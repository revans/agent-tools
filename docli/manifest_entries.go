package main

import (
	"context"
	"sort"
	"strconv"

	"github.com/digitalocean/godo"
	"gopkg.in/yaml.v3"
)

// What each resource kind writes to the manifest. Field order here is the order in the file. Nothing secret
// belongs in these structs: no tokens, passwords, connection strings, user data or key material.

type dropletEntry struct {
	ID          int      `yaml:"id"`
	Name        string   `yaml:"name"`
	Region      string   `yaml:"region"`
	Status      string   `yaml:"status"`
	SizeSlug    string   `yaml:"size_slug"`
	Image       string   `yaml:"image"`
	VCPUs       int      `yaml:"vcpus"`
	MemoryMB    int      `yaml:"memory_mb"`
	DiskGB      int      `yaml:"disk_gb"`
	PublicIPv4  string   `yaml:"public_ipv4"`
	PrivateIPv4 string   `yaml:"private_ipv4"`
	VPCName     string   `yaml:"vpc_name"`
	VPCID       string   `yaml:"vpc_id"`
	MonthlyCost float64  `yaml:"monthly_cost"`
	Tags        []string `yaml:"tags"`
	Firewalls   []string `yaml:"firewalls"`
	CreatedAt   string   `yaml:"created_at"`
}

func (e *dropletEntry) Section() string { return "droplets" }
func (e *dropletEntry) EntryID() string { return strconv.Itoa(e.ID) }

type vpcEntry struct {
	ID        string `yaml:"id"`
	Name      string `yaml:"name"`
	Region    string `yaml:"region"`
	IPRange   string `yaml:"ip_range"`
	CreatedAt string `yaml:"created_at"`
}

func (e *vpcEntry) Section() string { return "vpcs" }
func (e *vpcEntry) EntryID() string { return e.ID }

type firewallEntry struct {
	ID        string   `yaml:"id"`
	Name      string   `yaml:"name"`
	Rules     []string `yaml:"rules"`
	Tags      []string `yaml:"tags"`
	Droplets  []string `yaml:"droplets"`
	CreatedAt string   `yaml:"created_at"`
}

func (e *firewallEntry) Section() string { return "firewalls" }
func (e *firewallEntry) EntryID() string { return e.ID }

type sshKeyEntry struct {
	ID          int    `yaml:"id"`
	Name        string `yaml:"name"`
	Fingerprint string `yaml:"fingerprint"`
}

func (e *sshKeyEntry) Section() string { return "ssh_keys" }
func (e *sshKeyEntry) EntryID() string { return strconv.Itoa(e.ID) }

// recordEntry is a DNS record docli created. Only these are listed under their domain.
type recordEntry struct {
	ID       int    `yaml:"id"`
	Type     string `yaml:"type"`
	Name     string `yaml:"name"`
	Data     string `yaml:"data"`
	TTL      int    `yaml:"ttl"`
	Priority int    `yaml:"priority"`
	Flags    int    `yaml:"flags"`
	Tag      string `yaml:"tag"`
}

// domainEntry is the one place the manifest IS the authority: DigitalOcean cannot tag domains or records, so
// "docli created this" is remembered here. created_by_docli says the domain itself was created by docli; records
// lists only the records docli created (in a domain docli did not create, too).
type domainEntry struct {
	ID             string        `yaml:"id"`
	Name           string        `yaml:"name"`
	CreatedByDocli bool          `yaml:"created_by_docli"`
	TTL            int           `yaml:"ttl"`
	Records        []recordEntry `yaml:"records"`
}

func (e *domainEntry) Section() string { return "domains" }
func (e *domainEntry) EntryID() string { return e.ID }

// databaseEntry holds host and port, never a password or a connection URI.
type databaseEntry struct {
	ID             string   `yaml:"id"`
	Name           string   `yaml:"name"`
	Engine         string   `yaml:"engine"`
	Version        string   `yaml:"version"`
	Region         string   `yaml:"region"`
	SizeSlug       string   `yaml:"size_slug"`
	NumNodes       int      `yaml:"num_nodes"`
	Status         string   `yaml:"status"`
	VPCName        string   `yaml:"vpc_name"`
	VPCID          string   `yaml:"vpc_id"`
	Host           string   `yaml:"host"`
	PrivateHost    string   `yaml:"private_host"`
	Port           int      `yaml:"port"`
	Database       string   `yaml:"database"`
	Tags           []string `yaml:"tags"`
	TrustedSources []string `yaml:"trusted_sources"`
	CreatedAt      string   `yaml:"created_at"`
}

