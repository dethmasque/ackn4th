//go:build linux || windows

package sandbox

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSecretFilesFindsCredentials(t *testing.T) {
	ws := t.TempDir()
	os.MkdirAll(filepath.Join(ws, "a", "b"), 0o700)
	for _, f := range []string{".env", filepath.Join("a", "b", "server.pem"), "main.go", ".env.example"} {
		os.WriteFile(filepath.Join(ws, f), nil, 0o600)
	}
	got, err := secretFiles(ws)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Errorf("got %q, want .env and a/b/server.pem", got)
	}
}

// A workspace too big to scan completely must not yield a partial list:
// the credential files past the cut-off would be readable by commands.
func TestSecretFilesFailsClosedWhenTruncated(t *testing.T) {
	old := maxScan
	maxScan = 20
	defer func() { maxScan = old }()
	ws := t.TempDir()
	os.MkdirAll(filepath.Join(ws, "aaa"), 0o700)
	for i := 0; i < 30; i++ {
		os.WriteFile(filepath.Join(ws, "aaa", "f"+string(rune('a'+i%26))+string(rune('a'+i/26))), nil, 0o600)
	}
	os.WriteFile(filepath.Join(ws, "zzz.pem"), nil, 0o600)
	got, err := secretFiles(ws)
	if err == nil || !strings.Contains(err.Error(), "too many") {
		t.Errorf("got %q, %v: want an error", got, err)
	}
}
