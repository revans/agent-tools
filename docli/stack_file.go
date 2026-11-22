package main

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/digitalocean/godo"
	"gopkg.in/yaml.v3"
)

// The stack file (milestone 3) describes, in one YAML file, the milestone 1 and 2 resources a project needs.
// Parsing is strict on purpose: an unknown key, a duplicate key, a key that looks like a secret, a value that
// looks like a secret or an environment reference is an error that names the line. The file is meant to be
// committed to git, so nothing secret may ever be written in it.

const defaultStackRel = "config/deploys/stack.yml"

// srcPos remembers where something sat in the file so later errors can name the line.
type srcPos struct {
	Line int
	Keys map[string]int
}

func (p srcPos) at(key string) int {
	if l, ok := p.Keys[key]; ok {
		return l
	}
	return p.Line
}

type StackKey struct {
	Name          string `yaml:"name"`
	PublicKeyFile string `yaml:"public_key_file"`
	Pos           srcPos `yaml:"-"`

	line string // the public key line, read from the file during validation
}

type StackVPC struct {
	Name    string `yaml:"name"`
	Region  string `yaml:"region"`
	IPRange string `yaml:"ip_range"`
	Pos     srcPos `yaml:"-"`
}

type StackFirewall struct {
	Name  string   `yaml:"name"`
	Rules []string `yaml:"rules"`
	Pos   srcPos   `yaml:"-"`

	parsed []Rule
}

type StackDroplet struct {
	Name         string   `yaml:"name"`
	Region       string   `yaml:"region"`
	Size         string   `yaml:"size"`
	Image        string   `yaml:"image"`
	VPC          string   `yaml:"vpc"`
	SSHKeys      []string `yaml:"ssh_keys"`
	Firewalls    []string `yaml:"firewalls"`
	Tags         []string `yaml:"tags"`
	Backups      bool     `yaml:"backups"`
	Monitoring   bool     `yaml:"monitoring"`
	IPv6         bool     `yaml:"ipv6"`
	UserDataFile string   `yaml:"user_data_file"`
	Pos          srcPos   `yaml:"-"`

	userData string // read from UserDataFile during validation; never printed
}

// Milestone 4 kinds. Reserved IPs and Spaces are deliberately not here: a reserved IP has no name or tag docli
// could look it up by, and a Spaces key is a credential (see the README, "Stack files").
type StackProject struct {
	Name        string `yaml:"name"`
	Purpose     string `yaml:"purpose"`
	Environment string `yaml:"environment"`
	Description string `yaml:"description"`
	Pos         srcPos `yaml:"-"`
}

type StackVolume struct {
	Name           string   `yaml:"name"`
	Region         string   `yaml:"region"`
	SizeGB         int      `yaml:"size_gb"`
	Description    string   `yaml:"description"`
	FilesystemType string   `yaml:"filesystem_type"`
	Droplet        string   `yaml:"droplet"` // attach it to this droplet once both exist
	Project        string   `yaml:"project"` // put it here when it is created
	Tags           []string `yaml:"tags"`
	Pos            srcPos   `yaml:"-"`
}

type StackLB struct {
	Name            string   `yaml:"name"`
	Region          string   `yaml:"region"`
	Size            string   `yaml:"size"`
	VPC             string   `yaml:"vpc"`
	Algorithm       string   `yaml:"algorithm"`
	ForwardingRules []string `yaml:"forwarding_rules"`
	HealthCheck     string   `yaml:"health_check"`
	Droplets        []string `yaml:"droplets"`
	Tag             string   `yaml:"tag"`
	RedirectHTTP    bool     `yaml:"redirect_http_to_https"`
	Project         string   `yaml:"project"`
	Pos             srcPos   `yaml:"-"`

	rules  []godo.ForwardingRule
	health *godo.HealthCheck
}

type StackRecord struct {
	Type     string `yaml:"type"`
	Name     string `yaml:"name"`
	Data     string `yaml:"data"`
	TTL      int    `yaml:"ttl"`
	Priority *int   `yaml:"priority"`
	Flags    *int   `yaml:"flags"`
	Tag      string `yaml:"tag"`
	Droplet  string `yaml:"droplet"` // an A record whose address is the droplet's public IPv4
	Pos      srcPos `yaml:"-"`

	spec recordSpec
}

type StackDomain struct {
	Name    string        `yaml:"name"`
	Records []StackRecord `yaml:"-"`
	Pos     srcPos        `yaml:"-"`
}

