package postlog

import (
	"bytes"
	"flag"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func useLedger(t *testing.T) *Log {
	t.Helper()
	p := filepath.Join(t.TempDir(), "posts.jsonl")
	t.Setenv(EnvPath, p)
	return Open("")
}

func TestOptionsFlags(t *testing.T) {
	var o Options
	fs := flag.NewFlagSet("x", flag.ContinueOnError)
	o.Flags(fs)
	if err := fs.Parse([]string{"--allow-duplicate", "--no-log"}); err != nil || !o.AllowDuplicate || !o.NoLog {
		t.Errorf("flags: %v %+v", err, o)
	}
	var d Options
	fs2 := flag.NewFlagSet("y", flag.ContinueOnError)
	d.Flags(fs2)
	fs2.Parse(nil)
	if d.AllowDuplicate || d.NoLog {
		t.Error("both switches default to off")
	}
}

func TestBeforeAndAfter(t *testing.T) {
	l := useLedger(t)
	var o Options
	var w bytes.Buffer
	e := sample()

	if err := o.Before(e, false, &w); err != nil || w.Len() != 0 {
		t.Fatalf("a first post is fine and silent: %v %q", err, w.String())
	}
	o.After(e, &w)
	if d, _ := l.Read(); len(d.Entries) != 1 {
		t.Fatalf("After must record: %d", len(d.Entries))
	}

	// the same text again
	if err := o.Before(e, false, &w); err == nil || !strings.Contains(err.Error(), "--allow-duplicate") {
		t.Errorf("a repeat is refused, with the way out: %v", err)
	}
	w.Reset()
	if err := o.Before(e, true, &w); err != nil || !strings.HasPrefix(w.String(), "x: warning:") {
		t.Errorf("a dry run warns on the writer and does not block: %v %q", err, w.String())
	}
	allow := Options{AllowDuplicate: true}
	w.Reset()
	if err := allow.Before(e, false, &w); err != nil || w.Len() != 0 {
		t.Errorf("--allow-duplicate: %v %q", err, w.String())
	}
	// --no-log stops the record but not the repeat check
	quiet := Options{NoLog: true}
	if err := quiet.Before(e, false, &w); err == nil {
		t.Error("--no-log must not switch off the duplicate check")
	}
	other := e
	other.Text = "something new"
	other.RemoteID = "222"
	quiet.After(other, &w)
	if d, _ := l.Read(); len(d.Entries) != 1 {
		t.Errorf("--no-log must not record: %d entries", len(d.Entries))
	}
}

func TestAfterReportsAWriteFailureWithoutFailing(t *testing.T) {
	blocker := filepath.Join(t.TempDir(), "file")
	os.WriteFile(blocker, []byte("x"), 0o600)
	t.Setenv(EnvPath, filepath.Join(blocker, "posts.jsonl"))
	var w bytes.Buffer
	Options{}.After(sample(), &w)
	if !strings.Contains(w.String(), "was published") || !strings.Contains(w.String(), "111") {
		t.Errorf("the message must say the post went out, with its id: %q", w.String())
	}
	var nilWriter Options
	nilWriter.After(sample(), nil) // must not panic with no writer
}

func TestDisabledLedgerIsInvisible(t *testing.T) {
	t.Setenv(EnvPath, "off")
	var w bytes.Buffer
	var o Options
	if err := o.Before(sample(), false, &w); err != nil || w.Len() != 0 {
		t.Errorf("%v %q", err, w.String())
	}
	o.After(sample(), &w)
	if w.Len() != 0 {
		t.Errorf("a disabled ledger says nothing: %q", w.String())
	}
}
