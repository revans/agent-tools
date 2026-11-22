package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/digitalocean/godo"
)

// Tests for milestone 3: the stack file, plan, apply, prune and adopt. All of them run against the fake
// DigitalOcean in fakedo_test.go; nothing touches the real API.

const testPubKey = "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIOMqqetFC0v+dJ3I9MiTYvyBY0Aw0pLNUKQ1BGmVzVNH test@example"

func (e *testEnv) stackPath() string {
	return filepath.Join(e.project, "config", "deploys", "stack.yml")
}

func (e *testEnv) writeStack(content string) {
	e.t.Helper()
	if err := os.MkdirAll(filepath.Dir(e.stackPath()), 0o755); err != nil {
		e.t.Fatal(err)
	}
	if err := os.WriteFile(e.stackPath(), []byte(content), 0o644); err != nil {
		e.t.Fatal(err)
	}
}

// stackEnv is an environment with the shipped example stack in place and the public key it names.
func stackEnv(t *testing.T) *testEnv {
	t.Helper()
	e := newEnv(t)
	os.MkdirAll(filepath.Join(e.home, ".ssh"), 0o755)
	os.WriteFile(filepath.Join(e.home, ".ssh", "id_ed25519.pub"), []byte(testPubKey+"\n"), 0o644)
	e.fake.ExposeDBSizes = true
	e.writeStack(stackExample)
	return e
}

func (e *testEnv) planDoc() StackPlanDoc {
	e.t.Helper()
	var doc StackPlanDoc
	jsonOut(e.t, e, &doc)
	return doc
}

func (e *testEnv) item(doc StackPlanDoc, kind, name string) StackItem {
	e.t.Helper()
	for _, it := range doc.Items {
		if it.Kind == kind && it.Name == name {
			return it
		}
	}
	e.t.Fatalf("no %s %q in the plan: %+v", kind, name, doc.Items)
	return StackItem{}
}

func (e *testEnv) applyAll() {
	e.t.Helper()
	e.mustRun("apply", "--yes")
}

// ---------- stack init and validate ----------

func TestStackInitWritesAnExampleThatValidates(t *testing.T) {
	e := newEnv(t)
	os.MkdirAll(filepath.Join(e.home, ".ssh"), 0o755)
	os.WriteFile(filepath.Join(e.home, ".ssh", "id_ed25519.pub"), []byte(testPubKey), 0o644)
	e.mustRun("stack", "init")
	contain(t, e.out.String(), "config/deploys/stack.yml")
	b, err := os.ReadFile(e.stackPath())
	if err != nil || string(b) != stackExample {
		t.Fatalf("%v", err)
	}
	e.mustRun("stack", "validate")
	contain(t, e.out.String(), "is valid: 1 SSH key(s), 1 VPC(s), 1 firewall(s), 1 droplet(s), 1 database(s), 1 domain(s), 2 DNS record(s)")
	e.mustRun("stack", "validate", "--json")
	var v stackValidateJSON
	jsonOut(t, e, &v)
	if !v.Valid || v.Counts["droplets"] != 1 || v.Stack != "shop" {
		t.Fatalf("%+v", v)
	}
	// never overwrites
	e.expect(exitError, "stack", "init")
	contain(t, e.errb.String(), "will not overwrite")
	e.mustRun("stack", "init", "--force")
	if len(e.fake.Log) != 0 {
		t.Fatal("init and validate must not call the API")
	}
}

func TestStackInitDryRunAndFile(t *testing.T) {
	e := newEnv(t)
	e.mustRun("stack", "init", "--dry-run")
	if _, err := os.Stat(e.stackPath()); err == nil {
		t.Fatal("dry run wrote the file")
	}
	e.mustRun("stack", "init", "--file", "other/s.yml")
	if _, err := os.Stat(filepath.Join(e.project, "other", "s.yml")); err != nil {
		t.Fatal(err)
	}
}

// Every mistake names its line.
func TestStackParsingErrorsNameTheLine(t *testing.T) {
	for _, c := range []struct {
		name, yaml, want string
	}{
		{"unknown top key", "version: 1\nwidgets: []\n", "stack.yml:2: unknown key \"widgets\" in the top level"},
		{"unknown droplet key", "version: 1\ndroplets:\n  - name: web\n    colour: red\n", "stack.yml:4: unknown key \"colour\" in droplets"},
		{"unknown record key", "version: 1\ndomains:\n  - name: a.example\n    records:\n      - type: A\n        name: \"@\"\n        data: 1.2.3.4\n        weight: 3\n", "stack.yml:8: unknown key \"weight\" in records"},
		{"duplicate key", "version: 1\nversion: 1\n", "stack.yml:2: the key \"version\" appears twice (first on line 1)"},
		{"bad yaml", "version: 1\nvpcs: [\n", "not valid YAML"},
		{"wrong type", "version: 1\ndatabases:\n  - name: abc-db\n    nodes: many\n", "stack.yml:4:"},
		{"missing version", "name: x\n", "version: 1 is required"},
		{"two documents", "version: 1\n---\nversion: 1\n", "stack.yml:2: a stack file holds one YAML document"},
		{"alias", "version: 1\nvpcs:\n  - &v\n    name: a\n    region: nyc3\n  - *v\n", "YAML aliases are not supported"},
		{"list expected", "version: 1\ndroplets: nope\n", "stack.yml:2: droplets must be a list"},
		{"entry not a mapping", "version: 1\ndroplets:\n  - just-a-string\n", "stack.yml:3: each entry in droplets must be a mapping"},
		{"bad hostname", "version: 1\nregion: nyc3\ndroplets:\n  - name: Bad_Name\n    size: s\n    image: i\n", "stack.yml:4:"},
		{"duplicate name", "version: 1\nregion: nyc3\nvpcs:\n  - name: a\n  - name: a\n", "stack.yml:5: the VPC \"a\" is declared twice (first on line 4)"},
		{"firewall not declared", "version: 1\nregion: nyc3\ndroplets:\n  - name: web\n    size: s\n    image: i\n    firewalls: [nope]\n", "stack.yml:7: droplet \"web\" names the firewall \"nope\""},
		{"bad rule", "version: 1\nfirewalls:\n  - name: f\n    rules:\n      - in:tcp:22\n", "stack.yml:4: firewall \"f\": bad rule"},
		{"db without trusted source", "version: 1\nregion: nyc3\nvpcs:\n  - name: n\ndatabases:\n  - name: abc-db\n    version: \"16\"\n    size: db-s-1vcpu-1gb\n    nodes: 1\n    vpc: n\n", "stack.yml:6: database \"abc-db\" needs at least one of trusted_droplets"},
		{"db open to the world", "version: 1\nregion: nyc3\nvpcs:\n  - name: n\ndatabases:\n  - name: abc-db\n    version: \"16\"\n    size: db-s-1vcpu-1gb\n    nodes: 1\n    vpc: n\n    trusted_ips: [0.0.0.0/0]\n", "stack.yml:11: 0.0.0.0/0 would let every address"},
		{"db without vpc", "version: 1\nregion: nyc3\ndatabases:\n  - name: abc-db\n    version: \"16\"\n    size: s\n    nodes: 1\n    trusted_tags: [app]\n", "stack.yml:4: database \"abc-db\" needs a vpc"},
		{"no region", "version: 1\nvpcs:\n  - name: n\n", "stack.yml:3: VPC \"n\" has no region"},
		{"droplet ref on a CNAME", "version: 1\nregion: nyc3\ndroplets:\n  - name: web\n    size: s\n    image: i\ndomains:\n  - name: a.example\n    records:\n      - {type: CNAME, name: www, droplet: web}\n", "stack.yml:10: droplet: only works on an A record"},
		{"record needs a priority", "version: 1\ndomains:\n  - name: a.example\n    records:\n      - {type: MX, name: \"@\", data: mail.a.example.}\n", "stack.yml:5: MX record a.example:"},
		{"full record name", "version: 1\ndomains:\n  - name: a.example\n    records:\n      - {type: A, name: www.a.example, data: 1.2.3.4}\n", "stack.yml:5: record names are relative"},
		{"missing public key file", "version: 1\nssh_keys:\n  - name: k\n    public_key_file: ~/nope.pub\n", "stack.yml:4: cannot read ~/nope.pub"},
		{"private key refused", "version: 1\nssh_keys:\n  - name: k\n    public_key_file: priv.key\n", "stack.yml:4: priv.key:"},
	} {
		t.Run(c.name, func(t *testing.T) {
			e := newEnv(t)
			os.WriteFile(filepath.Join(e.project, "priv.key"), []byte("-----BEGIN OPENSSH PRIVATE KEY-----\nabc\n"), 0o600)
			e.writeStack(c.yaml)
			e.expect(exitError, "stack", "validate")
			contain(t, e.errb.String(), c.want)
			if len(e.fake.Log) != 0 {
				t.Fatal("validate called the API")
			}
		})
	}
}

