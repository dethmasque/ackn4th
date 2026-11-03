package dash

import (
	"errors"
	"fmt"
	"strings"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/dethmasque/ackn4th/internal/role"
)

// The roles screen (R): every role, built-in and the user's own, with an
// editor to add or change one and an import from a file or URL. Roles are
// written in the dashboard itself, so no terminal editor is needed.

// Roles configures the roles screen; nil hides it.
type Roles struct {
	// List returns every role, and the user role files that couldn't be
	// read.
	List func() (entries []RoleEntry, invalid []string, err error)
	// Save writes a user role; without overwrite an existing one is
	// refused with role.ErrExists.
	Save func(r *role.Role, overwrite bool) error
	// Import reads a role file from a path or https URL and saves it, like
	// Save. It may be slow, so it runs off the UI.
	Import func(src string, overwrite bool) (*role.Role, error)
	// Remove deletes a user role (an edited built-in reverts).
	Remove func(name string) error
}

// RoleEntry is one role on the roles screen.
type RoleEntry struct {
	Role *role.Role
	// Kind: "built-in", "yours", or "yours, overrides built-in".
	Kind string
}

func (e RoleEntry) custom() bool { return e.Kind != "built-in" }

type rolesView struct {
	entries []RoleEntry
	invalid []string
	idx     int
	err     string
	remove  string // the role d would delete, until d confirms
	edit    *roleEditor
	imp     *roleImport
}

// Role editor fields, in tab order.
const (
	reName = iota
	reTitle
	reSummary
	reSkills
	reWritable
	rePrompt
	reCancel
	reSave
)

var roleFieldNames = []string{"Name", "Title", "Summary", "Skills", "Writable paths", "Instructions", "Cancel", "Save"}

type roleEditor struct {
	isNew   bool
	fields  [6]input
	focus   int
	err     string
	replace bool // ctrl+s again replaces an existing role
}

type roleImport struct {
	src     input
	busy    bool
	err     string
	replace bool
}

type roleImportedMsg struct {
	r   *role.Role
	err error
}

// openRoles shows the roles screen (the R key).
func (m *model) openRoles() {
	if m.opt.RoleAdmin == nil {
		return
	}
	m.roles = &rolesView{}
	m.reloadRoles("")
}

// reloadRoles rereads the roles, selecting name if given, and refreshes
// the new-task form's role list.
func (m *model) reloadRoles(name string) {
	v := m.roles
	entries, invalid, err := m.opt.RoleAdmin.List()
	if err != nil {
		v.err = err.Error()
		return
	}
	v.entries, v.invalid = entries, invalid
	if name != "" {
		for i, e := range entries {
			if e.Role.Name == name {
				v.idx = i
			}
		}
	}
	v.idx = min(v.idx, max(0, len(entries)-1))
	// The new-task form offers the same roles.
	m.opt.Roles = m.opt.Roles[:0]
	for _, e := range entries {
		m.opt.Roles = append(m.opt.Roles, Choice{Value: e.Role.Name, Label: e.Role.Name, Note: e.Role.Title})
	}
}

func (v *rolesView) selected() (RoleEntry, bool) {
	if v.idx < len(v.entries) {
		return v.entries[v.idx], true
	}
	return RoleEntry{}, false
}

func newRoleEditor(r *role.Role, isNew bool) *roleEditor {
	e := &roleEditor{isNew: isNew}
	e.fields[reName] = newInput(r.Name)
	e.fields[reTitle] = newInput(r.Title)
	e.fields[reSummary] = newInput(r.Summary)
	e.fields[reSkills] = newInput(strings.Join(r.Skills, ", "))
	e.fields[reWritable] = newInput(strings.Join(r.Writable, ", "))
	e.fields[rePrompt] = newInput(r.Prompt)
	e.fields[rePrompt].cur = 0 // instructions are read from the top
	if !isNew {
		e.focus = reTitle // a role's name is what identifies it
	}
	return e
}

func splitList(s string) []string {
	var out []string
	for _, f := range strings.Split(s, ",") {
		if f = strings.TrimSpace(f); f != "" {
			out = append(out, f)
		}
	}
	return out
}

func (e *roleEditor) role() *role.Role {
	f := func(i int) string { return strings.TrimSpace(e.fields[i].String()) }
	return &role.Role{
		Name: f(reName), Title: f(reTitle), Summary: f(reSummary),
		Skills: splitList(f(reSkills)), Writable: splitList(f(reWritable)), Prompt: f(rePrompt),
	}
}

