package dash

import "strings"

// fieldTips explain each control of the new-task form in a line: the form
// shows the focused one's under the agents.
var fieldTips = map[string]string{
	"prompt":    "what to do, in plain words; GitHub issue and PR links in it are read for the agents",
	"name":      "how the task is listed; also works in place of its ID (ackn4th show-task \"Name\")",
	"workspace": "the directory the agents work in, e.g. ~/code/app; they can't touch files outside it",
	"trust":     "agents only work in directories you trust; ticking this trusts this one from now on",
	"repo":      "owner/name or a github.com URL, e.g. acme/web; cloned with gh, used instead of the workspace",
	"branch":    "optional, e.g. fix/rate-limit: checked out if it exists locally or on origin, else created",
	"mode":      "turns: one agent at a time, each building on the last · parallel: all at once",
	"rounds":    "how many rounds the team gets; 0 runs until every agent agrees it's done",
	"cost":      "the most this task may spend, in dollars, estimated (claude, gemini, openai); empty uses the configured cap (C)",
	"pr":        "when the task finishes, push its branch and open a draft pull request (needs gh)",
	"role":      "what this agent does on the team; R on the dashboard adds or edits roles",
	"provider":  "the AI service this agent runs on; p on the dashboard shows which are signed in",
	"model":     "the provider's model; pick a suggestion, or “type a model id…” for any other",
	"effort":    "how hard the model thinks; higher is slower and costs more, default is the provider's",
	"remove":    "remove this agent (also x or ctrl+x on any of its fields)",
	"add":       "add another agent (also ctrl+n)",
	"cancel":    "close the form; nothing is saved",
	"launch":    "check the providers are signed in, then start the task in the background",
}

// formHelpRows are the new-task form's help panel: its fields, then keys.
var formHelpRows = [][2]string{
	{"PROMPT", "What the agents should do. Paste GitHub issue or PR\nlinks and the agents get their text (needs gh)."},
	{"NAME", "Optional; defaults to the prompt's first words. Use it\ninstead of the ID: ackn4th watch-task \"Rate limiting\"."},
	{"WORKSPACE", "A directory here, e.g. ~/code/app. Agents work only\ninside it; a new directory needs trusting once."},
	{"REPO", "A GitHub repository instead of a directory, as any of:\n  acme/web\n  https://github.com/acme/web\n  git@github.com:acme/web.git\nCloned with gh into default_workspace_root (see\nackn4th config), or reused if it's already there."},
	{"BRANCH", "Optional, e.g. fix/rate-limit: the local branch, else\nthe one on origin, else a new one from the current commit."},
	{"MODE", "turns: agents take turns, each seeing the last's work.\nparallel: all at once (needs two or more agents)."},
	{"ROUNDS", "How many rounds to run; 0 runs until all agents agree."},
	{"AGENTS", "Each is a role on a provider, model and effort. Agents\nwith the same provider, model and effort share a cache."},
	{"KEYS", "tab, shift+tab or arrows move · enter opens a list\nctrl+n adds an agent · x removes the selected one · ctrl+s launches\nf1 (or ? off a text field) shows this · esc closes the form"},
}

// formHelpBox is the new-task form's help panel, at most w cells wide and,
// short of the KEYS row and spacing (the footer lists the keys), h tall.
func formHelpBox(w, h int) string {
	rows, lines, end := formHelpRows, []string{""}, []string{"", pFaint(" press any key")}
	n := 0
	for _, r := range rows {
		n += strings.Count(r[1], "\n") + 1
	}
	if n+len(lines)+len(end)+2 > h {
		rows, lines, end = rows[:len(rows)-1], nil, end[1:]
	}
	for _, r := range rows {
		for i, l := range strings.Split(r[1], "\n") {
			key := strings.Repeat(" ", 11)
			if i == 0 {
				key = pill(fit(r[0], 9), bg3, lime)
			}
			lines = append(lines, " "+key+"  "+pText(l))
		}
	}
	lines = append(lines, end...)
	return panel("NEW TASK · HELP", "", lines, min(80, w-4), len(lines)+2, true, bg2)
}
