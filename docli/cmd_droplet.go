package main

import (
	"context"
	"fmt"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/digitalocean/godo"
)

func (a *App) cmdDroplet(args []string) error {
	if len(args) == 0 {
		return usagef("usage: docli droplet list|get|create|delete|power-off|power-on|reboot (try: docli help droplet)")
	}
	switch args[0] {
	case "list":
		return a.dropletList(args[1:])
	case "get":
		return a.dropletGet(args[1:])
	case "create":
		return a.dropletCreate(args[1:])
	case "delete":
		return a.dropletDelete(args[1:])
	case "power-off", "power-on", "reboot":
		return a.dropletPower(args[0], args[1:])
	}
	return usagef("unknown droplet command %q", args[0])
}

// resolveDroplet finds one droplet by id or name.
func (r *Run) resolveDroplet(ref string) (*godo.Droplet, error) {
	if id, err := strconv.Atoi(ref); err == nil {
		d, err := r.Cloud.GetDroplet(r.Ctx, id)
		if err != nil {
			if isNotFound(err) {
				return nil, fmt.Errorf("no droplet with id %d", id)
			}
			return nil, err
		}
		return d, nil
	}
	all, err := r.Cloud.ListDroplets(r.Ctx, "")
	if err != nil {
		return nil, err
	}
	var hits []godo.Droplet
	for _, d := range all {
		if d.Name == ref {
			hits = append(hits, d)
		}
	}
	switch len(hits) {
	case 0:
		return nil, fmt.Errorf("no droplet named %q. List them with: docli droplet list", ref)
	case 1:
		return &hits[0], nil
	}
	return nil, fmt.Errorf("%d droplets are named %q; use the id instead", len(hits), ref)
}

func (r *Run) resolveDroplets(refs []string) ([]int, []string, error) {
	if len(refs) == 0 {
		return nil, nil, nil
	}
	var ids []int
	var names []string
	for _, ref := range refs {
		d, err := r.resolveDroplet(ref)
		if err != nil {
			return nil, nil, err
		}
		ids = append(ids, d.ID)
		names = append(names, d.Name)
	}
	return ids, names, nil
}

func (a *App) dropletList(args []string) error {
	fs, g := a.newFlags("droplet list")
	tag := fs.String("tag", "", "")
	vpcRef := fs.String("vpc", "", "")
	pos, err := a.parseFlags(fs, args, "droplet list")
	if err != nil {
		return err
	}
	if len(pos) > 0 {
		return usagef("usage: docli droplet list [--tag TAG] [--vpc NAME|ID] [--region R]")
	}
	r, err := a.start(*g)
	if err != nil {
		return err
	}
	vpcID := ""
	if *vpcRef != "" {
		vpcs, err := r.Cloud.ListVPCs(r.Ctx)
		if err != nil {
			return err
		}
		v, err := findVPC(vpcs, *vpcRef)
		if err != nil {
			return err
		}
		vpcID = v.ID
	}
	ds, err := r.Cloud.ListDroplets(r.Ctx, *tag)
	if err != nil {
		return err
	}
	var shown []godo.Droplet
	for _, d := range ds {
		if vpcID != "" && d.VPCUUID != vpcID {
			continue
		}
		if r.G.Region != "" && (d.Region == nil || d.Region.Slug != r.G.Region) {
			continue
		}
		shown = append(shown, d)
	}
	if g.JSON {
		out := []dropletJSON{}
		for _, d := range shown {
			out = append(out, dropletView(d))
		}
		return r.printJSON(out)
	}
	var rows [][]string
	for _, d := range shown {
		v := dropletView(d)
		rows = append(rows, []string{itoa(d.ID), d.Name, d.Status, v.Region, v.SizeSlug, orDash(v.PublicIPv4), orDash(v.PrivateIPv4), yesNo(v.Managed)})
	}
	printTable(a.Stdout, []string{"ID", "NAME", "STATUS", "REGION", "SIZE", "PUBLIC IP", "PRIVATE IP", "MANAGED"}, rows)
	return nil
}

