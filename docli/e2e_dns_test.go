package main

import (
	"os"
	"strings"
	"testing"
)

// ---------- domains ----------

func TestDomainLifecycle(t *testing.T) {
	e := newEnv(t)
	e.tty(false)
	e.mustRun("domain", "create", "--name", "shop.example", "--dry-run")
	contain(t, e.out.String(), `add the domain "shop.example"`)
	contain(t, e.out.String(), "ns1/ns2/ns3.digitalocean.com")
	contain(t, e.out.String(), "Dry run")
	if len(e.fake.Mutations()) != 0 {
		t.Fatal("dry run changed something")
	}
	e.expect(exitRefused, "domain", "create", "--name", "shop.example")
	e.expect(exitUsage, "domain", "create", "--name", "Shop.Example", "--yes")
	e.expect(exitUsage, "domain", "create", "--name", "nodots", "--yes")
	e.expect(exitUsage, "domain", "create", "--yes")
	e.expect(exitError, "domain", "create", "--name", "legacy.example", "--yes")
	contain(t, e.errb.String(), "already in this account")
	if len(e.fake.Mutations()) != 0 {
		t.Fatalf("refusals changed something: %v", e.fake.Mutations())
	}
	e.mustRun("domain", "create", "--name", "shop.example", "--yes", "--json")
	var d domainJSON
	jsonOut(t, e, &d)
	if d.Name != "shop.example" || !d.Managed {
		t.Fatalf("%+v", d)
	}
	m := e.readManifest()
	contain(t, m, "domains:")
	contain(t, m, "name: shop.example")
	contain(t, m, "created_by_docli: true")

	e.mustRun("domain", "list", "--json")
	var ds []domainJSON
	jsonOut(t, e, &ds)
	managed := map[string]bool{}
	for _, x := range ds {
		managed[x.Name] = x.Managed
	}
	if len(ds) != 2 || !managed["shop.example"] || managed["legacy.example"] {
		t.Fatalf("%+v", ds)
	}
	e.mustRun("domain", "get", "shop.example", "--json")
	var det domainDetailJSON
	jsonOut(t, e, &det)
	if len(det.Records) != 3 || det.Records[0].Type != "NS" || det.Records[0].Managed {
		t.Fatalf("DigitalOcean's own NS records are not docli's: %+v", det.Records)
	}
	e.mustRun("domain", "get", "legacy.example")
	contain(t, e.out.String(), "Managed:  no")
	e.expect(exitError, "domain", "get", "ghost.example")
	e.expect(exitError, "domain", "delete", "ghost.example", "--yes")

	// deleting needs the name typed even for docli's own domain: --yes is not enough
	e.expect(exitRefused, "domain", "delete", "shop.example", "--yes")
	contain(t, e.errb.String(), "--yes --confirm-name shop.example")
	e.expect(exitRefused, "domain", "delete", "shop.example", "--yes", "--confirm-name", "shop.exampl")
	e.mustRun("domain", "delete", "shop.example", "--dry-run")
	contain(t, e.out.String(), "ALL 3 of its DNS records (0 created by docli, 3 not)")
	if len(e.fake.Requests("DELETE")) != 0 {
		t.Fatal("nothing should be deleted yet")
	}
	e.mustRun("domain", "delete", "shop.example", "--yes", "--confirm-name", "shop.example", "--json")
	if len(e.fake.Requests("DELETE /v2/domains/shop.example")) != 1 {
		t.Fatal(e.fake.Log)
	}
	notContain(t, e.readManifest(), "shop.example")
}

