package main

import (
	"fmt"
	"sort"
	"strings"

	"github.com/digitalocean/godo"
)

// Planning for the milestone 4 kinds the stack file can hold: projects, volumes and load balancers. They are
// planned by plain methods called from planStack in a fixed place, like every other kind: there is no registry.
// Reserved IPs and Spaces are not in the stack file (see the README).

const volumeCostDetail = "monthly cost is not shown: the API does not report block storage prices"
const lbCostDetail = "monthly cost is not shown: the API does not report load balancer prices"

func (pl *planner) findProjectLive(name string) (int, bool, error) {
	return findNamed(pl.snap.projects, func(p *godo.Project) string { return p.Name }, name)
}

func (pl *planner) findLBLive(name string) (int, bool, error) {
	return findNamed(pl.snap.lbs, func(l *godo.LoadBalancer) string { return l.Name }, name)
}

// lookupProjectRef resolves a project: named by a volume or load balancer: declared in the file, or already in
// the account.
func (pl *planner) lookupProjectRef(ref string, line int, what string) bool {
	if ref == "" {
		return true
	}
	if _, ok := pl.x.projectID[ref]; ok || pl.willMake["project:"+ref] {
		return true
	}
	if nameIn(pl.s.Projects, func(x StackProject) string { return x.Name }, ref) {
		return false // declared but it failed earlier; that error is already listed
	}
	p, err := findProject(pl.snap.projects, ref)
	if err != nil {
		pl.errf(line, "%s: %v", what, err)
		return false
	}
	pl.x.projectID[ref] = p.ID
	return true
}

func (pl *planner) planProjects() {
	for i := range pl.s.Projects {
		pr := pl.s.Projects[i]
		idx, managed, err := pl.findProjectLive(pr.Name)
		if err != nil {
			pl.errf(pr.Pos.at("name"), "project %q: %v", pr.Name, err)
			continue
		}
		final := ensureNamePrefix(pr.Name)
		if idx < 0 {
			pl.item("project", pr.Name, actCreate, "", "name in the account: "+final, "purpose: "+pr.Purpose, "environment: "+orDash(pr.Environment), "a project has no charge of its own")
			pl.willMake["project:"+pr.Name] = true
			spec := pr
			pl.op("project", pr.Name, fmt.Sprintf("create project %q", final), func() error { return pl.x.createProject(spec, final) })
			continue
		}
		live := pl.snap.projects[idx]
		pl.x.projectID[pr.Name] = live.ID
		if !managed {
			pl.item("project", pr.Name, actUnmanaged, live.ID, "exists as "+live.Name+" but docli did not create it: it is used as it is and never changed")
			continue
		}
		var diffs []string
		if live.Purpose != pr.Purpose {
			diffs = append(diffs, fmt.Sprintf("purpose: file says %q, the account has %q", pr.Purpose, live.Purpose))
		}
		if pr.Environment != "" && live.Environment != pr.Environment {
			diffs = append(diffs, fmt.Sprintf("environment: file says %q, the account has %q", pr.Environment, live.Environment))
		}
		if len(diffs) > 0 {
			pl.item("project", pr.Name, actReplace, live.ID, append(diffs, "docli does not change a project's purpose or environment: nothing was done for these differences")...)
			continue
		}
		pl.item("project", pr.Name, actUnchanged, live.ID, "exists as "+live.Name)
	}
}

func (pl *planner) volumeTags(v *StackVolume) []string {
	tags := []string{managedTag}
	if t := pl.stackTag(); t != "" {
		tags = append(tags, t)
	}
	for _, t := range v.Tags {
		if !hasTag(tags, t) {
			tags = append(tags, t)
		}
	}
	return tags
}

