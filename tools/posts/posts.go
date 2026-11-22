package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os/exec"
	"sort"
	"strings"
	"time"

	"github.com/revans/agent-tools/apikit"
	"github.com/revans/agent-tools/postlog"
)

var validKinds = map[string]bool{"post": true, "reply": true, "comment": true}

// view is an entry with its latest metrics reading, for output.
type view struct {
	postlog.Entry
	Metrics   any        `json:"metrics,omitempty"`
	MetricsAt *time.Time `json:"metrics_at,omitempty"`
}

func readLedger() (*postlog.Log, *postlog.Data, error) {
	l := postlog.Open("")
	d, err := l.Read()
	return l, d, err
}

// latest maps a ledger id to its newest snapshot.
func latest(d *postlog.Data) map[string]postlog.Snapshot {
	m := map[string]postlog.Snapshot{}
	for _, s := range d.Snapshots {
		if cur, ok := m[s.PostID]; !ok || s.At.After(cur.At) {
			m[s.PostID] = s
		}
	}
	return m
}

func views(es []postlog.Entry, d *postlog.Data) []view {
	last := latest(d)
	out := make([]view, 0, len(es))
	for _, e := range es {
		v := view{Entry: e}
		if s, ok := last[e.ID]; ok {
			at := s.At
			v.Metrics, v.MetricsAt = s.Data, &at
		}
		out = append(out, v)
	}
	return out
}

// filter is the set of flags that narrows a list of entries.
type filter struct {
	platform, account, kind, since string
	limit                          int
}

func (f *filter) flags(fs *flag.FlagSet, defaultLimit int, defaultSince string) {
	fs.StringVar(&f.platform, "platform", "", "only this platform (x, linkedin, instagram)")
	fs.StringVar(&f.account, "account", "", "only this account")
	fs.StringVar(&f.kind, "kind", "", "only this kind: post, reply or comment")
	fs.StringVar(&f.since, "since", defaultSince, "only newer than this: 7d, 12h, 2w, 2026-11-01 or an RFC 3339 time")
	fs.IntVar(&f.limit, "limit", defaultLimit, "at most this many, newest first (0 for all)")
}

// apply returns the matching entries, newest first. The limit is applied last.
func (f *filter) apply(es []postlog.Entry) ([]postlog.Entry, error) {
	if f.kind != "" && !validKinds[f.kind] {
		return nil, usage("--kind must be post, reply or comment")
	}
	if f.limit < 0 {
		return nil, usage("--limit cannot be negative")
	}
	since, err := parseSince(f.since, time.Now())
	if err != nil {
		return nil, usage("--since: %v", err)
	}
	var out []postlog.Entry
	for _, e := range es {
		if (f.platform != "" && e.Platform != f.platform) || (f.account != "" && e.Account != f.account) ||
			(f.kind != "" && e.Kind != f.kind) || e.At.Before(since) {
			continue
		}
		out = append(out, e)
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].At.After(out[j].At) })
	if f.limit > 0 && len(out) > f.limit {
		out = out[:f.limit]
	}
	return out, nil
}

// ---- list, show, find ----

func listCmd() *command {
	var f filter
	return &command{
		name: "list", summary: "list what was published, newest first", usage: "list [--platform P] [--account A] [--kind K] [--since 7d] [--limit N]",
		setup: func(fs *flag.FlagSet) { f.flags(fs, 20, "") },
		run: func(c *ctx) (any, error) {
			_, d, err := readLedger()
			if err != nil {
				return nil, err
			}
			es, err := f.apply(d.Entries)
			if err != nil {
				return nil, err
			}
			return views(es, d), nil
		},
	}
}

func showCmd() *command {
	return &command{
		name: "show", summary: "one post with every metrics reading taken for it", usage: "show ID",
		run: func(c *ctx) (any, error) {
			if len(c.args) != 1 {
				return nil, usage("show needs one id: the ledger id (p_...), the remote id, or a unique start of either")
			}
			_, d, err := readLedger()
			if err != nil {
				return nil, err
			}
			e, err := d.Find(c.args[0])
			if err != nil {
				return nil, err
			}
			var snaps []postlog.Snapshot
			for _, s := range d.Snapshots {
				if s.PostID == e.ID {
					snaps = append(snaps, s)
				}
			}
			sort.SliceStable(snaps, func(i, j int) bool { return snaps[i].At.Before(snaps[j].At) })
			return map[string]any{"post": views([]postlog.Entry{*e}, d)[0], "snapshots": snaps}, nil
		},
	}
}

