package main

import (
	"fmt"

	"github.com/digitalocean/godo"
)

// docli adopt: take an existing resource under docli's management without changing it.
//
// Droplets and database clusters can carry tags, so adopting one adds the tag docli:managed (and nothing else:
// no rename, no reboot, no tag removed). Domains and DNS records cannot be tagged, so adopting one only records
// in the manifest that docli may manage it, and nothing at all changes on DigitalOcean. SSH keys, VPCs and
// firewalls are known to be docli's by a name prefix; adopting would mean renaming them, which docli will not do.

type adoptDoc struct {
	Adopted    bool   `json:"adopted"`
	Kind       string `json:"kind"`
	Name       string `json:"name"`
	ID         string `json:"id"`
	APIChanged bool   `json:"api_changed"` // true only when a tag was added on DigitalOcean
}

func (a *App) cmdAdopt(args []string) error {
	fs, g := a.newFlags("adopt")
	stack := fs.String("stack", "", "")
	mo := addManifestFlags(fs)
	pos, err := a.parseFlags(fs, args, "adopt")
	if err != nil {
		return err
	}
	if len(pos) == 0 {
		return usagef("usage: docli adopt droplet|database|volume NAME|ID | domain NAME | reserved-ip IP | record DOMAIN ID [--stack NAME] (try: docli help adopt)")
	}
	if *stack != "" && !stackRE.MatchString(*stack) {
		return usagef("--stack %q must use letters, digits, dash and underscore only", *stack)
	}
	kind, rest := pos[0], pos[1:]
	switch kind {
	case "droplet", "database", "volume":
		if len(rest) != 1 {
			return usagef("usage: docli adopt %s NAME|ID [--stack NAME]", kind)
		}
	case "domain":
		if len(rest) != 1 {
			return usagef("usage: docli adopt domain NAME")
		}
	case "reserved-ip":
		if len(rest) != 1 {
			return usagef("usage: docli adopt reserved-ip IP")
		}
	case "record":
		if len(rest) != 2 {
			return usagef("usage: docli adopt record DOMAIN RECORD_ID")
		}
	case "vpc", "firewall", "ssh-key", "project", "lb", "load-balancer":
		return refusedf("docli does not adopt %ss. It knows its own by the name prefix %s (they cannot be tagged), so adopting would mean renaming the %s, and docli will not rename something it did not create. Create a new one with docli, or leave this one alone. Nothing was changed", kind, namePrefix, kind)
	default:
		return usagef("cannot adopt %q (droplet, database, volume, domain, reserved-ip, record)", kind)
	}
	if (kind == "domain" || kind == "record" || kind == "reserved-ip") && (mo.Skip || *stack != "") {
		return usagef("a %s is adopted only by recording it in the manifest, so --no-manifest and --stack do not apply", kind)
	}
	r, err := a.start(*g)
	if err != nil {
		return err
	}
	switch kind {
	case "droplet":
		d, err := r.resolveDroplet(rest[0])
		if err != nil {
			return err
		}
		return r.adoptTagged(*mo, "droplet", d.Name, itoa(d.ID), godo.DropletResourceType, d.Tags, *stack, "droplets", true)
	case "database":
		d, err := r.resolveDB(rest[0])
		if err != nil {
			return err
		}
		return r.adoptTagged(*mo, "database", d.Name, d.ID, godo.DatabaseResourceType, d.Tags, *stack, "databases", false)
	case "volume":
		vols, err := r.Cloud.ListVolumes(r.Ctx)
		if err != nil {
			return err
		}
		v, err := findVolume(vols, rest[0], r.G.Region)
		if err != nil {
			return err
		}
		return r.adoptTagged(*mo, "volume", v.Name, v.ID, godo.VolumeResourceType, v.Tags, *stack, "volumes", false)
	case "reserved-ip":
		return r.adoptReservedIP(*mo, rest[0])
	case "domain":
		return r.adoptDomain(*mo, rest[0])
	}
	return r.adoptRecord(*mo, rest[0], rest[1])
}

func (r *Run) adoptTagged(mo manifestOpts, kind, name, id string, rtype godo.ResourceType, have []string, stack, section string, firewalls bool) error {
	tags := []string{managedTag}
	if stack != "" {
		tags = append(tags, "docli:stack:"+stack)
	}
	var missing []string
	for _, t := range tags {
		if !hasTag(have, t) {
			missing = append(missing, t)
		}
	}
	p := Plan{Action: "adopt." + kind, Summary: fmt.Sprintf("adopt %s %q (id %s) as docli-managed", kind, name, id)}
	if len(missing) > 0 {
		p.Steps = append(p.Steps, "add the tag(s) "+joinStrings(missing)+" on DigitalOcean (created if missing)")
	} else {
		p.Steps = append(p.Steps, "it already carries the docli tag(s); nothing changes on DigitalOcean")
	}
	p.Steps = append(p.Steps, "nothing else about it changes: no rename, no restart, no other tag removed")
	if mo.Skip {
		p.Warnings = append(p.Warnings, "--no-manifest: it will be tagged but not recorded in the manifest")
	} else {
		p.Steps = append(p.Steps, "record it in the manifest")
	}
	p.Warnings = append(p.Warnings, fmt.Sprintf("once adopted, docli may delete this %s (with its name typed) and a stack file may change it", kind))
	ok, err := r.Approve(p, approveOpts{})
	if !ok || err != nil {
		return err
	}
	for _, t := range missing {
		if err := r.Cloud.EnsureTag(r.Ctx, t); err != nil {
			return fmt.Errorf("could not create the tag %q, so nothing was adopted: %v", t, err)
		}
	}
	for _, t := range missing {
		if err := r.Cloud.TagResource(r.Ctx, t, id, string(rtype)); err != nil {
			return fmt.Errorf("could not add the tag %q to %s %q: %v", t, kind, name, err)
		}
	}
	r.updateManifest(mo, firewalls, change{Section: section, ID: id})
	return r.adopted(kind, name, id, len(missing) > 0)
}

