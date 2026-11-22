package main

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/digitalocean/godo"
)

// Load balancers. They are named, and the API only accepts their own tags when they are created, so docli does
// not rely on a tag: it owns the load balancers whose names start with docli-, the rule SSH keys, VPCs and
// firewalls follow. The API does not report a price for a load balancer, so none is shown.

func (a *App) cmdLB(args []string) error {
	if len(args) == 0 {
		return usagef("usage: docli lb list|get|create|delete (try: docli help lb)")
	}
	switch args[0] {
	case "list":
		return a.lbList(args[1:])
	case "get":
		return a.lbGet(args[1:])
	case "create":
		return a.lbCreate(args[1:])
	case "delete":
		return a.lbDelete(args[1:])
	}
	return usagef("unknown lb command %q (list, get, create, delete)", args[0])
}

func findLB(lbs []godo.LoadBalancer, ref string) (*godo.LoadBalancer, error) {
	var hits []godo.LoadBalancer
	for _, l := range lbs {
		if l.Name == ref || l.ID == ref {
			hits = append(hits, l)
		}
	}
	if len(hits) == 0 {
		for _, l := range lbs {
			if l.Name == namePrefix+ref {
				hits = append(hits, l)
			}
		}
	}
	switch len(hits) {
	case 0:
		return nil, fmt.Errorf("no load balancer named %q (or with that id). List them with: docli lb list", ref)
	case 1:
		return &hits[0], nil
	}
	return nil, fmt.Errorf("%d load balancers are named %q; use the id instead", len(hits), ref)
}

var (
	lbEntryProtos  = []string{"http", "https", "http2", "http3", "tcp", "udp"}
	lbTargetProtos = []string{"http", "https", "http2", "tcp", "udp"}
	lbHealthProtos = []string{"http", "https", "tcp"}
)

func splitProtoPort(s string) (string, int, error) {
	proto, port, ok := strings.Cut(s, ":")
	n, err := strconv.Atoi(port)
	if !ok || err != nil || n < 1 || n > 65535 {
		return "", 0, fmt.Errorf("%q is not PROTOCOL:PORT with a port from 1 to 65535", s)
	}
	return strings.ToLower(proto), n, nil
}

// parseForward reads ENTRY_PROTOCOL:PORT=TARGET_PROTOCOL:PORT[,cert=ID][,passthrough].
func parseForward(spec string) (godo.ForwardingRule, error) {
	parts := strings.Split(spec, ",")
	entry, target, ok := strings.Cut(parts[0], "=")
	if !ok {
		return godo.ForwardingRule{}, fmt.Errorf("the forwarding rule %q must look like http:80=http:8080 (entry=target)", spec)
	}
	var f godo.ForwardingRule
	var err error
	if f.EntryProtocol, f.EntryPort, err = splitProtoPort(entry); err != nil {
		return f, fmt.Errorf("forwarding rule %q: entry %v", spec, err)
	}
	if f.TargetProtocol, f.TargetPort, err = splitProtoPort(target); err != nil {
		return f, fmt.Errorf("forwarding rule %q: target %v", spec, err)
	}
	if !contains(lbEntryProtos, f.EntryProtocol) {
		return f, fmt.Errorf("forwarding rule %q: entry protocol %q must be one of %s", spec, f.EntryProtocol, strings.Join(lbEntryProtos, ", "))
	}
	if !contains(lbTargetProtos, f.TargetProtocol) {
		return f, fmt.Errorf("forwarding rule %q: target protocol %q must be one of %s", spec, f.TargetProtocol, strings.Join(lbTargetProtos, ", "))
	}
	for _, opt := range parts[1:] {
		switch {
		case opt == "passthrough":
			f.TlsPassthrough = true
		case strings.HasPrefix(opt, "cert=") && len(opt) > 5:
			f.CertificateID = opt[5:]
		default:
			return f, fmt.Errorf("forwarding rule %q: unknown option %q (cert=ID or passthrough)", spec, opt)
		}
	}
	if f.TlsPassthrough && f.CertificateID != "" {
		return f, fmt.Errorf("forwarding rule %q: use either cert=ID (the load balancer ends TLS) or passthrough (the droplet does), not both", spec)
	}
	if contains([]string{"https", "http2", "http3"}, f.EntryProtocol) && f.CertificateID == "" && !f.TlsPassthrough {
		return f, fmt.Errorf("forwarding rule %q: an %s entry needs a certificate (cert=ID) or passthrough, so the load balancer knows who ends TLS", spec, f.EntryProtocol)
	}
	return f, nil
}

