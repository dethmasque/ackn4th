package tools

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/dethmasque/ackn4th/internal/provider"
	"github.com/dethmasque/ackn4th/internal/sandbox"
	"github.com/dethmasque/ackn4th/internal/ui"
)

// Commands is how one agent runs shell commands.
type Commands struct {
	Policy CommandPolicy
	// Sandbox confines the agent's commands: the workspace, its scratch
	// directory, whether it may write, what it may not read.
	Sandbox sandbox.Config
}

// defaultTimeout is a command's limit when the model asks for none.
const defaultTimeout = 2 * time.Minute

func (e *Executor) runCommand(ctx context.Context, call provider.ToolCall) Result {
	var in struct {
		Command string `json:"command"`
		Timeout int    `json:"timeout_seconds"`
	}
	if err := decode(call.Input, &in, "command"); err != nil {
		return fail("%v", err)
	}
	if e.Commands == nil {
		return fail("commands are not enabled for this task")
	}
	if strings.TrimSpace(in.Command) == "" {
		return fail("the command is empty")
	}
	limit := e.Commands.Policy.Timeout
	timeout := min(defaultTimeout, limit)
	if in.Timeout > 0 {
		timeout = min(time.Duration(in.Timeout)*time.Second, limit)
	}
	var res sandbox.Result
	var err error
	run := func() { res, err = sandbox.Run(ctx, e.Commands.Sandbox, in.Command, timeout) }
	var changed []string
	if e.Team != nil && e.Team.Command != nil {
		changed = e.Team.Command(e.Self, run)
	} else {
		run()
	}
	if err != nil {
		return fail("%v", err)
	}
	status := fmt.Sprintf("exit %d", res.ExitCode)
	if res.TimedOut {
		status = "killed after " + ui.Duration(timeout) + " (timed out)"
	}
	took := ui.Duration(res.Duration.Round(100 * time.Millisecond))
	out := strings.TrimRight(res.Output, "\n")
	if out == "" {
		out = "(no output)"
	}
	if len(changed) > 0 {
		for _, p := range changed {
			e.forgetPath(p)
		}
		out += "\n\n[files this command changed: " + strings.Join(changed, ", ") + ". Read them again before editing.]"
	}
	return Result{
		Content: fmt.Sprintf("%s · %s\n\n%s", status, took, out),
		IsError: res.ExitCode != 0 || res.TimedOut,
		Summary: status + " · " + took,
	}
}

func oneLine(s string) string { return ui.Truncate(strings.Join(strings.Fields(s), " "), 90) }
