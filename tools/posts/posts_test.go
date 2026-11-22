package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/revans/agent-tools/postlog"
)

// every test gets its own ledger; none can reach the real one
func newLedger(t *testing.T) *postlog.Log {
	t.Helper()
	p := filepath.Join(t.TempDir(), "posts.jsonl")
	t.Setenv(postlog.EnvPath, p)
	t.Setenv("XDG_CONFIG_HOME", t.TempDir()) // and no real config either
	return postlog.Open("")
}

func ago(d time.Duration) time.Time { return time.Now().Add(-d).UTC() }

func rec(t *testing.T, l *postlog.Log, e postlog.Entry) postlog.Entry {
	t.Helper()
	if e.Kind == "" {
		e.Kind = "post"
	}
	got, err := l.Record(e)
	if err != nil || got == nil {
		t.Fatal(err)
	}
	return *got
}

func do(t *testing.T, stdin string, args ...string) (int, string, string) {
	t.Helper()
	var out, errb bytes.Buffer
	code := run(args, strings.NewReader(stdin), &out, &errb)
	return code, out.String(), errb.String()
}

func jsonOut(t *testing.T, s string) any {
	t.Helper()
	var v any
	if err := json.Unmarshal([]byte(s), &v); err != nil {
		t.Fatalf("not JSON: %v\n%s", err, s)
	}
	return v
}

func list(t *testing.T, args ...string) []map[string]any {
	t.Helper()
	code, out, errs := do(t, "", append([]string{"list"}, args...)...)
	if code != 0 {
		t.Fatalf("list %v: %d %s", args, code, errs)
	}
	var res []map[string]any
	for _, v := range jsonOut(t, out).([]any) {
		res = append(res, v.(map[string]any))
	}
	return res
}

func TestParseSince(t *testing.T) {
	now := time.Date(2026, 11, 22, 12, 0, 0, 0, time.UTC)
	for in, want := range map[string]time.Time{
		"30m": now.Add(-30 * time.Minute), "12h": now.Add(-12 * time.Hour), "7d": now.AddDate(0, 0, -7),
		"2w": now.AddDate(0, 0, -14), "2026-11-01": time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC),
		"2026-11-01T08:30:00Z": time.Date(2026, 11, 1, 8, 30, 0, 0, time.UTC), "": {},
	} {
		got, err := parseSince(in, now)
		if err != nil || !got.Equal(want) {
			t.Errorf("%q: got %v %v, want %v", in, got, err, want)
		}
	}
	for _, bad := range []string{"7x", "d", "-3d", "0d", "1.5d", "yesterday", "2026-13-45", "07d"} {
		if _, err := parseSince(bad, now); err == nil {
			t.Errorf("%q should be rejected", bad)
		}
	}
}

func TestListFiltersAndOrder(t *testing.T) {
	l := newLedger(t)
	rec(t, l, postlog.Entry{Platform: "x", Account: "a", RemoteID: "1", Text: "oldest", At: ago(10 * 24 * time.Hour)})
	rec(t, l, postlog.Entry{Platform: "linkedin", Account: "b", RemoteID: "2", Text: "middle", At: ago(5 * 24 * time.Hour)})
	rec(t, l, postlog.Entry{Platform: "x", Account: "a", Kind: "reply", Target: "9", RemoteID: "3", Text: "newest", At: ago(1 * time.Hour)})
	all := list(t)
	if len(all) != 3 || all[0]["remote_id"] != "3" || all[2]["remote_id"] != "1" {
		t.Errorf("newest first: %v", all)
	}
	for _, c := range []struct {
		args []string
		want int
	}{
		{[]string{"--platform", "x"}, 2}, {[]string{"--account", "b"}, 1}, {[]string{"--kind", "reply"}, 1},
		{[]string{"--since", "7d"}, 2}, {[]string{"--since", "2h"}, 1}, {[]string{"--limit", "2"}, 2},
		{[]string{"--platform", "x", "--since", "7d"}, 1}, {[]string{"--platform", "nope"}, 0},
	} {
		if got := list(t, c.args...); len(got) != c.want {
			t.Errorf("list %v: %d entries, want %d", c.args, len(got), c.want)
		}
	}
	if got := list(t, "--limit", "1"); got[0]["remote_id"] != "3" {
		t.Error("the limit keeps the newest")
	}
	for _, bad := range [][]string{{"--kind", "tweet"}, {"--limit", "-1"}, {"--since", "soon"}} {
		if code, _, _ := do(t, "", append([]string{"list"}, bad...)...); code != 2 {
			t.Errorf("list %v should be a usage error: %d", bad, code)
		}
	}
}