func TestStackReportsEveryProblemInLineOrder(t *testing.T) {
	e := newEnv(t)
	e.writeStack("version: 1\nregion: nyc3\nvpcs:\n  - name: a\n    ip_range: nonsense\n  - name: a\n")
	e.expect(exitError, "stack", "validate")
	msg := e.errb.String()
	contain(t, msg, "2 problem(s)")
	if strings.Index(msg, "stack.yml:5") > strings.Index(msg, "stack.yml:6") {
		t.Fatalf("not in line order:\n%s", msg)
	}
}

// A stack file is committed to git: nothing secret may be in it, and nothing is read from the environment.
func TestSecretsAreRejectedInTheStackFile(t *testing.T) {
	for _, c := range []struct{ name, yaml, want string }{
		{"password key", "version: 1\ndatabases:\n  - name: abc-db\n    password: hunter2\n", "stack.yml:4: the key \"password\" looks like a secret"},
		{"nested token key", "version: 1\ndomains:\n  - name: a.example\n    records:\n      - {type: TXT, name: \"@\", data: x, api_token: y}\n", "looks like a secret"},
		{"secret key", "version: 1\nname: x\nsecret_key_base: abc\n", "stack.yml:3: the key \"secret_key_base\" looks like a secret"},
		{"private key key", "version: 1\nssh_keys:\n  - name: k\n    private_key: x\n", "looks like a secret"},
		{"env var", "version: 1\nname: $DB_NAME\n", "stack.yml:2:"},
		{"env braces", "version: 1\nname: ${STACK}\n", "environment variable or command reference"},
		{"command", "version: 1\ndomains:\n  - name: a.example\n    records:\n      - {type: TXT, name: \"@\", data: \"$(cat secret)\"}\n", "does not interpolate"},
		{"do token value", "version: 1\nname: dop_v1_0123456789abcdef\n", "looks like a secret"},
		{"db password value", "version: 1\ndomains:\n  - name: a.example\n    records:\n      - {type: TXT, name: \"@\", data: AVNS_abcdefghij}\n", "looks like a secret"},
		{"private key value", "version: 1\ndomains:\n  - name: a.example\n    records:\n      - {type: TXT, name: \"@\", data: \"-----BEGIN PRIVATE KEY-----\"}\n", "looks like a secret"},
		{"connection string", "version: 1\ndomains:\n  - name: a.example\n    records:\n      - {type: TXT, name: \"@\", data: \"postgres://u:pw@host/db\"}\n", "looks like a secret"},
	} {
		t.Run(c.name, func(t *testing.T) {
			e := newEnv(t)
			e.env["DB_NAME"] = "from-env"
			e.writeStack(c.yaml)
			e.expect(exitError, "stack", "validate")
			contain(t, e.errb.String(), c.want)
			e.expect(exitError, "plan")
			e.expect(exitError, "apply", "--yes")
			if len(e.fake.Log) != 0 {
				t.Fatalf("a rejected stack file reached the API: %v", e.fake.Log)
			}
			notContain(t, e.errb.String()+e.out.String(), "hunter2")
		})
	}
}

// ---------- plan ----------

func TestPlanEmptyStackHasNoChanges(t *testing.T) {
	e := newEnv(t)
	e.writeStack("version: 1\n")
	e.mustRun("plan")
	contain(t, e.out.String(), "No changes")
	e.mustRun("plan", "--json")
	doc := e.planDoc()
	if doc.HasChanges || len(doc.Items) != 0 {
		t.Fatalf("%+v", doc)
	}
	// what exists in the account and is not docli's is only reported
	var names []string
	for _, n := range doc.NotManaged {
		names = append(names, n.Kind+":"+n.Name)
	}
	for _, want := range []string{"droplet:legacy-db", "vpc:old-net", "firewall:legacy-fw", "ssh_key:robert-laptop", "database:legacy-pg", "domain:legacy.example"} {
		if !contains(names, want) {
			t.Fatalf("%s is not reported as in the account but not docli's: %v", want, names)
		}
	}
	if len(e.fake.Mutations()) != 0 {
		t.Fatal(e.fake.Mutations())
	}
}

func TestPlanCreateAllNeverChangesAnything(t *testing.T) {
	e := stackEnv(t)
	e.expect(exitUsage, "plan") // 2 means there are changes
	out := e.out.String()
	contain(t, out, "Plan for config/deploys/stack.yml, stack shop")
	contain(t, out, "+ droplet     shop-web-1")
	contain(t, out, "create  $6.00/month")
	contain(t, out, "+ database    shop-db")
	contain(t, out, "$15.00/month")
	contain(t, out, "Added monthly cost: $21.00")
	contain(t, out, "the public IPv4 address of droplet shop-web-1 (known once it exists)")
	contain(t, out, "Plan: 10 to create")
	contain(t, out, "Exit code 2 means there are changes. Nothing was changed.")
	if m := e.fake.Mutations(); len(m) != 0 {
		t.Fatalf("plan changed something: %v", m)
	}
	if _, err := os.Stat(e.manifest()); err == nil {
		t.Fatal("plan wrote the manifest")
	}
	e.expect(exitUsage, "plan", "--json")
	doc := e.planDoc()
	if !doc.HasChanges || doc.Summary.Create != 10 || doc.MonthlyCostAddedUSD != 21 || len(doc.CostUnknownFor) != 0 {
		t.Fatalf("%+v", doc.Summary)
	}
	// plan and --dry-run of apply agree
	e.mustRun("apply", "--dry-run", "--json")
	var dry stackDryRunDoc
	jsonOut(t, e, &dry)
	if !dry.DryRun || dry.Plan.Summary.Create != 10 || len(e.fake.Mutations()) != 0 {
		t.Fatalf("%+v", dry)
	}
}

