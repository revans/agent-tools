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
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/revans/agent-tools/apikit"
)

const (
	testToken = "CF-TOKEN-SEKRET-123"
	zoneA     = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa" // example.com
	zoneD1    = "d1d1d1d1d1d1d1d1d1d1d1d1d1d1d1d1" // dup.com, in account one
	zoneD2    = "d2d2d2d2d2d2d2d2d2d2d2d2d2d2d2d2" // dup.com, in account two
	recID     = "023e105f4ecef8ad9ca31a8372d0c353"
	acctID    = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
)

// seenReq is one request the fake accepted.
type seenReq struct {
	Method, URI, Path string
	Query             url.Values
	Body              string
}

// fakeCF behaves the way Cloudflare's v4 API is documented to: every answer is
// an envelope, every call needs the Bearer token, and every bad call says why in
// errors[]. It records what it was asked so tests can assert the protocol.
type fakeCF struct {
	srv  *httptest.Server
	mu   sync.Mutex
	seen []seenReq
}

func envelope(result string, info string) string {
	if info != "" {
		info = `,"result_info":` + info
	}
	return `{"success":true,"errors":[],"messages":[],"result":` + result + info + `}`
}

func failure(code int, msg string) string {
	return fmt.Sprintf(`{"success":false,"errors":[{"code":%d,"message":%q}],"messages":[],"result":null}`, code, msg)
}

func pageInfo(page, total, count int) string {
	return fmt.Sprintf(`{"page":%d,"per_page":2,"count":%d,"total_count":%d,"total_pages":%d}`, page, count, total*2, total)
}

func zoneJSON(id, name, acct string) string {
	return fmt.Sprintf(`{"id":%q,"name":%q,"status":"active","account":{"id":"acct-%s","name":%q},"secret_internal":"x"}`, id, name, acct, "Acct "+acct)
}

func recJSON(id, typ, name, content string) string {
	return fmt.Sprintf(`{"id":%q,"type":%q,"name":%q,"content":%q,"ttl":1,"proxied":false,"created_on":"2026-01-01T00:00:00Z"}`, id, typ, name, content)
}

func newFake(t *testing.T) *fakeCF {
	f := &fakeCF{}
	f.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "application/json")
		if strings.Contains(r.URL.RawQuery, testToken) || strings.Contains(r.URL.Path, testToken) {
			t.Errorf("the token must never be in the URL: %s", r.URL)
		}
		if r.Header.Get("Authorization") != "Bearer "+testToken {
			w.WriteHeader(401)
			fmt.Fprint(w, failure(10000, "Authentication error"))
			return
		}
		f.mu.Lock()
		f.seen = append(f.seen, seenReq{r.Method, r.URL.RequestURI(), r.URL.Path, r.URL.Query(), string(body)})
		f.mu.Unlock()
		q := r.URL.Query()
		p := r.URL.Path
		switch {
		case p == "/user/tokens/verify":
			fmt.Fprint(w, envelope(`{"id":"tok","status":"active"}`, ""))
		case p == "/accounts/"+acctID+"/tokens/verify":
			fmt.Fprint(w, envelope(`{"id":"tok","status":"active"}`, ""))
		case p == "/zones" && r.Method == "GET":
			switch q.Get("name") {
			case "example.com":
				fmt.Fprint(w, envelope("["+zoneJSON(zoneA, "example.com", "1")+"]", pageInfo(1, 1, 1)))
			case "dup.com":
				fmt.Fprint(w, envelope("["+zoneJSON(zoneD1, "dup.com", "1")+","+zoneJSON(zoneD2, "dup.com", "2")+"]", pageInfo(1, 1, 2)))
			case "none.com":
				fmt.Fprint(w, envelope("[]", pageInfo(1, 1, 0)))
			case "locked.com":
				w.WriteHeader(403)
				fmt.Fprint(w, failure(9109, "Unauthorized to access requested resource"))
			case "":
				page, _ := strconv.Atoi(q.Get("page"))
				if page == 0 {
					page = 1
				}
				if page > 3 {
					t.Errorf("asked for page %d of 3", page)
				}
				fmt.Fprint(w, envelope(fmt.Sprintf("[%s,%s]", zoneJSON(fmt.Sprintf("%032d", page*10), fmt.Sprintf("p%da.com", page), "1"), zoneJSON(fmt.Sprintf("%032d", page*10+1), fmt.Sprintf("p%db.com", page), "1")), pageInfo(page, 3, 2)))
			default:
				t.Errorf("unexpected zone filter %s", r.URL.RawQuery)
				fmt.Fprint(w, envelope("[]", pageInfo(1, 1, 0)))
			}
		case p == "/zones/"+zoneA:
			fmt.Fprint(w, envelope(zoneJSON(zoneA, "example.com", "1"), ""))
		case p == "/zones/"+zoneA+"/dns_records" && r.Method == "GET":
			if q.Get("page") == "" || q.Get("page") == "1" {
				fmt.Fprint(w, envelope("["+recJSON(recID, "A", "www.example.com", "192.0.2.1")+","+recJSON("r2", "TXT", "example.com", "v=spf1 -all")+"]", pageInfo(1, 2, 2)))
			} else {
				fmt.Fprint(w, envelope("["+recJSON("r3", "CNAME", "blog.example.com", "www.example.com")+"]", pageInfo(2, 2, 1)))
			}
		case p == "/zones/"+zoneA+"/dns_records" && r.Method == "POST":
			var in map[string]any
			_ = json.Unmarshal(body, &in)
			if in["type"] == "TXT" && in["content"] == "dup" {
				fmt.Fprint(w, failure(81058, "An identical record already exists."))
				return
			}
			fmt.Fprint(w, envelope(recJSON("newrec", fmt.Sprint(in["type"]), fmt.Sprint(in["name"]), fmt.Sprint(in["content"])), ""))
		case p == "/zones/"+zoneA+"/dns_records/"+recID && r.Method == "GET":
			fmt.Fprint(w, envelope(recJSON(recID, "A", "www.example.com", "192.0.2.1"), ""))
		case p == "/zones/"+zoneA+"/dns_records/"+recID && r.Method == "PATCH":
			fmt.Fprint(w, envelope(recJSON(recID, "A", "www.example.com", "192.0.2.99"), ""))
		case p == "/zones/"+zoneA+"/dns_records/"+recID && r.Method == "DELETE":
			fmt.Fprint(w, envelope(`{"id":"`+recID+`"}`, ""))
		case strings.HasPrefix(p, "/zones/"+zoneA+"/dns_records/"):
			w.WriteHeader(404)
			fmt.Fprint(w, failure(81044, "Record does not exist."))
		case p == "/zones/"+zoneA+"/purge_cache":
			fmt.Fprint(w, envelope(`{"id":"purge1"}`, ""))
		case p == "/accounts/"+acctID+"/cfd_tunnel":
			fmt.Fprint(w, envelope(`[{"id":"t2","name":"web","status":"healthy","connections":[{"id":"c1"},{"id":"c2"}]},{"id":"t1","name":"api","status":"down","connections":[]}]`, pageInfo(1, 1, 2)))
		default:
			w.WriteHeader(404)
			fmt.Fprint(w, failure(7003, "Could not route to "+p))
		}
	}))
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakeCF) requests(method string) []seenReq {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []seenReq
	for _, s := range f.seen {
		if method == "" || s.Method == method {
			out = append(out, s)
		}
	}
	return out
}

