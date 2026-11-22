package main

import (
	"encoding/json"
	"flag"
	"github.com/digitalocean/godo"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"testing"
)

var update = flag.Bool("update", false, "rewrite the golden files")

func golden(t *testing.T, name, got string) {
	t.Helper()
	path := filepath.Join("testdata", "golden", name)
	if *update {
		os.MkdirAll(filepath.Dir(path), 0o755)
		if err := os.WriteFile(path, []byte(got), 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("missing golden file %s (run: go test -update): %v", path, err)
	}
	if string(want) != got {
		t.Fatalf("%s changed. If the change is intended run: go test -update\n--- want\n%s\n--- got\n%s", name, want, got)
	}
}

// The --json output is a contract. These golden files pin every field of every command.
func TestJSONGolden(t *testing.T) {
	e := newEnv(t)
	e.fake.PageSize = 3
	for _, c := range []struct {
		file string
		args []string
	}{
		{"token_check.json", []string{"token", "check", "--json"}},
		{"sizes.json", []string{"sizes", "--all", "--json"}},
		{"regions.json", []string{"regions", "--all", "--json"}},
		{"images.json", []string{"images", "--json"}},
		{"ssh_key_list.json", []string{"ssh-key", "list", "--json"}},
		{"ssh_key_get.json", []string{"ssh-key", "get", "robert-laptop", "--json"}},
		{"vpc_list.json", []string{"vpc", "list", "--json"}},
		{"vpc_get.json", []string{"vpc", "get", "default-nyc3", "--json"}},
		{"firewall_list.json", []string{"firewall", "list", "--json"}},
		{"firewall_get.json", []string{"firewall", "get", "legacy-fw", "--json"}},
		{"droplet_list.json", []string{"droplet", "list", "--json"}},
		{"droplet_get.json", []string{"droplet", "get", "legacy-db", "--json"}},
		{"droplet_create_dry_run.json", withArgs("--stack", "blog", "--backups", "--dry-run", "--json")},
		{"firewall_create_dry_run.json", []string{"firewall", "create", "--name", "web", "--rule", "in:tcp:22:from=10.116.0.0/20", "--rule", "out:tcp:all:to=0.0.0.0/0", "--dry-run", "--json"}},
		{"droplet_create_wait.json", withArgs("--wait", "--yes", "--json")},
		{"droplet_delete.json", []string{"droplet", "delete", "web-1", "--yes", "--confirm-name", "web-1", "--json"}},
	} {
		e.mustRun(c.args...)
		golden(t, c.file, e.out.String())
	}
	// the error shape
	e.expect(exitRefused, "droplet", "delete", "legacy-db", "--yes", "--json")
	golden(t, "error_refused.json", e.errb.String())
}

// Every droplet, VPC, firewall and key has the same fields whatever its state.
func TestJSONFieldsAreStable(t *testing.T) {
	e := newEnv(t)
	e.fake.addDroplet(&godo.Droplet{ID: 2002, Name: "bare"}, "s-1vcpu-1gb", "nyc3", "", "")
	d := e.fake.droplet(2002)
	d.Networks, d.Image, d.Tags = nil, nil, nil
	e.mustRun("droplet", "list", "--json")
	var items []map[string]any
	jsonOut(t, e, &items)
	if len(items) != 2 {
		t.Fatal(len(items))
	}
	if !reflect.DeepEqual(keysOf(items[0]), keysOf(items[1])) {
		t.Fatalf("different fields:\n%v\n%v", keysOf(items[0]), keysOf(items[1]))
	}
	for _, v := range items[1] {
		if v == nil {
			t.Fatal("null leaked into droplet JSON; unknown values should be empty strings or empty lists")
		}
	}
	e.mustRun("firewall", "list", "--json")
	jsonOut(t, e, &items)
	for _, k := range []string{"rules", "droplet_ids", "tags"} {
		if _, ok := items[0][k].([]any); !ok {
			t.Fatalf("%s must always be a list", k)
		}
	}
	// empty lists print as [] and not null
	e.fake.Keys = nil
	e.mustRun("ssh-key", "list", "--json")
	if e.out.String() != "[]\n" {
		t.Fatalf("%q", e.out.String())
	}
}

func keysOf(m map[string]any) []string {
	var k []string
	for x := range m {
		k = append(k, x)
	}
	sort.Strings(k)
	return k
}

var _ = json.Marshal
