package main

import (
	"strconv"

	"github.com/digitalocean/godo"
)

// The JSON the tool prints. Every field is always present; empty lists are [] and unknown values are "" or null.

type sizeJSON struct {
	Slug         string   `json:"slug"`
	MemoryMB     int      `json:"memory_mb"`
	VCPUs        int      `json:"vcpus"`
	DiskGB       int      `json:"disk_gb"`
	TransferTB   float64  `json:"transfer_tb"`
	PriceMonthly float64  `json:"price_monthly_usd"`
	PriceHourly  float64  `json:"price_hourly_usd"`
	Available    bool     `json:"available"`
	Regions      []string `json:"regions"`
}

func sizeView(s godo.Size) sizeJSON {
	return sizeJSON{s.Slug, s.Memory, s.Vcpus, s.Disk, s.Transfer, s.PriceMonthly, s.PriceHourly, s.Available, nonNil(s.Regions)}
}

type regionJSON struct {
	Slug      string   `json:"slug"`
	Name      string   `json:"name"`
	Available bool     `json:"available"`
	Features  []string `json:"features"`
}

func regionView(r godo.Region) regionJSON {
	return regionJSON{r.Slug, r.Name, r.Available, nonNil(r.Features)}
}

type imageJSON struct {
	ID           int      `json:"id"`
	Slug         string   `json:"slug"`
	Name         string   `json:"name"`
	Type         string   `json:"type"`
	Distribution string   `json:"distribution"`
	Public       bool     `json:"public"`
	Status       string   `json:"status"`
	MinDiskGB    int      `json:"min_disk_gb"`
	Regions      []string `json:"regions"`
}

func imageView(i godo.Image) imageJSON {
	return imageJSON{i.ID, i.Slug, i.Name, i.Type, i.Distribution, i.Public, i.Status, i.MinDiskSize, nonNil(i.Regions)}
}

type keyJSON struct {
	ID          int    `json:"id"`
	Name        string `json:"name"`
	Fingerprint string `json:"fingerprint"`
	Managed     bool   `json:"managed"`
}

func keyView(k godo.Key) keyJSON {
	return keyJSON{k.ID, k.Name, k.Fingerprint, hasManagedPrefix(k.Name)}
}

type vpcJSON struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Region      string `json:"region"`
	IPRange     string `json:"ip_range"`
	Default     bool   `json:"default"`
	Description string `json:"description"`
	Managed     bool   `json:"managed"`
	CreatedAt   string `json:"created_at"`
}

func vpcView(v *godo.VPC) vpcJSON {
	created := ""
	if !v.CreatedAt.IsZero() {
		created = v.CreatedAt.UTC().Format("2006-01-02T15:04:05Z")
	}
	return vpcJSON{v.ID, v.Name, v.RegionSlug, v.IPRange, v.Default, v.Description, hasManagedPrefix(v.Name), created}
}

type memberJSON struct {
	URN  string `json:"urn"`
	Name string `json:"name"`
}

type vpcDetailJSON struct {
	vpcJSON
	Members []memberJSON `json:"members"`
}

type ruleJSON struct {
	Direction  string   `json:"direction"`
	Protocol   string   `json:"protocol"`
	Ports      string   `json:"ports"`
	Addresses  []string `json:"addresses"`
	Tags       []string `json:"tags"`
	DropletIDs []int    `json:"droplet_ids"`
	Spec       string   `json:"spec"`
	Text       string   `json:"text"`
}

func ruleView(r Rule) ruleJSON {
	return ruleJSON{r.Dir, r.Proto, r.Ports, nonNil(r.Addrs), nonNil(r.Tags), nonNil(r.Droplets), r.Spec(), r.Describe()}
}

type firewallJSON struct {
	ID         string     `json:"id"`
	Name       string     `json:"name"`
	Status     string     `json:"status"`
	Managed    bool       `json:"managed"`
	Rules      []ruleJSON `json:"rules"`
	DropletIDs []int      `json:"droplet_ids"`
	Tags       []string   `json:"tags"`
	CreatedAt  string     `json:"created_at"`
}

func firewallView(f godo.Firewall) firewallJSON {
	rules := []ruleJSON{}
	for _, r := range firewallRules(f) {
		rules = append(rules, ruleView(r))
	}
	return firewallJSON{f.ID, f.Name, f.Status, hasManagedPrefix(f.Name), rules, nonNil(f.DropletIDs), nonNil(f.Tags), f.Created}
}

type dropletImageJSON struct {
	ID   int    `json:"id"`
	Slug string `json:"slug"`
	Name string `json:"name"`
}

type dropletJSON struct {
	ID          int              `json:"id"`
	Name        string           `json:"name"`
	Status      string           `json:"status"`
	Managed     bool             `json:"managed"`
	Region      string           `json:"region"`
	SizeSlug    string           `json:"size_slug"`
	Image       dropletImageJSON `json:"image"`
	VCPUs       int              `json:"vcpus"`
	MemoryMB    int              `json:"memory_mb"`
	DiskGB      int              `json:"disk_gb"`
	PublicIPv4  string           `json:"public_ipv4"`
	PrivateIPv4 string           `json:"private_ipv4"`
	PublicIPv6  string           `json:"public_ipv6"`
	VPCID       string           `json:"vpc_id"`
	Tags        []string         `json:"tags"`
	MonthlyCost float64          `json:"price_monthly_usd"`
	CreatedAt   string           `json:"created_at"`
}

func dropletIPs(d godo.Droplet) (pub4, priv4, pub6 string) {
	if d.Networks == nil {
		return
	}
	for _, n := range d.Networks.V4 {
		switch n.Type {
		case "public":
			if pub4 == "" {
				pub4 = n.IPAddress
			}
		case "private":
			if priv4 == "" {
				priv4 = n.IPAddress
			}
		}
	}
	for _, n := range d.Networks.V6 {
		if n.Type == "public" && pub6 == "" {
			pub6 = n.IPAddress
		}
	}
	return
}

func dropletView(d godo.Droplet) dropletJSON {
	pub4, priv4, pub6 := dropletIPs(d)
	region, size := "", d.SizeSlug
	if d.Region != nil {
		region = d.Region.Slug
	}
	cost := 0.0
	if d.Size != nil {
		cost = d.Size.PriceMonthly
		if size == "" {
			size = d.Size.Slug
		}
	}
	img := dropletImageJSON{}
	if d.Image != nil {
		img = dropletImageJSON{d.Image.ID, d.Image.Slug, d.Image.Name}
	}
	return dropletJSON{d.ID, d.Name, d.Status, hasTag(d.Tags, managedTag), region, size, img, d.Vcpus, d.Memory, d.Disk,
		pub4, priv4, pub6, d.VPCUUID, nonNil(d.Tags), cost, d.Created}
}

func itoa(i int) string { return strconv.Itoa(i) }