func (f *fakeCF) paths(method string) []string {
	var out []string
	for _, s := range f.requests(method) {
		out = append(out, s.Path)
	}
	return out
}

func setupWith(t *testing.T, f *fakeCF, token, extra string) func(args ...string) (int, string, string) {
	t.Helper()
	cfg := filepath.Join(t.TempDir(), "cloudflare.toml")
	body := fmt.Sprintf("default='main'\n[accounts.main]\napi_token=%q\nbase_url=%q\n%s", token, f.srv.URL, extra)
	if err := os.WriteFile(cfg, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return func(args ...string) (int, string, string) {
		var out, errb bytes.Buffer
		code := apikit.RunArgs(service(), append(args, "--config", cfg), &out, &errb)
		return code, out.String(), errb.String()
	}
}

func setup(t *testing.T, f *fakeCF) func(args ...string) (int, string, string) {
	return setupWith(t, f, testToken, "account_id='"+acctID+"'\n")
}

func decode(t *testing.T, s string) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal([]byte(s), &m); err != nil {
		t.Fatalf("not JSON: %v\n%s", err, s)
	}
	return m
}

func jsonBody(t *testing.T, s seenReq) map[string]any {
	t.Helper()
	return decode(t, s.Body)
}

func ok(t *testing.T, code int, out, errs string) string {
	t.Helper()
	if code != 0 {
		t.Fatalf("exit %d: %s", code, errs)
	}
	return out
}

// ---- auth and shape ----

func TestZonesSendsBearerAndShapesOutput(t *testing.T) {
	f := newFake(t)
	run := setup(t, f)
	code, out, errs := run("zones", "--name", "Example.com")
	m := decode(t, ok(t, code, out, errs))
	zs := m["zones"].([]any)
	if len(zs) != 1 || m["more"] != false {
		t.Fatalf("%v", m)
	}
	z := zs[0].(map[string]any)
	if z["name"] != "example.com" || z["id"] != zoneA || z["status"] != "active" || z["account"] != "Acct 1" || len(z) != 4 {
		t.Errorf("shape: %v", z)
	}
	if got := f.requests("")[0].Query["name"]; len(got) != 1 || got[0] != "example.com" {
		t.Errorf("name filter must be lower-cased: %v", got)
	}
	if strings.Contains(out, testToken) || strings.Contains(out, "secret_internal") {
		t.Error("output must be shaped and token-free")
	}
}

