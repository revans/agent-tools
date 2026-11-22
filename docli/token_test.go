package main

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func writeTokenFile(t *testing.T, home string, mode os.FileMode, content string) string {
	t.Helper()
	dir := filepath.Join(home, ".config", "docli")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(dir, "token")
	if err := os.WriteFile(p, []byte(content), mode); err != nil {
		t.Fatal(err)
	}
	os.Chmod(p, mode)
	return p
}

func TestTokenSources(t *testing.T) {
	e := newEnv(t)
	e.env = map[string]string{}
	e.expect(exitError, "droplet", "list")
	contain(t, e.errb.String(), "no token found")

	e.env["DIGITALOCEAN_TOKEN"] = "from-do"
	if tok, _ := e.app.loadToken(); tok != "from-do" {
		t.Fatal(tok)
	}
	e.env["DOCLI_TOKEN"] = "  from-docli \n"
	if tok, _ := e.app.loadToken(); tok != "from-docli" {
		t.Fatalf("DOCLI_TOKEN should win and be trimmed, got %q", tok)
	}
	delete(e.env, "DOCLI_TOKEN")
	delete(e.env, "DIGITALOCEAN_TOKEN")
	writeTokenFile(t, e.home, 0o600, fakeToken+"\n")
	if tok, err := e.app.loadToken(); err != nil || tok != fakeToken {
		t.Fatalf("%q %v", tok, err)
	}
	e.mustRun("droplet", "list") // works with the file
}

func TestTokenFileModeEnforced(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("file modes do not apply")
	}
	for _, mode := range []os.FileMode{0o644, 0o640, 0o604, 0o666, 0o660} {
		e := newEnv(t)
		e.env = map[string]string{}
		writeTokenFile(t, e.home, mode, fakeToken)
		code := e.run("droplet", "list")
		if code != exitError {
			t.Errorf("mode %04o: exit %d, want %d", mode, code, exitError)
		}
		contain(t, e.errb.String(), "chmod 600")
		notContain(t, e.errb.String(), fakeToken)
		if len(e.fake.Log) != 0 {
			t.Errorf("mode %04o: a request was made with an unsafe token file", mode)
		}
	}
	for _, mode := range []os.FileMode{0o600, 0o400} {
		e := newEnv(t)
		e.env = map[string]string{}
		writeTokenFile(t, e.home, mode, fakeToken)
		if _, err := e.app.loadToken(); err != nil {
			t.Errorf("mode %04o should be accepted: %v", mode, err)
		}
	}
	e := newEnv(t)
	e.env = map[string]string{}
	writeTokenFile(t, e.home, 0o600, "  \n")
	if _, err := e.app.loadToken(); err == nil || !strings.Contains(err.Error(), "empty") {
		t.Fatalf("empty file: %v", err)
	}
}

func TestTokenNeverAcceptedAsFlag(t *testing.T) {
	e := newEnv(t)
	e.expect(exitUsage, "--token", "abc", "droplet", "list")
	e.expect(exitUsage, "droplet", "list", "--token=abc")
	if len(e.fake.Log) != 0 {
		t.Fatal("no request should be made")
	}
}

// The token must not appear anywhere: stdout, stderr, --json, --debug, error paths, even when the server echoes it.
func TestTokenNeverPrinted(t *testing.T) {
	cmds := [][]string{
		{"token", "check"}, {"token", "check", "--json"}, {"token", "check", "--debug"},
		{"sizes", "--debug"}, {"regions", "--json", "--debug"}, {"images", "--debug"},
		{"droplet", "list", "--debug", "--json"}, {"droplet", "get", "nonexistent", "--debug"}, {"droplet", "get", "999999", "--json"},
		{"vpc", "list", "--debug"}, {"firewall", "list", "--json"}, {"ssh-key", "list", "--debug"},
		{"droplet", "create", "--name", "web", "--region", "nyc3", "--size", "s-1vcpu-1gb", "--image", "x", "--yes", "--debug"},
		{"droplet", "create", "--name", "web", "--region", "nyc3", "--size", "s-1vcpu-1gb", "--image", "x", "--dry-run", "--json"},
		{"droplet", "delete", "legacy-db", "--yes", "--confirm-name", "legacy-db"},
		{"droplet", "create", "--name", "bad_name", "--region", "nyc3", "--size", "s-1vcpu-1gb", "--image", "x"},
		{"manifest", "verify", "--debug"}, {"nonsense"}, {"--json", "nonsense"},
	}
	modes := map[string]func(e *testEnv){
		"good token":               func(e *testEnv) {},
		"server rejects the token": func(e *testEnv) { e.fake.token = "some-other-token" },
		"server fails and echoes": func(e *testEnv) {
			e.fake.Fails["GET /v2/droplets"] = []fakeFail{{Status: 500}, {Status: 500}, {Status: 500}, {Status: 500}}
		},
		"token in env DOCLI_TOKEN": func(e *testEnv) { e.env["DOCLI_TOKEN"] = fakeToken; e.env["DIGITALOCEAN_TOKEN"] = "" },
	}
	for mode, setup := range modes {
		for _, args := range cmds {
			e := newEnv(t)
			setup(e)
			e.tty(true)
			e.run(args...)
			all := e.out.String() + e.errb.String()
			if strings.Contains(all, fakeToken) || strings.Contains(all, "FAKETOKEN") {
				t.Errorf("[%s] docli %v leaked the token:\n%s", mode, args, all)
			}
		}
	}
}

func TestDebugShowsRequestsNotSecrets(t *testing.T) {
	e := newEnv(t)
	e.mustRun("regions", "--debug")
	contain(t, e.errb.String(), "debug: GET ")
	contain(t, e.errb.String(), "/v2/regions")
	contain(t, e.errb.String(), "-> 200")
	notContain(t, e.errb.String(), "Authorization")
	notContain(t, e.errb.String(), "Bearer")
	// The token source is named, never the value.
	e.mustRun("token", "check")
	contain(t, e.out.String(), "DIGITALOCEAN_TOKEN")
}

func TestRedact(t *testing.T) {
	if got := redact("a "+fakeToken+" b", fakeToken); strings.Contains(got, fakeToken) {
		t.Fatal(got)
	}
	if redact("plain", "") != "plain" {
		t.Fatal("empty token must change nothing")
	}
}
