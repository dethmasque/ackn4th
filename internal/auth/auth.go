// Package auth manages how ackn4th authenticates to model providers.
//
// Claude, and Gemini on Vertex AI, are reached with short-lived OAuth
// access tokens from grants that ackn4th owns and keeps apart from your
// everyday logins:
//
//   - Claude: an `ant auth login` profile in ackn4th's own Anthropic config
//     dir. The SDK refreshes its short-lived access token on demand.
//   - Gemini on Vertex AI: a `gcloud auth application-default login` grant
//     in ackn4th's own gcloud config dir. Access tokens last about an hour
//     and are refreshed in memory.
//
// claude-code runs Claude through the user's own Claude Code install and
// its login; ackn4th holds no credential for it, and logging out of
// ackn4th leaves Claude Code signed in. Like every provider, it still
// needs a live ackn4th session.
//
// Gemini without Google Cloud uses an AI Studio API key, the only
// long-lived credential ackn4th stores: in the OS keychain, never on disk
// or in the environment (see SaveGeminiKey).
//
// On top of that, `ackn4th login` starts a session (recorded in the OS
// keychain) that lasts at most 24h. Every model request checks it, and when
// it expires ackn4th ends the session and revokes the OAuth grants, so the
// next day starts with a fresh browser login. The API key stays in the
// keychain but is unusable until the next login; `ackn4th logout` deletes
// it.
package auth

import (
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/dethmasque/ackn4th/internal/config"
)

const (
	service = "ackn4th"

	// MaxSessionTTL is the longest a session may last, whatever the config
	// says: a login is required at least daily.
	MaxSessionTTL = 24 * time.Hour
)

// sessionKey is the session's keychain entry. A sandbox gets its own entry
// per sandbox directory, so logging in or out of one never touches the
// normal install's session or another clone's.
func sessionKey() string {
	if h, ok := config.Sandbox(); ok {
		return "sandbox:" + h
	}
	return "session"
}

// Providers lists the supported agent providers.
var Providers = []string{"claude", "gemini", "claude-code", "openai", "codex", "cursor", "copilot"}

var (
	// ErrNoSession means nobody has logged in, or the session was ended.
	ErrNoSession = errors.New("not logged in: run `ackn4th login`")
	// ErrExpired means the session's TTL has passed.
	ErrExpired = errors.New("session expired: run `ackn4th login` to start a new one")
)

// ValidProvider reports whether name is a supported provider.
func ValidProvider(name string) bool { return slices.Contains(Providers, name) }

// Session records a successful login.
type Session struct {
	User      string    `json:"user"`
	CreatedAt time.Time `json:"created_at"`
	ExpiresAt time.Time `json:"expires_at"`
	// Providers lists the providers whose grants were verified at login.
	Providers []string `json:"providers"`
}

// Remaining is the time left before the session expires.
func (s *Session) Remaining() time.Duration { return time.Until(s.ExpiresAt) }

// Has reports whether provider was verified for this session.
func (s *Session) Has(provider string) bool { return slices.Contains(s.Providers, provider) }

// StartSession records a new session valid for ttl, capped at MaxSessionTTL.
func StartSession(user string, providers []string, ttl time.Duration) (*Session, error) {
	if ttl <= 0 || ttl > MaxSessionTTL {
		ttl = MaxSessionTTL
	}
	now := time.Now().UTC()
	s := &Session{User: user, CreatedAt: now, ExpiresAt: now.Add(ttl), Providers: providers}
	data, err := json.Marshal(s)
	if err != nil {
		return nil, err
	}
	return s, secretSet(service, sessionKey(), string(data))
}

// Current returns the stored session even if it has expired; callers that
// need a usable session should call Require.
func Current() (*Session, error) {
	raw, err := secretGet(service, sessionKey())
	if errors.Is(err, errNotFound) {
		return nil, ErrNoSession
	}
	if err != nil {
		return nil, err
	}
	var s Session
	if err := json.Unmarshal([]byte(raw), &s); err != nil {
		return nil, fmt.Errorf("corrupt session (run `ackn4th login`): %w", err)
	}
	return &s, nil
}

// Require returns the live session, or ErrNoSession / ErrExpired. When
// providers are given, each must have been verified at login. The moment an
// expired session is seen it is deleted and every provider grant is revoked.
func Require(providers ...string) (*Session, error) {
	s, err := Current()
	if err != nil {
		return nil, err
	}
	if !time.Now().Before(s.ExpiresAt) || s.ExpiresAt.Sub(s.CreatedAt) > MaxSessionTTL {
		EndSession()
		RevokeAll()
		return nil, ErrExpired
	}
	for _, p := range providers {
		if !s.Has(p) {
			return nil, fmt.Errorf("%s was not verified in this session: run `ackn4th login --provider %s`", p, p)
		}
	}
	return s, nil
}

// DropProvider removes provider from the session, so tasks can't use it
// until it is logged in again. The session ends with its last provider.
// Without a session there is nothing to drop.
func DropProvider(provider string) error {
	s, err := Current()
	if errors.Is(err, ErrNoSession) {
		return nil
	}
	if err != nil {
		return err
	}
	s.Providers = slices.DeleteFunc(s.Providers, func(p string) bool { return p == provider })
	if len(s.Providers) == 0 {
		return EndSession()
	}
	data, err := json.Marshal(s)
	if err != nil {
		return err
	}
	return secretSet(service, sessionKey(), string(data))
}

// EndSession deletes the session. Ending a missing session is not an error.
func EndSession() error {
	err := secretDelete(service, sessionKey())
	if errors.Is(err, errNotFound) {
		return nil
	}
	return err
}
