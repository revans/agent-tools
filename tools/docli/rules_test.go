package main

import (
	"strings"
	"testing"
)

func TestParseRule(t *testing.T) {
	good := []struct{ in, spec, text string }{
		{"in:tcp:22:from=10.116.0.0/20", "in:tcp:22:from=10.116.0.0/20", "Allow incoming TCP on port 22 from 10.116.0.0/20"},
		{"in:tcp:5432:from-tag=app", "in:tcp:5432:from-tag=app", "Allow incoming TCP on port 5432 from droplets tagged app"},
		{"out:tcp:all:to=0.0.0.0/0", "out:tcp:all:to=0.0.0.0/0", "Allow outgoing TCP on all ports to anywhere (0.0.0.0/0)"},
		{"in:udp:8000-9000:from=1.2.3.4", "in:udp:8000-9000:from=1.2.3.4", "Allow incoming UDP on ports 8000-9000 from 1.2.3.4"},
		{"in:icmp:all:from=::/0", "in:icmp:all:from=::/0", "Allow incoming ICMP (ping) from anywhere (::/0)"},
		{"in:tcp:443:from=10.0.0.0/8,192.168.0.0/16", "in:tcp:443:from=10.0.0.0/8,192.168.0.0/16", "Allow incoming TCP on port 443 from 10.0.0.0/8, 192.168.0.0/16"},
		{"in:tcp:80:from=2001:db8::/32+from-tag=lb", "in:tcp:80:from=2001:db8::/32+from-tag=lb", "Allow incoming TCP on port 80 from 2001:db8::/32, droplets tagged lb"},
		{"in:tcp:80:from-droplet=42", "in:tcp:80:from-droplet=42", "Allow incoming TCP on port 80 from droplet 42"},
		{"in:tcp:80-80:from=10.0.0.1", "in:tcp:80:from=10.0.0.1", "Allow incoming TCP on port 80 from 10.0.0.1"},
	}
	for _, c := range good {
		r, err := ParseRule(c.in)
		if err != nil {
			t.Errorf("%q: %v", c.in, err)
			continue
		}
		if c.text == "" {
			continue
		}
		if r.Spec() != c.spec {
			t.Errorf("%q: spec %q, want %q", c.in, r.Spec(), c.spec)
		}
		if r.Describe() != c.text {
			t.Errorf("%q: text %q, want %q", c.in, r.Describe(), c.text)
		}
		if again, err := ParseRule(r.Spec()); err != nil || !again.Equal(r) {
			t.Errorf("%q does not round-trip: %v", c.in, err)
		}
	}
}

func TestParseRuleRejects(t *testing.T) {
	bad := map[string]string{
		"":                               "four parts",
		"in:tcp:22":                      "four parts",
		"sideways:tcp:22:from=1.2.3.4":   "direction",
		"in:http:22:from=1.2.3.4":        "protocol",
		"in:tcp:0:from=1.2.3.4":          "ports",
		"in:tcp:70000:from=1.2.3.4":      "ports",
		"in:tcp:abc:from=1.2.3.4":        "ports",
		"in:tcp:900-800:from=1.2.3.4":    "backwards",
		"in:tcp:22-:from=1.2.3.4":        "range",
		"in:icmp:22:from=1.2.3.4":        "icmp has no ports",
		"in:tcp:22:":                     "no target",
		"in:tcp:22:to=1.2.3.4":           "not a target for in rules",
		"out:tcp:22:from=1.2.3.4":        "not a target for out rules",
		"in:tcp:22:from=300.1.1.1":       "valid IP",
		"in:tcp:22:from=10.0.0.0/33":     "valid CIDR",
		"in:tcp:22:from=":                "must look like",
		"in:tcp:22:from":                 "must look like",
		"in:tcp:22:from-tag=bad tag":     "not a valid tag",
		"in:tcp:22:from-droplet=web":     "must be a number",
		"in:tcp:22:from-droplet=-3":      "must be a number",
		"in:tcp:22:from=1.2.3.4+bogus=1": "not a target",
		"in:tcp:22:from=1.2.3.4+x=1":     "not a target",
	}
	for in, want := range bad {
		_, err := ParseRule(in)
		if err == nil {
			t.Errorf("%q should be rejected", in)
			continue
		}
		if !strings.Contains(err.Error(), want) {
			t.Errorf("%q: error %q lacks %q", in, err, want)
		}
		if !strings.Contains(err.Error(), "Rule format:") {
			t.Errorf("%q: error should show the format help", in)
		}
	}
}

func TestRuleEqualIgnoresOrder(t *testing.T) {
	a, _ := ParseRule("in:tcp:22:from=10.0.0.0/8,192.168.0.0/16")
	b, _ := ParseRule("in:tcp:22:from=192.168.0.0/16,10.0.0.0/8")
	if !a.Equal(b) {
		t.Fatal("address order should not matter")
	}
}

func TestApiPortsAll(t *testing.T) {
	if apiPortsToRule("0") != "all" || apiPortsToRule("") != "all" || apiPortsToRule("22") != "22" {
		t.Fatal("API all-ports forms are not normalized")
	}
}
