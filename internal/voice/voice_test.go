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
		"Task k3f9qz: 3 agents, 12 files":      "Task K three F nine Q Z: three agents, twelve files",
		"Open the PR via the CLI":              "Open the P R via the C L I",
		"Round 2/10 · 1,250 tokens":            "Round two of ten, one thousand two hundred fifty tokens",
		"ackn4th uses 0.5 speed & a TTS voice": "acknowledge forth uses zero point five speed and a T T S voice",
		"NEW TASK — esc to close":              "new task, esc to close",
		"the SDKs":                             "the S D Ks",
		"2026":                                 "two thousand twenty six",
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
	os.WriteFile(lex, []byte("agent ˈ A ʤ ᵊ n t\nopen ˈ O p ᵊ n\nwant w ˈ ɑ n t\nfix f ˈ ɪ k s\nstop s t ˈ ɑ p\nmerge m ˈ ɜ ɹ ʤ\ntry t ɹ ˈ I\nnew n ˈ u\nk k ˈ A\nz z ˈ i\n"), 0o644)
	p, err := loadPhonemizer(lex)
	if err != nil {
		t.Fatal(err)
	}
	for w, want := range map[string]string{
		"Agents": "ˈAʤᵊnts", "opened": "ˈOpᵊnd", "wanted": "wˈɑntᵻd", "fixes": "fˈɪksᵻz", "stopped": "stˈɑpt",
		"merging": "mˈɜɹʤɪŋ", "tries": "tɹˈIz", "agent's": "ˈAʤᵊnts", "kz": "kˈA zˈi", "new-agent": "nˈu ˈAʤᵊnt",
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
