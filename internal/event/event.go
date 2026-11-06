// Package event defines the progress events a running task emits. Events
// are appended to the task's events.jsonl and rendered live by the CLI.
package event

import (
	"fmt"
	"time"
	"unicode/utf8"
)

// Kind identifies an event.
type Kind string

const (
	TaskStarted   Kind = "task_started"   // runner attached; Text holds "start" or "resume"
	FollowUp      Kind = "follow_up"      // a follow-up starts running; Text = its prompt
	UserMessage   Kind = "user_message"   // the person messaged the running team; Text = the message, Detail = who it is for
	RoundStarted  Kind = "round_started"  // Round set
	TurnStarted   Kind = "turn_started"   // an agent begins its turn
	Thinking      Kind = "thinking"       // streamed reasoning summary chunk (not persisted)
	Reasoning     Kind = "reasoning"      // a model response's whole reasoning summary, once it is done; Text = the summary
	Delta         Kind = "delta"          // streamed reply text chunk (not persisted)
	ToolInput     Kind = "tool_input"     // streamed tool-call input chunk, Tool set (not persisted)
	ToolCall      Kind = "tool_call"      // Tool, Text = short argument summary, Detail = the arguments
	ToolResult    Kind = "tool_result"    // Tool, Text = short result, Detail = what the model got back, Error if failed
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
	// Detail is the full story behind Text, for looking into an event
	// later: a tool call's arguments, or what a tool sent back to the
	// model. It is cut to DetailMax (ErrorDetailMax for failures).
	Detail string `json:"detail,omitempty"`
	Error  bool   `json:"error,omitempty"`
	InTok  int64  `json:"in_tokens,omitempty"`
	OutTok int64  `json:"out_tokens,omitempty"`
	// CacheTok is the part of InTok read from the prompt cache.
	CacheTok int64 `json:"cached_tokens,omitempty"`
	// CtxTok is how full the agent's context window was at its last request.
	CtxTok int64 `json:"context_tokens,omitempty"`
}

// How much of a detail or reasoning summary the log keeps. Failures keep
// more: they are what people dig into.
const (
	DetailMax      = 4 << 10
	ErrorDetailMax = 16 << 10
)

// Clip cuts s to at most n bytes, keeping its start and end, which is
// where commands and their output say the most.
func Clip(s string, n int) string {
	if len(s) <= n {
		return s
	}
	const mark = "\n… (%d bytes left out) …\n"
	head := n * 2 / 3
	tail := n - head
	for head > 0 && !utf8.RuneStart(s[head]) {
		head--
	}
	cut := len(s) - tail
	for cut < len(s) && !utf8.RuneStart(s[cut]) {
		cut++
	}
	return s[:head] + fmt.Sprintf(mark, cut-head) + s[cut:]
}

// Ephemeral reports whether the event is only for live display.
func (e Event) Ephemeral() bool { return e.Kind == Delta || e.Kind == Thinking || e.Kind == ToolInput }

// Sink receives events.
type Sink func(Event)

// ByUser reports whether the person wrote the event (a follow-up, or a
// message to the running team), as opposed to ackn4th or an agent.
func (e Event) ByUser() bool { return e.Kind == FollowUp || e.Kind == UserMessage }
