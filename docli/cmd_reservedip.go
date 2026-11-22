package main

import (
	"fmt"
	"net/netip"
	"os"

	"github.com/digitalocean/godo"
)

// Reserved IPs. A reserved IP has no name and cannot be tagged, so neither of docli's usual ownership rules can
// work. The manifest is the authority, as it is for DNS: docli created it if the manifest says so, and it
// refuses to assign, unassign or delete the rest unless --force-unmanaged is given and the address is typed.
// Assign MOVES an address that may be carrying live traffic, so the plan says where it is now.

func (a *App) cmdReservedIP(args []string) error {
	if len(args) == 0 {
		return usagef("usage: docli reserved-ip list|get|create|delete|assign|unassign (try: docli help reserved-ip)")
	}
	switch args[0] {
	case "list":
		return a.ripList(args[1:])
	case "get":
		return a.ripGet(args[1:])
	case "create":
		return a.ripCreate(args[1:])
	case "delete":
		return a.ripDelete(args[1:])
	case "assign", "unassign":
		return a.ripAssign(args[0], args[1:])
	}
	return usagef("unknown reserved-ip command %q (list, get, create, delete, assign, unassign)", args[0])
}

func findReservedIP(ips []godo.ReservedIP, ref string) (*godo.ReservedIP, error) {
	addr, err := netip.ParseAddr(ref)
	if err != nil {
		return nil, usagef("%q is not an IP address. Reserved IPs are named by their address; list them with: docli reserved-ip list", ref)
	}
	for i := range ips {
		if ips[i].IP == addr.String() {
			return &ips[i], nil
		}
	}
	return nil, fmt.Errorf("no reserved IP %s in this account. List them with: docli reserved-ip list", ref)
}

// ownedSet is what the manifest says docli created, for the kinds the account cannot tell us about.
type ownedSet struct {
	domains map[string]*ownedDomain
	ips     map[string]bool
}

func (o *ownedSet) has(ip string) bool { return o != nil && o.ips[ip] }

func (r *Run) loadOwned(mo manifestOpts) (*ownedSet, error) {
	o := &ownedSet{domains: map[string]*ownedDomain{}, ips: map[string]bool{}}
	if mo.Skip {
		return o, nil
	}
	path, _, err := r.manifestPath(mo)
	if err != nil {
		return nil, err
	}
	m, _, err := readManifest(path)
	if err != nil {
		if os.IsNotExist(err) {
			return o, nil
		}
		return nil, fmt.Errorf("cannot tell what docli created because the manifest is unreadable (%v). Nothing was changed", err)
	}
	s := &snapshot{own: o.domains, ownIPs: o.ips}
	s.attach(m)
	return o, nil
}

func ripDropletName(ip godo.ReservedIP) string {
	if ip.Droplet != nil {
		return ip.Droplet.Name
	}
	return ""
}

func ripRegion(ip godo.ReservedIP) string {
	if ip.Region != nil {
		return ip.Region.Slug
	}
	return ""
}

func (a *App) ripList(args []string) error {
	fs, g := a.newFlags("reserved-ip list")
	mo := addManifestFlags(fs)
	if _, err := a.parseFlags(fs, args, "reserved-ip list"); err != nil {
		return err
	}
	r, err := a.start(*g)
	if err != nil {
		return err
	}
	ips, err := r.Cloud.ListReservedIPs(r.Ctx)
	if err != nil {
		return err
	}
	owned, err := r.loadOwned(*mo)
	if err != nil {
		return err
	}
	var shown []godo.ReservedIP
	for _, ip := range ips {
		if r.G.Region == "" || ripRegion(ip) == r.G.Region {
			shown = append(shown, ip)
		}
	}
	if g.JSON {
		out := []reservedIPJSON{}
		for _, ip := range shown {
			out = append(out, reservedIPView(ip, owned.has(ip.IP)))
		}
		return r.printJSON(out)
	}
	var rows [][]string
	for _, ip := range shown {
		rows = append(rows, []string{ip.IP, ripRegion(ip), orDash(ripDropletName(ip)), yesNo(owned.has(ip.IP))})
	}
	printTable(a.Stdout, []string{"IP", "REGION", "ASSIGNED TO", "MANAGED"}, rows)
	return nil
}

