package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"net/http"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/revans/agent-tools/apikit"
)

// ---- vocabulary ----

// contentTypes carry their value in "content"; dataTypes carry a "data" object
// (Cloudflare marks their "content" read-only), given here as --data JSON.
var (
	contentTypes = []string{"A", "AAAA", "CNAME", "TXT", "MX", "NS", "PTR", "OPENPGPKEY"}
	dataTypes    = []string{"SRV", "CAA", "CERT", "DNSKEY", "DS", "HTTPS", "LOC", "NAPTR", "SMIMEA", "SSHFP", "SVCB", "TLSA", "URI"}
	proxiable    = map[string]bool{"A": true, "AAAA": true, "CNAME": true}

	zoneIDRe   = regexp.MustCompile(`^[0-9a-f]{32}$`)
	recordIDRe = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)
)

func in(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

func recordType(s string) (string, error) {
	t := strings.ToUpper(strings.TrimSpace(s))
	if t == "" {
		return "", apikit.Usage("--type is required (one of %s)", strings.Join(append(append([]string{}, contentTypes...), dataTypes...), ", "))
	}
	if !in(contentTypes, t) && !in(dataTypes, t) {
		return "", apikit.Usage("%q is not a DNS record type Cloudflare knows (A, AAAA, CNAME, TXT, MX, NS, SRV, CAA, ...)", s)
	}
	return t, nil
}

func validTTL(n int) error {
	if n == 1 || (n >= 30 && n <= 86400) {
		return nil
	}
	return apikit.Usage("--ttl %d is not valid: use 1 for automatic, or 60 to 86400 seconds (30 on Enterprise zones)", n)
}

// ---- small JSON helpers ----

func str(m map[string]any, k string) string {
	s, _ := m[k].(string)
	return s
}

func sub(m map[string]any, k string) map[string]any {
	s, _ := m[k].(map[string]any)
	return s
}

// numAt reads a number at a path of the raw response, or 0.
func numAt(raw any, path string) int {
	v, _ := apikit.Select(raw, path)
	switch n := v.(type) {
	case json.Number:
		i, _ := n.Int64()
		return int(i)
	case float64:
		return int(n)
	}
	return 0
}

func list(data any) []map[string]any {
	l, _ := data.([]any)
	out := make([]map[string]any, 0, len(l))
	for _, e := range l {
		if m, ok := e.(map[string]any); ok {
			out = append(out, m)
		}
	}
	return out
}

func recordView(m map[string]any) map[string]any {
	out := map[string]any{}
	for _, k := range []string{"id", "type", "name", "content", "ttl", "proxied", "priority", "comment", "data"} {
		if v, ok := m[k]; ok && v != nil && v != "" {
			out[k] = v
		}
	}
	return out
}

// explain adds the likely cause to the two statuses people hit first.
func explain(err error) error {
	var he *apikit.HTTPError
	if !errors.As(err, &he) {
		return err
	}
	switch he.Status {
	case http.StatusUnauthorized:
		return fmt.Errorf("%w (hint: Cloudflare did not accept the API token; is it correct, active and unexpired? `cloudflare check` tests it)", err)
	case http.StatusForbidden:
		return fmt.Errorf("%w (hint: the token lacks a permission this call needs; the README lists which command needs which)", err)
	}
	return err
}

// ---- calls ----

// readCall runs a read-only endpoint for real even under --dry-run: a dry run
// shows the write it would make, and to show it correctly (a zone's id, the
// record that would be deleted) the lookups before it must actually happen.
func readCall(r *apikit.Run, endpoint string, params map[string]string, pages int) (*apikit.Result, error) {
	reg, err := r.Registry()
	if err != nil {
		return nil, err
	}
	ep, ok := reg.Get(endpoint)
	if !ok || ep.Safety != apikit.SafetyRead {
		return nil, fmt.Errorf("internal: %q is not a read endpoint", endpoint)
	}
	c, err := r.Client()
	if err != nil {
		return nil, err
	}
	rc := *c
	rc.DryRun = false
	res, err := rc.Call(r.Ctx, ep, params, pages)
	if err != nil {
		return nil, explain(err)
	}
	return res, nil
}

func maxPages(r *apikit.Run, endpoint string) int {
	if reg, err := r.Registry(); err == nil {
		if ep, ok := reg.Get(endpoint); ok && ep.Paginate != nil && ep.Paginate.MaxPages > 0 {
			return ep.Paginate.MaxPages
		}
	}
	return 10
}

type zoneRef struct{ ID, Name string }

// resolveZone turns a zone name (example.com) or a zone id into a zone. A name
// that matches no zone, or more than one, is an error: acting on a guess is how
// records land in the wrong domain.
func resolveZone(r *apikit.Run, ref string) (zoneRef, error) {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return zoneRef{}, apikit.Usage("--zone is required (a domain name such as example.com, or a zone id)")
	}
	if zoneIDRe.MatchString(ref) {
		return zoneRef{ID: ref}, nil
	}
	name := strings.ToLower(strings.TrimSuffix(ref, "."))
	if strings.ContainsAny(name, "/ \t?#&=") || !strings.Contains(name, ".") {
		return zoneRef{}, apikit.Usage("%q is neither a zone id (32 hex characters) nor a domain name such as example.com", ref)
	}
	res, err := readCall(r, "zones", map[string]string{"name": name}, 1)
	if err != nil {
		return zoneRef{}, err
	}
	var hits []map[string]any
	for _, z := range list(res.Data) {
		if strings.EqualFold(str(z, "name"), name) {
			hits = append(hits, z)
		}
	}
	switch len(hits) {
	case 0:
		return zoneRef{}, fmt.Errorf("no zone named %q is visible to this token (check the spelling, the token's zone permissions and --account)", name)
	case 1:
		return zoneRef{ID: str(hits[0], "id"), Name: str(hits[0], "name")}, nil
	}
	var each []string
	for _, z := range hits {
		each = append(each, fmt.Sprintf("%s (account %s)", str(z, "id"), str(sub(z, "account"), "name")))
	}
	return zoneRef{}, fmt.Errorf("%d zones are named %q: %s. Pass the zone id as --zone to choose one", len(hits), name, strings.Join(each, "; "))
}

