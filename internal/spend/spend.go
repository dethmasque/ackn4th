// Package spend estimates what model requests cost and enforces spending
// caps: per task, per provider and for all of ackn4th, per day and per
// month. Every runner appends what it spends to a ledger shared by all of
// them, one file a month under the data dir:
//
//	spend/2026-11.jsonl   one line per model request
//
// Costs are estimates from the token counts each provider reports and the
// prices in this package (or the config's spend.prices). Providers run
// through a local CLI on the person's own plan (claude-code, codex, cursor,
// copilot) are not billed per token, so they cost nothing here.
package spend

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sync"
	"time"

	"github.com/dethmasque/ackn4th/internal/provider"
)

// Price is what a model costs, in US dollars per million tokens.
type Price struct {
	Input float64 `yaml:"input"`
	// Cached is input read from the prompt cache. Zero means Input/10.
	Cached float64 `yaml:"cached,omitempty"`
	// CacheWrite is input written to the cache (Claude). Zero means Input.
	CacheWrite float64 `yaml:"cache_write,omitempty"`
	Output     float64 `yaml:"output"`

	// Above a request's input of this many tokens, Long's prices apply.
	Above int64  `yaml:"-"`
	Long  *Price `yaml:"-"`
}

// Prices are the built-in prices, as of November 2026. A model missing
// here can be priced in the config's spend.prices.
var Prices = map[string]Price{
	"claude-fable-5-1":  {Input: 10, Cached: 0.25, CacheWrite: 12.5, Output: 50},
	"claude-opus-5-5":   {Input: 4, Cached: 0.20, CacheWrite: 5, Output: 20},
	"claude-sonnet-5-5": {Input: 2, Cached: 0.20, CacheWrite: 2.5, Output: 10},
	"claude-haiku-5-5": {Input: 0.10, Cached: 0.01, CacheWrite: 0.125, Output: 0.50,
		Above: 100_000, Long: &Price{Input: 0.50, Cached: 0.05, CacheWrite: 0.625, Output: 2.50}},
	"gemini-3.1-pro-preview": {Input: 2, Cached: 0.20, Output: 12,
		Above: 200_000, Long: &Price{Input: 4, Cached: 0.40, Output: 18}},
	"gpt-5.2":    {Input: 1.75, Cached: 0.175, Output: 14},
	"gpt-5-mini": {Input: 0.25, Cached: 0.025, Output: 2},
}

// Billed are the providers charged per token. The rest run on the
// person's plan through a local CLI, or (mock) nowhere.
var Billed = map[string]bool{"claude": true, "gemini": true, "openai": true}

// dated is an OpenAI snapshot suffix, priced as the model it snapshots.
var dated = regexp.MustCompile(`-\d{4}-\d{2}-\d{2}$`)

// Cost is what u cost at p, in dollars.
func (p Price) Cost(u provider.Usage) float64 {
	if p.Long != nil && u.InputTokens > p.Above {
		p = *p.Long
	}
	cached, write := p.Cached, p.CacheWrite
	if cached == 0 {
		cached = p.Input / 10
	}
	if write == 0 {
		write = p.Input
	}
	fresh := max(u.InputTokens-u.CachedTokens-u.CacheWriteTokens, 0)
	return (float64(fresh)*p.Input + float64(u.CachedTokens)*cached +
		float64(u.CacheWriteTokens)*write + float64(u.OutputTokens)*p.Output) / 1e6
}

// Cap is a spending limit in dollars a day and a month; zero is no limit.
type Cap struct {
	Daily   float64 `yaml:"daily,omitempty"`
	Monthly float64 `yaml:"monthly,omitempty"`
}

func (c Cap) set() bool { return c.Daily > 0 || c.Monthly > 0 }

// Limits are the spending caps, from the config's spend section.
type Limits struct {
	// PerTask caps one task over its whole life, unless the task has its
	// own (start-task --max-cost).
	PerTask float64 `yaml:"per_task,omitempty"`
	// Cap is for everything ackn4th spends, on every provider.
	Cap `yaml:",inline"`
	// Providers caps each provider's spending.
	Providers map[string]Cap `yaml:"providers,omitempty"`
	// Prices add or override model prices.
	Prices map[string]Price `yaml:"prices,omitempty"`
}