func (a *App) ripGet(args []string) error {
	fs, g := a.newFlags("reserved-ip get")
	mo := addManifestFlags(fs)
	pos, err := a.parseFlags(fs, args, "reserved-ip get")
	if err != nil {
		return err
	}
	if len(pos) != 1 {
		return usagef("usage: docli reserved-ip get IP")
	}
	r, err := a.start(*g)
	if err != nil {
		return err
	}
	ips, err := r.Cloud.ListReservedIPs(r.Ctx)
	if err != nil {
		return err
	}
	ip, err := findReservedIP(ips, pos[0])
	if err != nil {
		return err
	}
	owned, err := r.loadOwned(*mo)
	if err != nil {
		return err
	}
	v := reservedIPView(*ip, owned.has(ip.IP))
	if g.JSON {
		return r.printJSON(v)
	}
	to := "nothing (unassigned)"
	if v.DropletID != 0 {
		to = fmt.Sprintf("droplet %q (id %d)", v.Droplet, v.DropletID)
	}
	fmt.Fprintf(a.Stdout, "IP:         %s\nManaged:    %s (the manifest says whether docli created it)\nRegion:     %s\nAssigned:   %s\nProject:    %s\nLocked:     %s\n",
		v.IP, yesNo(v.Managed), v.Region, to, orDash(v.ProjectID), yesNo(v.Locked))
	return nil
}

func (a *App) ripCreate(args []string) error {
	fs, g := a.newFlags("reserved-ip create")
	dropRef := fs.String("droplet", "", "")
	projRef := fs.String("project", "", "")
	mo := addManifestFlags(fs)
	pos, err := a.parseFlags(fs, args, "reserved-ip create")
	if err != nil {
		return err
	}
	if len(pos) > 0 {
		return usagef("usage: docli reserved-ip create --region REGION | --droplet NAME|ID  [--project P]")
	}
	r, err := a.start(*g)
	if err != nil {
		return err
	}
	req := &godo.ReservedIPCreateRequest{Region: r.G.Region}
	desc := ""
	if *dropRef != "" {
		d, err := r.resolveDroplet(*dropRef)
		if err != nil {
			return err
		}
		dr := dropletView(*d).Region
		if r.G.Region != "" && r.G.Region != dr {
			return fmt.Errorf("droplet %q is in %s but --region says %s: a reserved IP belongs to one region. Nothing was changed", d.Name, dr, r.G.Region)
		}
		req.Region, req.DropletID = "", d.ID
		desc = fmt.Sprintf(", assigned to droplet %q", d.Name)
		if r.G.Region == "" {
			r.G.Region = dr
		}
	}
	if req.Region == "" && req.DropletID == 0 {
		return usagef("a region is required: --region nyc3 (or set DOCLI_REGION), or --droplet NAME to reserve an address in that droplet's region")
	}
	proj, err := r.lookupProject(*projRef)
	if err != nil {
		return err
	}
	p := Plan{Action: "reserved-ip.create", Summary: fmt.Sprintf("reserve an IP address in %s%s", r.G.Region, desc),
		Steps: []string{"the address is chosen by DigitalOcean and cannot be chosen or recovered once released",
			"monthly cost: not shown. The API does not report reserved IP prices, and DigitalOcean may charge for an address that is not assigned to a droplet; see its pricing page",
			"a reserved IP cannot be tagged or named, so docli records that it created this address in the manifest and will only change addresses the manifest lists"}}
	if s := projectStep(proj); s != "" {
		p.Steps = append(p.Steps, s)
	}
	if mo.Skip {
		p.Warnings = append(p.Warnings, "--no-manifest: docli will not remember it created this address, so changing or deleting it later needs --force-unmanaged")
	}
	ok, err := r.Approve(p, approveOpts{})
	if !ok || err != nil {
		return err
	}
	ip, err := r.Cloud.CreateReservedIP(r.Ctx, req)
	if err != nil {
		return madeOrUnknown("reserved IP", err, "docli reserved-ip list")
	}
	r.updateManifest(*mo, false, change{Section: "reserved_ips", ID: ip.IP, Adopt: true})
	if err := r.putInProject(proj, ip.URN(), "reserved IP", ip.IP); err != nil {
		return err
	}
	if g.JSON {
		return r.printJSON(reservedIPView(*ip, !mo.Skip))
	}
	fmt.Fprintf(a.Stdout, "Reserved %s in %s%s\n", ip.IP, ripRegion(*ip), desc)
	return nil
}