func TestPlanWithoutKnownPriceSaysSo(t *testing.T) {
	e := stackEnv(t)
	e.fake.ExposeDBSizes = false
	e.expect(exitUsage, "plan", "--json")
	doc := e.planDoc()
	if doc.MonthlyCostAddedUSD != 6 || len(doc.CostUnknownFor) != 1 || doc.CostUnknownFor[0] != "database shop-db" {
		t.Fatalf("%v %v", doc.MonthlyCostAddedUSD, doc.CostUnknownFor)
	}
	if it := e.item(doc, "database", "shop-db"); it.MonthlyUSD != nil {
		t.Fatal("a cost was invented")
	}
}

func TestPlanFlagMistakesExitOneBecauseTwoMeansChanges(t *testing.T) {
	e := stackEnv(t)
	e.expect(exitError, "plan", "--nope")
	e.expect(exitError, "plan", "extra")
	e.expect(exitError, "plan", "--prune-data")
	e.expect(exitError, "plan", "--file", "missing.yml")
	contain(t, e.errb.String(), "docli stack init")
	e.writeStack("version: 1\n")
	e.expect(exitOK, "plan")
}

func TestPlanRejectsWhatTheAccountCannotDo(t *testing.T) {
	e := stackEnv(t)
	s := strings.NewReplacer("s-1vcpu-1gb", "s-4vcpu-8gb", "nyc3", "ams3").Replace(stackExample)
	e.writeStack(s)
	e.expect(exitError, "plan")
	contain(t, e.errb.String(), "stack.yml:")
	contain(t, e.errb.String(), "region ams3 is not available")
	e.writeStack(strings.Replace(stackExample, "s-1vcpu-1gb", "s-9vcpu-9gb", 1))
	e.expect(exitError, "plan")
	contain(t, e.errb.String(), `unknown size "s-9vcpu-9gb"`)
	e.writeStack(strings.Replace(stackExample, "ubuntu-24-04-x64", "no-such-image", 1))
	e.expect(exitError, "plan")
	contain(t, e.errb.String(), `unknown image "no-such-image"`)
	e.writeStack(strings.Replace(stackExample, `"16"`, `"99"`, 1))
	e.expect(exitError, "plan")
	contain(t, e.errb.String(), "PostgreSQL version \"99\" is not offered")
	e.writeStack(strings.Replace(stackExample, "vpc: shop-net\n    ssh_keys", "vpc: missing-net\n    ssh_keys", 1))
	e.expect(exitError, "plan")
	contain(t, e.errb.String(), "missing-net")
	if len(e.fake.Mutations()) != 0 {
		t.Fatal(e.fake.Mutations())
	}
}

// A firewall cannot be tagged, so docli will not manage one it did not create, and says so.
func TestPlanRefusesToTakeOverAnUnprefixedFirewall(t *testing.T) {
	e := stackEnv(t)
	e.writeStack("version: 1\nfirewalls:\n  - name: legacy-fw\n    rules:\n      - in:tcp:22:from=0.0.0.0/0\n")
	e.expect(exitError, "plan")
	contain(t, e.errb.String(), "docli did not create it")
	contain(t, e.errb.String(), "stack.yml:3")
}

// ---------- apply ----------

func TestApplyFullStack(t *testing.T) {
	e := stackEnv(t)
	e.mustRun("apply", "--yes")
	out := e.out.String()
	for _, want := range []string{
		`done: create SSH key "docli-deploy"`, `done: create VPC "docli-shop-net"`, `done: create firewall "docli-shop-web"`,
		`done: create droplet "shop-web-1"`, `done: wait for droplet "shop-web-1" to be active`, `done: attach droplet "shop-web-1" to firewall "docli-shop-web"`,
		`done: create database cluster "shop-db"`, `done: wait for database cluster "shop-db" to be online`, `done: create user "shop" on "shop-db"`,
		`done: create database "shop_production" on "shop-db"`, `done: create domain "shop.example"`, `done: create A record shop.example`,
		`done: create CNAME record www.shop.example`, "Applied 13 step(s).",
		"docli db connection shop-db --user shop --password-file PATH",
	} {
		contain(t, out, want)
	}
	// what exists now
	var d *fakeDroplet
	for _, x := range e.fake.Droplets {
		if x.Name == "shop-web-1" {
			d = x
		}
	}
	if d == nil || !hasTag(d.Tags, managedTag) || !hasTag(d.Tags, "docli:stack:shop") || !hasTag(d.Tags, "app") || d.Status != "active" {
		t.Fatalf("%+v", d)
	}
	vpcID, fwID := "", ""
	for _, v := range e.fake.VPCs {
		if v.Name == "docli-shop-net" {
			vpcID = v.ID
		}
	}
	for _, f := range e.fake.Firewalls {
		if f.Name == "docli-shop-web" {
			fwID = f.ID
			if len(f.DropletIDs) != 1 || f.DropletIDs[0] != d.ID || len(f.InboundRules) != 3 || len(f.OutboundRules) != 3 {
				t.Fatalf("%+v", f)
			}
		}
	}
	if vpcID == "" || fwID == "" || d.VPCUUID != vpcID {
		t.Fatalf("vpc %q fw %q droplet vpc %q", vpcID, fwID, d.VPCUUID)
	}
	db := e.fake.dbByName("shop-db")
	if db == nil || !hasTag(db.Tags, managedTag) || db.PrivateNetworkUUID != vpcID || len(db.Rules) != 1 || db.Rules[0].Type != "droplet" || db.Rules[0].Value != itoa(d.ID) || db.Status != "online" {
		t.Fatalf("%+v", db)
	}
	if len(db.Users) != 3 || db.Users[2].Name != "shop" || !contains(db.DBNames, "shop_production") {
		t.Fatalf("%+v %v", db.Users, db.DBNames)
	}
	var a, cname *godo.DomainRecord
	for i, r := range e.fake.Records["shop.example"] {
		if r.Type == "A" {
			a = &e.fake.Records["shop.example"][i]
		}
		if r.Type == "CNAME" {
			cname = &e.fake.Records["shop.example"][i]
		}
	}
	if a == nil || a.Data != "203.0.113."+itoa(d.ID%250) || a.TTL != 300 || a.Name != "@" || cname == nil || cname.Data != "shop.example." {
		t.Fatalf("%+v %+v", a, cname)
	}
	// the manifest was updated as it went
	m := e.readManifest()
	for _, want := range []string{"shop-web-1", "docli-shop-net", "docli-shop-web", "docli-deploy", "shop-db", "created_by_docli: true", "203.0.113."} {
		contain(t, m, want)
	}
	// a rerun does nothing at all
	e.fake.Log = nil
	e.mustRun("apply", "--yes")
	contain(t, e.out.String(), "Nothing to do")
	if m := e.fake.Mutations(); len(m) != 0 {
		t.Fatalf("a second apply changed something: %v", m)
	}
	e.mustRun("plan")
	contain(t, e.out.String(), "No changes")
	e.mustRun("plan", "--json")
	doc := e.planDoc()
	if doc.HasChanges || doc.Summary.Unchanged != 10 || doc.Summary.Orphaned != 1 { // the fake's default user "app" is not in the file
		t.Fatalf("%+v", doc.Summary)
	}
}

