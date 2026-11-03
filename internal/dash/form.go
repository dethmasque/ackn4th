package dash

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
	"unicode"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/dethmasque/ackn4th/internal/task"
)

// NewTask is what the new-task form asks the host to start.
type NewTask struct {
	Name      string // "" = suggested from the prompt
	Prompt    string
	Workspace string
	Trust     bool // trust Workspace first (the operator ticked the box)
	Parallel  bool
	DraftPR   bool // propose the changes as a draft GitHub pull request when done
	// Repo (owner/name) is cloned, or reused, under the default workspace
	// root and used instead of Workspace; Branch is checked out or created.
	Repo   string
	Branch string
	Rounds int // 0 = until consensus
	// MaxCost caps the task's spending in dollars; 0 = the config's
	// spend.per_task.
	MaxCost float64
	Agents  []NewAgent
}

// NewAgent is one agent row of the form.
type NewAgent struct {
	Role     string // one role, or several joined with "+"
	Provider string
	Model    string
	Effort   string // "" = the provider's default
}

// LoginRequired is returned by Options.Launch when a provider needs a
// sign-in first; the form offers to run it.
type LoginRequired struct {
	Providers []string
	Err       error
}

func (e *LoginRequired) Error() string { return e.Err.Error() }
func (e *LoginRequired) Unwrap() error { return e.Err }

// Choice is one option the form offers.
type Choice struct {
	Value string
	Label string // shown; Value when empty
	Note  string // dimmed, after the label
}

func (c Choice) label() string {
	if c.Label != "" {
		return c.Label
	}
	return c.Value
}

// ProviderChoice describes a provider for the form.
type ProviderChoice struct {
	Name   string
	Note   string
	Models []string // suggestions; the first is the default
	// AutoModel, when set, is what an empty model means (openai: the
	// newest the key can use, picked at launch); "" is then a choice.
	AutoModel     string
	Efforts       []string // accepted efforts, lowest first
	DefaultEffort string
}

// FormDefaults seed the new-task form.
type FormDefaults struct {
	Workspace string
	Rounds    int
	MaxCost   float64  // the config's spend.per_task, 0 for none
	Provider  string   // the team's default provider
	Roles     []string // the agent rows to start with
}

const customModel = "\x00custom"

// input is a minimal line editor.
type input struct {
	r   []rune
	cur int
}

func newInput(s string) input { r := []rune(s); return input{r: r, cur: len(r)} }

func (in *input) String() string { return string(in.r) }

func (in *input) insert(s string) {
	add := []rune(s)
	in.r = append(in.r[:in.cur], append(add, in.r[in.cur:]...)...)
	in.cur += len(add)
}

// edit applies an editing key; it reports whether the key was used.
func (in *input) edit(k string, multiline bool) bool {
	switch k {
	case "left":
		in.cur = max(0, in.cur-1)
	case "right":
		in.cur = min(len(in.r), in.cur+1)
	case "home", "ctrl+a":
		in.cur = 0
	case "end", "ctrl+e":
		in.cur = len(in.r)
	case "backspace":
		if in.cur > 0 {
			in.r = append(in.r[:in.cur-1], in.r[in.cur:]...)
			in.cur--
		}
	case "delete":
		if in.cur < len(in.r) {
			in.r = append(in.r[:in.cur], in.r[in.cur+1:]...)
		}
	case "ctrl+w":
		i := in.cur
		for i > 0 && unicode.IsSpace(in.r[i-1]) {
			i--
		}
		for i > 0 && !unicode.IsSpace(in.r[i-1]) {
			i--
		}
		in.r = append(in.r[:i], in.r[in.cur:]...)
		in.cur = i
	case "ctrl+u":
		in.r = in.r[in.cur:]
		in.cur = 0
	case "enter":
		if !multiline {
			return false
		}
		in.insert("\n")
	case "space":
		in.insert(" ")
	default:
		if r := []rune(k); len(r) == 1 && unicode.IsPrint(r[0]) {
			in.insert(k)
			return true
		}
		return false
	}
	return true
}

// field identifies one focusable control.
type field struct {
	kind  string // prompt name workspace trust mode rounds role provider model effort remove add cancel launch
	agent int
}

type agentRow struct {
	role, provider, model, effort string
	editModel                     *input // set while typing a custom model
}

// dropdown is an open list of choices under a field.
type dropdown struct {
	owner   field
	choices []Choice
	idx     int
	top     int
	multi   map[int]bool // role picker: several roles for one agent
	picked  bool         // space was used: enter keeps the marked set
	x, y, w int          // where it opens, set while rendering
	colors  map[string]rgb
}

// form is the new-task modal.
type form struct {
	opt       *Options
	prompt    input
	name      input
	workspace input
	repo      input
	branch    input
	rounds    input
	cost      input // the spending cap, in dollars
	trust     bool
	parallel  bool
	draftPR   bool
	agents    []agentRow
	focus     int
	drop      *dropdown
	err       string
	help      bool     // the help panel is open
	login     []string // providers to sign in to, when err says so
	busy      bool
	top       int // first agent row shown, when they don't all fit
	// belowAgents is the form's row under the last agent row, and x, y
	// where the form was drawn: the walkthrough's tooltip points there.
	belowAgents, x, y int
	promptW           int // the prompt editor's width when last drawn
	// team is set when the form is a follow-up for that task: a prompt,
	// and its agents' roles, models and efforts. Providers stay, and
	// agents can't be added or removed (their conversations are theirs).
	team *task.Task
}

// followUp is what the form sends in follow-up mode.
type followUpMsg struct{ r *resumeReq }

// newFollowUpForm is the form for a follow-up to t.
func newFollowUpForm(opt *Options, t *task.Task) *form {
	f := &form{opt: opt, team: t}
	for _, a := range t.Agents {
		f.agents = append(f.agents, agentRow{role: a.Role, provider: a.Provider, model: a.Model, effort: a.Effort})
	}
	return f
}

type launchedMsg struct {
	id  string
	err error
}

type loginDoneMsg struct{ err error }

