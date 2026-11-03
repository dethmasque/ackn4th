// Package openai implements provider.Provider with the OpenAI Chat
// Completions API and an API key.
//
// Requests go through a rate-limit gate shared by every agent on the same
// key (see internal/ratelimit): agents slow down when the key's
// remaining requests or tokens run low, and back off together after a
// 429, instead of each burning retries.
package openai

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"slices"
	"strings"

	"github.com/openai/openai-go/v3"
	"github.com/openai/openai-go/v3/option"
	"github.com/openai/openai-go/v3/packages/param"
	"github.com/openai/openai-go/v3/shared"

	"github.com/dethmasque/ackn4th/internal/provider"
	"github.com/dethmasque/ackn4th/internal/ratelimit"
)

// Name is the provider's name in config and task files.
const Name = "openai"

// KeyURL is where users create an API key.
const KeyURL = "https://platform.openai.com/api-keys"

// baseURL is pinned, so the key only ever goes to OpenAI.
const baseURL = "https://api.openai.com/v1/"

// Config selects the model and its limits.
type Config struct {
	APIKey    string
	Model     string
	MaxTokens int64
	Effort    string // reasoning effort; empty uses the model default
	// CompactAt is the prompt size, in tokens, past which the conversation
	// is summarized at the start of the next turn (0 never).
	CompactAt int64
	// BaseURL overrides the endpoint, for tests only.
	BaseURL string
}

// Provider talks to OpenAI.
type Provider struct {
	client openai.Client
	cfg    Config
}

// New returns a provider. OPENAI_API_KEY, OPENAI_BASE_URL and similar
// variables are never read: the key and endpoint are set explicitly.
func New(cfg Config) *Provider {
	base := cmpOr(cfg.BaseURL, baseURL)
	gate := ratelimit.For(Name + ":" + ratelimit.Fingerprint(cfg.APIKey))
	return &Provider{cfg: cfg, client: openai.NewClient(
		option.WithAPIKey(cfg.APIKey),
		option.WithBaseURL(base),
		option.WithHTTPClient(ratelimit.Client(gate)),
		option.WithMaxRetries(4),
	)}
}

func (p *Provider) Name() string  { return Name }
func (p *Provider) Model() string { return p.cfg.Model }

// ErrNoModel means no model is set: login picks one the key can use.
var ErrNoModel = errors.New("openai: no model chosen yet: run `ackn4th login --provider openai`, which picks one your key can use, or set providers.openai.model")

// Verify checks that the key works and that the model answers the kind of
// request agents send: tools offered, with the configured effort.
func (p *Provider) Verify(ctx context.Context) error {
	if p.cfg.Model == "" {
		return ErrNoModel
	}
	return describe(p.probe(ctx, p.cfg.Model))
}

// probe sends model a tiny request with a tool, as an agent's would be.
func (p *Provider) probe(ctx context.Context, model string) error {
	params := openai.ChatCompletionNewParams{
		Model:               model,
		Messages:            []openai.ChatCompletionMessageParamUnion{openai.UserMessage("Reply with OK.")},
		Tools:               toolParams([]provider.ToolSpec{{Name: "noop", Description: "Does nothing.", Schema: map[string]any{"type": "object", "properties": map[string]any{}}}}),
		MaxCompletionTokens: param.NewOpt(int64(16)),
	}
	if e := p.cfg.Effort; e != "" {
		params.ReasoningEffort = shared.ReasoningEffort(e)
	}
	_, err := p.client.Chat.Completions.New(ctx, params)
	return err
}

