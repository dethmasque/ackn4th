package openai

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
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

func TestToolLoop(t *testing.T) {
	var bodies []map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer sk-test" {
			w.WriteHeader(401)
			io.WriteString(w, `{"error":{"message":"bad key"}}`)
			return
		}
		var b map[string]any
		json.NewDecoder(r.Body).Decode(&b)
		bodies = append(bodies, b)
		if len(bodies) == 1 {
			sse(w,
				`{"id":"1","object":"chat.completion.chunk","model":"m","choices":[{"index":0,"delta":{"role":"assistant","content":"Looking"}}]}`,
				`{"id":"1","object":"chat.completion.chunk","model":"m","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"call_1","type":"function","function":{"name":"read_file","arguments":"{\"path\":"}}]}}]}`,
				`{"id":"1","object":"chat.completion.chunk","model":"m","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"function":{"arguments":"\"a.go\"}"}}]},"finish_reason":"tool_calls"}]}`,
				`{"id":"1","object":"chat.completion.chunk","model":"m","choices":[],"usage":{"prompt_tokens":100,"completion_tokens":7,"total_tokens":107,"prompt_tokens_details":{"cached_tokens":64}}}`,
			)
			return
		}
		sse(w,
			`{"id":"2","object":"chat.completion.chunk","model":"m","choices":[{"index":0,"delta":{"role":"assistant","content":"Done."},"finish_reason":"stop"}]}`,
		)
	}))
	defer srv.Close()

	p := New(Config{APIKey: "sk-test", Model: "m", MaxTokens: 100, Effort: "high", BaseURL: srv.URL + "/"})
	s := p.NewSession(provider.System{Shared: "team", Own: "role"}, []provider.ToolSpec{{Name: "read_file", Description: "read", Schema: map[string]any{"type": "object"}}})
	var streamed strings.Builder
	r, err := s.SendText(context.Background(), "go", provider.Stream{Text: func(t string) { streamed.WriteString(t) }})
	if err != nil {
		t.Fatal(err)
	}
	if r.StopReason != provider.StopToolUse || len(r.ToolCalls) != 1 || string(r.ToolCalls[0].Input) != `{"path":"a.go"}` {
		t.Fatalf("reply = %+v", r)
	}
	if r.Usage.InputTokens != 100 || r.Usage.CachedTokens != 64 || streamed.String() != "Looking" {
		t.Errorf("usage %+v, streamed %q", r.Usage, streamed.String())
	}
	raw, _ := s.State()
	s2, err := p.RestoreSession(provider.System{Shared: "team"}, nil, raw)
	if err != nil || len(s2.PendingToolCalls()) != 1 {
		t.Fatalf("restore: %v %v", err, s2.PendingToolCalls())
	}
	r, err = s2.SendToolResults(context.Background(), []provider.ToolResult{{CallID: "call_1", Content: "package a"}}, "note", provider.Stream{})
	if err != nil || r.StopReason != provider.StopEnd || r.Text != "Done." {
		t.Fatalf("second reply = %+v, %v", r, err)
	}
	msgs := bodies[1]["messages"].([]any)
	roles := []string{}
	for _, m := range msgs {
		roles = append(roles, m.(map[string]any)["role"].(string))
	}
	if got := strings.Join(roles, ","); got != "developer,user,assistant,tool,user" {
		t.Errorf("roles = %s", got)
	}
	if bodies[0]["reasoning_effort"] != "high" || bodies[0]["max_completion_tokens"] != float64(100) {
		t.Errorf("params = %v", bodies[0])
	}

	bad := New(Config{APIKey: "sk-wrong", Model: "m", BaseURL: srv.URL + "/"})
	if _, err := bad.NewSession(provider.System{}, nil).SendText(context.Background(), "x", provider.Stream{}); err == nil || !strings.Contains(err.Error(), "rejected") {
		t.Errorf("401: %v", err)
	}
}

