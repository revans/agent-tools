package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// docli stack init|validate, docli plan, docli apply, docli adopt.

const stackExample = `# docli stack file: the DigitalOcean resources this project needs, written down once.
# Read it with:   docli stack validate        See what would change:   docli plan        Do it:   docli apply
#
# This file is committed to git, so it never holds secrets. docli rejects keys such as password or token, and
# values such as $VAR or ${VAR}: it does not read environment variables here. Database passwords are made by
# DigitalOcean and saved only to a 0600 file you name with: docli db connection NAME --user U --password-file PATH
#
# Names are the names you use in this file. docli stores SSH keys, VPCs and firewalls as docli-NAME so it can
# tell it made them (they cannot be tagged). Droplets and databases are tagged docli:managed.
# Relative file paths are from the project root; ~/ is your home directory.

version: 1
name: shop           # optional: tags droplets and databases docli:stack:shop
region: nyc3         # default region for everything below

ssh_keys:
  - name: deploy
    public_key_file: ~/.ssh/id_ed25519.pub      # the public half only; a private key is refused

vpcs:
  - name: shop-net
    ip_range: 10.50.0.0/24                      # optional

firewalls:
  # DigitalOcean firewalls deny by default: only what is listed is allowed.
  - name: shop-web
    rules:
      - in:tcp:22:from=0.0.0.0/0,::/0           # tighten this to your own address if you can
      - in:tcp:80:from=0.0.0.0/0,::/0
      - in:tcp:443:from=0.0.0.0/0,::/0
      - out:tcp:all:to=0.0.0.0/0,::/0
      - out:udp:all:to=0.0.0.0/0,::/0
      - out:icmp:all:to=0.0.0.0/0,::/0

droplets:
  - name: shop-web-1
    size: s-1vcpu-1gb
    image: ubuntu-24-04-x64
    vpc: shop-net
    ssh_keys: [deploy]
    firewalls: [shop-web]
    tags: [app]
    monitoring: true

databases:
  - name: shop-db
    version: "16"
    size: db-s-1vcpu-1gb
    nodes: 1
    vpc: shop-net
    trusted_droplets: [shop-web-1]              # only these may connect
    users: [shop]
    databases: [shop_production]

domains:
  - name: shop.example
    records:
      - {type: A, name: "@", droplet: shop-web-1, ttl: 300}   # the droplet's public IPv4, known once it exists
      - {type: CNAME, name: www, data: shop.example., ttl: 1800}
`

func (a *App) cmdStack(args []string) error {
	if len(args) == 0 {
		return usagef("usage: docli stack init|validate (try: docli help stack)")
	}
	switch args[0] {
	case "init":
		return a.stackInit(args[1:])
	case "validate":
		return a.stackValidate(args[1:])
	}
	return usagef("unknown stack command %q (init, validate)", args[0])
}

func (a *App) stackInit(args []string) error {
	fs, g := a.newFlags("stack init")
	file := fs.String("file", "", "")
	force := fs.Bool("force", false, "")
	pos, err := a.parseFlags(fs, args, "stack init")
	if err != nil {
		return err
	}
	if len(pos) > 0 {
		return usagef("usage: docli stack init [--file PATH] [--force]")
	}
	r := &Run{App: a, G: *g}
	path, display := r.stackPath(*file)
	if _, err := os.Lstat(path); err == nil && !*force {
		return fmt.Errorf("%s already exists; docli will not overwrite it without --force. Nothing was changed", display)
	}
	if g.DryRun {
		fmt.Fprintf(a.Stdout, "Dry run: would write an example stack file to %s. Nothing was changed.\n", display)
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("cannot create %s: %v", filepath.Dir(display), pathErr(err))
	}
	if err := os.WriteFile(path, []byte(stackExample), 0o644); err != nil {
		return fmt.Errorf("cannot write %s: %v", display, pathErr(err))
	}
	if g.JSON {
		return r.printJSON(map[string]any{"written": display})
	}
	fmt.Fprintf(a.Stdout, "Wrote an example stack file to %s. Edit it, then run: docli stack validate\n", display)
	return nil
}

