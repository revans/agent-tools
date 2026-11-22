package main

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

const minTokenLen = 32

type httpConfig struct {
	Addr        string
	Token       string
	CertFile    string
	KeyFile     string
	BehindProxy bool // a reverse proxy or tunnel on this host terminates TLS and forwards a public Host header
	Insecure    bool // allow plain HTTP on a non-loopback address
}

// loadToken reads the bearer token from a file (mode must not allow group or
// other access) or, failing that, from AGENT_TOOLS_MCP_TOKEN. It is never
// taken from a flag value, which would show up in `ps`.
func loadToken(file string) (string, error) {
	var tok string
	if file != "" {
		st, err := os.Stat(file)
		if err != nil {
			return "", err
		}
		if st.Mode().Perm()&0o077 != 0 {
			return "", fmt.Errorf("%s is readable by other users (mode %o); run chmod 600", file, st.Mode().Perm())
		}
		b, err := os.ReadFile(file)
		if err != nil {
			return "", err
		}
		tok = strings.TrimSpace(string(b))
	} else {
		tok = strings.TrimSpace(os.Getenv("AGENT_TOOLS_MCP_TOKEN"))
	}
	if tok == "" {
		return "", errors.New("an HTTP server needs a bearer token: use -token-file, or set AGENT_TOOLS_MCP_TOKEN (make one with -gen-token FILE)")
	}
	if len(tok) < minTokenLen {
		return "", fmt.Errorf("the token is %d characters; use at least %d (make one with -gen-token FILE)", len(tok), minTokenLen)
	}
	return tok, nil
}

// genToken writes a new random token to path with mode 0600 and refuses to
// overwrite an existing file. The token is never printed.
func genToken(path string) error {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return err
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	if _, err := f.WriteString(hex.EncodeToString(b) + "\n"); err != nil {
		f.Close()
		return err
	}
	return f.Close()
}

// isLoopback reports whether addr, with or without a port, is a loopback host.
func isLoopback(addr string) bool {
	host := addr
	if h, _, err := net.SplitHostPort(addr); err == nil {
		host = h
	}
	host = strings.Trim(host, "[]")
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

func (c httpConfig) validate() error {
	tls := c.CertFile != "" || c.KeyFile != ""
	if tls && (c.CertFile == "" || c.KeyFile == "") {
		return errors.New("-tls-cert and -tls-key go together")
	}
	if c.Addr == "" {
		return errors.New("-http needs an address such as 127.0.0.1:8808")
	}
	if _, _, err := net.SplitHostPort(c.Addr); err != nil {
		return fmt.Errorf("-http %q: %v", c.Addr, err)
	}
	if !isLoopback(c.Addr) && !tls && !c.Insecure {
		return fmt.Errorf("%s is not a loopback address and there is no TLS: the bearer token would cross the network in clear text.\n"+
			"Use -tls-cert and -tls-key, bind 127.0.0.1 behind a TLS-terminating proxy or tunnel, or pass -insecure-http on a network you trust", c.Addr)
	}
	return nil
}

// bearer rejects every request that does not carry the exact token. Both sides
// are hashed first so the comparison takes the same time whatever the input.
func bearer(token string, next http.Handler) http.Handler {
	want := sha256.Sum256([]byte(token))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
		sum := sha256.Sum256([]byte(strings.TrimSpace(got)))
		if !ok || subtle.ConstantTimeCompare(sum[:], want[:]) != 1 {
			logf("401 %s %s from %s", r.Method, r.URL.Path, r.RemoteAddr)
			w.Header().Set("WWW-Authenticate", `Bearer realm="agent-tools-mcp"`)
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// httpHandler serves the MCP endpoint at /mcp behind the bearer check, and an
// unauthenticated /healthz that reveals nothing. The server is stateless: each
// request stands alone, so a restart or a second instance loses nothing.
func httpHandler(server *mcp.Server, c httpConfig) http.Handler {
	mcpHandler := mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return server }, &mcp.StreamableHTTPOptions{
		Stateless:                  true,
		JSONResponse:               true,
		DisableLocalhostProtection: c.BehindProxy,
	})
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		fmt.Fprintln(w, "ok")
	})
	mux.Handle("/mcp", bearer(c.Token, mcpHandler))
	return mux
}

func serveHTTP(server *mcp.Server, c httpConfig) error {
	if err := c.validate(); err != nil {
		return err
	}
	srv := &http.Server{
		Addr:              c.Addr,
		Handler:           httpHandler(server, c),
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       2 * time.Minute,
		// No WriteTimeout: a tool call may legitimately run for minutes.
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	errc := make(chan error, 1)
	go func() {
		if c.CertFile != "" {
			errc <- srv.ListenAndServeTLS(c.CertFile, c.KeyFile)
		} else {
			errc <- srv.ListenAndServe()
		}
	}()
	scheme := "http"
	if c.CertFile != "" {
		scheme = "https"
	}
	logf("listening on %s://%s/mcp", scheme, c.Addr)
	select {
	case err := <-errc:
		return err
	case <-ctx.Done():
		shutdown, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		return srv.Shutdown(shutdown)
	}
}
