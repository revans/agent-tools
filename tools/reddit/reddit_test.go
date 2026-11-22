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
	"sync/atomic"
	"testing"

	"github.com/revans/agent-tools/apikit"
)

func TestParsePostID(t *testing.T) {
	ok := map[string]string{
		"1abc2d":    "1abc2d",
		"t3_1abc2d": "1abc2d",
		"https://www.reddit.com/r/golang/comments/1abc2d/some_title/": "1abc2d",
		"https://old.reddit.com/r/x/comments/1abc2d":                  "1abc2d",
		"https://redd.it/1abc2d":                                      "1abc2d",
		"  1abc2d  ":                                                  "1abc2d",
	}
	for in, want := range ok {
		if got, err := parsePostID(in); err != nil || got != want {
			t.Errorf("%q: got %q, %v", in, got, err)
		}
	}
	for _, in := range []string{"", "   ", "Has Caps", "../etc", "toolongtoolongid", "https://reddit.com/", "https://reddit.com/r/golang/", "http://[bad"} {
		if _, err := parsePostID(in); err == nil {
			t.Errorf("%q should be rejected", in)
		}
	}
}

func TestValidateSubreddit(t *testing.T) {
	for in, want := range map[string]string{"golang": "golang", "r/golang": "golang", "/r/golang": "golang", "ab_c": "ab_c"} {
		if got, err := validateSubreddit(in); err != nil || got != want {
			t.Errorf("%q: %q %v", in, got, err)
		}
	}
	for _, in := range []string{"", "ab", "a-b-c", "has space", "waytoolongsubredditnamex", "../x", "r/"} {
		if _, err := validateSubreddit(in); err == nil {
			t.Errorf("%q should be rejected", in)
		}
	}
}

func TestTruncateText(t *testing.T) {
	if truncateText("short") != "short" {
		t.Error("short text must pass through")
	}
	long := strings.Repeat("é", 2500)
	got := truncateText(long)
	if !strings.HasSuffix(got, "...[truncated]") || len([]rune(got)) != 2000+len([]rune("...[truncated]")) {
		t.Errorf("truncation counts characters, not bytes: %d", len([]rune(got)))
	}
}

// fakeReddit speaks enough of Reddit's API to check what the tool sends.
type fakeReddit struct {
	srv        *httptest.Server
	tokenCalls int32
	sawPaths   []string
}

