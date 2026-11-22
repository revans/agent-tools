package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/revans/agent-tools/apikit"
)

const testKey = "SEKRET-KEY-123"

// fakeYouTube checks the protocol details a real YouTube would care about.
type fakeYouTube struct {
	srv  *httptest.Server
	mu   sync.Mutex
	seen []string // request URIs that were accepted
	all  []string // every request URI
}

func item(kind, id, title, desc string) string {
	return fmt.Sprintf(`{"id":{"kind":%q,"videoId":%q},"snippet":{"title":%q,"channelTitle":"Chan","description":%q,"publishedAt":"2026-01-01T00:00:00Z"}}`, kind, id, title, desc)
}

func newFake(t *testing.T) *fakeYouTube {
	f := &fakeYouTube{}
	f.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		f.all = append(f.all, r.URL.RequestURI())
		f.mu.Unlock()
		q := r.URL.Query()
		if _, ok := q["key"]; ok || strings.Contains(r.URL.RawQuery, testKey) {
			t.Errorf("the API key must never be in the URL: %s", r.URL)
		}
		if r.Header.Get("X-Goog-Api-Key") != testKey {
			http.Error(w, `{"error":{"code":400,"message":"API key not valid. Please pass a valid API key.","errors":[{"reason":"badRequest"}]}}`, 400)
			return
		}
		f.mu.Lock()
		f.seen = append(f.seen, r.URL.RequestURI())
		f.mu.Unlock()
		switch r.URL.Path {
		case "/search":
			if q.Get("type") != "video" || q.Get("part") != "snippet" || q.Get("maxResults") != "10" {
				t.Errorf("search params: %s", r.URL)
			}
			switch q.Get("q") {
			case "quota":
				http.Error(w, `{"error":{"code":403,"message":"The request cannot be completed because you have exceeded your quota.","errors":[{"reason":"quotaExceeded"}]}}`, 403)
			case "notenabled":
				http.Error(w, `{"error":{"code":403,"message":"YouTube Data API v3 has not been used in project 1 before or it is disabled.","errors":[{"reason":"accessNotConfigured"}]}}`, 403)
			case "empty":
				fmt.Fprint(w, `{"pageInfo":{"totalResults":0,"resultsPerPage":10}}`)
			case "more":
				fmt.Fprintf(w, `{"pageInfo":{"totalResults":1000000,"resultsPerPage":10},"items":[%s]}`, item("youtube#video", "v1", "T", "d"))
			case "long":
				fmt.Fprintf(w, `{"items":[%s]}`, item("youtube#video", "v1", "T", strings.Repeat("é", 1500)))
			case "exact":
				fmt.Fprintf(w, `{"items":[%s]}`, item("youtube#video", "v1", "T", strings.Repeat("e", 1000)))
			case "mixed":
				fmt.Fprintf(w, `{"pageInfo":{"totalResults":3},"items":[%s,%s,{"id":{"kind":"youtube#video"},"snippet":{"title":"novid"}},%s]}`,
					item("youtube#channel", "UCx", "A channel", "c"), item("youtube#video", "keep-me", "Kept", "k"), item("youtube#playlist", "PL1", "A list", "p"))
			case "text":
				if q.Get("q") != "text" {
					t.Error("unreachable")
				}
				fmt.Fprintf(w, `{"pageInfo":{"totalResults":1},"items":[%s]}`, item("youtube#video", "abc123", "Rust Lang Tutorial", "Learn Rust."))
			case "a b&c=d":
				fmt.Fprintf(w, `{"items":[%s]}`, item("youtube#video", "enc", "Encoded", "ok"))
			default:
				http.Error(w, "unexpected q "+q.Get("q"), 500)
			}
		case "/videos":
			if q.Get("id") == "agenttoolscheck" {
				if q.Get("part") != "id" {
					t.Errorf("check should ask for the cheapest part: %s", r.URL)
				}
				fmt.Fprint(w, `{"items":[]}`)
				return
			}
			if q.Get("part") != "snippet,statistics,contentDetails" || q.Get("id") != "vid1" {
				t.Errorf("videos params: %s", r.URL)
			}
			fmt.Fprint(w, `{"items":[{"id":"vid1","statistics":{"viewCount":"42"},"contentDetails":{"duration":"PT1M"}}]}`)
		case "/channels":
			fmt.Fprint(w, `{"items":[{"id":"UC1","snippet":{"title":"Chan"}}]}`)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(f.srv.Close)
	return f
}

func setup(t *testing.T, f *fakeYouTube, key string) func(args ...string) (int, string, string) {
	t.Helper()
	cfg := filepath.Join(t.TempDir(), "youtube.toml")
	body := fmt.Sprintf("default='main'\n[accounts.main]\napi_key=%q\nbase_url=%q\n", key, f.srv.URL)
	if err := os.WriteFile(cfg, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return func(args ...string) (int, string, string) {
		var out, errb bytes.Buffer
		code := apikit.RunArgs(service(), append(args, "--config", cfg), &out, &errb)
		return code, out.String(), errb.String()
	}
}

func decode(t *testing.T, s string) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal([]byte(s), &m); err != nil {
		t.Fatalf("not JSON: %v\n%s", err, s)
	}
	return m
}

func TestTruncateSnippet(t *testing.T) {
	exact := strings.Repeat("a", 1000)
	if truncateSnippet(exact) != exact || truncateSnippet("short") != "short" {
		t.Error("text within the limit must pass through")
	}
	got := truncateSnippet(strings.Repeat("é", 1500) + "💥")
	if !strings.HasSuffix(got, "...[truncated]") || len([]rune(got)) != 1000+len([]rune("...[truncated]")) {
		t.Errorf("truncation counts characters, not bytes: %d", len([]rune(got)))
	}
}

func TestSearchSendsKeyInHeaderAndShapesResult(t *testing.T) {
	f := newFake(t)
	run := setup(t, f, testKey)
	code, out, errs := run("search", "--query", "text")
	if code != 0 {
		t.Fatalf("%d %s", code, errs)
	}
	m := decode(t, out)
	res := m["results"].([]any)
	if len(res) != 1 || m["results_truncated"] != false {
		t.Fatalf("%v", m)
	}
	r0 := res[0].(map[string]any)
	if r0["url"] != "https://www.youtube.com/watch?v=abc123" || r0["title"] != "Rust Lang Tutorial" || r0["channel"] != "Chan" ||
		r0["snippet"] != "Learn Rust." || r0["published_at"] != "2026-01-01T00:00:00Z" || len(r0) != 5 {
		t.Errorf("shape: %v", r0)
	}
	if len(f.seen) != 1 || !strings.HasPrefix(f.seen[0], "/search?") {
		t.Errorf("requests: %v", f.seen)
	}
	if strings.Contains(out, testKey) {
		t.Error("key leaked into output")
	}
}

func TestQueryIsPercentEncoded(t *testing.T) {
	f := newFake(t)
	run := setup(t, f, testKey)
	if code, out, e := run("search", "--query", "a b&c=d"); code != 0 || !strings.Contains(out, "Encoded") {
		t.Fatalf("%d %s %s", code, out, e)
	}
	if strings.Contains(f.seen[0], "c=d") || !strings.Contains(f.seen[0], "q=a+b%26c%3Dd") {
		t.Errorf("q must be encoded, not interpolated: %s", f.seen[0])
	}
}

func TestOnlyVideosAndSkipsItemsWithoutVideoID(t *testing.T) {
	run := setup(t, newFake(t), testKey)
	_, out, _ := run("search", "--query", "mixed")
	m := decode(t, out)
	res := m["results"].([]any)
	if len(res) != 1 || res[0].(map[string]any)["url"] != "https://www.youtube.com/watch?v=keep-me" {
		t.Errorf("only the real video survives: %v", res)
	}
	if m["results_truncated"] != true {
		t.Error("totalResults 3 > 1 returned")
	}
}

func TestSnippetTruncationEndToEnd(t *testing.T) {
	run := setup(t, newFake(t), testKey)
	_, out, _ := run("search", "--query", "long")
	s := decode(t, out)["results"].([]any)[0].(map[string]any)["snippet"].(string)
	if !strings.HasSuffix(s, "...[truncated]") || len([]rune(s)) != 1014 {
		t.Errorf("snippet: %d chars", len([]rune(s)))
	}
	_, out, _ = run("search", "--query", "exact")
	s = decode(t, out)["results"].([]any)[0].(map[string]any)["snippet"].(string)
	if strings.Contains(s, "truncated") {
		t.Error("a snippet exactly at the limit is untouched")
	}
}

func TestTruncatedFlag(t *testing.T) {
	run := setup(t, newFake(t), testKey)
	_, out, _ := run("search", "--query", "more")
	if decode(t, out)["results_truncated"] != true {
		t.Error("totalResults above the returned count means truncated")
	}
	_, out, _ = run("search", "--query", "exact") // no pageInfo at all
	if decode(t, out)["results_truncated"] != false {
		t.Error("absent pageInfo means not truncated")
	}
}

func TestEmptyResultsIsNotAnError(t *testing.T) {
	run := setup(t, newFake(t), testKey)
	code, out, _ := run("search", "--query", "empty")
	m := decode(t, out)
	if code != 0 || len(m["results"].([]any)) != 0 || m["results_truncated"] != false {
		t.Errorf("%d %v", code, m)
	}
}

func TestQuotaAndOtherErrors(t *testing.T) {
	f := newFake(t)
	run := setup(t, f, testKey)
	if code, _, e := run("search", "--query", "quota"); code != 1 || !strings.Contains(e, "quota exceeded") || !strings.Contains(e, "HTTP 403") {
		t.Errorf("quota 403: %d %q", code, e)
	}
	code, _, e := run("search", "--query", "notenabled")
	if code != 1 || strings.Contains(e, "quota exceeded") || !strings.Contains(e, "disabled") {
		t.Errorf("a 403 for a disabled API is not a quota error: %d %q", code, e)
	}
	bad := setup(t, f, "WRONG")
	code, _, e = bad("search", "--query", "text")
	if code != 1 || !strings.Contains(e, "HTTP 400") || !strings.Contains(e, "API key not valid") || strings.Contains(e, "WRONG") {
		t.Errorf("400 bad key: %d %q", code, e)
	}
}

func TestRateLimit429(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, `{"error":{"message":"slow down"}}`, 429)
	}))
	defer srv.Close()
	f := &fakeYouTube{srv: srv}
	run := setup(t, f, testKey)
	if code, _, e := run("search", "--query", "x"); code != 1 || !strings.Contains(e, "quota exceeded or rate limited") {
		t.Errorf("%d %q", code, e)
	}
}

