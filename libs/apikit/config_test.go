package apikit

import (
	"bytes"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/pelletier/go-toml/v2"
)

// These scenarios mirror libs/agent-config/src/lib.rs. The two implementations
// are separate code; running the same cases against both is what keeps them
// from drifting apart.

const (
	nestedDoc = "[reddit]\ndefault = \"a\"\n[reddit.accounts.a]\nclient_id = \"1\"\n[notif-slack.accounts.m]\ntoken_env = \"X\"\n"
	legacyDoc = "default = \"b\"\n[accounts.b]\nclient_id = \"2\"\n"
)

type tmp struct {
	t    *testing.T
	root string
}

func newTmp(t *testing.T) *tmp { return &tmp{t: t, root: t.TempDir()} }

func (x *tmp) write(rel, body string, mode os.FileMode) string {
	p := filepath.Join(x.root, rel)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		x.t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(body), mode); err != nil {
		x.t.Fatal(err)
	}
	os.Chmod(p, mode)
	return p
}

func (x *tmp) locator() Locator {
	return Locator{ConfigHome: filepath.Join(x.root, "home"), ProjectDir: filepath.Join(x.root, "proj")}
}

func TestSharedFileIsFoundAndParsed(t *testing.T) {
	x := newTmp(t)
	x.write("home/agent-tools/config.toml", nestedDoc, 0o600)
	c, err := x.locator().Load("reddit", "")
	if err != nil {
		t.Fatal(err)
	}
	if !c.Unified || c.Default != "a" || c.Accounts["a"]["client_id"] != "1" {
		t.Errorf("%+v", c)
	}
	if c.Dir() != filepath.Join(x.root, "home/agent-tools/reddit") {
		t.Errorf("dir %s", c.Dir())
	}
	if c.WhereDefined() != "[reddit] in "+c.Path {
		t.Errorf("where defined: %s", c.WhereDefined())
	}
	if other, err := x.locator().Load("notif-slack", ""); err != nil || !other.Unified {
		t.Errorf("another tool's table in the same file: %v", err)
	}
}

func TestLookupOrder(t *testing.T) {
	x := newTmp(t)
	x.write("home/reddit/config.toml", legacyDoc, 0o600)
	x.write("proj/reddit.toml", "default = \"p\"\n[accounts.p]\nclient_id = \"3\"\n", 0o600)
	c, err := x.locator().Load("reddit", "")
	if err != nil || c.Unified || !strings.HasSuffix(c.Path, "home/reddit/config.toml") {
		t.Fatalf("the user's own file comes before ./TOOL.toml: %v %+v", err, c)
	}
	if c.Dir() != filepath.Join(x.root, "home/reddit") {
		t.Errorf("a tool's own file keeps its directory: %s", c.Dir())
	}
	x.write("home/agent-tools/config.toml", nestedDoc, 0o600)
	if c, _ = x.locator().Load("reddit", ""); !c.Unified || !strings.HasSuffix(c.Path, "home/agent-tools/config.toml") {
		t.Errorf("the shared file beats the tool's own: %+v", c)
	}
	x.write("proj/agent-tools.toml", "[reddit]\ndefault = \"proj\"\n[reddit.accounts.proj]\nclient_id = \"4\"\n", 0o600)
	if c, _ = x.locator().Load("reddit", ""); c.Default != "proj" {
		t.Errorf("a project file beats the user file: %+v", c)
	}
	x.write("proj/agent-tools.toml", "[other]\nx = 1\n", 0o600)
	if c, _ = x.locator().Load("reddit", ""); c.Default != "a" {
		t.Errorf("a project file with no table for this tool must not hide the user file: %+v", c)
	}
}

func TestExplicitAndEnvPaths(t *testing.T) {
	x := newTmp(t)
	x.write("home/agent-tools/config.toml", nestedDoc, 0o600)
	own := x.write("elsewhere/mine.toml", legacyDoc, 0o600)
	if c, err := x.locator().Load("reddit", own); err != nil || c.Unified || c.Default != "b" {
		t.Errorf("explicit own file: %v %+v", err, c)
	}
	shared := x.write("elsewhere/shared.toml", nestedDoc, 0o600)
	if c, err := x.locator().Load("reddit", shared); err != nil || !c.Unified {
		t.Errorf("explicit shared file is detected by content: %v", err)
	}
	l := x.locator()
	l.EnvPath = own
	if c, _ := l.Load("reddit", ""); c.Path != own {
		t.Errorf("env path: %v", c)
	}
	l.EnvPath = filepath.Join(x.root, "missing.toml")
	if _, err := l.Load("reddit", ""); err == nil || !strings.Contains(err.Error(), "REDDIT_CONFIG points at a missing file") {
		t.Errorf("missing env path: %v", err)
	}
	if _, err := x.locator().Load("reddit", filepath.Join(x.root, "nope")); err == nil {
		t.Error("a missing explicit file is an error")
	}
}

