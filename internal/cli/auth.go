package cli

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"golang.org/x/term"

	"github.com/dethmasque/ackn4th/internal/auth"
	"github.com/dethmasque/ackn4th/internal/config"
	"github.com/dethmasque/ackn4th/internal/provider"
	"github.com/dethmasque/ackn4th/internal/provider/claudecode"
	"github.com/dethmasque/ackn4th/internal/provider/openai"
	"github.com/dethmasque/ackn4th/internal/task"
	"github.com/dethmasque/ackn4th/internal/ui"
)

func loginCmd(a *app) *cobra.Command {
	var providers []string
	var reauth bool
	var gcpProject string
	var pause bool
	cmd := &cobra.Command{
		Use:     "login",
		GroupID: "session",
		Short:   "Sign in and start a session (24h at most)",
		Long: `Sign in to each provider and start a session. With no --provider, on a
terminal, it lists every provider and asks which to sign in to.

Agent CLIs (claude-code, codex, cursor, copilot) use their own login, and
ackn4th stores nothing for them: signing in runs the CLI's login if it isn't
signed in already. They run on your Claude, ChatGPT, Cursor or Copilot plan.

Claude signs in with OAuth through ` + "`ant auth login`" + ` (needs an Anthropic Console
account; a claude.ai subscription doesn't cover API use), into a config dir
private to ackn4th, so your everyday ant login is never touched. Requests use
short-lived access tokens that refresh automatically.

OpenAI uses an API key (` + auth.OpenAIKeyURL + `), pasted when asked
or piped on stdin, and stored like the Gemini key.

Gemini, by default, uses an API key from Google AI Studio
(` + auth.GeminiKeyURL + `): sign in with your Google account, create a
key, paste it when asked. No Google Cloud project needed, and there's a free
tier. The key is stored only in your OS keychain, never in a file, flag or
environment variable, and is usable only during a session.
If you use Google Cloud, --gcp-project switches Gemini to Vertex AI with
short-lived OAuth through ` + "`gcloud`" + ` instead.

The session lasts session_ttl, capped at 24h, and is checked before every
model request. When it expires, running tasks stop (resumably), the session
is deleted, and OAuth grants are revoked, so the next login starts with a
fresh sign-in. ` + "`ackn4th logout`" + ` also deletes the API keys.`,
		Example: `  ackn4th login                                              # pick from a list
  ackn4th login --provider codex --provider claude-code      # your ChatGPT and Claude plans
  ackn4th login --provider claude
  op read op://dev/openai/key | ackn4th login --provider openai   # a key from stdin
  ackn4th login --provider gemini --reauth                   # paste a new AI Studio key
  ackn4th login --provider gemini --gcp-project my-project   # Vertex AI instead (saved to config.yaml)`,
		RunE: func(cmd *cobra.Command, args []string) error {
			if !pause {
				return login(cmd, a, providers, reauth, gcpProject)
			}
			// Started from the dashboard, which redraws the moment this
			// exits: keep the outcome on screen until the user has read it.
			err := login(cmd, a, providers, reauth, gcpProject)
			if err != nil {
				printError(a.term, cmd, err)
				err = errReported
			}
			if a.interactive() {
				a.term.Println()
				a.term.Println(a.term.Gray("  press enter to return to the dashboard"))
				a.stdin().ReadString('\n')
			}
			return err
		},
	}
	cmd.Flags().StringArrayVar(&providers, "provider", nil, "`provider` to sign in to (repeatable): claude, gemini, openai, claude-code, codex, cursor, copilot (default: ask, or all off a terminal)")
	cmd.Flags().BoolVar(&reauth, "reauth", false, "sign in again (or replace an API key) even if already authorized")
	cmd.Flags().StringVar(&gcpProject, "gcp-project", "", "use Gemini on Vertex AI in this GCP `project` instead of an AI Studio key (saved to config.yaml)")
	cmd.Flags().BoolVar(&pause, "pause", false, "wait for enter before exiting (used by the dashboard)")
	cmd.Flags().MarkHidden("pause")
	return cmd
}