// Past compact_at, the next turn starts by replacing the conversation with
// a summary the model writes, without tools.
func TestCompaction(t *testing.T) {
	var bodies []map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var b map[string]any
		json.NewDecoder(r.Body).Decode(&b)
		bodies = append(bodies, b)
		switch {
		case b["stream"] != true: // the summary request
			w.Header().Set("Content-Type", "application/json")
			io.WriteString(w, `{"id":"s","object":"chat.completion","model":"m","choices":[{"index":0,"message":{"role":"assistant","content":"Built the parser."},"finish_reason":"stop"}],"usage":{"prompt_tokens":900,"completion_tokens":40,"total_tokens":940}}`)
		default:
			sse(w,
				`{"id":"1","object":"chat.completion.chunk","model":"m","choices":[{"index":0,"delta":{"role":"assistant","content":"ok"},"finish_reason":"stop"}]}`,
				`{"id":"1","object":"chat.completion.chunk","model":"m","choices":[],"usage":{"prompt_tokens":900,"completion_tokens":2,"total_tokens":902}}`,
			)
		}
	}))
	defer srv.Close()

	p := New(Config{APIKey: "sk-test", Model: "m", CompactAt: 500, BaseURL: srv.URL + "/"})
	s := p.NewSession(provider.System{Shared: "team"}, nil)
	if r, err := s.SendText(context.Background(), "first", provider.Stream{}); err != nil || r.Compacted {
		t.Fatalf("first turn: %+v %v", r, err)
	}
	r, err := s.SendText(context.Background(), "second", provider.Stream{})
	if err != nil || !r.Compacted || r.Usage.InputTokens != 1800 {
		t.Fatalf("second turn should compact first: %+v %v", r, err)
	}
	if len(bodies) != 3 || bodies[1]["tool_choice"] != "none" {
		t.Fatalf("requests: %d, summary params %v", len(bodies), bodies[1])
	}
	msgs := bodies[2]["messages"].([]any)
	if len(msgs) != 4 || !strings.Contains(msgs[1].(map[string]any)["content"].(string), "Built the parser.") {
		t.Errorf("after compaction the history should be the summary and the new turn: %v", msgs)
	}
	raw, _ := s.State()
	if !strings.Contains(string(raw), `"context":900`) {
		t.Errorf("context size should persist: %s", raw)
	}
}

// PickModel takes the newest full chat model that answers; Verify needs a
// model and probes it.
func TestPickModel(t *testing.T) {
	var probed []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/models" {
			io.WriteString(w, `{"object":"list","data":[
				{"id":"gpt-old","object":"model","created":100,"owned_by":"openai"},
				{"id":"gpt-new-mini","object":"model","created":300,"owned_by":"openai"},
				{"id":"gpt-new","object":"model","created":200,"owned_by":"openai"},
				{"id":"gpt-newest-realtime","object":"model","created":400,"owned_by":"openai"},
				{"id":"gpt-new-2026-01-01","object":"model","created":200,"owned_by":"openai"},
				{"id":"text-embedding-3-large","object":"model","created":500,"owned_by":"openai"}]}`)
			return
		}
		var b struct {
			Model string `json:"model"`
		}
		json.NewDecoder(r.Body).Decode(&b)
		probed = append(probed, b.Model)
		if b.Model == "gpt-new" {
			w.WriteHeader(404)
			io.WriteString(w, `{"error":{"message":"no access","code":"model_not_found"}}`)
			return
		}
		io.WriteString(w, `{"id":"1","object":"chat.completion","created":1,"model":"`+b.Model+`","choices":[{"index":0,"finish_reason":"stop","message":{"role":"assistant","content":"OK"}}]}`)
	}))
	defer srv.Close()

	p := New(Config{APIKey: "sk-test", BaseURL: srv.URL + "/"})
	if err := p.Verify(context.Background()); !errors.Is(err, ErrNoModel) {
		t.Errorf("Verify without a model = %v", err)
	}
	m, err := p.PickModel(context.Background())
	if err != nil || m != "gpt-old" || !slices.Equal(probed, []string{"gpt-new", "gpt-old"}) {
		t.Errorf("picked %q, %v; probed %v", m, err, probed)
	}
	if err := New(Config{APIKey: "sk-test", Model: "gpt-new", BaseURL: srv.URL + "/"}).Verify(context.Background()); err == nil {
		t.Error("Verify passed a model the key can't use")
	}
}
