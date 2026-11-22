package main

import (
	"fmt"
	"os"
	"sort"
	"strings"

	"github.com/digitalocean/godo"
)

// Plan: read the stack file, read the account (GET requests only), and say what apply would do. Nothing here
// changes the account or writes any file. Each step apply will take is described by a stackOp; the plan and the
// steps are made together so they cannot disagree.

// Actions an item can have.
const (
	actCreate    = "create"
	actChange    = "change"
	actUnchanged = "unchanged"
	actReplace   = "needs_replacement" // differs in a way docli will not change in place: reported, never done
	actOrphaned  = "orphaned"          // docli created it, the file no longer lists it: reported, not deleted
	actUnmanaged = "unmanaged"         // in the file, exists, but docli did not create it: used as it is, never changed
	actPrune     = "prune"             // only with --prune: a deletion
)

type StackItem struct {
	Kind       string   `json:"kind"`
	Name       string   `json:"name"`
	Action     string   `json:"action"`
	ID         string   `json:"id"`
	Details    []string `json:"details"`
	MonthlyUSD *float64 `json:"monthly_cost_usd"`
}

type StackSummary struct {
	Create           int `json:"create"`
	Change           int `json:"change"`
	Unchanged        int `json:"unchanged"`
	NeedsReplacement int `json:"needs_replacement"`
	Orphaned         int `json:"orphaned"`
	Unmanaged        int `json:"unmanaged"`
	Prune            int `json:"prune"`
}

type NotManagedItem struct {
	Kind string `json:"kind"`
	Name string `json:"name"`
	ID   string `json:"id"`
}

type StackPlanDoc struct {
	StackFile           string           `json:"stack_file"`
	Stack               string           `json:"stack"`
	HasChanges          bool             `json:"has_changes"`
	Summary             StackSummary     `json:"summary"`
	MonthlyCostAddedUSD float64          `json:"monthly_cost_added_usd"`
	CostUnknownFor      []string         `json:"cost_unknown_for"`
	Items               []StackItem      `json:"items"`
	NotManaged          []NotManagedItem `json:"not_managed"`
	Warnings            []string         `json:"warnings"`
}

// stackOp is one step apply will take.
type stackOp struct {
	Kind  string
	Name  string
	Label string // for example: create droplet "web-1"
	Do    func() error

	Prune     bool
	TypedName string // droplets, databases and domains: the name that must be typed before a prune
}

type stackPlan struct {
	Doc *StackPlanDoc
	ops []*stackOp
	x   *applyState
}

type planOpts struct {
	Prune     bool
	PruneData bool
}

type planner struct {
	r     *Run
	s     *Stack
	mo    manifestOpts
	opt   planOpts
	snap  *snapshot
	man   *Manifest
	x     *applyState
	sizes []godo.Size
	regs  []godo.Region
	dbOpt *godo.DatabaseOptions

	items   []*StackItem
	ops     []*stackOp
	errs    []stackErr
	warns   []string
	cost    float64
	unknown []string

	vpcRegion map[string]string // stack or live VPC name -> region
	willMake  map[string]bool   // "kind:name" of things this plan creates
	pruneable []pruneCand
	pruning   map[string]bool // URNs of droplets and clusters this plan prunes
}

type pruneCand struct {
	item    *StackItem
	section string // manifest section
	id      string
	do      func() error
	typed   string
	data    bool // holds data: needs --prune-data
	label   string
	blocked string
}

func (pl *planner) errf(line int, format string, a ...any) {
	pl.errs = append(pl.errs, stackErr{line, fmt.Sprintf(format, a...)})
}

func (pl *planner) item(kind, name, action, id string, details ...string) *StackItem {
	it := &StackItem{Kind: kind, Name: name, Action: action, ID: id, Details: nonNil(details)}
	pl.items = append(pl.items, it)
	return it
}

func (pl *planner) op(kind, name, label string, do func() error) *stackOp {
	o := &stackOp{Kind: kind, Name: name, Label: label, Do: do}
	pl.ops = append(pl.ops, o)
	return o
}

func (pl *planner) addCost(it *StackItem, monthly float64, known bool) {
	if !known {
		pl.unknown = append(pl.unknown, it.Kind+" "+it.Name)
		it.Details = append(it.Details, "monthly cost is not shown: the API's size list has no price for this size")
		return
	}
	v := round2(monthly)
	it.MonthlyUSD = &v
	pl.cost += v
}

// loadManifestForPlan reads the manifest (for DNS ownership and for what --prune may touch). A missing file is
// fine; an unreadable one is an error, because docli will not guess about ownership.
func (r *Run) loadManifestForPlan(mo manifestOpts) (*Manifest, error) {
	if mo.Skip {
		return nil, nil
	}
	path, _, err := r.manifestPath(mo)
	if err != nil {
		return nil, err
	}
	m, _, err := readManifest(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("cannot tell what docli created because the manifest is unreadable (%v). Nothing was changed", err)
	}
	return m, nil
}

