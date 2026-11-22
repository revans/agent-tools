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

func TestPostCommentAndReplyAreRecorded(t *testing.T) {
	f := newFake(t)
	fakeSleep(t)
	run := setup(t, f, "")

	code, out, errs := run("post", "--caption", "A caption", "--image-url", goodURL)
	if code != 0 {
		t.Fatalf("%d %s", code, errs)
	}
	postID, _ := decode(t, out)["id"].(string)
	code, out, errs = run("comment", "--media-id", "17900000000000001", "--message", "Nice one")
	if code != 0 {
		t.Fatalf("%d %s", code, errs)
	}
	commentID, _ := decode(t, out)["id"].(string)
	if code, _, errs = run("reply", "--comment-id", "17900000000000002", "--message", "Thanks"); code != 0 {
		t.Fatalf("%d %s", code, errs)
	}

	got := ledgerEntries(t)
	if len(got) != 3 {
		t.Fatalf("three entries: %+v", got)
	}
	p, c, r := got[0], got[1], got[2]
	if p.Platform != "instagram" || p.Account != "main" || p.Kind != "post" || p.RemoteID != postID || p.Text != "A caption" ||
		p.Source != "instagram post" || len(p.Media) != 1 || p.Media[0] != goodURL || p.Target != "" {
		t.Errorf("post entry: %+v", p)
	}
	if c.Kind != "comment" || c.Target != "17900000000000001" || c.RemoteID != commentID || c.Text != "Nice one" || c.Source != "instagram comment" {
		t.Errorf("comment entry: %+v", c)
	}
	if r.Kind != "reply" || r.Target != "17900000000000002" || r.Text != "Thanks" || r.Source != "instagram reply" || r.RemoteID == "" {
		t.Errorf("reply entry: %+v", r)
	}
}

func TestRepeatsAreRefusedButOtherTargetsAreFine(t *testing.T) {
	f := newFake(t)
	fakeSleep(t)
	run := setup(t, f, "")
	if code, _, errs := run("post", "--caption", "Only once", "--image-url", goodURL); code != 0 {
		t.Fatalf("%d %s", code, errs)
	}
	n := len(f.paths())
	code, _, errs := run("post", "--caption", "ONLY  once", "--image-url", goodURL)
	if code != 1 || !strings.Contains(errs, "already published") || !strings.Contains(errs, "--allow-duplicate") {
		t.Errorf("a repeat post is refused: %d %q", code, errs)
	}
	if len(f.paths()) != n {
		t.Errorf("a refused repeat sends nothing: %v", f.paths())
	}
	if code, _, _ := run("post", "--caption", "Only once", "--image-url", goodURL, "--allow-duplicate"); code != 0 {
		t.Error("--allow-duplicate publishes again")
	}

	// a comment: the same words on another post are fine, on the same post they are not
	if code, _, _ := run("comment", "--media-id", "17900000000000001", "--message", "Love it"); code != 0 {
		t.Fatal("first comment")
	}
	if code, _, _ := run("comment", "--media-id", "17900000000000009", "--message", "Love it"); code != 0 {
		t.Error("the same comment on a different post is fine")
	}
	if code, _, errs := run("comment", "--media-id", "17900000000000001", "--message", "Love it"); code != 1 || !strings.Contains(errs, "already published") {
		t.Errorf("the same comment on the same post is a repeat: %d %q", code, errs)
	}
	if code, _, _ := run("reply", "--comment-id", "17900000000000003", "--message", "Love it"); code != 0 {
		t.Error("a reply is a different kind from a comment, so the same words are fine")
	}
	if code, _, errs := run("reply", "--comment-id", "17900000000000003", "--message", "Love it"); code != 1 || !strings.Contains(errs, "already published") {
		t.Errorf("the same reply to the same comment is a repeat: %d %q", code, errs)
	}
}

func TestNoLogDryRunAndFailuresRecordNothing(t *testing.T) {
	f := newFake(t)
	fakeSleep(t)
	run := setup(t, f, "")
	if code, _, _ := run("comment", "--media-id", "17900000000000001", "--message", "Quiet", "--no-log"); code != 0 {
		t.Fatal("--no-log still comments")
	}
	if code, out, _ := run("post", "--caption", "Dry", "--image-url", goodURL, "--dry-run"); code != 0 || !strings.Contains(out, `"dry_run":true`) {
		t.Fatalf("dry run: %d %s", code, out)
	}
	if code, _, _ := run("comment", "--media-id", "17900000000000001", "--message", "Dry too", "--dry-run"); code != 0 {
		t.Fatal("dry-run comment")
	}
	if len(ledgerEntries(t)) != 0 {
		t.Errorf("--no-log and --dry-run record nothing: %+v", ledgerEntries(t))
	}
	f.commentErr = `{"error":{"message":"Invalid parameter","code":100}}`
	if code, _, _ := run("comment", "--media-id", "17900000000000001", "--message", "Fails"); code != 1 {
		t.Fatal("the comment should fail")
	}
	if len(ledgerEntries(t)) != 0 {
		t.Error("a comment that failed never went out and must not be recorded")
	}
	// a dry run of a repeat only warns
	f.commentErr = ""
	run("comment", "--media-id", "17900000000000001", "--message", "Seen")
	code, _, errs := run("comment", "--media-id", "17900000000000001", "--message", "Seen", "--dry-run")
	if code != 0 || !strings.Contains(errs, "warning:") {
		t.Errorf("a dry run of a repeat warns and succeeds: %d %q", code, errs)
	}
}

