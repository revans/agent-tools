package main

import (
	"fmt"
	"sort"
	"strings"

	"github.com/digitalocean/godo"
)

// Projects. A DigitalOcean project is a folder for resources: it groups them in the control panel and for
// billing views, and does nothing else. Projects cannot be tagged, so docli knows its own by the name prefix
// docli-, like SSH keys, VPCs and firewalls. Putting a resource in a project is a separate call (there is no
// "unassign": a resource moves by being assigned to another project), which is why every create that takes
// --project does it as a second step and says so when that step fails.

func (a *App) cmdProject(args []string) error {
	if len(args) == 0 {
		return usagef("usage: docli project list|get|create|delete|assign (try: docli help project)")
	}
	switch args[0] {
	case "list":
		return a.projectList(args[1:])
	case "get":
		return a.projectGet(args[1:])
	case "create":
		return a.projectCreate(args[1:])
	case "delete":
		return a.projectDelete(args[1:])
	case "assign":
		return a.projectAssign(args[1:])
	}
	return usagef("unknown project command %q (list, get, create, delete, assign)", args[0])
}

func findProject(ps []godo.Project, ref string) (*godo.Project, error) {
	var hits []godo.Project
	for _, p := range ps {
		if p.Name == ref || p.ID == ref {
			hits = append(hits, p)
		}
	}
	if len(hits) == 0 {
		for _, p := range ps {
			if p.Name == namePrefix+ref {
				hits = append(hits, p)
			}
		}
	}
	switch len(hits) {
	case 0:
		return nil, fmt.Errorf("no project named %q (or with that id). List them with: docli project list", ref)
	case 1:
		return &hits[0], nil
	}
	return nil, fmt.Errorf("%d projects are named %q; use the id instead", len(hits), ref)
}

// lookupProject resolves a --project value before anything is created, so a typo costs nothing. An empty ref
// means no project.
func (r *Run) lookupProject(ref string) (*godo.Project, error) {
	if ref == "" {
		return nil, nil
	}
	ps, err := r.Cloud.ListProjects(r.Ctx)
	if err != nil {
		return nil, err
	}
	return findProject(ps, ref)
}

// putInProject is the second step of a create with --project. When it fails the resource exists, and the error
// says so and how to finish the job.
func (r *Run) putInProject(p *godo.Project, urn, kind, name string) error {
	if p == nil {
		return nil
	}
	if err := r.Cloud.AssignProjectResources(r.Ctx, p.ID, urn); err != nil {
		return fmt.Errorf("the %s %q was created, but it could not be put in the project %q: %v. Finish with: docli project assign %s %s:%s", kind, name, p.Name, err, shellWord(p.Name), projectKind(kind), shellWord(name))
	}
	return nil
}

func projectKind(kind string) string {
	switch kind {
	case "load balancer":
		return "lb"
	case "reserved IP":
		return "reserved-ip"
	}
	return kind
}

func shellWord(s string) string {
	if strings.ContainsAny(s, " \t'\"") {
		return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
	}
	return s
}

// projectStep is the plan line for --project.
func projectStep(p *godo.Project) string {
	if p == nil {
		return ""
	}
	return fmt.Sprintf("then put it in the project %q (id %s)", p.Name, p.ID)
}

func (a *App) projectList(args []string) error {
	fs, g := a.newFlags("project list")
	if _, err := a.parseFlags(fs, args, "project list"); err != nil {
		return err
	}
	r, err := a.start(*g)
	if err != nil {
		return err
	}
	ps, err := r.Cloud.ListProjects(r.Ctx)
	if err != nil {
		return err
	}
	if g.JSON {
		out := []projectJSON{}
		for _, p := range ps {
			out = append(out, projectView(p))
		}
		return r.printJSON(out)
	}
	var rows [][]string
	for _, p := range ps {
		rows = append(rows, []string{p.ID, p.Name, orDash(p.Environment), orDash(p.Purpose), yesNo(p.IsDefault), yesNo(hasManagedPrefix(p.Name))})
	}
	printTable(a.Stdout, []string{"ID", "NAME", "ENVIRONMENT", "PURPOSE", "DEFAULT", "MANAGED"}, rows)
	return nil
}

