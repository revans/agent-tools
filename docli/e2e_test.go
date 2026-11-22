package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/digitalocean/godo"
)

func jsonOut(t *testing.T, e *testEnv, v any) {
	t.Helper()
	if err := json.Unmarshal(e.out.Bytes(), v); err != nil {
		t.Fatalf("stdout is not the expected JSON: %v\n%s", err, e.out.String())
	}
}

// ---------- foundation ----------

func TestVersionHelpAndExitCodes(t *testing.T) {
	e := newEnv(t)
	e.mustRun("version")
	if strings.TrimSpace(e.out.String()) != "docli dev" {
		t.Fatal(e.out.String())
	}
	e.mustRun("version", "--json")
	var v map[string]string
	jsonOut(t, e, &v)
	if v["version"] != "dev" || v["os"] == "" {
		t.Fatalf("%v", v)
	}
	e.mustRun("help")
	contain(t, e.out.String(), "Exit codes: 0 ok, 1 error, 2 usage, 3 refused")
	e.mustRun("help", "firewall")
	contain(t, e.out.String(), "DENY BY DEFAULT")
	contain(t, e.out.String(), "in:tcp:22:from=10.116.0.0/20")
	e.mustRun("droplet", "create", "-h")
	contain(t, e.out.String(), "docli droplet create")
	e.expect(exitUsage)
	e.expect(exitUsage, "frobnicate")
	e.expect(exitUsage, "droplet")
	e.expect(exitUsage, "droplet", "explode")
	e.expect(exitUsage, "droplet", "list", "--nope")
	e.expect(exitUsage, "droplet", "get")
	e.expect(exitUsage, "help", "nothing-here")
	if len(e.fake.Log) != 0 {
		t.Fatal("usage errors must not call the API")
	}
}

func TestGlobalFlagsAnywhere(t *testing.T) {
	e := newEnv(t)
	for _, args := range [][]string{{"--json", "regions"}, {"regions", "--json"}, {"--json", "regions", "--all"}} {
		e.mustRun(args...)
		var out []regionJSON
		jsonOut(t, e, &out)
	}
	e.mustRun("droplet", "get", "legacy-db", "--json") // flag after a positional
	var d dropletJSON
	jsonOut(t, e, &d)
	if d.Name != "legacy-db" {
		t.Fatal(d.Name)
	}
}

func TestErrorsAsJSON(t *testing.T) {
	e := newEnv(t)
	e.expect(exitUsage, "--json", "frobnicate")
	var j struct {
		Error    string `json:"error"`
		ExitCode int    `json:"exit_code"`
	}
	if err := json.Unmarshal(e.errb.Bytes(), &j); err != nil || j.ExitCode != 2 || j.Error == "" {
		t.Fatalf("%v %s", err, e.errb.String())
	}
	if e.out.Len() != 0 {
		t.Fatal("errors go to stderr only")
	}
}

func TestTokenCheck(t *testing.T) {
	e := newEnv(t)
	e.mustRun("token", "check")
	contain(t, e.out.String(), "owner@example.test")
	contain(t, e.out.String(), "droplets")
	contain(t, e.out.String(), "allowed")
	contain(t, e.out.String(), "Write access: unknown")
	e.mustRun("token", "check", "--json")
	var tc tokenCheckJSON
	jsonOut(t, e, &tc)
	if !tc.Valid || tc.Email != "owner@example.test" || len(tc.ReadAccess) != 8 || tc.Source != "environment variable DIGITALOCEAN_TOKEN" {
		t.Fatalf("%+v", tc)
	}
	// a token that may not read firewalls
	e.fake.Denied["/v2/firewalls"] = true
	e.mustRun("token", "check", "--json")
	jsonOut(t, e, &tc)
	for _, p := range tc.ReadAccess {
		if (p.Resource == "firewalls") != (p.Read == "denied") {
			t.Fatalf("probe %+v", p)
		}
	}
	// a token without account scope is still a valid token
	e.fake.Denied = map[string]bool{"/v2/account": true}
	e.mustRun("token", "check", "--json")
	jsonOut(t, e, &tc)
	if !tc.Valid || tc.Email != "" || !strings.Contains(strings.Join(tc.Notes, " "), "cannot read the account") {
		t.Fatalf("%+v", tc)
	}
	// a rejected token
	e.fake.Denied = map[string]bool{}
	e.fake.token = "other"
	e.expect(exitError, "token", "check")
	contain(t, e.errb.String(), "rejected the token")
	e.expect(exitUsage, "token")
}

// ---------- read commands and paging ----------

