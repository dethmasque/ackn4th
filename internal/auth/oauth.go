package auth

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	gauth "cloud.google.com/go/auth"
	"cloud.google.com/go/auth/credentials"
	aconfig "github.com/anthropics/anthropic-sdk-go/config"

	"github.com/dethmasque/ackn4th/internal/config"
	"github.com/dethmasque/ackn4th/internal/provider/claudecode"
	"github.com/dethmasque/ackn4th/internal/provider/cliagent"
	"github.com/dethmasque/ackn4th/internal/provider/clis"
)

// ClaudeProfile is the name of ackn4th's Anthropic OAuth profile.
const ClaudeProfile = "ackn4th"

// googleScope is the only scope requested for Gemini (Vertex AI).
const googleScope = "https://www.googleapis.com/auth/cloud-platform"

// ErrNoGrant means the provider has no OAuth grant; run `ackn4th login`.
var ErrNoGrant = errors.New("not authorized")

// Grant describes a provider's current OAuth grant.
type Grant struct {
	Provider string
	Account  string
	// TokenExpiry is when the current access token expires. Tokens are
	// refreshed automatically until the grant is revoked.
	TokenExpiry time.Time
	Source      string // where the grant lives
	// APIKey is set for a stored API key rather than an OAuth grant.
	APIKey bool
	// External is set for a login ackn4th uses but doesn't hold (Claude
	// Code's own).
	External bool
}

func oauthDir(provider string) (string, error) {
	dir, err := config.Dir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "oauth", provider), nil
}

// anthropicDir is ackn4th's private ANTHROPIC_CONFIG_DIR.
func anthropicDir() (string, error) { return oauthDir("anthropic") }

// gcloudDir is ackn4th's private CLOUDSDK_CONFIG.
func gcloudDir() (string, error) { return oauthDir("gcloud") }

func adcPath() (string, error) {
	dir, err := gcloudDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "application_default_credentials.json"), nil
}

// LoadClaudeProfile returns ackn4th's Anthropic OAuth profile. Only
// user_oauth profiles are accepted: they carry short-lived access tokens,
// never a static API key.
func LoadClaudeProfile() (*aconfig.Config, error) {
	dir, err := anthropicDir()
	if err != nil {
		return nil, err
	}
	cfg, err := aconfig.LoadProfile(dir, ClaudeProfile)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, fmt.Errorf("claude: %w: run `ackn4th login --provider claude`", ErrNoGrant)
	}
	if err != nil {
		return nil, fmt.Errorf("claude: %w", err)
	}
	if cfg.AuthenticationInfo == nil || cfg.AuthenticationInfo.Type != aconfig.AuthenticationTypeUserOAuth {
		return nil, errors.New("claude: ackn4th's Anthropic profile is not a user OAuth login; run `ackn4th login --provider claude --reauth`")
	}
	return cfg, nil
}

func claudeGrant() (*Grant, error) {
	if _, err := LoadClaudeProfile(); err != nil {
		return nil, err
	}
	dir, _ := anthropicDir()
	path := aconfig.ProfileCredentialsPath(dir, ClaudeProfile)
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("claude: %w: run `ackn4th login --provider claude`", ErrNoGrant)
	}
	var c aconfig.Credentials
	if err := json.Unmarshal(data, &c); err != nil {
		return nil, fmt.Errorf("claude: unreadable credentials: %w", err)
	}
	g := &Grant{Provider: "claude", Account: c.AccountEmail, Source: "ant profile " + ClaudeProfile}
	if c.OrganizationName != "" {
		g.Account += " · " + c.OrganizationName
	}
	if c.ExpiresAt != nil {
		g.TokenExpiry = *c.ExpiresAt
	}
	return g, nil
}