// zoneName fills in a zone's name when --zone was an id.
func zoneName(r *apikit.Run, z zoneRef) (string, error) {
	if z.Name != "" {
		return z.Name, nil
	}
	res, err := readCall(r, "zone", map[string]string{"zone_id": z.ID}, 1)
	if err != nil {
		return "", err
	}
	m, _ := res.Data.(map[string]any)
	return strings.ToLower(str(m, "name")), nil
}

// fullName turns www or @ into the full record name the list filter needs.
func fullName(name, zone string) string {
	n := strings.ToLower(strings.TrimSuffix(strings.TrimSpace(name), "."))
	zone = strings.ToLower(zone)
	switch {
	case n == "@" || n == "":
		return zone
	case n == zone || strings.HasSuffix(n, "."+zone):
		return n
	}
	return n + "." + zone
}

// flagsSet lists the flags the user actually gave.
func flagsSet(fs *flag.FlagSet) map[string]bool {
	set := map[string]bool{}
	fs.Visit(func(f *flag.Flag) { set[f.Name] = true })
	return set
}

var commonFlags = map[string]bool{"account": true, "config": true, "dry-run": true, "debug": true, "max-safety": true, "pretty": true}

// onlyFlags refuses a flag that does not belong to the subcommand, rather than
// ignoring it and doing something other than what was asked.
func onlyFlags(sub string, set map[string]bool, allowed ...string) error {
	for f := range set {
		if !commonFlags[f] && !in(allowed, f) {
			return apikit.Usage("dns %s does not take --%s", sub, f)
		}
	}
	return nil
}

// ---- zones ----

func zonesCommand() *apikit.Command {
	var name, status string
	var all bool
	return &apikit.Command{
		Name:    "zones",
		Summary: "list zones (domains): name, id, status",
		Usage:   "zones [--name example.com] [--status active] [--all]",
		Setup: func(fs *flag.FlagSet) {
			fs.StringVar(&name, "name", "", "only the zone with this exact name")
			fs.StringVar(&status, "status", "", "only zones in this status: initializing, pending, active, moved")
			fs.BoolVar(&all, "all", false, "follow every page (default: the first page)")
		},
		Run: func(r *apikit.Run) (any, error) {
			if len(r.Args) != 0 {
				return nil, apikit.Usage("zones takes no arguments")
			}
			params := map[string]string{}
			if name != "" {
				params["name"] = strings.ToLower(strings.TrimSpace(name))
			}
			if status != "" {
				params["status"] = status
			}
			pages := 1
			if all {
				pages = maxPages(r, "zones")
			}
			res, err := readCall(r, "zones", params, pages)
			if err != nil {
				return nil, err
			}
			out := []map[string]any{}
			for _, z := range list(res.Data) {
				out = append(out, map[string]any{"name": str(z, "name"), "id": str(z, "id"), "status": str(z, "status"), "account": str(sub(z, "account"), "name")})
			}
			return map[string]any{"zones": out, "count": len(out), "more": !all && numAt(res.Raw, "result_info.total_pages") > numAt(res.Raw, "result_info.page")}, nil
		},
	}
}

