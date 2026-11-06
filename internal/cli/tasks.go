package cli

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/dethmasque/ackn4th/internal/config"
	"github.com/dethmasque/ackn4th/internal/event"
	"github.com/dethmasque/ackn4th/internal/github"
	"github.com/dethmasque/ackn4th/internal/provider/claudecode"
	"github.com/dethmasque/ackn4th/internal/provider/openai"
	"github.com/dethmasque/ackn4th/internal/task"
	"github.com/dethmasque/ackn4th/internal/ui"
	"github.com/dethmasque/ackn4th/internal/workspace"
)

// runFlags are shared by start-task and run-agent.
type runFlags struct {
	name       string
	prompt     string
	promptFile string
	workspace  string
	scratch    bool
	parallel   bool
	rounds     int
	forever    bool
	detach     bool
	repo       string
	branch     string
	draftPR    bool
	maxCost    float64
}

func (f *runFlags) register(cmd *cobra.Command) {
	cmd.Flags().StringVar(&f.name, "name", "", "a `name` for the task, shown everywhere and usable instead of its ID (default: asked for on a terminal, else from the prompt)")
	cmd.Flags().StringVarP(&f.prompt, "prompt", "p", "", "what to do, as `text` (or pass it as arguments)")
	cmd.Flags().StringVar(&f.promptFile, "prompt-file", "", "read the prompt from a `file` ('-' for stdin)")
	cmd.Flags().StringVarP(&f.workspace, "workspace", "w", "", "`dir`ectory the agents work in (default: the current directory)")
	cmd.Flags().BoolVar(&f.scratch, "scratch", false, "work in a new, empty directory under default_workspace_root")
	cmd.Flags().IntVarP(&f.rounds, "rounds", "r", -1, "at most `n` rounds; 0 runs until done (default: limits.max_rounds)")
	cmd.Flags().BoolVar(&f.forever, "forever", false, "same as --rounds 0: keep going until done")
	cmd.Flags().BoolVarP(&f.detach, "detach", "d", false, "run in the background (follow with watch-task, stop with stop-task)")
	cmd.Flags().StringVar(&f.repo, "repo", "", "work in a GitHub repository, as `owner/name` or its github.com URL: cloned with gh into default_workspace_root, or reused")
	cmd.Flags().StringVar(&f.branch, "branch", "", "git `branch` to work on: checked out, fetched from GitHub, or created from the current commit")
	cmd.Flags().Float64Var(&f.maxCost, "max-cost", 0, "cap the task's spending at `dollars`, estimated from token counts on billed providers (default: spend.per_task)")
	cmd.Flags().BoolVar(&f.draftPR, "draft-pr", false, "when the task completes, propose its changes as a draft GitHub pull request (needs gh)")
	rejectNegative(cmd, "rounds")
	rejectNegative(cmd, "max-cost")
}

// rejectNegative makes cmd fail when an int flag is set below zero.
func rejectNegative(cmd *cobra.Command, flag string) {
	prev := cmd.PreRunE
	cmd.PreRunE = func(c *cobra.Command, args []string) error {
		if n, err := c.Flags().GetInt(flag); err == nil && n < 0 && c.Flags().Changed(flag) {
			return fmt.Errorf("--%s can't be negative (0 means no limit)", flag)
		}
		if v, err := c.Flags().GetFloat64(flag); err == nil && v < 0 && c.Flags().Changed(flag) {
			return fmt.Errorf("--%s can't be negative", flag)
		}
		if prev != nil {
			return prev(c, args)
		}
		return nil
	}
}

func (f *runFlags) readPrompt(args []string) (string, error) {
	p := strings.TrimSpace(f.prompt)
	if p == "" && len(args) > 0 {
		p = strings.TrimSpace(strings.Join(args, " "))
	}
	if f.promptFile != "" {
		var data []byte
		var err error
		if f.promptFile == "-" {
			data, err = io.ReadAll(os.Stdin)
		} else {
			data, err = os.ReadFile(f.promptFile)
		}
		if err != nil {
			return "", err
		}
		p = strings.TrimSpace(strings.TrimSpace(p) + "\n\n" + string(data))
	}
	if p == "" {
		return "", errNoPrompt
	}
	return p, nil
}

var errNoPrompt = usageError{errors.New("no prompt: use --prompt, --prompt-file, or pass it as arguments")}

// parseList accepts "a,b", "a b", and JSON-ish `["a","b"]`, across one or
// more flag values.
func parseList(values []string) ([]string, error) {
	var out []string
	for _, v := range values {
		v = strings.Trim(strings.TrimSpace(v), "[]")
		for _, item := range strings.FieldsFunc(v, func(r rune) bool { return r == ',' || r == ' ' }) {
			item = strings.Trim(item, `"' `)
			if item != "" {
				out = append(out, strings.ReplaceAll(strings.ToLower(item), "-", "_"))
			}
		}
	}
	return out, nil
}

// agentReq is one requested team member, before its settings are resolved
// against the config.
type agentReq struct {
	role    string
	profile string
	set     config.AgentProfile // explicit settings; empty fields are unset
}

