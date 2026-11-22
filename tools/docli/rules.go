package main

import (
	"fmt"
	"net/netip"
	"sort"
	"strconv"
	"strings"

	"github.com/digitalocean/godo"
)

// Rule is one firewall rule in a form people can type: in:tcp:22:from=10.116.0.0/20
type Rule struct {
	Dir      string // "in" or "out"
	Proto    string // tcp, udp, icmp
	Ports    string // "all", "22" or "8000-9000"
	Addrs    []string
	Tags     []string
	Droplets []int
	Other    int // sources the spec form cannot express (load balancers, Kubernetes), kept as a count
}

const ruleHelp = `Rule format:  DIRECTION:PROTOCOL:PORTS:TARGET[+TARGET...]
  DIRECTION  in | out
  PROTOCOL   tcp | udp | icmp
  PORTS      22, 8000-9000 or all (icmp has no ports: write all)
  TARGET     in: from=ADDR[,ADDR] from-tag=NAME from-droplet=ID
             out: to=ADDR[,ADDR] to-tag=NAME to-droplet=ID
  ADDR       an IP or CIDR, e.g. 10.116.0.0/20 or 0.0.0.0/0
Examples:  in:tcp:22:from=10.116.0.0/20   in:tcp:5432:from-tag=app   out:tcp:all:to=0.0.0.0/0`

// ParseRule is strict: anything it does not understand is an error.
func ParseRule(spec string) (Rule, error) {
	bad := func(format string, a ...any) (Rule, error) {
		return Rule{}, fmt.Errorf("bad rule %q: %s\n%s", spec, fmt.Sprintf(format, a...), ruleHelp)
	}
	parts := strings.SplitN(strings.TrimSpace(spec), ":", 4) // the target may hold IPv6 colons
	if len(parts) != 4 {
		return bad("expected four parts separated by colons")
	}
	r := Rule{Dir: parts[0], Proto: parts[1]}
	if r.Dir != "in" && r.Dir != "out" {
		return bad("direction must be in or out, not %q", parts[0])
	}
	if r.Proto != "tcp" && r.Proto != "udp" && r.Proto != "icmp" {
		return bad("protocol must be tcp, udp or icmp, not %q", parts[1])
	}
	ports, err := parsePorts(parts[2], r.Proto)
	if err != nil {
		return bad("%v", err)
	}
	r.Ports = ports
	if parts[3] == "" {
		return bad("no target given")
	}
	wantKey := map[string]string{"in": "from", "out": "to"}[r.Dir]
	for _, t := range strings.Split(parts[3], "+") {
		k, v, ok := strings.Cut(t, "=")
		if !ok || v == "" {
			return bad("target %q must look like %s=VALUE", t, wantKey)
		}
		switch k {
		case wantKey:
			for _, a := range strings.Split(v, ",") {
				n, err := normalizeAddr(a)
				if err != nil {
					return bad("%v", err)
				}
				r.Addrs = append(r.Addrs, n)
			}
		case wantKey + "-tag":
			for _, tg := range strings.Split(v, ",") {
				if err := validateTag(tg); err != nil {
					return bad("%v", err)
				}
				r.Tags = append(r.Tags, tg)
			}
		case wantKey + "-droplet":
			for _, d := range strings.Split(v, ",") {
				id, err := strconv.Atoi(d)
				if err != nil || id <= 0 {
					return bad("droplet id %q must be a number", d)
				}
				r.Droplets = append(r.Droplets, id)
			}
		default:
			return bad("%q is not a target for %s rules (use %s, %s-tag or %s-droplet)", k, r.Dir, wantKey, wantKey, wantKey)
		}
	}
	r.normalize()
	return r, nil
}

func parsePorts(p, proto string) (string, error) {
	if proto == "icmp" {
		if p != "all" {
			return "", fmt.Errorf("icmp has no ports; write all")
		}
		return "all", nil
	}
	if p == "all" {
		return "all", nil
	}
	lo, hi, isRange := strings.Cut(p, "-")
	l, err := strconv.Atoi(lo)
	if err != nil || l < 1 || l > 65535 || strconv.Itoa(l) != lo {
		return "", fmt.Errorf("ports must be all, a number 1-65535 or a range like 8000-9000, not %q", p)
	}
	if !isRange {
		return lo, nil
	}
	h, err := strconv.Atoi(hi)
	if err != nil || h < 1 || h > 65535 || strconv.Itoa(h) != hi {
		return "", fmt.Errorf("the end of the port range %q is not a number 1-65535", p)
	}
	if h < l {
		return "", fmt.Errorf("the port range %q runs backwards", p)
	}
	if h == l {
		return lo, nil
	}
	return p, nil
}

func normalizeAddr(a string) (string, error) {
	if strings.Contains(a, "/") {
		p, err := netip.ParsePrefix(a)
		if err != nil {
			return "", fmt.Errorf("%q is not a valid CIDR range", a)
		}
		return p.String(), nil
	}
	ip, err := netip.ParseAddr(a)
	if err != nil {
		return "", fmt.Errorf("%q is not a valid IP address or CIDR range", a)
	}
	return ip.String(), nil
}

