package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/revans/agent-tools/apikit"
	"github.com/revans/agent-tools/postlog"
)

const secretToken = "IGQ-SECRET-TOKEN"

func TestValidateCaptionLimit(t *testing.T) {
	if err := validateCaption(strings.Repeat("é", 2200)); err != nil {
		t.Errorf("2200 characters (not bytes) is allowed: %v", err)
	}
	if err := validateCaption(strings.Repeat("é", 2201)); err == nil || !strings.Contains(err.Error(), "2200-character maximum") {
		t.Errorf("2201 must be refused: %v", err)
	}
	for _, c := range []string{"", "   \n"} {
		if validateCaption(c) == nil {
			t.Errorf("%q must be refused", c)
		}
	}
	if validateMessage("comment", strings.Repeat("a", 2200)) != nil {
		t.Error("2200 is allowed")
	}
	if validateMessage("comment", strings.Repeat("a", 2201)) == nil || validateMessage("comment", " ") == nil {
		t.Error("message limit and emptiness")
	}
}

func TestValidateImageURL(t *testing.T) {
	for _, ok := range []string{"https://cdn.example.com/a.jpg", "http://x.test/a.JPEG", "https://x.test/image?id=3", "  https://x.test/p.jpg  "} {
		if _, err := validateImageURL(ok); err != nil {
			t.Errorf("%q should be accepted: %v", ok, err)
		}
	}
	for _, bad := range []string{"", "/tmp/a.jpg", "a.jpg", "ftp://x.test/a.jpg", "https:///a.jpg", "file:///a.jpg", "https://x.test/a.png", "https://x.test/a.webp", "https://x.test/a.GIF?x=1"} {
		if _, err := validateImageURL(bad); err == nil {
			t.Errorf("%q should be refused", bad)
		}
	}
}

func TestValidateObjectID(t *testing.T) {
	if id, err := validateObjectID("--media-id", " 17895695668004550 "); err != nil || id != "17895695668004550" {
		t.Errorf("%q %v", id, err)
	}
	for _, bad := range []string{"", "abc", "123/comments", "../x", "12 3", "1?access_token=x", strings.Repeat("1", 65)} {
		if _, err := validateObjectID("--media-id", bad); err == nil {
			t.Errorf("%q should be refused", bad)
		}
	}
}

type recorded struct {
	Method, Path, RawQuery string
	Body                   map[string]any
	Header                 http.Header
}

type fakeIG struct {
	srv  *httptest.Server
	mu   sync.Mutex
	reqs []recorded

	statuses     []string // status_code answers, in order; the last repeats
	publishFails int      // media_publish answers an error this many times first
	publishErr   string   // body of that error
	publishCode  int      // HTTP status of that error
	containerErr func(w http.ResponseWriter)
	meBody       string
	commentErr   string // body of a 400 for comments/replies
	insightsErr  string // body of a 400 for a media insights call
	mediaErr     string // body of a 400 for a media fields call
	publishes    int
	polls        int
}