func newForm(opt *Options) *form {
	d := opt.Defaults
	f := &form{opt: opt, workspace: newInput(d.Workspace), rounds: newInput(strconv.Itoa(d.Rounds))}
	if d.MaxCost > 0 {
		f.cost = newInput(strconv.FormatFloat(d.MaxCost, 'f', -1, 64))
	}
	roles := d.Roles
	if len(roles) == 0 {
		roles = []string{"backend_dev"}
	}
	for _, r := range roles {
		f.agents = append(f.agents, f.newAgent(r, d.Provider))
	}
	return f
}

func (f *form) provider(name string) ProviderChoice {
	for _, p := range f.opt.Providers {
		if p.Name == name {
			return p
		}
	}
	if len(f.opt.Providers) > 0 {
		return f.opt.Providers[0]
	}
	return ProviderChoice{Name: name}
}

func (f *form) newAgent(role, provider string) agentRow {
	p := f.provider(provider)
	a := agentRow{role: role, provider: p.Name, effort: p.DefaultEffort}
	if len(p.Models) > 0 && p.AutoModel == "" {
		a.model = p.Models[0]
	}
	return a
}

// repoWhere says where a task for repo will work, in w cells.
func (f *form) repoWhere(repo string, w int) string {
	// The workspace field beside this isn't used with a repo: say so, and
	// where the agents will work instead.
	if f.opt.RepoDir == nil {
		return pDim(fit("← not used: the repo is cloned instead", w))
	}
	dir, exists, err := f.opt.RepoDir(repo)
	if err != nil {
		return pAmber(ansi.Truncate("✗ "+repoHint(err), w, "…"))
	}
	head := "← not used; clones to "
	if exists {
		head = "← not used; reuses the clone "
	}
	path := tildePath(dir)
	return pDim(head) + pLime(ansi.TruncateLeft(path, max(0, width(path)-(w-width(head))), "…"))
}

// repoHint is a repo error, short: the form shows it beside the field.
func repoHint(err error) string {
	if strings.Contains(err.Error(), "isn't a repository") {
		return "use owner/name or a github.com URL"
	}
	return err.Error()
}

// untrusted reports whether the workspace needs trusting first; a GitHub
// repo is cloned into the (trusted) default workspace root instead.
func (f *form) untrusted() bool {
	if strings.TrimSpace(f.repo.String()) != "" {
		return false
	}
	return f.opt.Trusted != nil && !f.opt.Trusted(strings.TrimSpace(f.workspace.String()))
}

// fields lists the controls in tab order, row by row.
func (f *form) rows() [][]field {
	if f.team != nil {
		rows := [][]field{{{kind: "prompt"}}}
		for i := range f.agents {
			rows = append(rows, []field{{"role", i}, {"model", i}, {"effort", i}})
		}
		return append(rows, []field{{kind: "cancel"}, {kind: "launch"}})
	}
	rows := [][]field{{{kind: "prompt"}}, {{kind: "name"}}}
	ws := []field{{kind: "workspace"}}
	if f.untrusted() {
		ws = append(ws, field{kind: "trust"})
	}
	rows = append(rows, ws, []field{{kind: "repo"}, {kind: "branch"}}, []field{{kind: "mode"}, {kind: "rounds"}, {kind: "cost"}, {kind: "pr"}})
	for i := range f.agents {
		rows = append(rows, []field{{"role", i}, {"provider", i}, {"model", i}, {"effort", i}, {"remove", i}})
	}
	return append(rows, []field{{kind: "add"}}, []field{{kind: "cancel"}, {kind: "launch"}})
}

func (f *form) fields() []field {
	var out []field
	for _, r := range f.rows() {
		out = append(out, r...)
	}
	return out
}

func (f *form) current() field {
	fs := f.fields()
	f.focus = min(max(f.focus, 0), len(fs)-1)
	return fs[f.focus]
}

func (f *form) focusOn(target field) {
	for i, x := range f.fields() {
		if x == target {
			f.focus = i
			return
		}
	}
}

// moveRow moves focus up or down a row, keeping the column where it can.
func (f *form) moveRow(d int) {
	rows := f.rows()
	cur := f.current()
	for r, row := range rows {
		for c, x := range row {
			if x != cur {
				continue
			}
			nr := min(max(r+d, 0), len(rows)-1)
			// Keep the column only between agent rows.
			if len(row) < 5 || len(rows[nr]) < 5 {
				c = 0
			}
			f.focusOn(rows[nr][min(c, len(rows[nr])-1)])
			return
		}
	}
}

func (f *form) moveCol(d int) {
	cur := f.current()
	for _, row := range f.rows() {
		for c, x := range row {
			if x == cur {
				f.focusOn(row[min(max(c+d, 0), len(row)-1)])
				return
			}
		}
	}
}

// text returns the editor behind a text field, if any.
func (f *form) text(x field) (*input, bool) {
	switch x.kind {
	case "prompt":
		return &f.prompt, true
	case "name":
		return &f.name, false
	case "workspace":
		return &f.workspace, false
	case "repo":
		return &f.repo, false
	case "branch":
		return &f.branch, false
	case "rounds":
		return &f.rounds, false
	case "cost":
		return &f.cost, false
	case "model":
		if e := f.agents[x.agent].editModel; e != nil {
			return e, false
		}
	}
	return nil, false
}