func TestDomainOwnershipRefusal(t *testing.T) {
	e := newEnv(t)
	e.tty(false)
	// a domain docli did not create
	e.expect(exitRefused, "domain", "delete", "legacy.example", "--yes", "--confirm-name", "legacy.example")
	contain(t, e.errb.String(), "did not create it")
	contain(t, e.errb.String(), "manifest")
	contain(t, e.errb.String(), "--force-unmanaged")
	e.expect(exitRefused, "domain", "delete", "legacy.example", "--force-unmanaged", "--yes") // name not typed
	e.expect(exitRefused, "domain", "delete", "legacy.example", "--force-unmanaged", "--yes", "--confirm-name", "legacy")
	if len(e.fake.Requests("DELETE")) != 0 {
		t.Fatalf("nothing should have been deleted: %v", e.fake.Log)
	}
	e.mustRun("domain", "delete", "legacy.example", "--force-unmanaged", "--yes", "--confirm-name", "legacy.example")
	if len(e.fake.Requests("DELETE /v2/domains/legacy.example")) != 1 {
		t.Fatal(e.fake.Log)
	}
}

// With no manifest (another machine, a fresh checkout, --no-manifest) docli cannot show it created anything, and
// it fails in the safe direction: it refuses.
func TestDomainWithoutManifestIsNotMine(t *testing.T) {
	e := newEnv(t)
	e.mustRun("domain", "create", "--name", "shop.example", "--yes", "--no-manifest")
	if _, err := os.Stat(e.manifest()); err == nil {
		t.Fatal("--no-manifest wrote a manifest")
	}
	e.expect(exitRefused, "domain", "delete", "shop.example", "--yes", "--confirm-name", "shop.example")
	contain(t, e.errb.String(), "did not create it")
	e.mustRun("domain", "create", "--name", "other.example", "--dry-run", "--no-manifest")
	contain(t, e.out.String(), "--no-manifest: docli will not remember")
	// a manifest that is not valid is an error, not "nothing is owned"
	os.MkdirAll(strings.TrimSuffix(e.manifest(), "manifest.yml"), 0o755)
	os.WriteFile(e.manifest(), []byte("version: 1\nresources: [oops\n"), 0o644)
	e.expect(exitError, "domain", "delete", "shop.example", "--yes", "--confirm-name", "shop.example")
	contain(t, e.errb.String(), "Nothing was changed")
	e.expect(exitError, "domain", "list")
	if len(e.fake.Requests("DELETE")) != 0 {
		t.Fatal(e.fake.Log)
	}
}

// ---------- records ----------

func dnsMust(e *testEnv, args ...string) {
	e.t.Helper()
	e.mustRun(append([]string{"dns"}, args...)...)
}