// parseAgent parses an --agent value: a role, then comma-separated
// key=value settings, e.g. "backend_dev,provider=claude,model=claude-sonnet-5-5".
func parseAgent(v string) (agentReq, error) {
	var r agentReq
	for i, field := range strings.Split(v, ",") {
		field = strings.TrimSpace(field)
		if field == "" {
			continue
		}
		k, val, ok := strings.Cut(field, "=")
		if !ok {
			if i == 0 {
				r.role = field
				continue
			}
			return r, fmt.Errorf("--agent %q: expected key=value, got %q", v, field)
		}
		k, val = strings.ToLower(strings.TrimSpace(k)), strings.TrimSpace(val)
		var err error
		switch strings.ReplaceAll(k, "-", "_") {
		case "role":
			r.role = val
		case "profile":
			r.profile = val
		case "provider":
			r.set.Provider = strings.ToLower(val)
		case "model":
			r.set.Model = val
		case "effort":
			r.set.Effort = strings.ToLower(val)
		case "max_tokens":
			r.set.MaxTokens, err = strconv.ParseInt(val, 10, 64)
		case "compact_at":
			r.set.CompactAt, err = parseTokens(val)
		default:
			return r, fmt.Errorf("--agent %q: unknown setting %q (role, profile, provider, model, effort, max_tokens, compact_at)", v, k)
		}
		if err != nil {
			return r, fmt.Errorf("--agent %q: bad %s: %w", v, k, err)
		}
	}
	r.role = strings.ReplaceAll(strings.ToLower(r.role), "-", "_")
	if r.role == "" {
		return r, fmt.Errorf("--agent %q: no role (start with the role name, e.g. backend_dev,model=...)", v)
	}
	return r, nil
}

// parseTokens accepts 200000, 200k or 1m.
func parseTokens(s string) (int64, error) {
	s = strings.ToLower(strings.TrimSpace(s))
	mult := int64(1)
	switch {
	case strings.HasSuffix(s, "k"):
		mult, s = 1000, strings.TrimSuffix(s, "k")
	case strings.HasSuffix(s, "m"):
		mult, s = 1000000, strings.TrimSuffix(s, "m")
	}
	n, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return 0, err
	}
	return int64(n * float64(mult)), nil
}

func startTaskCmd(a *app) *cobra.Command {
	var f runFlags
	var rolesFlag, useClaude, useGemini, agentFlags []string
	var defProvider string
	var mix bool
	cmd := &cobra.Command{
		Use:     "start-task [prompt]",
		GroupID: "tasks",
		Aliases: []string{"start"},
		Short:   "Put a team of agents on a task: in turns or in parallel, any providers and models",
		Long: `Start a task with a team of agents. By default they take turns in the order
given by --roles, each turn's closing message handed to the next agent; with
--parallel they all work each round at once. Every agent can have its own
provider, model and effort (see --agent), and one agent can play several
roles (backend_dev+qa_engineer). The task completes when every agent
declares done, or stops at the round limit.

Name the task with --name: lists, the dashboard and messages call it that,
and other commands accept the name in place of its ID. Without --name, a
terminal asks, offering a name drawn from the prompt; scripts get that name.

By default the whole team runs on one provider (default_provider in
config.yaml, claude out of the box). That's the fast, cheap setup: agents on
the same provider, model and effort share one cached copy of the team's
context (tools, rules, project brief) instead of each paying for its own.
Set the team's provider with --provider (claude, gemini, openai, deepseek, claude-code,
codex, cursor, copilot), one agent's with --agent, or Claude or Gemini per
role with --use-claude / --use-gemini; --mix alternates claude, gemini, ...
When no provider is named and its CLI is signed in, claude runs through
Claude Code and openai through Codex (prefer_cli in config.yaml).

With --parallel, every agent takes its turn of a round at the same time:
they split the work, message each other mid-turn, and can't overwrite a file
a teammate changed without reading it first. The task completes when every
agent declares done in the same round.

Configure any agent with --agent: the role, then comma-separated settings
(provider, model, effort, max_tokens, compact_at, or profile=<name> for an
agents: profile in config.yaml). --agent can also build the whole team on its
own, in the order given. Unset settings come from providers: in config.yaml.`,
		Example: `  ackn4th start-task --roles fullstack_dev,product_manager \
    --use-gemini fullstack_dev --use-claude product_manager \
    --prompt "Build a CLI todo app in Go with tests"

  ackn4th start-task --roles '["backend_dev","code_reviewer"]' -w ~/ackn4th-workspaces/api -d \
    "Add pagination to the /users endpoint"

  ackn4th start-task --name Settings --parallel --roles backend_dev,frontend_dev,qa_engineer \
    "Add a settings page backed by a /settings API, with tests"

  ackn4th start-task --provider codex --roles backend_dev,code_reviewer \
    "Add a /health endpoint with a test"                   # on your ChatGPT plan

  ackn4th start-task \
    --agent backend_dev,provider=claude,model=claude-opus-5-5,effort=xhigh \
    --agent code_reviewer,provider=gemini,effort=low,compact_at=150k \
    --agent tech_writer,profile=fast-writer \
    "Add a /healthz endpoint, review it, and document it"`,
		RunE: func(cmd *cobra.Command, args []string) error {
			roleNames, _ := parseList(rolesFlag)
			var custom []agentReq
			for _, v := range agentFlags {
				r, err := parseAgent(v)
				if err != nil {
					return err
				}
				custom = append(custom, r)
			}
			if len(roleNames) == 0 {
				for _, r := range custom {
					roleNames = append(roleNames, r.role)
				}
			}
			if len(roleNames) == 0 {
				var err error
				roleNames, err = a.chooseRoles(usageError{errors.New("--roles or --agent is required (see `ackn4th roles`)")}, "WHICH ROLES? (in turn order; a+b = one agent, both roles)", true)
				if err != nil {
					return err
				}
			}
			for _, r := range custom {
				if !slices.Contains(roleNames, r.role) {
					return fmt.Errorf("--agent %s: %q is not in --roles", r.role, r.role)
				}
			}
			claudeRoles, _ := parseList(useClaude)
			geminiRoles, _ := parseList(useGemini)
			for _, r := range append(slices.Clone(claudeRoles), geminiRoles...) {
				if !slices.Contains(roleNames, r) {
					return fmt.Errorf("%q is assigned a provider but is not in --roles", r)
				}
			}
			var reqs []agentReq
			for i, r := range roleNames {
				req := agentReq{role: r}
				// With --agent building the team, each entry configures its
				// own member, so a role can appear twice with different models.
				if len(rolesFlag) == 0 && len(custom) > 0 {
					req = custom[i]
				} else if j := slices.IndexFunc(custom, func(c agentReq) bool { return c.role == r }); j >= 0 {
					req = custom[j]
				}
				switch {
				case slices.Contains(claudeRoles, r) && slices.Contains(geminiRoles, r):
					return fmt.Errorf("%q is assigned to both claude and gemini", r)
				case slices.Contains(claudeRoles, r):
					req.set.Provider = cmp.Or(req.set.Provider, "claude")
				case slices.Contains(geminiRoles, r):
					req.set.Provider = cmp.Or(req.set.Provider, "gemini")
				}
				if req.set.Provider == "" && req.profile == "" {
					if defProvider != "" {
						req.set.Provider = strings.ToLower(defProvider)
					} else {
						// Not named: a signed-in CLI wins over the API.
						req.set.Provider = a.preferCLI(cmp.Or(a.cfg.DefaultProvider, "claude"))
						if mix {
							req.set.Provider = a.preferCLI([]string{"claude", "gemini"}[i%2])
						}
					}
				}
				reqs = append(reqs, req)
			}
			return a.launch(cmd.Context(), &f, args, reqs)
		},
	}
	f.register(cmd)
	cmd.Flags().StringArrayVar(&rolesFlag, "roles", nil, "team `roles` in turn order, e.g. backend_dev,code_reviewer")
	cmd.Flags().StringArrayVar(&useClaude, "use-claude", nil, "`roles` powered by Claude")
	cmd.Flags().StringArrayVar(&useGemini, "use-gemini", nil, "`roles` powered by Gemini")
	cmd.Flags().BoolVar(&f.parallel, "parallel", false, "agents take each round's turns at the same time")
	cmd.Flags().StringVar(&defProvider, "provider", "", "`provider` for every role not assigned one (default: default_provider in config.yaml)")
	cmd.Flags().BoolVar(&mix, "mix", false, "alternate claude, gemini, ... for roles not assigned a provider")
	cmd.MarkFlagsMutuallyExclusive("mix", "provider")
	cmd.Flags().StringArrayVar(&agentFlags, "agent", nil, "configure an agent: `role,key=value,...` with provider, model, effort, max_tokens, compact_at, profile (repeatable)")
	return cmd
}

