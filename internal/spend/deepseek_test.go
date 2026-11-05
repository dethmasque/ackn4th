package spend

import (
	"math"
	"testing"
)

func TestDeepSeekPriced(t *testing.T) {
	p := &Policy{}
	for _, m := range []string{"deepseek-chat", "deepseek-reasoner"} {
		pr, ok := p.Price("deepseek", m)
		if !ok || pr.Input <= 0 || pr.Output <= 0 {
			t.Fatalf("%s unpriced: %+v ok=%v", m, pr, ok)
		}
	}
	if !Billed["deepseek"] {
		t.Fatal("deepseek must be billed")
	}
	if _, ok := p.Price("deepseek", "deepseek-unknown"); ok {
		t.Fatal("unknown deepseek model must be unpriced, not $0")
	}
}

func TestDeepSeekCost(t *testing.T) {
	pr := Prices["deepseek-chat"]
	// 1M fresh in (0.28) + 1M cached (0.028) + 1M out (0.42), input includes cached.
	got := pr.Cost(Tokens{InputTokens: 2_000_000, CachedTokens: 1_000_000, OutputTokens: 1_000_000})
	if math.Abs(got-0.728) > 1e-9 {
		t.Fatalf("cost %v", got)
	}
	if pr.Cost(Tokens{}) != 0 {
		t.Fatal("zero tokens must cost zero")
	}
}
