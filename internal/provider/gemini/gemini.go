// Package gemini implements provider.Provider with Gemini: on the Gemini
// API with an AI Studio key, or on Vertex AI with short-lived Google OAuth
// access tokens.
package gemini

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"sync"

	gauth "cloud.google.com/go/auth"
	"google.golang.org/genai"

	"github.com/dethmasque/ackn4th/internal/provider"
	"github.com/dethmasque/ackn4th/internal/ratelimit"
)

// Config selects the model, its limits, and how to reach Gemini.
type Config struct {
	// APIKey selects the Gemini API; otherwise Vertex AI with OAuth.
	APIKey    string
	Model     string
	MaxTokens int64
	Project   string // GCP project with the Vertex AI API enabled
	Location  string // Vertex AI location, e.g. global or us-central1
	// Effort maps to a Gemini 3 thinking level; empty uses the model default.
	Effort string
	// CompactAt is the prompt size, in tokens, past which the conversation
	// is summarized before the agent's next turn.
	CompactAt int64
}

// thinkingLevel maps an ackn4th effort to a Gemini thinking level.
func thinkingLevel(effort string) genai.ThinkingLevel {
	switch effort {
	case "low":
		return genai.ThinkingLevelLow
	case "medium":
		return genai.ThinkingLevelMedium
	case "high", "xhigh", "max":
		return genai.ThinkingLevelHigh
	}
	return ""
}

// Provider talks to Gemini.
type Provider struct {
	creds *gauth.Credentials
	cfg   Config

	once      sync.Once
	client    *genai.Client
	clientErr error
}

// New returns a Gemini provider using an API key (cfg.APIKey) or Google
// OAuth credentials (see auth.LoadGoogleCredentials) for Vertex AI. Key,
// project, location, credentials and endpoint are all set explicitly, so
// GOOGLE_CLOUD_PROJECT, GOOGLE_API_KEY, GEMINI_API_KEY, base-URL and
// similar variables are never consulted.
func New(creds *gauth.Credentials, cfg Config) *Provider {
	if cfg.Location == "" {
		cfg.Location = "global"
	}
	return &Provider{creds: creds, cfg: cfg}
}

var validLocation = regexp.MustCompile(`^[a-z]+(-[a-z0-9]+)*$`)

// endpoint returns the official Vertex AI endpoint for a location.
func endpoint(location string) (string, error) {
	if !validLocation.MatchString(location) {
		return "", fmt.Errorf("gemini: invalid Vertex AI location %q", location)
	}
	switch location {
	case "global":
		return "https://aiplatform.googleapis.com/", nil
	case "us", "eu":
		return "https://aiplatform." + location + ".rep.googleapis.com/", nil
	}
	return "https://" + location + "-aiplatform.googleapis.com/", nil
}

func (p *Provider) Name() string  { return "gemini" }
func (p *Provider) Model() string { return p.cfg.Model }

func (p *Provider) conn(ctx context.Context) (*genai.Client, error) {
	p.once.Do(func() {
		if p.cfg.APIKey != "" {
			p.client, p.clientErr = genai.NewClient(ctx, &genai.ClientConfig{
				Backend: genai.BackendGeminiAPI,
				APIKey:  p.cfg.APIKey,
				// Agents sharing a key share its rate limits.
				HTTPClient:  ratelimit.Client(ratelimit.For("gemini:" + ratelimit.Fingerprint(p.cfg.APIKey))),
				HTTPOptions: genai.HTTPOptions{BaseURL: "https://generativelanguage.googleapis.com/"},
			})
			return
		}
		if p.cfg.Project == "" {
			p.clientErr = errors.New("gemini: set providers.gemini.project in config.yaml (a GCP project with the Vertex AI API enabled), or run `ackn4th login --gcp-project <id>`")
			return
		}
		base, err := endpoint(p.cfg.Location)
		if err != nil {
			p.clientErr = err
			return
		}
		p.client, p.clientErr = genai.NewClient(ctx, &genai.ClientConfig{
			Backend:     genai.BackendVertexAI,
			Project:     p.cfg.Project,
			Location:    p.cfg.Location,
			Credentials: p.creds,
			HTTPOptions: genai.HTTPOptions{
				BaseURL: base,
				// User credentials bill quota to this project.
				Headers: http.Header{"X-Goog-User-Project": []string{p.cfg.Project}},
			},
		})
	})
	return p.client, p.clientErr
}

// Verify checks the key by looking up the configured model.
func (p *Provider) Verify(ctx context.Context) error {
	c, err := p.conn(ctx)
	if err != nil {
		return err
	}
	_, err = c.Models.Get(ctx, p.cfg.Model, nil)
	return describe(err)
}

func (p *Provider) NewSession(system provider.System, tools []provider.ToolSpec) provider.Session {
	return &session{p: p, system: system.Text(), tools: toolDecls(tools)}
}

