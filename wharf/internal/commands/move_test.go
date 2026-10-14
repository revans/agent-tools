package commands

import (
	"reflect"
	"testing"
)

func TestRemoteWithin(t *testing.T) {
	cases := []struct {
		root, p string
		want    bool
	}{
		{"/home/u/site", "/home/u/site", true},
		{"/home/u/site", "/home/u/site/a/b.html", true},
		{"/home/u/site", "/home/u/site2/a", false},
		{"/home/u/site", "/home/u", false},
		{"/home/u/site", "/etc/passwd", false},
		{"/", "/anything", true},
	}
	for _, c := range cases {
		if got := remoteWithin(c.root, c.p); got != c.want {
			t.Errorf("remoteWithin(%q, %q) = %v, want %v", c.root, c.p, got, c.want)
		}
	}
}

func TestPlanArchive(t *testing.T) {
	names := []string{"index.html", "css", ".well-known", "archive", "robots.txt", ".htaccess", "js"}
	move, kept := planArchive(names, []string{"robots.txt"})

	if want := []string{"css", "index.html", "js"}; !reflect.DeepEqual(move, want) {
		t.Errorf("move = %v, want %v", move, want)
	}
	var keptNames []string
	for _, k := range kept {
		keptNames = append(keptNames, k.name)
	}
	if want := []string{".htaccess", ".well-known", "archive", "robots.txt"}; !reflect.DeepEqual(keptNames, want) {
		t.Errorf("kept = %v, want %v", keptNames, want)
	}
}

func TestPlanArchiveNothingToMove(t *testing.T) {
	move, _ := planArchive([]string{"archive", ".dh-diag"}, nil)
	if len(move) != 0 {
		t.Errorf("move = %v, want empty", move)
	}
}