func TestEmptyAndDisabledLedgers(t *testing.T) {
	newLedger(t)
	if got := list(t); len(got) != 0 {
		t.Errorf("an empty ledger lists nothing: %v", got)
	}
	code, out, _ := do(t, "", "report")
	if code != 0 || jsonOut(t, out).(map[string]any)["published"].(float64) != 0 {
		t.Errorf("an empty report: %d %s", code, out)
	}
	t.Setenv(postlog.EnvPath, "off")
	if code, _, errs := do(t, "", "add", "--platform", "x", "--remote-id", "1", "--text", "t"); code != 1 || !strings.Contains(errs, "switched off") {
		t.Errorf("add with the log off: %d %q", code, errs)
	}
}

func TestShowFindsByAnyIDAndOrdersSnapshots(t *testing.T) {
	l := newLedger(t)
	a := rec(t, l, postlog.Entry{Platform: "x", Account: "a", RemoteID: "1850000000000000001", Text: "one"})
	rec(t, l, postlog.Entry{Platform: "x", Account: "a", RemoteID: "1850000000000000002", Text: "two"})
	l.AddSnapshot(postlog.Snapshot{PostID: a.ID, At: ago(1 * time.Hour), Data: map[string]any{"like_count": 9}})
	l.AddSnapshot(postlog.Snapshot{PostID: a.ID, At: ago(3 * time.Hour), Data: map[string]any{"like_count": 2}})
	for _, ref := range []string{a.ID, "1850000000000000001", a.ID[:5]} {
		code, out, errs := do(t, "", "show", ref)
		if code != 0 {
			t.Fatalf("show %s: %d %s", ref, code, errs)
		}
		m := jsonOut(t, out).(map[string]any)
		snaps := m["snapshots"].([]any)
		if len(snaps) != 2 || snaps[0].(map[string]any)["data"].(map[string]any)["like_count"].(float64) != 2 {
			t.Errorf("snapshots oldest first: %v", snaps)
		}
		if m["post"].(map[string]any)["metrics"].(map[string]any)["like_count"].(float64) != 9 {
			t.Errorf("the post shows its latest reading: %v", m["post"])
		}
	}
	if code, _, errs := do(t, "", "show", "185000000000000000"); code != 1 || !strings.Contains(errs, "ambiguous") {
		t.Errorf("an ambiguous prefix: %d %q", code, errs)
	}
	if code, _, _ := do(t, "", "show", "nope"); code != 1 {
		t.Error("an unknown id is an error")
	}
	if code, _, _ := do(t, ""); code != 0 {
		t.Error("no command prints help")
	}
	if code, _, _ := do(t, "", "show"); code != 2 {
		t.Error("show needs an id")
	}
}

func TestFind(t *testing.T) {
	l := newLedger(t)
	rec(t, l, postlog.Entry{Platform: "x", RemoteID: "1", Text: "Shipping the new release today", At: ago(2 * 24 * time.Hour)})
	rec(t, l, postlog.Entry{Platform: "linkedin", RemoteID: "2", Text: "shipping   THE new release today", At: ago(40 * 24 * time.Hour)})
	rec(t, l, postlog.Entry{Platform: "x", RemoteID: "3", Text: "Something else entirely"})
	find := func(args ...string) int {
		t.Helper()
		code, out, errs := do(t, "", append([]string{"find"}, args...)...)
		if code != 0 {
			t.Fatalf("find %v: %d %s", args, code, errs)
		}
		return len(jsonOut(t, out).([]any))
	}
	if n := find("--text", "SHIPPING the new release TODAY"); n != 2 {
		t.Errorf("an exact match ignores case and spacing: %d", n)
	}
	if n := find("--text", "new release"); n != 0 {
		t.Errorf("a part is not an exact match: %d", n)
	}
	if n := find("--text", "new release", "--contains"); n != 2 {
		t.Errorf("--contains: %d", n)
	}
	if n := find("--text", "shipping the new release today", "--platform", "linkedin"); n != 1 {
		t.Errorf("--platform: %d", n)
	}
	if n := find("--text", "shipping the new release today", "--within", "30d"); n != 1 {
		t.Errorf("--within drops the old one: %d", n)
	}
	if code, _, _ := do(t, "", "find"); code != 2 {
		t.Error("--text is required")
	}
}

