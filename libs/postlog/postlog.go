// Package postlog is the ledger of what the publishing tools have posted: one
// JSON line per publication, appended after it succeeds. It lets a tool refuse to
// post the same text twice, and lets `posts` list, find and report on what went out.
//
// The file is append-only JSON Lines at $XDG_STATE_HOME/agent-tools/posts.jsonl
// (override with $AGENT_TOOLS_POSTS; "off" disables the log). It is plain text so it
// can be read, grepped and backed up without any tool.
package postlog

import (
	"bufio"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
	"unicode"
)

const (
	// EnvPath overrides the ledger's location; the value "off" turns it off.
	EnvPath = "AGENT_TOOLS_POSTS"
	// DefaultWindow is how far back a repeat of the same text counts as a duplicate.
	DefaultWindow = 30 * 24 * time.Hour

	maxText = 10000 // stored text is cut here so one huge post cannot bloat the ledger
	maxLine = 1 << 20
)

// Entry is one publication.
type Entry struct {
	Type     string    `json:"type"` // always "post"
	ID       string    `json:"id"`   // this ledger's id for it
	At       time.Time `json:"at"`
	Platform string    `json:"platform"` // x | linkedin | instagram | ...
	Account  string    `json:"account,omitempty"`
	Kind     string    `json:"kind"` // post | reply | comment
	RemoteID string    `json:"remote_id"`
	URL      string    `json:"url,omitempty"`
	Text     string    `json:"text"`
	TextHash string    `json:"text_hash"`
	Target   string    `json:"target,omitempty"` // what a reply or comment answers
	Media    []string  `json:"media,omitempty"`
	Source   string    `json:"source,omitempty"` // the command that made it, e.g. "x post"
}

// Snapshot is a reading of a post's metrics at one moment.
type Snapshot struct {
	Type   string    `json:"type"` // always "metrics"
	PostID string    `json:"post_id"`
	At     time.Time `json:"at"`
	Data   any       `json:"data"`
}

// Log is a ledger file.
type Log struct {
	Path string
	off  bool
}

// Open resolves the ledger: the override argument, then $AGENT_TOOLS_POSTS, then
// the default state path. A value of "off" disables it.
func Open(override string) *Log {
	p := override
	if p == "" {
		p = os.Getenv(EnvPath)
	}
	if strings.EqualFold(p, "off") {
		return &Log{off: true}
	}
	if p == "" {
		p = DefaultPath()
	}
	return &Log{Path: p}
}

// DefaultPath is $XDG_STATE_HOME/agent-tools/posts.jsonl, or ~/.local/state/....
func DefaultPath() string {
	base := os.Getenv("XDG_STATE_HOME")
	if base == "" {
		if h, err := os.UserHomeDir(); err == nil {
			base = filepath.Join(h, ".local", "state")
		} else {
			base = ".state"
		}
	}
	return filepath.Join(base, "agent-tools", "posts.jsonl")
}

// Enabled reports whether the log records anything.
func (l *Log) Enabled() bool { return l != nil && !l.off }

// Normalize is how two texts are compared: lower case, with every run of white
// space made one space. Punctuation is kept, so "ok!" and "ok" differ.
func Normalize(text string) string {
	var b strings.Builder
	space := true
	for _, r := range strings.ToLower(text) {
		if unicode.IsSpace(r) {
			if !space {
				b.WriteByte(' ')
			}
			space = true
			continue
		}
		b.WriteRune(r)
		space = false
	}
	return strings.TrimSpace(b.String())
}

// Hash is a short, stable fingerprint of Normalize(text).
func Hash(text string) string {
	sum := sha256.Sum256([]byte(Normalize(text)))
	return hex.EncodeToString(sum[:8])
}

func newID() string {
	b := make([]byte, 5)
	if _, err := rand.Read(b); err != nil {
		panic(err) // the system's random source is gone
	}
	return "p_" + hex.EncodeToString(b)
}

func (l *Log) append(v any) error {
	line, err := json.Marshal(v)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(l.Path), 0o700); err != nil {
		return err
	}
	f, err := os.OpenFile(l.Path, os.O_WRONLY|os.O_APPEND|os.O_CREATE, 0o600)
	if err != nil {
		return err
	}
	// One write call per record, to a file opened O_APPEND, so records from
	// concurrent processes do not interleave.
	_, werr := f.Write(append(line, '\n'))
	if cerr := f.Close(); werr == nil {
		werr = cerr
	}
	return werr
}

// Record appends an entry, filling in its type, id, time and text hash. It
// returns the entry as written. A disabled log records nothing and returns nil.
func (l *Log) Record(e Entry) (*Entry, error) {
	if !l.Enabled() {
		return nil, nil
	}
	if e.Platform == "" || e.Kind == "" || e.RemoteID == "" {
		return nil, errors.New("a ledger entry needs a platform, a kind and the remote id")
	}
	e.Type = "post"
	if e.ID == "" {
		e.ID = newID()
	}
	if e.At.IsZero() {
		e.At = time.Now().UTC()
	}
	if r := []rune(e.Text); len(r) > maxText {
		e.Text = string(r[:maxText])
	}
	e.TextHash = Hash(e.Text)
	return &e, l.append(e)
}

