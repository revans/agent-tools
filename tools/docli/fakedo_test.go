package main

// A fake DigitalOcean API for the tests. Nothing here talks to the real service: it is an httptest server
// that keeps accounts in memory and answers the handful of endpoints docli uses, with paging.

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/digitalocean/godo"
)

const fakeToken = "dop_v1_FAKETOKEN_s3cr3t_0123456789abcdef"

// Passwords the fake returns. No docli output may contain them (see password_test.go).
// They use the AVNS_ prefix on purpose, because docli scrubs that shape. Each is
// written as two joined pieces so a source scanner (GitHub push protection, for
// one) does not mistake a test value for a real Aiven password. The constant is
// the same string either way.
const (
	fakeDBPassword   = "AVNS" + "_FAKEDBPASSWORD_zQ9xT3kLm"
	fakeUserPassword = "AVNS" + "_FAKEUSERPASS_7hR2pWvYc"
)

type fakeFail struct {
	Status     int
	RetryAfter string
	Message    string // replaces the default message; tests use it to make the server echo a secret
}

type fakeDroplet struct {
	godo.Droplet
	polls    int
	UserData string
}

type fakeDO struct {
	t        *testing.T
	mu       sync.Mutex
	srv      *httptest.Server
	token    string
	PageSize int

	Log    []string            // "METHOD /path?query" of every request
	Bodies map[string][]string // request bodies by "METHOD /path"
	Fails  map[string][]fakeFail
	// ActiveAfter is how many GETs of a new droplet it takes to become active.
	ActiveAfter int
	// StuckNew keeps created droplets in status "new" forever.
	StuckNew bool

	Account   godo.Account
	Sizes     []godo.Size
	Regions   []godo.Region
	Images    []godo.Image
	Keys      []godo.Key
	VPCs      []*godo.VPC
	Firewalls []godo.Firewall
	Droplets  []*fakeDroplet
	Domains   []godo.Domain
	Records   map[string][]godo.DomainRecord
	DBs       []*fakeDB
	DBSizes   []godo.Size // database sizes; only served by /v2/sizes when ExposeDBSizes is set
	// ExposeDBSizes puts DBSizes into the sizes list. Off by default so the milestone 1 golden files do not change.
	ExposeDBSizes bool
	// DBOnlineAfter is how many GETs of a new or resizing cluster it takes to be online.
	DBOnlineAfter int
	Tags          map[string]bool
	Denied        map[string]bool // path prefixes that answer 403
	nextID        int

	m4 fakeM4 // projects, volumes, reserved IPs, load balancers, Spaces keys and buckets (fakedo_m4_test.go)
}