func TestPaginationFollowsEveryPage(t *testing.T) {
	e := newEnv(t)
	e.mustRun("sizes", "--all", "--json")
	var sizes []sizeJSON
	jsonOut(t, e, &sizes)
	if len(sizes) != 5 {
		t.Fatalf("got %d sizes, want 5 (three pages of two)", len(sizes))
	}
	if n := len(e.fake.Requests("GET /v2/sizes")); n != 3 {
		t.Fatalf("%d size requests, want 3", n)
	}
	e.fake.PageSize = 1000
	e.mustRun("sizes", "--all", "--json")
	if n := len(e.fake.Requests("GET /v2/sizes")); n != 4 {
		t.Fatalf("a single page should take one request, log: %v", e.fake.Log)
	}
}

func TestSizes(t *testing.T) {
	e := newEnv(t)
	e.mustRun("sizes")
	contain(t, e.out.String(), "s-1vcpu-512mb-10gb")
	contain(t, e.out.String(), "$4.00")
	notContain(t, e.out.String(), "gpu-h100x8")
	notContain(t, e.out.String(), "s-4vcpu-8gb")
	contain(t, e.errb.String(), "2 more size(s) hidden")
	if strings.Index(e.out.String(), "$4.00") > strings.Index(e.out.String(), "$18.00") {
		t.Fatal("cheapest first")
	}
	e.mustRun("sizes", "--region", "sfo3", "--json")
	var s []sizeJSON
	jsonOut(t, e, &s)
	if len(s) != 2 {
		t.Fatalf("%v", s)
	}
	e.mustRun("sizes", "--max-price", "5", "--json")
	jsonOut(t, e, &s)
	if len(s) != 1 || s[0].Slug != "s-1vcpu-512mb-10gb" {
		t.Fatalf("%v", s)
	}
}

func TestRegionsAndImages(t *testing.T) {
	e := newEnv(t)
	e.mustRun("regions")
	notContain(t, e.out.String(), "ams3")
	e.mustRun("regions", "--all")
	contain(t, e.out.String(), "ams3")
	e.mustRun("images")
	contain(t, e.out.String(), "ubuntu-24-04-x64")
	notContain(t, e.out.String(), "docker-20-04")
	e.mustRun("images", "--type", "application")
	contain(t, e.out.String(), "docker-20-04")
	notContain(t, e.out.String(), "ubuntu")
	e.mustRun("images", "--type", "custom")
	contain(t, e.out.String(), "my-custom")
	e.mustRun("images", "--slug", "debian-12-x64", "--json")
	var imgs []imageJSON
	jsonOut(t, e, &imgs)
	if len(imgs) != 1 || imgs[0].Slug != "debian-12-x64" {
		t.Fatalf("%v", imgs)
	}
	e.mustRun("images", "--region", "sfo3", "--json")
	jsonOut(t, e, &imgs)
	if len(imgs) != 2 {
		t.Fatalf("sfo3 has ubuntu and fedora, got %v", imgs)
	}
	e.expect(exitError, "images", "--slug", "nope")
	contain(t, e.errb.String(), `no image with the slug "nope"`)
	e.expect(exitUsage, "images", "--type", "weird")
}

// ---------- ssh keys ----------

func TestSSHKeyLifecycle(t *testing.T) {
	e := newEnv(t)
	e.tty(false)
	// dry run: fingerprint shown, nothing sent
	e.mustRun("ssh-key", "create", "--name", "laptop", "--public-key", testPub, "--dry-run")
	contain(t, e.out.String(), `"docli-laptop"`)
	contain(t, e.out.String(), "fingerprint: ")
	contain(t, e.out.String(), "Dry run")
	if len(e.fake.Mutations()) != 0 {
		t.Fatal(e.fake.Mutations())
	}
	// no terminal and no --yes: refused
	e.expect(exitRefused, "ssh-key", "create", "--name", "laptop", "--public-key", testPub)
	if len(e.fake.Mutations()) != 0 {
		t.Fatal("refusal must not change anything")
	}
	// from a file
	pub := filepath.Join(t.TempDir(), "id.pub")
	os.WriteFile(pub, []byte(testPub+"\n"), 0o644)
	e.mustRun("ssh-key", "create", "--name", "laptop", "--public-key-file", pub, "--yes")
	contain(t, e.out.String(), "Added SSH key")
	body := e.fake.Bodies["POST /v2/account/keys"][0]
	contain(t, body, `"name":"docli-laptop"`)
	contain(t, body, "AAAAC3NzaC1lZDI1NTE5AAAAIOMq")
	// duplicate
	e.expect(exitError, "ssh-key", "create", "--name", "laptop", "--public-key", testPub, "--yes")
	contain(t, e.errb.String(), "already exists")
	// get by short name (docli- fallback), list, json
	e.mustRun("ssh-key", "get", "laptop", "--json")
	var k keyJSON
	jsonOut(t, e, &k)
	if k.Name != "docli-laptop" || !k.Managed {
		t.Fatalf("%+v", k)
	}
	e.mustRun("ssh-key", "list")
	contain(t, e.out.String(), "robert-laptop")
	contain(t, e.out.String(), "docli-laptop")
	e.expect(exitError, "ssh-key", "get", "ghost")
	// delete the managed one
	e.mustRun("ssh-key", "delete", "laptop", "--yes")
	if len(e.fake.Requests("DELETE /v2/account/keys/")) != 1 {
		t.Fatal(e.fake.Log)
	}
	e.mustRun("ssh-key", "list", "--json")
	var keys []keyJSON
	jsonOut(t, e, &keys)
	if len(keys) != 1 || keys[0].Name != "robert-laptop" {
		t.Fatalf("%v", keys)
	}
}

