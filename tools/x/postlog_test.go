package main

import (
	"strings"
	"testing"

	"github.com/revans/agent-tools/postlog"
)

func entries(t *testing.T, e *env) []postlog.Entry {
	t.Helper()
	d, err := postlog.Open(e.ledger).Read()
	if err != nil {
		t.Fatal(err)
	}
	return d.Entries
}

func TestPublishingIsRecorded(t *testing.T) {
	f := newFake(t)
	e := setup(t, f, "")
	e.ok("post", "--text", "Recorded post")
	got := entries(t, e)
	if len(got) != 1 {
		t.Fatalf("one entry: %+v", got)
	}
	p := got[0]
	if p.Platform != "x" || p.Account != "main" || p.Kind != "post" || p.RemoteID != "555" || p.Text != "Recorded post" ||
		p.Source != "x post" || p.URL != "https://x.com/i/status/555" || p.Target != "" {
		t.Errorf("entry: %+v", p)
	}
	e.ok("reply", "100", "--text", "A reply", "--image", writeImage(t, "pic.PNG", "d"))
	got = entries(t, e)
	if len(got) != 2 || got[1].Kind != "reply" || got[1].Target != "100" || len(got[1].Media) != 1 || got[1].Source != "x reply" {
		t.Errorf("a reply records its target and its image: %+v", got)
	}
}

func TestTheSameTextTwiceIsRefusedBeforeAnythingIsSent(t *testing.T) {
	f := newFake(t)
	e := setup(t, f, "")
	e.ok("post", "--text", "Only once, please")
	sent := f.count()
	e.fail(1, "already published", "post", "--text", "  only ONCE,   please ")
	e.fail(1, "--allow-duplicate", "post", "--text", "Only once, please")
	if f.count() != sent {
		t.Errorf("a refused repeat must send nothing: %v", f.paths())
	}
	if len(entries(t, e)) != 1 {
		t.Error("a refused repeat must not be recorded")
	}
	// the same words in reply to a different post are fine; to the same post they are not
	e.ok("reply", "100", "--text", "Thanks!")
	e.ok("reply", "101", "--text", "Thanks!")
	e.fail(1, "already published", "reply", "100", "--text", "Thanks!")
}

func TestAllowDuplicatePublishesAgain(t *testing.T) {
	f := newFake(t)
	e := setup(t, f, "")
	e.ok("post", "--text", "Again")
	e.ok("post", "--text", "Again", "--allow-duplicate")
	if len(entries(t, e)) != 2 {
		t.Error("both are recorded")
	}
}

func TestNoLogRecordsNothingButStillChecksForRepeats(t *testing.T) {
	f := newFake(t)
	e := setup(t, f, "")
	e.ok("post", "--text", "Off the record", "--no-log")
	if len(entries(t, e)) != 0 {
		t.Error("--no-log must record nothing")
	}
	e.ok("post", "--text", "On the record")
	e.fail(1, "already published", "post", "--text", "On the record", "--no-log")
}

func TestDryRunNeverRecordsAndOnlyWarnsAboutARepeat(t *testing.T) {
	f := newFake(t)
	e := setup(t, f, "")
	if _, out, _ := e.run("post", "--text", "Dry", "--dry-run"); !strings.Contains(out, `"dry_run":true`) {
		t.Errorf("dry run output: %s", out)
	}
	if len(entries(t, e)) != 0 || f.count() != 0 {
		t.Error("a dry run records nothing and sends nothing")
	}
	e.ok("post", "--text", "Dry")
	sent := f.count()
	code, _, errs := e.run("post", "--text", "Dry", "--dry-run")
	if code != 0 || !strings.Contains(errs, "warning:") || !strings.Contains(errs, "--allow-duplicate") {
		t.Errorf("a dry run of a repeat warns on stderr and still succeeds: %d %q", code, errs)
	}
	if f.count() != sent {
		t.Error("and still sends nothing")
	}
}

func TestAFailedPublishIsNotRecorded(t *testing.T) {
	f := newFake(t)
	e := setup(t, f, "")
	e.fail(1, "HTTP 403", "post", "--text", "forbidden")
	e.fail(1, "rate limited", "post", "--text", "rate")
	if len(entries(t, e)) != 0 {
		t.Errorf("a post that failed never went out, so it must not be in the ledger: %+v", entries(t, e))
	}
	// and so retrying the same text is allowed
	e.fail(1, "HTTP 403", "post", "--text", "forbidden")
}

func TestAnUnwritableLedgerNeverHidesASuccessfulPost(t *testing.T) {
	f := newFake(t)
	e := setup(t, f, "")
	t.Setenv(postlog.EnvPath, e.cfg+"/not-a-dir/posts.jsonl") // the parent is a regular file
	code, out, errs := e.run("post", "--text", "Went out anyway")
	if code != 0 || !strings.Contains(out, `"id":"555"`) {
		t.Fatalf("the post must succeed and report its id: %d %s", code, out)
	}
	if !strings.Contains(errs, "was published") || !strings.Contains(errs, "555") {
		t.Errorf("stderr must say the post went out and could not be logged: %q", errs)
	}
}

func TestTheLedgerCanBeSwitchedOff(t *testing.T) {
	f := newFake(t)
	e := setup(t, f, "")
	t.Setenv(postlog.EnvPath, "off")
	e.ok("post", "--text", "Unlogged")
	e.ok("post", "--text", "Unlogged") // and so no duplicate check either
}