// key handles a key; done reports that the form should close.
func (f *form) key(k string) (cmd tea.Cmd, done bool) {
	if f.busy {
		return nil, false
	}
	if f.help {
		f.help = false
		return nil, false
	}
	if f.drop != nil {
		f.dropKey(k)
		return nil, false
	}
	cur := f.current()
	if cur.kind == "prompt" && (k == "up" || k == "down") && f.promptLine(k == "up") {
		return nil, false
	}
	if k == "f1" {
		f.help = true
		return nil, false
	}
	if in, multi := f.text(cur); in != nil {
		if cur.kind == "rounds" && (k == "space" || len(k) == 1 && !unicode.IsDigit(rune(k[0]))) {
			return nil, false
		}
		if cur.kind == "cost" && (k == "space" || len(k) == 1 && !unicode.IsDigit(rune(k[0])) && k != ".") {
			return nil, false
		}
		if cur.kind == "model" && (k == "enter" || k == "tab" || k == "esc") {
			a := &f.agents[cur.agent]
			if v := strings.TrimSpace(a.editModel.String()); v != "" && k != "esc" {
				a.model = v
			}
			a.editModel = nil
			if k == "tab" {
				f.focus++
			}
			return nil, false
		}
		if in.edit(k, multi) {
			f.err = ""
			// Typing a repo hides the trust checkbox above it (a repo
			// needs no trusting), which would move focus off the field.
			f.focusOn(cur)
			return nil, false
		}
	}
	switch k {
	case "?":
		f.help = true
	case "esc":
		return nil, true
	case "tab":
		f.focus = (f.focus + 1) % len(f.fields())
	case "shift+tab":
		f.focus = (f.focus - 1 + len(f.fields())) % len(f.fields())
	case "up":
		f.moveRow(-1)
	case "down":
		f.moveRow(1)
	case "left":
		f.moveCol(-1)
	case "right":
		f.moveCol(1)
	case "ctrl+s":
		return f.launch(), false
	case "ctrl+n":
		if f.team == nil {
			f.addAgent()
		}
	case "x", "ctrl+x", "delete", "backspace":
		if isAgentField(cur.kind) && f.team == nil {
			f.removeAgent(cur.agent)
		}
	case "ctrl+l":
		if len(f.login) > 0 && f.opt.Login != nil {
			provs := f.login
			return tea.ExecProcess(f.opt.Login(provs), func(err error) tea.Msg { return loginDoneMsg{err} }), false
		}
	case "enter", "space":
		return f.activate(cur)
	}
	return nil, false
}

func (f *form) addAgent() {
	prov := f.opt.Defaults.Provider
	role := "code_reviewer"
	if n := len(f.agents); n > 0 {
		prov = f.agents[n-1].provider
		if n%2 == 0 {
			role = "backend_dev"
		}
	}
	f.agents = append(f.agents, f.newAgent(role, prov))
	f.focusOn(field{"role", len(f.agents) - 1})
}

func isAgentField(kind string) bool {
	switch kind {
	case "role", "provider", "model", "effort", "remove":
		return true
	}
	return false
}

// removeAgent drops an agent row; the last one may go too, and the form
// then asks for one before launching.
func (f *form) removeAgent(i int) {
	f.agents = append(f.agents[:i], f.agents[i+1:]...)
	if len(f.agents) == 0 {
		f.focusOn(field{kind: "add"})
		return
	}
	f.focusOn(field{"remove", min(i, len(f.agents)-1)})
}

func (f *form) activate(cur field) (tea.Cmd, bool) {
	switch cur.kind {
	case "trust":
		f.trust = !f.trust
	case "mode":
		f.parallel = !f.parallel
	case "pr":
		f.draftPR = !f.draftPR
	case "role", "provider", "model", "effort":
		f.open(cur)
	case "remove":
		f.removeAgent(cur.agent)
	case "add":
		f.addAgent()
	case "cancel":
		return nil, true
	case "launch":
		return f.launch(), false
	case "prompt", "name", "workspace", "repo", "branch", "rounds", "cost":
		f.focus++
	}
	return nil, false
}

// paste inserts pasted text into the focused text field.
func (f *form) paste(s string) {
	if f.drop != nil || f.busy {
		return
	}
	cur := f.current()
	in, multi := f.text(cur)
	if in == nil {
		return
	}
	s = strings.ReplaceAll(s, "\r\n", "\n")
	switch {
	case cur.kind == "rounds", cur.kind == "cost":
		s = strings.Map(func(r rune) rune {
			if unicode.IsDigit(r) || r == '.' && cur.kind == "cost" {
				return r
			}
			return -1
		}, s)
	case !multi:
		s = strings.TrimSpace(strings.ReplaceAll(s, "\n", ""))
	}
	in.insert(s)
	f.err = ""
	f.focusOn(cur)
}

// open shows the dropdown for an agent field.
func (f *form) open(x field) {
	a := f.agents[x.agent]
	d := &dropdown{owner: x, colors: map[string]rgb{}}
	var selected []string
	switch x.kind {
	case "role":
		d.multi = map[int]bool{}
		selected = strings.Split(a.role, "+")
		d.choices = append(d.choices, f.opt.Roles...)
	case "provider":
		selected = []string{a.provider}
		for _, p := range f.opt.Providers {
			d.choices = append(d.choices, Choice{Value: p.Name, Note: p.Note})
			d.colors[p.Name] = providerColor(p.Name)
		}
	case "model":
		selected = []string{a.model}
		p := f.provider(a.provider)
		if p.AutoModel != "" {
			d.choices = append(d.choices, Choice{Value: "", Label: p.AutoModel, Note: "default"})
		}
		for i, m := range p.Models {
			note := ""
			if i == 0 && p.AutoModel == "" {
				note = "default"
			}
			d.choices = append(d.choices, Choice{Value: m, Note: note})
		}
		d.choices = append(d.choices, Choice{Value: customModel, Label: "✎ type a model id…"})
	case "effort":
		selected = []string{a.effort}
		p := f.provider(a.provider)
		d.choices = append(d.choices, Choice{Value: "", Label: "default", Note: "the model decides"})
		for _, e := range p.Efforts {
			d.choices = append(d.choices, Choice{Value: e})
		}
	}
	for i, c := range d.choices {
		for _, s := range selected {
			if c.Value == s {
				if d.multi != nil {
					d.multi[i] = true
				}
				if d.idx == 0 || d.multi == nil {
					d.idx = i
				}
			}
		}
	}
	f.drop = d
}

