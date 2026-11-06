package cli

import (
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/dethmasque/ackn4th/internal/auth"
	"github.com/dethmasque/ackn4th/internal/config"
	"github.com/dethmasque/ackn4th/internal/dash"
	"github.com/dethmasque/ackn4th/internal/spend"
	"github.com/dethmasque/ackn4th/internal/voice"
)

// settingsOffered records that the dashboard showed the configuration on
// a first run.
const settingsOffered = "settings.offered"

// efforts are the reasoning levels a provider setting can take.
var efforts = []string{"low", "medium", "high", "xhigh", "max"}

// dashSettings is the dashboard's configuration screen (C). Each change
// is written to config.yaml on its own, keeping the file's comments, and
// takes effect for tasks started from then on.
func (a *app) dashSettings(firstRun bool) *dash.Settings {
	return &dash.Settings{
		Load: func() ([]dash.Setting, string, error) {
			path, err := config.Path()
			return a.settings(), path, err
		},
		Save:  a.saveSetting,
		Offer: firstRun && !a.tips.Seen(settingsOffered),
		Done:  func() { a.tips.Mark(settingsOffered) },
	}
}

// settings lists the configuration's everyday settings, with their
// current values.
func (a *app) settings() []dash.Setting {
	c := a.cfg
	num := func(v float64) string {
		if v == 0 {
			return ""
		}
		return strconv.FormatFloat(v, 'f', -1, 64)
	}
	var provs []string
	for _, p := range auth.Providers {
		if _, ok := c.Providers[p]; ok {
			provs = append(provs, p)
		}
	}
	out := []dash.Setting{
		{Section: "General", Key: "default_provider", Label: "Default provider", Kind: dash.Pick, Value: c.DefaultProvider, Choices: provs,
			Help: "Powers every agent not given a provider. One provider and model for the whole team shares one cached context."},
		{Section: "General", Key: "prefer_cli", Label: "Prefer signed-in CLIs", Kind: dash.Toggle, Value: fmt.Sprint(c.PreferCLI),
			Help: "Runs the default provider through its local CLI on your plan when it is signed in: claude becomes claude-code, openai becomes codex."},
		{Section: "General", Key: "default_workspace_root", Label: "New workspaces go in", Kind: dash.Text, Value: c.DefaultWorkspaceRoot,
			Help: "Where scratch workspaces and cloned GitHub repositories are created."},

		{Section: "Limits", Key: "limits.max_rounds", Label: "Rounds per task", Kind: dash.Text, Value: fmt.Sprint(c.Limits.MaxRounds), Unit: "rounds", Empty: "until the agents agree",
			Help: "A new task's round limit, unless it sets its own. A round is one turn by every agent. 0 runs until every agent declares done."},
		{Section: "Limits", Key: "limits.max_steps_per_turn", Label: "Tool calls per turn", Kind: dash.Text, Value: fmt.Sprint(c.Limits.MaxStepsPerTurn), Unit: "tool calls",
			Help: "How many tool calls an agent may make in one turn before it is asked to hand off."},

		{Section: "Spending caps", Key: "spend.per_task", Label: "Each task", Kind: dash.Text, Value: num(c.Spend.PerTask), Unit: "dollars", Empty: "no cap",
			Help: "Caps one task's estimated spending over its whole life, unless it was started with its own cap. Billed providers only: claude, gemini, openai, deepseek."},
		{Section: "Spending caps", Key: "spend.daily", Label: "All providers, a day", Kind: dash.Text, Value: num(c.Spend.Daily), Unit: "dollars", Empty: "no cap",
			Help: "Caps what every task together spends in a day, on every billed provider. A task that reaches it stops, resumably. Each provider's own caps are in its section."},
		{Section: "Spending caps", Key: "spend.monthly", Label: "All providers, a month", Kind: dash.Text, Value: num(c.Spend.Monthly), Unit: "dollars", Empty: "no cap",
			Help: "Caps what every task together spends in a calendar month, on every billed provider."},
	}
	// Then a section per provider: its model, effort and, if it bills
	// per token, its own spending caps.
	for _, p := range provs {
		if p == "mock" {
			continue
		}
		pc, def := c.Providers[p], config.Default().Providers[p]
		section := p + " · " + providerNotes[p]
		empty := "the newest your key can use, picked at launch"
		if def.Model != "" {
			empty = def.Model
		}
		out = append(out, dash.Setting{Section: section, Key: "providers." + p + ".model", Label: "Model", Kind: dash.Text, Value: pc.Model, Empty: empty,
			Help: "The model " + p + " agents use unless the task names another. Empty goes back to the default (" + empty + ")."})
		if slices.Contains([]string{"claude", "claude-code", "gemini", "openai"}, p) {
			choices := efforts
			if def.Effort == "" {
				choices = append([]string{""}, efforts...)
			}
			out = append(out, dash.Setting{Section: section, Key: "providers." + p + ".effort", Label: "Effort", Kind: dash.Pick, Value: pc.Effort, Choices: choices, Empty: "the model's default",
				Help: "How hard " + p + " models think: higher is slower and costs more. Gemini takes xhigh and max as high."})
		}
		if spend.Billed[p] {
			sc := c.Spend.Providers[p]
			out = append(out,
				dash.Setting{Section: section, Key: "spend.providers." + p + ".daily", Label: "Spending cap, a day", Kind: dash.Text, Value: num(sc.Daily), Unit: "dollars", Empty: "no cap",
					Help: "Caps what every task spends on " + p + " in a day."},
				dash.Setting{Section: section, Key: "spend.providers." + p + ".monthly", Label: "Spending cap, a month", Kind: dash.Text, Value: num(sc.Monthly), Unit: "dollars", Empty: "no cap",
					Help: "Caps what every task spends on " + p + " in a calendar month."})
		}
	}
	out = append(out,
		dash.Setting{Section: "Commands", Key: "commands.enabled", Label: "Agents run commands", Kind: dash.Toggle, Value: fmt.Sprint(c.Commands.Enabled),
			Help: "Lets agents build, test and run code with run_command, confined by the OS sandbox to the workspace."},
		dash.Setting{Section: "Commands", Key: "commands.network", Label: "Commands reach the internet", Kind: dash.Toggle, Value: fmt.Sprint(c.Commands.Network),
			Help: "Lets agents' commands use the network, for package managers. Off, only localhost works. Turn on only for workspaces you trust."},
		dash.Setting{Section: "Commands", Key: "commands.timeout", Label: "Longest command", Kind: dash.Text, Value: c.Commands.Timeout.String(),
			Help: "How long one command may run before it is stopped, e.g. 10m or 90s."},

		dash.Setting{Section: "Dashboard", Key: "effects", Label: "Animations", Kind: dash.Toggle, Value: fmt.Sprint(c.Effects),
			Help: "Spinners and the header's animation, here and in command output."},
		dash.Setting{Section: "Dashboard", Key: "tips", Label: "Tips", Kind: dash.Toggle, Value: fmt.Sprint(c.Tips),
			Help: "First-run hints in commands and tooltips here. t replays the walkthrough either way."},
		dash.Setting{Section: "Dashboard", Key: "voice.enabled", Label: "Read aloud", Kind: dash.Toggle, Value: fmt.Sprint(c.Voice.Enabled),
			Help: "The dashboard reads itself aloud, on this computer. The first time downloads the voice, about 350 MB. v does the same."},
		dash.Setting{Section: "Dashboard", Key: "voice.name", Label: "Voice", Kind: dash.Pick, Value: or(c.Voice.Name, voice.DefaultVoice), Choices: voice.Voices,
			Help: "Who speaks: af and am voices are American, bf and bm British; f female, m male."},
		dash.Setting{Section: "Dashboard", Key: "voice.speed", Label: "Speaking rate", Kind: dash.Text, Value: num(float64(c.Voice.Speed)), Empty: "1, normal",
			Help: "1 is normal; from 0.5, slower, to 2, faster."},
	)
	return out
}