type stackValidateJSON struct {
	File   string         `json:"file"`
	Valid  bool           `json:"valid"`
	Stack  string         `json:"stack"`
	Counts map[string]int `json:"counts"`
	Notes  []string       `json:"notes"`
}

func (a *App) stackValidate(args []string) error {
	fs, g := a.newFlags("stack validate")
	file := fs.String("file", "", "")
	pos, err := a.parseFlags(fs, args, "stack validate")
	if err != nil {
		return err
	}
	if len(pos) > 0 {
		return usagef("usage: docli stack validate [--file PATH]")
	}
	r := &Run{App: a, G: *g}
	if r.G.Region == "" {
		r.G.Region = a.Getenv("DOCLI_REGION")
	}
	s, display, err := r.loadStack(*file)
	if err != nil {
		return err
	}
	counts := map[string]int{"ssh_keys": len(s.SSHKeys), "vpcs": len(s.VPCs), "firewalls": len(s.Firewalls), "droplets": len(s.Droplets),
		"databases": len(s.Databases), "domains": len(s.Domains)}
	records := 0
	for _, d := range s.Domains {
		records += len(d.Records)
	}
	counts["dns_records"] = records
	for k, n := range map[string]int{"projects": len(s.Projects), "volumes": len(s.Volumes), "load_balancers": len(s.LBs)} {
		if n > 0 { // only when used, so a stack file from before milestone 4 validates exactly as it did
			counts[k] = n
		}
	}
	if g.JSON {
		return r.printJSON(stackValidateJSON{display, true, s.Name, counts, nonNil(s.Notes)})
	}
	fmt.Fprintf(a.Stdout, "%s is valid: %d SSH key(s), %d VPC(s), %d firewall(s), %d droplet(s), %d database(s), %d domain(s), %d DNS record(s).\n",
		display, counts["ssh_keys"], counts["vpcs"], counts["firewalls"], counts["droplets"], counts["databases"], counts["domains"], records)
	if n := counts["projects"] + counts["volumes"] + counts["load_balancers"]; n > 0 {
		fmt.Fprintf(a.Stdout, "  also: %d project(s), %d volume(s), %d load balancer(s).\n", counts["projects"], counts["volumes"], counts["load_balancers"])
	}
	for _, n := range s.Notes {
		fmt.Fprintf(a.Stdout, "  note: %s\n", n)
	}
	fmt.Fprintln(a.Stdout, "This was checked against the file only. `docli plan` checks it against your account.")
	return nil
}

// ---- plan ----

type planFlags struct {
	file      *string
	prune     *bool
	pruneData *bool
	mo        *manifestOpts
}

func addPlanFlags(fs *flag.FlagSet) *planFlags {
	return &planFlags{file: fs.String("file", "", ""), prune: fs.Bool("prune", false, ""), pruneData: fs.Bool("prune-data", false, ""), mo: addManifestFlags(fs)}
}

func (pf *planFlags) usageCheck() error {
	if *pf.pruneData && !*pf.prune {
		return usagef("--prune-data only makes sense with --prune")
	}
	return nil
}

// plainError turns a usage mistake into an ordinary error. For `plan`, exit code 2 means "changes found", so
// its own mistakes exit 1 and the message says what was wrong.
func plainError(err error) error {
	var u *usageError
	if errors.As(err, &u) {
		return errors.New(u.msg)
	}
	return err
}

func (a *App) cmdPlan(args []string) error {
	fs, g := a.newFlags("plan")
	pf := addPlanFlags(fs)
	pos, err := a.parseFlags(fs, args, "plan")
	if err != nil {
		return plainError(err)
	}
	if len(pos) > 0 {
		return plainError(usagef("usage: docli plan [--file PATH] [--prune [--prune-data]] [--json]"))
	}
	if err := pf.usageCheck(); err != nil {
		return plainError(err)
	}
	r, s, display, err := a.startWithStack(*g, *pf.file)
	if err != nil {
		return err
	}
	sp, err := r.planStack(s, display, *pf.mo, planOpts{Prune: *pf.prune, PruneData: *pf.pruneData})
	if err != nil {
		return err
	}
	if g.JSON {
		if err := r.printJSON(sp.Doc); err != nil {
			return err
		}
	} else {
		printStackPlan(r.Stdout, sp.Doc)
	}
	if sp.Doc.HasChanges {
		return &exitCodeError{exitUsage} // 2: there are changes (documented)
	}
	return nil
}

