// Package deepseek implements provider.Provider with DeepSeek's
// OpenAI-compatible Chat Completions API and an API key. DeepSeek has no
// server-side conversation: ackn4th keeps the messages with the task and
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
const baseURL = "https://api.deepseek.com"

const maxAttempts = 4

// Config selects the model and its limits.
type Config struct {
	APIKey    string
	Model     string // deepseek-chat or deepseek-reasoner
	MaxTokens int64
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
		"model":      p.cfg.Model,
		"messages":   []any{map[string]string{"role": "user", "content": "Reply with OK."}},
		"tools":      toolParams([]provider.ToolSpec{{Name: "noop", Description: "Does nothing.", Schema: map[string]any{"type": "object", "properties": map[string]any{}}}}),
		"max_tokens": 16,
	}
	resp, err := p.post(ctx, body)
	if err != nil {
		return err
	}
	return resp.Body.Close()
}

// message is one chat message in DeepSeek's wire shape.
type message struct {
	Role             string     `json:"role"`
	Content          *string    `json:"content"`
	ReasoningContent string     `json:"reasoning_content,omitempty"`
	ToolCalls        []wireCall `json:"tool_calls,omitempty"`
	ToolCallID       string     `json:"tool_call_id,omitempty"`
}

type wireCall struct {
	ID       string `json:"id"`
	Type     string `json:"type"`
	Function struct {
		Name      string `json:"name"`
		Arguments string `json:"arguments"`
	} `json:"function"`
}

func textMsg(role, text string) message { return message{Role: role, Content: &text} }

type state struct {
	Messages []message           `json:"messages,omitempty"`
	Pending  []provider.ToolCall `json:"pending,omitempty"`
	Context  int64               `json:"context,omitempty"`
}

type session struct {
	p       *Provider
	system  provider.System
	tools   []map[string]any
	msgs    []message
	pending []provider.ToolCall
	context int64 // the last request's prompt tokens
}