func runAgentCmd(a *app) *cobra.Command {
	var f runFlags
	var roleName, prov, model, effort, profile, compactAt string
	cmd := &cobra.Command{
		Use:     "run-agent [prompt]",
		GroupID: "tasks",
		Aliases: []string{"solo"},
		Short:   "Run one agent, in one or more roles, iterating until it declares done",
		Long: `Run one agent on its own. Each round it continues the task where it left
off; it finishes when it calls declare_done, or stops at the round limit.

Give it one role, or several for one agent to cover at once
(--role backend_dev,qa_engineer): it gets every role's guidance and may write
wherever any of them may. Use ` + "`ackn4th roles`" + ` to pick. Unset model settings
come from the --profile, then from providers: in config.yaml.`,
		Example: `  ackn4th run-agent --role tech_writer --provider gemini -w . "Document every CLI flag in README.md"
  ackn4th run-agent --role security_engineer --rounds 3 -p "Audit the auth package"
  ackn4th run-agent --role backend_dev --model claude-sonnet-5-5 --effort medium "Fix the flaky test"
  ackn4th run-agent --role backend_dev,qa_engineer "Add pagination, with tests"`,
		RunE: func(cmd *cobra.Command, args []string) error {
			if roleName == "" {
				names, err := a.chooseRoles(usageError{errors.New("--role is required (see `ackn4th roles`)")}, "WHICH ROLES? (one agent plays all you pick)", true)
				if err != nil {
					return err
				}
				roleName = strings.Join(names, "+")
			}
			// Several roles make one agent that plays them all.
			if names, _ := parseList([]string{roleName}); len(names) > 1 {
				roleName = strings.Join(names, "+")
			}
			req := agentReq{role: roleName, profile: profile, set: config.AgentProfile{
				Provider: strings.ToLower(prov), Model: model, Effort: strings.ToLower(effort),
			}}
			if compactAt != "" {
				n, err := parseTokens(compactAt)
				if err != nil {
					return fmt.Errorf("--compact-at: %w", err)
				}
				req.set.CompactAt = n
			}
			if req.set.Provider == "" && profile == "" {
				req.set.Provider = a.preferCLI(cmp.Or(a.cfg.DefaultProvider, "claude"))
			}
			return a.launch(cmd.Context(), &f, args, []agentReq{req})
		},
	}
	f.register(cmd)
	cmd.Flags().StringVar(&roleName, "role", "", "the agent's `role`, or several for one agent: backend_dev,qa_engineer (see ackn4th roles)")
	cmd.Flags().StringVar(&prov, "provider", "", "`provider`: claude, gemini, openai, deepseek, claude-code, codex, cursor, copilot (default: default_provider in config.yaml)")
	cmd.Flags().StringVar(&model, "model", "", "`model` ID, e.g. claude-sonnet-5-5 (default: providers.<provider>.model)")
	cmd.Flags().StringVar(&effort, "effort", "", "reasoning `level`: low, medium, high, xhigh, max")
	cmd.Flags().StringVar(&profile, "profile", "", "agent `profile` from agents: in config.yaml")
	cmd.Flags().StringVar(&compactAt, "compact-at", "", "compact the conversation past this many input `tokens` (e.g. 300k)")
	return cmd
}

// chooseRoles asks which roles to use, in the order typed.
func (a *app) chooseRoles(err error, title string, multi bool) ([]string, error) {
	reg, rerr := a.roles()
	if rerr != nil || !a.interactive() {
		return nil, err
	}
	var opts []option
	for _, r := range reg.All() {
		opts = append(opts, option{r.Name, ui.Truncate(r.Summary, 50)})
	}
	return a.choose(err, title, opts, multi)
}