func lbRuleSpec(f godo.ForwardingRule) string {
	s := fmt.Sprintf("%s:%d=%s:%d", f.EntryProtocol, f.EntryPort, f.TargetProtocol, f.TargetPort)
	if f.CertificateID != "" {
		s += ",cert=" + f.CertificateID
	}
	if f.TlsPassthrough {
		s += ",passthrough"
	}
	return s
}

func describeLBRule(f godo.ForwardingRule) string {
	s := fmt.Sprintf("%s port %d goes to %s port %d on the droplets", strings.ToUpper(f.EntryProtocol), f.EntryPort, strings.ToUpper(f.TargetProtocol), f.TargetPort)
	switch {
	case f.CertificateID != "":
		s += fmt.Sprintf("; TLS ends at the load balancer with certificate %s", f.CertificateID)
	case f.TlsPassthrough:
		s += "; TLS passes through to the droplets"
	}
	return s
}

// parseHealth reads PROTOCOL:PORT[/PATH][,interval=N][,timeout=N][,healthy=N][,unhealthy=N].
func parseHealth(spec string) (*godo.HealthCheck, error) {
	parts := strings.Split(spec, ",")
	head, path, _ := strings.Cut(parts[0], "/")
	proto, port, err := splitProtoPort(head)
	if err != nil {
		return nil, fmt.Errorf("health check %q: %v", spec, err)
	}
	if !contains(lbHealthProtos, proto) {
		return nil, fmt.Errorf("health check %q: protocol %q must be one of %s", spec, proto, strings.Join(lbHealthProtos, ", "))
	}
	h := &godo.HealthCheck{Protocol: proto, Port: port, CheckIntervalSeconds: 10, ResponseTimeoutSeconds: 5, HealthyThreshold: 5, UnhealthyThreshold: 3}
	if proto != "tcp" {
		h.Path = "/" + path
	} else if path != "" {
		return nil, fmt.Errorf("health check %q: a tcp check has no path", spec)
	}
	for _, opt := range parts[1:] {
		k, v, ok := strings.Cut(opt, "=")
		n, err := strconv.Atoi(v)
		if !ok || err != nil || n < 1 || n > 300 {
			return nil, fmt.Errorf("health check %q: %q must look like interval=10 (a number from 1 to 300)", spec, opt)
		}
		switch k {
		case "interval":
			h.CheckIntervalSeconds = n
		case "timeout":
			h.ResponseTimeoutSeconds = n
		case "healthy":
			h.HealthyThreshold = n
		case "unhealthy":
			h.UnhealthyThreshold = n
		default:
			return nil, fmt.Errorf("health check %q: unknown option %q (interval, timeout, healthy, unhealthy)", spec, k)
		}
	}
	return h, nil
}

func lbHealthSpec(h *godo.HealthCheck) string {
	if h == nil {
		return ""
	}
	return fmt.Sprintf("%s:%d%s,interval=%d,timeout=%d,healthy=%d,unhealthy=%d", h.Protocol, h.Port, h.Path, h.CheckIntervalSeconds, h.ResponseTimeoutSeconds, h.HealthyThreshold, h.UnhealthyThreshold)
}

func lbRegion(l godo.LoadBalancer) string {
	if l.Region != nil {
		return l.Region.Slug
	}
	return ""
}

func (r *Run) lbTargets(l godo.LoadBalancer, names map[int]string) string {
	if l.Tag != "" {
		return "droplets tagged " + l.Tag
	}
	var out []string
	for _, id := range l.DropletIDs {
		if n := names[id]; n != "" {
			out = append(out, n)
		} else {
			out = append(out, itoa(id))
		}
	}
	return orDash(strings.Join(out, ", "))
}

func (a *App) lbList(args []string) error {
	fs, g := a.newFlags("lb list")
	if _, err := a.parseFlags(fs, args, "lb list"); err != nil {
		return err
	}
	r, err := a.start(*g)
	if err != nil {
		return err
	}
	lbs, err := r.Cloud.ListLoadBalancers(r.Ctx)
	if err != nil {
		return err
	}
	var shown []godo.LoadBalancer
	for _, l := range lbs {
		if r.G.Region == "" || lbRegion(l) == r.G.Region {
			shown = append(shown, l)
		}
	}
	if g.JSON {
		out := []lbJSON{}
		for _, l := range shown {
			out = append(out, lbView(l))
		}
		return r.printJSON(out)
	}
	var rows [][]string
	for _, l := range shown {
		rows = append(rows, []string{l.ID, l.Name, orDash(l.IP), lbRegion(l), orDash(l.Status), itoa(len(l.ForwardingRules)), yesNo(hasManagedPrefix(l.Name))})
	}
	printTable(a.Stdout, []string{"ID", "NAME", "IP", "REGION", "STATUS", "RULES", "MANAGED"}, rows)
	return nil
}