// PickModel finds the newest chat model this key can use that answers an
// agent's kind of request: full models before mini and nano ones. Only
// the first few candidates are tried, each with a few-token request.
func (p *Provider) PickModel(ctx context.Context) (string, error) {
	var models []openai.Model
	iter := p.client.Models.ListAutoPaging(ctx)
	for iter.Next() {
		if m := iter.Current(); chatModel(m.ID) {
			models = append(models, m)
		}
	}
	if err := iter.Err(); err != nil {
		return "", describe(err)
	}
	slices.SortStableFunc(models, func(a, b openai.Model) int {
		return cmp.Or(cmp.Compare(tier(a.ID), tier(b.ID)), cmp.Compare(b.Created, a.Created), strings.Compare(a.ID, b.ID))
	})
	var tried []string
	for _, m := range models[:min(len(models), 5)] {
		err := p.probe(ctx, m.ID)
		if err == nil {
			return m.ID, nil
		}
		var apiErr *openai.Error
		if !errors.As(err, &apiErr) || apiErr.StatusCode == http.StatusUnauthorized || apiErr.StatusCode == http.StatusTooManyRequests {
			return "", describe(err) // the key or the network, not the model
		}
		tried = append(tried, m.ID)
	}
	if len(tried) == 0 {
		return "", errors.New("openai: this key lists no chat models: check the project's model access at https://platform.openai.com/settings")
	}
	return "", fmt.Errorf("openai: none of %s answered a test request: set providers.openai.model to a chat model with tool use", strings.Join(tried, ", "))
}

// dated matches a snapshot suffix (gpt-5-2025-08-07): the undated alias is
// preferred.
var dated = regexp.MustCompile(`-\d{4}-\d{2}-\d{2}$`)

// chatModel reports whether id names a general chat model, not an audio,
// image, search or other special-purpose one.
func chatModel(id string) bool {
	if !strings.HasPrefix(id, "gpt-") || dated.MatchString(id) {
		return false
	}
	for _, s := range []string{"audio", "realtime", "tts", "transcribe", "image", "search", "instruct", "codex", "-pro", "oss", "chat-latest", "preview"} {
		if strings.Contains(id, s) {
			return false
		}
	}
	return true
}

// tier ranks full models before mini, then nano ones.
func tier(id string) int {
	switch {
	case strings.Contains(id, "nano"):
		return 2
	case strings.Contains(id, "mini"):
		return 1
	}
	return 0
}

// message is one conversation entry, kept in ackn4th's own shape so saved
// tasks don't depend on the SDK's types.
type message struct {
	Role      string              `json:"role"` // user, assistant, tool
	Text      string              `json:"text,omitempty"`
	ToolCalls []provider.ToolCall `json:"tool_calls,omitempty"`
	CallID    string              `json:"call_id,omitempty"`
}

type state struct {
	Messages []message           `json:"messages"`
	Pending  []provider.ToolCall `json:"pending,omitempty"`
	Context  int64               `json:"context,omitempty"`
}

type session struct {
	p        *Provider
	system   provider.System
	tools    []openai.ChatCompletionToolUnionParam
	messages []message
	pending  []provider.ToolCall
	context  int64 // the last request's prompt tokens
}

func (p *Provider) NewSession(system provider.System, tools []provider.ToolSpec) provider.Session {
	return &session{p: p, system: system, tools: toolParams(tools)}
}

func (p *Provider) RestoreSession(system provider.System, tools []provider.ToolSpec, raw json.RawMessage) (provider.Session, error) {
	var st state
	if err := json.Unmarshal(raw, &st); err != nil {
		return nil, fmt.Errorf("openai: restore session: %w", err)
	}
	return &session{p: p, system: system, tools: toolParams(tools), messages: st.Messages, pending: st.Pending, context: st.Context}, nil
}

func toolParams(specs []provider.ToolSpec) []openai.ChatCompletionToolUnionParam {
	var out []openai.ChatCompletionToolUnionParam
	for _, t := range specs {
		out = append(out, openai.ChatCompletionFunctionTool(shared.FunctionDefinitionParam{
			Name: t.Name, Description: param.NewOpt(t.Description), Parameters: shared.FunctionParameters(t.Schema),
		}))
	}
	return out
}

func (s *session) PendingToolCalls() []provider.ToolCall { return s.pending }

func (s *session) State() (json.RawMessage, error) {
	return json.Marshal(state{Messages: s.messages, Pending: s.pending, Context: s.context})
}

