package deepseek

import (
	"bytes"
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

// sse writes Responses API stream events, one line each.
func sse(w http.ResponseWriter, events ...string) {
	w.Header().Set("Content-Type", "text/event-stream")
	for _, e := range events {
		var b bytes.Buffer
		json.Compact(&b, []byte(e))
		var ev struct{ Type string }
		json.Unmarshal(b.Bytes(), &ev)
		fmt.Fprintf(w, "event: %s\ndata: %s\n\n", ev.Type, b.String())
	}
}

const usage = `"usage":{"input_tokens":100,"input_tokens_details":{"cached_tokens":40},"output_tokens":7,"output_tokens_details":{"reasoning_tokens":3},"total_tokens":107}`

func TestToolRoundTrip(t *testing.T) {
	var bodies []map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer sk-test" || r.URL.Path != "/responses" {
			t.Errorf("bad request %s %q", r.URL.Path, r.Header.Get("Authorization"))
		}
		b, _ := io.ReadAll(r.Body)
		var m map[string]any
		json.Unmarshal(b, &m)
		bodies = append(bodies, m)
		if len(bodies) == 1 {
			sse(w,
				`{"type":"response.reasoning_text.delta","item_id":"rs","output_index":0,"content_index":0,"delta":"hmm"}`,
				`{"type":"response.output_item.added","output_index":1,"item":{"type":"function_call","call_id":"c1","name":"read","arguments":""}}`,
				`{"type":"response.function_call_arguments.delta","item_id":"fc","output_index":1,"delta":"{\"p\":\"a\"}"}`,
				`{"type":"response.completed","response":{"id":"r1","status":"completed","output":[
					{"type":"reasoning","id":"rs","content":[{"type":"reasoning_text","text":"hmm"}]},
					{"type":"function_call","id":"fc","call_id":"c1","name":"read","arguments":"{\"p\":\"a\"}","status":"completed"}],`+usage+`}}`)
			return
		}
		sse(w,
			`{"type":"response.output_text.delta","item_id":"m","output_index":0,"content_index":0,"delta":"done"}`,
			`{"type":"response.completed","response":{"id":"r2","status":"completed","output":[
				{"type":"message","id":"m","role":"assistant","status":"completed","content":[{"type":"output_text","text":"done"}]}],`+usage+`}}`)
	}))
	defer srv.Close()

	p := New(Config{APIKey: "sk-test", Model: "deepseek-flash", MaxTokens: 100, Effort: "high", BaseURL: srv.URL})
	s := p.NewSession(provider.System{Shared: "sys"}, []provider.ToolSpec{{Name: "read", Schema: map[string]any{"type": "object"}}})
	var thought, toolName, toolArgs string
	r, err := s.SendText(context.Background(), "hi", provider.Stream{
		Thinking:  func(x string) { thought += x },
		ToolInput: func(name, delta string) { toolName += name; toolArgs += delta },
	})
	if err != nil {
		t.Fatal(err)
	}
	if r.StopReason != provider.StopToolUse || len(r.ToolCalls) != 1 || string(r.ToolCalls[0].Input) != `{"p":"a"}` || thought != "hmm" {
		t.Fatalf("reply %+v thought %q", r, thought)
	}
	if toolName != "read" || toolArgs != `{"p":"a"}` {
		t.Errorf("tool input streamed as %q %q", toolName, toolArgs)
	}
	if r.Usage.InputTokens != 100 || r.Usage.CachedTokens != 40 || r.ContextTokens != 100 {
		t.Fatalf("usage %+v", r.Usage)
	}
	b := bodies[0]
	if b["instructions"] != "sys" || b["max_output_tokens"] != float64(100) || b["stream"] != true ||
		b["reasoning"].(map[string]any)["effort"] != "high" {
		t.Errorf("params %v", b)
	}
	if tool := b["tools"].([]any)[0].(map[string]any); tool["type"] != "function" || tool["name"] != "read" {
		t.Errorf("tool %v", tool)
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
	// The tool round carries the reasoning back, in order.
	got, _ := json.Marshal(bodies[1]["input"])
	want := `[{"content":"hi","role":"user","type":"message"},{"content":[{"text":"hmm","type":"reasoning_text"}],"type":"reasoning"},{"arguments":"{\"p\":\"a\"}","call_id":"c1","name":"read","type":"function_call"},{"call_id":"c1","output":"ok","type":"function_call_output"}]`
	if string(got) != want {
		t.Errorf("second input:\n got %s\nwant %s", got, want)
	}
}