func TestDNSRecordLifecycle(t *testing.T) {
	e := newEnv(t)
	e.mustRun("domain", "create", "--name", "shop.example", "--yes")
	dnsMust(e, "create", "shop.example", "--type", "A", "--name", "www", "--data", "203.0.113.5", "--dry-run")
	contain(t, e.out.String(), "add a A record www.shop.example in shop.example")
	contain(t, e.out.String(), "A www -> 203.0.113.5, TTL 1800s")
	if len(e.fake.Requests("POST /v2/domains/shop.example/records")) != 0 {
		t.Fatal("dry run created a record")
	}
	e.tty(false)
	e.expect(exitRefused, "dns", "create", "shop.example", "--type", "A", "--name", "www", "--data", "203.0.113.5")
	e.mustRun("dns", "create", "shop.example", "--type", "a", "--name", "WWW", "--data", "203.0.113.5", "--ttl", "300", "--yes", "--json")
	var rec recordJSON
	jsonOut(t, e, &rec)
	if rec.Type != "A" || rec.Name != "www" || rec.FQDN != "www.shop.example" || rec.TTL != 300 || !rec.Managed || rec.ID == 0 {
		t.Fatalf("%+v", rec)
	}
	wwwID := rec.ID
	dnsMust(e, "create", "shop.example", "--type", "MX", "--name", "@", "--data", "mail.example.net.", "--priority", "10", "--yes")
	dnsMust(e, "create", "shop.example", "--type", "TXT", "--name", "@", "--data", "v=spf1 -all", "--yes")
	dnsMust(e, "create", "shop.example", "--type", "CAA", "--name", "@", "--data", "letsencrypt.org", "--tag", "issue", "--yes")
	dnsMust(e, "create", "shop.example", "--type", "CNAME", "--name", "blog", "--data", "host.example.net.", "--yes")
	dnsMust(e, "create", "shop.example", "--type", "AAAA", "--name", "www", "--data", "2001:db8::1", "--yes")
	e.expect(exitError, "dns", "create", "shop.example", "--type", "TXT", "--name", "blog", "--data", "x", "--yes")
	contain(t, e.errb.String(), "already a CNAME")
	e.expect(exitError, "dns", "create", "shop.example", "--type", "CNAME", "--name", "www", "--data", "x.example.net", "--yes")
	contain(t, e.errb.String(), "cannot share its name")
	body := e.fake.Bodies["POST /v2/domains/shop.example/records"]
	contain(t, strings.Join(body, "\n"), `"priority":10`)
	contain(t, strings.Join(body, "\n"), `"tag":"issue"`)
	contain(t, body[0], `"ttl":300`)
	contain(t, body[1], `"ttl":1800`) // the default

	m := e.readManifest()
	for _, want := range []string{"created_by_docli: true", "type: MX", "type: CAA", "data: 203.0.113.5", "priority: 10", "tag: issue"} {
		contain(t, m, want)
	}
	notContain(t, m, "digitalocean.com") // DigitalOcean's own records are not listed

	dnsMust(e, "list", "shop.example", "--json")
	var recs []recordJSON
	jsonOut(t, e, &recs)
	mine := 0
	for _, r := range recs {
		if r.Managed {
			mine++
		}
	}
	if len(recs) != 9 || mine != 6 {
		t.Fatalf("%d records, %d managed", len(recs), mine)
	}
	dnsMust(e, "list", "shop.example", "--type", "A", "--name", "www", "--json")
	jsonOut(t, e, &recs)
	if len(recs) != 1 || recs[0].ID != wwwID {
		t.Fatalf("%+v", recs)
	}
	dnsMust(e, "list", "shop.example")
	contain(t, e.out.String(), "priority 10")

	// update a record docli created
	dnsMust(e, "update", "shop.example", itoa(wwwID), "--data", "203.0.113.9", "--dry-run")
	contain(t, e.out.String(), "now:  A www -> 203.0.113.5")
	contain(t, e.out.String(), "after: A www -> 203.0.113.9")
	dnsMust(e, "update", "shop.example", itoa(wwwID), "--data", "203.0.113.9", "--ttl", "600", "--yes", "--json")
	jsonOut(t, e, &rec)
	if rec.Data != "203.0.113.9" || rec.TTL != 600 || rec.Type != "A" {
		t.Fatalf("%+v", rec)
	}
	contain(t, e.readManifest(), "data: 203.0.113.9")
	notContain(t, e.readManifest(), "data: 203.0.113.5")
	e.expect(exitUsage, "dns", "update", "shop.example", itoa(wwwID), "--yes")                      // nothing to change
	e.expect(exitUsage, "dns", "update", "shop.example", itoa(wwwID), "--type", "TXT", "--yes")     // type is fixed
	e.expect(exitUsage, "dns", "update", "shop.example", itoa(wwwID), "--data", "nope", "--yes")    // validated like create
	e.expect(exitError, "dns", "update", "shop.example", "99999", "--data", "203.0.113.1", "--yes") // unknown id
	e.expect(exitUsage, "dns", "update", "shop.example", "www", "--data", "203.0.113.1", "--yes")   // ids are numbers
	e.expect(exitError, "dns", "update", "shop.example", itoa(wwwID), "--name", "blog", "--yes")    // still conflicts: blog is a CNAME
	contain(t, e.errb.String(), "already a CNAME")

	// delete
	e.expect(exitRefused, "dns", "delete", "shop.example", itoa(wwwID))
	dnsMust(e, "delete", "shop.example", itoa(wwwID), "--dry-run")
	contain(t, e.out.String(), "remove A www -> 203.0.113.9")
	dnsMust(e, "delete", "shop.example", itoa(wwwID), "--yes", "--json")
	var del deletedJSON
	jsonOut(t, e, &del)
	if !del.Deleted || del.ID != itoa(wwwID) || del.Name != "www.shop.example" {
		t.Fatalf("%+v", del)
	}
	notContain(t, e.readManifest(), "data: 203.0.113.9")
	e.expect(exitError, "dns", "delete", "shop.example", itoa(wwwID), "--yes")
}

