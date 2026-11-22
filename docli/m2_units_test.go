package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/digitalocean/godo"
)

func TestRecordNames(t *testing.T) {
	good := map[string]string{"@": "@", "www": "www", "WWW": "www", "*.apps": "*.apps", "_dmarc": "_dmarc", "a.b.c": "a.b.c", "_acme-challenge.www": "_acme-challenge.www"}
	for in, want := range good {
		got, err := normalizeRecordName("example.com", in)
		if err != nil || got != want {
			t.Errorf("%q: got %q, %v", in, got, err)
		}
	}
	for _, bad := range []string{"", "www.example.com", "example.com", "www.", "-x", "a..b", "a b", "foo.*", "www.example.com."} {
		if _, err := normalizeRecordName("example.com", bad); err == nil {
			t.Errorf("%q should be refused", bad)
		}
	}
	_, err := normalizeRecordName("example.com", "www.example.com")
	if err == nil || !strings.Contains(err.Error(), `Write "www"`) {
		t.Errorf("the error should say what to write: %v", err)
	}
}

func TestRecordValidation(t *testing.T) {
	ok := func(s recordSpec) {
		t.Helper()
		if s.TTL == 0 {
			s.TTL = 300
		}
		if err := validateRecord(&s); err != nil {
			t.Errorf("%+v should be valid: %v", s, err)
		}
	}
	bad := func(want string, s recordSpec) {
		t.Helper()
		if s.TTL == 0 {
			s.TTL = 300
		}
		err := validateRecord(&s)
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%+v: want error containing %q, got %v", s, want, err)
		}
	}
	ok(recordSpec{Type: "A", Name: "www", Data: "203.0.113.5", Priority: -1})
	ok(recordSpec{Type: "AAAA", Name: "www", Data: "2001:db8::1", Priority: -1})
	ok(recordSpec{Type: "CNAME", Name: "blog", Data: "host.example.net.", Priority: -1})
	ok(recordSpec{Type: "CNAME", Name: "blog", Data: "@", Priority: -1})
	ok(recordSpec{Type: "TXT", Name: "@", Data: "v=spf1 include:_spf.example.net ~all", Priority: -1})
	ok(recordSpec{Type: "MX", Name: "@", Data: "mail.example.net.", Priority: 10})
	ok(recordSpec{Type: "NS", Name: "sub", Data: "ns1.example.net", Priority: -1})
	ok(recordSpec{Type: "CAA", Name: "@", Data: "letsencrypt.org", Priority: -1, Tag: "issue"})
	bad("not an IPv4", recordSpec{Type: "A", Name: "www", Data: "2001:db8::1", Priority: -1})
	bad("not an IPv4", recordSpec{Type: "A", Name: "www", Data: "999.1.1.1", Priority: -1})
	bad("not an IPv6", recordSpec{Type: "AAAA", Name: "www", Data: "203.0.113.5", Priority: -1})
	bad("not an IPv6", recordSpec{Type: "AAAA", Name: "www", Data: "::ffff:203.0.113.5", Priority: -1})
	bad("CNAME cannot be at the apex", recordSpec{Type: "CNAME", Name: "@", Data: "x.example.net", Priority: -1})
	bad("IP address", recordSpec{Type: "CNAME", Name: "x", Data: "203.0.113.5", Priority: -1})
	bad("not a valid host name", recordSpec{Type: "MX", Name: "@", Data: "bad host", Priority: 10})
	bad("needs --priority", recordSpec{Type: "MX", Name: "@", Data: "mail.example.net", Priority: -1})
	bad("needs --priority", recordSpec{Type: "MX", Name: "@", Data: "mail.example.net", Priority: 70000})
	bad("only applies to MX", recordSpec{Type: "A", Name: "x", Data: "203.0.113.5", Priority: 5})
	bad("only apply to CAA", recordSpec{Type: "A", Name: "x", Data: "203.0.113.5", Priority: -1, Tag: "issue"})
	bad("needs --tag", recordSpec{Type: "CAA", Name: "@", Data: "letsencrypt.org", Priority: -1})
	bad("0 or 128", recordSpec{Type: "CAA", Name: "@", Data: "x", Priority: -1, Tag: "issue", Flags: 7})
	bad("line breaks", recordSpec{Type: "TXT", Name: "@", Data: "a\nb", Priority: -1})
	bad("at most", recordSpec{Type: "TXT", Name: "@", Data: strings.Repeat("x", maxTXT+1), Priority: -1})
	bad("not supported", recordSpec{Type: "SRV", Name: "@", Data: "x", Priority: -1})
	bad("needs --data", recordSpec{Type: "A", Name: "x", Priority: -1})
	bad("out of range", recordSpec{Type: "A", Name: "x", Data: "203.0.113.5", Priority: -1, TTL: 5})
	bad("cannot point at @", recordSpec{Type: "NS", Name: "sub", Data: "@", Priority: -1})
}

