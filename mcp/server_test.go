package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestBuiltinManifestsLoad(t *testing.T) {
	tools, err := loadTools(nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(tools) < 30 {
		t.Fatalf("expected the built-in tools, got %d", len(tools))
	}
	// A destructive tool is off unless the server is started with -allow
	// destructive, so shipping one is safe. It must still tell the model, and
	// whoever reads the manifest, what it does: it publishes, and cannot be undone.
	for _, tl := range tools {
		if tl.Safety != SafetyDestructive {
			continue
		}
		d := strings.ToLower(tl.Description)
		if !(strings.Contains(d, "publish") || strings.Contains(d, "public")) || !strings.Contains(d, "cannot be undone") {
			t.Errorf("%s is destructive, so its description must say it is public (or publishes) and cannot be undone: %q", tl.Name, tl.Description)
		}
		if len(tl.Params) == 0 {
			t.Errorf("%s: a destructive tool must take explicit arguments", tl.Name)
		}
	}
}

// The default server must not offer any destructive tool: that is what makes
// shipping them safe.
func TestDefaultServerHidesEveryDestructiveBuiltin(t *testing.T) {
	tools, err := loadTools(nil)
	if err != nil {
		t.Fatal(err)
	}
	server, n := newServer(tools, safetyRank[SafetyWrite])
	ct, st := mcp.NewInMemoryTransports()
	ctx := context.Background()
	if _, err := server.Connect(ctx, st, nil); err != nil {
		t.Fatal(err)
	}
	sess, err := mcp.NewClient(&mcp.Implementation{Name: "t"}, nil).Connect(ctx, ct, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer sess.Close()
	listed, err := sess.ListTools(ctx, nil)
	if err != nil || len(listed.Tools) != n {
		t.Fatalf("%v %d/%d", err, len(listed.Tools), n)
	}
	hidden := map[string]bool{}
	for _, tl := range tools {
		if tl.Safety == SafetyDestructive {
			hidden[tl.Name] = true
		}
	}
	for _, lt := range listed.Tools {
		if hidden[lt.Name] {
			t.Errorf("%s is destructive and must not be registered at the default level", lt.Name)
		}
	}
	if len(hidden) == 0 {
		t.Log("no destructive built-ins yet")
	}
}

func mustTool(t *testing.T, doc string) Tool {
	t.Helper()
	ts, err := parseManifest("test", []byte(doc))
	if err != nil {
		t.Fatal(err)
	}
	return ts[0]
}

const sample = `
[[tool]]
name = "demo"
description = "demo tool"
bin = "fakecli"
args = ["run", "--json"]
safety = "write"
  [[tool.param]]
  name = "account"
  type = "string"
  flag = "--account"
  description = "a"
  [[tool.param]]
  name = "mode"
  type = "string"
  flag = "--mode"
  enum = ["fast", "slow"]
  description = "m"
  [[tool.param]]
  name = "limit"
  type = "integer"
  flag = "--limit"
  description = "l"
  [[tool.param]]
  name = "dry_run"
  type = "boolean"
  flag = "--dry-run"
  description = "d"
  [[tool.param]]
  name = "vars"
  type = "map"
  flag = "--var"
  description = "v"
  [[tool.param]]
  name = "file"
  type = "string"
  positional = true
  required = true
  description = "f"
`

func TestBuildArgs(t *testing.T) {
	tl := mustTool(t, sample)
	got, err := tl.buildArgs(json.RawMessage(`{"file":"-rf","account":"--evil","limit":5,"dry_run":true,"vars":{"b":"2","a":"x=y"}}`))
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"run", "--json", "--account=--evil", "--limit=5", "--dry-run", "--var=a=x=y", "--var=b=2", "--", "-rf"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got  %q\nwant %q", got, want)
	}
	// false booleans and omitted optionals add nothing
	got, _ = tl.buildArgs(json.RawMessage(`{"file":"a","dry_run":false}`))
	if !reflect.DeepEqual(got, []string{"run", "--json", "--", "a"}) {
		t.Fatalf("got %q", got)
	}
}

func TestBuildArgsRejects(t *testing.T) {
	tl := mustTool(t, sample)
	for name, in := range map[string]string{
		"missing required": `{}`,
		"empty required":   `{"file":""}`,
		"unknown arg":      `{"file":"a","shell":"rm -rf /"}`,
		"bad enum":         `{"file":"a","mode":"turbo"}`,
		"wrong type":       `{"file":"a","limit":"5"}`,
		"float integer":    `{"file":"a","limit":1.5}`,
		"map key with =":   `{"file":"a","vars":{"a=b":"c"}}`,
		"not an object":    `[1]`,
	} {
		if _, err := tl.buildArgs(json.RawMessage(in)); err == nil {
			t.Errorf("%s: expected an error", name)
		}
	}
}

func TestManifestValidation(t *testing.T) {
	for name, doc := range map[string]string{
		"no safety":    "[[tool]]\nname='a'\ndescription='d'\nbin='x'\n",
		"bad name":     "[[tool]]\nname='A b'\ndescription='d'\nbin='x'\nsafety='read'\n",
		"no desc":      "[[tool]]\nname='a'\nbin='x'\nsafety='read'\n",
		"bin has args": "[[tool]]\nname='a'\ndescription='d'\nbin='x --y'\nsafety='read'\n",
		"unknown key":  "[[tool]]\nname='a'\ndescription='d'\nbin='x'\nsafety='read'\nshell=true\n",
		"short flag":   "[[tool]]\nname='a'\ndescription='d'\nbin='x'\nsafety='read'\n[[tool.param]]\nname='p'\ntype='string'\nflag='-p'\ndescription='d'\n",
	} {
		if _, err := parseManifest("t", []byte(doc)); err == nil {
			t.Errorf("%s: expected an error", name)
		}
	}
}

func TestDuplicateToolNames(t *testing.T) {
	dir := t.TempDir()
	doc := "[[tool]]\nname='slack_post'\ndescription='d'\nbin='x'\nsafety='read'\n"
	os.WriteFile(filepath.Join(dir, "x.toml"), []byte(doc), 0o644)
	if _, err := loadTools([]string{dir}); err == nil || !strings.Contains(err.Error(), "more than once") {
		t.Fatalf("expected a duplicate error, got %v", err)
	}
}

// fakeCLI writes a program that reports its working directory and arguments,
// and exits with the code in FAKE_EXIT.
func fakeCLI(t *testing.T) {
	t.Helper()
	dir := t.TempDir()
	script := "#!/bin/sh\necho \"PWD=$(pwd)\"\nfor a in \"$@\"; do echo \"ARG=$a\"; done\necho oops >&2\nexit ${FAKE_EXIT:-0}\n"
	if err := os.WriteFile(filepath.Join(dir, "fakecli"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
}

func connect(t *testing.T, doc string, limit int) *mcp.ClientSession {
	t.Helper()
	ts, err := parseManifest("test", []byte(doc))
	if err != nil {
		t.Fatal(err)
	}
	server, _ := newServer(ts, limit)
	ct, st := mcp.NewInMemoryTransports()
	ctx := context.Background()
	if _, err := server.Connect(ctx, st, nil); err != nil {
		t.Fatal(err)
	}
	sess, err := mcp.NewClient(&mcp.Implementation{Name: "test"}, nil).Connect(ctx, ct, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { sess.Close() })
	return sess
}

func call(t *testing.T, s *mcp.ClientSession, name string, args map[string]any) (string, bool) {
	t.Helper()
	res, err := s.CallTool(context.Background(), &mcp.CallToolParams{Name: name, Arguments: args})
	if err != nil {
		t.Fatalf("protocol error: %v", err)
	}
	var b strings.Builder
	for _, c := range res.Content {
		if tc, ok := c.(*mcp.TextContent); ok {
			b.WriteString(tc.Text)
		}
	}
	return b.String(), res.IsError
}

func TestEndToEnd(t *testing.T) {
	fakeCLI(t)
	work := t.TempDir()
	t.Chdir(work)
	s := connect(t, sample, safetyRank[SafetyWrite])

	tools, err := s.ListTools(context.Background(), nil)
	if err != nil || len(tools.Tools) != 1 {
		t.Fatalf("list: %v %v", tools, err)
	}
	if tools.Tools[0].Annotations == nil || tools.Tools[0].Annotations.ReadOnlyHint {
		t.Errorf("a write tool must not be marked read-only")
	}

	out, isErr := call(t, s, "demo", map[string]any{"file": "-x", "account": "gierd"})
	if isErr {
		t.Fatalf("unexpected error: %s", out)
	}
	real, _ := filepath.EvalSymlinks(work)
	if !strings.Contains(out, "PWD="+real) {
		t.Errorf("the CLI must run in the client's cwd %s, got:\n%s", real, out)
	}
	for _, want := range []string{"ARG=run", "ARG=--account=gierd", "ARG=--", "ARG=-x"} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}
	if strings.Contains(out, "oops") {
		t.Errorf("stderr should be left out of a successful result:\n%s", out)
	}

	if out, isErr = call(t, s, "demo", map[string]any{}); !isErr || !strings.Contains(out, "missing required") {
		t.Errorf("bad arguments should be a tool error, got %v %q", isErr, out)
	}
}

func TestNonZeroExitAndOKExit(t *testing.T) {
	fakeCLI(t)
	t.Setenv("FAKE_EXIT", "2")
	s := connect(t, sample+"\n[[tool]]\nname='planlike'\ndescription='d'\nbin='fakecli'\nsafety='read'\nok_exit=[0,2]\n", safetyRank[SafetyWrite])

	out, isErr := call(t, s, "demo", map[string]any{"file": "a"})
	if !isErr || !strings.Contains(out, "code 2") || !strings.Contains(out, "oops") {
		t.Errorf("exit 2 should be an error carrying stderr, got %v %q", isErr, out)
	}
	out, isErr = call(t, s, "planlike", nil)
	if isErr || !strings.Contains(out, "exit code 2") {
		t.Errorf("exit 2 listed in ok_exit should succeed, got %v %q", isErr, out)
	}
}

func TestSafetyGate(t *testing.T) {
	doc := sample + "\n[[tool]]\nname='nuke'\ndescription='d'\nbin='fakecli'\nsafety='destructive'\n" +
		"\n[[tool]]\nname='peek'\ndescription='d'\nbin='fakecli'\nsafety='read'\n"
	names := func(limit int) []string {
		s := connect(t, doc, limit)
		r, _ := s.ListTools(context.Background(), nil)
		var n []string
		for _, tl := range r.Tools {
			n = append(n, tl.Name)
		}
		return n
	}
	if got := names(safetyRank[SafetyRead]); len(got) != 1 || got[0] != "peek" {
		t.Errorf("read limit: %v", got)
	}
	if got := names(safetyRank[SafetyWrite]); len(got) != 2 {
		t.Errorf("write limit: %v", got)
	}
	if got := names(safetyRank[SafetyDestructive]); len(got) != 3 {
		t.Errorf("destructive limit: %v", got)
	}
}

func TestMissingBinary(t *testing.T) {
	s := connect(t, "[[tool]]\nname='ghost'\ndescription='d'\nbin='no-such-cli-xyz'\nsafety='read'\n", 0)
	out, isErr := call(t, s, "ghost", nil)
	if !isErr || !strings.Contains(out, "not installed") {
		t.Errorf("got %v %q", isErr, out)
	}
}
