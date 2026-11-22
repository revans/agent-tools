package main

import (
	"strings"
	"testing"

	"github.com/digitalocean/godo"
)

// ---------- projects ----------

func TestProjectLifecycle(t *testing.T) {
	e := newEnvM4(t)
	e.mustRun("project", "create", "--name", "shop", "--environment", "Production", "--dry-run")
	contain(t, e.out.String(), `create the project "docli-shop"`)
	contain(t, e.out.String(), "prefix docli-")
	if len(e.fake.Mutations()) != 0 {
		t.Fatal("dry run sent a change")
	}
	e.expect(exitRefused, "project", "create", "--name", "shop") // no terminal, no --yes
	e.expect(exitUsage, "project", "create", "--name", "shop", "--environment", "Moon", "--yes")
	e.expect(exitUsage, "project", "create", "--yes")
	e.mustRun("project", "create", "--name", "shop", "--environment", "Production", "--yes", "--json")
	var p projectJSON
	jsonOut(t, e, &p)
	if p.Name != "docli-shop" || !p.Managed || p.Environment != "Production" || p.Purpose != "Web Application" {
		t.Fatalf("%+v", p)
	}
	contain(t, e.fake.Bodies["POST /v2/projects"][0], `"name":"docli-shop"`)
	contain(t, e.readManifest(), "projects:")
	contain(t, e.readManifest(), "name: docli-shop")
	e.expect(exitError, "project", "create", "--name", "shop", "--yes")
	contain(t, e.errb.String(), "already exists")
	e.mustRun("project", "get", "shop")
	contain(t, e.out.String(), "Resources:   0")
	e.mustRun("project", "list")
	contain(t, e.out.String(), "docli-shop")
	e.mustRun("project", "delete", "shop", "--yes")
	if len(e.fake.Requests("DELETE /v2/projects/")) != 1 {
		t.Fatal(e.fake.Log)
	}
	notContain(t, e.readManifest(), "docli-shop")
}

func TestProjectDeleteRefusals(t *testing.T) {
	e := newEnvM4(t)
	e.mustRun("project", "create", "--name", "full", "--yes")
	id := e.fake.m4.Projects[len(e.fake.m4.Projects)-1].ID
	e.fake.m4.ProjResources[id] = []string{"do:droplet:1001", "do:volume:vol-legacy"}
	// holds resources: refused, nothing sent, even with --force-unmanaged
	e.expect(exitRefused, "project", "delete", "docli-full", "--yes")
	contain(t, e.errb.String(), "still holds 2 resource(s)")
	contain(t, e.errb.String(), "do:droplet:1001")
	e.expect(exitRefused, "project", "delete", "docli-full", "--yes", "--force-unmanaged", "--confirm-name", "docli-full")
	// the default project and projects docli did not create
	e.expect(exitRefused, "project", "delete", "first-project", "--yes", "--force-unmanaged", "--confirm-name", "first-project")
	contain(t, e.errb.String(), "default project")
	e.expect(exitRefused, "project", "delete", "Production", "--yes")
	contain(t, e.errb.String(), "did not create it")
	e.expect(exitRefused, "project", "delete", "Production", "--yes", "--force-unmanaged") // name not typed
	if len(e.fake.Requests("DELETE")) != 0 {
		t.Fatal(e.fake.Log)
	}
	// an empty project docli did not create: force plus the typed name
	e.mustRun("project", "delete", "Production", "--yes", "--force-unmanaged", "--confirm-name", "Production")
	if len(e.fake.Requests("DELETE /v2/projects/proj-prod")) != 1 {
		t.Fatal(e.fake.Log)
	}
}