func TestSSHKeyPrivateKeyRefused(t *testing.T) {
	e := newEnv(t)
	e.expect(exitError, "ssh-key", "create", "--name", "x", "--public-key", "-----BEGIN OPENSSH PRIVATE KEY-----\nsecretsecretsecret\n-----END OPENSSH PRIVATE KEY-----", "--yes")
	contain(t, e.errb.String(), "PRIVATE key")
	notContain(t, e.errb.String(), "secretsecretsecret")
	notContain(t, e.out.String(), "secretsecretsecret")
	if len(e.fake.Log) != 0 {
		t.Fatal("a private key must never reach the API")
	}
	e.expect(exitUsage, "ssh-key", "create", "--name", "x")
	e.expect(exitUsage, "ssh-key", "create", "--public-key", testPub)
}

func TestSSHKeyOwnershipRefusal(t *testing.T) {
	e := newEnv(t)
	e.tty(true)
	e.stdin("robert-laptop\n")
	e.expect(exitRefused, "ssh-key", "delete", "robert-laptop", "--yes")
	contain(t, e.errb.String(), "did not create it")
	contain(t, e.errb.String(), "docli-")
	contain(t, e.errb.String(), "--force-unmanaged")
	e.expect(exitRefused, "ssh-key", "delete", "robert-laptop", "--yes", "--confirm-name", "robert-laptop")
	// forced but name not typed
	e.tty(false)
	e.expect(exitRefused, "ssh-key", "delete", "robert-laptop", "--force-unmanaged", "--yes")
	e.expect(exitRefused, "ssh-key", "delete", "robert-laptop", "--force-unmanaged", "--yes", "--confirm-name", "wrong")
	if len(e.fake.Requests("DELETE")) != 0 {
		t.Fatalf("nothing should have been deleted: %v", e.fake.Log)
	}
	e.mustRun("ssh-key", "delete", "robert-laptop", "--force-unmanaged", "--yes", "--confirm-name", "robert-laptop")
	if len(e.fake.Requests("DELETE /v2/account/keys/11")) != 1 {
		t.Fatal(e.fake.Log)
	}
}

// ---------- VPCs ----------

func TestVPCLifecycle(t *testing.T) {
	e := newEnv(t)
	e.expect(exitUsage, "vpc", "create", "--name", "net", "--yes") // no region
	e.env["DOCLI_REGION"] = "nyc3"
	e.mustRun("vpc", "create", "--name", "net", "--ip-range", "10.50.0.0/24", "--dry-run")
	contain(t, e.out.String(), `"docli-net" in nyc3`)
	contain(t, e.out.String(), "prefix docli-")
	if len(e.fake.Mutations()) != 0 {
		t.Fatal("dry run sent a change")
	}
	e.expect(exitRefused, "vpc", "create", "--name", "net")
	e.expect(exitUsage, "vpc", "create", "--name", "net", "--ip-range", "banana", "--yes")
	e.mustRun("vpc", "create", "--name", "net", "--ip-range", "10.50.0.0/24", "--yes", "--json")
	var v vpcJSON
	jsonOut(t, e, &v)
	if v.Name != "docli-net" || v.Region != "nyc3" || !v.Managed {
		t.Fatalf("%+v", v)
	}
	body := e.fake.Bodies["POST /v2/vpcs"][0]
	contain(t, body, `"ip_range":"10.50.0.0/24"`)
	e.expect(exitError, "vpc", "create", "--name", "net", "--yes")
	contain(t, e.errb.String(), "already exists")
	e.mustRun("vpc", "get", "net")
	contain(t, e.out.String(), "Members:   0")
	e.mustRun("vpc", "list", "--region", "nyc3")
	contain(t, e.out.String(), "docli-net")
	e.mustRun("vpc", "delete", "net", "--yes")
	if len(e.fake.Requests("DELETE /v2/vpcs/")) != 1 {
		t.Fatal(e.fake.Log)
	}
}

