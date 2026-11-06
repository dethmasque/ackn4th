// Package deepseek implements provider.Provider with DeepSeek's
// OpenAI-compatible Responses API (POST /responses) and an API key.
// DeepSeek keeps no conversation: ackn4th keeps the items with the task and
// sends them each turn, relying on DeepSeek's automatic prefix caching.
//
// Requests go through the shared rate-limit gate (see internal/ratelimit).
package deepseek

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/dethmasque/ackn4th/internal/provider"
	"github.com/dethmasque/ackn4th/internal/ratelimit"
)

// Name is the provider's name in config and task files.
const Name = "deepseek"

// KeyURL is where users create an API key.
const KeyURL = "https://platform.deepseek.com/api_keys"

// baseURL is pinned, so the key only ever goes to DeepSeek.
// The changes in https://github.com/dethmasque/ackn4th/pull/21
// will allow for a custom baseURL supplied to the OpenAI provider
// for use with DeepSeek.
const baseURL = "https://api.deepseek.com"

const maxAttempts = 4

// Config selects the model and its limits.
type Config struct {
	APIKey string
	Model  string // deepseek-flash or deepseek-v4-pro
	// MaxTokens caps a reply, reasoning included.
	MaxTokens int64
	// Effort is the reasoning effort; empty uses DeepSeek's default
	// (thinking on). DeepSeek maps medium and xhigh to high.
	Effort string
	// CompactAt is the prompt size, in tokens, past which the conversation
	// is summarized at the start of the next turn (0 never).
	CompactAt int64
	// BaseURL overrides the endpoint, for tests only.
	BaseURL string
}

// Provider talks to DeepSeek.
type Provider struct {
	cfg    Config
	client *http.Client
	base   string
}

// New returns a provider. DEEPSEEK_API_KEY and similar variables are never
// read: the key and endpoint are set explicitly.
func New(cfg Config) *Provider {
	base := cfg.BaseURL
	if base == "" {
		base = baseURL
	}
	gate := ratelimit.For(Name + ":" + ratelimit.Fingerprint(cfg.APIKey))
	return &Provider{cfg: cfg, base: strings.TrimRight(base, "/"), client: ratelimit.Client(gate)}
}

func (p *Provider) Name() string  { return Name }
func (p *Provider) Model() string { return p.cfg.Model }

// Verify checks that the key works and the model answers a request with a tool.
func (p *Provider) Verify(ctx context.Context) error {
	body := map[string]any{
		"model":             p.cfg.Model,
		"input":             "Reply with OK.",
		"tools":             toolParams([]provider.ToolSpec{{Name: "noop", Description: "Does nothing.", Schema: map[string]any{"type": "object", "properties": map[string]any{}}}}),
		"max_output_tokens": 16,
	}
	p.reasoning(body)
	resp, err := p.post(ctx, body)
	if err != nil {
		return err
	}
	return resp.Body.Close()
}

func (p *Provider) reasoning(body map[string]any) {
	if p.cfg.Effort != "" {
		body["reasoning"] = map[string]string{"effort": p.cfg.Effort}
	}
}

// item is one conversation entry, kept in ackn4th's own shape so saved
// tasks don't depend on the wire format.
type item struct {
	Type      string `json:"type"`           // message, reasoning, function_call, function_call_output
	Role      string `json:"role,omitempty"` // messages: user or assistant
	Text      string `json:"text,omitempty"` // a message's or reasoning's text
	CallID    string `json:"call_id,omitempty"`
	Name      string `json:"name,omitempty"`
	Arguments string `json:"arguments,omitempty"`
	Output    string `json:"output,omitempty"`
}

func textItem(role, text string) item { return item{Type: "message", Role: role, Text: text} }

func resultItem(id, text string) item {
	return item{Type: "function_call_output", CallID: id, Output: text}
}

type state struct {
	Items []item `json:"items,omitempty"`
	// Messages is the history as versions on Chat Completions saved it.
	Messages []message           `json:"messages,omitempty"`
	Pending  []provider.ToolCall `json:"pending,omitempty"`
	Context  int64               `json:"context,omitempty"`
}

// message is one chat message as older versions saved it.
type message struct {
	Role             string  `json:"role"`
	Content          *string `json:"content"`
	ReasoningContent string  `json:"reasoning_content,omitempty"`
	ToolCalls        []struct {
		ID       string `json:"id"`
		Function struct {
			Name      string `json:"name"`
			Arguments string `json:"arguments"`
		} `json:"function"`
	} `json:"tool_calls,omitempty"`
	ToolCallID string `json:"tool_call_id,omitempty"`
}

