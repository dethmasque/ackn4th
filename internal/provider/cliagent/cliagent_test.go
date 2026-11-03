package cliagent

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/dethmasque/ackn4th/internal/provider"
	"github.com/dethmasque/ackn4th/internal/sandbox"
)

// fake is a driver whose CLI is this test binary (see TestFakeCLI).
type fake struct{}

func (fake) Name() string                           { return "fake" }
func (fake) Bin() (string, error)                   { return os.Args[0], nil }
func (fake) Keep() []string                         { return nil }
func (fake) Keychain() bool                         { return false }
func (fake) Login(string) []string                  { return nil }
func (fake) Notes(string) string                    { return "" }
func (fake) Finish(Request, *Turn)                  {}
func (fake) Status(context.Context) (string, error) { return "ok", nil }

func (fake) Command(bin string, r Request) (Spec, error) {
	return Spec{
		Argv: []string{bin, "-test.run=^TestFakeCLI$"},
		Env:  []string{"FAKE_CLI=1", "FAKE_URL=" + r.MCPURL, "FAKE_TOKEN=" + r.MCPToken, "FAKE_RESUME=" + r.Resume, "FAKE_FIRST=" + fmt.Sprint(strings.HasPrefix(r.Prompt, "<ackn4th>"))},
	}, nil
}

func (fake) Parse(line []byte, t *Turn) {
	var ev struct{ T, Text, ID string }
	if json.Unmarshal(line, &ev) != nil {
		return
	}
	switch ev.T {
	case "session":
		t.SessionID = ev.ID
	case "text":
		t.Say(ev.Text)
	case "shell":
		t.Act("shell", ev.Text, false)
	case "done":
		t.Done = true
		t.Usage = provider.Usage{InputTokens: 10, OutputTokens: 2}
	case "fail":
		t.Error = ev.Text
	}
}

// TestFakeCLI is the fake CLI: it runs only when the provider starts it.
func TestFakeCLI(t *testing.T) {
	if os.Getenv("FAKE_CLI") != "1" {
		t.Skip("run by TestSession")
	}
	say := func(v map[string]string) { b, _ := json.Marshal(v); fmt.Println(string(b)) }
	say(map[string]string{"t": "session", "id": "s1"})
	if os.Getenv("FAKE_RESUME") != "" {
		say(map[string]string{"t": "text", "text": "resumed " + os.Getenv("FAKE_RESUME") + " first=" + os.Getenv("FAKE_FIRST")})
		say(map[string]string{"t": "done"})
		os.Exit(0)
	}
	os.WriteFile("made.txt", []byte("by the CLI\n"), 0o644)
	if err := os.WriteFile(filepath.Join(os.Getenv("HOME"), ".ackn4th-escape"), []byte("x"), 0o644); err == nil {
		say(map[string]string{"t": "text", "text": "ESCAPED THE SANDBOX"})
	}
	say(map[string]string{"t": "shell", "text": "touch made.txt"})
	body := `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"ping","arguments":{}}}`
	req, _ := http.NewRequest("POST", os.Getenv("FAKE_URL"), strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+os.Getenv("FAKE_TOKEN"))
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		say(map[string]string{"t": "fail", "text": err.Error()})
		os.Exit(1)
	}
	var out struct {
		Result struct {
			Content []struct{ Text string } `json:"content"`
		} `json:"result"`
	}
	json.NewDecoder(resp.Body).Decode(&out)
	say(map[string]string{"t": "text", "text": "got " + out.Result.Content[0].Text + " first=" + os.Getenv("FAKE_FIRST")})
	say(map[string]string{"t": "done"})
	os.Exit(0)
}

func TestSession(t *testing.T) {
	if err := sandbox.Available(); err != nil {
		t.Skip(err)
	}
	ws, scratch := t.TempDir(), t.TempDir()
	var credited []string
	p := New(fake{}, DefaultModel, "")
	p.Confine(provider.Confinement{
		Sandbox: sandbox.Config{Workspace: ws, Scratch: scratch},
		Credit: func(fn func()) []string {
			fn()
			if _, err := os.Stat(filepath.Join(ws, "made.txt")); err == nil && !slices.Contains(credited, "made.txt") {
				credited = append(credited, "made.txt")
			}
			return credited
		},
	})
	tools := []provider.ToolSpec{{Name: "ping", Schema: map[string]any{"type": "object"}}}
	s := p.NewSession(provider.System{Shared: "team rules"}, tools)
	var acts []string
	st := provider.Stream{Activity: func(tool, detail string, failed bool) { acts = append(acts, tool+" "+detail) }}

	r, err := s.SendText(context.Background(), "do it", st)
	if err != nil {
		t.Fatal(err)
	}
	if r.StopReason != provider.StopToolUse || len(r.ToolCalls) != 1 || r.ToolCalls[0].Name != "ping" {
		t.Fatalf("first reply = %+v", r)
	}
	r, err = s.SendToolResults(context.Background(), []provider.ToolResult{{CallID: r.ToolCalls[0].ID, Content: "pong"}}, "", st)
	if err != nil {
		t.Fatal(err)
	}
	if r.StopReason != provider.StopEnd || r.Text != "got pong first=true" || r.Usage.InputTokens != 10 {
		t.Fatalf("final reply = %+v", r)
	}
	if strings.Contains(r.Text, "ESCAPED") {
		t.Fatal("the CLI wrote outside the workspace")
	}
	if len(acts) != 1 || acts[0] != "shell touch made.txt" {
		t.Errorf("activity = %v", acts)
	}
	if len(credited) != 1 {
		t.Errorf("the CLI's file should be credited: %v", credited)
	}
	raw, _ := s.State()
	s2, err := p.RestoreSession(provider.System{}, tools, raw)
	if err != nil {
		t.Fatal(err)
	}
	r, err = s2.SendText(context.Background(), "again", st)
	if err != nil || r.Text != "resumed s1 first=false" {
		t.Fatalf("resume: %+v %v", r, err)
	}

	unconfined := New(fake{}, DefaultModel, "").NewSession(provider.System{}, tools)
	if _, err := unconfined.SendText(context.Background(), "x", st); err == nil || !strings.Contains(err.Error(), "sandbox") {
		t.Errorf("an unconfined CLI must refuse to run: %v", err)
	}
}