type StackDatabase struct {
	Name            string   `yaml:"name"`
	Region          string   `yaml:"region"`
	Version         string   `yaml:"version"`
	Size            string   `yaml:"size"`
	Nodes           int      `yaml:"nodes"`
	VPC             string   `yaml:"vpc"`
	TrustedDroplets []string `yaml:"trusted_droplets"`
	TrustedTags     []string `yaml:"trusted_tags"`
	TrustedIPs      []string `yaml:"trusted_ips"`
	Users           []string `yaml:"users"`
	Databases       []string `yaml:"databases"`
	Pos             srcPos   `yaml:"-"`
}

// Stack is the parsed and validated file.
type Stack struct {
	Version   int
	Name      string // optional: becomes the tag docli:stack:NAME on droplets and databases
	Region    string // default region
	SSHKeys   []StackKey
	VPCs      []StackVPC
	Firewalls []StackFirewall
	Droplets  []StackDroplet
	Domains   []StackDomain
	Databases []StackDatabase
	Projects  []StackProject
	Volumes   []StackVolume
	LBs       []StackLB
	Notes     []string // things validate cannot decide without the account (references to resources outside the file)
}

type stackErr struct {
	Line int
	Msg  string
}

// stackError is every problem found in a stack file, in line order.
type stackError struct {
	Path string
	Errs []stackErr
}

func (e *stackError) Error() string {
	var b strings.Builder
	fmt.Fprintf(&b, "%s is not a valid stack file (%d problem(s)):", e.Path, len(e.Errs))
	for _, x := range e.Errs {
		if x.Line > 0 {
			fmt.Fprintf(&b, "\n  %s:%d: %s", e.Path, x.Line, x.Msg)
		} else {
			fmt.Fprintf(&b, "\n  %s: %s", e.Path, x.Msg)
		}
	}
	return b.String()
}

type stackParser struct {
	errs []stackErr
}

func (p *stackParser) errf(line int, format string, a ...any) {
	p.errs = append(p.errs, stackErr{line, fmt.Sprintf(format, a...)})
}

var (
	secretKeyRE   = regexp.MustCompile(`(?i)(password|passwd|passphrase|secret|token|api[_-]?key|private[_-]?key|credential|access[_-]?key)`)
	interpolateRE = regexp.MustCompile(`\$\{|\$\(|\$[A-Za-z_]`)
	secretValueRE = regexp.MustCompile(`(?i)(PRIVATE KEY|-----BEGIN|\bdo[por]_v1_[0-9a-f]|\bAVNS_)`)
	yamlLineRE    = regexp.MustCompile(`line (\d+): (.*)`)
)

var topKeys = []string{"version", "name", "region", "ssh_keys", "vpcs", "firewalls", "droplets", "domains", "databases", "projects", "volumes", "load_balancers"}

// notInStack are kinds docli manages with commands but never from a stack file, and why.
var notInStack = map[string]string{
	"reserved_ips":   "a reserved IP has no name and cannot be tagged, so a stack file has nothing to look it up by; use docli reserved-ip",
	"spaces":         "Spaces keys are credentials and never go in a stack file; use docli spaces key and docli spaces bucket",
	"spaces_keys":    "Spaces keys are credentials and never go in a stack file; use docli spaces key",
	"spaces_buckets": "buckets are made through the S3 API with a Spaces key, which a stack file cannot hold; use docli spaces bucket",
}

// scan applies the rules that hold everywhere in the file: no aliases, no duplicate keys, no secret-looking keys
// or values, no environment references.
func (p *stackParser) scan(n *yaml.Node) {
	switch n.Kind {
	case yaml.AliasNode:
		p.errf(n.Line, "YAML aliases are not supported in a stack file; write the value out")
	case yaml.MappingNode:
		seen := map[string]int{}
		for i := 0; i+1 < len(n.Content); i += 2 {
			k := n.Content[i]
			if k.Value == "<<" {
				p.errf(k.Line, "YAML merge keys are not supported in a stack file; write the values out")
			}
			if first, dup := seen[k.Value]; dup {
				p.errf(k.Line, "the key %q appears twice (first on line %d)", k.Value, first)
			}
			seen[k.Value] = k.Line
			if secretKeyRE.MatchString(k.Value) {
				p.errf(k.Line, "the key %q looks like a secret. A stack file is committed to git and never holds passwords, tokens or keys: docli never needs them here (database passwords go only to a 0600 file named with --password-file on `docli db connection`)", k.Value)
			}
			p.scan(n.Content[i+1])
		}
	case yaml.SequenceNode:
		for _, c := range n.Content {
			p.scan(c)
		}
	case yaml.ScalarNode:
		if interpolateRE.MatchString(n.Value) {
			p.errf(n.Line, "%q looks like an environment variable or command reference. docli does not interpolate anything in a stack file, so a secret can never be pulled into it by accident; write the plain value", n.Value)
		} else if secretValueRE.MatchString(n.Value) || connURIRE.MatchString(n.Value) {
			p.errf(n.Line, "this value looks like a secret (a token, password, private key or connection string). A stack file never holds secrets")
		}
	}
}

