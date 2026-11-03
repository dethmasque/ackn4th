// Package event defines the progress events a running task emits. Events
// are appended to the task's events.jsonl and rendered live by the CLI.
package event

import "time"

// Kind identifies an event.
type Kind string

const (
	TaskStarted   Kind = "task_started"   // runner attached; Text holds "start" or "resume"
	RoundStarted  Kind = "round_started"  // Round set
	TurnStarted   Kind = "turn_started"   // an agent begins its turn
	Thinking      Kind = "thinking"       // streamed reasoning summary chunk (not persisted)
	Delta         Kind = "delta"          // streamed reply text chunk (not persisted)
	ToolInput     Kind = "tool_input"     // streamed tool-call input chunk, Tool set (not persisted)
	ToolCall      Kind = "tool_call"      // Tool, Text = short argument summary
	ToolResult    Kind = "tool_result"    // Tool, Text = short result, Error if failed
	Message       Kind = "message"        // an agent's full reply text for the turn
	DoneVote      Kind = "done_vote"      // agent declared the task complete; Text = summary
	TurnEnded     Kind = "turn_ended"     // usage totals for the turn
	Notice        Kind = "notice"         // informational message from the runner
	Context       Kind = "context"        // context handed to an agent at turn start (brief, notes, changes); Text = summary
	Compacted     Kind = "compacted"      // an agent's conversation was summarized to free context
	TaskStopped   Kind = "task_stopped"   // Text = reason
	TaskCompleted Kind = "task_completed" // Text = final summary
	TaskFailed    Kind = "task_failed"    // Text = error
)

// Event is one progress record.
type Event struct {
	Time     time.Time `json:"time"`
	Kind     Kind      `json:"kind"`
	Round    int       `json:"round,omitempty"`
	Agent    int       `json:"agent,omitempty"` // index into the task's agents, 1-based; 0 = runner
	Role     string    `json:"role,omitempty"`
	Provider string    `json:"provider,omitempty"`
	Model    string    `json:"model,omitempty"`
	Tool     string    `json:"tool,omitempty"`
	Text     string    `json:"text,omitempty"`
	Error    bool      `json:"error,omitempty"`
	InTok    int64     `json:"in_tokens,omitempty"`
	OutTok   int64     `json:"out_tokens,omitempty"`
	// CacheTok is the part of InTok read from the prompt cache.
	CacheTok int64 `json:"cached_tokens,omitempty"`
	// CtxTok is how full the agent's context window was at its last request.
	CtxTok int64 `json:"context_tokens,omitempty"`
}

// Ephemeral reports whether the event is only for live display.
func (e Event) Ephemeral() bool { return e.Kind == Delta || e.Kind == Thinking || e.Kind == ToolInput }

// Sink receives events.
type Sink func(Event)