// rolesKey handles keys on the roles screen.
func (m *model) rolesKey(k string) tea.Cmd {
	v := m.roles
	if k == "ctrl+c" {
		return tea.Quit
	}
	if v.edit != nil {
		m.roleEditKey(k)
		return nil
	}
	if v.imp != nil {
		return m.roleImportKey(k)
	}
	if k != "d" {
		v.remove = ""
	}
	switch k {
	case "esc", "q":
		m.roles = nil
	case "up", "k":
		if len(v.entries) > 0 {
			v.idx = (v.idx - 1 + len(v.entries)) % len(v.entries)
		}
	case "down", "j":
		if len(v.entries) > 0 {
			v.idx = (v.idx + 1) % len(v.entries)
		}
	case "a", "n":
		v.err = ""
		v.edit = newRoleEditor(role.Template("new_role"), true)
		v.edit.fields[reName] = newInput("")
	case "e", "enter":
		if e, ok := v.selected(); ok {
			v.err = ""
			v.edit = newRoleEditor(e.Role, false)
		}
	case "i":
		v.err = ""
		v.imp = &roleImport{}
	case "d", "x", "delete":
		e, ok := v.selected()
		switch {
		case !ok:
		case !e.custom():
			v.err = e.Role.Name + " is built in: edit it to make your own version, which you can remove later"
		case v.remove != e.Role.Name:
			v.remove = e.Role.Name
		default:
			v.remove = ""
			if err := m.opt.RoleAdmin.Remove(e.Role.Name); err != nil {
				v.err = err.Error()
				return nil
			}
			v.err = ""
			if e.Kind == "yours" {
				m.say("removed role " + e.Role.Name)
			} else {
				m.say("removed your edit: " + e.Role.Name + " is the built-in again")
			}
			m.reloadRoles(e.Role.Name)
		}
	}
	return nil
}

func (m *model) roleEditKey(k string) {
	v, e := m.roles, m.roles.edit
	if k != "ctrl+s" {
		e.replace = false
	}
	switch k {
	case "esc":
		v.edit = nil
		return
	case "tab", "down":
		if k == "tab" || e.focus != rePrompt {
			e.focus = (e.focus + 1) % (reSave + 1)
			if !e.isNew && e.focus == reName {
				e.focus = reTitle
			}
			return
		}
	case "shift+tab", "up":
		if k == "shift+tab" || e.focus != rePrompt {
			e.focus = (e.focus - 1 + reSave + 1) % (reSave + 1)
			if !e.isNew && e.focus == reName {
				e.focus = reSave
			}
			return
		}
	case "ctrl+s":
		m.saveRole()
		return
	case "enter", "space":
		switch e.focus {
		case reCancel:
			v.edit = nil
			return
		case reSave:
			m.saveRole()
			return
		}
		if k == "enter" && e.focus != rePrompt {
			e.focus++
			return
		}
	case "left", "right":
		if e.focus == reCancel || e.focus == reSave {
			e.focus = reCancel + reSave - e.focus
			return
		}
	}
	if e.focus < len(e.fields) && (e.isNew || e.focus != reName) {
		if e.fields[e.focus].edit(k, e.focus == rePrompt) {
			e.err = ""
		}
	}
}

func (m *model) saveRole() {
	v, e := m.roles, m.roles.edit
	r := e.role()
	if r.Title == "" && r.Name != "" {
		r.Title = role.Template(r.Name).Title
	}
	if err := role.Validate(r); err != nil {
		e.err = err.Error()
		return
	}
	err := m.opt.RoleAdmin.Save(r, !e.isNew || e.replace)
	if errors.Is(err, role.ErrExists) {
		e.err, e.replace = "you already have a role "+r.Name+": ctrl+s again replaces it", true
		return
	}
	if err != nil {
		e.err = err.Error()
		return
	}
	v.edit = nil
	m.say("saved role " + r.Name + " · it's in the new-task form's role list")
	m.reloadRoles(r.Name)
}

func (m *model) roleImportKey(k string) tea.Cmd {
	v, im := m.roles, m.roles.imp
	if im.busy {
		return nil
	}
	switch k {
	case "esc":
		v.imp = nil
		return nil
	case "enter", "ctrl+s":
		src := strings.TrimSpace(im.src.String())
		if src == "" {
			im.err = "type a file path or an https:// URL"
			return nil
		}
		im.busy, im.err = true, ""
		imp, overwrite := m.opt.RoleAdmin.Import, im.replace
		return func() tea.Msg {
			r, err := imp(src, overwrite)
			return roleImportedMsg{r, err}
		}
	}
	im.replace = false
	if im.src.edit(k, false) {
		im.err = ""
	}
	return nil
}