func TestUsageErrors(t *testing.T) {
	f := newFake(t)
	run := setup(t, f, testKey)
	if code, _, _ := run("search"); code != 2 {
		t.Errorf("missing --query: %d", code)
	}
	if code, _, _ := run("search", "--query", "   "); code != 2 {
		t.Errorf("blank --query: %d", code)
	}
	if len(f.all) != 0 {
		t.Errorf("a bad query must not reach the network: %v", f.all)
	}
}

func TestDryRunSendsNothingAndLeaksNoKey(t *testing.T) {
	f := newFake(t)
	run := setup(t, f, testKey)
	for _, args := range [][]string{{"search", "--query", "x", "--dry-run"}, {"call", "search", "--param", "q=x", "--dry-run"}} {
		code, out, e := run(args...)
		if code != 0 || decode(t, out)["dry_run"] != true {
			t.Errorf("%v: %d %s %s", args, code, out, e)
		}
		if strings.Contains(out+e, testKey) || strings.Contains(out, "key=") {
			t.Errorf("dry run leaked the key: %s", out)
		}
		if !strings.Contains(out, "[redacted]") || !strings.Contains(out, "type=video") {
			t.Errorf("dry run should show the redacted header and the request: %s", out)
		}
	}
	if len(f.all) != 0 {
		t.Errorf("dry run touched the network: %v", f.all)
	}
}