// login signs in to providers and starts a session.
func login(cmd *cobra.Command, a *app, providers []string, reauth bool, gcpProject string) error {
	t := a.term
	t.Banner()
	a.bannerShown = true
	sel, _ := parseList(providers)
	for i, p := range sel {
		sel[i] = strings.ReplaceAll(p, "_", "-") // parseList spells roles; providers use hyphens
	}
	if len(sel) == 0 && a.interactive() {
		var err error
		sel, err = a.choose(nil, "SIGN IN TO", []option{
			{"claude", "Anthropic · OAuth via the ant CLI"},
			{"gemini", "Google · AI Studio key (or Vertex AI)"},
			{"claude-code", "your Claude plan · via your local Claude Code"},
			{"openai", "OpenAI · API key"},
			{"codex", "your ChatGPT plan · via the Codex CLI"},
			{"cursor", "your Cursor plan · via the Cursor CLI"},
			{"copilot", "your Copilot plan · via the Copilot CLI"},
		}, true)
		if err != nil {
			return err
		}
		if len(sel) == 0 {
			return errors.New("no provider chosen")
		}
	}
	if len(sel) == 0 {
		// Non-interactive: renew the session for every provider that is
		// already signed in. Nobody is there for a browser or a key.
		sel = signedIn()
		if len(sel) == 0 {
			return errors.New("no provider is signed in yet: sign in once on a terminal, or name one with --provider (API keys can be piped on stdin)")
		}
	}
	for _, p := range sel {
		if !auth.ValidProvider(p) {
			return fmt.Errorf("unknown provider %q (supported: %s)", p, strings.Join(auth.Providers, ", "))
		}
	}
	if gcpProject != "" {
		if err := saveGCPProject(a, gcpProject); err != nil {
			return err
		}
	}
	// With prefer_cli, defaults may resolve to a provider's CLI, so a
	// signed-in CLI joins the session alongside its API provider.
	if a.cfg.PreferCLI {
		for _, p := range slices.Clone(sel) {
			if cli, ok := cliProviders[p]; ok && !slices.Contains(sel, cli) && cliSignedIn(cli) {
				sel = append(sel, cli)
			}
		}
	}
	var verified []string
	for _, p := range sel {
		if err := loginProvider(cmd.Context(), a, p, reauth); err != nil {
			t.Println(" ", t.Red("✗"), " ", t.ProviderColor(p, fmt.Sprintf("%-11s", p)), " ", inline(t, err.Error()))
			continue
		}
		verified = append(verified, p)
	}
	// Providers verified earlier in a still-live session stay usable.
	if prev, err := auth.Require(); err == nil {
		for _, p := range prev.Providers {
			if !slices.Contains(verified, p) && !slices.Contains(sel, p) {
				verified = append(verified, p)
			}
		}
	}
	if len(verified) == 0 {
		return errors.New("no provider authorized; nothing to log in with")
	}
	name := "unknown"
	if u, err := user.Current(); err == nil {
		name = u.Username
	}
	s, err := auth.StartSession(name, verified, a.cfg.SessionTTL)
	if err != nil {
		return fmt.Errorf("save session to keychain: %w", err)
	}
	t.Println()
	t.Rain(6, 900*time.Millisecond)
	t.Decode("  ▓▒░  ACCESS GRANTED  ░▒▓", func(s string) string { return t.Bold(t.Lime(s)) })
	t.Println()
	sessionBox(a, s)
	grantsBox(cmd.Context(), a)
	a.tip("logged-in", "you're signed in for "+strings.TrimSuffix(strings.TrimSuffix(a.cfg.SessionTTL.String(), "0s"), "0m")+". `cd` into a project and run `ackn4th` for the dashboard, or `ackn4th start-task \"Add a /health endpoint\"` straight from the shell.")
	return nil
}

// signedIn lists the providers that can join a session without asking
// anything: a stored grant or key, or a signed-in CLI.
func signedIn() []string {
	var out []string
	for _, p := range auth.Providers {
		ok := false
		switch p {
		case "claude":
			ok = auth.HasGrant(p)
		case "gemini":
			ok = auth.HasGeminiKey() || auth.HasGrant(p)
		case "openai":
			ok = auth.HasOpenAIKey()
		default:
			ok = cliSignedIn(p)
		}
		if ok {
			out = append(out, p)
		}
	}
	return out
}

