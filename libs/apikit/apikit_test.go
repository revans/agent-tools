package apikit

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

const spec = `
[[endpoint]]
name = "search"
description = "Search things."
method = "GET"
path = "/things/{kind}/search"
safety = "read"
select = "data.items[*]"
fields = ["id", "title", "who:author.name"]
  [endpoint.fixed]
  raw = "1"
  [[endpoint.param]]
  name = "kind"
  in = "path"
  required = true
  description = "Kind of thing."
  [[endpoint.param]]
  name = "q"
  required = true
  description = "Query."
  [[endpoint.param]]
  name = "limit"
  type = "integer"
  default = "5"
  description = "Max."
  [[endpoint.param]]
  name = "sort"
  enum = ["new", "top"]
  description = "Order."
  [[endpoint.param]]
  name = "after"
  description = "Cursor."
  [endpoint.paginate]
  cursor_path = "data.after"
  cursor_param = "after"

[[endpoint]]
name = "make"
description = "Make a thing."
method = "POST"
path = "/things"
safety = "write"
  [[endpoint.param]]
  name = "title"
  in = "body"
  required = true
  description = "Title."
  [[endpoint.param]]
  name = "tags"
  type = "list"
  in = "body"
  description = "Tags."
  [[endpoint.param]]
  name = "meta"
  type = "json"
  in = "body"
  description = "Extra."
`

func registry(t *testing.T) *Registry {
	t.Helper()
	r, err := LoadRegistry(spec, nil)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func TestSelectAndProject(t *testing.T) {
	var v any
	json.Unmarshal([]byte(`{"data":{"items":[{"id":"a","title":"A","author":{"name":"x"},"junk":1},{"id":"b","author":{"name":"y"}}]}}`), &v)
	sel, err := Select(v, "data.items[*]")
	if err != nil {
		t.Fatal(err)
	}
	got, _ := Project(sel, []string{"id", "title", "who:author.name"})
	b, _ := json.Marshal(got)
	if string(b) != `[{"id":"a","title":"A","who":"x"},{"id":"b","who":"y"}]` {
		t.Errorf("got %s", b)
	}
	if first, _ := Select(v, "data.items[0].id"); first != "a" {
		t.Errorf("index: %v", first)
	}
	if missing, _ := Select(v, "data.nope.deeper"); missing != nil {
		t.Errorf("a missing path must be nil, got %v", missing)
	}
	for _, bad := range []string{"a..b", "a[", "a[x]"} {
		if _, err := Select(v, bad); err == nil {
			t.Errorf("%q should be a syntax error", bad)
		}
	}
}

func TestBuild(t *testing.T) {
	r := registry(t)
	search, _ := r.Get("search")
	req, err := search.Build("https://api.example.com/", map[string]string{"kind": "a b", "q": "x&y", "sort": "top"})
	if err != nil {
		t.Fatal(err)
	}
	want := "https://api.example.com/things/a%20b/search?limit=5&q=x%26y&raw=1&sort=top"
	if req.URL != want {
		t.Errorf("url\n got %s\nwant %s", req.URL, want)
	}
	for name, p := range map[string]map[string]string{
		"missing required": {"kind": "a"},
		"unknown param":    {"kind": "a", "q": "x", "shell": "1"},
		"bad enum":         {"kind": "a", "q": "x", "sort": "random"},
		"bad integer":      {"kind": "a", "q": "x", "limit": "ten"},
	} {
		if _, err := search.Build("https://x", p); err == nil {
			t.Errorf("%s: expected an error", name)
		}
	}
	make_, _ := r.Get("make")
	req, err = make_.Build("https://api.example.com", map[string]string{"title": "T", "tags": "a, b ,c", "meta": `{"n":1}`})
	if err != nil {
		t.Fatal(err)
	}
	if string(req.Body) != `{"meta":{"n":1},"tags":["a","b","c"],"title":"T"}` || req.Headers["Content-Type"] != "application/json" {
		t.Errorf("body %s %v", req.Body, req.Headers)
	}
	if _, err := make_.Build("x", map[string]string{"title": "T", "meta": `{"n":1} trailing`}); err == nil {
		t.Error("trailing JSON data must be rejected")
	}
}

func TestSpecValidation(t *testing.T) {
	for name, doc := range map[string]string{
		"no safety":           "[[endpoint]]\nname='a'\ndescription='d'\nmethod='GET'\npath='/x'\n",
		"post is read":        "[[endpoint]]\nname='a'\ndescription='d'\nmethod='POST'\npath='/x'\nsafety='read'\n",
		"bad method":          "[[endpoint]]\nname='a'\ndescription='d'\nmethod='FETCH'\npath='/x'\nsafety='read'\n",
		"bad placeholder":     "[[endpoint]]\nname='a'\ndescription='d'\nmethod='GET'\npath='/x/{id}'\nsafety='read'\n",
		"unknown key":         "[[endpoint]]\nname='a'\ndescription='d'\nmethod='GET'\npath='/x'\nsafety='read'\nfoo=1\n",
		"cursor undeclared":   "[[endpoint]]\nname='a'\ndescription='d'\nmethod='GET'\npath='/x'\nsafety='read'\n[endpoint.paginate]\ncursor_path='n'\ncursor_param='after'\n",
		"optional path param": "[[endpoint]]\nname='a'\ndescription='d'\nmethod='GET'\npath='/x/{id}'\nsafety='read'\n[[endpoint.param]]\nname='id'\nin='path'\ndescription='d'\n",
	} {
		if _, err := ParseSpecs("t", []byte(doc)); err == nil {
			t.Errorf("%s: expected an error", name)
		}
	}
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "more.toml"), []byte(spec), 0o644)
	if _, err := LoadRegistry(spec, []string{dir}); err == nil || !strings.Contains(err.Error(), "defined in both") {
		t.Errorf("a duplicate endpoint must be an error, got %v", err)
	}
}

