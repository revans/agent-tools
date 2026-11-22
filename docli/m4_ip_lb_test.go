package main

import (
	"os"
	"strings"
	"testing"
)

// ---------- reserved IPs ----------

func TestReservedIPLifecycle(t *testing.T) {
	e := newEnvM4(t)
	e.mustRun("reserved-ip", "create", "--dry-run")
	contain(t, e.out.String(), "reserve an IP address in nyc3")
	contain(t, e.out.String(), "monthly cost: not shown")
	contain(t, e.out.String(), "the manifest")
	if len(e.fake.Mutations()) != 0 {
		t.Fatal("dry run sent a change")
	}
	e.env["DOCLI_REGION"] = ""
	e.expect(exitUsage, "reserved-ip", "create", "--yes")
	e.env["DOCLI_REGION"] = "nyc3"
	e.expect(exitRefused, "reserved-ip", "create")
	e.mustRun("reserved-ip", "create", "--yes", "--json")
	var ip reservedIPJSON
	jsonOut(t, e, &ip)
	if !ip.Managed || ip.Region != "nyc3" || ip.DropletID != 0 || ip.IP == "" {
		t.Fatalf("%+v", ip)
	}
	contain(t, e.readManifest(), "reserved_ips:")
	contain(t, e.readManifest(), "ip: "+ip.IP)
	e.mustRun("reserved-ip", "list", "--json")
	var list []reservedIPJSON
	jsonOut(t, e, &list)
	if len(list) != 2 || list[0].Managed || !list[1].Managed {
		t.Fatalf("only the one in the manifest is docli's: %+v", list)
	}
	e.mustRun("reserved-ip", "get", ip.IP)
	contain(t, e.out.String(), "nothing (unassigned)")
	e.expect(exitUsage, "reserved-ip", "get", "not-an-ip")
	e.expect(exitError, "reserved-ip", "get", "192.0.2.1")
	e.mustRun("reserved-ip", "delete", ip.IP, "--yes", "--confirm-name", ip.IP)
	notContain(t, e.readManifest(), ip.IP)
}

func TestReservedIPAssignShowsWhatItIsInUseBy(t *testing.T) {
	e := newEnvM4(t)
	e.mustRun("droplet", "create", "--name", "web-1", "--size", "s-1vcpu-1gb", "--image", "ubuntu-24-04-x64", "--yes", "--wait")
	e.mustRun("droplet", "create", "--name", "web-2", "--size", "s-1vcpu-1gb", "--image", "ubuntu-24-04-x64", "--yes", "--wait")
	e.mustRun("reserved-ip", "create", "--droplet", "web-1", "--yes", "--json")
	var ip reservedIPJSON
	jsonOut(t, e, &ip)
	if ip.Droplet != "web-1" || ip.Region != "nyc3" {
		t.Fatalf("%+v", ip)
	}
	// moving it: the plan names the droplet it is on now
	e.mustRun("reserved-ip", "assign", ip.IP, "web-2", "--dry-run")
	contain(t, e.out.String(), `currently assigned to droplet "web-1"`)
	contain(t, e.out.String(), "this MOVES it")
	contain(t, e.out.String(), "is in use by droplet")
	before := len(e.fake.Mutations())
	e.expect(exitError, "reserved-ip", "assign", ip.IP, "web-1", "--yes")
	contain(t, e.errb.String(), "already assigned")
	e.expect(exitRefused, "reserved-ip", "assign", ip.IP, "web-2") // no terminal
	if len(e.fake.Mutations()) != before {
		t.Fatal("a refused or no-op assign sent a change")
	}
	e.mustRun("reserved-ip", "assign", ip.IP, "web-2", "--yes", "--json")
	jsonOut(t, e, &ip)
	if ip.Droplet != "web-2" {
		t.Fatalf("%+v", ip)
	}
	contain(t, e.readManifest(), "droplet: web-2")
	// delete refuses an assigned address
	e.expect(exitRefused, "reserved-ip", "delete", ip.IP, "--yes", "--confirm-name", ip.IP)
	contain(t, e.errb.String(), `assigned to droplet "web-2"`)
	e.mustRun("reserved-ip", "unassign", ip.IP, "--dry-run")
	contain(t, e.out.String(), `currently assigned to droplet "web-2"`)
	e.mustRun("reserved-ip", "unassign", ip.IP, "--yes")
	e.expect(exitError, "reserved-ip", "unassign", ip.IP, "--yes")
	contain(t, e.errb.String(), "nothing to unassign")
	// region mismatch
	e.mustRun("droplet", "create", "--name", "sfo-1", "--size", "s-1vcpu-1gb", "--image", "ubuntu-24-04-x64", "--region", "sfo3", "--yes")
	e.expect(exitError, "reserved-ip", "assign", ip.IP, "sfo-1", "--yes")
	contain(t, e.errb.String(), "only be assigned within its region")
}