func TestAdd(t *testing.T) {
	newLedger(t)
	code, out, errs := do(t, "", "add", "--platform", "x", "--kind", "reply", "--remote-id", "77", "--text", "By hand",
		"--account", "me", "--target", "5", "--url", "https://x.com/i/status/77", "--at", "2026-10-01")
	if code != 0 {
		t.Fatalf("%d %s", code, errs)
	}
	m := jsonOut(t, out).(map[string]any)
	if m["source"] != "posts add" || m["kind"] != "reply" || m["target"] != "5" || !strings.HasPrefix(m["at"].(string), "2026-10-01") || m["text_hash"] == "" {
		t.Errorf("entry: %v", m)
	}
	if got := list(t); len(got) != 1 || got[0]["remote_id"] != "77" {
		t.Errorf("it is in the ledger: %v", got)
	}
	for name, args := range map[string][]string{
		"no platform": {"add", "--remote-id", "1", "--text", "t"}, "no remote id": {"add", "--platform", "x", "--text", "t"},
		"no text": {"add", "--platform", "x", "--remote-id", "1"}, "blank text": {"add", "--platform", "x", "--remote-id", "1", "--text", "  "},
		"bad kind": {"add", "--platform", "x", "--remote-id", "1", "--text", "t", "--kind", "tweet"},
		"bad time": {"add", "--platform", "x", "--remote-id", "1", "--text", "t", "--at", "someday"},
	} {
		if code, _, _ := do(t, "", args...); code != 2 {
			t.Errorf("%s should be a usage error: %d", name, code)
		}
	}
}

func TestMetrics(t *testing.T) {
	l := newLedger(t)
	p := rec(t, l, postlog.Entry{Platform: "x", RemoteID: "42", Text: "measured"})
	if code, _, errs := do(t, "", "metrics", p.ID, "--data", `{"like_count": 3, "reply_count": 1}`); code != 0 {
		t.Fatalf("%d %s", code, errs)
	}
	if code, _, errs := do(t, `{"like_count": 8}`, "metrics", "42", "--data", "-"); code != 0 {
		t.Fatalf("stdin: %d %s", code, errs)
	}
	got := list(t)
	if got[0]["metrics"].(map[string]any)["like_count"].(float64) != 8 {
		t.Errorf("the newest reading wins: %v", got[0]["metrics"])
	}
	for name, args := range map[string][]string{"an array": {"metrics", "42", "--data", `[1]`}, "not json": {"metrics", "42", "--data", "oops"}, "null": {"metrics", "42", "--data", "null"}, "no data": {"metrics", "42"}, "no id": {"metrics", "--data", "{}"}} {
		if code, _, _ := do(t, "", args...); code != 2 {
			t.Errorf("%s should be a usage error: %d", name, code)
		}
	}
	if code, _, _ := do(t, "", "metrics", "nope", "--data", "{}"); code != 1 {
		t.Error("an unknown post is an error")
	}
}

