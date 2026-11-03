package mcpbridge

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/dethmasque/ackn4th/internal/provider"
)

func post(t *testing.T, b *Bridge, auth, body string) (int, map[string]any) {
	t.Helper()
	req, _ := http.NewRequest("POST", b.URL, strings.NewReader(body))
	req.Header.Set("Authorization", auth)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var out map[string]any
	json.NewDecoder(resp.Body).Decode(&out)
	return resp.StatusCode, out
}

func TestBridge(t *testing.T) {
	b, err := Start([]provider.ToolSpec{{Name: "read_file", Schema: map[string]any{"type": "object"}}}, "x/id")
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()
	for _, auth := range []string{"", "Bearer wrong", "Bearer " + b.Token[:10]} {
		if code, _ := post(t, b, auth, `{"jsonrpc":"2.0","id":1,"method":"tools/list"}`); code != http.StatusUnauthorized {
			t.Errorf("%q: status %d", auth, code)
		}
	}
	auth := "Bearer " + b.Token
	_, out := post(t, b, auth, `{"jsonrpc":"2.0","id":1,"method":"tools/list"}`)
	if tools := out["result"].(map[string]any)["tools"].([]any); len(tools) != 1 {
		t.Fatalf("tools = %v", tools)
	}
	go func() {
		c := <-b.Calls
		if c.ID != "abc" || c.Name != "read_file" || string(c.Input) != `{"path":"a"}` {
			t.Errorf("call = %+v", c.ToolCall)
		}
		c.Result <- provider.ToolResult{CallID: c.ID, Content: "contents"}
	}()
	_, out = post(t, b, auth, `{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"read_file","arguments":{"path":"a"},"_meta":{"x/id":"abc"}}}`)
	content := out["result"].(map[string]any)["content"].([]any)[0].(map[string]any)["text"]
	if content != "contents" {
		t.Errorf("result = %v", out)
	}
	_, out = post(t, b, auth, `{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"rm_rf","arguments":{}}}`)
	if out["result"].(map[string]any)["isError"] != true {
		t.Errorf("unknown tools must fail: %v", out)
	}
}
