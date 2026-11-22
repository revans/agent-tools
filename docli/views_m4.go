package main

import (
	"github.com/digitalocean/godo"
)

// JSON for milestone 4. As before every field is always present. Nothing here has a field for a Spaces secret
// key, so no code path can print one by accident: a created key's secret goes only to the file named with
// --secret-file.

type projectJSON struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description"`
	Purpose     string `json:"purpose"`
	Environment string `json:"environment"`
	Default     bool   `json:"default"`
	Managed     bool   `json:"managed"`
	CreatedAt   string `json:"created_at"`
}

func projectView(p godo.Project) projectJSON {
	return projectJSON{p.ID, p.Name, p.Description, p.Purpose, p.Environment, p.IsDefault, hasManagedPrefix(p.Name), p.CreatedAt}
}

type projectResourceJSON struct {
	URN        string `json:"urn"`
	AssignedAt string `json:"assigned_at"`
	Status     string `json:"status"`
}

type projectDetailJSON struct {
	projectJSON
	Resources []projectResourceJSON `json:"resources"`
}

type projectAssignJSON struct {
	Assigned    bool     `json:"assigned"`
	ProjectID   string   `json:"project_id"`
	ProjectName string   `json:"project_name"`
	Resources   []string `json:"resources"`
}

type volumeJSON struct {
	ID              string   `json:"id"`
	Name            string   `json:"name"`
	Managed         bool     `json:"managed"`
	Region          string   `json:"region"`
	SizeGB          int64    `json:"size_gb"`
	DropletIDs      []int    `json:"droplet_ids"`
	FilesystemType  string   `json:"filesystem_type"`
	FilesystemLabel string   `json:"filesystem_label"`
	Description     string   `json:"description"`
	Tags            []string `json:"tags"`
	CreatedAt       string   `json:"created_at"`
}

func volumeView(v godo.Volume) volumeJSON {
	return volumeJSON{v.ID, v.Name, hasTag(v.Tags, managedTag), volumeRegion(v), v.SizeGigaBytes, nonNil(v.DropletIDs),
		v.FilesystemType, v.FilesystemLabel, v.Description, nonNil(v.Tags), fmtTime(v.CreatedAt)}
}

type reservedIPJSON struct {
	IP        string `json:"ip"`
	Region    string `json:"region"`
	DropletID int    `json:"droplet_id"` // 0 when it is not assigned
	Droplet   string `json:"droplet"`
	ProjectID string `json:"project_id"`
	Locked    bool   `json:"locked"`
	Managed   bool   `json:"managed"` // the manifest lists it as created by docli
}

func reservedIPView(ip godo.ReservedIP, managed bool) reservedIPJSON {
	id := 0
	if ip.Droplet != nil {
		id = ip.Droplet.ID
	}
	return reservedIPJSON{ip.IP, ripRegion(ip), id, ripDropletName(ip), ip.ProjectID, ip.Locked, managed}
}

type lbRuleJSON struct {
	EntryProtocol  string `json:"entry_protocol"`
	EntryPort      int    `json:"entry_port"`
	TargetProtocol string `json:"target_protocol"`
	TargetPort     int    `json:"target_port"`
	CertificateID  string `json:"certificate_id"`
	TLSPassthrough bool   `json:"tls_passthrough"`
	Spec           string `json:"spec"`
}

type lbHealthJSON struct {
	Protocol           string `json:"protocol"`
	Port               int    `json:"port"`
	Path               string `json:"path"`
	IntervalSeconds    int    `json:"interval_seconds"`
	TimeoutSeconds     int    `json:"timeout_seconds"`
	HealthyThreshold   int    `json:"healthy_threshold"`
	UnhealthyThreshold int    `json:"unhealthy_threshold"`
	Spec               string `json:"spec"`
}

type lbJSON struct {
	ID              string        `json:"id"`
	Name            string        `json:"name"`
	Managed         bool          `json:"managed"`
	IP              string        `json:"ip"`
	Status          string        `json:"status"`
	Region          string        `json:"region"`
	Size            string        `json:"size"`
	SizeUnit        uint32        `json:"size_unit"`
	Algorithm       string        `json:"algorithm"`
	VPCID           string        `json:"vpc_id"`
	ForwardingRules []lbRuleJSON  `json:"forwarding_rules"`
	HealthCheck     *lbHealthJSON `json:"health_check"`
	DropletIDs      []int         `json:"droplet_ids"`
	Tag             string        `json:"tag"`
	RedirectHTTP    bool          `json:"redirect_http_to_https"`
	CreatedAt       string        `json:"created_at"`
}

func lbView(l godo.LoadBalancer) lbJSON {
	rules := []lbRuleJSON{}
	for _, f := range l.ForwardingRules {
		rules = append(rules, lbRuleJSON{f.EntryProtocol, f.EntryPort, f.TargetProtocol, f.TargetPort, f.CertificateID, f.TlsPassthrough, lbRuleSpec(f)})
	}
	var hc *lbHealthJSON
	if l.HealthCheck != nil {
		h := *l.HealthCheck
		hc = &lbHealthJSON{h.Protocol, h.Port, h.Path, h.CheckIntervalSeconds, h.ResponseTimeoutSeconds, h.HealthyThreshold, h.UnhealthyThreshold, lbHealthSpec(&h)}
	}
	region := ""
	if l.Region != nil {
		region = l.Region.Slug
	}
	return lbJSON{l.ID, l.Name, hasManagedPrefix(l.Name), l.IP, l.Status, region, l.SizeSlug, l.SizeUnit, l.Algorithm, l.VPCUUID, rules, hc,
		nonNil(l.DropletIDs), l.Tag, l.RedirectHttpToHttps, l.Created}
}

type bucketJSON struct {
	Name      string `json:"name"`
	Region    string `json:"region"`
	Managed   bool   `json:"managed"`
	CreatedAt string `json:"created_at"`
}

type grantJSON struct {
	Bucket     string `json:"bucket"` // "" means every bucket
	Permission string `json:"permission"`
}

type spacesKeyJSON struct {
	Name        string      `json:"name"`
	AccessKeyID string      `json:"access_key_id"`
	Grants      []grantJSON `json:"grants"`
	Managed     bool        `json:"managed"`
	CreatedAt   string      `json:"created_at"`
	SecretFile  string      `json:"secret_file"` // where the secret was saved; "" when this was not a create
}

func spacesKeyView(k *godo.SpacesKey, secretFile string) spacesKeyJSON {
	grants := []grantJSON{}
	for _, g := range k.Grants {
		if g != nil {
			grants = append(grants, grantJSON{g.Bucket, string(g.Permission)})
		}
	}
	return spacesKeyJSON{k.Name, k.AccessKey, grants, hasManagedPrefix(k.Name), k.CreatedAt, secretFile}
}
