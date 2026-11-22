package postlog

import (
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func tmpLog(t *testing.T) *Log {
	t.Helper()
	return Open(filepath.Join(t.TempDir(), "state", "posts.jsonl"))
}

func sample() Entry {
	return Entry{Platform: "x", Account: "main", Kind: "post", RemoteID: "111", Text: "Hello, World!"}
}

func TestPathResolution(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", "/xdg/state")
	t.Setenv(EnvPath, "")
	if got := Open("").Path; got != "/xdg/state/agent-tools/posts.jsonl" {
		t.Errorf("default path: %s", got)
	}
	t.Setenv(EnvPath, "/env/p.jsonl")
	if got := Open("").Path; got != "/env/p.jsonl" {
		t.Errorf("env path: %s", got)
	}
	if got := Open("/arg/p.jsonl").Path; got != "/arg/p.jsonl" {
		t.Errorf("an explicit path beats the environment: %s", got)
	}
	for _, off := range []string{"off", "OFF"} {
		l := Open(off)
		if l.Enabled() {
			t.Errorf("%q must disable the log", off)
		}
		if e, err := l.Record(sample()); e != nil || err != nil {
			t.Errorf("a disabled log records nothing and says nothing: %v %v", e, err)
		}
		if dup, err := l.FindDuplicate(sample(), 0); dup != nil || err != nil {
			t.Errorf("a disabled log finds no duplicate")
		}
		if l.Note(sample()) != "" {
			t.Error("a disabled log has nothing to report")
		}
	}
}

func TestRecordAndReadBack(t *testing.T) {
	l := tmpLog(t)
	e, err := l.Record(sample())
	if err != nil || e == nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(e.ID, "p_") || e.Type != "post" || e.TextHash == "" || e.At.IsZero() {
		t.Errorf("entry not filled in: %+v", e)
	}
	if st, _ := os.Stat(l.Path); st.Mode().Perm() != 0o600 {
		t.Errorf("file mode %o, want 600", st.Mode().Perm())
	}
	if st, _ := os.Stat(filepath.Dir(l.Path)); st.Mode().Perm() != 0o700 {
		t.Errorf("dir mode %o, want 700", st.Mode().Perm())
	}
	if err := l.AddSnapshot(Snapshot{PostID: e.ID, Data: map[string]any{"like_count": 3}}); err != nil {
		t.Fatal(err)
	}
	d, err := l.Read()
	if err != nil || len(d.Entries) != 1 || len(d.Snapshots) != 1 || d.Skipped != 0 {
		t.Fatalf("%v %+v", err, d)
	}
	if d.Entries[0].RemoteID != "111" || d.Entries[0].Text != "Hello, World!" || d.Snapshots[0].PostID != e.ID {
		t.Errorf("round trip: %+v", d)
	}
}

func TestRecordRefusesIncompleteEntries(t *testing.T) {
	l := tmpLog(t)
	for name, e := range map[string]Entry{
		"no platform":  {Kind: "post", RemoteID: "1"},
		"no kind":      {Platform: "x", RemoteID: "1"},
		"no remote id": {Platform: "x", Kind: "post"},
	} {
		if _, err := l.Record(e); err == nil {
			t.Errorf("%s: expected an error", name)
		}
	}
	if err := l.AddSnapshot(Snapshot{}); err == nil {
		t.Error("a snapshot needs a post id")
	}
	if _, err := os.Stat(l.Path); err == nil {
		t.Error("a refused entry must not create the file")
	}
}

func TestMissingAndDamagedFiles(t *testing.T) {
	l := tmpLog(t)
	if d, err := l.Read(); err != nil || len(d.Entries) != 0 {
		t.Errorf("a missing ledger is an empty one: %v", err)
	}
	l.Record(sample())
	f, _ := os.OpenFile(l.Path, os.O_APPEND|os.O_WRONLY, 0o600)
	f.WriteString("not json at all\n{\"type\":\"weird\"}\n{\"type\":\"post\"}\n\n")
	f.Close()
	l.Record(Entry{Platform: "linkedin", Kind: "post", RemoteID: "9", Text: "two"})
	d, err := l.Read()
	if err != nil || len(d.Entries) != 2 || d.Skipped != 3 {
		t.Errorf("damaged lines are skipped and counted, never fatal: %v entries=%d skipped=%d", err, len(d.Entries), d.Skipped)
	}
}

func TestNormalizeAndHash(t *testing.T) {
	same := [][2]string{{"Hello  World", "hello world"}, {"  hi\n\tthere ", "Hi There"}, {"A B", "a b"}}
	for _, p := range same {
		if Hash(p[0]) != Hash(p[1]) {
			t.Errorf("%q and %q should match", p[0], p[1])
		}
	}
	for _, p := range [][2]string{{"ok!", "ok"}, {"a b", "ab"}, {"Hello", "Hello?"}} {
		if Hash(p[0]) == Hash(p[1]) {
			t.Errorf("%q and %q must differ", p[0], p[1])
		}
	}
	if Normalize("  \n ") != "" {
		t.Error("blank normalizes to empty")
	}
}

func TestFindDuplicateRules(t *testing.T) {
	l := tmpLog(t)
	base := sample()
	l.Record(base)
	same := base
	same.Text = "  hello,   WORLD!  "
	if d, _ := l.FindDuplicate(same, 0); d == nil || d.RemoteID != "111" {
		t.Error("the same text, differently spaced and cased, is a duplicate")
	}
	for name, mut := range map[string]func(*Entry){
		"other account":  func(e *Entry) { e.Account = "brand" },
		"other platform": func(e *Entry) { e.Platform = "linkedin" },
		"other kind":     func(e *Entry) { e.Kind = "reply" },
		"other target":   func(e *Entry) { e.Target = "999" },
		"other text":     func(e *Entry) { e.Text = "Something else" },
	} {
		e := same
		mut(&e)
		if d, _ := l.FindDuplicate(e, 0); d != nil {
			t.Errorf("%s must not count as a duplicate", name)
		}
	}
	// replies to different posts with the same words are not duplicates; to the same post they are
	r1 := Entry{Platform: "x", Account: "main", Kind: "reply", Target: "42", RemoteID: "r1", Text: "Thanks!"}
	l.Record(r1)
	r2 := r1
	r2.Target = "43"
	if d, _ := l.FindDuplicate(r2, 0); d != nil {
		t.Error("the same words in reply to another post are fine")
	}
	if d, _ := l.FindDuplicate(r1, 0); d == nil {
		t.Error("the same reply to the same post is a duplicate")
	}
	// the window
	old := Entry{Platform: "x", Account: "main", Kind: "post", RemoteID: "old", Text: "ancient", At: time.Now().Add(-40 * 24 * time.Hour)}
	l.Record(old)
	if d, _ := l.FindDuplicate(Entry{Platform: "x", Account: "main", Kind: "post", Text: "ancient"}, 0); d != nil {
		t.Error("a post older than the 30-day window is not a duplicate")
	}
	if d, _ := l.FindDuplicate(Entry{Platform: "x", Account: "main", Kind: "post", Text: "ancient"}, 60*24*time.Hour); d == nil {
		t.Error("a wider window finds it")
	}
}

func TestFindByIDAndPrefix(t *testing.T) {
	l := tmpLog(t)
	a, _ := l.Record(Entry{Platform: "x", Account: "a", Kind: "post", RemoteID: "1850000000000000001", Text: "one"})
	l.Record(Entry{Platform: "x", Account: "a", Kind: "post", RemoteID: "1850000000000000002", Text: "two"})
	l.Record(Entry{Platform: "x", Account: "b", Kind: "post", RemoteID: "777", Text: "three"})
	l.Record(Entry{Platform: "linkedin", Account: "c", Kind: "post", RemoteID: "777", Text: "four"})
	l.Record(Entry{Platform: "instagram", Account: "d", Kind: "post", RemoteID: "abcdef123456", Text: "five"})
	d, _ := l.Read()
	if e, err := d.Find("abcdef"); err != nil || e.Text != "five" {
		t.Errorf("a prefix that matches exactly one post resolves to it: %v", err)
	}
	if e, err := d.Find(a.ID); err != nil || e.Text != "one" {
		t.Errorf("by ledger id: %v", err)
	}
	if e, err := d.Find("1850000000000000002"); err != nil || e.Text != "two" {
		t.Errorf("by remote id: %v", err)
	}
	if e, err := d.Find("185000000000000000"); err == nil || e != nil {
		t.Errorf("an ambiguous prefix must be an error: %v", err)
	}
	if _, err := d.Find("777"); err == nil || !strings.Contains(err.Error(), "ledger id") {
		t.Errorf("the same remote id on two platforms must say to use the ledger id: %v", err)
	}
	if _, err := d.Find("nope"); err == nil {
		t.Error("an unknown id is an error")
	}
	if _, err := d.Find(""); err == nil {
		t.Error("an empty id is an error")
	}
}

func TestGuard(t *testing.T) {
	l := tmpLog(t)
	l.Record(sample())
	e := sample()
	if w, err := l.Guard(e, false, false); err == nil || w != "" || !strings.Contains(err.Error(), "--allow-duplicate") {
		t.Errorf("a repeat is an error that says how to override it: %v %q", err, w)
	}
	if w, err := l.Guard(e, true, false); err != nil || w != "" {
		t.Errorf("--allow-duplicate lets it through: %v %q", err, w)
	}
	if w, err := l.Guard(e, false, true); err != nil || !strings.HasPrefix(w, "warning:") {
		t.Errorf("a dry run warns and does not block: %v %q", err, w)
	}
	e.Text = "fresh text"
	if w, err := l.Guard(e, false, false); err != nil || w != "" {
		t.Errorf("new text is fine: %v %q", err, w)
	}
	// an unreadable ledger must never stop a post
	bad := Open(t.TempDir()) // a directory, not a file
	if w, err := bad.Guard(e, false, false); err != nil || w == "" {
		t.Errorf("a ledger that cannot be read is a warning, not a block: %v %q", err, w)
	}
}

func TestNoteNeverHidesASuccessfulPost(t *testing.T) {
	l := tmpLog(t)
	if m := l.Note(sample()); m != "" {
		t.Errorf("a good write has nothing to say: %q", m)
	}
	// a ledger path whose parent is a regular file cannot be written
	blocker := filepath.Join(t.TempDir(), "file")
	os.WriteFile(blocker, []byte("x"), 0o600)
	bad := Open(filepath.Join(blocker, "posts.jsonl"))
	m := bad.Note(sample())
	if !strings.Contains(m, "was published") || !strings.Contains(m, "111") || !strings.Contains(m, "post log") {
		t.Errorf("a failed write must say the post DID go out, with its id: %q", m)
	}
}

func TestConcurrentWritersDoNotCorruptTheFile(t *testing.T) {
	l := tmpLog(t)
	var wg sync.WaitGroup
	const n = 40
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			e := sample()
			e.RemoteID = strings.Repeat("9", 5) + string(rune('a'+i%26)) + strings.Repeat("x", i)
			e.Text = strings.Repeat("long text ", 200) + string(rune('a'+i%26))
			if _, err := l.Record(e); err != nil {
				t.Error(err)
			}
		}(i)
	}
	wg.Wait()
	d, err := l.Read()
	if err != nil || len(d.Entries) != n || d.Skipped != 0 {
		t.Errorf("every record must survive intact: %v entries=%d skipped=%d", err, len(d.Entries), d.Skipped)
	}
}

func TestLongTextIsCut(t *testing.T) {
	l := tmpLog(t)
	e := sample()
	e.Text = strings.Repeat("é", maxText+500)
	got, _ := l.Record(e)
	if len([]rune(got.Text)) != maxText {
		t.Errorf("text is cut at %d characters, not bytes: %d", maxText, len([]rune(got.Text)))
	}
}