// ---- dns ----

type dnsFlags struct {
	zone, typ, name, content, comment, data string
	ttl, priority                           int
	proxied, all                            bool
}

func dnsCommand() *apikit.Command {
	var f dnsFlags
	var cmd *apikit.Command
	cmd = &apikit.Command{
		Name:    "dns",
		Summary: "list, add, update or delete DNS records by zone name",
		Usage: `dns list --zone example.com [--type A] [--name www] [--all]
       dns add --zone Z --type T --name N --content C [--ttl 1] [--proxied] [--comment TEXT] [--priority N] [--data JSON]
       dns update RECORD_ID --zone Z [--type T] [--name N] [--content C] [--ttl N] [--proxied=false] [--comment TEXT] [--priority N] [--data JSON]
       dns delete RECORD_ID --zone Z   (needs --max-safety destructive)`,
		Setup: func(fs *flag.FlagSet) {
			fs.StringVar(&f.zone, "zone", "", "zone name (example.com) or zone id (required)")
			fs.StringVar(&f.typ, "type", "", "record type: A, AAAA, CNAME, TXT, MX, NS, SRV, CAA, ...")
			fs.StringVar(&f.name, "name", "", "record name: www, www.example.com or @ for the zone apex")
			fs.StringVar(&f.content, "content", "", "record content: an IP for A/AAAA, a hostname for CNAME/MX/NS, text for TXT")
			fs.IntVar(&f.ttl, "ttl", 1, "seconds to live; 1 means automatic (Cloudflare's own setting), otherwise 60 to 86400")
			fs.BoolVar(&f.proxied, "proxied", false, "send traffic through Cloudflare (A, AAAA, CNAME only; default false)")
			fs.StringVar(&f.comment, "comment", "", "a note on the record")
			fs.IntVar(&f.priority, "priority", 0, "priority, required for MX records")
			fs.StringVar(&f.data, "data", "", "JSON object for record types that take components (SRV, CAA, ...)")
			fs.BoolVar(&f.all, "all", false, "list: follow every page (default: the first page)")
		},
		Run: func(r *apikit.Run) (any, error) {
			if len(r.Args) == 0 {
				return nil, apikit.Usage("dns needs a subcommand: list, add, update or delete")
			}
			set := flagsSet(r.FS)
			switch r.Args[0] {
			case "list":
				return dnsList(r, &f, set)
			case "add":
				return dnsAdd(r, &f, set)
			case "update":
				return dnsUpdate(r, &f, set)
			case "delete":
				return dnsDelete(r, &f, set)
			}
			return nil, apikit.Usage("unknown dns subcommand %q (list, add, update, delete)", r.Args[0])
		},
	}
	return cmd
}

func dnsList(r *apikit.Run, f *dnsFlags, set map[string]bool) (any, error) {
	if len(r.Args) != 1 {
		return nil, apikit.Usage("dns list takes no arguments")
	}
	if err := onlyFlags("list", set, "zone", "type", "name", "content", "all"); err != nil {
		return nil, err
	}
	params := map[string]string{}
	if f.typ != "" {
		t, err := recordType(f.typ)
		if err != nil {
			return nil, err
		}
		params["type"] = t
	}
	if f.content != "" {
		params["content"] = f.content
	}
	z, err := resolveZone(r, f.zone)
	if err != nil {
		return nil, err
	}
	params["zone_id"] = z.ID
	if f.name != "" {
		zn, err := zoneName(r, z)
		if err != nil {
			return nil, err
		}
		z.Name = zn
		params["name"] = fullName(f.name, zn)
	}
	pages := 1
	if f.all {
		pages = maxPages(r, "dns_records")
	}
	res, err := readCall(r, "dns_records", params, pages)
	if err != nil {
		return nil, err
	}
	records := []map[string]any{}
	for _, m := range list(res.Data) {
		records = append(records, recordView(m))
	}
	out := map[string]any{"zone": map[string]any{"id": z.ID, "name": z.Name}, "records": records, "count": len(records)}
	out["more"] = !f.all && numAt(res.Raw, "result_info.total_pages") > numAt(res.Raw, "result_info.page")
	return out, nil
}