func TestWrongTokenIsRejectedWithAHint(t *testing.T) {
	f := newFake(t)
	run := setupWith(t, f, "wrong-token", "")
	code, _, errs := run("zones")
	if code != 1 || !strings.Contains(errs, "Authentication error") || !strings.Contains(errs, "10000") || !strings.Contains(errs, "hint") {
		t.Errorf("%d %s", code, errs)
	}
	if strings.Contains(errs, "wrong-token") {
		t.Error("token in error text")
	}
}

func TestZonesAllFollowsPageNumbers(t *testing.T) {
	f := newFake(t)
	run := setup(t, f)
	code, out, errs := run("zones", "--all")
	m := decode(t, ok(t, code, out, errs))
	if m["count"].(float64) != 6 || m["more"] != false {
		t.Fatalf("%v", m)
	}
	var pages []string
	for _, s := range f.requests("GET") {
		pages = append(pages, s.Query.Get("page"))
	}
	if strings.Join(pages, ",") != ",2,3" { // the first request names no page; the loop then asks for 2 and 3
		t.Errorf("pages requested: %v (%v)", pages, f.paths(""))
	}
	// without --all: first page only, and it says more exist
	f2 := newFake(t)
	code, out, errs = setup(t, f2)("zones")
	m = decode(t, ok(t, code, out, errs))
	if m["count"].(float64) != 2 || m["more"] != true || len(f2.requests("")) != 1 {
		t.Errorf("first page only: %v, %d requests", m, len(f2.requests("")))
	}
}

// ---- zone resolution ----

func TestZoneNameResolution(t *testing.T) {
	f := newFake(t)
	run := setup(t, f)
	code, _, errs := run("dns", "list", "--zone", "none.com")
	if code != 1 || !strings.Contains(errs, `no zone named "none.com"`) {
		t.Errorf("zero matches: %d %s", code, errs)
	}
	code, _, errs = run("dns", "list", "--zone", "dup.com")
	if code != 1 || !strings.Contains(errs, "2 zones are named") || !strings.Contains(errs, zoneD1) || !strings.Contains(errs, zoneD2) || !strings.Contains(errs, "zone id") {
		t.Errorf("two matches must be refused, listing both: %d %s", code, errs)
	}
	for _, s := range f.requests("") {
		if strings.Contains(s.Path, "dns_records") {
			t.Errorf("no DNS call may follow a failed zone lookup: %s", s.URI)
		}
	}
	code, _, errs = run("dns", "add", "--zone", "dup.com", "--type", "A", "--name", "x", "--content", "192.0.2.1")
	if code != 1 || len(f.requests("POST")) != 0 {
		t.Errorf("an ambiguous zone must not create anything: %d %s", code, errs)
	}
}

func TestZoneIDSkipsLookupAndLookupFailureIsExplained(t *testing.T) {
	f := newFake(t)
	run := setup(t, f)
	runOK(t, run, "dns", "list", "--zone", zoneA)
	for _, p := range f.paths("") {
		if p == "/zones" {
			t.Error("a zone id needs no name lookup")
		}
	}
	code, _, errs := run("dns", "list", "--zone", "locked.com")
	if code != 1 || !strings.Contains(errs, "Unauthorized to access requested resource") || !strings.Contains(errs, "9109") || !strings.Contains(errs, "permission") {
		t.Errorf("%d %s", code, errs)
	}
	if code, _, e := run("dns", "list", "--zone", "nodots"); code != 2 {
		t.Errorf("a bad zone is a usage error: %d %s", code, e)
	}
	if code, _, _ := run("dns", "list"); code != 2 {
		t.Error("--zone is required")
	}
}

// ---- dns list ----

func TestDNSListFiltersAndRelativeNames(t *testing.T) {
	f := newFake(t)
	run := setup(t, f)
	code, out, errs := run("dns", "list", "--zone", "example.com", "--type", "a", "--name", "www")
	m := decode(t, ok(t, code, out, errs))
	reqs := f.requests("GET")
	last := reqs[len(reqs)-1]
	if last.Path != "/zones/"+zoneA+"/dns_records" || last.Query.Get("type") != "A" || last.Query.Get("name") != "www.example.com" {
		t.Errorf("query: %s", last.URI)
	}
	recs := m["records"].([]any)
	r0 := recs[0].(map[string]any)
	if len(recs) != 2 || r0["id"] != recID || r0["content"] != "192.0.2.1" || r0["proxied"] != false || r0["ttl"].(float64) != 1 {
		t.Errorf("records: %v", recs)
	}
	if _, has := r0["created_on"]; has {
		t.Error("records are reshaped")
	}
	if m["more"] != true {
		t.Error("two pages exist, only one fetched")
	}
	// @ is the apex; a full name stays as it is; a zone id needs one lookup of the zone for its name
	runOK(t, run, "dns", "list", "--zone", "example.com", "--name", "@")
	runOK(t, run, "dns", "list", "--zone", "example.com", "--name", "blog.example.com")
	runOK(t, run, "dns", "list", "--zone", zoneA, "--name", "api")
	var names []string
	for _, s := range f.requests("GET") {
		if strings.HasSuffix(s.Path, "/dns_records") {
			names = append(names, s.Query.Get("name"))
		}
	}
	want := "www.example.com,example.com,blog.example.com,api.example.com"
	if strings.Join(names, ",") != want {
		t.Errorf("names: %v", names)
	}
}

