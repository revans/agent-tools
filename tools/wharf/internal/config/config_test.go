package config

import (
	"os"
	"path/filepath"
	"testing"
)

func writeWharfFile(t *testing.T, dir, contents string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, ConfigFileName), []byte(contents), 0o600); err != nil {
		t.Fatalf("writing .wharf: %v", err)
	}
}

func TestLoad_Valid(t *testing.T) {
	dir := t.TempDir()
	writeWharfFile(t, dir, `
host = "sftp.example.com"
port = 22
username = "deploy"
password = "s3cret"
remote_base_path = "/var/www/example.com"
`)

	cfg, err := Load(dir)
	if err != nil {
		t.Fatalf("Load returned error: %v", err)
	}
	if cfg.Host != "sftp.example.com" {
		t.Errorf("Host = %q, want sftp.example.com", cfg.Host)
	}
	if cfg.Port != 22 {
		t.Errorf("Port = %d, want 22", cfg.Port)
	}
	if cfg.Username != "deploy" {
		t.Errorf("Username = %q, want deploy", cfg.Username)
	}
	if cfg.Password != "s3cret" {
		t.Errorf("Password = %q, want s3cret", cfg.Password)
	}
	if cfg.RemoteBasePath != "/var/www/example.com" {
		t.Errorf("RemoteBasePath = %q, want /var/www/example.com", cfg.RemoteBasePath)
	}
}

func TestLoad_MissingFile(t *testing.T) {
	dir := t.TempDir()
	if _, err := Load(dir); err == nil {
		t.Fatal("expected error for missing .wharf file, got nil")
	}
}

func TestLoad_MissingRequiredField(t *testing.T) {
	dir := t.TempDir()
	writeWharfFile(t, dir, `
host = "sftp.example.com"
port = 22
username = "deploy"
remote_base_path = "/var/www/example.com"
`) // password omitted

	if _, err := Load(dir); err == nil {
		t.Fatal("expected error for missing password field, got nil")
	}
}

func TestLoad_InvalidTOML(t *testing.T) {
	dir := t.TempDir()
	writeWharfFile(t, dir, `this is not valid toml =====`)

	if _, err := Load(dir); err == nil {
		t.Fatal("expected error for invalid TOML, got nil")
	}
}

func TestFindRepoRoot(t *testing.T) {
	root := t.TempDir()
	writeWharfFile(t, root, `host = "x"`)

	nested := filepath.Join(root, "scripts", "wharf", "internal")
	if err := os.MkdirAll(nested, 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}

	found, err := FindRepoRoot(nested)
	if err != nil {
		t.Fatalf("FindRepoRoot returned error: %v", err)
	}

	wantRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		t.Fatalf("EvalSymlinks(root): %v", err)
	}
	gotRoot, err := filepath.EvalSymlinks(found)
	if err != nil {
		t.Fatalf("EvalSymlinks(found): %v", err)
	}
	if gotRoot != wantRoot {
		t.Errorf("FindRepoRoot = %q, want %q", gotRoot, wantRoot)
	}
}

func TestFindRepoRoot_NotFound(t *testing.T) {
	dir := t.TempDir()
	if _, err := FindRepoRoot(dir); err == nil {
		t.Fatal("expected error when no .wharf file exists in any ancestor, got nil")
	}
}
