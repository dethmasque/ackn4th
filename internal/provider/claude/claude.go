// Package claude implements provider.Provider with the Anthropic Messages API.
package claude

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/anthropics/anthropic-sdk-go"
	aconfig "github.com/anthropics/anthropic-sdk-go/config"
	"github.com/anthropics/anthropic-sdk-go/option"

	"github.com/dethmasque/ackn4th/internal/provider"
)

// Config selects the model and its limits.
type Config struct {
	Model     string
	MaxTokens int64
	Effort    string // low, medium, high, xhigh, max; empty uses the model default
	// CompactAt is the input size, in tokens, at which the API summarizes
	// the earlier conversation server-side (minimum 50000).
	CompactAt int64
}

// compactInstructions tell the server-side summarizer what an ackn4th agent
// needs to keep working after compaction.
const compactInstructions = `You are summarizing the conversation of one agent on an ackn4th team so it can keep working after the earlier conversation is removed. Keep, as concrete facts:
- the task as stated, and any guidance from the human operator, verbatim where short;
- the agent's role, the team, and what each teammate has said, asked for, or disagreed with;
- every file created, edited or deleted, by whom, and its current state as far as known; the key contents of files that matter (signatures, interfaces, decisions), not whole files;
- decisions made and why, open questions, known bugs, things not yet verified;
- the current plan and the next concrete steps;
- who has declared the task done, and any outstanding concerns.
Drop tool output that has been superseded, and anything that can be re-read from the workspace. Be specific: paths, names and numbers, not generalities.`

// Provider talks to Claude.
type Provider struct {
	client anthropic.Client
	cfg    Config
}

// baseURL is pinned: tokens only ever go to the official endpoint.
const baseURL = "https://api.anthropic.com/"

// New returns a Claude provider that authenticates with an OAuth profile
// (see auth.LoadClaudeProfile). The SDK sends its short-lived access token
// as a bearer token and refreshes it as needed. Environment variables such
// as ANTHROPIC_API_KEY, ANTHROPIC_AUTH_TOKEN and ANTHROPIC_BASE_URL are
// ignored on purpose, so nothing in the shell can swap the credential or
// redirect it.
func New(profile *aconfig.Config, cfg Config) *Provider {
	return &Provider{
		client: anthropic.NewClient(
			option.WithoutEnvironmentDefaults(),
			option.WithConfigQuiet(profile),
			option.WithBaseURL(baseURL),
			option.WithMaxRetries(4),
		),
		cfg: cfg,
	}
}

func (p *Provider) Name() string  { return "claude" }
func (p *Provider) Model() string { return p.cfg.Model }

// Verify checks the credentials by looking up the configured model.
func (p *Provider) Verify(ctx context.Context) error {
	_, err := p.client.Models.Get(ctx, p.cfg.Model, anthropic.ModelGetParams{})
	return describe(err)
}

func (p *Provider) NewSession(system provider.System, tools []provider.ToolSpec) provider.Session {
	return &session{p: p, system: systemBlocks(system), tools: toolParams(tools)}
}

func (p *Provider) RestoreSession(system provider.System, tools []provider.ToolSpec, raw json.RawMessage) (provider.Session, error) {
	s := &session{p: p, system: systemBlocks(system), tools: toolParams(tools)}
	var st state
	if err := json.Unmarshal(raw, &st); err != nil {
		return nil, fmt.Errorf("restore claude session: %w", err)
	}
	s.messages, s.pending = st.Messages, st.Pending
	return s, nil
}

// systemBlocks puts a cache breakpoint at the end of the shared part, so
// tools plus shared system prompt are one cache entry that every agent on
// the same model and settings reads; the agent's own part follows it.
func systemBlocks(s provider.System) []anthropic.BetaTextBlockParam {
	blocks := []anthropic.BetaTextBlockParam{{Text: s.Shared, CacheControl: anthropic.NewBetaCacheControlEphemeralParam()}}
	if s.Own != "" {
		blocks = append(blocks, anthropic.BetaTextBlockParam{Text: s.Own})
	}
	return blocks
}

