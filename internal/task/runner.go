package task

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/dethmasque/ackn4th/internal/event"
	"github.com/dethmasque/ackn4th/internal/provider"
	"github.com/dethmasque/ackn4th/internal/role"
	"github.com/dethmasque/ackn4th/internal/tools"
)

// ProviderFactory builds the provider for one agent, with its model and
// settings.
type ProviderFactory func(spec AgentSpec) (provider.Provider, error)

// Runner drives a task's agents: in turn, or in parallel rounds.
type Runner struct {
	Store        *Store
	Roles        *role.Registry
	NewProvider  ProviderFactory
	AllowedRoots []string
	MaxSteps     int
	MaxFileBytes int64
	// Authorize is checked before every model request. When it fails, the
	// task stops (resumably) instead of continuing on stale credentials.
	// It gets the agent's provider.
	Authorize func(provider string) error
	// Sink receives every event, including streamed text, for live display.
	Sink event.Sink
	// Commands lets agents run sandboxed shell commands; nil disallows
	// them. Hide lists paths their commands may never read (ackn4th's
	// config and data).
	Commands *tools.CommandPolicy
	Hide     []string
	// Completed runs once a task completes, before its summary is
	// written; it may record results on the task (the draft PR's URL).
	Completed func(*Task)

	emitMu sync.Mutex // agents working in parallel emit concurrently
	live   *liveFeed  // streamed output, for dashboards in other processes
}

// ErrRoundLimit is returned when a task stops because it used its rounds.
var ErrRoundLimit = errors.New("round limit reached")

// unauthorized wraps an Authorize failure.
type unauthorized struct{ error }

// Run executes the task until it completes, fails, hits its round limit,
// or ctx is cancelled (stop-task / Ctrl-C), recording the outcome. It
// returns the task's final state.
func (r *Runner) Run(ctx context.Context, id string, background bool) (*Task, error) {
	release, err := r.Store.Lock(id)
	if err != nil {
		return nil, err
	}
	defer release()
	defer func() {
		if t, err := r.Store.Load(id); err == nil && r.live != nil {
			r.live.clear(id, len(t.Agents))
		}
	}()

	t, err := r.Store.Load(id)
	if err != nil {
		return nil, err
	}
	if t.Status == Completed {
		return t, fmt.Errorf("task %s is already completed", id)
	}
	resumed := t.Status != Pending
	t.Status, t.Reason, t.PID, t.Background = Running, "", os.Getpid(), background
	if err := r.Store.Save(t); err != nil {
		return t, err
	}
	how := "start"
	if resumed {
		how = "resume"
	}
	r.emit(t, event.Event{Kind: event.TaskStarted, Text: how})

	var runErr error
	if rn, err := r.setup(t); err != nil {
		runErr = err
	} else {
		defer rn.close()
		if t.Parallel() {
			runErr = rn.parallel(ctx)
		} else {
			runErr = rn.sequential(ctx)
		}
	}

	t.PID = 0
	switch {
	case runErr == nil:
		t.Status = Completed
		r.emit(t, event.Event{Kind: event.TaskCompleted, Text: t.Reason})
	case errors.As(runErr, new(unauthorized)):
		t.Status = Stopped
		t.Reason = runErr.Error() + "; log in again, then resume"
		r.emit(t, event.Event{Kind: event.TaskStopped, Text: t.Reason})
	case errors.Is(runErr, ErrRoundLimit):
		t.Status = Stopped
		t.Reason = fmt.Sprintf("round limit reached (%d); resume with --rounds to continue", t.MaxRounds)
		r.emit(t, event.Event{Kind: event.TaskStopped, Text: t.Reason})
	case ctx.Err() != nil:
		t.Status = Stopped
		t.Reason = "stopped by user"
		r.emit(t, event.Event{Kind: event.TaskStopped, Text: t.Reason})
	default:
		t.Status = Failed
		t.Reason = runErr.Error()
		r.emit(t, event.Event{Kind: event.TaskFailed, Text: t.Reason, Error: true})
	}
	if err := r.Store.Save(t); err != nil {
		return t, err
	}
	if t.Status == Completed && r.Completed != nil {
		r.Completed(t)
		if err := r.Store.Save(t); err != nil {
			return t, err
		}
	}
	writeAtomic(r.Store.SummaryPath(id), []byte(r.Summary(t)))
	if t.Status == Completed || t.Status == Stopped {
		return t, nil
	}
	return t, runErr
}

func (r *Runner) authorize(provider string) error {
	if r.Authorize == nil {
		return nil
	}
	if err := r.Authorize(provider); err != nil {
		return unauthorized{err}
	}
	return nil
}

func allDone(t *Task) bool {
	for _, a := range t.Agents {
		if !a.Done {
			return false
		}
	}
	return true
}

func summary(t *Task) string {
	if t.Solo() {
		return t.Agents[0].DoneSummary
	}
	var b strings.Builder
	for _, a := range t.Agents {
		fmt.Fprintf(&b, "[%s] %s\n", a.Role, a.DoneSummary)
	}
	return strings.TrimSpace(b.String())
}

func (r *Runner) emit(t *Task, e event.Event) {
	e.Round = t.Round
	r.emitRaw(t.ID, e)
}

func (r *Runner) emitRaw(id string, e event.Event) {
	r.emitMu.Lock()
	defer r.emitMu.Unlock()
	if e.Time.IsZero() {
		e.Time = time.Now()
	}
	if !e.Ephemeral() {
		r.Store.AppendEvent(id, e)
	}
	if r.live == nil {
		r.live = newLiveFeed(r.Store)
	}
	r.live.Handle(id, e)
	if r.Sink != nil {
		r.Sink(e)
	}
}
