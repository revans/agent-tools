package apikit

import (
	"encoding/json"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/pelletier/go-toml/v2"
)

// Safety levels, lowest to highest.
const (
	SafetyRead        = "read"
	SafetyWrite       = "write"
	SafetyDestructive = "destructive"
)

var safetyRank = map[string]int{SafetyRead: 0, SafetyWrite: 1, SafetyDestructive: 2}

// SafetyRank returns a level's order, or -1 for an unknown level.
func SafetyRank(s string) int {
	if r, ok := safetyRank[s]; ok {
		return r
	}
	return -1
}

// Param is one input to an endpoint.
type Param struct {
	Name        string   `toml:"name"`
	Type        string   `toml:"type"` // string | integer | number | boolean | list | json
	In          string   `toml:"in"`   // path | query | header | body | form
	Wire        string   `toml:"wire"` // the name sent on the wire, if different from Name
	Description string   `toml:"description"`
	Required    bool     `toml:"required"`
	Default     string   `toml:"default"`
	Enum        []string `toml:"enum"`
}

// Paginate says how to follow pages. By default the response holds a cursor
// (cursor_path) that goes back in a parameter (cursor_param). With kind = "page"
// the response holds the current page number (cursor_path) and the total number
// of pages (total_path), and the next request asks for the page after this one,
// until the last, as Cloudflare's lists do.
type Paginate struct {
	Kind        string `toml:"kind"`         // "cursor" (default) or "page"
	CursorPath  string `toml:"cursor_path"`  // where the next cursor (or the current page number) sits in the response
	CursorParam string `toml:"cursor_param"` // the query parameter that carries it back
	TotalPath   string `toml:"total_path"`   // page kind only: where the total number of pages sits
	MaxPages    int    `toml:"max_pages"`    // ceiling for --pages (default 10)
}

// Endpoint is one API call, described in data.
type Endpoint struct {
	Name        string            `toml:"name"`
	Description string            `toml:"description"`
	Method      string            `toml:"method"`
	Path        string            `toml:"path"` // may hold {placeholders} filled from "path" params
	Safety      string            `toml:"safety"`
	Base        string            `toml:"base"`    // optional base URL override for this endpoint
	Body        string            `toml:"body"`    // json | form | none (default: json for non-GET params in body)
	Select      string            `toml:"select"`  // path into the response to return, e.g. data.children[*].data
	Fields      []string          `toml:"fields"`  // keep only these keys of each selected object ("alias:path" allowed)
	Timeout     string            `toml:"timeout"` // default 30s
	Fixed       map[string]string `toml:"fixed"`   // constant query parameters
	Headers     map[string]string `toml:"headers"` // constant request headers
	Paginate    *Paginate         `toml:"paginate"`
	Params      []Param           `toml:"param"`
	Source      string            `toml:"-"` // which file defined it

	timeout time.Duration
}

type specFile struct {
	Endpoints []Endpoint `toml:"endpoint"`
}

var (
	nameRe  = regexp.MustCompile(`^[a-z][a-z0-9_]{0,63}$`)
	placeRe = regexp.MustCompile(`\{([a-z][a-z0-9_]*)\}`)
)

// ParseSpecs decodes one TOML document of endpoints and validates each.
func ParseSpecs(source string, data []byte) ([]Endpoint, error) {
	var sf specFile
	dec := toml.NewDecoder(strings.NewReader(string(data)))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&sf); err != nil {
		return nil, fmt.Errorf("%s: %w", source, err)
	}
	for i := range sf.Endpoints {
		e := &sf.Endpoints[i]
		e.Source = source
		if err := e.validate(); err != nil {
			return nil, fmt.Errorf("%s: endpoint %q: %w", source, e.Name, err)
		}
	}
	return sf.Endpoints, nil
}

