package main

import (
	"context"
	"flag"
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/digitalocean/godo"
)

// Managed PostgreSQL. Ownership is the docli:managed tag on the cluster (clusters can be tagged).
//
// PASSWORDS. They arrive in API answers but nothing here prints them: the JSON views have no password field,
// errors are scrubbed (secrets.go), and the only output is a 0600 file named with --password-file.

const dbEngine = "pg"

func (a *App) cmdDB(args []string) error {
	if len(args) == 0 {
		return usagef("usage: docli db list|get|create|delete|resize|wait|connection|user|database|firewall|pool|backups (try: docli help db)")
	}
	rest := args[1:]
	switch args[0] {
	case "list":
		return a.dbList(rest)
	case "get":
		return a.dbGet(rest)
	case "create":
		return a.dbCreate(rest)
	case "delete":
		return a.dbDelete(rest)
	case "resize":
		return a.dbResize(rest)
	case "wait":
		return a.dbWait(rest)
	case "connection":
		return a.dbConnection(rest)
	case "user":
		return a.dbSub("user", rest, a.dbUserList, a.dbUserCreate, a.dbUserDelete)
	case "database":
		return a.dbSub("database", rest, a.dbDatabaseList, a.dbDatabaseCreate, a.dbDatabaseDelete)
	case "pool":
		return a.dbSub("pool", rest, a.dbPoolList, a.dbPoolCreate, a.dbPoolDelete)
	case "firewall":
		if len(rest) == 0 || (rest[0] != "list" && rest[0] != "set") {
			return usagef("usage: docli db firewall list CLUSTER | set CLUSTER (--droplet D | --tag T | --ip ADDR)... [--allow-open]")
		}
		if rest[0] == "list" {
			return a.dbFirewallList(rest[1:])
		}
		return a.dbFirewallSet(rest[1:])
	case "backups":
		if len(rest) == 0 || rest[0] != "list" {
			return usagef("usage: docli db backups list CLUSTER")
		}
		return a.dbBackupsList(rest[1:])
	}
	return usagef("unknown db command %q (try: docli help db)", args[0])
}

func (a *App) dbSub(kind string, args []string, list, create, del func([]string) error) error {
	if len(args) == 0 {
		return usagef("usage: docli db %s list|create|delete", kind)
	}
	switch args[0] {
	case "list":
		return list(args[1:])
	case "create":
		return create(args[1:])
	case "delete":
		return del(args[1:])
	}
	return usagef("unknown db %s command %q (list, create, delete)", kind, args[0])
}

// ---- finding things ----

// resolveDB finds a cluster by name or id. It also registers every password it sees so none can leak into an error.
func (r *Run) resolveDB(ref string) (*godo.Database, error) {
	all, err := r.Cloud.ListDatabases(r.Ctx)
	if err != nil {
		return nil, err
	}
	var hits []godo.Database
	for _, d := range all {
		r.noteSecrets(&d)
		if d.Name == ref || d.ID == ref {
			hits = append(hits, d)
		}
	}
	switch len(hits) {
	case 0:
		return nil, fmt.Errorf("no database cluster named %q (or with that id). List them with: docli db list", ref)
	case 1:
		return &hits[0], nil
	}
	return nil, fmt.Errorf("%d database clusters are named %q; use the id instead", len(hits), ref)
}

func (r *Run) noteSecrets(d *godo.Database) {
	for _, c := range []*godo.DatabaseConnection{d.Connection, d.PrivateConnection, d.StandbyConnection, d.StandbyPrivateConnection} {
		if c != nil {
			r.rememberSecret(c.Password)
		}
	}
	for _, u := range d.Users {
		r.rememberSecret(u.Password)
	}
}

func isManagedDB(d *godo.Database) bool { return hasTag(d.Tags, managedTag) }

