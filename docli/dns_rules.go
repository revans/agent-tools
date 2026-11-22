package main

import (
	"fmt"
	"net/netip"
	"regexp"
	"strings"
	"unicode"

	"github.com/digitalocean/godo"
)

// DNS record validation. Names are relative to the domain ("www", "@" for the apex), so a record cannot be
// pointed at the wrong zone by writing a full name. The limits here (TTL minimum, TXT length) are docli's
// sanity checks, not figures read from DigitalOcean's documentation; the API has the final say.

var dnsTypes = []string{"A", "AAAA", "CNAME", "TXT", "MX", "NS", "CAA"}

const (
	defaultTTL = 1800
	minTTL     = 30
	maxTTL     = 2147483647
	maxTXT     = 2048
)

var recordLabelRE = regexp.MustCompile(`^[a-z0-9_]([a-z0-9_-]{0,61}[a-z0-9_])?$`)

// recordSpec is a record as the person described it.
type recordSpec struct {
	Type     string
	Name     string // relative; "@" is the apex
	Data     string
	TTL      int
	Priority int // MX only; -1 means not given
	Flags    int // CAA only
	Tag      string
}

func validDNSType(t string) bool { return contains(dnsTypes, t) }

// normalizeRecordName lowercases a relative name and rejects full names.
func normalizeRecordName(domain, name string) (string, error) {
	n := strings.ToLower(strings.TrimSpace(name))
	if n == "" {
		return "", fmt.Errorf("a record name is required: use @ for the apex of %s, or a name like www", domain)
	}
	if n == "@" {
		return n, nil
	}
	if strings.HasSuffix(n, ".") {
		return "", fmt.Errorf("record names are relative to the domain: write %q, not %q", strings.TrimSuffix(strings.TrimSuffix(n, "."), "."+domain), name)
	}
	if n == domain || strings.HasSuffix(n, "."+domain) {
		rel := strings.TrimSuffix(n, "."+domain)
		if rel == domain {
			rel = "@"
		}
		return "", fmt.Errorf("record names are relative to the domain, so %q would sit under %s.%s. Write %q instead (use @ for the apex)", name, n, domain, rel)
	}
	if len(n)+len(domain)+1 > 253 {
		return "", fmt.Errorf("the name %q is too long", name)
	}
	labels := strings.Split(n, ".")
	for i, l := range labels {
		if l == "*" && i == 0 {
			continue
		}
		if !recordLabelRE.MatchString(l) {
			return "", fmt.Errorf("%q is not a valid record name: use letters, digits, dash and underscore in each dot-separated part (1-63 characters), or * as the first part", name)
		}
	}
	return n, nil
}

// validateDNSHost checks the target of a CNAME, MX or NS record.
func validateDNSHost(kind, data string) error {
	if data == "@" {
		return nil
	}
	h := strings.TrimSuffix(data, ".")
	if h == "" {
		return fmt.Errorf("a %s record needs a host name as its data", kind)
	}
	if _, err := netip.ParseAddr(h); err == nil {
		return fmt.Errorf("%q is an IP address; a %s record points at a host name (use an A or AAAA record for an address)", data, kind)
	}
	if len(h) > 253 {
		return fmt.Errorf("%q is too long for a host name", data)
	}
	for _, l := range strings.Split(h, ".") {
		if !recordLabelRE.MatchString(strings.ToLower(l)) {
			return fmt.Errorf("%q is not a valid host name for a %s record", data, kind)
		}
	}
	return nil
}

