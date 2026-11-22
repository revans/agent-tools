// agent-tools-mcp serves the agent-tools CLIs (and any CLI you describe in a
// TOML manifest) to MCP clients over stdio.
//
// It never changes directory: every CLI runs in the directory the client
// launched this server from, so per-project files such as .wharf or
// notif-slack.toml are found the same way they are from a shell there.
package main

import (
	"bytes"
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

const maxOutput = 256 * 1024

var version = "0.1.0"

func main() {
	var (
		allow    = flag.String("allow", envOr("AGENT_TOOLS_MCP_ALLOW", SafetyWrite), "highest safety level to register: read, write or destructive")
		extra    = flag.String("tools", os.Getenv("AGENT_TOOLS_MCP_TOOLS"), "extra directories of *.toml tool manifests, separated by ':'")
		list     = flag.Bool("list", false, "print the tools that would be registered and exit")
		showVers = flag.Bool("version", false, "print the version and exit")

		httpAddr  = flag.String("http", "", "serve MCP over HTTP at this address (e.g. 127.0.0.1:8808) instead of stdio")
		tokenFile = flag.String("token-file", "", "file holding the bearer token for -http (mode 0600); else $AGENT_TOOLS_MCP_TOKEN")
		cwd       = flag.String("cwd", "", "with -http: the directory every CLI runs in (default: the current directory)")
		tlsCert   = flag.String("tls-cert", "", "with -http: TLS certificate file")
		tlsKey    = flag.String("tls-key", "", "with -http: TLS key file")
		behind    = flag.Bool("behind-proxy", false, "with -http: a proxy or tunnel on this host terminates TLS and forwards its public Host header")
		insecure  = flag.Bool("insecure-http", false, "with -http: allow plain HTTP on a non-loopback address")
		genTok    = flag.String("gen-token", "", "write a new random bearer token to this file (mode 0600) and exit")
	)
	flag.Parse()
	if *genTok != "" {
		if err := genToken(*genTok); err != nil {
			fatal("%v", err)
		}
		fmt.Printf("wrote a new token to %s\n", *genTok)
		return
	}
	if *showVers {
		fmt.Println(version)
		return
	}
	limit, ok := safetyRank[*allow]
	if !ok {
		fatal("-allow must be read, write or destructive, got %q", *allow)
	}

	dirs := []string{}
	if home, err := os.UserHomeDir(); err == nil {
		dirs = append(dirs, filepath.Join(home, ".config", "agent-tools-mcp", "tools.d"))
	}
	for _, d := range strings.Split(*extra, ":") {
		if d != "" {
			dirs = append(dirs, d)
		}
	}
	tools, err := loadTools(dirs)
	if err != nil {
		fatal("%v", err)
	}

	if *list {
		for _, t := range tools {
			if safetyRank[t.Safety] <= limit {
				fmt.Printf("%-24s %-11s %s\n", t.Name, t.Safety, t.Bin)
			}
		}
		return
	}
	server, registered := newServer(tools, limit)
	if *httpAddr != "" {
		cfg := httpConfig{Addr: *httpAddr, CertFile: *tlsCert, KeyFile: *tlsKey, BehindProxy: *behind, Insecure: *insecure}
		if err := cfg.validate(); err != nil {
			fatal("%v", err)
		}
		if cfg.Token, err = loadToken(*tokenFile); err != nil {
			fatal("%v", err)
		}
		// Over HTTP no client launched us, so there is no cwd to inherit:
		// every CLI runs in one configured directory.
		if *cwd != "" {
			if err := os.Chdir(*cwd); err != nil {
				fatal("-cwd: %v", err)
			}
		}
		logf("serving %d tools, CLIs run in %s", registered, mustGetwd())
		if err := serveHTTP(server, cfg); err != nil {
			fatal("%v", err)
		}
		return
	}
	logf("serving %d tools from %s", registered, mustGetwd())
	if err := server.Run(context.Background(), &mcp.StdioTransport{}); err != nil {
		fatal("%v", err)
	}
}

// newServer registers every tool at or below the safety limit.
func newServer(tools []Tool, limit int) (*mcp.Server, int) {
	server := mcp.NewServer(&mcp.Implementation{Name: "agent-tools", Version: version}, nil)
	n := 0
	for i := range tools {
		t := tools[i]
		if safetyRank[t.Safety] > limit {
			logf("skipped %s (%s is above the allowed level)", t.Name, t.Safety)
			continue
		}
		server.AddTool(&mcp.Tool{
			Name:        t.Name,
			Description: t.Description,
			InputSchema: t.inputSchema(),
			Annotations: annotations(&t),
		}, handler(&t))
		n++
	}
	return server, n
}

func annotations(t *Tool) *mcp.ToolAnnotations {
	a := &mcp.ToolAnnotations{OpenWorldHint: boolPtr(true)}
	switch t.Safety {
	case SafetyRead:
		a.ReadOnlyHint = true
	case SafetyWrite:
		a.DestructiveHint = boolPtr(false)
	default:
		a.DestructiveHint = boolPtr(true)
	}
	return a
}

func handler(t *Tool) mcp.ToolHandler {
	return func(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		argv, err := t.buildArgs(req.Params.Arguments)
		if err != nil {
			return toolError("invalid arguments: %v", err), nil
		}
		return run(ctx, t, argv), nil
	}
}

// run executes the CLI with no shell, no stdin and the server's own working
// directory and environment. A CLI that tries to prompt therefore fails fast
// instead of hanging.
func run(ctx context.Context, t *Tool, argv []string) *mcp.CallToolResult {
	bin, err := resolveBin(t.Bin)
	if err != nil {
		return toolError("%v", err)
	}
	ctx, cancel := context.WithTimeout(ctx, t.timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, bin, argv...)
	cmd.WaitDelay = 2 * time.Second
	var stdout, stderr limitedBuffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	err = cmd.Run()

	out := strings.TrimRight(stdout.String(), "\n")
	errText := strings.TrimRight(stderr.String(), "\n")
	if ctx.Err() == context.DeadlineExceeded {
		return toolError("%s timed out after %s\n%s\n%s", t.Bin, t.timeout, out, errText)
	}
	code := 0
	if err != nil {
		var ee *exec.ExitError
		if !errors.As(err, &ee) {
			return toolError("running %s: %v", t.Bin, err)
		}
		code = ee.ExitCode()
	}
	good := false
	for _, c := range t.OKExit {
		if c == code {
			good = true
		}
	}
	text := out
	if errText != "" && (!good || out == "") {
		text = strings.TrimSpace(out + "\n" + errText)
	}
	if !good {
		return toolError("%s exited with code %d\n%s", t.Bin, code, text)
	}
	if text == "" {
		text = "(ok, no output)"
	}
	if code != 0 {
		text = fmt.Sprintf("(exit code %d, expected for this command)\n%s", code, text)
	}
	return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: text}}}
}

