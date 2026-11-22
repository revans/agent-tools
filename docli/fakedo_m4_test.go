package main

// The fake DigitalOcean API, milestone 4: projects, block storage volumes, reserved IPs, load balancers, Spaces
// keys (REST) and a fake S3 endpoint for Spaces buckets. The S3 side checks every request's AWS Signature
// Version 4 against the access key the test configured, so a request signed wrongly fails like it would on the
// real service (the real service is not available to the tests).

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/digitalocean/godo"
)

// Spaces credentials the fake accepts. No docli output may contain the secret (see spaces_test.go).
const (
	fakeSpacesAccess = "DO00FAKEACCESSKEY0000"
	fakeSpacesSecret = "fAkE+SpAcEs/SeCrEt0123456789abcdefABCDEFghij"
	// fakeNewKeySecret is what the fake returns when a key is created.
	fakeNewKeySecret = "nEwKeY+SeCrEt/Zz9876543210mnbvcxzLKJHGFDSA"
)

type fakeLB struct {
	godo.LoadBalancer
	polls int
}

type fakeBucket struct {
	Region  string
	Created string
	Objects int
}

type fakeM4 struct {
	Projects      []*godo.Project
	ProjResources map[string][]string // project id -> urns
	Volumes       []*godo.Volume
	ReservedIPs   []*godo.ReservedIP
	LBs           []*fakeLB
	SpacesKeys    []*godo.SpacesKey
	// ListReturnsSecret makes GET /v2/spaces/keys include each key's secret, to prove docli never prints one even
	// if the API is careless.
	ListReturnsSecret bool
	// KeysNotFound makes /v2/spaces/keys answer 404, as it would if the docs that say "control panel only" are right.
	KeysNotFound bool

	s3      *httptest.Server
	Buckets map[string]*fakeBucket // by name
	S3Log   []string               // "METHOD /path region=R"
	S3Fails map[string]int         // "METHOD /path" -> status to answer once
}

// initM4 makes the milestone 4 part of the fake empty, so the milestone 1 to 3 tests see the account they always
// saw. seedM4 (used by newEnvM4) adds the resources that exist before docli does anything.
func (f *fakeDO) initM4() {
	m := &f.m4
	m.ProjResources = map[string][]string{}
	m.Buckets = map[string]*fakeBucket{}
	m.S3Fails = map[string]int{}
	m.s3 = httptest.NewServer(http.HandlerFunc(f.handleS3))
	f.t.Cleanup(m.s3.Close)
}

func (f *fakeDO) seedM4() {
	m := &f.m4
	m.Projects = []*godo.Project{
		{ID: "proj-default", Name: "first-project", Purpose: "Just trying out DigitalOcean", Environment: "Development", IsDefault: true, CreatedAt: "2025-01-01T00:00:00Z"},
		{ID: "proj-prod", Name: "Production", Purpose: "Web Application", Environment: "Production", CreatedAt: "2025-02-01T00:00:00Z"},
	}
	m.ProjResources["proj-default"] = []string{"do:droplet:1001"}
	m.Volumes = []*godo.Volume{
		{ID: "vol-legacy", Name: "legacy-data", Region: &godo.Region{Slug: "nyc3"}, SizeGigaBytes: 100, Tags: []string{}, DropletIDs: []int{}, CreatedAt: time.Date(2025, 3, 1, 0, 0, 0, 0, time.UTC)},
	}
	m.ReservedIPs = []*godo.ReservedIP{{IP: "198.51.100.77", Region: &godo.Region{Slug: "nyc3"}}}
	m.Buckets["legacy-assets"] = &fakeBucket{Region: "nyc3", Created: "2025-01-02T03:04:05.000Z", Objects: 3}
	m.SpacesKeys = []*godo.SpacesKey{{Name: "legacy-key", AccessKey: "DO00LEGACYKEY000000", Grants: []*godo.Grant{{Bucket: "legacy-assets", Permission: godo.SpacesKeyRead}}, CreatedAt: "2025-01-03T00:00:00Z"}}
}

func (f *fakeDO) S3Requests() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.m4.S3Log...)
}

func (f *fakeDO) S3Mutations() []string {
	var out []string
	for _, l := range f.S3Requests() {
		if !strings.HasPrefix(l, "GET ") {
			out = append(out, l)
		}
	}
	return out
}

// ---------- projects ----------

func (f *fakeDO) project(id string) *godo.Project {
	for _, p := range f.m4.Projects {
		if p.ID == id {
			return p
		}
	}
	return nil
}