// LoadGoogleCredentials returns ackn4th's Google OAuth credentials. Only
// user (authorized_user) and impersonated service-account grants are
// accepted, which mint short-lived access tokens; service-account key files
// are rejected because they are long-lived secrets. The environment
// (GOOGLE_APPLICATION_CREDENTIALS, gcloud's own ADC) is never consulted.
func LoadGoogleCredentials() (*gauth.Credentials, error) {
	path, err := adcPath()
	if err != nil {
		return nil, err
	}
	data, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, fmt.Errorf("gemini: %w: run `ackn4th login --provider gemini`", ErrNoGrant)
	}
	if err != nil {
		return nil, err
	}
	typ, err := checkGoogleCredType(data)
	if err != nil {
		return nil, err
	}
	return credentials.NewCredentialsFromJSON(typ, data, &credentials.DetectOptions{
		Scopes: []string{googleScope},
	})
}

// checkGoogleCredType returns the grant's type, refusing any but the
// short-lived kinds.
func checkGoogleCredType(data []byte) (credentials.CredType, error) {
	var head struct {
		Type string `json:"type"`
	}
	if err := json.Unmarshal(data, &head); err != nil {
		return "", fmt.Errorf("gemini: unreadable credentials: %w", err)
	}
	switch t := credentials.CredType(head.Type); t {
	case credentials.AuthorizedUser, credentials.ImpersonatedServiceAccount:
		return t, nil
	}
	return "", fmt.Errorf("gemini: credentials of type %q are not short-lived OAuth; run `ackn4th login --provider gemini --reauth`", head.Type)
}

func geminiGrant(ctx context.Context) (*Grant, error) {
	creds, err := LoadGoogleCredentials()
	if err != nil {
		return nil, err
	}
	tok, err := creds.Token(ctx)
	if err != nil {
		return nil, fmt.Errorf("gemini: refreshing access token failed (grant revoked or expired?): %w", err)
	}
	return &Grant{Provider: "gemini", Account: "Google user (gcloud ADC)", TokenExpiry: tok.Expiry, Source: "ackn4th gcloud config"}, nil
}

// CurrentGrant inspects a provider's grant. For Gemini this mints (or
// reuses) an access token, so it needs the network.
func CurrentGrant(ctx context.Context, provider string) (*Grant, error) {
	switch provider {
	case "claude":
		return claudeGrant()
	case "gemini":
		if HasGeminiKey() {
			return geminiKeyGrant()
		}
		return geminiGrant(ctx)
	case "openai":
		key, err := OpenAIKey()
		if err != nil {
			return nil, err
		}
		return &Grant{Provider: provider, Account: "API key …" + key[len(key)-4:], Source: SecretStore(), APIKey: true}, nil
	case clis.CodexName, clis.CursorName, clis.CopilotName:
		d := CLIDriver(provider)
		who, err := d.Status(ctx)
		if err != nil {
			return nil, err
		}
		return &Grant{Provider: provider, Account: who, Source: provider + "'s own login", External: true}, nil
	case claudecode.Name:
		a, err := claudecode.Status(ctx, "")
		if err != nil {
			return nil, err
		}
		return &Grant{Provider: provider, Account: a.Describe(), Source: "Claude Code's own login", External: true}, nil
	}
	return nil, fmt.Errorf("unknown provider %q", provider)
}

// HasGrant reports whether an OAuth grant exists on disk, without network
// calls. A Gemini API key is not a grant; see HasGeminiKey.
func HasGrant(provider string) bool {
	switch provider {
	case "claude":
		_, err := claudeGrant()
		return err == nil
	case "gemini":
		path, err := adcPath()
		if err != nil {
			return false
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return false
		}
		_, err = checkGoogleCredType(data)
		return err == nil
	case claudecode.Name:
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		_, err := claudecode.Status(ctx, "")
		return err == nil
	case clis.CodexName, clis.CursorName, clis.CopilotName:
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		_, err := CLIDriver(provider).Status(ctx)
		return err == nil
	}
	return false
}

// ContainerOAuthPortEnv names the port, published by the container
// wrapper, that a browser OAuth callback can use from the host.
const ContainerOAuthPortEnv = "ACKN4TH_OAUTH_PORT"

// InContainer reports whether ackn4th runs in its container image.
func InContainer() bool { return os.Getenv("ACKN4TH_CONTAINER") != "" }

