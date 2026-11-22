package main

import (
	"fmt"
	"os"
	"strconv"

	"github.com/digitalocean/godo"
)

func (a *App) cmdSSHKey(args []string) error {
	if len(args) == 0 {
		return usagef("usage: docli ssh-key list|get|create|delete (try: docli help ssh-key)")
	}
	switch args[0] {
	case "list":
		return a.sshKeyList(args[1:])
	case "get":
		return a.sshKeyGet(args[1:])
	case "create":
		return a.sshKeyCreate(args[1:])
	case "delete":
		return a.sshKeyDelete(args[1:])
	}
	return usagef("unknown ssh-key command %q (list, get, create, delete)", args[0])
}

func findKey(keys []godo.Key, ref string) (*godo.Key, error) {
	var hits []godo.Key
	for _, k := range keys {
		if k.Name == ref || k.Fingerprint == ref || strconv.Itoa(k.ID) == ref {
			hits = append(hits, k)
		}
	}
	if len(hits) == 0 { // keys made by docli are stored as docli-NAME
		for _, k := range keys {
			if k.Name == namePrefix+ref {
				hits = append(hits, k)
			}
		}
	}
	switch len(hits) {
	case 0:
		return nil, fmt.Errorf("no SSH key named %q (or with that id or fingerprint). List them with: docli ssh-key list", ref)
	case 1:
		return &hits[0], nil
	}
	return nil, fmt.Errorf("%d SSH keys match %q; use the id instead", len(hits), ref)
}

func (a *App) sshKeyList(args []string) error {
	fs, g := a.newFlags("ssh-key list")
	if _, err := a.parseFlags(fs, args, "ssh-key list"); err != nil {
		return err
	}
	r, err := a.start(*g)
	if err != nil {
		return err
	}
	keys, err := r.Cloud.ListKeys(r.Ctx)
	if err != nil {
		return err
	}
	if g.JSON {
		out := []keyJSON{}
		for _, k := range keys {
			out = append(out, keyView(k))
		}
		return r.printJSON(out)
	}
	var rows [][]string
	for _, k := range keys {
		rows = append(rows, []string{itoa(k.ID), k.Name, k.Fingerprint, yesNo(hasManagedPrefix(k.Name))})
	}
	printTable(a.Stdout, []string{"ID", "NAME", "FINGERPRINT", "MANAGED"}, rows)
	return nil
}

func (a *App) sshKeyGet(args []string) error {
	fs, g := a.newFlags("ssh-key get")
	pos, err := a.parseFlags(fs, args, "ssh-key get")
	if err != nil {
		return err
	}
	if len(pos) != 1 {
		return usagef("usage: docli ssh-key get NAME|ID|FINGERPRINT")
	}
	r, err := a.start(*g)
	if err != nil {
		return err
	}
	keys, err := r.Cloud.ListKeys(r.Ctx)
	if err != nil {
		return err
	}
	k, err := findKey(keys, pos[0])
	if err != nil {
		return err
	}
	if g.JSON {
		return r.printJSON(keyView(*k))
	}
	fmt.Fprintf(a.Stdout, "ID:           %d\nName:         %s\nFingerprint:  %s\nManaged:      %s\n", k.ID, k.Name, k.Fingerprint, yesNo(hasManagedPrefix(k.Name)))
	return nil
}

func (a *App) sshKeyCreate(args []string) error {
	fs, g := a.newFlags("ssh-key create")
	name := fs.String("name", "", "")
	file := fs.String("public-key-file", "", "")
	text := fs.String("public-key", "", "")
	mo := addManifestFlags(fs)
	pos, err := a.parseFlags(fs, args, "ssh-key create")
	if err != nil {
		return err
	}
	if len(pos) > 0 || *name == "" || (*file == "") == (*text == "") {
		return usagef("usage: docli ssh-key create --name NAME (--public-key-file FILE | --public-key \"ssh-ed25519 AAAA...\")")
	}
	raw := *text
	if *file != "" {
		b, err := os.ReadFile(*file)
		if err != nil {
			return fmt.Errorf("cannot read %s: %v", *file, err)
		}
		raw = string(b)
	}
	line, ktype, fp, err := parsePublicKey(raw)
	if err != nil {
		return err
	}
	final := ensureNamePrefix(*name)
	if err := validateResourceName("SSH key", final); err != nil {
		return err
	}
	r, err := a.start(*g)
	if err != nil {
		return err
	}
	keys, err := r.Cloud.ListKeys(r.Ctx)
	if err != nil {
		return err
	}
	for _, k := range keys {
		if k.Name == final || k.Fingerprint == fp {
			return fmt.Errorf("an SSH key with that name or fingerprint already exists (%q, id %d). Nothing was changed", k.Name, k.ID)
		}
	}
	p := Plan{Action: "ssh-key.create", Summary: fmt.Sprintf("add SSH key %q", final), Steps: []string{
		"upload the public key (" + ktype + ")",
		"fingerprint: " + fp,
	}}
	if final != *name {
		p.Steps = append(p.Steps, fmt.Sprintf("the name gets the prefix %s so docli can tell it created this key (SSH keys cannot carry tags)", namePrefix))
	}
	ok, err := r.Approve(p, approveOpts{})
	if !ok || err != nil {
		return err
	}
	k, err := r.Cloud.CreateKey(r.Ctx, final, line)
	if err != nil {
		return err
	}
	r.updateManifest(*mo, false, change{Section: "ssh_keys", ID: itoa(k.ID)})
	if g.JSON {
		return r.printJSON(keyView(*k))
	}
	fmt.Fprintf(a.Stdout, "Added SSH key %q (id %d, fingerprint %s)\n", k.Name, k.ID, k.Fingerprint)
	return nil
}

func (a *App) sshKeyDelete(args []string) error {
	fs, g := a.newFlags("ssh-key delete")
	force := fs.Bool("force-unmanaged", false, "")
	confirm := fs.String("confirm-name", "", "")
	mo := addManifestFlags(fs)
	pos, err := a.parseFlags(fs, args, "ssh-key delete")
	if err != nil {
		return err
	}
	if len(pos) != 1 {
		return usagef("usage: docli ssh-key delete NAME|ID|FINGERPRINT")
	}
	r, err := a.start(*g)
	if err != nil {
		return err
	}
	keys, err := r.Cloud.ListKeys(r.Ctx)
	if err != nil {
		return err
	}
	k, err := findKey(keys, pos[0])
	if err != nil {
		return err
	}
	ok, err := r.ApproveDelete(deleteSpec{Kind: "ssh-key", Name: k.Name, Managed: hasManagedPrefix(k.Name),
		Rule: "a name starting with " + namePrefix,
		Steps: []string{fmt.Sprintf("remove SSH key id %d (%s) from the account", k.ID, k.Fingerprint),
			"droplets already created with it keep working; new droplets cannot use it"}}, *force, *confirm)
	if !ok || err != nil {
		return err
	}
	if err := r.Cloud.DeleteKey(r.Ctx, k.ID); err != nil {
		return err
	}
	r.updateManifest(*mo, false, change{Section: "ssh_keys", ID: itoa(k.ID)})
	return r.deleted("ssh-key", itoa(k.ID), k.Name)
}

type deletedJSON struct {
	Deleted bool   `json:"deleted"`
	Type    string `json:"type"`
	ID      string `json:"id"`
	Name    string `json:"name"`
}

func (r *Run) deleted(kind, id, name string) error {
	if r.G.JSON {
		return r.printJSON(deletedJSON{true, kind, id, name})
	}
	fmt.Fprintf(r.Stdout, "Deleted %s %q (%s)\n", kind, name, id)
	return nil
}