func TestDNSListAllFollowsPages(t *testing.T) {
	f := newFake(t)
	code, out, errs := setup(t, f)("dns", "list", "--zone", "example.com", "--all")
	m := decode(t, ok(t, code, out, errs))
	if m["count"].(float64) != 3 || m["more"] != false {
		t.Errorf("%v", m)
	}
	var pages []string
	for _, s := range f.requests("GET") {
		if strings.HasSuffix(s.Path, "/dns_records") {
			pages = append(pages, s.Query.Get("page"))
		}
	}
	if len(pages) != 2 || pages[1] != "2" {
		t.Errorf("pages: %v", pages)
	}
}

// ---- dns add ----

func TestDNSAddSendsOnlyWhatWasGiven(t *testing.T) {
	f := newFake(t)
	run := setup(t, f)
	code, out, errs := run("dns", "add", "--zone", "example.com", "--type", "a", "--name", "www", "--content", "192.0.2.7")
	m := decode(t, ok(t, code, out, errs))
	posts := f.requests("POST")
	if len(posts) != 1 || posts[0].Path != "/zones/"+zoneA+"/dns_records" {
		t.Fatalf("%v", f.seen)
	}
	b := jsonBody(t, posts[0])
	want := map[string]any{"type": "A", "name": "www", "content": "192.0.2.7", "ttl": float64(1), "proxied": false}
	if len(b) != len(want) {
		t.Errorf("body has extra fields: %v", b)
	}
	for k, v := range want {
		if b[k] != v {
			t.Errorf("body[%s] = %v, want %v", k, b[k], v)
		}
	}
	if m["created"].(map[string]any)["id"] != "newrec" {
		t.Errorf("%v", m)
	}
	runOK(t, run, "dns", "add", "--zone", "example.com", "--type", "CNAME", "--name", "blog", "--content", "www.example.com", "--ttl", "300", "--proxied", "--comment", "the blog")
	b = jsonBody(t, f.requests("POST")[1])
	if b["proxied"] != true || b["ttl"] != float64(300) || b["comment"] != "the blog" || len(b) != 6 {
		t.Errorf("body: %v", b)
	}
}

func TestDNSAddMXAndStructuredTypes(t *testing.T) {
	f := newFake(t)
	run := setup(t, f)
	runOK(t, run, "dns", "add", "--zone", "example.com", "--type", "MX", "--name", "@", "--content", "mail.example.com", "--priority", "0")
	b := jsonBody(t, f.requests("POST")[0])
	if b["priority"] != float64(0) || b["name"] != "@" {
		t.Errorf("priority 0 is valid and must be sent as a number: %v", b)
	}
	runOK(t, run, "dns", "add", "--zone", "example.com", "--type", "CAA", "--name", "@", "--data", `{"flags":0,"tag":"issue","value":"letsencrypt.org"}`)
	b = jsonBody(t, f.requests("POST")[1])
	d, _ := b["data"].(map[string]any)
	if d["tag"] != "issue" || b["content"] != nil {
		t.Errorf("data goes as an object, with no content: %v", b)
	}
}

func TestDNSAddValidationSendsNothing(t *testing.T) {
	f := newFake(t)
	run := setup(t, f)
	base := []string{"dns", "add", "--zone", "example.com"}
	cases := map[string][]string{
		"unknown type":       {"--type", "BOGUS", "--name", "x", "--content", "y"},
		"no type":            {"--name", "x", "--content", "y"},
		"empty name":         {"--type", "A", "--name", "  ", "--content", "192.0.2.1"},
		"no name":            {"--type", "A", "--content", "192.0.2.1"},
		"mx without prio":    {"--type", "MX", "--name", "@", "--content", "mail.example.com"},
		"no content":         {"--type", "A", "--name", "x"},
		"ttl too small":      {"--type", "A", "--name", "x", "--content", "192.0.2.1", "--ttl", "5"},
		"ttl too large":      {"--type", "A", "--name", "x", "--content", "192.0.2.1", "--ttl", "90000"},
		"proxied txt":        {"--type", "TXT", "--name", "x", "--content", "hi", "--proxied"},
		"srv without data":   {"--type", "SRV", "--name", "_sip._tcp", "--content", "x"},
		"data on a":          {"--type", "A", "--name", "x", "--data", "{}"},
		"bad data":           {"--type", "CAA", "--name", "@", "--data", "[1]"},
		"priority too large": {"--type", "MX", "--name", "@", "--content", "m.example.com", "--priority", "70000"},
		"flag for list":      {"--type", "A", "--name", "x", "--content", "192.0.2.1", "--all"},
	}
	for name, args := range cases {
		code, _, errs := run(append(append([]string{}, base...), args...)...)
		if code != 2 {
			t.Errorf("%s: exit %d, want 2 (%s)", name, code, errs)
		}
	}
	if n := len(f.requests("")); n != 0 {
		t.Errorf("validation happens before any request, got %d: %v", n, f.paths(""))
	}
}