func TestUserEndpointsAddWithoutCode(t *testing.T) {
	dir := t.TempDir()
	extra := "[[endpoint]]\nname='ping'\ndescription='Ping.'\nmethod='GET'\npath='/ping'\nsafety='read'\n"
	os.WriteFile(filepath.Join(dir, "extra.toml"), []byte(extra), 0o644)
	r, err := LoadRegistry(spec, []string{dir})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := r.Get("ping"); !ok || len(r.Names()) != 3 {
		t.Errorf("names: %v", r.Names())
	}
}

func api(t *testing.T, h http.HandlerFunc) (*Client, *Registry) {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	return &Client{BaseURL: srv.URL, HTTP: srv.Client(), UserAgent: "test/1"}, registry(t)
}

func TestCallSelectsAndPaginates(t *testing.T) {
	var calls int32
	c, reg := api(t, func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&calls, 1)
		if r.Header.Get("User-Agent") != "test/1" || r.Header.Get("Authorization") != "Bearer tok" {
			t.Errorf("headers: %v", r.Header)
		}
		if r.URL.Query().Get("after") == "" {
			w.Write([]byte(`{"data":{"after":"c2","items":[{"id":"1","title":"one","author":{"name":"a"}}]}}`))
			return
		}
		w.Write([]byte(`{"data":{"after":null,"items":[{"id":"2","title":"two","author":{"name":"b"}}]}}`))
	})
	c.Auth = Bearer{Token: "tok"}
	ep, _ := reg.Get("search")
	res, err := c.Call(context.Background(), ep, map[string]string{"kind": "k", "q": "x"}, 1)
	if err != nil || res.Cursor != "c2" {
		t.Fatalf("%v %+v", err, res)
	}
	b, _ := json.Marshal(res.Data)
	if string(b) != `[{"id":"1","title":"one","who":"a"}]` {
		t.Errorf("one page: %s", b)
	}
	res, err = c.Call(context.Background(), ep, map[string]string{"kind": "k", "q": "x"}, 3)
	if err != nil {
		t.Fatal(err)
	}
	b, _ = json.Marshal(res.Data)
	if !strings.Contains(string(b), `"id":"1"`) || !strings.Contains(string(b), `"id":"2"`) || atomic.LoadInt32(&calls) != 3 {
		t.Errorf("two pages merged, stopping when the cursor ends: %s (calls %d)", b, calls)
	}
	if _, err := c.Call(context.Background(), ep, map[string]string{"kind": "k", "q": "x"}, 99); err == nil {
		t.Error("asking for too many pages must fail")
	}
}

func TestErrorsAndSafety(t *testing.T) {
	c, reg := api(t, func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "limited") {
			w.Header().Set("Retry-After", "7")
			http.Error(w, `{"message":"slow down"}`, 429)
			return
		}
		w.WriteHeader(404)
		w.Write([]byte(`{"error":{"message":"no such kind"}}`))
	})
	ep, _ := reg.Get("search")
	_, err := c.Call(context.Background(), ep, map[string]string{"kind": "zzz", "q": "x"}, 1)
	if err == nil || !strings.Contains(err.Error(), "HTTP 404: no such kind") {
		t.Errorf("404: %v", err)
	}
	_, err = c.Call(context.Background(), ep, map[string]string{"kind": "limited", "q": "x"}, 1)
	if err == nil || !strings.Contains(err.Error(), "rate limited; retry after 7s") || !strings.Contains(err.Error(), "slow down") {
		t.Errorf("429: %v", err)
	}
	c.MaxSafety = SafetyRead
	mk, _ := reg.Get("make")
	if _, err := c.Call(context.Background(), mk, map[string]string{"title": "t"}, 1); err == nil || !strings.Contains(err.Error(), "above the allowed level") {
		t.Errorf("safety: %v", err)
	}
}

