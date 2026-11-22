package main

import (
	"fmt"
	"sort"
	"strings"

	"github.com/digitalocean/godo"
)

// ---- domains and DNS records ----
//
// DigitalOcean cannot tag a domain or a record, so what docli created is whatever the manifest says (see
// cmd_domain.go). A record in the file that already exists but is not in the manifest is left as it is.

func normData(typ, data string) string {
	switch typ {
	case "CNAME", "MX", "NS":
		return strings.TrimSuffix(strings.ToLower(data), ".")
	}
	return data
}

type recWant struct {
	rec    *StackRecord
	spec   recordSpec
	known  bool // the address is known now (always true unless it comes from a droplet that does not exist yet)
	liveID int  // the live record it matched, 0 for none
	exact  bool
}

func (pl *planner) dropletAddress(name string) (string, bool) {
	idx, _, _ := pl.findDropletLive(name)
	if idx < 0 {
		return "", false
	}
	pub4, _, _ := dropletIPs(pl.snap.droplets[idx])
	return pub4, pub4 != ""
}

func (pl *planner) planDomains() error {
	for di := range pl.s.Domains {
		d := pl.s.Domains[di]
		live := pl.snap.domain(d.Name)
		own := pl.snap.own[d.Name]
		var liveRecs []godo.DomainRecord
		switch {
		case live == nil:
			pl.item("domain", d.Name, actCreate, "", "creates the DNS zone only: it does not register the domain and has no charge of its own",
				"for DigitalOcean to answer for it, its nameservers at your registrar must be ns1/ns2/ns3.digitalocean.com")
			pl.willMake["domain:"+d.Name] = true
			name := d.Name
			pl.op("domain", d.Name, fmt.Sprintf("create domain %q", d.Name), func() error { return pl.x.createDomain(name) })
		default:
			if own != nil && own.Created {
				pl.item("domain", d.Name, actUnchanged, d.Name, "exists; docli created it")
			} else {
				pl.item("domain", d.Name, actUnmanaged, d.Name, "exists but the manifest does not say docli created it: the zone is left as it is; records docli adds are tracked",
					"to manage it, run: docli adopt domain "+d.Name)
			}
			var err error
			if liveRecs, err = pl.snap.recordsOf(d.Name); err != nil {
				return fmt.Errorf("cannot list the records of %s: %v", d.Name, err)
			}
		}

		wants := make([]*recWant, len(d.Records))
		for i := range d.Records {
			w := &recWant{rec: &d.Records[i], spec: d.Records[i].spec, known: true}
			if w.rec.Droplet != "" {
				w.spec.Data, w.known = pl.dropletAddress(w.rec.Droplet)
			}
			wants[i] = w
		}
		matched := map[int]bool{}
		for _, w := range wants { // pass 1: the same record already exists
			if !w.known {
				continue
			}
			for _, lr := range liveRecs {
				if !matched[lr.ID] && lr.Type == w.spec.Type && lr.Name == w.spec.Name && normData(lr.Type, lr.Data) == normData(w.spec.Type, w.spec.Data) {
					matched[lr.ID], w.liveID, w.exact = true, lr.ID, true
					break
				}
			}
		}
		for _, w := range wants { // pass 2: one record of docli's with this type and name takes the new value
			if w.liveID != 0 {
				continue
			}
			var cands []godo.DomainRecord
			for _, lr := range liveRecs {
				if !matched[lr.ID] && own.hasRecord(lr.ID) && lr.Type == w.spec.Type && lr.Name == w.spec.Name {
					cands = append(cands, lr)
				}
			}
			same := 0
			for _, o := range wants {
				if o.liveID == 0 && o.spec.Type == w.spec.Type && o.spec.Name == w.spec.Name {
					same++
				}
			}
			if len(cands) == 1 && same == 1 {
				matched[cands[0].ID], w.liveID = true, cands[0].ID
			}
		}

		for _, w := range wants {
			pl.planRecord(d, w, liveRecs, own)
		}
		// records docli created in this domain that the file no longer lists
		if own != nil && live != nil {
			for _, lr := range liveRecs {
				if own.hasRecord(lr.ID) && !matched[lr.ID] {
					pl.orphanRecord(d.Name, lr)
				}
			}
		}
	}
	return nil
}