// missingTool reports a sign-in CLI that p needs but that isn't installed.
// Gemini with an AI Studio key needs none.
func (a *app) missingTool(p string) error {
	if p == "gemini" && a.cfg.Providers["gemini"].GeminiAuth() == config.GeminiKey {
		return nil
	}
	tool, ok := auth.Tools[p]
	if !ok {
		return nil
	}
	if d := auth.CLIDriver(p); d != nil {
		// The CLI may live outside PATH (e.g. inside the ChatGPT app).
		if _, err := d.Bin(); err != nil {
			return fmt.Errorf("%s needs the `%s` CLI: %s", p, tool.Name, tool.Install)
		}
		return nil
	}
	if _, err := exec.LookPath(tool.Name); err != nil {
		return fmt.Errorf("%s sign-in needs the `%s` CLI: %s", p, tool.Name, tool.Install)
	}
	return nil
}

func saveGCPProject(a *app, project string) error {
	g := a.cfg.Providers["gemini"]
	g.Project, g.Auth = project, config.GeminiVertex
	a.cfg.Providers["gemini"] = g
	path, err := config.Save(a.cfg)
	if err != nil {
		return err
	}
	a.term.Println(a.term.Green("✓ "), "providers.gemini.project = ", a.term.Lime(project), a.term.Gray("  ("+path+")"))
	return nil
}

// loginProvider makes sure p can be used: it runs the sign-in (or asks
// for the Gemini key) when needed, then verifies with a real API call.
func loginProvider(ctx context.Context, a *app, p string, reauth bool) error {
	t := a.term
	gc := a.cfg.Providers["gemini"]
	how := "OAuth grant verified"
	switch {
	case p == "gemini" && gc.GeminiAuth() == config.GeminiKey:
		if reauth || !auth.HasGeminiKey() {
			if err := askGeminiKey(a); err != nil {
				return err
			}
		}
		how = "API key verified (" + auth.SecretStore() + ")"
	case p == "gemini" && gc.GeminiAuth() == config.GeminiVertex && gc.Project == "":
		project, err := a.ask(errors.New("a GCP project is needed for Gemini on Vertex AI: re-run with `--gcp-project <id>`, or set providers.gemini.auth: key"), "GCP project ID", nil)
		if err != nil {
			return err
		}
		if err := saveGCPProject(a, project); err != nil {
			return err
		}
		return loginProvider(ctx, a, p, reauth)
	case p == "gemini" && gc.GeminiAuth() != config.GeminiVertex:
		return fmt.Errorf("providers.gemini.auth: %q is not key or vertex", gc.Auth)
	case p == "openai":
		if reauth || !auth.HasOpenAIKey() {
			if err := askOpenAIKey(a); err != nil {
				return err
			}
		}
		how = "API key verified (" + auth.SecretStore() + "; rate-limit aware)"
	case p == claudecode.Name:
		if err := a.missingTool(p); err != nil {
			return err
		}
		if reauth || !auth.HasGrant(p) {
			t.Println(" ", t.Gray("·"), " ", t.ProviderColor(p, fmt.Sprintf("%-11s", p)), " ", t.Gray("signing in to Claude Code via "), t.Lime("claude auth login"))
			if err := auth.Login(ctx, p, "", os.Stdin, os.Stderr); err != nil {
				return err
			}
		}
		how = "Claude Code signed in (its login; ackn4th stores nothing)"
	case auth.CLIDriver(p) != nil:
		d := auth.CLIDriver(p)
		bin, err := d.Bin()
		if err != nil {
			return fmt.Errorf("%s isn't installed: %s", p, auth.Tools[p].Install)
		}
		if reauth || !auth.HasGrant(p) {
			t.Println(" ", t.Gray("·"), " ", t.ProviderColor(p, fmt.Sprintf("%-11s", p)), " ", t.Gray("signing in via "), t.Lime(strings.Join(d.Login(filepath.Base(bin)), " ")))
			if err := auth.Login(ctx, p, "", os.Stdin, os.Stderr); err != nil {
				return err
			}
		}
		how = p + " signed in (its own login; ackn4th stores nothing) · runs in ackn4th's sandbox"
	default:
		tool := auth.Tools[p]
		if err := a.missingTool(p); err != nil {
			return err
		}
		if reauth || !auth.HasGrant(p) {
			t.Println(" ", t.Gray("·"), " ", t.ProviderColor(p, fmt.Sprintf("%-11s", p)), " ", t.Gray("opening browser sign-in via "), t.Lime(tool.Name))
			if err := auth.Login(ctx, p, gc.Project, os.Stdin, os.Stderr); err != nil {
				return err
			}
		}
	}
	prov, err := a.newProvider(p)
	if err != nil {
		return err
	}
	if op, ok := prov.(*openai.Provider); ok {
		if prov, err = a.chooseOpenAIModel(ctx, op); err != nil {
			return err
		}
	}
	spin := t.NewPingPong()
	spin.Start("ackn4th", p, "verifying grant")
	vctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	err = prov.Verify(vctx)
	cancel()
	spin.Stop()
	if err != nil {
		return err
	}
	t.Println(" ", t.Green("✓"), " ", t.ProviderColor(p, fmt.Sprintf("%-11s", p)), " ", t.Gray(how+" · model "+prov.Model()))
	return nil
}