// startWithStack reads and checks the stack file first, so a mistake in it is reported without needing a token
// or the network, then connects.
func (a *App) startWithStack(g Globals, file string) (*Run, *Stack, string, error) {
	pre := &Run{App: a, G: g}
	if pre.G.Region == "" {
		pre.G.Region = a.Getenv("DOCLI_REGION")
	}
	s, display, err := pre.loadStack(file)
	if err != nil {
		return nil, nil, display, err
	}
	r, err := a.start(g)
	return r, s, display, err
}

var actionLabel = map[string]string{
	actCreate: "create", actChange: "change", actUnchanged: "ok", actReplace: "needs replacement, NOT DONE",
	actOrphaned: "orphaned, not deleted", actUnmanaged: "not docli's, left alone", actPrune: "PRUNE (delete)",
}

var actionMark = map[string]string{actCreate: "+", actChange: "~", actUnchanged: "=", actReplace: "!", actOrphaned: "?", actUnmanaged: "=", actPrune: "-"}

func printStackPlan(w io.Writer, doc *StackPlanDoc) {
	title := doc.StackFile
	if doc.Stack != "" {
		title += ", stack " + doc.Stack
	}
	fmt.Fprintf(w, "Plan for %s\n\n", title)
	if len(doc.Items) == 0 {
		fmt.Fprintln(w, "  (the stack file declares nothing, and docli created nothing that is missing from it)")
	}
	for _, it := range doc.Items {
		cost := ""
		if it.MonthlyUSD != nil {
			cost = fmt.Sprintf("  %s/month", money(*it.MonthlyUSD))
		}
		fmt.Fprintf(w, "  %s %-11s %-30s %s%s\n", actionMark[it.Action], it.Kind, it.Name, actionLabel[it.Action], cost)
		if it.Action == actUnchanged {
			continue
		}
		for _, d := range it.Details {
			fmt.Fprintf(w, "        %s\n", d)
		}
	}
	s := doc.Summary
	fmt.Fprintf(w, "\nPlan: %d to create, %d to change, %d unchanged, %d need replacement (not done), %d orphaned (not deleted), %d not docli's (left alone), %d to prune.\n",
		s.Create, s.Change, s.Unchanged, s.NeedsReplacement, s.Orphaned, s.Unmanaged, s.Prune)
	if doc.MonthlyCostAddedUSD > 0 || s.Create > 0 {
		fmt.Fprintf(w, "Added monthly cost: %s", money(doc.MonthlyCostAddedUSD))
		if len(doc.CostUnknownFor) > 0 {
			fmt.Fprintf(w, " (not shown for: %s)", strings.Join(doc.CostUnknownFor, ", "))
		}
		fmt.Fprintln(w)
	}
	for _, x := range doc.Warnings {
		fmt.Fprintf(w, "Warning: %s\n", x)
	}
	if len(doc.NotManaged) > 0 {
		var names []string
		for _, n := range doc.NotManaged {
			names = append(names, n.Kind+" "+n.Name)
		}
		fmt.Fprintf(w, "In the account, not created by docli and not in the file (only reported, never touched): %s\n", strings.Join(names, ", "))
	}
	if doc.HasChanges {
		fmt.Fprintln(w, "Exit code 2 means there are changes. Nothing was changed.")
	} else {
		fmt.Fprintln(w, "No changes. The account matches the stack file.")
	}
}

// ---- apply ----

type StepFailure struct {
	Step  string `json:"step"`
	Error string `json:"error"`
}

type StackApplyDoc struct {
	Applied          bool          `json:"applied"` // every step ran
	Done             []string      `json:"done"`
	Failed           []StepFailure `json:"failed"`
	NotDone          []string      `json:"not_done"`
	NeedsReplacement []string      `json:"needs_replacement"`
	Orphaned         []string      `json:"orphaned"`
	NextSteps        []string      `json:"next_steps"`
}