// launch validates a new task, creates it, and runs it.
func (a *app) launch(ctx context.Context, f *runFlags, args []string, reqs []agentReq) error {
	prompt, err := f.readPrompt(args)
	if errors.Is(err, errNoPrompt) {
		prompt, err = a.ask(err, "what should the agents do?", nil)
	}
	if err != nil {
		return err
	}
	name, err := a.taskName(f.name, prompt)
	if err != nil {
		return err
	}
	agents, provs, err := a.resolveAgents(reqs)
	if err != nil {
		return err
	}
	id := task.NewID()
	dir, err := a.taskWorkspace(ctx, f, id)
	if err != nil {
		return err
	}
	if err := a.useBranch(ctx, dir, f.branch, false); err != nil {
		return err
	}
	if f.draftPR {
		if err := github.CheckPR(ctx, dir); err != nil {
			return fmt.Errorf("--draft-pr: %w", err)
		}
	}
	if err := a.requireSignedIn(provs...); err != nil {
		return err
	}
	if err := a.preflight(ctx, agents, true); err != nil {
		return err
	}

	rounds := f.rounds
	switch {
	case f.forever:
		rounds = 0
	case rounds < 0:
		rounds = a.cfg.Limits.MaxRounds
	}
	tk := &task.Task{ID: id, Name: name, Prompt: prompt, Workspace: dir, Agents: agents, MaxRounds: rounds, MaxCost: f.maxCost, DraftPR: f.draftPR, Branch: f.branch}
	if f.parallel {
		if len(agents) < 2 {
			return errors.New("--parallel needs at least two agents")
		}
		tk.Mode = task.ModeParallel
	}
	if err := a.store.Create(tk); err != nil {
		return err
	}
	if err := a.fetchGitHub(ctx, tk, false); err != nil {
		a.store.Delete(tk.ID)
		return err
	}
	a.term.Banner()
	header(a.term, tk)
	if g := tk.CacheGroups(); len(g) > 1 {
		a.term.Println(a.term.Gray(fmt.Sprintf("  tip: this team spans %d provider/model/effort groups, so each group caches the shared context separately. One provider for the whole team (the default) is faster and cheaper.", len(g))))
	}
	if f.detach {
		return a.detach(tk)
	}
	return a.runForeground(ctx, tk.ID)
}

// taskName is the new task's name: --name, else asked for on a terminal
// (enter takes a suggestion drawn from the prompt), else that suggestion.
func (a *app) taskName(flag, prompt string) (string, error) {
	if flag != "" {
		name, err := task.CleanName(flag)
		if err != nil {
			return "", fmt.Errorf("--name: %w", err)
		}
		return name, nil
	}
	def := task.DefaultName(prompt)
	if !a.interactive() {
		return def, nil
	}
	t := a.term
	for {
		fmt.Fprint(os.Stdout, t.Bold("  name this task")+t.Gray(" ("+def+")")+t.Bold(": "))
		line, err := a.stdin().ReadString('\n')
		if strings.TrimSpace(line) == "" {
			if err != nil && line == "" {
				fmt.Fprintln(os.Stdout)
			}
			return def, nil
		}
		name, cerr := task.CleanName(line)
		if cerr == nil {
			return name, nil
		}
		t.Println(t.Red("  ✗ "), cerr.Error())
	}
}

// resolveAgents turns requests into complete agent specs, and lists the
// providers they use.
func (a *app) resolveAgents(reqs []agentReq) ([]task.AgentSpec, []string, error) {
	roles, err := a.roles()
	if err != nil {
		return nil, nil, err
	}
	var provs []string
	agents := make([]task.AgentSpec, len(reqs))
	for i, req := range reqs {
		r, err := roles.Get(req.role)
		if err != nil {
			return nil, nil, err
		}
		if req.set.Provider != "" && !validProvider(req.set.Provider) {
			return nil, nil, fmt.Errorf("unknown provider %q (supported: %s)", req.set.Provider, providerNames())
		}
		p, err := a.cfg.ResolveAgent(req.profile, req.set)
		if errors.Is(err, config.ErrNoModel) && p.Provider == openai.Name {
			if err = a.pickOpenAIModel(context.Background()); err == nil {
				p, err = a.cfg.ResolveAgent(req.profile, req.set)
			}
		}
		if err != nil {
			return nil, nil, fmt.Errorf("%s: %w", r.Name, err)
		}
		agents[i] = task.AgentSpec{
			Role: r.Name, Provider: p.Provider, Model: p.Model, Profile: req.profile,
			Effort: p.Effort, MaxTokens: p.MaxTokens, CompactAt: p.CompactAt,
		}
		if !slices.Contains(provs, p.Provider) {
			provs = append(provs, p.Provider)
		}
	}
	return agents, provs, nil
}

// taskWorkspace picks and checks the directory a new task works in: -w,
// a fresh directory with --scratch, or the current directory. A directory
// outside the trusted roots needs the operator's OK first.
func (a *app) taskWorkspace(ctx context.Context, f *runFlags, id string) (string, error) {
	allowed, err := a.cfg.AllowedRoots()
	if err != nil {
		return "", err
	}
	switch {
	case (f.scratch && f.workspace != "") || (f.repo != "" && (f.scratch || f.workspace != "")):
		return "", errors.New("use one of --workspace, --scratch and --repo")
	case f.scratch && f.branch != "":
		return "", errors.New("--branch needs a git workspace: use it with --repo or --workspace, not --scratch")
	case f.repo != "":
		return a.repoWorkspace(ctx, f.repo, allowed, false)
	case f.scratch:
		return workspace.Resolve(filepath.Join(allowed[0], id), allowed, true)
	}
	dir := cmp.Or(f.workspace, ".")
	resolved, err := workspace.Resolve(dir, allowed, true)
	if errors.Is(err, workspace.ErrUntrusted) {
		if err := a.askTrust(dir); err != nil {
			return "", err
		}
		allowed, _ = a.cfg.AllowedRoots()
		resolved, err = workspace.Resolve(dir, allowed, true)
	}
	return resolved, err
}