func findCmd() *command {
	var text, within string
	var contains bool
	var f filter
	return &command{
		name: "find", summary: "find posts by their text (to check whether something already went out)", usage: "find --text TEXT [--contains] [--platform P] [--within 30d]",
		setup: func(fs *flag.FlagSet) {
			fs.StringVar(&text, "text", "", "the text to look for (required)")
			fs.BoolVar(&contains, "contains", false, "match posts that contain the text, not only an exact match")
			fs.StringVar(&within, "within", "", "only posts newer than this (same forms as --since)")
			f.flags(fs, 0, "")
		},
		run: func(c *ctx) (any, error) {
			if strings.TrimSpace(text) == "" {
				return nil, usage("--text is required")
			}
			if within != "" {
				f.since = within
			}
			_, d, err := readLedger()
			if err != nil {
				return nil, err
			}
			es, err := f.apply(d.Entries)
			if err != nil {
				return nil, err
			}
			want := postlog.Normalize(text)
			var hit []postlog.Entry
			for _, e := range es {
				n := postlog.Normalize(e.Text)
				if n == want || (contains && strings.Contains(n, want)) {
					hit = append(hit, e)
				}
			}
			return views(hit, d), nil
		},
	}
}

// ---- add and metrics ----

func addCmd() *command {
	var platform, kind, remote, text, account, url, target, at string
	return &command{
		name: "add", summary: "record a post that was made without these tools", usage: "add --platform P --kind K --remote-id ID --text TEXT [--account A] [--url U] [--target T] [--at TIME]",
		setup: func(fs *flag.FlagSet) {
			fs.StringVar(&platform, "platform", "", "x, linkedin, instagram, ... (required)")
			fs.StringVar(&kind, "kind", "post", "post, reply or comment")
			fs.StringVar(&remote, "remote-id", "", "the platform's id for it (required)")
			fs.StringVar(&text, "text", "", "the text as published (required)")
			fs.StringVar(&account, "account", "", "the account it was published from")
			fs.StringVar(&url, "url", "", "a link to it")
			fs.StringVar(&target, "target", "", "what a reply or comment answers")
			fs.StringVar(&at, "at", "", "when it was published (RFC 3339 or a date); default now")
		},
		run: func(c *ctx) (any, error) {
			if platform == "" || remote == "" || strings.TrimSpace(text) == "" {
				return nil, usage("--platform, --remote-id and --text are required")
			}
			if !validKinds[kind] {
				return nil, usage("--kind must be post, reply or comment")
			}
			e := postlog.Entry{Platform: platform, Kind: kind, RemoteID: remote, Text: text, Account: account, URL: url, Target: target, Source: "posts add"}
			if at != "" {
				t, err := parseSince(at, time.Now())
				if err != nil || t.IsZero() {
					return nil, usage("--at: %q is not a date or an RFC 3339 time", at)
				}
				e.At = t.UTC()
			}
			l := postlog.Open("")
			if !l.Enabled() {
				return nil, errors.New("the post log is switched off ($AGENT_TOOLS_POSTS=off)")
			}
			got, err := l.Record(e)
			if err != nil {
				return nil, err
			}
			return got, nil
		},
	}
}

func metricsCmd() *command {
	var data string
	return &command{
		name: "metrics", summary: "store a metrics reading for a post", usage: "metrics ID --data JSON   (or --data - to read it from stdin)",
		setup: func(fs *flag.FlagSet) {
			fs.StringVar(&data, "data", "", "a JSON object of metrics, or - for stdin (required)")
		},
		run: func(c *ctx) (any, error) {
			if len(c.args) != 1 || data == "" {
				return nil, usage("metrics needs one post id and --data")
			}
			var raw []byte
			if data == "-" {
				b, err := io.ReadAll(io.LimitReader(c.stdin, 1<<20))
				if err != nil {
					return nil, err
				}
				raw = b
			} else {
				raw = []byte(data)
			}
			var obj map[string]any
			if err := json.Unmarshal(raw, &obj); err != nil || obj == nil {
				return nil, usage("--data must be a JSON object such as {\"like_count\": 3}")
			}
			l, d, err := readLedger()
			if err != nil {
				return nil, err
			}
			e, err := d.Find(c.args[0])
			if err != nil {
				return nil, err
			}
			if err := l.AddSnapshot(postlog.Snapshot{PostID: e.ID, Data: obj}); err != nil {
				return nil, err
			}
			return map[string]any{"post": e.ID, "remote_id": e.RemoteID, "stored": obj}, nil
		},
	}
}

