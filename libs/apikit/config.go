package apikit

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"

	"github.com/pelletier/go-toml/v2"
)

// Where a tool's settings live. These rules are the same as the Rust crate
// libs/agent-config, which the notif tools use; keep the two in step.
//
// Every tool reads one shared file, agent-tools.toml, with a table per tool:
//
//	[reddit]
//	default = "main"
//	[reddit.accounts.main]
//	client_id_env = "REDDIT_ID"
//
// A tool's table holds exactly what its own file would hold at the top level.
// Lookup, first match wins:
//
//  1. --config PATH
//  2. $TOOL_CONFIG
//  3. ./agent-tools.toml, if it has the tool's table
//  4. ~/.config/agent-tools/config.toml, if it has the tool's table
//  5. ~/.config/TOOL/config.toml
//  6. ./TOOL.toml
//
// A file with a tool's table in it is the shared file. It may hold every tool's
// credentials, so one that other users can read is refused.

// ProjectFile is the project-level shared file, read from the working directory.
const ProjectFile = "agent-tools.toml"

// Locator says where a lookup looks. NewLocator reads the real environment;
// tests build their own so they never touch the real config.
type Locator struct {
	ConfigHome string // $XDG_CONFIG_HOME, or ~/.config
	ProjectDir string // where ./agent-tools.toml and ./TOOL.toml are looked for
	EnvPath    string // the value of $TOOL_CONFIG, if set
}

// NewLocator returns a Locator for the real environment.
func NewLocator(tool string) Locator {
	l := Locator{ConfigHome: ConfigHome(), ProjectDir: "."}
	if v := os.Getenv(envName(tool, "CONFIG")); v != "" {
		l.EnvPath = expandTilde(v)
	}
	return l
}

// ConfigHome is $XDG_CONFIG_HOME, or ~/.config.
func ConfigHome() string {
	if x := os.Getenv("XDG_CONFIG_HOME"); x != "" {
		return x
	}
	if h, err := os.UserHomeDir(); err == nil {
		return filepath.Join(h, ".config")
	}
	return ".config"
}

// UserConfigPath is the shared user-level file for a config home.
func UserConfigPath(configHome string) string {
	return filepath.Join(configHome, "agent-tools", "config.toml")
}

// LegacyPath is a tool's own older file for a config home.
func LegacyPath(configHome, tool string) string {
	return filepath.Join(configHome, tool, "config.toml")
}

func expandTilde(p string) string {
	if strings.HasPrefix(p, "~/") {
		if h, err := os.UserHomeDir(); err == nil {
			return filepath.Join(h, p[2:])
		}
	}
	return p
}

func envName(tool, suffix string) string {
	return strings.ToUpper(strings.ReplaceAll(tool, "-", "_")) + "_" + suffix
}

// ErrNoConfig is returned (wrapped) when no config file exists anywhere a
// lookup looks. A tool that works without any config can test for it.
var ErrNoConfig = errors.New("no config file found")

// Config is a tool's settings: named accounts plus an optional default.
type Config struct {
	Path     string
	Unified  bool // true when the settings came from the shared file
	Default  string
	Accounts map[string]map[string]any
	// Settings is everything in the tool's table (or the tool's own file), for a
	// tool with keys beyond accounts.
	Settings map[string]any
	tool     string
}

// Dir is the directory a tool keeps its own files in (templates, state). Beside
// its own config file that is the file's directory. In the shared file it is a
// subdirectory named for the tool, so two tools never share one.
func (c *Config) Dir() string {
	parent := filepath.Dir(c.Path)
	if c.Unified {
		return filepath.Join(parent, c.tool)
	}
	return parent
}

// WhereDefined names the file, and the table in it when the file is shared.
func (c *Config) WhereDefined() string {
	if c.Unified {
		return fmt.Sprintf("[%s] in %s", c.tool, c.Path)
	}
	return c.Path
}