func TestProjectAssignMovesAndSaysWhere(t *testing.T) {
	e := newEnvM4(t)
	e.mustRun("project", "create", "--name", "shop", "--yes")
	e.mustRun("volume", "create", "--name", "data", "--size", "10", "--yes")
	// the unmanaged droplet is refused unless forced, and forcing needs the project name typed
	e.expect(exitRefused, "project", "assign", "docli-shop", "droplet:legacy-db", "--yes")
	contain(t, e.errb.String(), "docli did not create it")
	e.expect(exitRefused, "project", "assign", "docli-shop", "droplet:legacy-db", "--yes", "--force-unmanaged")
	e.mustRun("project", "assign", "docli-shop", "droplet:legacy-db", "volume:data", "--dry-run", "--force-unmanaged")
	contain(t, e.out.String(), `moves from the project "first-project" to "docli-shop"`)
	contain(t, e.out.String(), "NOT created by docli")
	e.expect(exitUsage, "project", "assign", "docli-shop", "legacy-db", "--yes")
	e.expect(exitUsage, "project", "assign", "docli-shop", "widget:x", "--yes")
	for _, d := range e.fake.Mutations() {
		if strings.Contains(d, "resources") {
			t.Fatal("a refused or dry run assign sent a change:", d)
		}
	}
	e.mustRun("project", "assign", "docli-shop", "droplet:legacy-db", "volume:data", "--yes", "--force-unmanaged", "--confirm-name", "docli-shop", "--json")
	var out projectAssignJSON
	jsonOut(t, e, &out)
	if !out.Assigned || len(out.Resources) != 2 || out.ProjectName != "docli-shop" {
		t.Fatalf("%+v", out)
	}
	if e.fake.inProject("do:droplet:1001") != out.ProjectID || e.fake.inProject("do:volume:"+e.fake.m4.Volumes[1].ID) != out.ProjectID {
		t.Fatalf("%+v", e.fake.m4.ProjResources)
	}
}

func TestProjectFlagOnCreates(t *testing.T) {
	e := newEnvM4(t)
	e.mustRun("project", "create", "--name", "shop", "--yes")
	// a typo in --project is caught before anything is created
	e.expect(exitError, "volume", "create", "--name", "data", "--size", "10", "--project", "nope", "--yes")
	contain(t, e.errb.String(), `no project named "nope"`)
	for _, m := range e.fake.Mutations() {
		if !strings.Contains(m, "/projects") {
			t.Fatal("a create ran although the project does not exist:", m)
		}
	}
	e.mustRun("volume", "create", "--name", "data", "--size", "10", "--project", "shop", "--dry-run")
	contain(t, e.out.String(), `put it in the project "docli-shop"`)
	e.mustRun("volume", "create", "--name", "data", "--size", "10", "--project", "shop", "--yes")
	if e.fake.inProject("do:volume:"+e.fake.m4.Volumes[1].ID) == "" {
		t.Fatal("the volume was not put in the project")
	}
	// the project step failing leaves the resource and says how to finish
	e.fake.Fails["POST /v2/projects/"+e.fake.m4.Projects[2].ID+"/resources"] = []fakeFail{{Status: 500, Message: "boom"}}
	e.expect(exitError, "volume", "create", "--name", "data2", "--size", "10", "--project", "shop", "--yes")
	contain(t, e.errb.String(), `volume "data2" was created, but it could not be put in the project "docli-shop"`)
	contain(t, e.errb.String(), "docli project assign docli-shop volume:data2")
	// every other create takes --project too
	e.mustRun("domain", "create", "--name", "proj.example", "--project", "shop", "--yes")
	e.mustRun("firewall", "create", "--name", "fw", "--rule", "in:tcp:22:from=10.0.0.0/8", "--project", "shop", "--yes")
	e.mustRun("droplet", "create", "--name", "web-1", "--size", "s-1vcpu-1gb", "--image", "ubuntu-24-04-x64", "--project", "shop", "--yes")
	e.mustRun("db", "create", "--name", "pdb", "--version", "16", "--size", "db-s-1vcpu-1gb", "--nodes", "1", "--vpc", "default-nyc3", "--trusted-ip", "1.2.3.4", "--project", "shop", "--yes")
	n := 0
	for _, u := range e.fake.m4.ProjResources[e.fake.m4.Projects[2].ID] {
		if strings.HasPrefix(u, "do:domain:") || strings.HasPrefix(u, "do:firewall:") || strings.HasPrefix(u, "do:droplet:") || strings.HasPrefix(u, "do:dbaas:") {
			n++
		}
	}
	if n != 4 {
		t.Fatalf("%v", e.fake.m4.ProjResources)
	}
}

// ---------- volumes ----------

