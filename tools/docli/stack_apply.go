package main

import (
	"fmt"
	"strings"
	"time"

	"github.com/digitalocean/godo"
)

// applyState carries what apply learns as it goes: the ids of the resources it finds or creates, by the name the
// stack file uses. Every step is one API write (or a wait), run once, in the fixed order the plan lists them.
// A create is never retried. The manifest is updated after each step that succeeds.
type applyState struct {
	r  *Run
	mo manifestOpts

	keyID  map[string]int
	vpcID  map[string]string
	fwID   map[string]string
	dropID map[string]int
	dbID   map[string]string
	dbSpec map[string]StackDatabase

	projectID map[string]string
	volID     map[string]string
	lbID      map[string]string

	waitDroplet_ time.Duration
	waitDB_      time.Duration
	nextSteps    []string
}

func newApplyState(r *Run, mo manifestOpts) *applyState {
	return &applyState{r: r, mo: mo, keyID: map[string]int{}, vpcID: map[string]string{}, fwID: map[string]string{},
		dropID: map[string]int{}, dbID: map[string]string{}, dbSpec: map[string]StackDatabase{},
		projectID: map[string]string{}, volID: map[string]string{}, lbID: map[string]string{},
		waitDroplet_: 5 * time.Minute, waitDB_: 20 * time.Minute}
}

// madeOrUnknown explains a failed create: when the request never got an answer, nobody knows whether it worked.
func madeOrUnknown(kind string, err error, check string) error {
	if statusOf(err) == 0 {
		return fmt.Errorf("the create request failed and it is not known whether the %s was made (%v). docli does not retry a create. Check with: %s", kind, err, check)
	}
	return err
}

func (x *applyState) ensureTags(tags []string, what string) error {
	for _, t := range tags {
		if err := x.r.Cloud.EnsureTag(x.r.Ctx, t); err != nil {
			return fmt.Errorf("could not create the tag %q, so no %s was created: %v", t, what, err)
		}
	}
	return nil
}

func (x *applyState) createKey(name, final, line string) error {
	k, err := x.r.Cloud.CreateKey(x.r.Ctx, final, line)
	if err != nil {
		return madeOrUnknown("SSH key", err, "docli ssh-key list")
	}
	x.keyID[name] = k.ID
	x.r.updateManifest(x.mo, false, change{Section: "ssh_keys", ID: itoa(k.ID)})
	return nil
}

func (x *applyState) createVPC(name, final, region, ipRange string) error {
	v, err := x.r.Cloud.CreateVPC(x.r.Ctx, &godo.VPCCreateRequest{Name: final, RegionSlug: region, IPRange: ipRange})
	if err != nil {
		return madeOrUnknown("VPC", err, "docli vpc list")
	}
	x.vpcID[name] = v.ID
	x.r.updateManifest(x.mo, false, change{Section: "vpcs", ID: v.ID})
	return nil
}

func (x *applyState) createFirewall(name, final string, rules []Rule) error {
	rr := rulesRequest(rules)
	f, err := x.r.Cloud.CreateFirewall(x.r.Ctx, &godo.FirewallRequest{Name: final, InboundRules: rr.InboundRules, OutboundRules: rr.OutboundRules,
		DropletIDs: []int{}, Tags: []string{}})
	if err != nil {
		return madeOrUnknown("firewall", err, "docli firewall list")
	}
	x.fwID[name] = f.ID
	x.r.updateManifest(x.mo, true, change{Section: "firewalls", ID: f.ID})
	return nil
}

func (x *applyState) changeFirewallRules(name, id string, add, remove []Rule) error {
	if len(add) > 0 {
		if err := x.r.Cloud.AddFirewallRules(x.r.Ctx, id, rulesRequest(add)); err != nil {
			return err
		}
	}
	if len(remove) > 0 {
		if err := x.r.Cloud.RemoveFirewallRules(x.r.Ctx, id, rulesRequest(remove)); err != nil {
			x.r.updateManifest(x.mo, true, change{Section: "firewalls", ID: id})
			return err
		}
	}
	x.r.updateManifest(x.mo, true, change{Section: "firewalls", ID: id})
	return nil
}

