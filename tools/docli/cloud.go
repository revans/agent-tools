package main

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/digitalocean/godo"
)

// Cloud is the thin slice of the DigitalOcean API the commands use. Lists are fully paged. Tests use the
// godo-backed implementation against a fake server; later stack planning can diff through the same interface.
type Cloud interface {
	Account(ctx context.Context) (*godo.Account, error)
	ProbeReadAccess(ctx context.Context) []Probe

	ListSizes(ctx context.Context) ([]godo.Size, error)
	ListRegions(ctx context.Context) ([]godo.Region, error)
	ListImages(ctx context.Context, kind string) ([]godo.Image, error) // distribution, application or custom
	GetImageBySlug(ctx context.Context, slug string) (*godo.Image, error)

	ListKeys(ctx context.Context) ([]godo.Key, error)
	CreateKey(ctx context.Context, name, publicKey string) (*godo.Key, error)
	DeleteKey(ctx context.Context, id int) error

	ListVPCs(ctx context.Context) ([]*godo.VPC, error)
	CreateVPC(ctx context.Context, req *godo.VPCCreateRequest) (*godo.VPC, error)
	DeleteVPC(ctx context.Context, id string) error
	ListVPCMembers(ctx context.Context, id string) ([]*godo.VPCMember, error)

	ListFirewalls(ctx context.Context) ([]godo.Firewall, error)
	CreateFirewall(ctx context.Context, req *godo.FirewallRequest) (*godo.Firewall, error)
	DeleteFirewall(ctx context.Context, id string) error
	AddFirewallRules(ctx context.Context, id string, rr *godo.FirewallRulesRequest) error
	RemoveFirewallRules(ctx context.Context, id string, rr *godo.FirewallRulesRequest) error
	AddFirewallDroplets(ctx context.Context, id string, droplets ...int) error
	RemoveFirewallDroplets(ctx context.Context, id string, droplets ...int) error
	AddFirewallTags(ctx context.Context, id string, tags ...string) error
	RemoveFirewallTags(ctx context.Context, id string, tags ...string) error

	ListDroplets(ctx context.Context, tag string) ([]godo.Droplet, error)
	GetDroplet(ctx context.Context, id int) (*godo.Droplet, error)
	CreateDroplet(ctx context.Context, req *godo.DropletCreateRequest) (*godo.Droplet, error)
	DeleteDroplet(ctx context.Context, id int) error
	DropletAction(ctx context.Context, id int, action string) error // power_off, power_on, reboot

	EnsureTag(ctx context.Context, name string) error
	// Milestone 3: adding a tag to a droplet or database cluster (adopt). resourceType is "droplet" or "database".
	TagResource(ctx context.Context, tag, id, resourceType string) error

	// Milestone 2: DNS.
	ListDomains(ctx context.Context) ([]godo.Domain, error)
	CreateDomain(ctx context.Context, name string) (*godo.Domain, error)
	DeleteDomain(ctx context.Context, name string) error
	ListDomainRecords(ctx context.Context, domain string) ([]godo.DomainRecord, error)
	CreateDomainRecord(ctx context.Context, domain string, req *godo.DomainRecordEditRequest) (*godo.DomainRecord, error)
	EditDomainRecord(ctx context.Context, domain string, id int, req *godo.DomainRecordEditRequest) (*godo.DomainRecord, error)
	DeleteDomainRecord(ctx context.Context, domain string, id int) error

	// Milestone 2: managed databases. Passwords arrive in some of these results; callers must never print them.
	ListDatabases(ctx context.Context) ([]godo.Database, error)
	GetDatabase(ctx context.Context, id string) (*godo.Database, error)
	CreateDatabase(ctx context.Context, req *godo.DatabaseCreateRequest) (*godo.Database, error)
	DeleteDatabase(ctx context.Context, id string) error
	ResizeDatabase(ctx context.Context, id string, req *godo.DatabaseResizeRequest) error
	DatabaseOptions(ctx context.Context) (*godo.DatabaseOptions, error)
	ListDatabaseBackups(ctx context.Context, id string) ([]godo.DatabaseBackup, error)
	ListDatabaseUsers(ctx context.Context, id string) ([]godo.DatabaseUser, error)
	CreateDatabaseUser(ctx context.Context, id, name string) (*godo.DatabaseUser, error)
	DeleteDatabaseUser(ctx context.Context, id, name string) error
	ListDatabaseDBs(ctx context.Context, id string) ([]godo.DatabaseDB, error)
	CreateDatabaseDB(ctx context.Context, id, name string) (*godo.DatabaseDB, error)
	DeleteDatabaseDB(ctx context.Context, id, name string) error
	ListDatabasePools(ctx context.Context, id string) ([]godo.DatabasePool, error)
	CreateDatabasePool(ctx context.Context, id string, req *godo.DatabaseCreatePoolRequest) (*godo.DatabasePool, error)
	DeleteDatabasePool(ctx context.Context, id, name string) error
	GetDatabaseFirewall(ctx context.Context, id string) ([]godo.DatabaseFirewallRule, error)
	SetDatabaseFirewall(ctx context.Context, id string, rules []*godo.DatabaseFirewallRule) error

	// Milestone 4: projects, block storage volumes, reserved IPs, load balancers, Spaces keys. A Spaces key's secret
	// arrives only in the CreateSpacesKey answer; callers must never print it.
	ListProjects(ctx context.Context) ([]godo.Project, error)
	CreateProject(ctx context.Context, req *godo.CreateProjectRequest) (*godo.Project, error)
	DeleteProject(ctx context.Context, id string) error
	ListProjectResources(ctx context.Context, id string) ([]godo.ProjectResource, error)
	AssignProjectResources(ctx context.Context, id string, urns ...string) error

	ListVolumes(ctx context.Context) ([]godo.Volume, error)
	CreateVolume(ctx context.Context, req *godo.VolumeCreateRequest) (*godo.Volume, error)
	DeleteVolume(ctx context.Context, id string) error
	AttachVolume(ctx context.Context, id string, dropletID int) error
	DetachVolume(ctx context.Context, id string, dropletID int) error
	ResizeVolume(ctx context.Context, id string, sizeGB int, region string) error

	ListReservedIPs(ctx context.Context) ([]godo.ReservedIP, error)
	CreateReservedIP(ctx context.Context, req *godo.ReservedIPCreateRequest) (*godo.ReservedIP, error)
	DeleteReservedIP(ctx context.Context, ip string) error
	AssignReservedIP(ctx context.Context, ip string, dropletID int) error
	UnassignReservedIP(ctx context.Context, ip string) error

	ListLoadBalancers(ctx context.Context) ([]godo.LoadBalancer, error)
	CreateLoadBalancer(ctx context.Context, req *godo.LoadBalancerRequest) (*godo.LoadBalancer, error)
	DeleteLoadBalancer(ctx context.Context, id string) error

	ListSpacesKeys(ctx context.Context) ([]*godo.SpacesKey, error)
	CreateSpacesKey(ctx context.Context, req *godo.SpacesKeyCreateRequest) (*godo.SpacesKey, error)
	DeleteSpacesKey(ctx context.Context, accessKey string) error
}