// recordBody validates the record fields that were given and returns the
// endpoint parameters for them. With create set, type, name and content (or
// data) are all required and proxied is always sent; otherwise only the fields
// the user gave are included, which is what makes a PATCH a PATCH.
func recordBody(f *dnsFlags, set map[string]bool, create bool) (map[string]string, error) {
	p := map[string]string{}
	typ := ""
	if create || set["type"] {
		t, err := recordType(f.typ)
		if err != nil {
			return nil, err
		}
		typ = t
		p["type"] = t
	}
	if create || set["name"] {
		if strings.TrimSpace(f.name) == "" {
			return nil, apikit.Usage("--name is required and must not be empty (use @ for the zone apex)")
		}
		p["name"] = strings.TrimSpace(f.name)
	}
	if set["content"] {
		if strings.TrimSpace(f.content) == "" {
			return nil, apikit.Usage("--content must not be empty")
		}
		p["content"] = f.content
	}
	if set["data"] {
		var obj map[string]any
		if err := json.Unmarshal([]byte(f.data), &obj); err != nil || obj == nil {
			return nil, apikit.Usage("--data must be a JSON object, e.g. {\"flags\":0,\"tag\":\"issue\",\"value\":\"letsencrypt.org\"}")
		}
		p["data"] = f.data
	}
	if create {
		switch {
		case in(dataTypes, typ):
			if !set["data"] {
				return nil, apikit.Usage("%s records take their parts as a JSON object: pass --data", typ)
			}
			if set["content"] {
				return nil, apikit.Usage("%s records are given with --data, not --content", typ)
			}
		case !set["content"]:
			return nil, apikit.Usage("--content is required for a %s record", typ)
		case set["data"]:
			return nil, apikit.Usage("%s records are given with --content, not --data", typ)
		}
	}
	if typ == "MX" && !set["priority"] {
		return nil, apikit.Usage("an MX record needs --priority (lower is preferred, e.g. 10)")
	}
	if set["priority"] {
		if f.priority < 0 || f.priority > 65535 {
			return nil, apikit.Usage("--priority must be between 0 and 65535")
		}
		p["priority"] = strconv.Itoa(f.priority)
	}
	if set["ttl"] || create {
		if err := validTTL(f.ttl); err != nil {
			return nil, err
		}
		p["ttl"] = strconv.Itoa(f.ttl)
	}
	if set["proxied"] || create {
		if f.proxied && typ != "" && !proxiable[typ] {
			return nil, apikit.Usage("only A, AAAA and CNAME records can be proxied, not %s", typ)
		}
		p["proxied"] = strconv.FormatBool(f.proxied)
	}
	if set["comment"] {
		p["comment"] = f.comment
	}
	return p, nil
}

func dnsAdd(r *apikit.Run, f *dnsFlags, set map[string]bool) (any, error) {
	if len(r.Args) != 1 {
		return nil, apikit.Usage("dns add takes no arguments; give --zone, --type, --name and --content")
	}
	if err := onlyFlags("add", set, "zone", "type", "name", "content", "ttl", "proxied", "comment", "priority", "data"); err != nil {
		return nil, err
	}
	body, err := recordBody(f, set, true)
	if err != nil {
		return nil, err
	}
	z, err := resolveZone(r, f.zone)
	if err != nil {
		return nil, err
	}
	body["zone_id"] = z.ID
	res, err := r.Call("dns_record_create", body)
	if err != nil {
		return nil, explain(err)
	}
	if r.DryRun() {
		return res.Data, nil
	}
	m, _ := res.Data.(map[string]any)
	return map[string]any{"created": recordView(m)}, nil
}

func recordIDArg(r *apikit.Run, sub string) (string, error) {
	if len(r.Args) != 2 {
		return "", apikit.Usage("dns %s needs one RECORD_ID (`dns list` shows them)", sub)
	}
	id := strings.TrimSpace(r.Args[1])
	if !recordIDRe.MatchString(id) {
		return "", apikit.Usage("%q is not a DNS record id (32 hex characters; `dns list` shows them)", r.Args[1])
	}
	return id, nil
}

