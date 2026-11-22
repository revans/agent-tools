package main

import (
	"strings"
	"testing"
)

// The token must not appear anywhere in the output of the new commands either, in any failure mode.
func TestTokenNeverPrintedM2(t *testing.T) {
	cmds := [][]string{
		{"domain", "list", "--debug"}, {"domain", "list", "--json"}, {"domain", "get", "legacy.example", "--debug", "--json"}, {"domain", "get", "ghost.example"},
		{"domain", "create", "--name", "shop.example", "--yes", "--debug"}, {"domain", "create", "--name", "shop.example", "--dry-run", "--json"},
		{"domain", "delete", "legacy.example", "--force-unmanaged", "--yes", "--confirm-name", "legacy.example", "--debug"}, {"domain", "delete", "legacy.example", "--yes"},
		{"dns", "list", "legacy.example", "--debug"}, {"dns", "list", "ghost.example", "--json"},
		{"dns", "create", "legacy.example", "--type", "TXT", "--name", "t", "--data", "v", "--yes", "--debug"},
		{"dns", "update", "legacy.example", "901", "--data", "203.0.113.1", "--force-unmanaged", "--yes", "--confirm-name", "legacy.example", "--json"},
		{"dns", "delete", "legacy.example", "902", "--yes"}, {"dns", "create", "legacy.example", "--type", "A", "--name", "bad_name!", "--data", "x"},
		{"db", "list", "--debug"}, {"db", "get", "legacy-pg", "--json"}, {"db", "get", "ghost"},
		{"db", "create", "--name", "app-db", "--region", "nyc3", "--version", "16", "--size", "db-s-1vcpu-1gb", "--nodes", "1", "--vpc", "default-nyc3", "--trusted-ip", "1.2.3.4", "--yes", "--wait", "--debug"},
		{"db", "create", "--name", "app-db", "--region", "nyc3", "--version", "16", "--size", "db-s-1vcpu-1gb", "--nodes", "1", "--vpc", "default-nyc3", "--trusted-ip", "1.2.3.4", "--dry-run", "--json"},
		{"db", "delete", "legacy-pg", "--force-unmanaged", "--yes", "--confirm-name", "legacy-pg", "--debug"},
		{"db", "resize", "legacy-pg", "--nodes", "2", "--yes", "--debug"}, {"db", "wait", "legacy-pg", "--debug"},
		{"db", "connection", "legacy-pg", "--debug", "--json"}, {"db", "user", "list", "legacy-pg", "--debug"}, {"db", "user", "create", "legacy-pg", "x", "--yes", "--debug"},
		{"db", "database", "list", "legacy-pg", "--debug"}, {"db", "pool", "list", "legacy-pg", "--debug"}, {"db", "backups", "list", "legacy-pg", "--debug"},
		{"db", "firewall", "list", "legacy-pg", "--debug"}, {"db", "firewall", "set", "legacy-pg", "--ip", "1.2.3.4", "--yes", "--debug"},
		{"manifest", "verify", "--debug"}, {"manifest", "refresh", "--yes", "--debug"}, {"token", "check", "--json"},
	}
	modes := map[string]func(e *testEnv){
		"good token":               func(e *testEnv) {},
		"server rejects the token": func(e *testEnv) { e.fake.token = "some-other-token" },
		"server fails and echoes": func(e *testEnv) {
			for _, k := range []string{"GET /v2/domains", "GET /v2/databases", "POST /v2/domains", "POST /v2/databases", "GET /v2/domains/legacy.example/records", "GET /v2/databases/db-legacy"} {
				e.fake.Fails[k] = []fakeFail{{Status: 500}, {Status: 500}, {Status: 500}, {Status: 500}}
			}
		},
		"token in env DOCLI_TOKEN": func(e *testEnv) { e.env["DOCLI_TOKEN"] = fakeToken; e.env["DIGITALOCEAN_TOKEN"] = "" },
		"token in a file": func(e *testEnv) {
			e.env = map[string]string{"DOCLI_REGION": "nyc3"}
			writeTokenFile(t, e.home, 0o600, fakeToken+"\n")
		},
	}
	for mode, setup := range modes {
		for _, args := range cmds {
			e := newEnv(t)
			e.env["DOCLI_REGION"] = "nyc3"
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

func TestTokenCheckListsDNSAndDatabases(t *testing.T) {
	e := newEnv(t)
	e.fake.Denied["/v2/databases"] = true
	e.mustRun("token", "check", "--json")
	var tc tokenCheckJSON
	jsonOut(t, e, &tc)
	got := map[string]string{}
	for _, p := range tc.ReadAccess {
		got[p.Resource] = p.Read
	}
	if got["domains"] != "allowed" || got["databases"] != "denied" {
		t.Fatalf("%v", got)
	}
}
