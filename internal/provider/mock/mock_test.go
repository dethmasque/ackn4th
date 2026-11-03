package mock

import "testing"

func TestFileFor(t *testing.T) {
	cases := map[string][]string{
		"mock/mock-backend_dev.md": {"**"},
		"test/mock-backend_dev.md": {"**/*_test.go", "test/**"},
		"mock-backend_dev.md":      {"*.go", "*.md"},
		"REVIEW.md":                {"REVIEW.md", "reviews/*.md"},
		"docs/mock-backend_dev.md": {"docs/**", "*.md"},
	}
	for want, globs := range cases {
		if got := fileFor(pickGlob(globs), "backend_dev"); got != want {
			t.Errorf("%v → %s, want %s", globs, got, want)
		}
	}
}
