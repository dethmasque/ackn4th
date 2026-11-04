package auth

import (
	"errors"
	"fmt"
	"strings"

	"github.com/dethmasque/ackn4th/internal/config"
)

// GeminiKeyURL is where users create a Gemini API key. No Google Cloud
// project or console is involved: AI Studio sets one up behind the scenes.
const GeminiKeyURL = "https://aistudio.google.com/apikey"

// ErrNoKey means no Gemini API key is stored; run `ackn4th login`.
var ErrNoKey = errors.New("gemini: no API key stored: run `ackn4th login --provider gemini`")

// geminiKeyEntry is the key's keychain entry, one per sandbox, so a sandbox never
// sees the normal install's key.
func geminiKeyEntry() string {
	if h, ok := config.Sandbox(); ok {
		return "sandbox:" + h + ":gemini-api-key"
	}
	return "gemini-api-key"
}

// SaveGeminiKey stores a Gemini API key in the OS keychain. It is a
// long-lived credential, for users without Google Cloud, so it never
// touches disk, flags or the environment, and `ackn4th logout` deletes
// it.
func SaveGeminiKey(key string) error {
	key = strings.TrimSpace(key)
	if err := checkGeminiKey(key); err != nil {
		return err
	}
	return secretSet(service, geminiKeyEntry(), key)
}

func checkGeminiKey(key string) error {
	if len(key) < 20 || strings.ContainsAny(key, " \t\r\n\"'") {
		return errors.New("that doesn't look like a Gemini API key: copy it from " + GeminiKeyURL)
	}
	return nil
}

// GeminiKey returns the stored Gemini API key.
func GeminiKey() (string, error) {
	key, err := secretGet(service, geminiKeyEntry())
	if errors.Is(err, errNotFound) {
		return "", ErrNoKey
	}
	if err != nil {
		return "", fmt.Errorf("gemini: read API key from keychain: %w", err)
	}
	return key, nil
}

// HasGeminiKey reports whether a Gemini API key is stored.
func HasGeminiKey() bool {
	_, err := GeminiKey()
	return err == nil
}

// ForgetGeminiKey deletes the stored key. Deleting a missing key is not an
// error.
func ForgetGeminiKey() error {
	err := secretDelete(service, geminiKeyEntry())
	if errors.Is(err, errNotFound) {
		return nil
	}
	return err
}

func geminiKeyGrant() (*Grant, error) {
	key, err := GeminiKey()
	if err != nil {
		return nil, err
	}
	return &Grant{Provider: "gemini", Account: "AI Studio API key …" + key[len(key)-4:], Source: SecretStore(), APIKey: true}, nil
}

// OpenAIKeyURL is where users create an OpenAI API key.
const OpenAIKeyURL = "https://platform.openai.com/api-keys"

func openAIKeyEntry() string {
	if h, ok := config.Sandbox(); ok {
		return "sandbox:" + h + ":openai-api-key"
	}
	return "openai-api-key"
}

// ErrNoOpenAIKey means no OpenAI API key is stored.
var ErrNoOpenAIKey = errors.New("openai: no API key stored: run `ackn4th login --provider openai`")

// SaveOpenAIKey stores an OpenAI API key, like SaveGeminiKey: only in the
// secret store, deleted on logout.
func SaveOpenAIKey(key string) error {
	key = strings.TrimSpace(key)
	if !strings.HasPrefix(key, "sk-") || len(key) < 20 || strings.ContainsAny(key, " \t\r\n\"'") {
		return errors.New("that doesn't look like an OpenAI API key (they start with sk-): create one at " + OpenAIKeyURL)
	}
	return secretSet(service, openAIKeyEntry(), key)
}

// OpenAIKey returns the stored OpenAI API key.
func OpenAIKey() (string, error) {
	key, err := secretGet(service, openAIKeyEntry())
	if errors.Is(err, errNotFound) {
		return "", ErrNoOpenAIKey
	}
	if err != nil {
		return "", fmt.Errorf("openai: read API key: %w", err)
	}
	return key, nil
}

// HasOpenAIKey reports whether an OpenAI API key is stored.
func HasOpenAIKey() bool {
	_, err := OpenAIKey()
	return err == nil
}

// ForgetOpenAIKey deletes the stored key; a missing key is not an error.
func ForgetOpenAIKey() error {
	err := secretDelete(service, openAIKeyEntry())
	if errors.Is(err, errNotFound) {
		return nil
	}
	return err
}
