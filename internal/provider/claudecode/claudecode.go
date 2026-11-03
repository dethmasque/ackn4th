// Package claudecode runs agents on Claude through a local Claude Code
// install (`claude -p`), so a Claude subscription can power them instead
// of API credits.
//
// Claude Code runs the model loop; ackn4th keeps the tools. Each session
// starts `claude -p` with every built-in tool switched off and the user's
// own settings, hooks, plugins and MCP servers ignored. The only tools the
// model sees are ackn4th's, served over MCP from a listener on 127.0.0.1
// that only this process can authenticate to. A tool call arriving there
// is handed to the runner as an ordinary ToolCall and its result is sent
// back as the MCP response, so the workspace jail, file ownership, done
// votes and events work exactly as with the other providers.
//
// ackn4th never sees or stores Claude Code's credentials: Claude Code uses
// its own login (`claude auth login`).
package claudecode

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
	"runtime"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/dethmasque/ackn4th/internal/provider"
	"github.com/dethmasque/ackn4th/internal/provider/mcpbridge"
	"github.com/dethmasque/ackn4th/internal/sandbox"
)

// Name is the provider's name in config and task files.
const Name = "claude-code"

// server is the MCP server name; Claude Code prefixes tool names with
// mcp__<server>__.
const server = "ackn4th"

const toolPrefix = "mcp__" + server + "__"

// Config selects the model and where Claude Code runs.
type Config struct {
	Model  string
	Effort string
	// Bin is the claude executable; empty finds it on PATH.
	Bin string
	// Dir is the working directory for Claude Code outside a task. In a
	// task it is the task's workspace (see Confine). Its built-in tools are
	// off, so it writes nothing there, but it files the session
	// transcripts by directory, and resuming needs the same one.
	Dir string
}

// Provider starts Claude Code sessions.
type Provider struct{ cfg Config }

// New returns a provider for cfg.
func New(cfg Config) *Provider { return &Provider{cfg: cfg} }

func (p *Provider) Name() string  { return Name }
func (p *Provider) Model() string { return p.cfg.Model }

// Install says how to get Claude Code.
const Install = "https://claude.com/claude-code (then `claude auth login`)"

func (p *Provider) bin() (string, error) {
	name := p.cfg.Bin
	if name == "" {
		name = "claude"
	}
	path, err := exec.LookPath(name)
	if err != nil {
		return "", fmt.Errorf("claude-code needs the `claude` CLI: %s", Install)
	}
	return path, nil
}

// Account is Claude Code's sign-in, from `claude auth status`.
type Account struct {
	LoggedIn     bool   `json:"loggedIn"`
	AuthMethod   string `json:"authMethod"`
	Email        string `json:"email"`
	OrgName      string `json:"orgName"`
	Subscription string `json:"subscriptionType"`
}

// Describe names the account for status screens.
func (a *Account) Describe() string {
	who := a.Email
	if a.Subscription != "" {
		who += " · " + a.Subscription + " plan"
	} else if a.AuthMethod != "" {
		who += " · " + a.AuthMethod
	}
	return strings.TrimPrefix(who, " · ")
}

// Status reports Claude Code's own sign-in. It fails when Claude Code is
// missing or signed out.
func Status(ctx context.Context, bin string) (*Account, error) {
	p := New(Config{Bin: bin})
	path, err := p.bin()
	if err != nil {
		return nil, err
	}
	cmd := exec.CommandContext(ctx, path, "auth", "status", "--json")
	cmd.Env = env()
	out, err := cmd.Output()
	var a Account
	if jerr := json.Unmarshal(out, &a); jerr != nil {
		if err == nil {
			err = jerr
		}
		return nil, fmt.Errorf("claude-code: reading `claude auth status` failed: %w", err)
	}
	if !a.LoggedIn {
		return nil, errors.New("claude-code: Claude Code is not signed in: run `claude auth login`")
	}
	return &a, nil
}

// Verify checks that Claude Code is installed and signed in.
func (p *Provider) Verify(ctx context.Context) error {
	_, err := Status(ctx, p.cfg.Bin)
	return err
}

// env is the environment for Claude Code: API keys and endpoint overrides
// from the user's shell are dropped, so it runs on its own login and never
// spends API credits by accident.
func env() []string {
	drop := []string{"ANTHROPIC_API_KEY", "ANTHROPIC_AUTH_TOKEN", "ANTHROPIC_BASE_URL", "CLAUDE_CODE_SAFE_MODE"}
	out := slices.DeleteFunc(os.Environ(), func(kv string) bool {
		name, _, _ := strings.Cut(kv, "=")
		return slices.Contains(drop, name)
	})
	// A tool call waits on the runner, which may hold it across a turn
	// limit; don't let Claude Code give up on it.
	return append(out, "MCP_TOOL_TIMEOUT=86400000")
}

