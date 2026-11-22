package main

import (
	"os"
	"strings"
	"testing"

	"github.com/revans/agent-tools/postlog"
)

func ledgerEntries(t *testing.T) []postlog.Entry {
	t.Helper()
	d, err := postlog.Open("").Read()
	if err != nil {
		t.Fatal(err)
	}
	return d.Entries
}

func TestPostIsRecorded(t *testing.T) {
	f := newFake(t)
	run := setup(t, f, "")
	img := writeImage(t, "pic.png", []byte("not really a png, but the tool checks the name and size"))
	if code, _, errs := run("post", "--text", "Hello (world) #go", "--image", img); code != 0 {
		t.Fatalf("%d %s", code, errs)
	}
	got := ledgerEntries(t)
	if len(got) != 1 {
		t.Fatalf("one entry: %+v", got)
	}
	p := got[0]
	if p.Platform != "linkedin" || p.Account != "main" || p.Kind != "post" || p.RemoteID != "urn:li:share:555" ||
		p.Source != "linkedin post" || p.URL != "https://www.linkedin.com/feed/update/urn:li:share:555/" {
		t.Errorf("entry: %+v", p)
	}
	if p.Text != "Hello (world) #go" {
		t.Errorf("the ledger holds the text as written, not LinkedIn's escaped form: %q", p.Text)
	}
	if len(p.Media) != 1 || p.Media[0] != img {
		t.Errorf("the image is recorded: %v", p.Media)
	}
}

func TestRepeatIsRefusedBeforeAnythingIsSent(t *testing.T) {
	f := newFake(t)
	run := setup(t, f, "")
	if code, _, errs := run("post", "--text", "Once only"); code != 0 {
		t.Fatalf("%d %s", code, errs)
	}
	sent := len(f.paths())
	code, _, errs := run("post", "--text", "  once ONLY ")
	if code != 1 || !strings.Contains(errs, "already published") || !strings.Contains(errs, "--allow-duplicate") {
		t.Errorf("a repeat is refused with the way out: %d %q", code, errs)
	}
	if len(f.paths()) != sent {
		t.Errorf("a refused repeat must send nothing: %v", f.paths())
	}
	if code, _, _ := run("post", "--text", "Once only", "--allow-duplicate"); code != 0 || len(ledgerEntries(t)) != 2 {
		t.Errorf("--allow-duplicate publishes and records again: %d", code)
	}
}

func TestNoLogAndDryRunRecordNothing(t *testing.T) {
	f := newFake(t)
	run := setup(t, f, "")
	if code, _, _ := run("post", "--text", "Quiet", "--no-log"); code != 0 {
		t.Fatal("--no-log still posts")
	}
	if len(ledgerEntries(t)) != 0 {
		t.Error("--no-log must record nothing")
	}
	n := len(f.paths())
	if code, out, _ := run("post", "--text", "Dry", "--dry-run"); code != 0 || !strings.Contains(out, `"dry_run":true`) {
		t.Fatalf("dry run: %d %s", code, out)
	}
	if len(ledgerEntries(t)) != 0 {
		t.Error("a dry run records nothing")
	}
	_ = n
	run("post", "--text", "Dry")
	code, _, errs := run("post", "--text", "Dry", "--dry-run")
	if code != 0 || !strings.Contains(errs, "warning:") {
		t.Errorf("a dry run of a repeat only warns: %d %q", code, errs)
	}
}

func TestFailedPostIsNotRecorded(t *testing.T) {
	f := newFake(t)
	run := setup(t, f, "")
	f.fail["/rest/posts"] = 403
	if code, _, _ := run("post", "--text", "Will fail"); code != 1 {
		t.Fatal("the post should fail")
	}
	if len(ledgerEntries(t)) != 0 {
		t.Error("a post that failed never went out and must not be recorded")
	}
	delete(f.fail, "/rest/posts")
	if code, _, errs := run("post", "--text", "Will fail"); code != 0 {
		t.Errorf("so the same text can be retried: %d %s", code, errs)
	}
}

func TestAnUnwritableLedgerNeverHidesASuccessfulPost(t *testing.T) {
	f := newFake(t)
	run := setup(t, f, "")
	blocker := t.TempDir() + "/file"
	os.WriteFile(blocker, []byte("x"), 0o600)
	t.Setenv(postlog.EnvPath, blocker+"/posts.jsonl")
	code, out, errs := run("post", "--text", "Went out")
	if code != 0 || !strings.Contains(out, "urn:li:share:555") {
		t.Fatalf("the post succeeds and reports its id: %d %s", code, out)
	}
	if !strings.Contains(errs, "was published") || !strings.Contains(errs, "urn:li:share:555") {
		t.Errorf("stderr says it went out and could not be logged: %q", errs)
	}
}
