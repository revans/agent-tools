package main

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// authTransport adds the bearer token. It is the only place the token touches a request.
type authTransport struct {
	token string
	next  http.RoundTripper
}

func (t *authTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	r2 := r.Clone(r.Context())
	r2.Header.Set("Authorization", "Bearer "+t.token)
	return t.next.RoundTrip(r2)
}

// retryTransport retries reads (GET/HEAD) that fail with 429, 5xx or a connection error, with exponential
// backoff, honouring Retry-After. It never retries anything else: a create must not run twice.
type retryTransport struct {
	next     http.RoundTripper
	sleep    func(ctx context.Context, d time.Duration) error
	now      func() time.Time
	attempts int
	base     time.Duration
	max      time.Duration
	debug    io.Writer // nil unless --debug
}

func retryable(status int) bool {
	return status == http.StatusTooManyRequests || (status >= 500 && status <= 599)
}

// retryDelay is the wait before attempt n+1 (n starts at 0).
func retryDelay(h http.Header, n int, base, max time.Duration, now time.Time) time.Duration {
	d := base << n
	if d > max || d <= 0 {
		d = max
	}
	if ra := strings.TrimSpace(h.Get("Retry-After")); ra != "" {
		var w time.Duration
		if secs, err := strconv.Atoi(ra); err == nil && secs >= 0 {
			w = time.Duration(secs) * time.Second
		} else if t, err := http.ParseTime(ra); err == nil {
			w = t.Sub(now)
		}
		if w > 2*time.Minute {
			w = 2 * time.Minute
		}
		if w > d {
			d = w
		}
	}
	return d
}

func (t *retryTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	canRetry := r.Method == http.MethodGet || r.Method == http.MethodHead
	for n := 0; ; n++ {
		start := t.now()
		resp, err := t.next.RoundTrip(r)
		status := 0
		if resp != nil {
			status = resp.StatusCode
		}
		if t.debug != nil {
			fmt.Fprintf(t.debug, "debug: %s %s -> %s (%dms)\n", r.Method, r.URL.String(), statusText(status, err), t.now().Sub(start).Milliseconds())
		}
		failed := err != nil || retryable(status)
		if !canRetry || !failed || n+1 >= t.attempts {
			return resp, err
		}
		var h http.Header
		if resp != nil {
			h = resp.Header
			io.Copy(io.Discard, resp.Body)
			resp.Body.Close()
		}
		d := retryDelay(h, n, t.base, t.max, t.now())
		if t.debug != nil {
			fmt.Fprintf(t.debug, "debug: retrying %s in %s\n", r.URL.Path, d)
		}
		if serr := t.sleep(r.Context(), d); serr != nil {
			return nil, serr
		}
	}
}

func statusText(status int, err error) string {
	if err != nil {
		return "network error"
	}
	return strconv.Itoa(status)
}
