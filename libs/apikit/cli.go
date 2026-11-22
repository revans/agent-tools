package apikit

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Service describes one API tool. Main turns it into a command line with the
// standard commands (endpoints, call, check, accounts, init, where) plus
// whatever high-level Commands the service adds.
type Service struct {
	Name      string // the binary and config name, e.g. "reddit"
	Summary   string
	Version   string
	Starter   string // starter config written by `init`
	Builtin   string // the built-in endpoint specs (TOML)
	UserAgent string

	// NewClient builds a client for one account: base URL and credentials.
	NewClient func(account string, acct map[string]any) (*Client, error)
	// Check verifies the credentials with the cheapest real call.
	Check func(ctx context.Context, r *Run) (any, error)
	// Commands are the service's own high-level commands.
	Commands []*Command
}

// Command is one subcommand.
type Command struct {
	Name    string
	Summary string
	Usage   string // e.g. "search --query TEXT [--subreddit NAME]"
	// Setup adds the command's own flags; the common ones are already there.
	Setup func(fs *flag.FlagSet)
	// Run does the work. Its result is printed as JSON.
	Run func(r *Run) (any, error)
	// NeedsAccount is false for commands that read no credentials.
	NoAccount bool
}

// Run is one invocation of a command.
type Run struct {
	Svc  *Service
	Ctx  context.Context
	FS   *flag.FlagSet
	Args []string // positional arguments

	account   string
	config    string
	dryRun    bool
	debug     bool
	maxSafety string
	pretty    bool
	params    stringList
	pages     int

	client *Client
	reg    *Registry
	acct   string
}

type stringList []string

func (s *stringList) String() string     { return strings.Join(*s, ",") }
func (s *stringList) Set(v string) error { *s = append(*s, v); return nil }

// Main runs the tool. It never returns.
func Main(svc *Service) {
	os.Exit(run(svc, os.Args[1:], os.Stdout, os.Stderr))
}

// RunArgs runs one command line and returns its exit code. Tests use it.
func RunArgs(svc *Service, args []string, stdout, stderr io.Writer) int {
	return run(svc, args, stdout, stderr)
}

// DryRun reports whether --dry-run was given.
func (r *Run) DryRun() bool { return r.dryRun }

func (svc *Service) commands() []*Command {
	std := []*Command{
		{Name: "endpoints", Summary: "list the API endpoints this tool knows (or show one)", Usage: "endpoints [NAME]", NoAccount: true, Run: cmdEndpoints},
		{Name: "call", Summary: "call any endpoint by name", Usage: "call ENDPOINT [--param key=value]... [--pages N]", Run: cmdCall,
			Setup: func(fs *flag.FlagSet) {}},
		{Name: "check", Summary: "verify the account's credentials", Usage: "check", Run: cmdCheck},
		{Name: "accounts", Summary: "list configured accounts (never prints secrets)", Usage: "accounts", NoAccount: true, Run: cmdAccounts},
		{Name: "init", Summary: "write a starter config file", Usage: "init", NoAccount: true, Run: cmdInit},
		{Name: "where", Summary: "show where the config and extra endpoints are read from", Usage: "where", NoAccount: true, Run: cmdWhere},
	}
	return append(append([]*Command{}, svc.Commands...), std...)
}

