package main

import (
	"fmt"
	"strings"

	"github.com/digitalocean/godo"
)

func (a *App) cmdFirewall(args []string) error {
	if len(args) == 0 {
		return usagef("usage: docli firewall list|get|create|delete|add-rule|remove-rule|attach|detach (try: docli help firewall)")
	}
	switch args[0] {
	case "list":
		return a.firewallList(args[1:])
	case "get":
		return a.firewallGet(args[1:])
	case "create":
		return a.firewallCreate(args[1:])
	case "delete":
		return a.firewallDelete(args[1:])
	case "add-rule", "remove-rule":
		return a.firewallRule(args[0], args[1:])
	case "attach", "detach":
		return a.firewallAttach(args[0], args[1:])
	}
	return usagef("unknown firewall command %q", args[0])
}

func findFirewall(fws []godo.Firewall, ref string) (*godo.Firewall, error) {
	var hits []godo.Firewall
	for _, f := range fws {
		if f.Name == ref || f.ID == ref {
			hits = append(hits, f)
		}
	}
	if len(hits) == 0 {
		for _, f := range fws {
			if f.Name == namePrefix+ref {
				hits = append(hits, f)
			}
		}
	}
	switch len(hits) {
	case 0:
		return nil, fmt.Errorf("no firewall named %q (or with that id). List them with: docli firewall list", ref)
	case 1:
		return &hits[0], nil
	}
	return nil, fmt.Errorf("%d firewalls are named %q; use the id instead", len(hits), ref)
}

func (r *Run) firewallByRef(ref string) (*godo.Firewall, error) {
	fws, err := r.Cloud.ListFirewalls(r.Ctx)
	if err != nil {
		return nil, err
	}
	return findFirewall(fws, ref)
}

func (a *App) firewallList(args []string) error {
	fs, g := a.newFlags("firewall list")
	if _, err := a.parseFlags(fs, args, "firewall list"); err != nil {
		return err
	}
	r, err := a.start(*g)
	if err != nil {
		return err
	}
	fws, err := r.Cloud.ListFirewalls(r.Ctx)
	if err != nil {
		return err
	}
	if g.JSON {
		out := []firewallJSON{}
		for _, f := range fws {
			out = append(out, firewallView(f))
		}
		return r.printJSON(out)
	}
	var rows [][]string
	for _, f := range fws {
		rows = append(rows, []string{f.ID, f.Name, f.Status, itoa(len(f.InboundRules)), itoa(len(f.OutboundRules)),
			itoa(len(f.DropletIDs)), strings.Join(f.Tags, ","), yesNo(hasManagedPrefix(f.Name))})
	}
	printTable(a.Stdout, []string{"ID", "NAME", "STATUS", "IN", "OUT", "DROPLETS", "TAGS", "MANAGED"}, rows)
	return nil
}

func printRules(r *Run, rules []Rule) {
	if len(rules) == 0 {
		fmt.Fprintln(r.Stdout, "  (no rules)")
	}
	for _, x := range rules {
		fmt.Fprintf(r.Stdout, "  %-45s %s\n", x.Spec(), x.Describe())
	}
}

func (a *App) firewallGet(args []string) error {
	fs, g := a.newFlags("firewall get")
	pos, err := a.parseFlags(fs, args, "firewall get")
	if err != nil {
		return err
	}
	if len(pos) != 1 {
		return usagef("usage: docli firewall get NAME|ID")
	}
	r, err := a.start(*g)
	if err != nil {
		return err
	}
	f, err := r.firewallByRef(pos[0])
	if err != nil {
		return err
	}
	if g.JSON {
		return r.printJSON(firewallView(*f))
	}
	fmt.Fprintf(a.Stdout, "ID:        %s\nName:      %s\nStatus:    %s\nManaged:   %s\nDroplets:  %v\nTags:      %s\nCreated:   %s\n\nRules (anything not listed is blocked):\n",
		f.ID, f.Name, f.Status, yesNo(hasManagedPrefix(f.Name)), nonNil(f.DropletIDs), strings.Join(f.Tags, ","), orDash(f.Created))
	printRules(r, firewallRules(*f))
	return nil
}