// chooseOpenAIModel keeps the configured OpenAI model if it works with
// this key; otherwise it picks the newest one that does and saves it.
func (a *app) chooseOpenAIModel(ctx context.Context, op *openai.Provider) (provider.Provider, error) {
	t := a.term
	spin := t.NewPingPong()
	spin.Start("ackn4th", openai.Name, "checking "+cmp.Or(op.Model(), "models"))
	vctx, cancel := context.WithTimeout(ctx, 90*time.Second)
	defer cancel()
	if op.Verify(vctx) == nil {
		spin.Stop()
		return op, nil
	}
	m, err := op.PickModel(vctx)
	spin.Stop()
	if err != nil {
		return nil, err
	}
	if _, err := config.SetKey("providers.openai.model", m); err != nil {
		return nil, err
	}
	why := "the newest your key can use"
	if was := op.Model(); was != "" {
		why = was + " doesn't work with this key"
	}
	t.Println(" ", t.Green("✓"), " ", t.ProviderColor(openai.Name, fmt.Sprintf("%-11s", openai.Name)), " ", t.Gray("model "+m+" ("+why+"; saved to config)"))
	pc := a.cfg.Providers[openai.Name]
	pc.Model = m
	a.cfg.Providers[openai.Name] = pc
	return a.newProvider(openai.Name)
}

// askGeminiKey reads an AI Studio key without echoing it and stores it in
// the OS keychain. Keys are never accepted as flags or environment
// variables, which leak into shell history and child processes.
func askGeminiKey(a *app) error {
	t := a.term
	return askKey(a, "gemini", "GEMINI API KEY", []string{
		"1. Open " + t.Lime(auth.GeminiKeyURL) + " and sign in with Google",
		"2. Create API key " + t.Gray("(free tier; no Cloud setup needed)"),
		"3. Paste it below " + t.Gray("(hidden; stored only in your "+auth.SecretStore()+")"),
	}, auth.SaveGeminiKey)
}

// askOpenAIKey is askGeminiKey for OpenAI.
func askOpenAIKey(a *app) error {
	t := a.term
	return askKey(a, "openai", "OPENAI API KEY", []string{
		"1. Open " + t.Lime(auth.OpenAIKeyURL) + " and sign in",
		"2. Create new secret key " + t.Gray("(a project key with model access is enough)"),
		"3. Paste it below " + t.Gray("(hidden; stored only in your "+auth.SecretStore()+")"),
		t.Gray("A ChatGPT plan doesn't include API use: the key's project needs billing."),
	}, auth.SaveOpenAIKey)
}

// askKey reads an API key without echoing it, or from piped stdin, and
// saves it.
func askKey(a *app, prov, title string, steps []string, save func(string) error) error {
	t := a.term
	fd := int(os.Stdin.Fd())
	if term.IsTerminal(fd) && !a.interactive() {
		return fmt.Errorf("no %s API key stored: run `ackn4th login --provider %s` interactively, or pipe the key in", prov, prov)
	}
	t.Box(title, steps)
	var key string
	if term.IsTerminal(fd) {
		fmt.Fprint(os.Stderr, "  key: ")
		b, err := term.ReadPassword(fd)
		fmt.Fprintln(os.Stderr)
		if err != nil {
			return err
		}
		key = string(b)
	} else {
		// Piped, e.g. from a password manager.
		line, err := a.stdin().ReadString('\n')
		if err != nil && line == "" {
			return fmt.Errorf("no %s API key given on stdin", prov)
		}
		key = line
	}
	if strings.TrimSpace(key) == "" {
		return errors.New("no key entered")
	}
	return save(key)
}