func (x *applyState) createDroplet(d StackDroplet, tags []string) error {
	if err := x.ensureTags(tags, "droplet"); err != nil {
		return err
	}
	req := &godo.DropletCreateRequest{Name: d.Name, Region: d.Region, Size: d.Size, Backups: d.Backups, IPv6: d.IPv6, Monitoring: d.Monitoring,
		UserData: d.userData, Tags: tags, SSHKeys: []godo.DropletCreateSSHKey{}}
	if isNumeric(d.Image) {
		id := 0
		fmt.Sscan(d.Image, &id)
		req.Image = godo.DropletCreateImage{ID: id}
	} else {
		req.Image = godo.DropletCreateImage{Slug: d.Image}
	}
	if d.VPC != "" {
		id, ok := x.vpcID[d.VPC]
		if !ok {
			return fmt.Errorf("the VPC %q does not exist yet, so droplet %q was not created", d.VPC, d.Name)
		}
		req.VPCUUID = id
	}
	for _, k := range d.SSHKeys {
		id, ok := x.keyID[k]
		if !ok {
			return fmt.Errorf("the SSH key %q does not exist yet, so droplet %q was not created", k, d.Name)
		}
		req.SSHKeys = append(req.SSHKeys, godo.DropletCreateSSHKey{ID: id})
	}
	got, err := x.r.Cloud.CreateDroplet(x.r.Ctx, req)
	if err != nil {
		return madeOrUnknown("droplet", err, "docli droplet list --tag "+managedTag)
	}
	x.dropID[d.Name] = got.ID
	x.r.updateManifest(x.mo, false, change{Section: "droplets", ID: itoa(got.ID)})
	return nil
}

func (x *applyState) waitDroplet(name string) error {
	id, ok := x.dropID[name]
	if !ok {
		return fmt.Errorf("droplet %q was not created, so there is nothing to wait for", name)
	}
	_, err := x.r.waitForStatus(id, "active", x.waitDroplet_)
	x.r.updateManifest(x.mo, false, change{Section: "droplets", ID: itoa(id)})
	return err
}

func (x *applyState) attachDroplet(droplet, fw string) error {
	did, ok := x.dropID[droplet]
	if !ok {
		return fmt.Errorf("droplet %q does not exist, so it was not attached to firewall %q", droplet, fw)
	}
	fid, ok := x.fwID[fw]
	if !ok {
		return fmt.Errorf("firewall %q does not exist, so droplet %q was not attached to it", fw, droplet)
	}
	if err := x.r.Cloud.AddFirewallDroplets(x.r.Ctx, fid, did); err != nil {
		return err
	}
	x.r.updateManifest(x.mo, true, change{Section: "firewalls", ID: fid})
	return nil
}

// sourceRules turns the stack's trusted sources into API rules. Droplets are looked up by the ids apply knows.
func (x *applyState) sourceRules(d StackDatabase) ([]*godo.DatabaseFirewallRule, error) {
	var rules []*godo.DatabaseFirewallRule
	for _, src := range desiredSources(&d) {
		typ, val, _ := strings.Cut(src, ":")
		if typ == "droplet" {
			id, ok := x.dropID[val]
			if !ok {
				return nil, fmt.Errorf("droplet %q does not exist, so database %q cannot trust it", val, d.Name)
			}
			val = itoa(id)
		}
		rules = append(rules, &godo.DatabaseFirewallRule{Type: typ, Value: val})
	}
	if len(rules) == 0 {
		return nil, fmt.Errorf("database %q would have no trusted sources; docli will not leave a database open", d.Name)
	}
	return rules, nil
}

