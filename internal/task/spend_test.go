package task

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/dethmasque/ackn4th/internal/provider"
	"github.com/dethmasque/ackn4th/internal/spend"
)

// priced is a reply that used a million tokens of input: $1 at $1/MTok.
func priced(r *provider.Reply) *provider.Reply {
	r.Usage = provider.Usage{InputTokens: 1_000_000}
	return r
}

func policy(t *testing.T, l spend.Limits) *spend.Policy {
	l.Prices = map[string]spend.Price{"m": {Input: 1, Output: 1}}
	return &spend.Policy{Limits: l, Ledger: &spend.Ledger{Dir: filepath.Join(t.TempDir(), "spend")}, Billed: map[string]bool{"claude": true}}
}

// A task stops, resumably, before the request that would go past its
// cap; what it spent is on the task and in the ledger.
func TestTaskSpendingCap(t *testing.T) {
	ls := call("list_dir", map[string]string{"path": "."})
	scripts := map[string]*script{"claude": {name: "claude", inputs: &[]string{}, replies: []*provider.Reply{
		priced(toolReply(ls)), priced(toolReply(call("read_file", map[string]string{"path": "x"}))), priced(textReply("never sent")),
	}}}
	r, tk := setup(t, scripts, AgentSpec{Role: "backend_dev", Provider: "claude", Model: "m"})
	r.Spend = policy(t, spend.Limits{PerTask: 2})
	got, err := r.Run(context.Background(), tk.ID, false)
	if err != nil || got.Status != Stopped || !strings.Contains(got.Reason, "spending cap: $2.00 of $2.00") || !strings.Contains(got.Reason, "--max-cost") {
		t.Fatalf("status %s, reason %q, err %v", got.Status, got.Reason, err)
	}
	if got.Cost() != 2 || got.Agents[0].Usage.InputTokens != 2_000_000 {
		t.Errorf("cost %v, usage %+v", got.Cost(), got.Agents[0].Usage)
	}
	if tot, _ := r.Spend.Ledger.Totals(time.Now()); tot.Day != 2 || tot.ProviderMonth["claude"] != 2 {
		t.Errorf("ledger %+v", tot)
	}

	// The task's own cap, raised, lets it go on.
	got.MaxCost = 3
	r.Store.Save(got)
	got, _ = r.Run(context.Background(), tk.ID, false)
	if got.Cost() != 3 || !strings.Contains(got.Reason, "$3.00 of $3.00") {
		t.Errorf("after raising the cap: cost %v, reason %q", got.Cost(), got.Reason)
	}
}

// Daily caps, for ackn4th and per provider, count every task's spending.
func TestSharedSpendingCaps(t *testing.T) {
	for _, l := range []spend.Limits{
		{Cap: spend.Cap{Daily: 1}},
		{Providers: map[string]spend.Cap{"claude": {Monthly: 1}}},
	} {
		scripts := map[string]*script{"claude": {name: "claude", inputs: &[]string{}, replies: []*provider.Reply{priced(textReply("hi")), textReply("never sent")}}}
		r, tk := setup(t, scripts, AgentSpec{Role: "backend_dev", Provider: "claude", Model: "m"}, AgentSpec{Role: "qa_engineer", Provider: "claude", Model: "m"})
		r.Spend = policy(t, l)
		got, _ := r.Run(context.Background(), tk.ID, false)
		if got.Status != Stopped || !strings.Contains(got.Reason, "spending cap: $1.00 of $1.00") {
			t.Errorf("%+v: status %s, reason %q", l, got.Status, got.Reason)
		}
	}
}

// Under a cap, a model with no known price doesn't run: the cap couldn't
// be kept. Without one, it runs.
func TestUnpricedModelUnderACap(t *testing.T) {
	scripts := map[string]*script{"claude": {name: "claude", inputs: &[]string{}, replies: []*provider.Reply{
		toolReply(call("declare_done", map[string]string{"summary": "ok"})), textReply("done"),
	}}}
	r, tk := setup(t, scripts, AgentSpec{Role: "backend_dev", Provider: "claude", Model: "unknown-model"})
	r.Spend = policy(t, spend.Limits{PerTask: 5})
	got, _ := r.Run(context.Background(), tk.ID, false)
	if got.Status != Failed || !strings.Contains(got.Reason, "spend.prices") {
		t.Fatalf("status %s, reason %q", got.Status, got.Reason)
	}
	r.Spend = policy(t, spend.Limits{})
	if got, _ = r.Run(context.Background(), tk.ID, false); got.Status != Completed {
		t.Errorf("without a cap: %s %q", got.Status, got.Reason)
	}
}