func (a *App) lbGet(args []string) error {
	fs, g := a.newFlags("lb get")
	pos, err := a.parseFlags(fs, args, "lb get")
	if err != nil {
		return err
	}
	if len(pos) != 1 {
		return usagef("usage: docli lb get NAME|ID")
	}
	r, err := a.start(*g)
	if err != nil {
		return err
	}
	lbs, err := r.Cloud.ListLoadBalancers(r.Ctx)
	if err != nil {
		return err
	}
	l, err := findLB(lbs, pos[0])
	if err != nil {
		return err
	}
	if g.JSON {
		return r.printJSON(lbView(*l))
	}
	size := firstNonEmpty(l.SizeSlug, "-")
	if l.SizeUnit > 0 {
		size = fmt.Sprintf("%s (%d unit(s))", size, l.SizeUnit)
	}
	fmt.Fprintf(a.Stdout, "ID:        %s\nName:      %s\nManaged:   %s\nIP:        %s\nStatus:    %s\nRegion:    %s\nSize:      %s\nAlgorithm: %s\nVPC:       %s\nTargets:   %s\nHealth:    %s\nCost:      not shown (the API does not report load balancer prices)\nCreated:   %s\nRules:\n",
		l.ID, l.Name, yesNo(hasManagedPrefix(l.Name)), orDash(l.IP), orDash(l.Status), lbRegion(*l), size, orDash(l.Algorithm), orDash(l.VPCUUID),
		r.lbTargets(*l, r.dropletNamesByID()), orDash(lbHealthSpec(l.HealthCheck)), orDash(l.Created))
	for _, f := range l.ForwardingRules {
		fmt.Fprintf(a.Stdout, "  - %s   (%s)\n", lbRuleSpec(f), describeLBRule(f))
	}
	return nil
}