// planStack builds the plan. It performs GET requests only.
func (r *Run) planStack(s *Stack, display string, mo manifestOpts, opt planOpts) (*stackPlan, error) {
	pl := &planner{r: r, s: s, mo: mo, opt: opt, vpcRegion: map[string]string{}, willMake: map[string]bool{}, pruning: map[string]bool{}}
	pl.x = newApplyState(r, mo)
	var err error
	if pl.man, err = r.loadManifestForPlan(mo); err != nil {
		return nil, err
	}
	if pl.snap, err = r.loadSnapshot(); err != nil {
		return nil, err
	}
	if pl.man != nil {
		pl.snap.attach(pl.man)
	}
	for i := range pl.snap.databases {
		r.noteSecrets(&pl.snap.databases[i])
	}
	if pl.snap.denied["domains"] && len(s.Domains) > 0 {
		pl.errf(0, "the token cannot read domains, so docli cannot plan the domains: in this file")
	}
	if pl.snap.denied["databases"] && len(s.Databases) > 0 {
		pl.errf(0, "the token cannot read databases, so docli cannot plan the databases: in this file")
	}
	for _, c := range []struct {
		section string
		n       int
	}{{"projects", len(s.Projects)}, {"volumes", len(s.Volumes)}, {"load_balancers", len(s.LBs)}} {
		pl.snap.ensure(c.section)
		if pl.snap.denied[c.section] && c.n > 0 {
			pl.errf(0, "%s, so docli cannot plan the %s: in this file", firstNonEmpty(pl.snap.why[c.section], "the token cannot read "+c.section), c.section)
		}
	}
	needsCatalog := len(s.Droplets)+len(s.Databases)+len(s.VPCs)+len(s.Volumes)+len(s.LBs) > 0
	if needsCatalog {
		if pl.regs, err = r.Cloud.ListRegions(r.Ctx); err != nil {
			return nil, err
		}
		if pl.sizes, err = r.Cloud.ListSizes(r.Ctx); err != nil {
			return nil, err
		}
	}
	if mo.Skip && len(s.Domains) > 0 {
		pl.warns = append(pl.warns, "--no-manifest: docli will not remember which DNS records it creates, so it cannot change or delete them later")
	}
	if len(pl.errs) == 0 {
		pl.planProjects()
		pl.planKeys()
		pl.planVPCs()
		pl.planFirewalls()
		pl.planDroplets()
		pl.planVolumes()
		pl.planLBs()
		if err := pl.planDatabases(); err != nil {
			return nil, err
		}
		if err := pl.planDomains(); err != nil {
			return nil, err
		}
		pl.planOrphans()
	}
	if len(pl.errs) > 0 {
		sort.SliceStable(pl.errs, func(i, j int) bool { return pl.errs[i].Line < pl.errs[j].Line })
		return nil, &stackError{display, pl.errs}
	}
	pl.addPrune()
	return pl.finish(display), nil
}

func (pl *planner) finish(display string) *stackPlan {
	doc := &StackPlanDoc{StackFile: display, Stack: pl.s.Name, Items: []StackItem{}, CostUnknownFor: nonNil(pl.unknown),
		Warnings: nonNil(append(append([]string(nil), pl.s.Notes...), pl.warns...)), NotManaged: pl.notManaged()}
	for _, it := range pl.items {
		doc.Items = append(doc.Items, *it)
		switch it.Action {
		case actCreate:
			doc.Summary.Create++
		case actChange:
			doc.Summary.Change++
		case actUnchanged:
			doc.Summary.Unchanged++
		case actReplace:
			doc.Summary.NeedsReplacement++
		case actOrphaned:
			doc.Summary.Orphaned++
		case actUnmanaged:
			doc.Summary.Unmanaged++
		case actPrune:
			doc.Summary.Prune++
		}
	}
	sum := doc.Summary
	doc.HasChanges = sum.Create+sum.Change+sum.NeedsReplacement+sum.Prune > 0
	doc.MonthlyCostAddedUSD = round2(pl.cost)
	return &stackPlan{Doc: doc, ops: pl.ops, x: pl.x}
}

// ---- looking things up in the account ----

// findNamed looks a resource up by its stack name among resources named docli-NAME (docli's) or NAME exactly.
func findNamed[T any](list []T, nameOf func(*T) string, want string) (idx int, managed bool, err error) {
	final := ensureNamePrefix(want)
	var hits []int
	for i := range list {
		if nameOf(&list[i]) == final {
			hits = append(hits, i)
		}
	}
	if len(hits) > 1 {
		return -1, false, fmt.Errorf("%d resources are named %q; use the account's console to remove the duplicate first", len(hits), final)
	}
	if len(hits) == 1 {
		return hits[0], true, nil
	}
	if final != want {
		for i := range list {
			if nameOf(&list[i]) == want {
				hits = append(hits, i)
			}
		}
		if len(hits) > 1 {
			return -1, false, fmt.Errorf("%d resources are named %q; use the account's console to remove the duplicate first", len(hits), want)
		}
		if len(hits) == 1 {
			return hits[0], false, nil
		}
	}
	return -1, false, nil
}

