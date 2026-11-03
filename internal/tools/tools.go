// Package tools defines the tools agents can call and executes them against
// a confined workspace.
package tools

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"path"
	"strings"
	"sync"
	"time"

	"github.com/dethmasque/ackn4th/internal/notes"
	"github.com/dethmasque/ackn4th/internal/provider"
	"github.com/dethmasque/ackn4th/internal/sandbox"
	"github.com/dethmasque/ackn4th/internal/ui"
	"github.com/dethmasque/ackn4th/internal/workspace"
)

// Tool names.
const (
	ListDir     = "list_dir"
	ReadFile    = "read_file"
	SearchFiles = "search_files"
	WriteFile   = "write_file"
	EditFile    = "edit_file"
	DeletePath  = "delete_path"
	UpdateNotes = "update_notes"
	MessageTeam = "message_teammate"
	DeclareDone = "declare_done"
	RunCommand  = "run_command"
	AddTodo     = "add_user_todo"
	ResolveTodo = "resolve_user_todo"
)

func obj(required []string, props map[string]any) map[string]any {
	return map[string]any{"type": "object", "properties": props, "required": required}
}

func str(desc string) map[string]any { return map[string]any{"type": "string", "description": desc} }
func num(desc string) map[string]any { return map[string]any{"type": "integer", "description": desc} }

var (
	readSpecs = []provider.ToolSpec{
		{
			Name:        ListDir,
			Description: "List files and directories in the workspace. Paths are relative to the workspace root.",
			Schema: obj([]string{}, map[string]any{
				"path":  str("Directory to list, relative to the workspace root. Defaults to the root."),
				"depth": num("How many levels deep to list (1-6). Defaults to 2."),
			}),
		},
		{
			Name:        ReadFile,
			Description: "Read a text file from the workspace. For large files, read a window of lines with offset and limit.",
			Schema: obj([]string{"path"}, map[string]any{
				"path":   str("File path relative to the workspace root."),
				"offset": num("1-based line number to start at. Optional."),
				"limit":  num("Maximum number of lines to return. Optional."),
			}),
		},
		{
			Name:        SearchFiles,
			Description: "Search file contents in the workspace with a regular expression (RE2 syntax). Returns matching lines with file:line.",
			Schema: obj([]string{"pattern"}, map[string]any{
				"pattern": str("Regular expression to search for."),
				"path":    str("Directory to search, relative to the workspace root. Defaults to the root."),
				"glob":    str("Only search files matching this glob, e.g. *.go or src/**/*.ts. Optional."),
			}),
		},
	}
	writeSpecs = []provider.ToolSpec{
		{
			Name:        WriteFile,
			Description: "Create a file or replace its entire contents. Parent directories are created. Prefer edit_file for changes to existing files.",
			Schema: obj([]string{"path", "content"}, map[string]any{
				"path":    str("File path relative to the workspace root."),
				"content": str("The complete new file contents."),
			}),
		},
		{
			Name:        EditFile,
			Description: "Replace an exact string in a file. old_string must match exactly once (include surrounding lines to make it unique) unless replace_all is true.",
			Schema: obj([]string{"path", "old_string", "new_string"}, map[string]any{
				"path":        str("File path relative to the workspace root."),
				"old_string":  str("Exact text to replace, including whitespace."),
				"new_string":  str("Replacement text."),
				"replace_all": map[string]any{"type": "boolean", "description": "Replace every occurrence. Defaults to false."},
			}),
		},
		{
			Name:        DeletePath,
			Description: "Delete a file or an empty directory.",
			Schema: obj([]string{"path"}, map[string]any{
				"path": str("Path relative to the workspace root."),
			}),
		},
	}
	notesSpec = provider.ToolSpec{
		Name: UpdateNotes,
		Description: "Write a section of the shared project notes. Every agent on every future task in this workspace reads the notes when it joins, so they are how the team's knowledge outlives this conversation. " +
			"Record what a newcomer needs and cannot cheaply rediscover: architecture and where things live, conventions, how to build and test, decisions and why, gotchas, current status and known gaps. " +
			"Not for turn-by-turn handoffs. The content replaces the whole section; empty content deletes it. Keep it tight: all notes together are limited to 24 KB.",
		Schema: obj([]string{"section", "content"}, map[string]any{
			"section": str("Section heading, e.g. Architecture, Conventions, Build and test, Decisions, Gotchas, Status."),
			"content": str("Markdown for the section, replacing what is there. Empty deletes the section."),
		}),
	}
	messageSpec = provider.ToolSpec{
		Name: MessageTeam,
		Description: "Send a short message to a teammate (by role name) or to \"all\". Teammates working at the same time get it during their current turn; otherwise at the start of their next one. " +
			"Use it to coordinate: say which files you are about to change, ask a question, or flag a problem. Your end-of-turn message is still shared as usual.",
		Schema: obj([]string{"to", "message"}, map[string]any{
			"to":      str("A teammate's role name, or \"all\"."),
			"message": str("The message."),
		}),
	}
	todoSpecs = []provider.ToolSpec{
		{
			Name: AddTodo,
			Description: "Record an action item for the person who gave this task: something only they can do or decide. Examples: choose between options, provide a credential or an account, run a deployment or a migration, review a risky change, or check something you could not verify. " +
				"They see the open items when the task ends. One item per call, written so it makes sense on its own. Don't add work the team can still do itself.",
			Schema: obj([]string{"text"}, map[string]any{
				"text": str("The action item: what they need to do and why, in one or two sentences."),
			}),
		},
		{
			Name:        ResolveTodo,
			Description: "Close an action item for the person that is no longer needed, because the team handled it or it became moot. Open items are listed at the start of each turn with their numbers.",
			Schema: obj([]string{"id", "note"}, map[string]any{
				"id":   num("The item's number."),
				"note": str("How it was resolved."),
			}),
		},
	}
	doneSpec = provider.ToolSpec{
		Name:        DeclareDone,
		Description: "Declare that, from your role's perspective, the whole task is complete and nothing more is needed. The task finishes only when every agent on the team declares done in consecutive turns. Do not call this if you still have concerns.",
		Schema: obj([]string{"summary"}, map[string]any{
			"summary": str("What was delivered and how it was verified, in a few sentences."),
		}),
	}
)

