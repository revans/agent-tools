package main

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"

	"gopkg.in/yaml.v3"
)

func (a *App) cmdManifest(args []string) error {
	if len(args) == 0 {
		return usagef("usage: docli manifest show|verify|refresh (try: docli help manifest)")
	}
	switch args[0] {
	case "show":
		return a.manifestShow(args[1:])
	case "verify":
		return a.manifestVerify(args[1:])
	case "refresh":
		return a.manifestRefresh(args[1:])
	}
	return usagef("unknown manifest command %q (show, verify, refresh)", args[0])
}

func manifestPathFlag(fs interface {
	StringVar(*string, string, string, string)
}) *string {
	p := new(string)
	fs.StringVar(p, "manifest", "", "")
	return p
}

func (a *App) manifestShow(args []string) error {
	fs, g := a.newFlags("manifest show")
	p := manifestPathFlag(fs)
	if _, err := a.parseFlags(fs, args, "manifest show"); err != nil {
		return err
	}
	r := &Run{App: a, G: *g}
	path, _, err := r.manifestPath(manifestOpts{Path: *p})
	if err != nil {
		return err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return fmt.Errorf("no manifest at %s. docli writes one when it creates something, or run: docli manifest refresh", path)
		}
		return err
	}
	if !g.JSON {
		_, err = a.Stdout.Write(data)
		return err
	}
	var v any
	if err := yaml.Unmarshal(data, &v); err != nil {
		return fmt.Errorf("%s is not valid YAML: %v", path, err)
	}
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	fmt.Fprintln(a.Stdout, string(b))
	return nil
}

type diffItem struct {
	Kind    string `json:"kind"` // missing_in_account, missing_in_file, drift or unverifiable
	Section string `json:"section"`
	ID      string `json:"id"`
	Name    string `json:"name"`
	Detail  string `json:"detail"`
}

type verifyJSON struct {
	Path        string     `json:"path"`
	InSync      bool       `json:"in_sync"`
	Differences []diffItem `json:"differences"`
}

func (a *App) manifestVerify(args []string) error {
	fs, g := a.newFlags("manifest verify")
	p := manifestPathFlag(fs)
	if _, err := a.parseFlags(fs, args, "manifest verify"); err != nil {
		return err
	}
	r, err := a.start(*g)
	if err != nil {
		return err
	}
	path, _, err := r.manifestPath(manifestOpts{Path: *p})
	if err != nil {
		return err
	}
	m, _, err := readManifest(path)
	if err != nil {
		if os.IsNotExist(err) {
			return fmt.Errorf("no manifest at %s to verify. Create it with: docli manifest refresh", path)
		}
		return err
	}
	snap, err := r.loadSnapshot()
	if err != nil {
		return err
	}
	snap.attach(m)
	out := verifyJSON{Path: path, Differences: []diffItem{}}
	inFile := map[string]bool{}
	for _, sec := range sectionOrder {
		for _, n := range m.sections[sec] {
			id, name := nodeID(n), nodeField(n, "name")
			inFile[sec+"/"+id] = true
			e, managed := snap.entry(sec, id)
			switch {
			case snap.denied[sec]:
				out.Differences = append(out.Differences, diffItem{"unverifiable", sec, id, name, firstNonEmpty(snap.why[sec], "the token cannot read "+sec+", so this entry was not checked")})
			case e == nil:
				out.Differences = append(out.Differences, diffItem{"missing_in_account", sec, id, name, "the file lists it but it no longer exists in the account"})
			case !managed:
				out.Differences = append(out.Differences, diffItem{"missing_in_account", sec, id, name, "it exists but is no longer marked docli-managed"})
			default:
				for _, d := range entryDiffs(sec, n, e) {
					out.Differences = append(out.Differences, diffItem{"drift", sec, id, name, d})
				}
			}
		}
	}
	for _, e := range snap.managedEntries() {
		if !inFile[e.Section()+"/"+e.EntryID()] {
			out.Differences = append(out.Differences, diffItem{"missing_in_file", e.Section(), e.EntryID(), entryName(e), "docli-managed in the account but not in the file"})
		}
	}
	out.InSync = len(out.Differences) == 0
	if g.JSON {
		if err := r.printJSON(out); err != nil {
			return err
		}
	} else if out.InSync {
		fmt.Fprintf(a.Stdout, "%s matches the account.\n", path)
	} else {
		fmt.Fprintf(a.Stdout, "%s differs from the account (%d difference(s)):\n", path, len(out.Differences))
		for _, d := range out.Differences {
			fmt.Fprintf(a.Stdout, "  %-18s %s %q (id %s): %s\n", d.Kind, d.Section, d.Name, d.ID, d.Detail)
		}
		fmt.Fprintln(a.Stdout, "Nothing was changed. To bring the file in line with the account: docli manifest refresh")
	}
	if !out.InSync {
		return silentError{}
	}
	return nil
}