func (f *fakeDO) projects(w http.ResponseWriter, r *http.Request, body []byte, parts []string) {
	m := &f.m4
	switch {
	case r.Method == "GET" && len(parts) == 1:
		f.page(w, r, "projects", m.Projects)
	case r.Method == "POST" && len(parts) == 1:
		var req godo.CreateProjectRequest
		json.Unmarshal(body, &req)
		p := &godo.Project{ID: "proj-" + strconv.Itoa(f.seq()), Name: req.Name, Description: req.Description, Purpose: req.Purpose, Environment: req.Environment, CreatedAt: "2026-11-12T10:00:00Z"}
		m.Projects = append(m.Projects, p)
		f.writeJSON(w, 201, map[string]any{"project": p})
	case len(parts) >= 2:
		p := f.project(parts[1])
		if p == nil {
			f.errorOut(w, 404, "The resource you requested could not be found.")
			return
		}
		switch {
		case r.Method == "GET" && len(parts) == 3 && parts[2] == "resources":
			var res []godo.ProjectResource
			for _, u := range m.ProjResources[p.ID] {
				res = append(res, godo.ProjectResource{URN: u, AssignedAt: "2026-01-01T00:00:00Z", Status: "ok"})
			}
			f.page(w, r, "resources", nonNil(res))
		case r.Method == "POST" && len(parts) == 3 && parts[2] == "resources":
			var req struct {
				Resources []string `json:"resources"`
			}
			json.Unmarshal(body, &req)
			for _, u := range req.Resources {
				for id, l := range m.ProjResources {
					var keep []string
					for _, x := range l {
						if x != u {
							keep = append(keep, x)
						}
					}
					m.ProjResources[id] = keep
				}
				m.ProjResources[p.ID] = append(m.ProjResources[p.ID], u)
			}
			var res []godo.ProjectResource
			for _, u := range req.Resources {
				res = append(res, godo.ProjectResource{URN: u})
			}
			f.writeJSON(w, 200, map[string]any{"resources": res})
		case r.Method == "DELETE" && len(parts) == 2:
			if len(m.ProjResources[p.ID]) > 0 {
				f.errorOut(w, 412, "cannot delete a project that has resources")
				return
			}
			for i, x := range m.Projects {
				if x.ID == p.ID {
					m.Projects = append(m.Projects[:i], m.Projects[i+1:]...)
				}
			}
			w.WriteHeader(204)
		default:
			f.errorOut(w, 405, "method not allowed")
		}
	}
}

// forget removes a deleted resource from every project, as DigitalOcean does.
func (f *fakeDO) forget(urn string) {
	for id, l := range f.m4.ProjResources {
		var keep []string
		for _, x := range l {
			if x != urn {
				keep = append(keep, x)
			}
		}
		f.m4.ProjResources[id] = keep
	}
}

// inProject reports which project holds a urn ("" when none does).
func (f *fakeDO) inProject(urn string) string {
	for id, l := range f.m4.ProjResources {
		if contains(l, urn) {
			return id
		}
	}
	return ""
}

// ---------- volumes ----------

func (f *fakeDO) volume(id string) *godo.Volume {
	for _, v := range f.m4.Volumes {
		if v.ID == id {
			return v
		}
	}
	return nil
}