func mapValue(m *yaml.Node, key string) *yaml.Node {
	for i := 0; i+1 < len(m.Content); i += 2 {
		if m.Content[i].Value == key {
			return m.Content[i+1]
		}
	}
	return nil
}

func posOf(n *yaml.Node) srcPos {
	p := srcPos{Line: n.Line, Keys: map[string]int{}}
	for i := 0; i+1 < len(n.Content); i += 2 {
		p.Keys[n.Content[i].Value] = n.Content[i].Line
	}
	return p
}

// checkKeys reports keys that are not allowed in a mapping.
func (p *stackParser) checkKeys(n *yaml.Node, where string, allowed []string) {
	for i := 0; i+1 < len(n.Content); i += 2 {
		k := n.Content[i]
		if !contains(allowed, k.Value) && !secretKeyRE.MatchString(k.Value) {
			p.errf(k.Line, "unknown key %q in %s (allowed: %s)", k.Value, where, strings.Join(allowed, ", "))
		}
	}
}

func (p *stackParser) typeErrs(err error, fallback int) {
	var te *yaml.TypeError
	if errors.As(err, &te) {
		for _, m := range te.Errors {
			if mm := yamlLineRE.FindStringSubmatch(m); mm != nil {
				l, _ := strconv.Atoi(mm[1])
				p.errf(l, "%s", mm[2])
				continue
			}
			p.errf(fallback, "%s", m)
		}
		return
	}
	p.errf(fallback, "%v", err)
}

// decodeList reads the sequence under key in parent, checking each entry's keys and types.
func decodeList[T any](p *stackParser, parent *yaml.Node, key string, allowed []string, set func(*T, srcPos)) []T {
	seq := mapValue(parent, key)
	if seq == nil {
		return nil
	}
	if seq.Kind == yaml.ScalarNode && seq.Tag == "!!null" {
		return nil
	}
	if seq.Kind != yaml.SequenceNode {
		p.errf(seq.Line, "%s must be a list", key)
		return nil
	}
	var out []T
	for _, it := range seq.Content {
		if it.Kind != yaml.MappingNode {
			p.errf(it.Line, "each entry in %s must be a mapping of keys to values", key)
			continue
		}
		p.checkKeys(it, key, allowed)
		var v T
		if err := it.Decode(&v); err != nil {
			p.typeErrs(err, it.Line)
			continue
		}
		set(&v, posOf(it))
		out = append(out, v)
	}
	return out
}

