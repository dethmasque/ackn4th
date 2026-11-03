package claude

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/anthropics/anthropic-sdk-go"

	"github.com/dethmasque/ackn4th/internal/provider"
)

// The request must carry cache breakpoints: a zero-value cache_control is
// silently dropped from the JSON, which once left caching off entirely.
func TestRequestsAreCached(t *testing.T) {
	p := &Provider{cfg: Config{Model: "claude-opus-5-5", MaxTokens: 1000, Effort: "high", CompactAt: 50000}}
	s := &session{p: p, system: systemBlocks(provider.System{Shared: "team", Own: "role"})}
	raw, err := json.Marshal(s.params([]anthropic.BetaMessageParam{anthropic.NewBetaUserMessage(anthropic.NewBetaTextBlock("hi"))}))
	if err != nil {
		t.Fatal(err)
	}
	var req struct {
		CacheControl *struct{ Type string } `json:"cache_control"`
		System       []struct {
			Text         string
			CacheControl *struct{ Type string } `json:"cache_control"`
		}
	}
	json.Unmarshal(raw, &req)
	if req.CacheControl == nil || req.CacheControl.Type != "ephemeral" {
		t.Errorf("no top-level automatic caching: %s", raw)
	}
	if len(req.System) != 2 || req.System[0].Text != "team" || req.System[0].CacheControl == nil || req.System[1].CacheControl != nil {
		t.Errorf("want a breakpoint on the shared block only: %s", raw)
	}
	if !strings.Contains(string(raw), `"effort":"high"`) {
		t.Errorf("effort missing: %s", raw)
	}
}

func TestDescribeLeadsWithTheMessage(t *testing.T) {
	e := &anthropic.Error{StatusCode: 400, RequestID: "req_1"}
	if err := e.UnmarshalJSON([]byte(`{"type":"error","error":{"type":"invalid_request_error","message":"Your credit balance is too low to access the Anthropic API."},"request_id":"req_1"}`)); err != nil {
		t.Fatal(err)
	}
	got := describe(e).Error()
	if !strings.HasPrefix(got, "claude: out of API credits: Your credit balance is too low") || !strings.Contains(got, "console.anthropic.com") {
		t.Errorf("credit error = %q", got)
	}
	e = &anthropic.Error{StatusCode: 400}
	e.UnmarshalJSON([]byte(`{"type":"error","error":{"type":"invalid_request_error","message":"max_tokens: too large"},"request_id":"req_2"}`))
	if got := describe(e).Error(); got != "claude: max_tokens: too large (400 invalid_request_error, request req_2)" {
		t.Errorf("other error = %q", got)
	}
}
