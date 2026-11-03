package clis

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"

	"github.com/dethmasque/ackn4th/internal/provider/cliagent"
	"github.com/dethmasque/ackn4th/internal/provider/mcpbridge"
)

// Copilot drives the GitHub Copilot CLI (`copilot -p --output-format
// json`) on the user's Copilot plan.
type Copilot struct{}

const CopilotName = "copilot"

func (Copilot) Name() string { return CopilotName }

func (Copilot) Bin() (string, error) { return cliagent.FindBin("copilot") }

// Copilot keeps its login in the keychain and its state in ~/.copilot.
func (Copilot) Keep() []string { return []string{".copilot"} }
func (Copilot) Keychain() bool { return true }

func (Copilot) Login(bin string) []string { return []string{bin, "login"} }
func (Copilot) Notes(string) string       { return "" }

func (Copilot) Command(bin string, r cliagent.Request) (cliagent.Spec, error) {
	mcp, _ := json.Marshal(map[string]any{"mcpServers": map[string]any{mcpbridge.Server: map[string]any{
		"type": "http", "url": r.MCPURL, "tools": []string{"*"},
		"headers": map[string]string{"Authorization": "Bearer " + r.MCPToken},
	}}})
	argv := []string{bin, "-p", r.Prompt, "--output-format", "json", "--no-color", "--no-auto-update",
		// Print mode can't ask; ackn4th's sandbox confines the tools.
		"--allow-all-tools", "--no-ask-user",
		// No GitHub MCP server acting with the user's GitHub token, and
		// ackn4th's brief already carries the project's instructions.
		"--disable-builtin-mcps", "--no-custom-instructions",
		"--additional-mcp-config", string(mcp),
		"--usage-output-file", filepath.Join(r.RunDir, "usage.json"),
	}
	if r.Model != "" && r.Model != cliagent.DefaultModel {
		argv = append(argv, "--model", r.Model)
	}
	if r.Effort != "" {
		argv = append(argv, "--reasoning-effort", r.Effort)
	}
	if r.Resume != "" {
		argv = append(argv, "--resume", r.Resume)
	}
	return cliagent.Spec{Argv: argv}, nil
}

type copilotEvent struct {
	Type      string `json:"type"`
	SessionID string `json:"sessionId"`
	ExitCode  *int   `json:"exitCode"`
	Data      struct {
		DeltaContent  string         `json:"deltaContent"`
		Content       string         `json:"content"`
		Message       string         `json:"message"`
		ToolName      string         `json:"toolName"`
		MCPServerName string         `json:"mcpServerName"`
		Arguments     map[string]any `json:"arguments"`
		Success       *bool          `json:"success"`
		Error         any            `json:"error"`
	} `json:"data"`
}

func (Copilot) Parse(line []byte, t *cliagent.Turn) {
	var ev copilotEvent
	if json.Unmarshal(line, &ev) != nil {
		return
	}
	d := ev.Data
	switch ev.Type {
	case "assistant.message_delta":
		if t.Stream.Text != nil {
			t.Stream.Text(d.DeltaContent)
		}
	case "assistant.reasoning_delta":
		if t.Stream.Thinking != nil {
			t.Stream.Thinking(d.DeltaContent)
		}
	case "assistant.message":
		if t.Stream.Text != nil && d.Content != "" {
			t.Stream.Text("\n\n")
		}
		t.Say(d.Content)
	case "tool.execution_start":
		if d.MCPServerName != mcpbridge.Server {
			t.Act(d.ToolName, cursorDetail(d.Arguments), false)
		}
	case "tool.execution_complete":
		if d.Success != nil && !*d.Success && d.MCPServerName != mcpbridge.Server {
			b, _ := json.Marshal(d.Error)
			t.Act(cmpOr(d.ToolName, "tool"), string(b), true)
		}
	case "session.error":
		t.Error = cmpOr(d.Message, "the session failed")
	case "result":
		t.SessionID = cmpOr(ev.SessionID, t.SessionID)
		if ev.ExitCode != nil && *ev.ExitCode != 0 {
			t.Error = cmpOr(t.Error, "copilot exited with an error")
			return
		}
		t.Done = true
	}
}

// Finish reads the run's token usage from the report Copilot writes.
func (Copilot) Finish(r cliagent.Request, t *cliagent.Turn) {
	path := filepath.Join(r.RunDir, "usage.json")
	data, err := os.ReadFile(path)
	os.Remove(path)
	if err != nil {
		return
	}
	var u struct {
		ModelMetrics map[string]struct {
			Usage struct {
				Input     int64 `json:"inputTokens"`
				Output    int64 `json:"outputTokens"`
				CacheRead int64 `json:"cacheReadTokens"`
			} `json:"usage"`
		} `json:"modelMetrics"`
	}
	if json.Unmarshal(data, &u) != nil {
		return
	}
	for _, m := range u.ModelMetrics {
		t.Usage.InputTokens += m.Usage.Input
		t.Usage.OutputTokens += m.Usage.Output
		t.Usage.CachedTokens += m.Usage.CacheRead
	}
}

// Status reads the signed-in accounts Copilot records in
// ~/.copilot/config.json (it has no status command, and a real request
// would spend a premium request).
func (Copilot) Status(ctx context.Context) (string, error) {
	if _, err := (Copilot{}).Bin(); err != nil {
		return "", err
	}
	if os.Getenv("COPILOT_GITHUB_TOKEN") != "" {
		return "token from COPILOT_GITHUB_TOKEN", nil
	}
	home, _ := os.UserHomeDir()
	dir := cmpOr(os.Getenv("COPILOT_HOME"), filepath.Join(home, ".copilot"))
	data, err := os.ReadFile(filepath.Join(dir, "config.json"))
	if err != nil {
		return "", errors.New("copilot: not signed in: run `copilot login`")
	}
	// The file starts with // comments.
	var b bytes.Buffer
	sc := bufio.NewScanner(bytes.NewReader(data))
	for sc.Scan() {
		if !strings.HasPrefix(strings.TrimSpace(sc.Text()), "//") {
			b.Write(sc.Bytes())
			b.WriteByte('\n')
		}
	}
	var cfg struct {
		Users []struct {
			Host  string `json:"host"`
			Login string `json:"login"`
		} `json:"loggedInUsers"`
	}
	if json.Unmarshal(b.Bytes(), &cfg) != nil || len(cfg.Users) == 0 {
		return "", errors.New("copilot: not signed in: run `copilot login`")
	}
	u := cfg.Users[0]
	return u.Login + " · " + strings.TrimPrefix(u.Host, "https://"), nil
}

func cmpOr(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}