func parseRules(specs []string) ([]Rule, error) {
	var out []Rule
	for _, s := range specs {
		x, err := ParseRule(s)
		if err != nil {
			return nil, usagef("%v", err)
		}
		out = append(out, x)
	}
	return out, nil
}

func rulePlanSteps(rules []Rule) []string {
	var steps []string
	steps = append(steps, fmt.Sprintf("resulting rule list (%d):", len(rules)))
	for _, x := range rules {
		steps = append(steps, fmt.Sprintf("  %s  =  %s", x.Spec(), x.Describe()))
	}
	return steps
}

const denyNote = "DigitalOcean firewalls deny by default: attached droplets accept only what an inbound rule allows and send only what an outbound rule allows."

func ruleWarnings(rules []Rule) []string {
	var in, out int
	for _, x := range rules {
		if x.Dir == "in" {
			in++
		} else {
			out++
		}
	}
	w := []string{denyNote}
	if in == 0 {
		w = append(w, "there are no inbound rules: every incoming connection to attached droplets will be blocked, including SSH")
	}
	if out == 0 {
		w = append(w, "there are no outbound rules: attached droplets will not be able to reach anything, including package mirrors and DNS")
	}
	return w
}

func (a *App) firewallCreate(args []string) error {
	fs, g := a.newFlags("firewall create")
	name := fs.String("name", "", "")
	var specs, tags, drops stringList
	fs.Var(&specs, "rule", "")
	fs.Var(&tags, "tag", "")
	fs.Var(&drops, "droplet", "")
	projRef := fs.String("project", "", "")
	mo := addManifestFlags(fs)
	pos, err := a.parseFlags(fs, args, "firewall create")
	if err != nil {
		return err
	}
	if len(pos) > 0 || *name == "" {
		return usagef("usage: docli firewall create --name NAME --rule in:tcp:22:from=10.116.0.0/20 [--rule ...] [--tag T] [--droplet NAME|ID] [--project P]")
	}
	rules, err := parseRules(specs)
	if err != nil {
		return err
	}
	for _, t := range tags {
		if err := validateTag(t); err != nil {
			return usagef("%v", err)
		}
	}
	final := ensureNamePrefix(*name)
	if err := validateResourceName("firewall", final); err != nil {
		return err
	}
	r, err := a.start(*g)
	if err != nil {
		return err
	}
	fws, err := r.Cloud.ListFirewalls(r.Ctx)
	if err != nil {
		return err
	}
	for _, f := range fws {
		if f.Name == final {
			return fmt.Errorf("a firewall named %q already exists (id %s). Nothing was changed", final, f.ID)
		}
	}
	ids, names, err := r.resolveDroplets(drops)
	if err != nil {
		return err
	}
	proj, err := r.lookupProject(*projRef)
	if err != nil {
		return err
	}
	p := Plan{Action: "firewall.create", Summary: fmt.Sprintf("create firewall %q", final)}
	if final != *name {
		p.Steps = append(p.Steps, fmt.Sprintf("the name gets the prefix %s so docli can tell it created this firewall (firewalls cannot carry tags)", namePrefix))
	}
	p.Steps = append(p.Steps, rulePlanSteps(rules)...)
	if len(ids) > 0 || len(tags) > 0 {
		p.Steps = append(p.Steps, fmt.Sprintf("apply to droplets: %s; tags: %s", orDash(strings.Join(names, ",")), orDash(strings.Join(tags, ","))))
	} else {
		p.Steps = append(p.Steps, "not attached to anything yet (use: docli firewall attach)")
	}
	if s := projectStep(proj); s != "" {
		p.Steps = append(p.Steps, s)
	}
	p.Warnings = ruleWarnings(rules)
	ok, err := r.Approve(p, approveOpts{})
	if !ok || err != nil {
		return err
	}
	req := &godo.FirewallRequest{Name: final, DropletIDs: ids, Tags: nonNil([]string(tags))}
	rr := rulesRequest(rules)
	req.InboundRules, req.OutboundRules = rr.InboundRules, rr.OutboundRules
	if req.DropletIDs == nil {
		req.DropletIDs = []int{}
	}
	f, err := r.Cloud.CreateFirewall(r.Ctx, req)
	if err != nil {
		return err
	}
	r.updateManifest(*mo, true, change{Section: "firewalls", ID: f.ID})
	if err := r.putInProject(proj, f.URN(), "firewall", f.Name); err != nil {
		return err
	}
	if g.JSON {
		return r.printJSON(firewallView(*f))
	}
	fmt.Fprintf(a.Stdout, "Created firewall %q (id %s) with %d rule(s)\n", f.Name, f.ID, len(rules))
	return nil
}