// parseStack parses and validates a stack file's text. display is the name shown in errors. defRegion is the
// region from --region or DOCLI_REGION. readFile reads files the stack names (public keys, user data).
func parseStack(data []byte, display, defRegion string, readFile func(string) ([]byte, error)) (*Stack, error) {
	p := &stackParser{}
	dec := yaml.NewDecoder(bytes.NewReader(data))
	var root yaml.Node
	if err := dec.Decode(&root); err != nil {
		if errors.Is(err, io.EOF) {
			return nil, &stackError{display, []stackErr{{0, "the file is empty. Write one with: docli stack init"}}}
		}
		msg := strings.TrimPrefix(err.Error(), "yaml: ")
		line := 0
		if mm := yamlLineRE.FindStringSubmatch(msg); mm != nil {
			line, _ = strconv.Atoi(mm[1])
			msg = mm[2]
		}
		return nil, &stackError{display, []stackErr{{line, "not valid YAML: " + msg}}}
	}
	var extra yaml.Node
	if err := dec.Decode(&extra); err == nil {
		return nil, &stackError{display, []stackErr{{extra.Line, "a stack file holds one YAML document; remove the second one"}}}
	}
	top := root.Content[0]
	if top.Kind != yaml.MappingNode {
		return nil, &stackError{display, []stackErr{{top.Line, "the file must be a mapping of keys to values (version, droplets, ...)"}}}
	}
	p.scan(top)
	for i := 0; i+1 < len(top.Content); i += 2 {
		if why, ok := notInStack[top.Content[i].Value]; ok {
			p.errf(top.Content[i].Line, "%q is not supported in a stack file: %s", top.Content[i].Value, why)
		}
	}
	p.checkKeys(top, "the top level", topKeys)

	s := &Stack{}
	for _, f := range []struct {
		key string
		dst any
	}{{"version", &s.Version}, {"name", &s.Name}, {"region", &s.Region}} {
		if v := mapValue(top, f.key); v != nil {
			if err := v.Decode(f.dst); err != nil {
				p.typeErrs(err, v.Line)
			}
		}
	}
	s.SSHKeys = decodeList(p, top, "ssh_keys", []string{"name", "public_key_file"}, func(x *StackKey, ps srcPos) { x.Pos = ps })
	s.VPCs = decodeList(p, top, "vpcs", []string{"name", "region", "ip_range"}, func(x *StackVPC, ps srcPos) { x.Pos = ps })
	s.Firewalls = decodeList(p, top, "firewalls", []string{"name", "rules"}, func(x *StackFirewall, ps srcPos) { x.Pos = ps })
	s.Droplets = decodeList(p, top, "droplets", []string{"name", "region", "size", "image", "vpc", "ssh_keys", "firewalls", "tags", "backups", "monitoring", "ipv6", "user_data_file"},
		func(x *StackDroplet, ps srcPos) { x.Pos = ps })
	s.Databases = decodeList(p, top, "databases", []string{"name", "region", "version", "size", "nodes", "vpc", "trusted_droplets", "trusted_tags", "trusted_ips", "users", "databases"},
		func(x *StackDatabase, ps srcPos) { x.Pos = ps })
	s.Projects = decodeList(p, top, "projects", []string{"name", "purpose", "environment", "description"}, func(x *StackProject, ps srcPos) { x.Pos = ps })
	s.Volumes = decodeList(p, top, "volumes", []string{"name", "region", "size_gb", "description", "filesystem_type", "droplet", "project", "tags"},
		func(x *StackVolume, ps srcPos) { x.Pos = ps })
	s.LBs = decodeList(p, top, "load_balancers", []string{"name", "region", "size", "vpc", "algorithm", "forwarding_rules", "health_check", "droplets", "tag", "redirect_http_to_https", "project"},
		func(x *StackLB, ps srcPos) { x.Pos = ps })
	if seq := mapValue(top, "domains"); seq != nil && seq.Kind == yaml.SequenceNode {
		for _, it := range seq.Content {
			if it.Kind != yaml.MappingNode {
				p.errf(it.Line, "each entry in domains must be a mapping of keys to values")
				continue
			}
			p.checkKeys(it, "domains", []string{"name", "records"})
			d := StackDomain{Pos: posOf(it)}
			if v := mapValue(it, "name"); v != nil {
				if err := v.Decode(&d.Name); err != nil {
					p.typeErrs(err, v.Line)
				}
			}
			d.Records = decodeList(p, it, "records", []string{"type", "name", "data", "ttl", "priority", "flags", "tag", "droplet"}, func(x *StackRecord, ps srcPos) { x.Pos = ps })
			s.Domains = append(s.Domains, d)
		}
	} else if seq != nil && !(seq.Kind == yaml.ScalarNode && seq.Tag == "!!null") {
		p.errf(seq.Line, "domains must be a list")
	}

	if len(p.errs) == 0 {
		s.validate(p, defRegion, readFile)
	}
	if len(p.errs) > 0 {
		sort.SliceStable(p.errs, func(i, j int) bool { return p.errs[i].Line < p.errs[j].Line })
		return nil, &stackError{display, p.errs}
	}
	return s, nil
}

func nameIn[T any](list []T, name func(T) string, want string) bool {
	for _, x := range list {
		if name(x) == want {
			return true
		}
	}
	return false
}

// checkUnique reports duplicate names within one kind.
func checkUnique[T any](p *stackParser, kind string, list []T, name func(T) string, pos func(T) srcPos) {
	seen := map[string]int{}
	for _, x := range list {
		n := name(x)
		if first, dup := seen[n]; dup {
			p.errf(pos(x).at("name"), "the %s %q is declared twice (first on line %d)", kind, n, first)
		}
		seen[n] = pos(x).at("name")
	}
}

