package main

import (
	"embed"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/pelletier/go-toml/v2"
)

//go:embed tools/*.toml
var builtinTools embed.FS

// Safety levels, lowest to highest. A server started with -allow=write
// registers read and write tools and leaves destructive ones out entirely.
const (
	SafetyRead        = "read"
	SafetyWrite       = "write"
	SafetyDestructive = "destructive"
)

var safetyRank = map[string]int{SafetyRead: 0, SafetyWrite: 1, SafetyDestructive: 2}

// Param is one argument the model may pass. It maps to a flag, or to a
// positional argument.
type Param struct {
	Name        string   `toml:"name"`
	Type        string   `toml:"type"` // string | integer | boolean | map
	Description string   `toml:"description"`
	Flag        string   `toml:"flag"`       // long flag, e.g. "--account"; empty for positionals
	Positional  bool     `toml:"positional"` // passed after "--", in manifest order
	Required    bool     `toml:"required"`
	Enum        []string `toml:"enum"`
}

// Tool is one MCP tool: a fixed CLI invocation plus the parameters the model
// is allowed to fill in.
type Tool struct {
	Name        string   `toml:"name"`
	Description string   `toml:"description"`
	Bin         string   `toml:"bin"`
	Args        []string `toml:"args"`    // fixed leading arguments
	Safety      string   `toml:"safety"`  // read | write | destructive
	Timeout     string   `toml:"timeout"` // Go duration, default 60s
	OKExit      []int    `toml:"ok_exit"` // exit codes that are not errors (default [0])
	Params      []Param  `toml:"param"`

	timeout time.Duration
}

type manifestFile struct {
	Tools []Tool `toml:"tool"`
}

var (
	toolNameRe  = regexp.MustCompile(`^[a-z][a-z0-9_]{0,63}$`)
	paramNameRe = regexp.MustCompile(`^[a-z][a-z0-9_]{0,63}$`)
	longFlagRe  = regexp.MustCompile(`^--[a-z][a-z0-9-]*$`)
)

// parseManifest decodes one TOML document and validates every tool in it.
func parseManifest(src string, data []byte) ([]Tool, error) {
	var mf manifestFile
	dec := toml.NewDecoder(strings.NewReader(string(data)))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&mf); err != nil {
		return nil, fmt.Errorf("%s: %w", src, err)
	}
	for i := range mf.Tools {
		if err := mf.Tools[i].validate(); err != nil {
			return nil, fmt.Errorf("%s: tool %q: %w", src, mf.Tools[i].Name, err)
		}
	}
	return mf.Tools, nil
}

func (t *Tool) validate() error {
	if !toolNameRe.MatchString(t.Name) {
		return fmt.Errorf("name must match %s", toolNameRe)
	}
	if strings.TrimSpace(t.Description) == "" {
		return fmt.Errorf("description is required (the model reads it to decide when to call the tool)")
	}
	if t.Bin == "" || strings.ContainsAny(t.Bin, " \t") {
		return fmt.Errorf("bin must be a single program name or path")
	}
	if t.Safety == "" {
		return fmt.Errorf("safety is required (read, write or destructive)")
	}
	if _, ok := safetyRank[t.Safety]; !ok {
		return fmt.Errorf("safety %q is not read, write or destructive", t.Safety)
	}
	t.timeout = 60 * time.Second
	if t.Timeout != "" {
		d, err := time.ParseDuration(t.Timeout)
		if err != nil || d <= 0 {
			return fmt.Errorf("timeout %q is not a positive duration", t.Timeout)
		}
		t.timeout = d
	}
	if len(t.OKExit) == 0 {
		t.OKExit = []int{0}
	}
	seen := map[string]bool{}
	for _, p := range t.Params {
		if !paramNameRe.MatchString(p.Name) {
			return fmt.Errorf("param name %q must match %s", p.Name, paramNameRe)
		}
		if seen[p.Name] {
			return fmt.Errorf("param %q is declared twice", p.Name)
		}
		seen[p.Name] = true
		switch p.Type {
		case "string", "integer", "boolean", "map":
		default:
			return fmt.Errorf("param %q: type %q is not string, integer, boolean or map", p.Name, p.Type)
		}
		if p.Positional {
			if p.Flag != "" {
				return fmt.Errorf("param %q: a positional has no flag", p.Name)
			}
			if p.Type != "string" {
				return fmt.Errorf("param %q: positionals must be strings", p.Name)
			}
		} else if !longFlagRe.MatchString(p.Flag) {
			return fmt.Errorf("param %q: flag %q must be a long flag like --account", p.Name, p.Flag)
		}
		if len(p.Enum) > 0 && p.Type != "string" {
			return fmt.Errorf("param %q: enum is only for strings", p.Name)
		}
		if strings.TrimSpace(p.Description) == "" {
			return fmt.Errorf("param %q: description is required", p.Name)
		}
	}
	return nil
}

