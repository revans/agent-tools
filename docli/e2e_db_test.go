package main

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/digitalocean/godo"
)

func dbArgs(extra ...string) []string {
	base := []string{"db", "create", "--name", "app-db", "--region", "nyc3", "--version", "16", "--size", "db-s-1vcpu-1gb", "--nodes", "1",
		"--vpc", "default-nyc3", "--trusted-droplet", "legacy-db"}
	return append(base, extra...)
}

func pwPath(t *testing.T) string { return filepath.Join(t.TempDir(), "db_password") }

// ---------- create ----------

func TestDBCreatePlanAndCost(t *testing.T) {
	e := newEnv(t)
	e.fake.ExposeDBSizes = true
	e.mustRun(dbArgs("--nodes", "2", "--stack", "blog", "--tag", "app", "--dry-run")...)
	out := e.out.String()
	contain(t, out, `create PostgreSQL 16 cluster "app-db" in nyc3`)
	contain(t, out, "db-s-1vcpu-1gb x 2 node(s)")
	contain(t, out, "network: VPC default-nyc3 (10.116.0.0/20)")
	contain(t, out, "trusted sources: droplet legacy-db (id 1001)")
	contain(t, out, "docli:managed, docli:stack:blog, app")
	contain(t, out, "$15.00 per node per month x 2 node(s)")
	contain(t, out, "Estimated cost: $30.00 per month")
	contain(t, out, "Dry run")
	if len(e.fake.Mutations()) != 0 {
		t.Fatalf("dry run changed something: %v", e.fake.Mutations())
	}
	e.mustRun(dbArgs("--dry-run", "--json")...)
	var doc struct {
		DryRun bool `json:"dry_run"`
		Plan   Plan `json:"plan"`
	}
	jsonOut(t, e, &doc)
	if !doc.DryRun || doc.Plan.MonthlyUSD == nil || *doc.Plan.MonthlyUSD != 15 || doc.Plan.Action != "db.create" {
		t.Fatalf("%+v", doc)
	}
}

// NOT VERIFIED against the real API: whether /v2/sizes lists database sizes. When it does not, the plan says
// the cost is unknown instead of printing a made-up number.
func TestDBCreateWithoutPriceSaysSo(t *testing.T) {
	e := newEnv(t)
	e.mustRun(dbArgs("--dry-run")...)
	contain(t, e.out.String(), "NOT shown")
	notContain(t, e.out.String(), "Estimated cost")
	e.mustRun(dbArgs("--dry-run", "--json")...)
	var doc struct {
		Plan struct {
			MonthlyUSD *float64 `json:"monthly_cost_usd"`
		} `json:"plan"`
	}
	jsonOut(t, e, &doc)
	if doc.Plan.MonthlyUSD != nil {
		t.Fatal("an unknown price must be null, not zero")
	}
}

func TestDBCreateNeedsConfirmation(t *testing.T) {
	e := newEnv(t)
	e.tty(false)
	e.expect(exitRefused, dbArgs()...)
	contain(t, e.errb.String(), "stdin is not a terminal")
	e.tty(true)
	e.stdin("\n")
	e.expect(exitRefused, dbArgs()...)
	e.stdin("n\n")
	e.expect(exitRefused, dbArgs()...)
	if len(e.fake.Mutations()) != 0 {
		t.Fatalf("no answer, or no, must not create anything: %v", e.fake.Mutations())
	}
	e.stdin("y\n")
	e.mustRun(dbArgs()...)
	if len(e.fake.Requests("POST /v2/databases")) != 1 {
		t.Fatal(e.fake.Log)
	}
}