func (e *databaseEntry) Section() string { return "databases" }
func (e *databaseEntry) EntryID() string { return e.ID }

// snapshot is the account's current droplets, VPCs, firewalls, keys, domains and databases. Domain ownership
// comes from the manifest file (attach), everything else from tags and name prefixes.
type snapshot struct {
	droplets  []godo.Droplet
	vpcs      []*godo.VPC
	firewalls []godo.Firewall
	keys      []godo.Key
	domains   []godo.Domain
	databases []godo.Database
	denied    map[string]bool // sections the token may not read (403): skipped, never treated as "gone"

	// milestone 4: loaded on first use (ensure), see manifest_m4.go
	projects    []godo.Project
	volumes     []godo.Volume
	reservedIPs []godo.ReservedIP
	lbs         []godo.LoadBalancer
	spacesKeys  []*godo.SpacesKey
	buckets     map[string][]bucketInfo
	ownIPs      map[string]bool // reserved IPs the manifest lists as docli's
	loaded      map[string]bool
	why         map[string]string // why a section is denied, when the reason is not a 403
	spaces      *Spaces           // nil when no Spaces access key is configured
	remember    func(string)      // registers a secret to scrub

	cloud     Cloud
	ctx       context.Context
	own       map[string]*ownedDomain
	records   map[string][]godo.DomainRecord
	dbRules   map[string][]godo.DatabaseFirewallRule
	dbRulesOK map[string]bool
}

func (r *Run) loadSnapshot() (*snapshot, error) {
	s := snapshot{denied: map[string]bool{}, cloud: r.Cloud, ctx: r.Ctx, own: map[string]*ownedDomain{},
		records: map[string][]godo.DomainRecord{}, dbRules: map[string][]godo.DatabaseFirewallRule{}, dbRulesOK: map[string]bool{},
		buckets: map[string][]bucketInfo{}, ownIPs: map[string]bool{}, loaded: map[string]bool{}, why: map[string]string{}, remember: r.rememberSecret}
	if sp, err := r.App.newSpaces(r.G); err == nil {
		s.spaces = sp
	}
	var err error
	if s.droplets, err = r.Cloud.ListDroplets(r.Ctx, ""); err != nil {
		return nil, err
	}
	if s.vpcs, err = r.Cloud.ListVPCs(r.Ctx); err != nil {
		return nil, err
	}
	if s.firewalls, err = r.Cloud.ListFirewalls(r.Ctx); err != nil {
		return nil, err
	}
	if s.keys, err = r.Cloud.ListKeys(r.Ctx); err != nil {
		return nil, err
	}
	// A token without domain or database access still works for everything else.
	if s.domains, err = r.Cloud.ListDomains(r.Ctx); err != nil {
		if statusOf(err) != 403 {
			return nil, err
		}
		s.denied["domains"] = true
	}
	if s.databases, err = r.Cloud.ListDatabases(r.Ctx); err != nil {
		if statusOf(err) != 403 {
			return nil, err
		}
		s.denied["databases"] = true
	}
	return &s, nil
}

func (s *snapshot) firewallsFor(d godo.Droplet) []string {
	var names []string
	for _, f := range s.firewalls {
		hit := false
		for _, id := range f.DropletIDs {
			hit = hit || id == d.ID
		}
		for _, t := range f.Tags {
			hit = hit || hasTag(d.Tags, t)
		}
		if hit {
			names = append(names, f.Name)
		}
	}
	sort.Strings(names)
	return names
}

func (s *snapshot) dropletEntry(d godo.Droplet) *dropletEntry {
	v := dropletView(d)
	vpcName := ""
	for _, x := range s.vpcs {
		if x.ID == d.VPCUUID {
			vpcName = x.Name
		}
	}
	tags := append([]string(nil), d.Tags...)
	sort.Strings(tags)
	return &dropletEntry{d.ID, d.Name, v.Region, d.Status, v.SizeSlug, firstNonEmpty(v.Image.Slug, v.Image.Name), d.Vcpus, d.Memory, d.Disk,
		v.PublicIPv4, v.PrivateIPv4, vpcName, d.VPCUUID, v.MonthlyCost, tags, nonNil(s.firewallsFor(d)), d.Created}
}

