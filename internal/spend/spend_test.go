package spend

import (
	"math"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func near(a, b float64) bool { return math.Abs(a-b) < 1e-9 }

func TestCost(t *testing.T) {
	opus := Prices["claude-opus-5-5"]
	// 1M in: 600k cached, 100k written, 300k fresh; 100k out.
	u := Tokens{InputTokens: 1_000_000, CachedTokens: 600_000, CacheWriteTokens: 100_000, OutputTokens: 100_000}
	if got, want := opus.Cost(u), 0.3*4+0.6*0.2+0.1*5+0.1*20; !near(got, want) {
		t.Errorf("opus = %v, want %v", got, want)
	}
	gem := Prices["gemini-3.1-pro-preview"]
	if got := gem.Cost(Tokens{InputTokens: 300_000}); !near(got, 1.2) {
		t.Errorf("long gemini prompt = %v, want the long price", got)
	}
	// Unset cached and write prices default to a tenth of input, and input.
	if got := (Price{Input: 10}).Cost(Tokens{InputTokens: 2_000_000, CachedTokens: 1_000_000}); !near(got, 11) {
		t.Errorf("defaults = %v", got)
	}
}

func TestPrice(t *testing.T) {
	p := &Policy{Limits: Limits{Prices: map[string]Price{"gpt-6": {Input: 3, Output: 9}}}}
	for _, c := range []struct {
		prov, model string
		ok          bool
	}{
		{"openai", "gpt-6", true}, {"openai", "gpt-5.2-2026-01-01", true}, {"openai", "gpt-7", false},
		{"claude-code", "anything", true}, {"claude", "claude-opus-5-5", true},
	} {
		if _, ok := p.Price(c.prov, c.model); ok != c.ok {
			t.Errorf("%s %s: ok = %v", c.prov, c.model, ok)
		}
	}
	if p.Capped("claude-code", 5) {
		t.Error("a plan-billed CLI isn't capped")
	}
}

// The ledger sums today and this month across appends from anyone, and
// reads only what is new.
func TestLedger(t *testing.T) {
	l := &Ledger{Dir: t.TempDir()}
	now := time.Date(2026, 11, 3, 15, 0, 0, 0, time.Local)
	l.Append(Entry{Time: now.AddDate(0, 0, -1), Provider: "gemini", USD: 2})
	l.Append(Entry{Time: now, Provider: "claude", USD: 1})
	l.Append(Entry{Time: now.AddDate(0, -1, 0), Provider: "claude", USD: 50})
	got, err := l.Totals(now)
	if err != nil || got.Day != 1 || got.Month != 3 || got.ProviderDay["gemini"] != 0 || got.ProviderMonth["gemini"] != 2 {
		t.Fatalf("%+v %v", got, err)
	}
	other := &Ledger{Dir: l.Dir} // another runner
	other.Append(Entry{Time: now, Provider: "claude", USD: 0.5})
	// A line being written isn't counted until it's whole.
	f, _ := os.OpenFile(filepath.Join(l.Dir, "2026-11.jsonl"), os.O_WRONLY|os.O_APPEND, 0)
	f.WriteString(`{"time":"2026-11-03T15:00:00Z","provider":"claude","usd":`)
	got, _ = l.Totals(now)
	if got.Day != 1.5 {
		t.Errorf("day = %v after another runner's append", got.Day)
	}
	f.WriteString("4}\n")
	f.Close()
	if got, _ = l.Totals(now); got.Day != 5.5 {
		t.Errorf("day = %v once the line is whole", got.Day)
	}
}

func TestCheck(t *testing.T) {
	p := &Policy{Limits: Limits{Cap: Cap{Monthly: 10}}, Ledger: &Ledger{Dir: t.TempDir()}}
	now := time.Now()
	if err := p.Check("claude", 0, 0, now); err != nil {
		t.Fatal(err)
	}
	p.Ledger.Append(Entry{Time: now, Provider: "gemini", USD: 10})
	if err := p.Check("claude", 0, 0, now); err == nil {
		t.Error("ackn4th's monthly cap counts every provider")
	}
	if err := p.Check("codex", 0, 0, now); err != nil {
		t.Error("a plan-billed CLI isn't capped")
	}
	if err := (Limits{Providers: map[string]Cap{"x": {Daily: -1}}}).Validate(); err == nil {
		t.Error("a negative cap is accepted")
	}
}
