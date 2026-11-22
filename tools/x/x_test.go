package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/revans/agent-tools/apikit"
)

// The four credentials the tests use. The two secrets must never appear in any
// output, header on the wire, or error.
const (
	apiKey       = "CONSUMERKEY123"
	apiSecret    = "SECRET-consumer-9f8e"
	accessToken  = "ACCESSTOKEN456"
	accessSecret = "SECRET-token-1a2b"
)

type seen struct {
	method, path, rawQuery, auth, contentType string
	body                                      []byte
}

func (s seen) query() url.Values {
	v, _ := url.ParseQuery(s.rawQuery)
	return v
}

// fakeX speaks enough of X API v2 to check what the tool sends.
type fakeX struct {
	srv  *httptest.Server
	mu   sync.Mutex
	reqs []seen
	t    *testing.T
}

func (f *fakeX) log() []seen {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]seen(nil), f.reqs...)
}

func (f *fakeX) count() int { return len(f.log()) }

func (f *fakeX) paths() []string {
	var p []string
	for _, r := range f.log() {
		p = append(p, r.method+" "+r.path)
	}
	return p
}

func (f *fakeX) last() seen {
	l := f.log()
	if len(l) == 0 {
		f.t.Fatal("no request reached the server")
	}
	return l[len(l)-1]
}

var (
	authFieldRe = regexp.MustCompile(`(\w+)="([^"]*)"`)
)

// checkAuth asserts that the request carries a well-formed OAuth 1.0a header.
func (f *fakeX) checkAuth(r *http.Request) {
	h := r.Header.Get("Authorization")
	if !strings.HasPrefix(h, "OAuth ") {
		f.t.Errorf("%s %s: Authorization must start with \"OAuth \", got %q", r.Method, r.URL.Path, h)
		return
	}
	got := map[string]string{}
	for _, m := range authFieldRe.FindAllStringSubmatch(h, -1) {
		got[m[1]] = m[2]
	}
	want := map[string]string{
		"oauth_consumer_key":     apiKey,
		"oauth_token":            accessToken,
		"oauth_signature_method": "HMAC-SHA1",
		"oauth_version":          "1.0",
	}
	for k, v := range want {
		if got[k] != v {
			f.t.Errorf("%s %s: %s = %q, want %q (header %s)", r.Method, r.URL.Path, k, got[k], v, h)
		}
	}
	for _, k := range []string{"oauth_nonce", "oauth_timestamp", "oauth_signature"} {
		if got[k] == "" {
			f.t.Errorf("%s %s: header lacks %s: %s", r.Method, r.URL.Path, k, h)
		}
	}
	if strings.Contains(h, apiSecret) || strings.Contains(h, accessSecret) {
		f.t.Errorf("the header carries a secret: %s", h)
	}
}

func post(id string, like, rt, reply, quote, bm, imp int) string {
	return fmt.Sprintf(`{"id":%q,"text":"text %s","author_id":"42","created_at":"2024-01-0%sT00:00:00.000Z","public_metrics":{"like_count":%d,"retweet_count":%d,"reply_count":%d,"quote_count":%d,"bookmark_count":%d,"impression_count":%d}}`,
		id, id, id[len(id)-1:], like, rt, reply, quote, bm, imp)
}

