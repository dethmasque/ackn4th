// Package cliagent runs agents through a coding-agent CLI (Codex, Cursor,
// Copilot) on the user's own plan for that tool.
//
// Unlike claude-code, these CLIs can't switch their built-in tools off, so
// the whole CLI runs inside ackn4th's OS sandbox (internal/sandbox):
// writes only to the workspace, the agent's scratch directory and the
// CLI's own state directory; secrets unreadable; the environment rebuilt
// from an allowlist. The network stays on, because the CLI must reach its
// service. ackn4th's own tools (declare_done, message_teammate, the file
// tools, ...) are served over MCP from 127.0.0.1 (internal/provider/mcpbridge),
// so teamwork and consensus work as with every provider. Files the CLI
// changes with its own tools are credited to the agent, like a command's.
//
// Each request runs the CLI once, non-interactively, resuming the CLI's
// own session after the first. A Driver adapts one CLI: its arguments, its
// JSON event stream and its sign-in check.
package cliagent

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/dethmasque/ackn4th/internal/provider"
	"github.com/dethmasque/ackn4th/internal/provider/mcpbridge"
	"github.com/dethmasque/ackn4th/internal/sandbox"
)

// DefaultModel lets the CLI pick its own model.
const DefaultModel = "default"

// Request is one run of the CLI.
type Request struct {
	Prompt string
	// Resume is the CLI's session ID from an earlier run; empty starts one.
	Resume string
	Model  string // DefaultModel for the CLI's choice
	Effort string
	// MCPURL and MCPToken reach ackn4th's tools.
	MCPURL, MCPToken string
	Workspace        string
	// RunDir is a private directory for this run's files (configs, usage
	// reports), inside the agent's scratch directory.
	RunDir string
}

// Spec is the process to start.
type Spec struct {
	Argv []string
	Env  []string
	// Dir is the working directory; empty means the workspace.
	Dir string
}

// Turn collects what one run produced. Drivers fill it from events.
type Turn struct {
	Stream    provider.Stream
	Text      strings.Builder
	Usage     provider.Usage
	Context   int64
	SessionID string
	// Done is set by the event that says the run finished well; Error by
	// one that says it failed.
	Done  bool
	Error string
}

// Say appends a finished message to the reply text.
func (t *Turn) Say(s string) {
	if strings.TrimSpace(s) == "" {
		return
	}
	if t.Text.Len() > 0 {
		t.Text.WriteString("\n\n")
	}
	t.Text.WriteString(s)
}

// Act reports the CLI's own tool use.
func (t *Turn) Act(tool, detail string, failed bool) {
	if t.Stream.Activity != nil {
		t.Stream.Activity(tool, OneLine(detail, 160), failed)
	}
}

// Driver adapts one CLI.
type Driver interface {
	// Name is the provider name, e.g. "codex".
	Name() string
	// Bin finds the executable.
	Bin() (string, error)
	// Keep lists the CLI's state directories, relative to home, that it
	// must be able to read and write inside the sandbox.
	Keep() []string
	// Keychain reports whether the CLI keeps its login in the OS keychain.
	Keychain() bool
	Command(bin string, r Request) (Spec, error)
	// Parse reads one line of the CLI's JSON output.
	Parse(line []byte, t *Turn)
	// Finish runs after the process exits, e.g. to read a usage report.
	Finish(r Request, t *Turn)
	// Status describes the signed-in account, or fails when signed out.
	Status(ctx context.Context) (string, error)
	// Login is the command that signs in, for `ackn4th login`.
	Login(bin string) []string
	// Notes tells the model anything particular about how this CLI sees
	// the workspace; may be empty.
	Notes(workspace string) string
}

// Provider runs one agent through a CLI.
type Provider struct {
	d     Driver
	model string
	eff   string
	conf  *provider.Confinement
}

// New returns a provider for d.
func New(d Driver, model, effort string) *Provider {
	return &Provider{d: d, model: model, eff: effort}
}

func (p *Provider) Name() string  { return p.d.Name() }
func (p *Provider) Model() string { return p.model }