func TestRecordConflicts(t *testing.T) {
	zone := []godo.DomainRecord{
		{ID: 1, Type: "A", Name: "www", Data: "203.0.113.5"},
		{ID: 2, Type: "CNAME", Name: "blog", Data: "x.example.net."},
	}
	cases := []struct {
		s      recordSpec
		except int
		want   string
	}{
		{recordSpec{Type: "A", Name: "www", Data: "203.0.113.5"}, 0, "already has that record"},
		{recordSpec{Type: "A", Name: "www", Data: "203.0.113.5"}, 1, ""}, // updating the record itself
		{recordSpec{Type: "A", Name: "www", Data: "203.0.113.6"}, 0, ""}, // round-robin A records are fine
		{recordSpec{Type: "CNAME", Name: "www", Data: "x.example.net"}, 0, "cannot share its name"},
		{recordSpec{Type: "TXT", Name: "blog", Data: "hi"}, 0, "already a CNAME"},
		{recordSpec{Type: "TXT", Name: "other", Data: "hi"}, 0, ""},
	}
	for _, c := range cases {
		err := recordConflict(&c.s, zone, c.except)
		if (c.want == "") != (err == nil) || (err != nil && !strings.Contains(err.Error(), c.want)) {
			t.Errorf("%+v: got %v, want %q", c.s, err, c.want)
		}
	}
}

func TestDomainNames(t *testing.T) {
	for _, n := range []string{"example.com", "a-b.example.co.uk", "x1.io"} {
		if err := validateDomainName(n); err != nil {
			t.Errorf("%s: %v", n, err)
		}
	}
	for _, n := range []string{"example", "Example.com", "example.com.", "*.example.com", "-a.com", "a..com", "exa mple.com", ""} {
		if validateDomainName(n) == nil {
			t.Errorf("%q should be refused", n)
		}
	}
}

func TestCheckDBOptions(t *testing.T) {
	o := &godo.DatabaseOptions{PostgresSQLOptions: godo.DatabaseEngineOptions{Regions: []string{"nyc3"}, Versions: []string{"15", "16"},
		Layouts: []godo.DatabaseLayout{{NodeNum: 1, Sizes: []string{"db-s-1vcpu-1gb"}}, {NodeNum: 2, Sizes: []string{"db-s-2vcpu-4gb"}}}}}
	if err := checkDBOptions(o, "16", "nyc3", "db-s-1vcpu-1gb", 1); err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		v, r, s string
		n       int
		want    string
	}{
		{"9", "nyc3", "db-s-1vcpu-1gb", 1, "version"},
		{"16", "mars1", "db-s-1vcpu-1gb", 1, "not offered in"},
		{"16", "nyc3", "db-s-1vcpu-1gb", 3, "Available node counts: 1, 2"},
		{"16", "nyc3", "db-s-2vcpu-4gb", 1, "not offered for 1 node"},
	} {
		if err := checkDBOptions(o, c.v, c.r, c.s, c.n); err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%+v: %v", c, err)
		}
	}
	if err := checkDBOptions(&godo.DatabaseOptions{}, "16", "x", "y", 9); err != nil {
		t.Errorf("an empty option list must not block: %v", err)
	}
}

