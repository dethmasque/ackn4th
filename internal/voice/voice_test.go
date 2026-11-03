package voice

import (
	"bytes"
	"context"
	"encoding/binary"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestNormalize(t *testing.T) {
	for in, want := range map[string]string{
		"Task k3f9qz: 3 agents, 12 files":        "Task K three F nine Q Z: three agents, twelve files",
		"Open the PR via the CLI":                "Open the P R via the C L I",
		"Round 2/10 · 1,250 tokens":              "Round two of ten, one thousand two hundred fifty tokens",
		"ackn4th uses 0.5 speed & a TTS voice":   "acknowledge forth uses zero point five speed and a T T S voice",
		"↑↓ picks one, 1-4 switch tabs, ⏎ zooms": "up and down picks one, one to four switch tabs, enter zooms",
		"task Add tests, and…, running":          "task Add tests, and, running",
		"NEW TASK — esc to close":                "new task, esc to close",
		"the SDKs":                               "the S D Ks",
		"2026":                                   "two thousand twenty six",
	} {
		if got := Normalize(in); got != want {
			t.Errorf("Normalize(%q)\n got %q\nwant %q", in, got, want)
		}
	}
	if SpeakerID(DefaultVoice) != 3 || SpeakerID("nope") != -1 {
		t.Error("speaker IDs")
	}
}

func TestWAV(t *testing.T) {
	var b bytes.Buffer
	if err := WriteWAV(&b, []float32{0, 1, -1, 2}, 24000); err != nil {
		t.Fatal(err)
	}
	d := b.Bytes()
	if len(d) != 44+8 || string(d[:4]) != "RIFF" || string(d[8:12]) != "WAVE" || binary.LittleEndian.Uint32(d[24:]) != 24000 {
		t.Fatalf("header % x", d[:44])
	}
	if v := int16(binary.LittleEndian.Uint16(d[46:])); v != 32767 {
		t.Errorf("1.0 = %d", v)
	}
	if v := int16(binary.LittleEndian.Uint16(d[50:])); v != 32767 {
		t.Errorf("clipped 2.0 = %d", v)
	}
}

func TestChecksumRefused(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write([]byte("tampered")) }))
	defer srv.Close()
	err := fetch(context.Background(), srv.URL, asset{name: "x.tar.bz2", sha256: strings.Repeat("0", 64)}, nil)
	if err == nil || !strings.Contains(err.Error(), "checksum") {
		t.Fatalf("err = %v", err)
	}
}

// With ACKN4TH_VOICE_ASSETS naming a directory holding the pinned
// archives, the real install and voice are tried (serving them locally).
func TestInstallAndSpeak(t *testing.T) {
	assets, dir := os.Getenv("ACKN4TH_VOICE_ASSETS"), os.Getenv("ACKN4TH_VOICE_DIR")
	switch {
	case dir != "": // already installed there
	case assets != "":
		srv := httptest.NewServer(http.FileServer(http.Dir(assets)))
		defer srv.Close()
		oe, om := engineBase, modelBase
		engineBase, modelBase = srv.URL+"/", srv.URL+"/"
		defer func() { engineBase, modelBase = oe, om }()
		dir = t.TempDir()
		if err := Install(context.Background(), dir, func(s string) { t.Log(s) }); err != nil {
			t.Fatal(err)
		}
	default:
		t.Skip("set ACKN4TH_VOICE_ASSETS to a directory with the downloaded archives, or ACKN4TH_VOICE_DIR to an install")
	}
	start := time.Now()
	e, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer e.Close()
	t.Logf("loaded in %v", time.Since(start))
	for _, s := range []string{"New task.", "Task k3f9qz: 3 agents are working.", "Draft pull request opened.", "The agents reviewed, refactored and merged everything."} {
		start = time.Now()
		samples, rate, err := e.Synthesize(Normalize(s), 3, 1)
		if err != nil {
			t.Fatal(err)
		}
		secs := float64(len(samples)) / float64(rate)
		t.Logf("%q: %.1fs of audio in %v", s, secs, time.Since(start))
		if secs < 0.3 {
			t.Errorf("%q: too short", s)
		}
		if os.Getenv("ACKN4TH_VOICE_PLAY") != "" {
			f := filepath.Join(t.TempDir(), "t.wav")
			w, _ := os.Create(f)
			WriteWAV(w, samples, rate)
			w.Close()
			Play(context.Background(), f)
		}
	}
}