func newFakeDO(t *testing.T) *fakeDO {
	f := &fakeDO{t: t, token: fakeToken, PageSize: 2, Bodies: map[string][]string{}, Fails: map[string][]fakeFail{},
		ActiveAfter: 2, Tags: map[string]bool{}, Denied: map[string]bool{}, nextID: 5000,
		Records: map[string][]godo.DomainRecord{}, DBOnlineAfter: 2}
	f.DBSizes = []godo.Size{
		{Slug: "db-s-1vcpu-1gb", Memory: 1024, Vcpus: 1, PriceMonthly: 15, PriceHourly: 0.022, Available: true},
		{Slug: "db-s-2vcpu-4gb", Memory: 4096, Vcpus: 2, PriceMonthly: 60, PriceHourly: 0.089, Available: true},
	}
	f.Account = godo.Account{DropletLimit: 25, Email: "owner@example.test", Status: "active", Team: &godo.TeamInfo{Name: "Fake Team"}}
	f.Sizes = []godo.Size{
		{Slug: "s-1vcpu-512mb-10gb", Memory: 512, Vcpus: 1, Disk: 10, PriceMonthly: 4, PriceHourly: 0.00595, Available: true, Transfer: 0.5, Regions: []string{"nyc3", "sfo3"}},
		{Slug: "s-1vcpu-1gb", Memory: 1024, Vcpus: 1, Disk: 25, PriceMonthly: 6, PriceHourly: 0.00893, Available: true, Transfer: 1, Regions: []string{"nyc3", "sfo3", "ams3"}},
		{Slug: "s-2vcpu-2gb", Memory: 2048, Vcpus: 2, Disk: 60, PriceMonthly: 18, PriceHourly: 0.02679, Available: true, Transfer: 3, Regions: []string{"nyc3"}},
		{Slug: "s-4vcpu-8gb", Memory: 8192, Vcpus: 4, Disk: 160, PriceMonthly: 48, PriceHourly: 0.07143, Available: false, Transfer: 5, Regions: []string{"nyc3"}},
		{Slug: "gpu-h100x8", Memory: 1966080, Vcpus: 160, Disk: 2000, PriceMonthly: 23000, PriceHourly: 34.5, Available: true, Transfer: 60, Regions: []string{"nyc3"}},
	}
	f.Regions = []godo.Region{
		{Slug: "nyc3", Name: "New York 3", Available: true, Features: []string{"private_networking", "backups"}},
		{Slug: "sfo3", Name: "San Francisco 3", Available: true, Features: []string{"backups"}},
		{Slug: "ams3", Name: "Amsterdam 3", Available: false, Features: []string{}},
	}
	f.Images = []godo.Image{
		{ID: 1, Slug: "ubuntu-24-04-x64", Name: "24.04 (LTS) x64", Type: "base", Distribution: "Ubuntu", Public: true, Regions: []string{"nyc3", "sfo3"}, MinDiskSize: 10, Status: "available"},
		{ID: 2, Slug: "debian-12-x64", Name: "12 x64", Type: "base", Distribution: "Debian", Public: true, Regions: []string{"nyc3"}, MinDiskSize: 10, Status: "available"},
		{ID: 3, Slug: "fedora-40-x64", Name: "40 x64", Type: "base", Distribution: "Fedora", Public: true, Regions: []string{"sfo3"}, MinDiskSize: 10, Status: "available"},
		{ID: 10, Slug: "docker-20-04", Name: "Docker on Ubuntu", Type: "snapshot", Distribution: "Ubuntu", Public: true, Regions: []string{"nyc3"}, MinDiskSize: 25, Status: "available"},
		{ID: 99, Slug: "", Name: "my-custom", Type: "custom", Distribution: "Unknown OS", Regions: []string{"nyc3"}, MinDiskSize: 20, Status: "available"},
	}
	f.Keys = []godo.Key{{ID: 11, Name: "robert-laptop", Fingerprint: "aa:aa:aa:aa:aa:aa:aa:aa:aa:aa:aa:aa:aa:aa:aa:aa"}}
	f.VPCs = []*godo.VPC{
		{ID: "vpc-default-nyc3", Name: "default-nyc3", RegionSlug: "nyc3", IPRange: "10.116.0.0/20", Default: true, CreatedAt: time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)},
		{ID: "vpc-old", Name: "old-net", RegionSlug: "nyc3", IPRange: "10.200.0.0/24", CreatedAt: time.Date(2025, 2, 1, 0, 0, 0, 0, time.UTC)},
	}
	f.Firewalls = []godo.Firewall{{ID: "fw-legacy", Name: "legacy-fw", Status: "succeeded", Created: "2025-03-01T00:00:00Z",
		InboundRules:  []godo.InboundRule{{Protocol: "tcp", PortRange: "22", Sources: &godo.Sources{Addresses: []string{"0.0.0.0/0"}}}},
		OutboundRules: []godo.OutboundRule{{Protocol: "tcp", PortRange: "0", Destinations: &godo.Destinations{Addresses: []string{"0.0.0.0/0"}}}},
		DropletIDs:    []int{}, Tags: []string{}}}
	f.addDroplet(&godo.Droplet{ID: 1001, Name: "legacy-db", Status: "active", Tags: []string{}, VPCUUID: "vpc-default-nyc3"}, "s-1vcpu-1gb", "nyc3", "10.116.0.2", "203.0.113.10")
	f.Domains = []godo.Domain{{Name: "legacy.example", TTL: 1800}}
	f.Records["legacy.example"] = []godo.DomainRecord{
		{ID: 901, Type: "A", Name: "@", Data: "203.0.113.10", TTL: 300},
		{ID: 902, Type: "MX", Name: "@", Data: "mail.legacy.example.", Priority: 10, TTL: 3600},
	}
	f.DBs = []*fakeDB{newFakeDB("legacy-pg", "db-legacy", "nyc3", "15", nil)}
	f.DBs[0].Status = "online"
	f.DBs[0].Rules = []godo.DatabaseFirewallRule{{UUID: "r-legacy", Type: "ip_addr", Value: "198.51.100.7"}}
	f.initM4()
	f.srv = httptest.NewServer(http.HandlerFunc(f.handle))
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakeDO) addDroplet(d *godo.Droplet, size, region, priv, pub string) *fakeDroplet {
	for _, s := range f.Sizes {
		if s.Slug == size {
			sc := s
			d.Size, d.SizeSlug, d.Vcpus, d.Memory, d.Disk = &sc, size, s.Vcpus, s.Memory, s.Disk
		}
	}
	d.Region = &godo.Region{Slug: region}
	d.Image = &godo.Image{ID: 1, Slug: "ubuntu-24-04-x64", Name: "24.04 (LTS) x64"}
	d.Created = "2025-04-01T00:00:00Z"
	d.Networks = &godo.Networks{V4: []godo.NetworkV4{{IPAddress: priv, Type: "private"}, {IPAddress: pub, Type: "public"}}}
	fd := &fakeDroplet{Droplet: *d}
	f.Droplets = append(f.Droplets, fd)
	return fd
}

func (f *fakeDO) droplet(id int) *fakeDroplet {
	for _, d := range f.Droplets {
		if d.ID == id {
			return d
		}
	}
	return nil
}

