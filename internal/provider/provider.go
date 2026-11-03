// Package provider defines the provider-neutral interface agents use to
// talk to a model. Implementations live in the claude and gemini
// subpackages.
//
// Each Session keeps its conversation in the provider's native format, so
// provider-specific state (Claude thinking blocks, Gemini thought
// signatures) round-trips exactly. The history can be serialized with
// State and restored later, which is what lets a stopped task resume.
package provider

import (
	"context"
	"encoding/json"

	"github.com/dethmasque/ackn4th/internal/sandbox"
)

// ToolSpec describes a tool the model may call.
type ToolSpec struct {
	Name        string
	Description string
	// Schema is a JSON Schema object describing the input.
	Schema map[string]any
}

// ToolCall is a model's request to run a tool.
type ToolCall struct {
	ID    string
	Name  string
	Input json.RawMessage
}

// ToolResult answers a ToolCall.
type ToolResult struct {
	CallID  string
	Name    string
	Content string
	IsError bool
}

// Usage counts tokens for one model response.
type Usage struct {
	InputTokens  int64 `json:"input_tokens"`
	OutputTokens int64 `json:"output_tokens"`
	// CachedTokens is the part of InputTokens read from the prompt cache:
	// cheaper and faster than fresh input.
	CachedTokens int64 `json:"cached_tokens,omitempty"`
}

// Add accumulates u2 into u.
func (u *Usage) Add(u2 Usage) {
	u.InputTokens += u2.InputTokens
	u.OutputTokens += u2.OutputTokens
	u.CachedTokens += u2.CachedTokens
}

// System is an agent's system prompt in two parts. Shared is
// byte-identical for every agent on a task (team, rules, project brief);
// Own is the agent's role. Providers send Shared first and cache it, so
// agents on the same model and settings read one cached copy of the
// shared context instead of each paying for its own.
type System struct {
	Shared string
	Own    string
}

// Text joins both parts, for providers without separate blocks.
func (s System) Text() string {
	if s.Own == "" {
		return s.Shared
	}
	return s.Shared + "\n\n" + s.Own
}

// Stop reasons, normalized across providers.
const (
	StopEnd       = "end"        // the model finished its reply
	StopToolUse   = "tool_use"   // the model wants tool results
	StopMaxTokens = "max_tokens" // output was cut off
	StopRefusal   = "refusal"    // the model declined
	StopOther     = "other"
)

// Reply is one model response.
type Reply struct {
	Text       string
	ToolCalls  []ToolCall
	StopReason string
	// StopDetail explains refusals and unusual stops when the provider says why.
	StopDetail string
	Usage      Usage
	// ContextTokens is the size of the conversation the model just read:
	// how full its context window is.
	ContextTokens int64
	// Compacted is set when earlier conversation was replaced by a summary
	// before this reply. Anything the agent read before then is no longer
	// verbatim in context.
	Compacted bool
}

// Stream receives incremental output while a reply is generated. Any
// field may be nil.
type Stream struct {
	Text     func(string)
	Thinking func(string)
	// ToolInput receives a tool call's JSON input as the model writes it:
	// first with the tool's name and an empty chunk when the call starts,
	// then with each chunk.
	ToolInput func(name, chunk string)
	// Activity reports work an agent CLI does with its own tools (a shell
	// command, a file edit): tool is its name, detail a short summary,
	// failed set when it went wrong. The runner never sees these as
	// ToolCalls.
	Activity func(tool, detail string, failed bool)
}

// Session is one ongoing conversation with a model.
type Session interface {
	// SendText appends a user message and returns the model's reply.
	SendText(ctx context.Context, text string, s Stream) (*Reply, error)
	// SendToolResults answers the previous reply's tool calls. Every call
	// must be answered, in order. Extra text is appended after the results.
	SendToolResults(ctx context.Context, results []ToolResult, extra string, s Stream) (*Reply, error)
	// PendingToolCalls returns tool calls from the last reply that have not
	// been answered yet, e.g. because the runner stopped mid-turn.
	PendingToolCalls() []ToolCall
	// State serializes the conversation for RestoreSession.
	State() (json.RawMessage, error)
}

// Provider creates sessions against one model.
type Provider interface {
	Name() string
	Model() string
	// NewSession starts an empty conversation.
	NewSession(system System, tools []ToolSpec) Session
	// RestoreSession continues a conversation saved with Session.State.
	RestoreSession(system System, tools []ToolSpec, state json.RawMessage) (Session, error)
	// Verify checks that the credentials work and the model exists.
	Verify(ctx context.Context) error
}

// Confinement is what a provider whose CLI runs its own tools needs from
// the runner: where it may work, and how to credit the files it changes.
type Confinement struct {
	// Sandbox is the agent's confinement: workspace, scratch, hidden paths.
	Sandbox sandbox.Config
	// Credit runs fn and credits every file the workspace gained, changed
	// or lost meanwhile to this agent, returning their paths.
	Credit func(fn func()) []string
}

// Confined is implemented by providers that run an agent CLI: those with
// their own tools (Codex, Cursor, Copilot), which refuse to run without
// it, and Claude Code, which works from the task's workspace. The runner
// calls Confine before the agent's first request.
type Confined interface {
	Confine(Confinement)
}

// Prewarmer is implemented by providers that can write the shared prompt
// prefix to the cache ahead of time. Agents that start at the same moment
// cannot read a cache entry another is still writing, so a parallel round
// prewarms once and every agent then reads the cached prefix.
type Prewarmer interface {
	Prewarm(ctx context.Context, system System, tools []ToolSpec) (Usage, error)
}

// CompactPrompt asks a model to summarize its own conversation, for
// providers that compact on the client (Gemini, OpenAI).
const CompactPrompt = `Your context is nearly full, so this conversation is about to be replaced by a summary that you write now. Write it for yourself: after this, the summary is all you will remember. Keep, as concrete facts:
- the task as stated, and any guidance from the human operator, verbatim where short;
- your role, the team, and what each teammate has said, asked for, or disagreed with;
- every file created, edited or deleted, by whom, and its current state as far as you know; key contents that matter (signatures, interfaces, decisions), not whole files;
- decisions made and why, open questions, known bugs, things not yet verified;
- your current plan and next concrete steps;
- who has declared the task done, and any outstanding concerns.
Drop tool output that has been superseded and anything you can re-read from the workspace. Be specific: paths, names and numbers. Reply with the summary only; do not call tools.`

// CompactedIntro starts the message that replaces a compacted
// conversation; the model's summary follows it.
const CompactedIntro = "# Summary of your work so far\n\nYour earlier conversation was compacted to save context. This is the summary you wrote:\n\n"