// Probe is the result of one read-access check made by `token check`.
type Probe struct {
	Name   string
	Status string // "allowed", "denied" or "unknown"
	Detail string
}

type godoCloud struct{ c *godo.Client }

// newGodoCloud builds the real client: auth, then retries for reads, then (with --debug) a request log.
func (a *App) newGodoCloud(token string, g Globals) (Cloud, error) {
	var base http.RoundTripper = http.DefaultTransport
	rt := &retryTransport{next: &authTransport{token: token, next: base}, sleep: a.Sleep, now: a.Now,
		attempts: 4, base: 500 * time.Millisecond, max: 10 * time.Second}
	if g.Debug {
		rt.debug = &redactWriter{w: a.Stderr, token: token}
	}
	hc := &http.Client{Transport: rt, Timeout: g.Timeout}
	opts := []godo.ClientOpt{godo.SetUserAgent("docli/" + version)}
	if a.BaseURL != "" {
		opts = append(opts, godo.SetBaseURL(a.BaseURL))
	}
	c, err := godo.New(hc, opts...)
	if err != nil {
		return nil, err
	}
	return &godoCloud{c}, nil
}

type listFn[T any] func(opt *godo.ListOptions) ([]T, *godo.Response, error)

// paged follows every page of a list.
func paged[T any](fn listFn[T]) ([]T, error) {
	var all []T
	opt := &godo.ListOptions{Page: 1, PerPage: 200}
	for i := 0; i < 1000; i++ {
		items, resp, err := fn(opt)
		if err != nil {
			return nil, err
		}
		all = append(all, items...)
		if resp == nil || resp.Links == nil || resp.Links.IsLastPage() {
			return all, nil
		}
		p, err := resp.Links.CurrentPage()
		if err != nil {
			return nil, err
		}
		opt = &godo.ListOptions{Page: p + 1, PerPage: 200}
	}
	return nil, errors.New("stopped after 1000 pages; the list does not seem to end")
}