// Searched lists every file a lookup consults, in order, for messages.
func (l Locator) Searched(tool string) []string {
	return []string{
		filepath.Join(l.ProjectDir, ProjectFile),
		UserConfigPath(l.ConfigHome),
		LegacyPath(l.ConfigHome, tool),
		filepath.Join(l.ProjectDir, tool+".toml"),
	}
}

// LoadConfig finds and reads a tool's settings using the real environment.
func LoadConfig(tool, explicit string) (*Config, error) {
	return NewLocator(tool).Load(tool, explicit)
}

// Load finds and reads a tool's settings.
func (l Locator) Load(tool, explicit string) (*Config, error) {
	if explicit != "" {
		if _, err := os.Stat(explicit); err != nil {
			return nil, fmt.Errorf("config file not found: %s", explicit)
		}
		return loadFile(tool, explicit)
	}
	if l.EnvPath != "" {
		if _, err := os.Stat(l.EnvPath); err != nil {
			return nil, fmt.Errorf("%s points at a missing file: %s", envName(tool, "CONFIG"), l.EnvPath)
		}
		return loadFile(tool, l.EnvPath)
	}
	// The shared files count only when they have this tool's table.
	for _, cand := range []string{filepath.Join(l.ProjectDir, ProjectFile), UserConfigPath(l.ConfigHome)} {
		if _, err := os.Stat(cand); err == nil {
			c, err := loadFile(tool, cand)
			if err != nil {
				return nil, err
			}
			if c.Unified {
				return c, nil
			}
		}
	}
	for _, cand := range []string{LegacyPath(l.ConfigHome, tool), filepath.Join(l.ProjectDir, tool+".toml")} {
		if _, err := os.Stat(cand); err == nil {
			return loadFile(tool, cand)
		}
	}
	return nil, fmt.Errorf("%w (looked at %s). Run `%s init` to create one",
		ErrNoConfig, strings.Join(l.Searched(tool), ", "), tool)
}

func loadFile(tool, path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var doc map[string]any
	if err := toml.Unmarshal(data, &doc); err != nil {
		return nil, fmt.Errorf("parsing %s: %w", path, err)
	}
	c := &Config{Path: path, tool: tool}
	settings := doc
	if v, ok := doc[tool]; ok {
		t, ok := v.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("%s: `%s` must be a table, as in [%s]", path, tool, tool)
		}
		if err := checkPrivate(path); err != nil {
			return nil, err
		}
		c.Unified, settings = true, t
	}
	if d, ok := settings["default"]; ok {
		s, ok := d.(string)
		if !ok {
			return nil, fmt.Errorf("%s: default must be a string", c.WhereDefined())
		}
		c.Default = s
	}
	c.Settings = settings
	c.Accounts = map[string]map[string]any{}
	if a, ok := settings["accounts"]; ok {
		tbl, ok := a.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("%s: accounts must be a table of tables", c.WhereDefined())
		}
		for name, v := range tbl {
			acct, ok := v.(map[string]any)
			if !ok {
				return nil, fmt.Errorf("%s: account %q must be a table", c.WhereDefined(), name)
			}
			c.Accounts[name] = acct
		}
	}
	return c, nil
}