func (r *Rule) normalize() {
	sort.Strings(r.Addrs)
	sort.Strings(r.Tags)
	sort.Ints(r.Droplets)
}

// Spec writes the rule back in the form ParseRule accepts.
func (r Rule) Spec() string {
	k := map[string]string{"in": "from", "out": "to"}[r.Dir]
	var ts []string
	if len(r.Addrs) > 0 {
		ts = append(ts, k+"="+strings.Join(r.Addrs, ","))
	}
	if len(r.Tags) > 0 {
		ts = append(ts, k+"-tag="+strings.Join(r.Tags, ","))
	}
	if len(r.Droplets) > 0 {
		var ids []string
		for _, d := range r.Droplets {
			ids = append(ids, strconv.Itoa(d))
		}
		ts = append(ts, k+"-droplet="+strings.Join(ids, ","))
	}
	return fmt.Sprintf("%s:%s:%s:%s", r.Dir, r.Proto, r.Ports, strings.Join(ts, "+"))
}

// Describe says what the rule does in plain English.
func (r Rule) Describe() string {
	dir, prep := "incoming", "from"
	if r.Dir == "out" {
		dir, prep = "outgoing", "to"
	}
	what := strings.ToUpper(r.Proto)
	switch {
	case r.Proto == "icmp":
		what = "ICMP (ping)"
	case r.Ports == "all":
		what += " on all ports"
	case strings.Contains(r.Ports, "-"):
		what += " on ports " + r.Ports
	default:
		what += " on port " + r.Ports
	}
	var who []string
	for _, a := range r.Addrs {
		if a == "0.0.0.0/0" || a == "::/0" {
			who = append(who, "anywhere ("+a+")")
		} else {
			who = append(who, a)
		}
	}
	for _, t := range r.Tags {
		who = append(who, "droplets tagged "+t)
	}
	for _, d := range r.Droplets {
		who = append(who, fmt.Sprintf("droplet %d", d))
	}
	if r.Other > 0 {
		who = append(who, fmt.Sprintf("%d other source(s) not shown here", r.Other))
	}
	return fmt.Sprintf("Allow %s %s %s %s", dir, what, prep, strings.Join(who, ", "))
}

func (r Rule) Equal(o Rule) bool {
	r.normalize()
	o.normalize()
	return r.Spec() == o.Spec()
}

func apiPorts(r Rule) string {
	if r.Proto == "icmp" {
		return ""
	}
	return r.Ports
}

func (r Rule) inbound() godo.InboundRule {
	return godo.InboundRule{Protocol: r.Proto, PortRange: apiPorts(r),
		Sources: &godo.Sources{Addresses: r.Addrs, Tags: r.Tags, DropletIDs: r.Droplets}}
}

func (r Rule) outbound() godo.OutboundRule {
	return godo.OutboundRule{Protocol: r.Proto, PortRange: apiPorts(r),
		Destinations: &godo.Destinations{Addresses: r.Addrs, Tags: r.Tags, DropletIDs: r.Droplets}}
}

func apiPortsToRule(p string) string {
	if p == "" || p == "0" || p == "all" {
		return "all"
	}
	return p
}

func ruleFromInbound(in godo.InboundRule) Rule {
	r := Rule{Dir: "in", Proto: in.Protocol, Ports: apiPortsToRule(in.PortRange)}
	if s := in.Sources; s != nil {
		r.Addrs, r.Tags, r.Droplets = append([]string(nil), s.Addresses...), append([]string(nil), s.Tags...), append([]int(nil), s.DropletIDs...)
		r.Other = len(s.LoadBalancerUIDs) + len(s.KubernetesIDs)
	}
	r.normalize()
	return r
}

func ruleFromOutbound(o godo.OutboundRule) Rule {
	r := Rule{Dir: "out", Proto: o.Protocol, Ports: apiPortsToRule(o.PortRange)}
	if s := o.Destinations; s != nil {
		r.Addrs, r.Tags, r.Droplets = append([]string(nil), s.Addresses...), append([]string(nil), s.Tags...), append([]int(nil), s.DropletIDs...)
		r.Other = len(s.LoadBalancerUIDs) + len(s.KubernetesIDs)
	}
	r.normalize()
	return r
}

// firewallRules lists every rule of a firewall, inbound first.
func firewallRules(f godo.Firewall) []Rule {
	var out []Rule
	for _, in := range f.InboundRules {
		out = append(out, ruleFromInbound(in))
	}
	for _, o := range f.OutboundRules {
		out = append(out, ruleFromOutbound(o))
	}
	return out
}

func rulesRequest(rules []Rule) *godo.FirewallRulesRequest {
	rr := &godo.FirewallRulesRequest{InboundRules: []godo.InboundRule{}, OutboundRules: []godo.OutboundRule{}}
	for _, r := range rules {
		if r.Dir == "in" {
			rr.InboundRules = append(rr.InboundRules, r.inbound())
		} else {
			rr.OutboundRules = append(rr.OutboundRules, r.outbound())
		}
	}
	return rr
}