func TestPhonemizer(t *testing.T) {
	lex := filepath.Join(t.TempDir(), "lex.txt")
	os.WriteFile(lex, []byte("agent ˈ A ʤ ᵊ n t\nopen ˈ O p ᵊ n\nwant w ˈ ɑ n t\nfix f ˈ ɪ k s\nstop s t ˈ ɑ p\nmerge m ˈ ɜ ɹ ʤ\ntry t ɹ ˈ I\nnew n ˈ u\nk k ˈ A\nz z ˈ i\nback b ˈ æ k\nend ˈ ɛ n d\n"), 0o644)
	p, err := loadPhonemizer(lex)
	if err != nil {
		t.Fatal(err)
	}
	for w, want := range map[string]string{
		"Agents": "ˈAʤᵊnts", "opened": "ˈOpᵊnd", "wanted": "wˈɑntᵻd", "fixes": "fˈɪksᵻz", "stopped": "stˈɑpt",
		"merging": "mˈɜɹʤɪŋ", "tries": "tɹˈIz", "agent's": "ˈAʤᵊnts", "kz": "kˈA zˈi", "new-agent": "nˈu ˈAʤᵊnt",
		"backend": "bˈækˌɛnd", "backends": "bˈækˌɛndz", "dev": "dˈɛv", "devs": "dˈɛvz", "zend": "zˈi",
	} {
		if got := p.word(w); got != want {
			t.Errorf("%s = %q, want %q", w, got, want)
		}
	}
	got := p.sentences("New agents, opened. Stop!")
	if len(got) != 2 || got[0] != "nˈu ˈAʤᵊnts, ˈOpᵊnd." || got[1] != "stˈɑp!" {
		t.Errorf("sentences = %q", got)
	}
}

func TestChunks(t *testing.T) {
	long := strings.Repeat("Agent two is idle. ", 20)
	got := chunks("Task toilet rush, done. " + long + "Tab opens it")
	if got[0] != "Task toilet rush, done." {
		t.Errorf("the first sentence should come alone, so speech starts at once: %q", got[0])
	}
	if last := got[len(got)-1]; !strings.HasSuffix(last, "Tab opens it") {
		t.Errorf("the end was lost: %q", last)
	}
	if strings.Join(got, " ") != strings.TrimSpace("Task toilet rush, done. "+long+"Tab opens it") {
		t.Error("chunks dropped or changed text")
	}
	for _, c := range got[1:] {
		if len(c) > chunkMax+len("Agent two is idle.") {
			t.Errorf("a chunk of %d characters", len(c))
		}
	}
	if chunks("") != nil || len(chunks("one point five")) != 1 {
		t.Error("short text should be one chunk")
	}
}

// The phrase cache stays under its cap, keeping the most recently used.
func TestPrune(t *testing.T) {
	dir := t.TempDir()
	now := time.Now()
	for i, name := range []string{"old.wav", "mid.wav", "new.wav", "stale.part", "fresh.part"} {
		p := filepath.Join(dir, name)
		os.WriteFile(p, make([]byte, 100), 0o644)
		age := time.Duration(5-i) * time.Hour
		if name == "fresh.part" {
			age = time.Minute
		}
		os.Chtimes(p, now.Add(-age), now.Add(-age))
	}
	prune(dir, 250)
	for name, keep := range map[string]bool{"old.wav": false, "mid.wav": true, "new.wav": true, "stale.part": false, "fresh.part": true} {
		_, err := os.Stat(filepath.Join(dir, name))
		if (err == nil) != keep {
			t.Errorf("%s: kept %v, want %v", name, err == nil, keep)
		}
	}
}