func (e *Endpoint) validate() error {
	if !nameRe.MatchString(e.Name) {
		return fmt.Errorf("name must match %s", nameRe)
	}
	if strings.TrimSpace(e.Description) == "" {
		return fmt.Errorf("description is required")
	}
	e.Method = strings.ToUpper(e.Method)
	switch e.Method {
	case "GET", "POST", "PUT", "PATCH", "DELETE":
	default:
		return fmt.Errorf("method %q is not GET, POST, PUT, PATCH or DELETE", e.Method)
	}
	if !strings.HasPrefix(e.Path, "/") && !strings.HasPrefix(e.Path, "http") {
		return fmt.Errorf("path must start with / (or be a full URL)")
	}
	if e.Safety == "" {
		return fmt.Errorf("safety is required (read, write or destructive)")
	}
	if SafetyRank(e.Safety) < 0 {
		return fmt.Errorf("safety %q is not read, write or destructive", e.Safety)
	}
	if e.Method != "GET" && e.Safety == SafetyRead {
		return fmt.Errorf("a %s endpoint cannot be safety = read", e.Method)
	}
	switch e.Body {
	case "", "json", "form", "none":
	default:
		return fmt.Errorf("body %q is not json, form or none", e.Body)
	}
	e.timeout = 30 * time.Second
	if e.Timeout != "" {
		d, err := time.ParseDuration(e.Timeout)
		if err != nil || d <= 0 {
			return fmt.Errorf("timeout %q is not a positive duration", e.Timeout)
		}
		e.timeout = d
	}
	if e.Paginate != nil {
		if e.Paginate.CursorPath == "" || e.Paginate.CursorParam == "" {
			return fmt.Errorf("paginate needs cursor_path and cursor_param")
		}
		switch e.Paginate.Kind {
		case "", "cursor":
			if e.Paginate.TotalPath != "" {
				return fmt.Errorf("paginate total_path is only for kind = \"page\"")
			}
		case "page":
			if e.Paginate.TotalPath == "" {
				return fmt.Errorf("paginate kind \"page\" needs total_path")
			}
		default:
			return fmt.Errorf("paginate kind %q is not cursor or page", e.Paginate.Kind)
		}
	}
	seen := map[string]bool{}
	for i := range e.Params {
		p := &e.Params[i]
		if !nameRe.MatchString(p.Name) {
			return fmt.Errorf("param name %q must match %s", p.Name, nameRe)
		}
		if seen[p.Name] {
			return fmt.Errorf("param %q is declared twice", p.Name)
		}
		seen[p.Name] = true
		switch p.Type {
		case "":
			p.Type = "string"
		case "string", "integer", "number", "boolean", "list", "json":
		default:
			return fmt.Errorf("param %q: type %q is not string, integer, number, boolean, list or json", p.Name, p.Type)
		}
		switch p.In {
		case "":
			p.In = "query"
		case "path", "query", "header", "body", "form":
		default:
			return fmt.Errorf("param %q: in %q is not path, query, header, body or form", p.Name, p.In)
		}
		if p.In == "path" && !p.Required {
			return fmt.Errorf("param %q: a path parameter must be required", p.Name)
		}
		if p.Type == "json" && p.In != "body" {
			return fmt.Errorf("param %q: json parameters belong in the body", p.Name)
		}
		if strings.TrimSpace(p.Description) == "" {
			return fmt.Errorf("param %q: description is required", p.Name)
		}
		if len(p.Enum) > 0 && p.Type != "string" {
			return fmt.Errorf("param %q: enum is only for strings", p.Name)
		}
		if p.Wire == "" {
			p.Wire = p.Name
		}
	}
	if e.Paginate != nil && !seen[e.Paginate.CursorParam] {
		return fmt.Errorf("paginate cursor_param %q is not a declared param", e.Paginate.CursorParam)
	}
	for _, m := range placeRe.FindAllStringSubmatch(e.Path, -1) {
		found := false
		for _, p := range e.Params {
			if p.Name == m[1] && p.In == "path" {
				found = true
			}
		}
		if !found {
			return fmt.Errorf("path placeholder {%s} has no matching path param", m[1])
		}
	}
	return nil
}

// Registry is the set of endpoints a tool knows.
type Registry struct {
	byName map[string]*Endpoint
}

