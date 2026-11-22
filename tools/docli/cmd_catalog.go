package main

import (
	"fmt"
	"sort"
	"strings"

	"github.com/digitalocean/godo"
)

func (a *App) cmdSizes(args []string) error {
	fs, g := a.newFlags("sizes")
	maxPrice := fs.Float64("max-price", 100, "")
	all := fs.Bool("all", false, "")
	if _, err := a.parseFlags(fs, args, "sizes"); err != nil {
		return err
	}
	r, err := a.start(*g)
	if err != nil {
		return err
	}
	sizes, err := r.Cloud.ListSizes(r.Ctx)
	if err != nil {
		return err
	}
	var shown []godo.Size
	hidden := 0
	for _, s := range sizes {
		ok := *all || (s.Available && (*maxPrice <= 0 || s.PriceMonthly <= *maxPrice))
		if ok && r.G.Region != "" && !contains(s.Regions, r.G.Region) {
			ok = false
		}
		if ok {
			shown = append(shown, s)
		} else {
			hidden++
		}
	}
	sort.SliceStable(shown, func(i, j int) bool { return shown[i].PriceMonthly < shown[j].PriceMonthly })
	if g.JSON {
		out := []sizeJSON{}
		for _, s := range shown {
			out = append(out, sizeView(s))
		}
		return r.printJSON(out)
	}
	var rows [][]string
	for _, s := range shown {
		rows = append(rows, []string{s.Slug, itoa(s.Vcpus), fmt.Sprintf("%d MB", s.Memory), fmt.Sprintf("%d GB", s.Disk),
			money(s.PriceMonthly), fmt.Sprintf("$%.5f", s.PriceHourly), yesNo(s.Available)})
	}
	printTable(a.Stdout, []string{"SLUG", "VCPUS", "MEMORY", "DISK", "MONTHLY", "HOURLY", "AVAILABLE"}, rows)
	if hidden > 0 {
		r.infof("%d more size(s) hidden (unavailable, over $%.0f a month, or not in the chosen region). Use --all to see every size.", hidden, *maxPrice)
	}
	return nil
}

func contains(l []string, s string) bool {
	for _, x := range l {
		if x == s {
			return true
		}
	}
	return false
}

func (a *App) cmdRegions(args []string) error {
	fs, g := a.newFlags("regions")
	all := fs.Bool("all", false, "")
	if _, err := a.parseFlags(fs, args, "regions"); err != nil {
		return err
	}
	r, err := a.start(*g)
	if err != nil {
		return err
	}
	regions, err := r.Cloud.ListRegions(r.Ctx)
	if err != nil {
		return err
	}
	var shown []godo.Region
	for _, x := range regions {
		if *all || x.Available {
			shown = append(shown, x)
		}
	}
	sort.Slice(shown, func(i, j int) bool { return shown[i].Slug < shown[j].Slug })
	if g.JSON {
		out := []regionJSON{}
		for _, x := range shown {
			out = append(out, regionView(x))
		}
		return r.printJSON(out)
	}
	var rows [][]string
	for _, x := range shown {
		rows = append(rows, []string{x.Slug, x.Name, yesNo(x.Available), strings.Join(x.Features, ",")})
	}
	printTable(a.Stdout, []string{"SLUG", "NAME", "AVAILABLE", "FEATURES"}, rows)
	return nil
}

func (a *App) cmdImages(args []string) error {
	fs, g := a.newFlags("images")
	typ := fs.String("type", "distribution", "")
	slug := fs.String("slug", "", "")
	if _, err := a.parseFlags(fs, args, "images"); err != nil {
		return err
	}
	if *typ != "distribution" && *typ != "application" && *typ != "custom" {
		return usagef("--type must be distribution, application or custom, not %q", *typ)
	}
	r, err := a.start(*g)
	if err != nil {
		return err
	}
	var images []godo.Image
	if *slug != "" {
		img, err := r.Cloud.GetImageBySlug(r.Ctx, *slug)
		if err != nil {
			if isNotFound(err) {
				return fmt.Errorf("no image with the slug %q (list slugs with: docli images)", *slug)
			}
			return err
		}
		images = []godo.Image{*img}
	} else {
		images, err = r.Cloud.ListImages(r.Ctx, *typ)
		if err != nil {
			return err
		}
	}
	var shown []godo.Image
	for _, i := range images {
		if r.G.Region == "" || contains(i.Regions, r.G.Region) {
			shown = append(shown, i)
		}
	}
	sort.Slice(shown, func(i, j int) bool { return shown[i].Slug < shown[j].Slug })
	if g.JSON {
		out := []imageJSON{}
		for _, i := range shown {
			out = append(out, imageView(i))
		}
		return r.printJSON(out)
	}
	var rows [][]string
	for _, i := range shown {
		rows = append(rows, []string{orDash(i.Slug), itoa(i.ID), i.Name, i.Distribution, i.Type})
	}
	printTable(a.Stdout, []string{"SLUG", "ID", "NAME", "DISTRIBUTION", "TYPE"}, rows)
	return nil
}
