package main

import (
	"fmt"
	"strings"

	"github.com/digitalocean/godo"
)

func (a *App) cmdVPC(args []string) error {
	if len(args) == 0 {
		return usagef("usage: docli vpc list|get|create|delete (try: docli help vpc)")
	}
	switch args[0] {
	case "list":
		return a.vpcList(args[1:])
	case "get":
		return a.vpcGet(args[1:])
	case "create":
		return a.vpcCreate(args[1:])
	case "delete":
		return a.vpcDelete(args[1:])
	}
	return usagef("unknown vpc command %q (list, get, create, delete)", args[0])
}

func findVPC(vpcs []*godo.VPC, ref string) (*godo.VPC, error) {
	var hits []*godo.VPC
	for _, v := range vpcs {
		if v.Name == ref || v.ID == ref {
			hits = append(hits, v)
		}
	}
	if len(hits) == 0 {
		for _, v := range vpcs {
			if v.Name == namePrefix+ref {
				hits = append(hits, v)
			}
		}
	}
	switch len(hits) {
	case 0:
		return nil, fmt.Errorf("no VPC named %q (or with that id). List them with: docli vpc list", ref)
	case 1:
		return hits[0], nil
	}
	return nil, fmt.Errorf("%d VPCs are named %q; use the id instead", len(hits), ref)
}

func (a *App) vpcList(args []string) error {
	fs, g := a.newFlags("vpc list")
	if _, err := a.parseFlags(fs, args, "vpc list"); err != nil {
		return err
	}
	r, err := a.start(*g)
	if err != nil {
		return err
	}
	vpcs, err := r.Cloud.ListVPCs(r.Ctx)
	if err != nil {
		return err
	}
	var shown []*godo.VPC
	for _, v := range vpcs {
		if r.G.Region == "" || v.RegionSlug == r.G.Region {
			shown = append(shown, v)
		}
	}
	if g.JSON {
		out := []vpcJSON{}
		for _, v := range shown {
			out = append(out, vpcView(v))
		}
		return r.printJSON(out)
	}
	var rows [][]string
	for _, v := range shown {
		rows = append(rows, []string{v.ID, v.Name, v.RegionSlug, v.IPRange, yesNo(v.Default), yesNo(hasManagedPrefix(v.Name))})
	}
	printTable(a.Stdout, []string{"ID", "NAME", "REGION", "IP RANGE", "DEFAULT", "MANAGED"}, rows)
	return nil
}

func (a *App) vpcGet(args []string) error {
	fs, g := a.newFlags("vpc get")
	pos, err := a.parseFlags(fs, args, "vpc get")
	if err != nil {
		return err
	}
	if len(pos) != 1 {
		return usagef("usage: docli vpc get NAME|ID")
	}
	r, err := a.start(*g)
	if err != nil {
		return err
	}
	vpcs, err := r.Cloud.ListVPCs(r.Ctx)
	if err != nil {
		return err
	}
	v, err := findVPC(vpcs, pos[0])
	if err != nil {
		return err
	}
	members, err := r.Cloud.ListVPCMembers(r.Ctx, v.ID)
	if err != nil {
		return err
	}
	det := vpcDetailJSON{vpcJSON: vpcView(v), Members: []memberJSON{}}
	for _, m := range members {
		det.Members = append(det.Members, memberJSON{m.URN, m.Name})
	}
	if g.JSON {
		return r.printJSON(det)
	}
	fmt.Fprintf(a.Stdout, "ID:        %s\nName:      %s\nRegion:    %s\nIP range:  %s\nDefault:   %s\nManaged:   %s\nCreated:   %s\nMembers:   %d\n",
		v.ID, v.Name, v.RegionSlug, v.IPRange, yesNo(v.Default), yesNo(hasManagedPrefix(v.Name)), orDash(det.CreatedAt), len(members))
	for _, m := range det.Members {
		fmt.Fprintf(a.Stdout, "  - %s (%s)\n", m.Name, m.URN)
	}
	return nil
}