// fromMessages converts a history saved by an older version.
func fromMessages(ms []message) []item {
	var out []item
	for _, m := range ms {
		text := ""
		if m.Content != nil {
			text = *m.Content
		}
		switch m.Role {
		case "user":
			out = append(out, textItem("user", text))
		case "tool":
			out = append(out, resultItem(m.ToolCallID, text))
		case "assistant":
			if m.ReasoningContent != "" {
				out = append(out, item{Type: "reasoning", Text: m.ReasoningContent})
			}
			if text != "" || len(m.ToolCalls) == 0 {
				out = append(out, textItem("assistant", text))
			}
			for _, c := range m.ToolCalls {
				out = append(out, item{Type: "function_call", CallID: c.ID, Name: c.Function.Name, Arguments: c.Function.Arguments})
			}
		}
	}
	return out
}

type session struct {
	p       *Provider
	system  provider.System
	tools   []map[string]any
	items   []item
	pending []provider.ToolCall
	context int64 // the last request's prompt tokens
}

func toolParams(specs []provider.ToolSpec) []map[string]any {
	var out []map[string]any
	for _, t := range specs {
		out = append(out, map[string]any{"type": "function", "name": t.Name, "description": t.Description, "parameters": t.Schema})
	}
	return out
}

func (p *Provider) NewSession(system provider.System, tools []provider.ToolSpec) provider.Session {
	return &session{p: p, system: system, tools: toolParams(tools)}
}

func (p *Provider) RestoreSession(system provider.System, tools []provider.ToolSpec, raw json.RawMessage) (provider.Session, error) {
	var st state
	if err := json.Unmarshal(raw, &st); err != nil {
		return nil, fmt.Errorf("deepseek: restore session: %w", err)
	}
	items := st.Items
	if len(items) == 0 {
		items = fromMessages(st.Messages)
	}
	return &session{p: p, system: system, tools: toolParams(tools), items: items, pending: st.Pending, context: st.Context}, nil
}

func (s *session) PendingToolCalls() []provider.ToolCall { return s.pending }

func (s *session) State() (json.RawMessage, error) {
	return json.Marshal(state{Items: s.items, Pending: s.pending, Context: s.context})
}

func (s *session) SendText(ctx context.Context, text string, st provider.Stream) (*provider.Reply, error) {
	// Calls the runner never answered still need answers in the history.
	for _, c := range s.pending {
		s.items = append(s.items, resultItem(c.ID, "Error: not run (the turn ended)."))
	}
	s.pending = nil
	var cost *provider.Usage
	if s.p.cfg.CompactAt > 0 && s.context >= s.p.cfg.CompactAt {
		u, err := s.compact(ctx)
		if err != nil {
			return nil, err
		}
		cost = &u
	}
	s.items = append(s.items, textItem("user", text))
	reply, err := s.send(ctx, st, true)
	if reply != nil && cost != nil {
		reply.Compacted = true
		reply.Usage.Add(*cost)
	}
	return reply, err
}

// compact replaces the history with a summary the model writes.
func (s *session) compact(ctx context.Context) (provider.Usage, error) {
	items := append(append([]item(nil), s.items...), textItem("user", provider.CompactPrompt))
	r, err := s.request(ctx, items, false, provider.Stream{})
	if err != nil {
		return provider.Usage{}, fmt.Errorf("compacting context: %w", err)
	}
	if r.Text == "" {
		return r.Usage, errors.New("compacting context: the model returned no summary")
	}
	s.items = []item{
		textItem("user", provider.CompactedIntro+r.Text),
		textItem("assistant", "Understood. I will continue from this summary and re-read files as needed."),
	}
	s.context = 0
	return r.Usage, nil
}

func (s *session) SendToolResults(ctx context.Context, results []provider.ToolResult, extra string, st provider.Stream) (*provider.Reply, error) {
	byID := map[string]provider.ToolResult{}
	for _, r := range results {
		byID[r.CallID] = r
	}
	for _, c := range s.pending {
		r, ok := byID[c.ID]
		content := r.Content
		if !ok {
			content = "Error: this call was not run."
		} else if r.IsError && !strings.HasPrefix(content, "Error") {
			content = "Error: " + content
		}
		s.items = append(s.items, resultItem(c.ID, content))
	}
	s.pending = nil
	if extra != "" {
		s.items = append(s.items, textItem("user", extra))
	}
	return s.send(ctx, st, true)
}

func (s *session) send(ctx context.Context, st provider.Stream, withTools bool) (*provider.Reply, error) {
	r, err := s.request(ctx, s.items, withTools, st)
	if err != nil {
		return nil, err
	}
	s.items = append(s.items, r.items...)
	s.pending = r.ToolCalls
	s.context = r.ContextTokens
	return &r.Reply, nil
}

