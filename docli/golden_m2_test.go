package main

import "testing"

// The --json output of the milestone 2 commands is a contract too.
func TestJSONGoldenM2(t *testing.T) {
	e := newEnv(t)
	e.fake.PageSize = 3
	e.env["DOCLI_REGION"] = "nyc3"
	e.fake.ExposeDBSizes = true
	for _, c := range []struct {
		file string
		args []string
	}{
		{"domain_create_dry_run.json", []string{"domain", "create", "--name", "shop.example", "--dry-run", "--json"}},
		{"domain_create.json", []string{"domain", "create", "--name", "shop.example", "--yes", "--json"}},
		{"domain_list.json", []string{"domain", "list", "--json"}},
		{"dns_create.json", []string{"dns", "create", "shop.example", "--type", "MX", "--name", "@", "--data", "mail.example.net.", "--priority", "10", "--yes", "--json"}},
		{"dns_create_caa.json", []string{"dns", "create", "shop.example", "--type", "CAA", "--name", "@", "--data", "letsencrypt.org", "--tag", "issue", "--yes", "--json"}},
		{"dns_list.json", []string{"dns", "list", "shop.example", "--json"}},
		{"domain_get.json", []string{"domain", "get", "shop.example", "--json"}},
		{"dns_update.json", []string{"dns", "update", "shop.example", "5005", "--ttl", "600", "--yes", "--json"}},
		{"dns_delete.json", []string{"dns", "delete", "shop.example", "5005", "--yes", "--json"}},
		{"db_create_dry_run.json", []string{"db", "create", "--name", "app-db", "--version", "16", "--size", "db-s-1vcpu-1gb", "--nodes", "2", "--vpc", "default-nyc3", "--trusted-droplet", "legacy-db", "--dry-run", "--json"}},
		{"db_create_wait.json", []string{"db", "create", "--name", "app-db", "--version", "16", "--size", "db-s-1vcpu-1gb", "--nodes", "1", "--vpc", "default-nyc3", "--trusted-droplet", "legacy-db", "--trusted-ip", "198.51.100.7", "--wait", "--yes", "--json"}},
		{"db_list.json", []string{"db", "list", "--json"}},
		{"db_get.json", []string{"db", "get", "app-db", "--json"}},
		{"db_connection.json", []string{"db", "connection", "app-db", "--json"}},
		{"db_user_create.json", []string{"db", "user", "create", "app-db", "rails", "--yes", "--json"}},
		{"db_user_list.json", []string{"db", "user", "list", "app-db", "--json"}},
		{"db_database_create.json", []string{"db", "database", "create", "app-db", "app_production", "--yes", "--json"}},
		{"db_database_list.json", []string{"db", "database", "list", "app-db", "--json"}},
		{"db_pool_create.json", []string{"db", "pool", "create", "app-db", "--name", "app_pool", "--database", "app_production", "--user", "rails", "--yes", "--json"}},
		{"db_pool_list.json", []string{"db", "pool", "list", "app-db", "--json"}},
		{"db_backups_list.json", []string{"db", "backups", "list", "app-db", "--json"}},
		{"db_firewall_list.json", []string{"db", "firewall", "list", "app-db", "--json"}},
		{"db_firewall_set_dry_run.json", []string{"db", "firewall", "set", "app-db", "--tag", "web", "--dry-run", "--json"}},
		{"db_resize.json", []string{"db", "resize", "app-db", "--size", "db-s-2vcpu-4gb", "--yes", "--json"}},
		{"db_delete.json", []string{"db", "delete", "app-db", "--yes", "--confirm-name", "app-db", "--json"}},
	} {
		e.mustRun(c.args...)
		golden(t, c.file, e.out.String())
	}
	e.expect(exitRefused, "domain", "delete", "legacy.example", "--yes", "--json")
	golden(t, "error_refused_domain.json", e.errb.String())
}

// Every domain, record and cluster has the same fields whatever its state, and lists are [] and never null.
func TestJSONFieldsAreStableM2(t *testing.T) {
	e := newEnv(t)
	e.fake.Domains = nil
	e.mustRun("domain", "list", "--json")
	if e.out.String() != "[]\n" {
		t.Fatalf("%q", e.out.String())
	}
	e.fake.DBs = nil
	e.mustRun("db", "list", "--json")
	if e.out.String() != "[]\n" {
		t.Fatalf("%q", e.out.String())
	}
	e = newEnv(t)
	bare := newFakeDB("bare", "db-bare", "nyc3", "16", nil)
	bare.Connection, bare.PrivateConnection, bare.Rules, bare.Users, bare.DBNames, bare.Backups = nil, nil, nil, nil, nil, nil
	e.fake.DBs = append(e.fake.DBs, bare)
	e.mustRun("db", "list", "--json")
	var items []map[string]any
	jsonOut(t, e, &items)
	if len(items) != 2 || !sameKeys(items[0], items[1]) {
		t.Fatalf("different fields:\n%v\n%v", keysOf(items[0]), keysOf(items[1]))
	}
	for _, k := range []string{"tags"} {
		if _, ok := items[1][k].([]any); !ok {
			t.Fatalf("%s must always be a list", k)
		}
	}
	for _, c := range [][]string{{"db", "user", "list", "bare"}, {"db", "database", "list", "bare"}, {"db", "pool", "list", "bare"}, {"db", "backups", "list", "bare"}, {"db", "firewall", "list", "bare"}} {
		e.mustRun(append(c, "--json")...)
		if e.out.String() != "[]\n" {
			t.Fatalf("%v: %q", c, e.out.String())
		}
	}
	e.mustRun("db", "get", "bare", "--json")
	var d map[string]any
	jsonOut(t, e, &d)
	if _, ok := d["trusted_sources"].([]any); !ok {
		t.Fatal("trusted_sources must be a list")
	}
}

func sameKeys(a, b map[string]any) bool {
	ka, kb := keysOf(a), keysOf(b)
	if len(ka) != len(kb) {
		return false
	}
	for i := range ka {
		if ka[i] != kb[i] {
			return false
		}
	}
	return true
}