func (a *App) projectGet(args []string) error {
	fs, g := a.newFlags("project get")
	pos, err := a.parseFlags(fs, args, "project get")
	if err != nil {
		return err
	}
	if len(pos) != 1 {
		return usagef("usage: docli project get NAME|ID")
	}
	r, err := a.start(*g)
	if err != nil {
		return err
	}
	p, err := r.lookupProject(pos[0])
	if err != nil {
		return err
	}
	res, err := r.Cloud.ListProjectResources(r.Ctx, p.ID)
	if err != nil {
		return err
	}
	det := projectDetailJSON{projectJSON: projectView(*p), Resources: []projectResourceJSON{}}
	for _, x := range res {
		det.Resources = append(det.Resources, projectResourceJSON{x.URN, x.AssignedAt, x.Status})
	}
	if g.JSON {
		return r.printJSON(det)
	}
	fmt.Fprintf(a.Stdout, "ID:          %s\nName:        %s\nManaged:     %s\nDefault:     %s\nPurpose:     %s\nEnvironment: %s\nDescription: %s\nCreated:     %s\nResources:   %d\n",
		p.ID, p.Name, yesNo(det.Managed), yesNo(p.IsDefault), orDash(p.Purpose), orDash(p.Environment), orDash(p.Description), orDash(p.CreatedAt), len(res))
	for _, x := range det.Resources {
		fmt.Fprintf(a.Stdout, "  - %s\n", x.URN)
	}
	return nil
}

var projectEnvs = []string{"Development", "Staging", "Production"}

func (a *App) projectCreate(args []string) error {
	fs, g := a.newFlags("project create")
	name := fs.String("name", "", "")
	purpose := fs.String("purpose", "Web Application", "")
	env := fs.String("environment", "", "")
	desc := fs.String("description", "", "")
	mo := addManifestFlags(fs)
	pos, err := a.parseFlags(fs, args, "project create")
	if err != nil {
		return err
	}
	if len(pos) > 0 || *name == "" {
		return usagef("usage: docli project create --name NAME [--purpose \"Web Application\"] [--environment Development|Staging|Production] [--description TEXT]")
	}
	final := ensureNamePrefix(*name)
	if len(final) > 175 {
		return usagef("the project name %q is too long (175 characters at most, with the prefix)", final)
	}
	if *env != "" && !contains(projectEnvs, *env) {
		return usagef("--environment %q must be one of %s", *env, strings.Join(projectEnvs, ", "))
	}
	r, err := a.start(*g)
	if err != nil {
		return err
	}
	ps, err := r.Cloud.ListProjects(r.Ctx)
	if err != nil {
		return err
	}
	for _, p := range ps {
		if p.Name == final {
			return fmt.Errorf("a project named %q already exists (id %s). Nothing was changed", final, p.ID)
		}
	}
	p := Plan{Action: "project.create", Summary: fmt.Sprintf("create the project %q", final),
		Steps: []string{"purpose: " + *purpose, "environment: " + orDash(*env), "a project is a folder for grouping resources; it has no charge of its own"}}
	if final != *name {
		p.Steps = append(p.Steps, fmt.Sprintf("the name gets the prefix %s so docli can tell it created this project (projects cannot carry tags)", namePrefix))
	}
	ok, err := r.Approve(p, approveOpts{})
	if !ok || err != nil {
		return err
	}
	pr, err := r.Cloud.CreateProject(r.Ctx, &godo.CreateProjectRequest{Name: final, Description: *desc, Purpose: *purpose, Environment: *env})
	if err != nil {
		return madeOrUnknown("project", err, "docli project list")
	}
	r.updateManifest(*mo, false, change{Section: "projects", ID: pr.ID})
	if g.JSON {
		return r.printJSON(projectView(*pr))
	}
	fmt.Fprintf(a.Stdout, "Created project %q (id %s)\n", pr.Name, pr.ID)
	return nil
}