func TestReasoningDroppedAfterNewUserTurn(t *testing.T) {
	out := wireInput([]item{
		textItem("user", "a"), {Type: "reasoning", Text: "old"}, textItem("assistant", "x"),
		textItem("user", "b"), {Type: "reasoning", Text: "new"},
	})
	got, _ := json.Marshal(out)
	if strings.Contains(string(got), "old") || !strings.Contains(string(got), "new") || len(out) != 4 {
		t.Fatalf("%s", got)
	}
}

// A conversation saved on Chat Completions carries on, its current turn's
// reasoning included.
func TestRestoreOldState(t *testing.T) {
	old := `{"messages":[{"role":"user","content":"hi"},{"role":"assistant","content":null,"reasoning_content":"hmm","tool_calls":[{"id":"c1","type":"function","function":{"name":"read","arguments":"{}"}}]}],"pending":[{"id":"c1","name":"read","input":{}}]}`
	s, err := New(Config{APIKey: "k", Model: "m"}).RestoreSession(provider.System{}, nil, json.RawMessage(old))
	if err != nil {
		t.Fatal(err)
	}
	got, _ := json.Marshal(wireInput(s.(*session).items))
	want := `[{"content":"hi","role":"user","type":"message"},{"content":[{"text":"hmm","type":"reasoning_text"}],"type":"reasoning"},{"arguments":"{}","call_id":"c1","name":"read","type":"function_call"}]`
	if string(got) != want || len(s.PendingToolCalls()) != 1 {
		t.Errorf("converted:\n got %s\nwant %s", got, want)
	}
}

func TestTruncatedStream(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sse(w, `{"type":"response.output_text.delta","delta":"partial"}`) // no terminal event
	}))
	defer srv.Close()
	s := New(Config{APIKey: "sk-test", Model: "m", BaseURL: srv.URL}).NewSession(provider.System{}, nil)
	if _, err := s.SendText(context.Background(), "hi", provider.Stream{}); err == nil {
		t.Fatal("a stream cut off without a terminal event should fail")
	}
}

func TestLengthDropsToolCalls(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sse(w, `{"type":"response.incomplete","response":{"status":"incomplete","incomplete_details":{"reason":"max_output_tokens"},
			"output":[{"type":"function_call","call_id":"c","name":"x","arguments":"{\"a"}]}}`)
	}))
	defer srv.Close()
	s := New(Config{APIKey: "sk-test", Model: "m", BaseURL: srv.URL}).NewSession(provider.System{}, nil)
	r, err := s.SendText(context.Background(), "hi", provider.Stream{})
	if err != nil || r.StopReason != provider.StopMaxTokens || len(r.ToolCalls) != 0 || len(s.PendingToolCalls()) != 0 {
		t.Fatalf("%+v %v", r, err)
	}
}

func TestFailedResponse(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sse(w, `{"type":"response.failed","response":{"status":"failed","error":{"code":"server_error","message":"overloaded"}}}`)
	}))
	defer srv.Close()
	s := New(Config{APIKey: "sk-test", Model: "m", BaseURL: srv.URL}).NewSession(provider.System{}, nil)
	if _, err := s.SendText(context.Background(), "hi", provider.Stream{}); err == nil || !strings.Contains(err.Error(), "overloaded") {
		t.Fatalf("err = %v", err)
	}
}

func TestErrors(t *testing.T) {
	for status, want := range map[int]string{401: "rejected", 402: "balance"} {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path != "/responses" {
				t.Errorf("verify went to %s", r.URL.Path)
			}
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