// validateRecord checks everything that can be checked without the account. The name must already be normalized.
func validateRecord(s *recordSpec) error {
	if !validDNSType(s.Type) {
		return fmt.Errorf("record type %q is not supported: use one of %s", s.Type, strings.Join(dnsTypes, ", "))
	}
	if s.TTL < minTTL || s.TTL > maxTTL {
		return fmt.Errorf("--ttl %d is out of range: use %d to %d seconds", s.TTL, minTTL, maxTTL)
	}
	if s.Data == "" {
		return fmt.Errorf("a record needs --data")
	}
	if s.Type != "MX" && s.Priority != -1 {
		return fmt.Errorf("--priority only applies to MX records")
	}
	if s.Type != "CAA" && (s.Flags != 0 || s.Tag != "") {
		return fmt.Errorf("--flags and --tag only apply to CAA records")
	}
	switch s.Type {
	case "A":
		if ip, err := netip.ParseAddr(s.Data); err != nil || !ip.Is4() {
			return fmt.Errorf("%q is not an IPv4 address (an A record needs one, like 203.0.113.10)", s.Data)
		}
	case "AAAA":
		if ip, err := netip.ParseAddr(s.Data); err != nil || !ip.Is6() || ip.Is4In6() {
			return fmt.Errorf("%q is not an IPv6 address (an AAAA record needs one, like 2001:db8::1)", s.Data)
		}
	case "CNAME":
		if s.Name == "@" {
			return fmt.Errorf("a CNAME cannot be at the apex (@): the apex must also hold NS and SOA records. Use an A or AAAA record there")
		}
		if err := validateDNSHost("CNAME", s.Data); err != nil {
			return err
		}
	case "NS":
		if err := validateDNSHost("NS", s.Data); err != nil || s.Data == "@" {
			if err == nil {
				err = fmt.Errorf("an NS record cannot point at @")
			}
			return err
		}
	case "MX":
		if s.Priority < 0 || s.Priority > 65535 {
			return fmt.Errorf("an MX record needs --priority between 0 and 65535 (lower is tried first)")
		}
		if err := validateDNSHost("MX", s.Data); err != nil {
			return err
		}
	case "TXT":
		if len(s.Data) > maxTXT {
			return fmt.Errorf("the TXT data is %d characters; docli accepts at most %d", len(s.Data), maxTXT)
		}
		for _, r := range s.Data {
			if unicode.IsControl(r) {
				return fmt.Errorf("TXT data cannot contain line breaks or control characters")
			}
		}
	case "CAA":
		if !contains([]string{"issue", "issuewild", "iodef"}, s.Tag) {
			return fmt.Errorf("a CAA record needs --tag issue, issuewild or iodef")
		}
		if s.Flags != 0 && s.Flags != 128 {
			return fmt.Errorf("--flags for a CAA record must be 0 or 128")
		}
	}
	return nil
}

// recordConflict checks a new or changed record against the zone's current records (ignoring the record with
// id `except`, for updates).
func recordConflict(s *recordSpec, existing []godo.DomainRecord, except int) error {
	for _, e := range existing {
		if e.ID == except {
			continue
		}
		sameName := e.Name == s.Name
		if sameName && e.Type == s.Type && e.Data == s.Data {
			return fmt.Errorf("the zone already has that record (id %d: %s %s %s)", e.ID, e.Type, e.Name, e.Data)
		}
		if !sameName {
			continue
		}
		if s.Type == "CNAME" {
			return fmt.Errorf("%q already has a %s record (id %d), and a CNAME cannot share its name with any other record", s.Name, e.Type, e.ID)
		}
		if e.Type == "CNAME" {
			return fmt.Errorf("%q is already a CNAME (id %d), so it cannot also hold a %s record", s.Name, e.ID, s.Type)
		}
	}
	return nil
}

var domainRE = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?$`)

// validateDomainName checks a registrable-looking domain: at least two labels, no wildcard, no trailing dot.
func validateDomainName(n string) error {
	if n != strings.ToLower(n) {
		return fmt.Errorf("%q must be lower case", n)
	}
	if len(n) > 253 || !strings.Contains(n, ".") {
		return fmt.Errorf("%q is not a domain name such as example.com", n)
	}
	for _, l := range strings.Split(n, ".") {
		if !domainRE.MatchString(l) {
			return fmt.Errorf("%q is not a valid domain name: use letters, digits and dashes in each dot-separated part, no trailing dot, no wildcard", n)
		}
	}
	return nil
}

func fqdn(domain, name string) string {
	if name == "@" || name == "" {
		return domain
	}
	return name + "." + domain
}

func (s recordSpec) request() *godo.DomainRecordEditRequest {
	req := &godo.DomainRecordEditRequest{Type: s.Type, Name: s.Name, Data: s.Data, TTL: s.TTL}
	if s.Type == "MX" {
		req.Priority = s.Priority
	}
	if s.Type == "CAA" {
		req.Flags, req.Tag = s.Flags, s.Tag
	}
	return req
}