func (s *snapshot) firewallEntry(f godo.Firewall) *firewallEntry {
	rules := []string{}
	for _, r := range firewallRules(f) {
		rules = append(rules, r.Spec())
	}
	var names []string
	for _, d := range s.droplets {
		hit := false
		for _, id := range f.DropletIDs {
			hit = hit || id == d.ID
		}
		for _, t := range f.Tags {
			hit = hit || hasTag(d.Tags, t)
		}
		if hit {
			names = append(names, d.Name)
		}
	}
	sort.Strings(names)
	tags := append([]string(nil), f.Tags...)
	sort.Strings(tags)
	return &firewallEntry{f.ID, f.Name, rules, nonNil(tags), nonNil(names), f.Created}
}

// managedEntry returns the entry for a managed resource, or nil if it is gone or not docli's.
func (s *snapshot) managedEntry(section, id string) ManifestEntry {
	e, managed := s.entry(section, id)
	if managed {
		return e
	}
	return nil
}

// entry returns the account's entry for a resource and whether docli owns it; nil when it does not exist.
func (s *snapshot) entry(section, id string) (ManifestEntry, bool) {
	if e, managed, ok := s.entryM4(section, id); ok {
		return e, managed
	}
	switch section {
	case "droplets":
		for _, d := range s.droplets {
			if strconv.Itoa(d.ID) == id {
				return s.dropletEntry(d), hasTag(d.Tags, managedTag)
			}
		}
	case "vpcs":
		for _, v := range s.vpcs {
			if v.ID == id {
				return &vpcEntry{v.ID, v.Name, v.RegionSlug, v.IPRange, vpcView(v).CreatedAt}, hasManagedPrefix(v.Name)
			}
		}
	case "firewalls":
		for _, f := range s.firewalls {
			if f.ID == id {
				return s.firewallEntry(f), hasManagedPrefix(f.Name)
			}
		}
	case "ssh_keys":
		for _, k := range s.keys {
			if strconv.Itoa(k.ID) == id {
				return &sshKeyEntry{k.ID, k.Name, k.Fingerprint}, hasManagedPrefix(k.Name)
			}
		}
	case "databases":
		for _, d := range s.databases {
			if d.ID == id {
				return s.databaseEntry(d), hasTag(d.Tags, managedTag)
			}
		}
	case "domains":
		// Only domains the file already lists are known to be docli's; the account cannot say.
		if o := s.own[id]; o != nil {
			if e := s.buildDomainEntry(id, o.Created, o.IDs); e != nil {
				return e, true
			}
		}
	}
	return nil, false
}

// ownedDomain is what the manifest file says docli created in one domain.
type ownedDomain struct {
	Created bool
	IDs     []int
}

// attach reads the file's domain entries so ownership and record lists can be compared with the account.
func (s *snapshot) attach(m *Manifest) {
	if s.ownIPs == nil {
		s.ownIPs = map[string]bool{}
	}
	for _, n := range m.sections["reserved_ips"] {
		if id := nodeID(n); id != "" {
			s.ownIPs[id] = true
		}
	}
	for _, n := range m.sections["domains"] {
		var de domainEntry
		if n.Decode(&de) != nil || de.ID == "" {
			continue
		}
		o := &ownedDomain{Created: de.CreatedByDocli}
		for _, r := range de.Records {
			o.IDs = append(o.IDs, r.ID)
		}
		s.own[de.ID] = o
	}
}

func (s *snapshot) domain(name string) *godo.Domain {
	for i := range s.domains {
		if s.domains[i].Name == name {
			return &s.domains[i]
		}
	}
	return nil
}

// recordsOf lists a domain's records once.
func (s *snapshot) recordsOf(name string) ([]godo.DomainRecord, error) {
	if rs, ok := s.records[name]; ok {
		return rs, nil
	}
	rs, err := s.cloud.ListDomainRecords(s.ctx, name)
	if err != nil {
		return nil, err
	}
	s.records[name] = rs
	return rs, nil
}

