package clis

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/dethmasque/ackn4th/internal/provider"
	"github.com/dethmasque/ackn4th/internal/provider/cliagent"
)

type recorded struct {
	acts []string
	text strings.Builder
}

func turnFor(r *recorded) *cliagent.Turn {
	return &cliagent.Turn{Stream: provider.Stream{
		Text: func(s string) { r.text.WriteString(s) },
		Activity: func(tool, detail string, failed bool) {
			mark := ""
			if failed {
				mark = "!"
			}
			r.acts = append(r.acts, mark+tool+" "+detail)
		},
	}}
}

func feed(d cliagent.Driver, t *cliagent.Turn, lines ...string) {
	for _, l := range lines {
		d.Parse([]byte(l), t)
	}
}

// Lines below were captured from the real CLIs (trimmed).

func TestCodexParse(t *testing.T) {
	var r recorded
	turn := turnFor(&r)
	feed(Codex{}, turn,
		`{"type":"thread.started","thread_id":"01a1989c"}`,
		`{"type":"turn.started"}`,
		`{"type":"item.started","item":{"id":"item_0","type":"command_execution","command":"/bin/zsh -lc ls","aggregated_output":"","exit_code":null,"status":"in_progress"}}`,
		`{"type":"item.completed","item":{"id":"item_0","type":"command_execution","command":"/bin/zsh -lc ls","aggregated_output":"a.txt\n","exit_code":0,"status":"completed"}}`,
		`{"type":"item.started","item":{"id":"item_1","type":"mcp_tool_call","server":"ackn4th","tool":"ping","arguments":{},"status":"in_progress"}}`,
		`{"type":"item.completed","item":{"id":"item_1","type":"mcp_tool_call","server":"ackn4th","tool":"ping","arguments":{},"result":{"content":[]},"error":null,"status":"completed"}}`,
		`{"type":"item.completed","item":{"id":"item_2","type":"agent_message","text":"a.txt"}}`,
		`{"type":"turn.completed","usage":{"input_tokens":29899,"cached_input_tokens":14720,"cache_write_input_tokens":0,"output_tokens":39,"reasoning_output_tokens":0}}`,
	)
	if turn.SessionID != "01a1989c" || !turn.Done || turn.Text.String() != "a.txt" {
		t.Errorf("turn = %+v", turn)
	}
	if turn.Usage != (provider.Usage{InputTokens: 29899, CachedTokens: 14720, OutputTokens: 39}) {
		t.Errorf("usage = %+v", turn.Usage)
	}
	if !slices.Equal(r.acts, []string{"shell ls"}) {
		t.Errorf("acts = %v", r.acts)
	}
	failed := turnFor(&r)
	feed(Codex{}, failed, `{"type":"turn.failed","error":{"message":"usage limit reached"}}`)
	if failed.Error != "usage limit reached" {
		t.Errorf("turn.failed: %+v", failed)
	}
}

func TestCodexCommand(t *testing.T) {
	spec, _ := Codex{}.Command("codex", cliagent.Request{Prompt: "-p looks like a flag", Resume: "abc", Model: cliagent.DefaultModel, Effort: "max", MCPURL: "http://127.0.0.1:1/mcp", MCPToken: "s3cr3t-9f", Workspace: "/ws"})
	argv := strings.Join(spec.Argv, " ")
	for _, want := range []string{"exec --json", "-C /ws", `mcp_servers.ackn4th.url="http://127.0.0.1:1/mcp"`, `model_reasoning_effort="xhigh"`, "resume abc -- -p looks like a flag"} {
		if !strings.Contains(argv, want) {
			t.Errorf("argv lacks %q: %s", want, argv)
		}
	}
	if strings.Contains(argv, "s3cr3t-9f") || !slices.Contains(spec.Env, "ACKN4TH_MCP_TOKEN=s3cr3t-9f") {
		t.Errorf("the token belongs in the environment, not argv: %s", argv)
	}
	if strings.Contains(argv, " -m ") {
		t.Error("the default model passes no -m")
	}
}