// Prewarm writes the shared prefix to the cache with a max_tokens 0
// request: the API prefills and returns at once, billing only the cache
// write. Every setting that is part of the prompt (tools, system,
// thinking, effort) matches real requests, or the entry would never be
// read.
func (p *Provider) Prewarm(ctx context.Context, system provider.System, tools []provider.ToolSpec) (provider.Usage, error) {
	s := &session{p: p, system: systemBlocks(system), tools: toolParams(tools)}
	params := s.params([]anthropic.BetaMessageParam{anthropic.NewBetaUserMessage(anthropic.NewBetaTextBlock("warmup"))})
	params.MaxTokens = 0
	// Automatic caching would key the entry to the placeholder message;
	// the explicit breakpoint on the shared block is what matters.
	params.CacheControl = anthropic.BetaCacheControlEphemeralParam{}
	msg, err := p.client.Beta.Messages.New(ctx, params)
	if err != nil {
		return provider.Usage{}, describe(err)
	}
	u := msg.Usage
	return provider.Usage{
		InputTokens:  u.InputTokens + u.CacheReadInputTokens + u.CacheCreationInputTokens,
		CachedTokens: u.CacheReadInputTokens,
	}, nil
}

func toolParams(specs []provider.ToolSpec) []anthropic.BetaToolUnionParam {
	out := make([]anthropic.BetaToolUnionParam, len(specs))
	for i, t := range specs {
		schema := anthropic.BetaToolInputSchemaParam{
			Properties:  t.Schema["properties"],
			ExtraFields: map[string]any{"additionalProperties": false},
		}
		if req, ok := t.Schema["required"].([]string); ok {
			schema.Required = req
		}
		out[i] = anthropic.BetaToolUnionParam{OfTool: &anthropic.BetaToolParam{
			Name:        t.Name,
			Description: anthropic.String(t.Description),
			InputSchema: schema,
			// Large inputs (whole files) stream as they are generated
			// instead of arriving in one burst; the tool executor
			// validates every input before acting on it.
			EagerInputStreaming: anthropic.Bool(true),
		}}
	}
	return out
}

type state struct {
	Messages []anthropic.BetaMessageParam `json:"messages"`
	Pending  []provider.ToolCall          `json:"pending,omitempty"`
}

type session struct {
	p        *Provider
	system   []anthropic.BetaTextBlockParam
	tools    []anthropic.BetaToolUnionParam
	messages []anthropic.BetaMessageParam
	pending  []provider.ToolCall
}

func (s *session) PendingToolCalls() []provider.ToolCall { return s.pending }

func (s *session) State() (json.RawMessage, error) {
	return json.Marshal(state{Messages: s.messages, Pending: s.pending})
}

func (s *session) SendText(ctx context.Context, text string, st provider.Stream) (*provider.Reply, error) {
	return s.send(ctx, anthropic.NewBetaUserMessage(anthropic.NewBetaTextBlock(text)), st)
}

func (s *session) SendToolResults(ctx context.Context, results []provider.ToolResult, extra string, st provider.Stream) (*provider.Reply, error) {
	blocks := make([]anthropic.BetaContentBlockParamUnion, 0, len(results)+1)
	for _, r := range results {
		blocks = append(blocks, anthropic.NewBetaToolResultBlock(r.CallID, r.Content, r.IsError))
	}
	if extra != "" {
		blocks = append(blocks, anthropic.NewBetaTextBlock(extra))
	}
	return s.send(ctx, anthropic.NewBetaUserMessage(blocks...), st)
}