// loadTools reads the built-in manifests, then every *.toml in each extra
// directory. Two tools with one name are an error, never a silent override.
func loadTools(extraDirs []string) ([]Tool, error) {
	var all []Tool
	add := func(src string, data []byte) error {
		ts, err := parseManifest(src, data)
		if err != nil {
			return err
		}
		all = append(all, ts...)
		return nil
	}
	entries, err := fs.ReadDir(builtinTools, "tools")
	if err != nil {
		return nil, err
	}
	for _, e := range entries {
		data, err := builtinTools.ReadFile("tools/" + e.Name())
		if err != nil {
			return nil, err
		}
		if err := add("builtin:"+e.Name(), data); err != nil {
			return nil, err
		}
	}
	for _, dir := range extraDirs {
		files, err := filepath.Glob(filepath.Join(dir, "*.toml"))
		if err != nil {
			return nil, err
		}
		sort.Strings(files)
		for _, f := range files {
			data, err := os.ReadFile(f)
			if err != nil {
				return nil, err
			}
			if err := add(f, data); err != nil {
				return nil, err
			}
		}
	}
	seen := map[string]bool{}
	for _, t := range all {
		if seen[t.Name] {
			return nil, fmt.Errorf("tool %q is defined more than once", t.Name)
		}
		seen[t.Name] = true
	}
	return all, nil
}

// inputSchema builds the JSON Schema the model sees. Schemas stay flat
// (strings, integers, booleans, string maps) so weaker tool callers cope.
func (t *Tool) inputSchema() map[string]any {
	props := map[string]any{}
	var required []string
	for _, p := range t.Params {
		s := map[string]any{"description": p.Description}
		switch p.Type {
		case "map":
			s["type"] = "object"
			s["additionalProperties"] = map[string]any{"type": "string"}
		default:
			s["type"] = p.Type
		}
		if len(p.Enum) > 0 {
			s["enum"] = p.Enum
		}
		props[p.Name] = s
		if p.Required {
			required = append(required, p.Name)
		}
	}
	schema := map[string]any{"type": "object", "properties": props, "additionalProperties": false}
	if len(required) > 0 {
		schema["required"] = required
	}
	return schema
}

// buildArgs validates the model's arguments against the manifest and returns
// the argv (without the program name). Flags always use the --flag=value form
// and positionals come after "--", so a value that starts with "-" can never be
// read as a flag.
func (t *Tool) buildArgs(raw json.RawMessage) ([]string, error) {
	in := map[string]json.RawMessage{}
	if len(raw) > 0 && string(raw) != "null" {
		if err := json.Unmarshal(raw, &in); err != nil {
			return nil, fmt.Errorf("arguments must be a JSON object: %w", err)
		}
	}
	known := map[string]bool{}
	for _, p := range t.Params {
		known[p.Name] = true
	}
	for k := range in {
		if !known[k] {
			return nil, fmt.Errorf("unknown argument %q", k)
		}
	}
	argv := append([]string{}, t.Args...)
	var positionals []string
	for _, p := range t.Params {
		v, present := in[p.Name]
		if present && string(v) == "null" {
			present = false
		}
		if !present {
			if p.Required {
				return nil, fmt.Errorf("missing required argument %q", p.Name)
			}
			continue
		}
		switch p.Type {
		case "string":
			var s string
			if err := json.Unmarshal(v, &s); err != nil {
				return nil, fmt.Errorf("argument %q must be a string", p.Name)
			}
			if strings.ContainsRune(s, 0) {
				return nil, fmt.Errorf("argument %q contains a NUL byte", p.Name)
			}
			if len(p.Enum) > 0 && !contains(p.Enum, s) {
				return nil, fmt.Errorf("argument %q must be one of %s", p.Name, strings.Join(p.Enum, ", "))
			}
			if p.Required && s == "" {
				return nil, fmt.Errorf("argument %q must not be empty", p.Name)
			}
			if p.Positional {
				positionals = append(positionals, s)
			} else {
				argv = append(argv, p.Flag+"="+s)
			}
		case "integer":
			var n int64
			if err := json.Unmarshal(v, &n); err != nil {
				return nil, fmt.Errorf("argument %q must be an integer", p.Name)
			}
			argv = append(argv, fmt.Sprintf("%s=%d", p.Flag, n))
		case "boolean":
			var b bool
			if err := json.Unmarshal(v, &b); err != nil {
				return nil, fmt.Errorf("argument %q must be true or false", p.Name)
			}
			if b {
				argv = append(argv, p.Flag)
			}
		case "map":
			var m map[string]string
			if err := json.Unmarshal(v, &m); err != nil {
				return nil, fmt.Errorf("argument %q must be an object of strings", p.Name)
			}
			keys := make([]string, 0, len(m))
			for k := range m {
				keys = append(keys, k)
			}
			sort.Strings(keys)
			for _, k := range keys {
				if k == "" || strings.Contains(k, "=") {
					return nil, fmt.Errorf("argument %q: key %q must be non-empty and contain no '='", p.Name, k)
				}
				argv = append(argv, p.Flag+"="+k+"="+m[k])
			}
		}
	}
	if len(positionals) > 0 {
		argv = append(argv, "--")
		argv = append(argv, positionals...)
	}
	return argv, nil
}

func contains(xs []string, s string) bool {
	for _, x := range xs {
		if x == s {
			return true
		}
	}
	return false
}