func (a *App) vpcCreate(args []string) error {
	fs, g := a.newFlags("vpc create")
	name := fs.String("name", "", "")
	ipRange := fs.String("ip-range", "", "")
	desc := fs.String("description", "", "")
	mo := addManifestFlags(fs)
	pos, err := a.parseFlags(fs, args, "vpc create")
	if err != nil {
		return err
	}
	if len(pos) > 0 || *name == "" {
		return usagef("usage: docli vpc create --name NAME --region REGION [--ip-range 10.10.10.0/24]")
	}
	final := ensureNamePrefix(*name)
	if err := validateResourceName("VPC", final); err != nil {
		return err
	}
	if *ipRange != "" {
		if _, err := normalizeAddr(*ipRange); err != nil || !strings.Contains(*ipRange, "/") {
			return usagef("--ip-range %q must be a CIDR range such as 10.10.10.0/24", *ipRange)
		}
	}
	r, err := a.start(*g)
	if err != nil {
		return err
	}
	region := r.G.Region
	if region == "" {
		return usagef("a region is required: --region nyc3 (or set DOCLI_REGION)")
	}
	vpcs, err := r.Cloud.ListVPCs(r.Ctx)
	if err != nil {
		return err
	}
	for _, v := range vpcs {
		if v.Name == final {
			return fmt.Errorf("a VPC named %q already exists (id %s). Nothing was changed", final, v.ID)
		}
	}
	rng := *ipRange
	if rng == "" {
		rng = "chosen by DigitalOcean"
	}
	p := Plan{Action: "vpc.create", Summary: fmt.Sprintf("create VPC %q in %s", final, region), Steps: []string{"IP range: " + rng}}
	if final != *name {
		p.Steps = append(p.Steps, fmt.Sprintf("the name gets the prefix %s so docli can tell it created this VPC (VPCs cannot carry tags)", namePrefix))
	}
	ok, err := r.Approve(p, approveOpts{})
	if !ok || err != nil {
		return err
	}
	v, err := r.Cloud.CreateVPC(r.Ctx, &godo.VPCCreateRequest{Name: final, RegionSlug: region, IPRange: *ipRange, Description: *desc})
	if err != nil {
		return err
	}
	r.updateManifest(*mo, false, change{Section: "vpcs", ID: v.ID})
	if g.JSON {
		return r.printJSON(vpcView(v))
	}
	fmt.Fprintf(a.Stdout, "Created VPC %q (id %s, %s, %s)\n", v.Name, v.ID, v.RegionSlug, v.IPRange)
	return nil
}

func (a *App) vpcDelete(args []string) error {
	fs, g := a.newFlags("vpc delete")
	force := fs.Bool("force-unmanaged", false, "")
	confirm := fs.String("confirm-name", "", "")
	mo := addManifestFlags(fs)
	pos, err := a.parseFlags(fs, args, "vpc delete")
	if err != nil {
		return err
	}
	if len(pos) != 1 {
		return usagef("usage: docli vpc delete NAME|ID")
	}
	r, err := a.start(*g)
	if err != nil {
		return err
	}
	vpcs, err := r.Cloud.ListVPCs(r.Ctx)
	if err != nil {
		return err
	}
	v, err := findVPC(vpcs, pos[0])
	if err != nil {
		return err
	}
	managed := hasManagedPrefix(v.Name)
	if !managed && !*force {
		// the ownership refusal comes first, with its own explanation
		_, err := r.ApproveDelete(deleteSpec{Kind: "vpc", Name: v.Name, Managed: false, Rule: "a name starting with " + namePrefix}, false, "")
		return err
	}
	if v.Default {
		return refusedf("refusing to delete %q: it is the default VPC for %s. Nothing was changed", v.Name, v.RegionSlug)
	}
	members, err := r.Cloud.ListVPCMembers(r.Ctx, v.ID)
	if err != nil {
		return err
	}
	if len(members) > 0 {
		var names []string
		for _, m := range members {
			names = append(names, fmt.Sprintf("%s (%s)", m.Name, m.URN))
		}
		return refusedf("refusing to delete VPC %q: it still has %d member(s): %s. Delete or move them first. Nothing was changed", v.Name, len(members), strings.Join(names, ", "))
	}
	ok, err := r.ApproveDelete(deleteSpec{Kind: "vpc", Name: v.Name, Managed: managed, Rule: "a name starting with " + namePrefix,
		Steps: []string{fmt.Sprintf("delete VPC id %s in %s (no members)", v.ID, v.RegionSlug)}}, *force, *confirm)
	if !ok || err != nil {
		return err
	}
	if err := r.Cloud.DeleteVPC(r.Ctx, v.ID); err != nil {
		return err
	}
	r.updateManifest(*mo, false, change{Section: "vpcs", ID: v.ID})
	return r.deleted("vpc", v.ID, v.Name)
}