func (f *fakeDO) seq() int { f.nextID++; return f.nextID }

// Requests returns the log entries that start with prefix.
func (f *fakeDO) Requests(prefix string) []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []string
	for _, l := range f.Log {
		if strings.HasPrefix(l, prefix) {
			out = append(out, l)
		}
	}
	return out
}

// Mutations returns every request that was not a GET.
func (f *fakeDO) Mutations() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []string
	for _, l := range f.Log {
		if !strings.HasPrefix(l, "GET ") {
			out = append(out, l)
		}
	}
	return out
}

func (f *fakeDO) writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}

func (f *fakeDO) errorOut(w http.ResponseWriter, status int, msg string) {
	f.writeJSON(w, status, map[string]string{"id": "error", "message": msg})
}

// page writes one page of a list. items is a slice; key is the JSON key.
func (f *fakeDO) page(w http.ResponseWriter, r *http.Request, key string, items any) {
	v := reflect.ValueOf(items)
	total := v.Len()
	page, _ := strconv.Atoi(r.URL.Query().Get("page"))
	if page < 1 {
		page = 1
	}
	lo := (page - 1) * f.PageSize
	hi := lo + f.PageSize
	if lo > total {
		lo = total
	}
	if hi > total {
		hi = total
	}
	out := map[string]any{key: v.Slice(lo, hi).Interface(), "meta": map[string]int{"total": total}}
	pages := map[string]string{}
	if page > 1 {
		pages["prev"] = fmt.Sprintf("%s%s?page=%d", f.srv.URL, r.URL.Path, page-1)
		pages["first"] = fmt.Sprintf("%s%s?page=1", f.srv.URL, r.URL.Path)
	}
	if hi < total {
		pages["next"] = fmt.Sprintf("%s%s?page=%d", f.srv.URL, r.URL.Path, page+1)
		pages["last"] = fmt.Sprintf("%s%s?page=%d", f.srv.URL, r.URL.Path, (total+f.PageSize-1)/f.PageSize)
	}
	if len(pages) > 0 {
		out["links"] = map[string]any{"pages": pages}
	}
	f.writeJSON(w, 200, out)
}

func (f *fakeDO) handle(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	body, _ := io.ReadAll(r.Body)
	key := r.Method + " " + r.URL.Path
	f.Log = append(f.Log, r.Method+" "+r.URL.RequestURI())
	if len(body) > 0 {
		f.Bodies[key] = append(f.Bodies[key], string(body))
	}
	if r.Header.Get("Authorization") != "Bearer "+f.token {
		// A careless server might echo what it was sent. docli must still not print it.
		f.errorOut(w, 401, "Unable to authenticate you (got "+r.Header.Get("Authorization")+")")
		return
	}
	if q := f.Fails[key]; len(q) > 0 {
		fl := q[0]
		f.Fails[key] = q[1:]
		if fl.RetryAfter != "" {
			w.Header().Set("Retry-After", fl.RetryAfter)
		}
		msg := "injected failure; token " + f.token + " password " + fakeDBPassword
		if fl.Message != "" {
			msg = fl.Message
		}
		f.errorOut(w, fl.Status, msg)
		return
	}
	for p := range f.Denied {
		if strings.HasPrefix(r.URL.Path, p) {
			f.errorOut(w, 403, "You are not authorized to perform this operation")
			return
		}
	}
	parts := strings.Split(strings.Trim(strings.TrimPrefix(r.URL.Path, "/v2"), "/"), "/")
	switch parts[0] {
	case "account":
		if len(parts) == 1 {
			f.writeJSON(w, 200, map[string]any{"account": f.Account})
		} else {
			f.keys(w, r, body, parts[1:])
		}
	case "sizes":
		sizes := append([]godo.Size(nil), f.Sizes...)
		if f.ExposeDBSizes {
			sizes = append(sizes, f.DBSizes...)
		}
		f.page(w, r, "sizes", sizes)
	case "regions":
		f.page(w, r, "regions", f.Regions)
	case "images":
		f.images(w, r, parts)
	case "vpcs":
		f.vpcs(w, r, body, parts)
	case "firewalls":
		f.firewalls(w, r, body, parts)
	case "droplets":
		f.droplets(w, r, body, parts)
	case "tags":
		f.tags(w, r, body, parts)
	case "domains":
		f.domains(w, r, body, parts)
	case "databases":
		f.databases(w, r, body, parts)
	case "projects":
		f.projects(w, r, body, parts)
	case "volumes":
		f.volumes(w, r, body, parts)
	case "reserved_ips":
		f.reservedIPs(w, r, body, parts)
	case "load_balancers":
		f.loadBalancers(w, r, body, parts)
	case "spaces":
		f.spacesKeys(w, r, body, parts)
	default:
		f.errorOut(w, 404, "not found")
	}
}