// docli never retries a create: a failure stops the run and says exactly what was done and what was not.
func TestApplyStopsAtTheFirstFailureAndSaysWhatWasDone(t *testing.T) {
	e := stackEnv(t)
	e.fake.Fails["POST /v2/droplets"] = []fakeFail{{Status: 500}}
	e.expect(exitError, "apply", "--yes")
	out := e.out.String()
	contain(t, out, "Stopped at the first failure. Nothing was retried")
	contain(t, out, "Done (3):")
	contain(t, out, `create SSH key "docli-deploy"`)
	contain(t, out, `create VPC "docli-shop-net"`)
	contain(t, out, `create firewall "docli-shop-web"`)
	contain(t, out, `FAILED:`+"\n"+`  - create droplet "shop-web-1": `)
	contain(t, out, "Not done (9):")
	contain(t, out, `create database cluster "shop-db"`)
	contain(t, out, `create CNAME record www.shop.example`)
	if n := len(e.fake.Requests("POST /v2/droplets")); n != 1 {
		t.Fatalf("the create was tried %d times; it must never be retried", n)
	}
	if len(e.fake.Requests("POST /v2/databases")) != 0 || len(e.fake.Requests("POST /v2/domains")) != 0 {
		t.Fatal("steps after the failure ran")
	}
	// what was done is in the manifest, what was not is not
	m := e.readManifest()
	contain(t, m, "docli-shop-net")
	notContain(t, m, "shop-web-1")
	// the secrets the server echoed are not in any output
	for _, s := range []string{fakeToken, fakeDBPassword} {
		notContain(t, out+e.errb.String(), s)
	}
	// fix the cause: the rerun does only what is left
	before := len(e.fake.Requests("POST /v2/vpcs"))
	e.mustRun("apply", "--yes")
	if len(e.fake.Requests("POST /v2/vpcs")) != before || e.fake.count("POST /v2/account/keys") != 1 || e.fake.count("POST /v2/firewalls") != 1 {
		t.Fatalf("the rerun repeated a create: %v", e.fake.Mutations())
	}
	e.mustRun("plan")
	contain(t, e.out.String(), "No changes")
}

func TestApplyFailureJSON(t *testing.T) {
	e := stackEnv(t)
	e.fake.Fails["POST /v2/vpcs"] = []fakeFail{{Status: 422, Message: "name already in use"}}
	e.expect(exitError, "apply", "--yes", "--json")
	var doc StackApplyDoc
	jsonOut(t, e, &doc)
	if doc.Applied || len(doc.Done) != 1 || len(doc.Failed) != 1 || doc.Failed[0].Step != `create VPC "docli-shop-net"` || len(doc.NotDone) != 11 ||
		!strings.Contains(doc.Failed[0].Error, "name already in use") {
		t.Fatalf("%+v", doc)
	}
}

func TestApplyCancelledAndRefused(t *testing.T) {
	e := stackEnv(t)
	e.expect(exitRefused, "apply") // no terminal, no --yes
	contain(t, e.errb.String(), "Nothing was changed")
	e.tty(true)
	e.stdin("n\n")
	e.expect(exitRefused, "apply")
	contain(t, e.errb.String(), "cancelled")
	e.stdin("y\n")
	e.mustRun("apply")
	if len(e.fake.Mutations()) == 0 {
		t.Fatal("answering y did nothing")
	}
}

func TestApplyDryRunChangesNothing(t *testing.T) {
	e := stackEnv(t)
	e.mustRun("apply", "--dry-run")
	contain(t, e.out.String(), "Dry run: nothing was changed.")
	if len(e.fake.Mutations()) != 0 {
		t.Fatal(e.fake.Mutations())
	}
	if _, err := os.Stat(e.manifest()); err == nil {
		t.Fatal("dry run wrote the manifest")
	}
}

// Changes that cannot be made in place are reported and never done.
func TestApplyReportsNeedsReplacementAndDoesNotDoIt(t *testing.T) {
	e := stackEnv(t)
	e.applyAll()
	for _, d := range e.fake.Droplets {
		if d.Name == "shop-web-1" {
			d.SizeSlug = "s-2vcpu-2gb"
			d.Region = &godo.Region{Slug: "sfo3"}
		}
	}
	e.fake.dbByName("shop-db").VersionSlug = "15"
	e.fake.Log = nil
	e.expect(exitUsage, "plan", "--json")
	doc := e.planDoc()
	d := e.item(doc, "droplet", "shop-web-1")
	if d.Action != actReplace || !strings.Contains(strings.Join(d.Details, "\n"), "size: file says s-1vcpu-1gb, the account has s-2vcpu-2gb") || !strings.Contains(strings.Join(d.Details, "\n"), "region: file says nyc3") {
		t.Fatalf("%+v", d)
	}
	if db := e.item(doc, "database", "shop-db"); db.Action != actReplace || !strings.Contains(strings.Join(db.Details, "\n"), "version: file says 16, the account has 15") {
		t.Fatalf("%+v", db)
	}
	e.mustRun("apply", "--yes")
	contain(t, e.out.String(), "Needs replacement, not done (docli never replaces these automatically): droplet shop-web-1, database shop-db")
	if m := e.fake.Mutations(); len(m) != 0 {
		t.Fatalf("apply did something about a replacement: %v", m)
	}
}

func TestApplyChangesFirewallDatabaseAndDNSInPlace(t *testing.T) {
	e := stackEnv(t)
	e.applyAll()
	// someone changed things by hand
	for i, f := range e.fake.Firewalls {
		if f.Name == "docli-shop-web" {
			e.fake.Firewalls[i].InboundRules = append(e.fake.Firewalls[i].InboundRules, godo.InboundRule{Protocol: "tcp", PortRange: "8080", Sources: &godo.Sources{Addresses: []string{"0.0.0.0/0"}}})
			e.fake.Firewalls[i].InboundRules = e.fake.Firewalls[i].InboundRules[1:] // and removed the ssh rule
		}
	}
	db := e.fake.dbByName("shop-db")
	db.SizeSlug = "db-s-2vcpu-4gb"
	db.Rules = append(db.Rules, godo.DatabaseFirewallRule{UUID: "x", Type: "ip_addr", Value: "198.51.100.9"})
	for i, r := range e.fake.Records["shop.example"] {
		if r.Type == "CNAME" {
			e.fake.Records["shop.example"][i].TTL = 60
		}
		if r.Type == "A" {
			e.fake.Records["shop.example"][i].Data = "192.0.2.1"
		}
	}
	e.expect(exitUsage, "plan", "--json")
	doc := e.planDoc()
	for _, c := range []struct{ kind, name, action, detail string }{
		{"firewall", "shop-web", actChange, "add rule in:tcp:22:from=0.0.0.0/0,::/0"},
		{"firewall", "shop-web", actChange, "remove rule in:tcp:8080:from=0.0.0.0/0"},
		{"database", "shop-db", actChange, "resize: db-s-2vcpu-4gb x 1 node(s) now, db-s-1vcpu-1gb x 1 after"},
		{"database", "shop-db", actChange, "stop trusting ip_addr:198.51.100.9"},
		{"dns_record", "CNAME www.shop.example", actChange, "ttl: 60 -> 1800"},
		{"dns_record", "A shop.example", actChange, "data: 192.0.2.1 -> 203.0.113."},
	} {
		it := e.item(doc, c.kind, c.name)
		if it.Action != c.action || !strings.Contains(strings.Join(it.Details, "\n"), c.detail) {
			t.Fatalf("%s %s: %+v, want %q", c.kind, c.name, it, c.detail)
		}
	}
	e.mustRun("apply", "--yes")
	e.mustRun("plan")
	contain(t, e.out.String(), "No changes")
	if db.SizeSlug != "db-s-1vcpu-1gb" || len(db.Rules) != 1 {
		t.Fatalf("%v %v", db.SizeSlug, db.Rules)
	}
	for _, r := range e.fake.Records["shop.example"] {
		if r.Type == "A" && !strings.HasPrefix(r.Data, "203.0.113.") || r.Type == "CNAME" && r.TTL != 1800 {
			t.Fatalf("%+v", r)
		}
	}
}