func TestDBCreateEndToEnd(t *testing.T) {
	e := newEnv(t)
	e.env["DOCLI_REGION"] = "nyc3"
	pw := pwPath(t)
	e.mustRun(dbArgs("--stack", "blog", "--trusted-tag", "app", "--trusted-ip", "198.51.100.0/24", "--wait", "--password-file", pw, "--yes", "--json")...)
	var d dbJSON
	jsonOut(t, e, &d)
	if d.Status != "online" || !d.Managed || d.Engine != "pg" || d.Version != "16" || d.NumNodes != 1 || d.VPCID != "vpc-default-nyc3" || d.Connection.Port != 25060 || d.Connection.SSLMode != "require" {
		t.Fatalf("%+v", d)
	}
	for _, tag := range []string{"docli:managed", "docli:stack:blog"} {
		if !hasTag(d.Tags, tag) || !e.fake.Tags[tag] {
			t.Fatalf("tag %s missing: %v / %v", tag, d.Tags, e.fake.Tags)
		}
	}
	log := strings.Join(e.fake.Log, "\n")
	if strings.Index(log, "POST /v2/tags") > strings.Index(log, "POST /v2/databases") || strings.Count(log, "POST /v2/databases") != 1 {
		t.Fatalf("tags must be created first and the cluster once:\n%s", log)
	}
	body := e.fake.Bodies["POST /v2/databases"][0]
	for _, want := range []string{`"engine":"pg"`, `"version":"16"`, `"num_nodes":1`, `"private_network_uuid":"vpc-default-nyc3"`, `"size":"db-s-1vcpu-1gb"`, `"region":"nyc3"`,
		`"type":"droplet","value":"1001"`, `"type":"tag","value":"app"`, `"type":"ip_addr","value":"198.51.100.0/24"`} {
		contain(t, body, want)
	}
	contain(t, e.errb.String(), "waiting for database cluster app-db")
	contain(t, e.errb.String(), "is online")
	contain(t, e.errb.String(), "wrote the password to "+pw)
	if len(e.sleeps) == 0 {
		t.Fatal("waiting should sleep between polls")
	}
	b, err := os.ReadFile(pw)
	if err != nil || string(b) != fakeDBPassword {
		t.Fatalf("password file: %q %v", b, err)
	}
	if runtime.GOOS != "windows" {
		if fi, _ := os.Stat(pw); fi.Mode().Perm() != 0o600 {
			t.Fatalf("mode %v", fi.Mode().Perm())
		}
	}
	m := e.readManifest()
	for _, want := range []string{"databases:", "name: app-db", "host: app-db-do-user-1-0.db.ondigitalocean.com", "private_host: private-app-db", "port: 25060",
		"vpc_id: vpc-default-nyc3", "- droplet:1001", "- tag:app", "- ip_addr:198.51.100.0/24", "status: online"} {
		contain(t, m, want)
	}
}

func TestDBCreateRefusesOpenDatabases(t *testing.T) {
	e := newEnv(t)
	e.env["DOCLI_REGION"] = "nyc3"
	noSource := []string{"db", "create", "--name", "app-db", "--version", "16", "--size", "db-s-1vcpu-1gb", "--nodes", "1", "--vpc", "default-nyc3"}
	e.expect(exitRefused, append(noSource, "--yes")...)
	contain(t, e.errb.String(), "any address")
	e.expect(exitRefused, append(append([]string{}, noSource...), "--trusted-ip", "0.0.0.0/0", "--yes")...)
	if len(e.fake.Mutations()) != 0 {
		t.Fatal(e.fake.Mutations())
	}
	// --allow-open still needs the name typed
	e.tty(false)
	e.expect(exitRefused, append(append([]string{}, noSource...), "--allow-open", "--yes")...)
	contain(t, e.errb.String(), "--yes --confirm-name app-db")
	e.expect(exitRefused, append(append([]string{}, noSource...), "--allow-open", "--yes", "--confirm-name", "other")...)
	e.mustRun(append(append([]string{}, noSource...), "--allow-open", "--dry-run")...)
	contain(t, e.out.String(), "OPEN: no trusted sources")
	if len(e.fake.Mutations()) != 0 {
		t.Fatal(e.fake.Mutations())
	}
	e.mustRun(append(append([]string{}, noSource...), "--allow-open", "--yes", "--confirm-name", "app-db")...)
	if len(e.fake.Requests("POST /v2/databases")) != 1 {
		t.Fatal(e.fake.Log)
	}
}