// CLIDriver returns the driver for an agent-CLI provider, or nil.
func CLIDriver(provider string) cliagent.Driver {
	switch provider {
	case clis.CodexName:
		return clis.Codex{}
	case clis.CursorName:
		return clis.Cursor{}
	case clis.CopilotName:
		return clis.Copilot{}
	}
	return nil
}

// Tool is an external CLI ackn4th drives for OAuth.
type Tool struct {
	Name    string
	Install string
}

// Tools lists the CLI each provider's login uses.
var Tools = map[string]Tool{
	"claude":         {Name: "ant", Install: "go install github.com/anthropics/anthropic-cli/v2/cmd/ant@v2.4.0  (the container image includes it)"},
	"gemini":         {Name: "gcloud", Install: "https://cloud.google.com/sdk/docs/install"},
	claudecode.Name:  {Name: "claude", Install: claudecode.Install},
	clis.CodexName:   {Name: "codex", Install: "install the ChatGPT desktop app, or `npm i -g @openai/codex@0.166.0`; then `codex login`"},
	clis.CursorName:  {Name: "cursor-agent", Install: cursorInstall()},
	clis.CopilotName: {Name: "copilot", Install: "`npm i -g @github/copilot@1.0.105`, then `copilot login`"},
}

func cursorInstall() string {
	if runtime.GOOS == "windows" {
		return "in PowerShell, `irm 'https://cursor.com/install?win32=true' | iex`; then `cursor-agent login`"
	}
	return "`curl https://cursor.com/install -fsS | bash`; then `cursor-agent login`"
}

// isolatedEnv returns the environment for a provider CLI: credentials and
// config-dir variables from the user's shell are dropped, so the CLI can
// only see and write ackn4th's private config dir.
func isolatedEnv(set map[string]string) []string {
	drop := []string{"ANTHROPIC_API_KEY", "ANTHROPIC_AUTH_TOKEN", "ANTHROPIC_PROFILE", "ANTHROPIC_CONFIG_DIR", "ANTHROPIC_BASE_URL",
		"CLOUDSDK_CONFIG", "GOOGLE_APPLICATION_CREDENTIALS", "CLOUDSDK_AUTH_ACCESS_TOKEN_FILE"}
	var env []string
	for _, kv := range os.Environ() {
		name, _, _ := strings.Cut(kv, "=")
		keep := true
		for _, d := range drop {
			if name == d {
				keep = false
			}
		}
		if keep {
			env = append(env, kv)
		}
	}
	for k, v := range set {
		env = append(env, k+"="+v)
	}
	return env
}

func run(ctx context.Context, provider string, stdio [3]any, args ...string) error {
	tool := Tools[provider]
	bin, err := exec.LookPath(tool.Name)
	if err != nil {
		return fmt.Errorf("%s needs the `%s` CLI: %s", provider, tool.Name, tool.Install)
	}
	var env map[string]string
	switch provider {
	case "claude":
		dir, err := anthropicDir()
		if err != nil {
			return err
		}
		env = map[string]string{"ANTHROPIC_CONFIG_DIR": dir}
	case "gemini":
		dir, err := gcloudDir()
		if err != nil {
			return err
		}
		env = map[string]string{"CLOUDSDK_CONFIG": dir}
	}
	for _, d := range env {
		if err := os.MkdirAll(d, 0o700); err != nil {
			return err
		}
	}
	cmd := exec.CommandContext(ctx, bin, args...)
	cmd.Env = isolatedEnv(env)
	if r, ok := stdio[0].(io.Reader); ok {
		cmd.Stdin = r
	}
	if w, ok := stdio[1].(io.Writer); ok {
		cmd.Stdout = w
	}
	if w, ok := stdio[2].(io.Writer); ok {
		cmd.Stderr = w
	}
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("%s %s: %w", tool.Name, strings.Join(args[:min(3, len(args))], " "), err)
	}
	return nil
}

