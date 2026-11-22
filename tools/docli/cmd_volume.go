package main

import (
	"fmt"
	"regexp"
	"strings"

	"github.com/digitalocean/godo"
)

// Block storage volumes. They can be tagged, so docli owns the ones tagged docli:managed (the rule the droplets
// follow). A volume holds data, so deleting needs the name typed and an attached volume is refused. The API
// can grow a volume but never shrink it.

func (a *App) cmdVolume(args []string) error {
	if len(args) == 0 {
		return usagef("usage: docli volume list|get|create|delete|attach|detach|resize (try: docli help volume)")
	}
	switch args[0] {
	case "list":
		return a.volumeList(args[1:])
	case "get":
		return a.volumeGet(args[1:])
	case "create":
		return a.volumeCreate(args[1:])
	case "delete":
		return a.volumeDelete(args[1:])
	case "attach", "detach":
		return a.volumeAttach(args[0], args[1:])
	case "resize":
		return a.volumeResize(args[1:])
	}
	return usagef("unknown volume command %q (list, get, create, delete, attach, detach, resize)", args[0])
}

var volumeNameRE = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]{0,62}[a-z0-9])?$`)

const maxVolumeGB = 16000

func volumeRegion(v godo.Volume) string {
	if v.Region != nil {
		return v.Region.Slug
	}
	return ""
}

// findVolume looks a volume up by id or name. A volume name is only unique within a region, so --region narrows.
func findVolume(vols []godo.Volume, ref, region string) (*godo.Volume, error) {
	var hits []godo.Volume
	for _, v := range vols {
		if (v.ID == ref || v.Name == ref) && (region == "" || v.ID == ref || volumeRegion(v) == region) {
			hits = append(hits, v)
		}
	}
	switch len(hits) {
	case 0:
		return nil, fmt.Errorf("no volume named %q (or with that id). List them with: docli volume list", ref)
	case 1:
		return &hits[0], nil
	}
	return nil, fmt.Errorf("%d volumes are named %q (names are unique only within a region); add --region or use the id", len(hits), ref)
}

func (r *Run) dropletNamesByID() map[int]string {
	out := map[int]string{}
	ds, err := r.Cloud.ListDroplets(r.Ctx, "")
	if err != nil {
		return out
	}
	for _, d := range ds {
		out[d.ID] = d.Name
	}
	return out
}

func (a *App) volumeList(args []string) error {
	fs, g := a.newFlags("volume list")
	if _, err := a.parseFlags(fs, args, "volume list"); err != nil {
		return err
	}
	r, err := a.start(*g)
	if err != nil {
		return err
	}
	vols, err := r.Cloud.ListVolumes(r.Ctx)
	if err != nil {
		return err
	}
	var shown []godo.Volume
	for _, v := range vols {
		if r.G.Region == "" || volumeRegion(v) == r.G.Region {
			shown = append(shown, v)
		}
	}
	if g.JSON {
		out := []volumeJSON{}
		for _, v := range shown {
			out = append(out, volumeView(v))
		}
		return r.printJSON(out)
	}
	names := r.dropletNamesByID()
	var rows [][]string
	for _, v := range shown {
		rows = append(rows, []string{v.ID, v.Name, volumeRegion(v), fmt.Sprintf("%d GB", v.SizeGigaBytes), orDash(attachedTo(v, names)), yesNo(hasTag(v.Tags, managedTag))})
	}
	printTable(a.Stdout, []string{"ID", "NAME", "REGION", "SIZE", "ATTACHED TO", "MANAGED"}, rows)
	return nil
}

func attachedTo(v godo.Volume, names map[int]string) string {
	var out []string
	for _, id := range v.DropletIDs {
		if n := names[id]; n != "" {
			out = append(out, n)
		} else {
			out = append(out, itoa(id))
		}
	}
	return strings.Join(out, ", ")
}

func (a *App) volumeGet(args []string) error {
	fs, g := a.newFlags("volume get")
	pos, err := a.parseFlags(fs, args, "volume get")
	if err != nil {
		return err
	}
	if len(pos) != 1 {
		return usagef("usage: docli volume get NAME|ID [--region R]")
	}
	r, err := a.start(*g)
	if err != nil {
		return err
	}
	vols, err := r.Cloud.ListVolumes(r.Ctx)
	if err != nil {
		return err
	}
	v, err := findVolume(vols, pos[0], r.G.Region)
	if err != nil {
		return err
	}
	if g.JSON {
		return r.printJSON(volumeView(*v))
	}
	fmt.Fprintf(a.Stdout, "ID:          %s\nName:        %s\nManaged:     %s\nRegion:      %s\nSize:        %d GB\nAttached to: %s\nFilesystem:  %s\nLabel:       %s\nTags:        %s\nDescription: %s\nCreated:     %s\n",
		v.ID, v.Name, yesNo(hasTag(v.Tags, managedTag)), volumeRegion(*v), v.SizeGigaBytes, orDash(attachedTo(*v, r.dropletNamesByID())),
		orDash(v.FilesystemType), orDash(v.FilesystemLabel), orDash(strings.Join(v.Tags, ",")), orDash(v.Description), orDash(fmtTime(v.CreatedAt)))
	return nil
}

const volumeCostNote = "monthly cost: not shown. The API does not report block storage prices, so docli will not guess; see DigitalOcean's pricing page"

func (a *App) volumeCreate(args []string) error {
	fs, g := a.newFlags("volume create")
	name := fs.String("name", "", "")
	size := fs.Int("size", 0, "")
	desc := fs.String("description", "", "")
	fsType := fs.String("filesystem-type", "", "")
	fsLabel := fs.String("filesystem-label", "", "")
	stack := fs.String("stack", "", "")
	projRef := fs.String("project", "", "")
	var userTags stringList
	fs.Var(&userTags, "tag", "")
	mo := addManifestFlags(fs)
	pos, err := a.parseFlags(fs, args, "volume create")
	if err != nil {
		return err
	}
	if len(pos) > 0 || *name == "" || *size == 0 {
		return usagef("usage: docli volume create --name NAME --region REGION --size GB [--description T] [--filesystem-type ext4|xfs] [--filesystem-label L] [--tag T] [--stack S] [--project P]")
	}
	if !volumeNameRE.MatchString(*name) {
		return usagef("%q is not a valid volume name: lower case letters, digits and dashes, 1-64 characters, not starting or ending with a dash", *name)
	}
	if *size < 1 || *size > maxVolumeGB {
		return usagef("--size must be between 1 and %d (gigabytes)", maxVolumeGB)
	}
	if *fsType != "" && *fsType != "ext4" && *fsType != "xfs" {
		return usagef("--filesystem-type must be ext4 or xfs (DigitalOcean formats the volume on first attach)")
	}
	tags := []string{managedTag}
	if *stack != "" {
		if !stackRE.MatchString(*stack) {
			return usagef("--stack %q must use letters, digits, dash and underscore only", *stack)
		}
		tags = append(tags, "docli:stack:"+*stack)
	}
	for _, t := range userTags {
		if err := validateTag(t); err != nil {
			return usagef("%v", err)
		}
		if !hasTag(tags, t) {
			tags = append(tags, t)
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
	proj, err := r.lookupProject(*projRef)
	if err != nil {
		return err
	}
	vols, err := r.Cloud.ListVolumes(r.Ctx)
	if err != nil {
		return err
	}
	for _, v := range vols {
		if v.Name == *name && volumeRegion(v) == region {
			return fmt.Errorf("a volume named %q already exists in %s (id %s). Nothing was changed", *name, region, v.ID)
		}
	}
	p := Plan{Action: "volume.create", Summary: fmt.Sprintf("create the %d GB volume %q in %s", *size, *name, region)}
	p.Steps = []string{volumeCostNote, "tags that will be applied: " + strings.Join(tags, ", ") + " (created if missing)",
		"it starts unattached; attach it with: docli volume attach " + *name + " DROPLET"}
	if *fsType != "" {
		p.Steps = append(p.Steps, "filesystem: "+*fsType)
	}
	if s := projectStep(proj); s != "" {
		p.Steps = append(p.Steps, s)
	}
	ok, err := r.Approve(p, approveOpts{})
	if !ok || err != nil {
		return err
	}
	for _, t := range tags {
		if err := r.Cloud.EnsureTag(r.Ctx, t); err != nil {
			return fmt.Errorf("could not create the tag %q, so no volume was created: %v", t, err)
		}
	}
	v, err := r.Cloud.CreateVolume(r.Ctx, &godo.VolumeCreateRequest{Region: region, Name: *name, Description: *desc, SizeGigaBytes: int64(*size),
		FilesystemType: *fsType, FilesystemLabel: *fsLabel, Tags: tags})
	if err != nil {
		return madeOrUnknown("volume", err, "docli volume list --region "+region)
	}
	r.updateManifest(*mo, false, change{Section: "volumes", ID: v.ID})
	if err := r.putInProject(proj, v.URN(), "volume", v.Name); err != nil {
		return err
	}
	if g.JSON {
		return r.printJSON(volumeView(*v))
	}
	fmt.Fprintf(a.Stdout, "Created volume %q (id %s, %d GB, %s)\n", v.Name, v.ID, v.SizeGigaBytes, volumeRegion(*v))
	return nil
}

func (a *App) volumeDelete(args []string) error {
	fs, g := a.newFlags("volume delete")
	force := fs.Bool("force-unmanaged", false, "")
	confirm := fs.String("confirm-name", "", "")
	mo := addManifestFlags(fs)
	pos, err := a.parseFlags(fs, args, "volume delete")
	if err != nil {
		return err
	}
	if len(pos) != 1 {
		return usagef("usage: docli volume delete NAME|ID [--region R] [--yes --confirm-name NAME]")
	}
	r, err := a.start(*g)
	if err != nil {
		return err
	}
	vols, err := r.Cloud.ListVolumes(r.Ctx)
	if err != nil {
		return err
	}
	v, err := findVolume(vols, pos[0], r.G.Region)
	if err != nil {
		return err
	}
	managed := hasTag(v.Tags, managedTag)
	rule := "the tag " + managedTag
	if !managed && !*force {
		_, err := r.ApproveDelete(deleteSpec{Kind: "volume", Name: v.Name, Managed: false, Rule: rule}, false, "")
		return err
	}
	if len(v.DropletIDs) > 0 {
		return refusedf("refusing to delete volume %q: it is attached to %s. Detach it first (docli volume detach %s). Nothing was changed", v.Name, attachedTo(*v, r.dropletNamesByID()), v.Name)
	}
	ok, err := r.ApproveDelete(deleteSpec{Kind: "volume", Name: v.Name, Managed: managed, Rule: rule, Always: true,
		Steps: []string{fmt.Sprintf("permanently destroy volume %q (id %s, %d GB in %s); everything stored on it is lost and cannot be recovered", v.Name, v.ID, v.SizeGigaBytes, volumeRegion(*v))}}, *force, *confirm)
	if !ok || err != nil {
		return err
	}
	if err := r.Cloud.DeleteVolume(r.Ctx, v.ID); err != nil {
		return err
	}
	r.updateManifest(*mo, false, change{Section: "volumes", ID: v.ID})
	return r.deleted("volume", v.ID, v.Name)
}

// requireManagedVolume refuses to change a volume docli did not create unless --force-unmanaged is given and
// the name is typed.
func (r *Run) volumeGuard(v *godo.Volume, verb string, force bool) (typed string, warn string, err error) {
	if hasTag(v.Tags, managedTag) {
		return "", "", nil
	}
	if !force {
		return "", "", refusedf("refusing to %s volume %q: it does not have the tag %s, so docli did not create it. Nothing was changed. If you are sure, add --force-unmanaged and type the name", verb, v.Name, managedTag)
	}
	return v.Name, fmt.Sprintf("volume %q was NOT created by docli (no tag %s). You are forcing the %s.", v.Name, managedTag, verb), nil
}

func (a *App) volumeAttach(verb string, args []string) error {
	fs, g := a.newFlags("volume " + verb)
	force := fs.Bool("force-unmanaged", false, "")
	confirm := fs.String("confirm-name", "", "")
	mo := addManifestFlags(fs)
	pos, err := a.parseFlags(fs, args, "volume "+verb)
	if err != nil {
		return err
	}
	if len(pos) != 2 {
		return usagef("usage: docli volume %s VOLUME DROPLET [--region R]", verb)
	}
	r, err := a.start(*g)
	if err != nil {
		return err
	}
	vols, err := r.Cloud.ListVolumes(r.Ctx)
	if err != nil {
		return err
	}
	v, err := findVolume(vols, pos[0], r.G.Region)
	if err != nil {
		return err
	}
	d, err := r.resolveDroplet(pos[1])
	if err != nil {
		return err
	}
	typed, warn, err := r.volumeGuard(v, verb, *force)
	if err != nil {
		return err
	}
	p := Plan{Action: "volume." + verb}
	if warn != "" {
		p.Warnings = append(p.Warnings, warn)
	}
	attachedHere := false
	for _, id := range v.DropletIDs {
		attachedHere = attachedHere || id == d.ID
	}
	if verb == "attach" {
		switch {
		case attachedHere:
			return fmt.Errorf("volume %q is already attached to droplet %q. Nothing was changed", v.Name, d.Name)
		case len(v.DropletIDs) > 0:
			return refusedf("refusing to attach volume %q: it is already attached to %s, and a volume can be on one droplet at a time. Detach it first. Nothing was changed", v.Name, attachedTo(*v, r.dropletNamesByID()))
		case d.Region == nil || d.Region.Slug != volumeRegion(*v):
			return fmt.Errorf("volume %q is in %s but droplet %q is in %s: a volume can only be attached within its region. Nothing was changed", v.Name, volumeRegion(*v), d.Name, dropletView(*d).Region)
		}
		p.Summary = fmt.Sprintf("attach volume %q to droplet %q", v.Name, d.Name)
		p.Steps = []string{"the volume appears on the droplet as a block device; mount it there (DigitalOcean formats it on first attach if you chose a filesystem type)"}
		if !hasTag(d.Tags, managedTag) {
			p.Warnings = append(p.Warnings, fmt.Sprintf("droplet %q was not created by docli; only the volume changes on DigitalOcean's side, but the droplet gains a disk", d.Name))
		}
	} else {
		if !attachedHere {
			return fmt.Errorf("volume %q is not attached to droplet %q. Nothing was changed", v.Name, d.Name)
		}
		p.Summary = fmt.Sprintf("detach volume %q from droplet %q", v.Name, d.Name)
		p.Steps = []string{"the data on the volume is kept"}
		p.Warnings = append(p.Warnings, fmt.Sprintf("unmount the volume on %q first. Detaching a mounted volume can corrupt its filesystem and break what is using it", d.Name))
	}
	ok, err := r.Approve(p, approveOpts{TypeName: typed, ConfirmName: *confirm})
	if !ok || err != nil {
		return err
	}
	if verb == "attach" {
		err = r.Cloud.AttachVolume(r.Ctx, v.ID, d.ID)
	} else {
		err = r.Cloud.DetachVolume(r.Ctx, v.ID, d.ID)
	}
	if err != nil {
		return err
	}
	r.updateManifest(*mo, false, change{Section: "volumes", ID: v.ID})
	return r.volumeResult(v.ID, fmt.Sprintf("Requested %s of volume %q (droplet %q). DigitalOcean completes it in the background.", verb, v.Name, d.Name))
}

// volumeResult prints the volume as the account now has it.
func (r *Run) volumeResult(id, msg string) error {
	vols, err := r.Cloud.ListVolumes(r.Ctx)
	if err != nil {
		return err
	}
	for _, v := range vols {
		if v.ID == id {
			if r.G.JSON {
				return r.printJSON(volumeView(v))
			}
			fmt.Fprintln(r.Stdout, msg)
			return nil
		}
	}
	return fmt.Errorf("the volume %s was changed but is no longer listed", id)
}

func (a *App) volumeResize(args []string) error {
	fs, g := a.newFlags("volume resize")
	size := fs.Int("size", 0, "")
	force := fs.Bool("force-unmanaged", false, "")
	confirm := fs.String("confirm-name", "", "")
	mo := addManifestFlags(fs)
	pos, err := a.parseFlags(fs, args, "volume resize")
	if err != nil {
		return err
	}
	if len(pos) != 1 || *size == 0 {
		return usagef("usage: docli volume resize NAME|ID --size GB [--region R]   (a volume can only grow)")
	}
	r, err := a.start(*g)
	if err != nil {
		return err
	}
	vols, err := r.Cloud.ListVolumes(r.Ctx)
	if err != nil {
		return err
	}
	v, err := findVolume(vols, pos[0], r.G.Region)
	if err != nil {
		return err
	}
	switch {
	case int64(*size) == v.SizeGigaBytes:
		return fmt.Errorf("volume %q is already %d GB. Nothing was changed", v.Name, v.SizeGigaBytes)
	case int64(*size) < v.SizeGigaBytes:
		return refusedf("refusing to resize volume %q from %d GB down to %d GB: DigitalOcean volumes can only grow, never shrink. To get a smaller one, create a new volume and copy the data over. Nothing was changed", v.Name, v.SizeGigaBytes, *size)
	case *size > maxVolumeGB:
		return usagef("--size must be at most %d (gigabytes)", maxVolumeGB)
	}
	typed, warn, err := r.volumeGuard(v, "resize", *force)
	if err != nil {
		return err
	}
	p := Plan{Action: "volume.resize", Summary: fmt.Sprintf("grow volume %q from %d GB to %d GB", v.Name, v.SizeGigaBytes, *size),
		Steps: []string{"this cannot be undone: a volume can never be made smaller again", volumeCostNote,
			"the filesystem is not grown for you: after the resize, grow it on the droplet (for ext4: resize2fs on the device)"}}
	if warn != "" {
		p.Warnings = append(p.Warnings, warn)
	}
	ok, err := r.Approve(p, approveOpts{TypeName: typed, ConfirmName: *confirm})
	if !ok || err != nil {
		return err
	}
	if err := r.Cloud.ResizeVolume(r.Ctx, v.ID, *size, volumeRegion(*v)); err != nil {
		return err
	}
	r.updateManifest(*mo, false, change{Section: "volumes", ID: v.ID})
	return r.volumeResult(v.ID, fmt.Sprintf("Requested a resize of volume %q to %d GB.", v.Name, *size))
}