func TestDBCreateValidation(t *testing.T) {
	e := newEnv(t)
	e.env["DOCLI_REGION"] = "nyc3"
	set := func(a []string, flag, val string) []string {
		out := append([]string{}, a...)
		for i := range out {
			if out[i] == flag {
				out[i+1] = val
				return out
			}
		}
		t.Fatalf("no %s in %v", flag, a)
		return nil
	}
	base := dbArgs("--yes")
	for _, c := range []struct {
		args []string
		code int
		want string
	}{
		{set(base, "--name", "Bad_Name"), exitUsage, "not a valid cluster name"},
		{set(base, "--nodes", "4"), exitUsage, "1, 2 or 3"},
		{[]string{"db", "create", "--name", "app-db", "--version", "16", "--size", "db-s-1vcpu-1gb", "--nodes", "1", "--trusted-ip", "1.2.3.4", "--yes"}, exitUsage, "VPC is required"},
		{[]string{"db", "create", "--name", "app-db", "--version", "16", "--size", "db-s-1vcpu-1gb", "--yes"}, exitUsage, "usage: docli db create"},
		{set(base, "--version", "9"), exitError, "version"},
		{set(base, "--size", "db-s-9vcpu"), exitError, "not offered for 1 node"},
		{set(base, "--vpc", "nope"), exitError, "no VPC named"},
		{set(base, "--vpc", "ghost"), exitError, "no VPC named"},
		{set(base, "--trusted-droplet", "ghost"), exitError, "no droplet named"},
		{set(base, "--name", "legacy-pg"), exitError, "already exists"},
		{append(append([]string{}, base...), "--tag", "bad tag"), exitUsage, "not a valid tag"},
		{append(append([]string{}, base...), "--stack", "bad stack"), exitUsage, "--stack"},
		{append(append([]string{}, base...), "--trusted-ip", "banana"), exitUsage, "not a valid IP"},
		{append(append([]string{}, base...), "--region", "sfo3"), exitError, "VPC"}, // the VPC is in nyc3
		{append(append([]string{}, base...), "--region", "mars1"), exitError, "not offered in"},
	} {
		if code := e.run(c.args...); code != c.code || !strings.Contains(e.errb.String(), c.want) {
			t.Errorf("%v: exit %d (want %d), stderr %q (want %q)", c.args, code, c.code, e.errb.String(), c.want)
		}
	}
	if len(e.fake.Mutations()) != 0 {
		t.Fatalf("validation failures must not change anything: %v", e.fake.Mutations())
	}
	e.env["DOCLI_REGION"] = ""
	noRegion := append([]string{}, base...)
	for i := range noRegion {
		if noRegion[i] == "--region" {
			noRegion = append(noRegion[:i], noRegion[i+2:]...)
			break
		}
	}
	e.expect(exitUsage, noRegion...)
	contain(t, e.errb.String(), "region is required")
}

func TestDBCreatePasswordFileRules(t *testing.T) {
	e := newEnv(t)
	e.env["DOCLI_REGION"] = "nyc3"
	pw := pwPath(t)
	os.WriteFile(pw, []byte("precious"), 0o600)
	// refused before anything is created, so no money is spent on a cluster whose password has nowhere to go
	e.expect(exitUsage, dbArgs("--password-file", pw, "--yes")...)
	contain(t, e.errb.String(), "--force")
	if len(e.fake.Log) != 0 {
		t.Fatalf("the file check must come before any request: %v", e.fake.Log)
	}
	if b, _ := os.ReadFile(pw); string(b) != "precious" {
		t.Fatal("the existing file was changed")
	}
	e.expect(exitUsage, dbArgs("--password-file", filepath.Join(t.TempDir(), "missing", "p"), "--yes")...)
	e.mustRun(dbArgs("--password-file", pw, "--force", "--yes")...)
	if b, _ := os.ReadFile(pw); string(b) != fakeDBPassword {
		t.Fatalf("--force should replace the file, got %q", b)
	}
	// a file that appears after the check is still not overwritten, and the failure names the way back
	e2 := newEnv(t)
	e2.env["DOCLI_REGION"] = "nyc3"
	racy := pwPath(t)
	e2.fake.Fails = map[string][]fakeFail{}
	args := dbArgs("--password-file", racy, "--yes")
	e2.app.WriteFile = func(path string, data []byte) error {
		os.WriteFile(racy, []byte("appeared"), 0o600) // the manifest write happens before the password write
		return atomicWriteFile(path, data)
	}
	e2.expect(exitError, args...)
	contain(t, e2.errb.String(), "The cluster exists")
	contain(t, e2.errb.String(), "docli db connection app-db --password-file")
	notContain(t, e2.errb.String(), fakeDBPassword)
	if b, _ := os.ReadFile(racy); string(b) != "appeared" {
		t.Fatal("a file that appeared late was overwritten")
	}
}