// Confine runs Claude Code in the task's workspace, so each task's
// transcripts are filed with that project (as `claude --resume` there
// shows), not all in one shared folder. Its tools stay off: ackn4th's own
// tools do all the work.
func (p *Provider) Confine(c provider.Confinement) {
	if c.Sandbox.Workspace != "" {
		p.cfg.Dir = c.Sandbox.Workspace
	}
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
		return nil, fmt.Errorf("claude-code: saved session is not from Claude Code: %w", err)
	}
	return &session{p: p, system: system, tools: tools, id: st.SessionID}, nil
}

// call is a tool call from Claude Code waiting for its result.
type call = mcpbridge.Call

// session is one Claude Code conversation. Its methods are called by one
// agent at a time; the MCP handler runs on its own goroutines.
type session struct {
	p      *Provider
	system provider.System
	tools  []provider.ToolSpec
	id     string // Claude Code's session ID, for --resume

	proc    *proc
	waiting []*call // handed to the runner, not answered yet
	// counted is the usage already reported per assistant message, and
	// reported its total for the current request.
	counted  map[string]provider.Usage
	reported provider.Usage
}

// proc is a running `claude -p` and its MCP listener.
type proc struct {
	cmd    *exec.Cmd
	stdin  io.WriteCloser
	lines  chan []byte // stdout, one event per line; closed at exit
	calls  chan *call
	bridge *mcpbridge.Bridge
	stderr *tail
	tmp    string // the system prompt's file, on Windows
}

func (pr *proc) cleanup() {
	if pr.tmp != "" {
		os.Remove(pr.tmp)
	}
}

func (s *session) PendingToolCalls() []provider.ToolCall {
	var out []provider.ToolCall
	for _, c := range s.waiting {
		out = append(out, c.ToolCall)
	}
	return out
}

func (s *session) State() (json.RawMessage, error) { return json.Marshal(state{SessionID: s.id}) }

func (s *session) SendText(ctx context.Context, text string, st provider.Stream) (*provider.Reply, error) {
	if len(s.waiting) > 0 {
		// Claude Code is still inside the previous request, waiting on
		// these; close it out before starting a new one.
		if err := s.abandon(ctx); err != nil {
			return nil, err
		}
	}
	if err := s.start(); err != nil {
		return nil, err
	}
	msg, _ := json.Marshal(map[string]any{"type": "user", "message": map[string]any{"role": "user", "content": text}})
	if _, err := s.proc.stdin.Write(append(msg, '\n')); err != nil {
		return nil, s.died()
	}
	return s.wait(ctx, st)
}

func (s *session) SendToolResults(ctx context.Context, results []provider.ToolResult, extra string, st provider.Stream) (*provider.Reply, error) {
	if s.proc == nil {
		return nil, errors.New("claude-code: no tool calls are waiting (Claude Code restarted)")
	}
	byID := map[string]provider.ToolResult{}
	for _, r := range results {
		byID[r.CallID] = r
	}
	for i, c := range s.waiting {
		r, ok := byID[c.ID]
		if !ok {
			r = provider.ToolResult{CallID: c.ID, Name: c.Name, IsError: true, Content: "Error: this call was not run."}
		}
		// MCP has no way to add a user message next to tool results, so
		// extra text rides on the last one.
		if extra != "" && i == len(s.waiting)-1 {
			r.Content = strings.TrimSpace(r.Content + "\n\n" + extra)
		}
		c.Result <- r
	}
	s.waiting = nil
	return s.wait(ctx, st)
}

// abandon answers waiting calls with an error and lets Claude Code finish
// its request, refusing any further calls.
func (s *session) abandon(ctx context.Context) error {
	refuse := func(c *call) {
		c.Result <- provider.ToolResult{CallID: c.ID, Name: c.Name, IsError: true,
			Content: "Error: ackn4th ended your turn before running this. Do not call more tools; reply briefly."}
	}
	for _, c := range s.waiting {
		refuse(c)
	}
	s.waiting = nil
	if s.proc == nil {
		return nil
	}
	for {
		select {
		case <-ctx.Done():
			s.stop()
			return ctx.Err()
		case c := <-s.proc.calls:
			refuse(c)
		case line, ok := <-s.proc.lines:
			if !ok {
				s.stop()
				return nil // the next request restarts it
			}
			var ev struct{ Type string }
			if json.Unmarshal(line, &ev) == nil && ev.Type == "result" {
				return nil
			}
		}
	}
}