func (m *model) roleImported(msg roleImportedMsg) {
	if m.roles == nil || m.roles.imp == nil {
		return
	}
	im := m.roles.imp
	im.busy = false
	switch {
	case errors.Is(msg.err, role.ErrExists):
		im.err, im.replace = "you already have a role by that name: enter again replaces it", true
	case msg.err != nil:
		im.err = msg.err.Error()
	default:
		m.roles.imp = nil
		m.say("imported role " + msg.r.Name + " · read its instructions before using it")
		m.reloadRoles(msg.r.Name)
	}
}

// rolePaste inserts pasted text into the focused field.
func (m *model) rolePaste(s string) {
	v := m.roles
	s = strings.ReplaceAll(s, "\r\n", "\n")
	switch {
	case v.edit != nil && v.edit.focus < len(v.edit.fields) && (v.edit.isNew || v.edit.focus != reName):
		if v.edit.focus != rePrompt {
			s = strings.TrimSpace(strings.ReplaceAll(s, "\n", " "))
		}
		v.edit.fields[v.edit.focus].insert(s)
	case v.imp != nil && !v.imp.busy:
		v.imp.src.insert(strings.TrimSpace(s))
	}
}

// typingRole reports whether a roles-screen text field has focus.
func (v *rolesView) typingRole() (string, bool) {
	switch {
	case v == nil:
	case v.edit != nil && v.edit.focus < len(v.edit.fields):
		return fmt.Sprint("role.", v.edit.focus), true
	case v.imp != nil:
		return "role.import", true
	}
	return "", false
}

func (m *model) drawRoles(lines []string) string {
	v := m.roles
	w := min(104, m.w-6)
	inner := w - 4
	var out []string
	add := func(s ...string) {
		for _, l := range s {
			out = append(out, " "+l)
		}
	}
	title := "ROLES"
	switch {
	case v.edit != nil:
		title = m.drawRoleEditor(add, inner)
	case v.imp != nil:
		title = "IMPORT A ROLE"
		im := v.imp
		add("", pText("A role file (Markdown with YAML front matter) from a path or an https URL."),
			pDim("GitHub file pages work as they are. An imported role is instructions your agents will follow: read it first."), "",
			bold(pLime("FROM")), textField(im.src, inner, !im.busy), "")
		switch {
		case im.busy:
			add(pDim(m.spin(spinner) + " importing…"))
		case im.err != "":
			add(pRed("✗ " + fit(im.err, inner-2)))
		default:
			add("")
		}
		add("", pDim("⏎ imports · esc goes back"))
	default:
		m.drawRoleList(add, inner)
	}
	box := panel(title, "", out, w, len(out)+2, true, bg2)
	for i := range lines[:len(lines)-1] {
		lines[i] = onp(bg0)(fgp(backdropFg)(ansi.Strip(lines[i])))
	}
	h := len(out) + 2
	x, y := (m.w-w-2)/2, max(0, (m.h-1-h-1)/2)
	shadow(lines, x, y, w, h, m.w)
	overlayAt(lines, box, x, y, m.w)
	lines[len(lines)-1] = m.footer()
	return strings.Join(lines, "\n")
}

func (m *model) drawRoleList(add func(...string), inner int) {
	v := m.roles
	add("", pText("Each agent plays a role: its instructions, and the paths it may change."), "")
	rows := max(3, m.h-17)
	top := min(max(0, v.idx-rows/2), max(0, len(v.entries)-rows))
	nameW, kindW := 20, 26
	for i := top; i < min(len(v.entries), top+rows); i++ {
		e := v.entries[i]
		kind := pFaint(fit(e.Kind, kindW))
		if e.custom() {
			kind = pLime(fit(e.Kind, kindW))
		}
		row := pText(bold(fit(e.Role.Name, nameW))) + " " + pDim(fit(e.Role.Title, inner-nameW-kindW-2)) + " " + kind
		if i == v.idx {
			row = onp(mix(bg2, green, 0.22))(fill(row, inner, mix(bg2, green, 0.22)))
		}
		add(row)
	}
	add("")
	if e, ok := v.selected(); ok {
		writes := "read-only"
		if !e.Role.ReadOnly() {
			writes = "writes " + strings.Join(e.Role.Writable, ", ")
		}
		add(pDim(fit(e.Role.Summary, inner)), pFaint(fit(writes, inner)))
	}
	for _, bad := range v.invalid {
		add(pRed(fit("✗ skipped "+bad, inner)))
	}
	switch {
	case v.remove != "":
		add(pAmber(fit("press d again to delete your role "+v.remove, inner)))
	case v.err != "":
		add(pRed("✗ " + fit(v.err, inner-2)))
	default:
		add("")
	}
	add("", pDim("a adds · e or ⏎ edits · i imports · d removes yours · esc closes"))
}

