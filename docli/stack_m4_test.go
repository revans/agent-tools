package main

import (
	"strings"
	"testing"

	"github.com/digitalocean/godo"
)

// Stack file support for projects, volumes and load balancers (milestone 4). Reserved IPs and Spaces are not
// stack file kinds, and the file says so when it is asked to hold one.

const stackM4 = `version: 1
name: shop
region: nyc3
projects:
  - name: shop-prod
    environment: Production
vpcs:
  - name: shop-net
droplets:
  - name: web-1
    size: s-1vcpu-1gb
    image: ubuntu-24-04-x64
    vpc: shop-net
volumes:
  - name: shop-data
    size_gb: 50
    droplet: web-1
    project: shop-prod
load_balancers:
  - name: shop-lb
    forwarding_rules: ["http:80=http:8080"]
    health_check: "http:8080/"
    droplets: [web-1]
    project: shop-prod
`

func m4StackEnv(t *testing.T) *testEnv {
	t.Helper()
	e := newEnvM4(t)
	e.writeStack(stackM4)
	return e
}

func TestStackM4Validate(t *testing.T) {
	e := m4StackEnv(t)
	e.mustRun("stack", "validate")
	contain(t, e.out.String(), "also: 1 project(s), 1 volume(s), 1 load balancer(s)")
	e.mustRun("stack", "validate", "--json")
	contain(t, e.out.String(), `"load_balancers": 1`)
	for _, c := range []struct{ old, new, want string }{
		{"size_gb: 50", "size_gb: 0", "size_gb must be between 1 and 16000"},
		{"name: shop-data", "name: Shop_Data", "not a valid volume name"},
		{"droplet: web-1\n    project", "droplet: nope\n    project", `names the droplet "nope"`},
		{"environment: Production", "environment: Moon", "must be one of"},
		{`forwarding_rules: ["http:80=http:8080"]`, `forwarding_rules: ["https:443=http:8080"]`, "needs a certificate"},
		{`health_check: "http:8080/"`, `health_check: "http:9999/"`, "not a target port"},
		{"droplets: [web-1]\n    project", "project", "needs its targets"},
		{"droplets: [web-1]\n    project", "droplets: [web-1]\n    tag: web\n    project", "needs its targets"},
		{"droplets: [web-1]\n    project", "droplets: [nope]\n    project", `fronts the droplet "nope"`},
		{"    project: shop-prod\nload_balancers", "    project: shop-prod\n    password: x\nload_balancers", "looks like a secret"},
	} {
		e.writeStack(strings.Replace(stackM4, c.old, c.new, 1))
		e.expect(exitError, "stack", "validate")
		contain(t, e.errb.String(), c.want)
	}
	// kinds that never go in a stack file say why
	for key, why := range map[string]string{"reserved_ips": "has no name", "spaces_keys": "credentials", "spaces": "credentials", "spaces_buckets": "S3 API"} {
		e.writeStack(stackM4 + key + ":\n  - name: x\n")
		e.expect(exitError, "stack", "validate")
		contain(t, e.errb.String(), `"`+key+`" is not supported in a stack file`)
		contain(t, e.errb.String(), why)
	}
	// a project the file does not declare must exist in the account
	e.writeStack(strings.Replace(stackM4, "project: shop-prod\nload_balancers", "project: elsewhere\nload_balancers", 1))
	e.mustRun("stack", "validate")
	contain(t, e.out.String(), `uses the project "elsewhere"`)
	e.expect(exitError, "plan")
	contain(t, e.errb.String(), `no project named "elsewhere"`)
}