func (f *fakeDO) images(w http.ResponseWriter, r *http.Request, parts []string) {
	if len(parts) == 2 {
		for _, i := range f.Images {
			if i.Slug == parts[1] {
				f.writeJSON(w, 200, map[string]any{"image": i})
				return
			}
		}
		f.errorOut(w, 404, "The resource you requested could not be found.")
		return
	}
	q := r.URL.Query()
	var out []godo.Image
	for _, i := range f.Images {
		switch {
		case q.Get("private") == "true" && i.Type == "custom":
		case q.Get("type") == "distribution" && i.Type == "base":
		case q.Get("type") == "application" && i.Type == "snapshot" && i.Public:
		default:
			continue
		}
		out = append(out, i)
	}
	f.page(w, r, "images", out)
}

func (f *fakeDO) keys(w http.ResponseWriter, r *http.Request, body []byte, parts []string) {
	if parts[0] != "keys" {
		f.errorOut(w, 404, "not found")
		return
	}
	switch {
	case r.Method == "GET" && len(parts) == 1:
		f.page(w, r, "ssh_keys", f.Keys)
	case r.Method == "POST":
		var k godo.Key
		json.Unmarshal(body, &k)
		k.ID = f.seq()
		k.Fingerprint = "fp:" + strconv.Itoa(k.ID)
		f.Keys = append(f.Keys, k)
		f.writeJSON(w, 201, map[string]any{"ssh_key": k})
	case r.Method == "DELETE" && len(parts) == 2:
		for i, k := range f.Keys {
			if strconv.Itoa(k.ID) == parts[1] {
				f.Keys = append(f.Keys[:i], f.Keys[i+1:]...)
				w.WriteHeader(204)
				return
			}
		}
		f.errorOut(w, 404, "not found")
	default:
		f.errorOut(w, 405, "method not allowed")
	}
}

func (f *fakeDO) vpcMembers(id string) []map[string]string {
	var out []map[string]string
	for _, d := range f.Droplets {
		if d.VPCUUID == id {
			out = append(out, map[string]string{"urn": "do:droplet:" + strconv.Itoa(d.ID), "name": d.Name})
		}
	}
	return nonNil(out)
}

func (f *fakeDO) vpcs(w http.ResponseWriter, r *http.Request, body []byte, parts []string) {
	switch {
	case r.Method == "GET" && len(parts) == 1:
		f.page(w, r, "vpcs", f.VPCs)
	case r.Method == "GET" && len(parts) == 3 && parts[2] == "members":
		f.page(w, r, "members", f.vpcMembers(parts[1]))
	case r.Method == "POST":
		var v godo.VPC
		json.Unmarshal(body, &v)
		v.ID = "vpc-" + strconv.Itoa(f.seq())
		if v.IPRange == "" {
			v.IPRange = "10.116.16.0/20"
		}
		v.CreatedAt = time.Date(2026, 11, 12, 10, 0, 0, 0, time.UTC)
		f.VPCs = append(f.VPCs, &v)
		f.writeJSON(w, 201, map[string]any{"vpc": v})
	case r.Method == "DELETE" && len(parts) == 2:
		for i, v := range f.VPCs {
			if v.ID == parts[1] {
				f.VPCs = append(f.VPCs[:i], f.VPCs[i+1:]...)
				w.WriteHeader(204)
				return
			}
		}
		f.errorOut(w, 404, "not found")
	default:
		f.errorOut(w, 405, "method not allowed")
	}
}

func (f *fakeDO) firewall(id string) *godo.Firewall {
	for i := range f.Firewalls {
		if f.Firewalls[i].ID == id {
			return &f.Firewalls[i]
		}
	}
	return nil
}

func sameJSON(a, b any) bool {
	x, _ := json.Marshal(a)
	y, _ := json.Marshal(b)
	return string(x) == string(y)
}

func (f *fakeDO) firewalls(w http.ResponseWriter, r *http.Request, body []byte, parts []string) {
	switch {
	case r.Method == "GET" && len(parts) == 1:
		f.page(w, r, "firewalls", f.Firewalls)
	case r.Method == "POST" && len(parts) == 1:
		var req godo.FirewallRequest
		json.Unmarshal(body, &req)
		fw := godo.Firewall{ID: "fw-" + strconv.Itoa(f.seq()), Name: req.Name, Status: "waiting", Created: "2026-11-12T10:00:00Z",
			InboundRules: req.InboundRules, OutboundRules: req.OutboundRules, DropletIDs: nonNil(req.DropletIDs), Tags: nonNil(req.Tags)}
		f.Firewalls = append(f.Firewalls, fw)
		f.writeJSON(w, 202, map[string]any{"firewall": fw})
	case r.Method == "DELETE" && len(parts) == 2:
		for i, fw := range f.Firewalls {
			if fw.ID == parts[1] {
				f.Firewalls = append(f.Firewalls[:i], f.Firewalls[i+1:]...)
				w.WriteHeader(204)
				return
			}
		}
		f.errorOut(w, 404, "not found")
	case len(parts) == 3:
		fw := f.firewall(parts[1])
		if fw == nil {
			f.errorOut(w, 404, "not found")
			return
		}
		f.firewallSub(w, r, body, fw, parts[2])
	default:
		f.errorOut(w, 405, "method not allowed")
	}
}