func TestRetriesGetOnly(t *testing.T) {
	var n int32
	c, reg := api(t, func(w http.ResponseWriter, r *http.Request) {
		if atomic.AddInt32(&n, 1) < 3 {
			http.Error(w, "busy", 503)
			return
		}
		w.Write([]byte(`{"data":{"items":[]}}`))
	})
	c.RetryLimit, c.Sleep = 3, func(time.Duration) {}
	ep, _ := reg.Get("search")
	if _, err := c.Call(context.Background(), ep, map[string]string{"kind": "k", "q": "x"}, 1); err != nil || n != 3 {
		t.Errorf("a GET should be retried through 503s: err=%v calls=%d", err, n)
	}
	n = 0
	c2, reg2 := api(t, func(w http.ResponseWriter, r *http.Request) { atomic.AddInt32(&n, 1); http.Error(w, "busy", 503) })
	c2.RetryLimit, c2.Sleep = 3, func(time.Duration) {}
	mk, _ := reg2.Get("make")
	if _, err := c2.Call(context.Background(), mk, map[string]string{"title": "t"}, 1); err == nil || n != 1 {
		t.Errorf("a POST must never be retried (it could post twice): err=%v calls=%d", err, n)
	}
}

func TestDryRunHidesSecretsAndSendsNothing(t *testing.T) {
	var hit int32
	c, reg := api(t, func(w http.ResponseWriter, r *http.Request) { atomic.AddInt32(&hit, 1) })
	c.DryRun = true
	c.Auth = Bearer{Token: "SUPERSECRET"}
	ep, _ := reg.Get("make")
	res, err := c.Call(context.Background(), ep, map[string]string{"title": "hello"}, 1)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := json.Marshal(res.Data)
	if atomic.LoadInt32(&hit) != 0 || strings.Contains(string(b), "SUPERSECRET") || !strings.Contains(string(b), `"dry_run":true`) || !strings.Contains(string(b), `[redacted]`) {
		t.Errorf("hit=%d %s", hit, b)
	}
	if got := redactURL("https://x/y?q=1&key=ABC&access_token=DEF"); strings.Contains(got, "ABC") || strings.Contains(got, "DEF") || !strings.Contains(got, "q=1") {
		t.Errorf("redactURL: %s", got)
	}
}

func TestClientCredentials(t *testing.T) {
	var tokenCalls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/token" {
			atomic.AddInt32(&tokenCalls, 1)
			u, p, _ := r.BasicAuth()
			r.ParseForm()
			if u != "id" || p != "secret" || r.PostForm.Get("grant_type") != "client_credentials" || r.Header.Get("User-Agent") != "ua/1" {
				http.Error(w, `{"error":"bad"}`, 401)
				return
			}
			w.Write([]byte(`{"access_token":"T0K"}`))
			return
		}
		if r.Header.Get("Authorization") != "Bearer T0K" {
			http.Error(w, "no", 401)
			return
		}
		w.Write([]byte(`{"data":{"items":[]}}`))
	}))
	defer srv.Close()
	cc := &ClientCredentials{TokenURL: srv.URL + "/token", ID: "id", Secret: "secret", UserAgent: "ua/1"}
	c := &Client{BaseURL: srv.URL, Auth: cc, HTTP: srv.Client()}
	ep, _ := registry(t).Get("search")
	for i := 0; i < 2; i++ {
		if _, err := c.Call(context.Background(), ep, map[string]string{"kind": "k", "q": "x"}, 1); err != nil {
			t.Fatal(err)
		}
	}
	if tokenCalls != 1 {
		t.Errorf("the token must be fetched once and reused, got %d", tokenCalls)
	}
	bad := &ClientCredentials{TokenURL: srv.URL + "/token", ID: "id", Secret: "wrong", UserAgent: "ua/1"}
	c.Auth = bad
	if _, err := c.Call(context.Background(), ep, map[string]string{"kind": "k", "q": "x"}, 1); err == nil || !strings.Contains(err.Error(), "token request") {
		t.Errorf("a failed token request must be reported: %v", err)
	}
}

