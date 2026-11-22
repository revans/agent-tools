package main

import (
	"fmt"
	"os"
	"strconv"
	"strings"

	"github.com/digitalocean/godo"
)

// Domains and DNS records.
//
// OWNERSHIP. DigitalOcean cannot tag a domain or a record. docli therefore remembers what it created in the
// manifest (config/deploys/manifest.yml, section `domains:`), and for these two kinds only, the manifest IS the
// authority: update and delete refuse anything the file does not list, unless --force-unmanaged is given and the
// name is typed. The limits are in the README: a missing, edited or other-machine manifest means "not mine"
// (the safe direction), and anyone who can edit the file can claim a record.

// loadOwnership reads which domains and records the manifest says docli created. A missing file or --no-manifest
// means nothing is owned. A file that cannot be read is an error: docli will not guess about ownership.
func (r *Run) loadOwnership(mo manifestOpts) (map[string]*ownedDomain, error) {
	own := map[string]*ownedDomain{}
	if mo.Skip {
		return own, nil
	}
	path, _, err := r.manifestPath(mo)
	if err != nil {
		return nil, err
	}
	m, _, err := readManifest(path)
	if err != nil {
		if os.IsNotExist(err) {
			return own, nil
		}
		return nil, fmt.Errorf("cannot tell what docli created because the manifest is unreadable (%v). Nothing was changed", err)
	}
	s := &snapshot{own: own}
	s.attach(m)
	return own, nil
}

func (o *ownedDomain) hasRecord(id int) bool {
	if o == nil {
		return false
	}
	for _, x := range o.IDs {
		if x == id {
			return true
		}
	}
	return false
}

const domainOwnRule = "a record in the manifest saying docli created it"

func (a *App) cmdDomain(args []string) error {
	if len(args) == 0 {
		return usagef("usage: docli domain list|get|create|delete (try: docli help domain)")
	}
	switch args[0] {
	case "list":
		return a.domainList(args[1:])
	case "get":
		return a.domainGet(args[1:])
	case "create":
		return a.domainCreate(args[1:])
	case "delete":
		return a.domainDelete(args[1:])
	}
	return usagef("unknown domain command %q (list, get, create, delete)", args[0])
}

func (a *App) cmdDNS(args []string) error {
	if len(args) == 0 {
		return usagef("usage: docli dns list|create|update|delete (try: docli help dns)")
	}
	switch args[0] {
	case "list":
		return a.dnsList(args[1:])
	case "create":
		return a.dnsCreate(args[1:])
	case "update":
		return a.dnsUpdate(args[1:])
	case "delete":
		return a.dnsDelete(args[1:])
	}
	return usagef("unknown dns command %q (list, create, update, delete)", args[0])
}

func findDomain(ds []godo.Domain, name string) (*godo.Domain, error) {
	n := strings.ToLower(strings.TrimSuffix(name, "."))
	for i := range ds {
		if ds[i].Name == n {
			return &ds[i], nil
		}
	}
	return nil, fmt.Errorf("no domain named %q in this account. List them with: docli domain list", name)
}

func (a *App) domainList(args []string) error {
	fs, g := a.newFlags("domain list")
	mo := addManifestFlags(fs)
	if _, err := a.parseFlags(fs, args, "domain list"); err != nil {
		return err
	}
	r, err := a.start(*g)
	if err != nil {
		return err
	}
	own, err := r.loadOwnership(*mo)
	if err != nil {
		return err
	}
	ds, err := r.Cloud.ListDomains(r.Ctx)
	if err != nil {
		return err
	}
	if g.JSON {
		out := []domainJSON{}
		for _, d := range ds {
			out = append(out, domainJSON{d.Name, d.TTL, own[d.Name] != nil && own[d.Name].Created})
		}
		return r.printJSON(out)
	}
	var rows [][]string
	for _, d := range ds {
		rows = append(rows, []string{d.Name, itoa(d.TTL), yesNo(own[d.Name] != nil && own[d.Name].Created), itoa(len(own[d.Name].ids()))})
	}
	printTable(a.Stdout, []string{"DOMAIN", "TTL", "MANAGED", "DOCLI RECORDS"}, rows)
	return nil
}

func (o *ownedDomain) ids() []int {
	if o == nil {
		return nil
	}
	return o.IDs
}