func (a *App) dropletGet(args []string) error {
	fs, g := a.newFlags("droplet get")
	pos, err := a.parseFlags(fs, args, "droplet get")
	if err != nil {
		return err
	}
	if len(pos) != 1 {
		return usagef("usage: docli droplet get NAME|ID")
	}
	r, err := a.start(*g)
	if err != nil {
		return err
	}
	d, err := r.resolveDroplet(pos[0])
	if err != nil {
		return err
	}
	v := dropletView(*d)
	if g.JSON {
		return r.printJSON(v)
	}
	fmt.Fprintf(a.Stdout, "ID:           %d\nName:         %s\nStatus:       %s\nManaged:      %s\nRegion:       %s\nSize:         %s (%d vCPU, %d MB, %d GB disk, %s/month)\nImage:        %s\nPublic IPv4:  %s\nPrivate IPv4: %s\nPublic IPv6:  %s\nVPC:          %s\nTags:         %s\nCreated:      %s\n",
		v.ID, v.Name, v.Status, yesNo(v.Managed), v.Region, v.SizeSlug, v.VCPUs, v.MemoryMB, v.DiskGB, money(v.MonthlyCost),
		orDash(firstNonEmpty(v.Image.Slug, v.Image.Name)), orDash(v.PublicIPv4), orDash(v.PrivateIPv4), orDash(v.PublicIPv6), orDash(v.VPCID), orDash(strings.Join(v.Tags, ",")), orDash(v.CreatedAt))
	return nil
}

func firstNonEmpty(s ...string) string {
	for _, x := range s {
		if x != "" {
			return x
		}
	}
	return ""
}

var stackRE = regexp.MustCompile(`^[A-Za-z0-9_-]{1,100}$`)

const maxUserData = 64 * 1024

