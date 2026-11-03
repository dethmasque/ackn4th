package auth

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/zalando/go-keyring"
)

// SecretsDirEnv points ackn4th at a directory to keep secrets in instead
// of the OS keychain, for systems that have none, such as containers. The
// container image sets it to a tmpfs mount, so secrets live in memory and
// vanish when the container stops. The directory must exist, belong to the
// user and be private (mode 0700).
const SecretsDirEnv = "ACKN4TH_SECRETS_DIR"

// errNotFound is what the secret store returns for a missing entry.
var errNotFound = keyring.ErrNotFound

// secretsDir returns the directory store, if one is configured.
func secretsDir() (string, bool, error) {
	dir := os.Getenv(SecretsDirEnv)
	if dir == "" {
		return "", false, nil
	}
	fi, err := os.Stat(dir)
	if err != nil {
		return "", true, fmt.Errorf("%s: %w", SecretsDirEnv, err)
	}
	if !fi.IsDir() || fi.Mode().Perm()&0o077 != 0 {
		return "", true, fmt.Errorf("%s=%s must be a directory only you can read (chmod 700)", SecretsDirEnv, dir)
	}
	return dir, true, nil
}

func secretFile(dir, svc, user string) string {
	sum := sha256.Sum256([]byte(svc + "\x00" + user))
	return filepath.Join(dir, hex.EncodeToString(sum[:16]))
}

func secretSet(svc, user, value string) error {
	dir, ok, err := secretsDir()
	if !ok {
		return keyring.Set(svc, user, value)
	}
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".new-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.WriteString(value); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), secretFile(dir, svc, user))
}

func secretGet(svc, user string) (string, error) {
	dir, ok, err := secretsDir()
	if !ok {
		return keyring.Get(svc, user)
	}
	if err != nil {
		return "", err
	}
	data, err := os.ReadFile(secretFile(dir, svc, user))
	if errors.Is(err, os.ErrNotExist) {
		return "", errNotFound
	}
	return string(data), err
}

func secretDelete(svc, user string) error {
	dir, ok, err := secretsDir()
	if !ok {
		return keyring.Delete(svc, user)
	}
	if err != nil {
		return err
	}
	err = os.Remove(secretFile(dir, svc, user))
	if errors.Is(err, os.ErrNotExist) {
		return errNotFound
	}
	return err
}

// SecretStore names where secrets are kept, for status screens.
func SecretStore() string {
	if dir, ok, _ := secretsDir(); ok {
		return "private directory " + dir
	}
	return "OS keychain"
}