// ripGuard refuses an address the manifest does not list unless --force-unmanaged is given.
func ripGuard(ip *godo.ReservedIP, owned *ownedSet, verb string, force bool) (typed, warn string, err error) {
	if owned.has(ip.IP) {
		return "", "", nil
	}
	if !force {
		return "", "", refusedf("refusing to %s the reserved IP %s: the manifest does not list it as created by docli (a reserved IP cannot be tagged or named, so the manifest is the only record). Nothing was changed. If you are sure, add --force-unmanaged and type the address", verb, ip.IP)
	}
	return ip.IP, fmt.Sprintf("%s was NOT created by docli (the manifest does not list it). You are forcing the %s.", ip.IP, verb), nil
}

func (a *App) ripDelete(args []string) error {
	fs, g := a.newFlags("reserved-ip delete")
	force := fs.Bool("force-unmanaged", false, "")
	confirm := fs.String("confirm-name", "", "")
	mo := addManifestFlags(fs)
	pos, err := a.parseFlags(fs, args, "reserved-ip delete")
	if err != nil {
		return err
	}
	if len(pos) != 1 {
		return usagef("usage: docli reserved-ip delete IP [--yes --confirm-name IP]")
	}
	r, err := a.start(*g)
	if err != nil {
		return err
	}
	ips, err := r.Cloud.ListReservedIPs(r.Ctx)
	if err != nil {
		return err
	}
	ip, err := findReservedIP(ips, pos[0])
	if err != nil {
		return err
	}
	owned, err := r.loadOwned(*mo)
	if err != nil {
		return err
	}
	managed := owned.has(ip.IP)
	rule := "a record in the manifest (reserved IPs cannot be tagged)"
	if !managed && !*force {
		_, err := r.ApproveDelete(deleteSpec{Kind: "reserved IP", Name: ip.IP, Managed: false, Rule: rule}, false, "")
		return err
	}
	if ip.Droplet != nil {
		return refusedf("refusing to release the reserved IP %s: it is assigned to droplet %q. Unassign it first (docli reserved-ip unassign %s). Nothing was changed", ip.IP, ip.Droplet.Name, ip.IP)
	}
	ok, err := r.ApproveDelete(deleteSpec{Kind: "reserved IP", Name: ip.IP, Managed: managed, Rule: rule, Always: true,
		Steps: []string{fmt.Sprintf("release %s in %s; DigitalOcean cannot give you the same address back, so anything that points at it (DNS, allow lists) stops working", ip.IP, ripRegion(*ip))}}, *force, *confirm)
	if !ok || err != nil {
		return err
	}
	if err := r.Cloud.DeleteReservedIP(r.Ctx, ip.IP); err != nil {
		return err
	}
	r.updateManifest(*mo, false, change{Section: "reserved_ips", ID: ip.IP})
	return r.deleted("reserved-ip", ip.IP, ip.IP)
}