func TestReport(t *testing.T) {
	l := newLedger(t)
	a := rec(t, l, postlog.Entry{Platform: "x", RemoteID: "1", Text: "big hit", At: ago(2 * 24 * time.Hour), URL: "https://x.com/i/status/1"})
	b := rec(t, l, postlog.Entry{Platform: "x", RemoteID: "2", Text: "small", At: ago(3 * 24 * time.Hour)})
	rec(t, l, postlog.Entry{Platform: "x", Kind: "reply", Target: "1", RemoteID: "3", Text: "unmeasured", At: ago(1 * 24 * time.Hour)})
	rec(t, l, postlog.Entry{Platform: "instagram", RemoteID: "4", Text: "old", At: ago(60 * 24 * time.Hour)})
	l.AddSnapshot(postlog.Snapshot{PostID: a.ID, At: ago(48 * time.Hour), Data: map[string]any{"like_count": 1, "reply_count": 1}})
	l.AddSnapshot(postlog.Snapshot{PostID: a.ID, At: ago(1 * time.Hour), Data: map[string]any{"like_count": 100, "reply_count": 4, "text": "ignored"}})
	l.AddSnapshot(postlog.Snapshot{PostID: b.ID, Data: map[string]any{"like_count": 10}})

	code, out, errs := do(t, "", "report")
	if code != 0 {
		t.Fatalf("%d %s", code, errs)
	}
	r := jsonOut(t, out).(map[string]any)
	if r["published"].(float64) != 4 {
		t.Errorf("published: %v", r["published"])
	}
	bp := r["by_platform"].(map[string]any)
	if bp["x"].(map[string]any)["posts"].(float64) != 2 || bp["x"].(map[string]any)["replys"] != nil && false {
		t.Errorf("by platform: %v", bp)
	}
	xm := r["metrics"].(map[string]any)["x"].(map[string]any)
	if xm["posts_measured"].(float64) != 2 {
		t.Errorf("two posts were measured: %v", xm)
	}
	if got := xm["totals"].(map[string]any)["like_count"].(float64); got != 110 {
		t.Errorf("only each post's LATEST reading counts (100 + 10, not 1): %v", got)
	}
	if got := xm["averages"].(map[string]any)["like_count"].(float64); got != 55 {
		t.Errorf("average: %v", got)
	}
	if got := xm["averages"].(map[string]any)["reply_count"].(float64); got != 4 {
		t.Errorf("an average is over the posts that HAVE the metric (one here): %v", got)
	}
	if _, has := xm["totals"].(map[string]any)["text"]; has {
		t.Error("non-numeric fields are not metrics")
	}
	top := r["top"].([]any)
	if r["ranked_by"] != "like_count" || len(top) != 2 || top[0].(map[string]any)["remote_id"] != "1" || top[0].(map[string]any)["like_count"].(float64) != 100 {
		t.Errorf("top: %v by %v", top, r["ranked_by"])
	}
	if _, has := r["note"]; has {
		t.Error("no note when there are metrics")
	}

	_, out, _ = do(t, "", "report", "--since", "7d", "--top", "1", "--by", "reply_count")
	r = jsonOut(t, out).(map[string]any)
	if r["published"].(float64) != 3 || r["ranked_by"] != "reply_count" || len(r["top"].([]any)) != 1 {
		t.Errorf("--since/--top/--by: %v", r)
	}
	_, out, _ = do(t, "", "report", "--platform", "instagram")
	r = jsonOut(t, out).(map[string]any)
	if r["published"].(float64) != 1 || r["note"] == nil {
		t.Errorf("a platform with no readings says so: %v", r)
	}
}

// ---- refresh ----