func (pl *planner) findKeyLive(name string) (int, bool, error) {
	return findNamed(pl.snap.keys, func(k *godo.Key) string { return k.Name }, name)
}

func (pl *planner) findVPCLive(name string) (int, bool, error) {
	return findNamed(pl.snap.vpcs, func(v **godo.VPC) string { return (*v).Name }, name)
}

func (pl *planner) findFirewallLive(name string) (int, bool, error) {
	return findNamed(pl.snap.firewalls, func(f *godo.Firewall) string { return f.Name }, name)
}

// findTagged finds a droplet or cluster by exact name; managed means it carries docli:managed.
func findTagged[T any](list []T, nameOf func(*T) string, tagsOf func(*T) []string, name string) (int, bool, error) {
	var hits []int
	for i := range list {
		if nameOf(&list[i]) == name {
			hits = append(hits, i)
		}
	}
	switch len(hits) {
	case 0:
		return -1, false, nil
	case 1:
		return hits[0], hasTag(tagsOf(&list[hits[0]]), managedTag), nil
	}
	return -1, false, fmt.Errorf("%d resources are named %q, so docli cannot tell which one the stack means; remove the duplicate first", len(hits), name)
}

func (pl *planner) findDropletLive(name string) (int, bool, error) {
	return findTagged(pl.snap.droplets, func(d *godo.Droplet) string { return d.Name }, func(d *godo.Droplet) []string { return d.Tags }, name)
}

func (pl *planner) findDBLive(name string) (int, bool, error) {
	return findTagged(pl.snap.databases, func(d *godo.Database) string { return d.Name }, func(d *godo.Database) []string { return d.Tags }, name)
}

func (pl *planner) regionKnown(slug string, line int, what string) bool {
	for _, g := range pl.regs {
		if g.Slug == slug {
			if !g.Available {
				pl.errf(line, "%s: region %s is not available for new resources right now (see: docli regions)", what, slug)
				return false
			}
			return true
		}
	}
	pl.errf(line, "%s: unknown region %q (see: docli regions)", what, slug)
	return false
}

func (pl *planner) stackTag() string {
	if pl.s.Name == "" {
		return ""
	}
	return "docli:stack:" + pl.s.Name
}

func (pl *planner) dropletTags(d *StackDroplet) []string {
	tags := []string{managedTag}
	if t := pl.stackTag(); t != "" {
		tags = append(tags, t)
	}
	for _, t := range d.Tags {
		if !hasTag(tags, t) {
			tags = append(tags, t)
		}
	}
	return tags
}

func (pl *planner) dbTags() []string {
	tags := []string{managedTag}
	if t := pl.stackTag(); t != "" {
		tags = append(tags, t)
	}
	return tags
}

// ---- SSH keys ----

func (pl *planner) planKeys() {
	for i := range pl.s.SSHKeys {
		k := pl.s.SSHKeys[i]
		idx, managed, err := pl.findKeyLive(k.Name)
		if err != nil {
			pl.errf(k.Pos.at("name"), "SSH key %q: %v", k.Name, err)
			continue
		}
		final := ensureNamePrefix(k.Name)
		switch {
		case idx >= 0 && managed:
			live := pl.snap.keys[idx]
			pl.x.keyID[k.Name] = live.ID
			pl.item("ssh_key", k.Name, actUnchanged, itoa(live.ID), "exists as "+live.Name)
		case idx >= 0:
			live := pl.snap.keys[idx]
			pl.x.keyID[k.Name] = live.ID
			pl.item("ssh_key", k.Name, actUnmanaged, itoa(live.ID), "exists as "+live.Name+" but docli did not create it: it is used as it is and never changed")
		case k.line == "":
			pl.errf(k.Pos.Line, "SSH key %q is not in the account and has no public_key_file to create it from", k.Name)
		default:
			it := pl.item("ssh_key", k.Name, actCreate, "", "name in the account: "+final, "uploads the public key from "+k.PublicKeyFile)
			pl.willMake["ssh_key:"+k.Name] = true
			name, line := k.Name, k.line
			_ = it
			pl.op("ssh_key", k.Name, fmt.Sprintf("create SSH key %q", final), func() error { return pl.x.createKey(name, final, line) })
		}
	}
}

// ---- VPCs ----

