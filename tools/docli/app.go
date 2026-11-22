package main

import (
	"bufio"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
	"time"
)

// version is set at build time: -X main.version=...
var version = "dev"

// Exit codes.
const (
	exitOK      = 0
	exitError   = 1
	exitUsage   = 2
	exitRefused = 3
)

// usageError is a mistake in how the command was typed (exit 2).
type usageError struct{ msg string }

func (e *usageError) Error() string { return e.msg }

func usagef(format string, a ...any) error { return &usageError{fmt.Sprintf(format, a...)} }

// refusedError means the tool declined to act: the person said no, or the action was unsafe (exit 3).
type refusedError struct{ msg string }

func (e *refusedError) Error() string { return e.msg }

func refusedf(format string, a ...any) error { return &refusedError{fmt.Sprintf(format, a...)} }

// diffError is a non-error "difference found" outcome (manifest verify): exit 1 with the report already printed.
type silentError struct{}

func (silentError) Error() string { return "" }

// exitCodeError ends the program with a specific exit code after the output was already printed. `docli plan`
// uses code 2 for "there are changes", which is not an error.
type exitCodeError struct{ code int }

func (e *exitCodeError) Error() string { return fmt.Sprintf("exit %d", e.code) }

// Globals are the flags every command accepts.
type Globals struct {
	JSON    bool
	Yes     bool
	DryRun  bool
	Debug   bool
	Region  string
	Timeout time.Duration
}

// App is everything outside the process the commands touch, so tests can replace it.
type App struct {
	Stdin      io.Reader
	Stdout     io.Writer
	Stderr     io.Writer
	Getenv     func(string) string
	HomeDir    func() (string, error)
	Getwd      func() (string, error)
	StdinIsTTY bool
	BaseURL    string // empty means the real DigitalOcean API; only tests set it
	SpacesURL  string // empty means REGION.digitaloceanspaces.com; only tests set it (path-style, one endpoint for every region)
	Cloud      Cloud  // optional replacement for the godo-backed client
	Sleep      func(ctx context.Context, d time.Duration) error
	Now        func() time.Time
	WriteFile  func(path string, data []byte) error // manifest writer; tests inject failures

	secrets []string // passwords seen in API answers; scrubbed from every error and message (see secrets.go)

	base    Globals // globals given before the command name
	errJSON bool    // --json was seen, so errors print as JSON
	reader  *bufio.Reader
}

func realSleep(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

// Run holds one command's connection and settings.
type Run struct {
	*App
	G     Globals
	Cloud Cloud
	Ctx   context.Context
	Token string
}

// newFlags makes a flag set that has the global flags plus whatever the command adds.
func (a *App) newFlags(name string) (*flag.FlagSet, *Globals) {
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	g := a.base
	fs.BoolVar(&g.JSON, "json", g.JSON, "")
	fs.BoolVar(&g.Yes, "yes", g.Yes, "")
	fs.BoolVar(&g.DryRun, "dry-run", g.DryRun, "")
	fs.BoolVar(&g.Debug, "debug", g.Debug, "")
	fs.StringVar(&g.Region, "region", g.Region, "")
	fs.DurationVar(&g.Timeout, "timeout", g.Timeout, "")
	return fs, &g
}

type helpRequested struct{ cmd string }

func (helpRequested) Error() string { return "help requested" }

// parseFlags parses flags that may appear before, between or after positional arguments.
func (a *App) parseFlags(fs *flag.FlagSet, args []string, cmd string) ([]string, error) {
	var pos []string
	for {
		if err := fs.Parse(args); err != nil {
			if errors.Is(err, flag.ErrHelp) {
				return nil, helpRequested{cmd}
			}
			return nil, usagef("%s: %s (try: docli help %s)", cmd, cleanFlagErr(err), cmd)
		}
		if f := fs.Lookup("json"); f != nil && f.Value.String() == "true" {
			a.errJSON = true
		}
		args = fs.Args()
		if len(args) == 0 {
			return pos, nil
		}
		if args[0] == "--" {
			return append(pos, args[1:]...), nil
		}
		pos = append(pos, args[0])
		args = args[1:]
	}
}

func cleanFlagErr(err error) string {
	return strings.TrimPrefix(err.Error(), "flag provided but not defined: ")
}

// stringList is a repeatable flag.
type stringList []string

func (s *stringList) String() string     { return strings.Join(*s, ",") }
func (s *stringList) Set(v string) error { *s = append(*s, v); return nil }

// envOr returns the flag value, or the environment variable when the flag is empty.
func (a *App) envOr(v, env string) string {
	if v != "" {
		return v
	}
	return a.Getenv(env)
}

func (a *App) readLine() (string, error) {
	if a.reader == nil {
		a.reader = bufio.NewReader(a.Stdin)
	}
	s, err := a.reader.ReadString('\n')
	if err != nil && s == "" {
		return "", err
	}
	return strings.TrimSpace(s), nil
}

func newOSApp() *App {
	fi, err := os.Stdin.Stat()
	tty := err == nil && fi.Mode()&os.ModeCharDevice != 0
	return &App{
		Stdin: os.Stdin, Stdout: os.Stdout, Stderr: os.Stderr, Getenv: os.Getenv,
		HomeDir: os.UserHomeDir, Getwd: os.Getwd, StdinIsTTY: tty,
		Sleep: realSleep, Now: time.Now, WriteFile: atomicWriteFile,
	}
}