func TestDNSAddSurfacesAPIErrorMessage(t *testing.T) {
	f := newFake(t)
	code, _, errs := setup(t, f)("dns", "add", "--zone", "example.com", "--type", "TXT", "--name", "x", "--content", "dup")
	if code != 1 || !strings.Contains(errs, "81058") || !strings.Contains(errs, "An identical record already exists.") {
		t.Errorf("%d %s", code, errs)
	}
}

// ---- dns update ----

func TestDNSUpdateIsAPatchOfOnlyGivenFields(t *testing.T) {
	f := newFake(t)
	run := setup(t, f)
	code, out, errs := run("dns", "update", recID, "--zone", "example.com", "--content", "192.0.2.99")
	m := decode(t, ok(t, code, out, errs))
	patches := f.requests("PATCH")
	if len(patches) != 1 || patches[0].Path != "/zones/"+zoneA+"/dns_records/"+recID {
		t.Fatalf("%v", f.seen)
	}
	b := jsonBody(t, patches[0])
	if len(b) != 1 || b["content"] != "192.0.2.99" {
		t.Errorf("a PATCH carries only the fields given: %v", b)
	}
	if m["updated"].(map[string]any)["content"] != "192.0.2.99" {
		t.Errorf("%v", m)
	}
	runOK(t, run, "dns", "update", recID, "--zone", "example.com", "--proxied=false")
	if b := jsonBody(t, f.requests("PATCH")[1]); len(b) != 1 || b["proxied"] != false {
		t.Errorf("an explicit proxied=false is sent, alone: %v", b)
	}
	runOK(t, run, "dns", "update", recID, "--zone", "example.com", "--comment", "", "--ttl", "120")
	if b := jsonBody(t, f.requests("PATCH")[2]); len(b) != 2 || b["comment"] != "" || b["ttl"] != float64(120) {
		t.Errorf("an empty comment clears it: %v", b)
	}
}

func TestDNSUpdateRefusals(t *testing.T) {
	f := newFake(t)
	run := setup(t, f)
	for name, args := range map[string][]string{
		"nothing to change": {"dns", "update", recID, "--zone", "example.com"},
		"no id":             {"dns", "update", "--zone", "example.com", "--ttl", "300"},
		"bad id":            {"dns", "update", "a/b", "--zone", "example.com", "--ttl", "300"},
		"to MX no priority": {"dns", "update", recID, "--zone", "example.com", "--type", "MX"},
		"empty name":        {"dns", "update", recID, "--zone", "example.com", "--name", ""},
		"bad ttl":           {"dns", "update", recID, "--zone", "example.com", "--ttl", "7"},
		"foreign flag":      {"dns", "update", recID, "--zone", "example.com", "--ttl", "300", "--all"},
	} {
		if code, _, e := run(args...); code != 2 {
			t.Errorf("%s: exit %d (%s)", name, code, e)
		}
	}
	if len(f.requests("")) != 0 {
		t.Errorf("nothing may be sent: %v", f.paths(""))
	}
}

// ---- dns delete ----

func TestDNSDeleteNeedsExplicitDestructive(t *testing.T) {
	f := newFake(t)
	run := setup(t, f)
	code, _, errs := run("dns", "delete", recID, "--zone", "example.com")
	if code != 1 || !strings.Contains(errs, "--max-safety destructive") {
		t.Errorf("%d %s", code, errs)
	}
	if code, _, _ := run("dns", "delete", recID, "--zone", "example.com", "--max-safety", "write"); code != 1 {
		t.Error("write is not enough")
	}
	if code, _, _ := run("dns", "delete", recID, "--zone", "example.com", "--dry-run"); code != 1 {
		t.Error("a dry run is gated the same way")
	}
	if n := len(f.requests("")); n != 0 {
		t.Errorf("a refused delete makes no request at all, got %v", f.paths(""))
	}
	code, out, errs := run("dns", "delete", recID, "--zone", "example.com", "--max-safety", "destructive")
	m := decode(t, ok(t, code, out, errs))
	if len(f.requests("DELETE")) != 1 || f.requests("DELETE")[0].Path != "/zones/"+zoneA+"/dns_records/"+recID {
		t.Fatalf("%v", f.seen)
	}
	d := m["deleted"].(map[string]any)
	if d["content"] != "192.0.2.1" || d["name"] != "www.example.com" {
		t.Errorf("the result names what was deleted: %v", m)
	}
}