func (pl *planner) planVolumes() {
	for i := range pl.s.Volumes {
		v := pl.s.Volumes[i]
		what := fmt.Sprintf("volume %q", v.Name)
		projOK := pl.lookupProjectRef(v.Project, v.Pos.at("project"), what)
		idx := -1
		for j := range pl.snap.volumes {
			if pl.snap.volumes[j].Name == v.Name && volumeRegion(pl.snap.volumes[j]) == v.Region {
				idx = j
			}
		}
		if idx < 0 {
			pl.planVolumeCreate(v, what, projOK)
			continue
		}
		live := pl.snap.volumes[idx]
		pl.x.volID[v.Name] = live.ID
		if !hasTag(live.Tags, managedTag) {
			pl.item("volume", v.Name, actUnmanaged, live.ID, "exists but docli did not create it: it is used as it is and never changed",
				"to manage it, run: docli adopt volume "+v.Name)
			continue
		}
		var diffs, chg []string
		switch {
		case int64(v.SizeGB) > live.SizeGigaBytes:
			chg = append(chg, fmt.Sprintf("grow: %d GB now, %d GB after (a volume can never be shrunk again)", live.SizeGigaBytes, v.SizeGB))
			name, id, size, region := v.Name, live.ID, v.SizeGB, v.Region
			pl.op("volume", v.Name, fmt.Sprintf("grow volume %q to %d GB", v.Name, v.SizeGB), func() error { return pl.x.resizeVolume(name, id, size, region) })
		case int64(v.SizeGB) < live.SizeGigaBytes:
			diffs = append(diffs, fmt.Sprintf("size_gb: file says %d, the account has %d (a volume cannot be shrunk; create a new one and copy the data)", v.SizeGB, live.SizeGigaBytes))
		}
		if v.Droplet != "" {
			wantID, known := pl.x.dropID[v.Droplet]
			attachedWant := false
			for _, id := range live.DropletIDs {
				attachedWant = attachedWant || (known && id == wantID)
			}
			switch {
			case attachedWant:
			case len(live.DropletIDs) > 0:
				diffs = append(diffs, fmt.Sprintf("droplet: file says %s, the volume is attached to %s (docli does not move a volume in a plan: detach it with docli volume detach)", v.Droplet, pl.dropletNames(live.DropletIDs)))
			default:
				chg = append(chg, "attach to droplet "+v.Droplet)
				name, drop := v.Name, v.Droplet
				pl.op("volume", v.Name, fmt.Sprintf("attach volume %q to droplet %q", v.Name, v.Droplet), func() error { return pl.x.attachVolume(name, drop) })
			}
		}
		switch {
		case len(diffs) > 0:
			pl.item("volume", v.Name, actReplace, live.ID, append(append(diffs, chg...), "docli does not replace a volume automatically: nothing was done for the differences above")...)
		case len(chg) > 0:
			pl.item("volume", v.Name, actChange, live.ID, chg...)
		default:
			pl.item("volume", v.Name, actUnchanged, live.ID, fmt.Sprintf("exists, %d GB in %s", live.SizeGigaBytes, v.Region))
		}
	}
}

func (pl *planner) dropletNames(ids []int) string {
	var out []string
	for _, id := range ids {
		name := itoa(id)
		for _, d := range pl.snap.droplets {
			if d.ID == id {
				name = d.Name
			}
		}
		out = append(out, name)
	}
	return strings.Join(out, ", ")
}

func (pl *planner) planVolumeCreate(v StackVolume, what string, projOK bool) {
	if !pl.regionKnown(v.Region, v.Pos.Line, what) || !projOK {
		return
	}
	tags := pl.volumeTags(&v)
	det := []string{fmt.Sprintf("%d GB in %s", v.SizeGB, v.Region), "tags: " + strings.Join(tags, ", "), volumeCostDetail}
	if v.FilesystemType != "" {
		det = append(det, "filesystem: "+v.FilesystemType)
	}
	if v.Droplet != "" {
		det = append(det, "attach to droplet "+v.Droplet+" once both exist")
	}
	if v.Project != "" {
		det = append(det, "put it in the project "+v.Project)
	}
	pl.item("volume", v.Name, actCreate, "", det...)
	pl.unknown = append(pl.unknown, "volume "+v.Name)
	pl.willMake["volume:"+v.Name] = true
	spec := v
	pl.op("volume", v.Name, fmt.Sprintf("create volume %q", v.Name), func() error { return pl.x.createVolume(spec, tags) })
	if v.Droplet != "" {
		name, drop := v.Name, v.Droplet
		pl.op("volume", v.Name, fmt.Sprintf("attach volume %q to droplet %q", v.Name, v.Droplet), func() error { return pl.x.attachVolume(name, drop) })
	}
}

