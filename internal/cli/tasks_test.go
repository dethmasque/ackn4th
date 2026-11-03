package cli

import (
	"errors"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/dethmasque/ackn4th/internal/config"
	"github.com/dethmasque/ackn4th/internal/task"
	"github.com/dethmasque/ackn4th/internal/tips"
	"github.com/dethmasque/ackn4th/internal/ui"
)

func TestParseAgent(t *testing.T) {
	r, err := parseAgent("backend-dev, provider=Claude, model=claude-sonnet-5-5, effort=MEDIUM, compact_at=150k, max_tokens=32000")
	if err != nil {
		t.Fatal(err)
	}
	if r.role != "backend_dev" || r.set.Provider != "claude" || r.set.Model != "claude-sonnet-5-5" ||
		r.set.Effort != "medium" || r.set.CompactAt != 150000 || r.set.MaxTokens != 32000 {
		t.Fatalf("parsed %+v", r)
	}
	if r, _ := parseAgent("role=qa_engineer,profile=fast"); r.role != "qa_engineer" || r.profile != "fast" {
		t.Fatalf("parsed %+v", r)
	}
	for _, bad := range []string{"", "model=x", "qa_engineer,temperature=1", "qa_engineer,oops", "qa_engineer,compact_at=lots"} {
		if _, err := parseAgent(bad); err == nil {
			t.Errorf("parseAgent(%q) should fail", bad)
		}
	}
	if n, _ := parseTokens("1.5m"); n != 1500000 {
		t.Errorf("1.5m = %d", n)
	}
}

func TestPick(t *testing.T) {
	opts := []option{{"claude", ""}, {"gemini", ""}, {"backend_dev", ""}}
	for in, want := range map[string]string{"1": "claude", "2": "gemini", "Gemini": "gemini", "backend-dev": "backend_dev"} {
		if got, ok := pick(in, opts); !ok || got != want {
			t.Errorf("pick(%q) = %q, %v; want %q", in, got, ok, want)
		}
	}
	for _, in := range []string{"0", "4", "openai"} {
		if _, ok := pick(in, opts); ok {
			t.Errorf("pick(%q) accepted", in)
		}
	}
}

func TestNoInputFailsFast(t *testing.T) {
	missing := errors.New("missing thing")
	for _, a := range []*app{{noInput: true}, {}} { // the flag; and tests have no terminal
		if _, err := a.ask(missing, "thing", nil); err != missing {
			t.Errorf("ask = %v, want the original error", err)
		}
		if _, err := a.choose(missing, "X", []option{{"a", ""}}, false); err != missing {
			t.Errorf("choose = %v, want the original error", err)
		}
		if err := a.confirm(missing, "ok?"); err != missing {
			t.Errorf("confirm = %v, want the original error", err)
		}
	}
	t.Setenv(NoInputEnv, "1")
	if (&app{}).interactive() {
		t.Error(NoInputEnv + "=1 ignored")
	}
}

func TestPlanSwitchToClaudeCode(t *testing.T) {
	a := &app{cfg: config.Default()}
	tk := &task.Task{Agents: []task.AgentSpec{
		{Role: "backend_dev", Provider: "claude", Model: "claude-opus-5-5", Effort: "high"},
		{Role: "qa_engineer", Provider: "gemini", Model: "gemini-3.1-pro-preview"},
		{Role: "ux_designer", Provider: "claude-code", Model: "claude-haiku-5-5", Effort: "low"},
	}}
	moves, err := a.planSwitch(tk, "claude-code")
	if err != nil {
		t.Fatal(err)
	}
	want := []move{{0, "claude-code", "claude-opus-5-5", "medium"}, {1, "claude-code", "claude-sonnet-5-5", "medium"}}
	if !slices.Equal(moves, want) {
		t.Errorf("moves = %v, want %v", moves, want)
	}
	if got := taskProviders(tk, moves); !slices.Equal(got, []string{"claude-code", "claude-code", "claude-code"}) {
		t.Errorf("providers = %v", got)
	}
}

// The home directory can't be trusted under any spelling of its path.
func TestTrustableRefusesHome(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil {
		t.Skip(err)
	}
	for _, d := range []string{home, home + string(filepath.Separator), filepath.Dir(home), strings.ToUpper(home), strings.ToLower(home)} {
		if _, err := os.Stat(d); err != nil {
			continue // another case doesn't exist on a case-sensitive filesystem
		}
		if _, err := trustable(d); err == nil {
			t.Errorf("trusted %s", d)
		}
	}
}

// The dashboard gets the provider-setup screen (its `p` key and first-run
// offer).
func TestDashOffersSetup(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	a := &app{cfg: config.Default(), term: ui.NewTerm(io.Discard, false), tips: tips.Load(t.TempDir(), true)}
	opt, err := a.dashOptions(t.Context(), "", nil)
	if err != nil {
		t.Fatal(err)
	}
	if opt.Setup == nil || opt.Setup.Status == nil || opt.Setup.Done == nil {
		t.Errorf("setup = %+v", opt.Setup)
	}
}