type result struct {
	provider.Reply
	items []item // the reply, for the history
}

// wireInput turns the history into request items. Reasoning from before
// the last user message is left out: it belongs to finished turns, and
// only costs tokens. Reasoning within the current turn goes back, so the
// model keeps its thread across tool calls.
func wireInput(items []item) []map[string]any {
	last := -1
	for i, it := range items {
		if it.Type == "message" && it.Role == "user" {
			last = i
		}
	}
	out := make([]map[string]any, 0, len(items))
	for i, it := range items {
		switch it.Type {
		case "message":
			out = append(out, map[string]any{"type": "message", "role": it.Role, "content": it.Text})
		case "reasoning":
			if i > last {
				out = append(out, map[string]any{"type": "reasoning", "content": []map[string]string{{"type": "reasoning_text", "text": it.Text}}})
			}
		case "function_call":
			out = append(out, map[string]any{"type": "function_call", "call_id": it.CallID, "name": it.Name, "arguments": it.Arguments})
		case "function_call_output":
			out = append(out, map[string]any{"type": "function_call_output", "call_id": it.CallID, "output": it.Output})
		}
	}
	return out
}

// event is the part of a stream event ackn4th reads.
type event struct {
	Type  string `json:"type"`
	Delta string `json:"delta"`
	Item  struct {
		Type string `json:"type"`
		Name string `json:"name"`
	} `json:"item"`
	Response *response `json:"response"`
	Message  string    `json:"message"`
}

type response struct {
	Output []struct {
		Type      string `json:"type"`
		CallID    string `json:"call_id"`
		Name      string `json:"name"`
		Arguments string `json:"arguments"`
		Content   []struct {
			Type    string `json:"type"`
			Text    string `json:"text"`
			Refusal string `json:"refusal"`
		} `json:"content"`
	} `json:"output"`
	Usage *struct {
		InputTokens        int64 `json:"input_tokens"`
		OutputTokens       int64 `json:"output_tokens"`
		InputTokensDetails struct {
			CachedTokens int64 `json:"cached_tokens"`
		} `json:"input_tokens_details"`
	} `json:"usage"`
	IncompleteDetails *struct {
		Reason string `json:"reason"`
	} `json:"incomplete_details"`
	Error *struct {
		Message string `json:"message"`
	} `json:"error"`
}

// request sends items and reads the streamed reply.
func (s *session) request(ctx context.Context, items []item, withTools bool, st provider.Stream) (*result, error) {
	body := map[string]any{
		"model":        s.p.cfg.Model,
		"instructions": s.system.Text(),
		"input":        wireInput(items),
		"stream":       true,
	}
	if s.p.cfg.MaxTokens > 0 {
		body["max_output_tokens"] = s.p.cfg.MaxTokens
	}
	if withTools && len(s.tools) > 0 {
		body["tools"] = s.tools
	}
	s.p.reasoning(body)
	resp, err := s.p.post(ctx, body)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	var final *response
	sc := bufio.NewScanner(resp.Body)
	sc.Buffer(make([]byte, 0, 64<<10), 8<<20)
	for sc.Scan() {
		data, ok := strings.CutPrefix(sc.Text(), "data:")
		if !ok {
			continue // event names, comments and keep-alives
		}
		var ev event
		if json.Unmarshal([]byte(strings.TrimSpace(data)), &ev) != nil {
			continue
		}
		switch ev.Type {
		case "response.reasoning_text.delta", "response.reasoning_summary_text.delta":
			if st.Thinking != nil && ev.Delta != "" {
				st.Thinking(ev.Delta)
			}
		case "response.output_text.delta":
			if st.Text != nil && ev.Delta != "" {
				st.Text(ev.Delta)
			}
		case "response.output_item.added":
			if ev.Item.Type == "function_call" && st.ToolInput != nil {
				st.ToolInput(ev.Item.Name, "")
			}
		case "response.function_call_arguments.delta":
			if st.ToolInput != nil && ev.Delta != "" {
				st.ToolInput("", ev.Delta)
			}
		case "response.completed", "response.incomplete":
			final = ev.Response
		case "response.failed":
			msg := "the response failed"
			if ev.Response != nil && ev.Response.Error != nil && ev.Response.Error.Message != "" {
				msg = ev.Response.Error.Message
			}
			return nil, errors.New("deepseek: " + msg)
		case "error":
			return nil, errors.New("deepseek: " + cmpOr(ev.Message, "the stream failed"))
		}
	}
	if err := sc.Err(); err != nil {
		return nil, fmt.Errorf("deepseek: reading the stream: %w", err)
	}
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	if final == nil {
		// No completed, incomplete or failed event: the stream was cut off.
		return nil, errors.New("deepseek: the response ended early (connection dropped?)")
	}
	return s.read(final), nil
}

