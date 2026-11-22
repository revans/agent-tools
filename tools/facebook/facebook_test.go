package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/revans/agent-tools/apikit"
	"github.com/revans/agent-tools/postlog"
)

const secretToken = "EAAB-SECRET-PAGE-TOKEN"

func TestValidateMessage(t *testing.T) {
	if validateMessage("--message", strings.Repeat("é", 70000)) != nil {
		t.Error("Meta documents no limit, so a long message is not refused here")
	}
	for _, c := range []string{"", "  \n"} {
		if validateMessage("--message", c) == nil {
			t.Errorf("%q must be refused", c)
		}
	}
}

func TestValidateURL(t *testing.T) {
	for _, ok := range []string{"https://cdn.example.com/a.jpg", "http://x.test/a.png", "https://x.test/image?id=3", "  https://x.test/p.gif  "} {
		if _, err := validateURL("--image-url", ok); err != nil {
			t.Errorf("%q should be accepted: %v", ok, err)
		}
	}
	for _, bad := range []string{"", "/tmp/a.jpg", "a.jpg", "ftp://x.test/a.jpg", "https:///a.jpg", "file:///a.jpg", "javascript:alert(1)"} {
		if _, err := validateURL("--image-url", bad); err == nil {
			t.Errorf("%q should be refused", bad)
		}
	}
}

func TestValidateObjectID(t *testing.T) {
	for _, ok := range []string{"123456789", "111222333_444555666", " 1_2 "} {
		if _, err := validateObjectID("--post-id", ok); err != nil {
			t.Errorf("%q should be accepted: %v", ok, err)
		}
	}
	for _, bad := range []string{"", "abc", "123/comments", "../x", "12 3", "1?access_token=x", "1_", "_1", "1_2_3", strings.Repeat("1", 65)} {
		if _, err := validateObjectID("--post-id", bad); err == nil {
			t.Errorf("%q should be refused", bad)
		}
	}
}

type recorded struct {
	Method, Path, RawQuery string
	Form                   url.Values // the body of a POST
	Header                 http.Header
}

type fakeFB struct {
	srv  *httptest.Server
	mu   sync.Mutex
	reqs []recorded

	postErr     string // body of a 400 for feed/photos/comments
	insightsErr string // body of a 400 for post insights
	postNodeErr string // body of a 400 for the post node
	postNode    string // override of the post node answer
	insights    string // override of the insights answer
}

