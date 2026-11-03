package clis

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/dethmasque/ackn4th/internal/provider/cliagent"
	"github.com/dethmasque/ackn4th/internal/provider/mcpbridge"
)

// Cursor drives the Cursor CLI (`cursor-agent -p --output-format
// stream-json`) on the user's Cursor plan.
//
// Cursor reads MCP servers and permissions only from files: the
// project's .cursor/ and ~/.cursor/. So that neither the user's project
// nor their global config is touched, the CLI runs from a private
// directory holding those files, with the real workspace added as a
// second root (--add-dir).
type Cursor struct{}

const CursorName = "cursor"

func (Cursor) Name() string { return CursorName }

func (Cursor) Bin() (string, error) {
	return cliagent.FindBin("cursor-agent", "~/.local/bin/cursor-agent", "~/.local/bin/agent")
}

// Cursor keeps its login in the keychain and its state under ~/.cursor.
func (Cursor) Keep() []string { return []string{".cursor", ".local/share/cursor-agent"} }
func (Cursor) Keychain() bool { return true }

func (Cursor) Login(bin string) []string { return []string{bin, "login"} }

func (Cursor) Notes(ws string) string {
	return "Cursor's working directory is a private scratch folder. The project you are working on is " + ws +
		": use absolute paths under it, and `cd " + ws + "` before running commands.\n"
}

// cursorPerms lets Cursor use its own tools and ackn4th's without asking:
// print mode can't ask, and ackn4th's sandbox is what confines them.
var cursorPerms = map[string]any{"permissions": map[string]any{
	"allow": []string{"Mcp(" + mcpbridge.Server + ":*)", "Shell(*)", "Read(**)", "Write(**)"},
	"deny":  []string{},
}}

func (Cursor) Command(bin string, r cliagent.Request) (cliagent.Spec, error) {
	dir := filepath.Join(r.RunDir, ".cursor")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return cliagent.Spec{}, err
	}
	mcp, _ := json.Marshal(map[string]any{"mcpServers": map[string]any{mcpbridge.Server: map[string]any{
		"url": r.MCPURL, "headers": map[string]string{"Authorization": "Bearer " + r.MCPToken},
	}}})
	perms, _ := json.Marshal(cursorPerms)
	for name, data := range map[string][]byte{"mcp.json": mcp, "cli.json": perms} {
		if err := os.WriteFile(filepath.Join(dir, name), data, 0o600); err != nil {
			return cliagent.Spec{}, err
		}
	}
	argv := []string{bin, "-p", "--output-format", "stream-json", "--trust", "--approve-mcps",
		// Cursor's own sandbox can't nest inside ackn4th's.
		"--sandbox", "disabled",
		"--workspace", r.RunDir, "--add-dir", r.Workspace}
	if r.Model != "" && r.Model != cliagent.DefaultModel {
		argv = append(argv, "--model", r.Model)
	}
	if r.Resume != "" {
		argv = append(argv, "--resume", r.Resume)
	}
	argv = append(argv, "--", r.Prompt)
	return cliagent.Spec{Argv: argv, Dir: r.RunDir}, nil
}

type cursorEvent struct {
	Type      string `json:"type"`
	Subtype   string `json:"subtype"`
	SessionID string `json:"session_id"`
	Text      string `json:"text"`
	IsError   bool   `json:"is_error"`
	Result    string `json:"result"`
	Message   struct {
		Content []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
	} `json:"message"`
	ToolCall map[string]json.RawMessage `json:"tool_call"`
	Usage    *struct {
		Input      int64 `json:"inputTokens"`
		Output     int64 `json:"outputTokens"`
		CacheRead  int64 `json:"cacheReadTokens"`
		CacheWrite int64 `json:"cacheWriteTokens"`
	} `json:"usage"`
}

type cursorTool struct {
	Args   map[string]any `json:"args"`
	Result map[string]any `json:"result"`
}

// cursorDetail summarizes a tool call's arguments.
func cursorDetail(args map[string]any) string {
	for _, k := range []string{"command", "path", "globPattern", "pattern", "query", "url", "name"} {
		if v, ok := args[k].(string); ok && v != "" {
			return v
		}
	}
	return ""
}

func (Cursor) Parse(line []byte, t *cliagent.Turn) {
	var ev cursorEvent
	if json.Unmarshal(line, &ev) != nil {
		return
	}
	if ev.SessionID != "" {
		t.SessionID = ev.SessionID
	}
	switch ev.Type {
	case "thinking":
		if ev.Subtype == "delta" && t.Stream.Thinking != nil {
			t.Stream.Thinking(ev.Text)
		}
	case "assistant":
		var b strings.Builder
		for _, c := range ev.Message.Content {
			if c.Type == "text" {
				b.WriteString(c.Text)
			}
		}
		if t.Stream.Text != nil {
			t.Stream.Text(b.String() + "\n\n")
		}
		t.Say(b.String())
	case "tool_call":
		for key, raw := range ev.ToolCall {
			name := strings.TrimSuffix(key, "ToolCall")
			if name == "getMcpTools" {
				continue
			}
			var tc cursorTool
			json.Unmarshal(raw, &tc)
			detail := cursorDetail(tc.Args)
			if name == "mcp" && strings.HasPrefix(detail, mcpbridge.Server+"-") {
				continue // ackn4th's own tools are shown as they run
			}
			switch ev.Subtype {
			case "started":
				t.Act(name, detail, false)
			case "completed":
				if e, ok := tc.Result["error"]; ok {
					b, _ := json.Marshal(e)
					t.Act(name, detail+": "+string(b), true)
				} else if f, ok := tc.Result["failure"]; ok {
					b, _ := json.Marshal(f)
					t.Act(name, detail+": "+string(b), true)
				}
			}
		}
	case "result":
		if ev.IsError || (ev.Subtype != "" && ev.Subtype != "success") {
			t.Error = cliagent.OneLine(ev.Result, 400)
			if t.Error == "" {
				t.Error = "the request failed (" + ev.Subtype + ")"
			}
			return
		}
		t.Done = true
		if u := ev.Usage; u != nil {
			t.Usage.InputTokens += u.Input + u.CacheRead + u.CacheWrite
			t.Usage.CachedTokens += u.CacheRead
			t.Usage.OutputTokens += u.Output
			t.Context = u.Input + u.CacheRead + u.CacheWrite
		}
	}
}

func (Cursor) Finish(cliagent.Request, *cliagent.Turn) {}

var ansiRe = regexp.MustCompile(`\x1b\[[0-9;?]*[A-Za-z]`)

// Status runs `cursor-agent status`.
func (c Cursor) Status(ctx context.Context) (string, error) {
	bin, err := c.Bin()
	if err != nil {
		return "", err
	}
	cmd := exec.CommandContext(ctx, bin, "status")
	cmd.Env = append(os.Environ(), "NO_COLOR=1")
	out, _ := cmd.CombinedOutput()
	msg := strings.TrimSpace(ansiRe.ReplaceAllString(string(out), ""))
	low := strings.ToLower(msg)
	if !strings.Contains(low, "logged in") || strings.Contains(low, "not logged in") {
		return "", errors.New("cursor: not signed in: run `cursor-agent login`")
	}
	for _, l := range strings.Split(msg, "\n") {
		if strings.Contains(strings.ToLower(l), "logged in") {
			return strings.TrimSpace(strings.TrimLeft(l, "✓ ")), nil
		}
	}
	return msg, fmt.Errorf("cursor: unexpected status output")
}
