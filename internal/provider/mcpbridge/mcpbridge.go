// Package mcpbridge serves ackn4th's tools to an agent CLI (Claude Code,
// Codex, Cursor, Copilot) over MCP's streamable HTTP transport, on
// 127.0.0.1 with a random bearer token. Each tool call is handed to the
// session as a Call and blocks until the runner sends its result back, so
// the workspace jail, file ownership, done votes and events work the same
// as with API providers.
package mcpbridge

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"slices"
	"time"

	"github.com/dethmasque/ackn4th/internal/provider"
)

// Server is the MCP server name; CLIs prefix tool names with it.
const Server = "ackn4th"

// Call is a tool call waiting for its result.
type Call struct {
	provider.ToolCall
	Result chan provider.ToolResult
}

// Bridge is one running MCP endpoint.
type Bridge struct {
	URL   string // http://127.0.0.1:port/mcp
	Token string
	Calls chan *Call
	quit  chan struct{}
	srv   *http.Server
	tools []provider.ToolSpec
	// IDMeta is the _meta key a CLI puts its own tool-call ID under.
	IDMeta string
}

// Start serves tools on a fresh localhost port.
func Start(tools []provider.ToolSpec, idMeta string) (*Bridge, error) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, fmt.Errorf("tool server: %w", err)
	}
	b := &Bridge{
		URL: "http://" + ln.Addr().String() + "/mcp", Token: RandomHex(32),
		Calls: make(chan *Call), quit: make(chan struct{}), tools: tools, IDMeta: idMeta,
	}
	b.srv = &http.Server{Handler: b, ReadHeaderTimeout: 10 * time.Second}
	go b.srv.Serve(ln)
	return b, nil
}

// Close stops the server; waiting calls return an error to the CLI.
func (b *Bridge) Close() {
	select {
	case <-b.quit:
	default:
		close(b.quit)
	}
	b.srv.Close()
}

type rpcRequest struct {
	ID     json.RawMessage `json:"id"`
	Method string          `json:"method"`
	Params json.RawMessage `json:"params"`
}

func (b *Bridge) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	want := "Bearer " + b.Token
	if subtle.ConstantTimeCompare([]byte(r.Header.Get("Authorization")), []byte(want)) != 1 {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	var req rpcRequest
	if err := json.NewDecoder(io.LimitReader(r.Body, 64<<20)).Decode(&req); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	if len(req.ID) == 0 {
		w.WriteHeader(http.StatusAccepted) // a notification
		return
	}
	result, rpcErr := b.handle(r.Context(), req)
	resp := map[string]any{"jsonrpc": "2.0", "id": req.ID}
	if rpcErr != nil {
		resp["error"] = rpcErr
	} else {
		resp["result"] = result
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(resp)
}

func (b *Bridge) handle(ctx context.Context, req rpcRequest) (any, map[string]any) {
	switch req.Method {
	case "initialize":
		var p struct {
			ProtocolVersion string `json:"protocolVersion"`
		}
		json.Unmarshal(req.Params, &p)
		if p.ProtocolVersion == "" {
			p.ProtocolVersion = "2025-06-18"
		}
		return map[string]any{
			"protocolVersion": p.ProtocolVersion,
			"capabilities":    map[string]any{"tools": map[string]any{}},
			"serverInfo":      map[string]any{"name": Server, "version": "1"},
		}, nil
	case "ping":
		return map[string]any{}, nil
	case "tools/list":
		list := []map[string]any{}
		for _, t := range b.tools {
			list = append(list, map[string]any{"name": t.Name, "description": t.Description, "inputSchema": t.Schema})
		}
		return map[string]any{"tools": list}, nil
	case "tools/call":
		return b.call(ctx, req.Params), nil
	case "resources/list":
		return map[string]any{"resources": []any{}}, nil
	case "prompts/list":
		return map[string]any{"prompts": []any{}}, nil
	}
	return nil, map[string]any{"code": -32601, "message": "method not found: " + req.Method}
}

// call hands a tool call to the session and waits for the runner's result.
func (b *Bridge) call(ctx context.Context, params json.RawMessage) any {
	var p struct {
		Name      string          `json:"name"`
		Arguments json.RawMessage `json:"arguments"`
		Meta      map[string]any  `json:"_meta"`
	}
	if err := json.Unmarshal(params, &p); err != nil {
		return text("Error: malformed tool call", true)
	}
	if !slices.ContainsFunc(b.tools, func(t provider.ToolSpec) bool { return t.Name == p.Name }) {
		return text("Error: unknown tool "+p.Name, true)
	}
	id, _ := p.Meta[b.IDMeta].(string)
	if id == "" {
		id = "call_" + RandomHex(8)
	}
	if len(p.Arguments) == 0 || string(p.Arguments) == "null" {
		p.Arguments = json.RawMessage("{}")
	}
	c := &Call{ToolCall: provider.ToolCall{ID: id, Name: p.Name, Input: p.Arguments}, Result: make(chan provider.ToolResult, 1)}
	select {
	case b.Calls <- c:
	case <-b.quit:
		return text("Error: ackn4th stopped", true)
	case <-ctx.Done():
		return text("Error: cancelled", true)
	}
	select {
	case r := <-c.Result:
		return text(r.Content, r.IsError)
	case <-b.quit:
		return text("Error: ackn4th stopped", true)
	case <-ctx.Done():
		return text("Error: cancelled", true)
	}
}

func text(s string, isErr bool) map[string]any {
	return map[string]any{"content": []any{map[string]any{"type": "text", "text": s}}, "isError": isErr}
}

// RandomHex returns n random bytes, hex encoded.
func RandomHex(n int) string {
	b := make([]byte, n)
	rand.Read(b)
	return hex.EncodeToString(b)
}