func (a *App) domainGet(args []string) error {
	fs, g := a.newFlags("domain get")
	mo := addManifestFlags(fs)
	pos, err := a.parseFlags(fs, args, "domain get")
	if err != nil {
		return err
	}
	if len(pos) != 1 {
		return usagef("usage: docli domain get DOMAIN")
	}
	r, err := a.start(*g)
	if err != nil {
		return err
	}
	own, err := r.loadOwnership(*mo)
	if err != nil {
		return err
	}
	ds, err := r.Cloud.ListDomains(r.Ctx)
	if err != nil {
		return err
	}
	d, err := findDomain(ds, pos[0])
	if err != nil {
		return err
	}
	recs, err := r.Cloud.ListDomainRecords(r.Ctx, d.Name)
	if err != nil {
		return err
	}
	o := own[d.Name]
	det := domainDetailJSON{domainJSON{d.Name, d.TTL, o != nil && o.Created}, []recordJSON{}}
	for _, rec := range recs {
		det.Records = append(det.Records, recordView(d.Name, rec, o.hasRecord(rec.ID)))
	}
	if g.JSON {
		return r.printJSON(det)
	}
	fmt.Fprintf(a.Stdout, "Domain:   %s\nTTL:      %d\nManaged:  %s\nRecords:  %d\n", d.Name, d.TTL, yesNo(det.Managed), len(recs))
	printRecords(a, det.Records)
	return nil
}

func printRecords(a *App, recs []recordJSON) {
	var rows [][]string
	for _, x := range recs {
		extra := ""
		if x.Type == "MX" {
			extra = "priority " + itoa(x.Priority)
		}
		if x.Type == "CAA" {
			extra = fmt.Sprintf("flags %d tag %s", x.Flags, x.Tag)
		}
		rows = append(rows, []string{itoa(x.ID), x.Type, x.Name, x.Data, itoa(x.TTL), orDash(extra), yesNo(x.Managed)})
	}
	printTable(a.Stdout, []string{"ID", "TYPE", "NAME", "DATA", "TTL", "EXTRA", "MANAGED"}, rows)
}

func (a *App) domainCreate(args []string) error {
	fs, g := a.newFlags("domain create")
	name := fs.String("name", "", "")
	projRef := fs.String("project", "", "")
	mo := addManifestFlags(fs)
	pos, err := a.parseFlags(fs, args, "domain create")
	if err != nil {
		return err
	}
	if len(pos) > 0 || *name == "" {
		return usagef("usage: docli domain create --name example.com [--project P]")
	}
	if err := validateDomainName(*name); err != nil {
		return usagef("%v", err)
	}
	r, err := a.start(*g)
	if err != nil {
		return err
	}
	proj, err := r.lookupProject(*projRef)
	if err != nil {
		return err
	}
	ds, err := r.Cloud.ListDomains(r.Ctx)
	if err != nil {
		return err
	}
	for _, d := range ds {
		if d.Name == *name {
			return fmt.Errorf("the domain %q is already in this account. Nothing was changed", *name)
		}
	}
	p := Plan{Action: "domain.create", Summary: fmt.Sprintf("add the domain %q to DigitalOcean DNS", *name),
		Steps: []string{
			"this creates the DNS zone only; it does not register the domain and has no charge of its own",
			"docli will remember it created this domain in the manifest, so it may delete it later",
			"for DigitalOcean to answer for the domain, its nameservers at your registrar must be ns1/ns2/ns3.digitalocean.com",
		}}
	if s := projectStep(proj); s != "" {
		p.Steps = append(p.Steps, s)
	}
	if mo.Skip {
		p.Warnings = append(p.Warnings, "--no-manifest: docli will not remember it created this domain, so deleting it later needs --force-unmanaged")
	}
	ok, err := r.Approve(p, approveOpts{})
	if !ok || err != nil {
		return err
	}
	d, err := r.Cloud.CreateDomain(r.Ctx, *name)
	if err != nil {
		if statusOf(err) == 0 {
			return fmt.Errorf("the create request failed and it is not known whether the domain was added (%v). docli does not retry a create. Check with: docli domain list", err)
		}
		return err
	}
	r.updateManifest(*mo, false, change{Section: "domains", ID: d.Name, Adopt: true})
	if err := r.putInProject(proj, d.URN(), "domain", d.Name); err != nil {
		return err
	}
	if g.JSON {
		return r.printJSON(domainJSON{d.Name, d.TTL, !mo.Skip})
	}
	fmt.Fprintf(a.Stdout, "Added domain %q. Point its nameservers at ns1.digitalocean.com, ns2.digitalocean.com and ns3.digitalocean.com.\n", d.Name)
	return nil
}