func TestReservedIPOwnershipIsTheManifest(t *testing.T) {
	e := newEnvM4(t)
	legacy := "198.51.100.77"
	for _, args := range [][]string{
		{"reserved-ip", "delete", legacy, "--yes", "--confirm-name", legacy},
		{"reserved-ip", "assign", legacy, "legacy-db", "--yes"},
	} {
		e.expect(exitRefused, args...)
		contain(t, e.errb.String(), "manifest")
	}
	if len(e.fake.Mutations()) != 0 {
		t.Fatal(e.fake.Mutations())
	}
	e.expect(exitRefused, "reserved-ip", "delete", legacy, "--yes", "--force-unmanaged") // address not typed
	// one docli made, but whose manifest entry is gone, is no longer docli's
	e.mustRun("reserved-ip", "create", "--yes", "--json")
	var ip reservedIPJSON
	jsonOut(t, e, &ip)
	os.Remove(e.manifest())
	e.expect(exitRefused, "reserved-ip", "delete", ip.IP, "--yes", "--confirm-name", ip.IP)
	// --no-manifest creates are never remembered, and say so
	e.mustRun("reserved-ip", "create", "--no-manifest", "--dry-run")
	contain(t, e.out.String(), "needs --force-unmanaged")
	e.mustRun("reserved-ip", "delete", legacy, "--yes", "--force-unmanaged", "--confirm-name", legacy)
	if len(e.fake.Requests("DELETE /v2/reserved_ips/"+legacy)) != 1 {
		t.Fatal(e.fake.Log)
	}
}

// ---------- load balancers ----------

var lbArgs = []string{"lb", "create", "--name", "web", "--forward", "http:80=http:8080", "--health-check", "http:8080/healthz", "--tag", "web"}

func TestLBParsers(t *testing.T) {
	for _, c := range []struct {
		spec, want string
	}{
		{"http:80=http:8080", "http:80=http:8080"},
		{"HTTPS:443=http:8080,cert=abc-123", "https:443=http:8080,cert=abc-123"},
		{"https:443=https:443,passthrough", "https:443=https:443,passthrough"},
		{"tcp:5432=tcp:5432", "tcp:5432=tcp:5432"},
	} {
		f, err := parseForward(c.spec)
		if err != nil || lbRuleSpec(f) != c.want {
			t.Errorf("%s: %v %q", c.spec, err, lbRuleSpec(f))
		}
	}
	for _, bad := range []string{"http:80", "http:0=http:80", "ftp:21=tcp:21", "https:443=http:80", "https:443=http:80,cert=a,passthrough", "http:80=http:80,color=red", "http:x=http:80", "http:80=smtp:25"} {
		if _, err := parseForward(bad); err == nil {
			t.Errorf("%q should be rejected", bad)
		}
	}
	h, err := parseHealth("http:8080/healthz,interval=20")
	if err != nil || lbHealthSpec(h) != "http:8080/healthz,interval=20,timeout=5,healthy=5,unhealthy=3" {
		t.Fatalf("%v %q", err, lbHealthSpec(h))
	}
	if h, _ := parseHealth("tcp:5432"); lbHealthSpec(h) != "tcp:5432,interval=10,timeout=5,healthy=5,unhealthy=3" {
		t.Fatal(lbHealthSpec(h))
	}
	for _, bad := range []string{"udp:53", "http:99999", "tcp:80/path", "http:80,interval=0", "http:80,speed=3", "http"} {
		if _, err := parseHealth(bad); err == nil {
			t.Errorf("%q should be rejected", bad)
		}
	}
}

func TestLBLifecycle(t *testing.T) {
	e := newEnvM4(t)
	e.mustRun(append(append([]string{}, lbArgs...), "--dry-run")...)
	contain(t, e.out.String(), `create the load balancer "docli-web" in nyc3`)
	contain(t, e.out.String(), "forward http:80=http:8080")
	contain(t, e.out.String(), "health check: http:8080/healthz")
	contain(t, e.out.String(), "every droplet tagged web")
	contain(t, e.out.String(), "monthly cost: not shown")
	if len(e.fake.Mutations()) != 0 {
		t.Fatal("dry run sent a change")
	}
	e.expect(exitRefused, lbArgs...)
	e.mustRun(append(append([]string{}, lbArgs...), "--yes", "--wait", "--json", "--vpc", "default-nyc3")...)
	var l lbJSON
	jsonOut(t, e, &l)
	if l.Name != "docli-web" || !l.Managed || l.Status != "active" || l.IP == "" || l.Tag != "web" || len(l.ForwardingRules) != 1 || l.HealthCheck == nil || l.VPCID != "vpc-default-nyc3" {
		t.Fatalf("%+v", l)
	}
	body := e.fake.Bodies["POST /v2/load_balancers"][0]
	contain(t, body, `"name":"docli-web"`)
	contain(t, body, `"entry_port":80`)
	contain(t, body, `"vpc_uuid":"vpc-default-nyc3"`)
	contain(t, body, `"tag":"web"`)
	notContain(t, body, `"droplet_ids"`)
	contain(t, e.readManifest(), "load_balancers:")
	contain(t, e.readManifest(), "- http:80=http:8080")
	e.expect(exitError, append(append([]string{}, lbArgs...), "--yes")...)
	contain(t, e.errb.String(), "already exists")
	e.mustRun("lb", "get", "web")
	contain(t, e.out.String(), "Cost:      not shown")
	contain(t, e.out.String(), "droplets tagged web")
	e.mustRun("lb", "list")
	contain(t, e.out.String(), "docli-web")
	e.expect(exitRefused, "lb", "delete", "web", "--yes") // typed name needed
	e.mustRun("lb", "delete", "web", "--yes", "--confirm-name", "docli-web")
	if len(e.fake.Requests("DELETE /v2/load_balancers/")) != 1 {
		t.Fatal(e.fake.Log)
	}
	notContain(t, e.readManifest(), "docli-web")
}