// What exists but is not docli's is used as it is and never changed.
func TestApplyNeverTouchesWhatDocliDidNotCreate(t *testing.T) {
	e := stackEnv(t)
	e.writeStack(`version: 1
region: nyc3
vpcs:
  - name: old-net
droplets:
  - name: legacy-db
    size: s-2vcpu-2gb
    image: debian-12-x64
    vpc: old-net
    firewalls: [guard]
firewalls:
  - name: guard
    rules:
      - in:tcp:22:from=0.0.0.0/0
databases:
  - name: legacy-pg
    version: "16"
    size: db-s-2vcpu-4gb
    nodes: 2
    vpc: old-net
    trusted_droplets: [legacy-db]
    users: [extra]
domains:
  - name: legacy.example
    records:
      - {type: A, name: "@", data: 203.0.113.10, ttl: 60}
      - {type: A, name: www, data: 203.0.113.10}
`)
	e.expect(exitUsage, "plan", "--json")
	doc := e.planDoc()
	for _, c := range []struct{ kind, name, action string }{
		{"vpc", "old-net", actUnmanaged}, {"droplet", "legacy-db", actUnmanaged}, {"database", "legacy-pg", actUnmanaged}, {"domain", "legacy.example", actUnmanaged},
		{"dns_record", "A legacy.example", actUnmanaged}, {"dns_record", "A www.legacy.example", actCreate}, {"firewall", "guard", actCreate},
	} {
		if it := e.item(doc, c.kind, c.name); it.Action != c.action {
			t.Fatalf("%s %s is %s, want %s", c.kind, c.name, it.Action, c.action)
		}
	}
	e.mustRun("apply", "--yes")
	for _, m := range e.fake.Mutations() {
		for _, forbidden := range []string{"PUT /v2/databases", "DELETE", "/resize", "PUT /v2/domains", "/users", "POST /v2/droplets", "POST /v2/databases"} {
			if strings.Contains(m, forbidden) {
				t.Fatalf("touched something that is not docli's: %s", m)
			}
		}
	}
	// the firewall was not attached to a droplet docli does not own
	for _, f := range e.fake.Firewalls {
		if f.Name == "docli-guard" && len(f.DropletIDs) != 0 {
			t.Fatalf("attached a droplet docli did not create: %+v", f)
		}
	}
	contain(t, e.out.String(), "not attached to its firewall(s) guard")
}

// ---------- orphans and prune ----------

func TestPlanShowsOrphansAndNeverDeletesThem(t *testing.T) {
	e := stackEnv(t)
	e.applyAll()
	e.writeStack("version: 1\nname: shop\nregion: nyc3\n")
	e.expect(exitOK, "plan") // orphans are reported, they are not changes
	contain(t, e.out.String(), "orphaned, not deleted")
	e.mustRun("plan", "--json")
	doc := e.planDoc()
	for _, c := range [][2]string{{"droplet", "shop-web-1"}, {"database", "shop-db"}, {"vpc", "shop-net"}, {"firewall", "shop-web"}, {"ssh_key", "deploy"}, {"domain", "shop.example"}} {
		if it := e.item(doc, c[0], c[1]); it.Action != actOrphaned {
			t.Fatalf("%+v", it)
		}
	}
	e.fake.Log = nil
	e.mustRun("apply", "--yes")
	if m := e.fake.Mutations(); len(m) != 0 {
		t.Fatalf("apply deleted without --prune: %v", m)
	}
	// another stack's droplets are not this stack's orphans, and nothing foreign is ever one
	e.fake.addDroplet(&godo.Droplet{ID: 3001, Name: "other-stack-web", Status: "active", Tags: []string{managedTag, "docli:stack:other"}}, "s-1vcpu-1gb", "nyc3", "10.0.0.1", "203.0.113.77")
	e.mustRun("plan", "--json")
	doc = e.planDoc()
	for _, it := range doc.Items {
		if it.Name == "other-stack-web" || it.Name == "legacy-db" {
			t.Fatalf("%+v", it)
		}
	}
}

func orphanEnv(t *testing.T) *testEnv {
	e := stackEnv(t)
	e.applyAll()
	e.writeStack("version: 1\nname: shop\nregion: nyc3\n")
	e.fake.Log = nil
	return e
}

func TestPruneRefusals(t *testing.T) {
	e := orphanEnv(t)
	// the word is always needed
	e.expect(exitRefused, "apply", "--prune", "--prune-data", "--yes")
	contain(t, e.errb.String(), `deleting needs the word "prune"`)
	e.expect(exitRefused, "apply", "--prune", "--prune-data", "--yes", "--confirm-prune", "yes")
	contain(t, e.errb.String(), `--confirm-prune must be the word "prune"`)
	// droplets, databases and domains also need their names confirmed, even with --yes
	e.expect(exitRefused, "apply", "--prune", "--prune-data", "--yes", "--confirm-prune", "prune")
	contain(t, e.errb.String(), "needs its name")
	// without --prune-data, droplets and databases are never deleted
	e.mustRun("apply", "--prune", "--yes", "--confirm-prune", "prune", "--prune-confirm-name", "shop.example")
	contain(t, e.out.String(), "not pruned: it may hold data")
	for _, m := range e.fake.Mutations() {
		if strings.Contains(m, "/v2/droplets") || strings.Contains(m, "/v2/databases") {
			t.Fatalf("pruned data without --prune-data: %s", m)
		}
	}
	// a plan with --prune shows deletions and exits 2 but changes nothing
	e.fake.Log = nil
	e.expect(exitUsage, "plan", "--prune", "--prune-data")
	contain(t, e.out.String(), "PRUNE (delete)")
	if len(e.fake.Mutations()) != 0 {
		t.Fatal("plan deleted something")
	}
	e.expect(exitUsage, "apply", "--prune-data")
	contain(t, e.errb.String(), "--prune-data only makes sense with --prune")
}

