package main

import (
	"fmt"
	"regexp"
	"strings"

	"github.com/digitalocean/godo"
)

// Spaces: `spaces bucket list|create|delete` and `spaces key list|create|delete`.
//
// Keys use DigitalOcean's REST API (/v2/spaces/keys, in godo as SpacesKeys). Buckets are not in the REST API at
// all: they go through the S3-compatible API with a Spaces key (spaces.go). Both kinds of thing cannot be tagged,
// so docli owns the ones whose names start with docli-.

func (a *App) cmdSpaces(args []string) error {
	if len(args) < 2 {
		return usagef("usage: docli spaces bucket|key list|create|delete (try: docli help spaces)")
	}
	switch args[0] + " " + args[1] {
	case "bucket list":
		return a.bucketList(args[2:])
	case "bucket create":
		return a.bucketCreate(args[2:])
	case "bucket delete":
		return a.bucketDelete(args[2:])
	case "key list":
		return a.spacesKeyList(args[2:])
	case "key create":
		return a.spacesKeyCreate(args[2:])
	case "key delete":
		return a.spacesKeyDelete(args[2:])
	}
	return usagef("unknown command \"spaces %s %s\" (spaces bucket|key list|create|delete)", args[0], args[1])
}

var bucketNameRE = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{1,61}[a-z0-9]$`)

func (r *Run) bucketRegion() (string, error) {
	if r.G.Region == "" {
		return "", usagef("a region is required: --region nyc3 (or set DOCLI_REGION). Spaces buckets are listed and made one region at a time")
	}
	return r.G.Region, nil
}

func (r *Run) spaces() (*Spaces, error) { return r.App.newSpaces(r.G) }

const spacesCostNote = "monthly cost: not shown. The API does not report Spaces prices (a Spaces subscription has a base charge), so docli will not guess; see DigitalOcean's pricing page"

func (a *App) bucketList(args []string) error {
	fs, g := a.newFlags("spaces bucket list")
	if _, err := a.parseFlags(fs, args, "spaces bucket list"); err != nil {
		return err
	}
	r, err := a.start(*g)
	if err != nil {
		return err
	}
	region, err := r.bucketRegion()
	if err != nil {
		return err
	}
	sp, err := r.spaces()
	if err != nil {
		return err
	}
	bs, err := sp.ListBuckets(r.Ctx, region)
	if err != nil {
		return err
	}
	if g.JSON {
		out := []bucketJSON{}
		for _, b := range bs {
			out = append(out, bucketJSON{b.Name, region, hasManagedPrefix(b.Name), b.Created})
		}
		return r.printJSON(out)
	}
	var rows [][]string
	for _, b := range bs {
		rows = append(rows, []string{b.Name, region, orDash(b.Created), yesNo(hasManagedPrefix(b.Name))})
	}
	printTable(a.Stdout, []string{"NAME", "REGION", "CREATED", "MANAGED"}, rows)
	return nil
}

func (a *App) bucketCreate(args []string) error {
	fs, g := a.newFlags("spaces bucket create")
	name := fs.String("name", "", "")
	mo := addManifestFlags(fs)
	pos, err := a.parseFlags(fs, args, "spaces bucket create")
	if err != nil {
		return err
	}
	if len(pos) > 0 || *name == "" {
		return usagef("usage: docli spaces bucket create --name NAME --region REGION")
	}
	final := ensureNamePrefix(*name)
	if !bucketNameRE.MatchString(final) {
		return usagef("%q is not a valid bucket name: 3-63 characters, lower case letters, digits and dashes, not starting or ending with a dash", final)
	}
	r, err := a.start(*g)
	if err != nil {
		return err
	}
	region, err := r.bucketRegion()
	if err != nil {
		return err
	}
	sp, err := r.spaces()
	if err != nil {
		return err
	}
	have, err := sp.ListBuckets(r.Ctx, region)
	if err != nil {
		return err
	}
	for _, b := range have {
		if b.Name == final {
			return fmt.Errorf("a bucket named %q already exists in %s. Nothing was changed", final, region)
		}
	}
	p := Plan{Action: "spaces.bucket.create", Summary: fmt.Sprintf("create the private bucket %q in %s", final, region),
		Steps: []string{spacesCostNote, "done through the S3-compatible API with your Spaces key (the REST API cannot create buckets)",
			"the bucket is private; docli does not change who can read it",
			fmt.Sprintf("the name gets the prefix %s so docli can tell it created this bucket (buckets cannot carry tags)", namePrefix)}}
	if final != *name {
		p.Steps = append(p.Steps, "bucket names are unique across a whole region, so a short name may already be taken by someone else")
	}
	ok, err := r.Approve(p, approveOpts{})
	if !ok || err != nil {
		return err
	}
	if err := sp.CreateBucket(r.Ctx, region, final); err != nil {
		if _, isSE := err.(*spacesError); !isSE {
			return fmt.Errorf("the create request failed and it is not known whether the bucket was made (%v). docli does not retry a create. Check with: docli spaces bucket list --region %s", err, region)
		}
		return err
	}
	r.updateManifest(*mo, false, change{Section: "spaces_buckets", ID: region + "/" + final})
	if g.JSON {
		return r.printJSON(bucketJSON{final, region, true, ""})
	}
	fmt.Fprintf(a.Stdout, "Created private bucket %q in %s\n", final, region)
	return nil
}

func (a *App) bucketDelete(args []string) error {
	fs, g := a.newFlags("spaces bucket delete")
	force := fs.Bool("force-unmanaged", false, "")
	confirm := fs.String("confirm-name", "", "")
	mo := addManifestFlags(fs)
	pos, err := a.parseFlags(fs, args, "spaces bucket delete")
	if err != nil {
		return err
	}
	if len(pos) != 1 {
		return usagef("usage: docli spaces bucket delete NAME --region REGION [--yes --confirm-name NAME]")
	}
	r, err := a.start(*g)
	if err != nil {
		return err
	}
	region, err := r.bucketRegion()
	if err != nil {
		return err
	}
	sp, err := r.spaces()
	if err != nil {
		return err
	}
	bs, err := sp.ListBuckets(r.Ctx, region)
	if err != nil {
		return err
	}
	name := ""
	for _, b := range bs {
		if b.Name == pos[0] {
			name = b.Name
		}
	}
	if name == "" { // buckets made by docli are stored as docli-NAME
		for _, b := range bs {
			if b.Name == namePrefix+pos[0] {
				name = b.Name
			}
		}
	}
	if name == "" {
		return fmt.Errorf("no bucket named %q in %s. List them with: docli spaces bucket list --region %s", pos[0], region, region)
	}
	managed := hasManagedPrefix(name)
	rule := "a name starting with " + namePrefix
	if !managed && !*force {
		_, err := r.ApproveDelete(deleteSpec{Kind: "bucket", Name: name, Managed: false, Rule: rule}, false, "")
		return err
	}
	empty, err := sp.BucketEmpty(r.Ctx, region, name)
	if err != nil {
		return err
	}
	if !empty {
		return refusedf("refusing to delete bucket %q: it is not empty. docli never deletes objects; empty the bucket yourself first. Nothing was changed", name)
	}
	ok, err := r.ApproveDelete(deleteSpec{Kind: "bucket", Name: name, Managed: managed, Rule: rule, Always: true,
		Steps: []string{fmt.Sprintf("delete the empty bucket %q in %s; the name may be taken by someone else once it is free", name, region)}}, *force, *confirm)
	if !ok || err != nil {
		return err
	}
	if err := sp.DeleteBucket(r.Ctx, region, name); err != nil {
		return err
	}
	r.updateManifest(*mo, false, change{Section: "spaces_buckets", ID: region + "/" + name})
	return r.deleted("bucket", region+"/"+name, name)
}

// ---- keys ----

var keyNameRE = regexp.MustCompile(`^[A-Za-z0-9.-]{1,64}$`)

// keyErr adds the hint DigitalOcean's own documentation makes necessary: its reference lists /v2/spaces/keys, but
// its how-to page says keys can only be made in the control panel.
func keyErr(err error) error {
	switch statusOf(err) {
	case 401, 403, 404:
		return fmt.Errorf("%v. DigitalOcean's documentation disagrees about whether the API can manage Spaces keys (the API reference lists /v2/spaces/keys, a how-to page says control panel only); if this keeps failing, make the key in the control panel", err)
	}
	return err
}

func (r *Run) noteKeySecrets(ks ...*godo.SpacesKey) {
	for _, k := range ks {
		if k != nil {
			r.rememberSecret(k.SecretKey)
		}
	}
}

func (a *App) spacesKeyList(args []string) error {
	fs, g := a.newFlags("spaces key list")
	if _, err := a.parseFlags(fs, args, "spaces key list"); err != nil {
		return err
	}
	r, err := a.start(*g)
	if err != nil {
		return err
	}
	keys, err := r.Cloud.ListSpacesKeys(r.Ctx)
	r.noteKeySecrets(keys...)
	if err != nil {
		return keyErr(err)
	}
	if g.JSON {
		out := []spacesKeyJSON{}
		for _, k := range keys {
			out = append(out, spacesKeyView(k, ""))
		}
		return r.printJSON(out)
	}
	var rows [][]string
	for _, k := range keys {
		v := spacesKeyView(k, "")
		rows = append(rows, []string{k.Name, k.AccessKey, orDash(grantsText(v.Grants)), yesNo(v.Managed)})
	}
	printTable(a.Stdout, []string{"NAME", "ACCESS KEY ID", "GRANTS", "MANAGED"}, rows)
	return nil
}

func grantsText(gs []grantJSON) string {
	var out []string
	for _, g := range gs {
		b := g.Bucket
		if b == "" {
			b = "ALL BUCKETS"
		}
		out = append(out, b+":"+g.Permission)
	}
	return strings.Join(out, ", ")
}

var keyPerms = []string{"read", "readwrite", "fullaccess"}

func (a *App) spacesKeyCreate(args []string) error {
	fs, g := a.newFlags("spaces key create")
	name := fs.String("name", "", "")
	full := fs.Bool("full-access", false, "")
	secretFile := fs.String("secret-file", "", "")
	force := fs.Bool("force", false, "")
	var grants stringList
	fs.Var(&grants, "grant", "")
	mo := addManifestFlags(fs)
	pos, err := a.parseFlags(fs, args, "spaces key create")
	if err != nil {
		return err
	}
	if len(pos) > 0 || *name == "" {
		return usagef("usage: docli spaces key create --name NAME (--grant BUCKET:read|readwrite|fullaccess ... | --full-access) --secret-file PATH [--force]")
	}
	final := ensureNamePrefix(*name)
	if !keyNameRE.MatchString(final) {
		return usagef("%q is not a valid key name: letters, digits, dashes and periods only (up to 64 characters)", final)
	}
	if *secretFile == "" {
		return usagef("--secret-file PATH is required: the secret key is shown by DigitalOcean only once, so docli must have somewhere to save it before it creates the key. It is never printed")
	}
	if *full && len(grants) > 0 {
		return usagef("--full-access cannot be mixed with --grant: DigitalOcean gives a full-access key every bucket and ignores narrower grants")
	}
	if !*full && len(grants) == 0 {
		return usagef("say what the key may reach: --grant BUCKET:PERMISSION (read, readwrite or fullaccess), repeatable, or --full-access for every bucket. A key with no grants can do nothing")
	}
	var reqGrants []*godo.Grant
	if *full {
		reqGrants = []*godo.Grant{{Bucket: "", Permission: godo.SpacesKeyFullAccess}}
	}
	for _, spec := range grants {
		bucket, perm, ok := strings.Cut(spec, ":")
		if !ok || bucket == "" || !contains(keyPerms, perm) {
			return usagef("--grant %q must look like my-bucket:read (permissions: read, readwrite, fullaccess)", spec)
		}
		reqGrants = append(reqGrants, &godo.Grant{Bucket: bucket, Permission: godo.SpacesKeyPermission(perm)})
	}
	if err := checkSecretTargetFlag("--secret-file", *secretFile, *force); err != nil {
		return usagef("%v", err)
	}
	r, err := a.start(*g)
	if err != nil {
		return err
	}
	existing, err := r.Cloud.ListSpacesKeys(r.Ctx)
	r.noteKeySecrets(existing...)
	if err != nil {
		return keyErr(err)
	}
	for _, k := range existing {
		if k.Name == final {
			return fmt.Errorf("a Spaces key named %q already exists (access key id %s). Nothing was changed", final, k.AccessKey)
		}
	}
	scope := "every bucket in the account, with full access"
	if !*full {
		scope = ""
		for i, gr := range reqGrants {
			if i > 0 {
				scope += ", "
			}
			scope += fmt.Sprintf("%s: %s", gr.Bucket, gr.Permission)
		}
	}
	p := Plan{Action: "spaces.key.create", Summary: fmt.Sprintf("create the Spaces key %q", final),
		Steps: []string{"access: " + scope,
			fmt.Sprintf("the secret key will be written to %s (mode 0600) and never printed; DigitalOcean shows it only once", *secretFile),
			fmt.Sprintf("the name gets the prefix %s so docli can tell it created this key", namePrefix)}}
	if *full {
		p.Warnings = append(p.Warnings, "a full-access key can read, change and delete everything in every bucket, and can create and delete buckets. Prefer --grant for one bucket")
	}
	ok, err := r.Approve(p, approveOpts{})
	if !ok || err != nil {
		return err
	}
	k, err := r.Cloud.CreateSpacesKey(r.Ctx, &godo.SpacesKeyCreateRequest{Name: final, Grants: reqGrants})
	r.noteKeySecrets(k) // before anything can be printed
	if err != nil {
		return keyErr(madeOrUnknown("Spaces key", err, "docli spaces key list"))
	}
	r.updateManifest(*mo, false, change{Section: "spaces_keys", ID: k.AccessKey})
	if k.SecretKey == "" {
		return fmt.Errorf("the Spaces key %q (access key id %s) was created but DigitalOcean did not return a secret, so there is nothing to save to %s. Delete it with: docli spaces key delete %s", final, k.AccessKey, *secretFile, final)
	}
	if err := writeSecretFile(*secretFile, k.SecretKey, *force); err != nil {
		return fmt.Errorf("%v. The key %q (access key id %s) exists but its secret was not saved and cannot be fetched again. Delete the key with: docli spaces key delete %s, then create it again", err, final, k.AccessKey, final)
	}
	if g.JSON {
		return r.printJSON(spacesKeyView(k, *secretFile))
	}
	fmt.Fprintf(a.Stdout, "Created Spaces key %q (access key id %s). The secret key was written to %s (mode 0600) and is not shown.\n", k.Name, k.AccessKey, *secretFile)
	return nil
}

func (a *App) spacesKeyDelete(args []string) error {
	fs, g := a.newFlags("spaces key delete")
	force := fs.Bool("force-unmanaged", false, "")
	confirm := fs.String("confirm-name", "", "")
	mo := addManifestFlags(fs)
	pos, err := a.parseFlags(fs, args, "spaces key delete")
	if err != nil {
		return err
	}
	if len(pos) != 1 {
		return usagef("usage: docli spaces key delete NAME|ACCESS_KEY_ID [--yes --confirm-name NAME]")
	}
	r, err := a.start(*g)
	if err != nil {
		return err
	}
	keys, err := r.Cloud.ListSpacesKeys(r.Ctx)
	r.noteKeySecrets(keys...)
	if err != nil {
		return keyErr(err)
	}
	var hits []*godo.SpacesKey
	for _, k := range keys {
		if k.Name == pos[0] || k.AccessKey == pos[0] {
			hits = append(hits, k)
		}
	}
	if len(hits) == 0 {
		for _, k := range keys {
			if k.Name == namePrefix+pos[0] {
				hits = append(hits, k)
			}
		}
	}
	switch len(hits) {
	case 0:
		return fmt.Errorf("no Spaces key named %q (or with that access key id). List them with: docli spaces key list", pos[0])
	case 1:
	default:
		return fmt.Errorf("%d Spaces keys match %q; use the access key id", len(hits), pos[0])
	}
	k := hits[0]
	ok, err := r.ApproveDelete(deleteSpec{Kind: "Spaces key", Name: k.Name, Managed: hasManagedPrefix(k.Name), Rule: "a name starting with " + namePrefix, Always: true,
		Steps: []string{fmt.Sprintf("delete the key with access key id %s", k.AccessKey), "every application that uses this key loses access to its buckets at once"}}, *force, *confirm)
	if !ok || err != nil {
		return err
	}
	if err := r.Cloud.DeleteSpacesKey(r.Ctx, k.AccessKey); err != nil {
		return keyErr(err)
	}
	r.updateManifest(*mo, false, change{Section: "spaces_keys", ID: k.AccessKey})
	return r.deleted("spaces-key", k.AccessKey, k.Name)
}