func TestLBTargetsAndNetworkChecks(t *testing.T) {
	e := newEnvM4(t)
	e.mustRun("droplet", "create", "--name", "web-1", "--size", "s-1vcpu-1gb", "--image", "ubuntu-24-04-x64", "--yes")
	e.mustRun("droplet", "create", "--name", "web-2", "--size", "s-1vcpu-1gb", "--image", "ubuntu-24-04-x64", "--yes")
	e.mustRun("droplet", "create", "--name", "far", "--size", "s-1vcpu-1gb", "--image", "ubuntu-24-04-x64", "--region", "sfo3", "--yes")
	e.mustRun("vpc", "create", "--name", "net", "--yes")
	base := []string{"lb", "create", "--name", "web", "--forward", "http:80=http:8080", "--yes"}
	// neither or both of droplets and tag
	e.expect(exitUsage, base...)
	e.expect(exitUsage, append(append([]string{}, base...), "--droplet", "web-1", "--tag", "web")...)
	// another region
	e.expect(exitError, append(append([]string{}, base...), "--droplet", "far")...)
	contain(t, e.errb.String(), "only front droplets in its own region")
	// the droplets are in the default VPC, not "net"
	e.expect(exitError, append(append([]string{}, base...), "--droplet", "web-1", "--vpc", "net")...)
	contain(t, e.errb.String(), "is not in VPC")
	// the health check must probe a port that is forwarded to
	e.expect(exitUsage, append(append([]string{}, base...), "--droplet", "web-1", "--health-check", "http:9999/")...)
	// by droplet ids, the VPC is taken from the droplets
	e.mustRun(append(append([]string{}, base...), "--droplet", "web-1", "--droplet", "web-2", "--json")...)
	var l lbJSON
	jsonOut(t, e, &l)
	if len(l.DropletIDs) != 2 || l.VPCID != "vpc-default-nyc3" || l.Tag != "" {
		t.Fatalf("%+v", l)
	}
	// bad rules are usage mistakes
	e.expect(exitUsage, "lb", "create", "--name", "x", "--forward", "https:443=http:80", "--tag", "t", "--yes")
	contain(t, e.errb.String(), "needs a certificate")
	e.expect(exitUsage, "lb", "create", "--name", "x", "--forward", "http:80=http:80", "--forward", "http:80=http:81", "--tag", "t", "--yes")
	e.expect(exitUsage, "lb", "create", "--name", "x", "--forward", "http:80=http:80", "--tag", "t", "--size", "lb-small", "--size-unit", "2", "--yes")
}

func TestLBOwnershipRefusal(t *testing.T) {
	e := newEnvM4(t)
	e.fake.m4.LBs = append(e.fake.m4.LBs, &fakeLB{})
	e.fake.m4.LBs[0].ID, e.fake.m4.LBs[0].Name, e.fake.m4.LBs[0].Status = "lb-legacy", "legacy-lb", "active"
	e.expect(exitRefused, "lb", "delete", "legacy-lb", "--yes", "--confirm-name", "legacy-lb")
	contain(t, e.errb.String(), "did not create it")
	contain(t, e.errb.String(), "docli-")
	e.expect(exitRefused, "lb", "delete", "legacy-lb", "--yes", "--force-unmanaged") // not typed
	if len(e.fake.Requests("DELETE")) != 0 {
		t.Fatal(e.fake.Log)
	}
	e.mustRun("lb", "delete", "legacy-lb", "--yes", "--force-unmanaged", "--confirm-name", "legacy-lb")
	if len(e.fake.Requests("DELETE /v2/load_balancers/lb-legacy")) != 1 {
		t.Fatal(e.fake.Log)
	}
	if strings.Contains(e.readManifestOrEmpty(), "legacy-lb") {
		t.Fatal("an LB docli did not create was recorded")
	}
}

func (e *testEnv) readManifestOrEmpty() string {
	b, _ := os.ReadFile(e.manifest())
	return string(b)
}