func (f *form) dropKey(k string) {
	d := f.drop
	switch k {
	case "esc":
		f.drop = nil
	case "up", "k":
		d.idx = (d.idx - 1 + len(d.choices)) % len(d.choices)
	case "down", "j":
		d.idx = (d.idx + 1) % len(d.choices)
	case "pgup":
		d.idx = max(0, d.idx-8)
	case "pgdown":
		d.idx = min(len(d.choices)-1, d.idx+8)
	case "space":
		if d.multi != nil {
			d.multi[d.idx] = !d.multi[d.idx]
			d.picked = true
			return
		}
		f.choose()
	case "enter", "tab":
		f.choose()
	default:
		// Type a letter to jump to the next choice starting with it.
		if r := []rune(k); len(r) == 1 && unicode.IsLetter(r[0]) {
			for n := 1; n <= len(d.choices); n++ {
				i := (d.idx + n) % len(d.choices)
				if strings.HasPrefix(strings.ToLower(d.choices[i].label()), strings.ToLower(k)) {
					d.idx = i
					break
				}
			}
		}
	}
}

func (f *form) choose() {
	d := f.drop
	f.drop = nil
	a := &f.agents[d.owner.agent]
	v := d.choices[d.idx].Value
	switch d.owner.kind {
	case "role":
		// Enter alone replaces the role with the highlighted one; after
		// space has marked roles, enter keeps exactly what is marked.
		var picked []string
		for i, c := range d.choices {
			if d.picked && d.multi[i] {
				picked = append(picked, c.Value)
			}
		}
		if len(picked) == 0 {
			picked = []string{v}
		}
		a.role = strings.Join(picked, "+")
	case "provider":
		if v != a.provider {
			*a = f.newAgent(a.role, v)
		}
	case "model":
		if v == customModel {
			in := newInput(a.model)
			a.editModel = &in
			return
		}
		a.model = v
	case "effort":
		a.effort = v
	}
	f.err = ""
}

// suggestedName is the name a task gets if the Name field is left empty.
func (f *form) suggestedName() string {
	if strings.TrimSpace(f.prompt.String()) == "" {
		return "a name for the task, e.g. “Rate limiting”"
	}
	return task.DefaultName(f.prompt.String()) + "  (from the prompt; type to change)"
}

// request validates the form into a NewTask.
func (f *form) request() (NewTask, error) {
	t := NewTask{
		Prompt:    strings.TrimSpace(f.prompt.String()),
		Name:      strings.TrimSpace(f.name.String()),
		Workspace: strings.TrimSpace(f.workspace.String()),
		Trust:     f.trust,
		Parallel:  f.parallel,
		DraftPR:   f.draftPR,
		Repo:      strings.TrimSpace(f.repo.String()),
		Branch:    strings.TrimSpace(f.branch.String()),
	}
	if t.Prompt == "" {
		f.focusOn(field{kind: "prompt"})
		return t, errors.New("tell the agents what to do: write a prompt")
	}
	if t.Name != "" {
		name, err := task.CleanName(t.Name)
		if err != nil {
			f.focusOn(field{kind: "name"})
			return t, err
		}
		t.Name = name
	}
	if t.Workspace == "" && t.Repo == "" {
		f.focusOn(field{kind: "workspace"})
		return t, errors.New("choose a workspace directory, or a GitHub repo")
	}
	if t.Repo == "" && f.untrusted() && !f.trust {
		f.focusOn(field{kind: "trust"})
		return t, errors.New("agents may only work in a trusted directory: tick “trust it”")
	}
	n, err := strconv.Atoi(strings.TrimSpace(f.rounds.String()))
	if err != nil || n < 0 {
		f.focusOn(field{kind: "rounds"})
		return t, errors.New("rounds must be a number (0 runs until the agents agree)")
	}
	t.Rounds = n
	if c := strings.TrimSpace(f.cost.String()); c != "" {
		v, err := strconv.ParseFloat(c, 64)
		if err != nil || v < 0 {
			f.focusOn(field{kind: "cost"})
			return t, errors.New("the spending cap is an amount in dollars, like 5 or 2.50 (empty: the configured cap)")
		}
		t.MaxCost = v
	}
	if len(f.agents) == 0 {
		f.focusOn(field{kind: "add"})
		return t, errors.New("add at least one agent")
	}
	if t.Parallel && len(f.agents) < 2 {
		return t, errors.New("parallel needs at least two agents")
	}
	for _, a := range f.agents {
		t.Agents = append(t.Agents, NewAgent{Role: a.role, Provider: a.provider, Model: a.model, Effort: a.effort})
	}
	return t, nil
}

func (f *form) launch() tea.Cmd {
	if f.team != nil {
		return f.followUp()
	}
	if f.opt.Launch == nil {
		return nil
	}
	req, err := f.request()
	if err != nil {
		f.err = err.Error()
		return nil
	}
	f.busy, f.err, f.login = true, "", nil
	launch := f.opt.Launch
	return func() tea.Msg {
		id, err := launch(req)
		return launchedMsg{id, err}
	}
}

// followUp sends the follow-up, with the team as it now is (nil if
// unchanged), for the dashboard to start.
func (f *form) followUp() tea.Cmd {
	prompt := strings.TrimSpace(f.prompt.String())
	if prompt == "" {
		f.focusOn(field{kind: "prompt"})
		f.err = "write what the agents should do next"
		return nil
	}
	var team []NewAgent
	changed := false
	for i, a := range f.agents {
		old := f.team.Agents[i]
		team = append(team, NewAgent{Role: a.role, Provider: a.provider, Model: a.model, Effort: a.effort})
		changed = changed || a.role != old.Role || a.model != old.Model || a.effort != old.Effort
	}
	if !changed {
		team = nil
	}
	r := &resumeReq{task: f.team, followUp: prompt, team: team}
	return func() tea.Msg { return followUpMsg{r} }
}

// launched records the outcome; it reports whether the form is done.
func (f *form) launched(msg launchedMsg) bool {
	f.busy = false
	if msg.err == nil {
		return true
	}
	f.err = msg.err.Error()
	var lr *LoginRequired
	if errors.As(msg.err, &lr) && f.opt.Login != nil {
		f.login = lr.Providers
	}
	return false
}

