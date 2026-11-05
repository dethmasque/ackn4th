// Package auth manages how ackn4th authenticates to model providers.
//
// ackn4th has no accounts of its own, and no sign-in of its own: a provider
// is usable while its credential is.
//
//   - Claude: an `ant auth login` profile in ackn4th's own Anthropic config
//     dir. The SDK refreshes its short-lived access token on demand, and
//     the refresh token expires server-side.
//   - Gemini on Vertex AI: a `gcloud auth application-default login` grant
//     in ackn4th's own gcloud config dir. Access tokens last about an hour
//     and are refreshed in memory.
//   - Gemini without Google Cloud, and OpenAI: an API key, the only
//     long-lived credentials ackn4th stores: in the OS keychain, never on
//     disk or in the environment (see SaveGeminiKey).
//   - claude-code, codex, cursor and copilot: each CLI's own login. ackn4th
//     holds no credential for them.
//
// `ackn4th logout` revokes the grants and deletes the keys. A running task
// checks before each model request that the credential ackn4th holds is
// still there, so signing out stops it at its next request.
package auth

import (
	"errors"
	"fmt"
	"slices"
	"sync"

	"github.com/dethmasque/ackn4th/internal/config"
)

const service = "ackn4th"

// Providers lists the supported agent providers.
var Providers = []string{"claude", "gemini", "claude-code", "openai", "deepseek", "codex", "cursor", "copilot"}

// ErrNotSignedIn means a provider has no credential ackn4th can use.
var ErrNotSignedIn = errors.New("not signed in")

// ValidProvider reports whether name is a supported provider.
func ValidProvider(name string) bool { return slices.Contains(Providers, name) }

// Holds reports whether ackn4th holds a credential for provider, without
// network calls: an OAuth grant on disk, or an API key in the keychain.
// The agent CLIs keep their own logins, so for them it is always false.
func Holds(provider string) bool {
	switch provider {
	case "claude":
		return HasGrant(provider)
	case "gemini":
		return HasGeminiKey() || HasGrant(provider)
	case "openai":
		return HasOpenAIKey()
	case "deepseek":
		return HasDeepSeekKey()
	}
	return false
}

// SignedIn reports whether provider can be used now. For an agent CLI
// this asks the CLI, which takes a moment.
func SignedIn(provider string) bool {
	switch provider {
	case "claude", "gemini", "openai", "deepseek":
		return Holds(provider)
	}
	return HasGrant(provider)
}

// Require fails unless every provider given is signed in, checking them
// all at once and naming each one that isn't.
func Require(providers ...string) error {
	errs := make([]error, len(providers))
	var wg sync.WaitGroup
	for i, p := range providers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if !SignedIn(p) {
				errs[i] = fmt.Errorf("%s isn't signed in (%w): run `ackn4th login --provider %s`", p, ErrNotSignedIn, p)
			}
		}()
	}
	wg.Wait()
	return errors.Join(errs...)
}

// StillHeld is checked before every model request: it fails once the
// credential ackn4th held for provider is gone, so `ackn4th logout`
// stops running tasks. An agent CLI fails by itself when its own login
// ends, so for one it always passes.
func StillHeld(provider string) error {
	switch provider {
	case "claude", "gemini", "openai", "deepseek":
		if !Holds(provider) {
			return fmt.Errorf("%s was signed out (%w): run `ackn4th login --provider %s`, then resume the task", provider, ErrNotSignedIn, provider)
		}
	}
	return nil
}

// ForgetOldSignIn deletes the record older versions kept of which
// providers were signed in and until when, if it is still there.
func ForgetOldSignIn() { secretDelete(service, oldSignInKey()) }

func oldSignInKey() string {
	if h, ok := config.Sandbox(); ok {
		return "sandbox:" + h
	}
	return "session"
}
