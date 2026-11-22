package main

import (
	"fmt"
	"sort"
	"strings"

	"github.com/digitalocean/godo"
)

// Manifest entries for milestone 4. As before: nothing secret. A Spaces key's entry has its access key id (an
// identifier, like a user name) but never the secret key, which only ever goes to the --secret-file.
//
// Ownership: projects and load balancers by the docli- name prefix, volumes by the docli:managed tag, Spaces keys
// and buckets by the docli- name prefix. Reserved IPs cannot be named or tagged, so the manifest itself is the
// authority for them, as it is for DNS.

type projectEntry struct {
	ID          string `yaml:"id"`
	Name        string `yaml:"name"`
	Purpose     string `yaml:"purpose"`
	Environment string `yaml:"environment"`
	CreatedAt   string `yaml:"created_at"`
}

func (e *projectEntry) Section() string { return "projects" }
func (e *projectEntry) EntryID() string { return e.ID }

type volumeEntry struct {
	ID             string   `yaml:"id"`
	Name           string   `yaml:"name"`
	Region         string   `yaml:"region"`
	SizeGB         int64    `yaml:"size_gb"`
	Droplets       []string `yaml:"droplets"`
	FilesystemType string   `yaml:"filesystem_type"`
	Tags           []string `yaml:"tags"`
	CreatedAt      string   `yaml:"created_at"`
}

func (e *volumeEntry) Section() string { return "volumes" }
func (e *volumeEntry) EntryID() string { return e.ID }

// reservedIPEntry's id is the address itself; it is the only identity a reserved IP has.
type reservedIPEntry struct {
	ID      string `yaml:"id"`
	IP      string `yaml:"ip"`
	Region  string `yaml:"region"`
	Droplet string `yaml:"droplet"`
}

func (e *reservedIPEntry) Section() string { return "reserved_ips" }
func (e *reservedIPEntry) EntryID() string { return e.ID }

type lbEntry struct {
	ID           string   `yaml:"id"`
	Name         string   `yaml:"name"`
	IP           string   `yaml:"ip"`
	Region       string   `yaml:"region"`
	Size         string   `yaml:"size"`
	SizeUnit     uint32   `yaml:"size_unit"`
	Algorithm    string   `yaml:"algorithm"`
	VPCName      string   `yaml:"vpc_name"`
	VPCID        string   `yaml:"vpc_id"`
	Rules        []string `yaml:"forwarding_rules"`
	HealthCheck  string   `yaml:"health_check"`
	Droplets     []string `yaml:"droplets"`
	Tag          string   `yaml:"tag"`
	RedirectHTTP bool     `yaml:"redirect_http_to_https"`
	CreatedAt    string   `yaml:"created_at"`
}

func (e *lbEntry) Section() string { return "load_balancers" }
func (e *lbEntry) EntryID() string { return e.ID }

// bucketEntry's id is REGION/NAME: a bucket name is only unique within a region.
type bucketEntry struct {
	ID        string `yaml:"id"`
	Name      string `yaml:"name"`
	Region    string `yaml:"region"`
	CreatedAt string `yaml:"created_at"`
}

func (e *bucketEntry) Section() string { return "spaces_buckets" }
func (e *bucketEntry) EntryID() string { return e.ID }

type spacesKeyEntry struct {
	ID        string   `yaml:"id"` // the access key id
	Name      string   `yaml:"name"`
	Grants    []string `yaml:"grants"`
	CreatedAt string   `yaml:"created_at"`
}

func (e *spacesKeyEntry) Section() string { return "spaces_keys" }
func (e *spacesKeyEntry) EntryID() string { return e.ID }

// m4Sections are the kinds added in milestone 4, in the order they are loaded.
var m4Sections = []string{"projects", "volumes", "reserved_ips", "load_balancers", "spaces_keys", "spaces_buckets"}