func newFake(t *testing.T) *fakeIG {
	f := &fakeIG{
		publishErr:  `{"error":{"message":"Media ID is not available","type":"OAuthException","code":9007}}`,
		publishCode: 400,
		meBody:      `{"data":[{"user_id":"17841400000000000","username":"robert.codes"}]}`,
	}
	f.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rec := recorded{Method: r.Method, Path: r.URL.Path, RawQuery: r.URL.RawQuery, Header: r.Header}
		raw, _ := io.ReadAll(r.Body)
		if len(raw) > 0 {
			if err := json.Unmarshal(raw, &rec.Body); err != nil {
				t.Errorf("body is not JSON: %s", raw)
			}
		}
		f.mu.Lock()
		defer f.mu.Unlock()
		f.reqs = append(f.reqs, rec)
		w.Header().Set("Content-Type", "application/json")
		tokenOK := func() bool {
			if r.Method == "GET" {
				return r.URL.Query().Get("access_token") == secretToken
			}
			return rec.Body["access_token"] == secretToken
		}
		if !tokenOK() {
			w.WriteHeader(400)
			fmt.Fprint(w, `{"error":{"message":"Invalid OAuth access token","code":190}}`)
			return
		}
		switch {
		case r.Method == "GET" && r.URL.Path == "/v25.0/me":
			fmt.Fprint(w, f.meBody)
		case r.Method == "POST" && strings.HasSuffix(r.URL.Path, "/media"):
			if f.containerErr != nil {
				f.containerErr(w)
				return
			}
			fmt.Fprint(w, `{"id":"CONTAINER1"}`)
		case r.Method == "POST" && strings.HasSuffix(r.URL.Path, "/media_publish"):
			f.publishes++
			if f.publishes <= f.publishFails {
				w.WriteHeader(f.publishCode)
				fmt.Fprint(w, f.publishErr)
				return
			}
			fmt.Fprint(w, `{"id":"MEDIA9"}`)
		case r.Method == "GET" && strings.HasSuffix(r.URL.Path, "/insights"):
			if f.insightsErr != "" {
				w.WriteHeader(400)
				fmt.Fprint(w, f.insightsErr)
				return
			}
			fmt.Fprint(w, `{"data":[{"name":"views","period":"lifetime","values":[{"value":80}]},{"name":"reach","period":"lifetime","values":[{"value":60}]},{"name":"saved","period":"lifetime","total_value":{"value":3}},{"name":"shares","period":"lifetime","values":[{"value":1}]}]}`)
		case r.Method == "GET" && strings.HasPrefix(r.URL.Path, "/v25.0/1790"):
			if f.mediaErr != "" {
				w.WriteHeader(400)
				fmt.Fprint(w, f.mediaErr)
				return
			}
			fmt.Fprint(w, `{"like_count":12,"comments_count":3,"media_type":"IMAGE","permalink":"https://www.instagram.com/p/ABC/","id":"17900000000000001"}`)
		case r.Method == "GET" && r.URL.Path == "/v25.0/CONTAINER1":
			f.polls++
			s := "FINISHED"
			if len(f.statuses) > 0 {
				i := f.polls - 1
				if i >= len(f.statuses) {
					i = len(f.statuses) - 1
				}
				s = f.statuses[i]
			}
			fmt.Fprintf(w, `{"status_code":%q,"id":"CONTAINER1"}`, s)
		case r.Method == "POST" && (strings.HasSuffix(r.URL.Path, "/comments") || strings.HasSuffix(r.URL.Path, "/replies")):
			if f.commentErr != "" {
				w.WriteHeader(400)
				fmt.Fprint(w, f.commentErr)
				return
			}
			fmt.Fprint(w, `{"id":"18000000000000001"}`)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakeIG) paths() []string {
	var out []string
	for _, r := range f.reqs {
		out = append(out, r.Method+" "+r.Path)
	}
	return out
}

func (f *fakeIG) count(method, suffix string) int {
	n := 0
	for _, r := range f.reqs {
		if r.Method == method && strings.HasSuffix(r.Path, suffix) {
			n++
		}
	}
	return n
}

// fakeSleep stops tests from waiting and records every wait.
func fakeSleep(t *testing.T) *[]time.Duration {
	var waits []time.Duration
	old := sleep
	sleep = func(d time.Duration) { waits = append(waits, d) }
	t.Cleanup(func() { sleep = old })
	return &waits
}

func setup(t *testing.T, f *fakeIG, extra string) func(args ...string) (int, string, string) {
	t.Helper()
	t.Setenv(postlog.EnvPath, filepath.Join(t.TempDir(), "posts.jsonl")) // never the real log
	cfg := filepath.Join(t.TempDir(), "instagram.toml")
	body := fmt.Sprintf("default='main'\n[accounts.main]\naccess_token=%q\nbase_url=%q\n%s", secretToken, f.srv.URL, extra)
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

func noToken(t *testing.T, r recorded) {
	t.Helper()
	if strings.Contains(r.RawQuery, secretToken) && r.Method == "POST" {
		t.Errorf("a POST must carry the token in its body, not the URL: %s?%s", r.Path, r.RawQuery)
	}
	if r.Header.Get("Authorization") != "" {
		t.Errorf("the token is not sent as a header: %v", r.Header)
	}
}

const goodURL = "https://cdn.example.com/photo.jpg"

func TestPostLooksUpUserIDCreatesContainerThenPublishesIt(t *testing.T) {
	f := newFake(t)
	waits := fakeSleep(t)
	run := setup(t, f, "")
	code, out, errs := run("post", "--caption", "Hello é world", "--image-url", goodURL)
	if code != 0 {
		t.Fatalf("%d %s", code, errs)
	}
	want := []string{"GET /v25.0/me", "POST /v25.0/17841400000000000/media", "POST /v25.0/17841400000000000/media_publish"}
	if got := f.paths(); strings.Join(got, "|") != strings.Join(want, "|") {
		t.Fatalf("requests %v, want %v", got, want)
	}
	if q := f.reqs[0].RawQuery; !strings.Contains(q, "fields=user_id%2Cusername") || !strings.Contains(q, "access_token="+secretToken) {
		t.Errorf("GET /me carries the token in the query: %s", q)
	}
	c := f.reqs[1].Body
	if c["image_url"] != goodURL || c["caption"] != "Hello é world" || c["access_token"] != secretToken || len(c) != 3 {
		t.Errorf("container body: %v", c)
	}
	p := f.reqs[2].Body
	if p["creation_id"] != "CONTAINER1" || p["access_token"] != secretToken || len(p) != 2 {
		t.Errorf("publish must use the container's id: %v", p)
	}
	for _, r := range f.reqs[1:] {
		noToken(t, r)
		if r.Header.Get("Content-Type") != "application/json" {
			t.Errorf("content type: %v", r.Header)
		}
	}
	m := decode(t, out)
	if m["id"] != "MEDIA9" || m["container_id"] != "CONTAINER1" || m["account"] != "main" {
		t.Errorf("result: %v", m)
	}
	if strings.Contains(out, secretToken) {
		t.Error("the token must never be printed")
	}
	if len(*waits) != 0 || f.polls != 0 {
		t.Errorf("a container that publishes at once is not polled: waits=%v polls=%d", *waits, f.polls)
	}
}

func TestPostUsesConfiguredUserIDWithoutLookup(t *testing.T) {
	f := newFake(t)
	fakeSleep(t)
	run := setup(t, f, "user_id='555'\n")
	if code, _, errs := run("post", "--caption", "c", "--image-url", goodURL); code != 0 {
		t.Fatal(errs)
	}
	if f.count("GET", "/me") != 0 || f.reqs[0].Path != "/v25.0/555/media" {
		t.Errorf("user_id overrides the lookup: %v", f.paths())
	}
	// an integer in the config is accepted too
	f2 := newFake(t)
	run = setup(t, f2, "user_id=777\n")
	if code, _, errs := run("post", "--caption", "c", "--image-url", goodURL); code != 0 || f2.reqs[0].Path != "/v25.0/777/media" {
		t.Errorf("%d %s %v", code, errs, f2.paths())
	}
}

func TestPostWaitsForContainerThenPublishesAgain(t *testing.T) {
	f := newFake(t)
	waits := fakeSleep(t)
	f.publishFails = 1
	f.statuses = []string{"IN_PROGRESS", "IN_PROGRESS", "FINISHED"}
	run := setup(t, f, "user_id='555'\n")
	code, out, errs := run("post", "--caption", "c", "--image-url", goodURL)
	if code != 0 {
		t.Fatalf("%d %s", code, errs)
	}
	want := "POST media|POST media_publish|GET status|GET status|GET status|POST media_publish"
	var got []string
	for _, r := range f.reqs {
		switch {
		case strings.HasSuffix(r.Path, "/media_publish"):
			got = append(got, "POST media_publish")
		case strings.HasSuffix(r.Path, "/media"):
			got = append(got, "POST media")
		default:
			got = append(got, "GET status")
		}
	}
	if strings.Join(got, "|") != want {
		t.Fatalf("order\n got %v\nwant %s", got, want)
	}
	if f.reqs[len(f.reqs)-1].Body["creation_id"] != "CONTAINER1" {
		t.Error("second publish uses the same container")
	}
	if !strings.Contains(f.reqs[2].RawQuery, "fields=status_code") || !strings.Contains(f.reqs[2].RawQuery, "access_token="+secretToken) {
		t.Errorf("status poll: %s", f.reqs[2].RawQuery)
	}
	if len(*waits) != 3 || (*waits)[0] != 3*time.Second {
		t.Errorf("waits 3s before each poll: %v", *waits)
	}
	if decode(t, out)["id"] != "MEDIA9" {
		t.Errorf("out: %s", out)
	}
}

func TestPostContainerErrorStatusDoesNotPublishAgain(t *testing.T) {
	for _, st := range []string{"ERROR", "EXPIRED", "SOMETHING_NEW"} {
		f := newFake(t)
		fakeSleep(t)
		f.publishFails = 99
		f.statuses = []string{st}
		run := setup(t, f, "user_id='555'\n")
		code, _, errs := run("post", "--caption", "c", "--image-url", goodURL)
		if code != 1 || !strings.Contains(errs, "did not become publishable") || !strings.Contains(errs, st) {
			t.Errorf("%s: %d %s", st, code, errs)
		}
		if f.publishes != 1 {
			t.Errorf("%s: published %d times, want only the first attempt", st, f.publishes)
		}
	}
}

func TestPostGivesUpAfterFivePolls(t *testing.T) {
	f := newFake(t)
	waits := fakeSleep(t)
	f.publishFails = 99
	f.statuses = []string{"IN_PROGRESS"}
	run := setup(t, f, "user_id='555'\n")
	code, _, errs := run("post", "--caption", "c", "--image-url", goodURL)
	if code != 1 || !strings.Contains(errs, "Media ID is not available") {
		t.Errorf("the original publish error is reported: %d %s", code, errs)
	}
	if f.polls != 5 || len(*waits) != 5 || f.publishes != 1 {
		t.Errorf("polls=%d waits=%d publishes=%d", f.polls, len(*waits), f.publishes)
	}
}

func TestPostAlreadyPublishedContainer(t *testing.T) {
	f := newFake(t)
	fakeSleep(t)
	f.publishFails = 99
	f.statuses = []string{"PUBLISHED"}
	run := setup(t, f, "user_id='555'\n")
	code, _, errs := run("post", "--caption", "c", "--image-url", goodURL)
	if code != 1 || !strings.Contains(errs, "already published") || f.publishes != 1 {
		t.Errorf("%d %s publishes=%d", code, errs, f.publishes)
	}
}

func TestPostRateLimitedIsNotRetriedOrPolled(t *testing.T) {
	f := newFake(t)
	waits := fakeSleep(t)
	f.containerErr = func(w http.ResponseWriter) {
		w.Header().Set("Retry-After", "7")
		w.WriteHeader(429)
		fmt.Fprint(w, `{"error":{"message":"too many","code":4}}`)
	}
	run := setup(t, f, "user_id='555'\n")
	code, _, errs := run("post", "--caption", "c", "--image-url", goodURL)
	if code != 1 || !strings.Contains(errs, "rate limited") || !strings.Contains(errs, "7s") {
		t.Errorf("%d %s", code, errs)
	}
	if f.count("POST", "/media") != 1 || len(*waits) != 0 {
		t.Errorf("a POST is never retried: %v waits=%v", f.paths(), *waits)
	}

	// 429 on publish: no polling either
	f = newFake(t)
	waits = fakeSleep(t)
	f.publishFails, f.publishCode, f.publishErr = 99, 429, `{"error":{"message":"slow down"}}`
	run = setup(t, f, "user_id='555'\n")
	code, _, errs = run("post", "--caption", "c", "--image-url", goodURL)
	if code != 1 || !strings.Contains(errs, "rate limited") || f.polls != 0 || f.publishes != 1 || len(*waits) != 0 {
		t.Errorf("%d %s polls=%d publishes=%d", code, errs, f.polls, f.publishes)
	}
}

func TestExpiredTokenIsExplained(t *testing.T) {
	f := newFake(t)
	fakeSleep(t)
	f.meBody = ""
	cfg := filepath.Join(t.TempDir(), "c.toml")
	os.WriteFile(cfg, []byte(fmt.Sprintf("[accounts.main]\naccess_token='stale'\nbase_url=%q\n", f.srv.URL)), 0o600)
	for _, args := range [][]string{{"check"}, {"post", "--caption", "c", "--image-url", goodURL}, {"comment", "--media-id", "1", "--message", "m"}} {
		var out, errb bytes.Buffer
		code := apikit.RunArgs(service(), append(args, "--config", cfg), &out, &errb)
		e := errb.String()
		if code != 1 || !strings.Contains(e, "expired or invalid") || !strings.Contains(e, "no refresh flow") || strings.Contains(e, "stale") {
			t.Errorf("%v: %d %q", args, code, e)
		}
	}
	if f.count("POST", "/media") != 0 {
		t.Error("nothing is posted when the user id lookup fails")
	}
}

func TestPostRejectsBadInputBeforeAnyRequest(t *testing.T) {
	f := newFake(t)
	run := setup(t, f, "")
	cases := [][]string{
		{"post", "--image-url", goodURL},
		{"post", "--caption", "c"},
		{"post", "--caption", "   ", "--image-url", goodURL},
		{"post", "--caption", strings.Repeat("x", 2201), "--image-url", goodURL},
		{"post", "--caption", "c", "--image-url", "/home/me/photo.jpg"},
		{"post", "--caption", "c", "--image-url", "https://x.test/photo.png"},
		{"comment", "--media-id", "abc", "--message", "m"},
		{"comment", "--media-id", "1", "--message", ""},
		{"comment", "--message", "m"},
		{"reply", "--comment-id", "1/replies", "--message", "m"},
		{"reply", "--comment-id", "1", "--message", strings.Repeat("x", 2201)},
	}
	for _, c := range cases {
		if code, _, _ := run(c...); code == 0 {
			t.Errorf("%v should fail", c)
		}
	}
	if len(f.reqs) != 0 {
		t.Errorf("nothing may be sent: %v", f.paths())
	}
}

func TestCommentAndReply(t *testing.T) {
	f := newFake(t)
	run := setup(t, f, "")
	code, out, errs := run("comment", "--media-id", "17900000000000001", "--message", "Nice one")
	if code != 0 {
		t.Fatalf("%d %s", code, errs)
	}
	if len(f.reqs) != 1 {
		t.Fatalf("a comment needs no user id lookup: %v", f.paths())
	}
	r := f.reqs[0]
	if r.Method != "POST" || r.Path != "/v25.0/17900000000000001/comments" || r.Body["message"] != "Nice one" || r.Body["access_token"] != secretToken || len(r.Body) != 2 {
		t.Errorf("comment request: %+v", r)
	}
	noToken(t, r)
	if m := decode(t, out); m["id"] != "18000000000000001" || m["account"] != "main" {
		t.Errorf("out: %s", out)
	}

	code, out, errs = run("reply", "--comment-id", "18000000000000009", "--message", "Thanks!")
	if code != 0 {
		t.Fatalf("%d %s", code, errs)
	}
	r = f.reqs[1]
	if r.Method != "POST" || r.Path != "/v25.0/18000000000000009/replies" || r.Body["message"] != "Thanks!" || r.Body["access_token"] != secretToken || len(r.Body) != 2 {
		t.Errorf("reply request: %+v", r)
	}
	noToken(t, r)
	if decode(t, out)["id"] != "18000000000000001" {
		t.Errorf("out: %s", out)
	}
}

func TestCommentPermissionDeniedSaysWhatToDo(t *testing.T) {
	f := newFake(t)
	f.commentErr = `{"error":{"message":"(#10) Application does not have permission for this action","type":"OAuthException","code":10}}`
	run := setup(t, f, "")
	for _, args := range [][]string{{"comment", "--media-id", "1", "--message", "m"}, {"reply", "--comment-id", "1", "--message", "m"}} {
		code, _, errs := run(args...)
		if code != 1 || !strings.Contains(errs, "missing a permission") || !strings.Contains(errs, "instagram_business_manage_comments") {
			t.Errorf("%v: %d %s", args, code, errs)
		}
	}
	f.commentErr = `{"error":{"message":"Unsupported post request","code":100}}`
	if code, _, errs := run("comment", "--media-id", "1", "--message", "m"); code != 1 || !strings.Contains(errs, "Unsupported post request") || !strings.Contains(errs, "400") {
		t.Errorf("other errors keep the API's own message: %d %s", code, errs)
	}
}

func TestDryRunSendsNothingAndLeaksNothing(t *testing.T) {
	f := newFake(t)
	waits := fakeSleep(t)
	run := setup(t, f, "") // no user_id: a dry run must not look it up
	for _, args := range [][]string{
		{"post", "--caption", "Hi", "--image-url", goodURL, "--dry-run"},
		{"comment", "--media-id", "123", "--message", "Hi", "--dry-run"},
		{"reply", "--comment-id", "123", "--message", "Hi", "--dry-run"},
		{"check", "--dry-run"},
	} {
		code, out, errs := run(args...)
		if code != 0 {
			t.Fatalf("%v: %d %s", args, code, errs)
		}
		if strings.Contains(out+errs, secretToken) {
			t.Errorf("%v leaked the token: %s", args, out)
		}
	}
	if len(f.reqs) != 0 || len(*waits) != 0 {
		t.Errorf("dry run sent %v", f.paths())
	}
	_, out, _ := run("post", "--caption", "Hi", "--image-url", goodURL, "--dry-run")
	m := decode(t, out)
	steps, _ := m["steps"].([]any)
	if m["dry_run"] != true || len(steps) != 2 {
		t.Fatalf("both steps are shown: %s", out)
	}
	first := steps[0].(map[string]any)
	if first["method"] != "POST" || !strings.HasSuffix(first["url"].(string), "/media") || first["body"].(map[string]any)["caption"] != "Hi" {
		t.Errorf("step 1: %v", first)
	}
	if !strings.Contains(out, "container id from step 1") {
		t.Errorf("step 2 is described: %s", out)
	}
	// a dry run still validates
	if code, _, _ := run("post", "--caption", "", "--image-url", goodURL, "--dry-run"); code == 0 {
		t.Error("dry run must still validate")
	}
}

func TestGenericCallAndSafety(t *testing.T) {
	f := newFake(t)
	run := setup(t, f, "")
	code, out, errs := run("call", "me")
	if code != 0 || !strings.Contains(out, "robert.codes") {
		t.Fatalf("%d %s %s", code, out, errs)
	}
	code, out, errs = run("call", "comment_create", "--param", "media_id=5", "--param", "message=hey")
	if code != 0 || !strings.Contains(out, "18000000000000001") {
		t.Fatalf("%d %s %s", code, out, errs)
	}
	if r := f.reqs[1]; r.Path != "/v25.0/5/comments" || r.Body["access_token"] != secretToken {
		t.Errorf("%+v", r)
	}
	n := len(f.reqs)
	for _, name := range []string{"comment_create", "reply_create", "container_create", "media_publish"} {
		code, _, errs = run("call", "--max-safety=read", name, "--param", "x=1")
		if code == 0 {
			t.Errorf("%s must be refused under --max-safety=read", name)
		}
	}
	if code, _, _ := run("call", "--max-safety=read", "container_status", "--param", "container_id=CONTAINER1"); code != 0 {
		t.Error("a read endpoint is allowed")
	}
	if len(f.reqs) != n+1 {
		t.Errorf("only the read call was sent: %v", f.paths())
	}
}

func TestCheckReportsAccount(t *testing.T) {
	f := newFake(t)
	run := setup(t, f, "")
	code, out, errs := run("check")
	if code != 0 {
		t.Fatalf("%d %s", code, errs)
	}
	m := decode(t, out)
	d, _ := m["detail"].(map[string]any)
	if m["ok"] != true || d["user_id"] != "17841400000000000" || d["username"] != "robert.codes" {
		t.Errorf("%s", out)
	}
	if strings.Contains(out, secretToken) {
		t.Error("leak")
	}
	if len(f.reqs) != 1 {
		t.Errorf("check is one call: %v", f.paths())
	}
}

func TestTokenFromEnvironmentAndMissingToken(t *testing.T) {
	f := newFake(t)
	t.Setenv("IG_TEST_TOKEN", secretToken)
	cfg := filepath.Join(t.TempDir(), "c.toml")
	os.WriteFile(cfg, []byte(fmt.Sprintf("[accounts.main]\naccess_token_env='IG_TEST_TOKEN'\nbase_url=%q\n", f.srv.URL)), 0o600)
	var out, errb bytes.Buffer
	if code := apikit.RunArgs(service(), []string{"check", "--config", cfg}, &out, &errb); code != 0 {
		t.Errorf("%d %s", code, errb.String())
	}
	os.WriteFile(cfg, []byte("[accounts.main]\nuser_id='1'\n"), 0o600)
	out.Reset()
	errb.Reset()
	if code := apikit.RunArgs(service(), []string{"check", "--config", cfg}, &out, &errb); code != 1 || !strings.Contains(errb.String(), "access_token") {
		t.Errorf("%d %s", code, errb.String())
	}
}

func TestSharedConfigTable(t *testing.T) {
	f := newFake(t)
	dir := t.TempDir()
	cfg := filepath.Join(dir, "config.toml")
	body := fmt.Sprintf("[instagram]\ndefault='work'\n[instagram.accounts.work]\naccess_token=%q\nuser_id='42'\nbase_url=%q\n[instagram.accounts.other]\naccess_token='x'\n", secretToken, f.srv.URL)
	os.WriteFile(cfg, []byte(body), 0o600)
	fakeSleep(t)
	var out, errb bytes.Buffer
	code := apikit.RunArgs(service(), []string{"post", "--caption", "c", "--image-url", goodURL, "--config", cfg}, &out, &errb)
	if code != 0 || f.reqs[0].Path != "/v25.0/42/media" || decode(t, out.String())["account"] != "work" {
		t.Errorf("%d %s %s", code, out.String(), errb.String())
	}
}