func TestCursorParse(t *testing.T) {
	var r recorded
	turn := turnFor(&r)
	feed(Cursor{}, turn,
		`{"type":"system","subtype":"init","apiKeySource":"login","session_id":"53c6","model":"Auto"}`,
		`{"type":"thinking","subtype":"delta","text":"The","session_id":"53c6"}`,
		`{"type":"tool_call","subtype":"started","call_id":"c1","tool_call":{"shellToolCall":{"args":{"command":"ls -la"}}},"session_id":"53c6"}`,
		`{"type":"tool_call","subtype":"started","call_id":"c2","tool_call":{"getMcpToolsToolCall":{"args":{"server":"ackn4th"}}},"session_id":"53c6"}`,
		`{"type":"tool_call","subtype":"started","call_id":"c3","tool_call":{"mcpToolCall":{"args":{"name":"ackn4th-ping","args":{}}}},"session_id":"53c6"}`,
		`{"type":"tool_call","subtype":"completed","call_id":"c4","tool_call":{"readToolCall":{"args":{"path":"/ws/x"},"result":{"error":{"errorMessage":"no such file"}}}},"session_id":"53c6"}`,
		`{"type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":"PONG"}]},"session_id":"53c6"}`,
		`{"type":"result","subtype":"success","duration_ms":1938,"is_error":false,"result":"PONG","session_id":"53c6","usage":{"inputTokens":7615,"outputTokens":15,"cacheReadTokens":3968,"cacheWriteTokens":0}}`,
	)
	if turn.SessionID != "53c6" || !turn.Done || turn.Text.String() != "PONG" || turn.Usage.CachedTokens != 3968 || turn.Usage.InputTokens != 7615+3968 {
		t.Errorf("turn = %+v", turn)
	}
	if len(r.acts) != 2 || r.acts[0] != "shell ls -la" || !strings.HasPrefix(r.acts[1], "!read /ws/x") {
		t.Errorf("acts = %v", r.acts)
	}
}

func TestCursorCommandWritesPrivateConfig(t *testing.T) {
	run := t.TempDir()
	spec, err := Cursor{}.Command("cursor-agent", cliagent.Request{Prompt: "hi", MCPURL: "http://127.0.0.1:1/mcp", MCPToken: "tok", Workspace: "/ws", RunDir: run})
	if err != nil {
		t.Fatal(err)
	}
	if spec.Dir != run || !strings.Contains(strings.Join(spec.Argv, " "), "--workspace "+run+" --add-dir /ws") {
		t.Errorf("spec = %+v", spec)
	}
	for _, f := range []string{"mcp.json", "cli.json"} {
		fi, err := os.Stat(filepath.Join(run, ".cursor", f))
		if err != nil || fi.Mode().Perm() != 0o600 {
			t.Errorf("%s: %v %v", f, err, fi)
		}
	}
}

func TestCopilotParse(t *testing.T) {
	var r recorded
	turn := turnFor(&r)
	feed(Copilot{}, turn,
		`{"type":"assistant.message_delta","data":{"messageId":"m","deltaContent":"P"},"ephemeral":true}`,
		`{"type":"assistant.message","data":{"content":"","toolRequests":[{"toolCallId":"call_S","name":"bash","arguments":{"command":"ls"}}]}}`,
		`{"type":"tool.execution_start","data":{"toolCallId":"call_S","toolName":"bash","arguments":{"command":"ls","description":"List"}}}`,
		`{"type":"tool.execution_complete","data":{"toolCallId":"call_S","success":true,"result":{"content":"a.txt"}}}`,
		`{"type":"tool.execution_start","data":{"toolCallId":"call_M","toolName":"ackn4th-ping","arguments":{},"mcpServerName":"ackn4th"}}`,
		`{"type":"assistant.message","data":{"content":"a.txt","toolRequests":[]}}`,
		`{"type":"result","timestamp":"2026-11-01T19:37:16.963Z","sessionId":"34eb","exitCode":0,"usage":{"premiumRequests":1}}`,
	)
	if turn.SessionID != "34eb" || !turn.Done || turn.Text.String() != "a.txt" {
		t.Errorf("turn = %+v", turn)
	}
	if !slices.Equal(r.acts, []string{"bash ls"}) {
		t.Errorf("acts = %v", r.acts)
	}
	run := t.TempDir()
	os.WriteFile(filepath.Join(run, "usage.json"), []byte(`{"modelMetrics":{"gpt-6-luna":{"usage":{"inputTokens":12950,"outputTokens":6,"cacheReadTokens":8570}}}}`), 0o600)
	Copilot{}.Finish(cliagent.Request{RunDir: run}, turn)
	if turn.Usage != (provider.Usage{InputTokens: 12950, OutputTokens: 6, CachedTokens: 8570}) {
		t.Errorf("usage = %+v", turn.Usage)
	}
}

// Every CLI verifies TLS through macOS's security service; blocking it
// breaks all their HTTPS requests.
func TestCLIsReachSecurityService(t *testing.T) {
	for _, d := range []interface{ Keychain() bool }{Codex{}, Cursor{}, Copilot{}} {
		if !d.Keychain() {
			t.Errorf("%T must reach the security service", d)
		}
	}
}