func newFake(t *testing.T) *fakeFB {
	f := &fakeFB{}
	f.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rec := recorded{Method: r.Method, Path: r.URL.Path, RawQuery: r.URL.RawQuery, Header: r.Header}
		raw, _ := io.ReadAll(r.Body)
		if len(raw) > 0 {
			if ct := r.Header.Get("Content-Type"); ct != "application/x-www-form-urlencoded" {
				t.Errorf("a POST body must be form-encoded, got %q: %s", ct, raw)
			}
			rec.Form, _ = url.ParseQuery(string(raw))
		}
		f.mu.Lock()
		defer f.mu.Unlock()
		f.reqs = append(f.reqs, rec)
		w.Header().Set("Content-Type", "application/json")
		token := r.URL.Query().Get("access_token")
		if r.Method == "POST" {
			token = rec.Form.Get("access_token")
		}
		if token != secretToken {
			w.WriteHeader(400)
			fmt.Fprint(w, `{"error":{"message":"Invalid OAuth access token","code":190}}`)
			return
		}
		fail := func(body string) bool {
			if body == "" {
				return false
			}
			w.WriteHeader(400)
			fmt.Fprint(w, body)
			return true
		}
		switch {
		case r.Method == "GET" && r.URL.Path == "/v27.0/555":
			fmt.Fprint(w, `{"name":"Robert's Page","id":"555"}`)
		case r.Method == "POST" && r.URL.Path == "/v27.0/555/feed":
			if !fail(f.postErr) {
				fmt.Fprint(w, `{"id":"555_9001"}`)
			}
		case r.Method == "POST" && r.URL.Path == "/v27.0/555/photos":
			if !fail(f.postErr) {
				fmt.Fprint(w, `{"id":"7001","post_id":"555_7002"}`)
			}
		case r.Method == "POST" && strings.HasSuffix(r.URL.Path, "/comments"):
			if !fail(f.postErr) {
				fmt.Fprint(w, `{"id":"555_9001_300"}`)
			}
		case r.Method == "GET" && r.URL.Path == "/v27.0/555/published_posts":
			fmt.Fprint(w, `{"data":[{"created_time":"2026-11-01T10:00:00+0000","message":"Hello","id":"555_9001","permalink_url":"https://www.facebook.com/555/posts/9001"}],"paging":{"cursors":{"before":"B","after":"A"}}}`)
		case r.Method == "GET" && strings.HasSuffix(r.URL.Path, "/insights"):
			if fail(f.insightsErr) {
				return
			}
			if f.insights != "" {
				fmt.Fprint(w, f.insights)
				return
			}
			fmt.Fprint(w, `{"data":[{"name":"post_media_view","period":"lifetime","values":[{"value":120}]},{"name":"post_total_media_view_unique","period":"lifetime","values":[{"value":90}]}]}`)
		case r.Method == "GET" && strings.HasPrefix(r.URL.Path, "/v27.0/555_"):
			if fail(f.postNodeErr) {
				return
			}
			if f.postNode != "" {
				fmt.Fprint(w, f.postNode)
				return
			}
			fmt.Fprint(w, `{"id":"555_9001","message":"Hello","created_time":"2026-11-01T10:00:00+0000","permalink_url":"https://www.facebook.com/555/posts/9001","reactions":{"data":[],"summary":{"total_count":14,"viewer_reaction":"NONE"}},"comments":{"data":[],"summary":{"order":"ranked","total_count":4,"can_comment":true}},"shares":{"count":2}}`)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakeFB) paths() []string {
	var out []string
	for _, r := range f.reqs {
		out = append(out, r.Method+" "+r.Path)
	}
	return out
}

func setup(t *testing.T, f *fakeFB, extra string) func(args ...string) (int, string, string) {
	t.Helper()
	t.Setenv(postlog.EnvPath, filepath.Join(t.TempDir(), "posts.jsonl")) // never the real log
	cfg := filepath.Join(t.TempDir(), "facebook.toml")
	body := fmt.Sprintf("default='main'\n[accounts.main]\npage_id='555'\naccess_token=%q\nbase_url=%q\n%s", secretToken, f.srv.URL, extra)
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

// tokenPlacement: a POST carries the token in its form body, a GET in its query,
// and never in a POST's URL or a header.
func tokenPlacement(t *testing.T, r recorded) {
	t.Helper()
	if r.Method == "POST" {
		if strings.Contains(r.RawQuery, secretToken) || strings.Contains(r.RawQuery, "access_token") {
			t.Errorf("a POST must carry the token in its body, not the URL: %s?%s", r.Path, r.RawQuery)
		}
		if r.Form.Get("access_token") != secretToken {
			t.Errorf("a POST carries access_token in its form body: %v", r.Form)
		}
	} else if !strings.Contains(r.RawQuery, "access_token="+secretToken) {
		t.Errorf("a GET carries access_token in its query: %s", r.RawQuery)
	}
	if r.Header.Get("Authorization") != "" {
		t.Errorf("the token is not sent as a header: %v", r.Header)
	}
}

func TestPostSendsMessageAndLinkAsAForm(t *testing.T) {
	f := newFake(t)
	run := setup(t, f, "")
	code, out, errs := run("post", "--message", "Hello é world", "--link", "https://example.com/a")
	if code != 0 {
		t.Fatalf("%d %s", code, errs)
	}
	if got := strings.Join(f.paths(), "|"); got != "POST /v27.0/555/feed" {
		t.Fatalf("requests: %s", got)
	}
	r := f.reqs[0]
	tokenPlacement(t, r)
	if r.Form.Get("message") != "Hello é world" || r.Form.Get("link") != "https://example.com/a" || len(r.Form) != 3 {
		t.Errorf("form: %v", r.Form)
	}
	if m := decode(t, out); m["id"] != "555_9001" || m["account"] != "main" {
		t.Errorf("result: %v", m)
	}
	if strings.Contains(out, secretToken) {
		t.Error("the token must never be printed")
	}
	// no link: the field is not sent at all
	run("post", "--message", "Plain")
	if _, has := f.reqs[1].Form["link"]; has {
		t.Errorf("an absent link is not sent: %v", f.reqs[1].Form)
	}
}

func TestPostValidationSendsNothing(t *testing.T) {
	f := newFake(t)
	run := setup(t, f, "")
	for name, args := range map[string][]string{
		"empty message": {"post", "--message", "  "},
		"no message":    {"post"},
		"bad link":      {"post", "--message", "x", "--link", "ftp://x.test/a"},
		"local link":    {"post", "--message", "x", "--link", "/etc/passwd"},
		"photo no url":  {"photo"},
		"photo ftp":     {"photo", "--image-url", "ftp://x.test/a.jpg"},
		"photo local":   {"photo", "--image-url", "/tmp/a.jpg"},
		"comment id":    {"comment", "--post-id", "abc", "--message", "x"},
		"comment empty": {"comment", "--post-id", "555_1", "--message", ""},
		"limit 0":       {"posts", "--limit", "0"},
		"limit 101":     {"posts", "--limit", "101"},
	} {
		if code, _, _ := run(args...); code == 0 {
			t.Errorf("%s: should fail", name)
		}
	}
	if len(f.reqs) != 0 {
		t.Errorf("nothing may be sent: %v", f.paths())
	}
}

func TestPhotoSendsUrlAndCaptionAndReturnsThePostID(t *testing.T) {
	f := newFake(t)
	run := setup(t, f, "")
	code, out, errs := run("photo", "--image-url", "https://cdn.example.com/p.png", "--caption", "A photo")
	if code != 0 {
		t.Fatalf("%d %s", code, errs)
	}
	r := f.reqs[0]
	tokenPlacement(t, r)
	if r.Path != "/v27.0/555/photos" || r.Form.Get("url") != "https://cdn.example.com/p.png" || r.Form.Get("caption") != "A photo" || len(r.Form) != 3 {
		t.Errorf("photo request: %s %v", r.Path, r.Form)
	}
	if m := decode(t, out); m["id"] != "555_7002" || m["post_id"] != "555_7002" || m["photo_id"] != "7001" {
		t.Errorf("the post id is the id: %v", m)
	}
	run("photo", "--image-url", "https://cdn.example.com/q.png")
	if _, has := f.reqs[1].Form["caption"]; has {
		t.Errorf("no caption, none sent: %v", f.reqs[1].Form)
	}
}

func TestCommentUsesThePostIDInThePath(t *testing.T) {
	f := newFake(t)
	run := setup(t, f, "")
	code, out, errs := run("comment", "--post-id", "555_9001", "--message", "Thanks!")
	if code != 0 {
		t.Fatalf("%d %s", code, errs)
	}
	r := f.reqs[0]
	tokenPlacement(t, r)
	if r.Path != "/v27.0/555_9001/comments" || r.Form.Get("message") != "Thanks!" || len(r.Form) != 2 {
		t.Errorf("comment request: %s %v", r.Path, r.Form)
	}
	if decode(t, out)["id"] != "555_9001_300" {
		t.Errorf("result: %s", out)
	}
}

func TestPostsListsPublishedPosts(t *testing.T) {
	f := newFake(t)
	run := setup(t, f, "")
	code, out, errs := run("posts", "--limit", "5")
	if code != 0 {
		t.Fatalf("%d %s", code, errs)
	}
	var l []map[string]any
	if err := json.Unmarshal([]byte(out), &l); err != nil || len(l) != 1 || l[0]["id"] != "555_9001" || l[0]["permalink_url"] == nil {
		t.Fatalf("%v %s", err, out)
	}
	r := f.reqs[0]
	tokenPlacement(t, r)
	q, _ := url.ParseQuery(r.RawQuery)
	if r.Path != "/v27.0/555/published_posts" || q.Get("limit") != "5" || q.Get("fields") != "id,message,created_time,permalink_url" {
		t.Errorf("request: %s %s", r.Path, r.RawQuery)
	}
}

func TestCheckReadsThePage(t *testing.T) {
	f := newFake(t)
	run := setup(t, f, "")
	code, out, errs := run("check")
	if code != 0 {
		t.Fatalf("%d %s", code, errs)
	}
	d, _ := decode(t, out)["detail"].(map[string]any)
	if d["page_id"] != "555" || d["name"] != "Robert's Page" {
		t.Errorf("%s", out)
	}
	tokenPlacement(t, f.reqs[0])
	if !strings.Contains(f.reqs[0].RawQuery, "fields=id%2Cname") {
		t.Errorf("query: %s", f.reqs[0].RawQuery)
	}
}

func TestBadTokenAndMissingPageID(t *testing.T) {
	f := newFake(t)
	cfg := filepath.Join(t.TempDir(), "c.toml")
	os.WriteFile(cfg, []byte(fmt.Sprintf("default='main'\n[accounts.main]\naccess_token='wrong'\npage_id='555'\nbase_url=%q\n", f.srv.URL)), 0o600)
	var out, errb bytes.Buffer
	if code := apikit.RunArgs(service(), []string{"check", "--config", cfg}, &out, &errb); code != 1 || !strings.Contains(errb.String(), "invalid or expired") {
		t.Errorf("an invalid token is explained: %q", errb.String())
	}
	os.WriteFile(cfg, []byte(fmt.Sprintf("default='main'\n[accounts.main]\naccess_token='x'\nbase_url=%q\n", f.srv.URL)), 0o600)
	out.Reset()
	errb.Reset()
	if code := apikit.RunArgs(service(), []string{"post", "--message", "hi", "--config", cfg}, &out, &errb); code != 1 || !strings.Contains(errb.String(), "page_id") {
		t.Errorf("a missing page_id is explained: %q", errb.String())
	}
}

func TestPermissionErrorIsExplained(t *testing.T) {
	f := newFake(t)
	f.postErr = `{"error":{"message":"(#200) Requires pages_manage_posts permission","code":200}}`
	run := setup(t, f, "")
	code, _, errs := run("post", "--message", "x")
	if code != 1 || !strings.Contains(errs, "pages_manage_posts") || !strings.Contains(errs, "missing a permission") {
		t.Errorf("%d %q", code, errs)
	}
}

func TestMetricsReadsTheCountsAndTheInsights(t *testing.T) {
	f := newFake(t)
	run := setup(t, f, "")
	code, out, errs := run("metrics", "555_9001")
	if code != 0 {
		t.Fatalf("%d %s", code, errs)
	}
	m := decode(t, out)
	for k, want := range map[string]float64{"reactions": 14, "comments": 4, "shares": 2, "post_media_view": 120, "post_total_media_view_unique": 90} {
		if got, _ := m[k].(float64); got != want {
			t.Errorf("%s = %v, want %v (%v)", k, m[k], want, m)
		}
	}
	if m["insights_error"] != nil || m["permalink_url"] != "https://www.facebook.com/555/posts/9001" {
		t.Errorf("link comes along, no error: %v", m)
	}
	var sawFields, sawInsights bool
	for _, r := range f.reqs {
		tokenPlacement(t, r)
		q, _ := url.ParseQuery(r.RawQuery)
		if strings.HasSuffix(r.Path, "/insights") {
			sawInsights = r.Path == "/v27.0/555_9001/insights" && q.Get("metric") == "post_media_view,post_total_media_view_unique" && q.Get("period") == "lifetime"
		} else {
			sawFields = r.Path == "/v27.0/555_9001" && q.Get("fields") == "id,message,created_time,permalink_url,reactions.summary(true).limit(0),comments.summary(true).limit(0),shares"
		}
	}
	if !sawFields || !sawInsights {
		t.Errorf("the requests: fields=%v insights=%v %v", sawFields, sawInsights, f.paths())
	}
}

func TestMetricsKeepsTheCountsWhenInsightsAreRefused(t *testing.T) {
	f := newFake(t)
	f.insightsErr = `{"error":{"message":"(#200) Requires read_insights permission","code":200}}`
	run := setup(t, f, "")
	code, out, errs := run("metrics", "555_9001")
	if code != 0 {
		t.Fatalf("a refused insights call must not fail the command: %d %s", code, errs)
	}
	m := decode(t, out)
	if m["reactions"].(float64) != 14 || m["comments"].(float64) != 4 || m["shares"].(float64) != 2 {
		t.Errorf("the counts are still returned: %v", m)
	}
	if e, _ := m["insights_error"].(string); !strings.Contains(e, "permission") {
		t.Errorf("and the reason sits beside them: %v", m)
	}
	if m["post_media_view"] != nil {
		t.Error("no insight numbers are invented")
	}
}

func TestMetricsSharesAbsentMeansZeroAndNoReactionsIsZero(t *testing.T) {
	f := newFake(t)
	f.postNode = `{"id":"555_9001","reactions":{"data":[],"summary":{"total_count":0}},"comments":{"data":[],"summary":{"total_count":0}}}`
	f.insights = `{"data":[]}`
	run := setup(t, f, "")
	code, out, _ := run("metrics", "555_9001")
	m := decode(t, out)
	if code != 0 || m["shares"].(float64) != 0 || m["reactions"].(float64) != 0 || m["post_media_view"] != nil {
		t.Errorf("%d %v", code, m)
	}
}

func TestMetricsFailsWhenTheCountsCannotBeRead(t *testing.T) {
	f := newFake(t)
	f.postNodeErr = `{"error":{"message":"Unsupported get request. Object does not exist","code":100}}`
	run := setup(t, f, "")
	if code, _, errs := run("metrics", "555_9001"); code != 1 || errs == "" {
		t.Errorf("no counts, no result: %d %q", code, errs)
	}
	for name, args := range map[string][]string{"no id": {"metrics"}, "two ids": {"metrics", "1", "2"}, "not an id": {"metrics", "abc"}} {
		if code, _, _ := run(args...); code != 2 && code != 1 {
			t.Errorf("%s: %d", name, code)
		}
	}
	code, out, _ := run("metrics", "555_9001", "--dry-run")
	if code != 0 || !strings.Contains(out, `"dry_run":true`) {
		t.Errorf("dry run: %d %s", code, out)
	}
}

func TestDryRunSendsNothingAndHidesTheToken(t *testing.T) {
	f := newFake(t)
	run := setup(t, f, "")
	for _, args := range [][]string{
		{"post", "--message", "Dry", "--dry-run"},
		{"photo", "--image-url", "https://x.test/a.jpg", "--dry-run"},
		{"comment", "--post-id", "555_1", "--message", "Dry", "--dry-run"},
		{"check", "--dry-run"},
	} {
		code, out, errs := run(args...)
		if code != 0 || !strings.Contains(out, `"dry_run":true`) || strings.Contains(out+errs, secretToken) {
			t.Errorf("%v: %d %s %s", args, code, out, errs)
		}
	}
	if len(f.reqs) != 0 {
		t.Errorf("a dry run sends nothing: %v", f.paths())
	}
}

func TestCallRunsAnEndpointAndRefusesWritesUnderReadOnly(t *testing.T) {
	f := newFake(t)
	run := setup(t, f, "")
	if code, out, errs := run("call", "page_info", "--param", "page_id=555", "--max-safety=read"); code != 0 || !strings.Contains(out, "Robert") {
		t.Errorf("%d %s %s", code, out, errs)
	}
	if code, _, _ := run("call", "post_create", "--param", "page_id=555", "--param", "message=x", "--max-safety=read"); code == 0 {
		t.Error("a write endpoint must be refused at --max-safety=read")
	}
	if len(f.reqs) != 1 {
		t.Errorf("only the read was sent: %v", f.paths())
	}
	if code, out, errs := run("call", "published_posts", "--param", "page_id=555", "--max-safety=read"); code != 0 || !strings.Contains(out, "555_9001") {
		t.Errorf("%d %s %s", code, out, errs)
	}
}
