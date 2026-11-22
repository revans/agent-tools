package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

type sleepLog struct{ d []time.Duration }

func (s *sleepLog) sleep(_ context.Context, d time.Duration) error { s.d = append(s.d, d); return nil }

func newRetryClient(srv *httptest.Server, s *sleepLog, now func() time.Time) *http.Client {
	return &http.Client{Transport: &retryTransport{next: http.DefaultTransport, sleep: s.sleep, now: now,
		attempts: 4, base: 500 * time.Millisecond, max: 10 * time.Second}}
}

func TestRetryBackoffOnServerErrors(t *testing.T) {
	var calls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if atomic.AddInt32(&calls, 1) < 4 {
			w.WriteHeader(503)
			return
		}
		w.Write([]byte("ok"))
	}))
	defer srv.Close()
	s := &sleepLog{}
	resp, err := newRetryClient(srv, s, func() time.Time { return testNow }).Get(srv.URL)
	if err != nil || resp.StatusCode != 200 {
		t.Fatalf("%v %v", resp, err)
	}
	want := []time.Duration{500 * time.Millisecond, time.Second, 2 * time.Second}
	if len(s.d) != 3 || s.d[0] != want[0] || s.d[1] != want[1] || s.d[2] != want[2] {
		t.Fatalf("backoff %v, want %v", s.d, want)
	}
}

func TestRetryGivesUp(t *testing.T) {
	var calls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&calls, 1)
		w.WriteHeader(500)
	}))
	defer srv.Close()
	resp, err := newRetryClient(srv, &sleepLog{}, time.Now).Get(srv.URL)
	if err != nil || resp.StatusCode != 500 || calls != 4 {
		t.Fatalf("status %v calls %d err %v", resp, calls, err)
	}
}

func TestRetryHonoursRetryAfter(t *testing.T) {
	var calls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch atomic.AddInt32(&calls, 1) {
		case 1:
			w.Header().Set("Retry-After", "7")
			w.WriteHeader(429)
		case 2:
			w.Header().Set("Retry-After", testNow.Add(30*time.Second).UTC().Format(http.TimeFormat))
			w.WriteHeader(429)
		default:
			w.Write([]byte("ok"))
		}
	}))
	defer srv.Close()
	s := &sleepLog{}
	resp, err := newRetryClient(srv, s, func() time.Time { return testNow }).Get(srv.URL)
	if err != nil || resp.StatusCode != 200 {
		t.Fatal(resp, err)
	}
	if len(s.d) != 2 || s.d[0] != 7*time.Second || s.d[1] != 30*time.Second {
		t.Fatalf("waits %v, want 7s then 30s", s.d)
	}
}

func TestRetryNeverRepeatsWrites(t *testing.T) {
	for _, method := range []string{"POST", "DELETE", "PUT", "PATCH"} {
		var calls int32
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			atomic.AddInt32(&calls, 1)
			w.WriteHeader(503)
		}))
		req, _ := http.NewRequest(method, srv.URL, strings.NewReader("{}"))
		s := &sleepLog{}
		resp, err := newRetryClient(srv, s, time.Now).Do(req)
		srv.Close()
		if err != nil || resp.StatusCode != 503 || calls != 1 || len(s.d) != 0 {
			t.Errorf("%s was tried %d time(s) (status %v, err %v): a write must never be retried", method, calls, resp, err)
		}
	}
}

func TestRetryDelayCaps(t *testing.T) {
	if d := retryDelay(http.Header{}, 10, time.Second, 10*time.Second, testNow); d != 10*time.Second {
		t.Fatal(d)
	}
	h := http.Header{"Retry-After": []string{"100000"}}
	if d := retryDelay(h, 0, time.Second, 10*time.Second, testNow); d != 2*time.Minute {
		t.Fatalf("Retry-After should be capped at two minutes, got %v", d)
	}
}

// End to end: a read that fails twice with 503 then works, and a create that fails once and is not repeated.
func TestE2EReadsRetryCreatesDoNot(t *testing.T) {
	e := newEnv(t)
	e.fake.Fails["GET /v2/regions"] = []fakeFail{{Status: 503}, {Status: 429, RetryAfter: "3"}}
	e.mustRun("regions")
	if len(e.sleeps) != 2 || e.sleeps[1] != 3*time.Second {
		t.Fatalf("sleeps %v", e.sleeps)
	}
	e.fake.Fails["POST /v2/droplets"] = []fakeFail{{Status: 503}}
	e.expect(exitError, "droplet", "create", "--name", "web", "--region", "nyc3", "--size", "s-1vcpu-1gb", "--image", "ubuntu-24-04-x64", "--yes")
	if n := len(e.fake.Requests("POST /v2/droplets")); n != 1 {
		t.Fatalf("the create was sent %d times", n)
	}
}