func TestDBCreateWaitTimeoutKeepsTheCluster(t *testing.T) {
	e := newEnv(t)
	e.fake.StuckNew = true
	e.expect(exitError, dbArgs("--wait", "--wait-timeout", "45s", "--yes")...)
	contain(t, e.errb.String(), `still "creating"`)
	contain(t, e.errb.String(), "still exists and is not deleted")
	if len(e.sleeps) != 3 || len(e.fake.Requests("DELETE")) != 0 || len(e.fake.Requests("POST /v2/databases")) != 1 {
		t.Fatalf("sleeps %v log %v", e.sleeps, e.fake.Log)
	}
	contain(t, e.readManifest(), "name: app-db") // it exists, so it is recorded
}

func TestDBWaitCommand(t *testing.T) {
	e := newEnv(t)
	e.mustRun(dbArgs("--yes")...)
	e.mustRun("db", "wait", "app-db")
	contain(t, e.out.String(), "is online")
	e.mustRun("db", "wait", "app-db", "--json")
	var d dbJSON
	jsonOut(t, e, &d)
	if d.Status != "online" {
		t.Fatalf("%+v", d)
	}
	e.expect(exitError, "db", "wait", "ghost")
	e.expect(exitUsage, "db", "wait")
}

// ---------- read ----------

func TestDBListGetBackups(t *testing.T) {
	e := newEnv(t)
	e.mustRun("db", "list")
	contain(t, e.out.String(), "legacy-pg")
	contain(t, e.out.String(), "pg 15")
	e.mustRun("db", "list", "--region", "sfo3", "--json")
	if e.out.String() != "[]\n" {
		t.Fatalf("%q", e.out.String())
	}
	e.mustRun("db", "get", "legacy-pg")
	contain(t, e.out.String(), "Managed:   no")
	contain(t, e.out.String(), "Trusted source: address 198.51.100.7")
	contain(t, e.out.String(), "Password:  not shown")
	e.mustRun("db", "get", "db-legacy", "--json")
	var d dbDetailJSON
	jsonOut(t, e, &d)
	if d.Name != "legacy-pg" || d.Managed || len(d.TrustedSources) != 1 || d.TrustedSources[0].Value != "198.51.100.7" {
		t.Fatalf("%+v", d)
	}
	e.expect(exitError, "db", "get", "ghost")
	e.mustRun("db", "backups", "list", "legacy-pg", "--json")
	var bs []backupJSON
	jsonOut(t, e, &bs)
	if len(bs) != 1 || bs[0].CreatedAt != "2026-11-11T03:00:00Z" || bs[0].SizeGigabytes != 0.5 {
		t.Fatalf("%+v", bs)
	}
	e.mustRun("db", "backups", "list", "legacy-pg")
	contain(t, e.out.String(), "0.5 GB")
}

func TestDBConnection(t *testing.T) {
	e := newEnv(t)
	e.mustRun("db", "connection", "legacy-pg")
	out := e.out.String()
	for _, want := range []string{"Host:          legacy-pg-do-user-1-0.db.ondigitalocean.com", "Port:          25060", "Private host:  private-legacy-pg", "Database:      defaultdb", "User:          doadmin", "SSL mode:      require", "not shown"} {
		contain(t, out, want)
	}
	notContain(t, out, fakeDBPassword)
	notContain(t, out, "postgresql://")
	e.mustRun("db", "connection", "legacy-pg", "--json", "--user", "app", "--database", "defaultdb")
	var c connectionJSON
	jsonOut(t, e, &c)
	if c.User != "app" || c.Host == "" || c.PasswordFile != "" || c.SSLMode != "require" {
		t.Fatalf("%+v", c)
	}
	notContain(t, e.out.String(), "AVNS_")
	// to a file
	pw := pwPath(t)
	e.mustRun("db", "connection", "legacy-pg", "--password-file", pw)
	contain(t, e.out.String(), "written to "+pw)
	if b, _ := os.ReadFile(pw); string(b) != fakeDBPassword {
		t.Fatalf("%q", b)
	}
	e.expect(exitUsage, "db", "connection", "legacy-pg", "--password-file", pw) // exists
	pw2 := pwPath(t)
	e.mustRun("db", "connection", "legacy-pg", "--user", "app", "--password-file", pw2, "--json")
	if b, _ := os.ReadFile(pw2); string(b) != fakeUserPassword {
		t.Fatalf("the app user's own password expected, got %q", b)
	}
	e.mustRun("db", "connection", "legacy-pg", "--password-file", pw, "--force")
	e.expect(exitError, "db", "connection", "legacy-pg", "--user", "ghost")
	e.expect(exitError, "db", "connection", "legacy-pg", "--database", "ghost")
	// no password from the API: say so and write nothing
	e.fake.dbByName("legacy-pg").Connection.Password = ""
	pw3 := pwPath(t)
	e.expect(exitError, "db", "connection", "legacy-pg", "--password-file", pw3)
	if _, err := os.Stat(pw3); err == nil {
		t.Fatal("an empty password must not produce a file")
	}
}

