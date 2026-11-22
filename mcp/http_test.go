package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

const goodToken = "0123456789abcdef0123456789abcdef0123"

type headerTransport struct{ token string }

func (h headerTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	r = r.Clone(r.Context())
	if h.token != "" {
		r.Header.Set("Authorization", "Bearer "+h.token)
	}
	return http.DefaultTransport.RoundTrip(r)
}

func httpServer(t *testing.T) *httptest.Server {
	t.Helper()
	ts, err := parseManifest("test", []byte(sample))
	if err != nil {
		t.Fatal(err)
	}
	server, _ := newServer(ts, safetyRank[SafetyWrite])
	// httptest listens on 127.0.0.1 with a 127.0.0.1 Host, so the default
	// localhost protection is satisfied.
	hs := httptest.NewServer(httpHandler(server, httpConfig{Token: goodToken}))
	t.Cleanup(hs.Close)
	return hs
}

func TestHTTPAuth(t *testing.T) {
	hs := httpServer(t)
	for name, auth := range map[string]string{
		"none":         "",
		"wrong":        "Bearer nope",
		"prefix only":  "Bearer " + goodToken[:20],
		"basic scheme": "Basic " + goodToken,
		"suffix added": "Bearer " + goodToken + "x",
	} {
		req, _ := http.NewRequest("POST", hs.URL+"/mcp", strings.NewReader("{}"))
		if auth != "" {
			req.Header.Set("Authorization", auth)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != 401 {
			t.Errorf("%s: got %d, want 401", name, resp.StatusCode)
		}
	}
	resp, err := http.Get(hs.URL + "/healthz")
	if err != nil || resp.StatusCode != 200 {
		t.Errorf("healthz should be open: %v %v", resp, err)
	}
}

func TestHTTPEndToEnd(t *testing.T) {
	fakeCLI(t)
	work := t.TempDir()
	t.Chdir(work)
	hs := httpServer(t)

	tr := &mcp.StreamableClientTransport{
		Endpoint:   hs.URL + "/mcp",
		HTTPClient: &http.Client{Transport: headerTransport{goodToken}},
	}
	sess, err := mcp.NewClient(&mcp.Implementation{Name: "t"}, nil).Connect(context.Background(), tr, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer sess.Close()
	out, isErr := call(t, sess, "demo", map[string]any{"file": "a.txt"})
	real, _ := filepath.EvalSymlinks(work)
	if isErr || !strings.Contains(out, "PWD="+real) || !strings.Contains(out, "ARG=a.txt") {
		t.Fatalf("got %v %q", isErr, out)
	}

	bad := &mcp.StreamableClientTransport{
		Endpoint:   hs.URL + "/mcp",
		HTTPClient: &http.Client{Transport: headerTransport{"wrong-token"}},
	}
	if _, err := mcp.NewClient(&mcp.Implementation{Name: "t"}, nil).Connect(context.Background(), bad, nil); err == nil {
		t.Fatal("a client with the wrong token must not connect")
	}
}

func TestHTTPConfigValidation(t *testing.T) {
	ok := []httpConfig{
		{Addr: "127.0.0.1:8808"},
		{Addr: "localhost:8808"},
		{Addr: "[::1]:8808"},
		{Addr: "0.0.0.0:8808", CertFile: "c", KeyFile: "k"},
		{Addr: "0.0.0.0:8808", Insecure: true},
	}
	for _, c := range ok {
		if err := c.validate(); err != nil {
			t.Errorf("%+v: unexpected %v", c, err)
		}
	}
	bad := []httpConfig{
		{Addr: "0.0.0.0:8808"},
		{Addr: "192.168.1.5:8808"},
		{Addr: "127.0.0.1:8808", CertFile: "c"},
		{Addr: "nonsense"},
		{Addr: ""},
	}
	for _, c := range bad {
		if err := c.validate(); err == nil {
			t.Errorf("%+v: expected an error", c)
		}
	}
}

func TestTokenFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "token")
	if err := genToken(path); err != nil {
		t.Fatal(err)
	}
	if err := genToken(path); err == nil {
		t.Error("genToken must not overwrite an existing file")
	}
	if st, _ := os.Stat(path); st.Mode().Perm() != 0o600 {
		t.Errorf("mode %o, want 600", st.Mode().Perm())
	}
	tok, err := loadToken(path)
	if err != nil || len(tok) < minTokenLen {
		t.Fatalf("%q %v", tok, err)
	}
	os.Chmod(path, 0o644)
	if _, err := loadToken(path); err == nil || !strings.Contains(err.Error(), "readable by other users") {
		t.Errorf("a world-readable token file must be refused, got %v", err)
	}
	short := filepath.Join(dir, "short")
	os.WriteFile(short, []byte("abc\n"), 0o600)
	if _, err := loadToken(short); err == nil {
		t.Error("a short token must be refused")
	}
	t.Setenv("AGENT_TOOLS_MCP_TOKEN", "")
	if _, err := loadToken(""); err == nil {
		t.Error("no token at all must be refused")
	}
}