func TestVolumeLifecycle(t *testing.T) {
	e := newEnvM4(t)
	e.mustRun("volume", "create", "--name", "data", "--size", "50", "--dry-run")
	contain(t, e.out.String(), `create the 50 GB volume "data" in nyc3`)
	contain(t, e.out.String(), "docli:managed")
	contain(t, e.out.String(), "monthly cost: not shown")
	if len(e.fake.Mutations()) != 0 {
		t.Fatal("dry run sent a change")
	}
	e.expect(exitUsage, "volume", "create", "--name", "Data_1", "--size", "50", "--yes")
	e.expect(exitUsage, "volume", "create", "--name", "data", "--size", "0", "--yes")
	e.expect(exitUsage, "volume", "create", "--name", "data", "--size", "50", "--filesystem-type", "ntfs", "--yes")
	e.expect(exitRefused, "volume", "create", "--name", "data", "--size", "50")
	e.mustRun("volume", "create", "--name", "data", "--size", "50", "--stack", "shop", "--tag", "db", "--yes", "--json")
	var v volumeJSON
	jsonOut(t, e, &v)
	if v.Name != "data" || !v.Managed || v.SizeGB != 50 || v.Region != "nyc3" || !contains(v.Tags, "docli:stack:shop") || !contains(v.Tags, "db") {
		t.Fatalf("%+v", v)
	}
	if !e.fake.Tags["docli:managed"] || !e.fake.Tags["docli:stack:shop"] {
		t.Fatalf("tags were not created first: %v", e.fake.Tags)
	}
	contain(t, e.readManifest(), "volumes:")
	contain(t, e.readManifest(), "size_gb: 50")
	e.expect(exitError, "volume", "create", "--name", "data", "--size", "50", "--yes")
	contain(t, e.errb.String(), "already exists in nyc3")
	e.mustRun("volume", "get", "data")
	contain(t, e.out.String(), "Managed:     yes")
	e.mustRun("volume", "list", "--region", "nyc3")
	contain(t, e.out.String(), "legacy-data")
	e.mustRun("volume", "delete", "data", "--yes", "--confirm-name", "data")
	if len(e.fake.Requests("DELETE /v2/volumes/")) != 1 {
		t.Fatal(e.fake.Log)
	}
	notContain(t, e.readManifest(), "name: data")
}

func TestVolumeDeleteNeedsTypedNameAndRefusesAttached(t *testing.T) {
	e := newEnvM4(t)
	e.mustRun("volume", "create", "--name", "data", "--size", "50", "--yes")
	e.expect(exitRefused, "volume", "delete", "data", "--yes") // --yes alone is not enough
	contain(t, e.errb.String(), `needs the name "data" typed`)
	e.expect(exitRefused, "volume", "delete", "data", "--yes", "--confirm-name", "other")
	e.tty(true)
	e.stdin("nope\n")
	e.expect(exitRefused, "volume", "delete", "data")
	contain(t, e.errb.String(), "did not match")
	if len(e.fake.Requests("DELETE")) != 0 {
		t.Fatal(e.fake.Log)
	}
	e.tty(false)
	e.fake.m4.Volumes[1].DropletIDs = []int{1001}
	e.expect(exitRefused, "volume", "delete", "data", "--yes", "--confirm-name", "data")
	contain(t, e.errb.String(), "attached to legacy-db")
	if len(e.fake.Requests("DELETE")) != 0 {
		t.Fatal(e.fake.Log)
	}
	e.fake.m4.Volumes[1].DropletIDs = nil
	e.tty(true)
	e.stdin("data\n")
	e.mustRun("volume", "delete", "data")
	if len(e.fake.Requests("DELETE /v2/volumes/")) != 1 {
		t.Fatal(e.fake.Log)
	}
}