type stackDryRunDoc struct {
	DryRun bool         `json:"dry_run"`
	Plan   StackPlanDoc `json:"plan"`
}

// clean removes the token and any password docli has seen from text that may have come from the server.
func (r *Run) clean(s string) string {
	if tok := r.App.currentToken(); tok != "" {
		s = redact(s, tok)
	}
	return r.scrubSecrets(redact(s, r.Token))
}

func (a *App) cmdApply(args []string) error {
	fs, g := a.newFlags("apply")
	pf := addPlanFlags(fs)
	confirmPrune := fs.String("confirm-prune", "", "")
	waitTimeout := fs.Duration("wait-timeout", 5*time.Minute, "")
	dbWait := fs.Duration("db-wait-timeout", 20*time.Minute, "")
	var pruneNames stringList
	fs.Var(&pruneNames, "prune-confirm-name", "")
	pos, err := a.parseFlags(fs, args, "apply")
	if err != nil {
		return err
	}
	if len(pos) > 0 {
		return usagef("usage: docli apply [--file PATH] [--prune [--prune-data]] [--yes] [--dry-run]")
	}
	if err := pf.usageCheck(); err != nil {
		return err
	}
	if *confirmPrune != "" && !*pf.prune {
		return usagef("--confirm-prune only makes sense with --prune")
	}
	r, s, display, err := a.startWithStack(*g, *pf.file)
	if err != nil {
		return err
	}
	sp, err := r.planStack(s, display, *pf.mo, planOpts{Prune: *pf.prune, PruneData: *pf.pruneData})
	if err != nil {
		return err
	}
	sp.x.waitDroplet_, sp.x.waitDB_ = *waitTimeout, *dbWait
	doc := sp.Doc

	if g.DryRun {
		if g.JSON {
			return r.printJSON(stackDryRunDoc{true, *doc})
		}
		printStackPlan(r.Stdout, doc)
		fmt.Fprintln(r.Stdout, "Dry run: nothing was changed.")
		return nil
	}
	if g.JSON {
		printStackPlan(r.Stderr, doc)
	} else {
		printStackPlan(r.Stdout, doc)
	}
	if len(sp.ops) == 0 {
		return r.finishApply(sp, nil, nil, nil)
	}
	ok, err := r.askProceed()
	if !ok || err != nil {
		return err
	}
	if err := r.confirmPrune(sp.ops, *confirmPrune, pruneNames); err != nil {
		return err
	}

	var done []string
	var failed []StepFailure
	var notDone []string
	for i, op := range sp.ops {
		if err := op.Do(); err != nil {
			failed = append(failed, StepFailure{op.Label, r.clean(err.Error())})
			for _, rest := range sp.ops[i+1:] {
				notDone = append(notDone, rest.Label)
			}
			break
		}
		done = append(done, op.Label)
		if !g.JSON {
			fmt.Fprintf(r.Stdout, "  done: %s\n", op.Label)
		}
	}
	return r.finishApply(sp, done, failed, notDone)
}

