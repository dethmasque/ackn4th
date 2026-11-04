package openai

import (
	"bytes"
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

// sse writes Responses API stream events.
func sse(w http.ResponseWriter, events ...string) {
	w.Header().Set("Content-Type", "text/event-stream")
	for _, e := range events {
		var b bytes.Buffer
		json.Compact(&b, []byte(e)) // one line per event
		fmt.Fprintf(w, "data: %s\n\n", b.String())
	}
}

func TestToolLoop(t *testing.T) {
	var bodies []map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer sk-test" {
			w.WriteHeader(401)
			io.WriteString(w, `{"error":{"message":"bad key"}}`)
			return
		}
		if r.URL.Path != "/responses" {
			t.Errorf("request to %s", r.URL.Path)
		}
		var b map[string]any
		json.NewDecoder(r.Body).Decode(&b)
		bodies = append(bodies, b)
		if len(bodies) == 1 {
			sse(w,
				`{"type":"response.output_text.delta","item_id":"msg_1","output_index":1,"content_index":0,"delta":"Looking","sequence_number":1}`,
				`{"type":"response.output_item.added","output_index":2,"sequence_number":2,"item":{"type":"function_call","id":"fc_1","call_id":"call_1","name":"read_file","arguments":""}}`,
				`{"type":"response.function_call_arguments.delta","item_id":"fc_1","output_index":2,"delta":"{\"path\":\"a.go\"}","sequence_number":3}`,
				`{"type":"response.completed","sequence_number":4,"response":{"id":"r1","object":"response","status":"completed","model":"m","output":[
					{"type":"reasoning","id":"rs_1","summary":[{"type":"summary_text","text":"Check the files."}],"encrypted_content":"SECRET"},
					{"type":"message","id":"msg_1","role":"assistant","status":"completed","content":[{"type":"output_text","text":"Looking","annotations":[]}]},
					{"type":"function_call","id":"fc_1","call_id":"call_1","name":"read_file","arguments":"{\"path\":\"a.go\"}","status":"completed"}],
					"usage":{"input_tokens":100,"input_tokens_details":{"cached_tokens":64},"output_tokens":7,"output_tokens_details":{"reasoning_tokens":3},"total_tokens":107}}}`,
			)
			return
		}
		sse(w,
			`{"type":"response.output_text.delta","item_id":"msg_2","output_index":0,"content_index":0,"delta":"Done.","sequence_number":1}`,
			`{"type":"response.completed","sequence_number":2,"response":{"id":"r2","object":"response","status":"completed","model":"m","output":[
				{"type":"message","id":"msg_2","role":"assistant","status":"completed","content":[{"type":"output_text","text":"Done.","annotations":[]}]}],
				"usage":{"input_tokens":150,"input_tokens_details":{"cached_tokens":0},"output_tokens":2,"output_tokens_details":{"reasoning_tokens":0},"total_tokens":152}}}`,
		)
	}))
	defer srv.Close()

	p := New(Config{APIKey: "sk-test", Model: "m", MaxTokens: 100, Effort: "high", BaseURL: srv.URL + "/"})
	s := p.NewSession(provider.System{Shared: "team", Own: "role"}, []provider.ToolSpec{{Name: "read_file", Description: "read", Schema: map[string]any{"type": "object"}}})
	var streamed, toolName, toolArgs strings.Builder
	r, err := s.SendText(context.Background(), "go", provider.Stream{
		Text:      func(t string) { streamed.WriteString(t) },
		ToolInput: func(name, delta string) { toolName.WriteString(name); toolArgs.WriteString(delta) },
	})
	if err != nil {
		t.Fatal(err)
	}
	if r.StopReason != provider.StopToolUse || len(r.ToolCalls) != 1 || string(r.ToolCalls[0].Input) != `{"path":"a.go"}` || r.Text != "Looking" {
		t.Fatalf("reply = %+v", r)
	}
	if r.Usage.InputTokens != 100 || r.Usage.CachedTokens != 64 || streamed.String() != "Looking" {
		t.Errorf("usage %+v, streamed %q", r.Usage, streamed.String())
	}
	if toolName.String() != "read_file" || toolArgs.String() != `{"path":"a.go"}` {
		t.Errorf("tool input streamed as %q %q", toolName.String(), toolArgs.String())
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
	// The second request carries the whole conversation, the encrypted
	// reasoning included, in order.
	var kinds []string
	for _, it := range bodies[1]["input"].([]any) {
		m := it.(map[string]any)
		kinds = append(kinds, fmt.Sprint(m["type"], "/", m["role"]))
	}
	want := "<nil>/user,reasoning/<nil>,<nil>/assistant,function_call/<nil>,function_call_output/<nil>,<nil>/user"
	if got := strings.Join(kinds, ","); got != want {
		t.Errorf("input = %s", got)
	}
	rs := bodies[1]["input"].([]any)[1].(map[string]any)
	if sum, ok := rs["summary"].([]any); !ok || len(sum) != 1 || rs["encrypted_content"] != "SECRET" || rs["id"] != "rs_1" {
		t.Errorf("the reasoning should go back as it came, its summary an array: %v", rs)
	}
	b := bodies[0]
	if b["store"] != false || b["max_output_tokens"] != float64(100) || b["instructions"] == "" ||
		b["reasoning"].(map[string]any)["effort"] != "high" || fmt.Sprint(b["include"]) != "[reasoning.encrypted_content]" {
		t.Errorf("params = %v", b)
	}
	if tool := b["tools"].([]any)[0].(map[string]any); tool["strict"] != false || tool["type"] != "function" {
		t.Errorf("tool = %v", tool)
	}

	bad := New(Config{APIKey: "sk-wrong", Model: "m", BaseURL: srv.URL + "/"})
	if _, err := bad.NewSession(provider.System{}, nil).SendText(context.Background(), "x", provider.Stream{}); err == nil || !strings.Contains(err.Error(), "rejected") {
		t.Errorf("401: %v", err)
	}
}

