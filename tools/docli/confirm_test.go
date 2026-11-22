package main

import (
	"bytes"
	"errors"
	"strings"
	"testing"
)

func newTestRun(tty bool, stdin string, g Globals) (*Run, *bytes.Buffer, *bytes.Buffer) {
	out, errb := &bytes.Buffer{}, &bytes.Buffer{}
	a := &App{Stdin: strings.NewReader(stdin), Stdout: out, Stderr: errb, StdinIsTTY: tty}
	return &Run{App: a, G: g}, out, errb
}

func TestApproveTable(t *testing.T) {
	plan := Plan{Action: "x.create", Summary: "do a thing", Steps: []string{"step one"}}
	cases := []struct {
		name    string
		tty     bool
		stdin   string
		g       Globals
		wantOK  bool
		wantErr string // "" none, "refused" for exit 3
	}{
		{"tty, y", true, "y\n", Globals{}, true, ""},
		{"tty, yes", true, "YES\n", Globals{}, true, ""},
		{"tty, n", true, "n\n", Globals{}, false, "refused"},
		{"tty, just enter defaults to no", true, "\n", Globals{}, false, "refused"},
		{"tty, garbage is no", true, "sure\n", Globals{}, false, "refused"},
		{"tty, closed stdin is no", true, "", Globals{}, false, "refused"},
		{"no tty, no --yes refuses even if stdin says y", false, "y\n", Globals{}, false, "refused"},
		{"no tty, --yes", false, "", Globals{Yes: true}, true, ""},
		{"tty, --yes skips the question", true, "", Globals{Yes: true}, true, ""},
		{"dry run changes nothing", true, "y\n", Globals{DryRun: true}, false, ""},
		{"dry run beats --yes", false, "", Globals{DryRun: true, Yes: true}, false, ""},
	}
	for _, c := range cases {
		r, out, _ := newTestRun(c.tty, c.stdin, c.g)
		ok, err := r.Approve(plan, approveOpts{})
		if ok != c.wantOK {
			t.Errorf("%s: proceed=%v want %v", c.name, ok, c.wantOK)
		}
		var rf *refusedError
		switch {
		case c.wantErr == "refused" && !errors.As(err, &rf):
			t.Errorf("%s: want a refusal, got %v", c.name, err)
		case c.wantErr == "" && err != nil:
			t.Errorf("%s: unexpected error %v", c.name, err)
		}
		if !strings.Contains(out.String(), "do a thing") {
			t.Errorf("%s: the plan was not printed", c.name)
		}
		if c.g.DryRun && !strings.Contains(out.String(), "Dry run: nothing was changed") {
			t.Errorf("%s: dry run did not say so", c.name)
		}
	}
}

func TestApproveTypedName(t *testing.T) {
	plan := Plan{Action: "droplet.delete", Summary: "delete"}
	cases := []struct {
		name    string
		tty     bool
		stdin   string
		g       Globals
		confirm string
		wantOK  bool
	}{
		{"typed correctly", true, "web-1\n", Globals{}, "", true},
		{"typed wrong", true, "web-2\n", Globals{}, "", false},
		{"y is not the name", true, "y\n", Globals{}, "", false},
		{"--yes alone on a tty still asks for the name", true, "web-1\n", Globals{Yes: true}, "", true},
		{"--yes alone on a tty, wrong name", true, "nope\n", Globals{Yes: true}, "", false},
		{"--yes alone without a tty refuses", false, "", Globals{Yes: true}, "", false},
		{"--yes --confirm-name works unattended", false, "", Globals{Yes: true}, "web-1", true},
		{"--confirm-name wrong", false, "", Globals{Yes: true}, "web-2", false},
		{"--confirm-name without --yes and without tty", false, "", Globals{}, "web-1", false},
		{"no tty no flags", false, "web-1\n", Globals{}, "", false},
	}
	for _, c := range cases {
		r, _, _ := newTestRun(c.tty, c.stdin, c.g)
		ok, err := r.Approve(plan, approveOpts{TypeName: "web-1", ConfirmName: c.confirm})
		if ok != c.wantOK {
			t.Errorf("%s: proceed=%v want %v (err %v)", c.name, ok, c.wantOK, err)
		}
		if !ok && err == nil {
			t.Errorf("%s: declined without an error (exit code would be 0)", c.name)
		}
	}
}

func TestApproveJSONDryRun(t *testing.T) {
	r, out, errb := newTestRun(false, "", Globals{DryRun: true, JSON: true})
	ok, err := r.Approve(Plan{Action: "x", Summary: "s"}, approveOpts{})
	if ok || err != nil {
		t.Fatal(ok, err)
	}
	contain(t, out.String(), `"dry_run": true`)
	contain(t, out.String(), `"steps": []`)
	contain(t, out.String(), `"monthly_cost_usd": null`)
	notContain(t, out.String(), "Plan:")
	contain(t, errb.String(), "Plan: s")
}

func TestApproveDeleteOwnership(t *testing.T) {
	spec := deleteSpec{Kind: "firewall", Name: "legacy-fw", Managed: false, Rule: "a name starting with docli-"}
	// refused without --force-unmanaged, whatever else is given
	r, _, _ := newTestRun(true, "legacy-fw\n", Globals{Yes: true})
	ok, err := r.ApproveDelete(spec, false, "legacy-fw")
	var rf *refusedError
	if ok || !errors.As(err, &rf) || !strings.Contains(err.Error(), "did not create it") || !strings.Contains(err.Error(), "docli-") {
		t.Fatalf("ok=%v err=%v", ok, err)
	}
	// forced: the name must be typed (--yes alone is not enough)
	r, _, _ = newTestRun(false, "", Globals{Yes: true})
	if ok, err := r.ApproveDelete(spec, true, ""); ok || err == nil {
		t.Fatal("forced delete without the typed name must not proceed")
	}
	r, _, _ = newTestRun(true, "legacy-fw\n", Globals{})
	if ok, err := r.ApproveDelete(spec, true, ""); !ok || err != nil {
		t.Fatalf("typed name should allow: %v", err)
	}
	r, _, out := newTestRun(true, "legacy-fw\n", Globals{})
	r.ApproveDelete(spec, true, "")
	_ = out
	// managed things need only the normal confirmation
	managed := deleteSpec{Kind: "firewall", Name: "docli-fw", Managed: true, Rule: "x"}
	r, _, _ = newTestRun(true, "y\n", Globals{})
	if ok, err := r.ApproveDelete(managed, false, ""); !ok || err != nil {
		t.Fatalf("managed delete with y: %v", err)
	}
	r, _, _ = newTestRun(false, "", Globals{})
	if ok, _ := r.ApproveDelete(managed, false, ""); ok {
		t.Fatal("managed delete without tty and --yes must refuse")
	}
	// droplets always need the name
	d := deleteSpec{Kind: "droplet", Name: "web-1", Managed: true, Always: true, Rule: "x"}
	r, _, _ = newTestRun(true, "y\n", Globals{})
	if ok, _ := r.ApproveDelete(d, false, ""); ok {
		t.Fatal("y must not confirm a droplet delete")
	}
}
