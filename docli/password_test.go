package main

import (
	"encoding/json"
	"fmt"
	"os"
	"regexp"
	"strings"
	"testing"
)

// Database passwords must never reach stdout, stderr, --json, --debug, the manifest or an error, whether the
// server returns them in an answer or echoes them in a failure. The only place one may go is a --password-file.

var secretMarkers = []string{fakeDBPassword, fakeUserPassword, "AVNS_", "FAKEDBPASSWORD", "FAKEUSERPASS"}

// a connection string with credentials in it (a scrubbed one reads postgresql://[credentials hidden]@host)
var credURIRE = regexp.MustCompile(`://[^/\s\[]+:[^\s@\[]+@`)

func assertNoSecrets(t *testing.T, where string, args []string, e *testEnv) {
	t.Helper()
	all := e.out.String() + "\n" + e.errb.String()
	if credURIRE.MatchString(all) {
		t.Errorf("[%s] docli %v printed a connection string with credentials:\n%s", where, args, all)
	}
	for _, m := range secretMarkers {
		if strings.Contains(all, m) {
			t.Errorf("[%s] docli %v leaked %q:\n%s", where, args, m, all)
		}
	}
}

func TestPasswordsNeverPrinted(t *testing.T) {
	pwDir := t.TempDir()
	n := 0
	pwf := func() string { n++; return pwDir + "/pw" + itoa(n) }
	cmds := func() [][]string {
		return [][]string{
			{"db", "list"}, {"db", "list", "--json"}, {"db", "list", "--debug"},
			{"db", "get", "legacy-pg"}, {"db", "get", "legacy-pg", "--json", "--debug"},
			{"db", "connection", "legacy-pg"}, {"db", "connection", "legacy-pg", "--json"}, {"db", "connection", "legacy-pg", "--user", "app", "--debug"},
			{"db", "connection", "legacy-pg", "--password-file", pwf()}, {"db", "connection", "legacy-pg", "--password-file", pwf(), "--json", "--debug"},
			{"db", "user", "list", "legacy-pg"}, {"db", "user", "list", "legacy-pg", "--json", "--debug"},
			{"db", "user", "create", "legacy-pg", "worker", "--yes"}, {"db", "user", "create", "legacy-pg", "worker2", "--yes", "--json", "--debug"},
			{"db", "user", "create", "legacy-pg", "worker3", "--yes", "--password-file", pwf()},
			{"db", "user", "delete", "legacy-pg", "app", "--force-unmanaged", "--yes", "--confirm-name", "app"},
			{"db", "database", "list", "legacy-pg", "--json"}, {"db", "pool", "list", "legacy-pg", "--json"}, {"db", "pool", "list", "legacy-pg"},
			{"db", "pool", "create", "legacy-pg", "--name", "p", "--database", "defaultdb", "--user", "doadmin", "--yes", "--json", "--debug"},
			{"db", "firewall", "list", "legacy-pg", "--json"}, {"db", "backups", "list", "legacy-pg"},
			{"db", "create", "--name", "app-db", "--region", "nyc3", "--version", "16", "--size", "db-s-1vcpu-1gb", "--nodes", "1", "--vpc", "default-nyc3", "--trusted-ip", "1.2.3.4", "--yes", "--wait", "--debug", "--json"},
			{"db", "create", "--name", "app-db", "--region", "nyc3", "--version", "16", "--size", "db-s-1vcpu-1gb", "--nodes", "1", "--vpc", "default-nyc3", "--trusted-ip", "1.2.3.4", "--yes", "--wait", "--password-file", pwf()},
			{"db", "resize", "legacy-pg", "--nodes", "2", "--yes"}, {"db", "delete", "legacy-pg", "--force-unmanaged", "--yes", "--confirm-name", "legacy-pg"},
			{"db", "wait", "legacy-pg", "--debug"}, {"manifest", "show"}, {"manifest", "verify", "--debug"},
		}
	}
	modes := map[string]func(e *testEnv){
		"good": func(e *testEnv) {},
		"server fails and echoes": func(e *testEnv) {
			for _, k := range []string{"GET /v2/databases", "POST /v2/databases", "GET /v2/databases/db-legacy", "POST /v2/databases/db-legacy/users", "GET /v2/databases/db-legacy/users", "POST /v2/databases/db-legacy/pools", "DELETE /v2/databases/db-legacy", "PUT /v2/databases/db-legacy/resize", "GET /v2/databases/db-legacy/firewall"} {
				e.fake.Fails[k] = []fakeFail{{Status: 500}, {Status: 500}, {Status: 500}, {Status: 500}}
			}
		},
		"server echoes a user password in an error": func(e *testEnv) {
			e.fake.Fails["POST /v2/databases/db-legacy/users"] = []fakeFail{{Status: 422, Message: "user rejected: password " + fakeUserPassword + " too weak; uri postgresql://worker:" + fakeUserPassword + "@host:25060/db"}}
			e.fake.Fails["DELETE /v2/databases/db-legacy/users/app"] = []fakeFail{{Status: 422, Message: "cannot drop " + fakeUserPassword}}
		},
		"server echoes a connection string it was never shown": func(e *testEnv) {
			e.fake.Fails["POST /v2/databases/db-legacy/pools"] = []fakeFail{{Status: 422, Message: "bad pool postgresql://doadmin:NEVERSEEN-xyz@h:25060/x"}}
		},
		"server rejects the token": func(e *testEnv) { e.fake.token = "other" },
	}
	for mode, setup := range modes {
		for _, args := range cmds() {
			e := newEnv(t)
			e.env["DOCLI_REGION"] = "nyc3"
			setup(e)
			e.tty(false)
			e.run(args...)
			assertNoSecrets(t, mode, args, e)
			notContain(t, e.out.String()+e.errb.String(), "NEVERSEEN")
			if m, err := os.ReadFile(e.manifest()); err == nil {
				for _, s := range secretMarkers {
					if strings.Contains(string(m), s) {
						t.Errorf("[%s] docli %v wrote %q to the manifest", mode, args, s)
					}
				}
			}
		}
	}
}

