package main

import (
	"os"
	"strings"
	"testing"

	"github.com/digitalocean/godo"
)

// manifestBody is the file without its comment header, which itself mentions "domains:" and "passwords".
func (e *testEnv) manifestBody() string {
	return "version:" + strings.SplitN(e.readManifest(), "\nversion:", 2)[1]
}

func buildScenarioM2(t *testing.T, e *testEnv) {
	t.Helper()
	e.env["DOCLI_REGION"] = "nyc3"
	e.mustRun("vpc", "create", "--name", "net", "--ip-range", "10.50.0.0/24", "--yes")
	e.mustRun("domain", "create", "--name", "shop.example", "--yes")
	e.mustRun("dns", "create", "shop.example", "--type", "A", "--name", "www", "--data", "203.0.113.5", "--ttl", "300", "--yes")
	e.mustRun("dns", "create", "shop.example", "--type", "MX", "--name", "@", "--data", "mail.example.net.", "--priority", "10", "--yes")
	e.mustRun("db", "create", "--name", "app-db", "--version", "16", "--size", "db-s-1vcpu-1gb", "--nodes", "1", "--vpc", "net", "--stack", "shop",
		"--trusted-tag", "app", "--trusted-ip", "198.51.100.7", "--wait", "--yes")
}

func TestManifestM2Golden(t *testing.T) {
	e := newEnv(t)
	buildScenarioM2(t, e)
	golden(t, "manifest_m2.yml", e.readManifest())
}

func TestManifestM2HoldsNoSecrets(t *testing.T) {
	e := newEnv(t)
	buildScenarioM2(t, e)
	pw := pwPath(t)
	e.mustRun("db", "connection", "app-db", "--password-file", pw)
	e.mustRun("db", "user", "create", "app-db", "rails", "--yes")
	e.mustRun("db", "firewall", "set", "app-db", "--tag", "web", "--yes")
	e.mustRun("manifest", "refresh", "--yes")
	m := e.manifestBody()
	for _, secret := range []string{fakeDBPassword, fakeUserPassword, "AVNS_", "postgresql://", "sslmode", "password", "uri:", fakeToken, "FAKETOKEN"} {
		notContain(t, m, secret)
	}
	contain(t, m, "host: app-db-do-user-1-0.db.ondigitalocean.com")
	contain(t, m, "port: 25060")
}

func TestManifestM2UpdatesInPlace(t *testing.T) {
	e := newEnv(t)
	buildScenarioM2(t, e)
	e.mustRun("droplet", "create", "--name", "web-1", "--size", "s-1vcpu-1gb", "--image", "ubuntu-24-04-x64", "--wait", "--yes")
	before := e.readManifest()
	e.mustRun("dns", "create", "shop.example", "--type", "TXT", "--name", "@", "--data", "v=spf1 -all", "--yes")
	after := e.readManifest()
	var changed []string
	for _, l := range lineDiff(before, after) {
		if !strings.Contains(l, "updated_at:") {
			changed = append(changed, l)
		}
	}
	joined := strings.Join(changed, "\n")
	if !strings.Contains(joined, "v=spf1 -all") || strings.Contains(joined, "web-1") || strings.Contains(joined, "app-db") || strings.Contains(joined, "docli-net") {
		t.Fatalf("only the domain entry should change:\n%s", joined)
	}
	for _, l := range changed {
		if !strings.HasPrefix(l, "+ ") {
			t.Fatalf("adding a record must only add lines, got %q", l)
		}
	}
	// a database change touches the database entry only
	before = after
	e.mustRun("db", "firewall", "set", "app-db", "--tag", "app", "--yes")
	changed = nil
	for _, l := range lineDiff(before, e.readManifest()) {
		if !strings.Contains(l, "updated_at:") {
			changed = append(changed, l)
		}
	}
	joined = strings.Join(changed, "\n")
	if !strings.Contains(joined, "198.51.100.7") || strings.Contains(joined, "shop.example") || strings.Contains(joined, "web-1") {
		t.Fatalf("only the database entry should change:\n%s", joined)
	}
	// deleting a record and the cluster removes just those lines
	e.mustRun("dns", "delete", "shop.example", itoa(e.fake.Records["shop.example"][3].ID), "--yes")
	e.mustRun("db", "delete", "app-db", "--yes", "--confirm-name", "app-db")
	m := e.manifestBody()
	notContain(t, m, "www")
	notContain(t, m, "app-db")
	notContain(t, m, "databases:")
	contain(t, m, "name: web-1")
	contain(t, m, "name: shop.example")
}