func TestAnUnwritableLedgerNeverHidesASuccessfulPublish(t *testing.T) {
	f := newFake(t)
	fakeSleep(t)
	run := setup(t, f, "")
	blocker := t.TempDir() + "/file"
	os.WriteFile(blocker, []byte("x"), 0o600)
	t.Setenv(postlog.EnvPath, blocker+"/posts.jsonl")
	code, out, errs := run("comment", "--media-id", "17900000000000001", "--message", "Went out")
	id, _ := decode(t, out)["id"].(string)
	if code != 0 || id == "" {
		t.Fatalf("the comment succeeds and reports its id: %d %s", code, out)
	}
	if !strings.Contains(errs, "was published") || !strings.Contains(errs, id) {
		t.Errorf("stderr says it went out and could not be logged: %q", errs)
	}
}

func TestMetricsReadsTheCountsAndTheInsights(t *testing.T) {
	f := newFake(t)
	run := setup(t, f, "")
	code, out, errs := run("metrics", "17900000000000001")
	if code != 0 {
		t.Fatalf("%d %s", code, errs)
	}
	m := decode(t, out)
	for k, want := range map[string]float64{"like_count": 12, "comments_count": 3, "views": 80, "reach": 60, "saved": 3, "shares": 1} {
		if got, _ := m[k].(float64); got != want {
			t.Errorf("%s = %v, want %v (%v)", k, m[k], want, m)
		}
	}
	if m["media_type"] != "IMAGE" || m["permalink"] != "https://www.instagram.com/p/ABC/" || m["insights_error"] != nil {
		t.Errorf("type and link come along, with no error: %v", m)
	}
	var sawFields, sawInsights bool
	for _, r := range f.reqs {
		if r.Method != "GET" {
			continue
		}
		noToken(t, r)
		if strings.HasSuffix(r.Path, "/insights") {
			sawInsights = strings.Contains(r.RawQuery, "metric=views%2Creach%2Csaved%2Cshares")
		} else if strings.Contains(r.Path, "1790") {
			sawFields = strings.Contains(r.RawQuery, "fields=like_count%2Ccomments_count%2Cmedia_type%2Cpermalink")
		}
	}
	if !sawFields || !sawInsights {
		t.Errorf("the requests: fields=%v insights=%v %v", sawFields, sawInsights, f.paths())
	}
}

func TestMetricsKeepsTheCountsWhenInsightsAreRefused(t *testing.T) {
	f := newFake(t)
	f.insightsErr = `{"error":{"message":"(#10) Application does not have permission for this action","code":10}}`
	run := setup(t, f, "")
	code, out, errs := run("metrics", "17900000000000001")
	if code != 0 {
		t.Fatalf("a refused insights call must not fail the command: %d %s", code, errs)
	}
	m := decode(t, out)
	if m["like_count"].(float64) != 12 || m["comments_count"].(float64) != 3 {
		t.Errorf("the counts are still returned: %v", m)
	}
	if e, _ := m["insights_error"].(string); !strings.Contains(e, "permission") {
		t.Errorf("and the reason sits beside them: %v", m)
	}
	if m["views"] != nil {
		t.Error("no insight numbers are invented")
	}
}

func TestMetricsFailsWhenTheCountsCannotBeRead(t *testing.T) {
	f := newFake(t)
	f.mediaErr = `{"error":{"message":"Unsupported get request. Object does not exist","code":100}}`
	run := setup(t, f, "")
	if code, _, errs := run("metrics", "17900000000000001"); code != 1 || errs == "" {
		t.Errorf("no counts, no result: %d %q", code, errs)
	}
	for name, args := range map[string][]string{"no id": {"metrics"}, "two ids": {"metrics", "1", "2"}, "not numeric": {"metrics", "abc"}} {
		if code, _, _ := run(args...); code != 2 && code != 1 {
			t.Errorf("%s: %d", name, code)
		}
	}
	code, out, _ := run("metrics", "17900000000000001", "--dry-run")
	if code != 0 || !strings.Contains(out, `"dry_run":true`) {
		t.Errorf("dry run: %d %s", code, out)
	}
}
