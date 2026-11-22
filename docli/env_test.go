package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

var testNow = time.Date(2026, 11, 12, 10, 0, 0, 0, time.UTC)

// testEnv runs docli in-process against a fake DigitalOcean, in a temporary project and home directory.
type testEnv struct {
	t       *testing.T
	fake    *fakeDO
	app     *App
	out     *bytes.Buffer
	errb    *bytes.Buffer
	sleeps  []time.Duration
	home    string
	project string // has a .git directory, so it is the project root
	cwd     string
	env     map[string]string
}

func newEnv(t *testing.T) *testEnv {
	t.Helper()
	e := &testEnv{t: t, fake: newFakeDO(t), out: &bytes.Buffer{}, errb: &bytes.Buffer{}, env: map[string]string{"DIGITALOCEAN_TOKEN": fakeToken}}
	e.home = t.TempDir()
	e.project = t.TempDir()
	os.Mkdir(filepath.Join(e.project, ".git"), 0o755)
	e.cwd = e.project
	e.app = &App{
		Stdin: strings.NewReader(""), Stdout: e.out, Stderr: e.errb,
		Getenv:  func(k string) string { return e.env[k] },
		HomeDir: func() (string, error) { return e.home, nil },
		Getwd:   func() (string, error) { return e.cwd, nil },
		BaseURL: e.fake.srv.URL,
		Sleep: func(ctx context.Context, d time.Duration) error {
			e.sleeps = append(e.sleeps, d)
			return nil
		},
		Now:       func() time.Time { return testNow },
		WriteFile: atomicWriteFile,
	}
	return e
}

// run executes docli with the given arguments, clearing the output buffers first.
func (e *testEnv) run(args ...string) int {
	e.out.Reset()
	e.errb.Reset()
	return e.app.Main(args)
}

func (e *testEnv) tty(v bool)     { e.app.StdinIsTTY = v }
func (e *testEnv) stdin(s string) { e.app.Stdin = strings.NewReader(s); e.app.reader = nil }
func (e *testEnv) manifest() string {
	return filepath.Join(e.project, "config", "deploys", "manifest.yml")
}

func (e *testEnv) readManifest() string {
	b, err := os.ReadFile(e.manifest())
	if err != nil {
		e.t.Fatalf("manifest not written: %v", err)
	}
	return string(b)
}

func (e *testEnv) mustRun(args ...string) {
	e.t.Helper()
	if code := e.run(args...); code != 0 {
		e.t.Fatalf("docli %v exited %d\nstdout: %s\nstderr: %s", args, code, e.out.String(), e.errb.String())
	}
}

func (e *testEnv) expect(code int, args ...string) {
	e.t.Helper()
	if got := e.run(args...); got != code {
		e.t.Fatalf("docli %v exited %d, want %d\nstdout: %s\nstderr: %s", args, got, code, e.out.String(), e.errb.String())
	}
}

func contain(t *testing.T, haystack, needle string) {
	t.Helper()
	if !strings.Contains(haystack, needle) {
		t.Fatalf("expected %q in:\n%s", needle, haystack)
	}
}

func notContain(t *testing.T, haystack, needle string) {
	t.Helper()
	if strings.Contains(haystack, needle) {
		t.Fatalf("did not expect %q in:\n%s", needle, haystack)
	}
}