// Validate rejects negative caps and prices.
func (l Limits) Validate() error {
	bad := func(what string, v float64) error {
		if v < 0 {
			return fmt.Errorf("spend.%s: %v is negative", what, v)
		}
		return nil
	}
	errs := []error{bad("per_task", l.PerTask), bad("daily", l.Daily), bad("monthly", l.Monthly)}
	for p, c := range l.Providers {
		errs = append(errs, bad("providers."+p+".daily", c.Daily), bad("providers."+p+".monthly", c.Monthly))
	}
	for m, p := range l.Prices {
		for _, v := range []float64{p.Input, p.Cached, p.CacheWrite, p.Output} {
			errs = append(errs, bad("prices."+m, v))
		}
	}
	return errors.Join(errs...)
}

// Policy prices requests, records them and enforces the caps.
type Policy struct {
	Limits Limits
	Ledger *Ledger
	// Billed overrides which providers are charged (tests).
	Billed map[string]bool
}

func (p *Policy) billed(prov string) bool {
	if p.Billed != nil {
		return p.Billed[prov]
	}
	return Billed[prov]
}

// Price is what model costs on prov. ok is false for a billed provider's
// model with no known price.
func (p *Policy) Price(prov, model string) (Price, bool) {
	if !p.billed(prov) {
		return Price{}, true
	}
	for _, m := range []string{model, dated.ReplaceAllString(model, "")} {
		if pr, ok := p.Limits.Prices[m]; ok {
			return pr, true
		}
		if pr, ok := Prices[m]; ok {
			return pr, true
		}
	}
	return Price{}, false
}

// Capped reports whether any cap applies to a task on prov with taskCap.
func (p *Policy) Capped(prov string, taskCap float64) bool {
	if !p.billed(prov) {
		return false
	}
	return taskCap > 0 || p.Limits.Cap.set() || p.Limits.Providers[prov].set()
}

// Entry is one model request in the ledger.
type Entry struct {
	Time     time.Time `json:"time"`
	Task     string    `json:"task"`
	Provider string    `json:"provider"`
	Model    string    `json:"model"`
	USD      float64   `json:"usd"`
	In       int64     `json:"in,omitempty"`
	Out      int64     `json:"out,omitempty"`
	Cached   int64     `json:"cached,omitempty"`
}

// OverError is returned when a cap is reached.
type OverError struct{ Msg string }

func (e *OverError) Error() string { return e.Msg }

// Check returns an *OverError when a cap applies and is reached: the
// task's (taskCost against taskCap), or prov's or ackn4th's for today or
// this month.
func (p *Policy) Check(prov string, taskCost, taskCap float64, now time.Time) error {
	if !p.billed(prov) {
		return nil
	}
	if taskCap > 0 && taskCost >= taskCap {
		return &OverError{fmt.Sprintf("the task reached its spending cap: %s of %s. Resume it with --max-cost to allow more", USD(taskCost), USD(taskCap))}
	}
	if !p.Limits.Cap.set() && !p.Limits.Providers[prov].set() {
		return nil
	}
	t, err := p.Ledger.Totals(now)
	if err != nil {
		// Unreadable, the caps can't be enforced: refuse rather than spend.
		return &OverError{"spending caps can't be checked: " + err.Error()}
	}
	pc := p.Limits.Providers[prov]
	for _, c := range []struct {
		who         string
		spent, cap  float64
		when, until string
	}{
		{"ackn4th", t.Day, p.Limits.Daily, "daily", "tomorrow"},
		{"ackn4th", t.Month, p.Limits.Monthly, "monthly", "next month"},
		{prov, t.ProviderDay[prov], pc.Daily, "daily", "tomorrow"},
		{prov, t.ProviderMonth[prov], pc.Monthly, "monthly", "next month"},
	} {
		if c.cap > 0 && c.spent >= c.cap {
			return &OverError{fmt.Sprintf("%s reached its %s spending cap: %s of %s. Raise %s in the config, or wait until %s",
				c.who, c.when, USD(c.spent), USD(c.cap), capKey(c.who, prov, c.when), c.until)}
		}
	}
	return nil
}