func or(s, def string) string {
	if s == "" {
		return def
	}
	return s
}

// saveSetting validates one setting's typed value, writes it to
// config.yaml and reloads the configuration. It returns the value as
// saved.
func (a *app) saveSetting(key, raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	var val any
	shown := raw
	switch {
	case key == "default_provider":
		if _, ok := a.cfg.Providers[raw]; !ok {
			return "", fmt.Errorf("%q isn't a provider", raw)
		}
		val = raw
	case key == "prefer_cli", key == "commands.enabled", key == "commands.network", key == "effects", key == "tips", key == "voice.enabled":
		b, err := strconv.ParseBool(raw)
		if err != nil {
			return "", fmt.Errorf("%q isn't on or off", raw)
		}
		val, shown = b, fmt.Sprint(b)
	case key == "default_workspace_root":
		if raw == "" {
			return "", fmt.Errorf("give a directory")
		}
		val = raw
	case key == "limits.max_rounds", key == "limits.max_steps_per_turn":
		n, err := strconv.Atoi(orZero(raw))
		low := 0
		if key == "limits.max_steps_per_turn" {
			low = 1
		}
		if err != nil || n < low {
			return "", fmt.Errorf("give a whole number, %d or more", low)
		}
		val, shown = n, fmt.Sprint(n)
	case strings.HasPrefix(key, "spend."):
		v, err := strconv.ParseFloat(orZero(strings.TrimPrefix(raw, "$")), 64)
		if err != nil || v < 0 {
			return "", fmt.Errorf("give an amount in dollars, like 5 or 2.50; empty or 0 means no cap")
		}
		val, shown = v, ""
		if v > 0 {
			shown = strconv.FormatFloat(v, 'f', -1, 64)
		}
	case strings.HasSuffix(key, ".effort"):
		p := strings.TrimSuffix(strings.TrimPrefix(key, "providers."), ".effort")
		if !slices.Contains(efforts, raw) && (raw != "" || config.Default().Providers[p].Effort != "") {
			return "", fmt.Errorf("effort is one of %s", strings.Join(efforts, ", "))
		}
		val = raw
	case strings.HasSuffix(key, ".model"):
		p := strings.TrimSuffix(strings.TrimPrefix(key, "providers."), ".model")
		if raw == "" {
			raw = config.Default().Providers[p].Model
		}
		if strings.ContainsAny(raw, " \t") {
			return "", fmt.Errorf("a model ID has no spaces")
		}
		val, shown = raw, raw
	case key == "commands.timeout":
		d, err := time.ParseDuration(raw)
		if err != nil || d <= 0 {
			return "", fmt.Errorf("give a duration, like 10m or 90s")
		}
		val, shown = d.String(), d.String()
	case key == "voice.name":
		if voice.SpeakerID(raw) < 0 {
			return "", fmt.Errorf("%q isn't a voice (ackn4th voice list)", raw)
		}
		val = raw
	case key == "voice.speed":
		v, err := strconv.ParseFloat(or(raw, "1"), 32)
		if err != nil || v < 0.5 || v > 2 {
			return "", fmt.Errorf("give a rate from 0.5 to 2")
		}
		val, shown = v, strconv.FormatFloat(v, 'f', -1, 32)
	default:
		return "", fmt.Errorf("%s can't be changed here", key)
	}
	old := a.cfg
	if _, err := config.SetKey(key, val); err != nil {
		return "", err
	}
	cfg, err := config.Load()
	if err != nil {
		return "", err
	}
	a.cfg = cfg
	if cfg.Effects != old.Effects {
		a.term.Effects = cfg.Effects
	}
	return shown, nil
}

func orZero(s string) string { return or(s, "0") }