// groups counts the distinct cache groups (provider, model, effort).
func (f *form) groups() int {
	seen := map[string]bool{}
	for _, a := range f.agents {
		seen[a.provider+"\x00"+a.model+"\x00"+a.effort] = true
	}
	return len(seen)
}

// cols are the widths of an agent row's fields, fitted to the form.
type cols struct{ role, provider, model, effort int }

func agentCols(inner int) cols {
	// "NN " + role + " " + provider + " " + model + " " + effort + " " + "✕"
	c := cols{provider: 11, effort: 11}
	rest := inner - 3 - 4 - 3 - c.provider - c.effort
	c.role = rest * 11 / 20
	c.model = rest - c.role
	return c
}

// render draws the form, w cells wide and at most maxH tall. It records
// where an open dropdown should appear, relative to the form's corner.
func (f *form) render(m *model, w, maxH int) string {
	inner := w - 4
	cur := f.current()
	focused := func(kind string, agent int) bool { return cur.kind == kind && cur.agent == agent && f.drop == nil }
	label := func(s string) string { return bold(pLime(s)) }
	c := agentCols(inner)

	// Fit the height: drop spacing first, then shrink the prompt, then
	// scroll the agent rows.
	pw := inner - 2
	f.promptW = pw
	plines := editorLines(f.prompt, pw, focused("prompt", 0))
	if f.prompt.String() == "" && !focused("prompt", 0) {
		plines = []string{pFaint("Describe the task, e.g. “Add rate limiting to the API, with tests”")}
	}
	budget := maxH - 2
	fixed := 15
	if f.team != nil {
		fixed = 12 // the task's lines instead of its settings, no add button
	}
	gaps, pl := 2, min(max(len(plines), 3), 7)
	if fixed+gaps+pl+max(1, len(f.agents)) > budget {
		gaps = 0
	}
	for pl > 2 && fixed+gaps+pl+max(1, len(f.agents)) > budget {
		pl--
	}
	shown := min(len(f.agents), max(1, budget-fixed-gaps-pl))
	if isAgentField(cur.kind) {
		if cur.agent < f.top {
			f.top = cur.agent
		}
		if cur.agent >= f.top+shown {
			f.top = cur.agent - shown + 1
		}
	}
	f.top = min(max(f.top, 0), len(f.agents)-shown)
	// Keep the cursor's line in view.
	if len(plines) > pl {
		at := cursorLine(f.prompt, pw)
		start := min(max(0, at-pl+1), len(plines)-pl)
		plines = plines[start : start+pl]
	}
	for len(plines) < pl {
		plines = append(plines, "")
	}

	var lines []string
	add := func(s ...string) { lines = append(lines, s...) }
	gap := func() {
		if gaps > 0 {
			add("")
		}
	}

	// The prompt editor.
	ask := "WHAT SHOULD THE AGENTS DO?"
	if f.team != nil {
		ask = "WHAT SHOULD THEY DO NEXT?"
	}
	add(label(ask) + pDim("   enter adds a line · tab moves on"))
	boxEdge := fgp(edge)
	if focused("prompt", 0) {
		boxEdge = fgp(green)
	}
	add(boxEdge("╭" + strings.Repeat("─", pw) + "╮"))
	for _, l := range plines {
		add(boxEdge("│") + fill(l, pw, bg1) + boxEdge("│"))
	}
	add(boxEdge("╰" + strings.Repeat("─", pw) + "╯"))
	if f.team != nil {
		f.renderTeamHead(add, inner)
		gap()
	} else {
		f.renderSettings(add, gap, inner, focused)
	}
	f.renderAgents(add, &lines, inner, c, shown, focused, label)
	return f.renderEnd(m, lines, inner, w, cur)
}

// renderTeamHead is a follow-up's lines under the prompt: where, and how.
func (f *form) renderTeamHead(add func(...string), inner int) {
	t := f.team
	add(pDim("IN         ")+pLime(ansi.Truncate(tildePath(t.Workspace), inner-11, "…")),
		pDim(fit("Same agents, conversations and workspace; all must declare done again.", inner)),
		pDim(fit("Change a role or effort freely; a new model starts that agent afresh.", inner)))
}

// renderSettings is the new-task form's name, workspace, repo and mode.
func (f *form) renderSettings(add func(...string), gap func(), inner int, focused func(string, int) bool) {
	name := textField(f.name, inner-11, focused("name", 0))
	if f.name.String() == "" && !focused("name", 0) {
		name = onp(bg3)(fill(" "+pFaint(f.suggestedName()), inner-11, bg3))
	}
	add(pDim("NAME       ") + name)
	gap()

	// Workspace and trust.
	trust := "  " + pLime("✓ trusted")
	if f.untrusted() {
		trust = "  " + checkbox("trust it", f.trust, focused("trust", 0))
	}
	fieldW := inner - 11 - width(trust)
	if repo := strings.TrimSpace(f.repo.String()); repo != "" {
		// The workspace isn't used: say where the work will be instead,
		// since it isn't the directory ackn4th was started in.
		fieldW = min(fieldW, 14)
		trust = "  " + f.repoWhere(repo, inner-11-fieldW-2)
	}
	add(pDim("WORKSPACE  ") + textField(f.workspace, fieldW, focused("workspace", 0)) + trust)
	bw := min(28, max(12, (inner-11-9-9)/3))
	add(pDim("GITHUB     ") + pDim("repo ") + textField(f.repo, inner-11-5-9-bw-2, focused("repo", 0)) +
		pDim("  branch ") + textField(f.branch, bw, focused("branch", 0)))

	// Mode and rounds.
	modeBg := bg2
	if focused("mode", 0) {
		modeBg = mix(bg3, green, 0.25)
	}
	mode := onp(modeBg)(" " + radio("turns", !f.parallel) + "   " + radio("parallel", f.parallel) + " ")
	pr := checkbox("draft PR when done", f.draftPR, focused("pr", 0))
	left := pDim("MODE       ") + mode + pDim("     ROUNDS ") + textField(f.rounds, 6, focused("rounds", 0)) +
		pDim("   CAP $") + textField(f.cost, 7, focused("cost", 0))
	hint := ""
	if room := inner - width(left) - width(pr) - 2; room >= 12 {
		hint = pDim(fit("  0 = until every agent agrees", room))
	} else {
		pr = checkbox("draft PR", f.draftPR, focused("pr", 0))
	}
	add(left + hint + strings.Repeat(" ", max(1, inner-width(left)-width(hint)-width(pr)-1)) + pr)
	gap()
}