func TestVPCDeleteRefusesWithMembers(t *testing.T) {
	e := newEnv(t)
	e.fake.VPCs = append(e.fake.VPCs, &godo.VPC{ID: "vpc-mine", Name: "docli-mine", RegionSlug: "nyc3", IPRange: "10.9.0.0/24"})
	e.fake.addDroplet(&godo.Droplet{ID: 77, Name: "db-1", Status: "active", VPCUUID: "vpc-mine"}, "s-1vcpu-1gb", "nyc3", "10.9.0.2", "203.0.113.77")
	e.expect(exitRefused, "vpc", "delete", "docli-mine", "--yes")
	contain(t, e.errb.String(), "1 member(s)")
	contain(t, e.errb.String(), "db-1")
	contain(t, e.errb.String(), "do:droplet:77")
	e.mustRun("vpc", "get", "docli-mine", "--json")
	var d vpcDetailJSON
	jsonOut(t, e, &d)
	if len(d.Members) != 1 || d.Members[0].Name != "db-1" {
		t.Fatalf("%+v", d)
	}
	if len(e.fake.Mutations()) != 0 {
		t.Fatal("nothing should change")
	}
}

func TestVPCOwnershipRefusal(t *testing.T) {
	e := newEnv(t)
	e.expect(exitRefused, "vpc", "delete", "old-net", "--yes")
	contain(t, e.errb.String(), "did not create it")
	e.expect(exitRefused, "vpc", "delete", "old-net", "--force-unmanaged", "--yes")
	e.expect(exitRefused, "vpc", "delete", "default-nyc3", "--force-unmanaged", "--yes", "--confirm-name", "default-nyc3")
	contain(t, e.errb.String(), "default VPC")
	if len(e.fake.Requests("DELETE")) != 0 {
		t.Fatal(e.fake.Log)
	}
	e.mustRun("vpc", "delete", "old-net", "--force-unmanaged", "--yes", "--confirm-name", "old-net")
	if len(e.fake.Requests("DELETE /v2/vpcs/vpc-old")) != 1 {
		t.Fatal(e.fake.Log)
	}
}

// ---------- firewalls ----------