func (a *App) manifestRefresh(args []string) error {
	fs, g := a.newFlags("manifest refresh")
	p := manifestPathFlag(fs)
	if _, err := a.parseFlags(fs, args, "manifest refresh"); err != nil {
		return err
	}
	r, err := a.start(*g)
	if err != nil {
		return err
	}
	path, display, err := r.manifestPath(manifestOpts{Path: *p})
	if err != nil {
		return err
	}
	m, old, err := readManifest(path)
	if err != nil {
		if !os.IsNotExist(err) {
			return err
		}
		m, old = newManifest(), nil
	}
	snap, err := r.loadSnapshot()
	if err != nil {
		return err
	}
	snap.attach(m)
	for _, sec := range accountSections { // only the kinds docli knows are rebuilt; other sections are left alone
		snap.ensure(sec)
		if !snap.denied[sec] {
			m.sections[sec] = nil
		}
	}
	for _, e := range snap.managedEntries() {
		if err := m.Upsert(e); err != nil {
			return err
		}
	}
	if err := m.refreshDomains(snap); err != nil {
		return err
	}
	for _, sec := range []string{"reserved_ips", "spaces_buckets"} { // kinds the account cannot say docli owns: only what the file lists
		if err := m.refreshListedOnly(snap, sec); err != nil {
			return err
		}
	}
	data, err := m.Marshal(a.Now())
	if err != nil {
		return err
	}
	if old != nil && stripStamps(old) == stripStamps(data) {
		if g.JSON {
			return r.printJSON(dryRunDoc{false, Plan{Action: "manifest.refresh", Summary: "already up to date", Steps: []string{}, Warnings: []string{}}})
		}
		fmt.Fprintf(a.Stdout, "%s already matches the account.\n", display)
		return nil
	}
	var diff []string
	if old == nil {
		for _, l := range strings.Split(strings.TrimRight(string(data), "\n"), "\n") {
			diff = append(diff, "+ "+l)
		}
	} else {
		diff = lineDiff(string(old), string(data))
	}
	var w io.Writer = a.Stdout
	if g.JSON {
		w = a.Stderr
	}
	fmt.Fprintf(w, "Changes to %s:\n%s\n", display, strings.Join(diff, "\n"))
	ok, err := r.Approve(Plan{Action: "manifest.refresh", Summary: fmt.Sprintf("rewrite %s from the account's %s resources", display, managedTag),
		Steps: []string{fmt.Sprintf("%d line(s) differ", len(diff))}}, approveOpts{})
	if !ok || err != nil {
		return err
	}
	if err := a.WriteFile(path, data); err != nil {
		return fmt.Errorf("could not write %s: %v (the old file is unchanged)", path, err)
	}
	r.infof("updated %s", display)
	return nil
}

// entryDiffs lists how a file entry differs from the account's. Domains compare record by record so the report
// says which record changed.
func entryDiffs(section string, n *yaml.Node, e ManifestEntry) []string {
	if section != "domains" {
		return diffMaps(entryMap(n), entryToMap(e))
	}
	fileMap, acct := entryMap(n), entryToMap(e)
	delete(fileMap, "records")
	delete(acct, "records")
	out := diffMaps(fileMap, acct)
	var fe domainEntry
	n.Decode(&fe)
	have := map[int]recordEntry{}
	for _, r := range e.(*domainEntry).Records {
		have[r.ID] = r
	}
	for _, fr := range fe.Records {
		ar, ok := have[fr.ID]
		if !ok {
			out = append(out, fmt.Sprintf("record %d (%s %s) is listed in the file but no longer exists in the account", fr.ID, fr.Type, fr.Name))
			continue
		}
		fm, am := map[string]any{}, map[string]any{}
		if x, err := encodeAny(fr); err == nil {
			fm = x
		}
		if x, err := encodeAny(ar); err == nil {
			am = x
		}
		for _, d := range diffMaps(fm, am) {
			out = append(out, fmt.Sprintf("record %d (%s %s) %s", fr.ID, fr.Type, fr.Name, d))
		}
	}
	return out
}

func encodeAny(v any) (map[string]any, error) {
	n := &yaml.Node{}
	if err := n.Encode(v); err != nil {
		return nil, err
	}
	return entryMap(n), nil
}
