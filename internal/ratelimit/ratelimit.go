// Package ratelimit keeps API-key clients under their provider's rate
// limits, so a team of agents slows down instead of getting rejected.
//
// A Gate is shared by every agent on one API key in this process. Before a
// request it waits out any cool-down; after a response it reads the
// provider's rate-limit headers (OpenAI-style x-ratelimit-*, and
// Retry-After) and, when the remaining requests or tokens run low, holds
// further requests until the window resets. A 429 sets a cool-down so the
// SDK's own retry, and every other agent, waits instead of hammering.
package ratelimit

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Gate paces requests against one rate-limited credential.
type Gate struct {
	mu    sync.Mutex
	until time.Time // no requests before this
	// MinGap spaces requests out, as a floor on top of what headers say.
	MinGap time.Duration
	last   time.Time
	now    func() time.Time
}

var (
	gatesMu sync.Mutex
	gates   = map[string]*Gate{}
)

// For returns the process-wide gate for key (a provider name plus a
// fingerprint of the credential, never the credential itself).
func For(key string) *Gate {
	gatesMu.Lock()
	defer gatesMu.Unlock()
	g := gates[key]
	if g == nil {
		g = &Gate{}
		gates[key] = g
	}
	return g
}

func (g *Gate) clock() time.Time {
	if g.now != nil {
		return g.now()
	}
	return time.Now()
}

// Wait blocks until a request may go out.
func (g *Gate) Wait(ctx context.Context) error {
	for {
		g.mu.Lock()
		now := g.clock()
		at := g.until
		if next := g.last.Add(g.MinGap); next.After(at) {
			at = next
		}
		if !at.After(now) {
			g.last = now
			g.mu.Unlock()
			return nil
		}
		g.mu.Unlock()
		t := time.NewTimer(at.Sub(now))
		select {
		case <-ctx.Done():
			t.Stop()
			return ctx.Err()
		case <-t.C:
		}
	}
}

// hold pushes the cool-down out to at least d from now.
func (g *Gate) hold(d time.Duration) {
	if d <= 0 {
		return
	}
	d = min(d, 5*time.Minute)
	g.mu.Lock()
	defer g.mu.Unlock()
	if u := g.clock().Add(d); u.After(g.until) {
		g.until = u
	}
}

// Delay is how long to hold off after a response, from its headers.
func Delay(status int, h http.Header) time.Duration {
	var d time.Duration
	if status == http.StatusTooManyRequests || status == http.StatusServiceUnavailable {
		d = retryAfter(h)
		if d == 0 {
			d = 2 * time.Second
		}
	}
	// Running low: wait for the window to reset rather than spend the last
	// few and get rejected mid-turn.
	for _, kind := range []string{"requests", "tokens"} {
		rem, err := strconv.ParseInt(h.Get("x-ratelimit-remaining-"+kind), 10, 64)
		if err != nil {
			continue
		}
		limit, _ := strconv.ParseInt(h.Get("x-ratelimit-limit-"+kind), 10, 64)
		low := rem <= 1
		if limit > 0 {
			low = rem*20 < limit // under 5% left
		}
		if low {
			d = max(d, parseReset(h.Get("x-ratelimit-reset-"+kind)))
		}
	}
	return d
}

func retryAfter(h http.Header) time.Duration {
	if ms, err := strconv.ParseFloat(h.Get("retry-after-ms"), 64); err == nil {
		return time.Duration(ms * float64(time.Millisecond))
	}
	v := h.Get("Retry-After")
	if s, err := strconv.ParseFloat(v, 64); err == nil {
		return time.Duration(s * float64(time.Second))
	}
	if t, err := http.ParseTime(v); err == nil {
		return time.Until(t)
	}
	return 0
}

// parseReset reads OpenAI's reset durations: "1s", "6m0s", "120ms", "1.5s".
func parseReset(v string) time.Duration {
	v = strings.TrimSpace(v)
	if v == "" {
		return 0
	}
	if d, err := time.ParseDuration(v); err == nil {
		return d
	}
	if s, err := strconv.ParseFloat(v, 64); err == nil {
		return time.Duration(s * float64(time.Second))
	}
	return 0
}

// Transport is an http.RoundTripper that waits on Gate before each request
// and updates it from each response.
type Transport struct {
	Base http.RoundTripper
	Gate *Gate
}

func (t *Transport) RoundTrip(r *http.Request) (*http.Response, error) {
	if err := t.Gate.Wait(r.Context()); err != nil {
		return nil, err
	}
	base := t.Base
	if base == nil {
		base = http.DefaultTransport
	}
	resp, err := base.RoundTrip(r)
	if err == nil {
		t.Gate.hold(Delay(resp.StatusCode, resp.Header))
	}
	return resp, err
}

// Client wraps an HTTP client's transport with the gate.
func Client(g *Gate) *http.Client {
	return &http.Client{Transport: &Transport{Gate: g}}
}

// Fingerprint identifies a credential for For without keeping it.
func Fingerprint(secret string) string {
	sum := sha256.Sum256([]byte(secret))
	return hex.EncodeToString(sum[:8])
}