func TestStackM4PlanThenApplyInOrder(t *testing.T) {
	e := m4StackEnv(t)
	e.expect(exitUsage, "plan", "--json")
	golden(t, "plan_m4.json", e.out.String())
	doc := e.planDoc()
	var kinds []string
	for _, it := range doc.Items {
		if it.Action == actCreate {
			kinds = append(kinds, it.Kind)
		}
	}
	if got := strings.Join(kinds, ","); got != "project,vpc,droplet,volume,load_balancer" {
		t.Fatalf("plan order %s", got)
	}
	if !contains(doc.CostUnknownFor, "volume shop-data") || !contains(doc.CostUnknownFor, "load_balancer shop-lb") {
		t.Fatalf("the API reports no price, so none may be shown: %v", doc.CostUnknownFor)
	}
	contain(t, strings.Join(e.item(doc, "volume", "shop-data").Details, "\n"), "monthly cost is not shown")
	if len(e.fake.Mutations()) != 0 {
		t.Fatal("plan changed something")
	}
	e.mustRun("apply", "--yes")
	// fixed order: the project first, volumes and load balancers after the droplet
	var order []string
	for _, m := range e.fake.Mutations() {
		switch {
		case strings.HasPrefix(m, "POST /v2/projects") && !strings.Contains(m, "resources"):
			order = append(order, "project")
		case m == "POST /v2/vpcs":
			order = append(order, "vpc")
		case m == "POST /v2/droplets":
			order = append(order, "droplet")
		case m == "POST /v2/volumes":
			order = append(order, "volume")
		case strings.HasSuffix(m, "/actions") && strings.Contains(m, "/v2/volumes/"):
			order = append(order, "attach")
		case m == "POST /v2/load_balancers":
			order = append(order, "lb")
		}
	}
	if got := strings.Join(order, ","); got != "project,vpc,droplet,volume,attach,lb" {
		t.Fatalf("apply order %s", got)
	}
	vol := e.fake.m4.Volumes[len(e.fake.m4.Volumes)-1]
	if vol.Name != "shop-data" || !hasTag(vol.Tags, managedTag) || !hasTag(vol.Tags, "docli:stack:shop") || len(vol.DropletIDs) != 1 {
		t.Fatalf("%+v", vol)
	}
	lb := e.fake.m4.LBs[0]
	if lb.Name != "docli-shop-lb" || len(lb.DropletIDs) != 1 || lb.VPCUUID == "" || lb.VPCUUID == "vpc-default-nyc3" {
		t.Fatalf("%+v", lb.LoadBalancer)
	}
	proj := e.fake.m4.Projects[len(e.fake.m4.Projects)-1]
	if proj.Name != "docli-shop-prod" || len(e.fake.m4.ProjResources[proj.ID]) != 2 {
		t.Fatalf("%+v %v", proj, e.fake.m4.ProjResources)
	}
	m := e.readManifest()
	for _, want := range []string{"projects:", "volumes:", "load_balancers:", "name: docli-shop-prod", "name: shop-data", "name: docli-shop-lb"} {
		contain(t, m, want)
	}
	// run again: nothing to do
	e.fake.Log = nil
	e.mustRun("plan")
	contain(t, e.out.String(), "No changes")
	e.mustRun("apply", "--yes")
	contain(t, e.out.String(), "Nothing to do")
	if len(e.fake.Mutations()) != 0 {
		t.Fatal(e.fake.Mutations())
	}
}

func TestStackM4ChangesAndWhatIsNotDone(t *testing.T) {
	e := m4StackEnv(t)
	e.mustRun("apply", "--yes")
	// grow: done in place
	e.writeStack(strings.Replace(stackM4, "size_gb: 50", "size_gb: 80", 1))
	e.expect(exitUsage, "plan", "--json")
	doc := e.planDoc()
	if it := e.item(doc, "volume", "shop-data"); it.Action != actChange || !strings.Contains(strings.Join(it.Details, " "), "never be shrunk again") {
		t.Fatalf("%+v", it)
	}
	e.mustRun("apply", "--yes")
	vol := e.fake.m4.Volumes[len(e.fake.m4.Volumes)-1]
	if vol.SizeGigaBytes != 80 {
		t.Fatalf("not grown: %d", vol.SizeGigaBytes)
	}
	// shrink: reported, never done
	e.writeStack(strings.Replace(stackM4, "size_gb: 50", "size_gb: 20", 1))
	e.fake.Log = nil
	e.expect(exitUsage, "plan", "--json")
	if it := e.item(e.planDoc(), "volume", "shop-data"); it.Action != actReplace || !strings.Contains(strings.Join(it.Details, " "), "cannot be shrunk") {
		t.Fatalf("%+v", it)
	}
	e.mustRun("apply", "--yes")
	contain(t, e.out.String(), "Needs replacement, not done")
	if len(e.fake.Mutations()) != 0 || vol.SizeGigaBytes != 80 {
		t.Fatalf("a shrink was attempted: %v", e.fake.Mutations())
	}
	// a load balancer is never changed in place
	e.writeStack(strings.Replace(stackM4, `"http:80=http:8080"`, `"http:81=http:8080"`, 1))
	e.expect(exitUsage, "plan", "--json")
	it := e.item(e.planDoc(), "load_balancer", "shop-lb")
	if it.Action != actReplace || !strings.Contains(strings.Join(it.Details, " "), "no load balancer update yet") {
		t.Fatalf("%+v", it)
	}
	e.fake.Log = nil
	e.mustRun("apply", "--yes")
	if len(e.fake.Mutations()) != 0 {
		t.Fatal(e.fake.Mutations())
	}
	// a project's purpose is not changed either
	e.writeStack(strings.Replace(stackM4, "environment: Production", "environment: Staging", 1))
	e.expect(exitUsage, "plan", "--json")
	if it := e.item(e.planDoc(), "project", "shop-prod"); it.Action != actReplace {
		t.Fatalf("%+v", it)
	}
}