func recordDiffs(have godo.DomainRecord, want recordSpec) []string {
	var diffs []string
	if want.TTL != have.TTL {
		diffs = append(diffs, fmt.Sprintf("ttl: %d -> %d", have.TTL, want.TTL))
	}
	if want.Type == "MX" && want.Priority != have.Priority {
		diffs = append(diffs, fmt.Sprintf("priority: %d -> %d", have.Priority, want.Priority))
	}
	if want.Type == "CAA" && (want.Flags != have.Flags || want.Tag != have.Tag) {
		diffs = append(diffs, fmt.Sprintf("flags/tag: %d %s -> %d %s", have.Flags, have.Tag, want.Flags, want.Tag))
	}
	return diffs
}

func (pl *planner) planRecord(d StackDomain, w *recWant, liveRecs []godo.DomainRecord, own *ownedDomain) {
	name := w.spec.Type + " " + fqdn(d.Name, w.spec.Name)
	dataText := w.spec.Data
	if !w.known {
		dataText = "the public IPv4 address of droplet " + w.rec.Droplet + " (known once it exists)"
	}
	var have godo.DomainRecord
	for _, lr := range liveRecs {
		if lr.ID == w.liveID {
			have = lr
		}
	}
	domain, droplet, spec := d.Name, w.rec.Droplet, w.spec
	switch {
	case w.liveID != 0 && w.exact:
		diffs := recordDiffs(have, w.spec)
		switch {
		case len(diffs) == 0 && own.hasRecord(have.ID):
			pl.item("dns_record", name, actUnchanged, itoa(have.ID), describeRecord(w.spec))
		case len(diffs) == 0:
			pl.item("dns_record", name, actUnchanged, itoa(have.ID), describeRecord(w.spec), "exists but docli did not create it; adopt it to manage it: docli adopt record "+d.Name+" "+itoa(have.ID))
		case own.hasRecord(have.ID):
			pl.item("dns_record", name, actChange, itoa(have.ID), diffs...)
			id := have.ID
			pl.op("dns_record", name, fmt.Sprintf("update %s record %s", w.spec.Type, fqdn(d.Name, w.spec.Name)), func() error { return pl.x.editRecord(domain, id, spec, droplet) })
		default:
			pl.item("dns_record", name, actUnmanaged, itoa(have.ID), append(diffs, "docli did not create this record, so it is not changed")...)
		}
	case w.liveID != 0: // an owned record takes a new value
		det := []string{fmt.Sprintf("data: %s -> %s", have.Data, dataText)}
		det = append(det, recordDiffs(have, w.spec)...)
		pl.item("dns_record", name, actChange, itoa(have.ID), det...)
		id := have.ID
		pl.op("dns_record", name, fmt.Sprintf("update %s record %s", w.spec.Type, fqdn(d.Name, w.spec.Name)), func() error { return pl.x.editRecord(domain, id, spec, droplet) })
	default:
		if w.known {
			if err := recordConflict(&w.spec, liveRecs, 0); err != nil {
				pl.errf(w.rec.Pos.Line, "%s: %v", name, err)
				return
			}
		}
		det := []string{describeRecord(recordSpec{Type: w.spec.Type, Name: w.spec.Name, Data: dataText, TTL: w.spec.TTL, Priority: w.spec.Priority, Flags: w.spec.Flags, Tag: w.spec.Tag})}
		pl.item("dns_record", name, actCreate, "", det...)
		pl.op("dns_record", name, fmt.Sprintf("create %s record %s", w.spec.Type, fqdn(d.Name, w.spec.Name)), func() error { return pl.x.createRecord(domain, spec, droplet) })
	}
}

func (pl *planner) orphanRecord(domain string, lr godo.DomainRecord) {
	name := lr.Type + " " + fqdn(domain, lr.Name)
	it := pl.item("dns_record", name, actOrphaned, itoa(lr.ID), "docli created it (the manifest says so) and the file does not list it")
	id := lr.ID
	pl.pruneable = append(pl.pruneable, pruneCand{item: it, section: "domains", id: domain, label: fmt.Sprintf("delete %s record %s", lr.Type, fqdn(domain, lr.Name)),
		do: func() error { return pl.x.deleteRecord(domain, id) }})
}

// ---- things docli created that the file does not list ----

func otherStackTag(tags []string, mine string) bool {
	for _, t := range tags {
		if strings.HasPrefix(t, "docli:stack:") && t != mine {
			return true
		}
	}
	return false
}