func TestRawEndpoints(t *testing.T) {
	f := newFake(t)
	run := setup(t, f, testKey)
	code, out, e := run("call", "search", "--param", "q=text")
	if code != 0 {
		t.Fatalf("%d %s", code, e)
	}
	var list []map[string]any
	if err := json.Unmarshal([]byte(out), &list); err != nil || len(list) != 1 || list[0]["video_id"] != "abc123" || list[0]["title"] != "Rust Lang Tutorial" {
		t.Errorf("call search projection: %s", out)
	}
	if code, out, _ := run("call", "videos", "--param", "id=vid1"); code != 0 || !strings.Contains(out, `"viewCount": "42"`) && !strings.Contains(out, `"viewCount":"42"`) {
		t.Errorf("videos: %d %s", code, out)
	}
	if code, out, _ := run("call", "channels", "--param", "id=UC1"); code != 0 || !strings.Contains(out, "UC1") {
		t.Errorf("channels: %d %s", code, out)
	}
	if code, _, e := run("call", "search"); code != 1 || !strings.Contains(e, "q") {
		t.Errorf("missing required q: %d %q", code, e)
	}
}

func TestEveryEndpointIsReadOnly(t *testing.T) {
	_, out, _ := setup(t, newFake(t), testKey)("endpoints")
	for _, n := range []string{"search", "videos", "channels"} {
		if !strings.Contains(out, n) {
			t.Errorf("%s not listed", n)
		}
	}
	if strings.Contains(out, `"write"`) || strings.Contains(out, `"destructive"`) {
		t.Errorf("all endpoints must be read: %s", out)
	}
	if !strings.Contains(builtin, "NOT from Mimir") {
		t.Error("extras must be labelled")
	}
	if strings.Count(builtin, `safety = "read"`) != 3 {
		t.Error("each of the three endpoints must be safety=read")
	}
}