func TestDNSValidationBeforeAnyRequest(t *testing.T) {
	e := newEnv(t)
	bad := [][]string{
		{"create", "legacy.example", "--type", "A", "--name", "www.legacy.example", "--data", "203.0.113.5", "--yes"},
		{"create", "legacy.example", "--type", "A", "--name", "www", "--data", "not-an-ip", "--yes"},
		{"create", "legacy.example", "--type", "CNAME", "--name", "@", "--data", "x.example.net", "--yes"},
		{"create", "legacy.example", "--type", "MX", "--name", "@", "--data", "mail.example.net", "--yes"},
		{"create", "legacy.example", "--type", "SRV", "--name", "@", "--data", "x", "--yes"},
		{"create", "legacy.example", "--type", "A", "--name", "www", "--data", "203.0.113.5", "--ttl", "1", "--yes"},
		{"create", "legacy.example", "--type", "A", "--data", "203.0.113.5", "--yes"},
		{"create", "legacy.example", "--name", "www", "--yes"},
		{"create", "--type", "A", "--name", "www", "--data", "203.0.113.5", "--yes"},
	}
	for _, a := range bad {
		if code := e.run(append([]string{"dns"}, a...)...); code != exitUsage {
			t.Errorf("dns %v: exit %d, want %d (%s)", a, code, exitUsage, e.errb.String())
		}
	}
	if len(e.fake.Log) != 0 {
		t.Fatalf("validation errors must not call the API: %v", e.fake.Log)
	}
	// problems that need the zone
	e.expect(exitError, "dns", "create", "legacy.example", "--type", "A", "--name", "@", "--data", "203.0.113.10", "--yes")
	contain(t, e.errb.String(), "already has that record")
	e.expect(exitError, "dns", "create", "ghost.example", "--type", "A", "--name", "www", "--data", "203.0.113.5", "--yes")
	contain(t, e.errb.String(), "no domain named")
	if len(e.fake.Mutations()) != 0 {
		t.Fatalf("%v", e.fake.Mutations())
	}
	e.mustRun("dns", "create", "legacy.example", "--type", "NS", "--name", "@", "--data", "ns.example.net", "--dry-run")
	contain(t, e.out.String(), "changes who answers for the whole domain")
}

func TestDNSOwnershipRefusal(t *testing.T) {
	e := newEnv(t)
	e.tty(false)
	// 901 is an A record that was in the domain already
	e.expect(exitRefused, "dns", "delete", "legacy.example", "901", "--yes")
	contain(t, e.errb.String(), "did not create it")
	contain(t, e.errb.String(), "manifest")
	e.expect(exitRefused, "dns", "update", "legacy.example", "901", "--data", "203.0.113.99", "--yes")
	contain(t, e.errb.String(), "refusing to update")
	e.expect(exitRefused, "dns", "delete", "legacy.example", "901", "--force-unmanaged", "--yes")
	e.expect(exitRefused, "dns", "delete", "legacy.example", "901", "--force-unmanaged", "--yes", "--confirm-name", "legacy")
	if len(e.fake.Mutations()) != 0 {
		t.Fatalf("nothing should change: %v", e.fake.Mutations())
	}
	e.mustRun("dns", "update", "legacy.example", "901", "--data", "203.0.113.99", "--force-unmanaged", "--yes", "--confirm-name", "legacy.example")
	if got := e.fake.Records["legacy.example"][0].Data; got != "203.0.113.99" {
		t.Fatalf("forced update with the typed name should work, data is %s", got)
	}
	e.mustRun("dns", "delete", "legacy.example", "902", "--force-unmanaged", "--yes", "--confirm-name", "legacy.example")
	if len(e.fake.Records["legacy.example"]) != 1 {
		t.Fatal("forced delete with the typed name should work")
	}
	// a force does not make docli claim the record
	if _, err := os.Stat(e.manifest()); err == nil {
		notContain(t, e.readManifest(), "legacy.example")
	}
}