func (pl *planner) planVPCs() {
	for i := range pl.s.VPCs {
		v := pl.s.VPCs[i]
		pl.vpcRegion[v.Name] = v.Region
		idx, managed, err := pl.findVPCLive(v.Name)
		if err != nil {
			pl.errf(v.Pos.at("name"), "VPC %q: %v", v.Name, err)
			continue
		}
		final := ensureNamePrefix(v.Name)
		if idx < 0 {
			if !pl.regionKnown(v.Region, v.Pos.Line, fmt.Sprintf("VPC %q", v.Name)) {
				continue
			}
			rng := orDash(v.IPRange)
			if v.IPRange == "" {
				rng = "chosen by DigitalOcean"
			}
			pl.item("vpc", v.Name, actCreate, "", "name in the account: "+final, "region: "+v.Region, "IP range: "+rng)
			pl.willMake["vpc:"+v.Name] = true
			name, region, ipr := v.Name, v.Region, v.IPRange
			pl.op("vpc", v.Name, fmt.Sprintf("create VPC %q", final), func() error { return pl.x.createVPC(name, final, region, ipr) })
			continue
		}
		live := pl.snap.vpcs[idx]
		pl.x.vpcID[v.Name] = live.ID
		pl.vpcRegion[v.Name] = live.RegionSlug
		if !managed {
			pl.item("vpc", v.Name, actUnmanaged, live.ID, "exists as "+live.Name+" but docli did not create it: it is used as it is and never changed")
			continue
		}
		var diffs []string
		if live.RegionSlug != v.Region {
			diffs = append(diffs, fmt.Sprintf("region: file says %s, the account has %s", v.Region, live.RegionSlug))
		}
		if v.IPRange != "" && live.IPRange != v.IPRange {
			diffs = append(diffs, fmt.Sprintf("ip_range: file says %s, the account has %s", v.IPRange, live.IPRange))
		}
		if len(diffs) > 0 {
			pl.item("vpc", v.Name, actReplace, live.ID, append(diffs, "a VPC cannot be changed in place; docli will not replace it automatically (not done)")...)
			continue
		}
		pl.item("vpc", v.Name, actUnchanged, live.ID, "exists as "+live.Name)
	}
}

// ---- firewalls ----

func (pl *planner) planFirewalls() {
	for i := range pl.s.Firewalls {
		f := pl.s.Firewalls[i]
		idx, managed, err := pl.findFirewallLive(f.Name)
		if err != nil {
			pl.errf(f.Pos.at("name"), "firewall %q: %v", f.Name, err)
			continue
		}
		final := ensureNamePrefix(f.Name)
		if idx >= 0 && !managed {
			pl.errf(f.Pos.at("name"), "a firewall named %q exists but docli did not create it, and a firewall cannot be tagged or adopted. Docli will not change it. Name this firewall differently in the stack file (it is stored as %s)", f.Name, final)
			continue
		}
		if idx < 0 {
			it := pl.item("firewall", f.Name, actCreate, "", append([]string{"name in the account: " + final}, ruleDetails(f.parsed)...)...)
			pl.willMake["firewall:"+f.Name] = true
			for _, w := range ruleWarnings(f.parsed)[1:] {
				pl.warns = append(pl.warns, fmt.Sprintf("firewall %q: %s", f.Name, w))
			}
			_ = it
			name, rules := f.Name, f.parsed
			pl.op("firewall", f.Name, fmt.Sprintf("create firewall %q", final), func() error { return pl.x.createFirewall(name, final, rules) })
			continue
		}
		live := pl.snap.firewalls[idx]
		pl.x.fwID[f.Name] = live.ID
		have := map[string]Rule{}
		for _, r := range firewallRules(live) {
			have[r.Spec()] = r
		}
		want := map[string]Rule{}
		for _, r := range f.parsed {
			want[r.Spec()] = r
		}
		var add, remove []Rule
		var det []string
		for _, r := range f.parsed {
			if _, ok := have[r.Spec()]; !ok {
				add = append(add, r)
				det = append(det, fmt.Sprintf("add rule %s  =  %s", r.Spec(), r.Describe()))
			}
		}
		for _, r := range firewallRules(live) {
			if _, ok := want[r.Spec()]; !ok {
				remove = append(remove, r)
				det = append(det, fmt.Sprintf("remove rule %s  =  %s", r.Spec(), r.Describe()))
			}
		}
		if len(det) == 0 {
			pl.item("firewall", f.Name, actUnchanged, live.ID, fmt.Sprintf("exists as %s with the %d rule(s) in the file", live.Name, len(f.parsed)))
			continue
		}
		pl.item("firewall", f.Name, actChange, live.ID, det...)
		name, id := f.Name, live.ID
		pl.op("firewall", f.Name, fmt.Sprintf("change rules of firewall %q", live.Name), func() error { return pl.x.changeFirewallRules(name, id, add, remove) })
	}
}

func ruleDetails(rules []Rule) []string {
	var d []string
	for _, r := range rules {
		d = append(d, fmt.Sprintf("rule %s  =  %s", r.Spec(), r.Describe()))
	}
	if len(rules) == 0 {
		d = append(d, "no rules (everything is blocked)")
	}
	return d
}

// ---- droplets ----

// lookupVPC resolves a vpc name used by a droplet or database: declared in the file, or already in the account.
func (pl *planner) lookupVPC(ref string, line int, what string) bool {
	if _, ok := pl.x.vpcID[ref]; ok || pl.willMake["vpc:"+ref] {
		return true
	}
	if nameIn(pl.s.VPCs, func(x StackVPC) string { return x.Name }, ref) {
		return false // declared but it failed earlier; that error is already listed
	}
	v, err := findVPC(pl.snap.vpcs, ref)
	if err != nil {
		pl.errf(line, "%s: %v", what, err)
		return false
	}
	pl.x.vpcID[ref] = v.ID
	pl.vpcRegion[ref] = v.RegionSlug
	return true
}