func (a *App) ripAssign(verb string, args []string) error {
	fs, g := a.newFlags("reserved-ip " + verb)
	force := fs.Bool("force-unmanaged", false, "")
	confirm := fs.String("confirm-name", "", "")
	mo := addManifestFlags(fs)
	pos, err := a.parseFlags(fs, args, "reserved-ip "+verb)
	if err != nil {
		return err
	}
	want := 1
	if verb == "assign" {
		want = 2
	}
	if len(pos) != want {
		if verb == "assign" {
			return usagef("usage: docli reserved-ip assign IP DROPLET")
		}
		return usagef("usage: docli reserved-ip unassign IP")
	}
	r, err := a.start(*g)
	if err != nil {
		return err
	}
	ips, err := r.Cloud.ListReservedIPs(r.Ctx)
	if err != nil {
		return err
	}
	ip, err := findReservedIP(ips, pos[0])
	if err != nil {
		return err
	}
	owned, err := r.loadOwned(*mo)
	if err != nil {
		return err
	}
	typed, warn, err := ripGuard(ip, owned, verb, *force)
	if err != nil {
		return err
	}
	p := Plan{Action: "reserved-ip." + verb}
	if warn != "" {
		p.Warnings = append(p.Warnings, warn)
	}
	now := "currently assigned to nothing"
	if ip.Droplet != nil {
		now = fmt.Sprintf("currently assigned to droplet %q (id %d)", ip.Droplet.Name, ip.Droplet.ID)
	}
	var d *godo.Droplet
	if verb == "assign" {
		if d, err = r.resolveDroplet(pos[1]); err != nil {
			return err
		}
		switch {
		case ip.Droplet != nil && ip.Droplet.ID == d.ID:
			return fmt.Errorf("%s is already assigned to droplet %q. Nothing was changed", ip.IP, d.Name)
		case dropletView(*d).Region != ripRegion(*ip):
			return fmt.Errorf("%s is in %s but droplet %q is in %s: a reserved IP can only be assigned within its region. Nothing was changed", ip.IP, ripRegion(*ip), d.Name, dropletView(*d).Region)
		}
		p.Summary = fmt.Sprintf("assign %s to droplet %q (id %d)", ip.IP, d.Name, d.ID)
		p.Steps = []string{ip.IP + " is " + now}
		if ip.Droplet != nil {
			p.Steps = append(p.Steps, fmt.Sprintf("this MOVES it: traffic to %s stops reaching droplet %q and goes to %q from now on", ip.IP, ip.Droplet.Name, d.Name))
			p.Warnings = append(p.Warnings, fmt.Sprintf("%s is in use by droplet %q. Moving it interrupts whatever that droplet serves on this address", ip.IP, ip.Droplet.Name))
		}
	} else {
		if ip.Droplet == nil {
			return fmt.Errorf("%s is not assigned to a droplet, so there is nothing to unassign. Nothing was changed", ip.IP)
		}
		p.Summary = fmt.Sprintf("unassign %s from droplet %q", ip.IP, ip.Droplet.Name)
		p.Steps = []string{ip.IP + " is " + now, "the address stays in your account but points at nothing; traffic to it is dropped"}
		p.Warnings = append(p.Warnings, fmt.Sprintf("droplet %q stops receiving traffic sent to %s", ip.Droplet.Name, ip.IP))
	}
	ok, err := r.Approve(p, approveOpts{TypeName: typed, ConfirmName: *confirm})
	if !ok || err != nil {
		return err
	}
	if verb == "assign" {
		err = r.Cloud.AssignReservedIP(r.Ctx, ip.IP, d.ID)
	} else {
		err = r.Cloud.UnassignReservedIP(r.Ctx, ip.IP)
	}
	if err != nil {
		return err
	}
	r.updateManifest(*mo, false, change{Section: "reserved_ips", ID: ip.IP})
	after, err := r.Cloud.ListReservedIPs(r.Ctx)
	if err == nil {
		if cur, err := findReservedIP(after, ip.IP); err == nil {
			if g.JSON {
				return r.printJSON(reservedIPView(*cur, owned.has(cur.IP)))
			}
			if verb == "assign" {
				fmt.Fprintf(a.Stdout, "Requested: %s now goes to droplet %q. DigitalOcean completes it in the background.\n", ip.IP, d.Name)
			} else {
				fmt.Fprintf(a.Stdout, "Requested: %s is unassigned. DigitalOcean completes it in the background.\n", ip.IP)
			}
			return nil
		}
	}
	return fmt.Errorf("the request was accepted but %s could not be read back; check with: docli reserved-ip get %s", ip.IP, ip.IP)
}