func (pl *planner) planOrphans() {
	s := pl.s
	keep := func(final string, list []string) bool { return contains(list, final) }
	var keys, vpcs, fws, drops, dbs []string
	for _, k := range s.SSHKeys {
		keys = append(keys, ensureNamePrefix(k.Name))
	}
	for _, v := range s.VPCs {
		vpcs = append(vpcs, ensureNamePrefix(v.Name))
	}
	for _, f := range s.Firewalls {
		fws = append(fws, ensureNamePrefix(f.Name))
	}
	for _, d := range s.Droplets {
		drops = append(drops, d.Name)
	}
	for _, d := range s.Databases {
		dbs = append(dbs, d.Name)
	}
	const note = "docli created it and the file does not list it"
	for _, k := range pl.snap.keys {
		if hasManagedPrefix(k.Name) && !keep(k.Name, keys) {
			it := pl.item("ssh_key", strings.TrimPrefix(k.Name, namePrefix), actOrphaned, itoa(k.ID), note)
			id := k.ID
			pl.pruneable = append(pl.pruneable, pruneCand{item: it, section: "ssh_keys", id: itoa(k.ID), label: fmt.Sprintf("delete SSH key %q", k.Name), do: func() error { return pl.x.deleteKey(id) }})
		}
	}
	for _, v := range pl.snap.vpcs {
		if hasManagedPrefix(v.Name) && !keep(v.Name, vpcs) {
			it := pl.item("vpc", strings.TrimPrefix(v.Name, namePrefix), actOrphaned, v.ID, note)
			c := pruneCand{item: it, section: "vpcs", id: v.ID, label: fmt.Sprintf("delete VPC %q", v.Name)}
			id := v.ID
			c.do = func() error { return pl.x.deleteVPC(id) }
			pl.pruneable = append(pl.pruneable, c)
		}
	}
	for _, f := range pl.snap.firewalls {
		if hasManagedPrefix(f.Name) && !keep(f.Name, fws) {
			it := pl.item("firewall", strings.TrimPrefix(f.Name, namePrefix), actOrphaned, f.ID, note)
			id := f.ID
			c := pruneCand{item: it, section: "firewalls", id: f.ID, label: fmt.Sprintf("delete firewall %q", f.Name), do: func() error { return pl.x.deleteFirewall(id) }}
			if n := len(f.DropletIDs) + len(f.Tags); n > 0 {
				it.Details = append(it.Details, fmt.Sprintf("it is attached to %d droplet(s) and %d tag(s); they would lose this protection", len(f.DropletIDs), len(f.Tags)))
			}
			pl.pruneable = append(pl.pruneable, c)
		}
	}
	for _, d := range pl.snap.droplets {
		if hasTag(d.Tags, managedTag) && !keep(d.Name, drops) && !otherStackTag(d.Tags, pl.stackTag()) {
			it := pl.item("droplet", d.Name, actOrphaned, itoa(d.ID), note)
			id := d.ID
			pl.pruneable = append(pl.pruneable, pruneCand{item: it, section: "droplets", id: itoa(d.ID), typed: d.Name, data: true,
				label: fmt.Sprintf("delete droplet %q", d.Name), do: func() error { return pl.x.deleteDroplet(id) }})
		}
	}
	for _, d := range pl.snap.databases {
		if hasTag(d.Tags, managedTag) && !keep(d.Name, dbs) && !otherStackTag(d.Tags, pl.stackTag()) {
			it := pl.item("database", d.Name, actOrphaned, d.ID, note)
			id := d.ID
			pl.pruneable = append(pl.pruneable, pruneCand{item: it, section: "databases", id: d.ID, typed: d.Name, data: true,
				label: fmt.Sprintf("delete database cluster %q", d.Name), do: func() error { return pl.x.deleteDatabase(id) }})
		}
	}
	pl.planOrphansM4()
	// domains the manifest says docli created, that the file does not list
	var owned []string
	for name := range pl.snap.own {
		owned = append(owned, name)
	}
	sort.Strings(owned)
	for _, name := range owned {
		if nameIn(s.Domains, func(x StackDomain) string { return x.Name }, name) || pl.snap.domain(name) == nil {
			continue
		}
		o := pl.snap.own[name]
		if o.Created {
			it := pl.item("domain", name, actOrphaned, name, note+" (deleting a domain removes every record in it)")
			dn := name
			pl.pruneable = append(pl.pruneable, pruneCand{item: it, section: "domains", id: name, typed: name, label: fmt.Sprintf("delete domain %q and every record in it", name),
				do: func() error { return pl.x.deleteDomain(dn) }})
			continue
		}
		recs, err := pl.snap.recordsOf(name)
		if err != nil {
			continue
		}
		for _, lr := range recs {
			if o.hasRecord(lr.ID) {
				pl.orphanRecord(name, lr)
			}
		}
	}
}