func run(svc *Service, args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 || args[0] == "help" || args[0] == "-h" || args[0] == "--help" {
		usage(svc, stdout)
		return 0
	}
	if args[0] == "version" || args[0] == "--version" {
		fmt.Fprintln(stdout, svc.Name, svc.Version)
		return 0
	}
	var cmd *Command
	for _, c := range svc.commands() {
		if c.Name == args[0] {
			cmd = c
			break
		}
	}
	if cmd == nil {
		fmt.Fprintf(stderr, "%s: unknown command %q. Run `%s help`.\n", svc.Name, args[0], svc.Name)
		return 2
	}
	r := &Run{Svc: svc, Ctx: context.Background()}
	fs := flag.NewFlagSet(svc.Name+" "+cmd.Name, flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.StringVar(&r.account, "account", "", "account name from the config (default: the default or only account)")
	fs.StringVar(&r.config, "config", "", "config file (else $"+envName(svc.Name, "CONFIG")+", ~/.config/"+svc.Name+"/config.toml, ./"+svc.Name+".toml)")
	fs.BoolVar(&r.dryRun, "dry-run", false, "show the request and send nothing")
	fs.BoolVar(&r.debug, "debug", false, "log each request's method, URL and status to stderr")
	fs.StringVar(&r.maxSafety, "max-safety", SafetyWrite, "refuse endpoints above this level: read, write or destructive")
	fs.BoolVar(&r.pretty, "pretty", false, "indent the JSON output (the default when stdout is a terminal)")
	r.FS = fs
	if cmd.Name == "call" {
		cmdCallFlags(r)
	}
	if cmd.Setup != nil {
		cmd.Setup(fs)
	}
	pos, err := parseInterspersed(fs, args[1:])
	if err != nil {
		if err == flag.ErrHelp {
			return 0
		}
		return 2
	}
	r.Args = pos
	if SafetyRank(r.maxSafety) < 0 {
		fmt.Fprintf(stderr, "%s: --max-safety must be read, write or destructive\n", svc.Name)
		return 2
	}
	out, err := cmd.Run(r)
	if err != nil {
		if ue, ok := err.(usageError); ok {
			fmt.Fprintf(stderr, "%s: %s\nusage: %s %s\n", svc.Name, string(ue), svc.Name, cmd.Usage)
			return 2
		}
		fmt.Fprintf(stderr, "%s: %v\n", svc.Name, err)
		return 1
	}
	if out != nil {
		if err := printJSON(stdout, out, r.pretty || isTerminal(stdout)); err != nil {
			fmt.Fprintf(stderr, "%s: %v\n", svc.Name, err)
			return 1
		}
	}
	return 0
}

type usageError string

func (u usageError) Error() string { return string(u) }

// Usage returns an error that makes the tool print the command's usage line and exit 2.
func Usage(format string, a ...any) error { return usageError(fmt.Sprintf(format, a...)) }

func usage(svc *Service, w io.Writer) {
	fmt.Fprintf(w, "%s: %s\n\nUsage: %s COMMAND [flags]\n\nCommands:\n", svc.Name, svc.Summary, svc.Name)
	cmds := svc.commands()
	for _, c := range cmds {
		fmt.Fprintf(w, "  %-10s %s\n", c.Name, c.Summary)
	}
	fmt.Fprintf(w, "\nFlags on every command:\n  --account NAME   --config FILE   --dry-run   --debug   --max-safety read|write|destructive   --pretty\n")
	fmt.Fprintf(w, "\nOutput is JSON. Errors go to stderr; exit 0 ok, 1 error, 2 usage.\nCredentials come from the config file; none is ever taken from a flag.\n")
}

func isTerminal(w io.Writer) bool {
	f, ok := w.(*os.File)
	if !ok {
		return false
	}
	st, err := f.Stat()
	return err == nil && st.Mode()&os.ModeCharDevice != 0
}

func printJSON(w io.Writer, v any, pretty bool) error {
	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(false)
	if pretty {
		enc.SetIndent("", "  ")
	}
	return enc.Encode(v)
}

// parseInterspersed lets flags and positionals mix (call reddit_x --param a=b).
// Everything after a bare -- is positional.
func parseInterspersed(fs *flag.FlagSet, args []string) ([]string, error) {
	var tail []string
	for i, a := range args {
		if a == "--" {
			args, tail = args[:i], args[i+1:]
			break
		}
	}
	var pos []string
	for {
		if err := fs.Parse(args); err != nil {
			return nil, err
		}
		args = fs.Args()
		if len(args) == 0 {
			break
		}
		pos = append(pos, args[0])
		args = args[1:]
	}
	return append(pos, tail...), nil
}

// Client builds (once) the client for the chosen account.
func (r *Run) Client() (*Client, error) {
	if r.client != nil {
		return r.client, nil
	}
	cfg, err := LoadConfig(r.Svc.Name, r.config)
	if err != nil {
		return nil, err
	}
	name, acct, err := cfg.Account(r.account)
	if err != nil {
		return nil, err
	}
	c, err := r.Svc.NewClient(name, acct)
	if err != nil {
		return nil, err
	}
	if c.HTTP == nil {
		c.HTTP = &http.Client{}
	}
	if c.UserAgent == "" {
		c.UserAgent = r.Svc.UserAgent
	}
	c.DryRun, c.MaxSafety = r.dryRun, r.maxSafety
	if r.debug {
		c.Debug = os.Stderr
	}
	r.client, r.acct = c, name
	return c, nil
}

// AccountName is the account in use, once Client has been called.
func (r *Run) AccountName() string { return r.acct }

// Registry loads the built-in endpoints plus any in the user's endpoints.d.
func (r *Run) Registry() (*Registry, error) {
	if r.reg != nil {
		return r.reg, nil
	}
	reg, err := LoadRegistry(r.Svc.Builtin, endpointDirs(r.Svc.Name))
	if err != nil {
		return nil, err
	}
	r.reg = reg
	return reg, nil
}

func endpointDirs(tool string) []string {
	home := ConfigHome()
	dirs := []string{
		filepath.Join(home, "agent-tools", tool, "endpoints.d"),
		filepath.Join(home, tool, "endpoints.d"),
	}
	for _, d := range strings.Split(os.Getenv(envName(tool, "ENDPOINTS")), ":") {
		if d != "" {
			dirs = append(dirs, d)
		}
	}
	return dirs
}

// Call runs one named endpoint for a high-level command.
func (r *Run) Call(endpoint string, params map[string]string) (*Result, error) {
	reg, err := r.Registry()
	if err != nil {
		return nil, err
	}
	ep, ok := reg.Get(endpoint)
	if !ok {
		return nil, fmt.Errorf("no endpoint %q", endpoint)
	}
	c, err := r.Client()
	if err != nil {
		return nil, err
	}
	return c.Call(r.Ctx, ep, params, 1)
}

// ---- standard commands ----

func cmdCallFlags(r *Run) {
	r.FS.Var(&r.params, "param", "endpoint parameter as key=value (repeatable)")
	r.FS.IntVar(&r.pages, "pages", 1, "follow the endpoint's cursor for up to this many pages")
}

func cmdCall(r *Run) (any, error) {
	if len(r.Args) != 1 {
		return nil, Usage("call needs exactly one endpoint name")
	}
	reg, err := r.Registry()
	if err != nil {
		return nil, err
	}
	ep, ok := reg.Get(r.Args[0])
	if !ok {
		return nil, fmt.Errorf("no endpoint %q. Run `%s endpoints` to list them", r.Args[0], r.Svc.Name)
	}
	params := map[string]string{}
	for _, kv := range r.params {
		k, v, ok := strings.Cut(kv, "=")
		if !ok || k == "" {
			return nil, Usage("--param needs key=value, got %q", kv)
		}
		if _, dup := params[k]; dup {
			return nil, Usage("--param %s is given twice", k)
		}
		params[k] = v
	}
	c, err := r.Client()
	if err != nil {
		return nil, err
	}
	res, err := c.Call(r.Ctx, ep, params, r.pages)
	if err != nil {
		return nil, err
	}
	return res.Data, nil
}

func cmdEndpoints(r *Run) (any, error) {
	reg, err := r.Registry()
	if err != nil {
		return nil, err
	}
	describe := func(e *Endpoint) map[string]any {
		ps := make([]map[string]any, 0, len(e.Params))
		for _, p := range e.Params {
			m := map[string]any{"name": p.Name, "type": p.Type, "in": p.In, "required": p.Required, "description": p.Description}
			if p.Default != "" {
				m["default"] = p.Default
			}
			if len(p.Enum) > 0 {
				m["enum"] = p.Enum
			}
			ps = append(ps, m)
		}
		return map[string]any{"name": e.Name, "method": e.Method, "path": e.Path, "safety": e.Safety,
			"description": e.Description, "source": e.Source, "paginates": e.Paginate != nil, "params": ps}
	}
	if len(r.Args) == 1 {
		e, ok := reg.Get(r.Args[0])
		if !ok {
			return nil, fmt.Errorf("no endpoint %q", r.Args[0])
		}
		return describe(e), nil
	}
	if len(r.Args) > 1 {
		return nil, Usage("endpoints takes at most one name")
	}
	var out []map[string]any
	for _, n := range reg.Names() {
		e, _ := reg.Get(n)
		d := describe(e)
		delete(d, "params")
		out = append(out, d)
	}
	return out, nil
}

func cmdCheck(r *Run) (any, error) {
	if r.Svc.Check == nil {
		return nil, fmt.Errorf("%s has no credential check", r.Svc.Name)
	}
	if _, err := r.Client(); err != nil {
		return nil, err
	}
	out, err := r.Svc.Check(r.Ctx, r)
	if err != nil {
		return nil, err
	}
	return map[string]any{"ok": true, "account": r.acct, "detail": out}, nil
}

func cmdAccounts(r *Run) (any, error) {
	cfg, err := LoadConfig(r.Svc.Name, r.config)
	if err != nil {
		return nil, err
	}
	return map[string]any{"default": cfg.Default, "accounts": cfg.AccountNames(), "config": cfg.Path}, nil
}

func cmdInit(r *Run) (any, error) {
	// With --config the file is the tool's own. Otherwise the settings go into the
	// shared agent-tools file, beside any other tool's table.
	if r.config != "" {
		if err := WriteStarter(r.config, r.Svc.Starter); err != nil {
			return nil, err
		}
		return map[string]any{"wrote": r.config, "next": "add your credentials, then run `" + r.Svc.Name + " check`"}, nil
	}
	dest := UserConfigPath(ConfigHome())
	if err := InitShared(r.Svc.Name, r.Svc.Starter, dest); err != nil {
		return nil, err
	}
	return map[string]any{
		"wrote": dest,
		"table": "[" + r.Svc.Name + "]",
		"next":  "add your credentials, then run `" + r.Svc.Name + " check`",
	}, nil
}

func cmdWhere(r *Run) (any, error) {
	loc := NewLocator(r.Svc.Name)
	dirs := endpointDirs(r.Svc.Name)
	out := map[string]any{
		"searched":      loc.Searched(r.Svc.Name),
		"expected":      UserConfigPath(loc.ConfigHome),
		"endpoint_dirs": dirs,
	}
	cfg, err := loc.Load(r.Svc.Name, r.config)
	if err != nil {
		out["config"], out["error"] = nil, err.Error()
		return out, nil
	}
	out["config"], out["unified"], out["dir"] = cfg.Path, cfg.Unified, cfg.Dir()
	if cfg.Unified {
		out["table"] = "[" + r.Svc.Name + "]"
	}
	return out, nil
}

// Timeout is a helper for commands that need a bounded context.
func (r *Run) Timeout(d time.Duration) (context.Context, context.CancelFunc) {
	return context.WithTimeout(r.Ctx, d)
}