func script(t *testing.T, body string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "fake-metrics")
	if err := os.WriteFile(p, []byte("#!/bin/sh\n"+body+"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	return p
}

func refreshConfig(t *testing.T, platform, command, extra string) string {
	t.Helper()
	cfg := filepath.Join(t.TempDir(), "agent-tools.toml")
	body := fmt.Sprintf("[posts]\n[posts.refresh.%s]\ncommand = %s\n%s\n", platform, command, extra)
	if err := os.WriteFile(cfg, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return cfg
}

func TestRefreshStoresWhatTheConfiguredCommandPrints(t *testing.T) {
	l := newLedger(t)
	a := rec(t, l, postlog.Entry{Platform: "x", Account: "main", RemoteID: "111", Text: "one"})
	rec(t, l, postlog.Entry{Platform: "x", Account: "main", RemoteID: "222", Text: "two"})
	s := script(t, `printf '{"tweet":{"rid":"%s","who":"%s","like_count":7},"replies":[]}' "$1" "$2"`)
	cfg := refreshConfig(t, "x", fmt.Sprintf(`[%q, "{remote_id}", "{account}"]`, s), `select = "tweet"`)

	code, out, errs := do(t, "", "refresh", "--config", cfg)
	if code != 0 {
		t.Fatalf("%d %s", code, errs)
	}
	res := jsonOut(t, out).([]any)
	if len(res) != 2 {
		t.Fatalf("results: %v", res)
	}
	for _, r := range res {
		if r.(map[string]any)["ok"] != true {
			t.Errorf("result: %v", r)
		}
	}
	code, out, _ = do(t, "", "show", a.ID)
	m := jsonOut(t, out).(map[string]any)["post"].(map[string]any)["metrics"].(map[string]any)
	if m["rid"] != "111" || m["who"] != "main" || m["like_count"].(float64) != 7 || m["replies"] != nil {
		t.Errorf("the placeholders were filled in and `select` kept only the tweet: %v", m)
	}
}

func TestRefreshNeverLetsAnIDRunAnything(t *testing.T) {
	l := newLedger(t)
	marker := filepath.Join(t.TempDir(), "PWNED")
	hostile := "1; touch " + marker + " #"
	rec(t, l, postlog.Entry{Platform: "x", Account: "main", RemoteID: hostile, Text: "hostile id"})
	s := script(t, `printf '{"arg":"%s"}' "$(printf %s "$1" | tr -d '"\\')"`)
	cfg := refreshConfig(t, "x", fmt.Sprintf(`[%q, "{remote_id}"]`, s), "")
	if code, _, errs := do(t, "", "refresh", "--config", cfg); code != 0 {
		t.Fatalf("%d %s", code, errs)
	}
	if _, err := os.Stat(marker); err == nil {
		t.Fatal("the remote id was run as a command: it must only ever be one argument")
	}
	_, out, _ := do(t, "", "list")
	if got := jsonOut(t, out).([]any)[0].(map[string]any)["metrics"].(map[string]any)["arg"]; got != hostile {
		t.Errorf("the whole id arrives as a single argument: %v", got)
	}
}

func TestRefreshCarriesOnPastFailuresAndSkipsWhatItCannotRead(t *testing.T) {
	l := newLedger(t)
	rec(t, l, postlog.Entry{Platform: "x", Account: "main", RemoteID: "bad", Text: "fails", At: ago(3 * time.Hour)})
	rec(t, l, postlog.Entry{Platform: "x", Account: "main", RemoteID: "good", Text: "works", At: ago(2 * time.Hour)})
	rec(t, l, postlog.Entry{Platform: "x", Account: "main", RemoteID: "notjson", Text: "prints text", At: ago(90 * time.Minute)})
	rec(t, l, postlog.Entry{Platform: "x", Account: "main", RemoteID: "nopath", Text: "no tweet key", At: ago(80 * time.Minute)})
	rec(t, l, postlog.Entry{Platform: "linkedin", Account: "main", RemoteID: "urn:1", Text: "no command for linkedin", At: ago(1 * time.Hour)})
	rec(t, l, postlog.Entry{Platform: "x", Kind: "comment", Account: "main", RemoteID: "c1", Text: "a comment", At: ago(30 * time.Minute)})
	s := script(t, `case "$1" in bad) echo "boom" >&2; exit 3;; notjson) echo "hello";; nopath) echo '{"other":1}';; *) echo '{"tweet":{"like_count":1}}';; esac`)
	cfg := refreshConfig(t, "x", fmt.Sprintf(`[%q, "{remote_id}"]`, s), `select = "tweet"`)

	code, out, errs := do(t, "", "refresh", "--config", cfg)
	if code != 0 {
		t.Fatalf("a failed reading is reported, not fatal: %d %s", code, errs)
	}
	byRemote := map[string]map[string]any{}
	for _, r := range jsonOut(t, out).([]any) {
		m := r.(map[string]any)
		byRemote[m["remote_id"].(string)] = m
	}
	if byRemote["good"]["ok"] != true {
		t.Errorf("the good one still ran: %v", byRemote["good"])
	}
	if e, _ := byRemote["bad"]["error"].(string); !strings.Contains(e, "boom") || byRemote["bad"]["ok"] == true {
		t.Errorf("a failing command's stderr is reported: %v", byRemote["bad"])
	}
	if e, _ := byRemote["notjson"]["error"].(string); !strings.Contains(e, "did not print JSON") {
		t.Errorf("non-JSON: %v", byRemote["notjson"])
	}
	if e, _ := byRemote["nopath"]["error"].(string); !strings.Contains(e, `nothing at "tweet"`) {
		t.Errorf("a missing path: %v", byRemote["nopath"])
	}
	if sk, _ := byRemote["urn:1"]["skipped"].(string); !strings.Contains(sk, "no refresh command is configured for linkedin") {
		t.Errorf("a platform with no command is skipped: %v", byRemote["urn:1"])
	}
	if sk, _ := byRemote["c1"]["skipped"].(string); !strings.Contains(sk, "comments") {
		t.Errorf("comments are skipped: %v", byRemote["c1"])
	}
	// only the good one has a reading
	for _, v := range list(t) {
		_, has := v["metrics"]
		if has != (v["remote_id"] == "good") {
			t.Errorf("%v: has metrics = %v", v["remote_id"], has)
		}
	}
}

func TestRefreshDryRunRunsNothingAndDropsAMissingAccountPair(t *testing.T) {
	l := newLedger(t)
	rec(t, l, postlog.Entry{Platform: "x", Account: "main", RemoteID: "1", Text: "with an account", At: ago(2 * time.Hour)})
	rec(t, l, postlog.Entry{Platform: "x", RemoteID: "2", Text: "without one", At: ago(1 * time.Hour)})
	marker := filepath.Join(t.TempDir(), "RAN")
	s := script(t, "touch "+marker)
	cfg := refreshConfig(t, "x", fmt.Sprintf(`[%q, "thread", "{remote_id}", "--account", "{account}"]`, s), "")
	code, out, _ := do(t, "", "refresh", "--config", cfg, "--dry-run")
	if code != 0 {
		t.Fatalf("dry run: %d", code)
	}
	if _, err := os.Stat(marker); err == nil {
		t.Error("--dry-run must run nothing")
	}
	byRemote := map[string][]any{}
	for _, r := range jsonOut(t, out).([]any) {
		m := r.(map[string]any)
		byRemote[m["remote_id"].(string)], _ = m["command"].([]any)
	}
	if got := fmt.Sprint(byRemote["1"][1:]); got != "[thread 1 --account main]" {
		t.Errorf("with an account: %s", got)
	}
	if got := fmt.Sprint(byRemote["2"][1:]); got != "[thread 2]" {
		t.Errorf("a post with no account loses --account and its value: %s", got)
	}
	if d, _ := postlog.Open("").Read(); len(d.Snapshots) != 0 {
		t.Error("a dry run stores nothing")
	}
}

func TestRefreshConfigRules(t *testing.T) {
	l := newLedger(t)
	rec(t, l, postlog.Entry{Platform: "x", Account: "main", RemoteID: "1", Text: "one"})
	// a shell line is refused: commands are argument lists
	cfg := refreshConfig(t, "x", `"x thread 1 | jq ."`, "")
	if code, _, errs := do(t, "", "refresh", "--config", cfg); code != 1 || !strings.Contains(errs, "list of strings, not a shell line") {
		t.Errorf("a shell line: %d %q", code, errs)
	}
	cfg = refreshConfig(t, "x", `["x", 3]`, "")
	if code, _, errs := do(t, "", "refresh", "--config", cfg); code != 1 || !strings.Contains(errs, "list of strings") {
		t.Errorf("a non-string argument: %d %q", code, errs)
	}
	// an empty command switches the default off
	cfg = refreshConfig(t, "x", `[]`, "")
	_, out, _ := do(t, "", "refresh", "--config", cfg)
	if sk := jsonOut(t, out).([]any)[0].(map[string]any)["skipped"].(string); !strings.Contains(sk, "no refresh command") {
		t.Errorf("an empty command disables the platform: %s", sk)
	}
	// with no config at all the X default is there
	_, out, _ = do(t, "", "refresh", "--dry-run")
	cmd := jsonOut(t, out).([]any)[0].(map[string]any)["command"].([]any)
	if fmt.Sprint(cmd) != "[x thread 1 --account main]" {
		t.Errorf("the default X refresh: %v", cmd)
	}
	// where shows it
	code, out, _ := do(t, "", "where")
	w := jsonOut(t, out).(map[string]any)
	if code != 0 || w["posts"].(float64) != 1 || w["refresh"].(map[string]any)["x"] == nil {
		t.Errorf("where: %v", w)
	}
}

func TestRefreshOneByID(t *testing.T) {
	l := newLedger(t)
	a := rec(t, l, postlog.Entry{Platform: "x", Account: "main", RemoteID: "1", Text: "one"})
	rec(t, l, postlog.Entry{Platform: "x", Account: "main", RemoteID: "2", Text: "two"})
	s := script(t, `echo '{"like_count":5}'`)
	cfg := refreshConfig(t, "x", fmt.Sprintf(`[%q]`, s), "")
	code, out, errs := do(t, "", "refresh", a.ID, "--config", cfg)
	if code != 0 || len(jsonOut(t, out).([]any)) != 1 {
		t.Fatalf("one id, one result: %d %s %s", code, out, errs)
	}
	if code, _, _ := do(t, "", "refresh", "nope", "--config", cfg); code != 1 {
		t.Error("an unknown id is an error")
	}
	if code, _, _ := do(t, "", "refresh", "a", "b", "--config", cfg); code != 2 {
		t.Error("at most one id")
	}
}