func (a *App) domainDelete(args []string) error {
	fs, g := a.newFlags("domain delete")
	force := fs.Bool("force-unmanaged", false, "")
	confirm := fs.String("confirm-name", "", "")
	mo := addManifestFlags(fs)
	pos, err := a.parseFlags(fs, args, "domain delete")
	if err != nil {
		return err
	}
	if len(pos) != 1 {
		return usagef("usage: docli domain delete DOMAIN [--yes --confirm-name DOMAIN]")
	}
	r, err := a.start(*g)
	if err != nil {
		return err
	}
	own, err := r.loadOwnership(*mo)
	if err != nil {
		return err
	}
	ds, err := r.Cloud.ListDomains(r.Ctx)
	if err != nil {
		return err
	}
	d, err := findDomain(ds, pos[0])
	if err != nil {
		return err
	}
	managed := own[d.Name] != nil && own[d.Name].Created
	recs, err := r.Cloud.ListDomainRecords(r.Ctx, d.Name)
	if err != nil {
		return err
	}
	mine := 0
	for _, rec := range recs {
		if own[d.Name].hasRecord(rec.ID) {
			mine++
		}
	}
	spec := deleteSpec{Kind: "domain", Name: d.Name, Managed: managed, Rule: domainOwnRule, Always: true,
		Steps: []string{fmt.Sprintf("remove the domain %q and ALL %d of its DNS records (%d created by docli, %d not); the sites and mail that use them stop resolving", d.Name, len(recs), mine, len(recs)-mine)}}
	ok, err := r.ApproveDelete(spec, *force, *confirm)
	if !ok || err != nil {
		return err
	}
	if err := r.Cloud.DeleteDomain(r.Ctx, d.Name); err != nil {
		return err
	}
	r.updateManifest(*mo, false, change{Section: "domains", ID: d.Name})
	return r.deleted("domain", d.Name, d.Name)
}

// ---- records ----

type recordFlags struct {
	typ, name, data, tag *string
	ttl, priority, flags *int
}

func addRecordFlags(fs interface {
	StringVar(*string, string, string, string)
	IntVar(*int, string, int, string)
}, ttlDefault int) *recordFlags {
	f := &recordFlags{new(string), new(string), new(string), new(string), new(int), new(int), new(int)}
	fs.StringVar(f.typ, "type", "", "")
	fs.StringVar(f.name, "name", "", "")
	fs.StringVar(f.data, "data", "", "")
	fs.StringVar(f.tag, "tag", "", "")
	fs.IntVar(f.ttl, "ttl", ttlDefault, "")
	fs.IntVar(f.priority, "priority", -1, "")
	fs.IntVar(f.flags, "flags", 0, "")
	return f
}

func (a *App) dnsList(args []string) error {
	fs, g := a.newFlags("dns list")
	typ := fs.String("type", "", "")
	name := fs.String("name", "", "")
	mo := addManifestFlags(fs)
	pos, err := a.parseFlags(fs, args, "dns list")
	if err != nil {
		return err
	}
	if len(pos) != 1 {
		return usagef("usage: docli dns list DOMAIN [--type A] [--name www]")
	}
	r, err := a.start(*g)
	if err != nil {
		return err
	}
	own, err := r.loadOwnership(*mo)
	if err != nil {
		return err
	}
	ds, err := r.Cloud.ListDomains(r.Ctx)
	if err != nil {
		return err
	}
	d, err := findDomain(ds, pos[0])
	if err != nil {
		return err
	}
	recs, err := r.Cloud.ListDomainRecords(r.Ctx, d.Name)
	if err != nil {
		return err
	}
	out := []recordJSON{}
	for _, rec := range recs {
		if (*typ != "" && !strings.EqualFold(rec.Type, *typ)) || (*name != "" && rec.Name != strings.ToLower(*name)) {
			continue
		}
		out = append(out, recordView(d.Name, rec, own[d.Name].hasRecord(rec.ID)))
	}
	if g.JSON {
		return r.printJSON(out)
	}
	printRecords(a, out)
	return nil
}

func (r *Run) domainAndRecords(name string) (*godo.Domain, []godo.DomainRecord, error) {
	ds, err := r.Cloud.ListDomains(r.Ctx)
	if err != nil {
		return nil, nil, err
	}
	d, err := findDomain(ds, name)
	if err != nil {
		return nil, nil, err
	}
	recs, err := r.Cloud.ListDomainRecords(r.Ctx, d.Name)
	return d, recs, err
}