// buildDomainEntry is the account's view of a domain restricted to the records docli created (ids). It returns
// nil when the domain does not exist. Records that no longer exist are left out.
func (s *snapshot) buildDomainEntry(name string, created bool, ids []int) *domainEntry {
	d := s.domain(name)
	if d == nil {
		return nil
	}
	e := &domainEntry{ID: d.Name, Name: d.Name, CreatedByDocli: created, TTL: d.TTL, Records: []recordEntry{}}
	if len(ids) == 0 {
		return e
	}
	rs, err := s.recordsOf(name)
	if err != nil {
		return e
	}
	sort.Ints(ids)
	for _, id := range ids {
		for _, r := range rs {
			if r.ID == id {
				e.Records = append(e.Records, recordEntry{r.ID, r.Type, r.Name, r.Data, r.TTL, r.Priority, r.Flags, r.Tag})
			}
		}
	}
	return e
}

// trustedSources returns the cluster's trusted sources as sorted "type:value" strings, fetched once.
func (s *snapshot) trustedSources(id string) []string {
	if !s.dbRulesOK[id] {
		rules, err := s.cloud.GetDatabaseFirewall(s.ctx, id)
		if err == nil {
			s.dbRules[id] = rules
		}
		s.dbRulesOK[id] = true
	}
	var out []string
	for _, r := range s.dbRules[id] {
		out = append(out, r.Type+":"+r.Value)
	}
	sort.Strings(out)
	return nonNil(out)
}

func (s *snapshot) databaseEntry(d godo.Database) *databaseEntry {
	vpcName := ""
	for _, x := range s.vpcs {
		if x.ID == d.PrivateNetworkUUID {
			vpcName = x.Name
		}
	}
	c, p := connView(d.Connection), connView(d.PrivateConnection)
	tags := append([]string(nil), d.Tags...)
	sort.Strings(tags)
	return &databaseEntry{d.ID, d.Name, d.EngineSlug, d.VersionSlug, d.RegionSlug, d.SizeSlug, d.NumNodes, d.Status, vpcName,
		d.PrivateNetworkUUID, c.Host, p.Host, c.Port, c.Database, nonNil(tags), s.trustedSources(d.ID), fmtTime(d.CreatedAt)}
}

var _ = yaml.Node{}

// managedEntries lists every managed resource, by section, sorted by name then id.
func (s *snapshot) managedEntries() []ManifestEntry {
	var out []ManifestEntry
	for _, d := range s.droplets {
		if hasTag(d.Tags, managedTag) {
			out = append(out, s.dropletEntry(d))
		}
	}
	for _, v := range s.vpcs {
		if hasManagedPrefix(v.Name) {
			e, _ := s.entry("vpcs", v.ID)
			out = append(out, e)
		}
	}
	for _, f := range s.firewalls {
		if hasManagedPrefix(f.Name) {
			out = append(out, s.firewallEntry(f))
		}
	}
	for _, k := range s.keys {
		if hasManagedPrefix(k.Name) {
			out = append(out, &sshKeyEntry{k.ID, k.Name, k.Fingerprint})
		}
	}
	for _, d := range s.databases {
		if hasTag(d.Tags, managedTag) {
			out = append(out, s.databaseEntry(d))
		}
	}
	// Domains are not listed here: the account cannot say which ones docli created (see domainEntry).
	out = append(out, s.managedEntriesM4()...)
	sort.SliceStable(out, func(i, j int) bool {
		a, b := entryName(out[i]), entryName(out[j])
		if a != b {
			return a < b
		}
		return out[i].EntryID() < out[j].EntryID()
	})
	return out
}

func entryName(e ManifestEntry) string {
	switch x := e.(type) {
	case *dropletEntry:
		return x.Name
	case *vpcEntry:
		return x.Name
	case *firewallEntry:
		return x.Name
	case *sshKeyEntry:
		return x.Name
	case *domainEntry:
		return x.Name
	case *databaseEntry:
		return x.Name
	case *projectEntry:
		return x.Name
	case *volumeEntry:
		return x.Name
	case *reservedIPEntry:
		return x.IP
	case *lbEntry:
		return x.Name
	case *bucketEntry:
		return x.Name
	case *spacesKeyEntry:
		return x.Name
	}
	return ""
}