// SpecOptions selects the optional tools.
type SpecOptions struct {
	Team bool // message_teammate
	// Commands enables run_command, with commands confined as described.
	Commands *CommandPolicy
}

// CommandPolicy is how agents' shell commands are confined.
type CommandPolicy struct {
	Network bool
	Timeout time.Duration // the longest a command may run
}

func (p *CommandPolicy) spec() provider.ToolSpec {
	local := "localhost works"
	if sandbox.Loopback() != nil {
		local = "not even localhost"
	}
	net := "There is no network access (" + local + "), so dependencies must already be installed or vendored."
	if p.Network {
		net = "Network access is allowed, so package managers can fetch dependencies."
	}
	return provider.ToolSpec{
		Name: RunCommand,
		Description: "Run a shell command (" + sandbox.Shell() + ") in the workspace root to build, test, lint, type-check or run the code, and get its exit code and output. " +
			"It runs in a sandbox: it can write only inside the workspace (never .git or .ackn4th, and nothing at all for read-only roles), the home and temp directories are a private scratch directory, and credential files are unreadable. " + net +
			fmt.Sprintf(" Commands are killed after %s; output is cut in the middle when long. Use it to verify your work, not to change files: make changes with the file tools.", ui.Duration(p.Timeout)),
		Schema: obj([]string{"command"}, map[string]any{
			"command":         str("The shell command, e.g. `go test ./...` or `npm test -- --run`."),
			"timeout_seconds": num(fmt.Sprintf("Seconds before the command is killed. Optional; at most %d.", int(p.Timeout.Seconds()))),
		}),
	}
}

// Specs returns the tools: file tools, run_command when enabled,
// update_notes, message_teammate when there is a team, the to-do tools and
// declare_done. Every agent on a task gets the same list, read-only roles
// included (the workspace refuses their writes), because tools are the
// start of the cached prompt prefix that agents on the same model share.
func Specs(o SpecOptions) []provider.ToolSpec {
	out := append([]provider.ToolSpec{}, readSpecs...)
	out = append(out, writeSpecs...)
	if o.Commands != nil {
		out = append(out, o.Commands.spec())
	}
	out = append(out, notesSpec)
	if o.Team {
		out = append(out, messageSpec)
	}
	out = append(out, todoSpecs...)
	return append(out, doneSpec)
}

