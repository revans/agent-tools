package apikit

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/pelletier/go-toml/v2"
)

// Config is a tool's TOML file: named accounts plus an optional default.
//
//	default = "main"
//	[accounts.main]
//	client_id = "..."
type Config struct {
	Path     string
	Default  string
	Accounts map[string]map[string]any
}

type configFile struct {
	Default  string                    `toml:"default"`
	Accounts map[string]map[string]any `toml:"accounts"`
}

// ConfigPath finds the config file: an explicit path, then $<TOOL>_CONFIG,
// then ~/.config/<tool>/config.toml, then ./<tool>.toml. When none exists it
// returns the user-config path, so `init` knows where to write.
func ConfigPath(tool, explicit string) (path string, exists bool) {
	if explicit != "" {
		_, err := os.Stat(explicit)
		return explicit, err == nil
	}
	if v := os.Getenv(envName(tool, "CONFIG")); v != "" {
		_, err := os.Stat(v)
		return v, err == nil
	}
	var candidates []string
	if dir, err := os.UserConfigDir(); err == nil {
		candidates = append(candidates, filepath.Join(dir, tool, "config.toml"))
	}
	candidates = append(candidates, tool+".toml")
	for _, c := range candidates {
		if _, err := os.Stat(c); err == nil {
			return c, true
		}
	}
	return candidates[0], false
}

func envName(tool, suffix string) string {
	return strings.ToUpper(strings.ReplaceAll(tool, "-", "_")) + "_" + suffix
}

// LoadConfig reads and validates the config. A file readable by other users is
// reported on stderr, because it may hold literal secrets.
func LoadConfig(tool, explicit string) (*Config, error) {
	path, ok := ConfigPath(tool, explicit)
	if !ok {
		return nil, fmt.Errorf("no config file found (looked at %s). Run `%s init` to create one", path, tool)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var cf configFile
	if err := toml.Unmarshal(data, &cf); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	if st, err := os.Stat(path); err == nil && st.Mode().Perm()&0o077 != 0 {
		fmt.Fprintf(os.Stderr, "%s: warning: %s is readable by other users (mode %o); run chmod 600\n", tool, path, st.Mode().Perm())
	}
	return &Config{Path: path, Default: cf.Default, Accounts: cf.Accounts}, nil
}

// Account returns the named account, or the default, or the only one.
func (c *Config) Account(name string) (string, map[string]any, error) {
	if name == "" {
		name = c.Default
	}
	if name == "" && len(c.Accounts) == 1 {
		for k := range c.Accounts {
			name = k
		}
	}
	if name == "" {
		return "", nil, fmt.Errorf("several accounts are configured (%s); pass --account or set default", strings.Join(c.AccountNames(), ", "))
	}
	acct, ok := c.Accounts[name]
	if !ok {
		return "", nil, fmt.Errorf("no account %q in %s (have: %s)", name, c.Path, strings.Join(c.AccountNames(), ", "))
	}
	return name, acct, nil
}

func (c *Config) AccountNames() []string {
	names := make([]string, 0, len(c.Accounts))
	for k := range c.Accounts {
		names = append(names, k)
	}
	sort.Strings(names)
	return names
}

// WriteStarter writes a starter config at mode 0600 and refuses to overwrite.
func WriteStarter(path, content string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		if errors.Is(err, fs.ErrExist) {
			return fmt.Errorf("%s already exists; not overwriting it", path)
		}
		return err
	}
	if _, err := f.WriteString(content); err != nil {
		f.Close()
		return err
	}
	return f.Close()
}