func (a *App) dropletCreate(args []string) error {
	fs, g := a.newFlags("droplet create")
	name := fs.String("name", "", "")
	size := fs.String("size", "", "")
	image := fs.String("image", "", "")
	vpcRef := fs.String("vpc", "", "")
	stack := fs.String("stack", "", "")
	udFile := fs.String("user-data-file", "", "")
	backups := fs.Bool("backups", false, "")
	monitoring := fs.Bool("monitoring", false, "")
	ipv6 := fs.Bool("ipv6", false, "")
	wait := fs.Bool("wait", false, "")
	waitTimeout := fs.Duration("wait-timeout", 5*time.Minute, "")
	projRef := fs.String("project", "", "")
	var keyRefs, userTags stringList
	fs.Var(&keyRefs, "ssh-key", "")
	fs.Var(&userTags, "tag", "")
	mo := addManifestFlags(fs)
	pos, err := a.parseFlags(fs, args, "droplet create")
	if err != nil {
		return err
	}
	if len(pos) > 0 || *name == "" || *size == "" || *image == "" {
		return usagef("usage: docli droplet create --name NAME --region REGION --size SIZE --image IMAGE [--vpc V] [--ssh-key K] [--tag T] [--user-data-file F] [--backups] [--monitoring] [--ipv6] [--project P] [--wait]")
	}
	if err := validateHostname(*name); err != nil {
		return usagef("%v", err)
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
	userData := ""
	if *udFile != "" {
		b, err := os.ReadFile(*udFile)
		if err != nil {
			return fmt.Errorf("cannot read %s: %v", *udFile, err)
		}
		if len(b) > maxUserData {
			return fmt.Errorf("%s is %d bytes; DigitalOcean accepts at most %d bytes of user data", *udFile, len(b), maxUserData)
		}
		userData = string(b)
	}
	r, err := a.start(*g)
	if err != nil {
		return err
	}
	region := r.G.Region
	if region == "" {
		return usagef("a region is required: --region nyc3 (or set DOCLI_REGION)")
	}
	vpcName := a.envOr(*vpcRef, "DOCLI_VPC")
	if len(keyRefs) == 0 {
		if env := a.Getenv("DOCLI_SSH_KEY"); env != "" {
			keyRefs = stringList(strings.Split(env, ","))
		}
	}

	regions, err := r.Cloud.ListRegions(r.Ctx)
	if err != nil {
		return err
	}
	regionOK := false
	for _, x := range regions {
		if x.Slug == region {
			regionOK = true
			if !x.Available {
				return fmt.Errorf("region %s is not available for new droplets right now. See: docli regions", region)
			}
		}
	}
	if !regionOK {
		return fmt.Errorf("unknown region %q. See: docli regions", region)
	}
	sizes, err := r.Cloud.ListSizes(r.Ctx)
	if err != nil {
		return err
	}
	var sz *godo.Size
	for i := range sizes {
		if sizes[i].Slug == *size {
			sz = &sizes[i]
		}
	}
	if sz == nil {
		return fmt.Errorf("unknown size %q. See: docli sizes", *size)
	}
	if !sz.Available || !contains(sz.Regions, region) {
		return fmt.Errorf("size %s is not available in %s. See: docli sizes --region %s", *size, region, region)
	}

	req := &godo.DropletCreateRequest{Name: *name, Region: region, Size: *size, Backups: *backups, IPv6: *ipv6, Monitoring: *monitoring,
		UserData: userData, Tags: tags, SSHKeys: []godo.DropletCreateSSHKey{}}
	if id, err := strconv.Atoi(*image); err == nil {
		req.Image = godo.DropletCreateImage{ID: id}
	} else {
		req.Image = godo.DropletCreateImage{Slug: *image}
	}
	vpcDesc := "the default VPC for " + region
	if vpcName != "" {
		vpcs, err := r.Cloud.ListVPCs(r.Ctx)
		if err != nil {
			return err
		}
		v, err := findVPC(vpcs, vpcName)
		if err != nil {
			return err
		}
		if v.RegionSlug != region {
			return fmt.Errorf("VPC %q is in %s but the droplet is for %s", v.Name, v.RegionSlug, region)
		}
		req.VPCUUID = v.ID
		vpcDesc = fmt.Sprintf("VPC %s (%s)", v.Name, v.IPRange)
	}
	var keyNames []string
	if len(keyRefs) > 0 {
		keys, err := r.Cloud.ListKeys(r.Ctx)
		if err != nil {
			return err
		}
		for _, ref := range keyRefs {
			k, err := findKey(keys, ref)
			if err != nil {
				return err
			}
			req.SSHKeys = append(req.SSHKeys, godo.DropletCreateSSHKey{ID: k.ID})
			keyNames = append(keyNames, k.Name)
		}
	}

	proj, err := r.lookupProject(*projRef)
	if err != nil {
		return err
	}
	cost := estimateCost(*sz, *backups)
	p := Plan{Action: "droplet.create", Summary: fmt.Sprintf("create droplet %q in %s", *name, region)}
	p.Steps = []string{
		fmt.Sprintf("size: %s (%d vCPU, %d MB memory, %d GB disk): %s/month, %s/hour", sz.Slug, sz.Vcpus, sz.Memory, sz.Disk, money(cost.Monthly), fmt.Sprintf("$%.5f", cost.Hourly)),
		"image: " + *image,
		"network: " + vpcDesc,
		"ssh keys: " + orDash(strings.Join(keyNames, ", ")),
		"tags that will be applied: " + strings.Join(tags, ", ") + " (created if missing)",
		fmt.Sprintf("extras: backups=%s monitoring=%s ipv6=%s", yesNo(*backups), yesNo(*monitoring), yesNo(*ipv6)),
	}
	if userData != "" {
		p.Steps = append(p.Steps, fmt.Sprintf("user data: %d bytes from %s (contents not shown)", len(userData), *udFile))
	}
	if *backups {
		p.Steps = append(p.Steps, fmt.Sprintf("backups add about %s/month (20%% of the size price; an estimate, the API does not report it)", money(cost.BackupsMonthly)))
	}
	total := cost.Total()
	p.MonthlyUSD = &total
	if s := projectStep(proj); s != "" {
		p.Steps = append(p.Steps, s)
	}
	if len(keyNames) == 0 {
		p.Warnings = append(p.Warnings, "no SSH key: DigitalOcean will email a root password instead")
	}
	ok, err := r.Approve(p, approveOpts{})
	if !ok || err != nil {
		return err
	}
	for _, t := range tags {
		if err := r.Cloud.EnsureTag(r.Ctx, t); err != nil {
			return fmt.Errorf("could not create the tag %q, so no droplet was created: %v", t, err)
		}
	}
	d, err := r.Cloud.CreateDroplet(r.Ctx, req)
	if err != nil {
		if statusOf(err) == 0 {
			return fmt.Errorf("the create request failed and it is not known whether the droplet was made (%v). docli does not retry a create. Check with: docli droplet list --tag %s", err, managedTag)
		}
		return err
	}
	created := d.ID
	r.infof("created droplet %d (%s), status %s", d.ID, d.Name, d.Status)
	if *wait {
		d, err = r.waitForStatus(created, "active", *waitTimeout)
		if err != nil {
			r.updateManifest(*mo, false, change{Section: "droplets", ID: itoa(created)}) // it exists even though waiting failed
			return err
		}
	}
	r.updateManifest(*mo, false, change{Section: "droplets", ID: itoa(d.ID)})
	if err := r.putInProject(proj, d.URN(), "droplet", d.Name); err != nil {
		return err
	}
	if g.JSON {
		return r.printJSON(dropletView(*d))
	}
	v := dropletView(*d)
	fmt.Fprintf(a.Stdout, "Created droplet %q (id %d), status %s, public IP %s, private IP %s\n", d.Name, d.ID, d.Status, orDash(v.PublicIPv4), orDash(v.PrivateIPv4))
	return nil
}

// waitForStatus polls until the droplet reaches status, printing progress to stderr.
func (r *Run) waitForStatus(id int, status string, timeout time.Duration) (*godo.Droplet, error) {
	const every = 5 * time.Second
	var elapsed time.Duration
	ctx, cancel := context.WithCancel(r.Ctx)
	defer cancel()
	for {
		d, err := r.Cloud.GetDroplet(ctx, id)
		if err != nil {
			return nil, fmt.Errorf("lost track of droplet %d while waiting: %v. It still exists; check with: docli droplet get %d", id, err, id)
		}
		if d.Status == status {
			r.infof("droplet %d is %s", id, status)
			return d, nil
		}
		if elapsed >= timeout {
			return d, fmt.Errorf("droplet %d is still %q after %s. It still exists and is not deleted; check with: docli droplet get %d", id, d.Status, timeout, id)
		}
		r.infof("waiting for droplet %d to be %s: now %s (%ds elapsed)", id, status, d.Status, int(elapsed.Seconds()))
		if err := r.Sleep(ctx, every); err != nil {
			return d, err
		}
		elapsed += every
	}
}

func (a *App) dropletDelete(args []string) error {
	fs, g := a.newFlags("droplet delete")
	force := fs.Bool("force-unmanaged", false, "")
	confirm := fs.String("confirm-name", "", "")
	mo := addManifestFlags(fs)
	pos, err := a.parseFlags(fs, args, "droplet delete")
	if err != nil {
		return err
	}
	if len(pos) != 1 {
		return usagef("usage: docli droplet delete NAME|ID [--yes --confirm-name NAME]")
	}
	r, err := a.start(*g)
	if err != nil {
		return err
	}
	d, err := r.resolveDroplet(pos[0])
	if err != nil {
		return err
	}
	spec := deleteSpec{Kind: "droplet", Name: d.Name, Managed: hasTag(d.Tags, managedTag), Rule: "the tag " + managedTag, Always: true,
		Steps: []string{fmt.Sprintf("permanently destroy droplet %q (id %d, status %s); its disk and data cannot be recovered", d.Name, d.ID, d.Status)}}
	if n := len(d.VolumeIDs); n > 0 {
		spec.Warns = append(spec.Warns, fmt.Sprintf("%d volume(s) are attached; they are kept but detached", n))
	}
	ok, err := r.ApproveDelete(spec, *force, *confirm)
	if !ok || err != nil {
		return err
	}
	if err := r.Cloud.DeleteDroplet(r.Ctx, d.ID); err != nil {
		return err
	}
	r.updateManifest(*mo, true, change{Section: "droplets", ID: itoa(d.ID)})
	return r.deleted("droplet", itoa(d.ID), d.Name)
}

func (a *App) dropletPower(verb string, args []string) error {
	fs, g := a.newFlags("droplet " + verb)
	wait := fs.Bool("wait", false, "")
	waitTimeout := fs.Duration("wait-timeout", 5*time.Minute, "")
	pos, err := a.parseFlags(fs, args, "droplet "+verb)
	if err != nil {
		return err
	}
	if len(pos) != 1 {
		return usagef("usage: docli droplet %s NAME|ID [--wait]", verb)
	}
	r, err := a.start(*g)
	if err != nil {
		return err
	}
	d, err := r.resolveDroplet(pos[0])
	if err != nil {
		return err
	}
	action := strings.ReplaceAll(verb, "-", "_")
	effects := map[string]string{
		"power-off": "turn off droplet %q (id %d) like pulling the plug; it keeps billing while off",
		"power-on":  "turn on droplet %q (id %d)",
		"reboot":    "reboot droplet %q (id %d); connections drop for a short while",
	}
	p := Plan{Action: "droplet." + action, Summary: fmt.Sprintf(effects[verb], d.Name, d.ID), Steps: []string{"current status: " + d.Status}}
	if !hasTag(d.Tags, managedTag) {
		p.Warnings = append(p.Warnings, "this droplet was not created by docli")
	}
	ok, err := r.Approve(p, approveOpts{})
	if !ok || err != nil {
		return err
	}
	if err := r.Cloud.DropletAction(r.Ctx, d.ID, action); err != nil {
		return err
	}
	target := map[string]string{"power-off": "off", "power-on": "active"}[verb]
	if *wait && target != "" {
		if d, err = r.waitForStatus(d.ID, target, *waitTimeout); err != nil {
			return err
		}
	} else if d2, err := r.Cloud.GetDroplet(r.Ctx, d.ID); err == nil {
		d = d2
	}
	if g.JSON {
		return r.printJSON(dropletView(*d))
	}
	fmt.Fprintf(a.Stdout, "Requested %s for droplet %q (id %d); status now %s\n", verb, d.Name, d.ID, d.Status)
	return nil
}
