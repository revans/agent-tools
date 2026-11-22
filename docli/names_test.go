package main

import (
	"testing"

	"github.com/digitalocean/godo"
)

func TestValidateHostname(t *testing.T) {
	good := []string{"web", "web-1", "a", "web.example.com", "A1", "x" + string(make([]byte, 0)), "1web"}
	for _, n := range good {
		if err := validateHostname(n); err != nil {
			t.Errorf("%q should be valid: %v", n, err)
		}
	}
	long := ""
	for i := 0; i < 64; i++ {
		long += "a"
	}
	bad := []string{"", "-web", "web-", "we b", "web_1", "web.", ".web", "web..com", "wëb", long, "web/1", "web:1"}
	for _, n := range bad {
		if err := validateHostname(n); err == nil {
			t.Errorf("%q should be invalid", n)
		}
	}
}

func TestValidateTag(t *testing.T) {
	for _, n := range []string{"app", "docli:managed", "docli:stack:blog", "a_b-c"} {
		if validateTag(n) != nil {
			t.Errorf("%q should be valid", n)
		}
	}
	for _, n := range []string{"", "has space", "slash/ed", "ünï"} {
		if validateTag(n) == nil {
			t.Errorf("%q should be invalid", n)
		}
	}
}

func TestEstimateCost(t *testing.T) {
	s := godo.Size{Slug: "s-1vcpu-1gb", PriceMonthly: 6, PriceHourly: 0.00893}
	c := estimateCost(s, false)
	if c.Total() != 6 || c.BackupsMonthly != 0 || c.Hourly != 0.00893 {
		t.Fatalf("%+v", c)
	}
	c = estimateCost(godo.Size{PriceMonthly: 18}, true)
	if c.BackupsMonthly != 3.6 || c.Total() != 21.6 {
		t.Fatalf("%+v total %v", c, c.Total())
	}
	c = estimateCost(godo.Size{PriceMonthly: 4}, true)
	if c.Total() != 4.8 {
		t.Fatalf("total %v", c.Total())
	}
}

const testPub = "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIOMqqnkVzrm0SdG6UOoqKLsabgH5C9okWi0dh2l9GKJl robert@laptop"

func TestParsePublicKey(t *testing.T) {
	line, typ, fp, err := parsePublicKey("  " + testPub + "\n")
	if err != nil || typ != "ssh-ed25519" || line != testPub {
		t.Fatalf("%q %q %v", line, typ, err)
	}
	if len(fp) != 47 || fp[2] != ':' {
		t.Fatalf("fingerprint %q is not MD5 colon-hex", fp)
	}
	for _, bad := range []string{"", "hello", "ssh-ed25519", "ssh-foo AAAAAAAAAAAA", "ssh-ed25519 !!!notbase64", "-----BEGIN OPENSSH PRIVATE KEY-----\nabc\n-----END OPENSSH PRIVATE KEY-----"} {
		if _, _, _, err := parsePublicKey(bad); err == nil {
			t.Errorf("%q should be rejected", bad)
		}
	}
}

func TestEnsureNamePrefix(t *testing.T) {
	if ensureNamePrefix("net") != "docli-net" || ensureNamePrefix("docli-net") != "docli-net" {
		t.Fatal("prefix handling wrong")
	}
}