// Login runs the provider's browser OAuth flow into ackn4th's private
// config dir. gcpProject is the Vertex AI project for Gemini, billed for
// quota.
func Login(ctx context.Context, provider, gcpProject string, in io.Reader, out io.Writer) error {
	stdio := [3]any{in, out, out}
	switch provider {
	case "claude":
		args := []string{"auth", "login", "--profile", ClaudeProfile}
		// In the container, the host's browser reaches ant's callback
		// through a published port (see container/ackn4th-container).
		if port := os.Getenv(ContainerOAuthPortEnv); port != "" {
			args = append(args, "--no-browser", "--callback-port", port)
		}
		return run(ctx, provider, stdio, args...)
	case "gemini":
		if err := run(ctx, provider, stdio, "auth", "application-default", "login", "--scopes="+googleScope); err != nil {
			return err
		}
		if gcpProject == "" {
			return nil
		}
		// User credentials need a quota project for Vertex AI.
		return run(ctx, provider, stdio, "auth", "application-default", "set-quota-project", gcpProject, "--quiet")
	case clis.CodexName, clis.CursorName, clis.CopilotName:
		// The CLI's everyday login, like Claude Code's.
		d := CLIDriver(provider)
		bin, err := d.Bin()
		if err != nil {
			return fmt.Errorf("%s needs its CLI: %s", provider, Tools[provider].Install)
		}
		argv := d.Login(bin)
		if InContainer() && provider == clis.CodexName {
			// No browser in here, and its callback is on the container's
			// localhost: a device code works from any browser.
			argv = append(argv, "--device-auth")
		}
		cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
		cmd.Stdin, cmd.Stdout, cmd.Stderr = in, out, out
		if err := cmd.Run(); err != nil {
			return fmt.Errorf("%s: %w", strings.Join(argv[1:], " "), err)
		}
		return nil
	case claudecode.Name:
		// Claude Code's everyday login, not a private one: using it is
		// the point.
		bin, err := exec.LookPath("claude")
		if err != nil {
			return fmt.Errorf("claude-code needs the `claude` CLI: %s", claudecode.Install)
		}
		cmd := exec.CommandContext(ctx, bin, "auth", "login")
		cmd.Stdin, cmd.Stdout, cmd.Stderr = in, out, out
		if err := cmd.Run(); err != nil {
			return fmt.Errorf("claude auth login: %w", err)
		}
		return nil
	}
	return fmt.Errorf("unknown provider %q", provider)
}

// Revoke ends a provider grant. For Gemini the refresh token is revoked
// with Google; for Claude the profile's tokens are deleted from disk (the
// refresh token also hard-expires server-side). Claude Code's login isn't
// ackn4th's, so it is left alone. Revoking a missing grant is not an
// error.
func Revoke(provider string) error {
	switch provider {
	case claudecode.Name, "openai", clis.CodexName, clis.CursorName, clis.CopilotName:
		// The OpenAI key is kept like the Gemini key: unusable without a
		// session, deleted by logout.
		return nil
	case "claude":
		dir, err := anthropicDir()
		if err != nil {
			return err
		}
		if err := os.Remove(aconfig.ProfileCredentialsPath(dir, ClaudeProfile)); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return err
		}
		return nil
	case "gemini":
		path, err := adcPath()
		if err != nil {
			return err
		}
		if _, err := os.Stat(path); errors.Is(err, fs.ErrNotExist) {
			return nil
		}
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		revokeErr := run(ctx, provider, [3]any{nil, io.Discard, io.Discard}, "auth", "application-default", "revoke", "--quiet")
		// Delete the file regardless, so a failed revoke (offline) still
		// leaves nothing usable on disk.
		if err := os.Remove(path); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return err
		}
		if revokeErr != nil {
			return fmt.Errorf("gemini: token deleted locally, but revoking it with Google failed: %w", revokeErr)
		}
		return nil
	}
	return fmt.Errorf("unknown provider %q", provider)
}

// RevokeAll revokes every provider's OAuth grant, returning the first
// error. A stored Gemini API key is kept (it is unusable without a
// session); `ackn4th logout` deletes it too.
func RevokeAll() error {
	var first error
	for _, p := range Providers {
		if err := Revoke(p); err != nil && first == nil {
			first = err
		}
	}
	return first
}