// Confine receives the agent's sandbox from the runner.
func (p *Provider) Confine(c provider.Confinement) { p.conf = &c }

// Verify checks the CLI is installed and signed in, and that it can be
// sandboxed here.
func (p *Provider) Verify(ctx context.Context) error {
	if _, err := p.d.Bin(); err != nil {
		return err
	}
	if err := sandbox.Available(); err != nil {
		return fmt.Errorf("%s runs inside ackn4th's sandbox, which isn't available here: %w", p.d.Name(), err)
	}
	if err := sandbox.Loopback(); err != nil {
		return fmt.Errorf("%s: %w", p.d.Name(), err)
	}
	_, err := p.d.Status(ctx)
	return err
}

func (p *Provider) NewSession(system provider.System, tools []provider.ToolSpec) provider.Session {
	return &session{p: p, system: system, tools: tools}
}

type state struct {
	SessionID string `json:"session_id,omitempty"`
}

func (p *Provider) RestoreSession(system provider.System, tools []provider.ToolSpec, raw json.RawMessage) (provider.Session, error) {
	var st state
	if err := json.Unmarshal(raw, &st); err != nil {
		return nil, fmt.Errorf("%s: saved session is not from %s: %w", p.d.Name(), p.d.Name(), err)
	}
	return &session{p: p, system: system, tools: tools, id: st.SessionID}, nil
}

type session struct {
	p      *Provider
	system provider.System
	tools  []provider.ToolSpec
	id     string

	proc    *proc
	waiting []*mcpbridge.Call
}

type proc struct {
	cmd    *exec.Cmd
	lines  chan []byte // closed when the process exits
	bridge *mcpbridge.Bridge
	stderr *tail
	turn   *Turn
	req    Request
}

func (s *session) PendingToolCalls() []provider.ToolCall {
	var out []provider.ToolCall
	for _, c := range s.waiting {
		out = append(out, c.ToolCall)
	}
	return out
}

func (s *session) State() (json.RawMessage, error) { return json.Marshal(state{SessionID: s.id}) }

// preamble tells the model how it is running. The CLI has its own system
// prompt, so ackn4th's goes at the start of the conversation.
func (s *session) preamble() string {
	return "<ackn4th>\n" + s.system.Text() + "\n\n" +
		"You are running inside " + s.p.d.Name() + ", with its own tools for reading, editing and running things in the workspace, confined to it by ackn4th's sandbox. " +
		"ackn4th's tools come from the MCP server named \"" + mcpbridge.Server + "\": use them for teamwork (message_teammate, declare_done, add_user_todo, update_notes). " +
		"End every turn with a short message for your teammates; call declare_done only when the whole task is finished.\n" +
		s.p.d.Notes(s.p.conf.Sandbox.Workspace) + "</ackn4th>\n\n"
}

func (s *session) SendText(ctx context.Context, text string, st provider.Stream) (*provider.Reply, error) {
	if len(s.waiting) > 0 || s.proc != nil {
		if err := s.abandon(ctx); err != nil {
			return nil, err
		}
	}
	if s.p.conf == nil {
		return nil, fmt.Errorf("%s: this agent has no sandbox (the runner didn't confine it)", s.p.d.Name())
	}
	if s.id == "" {
		text = s.preamble() + text
	}
	if err := s.start(text, st); err != nil {
		return nil, err
	}
	return s.wait(ctx)
}

func (s *session) SendToolResults(ctx context.Context, results []provider.ToolResult, extra string, st provider.Stream) (*provider.Reply, error) {
	if s.proc == nil {
		return nil, fmt.Errorf("%s: no tool calls are waiting (the CLI restarted)", s.p.d.Name())
	}
	s.proc.turn.Stream = st
	byID := map[string]provider.ToolResult{}
	for _, r := range results {
		byID[r.CallID] = r
	}
	for i, c := range s.waiting {
		r, ok := byID[c.ID]
		if !ok {
			r = provider.ToolResult{CallID: c.ID, Name: c.Name, IsError: true, Content: "Error: this call was not run."}
		}
		if extra != "" && i == len(s.waiting)-1 {
			r.Content = strings.TrimSpace(r.Content + "\n\n" + extra)
		}
		c.Result <- r
	}
	s.waiting = nil
	return s.wait(ctx)
}

