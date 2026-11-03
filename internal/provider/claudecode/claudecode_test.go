package claudecode

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"slices"
	"strings"
	"testing"

	"github.com/dethmasque/ackn4th/internal/provider"
)

// TestMain lets the test binary stand in for `claude`.
func TestMain(m *testing.M) {
	if os.Getenv("FAKE_CLAUDE") == "1" {
		fakeClaude()
		os.Exit(0)
	}
	os.Exit(m.Run())
}

// fakeClaude speaks just enough of `claude -p` stream-json: each user
// message makes it call read_file over MCP and then reply with what it
// read.
func fakeClaude() {
	args := os.Args[1:]
	flag := func(name string) string {
		if i := slices.Index(args, name); i >= 0 && i+1 < len(args) {
			return args[i+1]
		}
		return ""
	}
	if slices.Contains(args, "auth") {
		fmt.Println(`{"loggedIn":true,"authMethod":"claude.ai","email":"op@example.com","subscriptionType":"pro"}`)
		return
	}
	var cfg struct {
		MCPServers map[string]struct {
			URL     string            `json:"url"`
			Headers map[string]string `json:"headers"`
		} `json:"mcpServers"`
	}
	json.Unmarshal([]byte(flag("--mcp-config")), &cfg)
	srv := cfg.MCPServers[server]
	rpc := func(method string, params any) json.RawMessage {
		body, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": 1, "method": method, "params": params})
		req, _ := http.NewRequest("POST", srv.URL, bytes.NewReader(body))
		for k, v := range srv.Headers {
			req.Header.Set(k, v)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		defer resp.Body.Close()
		var out struct{ Result json.RawMessage }
		json.NewDecoder(resp.Body).Decode(&out)
		return out.Result
	}
	emit := func(v any) { b, _ := json.Marshal(v); fmt.Println(string(b)) }
	session := "sess-new"
	if id := flag("--resume"); id != "" {
		session = id
	}
	emit(map[string]any{"type": "system", "subtype": "init", "session_id": session})
	usage := map[string]any{"input_tokens": 10, "cache_read_input_tokens": 100, "output_tokens": 5}
	sc := bufio.NewScanner(os.Stdin)
	for sc.Scan() {
		var in struct {
			Message struct{ Content string }
		}
		json.Unmarshal(sc.Bytes(), &in)
		if strings.Contains(in.Message.Content, "limit") {
			emit(map[string]any{"type": "result", "subtype": "success", "is_error": true, "result": "Claude AI usage limit reached|1793559000"})
			continue
		}
		if tools := string(rpc("tools/list", nil)); !strings.Contains(tools, "read_file") {
			fmt.Fprintln(os.Stderr, "tools/list:", tools)
			os.Exit(1)
		}
		emit(map[string]any{"type": "assistant", "message": map[string]any{"id": "m1", "usage": usage,
			"content": []any{map[string]any{"type": "text", "text": "Looking."}}}})
		emit(map[string]any{"type": "assistant", "message": map[string]any{"id": "m1", "usage": usage,
			"content": []any{map[string]any{"type": "tool_use", "id": "toolu_1", "name": toolPrefix + "read_file", "input": map[string]any{"path": "a.txt"}}}}})
		res := rpc("tools/call", map[string]any{"name": "read_file", "arguments": map[string]any{"path": "a.txt"}, "_meta": map[string]any{"claudecode/toolUseId": "toolu_1"}})
		var r struct {
			Content []struct{ Text string }
		}
		json.Unmarshal(res, &r)
		emit(map[string]any{"type": "stream_event", "event": map[string]any{"type": "content_block_delta", "delta": map[string]any{"type": "text_delta", "text": "read: "}}})
		emit(map[string]any{"type": "assistant", "message": map[string]any{"id": "m2", "usage": usage,
			"content": []any{map[string]any{"type": "text", "text": "read: " + r.Content[0].Text}}}})
		emit(map[string]any{"type": "result", "subtype": "success", "stop_reason": "end_turn", "session_id": session,
			"usage": map[string]any{"input_tokens": 20, "cache_read_input_tokens": 200, "output_tokens": 30}})
	}
}

func testProvider(t *testing.T) *Provider {
	t.Setenv("FAKE_CLAUDE", "1")
	return New(Config{Model: "claude-sonnet-5-5", Bin: os.Args[0], Dir: t.TempDir()})
}

var readFile = provider.ToolSpec{Name: "read_file", Description: "Read a file", Schema: map[string]any{"type": "object"}}

func TestToolCallsReachTheRunner(t *testing.T) {
	p := testProvider(t)
	ctx := context.Background()
	s := p.NewSession(provider.System{Shared: "team", Own: "role"}, []provider.ToolSpec{readFile})
	var streamed strings.Builder
	st := provider.Stream{Text: func(s string) { streamed.WriteString(s) }}

	r, err := s.SendText(ctx, "go", st)
	if err != nil {
		t.Fatal(err)
	}
	if r.StopReason != provider.StopToolUse || len(r.ToolCalls) != 1 || r.Text != "Looking." {
		t.Fatalf("first reply = %+v", r)
	}
	c := r.ToolCalls[0]
	if c.ID != "toolu_1" || c.Name != "read_file" || string(c.Input) != `{"path":"a.txt"}` {
		t.Errorf("call = %+v (%s)", c, c.Input)
	}
	if got := s.PendingToolCalls(); len(got) != 1 {
		t.Errorf("pending = %v", got)
	}
	if r.Usage.InputTokens != 110 || r.Usage.CachedTokens != 100 {
		t.Errorf("usage counted once per message: %+v", r.Usage)
	}

	r, err = s.SendToolResults(ctx, []provider.ToolResult{{CallID: "toolu_1", Name: "read_file", Content: "hello"}}, "a note", st)
	if err != nil {
		t.Fatal(err)
	}
	if r.StopReason != provider.StopEnd || r.Text != "read: hello\n\na note" {
		t.Errorf("final reply = %+v", r)
	}
	if streamed.String() != "read: " {
		t.Errorf("streamed %q", streamed.String())
	}
	// Two messages of 110 in plus the result's total of 220: the total wins.
	if r.Usage.InputTokens != 110 || r.Usage.OutputTokens != 25 {
		t.Errorf("final usage = %+v", r.Usage)
	}
	state, _ := s.State()
	if string(state) != `{"session_id":"sess-new"}` {
		t.Errorf("state = %s", state)
	}

	// A restored session resumes the same Claude Code conversation.
	s2, err := p.RestoreSession(provider.System{}, []provider.ToolSpec{readFile}, state)
	if err != nil {
		t.Fatal(err)
	}
	r, err = s2.SendText(ctx, "again", provider.Stream{})
	if err != nil || r.StopReason != provider.StopToolUse {
		t.Fatalf("restored: %+v, %v", r, err)
	}
	s2.(*session).stop()
	s.(*session).stop()
}

func TestUsageLimitIsExplained(t *testing.T) {
	p := testProvider(t)
	s := p.NewSession(provider.System{}, []provider.ToolSpec{readFile})
	_, err := s.SendText(context.Background(), "hit the limit", provider.Stream{})
	if err == nil || !strings.Contains(err.Error(), "usage limit") || !strings.Contains(err.Error(), "resume the task") {
		t.Errorf("err = %v", err)
	}
	s.(*session).stop()
}

func TestStatus(t *testing.T) {
	t.Setenv("FAKE_CLAUDE", "1")
	a, err := Status(context.Background(), os.Args[0])
	if err != nil {
		t.Fatal(err)
	}
	if a.Describe() != "op@example.com · pro plan" {
		t.Errorf("describe = %q", a.Describe())
	}
}