func TestFirewallLifecycle(t *testing.T) {
	e := newEnv(t)
	args := []string{"firewall", "create", "--name", "web", "--rule", "in:tcp:22:from=10.116.0.0/20", "--rule", "in:tcp:443:from=0.0.0.0/0,::/0", "--rule", "out:tcp:all:to=0.0.0.0/0", "--tag", "web"}
	e.mustRun(append(args, "--dry-run")...)
	contain(t, e.out.String(), "resulting rule list (3)")
	contain(t, e.out.String(), "Allow incoming TCP on port 22 from 10.116.0.0/20")
	contain(t, e.out.String(), "deny by default")
	if len(e.fake.Mutations()) != 0 {
		t.Fatal("dry run changed something")
	}
	e.expect(exitRefused, args...)
	e.mustRun(append(args, "--yes")...)
	body := e.fake.Bodies["POST /v2/firewalls"][0]
	contain(t, body, `"name":"docli-web"`)
	contain(t, body, `"ports":"22"`)
	contain(t, body, `"ports":"all"`)
	contain(t, body, `"tags":["web"]`)
	e.mustRun("firewall", "get", "web")
	contain(t, e.out.String(), "in:tcp:22:from=10.116.0.0/20")
	contain(t, e.out.String(), "anything not listed is blocked")

	// add-rule: plan shows the full resulting list
	e.mustRun("firewall", "add-rule", "web", "in:tcp:5432:from-tag=app", "--dry-run")
	contain(t, e.out.String(), "resulting rule list (4)")
	contain(t, e.out.String(), "Allow incoming TCP on port 5432 from droplets tagged app")
	e.mustRun("firewall", "add-rule", "web", "in:tcp:5432:from-tag=app", "--yes")
	e.expect(exitError, "firewall", "add-rule", "web", "in:tcp:5432:from-tag=app", "--yes")
	contain(t, e.errb.String(), "already on firewall")
	e.expect(exitUsage, "firewall", "add-rule", "web", "in:tcp:5432", "--yes")
	e.mustRun("firewall", "get", "web", "--json")
	var f firewallJSON
	jsonOut(t, e, &f)
	if len(f.Rules) != 4 {
		t.Fatalf("%+v", f.Rules)
	}
	// remove-rule
	e.expect(exitError, "firewall", "remove-rule", "web", "in:tcp:9999:from=1.2.3.4", "--yes")
	contain(t, e.errb.String(), "has no rule")
	e.mustRun("firewall", "remove-rule", "web", "in:tcp:5432:from-tag=app", "--dry-run")
	contain(t, e.out.String(), "resulting rule list (3)")
	e.mustRun("firewall", "remove-rule", "web", "in:tcp:5432:from-tag=app", "--yes")
	e.mustRun("firewall", "get", "web", "--json")
	jsonOut(t, e, &f)
	if len(f.Rules) != 3 {
		t.Fatalf("%+v", f.Rules)
	}
	// attach / detach
	e.mustRun("firewall", "attach", "web", "--droplet", "legacy-db", "--tag", "db", "--dry-run")
	contain(t, e.out.String(), "droplets: legacy-db; tags: db")
	e.mustRun("firewall", "attach", "web", "--droplet", "legacy-db", "--tag", "db", "--yes")
	e.mustRun("firewall", "get", "web", "--json")
	jsonOut(t, e, &f)
	if len(f.DropletIDs) != 1 || f.DropletIDs[0] != 1001 || len(f.Tags) != 2 {
		t.Fatalf("%+v", f)
	}
	e.mustRun("firewall", "detach", "web", "--droplet", "1001", "--tag", "db", "--yes")
	e.mustRun("firewall", "get", "web", "--json")
	jsonOut(t, e, &f)
	if len(f.DropletIDs) != 0 || len(f.Tags) != 1 || f.Tags[0] != "web" {
		t.Fatalf("%+v", f)
	}
	e.expect(exitUsage, "firewall", "attach", "web", "--yes")
	e.expect(exitError, "firewall", "attach", "web", "--droplet", "ghost", "--yes")
	// delete
	e.mustRun("firewall", "delete", "web", "--dry-run")
	contain(t, e.out.String(), "attached to 0 droplet(s) and 1 tag(s)")
	e.mustRun("firewall", "delete", "web", "--yes")
	e.mustRun("firewall", "list", "--json")
	var all []firewallJSON
	jsonOut(t, e, &all)
	if len(all) != 1 || all[0].Name != "legacy-fw" {
		t.Fatalf("%+v", all)
	}
}

func TestFirewallBadRuleIsUsageError(t *testing.T) {
	e := newEnv(t)
	e.expect(exitUsage, "firewall", "create", "--name", "x", "--rule", "in:tcp:22", "--yes")
	contain(t, e.errb.String(), "Rule format:")
	e.expect(exitUsage, "firewall", "create", "--name", "x", "--rule", "in:tcp:22:from=nope", "--yes")
	if len(e.fake.Log) != 0 {
		t.Fatal("a bad rule must be caught before any request")
	}
}

func TestFirewallWarnsAboutNoRules(t *testing.T) {
	e := newEnv(t)
	e.mustRun("firewall", "create", "--name", "empty", "--dry-run")
	contain(t, e.out.String(), "no inbound rules")
	contain(t, e.out.String(), "no outbound rules")
}

func TestFirewallOwnershipRefusal(t *testing.T) {
	e := newEnv(t)
	e.expect(exitRefused, "firewall", "delete", "legacy-fw", "--yes")
	contain(t, e.errb.String(), "did not create it")
	e.expect(exitRefused, "firewall", "delete", "legacy-fw", "--yes", "--force-unmanaged")
	if len(e.fake.Requests("DELETE")) != 0 {
		t.Fatal(e.fake.Log)
	}
	e.tty(true)
	e.stdin("legacy-fw\n")
	e.mustRun("firewall", "delete", "legacy-fw", "--force-unmanaged")
	if len(e.fake.Requests("DELETE /v2/firewalls/fw-legacy")) != 1 {
		t.Fatal(e.fake.Log)
	}
}

// ---------- droplets ----------

var createArgs = []string{"droplet", "create", "--name", "web-1", "--region", "nyc3", "--size", "s-1vcpu-1gb", "--image", "ubuntu-24-04-x64"}

func withArgs(extra ...string) []string { return append(append([]string{}, createArgs...), extra...) }