// Result is the outcome of one tool call.
type Result struct {
	Content string // sent back to the model
	IsError bool
	Summary string // one line for the UI
	// Done is set when the agent called declare_done.
	Done        bool
	DoneSummary string
}

// Team is the coordination state every agent's executor in a task shares.
type Team struct {
	// mu makes check-then-change atomic across agents working at once.
	mu sync.Mutex
	// Members labels each agent, index i for agent i+1, e.g. "backend_dev".
	Members []string
	// Changed is called (under the team lock) after agent changes path.
	Changed func(agent int, path string)
	// Send delivers a message from one agent to another (to 0 = everyone).
	Send func(from, to int, text string) error
	// Command runs one of agent's commands and returns the workspace
	// paths it changed, so they are credited to the agent. Nil runs it
	// without tracking.
	Command func(agent int, run func()) []string
	// Todo records an action item for the operator and returns its number;
	// Resolve closes one.
	Todo    func(agent int, text string) (int, error)
	Resolve func(agent, id int, note string) error

	writer map[string]int // path → agent who changed it last in this run
}

func (t *Team) changed(agent int, p string) {
	if t.writer == nil {
		t.writer = map[string]int{}
	}
	t.writer[key(p)] = agent
	if t.Changed != nil {
		t.Changed(agent, key(p))
	}
}

// Executor runs one agent's tool calls against a workspace.
type Executor struct {
	WS *workspace.Workspace
	// Notes is the project's shared notes; Author signs updates.
	Notes  *notes.Book
	Author string
	// Team and Self (the agent's 1-based index) connect the agent to its
	// teammates. Team may be nil when the agent works alone.
	Team *Team
	Self int
	// Commands runs run_command; nil leaves agents without it.
	Commands *Commands

	// seen maps a path to the hash of the contents this conversation has
	// verbatim (last read or written), so an unchanged file is not sent to
	// the model twice.
	seen map[string]string
	// known maps a path to the hash of the latest version the agent knows,
	// whether read in full, written, or learned from a diff. Changing a file
	// requires knowing its current version, so agents working at the same
	// time cannot silently overwrite each other.
	known map[string]string
}

func hash(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}

func key(p string) string {
	return path.Clean(strings.TrimPrefix(strings.ReplaceAll(p, "\\", "/"), "./"))
}

// Forget drops the record of what the model has verbatim. Call it when the
// conversation is compacted: earlier file contents are no longer in
// context, so re-reads must return them in full again.
func (e *Executor) Forget() { e.seen = nil }

// Acknowledge records that the agent was shown a file change in full, as
// a diff or a whole new file (oldText ""): if it knew the old version, or
// the file is new to it, it now knows the new one.
func (e *Executor) Acknowledge(p, oldText, newText string) {
	if e.known == nil {
		e.seen, e.known = map[string]string{}, map[string]string{}
	}
	k, ok := e.known[key(p)]
	if (ok && k == hash(oldText)) || (!ok && oldText == "") {
		e.known[key(p)] = hash(newText)
	}
}

func (e *Executor) remember(p, content string, verbatim bool) {
	if e.seen == nil {
		e.seen, e.known = map[string]string{}, map[string]string{}
	}
	h := hash(content)
	e.known[key(p)] = h
	if verbatim {
		e.seen[key(p)] = h
	} else {
		delete(e.seen, key(p))
	}
}

func (e *Executor) forgetPath(p string) {
	delete(e.seen, key(p))
	delete(e.known, key(p))
}

func (e *Executor) lock() func() {
	if e.Team == nil {
		return func() {}
	}
	e.Team.mu.Lock()
	return e.Team.mu.Unlock
}