// abandon refuses waiting calls and lets the previous run finish.
func (s *session) abandon(ctx context.Context) error {
	refuse := func(c *mcpbridge.Call) {
		c.Result <- provider.ToolResult{CallID: c.ID, Name: c.Name, IsError: true,
			Content: "Error: ackn4th ended your turn before running this. Do not call more tools; reply briefly."}
	}
	for _, c := range s.waiting {
		refuse(c)
	}
	s.waiting = nil
	pr := s.proc
	if pr == nil {
		return nil
	}
	timeout := time.After(2 * time.Minute)
	for {
		select {
		case <-ctx.Done():
			s.stop()
			return ctx.Err()
		case <-timeout:
			s.stop()
			return nil
		case c := <-pr.bridge.Calls:
			refuse(c)
		case _, ok := <-pr.lines:
			if !ok {
				s.stop()
				return nil
			}
		}
	}
}

func (s *session) start(prompt string, st provider.Stream) error {
	d := s.p.d
	if s.p.conf == nil {
		return fmt.Errorf("%s: this agent has no sandbox (the runner didn't confine it)", d.Name())
	}
	bin, err := d.Bin()
	if err != nil {
		return err
	}
	br, err := mcpbridge.Start(s.tools, "")
	if err != nil {
		return fmt.Errorf("%s: %w", d.Name(), err)
	}
	box := s.p.conf.Sandbox
	runDir := filepath.Join(box.Scratch, "cli")
	if err := os.MkdirAll(runDir, 0o700); err != nil {
		br.Close()
		return err
	}
	req := Request{
		Prompt: prompt, Resume: s.id, Model: s.p.model, Effort: s.p.eff,
		MCPURL: br.URL, MCPToken: br.Token, Workspace: box.Workspace, RunDir: runDir,
	}
	spec, err := d.Command(bin, req)
	if err != nil {
		br.Close()
		return err
	}
	home, _ := os.UserHomeDir()
	box.Network = true // the CLI talks to its service
	box.RealHome = true
	box.Keychain = d.Keychain()
	box.Env = append(box.Env, spec.Env...)
	box.Keep = nil
	for _, k := range d.Keep() {
		box.Keep = append(box.Keep, filepath.Join(home, k))
	}
	if spec.Dir != "" && spec.Dir != box.Workspace {
		// A CLI that runs from its own folder (Cursor) can't save files
		// there: they belong in the workspace.
		box.Fixed = []string{spec.Dir}
	}
	cmd, err := sandbox.Command(box, spec.Argv)
	if err != nil {
		br.Close()
		return fmt.Errorf("%s: %w", d.Name(), err)
	}
	if spec.Dir != "" {
		cmd.Dir = spec.Dir
	}
	pr := &proc{cmd: cmd, lines: make(chan []byte, 64), bridge: br, stderr: &tail{}, turn: &Turn{Stream: st}, req: req}
	cmd.Stdin = nil
	cmd.Stderr = pr.stderr
	out, err := cmd.StdoutPipe()
	if err != nil {
		br.Close()
		return err
	}
	if err := cmd.Start(); err != nil {
		br.Close()
		return fmt.Errorf("%s: starting %s: %w", d.Name(), filepath.Base(bin), err)
	}
	go func() {
		sc := bufio.NewScanner(out)
		sc.Buffer(nil, 64<<20)
		for sc.Scan() {
			pr.lines <- bytes.Clone(sc.Bytes())
		}
		cmd.Wait()
		close(pr.lines)
	}()
	s.proc = pr
	return nil
}

func (s *session) stop() {
	pr := s.proc
	if pr == nil {
		return
	}
	s.proc = nil
	pr.bridge.Close()
	sandbox.Kill(pr.cmd)
	s.waiting = nil
}

// gather is how long to wait for more tool calls after one arrives, so
// calls made together reach the runner together.
const gather = 60 * time.Millisecond