func (a *App) firewallDelete(args []string) error {
	fs, g := a.newFlags("firewall delete")
	force := fs.Bool("force-unmanaged", false, "")
	confirm := fs.String("confirm-name", "", "")
	mo := addManifestFlags(fs)
	pos, err := a.parseFlags(fs, args, "firewall delete")
	if err != nil {
		return err
	}
	if len(pos) != 1 {
		return usagef("usage: docli firewall delete NAME|ID")
	}
	r, err := a.start(*g)
	if err != nil {
		return err
	}
	f, err := r.firewallByRef(pos[0])
	if err != nil {
		return err
	}
	spec := deleteSpec{Kind: "firewall", Name: f.Name, Managed: hasManagedPrefix(f.Name), Rule: "a name starting with " + namePrefix,
		Steps: []string{fmt.Sprintf("delete firewall id %s (%d inbound, %d outbound rules)", f.ID, len(f.InboundRules), len(f.OutboundRules))}}
	if len(f.DropletIDs)+len(f.Tags) > 0 {
		spec.Warns = []string{fmt.Sprintf("it is attached to %d droplet(s) and %d tag(s); they lose this protection", len(f.DropletIDs), len(f.Tags))}
	}
	ok, err := r.ApproveDelete(spec, *force, *confirm)
	if !ok || err != nil {
		return err
	}
	if err := r.Cloud.DeleteFirewall(r.Ctx, f.ID); err != nil {
		return err
	}
	r.updateManifest(*mo, true, change{Section: "firewalls", ID: f.ID})
	return r.deleted("firewall", f.ID, f.Name)
}

func (a *App) firewallRule(verb string, args []string) error {
	fs, g := a.newFlags("firewall " + verb)
	mo := addManifestFlags(fs)
	pos, err := a.parseFlags(fs, args, "firewall "+verb)
	if err != nil {
		return err
	}
	if len(pos) < 2 {
		return usagef("usage: docli firewall %s NAME RULE [RULE...]   e.g. in:tcp:22:from=10.116.0.0/20", verb)
	}
	rules, err := parseRules(pos[1:])
	if err != nil {
		return err
	}
	r, err := a.start(*g)
	if err != nil {
		return err
	}
	f, err := r.firewallByRef(pos[0])
	if err != nil {
		return err
	}
	current := firewallRules(*f)
	var result []Rule
	rr := &godo.FirewallRulesRequest{InboundRules: []godo.InboundRule{}, OutboundRules: []godo.OutboundRule{}}
	p := Plan{Action: "firewall." + strings.ReplaceAll(verb, "-", "_"), Summary: fmt.Sprintf("%s on firewall %q", strings.Replace(verb, "-", " ", 1), f.Name)}
	if verb == "add-rule" {
		result = append(result, current...)
		for _, x := range rules {
			for _, c := range current {
				if c.Equal(x) {
					return fmt.Errorf("the rule %s is already on firewall %q. Nothing was changed", x.Spec(), f.Name)
				}
			}
			p.Steps = append(p.Steps, "add: "+x.Describe())
			result = append(result, x)
		}
		rr = rulesRequest(rules)
	} else {
		for _, x := range rules {
			found := false
			for i, in := range f.InboundRules {
				if x.Dir == "in" && ruleFromInbound(in).Equal(x) {
					rr.InboundRules, found = append(rr.InboundRules, f.InboundRules[i]), true
				}
			}
			for i, o := range f.OutboundRules {
				if x.Dir == "out" && ruleFromOutbound(o).Equal(x) {
					rr.OutboundRules, found = append(rr.OutboundRules, f.OutboundRules[i]), true
				}
			}
			if !found {
				return fmt.Errorf("firewall %q has no rule %s. See its rules with: docli firewall get %s", f.Name, x.Spec(), f.Name)
			}
			p.Steps = append(p.Steps, "remove: "+x.Describe())
		}
		for _, c := range current {
			keep := true
			for _, x := range rules {
				if c.Equal(x) {
					keep = false
				}
			}
			if keep {
				result = append(result, c)
			}
		}
	}
	p.Steps = append(p.Steps, rulePlanSteps(result)...)
	p.Warnings = ruleWarnings(result)
	ok, err := r.Approve(p, approveOpts{})
	if !ok || err != nil {
		return err
	}
	if verb == "add-rule" {
		err = r.Cloud.AddFirewallRules(r.Ctx, f.ID, rr)
	} else {
		err = r.Cloud.RemoveFirewallRules(r.Ctx, f.ID, rr)
	}
	if err != nil {
		return err
	}
	r.updateManifest(*mo, true, change{Section: "firewalls", ID: f.ID})
	if g.JSON {
		out := firewallView(*f)
		out.Rules = []ruleJSON{}
		for _, x := range result {
			out.Rules = append(out.Rules, ruleView(x))
		}
		return r.printJSON(out)
	}
	fmt.Fprintf(a.Stdout, "Firewall %q now has %d rule(s)\n", f.Name, len(result))
	return nil
}

