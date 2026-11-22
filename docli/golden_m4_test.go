package main

import (
	"path/filepath"
	"strings"
	"testing"
)

// The --json output of the milestone 4 commands is a contract too.
func TestJSONGoldenM4(t *testing.T) {
	e := newEnvM4(t)
	e.fake.PageSize = 3
	dir := t.TempDir()
	sf := filepath.Join(dir, "key.secret")
	norm := func(s string) string {
		return strings.ReplaceAll(strings.ReplaceAll(s, dir, "/SECRET_DIR"), e.project, "/PROJECT")
	}
	for _, c := range []struct {
		file string
		args []string
	}{
		{"project_create_dry_run.json", []string{"project", "create", "--name", "shop", "--environment", "Production", "--dry-run", "--json"}},
		{"project_create.json", []string{"project", "create", "--name", "shop", "--environment", "Production", "--yes", "--json"}},
		{"project_list.json", []string{"project", "list", "--json"}},
		{"volume_create_dry_run.json", []string{"volume", "create", "--name", "data", "--size", "50", "--project", "shop", "--dry-run", "--json"}},
		{"volume_create.json", []string{"volume", "create", "--name", "data", "--size", "50", "--project", "shop", "--yes", "--json"}},
		{"volume_list.json", []string{"volume", "list", "--json"}},
		{"volume_get.json", []string{"volume", "get", "data", "--json"}},
		{"volume_resize.json", []string{"volume", "resize", "data", "--size", "80", "--yes", "--json"}},
		{"project_get.json", []string{"project", "get", "shop", "--json"}},
		{"project_assign.json", []string{"project", "assign", "docli-shop", "volume:data", "--yes", "--json"}},
		{"reserved_ip_create.json", []string{"reserved-ip", "create", "--yes", "--json"}},
		{"reserved_ip_list.json", []string{"reserved-ip", "list", "--json"}},
		{"lb_create_dry_run.json", []string{"lb", "create", "--name", "web", "--forward", "http:80=http:8080", "--health-check", "http:8080/healthz", "--tag", "web", "--dry-run", "--json"}},
		{"lb_create.json", []string{"lb", "create", "--name", "web", "--forward", "http:80=http:8080", "--health-check", "http:8080/healthz", "--tag", "web", "--yes", "--wait", "--json"}},
		{"lb_list.json", []string{"lb", "list", "--json"}},
		{"lb_get.json", []string{"lb", "get", "web", "--json"}},
		{"spaces_bucket_create.json", []string{"spaces", "bucket", "create", "--name", "assets", "--yes", "--json"}},
		{"spaces_bucket_list.json", []string{"spaces", "bucket", "list", "--json"}},
		{"spaces_key_create.json", []string{"spaces", "key", "create", "--name", "ci", "--grant", "docli-assets:read", "--secret-file", sf, "--yes", "--json"}},
		{"spaces_key_list.json", []string{"spaces", "key", "list", "--json"}},
		{"spaces_key_delete.json", []string{"spaces", "key", "delete", "ci", "--yes", "--confirm-name", "docli-ci", "--json"}},
		{"spaces_bucket_delete.json", []string{"spaces", "bucket", "delete", "assets", "--yes", "--confirm-name", "docli-assets", "--json"}},
		{"lb_delete.json", []string{"lb", "delete", "web", "--yes", "--confirm-name", "docli-web", "--json"}},
		{"volume_delete.json", []string{"volume", "delete", "data", "--yes", "--confirm-name", "data", "--json"}},
		{"manifest_verify_m4.json", []string{"manifest", "verify", "--json"}},
	} {
		code := e.run(c.args...)
		if code != 0 && c.file != "manifest_verify_m4.json" {
			t.Fatalf("docli %v exited %d\n%s\n%s", c.args, code, e.out.String(), e.errb.String())
		}
		golden(t, c.file, norm(e.out.String()))
	}
	e.expect(exitRefused, "volume", "delete", "legacy-data", "--yes", "--json")
	golden(t, "error_refused_volume.json", e.errb.String())
	e.expect(exitRefused, "project", "delete", "Production", "--yes", "--json")
	golden(t, "error_refused_project.json", e.errb.String())
}

// Every volume, load balancer and key has the same fields whatever its state, and lists are [] and never null.
func TestJSONFieldsAreStableM4(t *testing.T) {
	e := newEnvM4(t)
	e.fake.m4.Volumes, e.fake.m4.ReservedIPs, e.fake.m4.LBs, e.fake.m4.SpacesKeys = nil, nil, nil, nil
	e.fake.m4.Buckets = map[string]*fakeBucket{}
	for _, args := range [][]string{{"volume", "list"}, {"reserved-ip", "list"}, {"lb", "list"}, {"spaces", "key", "list"}, {"spaces", "bucket", "list"}} {
		e.mustRun(append(args, "--json")...)
		if e.out.String() != "[]\n" {
			t.Fatalf("%v: %q", args, e.out.String())
		}
	}
	e.mustRun("lb", "create", "--name", "w", "--forward", "tcp:80=tcp:80", "--tag", "t", "--yes", "--json")
	contain(t, e.out.String(), `"health_check": null`)
	contain(t, e.out.String(), `"droplet_ids": []`)
}