func sessionBox(a *app, s *auth.Session) {
	t := a.term
	left := s.Remaining()
	frac := float64(left) / float64(s.ExpiresAt.Sub(s.CreatedAt))
	var provs []string
	for _, p := range auth.Providers {
		mark := t.Gray("· " + p)
		if s.Has(p) {
			mark = t.ProviderColor(p, "● "+p)
		}
		provs = append(provs, mark)
	}
	t.Box("SESSION", t.KV(
		[2]string{"operator", t.Lime(s.User)},
		[2]string{"providers", strings.Join(provs, "  ")},
		[2]string{"expires", s.ExpiresAt.Local().Format("Mon 15:04 MST") + t.Gray("  · then grants are revoked")},
		[2]string{"remaining", t.Gauge(frac, 24) + " " + ui.Duration(left.Round(time.Minute))},
	))
}

// grantsBox shows each provider's OAuth grant and access-token lifetime.
func grantsBox(ctx context.Context, a *app) {
	t := a.term
	var lines []string
	for _, p := range auth.Providers {
		name := t.ProviderColor(p, fmt.Sprintf("%-11s", p))
		gctx, cancel := context.WithTimeout(ctx, 15*time.Second)
		g, err := auth.CurrentGrant(gctx, p)
		cancel()
		if err != nil {
			lines = append(lines, t.Gray("✗ ")+name+" "+inline(t, strings.TrimPrefix(err.Error(), p+": ")))
			continue
		}
		token := t.Gray("token lifetime unknown")
		if g.External {
			token = t.Green("signed in ") + t.Gray("· "+g.Source+"; ackn4th stores nothing")
		} else if g.APIKey {
			token = t.Green("API key ") + t.Gray("in "+g.Source+" · usable only during a session")
		} else if !g.TokenExpiry.IsZero() {
			if left := time.Until(g.TokenExpiry); left > 0 {
				token = t.Green("access token ") + t.Gray("expires in "+ui.Duration(left.Round(time.Minute))+", auto-refreshes")
			} else {
				token = t.Amber("access token expired") + t.Gray(" · refreshes on next request")
			}
		}
		lines = append(lines, t.Green("✓ ")+name+" "+token)
		if g.Account != "" {
			lines = append(lines, "              "+t.Gray(g.Account))
		}
	}
	t.Box("CREDENTIALS", lines)
}

func logoutCmd(a *app) *cobra.Command {
	var only []string
	cmd := &cobra.Command{
		Use:     "logout",
		GroupID: "session",
		Short:   "Sign out of every provider, or just some, and delete what ackn4th holds",
		Long: `Without --provider, end the session and remove every credential ackn4th
holds: the Gemini and OpenAI API keys are deleted from the OS keychain,
Gemini's Vertex AI refresh token (if any) is revoked with Google, and
Claude's tokens are deleted (its refresh token also expires server-side).

With --provider, do that for the named providers only: they leave the
session, and the rest stay signed in.

Your everyday ant and gcloud logins are not affected, and agent CLIs
(Claude Code, Codex, Cursor, Copilot) stay signed in to their own logins:
ackn4th only stops using them. The API keys themselves stay valid at Google
and OpenAI; delete them there if you're done with them.`,
		Example: `  ackn4th logout
  ackn4th logout --provider gemini
  ackn4th logout --provider openai --provider codex`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			t := a.term
			for _, p := range only {
				if !auth.ValidProvider(p) {
					return fmt.Errorf("unknown provider %q: one of %s", p, strings.Join(auth.Providers, ", "))
				}
			}
			before, _ := auth.Current()
			provs := only
			if len(provs) == 0 {
				if err := auth.EndSession(); err != nil {
					return err
				}
				t.Println(t.Green("✓ "), "session ended")
				provs = auth.Providers
			}
			for _, p := range provs {
				if len(only) > 0 {
					if err := auth.DropProvider(p); err != nil {
						return err
					}
				}
				forgetProvider(a, p)
			}
			if len(only) > 0 && before != nil {
				if s, err := auth.Current(); err == nil && s.Remaining() > 0 {
					t.Println(t.Gray("· still signed in: " + strings.Join(s.Providers, ", ")))
				} else if err == nil || errors.Is(err, auth.ErrNoSession) {
					t.Println(t.Green("✓ "), "session ended (no providers left)")
				}
			}
			return nil
		},
	}
	cmd.Flags().StringSliceVar(&only, "provider", nil, "sign out of just this `provider` (repeatable): claude, gemini, openai, claude-code, codex, cursor, copilot")
	return cmd
}