func toolParams(specs []provider.ToolSpec) []map[string]any {
	var out []map[string]any
	for _, t := range specs {
		out = append(out, map[string]any{"type": "function", "function": map[string]any{
			"name": t.Name, "description": t.Description, "parameters": t.Schema,
		}})
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
	return &session{p: p, system: system, tools: toolParams(tools), msgs: st.Messages, pending: st.Pending, context: st.Context}, nil
}

func (s *session) PendingToolCalls() []provider.ToolCall { return s.pending }

func (s *session) State() (json.RawMessage, error) {
	return json.Marshal(state{Messages: s.msgs, Pending: s.pending, Context: s.context})
}

func (s *session) SendText(ctx context.Context, text string, st provider.Stream) (*provider.Reply, error) {
	// Calls the runner never answered still need answers in the history.
	for _, c := range s.pending {
		s.msgs = append(s.msgs, toolMsg(c.ID, "Error: not run (the turn ended)."))
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
	s.msgs = append(s.msgs, textMsg("user", text))
	reply, err := s.send(ctx, st, true)
	if reply != nil && cost != nil {
		reply.Compacted = true
		reply.Usage.Add(*cost)
	}
	return reply, err
}

func toolMsg(id, text string) message {
	m := textMsg("tool", text)
	m.ToolCallID = id
	return m
}

// compact replaces the history with a summary the model writes.
func (s *session) compact(ctx context.Context) (provider.Usage, error) {
	msgs := append(append([]message(nil), s.msgs...), textMsg("user", provider.CompactPrompt))
	r, err := s.request(ctx, msgs, false, provider.Stream{})
	if err != nil {
		return provider.Usage{}, fmt.Errorf("compacting context: %w", err)
	}
	if r.Text == "" {
		return r.Usage, errors.New("compacting context: the model returned no summary")
	}
	s.msgs = []message{
		textMsg("user", provider.CompactedIntro+r.Text),
		textMsg("assistant", "Understood. I will continue from this summary and re-read files as needed."),
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
		s.msgs = append(s.msgs, toolMsg(c.ID, content))
	}
	s.pending = nil
	if extra != "" {
		s.msgs = append(s.msgs, textMsg("user", extra))
	}
	return s.send(ctx, st, true)
}

func (s *session) send(ctx context.Context, st provider.Stream, withTools bool) (*provider.Reply, error) {
	r, err := s.request(ctx, s.msgs, withTools, st)
	if err != nil {
		return nil, err
	}
	am := message{Role: "assistant", ReasoningContent: r.reasoning}
	if r.Text != "" || len(r.calls) == 0 {
		t := r.Text
		am.Content = &t
	}
	am.ToolCalls = r.calls
	s.msgs = append(s.msgs, am)
	s.pending = r.ToolCalls
	s.context = r.ContextTokens
	return &r.Reply, nil
}

type result struct {
	provider.Reply
	reasoning string
	calls     []wireCall
}

// wireMessages prepends the system prompt, and drops reasoning from
// assistant messages before the last user message: DeepSeek ignores or
// rejects it, and it only costs tokens.
func (s *session) wireMessages(msgs []message) []message {
	last := -1
	for i, m := range msgs {
		if m.Role == "user" {
			last = i
		}
	}
	out := make([]message, 0, len(msgs)+1)
	out = append(out, textMsg("system", s.system.Text()))
	for i, m := range msgs {
		if i < last {
			m.ReasoningContent = ""
		}
		out = append(out, m)
	}
	return out
}

type chunk struct {
	Choices []struct {
		Delta struct {
			Content          string `json:"content"`
			ReasoningContent string `json:"reasoning_content"`
			ToolCalls        []struct {
				Index    int    `json:"index"`
				ID       string `json:"id"`
				Function struct {
					Name      string `json:"name"`
					Arguments string `json:"arguments"`
				} `json:"function"`
			} `json:"tool_calls"`
		} `json:"delta"`
		FinishReason string `json:"finish_reason"`
	} `json:"choices"`
	Usage *struct {
		PromptTokens         int64 `json:"prompt_tokens"`
		CompletionTokens     int64 `json:"completion_tokens"`
		PromptCacheHitTokens int64 `json:"prompt_cache_hit_tokens"`
	} `json:"usage"`
	Error *struct {
		Message string `json:"message"`
	} `json:"error"`
}

// request sends msgs and reads the streamed reply.
func (s *session) request(ctx context.Context, msgs []message, withTools bool, st provider.Stream) (*result, error) {
	body := map[string]any{
		"model":          s.p.cfg.Model,
		"messages":       s.wireMessages(msgs),
		"stream":         true,
		"stream_options": map[string]any{"include_usage": true},
	}
	if s.p.cfg.MaxTokens > 0 {
		body["max_tokens"] = s.p.cfg.MaxTokens
	}
	if withTools && len(s.tools) > 0 {
		body["tools"] = s.tools
	}
	resp, err := s.p.post(ctx, body)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	res := &result{}
	var text, reasoning strings.Builder
	var calls []*wireCall
	var finish string
	sc := bufio.NewScanner(resp.Body)
	sc.Buffer(make([]byte, 0, 64<<10), 8<<20)
	for sc.Scan() {
		line := sc.Text()
		data, ok := strings.CutPrefix(line, "data:")
		if !ok {
			continue // comments and keep-alives
		}
		data = strings.TrimSpace(data)
		if data == "[DONE]" {
			break
		}
		var c chunk
		if json.Unmarshal([]byte(data), &c) != nil {
			continue
		}
		if c.Error != nil {
			return nil, errors.New("deepseek: " + c.Error.Message)
		}
		if c.Usage != nil {
			res.Usage = provider.Usage{InputTokens: c.Usage.PromptTokens, OutputTokens: c.Usage.CompletionTokens, CachedTokens: c.Usage.PromptCacheHitTokens}
			res.ContextTokens = c.Usage.PromptTokens
		}
		for _, ch := range c.Choices {
			d := ch.Delta
			if d.ReasoningContent != "" {
				reasoning.WriteString(d.ReasoningContent)
				if st.Thinking != nil {
					st.Thinking(d.ReasoningContent)
				}
			}
			if d.Content != "" {
				text.WriteString(d.Content)
				if st.Text != nil {
					st.Text(d.Content)
				}
			}
			for _, tc := range d.ToolCalls {
				for len(calls) <= tc.Index {
					calls = append(calls, &wireCall{Type: "function"})
				}
				w := calls[tc.Index]
				if tc.ID != "" {
					w.ID = tc.ID
				}
				if tc.Function.Name != "" {
					w.Function.Name += tc.Function.Name
					if st.ToolInput != nil {
						st.ToolInput(tc.Function.Name, "")
					}
				}
				if tc.Function.Arguments != "" {
					w.Function.Arguments += tc.Function.Arguments
					if st.ToolInput != nil {
						st.ToolInput("", tc.Function.Arguments)
					}
				}
			}
			if ch.FinishReason != "" {
				finish = ch.FinishReason
			}
		}
	}
	if err := sc.Err(); err != nil {
		return nil, fmt.Errorf("deepseek: reading the stream: %w", err)
	}
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	if finish == "" {
		// The stream ended without a finish reason: it was cut off.
		return nil, errors.New("deepseek: the response ended early (connection dropped?)")
	}
	if finish == "length" {
		calls = nil // tool arguments cut off mid-way are unusable
	}

	res.Text = text.String()
	res.reasoning = reasoning.String()
	for i, c := range calls {
		if c.ID == "" {
			c.ID = fmt.Sprintf("call_%d", i)
		}
		args := json.RawMessage(c.Function.Arguments)
		if !json.Valid(args) {
			args = json.RawMessage("{}")
			c.Function.Arguments = "{}"
		}
		res.calls = append(res.calls, *c)
		res.ToolCalls = append(res.ToolCalls, provider.ToolCall{ID: c.ID, Name: c.Function.Name, Input: args})
	}
	switch {
	case len(res.ToolCalls) > 0:
		res.StopReason = provider.StopToolUse
	case finish == "length":
		res.StopReason = provider.StopMaxTokens
	case finish == "content_filter":
		res.StopReason, res.StopDetail = provider.StopRefusal, "content filter"
	case finish == "insufficient_system_resource":
		res.StopReason, res.StopDetail = provider.StopOther, "DeepSeek ran short of resources; try again"
	default:
		res.StopReason = provider.StopEnd
	}
	return res, nil
}

// post sends a chat request, retrying rate limits and server errors. The
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
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, p.base+"/chat/completions", bytes.NewReader(payload))
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
