package main

import (
	"context"
	"fmt"
	"io"
	"strings"
)

const (
	managedTag = "docli:managed"
	namePrefix = "docli-"
)

// redactWriter hides the token from anything written through it (used for --debug).
type redactWriter struct {
	w     io.Writer
	token string
}

func (r *redactWriter) Write(p []byte) (int, error) {
	_, err := r.w.Write([]byte(redact(string(p), r.token)))
	return len(p), err
}

// start loads the token and connects.
func (a *App) start(g Globals) (*Run, error) {
	a.noteSpacesSecret()
	if g.Region == "" {
		g.Region = a.Getenv("DOCLI_REGION")
	}
	run := &Run{App: a, G: g}
	ctx := context.Background()
	if a.Cloud != nil {
		run.Cloud, run.Ctx = a.Cloud, ctx
		return run, nil
	}
	tok, err := a.loadToken()
	if err != nil {
		return nil, err
	}
	run.Token = tok
	c, err := a.newGodoCloud(tok, g)
	if err != nil {
		return nil, err
	}
	run.Cloud, run.Ctx = c, ctx
	return run, nil
}

// Plan is what a mutating command says it will do.
type Plan struct {
	Action     string   `json:"action"`
	Summary    string   `json:"summary"`
	Steps      []string `json:"steps"`
	Warnings   []string `json:"warnings"`
	MonthlyUSD *float64 `json:"monthly_cost_usd"`
}

type dryRunDoc struct {
	DryRun bool `json:"dry_run"`
	Plan   Plan `json:"plan"`
}

func (p Plan) print(w io.Writer) {
	fmt.Fprintf(w, "Plan: %s\n", p.Summary)
	for _, s := range p.Steps {
		fmt.Fprintf(w, "  - %s\n", s)
	}
	if p.MonthlyUSD != nil {
		fmt.Fprintf(w, "  Estimated cost: %s per month\n", money(*p.MonthlyUSD))
	}
	for _, s := range p.Warnings {
		fmt.Fprintf(w, "  Warning: %s\n", s)
	}
}

// approveOpts controls how a plan is confirmed.
type approveOpts struct {
	TypeName    string // when set, the person must type this name
	ConfirmName string // value of --confirm-name
}

// Approve prints the plan, then decides whether to go ahead. It returns false with no error for --dry-run.
// It never assumes yes: without --yes it needs a terminal and an answer of y/yes.
func (r *Run) Approve(p Plan, o approveOpts) (bool, error) {
	p.Steps, p.Warnings = nonNil(p.Steps), nonNil(p.Warnings)
	if r.G.JSON {
		p.print(r.Stderr)
	} else {
		p.print(r.Stdout)
	}
	if r.G.DryRun {
		if r.G.JSON {
			return false, r.printJSON(dryRunDoc{true, p})
		}
		fmt.Fprintln(r.Stdout, "Dry run: nothing was changed.")
		return false, nil
	}
	if o.TypeName != "" {
		return r.approveTyped(o)
	}
	return r.askProceed()
}

// askProceed is the plain y/N question. It never assumes yes: without --yes it needs a terminal and a y.
func (r *Run) askProceed() (bool, error) {
	if r.G.Yes {
		return true, nil
	}
	if !r.StdinIsTTY {
		return false, refusedf("this changes your account and stdin is not a terminal, so there is nobody to confirm. Nothing was changed. Run it again with --yes to go ahead, or --dry-run to see the plan")
	}
	fmt.Fprint(r.Stderr, "Proceed? [y/N] ")
	ans, _ := r.readLine()
	switch strings.ToLower(ans) {
	case "y", "yes":
		return true, nil
	}
	return false, refusedf("cancelled. Nothing was changed")
}

func (r *Run) approveTyped(o approveOpts) (bool, error) {
	if o.ConfirmName != "" && o.ConfirmName != o.TypeName {
		return false, refusedf("--confirm-name %q does not match %q. Nothing was changed", o.ConfirmName, o.TypeName)
	}
	if o.ConfirmName == o.TypeName && r.G.Yes {
		return true, nil
	}
	if !r.StdinIsTTY {
		return false, refusedf("this needs the name %q typed to confirm and stdin is not a terminal. Nothing was changed. To run it unattended use: --yes --confirm-name %s", o.TypeName, o.TypeName)
	}
	fmt.Fprintf(r.Stderr, "Type the name %q to confirm: ", o.TypeName)
	ans, _ := r.readLine()
	if ans != o.TypeName {
		return false, refusedf("the name did not match. Nothing was changed")
	}
	return true, nil
}

// deleteSpec describes something about to be deleted, for the ownership check.
type deleteSpec struct {
	Kind    string // "droplet"
	Verb    string // "delete" unless something else is guarded the same way (for example "update")
	Summary string // overrides the default "delete KIND NAME" plan summary
	Name    string
	Managed bool
	Rule    string // how ownership is known: "tag docli:managed" or "a name starting with docli-"
	Always  bool   // the name must always be typed (droplets)
	Steps   []string
	Warns   []string
}

// ApproveDelete refuses anything docli did not create unless --force-unmanaged is given and the name is typed.
func (r *Run) ApproveDelete(s deleteSpec, forceUnmanaged bool, confirmName string) (bool, error) {
	verb := s.Verb
	if verb == "" {
		verb = "delete"
	}
	if !s.Managed && !forceUnmanaged {
		return false, refusedf("refusing to %s %s %q: it does not have %s, so docli did not create it. Nothing was changed. If you are sure, add --force-unmanaged and type the name", verb, s.Kind, s.Name, s.Rule)
	}
	p := Plan{Action: s.Kind + "." + verb, Summary: firstNonEmpty(s.Summary, fmt.Sprintf("%s %s %q", verb, s.Kind, s.Name)), Steps: s.Steps, Warnings: s.Warns}
	typed := ""
	if s.Always || !s.Managed {
		typed = s.Name
	}
	if !s.Managed {
		p.Warnings = append(p.Warnings, fmt.Sprintf("%s was NOT created by docli (no %s). You are forcing the %s.", s.Name, s.Rule, verb))
	}
	return r.Approve(p, approveOpts{TypeName: typed, ConfirmName: confirmName})
}

// ensureNamePrefix adds docli- to names of things that cannot carry tags (ssh keys, vpcs, firewalls).
func ensureNamePrefix(n string) string {
	if strings.HasPrefix(n, namePrefix) {
		return n
	}
	return namePrefix + n
}

func hasManagedPrefix(n string) bool { return strings.HasPrefix(n, namePrefix) }

func hasTag(tags []string, t string) bool {
	for _, x := range tags {
		if x == t {
			return true
		}
	}
	return false
}