func (pl *planner) lookupKey(ref string, line int, what string) bool {
	if _, ok := pl.x.keyID[ref]; ok || pl.willMake["ssh_key:"+ref] {
		return true
	}
	if nameIn(pl.s.SSHKeys, func(x StackKey) string { return x.Name }, ref) {
		return false
	}
	k, err := findKey(pl.snap.keys, ref)
	if err != nil {
		pl.errf(line, "%s: %v", what, err)
		return false
	}
	pl.x.keyID[ref] = k.ID
	return true
}

func (pl *planner) sizeFor(slug, region string, line int, what string) *godo.Size {
	for i := range pl.sizes {
		if pl.sizes[i].Slug == slug {
			sz := &pl.sizes[i]
			if !sz.Available || !contains(sz.Regions, region) {
				pl.errf(line, "%s: size %s is not available in %s (see: docli sizes --region %s)", what, slug, region, region)
				return nil
			}
			return sz
		}
	}
	pl.errf(line, "%s: unknown size %q (see: docli sizes)", what, slug)
	return nil
}

func (pl *planner) planDroplets() {
	for i := range pl.s.Droplets {
		d := pl.s.Droplets[i]
		what := fmt.Sprintf("droplet %q", d.Name)
		idx, managed, err := pl.findDropletLive(d.Name)
		if err != nil {
			pl.errf(d.Pos.at("name"), "%v", err)
			continue
		}
		vpcOK := true
		if d.VPC != "" {
			vpcOK = pl.lookupVPC(d.VPC, d.Pos.at("vpc"), what)
		}
		keysOK := true
		for _, k := range d.SSHKeys {
			keysOK = pl.lookupKey(k, d.Pos.at("ssh_keys"), what) && keysOK
		}
		if idx < 0 {
			pl.planDropletCreate(d, what, vpcOK && keysOK)
			continue
		}
		live := pl.snap.droplets[idx]
		pl.x.dropID[d.Name] = live.ID
		v := dropletView(live)
		if !managed {
			pl.item("droplet", d.Name, actUnmanaged, itoa(live.ID), "exists but docli did not create it: it is used as it is and never changed",
				"to manage it, run: docli adopt droplet "+d.Name)
			if len(d.Firewalls) > 0 {
				pl.warns = append(pl.warns, fmt.Sprintf("droplet %q is not docli's, so it is not attached to its firewall(s) %s. Adopt it first", d.Name, strings.Join(d.Firewalls, ", ")))
			}
			continue
		}
		var diffs []string
		if v.Region != d.Region {
			diffs = append(diffs, fmt.Sprintf("region: file says %s, the account has %s", d.Region, v.Region))
		}
		if v.SizeSlug != d.Size {
			diffs = append(diffs, fmt.Sprintf("size: file says %s, the account has %s (docli does not resize droplets)", d.Size, v.SizeSlug))
		}
		if img := v.Image.Slug; img != "" && !isNumeric(d.Image) && img != d.Image {
			diffs = append(diffs, fmt.Sprintf("image: file says %s, the account has %s", d.Image, img))
		}
		if vpcOK && d.VPC != "" {
			if want := pl.x.vpcID[d.VPC]; want != "" && live.VPCUUID != want {
				diffs = append(diffs, fmt.Sprintf("vpc: file says %s, the droplet is in another VPC", d.VPC))
			}
		}
		var attach []string
		for _, fw := range d.Firewalls {
			if pl.willMake["firewall:"+fw] || !pl.firewallHasDroplet(fw, live) {
				attach = append(attach, fw)
			}
		}
		switch {
		case len(diffs) > 0:
			det := append(diffs, "docli does not replace a droplet automatically: nothing was done for these differences")
			pl.item("droplet", d.Name, actReplace, itoa(live.ID), det...)
		case len(attach) == 0:
			pl.item("droplet", d.Name, actUnchanged, itoa(live.ID), fmt.Sprintf("exists, %s, %s", v.Region, v.SizeSlug))
		}
		if len(attach) > 0 {
			var det []string
			for _, fw := range attach {
				det = append(det, "attach to firewall "+fw)
			}
			if len(diffs) == 0 {
				pl.item("droplet", d.Name, actChange, itoa(live.ID), det...)
			} else {
				pl.items[len(pl.items)-1].Details = append(pl.items[len(pl.items)-1].Details, det...)
			}
			for _, fw := range attach {
				pl.attachOp(d.Name, fw)
			}
		}
	}
}

func isNumeric(s string) bool {
	for _, c := range s {
		if c < '0' || c > '9' {
			return false
		}
	}
	return s != ""
}