func TestPruneDeletesOnlyWhatDocliCreatedAndRecordsIt(t *testing.T) {
	e := orphanEnv(t)
	e.fake.addDroplet(&godo.Droplet{ID: 3002, Name: "foreign-web", Status: "active", Tags: []string{"mine"}}, "s-1vcpu-1gb", "nyc3", "10.0.0.2", "203.0.113.78")
	e.mustRun("apply", "--prune", "--prune-data", "--yes", "--confirm-prune", "prune",
		"--prune-confirm-name", "shop-web-1", "--prune-confirm-name", "shop-db", "--prune-confirm-name", "shop.example")
	out := e.out.String()
	for _, want := range []string{`delete domain "shop.example" and every record in it`, `delete database cluster "shop-db"`, `delete droplet "shop-web-1"`,
		`delete firewall "docli-shop-web"`, `delete VPC "docli-shop-net"`, `delete SSH key "docli-deploy"`} {
		contain(t, out, want)
	}
	// the order is the reverse of creation
	order := []string{"delete domain", "delete database", "delete droplet", "delete firewall", "delete VPC", "delete SSH key"}
	last := -1
	for _, o := range order {
		i := strings.Index(out, "done: "+o)
		if i < last {
			t.Fatalf("deleted out of order:\n%s", out)
		}
		last = i
	}
	// everything foreign is still there
	if e.fake.droplet(1001) == nil || e.fake.droplet(3002) == nil || e.fake.dbByName("legacy-pg") == nil || len(e.fake.Domains) != 1 || len(e.fake.VPCs) != 2 || len(e.fake.Keys) != 1 || len(e.fake.Firewalls) != 1 {
		t.Fatal("pruned something docli did not create")
	}
	notContain(t, e.readManifest(), "shop-web-1")
	e.mustRun("plan")
	contain(t, e.out.String(), "No changes")
}

func TestPruneAsksForTheWordAndEachName(t *testing.T) {
	e := orphanEnv(t)
	e.tty(true)
	e.stdin("y\nwrong\n")
	e.expect(exitRefused, "apply", "--prune", "--prune-data")
	contain(t, e.errb.String(), `the word did not match`)
	if len(e.fake.Mutations()) != 0 {
		t.Fatal(e.fake.Mutations())
	}
	// right word, a wrong name for the first thing that needs one
	e.stdin("y\nprune\nnot-the-name\n")
	e.expect(exitRefused, "apply", "--prune", "--prune-data")
	contain(t, e.errb.String(), "the name did not match")
	if len(e.fake.Mutations()) != 0 {
		t.Fatalf("something was deleted before every confirmation was given: %v", e.fake.Mutations())
	}
	// everything typed correctly (the order is the order of deletion)
	e.stdin("y\nprune\nshop.example\nshop-db\nshop-web-1\ny\ny\ny\n")
	e.mustRun("apply", "--prune", "--prune-data")
	if e.fake.dbByName("shop-db") != nil || e.fake.droplet(1001) == nil {
		t.Fatal("wrong things were deleted")
	}
}

// Anything docli did not record in this project's manifest is not pruned, even if it carries the docli name.
func TestPruneNeedsThisProjectsManifest(t *testing.T) {
	e := orphanEnv(t)
	os.Remove(e.manifest())
	e.mustRun("apply", "--prune", "--prune-data", "--yes", "--confirm-prune", "prune", "--prune-confirm-name", "shop-web-1", "--prune-confirm-name", "shop-db")
	contain(t, e.out.String(), "not pruned: this project's manifest does not list it")
	for _, m := range e.fake.Mutations() {
		if strings.HasPrefix(m, "DELETE") {
			t.Fatalf("pruned without the manifest: %s", m)
		}
	}
}

func TestPruneRefusesAVPCThatStillHasMembers(t *testing.T) {
	e := orphanEnv(t)
	// keep the droplet in the file so it stays in the VPC, drop the VPC from the file
	e.writeStack("version: 1\nname: shop\nregion: nyc3\ndroplets:\n  - name: shop-web-1\n    size: s-1vcpu-1gb\n    image: ubuntu-24-04-x64\n")
	e.mustRun("apply", "--prune", "--yes", "--confirm-prune", "prune", "--prune-confirm-name", "shop.example")
	contain(t, e.out.String(), "not pruned: it still has 1 member(s)")
}

func TestPruneNeverDeletesUsersOrDatabasesInsideACluster(t *testing.T) {
	e := stackEnv(t)
	e.applyAll()
	db := e.fake.dbByName("shop-db")
	db.DBNames = append(db.DBNames, "scratch")
	e.mustRun("plan", "--json", "--prune", "--prune-data")
	doc := e.planDoc()
	if it := e.item(doc, "db_database", "shop-db/scratch"); it.Action != actOrphaned {
		t.Fatalf("%+v", it)
	}
	if it := e.item(doc, "db_user", "shop-db/app"); it.Action != actOrphaned {
		t.Fatalf("%+v", it)
	}
	e.mustRun("apply", "--yes", "--prune", "--prune-data", "--confirm-prune", "prune")
	if !contains(db.DBNames, "scratch") {
		t.Fatal("a database inside the cluster was deleted")
	}
}

// ---------- adopt ----------

func TestAdoptDropletAddsOnlyTheTag(t *testing.T) {
	e := newEnv(t)
	e.mustRun("adopt", "droplet", "legacy-db", "--dry-run")
	contain(t, e.out.String(), "add the tag(s) docli:managed")
	contain(t, e.out.String(), "no rename, no restart, no other tag removed")
	if len(e.fake.Mutations()) != 0 {
		t.Fatal(e.fake.Mutations())
	}
	e.expect(exitRefused, "adopt", "droplet", "legacy-db") // no terminal, no --yes
	e.fake.droplet(1001).Tags = []string{"mine"}
	e.mustRun("adopt", "droplet", "legacy-db", "--yes", "--stack", "shop", "--json")
	var doc adoptDoc
	jsonOut(t, e, &doc)
	if !doc.Adopted || !doc.APIChanged || doc.ID != "1001" {
		t.Fatalf("%+v", doc)
	}
	d := e.fake.droplet(1001)
	if !hasTag(d.Tags, "mine") || !hasTag(d.Tags, managedTag) || !hasTag(d.Tags, "docli:stack:shop") || d.Name != "legacy-db" || d.Status != "active" {
		t.Fatalf("%+v", d.Droplet)
	}
	for _, m := range e.fake.Mutations() {
		if !strings.HasPrefix(m, "POST /v2/tags") {
			t.Fatalf("adopt did more than tag: %s", m)
		}
	}
	contain(t, e.readManifest(), "legacy-db")
	// adopting again changes nothing on DigitalOcean
	e.fake.Log = nil
	e.mustRun("adopt", "droplet", "1001", "--yes", "--stack", "shop", "--json")
	jsonOut(t, e, &doc)
	if doc.APIChanged || len(e.fake.Mutations()) != 0 {
		t.Fatalf("%+v %v", doc, e.fake.Mutations())
	}
	// and a stack file can now manage it
	e.writeStack("version: 1\nregion: nyc3\ndroplets:\n  - name: legacy-db\n    size: s-1vcpu-1gb\n    image: ubuntu-24-04-x64\n")
	e.mustRun("plan", "--json")
	if it := e.item(e.planDoc(), "droplet", "legacy-db"); it.Action != actUnchanged {
		t.Fatalf("%+v", it)
	}
}

func TestAdoptDatabase(t *testing.T) {
	e := newEnv(t)
	e.mustRun("adopt", "database", "legacy-pg", "--yes")
	if !hasTag(e.fake.dbByName("legacy-pg").Tags, managedTag) {
		t.Fatal("not tagged")
	}
	contain(t, e.readManifest(), "legacy-pg")
}