func TestLooseSharedFileIsRefusedButAnOwnFileIsNot(t *testing.T) {
	x := newTmp(t)
	x.write("home/agent-tools/config.toml", nestedDoc, 0o644)
	_, err := x.locator().Load("reddit", "")
	if err == nil || !strings.Contains(err.Error(), "readable by other users") || !strings.Contains(err.Error(), "chmod 600") {
		t.Errorf("%v", err)
	}
	y := newTmp(t)
	y.write("home/reddit/config.toml", legacyDoc, 0o644)
	if _, err := y.locator().Load("reddit", ""); err != nil {
		t.Errorf("a tool's own file keeps its old behavior: %v", err)
	}
}

func TestNotFoundNamesEveryPlace(t *testing.T) {
	_, err := newTmp(t).locator().Load("reddit", "")
	if err == nil {
		t.Fatal("expected an error")
	}
	for _, want := range []string{"agent-tools.toml", "agent-tools/config.toml", "reddit/config.toml", "reddit.toml", "reddit init"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("%q missing from: %v", want, err)
		}
	}
}

func TestBrokenSharedFileIsAnErrorNotASkip(t *testing.T) {
	x := newTmp(t)
	x.write("proj/agent-tools.toml", "[reddit\nbroken", 0o600)
	x.write("home/reddit/config.toml", legacyDoc, 0o600)
	if _, err := x.locator().Load("reddit", ""); err == nil || !strings.Contains(err.Error(), "parsing") {
		t.Errorf("%v", err)
	}
	y := newTmp(t)
	y.write("home/agent-tools/config.toml", "reddit = 3\n", 0o600)
	if _, err := y.locator().Load("reddit", ""); err == nil || !strings.Contains(err.Error(), "must be a table") {
		t.Errorf("%v", err)
	}
}

func decodeDoc(t *testing.T, s string) map[string]any {
	t.Helper()
	var m map[string]any
	if err := toml.Unmarshal([]byte(s), &m); err != nil {
		t.Fatalf("%v\n%s", err, s)
	}
	return m
}

func assertNests(t *testing.T, text string) string {
	t.Helper()
	nested, err := Nest("tool", text)
	if err != nil {
		t.Fatalf("%v\n---\n%s", err, text)
	}
	want := map[string]any{}
	if orig := decodeDoc(t, text); len(orig) > 0 {
		want["tool"] = orig
	}
	if got := decodeDoc(t, nested); !reflect.DeepEqual(got, want) {
		t.Errorf("meaning changed:\n%s", nested)
	}
	return nested
}

func TestNestKeepsMeaningAndComments(t *testing.T) {
	n := assertNests(t, "# my settings\ndefault = \"a\"\n\n# the account\n[accounts.a]\nclient_id = \"T\" # trailing\n[accounts.a.sub]\nx = \"y\"\n")
	for _, want := range []string{"# my settings", "# trailing", "[tool.accounts.a.sub]"} {
		if !strings.Contains(n, want) {
			t.Errorf("%q missing:\n%s", want, n)
		}
	}
	assertNests(t, "[accounts.\"odd.name\"]\nx = 1\n")
	assertNests(t, "[[servers]]\nname = \"a\"\n[[servers]]\nname = \"b\"\n")
	assertNests(t, "a = 1\nb = \"two\"\n")
	assertNests(t, "")
	assertNests(t, "# only a comment\n")
	assertNests(t, "  [ accounts . a ]\n  x = 1\n")
	n = assertNests(t, "note = \"\"\"\n[not.a.header]\n[[nor.this]]\n\"\"\"\n[accounts.a]\nx = 1\n")
	if !strings.Contains(n, "\n[not.a.header]\n") || strings.Contains(n, "[tool.not") {
		t.Errorf("a header-looking line inside a multi-line string must be left alone:\n%s", n)
	}
	assertNests(t, "note = '''\n[x]\n'''\n")
	assertNests(t, "no_trailing_newline = true")
}