func (a *App) dnsCreate(args []string) error {
	fs, g := a.newFlags("dns create")
	f := addRecordFlags(fs, defaultTTL)
	mo := addManifestFlags(fs)
	pos, err := a.parseFlags(fs, args, "dns create")
	if err != nil {
		return err
	}
	if len(pos) != 1 || *f.typ == "" || *f.data == "" {
		return usagef("usage: docli dns create DOMAIN --type A|AAAA|CNAME|TXT|MX|NS|CAA --name NAME|@ --data VALUE [--ttl %d] [--priority N (MX)] [--flags N --tag issue (CAA)]", defaultTTL)
	}
	domain := strings.ToLower(strings.TrimSuffix(pos[0], "."))
	nm, err := normalizeRecordName(domain, *f.name)
	if err != nil {
		return usagef("%v", err)
	}
	spec := recordSpec{Type: strings.ToUpper(*f.typ), Name: nm, Data: *f.data, TTL: *f.ttl, Priority: *f.priority, Flags: *f.flags, Tag: *f.tag}
	if err := validateRecord(&spec); err != nil {
		return usagef("%v", err)
	}
	r, err := a.start(*g)
	if err != nil {
		return err
	}
	d, recs, err := r.domainAndRecords(domain)
	if err != nil {
		return err
	}
	if err := recordConflict(&spec, recs, 0); err != nil {
		return err
	}
	p := Plan{Action: "dns.create", Summary: fmt.Sprintf("add a %s record %s in %s", spec.Type, fqdn(d.Name, spec.Name), d.Name),
		Steps: []string{describeRecord(spec), "docli will remember it created this record in the manifest, so it may update or delete it later"}}
	if spec.Type == "NS" && spec.Name == "@" {
		p.Warnings = append(p.Warnings, "an extra NS record at the apex changes who answers for the whole domain")
	}
	if mo.Skip {
		p.Warnings = append(p.Warnings, "--no-manifest: docli will not remember it created this record, so changing it later needs --force-unmanaged")
	}
	ok, err := r.Approve(p, approveOpts{})
	if !ok || err != nil {
		return err
	}
	rec, err := r.Cloud.CreateDomainRecord(r.Ctx, d.Name, spec.request())
	if err != nil {
		if statusOf(err) == 0 {
			return fmt.Errorf("the create request failed and it is not known whether the record was added (%v). docli does not retry a create. Check with: docli dns list %s", err, d.Name)
		}
		return err
	}
	r.updateManifest(*mo, false, change{Section: "domains", ID: d.Name, AddRecords: []int{rec.ID}})
	if g.JSON {
		return r.printJSON(recordView(d.Name, *rec, !mo.Skip))
	}
	fmt.Fprintf(a.Stdout, "Added %s record %s (id %d)\n", rec.Type, fqdn(d.Name, rec.Name), rec.ID)
	return nil
}

func describeRecord(s recordSpec) string {
	t := fmt.Sprintf("%s %s -> %s, TTL %ds", s.Type, s.Name, s.Data, s.TTL)
	if s.Type == "MX" {
		t += fmt.Sprintf(", priority %d", s.Priority)
	}
	if s.Type == "CAA" {
		t += fmt.Sprintf(", flags %d, tag %s", s.Flags, s.Tag)
	}
	return t
}

func specOf(rec godo.DomainRecord) recordSpec {
	s := recordSpec{Type: rec.Type, Name: rec.Name, Data: rec.Data, TTL: rec.TTL, Priority: -1, Flags: rec.Flags, Tag: rec.Tag}
	if rec.Type == "MX" {
		s.Priority = rec.Priority
	}
	return s
}

func findRecord(recs []godo.DomainRecord, ref, domain string) (*godo.DomainRecord, error) {
	id, err := strconv.Atoi(ref)
	if err != nil {
		return nil, usagef("the record is given by id (a number); find it with: docli dns list %s", domain)
	}
	for i := range recs {
		if recs[i].ID == id {
			return &recs[i], nil
		}
	}
	return nil, fmt.Errorf("%s has no record with id %d. List them with: docli dns list %s", domain, id, domain)
}