func newFake(t *testing.T) *fakeReddit {
	f := &fakeReddit{}
	f.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/token" {
			atomic.AddInt32(&f.tokenCalls, 1)
			u, p, _ := r.BasicAuth()
			r.ParseForm()
			if u != "cid" || p != "csecret" || r.PostForm.Get("grant_type") != "client_credentials" || !strings.Contains(r.UserAgent(), "agent-tools-reddit") {
				http.Error(w, `{"error":"unauthorized"}`, 401)
				return
			}
			w.Write([]byte(`{"access_token":"TOK","token_type":"bearer"}`))
			return
		}
		f.sawPaths = append(f.sawPaths, r.URL.RequestURI())
		if r.Header.Get("Authorization") != "Bearer TOK" || !strings.Contains(r.UserAgent(), "agent-tools-reddit") {
			http.Error(w, "no", 401)
			return
		}
		q := r.URL.Query()
		if q.Get("raw_json") != "1" {
			t.Errorf("raw_json=1 is required so text is not HTML-escaped: %s", r.URL)
		}
		switch {
		case r.URL.Path == "/search" || r.URL.Path == "/r/golang/search":
			if r.URL.Path == "/r/golang/search" && q.Get("restrict_sr") != "true" {
				t.Errorf("subreddit search must set restrict_sr=true: %s", r.URL)
			}
			after := "null"
			if q.Get("q") == "more" {
				after = `"t3_next"`
			}
			fmt.Fprintf(w, `{"data":{"after":%s,"children":[
			 {"kind":"t3","data":{"id":"a1","title":"Safe","subreddit":"golang","score":12,"num_comments":3,"permalink":"/r/golang/comments/a1/safe/","over_18":false}},
			 {"kind":"t3","data":{"id":"n1","title":"Nsfw","subreddit":"x","score":99,"num_comments":9,"permalink":"/r/x/comments/n1/","over_18":true}},
			 {"kind":"t3","data":{"id":"a2","title":"Also safe","subreddit":"golang","score":5,"num_comments":0,"permalink":"/r/golang/comments/a2/also/"}}]}}`, after)
		case r.URL.Path == "/r/nosuch/search":
			http.Error(w, `{"message":"Not Found","error":404}`, 404)
		case r.URL.Path == "/comments/selfpost":
			if q.Get("sort") != "top" || q.Get("limit") != "100" {
				t.Errorf("comment query: %s", r.URL)
			}
			long := strings.Repeat("x", 2500)
			fmt.Fprintf(w, `[{"data":{"children":[{"kind":"t3","data":{"id":"selfpost","title":"Hello","subreddit":"golang","score":7,"author":"op","is_self":true,"selftext":"body text","url":"https://reddit.com/ignored"}}]}},
			 {"data":{"children":[
			  {"kind":"t1","data":{"author":"low","body":"low","score":1}},
			  {"kind":"t1","data":{"author":"high","body":%q,"score":50}},
			  {"kind":"more","data":{"children":["zz"]}},
			  {"kind":"t1","data":{"author":"mid","body":"mid","score":10}}]}}]`, long)
		case r.URL.Path == "/comments/manycomments":
			var kids []string
			for i := 1; i <= 30; i++ {
				kids = append(kids, fmt.Sprintf(`{"kind":"t1","data":{"author":"u%d","body":"c%d","score":%d}}`, i, i, i))
			}
			fmt.Fprintf(w, `[{"data":{"children":[{"kind":"t3","data":{"id":"manycomments","title":"Busy","is_self":true,"selftext":"s"}}]}},{"data":{"children":[%s]}}]`, strings.Join(kids, ","))
		case r.URL.Path == "/comments/linkpost":
			fmt.Fprint(w, `[{"data":{"children":[{"kind":"t3","data":{"id":"linkpost","title":"A link","subreddit":"golang","score":1,"author":"op","is_self":false,"url":"https://example.com/x"}}]}},{"data":{"children":[]}}]`)
		case r.URL.Path == "/comments/nsfwpost":
			fmt.Fprint(w, `[{"data":{"children":[{"kind":"t3","data":{"id":"nsfwpost","title":"x","over_18":true}}]}},{"data":{"children":[]}}]`)
		case r.URL.Path == "/comments/gone":
			http.Error(w, `{"message":"Forbidden"}`, 403)
		case r.URL.Path == "/comments/empty":
			fmt.Fprint(w, `[]`)
		case r.URL.Path == "/r/golang/about":
			fmt.Fprint(w, `{"kind":"t5","data":{"display_name":"golang","title":"Go","public_description":"The Go language","subscribers":123456,"over18":false,"created_utc":1.2e9,"secret_internal":"x"}}`)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(f.srv.Close)
	return f
}