// start launches Claude Code for this session unless it is running.
func (s *session) start() error {
	if s.proc != nil {
		return nil
	}
	bin, err := s.p.bin()
	if err != nil {
		return err
	}
	if s.p.cfg.Dir != "" {
		if err := os.MkdirAll(s.p.cfg.Dir, 0o700); err != nil {
			return err
		}
	}
	br, err := mcpbridge.Start(s.tools, "claudecode/toolUseId")
	if err != nil {
		return fmt.Errorf("claude-code: %w", err)
	}
	pr := &proc{lines: make(chan []byte, 64), calls: br.Calls, bridge: br, stderr: &tail{}}

	cfg, _ := json.Marshal(map[string]any{"mcpServers": map[string]any{server: map[string]any{
		"type": "http", "url": br.URL,
		"headers": map[string]string{"Authorization": "Bearer " + br.Token},
	}}})
	system := []string{"--system-prompt", s.system.Text()}
	if runtime.GOOS == "windows" {
		// A Windows command line holds 32K characters, and npm's .cmd
		// shim can't pass newlines: the system prompt goes in a file.
		f, err := os.CreateTemp("", "ackn4th-system-*.md")
		if err != nil {
			br.Close()
			return err
		}
		_, werr := f.WriteString(s.system.Text())
		if err := errors.Join(werr, f.Close()); err != nil {
			os.Remove(f.Name())
			br.Close()
			return err
		}
		pr.tmp = f.Name()
		system = []string{"--system-prompt-file", f.Name()}
	}
	args := append([]string{"-p",
		"--input-format", "stream-json", "--output-format", "stream-json", "--verbose", "--include-partial-messages",
		"--model", s.p.cfg.Model}, system...)
	args = append(args,
		// No built-in tools, no settings files (so no hooks, plugins or
		// permission rules), no MCP servers but ackn4th's.
		"--tools", "", "--setting-sources", "", "--disable-slash-commands",
		"--mcp-config", string(cfg), "--strict-mcp-config", "--allowedTools", "mcp__"+server,
	)
	if s.p.cfg.Effort != "" {
		args = append(args, "--effort", s.p.cfg.Effort)
	}
	if s.id != "" {
		args = append(args, "--resume", s.id)
	}
	argv, err := sandbox.Program(append([]string{bin}, args...))
	if err != nil {
		pr.cleanup()
		br.Close()
		return fmt.Errorf("claude-code: %w", err)
	}
	cmd := exec.Command(argv[0], argv[1:]...)
	cmd.Dir, cmd.Env, cmd.Stderr = s.p.cfg.Dir, env(), pr.stderr
	if pr.stdin, err = cmd.StdinPipe(); err != nil {
		pr.cleanup()
		br.Close()
		return err
	}
	out, err := cmd.StdoutPipe()
	if err != nil {
		pr.cleanup()
		br.Close()
		return err
	}
	if err := cmd.Start(); err != nil {
		pr.cleanup()
		br.Close()
		return fmt.Errorf("claude-code: starting Claude Code: %w", err)
	}
	pr.cmd = cmd
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

// stop ends Claude Code and its tool server.
func (s *session) stop() {
	pr := s.proc
	if pr == nil {
		return
	}
	s.proc = nil
	pr.bridge.Close()
	pr.stdin.Close()
	if pr.cmd.Process != nil {
		pr.cmd.Process.Kill()
	}
	pr.cleanup()
	s.waiting = nil // their handlers return on quit
}

// died reports that Claude Code exited, with the end of what it printed.
func (s *session) died() error {
	msg := "Claude Code exited unexpectedly"
	if s.proc != nil {
		if t := strings.TrimSpace(s.proc.stderr.String()); t != "" {
			msg += ": " + t
		}
	}
	s.stop()
	return errors.New("claude-code: " + msg)
}

// event is one line of Claude Code's stream-json output.
type event struct {
	Type      string `json:"type"`
	Subtype   string `json:"subtype"`
	SessionID string `json:"session_id"`
	Message   struct {
		ID      string `json:"id"`
		Content []struct {
			Type  string          `json:"type"`
			Text  string          `json:"text"`
			ID    string          `json:"id"`
			Name  string          `json:"name"`
			Input json.RawMessage `json:"input"`
		} `json:"content"`
		Usage *usage `json:"usage"`
	} `json:"message"`
	Event struct {
		Type  string `json:"type"`
		Delta struct {
			Type        string `json:"type"`
			Text        string `json:"text"`
			Thinking    string `json:"thinking"`
			PartialJSON string `json:"partial_json"`
		} `json:"delta"`
		Block struct {
			Type string `json:"type"`
			Name string `json:"name"`
		} `json:"content_block"`
	} `json:"event"`
	// result events
	IsError    bool     `json:"is_error"`
	Result     string   `json:"result"`
	StopReason string   `json:"stop_reason"`
	Errors     []string `json:"errors"`
	Usage      *usage   `json:"usage"`
}

type usage struct {
	Input       int64 `json:"input_tokens"`
	CacheRead   int64 `json:"cache_read_input_tokens"`
	CacheCreate int64 `json:"cache_creation_input_tokens"`
	Output      int64 `json:"output_tokens"`
}

func (u *usage) normalize() provider.Usage {
	return provider.Usage{InputTokens: u.Input + u.CacheRead + u.CacheCreate, OutputTokens: u.Output, CachedTokens: u.CacheRead}
}

func sub(a, b provider.Usage) provider.Usage {
	return provider.Usage{InputTokens: max(0, a.InputTokens-b.InputTokens), OutputTokens: max(0, a.OutputTokens-b.OutputTokens), CachedTokens: max(0, a.CachedTokens-b.CachedTokens)}
}

// gather is how long to wait for more tool calls after one arrives, so
// calls the model made together reach the runner together.
const gather = 60 * time.Millisecond

// wait reads Claude Code's output until it either calls ackn4th's tools
// or finishes the request.
func (s *session) wait(ctx context.Context, st provider.Stream) (*provider.Reply, error) {
	pr := s.proc
	if s.counted == nil {
		s.counted = map[string]provider.Usage{}
	}
	reply := &provider.Reply{}
	var text strings.Builder
	var batch <-chan time.Time
	toolReply := func() *provider.Reply {
		reply.Text, reply.StopReason = text.String(), provider.StopToolUse
		for _, c := range s.waiting {
			reply.ToolCalls = append(reply.ToolCalls, c.ToolCall)
		}
		return reply
	}
	for {
		select {
		case <-ctx.Done():
			s.stop()
			return nil, ctx.Err()
		case c := <-pr.calls:
			s.waiting = append(s.waiting, c)
			batch = time.After(gather)
		case <-batch:
			return toolReply(), nil
		case line, ok := <-pr.lines:
			if !ok {
				if len(s.waiting) > 0 {
					return toolReply(), nil
				}
				return nil, s.died()
			}
			var ev event
			if json.Unmarshal(line, &ev) != nil {
				continue
			}
			switch ev.Type {
			case "system":
				if ev.SessionID != "" {
					s.id = ev.SessionID
				}
				if ev.Subtype == "compact_boundary" {
					reply.Compacted = true
				}
			case "stream_event":
				switch e := ev.Event; {
				case e.Type == "content_block_start" && e.Block.Type == "tool_use" && st.ToolInput != nil:
					st.ToolInput(strings.TrimPrefix(e.Block.Name, toolPrefix), "")
				case e.Type != "content_block_delta":
				case e.Delta.Type == "text_delta" && st.Text != nil:
					st.Text(e.Delta.Text)
				case e.Delta.Type == "thinking_delta" && st.Thinking != nil:
					st.Thinking(e.Delta.Thinking)
				case e.Delta.Type == "input_json_delta" && st.ToolInput != nil:
					st.ToolInput("", e.Delta.PartialJSON)
				}
			case "assistant":
				for _, b := range ev.Message.Content {
					if b.Type == "text" {
						if text.Len() > 0 {
							text.WriteString("\n\n")
						}
						text.WriteString(b.Text)
					}
				}
				if u := ev.Message.Usage; u != nil {
					// Claude Code repeats a message's usage on each of its
					// content blocks; count each message once.
					n := u.normalize()
					reply.Usage.Add(sub(n, s.counted[ev.Message.ID]))
					s.reported.Add(sub(n, s.counted[ev.Message.ID]))
					s.counted[ev.Message.ID] = n
					reply.ContextTokens = n.InputTokens
				}
			case "result":
				if ev.SessionID != "" {
					s.id = ev.SessionID
				}
				if ev.Usage != nil {
					// The request's total, for whatever the per-message
					// usage missed.
					reply.Usage.Add(sub(ev.Usage.normalize(), s.reported))
				}
				s.reported, s.counted = provider.Usage{}, nil
				if ev.IsError || (ev.Subtype != "" && ev.Subtype != "success") {
					return nil, resultError(ev)
				}
				reply.Text = text.String()
				switch ev.StopReason {
				case "max_tokens":
					reply.StopReason = provider.StopMaxTokens
				case "refusal":
					reply.StopReason, reply.StopDetail = provider.StopRefusal, ev.Result
				default:
					reply.StopReason = provider.StopEnd
				}
				return reply, nil
			}
		}
	}
}

func resultError(ev event) error {
	msg := strings.TrimSpace(ev.Result)
	if msg == "" {
		msg = strings.Join(ev.Errors, "; ")
	}
	if msg == "" {
		msg = "request failed (" + ev.Subtype + ")"
	}
	low := strings.ToLower(msg)
	if strings.Contains(low, "limit") && (strings.Contains(low, "usage") || strings.Contains(low, "reset")) {
		msg += " · your Claude plan's usage limit; resume the task once it resets"
	}
	return errors.New("claude-code: " + msg)
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