func (s *session) SendText(ctx context.Context, text string, st provider.Stream) (*provider.Reply, error) {
	// Calls the runner never answered still need answers in the history.
	for _, c := range s.pending {
		s.messages = append(s.messages, message{Role: "tool", CallID: c.ID, Text: "Error: not run (the turn ended)."})
	}
	s.pending = nil
	// A new turn is the only safe point to compact: no tool round is open.
	var cost *provider.Usage
	if s.p.cfg.CompactAt > 0 && s.context >= s.p.cfg.CompactAt {
		u, err := s.compact(ctx)
		if err != nil {
			return nil, err
		}
		cost = &u
	}
	s.messages = append(s.messages, message{Role: "user", Text: text})
	reply, err := s.send(ctx, st)
	if reply != nil && cost != nil {
		reply.Compacted = true
		reply.Usage.Add(*cost)
	}
	return reply, err
}

// compact replaces the whole history with a summary the model writes,
// using the same system prompt and tools so the request shares the cached
// prefix.
func (s *session) compact(ctx context.Context) (provider.Usage, error) {
	var cost provider.Usage
	p := s.params()
	p.Messages = append(p.Messages, openai.UserMessage(provider.CompactPrompt))
	p.ToolChoice = openai.ChatCompletionToolChoiceOptionUnionParam{OfAuto: param.NewOpt("none")}
	p.StreamOptions = openai.ChatCompletionStreamOptionsParam{}
	resp, err := s.p.client.Chat.Completions.New(ctx, p)
	if err != nil {
		return cost, fmt.Errorf("compacting context: %w", describe(err))
	}
	cost = provider.Usage{InputTokens: resp.Usage.PromptTokens, OutputTokens: resp.Usage.CompletionTokens, CachedTokens: resp.Usage.PromptTokensDetails.CachedTokens}
	if len(resp.Choices) == 0 || resp.Choices[0].Message.Content == "" {
		return cost, errors.New("compacting context: the model returned no summary")
	}
	s.messages = []message{
		{Role: "user", Text: provider.CompactedIntro + resp.Choices[0].Message.Content},
		{Role: "assistant", Text: "Understood. I will continue from this summary and re-read files as needed."},
	}
	s.context = 0
	return cost, nil
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
		s.messages = append(s.messages, message{Role: "tool", CallID: c.ID, Text: content})
	}
	s.pending = nil
	if extra != "" {
		s.messages = append(s.messages, message{Role: "user", Text: extra})
	}
	return s.send(ctx, st)
}

func (s *session) params() openai.ChatCompletionNewParams {
	msgs := []openai.ChatCompletionMessageParamUnion{openai.DeveloperMessage(s.system.Text())}
	for _, m := range s.messages {
		switch m.Role {
		case "user":
			msgs = append(msgs, openai.UserMessage(m.Text))
		case "tool":
			msgs = append(msgs, openai.ToolMessage(m.Text, m.CallID))
		case "assistant":
			a := openai.ChatCompletionAssistantMessageParam{}
			if m.Text != "" {
				a.Content.OfString = param.NewOpt(m.Text)
			}
			for _, c := range m.ToolCalls {
				a.ToolCalls = append(a.ToolCalls, openai.ChatCompletionMessageToolCallUnionParam{OfFunction: &openai.ChatCompletionMessageFunctionToolCallParam{
					ID: c.ID, Function: openai.ChatCompletionMessageFunctionToolCallFunctionParam{Name: c.Name, Arguments: string(c.Input)},
				}})
			}
			msgs = append(msgs, openai.ChatCompletionMessageParamUnion{OfAssistant: &a})
		}
	}
	p := openai.ChatCompletionNewParams{
		Model:         s.p.cfg.Model,
		Messages:      msgs,
		Tools:         s.tools,
		StreamOptions: openai.ChatCompletionStreamOptionsParam{IncludeUsage: param.NewOpt(true)},
		// Agents with the same system prompt and tools share a cache entry;
		// the key keeps them on the same cache shard.
		PromptCacheKey: param.NewOpt("ackn4th:" + ratelimit.Fingerprint(s.system.Shared)),
	}
	if s.p.cfg.MaxTokens > 0 {
		p.MaxCompletionTokens = param.NewOpt(s.p.cfg.MaxTokens)
	}
	if e := s.p.cfg.Effort; e != "" {
		p.ReasoningEffort = shared.ReasoningEffort(e)
	}
	return p
}