func statusOf(err error) int {
	var er *godo.ErrorResponse
	if errors.As(err, &er) && er.Response != nil {
		return er.Response.StatusCode
	}
	return 0
}

func isNotFound(err error) bool { return statusOf(err) == http.StatusNotFound }

func (g *godoCloud) Account(ctx context.Context) (*godo.Account, error) {
	a, _, err := g.c.Account.Get(ctx)
	return a, err
}

func (g *godoCloud) ProbeReadAccess(ctx context.Context) []Probe {
	one := &godo.ListOptions{Page: 1, PerPage: 1}
	checks := []struct {
		name string
		fn   func() error
	}{
		{"droplets", func() error { _, _, e := g.c.Droplets.List(ctx, one); return e }},
		{"ssh keys", func() error { _, _, e := g.c.Keys.List(ctx, one); return e }},
		{"vpcs", func() error { _, _, e := g.c.VPCs.List(ctx, one); return e }},
		{"firewalls", func() error { _, _, e := g.c.Firewalls.List(ctx, one); return e }},
		{"tags", func() error { _, _, e := g.c.Tags.List(ctx, one); return e }},
		{"sizes", func() error { _, _, e := g.c.Sizes.List(ctx, one); return e }},
		{"domains", func() error { _, _, e := g.c.Domains.List(ctx, one); return e }},
		{"databases", func() error { _, _, e := g.c.Databases.List(ctx, one); return e }},
	}
	var out []Probe
	for _, c := range checks {
		err := c.fn()
		switch s := statusOf(err); {
		case err == nil:
			out = append(out, Probe{c.name, "allowed", "read works"})
		case s == 401 || s == 403:
			out = append(out, Probe{c.name, "denied", fmt.Sprintf("the API answered %d", s)})
		default:
			out = append(out, Probe{c.name, "unknown", "the check failed for another reason"})
		}
	}
	return out
}

func (g *godoCloud) ListSizes(ctx context.Context) ([]godo.Size, error) {
	return paged(func(o *godo.ListOptions) ([]godo.Size, *godo.Response, error) { return g.c.Sizes.List(ctx, o) })
}
func (g *godoCloud) ListRegions(ctx context.Context) ([]godo.Region, error) {
	return paged(func(o *godo.ListOptions) ([]godo.Region, *godo.Response, error) { return g.c.Regions.List(ctx, o) })
}
func (g *godoCloud) ListImages(ctx context.Context, kind string) ([]godo.Image, error) {
	return paged(func(o *godo.ListOptions) ([]godo.Image, *godo.Response, error) {
		switch kind {
		case "application":
			return g.c.Images.ListApplication(ctx, o)
		case "custom":
			return g.c.Images.ListUser(ctx, o)
		default:
			return g.c.Images.ListDistribution(ctx, o)
		}
	})
}
func (g *godoCloud) GetImageBySlug(ctx context.Context, slug string) (*godo.Image, error) {
	i, _, err := g.c.Images.GetBySlug(ctx, slug)
	return i, err
}

func (g *godoCloud) ListKeys(ctx context.Context) ([]godo.Key, error) {
	return paged(func(o *godo.ListOptions) ([]godo.Key, *godo.Response, error) { return g.c.Keys.List(ctx, o) })
}
func (g *godoCloud) CreateKey(ctx context.Context, name, pub string) (*godo.Key, error) {
	k, _, err := g.c.Keys.Create(ctx, &godo.KeyCreateRequest{Name: name, PublicKey: pub})
	return k, err
}
func (g *godoCloud) DeleteKey(ctx context.Context, id int) error {
	_, err := g.c.Keys.DeleteByID(ctx, id)
	return err
}

func (g *godoCloud) ListVPCs(ctx context.Context) ([]*godo.VPC, error) {
	return paged(func(o *godo.ListOptions) ([]*godo.VPC, *godo.Response, error) { return g.c.VPCs.List(ctx, o) })
}
func (g *godoCloud) CreateVPC(ctx context.Context, req *godo.VPCCreateRequest) (*godo.VPC, error) {
	v, _, err := g.c.VPCs.Create(ctx, req)
	return v, err
}
func (g *godoCloud) DeleteVPC(ctx context.Context, id string) error {
	_, err := g.c.VPCs.Delete(ctx, id)
	return err
}
func (g *godoCloud) ListVPCMembers(ctx context.Context, id string) ([]*godo.VPCMember, error) {
	return paged(func(o *godo.ListOptions) ([]*godo.VPCMember, *godo.Response, error) {
		return g.c.VPCs.ListMembers(ctx, id, nil, o)
	})
}