func (a *App) lbCreate(args []string) error {
	fs, g := a.newFlags("lb create")
	name := fs.String("name", "", "")
	size := fs.String("size", "", "")
	sizeUnit := fs.Uint("size-unit", 0, "")
	algo := fs.String("algorithm", "round_robin", "")
	health := fs.String("health-check", "", "")
	tag := fs.String("tag", "", "")
	vpcRef := fs.String("vpc", "", "")
	redirect := fs.Bool("redirect-http-to-https", false, "")
	projRef := fs.String("project", "", "")
	wait := fs.Bool("wait", false, "")
	waitTimeout := fs.Duration("wait-timeout", 10*time.Minute, "")
	var forwards, dropRefs stringList
	fs.Var(&forwards, "forward", "")
	fs.Var(&dropRefs, "droplet", "")
	mo := addManifestFlags(fs)
	pos, err := a.parseFlags(fs, args, "lb create")
	if err != nil {
		return err
	}
	if len(pos) > 0 || *name == "" || len(forwards) == 0 {
		return usagef("usage: docli lb create --name NAME --region R --forward http:80=http:8080 [--forward ...] (--droplet D... | --tag T) [--vpc V] [--health-check http:8080/healthz] [--size lb-small | --size-unit N] [--algorithm round_robin|least_connections] [--redirect-http-to-https] [--project P] [--wait]")
	}
	final := ensureNamePrefix(*name)
	if err := validateHostname(final); err != nil {
		return usagef("the load balancer name %v", err)
	}
	if (len(dropRefs) == 0) == (*tag == "") {
		return usagef("name the droplets behind it with --droplet NAME (repeatable) or --tag TAG, one of the two")
	}
	if *tag != "" {
		if err := validateTag(*tag); err != nil {
			return usagef("%v", err)
		}
	}
	if *size != "" && *sizeUnit != 0 {
		return usagef("--size and --size-unit cannot be used together")
	}
	if *algo != "round_robin" && *algo != "least_connections" {
		return usagef("--algorithm must be round_robin or least_connections")
	}
	var rules []godo.ForwardingRule
	seenEntry := map[string]bool{}
	for _, f := range forwards {
		rule, err := parseForward(f)
		if err != nil {
			return usagef("%v", err)
		}
		k := fmt.Sprintf("%s:%d", rule.EntryProtocol, rule.EntryPort)
		if seenEntry[k] {
			return usagef("two forwarding rules listen on %s", k)
		}
		seenEntry[k] = true
		rules = append(rules, rule)
	}
	var hc *godo.HealthCheck
	if *health != "" {
		if hc, err = parseHealth(*health); err != nil {
			return usagef("%v", err)
		}
	}
	r, err := a.start(*g)
	if err != nil {
		return err
	}
	region := r.G.Region
	if region == "" {
		return usagef("a region is required: --region nyc3 (or set DOCLI_REGION)")
	}
	if err := r.checkRegion(region); err != nil {
		return err
	}
	proj, err := r.lookupProject(*projRef)
	if err != nil {
		return err
	}
	var drops []*godo.Droplet
	var ids []int
	var dnames []string
	for _, ref := range dropRefs {
		d, err := r.resolveDroplet(ref)
		if err != nil {
			return err
		}
		if dr := dropletView(*d).Region; dr != region {
			return fmt.Errorf("droplet %q is in %s but the load balancer is for %s: a load balancer can only front droplets in its own region", d.Name, dr, region)
		}
		drops = append(drops, d)
		ids = append(ids, d.ID)
		dnames = append(dnames, d.Name)
	}
	req := &godo.LoadBalancerRequest{Name: final, Algorithm: *algo, Region: region, SizeSlug: *size, SizeUnit: uint32(*sizeUnit),
		ForwardingRules: rules, HealthCheck: hc, DropletIDs: ids, Tag: *tag, RedirectHttpToHttps: *redirect}
	vpcDesc := "the default VPC for " + region
	vpcName := a.envOr(*vpcRef, "DOCLI_VPC")
	switch {
	case vpcName != "":
		vpcs, err := r.Cloud.ListVPCs(r.Ctx)
		if err != nil {
			return err
		}
		v, err := findVPC(vpcs, vpcName)
		if err != nil {
			return err
		}
		if v.RegionSlug != region {
			return fmt.Errorf("VPC %q is in %s but the load balancer is for %s", v.Name, v.RegionSlug, region)
		}
		for _, d := range drops {
			if d.VPCUUID != v.ID {
				return fmt.Errorf("droplet %q is not in VPC %q, and a load balancer reaches its droplets over its own VPC. Move the droplet or pick the VPC it is in", d.Name, v.Name)
			}
		}
		req.VPCUUID = v.ID
		vpcDesc = fmt.Sprintf("VPC %s (%s)", v.Name, v.IPRange)
	case len(drops) > 0:
		common := drops[0].VPCUUID
		for _, d := range drops {
			if d.VPCUUID != common {
				return fmt.Errorf("the droplets are in different VPCs, so docli cannot choose one for the load balancer. Name it with --vpc")
			}
		}
		req.VPCUUID = common
		vpcDesc = "VPC " + common + " (the one the droplets are in)"
	}
	if hc != nil && hc.Port != 0 && !lbTargetsPort(rules, hc.Port) {
		return usagef("the health check port %d is not a target port of any forwarding rule (%s)", hc.Port, strings.Join(forwards, ", "))
	}
	lbs, err := r.Cloud.ListLoadBalancers(r.Ctx)
	if err != nil {
		return err
	}
	for _, l := range lbs {
		if l.Name == final {
			return fmt.Errorf("a load balancer named %q already exists (id %s). Nothing was changed", final, l.ID)
		}
	}

	p := Plan{Action: "lb.create", Summary: fmt.Sprintf("create the load balancer %q in %s", final, region)}
	sz := firstNonEmpty(*size, "the API default")
	if *sizeUnit > 0 {
		sz = fmt.Sprintf("%d size unit(s)", *sizeUnit)
	}
	p.Steps = []string{"size: " + sz, "algorithm: " + *algo, "network: " + vpcDesc}
	if *tag != "" {
		p.Steps = append(p.Steps, "targets: every droplet tagged "+*tag+" (now and in future)")
	} else {
		p.Steps = append(p.Steps, "targets: droplets "+strings.Join(dnames, ", "))
	}
	for _, f := range rules {
		p.Steps = append(p.Steps, "forward "+lbRuleSpec(f)+"  =  "+describeLBRule(f))
	}
	if hc != nil {
		p.Steps = append(p.Steps, "health check: "+lbHealthSpec(hc))
	} else {
		p.Warnings = append(p.Warnings, "no --health-check: DigitalOcean applies its own default check, so a droplet that is up but not serving may still receive traffic")
	}
	if *redirect {
		p.Steps = append(p.Steps, "HTTP requests are redirected to HTTPS")
	}
	p.Steps = append(p.Steps, "monthly cost: not shown. The API does not report load balancer prices, so docli will not guess; see DigitalOcean's pricing page. A load balancer bills while it exists",
		"it can be provisioned for a few minutes; add --wait to wait until it is active",
		fmt.Sprintf("ownership: the name gets the prefix %s so docli can tell it created this load balancer", namePrefix))
	if s := projectStep(proj); s != "" {
		p.Steps = append(p.Steps, s)
	}
	ok, err := r.Approve(p, approveOpts{})
	if !ok || err != nil {
		return err
	}
	l, err := r.Cloud.CreateLoadBalancer(r.Ctx, req)
	if err != nil {
		return madeOrUnknown("load balancer", err, "docli lb list")
	}
	created := l.ID
	r.infof("created load balancer %s (%s), status %s", l.ID, l.Name, orDash(l.Status))
	if *wait {
		if l, err = r.waitForLB(created, *waitTimeout); err != nil {
			r.updateManifest(*mo, false, change{Section: "load_balancers", ID: created})
			return err
		}
	}
	r.updateManifest(*mo, false, change{Section: "load_balancers", ID: created})
	if err := r.putInProject(proj, l.URN(), "load balancer", l.Name); err != nil {
		return err
	}
	if g.JSON {
		return r.printJSON(lbView(*l))
	}
	fmt.Fprintf(a.Stdout, "Created load balancer %q (id %s), status %s, IP %s\n", l.Name, l.ID, orDash(l.Status), orDash(l.IP))
	return nil
}