// validate checks everything that can be checked without the account.
func (s *Stack) validate(p *stackParser, defRegion string, readFile func(string) ([]byte, error)) {
	if s.Version != 1 {
		p.errf(0, "version: 1 is required (this docli reads stack files of version 1)")
	}
	if s.Name != "" && !stackRE.MatchString(s.Name) {
		p.errf(0, "name %q must use letters, digits, dash and underscore only", s.Name)
	}
	s.Region = firstNonEmpty(s.Region, defRegion)
	needRegion := func(r *string, ps srcPos, what string) {
		*r = firstNonEmpty(*r, s.Region)
		if *r == "" {
			p.errf(ps.Line, "%s has no region: set region: on it or at the top of the file (or use --region)", what)
		}
	}

	checkUnique(p, "SSH key", s.SSHKeys, func(x StackKey) string { return x.Name }, func(x StackKey) srcPos { return x.Pos })
	checkUnique(p, "VPC", s.VPCs, func(x StackVPC) string { return x.Name }, func(x StackVPC) srcPos { return x.Pos })
	checkUnique(p, "firewall", s.Firewalls, func(x StackFirewall) string { return x.Name }, func(x StackFirewall) srcPos { return x.Pos })
	checkUnique(p, "droplet", s.Droplets, func(x StackDroplet) string { return x.Name }, func(x StackDroplet) srcPos { return x.Pos })
	checkUnique(p, "database", s.Databases, func(x StackDatabase) string { return x.Name }, func(x StackDatabase) srcPos { return x.Pos })
	checkUnique(p, "project", s.Projects, func(x StackProject) string { return x.Name }, func(x StackProject) srcPos { return x.Pos })
	checkUnique(p, "volume", s.Volumes, func(x StackVolume) string { return x.Name }, func(x StackVolume) srcPos { return x.Pos })
	checkUnique(p, "load balancer", s.LBs, func(x StackLB) string { return x.Name }, func(x StackLB) srcPos { return x.Pos })
	checkUnique(p, "domain", s.Domains, func(x StackDomain) string { return x.Name }, func(x StackDomain) srcPos { return x.Pos })

	note := func(format string, a ...any) { s.Notes = append(s.Notes, fmt.Sprintf(format, a...)) }

	for i := range s.SSHKeys {
		k := &s.SSHKeys[i]
		if err := validateResourceName("SSH key", ensureNamePrefix(k.Name)); err != nil {
			p.errf(k.Pos.at("name"), "%v", err)
		}
		if k.PublicKeyFile == "" {
			note("SSH key %q (line %d) has no public_key_file, so it must already exist in the account; docli plan checks", k.Name, k.Pos.Line)
			continue
		}
		b, err := readFile(k.PublicKeyFile)
		if err != nil {
			p.errf(k.Pos.at("public_key_file"), "cannot read %s: %v", k.PublicKeyFile, pathErr(err))
			continue
		}
		line, _, _, err := parsePublicKey(string(b))
		if err != nil {
			p.errf(k.Pos.at("public_key_file"), "%s: %v", k.PublicKeyFile, err)
			continue
		}
		k.line = line
	}

	for i := range s.VPCs {
		v := &s.VPCs[i]
		if err := validateResourceName("VPC", ensureNamePrefix(v.Name)); err != nil {
			p.errf(v.Pos.at("name"), "%v", err)
		}
		needRegion(&v.Region, v.Pos, fmt.Sprintf("VPC %q", v.Name))
		if v.IPRange != "" {
			if _, err := normalizeAddr(v.IPRange); err != nil || !strings.Contains(v.IPRange, "/") {
				p.errf(v.Pos.at("ip_range"), "ip_range %q must be a CIDR range such as 10.50.0.0/24", v.IPRange)
			}
		}
	}

	for i := range s.Firewalls {
		f := &s.Firewalls[i]
		if err := validateResourceName("firewall", ensureNamePrefix(f.Name)); err != nil {
			p.errf(f.Pos.at("name"), "%v", err)
		}
		seen := map[string]bool{}
		for _, spec := range f.Rules {
			r, err := ParseRule(spec)
			if err != nil {
				p.errf(f.Pos.at("rules"), "firewall %q: %v", f.Name, err)
				continue
			}
			if seen[r.Spec()] {
				p.errf(f.Pos.at("rules"), "firewall %q lists the rule %s twice", f.Name, r.Spec())
				continue
			}
			seen[r.Spec()] = true
			f.parsed = append(f.parsed, r)
		}
	}

	for i := range s.Droplets {
		d := &s.Droplets[i]
		if err := validateHostname(d.Name); err != nil {
			p.errf(d.Pos.at("name"), "%v", err)
		}
		needRegion(&d.Region, d.Pos, fmt.Sprintf("droplet %q", d.Name))
		if d.Size == "" {
			p.errf(d.Pos.Line, "droplet %q needs a size, for example s-1vcpu-1gb (see: docli sizes)", d.Name)
		}
		if d.Image == "" {
			p.errf(d.Pos.Line, "droplet %q needs an image, for example ubuntu-24-04-x64 (see: docli images)", d.Name)
		}
		for _, t := range d.Tags {
			if err := validateTag(t); err != nil {
				p.errf(d.Pos.at("tags"), "%v", err)
			}
		}
		for _, fw := range d.Firewalls {
			if !nameIn(s.Firewalls, func(x StackFirewall) string { return x.Name }, fw) {
				p.errf(d.Pos.at("firewalls"), "droplet %q names the firewall %q, which is not declared under firewalls: in this file (docli only attaches firewalls it manages)", d.Name, fw)
			}
		}
		for _, k := range d.SSHKeys {
			if !nameIn(s.SSHKeys, func(x StackKey) string { return x.Name }, k) {
				note("droplet %q (line %d) uses the SSH key %q, which is not declared here; it must already exist in the account", d.Name, d.Pos.Line, k)
			}
		}
		if d.VPC != "" && !nameIn(s.VPCs, func(x StackVPC) string { return x.Name }, d.VPC) {
			note("droplet %q (line %d) uses the VPC %q, which is not declared here; it must already exist in the account", d.Name, d.Pos.Line, d.VPC)
		}
		if d.UserDataFile != "" {
			b, err := readFile(d.UserDataFile)
			switch {
			case err != nil:
				p.errf(d.Pos.at("user_data_file"), "cannot read %s: %v", d.UserDataFile, pathErr(err))
			case len(b) > maxUserData:
				p.errf(d.Pos.at("user_data_file"), "%s is %d bytes; DigitalOcean accepts at most %d bytes of user data", d.UserDataFile, len(b), maxUserData)
			default:
				d.userData = string(b)
			}
		}
	}

	s.validateM4(p, needRegion, note)

	for i := range s.Domains {
		d := &s.Domains[i]
		if err := validateDomainName(d.Name); err != nil {
			p.errf(d.Pos.at("name"), "%v", err)
			continue
		}
		var earlier []godo.DomainRecord
		for j := range d.Records {
			rec := &d.Records[j]
			s.validateRecord(p, d, rec)
			if rec.spec.Type == "" {
				continue
			}
			if err := recordConflict(&rec.spec, earlier, 0); err != nil {
				p.errf(rec.Pos.Line, "%v", err)
			}
			earlier = append(earlier, godo.DomainRecord{Type: rec.spec.Type, Name: rec.spec.Name, Data: rec.spec.Data})
		}
	}

	for i := range s.Databases {
		d := &s.Databases[i]
		if !dbNameRE.MatchString(d.Name) {
			p.errf(d.Pos.at("name"), "%q is not a valid cluster name: 3-63 characters, lower case letters, digits and dashes, starting with a letter", d.Name)
		}
		needRegion(&d.Region, d.Pos, fmt.Sprintf("database %q", d.Name))
		if d.Version == "" || d.Size == "" {
			p.errf(d.Pos.Line, "database %q needs a version (for example \"16\") and a size (for example db-s-1vcpu-1gb)", d.Name)
		}
		if d.Nodes < 1 || d.Nodes > 3 {
			p.errf(d.Pos.at("nodes"), "database %q: nodes must be 1, 2 or 3", d.Name)
		}
		if d.VPC == "" {
			p.errf(d.Pos.Line, "database %q needs a vpc: it is reached over the private network", d.Name)
		} else if !nameIn(s.VPCs, func(x StackVPC) string { return x.Name }, d.VPC) {
			note("database %q (line %d) uses the VPC %q, which is not declared here; it must already exist in the account", d.Name, d.Pos.Line, d.VPC)
		}
		if len(d.TrustedDroplets)+len(d.TrustedTags)+len(d.TrustedIPs) == 0 {
			p.errf(d.Pos.Line, "database %q needs at least one of trusted_droplets, trusted_tags or trusted_ips. A database that any address may try to reach is not something a stack file can create", d.Name)
		}
		for _, n := range d.TrustedDroplets {
			if !nameIn(s.Droplets, func(x StackDroplet) string { return x.Name }, n) {
				p.errf(d.Pos.at("trusted_droplets"), "database %q trusts the droplet %q, which is not declared under droplets: in this file", d.Name, n)
			}
		}
		for _, t := range d.TrustedTags {
			if err := validateTag(t); err != nil {
				p.errf(d.Pos.at("trusted_tags"), "%v", err)
			}
		}
		for _, ip := range d.TrustedIPs {
			a, err := normalizeAddr(ip)
			switch {
			case err != nil:
				p.errf(d.Pos.at("trusted_ips"), "%v", err)
			case a == "0.0.0.0/0" || a == "::/0":
				p.errf(d.Pos.at("trusted_ips"), "%s would let every address try to connect. A stack file does not create open databases", ip)
			}
		}
		for _, u := range d.Users {
			if !dbIdentRE.MatchString(u) || u == "doadmin" {
				p.errf(d.Pos.at("users"), "%q is not a usable user name (letters, digits and underscore; doadmin is the administrator and is never managed)", u)
			}
		}
		for _, n := range d.Databases {
			if !dbIdentRE.MatchString(n) || n == "defaultdb" {
				p.errf(d.Pos.at("databases"), "%q is not a usable database name (letters, digits and underscore; defaultdb already exists)", n)
			}
		}
	}
}