func TestSecretFileRules(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("file modes do not apply")
	}
	dir := t.TempDir()
	p := filepath.Join(dir, "db_password")
	if err := checkSecretTarget(p, false); err != nil {
		t.Fatal(err)
	}
	if err := writeSecretFile(p, "s3cret-value", false); err != nil {
		t.Fatal(err)
	}
	fi, _ := os.Stat(p)
	b, _ := os.ReadFile(p)
	if fi.Mode().Perm() != 0o600 || string(b) != "s3cret-value" {
		t.Fatalf("mode %v content %q", fi.Mode().Perm(), b)
	}
	// an existing file is never overwritten without force, neither by the early check nor the write itself
	if err := checkSecretTarget(p, false); err == nil || !strings.Contains(err.Error(), "--force") {
		t.Fatalf("early check: %v", err)
	}
	if err := writeSecretFile(p, "other", false); err == nil {
		t.Fatal("exclusive create must fail on an existing file")
	}
	if b, _ := os.ReadFile(p); string(b) != "s3cret-value" {
		t.Fatalf("the existing file was changed: %q", b)
	}
	if err := checkSecretTarget(p, true); err != nil {
		t.Fatal(err)
	}
	os.Chmod(p, 0o644)
	if err := writeSecretFile(p, "replaced", true); err != nil {
		t.Fatal(err)
	}
	fi, _ = os.Stat(p)
	b, _ = os.ReadFile(p)
	if fi.Mode().Perm() != 0o600 || string(b) != "replaced" {
		t.Fatalf("force: mode %v content %q", fi.Mode().Perm(), b)
	}
	if left, _ := filepath.Glob(filepath.Join(dir, ".docli-secret-*")); len(left) != 0 {
		t.Fatalf("temp files left behind: %v", left)
	}
	if err := checkSecretTarget(filepath.Join(dir, "no", "such", "dir", "f"), false); err == nil {
		t.Fatal("a missing directory should be reported before anything is created")
	}
	os.Mkdir(filepath.Join(dir, "sub"), 0o755)
	if err := checkSecretTarget(filepath.Join(dir, "sub"), true); err == nil {
		t.Fatal("a directory must not be replaced")
	}
	// errors carry the path, never the secret
	err := writeSecretFile(filepath.Join(dir, "no", "such", "f"), "TOPSECRET-VALUE", false)
	if err == nil || strings.Contains(err.Error(), "TOPSECRET") {
		t.Fatalf("%v", err)
	}
}

func TestScrubSecrets(t *testing.T) {
	a := &App{}
	a.rememberSecret("hunter2-hunter2")
	a.rememberSecret("ab") // too short to be a secret: would scrub ordinary words
	got := a.scrubSecrets("failed for hunter2-hunter2 and ab and postgresql://doadmin:NEVERSEEN@host:25060/db and mysql://u:p@h")
	for _, bad := range []string{"hunter2-hunter2", "NEVERSEEN", ":p@"} {
		if strings.Contains(got, bad) {
			t.Errorf("%q leaked: %s", bad, got)
		}
	}
	if !strings.Contains(got, " ab ") {
		t.Errorf("short values must be left alone: %s", got)
	}
}

func jsonString(v any) string {
	b, _ := json.Marshal(v)
	return string(b)
}

func (a *App) newGodoCloudForTest(e *testEnv) (Cloud, error) {
	return a.newGodoCloud(fakeToken, Globals{Timeout: 5 * time.Second})
}

func (a *App) ctxForTest() context.Context { return context.Background() }