// lbVPC is the VPC a stack load balancer goes in: the one it names, else the one all its droplets share.
func (pl *planner) lbVPC(l *StackLB) string {
	if l.VPC != "" {
		return l.VPC
	}
	common := ""
	for _, name := range l.Droplets {
		for _, d := range pl.s.Droplets {
			if d.Name == name {
				if common != "" && d.VPC != common {
					return ""
				}
				common = d.VPC
			}
		}
	}
	return common
}

func (pl *planner) planLBs() {
	for i := range pl.s.LBs {
		l := pl.s.LBs[i]
		what := fmt.Sprintf("load balancer %q", l.Name)
		projOK := pl.lookupProjectRef(l.Project, l.Pos.at("project"), what)
		vpc := pl.lbVPC(&l)
		vpcOK := true
		if vpc != "" {
			vpcOK = pl.lookupVPC(vpc, l.Pos.at("vpc"), what)
		}
		idx, managed, err := pl.findLBLive(l.Name)
		if err != nil {
			pl.errf(l.Pos.at("name"), "%v", err)
			continue
		}
		final := ensureNamePrefix(l.Name)
		if idx < 0 {
			pl.planLBCreate(l, final, vpc, what, vpcOK && projOK)
			continue
		}
		live := pl.snap.lbs[idx]
		pl.x.lbID[l.Name] = live.ID
		if !managed {
			pl.item("load_balancer", l.Name, actUnmanaged, live.ID, "exists as "+live.Name+" but docli did not create it: it is used as it is and never changed")
			continue
		}
		var diffs []string
		if lbRegion(live) != l.Region {
			diffs = append(diffs, fmt.Sprintf("region: file says %s, the account has %s", l.Region, lbRegion(live)))
		}
		if vpcOK && vpc != "" {
			if want := pl.x.vpcID[vpc]; want != "" && live.VPCUUID != want {
				diffs = append(diffs, fmt.Sprintf("vpc: file says %s, the load balancer is in another VPC", vpc))
			}
		}
		have, want := []string{}, []string{}
		for _, f := range live.ForwardingRules {
			have = append(have, lbRuleSpec(f))
		}
		for _, f := range l.rules {
			want = append(want, lbRuleSpec(f))
		}
		sort.Strings(have)
		sort.Strings(want)
		if strings.Join(have, " ") != strings.Join(want, " ") {
			diffs = append(diffs, fmt.Sprintf("forwarding_rules: file says [%s], the account has [%s]", strings.Join(want, "; "), strings.Join(have, "; ")))
		}
		if l.health != nil && lbHealthSpec(live.HealthCheck) != lbHealthSpec(l.health) {
			diffs = append(diffs, fmt.Sprintf("health_check: file says %s, the account has %s", lbHealthSpec(l.health), orDash(lbHealthSpec(live.HealthCheck))))
		}
		if l.Tag != "" && live.Tag != l.Tag {
			diffs = append(diffs, fmt.Sprintf("tag: file says %s, the account has %s", l.Tag, orDash(live.Tag)))
		}
		if len(l.Droplets) > 0 {
			var wantIDs []int
			missing := false
			for _, d := range l.Droplets {
				if id, ok := pl.x.dropID[d]; ok {
					wantIDs = append(wantIDs, id)
				} else {
					missing = true
				}
			}
			sort.Ints(wantIDs)
			haveIDs := append([]int(nil), live.DropletIDs...)
			sort.Ints(haveIDs)
			if missing || fmt.Sprint(wantIDs) != fmt.Sprint(haveIDs) {
				diffs = append(diffs, fmt.Sprintf("droplets: file says [%s], the account has [%s]", strings.Join(l.Droplets, ", "), pl.dropletNames(live.DropletIDs)))
			}
		}
		if live.Algorithm != "" && live.Algorithm != l.Algorithm {
			diffs = append(diffs, fmt.Sprintf("algorithm: file says %s, the account has %s", l.Algorithm, live.Algorithm))
		}
		if live.RedirectHttpToHttps != l.RedirectHTTP {
			diffs = append(diffs, fmt.Sprintf("redirect_http_to_https: file says %v, the account has %v", l.RedirectHTTP, live.RedirectHttpToHttps))
		}
		if l.Size != "" && live.SizeSlug != "" && live.SizeSlug != l.Size {
			diffs = append(diffs, fmt.Sprintf("size: file says %s, the account has %s", l.Size, live.SizeSlug))
		}
		if len(diffs) > 0 {
			pl.item("load_balancer", l.Name, actReplace, live.ID, append(diffs, "docli has no load balancer update yet and does not replace one automatically: nothing was done for the differences above")...)
			continue
		}
		pl.item("load_balancer", l.Name, actUnchanged, live.ID, fmt.Sprintf("exists as %s, %d forwarding rule(s), %s", live.Name, len(live.ForwardingRules), orDash(live.Status)))
	}
}