func dnsUpdate(r *apikit.Run, f *dnsFlags, set map[string]bool) (any, error) {
	if err := onlyFlags("update", set, "zone", "type", "name", "content", "ttl", "proxied", "comment", "priority", "data"); err != nil {
		return nil, err
	}
	id, err := recordIDArg(r, "update")
	if err != nil {
		return nil, err
	}
	body, err := recordBody(f, set, false)
	if err != nil {
		return nil, err
	}
	if len(body) == 0 {
		return nil, apikit.Usage("nothing to change: give at least one of --type, --name, --content, --ttl, --proxied, --comment, --priority, --data")
	}
	z, err := resolveZone(r, f.zone)
	if err != nil {
		return nil, err
	}
	body["zone_id"], body["dns_record_id"] = z.ID, id
	res, err := r.Call("dns_record_update", body)
	if err != nil {
		return nil, explain(err)
	}
	if r.DryRun() {
		return res.Data, nil
	}
	m, _ := res.Data.(map[string]any)
	return map[string]any{"updated": recordView(m)}, nil
}

func dnsDelete(r *apikit.Run, f *dnsFlags, set map[string]bool) (any, error) {
	if err := onlyFlags("delete", set, "zone"); err != nil {
		return nil, err
	}
	id, err := recordIDArg(r, "delete")
	if err != nil {
		return nil, err
	}
	c, err := r.Client()
	if err != nil {
		return nil, err
	}
	if apikit.SafetyRank(c.MaxSafety) < apikit.SafetyRank(apikit.SafetyDestructive) {
		return nil, fmt.Errorf("dns delete removes a public DNS record, which cannot be undone and can take a site offline: it only runs with an explicit --max-safety destructive")
	}
	z, err := resolveZone(r, f.zone)
	if err != nil {
		return nil, err
	}
	// Fetch the record first (a real read, even under --dry-run) so the dry run,
	// and the result, say exactly what is going.
	cur, err := readCall(r, "dns_record", map[string]string{"zone_id": z.ID, "dns_record_id": id}, 1)
	if err != nil {
		return nil, fmt.Errorf("cannot find record %s in that zone, so nothing was deleted: %w", id, err)
	}
	cm, _ := cur.Data.(map[string]any)
	view := recordView(cm)
	res, err := r.Call("dns_record_delete", map[string]string{"zone_id": z.ID, "dns_record_id": id})
	if err != nil {
		return nil, explain(err)
	}
	if r.DryRun() {
		return map[string]any{"dry_run": true, "would_delete": view, "request": res.Data}, nil
	}
	return map[string]any{"deleted": view}, nil
}

// ---- purge ----

func purgeCommand() *apikit.Command {
	var zone, files, hosts, tags, mode, values string
	var everything bool
	return &apikit.Command{
		Name:    "purge",
		Summary: "purge a zone's cache: everything, or by URL, hostname or cache tag",
		Usage:   "purge --zone example.com (--everything | --files URL,URL | --hosts H,H | --tags T,T)",
		Setup: func(fs *flag.FlagSet) {
			fs.StringVar(&zone, "zone", "", "zone name (example.com) or zone id (required)")
			fs.BoolVar(&everything, "everything", false, "purge everything cached for the zone")
			fs.StringVar(&files, "files", "", "comma-separated full URLs to purge")
			fs.StringVar(&hosts, "hosts", "", "comma-separated hostnames to purge")
			fs.StringVar(&tags, "tags", "", "comma-separated Cache-Tag values to purge")
			fs.StringVar(&mode, "mode", "", "another way to choose the one mode: everything, files, hosts or tags (with --values)")
			fs.StringVar(&values, "values", "", "with --mode: the comma-separated URLs, hostnames or tags")
		},
		Run: func(r *apikit.Run) (any, error) {
			if len(r.Args) != 0 {
				return nil, apikit.Usage("purge takes no arguments")
			}
			set := flagsSet(r.FS)
			chosen, vals, err := purgeSpec(set, everything, files, hosts, tags, mode, values)
			if err != nil {
				return nil, err
			}
			z, err := resolveZone(r, zone)
			if err != nil {
				return nil, err
			}
			params := map[string]string{"zone_id": z.ID}
			if chosen == "everything" {
				params["purge_everything"] = "true"
			} else {
				params[chosen] = strings.Join(vals, ",")
			}
			res, err := r.Call("purge_cache", params)
			if err != nil {
				return nil, explain(err)
			}
			if r.DryRun() {
				return res.Data, nil
			}
			m, _ := res.Data.(map[string]any)
			out := map[string]any{"purged": chosen, "zone": z.ID, "id": str(m, "id")}
			if chosen != "everything" {
				out["count"] = len(vals)
			}
			return out, nil
		},
	}
}