// preflight checks every distinct provider and model once before a task
// starts, so a typo in a model name fails now rather than mid-task. With
// show, a spinner runs while it checks.
func (a *app) preflight(ctx context.Context, agents []task.AgentSpec, show bool) error {
	t := a.term
	checked := map[string]bool{}
	for _, ag := range agents {
		k := ag.Provider + "/" + ag.Model
		if checked[k] {
			continue
		}
		checked[k] = true
		p, err := a.providerFor(ag)
		if err != nil {
			return err
		}
		spin := t.NewPingPong()
		if show {
			spin.Start("ackn4th", ag.Provider, "checking "+ag.Model)
		}
		vctx, cancel := context.WithTimeout(ctx, 30*time.Second)
		err = p.Verify(vctx)
		cancel()
		if show {
			spin.Stop()
		}
		if err != nil {
			return fmt.Errorf("model %s: %w", ag.Model, err)
		}
	}
	return nil
}

// detach hands the task to a background runner.
func (a *app) detach(tk *task.Task) error {
	t := a.term
	pid, err := a.store.Spawn(tk.ID)
	if err != nil {
		return err
	}
	t.Println()
	t.Decode(fmt.Sprintf("  ▓▒░  %s (%s) RUNNING IN THE BACKGROUND (pid %d)", strings.ToUpper(tk.Title()), tk.ID, pid), func(s string) string { return t.Bold(t.Lime(s)) })
	t.Println()
	t.Println(t.Gray("  explore "), t.Lime("ackn4th dash "+tk.ID))
	t.Println(t.Gray("  watch   "), t.Lime("ackn4th watch-task "+tk.ID))
	t.Println(t.Gray("  stop    "), t.Lime("ackn4th stop-task "+tk.ID))
	t.Println(t.Gray("  list    "), t.Lime("ackn4th list-tasks"))
	a.tip("detached", "closing this terminal never stops a background task. `ackn4th dash` shows every task and lets you stop or resume them.")
	return nil
}

// runForeground runs a task in this process with live rendering. Ctrl-C
// stops it cleanly; it can be resumed later.
func (a *app) runForeground(ctx context.Context, id string) error {
	r, err := a.runner()
	if err != nil {
		return err
	}
	tk, err := a.store.Load(id)
	if err != nil {
		return err
	}
	rend := newRenderer(a.term, tk)
	defer rend.Close()
	r.Sink = rend.Handle

	ctx, stop := task.StopContext(ctx)
	defer stop()
	finished := make(chan struct{})
	defer close(finished) // runs before stop, so no notice after a normal end
	go func() {
		select {
		case <-ctx.Done():
			rend.Handle(event.Event{Kind: event.Notice, Text: "stopping after the current request is cancelled…"})
		case <-finished:
		}
	}()

	started := time.Now()
	final, err := r.Run(ctx, id, false)
	rend.Close()
	if final != nil {
		outcome(a.term, final, started, a.summaryFile(final.ID))
		a.tip("first-task", "look back over every turn, file change and handoff with `ackn4th dash "+final.ID+"`; `ackn4th resume-task "+final.ID+"` continues a stopped task.")
	}
	if final != nil && final.Status == task.Failed {
		return errors.New("task failed")
	}
	return err
}

func stopTaskCmd(a *app) *cobra.Command {
	var wait time.Duration
	cmd := &cobra.Command{
		Use:     "stop-task <task>",
		GroupID: "tasks",
		Short:   "Stop a running task (it can be resumed later)",
		Long: `Stop a running task, in the foreground or background. The runner cancels
its in-flight request and saves state; nothing done so far is lost, and
` + "`ackn4th resume-task <task>`" + ` continues from the same point.`,
		Example: `  ackn4th stop-task k3f9qz
  ackn4th stop-task "Rate limiting" # by name
  ackn4th stop-task k3f            # or the start of an ID
  ackn4th stop-task k3f9qz --wait 1m`,
		Args: upTo(1, "<task>"),
		RunE: func(cmd *cobra.Command, args []string) error {
			t := a.term
			tk, err := a.taskArg(args, func(tk *task.Task) bool { return tk.Status == task.Running })
			if err != nil {
				return err
			}
			a.store.Reconcile(tk)
			if tk.Status != task.Running {
				t.Println(t.Gray("task "+tk.Label()+" is not running: "), statusLabel(t, tk.Status))
				return nil
			}
			if err := task.Signal(tk.PID); err != nil {
				return fmt.Errorf("signal runner (pid %d): %w", tk.PID, err)
			}
			spin := t.NewPingPong()
			spin.Start("ackn4th", tk.ID, "stopping")
			deadline := time.Now().Add(wait)
			for a.store.Locked(tk.ID) && time.Now().Before(deadline) {
				time.Sleep(200 * time.Millisecond)
			}
			spin.Stop()
			if a.store.Locked(tk.ID) {
				return fmt.Errorf("runner (pid %d) has not stopped after %s", tk.PID, wait)
			}
			tk, _ = a.store.Load(tk.ID)
			t.Println(t.Green("✓ "), "task ", t.Bold(tk.Title()), t.Gray(" ("+tk.ID+") "), statusLabel(t, tk.Status), t.Gray("  · resume with "), t.Lime("ackn4th resume-task "+tk.ID))
			return nil
		},
	}
	cmd.Flags().DurationVar(&wait, "wait", 30*time.Second, "how long to wait for the runner to exit, e.g. 1m")
	return cmd
}