func TestInitAndAddToShared(t *testing.T) {
	x := newTmp(t)
	dest := filepath.Join(x.root, "home/agent-tools/config.toml")
	if err := InitShared("reddit", "# starter\ndefault = \"a\"\n[accounts.a]\nclient_id = \"1\"\n", dest); err != nil {
		t.Fatal(err)
	}
	if st, _ := os.Stat(dest); st.Mode().Perm() != 0o600 {
		t.Errorf("file mode %o", st.Mode().Perm())
	}
	if st, _ := os.Stat(filepath.Dir(dest)); st.Mode().Perm() != 0o700 {
		t.Errorf("dir mode %o", st.Mode().Perm())
	}
	if err := AddToShared("youtube", "default = \"k\"\n[accounts.k]\napi_key_env = \"K\"\n", dest, "youtube: added"); err != nil {
		t.Fatal(err)
	}
	doc := decodeDoc(t, readFile(t, dest))
	if doc["reddit"] == nil || doc["youtube"] == nil {
		t.Errorf("both tools' tables, side by side: %v", doc)
	}
	before := readFile(t, dest)
	if err := AddToShared("youtube", "x = 1\n", dest, "again"); err == nil || !strings.Contains(err.Error(), "already has a [youtube] table") {
		t.Errorf("%v", err)
	}
	if readFile(t, dest) != before {
		t.Error("a refusal must change nothing")
	}
	os.Chmod(dest, 0o644)
	if err := AddToShared("x", "y = 1\n", dest, "n"); err == nil || !strings.Contains(err.Error(), "readable by other users") {
		t.Errorf("appending to a loose shared file: %v", err)
	}
	os.Chmod(dest, 0o600)
	if c, err := x.locator().Load("youtube", dest); err != nil || !c.Unified {
		t.Errorf("the written file loads: %v", err)
	}
}

func readFile(t *testing.T, p string) string {
	t.Helper()
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestCLIInitWritesTheSharedFileAndWhereExplains(t *testing.T) {
	x := newTmp(t)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(x.root, "xdg"))
	t.Chdir(x.root)
	svc := &Service{
		Name: "demo", Summary: "demo", Version: "0", Builtin: spec, Starter: "# demo starter\ndefault = \"a\"\n[accounts.a]\nbase = \"x\"\n",
		NewClient: func(name string, acct map[string]any) (*Client, error) { return &Client{Auth: NoAuth{}}, nil },
	}
	do := func(args ...string) (int, string) {
		var out, errb bytes.Buffer
		code := RunArgs(svc, args, &out, &errb)
		return code, out.String() + errb.String()
	}
	code, out := do("where")
	if code != 0 || !strings.Contains(out, `"config":null`) || !strings.Contains(out, "agent-tools/config.toml") {
		t.Errorf("where, before init: %d %s", code, out)
	}
	if code, out = do("init"); code != 0 || !strings.Contains(out, `"table":"[demo]"`) {
		t.Fatalf("init: %d %s", code, out)
	}
	shared := filepath.Join(x.root, "xdg", "agent-tools", "config.toml")
	if doc := decodeDoc(t, readFile(t, shared)); doc["demo"] == nil {
		t.Errorf("init must write a [demo] table: %v", doc)
	}
	code, out = do("where")
	if code != 0 || !strings.Contains(out, `"unified":true`) || !strings.Contains(out, `"table":"[demo]"`) {
		t.Errorf("where, after init: %d %s", code, out)
	}
	if code, _ = do("init"); code != 1 {
		t.Errorf("a second init must refuse: %d", code)
	}
	own := filepath.Join(x.root, "own.toml")
	if code, _ = do("init", "--config", own); code != 0 {
		t.Errorf("init --config writes the tool's own file")
	}
	if doc := decodeDoc(t, readFile(t, own)); doc["accounts"] == nil || doc["demo"] != nil {
		t.Errorf("an own-format file has no [demo] table: %v", doc)
	}
	if code, out = do("accounts"); code != 0 || !strings.Contains(out, `"a"`) {
		t.Errorf("accounts from the shared file: %d %s", code, out)
	}
}