func (f *fakeDO) volumes(w http.ResponseWriter, r *http.Request, body []byte, parts []string) {
	m := &f.m4
	switch {
	case r.Method == "GET" && len(parts) == 1:
		q := r.URL.Query()
		var out []godo.Volume
		for _, v := range m.Volumes {
			if (q.Get("region") == "" || v.Region.Slug == q.Get("region")) && (q.Get("name") == "" || v.Name == q.Get("name")) {
				out = append(out, *v)
			}
		}
		f.page(w, r, "volumes", nonNil(out))
	case r.Method == "POST" && len(parts) == 1:
		var req godo.VolumeCreateRequest
		json.Unmarshal(body, &req)
		for _, t := range req.Tags {
			if !f.Tags[t] {
				f.errorOut(w, 422, "tag "+t+" does not exist")
				return
			}
		}
		v := &godo.Volume{ID: "vol-" + strconv.Itoa(f.seq()), Name: req.Name, Region: &godo.Region{Slug: req.Region}, SizeGigaBytes: req.SizeGigaBytes,
			Description: req.Description, FilesystemType: req.FilesystemType, FilesystemLabel: req.FilesystemLabel, Tags: nonNil(req.Tags), DropletIDs: []int{},
			CreatedAt: time.Date(2026, 11, 12, 10, 0, 0, 0, time.UTC)}
		m.Volumes = append(m.Volumes, v)
		f.writeJSON(w, 201, map[string]any{"volume": v})
	case len(parts) >= 2:
		v := f.volume(parts[1])
		if v == nil {
			f.errorOut(w, 404, "The resource you requested could not be found.")
			return
		}
		switch {
		case r.Method == "DELETE" && len(parts) == 2:
			if len(v.DropletIDs) > 0 {
				f.errorOut(w, 409, "volume is attached")
				return
			}
			for i, x := range m.Volumes {
				if x.ID == v.ID {
					m.Volumes = append(m.Volumes[:i], m.Volumes[i+1:]...)
				}
			}
			f.forget("do:volume:" + v.ID)
			w.WriteHeader(204)
		case r.Method == "POST" && len(parts) == 3 && parts[2] == "actions":
			var a struct {
				Type      string `json:"type"`
				DropletID int    `json:"droplet_id"`
				Size      int64  `json:"size_gigabytes"`
				Region    string `json:"region"`
			}
			json.Unmarshal(body, &a)
			switch a.Type {
			case "attach":
				v.DropletIDs = []int{a.DropletID}
			case "detach":
				v.DropletIDs = []int{}
			case "resize":
				if a.Size <= v.SizeGigaBytes {
					f.errorOut(w, 422, "volume size can only be increased")
					return
				}
				v.SizeGigaBytes = a.Size
			}
			f.writeJSON(w, 202, map[string]any{"action": map[string]any{"id": f.seq(), "status": "in-progress", "type": a.Type}})
		default:
			f.errorOut(w, 405, "method not allowed")
		}
	}
}

// ---------- reserved IPs ----------

func (f *fakeDO) reservedIP(ip string) *godo.ReservedIP {
	for _, x := range f.m4.ReservedIPs {
		if x.IP == ip {
			return x
		}
	}
	return nil
}

func (f *fakeDO) reservedIPs(w http.ResponseWriter, r *http.Request, body []byte, parts []string) {
	m := &f.m4
	switch {
	case r.Method == "GET" && len(parts) == 1:
		var out []godo.ReservedIP
		for _, x := range m.ReservedIPs {
			out = append(out, *x)
		}
		f.page(w, r, "reserved_ips", nonNil(out))
	case r.Method == "POST" && len(parts) == 1:
		var req godo.ReservedIPCreateRequest
		json.Unmarshal(body, &req)
		x := &godo.ReservedIP{IP: fmt.Sprintf("203.0.113.%d", 100+len(m.ReservedIPs)), Region: &godo.Region{Slug: req.Region}}
		if req.DropletID != 0 {
			if d := f.droplet(req.DropletID); d != nil {
				x.Region = &godo.Region{Slug: d.Region.Slug}
				x.Droplet = &d.Droplet
			}
		}
		m.ReservedIPs = append(m.ReservedIPs, x)
		f.writeJSON(w, 202, map[string]any{"reserved_ip": x})
	case len(parts) >= 2:
		x := f.reservedIP(parts[1])
		if x == nil {
			f.errorOut(w, 404, "The resource you requested could not be found.")
			return
		}
		switch {
		case r.Method == "DELETE" && len(parts) == 2:
			for i, y := range m.ReservedIPs {
				if y.IP == x.IP {
					m.ReservedIPs = append(m.ReservedIPs[:i], m.ReservedIPs[i+1:]...)
				}
			}
			w.WriteHeader(204)
		case r.Method == "POST" && len(parts) == 3 && parts[2] == "actions":
			var a struct {
				Type      string `json:"type"`
				DropletID int    `json:"droplet_id"`
			}
			json.Unmarshal(body, &a)
			switch a.Type {
			case "assign":
				d := f.droplet(a.DropletID)
				if d == nil {
					f.errorOut(w, 404, "droplet not found")
					return
				}
				x.Droplet = &d.Droplet
			case "unassign":
				x.Droplet = nil
			}
			f.writeJSON(w, 201, map[string]any{"action": map[string]any{"id": f.seq(), "status": "in-progress", "type": a.Type}})
		default:
			f.errorOut(w, 405, "method not allowed")
		}
	}
}

// ---------- load balancers ----------

func (f *fakeDO) lb(id string) *fakeLB {
	for _, l := range f.m4.LBs {
		if l.ID == id {
			return l
		}
	}
	return nil
}