func setup(t *testing.T, f *fakeReddit, extra string) (run func(args ...string) (int, string, string)) {
	t.Helper()
	cfg := filepath.Join(t.TempDir(), "reddit.toml")
	body := fmt.Sprintf("default='main'\n[accounts.main]\nclient_id='cid'\nclient_secret='csecret'\nbase_url=%q\ntoken_url=%q\n%s", f.srv.URL, f.srv.URL+"/token", extra)
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

func TestSearchShapesAndFiltersNSFW(t *testing.T) {
	f := newFake(t)
	run := setup(t, f, "")
	code, out, errs := run("search", "--query", "go modules")
	if code != 0 {
		t.Fatalf("%d %s", code, errs)
	}
	m := decode(t, out)
	res := m["results"].([]any)
	if len(res) != 2 || m["results_truncated"] != false {
		t.Fatalf("NSFW must be dropped, nothing truncated: %v", m)
	}
	first := res[0].(map[string]any)
	if first["id"] != "a1" || first["comment_count"].(float64) != 3 || first["permalink"] != "/r/golang/comments/a1/safe/" || len(first) != 6 {
		t.Errorf("shape: %v", first)
	}
	if !strings.Contains(f.sawPaths[0], "sort=relevance") || !strings.Contains(f.sawPaths[0], "limit=10") || !strings.Contains(f.sawPaths[0], "q=go+modules") {
		t.Errorf("query: %s", f.sawPaths[0])
	}
	_, out, _ = run("search", "--query", "more")
	if decode(t, out)["results_truncated"] != true {
		t.Error("a non-null cursor means more results exist")
	}
	if f.tokenCalls != 2 {
		t.Errorf("one token per invocation, got %d for two runs", f.tokenCalls)
	}
}

func TestSearchInSubredditAndErrors(t *testing.T) {
	f := newFake(t)
	run := setup(t, f, "")
	if code, out, _ := run("search", "--query", "x", "--subreddit", "r/golang"); code != 0 || len(decode(t, out)["results"].([]any)) != 2 {
		t.Errorf("subreddit search: %d %s", code, out)
	}
	if code, _, e := run("search", "--query", "x", "--subreddit", "nosuch"); code != 1 || !strings.Contains(e, "private/quarantined/inaccessible") || !strings.Contains(e, "r/nosuch") {
		t.Errorf("404 subreddit: %d %q", code, e)
	}
	if code, _, e := run("search", "--query", "x", "--subreddit", "a b"); code != 1 || !strings.Contains(e, "not a valid subreddit") {
		t.Errorf("invalid subreddit: %d %q", code, e)
	}
	if code, _, _ := run("search"); code != 2 {
		t.Errorf("missing --query is a usage error: %d", code)
	}
	if code, _, _ := run("search", "--query", "   "); code != 2 {
		t.Errorf("blank --query is a usage error: %d", code)
	}
}

func TestFetchSelfPost(t *testing.T) {
	run := setup(t, newFake(t), "")
	code, out, errs := run("fetch", "https://www.reddit.com/r/golang/comments/selfpost/hello/")
	if code != 0 {
		t.Fatalf("%d %s", code, errs)
	}
	m := decode(t, out)
	if m["content_type"] != "self" || m["content"] != "body text" || m["author"] != "op" || m["score"].(float64) != 7 {
		t.Errorf("post: %v", m)
	}
	cs := m["comments"].([]any)
	if len(cs) != 3 {
		t.Fatalf("the 'more' stub must be skipped: %d comments", len(cs))
	}
	order := []string{}
	for _, c := range cs {
		order = append(order, c.(map[string]any)["author"].(string))
	}
	if strings.Join(order, ",") != "high,mid,low" {
		t.Errorf("comments are sorted by score: %v", order)
	}
	if body := cs[0].(map[string]any)["body"].(string); !strings.HasSuffix(body, "...[truncated]") {
		t.Errorf("a 2500-character comment must be truncated")
	}
}

func TestFetchLinkNSFWAndMissing(t *testing.T) {
	run := setup(t, newFake(t), "")
	_, out, _ := run("fetch", "t3_linkpost")
	if m := decode(t, out); m["content_type"] != "link" || m["content"] != "https://example.com/x" {
		t.Errorf("link post: %v", m)
	}
	if code, _, e := run("fetch", "nsfwpost"); code != 1 || !strings.Contains(e, "NSFW") {
		t.Errorf("nsfw: %d %q", code, e)
	}
	if code, _, e := run("fetch", "gone"); code != 1 || !strings.Contains(e, "private/quarantined/inaccessible") {
		t.Errorf("403: %d %q", code, e)
	}
	if code, _, e := run("fetch", "empty"); code != 1 || !strings.Contains(e, "no Reddit post found") {
		t.Errorf("empty listing: %d %q", code, e)
	}
	if code, _, _ := run("fetch"); code != 2 {
		t.Errorf("no argument: %d", code)
	}
	if code, _, _ := run("fetch", "Not A Valid ID"); code != 1 {
		t.Errorf("bad id: %d", code)
	}
}

func TestGenericCallAndNewEndpointWithoutCode(t *testing.T) {
	f := newFake(t)
	run := setup(t, f, "")
	code, out, _ := run("call", "subreddit_about", "--param", "subreddit=golang")
	m := decode(t, out)
	if code != 0 || m["display_name"] != "golang" || m["subscribers"].(float64) != 123456 || m["secret_internal"] != nil {
		t.Errorf("call + field projection: %d %v", code, m)
	}
	if code, _, e := run("call", "subreddit_listing", "--param", "subreddit=golang", "--param", "listing=controversial"); code != 1 || !strings.Contains(e, "must be one of") {
		t.Errorf("enum on a path param: %d %q", code, e)
	}

	// A new endpoint is just a TOML file in endpoints.d: no code, no rebuild.
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "mine.toml"), []byte(`
[[endpoint]]
name = "about_again"
description = "Same thing as subreddit_about, added by the user."
method = "GET"
path = "/r/{subreddit}/about"
safety = "read"
select = "data.display_name"
  [endpoint.fixed]
  raw_json = "1"
  [[endpoint.param]]
  name = "subreddit"
  in = "path"
  required = true
  description = "Subreddit."
`), 0o644)
	t.Setenv("REDDIT_ENDPOINTS", dir)
	if code, out, _ := run("call", "about_again", "--param", "subreddit=golang"); code != 0 || strings.TrimSpace(out) != `"golang"` {
		t.Errorf("user endpoint: %d %q", code, out)
	}
	if _, out, _ := run("endpoints"); !strings.Contains(out, "about_again") {
		t.Errorf("a user endpoint must be listed: %s", out)
	}
}