func (a *App) projectDelete(args []string) error {
	fs, g := a.newFlags("project delete")
	force := fs.Bool("force-unmanaged", false, "")
	confirm := fs.String("confirm-name", "", "")
	mo := addManifestFlags(fs)
	pos, err := a.parseFlags(fs, args, "project delete")
	if err != nil {
		return err
	}
	if len(pos) != 1 {
		return usagef("usage: docli project delete NAME|ID")
	}
	r, err := a.start(*g)
	if err != nil {
		return err
	}
	p, err := r.lookupProject(pos[0])
	if err != nil {
		return err
	}
	managed := hasManagedPrefix(p.Name)
	rule := "a name starting with " + namePrefix
	if !managed && !*force {
		_, err := r.ApproveDelete(deleteSpec{Kind: "project", Name: p.Name, Managed: false, Rule: rule}, false, "")
		return err
	}
	if p.IsDefault {
		return refusedf("refusing to delete %q: it is the default project, and DigitalOcean does not allow deleting it. Nothing was changed", p.Name)
	}
	res, err := r.Cloud.ListProjectResources(r.Ctx, p.ID)
	if err != nil {
		return err
	}
	if len(res) > 0 {
		var urns []string
		for _, x := range res {
			urns = append(urns, x.URN)
		}
		return refusedf("refusing to delete project %q: it still holds %d resource(s): %s. Move them to another project first (docli project assign OTHER-PROJECT ...). Nothing was changed", p.Name, len(res), strings.Join(urns, ", "))
	}
	ok, err := r.ApproveDelete(deleteSpec{Kind: "project", Name: p.Name, Managed: managed, Rule: rule,
		Steps: []string{fmt.Sprintf("delete the project id %s; it holds no resources, so nothing else is affected", p.ID)}}, *force, *confirm)
	if !ok || err != nil {
		return err
	}
	if err := r.Cloud.DeleteProject(r.Ctx, p.ID); err != nil {
		return err
	}
	r.updateManifest(*mo, false, change{Section: "projects", ID: p.ID})
	return r.deleted("project", p.ID, p.Name)
}

// projectTarget is one resource named on the command line of `project assign`.
type projectTarget struct {
	Kind, Name, URN string
	Managed         bool
	Rule            string
}

// resolveTarget turns KIND:REF into a URN and says whether docli owns it.
func (r *Run) resolveTarget(spec string, owned *ownedSet) (projectTarget, error) {
	kind, ref, ok := strings.Cut(spec, ":")
	if !ok || ref == "" {
		return projectTarget{}, usagef("%q is not KIND:NAME. Kinds: droplet, volume, lb, reserved-ip, database, domain, firewall (for example droplet:web-1)", spec)
	}
	switch kind {
	case "droplet":
		d, err := r.resolveDroplet(ref)
		if err != nil {
			return projectTarget{}, err
		}
		return projectTarget{"droplet", d.Name, d.URN(), hasTag(d.Tags, managedTag), "the tag " + managedTag}, nil
	case "volume":
		vols, err := r.Cloud.ListVolumes(r.Ctx)
		if err != nil {
			return projectTarget{}, err
		}
		v, err := findVolume(vols, ref, r.G.Region)
		if err != nil {
			return projectTarget{}, err
		}
		return projectTarget{"volume", v.Name, v.URN(), hasTag(v.Tags, managedTag), "the tag " + managedTag}, nil
	case "lb", "load-balancer":
		lbs, err := r.Cloud.ListLoadBalancers(r.Ctx)
		if err != nil {
			return projectTarget{}, err
		}
		l, err := findLB(lbs, ref)
		if err != nil {
			return projectTarget{}, err
		}
		return projectTarget{"load balancer", l.Name, l.URN(), hasManagedPrefix(l.Name), "a name starting with " + namePrefix}, nil
	case "reserved-ip", "ip":
		ips, err := r.Cloud.ListReservedIPs(r.Ctx)
		if err != nil {
			return projectTarget{}, err
		}
		ip, err := findReservedIP(ips, ref)
		if err != nil {
			return projectTarget{}, err
		}
		return projectTarget{"reserved IP", ip.IP, ip.URN(), owned.has(ip.IP), "a record in the manifest"}, nil
	case "database", "db":
		d, err := r.resolveDB(ref)
		if err != nil {
			return projectTarget{}, err
		}
		return projectTarget{"database", d.Name, d.URN(), hasTag(d.Tags, managedTag), "the tag " + managedTag}, nil
	case "domain":
		ds, err := r.Cloud.ListDomains(r.Ctx)
		if err != nil {
			return projectTarget{}, err
		}
		d, err := findDomain(ds, ref)
		if err != nil {
			return projectTarget{}, err
		}
		o := owned.domains[d.Name]
		return projectTarget{"domain", d.Name, d.URN(), o != nil && o.Created, "a record in the manifest"}, nil
	case "firewall":
		fws, err := r.Cloud.ListFirewalls(r.Ctx)
		if err != nil {
			return projectTarget{}, err
		}
		f, err := findFirewall(fws, ref)
		if err != nil {
			return projectTarget{}, err
		}
		return projectTarget{"firewall", f.Name, f.URN(), hasManagedPrefix(f.Name), "a name starting with " + namePrefix}, nil
	}
	return projectTarget{}, usagef("unknown kind %q in %q (droplet, volume, lb, reserved-ip, database, domain, firewall)", kind, spec)
}