// A record docli adds to a domain it did not create is tracked, but does not make the domain docli's.
func TestRecordInForeignDomain(t *testing.T) {
	e := newEnv(t)
	e.mustRun("dns", "create", "legacy.example", "--type", "TXT", "--name", "_verify", "--data", "token-123", "--yes", "--json")
	var rec recordJSON
	jsonOut(t, e, &rec)
	m := e.readManifest()
	contain(t, m, "name: legacy.example")
	contain(t, m, "created_by_docli: false")
	contain(t, m, "token-123")
	e.tty(false)
	e.expect(exitRefused, "domain", "delete", "legacy.example", "--yes", "--confirm-name", "legacy.example")
	e.mustRun("domain", "list", "--json")
	var ds []domainJSON
	jsonOut(t, e, &ds)
	for _, d := range ds {
		if d.Managed {
			t.Fatalf("%+v: the domain is not docli's", d)
		}
	}
	e.mustRun("dns", "delete", "legacy.example", itoa(rec.ID), "--yes")
	if _, err := os.Stat(e.manifest()); err == nil {
		notContain(t, e.readManifest(), "legacy.example") // nothing left that docli owns there
	}
	// the pre-existing records were never touched
	if len(e.fake.Records["legacy.example"]) != 2 {
		t.Fatalf("%+v", e.fake.Records["legacy.example"])
	}
}

// DOCUMENTED LIMIT: the manifest is the authority for DNS, so anyone who can edit it can claim a record.
// Deleting still needs a confirmation, and the account's own tags are not involved (domains have none).
func TestEditedManifestCanClaimARecord(t *testing.T) {
	e := newEnv(t)
	e.mustRun("dns", "create", "legacy.example", "--type", "TXT", "--name", "_x", "--data", "v", "--yes")
	m := e.readManifest()
	m = strings.Replace(m, "records:\n", "records:\n        - id: 901\n          type: A\n          name: \"@\"\n          data: 203.0.113.10\n          ttl: 300\n          priority: 0\n          flags: 0\n          tag: \"\"\n", 1)
	if !strings.Contains(m, "id: 901") {
		t.Fatalf("test setup: could not edit the manifest:\n%s", m)
	}
	os.WriteFile(e.manifest(), []byte(m), 0o644)
	e.tty(false)
	e.mustRun("dns", "delete", "legacy.example", "901", "--yes")
	if len(e.fake.Requests("DELETE /v2/domains/legacy.example/records/901")) != 1 {
		t.Fatal(e.fake.Log)
	}
}

func TestDNSNeverRetriesAWrite(t *testing.T) {
	e := newEnv(t)
	e.fake.Fails["POST /v2/domains/legacy.example/records"] = []fakeFail{{Status: 503}}
	e.expect(exitError, "dns", "create", "legacy.example", "--type", "TXT", "--name", "t", "--data", "v", "--yes")
	if n := len(e.fake.Requests("POST /v2/domains/legacy.example/records")); n != 1 {
		t.Fatalf("a create was sent %d times", n)
	}
	// a failed change leaves the manifest alone
	if _, err := os.Stat(e.manifest()); err == nil {
		t.Fatal("a failed create wrote the manifest")
	}
	// reads are retried
	e.fake.Fails["GET /v2/domains"] = []fakeFail{{Status: 503}, {Status: 429, RetryAfter: "1"}}
	before := len(e.fake.Requests("GET /v2/domains?"))
	e.mustRun("domain", "list")
	contain(t, e.out.String(), "legacy.example")
	if n := len(e.fake.Requests("GET /v2/domains?")) - before; n != 3 {
		t.Fatalf("expected 3 GETs (two failures, then success), got %d", n)
	}
	e.fake.Fails["POST /v2/domains"] = []fakeFail{{Status: 500}}
	posts := len(e.fake.Requests("POST /v2/domains"))
	e.expect(exitError, "domain", "create", "--name", "shop.example", "--yes")
	if n := len(e.fake.Requests("POST /v2/domains")) - posts; n != 1 {
		t.Fatalf("a create was sent %d times", n)
	}
}