// ---------- delete / resize ----------

func TestDBDeleteAndOwnership(t *testing.T) {
	e := newEnv(t)
	e.mustRun(dbArgs("--yes")...)
	// --yes alone is not enough
	e.expect(exitRefused, "db", "delete", "app-db", "--yes")
	contain(t, e.errb.String(), "--yes --confirm-name app-db")
	e.expect(exitRefused, "db", "delete", "app-db", "--yes", "--confirm-name", "app")
	e.tty(true)
	e.stdin("y\n")
	e.expect(exitRefused, "db", "delete", "app-db")
	e.mustRun("db", "delete", "app-db", "--dry-run")
	contain(t, e.out.String(), "with all its databases, users and pools")
	if len(e.fake.Requests("DELETE")) != 0 {
		t.Fatal("nothing should be deleted yet")
	}
	// ownership: legacy-pg has no docli:managed tag
	e.stdin("legacy-pg\n")
	e.expect(exitRefused, "db", "delete", "legacy-pg")
	contain(t, e.errb.String(), "does not have the tag docli:managed")
	contain(t, e.errb.String(), "did not create it")
	e.expect(exitRefused, "db", "delete", "legacy-pg", "--yes", "--confirm-name", "legacy-pg")
	e.tty(false)
	e.expect(exitRefused, "db", "delete", "legacy-pg", "--force-unmanaged", "--yes")
	if len(e.fake.Requests("DELETE")) != 0 || e.fake.dbByName("legacy-pg") == nil {
		t.Fatal("the unmanaged cluster must be untouched")
	}
	e.stdin("app-db\n")
	e.tty(true)
	e.mustRun("db", "delete", "app-db")
	if e.fake.dbByName("app-db") != nil || len(e.fake.Requests("DELETE /v2/databases/")) != 1 {
		t.Fatal(e.fake.Log)
	}
	notContain(t, e.readManifest(), "app-db")
	e.tty(false)
	e.mustRun("db", "delete", "legacy-pg", "--force-unmanaged", "--yes", "--confirm-name", "legacy-pg", "--json")
	var del deletedJSON
	jsonOut(t, e, &del)
	if !del.Deleted || del.Type != "database cluster" || del.Name != "legacy-pg" {
		t.Fatalf("%+v", del)
	}
}

func TestDBResize(t *testing.T) {
	e := newEnv(t)
	e.fake.ExposeDBSizes = true
	e.mustRun(dbArgs("--wait", "--yes")...)
	e.mustRun("db", "resize", "app-db", "--size", "db-s-2vcpu-4gb", "--nodes", "2", "--dry-run")
	contain(t, e.out.String(), "now:   db-s-1vcpu-1gb x 1 node(s)")
	contain(t, e.out.String(), "after: db-s-2vcpu-4gb x 2 node(s)")
	contain(t, e.out.String(), "$15.00 per month now, $120.00 after")
	contain(t, e.out.String(), "Estimated cost: $120.00 per month")
	if len(e.fake.Requests("PUT")) != 0 {
		t.Fatal("dry run resized")
	}
	e.tty(false)
	e.expect(exitRefused, "db", "resize", "app-db", "--nodes", "2")
	e.expect(exitUsage, "db", "resize", "app-db")
	e.expect(exitUsage, "db", "resize", "app-db", "--nodes", "7")
	e.expect(exitError, "db", "resize", "app-db", "--size", "db-s-1vcpu-1gb")
	contain(t, e.errb.String(), "already")
	e.expect(exitError, "db", "resize", "app-db", "--size", "db-s-99")
	e.mustRun("db", "resize", "app-db", "--size", "db-s-2vcpu-4gb", "--nodes", "2", "--yes", "--json")
	var d dbJSON
	jsonOut(t, e, &d)
	if d.Status != "resizing" || d.SizeSlug != "db-s-2vcpu-4gb" || d.NumNodes != 2 {
		t.Fatalf("%+v", d)
	}
	e.expect(exitError, "db", "resize", "app-db", "--nodes", "3", "--yes") // not online yet
	contain(t, e.errb.String(), "not online")
	e.mustRun("db", "wait", "app-db")
	e.mustRun("db", "resize", "app-db", "--nodes", "3", "--wait", "--yes")
	contain(t, e.readManifest(), "num_nodes: 3")
	e.mustRun("db", "resize", "legacy-pg", "--nodes", "2", "--dry-run")
	contain(t, e.out.String(), "not created by docli")
}