// firewallHasDroplet is true when the live firewall already covers the droplet by id or by one of its tags.
func (pl *planner) firewallHasDroplet(fwName string, d godo.Droplet) bool {
	idx, _, _ := pl.findFirewallLive(fwName)
	if idx < 0 {
		return false
	}
	f := pl.snap.firewalls[idx]
	for _, id := range f.DropletIDs {
		if id == d.ID {
			return true
		}
	}
	for _, t := range f.Tags {
		if hasTag(d.Tags, t) {
			return true
		}
	}
	return false
}

func (pl *planner) attachOp(droplet, fw string) {
	pl.op("droplet", droplet, fmt.Sprintf("attach droplet %q to firewall %q", droplet, ensureNamePrefix(fw)), func() error { return pl.x.attachDroplet(droplet, fw) })
}

func (pl *planner) planDropletCreate(d StackDroplet, what string, refsOK bool) {
	ok := pl.regionKnown(d.Region, d.Pos.Line, what)
	var sz *godo.Size
	if ok {
		sz = pl.sizeFor(d.Size, d.Region, d.Pos.at("size"), what)
	}
	if !isNumeric(d.Image) {
		if _, err := pl.r.Cloud.GetImageBySlug(pl.r.Ctx, d.Image); err != nil {
			if isNotFound(err) {
				pl.errf(d.Pos.at("image"), "%s: unknown image %q (see: docli images)", what, d.Image)
			} else {
				pl.errf(d.Pos.at("image"), "%s: cannot check image %q: %v", what, d.Image, err)
			}
			ok = false
		}
	}
	if d.VPC != "" && refsOK {
		if rg := pl.vpcRegion[d.VPC]; rg != "" && rg != d.Region {
			pl.errf(d.Pos.at("vpc"), "%s is for %s but VPC %q is in %s", what, d.Region, d.VPC, rg)
			refsOK = false
		}
	}
	if !ok || sz == nil || !refsOK {
		return
	}
	tags := pl.dropletTags(&d)
	vpcDesc := "the default VPC for " + d.Region
	if d.VPC != "" {
		vpcDesc = "VPC " + d.VPC
	}
	det := []string{
		fmt.Sprintf("size: %s (%d vCPU, %d MB memory, %d GB disk)", sz.Slug, sz.Vcpus, sz.Memory, sz.Disk),
		"region: " + d.Region, "image: " + d.Image, "network: " + vpcDesc,
		"ssh keys: " + orDash(strings.Join(d.SSHKeys, ", ")),
		"tags: " + strings.Join(tags, ", "),
		fmt.Sprintf("extras: backups=%s monitoring=%s ipv6=%s", yesNo(d.Backups), yesNo(d.Monitoring), yesNo(d.IPv6)),
	}
	if d.userData != "" {
		det = append(det, fmt.Sprintf("user data: %d bytes from %s (contents not shown)", len(d.userData), d.UserDataFile))
	}
	for _, fw := range d.Firewalls {
		det = append(det, "attach to firewall "+fw+" once it exists")
	}
	if len(d.SSHKeys) == 0 {
		pl.warns = append(pl.warns, fmt.Sprintf("droplet %q has no ssh_keys: DigitalOcean will email a root password instead", d.Name))
	}
	cost := estimateCost(*sz, d.Backups)
	it := pl.item("droplet", d.Name, actCreate, "", det...)
	pl.addCost(it, cost.Total(), true)
	pl.willMake["droplet:"+d.Name] = true
	spec := d
	pl.op("droplet", d.Name, fmt.Sprintf("create droplet %q", d.Name), func() error { return pl.x.createDroplet(spec, tags) })
	pl.op("droplet", d.Name, fmt.Sprintf("wait for droplet %q to be active", d.Name), func() error { return pl.x.waitDroplet(spec.Name) })
	for _, fw := range d.Firewalls {
		pl.attachOp(d.Name, fw)
	}
}

// ---- databases ----

// desiredSources lists the stack's trusted sources as "type:value", droplets by name.
func desiredSources(d *StackDatabase) []string {
	var out []string
	for _, n := range d.TrustedDroplets {
		out = append(out, "droplet:"+n)
	}
	for _, t := range d.TrustedTags {
		out = append(out, "tag:"+t)
	}
	for _, ip := range d.TrustedIPs {
		n, _ := normalizeAddr(ip)
		out = append(out, "ip_addr:"+n)
	}
	sort.Strings(out)
	return out
}

func (pl *planner) liveSources(rules []godo.DatabaseFirewallRule) []string {
	names := map[string]string{}
	for _, d := range pl.snap.droplets {
		names[itoa(d.ID)] = d.Name
	}
	var out []string
	for _, r := range rules {
		v := r.Value
		if r.Type == "droplet" && names[v] != "" {
			v = names[v]
		}
		out = append(out, r.Type+":"+v)
	}
	sort.Strings(out)
	return out
}

func diffSets(want, have []string) (add, remove []string) {
	for _, w := range want {
		if !contains(have, w) {
			add = append(add, w)
		}
	}
	for _, h := range have {
		if !contains(want, h) {
			remove = append(remove, h)
		}
	}
	return
}