func TestAdoptDomainAndRecordOnlyWriteTheManifest(t *testing.T) {
	e := newEnv(t)
	e.mustRun("adopt", "record", "legacy.example", "901", "--yes")
	if len(e.fake.Mutations()) != 0 {
		t.Fatalf("adopting a record changed DigitalOcean: %v", e.fake.Mutations())
	}
	m := e.readManifest()
	contain(t, m, "created_by_docli: false")
	contain(t, m, "id: 901")
	// now docli may update that record, and only that record
	e.mustRun("dns", "update", "legacy.example", "901", "--data", "203.0.113.50", "--yes")
	e.expect(exitRefused, "dns", "update", "legacy.example", "902", "--ttl", "60", "--yes")
	e.mustRun("adopt", "domain", "legacy.example", "--dry-run")
	contain(t, e.out.String(), "nothing changes on DigitalOcean")
	e.mustRun("adopt", "domain", "legacy.example", "--yes")
	contain(t, e.readManifest(), "created_by_docli: true")
	for _, m := range e.fake.Mutations() {
		if !strings.HasPrefix(m, "PUT /v2/domains/legacy.example/records/901") {
			t.Fatalf("unexpected change: %s", m)
		}
	}
	e.expect(exitUsage, "adopt", "domain", "legacy.example", "--no-manifest")
}

func TestAdoptRefusesWhatCannotBeTaggedAndMisuse(t *testing.T) {
	e := newEnv(t)
	for _, k := range []string{"vpc", "firewall", "ssh-key"} {
		e.expect(exitRefused, "adopt", k, "x")
		contain(t, e.errb.String(), "will not rename")
	}
	e.expect(exitUsage, "adopt")
	e.expect(exitUsage, "adopt", "widget", "x")
	e.expect(exitUsage, "adopt", "droplet")
	e.expect(exitUsage, "adopt", "record", "legacy.example")
	e.expect(exitError, "adopt", "droplet", "nope")
	if len(e.fake.Mutations()) != 0 {
		t.Fatal(e.fake.Mutations())
	}
}

// ---------- secrets never appear ----------

// The token and the passwords the fake hands out must not appear in anything docli prints, whatever happens.
func TestNoSecretInAnyStackOutput(t *testing.T) {
	secrets := []string{fakeToken, fakeDBPassword, fakeUserPassword, "AVNS_"}
	check := func(e *testEnv, what string) {
		t.Helper()
		all := e.out.String() + e.errb.String()
		if _, err := os.Stat(e.manifest()); err == nil {
			all += e.readManifest()
		}
		all += e.stackPathContents()
		for _, s := range secrets {
			if strings.Contains(all, s) {
				t.Fatalf("%s: %q leaked:\n%s", what, s, all)
			}
		}
	}
	e := stackEnv(t)
	e.app.Stdin = strings.NewReader("")
	e.run("plan", "--debug")
	check(e, "plan --debug")
	e.run("plan", "--json")
	check(e, "plan json")
	e.run("apply", "--yes", "--debug")
	check(e, "apply --debug")
	e.run("apply", "--yes", "--json")
	check(e, "apply json")
	e.run("plan", "--json")
	check(e, "plan after")
	e.run("adopt", "database", "legacy-pg", "--yes", "--debug")
	check(e, "adopt")
	// failures where the server echoes the token and a password
	for _, path := range []string{"POST /v2/droplets", "POST /v2/databases", "POST /v2/vpcs", "POST /v2/domains"} {
		e2 := stackEnv(t)
		e2.fake.Fails[path] = []fakeFail{{Status: 500}}
		e2.run("apply", "--yes", "--debug")
		check(e2, "failure at "+path)
		e2.fake.Fails["GET /v2/droplets"] = []fakeFail{{Status: 400}}
		e2.run("plan")
		check(e2, "plan failure")
	}
	// a rejected token
	e3 := stackEnv(t)
	e3.fake.token = "other"
	e3.expect(exitError, "plan")
	check(e3, "rejected token")
}

// count is how many requests were exactly this method and path.
func (f *fakeDO) count(methodPath string) int {
	n := 0
	for _, l := range f.Requests(methodPath) {
		if l == methodPath {
			n++
		}
	}
	return n
}

func (e *testEnv) stackPathContents() string {
	b, err := os.ReadFile(e.stackPath())
	if err != nil {
		return ""
	}
	return string(b)
}

// ---------- golden JSON ----------

func TestStackJSONGolden(t *testing.T) {
	norm := func(s string) string { return s }
	// empty
	e := newEnv(t)
	e.writeStack("version: 1\n")
	e.mustRun("plan", "--json")
	golden(t, "plan_empty.json", norm(e.out.String()))

	// create everything
	e = stackEnv(t)
	e.expect(exitUsage, "plan", "--json")
	golden(t, "plan_create_all.json", e.out.String())
	e.mustRun("apply", "--dry-run", "--json")
	golden(t, "apply_dry_run.json", e.out.String())
	e.mustRun("stack", "validate", "--json")
	golden(t, "stack_validate.json", e.out.String())

	// partial: the droplet and its attachment are gone
	e.mustRun("apply", "--yes", "--json")
	golden(t, "apply_full.json", e.out.String())
	e.mustRun("apply", "--yes", "--json")
	golden(t, "apply_nothing.json", e.out.String())
	e.mustRun("plan", "--json")
	golden(t, "plan_in_sync.json", e.out.String())
	e.fake.Droplets = e.fake.Droplets[:1]
	e.expect(exitUsage, "plan", "--json")
	golden(t, "plan_partial.json", e.out.String())

	// drift
	e = stackEnv(t)
	e.applyAll()
	for i, f := range e.fake.Firewalls {
		if f.Name == "docli-shop-web" {
			e.fake.Firewalls[i].InboundRules = e.fake.Firewalls[i].InboundRules[1:]
		}
	}
	for _, d := range e.fake.Droplets {
		if d.Name == "shop-web-1" {
			d.SizeSlug = "s-2vcpu-2gb"
		}
	}
	e.fake.dbByName("shop-db").SizeSlug = "db-s-2vcpu-4gb"
	e.expect(exitUsage, "plan", "--json")
	golden(t, "plan_drift.json", e.out.String())

	// orphaned, and with --prune
	e = stackEnv(t)
	e.applyAll()
	e.writeStack("version: 1\nname: shop\nregion: nyc3\n")
	e.mustRun("plan", "--json")
	golden(t, "plan_orphaned.json", e.out.String())
	e.expect(exitUsage, "plan", "--json", "--prune", "--prune-data")
	golden(t, "plan_prune.json", e.out.String())

	// a failed apply
	e = stackEnv(t)
	e.fake.Fails["POST /v2/droplets"] = []fakeFail{{Status: 422, Message: "size not allowed"}}
	e.expect(exitError, "apply", "--yes", "--json")
	golden(t, "apply_failed.json", regexp.MustCompile(`127\.0\.0\.1:\d+`).ReplaceAllString(e.out.String(), "FAKE-HOST"))

	// adopt
	e = newEnv(t)
	e.mustRun("adopt", "droplet", "legacy-db", "--yes", "--json")
	golden(t, "adopt_droplet.json", e.out.String())
	e.mustRun("adopt", "record", "legacy.example", "901", "--yes", "--json")
	golden(t, "adopt_record.json", e.out.String())
}