func (s *session) send(ctx context.Context, st provider.Stream) (*provider.Reply, error) {
	stream := s.p.client.Chat.Completions.NewStreaming(ctx, s.params())
	acc := openai.ChatCompletionAccumulator{}
	started := map[int64]bool{}
	for stream.Next() {
		chunk := stream.Current()
		acc.AddChunk(chunk)
		for _, ch := range chunk.Choices {
			if ch.Delta.Content != "" && st.Text != nil {
				st.Text(ch.Delta.Content)
			}
			for _, tc := range ch.Delta.ToolCalls {
				if st.ToolInput == nil {
					continue
				}
				if !started[tc.Index] {
					started[tc.Index] = true
					st.ToolInput(tc.Function.Name, "")
				}
				if tc.Function.Arguments != "" {
					st.ToolInput("", tc.Function.Arguments)
				}
			}
		}
	}
	if err := stream.Err(); err != nil {
		return nil, describe(err)
	}
	if len(acc.Choices) == 0 {
		return nil, errors.New("openai: empty response")
	}
	choice := acc.Choices[0]
	msg := choice.Message
	u := acc.Usage
	reply := &provider.Reply{
		Text: msg.Content,
		Usage: provider.Usage{
			InputTokens: u.PromptTokens, OutputTokens: u.CompletionTokens, CachedTokens: u.PromptTokensDetails.CachedTokens,
		},
		ContextTokens: u.PromptTokens,
	}
	s.context = u.PromptTokens
	am := message{Role: "assistant", Text: msg.Content}
	for _, tc := range msg.ToolCalls {
		args := json.RawMessage(tc.Function.Arguments)
		if !json.Valid(args) {
			args = json.RawMessage("{}")
		}
		call := provider.ToolCall{ID: tc.ID, Name: tc.Function.Name, Input: args}
		am.ToolCalls = append(am.ToolCalls, call)
		reply.ToolCalls = append(reply.ToolCalls, call)
	}
	s.messages = append(s.messages, am)
	s.pending = reply.ToolCalls
	switch {
	case len(reply.ToolCalls) > 0:
		reply.StopReason = provider.StopToolUse
	case msg.Refusal != "":
		reply.StopReason, reply.StopDetail = provider.StopRefusal, msg.Refusal
	case choice.FinishReason == "length":
		reply.StopReason = provider.StopMaxTokens
	case choice.FinishReason == "content_filter":
		reply.StopReason, reply.StopDetail = provider.StopRefusal, "content filter"
	default:
		reply.StopReason = provider.StopEnd
	}
	return reply, nil
}

// describe turns API errors into short messages. The key is never included.
func describe(err error) error {
	if err == nil {
		return nil
	}
	var apiErr *openai.Error
	if errors.As(err, &apiErr) {
		msg := apiErr.Message
		switch apiErr.StatusCode {
		case http.StatusUnauthorized:
			return errors.New("openai: the API key was rejected: run `ackn4th login --provider openai` with a new one from " + KeyURL)
		case http.StatusTooManyRequests:
			if strings.Contains(apiErr.Code, "insufficient_quota") {
				return errors.New("openai: out of API credits: add billing at https://platform.openai.com/settings/organization/billing")
			}
			return errors.New("openai: rate limited even after backing off; try fewer parallel agents, or resume later: " + msg)
		case http.StatusNotFound:
			return fmt.Errorf("openai: model not available to this key: %s", msg)
		}
		return fmt.Errorf("openai: %s (HTTP %d)", cmpOr(msg, http.StatusText(apiErr.StatusCode)), apiErr.StatusCode)
	}
	return fmt.Errorf("openai: %w", err)
}

func cmpOr(a, b string) string {
	if a != "" {
		return a
	}
	return b
}