func (r *Run) adopted(kind, name, id string, apiChanged bool) error {
	if r.G.JSON {
		return r.printJSON(adoptDoc{true, kind, name, id, apiChanged})
	}
	fmt.Fprintf(r.Stdout, "Adopted %s %q (id %s).\n", kind, name, id)
	return nil
}

func (r *Run) adoptDomain(mo manifestOpts, ref string) error {
	ds, err := r.Cloud.ListDomains(r.Ctx)
	if err != nil {
		return err
	}
	d, err := findDomain(ds, ref)
	if err != nil {
		return err
	}
	own, err := r.loadOwnership(mo)
	if err != nil {
		return err
	}
	p := Plan{Action: "adopt.domain", Summary: fmt.Sprintf("adopt the domain %q as docli-managed", d.Name)}
	if o := own[d.Name]; o != nil && o.Created {
		p.Steps = append(p.Steps, "the manifest already says docli created this domain; nothing changes")
	} else {
		p.Steps = append(p.Steps, "record in the manifest that docli may manage this domain (a domain cannot be tagged)")
	}
	p.Steps = append(p.Steps, "nothing changes on DigitalOcean, and no record in the domain is adopted by this: adopt records one at a time with: docli adopt record")
	p.Warnings = append(p.Warnings, "once adopted, docli may delete this domain, and every record in it, if you type its name")
	ok, err := r.Approve(p, approveOpts{})
	if !ok || err != nil {
		return err
	}
	r.updateManifest(mo, false, change{Section: "domains", ID: d.Name, Adopt: true})
	return r.adopted("domain", d.Name, d.Name, false)
}

func (r *Run) adoptRecord(mo manifestOpts, domain, ref string) error {
	d, recs, err := r.domainAndRecords(domain)
	if err != nil {
		return err
	}
	rec, err := findRecord(recs, ref, d.Name)
	if err != nil {
		return err
	}
	own, err := r.loadOwnership(mo)
	if err != nil {
		return err
	}
	p := Plan{Action: "adopt.record", Summary: fmt.Sprintf("adopt the %s record %s (id %d) as docli-managed", rec.Type, fqdn(d.Name, rec.Name), rec.ID)}
	if own[d.Name].hasRecord(rec.ID) {
		p.Steps = append(p.Steps, "the manifest already lists this record as docli's; nothing changes")
	} else {
		p.Steps = append(p.Steps, "record in the manifest that docli may update and delete this record (a record cannot be tagged)")
	}
	p.Steps = append(p.Steps, "nothing changes on DigitalOcean: "+describeRecord(specOf(*rec)))
	ok, err := r.Approve(p, approveOpts{})
	if !ok || err != nil {
		return err
	}
	r.updateManifest(mo, false, change{Section: "domains", ID: d.Name, AddRecords: []int{rec.ID}})
	return r.adopted("record", fmt.Sprintf("%s %s", rec.Type, fqdn(d.Name, rec.Name)), itoa(rec.ID), false)
}

func (r *Run) adoptReservedIP(mo manifestOpts, ref string) error {
	ips, err := r.Cloud.ListReservedIPs(r.Ctx)
	if err != nil {
		return err
	}
	ip, err := findReservedIP(ips, ref)
	if err != nil {
		return err
	}
	owned, err := r.loadOwned(mo)
	if err != nil {
		return err
	}
	p := Plan{Action: "adopt.reserved-ip", Summary: fmt.Sprintf("adopt the reserved IP %s as docli-managed", ip.IP)}
	if owned.has(ip.IP) {
		p.Steps = append(p.Steps, "the manifest already says docli created this address; nothing changes")
	} else {
		p.Steps = append(p.Steps, "record in the manifest that docli may assign, unassign and release this address (a reserved IP cannot be tagged or named)")
	}
	p.Steps = append(p.Steps, "nothing changes on DigitalOcean")
	p.Warnings = append(p.Warnings, "once adopted, docli may move this address to another droplet or release it (with the address typed)")
	ok, err := r.Approve(p, approveOpts{})
	if !ok || err != nil {
		return err
	}
	r.updateManifest(mo, false, change{Section: "reserved_ips", ID: ip.IP, Adopt: true})
	return r.adopted("reserved-ip", ip.IP, ip.IP, false)
}

func joinStrings(s []string) string {
	out := ""
	for i, x := range s {
		if i > 0 {
			out += ", "
		}
		out += x
	}
	return out
}
