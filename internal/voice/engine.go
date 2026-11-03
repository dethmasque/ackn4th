//go:build darwin || linux || windows

// The C structs below are mirrored whole, so the fields ackn4th sets sit at
// the offsets the library reads; the rest are never touched from Go.
//lint:file-ignore U1000 the structs mirror sherpa-onnx's C layout field for field

package voice

import (
	"errors"
	"fmt"
	"runtime"
	"sync"
	"unsafe"

	"github.com/ebitengine/purego"
)

// The engine is sherpa-onnx's C API, loaded from the downloaded shared
// library with purego (ackn4th is built without cgo). The model loads once
// and stays loaded, so each phrase costs only its synthesis.

// The structs mirror sherpa-onnx v2.0.1's c-api.h field for field; Go lays
// out pointers, int32 and float32 as C does on every platform ackn4th
// ships for.

type cstr = *byte

type vitsConfig struct {
	model, lexicon, tokens, dataDir cstr
	noiseScale, noiseScaleW         float32
	lengthScale                     float32
	dictDir                         cstr
}

type matchaConfig struct {
	acousticModel, vocoder, lexicon, tokens, dataDir cstr
	noiseScale, lengthScale                          float32
	dictDir                                          cstr
}

type kokoroConfig struct {
	model, voices, tokens, dataDir cstr
	lengthScale                    float32
	dictDir, lexicon, lang         cstr
}

type kittenConfig struct {
	model, voices, tokens, dataDir cstr
	lengthScale                    float32
	lexicon                        cstr
}

type zipvoiceConfig struct {
	tokens, encoder, decoder, vocoder, dataDir, lexicon cstr
	featScale, tShift, targetRMS, guidanceScale         float32
}

type pocketConfig struct {
	lmFlow, lmMain, encoder, decoder, textConditioner, vocabJSON, tokenScoresJSON cstr
	voiceEmbeddingCacheCapacity                                                   int32
}

type supertonicConfig struct {
	durationPredictor, textEncoder, vectorEstimator, vocoder, ttsJSON, unicodeIndexer, voiceStyle cstr
}

type modelConfig struct {
	vits       vitsConfig
	numThreads int32
	debug      int32
	provider   cstr
	matcha     matchaConfig
	kokoro     kokoroConfig
	kitten     kittenConfig
	zipvoice   zipvoiceConfig
	pocket     pocketConfig
	supertonic supertonicConfig
}

type ttsConfig struct {
	model           modelConfig
	ruleFsts        cstr
	maxNumSentences int32
	ruleFars        cstr
	silenceScale    float32
}

// generationConfig mirrors SherpaOnnxGenerationConfig.
type generationConfig struct {
	silenceScale, speed        float32
	sid                        int32
	referenceAudio             *float32
	referenceAudioLen, refRate int32
	referenceText              cstr
	numSteps                   int32
	extra                      cstr
	phonemes, phonemeLens      *int32
	numSentences               int32
	tokens                     **byte
	tokensLens                 *int32
	tokensNumSentences         int32
}

type generatedAudio struct {
	samples    *float32
	n          int32
	sampleRate int32
}

// Engine synthesizes speech with a loaded Kokoro model. It is safe for
// one goroutine at a time; Speaker serializes its use.
type Engine struct {
	mu      sync.Mutex
	tts     uintptr
	destroy func(uintptr)
	gen     func(tts uintptr, text cstr, cfg *generationConfig, callback, arg uintptr) *generatedAudio
	free    func(*generatedAudio)
	ph      *phonemizer
}

// cString makes a NUL-terminated copy of s; keep it alive while C reads it.
func cString(s string) cstr {
	b := append([]byte(s), 0)
	return &b[0]
}

// Load opens the engine and model installed in dir (see Install). It
// takes a few seconds: the model is read into memory.
func Load(dir string) (*Engine, error) {
	f, err := files(dir)
	if err != nil {
		return nil, err
	}
	ph, err := loadPhonemizer(f.lexicon)
	if err != nil {
		return nil, fmt.Errorf("voice: %w", err)
	}
	lib, err := openLibrary(f.lib)
	if err != nil {
		return nil, fmt.Errorf("voice: load %s: %w", f.lib, err)
	}
	e := &Engine{ph: ph}
	var create func(*ttsConfig) uintptr
	purego.RegisterLibFunc(&create, lib, "SherpaOnnxCreateOfflineTts")
	purego.RegisterLibFunc(&e.destroy, lib, "SherpaOnnxDestroyOfflineTts")
	purego.RegisterLibFunc(&e.gen, lib, "SherpaOnnxOfflineTtsGenerateWithConfig")
	purego.RegisterLibFunc(&e.free, lib, "SherpaOnnxDestroyOfflineTtsGeneratedAudio")

	// No lexicon: Synthesize passes phonemes (see phonemizer).
	strs := []cstr{cString(f.model), cString(f.voices), cString(f.tokens), cString("cpu")}
	cfg := ttsConfig{maxNumSentences: 1, silenceScale: 0.2}
	cfg.model.numThreads = int32(max(1, min(4, runtime.NumCPU()/2)))
	cfg.model.provider = strs[3]
	cfg.model.kokoro = kokoroConfig{model: strs[0], voices: strs[1], tokens: strs[2], lengthScale: 1}
	e.tts = create(&cfg)
	runtime.KeepAlive(strs)
	if e.tts == 0 {
		return nil, errors.New("voice: the engine couldn't load the model (reinstall it with `ackn4th voice install`)")
	}
	return e, nil
}

// Synthesize speaks text with voice sid at speed (1 is normal) and returns
// the samples, mono, and their sample rate. Normalize text first.
func (e *Engine) Synthesize(text string, sid int, speed float32) ([]float32, int, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.tts == 0 {
		return nil, 0, errors.New("voice: the engine is closed")
	}
	var cps, lens []int32
	for _, s := range e.ph.sentences(text) {
		n := 0
		for _, r := range s {
			cps = append(cps, r)
			n++
		}
		lens = append(lens, int32(n))
	}
	if len(lens) == 0 {
		return nil, 0, fmt.Errorf("voice: nothing to say in %q", text)
	}
	cfg := generationConfig{silenceScale: 0.2, speed: speed, sid: int32(sid),
		phonemes: &cps[0], phonemeLens: &lens[0], numSentences: int32(len(lens))}
	t := cString(text)
	a := e.gen(e.tts, t, &cfg, 0, 0)
	runtime.KeepAlive(t)
	runtime.KeepAlive(cps)
	runtime.KeepAlive(lens)
	if a == nil || a.n <= 0 || a.samples == nil {
		if a != nil {
			e.free(a)
		}
		return nil, 0, fmt.Errorf("voice: no audio for %q", text)
	}
	out := make([]float32, a.n)
	copy(out, unsafe.Slice(a.samples, a.n))
	rate := int(a.sampleRate)
	e.free(a)
	return out, rate, nil
}

// Close unloads the model.
func (e *Engine) Close() {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.tts != 0 {
		e.destroy(e.tts)
		e.tts = 0
	}
}