func resumeTaskCmd(a *app) *cobra.Command {
	var rounds int
	var maxCost float64
	var detach bool
	var note, prov string
	cmd := &cobra.Command{
		Use:     "resume-task <task>",
		GroupID: "tasks",
		Aliases: []string{"resume"},
		Short:   "Resume a stopped or failed task, or attach to a running one",
		Long: `Resume a stopped or failed task where it left off. If the task is already
running in the background, attach to its live output instead.

--rounds adds rounds beyond the current one (useful after hitting the round
limit); --max-cost lets it spend that many dollars more (after hitting its
spending cap); --note passes guidance from you to the next agent.

--provider moves every agent to another provider, e.g. from claude (API
credits) to claude-code (your Claude plan, through your local Claude
Code). Moved agents take the provider's configured effort (claude-code:
medium, to stretch your plan's usage limits). A conversation can't move
between providers, so they start fresh; the workspace, the project notes
and the task's history carry over.`,
		Example: `  ackn4th resume-task k3f9qz
  ackn4th resume-task k3f9qz --rounds 5 --note "Focus on the failing edge cases" -d
  ackn4th resume-task k3f9qz --provider claude-code`,
		Args: upTo(1, "<task>"),
		RunE: func(cmd *cobra.Command, args []string) error {
			tk, err := a.taskArg(args, func(tk *task.Task) bool { return tk.Status != task.Completed })
			if err != nil {
				return err
			}
			a.store.Reconcile(tk)
			if tk.Status == task.Running {
				a.term.Println(a.term.Gray("task " + tk.Label() + " is already running; attaching (Ctrl-C detaches, the task keeps running)"))
				return a.watch(cmd.Context(), tk.ID, true)
			}
			if !tk.Resumable() {
				return fmt.Errorf("task %s is %s and cannot be resumed", tk.Label(), tk.Status)
			}
			moves, err := a.planSwitch(tk, strings.ToLower(prov))
			if err != nil {
				return err
			}
			if err := a.requireSignedIn(taskProviders(tk, moves)...); err != nil {
				return err
			}
			if err := a.applySwitch(tk, moves); err != nil {
				return err
			}
			if len(moves) > 0 {
				a.term.Println(a.term.Green("✓ "), fmt.Sprintf("moved %d agent(s) to ", len(moves)), a.term.ProviderColor(moves[0].provider, moves[0].provider),
					a.term.Gray(" · they start fresh conversations; the workspace and notes carry over"))
			}
			if cmd.Flags().Changed("rounds") {
				if rounds == 0 {
					tk.MaxRounds = 0
				} else {
					tk.MaxRounds = tk.Round - 1 + rounds
				}
			} else if tk.MaxRounds > 0 && tk.Round > tk.MaxRounds {
				return fmt.Errorf("task %s used all %d rounds: pass --rounds N to continue", tk.Label(), tk.MaxRounds)
			}
			if cmd.Flags().Changed("max-cost") {
				tk.MaxCost = tk.Cost() + maxCost
			}
			if note != "" {
				tk.Note = note
			}
			if err := a.store.Save(tk); err != nil {
				return err
			}
			a.term.Banner()
			header(a.term, tk)
			if detach {
				return a.detach(tk)
			}
			return a.runForeground(cmd.Context(), tk.ID)
		},
	}
	cmd.Flags().IntVarP(&rounds, "rounds", "r", 0, "`n` more rounds to allow from here; 0 runs until consensus")
	rejectNegative(cmd, "rounds")
	cmd.Flags().Float64Var(&maxCost, "max-cost", 0, "let the task spend up to `dollars` more, past its spending cap (estimated; billed providers only)")
	rejectNegative(cmd, "max-cost")
	cmd.Flags().BoolVarP(&detach, "detach", "d", false, "resume in the background")
	cmd.Flags().StringVarP(&note, "note", "n", "", "guidance `text` for the next agent")
	cmd.Flags().StringVar(&prov, "provider", "", "move every agent to this `provider` first, e.g. claude-code")
	return cmd
}

func followUpCmd(a *app) *cobra.Command {
	var rounds int
	var maxCost float64
	var detach bool
	cmd := &cobra.Command{
		Use:     "follow-up <task> [prompt]",
		GroupID: "tasks",
		Aliases: []string{"followup"},
		Short:   "Ask a finished task's team for more work",
		Long: `Give a task that isn't running a new request, and run it again. The same
agents continue with their conversations, the workspace and the task's
history: every agent gets the follow-up at the start of its next turn, and
every agent has to declare done again.

It works on completed, stopped and failed tasks. Without --rounds, the
follow-up gets as many rounds as the task's last run allowed (or runs until
the agents agree, if that had no limit).`,
		Example: `  ackn4th follow-up kjbk3z "Also update the Windows notes in CONTRIBUTING.md"
  ackn4th follow-up kjbk3z "Address the review comments in REVIEW.md" --rounds 2 -d`,
		Args: upTo(2, "<task> [prompt]"),
		RunE: func(cmd *cobra.Command, args []string) error {
			tk, err := a.taskArg(args, func(tk *task.Task) bool { return tk.Status != task.Running })
			if err != nil {
				return err
			}
			a.store.Reconcile(tk)
			prompt := ""
			if len(args) > 1 {
				prompt = args[1]
			}
			if strings.TrimSpace(prompt) == "" {
				prompt, err = a.ask(usageError{errors.New("missing the follow-up prompt")}, "what should the agents do next?", nil)
				if err != nil {
					return err
				}
			}
			if err := a.requireSignedIn(taskProviders(tk, nil)...); err != nil {
				return err
			}
			n := -1
			if cmd.Flags().Changed("rounds") {
				n = rounds
			}
			if err := tk.AddFollowUp(prompt, n, time.Now()); err != nil {
				return err
			}
			if cmd.Flags().Changed("max-cost") {
				tk.MaxCost = tk.Cost() + maxCost
			}
			if err := a.store.Save(tk); err != nil {
				return err
			}
			a.term.Banner()
			header(a.term, tk)
			if detach {
				return a.detach(tk)
			}
			return a.runForeground(cmd.Context(), tk.ID)
		},
	}
	cmd.Flags().IntVarP(&rounds, "rounds", "r", 0, "`n` rounds for the follow-up; 0 runs until the agents agree")
	rejectNegative(cmd, "rounds")
	cmd.Flags().Float64Var(&maxCost, "max-cost", 0, "let the task spend up to `dollars` more, past its spending cap (estimated; billed providers only)")
	rejectNegative(cmd, "max-cost")
	cmd.Flags().BoolVarP(&detach, "detach", "d", false, "run it in the background")
	return cmd
}