func (f *fakeDO) firewallSub(w http.ResponseWriter, r *http.Request, body []byte, fw *godo.Firewall, sub string) {
	add := r.Method == "POST"
	switch sub {
	case "rules":
		var rr godo.FirewallRulesRequest
		json.Unmarshal(body, &rr)
		if add {
			fw.InboundRules = append(fw.InboundRules, rr.InboundRules...)
			fw.OutboundRules = append(fw.OutboundRules, rr.OutboundRules...)
		} else {
			var in []godo.InboundRule
			for _, x := range fw.InboundRules {
				drop := false
				for _, y := range rr.InboundRules {
					drop = drop || sameJSON(x, y)
				}
				if !drop {
					in = append(in, x)
				}
			}
			var out []godo.OutboundRule
			for _, x := range fw.OutboundRules {
				drop := false
				for _, y := range rr.OutboundRules {
					drop = drop || sameJSON(x, y)
				}
				if !drop {
					out = append(out, x)
				}
			}
			fw.InboundRules, fw.OutboundRules = in, out
		}
	case "droplets":
		var req struct {
			DropletIDs []int `json:"droplet_ids"`
		}
		json.Unmarshal(body, &req)
		fw.DropletIDs = toggleInts(fw.DropletIDs, req.DropletIDs, add)
	case "tags":
		var req struct {
			Tags []string `json:"tags"`
		}
		json.Unmarshal(body, &req)
		fw.Tags = toggleStrings(fw.Tags, req.Tags, add)
	default:
		f.errorOut(w, 404, "not found")
		return
	}
	w.WriteHeader(204)
}

func toggleInts(cur, change []int, add bool) []int {
	out := []int{}
	for _, c := range cur {
		keep := true
		for _, x := range change {
			if !add && x == c {
				keep = false
			}
		}
		if keep {
			out = append(out, c)
		}
	}
	if add {
		out = append(out, change...)
	}
	return out
}

func toggleStrings(cur, change []string, add bool) []string {
	out := []string{}
	for _, c := range cur {
		if add || !contains(change, c) {
			out = append(out, c)
		}
	}
	if add {
		out = append(out, change...)
	}
	return out
}

func (f *fakeDO) droplets(w http.ResponseWriter, r *http.Request, body []byte, parts []string) {
	switch {
	case r.Method == "GET" && len(parts) == 1:
		tag := r.URL.Query().Get("tag_name")
		var out []godo.Droplet
		for _, d := range f.Droplets {
			if tag == "" || hasTag(d.Tags, tag) {
				out = append(out, d.Droplet)
			}
		}
		f.page(w, r, "droplets", out)
	case r.Method == "POST" && len(parts) == 1:
		var req struct {
			godo.DropletCreateRequest
			Image   any   `json:"image"`
			SSHKeys []any `json:"ssh_keys"`
		}
		json.Unmarshal(body, &req)
		id := f.seq()
		d := &godo.Droplet{ID: id, Name: req.Name, Status: "new", Tags: nonNil(req.Tags), VPCUUID: req.VPCUUID}
		if d.VPCUUID == "" {
			d.VPCUUID = "vpc-default-" + req.Region
		}
		for _, t := range req.Tags {
			f.Tags[t] = true
		}
		fd := f.addDroplet(d, req.Size, req.Region, "", "")
		fd.Networks = nil
		fd.UserData = req.UserData
		fd.Created = "2026-11-12T10:00:00Z"
		if sl, ok := req.Image.(string); ok {
			fd.Image = &godo.Image{Slug: sl, Name: sl}
		}
		f.writeJSON(w, 202, map[string]any{"droplet": fd.Droplet})
	case len(parts) >= 2:
		id, _ := strconv.Atoi(parts[1])
		d := f.droplet(id)
		if d == nil {
			f.errorOut(w, 404, "The resource you requested could not be found.")
			return
		}
		switch {
		case r.Method == "GET" && len(parts) == 2:
			d.polls++
			if d.Status == "new" && !f.StuckNew && d.polls >= f.ActiveAfter {
				d.Status = "active"
				d.Networks = &godo.Networks{V4: []godo.NetworkV4{{IPAddress: fmt.Sprintf("10.116.0.%d", id%250), Type: "private"}, {IPAddress: fmt.Sprintf("203.0.113.%d", id%250), Type: "public"}}}
			}
			f.writeJSON(w, 200, map[string]any{"droplet": d.Droplet})
		case r.Method == "DELETE" && len(parts) == 2:
			for i, x := range f.Droplets {
				if x.ID == id {
					f.Droplets = append(f.Droplets[:i], f.Droplets[i+1:]...)
				}
			}
			w.WriteHeader(204)
		case r.Method == "POST" && len(parts) == 3 && parts[2] == "actions":
			var a struct {
				Type string `json:"type"`
			}
			json.Unmarshal(body, &a)
			switch a.Type {
			case "power_off":
				d.Status = "off"
			case "power_on":
				d.Status = "active"
			}
			f.writeJSON(w, 201, map[string]any{"action": map[string]any{"id": f.seq(), "status": "in-progress", "type": a.Type}})
		default:
			f.errorOut(w, 405, "method not allowed")
		}
	}
}

