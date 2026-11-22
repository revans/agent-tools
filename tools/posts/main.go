// posts is the ledger of what the publishing tools have published: list it,
// search it, record posts made elsewhere, store and refresh their metrics, and
// report on how they did. x, linkedin and instagram write to it on their own
// whenever they publish; this tool reads it.
//
// The ledger is a plain JSON Lines file (see libs/postlog), so everything here can
// also be done with grep and jq. Nothing in this tool talks to a network: `refresh`
// runs commands you configured, such as `x thread ID`, and stores what they print.
package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
	"time"
)

var version = "0.1.0"

// command is one subcommand.
type command struct {
	name, summary, usage string
	run                  func(c *ctx) (any, error)
	setup                func(fs *flag.FlagSet)
}

// ctx is one invocation.
type ctx struct {
	fs     *flag.FlagSet
	args   []string // positional
	stdin  io.Reader
	stderr io.Writer
	config string
	pretty bool
	dryRun bool
}

type usageError string

func (u usageError) Error() string { return string(u) }

func usage(format string, a ...any) error { return usageError(fmt.Sprintf(format, a...)) }

func commands() []*command {
	return []*command{
		listCmd(), showCmd(), findCmd(), reportCmd(), addCmd(), metricsCmd(), refreshCmd(), whereCmd(),
	}
}

func main() {
	os.Exit(run(os.Args[1:], os.Stdin, os.Stdout, os.Stderr))
}

func run(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	if len(args) == 0 || args[0] == "help" || args[0] == "-h" || args[0] == "--help" {
		help(stdout)
		return 0
	}
	if args[0] == "version" || args[0] == "--version" {
		fmt.Fprintln(stdout, "posts", version)
		return 0
	}
	var cmd *command
	for _, c := range commands() {
		if c.name == args[0] {
			cmd = c
		}
	}
	if cmd == nil {
		fmt.Fprintf(stderr, "posts: unknown command %q. Run `posts help`.\n", args[0])
		return 2
	}
	c := &ctx{stdin: stdin, stderr: stderr}
	fs := flag.NewFlagSet("posts "+cmd.name, flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.StringVar(&c.config, "config", "", "config file (the shared agent-tools file's [posts] table, or a posts.toml)")
	fs.BoolVar(&c.pretty, "pretty", false, "indent the JSON output (the default when stdout is a terminal)")
	fs.BoolVar(&c.dryRun, "dry-run", false, "refresh: show the commands that would run, and run none")
	if cmd.setup != nil {
		cmd.setup(fs)
	}
	pos, err := parseInterspersed(fs, args[1:])
	if err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}
	c.fs, c.args = fs, pos
	out, err := cmd.run(c)
	if err != nil {
		var ue usageError
		if errors.As(err, &ue) {
			fmt.Fprintf(stderr, "posts: %s\nusage: posts %s\n", string(ue), cmd.usage)
			return 2
		}
		fmt.Fprintf(stderr, "posts: %v\n", err)
		return 1
	}
	if out != nil {
		var buf bytes.Buffer
		enc := json.NewEncoder(&buf)
		enc.SetEscapeHTML(false)
		if c.pretty || isTerminal(stdout) {
			enc.SetIndent("", "  ")
		}
		if err := enc.Encode(out); err != nil {
			fmt.Fprintf(stderr, "posts: %v\n", err)
			return 1
		}
		stdout.Write(buf.Bytes())
	}
	return 0
}

func help(w io.Writer) {
	fmt.Fprintln(w, "posts: the ledger of what the publishing tools have published\n\nUsage: posts COMMAND [flags]\n\nCommands:")
	for _, c := range commands() {
		fmt.Fprintf(w, "  %-9s %s\n", c.name, c.summary)
	}
	fmt.Fprintln(w, "\nOutput is JSON. Errors go to stderr; exit 0 ok, 1 error, 2 usage.\nThe ledger is $AGENT_TOOLS_POSTS, or ~/.local/state/agent-tools/posts.jsonl ('off' disables it).")
}

func isTerminal(w io.Writer) bool {
	f, ok := w.(*os.File)
	if !ok {
		return false
	}
	st, err := f.Stat()
	return err == nil && st.Mode()&os.ModeCharDevice != 0
}

// parseInterspersed lets flags and positionals mix. Everything after -- is positional.
func parseInterspersed(fs *flag.FlagSet, args []string) ([]string, error) {
	var tail []string
	for i, a := range args {
		if a == "--" {
			args, tail = args[:i], args[i+1:]
			break
		}
	}
	var pos []string
	for {
		if err := fs.Parse(args); err != nil {
			return nil, err
		}
		args = fs.Args()
		if len(args) == 0 {
			break
		}
		pos = append(pos, args[0])
		args = args[1:]
	}
	return append(pos, tail...), nil
}

// parseSince accepts a duration (30m, 12h, 7d, 2w) counted back from now, a date
// (2026-11-01), or an RFC 3339 time.
func parseSince(s string, now time.Time) (time.Time, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return time.Time{}, nil
	}
	if n := len(s); n >= 2 {
		var unit time.Duration
		switch s[n-1] {
		case 'm':
			unit = time.Minute
		case 'h':
			unit = time.Hour
		case 'd':
			unit = 24 * time.Hour
		case 'w':
			unit = 7 * 24 * time.Hour
		}
		if unit != 0 {
			var k int
			if _, err := fmt.Sscanf(s[:n-1], "%d", &k); err == nil && k > 0 && fmt.Sprint(k) == s[:n-1] {
				return now.Add(-time.Duration(k) * unit), nil
			}
		}
	}
	for _, layout := range []string{time.RFC3339, "2006-01-02"} {
		if t, err := time.Parse(layout, s); err == nil {
			return t, nil
		}
	}
	return time.Time{}, fmt.Errorf("%q is not a duration like 7d or 12h, a date like 2026-11-01, or an RFC 3339 time", s)
}
