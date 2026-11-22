package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/digitalocean/godo"
)

// buildAll creates one of each milestone 4 kind with docli, so the manifest has every new section.
func buildAll(t *testing.T) *testEnv {
	t.Helper()
	e := newEnvM4(t)
	e.mustRun("project", "create", "--name", "shop", "--yes")
	e.mustRun("volume", "create", "--name", "data", "--size", "50", "--yes")
	e.mustRun("reserved-ip", "create", "--yes")
	e.mustRun("lb", "create", "--name", "web", "--forward", "http:80=http:8080", "--health-check", "http:8080/", "--tag", "web", "--yes", "--wait")
	e.mustRun("spaces", "bucket", "create", "--name", "assets", "--yes")
	e.mustRun("spaces", "key", "create", "--name", "ci", "--grant", "docli-assets:read", "--secret-file", filepath.Join(t.TempDir(), "s"), "--yes")
	return e
}

type verifyOut struct {
	InSync      bool       `json:"in_sync"`
	Differences []diffItem `json:"differences"`
}

func (e *testEnv) verify() verifyOut {
	e.t.Helper()
	e.run("manifest", "verify", "--json")
	var v verifyOut
	if err := json.Unmarshal(e.out.Bytes(), &v); err != nil {
		e.t.Fatalf("%v\n%s\n%s", err, e.out.String(), e.errb.String())
	}
	return v
}

func hasDiff(v verifyOut, kind, section, substr string) bool {
	for _, d := range v.Differences {
		if d.Kind == kind && d.Section == section && strings.Contains(d.Detail, substr) {
			return true
		}
	}
	return false
}

func TestManifestHasEveryM4Section(t *testing.T) {
	e := buildAll(t)
	m := e.readManifest()
	for _, want := range []string{"projects:", "volumes:", "reserved_ips:", "load_balancers:", "spaces_buckets:", "spaces_keys:",
		"name: docli-shop", "name: data", "name: docli-web", "forwarding_rules:", "id: nyc3/docli-assets", "name: docli-ci", "- docli-assets:read"} {
		contain(t, m, want)
	}
	notContain(t, m, fakeNewKeySecret)
	notContain(t, m, fakeSpacesSecret)
	if v := e.verify(); !v.InSync {
		t.Fatalf("%+v", v.Differences)
	}
	e.mustRun("manifest", "verify")
	contain(t, e.out.String(), "matches the account")
}

func TestManifestVerifyDetectsDriftInEveryM4Kind(t *testing.T) {
	e := buildAll(t)
	m := &e.fake.m4
	var ipAddr string
	for _, x := range m.ReservedIPs {
		if x.IP != "198.51.100.77" {
			ipAddr = x.IP
		}
	}
	// somebody changes things in the control panel
	for _, p := range m.Projects {
		if p.Name == "docli-shop" {
			p.Purpose = "Something else"
		}
	}
	m.Volumes[1].SizeGigaBytes = 500
	m.reservedIP(t, ipAddr).Droplet = &godo.Droplet{ID: 1001, Name: "legacy-db"}
	m.LBs[0].ForwardingRules = append(m.LBs[0].ForwardingRules, godo.ForwardingRule{EntryProtocol: "tcp", EntryPort: 22, TargetProtocol: "tcp", TargetPort: 22})
	m.SpacesKeys[1].Grants = []*godo.Grant{{Bucket: "docli-assets", Permission: godo.SpacesKeyReadWrite}}
	m.Buckets["docli-assets"].Created = "2020-01-01T00:00:00.000Z"
	v := e.verify()
	if v.InSync {
		t.Fatal("drift not detected")
	}
	for _, c := range [][2]string{{"projects", "purpose"}, {"volumes", "size_gb"}, {"reserved_ips", "droplet"}, {"load_balancers", "forwarding_rules"}, {"spaces_keys", "grants"}, {"spaces_buckets", "created_at"}} {
		if !hasDiff(v, "drift", c[0], c[1]) {
			t.Errorf("no drift on %s %s in %+v", c[0], c[1], v.Differences)
		}
	}
	e.expect(exitError, "manifest", "verify")
	contain(t, e.out.String(), "differs from the account")
	// refresh brings the file back in line
	e.mustRun("manifest", "refresh", "--yes")
	if v := e.verify(); !v.InSync {
		t.Fatalf("after refresh: %+v", v.Differences)
	}
}

func (m *fakeM4) reservedIP(t *testing.T, ip string) *godo.ReservedIP {
	for _, x := range m.ReservedIPs {
		if x.IP == ip {
			return x
		}
	}
	t.Fatal("no such reserved IP", ip)
	return nil
}