func (f *fakeDO) tags(w http.ResponseWriter, r *http.Request, body []byte, parts []string) {
	switch {
	case r.Method == "GET" && len(parts) == 2:
		if f.Tags[parts[1]] {
			f.writeJSON(w, 200, map[string]any{"tag": map[string]string{"name": parts[1]}})
			return
		}
		f.errorOut(w, 404, "The resource you requested could not be found.")
	case r.Method == "GET":
		var names []map[string]string
		for n := range f.Tags {
			names = append(names, map[string]string{"name": n})
		}
		f.page(w, r, "tags", nonNil(names))
	case r.Method == "POST" && len(parts) == 3 && parts[2] == "resources":
		var req godo.TagResourcesRequest
		json.Unmarshal(body, &req)
		if !f.Tags[parts[1]] {
			f.errorOut(w, 404, "The resource you requested could not be found.")
			return
		}
		for _, res := range req.Resources {
			switch res.Type {
			case godo.DropletResourceType:
				id, _ := strconv.Atoi(res.ID)
				if d := f.droplet(id); d != nil && !hasTag(d.Tags, parts[1]) {
					d.Tags = append(d.Tags, parts[1])
				}
			case godo.DatabaseResourceType:
				if d := f.db(res.ID); d != nil && !hasTag(d.Tags, parts[1]) {
					d.Tags = append(d.Tags, parts[1])
				}
			case godo.VolumeResourceType:
				if v := f.volume(res.ID); v != nil && !hasTag(v.Tags, parts[1]) {
					v.Tags = append(v.Tags, parts[1])
				}
			}
		}
		w.WriteHeader(204)
	case r.Method == "POST":
		var t godo.TagCreateRequest
		json.Unmarshal(body, &t)
		f.Tags[t.Name] = true
		f.writeJSON(w, 201, map[string]any{"tag": map[string]string{"name": t.Name}})
	default:
		f.errorOut(w, 405, "method not allowed")
	}
}

// ---------- domains ----------

func (f *fakeDO) domains(w http.ResponseWriter, r *http.Request, body []byte, parts []string) {
	switch {
	case r.Method == "GET" && len(parts) == 1:
		f.page(w, r, "domains", f.Domains)
	case r.Method == "POST" && len(parts) == 1:
		var req godo.DomainCreateRequest
		json.Unmarshal(body, &req)
		d := godo.Domain{Name: req.Name, TTL: 1800}
		f.Domains = append(f.Domains, d)
		// DigitalOcean puts its own NS records in every new zone.
		for _, ns := range []string{"ns1", "ns2", "ns3"} {
			f.Records[d.Name] = append(f.Records[d.Name], godo.DomainRecord{ID: f.seq(), Type: "NS", Name: "@", Data: ns + ".digitalocean.com.", TTL: 1800})
		}
		f.writeJSON(w, 201, map[string]any{"domain": d})
	case r.Method == "DELETE" && len(parts) == 2:
		for i, d := range f.Domains {
			if d.Name == parts[1] {
				f.Domains = append(f.Domains[:i], f.Domains[i+1:]...)
				delete(f.Records, d.Name)
				w.WriteHeader(204)
				return
			}
		}
		f.errorOut(w, 404, "The resource you requested could not be found.")
	case len(parts) >= 3 && parts[2] == "records":
		known := false
		for _, d := range f.Domains {
			known = known || d.Name == parts[1]
		}
		if !known {
			f.errorOut(w, 404, "The resource you requested could not be found.")
			return
		}
		f.records(w, r, body, parts)
	default:
		f.errorOut(w, 405, "method not allowed")
	}
}

func (f *fakeDO) records(w http.ResponseWriter, r *http.Request, body []byte, parts []string) {
	domain := parts[1]
	switch {
	case r.Method == "GET" && len(parts) == 3:
		f.page(w, r, "domain_records", nonNil(f.Records[domain]))
	case r.Method == "POST" && len(parts) == 3:
		var rec godo.DomainRecord
		json.Unmarshal(body, &rec)
		rec.ID = f.seq()
		f.Records[domain] = append(f.Records[domain], rec)
		f.writeJSON(w, 201, map[string]any{"domain_record": rec})
	case len(parts) == 4:
		id, _ := strconv.Atoi(parts[3])
		for i, rec := range f.Records[domain] {
			if rec.ID != id {
				continue
			}
			switch r.Method {
			case "PUT":
				var nr godo.DomainRecord
				json.Unmarshal(body, &nr)
				nr.ID = id
				f.Records[domain][i] = nr
				f.writeJSON(w, 200, map[string]any{"domain_record": nr})
			case "DELETE":
				f.Records[domain] = append(f.Records[domain][:i], f.Records[domain][i+1:]...)
				w.WriteHeader(204)
			default:
				f.writeJSON(w, 200, map[string]any{"domain_record": rec})
			}
			return
		}
		f.errorOut(w, 404, "The resource you requested could not be found.")
	default:
		f.errorOut(w, 405, "method not allowed")
	}
}

