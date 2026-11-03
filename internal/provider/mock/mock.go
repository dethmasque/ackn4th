// Package mock is an offline provider for the sandbox. Its agents follow a
// fixed script that exercises everything a real agent does: exploring,
// writing within the role's paths, project notes, messages to teammates,
// re-reads, edits and declaring done. It needs no login and no network,
// so a fresh clone can try every command, watch the animations, and test
// ackn4th end to end.
package mock

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/dethmasque/ackn4th/internal/provider"
)

// Provider is the mock provider.
type Provider struct {
	model string
	delay time.Duration

	mu     sync.Mutex
	warmed map[string]bool // shared prompts seen, standing in for a cache
}

// New returns a mock provider that pauses for delay before each reply, so
// live output looks like a model at work.
func New(model string, delay time.Duration) *Provider {
	return &Provider{model: model, delay: delay}
}

// Prewarm pretends to cache the shared prompt, so the sandbox shows the
// same prewarm and cache-hit lines as Claude.
func (p *Provider) Prewarm(ctx context.Context, system provider.System, tools []provider.ToolSpec) (provider.Usage, error) {
	p.warm(system.Shared)
	n := int64(len(system.Shared) / 4)
	return provider.Usage{InputTokens: n}, nil
}

// warm records a shared prompt and reports whether it was already cached.
func (p *Provider) warm(shared string) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.warmed == nil {
		p.warmed = map[string]bool{}
	}
	hit := p.warmed[shared]
	p.warmed[shared] = true
	return hit
}

func (p *Provider) Name() string                     { return "mock" }
func (p *Provider) Model() string                    { return p.model }
func (p *Provider) Verify(ctx context.Context) error { return nil }

func (p *Provider) NewSession(system provider.System, tools []provider.ToolSpec) provider.Session {
	return newSession(p, system, tools)
}

func (p *Provider) RestoreSession(system provider.System, tools []provider.ToolSpec, raw json.RawMessage) (provider.Session, error) {
	s := newSession(p, system, tools)
	if err := json.Unmarshal(raw, &s.st); err != nil {
		return nil, fmt.Errorf("restore mock session: %w", err)
	}
	return s, nil
}

type state struct {
	Turn    int                 `json:"turn"`
	Step    int                 `json:"step"`
	Seq     int                 `json:"seq"`
	Context int64               `json:"context"`
	Pending []provider.ToolCall `json:"pending,omitempty"`
}

type session struct {
	p      *Provider
	shared string
	role   string
	file   string // where this role writes, "" if read-only
	team   bool
	tools  []string
	st     state
}

var (
	roleRe     = regexp.MustCompile(`(?:on the team: the|working alone as the) [^(\n]+\(([\w+]+)\)`)
	writableRe = regexp.MustCompile(`paths matching: (.+?)\. Other paths`)
)

func newSession(p *Provider, system provider.System, tools []provider.ToolSpec) *session {
	s := &session{p: p, role: "agent", shared: system.Shared}
	if m := roleRe.FindStringSubmatch(system.Own); m != nil {
		s.role = m[1]
	}
	for _, t := range tools {
		s.tools = append(s.tools, t.Name)
	}
	s.team = slices.Contains(s.tools, "message_teammate")
	if m := writableRe.FindStringSubmatch(system.Own); m != nil {
		s.file = fileFor(pickGlob(strings.Split(m[1], ", ")), s.role)
	}
	return s
}

// pickGlob prefers a glob the mock's Markdown file fits without breaking
// the project: a directory, then a Markdown pattern, then the first.
func pickGlob(globs []string) string {
	for _, g := range globs {
		if g == "**" || (strings.HasSuffix(g, "/**") && !strings.Contains(strings.TrimSuffix(g, "/**"), "*")) {
			return g
		}
	}
	for _, g := range globs {
		if strings.HasSuffix(g, ".md") {
			return g
		}
	}
	return globs[0]
}

// fileFor picks a file this role may write, from a writable glob.
func fileFor(glob, role string) string {
	name := "mock-" + role + ".md"
	switch {
	case glob == "**":
		return "mock/" + name
	case strings.HasSuffix(glob, "/**"):
		return strings.TrimSuffix(glob, "**") + name
	case strings.Contains(glob, "*"):
		g := strings.ReplaceAll(glob, "**/", "")
		return strings.Replace(g, "*", "mock-"+role, 1)
	}
	return glob
}