func TestStackM4LeavesWhatDocliDidNotCreateAlone(t *testing.T) {
	e := m4StackEnv(t)
	e.fake.m4.Volumes = append(e.fake.m4.Volumes, &godo.Volume{ID: "vol-theirs", Name: "shop-data", Region: &godo.Region{Slug: "nyc3"}, SizeGigaBytes: 10, Tags: []string{}, DropletIDs: []int{}})
	e.fake.m4.LBs = append(e.fake.m4.LBs, &fakeLB{LoadBalancer: godo.LoadBalancer{ID: "lb-theirs", Name: "shop-lb", Status: "active", Region: &godo.Region{Slug: "nyc3"}}})
	e.expect(exitUsage, "plan", "--json")
	doc := e.planDoc()
	if e.item(doc, "volume", "shop-data").Action != actUnmanaged || e.item(doc, "load_balancer", "shop-lb").Action != actUnmanaged {
		t.Fatalf("%+v", doc.Items)
	}
	contain(t, strings.Join(e.item(doc, "volume", "shop-data").Details, " "), "docli adopt volume shop-data")
	e.mustRun("apply", "--yes")
	if e.fake.m4.Volumes[len(e.fake.m4.Volumes)-1].SizeGigaBytes != 10 || len(e.fake.m4.LBs[0].ForwardingRules) != 0 {
		t.Fatal("something docli did not create was changed")
	}
	// the legacy volume, project and load balancer that the file does not mention are only reported
	e.writeStack("version: 1\nname: shop\nregion: nyc3\n")
	e.mustRun("plan", "--json") // the docli-made things from the apply above are orphans, which are not changes
	var names []string
	for _, n := range e.planDoc().NotManaged {
		names = append(names, n.Kind+" "+n.Name)
	}
	for _, want := range []string{"volume legacy-data", "project Production"} {
		if !contains(names, want) {
			t.Errorf("%q not reported as not docli's: %v", want, names)
		}
	}
	if contains(names, "project first-project") {
		t.Error("the default project is not something to report")
	}
}

func TestStackM4Prune(t *testing.T) {
	e := m4StackEnv(t)
	e.mustRun("apply", "--yes")
	e.writeStack("version: 1\nname: shop\nregion: nyc3\n")
	e.fake.Log = nil
	e.mustRun("plan", "--json") // orphans are reported, and are not changes
	doc := e.planDoc()
	for _, c := range [][2]string{{"project", "shop-prod"}, {"volume", "shop-data"}, {"load_balancer", "shop-lb"}} {
		if it := e.item(doc, c[0], c[1]); it.Action != actOrphaned {
			t.Fatalf("%+v", it)
		}
	}
	// nothing is deleted without --prune
	e.mustRun("apply", "--yes")
	contain(t, e.out.String(), "Orphaned, not deleted")
	if len(e.fake.Mutations()) != 0 {
		t.Fatal(e.fake.Mutations())
	}
	// the volume is attached to a droplet that stays: it is not pruned, and neither is the project that holds it
	e.mustRun("apply", "--prune", "--prune-data", "--yes", "--confirm-prune", "prune", "--prune-confirm-name", "shop-data", "--prune-confirm-name", "docli-shop-lb", "--prune-confirm-name", "web-1")
	out := e.out.String()
	contain(t, out, `delete load balancer "docli-shop-lb"`)
	contain(t, out, "not pruned: it is attached to web-1; detach it first")
	contain(t, out, "not pruned: it still holds")
	if len(e.fake.m4.LBs) != 0 || len(e.fake.m4.Volumes) != 2 {
		t.Fatalf("lbs=%d volumes=%d", len(e.fake.m4.LBs), len(e.fake.m4.Volumes))
	}
}

func TestStackM4PruneNeedsDataFlagTypedNameAndProjectOrder(t *testing.T) {
	e := m4StackEnv(t)
	e.mustRun("apply", "--yes")
	// detach the volume, empty the project of the droplet-free resources it holds by pruning them too
	e.mustRun("volume", "detach", "shop-data", "web-1", "--yes")
	e.writeStack("version: 1\nname: shop\nregion: nyc3\ndroplets:\n  - name: web-1\n    size: s-1vcpu-1gb\n    image: ubuntu-24-04-x64\n    vpc: shop-net\nvpcs:\n  - name: shop-net\n")
	e.fake.Log = nil
	// the volume holds data: --prune-data is needed
	e.mustRun("apply", "--prune", "--yes", "--confirm-prune", "prune", "--prune-confirm-name", "docli-shop-lb")
	contain(t, e.out.String(), "not pruned: it may hold data")
	if len(e.fake.m4.Volumes) != 2 {
		t.Fatal("pruned a volume without --prune-data")
	}
	// its name must be typed, even with --yes
	e.expect(exitRefused, "apply", "--prune", "--prune-data", "--yes", "--confirm-prune", "prune")
	contain(t, e.errb.String(), `needs its name "shop-data"`)
	e.fake.Log = nil
	e.mustRun("apply", "--prune", "--prune-data", "--yes", "--confirm-prune", "prune", "--prune-confirm-name", "shop-data")
	// the volume went first, then the project that held it
	var dels []string
	for _, m := range e.fake.Mutations() {
		if strings.HasPrefix(m, "DELETE") {
			dels = append(dels, strings.Split(m, "/")[2])
		}
	}
	if strings.Join(dels, ",") != "volumes,projects" {
		t.Fatalf("deleted %v (%v)", dels, e.fake.Mutations())
	}
	for _, d := range e.fake.m4.Volumes {
		if d.Name == "shop-data" {
			t.Fatal("not pruned")
		}
	}
	if e.fake.m4.Volumes[0].Name != "legacy-data" {
		t.Fatal("pruned something docli did not create")
	}
	e.mustRun("plan")
	contain(t, e.out.String(), "No changes")
}