// read turns a finished response into a reply and history items.
func (s *session) read(r *response) *result {
	res := &result{}
	if u := r.Usage; u != nil {
		res.Usage = provider.Usage{InputTokens: u.InputTokens, OutputTokens: u.OutputTokens, CachedTokens: u.InputTokensDetails.CachedTokens}
		res.ContextTokens = u.InputTokens
	}
	why := ""
	if r.IncompleteDetails != nil {
		why = r.IncompleteDetails.Reason
	}
	var text, refusal strings.Builder
	var calls []item
	for _, o := range r.Output {
		switch o.Type {
		case "reasoning":
			var t strings.Builder
			for _, c := range o.Content {
				t.WriteString(c.Text)
			}
			if t.Len() > 0 {
				res.items = append(res.items, item{Type: "reasoning", Text: t.String()})
			}
		case "message":
			for _, c := range o.Content {
				text.WriteString(c.Text)
				refusal.WriteString(c.Refusal)
			}
		case "function_call":
			calls = append(calls, item{Type: "function_call", CallID: o.CallID, Name: o.Name, Arguments: o.Arguments})
		}
	}
	if why == "max_output_tokens" {
		calls = nil // tool arguments cut off mid-way are unusable
	}
	res.Text = text.String()
	if res.Text != "" || len(calls) == 0 {
		res.items = append(res.items, textItem("assistant", res.Text))
	}
	for i, c := range calls {
		if c.CallID == "" {
			c.CallID = fmt.Sprintf("call_%d", i)
		}
		args := json.RawMessage(c.Arguments)
		if !json.Valid(args) {
			args, c.Arguments = json.RawMessage("{}"), "{}"
		}
		res.items = append(res.items, c)
		res.ToolCalls = append(res.ToolCalls, provider.ToolCall{ID: c.CallID, Name: c.Name, Input: args})
	}
	switch {
	case len(res.ToolCalls) > 0:
		res.StopReason = provider.StopToolUse
	case refusal.Len() > 0:
		res.StopReason, res.StopDetail = provider.StopRefusal, refusal.String()
	case why == "max_output_tokens":
		res.StopReason = provider.StopMaxTokens
	case why == "content_filter":
		res.StopReason, res.StopDetail = provider.StopRefusal, "content filter"
	case why != "":
		res.StopReason, res.StopDetail = provider.StopOther, why
	default:
		res.StopReason = provider.StopEnd
	}
	return res
}

func cmpOr(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

// post sends a request to /responses, retrying rate limits and server errors. The
// caller closes the body of a successful response.
func (p *Provider) post(ctx context.Context, body map[string]any) (*http.Response, error) {
	payload, err := json.Marshal(body)
	if err != nil {
		return nil, err
	}
	var last error
	for attempt := 0; attempt < maxAttempts; attempt++ {
		if attempt > 0 {
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-time.After(time.Duration(attempt) * 500 * time.Millisecond):
			}
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, p.base+"/responses", bytes.NewReader(payload))
		if err != nil {
			return nil, err
		}
		req.Header.Set("Authorization", "Bearer "+p.cfg.APIKey)
		req.Header.Set("Content-Type", "application/json")
		resp, err := p.client.Do(req)
		if err != nil {
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			last = fmt.Errorf("deepseek: %w", err)
			continue
		}
		if resp.StatusCode < 300 {
			return resp, nil
		}
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<16))
		resp.Body.Close()
		last = describe(resp.StatusCode, b)
		if resp.StatusCode != http.StatusTooManyRequests && resp.StatusCode < 500 {
			return nil, last
		}
	}
	return nil, last
}

// describe turns API errors into short messages. The key is never included.
func describe(status int, body []byte) error {
	var e struct {
		Error struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	json.Unmarshal(body, &e)
	msg := e.Error.Message
	switch status {
	case http.StatusUnauthorized:
		return errors.New("deepseek: the API key was rejected: run `ackn4th login --provider deepseek` with a new one from " + KeyURL)
	case http.StatusPaymentRequired:
		return errors.New("deepseek: out of balance: top up at https://platform.deepseek.com/top_up")
	case http.StatusTooManyRequests:
		return errors.New("deepseek: rate limited even after backing off; try fewer parallel agents, or resume later: " + msg)
	}
	if msg == "" {
		msg = http.StatusText(status)
	}
	return fmt.Errorf("deepseek: %s (HTTP %d)", msg, status)
}