func TestManifestM2Verify(t *testing.T) {
	e := newEnv(t)
	buildScenarioM2(t, e)
	e.mustRun("manifest", "verify")
	contain(t, e.out.String(), "matches the account")

	diffs := func() []diffItem {
		t.Helper()
		e.expect(exitError, "manifest", "verify", "--json")
		var v verifyJSON
		jsonOut(t, e, &v)
		if v.InSync {
			t.Fatal("expected differences")
		}
		return v.Differences
	}
	has := func(ds []diffItem, kind, section, substr string) {
		t.Helper()
		for _, d := range ds {
			if d.Kind == kind && d.Section == section && strings.Contains(d.Detail, substr) {
				return
			}
		}
		t.Fatalf("no %s/%s difference containing %q in %+v", kind, section, substr, ds)
	}

	// a record changed outside docli
	www := &e.fake.Records["shop.example"][3]
	if www.Name != "www" {
		t.Fatalf("test setup: %+v", e.fake.Records["shop.example"])
	}
	www.Data = "203.0.113.99"
	has(diffs(), "drift", "domains", "record "+itoa(www.ID)+" (A www) data: file has \"203.0.113.5\", account has \"203.0.113.99\"")
	www.Data = "203.0.113.5"
	e.mustRun("manifest", "verify")

	// a record deleted outside docli
	gone := e.fake.Records["shop.example"][4]
	e.fake.Records["shop.example"] = e.fake.Records["shop.example"][:4]
	has(diffs(), "drift", "domains", "record "+itoa(gone.ID)+" (MX @) is listed in the file but no longer exists")
	e.fake.Records["shop.example"] = append(e.fake.Records["shop.example"], gone)

	// a database resized outside docli, and its trusted sources changed
	db := e.fake.dbByName("app-db")
	db.SizeSlug, db.NumNodes = "db-s-2vcpu-4gb", 2
	db.Rules = db.Rules[:1]
	ds := diffs()
	has(ds, "drift", "databases", "size_slug")
	has(ds, "drift", "databases", "num_nodes")
	has(ds, "drift", "databases", "trusted_sources")
	e.mustRun("manifest", "refresh", "--yes")
	e.mustRun("manifest", "verify")

	// a cluster that lost its docli tag is no longer docli's
	db.Tags = []string{"app"}
	has(diffs(), "missing_in_account", "databases", "no longer marked docli-managed")
	db.Tags = []string{managedTag, "docli:stack:shop"}
	e.mustRun("manifest", "verify")

	// a cluster and a domain deleted outside docli
	e.fake.DBs = e.fake.DBs[:1]
	e.fake.Domains = e.fake.Domains[:1]
	ds = diffs()
	has(ds, "missing_in_account", "databases", "no longer exists")
	has(ds, "missing_in_account", "domains", "no longer exists")
}

func TestManifestM2MissingFromFile(t *testing.T) {
	e := newEnv(t)
	buildScenarioM2(t, e)
	// a docli cluster created on another machine: tagged in the account, absent from this file
	other := newFakeDB("other-db", "db-other", "nyc3", "16", []string{managedTag})
	e.fake.DBs = append(e.fake.DBs, other)
	e.expect(exitError, "manifest", "verify", "--json")
	var v verifyJSON
	jsonOut(t, e, &v)
	if len(v.Differences) != 1 || v.Differences[0].Kind != "missing_in_file" || v.Differences[0].Name != "other-db" {
		t.Fatalf("%+v", v.Differences)
	}
	e.mustRun("manifest", "refresh", "--yes")
	contain(t, e.readManifest(), "name: other-db")
}

// Refresh can add clusters (tags say whose they are) but never domains: the account cannot say which are docli's.
func TestManifestM2RefreshAndDomains(t *testing.T) {
	e := newEnv(t)
	buildScenarioM2(t, e)
	// a record is gone and a domain exists that docli never touched
	e.fake.Records["shop.example"] = e.fake.Records["shop.example"][:4]
	e.fake.Domains = append(e.fake.Domains, godo.Domain{Name: "stranger.example", TTL: 1800})
	e.mustRun("manifest", "refresh", "--yes")
	m := e.readManifest()
	notContain(t, m, "type: MX")
	notContain(t, m, "stranger.example")
	notContain(t, m, "legacy.example")
	contain(t, m, "created_by_docli: true")
	contain(t, m, "name: www")
	// a domain deleted outside docli disappears from the file
	e.fake.Domains = []godo.Domain{{Name: "legacy.example", TTL: 1800}}
	e.mustRun("manifest", "refresh", "--yes")
	notContain(t, e.readManifest(), "shop.example")
	// refresh on a fresh checkout has no memory, so it records no domains
	os.Remove(e.manifest())
	e.mustRun("manifest", "refresh", "--yes")
	notContain(t, e.manifestBody(), "domains:")
	contain(t, e.manifestBody(), "databases:")
}

// A token that may not read domains or databases still works for everything else.
func TestManifestM2ToleratesDeniedSections(t *testing.T) {
	e := newEnv(t)
	e.fake.Denied["/v2/domains"] = true
	e.fake.Denied["/v2/databases"] = true
	e.mustRun(withArgs("--wait", "--yes")...)
	contain(t, e.errb.String(), "updated config/deploys/manifest.yml")
	e.mustRun("manifest", "verify")
	e.mustRun("manifest", "refresh", "--yes")
	contain(t, e.readManifest(), "name: web-1")
	// entries it cannot check are reported as such, not as gone
	m := e.readManifest()
	m += "  domains:\n    - id: shop.example\n      name: shop.example\n      created_by_docli: true\n      ttl: 1800\n      records: []\n"
	os.WriteFile(e.manifest(), []byte(m), 0o644)
	e.expect(exitError, "manifest", "verify", "--json")
	var v verifyJSON
	jsonOut(t, e, &v)
	if len(v.Differences) != 1 || v.Differences[0].Kind != "unverifiable" {
		t.Fatalf("%+v", v.Differences)
	}
	e.mustRun("manifest", "refresh", "--yes")
	contain(t, e.readManifest(), "id: shop.example") // refresh does not drop what it cannot see
}
