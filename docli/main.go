// docli manages DigitalOcean droplets, VPCs, firewalls and SSH keys from the command line, safely:
// every change shows a plan and waits for confirmation, and nothing is deleted unless docli created it.
package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"strings"
	"time"
)

func main() { os.Exit(newOSApp().Main(os.Args[1:])) }

// Main runs the command line and returns the exit code.
func (a *App) Main(args []string) int {
	a.errJSON = false
	a.base = Globals{Timeout: 30 * time.Second}
	fs, g := a.newFlags("docli")
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			fmt.Fprint(a.Stdout, helpTop)
			return exitOK
		}
		return a.fail(usagef("%s (try: docli help)", cleanFlagErr(err)), a.base, "")
	}
	a.base = *g
	rest := fs.Args()
	if len(rest) == 0 {
		fmt.Fprint(a.Stdout, helpTop)
		return exitUsage
	}
	err := a.dispatch(rest[0], rest[1:])
	var h helpRequested
	if errors.As(err, &h) {
		fmt.Fprint(a.Stdout, helpFor(h.cmd))
		return exitOK
	}
	return a.fail(err, a.base, "")
}

func (a *App) dispatch(cmd string, args []string) error {
	switch cmd {
	case "version", "--version":
		return a.cmdVersion(args)
	case "help", "-h", "--help":
		return a.cmdHelp(args)
	case "token":
		return a.cmdToken(args)
	case "sizes":
		return a.cmdSizes(args)
	case "regions":
		return a.cmdRegions(args)
	case "images":
		return a.cmdImages(args)
	case "ssh-key":
		return a.cmdSSHKey(args)
	case "vpc":
		return a.cmdVPC(args)
	case "firewall":
		return a.cmdFirewall(args)
	case "droplet":
		return a.cmdDroplet(args)
	case "domain":
		return a.cmdDomain(args)
	case "dns":
		return a.cmdDNS(args)
	case "db":
		return a.cmdDB(args)
	case "project":
		return a.cmdProject(args)
	case "volume":
		return a.cmdVolume(args)
	case "reserved-ip":
		return a.cmdReservedIP(args)
	case "lb":
		return a.cmdLB(args)
	case "spaces":
		return a.cmdSpaces(args)
	case "manifest":
		return a.cmdManifest(args)
	case "stack":
		return a.cmdStack(args)
	case "plan":
		return a.cmdPlan(args)
	case "apply":
		return a.cmdApply(args)
	case "adopt":
		return a.cmdAdopt(args)
	}
	return usagef("unknown command %q (try: docli help)", cmd)
}

// fail prints an error and returns its exit code. The token is removed from the text first.
func (a *App) fail(err error, g Globals, token string) int {
	if err == nil {
		return exitOK
	}
	code := exitError
	var u *usageError
	var rf *refusedError
	var se silentError
	var ec *exitCodeError
	switch {
	case errors.As(err, &ec):
		return ec.code
	case errors.As(err, &se):
		return exitError
	case errors.As(err, &u):
		code = exitUsage
	case errors.As(err, &rf):
		code = exitRefused
	}
	a.noteSpacesSecret()
	msg := err.Error()
	if tok := a.currentToken(); tok != "" {
		msg = redact(msg, tok)
	}
	msg = a.scrubSecrets(msg)
	if g.JSON || a.errJSON {
		b, _ := json.Marshal(struct {
			Error    string `json:"error"`
			ExitCode int    `json:"exit_code"`
		}{msg, code})
		fmt.Fprintln(a.Stderr, string(b))
	} else {
		fmt.Fprintln(a.Stderr, "docli: "+strings.TrimSpace(msg))
	}
	return code
}

// currentToken is used only to scrub error text.
func (a *App) currentToken() string {
	if t, err := a.loadToken(); err == nil {
		return t
	}
	return ""
}
