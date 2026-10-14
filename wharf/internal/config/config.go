// Package config loads wharf's connection configuration from the .wharf
// TOML dotfile that lives at the root of the site repo.
package config

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/BurntSushi/toml"
)

// ConfigFileName is the name of wharf's dotfile, expected at the
// site repo root.
const ConfigFileName = ".wharf"

// Config holds the SFTP connection details read from .wharf.
type Config struct {
	Host           string `toml:"host"`
	Port           int    `toml:"port"`
	Username       string `toml:"username"`
	Password       string `toml:"password"`
	RemoteBasePath string `toml:"remote_base_path"`

	// BaseURL is the public URL the remote base path is served at, e.g.
	// "https://example.com". Optional: only `wharf verify` uses it, and it
	// can be overridden with --base-url.
	BaseURL string `toml:"base_url"`
}

// Validate checks that every required field was actually populated. TOML
// parsing does not fail on missing keys, so this catches an incomplete
// .wharf file with a clear error instead of a confusing connection failure.
func (c *Config) Validate() error {
	var missing []string
	if c.Host == "" {
		missing = append(missing, "host")
	}
	if c.Port == 0 {
		missing = append(missing, "port")
	}
	if c.Username == "" {
		missing = append(missing, "username")
	}
	if c.Password == "" {
		missing = append(missing, "password")
	}
	if c.RemoteBasePath == "" {
		missing = append(missing, "remote_base_path")
	}
	if len(missing) > 0 {
		return fmt.Errorf("%s is missing required field(s): %v", ConfigFileName, missing)
	}
	return nil
}

// Load reads and parses the .wharf file located directly inside repoRoot.
func Load(repoRoot string) (*Config, error) {
	path := filepath.Join(repoRoot, ConfigFileName)
	var cfg Config
	if _, err := toml.DecodeFile(path, &cfg); err != nil {
		return nil, fmt.Errorf("reading %s: %w", path, err)
	}
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	return &cfg, nil
}

// FindRepoRoot searches startDir and its ancestors for a directory
// containing a .wharf file, the same way git searches for .git. This lets
// wharf be invoked from anywhere inside the site checkout
// (including from within scripts/wharf itself) and still resolve the
// correct repo root.
func FindRepoRoot(startDir string) (string, error) {
	dir, err := filepath.Abs(startDir)
	if err != nil {
		return "", fmt.Errorf("resolving absolute path for %s: %w", startDir, err)
	}
	for {
		candidate := filepath.Join(dir, ConfigFileName)
		if info, err := os.Stat(candidate); err == nil && !info.IsDir() {
			return dir, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", fmt.Errorf("no %s file found in %s or any parent directory (copy .wharf.example to .wharf at the repo root and fill in real values)", ConfigFileName, startDir)
		}
		dir = parent
	}
}
