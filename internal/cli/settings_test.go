package cli

import (
	"io"
	"os"
	"strings"
	"testing"

	"github.com/dethmasque/ackn4th/internal/config"
	"github.com/dethmasque/ackn4th/internal/dash"
	"github.com/dethmasque/ackn4th/internal/tips"
	"github.com/dethmasque/ackn4th/internal/ui"
)

// The dashboard's configuration saves one key at a time, keeping the
// file's comments, checks values first, and applies them at once.
func TestSaveSetting(t *testing.T) {
	t.Setenv(config.HomeEnv, t.TempDir())
	path, _ := config.Path()
	os.MkdirAll(strings.TrimSuffix(path, "config.yaml"), 0o700)
	os.WriteFile(path, []byte("# mine\ntips: true\n"), 0o600)
	a := &app{cfg: config.Default(), term: ui.NewTerm(io.Discard, false), tips: tips.Load(t.TempDir(), true)}

	for _, c := range []struct{ key, in, shown string }{
		{"spend.daily", "$20", "20"},
		{"spend.per_task", "", ""},
		{"spend.providers.gemini.monthly", "50", "50"},
		{"commands.network", "true", "true"},
		{"limits.max_rounds", "0", "0"},
		{"providers.claude.effort", "low", "low"},
		{"providers.claude.model", "", "claude-opus-5-5"},
		{"commands.timeout", "90s", "1m30s"},
		{"voice.name", "bm_george", "bm_george"},
		{"voice.speed", "1.25", "1.25"},
	} {
		got, err := a.saveSetting(c.key, c.in)
		if err != nil || got != c.shown {
			t.Errorf("%s = %q: got %q, %v", c.key, c.in, got, err)
		}
	}
	if a.cfg.Spend.Daily != 20 || a.cfg.Spend.Providers["gemini"].Monthly != 50 || !a.cfg.Commands.Network || a.cfg.Voice.Name != "bm_george" {
		t.Errorf("not applied: %+v", a.cfg)
	}
	data, _ := os.ReadFile(path)
	if !strings.HasPrefix(string(data), "# mine") {
		t.Errorf("the file's comment went:\n%s", data)
	}

	for _, c := range [][2]string{
		{"spend.daily", "-5"}, {"spend.daily", "lots"}, {"limits.max_steps_per_turn", "0"}, {"providers.claude.effort", "huge"},
		{"commands.timeout", "soon"}, {"voice.speed", "3"}, {"voice.name", "hal"}, {"default_provider", "nope"},
		{"session_ttl", "1h"}, // gone: sign-ins last until logout
	} {
		if _, err := a.saveSetting(c[0], c[1]); err == nil {
			t.Errorf("%s = %q was accepted", c[0], c[1])
		}
	}
	if a.cfg.Spend.Daily != 20 {
		t.Error("a rejected value changed the config")
	}

	// Every setting offered can be saved as it is.
	for _, s := range a.settings() {
		if s.Kind == dash.Toggle || s.Value == "" {
			continue
		}
		if _, err := a.saveSetting(s.Key, s.Value); err != nil {
			t.Errorf("%s = %q: %v", s.Key, s.Value, err)
		}
	}
}
