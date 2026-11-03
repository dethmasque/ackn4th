package ratelimit

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestDelay(t *testing.T) {
	h := http.Header{}
	if d := Delay(200, h); d != 0 {
		t.Errorf("no headers: %v", d)
	}
	h.Set("Retry-After", "3")
	if d := Delay(429, h); d != 3*time.Second {
		t.Errorf("retry-after: %v", d)
	}
	h = http.Header{}
	h.Set("x-ratelimit-limit-tokens", "30000")
	h.Set("x-ratelimit-remaining-tokens", "900")
	h.Set("x-ratelimit-reset-tokens", "6m0s")
	if d := Delay(200, h); d != 6*time.Minute {
		t.Errorf("low tokens: %v", d)
	}
	h.Set("x-ratelimit-remaining-tokens", "20000")
	if d := Delay(200, h); d != 0 {
		t.Errorf("plenty left: %v", d)
	}
}

func TestTransportHoldsAfter429(t *testing.T) {
	n := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n++
		if n == 1 {
			w.Header().Set("retry-after-ms", "150")
			w.WriteHeader(429)
		}
	}))
	defer srv.Close()
	c := Client(&Gate{})
	c.Get(srv.URL)
	start := time.Now()
	resp, err := c.Get(srv.URL)
	if err != nil || resp.StatusCode != 200 {
		t.Fatal(err)
	}
	if time.Since(start) < 120*time.Millisecond {
		t.Errorf("the second request should wait out the 429")
	}
	ctx, cancel := context.WithCancel(context.Background())
	g := &Gate{}
	g.hold(time.Hour)
	cancel()
	if g.Wait(ctx) == nil {
		t.Error("Wait should honor cancellation")
	}
}