func (f *fakeDO) loadBalancers(w http.ResponseWriter, r *http.Request, body []byte, parts []string) {
	m := &f.m4
	switch {
	case r.Method == "GET" && len(parts) == 1:
		var out []godo.LoadBalancer
		for _, l := range m.LBs {
			l.polls++
			if l.Status == "new" && !f.StuckNew && l.polls >= f.ActiveAfter {
				l.Status = "active"
				l.IP = fmt.Sprintf("203.0.113.%d", 200+len(m.LBs))
			}
			out = append(out, l.LoadBalancer)
		}
		f.page(w, r, "load_balancers", nonNil(out))
	case r.Method == "POST" && len(parts) == 1:
		var req godo.LoadBalancerRequest
		json.Unmarshal(body, &req)
		l := &fakeLB{LoadBalancer: godo.LoadBalancer{ID: fmt.Sprintf("lb-%d", f.seq()), Name: req.Name, Algorithm: req.Algorithm, Status: "new", Created: "2026-11-12T10:00:00Z",
			SizeSlug: firstNonEmpty(req.SizeSlug, "lb-small"), SizeUnit: req.SizeUnit, ForwardingRules: req.ForwardingRules, HealthCheck: req.HealthCheck, DropletIDs: req.DropletIDs,
			Tag: req.Tag, VPCUUID: firstNonEmpty(req.VPCUUID, "vpc-default-"+req.Region), RedirectHttpToHttps: req.RedirectHttpToHttps, Region: &godo.Region{Slug: req.Region}}}
		m.LBs = append(m.LBs, l)
		f.writeJSON(w, 202, map[string]any{"load_balancer": l.LoadBalancer})
	case r.Method == "DELETE" && len(parts) == 2:
		for i, l := range m.LBs {
			if l.ID == parts[1] {
				m.LBs = append(m.LBs[:i], m.LBs[i+1:]...)
				f.forget("do:loadbalancer:" + l.ID)
				w.WriteHeader(204)
				return
			}
		}
		f.errorOut(w, 404, "The resource you requested could not be found.")
	default:
		f.errorOut(w, 405, "method not allowed")
	}
}

// ---------- Spaces keys (REST) ----------

func (f *fakeDO) spacesKeys(w http.ResponseWriter, r *http.Request, body []byte, parts []string) {
	m := &f.m4
	if len(parts) < 2 || parts[1] != "keys" || m.KeysNotFound {
		f.errorOut(w, 404, "not found")
		return
	}
	switch {
	case r.Method == "GET" && len(parts) == 2:
		var out []godo.SpacesKey
		for _, k := range m.SpacesKeys {
			c := *k
			if m.ListReturnsSecret {
				c.SecretKey = fakeNewKeySecret
			}
			out = append(out, c)
		}
		f.page(w, r, "keys", nonNil(out))
	case r.Method == "POST" && len(parts) == 2:
		var req godo.SpacesKeyCreateRequest
		json.Unmarshal(body, &req)
		k := &godo.SpacesKey{Name: req.Name, AccessKey: fmt.Sprintf("DO00NEWKEY%08d", f.seq()), Grants: req.Grants, CreatedAt: "2026-11-12T10:00:00Z"}
		m.SpacesKeys = append(m.SpacesKeys, k)
		out := *k
		out.SecretKey = fakeNewKeySecret
		f.writeJSON(w, 201, map[string]any{"key": out})
	case r.Method == "DELETE" && len(parts) == 3:
		for i, k := range m.SpacesKeys {
			if k.AccessKey == parts[2] {
				m.SpacesKeys = append(m.SpacesKeys[:i], m.SpacesKeys[i+1:]...)
				w.WriteHeader(204)
				return
			}
		}
		f.errorOut(w, 404, "not found")
	default:
		f.errorOut(w, 405, "method not allowed")
	}
}

// ---------- fake S3 (Spaces buckets) ----------

func fakeS3Error(w http.ResponseWriter, status int, code, msg string) {
	w.Header().Set("Content-Type", "application/xml")
	w.WriteHeader(status)
	fmt.Fprintf(w, `<?xml version="1.0" encoding="UTF-8"?><Error><Code>%s</Code><Message>%s</Message></Error>`, code, msg)
}

