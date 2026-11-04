package auth

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

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

func TestSignedIn(t *testing.T) {
	isolate(t)
	if Holds("claude") || SignedIn("openai") {
		t.Fatal("nothing is stored yet")
	}
	err := Require("claude", "openai")
	if !errors.Is(err, ErrNotSignedIn) || !strings.Contains(err.Error(), "claude") || !strings.Contains(err.Error(), "openai") {
		t.Fatalf("want both named, got %v", err)
	}
	if err := Require(); err != nil {
		t.Fatalf("no providers needed: %v", err)
	}
	if err := SaveOpenAIKey("sk-proj-TESTKEY0123456789abcdefghijklmn"); err != nil {
		t.Fatal(err)
	}
	if err := Require("openai"); err != nil {
		t.Fatalf("stored key rejected: %v", err)
	}
	if err := StillHeld("openai"); err != nil {
		t.Fatal(err)
	}
	// Signing out stops running tasks at their next request.
	if err := ForgetOpenAIKey(); err != nil {
		t.Fatal(err)
	}
	if err := StillHeld("openai"); !errors.Is(err, ErrNotSignedIn) {
		t.Fatalf("deleted key still held: %v", err)
	}
	// The agent CLIs keep their own logins: nothing to lose mid-task.
	if err := StillHeld("copilot"); err != nil {
		t.Fatal(err)
	}
}

func TestOldSignInForgotten(t *testing.T) {
	isolate(t)
	secretSet(service, oldSignInKey(), `{"providers":["claude"]}`)
	ForgetOldSignIn()
	if _, err := secretGet(service, oldSignInKey()); !errors.Is(err, errNotFound) {
		t.Fatalf("old record kept: %v", err)
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

func TestSandboxKeyIsSeparate(t *testing.T) {
	isolate(t)
	if err := SaveOpenAIKey("sk-proj-TESTKEY0123456789abcdefghijklmn"); err != nil {
		t.Fatal(err)
	}
	t.Setenv("ACKN4TH_HOME", t.TempDir())
	if HasOpenAIKey() {
		t.Fatal("a sandbox sees the normal install's key")
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
	if !Holds("gemini") {
		t.Fatal("a stored key should sign gemini in")
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
	if runtime.GOOS == "windows" {
		t.Setenv(SecretsDirEnv, dir)
		if err := secretSet("svc", "u", "v"); err == nil {
			t.Fatal("Windows can't check the directory is private: it should be refused")
		}
		return
	}
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
