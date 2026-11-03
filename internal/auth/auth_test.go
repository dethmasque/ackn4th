package auth

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/zalando/go-keyring"

	"github.com/dethmasque/ackn4th/internal/config"
)

// isolate points ackn4th's config dir (and so its OAuth dirs) at a temp dir.
func isolate(t *testing.T) string {
	t.Helper()
	keyring.MockInit()
	dir := t.TempDir()
	t.Setenv(config.HomeEnv, dir)
	return filepath.Join(dir, "config", "oauth")
}

func TestSessionLifecycle(t *testing.T) {
	isolate(t)
	if _, err := Require(); !errors.Is(err, ErrNoSession) {
		t.Fatalf("want ErrNoSession, got %v", err)
	}
	if _, err := StartSession("me", []string{"claude"}, time.Hour); err != nil {
		t.Fatal(err)
	}
	if _, err := Require("claude"); err != nil {
		t.Fatalf("live session rejected: %v", err)
	}
	if _, err := Require("gemini"); err == nil {
		t.Fatal("unverified provider accepted")
	}
	if s, err := StartSession("me", []string{"claude"}, 30*24*time.Hour); err != nil || s.ExpiresAt.Sub(s.CreatedAt) != MaxSessionTTL {
		t.Fatalf("TTL not capped: %v %v", s, err)
	}
	if err := EndSession(); err != nil {
		t.Fatal(err)
	}
	if _, err := Current(); !errors.Is(err, ErrNoSession) {
		t.Fatalf("session survived EndSession: %v", err)
	}
}

func TestDropProvider(t *testing.T) {
	isolate(t)
	if err := DropProvider("claude"); err != nil {
		t.Fatalf("no session: %v", err)
	}
	StartSession("me", []string{"claude", "gemini"}, time.Hour)
	if err := DropProvider("claude"); err != nil {
		t.Fatal(err)
	}
	if s, err := Require("gemini"); err != nil || s.Has("claude") {
		t.Fatalf("after dropping claude: %v %v", s, err)
	}
	DropProvider("gemini")
	if _, err := Current(); !errors.Is(err, ErrNoSession) {
		t.Fatalf("the session should end with its last provider: %v", err)
	}
}

func TestExpiryRevokesGrants(t *testing.T) {
	oauth := isolate(t)
	// A Claude grant on disk, as `ant auth login` would leave it.
	creds := filepath.Join(oauth, "anthropic", "credentials", ClaudeProfile+".json")
	os.MkdirAll(filepath.Dir(creds), 0o700)
	os.WriteFile(creds, []byte(`{"access_token":"x","refresh_token":"y"}`), 0o600)

	expired := &Session{User: "me", CreatedAt: time.Now().Add(-25 * time.Hour), ExpiresAt: time.Now().Add(-time.Hour), Providers: []string{"claude"}}
	data, _ := json.Marshal(expired)
	secretSet(service, sessionKey(), string(data))

	if _, err := Require(); !errors.Is(err, ErrExpired) {
		t.Fatalf("want ErrExpired, got %v", err)
	}
	if _, err := Current(); !errors.Is(err, ErrNoSession) {
		t.Fatalf("expired session was not deleted: %v", err)
	}
	if _, err := os.Stat(creds); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("Claude tokens survived session expiry")
	}
}

func TestTamperedSessionRejected(t *testing.T) {
	isolate(t)
	// A record whose lifetime exceeds the cap was not written by StartSession.
	forged := &Session{CreatedAt: time.Now(), ExpiresAt: time.Now().Add(365 * 24 * time.Hour)}
	data, _ := json.Marshal(forged)
	secretSet(service, sessionKey(), string(data))
	if _, err := Require(); !errors.Is(err, ErrExpired) {
		t.Fatalf("forged session accepted: %v", err)
	}
}

func TestGoogleCredentialTypes(t *testing.T) {
	oauth := isolate(t)
	adc := filepath.Join(oauth, "gcloud", "application_default_credentials.json")
	os.MkdirAll(filepath.Dir(adc), 0o700)

	if _, err := LoadGoogleCredentials(); !errors.Is(err, ErrNoGrant) {
		t.Fatalf("missing grant: %v", err)
	}
	// Service-account keys are long-lived secrets: refused.
	os.WriteFile(adc, []byte(`{"type":"service_account","private_key":"k","client_email":"a@b"}`), 0o600)
	if _, err := LoadGoogleCredentials(); err == nil || HasGrant("gemini") {
		t.Fatal("service-account key accepted")
	}
	os.WriteFile(adc, []byte(`{"type":"authorized_user","client_id":"c","client_secret":"s","refresh_token":"r"}`), 0o600)
	if _, err := LoadGoogleCredentials(); err != nil || !HasGrant("gemini") {
		t.Fatalf("user OAuth grant rejected: %v", err)
	}
}