// ---- refresh ----

// refreshSpec says how to read one platform's metrics: a command to run, and the
// part of its JSON output to keep.
type refreshSpec struct {
	Command []string
	Select  string
}

// defaultRefresh is what works with the tools in this repository.
var defaultRefresh = map[string]refreshSpec{
	"x": {Command: []string{"x", "thread", "{remote_id}", "--account", "{account}"}, Select: "tweet"},
}

// loadRefresh reads [posts.refresh.PLATFORM] from the config over the defaults.
// No config file is fine. An empty command switches a platform off.
func loadRefresh(configPath string) (map[string]refreshSpec, string, error) {
	out := map[string]refreshSpec{}
	for k, v := range defaultRefresh {
		out[k] = v
	}
	cfg, err := apikit.NewLocator("posts").Load("posts", configPath)
	if err != nil {
		if errors.Is(err, apikit.ErrNoConfig) {
			return out, "", nil
		}
		return nil, "", err
	}
	raw, ok := cfg.Settings["refresh"]
	if !ok {
		return out, cfg.Path, nil
	}
	tbl, ok := raw.(map[string]any)
	if !ok {
		return nil, "", fmt.Errorf("%s: refresh must be a table of platforms", cfg.WhereDefined())
	}
	for platform, v := range tbl {
		spec, ok := v.(map[string]any)
		if !ok {
			return nil, "", fmt.Errorf("%s: refresh.%s must be a table with a command", cfg.WhereDefined(), platform)
		}
		var rs refreshSpec
		if cmd, ok := spec["command"]; ok {
			list, ok := cmd.([]any)
			if !ok {
				return nil, "", fmt.Errorf("%s: refresh.%s.command must be a list of strings, not a shell line", cfg.WhereDefined(), platform)
			}
			for _, a := range list {
				s, ok := a.(string)
				if !ok {
					return nil, "", fmt.Errorf("%s: refresh.%s.command must be a list of strings", cfg.WhereDefined(), platform)
				}
				rs.Command = append(rs.Command, s)
			}
		}
		if sel, ok := spec["select"]; ok {
			if rs.Select, ok = sel.(string); !ok {
				return nil, "", fmt.Errorf("%s: refresh.%s.select must be a string", cfg.WhereDefined(), platform)
			}
		}
		if len(rs.Command) == 0 {
			delete(out, platform)
			continue
		}
		out[platform] = rs
	}
	return out, cfg.Path, nil
}

// expand builds the argument list for one post. A placeholder is replaced inside
// a single argument; nothing is joined into a shell line. If the post has no
// account, an argument that needs {account} is dropped with the flag before it.
func expand(spec refreshSpec, e postlog.Entry) []string {
	repl := strings.NewReplacer("{remote_id}", e.RemoteID, "{account}", e.Account, "{id}", e.ID, "{platform}", e.Platform, "{target}", e.Target, "{url}", e.URL)
	var argv []string
	for _, a := range spec.Command {
		if e.Account == "" && strings.Contains(a, "{account}") {
			if n := len(argv); n > 1 && strings.HasPrefix(argv[n-1], "-") {
				argv = argv[:n-1]
			}
			continue
		}
		argv = append(argv, repl.Replace(a))
	}
	return argv
}

const refreshTimeout = 60 * time.Second