// send makes one request. History is only extended once the reply has fully
// arrived, so a cancelled or failed request leaves the session unchanged and
// can simply be retried. The history is append-only, which keeps thinking
// blocks valid across turns.
func (s *session) send(ctx context.Context, user anthropic.BetaMessageParam, st provider.Stream) (*provider.Reply, error) {
	msgs := append(s.messages[:len(s.messages):len(s.messages)], user)
	params := s.params(msgs)

	stream := s.p.client.Beta.Messages.NewStreaming(ctx, params)
	defer stream.Close()
	var msg anthropic.BetaMessage
	for stream.Next() {
		ev := stream.Current()
		if err := msg.Accumulate(ev); err != nil {
			return nil, err
		}
		if b, ok := ev.AsAny().(anthropic.BetaRawContentBlockStartEvent); ok && st.ToolInput != nil {
			if tu, ok := b.ContentBlock.AsAny().(anthropic.BetaToolUseBlock); ok {
				st.ToolInput(tu.Name, "")
			}
		}
		if d, ok := ev.AsAny().(anthropic.BetaRawContentBlockDeltaEvent); ok {
			switch delta := d.Delta.AsAny().(type) {
			case anthropic.BetaInputJSONDelta:
				if st.ToolInput != nil {
					st.ToolInput("", delta.PartialJSON)
				}
			case anthropic.BetaTextDelta:
				if st.Text != nil {
					st.Text(delta.Text)
				}
			case anthropic.BetaThinkingDelta:
				if st.Thinking != nil {
					st.Thinking(delta.Thinking)
				}
			}
		}
	}
	if err := stream.Err(); err != nil {
		return nil, describe(err)
	}

	in := msg.Usage.InputTokens + msg.Usage.CacheReadInputTokens + msg.Usage.CacheCreationInputTokens
	reply := &provider.Reply{
		Usage:         provider.Usage{InputTokens: in, OutputTokens: msg.Usage.OutputTokens, CachedTokens: msg.Usage.CacheReadInputTokens},
		ContextTokens: in,
	}
	if its := msg.Usage.Iterations; len(its) > 0 {
		// A compaction is billed as its own iteration; the top-level
		// usage only covers the final one.
		reply.Usage = provider.Usage{}
		for _, it := range its {
			n := it.InputTokens + it.CacheReadInputTokens + it.CacheCreationInputTokens
			reply.Usage.Add(provider.Usage{InputTokens: n, OutputTokens: it.OutputTokens, CachedTokens: it.CacheReadInputTokens})
			if it.Type == "message" {
				reply.ContextTokens = n
			}
		}
	}
	for _, block := range msg.Content {
		switch b := block.AsAny().(type) {
		case anthropic.BetaCompactionBlock:
			reply.Compacted = true
		case anthropic.BetaTextBlock:
			reply.Text += b.Text
		case anthropic.BetaToolUseBlock:
			reply.ToolCalls = append(reply.ToolCalls, provider.ToolCall{
				ID: b.ID, Name: b.Name, Input: json.RawMessage(b.JSON.Input.Raw()),
			})
		}
	}
	switch msg.StopReason {
	case anthropic.BetaStopReasonToolUse:
		reply.StopReason = provider.StopToolUse
	case anthropic.BetaStopReasonEndTurn, anthropic.BetaStopReasonStopSequence:
		reply.StopReason = provider.StopEnd
	case anthropic.BetaStopReasonMaxTokens, anthropic.BetaStopReasonModelContextWindowExceeded:
		reply.StopReason = provider.StopMaxTokens
	case anthropic.BetaStopReasonRefusal:
		reply.StopReason = provider.StopRefusal
		reply.StopDetail = string(msg.StopDetails.Category)
		if msg.StopDetails.Explanation != "" {
			reply.StopDetail += ": " + msg.StopDetails.Explanation
		}
	default:
		reply.StopReason = provider.StopOther
		reply.StopDetail = string(msg.StopReason)
	}

	s.messages = append(msgs, msg.ToParam())
	s.pending = reply.ToolCalls
	return reply, nil
}