func (pl *planner) planLBCreate(l StackLB, final, vpc, what string, refsOK bool) {
	ok := pl.regionKnown(l.Region, l.Pos.Line, what)
	if vpc != "" && refsOK {
		if rg := pl.vpcRegion[vpc]; rg != "" && rg != l.Region {
			pl.errf(l.Pos.at("vpc"), "%s is for %s but VPC %q is in %s", what, l.Region, vpc, rg)
			refsOK = false
		}
	}
	if !ok || !refsOK {
		return
	}
	var det []string
	det = append(det, "name in the account: "+final, "region: "+l.Region, "size: "+firstNonEmpty(l.Size, "the API default"), "algorithm: "+l.Algorithm)
	if vpc != "" {
		det = append(det, "network: VPC "+vpc)
	} else {
		det = append(det, "network: the default VPC for "+l.Region)
	}
	if l.Tag != "" {
		det = append(det, "targets: every droplet tagged "+l.Tag)
	} else {
		det = append(det, "targets: droplets "+strings.Join(l.Droplets, ", "))
	}
	for _, f := range l.rules {
		det = append(det, "forward "+lbRuleSpec(f)+"  =  "+describeLBRule(f))
	}
	if l.health != nil {
		det = append(det, "health check: "+lbHealthSpec(l.health))
	} else {
		pl.warns = append(pl.warns, fmt.Sprintf("load balancer %q has no health_check: DigitalOcean applies its own default check", l.Name))
	}
	if l.Project != "" {
		det = append(det, "put it in the project "+l.Project)
	}
	det = append(det, lbCostDetail)
	pl.item("load_balancer", l.Name, actCreate, "", det...)
	pl.unknown = append(pl.unknown, "load_balancer "+l.Name)
	pl.willMake["load_balancer:"+l.Name] = true
	spec := l
	pl.op("load_balancer", l.Name, fmt.Sprintf("create load balancer %q", final), func() error { return pl.x.createLB(spec, final, vpc) })
	pl.x.nextSteps = append(pl.x.nextSteps, fmt.Sprintf("docli lb get %s   (a new load balancer takes a few minutes to become active and get its IP)", l.Name))
}