// validateM4 checks the milestone 4 kinds. It is called from validate, after droplets and VPCs are known.
func (s *Stack) validateM4(p *stackParser, needRegion func(*string, srcPos, string), note func(string, ...any)) {
	projectKnown := func(ref string, ps srcPos, what string) {
		if ref != "" && !nameIn(s.Projects, func(x StackProject) string { return x.Name }, ref) {
			note("%s (line %d) uses the project %q, which is not declared here; it must already exist in the account", what, ps.Line, ref)
		}
	}
	for i := range s.Projects {
		pr := &s.Projects[i]
		if final := ensureNamePrefix(pr.Name); len(final) > 175 || strings.TrimSpace(pr.Name) == "" {
			p.errf(pr.Pos.at("name"), "the project name %q must be 1 to 175 characters with the prefix %s", pr.Name, namePrefix)
		}
		pr.Purpose = firstNonEmpty(pr.Purpose, "Web Application")
		if pr.Environment != "" && !contains(projectEnvs, pr.Environment) {
			p.errf(pr.Pos.at("environment"), "environment %q must be one of %s", pr.Environment, strings.Join(projectEnvs, ", "))
		}
	}
	for i := range s.Volumes {
		v := &s.Volumes[i]
		what := fmt.Sprintf("volume %q", v.Name)
		if !volumeNameRE.MatchString(v.Name) {
			p.errf(v.Pos.at("name"), "%q is not a valid volume name: lower case letters, digits and dashes, 1-64 characters, not starting or ending with a dash", v.Name)
		}
		needRegion(&v.Region, v.Pos, what)
		if v.SizeGB < 1 || v.SizeGB > maxVolumeGB {
			p.errf(v.Pos.at("size_gb"), "%s: size_gb must be between 1 and %d", what, maxVolumeGB)
		}
		if v.FilesystemType != "" && v.FilesystemType != "ext4" && v.FilesystemType != "xfs" {
			p.errf(v.Pos.at("filesystem_type"), "%s: filesystem_type must be ext4 or xfs", what)
		}
		for _, t := range v.Tags {
			if err := validateTag(t); err != nil {
				p.errf(v.Pos.at("tags"), "%v", err)
			}
		}
		if v.Droplet != "" && !nameIn(s.Droplets, func(x StackDroplet) string { return x.Name }, v.Droplet) {
			p.errf(v.Pos.at("droplet"), "%s names the droplet %q, which is not declared under droplets: in this file", what, v.Droplet)
		}
		projectKnown(v.Project, v.Pos, what)
	}
	for i := range s.LBs {
		l := &s.LBs[i]
		what := fmt.Sprintf("load balancer %q", l.Name)
		if err := validateHostname(ensureNamePrefix(l.Name)); err != nil {
			p.errf(l.Pos.at("name"), "the load balancer name %v", err)
		}
		needRegion(&l.Region, l.Pos, what)
		l.Algorithm = firstNonEmpty(l.Algorithm, "round_robin")
		if l.Algorithm != "round_robin" && l.Algorithm != "least_connections" {
			p.errf(l.Pos.at("algorithm"), "%s: algorithm must be round_robin or least_connections", what)
		}
		if len(l.ForwardingRules) == 0 {
			p.errf(l.Pos.Line, "%s needs at least one forwarding_rules entry, for example http:80=http:8080", what)
		}
		seen := map[string]bool{}
		for _, spec := range l.ForwardingRules {
			f, err := parseForward(spec)
			if err != nil {
				p.errf(l.Pos.at("forwarding_rules"), "%s: %v", what, err)
				continue
			}
			k := fmt.Sprintf("%s:%d", f.EntryProtocol, f.EntryPort)
			if seen[k] {
				p.errf(l.Pos.at("forwarding_rules"), "%s: two forwarding rules listen on %s", what, k)
			}
			seen[k] = true
			l.rules = append(l.rules, f)
		}
		if l.HealthCheck != "" {
			h, err := parseHealth(l.HealthCheck)
			if err != nil {
				p.errf(l.Pos.at("health_check"), "%s: %v", what, err)
			} else {
				if !lbTargetsPort(l.rules, h.Port) && len(l.rules) > 0 && len(l.rules) == len(l.ForwardingRules) {
					p.errf(l.Pos.at("health_check"), "%s: the health check port %d is not a target port of any forwarding rule", what, h.Port)
				}
				l.health = h
			}
		}
		if (len(l.Droplets) == 0) == (l.Tag == "") {
			p.errf(l.Pos.Line, "%s needs its targets: either droplets: (names declared in this file) or tag:, one of the two", what)
		}
		if l.Tag != "" {
			if err := validateTag(l.Tag); err != nil {
				p.errf(l.Pos.at("tag"), "%v", err)
			}
		}
		for _, d := range l.Droplets {
			if !nameIn(s.Droplets, func(x StackDroplet) string { return x.Name }, d) {
				p.errf(l.Pos.at("droplets"), "%s fronts the droplet %q, which is not declared under droplets: in this file", what, d)
			}
		}
		if l.Size != "" && !strings.HasPrefix(l.Size, "lb-") {
			p.errf(l.Pos.at("size"), "%s: size %q should look like lb-small, lb-medium or lb-large", what, l.Size)
		}
		if l.VPC != "" && !nameIn(s.VPCs, func(x StackVPC) string { return x.Name }, l.VPC) {
			note("%s (line %d) uses the VPC %q, which is not declared here; it must already exist in the account", what, l.Pos.Line, l.VPC)
		}
		projectKnown(l.Project, l.Pos, what)
	}
}