func (g *godoCloud) ListFirewalls(ctx context.Context) ([]godo.Firewall, error) {
	return paged(func(o *godo.ListOptions) ([]godo.Firewall, *godo.Response, error) { return g.c.Firewalls.List(ctx, o) })
}
func (g *godoCloud) CreateFirewall(ctx context.Context, req *godo.FirewallRequest) (*godo.Firewall, error) {
	f, _, err := g.c.Firewalls.Create(ctx, req)
	return f, err
}
func (g *godoCloud) DeleteFirewall(ctx context.Context, id string) error {
	_, err := g.c.Firewalls.Delete(ctx, id)
	return err
}
func (g *godoCloud) AddFirewallRules(ctx context.Context, id string, rr *godo.FirewallRulesRequest) error {
	_, err := g.c.Firewalls.AddRules(ctx, id, rr)
	return err
}
func (g *godoCloud) RemoveFirewallRules(ctx context.Context, id string, rr *godo.FirewallRulesRequest) error {
	_, err := g.c.Firewalls.RemoveRules(ctx, id, rr)
	return err
}
func (g *godoCloud) AddFirewallDroplets(ctx context.Context, id string, d ...int) error {
	_, err := g.c.Firewalls.AddDroplets(ctx, id, d...)
	return err
}
func (g *godoCloud) RemoveFirewallDroplets(ctx context.Context, id string, d ...int) error {
	_, err := g.c.Firewalls.RemoveDroplets(ctx, id, d...)
	return err
}
func (g *godoCloud) AddFirewallTags(ctx context.Context, id string, t ...string) error {
	_, err := g.c.Firewalls.AddTags(ctx, id, t...)
	return err
}
func (g *godoCloud) RemoveFirewallTags(ctx context.Context, id string, t ...string) error {
	_, err := g.c.Firewalls.RemoveTags(ctx, id, t...)
	return err
}

func (g *godoCloud) ListDroplets(ctx context.Context, tag string) ([]godo.Droplet, error) {
	return paged(func(o *godo.ListOptions) ([]godo.Droplet, *godo.Response, error) {
		if tag != "" {
			return g.c.Droplets.ListByTag(ctx, tag, o)
		}
		return g.c.Droplets.List(ctx, o)
	})
}
func (g *godoCloud) GetDroplet(ctx context.Context, id int) (*godo.Droplet, error) {
	d, _, err := g.c.Droplets.Get(ctx, id)
	return d, err
}
func (g *godoCloud) CreateDroplet(ctx context.Context, req *godo.DropletCreateRequest) (*godo.Droplet, error) {
	d, _, err := g.c.Droplets.Create(ctx, req) // POST: never retried
	return d, err
}
func (g *godoCloud) DeleteDroplet(ctx context.Context, id int) error {
	_, err := g.c.Droplets.Delete(ctx, id)
	return err
}
func (g *godoCloud) DropletAction(ctx context.Context, id int, action string) error {
	var err error
	switch action {
	case "power_off":
		_, _, err = g.c.DropletActions.PowerOff(ctx, id)
	case "power_on":
		_, _, err = g.c.DropletActions.PowerOn(ctx, id)
	case "reboot":
		_, _, err = g.c.DropletActions.Reboot(ctx, id)
	default:
		err = fmt.Errorf("unknown droplet action %q", action)
	}
	return err
}

// EnsureTag creates the tag when it does not exist yet.
func (g *godoCloud) EnsureTag(ctx context.Context, name string) error {
	_, _, err := g.c.Tags.Get(ctx, name)
	if err == nil {
		return nil
	}
	if !isNotFound(err) {
		return err
	}
	_, _, err = g.c.Tags.Create(ctx, &godo.TagCreateRequest{Name: name})
	return err
}

// TagResource adds a tag to one resource. It never removes any tag.
func (g *godoCloud) TagResource(ctx context.Context, tag, id, resourceType string) error {
	_, err := g.c.Tags.TagResources(ctx, tag, &godo.TagResourcesRequest{Resources: []godo.Resource{{ID: id, Type: godo.ResourceType(resourceType)}}})
	return err
}