func (m *model) drawRoleEditor(add func(...string), inner int) string {
	e := m.roles.edit
	title := "NEW ROLE"
	if !e.isNew {
		title = "EDIT ROLE " + strings.ToUpper(e.fields[reName].String())
	}
	labelW := 16
	row := func(i int, hint string) {
		label := bold(pLime(fit(strings.ToUpper(roleFieldNames[i]), labelW)))
		fw := inner - labelW
		if i == reName && !e.isNew {
			add(label + pText(e.fields[i].String()) + pFaint("  (a role's name can't change)"))
			return
		}
		in := e.fields[i]
		if e.focus != i {
			in.cur = 0 // show the start of a long value
		}
		add(label + textField(in, fw, e.focus == i))
		if hint != "" {
			add(strings.Repeat(" ", labelW) + pFaint(fit(hint, fw)))
		}
	}
	add("")
	row(reName, "lower_snake_case, e.g. game_designer; a built-in's name overrides it")
	row(reTitle, "")
	row(reSummary, "")
	row(reSkills, "comma-separated")
	row(reWritable, "globs, comma-separated, e.g. src/**, *.md · empty = read-only · ** = everything")
	add("", bold(pLime("INSTRUCTIONS"))+pDim("   what the agent should do and care about · enter adds a line"))
	pw := inner - 2
	prompt := e.fields[rePrompt]
	if e.focus != rePrompt {
		prompt.cur = 0
	}
	plines := editorLines(prompt, pw, e.focus == rePrompt)
	ph := max(3, min(12, m.h-28))
	if len(plines) > ph {
		at := cursorLine(prompt, pw)
		start := min(max(0, at-ph+1), len(plines)-ph)
		plines = plines[start : start+ph]
	}
	for len(plines) < ph {
		plines = append(plines, "")
	}
	edgeC := fgp(edge)
	if e.focus == rePrompt {
		edgeC = fgp(green)
	}
	add(edgeC("╭" + strings.Repeat("─", pw) + "╮"))
	for _, l := range plines {
		add(edgeC("│") + fill(l, pw, bg1) + edgeC("│"))
	}
	add(edgeC("╰" + strings.Repeat("─", pw) + "╯"))
	if e.err != "" {
		add(pRed("✗ " + fit(e.err, inner-2)))
	} else {
		add("")
	}
	cb, sb := button("CANCEL", e.focus == reCancel, bg2, false), button("SAVE", e.focus == reSave, bg2, true)
	pad := strings.Repeat(" ", max(0, inner-width(cb)-width(sb)-2))
	add(pad+cb+"  "+sb, pad+buttonShadow("CANCEL", bg2)+"  "+buttonShadow("SAVE", bg2))
	return title
}

// whereRoles describes the roles screen for the voice.
func (m *model) whereRoles() string {
	v := m.roles
	switch {
	case v.edit != nil:
		e := v.edit
		head := "Edit role " + e.fields[reName].String() + ". "
		if e.isNew {
			head = "New role. "
		}
		if e.err != "" {
			return "Error: " + e.err
		}
		switch e.focus {
		case reCancel, reSave:
			return head + roleFieldNames[e.focus] + ", button. Control s saves."
		}
		val := strings.TrimSpace(e.fields[e.focus].String())
		if val == "" {
			val = "empty"
		}
		return head + roleFieldNames[e.focus] + ", text field: " + val
	case v.imp != nil:
		if v.imp.err != "" {
			return "Error: " + v.imp.err
		}
		return "Import a role. Path or https URL, text field: " + cmpOr(strings.TrimSpace(v.imp.src.String()), "empty") + ". Enter imports."
	}
	if v.remove != "" {
		return "Press d again to delete your role " + v.remove + "."
	}
	if v.err != "" {
		return "Error: " + v.err
	}
	e, ok := v.selected()
	if !ok {
		return "Roles. None."
	}
	return fmt.Sprintf("Roles, %d of %d: %s, %s, %s. %s A adds, E edits, I imports, D removes yours, escape closes.",
		v.idx+1, len(v.entries), e.Role.Name, e.Role.Title, e.Kind, e.Role.Summary)
}