func TestDNSDeleteDryRunShowsTheRecordAndDeletesNothing(t *testing.T) {
	f := newFake(t)
	code, out, errs := setup(t, f)("dns", "delete", recID, "--zone", "example.com", "--dry-run", "--max-safety", "destructive")
	m := decode(t, ok(t, code, out, errs))
	if len(f.requests("DELETE")) != 0 {
		t.Fatal("a dry run must not delete")
	}
	w := m["would_delete"].(map[string]any)
	if m["dry_run"] != true || w["id"] != recID || w["content"] != "192.0.2.1" || w["type"] != "A" {
		t.Errorf("the record that would go must be fetched and shown: %v", m)
	}
	req := m["request"].(map[string]any)
	if req["method"] != "DELETE" || !strings.HasSuffix(req["url"].(string), "/zones/"+zoneA+"/dns_records/"+recID) {
		t.Errorf("request: %v", req)
	}
	if strings.Contains(out, testToken) {
		t.Error("token leaked into the dry run")
	}
	if req["headers"].(map[string]any)["Authorization"] != "[redacted]" {
		t.Errorf("headers: %v", req["headers"])
	}
}

func TestDNSDeleteOfMissingRecordDeletesNothing(t *testing.T) {
	f := newFake(t)
	code, _, errs := setup(t, f)("dns", "delete", "ffffffffffffffffffffffffffffffff", "--zone", "example.com", "--max-safety", "destructive")
	if code != 1 || !strings.Contains(errs, "Record does not exist.") || !strings.Contains(errs, "nothing was deleted") {
		t.Errorf("%d %s", code, errs)
	}
	if len(f.requests("DELETE")) != 0 {
		t.Error("no DELETE for a record that is not there")
	}
}

// ---- dry runs ----

func TestDryRunOfWritesSendsNothing(t *testing.T) {
	f := newFake(t)
	run := setup(t, f)
	for _, args := range [][]string{
		{"dns", "add", "--zone", "example.com", "--type", "A", "--name", "www", "--content", "192.0.2.7", "--dry-run"},
		{"dns", "update", recID, "--zone", "example.com", "--ttl", "300", "--dry-run"},
		{"purge", "--zone", "example.com", "--everything", "--dry-run"},
	} {
		code, out, errs := run(args...)
		m := decode(t, ok(t, code, out, errs))
		if m["dry_run"] != true || m["body"] == nil || m["method"] == nil {
			t.Errorf("%v: %v", args[:2], m)
		}
		if strings.Contains(out, testToken) {
			t.Errorf("token in dry-run output of %v", args[:2])
		}
	}
	for _, m := range []string{"POST", "PATCH", "DELETE", "PUT"} {
		if n := len(f.requests(m)); n != 0 {
			t.Errorf("%d %s requests were sent during dry runs", n, m)
		}
	}
	// the dry run's body is the real one
	code, out, _ := run("dns", "add", "--zone", "example.com", "--type", "A", "--name", "www", "--content", "192.0.2.7", "--dry-run")
	b := decode(t, out)["body"].(map[string]any)
	if code != 0 || b["proxied"] != false || b["content"] != "192.0.2.7" {
		t.Errorf("%v", b)
	}
}

// ---- purge ----

func TestPurgeBodies(t *testing.T) {
	f := newFake(t)
	run := setup(t, f)
	cases := []struct {
		args []string
		body string
	}{
		{[]string{"--everything"}, `{"purge_everything":true}`},
		{[]string{"--files", "https://example.com/a.css, https://example.com/b.js"}, `{"files":["https://example.com/a.css","https://example.com/b.js"]}`},
		{[]string{"--hosts", "www.example.com,img.example.com"}, `{"hosts":["www.example.com","img.example.com"]}`},
		{[]string{"--tags", "t1,t2"}, `{"tags":["t1","t2"]}`},
		{[]string{"--mode", "tags", "--values", "t3"}, `{"tags":["t3"]}`},
		{[]string{"--mode", "everything"}, `{"purge_everything":true}`},
	}
	for i, c := range cases {
		code, out, errs := run(append([]string{"purge", "--zone", "example.com"}, c.args...)...)
		m := decode(t, ok(t, code, out, errs))
		if m["id"] != "purge1" {
			t.Errorf("%v", m)
		}
		got := f.requests("POST")[i]
		if got.Path != "/zones/"+zoneA+"/purge_cache" {
			t.Errorf("path %s", got.Path)
		}
		var a, b any
		_ = json.Unmarshal([]byte(got.Body), &a)
		_ = json.Unmarshal([]byte(c.body), &b)
		if fmt.Sprint(a) != fmt.Sprint(b) {
			t.Errorf("case %d body %s, want %s", i, got.Body, c.body)
		}
	}
}