func (s *Stack) validateRecord(p *stackParser, d *StackDomain, rec *StackRecord) {
	nm, err := normalizeRecordName(d.Name, rec.Name)
	if err != nil {
		p.errf(rec.Pos.at("name"), "%v", err)
		return
	}
	spec := recordSpec{Type: strings.ToUpper(rec.Type), Name: nm, Data: rec.Data, TTL: rec.TTL, Priority: -1, Tag: rec.Tag}
	if spec.TTL == 0 {
		spec.TTL = defaultTTL
	}
	if rec.Priority != nil {
		spec.Priority = *rec.Priority
	}
	if rec.Flags != nil {
		spec.Flags = *rec.Flags
	}
	if rec.Droplet != "" {
		switch {
		case spec.Type != "A":
			p.errf(rec.Pos.at("droplet"), "droplet: only works on an A record (it means the droplet's public IPv4 address)")
			return
		case rec.Data != "":
			p.errf(rec.Pos.at("droplet"), "a record has either data: or droplet:, not both")
			return
		case !nameIn(s.Droplets, func(x StackDroplet) string { return x.Name }, rec.Droplet):
			p.errf(rec.Pos.at("droplet"), "the droplet %q is not declared under droplets: in this file", rec.Droplet)
			return
		}
		spec.Data = "0.0.0.0" // placeholder so the other checks can run; the real address is read from the droplet
	}
	if err := validateRecord(&spec); err != nil {
		p.errf(rec.Pos.Line, "%s record %s: %v", spec.Type, fqdn(d.Name, spec.Name), err)
		return
	}
	if rec.Droplet != "" {
		spec.Data = ""
	}
	rec.spec = spec
}