// pruneOrder is the order deletions are done in: the reverse of creation.
var pruneOrder = []string{"dns_record", "domain", "database", "load_balancer", "volume", "droplet", "firewall", "vpc", "ssh_key", "project"}

// addPrune turns the orphans docli may delete into deletions, when --prune was given. The rest stay orphaned.
func (pl *planner) addPrune() {
	if !pl.opt.Prune {
		return
	}
	for _, kind := range pruneOrder {
		for _, c := range pl.pruneable {
			if c.item.Kind != kind {
				continue
			}
			if reason := pl.pruneBlock(c); reason != "" {
				c.item.Details = append(c.item.Details, "not pruned: "+reason)
				continue
			}
			c.item.Action = actPrune
			switch c.item.Kind {
			case "droplet":
				pl.pruning["do:droplet:"+c.id] = true
			case "database":
				pl.pruning["do:dbaas:"+c.id] = true
			case "volume":
				pl.pruning["do:volume:"+c.id] = true
			case "load_balancer":
				pl.pruning["do:loadbalancer:"+c.id] = true
			}
			c.item.Details = append(c.item.Details, "will be deleted (--prune): "+c.label)
			op := pl.op(c.item.Kind, c.item.Name, c.label, c.do)
			op.Prune, op.TypedName = true, c.typed
		}
	}
}

// pruneBlock says why an orphan must not be deleted, or "" when it may be.
func (pl *planner) pruneBlock(c pruneCand) string {
	if c.section != "domains" { // DNS ownership is the manifest itself; the rest also needs this project's manifest
		if pl.man == nil || pl.man.find(c.section, c.id) == nil {
			return "this project's manifest does not list it, so it may belong to another project"
		}
	}
	if c.data && !pl.opt.PruneData {
		return "it may hold data; add --prune-data to allow deleting droplets and databases"
	}
	if reason := pl.pruneBlockM4(c); reason != "" {
		return reason
	}
	if c.item.Kind == "vpc" {
		members, err := pl.r.Cloud.ListVPCMembers(pl.r.Ctx, c.id)
		if err != nil {
			return "cannot list its members: " + err.Error()
		}
		left := 0
		for _, m := range members {
			if !pl.pruning[m.URN] { // a member that is itself being pruned first will be gone by then
				left++
			}
		}
		if left > 0 {
			return fmt.Sprintf("it still has %d member(s)", left)
		}
	}
	return ""
}

// notManaged lists what is in the account, is not docli's and is not in the file. It is only reported.
func (pl *planner) notManaged() []NotManagedItem {
	out := []NotManagedItem{}
	s := pl.s
	for _, k := range pl.snap.keys {
		if !hasManagedPrefix(k.Name) && !nameIn(s.SSHKeys, func(x StackKey) string { return x.Name }, k.Name) {
			out = append(out, NotManagedItem{"ssh_key", k.Name, itoa(k.ID)})
		}
	}
	for _, v := range pl.snap.vpcs {
		if !hasManagedPrefix(v.Name) && !nameIn(s.VPCs, func(x StackVPC) string { return x.Name }, v.Name) {
			out = append(out, NotManagedItem{"vpc", v.Name, v.ID})
		}
	}
	for _, f := range pl.snap.firewalls {
		if !hasManagedPrefix(f.Name) && !nameIn(s.Firewalls, func(x StackFirewall) string { return x.Name }, f.Name) {
			out = append(out, NotManagedItem{"firewall", f.Name, f.ID})
		}
	}
	for _, d := range pl.snap.droplets {
		if !hasTag(d.Tags, managedTag) && !nameIn(s.Droplets, func(x StackDroplet) string { return x.Name }, d.Name) {
			out = append(out, NotManagedItem{"droplet", d.Name, itoa(d.ID)})
		}
	}
	for _, d := range pl.snap.databases {
		if !hasTag(d.Tags, managedTag) && !nameIn(s.Databases, func(x StackDatabase) string { return x.Name }, d.Name) {
			out = append(out, NotManagedItem{"database", d.Name, d.ID})
		}
	}
	out = append(out, pl.notManagedM4()...)
	for _, d := range pl.snap.domains {
		if o := pl.snap.own[d.Name]; (o == nil || !o.Created) && !nameIn(s.Domains, func(x StackDomain) string { return x.Name }, d.Name) {
			out = append(out, NotManagedItem{"domain", d.Name, d.Name})
		}
	}
	return out
}