// finishApply says exactly what was done and what was not.
func (r *Run) finishApply(sp *stackPlan, done []string, failed []StepFailure, notDone []string) error {
	out := StackApplyDoc{Applied: len(failed) == 0, Done: nonNil(done), Failed: nonNil(failed), NotDone: nonNil(notDone),
		NeedsReplacement: []string{}, Orphaned: []string{}, NextSteps: []string{}}
	for _, it := range sp.Doc.Items {
		switch it.Action {
		case actReplace:
			out.NeedsReplacement = append(out.NeedsReplacement, it.Kind+" "+it.Name)
		case actOrphaned:
			out.Orphaned = append(out.Orphaned, it.Kind+" "+it.Name)
		}
	}
	if len(failed) == 0 {
		out.NextSteps = nonNil(sp.x.nextSteps)
	}
	if r.G.JSON {
		if err := r.printJSON(out); err != nil {
			return err
		}
	} else {
		w := r.Stdout
		switch {
		case len(failed) > 0:
			fmt.Fprintf(w, "\nStopped at the first failure. Nothing was retried and nothing further was attempted.\n")
			fmt.Fprintf(w, "Done (%d):\n", len(done))
			for _, d := range done {
				fmt.Fprintf(w, "  - %s\n", d)
			}
			fmt.Fprintf(w, "FAILED:\n  - %s: %s\n", failed[0].Step, failed[0].Error)
			fmt.Fprintf(w, "Not done (%d):\n", len(notDone))
			for _, d := range notDone {
				fmt.Fprintf(w, "  - %s\n", d)
			}
			fmt.Fprintln(w, "Fix the cause and run `docli apply` again: steps already done are seen as done and are not repeated.")
		case len(done) == 0:
			fmt.Fprintln(w, "\nNothing to do: the account already matches the stack file.")
		default:
			fmt.Fprintf(w, "\nApplied %d step(s).\n", len(done))
		}
		if len(out.NeedsReplacement) > 0 {
			fmt.Fprintf(w, "Needs replacement, not done (docli never replaces these automatically): %s\n", strings.Join(out.NeedsReplacement, ", "))
		}
		if len(out.Orphaned) > 0 {
			fmt.Fprintf(w, "Orphaned, not deleted: %s\n", strings.Join(out.Orphaned, ", "))
		}
		if len(out.NextSteps) > 0 {
			fmt.Fprintln(w, "Next:")
			for _, n := range out.NextSteps {
				fmt.Fprintf(w, "  %s\n", n)
			}
		}
	}
	if len(failed) > 0 {
		return &exitCodeError{exitError}
	}
	return nil
}

const pruneWord = "prune"

// confirmPrune asks for everything a deletion needs, before any step runs. The word "prune" is always needed.
// A droplet, database or domain also needs its own name typed; the rest need a y. --yes never skips the names,
// and without a terminal each such name must be given with --prune-confirm-name.
func (r *Run) confirmPrune(ops []*stackOp, word string, names []string) error {
	var prunes []*stackOp
	for _, o := range ops {
		if o.Prune {
			prunes = append(prunes, o)
		}
	}
	if len(prunes) == 0 {
		return nil
	}
	fmt.Fprintf(r.Stderr, "--prune will DELETE %d resource(s) docli created that the stack file no longer lists:\n", len(prunes))
	for _, o := range prunes {
		fmt.Fprintf(r.Stderr, "  - %s\n", o.Label)
	}
	switch {
	case word == pruneWord:
	case word != "":
		return refusedf("--confirm-prune must be the word %q. Nothing was changed", pruneWord)
	case r.G.Yes || !r.StdinIsTTY:
		return refusedf("deleting needs the word %q: run it in a terminal and type it, or pass --confirm-prune %s. --yes alone is not enough. Nothing was changed", pruneWord, pruneWord)
	default:
		fmt.Fprintf(r.Stderr, "Type the word %q to confirm: ", pruneWord)
		if ans, _ := r.readLine(); ans != pruneWord {
			return refusedf("the word did not match. Nothing was changed")
		}
	}
	for _, o := range prunes {
		if o.TypedName != "" {
			switch {
			case contains(names, o.TypedName):
			case r.StdinIsTTY:
				fmt.Fprintf(r.Stderr, "Type the name %q to delete it: ", o.TypedName)
				if ans, _ := r.readLine(); ans != o.TypedName {
					return refusedf("the name did not match for %s. Nothing was changed", o.Label)
				}
			default:
				return refusedf("%s needs its name %q confirmed and stdin is not a terminal. Add --prune-confirm-name %s. Nothing was changed", o.Label, o.TypedName, o.TypedName)
			}
			continue
		}
		if r.G.Yes || word == pruneWord && !r.StdinIsTTY {
			continue
		}
		if !r.StdinIsTTY {
			return refusedf("%s needs a confirmation and stdin is not a terminal. Add --yes. Nothing was changed", o.Label)
		}
		fmt.Fprintf(r.Stderr, "%s? [y/N] ", o.Label)
		if ans, _ := r.readLine(); strings.ToLower(ans) != "y" && strings.ToLower(ans) != "yes" {
			return refusedf("not confirmed: %s. Nothing was changed", o.Label)
		}
	}
	return nil
}
