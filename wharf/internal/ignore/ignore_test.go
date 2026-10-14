package ignore

import "testing"

func TestMatch_DirectoryPattern(t *testing.T) {
	m := New([]string{"scripts/", ".git/", "log/"})

	cases := []struct {
		path  string
		isDir bool
		want  bool
	}{
		{"scripts", true, true},
		{"scripts", false, false}, // dirOnly pattern shouldn't match a file
		{".git", true, true},
		{"log", true, true},
		{"images", true, false},
	}
	for _, c := range cases {
		if got := m.Match(c.path, c.isDir); got != c.want {
			t.Errorf("Match(%q, isDir=%v) = %v, want %v", c.path, c.isDir, got, c.want)
		}
	}
}

func TestMatch_FilePattern(t *testing.T) {
	m := New([]string{"CLAUDE.md", "*.log"})

	cases := []struct {
		path  string
		isDir bool
		want  bool
	}{
		{"CLAUDE.md", false, true},
		{"index.html", false, false},
		{"wharf.log", false, true},
		{"scripts/wharf/log/wharf.log", false, true}, // unanchored, matches by basename at any depth
	}
	for _, c := range cases {
		if got := m.Match(c.path, c.isDir); got != c.want {
			t.Errorf("Match(%q, isDir=%v) = %v, want %v", c.path, c.isDir, got, c.want)
		}
	}
}

func TestMatch_AnchoredPattern(t *testing.T) {
	m := New([]string{"images/games/"})

	cases := []struct {
		path  string
		isDir bool
		want  bool
	}{
		{"images/games", true, true},
		{"games", true, false},              // unanchored equivalent would match, this shouldn't
		{"other/images/games", true, false}, // anchored means root-relative match only
	}
	for _, c := range cases {
		if got := m.Match(c.path, c.isDir); got != c.want {
			t.Errorf("Match(%q, isDir=%v) = %v, want %v", c.path, c.isDir, got, c.want)
		}
	}
}

func TestMatch_Comments_And_BlankLines(t *testing.T) {
	m := New([]string{
		"# this is a comment",
		"",
		"   ",
		"docs/",
	})
	if !m.Match("docs", true) {
		t.Error("expected docs/ pattern to still be parsed despite comments and blank lines")
	}
	if len(m.patterns) != 1 {
		t.Errorf("expected 1 parsed pattern, got %d", len(m.patterns))
	}
}

func TestMatch_Negation(t *testing.T) {
	m := New([]string{"*.png", "!keep.png"})

	if !m.Match("foo.png", false) {
		t.Error("expected foo.png to be ignored")
	}
	if m.Match("keep.png", false) {
		t.Error("expected keep.png to be un-ignored by negation")
	}
}

func TestMatch_NoPatterns(t *testing.T) {
	m := New(nil)
	if m.Match("anything.txt", false) {
		t.Error("expected no patterns to mean nothing is ignored")
	}
}

func TestLoad_MissingFile(t *testing.T) {
	m, err := Load("/nonexistent/path/.wharfignore")
	if err != nil {
		t.Fatalf("Load returned error for missing file: %v", err)
	}
	if m.Match("anything.txt", false) {
		t.Error("expected empty matcher from missing file to ignore nothing")
	}
}
