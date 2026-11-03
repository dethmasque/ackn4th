package voice

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
)

// paths are the installed engine's and model's files.
type paths struct {
	lib, model, voices, tokens, lexicon string
}

// libName is the C API library's file name on this OS.
func libName() string {
	switch runtime.GOOS {
	case "darwin":
		return "libsherpa-onnx-c-api.dylib"
	case "windows":
		return "sherpa-onnx-c-api.dll"
	}
	return "libsherpa-onnx-c-api.so"
}

// ErrNotInstalled means the voice's engine or model isn't downloaded yet.
var ErrNotInstalled = errors.New("voice: not installed: run `ackn4th voice install` (or turn the voice on in the dashboard)")

func files(dir string) (paths, error) {
	m := filepath.Join(dir, "model")
	p := paths{
		lib:    filepath.Join(dir, "engine", libName()),
		model:  filepath.Join(m, modelFile),
		voices: filepath.Join(m, "voices.bin"), tokens: filepath.Join(m, "tokens.txt"),
		lexicon: filepath.Join(m, "lexicon-us-en.txt"),
	}
	for _, f := range []string{p.lib, p.model, p.voices, p.tokens, p.lexicon} {
		if _, err := os.Stat(f); err != nil {
			return p, ErrNotInstalled
		}
	}
	return p, nil
}