func (pl *planner) planDatabases() error {
	for i := range pl.s.Databases {
		d := pl.s.Databases[i]
		pl.x.dbSpec[d.Name] = d
		what := fmt.Sprintf("database %q", d.Name)
		idx, managed, err := pl.findDBLive(d.Name)
		if err != nil {
			pl.errf(d.Pos.at("name"), "%v", err)
			continue
		}
		vpcOK := pl.lookupVPC(d.VPC, d.Pos.at("vpc"), what)
		if idx < 0 {
			if err := pl.planDBCreate(d, what, vpcOK); err != nil {
				return err
			}
			continue
		}
		live := pl.snap.databases[idx]
		pl.x.dbID[d.Name] = live.ID
		if !managed {
			pl.item("database", d.Name, actUnmanaged, live.ID, "exists but docli did not create it: it is used as it is and never changed (users, databases and trusted sources too)",
				"to manage it, run: docli adopt database "+d.Name)
			continue
		}
		if err := pl.planDBExisting(d, live, vpcOK); err != nil {
			return err
		}
	}
	return nil
}

func (pl *planner) dbOptions() (*godo.DatabaseOptions, error) {
	if pl.dbOpt == nil {
		o, err := pl.r.Cloud.DatabaseOptions(pl.r.Ctx)
		if err != nil {
			return nil, err
		}
		pl.dbOpt = o
	}
	return pl.dbOpt, nil
}

func (pl *planner) dbPrice(size string, nodes int) (float64, bool) {
	p, ok := sizePrice(pl.sizes, size)
	return p * float64(nodes), ok
}

func (pl *planner) planDBCreate(d StackDatabase, what string, vpcOK bool) error {
	ok := pl.regionKnown(d.Region, d.Pos.Line, what)
	opts, err := pl.dbOptions()
	if err != nil {
		return err
	}
	if err := checkDBOptions(opts, d.Version, d.Region, d.Size, d.Nodes); err != nil {
		pl.errf(d.Pos.Line, "%s: %v", what, err)
		ok = false
	}
	if vpcOK {
		if rg := pl.vpcRegion[d.VPC]; rg != "" && rg != d.Region {
			pl.errf(d.Pos.at("vpc"), "%s is for %s but VPC %q is in %s", what, d.Region, d.VPC, rg)
			vpcOK = false
		}
	}
	if !ok || !vpcOK {
		return nil
	}
	tags := pl.dbTags()
	det := []string{
		fmt.Sprintf("PostgreSQL %s, %s x %d node(s), region %s", d.Version, d.Size, d.Nodes, d.Region),
		"network: VPC " + d.VPC,
		"trusted sources: " + strings.Join(desiredSources(&d), ", "),
		"tags: " + strings.Join(tags, ", "),
	}
	it := pl.item("database", d.Name, actCreate, "", det...)
	total, known := pl.dbPrice(d.Size, d.Nodes)
	pl.addCost(it, total, known)
	pl.willMake["database:"+d.Name] = true
	spec := d
	pl.op("database", d.Name, fmt.Sprintf("create database cluster %q", d.Name), func() error { return pl.x.createDatabase(spec, tags) })
	if len(d.Users)+len(d.Databases) > 0 {
		pl.op("database", d.Name, fmt.Sprintf("wait for database cluster %q to be online", d.Name), func() error { return pl.x.waitDB(spec.Name) })
	}
	for _, u := range d.Users {
		pl.item("db_user", d.Name+"/"+u, actCreate, "", "its password is generated by DigitalOcean and never shown; fetch it with: docli db connection "+d.Name+" --user "+u+" --password-file PATH")
		name, user := d.Name, u
		pl.op("db_user", d.Name+"/"+u, fmt.Sprintf("create user %q on %q", u, d.Name), func() error { return pl.x.createDBUser(name, user) })
		pl.x.nextSteps = append(pl.x.nextSteps, fmt.Sprintf("docli db connection %s --user %s --password-file PATH   (saves the password for user %s to a new 0600 file)", d.Name, u, u))
	}
	for _, n := range d.Databases {
		pl.item("db_database", d.Name+"/"+n, actCreate, "")
		name, dbn := d.Name, n
		pl.op("db_database", d.Name+"/"+n, fmt.Sprintf("create database %q on %q", n, d.Name), func() error { return pl.x.createDBDatabase(name, dbn) })
	}
	return nil
}

