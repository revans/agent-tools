package apikit

import (
	"fmt"
	"strconv"
	"strings"
)

type tok struct {
	kind byte // 'k' key, '*' every element, 'i' index
	key  string
	idx  int
}

// parsePath reads paths like data.children[*].data or items[0].id.
func parsePath(p string) ([]tok, error) {
	var out []tok
	if p == "" {
		return nil, nil
	}
	for _, part := range strings.Split(p, ".") {
		if part == "" {
			return nil, fmt.Errorf("path %q has an empty segment", p)
		}
		name := part
		rest := ""
		if i := strings.IndexByte(part, '['); i >= 0 {
			name, rest = part[:i], part[i:]
		}
		if name != "" {
			out = append(out, tok{kind: 'k', key: name})
		}
		for rest != "" {
			if rest[0] != '[' {
				return nil, fmt.Errorf("path %q: unexpected %q", p, rest)
			}
			end := strings.IndexByte(rest, ']')
			if end < 0 {
				return nil, fmt.Errorf("path %q: missing ]", p)
			}
			inner := rest[1:end]
			rest = rest[end+1:]
			if inner == "*" {
				out = append(out, tok{kind: '*'})
				continue
			}
			n, err := strconv.Atoi(inner)
			if err != nil || n < 0 {
				return nil, fmt.Errorf("path %q: [%s] is not * or an index", p, inner)
			}
			out = append(out, tok{kind: 'i', idx: n})
		}
	}
	return out, nil
}

// Select returns the part of a decoded JSON value that a path names. A missing
// key gives nil, not an error, so an optional field simply drops out.
func Select(v any, path string) (any, error) {
	toks, err := parsePath(path)
	if err != nil {
		return nil, err
	}
	return walk(v, toks), nil
}

func walk(v any, toks []tok) any {
	if len(toks) == 0 {
		return v
	}
	t, rest := toks[0], toks[1:]
	switch t.kind {
	case 'k':
		m, ok := v.(map[string]any)
		if !ok {
			return nil
		}
		return walk(m[t.key], rest)
	case 'i':
		l, ok := v.([]any)
		if !ok || t.idx >= len(l) {
			return nil
		}
		return walk(l[t.idx], rest)
	default:
		l, ok := v.([]any)
		if !ok {
			return nil
		}
		out := make([]any, 0, len(l))
		for _, e := range l {
			if r := walk(e, rest); r != nil {
				out = append(out, r)
			}
		}
		return out
	}
}

// Project keeps only the named fields of an object, or of each object in a list.
// A field is "name" (a top-level key, or the last segment of a dotted path) or
// "alias:path.to.value".
func Project(v any, fields []string) (any, error) {
	if len(fields) == 0 {
		return v, nil
	}
	type spec struct{ alias, path string }
	specs := make([]spec, 0, len(fields))
	for _, f := range fields {
		alias, path := f, f
		if i := strings.IndexByte(f, ':'); i > 0 {
			alias, path = f[:i], f[i+1:]
		} else if i := strings.LastIndexByte(f, '.'); i >= 0 {
			alias = f[i+1:]
		}
		if _, err := parsePath(path); err != nil {
			return nil, err
		}
		specs = append(specs, spec{alias, path})
	}
	one := func(m map[string]any) map[string]any {
		out := make(map[string]any, len(specs))
		for _, s := range specs {
			if val, _ := Select(m, s.path); val != nil {
				out[s.alias] = val
			}
		}
		return out
	}
	switch x := v.(type) {
	case map[string]any:
		return one(x), nil
	case []any:
		out := make([]any, 0, len(x))
		for _, e := range x {
			if m, ok := e.(map[string]any); ok {
				out = append(out, one(m))
			} else {
				out = append(out, e)
			}
		}
		return out, nil
	}
	return v, nil
}