func (x *applyState) createDatabase(d StackDatabase, tags []string) error {
	rules, err := x.sourceRules(d)
	if err != nil {
		return err
	}
	vpc, ok := x.vpcID[d.VPC]
	if !ok {
		return fmt.Errorf("the VPC %q does not exist yet, so database %q was not created", d.VPC, d.Name)
	}
	if err := x.ensureTags(tags, "cluster"); err != nil {
		return err
	}
	req := &godo.DatabaseCreateRequest{Name: d.Name, EngineSlug: dbEngine, Version: d.Version, SizeSlug: d.Size, Region: d.Region, NumNodes: d.Nodes,
		PrivateNetworkUUID: vpc, Tags: tags, Rules: []*godo.DatabaseCreateFirewallRule{}}
	for _, ru := range rules {
		req.Rules = append(req.Rules, &godo.DatabaseCreateFirewallRule{Type: ru.Type, Value: ru.Value})
	}
	got, err := x.r.Cloud.CreateDatabase(x.r.Ctx, req)
	if err != nil {
		return madeOrUnknown("cluster", err, "docli db list")
	}
	x.r.noteSecrets(got)
	x.dbID[d.Name] = got.ID
	x.r.updateManifest(x.mo, false, change{Section: "databases", ID: got.ID})
	return nil
}

func (x *applyState) waitDB(name string) error {
	id, ok := x.dbID[name]
	if !ok {
		return fmt.Errorf("database cluster %q was not created, so there is nothing to wait for", name)
	}
	d, err := x.r.waitForDB(id, "online", x.waitDB_)
	if d != nil {
		x.r.noteSecrets(d)
	}
	x.r.updateManifest(x.mo, false, change{Section: "databases", ID: id})
	return err
}

func (x *applyState) resizeDB(name, id, size string, nodes int) error {
	cur, err := x.r.Cloud.GetDatabase(x.r.Ctx, id)
	if err != nil {
		return err
	}
	x.r.noteSecrets(cur)
	if cur.Status != "online" {
		return fmt.Errorf("cluster %q is %q, not online, so it was not resized. Wait with: docli db wait %s", name, cur.Status, name)
	}
	if err := x.r.Cloud.ResizeDatabase(x.r.Ctx, id, &godo.DatabaseResizeRequest{SizeSlug: size, NumNodes: nodes}); err != nil {
		return err
	}
	_, err = x.r.waitForDB(id, "online", x.waitDB_)
	x.r.updateManifest(x.mo, false, change{Section: "databases", ID: id})
	return err
}

func (x *applyState) setTrusted(name string) error {
	id, ok := x.dbID[name]
	if !ok {
		return fmt.Errorf("database cluster %q does not exist", name)
	}
	rules, err := x.sourceRules(x.dbSpec[name])
	if err != nil {
		return err
	}
	if err := x.r.Cloud.SetDatabaseFirewall(x.r.Ctx, id, rules); err != nil {
		return err
	}
	x.r.updateManifest(x.mo, false, change{Section: "databases", ID: id})
	return nil
}

func (x *applyState) createDBUser(cluster, user string) error {
	id, ok := x.dbID[cluster]
	if !ok {
		return fmt.Errorf("database cluster %q does not exist", cluster)
	}
	u, err := x.r.Cloud.CreateDatabaseUser(x.r.Ctx, id, user)
	if err != nil {
		return madeOrUnknown("user", err, "docli db user list "+cluster)
	}
	x.r.rememberSecret(u.Password) // never shown
	return nil
}

func (x *applyState) createDBDatabase(cluster, name string) error {
	id, ok := x.dbID[cluster]
	if !ok {
		return fmt.Errorf("database cluster %q does not exist", cluster)
	}
	if _, err := x.r.Cloud.CreateDatabaseDB(x.r.Ctx, id, name); err != nil {
		return madeOrUnknown("database", err, "docli db database list "+cluster)
	}
	return nil
}

func (x *applyState) createDomain(name string) error {
	d, err := x.r.Cloud.CreateDomain(x.r.Ctx, name)
	if err != nil {
		return madeOrUnknown("domain", err, "docli domain list")
	}
	x.r.updateManifest(x.mo, false, change{Section: "domains", ID: d.Name, Adopt: true})
	return nil
}