// Every item has every field, and nothing is null except an unknown cost.
func TestStackPlanJSONFieldsAreStable(t *testing.T) {
	e := stackEnv(t)
	e.expect(exitUsage, "plan", "--json")
	var raw map[string]any
	jsonOut(t, e, &raw)
	for _, k := range []string{"stack_file", "stack", "has_changes", "summary", "monthly_cost_added_usd", "cost_unknown_for", "items", "not_managed", "warnings"} {
		if _, ok := raw[k]; !ok {
			t.Fatalf("missing %s", k)
		}
	}
	for _, v := range raw["items"].([]any) {
		it := v.(map[string]any)
		if len(it) != 6 {
			t.Fatalf("%v", it)
		}
		if _, ok := it["details"].([]any); !ok {
			t.Fatalf("details must be a list: %v", it)
		}
	}
	for _, k := range []string{"cost_unknown_for", "not_managed", "warnings"} {
		if _, ok := raw[k].([]any); !ok {
			t.Fatalf("%s must be a list", k)
		}
	}
	var _ = json.Marshal
}

// A mistake in the stack file is reported without a token and without the network.
func TestStackFileErrorsNeedNoToken(t *testing.T) {
	e := newEnv(t)
	e.env = map[string]string{}
	e.writeStack("version: 1\nwidgets: 1\n")
	e.expect(exitError, "plan")
	contain(t, e.errb.String(), "stack.yml:2: unknown key")
	e.expect(exitError, "apply", "--yes")
	contain(t, e.errb.String(), "stack.yml:2: unknown key")
}

// User data may hold anything; docli sends it and never prints it.
func TestUserDataIsSentAndNeverPrinted(t *testing.T) {
	e := stackEnv(t)
	const planted = "PLANTED-USER-DATA-MARKER-9f3"
	os.WriteFile(filepath.Join(e.project, "cloud-init.yml"), []byte("#cloud-config\nruncmd: [echo "+planted+"]\n"), 0o600)
	e.writeStack(strings.Replace(stackExample, "    monitoring: true\n", "    monitoring: true\n    user_data_file: cloud-init.yml\n", 1))
	e.expect(exitUsage, "plan")
	contain(t, e.out.String(), "user data: ")
	contain(t, e.out.String(), "contents not shown")
	e.run("apply", "--yes", "--debug")
	notContain(t, e.out.String()+e.errb.String()+e.readManifest(), planted)
	sent := false
	for _, d := range e.fake.Droplets {
		sent = sent || strings.Contains(d.UserData, planted)
	}
	if !sent {
		t.Fatal("the user data was not sent")
	}
}

// plan only reads: whatever the situation, it sends no write and leaves the manifest alone.
func TestPlanNeverWritesAnything(t *testing.T) {
	e := stackEnv(t)
	e.applyAll()
	before := e.readManifest()
	e.fake.dbByName("shop-db").SizeSlug = "db-s-2vcpu-4gb"
	e.fake.Droplets = e.fake.Droplets[:1]
	e.fake.Log = nil
	for _, args := range [][]string{{"plan"}, {"plan", "--json"}, {"plan", "--prune", "--prune-data"}, {"apply", "--dry-run"}, {"apply", "--dry-run", "--prune", "--prune-data"}} {
		e.run(args...)
		if m := e.fake.Mutations(); len(m) != 0 {
			t.Fatalf("%v sent a write: %v", args, m)
		}
	}
	e.writeStack("version: 1\nname: shop\nregion: nyc3\n")
	for _, args := range [][]string{{"plan"}, {"plan", "--prune", "--prune-data"}} {
		e.run(args...)
		if m := e.fake.Mutations(); len(m) != 0 {
			t.Fatalf("%v sent a write: %v", args, m)
		}
	}
	if e.readManifest() != before {
		t.Fatal("plan changed the manifest")
	}
}

func TestStackHelpAndUsage(t *testing.T) {
	e := newEnv(t)
	e.mustRun("help")
	for _, w := range []string{"stack init|validate", "plan ", "apply ", "adopt droplet|database|domain|record", `for "docli plan", 2 means "there are changes"`} {
		contain(t, e.out.String(), w)
	}
	for topic, want := range map[string]string{"plan": "Exit code: 0 no changes, 1 error", "apply": "never retries a create", "adopt": "will not rename", "stack": "unknown keys"} {
		e.mustRun("help", topic)
		contain(t, e.out.String(), want)
	}
	e.mustRun("plan", "-h")
	contain(t, e.out.String(), "docli plan")
	e.expect(exitUsage, "stack")
	e.expect(exitUsage, "stack", "explode")
	e.expect(exitUsage, "apply", "extra")
	e.expect(exitUsage, "apply", "--confirm-prune", "prune")
	if len(e.fake.Log) != 0 {
		t.Fatal("usage errors must not call the API")
	}
}

// The README shows the same example docli stack init writes.
func TestReadmeExampleIsTheShippedExample(t *testing.T) {
	b, err := os.ReadFile("README.md")
	if err != nil {
		t.Fatal(err)
	}
	start := strings.Index(string(b), "```yaml\nversion: 1\nname: shop")
	if start < 0 {
		t.Fatal("the README has no Kamal example")
	}
	body := string(b)[start+len("```yaml\n"):]
	body = body[:strings.Index(body, "```")]
	want := stackExample[strings.Index(stackExample, "version: 1"):]
	if body != want {
		t.Fatalf("the README example differs from stackExample.\n--- README\n%s\n--- code\n%s", body, want)
	}
}

func TestApplyWaitsForADropletAndStopsIfItNeverBecomesActive(t *testing.T) {
	e := stackEnv(t)
	e.fake.StuckNew = true
	e.expect(exitError, "apply", "--yes", "--wait-timeout", "10s")
	contain(t, e.out.String(), `FAILED:`+"\n"+`  - wait for droplet "shop-web-1" to be active: `)
	contain(t, e.out.String(), "still \"new\" after 10s")
	contain(t, e.out.String(), `create database cluster "shop-db"`) // listed as not done
	if len(e.fake.Requests("POST /v2/databases")) != 0 {
		t.Fatal("later steps ran")
	}
	contain(t, e.readManifest(), "shop-web-1") // the droplet exists, so the manifest says so
}

// A new database is waited for only when a later step needs it.
func TestApplyWaitsForANewDatabaseOnlyWhenNeeded(t *testing.T) {
	e := stackEnv(t)
	s := strings.Replace(stackExample, "    users: [shop]\n    databases: [shop_production]\n", "", 1)
	e.writeStack(s)
	e.mustRun("apply", "--yes")
	notContain(t, e.out.String(), "wait for database cluster")
	contain(t, e.out.String(), `done: create database cluster "shop-db"`)
	if db := e.fake.dbByName("shop-db"); db == nil || db.Status != "creating" {
		t.Fatalf("%+v", db)
	}
}

// Ownership is checked again at the moment of deletion, not only when the plan was made.
func TestPruneStepsRecheckOwnershipAtDeletion(t *testing.T) {
	e := newEnv(t)
	r, err := e.app.start(Globals{})
	if err != nil {
		t.Fatal(err)
	}
	x := newApplyState(r, manifestOpts{Skip: true})
	if err := x.deleteDroplet(1001); err == nil || !strings.Contains(err.Error(), "no longer carries the tag") {
		t.Fatalf("%v", err)
	}
	if err := x.deleteDatabase("db-legacy"); err == nil || !strings.Contains(err.Error(), "no longer carries the tag") {
		t.Fatalf("%v", err)
	}
	if m := e.fake.Mutations(); len(m) != 0 {
		t.Fatalf("%v", m)
	}
}