func (a *App) projectAssign(args []string) error {
	fs, g := a.newFlags("project assign")
	force := fs.Bool("force-unmanaged", false, "")
	confirm := fs.String("confirm-name", "", "")
	mo := addManifestFlags(fs) // only read: it says which reserved IPs and domains docli created
	pos, err := a.parseFlags(fs, args, "project assign")
	if err != nil {
		return err
	}
	if len(pos) < 2 {
		return usagef("usage: docli project assign PROJECT KIND:NAME... (kinds: droplet, volume, lb, reserved-ip, database, domain, firewall)")
	}
	r, err := a.start(*g)
	if err != nil {
		return err
	}
	ps, err := r.Cloud.ListProjects(r.Ctx)
	if err != nil {
		return err
	}
	target, err := findProject(ps, pos[0])
	if err != nil {
		return err
	}
	owned, err := r.loadOwned(*mo)
	if err != nil {
		return err
	}
	var targets []projectTarget
	for _, spec := range pos[1:] {
		t, err := r.resolveTarget(spec, owned)
		if err != nil {
			return err
		}
		targets = append(targets, t)
	}
	// Assigning MOVES a resource: say where each one is now.
	where := map[string]string{}
	for _, p := range ps {
		res, err := r.Cloud.ListProjectResources(r.Ctx, p.ID)
		if err != nil {
			return err
		}
		for _, x := range res {
			where[x.URN] = p.Name
		}
	}
	plan := Plan{Action: "project.assign", Summary: fmt.Sprintf("move %d resource(s) into the project %q", len(targets), target.Name)}
	var unmanaged []string
	var urns []string
	for _, t := range targets {
		cur := where[t.URN]
		switch {
		case cur == target.Name:
			plan.Steps = append(plan.Steps, fmt.Sprintf("%s %q is already in %q; nothing changes for it", t.Kind, t.Name, target.Name))
		case cur != "":
			plan.Steps = append(plan.Steps, fmt.Sprintf("%s %q moves from the project %q to %q", t.Kind, t.Name, cur, target.Name))
		default:
			plan.Steps = append(plan.Steps, fmt.Sprintf("%s %q goes into %q (it is not in a listed project now)", t.Kind, t.Name, target.Name))
		}
		if !t.Managed {
			unmanaged = append(unmanaged, fmt.Sprintf("%s %q (it does not have %s)", t.Kind, t.Name, t.Rule))
		}
		urns = append(urns, t.URN)
	}
	plan.Steps = append(plan.Steps, "a project only groups resources; the resources themselves are not changed")
	if len(unmanaged) > 0 && !*force {
		return refusedf("refusing to move %s: docli did not create %s. Nothing was changed. If you are sure, add --force-unmanaged and type the project name", strings.Join(unmanaged, "; "), pluralIt(len(unmanaged)))
	}
	typed := ""
	if len(unmanaged) > 0 {
		typed = target.Name
		plan.Warnings = append(plan.Warnings, "NOT created by docli, and you are forcing the move: "+strings.Join(unmanaged, "; "))
	}
	ok, err := r.Approve(plan, approveOpts{TypeName: typed, ConfirmName: *confirm})
	if !ok || err != nil {
		return err
	}
	if err := r.Cloud.AssignProjectResources(r.Ctx, target.ID, urns...); err != nil {
		return err
	}
	sort.Strings(urns)
	if g.JSON {
		return r.printJSON(projectAssignJSON{true, target.ID, target.Name, urns})
	}
	fmt.Fprintf(a.Stdout, "Moved %d resource(s) into the project %q.\n", len(urns), target.Name)
	return nil
}

func pluralIt(n int) string {
	if n == 1 {
		return "it"
	}
	return "them"
}