// summaryFile is the task's summary.md, if a run has written one.
func (a *app) summaryFile(id string) string {
	p := a.store.SummaryPath(id)
	if _, err := os.Stat(p); err != nil {
		return ""
	}
	return tildeHome(p)
}

// move is one agent changing provider.
type move struct {
	agent                   int
	provider, model, effort string
}

// planSwitch lists the agents of tk to move to prov: every agent on
// another provider. Claude and claude-code share model IDs, so those
// keep their model; others get prov's configured one. Every moved agent
// takes prov's configured effort, e.g. claude-code's medium, which
// stretches a plan's usage limits. An empty prov moves nobody.
func (a *app) planSwitch(tk *task.Task, prov string) ([]move, error) {
	if prov == "" {
		return nil, nil
	}
	if !validProvider(prov) {
		return nil, fmt.Errorf("unknown provider %q (supported: %s)", prov, providerNames())
	}
	claudes := []string{"claude", claudecode.Name}
	var out []move
	for i, ag := range tk.Agents {
		if ag.Provider == prov {
			continue
		}
		model := a.cfg.Providers[prov].Model
		if slices.Contains(claudes, ag.Provider) && slices.Contains(claudes, prov) {
			model = ag.Model
		}
		out = append(out, move{i, prov, model, a.cfg.Providers[prov].Effort})
	}
	return out, nil
}

// taskProviders lists the providers tk's agents use once moves apply.
func taskProviders(tk *task.Task, moves []move) []string {
	var provs []string
	for i, ag := range tk.Agents {
		p := ag.Provider
		for _, m := range moves {
			if m.agent == i {
				p = m.provider
			}
		}
		provs = append(provs, p)
	}
	return provs
}

// applySwitch moves the agents and saves the task.
func (a *app) applySwitch(tk *task.Task, moves []move) error {
	if len(moves) == 0 {
		return nil
	}
	for _, m := range moves {
		if err := a.store.Switch(tk, m.agent, m.provider, m.model, m.effort); err != nil {
			return err
		}
	}
	a.store.AppendEvent(tk.ID, event.Event{Kind: event.Notice, Time: time.Now(), Round: tk.Round,
		Text: fmt.Sprintf("moved %d agent(s) to %s; they start fresh conversations (workspace and notes carry over)", len(moves), moves[0].provider)})
	return a.store.Save(tk)
}

func listTasksCmd(a *app) *cobra.Command {
	var all bool
	cmd := &cobra.Command{
		Use:     "list-tasks",
		GroupID: "tasks",
		Aliases: []string{"tasks", "ls"},
		Short:   "List tasks",
		Long: `List tasks, most recently updated first: the 20 latest, or every one with
--all. Any command that takes a task accepts its name, its ID, or a unique
start of its ID.`,
		Example: `  ackn4th list-tasks
  ackn4th ls --all`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			t := a.term
			tasks, err := a.store.List()
			if err != nil {
				return err
			}
			if len(tasks) == 0 {
				t.Println(t.Gray("no tasks yet: try "), t.Lime("ackn4th start-task --help"))
				return nil
			}
			var rows [][]string
			for i, tk := range tasks {
				if !all && i >= 20 {
					break
				}
				team := make([]string, len(tk.Agents))
				for j, ag := range tk.Agents {
					team[j] = t.ProviderColor(ag.Provider, ag.Role)
				}
				round := fmt.Sprint(tk.Round)
				if tk.MaxRounds > 0 {
					round = fmt.Sprintf("%d/%d", min(tk.Round, tk.MaxRounds), tk.MaxRounds)
				}
				u := tk.Usage()
				bg := ""
				if tk.Status == task.Running && tk.Background {
					bg = t.Gray(" bg")
				}
				rows = append(rows, []string{
					t.Bold(ui.Truncate(tk.Title(), 32)), t.Gray(tk.ID), statusLabel(t, tk.Status) + bg, strings.Join(team, t.Moss(" ⇄ ")), round,
					ui.Tokens(u.InputTokens + u.OutputTokens), costCell(tk.Cost()), ui.Ago(tk.UpdatedAt),
				})
			}
			t.Table([]string{"NAME", "ID", "STATUS", "TEAM", "ROUND", "TOKENS", "COST", "UPDATED"}, rows)
			if !all && len(tasks) > 20 {
				t.Println(t.Gray(fmt.Sprintf("  … %d more (use --all)", len(tasks)-20)))
			}
			a.tip("list-tasks", "Name a task or give its ID (a unique start of it works): `ackn4th show-task "+tasks[0].ID[:3]+"` details a task, `ackn4th watch-task` follows a running one.")
			return nil
		},
	}
	cmd.Flags().BoolVarP(&all, "all", "a", false, "show every task")
	return cmd
}