// LoadRegistry reads the built-in spec, then every *.toml in each extra
// directory. A duplicate name is an error, never a silent override.
func LoadRegistry(builtin string, dirs []string) (*Registry, error) {
	r := &Registry{byName: map[string]*Endpoint{}}
	add := func(source string, data []byte) error {
		eps, err := ParseSpecs(source, data)
		if err != nil {
			return err
		}
		for i := range eps {
			if prev, dup := r.byName[eps[i].Name]; dup {
				return fmt.Errorf("endpoint %q is defined in both %s and %s", eps[i].Name, prev.Source, eps[i].Source)
			}
			e := eps[i]
			r.byName[e.Name] = &e
		}
		return nil
	}
	if builtin != "" {
		if err := add("builtin", []byte(builtin)); err != nil {
			return nil, err
		}
	}
	for _, d := range dirs {
		files, _ := filepath.Glob(filepath.Join(d, "*.toml"))
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
	return r, nil
}

func (r *Registry) Get(name string) (*Endpoint, bool) {
	e, ok := r.byName[name]
	return e, ok
}

func (r *Registry) Names() []string {
	names := make([]string, 0, len(r.byName))
	for n := range r.byName {
		names = append(names, n)
	}
	sort.Strings(names)
	return names
}

// Request is a fully built HTTP request, before auth is applied.
type Request struct {
	Method  string
	URL     string
	Headers map[string]string
	Body    []byte
	Form    url.Values // set when the body is form-encoded
	// SkipAuth sends the request without credentials, for a pre-signed upload URL.
	SkipAuth bool
}

// Build turns an endpoint and the caller's parameters into a request.
// params carries every value as text; each is checked against the spec.
func (e *Endpoint) Build(base string, params map[string]string) (*Request, error) {
	known := map[string]*Param{}
	for i := range e.Params {
		known[e.Params[i].Name] = &e.Params[i]
	}
	for k := range params {
		if known[k] == nil {
			names := make([]string, 0, len(known))
			for n := range known {
				names = append(names, n)
			}
			sort.Strings(names)
			return nil, fmt.Errorf("endpoint %s has no parameter %q (it takes: %s)", e.Name, k, strings.Join(names, ", "))
		}
	}
	path := e.Path
	query := url.Values{}
	form := url.Values{}
	bodyObj := map[string]any{}
	headers := map[string]string{}
	for k, v := range e.Headers {
		headers[k] = v
	}
	for k, v := range e.Fixed {
		query.Set(k, v)
	}
	for i := range e.Params {
		p := &e.Params[i]
		raw, given := params[p.Name]
		if !given && p.Default != "" {
			raw, given = p.Default, true
		}
		if !given {
			if p.Required {
				return nil, fmt.Errorf("endpoint %s needs parameter %q: %s", e.Name, p.Name, p.Description)
			}
			continue
		}
		val, err := coerce(p, raw)
		if err != nil {
			return nil, fmt.Errorf("endpoint %s, parameter %q: %w", e.Name, p.Name, err)
		}
		switch p.In {
		case "path":
			path = strings.ReplaceAll(path, "{"+p.Name+"}", url.PathEscape(fmt.Sprint(val)))
		case "query":
			for _, s := range queryStrings(val) {
				query.Add(p.Wire, s)
			}
		case "header":
			headers[p.Wire] = fmt.Sprint(val)
		case "form":
			for _, s := range queryStrings(val) {
				form.Add(p.Wire, s)
			}
		case "body":
			bodyObj[p.Wire] = val
		}
	}
	if strings.HasPrefix(path, "http") {
		base = ""
	} else if e.Base != "" {
		base = e.Base
	}
	full := strings.TrimRight(base, "/") + path
	if len(query) > 0 {
		sep := "?"
		if strings.Contains(full, "?") {
			sep = "&"
		}
		full += sep + query.Encode()
	}
	req := &Request{Method: e.Method, URL: full, Headers: headers}
	mode := e.Body
	if mode == "" {
		switch {
		case len(form) > 0:
			mode = "form"
		case len(bodyObj) > 0:
			mode = "json"
		default:
			mode = "none"
		}
	}
	switch mode {
	case "json":
		if len(bodyObj) > 0 || e.Method != "GET" {
			b, err := json.Marshal(bodyObj)
			if err != nil {
				return nil, err
			}
			req.Body = b
			req.Headers["Content-Type"] = "application/json"
		}
	case "form":
		for k, v := range bodyObj {
			form.Set(k, fmt.Sprint(v))
		}
		req.Form = form
		req.Body = []byte(form.Encode())
		req.Headers["Content-Type"] = "application/x-www-form-urlencoded"
	}
	return req, nil
}

// coerce checks a text value against a param's type and returns the typed value.
func coerce(p *Param, raw string) (any, error) {
	switch p.Type {
	case "integer":
		n, err := strconv.ParseInt(raw, 10, 64)
		if err != nil {
			return nil, fmt.Errorf("%q is not an integer", raw)
		}
		return n, nil
	case "number":
		n, err := strconv.ParseFloat(raw, 64)
		if err != nil {
			return nil, fmt.Errorf("%q is not a number", raw)
		}
		return n, nil
	case "boolean":
		b, err := strconv.ParseBool(raw)
		if err != nil {
			return nil, fmt.Errorf("%q is not true or false", raw)
		}
		return b, nil
	case "list":
		var out []string
		for _, s := range strings.Split(raw, ",") {
			if s = strings.TrimSpace(s); s != "" {
				out = append(out, s)
			}
		}
		return out, nil
	case "json":
		var v any
		dec := json.NewDecoder(strings.NewReader(raw))
		dec.UseNumber()
		if err := dec.Decode(&v); err != nil {
			return nil, fmt.Errorf("is not valid JSON: %v", err)
		}
		if _, err := dec.Token(); err != io.EOF {
			return nil, fmt.Errorf("has trailing data after the JSON value")
		}
		return v, nil
	}
	if len(p.Enum) > 0 {
		for _, e := range p.Enum {
			if e == raw {
				return raw, nil
			}
		}
		return nil, fmt.Errorf("must be one of %s", strings.Join(p.Enum, ", "))
	}
	if strings.ContainsRune(raw, 0) {
		return nil, fmt.Errorf("contains a NUL byte")
	}
	return raw, nil
}

func queryStrings(v any) []string {
	if l, ok := v.([]string); ok {
		return []string{strings.Join(l, ",")}
	}
	return []string{fmt.Sprint(v)}
}