// ensure loads one milestone 4 list the first time it is needed, so a droplet create does not also list volumes.
// A token that may not read a kind (403) leaves it "denied": skipped, never treated as "gone".
func (s *snapshot) ensure(section string) {
	if s.loaded[section] || s.denied[section] {
		return
	}
	s.loaded[section] = true
	var err error
	switch section {
	case "projects":
		s.projects, err = s.cloud.ListProjects(s.ctx)
	case "volumes":
		s.volumes, err = s.cloud.ListVolumes(s.ctx)
	case "reserved_ips":
		s.reservedIPs, err = s.cloud.ListReservedIPs(s.ctx)
	case "load_balancers":
		s.lbs, err = s.cloud.ListLoadBalancers(s.ctx)
	case "spaces_keys":
		s.spacesKeys, err = s.cloud.ListSpacesKeys(s.ctx)
		for _, k := range s.spacesKeys {
			if k != nil && s.remember != nil {
				s.remember(k.SecretKey)
			}
		}
	}
	if err != nil {
		s.denied[section] = true
		s.why[section] = fmt.Sprintf("the %s could not be read (%v), so this entry was not checked", strings.ReplaceAll(section, "_", " "), statusWord(err))
		if statusOf(err) == 403 {
			s.why[section] = "the token cannot read " + strings.ReplaceAll(section, "_", " ") + ", so this entry was not checked"
		}
	}
}

func statusWord(err error) string {
	if st := statusOf(err); st != 0 {
		return fmt.Sprintf("the API answered %d", st)
	}
	return "the request failed"
}

// bucketsIn lists one region's buckets once.
func (s *snapshot) bucketsIn(region string) ([]bucketInfo, bool) {
	if bs, ok := s.buckets[region]; ok {
		return bs, true
	}
	if s.denied["spaces_buckets"] {
		return nil, false
	}
	if s.spaces == nil {
		s.denied["spaces_buckets"] = true
		s.why["spaces_buckets"] = "no Spaces access key is configured (DOCLI_SPACES_ACCESS_KEY and DOCLI_SPACES_SECRET_KEY), so buckets were not checked"
		return nil, false
	}
	bs, err := s.spaces.ListBuckets(s.ctx, region)
	if err != nil {
		s.denied["spaces_buckets"] = true
		s.why["spaces_buckets"] = fmt.Sprintf("the buckets in %s could not be listed (%v), so this entry was not checked", region, err)
		return nil, false
	}
	s.buckets[region] = bs
	return bs, true
}

func (s *snapshot) dropletNameByID(id int) string {
	for _, d := range s.droplets {
		if d.ID == id {
			return d.Name
		}
	}
	return itoa(id)
}

func (s *snapshot) volumeEntry(v godo.Volume) *volumeEntry {
	var names []string
	for _, id := range v.DropletIDs {
		names = append(names, s.dropletNameByID(id))
	}
	sort.Strings(names)
	tags := append([]string(nil), v.Tags...)
	sort.Strings(tags)
	return &volumeEntry{v.ID, v.Name, volumeRegion(v), v.SizeGigaBytes, nonNil(names), v.FilesystemType, nonNil(tags), fmtTime(v.CreatedAt)}
}

func (s *snapshot) reservedIPEntry(ip godo.ReservedIP) *reservedIPEntry {
	return &reservedIPEntry{ip.IP, ip.IP, ripRegion(ip), ripDropletName(ip)}
}

func (s *snapshot) lbEntry(l godo.LoadBalancer) *lbEntry {
	vpcName := ""
	for _, x := range s.vpcs {
		if x.ID == l.VPCUUID {
			vpcName = x.Name
		}
	}
	rules := []string{}
	for _, f := range l.ForwardingRules {
		rules = append(rules, lbRuleSpec(f))
	}
	sort.Strings(rules)
	var names []string
	for _, id := range l.DropletIDs {
		names = append(names, s.dropletNameByID(id))
	}
	sort.Strings(names)
	return &lbEntry{l.ID, l.Name, l.IP, lbRegion(l), l.SizeSlug, l.SizeUnit, l.Algorithm, vpcName, l.VPCUUID, rules, lbHealthSpec(l.HealthCheck),
		nonNil(names), l.Tag, l.RedirectHttpToHttps, l.Created}
}

