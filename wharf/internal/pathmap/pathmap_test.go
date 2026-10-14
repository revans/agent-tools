package pathmap

import (
	"os"
	"path/filepath"
	"testing"
)

func TestRepoRelative(t *testing.T) {
	root := t.TempDir()
	nestedDir := filepath.Join(root, "assets")
	if err := os.MkdirAll(nestedDir, 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	localFile := filepath.Join(nestedDir, "foo.png")
	if err := os.WriteFile(localFile, []byte("x"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	got, err := RepoRelative(root, localFile)
	if err != nil {
		t.Fatalf("RepoRelative returned error: %v", err)
	}
	if want := "assets/foo.png"; got != want {
		t.Errorf("RepoRelative = %q, want %q", got, want)
	}
}

func TestRepoRelative_RelativeInput(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "assets"), 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	localFile := filepath.Join(root, "assets", "foo.png")
	if err := os.WriteFile(localFile, []byte("x"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	cwd, err := os.Getwd()
	if err != nil {
		t.Fatalf("Getwd: %v", err)
	}
	defer os.Chdir(cwd)

	if err := os.Chdir(root); err != nil {
		t.Fatalf("Chdir: %v", err)
	}

	got, err := RepoRelative(".", filepath.Join("assets", "foo.png"))
	if err != nil {
		t.Fatalf("RepoRelative returned error: %v", err)
	}
	if want := "assets/foo.png"; got != want {
		t.Errorf("RepoRelative = %q, want %q", got, want)
	}
}

func TestRepoRelative_OutsideRoot(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	outsideFile := filepath.Join(outside, "foo.png")
	if err := os.WriteFile(outsideFile, []byte("x"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	if _, err := RepoRelative(root, outsideFile); err == nil {
		t.Fatal("expected error for a path outside the repo root, got nil")
	}
}

func TestRepoRelative_RootItself(t *testing.T) {
	root := t.TempDir()
	if _, err := RepoRelative(root, root); err == nil {
		t.Fatal("expected error when localPath equals repoRoot, got nil")
	}
}

func TestRemotePath(t *testing.T) {
	tests := []struct {
		name           string
		remoteBasePath string
		repoRelPath    string
		want           string
	}{
		{"simple", "/var/www/example.com", "assets/foo.png", "/var/www/example.com/assets/foo.png"},
		{"base with trailing slash", "/var/www/example.com/", "assets/foo.png", "/var/www/example.com/assets/foo.png"},
		{"nested rel path", "/home/deploy/site", "images/games/thumb.png", "/home/deploy/site/images/games/thumb.png"},
		{"root remote base", "/", "index.html", "/index.html"},
		{"rel path with leading slash", "/var/www/site", "/index.html", "/var/www/site/index.html"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := RemotePath(tt.remoteBasePath, tt.repoRelPath)
			if got != tt.want {
				t.Errorf("RemotePath(%q, %q) = %q, want %q", tt.remoteBasePath, tt.repoRelPath, got, tt.want)
			}
		})
	}
}