// checkPrivate refuses a shared file that other users can read.
func checkPrivate(path string) error {
	st, err := os.Stat(path)
	if err != nil {
		return err
	}
	if mode := st.Mode().Perm(); mode&0o077 != 0 {
		return fmt.Errorf("%s is readable by other users (mode %o), and it can hold every tool's credentials. Run: chmod 600 %s", path, mode, path)
	}
	return nil
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
		return "", nil, fmt.Errorf("no account %q in %s (have: %s)", name, c.WhereDefined(), strings.Join(c.AccountNames(), ", "))
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

// ---- nesting and writing ----

// Nest rewrites a tool's own file as the body of its table in the shared file:
// top-level keys move under [tool], and every [x] or [[x]] becomes [tool.x] or
// [[tool.x]]. Comments and layout are kept. The result is parsed and compared
// with the input, so a rewrite that changed the meaning is an error and never
// reaches a file.
func Nest(tool, text string) (string, error) {
	var out strings.Builder
	var inML string // the quote of a multi-line string we are inside, if any
	headerOpen, seenHeader := false, false
	for _, line := range strings.SplitAfter(text, "\n") {
		if line == "" {
			continue
		}
		trimmed := strings.TrimLeft(line, " \t")
		if inML != "" {
			out.WriteString(line)
			if strings.Count(line, inML)%2 == 1 {
				inML = ""
			}
			continue
		}
		if strings.TrimSpace(trimmed) == "" || strings.HasPrefix(trimmed, "#") {
			out.WriteString(line)
			continue
		}
		indent := line[:len(line)-len(trimmed)]
		switch {
		case strings.HasPrefix(trimmed, "[["):
			out.WriteString(indent + "[[" + tool + "." + trimmed[2:])
			seenHeader = true
		case strings.HasPrefix(trimmed, "["):
			out.WriteString(indent + "[" + tool + "." + trimmed[1:])
			seenHeader = true
		default:
			if !seenHeader && !headerOpen {
				out.WriteString("[" + tool + "]\n")
				headerOpen = true
			}
			out.WriteString(line)
			for _, q := range []string{`"""`, `'''`} {
				if strings.Count(line, q)%2 == 1 {
					inML = q
				}
			}
		}
	}
	res := out.String()
	if !strings.HasSuffix(res, "\n") {
		res += "\n"
	}
	var original, nested map[string]any
	if err := toml.Unmarshal([]byte(text), &original); err != nil {
		return "", fmt.Errorf("the original does not parse: %w", err)
	}
	if err := toml.Unmarshal([]byte(res), &nested); err != nil {
		return "", fmt.Errorf("the nested copy does not parse: %w", err)
	}
	expected := map[string]any{}
	if len(original) > 0 {
		expected[tool] = original
	}
	if !reflect.DeepEqual(nested, expected) {
		return "", fmt.Errorf("could not move this file into the shared file safely; add a [%s] table by hand", tool)
	}
	return res, nil
}

func writePrivateAtomic(path, contents string) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	tmp := fmt.Sprintf("%s.tmp%d", path, os.Getpid())
	f, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	_, werr := f.WriteString(contents)
	if cerr := f.Close(); werr == nil {
		werr = cerr
	}
	if werr == nil {
		werr = os.Rename(tmp, path)
	}
	if werr != nil {
		os.Remove(tmp)
		return fmt.Errorf("writing %s: %w", path, werr)
	}
	return nil
}

// AddToShared adds a tool's table to the shared file, creating it (mode 0600)
// if needed. Another tool's tables are left exactly as they were. It refuses,
// changing nothing, when the file already has this tool's table.
func AddToShared(tool, ownFormatText, dest, note string) error {
	nested, err := Nest(tool, ownFormatText)
	if err != nil {
		return err
	}
	var out string
	switch existing, err := os.ReadFile(dest); {
	case err == nil:
		if err := checkPrivate(dest); err != nil {
			return err
		}
		var doc map[string]any
		if err := toml.Unmarshal(existing, &doc); err != nil {
			return fmt.Errorf("parsing %s: %w", dest, err)
		}
		if _, has := doc[tool]; has {
			return fmt.Errorf("%s already has a [%s] table; not changing it", dest, tool)
		}
		out = string(existing)
		if out != "" && !strings.HasSuffix(out, "\n") {
			out += "\n"
		}
		if out != "" {
			out += "\n"
		}
	case errors.Is(err, fs.ErrNotExist):
	default:
		return err
	}
	out += "# " + note + "\n" + nested
	var check map[string]any
	if err := toml.Unmarshal([]byte(out), &check); err != nil {
		return fmt.Errorf("the combined file would not parse; nothing was written: %w", err)
	}
	return writePrivateAtomic(dest, out)
}

// InitShared writes a tool's starter config into the shared file.
func InitShared(tool, starter, dest string) error {
	return AddToShared(tool, starter, dest, tool+": starter settings. Fill in your credentials.")
}

// WriteStarter writes a tool's own starter file at mode 0600 and refuses to overwrite.
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
