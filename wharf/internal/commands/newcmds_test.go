package commands

import (
	"os"
	"reflect"
	"testing"
)

func TestParseMode(t *testing.T) {
	good := map[string]os.FileMode{"644": 0o644, "0755": 0o755, "600": 0o600}
	for in, want := range good {
		got, err := parseMode(in)
		if err != nil || got != want {
			t.Errorf("parseMode(%q) = %v, %v; want %v", in, got, err, want)
		}
	}
	for _, in := range []string{"", "64", "6444", "2755", "abc", "888", "u+x", "07777"} {
		if _, err := parseMode(in); err == nil {
			t.Errorf("parseMode(%q) succeeded, want error", in)
		}
	}
}

func TestDirMode(t *testing.T) {
	cases := map[os.FileMode]os.FileMode{0o644: 0o755, 0o640: 0o750, 0o600: 0o700, 0o444: 0o555, 0o000: 0o000}
	for in, want := range cases {
		if got := dirMode(in); got != want {
			t.Errorf("dirMode(%04o) = %04o, want %04o", in, got, want)
		}
	}
}

func TestFindOrphans(t *testing.T) {
	remote := []string{"index.html", "city.js", "js/city.js", "archive/2026/index.html", ".well-known/x", "old/a.png"}
	local := map[string]bool{"index.html": true, "js/city.js": true}
	orphans, skipped := findOrphans(remote, local)
	if want := []string{"city.js", "old/a.png"}; !reflect.DeepEqual(orphans, want) {
		t.Errorf("orphans = %v, want %v", orphans, want)
	}
	if skipped["archive"] != 1 || skipped[".well-known"] != 1 {
		t.Errorf("skipped = %v", skipped)
	}
}

func TestMatchBackupDirs(t *testing.T) {
	names := []string{
		"20261014-122645-home-u-site-index.html",
		"20261013-101010-home-u-site-index.html",
		"20261014-122645-home-u-site-css-index.html",
		"20261014-122645-xhome-u-site-index.html",
		"notabackup",
	}
	got := matchBackupDirs(names, "home-u-site-index.html")
	want := []string{"20261013-101010-home-u-site-index.html", "20261014-122645-home-u-site-index.html"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
}

func TestVerifyURL(t *testing.T) {
	if got := verifyURL("https://x.dev/", "fonts/my font.woff2"); got != "https://x.dev/fonts/my%20font.woff2" {
		t.Errorf("got %s", got)
	}
}

func TestVerifyTargets(t *testing.T) {
	cands := []localCandidate{{relPath: "index.html", size: 10}, {relPath: "docs/index.html", size: 5}, {relPath: "a.css", size: 1}}
	var urls []string
	for _, tg := range verifyTargets("https://x.dev", cands) {
		urls = append(urls, tg.url)
	}
	want := []string{
		"https://x.dev/index.html", "https://x.dev/",
		"https://x.dev/docs/index.html", "https://x.dev/docs/",
		"https://x.dev/a.css",
	}
	if !reflect.DeepEqual(urls, want) {
		t.Errorf("got %v, want %v", urls, want)
	}
}

func TestIsBinary(t *testing.T) {
	if isBinary([]byte("hello\n")) || !isBinary([]byte{'a', 0, 'b'}) {
		t.Error("isBinary misclassified")
	}
}