func TestDropletCreatePlanAndCost(t *testing.T) {
	e := newEnv(t)
	e.fake.Keys = append(e.fake.Keys, godo.Key{ID: 12, Name: "docli-deploy", Fingerprint: "bb"})
	e.mustRun(withArgs("--stack", "blog", "--backups", "--ssh-key", "deploy", "--tag", "app", "--dry-run")...)
	out := e.out.String()
	contain(t, out, `create droplet "web-1" in nyc3`)
	contain(t, out, "$6.00/month")
	contain(t, out, "$0.00893/hour")
	contain(t, out, "Estimated cost: $7.20 per month")
	contain(t, out, "backups add about $1.20/month")
	contain(t, out, "docli:managed, docli:stack:blog, app")
	contain(t, out, "ssh keys: docli-deploy")
	notContain(t, out, "no SSH key")
	contain(t, out, "Dry run")
	if len(e.fake.Mutations()) != 0 {
		t.Fatalf("dry run changed something: %v", e.fake.Mutations())
	}
	// JSON dry run keeps the same fields
	e.mustRun(withArgs("--dry-run", "--json")...)
	var doc struct {
		DryRun bool `json:"dry_run"`
		Plan   Plan `json:"plan"`
	}
	jsonOut(t, e, &doc)
	if !doc.DryRun || doc.Plan.MonthlyUSD == nil || *doc.Plan.MonthlyUSD != 6 || len(doc.Plan.Warnings) != 1 {
		t.Fatalf("%+v", doc)
	}
}

func TestDropletCreateNeedsConfirmation(t *testing.T) {
	e := newEnv(t)
	e.tty(false)
	e.stdin("y\n")
	e.expect(exitRefused, withArgs()...)
	contain(t, e.errb.String(), "stdin is not a terminal")
	contain(t, e.errb.String(), "--yes")
	if len(e.fake.Mutations()) != 0 {
		t.Fatal("nothing may change without confirmation")
	}
	e.tty(true)
	e.stdin("\n")
	e.expect(exitRefused, withArgs()...)
	e.stdin("n\n")
	e.expect(exitRefused, withArgs()...)
	if len(e.fake.Mutations()) != 0 {
		t.Fatal("a no (or an empty answer) must not create anything")
	}
	e.stdin("y\n")
	e.mustRun(withArgs()...)
	if len(e.fake.Requests("POST /v2/droplets")) != 1 {
		t.Fatal(e.fake.Log)
	}
}

func TestDropletCreateEndToEnd(t *testing.T) {
	e := newEnv(t)
	e.fake.Keys = append(e.fake.Keys, godo.Key{ID: 12, Name: "docli-deploy", Fingerprint: "bb"})
	e.fake.VPCs = append(e.fake.VPCs, &godo.VPC{ID: "vpc-mine", Name: "docli-mine", RegionSlug: "nyc3", IPRange: "10.9.0.0/24"})
	ud := filepath.Join(t.TempDir(), "init.sh")
	os.WriteFile(ud, []byte("#!/bin/sh\nexport DB_PASSWORD=hunter2-USERDATA-SECRET\n"), 0o600)
	e.env["DOCLI_VPC"] = "mine"
	e.env["DOCLI_SSH_KEY"] = "deploy"
	e.mustRun(withArgs("--stack", "blog", "--user-data-file", ud, "--monitoring", "--ipv6", "--wait", "--yes", "--json")...)
	var d dropletJSON
	jsonOut(t, e, &d)
	if d.Status != "active" || d.PublicIPv4 == "" || d.PrivateIPv4 == "" || !d.Managed || d.VPCID != "vpc-mine" || d.Region != "nyc3" {
		t.Fatalf("%+v", d)
	}
	for _, tag := range []string{"docli:managed", "docli:stack:blog"} {
		if !hasTag(d.Tags, tag) || !e.fake.Tags[tag] {
			t.Fatalf("tag %s missing: %+v tags=%v", tag, d.Tags, e.fake.Tags)
		}
	}
	// tags were created before the droplet
	log := strings.Join(e.fake.Log, "\n")
	if strings.Index(log, "POST /v2/tags") > strings.Index(log, "POST /v2/droplets") || strings.Count(log, "POST /v2/tags") != 2 {
		t.Fatalf("tags must be created first:\n%s", log)
	}
	body := e.fake.Bodies["POST /v2/droplets"][0]
	contain(t, body, `"vpc_uuid":"vpc-mine"`)
	contain(t, body, `"ssh_keys":[12]`)
	contain(t, body, `"monitoring":true`)
	contain(t, body, `"ipv6":true`)
	contain(t, body, "USERDATA-SECRET") // sent to DigitalOcean...
	notContain(t, e.out.String(), "USERDATA-SECRET")
	notContain(t, e.errb.String(), "USERDATA-SECRET") // ...but never shown or logged
	contain(t, e.errb.String(), "waiting for droplet")
	contain(t, e.errb.String(), "is active")
	if len(e.sleeps) == 0 {
		t.Fatal("waiting should sleep between polls")
	}
	// human output and the plan mention user data by size only
	e2 := newEnv(t)
	e2.mustRun(withArgs("--user-data-file", ud, "--dry-run")...)
	contain(t, e2.out.String(), "user data: 53 bytes")
	notContain(t, e2.out.String(), "hunter2")
}