func TestVolumeOwnershipRefusal(t *testing.T) {
	e := newEnvM4(t)
	for _, args := range [][]string{
		{"volume", "delete", "legacy-data", "--yes", "--confirm-name", "legacy-data"},
		{"volume", "attach", "legacy-data", "legacy-db", "--yes"},
		{"volume", "resize", "legacy-data", "--size", "200", "--yes"},
	} {
		e.expect(exitRefused, args...)
		contain(t, e.errb.String(), "did not create it")
	}
	if len(e.fake.Mutations()) != 0 {
		t.Fatal(e.fake.Mutations())
	}
	e.expect(exitRefused, "volume", "resize", "legacy-data", "--size", "200", "--yes", "--force-unmanaged") // name not typed
	e.mustRun("volume", "resize", "legacy-data", "--size", "200", "--yes", "--force-unmanaged", "--confirm-name", "legacy-data")
	if e.fake.m4.Volumes[0].SizeGigaBytes != 200 {
		t.Fatal("not resized")
	}
}

func TestVolumeAttachDetachResize(t *testing.T) {
	e := newEnvM4(t)
	e.mustRun("volume", "create", "--name", "data", "--size", "50", "--yes")
	e.mustRun("droplet", "create", "--name", "web-1", "--size", "s-1vcpu-1gb", "--image", "ubuntu-24-04-x64", "--yes")
	e.mustRun("droplet", "create", "--name", "web-2", "--size", "s-1vcpu-1gb", "--image", "ubuntu-24-04-x64", "--region", "sfo3", "--yes")
	// wrong region
	e.expect(exitError, "volume", "attach", "data", "web-2", "--yes")
	contain(t, e.errb.String(), "only be attached within its region")
	e.expect(exitError, "volume", "detach", "data", "web-1", "--yes")
	contain(t, e.errb.String(), "is not attached")
	e.mustRun("volume", "attach", "data", "web-1", "--dry-run")
	contain(t, e.out.String(), `attach volume "data" to droplet "web-1"`)
	e.mustRun("volume", "attach", "data", "web-1", "--yes", "--json")
	var v volumeJSON
	jsonOut(t, e, &v)
	if len(v.DropletIDs) != 1 {
		t.Fatalf("%+v", v)
	}
	contain(t, e.readManifest(), "- web-1")
	e.expect(exitError, "volume", "attach", "data", "web-1", "--yes")
	contain(t, e.errb.String(), "already attached")
	e.expect(exitRefused, "volume", "attach", "data", "legacy-db", "--yes")
	contain(t, e.errb.String(), "one droplet at a time")
	// resize only grows
	e.expect(exitRefused, "volume", "resize", "data", "--size", "10", "--yes")
	contain(t, e.errb.String(), "can only grow")
	e.expect(exitError, "volume", "resize", "data", "--size", "50", "--yes")
	contain(t, e.errb.String(), "already 50 GB")
	for _, m := range e.fake.Requests("POST /v2/volumes/") {
		if strings.Contains(e.fake.Bodies[strings.Fields(m)[0]+" "+strings.Fields(m)[1]][0], "resize") {
			t.Fatal("a refused resize was sent")
		}
	}
	e.mustRun("volume", "resize", "data", "--size", "80", "--dry-run")
	contain(t, e.out.String(), "can never be made smaller")
	e.mustRun("volume", "resize", "data", "--size", "80", "--yes")
	contain(t, e.fake.m4.Volumes[1].Name, "data")
	if e.fake.m4.Volumes[1].SizeGigaBytes != 80 {
		t.Fatal("not resized")
	}
	contain(t, e.readManifest(), "size_gb: 80")
	e.mustRun("volume", "detach", "data", "web-1", "--dry-run")
	contain(t, e.out.String(), "unmount the volume")
	e.mustRun("volume", "detach", "data", "web-1", "--yes")
	if len(e.fake.m4.Volumes[1].DropletIDs) != 0 {
		t.Fatal("not detached")
	}
}

func TestVolumeAmbiguousNameNeedsRegion(t *testing.T) {
	e := newEnvM4(t)
	e.fake.m4.Volumes = append(e.fake.m4.Volumes, &godo.Volume{ID: "vol-sfo", Name: "legacy-data", Region: &godo.Region{Slug: "sfo3"}, SizeGigaBytes: 10, Tags: []string{}, DropletIDs: []int{}})
	e.env["DOCLI_REGION"] = ""
	e.expect(exitError, "volume", "get", "legacy-data")
	contain(t, e.errb.String(), "unique only within a region")
	e.mustRun("volume", "get", "legacy-data", "--region", "sfo3")
	contain(t, e.out.String(), "vol-sfo")
}