func (a *App) firewallAttach(verb string, args []string) error {
	fs, g := a.newFlags("firewall " + verb)
	var tags, drops stringList
	fs.Var(&tags, "tag", "")
	fs.Var(&drops, "droplet", "")
	mo := addManifestFlags(fs)
	pos, err := a.parseFlags(fs, args, "firewall "+verb)
	if err != nil {
		return err
	}
	if len(pos) != 1 || len(tags)+len(drops) == 0 {
		return usagef("usage: docli firewall %s NAME (--droplet NAME|ID | --tag TAG) [...]", verb)
	}
	for _, t := range tags {
		if err := validateTag(t); err != nil {
			return usagef("%v", err)
		}
	}
	r, err := a.start(*g)
	if err != nil {
		return err
	}
	f, err := r.firewallByRef(pos[0])
	if err != nil {
		return err
	}
	ids, names, err := r.resolveDroplets(drops)
	if err != nil {
		return err
	}
	word, prep := "attach", "to"
	if verb == "detach" {
		word, prep = "detach", "from"
	}
	p := Plan{Action: "firewall." + verb, Summary: fmt.Sprintf("%s firewall %q %s droplets: %s; tags: %s", word, f.Name, prep, orDash(strings.Join(names, ",")), orDash(strings.Join(tags, ",")))}
	rules := firewallRules(*f)
	if verb == "attach" {
		p.Steps = append(p.Steps, "these droplets will then accept and send only what these rules allow:")
		p.Steps = append(p.Steps, rulePlanSteps(rules)...)
		p.Warnings = ruleWarnings(rules)
	} else {
		p.Steps = append(p.Steps, "the droplets lose the protection of this firewall (they may be exposed if they have no other firewall)")
	}
	ok, err := r.Approve(p, approveOpts{})
	if !ok || err != nil {
		return err
	}
	if verb == "attach" {
		if len(ids) > 0 {
			err = r.Cloud.AddFirewallDroplets(r.Ctx, f.ID, ids...)
		}
		if err == nil && len(tags) > 0 {
			err = r.Cloud.AddFirewallTags(r.Ctx, f.ID, tags...)
		}
	} else {
		if len(ids) > 0 {
			err = r.Cloud.RemoveFirewallDroplets(r.Ctx, f.ID, ids...)
		}
		if err == nil && len(tags) > 0 {
			err = r.Cloud.RemoveFirewallTags(r.Ctx, f.ID, tags...)
		}
	}
	if err != nil {
		return err
	}
	r.updateManifest(*mo, true, change{Section: "firewalls", ID: f.ID})
	nf, err := r.firewallByRef(f.ID)
	if err != nil {
		nf = f
	}
	if g.JSON {
		return r.printJSON(firewallView(*nf))
	}
	fmt.Fprintf(a.Stdout, "Firewall %q %sed: %d droplet(s), tags %s\n", nf.Name, verb, len(nf.DropletIDs), orDash(strings.Join(nf.Tags, ",")))
	return nil
}