// planOrphansM4 lists the projects, volumes and load balancers docli created that the file no longer lists.
func (pl *planner) planOrphansM4() {
	s := pl.s
	const note = "docli created it and the file does not list it"
	for _, p := range pl.snap.projects {
		if p.IsDefault || !hasManagedPrefix(p.Name) || nameIn(s.Projects, func(x StackProject) string { return ensureNamePrefix(x.Name) }, p.Name) {
			continue
		}
		it := pl.item("project", strings.TrimPrefix(p.Name, namePrefix), actOrphaned, p.ID, note)
		id := p.ID
		pl.pruneable = append(pl.pruneable, pruneCand{item: it, section: "projects", id: p.ID, label: fmt.Sprintf("delete project %q", p.Name), do: func() error { return pl.x.deleteProject(id) }})
	}
	for _, v := range pl.snap.volumes {
		if !hasTag(v.Tags, managedTag) || otherStackTag(v.Tags, pl.stackTag()) {
			continue
		}
		listed := false
		for _, sv := range s.Volumes {
			listed = listed || (sv.Name == v.Name && sv.Region == volumeRegion(v))
		}
		if listed {
			continue
		}
		it := pl.item("volume", v.Name, actOrphaned, v.ID, note)
		id := v.ID
		pl.pruneable = append(pl.pruneable, pruneCand{item: it, section: "volumes", id: v.ID, typed: v.Name, data: true,
			label: fmt.Sprintf("delete volume %q", v.Name), do: func() error { return pl.x.deleteVolume(id) }})
		if len(v.DropletIDs) > 0 {
			it.Details = append(it.Details, "it is attached to "+pl.dropletNames(v.DropletIDs)+", so it cannot be deleted until it is detached")
		}
	}
	for _, l := range pl.snap.lbs {
		if !hasManagedPrefix(l.Name) || nameIn(s.LBs, func(x StackLB) string { return ensureNamePrefix(x.Name) }, l.Name) {
			continue
		}
		it := pl.item("load_balancer", strings.TrimPrefix(l.Name, namePrefix), actOrphaned, l.ID, note)
		id := l.ID
		pl.pruneable = append(pl.pruneable, pruneCand{item: it, section: "load_balancers", id: l.ID, typed: l.Name,
			label: fmt.Sprintf("delete load balancer %q", l.Name), do: func() error { return pl.x.deleteLB(id) }})
	}
}

// pruneBlockM4 says why a project, volume or load balancer orphan must not be deleted, or "".
func (pl *planner) pruneBlockM4(c pruneCand) string {
	switch c.item.Kind {
	case "project":
		res, err := pl.r.Cloud.ListProjectResources(pl.r.Ctx, c.id)
		if err != nil {
			return "cannot list its resources: " + err.Error()
		}
		left := 0
		for _, x := range res {
			if !pl.pruning[x.URN] { // a resource that is itself being pruned first will be gone by then
				left++
			}
		}
		if left > 0 {
			return fmt.Sprintf("it still holds %d resource(s)", left)
		}
	case "volume":
		for _, v := range pl.snap.volumes {
			if v.ID == c.id && len(v.DropletIDs) > 0 {
				return "it is attached to " + pl.dropletNames(v.DropletIDs) + "; detach it first"
			}
		}
	}
	return ""
}

func (pl *planner) notManagedM4() []NotManagedItem {
	var out []NotManagedItem
	s := pl.s
	for _, p := range pl.snap.projects {
		if !hasManagedPrefix(p.Name) && !p.IsDefault && !nameIn(s.Projects, func(x StackProject) string { return x.Name }, p.Name) {
			out = append(out, NotManagedItem{"project", p.Name, p.ID})
		}
	}
	for _, v := range pl.snap.volumes {
		if !hasTag(v.Tags, managedTag) && !nameIn(s.Volumes, func(x StackVolume) string { return x.Name }, v.Name) {
			out = append(out, NotManagedItem{"volume", v.Name, v.ID})
		}
	}
	for _, l := range pl.snap.lbs {
		if !hasManagedPrefix(l.Name) && !nameIn(s.LBs, func(x StackLB) string { return x.Name }, l.Name) {
			out = append(out, NotManagedItem{"load_balancer", l.Name, l.ID})
		}
	}
	return out
}
