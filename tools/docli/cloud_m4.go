package main

import (
	"context"

	"github.com/digitalocean/godo"
)

// Milestone 4 calls: projects, volumes, reserved IPs, load balancers and Spaces keys. Lists are paged; only GETs
// are retried (see retry.go). The S3 side of Spaces (buckets) is in spaces.go.

func (g *godoCloud) ListProjects(ctx context.Context) ([]godo.Project, error) {
	return paged(func(o *godo.ListOptions) ([]godo.Project, *godo.Response, error) { return g.c.Projects.List(ctx, o) })
}
func (g *godoCloud) CreateProject(ctx context.Context, req *godo.CreateProjectRequest) (*godo.Project, error) {
	p, _, err := g.c.Projects.Create(ctx, req)
	return p, err
}
func (g *godoCloud) DeleteProject(ctx context.Context, id string) error {
	_, err := g.c.Projects.Delete(ctx, id)
	return err
}
func (g *godoCloud) ListProjectResources(ctx context.Context, id string) ([]godo.ProjectResource, error) {
	return paged(func(o *godo.ListOptions) ([]godo.ProjectResource, *godo.Response, error) {
		return g.c.Projects.ListResources(ctx, id, o)
	})
}
func (g *godoCloud) AssignProjectResources(ctx context.Context, id string, urns ...string) error {
	args := make([]any, len(urns))
	for i, u := range urns {
		args[i] = u
	}
	_, _, err := g.c.Projects.AssignResources(ctx, id, args...)
	return err
}

func (g *godoCloud) ListVolumes(ctx context.Context) ([]godo.Volume, error) {
	return paged(func(o *godo.ListOptions) ([]godo.Volume, *godo.Response, error) {
		return g.c.Storage.ListVolumes(ctx, &godo.ListVolumeParams{ListOptions: o})
	})
}
func (g *godoCloud) CreateVolume(ctx context.Context, req *godo.VolumeCreateRequest) (*godo.Volume, error) {
	v, _, err := g.c.Storage.CreateVolume(ctx, req) // POST: never retried
	return v, err
}
func (g *godoCloud) DeleteVolume(ctx context.Context, id string) error {
	_, err := g.c.Storage.DeleteVolume(ctx, id)
	return err
}
func (g *godoCloud) AttachVolume(ctx context.Context, id string, dropletID int) error {
	_, _, err := g.c.StorageActions.Attach(ctx, id, dropletID)
	return err
}
func (g *godoCloud) DetachVolume(ctx context.Context, id string, dropletID int) error {
	_, _, err := g.c.StorageActions.DetachByDropletID(ctx, id, dropletID)
	return err
}
func (g *godoCloud) ResizeVolume(ctx context.Context, id string, sizeGB int, region string) error {
	_, _, err := g.c.StorageActions.Resize(ctx, id, sizeGB, region)
	return err
}

func (g *godoCloud) ListReservedIPs(ctx context.Context) ([]godo.ReservedIP, error) {
	return paged(func(o *godo.ListOptions) ([]godo.ReservedIP, *godo.Response, error) {
		return g.c.ReservedIPs.List(ctx, o)
	})
}
func (g *godoCloud) CreateReservedIP(ctx context.Context, req *godo.ReservedIPCreateRequest) (*godo.ReservedIP, error) {
	ip, _, err := g.c.ReservedIPs.Create(ctx, req) // POST: never retried
	return ip, err
}
func (g *godoCloud) DeleteReservedIP(ctx context.Context, ip string) error {
	_, err := g.c.ReservedIPs.Delete(ctx, ip)
	return err
}
func (g *godoCloud) AssignReservedIP(ctx context.Context, ip string, dropletID int) error {
	_, _, err := g.c.ReservedIPActions.Assign(ctx, ip, dropletID)
	return err
}
func (g *godoCloud) UnassignReservedIP(ctx context.Context, ip string) error {
	_, _, err := g.c.ReservedIPActions.Unassign(ctx, ip)
	return err
}

func (g *godoCloud) ListLoadBalancers(ctx context.Context) ([]godo.LoadBalancer, error) {
	return paged(func(o *godo.ListOptions) ([]godo.LoadBalancer, *godo.Response, error) {
		return g.c.LoadBalancers.List(ctx, o)
	})
}
func (g *godoCloud) CreateLoadBalancer(ctx context.Context, req *godo.LoadBalancerRequest) (*godo.LoadBalancer, error) {
	l, _, err := g.c.LoadBalancers.Create(ctx, req) // POST: never retried
	return l, err
}
func (g *godoCloud) DeleteLoadBalancer(ctx context.Context, id string) error {
	_, err := g.c.LoadBalancers.Delete(ctx, id)
	return err
}

func (g *godoCloud) ListSpacesKeys(ctx context.Context) ([]*godo.SpacesKey, error) {
	return paged(func(o *godo.ListOptions) ([]*godo.SpacesKey, *godo.Response, error) {
		return g.c.SpacesKeys.List(ctx, o)
	})
}
func (g *godoCloud) CreateSpacesKey(ctx context.Context, req *godo.SpacesKeyCreateRequest) (*godo.SpacesKey, error) {
	k, _, err := g.c.SpacesKeys.Create(ctx, req) // POST: never retried
	return k, err
}
func (g *godoCloud) DeleteSpacesKey(ctx context.Context, accessKey string) error {
	_, err := g.c.SpacesKeys.Delete(ctx, accessKey)
	return err
}