// resolveBin finds the program on PATH, then next to this executable, so a
// directory holding all the binaries works without touching PATH.
func resolveBin(name string) (string, error) {
	if p, err := exec.LookPath(name); err == nil {
		return p, nil
	}
	if self, err := os.Executable(); err == nil {
		p := filepath.Join(filepath.Dir(self), name)
		if st, err := os.Stat(p); err == nil && !st.IsDir() {
			return p, nil
		}
	}
	return "", fmt.Errorf("%s is not installed: it is not on PATH or next to agent-tools-mcp", name)
}

// limitedBuffer keeps the first maxOutput bytes and notes what it dropped.
type limitedBuffer struct {
	buf     bytes.Buffer
	dropped int
}

func (b *limitedBuffer) Write(p []byte) (int, error) {
	room := maxOutput - b.buf.Len()
	if room > len(p) {
		room = len(p)
	}
	if room > 0 {
		b.buf.Write(p[:room])
	}
	b.dropped += len(p) - room
	return len(p), nil
}

func (b *limitedBuffer) String() string {
	if b.dropped > 0 {
		return fmt.Sprintf("%s\n[output truncated: %d more bytes]", b.buf.String(), b.dropped)
	}
	return b.buf.String()
}

func toolError(format string, a ...any) *mcp.CallToolResult {
	return &mcp.CallToolResult{
		IsError: true,
		Content: []mcp.Content{&mcp.TextContent{Text: strings.TrimSpace(fmt.Sprintf(format, a...))}},
	}
}

// logf writes to stderr; stdout belongs to the MCP protocol.
func logf(format string, a ...any) { fmt.Fprintf(os.Stderr, "agent-tools-mcp: "+format+"\n", a...) }

func fatal(format string, a ...any) {
	logf(format, a...)
	os.Exit(1)
}

func mustGetwd() string {
	d, _ := os.Getwd()
	return d
}

func envOr(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

func boolPtr(b bool) *bool { return &b }