func TestSecrets(t *testing.T) {
	t.Setenv("APIKIT_TEST_SECRET", "from-env")
	acct := map[string]any{"a": "literal", "b_env": "APIKIT_TEST_SECRET", "c_command": "echo from-cmd", "d": "x", "d_env": "APIKIT_TEST_SECRET", "e_env": "APIKIT_NOT_SET", "f_command": "exit 3"}
	for key, want := range map[string]string{"a": "literal", "b": "from-env", "c": "from-cmd"} {
		if got, err := ResolveSecret("acct", acct, key); err != nil || got != want {
			t.Errorf("%s: %q %v", key, got, err)
		}
	}
	for _, key := range []string{"d", "e", "f", "missing"} {
		_, err := ResolveSecret("acct", acct, key)
		if err == nil {
			t.Errorf("%s: expected an error", key)
		} else if strings.Contains(err.Error(), "from-env") {
			t.Errorf("an error must never contain a secret: %v", err)
		}
	}
}

func TestConfigAccounts(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "c.toml")
	os.WriteFile(p, []byte("default='b'\n[accounts.a]\nx='1'\n[accounts.b]\nx='2'\n"), 0o600)
	cfg, err := LoadConfig("tool", p)
	if err != nil {
		t.Fatal(err)
	}
	if n, a, err := cfg.Account(""); err != nil || n != "b" || a["x"] != "2" {
		t.Errorf("default: %s %v %v", n, a, err)
	}
	if _, _, err := cfg.Account("zzz"); err == nil || !strings.Contains(err.Error(), "have: a, b") {
		t.Errorf("unknown account: %v", err)
	}
	cfg.Default = ""
	if _, _, err := cfg.Account(""); err == nil {
		t.Error("several accounts and no default must ask for --account")
	}
	if err := WriteStarter(p, "x"); err == nil {
		t.Error("WriteStarter must not overwrite")
	}
	np := filepath.Join(dir, "sub", "n.toml")
	if err := WriteStarter(np, "x"); err != nil {
		t.Fatal(err)
	}
	if st, _ := os.Stat(np); st.Mode().Perm() != 0o600 {
		t.Errorf("starter mode %o", st.Mode().Perm())
	}
}

func TestCLI(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"data":{"after":null,"items":[{"id":"1","title":"T","author":{"name":"n"}}]}}`))
	}))
	defer srv.Close()
	cfg := filepath.Join(t.TempDir(), "c.toml")
	os.WriteFile(cfg, []byte("[accounts.main]\nbase='"+srv.URL+"'\n"), 0o600)
	svc := &Service{
		Name: "demo", Summary: "demo", Version: "0", Builtin: spec, Starter: "# starter\n",
		NewClient: func(name string, acct map[string]any) (*Client, error) {
			return &Client{BaseURL: OptString(acct, "base", ""), Auth: NoAuth{}}, nil
		},
	}
	do := func(args ...string) (int, string, string) {
		var out, errb bytes.Buffer
		code := run(svc, args, &out, &errb)
		return code, out.String(), errb.String()
	}
	code, out, _ := do("call", "search", "--config", cfg, "--param", "kind=k", "--param", "q=x")
	if code != 0 || strings.TrimSpace(out) != `[{"id":"1","title":"T","who":"n"}]` {
		t.Errorf("call: %d %q", code, out)
	}
	// flags may come before or after the endpoint name
	if code, _, _ := do("call", "--config", cfg, "--param", "kind=k", "--param", "q=x", "search"); code != 0 {
		t.Errorf("interspersed flags: %d", code)
	}
	code, out, _ = do("call", "make", "--config", cfg, "--param", "title=hi", "--dry-run")
	if code != 0 || !strings.Contains(out, `"dry_run":true`) {
		t.Errorf("dry run: %d %q", code, out)
	}
	if code, _, e := do("call", "make", "--config", cfg, "--param", "title=hi", "--max-safety", "read"); code != 1 || !strings.Contains(e, "above the allowed level") {
		t.Errorf("max-safety: %d %q", code, e)
	}
	if code, _, _ := do("call", "search", "--config", cfg); code != 1 {
		t.Errorf("missing params: %d", code)
	}
	if code, _, _ := do("call", "--config", cfg); code != 2 {
		t.Errorf("no endpoint is a usage error: %d", code)
	}
	if code, _, _ := do("nope"); code != 2 {
		t.Errorf("unknown command: %d", code)
	}
	code, out, _ = do("endpoints", "--config", cfg)
	if code != 0 || !strings.Contains(out, `"name":"search"`) || strings.Contains(out, `"params"`) {
		t.Errorf("endpoints: %d %q", code, out)
	}
	code, out, _ = do("endpoints", "make")
	if code != 0 || !strings.Contains(out, `"params"`) {
		t.Errorf("endpoints one: %d %q", code, out)
	}
	code, out, _ = do("accounts", "--config", cfg)
	if code != 0 || !strings.Contains(out, `"main"`) || strings.Contains(out, srv.URL) {
		t.Errorf("accounts must list names and no settings: %d %q", code, out)
	}
}