func TestPurgeNeedsExactlyOneMode(t *testing.T) {
	f := newFake(t)
	run := setup(t, f)
	for name, args := range map[string][]string{
		"none":                 {},
		"everything and files": {"--everything", "--files", "https://example.com/a"},
		"files and tags":       {"--files", "https://example.com/a", "--tags", "t"},
		"hosts and tags":       {"--hosts", "h.example.com", "--tags", "t"},
		"mode and flag":        {"--mode", "tags", "--values", "t", "--tags", "t"},
		"everything false":     {"--everything=false"},
		"empty files":          {"--files", " , "},
		"relative file":        {"--files", "/a.css"},
		"host with path":       {"--hosts", "example.com/x"},
		"values alone":         {"--values", "x"},
		"bad mode":             {"--mode", "some", "--values", "x"},
		"everything with vals": {"--mode", "everything", "--values", "x"},
	} {
		code, _, e := run(append([]string{"purge", "--zone", "example.com"}, args...)...)
		if code != 2 {
			t.Errorf("%s: exit %d (%s)", name, code, e)
		}
	}
	if len(f.requests("")) != 0 {
		t.Errorf("nothing may be sent: %v", f.paths(""))
	}
}

// ---- envelope ----

func TestSuccessFalseOn200IsAnError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"success":false,"errors":[{"code":6003,"message":"Invalid request headers"},{"code":6111,"message":"Invalid format for Authorization header"}],"messages":[],"result":null}`)
	}))
	defer srv.Close()
	f := &fakeCF{srv: srv}
	run := setup(t, f)
	for _, args := range [][]string{{"check"}, {"call", "zones"}, {"call", "token_verify"}, {"zones"}} {
		code, out, errs := run(args...)
		if code != 1 || !strings.Contains(errs, "6003") || !strings.Contains(errs, "Invalid request headers") || !strings.Contains(errs, "6111") {
			t.Errorf("%v: a success:false envelope with HTTP 200 must be an error: %d out=%q err=%q", args, code, out, errs)
		}
	}
}

func TestCheckUsesAccountEndpointWhenUserVerifyFails(t *testing.T) {
	f := newFake(t)
	run := setup(t, f)
	code, out, errs := run("check")
	m := decode(t, ok(t, code, out, errs))
	if m["ok"] != true || m["detail"].(map[string]any)["status"] != "active" {
		t.Errorf("%v", m)
	}
	if p := f.paths(""); len(p) != 1 || p[0] != "/user/tokens/verify" {
		t.Errorf("%v", p)
	}
	// the user endpoint rejects it; the account endpoint accepts it
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/user/tokens/verify" {
			w.WriteHeader(400)
			fmt.Fprint(w, failure(1000, "Invalid API Token"))
			return
		}
		fmt.Fprint(w, envelope(`{"id":"t","status":"active"}`, ""))
	}))
	defer srv.Close()
	code, out, errs = setup(t, &fakeCF{srv: srv})("check")
	if ok(t, code, out, errs); !strings.Contains(out, "active") {
		t.Errorf("%s", out)
	}
	// and without an account id the user error is what you see
	code, _, errs = setupWith(t, &fakeCF{srv: srv}, testToken, "")("check")
	if code != 1 || !strings.Contains(errs, "Invalid API Token") {
		t.Errorf("%s", errs)
	}
}

func TestRewriteEnvelope(t *testing.T) {
	out, changed := rewriteEnvelope([]byte(`{"success":false,"errors":[{"code":1003,"message":"Invalid zone"}],"result":null}`), true)
	var m map[string]any
	_ = json.Unmarshal(out, &m)
	if !changed || m["message"] != "the API answered 200 but success is false: [1003] Invalid zone" {
		t.Errorf("%s", out)
	}
	if _, changed := rewriteEnvelope([]byte(`{"success":true,"errors":[],"result":[]}`), true); changed {
		t.Error("a success envelope is left alone")
	}
	if _, changed := rewriteEnvelope([]byte(`<html>`), false); changed {
		t.Error("not JSON is left alone")
	}
	if _, changed := rewriteEnvelope([]byte(`[1,2]`), true); changed {
		t.Error("a non-object is left alone")
	}
	out, changed = rewriteEnvelope([]byte(`{"success":false,"errors":[]}`), false)
	_ = json.Unmarshal(out, &m)
	if !changed || !strings.Contains(m["message"].(string), "without an error message") {
		t.Errorf("%s", out)
	}
}

// ---- tunnels ----

func TestTunnels(t *testing.T) {
	f := newFake(t)
	code, out, errs := setup(t, f)("tunnels", "--status", "healthy")
	m := decode(t, ok(t, code, out, errs))
	ts := m["tunnels"].([]any)
	if len(ts) != 2 || ts[0].(map[string]any)["name"] != "api" || ts[1].(map[string]any)["connections"] != float64(2) {
		t.Errorf("%v", m)
	}
	q := f.requests("")[0]
	if q.Path != "/accounts/"+acctID+"/cfd_tunnel" || q.Query.Get("is_deleted") != "false" || q.Query.Get("status") != "healthy" {
		t.Errorf("%s", q.URI)
	}
}

func TestTunnelsNeedAnAccountID(t *testing.T) {
	f := newFake(t)
	code, _, errs := setupWith(t, f, testToken, "")("tunnels")
	if code != 1 || !strings.Contains(errs, "account_id") || len(f.requests("")) != 0 {
		t.Errorf("%d %s", code, errs)
	}
}

// ---- generic call, endpoints, secrets ----

func TestGenericCallPagesAndSafety(t *testing.T) {
	f := newFake(t)
	run := setup(t, f)
	code, out, errs := run("call", "dns_records", "--param", "zone_id="+zoneA, "--pages", "2")
	ok(t, code, out, errs)
	var l []any
	if err := json.Unmarshal([]byte(out), &l); err != nil || len(l) != 3 {
		t.Errorf("%v %s", err, out)
	}
	if code, _, e := run("call", "dns_record_delete", "--param", "zone_id="+zoneA, "--param", "dns_record_id="+recID); code != 1 || !strings.Contains(e, "above the allowed level") {
		t.Errorf("a destructive endpoint is refused by default: %d %s", code, e)
	}
	if code, _, _ := run("call", "dns_records", "--max-safety", "read", "--param", "zone_id="+zoneA); code != 0 {
		t.Error("reads work at --max-safety read")
	}
	if code, _, _ := run("call", "purge_cache", "--max-safety", "read", "--param", "zone_id="+zoneA, "--param", "purge_everything=true"); code != 1 {
		t.Error("a write is refused at --max-safety read")
	}
	if n := len(f.requests("DELETE")) + len(f.requests("POST")); n != 0 {
		t.Errorf("%d refused writes were sent", n)
	}
}

func TestEndpointSafetyLevels(t *testing.T) {
	reg, err := apikit.LoadRegistry(builtin, nil)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]string{
		"token_verify": "read", "accounts": "read", "zones": "read", "zone": "read", "dns_records": "read", "dns_record": "read",
		"zone_settings": "read", "tunnels": "read", "tunnel": "read", "tunnel_configuration": "read", "pages_projects": "read", "pages_deployments": "read",
		"dns_record_create": "write", "dns_record_update": "write", "purge_cache": "write", "dns_record_delete": "destructive",
	}
	for name, safety := range want {
		ep, found := reg.Get(name)
		if !found || ep.Safety != safety {
			t.Errorf("%s: %v", name, ep)
		}
	}
	for _, n := range []string{"zones", "dns_records", "tunnels", "accounts"} {
		ep, _ := reg.Get(n)
		if ep.Paginate == nil || ep.Paginate.Kind != "page" || ep.Paginate.TotalPath != "result_info.total_pages" {
			t.Errorf("%s must paginate by page number", n)
		}
	}
	for n, m := range map[string]string{"dns_record_create": "POST", "dns_record_update": "PATCH", "dns_record_delete": "DELETE", "purge_cache": "POST"} {
		if ep, _ := reg.Get(n); ep.Method != m {
			t.Errorf("%s is %s", n, ep.Method)
		}
	}
}

func TestTokenFromEnvAndNeverPrinted(t *testing.T) {
	f := newFake(t)
	t.Setenv("CF_TEST_TOKEN", testToken)
	cfg := filepath.Join(t.TempDir(), "c.toml")
	body := fmt.Sprintf("default='main'\n[accounts.main]\napi_token_env='CF_TEST_TOKEN'\nbase_url=%q\n", f.srv.URL)
	if err := os.WriteFile(cfg, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	var out, errb bytes.Buffer
	code := apikit.RunArgs(service(), []string{"zones", "--config", cfg}, &out, &errb)
	if code != 0 {
		t.Fatalf("%d %s", code, errb.String())
	}
	if strings.Contains(out.String()+errb.String(), testToken) {
		t.Error("the token must not appear in output")
	}
}

func TestHelpers(t *testing.T) {
	if fullName("www", "Example.com") != "www.example.com" || fullName("@", "example.com") != "example.com" || fullName("a.example.com.", "example.com") != "a.example.com" || fullName("example.com", "example.com") != "example.com" {
		t.Error("fullName")
	}
	if validTTL(1) != nil || validTTL(60) != nil || validTTL(86400) != nil || validTTL(0) == nil || validTTL(86401) == nil || validTTL(2) == nil {
		t.Error("validTTL")
	}
	if _, err := recordType("srv"); err != nil {
		t.Error(err)
	}
}

func runOK(t *testing.T, run func(args ...string) (int, string, string), args ...string) string {
	t.Helper()
	code, out, errs := run(args...)
	return ok(t, code, out, errs)
}

func TestStarterConfigIsValid(t *testing.T) {
	cfg := filepath.Join(t.TempDir(), "c.toml")
	var out, errb bytes.Buffer
	if code := apikit.RunArgs(service(), []string{"init", "--config", cfg}, &out, &errb); code != 0 {
		t.Fatalf("%d %s", code, errb.String())
	}
	out.Reset()
	if code := apikit.RunArgs(service(), []string{"accounts", "--config", cfg}, &out, &errb); code != 0 || !strings.Contains(out.String(), `"main"`) {
		t.Errorf("%d %s %s", code, out.String(), errb.String())
	}
}