// ---------- users, databases, pools ----------

func TestDBUsers(t *testing.T) {
	e := newEnv(t)
	e.mustRun(dbArgs("--yes")...)
	e.mustRun("db", "user", "list", "app-db", "--json")
	var us []dbUserJSON
	jsonOut(t, e, &us)
	if len(us) != 2 || us[0].Name != "doadmin" {
		t.Fatalf("%+v", us)
	}
	e.tty(false)
	e.expect(exitRefused, "db", "user", "create", "app-db", "rails")
	e.expect(exitUsage, "db", "user", "create", "app-db", "bad name", "--yes")
	e.expect(exitError, "db", "user", "create", "app-db", "app", "--yes") // already there
	pw := pwPath(t)
	e.mustRun("db", "user", "create", "app-db", "rails", "--dry-run", "--password-file", pw)
	contain(t, e.out.String(), "written to "+pw)
	if len(e.fake.Requests("POST /v2/databases/db-")) != 0 && len(e.fake.Mutations()) > 1 {
		t.Fatal("dry run created a user")
	}
	e.mustRun("db", "user", "create", "app-db", "rails", "--password-file", pw, "--yes")
	notContain(t, e.out.String(), fakeUserPassword)
	if b, _ := os.ReadFile(pw); string(b) != fakeUserPassword {
		t.Fatalf("%q", b)
	}
	e.mustRun("db", "user", "create", "app-db", "worker", "--yes")
	contain(t, e.out.String(), "Save it with: docli db connection app-db --user worker")
	// delete
	e.expect(exitRefused, "db", "user", "delete", "app-db", "doadmin", "--yes")
	contain(t, e.errb.String(), "administrator")
	e.expect(exitError, "db", "user", "delete", "app-db", "ghost", "--yes")
	e.mustRun("db", "user", "delete", "app-db", "worker", "--yes")
	// users of a cluster docli did not create: refused, then typed
	e.expect(exitRefused, "db", "user", "delete", "legacy-pg", "app", "--yes")
	contain(t, e.errb.String(), "did not create it")
	e.expect(exitRefused, "db", "user", "delete", "legacy-pg", "app", "--force-unmanaged", "--yes")
	e.mustRun("db", "user", "delete", "legacy-pg", "app", "--force-unmanaged", "--yes", "--confirm-name", "app")
}