// ---------- databases ----------

type fakeDB struct {
	godo.Database
	polls   int
	Users   []godo.DatabaseUser
	DBNames []string
	Pools   []godo.DatabasePool
	Rules   []godo.DatabaseFirewallRule
	Backups []godo.DatabaseBackup
	Target  string // the status it becomes after DBOnlineAfter GETs ("" for none)
}

func newFakeDB(name, id, region, version string, tags []string) *fakeDB {
	host := name + "-do-user-1-0.db.ondigitalocean.com"
	mk := func(h string, port int) *godo.DatabaseConnection {
		return &godo.DatabaseConnection{Protocol: "postgresql", Host: h, Port: port, Database: "defaultdb", User: "doadmin", Password: fakeDBPassword, SSL: true,
			URI: "postgresql://doadmin:" + fakeDBPassword + "@" + h + ":" + strconv.Itoa(port) + "/defaultdb?sslmode=require"}
	}
	d := &fakeDB{Database: godo.Database{ID: id, Name: name, EngineSlug: "pg", VersionSlug: version, SizeSlug: "db-s-1vcpu-1gb", NumNodes: 1, RegionSlug: region,
		Status: "online", Tags: nonNil(tags), PrivateNetworkUUID: "vpc-default-" + region, CreatedAt: time.Date(2025, 5, 1, 0, 0, 0, 0, time.UTC),
		Connection: mk(host, 25060), PrivateConnection: mk("private-"+host, 25060)}}
	d.Users = []godo.DatabaseUser{{Name: "doadmin", Role: "primary", Password: fakeDBPassword}, {Name: "app", Role: "normal", Password: fakeUserPassword}}
	d.DBNames = []string{"defaultdb"}
	d.Backups = []godo.DatabaseBackup{{CreatedAt: time.Date(2026, 11, 11, 3, 0, 0, 0, time.UTC), SizeGigabytes: 0.5}}
	return d
}

func (f *fakeDO) db(id string) *fakeDB {
	for _, d := range f.DBs {
		if d.ID == id {
			return d
		}
	}
	return nil
}

func (f *fakeDO) databases(w http.ResponseWriter, r *http.Request, body []byte, parts []string) {
	switch {
	case r.Method == "GET" && len(parts) == 1:
		var out []godo.Database
		for _, d := range f.DBs {
			out = append(out, d.Database)
		}
		f.pageAll(w, "databases", nonNil(out))
		return
	case r.Method == "GET" && len(parts) == 2 && parts[1] == "options":
		sizes := []string{"db-s-1vcpu-1gb", "db-s-2vcpu-4gb"}
		f.writeJSON(w, 200, map[string]any{"options": map[string]any{"pg": map[string]any{
			"regions": []string{"nyc3", "sfo3"}, "versions": []string{"15", "16"},
			"layouts": []map[string]any{{"num_nodes": 1, "sizes": sizes}, {"num_nodes": 2, "sizes": sizes}, {"num_nodes": 3, "sizes": sizes}}}}})
		return
	case r.Method == "POST" && len(parts) == 1:
		var req godo.DatabaseCreateRequest
		json.Unmarshal(body, &req)
		id := "db-" + strconv.Itoa(f.seq())
		d := newFakeDB(req.Name, id, req.Region, req.Version, req.Tags)
		d.SizeSlug, d.NumNodes, d.PrivateNetworkUUID, d.Status = req.SizeSlug, req.NumNodes, req.PrivateNetworkUUID, "creating"
		d.Target = "online"
		d.CreatedAt = time.Date(2026, 11, 12, 10, 0, 0, 0, time.UTC)
		for _, rl := range req.Rules {
			d.Rules = append(d.Rules, godo.DatabaseFirewallRule{UUID: "rule-" + strconv.Itoa(f.seq()), Type: rl.Type, Value: rl.Value})
		}
		for _, t := range req.Tags {
			f.Tags[t] = true
		}
		f.DBs = append(f.DBs, d)
		f.writeJSON(w, 201, map[string]any{"database": d.Database})
		return
	}
	d := f.db(parts[1])
	if d == nil {
		f.errorOut(w, 404, "The resource you requested could not be found.")
		return
	}
	if len(parts) == 2 {
		switch r.Method {
		case "GET":
			d.polls++
			if d.Target != "" && d.polls >= f.DBOnlineAfter && !f.StuckNew {
				d.Status, d.Target = d.Target, ""
			}
			f.writeJSON(w, 200, map[string]any{"database": d.Database})
		case "DELETE":
			for i, x := range f.DBs {
				if x == d {
					f.DBs = append(f.DBs[:i], f.DBs[i+1:]...)
				}
			}
			w.WriteHeader(204)
		default:
			f.errorOut(w, 405, "method not allowed")
		}
		return
	}
	f.dbSub(w, r, body, d, parts[2:])
}

