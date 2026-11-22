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

	"github.com/revans/agent-tools/apikit"
	"github.com/revans/agent-tools/postlog"
)

const goodToken = "TOK123"

type seen struct {
	Method, Path, Query string
	Header              http.Header
	Body                []byte
}

// fakeLinkedIn speaks enough of LinkedIn's API to check what the tool sends.
// fail maps a path to a status to answer with instead of success.
type fakeLinkedIn struct {
	srv  *httptest.Server
	mu   sync.Mutex
	reqs []seen
	fail map[string]int
	noID bool // answer the post without an x-restli-id header
}

func (f *fakeLinkedIn) paths() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	var p []string
	for _, r := range f.reqs {
		p = append(p, r.Method+" "+r.Path)
	}
	return p
}

func (f *fakeLinkedIn) find(method, path string) *seen {
	f.mu.Lock()
	defer f.mu.Unlock()
	for i := range f.reqs {
		if f.reqs[i].Method == method && f.reqs[i].Path == path {
			return &f.reqs[i]
		}
	}
	return nil
}

func newFake(t *testing.T) *fakeLinkedIn {
	f := &fakeLinkedIn{fail: map[string]int{}}
	f.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		f.mu.Lock()
		f.reqs = append(f.reqs, seen{r.Method, r.URL.Path, r.URL.RawQuery, r.Header.Clone(), body})
		status := f.fail[r.URL.Path]
		f.mu.Unlock()
		if status != 0 {
			if status == 429 {
				w.Header().Set("Retry-After", "30")
			}
			w.WriteHeader(status)
			fmt.Fprintf(w, `{"message":"fake %d","status":%d}`, status, status)
			return
		}
		switch {
		case r.Method == "GET" && r.URL.Path == "/v2/userinfo":
			w.Write([]byte(`{"sub":"ABC123","name":"Pat Poster","email":"nope@example.com"}`))
		case r.Method == "POST" && r.URL.Path == "/rest/images":
			fmt.Fprintf(w, `{"value":{"uploadUrlExpiresAt":1,"uploadUrl":"%s/upload/xyz?ut=SECRETUPLOAD","image":"urn:li:image:IMG1"}}`, f.srv.URL)
		case r.Method == "PUT" && r.URL.Path == "/upload/xyz":
			w.WriteHeader(201)
		case r.Method == "POST" && r.URL.Path == "/rest/posts":
			if !f.noID {
				w.Header().Set("x-restli-id", "urn:li:share:555")
			}
			w.WriteHeader(201)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(f.srv.Close)
	return f
}

// setup writes a config for the fake and returns a runner.
func setup(t *testing.T, f *fakeLinkedIn, extra string) func(args ...string) (int, string, string) {
	t.Helper()
	t.Setenv(postlog.EnvPath, filepath.Join(t.TempDir(), "posts.jsonl")) // never the real log
	cfg := filepath.Join(t.TempDir(), "linkedin.toml")
	body := fmt.Sprintf("[accounts.main]\naccess_token = %q\nbase_url = %q\n%s", goodToken, f.srv.URL, extra)
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

func writeImage(t *testing.T, name string, data []byte) string {
	p := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(p, data, 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func wantRestli(t *testing.T, r *seen) {
	t.Helper()
	if r.Header.Get("Authorization") != "Bearer "+goodToken {
		t.Errorf("%s %s: Authorization = %q", r.Method, r.Path, r.Header.Get("Authorization"))
	}
	if r.Header.Get("Linkedin-Version") != "202608" || r.Header.Get("X-Restli-Protocol-Version") != "2.0.0" {
		t.Errorf("%s %s: version headers = %q / %q", r.Method, r.Path, r.Header.Get("Linkedin-Version"), r.Header.Get("X-Restli-Protocol-Version"))
	}
}

func TestEscapeLittleText(t *testing.T) {
	for _, ch := range littleReserved {
		if got := escapeLittleText("a" + string(ch) + "b"); got != "a\\"+string(ch)+"b" {
			t.Errorf("%q: %q", ch, got)
		}
	}
	if got := escapeLittleText("Check out #mimir (v2.0) — it's great! [link] @robert_evans"); got != `Check out \#mimir \(v2.0\) — it's great! \[link\] \@robert\_evans` {
		t.Errorf("realistic: %q", got)
	}
	if escapeLittleText("hello world") != "hello world" || escapeLittleText("") != "" {
		t.Error("plain text is unchanged")
	}
	if escapeLittleText(`a\b`) != `a\\b` {
		t.Error("a backslash is escaped once")
	}
}

func TestValidateCommentary(t *testing.T) {
	for _, bad := range []string{"", "   \n"} {
		if validateCommentary(bad) == nil {
			t.Errorf("%q must be rejected", bad)
		}
	}
	if err := validateCommentary(strings.Repeat("é", 3000)); err != nil {
		t.Errorf("3000 characters (6000 bytes) is allowed: %v", err)
	}
	if err := validateCommentary(strings.Repeat("a", 3001)); err == nil || !strings.Contains(err.Error(), "3001") {
		t.Errorf("3001 characters must be rejected: %v", err)
	}
	// The limit applies to the raw text, not the escaped text.
	if err := validateCommentary(strings.Repeat("#", 3000)); err != nil {
		t.Errorf("limit must be checked before escaping: %v", err)
	}
}

func TestValidateImage(t *testing.T) {
	for _, name := range []string{"a.jpg", "a.JPEG", "a.png", "a.gif"} {
		if n, err := validateImage(writeImage(t, name, []byte("12345"))); err != nil || n != 5 {
			t.Errorf("%s: %d %v", name, n, err)
		}
	}
	for _, name := range []string{"a.webp", "a.bmp", "a.txt", "noext"} {
		if _, err := validateImage(writeImage(t, name, []byte("x"))); err == nil || !strings.Contains(err.Error(), "JPEG, GIF, or PNG") {
			t.Errorf("%s must be refused as a type: %v", name, err)
		}
	}
	if _, err := validateImage(filepath.Join(t.TempDir(), "gone.png")); err == nil || !strings.Contains(err.Error(), "not found") {
		t.Errorf("missing: %v", err)
	}
	if _, err := validateImage(t.TempDir() + "/dir.png"); err == nil {
		t.Error("missing dir.png")
	}
	dir := filepath.Join(t.TempDir(), "d.png")
	os.Mkdir(dir, 0o700)
	if _, err := validateImage(dir); err == nil || !strings.Contains(err.Error(), "regular file") {
		t.Errorf("directory: %v", err)
	}
	big := writeImage(t, "big.png", make([]byte, imageMaxBytes+1))
	if _, err := validateImage(big); err == nil || !strings.Contains(err.Error(), "size ceiling") {
		t.Errorf("oversize: %v", err)
	}
	if _, err := validateImage(writeImage(t, "ok.png", make([]byte, imageMaxBytes))); err != nil {
		t.Errorf("exactly the ceiling is allowed: %v", err)
	}
}

func TestNormalizeAuthor(t *testing.T) {
	for in, want := range map[string]string{"": "", "ABC_1-x": "urn:li:person:ABC_1-x", "urn:li:person:Q": "urn:li:person:Q", " Q ": "urn:li:person:Q"} {
		if got, err := normalizeAuthor(in); err != nil || got != want {
			t.Errorf("%q: %q %v", in, got, err)
		}
	}
	if _, err := normalizeAuthor("has space/slash"); err == nil {
		t.Error("junk must be rejected")
	}
}

func TestTextPostFlow(t *testing.T) {
	f := newFake(t)
	run := setup(t, f, "")
	code, out, errs := run("post", "--text", "Hello (world) #go")
	if code != 0 {
		t.Fatalf("%d %s", code, errs)
	}
	m := decode(t, out)
	if m["id"] != "urn:li:share:555" || m["author"] != "urn:li:person:ABC123" || m["image_attached"] != false {
		t.Errorf("output: %v", m)
	}
	if got := strings.Join(f.paths(), ","); got != "GET /v2/userinfo,POST /rest/posts" {
		t.Errorf("calls: %s", got)
	}
	ui := f.find("GET", "/v2/userinfo")
	if ui.Header.Get("Authorization") != "Bearer "+goodToken {
		t.Errorf("userinfo auth: %q", ui.Header.Get("Authorization"))
	}
	p := f.find("POST", "/rest/posts")
	wantRestli(t, p)
	if p.Header.Get("Content-Type") != "application/json" {
		t.Errorf("content type %q", p.Header.Get("Content-Type"))
	}
	var body map[string]any
	json.Unmarshal(p.Body, &body)
	dist, _ := json.Marshal(body["distribution"])
	if body["author"] != "urn:li:person:ABC123" || body["commentary"] != `Hello \(world\) \#go` ||
		body["visibility"] != "PUBLIC" || body["lifecycleState"] != "PUBLISHED" ||
		body["isReshareDisabledByAuthor"] != false ||
		string(dist) != `{"feedDistribution":"MAIN_FEED","targetEntities":[],"thirdPartyDistributionChannels":[]}` {
		t.Errorf("body: %s", p.Body)
	}
	if _, has := body["content"]; has {
		t.Errorf("a text post has no content: %s", p.Body)
	}
}

func TestLimitIsOnRawTextNotEscaped(t *testing.T) {
	f := newFake(t)
	run := setup(t, f, "author = \"A\"\n")
	if code, _, e := run("post", "--text", strings.Repeat("#", 3000)); code != 0 {
		t.Fatalf("3000 reserved characters are within the limit: %d %s", code, e)
	}
	var body map[string]any
	json.Unmarshal(f.find("POST", "/rest/posts").Body, &body)
	if len(body["commentary"].(string)) != 6000 {
		t.Errorf("the wire text is escaped: %d", len(body["commentary"].(string)))
	}
}

func TestAuthorFromConfigSkipsUserinfo(t *testing.T) {
	f := newFake(t)
	run := setup(t, f, "author = \"ZZZ9\"\n")
	if code, out, errs := run("post", "--text", "hi"); code != 0 || decode(t, out)["author"] != "urn:li:person:ZZZ9" {
		t.Fatalf("%d %s %s", code, out, errs)
	}
	if f.find("GET", "/v2/userinfo") != nil {
		t.Error("userinfo must not be called when author is configured")
	}
	var body map[string]any
	json.Unmarshal(f.find("POST", "/rest/posts").Body, &body)
	if body["author"] != "urn:li:person:ZZZ9" {
		t.Errorf("author: %v", body["author"])
	}
}

func TestBadAuthorConfig(t *testing.T) {
	run := setup(t, newFake(t), "author = \"no good!\"\n")
	if code, _, e := run("post", "--text", "hi"); code != 1 || !strings.Contains(e, "author") {
		t.Errorf("%d %q", code, e)
	}
}

func TestImagePostFlow(t *testing.T) {
	f := newFake(t)
	run := setup(t, f, "")
	img := []byte("\x89PNG-not-really-but-bytes\x00\x01")
	path := writeImage(t, "pic.png", img)
	code, out, errs := run("post", "--text", "with picture", "--image", path)
	if code != 0 {
		t.Fatalf("%d %s", code, errs)
	}
	m := decode(t, out)
	if m["id"] != "urn:li:share:555" || m["image_attached"] != true {
		t.Errorf("output: %v", m)
	}
	if got := strings.Join(f.paths(), ","); got != "GET /v2/userinfo,POST /rest/images,PUT /upload/xyz,POST /rest/posts" {
		t.Errorf("calls: %s", got)
	}
	init := f.find("POST", "/rest/images")
	wantRestli(t, init)
	if init.Query != "action=initializeUpload" {
		t.Errorf("init query: %q", init.Query)
	}
	if string(init.Body) != `{"initializeUploadRequest":{"owner":"urn:li:person:ABC123"}}` {
		t.Errorf("init body: %s", init.Body)
	}
	up := f.find("PUT", "/upload/xyz")
	if !bytes.Equal(up.Body, img) {
		t.Errorf("the upload URL must get the raw file bytes, got %q", up.Body)
	}
	if up.Query != "ut=SECRETUPLOAD" {
		t.Errorf("the upload URL is used as given: %q", up.Query)
	}
	// Mimir sends the bearer token with the upload; so does this.
	if up.Header.Get("Authorization") != "Bearer "+goodToken {
		t.Errorf("upload auth (as Mimir): %q", up.Header.Get("Authorization"))
	}
	var body map[string]any
	json.Unmarshal(f.find("POST", "/rest/posts").Body, &body)
	content, _ := json.Marshal(body["content"])
	if string(content) != `{"media":{"id":"urn:li:image:IMG1"}}` {
		t.Errorf("post content must reference the image URN: %s", content)
	}
}

func TestPostIDComesFromHeader(t *testing.T) {
	f := newFake(t)
	f.noID = true
	run := setup(t, f, "author = \"A\"\n")
	code, _, e := run("post", "--text", "hi")
	if code != 1 || !strings.Contains(e, "x-restli-id") {
		t.Errorf("a missing header is an error: %d %q", code, e)
	}
}

func TestValidationHappensBeforeAnyRequest(t *testing.T) {
	f := newFake(t)
	run := setup(t, f, "")
	good := writeImage(t, "ok.png", []byte("x"))
	cases := []struct {
		args []string
		code int
		msg  string
	}{
		{[]string{"post"}, 2, "--text"},
		{[]string{"post", "--text", "  "}, 2, "--text"},
		{[]string{"post", "--text", strings.Repeat("a", 3001)}, 1, "3000-character"},
		{[]string{"post", "--text", "x", "--image", writeImage(t, "p.webp", []byte("x"))}, 1, "JPEG, GIF, or PNG"},
		{[]string{"post", "--text", "x", "--image", "/nonexistent/p.png"}, 1, "not found"},
		{[]string{"post", "--text", strings.Repeat("a", 3001), "--image", good}, 1, "3000-character"},
	}
	for _, c := range cases {
		code, _, e := run(c.args...)
		if code != c.code || !strings.Contains(e, c.msg) {
			t.Errorf("%v: %d %q", c.args, code, e)
		}
	}
	if len(f.paths()) != 0 {
		t.Errorf("validation must come before any request: %v", f.paths())
	}
}

func TestErrorMapping(t *testing.T) {
	cases := []struct {
		status int
		want   []string
	}{
		{401, []string{"HTTP 401", "fake 401", "expired", "60 days", "no refresh"}},
		{403, []string{"HTTP 403", "fake 403", "w_member_social"}},
		{422, []string{"HTTP 422", "fake 422"}},
		{429, []string{"HTTP 429", "rate limited", "retry after 30"}},
	}
	for _, c := range cases {
		f := newFake(t)
		f.fail["/rest/posts"] = c.status
		run := setup(t, f, "author = \"A\"\n")
		code, out, e := run("post", "--text", "hi")
		if code != 1 || out != "" {
			t.Errorf("%d: code %d out %q", c.status, code, out)
		}
		for _, w := range c.want {
			if !strings.Contains(e, w) {
				t.Errorf("%d: %q missing from %q", c.status, w, e)
			}
		}
		if n := len(f.paths()); n != 1 {
			t.Errorf("%d: a failed POST must not be retried, saw %d requests", c.status, n)
		}
	}
}

func TestErrorsAtEachImageStep(t *testing.T) {
	path := writeImage(t, "p.png", []byte("bytes"))
	// Init fails: nothing uploaded, nothing posted.
	f := newFake(t)
	f.fail["/rest/images"] = 403
	run := setup(t, f, "author = \"A\"\n")
	if code, _, e := run("post", "--text", "hi", "--image", path); code != 1 || !strings.Contains(e, "HTTP 403") {
		t.Errorf("init: %d %q", code, e)
	}
	if f.find("PUT", "/upload/xyz") != nil || f.find("POST", "/rest/posts") != nil {
		t.Error("nothing may follow a failed init")
	}
	// Upload fails: no post is created.
	f = newFake(t)
	f.fail["/upload/xyz"] = 400
	run = setup(t, f, "author = \"A\"\n")
	code, _, e := run("post", "--text", "hi", "--image", path)
	if code != 1 || !strings.Contains(e, "HTTP 400") || !strings.Contains(e, "nothing was posted") {
		t.Errorf("upload: %d %q", code, e)
	}
	if f.find("POST", "/rest/posts") != nil {
		t.Error("no post after a failed upload")
	}
	if strings.Contains(e, "SECRETUPLOAD") {
		t.Errorf("the upload URL's token leaked into the error: %q", e)
	}
	// Userinfo fails with 401.
	f = newFake(t)
	f.fail["/v2/userinfo"] = 401
	run = setup(t, f, "")
	code, _, e = run("post", "--text", "hi")
	if code != 1 || !strings.Contains(e, "finding the author") || !strings.Contains(e, "expired") {
		t.Errorf("userinfo: %d %q", code, e)
	}
}

func TestDryRunSendsNothingAndLeaksNothing(t *testing.T) {
	f := newFake(t)
	run := setup(t, f, "")
	path := writeImage(t, "pic.png", []byte("IMAGEBYTES"))
	code, out, errs := run("post", "--text", "secret-free text", "--image", path, "--dry-run")
	if code != 0 {
		t.Fatalf("%d %s", code, errs)
	}
	if len(f.paths()) != 0 {
		t.Errorf("dry run touched the network: %v", f.paths())
	}
	if strings.Contains(out, goodToken) || strings.Contains(out, "IMAGEBYTES") {
		t.Errorf("dry run leaked: %s", out)
	}
	m := decode(t, out)
	steps, _ := m["steps"].([]any)
	if m["dry_run"] != true || len(steps) != 4 {
		t.Fatalf("want 4 steps (userinfo, init, upload, post): %s", out)
	}
	if !strings.Contains(out, "/rest/posts") || !strings.Contains(out, "10 bytes") {
		t.Errorf("steps: %s", out)
	}
	// Even a dry run refuses a bad image.
	if code, _, _ := run("post", "--text", "x", "--image", writeImage(t, "a.webp", nil), "--dry-run"); code != 1 {
		t.Errorf("dry run must still validate the image: %d", code)
	}
	if code, _, _ := run("post", "--text", strings.Repeat("a", 3001), "--dry-run"); code != 1 {
		t.Errorf("dry run must still validate the text: %d", code)
	}
	// With the author configured the plan has no userinfo step.
	run = setup(t, f, "author = \"A\"\n")
	_, out, _ = run("post", "--text", "hi", "--dry-run")
	if got := decode(t, out)["steps"].([]any); len(got) != 1 {
		t.Errorf("text post with author: %s", out)
	}
	if len(f.paths()) != 0 {
		t.Errorf("dry run touched the network: %v", f.paths())
	}
}

func TestCheckUsesUserinfo(t *testing.T) {
	f := newFake(t)
	run := setup(t, f, "")
	code, out, errs := run("check")
	if code != 0 {
		t.Fatalf("%d %s", code, errs)
	}
	m := decode(t, out)
	if d, ok := m["detail"].(map[string]any); ok && m["ok"] == true {
		m = d
	}
	if m["ok"] != true || m["name"] != "Pat Poster" || m["sub"] != "ABC123" || strings.Contains(out, "nope@example.com") {
		t.Errorf("check output: %s", out)
	}
	if got := strings.Join(f.paths(), ","); got != "GET /v2/userinfo" {
		t.Errorf("check calls: %s", got)
	}
	f.mu.Lock()
	f.fail["/v2/userinfo"] = 401
	f.mu.Unlock()
	if code, _, e := run("check"); code != 1 || !strings.Contains(e, "expired") {
		t.Errorf("an expired token: %d %q", code, e)
	}
}

func TestCallEndpointsAndSafety(t *testing.T) {
	f := newFake(t)
	run := setup(t, f, "")
	if code, out, _ := run("call", "userinfo"); code != 0 || decode(t, out)["sub"] != "ABC123" {
		t.Errorf("call userinfo: %d %s", code, out)
	}
	// The read-only gate the MCP reader uses refuses the write endpoints.
	for _, ep := range []string{"post_create", "image_upload_init"} {
		code, _, e := run("call", ep, "--max-safety=read", "--param", "author=x", "--param", "commentary=x", "--param", `request={}`)
		if code == 0 || !strings.Contains(e, "above the allowed level") {
			t.Errorf("%s must be refused at read: %d %q", ep, code, e)
		}
	}
	if f.find("POST", "/rest/posts") != nil || f.find("POST", "/rest/images") != nil {
		t.Error("a refused call must send nothing")
	}
	if code, out, _ := run("endpoints"); code != 0 || !strings.Contains(out, "post_create") || !strings.Contains(out, "userinfo") {
		t.Errorf("endpoints: %d %s", code, out)
	}
}

func TestSecretFromEnvAndCommand(t *testing.T) {
	f := newFake(t)
	cfg := filepath.Join(t.TempDir(), "c.toml")
	os.WriteFile(cfg, []byte(fmt.Sprintf("[accounts.main]\naccess_token_env='LI_TOK_TEST'\nbase_url=%q\n", f.srv.URL)), 0o600)
	t.Setenv("LI_TOK_TEST", goodToken)
	var out, errb bytes.Buffer
	if code := apikit.RunArgs(service(), []string{"check", "--config", cfg}, &out, &errb); code != 0 {
		t.Fatalf("env: %d %s", code, errb.String())
	}
	os.WriteFile(cfg, []byte(fmt.Sprintf("[accounts.main]\naccess_token_command='echo %s'\nbase_url=%q\n", goodToken, f.srv.URL)), 0o600)
	out.Reset()
	if code := apikit.RunArgs(service(), []string{"check", "--config", cfg}, &out, &errb); code != 0 {
		t.Fatalf("command: %d %s", code, errb.String())
	}
	if f.find("GET", "/v2/userinfo").Header.Get("Authorization") != "Bearer "+goodToken {
		t.Error("the token must come through")
	}
}

func TestBuiltinEndpointsLoad(t *testing.T) {
	reg, err := apikit.LoadRegistry(builtin, nil)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]string{"userinfo": "read", "image_upload_init": "write", "post_create": "write"}
	for name, safety := range want {
		ep, ok := reg.Get(name)
		if !ok || ep.Safety != safety {
			t.Errorf("%s: %v", name, ep)
		}
	}
}