// fresh checks the agent knows the current version of an existing file
// before changing it. Call it with the team lock held.
func (e *Executor) fresh(p string) error {
	cur, err := e.WS.ReadFile(p, 0, 0)
	if err != nil {
		return nil // missing, or not text we could compare: nothing to protect
	}
	k, ok := e.known[key(p)]
	if !ok {
		return fmt.Errorf("%s already exists and you have not read it in this conversation: read it first, so you change the current version", p)
	}
	if k != hash(cur) {
		by := ""
		if e.Team != nil {
			if w, ok := e.Team.writer[key(p)]; ok && w != e.Self && w <= len(e.Team.Members) {
				by = " (by " + e.Team.Members[w-1] + ")"
			}
		}
		return fmt.Errorf("%s changed since you last read it%s: read it again and redo your change on the current version", p, by)
	}
	return nil
}

// changed records a successful change by this agent.
func (e *Executor) changed(p string) {
	if e.Team != nil {
		e.Team.changed(e.Self, p)
	}
}

const (
	maxListEntries = 400
	maxSearchHits  = 200
)

// Summarize renders a tool call's arguments as one short line for the UI.
func Summarize(call provider.ToolCall) string {
	var in map[string]any
	if json.Unmarshal(call.Input, &in) != nil {
		return ""
	}
	p, _ := in["path"].(string)
	switch call.Name {
	case WriteFile:
		c, _ := in["content"].(string)
		return fmt.Sprintf("%s (%s)", p, ui.Bytes(len(c)))
	case SearchFiles:
		pat, _ := in["pattern"].(string)
		if g, _ := in["glob"].(string); g != "" {
			return fmt.Sprintf("/%s/ in %s", pat, g)
		}
		return fmt.Sprintf("/%s/", pat)
	case DeclareDone:
		return ""
	case RunCommand:
		c, _ := in["command"].(string)
		return "$ " + oneLine(c)
	case AddTodo:
		text, _ := in["text"].(string)
		return "☐ " + oneLine(text)
	case ResolveTodo:
		id, _ := in["id"].(float64)
		return fmt.Sprintf("☑ #%d", int(id))
	case UpdateNotes:
		sec, _ := in["section"].(string)
		return "§ " + sec
	case MessageTeam:
		to, _ := in["to"].(string)
		msg, _ := in["message"].(string)
		return "→ " + to + ": " + msg
	case ListDir:
		if p == "" {
			return "."
		}
	}
	return p
}

func fail(format string, args ...any) Result {
	msg := fmt.Sprintf(format, args...)
	return Result{Content: "Error: " + msg, IsError: true, Summary: msg}
}

// decode parses the call input strictly. Streamed tool inputs can be cut
// off or malformed, so nothing runs unless the input parses and every
// required field is present.
func decode(raw json.RawMessage, v any, required ...string) error {
	dec := json.NewDecoder(strings.NewReader(string(raw)))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		return fmt.Errorf("invalid input: %v", err)
	}
	var present map[string]json.RawMessage
	json.Unmarshal(raw, &present)
	for _, f := range required {
		if _, ok := present[f]; !ok {
			return fmt.Errorf("invalid input: missing required field %q", f)
		}
	}
	return nil
}