func (p *Provider) RestoreSession(system provider.System, tools []provider.ToolSpec, raw json.RawMessage) (provider.Session, error) {
	var st state
	if err := json.Unmarshal(raw, &st); err != nil {
		return nil, fmt.Errorf("restore gemini session: %w", err)
	}
	return &session{p: p, system: system.Text(), tools: toolDecls(tools), contents: st.Contents, pending: st.Pending, seq: st.Seq, context: st.Context}, nil
}

func toolDecls(specs []provider.ToolSpec) []*genai.Tool {
	decls := make([]*genai.FunctionDeclaration, len(specs))
	for i, t := range specs {
		decls[i] = &genai.FunctionDeclaration{
			Name:                 t.Name,
			Description:          t.Description,
			ParametersJsonSchema: t.Schema,
		}
	}
	return []*genai.Tool{{FunctionDeclarations: decls}}
}

type state struct {
	Contents []*genai.Content    `json:"contents"`
	Pending  []provider.ToolCall `json:"pending,omitempty"`
	Seq      int                 `json:"seq"`
	Context  int64               `json:"context,omitempty"`
}

type session struct {
	p        *Provider
	system   string
	tools    []*genai.Tool
	contents []*genai.Content
	pending  []provider.ToolCall
	seq      int   // for synthesizing call IDs when the API omits them
	context  int64 // prompt tokens of the last request
}

func (s *session) PendingToolCalls() []provider.ToolCall { return s.pending }

func (s *session) State() (json.RawMessage, error) {
	return json.Marshal(state{Contents: s.contents, Pending: s.pending, Seq: s.seq, Context: s.context})
}

func (s *session) SendText(ctx context.Context, text string, st provider.Stream) (*provider.Reply, error) {
	// A new turn is the only safe point to compact: no tool round is open.
	var cost *provider.Usage
	if s.p.cfg.CompactAt > 0 && s.context >= s.p.cfg.CompactAt && len(s.pending) == 0 {
		u, err := s.compact(ctx)
		if err != nil {
			return nil, err
		}
		cost = &u
	}
	reply, err := s.send(ctx, genai.NewContentFromText(text, genai.RoleUser), st)
	if reply != nil && cost != nil {
		reply.Compacted = true
		reply.Usage.Add(*cost)
	}
	return reply, err
}

// compact replaces the whole history with a summary written by the model.
// Replacing everything (rather than keeping a recent tail) means no model
// turn is replayed out of the context that produced it, so thought
// signatures stay valid. The summary request reuses the system prompt and
// tools so it shares the cached prefix.
func (s *session) compact(ctx context.Context) (provider.Usage, error) {
	var cost provider.Usage
	c, err := s.p.conn(ctx)
	if err != nil {
		return cost, err
	}
	contents := append(s.contents[:len(s.contents):len(s.contents)], genai.NewContentFromText(provider.CompactPrompt, genai.RoleUser))
	cfg := s.config()
	cfg.ToolConfig = &genai.ToolConfig{FunctionCallingConfig: &genai.FunctionCallingConfig{Mode: genai.FunctionCallingConfigModeNone}}
	resp, err := c.Models.GenerateContent(ctx, s.p.cfg.Model, contents, cfg)
	if err != nil {
		return cost, fmt.Errorf("compacting context: %w", describe(err))
	}
	if u := resp.UsageMetadata; u != nil {
		cost = provider.Usage{InputTokens: int64(u.PromptTokenCount), OutputTokens: int64(u.CandidatesTokenCount + u.ThoughtsTokenCount)}
	}
	var summary string
	if len(resp.Candidates) > 0 && resp.Candidates[0].Content != nil {
		for _, part := range resp.Candidates[0].Content.Parts {
			if !part.Thought {
				summary += part.Text
			}
		}
	}
	if summary == "" {
		return cost, errors.New("compacting context: the model returned no summary")
	}
	s.contents = []*genai.Content{
		genai.NewContentFromText(provider.CompactedIntro+summary, genai.RoleUser),
		genai.NewContentFromText("Understood. I will continue from this summary and re-read files as needed.", genai.RoleModel),
	}
	s.context = 0
	return cost, nil
}

func (s *session) config() *genai.GenerateContentConfig {
	cfg := &genai.GenerateContentConfig{
		SystemInstruction: genai.NewContentFromText(s.system, genai.RoleUser),
		Tools:             s.tools,
		MaxOutputTokens:   int32(s.p.cfg.MaxTokens),
		ThinkingConfig:    &genai.ThinkingConfig{IncludeThoughts: true, ThinkingLevel: thinkingLevel(s.p.cfg.Effort)},
	}
	return cfg
}