// AddSnapshot appends a metrics reading for a post.
func (l *Log) AddSnapshot(s Snapshot) error {
	if !l.Enabled() {
		return nil
	}
	if s.PostID == "" {
		return errors.New("a snapshot needs the post's ledger id")
	}
	s.Type = "metrics"
	if s.At.IsZero() {
		s.At = time.Now().UTC()
	}
	return l.append(s)
}

// Data is everything in the ledger.
type Data struct {
	Entries   []Entry
	Snapshots []Snapshot
	Skipped   int // lines that were not valid records, left alone
}

// Read loads the ledger. A missing file is an empty ledger.
func (l *Log) Read() (*Data, error) {
	d := &Data{}
	if !l.Enabled() {
		return d, nil
	}
	f, err := os.Open(l.Path)
	if errors.Is(err, os.ErrNotExist) {
		return d, nil
	}
	if err != nil {
		return nil, err
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 64*1024), maxLine)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		var head struct {
			Type string `json:"type"`
		}
		if json.Unmarshal([]byte(line), &head) != nil {
			d.Skipped++
			continue
		}
		switch head.Type {
		case "post":
			var e Entry
			if json.Unmarshal([]byte(line), &e) != nil || e.ID == "" {
				d.Skipped++
				continue
			}
			d.Entries = append(d.Entries, e)
		case "metrics":
			var s Snapshot
			if json.Unmarshal([]byte(line), &s) != nil || s.PostID == "" {
				d.Skipped++
				continue
			}
			d.Snapshots = append(d.Snapshots, s)
		default:
			d.Skipped++
		}
	}
	return d, sc.Err()
}

// Find returns the entry whose ledger id or remote id is ref, or starts with it
// when that is unambiguous.
func (d *Data) Find(ref string) (*Entry, error) {
	if ref == "" {
		return nil, errors.New("no id given")
	}
	var exact, prefix []*Entry
	for i := range d.Entries {
		e := &d.Entries[i]
		if e.ID == ref || e.RemoteID == ref {
			exact = append(exact, e)
		} else if strings.HasPrefix(e.ID, ref) || strings.HasPrefix(e.RemoteID, ref) {
			prefix = append(prefix, e)
		}
	}
	switch {
	case len(exact) == 1:
		return exact[0], nil
	case len(exact) > 1:
		return nil, fmt.Errorf("%q matches %d posts (the same remote id on different accounts); use the ledger id such as %s", ref, len(exact), exact[0].ID)
	case len(prefix) == 1:
		return prefix[0], nil
	case len(prefix) > 1:
		return nil, fmt.Errorf("%q is ambiguous: it starts %d ids", ref, len(prefix))
	}
	return nil, fmt.Errorf("no post with id %q", ref)
}

// FindDuplicate returns the newest earlier entry that is the same publication:
// the same platform, account, kind and target, with the same normalized text,
// made within the window. The same text from another account, or as a reply to a
// different post, is not a duplicate.
func (l *Log) FindDuplicate(e Entry, within time.Duration) (*Entry, error) {
	if !l.Enabled() {
		return nil, nil
	}
	d, err := l.Read()
	if err != nil {
		return nil, err
	}
	if within <= 0 {
		within = DefaultWindow
	}
	hash, now := Hash(e.Text), time.Now()
	var best *Entry
	for i := range d.Entries {
		p := &d.Entries[i]
		if p.Platform != e.Platform || p.Account != e.Account || p.Kind != e.Kind || p.Target != e.Target || p.TextHash != hash {
			continue
		}
		if now.Sub(p.At) > within {
			continue
		}
		if best == nil || p.At.After(best.At) {
			best = p
		}
	}
	return best, nil
}

// Guard is the check a publishing tool makes before it posts. A repeat is an
// error, unless allow is set. In a dry run nothing is blocked: the repeat comes
// back as a warning to print. A problem reading the ledger is a warning too, never
// a reason to stop a post.
func (l *Log) Guard(e Entry, allow, dry bool) (warning string, err error) {
	dup, rerr := l.FindDuplicate(e, DefaultWindow)
	if rerr != nil {
		return fmt.Sprintf("could not read the post log to check for a repeat: %v", rerr), nil
	}
	if dup == nil || allow {
		return "", nil
	}
	msg := fmt.Sprintf("this exact text was already published to %s as %s on %s (id %s, %s ago). Pass --allow-duplicate to publish it again",
		dup.Platform, orDash(dup.Account), dup.At.Format("2006-01-02 15:04"), dup.RemoteID, since(dup.At))
	if dry {
		return "warning: " + msg, nil
	}
	return "", errors.New(msg)
}

// Note records a publication that has already happened. A failure to write the
// ledger must never hide that the post went out, so it comes back as a message
// for the caller to print, not as an error.
func (l *Log) Note(e Entry) (message string) {
	if !l.Enabled() {
		return ""
	}
	if _, err := l.Record(e); err != nil {
		return fmt.Sprintf("the post was published (id %s) but could not be written to the post log %s: %v", e.RemoteID, l.Path, err)
	}
	return ""
}

func orDash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

func since(t time.Time) string {
	d := time.Since(t).Round(time.Minute)
	switch {
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	case d < 48*time.Hour:
		return fmt.Sprintf("%dh", int(d.Hours()))
	}
	return fmt.Sprintf("%dd", int(d.Hours()/24))
}