// purgeSpec decides which single purge mode was asked for and checks its values.
// Cloudflare's own rule is that purge_everything must be alone and the other
// modes are separate request shapes; one mode per call keeps it unambiguous.
func purgeSpec(set map[string]bool, everything bool, files, hosts, tags, mode, values string) (string, []string, error) {
	var chosen []string
	if set["everything"] && everything {
		chosen = append(chosen, "everything")
	}
	for _, m := range []struct{ name, v string }{{"files", files}, {"hosts", hosts}, {"tags", tags}} {
		if set[m.name] {
			chosen = append(chosen, m.name)
		}
	}
	raw := map[string]string{"files": files, "hosts": hosts, "tags": tags, "everything": ""}
	if set["mode"] {
		if !in([]string{"everything", "files", "hosts", "tags"}, mode) {
			return "", nil, apikit.Usage("--mode must be everything, files, hosts or tags")
		}
		chosen = append(chosen, mode)
		raw[mode] = values
	} else if set["values"] {
		return "", nil, apikit.Usage("--values goes with --mode")
	}
	if len(chosen) != 1 {
		return "", nil, apikit.Usage("choose exactly one purge mode: --everything, --files, --hosts or --tags (got %d)", len(chosen))
	}
	m := chosen[0]
	if m == "everything" {
		if strings.TrimSpace(raw["everything"]) != "" {
			return "", nil, apikit.Usage("purging everything takes no values")
		}
		return m, nil, nil
	}
	var vals []string
	for _, v := range strings.Split(raw[m], ",") {
		if v = strings.TrimSpace(v); v != "" {
			vals = append(vals, v)
		}
	}
	if len(vals) == 0 {
		return "", nil, apikit.Usage("--%s needs at least one value", m)
	}
	for _, v := range vals {
		switch m {
		case "files":
			if !strings.HasPrefix(v, "http://") && !strings.HasPrefix(v, "https://") {
				return "", nil, apikit.Usage("%q is not a full URL: files to purge start with http:// or https:// (a comma inside a URL must be written %%2C)", v)
			}
		case "hosts":
			if strings.Contains(v, "/") || strings.Contains(v, ":") {
				return "", nil, apikit.Usage("%q is not a hostname: give just the host, like www.example.com", v)
			}
		}
	}
	return m, vals, nil
}

// ---- tunnels ----

func tunnelsCommand() *apikit.Command {
	var name, status string
	var all bool
	return &apikit.Command{
		Name:    "tunnels",
		Summary: "list Cloudflare Tunnels: name, id, status, connections (needs account_id)",
		Usage:   "tunnels [--name NAME] [--status healthy] [--all]",
		Setup: func(fs *flag.FlagSet) {
			fs.StringVar(&name, "name", "", "only the tunnel with this exact name")
			fs.StringVar(&status, "status", "", "only tunnels in this status: inactive, degraded, healthy, down")
			fs.BoolVar(&all, "all", false, "follow every page (default: the first page)")
		},
		Run: func(r *apikit.Run) (any, error) {
			if len(r.Args) != 0 {
				return nil, apikit.Usage("tunnels takes no arguments")
			}
			id, err := accountID(r)
			if err != nil {
				return nil, err
			}
			params := map[string]string{"account_id": id}
			if name != "" {
				params["name"] = name
			}
			if status != "" {
				params["status"] = status
			}
			pages := 1
			if all {
				pages = maxPages(r, "tunnels")
			}
			res, err := readCall(r, "tunnels", params, pages)
			if err != nil {
				return nil, err
			}
			out := []map[string]any{}
			for _, t := range list(res.Data) {
				conns, _ := t["connections"].([]any)
				out = append(out, map[string]any{"name": str(t, "name"), "id": str(t, "id"), "status": str(t, "status"), "connections": len(conns)})
			}
			sort.SliceStable(out, func(i, j int) bool { return out[i]["name"].(string) < out[j]["name"].(string) })
			return map[string]any{"tunnels": out, "count": len(out), "more": !all && numAt(res.Raw, "result_info.total_pages") > numAt(res.Raw, "result_info.page")}, nil
		},
	}
}