func (s *session) SendToolResults(ctx context.Context, results []provider.ToolResult, extra string, st provider.Stream) (*provider.Reply, error) {
	parts := make([]*genai.Part, 0, len(results)+1)
	for i, r := range results {
		resp := map[string]any{"output": r.Content}
		if r.IsError {
			resp = map[string]any{"error": r.Content}
		}
		fr := &genai.FunctionResponse{Name: r.Name, Response: resp}
		// Echo the API's own call ID back; synthesized IDs stay local.
		if i < len(s.pending) && s.pending[i].ID == r.CallID && !synthetic(r.CallID) {
			fr.ID = r.CallID
		}
		parts = append(parts, &genai.Part{FunctionResponse: fr})
	}
	if extra != "" {
		parts = append(parts, genai.NewPartFromText(extra))
	}
	return s.send(ctx, genai.NewContentFromParts(parts, genai.RoleUser), st)
}

const idPrefix = "ackn4th-call-"

func synthetic(id string) bool { return len(id) > len(idPrefix) && id[:len(idPrefix)] == idPrefix }

// send makes one request, extending the history only on success. Model
// turns are stored verbatim so thought signatures survive.
func (s *session) send(ctx context.Context, user *genai.Content, st provider.Stream) (*provider.Reply, error) {
	c, err := s.p.conn(ctx)
	if err != nil {
		return nil, err
	}
	contents := append(s.contents[:len(s.contents):len(s.contents)], user)
	resp, err := c.Models.GenerateContent(ctx, s.p.cfg.Model, contents, s.config())
	if err != nil {
		return nil, describe(err)
	}

	reply := &provider.Reply{}
	if u := resp.UsageMetadata; u != nil {
		reply.Usage = provider.Usage{
			InputTokens:  int64(u.PromptTokenCount),
			OutputTokens: int64(u.CandidatesTokenCount + u.ThoughtsTokenCount),
			// Implicit caching: Gemini reuses a recent identical prompt
			// prefix (here the tools and shared system prompt) on its own.
			CachedTokens: int64(u.CachedContentTokenCount),
		}
		reply.ContextTokens = int64(u.PromptTokenCount)
		s.context = reply.ContextTokens
	}
	if len(resp.Candidates) == 0 || resp.Candidates[0].Content == nil {
		reply.StopReason = provider.StopRefusal
		if resp.PromptFeedback != nil {
			reply.StopDetail = string(resp.PromptFeedback.BlockReason)
		}
		// Record an empty model turn so user/model turns keep alternating.
		s.contents = append(contents, genai.NewContentFromText("(no response)", genai.RoleModel))
		s.pending = nil
		return reply, nil
	}

	cand := resp.Candidates[0]
	for _, part := range cand.Content.Parts {
		switch {
		case part.FunctionCall != nil:
			fc := part.FunctionCall
			args, err := json.Marshal(fc.Args)
			if err != nil {
				return nil, err
			}
			id := fc.ID
			if id == "" {
				s.seq++
				id = fmt.Sprintf("%s%d", idPrefix, s.seq)
			}
			reply.ToolCalls = append(reply.ToolCalls, provider.ToolCall{ID: id, Name: fc.Name, Input: args})
		case part.Thought && part.Text != "":
			if st.Thinking != nil {
				st.Thinking(part.Text)
			}
		case part.Text != "":
			reply.Text += part.Text
			if st.Text != nil {
				st.Text(part.Text)
			}
		}
	}
	switch {
	case len(reply.ToolCalls) > 0:
		reply.StopReason = provider.StopToolUse
	case cand.FinishReason == genai.FinishReasonStop || cand.FinishReason == "":
		reply.StopReason = provider.StopEnd
	case cand.FinishReason == genai.FinishReasonMaxTokens:
		reply.StopReason = provider.StopMaxTokens
	case cand.FinishReason == genai.FinishReasonSafety || cand.FinishReason == genai.FinishReasonProhibitedContent:
		reply.StopReason = provider.StopRefusal
		reply.StopDetail = string(cand.FinishReason)
	default:
		reply.StopReason = provider.StopOther
		reply.StopDetail = string(cand.FinishReason)
	}
	if cand.Content.Role == "" {
		cand.Content.Role = genai.RoleModel
	}
	s.contents = append(contents, cand.Content)
	s.pending = reply.ToolCalls
	return reply, nil
}

// describe turns API errors into short messages. Tokens are never included.
func describe(err error) error {
	if err == nil {
		return nil
	}
	var apiErr genai.APIError
	if errors.As(err, &apiErr) {
		switch apiErr.Code {
		case http.StatusBadRequest:
			if apiErr.Status == "INVALID_ARGUMENT" && apiErr.Message != "" {
				return fmt.Errorf("gemini: %s", apiErr.Message)
			}
		case http.StatusUnauthorized, http.StatusForbidden:
			return fmt.Errorf("gemini: access denied (%d): check the Vertex AI API is enabled for the project and your account can use it, or run `ackn4th login --provider gemini --reauth`: %s", apiErr.Code, apiErr.Message)
		case http.StatusNotFound:
			return errors.New("gemini: model not found (404): check providers.gemini.model and providers.gemini.location in config.yaml")
		case http.StatusTooManyRequests:
			return errors.New("gemini: rate limited (429)")
		}
		return fmt.Errorf("gemini: API error %d: %s", apiErr.Code, apiErr.Message)
	}
	return err
}
