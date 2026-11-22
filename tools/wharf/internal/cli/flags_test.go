package cli

import (
	"reflect"
	"testing"
)

func TestParse(t *testing.T) {
	allowed := map[string]string{"--dry-run": Bool, "-r": Bool, "--base-url": Value}
	p, err := Parse([]string{"644", "--dry-run", "a/b", "--base-url", "https://x.dev", "-r"}, allowed)
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"644", "a/b"}; !reflect.DeepEqual(p.Args, want) {
		t.Errorf("Args = %v, want %v", p.Args, want)
	}
	if !p.Bools["--dry-run"] || !p.Bools["-r"] {
		t.Errorf("Bools = %v", p.Bools)
	}
	if p.Vals["--base-url"] != "https://x.dev" {
		t.Errorf("Vals = %v", p.Vals)
	}
}

func TestParseEqualsAndDoubleDash(t *testing.T) {
	p, err := Parse([]string{"--base-url=https://x.dev", "--", "--weird-name"}, map[string]string{"--base-url": Value})
	if err != nil {
		t.Fatal(err)
	}
	if p.Vals["--base-url"] != "https://x.dev" || !reflect.DeepEqual(p.Args, []string{"--weird-name"}) {
		t.Errorf("got %+v", p)
	}
}

func TestParseErrors(t *testing.T) {
	allowed := map[string]string{"--dry-run": Bool, "--base-url": Value}
	for _, args := range [][]string{
		{"--dry-rn"},
		{"--base-url"},
		{"--dry-run=yes"},
	} {
		if _, err := Parse(args, allowed); err == nil {
			t.Errorf("Parse(%v) succeeded, want error", args)
		}
	}
}
