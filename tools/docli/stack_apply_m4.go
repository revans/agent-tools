package main

import (
	"fmt"

	"github.com/digitalocean/godo"
)

// Apply steps for projects, volumes and load balancers. Each is one API write run once, never retried; the
// manifest is updated after each. `project:` on a volume or load balancer is applied when it is created.

func (x *applyState) assignProject(name, ref, kind, urn string) error {
	if ref == "" {
		return nil
	}
	id, ok := x.projectID[ref]
	if !ok {
		return fmt.Errorf("the project %q does not exist, so the %s %q was not put in it", ref, kind, name)
	}
	if err := x.r.Cloud.AssignProjectResources(x.r.Ctx, id, urn); err != nil {
		return fmt.Errorf("the %s %q was created, but it could not be put in the project %q: %v. Finish with: docli project assign %s %s:%s", kind, name, ref, err, shellWord(ref), projectKind(kind), shellWord(name))
	}
	return nil
}

func (x *applyState) createProject(p StackProject, final string) error {
	got, err := x.r.Cloud.CreateProject(x.r.Ctx, &godo.CreateProjectRequest{Name: final, Description: p.Description, Purpose: p.Purpose, Environment: p.Environment})
	if err != nil {
		return madeOrUnknown("project", err, "docli project list")
	}
	x.projectID[p.Name] = got.ID
	x.r.updateManifest(x.mo, false, change{Section: "projects", ID: got.ID})
	return nil
}

func (x *applyState) createVolume(v StackVolume, tags []string) error {
	if err := x.ensureTags(tags, "volume"); err != nil {
		return err
	}
	got, err := x.r.Cloud.CreateVolume(x.r.Ctx, &godo.VolumeCreateRequest{Region: v.Region, Name: v.Name, Description: v.Description, SizeGigaBytes: int64(v.SizeGB),
		FilesystemType: v.FilesystemType, Tags: tags})
	if err != nil {
		return madeOrUnknown("volume", err, "docli volume list --region "+v.Region)
	}
	x.volID[v.Name] = got.ID
	x.r.updateManifest(x.mo, false, change{Section: "volumes", ID: got.ID})
	return x.assignProject(v.Name, v.Project, "volume", got.URN())
}

func (x *applyState) attachVolume(name, droplet string) error {
	vid, ok := x.volID[name]
	if !ok {
		return fmt.Errorf("volume %q does not exist, so it was not attached", name)
	}
	did, ok := x.dropID[droplet]
	if !ok {
		return fmt.Errorf("droplet %q does not exist, so volume %q was not attached to it", droplet, name)
	}
	if err := x.r.Cloud.AttachVolume(x.r.Ctx, vid, did); err != nil {
		return err
	}
	x.r.updateManifest(x.mo, false, change{Section: "volumes", ID: vid})
	return nil
}

func (x *applyState) resizeVolume(name, id string, size int, region string) error {
	if err := x.r.Cloud.ResizeVolume(x.r.Ctx, id, size, region); err != nil {
		return err
	}
	x.r.updateManifest(x.mo, false, change{Section: "volumes", ID: id})
	return nil
}

func (x *applyState) createLB(l StackLB, final, vpc string) error {
	req := &godo.LoadBalancerRequest{Name: final, Algorithm: l.Algorithm, Region: l.Region, SizeSlug: l.Size, ForwardingRules: l.rules, HealthCheck: l.health,
		Tag: l.Tag, RedirectHttpToHttps: l.RedirectHTTP}
	if vpc != "" {
		id, ok := x.vpcID[vpc]
		if !ok {
			return fmt.Errorf("the VPC %q does not exist yet, so load balancer %q was not created", vpc, l.Name)
		}
		req.VPCUUID = id
	}
	for _, d := range l.Droplets {
		id, ok := x.dropID[d]
		if !ok {
			return fmt.Errorf("droplet %q does not exist yet, so load balancer %q was not created", d, l.Name)
		}
		req.DropletIDs = append(req.DropletIDs, id)
	}
	got, err := x.r.Cloud.CreateLoadBalancer(x.r.Ctx, req)
	if err != nil {
		return madeOrUnknown("load balancer", err, "docli lb list")
	}
	x.lbID[l.Name] = got.ID
	x.r.updateManifest(x.mo, false, change{Section: "load_balancers", ID: got.ID})
	return x.assignProject(l.Name, l.Project, "load balancer", got.URN())
}

// ---- deletions: only ever reached through --prune; ownership is checked again at the moment of deletion ----

func (x *applyState) deleteProject(id string) error {
	ps, err := x.r.Cloud.ListProjects(x.r.Ctx)
	if err != nil {
		return err
	}
	for _, p := range ps {
		if p.ID == id {
			if !hasManagedPrefix(p.Name) || p.IsDefault {
				return refusedf("refusing to delete project %q: it is not a docli project. Nothing was deleted", p.Name)
			}
			res, err := x.r.Cloud.ListProjectResources(x.r.Ctx, id)
			if err != nil {
				return err
			}
			if len(res) > 0 {
				return refusedf("refusing to delete project %q: it still holds %d resource(s). Nothing was deleted", p.Name, len(res))
			}
		}
	}
	if err := x.r.Cloud.DeleteProject(x.r.Ctx, id); err != nil {
		return err
	}
	x.r.updateManifest(x.mo, false, change{Section: "projects", ID: id})
	return nil
}

func (x *applyState) deleteVolume(id string) error {
	vols, err := x.r.Cloud.ListVolumes(x.r.Ctx)
	if err != nil {
		return err
	}
	for _, v := range vols {
		if v.ID != id {
			continue
		}
		if !hasTag(v.Tags, managedTag) {
			return refusedf("refusing to delete volume %q: it no longer carries the tag %s. Nothing was deleted", v.Name, managedTag)
		}
		if len(v.DropletIDs) > 0 {
			return refusedf("refusing to delete volume %q: it is attached to a droplet. Nothing was deleted", v.Name)
		}
	}
	if err := x.r.Cloud.DeleteVolume(x.r.Ctx, id); err != nil {
		return err
	}
	x.r.updateManifest(x.mo, false, change{Section: "volumes", ID: id})
	return nil
}

func (x *applyState) deleteLB(id string) error {
	lbs, err := x.r.Cloud.ListLoadBalancers(x.r.Ctx)
	if err != nil {
		return err
	}
	for _, l := range lbs {
		if l.ID == id && !hasManagedPrefix(l.Name) {
			return refusedf("refusing to delete load balancer %q: its name no longer starts with %s. Nothing was deleted", l.Name, namePrefix)
		}
	}
	if err := x.r.Cloud.DeleteLoadBalancer(x.r.Ctx, id); err != nil {
		return err
	}
	x.r.updateManifest(x.mo, false, change{Section: "load_balancers", ID: id})
	return nil
}