func TestDryRunNeverSendsOrFetchesToken(t *testing.T) {
	f := newFake(t)
	run := setup(t, f, "")
	code, out, _ := run("search", "--query", "x", "--dry-run")
	m := decode(t, out)
	if code != 0 || m["dry_run"] != true || len(f.sawPaths) != 0 || f.tokenCalls != 0 {
		t.Errorf("dry run touched the network: %d %v paths=%v tokens=%d", code, m, f.sawPaths, f.tokenCalls)
	}
	if strings.Contains(out, "TOK") || strings.Contains(out, "csecret") {
		t.Errorf("dry run leaked a secret: %s", out)
	}
}

func TestCheckAndBadCredentials(t *testing.T) {
	f := newFake(t)
	run := setup(t, f, "")
	if code, out, e := run("check"); code != 0 || decode(t, out)["ok"] != true {
		t.Errorf("check: %d %s %s", code, out, e)
	}
	bad := filepath.Join(t.TempDir(), "bad.toml")
	os.WriteFile(bad, []byte(fmt.Sprintf("[accounts.main]\nclient_id='cid'\nclient_secret='WRONG'\nbase_url=%q\ntoken_url=%q\n", f.srv.URL, f.srv.URL+"/token")), 0o600)
	var out, errb bytes.Buffer
	code := apikit.RunArgs(service(), []string{"check", "--config", bad}, &out, &errb)
	if code != 1 || !strings.Contains(errb.String(), "token request") || strings.Contains(errb.String(), "WRONG") {
		t.Errorf("bad secret: %d %q", code, errb.String())
	}
}

func TestSecretsFromEnvironment(t *testing.T) {
	f := newFake(t)
	cfg := filepath.Join(t.TempDir(), "env.toml")
	os.WriteFile(cfg, []byte(fmt.Sprintf("[accounts.main]\nclient_id_env='RD_ID'\nclient_secret_command='echo csecret'\nbase_url=%q\ntoken_url=%q\n", f.srv.URL, f.srv.URL+"/token")), 0o600)
	t.Setenv("RD_ID", "cid")
	var out, errb bytes.Buffer
	if code := apikit.RunArgs(service(), []string{"search", "--query", "x", "--config", cfg}, &out, &errb); code != 0 {
		t.Errorf("env and command secrets: %d %s", code, errb.String())
	}
}

func TestFetchKeepsTheTwentyBestComments(t *testing.T) {
	run := setup(t, newFake(t), "")
	_, out, _ := run("fetch", "manycomments")
	cs := decode(t, out)["comments"].([]any)
	if len(cs) != 20 {
		t.Fatalf("at most 20 comments are returned, got %d", len(cs))
	}
	if top, last := cs[0].(map[string]any), cs[19].(map[string]any); top["score"].(float64) != 30 || last["score"].(float64) != 11 {
		t.Errorf("the best 20, best first: top=%v last=%v", top["score"], last["score"])
	}
}