func (f *fakeDO) dbSub(w http.ResponseWriter, r *http.Request, body []byte, d *fakeDB, sub []string) {
	switch sub[0] {
	case "resize":
		var req godo.DatabaseResizeRequest
		json.Unmarshal(body, &req)
		d.SizeSlug, d.NumNodes = req.SizeSlug, req.NumNodes
		d.Status, d.Target, d.polls = "resizing", "online", 0
		w.WriteHeader(202)
	case "backups":
		f.pageAll(w, "backups", nonNil(d.Backups))
	case "firewall":
		if r.Method == "PUT" {
			var req godo.DatabaseUpdateFirewallRulesRequest
			json.Unmarshal(body, &req)
			d.Rules = nil
			for _, rl := range req.Rules {
				d.Rules = append(d.Rules, godo.DatabaseFirewallRule{UUID: "rule-" + strconv.Itoa(f.seq()), Type: rl.Type, Value: rl.Value})
			}
			w.WriteHeader(204)
			return
		}
		f.writeJSON(w, 200, map[string]any{"rules": nonNil(d.Rules)})
	case "users":
		switch {
		case r.Method == "GET":
			f.pageAll(w, "users", d.Users)
		case r.Method == "POST":
			var req godo.DatabaseCreateUserRequest
			json.Unmarshal(body, &req)
			u := godo.DatabaseUser{Name: req.Name, Role: "normal", Password: fakeUserPassword}
			d.Users = append(d.Users, u)
			f.writeJSON(w, 201, map[string]any{"user": u})
		case r.Method == "DELETE" && len(sub) == 2:
			for i, u := range d.Users {
				if u.Name == sub[1] {
					d.Users = append(d.Users[:i], d.Users[i+1:]...)
					w.WriteHeader(204)
					return
				}
			}
			f.errorOut(w, 404, "not found")
		}
	case "dbs":
		switch {
		case r.Method == "GET":
			var out []godo.DatabaseDB
			for _, n := range d.DBNames {
				out = append(out, godo.DatabaseDB{Name: n})
			}
			f.pageAll(w, "dbs", nonNil(out))
		case r.Method == "POST":
			var req godo.DatabaseCreateDBRequest
			json.Unmarshal(body, &req)
			d.DBNames = append(d.DBNames, req.Name)
			f.writeJSON(w, 201, map[string]any{"db": godo.DatabaseDB{Name: req.Name}})
		case r.Method == "DELETE" && len(sub) == 2:
			d.DBNames = toggleStrings(d.DBNames, []string{sub[1]}, false)
			w.WriteHeader(204)
		}
	case "pools":
		switch {
		case r.Method == "GET":
			f.pageAll(w, "pools", nonNil(d.Pools))
		case r.Method == "POST":
			var req godo.DatabaseCreatePoolRequest
			json.Unmarshal(body, &req)
			p := godo.DatabasePool{Name: req.Name, User: req.User, Size: req.Size, Database: req.Database, Mode: req.Mode,
				Connection:        &godo.DatabaseConnection{Host: d.Connection.Host, Port: 25061, Database: req.Name, User: req.User, Password: fakeUserPassword, SSL: true, URI: "postgresql://" + req.User + ":" + fakeUserPassword + "@pool"},
				PrivateConnection: &godo.DatabaseConnection{Host: d.PrivateConnection.Host, Port: 25061, Database: req.Name, User: req.User, Password: fakeUserPassword, SSL: true}}
			d.Pools = append(d.Pools, p)
			f.writeJSON(w, 201, map[string]any{"pool": p})
		case r.Method == "DELETE" && len(sub) == 2:
			for i, p := range d.Pools {
				if p.Name == sub[1] {
					d.Pools = append(d.Pools[:i], d.Pools[i+1:]...)
					w.WriteHeader(204)
					return
				}
			}
			f.errorOut(w, 404, "not found")
		}
	default:
		f.errorOut(w, 404, "not found")
	}
}

func (f *fakeDO) dbByName(name string) *fakeDB {
	for _, d := range f.DBs {
		if d.Name == name {
			return d
		}
	}
	return nil
}

// pageAll answers a list in one piece with no links, like the database lists as godo reads them.
func (f *fakeDO) pageAll(w http.ResponseWriter, key string, items any) {
	f.writeJSON(w, 200, map[string]any{key: items})
}
