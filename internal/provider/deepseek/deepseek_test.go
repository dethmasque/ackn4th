package deepseek

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/dethmasque/ackn4th/internal/provider"
)

func sse(w http.ResponseWriter, chunks ...string) {
	w.Header().Set("Content-Type", "text/event-stream")
	for _, c := range chunks {
		fmt.Fprintf(w, "data: %s\n\n", c)
	}
	fmt.Fprint(w, "data: [DONE]\n\n")
}

func TestToolRoundTrip(t *testing.T) {
	var bodies []map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer sk-test" || r.URL.Path != "/chat/completions" {
			t.Errorf("bad request %s %q", r.URL.Path, r.Header.Get("Authorization"))
		}
		b, _ := io.ReadAll(r.Body)
		var m map[string]any
		json.Unmarshal(b, &m)
		bodies = append(bodies, m)
		if len(bodies) == 1 {
			sse(w,
				`{"choices":[{"delta":{"reasoning_content":"hmm"}}]}`,
				`{"choices":[{"delta":{"tool_calls":[{"index":0,"id":"c1","function":{"name":"read","arguments":"{\"p\":"}}]}}]}`,
				`{"choices":[{"delta":{"tool_calls":[{"index":0,"function":{"arguments":"\"a\"}"}}]},"finish_reason":"tool_calls"}]}`,
				`{"choices":[],"usage":{"prompt_tokens":100,"completion_tokens":7,"prompt_cache_hit_tokens":40}}`)
			return
		}
		sse(w, `{"choices":[{"delta":{"content":"done"},"finish_reason":"stop"}]}`,
			`{"choices":[],"usage":{"prompt_tokens":120,"completion_tokens":2}}`)
	}))
	defer srv.Close()

	p := New(Config{APIKey: "sk-test", Model: "deepseek-chat", MaxTokens: 100, BaseURL: srv.URL})
	s := p.NewSession(provider.System{Shared: "sys"}, []provider.ToolSpec{{Name: "read", Schema: map[string]any{"type": "object"}}})
	var thought string
	r, err := s.SendText(context.Background(), "hi", provider.Stream{Thinking: func(x string) { thought += x }})
	if err != nil {
		t.Fatal(err)
	}
	if r.StopReason != provider.StopToolUse || len(r.ToolCalls) != 1 || string(r.ToolCalls[0].Input) != `{"p":"a"}` || thought != "hmm" {
		t.Fatalf("reply %+v thought %q", r, thought)
	}
	if r.Usage.InputTokens != 100 || r.Usage.CachedTokens != 40 || r.ContextTokens != 100 {
		t.Fatalf("usage %+v", r.Usage)
	}

	// Save and restore mid-turn, then answer the tool call.
	raw, err := s.State()
	if err != nil {
		t.Fatal(err)
	}
	s2, err := p.RestoreSession(provider.System{Shared: "sys"}, nil, raw)
	if err != nil {
		t.Fatal(err)
	}
	if len(s2.PendingToolCalls()) != 1 {
		t.Fatal("pending call lost")
	}
	r, err = s2.SendToolResults(context.Background(), []provider.ToolResult{{CallID: "c1", Content: "ok"}}, "", provider.Stream{})
	if err != nil || r.Text != "done" || r.StopReason != provider.StopEnd {
		t.Fatalf("reply %+v err %v", r, err)
	}
	msgs, _ := json.Marshal(bodies[1]["messages"])
	for _, want := range []string{`"role":"system"`, `"role":"tool"`, `"tool_call_id":"c1"`, `"reasoning_content":"hmm"`} {
		if !strings.Contains(string(msgs), want) {
			t.Errorf("second request lacks %s: %s", want, msgs)
		}
	}
}

func TestReasoningDroppedAfterNewUserTurn(t *testing.T) {
	s := &session{system: provider.System{Shared: "s"}}
	out := s.wireMessages([]message{
		textMsg("user", "a"), {Role: "assistant", ReasoningContent: "old"}, textMsg("user", "b"), {Role: "assistant", ReasoningContent: "new"},
	})
	if out[2].ReasoningContent != "" || out[4].ReasoningContent != "new" {
		t.Fatalf("%+v", out)
	}
}

func TestTruncatedStream(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sse(w, `{"choices":[{"delta":{"content":"partial"}}]}`) // no finish reason
	}))
	defer srv.Close()
	s := New(Config{APIKey: "sk-test", Model: "m", BaseURL: srv.URL}).NewSession(provider.System{}, nil)
	if _, err := s.SendText(context.Background(), "hi", provider.Stream{}); err == nil {
		t.Fatal("a stream cut off without a finish reason should fail")
	}
}

func TestLengthDropsToolCalls(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sse(w, `{"choices":[{"delta":{"tool_calls":[{"index":0,"id":"c","function":{"name":"x","arguments":"{\"a"}}]},"finish_reason":"length"}]}`)
	}))
	defer srv.Close()
	s := New(Config{APIKey: "sk-test", Model: "m", BaseURL: srv.URL}).NewSession(provider.System{}, nil)
	r, err := s.SendText(context.Background(), "hi", provider.Stream{})
	if err != nil || r.StopReason != provider.StopMaxTokens || len(r.ToolCalls) != 0 || len(s.PendingToolCalls()) != 0 {
		t.Fatalf("%+v %v", r, err)
	}
}

func TestErrors(t *testing.T) {
	for status, want := range map[int]string{401: "rejected", 402: "balance"} {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(status)
			io.WriteString(w, `{"error":{"message":"nope"}}`)
		}))
		err := New(Config{APIKey: "sk-secret", Model: "m", BaseURL: srv.URL}).Verify(context.Background())
		srv.Close()
		if err == nil || !strings.Contains(err.Error(), want) || strings.Contains(err.Error(), "sk-secret") {
			t.Errorf("status %d: %v", status, err)
		}
	}
}