func TestStackM4PruneStepsRecheckOwnershipAtDeletion(t *testing.T) {
	e := newEnvM4(t)
	r, err := e.app.start(Globals{})
	if err != nil {
		t.Fatal(err)
	}
	x := newApplyState(r, manifestOpts{Skip: true})
	if err := x.deleteVolume("vol-legacy"); err == nil || !strings.Contains(err.Error(), "no longer carries the tag") {
		t.Fatalf("%v", err)
	}
	e.fake.m4.LBs = append(e.fake.m4.LBs, &fakeLB{LoadBalancer: godo.LoadBalancer{ID: "lb-theirs", Name: "theirs"}})
	if err := x.deleteLB("lb-theirs"); err == nil || !strings.Contains(err.Error(), "no longer starts with docli-") {
		t.Fatalf("%v", err)
	}
	if err := x.deleteProject("proj-prod"); err == nil || !strings.Contains(err.Error(), "not a docli project") {
		t.Fatalf("%v", err)
	}
	e.mustRun("volume", "create", "--name", "mine", "--size", "5", "--yes")
	mine := e.fake.m4.Volumes[len(e.fake.m4.Volumes)-1]
	mine.DropletIDs = []int{1001}
	if err := x.deleteVolume(mine.ID); err == nil || !strings.Contains(err.Error(), "attached to a droplet") {
		t.Fatalf("%v", err)
	}
	e.mustRun("project", "create", "--name", "full", "--yes")
	full := e.fake.m4.Projects[len(e.fake.m4.Projects)-1]
	e.fake.m4.ProjResources[full.ID] = []string{"do:droplet:1001"}
	if err := x.deleteProject(full.ID); err == nil || !strings.Contains(err.Error(), "still holds") {
		t.Fatalf("%v", err)
	}
	for _, m := range e.fake.Mutations() {
		if strings.HasPrefix(m, "DELETE") {
			t.Fatal("a refused prune step sent a DELETE:", m)
		}
	}
}

func TestAdoptVolumeAndReservedIP(t *testing.T) {
	e := newEnvM4(t)
	e.mustRun("adopt", "volume", "legacy-data", "--stack", "shop", "--dry-run")
	contain(t, e.out.String(), "docli:managed")
	e.mustRun("adopt", "volume", "legacy-data", "--stack", "shop", "--yes", "--json")
	contain(t, e.out.String(), `"api_changed": true`)
	if v := e.fake.m4.Volumes[0]; !hasTag(v.Tags, managedTag) || !hasTag(v.Tags, "docli:stack:shop") {
		t.Fatalf("%v", v.Tags)
	}
	contain(t, e.readManifest(), "name: legacy-data")
	e.mustRun("volume", "resize", "legacy-data", "--size", "150", "--yes") // docli's now: no force needed
	// a reserved IP is adopted by the manifest alone
	before := len(e.fake.Mutations())
	e.mustRun("adopt", "reserved-ip", "198.51.100.77", "--yes")
	if len(e.fake.Mutations()) != before {
		t.Fatal("adopting a reserved IP changed something on DigitalOcean")
	}
	contain(t, e.readManifest(), "ip: 198.51.100.77")
	e.expect(exitError, "reserved-ip", "unassign", "198.51.100.77", "--yes") // owned now: fails only because nothing is assigned
	contain(t, e.errb.String(), "nothing to unassign")
	e.expect(exitUsage, "adopt", "reserved-ip", "198.51.100.77", "--no-manifest", "--yes")
	// these cannot be adopted: no tag to add, and docli will not rename them
	for _, kind := range []string{"project", "lb", "vpc"} {
		e.expect(exitRefused, "adopt", kind, "x", "--yes")
		contain(t, e.errb.String(), "does not adopt")
	}
}
