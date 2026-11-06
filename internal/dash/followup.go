package dash

import "github.com/dethmasque/ackn4th/internal/task"

// Follow-ups: a, on a task that isn't running, asks its team for more
// work. On a running task, a messages the team instead, without stopping
// it. The same agents continue with their conversations and the
// workspace; every agent gets the prompt, and all must declare done again.
// The new-task form, in follow-up mode, takes the prompt and lets each
// agent's role, model and effort change.

// openFollowUp is the a key.
func (m *model) openFollowUp() {
	l := m.current()
	switch {
	case l == nil || m.opt.FollowUp == nil:
	case l.Task.Status == task.Running && m.opt.Message != nil:
		m.form = newMessageForm(&m.opt, l.Task)
	case l.Task.Status == task.Running:
		m.say("task " + l.Task.Title() + " is still running: follow-ups are for when it stops (s stops it)")
	default:
		m.form = newFollowUpForm(&m.opt, l.Task)
	}
}