var dbNameRE = regexp.MustCompile(`^[a-z][a-z0-9-]{1,61}[a-z0-9]$`)
var dbIdentRE = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]{0,62}$`)

// sizePrice looks a database size up in the sizes list. Whether DigitalOcean's sizes list includes database
// sizes has not been checked against the real API; when it does not, the price is unknown and the plan says so.
func sizePrice(sizes []godo.Size, slug string) (float64, bool) {
	for _, s := range sizes {
		if s.Slug == slug {
			return s.PriceMonthly, true
		}
	}
	return 0, false
}

func (r *Run) loadSizesQuiet() []godo.Size {
	s, err := r.Cloud.ListSizes(r.Ctx)
	if err != nil {
		return nil
	}
	return s
}

// checkDBOptions validates version, region, node count and size against what DigitalOcean offers for PostgreSQL.
// Empty option lists (a changed API) skip that check rather than block.
func checkDBOptions(o *godo.DatabaseOptions, version, region, size string, nodes int) error {
	pg := o.PostgresSQLOptions
	if version != "" && len(pg.Versions) > 0 && !contains(pg.Versions, version) {
		return fmt.Errorf("PostgreSQL version %q is not offered. Available: %s", version, strings.Join(pg.Versions, ", "))
	}
	if region != "" && len(pg.Regions) > 0 && !contains(pg.Regions, region) {
		return fmt.Errorf("managed PostgreSQL is not offered in %q. Available: %s", region, strings.Join(pg.Regions, ", "))
	}
	if len(pg.Layouts) == 0 {
		return nil
	}
	var counts []string
	for _, l := range pg.Layouts {
		counts = append(counts, itoa(l.NodeNum))
		if l.NodeNum != nodes {
			continue
		}
		if size != "" && !contains(l.Sizes, size) {
			return fmt.Errorf("size %q is not offered for %d node(s). Available: %s", size, nodes, strings.Join(l.Sizes, ", "))
		}
		return nil
	}
	return fmt.Errorf("%d node(s) is not offered. Available node counts: %s", nodes, strings.Join(counts, ", "))
}

// ---- trusted sources ----

type srcFlags struct{ droplets, tags, ips stringList }

func addSourceFlags(fs *flag.FlagSet, prefix string) *srcFlags {
	sf := &srcFlags{}
	fs.Var(&sf.droplets, prefix+"droplet", "")
	fs.Var(&sf.tags, prefix+"tag", "")
	fs.Var(&sf.ips, prefix+"ip", "")
	return sf
}

func (sf *srcFlags) any() bool { return len(sf.droplets)+len(sf.tags)+len(sf.ips) > 0 }

// resolveSources turns the flags into API rules, sorted and without duplicates. open is true when a rule allows
// every address.
func (r *Run) resolveSources(sf *srcFlags) (rules []*godo.DatabaseFirewallRule, open bool, err error) {
	seen := map[string]bool{}
	add := func(t, v string) {
		if !seen[t+":"+v] {
			seen[t+":"+v] = true
			rules = append(rules, &godo.DatabaseFirewallRule{Type: t, Value: v})
		}
	}
	for _, ref := range sf.droplets {
		d, e := r.resolveDroplet(ref)
		if e != nil {
			return nil, false, e
		}
		add("droplet", itoa(d.ID))
	}
	for _, t := range sf.tags {
		if e := validateTag(t); e != nil {
			return nil, false, usagef("%v", e)
		}
		add("tag", t)
	}
	for _, ip := range sf.ips {
		n, e := normalizeAddr(ip)
		if e != nil {
			return nil, false, usagef("%v", e)
		}
		if n == "0.0.0.0/0" || n == "::/0" {
			open = true
		}
		add("ip_addr", n)
	}
	sort.Slice(rules, func(i, j int) bool { return sourceKey(*rules[i]) < sourceKey(*rules[j]) })
	return rules, open, nil
}

func (r *Run) dropletNames() map[string]string {
	out := map[string]string{}
	ds, err := r.Cloud.ListDroplets(r.Ctx, "")
	if err != nil {
		return out
	}
	for _, d := range ds {
		out[itoa(d.ID)] = d.Name
	}
	return out
}

func describeSource(t, v string, names map[string]string) string {
	switch t {
	case "droplet":
		if n := names[v]; n != "" {
			return fmt.Sprintf("droplet %s (id %s)", n, v)
		}
		return "droplet id " + v
	case "tag":
		return "droplets tagged " + v
	case "ip_addr":
		return "address " + v
	}
	return t + " " + v
}

// ---- list / get ----

func (a *App) dbList(args []string) error {
	fs, g := a.newFlags("db list")
	if _, err := a.parseFlags(fs, args, "db list"); err != nil {
		return err
	}
	r, err := a.start(*g)
	if err != nil {
		return err
	}
	all, err := r.Cloud.ListDatabases(r.Ctx)
	if err != nil {
		return err
	}
	var shown []godo.Database
	for _, d := range all {
		r.noteSecrets(&d)
		if r.G.Region == "" || d.RegionSlug == r.G.Region {
			shown = append(shown, d)
		}
	}
	if g.JSON {
		out := []dbJSON{}
		for _, d := range shown {
			out = append(out, dbView(d))
		}
		return r.printJSON(out)
	}
	var rows [][]string
	for _, d := range shown {
		rows = append(rows, []string{d.ID, d.Name, d.EngineSlug + " " + d.VersionSlug, d.Status, d.RegionSlug, d.SizeSlug, itoa(d.NumNodes), yesNo(hasTag(d.Tags, managedTag))})
	}
	printTable(a.Stdout, []string{"ID", "NAME", "ENGINE", "STATUS", "REGION", "SIZE", "NODES", "MANAGED"}, rows)
	return nil
}

func (r *Run) sourceViews(id string) ([]sourceJSON, error) {
	rules, err := r.Cloud.GetDatabaseFirewall(r.Ctx, id)
	if err != nil {
		return nil, err
	}
	names := r.dropletNames()
	out := []sourceJSON{}
	for _, s := range sortedSources(rules) {
		out = append(out, sourceJSON{s.UUID, s.Type, s.Value, names[s.Value]})
	}
	return out, nil
}

func (a *App) dbGet(args []string) error {
	fs, g := a.newFlags("db get")
	pos, err := a.parseFlags(fs, args, "db get")
	if err != nil {
		return err
	}
	if len(pos) != 1 {
		return usagef("usage: docli db get CLUSTER")
	}
	r, err := a.start(*g)
	if err != nil {
		return err
	}
	d, err := r.resolveDB(pos[0])
	if err != nil {
		return err
	}
	srcs, err := r.sourceViews(d.ID)
	if err != nil {
		return err
	}
	v := dbView(*d)
	if g.JSON {
		return r.printJSON(dbDetailJSON{v, srcs})
	}
	fmt.Fprintf(a.Stdout, "ID:        %s\nName:      %s\nEngine:    %s %s\nStatus:    %s\nManaged:   %s\nRegion:    %s\nSize:      %s x %d node(s)\nVPC:       %s\nTags:      %s\nHost:      %s port %d\nPrivate:   %s port %d\nDatabase:  %s\nCreated:   %s\n",
		v.ID, v.Name, v.Engine, v.Version, v.Status, yesNo(v.Managed), v.Region, v.SizeSlug, v.NumNodes, orDash(v.VPCID), orDash(strings.Join(v.Tags, ",")),
		orDash(v.Connection.Host), v.Connection.Port, orDash(v.PrivateConnection.Host), v.PrivateConnection.Port, orDash(v.Connection.Database), orDash(v.CreatedAt))
	if len(srcs) == 0 {
		fmt.Fprintln(a.Stdout, "Trusted sources: none (see: docli help db)")
	}
	for _, s := range srcs {
		fmt.Fprintf(a.Stdout, "Trusted source: %s\n", describeSource(s.Type, s.Value, map[string]string{s.Value: s.Name}))
	}
	fmt.Fprintln(a.Stdout, "Password:  not shown. Save it with: docli db connection NAME --password-file PATH")
	return nil
}

// ---- create ----

func (a *App) dbCreate(args []string) error {
	fs, g := a.newFlags("db create")
	name := fs.String("name", "", "")
	ver := fs.String("version", "", "")
	size := fs.String("size", "", "")
	nodes := fs.Int("nodes", 0, "")
	vpcRef := fs.String("vpc", "", "")
	stack := fs.String("stack", "", "")
	wait := fs.Bool("wait", false, "")
	waitTimeout := fs.Duration("wait-timeout", 20*time.Minute, "")
	allowOpen := fs.Bool("allow-open", false, "")
	confirm := fs.String("confirm-name", "", "")
	pwFile := fs.String("password-file", "", "")
	force := fs.Bool("force", false, "")
	var userTags stringList
	fs.Var(&userTags, "tag", "")
	projRef := fs.String("project", "", "")
	sf := addSourceFlags(fs, "trusted-")
	mo := addManifestFlags(fs)
	pos, err := a.parseFlags(fs, args, "db create")
	if err != nil {
		return err
	}
	if len(pos) > 0 || *name == "" || *ver == "" || *size == "" || *nodes == 0 {
		return usagef("usage: docli db create --name NAME --region REGION --version 16 --size db-s-1vcpu-1gb --nodes 1 --vpc VPC (--trusted-droplet D | --trusted-tag T | --trusted-ip ADDR)... [--wait] [--password-file PATH]")
	}
	if !dbNameRE.MatchString(*name) {
		return usagef("%q is not a valid cluster name: 3-63 characters, lower case letters, digits and dashes, starting with a letter", *name)
	}
	if *nodes < 1 || *nodes > 3 {
		return usagef("--nodes must be 1, 2 or 3")
	}
	vpcName := a.envOr(*vpcRef, "DOCLI_VPC")
	if vpcName == "" {
		return usagef("a VPC is required so the database is reachable on the private network: --vpc NAME (or set DOCLI_VPC). Create one with: docli vpc create")
	}
	tags := []string{managedTag}
	if *stack != "" {
		if !stackRE.MatchString(*stack) {
			return usagef("--stack %q must use letters, digits, dash and underscore only", *stack)
		}
		tags = append(tags, "docli:stack:"+*stack)
	}
	for _, t := range userTags {
		if err := validateTag(t); err != nil {
			return usagef("%v", err)
		}
		if !hasTag(tags, t) {
			tags = append(tags, t)
		}
	}
	if err := checkSecretTarget(*pwFile, *force); err != nil {
		return usagef("%v", err)
	}
	r, err := a.start(*g)
	if err != nil {
		return err
	}
	region := r.G.Region
	if region == "" {
		return usagef("a region is required: --region nyc3 (or set DOCLI_REGION)")
	}
	rules, open, err := r.resolveSources(sf)
	if err != nil {
		return err
	}
	if (len(rules) == 0 || open) && !*allowOpen {
		return refusedf("refusing to create a database that any address could try to reach: give at least one trusted source (--trusted-droplet, --trusted-tag or --trusted-ip), or pass --allow-open and type the cluster name to accept that. Nothing was changed")
	}
	opts, err := r.Cloud.DatabaseOptions(r.Ctx)
	if err != nil {
		return err
	}
	if err := checkDBOptions(opts, *ver, region, *size, *nodes); err != nil {
		return err
	}
	vpcs, err := r.Cloud.ListVPCs(r.Ctx)
	if err != nil {
		return err
	}
	v, err := findVPC(vpcs, vpcName)
	if err != nil {
		return err
	}
	if v.RegionSlug != region {
		return fmt.Errorf("VPC %q is in %s but the database is for %s", v.Name, v.RegionSlug, region)
	}
	proj, err := r.lookupProject(*projRef)
	if err != nil {
		return err
	}
	existing, err := r.Cloud.ListDatabases(r.Ctx)
	if err != nil {
		return err
	}
	for _, d := range existing {
		if d.Name == *name {
			return fmt.Errorf("a database cluster named %q already exists (id %s). Nothing was changed", *name, d.ID)
		}
	}

	req := &godo.DatabaseCreateRequest{Name: *name, EngineSlug: dbEngine, Version: *ver, SizeSlug: *size, Region: region, NumNodes: *nodes,
		PrivateNetworkUUID: v.ID, Tags: tags, Rules: []*godo.DatabaseCreateFirewallRule{}}
	names := r.dropletNames()
	var srcDesc []string
	for _, ru := range rules {
		req.Rules = append(req.Rules, &godo.DatabaseCreateFirewallRule{Type: ru.Type, Value: ru.Value})
		srcDesc = append(srcDesc, describeSource(ru.Type, ru.Value, names))
	}
	p := Plan{Action: "db.create", Summary: fmt.Sprintf("create PostgreSQL %s cluster %q in %s", *ver, *name, region)}
	p.Steps = []string{
		fmt.Sprintf("size: %s x %d node(s)", *size, *nodes),
		fmt.Sprintf("network: VPC %s (%s); the private host name is only reachable from inside it", v.Name, v.IPRange),
		"trusted sources: " + orDash(strings.Join(srcDesc, "; ")),
		"tags that will be applied: " + strings.Join(tags, ", ") + " (created if missing)",
		"creation takes several minutes; add --wait to wait for it, or run: docli db wait " + *name,
	}
	if price, ok := sizePrice(r.loadSizesQuiet(), *size); ok {
		total := round2(price * float64(*nodes))
		p.MonthlyUSD = &total
		p.Steps = append(p.Steps, fmt.Sprintf("price: %s per node per month x %d node(s) (taken from the API's size list; storage and extras are not included)", money(price), *nodes))
	} else {
		p.Warnings = append(p.Warnings, fmt.Sprintf("the API's size list has no price for %q, so the monthly cost is NOT shown. Check DigitalOcean's pricing page before you proceed", *size))
	}
	if *pwFile != "" {
		p.Steps = append(p.Steps, fmt.Sprintf("the admin password will be written to %s (mode 0600) and never printed", *pwFile))
	}
	if s := projectStep(proj); s != "" {
		p.Steps = append(p.Steps, s)
	}
	if len(rules) == 0 || open {
		p.Warnings = append(p.Warnings, "OPEN: no trusted sources (or 0.0.0.0/0), so any address that has the credentials can connect")
	}
	if mo.Skip {
		p.Warnings = append(p.Warnings, "--no-manifest: the cluster is still tagged "+managedTag+", but it will not be recorded in the manifest")
	}
	approve := approveOpts{}
	if open || len(rules) == 0 {
		approve = approveOpts{TypeName: *name, ConfirmName: *confirm}
	}
	ok, err := r.Approve(p, approve)
	if !ok || err != nil {
		return err
	}
	for _, t := range tags {
		if err := r.Cloud.EnsureTag(r.Ctx, t); err != nil {
			return fmt.Errorf("could not create the tag %q, so no cluster was created: %v", t, err)
		}
	}
	d, err := r.Cloud.CreateDatabase(r.Ctx, req)
	if err != nil {
		if statusOf(err) == 0 {
			return fmt.Errorf("the create request failed and it is not known whether the cluster was made (%v). docli does not retry a create. Check with: docli db list", err)
		}
		return err
	}
	r.noteSecrets(d)
	pw := ""
	if d.Connection != nil {
		pw = d.Connection.Password
	}
	created := d.ID
	r.infof("created database cluster %s (%s), status %s", d.Name, d.ID, d.Status)
	var waitErr error
	if *wait {
		var wd *godo.Database
		if wd, waitErr = r.waitForDB(created, "online", *waitTimeout); wd != nil {
			r.noteSecrets(wd)
			d = wd
			if d.Connection != nil && d.Connection.Password != "" {
				pw = d.Connection.Password
			}
		}
	}
	r.updateManifest(*mo, false, change{Section: "databases", ID: created})
	if waitErr != nil {
		return waitErr
	}
	if err := r.putInProject(proj, d.URN(), "database", d.Name); err != nil {
		return err
	}
	if *pwFile != "" {
		if err := r.savePassword(*pwFile, pw, *force, d.Name); err != nil {
			return err
		}
	}
	if g.JSON {
		return r.printJSON(dbView(*d))
	}
	fmt.Fprintf(a.Stdout, "Created database cluster %q (id %s), status %s. Password: not shown.\n", d.Name, d.ID, d.Status)
	return nil
}

// savePassword writes a password to the file the person named. The cluster or user already exists, so a failure
// says how to fetch the password again, and never includes it.
func (r *Run) savePassword(path, pw string, force bool, cluster string) error {
	if pw == "" {
		return fmt.Errorf("DigitalOcean did not return a password yet, so %s was not written. Once the cluster is online run: docli db connection %s --password-file %s", path, cluster, path)
	}
	r.rememberSecret(pw)
	if err := writeSecretFile(path, pw, force); err != nil {
		return fmt.Errorf("%v. The cluster exists; fetch the password again with: docli db connection %s --password-file PATH", err, cluster)
	}
	r.infof("wrote the password to %s (mode 0600)", path)
	return nil
}

// waitForDB polls until the cluster reaches status, printing progress to stderr.
func (r *Run) waitForDB(id, status string, timeout time.Duration) (*godo.Database, error) {
	const every = 15 * time.Second
	var elapsed time.Duration
	ctx, cancel := context.WithCancel(r.Ctx)
	defer cancel()
	for {
		d, err := r.Cloud.GetDatabase(ctx, id)
		if err != nil {
			return nil, fmt.Errorf("lost track of database cluster %s while waiting: %v. It still exists; check with: docli db get %s", id, err, id)
		}
		r.noteSecrets(d)
		if d.Status == status {
			r.infof("database cluster %s is %s", d.Name, status)
			return d, nil
		}
		if elapsed >= timeout {
			return d, fmt.Errorf("database cluster %s is still %q after %s. It still exists and is not deleted; check with: docli db get %s", d.Name, d.Status, timeout, d.Name)
		}
		r.infof("waiting for database cluster %s to be %s: now %s (%ds elapsed)", d.Name, status, d.Status, int(elapsed.Seconds()))
		if err := r.Sleep(ctx, every); err != nil {
			return d, err
		}
		elapsed += every
	}
}

func (a *App) dbWait(args []string) error {
	fs, g := a.newFlags("db wait")
	waitTimeout := fs.Duration("wait-timeout", 20*time.Minute, "")
	pos, err := a.parseFlags(fs, args, "db wait")
	if err != nil {
		return err
	}
	if len(pos) != 1 {
		return usagef("usage: docli db wait CLUSTER [--wait-timeout 20m]")
	}
	r, err := a.start(*g)
	if err != nil {
		return err
	}
	d, err := r.resolveDB(pos[0])
	if err != nil {
		return err
	}
	d, err = r.waitForDB(d.ID, "online", *waitTimeout)
	if err != nil {
		return err
	}
	if g.JSON {
		return r.printJSON(dbView(*d))
	}
	fmt.Fprintf(a.Stdout, "Database cluster %q is online.\n", d.Name)
	return nil
}

// ---- delete / resize ----

func (a *App) dbDelete(args []string) error {
	fs, g := a.newFlags("db delete")
	force := fs.Bool("force-unmanaged", false, "")
	confirm := fs.String("confirm-name", "", "")
	mo := addManifestFlags(fs)
	pos, err := a.parseFlags(fs, args, "db delete")
	if err != nil {
		return err
	}
	if len(pos) != 1 {
		return usagef("usage: docli db delete CLUSTER [--yes --confirm-name CLUSTER]")
	}
	r, err := a.start(*g)
	if err != nil {
		return err
	}
	d, err := r.resolveDB(pos[0])
	if err != nil {
		return err
	}
	spec := deleteSpec{Kind: "database cluster", Name: d.Name, Managed: isManagedDB(d), Rule: "the tag " + managedTag, Always: true,
		Steps: []string{fmt.Sprintf("permanently destroy database cluster %q (id %s, status %s) with all its databases, users and pools; the data cannot be recovered", d.Name, d.ID, d.Status)}}
	ok, err := r.ApproveDelete(spec, *force, *confirm)
	if !ok || err != nil {
		return err
	}
	if err := r.Cloud.DeleteDatabase(r.Ctx, d.ID); err != nil {
		return err
	}
	r.updateManifest(*mo, false, change{Section: "databases", ID: d.ID})
	return r.deleted("database cluster", d.ID, d.Name)
}

func (a *App) dbResize(args []string) error {
	fs, g := a.newFlags("db resize")
	size := fs.String("size", "", "")
	nodes := fs.Int("nodes", 0, "")
	wait := fs.Bool("wait", false, "")
	waitTimeout := fs.Duration("wait-timeout", 30*time.Minute, "")
	mo := addManifestFlags(fs)
	pos, err := a.parseFlags(fs, args, "db resize")
	if err != nil {
		return err
	}
	if len(pos) != 1 || (*size == "" && *nodes == 0) {
		return usagef("usage: docli db resize CLUSTER [--size SLUG] [--nodes N] [--wait]")
	}
	if *nodes < 0 || *nodes > 3 {
		return usagef("--nodes must be 1, 2 or 3")
	}
	r, err := a.start(*g)
	if err != nil {
		return err
	}
	d, err := r.resolveDB(pos[0])
	if err != nil {
		return err
	}
	newSize, newNodes := firstNonEmpty(*size, d.SizeSlug), d.NumNodes
	if *nodes != 0 {
		newNodes = *nodes
	}
	if newSize == d.SizeSlug && newNodes == d.NumNodes {
		return fmt.Errorf("cluster %q is already %s x %d node(s). Nothing was changed", d.Name, d.SizeSlug, d.NumNodes)
	}
	if d.Status != "online" {
		return fmt.Errorf("cluster %q is %q, not online, so it cannot be resized now. Wait with: docli db wait %s", d.Name, d.Status, d.Name)
	}
	opts, err := r.Cloud.DatabaseOptions(r.Ctx)
	if err != nil {
		return err
	}
	if err := checkDBOptions(opts, "", "", newSize, newNodes); err != nil {
		return err
	}
	p := Plan{Action: "db.resize", Summary: fmt.Sprintf("resize database cluster %q", d.Name),
		Steps: []string{fmt.Sprintf("now:   %s x %d node(s)", d.SizeSlug, d.NumNodes), fmt.Sprintf("after: %s x %d node(s)", newSize, newNodes),
			"the cluster will show a resizing status; connections may be interrupted while it finishes. Wait with: docli db wait " + d.Name}}
	sizes := r.loadSizesQuiet()
	oldP, okOld := sizePrice(sizes, d.SizeSlug)
	newP, okNew := sizePrice(sizes, newSize)
	if okOld && okNew {
		total := round2(newP * float64(newNodes))
		p.MonthlyUSD = &total
		p.Steps = append(p.Steps, fmt.Sprintf("price: %s per month now, %s after (from the API's size list)", money(round2(oldP*float64(d.NumNodes))), money(total)))
	} else {
		p.Warnings = append(p.Warnings, "the API's size list has no price for this size, so the monthly cost is NOT shown. Check DigitalOcean's pricing page")
	}
	if !isManagedDB(d) {
		p.Warnings = append(p.Warnings, "this cluster was not created by docli")
	}
	ok, err := r.Approve(p, approveOpts{})
	if !ok || err != nil {
		return err
	}
	if err := r.Cloud.ResizeDatabase(r.Ctx, d.ID, &godo.DatabaseResizeRequest{SizeSlug: newSize, NumNodes: newNodes}); err != nil {
		return err
	}
	if *wait {
		if d, err = r.waitForDB(d.ID, "online", *waitTimeout); err != nil {
			r.updateManifest(*mo, false, change{Section: "databases", ID: d.ID})
			return err
		}
	} else if d2, err := r.Cloud.GetDatabase(r.Ctx, d.ID); err == nil {
		d = d2
	}
	r.updateManifest(*mo, false, change{Section: "databases", ID: d.ID})
	if g.JSON {
		return r.printJSON(dbView(*d))
	}
	fmt.Fprintf(a.Stdout, "Requested resize of %q to %s x %d node(s); status now %s\n", d.Name, newSize, newNodes, d.Status)
	return nil
}

// ---- connection ----

func (a *App) dbConnection(args []string) error {
	fs, g := a.newFlags("db connection")
	user := fs.String("user", "", "")
	dbname := fs.String("database", "", "")
	pwFile := fs.String("password-file", "", "")
	force := fs.Bool("force", false, "")
	pos, err := a.parseFlags(fs, args, "db connection")
	if err != nil {
		return err
	}
	if len(pos) != 1 {
		return usagef("usage: docli db connection CLUSTER [--user U] [--database D] [--password-file PATH [--force]]")
	}
	if err := checkSecretTarget(*pwFile, *force); err != nil {
		return usagef("%v", err)
	}
	r, err := a.start(*g)
	if err != nil {
		return err
	}
	d, err := r.resolveDB(pos[0])
	if err != nil {
		return err
	}
	c, pc := connView(d.Connection), connView(d.PrivateConnection)
	pw := ""
	if d.Connection != nil {
		pw = d.Connection.Password
	}
	if *user != "" && *user != c.User {
		users, err := r.Cloud.ListDatabaseUsers(r.Ctx, d.ID)
		if err != nil {
			return err
		}
		pw = ""
		found := false
		for _, u := range users {
			r.rememberSecret(u.Password)
			if u.Name == *user {
				found, pw = true, u.Password
			}
		}
		if !found {
			return fmt.Errorf("cluster %q has no user %q. List them with: docli db user list %s", d.Name, *user, d.Name)
		}
		c.User, pc.User = *user, *user
	}
	if *dbname != "" {
		dbs, err := r.Cloud.ListDatabaseDBs(r.Ctx, d.ID)
		if err != nil {
			return err
		}
		ok := false
		for _, x := range dbs {
			ok = ok || x.Name == *dbname
		}
		if !ok {
			return fmt.Errorf("cluster %q has no database %q. List them with: docli db database list %s", d.Name, *dbname, d.Name)
		}
		c.Database = *dbname
	}
	if *pwFile != "" {
		if pw == "" {
			return fmt.Errorf("DigitalOcean did not return a password for %q, so %s was not written", firstNonEmpty(*user, c.User), *pwFile)
		}
		if err := r.savePassword(*pwFile, pw, *force, d.Name); err != nil {
			return err
		}
	}
	out := connectionJSON{d.Name, c.Host, c.Port, pc.Host, pc.Port, c.Database, c.User, c.SSLMode, *pwFile}
	if g.JSON {
		return r.printJSON(out)
	}
	fmt.Fprintf(a.Stdout, "Cluster:       %s\nHost:          %s\nPort:          %d\nPrivate host:  %s\nPrivate port:  %d\nDatabase:      %s\nUser:          %s\nSSL mode:      %s\n",
		out.Name, orDash(out.Host), out.Port, orDash(out.PrivateHost), out.PrivatePort, orDash(out.Database), orDash(out.User), out.SSLMode)
	if *pwFile != "" {
		fmt.Fprintf(a.Stdout, "Password:      written to %s (mode 0600), not shown\n", *pwFile)
	} else {
		fmt.Fprintln(a.Stdout, "Password:      not shown. Save it with --password-file PATH")
	}
	return nil
}

// ---- users, databases, pools ----

// subTarget finds the cluster for a sub-resource command.
func (a *App) subTarget(g *Globals, ref string) (*Run, *godo.Database, error) {
	r, err := a.start(*g)
	if err != nil {
		return nil, nil, err
	}
	d, err := r.resolveDB(ref)
	return r, d, err
}

func (a *App) dbUserList(args []string) error {
	fs, g := a.newFlags("db user list")
	pos, err := a.parseFlags(fs, args, "db user list")
	if err != nil {
		return err
	}
	if len(pos) != 1 {
		return usagef("usage: docli db user list CLUSTER")
	}
	r, d, err := a.subTarget(g, pos[0])
	if err != nil {
		return err
	}
	us, err := r.Cloud.ListDatabaseUsers(r.Ctx, d.ID)
	if err != nil {
		return err
	}
	out := []dbUserJSON{}
	var rows [][]string
	for _, u := range us {
		r.rememberSecret(u.Password)
		out = append(out, dbUserJSON{u.Name, u.Role})
		rows = append(rows, []string{u.Name, orDash(u.Role)})
	}
	if g.JSON {
		return r.printJSON(out)
	}
	printTable(a.Stdout, []string{"USER", "ROLE"}, rows)
	return nil
}

func (a *App) dbUserCreate(args []string) error {
	fs, g := a.newFlags("db user create")
	pwFile := fs.String("password-file", "", "")
	force := fs.Bool("force", false, "")
	pos, err := a.parseFlags(fs, args, "db user create")
	if err != nil {
		return err
	}
	if len(pos) != 2 {
		return usagef("usage: docli db user create CLUSTER USER [--password-file PATH [--force]]")
	}
	if !dbIdentRE.MatchString(pos[1]) {
		return usagef("%q is not a valid user name: letters, digits and underscore, starting with a letter or underscore (up to 63 characters)", pos[1])
	}
	if err := checkSecretTarget(*pwFile, *force); err != nil {
		return usagef("%v", err)
	}
	r, d, err := a.subTarget(g, pos[0])
	if err != nil {
		return err
	}
	us, err := r.Cloud.ListDatabaseUsers(r.Ctx, d.ID)
	if err != nil {
		return err
	}
	for _, u := range us {
		r.rememberSecret(u.Password)
		if u.Name == pos[1] {
			return fmt.Errorf("cluster %q already has a user %q. Nothing was changed", d.Name, pos[1])
		}
	}
	p := Plan{Action: "db.user.create", Summary: fmt.Sprintf("create user %q on cluster %q", pos[1], d.Name), Steps: []string{"DigitalOcean generates the password"}}
	if *pwFile != "" {
		p.Steps = append(p.Steps, fmt.Sprintf("the password will be written to %s (mode 0600) and never printed", *pwFile))
	}
	if !isManagedDB(d) {
		p.Warnings = append(p.Warnings, "this cluster was not created by docli")
	}
	ok, err := r.Approve(p, approveOpts{})
	if !ok || err != nil {
		return err
	}
	u, err := r.Cloud.CreateDatabaseUser(r.Ctx, d.ID, pos[1])
	if err != nil {
		if statusOf(err) == 0 {
			return fmt.Errorf("the create request failed and it is not known whether the user was made (%v). docli does not retry a create. Check with: docli db user list %s", err, d.Name)
		}
		return err
	}
	r.rememberSecret(u.Password)
	if *pwFile != "" {
		if err := r.savePassword(*pwFile, u.Password, *force, d.Name); err != nil {
			return err
		}
	}
	if g.JSON {
		return r.printJSON(dbUserJSON{u.Name, u.Role})
	}
	fmt.Fprintf(a.Stdout, "Created user %q on %q. Password: not shown.", u.Name, d.Name)
	if *pwFile == "" {
		fmt.Fprintf(a.Stdout, " Save it with: docli db connection %s --user %s --password-file PATH", d.Name, u.Name)
	}
	fmt.Fprintln(a.Stdout)
	return nil
}

func (a *App) dbUserDelete(args []string) error {
	fs, g := a.newFlags("db user delete")
	force := fs.Bool("force-unmanaged", false, "")
	confirm := fs.String("confirm-name", "", "")
	pos, err := a.parseFlags(fs, args, "db user delete")
	if err != nil {
		return err
	}
	if len(pos) != 2 {
		return usagef("usage: docli db user delete CLUSTER USER")
	}
	if pos[1] == "doadmin" {
		return refusedf("refusing to delete doadmin: it is the cluster's administrator account. Nothing was changed")
	}
	r, d, err := a.subTarget(g, pos[0])
	if err != nil {
		return err
	}
	us, err := r.Cloud.ListDatabaseUsers(r.Ctx, d.ID)
	if err != nil {
		return err
	}
	found := false
	for _, u := range us {
		r.rememberSecret(u.Password)
		found = found || u.Name == pos[1]
	}
	if !found {
		return fmt.Errorf("cluster %q has no user %q. List them with: docli db user list %s", d.Name, pos[1], d.Name)
	}
	ok, err := r.ApproveDelete(deleteSpec{Kind: "database user", Name: pos[1], Managed: isManagedDB(d), Rule: "the tag " + managedTag + " on its cluster " + d.Name,
		Steps: []string{fmt.Sprintf("remove user %q from cluster %q; anything connecting as this user loses access", pos[1], d.Name)}}, *force, *confirm)
	if !ok || err != nil {
		return err
	}
	if err := r.Cloud.DeleteDatabaseUser(r.Ctx, d.ID, pos[1]); err != nil {
		return err
	}
	return r.deleted("database user", pos[1], pos[1])
}

func (a *App) dbDatabaseList(args []string) error {
	fs, g := a.newFlags("db database list")
	pos, err := a.parseFlags(fs, args, "db database list")
	if err != nil {
		return err
	}
	if len(pos) != 1 {
		return usagef("usage: docli db database list CLUSTER")
	}
	r, d, err := a.subTarget(g, pos[0])
	if err != nil {
		return err
	}
	dbs, err := r.Cloud.ListDatabaseDBs(r.Ctx, d.ID)
	if err != nil {
		return err
	}
	out := []dbNameJSON{}
	var rows [][]string
	for _, x := range dbs {
		out = append(out, dbNameJSON{x.Name})
		rows = append(rows, []string{x.Name})
	}
	if g.JSON {
		return r.printJSON(out)
	}
	printTable(a.Stdout, []string{"DATABASE"}, rows)
	return nil
}

func (a *App) dbDatabaseCreate(args []string) error {
	fs, g := a.newFlags("db database create")
	pos, err := a.parseFlags(fs, args, "db database create")
	if err != nil {
		return err
	}
	if len(pos) != 2 {
		return usagef("usage: docli db database create CLUSTER NAME")
	}
	if !dbIdentRE.MatchString(pos[1]) {
		return usagef("%q is not a valid database name: letters, digits and underscore, starting with a letter or underscore (up to 63 characters)", pos[1])
	}
	r, d, err := a.subTarget(g, pos[0])
	if err != nil {
		return err
	}
	dbs, err := r.Cloud.ListDatabaseDBs(r.Ctx, d.ID)
	if err != nil {
		return err
	}
	for _, x := range dbs {
		if x.Name == pos[1] {
			return fmt.Errorf("cluster %q already has a database %q. Nothing was changed", d.Name, pos[1])
		}
	}
	p := Plan{Action: "db.database.create", Summary: fmt.Sprintf("create database %q on cluster %q", pos[1], d.Name)}
	if !isManagedDB(d) {
		p.Warnings = append(p.Warnings, "this cluster was not created by docli")
	}
	ok, err := r.Approve(p, approveOpts{})
	if !ok || err != nil {
		return err
	}
	x, err := r.Cloud.CreateDatabaseDB(r.Ctx, d.ID, pos[1])
	if err != nil {
		return err
	}
	if g.JSON {
		return r.printJSON(dbNameJSON{x.Name})
	}
	fmt.Fprintf(a.Stdout, "Created database %q on %q\n", x.Name, d.Name)
	return nil
}

func (a *App) dbDatabaseDelete(args []string) error {
	fs, g := a.newFlags("db database delete")
	force := fs.Bool("force-unmanaged", false, "")
	confirm := fs.String("confirm-name", "", "")
	pos, err := a.parseFlags(fs, args, "db database delete")
	if err != nil {
		return err
	}
	if len(pos) != 2 {
		return usagef("usage: docli db database delete CLUSTER NAME [--yes --confirm-name NAME]")
	}
	if pos[1] == "defaultdb" {
		return refusedf("refusing to delete defaultdb: DigitalOcean creates it with the cluster. Nothing was changed")
	}
	r, d, err := a.subTarget(g, pos[0])
	if err != nil {
		return err
	}
	dbs, err := r.Cloud.ListDatabaseDBs(r.Ctx, d.ID)
	if err != nil {
		return err
	}
	found := false
	for _, x := range dbs {
		found = found || x.Name == pos[1]
	}
	if !found {
		return fmt.Errorf("cluster %q has no database %q. List them with: docli db database list %s", d.Name, pos[1], d.Name)
	}
	ok, err := r.ApproveDelete(deleteSpec{Kind: "database", Name: pos[1], Managed: isManagedDB(d), Always: true, Rule: "the tag " + managedTag + " on its cluster " + d.Name,
		Steps: []string{fmt.Sprintf("permanently drop database %q from cluster %q with all its tables and data", pos[1], d.Name)}}, *force, *confirm)
	if !ok || err != nil {
		return err
	}
	if err := r.Cloud.DeleteDatabaseDB(r.Ctx, d.ID, pos[1]); err != nil {
		return err
	}
	return r.deleted("database", pos[1], pos[1])
}

var poolModes = []string{"session", "transaction", "statement"}

func (a *App) dbPoolList(args []string) error {
	fs, g := a.newFlags("db pool list")
	pos, err := a.parseFlags(fs, args, "db pool list")
	if err != nil {
		return err
	}
	if len(pos) != 1 {
		return usagef("usage: docli db pool list CLUSTER")
	}
	r, d, err := a.subTarget(g, pos[0])
	if err != nil {
		return err
	}
	ps, err := r.Cloud.ListDatabasePools(r.Ctx, d.ID)
	if err != nil {
		return err
	}
	out := []poolJSON{}
	var rows [][]string
	for _, p := range ps {
		if p.Connection != nil {
			r.rememberSecret(p.Connection.Password)
		}
		v := poolView(p)
		out = append(out, v)
		rows = append(rows, []string{v.Name, v.Database, orDash(v.User), v.Mode, itoa(v.Size), orDash(v.Host), itoa(v.Port)})
	}
	if g.JSON {
		return r.printJSON(out)
	}
	printTable(a.Stdout, []string{"POOL", "DATABASE", "USER", "MODE", "SIZE", "HOST", "PORT"}, rows)
	return nil
}

func (a *App) dbPoolCreate(args []string) error {
	fs, g := a.newFlags("db pool create")
	name := fs.String("name", "", "")
	dbn := fs.String("database", "", "")
	user := fs.String("user", "", "")
	mode := fs.String("mode", "transaction", "")
	size := fs.Int("size", 10, "")
	pos, err := a.parseFlags(fs, args, "db pool create")
	if err != nil {
		return err
	}
	if len(pos) != 1 || *name == "" || *dbn == "" {
		return usagef("usage: docli db pool create CLUSTER --name POOL --database DB [--user U] [--mode transaction] [--size 10]")
	}
	if !dbIdentRE.MatchString(*name) {
		return usagef("%q is not a valid pool name: letters, digits and underscore, starting with a letter or underscore", *name)
	}
	if !contains(poolModes, *mode) {
		return usagef("--mode must be one of %s", strings.Join(poolModes, ", "))
	}
	if *size < 1 || *size > 1000 {
		return usagef("--size must be between 1 and 1000 connections")
	}
	r, d, err := a.subTarget(g, pos[0])
	if err != nil {
		return err
	}
	dbs, err := r.Cloud.ListDatabaseDBs(r.Ctx, d.ID)
	if err != nil {
		return err
	}
	hasDB := false
	for _, x := range dbs {
		hasDB = hasDB || x.Name == *dbn
	}
	if !hasDB {
		return fmt.Errorf("cluster %q has no database %q. Create it with: docli db database create %s %s", d.Name, *dbn, d.Name, *dbn)
	}
	if *user != "" {
		us, err := r.Cloud.ListDatabaseUsers(r.Ctx, d.ID)
		if err != nil {
			return err
		}
		hasUser := false
		for _, u := range us {
			r.rememberSecret(u.Password)
			hasUser = hasUser || u.Name == *user
		}
		if !hasUser {
			return fmt.Errorf("cluster %q has no user %q. Create it with: docli db user create %s %s", d.Name, *user, d.Name, *user)
		}
	}
	ps, err := r.Cloud.ListDatabasePools(r.Ctx, d.ID)
	if err != nil {
		return err
	}
	for _, p := range ps {
		if p.Name == *name {
			return fmt.Errorf("cluster %q already has a pool %q. Nothing was changed", d.Name, *name)
		}
	}
	p := Plan{Action: "db.pool.create", Summary: fmt.Sprintf("create connection pool %q on cluster %q", *name, d.Name),
		Steps: []string{fmt.Sprintf("database %s, user %s, %s mode, %d connections", *dbn, orDash(*user), *mode, *size)}}
	if !isManagedDB(d) {
		p.Warnings = append(p.Warnings, "this cluster was not created by docli")
	}
	ok, err := r.Approve(p, approveOpts{})
	if !ok || err != nil {
		return err
	}
	pl, err := r.Cloud.CreateDatabasePool(r.Ctx, d.ID, &godo.DatabaseCreatePoolRequest{User: *user, Name: *name, Size: *size, Database: *dbn, Mode: *mode})
	if err != nil {
		return err
	}
	if pl.Connection != nil {
		r.rememberSecret(pl.Connection.Password)
	}
	if g.JSON {
		return r.printJSON(poolView(*pl))
	}
	fmt.Fprintf(a.Stdout, "Created pool %q on %q\n", pl.Name, d.Name)
	return nil
}

func (a *App) dbPoolDelete(args []string) error {
	fs, g := a.newFlags("db pool delete")
	force := fs.Bool("force-unmanaged", false, "")
	confirm := fs.String("confirm-name", "", "")
	pos, err := a.parseFlags(fs, args, "db pool delete")
	if err != nil {
		return err
	}
	if len(pos) != 2 {
		return usagef("usage: docli db pool delete CLUSTER POOL")
	}
	r, d, err := a.subTarget(g, pos[0])
	if err != nil {
		return err
	}
	ps, err := r.Cloud.ListDatabasePools(r.Ctx, d.ID)
	if err != nil {
		return err
	}
	found := false
	for _, p := range ps {
		found = found || p.Name == pos[1]
	}
	if !found {
		return fmt.Errorf("cluster %q has no pool %q. List them with: docli db pool list %s", d.Name, pos[1], d.Name)
	}
	ok, err := r.ApproveDelete(deleteSpec{Kind: "connection pool", Name: pos[1], Managed: isManagedDB(d), Rule: "the tag " + managedTag + " on its cluster " + d.Name,
		Steps: []string{fmt.Sprintf("remove pool %q from cluster %q; clients connected through it are disconnected", pos[1], d.Name)}}, *force, *confirm)
	if !ok || err != nil {
		return err
	}
	if err := r.Cloud.DeleteDatabasePool(r.Ctx, d.ID, pos[1]); err != nil {
		return err
	}
	return r.deleted("connection pool", pos[1], pos[1])
}

// ---- backups, firewall ----

func (a *App) dbBackupsList(args []string) error {
	fs, g := a.newFlags("db backups list")
	pos, err := a.parseFlags(fs, args, "db backups list")
	if err != nil {
		return err
	}
	if len(pos) != 1 {
		return usagef("usage: docli db backups list CLUSTER")
	}
	r, d, err := a.subTarget(g, pos[0])
	if err != nil {
		return err
	}
	bs, err := r.Cloud.ListDatabaseBackups(r.Ctx, d.ID)
	if err != nil {
		return err
	}
	out := []backupJSON{}
	var rows [][]string
	for _, b := range bs {
		out = append(out, backupJSON{fmtTime(b.CreatedAt), b.SizeGigabytes})
		rows = append(rows, []string{fmtTime(b.CreatedAt), strconv.FormatFloat(b.SizeGigabytes, 'f', -1, 64) + " GB"})
	}
	if g.JSON {
		return r.printJSON(out)
	}
	printTable(a.Stdout, []string{"CREATED", "SIZE"}, rows)
	return nil
}

func (a *App) dbFirewallList(args []string) error {
	fs, g := a.newFlags("db firewall list")
	pos, err := a.parseFlags(fs, args, "db firewall list")
	if err != nil {
		return err
	}
	if len(pos) != 1 {
		return usagef("usage: docli db firewall list CLUSTER")
	}
	r, d, err := a.subTarget(g, pos[0])
	if err != nil {
		return err
	}
	srcs, err := r.sourceViews(d.ID)
	if err != nil {
		return err
	}
	if g.JSON {
		return r.printJSON(srcs)
	}
	var rows [][]string
	for _, s := range srcs {
		rows = append(rows, []string{s.Type, s.Value, orDash(s.Name)})
	}
	printTable(a.Stdout, []string{"TYPE", "VALUE", "NAME"}, rows)
	if len(srcs) == 0 {
		fmt.Fprintln(a.Stdout, "No trusted sources: see 'docli help db' for what that means.")
	}
	return nil
}

func (a *App) dbFirewallSet(args []string) error {
	fs, g := a.newFlags("db firewall set")
	sf := addSourceFlags(fs, "")
	allowOpen := fs.Bool("allow-open", false, "")
	confirm := fs.String("confirm-name", "", "")
	mo := addManifestFlags(fs)
	pos, err := a.parseFlags(fs, args, "db firewall set")
	if err != nil {
		return err
	}
	if len(pos) != 1 {
		return usagef("usage: docli db firewall set CLUSTER (--droplet D | --tag T | --ip ADDR)... [--allow-open --confirm-name CLUSTER]")
	}
	r, d, err := a.subTarget(g, pos[0])
	if err != nil {
		return err
	}
	rules, open, err := r.resolveSources(sf)
	if err != nil {
		return err
	}
	if (len(rules) == 0 || open) && !*allowOpen {
		return refusedf("refusing to leave cluster %q reachable from any address (an empty list, or 0.0.0.0/0). Give at least one trusted source, or pass --allow-open and type the cluster name. Nothing was changed", d.Name)
	}
	cur, err := r.Cloud.GetDatabaseFirewall(r.Ctx, d.ID)
	if err != nil {
		return err
	}
	names := r.dropletNames()
	have, want := map[string]godo.DatabaseFirewallRule{}, map[string]*godo.DatabaseFirewallRule{}
	for _, c := range cur {
		have[sourceKey(c)] = c
	}
	for _, w := range rules {
		want[sourceKey(*w)] = w
	}
	var added, removed, kept []string
	for k, w := range want {
		if _, ok := have[k]; ok {
			kept = append(kept, describeSource(w.Type, w.Value, names))
		} else {
			added = append(added, describeSource(w.Type, w.Value, names))
		}
	}
	for k, c := range have {
		if _, ok := want[k]; !ok {
			removed = append(removed, describeSource(c.Type, c.Value, names))
		}
	}
	sort.Strings(added)
	sort.Strings(removed)
	sort.Strings(kept)
	if len(added) == 0 && len(removed) == 0 {
		if g.JSON {
			srcs, err := r.sourceViews(d.ID)
			if err != nil {
				return err
			}
			return r.printJSON(srcs)
		}
		fmt.Fprintf(a.Stdout, "Cluster %q already has exactly these trusted sources. Nothing was changed.\n", d.Name)
		return nil
	}
	p := Plan{Action: "db.firewall.set", Summary: fmt.Sprintf("replace the trusted sources of cluster %q (the new list replaces the old one)", d.Name)}
	for _, s := range added {
		p.Steps = append(p.Steps, "add:    "+s)
	}
	for _, s := range removed {
		p.Steps = append(p.Steps, "remove: "+s)
	}
	for _, s := range kept {
		p.Steps = append(p.Steps, "keep:   "+s)
	}
	if len(removed) > 0 {
		p.Warnings = append(p.Warnings, "removing a source cuts off whatever connects from it")
	}
	if !isManagedDB(d) {
		p.Warnings = append(p.Warnings, "this cluster was not created by docli")
	}
	approve := approveOpts{}
	if open || len(rules) == 0 {
		p.Warnings = append(p.Warnings, "OPEN: no trusted sources (or 0.0.0.0/0), so any address that has the credentials can connect")
		approve = approveOpts{TypeName: d.Name, ConfirmName: *confirm}
	}
	ok, err := r.Approve(p, approve)
	if !ok || err != nil {
		return err
	}
	if err := r.Cloud.SetDatabaseFirewall(r.Ctx, d.ID, rules); err != nil {
		return err
	}
	r.updateManifest(*mo, false, change{Section: "databases", ID: d.ID})
	srcs, err := r.sourceViews(d.ID)
	if err != nil {
		return err
	}
	if g.JSON {
		return r.printJSON(srcs)
	}
	fmt.Fprintf(a.Stdout, "Cluster %q now has %d trusted source(s).\n", d.Name, len(srcs))
	return nil
}