func capKey(who, prov, when string) string {
	if who == "ackn4th" {
		return "spend." + when
	}
	return "spend.providers." + prov + "." + when
}

// USD formats dollars for people: $0.042, $1.23, $120.
func USD(v float64) string {
	switch {
	case v <= 0:
		return "$0"
	case v < 0.1:
		return fmt.Sprintf("$%.3f", v)
	case v < 100:
		return fmt.Sprintf("$%.2f", v)
	}
	return fmt.Sprintf("$%.0f", v)
}

// Record prices u and appends it to the ledger, returning its cost.
func (p *Policy) Record(task, prov, model string, u provider.Usage, now time.Time) (float64, error) {
	pr, ok := p.Price(prov, model)
	if !ok || !p.billed(prov) {
		return 0, nil
	}
	usd := pr.Cost(u)
	if p.Ledger == nil {
		return usd, nil
	}
	return usd, p.Ledger.Append(Entry{Time: now, Task: task, Provider: prov, Model: model, USD: usd,
		In: u.InputTokens, Out: u.OutputTokens, Cached: u.CachedTokens})
}

// Totals are what was spent today and this month, in all and by provider.
type Totals struct {
	Day, Month                 float64
	ProviderDay, ProviderMonth map[string]float64
}

// Ledger is the shared record of spending. Runners in other processes
// append to the same files; each line is one write, so lines don't mix.
type Ledger struct {
	Dir string

	mu    sync.Mutex
	month string // the file read so far
	off   int64
	sum   []Entry // this month's entries, summarized by day and provider
}

func monthOf(t time.Time) string { return t.Local().Format("2006-01") }

func (l *Ledger) path(month string) string { return filepath.Join(l.Dir, month+".jsonl") }

// Append adds e to its month's file.
func (l *Ledger) Append(e Entry) error {
	if err := os.MkdirAll(l.Dir, 0o700); err != nil {
		return err
	}
	b, err := json.Marshal(e)
	if err != nil {
		return err
	}
	f, err := os.OpenFile(l.path(monthOf(e.Time)), os.O_WRONLY|os.O_APPEND|os.O_CREATE, 0o600)
	if err != nil {
		return err
	}
	_, err = f.Write(append(b, '\n'))
	return errors.Join(err, f.Close())
}

// Totals sums now's day and month. It reads only what was appended since
// it last looked.
func (l *Ledger) Totals(now time.Time) (Totals, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	month := monthOf(now)
	if month != l.month {
		l.month, l.off, l.sum = month, 0, nil
	}
	f, err := os.Open(l.path(month))
	switch {
	case errors.Is(err, os.ErrNotExist):
	case err != nil:
		return Totals{}, err
	default:
		defer f.Close()
		if _, err := f.Seek(l.off, io.SeekStart); err != nil {
			return Totals{}, err
		}
		r := bufio.NewReader(f)
		for {
			line, err := r.ReadBytes('\n')
			if err != nil {
				break // a partial last line is read once it is complete
			}
			l.off += int64(len(line))
			var e Entry
			if json.Unmarshal(line, &e) == nil {
				l.add(e)
			}
		}
	}
	t := Totals{ProviderDay: map[string]float64{}, ProviderMonth: map[string]float64{}}
	day := now.Local().Format("2006-01-02")
	for _, e := range l.sum {
		t.Month += e.USD
		t.ProviderMonth[e.Provider] += e.USD
		if e.Time.Local().Format("2006-01-02") == day {
			t.Day += e.USD
			t.ProviderDay[e.Provider] += e.USD
		}
	}
	return t, nil
}

// add folds e into the summary: one entry per day and provider.
func (l *Ledger) add(e Entry) {
	day := e.Time.Local().Format("2006-01-02")
	for i := range l.sum {
		s := &l.sum[i]
		if s.Provider == e.Provider && s.Time.Local().Format("2006-01-02") == day {
			s.USD += e.USD
			return
		}
	}
	l.sum = append(l.sum, Entry{Time: e.Time, Provider: e.Provider, USD: e.USD})
}