// forgetProvider deletes what ackn4th holds for p and says what happened.
func forgetProvider(a *app, p string) {
	t := a.term
	name := t.ProviderColor(p, p)
	switch {
	case p == claudecode.Name:
		t.Println(t.Green("✓ "), name, t.Gray(" no longer used · Claude Code stays signed in (`claude auth logout` signs it out)"))
		return
	case auth.CLIDriver(p) != nil:
		t.Println(t.Green("✓ "), name, t.Gray(" no longer used · "+p+" stays signed in to its own login"))
		return
	}
	revoked, keyErr := forget(p)
	if revoked != nil {
		t.Println(t.Amber("! "), revoked.Error())
	} else if p != "openai" {
		t.Println(t.Green("✓ "), name, " grant revoked")
	}
	switch {
	case keyErr != nil:
		t.Println(t.Amber("! "), keyErr.Error())
	case p == "gemini" || p == "openai":
		t.Println(t.Green("✓ "), name, " API key deleted from "+auth.SecretStore())
	}
}

// forget revokes p's grant and deletes its stored API key, if any.
func forget(p string) (revokeErr, keyErr error) {
	revokeErr = auth.Revoke(p)
	switch p {
	case "gemini":
		keyErr = auth.ForgetGeminiKey()
	case "openai":
		keyErr = auth.ForgetOpenAIKey()
	}
	return revokeErr, keyErr
}

// signOut takes providers out of the session and deletes what ackn4th
// holds for them, quietly: the dashboard's sign-out.
func signOut(providers []string) error {
	var errs []error
	for _, p := range providers {
		if err := auth.DropProvider(p); err != nil {
			return err
		}
		r, k := forget(p)
		errs = append(errs, r, k)
	}
	return errors.Join(errs...)
}

func statusCmd(a *app) *cobra.Command {
	return &cobra.Command{
		Use:     "status",
		GroupID: "session",
		Short:   "Show the session, OAuth grants, workspace roots and task counts",
		RunE: func(cmd *cobra.Command, args []string) error {
			t := a.term
			t.Banner()
			s, err := auth.Current()
			switch {
			case errors.Is(err, auth.ErrNoSession):
				t.Box("SESSION", []string{t.Amber("not logged in") + t.Gray("  run ") + t.Lime("ackn4th login")})
				a.tip("status-login", "`ackn4th login` asks which provider to sign in to. No API credits? Pick claude-code to use your Claude plan through a local Claude Code.")
			case err != nil:
				return err
			case s.Remaining() <= 0:
				// Require deletes the session and revokes the grants.
				auth.Require()
				t.Box("SESSION", []string{t.Red("expired "+ui.Ago(s.ExpiresAt)) + t.Gray(" · grants revoked · run ") + t.Lime("ackn4th login")})
			default:
				sessionBox(a, s)
			}
			grantsBox(cmd.Context(), a)

			roots, _ := a.cfg.AllowedRoots()
			var rl []string
			for i, r := range roots {
				tag := ""
				if i == 0 {
					tag = t.Gray("  (default)")
				}
				rl = append(rl, t.Lime(r)+tag)
			}
			path, _ := config.Path()
			rl = append(rl, t.Gray("config: "+path))
			t.Box("WORKSPACE ROOTS", rl)

			tasks, err := a.store.List()
			if err != nil {
				return err
			}
			counts := map[task.Status]int{}
			for _, tk := range tasks {
				counts[tk.Status]++
			}
			var parts []string
			for _, st := range []task.Status{task.Running, task.Stopped, task.Failed, task.Pending, task.Completed} {
				parts = append(parts, statusLabel(t, st)+" "+t.Bold(fmt.Sprint(counts[st])))
			}
			t.Box("TASKS", []string{strings.Join(parts, "   ")})
			return nil
		},
	}
}