func (pl *planner) planDBExisting(d StackDatabase, live godo.Database, vpcOK bool) error {
	var diffs []string
	if live.EngineSlug != "" && live.EngineSlug != dbEngine {
		diffs = append(diffs, fmt.Sprintf("engine: the cluster runs %s, the stack manages PostgreSQL only", live.EngineSlug))
	}
	if live.VersionSlug != "" && live.VersionSlug != d.Version {
		diffs = append(diffs, fmt.Sprintf("version: file says %s, the account has %s (docli does not upgrade major versions)", d.Version, live.VersionSlug))
	}
	if live.RegionSlug != d.Region {
		diffs = append(diffs, fmt.Sprintf("region: file says %s, the account has %s", d.Region, live.RegionSlug))
	}
	if vpcOK {
		if want := pl.x.vpcID[d.VPC]; want != "" && live.PrivateNetworkUUID != want {
			diffs = append(diffs, fmt.Sprintf("vpc: file says %s, the cluster is in another VPC", d.VPC))
		}
	}
	var chg []string
	if live.SizeSlug != d.Size || live.NumNodes != d.Nodes {
		opts, err := pl.dbOptions()
		if err != nil {
			return err
		}
		if err := checkDBOptions(opts, "", "", d.Size, d.Nodes); err != nil {
			pl.errf(d.Pos.Line, "database %q: %v", d.Name, err)
		} else {
			chg = append(chg, fmt.Sprintf("resize: %s x %d node(s) now, %s x %d after", live.SizeSlug, live.NumNodes, d.Size, d.Nodes))
			if live.Status != "online" {
				pl.warns = append(pl.warns, fmt.Sprintf("database %q is %q, not online, so the resize will be refused until it is. Wait with: docli db wait %s", d.Name, live.Status, d.Name))
			}
			name, id, size, nodes := d.Name, live.ID, d.Size, d.Nodes
			pl.op("database", d.Name, fmt.Sprintf("resize database cluster %q", d.Name), func() error { return pl.x.resizeDB(name, id, size, nodes) })
		}
	}
	rules, err := pl.r.Cloud.GetDatabaseFirewall(pl.r.Ctx, live.ID)
	if err != nil {
		return fmt.Errorf("cannot read the trusted sources of %q: %v", d.Name, err)
	}
	add, remove := diffSets(desiredSources(&d), pl.liveSources(rules))
	if len(add)+len(remove) > 0 {
		for _, a := range add {
			chg = append(chg, "trust "+a)
		}
		for _, x := range remove {
			chg = append(chg, "stop trusting "+x)
		}
		name := d.Name
		pl.op("database", d.Name, fmt.Sprintf("set the trusted sources of %q", d.Name), func() error { return pl.x.setTrusted(name) })
	}
	switch {
	case len(diffs) > 0:
		pl.item("database", d.Name, actReplace, live.ID, append(append(diffs, chg...), "docli does not replace a database automatically: nothing was done for the differences above")...)
	case len(chg) > 0:
		pl.item("database", d.Name, actChange, live.ID, chg...)
	default:
		pl.item("database", d.Name, actUnchanged, live.ID, fmt.Sprintf("exists, PostgreSQL %s, %s x %d node(s), %s", live.VersionSlug, live.SizeSlug, live.NumNodes, live.Status))
	}

	users, err := pl.r.Cloud.ListDatabaseUsers(pl.r.Ctx, live.ID)
	if err != nil {
		return fmt.Errorf("cannot list the users of %q: %v", d.Name, err)
	}
	haveU := map[string]bool{}
	for _, u := range users {
		pl.r.rememberSecret(u.Password)
		haveU[u.Name] = true
	}
	for _, u := range d.Users {
		if haveU[u] {
			pl.item("db_user", d.Name+"/"+u, actUnchanged, "")
			continue
		}
		pl.item("db_user", d.Name+"/"+u, actCreate, "", "its password is generated by DigitalOcean and never shown; fetch it with: docli db connection "+d.Name+" --user "+u+" --password-file PATH")
		name, user := d.Name, u
		pl.op("db_user", d.Name+"/"+u, fmt.Sprintf("create user %q on %q", u, d.Name), func() error { return pl.x.createDBUser(name, user) })
		pl.x.nextSteps = append(pl.x.nextSteps, fmt.Sprintf("docli db connection %s --user %s --password-file PATH   (saves the password for user %s to a new 0600 file)", d.Name, u, u))
	}
	for _, u := range users {
		if u.Name != "doadmin" && !contains(d.Users, u.Name) {
			pl.item("db_user", d.Name+"/"+u.Name, actOrphaned, "", "in the cluster but not in the file: not deleted (docli never prunes users)")
		}
	}
	dbs, err := pl.r.Cloud.ListDatabaseDBs(pl.r.Ctx, live.ID)
	if err != nil {
		return fmt.Errorf("cannot list the databases of %q: %v", d.Name, err)
	}
	haveD := map[string]bool{}
	for _, x := range dbs {
		haveD[x.Name] = true
	}
	for _, n := range d.Databases {
		if haveD[n] {
			pl.item("db_database", d.Name+"/"+n, actUnchanged, "")
			continue
		}
		pl.item("db_database", d.Name+"/"+n, actCreate, "")
		name, dbn := d.Name, n
		pl.op("db_database", d.Name+"/"+n, fmt.Sprintf("create database %q on %q", n, d.Name), func() error { return pl.x.createDBDatabase(name, dbn) })
	}
	for _, x := range dbs {
		if x.Name != "defaultdb" && !contains(d.Databases, x.Name) {
			pl.item("db_database", d.Name+"/"+x.Name, actOrphaned, "", "in the cluster but not in the file: not deleted (docli never prunes databases inside a cluster)")
		}
	}
	return nil
}