func TestDBDatabasesAndPools(t *testing.T) {
	e := newEnv(t)
	e.mustRun(dbArgs("--yes")...)
	e.tty(false)
	e.mustRun("db", "database", "create", "app-db", "app_production", "--yes")
	e.expect(exitError, "db", "database", "create", "app-db", "app_production", "--yes")
	e.expect(exitUsage, "db", "database", "create", "app-db", "bad-name", "--yes")
	e.mustRun("db", "database", "list", "app-db", "--json")
	var names []dbNameJSON
	jsonOut(t, e, &names)
	if len(names) != 2 {
		t.Fatalf("%+v", names)
	}
	// pools
	e.mustRun("db", "pool", "create", "app-db", "--name", "app_pool", "--database", "app_production", "--user", "app", "--size", "20", "--dry-run")
	contain(t, e.out.String(), "database app_production, user app, transaction mode, 20 connections")
	for _, bad := range [][]string{
		{"--name", "p", "--database", "app_production", "--mode", "weird"},
		{"--name", "p", "--database", "app_production", "--size", "0"},
		{"--name", "p"},
	} {
		e.expect(exitUsage, append([]string{"db", "pool", "create", "app-db", "--yes"}, bad...)...)
	}
	e.expect(exitError, "db", "pool", "create", "app-db", "--name", "p", "--database", "nodb", "--yes")
	e.expect(exitError, "db", "pool", "create", "app-db", "--name", "p", "--database", "app_production", "--user", "nobody", "--yes")
	e.mustRun("db", "pool", "create", "app-db", "--name", "app_pool", "--database", "app_production", "--user", "app", "--yes")
	e.expect(exitError, "db", "pool", "create", "app-db", "--name", "app_pool", "--database", "app_production", "--yes")
	e.mustRun("db", "pool", "list", "app-db", "--json")
	var ps []poolJSON
	jsonOut(t, e, &ps)
	if len(ps) != 1 || ps[0].Mode != "transaction" || ps[0].Size != 10 || ps[0].Port != 25061 || ps[0].Host == "" {
		t.Fatalf("%+v", ps)
	}
	e.mustRun("db", "pool", "delete", "app-db", "app_pool", "--yes")
	e.expect(exitError, "db", "pool", "delete", "app-db", "app_pool", "--yes")
	// dropping a database needs its name typed; defaultdb is never dropped
	e.expect(exitRefused, "db", "database", "delete", "app-db", "app_production", "--yes")
	contain(t, e.errb.String(), "--yes --confirm-name app_production")
	e.expect(exitRefused, "db", "database", "delete", "app-db", "defaultdb", "--yes", "--confirm-name", "defaultdb")
	e.mustRun("db", "database", "delete", "app-db", "app_production", "--yes", "--confirm-name", "app_production")
	if len(e.fake.dbByName("app-db").DBNames) != 1 {
		t.Fatal("the database was not dropped")
	}
	// an unmanaged cluster's pools and databases are protected too
	e.fake.dbByName("legacy-pg").Pools = []godo.DatabasePool{{Name: "keep", Database: "defaultdb", Mode: "session", Size: 5}}
	e.expect(exitRefused, "db", "pool", "delete", "legacy-pg", "keep", "--yes")
	e.expect(exitRefused, "db", "database", "delete", "legacy-pg", "defaultdb", "--yes")
	if len(e.fake.dbByName("legacy-pg").Pools) != 1 {
		t.Fatal("the pool must be untouched")
	}
}

// ---------- trusted sources ----------

func TestDBFirewall(t *testing.T) {
	e := newEnv(t)
	e.mustRun(dbArgs("--trusted-ip", "198.51.100.7", "--yes")...)
	e.mustRun("db", "firewall", "list", "app-db", "--json")
	var srcs []sourceJSON
	jsonOut(t, e, &srcs)
	if len(srcs) != 2 || srcs[0].Type != "droplet" || srcs[0].Name != "legacy-db" || srcs[1].Value != "198.51.100.7" {
		t.Fatalf("%+v", srcs)
	}
	e.mustRun("db", "firewall", "list", "app-db")
	contain(t, e.out.String(), "legacy-db")
	// set replaces: the plan shows what is added, removed and kept
	e.mustRun("db", "firewall", "set", "app-db", "--droplet", "1001", "--tag", "web", "--dry-run")
	out := e.out.String()
	contain(t, out, "replace the trusted sources")
	contain(t, out, "add:    droplets tagged web")
	contain(t, out, "remove: address 198.51.100.7")
	contain(t, out, "keep:   droplet legacy-db (id 1001)")
	contain(t, out, "cuts off whatever connects")
	if len(e.fake.Requests("PUT")) != 0 {
		t.Fatal("dry run replaced the list")
	}
	e.tty(false)
	e.expect(exitRefused, "db", "firewall", "set", "app-db", "--tag", "web")
	e.mustRun("db", "firewall", "set", "app-db", "--droplet", "1001", "--tag", "web", "--yes", "--json")
	jsonOut(t, e, &srcs)
	if len(srcs) != 2 || srcs[1].Type != "tag" {
		t.Fatalf("%+v", srcs)
	}
	contain(t, e.readManifest(), "- tag:web")
	notContain(t, e.readManifest(), "198.51.100.7")
	// no change: nothing sent
	puts := len(e.fake.Requests("PUT"))
	e.mustRun("db", "firewall", "set", "app-db", "--droplet", "legacy-db", "--tag", "web", "--yes")
	contain(t, e.out.String(), "already has exactly these")
	if len(e.fake.Requests("PUT")) != puts {
		t.Fatal("an unchanged list was sent again")
	}
	e.expect(exitUsage, "db", "firewall", "set", "app-db", "--ip", "banana", "--yes")
	e.expect(exitUsage, "db", "firewall", "set")
	e.expect(exitUsage, "db", "firewall")
	e.expect(exitError, "db", "firewall", "set", "app-db", "--droplet", "ghost", "--yes")
}

