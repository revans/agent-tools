package main

import (
	"bytes"
	_ "embed"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/pelletier/go-toml/v2"
)

//go:embed profiles.toml
var builtinProfiles []byte

// Profile describes what one platform accepts. Zero means "no limit".
type Profile struct {
	Name        string   `toml:"name" json:"name"`
	Description string   `toml:"description" json:"description"`
	Formats     []string `toml:"formats" json:"formats"`
	MaxBytes    int64    `toml:"max_bytes" json:"max_bytes"`
	TargetBytes int64    `toml:"target_bytes" json:"target_bytes,omitempty"`
	MinWidth    int      `toml:"min_width" json:"min_width"`
	MaxWidth    int      `toml:"max_width" json:"max_width"`
	MinHeight   int      `toml:"min_height" json:"min_height"`
	MaxHeight   int      `toml:"max_height" json:"max_height"`
	MinAspect   float64  `toml:"min_aspect" json:"min_aspect"`
	MaxAspect   float64  `toml:"max_aspect" json:"max_aspect"`
	Source      string   `toml:"source" json:"source"`
	// Origin is "builtin" or the file the profile came from.
	Origin string `toml:"-" json:"origin"`
}

type profileFile struct {
	Profile []Profile `toml:"profile"`
}

var knownFormats = map[string]bool{"jpeg": true, "png": true, "gif": true, "webp": true}

func (p *Profile) validate() error {
	if strings.TrimSpace(p.Name) == "" {
		return fmt.Errorf("a profile has no name")
	}
	if len(p.Formats) == 0 {
		return fmt.Errorf("profile %q: formats is empty", p.Name)
	}
	for i, f := range p.Formats {
		f = strings.ToLower(f)
		if f == "jpg" {
			f = "jpeg"
		}
		if !knownFormats[f] {
			return fmt.Errorf("profile %q: unknown format %q (use jpeg, png, gif or webp)", p.Name, f)
		}
		p.Formats[i] = f
	}
	if p.MaxBytes < 0 || p.TargetBytes < 0 || p.MinWidth < 0 || p.MaxWidth < 0 ||
		p.MinHeight < 0 || p.MaxHeight < 0 || p.MinAspect < 0 || p.MaxAspect < 0 {
		return fmt.Errorf("profile %q: limits cannot be negative", p.Name)
	}
	if p.MaxWidth > 0 && p.MinWidth > p.MaxWidth {
		return fmt.Errorf("profile %q: min_width is larger than max_width", p.Name)
	}
	if p.MaxHeight > 0 && p.MinHeight > p.MaxHeight {
		return fmt.Errorf("profile %q: min_height is larger than max_height", p.Name)
	}
	if p.MaxAspect > 0 && p.MinAspect > p.MaxAspect {
		return fmt.Errorf("profile %q: min_aspect is larger than max_aspect", p.Name)
	}
	if p.MaxBytes > 0 && p.TargetBytes > p.MaxBytes {
		return fmt.Errorf("profile %q: target_bytes is larger than max_bytes", p.Name)
	}
	return nil
}

func parseProfiles(data []byte, origin string) ([]Profile, error) {
	var pf profileFile
	dec := toml.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&pf); err != nil {
		return nil, fmt.Errorf("%s: %v", origin, err)
	}
	for i := range pf.Profile {
		if err := pf.Profile[i].validate(); err != nil {
			return nil, fmt.Errorf("%s: %v", origin, err)
		}
		pf.Profile[i].Origin = origin
	}
	return pf.Profile, nil
}

// profileDirs lists the directories extra profiles are read from.
func profileDirs() []string {
	var dirs []string
	base := os.Getenv("XDG_CONFIG_HOME")
	if base == "" {
		if home, err := os.UserHomeDir(); err == nil {
			base = filepath.Join(home, ".config")
		}
	}
	if base != "" {
		dirs = append(dirs, filepath.Join(base, "agent-tools", "media", "profiles.d"))
	}
	for _, d := range filepath.SplitList(os.Getenv("MEDIA_PROFILES")) {
		if d != "" {
			dirs = append(dirs, d)
		}
	}
	return dirs
}

// loadProfiles returns the built-in profiles plus any user profiles, sorted by
// name. A duplicate name or an invalid file is an error.
func loadProfiles() ([]Profile, error) {
	all, err := parseProfiles(builtinProfiles, "builtin")
	if err != nil {
		return nil, err
	}
	seen := map[string]string{}
	for _, p := range all {
		seen[p.Name] = p.Origin
	}
	for _, dir := range profileDirs() {
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
			ps, err := parseProfiles(data, f)
			if err != nil {
				return nil, err
			}
			for _, p := range ps {
				if prev, dup := seen[p.Name]; dup {
					return nil, fmt.Errorf("%s: profile %q is already defined in %s", f, p.Name, prev)
				}
				seen[p.Name] = f
				all = append(all, p)
			}
		}
	}
	sort.SliceStable(all, func(i, j int) bool { return all[i].Name < all[j].Name })
	return all, nil
}

func findProfile(name string) (*Profile, error) {
	all, err := loadProfiles()
	if err != nil {
		return nil, err
	}
	var names []string
	for i := range all {
		if all[i].Name == name {
			return &all[i], nil
		}
		names = append(names, all[i].Name)
	}
	return nil, usageError{fmt.Sprintf("unknown profile %q (known: %s)", name, strings.Join(names, ", "))}
}