func runRefresh(parent context.Context, spec refreshSpec, e postlog.Entry) (any, error) {
	argv := expand(spec, e)
	if len(argv) == 0 || argv[0] == "" {
		return nil, errors.New("the refresh command is empty")
	}
	cctx, cancel := context.WithTimeout(parent, refreshTimeout)
	defer cancel()
	cmd := exec.CommandContext(cctx, argv[0], argv[1:]...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &limitWriter{w: &stdout, n: 4 << 20}, &limitWriter{w: &stderr, n: 64 << 10}
	if err := cmd.Run(); err != nil {
		msg := strings.TrimSpace(stderr.String())
		if len(msg) > 300 {
			msg = msg[:300] + "…"
		}
		if cctx.Err() != nil {
			return nil, fmt.Errorf("%s timed out after %s", argv[0], refreshTimeout)
		}
		return nil, fmt.Errorf("%s: %v: %s", argv[0], err, msg)
	}
	var v any
	dec := json.NewDecoder(&stdout)
	if err := dec.Decode(&v); err != nil {
		return nil, fmt.Errorf("%s did not print JSON: %v", argv[0], err)
	}
	if spec.Select != "" {
		sel, err := apikit.Select(v, spec.Select)
		if err != nil {
			return nil, err
		}
		if sel == nil {
			return nil, fmt.Errorf("%s printed JSON with nothing at %q", argv[0], spec.Select)
		}
		v = sel
	}
	return v, nil
}

type limitWriter struct {
	w io.Writer
	n int
}

func (l *limitWriter) Write(p []byte) (int, error) {
	if l.n <= 0 {
		return len(p), nil
	}
	if len(p) > l.n {
		l.w.Write(p[:l.n])
		l.n = 0
		return len(p), nil
	}
	l.n -= len(p)
	return l.w.Write(p)
}

func refreshCmd() *command {
	var f filter
	return &command{
		name: "refresh", summary: "read fresh metrics for recent posts, using the commands in your config", usage: "refresh [ID] [--platform P] [--since 30d] [--limit N] [--dry-run]",
		setup: func(fs *flag.FlagSet) {
			f.flags(fs, 20, "30d")
		},
		run: func(c *ctx) (any, error) {
			specs, _, err := loadRefresh(c.config)
			if err != nil {
				return nil, err
			}
			l, d, err := readLedger()
			if err != nil {
				return nil, err
			}
			var targets []postlog.Entry
			if len(c.args) == 1 {
				e, err := d.Find(c.args[0])
				if err != nil {
					return nil, err
				}
				targets = []postlog.Entry{*e}
			} else if len(c.args) == 0 {
				es, err := f.apply(d.Entries)
				if err != nil {
					return nil, err
				}
				targets = es
			} else {
				return nil, usage("refresh takes at most one id")
			}
			type result struct {
				ID       string   `json:"id"`
				RemoteID string   `json:"remote_id"`
				Platform string   `json:"platform"`
				OK       bool     `json:"ok"`
				Skipped  string   `json:"skipped,omitempty"`
				Error    string   `json:"error,omitempty"`
				Command  []string `json:"command,omitempty"`
				Data     any      `json:"data,omitempty"`
			}
			results := []result{}
			for _, e := range targets {
				r := result{ID: e.ID, RemoteID: e.RemoteID, Platform: e.Platform}
				spec, ok := specs[e.Platform]
				switch {
				case !ok:
					r.Skipped = "no refresh command is configured for " + e.Platform
				case e.Kind == "comment":
					r.Skipped = "comments have no metrics to read"
				case c.dryRun:
					r.Command, r.Skipped = expand(spec, e), "dry run"
				default:
					data, err := runRefresh(context.Background(), spec, e)
					if err != nil {
						r.Error = err.Error()
						break
					}
					if err := l.AddSnapshot(postlog.Snapshot{PostID: e.ID, Data: data}); err != nil {
						r.Error = "could not store the reading: " + err.Error()
						break
					}
					r.OK, r.Data = true, data
				}
				results = append(results, r)
			}
			return results, nil
		},
	}
}

// ---- where ----

func whereCmd() *command {
	return &command{
		name: "where", summary: "show where the ledger is, how much is in it, and what refresh will run", usage: "where",
		run: func(c *ctx) (any, error) {
			l := postlog.Open("")
			out := map[string]any{"enabled": l.Enabled()}
			if l.Enabled() {
				out["ledger"] = l.Path
				d, err := l.Read()
				if err != nil {
					return nil, err
				}
				out["posts"], out["snapshots"], out["skipped_lines"] = len(d.Entries), len(d.Snapshots), d.Skipped
			}
			specs, cfg, err := loadRefresh(c.config)
			if err != nil {
				return nil, err
			}
			refresh := map[string][]string{}
			for p, s := range specs {
				refresh[p] = s.Command
			}
			out["refresh"], out["config"] = refresh, cfg
			return out, nil
		},
	}
}