// Run executes one call.
func (e *Executor) Run(ctx context.Context, call provider.ToolCall) Result {
	if err := ctx.Err(); err != nil {
		return fail("cancelled")
	}
	switch call.Name {
	case ListDir:
		var in struct {
			Path  string `json:"path"`
			Depth int    `json:"depth"`
		}
		if err := decode(call.Input, &in); err != nil {
			return fail("%v", err)
		}
		if in.Depth > 6 {
			in.Depth = 6
		}
		entries, truncated, err := e.WS.List(in.Path, in.Depth, maxListEntries)
		if err != nil {
			return fail("%v", err)
		}
		var b strings.Builder
		for _, en := range entries {
			if en.IsDir {
				fmt.Fprintf(&b, "%s/\n", en.Path)
			} else {
				fmt.Fprintf(&b, "%s  (%s)\n", en.Path, ui.Bytes(int(en.Size)))
			}
		}
		if len(entries) == 0 {
			b.WriteString("(empty)\n")
		}
		if truncated {
			fmt.Fprintf(&b, "… truncated at %d entries; list a subdirectory for more\n", maxListEntries)
		}
		return Result{Content: b.String(), Summary: fmt.Sprintf("%d entries", len(entries))}

	case ReadFile:
		var in struct {
			Path   string `json:"path"`
			Offset int    `json:"offset"`
			Limit  int    `json:"limit"`
		}
		if err := decode(call.Input, &in, "path"); err != nil {
			return fail("%v", err)
		}
		text, err := e.WS.ReadFile(in.Path, in.Offset, in.Limit)
		if err != nil {
			return fail("%v", err)
		}
		lines := strings.Count(text, "\n")
		if in.Offset == 0 && in.Limit == 0 {
			if e.seen != nil && e.seen[key(in.Path)] == hash(text) {
				return Result{
					Content: fmt.Sprintf("%s is unchanged since you last read or wrote it in this conversation (%d lines): use that copy. To get it again anyway, call read_file with offset 1.", in.Path, lines),
					Summary: "unchanged, not resent",
				}
			}
			e.remember(in.Path, text, true)
		} else if full, err := e.WS.ReadFile(in.Path, 0, 0); err == nil {
			// A window of the current version is enough to edit it.
			e.remember(in.Path, full, false)
		}
		if text == "" {
			text = "(empty file)"
		}
		return Result{Content: text, Summary: fmt.Sprintf("%d lines", lines)}

	case SearchFiles:
		var in struct {
			Pattern string `json:"pattern"`
			Path    string `json:"path"`
			Glob    string `json:"glob"`
		}
		if err := decode(call.Input, &in, "pattern"); err != nil {
			return fail("%v", err)
		}
		hits, truncated, err := e.WS.Search(in.Pattern, in.Path, in.Glob, maxSearchHits)
		if err != nil {
			return fail("%v", err)
		}
		var b strings.Builder
		for _, h := range hits {
			fmt.Fprintf(&b, "%s:%d: %s\n", h.Path, h.Line, ui.Truncate(h.Text, 200))
		}
		if len(hits) == 0 {
			b.WriteString("no matches\n")
		}
		if truncated {
			fmt.Fprintf(&b, "… truncated at %d matches; narrow the pattern or path\n", maxSearchHits)
		}
		return Result{Content: b.String(), Summary: fmt.Sprintf("%d matches", len(hits))}

	case WriteFile:
		var in struct {
			Path    string `json:"path"`
			Content string `json:"content"`
		}
		if err := decode(call.Input, &in, "path", "content"); err != nil {
			return fail("%v", err)
		}
		unlock := e.lock()
		defer unlock()
		if err := e.fresh(in.Path); err != nil {
			return fail("%v", err)
		}
		if err := e.WS.WriteFile(in.Path, in.Content); err != nil {
			return fail("%v", err)
		}
		if text, err := e.WS.ReadFile(in.Path, 0, 0); err == nil {
			e.remember(in.Path, text, true)
		}
		e.changed(in.Path)
		return Result{Content: fmt.Sprintf("Wrote %s (%d bytes).", in.Path, len(in.Content)), Summary: "written"}

	case EditFile:
		var in struct {
			Path       string `json:"path"`
			OldString  string `json:"old_string"`
			NewString  string `json:"new_string"`
			ReplaceAll bool   `json:"replace_all"`
		}
		if err := decode(call.Input, &in, "path", "old_string", "new_string"); err != nil {
			return fail("%v", err)
		}
		unlock := e.lock()
		defer unlock()
		if err := e.fresh(in.Path); err != nil {
			return fail("%v", err)
		}
		n, err := e.WS.EditFile(in.Path, in.OldString, in.NewString, in.ReplaceAll)
		if err != nil {
			return fail("%v", err)
		}
		// The agent knows the new version but does not have it verbatim.
		if text, err := e.WS.ReadFile(in.Path, 0, 0); err == nil {
			e.remember(in.Path, text, false)
		}
		e.changed(in.Path)
		return Result{Content: fmt.Sprintf("Edited %s (%d replacement(s)).", in.Path, n), Summary: fmt.Sprintf("%d replaced", n)}

	case DeletePath:
		var in struct {
			Path string `json:"path"`
		}
		if err := decode(call.Input, &in, "path"); err != nil {
			return fail("%v", err)
		}
		unlock := e.lock()
		defer unlock()
		if err := e.fresh(in.Path); err != nil {
			return fail("%v", err)
		}
		if err := e.WS.Remove(in.Path); err != nil {
			return fail("%v", err)
		}
		e.forgetPath(in.Path)
		e.changed(in.Path)
		return Result{Content: fmt.Sprintf("Deleted %s.", in.Path), Summary: "deleted"}

	case UpdateNotes:
		var in struct {
			Section string `json:"section"`
			Content string `json:"content"`
		}
		if err := decode(call.Input, &in, "section", "content"); err != nil {
			return fail("%v", err)
		}
		if e.Notes == nil {
			return fail("project notes are not available in this run")
		}
		if err := e.Notes.Update(in.Section, in.Content, e.Author); err != nil {
			return fail("%v", err)
		}
		if strings.TrimSpace(in.Content) == "" {
			return Result{Content: fmt.Sprintf("Deleted notes section %q.", in.Section), Summary: "deleted"}
		}
		return Result{Content: fmt.Sprintf("Saved notes section %q. Teammates see it at their next turn.", in.Section), Summary: "saved"}

	case MessageTeam:
		var in struct {
			To      string `json:"to"`
			Message string `json:"message"`
		}
		if err := decode(call.Input, &in, "to", "message"); err != nil {
			return fail("%v", err)
		}
		if e.Team == nil || e.Team.Send == nil || len(e.Team.Members) < 2 {
			return fail("you have no teammates in this task")
		}
		to := strings.ToLower(strings.TrimSpace(in.To))
		var targets []int
		for i, m := range e.Team.Members {
			if i+1 != e.Self && (to == "all" || to == "everyone" || strings.EqualFold(m, to)) {
				targets = append(targets, i+1)
			}
		}
		if len(targets) == 0 {
			return fail("no teammate %q: use one of %s, or \"all\"", in.To, strings.Join(e.Team.Members, ", "))
		}
		if strings.TrimSpace(in.Message) == "" {
			return fail("empty message")
		}
		for _, t := range targets {
			if err := e.Team.Send(e.Self, t, strings.TrimSpace(in.Message)); err != nil {
				return fail("%v", err)
			}
		}
		return Result{Content: "Sent.", Summary: "sent"}

	case RunCommand:
		return e.runCommand(ctx, call)
	case AddTodo:
		var in struct {
			Text string `json:"text"`
		}
		if err := decode(call.Input, &in, "text"); err != nil {
			return fail("%v", err)
		}
		if strings.TrimSpace(in.Text) == "" {
			return fail("the item is empty")
		}
		if e.Team == nil || e.Team.Todo == nil {
			return fail("to-dos are not available")
		}
		id, err := e.Team.Todo(e.Self, strings.TrimSpace(in.Text))
		if err != nil {
			return fail("%v", err)
		}
		return Result{Content: fmt.Sprintf("Recorded as item #%d for the person who gave the task.", id), Summary: fmt.Sprintf("#%d", id)}
	case ResolveTodo:
		var in struct {
			ID   int    `json:"id"`
			Note string `json:"note"`
		}
		if err := decode(call.Input, &in, "id", "note"); err != nil {
			return fail("%v", err)
		}
		if e.Team == nil || e.Team.Resolve == nil {
			return fail("to-dos are not available")
		}
		if err := e.Team.Resolve(e.Self, in.ID, strings.TrimSpace(in.Note)); err != nil {
			return fail("%v", err)
		}
		return Result{Content: fmt.Sprintf("Item #%d closed.", in.ID), Summary: "resolved"}
	case DeclareDone:
		var in struct {
			Summary string `json:"summary"`
		}
		if err := decode(call.Input, &in, "summary"); err != nil {
			return fail("%v", err)
		}
		return Result{
			Content: "Recorded. End your turn now with a brief closing message for your teammates.",
			Summary: "done", Done: true, DoneSummary: in.Summary,
		}
	}
	return fail("unknown tool %q", call.Name)
}