func newFake(t *testing.T) *fakeX {
	f := &fakeX{t: t}
	f.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		f.mu.Lock()
		f.reqs = append(f.reqs, seen{r.Method, r.URL.Path, r.URL.RawQuery, r.Header.Get("Authorization"), r.Header.Get("Content-Type"), body})
		f.mu.Unlock()
		f.checkAuth(r)
		q := r.URL.Query()
		const fields = "created_at,public_metrics,author_id"
		switch {
		case r.Method == "GET" && r.URL.Path == "/2/tweets/search/recent":
			if q.Get("max_results") != "100" || q.Get("tweet.fields") != fields {
				t.Errorf("search query: %s", r.URL.RawQuery)
			}
			switch qs := q.Get("query"); {
			case qs == "ratelimit":
				w.Header().Set("Retry-After", "30")
				http.Error(w, `{"title":"Too Many Requests","detail":"Too Many Requests"}`, 429)
			case qs == "unauth":
				http.Error(w, `{"title":"Unauthorized","detail":"Unauthorized","status":401}`, 401)
			case qs == "forbidden":
				http.Error(w, `{"title":"Forbidden","detail":"Your app is not enrolled in the right access tier."}`, 403)
			case qs == "empty":
				fmt.Fprint(w, `{"meta":{"result_count":0}}`)
			case qs == "more":
				fmt.Fprintf(w, `{"data":[%s],"meta":{"next_token":"NT"}}`, post("101", 1, 2, 3, 4, 5, 6))
			case strings.HasPrefix(qs, "conversation_id:"):
				conv := strings.TrimPrefix(qs, "conversation_id:")
				if conv == "100" {
					fmt.Fprintf(w, `{"data":[%s,%s,%s],"meta":{"next_token":"NT"}}`, post("100", 9, 9, 9, 9, 9, 9), post("201", 1, 0, 0, 0, 0, 10), post("202", 2, 0, 0, 0, 0, 20))
				} else {
					fmt.Fprint(w, `{"meta":{"result_count":0}}`)
				}
			default:
				fmt.Fprintf(w, `{"data":[%s,%s,{"id":"103","text":"bare"}],"meta":{"result_count":3}}`, post("101", 1, 2, 3, 4, 5, 6), post("102", 10, 20, 30, 40, 50, 60))
			}
		case r.Method == "GET" && r.URL.Path == "/2/users/by/username/alice":
			fmt.Fprint(w, `{"data":{"id":"42","name":"Alice","username":"alice"}}`)
		case r.Method == "GET" && r.URL.Path == "/2/users/by/username/bob":
			fmt.Fprint(w, `{"data":{"id":"43","name":"Bob","username":"bob"}}`)
		case r.Method == "GET" && r.URL.Path == "/2/users/by/username/nobody":
			fmt.Fprint(w, `{"errors":[{"detail":"Could not find user with username: [nobody].","title":"Not Found Error"}]}`)
		case r.Method == "GET" && r.URL.Path == "/2/users/by/username/gone":
			http.Error(w, `{"title":"Not Found Error","detail":"nope"}`, 404)
		case r.Method == "GET" && (r.URL.Path == "/2/users/42/tweets" || r.URL.Path == "/2/users/43/tweets"):
			if q.Get("max_results") != "100" || q.Get("tweet.fields") != fields {
				t.Errorf("timeline query: %s", r.URL.RawQuery)
			}
			if r.URL.Path == "/2/users/43/tweets" {
				fmt.Fprintf(w, `{"data":[%s],"meta":{"next_token":"NT"}}`, post("301", 6, 0, 0, 0, 0, 100))
				return
			}
			fmt.Fprintf(w, `{"data":[%s,%s,%s,%s]}`, post("11", 10, 4, 2, 0, 1, 100), post("12", 20, 6, 4, 2, 3, 200), post("13", 0, 0, 0, 0, 0, 0), post("14", 30, 10, 6, 4, 4, 300))
		case r.Method == "GET" && r.URL.Path == "/2/tweets/100":
			if q.Get("tweet.fields") != fields+",conversation_id" {
				t.Errorf("tweet query: %s", r.URL.RawQuery)
			}
			fmt.Fprintf(w, `{"data":{"id":"100","text":"root","author_id":"42","created_at":"2024-01-01T00:00:00.000Z","conversation_id":"100","public_metrics":{"like_count":7,"retweet_count":1,"reply_count":2,"quote_count":0,"bookmark_count":3,"impression_count":99}}}`)
		case r.Method == "GET" && r.URL.Path == "/2/tweets/150":
			// a reply: its conversation is the root's
			fmt.Fprint(w, `{"data":{"id":"150","text":"a reply","conversation_id":"100"}}`)
		case r.Method == "GET" && r.URL.Path == "/2/tweets/999":
			http.Error(w, `{"title":"Not Found Error","detail":"Could not find tweet with id: [999]."}`, 404)
		case r.Method == "GET" && r.URL.Path == "/2/tweets/998":
			fmt.Fprint(w, `{"errors":[{"detail":"Could not find tweet with ids: [998].","title":"Not Found Error"}]}`)
		case r.Method == "GET" && r.URL.Path == "/2/users/me":
			if q.Get("user.fields") != "public_metrics" {
				t.Errorf("me query: %s", r.URL.RawQuery)
			}
			fmt.Fprint(w, `{"data":{"id":"7","name":"Me","username":"me_user","public_metrics":{"followers_count":1000,"following_count":200,"tweet_count":3000,"listed_count":12,"like_count":55,"media_count":8}}}`)
		case r.Method == "POST" && r.URL.Path == "/2/media/upload":
			mt, params, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
			if err != nil || mt != "multipart/form-data" {
				t.Errorf("upload content type %q", r.Header.Get("Content-Type"))
				return
			}
			mr := multipart.NewReader(bytes.NewReader(body), params["boundary"])
			parts := map[string]string{}
			for {
				p, err := mr.NextPart()
				if err != nil {
					break
				}
				b, _ := io.ReadAll(p)
				parts[p.FormName()] = string(b)
				if p.FormName() == "media" {
					parts["media.filename"] = p.FileName()
					parts["media.type"] = p.Header.Get("Content-Type")
				}
			}
			if parts["media_category"] != "tweet_image" || parts["media"] == "" || parts["media.filename"] != "pic.PNG" || parts["media.type"] != "image/png" {
				t.Errorf("upload parts: category=%q media bytes=%d filename=%q type=%q", parts["media_category"], len(parts["media"]), parts["media.filename"], parts["media.type"])
			}
			if strings.Contains(parts["media"], "SECRET") {
				t.Error("secret in upload")
			}
			if strings.Contains(parts["media"], "REJECT") {
				http.Error(w, `{"detail":"Unsupported image."}`, 400)
				return
			}
			if strings.Contains(parts["media"], "NOID") {
				fmt.Fprint(w, `{"data":{}}`)
				return
			}
			fmt.Fprint(w, `{"data":{"id":"M1","media_key":"3_1"}}`)
		case r.Method == "POST" && r.URL.Path == "/2/tweets":
			if r.Header.Get("Content-Type") != "application/json" {
				t.Errorf("create post content type %q", r.Header.Get("Content-Type"))
			}
			var in map[string]any
			json.Unmarshal(body, &in)
			switch in["text"] {
			case "forbidden":
				http.Error(w, `{"title":"Forbidden","detail":"You are not permitted to create a Tweet."}`, 403)
			case "rate":
				http.Error(w, `{"detail":"Too Many Requests"}`, 429)
			case "nodata":
				fmt.Fprint(w, `{}`)
			case "notext":
				fmt.Fprint(w, `{"data":{"id":"555"}}`)
			default:
				fmt.Fprintf(w, `{"data":{"id":"555","text":%q,"edit_history_tweet_ids":["555"]}}`, in["text"])
			}
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(f.srv.Close)
	return f
}

type env struct {
	f     *fakeX
	t     *testing.T
	state string
	cfg   string
}

func setup(t *testing.T, f *fakeX, extra string) *env {
	t.Helper()
	// Never touch the real config or state.
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	e := &env{f: f, t: t, state: t.TempDir(), cfg: filepath.Join(t.TempDir(), "x.toml")}
	body := fmt.Sprintf("default='main'\n[accounts.main]\napi_key=%q\napi_secret=%q\naccess_token=%q\naccess_token_secret=%q\nbase_url=%q\nstate_dir=%q\n%s",
		apiKey, apiSecret, accessToken, accessSecret, f.srv.URL+"/2", e.state, extra)
	if err := os.WriteFile(e.cfg, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return e
}

func (e *env) run(args ...string) (int, string, string) {
	var out, errb bytes.Buffer
	code := apikit.RunArgs(service(), append(args, "--config", e.cfg), &out, &errb)
	return code, out.String(), errb.String()
}

func (e *env) ok(args ...string) map[string]any {
	e.t.Helper()
	code, out, errs := e.run(args...)
	if code != 0 {
		e.t.Fatalf("x %v: exit %d: %s", args, code, errs)
	}
	return decode(e.t, out)
}

func (e *env) fail(wantCode int, wantMsg string, args ...string) {
	e.t.Helper()
	code, out, errs := e.run(args...)
	if code != wantCode || !strings.Contains(errs, wantMsg) {
		e.t.Errorf("x %v: want exit %d containing %q, got %d stderr %q stdout %q", args, wantCode, wantMsg, code, errs, out)
	}
	for _, s := range []string{apiSecret, accessSecret} {
		if strings.Contains(errs+out, s) {
			e.t.Errorf("x %v leaked a secret", args)
		}
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

func list(t *testing.T, v any) []any {
	t.Helper()
	l, ok := v.([]any)
	if !ok {
		t.Fatalf("not a list: %v", v)
	}
	return l
}

// ---- validation ----

func TestValidateUsername(t *testing.T) {
	for in, want := range map[string]string{"alice": "alice", "@alice": "alice", "  @a_b9 ": "a_b9", "123456789012345": "123456789012345"} {
		if got := normalizeUsername(in); got != want || validateUsername(got) != nil {
			t.Errorf("%q: %q %v", in, got, validateUsername(got))
		}
	}
	for _, in := range []string{"", "has space", "../x", "a/b", "a?b=c", "1234567890123456", "ünï", "a-b", "@@a"} {
		if validateUsername(normalizeUsername(in)) == nil {
			t.Errorf("%q should be rejected", in)
		}
	}
}

func TestValidateTweetID(t *testing.T) {
	for _, in := range []string{"1", "20", "12345678901234567890"} {
		if validateTweetID(in) != nil {
			t.Errorf("%q should pass", in)
		}
	}
	for _, in := range []string{"", "12a", "-1", "1.5", "123456789012345678901", "../x", "1/2", " 1"} {
		if validateTweetID(in) == nil {
			t.Errorf("%q should be rejected", in)
		}
	}
}

// ---- search ----

func TestSearchShapeAndRequest(t *testing.T) {
	f := newFake(t)
	e := setup(t, f, "")
	m := e.ok("search", "--query", "go lang")
	res := list(t, m["results"])
	if len(res) != 3 || m["results_truncated"] != false {
		t.Fatalf("results: %v", m)
	}
	first := res[0].(map[string]any)
	want := map[string]float64{"like_count": 1, "repost_count": 2, "reply_count": 3, "quote_count": 4, "bookmark_count": 5, "impression_count": 6}
	for k, v := range want {
		if first[k] != v {
			t.Errorf("%s = %v, want %v", k, first[k], v)
		}
	}
	for _, k := range []string{"id", "text", "author_id", "created_at"} {
		if _, ok := first[k]; !ok {
			t.Errorf("missing %s", k)
		}
	}
	if len(first) != 10 {
		t.Errorf("exactly the Mimir fields: %v", first)
	}
	if _, ok := first["retweet_count"]; ok {
		t.Error("retweet_count must be reported as repost_count")
	}
	bare := res[2].(map[string]any)
	if bare["like_count"] != float64(0) || bare["impression_count"] != float64(0) || bare["author_id"] != nil {
		t.Errorf("a post with no metrics reports 0, not an error: %v", bare)
	}
	if q := f.last().query(); q["query"][0] != "go lang" || q["max_results"][0] != "100" {
		t.Errorf("query: %v", q)
	}
	if got := f.paths(); len(got) != 1 || got[0] != "GET /2/tweets/search/recent" {
		t.Errorf("paths: %v", got)
	}
}

func TestSearchTruncatedAndEmpty(t *testing.T) {
	e := setup(t, newFake(t), "")
	if m := e.ok("search", "--query", "more"); m["results_truncated"] != true {
		t.Errorf("a next_token means more exist: %v", m)
	}
	m := e.ok("search", "--query", "empty")
	if len(list(t, m["results"])) != 0 || m["results_truncated"] != false {
		t.Errorf("no data is an empty list: %v", m)
	}
}

func TestSearchNeedsAQuery(t *testing.T) {
	f := newFake(t)
	e := setup(t, f, "")
	e.fail(2, "--query is required", "search")
	e.fail(2, "--query is required", "search", "--query", "   ")
	if f.count() != 0 {
		t.Error("nothing should be sent")
	}
}

// ---- error paths ----

func TestHTTPErrors(t *testing.T) {
	f := newFake(t)
	e := setup(t, f, "")
	e.fail(1, "HTTP 401: Unauthorized", "search", "--query", "unauth")
	e.fail(1, "HTTP 403: Your app is not enrolled", "search", "--query", "forbidden")
	n := f.count()
	e.fail(1, "rate limited", "search", "--query", "ratelimit")
	if f.count() != n+1 {
		t.Errorf("a rate-limited read is sent once, not retried: %d requests", f.count()-n)
	}
}

func TestWriteErrorsAreNotRetried(t *testing.T) {
	f := newFake(t)
	e := setup(t, f, "")
	e.fail(1, "HTTP 403: You are not permitted to create a Tweet.", "post", "--text", "forbidden")
	e.fail(1, "rate limited", "post", "--text", "rate")
	if f.count() != 2 {
		t.Errorf("a POST must never be repeated: %v", f.paths())
	}
	e.fail(1, "did not include the created post", "post", "--text", "nodata")
}

// ---- timeline ----

func TestTimeline(t *testing.T) {
	f := newFake(t)
	e := setup(t, f, "")
	m := e.ok("timeline", "--username", "@alice")
	if m["username"] != "alice" || m["user_id"] != "42" || m["results_truncated"] != false || len(list(t, m["results"])) != 4 {
		t.Errorf("timeline: %v", m)
	}
	if got := strings.Join(f.paths(), ","); got != "GET /2/users/by/username/alice,GET /2/users/42/tweets" {
		t.Errorf("two calls in order: %s", got)
	}
	if m := e.ok("timeline", "--username", "bob"); m["results_truncated"] != true {
		t.Errorf("a next_token means more exist: %v", m)
	}
}

func TestTimelineRejectsBadUsernamesBeforeAnyRequest(t *testing.T) {
	f := newFake(t)
	e := setup(t, f, "")
	e.fail(1, "is not a valid X username", "timeline", "--username", "../users/me")
	e.fail(1, "is not a valid X username", "timeline", "--username", "a?b")
	e.fail(1, "is not a valid X username", "timeline", "--username", "waytoolongusername1")
	e.fail(2, "--username is required", "timeline", "--username", "@")
	e.fail(2, "--username is required", "timeline")
	if f.count() != 0 {
		t.Errorf("nothing should be sent: %v", f.paths())
	}
}

func TestTimelineUserNotFound(t *testing.T) {
	f := newFake(t)
	e := setup(t, f, "")
	e.fail(1, "no X account found for username: nobody", "timeline", "--username", "nobody")
	e.fail(1, "no X account found for username: gone", "timeline", "--username", "gone")
	if f.count() != 2 {
		t.Errorf("the timeline must not be fetched for an unknown user: %v", f.paths())
	}
}

// ---- thread ----

func TestThread(t *testing.T) {
	f := newFake(t)
	e := setup(t, f, "")
	m := e.ok("thread", "100")
	tw := m["tweet"].(map[string]any)
	if tw["id"] != "100" || tw["like_count"] != float64(7) || tw["bookmark_count"] != float64(3) || tw["impression_count"] != float64(99) {
		t.Errorf("tweet: %v", tw)
	}
	replies := list(t, m["replies"])
	if len(replies) != 2 || m["replies_truncated"] != true {
		t.Fatalf("the post itself is not one of its replies: %v", m)
	}
	for _, r := range replies {
		if r.(map[string]any)["id"] == "100" {
			t.Error("the original post leaked into the replies")
		}
	}
	reqs := f.log()
	if len(reqs) != 2 || reqs[1].query()["query"][0] != "conversation_id:100" || reqs[1].query()["max_results"][0] != "100" {
		t.Errorf("requests: %v", f.paths())
	}
}

func TestThreadUsesTheConversationOfAReply(t *testing.T) {
	f := newFake(t)
	e := setup(t, f, "")
	m := e.ok("thread", "150")
	// The reply search is for the root's conversation (100), not for 150.
	if q := f.last().query()["query"][0]; q != "conversation_id:100" {
		t.Errorf("query = %s", q)
	}
	if len(list(t, m["replies"])) != 3 {
		t.Errorf("the replies of conversation 100 minus nothing (150 is not in it): %v", m)
	}
}

func TestThreadErrors(t *testing.T) {
	f := newFake(t)
	e := setup(t, f, "")
	e.fail(1, "is not a valid X tweet id", "thread", "12a")
	e.fail(1, "is not a valid X tweet id", "thread", "../users/me")
	e.fail(2, "exactly one tweet id", "thread")
	if f.count() != 0 {
		t.Error("nothing should be sent for an invalid id")
	}
	e.fail(1, "no X post found for id: 999", "thread", "999")
	e.fail(1, "no X post found for id: 998", "thread", "998")
	if got := f.paths(); len(got) != 2 {
		t.Errorf("replies must not be fetched for a missing post: %v", got)
	}
}

// ---- profile metrics ----

func TestProfileMetrics(t *testing.T) {
	f := newFake(t)
	e := setup(t, f, "")
	m := e.ok("profile-metrics")
	want := map[string]float64{"followers_count": 1000, "following_count": 200, "tweet_count": 3000, "listed_count": 12, "like_count": 55, "media_count": 8}
	if len(m) != len(want) {
		t.Errorf("fields: %v", m)
	}
	for k, v := range want {
		if m[k] != v {
			t.Errorf("%s = %v want %v", k, m[k], v)
		}
	}
}

func TestCheck(t *testing.T) {
	e := setup(t, newFake(t), "")
	m := e.ok("check")
	if m["ok"] != true || !strings.Contains(fmt.Sprint(m["detail"]), "@me_user") {
		t.Errorf("check: %v", m)
	}
}

// ---- engagement baseline ----

func TestComputeAverages(t *testing.T) {
	if got := computeAverages(nil); len(got) != 0 {
		t.Errorf("no posts, no metrics: %v", got)
	}
	posts := []map[string]any{
		extractPost(map[string]any{"public_metrics": map[string]any{"like_count": float64(1), "retweet_count": float64(10)}}),
		extractPost(map[string]any{"public_metrics": map[string]any{"like_count": float64(2), "retweet_count": float64(0)}}),
	}
	got := computeAverages(posts)
	if got["like_count"] != 1.5 || got["repost_count"] != 5 || got["impression_count"] != 0 || len(got) != 6 {
		t.Errorf("averages: %v", got)
	}
}

func TestBaselineComputeAndCache(t *testing.T) {
	f := newFake(t)
	e := setup(t, f, "")
	t0 := time.Date(2025, 1, 1, 12, 0, 0, 0, time.UTC)
	clock := t0
	now = func() time.Time { return clock }
	t.Cleanup(func() { now = time.Now })

	m := e.ok("engagement-baseline", "--username", "@alice")
	if m["refreshed"] != true || m["sample_size"] != float64(4) || m["account_identifier"] != "alice" || m["platform"] != "x" {
		t.Fatalf("first call computes: %v", m)
	}
	metrics := m["metrics"].(map[string]any)
	// likes 10,20,0,30 -> 15; reposts 4,6,0,10 -> 5; impressions 100,200,0,300 -> 150
	if metrics["like_count"] != float64(15) || metrics["repost_count"] != float64(5) || metrics["impression_count"] != float64(150) || metrics["quote_count"] != float64(1.5) {
		t.Errorf("metrics: %v", metrics)
	}
	if m["computed_at"] != "2025-01-01T12:00:00Z" {
		t.Errorf("computed_at: %v", m["computed_at"])
	}
	n := f.count()

	clock = t0.Add(7*24*time.Hour - time.Second)
	m = e.ok("engagement-baseline", "--username", "alice")
	if m["refreshed"] != false || f.count() != n || m["computed_at"] != "2025-01-01T12:00:00Z" {
		t.Errorf("a fresh baseline is served from the cache with no request: %v (%d requests)", m, f.count()-n)
	}
	// username omitted: reuse the only one known
	m = e.ok("engagement-baseline")
	if m["account_identifier"] != "alice" || m["refreshed"] != false || f.count() != n {
		t.Errorf("omitted username: %v", m)
	}
	// the cache is case-insensitive on the handle
	if m = e.ok("engagement-baseline", "--username", "ALICE"); m["refreshed"] != false {
		t.Errorf("case: %v", m)
	}

	clock = t0.Add(7 * 24 * time.Hour) // exactly the interval: stale
	m = e.ok("engagement-baseline")
	if m["refreshed"] != true || f.count() != n+2 {
		t.Errorf("at exactly the interval it refreshes: %v", m)
	}

	m = e.ok("engagement-baseline", "--force-refresh")
	if m["refreshed"] != true || f.count() != n+4 {
		t.Errorf("--force-refresh bypasses the cache: %v", m)
	}
}

func TestBaselineMonthlyInterval(t *testing.T) {
	f := newFake(t)
	e := setup(t, f, "baseline_refresh_interval='monthly'\n")
	t0 := time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)
	clock := t0
	now = func() time.Time { return clock }
	t.Cleanup(func() { now = time.Now })
	e.ok("engagement-baseline", "--username", "alice")
	n := f.count()
	clock = t0.Add(29 * 24 * time.Hour)
	if m := e.ok("engagement-baseline"); m["refreshed"] != false || f.count() != n {
		t.Errorf("monthly keeps it for 30 days: %v", m)
	}
	clock = t0.Add(30 * 24 * time.Hour)
	if m := e.ok("engagement-baseline"); m["refreshed"] != true {
		t.Errorf("then recomputes: %v", m)
	}
	setup2 := setup(t, f, "baseline_refresh_interval='daily'\n")
	setup2.fail(1, "baseline_refresh_interval", "engagement-baseline", "--username", "alice")
}

func TestBaselineAccountResolution(t *testing.T) {
	f := newFake(t)
	e := setup(t, f, "")
	e.fail(1, "requires --username: no baseline exists yet", "engagement-baseline")
	e.ok("engagement-baseline", "--username", "alice")
	e.ok("engagement-baseline", "--username", "bob")
	e.fail(1, "multiple accounts already have a baseline: alice, bob", "engagement-baseline")
	e.fail(1, "is not a valid X username", "engagement-baseline", "--username", "../x")
	// the cache file is private and holds only numbers
	data, err := os.ReadFile(filepath.Join(e.state, "baselines.json"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "SECRET") || strings.Contains(string(data), apiKey) {
		t.Error("credentials in the cache")
	}
}

func TestBaselineUnknownUserCachesNothing(t *testing.T) {
	e := setup(t, newFake(t), "")
	e.fail(1, "no X account found for username: nobody", "engagement-baseline", "--username", "nobody")
	if _, err := os.Stat(filepath.Join(e.state, "baselines.json")); err == nil {
		t.Error("a failed refresh must not write a baseline")
	}
}

func TestBaselineCorruptCacheIsRecomputed(t *testing.T) {
	e := setup(t, newFake(t), "")
	os.WriteFile(filepath.Join(e.state, "baselines.json"), []byte("{not json"), 0o600)
	if m := e.ok("engagement-baseline", "--username", "alice"); m["refreshed"] != true {
		t.Errorf("%v", m)
	}
}

func TestBaselineDefaultStateDirFollowsXDG(t *testing.T) {
	f := newFake(t)
	e := setup(t, f, "")
	// no state_dir: rewrite the config without it
	body, _ := os.ReadFile(e.cfg)
	var kept []string
	for _, l := range strings.Split(string(body), "\n") {
		if !strings.HasPrefix(l, "state_dir") {
			kept = append(kept, l)
		}
	}
	os.WriteFile(e.cfg, []byte(strings.Join(kept, "\n")), 0o600)
	xdg := t.TempDir()
	t.Setenv("XDG_STATE_HOME", xdg)
	e.ok("engagement-baseline", "--username", "alice")
	if _, err := os.Stat(filepath.Join(xdg, "agent-tools", "x", "baselines.json")); err != nil {
		t.Errorf("cache not under XDG_STATE_HOME: %v", err)
	}
}

// ---- post and reply ----

func writeImage(t *testing.T, name, content string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func jsonBody(t *testing.T, s seen) map[string]any {
	t.Helper()
	return decode(t, string(s.body))
}

func TestPostText(t *testing.T) {
	f := newFake(t)
	e := setup(t, f, "")
	m := e.ok("post", "--text", "hello \"world\" — é")
	if m["id"] != "555" || m["text"] != "hello \"world\" — é" || m["image_attached"] != false || m["account"] != "main" {
		t.Errorf("result: %v", m)
	}
	if got := f.paths(); len(got) != 1 || got[0] != "POST /2/tweets" {
		t.Errorf("one call, no upload: %v", got)
	}
	body := jsonBody(t, f.last())
	if len(body) != 1 || body["text"] != "hello \"world\" — é" {
		t.Errorf("a plain post sends only text, no reply or media: %v", body)
	}
}

func TestPostRejectsEmptyText(t *testing.T) {
	f := newFake(t)
	e := setup(t, f, "")
	e.fail(2, "--text is required", "post", "--text", "   ")
	e.fail(2, "--text is required", "post")
	e.fail(2, "--text is required", "reply", "100")
	if f.count() != 0 {
		t.Error("nothing should be sent")
	}
}

func TestPostWithImage(t *testing.T) {
	f := newFake(t)
	e := setup(t, f, "")
	img := writeImage(t, "pic.PNG", "\x89PNG-bytes-here")
	m := e.ok("post", "--text", "look", "--image", img)
	if m["image_attached"] != true || m["id"] != "555" {
		t.Errorf("result: %v", m)
	}
	if got := strings.Join(f.paths(), ","); got != "POST /2/media/upload,POST /2/tweets" {
		t.Fatalf("upload first, then the post: %s", got)
	}
	body := jsonBody(t, f.last())
	media := body["media"].(map[string]any)
	if ids := media["media_ids"].([]any); len(ids) != 1 || ids[0] != "M1" {
		t.Errorf("media ids: %v", body)
	}
	if _, ok := body["reply"]; ok {
		t.Error("a post is not a reply")
	}
}

func TestReply(t *testing.T) {
	f := newFake(t)
	e := setup(t, f, "")
	m := e.ok("reply", "1234567890", "--text", "nice one")
	if m["id"] != "555" || m["text"] != "nice one" {
		t.Errorf("result: %v", m)
	}
	body := jsonBody(t, f.last())
	rep, _ := body["reply"].(map[string]any)
	if rep["in_reply_to_tweet_id"] != "1234567890" || body["text"] != "nice one" {
		t.Errorf("body: %v", body)
	}
	if _, ok := body["media"]; ok {
		t.Error("no image, no media")
	}
	if f.count() != 1 {
		t.Errorf("paths: %v", f.paths())
	}
}

func TestReplyWithImage(t *testing.T) {
	f := newFake(t)
	e := setup(t, f, "")
	e.ok("reply", "77", "--text", "pic", "--image", writeImage(t, "pic.PNG", "data"))
	body := jsonBody(t, f.last())
	if body["reply"].(map[string]any)["in_reply_to_tweet_id"] != "77" || body["media"].(map[string]any)["media_ids"].([]any)[0] != "M1" {
		t.Errorf("body: %v", body)
	}
}

func TestReplyRejectsABadTarget(t *testing.T) {
	f := newFake(t)
	e := setup(t, f, "")
	e.fail(1, "is not a valid X tweet id", "reply", "abc", "--text", "x")
	e.fail(1, "is not a valid X tweet id", "reply", "https://x.com/a/status/1", "--text", "x")
	e.fail(2, "exactly one tweet id", "reply", "--text", "x")
	if f.count() != 0 {
		t.Error("nothing should be sent")
	}
}

func TestImageValidation(t *testing.T) {
	f := newFake(t)
	e := setup(t, f, "")
	dir := t.TempDir()
	big := filepath.Join(dir, "big.png")
	fh, _ := os.Create(big)
	fh.Truncate(5*1024*1024 + 1)
	fh.Close()
	exact := filepath.Join(dir, "exact.png")
	fh, _ = os.Create(exact)
	fh.Truncate(5 * 1024 * 1024)
	fh.Close()
	os.Mkdir(filepath.Join(dir, "dir.png"), 0o700)

	e.fail(1, "requires an absolute --image path", "post", "--text", "x", "--image", "pic.png")
	e.fail(1, "is not a recognized image file", "post", "--text", "x", "--image", writeImage(t, "notes.txt", "x"))
	e.fail(1, "is not a recognized image file", "post", "--text", "x", "--image", writeImage(t, "noext", "x"))
	e.fail(1, "image file not found", "post", "--text", "x", "--image", filepath.Join(dir, "missing.png"))
	e.fail(1, "is not a regular file", "post", "--text", "x", "--image", filepath.Join(dir, "dir.png"))
	e.fail(1, "over the 5242880-byte image size ceiling", "post", "--text", "x", "--image", big)
	e.fail(1, "requires an absolute --image path", "reply", "5", "--text", "x", "--image", "pic.png")
	if f.count() != 0 {
		t.Errorf("nothing should be sent for a bad image: %v", f.paths())
	}
	for _, ext := range []string{"jpg", "JPEG", "gif", "webp"} {
		if mime := imageTypes["."+strings.ToLower(ext)]; mime == "" {
			t.Errorf("%s should be recognized", ext)
		}
	}
	// exactly the limit is allowed (the fake then rejects the filename, which is fine)
	if _, err := loadImage("post", exact); err != nil {
		t.Errorf("5 MiB exactly must pass: %v", err)
	}
}

func TestUploadFailureStopsThePost(t *testing.T) {
	f := newFake(t)
	e := setup(t, f, "")
	e.fail(1, "image upload failed, nothing was posted", "post", "--text", "x", "--image", writeImage(t, "pic.PNG", "REJECT"))
	e.fail(1, "did not include a media id", "post", "--text", "x", "--image", writeImage(t, "pic.PNG", "NOID"))
	for _, p := range f.paths() {
		if p == "POST /2/tweets" {
			t.Error("the post must not be created when the upload failed")
		}
	}
}

func TestMaxSafetyReadRefusesPublishing(t *testing.T) {
	f := newFake(t)
	e := setup(t, f, "")
	e.fail(1, "above the allowed level read", "post", "--text", "x", "--max-safety", "read")
	e.fail(1, "above the allowed level read", "reply", "5", "--text", "x", "--max-safety", "read")
	e.fail(1, "above the allowed level read", "post", "--text", "x", "--image", writeImage(t, "pic.PNG", "d"), "--max-safety", "read")
	e.fail(1, "above the allowed level read", "call", "create_post", "--param", "text=x", "--max-safety", "read")
	if f.count() != 0 {
		t.Errorf("nothing may be sent: %v", f.paths())
	}
}

// ---- --dry-run ----

func TestDryRunSendsNothingAndLeaksNothing(t *testing.T) {
	f := newFake(t)
	e := setup(t, f, "")
	img := writeImage(t, "pic.PNG", "SECRETPIXELS-should-not-be-printed")
	cases := [][]string{
		{"post", "--text", "hi"},
		{"post", "--text", "hi", "--image", img},
		{"reply", "123", "--text", "hi", "--image", img},
		{"reply", "123", "--text", "hi"},
		{"search", "--query", "x"},
		{"timeline", "--username", "alice"},
		{"thread", "100"},
		{"profile-metrics"},
		{"engagement-baseline", "--username", "alice"},
		{"call", "create_post", "--param", "text=hi"},
		{"check"},
	}
	for _, c := range cases {
		code, out, errs := e.run(append(c, "--dry-run")...)
		if code != 0 {
			t.Errorf("%v: exit %d %s", c, code, errs)
		}
		for _, secret := range []string{apiSecret, accessSecret, apiKey, accessToken, "SECRETPIXELS"} {
			if strings.Contains(out+errs, secret) {
				t.Errorf("%v: --dry-run leaked %q:\n%s", c, secret, out)
			}
		}
		if !strings.Contains(out, `"dry_run"`) {
			t.Errorf("%v: no dry_run marker: %s", c, out)
		}
	}
	if f.count() != 0 {
		t.Fatalf("--dry-run must send nothing, sent: %v", f.paths())
	}
	if _, err := os.Stat(filepath.Join(e.state, "baselines.json")); err == nil {
		t.Error("--dry-run must not write the baseline cache")
	}
}

func TestDryRunShowsTheWholePlan(t *testing.T) {
	e := setup(t, newFake(t), "")
	img := writeImage(t, "pic.PNG", "0123456789")
	m := e.ok("reply", "123", "--text", "hi there", "--image", img, "--dry-run")
	steps := list(t, m["steps"])
	if len(steps) != 2 {
		t.Fatalf("upload then post: %v", m)
	}
	up := steps[0].(map[string]any)
	file := up["file"].(map[string]any)
	if !strings.HasSuffix(up["url"].(string), "/2/media/upload") || file["bytes"] != float64(10) || file["content_type"] != "image/png" || file["filename"] != "pic.PNG" {
		t.Errorf("upload step: %v", up)
	}
	tw := steps[1].(map[string]any)
	body := tw["body"].(map[string]any)
	if tw["method"] != "POST" || body["text"] != "hi there" || body["reply"].(map[string]any)["in_reply_to_tweet_id"] != "123" {
		t.Errorf("post step: %v", tw)
	}
	if tw["headers"].(map[string]any)["Authorization"] != "[redacted]" {
		t.Errorf("authorization must be redacted: %v", tw["headers"])
	}
	// the image is still validated in a dry run
	e.fail(1, "is not a recognized image file", "post", "--text", "x", "--image", writeImage(t, "a.txt", "x"), "--dry-run")
}

// ---- raw endpoints ----

func TestEndpointsListAndMarkExtras(t *testing.T) {
	e := setup(t, newFake(t), "")
	code, out, errs := e.run("endpoints")
	if code != 0 {
		t.Fatal(errs)
	}
	var eps []map[string]any
	if err := json.Unmarshal([]byte(out), &eps); err != nil {
		t.Fatal(err)
	}
	got := map[string]map[string]any{}
	for _, ep := range eps {
		got[ep["name"].(string)] = ep
	}
	for _, n := range []string{"search_recent", "user_by_username", "user_tweets", "get_tweet", "users_me", "create_post", "user_by_id", "users_lookup", "tweets_lookup"} {
		if got[n] == nil {
			t.Errorf("missing endpoint %s", n)
		}
	}
	for n, ep := range got {
		wantSafety := "read"
		if n == "create_post" {
			wantSafety = "write"
		}
		if ep["safety"] != wantSafety {
			t.Errorf("%s safety = %v", n, ep["safety"])
		}
	}
	for _, n := range []string{"user_by_id", "users_lookup", "tweets_lookup"} {
		if !strings.Contains(got[n]["description"].(string), "NOT FROM MIMIR") {
			t.Errorf("%s must say it is not from Mimir", n)
		}
	}
}

func TestCallRawEndpoints(t *testing.T) {
	f := newFake(t)
	e := setup(t, f, "")
	code, out, errs := e.run("call", "search_recent", "--param", "query=hello")
	if code != 0 {
		t.Fatal(errs)
	}
	if l := list(t, mustJSON(t, out)); len(l) != 3 {
		t.Errorf("select=data: %s", out)
	}
	if q := f.last().query(); q["max_results"][0] != "100" || q["tweet.fields"][0] != "created_at,public_metrics,author_id" {
		t.Errorf("query: %v", q)
	}
	code, out, errs = e.run("call", "create_post", "--param", "text=raw", "--param", `reply={"in_reply_to_tweet_id":"9"}`)
	if code != 0 {
		t.Fatal(errs)
	}
	body := jsonBody(t, f.last())
	if body["text"] != "raw" || body["reply"].(map[string]any)["in_reply_to_tweet_id"] != "9" {
		t.Errorf("body: %v", body)
	}
	e.fail(1, "needs parameter", "call", "user_by_username")
}

func mustJSON(t *testing.T, s string) any {
	t.Helper()
	var v any
	if err := json.Unmarshal([]byte(s), &v); err != nil {
		t.Fatal(err)
	}
	return v
}

// ---- credentials and config ----

func TestCredentialsFromEnvAndCommand(t *testing.T) {
	f := newFake(t)
	t.Setenv("X_TEST_KEY", apiKey)
	t.Setenv("X_TEST_SECRET", apiSecret)
	cfg := filepath.Join(t.TempDir(), "x.toml")
	body := fmt.Sprintf("default='main'\n[accounts.main]\napi_key_env='X_TEST_KEY'\napi_secret_env='X_TEST_SECRET'\naccess_token_command='printf %s'\naccess_token_secret=%q\nbase_url=%q\nstate_dir=%q\n",
		accessToken, accessSecret, f.srv.URL+"/2", t.TempDir())
	os.WriteFile(cfg, []byte(body), 0o600)
	var out, errb bytes.Buffer
	if code := apikit.RunArgs(service(), []string{"profile-metrics", "--config", cfg}, &out, &errb); code != 0 {
		t.Fatalf("%d %s", code, errb.String())
	}
	if f.count() != 1 {
		t.Error("the request should have been signed with the resolved credentials (the fake checks the header)")
	}
}

func TestMissingCredentialNamesTheFieldNotTheValue(t *testing.T) {
	f := newFake(t)
	cfg := filepath.Join(t.TempDir(), "x.toml")
	os.WriteFile(cfg, []byte("default='main'\n[accounts.main]\napi_key='k'\napi_secret='s'\naccess_token='t'\n"), 0o600)
	var out, errb bytes.Buffer
	code := apikit.RunArgs(service(), []string{"search", "--query", "x", "--config", cfg}, &out, &errb)
	if code != 1 || !strings.Contains(errb.String(), `account "main" has no access_token_secret`) {
		t.Errorf("%d %s", code, errb.String())
	}
	if f.count() != 0 {
		t.Error("nothing should be sent")
	}
}

func TestStarterNestsIntoTheSharedFile(t *testing.T) {
	nested, err := apikit.Nest("x", starter)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(nested, "[x]") || !strings.Contains(nested, "[x.accounts.main]") {
		t.Errorf("nested:\n%s", nested)
	}
}

func TestSharedFileConfig(t *testing.T) {
	f := newFake(t)
	cfg := filepath.Join(t.TempDir(), "agent-tools.toml")
	body := fmt.Sprintf("[x]\ndefault='main'\n[x.accounts.main]\napi_key=%q\napi_secret=%q\naccess_token=%q\naccess_token_secret=%q\nbase_url=%q\nstate_dir=%q\n",
		apiKey, apiSecret, accessToken, accessSecret, f.srv.URL+"/2", t.TempDir())
	os.WriteFile(cfg, []byte(body), 0o600)
	var out, errb bytes.Buffer
	if code := apikit.RunArgs(service(), []string{"profile-metrics", "--config", cfg}, &out, &errb); code != 0 {
		t.Fatalf("%d %s", code, errb.String())
	}
}

func TestExtractPostDefaultsMissingFields(t *testing.T) {
	p := extractPost(map[string]any{"id": "1"})
	if p["like_count"] != int64(0) || p["text"] != nil || p["id"] != "1" {
		t.Errorf("%v", p)
	}
}