func TestDBFirewallRefusesOpenLists(t *testing.T) {
	e := newEnv(t)
	e.mustRun(dbArgs("--yes")...)
	e.tty(false)
	for _, args := range [][]string{
		{"db", "firewall", "set", "app-db", "--yes"},                      // an empty list
		{"db", "firewall", "set", "app-db", "--ip", "0.0.0.0/0", "--yes"}, // everyone
		{"db", "firewall", "set", "app-db", "--ip", "::/0", "--yes"},
		{"db", "firewall", "set", "app-db", "--ip", "0.0.0.0/0", "--allow-open", "--yes"}, // typed name missing
		{"db", "firewall", "set", "app-db", "--allow-open", "--yes", "--confirm-name", "wrong"},
	} {
		e.expect(exitRefused, args...)
	}
	if len(e.fake.Requests("PUT")) != 0 || len(e.fake.dbByName("app-db").Rules) != 1 {
		t.Fatalf("the list must be unchanged: %v", e.fake.dbByName("app-db").Rules)
	}
	e.mustRun("db", "firewall", "set", "app-db", "--allow-open", "--dry-run")
	contain(t, e.out.String(), "OPEN: no trusted sources")
	e.mustRun("db", "firewall", "set", "app-db", "--allow-open", "--yes", "--confirm-name", "app-db")
	if len(e.fake.dbByName("app-db").Rules) != 0 {
		t.Fatal("the typed-name path should empty the list")
	}
	notContain(t, e.readManifest(), "droplet:1001")
}

func TestDBNeverRetriesAWrite(t *testing.T) {
	e := newEnv(t)
	e.fake.Fails["POST /v2/databases"] = []fakeFail{{Status: 503}}
	e.expect(exitError, dbArgs("--yes")...)
	if n := len(e.fake.Requests("POST /v2/databases")); n != 1 {
		t.Fatalf("a create was sent %d times", n)
	}
	if e.fake.dbByName("app-db") != nil {
		t.Fatal("test setup")
	}
	if _, err := os.Stat(e.manifest()); err == nil {
		t.Fatal("a failed create wrote the manifest")
	}
	e.fake.Fails["GET /v2/databases"] = []fakeFail{{Status: 502}, {Status: 429, RetryAfter: "1"}}
	before := len(e.fake.Requests("GET /v2/databases?"))
	e.mustRun("db", "list")
	if n := len(e.fake.Requests("GET /v2/databases?")) - before; n != 3 {
		t.Fatalf("reads should be retried: %d requests", n)
	}
	contain(t, e.out.String(), "legacy-pg")
}

// godo cannot page any database list, so a full page might be only the first one. docli refuses a list that
// might be partial rather than act on part of the account.
func TestDBListThatMayBeTruncatedIsRefused(t *testing.T) {
	e := newEnv(t)
	for i := 0; i < noLinksPage; i++ {
		e.fake.DBs = append(e.fake.DBs, newFakeDB("bulk-"+itoa(i), "db-bulk-"+itoa(i), "nyc3", "16", nil))
	}
	e.expect(exitError, "db", "list")
	contain(t, e.errb.String(), "may be incomplete")
	e.expect(exitError, "db", "delete", "bulk-1", "--force-unmanaged", "--yes", "--confirm-name", "bulk-1")
	if len(e.fake.Requests("DELETE")) != 0 {
		t.Fatal("nothing may be deleted on the strength of a partial list")
	}
}
