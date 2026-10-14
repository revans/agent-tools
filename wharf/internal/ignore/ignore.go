// Package ignore implements a small, gitignore-style pattern matcher used
// to decide which files under the site repo root are
// candidates for `wharf sync`.
//
// Supported syntax (a practical subset of .gitignore):
//   - blank lines and lines starting with '#' are ignored
//   - a leading '!' negates a pattern (a later match un-ignores a path
//     matched by an earlier pattern)
//   - a trailing '/' restricts the pattern to directories
//   - a pattern containing a '/' (other than a trailing one) is anchored
//     to the repo root; a pattern with no '/' matches at any depth
//   - '*', '?' and '[...]' behave as in path.Match (a single path segment)
//
// Not supported: '**' double-star globs. None of wharf's own ignore
// patterns need it, and it isn't worth the added complexity for v1.
package ignore

import (
	"bufio"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"strings"
)

// Matcher holds a parsed set of ignore patterns.
type Matcher struct {
	patterns []pattern
}

type pattern struct {
	glob    string
	negate  bool
	dirOnly bool
	// anchored patterns only match against the full relative path;
	// unanchored patterns also match against the path's base name so they
	// apply at any depth (e.g. "*.log" or "node_modules").
	anchored bool
}

// Load reads a .wharfignore-style file from disk and returns a Matcher. A
// missing file is not an error: it simply produces a Matcher with no
// patterns, so every candidate path is considered.
func Load(path string) (*Matcher, error) {
	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return New(nil), nil
		}
		return nil, fmt.Errorf("opening %s: %w", path, err)
	}
	defer f.Close()

	var lines []string
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		lines = append(lines, scanner.Text())
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("reading %s: %w", path, err)
	}
	return New(lines), nil
}

// New builds a Matcher directly from ignore-file lines. Exposed separately
// from Load so tests (and callers with in-memory content) don't need a
// real file on disk.
func New(lines []string) *Matcher {
	var patterns []pattern
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}

		p := pattern{}
		if strings.HasPrefix(trimmed, "!") {
			p.negate = true
			trimmed = trimmed[1:]
		}
		leadingSlash := strings.HasPrefix(trimmed, "/")
		trimmed = strings.TrimPrefix(trimmed, "/")
		if strings.HasSuffix(trimmed, "/") {
			p.dirOnly = true
			trimmed = strings.TrimSuffix(trimmed, "/")
		}
		if trimmed == "" {
			continue
		}
		p.anchored = leadingSlash || strings.Contains(trimmed, "/")
		p.glob = trimmed
		patterns = append(patterns, p)
	}
	return &Matcher{patterns: patterns}
}

// Match reports whether relPath (slash-separated, relative to the repo
// root, no leading slash) should be ignored. isDir must reflect whether
// relPath refers to a directory, since directory-only patterns (those
// ending in '/') only match directories.
//
// As in gitignore, later patterns win over earlier ones, which is what
// makes negation ("!keep-me.txt") useful.
func (m *Matcher) Match(relPath string, isDir bool) bool {
	relPath = strings.TrimPrefix(path.Clean(filepath.ToSlash(relPath)), "./")
	base := path.Base(relPath)

	matched := false
	for _, p := range m.patterns {
		if p.dirOnly && !isDir {
			continue
		}
		if matchesGlob(p, relPath, base) {
			matched = !p.negate
		}
	}
	return matched
}

func matchesGlob(p pattern, relPath, base string) bool {
	if ok, _ := path.Match(p.glob, relPath); ok {
		return true
	}
	if !p.anchored {
		if ok, _ := path.Match(p.glob, base); ok {
			return true
		}
	}
	return false
}