// renderAgents is the agent rows.
func (f *form) renderAgents(add func(...string), lines *[]string, inner int, c cols, shown int, focused func(string, int) bool, label func(string) string) {
	// Agent rows.
	count := fmt.Sprintf("  %d  ", len(f.agents))
	if shown < len(f.agents) {
		count = fmt.Sprintf("  %d-%d of %d  ", f.top+1, f.top+shown, len(f.agents))
	}
	add(fit(label("AGENTS")+pDim(count), 3+c.role+1) + pFaint(fit("provider", c.provider+1)+fit("model", c.model+1)+"effort"))
	if len(f.agents) == 0 {
		add(pAmber("   no agents yet: add one below"))
	}
	for i := f.top; i < f.top+shown; i++ {
		a := f.agents[i]
		pc := providerColor(a.provider)
		var modelCell string
		if a.editModel != nil {
			modelCell = textField(*a.editModel, c.model, focused("model", i))
		} else {
			modelCell = dropField(f.modelLabel(a), c.model, text, focused("model", i), f.dropOpen("model", i))
		}
		effort := cmpOr(a.effort, "default")
		remove := pRed("✕")
		if focused("remove", i) {
			remove = onp(red)(fgp(bg0)(bold("✕")))
		}
		prov := dropField(a.provider, c.provider, pc, focused("provider", i), f.dropOpen("provider", i))
		if f.team != nil {
			remove, prov = " ", fgp(pc)(fit(" "+a.provider, c.provider))
		}
		row := fgp(pc)(fmt.Sprintf("%2d ", i+1)) +
			dropField(strings.ReplaceAll(a.role, "+", " + "), c.role, pc, focused("role", i), f.dropOpen("role", i)) + " " +
			prov + " " +
			modelCell + " " +
			dropField(effort, c.effort, text, focused("effort", i), f.dropOpen("effort", i)) + " " + remove
		add(row)
		if f.drop != nil && f.drop.owner.agent == i {
			x := 3
			switch f.drop.owner.kind {
			case "provider":
				x += c.role + 1
			case "model":
				x += c.role + c.provider + 2
			case "effort":
				x += c.role + c.provider + c.model + 3
			}
			// +2: the panel's border and margin; +1: the border above.
			f.drop.x, f.drop.y = x+2, len(*lines)
			f.drop.w = max(map[string]int{"role": 44, "provider": 30, "model": 34, "effort": 24}[f.drop.owner.kind], c.model)
		}
	}
	f.belowAgents = len(*lines) + 1 // +1: the panel's top border
	if f.team == nil {
		add("   "+button("+ ADD AGENT", focused("add", 0), bg2, false), "   "+buttonShadow("+ ADD AGENT", bg2))
	}
}

// renderEnd is the form's tips, errors and buttons, and the panel.
func (f *form) renderEnd(m *model, lines []string, inner, w int, cur field) string {
	add := func(s ...string) { lines = append(lines, s...) }
	focused := func(kind string, agent int) bool { return cur.kind == kind && cur.agent == agent && f.drop == nil }

	// The cache tip, any error, then the actions.
	switch g := f.groups(); {
	case g == 1 && len(f.agents) > 1:
		add(pLime(fmt.Sprintf("↯ all %d agents share one cached context: faster and cheaper", len(f.agents))))
	case g > 1:
		add(pAmber(fmt.Sprintf("↯ %d cache groups: agents with the same provider, model and effort share context", g)))
	default:
		add("")
	}
	switch {
	case f.err != "" && len(f.login) > 0:
		add(pRed("✗ "+f.err), pAmber("  press ")+pill("ctrl+l", amber, bg0)+pAmber(" to sign in to "+strings.Join(f.login, " and ")+"; you come right back here"))
	case f.err != "":
		add(pRed("✗ "+f.err), "")
	case f.drop == nil && fieldTips[cur.kind] != "":
		add(pLime("› ")+pDim(fit(fieldTips[cur.kind], inner-3)), "")
	default:
		add("", "")
	}
	launch := "▶ LAUNCH"
	if f.team != nil {
		launch = "▶ RUN IT"
	}
	if f.busy {
		launch = m.spin(spinner) + " CHECKING"
	}
	cancel := button("CANCEL", focused("cancel", 0), bg2, false)
	run := button(launch, focused("launch", 0), bg2, true)
	pad := strings.Repeat(" ", max(0, inner-width(cancel)-width(run)-3))
	add(pad+cancel+"  "+run, pad+buttonShadow("CANCEL", bg2)+"  "+buttonShadow(launch, bg2))

	for i := range lines {
		lines[i] = " " + lines[i]
	}
	if f.drop != nil {
		f.drop.y++ // the panel's top border
	}
	if f.team != nil {
		return panel("FOLLOW-UP “"+ansi.Truncate(f.team.Title(), w-30, "…")+"”", pDim("esc closes · ctrl+s runs it"), lines, w, len(lines)+2, true, bg2)
	}
	return panel("NEW TASK", pDim("f1 help · esc closes · ctrl+s launches"), lines, w, len(lines)+2, true, bg2)
}

func cmpOr(s, def string) string {
	if s == "" {
		return def
	}
	return s
}