func TestGoogleEnvironmentIgnored(t *testing.T) {
	isolate(t)
	// Even a valid-looking credentials file in the environment is not used.
	f := filepath.Join(t.TempDir(), "sa.json")
	os.WriteFile(f, []byte(`{"type":"authorized_user","client_id":"c","client_secret":"s","refresh_token":"r"}`), 0o600)
	t.Setenv("GOOGLE_APPLICATION_CREDENTIALS", f)
	if _, err := LoadGoogleCredentials(); !errors.Is(err, ErrNoGrant) {
		t.Fatalf("GOOGLE_APPLICATION_CREDENTIALS was consulted: %v", err)
	}
}

func TestClaudeProfileMissing(t *testing.T) {
	isolate(t)
	t.Setenv("ANTHROPIC_CONFIG_DIR", t.TempDir()) // must not be consulted either
	if _, err := LoadClaudeProfile(); !errors.Is(err, ErrNoGrant) {
		t.Fatalf("want ErrNoGrant, got %v", err)
	}
}

func TestIsolatedEnv(t *testing.T) {
	t.Setenv("ANTHROPIC_API_KEY", "sk-leak")
	t.Setenv("GOOGLE_APPLICATION_CREDENTIALS", "/tmp/x")
	env := isolatedEnv(map[string]string{"CLOUDSDK_CONFIG": "/mine"})
	for _, kv := range env {
		if kv == "ANTHROPIC_API_KEY=sk-leak" || kv == "GOOGLE_APPLICATION_CREDENTIALS=/tmp/x" {
			t.Errorf("leaked %s into the provider CLI", kv)
		}
	}
	found := false
	for _, kv := range env {
		found = found || kv == "CLOUDSDK_CONFIG=/mine"
	}
	if !found {
		t.Error("private config dir not set")
	}
}

func TestSandboxSessionIsSeparate(t *testing.T) {
	isolate(t)
	if _, err := StartSession("me", []string{"claude"}, time.Hour); err != nil {
		t.Fatal(err)
	}
	t.Setenv("ACKN4TH_HOME", t.TempDir())
	if _, err := Current(); !errors.Is(err, ErrNoSession) {
		t.Fatalf("sandbox sees the normal session: %v", err)
	}
}

func TestGeminiKeyLifecycle(t *testing.T) {
	isolate(t)
	if err := SaveGeminiKey("not a key"); err == nil {
		t.Fatal("malformed key accepted")
	}
	key := "AIzaSyTESTKEY0123456789abcdefghijklmn"
	if err := SaveGeminiKey("  " + key + "\n"); err != nil {
		t.Fatal(err)
	}
	if got, err := GeminiKey(); err != nil || got != key {
		t.Fatalf("stored key = %q, %v", got, err)
	}
	g, err := geminiKeyGrant()
	if err != nil || !g.APIKey || g.Account != "AI Studio API key …klmn" {
		t.Fatalf("grant = %+v, %v", g, err)
	}
	// Session expiry locks the key out but keeps it; logout deletes it.
	RevokeAll()
	if !HasGeminiKey() {
		t.Fatal("session expiry deleted the key")
	}
	if err := ForgetGeminiKey(); err != nil || HasGeminiKey() {
		t.Fatalf("key survived logout: %v", err)
	}
	if err := ForgetGeminiKey(); err != nil {
		t.Fatalf("forgetting a missing key: %v", err)
	}
}

func TestSecretsDir(t *testing.T) {
	dir := t.TempDir()
	os.Chmod(dir, 0o755)
	t.Setenv(SecretsDirEnv, dir)
	if err := secretSet("svc", "u", "v"); err == nil {
		t.Fatal("a directory others can read should be refused")
	}
	os.Chmod(dir, 0o700)
	if _, err := secretGet("svc", "u"); err != errNotFound {
		t.Fatalf("missing: %v", err)
	}
	if err := secretSet("svc", "u", "v"); err != nil {
		t.Fatal(err)
	}
	if v, err := secretGet("svc", "u"); err != nil || v != "v" {
		t.Fatalf("get = %q %v", v, err)
	}
	if err := secretDelete("svc", "u"); err != nil {
		t.Fatal(err)
	}
	if err := secretDelete("svc", "u"); err != errNotFound {
		t.Fatalf("second delete: %v", err)
	}
}
