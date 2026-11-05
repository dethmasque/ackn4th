package dash

import "testing"

func TestProviderColorsDistinct(t *testing.T) {
	names := []string{"claude", "gemini", "claude-code", "openai", "deepseek", "codex", "cursor", "copilot"}
	seen := map[rgb]string{}
	for _, n := range names {
		c := providerColor(n)
		if prev, dup := seen[c]; dup {
			t.Errorf("%s and %s share a color", n, prev)
		}
		seen[c] = n
	}
	if providerColor("deepseek") == providerColor("unknown-provider") {
		t.Error("deepseek fell through to the default color")
	}
}