func (s *session) PendingToolCalls() []provider.ToolCall { return s.st.Pending }

func (s *session) State() (json.RawMessage, error) { return json.Marshal(s.st) }

func (s *session) SendText(ctx context.Context, text string, st provider.Stream) (*provider.Reply, error) {
	s.st.Turn++
	s.st.Step = 0
	return s.reply(ctx, text, st)
}

func (s *session) SendToolResults(ctx context.Context, results []provider.ToolResult, extra string, st provider.Stream) (*provider.Reply, error) {
	var b strings.Builder
	for _, r := range results {
		b.WriteString(r.Content)
	}
	b.WriteString(extra)
	return s.reply(ctx, b.String(), st)
}

// plan is the script for the current turn: tool calls, then a closing
// message.
func (s *session) plan() []provider.ToolCall {
	var calls []provider.ToolCall
	add := func(name string, in map[string]any) {
		if slices.Contains(s.tools, name) {
			raw, _ := json.Marshal(in)
			calls = append(calls, provider.ToolCall{Name: name, Input: raw})
		}
	}
	if s.st.Turn == 1 {
		if s.team {
			add("message_teammate", map[string]any{"to": "all", "message": fmt.Sprintf("Hi from the mock %s. I'll write %s.", s.role, cmpOr(s.file, "nothing (read-only)"))})
		}
		add("list_dir", map[string]any{"depth": 2})
		if s.file != "" {
			add("write_file", map[string]any{"path": s.file, "content": fmt.Sprintf(
				"# Mock %s\n\nWritten by ackn4th's offline mock provider.\n\n- turn 1\n<!-- mock:end -->\n", s.role)})
		}
		add("update_notes", map[string]any{"section": "Mock " + s.role, "content": fmt.Sprintf("The mock %s writes %s.", s.role, cmpOr(s.file, "nothing"))})
		add("add_user_todo", map[string]any{"text": fmt.Sprintf("Review what the mock %s wrote.", s.role)})
		return calls
	}
	if s.file != "" {
		add("read_file", map[string]any{"path": s.file})
		add("edit_file", map[string]any{"path": s.file, "old_string": "<!-- mock:end -->",
			"new_string": fmt.Sprintf("- turn %d\n<!-- mock:end -->", s.st.Turn)})
	} else {
		add("search_files", map[string]any{"pattern": "mock"})
	}
	add("run_command", map[string]any{"command": "ls"})
	add("declare_done", map[string]any{"summary": fmt.Sprintf("The mock %s finished its script in %d turns.", s.role, s.st.Turn)})
	return calls
}

func cmpOr(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

func (s *session) reply(ctx context.Context, input string, st provider.Stream) (*provider.Reply, error) {
	s.st.Context += int64(len(input)/4) + 400
	if st.Thinking != nil {
		st.Thinking("Following the mock script.")
	}
	select {
	case <-time.After(s.p.delay):
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	reply := &provider.Reply{
		Usage:         provider.Usage{InputTokens: s.st.Context, OutputTokens: 120},
		ContextTokens: s.st.Context,
	}
	if s.p.warm(s.shared) {
		reply.Usage.CachedTokens = min(int64(len(s.shared)/4), s.st.Context)
	}
	plan := s.plan()
	if s.st.Step < len(plan) {
		call := plan[s.st.Step]
		s.st.Step++
		s.st.Seq++
		call.ID = fmt.Sprintf("mock-%d", s.st.Seq)
		reply.ToolCalls = []provider.ToolCall{call}
		reply.StopReason = provider.StopToolUse
		s.st.Pending = reply.ToolCalls
		return reply, nil
	}
	reply.Text = fmt.Sprintf("Mock %s, turn %d: script step done.", s.role, s.st.Turn)
	if s.st.Turn == 1 {
		reply.Text += " Next turn I'll revise my file and declare done."
	}
	for _, w := range strings.SplitAfter(reply.Text, " ") {
		if st.Text != nil {
			st.Text(w)
		}
	}
	reply.StopReason = provider.StopEnd
	s.st.Pending = nil
	return reply, nil
}