func lbTargetsPort(rules []godo.ForwardingRule, port int) bool {
	for _, f := range rules {
		if f.TargetPort == port {
			return true
		}
	}
	return false
}

// checkRegion fails early, before a plan is shown, for a region that does not exist or takes nothing new.
func (r *Run) checkRegion(slug string) error {
	regions, err := r.Cloud.ListRegions(r.Ctx)
	if err != nil {
		return err
	}
	for _, x := range regions {
		if x.Slug == slug {
			if !x.Available {
				return fmt.Errorf("region %s is not available for new resources right now. See: docli regions", slug)
			}
			return nil
		}
	}
	return fmt.Errorf("unknown region %q. See: docli regions", slug)
}

func (r *Run) waitForLB(id string, timeout time.Duration) (*godo.LoadBalancer, error) {
	const every = 5 * time.Second
	var elapsed time.Duration
	for {
		lbs, err := r.Cloud.ListLoadBalancers(r.Ctx)
		if err != nil {
			return nil, fmt.Errorf("lost track of load balancer %s while waiting: %v. It still exists; check with: docli lb get %s", id, err, id)
		}
		for i := range lbs {
			if lbs[i].ID != id {
				continue
			}
			if lbs[i].Status == "active" {
				r.infof("load balancer %s is active", id)
				return &lbs[i], nil
			}
			if elapsed >= timeout {
				return &lbs[i], fmt.Errorf("load balancer %s is still %q after %s. It still exists and is not deleted; check with: docli lb get %s", id, lbs[i].Status, timeout, id)
			}
			r.infof("waiting for load balancer %s to be active: now %s (%ds elapsed)", id, orDash(lbs[i].Status), int(elapsed.Seconds()))
		}
		if err := r.Sleep(r.Ctx, every); err != nil {
			return nil, err
		}
		elapsed += every
	}
}

func (a *App) lbDelete(args []string) error {
	fs, g := a.newFlags("lb delete")
	force := fs.Bool("force-unmanaged", false, "")
	confirm := fs.String("confirm-name", "", "")
	mo := addManifestFlags(fs)
	pos, err := a.parseFlags(fs, args, "lb delete")
	if err != nil {
		return err
	}
	if len(pos) != 1 {
		return usagef("usage: docli lb delete NAME|ID [--yes --confirm-name NAME]")
	}
	r, err := a.start(*g)
	if err != nil {
		return err
	}
	lbs, err := r.Cloud.ListLoadBalancers(r.Ctx)
	if err != nil {
		return err
	}
	l, err := findLB(lbs, pos[0])
	if err != nil {
		return err
	}
	ok, err := r.ApproveDelete(deleteSpec{Kind: "load balancer", Name: l.Name, Managed: hasManagedPrefix(l.Name), Rule: "a name starting with " + namePrefix, Always: true,
		Steps: []string{fmt.Sprintf("delete load balancer %q (id %s, IP %s, %d forwarding rule(s)); its droplets are not touched", l.Name, l.ID, orDash(l.IP), len(l.ForwardingRules)),
			fmt.Sprintf("traffic sent to %s stops being served, and DNS records or firewalls that point at it will break", orDash(l.IP))}}, *force, *confirm)
	if !ok || err != nil {
		return err
	}
	if err := r.Cloud.DeleteLoadBalancer(r.Ctx, l.ID); err != nil {
		return err
	}
	r.updateManifest(*mo, false, change{Section: "load_balancers", ID: l.ID})
	return r.deleted("load-balancer", l.ID, l.Name)
}
