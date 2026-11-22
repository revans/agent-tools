package main

import (
	"sort"
	"time"

	"github.com/digitalocean/godo"
)

// JSON for milestone 2. As before every field is always present. Nothing here has a field for a password or a
// connection URI, so no code path can print one by accident.

func fmtTime(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.UTC().Format("2006-01-02T15:04:05Z")
}

type domainJSON struct {
	Name    string `json:"name"`
	TTL     int    `json:"ttl"`
	Managed bool   `json:"managed"`
}

type recordJSON struct {
	ID       int    `json:"id"`
	Domain   string `json:"domain"`
	Type     string `json:"type"`
	Name     string `json:"name"`
	FQDN     string `json:"fqdn"`
	Data     string `json:"data"`
	TTL      int    `json:"ttl"`
	Priority int    `json:"priority"`
	Flags    int    `json:"flags"`
	Tag      string `json:"tag"`
	Managed  bool   `json:"managed"`
}

type domainDetailJSON struct {
	domainJSON
	Records []recordJSON `json:"records"`
}

func recordView(domain string, r godo.DomainRecord, managed bool) recordJSON {
	return recordJSON{r.ID, domain, r.Type, r.Name, fqdn(domain, r.Name), r.Data, r.TTL, r.Priority, r.Flags, r.Tag, managed}
}

// connJSON is where to connect. There is deliberately no password or uri field.
type connJSON struct {
	Host     string `json:"host"`
	Port     int    `json:"port"`
	Database string `json:"database"`
	User     string `json:"user"`
	SSLMode  string `json:"ssl_mode"`
}

func connView(c *godo.DatabaseConnection) connJSON {
	if c == nil {
		return connJSON{}
	}
	mode := "disable"
	if c.SSL {
		mode = "require"
	}
	return connJSON{c.Host, c.Port, c.Database, c.User, mode}
}

type dbJSON struct {
	ID                string   `json:"id"`
	Name              string   `json:"name"`
	Engine            string   `json:"engine"`
	Version           string   `json:"version"`
	Status            string   `json:"status"`
	Managed           bool     `json:"managed"`
	Region            string   `json:"region"`
	SizeSlug          string   `json:"size_slug"`
	NumNodes          int      `json:"num_nodes"`
	VPCID             string   `json:"vpc_id"`
	Tags              []string `json:"tags"`
	Connection        connJSON `json:"connection"`
	PrivateConnection connJSON `json:"private_connection"`
	StorageMiB        uint64   `json:"storage_size_mib"`
	CreatedAt         string   `json:"created_at"`
}

func dbView(d godo.Database) dbJSON {
	return dbJSON{d.ID, d.Name, d.EngineSlug, d.VersionSlug, d.Status, hasTag(d.Tags, managedTag), d.RegionSlug, d.SizeSlug, d.NumNodes,
		d.PrivateNetworkUUID, nonNil(d.Tags), connView(d.Connection), connView(d.PrivateConnection), d.StorageSizeMib, fmtTime(d.CreatedAt)}
}

type sourceJSON struct {
	UUID  string `json:"uuid"`
	Type  string `json:"type"`
	Value string `json:"value"`
	Name  string `json:"name"` // a droplet's name when it is one of this account's droplets
}

type dbDetailJSON struct {
	dbJSON
	TrustedSources []sourceJSON `json:"trusted_sources"`
}

type dbUserJSON struct {
	Name string `json:"name"`
	Role string `json:"role"`
}

type dbNameJSON struct {
	Name string `json:"name"`
}

type poolJSON struct {
	Name        string `json:"name"`
	User        string `json:"user"`
	Database    string `json:"database"`
	Mode        string `json:"mode"`
	Size        int    `json:"size"`
	Host        string `json:"host"`
	PrivateHost string `json:"private_host"`
	Port        int    `json:"port"`
}

func poolView(p godo.DatabasePool) poolJSON {
	c, pc := connView(p.Connection), connView(p.PrivateConnection)
	return poolJSON{p.Name, p.User, p.Database, p.Mode, p.Size, c.Host, pc.Host, c.Port}
}

type backupJSON struct {
	CreatedAt     string  `json:"created_at"`
	SizeGigabytes float64 `json:"size_gigabytes"`
}

type connectionJSON struct {
	Name         string `json:"name"`
	Host         string `json:"host"`
	Port         int    `json:"port"`
	PrivateHost  string `json:"private_host"`
	PrivatePort  int    `json:"private_port"`
	Database     string `json:"database"`
	User         string `json:"user"`
	SSLMode      string `json:"ssl_mode"`
	PasswordFile string `json:"password_file"`
}

// sourceKey orders and compares trusted sources.
func sourceKey(s godo.DatabaseFirewallRule) string { return s.Type + ":" + s.Value }

func sortedSources(rs []godo.DatabaseFirewallRule) []godo.DatabaseFirewallRule {
	out := append([]godo.DatabaseFirewallRule(nil), rs...)
	sort.Slice(out, func(i, j int) bool { return sourceKey(out[i]) < sourceKey(out[j]) })
	return out
}
