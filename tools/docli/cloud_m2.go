package main

import (
	"context"
	"errors"

	"github.com/digitalocean/godo"
)

// godo only follows pages where a response type carries "links", and none of its database list types do. A full
// page might therefore be only the first of several. docli refuses to act on a list that might be partial.
const noLinksPage = 200

func pagedNoLinks[T any](fn listFn[T]) ([]T, error) {
	items, _, err := fn(&godo.ListOptions{Page: 1, PerPage: noLinksPage})
	if err != nil {
		return nil, err
	}
	if len(items) >= noLinksPage {
		return nil, errors.New("DigitalOcean returned a full page of 200 items and this list cannot be paged, so it may be incomplete; docli will not act on a partial list")
	}
	return items, nil
}

// Milestone 2 calls: DNS and managed databases. Lists are paged; only GETs are ever retried (see retry.go).

func (g *godoCloud) ListDomains(ctx context.Context) ([]godo.Domain, error) {
	return paged(func(o *godo.ListOptions) ([]godo.Domain, *godo.Response, error) { return g.c.Domains.List(ctx, o) })
}
func (g *godoCloud) CreateDomain(ctx context.Context, name string) (*godo.Domain, error) {
	d, _, err := g.c.Domains.Create(ctx, &godo.DomainCreateRequest{Name: name})
	return d, err
}
func (g *godoCloud) DeleteDomain(ctx context.Context, name string) error {
	_, err := g.c.Domains.Delete(ctx, name)
	return err
}
func (g *godoCloud) ListDomainRecords(ctx context.Context, domain string) ([]godo.DomainRecord, error) {
	return paged(func(o *godo.ListOptions) ([]godo.DomainRecord, *godo.Response, error) {
		return g.c.Domains.Records(ctx, domain, o)
	})
}
func (g *godoCloud) CreateDomainRecord(ctx context.Context, domain string, req *godo.DomainRecordEditRequest) (*godo.DomainRecord, error) {
	r, _, err := g.c.Domains.CreateRecord(ctx, domain, req)
	return r, err
}
func (g *godoCloud) EditDomainRecord(ctx context.Context, domain string, id int, req *godo.DomainRecordEditRequest) (*godo.DomainRecord, error) {
	r, _, err := g.c.Domains.EditRecord(ctx, domain, id, req)
	return r, err
}
func (g *godoCloud) DeleteDomainRecord(ctx context.Context, domain string, id int) error {
	_, err := g.c.Domains.DeleteRecord(ctx, domain, id)
	return err
}

func (g *godoCloud) ListDatabases(ctx context.Context) ([]godo.Database, error) {
	return pagedNoLinks(func(o *godo.ListOptions) ([]godo.Database, *godo.Response, error) { return g.c.Databases.List(ctx, o) })
}
func (g *godoCloud) GetDatabase(ctx context.Context, id string) (*godo.Database, error) {
	d, _, err := g.c.Databases.Get(ctx, id)
	return d, err
}
func (g *godoCloud) CreateDatabase(ctx context.Context, req *godo.DatabaseCreateRequest) (*godo.Database, error) {
	d, _, err := g.c.Databases.Create(ctx, req) // POST: never retried
	return d, err
}
func (g *godoCloud) DeleteDatabase(ctx context.Context, id string) error {
	_, err := g.c.Databases.Delete(ctx, id)
	return err
}
func (g *godoCloud) ResizeDatabase(ctx context.Context, id string, req *godo.DatabaseResizeRequest) error {
	_, err := g.c.Databases.Resize(ctx, id, req)
	return err
}
func (g *godoCloud) DatabaseOptions(ctx context.Context) (*godo.DatabaseOptions, error) {
	o, _, err := g.c.Databases.ListOptions(ctx)
	return o, err
}
func (g *godoCloud) ListDatabaseBackups(ctx context.Context, id string) ([]godo.DatabaseBackup, error) {
	return pagedNoLinks(func(o *godo.ListOptions) ([]godo.DatabaseBackup, *godo.Response, error) {
		return g.c.Databases.ListBackups(ctx, id, o)
	})
}
func (g *godoCloud) ListDatabaseUsers(ctx context.Context, id string) ([]godo.DatabaseUser, error) {
	return pagedNoLinks(func(o *godo.ListOptions) ([]godo.DatabaseUser, *godo.Response, error) {
		return g.c.Databases.ListUsers(ctx, id, o)
	})
}
func (g *godoCloud) CreateDatabaseUser(ctx context.Context, id, name string) (*godo.DatabaseUser, error) {
	u, _, err := g.c.Databases.CreateUser(ctx, id, &godo.DatabaseCreateUserRequest{Name: name})
	return u, err
}
func (g *godoCloud) DeleteDatabaseUser(ctx context.Context, id, name string) error {
	_, err := g.c.Databases.DeleteUser(ctx, id, name)
	return err
}
func (g *godoCloud) ListDatabaseDBs(ctx context.Context, id string) ([]godo.DatabaseDB, error) {
	return pagedNoLinks(func(o *godo.ListOptions) ([]godo.DatabaseDB, *godo.Response, error) {
		return g.c.Databases.ListDBs(ctx, id, o)
	})
}
func (g *godoCloud) CreateDatabaseDB(ctx context.Context, id, name string) (*godo.DatabaseDB, error) {
	d, _, err := g.c.Databases.CreateDB(ctx, id, &godo.DatabaseCreateDBRequest{Name: name})
	return d, err
}
func (g *godoCloud) DeleteDatabaseDB(ctx context.Context, id, name string) error {
	_, err := g.c.Databases.DeleteDB(ctx, id, name)
	return err
}
func (g *godoCloud) ListDatabasePools(ctx context.Context, id string) ([]godo.DatabasePool, error) {
	return pagedNoLinks(func(o *godo.ListOptions) ([]godo.DatabasePool, *godo.Response, error) {
		return g.c.Databases.ListPools(ctx, id, o)
	})
}
func (g *godoCloud) CreateDatabasePool(ctx context.Context, id string, req *godo.DatabaseCreatePoolRequest) (*godo.DatabasePool, error) {
	p, _, err := g.c.Databases.CreatePool(ctx, id, req)
	return p, err
}
func (g *godoCloud) DeleteDatabasePool(ctx context.Context, id, name string) error {
	_, err := g.c.Databases.DeletePool(ctx, id, name)
	return err
}
func (g *godoCloud) GetDatabaseFirewall(ctx context.Context, id string) ([]godo.DatabaseFirewallRule, error) {
	r, _, err := g.c.Databases.GetFirewallRules(ctx, id)
	return r, err
}
func (g *godoCloud) SetDatabaseFirewall(ctx context.Context, id string, rules []*godo.DatabaseFirewallRule) error {
	_, err := g.c.Databases.UpdateFirewallRules(ctx, id, &godo.DatabaseUpdateFirewallRulesRequest{Rules: rules})
	return err
}