func TestDropletCreateValidation(t *testing.T) {
	e := newEnv(t)
	for _, name := range []string{"bad_name", "-x", "has space", "x.", ""} {
		a := withArgs("--yes")
		a[3] = name
		code := e.run(a...)
		if code != exitUsage {
			t.Errorf("name %q: exit %d, want usage error", name, code)
		}
	}
	if len(e.fake.Log) != 0 {
		t.Fatal("bad names must be caught before any request")
	}
	e.expect(exitUsage, "droplet", "create", "--name", "web", "--region", "nyc3")
	e.expect(exitUsage, withArgs("--stack", "bad stack", "--yes")...)
	e.expect(exitUsage, withArgs("--tag", "bad tag", "--yes")...)
	// region/size problems are found before anything is created
	a := withArgs("--yes")
	a[5] = "ams3"
	e.expect(exitError, a...)
	contain(t, e.errb.String(), "not available")
	a[5] = "mars1"
	e.expect(exitError, a...)
	contain(t, e.errb.String(), "unknown region")
	a = withArgs("--yes")
	a[7] = "s-9vcpu"
	e.expect(exitError, a...)
	contain(t, e.errb.String(), "unknown size")
	a[7] = "s-2vcpu-2gb"
	a[5] = "sfo3"
	e.expect(exitError, a...)
	contain(t, e.errb.String(), "not available in sfo3")
	e.expect(exitError, withArgs("--vpc", "nope", "--yes")...)
	e.expect(exitError, withArgs("--vpc", "old-net", "--region", "sfo3", "--yes")...)
	e.expect(exitError, withArgs("--ssh-key", "nobody", "--yes")...)
	e.expect(exitError, withArgs("--user-data-file", "/does/not/exist", "--yes")...)
	if len(e.fake.Mutations()) != 0 {
		t.Fatalf("validation failures must not change anything: %v", e.fake.Mutations())
	}
	e.env["DOCLI_REGION"] = "nyc3"
	e.mustRun("droplet", "create", "--name", "web", "--size", "s-1vcpu-1gb", "--image", "ubuntu-24-04-x64", "--dry-run")
	contain(t, e.out.String(), "no SSH key")
	e.env["DOCLI_REGION"] = ""
	e.expect(exitUsage, "droplet", "create", "--name", "web", "--size", "s-1vcpu-1gb", "--image", "ubuntu-24-04-x64", "--dry-run")
	contain(t, e.errb.String(), "region is required")
}

func TestDropletWaitTimeout(t *testing.T) {
	e := newEnv(t)
	e.fake.StuckNew = true
	e.expect(exitError, withArgs("--wait", "--wait-timeout", "12s", "--yes")...)
	contain(t, e.errb.String(), "still \"new\"")
	contain(t, e.errb.String(), "still exists and is not deleted")
	if len(e.sleeps) != 3 || len(e.fake.Requests("DELETE")) != 0 {
		t.Fatalf("sleeps %v", e.sleeps)
	}
	if len(e.fake.Requests("POST /v2/droplets")) != 1 {
		t.Fatal("exactly one create expected")
	}
}

func TestDropletListGetFilters(t *testing.T) {
	e := newEnv(t)
	e.fake.VPCs = append(e.fake.VPCs, &godo.VPC{ID: "vpc-mine", Name: "docli-mine", RegionSlug: "nyc3", IPRange: "10.9.0.0/24"})
	e.mustRun(withArgs("--vpc", "mine", "--wait", "--yes")...)
	e.mustRun("droplet", "list")
	contain(t, e.out.String(), "legacy-db")
	contain(t, e.out.String(), "web-1")
	e.mustRun("droplet", "list", "--tag", "docli:managed", "--json")
	var ds []dropletJSON
	jsonOut(t, e, &ds)
	if len(ds) != 1 || ds[0].Name != "web-1" {
		t.Fatalf("%+v", ds)
	}
	e.mustRun("droplet", "list", "--vpc", "vpc-default-nyc3", "--json")
	jsonOut(t, e, &ds)
	if len(ds) != 1 || ds[0].Name != "legacy-db" {
		t.Fatalf("%+v", ds)
	}
	e.mustRun("droplet", "list", "--region", "sfo3", "--json")
	jsonOut(t, e, &ds)
	if len(ds) != 0 {
		t.Fatalf("%+v", ds)
	}
	e.mustRun("droplet", "get", "web-1")
	contain(t, e.out.String(), "Public IPv4:  203.0.113.")
	contain(t, e.out.String(), "Private IPv4: 10.116.0.")
	contain(t, e.out.String(), "Managed:      yes")
	e.mustRun("droplet", "get", "1001", "--json")
	var d dropletJSON
	jsonOut(t, e, &d)
	if d.PublicIPv4 != "203.0.113.10" || d.PrivateIPv4 != "10.116.0.2" || d.Managed {
		t.Fatalf("%+v", d)
	}
	e.expect(exitError, "droplet", "get", "ghost")
	e.expect(exitError, "droplet", "get", "424242")
	e.expect(exitError, "droplet", "list", "--vpc", "ghost")
	e.expect(exitUsage, "droplet", "list", "extra")
}