// stackKinds lists the kinds in the fixed order apply uses.
var stackOrder = []string{"project", "ssh_key", "vpc", "firewall", "droplet", "volume", "load_balancer", "database", "db_user", "db_database", "domain", "dns_record"}

// ---- locating and reading the file ----

func (r *Run) projectRoot() string {
	cwd, err := r.Getwd()
	if err != nil {
		return "."
	}
	root, _ := findProjectRoot(cwd)
	return root
}

// stackPath returns where the stack file is and how to show that path.
func (r *Run) stackPath(file string) (path, display string) {
	if file != "" {
		if filepath.IsAbs(file) {
			return file, file
		}
		cwd, _ := r.Getwd()
		return filepath.Join(cwd, file), file
	}
	return filepath.Join(r.projectRoot(), filepath.FromSlash(defaultStackRel)), defaultStackRel
}

// fileReader reads files named inside the stack: relative paths are from the project root, and a leading ~/ is
// the home directory.
func (r *Run) fileReader() func(string) ([]byte, error) {
	root := r.projectRoot()
	return func(p string) ([]byte, error) {
		switch {
		case strings.HasPrefix(p, "~/"):
			home, err := r.HomeDir()
			if err != nil {
				return nil, err
			}
			p = filepath.Join(home, p[2:])
		case !filepath.IsAbs(p):
			p = filepath.Join(root, filepath.FromSlash(p))
		}
		return os.ReadFile(p)
	}
}

func (r *Run) loadStack(file string) (*Stack, string, error) {
	path, display := r.stackPath(file)
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, display, fmt.Errorf("no stack file at %s. Write an example with: docli stack init", display)
		}
		return nil, display, fmt.Errorf("cannot read %s: %v", display, pathErr(err))
	}
	s, err := parseStack(data, display, r.G.Region, r.fileReader())
	return s, display, err
}