// params builds a request. Everything except messages is identical on
// every request, which keeps the cached prefix valid.
func (s *session) params(msgs []anthropic.BetaMessageParam) anthropic.BetaMessageNewParams {
	params := anthropic.BetaMessageNewParams{
		Model:     anthropic.Model(s.p.cfg.Model),
		MaxTokens: s.p.cfg.MaxTokens,
		System:    s.system,
		Messages:  msgs,
		Tools:     s.tools,
		Thinking: anthropic.BetaThinkingConfigParamUnion{OfAdaptive: &anthropic.BetaThinkingConfigAdaptiveParam{
			Display: anthropic.BetaThinkingConfigAdaptiveDisplaySummarized,
		}},
		// Cache the growing conversation prefix between turns. (The zero
		// value would be omitted from the request.)
		CacheControl: anthropic.NewBetaCacheControlEphemeralParam(),
		// If a safety classifier declines a request, let the API re-serve
		// it on an appropriate fallback model instead of failing the turn.
		Fallbacks: anthropic.BetaFallbacksParamOfDefault(),
		// Long tasks outgrow any context window: past CompactAt input
		// tokens the API replaces the earlier conversation with a summary
		// block. Server-side compaction keeps thinking blocks valid, which
		// trimming the history here would not.
		ContextManagement: anthropic.BetaContextManagementConfigParam{
			Edits: []anthropic.BetaContextManagementConfigEditUnionParam{{
				OfCompact20260112: &anthropic.BetaCompact20260112EditParam{
					Trigger:      anthropic.BetaInputTokensTriggerParam{Value: s.p.cfg.CompactAt},
					Instructions: anthropic.String(compactInstructions),
				},
			}},
		},
		Betas: []anthropic.AnthropicBeta{
			anthropic.AnthropicBetaServerSideFallback2026_07_01,
			anthropic.AnthropicBetaCompact2026_01_12,
		},
	}
	if s.p.cfg.Effort != "" {
		params.OutputConfig = anthropic.BetaOutputConfigParam{Effort: anthropic.BetaOutputConfigEffort(s.p.cfg.Effort)}
	}
	return params
}

// describe turns API errors into short messages. Tokens are never included.
func describe(err error) error {
	var apiErr *anthropic.Error
	if errors.As(err, &apiErr) {
		switch apiErr.StatusCode {
		case 401:
			return errors.New("claude: OAuth token rejected (401): run `ackn4th login --provider claude --reauth`")
		case 403:
			return errors.New("claude: this account or workspace lacks permission (403)")
		case 404:
			return fmt.Errorf("claude: model not found (404): check providers.claude.model in config.yaml")
		case 429:
			return errors.New("claude: rate limited (429) after retries")
		}
		typ, msg, id := apiMessage(apiErr)
		switch {
		case strings.Contains(msg, "credit balance"):
			return fmt.Errorf("claude: out of API credits: %s Add credits at https://console.anthropic.com/settings/billing (a claude.ai plan doesn't cover the API), then resume the task (request %s)", msg, id)
		case msg != "":
			return fmt.Errorf("claude: %s (%d %s, request %s)", msg, apiErr.StatusCode, typ, id)
		}
		return fmt.Errorf("claude: API error %d: %s", apiErr.StatusCode, apiErr.Error())
	}
	return err
}

// apiMessage reads the error type, message and request ID from an API
// error's body, so the message leads instead of the URL and headers.
func apiMessage(e *anthropic.Error) (typ, msg, id string) {
	var body struct {
		Error struct {
			Type    string `json:"type"`
			Message string `json:"message"`
		} `json:"error"`
		RequestID string `json:"request_id"`
	}
	if json.Unmarshal([]byte(e.RawJSON()), &body) != nil {
		return "", "", ""
	}
	return body.Error.Type, body.Error.Message, cmpOr(body.RequestID, e.RequestID)
}

func cmpOr(a, b string) string {
	if a != "" {
		return a
	}
	return b
}