// promptLine moves the prompt's cursor to the wrapped line above or
// below, keeping its column; it reports false on the first or last line,
// where the arrow keys move between fields instead.
func (f *form) promptLine(up bool) bool {
	in := &f.prompt
	spans := softWrap(in.r, max(f.promptW, 8)-1)
	at := cursorLine(*in, max(f.promptW, 8))
	to := at + 1
	if up {
		to = at - 1
	}
	if to < 0 || to >= len(spans) {
		return false
	}
	col := ansi.StringWidth(string(in.r[spans[at][0]:in.cur]))
	in.cur = spans[to][0]
	for w := 0; in.cur < spans[to][1]; in.cur++ {
		cw := ansi.StringWidth(string(in.r[in.cur]))
		if w+cw > col {
			break
		}
		w += cw
	}
	// A soft-wrapped line's end is the next line's start: stay on this one.
	if in.cur == spans[to][1] && to < len(spans)-1 && in.cur > spans[to][0] && in.r[in.cur-1] != '\n' && spans[to+1][0] == in.cur {
		in.cur--
	}
	return true
}

// cursorLine is the wrapped line the editor's cursor is on.
func cursorLine(in input, w int) int {
	spans := softWrap(in.r, w-1)
	for i, sp := range spans {
		if in.cur >= sp[0] && (in.cur < sp[1] || in.cur == sp[1] && (sp[1] == len(in.r) || in.r[sp[1]] == '\n')) {
			return i
		}
	}
	return len(spans) - 1
}

// softWrap lays text out in lines of at most limit cells, as [start, end)
// rune spans. Lines break after a space where they can; the space stays at
// the line's end (the editor keeps a spare column for it and the cursor).
// A word wider than a line is split. A newline ends a line and belongs to
// none.
func softWrap(r []rune, limit int) [][2]int {
	limit = max(limit, 1)
	var spans [][2]int
	start := 0
	for {
		col, brk, i := 0, -1, start
		for ; i < len(r) && r[i] != '\n'; i++ {
			cw := max(1, ansi.StringWidth(string(r[i])))
			if col+cw > limit {
				break
			}
			if r[i] == ' ' {
				brk = i + 1
			}
			col += cw
		}
		switch {
		case i == len(r):
			return append(spans, [2]int{start, i})
		case r[i] == '\n':
			spans = append(spans, [2]int{start, i})
			start = i + 1
		default: // the line is full at i
			end := i
			switch {
			case r[i] == ' ':
				end = i + 1
			case brk > start:
				end = brk
			case i == start:
				end = i + 1 // one character wider than the line
			}
			spans = append(spans, [2]int{start, end})
			start = end
		}
	}
}

// box draws the open dropdown, at most maxRows choices tall, with a
// scrollbar when it holds more.
func (d *dropdown) box(maxRows int) string {
	rows := min(len(d.choices), max(1, maxRows))
	if d.idx < d.top {
		d.top = d.idx
	}
	if d.idx >= d.top+rows {
		d.top = d.idx - rows + 1
	}
	surf := hex("#14261a")
	border := fgp(moss)
	inner := d.w - 2
	hint := " ⏎ choose "
	if d.multi != nil {
		hint = " space picks · ⏎ done "
	}
	top := fill(border("╭")+pDim(hint)+border(strings.Repeat("─", max(0, inner-width(hint))))+border("╮"), d.w, surf)
	out := []string{top}
	for i := d.top; i < d.top+rows; i++ {
		ch := d.choices[i]
		mark := "  "
		switch {
		case d.multi != nil && d.multi[i]:
			mark = pLime("■ ")
		case d.multi != nil:
			mark = pFaint("□ ")
		}
		dot := ""
		if col, ok := d.colors[ch.Value]; ok {
			dot = fgp(col)("● ")
		}
		lbl := pText(ch.label())
		rowBg := surf
		if i == d.idx {
			rowBg = mix(surf, green, 0.3)
			lbl = bold(pWhite(ch.label()))
		}
		note := ""
		if ch.Note != "" {
			note = "  " + pDim(ch.Note)
		}
		bar := " "
		if len(d.choices) > rows {
			pos := d.top * rows / len(d.choices)
			size := max(1, rows*rows/len(d.choices))
			if r := i - d.top; r >= pos && r < pos+size {
				bar = pMoss("▐")
			} else {
				bar = pFaint("│")
			}
		}
		out = append(out, border("│")+fill(" "+mark+dot+lbl+note, inner-1, rowBg)+onp(surf)(bar)+border("│"))
	}
	more := ""
	if n := len(d.choices) - d.top - rows; n > 0 {
		more = fmt.Sprintf(" ↓ %d more ", n)
	}
	out = append(out, fill(border("╰")+pDim(more)+border(strings.Repeat("─", max(0, inner-width(more))))+border("╯"), d.w, surf))
	for i := range out {
		out[i] = fill(out[i], d.w, surf)
	}
	return strings.Join(out, "\n")
}

func (f *form) dropOpen(kind string, agent int) bool {
	return f.drop != nil && f.drop.owner == field{kind, agent}
}

// editorLines wraps an editor's text between words, with a block cursor
// when focused.
func editorLines(in input, w int, focused bool) []string {
	r := in.r
	cursor := onp(lime)
	var lines []string
	for _, sp := range softWrap(r, w-1) {
		var b strings.Builder
		for i := sp[0]; i < sp[1]; i++ {
			if focused && i == in.cur {
				b.WriteString(cursor(fgp(bg0)(string(r[i]))))
			} else {
				b.WriteString(pText(string(r[i])))
			}
		}
		// The cursor after a line's last character: at a newline or the
		// end of the text.
		if focused && in.cur == sp[1] && (sp[1] == len(r) || r[sp[1]] == '\n') {
			b.WriteString(cursor(fgp(bg0)(" ")))
		}
		lines = append(lines, b.String())
	}
	return lines
}

// textField is a single-line input, w cells wide.
func textField(in input, w int, focused bool) string {
	surface := bg3
	if focused {
		surface = mix(bg3, green, 0.22)
	}
	shown := in
	line := editorLines(shown, 1<<20, focused)[0]
	// Keep the cursor visible in long values.
	if width(line) > w-2 {
		line = trimLeft(line, width(line)-(w-2))
	}
	return onp(surface)(fill(" "+line, w, surface))
}

func trimLeft(s string, n int) string {
	if n <= 0 {
		return s
	}
	return "…" + ansi.TruncateLeft(s, n+1, "")
}