// wait reads the CLI's output until it calls ackn4th's tools or exits.
// Whatever the CLI changes meanwhile is credited to the agent.
func (s *session) wait(ctx context.Context) (reply *provider.Reply, err error) {
	s.p.conf.Credit(func() { reply, err = s.waitRaw(ctx) })
	return reply, err
}

func (s *session) waitRaw(ctx context.Context) (*provider.Reply, error) {
	pr := s.proc
	d := s.p.d
	var batch <-chan time.Time
	toolReply := func() *provider.Reply {
		r := &provider.Reply{Text: pr.turn.Text.String(), StopReason: provider.StopToolUse}
		pr.turn.Text.Reset()
		for _, c := range s.waiting {
			r.ToolCalls = append(r.ToolCalls, c.ToolCall)
		}
		return r
	}
	for {
		select {
		case <-ctx.Done():
			s.stop()
			return nil, ctx.Err()
		case c := <-pr.bridge.Calls:
			s.waiting = append(s.waiting, c)
			batch = time.After(gather)
		case <-batch:
			return toolReply(), nil
		case line, ok := <-pr.lines:
			if ok {
				d.Parse(line, pr.turn)
				if pr.turn.SessionID != "" {
					s.id = pr.turn.SessionID
				}
				continue
			}
			// The process exited.
			if len(s.waiting) > 0 {
				return toolReply(), nil
			}
			d.Finish(pr.req, pr.turn)
			t := pr.turn
			if t.SessionID != "" {
				s.id = t.SessionID
			}
			code := pr.cmd.ProcessState.ExitCode()
			stderr := strings.TrimSpace(pr.stderr.String())
			s.stop()
			if t.Error != "" || (!t.Done && code != 0) {
				msg := cmpOr(t.Error, stderr, fmt.Sprintf("exited with status %d", code))
				return nil, explain(d.Name(), msg)
			}
			return &provider.Reply{Text: t.Text.String(), StopReason: provider.StopEnd, Usage: t.Usage, ContextTokens: t.Context}, nil
		}
	}
}

// explain turns a CLI failure into an error, with a hint for the usual
// causes.
func explain(name, msg string) error {
	msg = OneLine(msg, 600)
	low := strings.ToLower(msg)
	switch {
	case strings.Contains(low, "login") || strings.Contains(low, "unauthorized") || strings.Contains(low, "not logged in") || strings.Contains(low, "authenticat"):
		msg += " · sign in again: `ackn4th login --provider " + name + "`"
	case strings.Contains(low, "limit") || strings.Contains(low, "quota") || strings.Contains(low, "credits"):
		msg += " · your " + name + " plan's usage limit; resume the task once it resets"
	case strings.Contains(low, "operation not permitted") || strings.Contains(low, "eperm") || strings.Contains(low, "sandbox"):
		msg += " · ackn4th's sandbox blocked it; please report this with the message above"
	}
	return errors.New(name + ": " + msg)
}

func cmpOr(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}

func OneLine(s string, n int) string {
	s = strings.Join(strings.Fields(s), " ")
	if r := []rune(s); len(r) > n {
		return string(r[:n-1]) + "…"
	}
	return s
}

// tail keeps the last few KB written to it.
type tail struct {
	mu  sync.Mutex
	buf []byte
}

func (t *tail) Write(p []byte) (int, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.buf = append(t.buf, p...)
	if len(t.buf) > 4096 {
		t.buf = t.buf[len(t.buf)-4096:]
	}
	return len(p), nil
}

func (t *tail) String() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	return string(t.buf)
}

var _ io.Writer = (*tail)(nil)

// FindBin looks for name on PATH, then in extra locations.
func FindBin(name string, extra ...string) (string, error) {
	if p, err := exec.LookPath(name); err == nil {
		return p, nil
	}
	home, _ := os.UserHomeDir()
	for _, e := range extra {
		e = strings.Replace(e, "~", home, 1)
		if fi, err := os.Stat(e); err == nil && !fi.IsDir() && fi.Mode()&0o111 != 0 {
			return e, nil
		}
	}
	return "", fmt.Errorf("`%s` isn't installed (or not on PATH)", name)
}