func (a *App) dnsUpdate(args []string) error {
	fs, g := a.newFlags("dns update")
	f := addRecordFlags(fs, 0)
	force := fs.Bool("force-unmanaged", false, "")
	confirm := fs.String("confirm-name", "", "")
	mo := addManifestFlags(fs)
	pos, err := a.parseFlags(fs, args, "dns update")
	if err != nil {
		return err
	}
	if len(pos) != 2 {
		return usagef("usage: docli dns update DOMAIN RECORD_ID [--data V] [--name N] [--ttl N] [--priority N] [--flags N] [--tag T]")
	}
	if *f.typ != "" {
		return usagef("a record's type cannot be changed; delete it and create a new one")
	}
	if *f.name == "" && *f.data == "" && *f.ttl == 0 && *f.priority == -1 && *f.flags == 0 && *f.tag == "" {
		return usagef("nothing to change: give at least one of --data, --name, --ttl, --priority, --flags, --tag")
	}
	domain := strings.ToLower(strings.TrimSuffix(pos[0], "."))
	r, err := a.start(*g)
	if err != nil {
		return err
	}
	own, err := r.loadOwnership(*mo)
	if err != nil {
		return err
	}
	d, recs, err := r.domainAndRecords(domain)
	if err != nil {
		return err
	}
	cur, err := findRecord(recs, pos[1], d.Name)
	if err != nil {
		return err
	}
	next := specOf(*cur)
	if *f.name != "" {
		if next.Name, err = normalizeRecordName(d.Name, *f.name); err != nil {
			return usagef("%v", err)
		}
	}
	if *f.data != "" {
		next.Data = *f.data
	}
	if *f.ttl != 0 {
		next.TTL = *f.ttl
	}
	if *f.priority != -1 {
		next.Priority = *f.priority
	}
	if *f.flags != 0 {
		next.Flags = *f.flags
	}
	if *f.tag != "" {
		next.Tag = *f.tag
	}
	if err := validateRecord(&next); err != nil {
		return usagef("%v", err)
	}
	if err := recordConflict(&next, recs, cur.ID); err != nil {
		return err
	}
	spec := deleteSpec{Kind: "DNS record", Verb: "update", Name: fqdn(d.Name, cur.Name), Managed: own[d.Name].hasRecord(cur.ID), Rule: domainOwnRule,
		Summary: fmt.Sprintf("update the %s record %s (id %d) in %s", cur.Type, fqdn(d.Name, cur.Name), cur.ID, d.Name),
		Steps:   []string{"now:  " + describeRecord(specOf(*cur)), "after: " + describeRecord(next)}}
	ok, err := r.ApproveDelete(spec, *force, *confirm)
	if !ok || err != nil {
		return err
	}
	rec, err := r.Cloud.EditDomainRecord(r.Ctx, d.Name, cur.ID, next.request())
	if err != nil {
		return err
	}
	r.updateManifest(*mo, false, change{Section: "domains", ID: d.Name})
	if g.JSON {
		return r.printJSON(recordView(d.Name, *rec, spec.Managed))
	}
	fmt.Fprintf(a.Stdout, "Updated %s record %s (id %d)\n", rec.Type, fqdn(d.Name, rec.Name), rec.ID)
	return nil
}

func (a *App) dnsDelete(args []string) error {
	fs, g := a.newFlags("dns delete")
	force := fs.Bool("force-unmanaged", false, "")
	confirm := fs.String("confirm-name", "", "")
	mo := addManifestFlags(fs)
	pos, err := a.parseFlags(fs, args, "dns delete")
	if err != nil {
		return err
	}
	if len(pos) != 2 {
		return usagef("usage: docli dns delete DOMAIN RECORD_ID")
	}
	domain := strings.ToLower(strings.TrimSuffix(pos[0], "."))
	r, err := a.start(*g)
	if err != nil {
		return err
	}
	own, err := r.loadOwnership(*mo)
	if err != nil {
		return err
	}
	d, recs, err := r.domainAndRecords(domain)
	if err != nil {
		return err
	}
	cur, err := findRecord(recs, pos[1], d.Name)
	if err != nil {
		return err
	}
	spec := deleteSpec{Kind: "DNS record", Name: fqdn(d.Name, cur.Name), Managed: own[d.Name].hasRecord(cur.ID), Rule: domainOwnRule,
		Steps: []string{fmt.Sprintf("remove %s (id %d) from %s", describeRecord(specOf(*cur)), cur.ID, d.Name)}}
	ok, err := r.ApproveDelete(spec, *force, *confirm)
	if !ok || err != nil {
		return err
	}
	if err := r.Cloud.DeleteDomainRecord(r.Ctx, d.Name, cur.ID); err != nil {
		return err
	}
	r.updateManifest(*mo, false, change{Section: "domains", ID: d.Name})
	return r.deleted("dns record", itoa(cur.ID), spec.Name)
}
