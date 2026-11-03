// Package clis holds the cliagent drivers for Codex, Cursor and Copilot.
package clis

import (
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"strconv"
	"strings"

	"github.com/dethmasque/ackn4th/internal/provider/cliagent"
	"github.com/dethmasque/ackn4th/internal/provider/mcpbridge"
)

// Codex drives OpenAI's Codex CLI (`codex exec --json`) on the user's
// ChatGPT plan.
type Codex struct{}

const CodexName = "codex"

func (Codex) Name() string { return CodexName }

func (Codex) Bin() (string, error) {
	return cliagent.FindBin("codex",
		"/Applications/ChatGPT.app/Contents/Resources/codex-cli/bin/codex",
		"/Applications/Codex.app/Contents/Resources/codex",
		"~/.local/bin/codex")
}

func (Codex) Keep() []string { return []string{".codex"} }

// Keychain: Codex checks TLS certificates through macOS's security
// service, so with it blocked every HTTPS request fails ("workspace routing
// discovery failed").
func (Codex) Keychain() bool            { return true }
func (Codex) Login(bin string) []string { return []string{bin, "login"} }
func (Codex) Notes(string) string       { return "" }

// tokenEnv carries the MCP bearer token, so it isn't on the command line.
const tokenEnv = "ACKN4TH_MCP_TOKEN"

func (Codex) Command(bin string, r cliagent.Request) (cliagent.Spec, error) {
	argv := []string{bin, "exec", "--json", "--skip-git-repo-check",
		// Codex's own Seatbelt sandbox can't nest inside ackn4th's, which
		// confines the whole process instead.
		"--dangerously-bypass-approvals-and-sandbox",
		"-C", r.Workspace,
		"-c", "mcp_servers." + mcpbridge.Server + ".url=" + strconv.Quote(r.MCPURL),
		"-c", "mcp_servers." + mcpbridge.Server + ".bearer_token_env_var=" + strconv.Quote(tokenEnv),
		"-c", "mcp_servers." + mcpbridge.Server + ".default_tools_approval_mode=\"approve\"",
		// A call waits while the runner works; don't time it out.
		"-c", "mcp_servers." + mcpbridge.Server + ".tool_timeout_sec=86400",
	}
	if r.Model != "" && r.Model != cliagent.DefaultModel {
		argv = append(argv, "-m", r.Model)
	}
	if r.Effort != "" {
		e := r.Effort
		if e == "max" {
			e = "xhigh"
		}
		argv = append(argv, "-c", "model_reasoning_effort="+strconv.Quote(e))
	}
	if r.Resume != "" {
		argv = append(argv, "resume", r.Resume)
	}
	argv = append(argv, "--", r.Prompt)
	return cliagent.Spec{Argv: argv, Env: []string{tokenEnv + "=" + r.MCPToken}}, nil
}

type codexEvent struct {
	Type     string `json:"type"`
	ThreadID string `json:"thread_id"`
	Message  string `json:"message"`
	Error    *struct {
		Message string `json:"message"`
	} `json:"error"`
	Usage *struct {
		Input  int64 `json:"input_tokens"`
		Cached int64 `json:"cached_input_tokens"`
		Output int64 `json:"output_tokens"`
	} `json:"usage"`
	Item struct {
		Type     string `json:"type"`
		Text     string `json:"text"`
		Command  string `json:"command"`
		ExitCode *int   `json:"exit_code"`
		Status   string `json:"status"`
		Server   string `json:"server"`
		Tool     string `json:"tool"`
		Query    string `json:"query"`
		Changes  []struct {
			Path string `json:"path"`
			Kind string `json:"kind"`
		} `json:"changes"`
		Error *struct {
			Message string `json:"message"`
		} `json:"error"`
	} `json:"item"`
}

// shellCommand drops the shell wrapper Codex reports: `/bin/zsh -lc ls`.
func shellCommand(c string) string {
	for _, sep := range []string{" -lc ", " -c "} {
		if i := strings.Index(c, sep); i >= 0 && !strings.Contains(c[:i], " ") {
			return strings.Trim(c[i+len(sep):], `'"`)
		}
	}
	return c
}

func (Codex) Parse(line []byte, t *cliagent.Turn) {
	var ev codexEvent
	if json.Unmarshal(line, &ev) != nil {
		return
	}
	it := ev.Item
	switch ev.Type {
	case "thread.started":
		t.SessionID = ev.ThreadID
	case "item.started":
		if it.Type == "command_execution" {
			t.Act("shell", shellCommand(it.Command), false)
		}
	case "item.completed":
		switch it.Type {
		case "agent_message":
			if t.Stream.Text != nil {
				t.Stream.Text(it.Text + "\n\n")
			}
			t.Say(it.Text)
		case "reasoning":
			if t.Stream.Thinking != nil {
				t.Stream.Thinking(it.Text)
			}
		case "command_execution":
			if it.ExitCode != nil && *it.ExitCode != 0 {
				t.Act("shell", fmt.Sprintf("exit %d: %s", *it.ExitCode, shellCommand(it.Command)), true)
			}
		case "file_change":
			var paths []string
			for _, c := range it.Changes {
				paths = append(paths, c.Path)
			}
			t.Act("edit", strings.Join(paths, ", "), it.Status == "failed")
		case "mcp_tool_call":
			if it.Server != mcpbridge.Server {
				t.Act(it.Server+"."+it.Tool, "", it.Error != nil)
			}
		case "web_search":
			t.Act("web_search", it.Query, false)
		case "error":
			t.Act("codex", it.Text, true)
		}
	case "turn.completed":
		t.Done = true
		if u := ev.Usage; u != nil {
			t.Usage.InputTokens += u.Input
			t.Usage.CachedTokens += u.Cached
			t.Usage.OutputTokens += u.Output
		}
	case "turn.failed":
		if ev.Error != nil {
			t.Error = ev.Error.Message
		} else {
			t.Error = "the turn failed"
		}
	case "error":
		// Also used for transient notices (reconnecting); only fatal if
		// the turn never completes.
		if !t.Done && ev.Message != "" && t.Error == "" && !strings.Contains(strings.ToLower(ev.Message), "reconnect") {
			t.Error = ev.Message
		}
	}
}

func (Codex) Finish(cliagent.Request, *cliagent.Turn) {}

// Status runs `codex login status`.
func (c Codex) Status(ctx context.Context) (string, error) {
	bin, err := c.Bin()
	if err != nil {
		return "", err
	}
	out, err := exec.CommandContext(ctx, bin, "login", "status").CombinedOutput()
	msg := strings.TrimSpace(string(out))
	if err != nil || !strings.Contains(strings.ToLower(msg), "logged in") || strings.Contains(strings.ToLower(msg), "not logged in") {
		return "", fmt.Errorf("codex: not signed in (%s): run `codex login`", cliagent.OneLine(msg, 120))
	}
	return msg, nil
}
