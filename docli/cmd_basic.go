package main

import (
	"fmt"
	"runtime"
	"strings"
)

func (a *App) cmdVersion(args []string) error {
	fs, g := a.newFlags("version")
	if _, err := a.parseFlags(fs, args, "version"); err != nil {
		return err
	}
	if g.JSON {
		r := &Run{App: a, G: *g}
		return r.printJSON(struct {
			Version string `json:"version"`
			Go      string `json:"go"`
			OS      string `json:"os"`
			Arch    string `json:"arch"`
		}{version, runtime.Version(), runtime.GOOS, runtime.GOARCH})
	}
	fmt.Fprintf(a.Stdout, "docli %s\n", version)
	return nil
}

func (a *App) cmdHelp(args []string) error {
	if len(args) == 0 {
		fmt.Fprint(a.Stdout, helpTop)
		return nil
	}
	h := helpFor(strings.Join(args, " "))
	if h == "" {
		return usagef("no help for %q (try: docli help)", strings.Join(args, " "))
	}
	fmt.Fprint(a.Stdout, h)
	return nil
}

type probeJSON struct {
	Resource string `json:"resource"`
	Read     string `json:"read"`
	Detail   string `json:"detail"`
}

type tokenCheckJSON struct {
	Valid        bool        `json:"valid"`
	Email        string      `json:"email"`
	Status       string      `json:"status"`
	Team         string      `json:"team"`
	DropletLimit int         `json:"droplet_limit"`
	Source       string      `json:"source"`
	ReadAccess   []probeJSON `json:"read_access"`
	WriteAccess  string      `json:"write_access"`
	Notes        []string    `json:"notes"`
}

func (a *App) cmdToken(args []string) error {
	if len(args) == 0 || args[0] != "check" {
		return usagef("usage: docli token check")
	}
	fs, g := a.newFlags("token check")
	if _, err := a.parseFlags(fs, args[1:], "token check"); err != nil {
		return err
	}
	r, err := a.start(*g)
	if err != nil {
		return err
	}
	out := tokenCheckJSON{Source: a.tokenSource(), ReadAccess: []probeJSON{}, Notes: []string{},
		WriteAccess: "unknown: DigitalOcean offers no way to ask a token what it may change, and docli will not change anything just to find out"}
	acct, err := r.Cloud.Account(r.Ctx)
	switch s := statusOf(err); {
	case err == nil:
		out.Valid, out.Email, out.Status, out.DropletLimit = true, acct.Email, acct.Status, acct.DropletLimit
		if acct.Team != nil {
			out.Team = acct.Team.Name
		}
	case s == 401:
		return fmt.Errorf("DigitalOcean rejected the token (401). It may be wrong, expired or revoked")
	case s == 403:
		out.Valid = true
		out.Notes = append(out.Notes, "the token cannot read the account (403), so it was likely created without account read scope; the token itself was accepted")
	default:
		return fmt.Errorf("could not reach DigitalOcean to check the token: %v", err)
	}
	for _, p := range r.Cloud.ProbeReadAccess(r.Ctx) {
		out.ReadAccess = append(out.ReadAccess, probeJSON{p.Name, p.Status, p.Detail})
	}
	out.Notes = append(out.Notes, "scopes are inferred from read requests; the API does not list a token's scopes")
	if g.JSON {
		return r.printJSON(out)
	}
	fmt.Fprintf(a.Stdout, "Token source:   %s\n", out.Source)
	fmt.Fprintf(a.Stdout, "Account:        %s (%s)\n", orDash(out.Email), orDash(out.Status))
	if out.Team != "" {
		fmt.Fprintf(a.Stdout, "Team:           %s\n", out.Team)
	}
	fmt.Fprintf(a.Stdout, "Droplet limit:  %d\n\n", out.DropletLimit)
	var rows [][]string
	for _, p := range out.ReadAccess {
		rows = append(rows, []string{p.Resource, p.Read, p.Detail})
	}
	printTable(a.Stdout, []string{"RESOURCE", "READ", "DETAIL"}, rows)
	fmt.Fprintf(a.Stdout, "\nWrite access: %s.\n", out.WriteAccess)
	for _, n := range out.Notes {
		fmt.Fprintf(a.Stdout, "Note: %s.\n", n)
	}
	return nil
}

// tokenSource says where the token came from, never the token.
func (a *App) tokenSource() string {
	switch {
	case strings.TrimSpace(a.Getenv("DOCLI_TOKEN")) != "":
		return "environment variable DOCLI_TOKEN"
	case strings.TrimSpace(a.Getenv("DIGITALOCEAN_TOKEN")) != "":
		return "environment variable DIGITALOCEAN_TOKEN"
	}
	return "file ~/.config/docli/token"
}
