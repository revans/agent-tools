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

func TestPostPhotoAndCommentAreRecorded(t *testing.T) {
	f := newFake(t)
	run := setup(t, f, "")

	code, out, errs := run("post", "--message", "A post", "--link", "https://example.com/a")
	if code != 0 {
		t.Fatalf("%d %s", code, errs)
	}
	postID, _ := decode(t, out)["id"].(string)
	if code, _, errs = run("photo", "--image-url", "https://cdn.example.com/p.jpg", "--caption", "A photo"); code != 0 {
		t.Fatalf("%d %s", code, errs)
	}
	if code, _, errs = run("photo", "--image-url", "https://cdn.example.com/bare.jpg"); code != 0 {
		t.Fatalf("%d %s", code, errs)
	}
	code, out, errs = run("comment", "--post-id", "555_9001", "--message", "Nice one")
	if code != 0 {
		t.Fatalf("%d %s", code, errs)
	}
	commentID, _ := decode(t, out)["id"].(string)

	got := ledgerEntries(t)
	if len(got) != 4 {
		t.Fatalf("four entries: %+v", got)
	}
	p, ph, bare, c := got[0], got[1], got[2], got[3]
	if p.Platform != "facebook" || p.Account != "main" || p.Kind != "post" || p.RemoteID != postID || p.Text != "A post" ||
		p.Source != "facebook post" || len(p.Media) != 1 || p.Media[0] != "https://example.com/a" || p.Target != "" {
		t.Errorf("post entry: %+v", p)
	}
	if ph.Kind != "post" || ph.RemoteID != "555_7002" || ph.Text != "A photo" || ph.Source != "facebook photo" || len(ph.Media) != 1 || ph.Media[0] != "https://cdn.example.com/p.jpg" {
		t.Errorf("photo entry: %+v", ph)
	}
	if bare.Text != "https://cdn.example.com/bare.jpg" {
		t.Errorf("a captionless photo is keyed by its image URL: %+v", bare)
	}
	if c.Kind != "comment" || c.Target != "555_9001" || c.RemoteID != commentID || c.Text != "Nice one" || c.Source != "facebook comment" {
		t.Errorf("comment entry: %+v", c)
	}
}

func TestRepeatsAreRefusedButOtherTargetsAreFine(t *testing.T) {
	f := newFake(t)
	run := setup(t, f, "")
	if code, _, errs := run("post", "--message", "Only once"); code != 0 {
		t.Fatalf("%d %s", code, errs)
	}
	n := len(f.paths())
	code, _, errs := run("post", "--message", "ONLY  once")
	if code != 1 || !strings.Contains(errs, "already published") || !strings.Contains(errs, "--allow-duplicate") {
		t.Errorf("a repeat post is refused: %d %q", code, errs)
	}
	if len(f.paths()) != n {
		t.Errorf("a refused repeat sends nothing: %v", f.paths())
	}
	if code, _, _ := run("post", "--message", "Only once", "--allow-duplicate"); code != 0 {
		t.Error("--allow-duplicate publishes again")
	}

	// a photo without a caption repeats by its image
	if code, _, _ := run("photo", "--image-url", "https://x.test/a.jpg"); code != 0 {
		t.Fatal("first photo")
	}
	if code, _, _ := run("photo", "--image-url", "https://x.test/b.jpg"); code != 0 {
		t.Error("another image without a caption is not a repeat")
	}
	if code, _, errs := run("photo", "--image-url", "https://x.test/a.jpg"); code != 1 || !strings.Contains(errs, "already published") {
		t.Errorf("the same bare image twice is a repeat: %d %q", code, errs)
	}

	// a comment: the same words on another post are fine, on the same post they are not
	if code, _, _ := run("comment", "--post-id", "555_1", "--message", "Love it"); code != 0 {
		t.Fatal("first comment")
	}
	if code, _, _ := run("comment", "--post-id", "555_2", "--message", "Love it"); code != 0 {
		t.Error("the same comment on a different post is fine")
	}
	if code, _, errs := run("comment", "--post-id", "555_1", "--message", "Love it"); code != 1 || !strings.Contains(errs, "already published") {
		t.Errorf("the same comment on the same post is a repeat: %d %q", code, errs)
	}
	if code, _, _ := run("post", "--message", "Love it"); code != 0 {
		t.Error("a post is a different kind from a comment, so the same words are fine")
	}
}

func TestNoLogDryRunAndFailuresRecordNothing(t *testing.T) {
	f := newFake(t)
	run := setup(t, f, "")
	if code, _, _ := run("post", "--message", "Quiet", "--no-log"); code != 0 {
		t.Fatal("--no-log still posts")
	}
	if code, out, _ := run("post", "--message", "Dry", "--dry-run"); code != 0 || !strings.Contains(out, `"dry_run":true`) {
		t.Fatalf("dry run: %d %s", code, out)
	}
	if code, _, _ := run("comment", "--post-id", "555_1", "--message", "Dry too", "--dry-run"); code != 0 {
		t.Fatal("dry-run comment")
	}
	if len(ledgerEntries(t)) != 0 {
		t.Errorf("--no-log and --dry-run record nothing: %+v", ledgerEntries(t))
	}
	f.postErr = `{"error":{"message":"Invalid parameter","code":100}}`
	if code, _, _ := run("post", "--message", "Fails"); code != 1 {
		t.Fatal("the post should fail")
	}
	if len(ledgerEntries(t)) != 0 {
		t.Error("a post that failed never went out and must not be recorded")
	}
	// a dry run of a repeat only warns
	f.postErr = ""
	run("post", "--message", "Seen")
	code, _, errs := run("post", "--message", "Seen", "--dry-run")
	if code != 0 || !strings.Contains(errs, "warning:") {
		t.Errorf("a dry run of a repeat warns and succeeds: %d %q", code, errs)
	}
}

func TestAnUnwritableLedgerNeverHidesASuccessfulPublish(t *testing.T) {
	f := newFake(t)
	run := setup(t, f, "")
	blocker := t.TempDir() + "/file"
	os.WriteFile(blocker, []byte("x"), 0o600)
	t.Setenv(postlog.EnvPath, blocker+"/posts.jsonl")
	code, out, errs := run("post", "--message", "Went out")
	id, _ := decode(t, out)["id"].(string)
	if code != 0 || id == "" {
		t.Fatalf("the post succeeds and reports its id: %d %s", code, out)
	}
	if !strings.Contains(errs, "was published") || !strings.Contains(errs, id) {
		t.Errorf("stderr says it went out and could not be logged: %q", errs)
	}
}