func TestDropletDelete(t *testing.T) {
	e := newEnv(t)
	e.mustRun(withArgs("--yes")...)
	// --yes alone is not enough to delete a droplet
	e.expect(exitRefused, "droplet", "delete", "web-1", "--yes")
	contain(t, e.errb.String(), "--yes --confirm-name web-1")
	e.expect(exitRefused, "droplet", "delete", "web-1", "--yes", "--confirm-name", "web-2")
	e.tty(true)
	e.stdin("y\n")
	e.expect(exitRefused, "droplet", "delete", "web-1")
	e.stdin("web-2\n")
	e.expect(exitRefused, "droplet", "delete", "web-1")
	e.mustRun("droplet", "delete", "web-1", "--dry-run")
	if len(e.fake.Requests("DELETE")) != 0 {
		t.Fatalf("nothing should be deleted yet: %v", e.fake.Log)
	}
	e.stdin("web-1\n")
	e.mustRun("droplet", "delete", "web-1")
	contain(t, e.out.String(), "Deleted droplet")
	if len(e.fake.Requests("DELETE /v2/droplets/")) != 1 || e.fake.droplet(5001) != nil {
		t.Fatal(e.fake.Log)
	}
	// unattended with the name
	e.tty(false)
	e.mustRun(withArgs("--yes")...)
	e.mustRun("droplet", "delete", "web-1", "--yes", "--confirm-name", "web-1", "--json")
	var del deletedJSON
	jsonOut(t, e, &del)
	if !del.Deleted || del.Type != "droplet" || del.Name != "web-1" {
		t.Fatalf("%+v", del)
	}
}

func TestDropletOwnershipRefusal(t *testing.T) {
	e := newEnv(t)
	e.tty(true)
	e.stdin("legacy-db\n")
	e.expect(exitRefused, "droplet", "delete", "legacy-db")
	contain(t, e.errb.String(), "does not have the tag docli:managed")
	contain(t, e.errb.String(), "did not create it")
	e.expect(exitRefused, "droplet", "delete", "legacy-db", "--yes", "--confirm-name", "legacy-db")
	e.tty(false)
	e.expect(exitRefused, "droplet", "delete", "legacy-db", "--force-unmanaged", "--yes")
	if len(e.fake.Requests("DELETE")) != 0 || e.fake.droplet(1001) == nil {
		t.Fatal("the unmanaged droplet must be untouched")
	}
	e.mustRun("droplet", "delete", "legacy-db", "--force-unmanaged", "--yes", "--confirm-name", "legacy-db")
	if e.fake.droplet(1001) != nil {
		t.Fatal("forced delete with the typed name should work")
	}
}

func TestDropletPower(t *testing.T) {
	e := newEnv(t)
	e.mustRun(withArgs("--wait", "--yes")...)
	e.expect(exitRefused, "droplet", "power-off", "web-1")
	e.mustRun("droplet", "power-off", "web-1", "--dry-run")
	if e.fake.droplet(5001).Status != "active" {
		t.Fatal("dry run must not power off")
	}
	e.mustRun("droplet", "power-off", "web-1", "--yes", "--wait")
	if e.fake.droplet(5001).Status != "off" {
		t.Fatal("not off")
	}
	e.mustRun("droplet", "power-on", "web-1", "--yes", "--json")
	var d dropletJSON
	jsonOut(t, e, &d)
	if d.Status != "active" {
		t.Fatalf("%+v", d)
	}
	e.mustRun("droplet", "reboot", "web-1", "--yes")
	contain(t, e.out.String(), "Requested reboot")
	e.tty(true)
	e.stdin("n\n")
	e.expect(exitRefused, "droplet", "reboot", "web-1")
	e.mustRun("droplet", "reboot", "legacy-db", "--dry-run")
	contain(t, e.out.String(), "not created by docli")
}