// verifySigV4 recomputes the signature of the request the way a server does, from the Authorization header.
func (f *fakeDO) verifySigV4(r *http.Request) (region string, ok bool) {
	auth := r.Header.Get("Authorization")
	rest, found := strings.CutPrefix(auth, sigAlgo+" ")
	if !found {
		return "", false
	}
	fields := map[string]string{}
	for _, p := range strings.Split(rest, ",") {
		k, v, _ := strings.Cut(strings.TrimSpace(p), "=")
		fields[k] = v
	}
	cred := strings.Split(fields["Credential"], "/")
	if len(cred) != 5 || cred[0] != fakeSpacesAccess || cred[3] != "s3" || cred[4] != "aws4_request" {
		return "", false
	}
	headers := map[string]string{}
	for _, name := range strings.Split(fields["SignedHeaders"], ";") {
		if name == "host" {
			headers[name] = r.Host
		} else {
			headers[name] = r.Header.Get(name)
		}
	}
	amzDate := r.Header.Get("x-amz-date")
	if !strings.HasPrefix(amzDate, cred[1]) || r.Header.Get("x-amz-content-sha256") != emptySHA256 {
		return "", false
	}
	q, _ := url.ParseQuery(r.URL.RawQuery)
	sig, _ := sigV4(fakeSpacesSecret, r.Method, r.URL.Path, q, headers, emptySHA256, amzDate, cred[2], "s3")
	return cred[2], sig == fields["Signature"] && fields["SignedHeaders"] != ""
}

func (f *fakeDO) handleS3(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	m := &f.m4
	region, ok := f.verifySigV4(r)
	m.S3Log = append(m.S3Log, fmt.Sprintf("%s %s region=%s", r.Method, r.URL.RequestURI(), region))
	if !ok {
		fakeS3Error(w, 403, "SignatureDoesNotMatch", "The request signature we calculated does not match the signature you provided. Authorization: "+r.Header.Get("Authorization"))
		return
	}
	if st := m.S3Fails[r.Method+" "+r.URL.Path]; st != 0 {
		delete(m.S3Fails, r.Method+" "+r.URL.Path)
		fakeS3Error(w, st, "InternalError", "injected failure; secret "+fakeSpacesSecret)
		return
	}
	name := strings.Trim(r.URL.Path, "/")
	switch {
	case r.Method == "GET" && name == "":
		var b strings.Builder
		b.WriteString(`<?xml version="1.0" encoding="UTF-8"?><ListAllMyBucketsResult><Buckets>`)
		var names []string
		for n := range m.Buckets {
			names = append(names, n)
		}
		sort.Strings(names) // a map iterates in random order; goldens must not
		for _, n := range names {
			if bk := m.Buckets[n]; bk.Region == region {
				fmt.Fprintf(&b, "<Bucket><Name>%s</Name><CreationDate>%s</CreationDate></Bucket>", n, bk.Created)
			}
		}
		b.WriteString(`</Buckets></ListAllMyBucketsResult>`)
		w.Header().Set("Content-Type", "application/xml")
		fmt.Fprint(w, b.String())
	case r.Method == "PUT":
		if _, exists := m.Buckets[name]; exists {
			fakeS3Error(w, 409, "BucketAlreadyExists", "The requested bucket name is not available.")
			return
		}
		m.Buckets[name] = &fakeBucket{Region: region, Created: "2026-11-12T10:00:00.000Z"}
		w.WriteHeader(200)
	case r.Method == "DELETE":
		bk := m.Buckets[name]
		switch {
		case bk == nil:
			fakeS3Error(w, 404, "NoSuchBucket", "The specified bucket does not exist")
		case bk.Objects > 0:
			fakeS3Error(w, 409, "BucketNotEmpty", "The bucket you tried to delete is not empty")
		default:
			delete(m.Buckets, name)
			w.WriteHeader(204)
		}
	case r.Method == "GET" && r.URL.Query().Get("list-type") == "2":
		bk := m.Buckets[name]
		if bk == nil {
			fakeS3Error(w, 404, "NoSuchBucket", "The specified bucket does not exist")
			return
		}
		w.Header().Set("Content-Type", "application/xml")
		if bk.Objects > 0 {
			fmt.Fprint(w, `<?xml version="1.0" encoding="UTF-8"?><ListBucketResult><KeyCount>1</KeyCount><Contents><Key>a.txt</Key></Contents></ListBucketResult>`)
		} else {
			fmt.Fprint(w, `<?xml version="1.0" encoding="UTF-8"?><ListBucketResult><KeyCount>0</KeyCount></ListBucketResult>`)
		}
	default:
		fakeS3Error(w, 405, "MethodNotAllowed", "no")
	}
}

// newEnvM4 is newEnv with Spaces credentials and the fake S3 endpoint configured.
func newEnvM4(t *testing.T) *testEnv {
	t.Helper()
	e := newEnv(t)
	e.fake.seedM4()
	e.env["DOCLI_SPACES_ACCESS_KEY"] = fakeSpacesAccess
	e.env["DOCLI_SPACES_SECRET_KEY"] = fakeSpacesSecret
	e.app.SpacesURL = e.fake.m4.s3.URL
	e.env["DOCLI_REGION"] = "nyc3"
	return e
}