// A conversation saved by a version on Chat Completions carries on.
func TestRestoreOldState(t *testing.T) {
	var body map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewDecoder(r.Body).Decode(&body)
		sse(w, `{"type":"response.completed","sequence_number":1,"response":{"id":"r","object":"response","status":"completed","model":"m","output":[],"usage":{"input_tokens":1,"input_tokens_details":{"cached_tokens":0},"output_tokens":1,"output_tokens_details":{"reasoning_tokens":0},"total_tokens":2}}}`)
	}))
	defer srv.Close()
	old := `{"messages":[{"role":"user","text":"go"},{"role":"assistant","text":"Looking","tool_calls":[{"id":"call_1","name":"read_file","input":{"path":"a.go"}}]}],"pending":[{"id":"call_1","name":"read_file","input":{"path":"a.go"}}],"context":10}`
	p := New(Config{APIKey: "sk-test", Model: "m", BaseURL: srv.URL + "/"})
	s, err := p.RestoreSession(provider.System{}, nil, json.RawMessage(old))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.SendToolResults(context.Background(), []provider.ToolResult{{CallID: "call_1", Content: "package a"}}, "", provider.Stream{}); err != nil {
		t.Fatal(err)
	}
	got, _ := json.Marshal(body["input"])
	want := `[{"content":"go","role":"user"},{"content":"Looking","role":"assistant"},{"arguments":"{\"path\":\"a.go\"}","call_id":"call_1","name":"read_file","type":"function_call"},{"call_id":"call_1","output":"package a","type":"function_call_output"}]`
	if string(got) != want {
		t.Errorf("input = %s", got)
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
		const usage = `"usage":{"input_tokens":900,"input_tokens_details":{"cached_tokens":0},"output_tokens":40,"output_tokens_details":{"reasoning_tokens":0},"total_tokens":940}`
		switch {
		case b["stream"] != true: // the summary request
			w.Header().Set("Content-Type", "application/json")
			io.WriteString(w, `{"id":"s","object":"response","status":"completed","model":"m","output":[{"type":"message","id":"m","role":"assistant","status":"completed","content":[{"type":"output_text","text":"Built the parser.","annotations":[]}]}],`+usage+`}`)
		default:
			sse(w, `{"type":"response.completed","sequence_number":1,"response":{"id":"r","object":"response","status":"completed","model":"m","output":[{"type":"message","id":"m","role":"assistant","status":"completed","content":[{"type":"output_text","text":"ok","annotations":[]}]}],`+usage+`}}`)
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
	input := bodies[2]["input"].([]any)
	if len(input) != 3 || !strings.Contains(input[0].(map[string]any)["content"].(string), "Built the parser.") {
		t.Errorf("after compaction the history should be the summary and the new turn: %v", input)
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
		io.WriteString(w, `{"id":"1","object":"response","status":"completed","model":"`+b.Model+`","output":[]}`)
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

// When no model answers, the error says why each one didn't, grouping
// models that failed the same way.
func TestPickModelSaysWhy(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/models" {
			io.WriteString(w, `{"object":"list","data":[
				{"id":"gpt-a","object":"model","created":300,"owned_by":"openai"},
				{"id":"gpt-b","object":"model","created":200,"owned_by":"openai"},
				{"id":"gpt-c","object":"model","created":100,"owned_by":"openai"}]}`)
			return
		}
		var b struct {
			Model string `json:"model"`
		}
		json.NewDecoder(r.Body).Decode(&b)
		w.WriteHeader(400)
		msg := "Function tools with reasoning_effort are not supported for " + b.Model + "."
		if b.Model == "gpt-c" {
			msg = "This model is not a chat model."
		}
		io.WriteString(w, `{"error":{"message":"`+msg+`","code":"invalid_request_error"}}`)
	}))
	defer srv.Close()

	_, err := New(Config{APIKey: "sk-test", Effort: "medium", BaseURL: srv.URL + "/"}).PickModel(context.Background())
	if err == nil {
		t.Fatal("picked a model though none answered")
	}
	for _, want := range []string{
		"gpt-a, gpt-b: Function tools with reasoning_effort are not supported for this model;",
		"gpt-c: This model is not a chat model",
		"providers.openai.model",
	} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error lacks %q: %v", want, err)
		}
	}
}

// A reasoning item saved with its summary as a string is repaired.
func TestRepairSummary(t *testing.T) {
	got := string(repairSummary(json.RawMessage(`{"type":"reasoning","id":"rs_1","summary":"[]","encrypted_content":"X"}`)))
	if got != `{"encrypted_content":"X","id":"rs_1","summary":[],"type":"reasoning"}` {
		t.Errorf("repaired = %s", got)
	}
	ok := `{"type":"reasoning","summary":[]}`
	if string(repairSummary(json.RawMessage(ok))) != ok {
		t.Error("changed a good item")
	}
}