func TestPasswordsNeverInJSONShapes(t *testing.T) {
	// the JSON structs have no field that could hold one
	var walk func(v any, path string)
	walk = func(v any, path string) {
		switch x := v.(type) {
		case map[string]any:
			for k, val := range x {
				switch k {
				case "password", "uri", "secret", "token", "access_key", "access_cert":
					t.Errorf("%s has a field named %q", path, k)
				}
				walk(val, path+"."+k)
			}
		case []any:
			for _, i := range x {
				walk(i, path+"[]")
			}
		}
	}
	for _, v := range []any{dbJSON{}, dbDetailJSON{TrustedSources: []sourceJSON{{}}}, connJSON{}, connectionJSON{}, dbUserJSON{}, poolJSON{}, sourceJSON{}, backupJSON{}, dbNameJSON{}} {
		var generic any
		json.Unmarshal([]byte(jsonString(v)), &generic)
		walk(generic, strings.TrimPrefix(fmt.Sprintf("%T", v), "main."))
	}
}

// The passwords the fake hands out are used by commands that must never print them. If a refactor made
// the fake stop returning them, the tests above would pass for the wrong reason.
func TestFakeReallyReturnsPasswords(t *testing.T) {
	e := newEnv(t)
	d, err := e.app.newGodoCloudForTest(e)
	if err != nil {
		t.Fatal(err)
	}
	dbs, err := d.ListDatabases(e.app.ctxForTest())
	if err != nil || dbs[0].Connection.Password != fakeDBPassword || !strings.Contains(dbs[0].Connection.URI, fakeDBPassword) {
		t.Fatalf("the fake must return a password and a URI containing it: %v", err)
	}
}
