package task

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/dethmasque/ackn4th/internal/event"
)

func TestLiveFeedShowsAFileBeingWritten(t *testing.T) {
	s := NewStore(t.TempDir())
	os.MkdirAll(s.Dir("t1")+"/agents", 0o700)
	lf := newLiveFeed(s)
	ev := func(k event.Kind, tool, text string) {
		lf.Handle("t1", event.Event{Kind: k, Agent: 2, Tool: tool, Text: text})
	}
	ev(event.Thinking, "", "Planning the tracer.")
	content := strings.Repeat("// line\n", 2000) + "export function trace() {\n\treturn \"ok\";\n}\n"
	input, _ := json.Marshal(map[string]string{"path": "src/trace.js", "content": content})
	ev(event.ToolInput, "write_file", "")
	for i := 0; i < len(input); i += 37 {
		ev(event.ToolInput, "", string(input[i:min(i+37, len(input))]))
	}
	time.Sleep(2 * liveEvery)
	l := s.ReadLive("t1", 1)
	if l == nil {
		t.Fatal("no live file")
	}
	if l.Phase != "tool" || l.Tool != "write_file" || l.Path != "src/trace.js" || l.Bytes != len(input) {
		t.Errorf("live = %+v", l)
	}
	if !strings.HasSuffix(l.Preview, "export function trace() {\n    return \"ok\";\n}\n") || len(l.Preview) > liveTail {
		t.Errorf("preview tail = %q", lastN(l.Preview, 80))
	}
	if l.Thinking != "Planning the tracer." {
		t.Errorf("thinking = %q", l.Thinking)
	}
	// Once the call is in the log, the live view goes away.
	ev(event.ToolCall, "write_file", "src/trace.js (16.7 KB)")
	if s.ReadLive("t1", 1) != nil {
		t.Error("live file should be removed after the tool call")
	}
}

func TestPreviewInput(t *testing.T) {
	for _, c := range []struct {
		tail  string
		whole bool
		want  string
	}{
		{`{"path":"a.go","content":"package a\nfunc A() {}`, true, "package a\nfunc A() {}"},
		{`{"path":"a.go","old_text":"x","new_text":"y = \"q\"","replace_all`, true, `y = "q"`},
		{`ine two\nline three`, false, "ine two\nline three"},
		{`{"path":"a.go"`, true, ""},
		// JSON inside the content doesn't restart the preview.
		{`{"path":"a.json","content":"{\"k\": \"v\"}`, true, `{"k": "v"}`},
	} {
		if got := previewInput(c.tail, c.whole); got != c.want {
			t.Errorf("previewInput(%q) = %q, want %q", c.tail, got, c.want)
		}
	}
}

func lastN(s string, n int) string { return s[max(0, len(s)-n):] }