func TestManifestVerifyMissingAndUnmanagedM4(t *testing.T) {
	e := buildAll(t)
	m := &e.fake.m4
	// gone from the account
	m.Projects = m.Projects[:2]
	delete(m.Buckets, "docli-assets")
	m.SpacesKeys = m.SpacesKeys[:1]
	m.LBs = nil
	// no longer docli's: the tag was removed
	m.Volumes[1].Tags = []string{}
	// and one docli made that the file does not list
	m.Volumes = append(m.Volumes, &godo.Volume{ID: "vol-extra", Name: "extra", Region: &godo.Region{Slug: "nyc3"}, Tags: []string{managedTag}, DropletIDs: []int{}})
	m.Projects = append(m.Projects, &godo.Project{ID: "proj-extra", Name: "docli-extra"})
	v := e.verify()
	for _, c := range [][2]string{{"projects", "no longer exists"}, {"spaces_buckets", "no longer exists"}, {"spaces_keys", "no longer exists"}, {"load_balancers", "no longer exists"}, {"volumes", "no longer marked docli-managed"}} {
		if !hasDiff(v, "missing_in_account", c[0], c[1]) {
			t.Errorf("%v not reported: %+v", c, v.Differences)
		}
	}
	if !hasDiff(v, "missing_in_file", "volumes", "not in the file") || !hasDiff(v, "missing_in_file", "projects", "not in the file") {
		t.Errorf("%+v", v.Differences)
	}
	// a reserved IP released outside docli
	e.fake.m4.ReservedIPs = e.fake.m4.ReservedIPs[:1]
	if v := e.verify(); !hasDiff(v, "missing_in_account", "reserved_ips", "no longer exists") {
		t.Errorf("%+v", v.Differences)
	}
}

func TestManifestVerifyWithoutSpacesKeyOrWithDeniedReads(t *testing.T) {
	e := buildAll(t)
	delete(e.env, "DOCLI_SPACES_ACCESS_KEY")
	delete(e.env, "DOCLI_SPACES_SECRET_KEY")
	v := e.verify()
	if !hasDiff(v, "unverifiable", "spaces_buckets", "no Spaces access key") {
		t.Fatalf("%+v", v.Differences)
	}
	if len(v.Differences) != 1 {
		t.Fatalf("everything else should still be checked and in sync: %+v", v.Differences)
	}
	e.expect(exitError, "manifest", "verify")
	// a token that may not read volumes or load balancers: those entries are unverifiable, never "gone"
	e.fake.Denied["/v2/volumes"] = true
	e.fake.Denied["/v2/load_balancers"] = true
	v = e.verify()
	if !hasDiff(v, "unverifiable", "volumes", "cannot read volumes") || !hasDiff(v, "unverifiable", "load_balancers", "cannot read load balancers") {
		t.Fatalf("%+v", v.Differences)
	}
	for _, d := range v.Differences {
		if d.Kind == "missing_in_account" {
			t.Fatalf("a section the token cannot read was treated as gone: %+v", d)
		}
	}
	// refresh leaves sections it cannot read exactly as they were
	before := e.readManifest()
	e.mustRun("manifest", "refresh", "--yes")
	after := e.readManifest()
	for _, want := range []string{"name: data", "name: docli-web", "id: nyc3/docli-assets"} {
		if !strings.Contains(after, want) {
			t.Errorf("refresh dropped %q that it could not check.\nbefore:\n%s\nafter:\n%s", want, before, after)
		}
	}
}

func TestManifestRefreshNeverAddsReservedIPsOrBuckets(t *testing.T) {
	e := newEnvM4(t)
	e.mustRun("manifest", "refresh", "--yes")
	m := e.readManifestOrEmpty()
	notContain(t, m, "198.51.100.77")
	notContain(t, m, "legacy-assets")
	notContain(t, m, "legacy-data") // not tagged docli:managed
	notContain(t, m, "legacy-key")
	// a reserved IP the file lists is kept up to date, and dropped when the address is gone
	e.mustRun("reserved-ip", "create", "--yes")
	contain(t, e.readManifest(), "reserved_ips:")
	e.fake.m4.ReservedIPs = e.fake.m4.ReservedIPs[:1]
	e.mustRun("manifest", "refresh", "--yes")
	notContain(t, e.readManifest(), "reserved_ips:")
}

func TestManifestUnreadableOwnershipRefusesM4(t *testing.T) {
	e := newEnvM4(t)
	os.MkdirAll(filepath.Dir(e.manifest()), 0o755)
	os.WriteFile(e.manifest(), []byte("not: [valid"), 0o644)
	e.expect(exitError, "reserved-ip", "delete", "198.51.100.77", "--yes", "--confirm-name", "198.51.100.77")
	contain(t, e.errb.String(), "manifest is unreadable")
	if len(e.fake.Mutations()) != 0 {
		t.Fatal("docli acted although it could not tell what it created")
	}
}