func TestCheck(t *testing.T) {
	f := newFake(t)
	run := setup(t, f, testKey)
	if code, out, e := run("check"); code != 0 || decode(t, out)["ok"] != true {
		t.Errorf("check: %d %s %s", code, out, e)
	}
	if len(f.seen) != 1 || !strings.HasPrefix(f.seen[0], "/videos?") {
		t.Errorf("check must use the cheap videos.list call, not search: %v", f.seen)
	}
	bad := setup(t, f, "WRONG")
	if code, _, e := bad("check"); code != 1 || !strings.Contains(e, "API key not valid") {
		t.Errorf("bad key: %d %q", code, e)
	}
}

func TestKeyFromEnvironment(t *testing.T) {
	f := newFake(t)
	t.Setenv("YT_TEST_KEY", testKey)
	cfg := filepath.Join(t.TempDir(), "y.toml")
	os.WriteFile(cfg, []byte(fmt.Sprintf("[accounts.main]\napi_key_env='YT_TEST_KEY'\nbase_url=%q\n", f.srv.URL)), 0o600)
	var out, errb bytes.Buffer
	if code := apikit.RunArgs(service(), []string{"search", "--query", "text", "--config", cfg}, &out, &errb); code != 0 {
		t.Errorf("%d %s", code, errb.String())
	}
	// A missing credential fails before any request is made.
	n := len(f.all)
	os.WriteFile(cfg, []byte(fmt.Sprintf("[accounts.main]\nbase_url=%q\n", f.srv.URL)), 0o600)
	out.Reset()
	errb.Reset()
	if code := apikit.RunArgs(service(), []string{"search", "--query", "text", "--config", cfg}, &out, &errb); code == 0 || len(f.all) != n {
		t.Errorf("missing key: %d %s", code, errb.String())
	}
}