// recordRequest fills in the address of a droplet when the record points at one.
func (x *applyState) recordRequest(spec recordSpec, droplet string) (*godo.DomainRecordEditRequest, error) {
	if droplet != "" {
		id, ok := x.dropID[droplet]
		if !ok {
			return nil, fmt.Errorf("droplet %q does not exist, so its address cannot be used", droplet)
		}
		d, err := x.r.Cloud.GetDroplet(x.r.Ctx, id)
		if err != nil {
			return nil, err
		}
		pub4, _, _ := dropletIPs(*d)
		if pub4 == "" {
			return nil, fmt.Errorf("droplet %q has no public IPv4 address yet", droplet)
		}
		spec.Data = pub4
	}
	return spec.request(), nil
}

func (x *applyState) createRecord(domain string, spec recordSpec, droplet string) error {
	req, err := x.recordRequest(spec, droplet)
	if err != nil {
		return err
	}
	rec, err := x.r.Cloud.CreateDomainRecord(x.r.Ctx, domain, req)
	if err != nil {
		return madeOrUnknown("record", err, "docli dns list "+domain)
	}
	x.r.updateManifest(x.mo, false, change{Section: "domains", ID: domain, AddRecords: []int{rec.ID}})
	return nil
}

func (x *applyState) editRecord(domain string, id int, spec recordSpec, droplet string) error {
	req, err := x.recordRequest(spec, droplet)
	if err != nil {
		return err
	}
	if _, err := x.r.Cloud.EditDomainRecord(x.r.Ctx, domain, id, req); err != nil {
		return err
	}
	x.r.updateManifest(x.mo, false, change{Section: "domains", ID: domain})
	return nil
}

// ---- deletions: only ever reached through --prune ----

func (x *applyState) deleteRecord(domain string, id int) error {
	if err := x.r.Cloud.DeleteDomainRecord(x.r.Ctx, domain, id); err != nil {
		return err
	}
	x.r.updateManifest(x.mo, false, change{Section: "domains", ID: domain})
	return nil
}

func (x *applyState) deleteDomain(name string) error {
	if err := x.r.Cloud.DeleteDomain(x.r.Ctx, name); err != nil {
		return err
	}
	x.r.updateManifest(x.mo, false, change{Section: "domains", ID: name})
	return nil
}

func (x *applyState) deleteDatabase(id string) error {
	// ownership is checked again at the moment of deletion, not only when the plan was made
	d, err := x.r.Cloud.GetDatabase(x.r.Ctx, id)
	if err != nil {
		return err
	}
	x.r.noteSecrets(d)
	if !isManagedDB(d) {
		return refusedf("refusing to delete database cluster %q: it no longer carries the tag %s. Nothing was deleted", d.Name, managedTag)
	}
	if err := x.r.Cloud.DeleteDatabase(x.r.Ctx, id); err != nil {
		return err
	}
	x.r.updateManifest(x.mo, false, change{Section: "databases", ID: id})
	return nil
}

func (x *applyState) deleteDroplet(id int) error {
	d, err := x.r.Cloud.GetDroplet(x.r.Ctx, id)
	if err != nil {
		return err
	}
	if !hasTag(d.Tags, managedTag) {
		return refusedf("refusing to delete droplet %q: it no longer carries the tag %s. Nothing was deleted", d.Name, managedTag)
	}
	if err := x.r.Cloud.DeleteDroplet(x.r.Ctx, id); err != nil {
		return err
	}
	x.r.updateManifest(x.mo, true, change{Section: "droplets", ID: itoa(id)})
	return nil
}

func (x *applyState) deleteFirewall(id string) error {
	if err := x.r.Cloud.DeleteFirewall(x.r.Ctx, id); err != nil {
		return err
	}
	x.r.updateManifest(x.mo, true, change{Section: "firewalls", ID: id})
	return nil
}

func (x *applyState) deleteVPC(id string) error {
	if err := x.r.Cloud.DeleteVPC(x.r.Ctx, id); err != nil {
		return err
	}
	x.r.updateManifest(x.mo, false, change{Section: "vpcs", ID: id})
	return nil
}

func (x *applyState) deleteKey(id int) error {
	if err := x.r.Cloud.DeleteKey(x.r.Ctx, id); err != nil {
		return err
	}
	x.r.updateManifest(x.mo, false, change{Section: "ssh_keys", ID: itoa(id)})
	return nil
}