// dropField is a dropdown's closed face: value and a chevron, raised.
func dropField(value string, w int, accent rgb, focused, open bool) string {
	surface := bg3
	chev := "▾"
	if open {
		chev = "▴"
	}
	fgc := text
	if focused || open {
		surface, fgc = mix(bg3, accent, 0.35), white
	}
	return fill(fgp(fgc)(" "+fit(value, w-3))+fgp(accent)(chev)+" ", w, surface)
}

func checkbox(label string, on, focused bool) string {
	box := pDim("□ ")
	if on {
		box = pLime("■ ")
	}
	s := box + pText(label)
	if focused {
		return onp(mix(bg3, green, 0.25))(" " + s + " ")
	}
	return " " + s + " "
}

func radio(label string, on bool) string {
	if on {
		return pLime("● ") + bold(pWhite(label))
	}
	return pDim("○ " + label)
}

// button draws a raised button; buttonShadow is the row beneath it. The
// shadow is a half-cell offset to the right and below, so the button seems
// to float over the surface.
func button(label string, focused bool, surface rgb, primary bool) string {
	face, fgc := bg3, text
	switch {
	case focused:
		face, fgc = green, bg0
	case primary:
		face, fgc = mix(bg3, green, 0.35), white
	}
	return onp(face)(fgp(fgc)(bold("  "+label+"  "))) + onp(surface)(fgp(shadowColor)("▄"))
}

func buttonShadow(label string, surface rgb) string {
	return onp(surface)(" " + fgp(shadowColor)(strings.Repeat("▀", width(label)+4)))
}

var (
	shadowColor = hex("#010301")
	shadowFg    = hex("#18271d") // what shows through a drop shadow
	backdropFg  = hex("#1a2b1f") // the dashboard, dimmed behind a modal
)

// shadow darkens the cells a floating block at (x, y), w×h, would shade:
// a strip two cells wide on its right and one row below, offset so the
// light seems to come from the top left.
func shadow(lines []string, x, y, w, h, total int) {
	shade := func(r, a, b int) {
		b = min(b, total)
		if r < 0 || r >= len(lines) || a >= b {
			return
		}
		row := lines[r]
		seg := ansi.Strip(ansi.Cut(row, a, b))
		lines[r] = ansi.Cut(row, 0, a) + "\x1b[0m" + shadowColor.bg() + shadowFg.fg() + seg + "\x1b[0m" + ansi.Cut(row, b, total)
	}
	for r := y + 1; r < y+h; r++ {
		shade(r, x+w, x+w+2)
	}
	shade(y+h, x+2, x+w+2)
}

// overlayAt draws block over lines with its top-left corner at (x, y).
func overlayAt(lines []string, block string, x, y, total int) {
	for i, line := range strings.Split(block, "\n") {
		r := y + i
		if r < 0 || r >= len(lines) {
			continue
		}
		bw := width(line)
		row := lines[r]
		lines[r] = ansi.Cut(row, 0, x) + "\x1b[0m" + line + "\x1b[0m" + ansi.Cut(row, x+bw, total)
	}
}

// drawForm floats the new-task form, and any open dropdown, over a dimmed
// dashboard, each with a drop shadow.
func (m *model) drawForm(lines []string) string {
	for i := range lines[:len(lines)-1] {
		lines[i] = onp(bg0)(fgp(backdropFg)(ansi.Strip(lines[i])))
	}
	fw := min(112, m.w-4)
	block := m.form.render(m, fw, m.h-3)
	bh := strings.Count(block, "\n") + 1
	x, y := (m.w-fw-2)/2, max(0, (m.h-1-bh-1)/2)
	m.form.x, m.form.y = x, y
	shadow(lines, x, y, fw, bh, m.w)
	overlayAt(lines, block, x, y, m.w)
	if d := m.form.drop; d != nil {
		field := y + d.y - 1 // the row the dropdown belongs to
		below := m.h - 2 - (field + 1) - 2
		above := field - 2
		rows := min(len(d.choices), 10)
		dy := field + 1
		if below < min(rows, 5) && above > below {
			rows = min(rows, above)
			dy = field - rows - 2
		} else {
			rows = min(rows, max(1, below))
		}
		dx := min(x+d.x, m.w-d.w-2)
		box := d.box(rows)
		shadow(lines, dx, dy, d.w, rows+2, m.w)
		overlayAt(lines, box, dx, dy, m.w)
	}
	lines[len(lines)-1] = m.formFooter()
	screen := strings.Join(lines, "\n")
	if m.form.help {
		screen = overlay(screen, formHelpBox(m.w, m.h-1), m.w)
	}
	return screen
}

func (m *model) formFooter() string {
	chip := func(k, label string) string { return pill(k, bg3, lime) + pDim(" "+label+"  ") }
	keys := [][2]string{{"f1", "help"}, {"tab", "next"}, {"↑↓←→", "move"}, {"⏎", "open · choose"}, {"space", "toggle"}, {"ctrl+n", "add agent"}, {"del", "remove agent"}, {"ctrl+s", "launch"}, {"esc", "close"}}
	if m.form.team != nil {
		keys = [][2]string{{"tab", "next"}, {"↑↓←→", "move"}, {"⏎", "new line · open a list"}, {"ctrl+s", "run it"}, {"esc", "cancel"}}
	}
	if m.form.drop != nil {
		keys = [][2]string{{"↑↓", "move"}, {"a-z", "jump"}, {"⏎", "choose"}, {"esc", "close"}}
		if m.form.drop.multi != nil {
			keys = [][2]string{{"↑↓", "move"}, {"space", "pick several: one agent plays them all"}, {"⏎", "done"}, {"esc", "cancel"}}
		}
	}
	var b strings.Builder
	b.WriteString(" ")
	for _, k := range keys {
		b.WriteString(chip(k[0], k[1]))
	}
	return fill(b.String(), m.w, bg0)
}

// modelLabel is an agent row's model as shown: what an empty one means.
func (f *form) modelLabel(a agentRow) string {
	if a.model == "" {
		if auto := f.provider(a.provider).AutoModel; auto != "" {
			return auto
		}
	}
	return a.model
}