func showTaskCmd(a *app) *cobra.Command {
	return &cobra.Command{
		Use:     "show-task <task>",
		GroupID: "tasks",
		Short:   "Show a task and replay its transcript",
		Example: `  ackn4th show-task k3f9qz
  ackn4th show-task k3f9qz --no-fx | less -R`,
		Args: upTo(1, "<task>"),
		RunE: func(cmd *cobra.Command, args []string) error {
			tk, err := a.taskArg(args, nil)
			if err != nil {
				return err
			}
			a.store.Reconcile(tk)
			return a.watch(cmd.Context(), tk.ID, false)
		},
	}
}

func watchTaskCmd(a *app) *cobra.Command {
	return &cobra.Command{
		Use:     "watch-task <task>",
		GroupID: "tasks",
		Aliases: []string{"watch", "attach"},
		Short:   "Follow a task's live progress (Ctrl-C detaches; the task keeps running)",
		Example: `  ackn4th start-task -d --roles backend_dev,qa_engineer "Add rate limiting"
  ackn4th watch-task k3f9qz`,
		Args: upTo(1, "<task>"),
		RunE: func(cmd *cobra.Command, args []string) error {
			tk, err := a.taskArg(args, nil)
			if err != nil {
				return err
			}
			return a.watch(cmd.Context(), tk.ID, true)
		},
	}
}

// watch replays a task's events and, with follow set, keeps tailing them
// until the task stops running.
func (a *app) watch(ctx context.Context, id string, follow bool) error {
	tk, err := a.store.Load(id)
	if err != nil {
		return err
	}
	t := a.term
	header(t, tk)
	rend := newRenderer(t, tk)
	defer rend.Close()

	ctx, stop := signal.NotifyContext(ctx, os.Interrupt)
	defer stop()
	var off int64
	for {
		events, next, err := a.store.ReadEvents(id, off)
		if err != nil {
			return err
		}
		off = next
		for _, e := range events {
			rend.Handle(e)
		}
		tk, err = a.store.Load(id)
		if err != nil {
			return err
		}
		a.store.Reconcile(tk)
		if !follow || tk.Status != task.Running {
			rend.Close()
			outcome(t, tk, time.Time{}, a.summaryFile(tk.ID))
			return nil
		}
		select {
		case <-ctx.Done():
			rend.Close()
			t.Println()
			t.Println(t.Gray("detached; task " + id + " keeps running"))
			return nil
		case <-time.After(300 * time.Millisecond):
		}
	}
}

func renameTaskCmd(a *app) *cobra.Command {
	return &cobra.Command{
		Use:     "rename-task <task> [name]",
		GroupID: "tasks",
		Aliases: []string{"rename"},
		Short:   "Give a task a new name",
		Long: `Give a task a new name: what list-tasks, the dashboard and every message
call it, and what other commands accept in place of its ID. Names needn't
be unique, but a name shared by several tasks can't stand in for an ID.
It works while the task runs. Without a name, on a terminal, it asks.`,
		Example: `  ackn4th rename-task k3f9qz "Rate limiting"
  ackn4th rename-task "Rate limiting" "API rate limits"`,
		Args: func(cmd *cobra.Command, args []string) error {
			if len(args) > 2 {
				return usageError{fmt.Errorf("expected <task> and a name, got %d arguments: quote a name with spaces", len(args))}
			}
			return nil
		},
		RunE: func(cmd *cobra.Command, args []string) error {
			tk, err := a.taskArg(args, nil)
			if err != nil {
				return err
			}
			var name string
			if len(args) == 2 {
				name = args[1]
			} else {
				name, err = a.ask(usageError{errors.New("missing the new name")}, "new name for "+tk.Label(), func(v string) error {
					_, err := task.CleanName(v)
					return err
				})
				if err != nil {
					return err
				}
			}
			old := tk.Title()
			if err := a.store.Rename(tk, name); err != nil {
				return err
			}
			a.term.Println(a.term.Green("✓ "), old, a.term.Gray(" → "), a.term.Bold(tk.Name), a.term.Gray(" ("+tk.ID+")"))
			return nil
		},
	}
}

func deleteTaskCmd(a *app) *cobra.Command {
	var yes bool
	cmd := &cobra.Command{
		Use:     "delete-task <task>",
		GroupID: "tasks",
		Short:   "Delete a task's records (the workspace is left untouched)",
		Example: `  ackn4th delete-task k3f9qz --yes`,
		Args:    upTo(1, "<task>"),
		RunE: func(cmd *cobra.Command, args []string) error {
			tk, err := a.taskArg(args, nil)
			if err != nil {
				return err
			}
			if !yes {
				err := fmt.Errorf("this deletes task %s's transcript and agent state; re-run with --yes to confirm", tk.Label())
				if err := a.confirm(err, "delete task "+tk.Label()+"?"); err != nil {
					return err
				}
			}
			if err := a.store.Delete(tk.ID); err != nil {
				return err
			}
			a.term.Println(a.term.Green("✓ "), "deleted task ", tk.Label(), a.term.Gray("  (workspace kept: "+tk.Workspace+")"))
			return nil
		},
	}
	cmd.Flags().BoolVarP(&yes, "yes", "y", false, "confirm deletion")
	return cmd
}

// internalRunCmd is the entry point of a detached background runner.
func internalRunCmd(a *app) *cobra.Command {
	return &cobra.Command{
		Use:    "__run <task>",
		Hidden: true,
		Args:   args(1, "<task>"),
		RunE: func(cmd *cobra.Command, args []string) error {
			r, err := a.runner()
			if err != nil {
				return err
			}
			r.Sink = func(e event.Event) {
				if !e.Ephemeral() {
					fmt.Printf("%s %-14s %s %s\n", e.Time.Format(time.RFC3339), e.Kind, e.Role, ui.Truncate(e.Text, 120))
				}
			}
			ctx, stop := task.StopContext(cmd.Context())
			defer stop()
			_, err = r.Run(ctx, args[0], true)
			return err
		},
	}
}