func spacesKeyEntryOf(k *godo.SpacesKey) *spacesKeyEntry {
	var grants []string
	for _, g := range k.Grants {
		if g != nil {
			grants = append(grants, g.Bucket+":"+string(g.Permission))
		}
	}
	sort.Strings(grants)
	return &spacesKeyEntry{k.AccessKey, k.Name, nonNil(grants), k.CreatedAt}
}

// entryM4 is snapshot.entry for the milestone 4 kinds. handled is false for any other section.
func (s *snapshot) entryM4(section, id string) (e ManifestEntry, managed, handled bool) {
	if !contains(m4Sections, section) {
		return nil, false, false
	}
	s.ensure(section)
	switch section {
	case "projects":
		for _, p := range s.projects {
			if p.ID == id {
				return &projectEntry{p.ID, p.Name, p.Purpose, p.Environment, p.CreatedAt}, hasManagedPrefix(p.Name), true
			}
		}
	case "volumes":
		for _, v := range s.volumes {
			if v.ID == id {
				return s.volumeEntry(v), hasTag(v.Tags, managedTag), true
			}
		}
	case "reserved_ips":
		for _, ip := range s.reservedIPs {
			if ip.IP == id {
				return s.reservedIPEntry(ip), s.ownIPs[id], true
			}
		}
	case "load_balancers":
		for _, l := range s.lbs {
			if l.ID == id {
				return s.lbEntry(l), hasManagedPrefix(l.Name), true
			}
		}
	case "spaces_keys":
		for _, k := range s.spacesKeys {
			if k != nil && k.AccessKey == id {
				return spacesKeyEntryOf(k), hasManagedPrefix(k.Name), true
			}
		}
	case "spaces_buckets":
		region, name, _ := strings.Cut(id, "/")
		bs, ok := s.bucketsIn(region)
		if !ok {
			return nil, false, true
		}
		for _, b := range bs {
			if b.Name == name {
				return &bucketEntry{id, name, region, b.Created}, hasManagedPrefix(name), true
			}
		}
	}
	return nil, false, true
}

// managedEntriesM4 lists every milestone 4 resource docli owns that the account can tell us about. Reserved IPs
// and buckets are not listed: the account cannot say which reserved IPs docli created, and buckets need a Spaces
// key and a region to be listed at all. Those entries are only kept up to date when the file already lists them.
func (s *snapshot) managedEntriesM4() []ManifestEntry {
	var out []ManifestEntry
	s.ensure("projects")
	for _, p := range s.projects {
		if hasManagedPrefix(p.Name) {
			out = append(out, &projectEntry{p.ID, p.Name, p.Purpose, p.Environment, p.CreatedAt})
		}
	}
	s.ensure("volumes")
	for _, v := range s.volumes {
		if hasTag(v.Tags, managedTag) {
			out = append(out, s.volumeEntry(v))
		}
	}
	s.ensure("load_balancers")
	for _, l := range s.lbs {
		if hasManagedPrefix(l.Name) {
			out = append(out, s.lbEntry(l))
		}
	}
	s.ensure("spaces_keys")
	for _, k := range s.spacesKeys {
		if k != nil && hasManagedPrefix(k.Name) {
			out = append(out, spacesKeyEntryOf(k))
		}
	}
	return out
}

// refreshListedOnly updates and prunes the entries of one section that the file already lists, and never adds
// one. It is for the kinds the account cannot say docli owns (reserved IPs) or cannot list without more
// information (buckets). A section that cannot be read is left exactly as it was.
func (m *Manifest) refreshListedOnly(s *snapshot, section string) error {
	old := m.sections[section]
	var keep []ManifestEntry
	for _, n := range old {
		e, managed, _ := s.entryM4(section, nodeID(n))
		if s.denied[section] {
			return nil
		}
		if e != nil && managed {
			keep = append(keep, e)
		}
	}
	m.sections[section] = nil
	for _, e := range keep {
		if err := m.Upsert(e); err != nil {
			return err
		}
	}
	return nil
}
