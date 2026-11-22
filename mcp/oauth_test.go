package main

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

const ownerSecret = "owner-secret-0123456789abcdef0123456789abcdef"

type oauthEnv struct {
	t      *testing.T
	hs     *httptest.Server
	oa     *oauthServer
	client *http.Client // never follows redirects
	state  string
}

func newOAuthEnv(t *testing.T) *oauthEnv {
	t.Helper()
	hs := httptest.NewUnstartedServer(nil)
	state := filepath.Join(t.TempDir(), "state", "oauth.json")
	oa, err := newOAuthServer("http://"+hs.Listener.Addr().String(), state, ownerSecret, false)
	if err != nil {
		t.Fatal(err)
	}
	ts, _ := parseManifest("test", []byte(sample))
	server, _ := newServer(ts, safetyRank[SafetyWrite])
	hs.Config.Handler = httpHandler(server, httpConfig{Token: goodToken, OAuth: oa})
	hs.Start()
	t.Cleanup(hs.Close)
	return &oauthEnv{t: t, hs: hs, oa: oa, state: state, client: &http.Client{
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}}
}

func (e *oauthEnv) do(method, path string, body string, hdr map[string]string) *http.Response {
	e.t.Helper()
	req, _ := http.NewRequest(method, e.hs.URL+path, strings.NewReader(body))
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	resp, err := e.client.Do(req)
	if err != nil {
		e.t.Fatal(err)
	}
	return resp
}

func readJSON(t *testing.T, resp *http.Response) map[string]any {
	t.Helper()
	defer resp.Body.Close()
	var m map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&m); err != nil {
		t.Fatalf("status %d: %v", resp.StatusCode, err)
	}
	return m
}

func (e *oauthEnv) register(method string) (id, secret string) {
	e.t.Helper()
	body := `{"client_name":"Test <b>App</b>","redirect_uris":["https://claude.ai/api/mcp/auth_callback"],"token_endpoint_auth_method":"` + method + `"}`
	resp := e.do("POST", "/register", body, map[string]string{"Content-Type": "application/json"})
	if resp.StatusCode != 201 {
		e.t.Fatalf("register: %d", resp.StatusCode)
	}
	m := readJSON(e.t, resp)
	s, _ := m["client_secret"].(string)
	return m["client_id"].(string), s
}

func pkce() (verifier, challenge string) {
	verifier = randomToken(32)
	sum := sha256.Sum256([]byte(verifier))
	return verifier, base64.RawURLEncoding.EncodeToString(sum[:])
}

const cb = "https://claude.ai/api/mcp/auth_callback"

func (e *oauthEnv) authorizeURL(clientID, challenge string, extra url.Values) string {
	q := url.Values{"response_type": {"code"}, "client_id": {clientID}, "redirect_uri": {cb},
		"code_challenge": {challenge}, "code_challenge_method": {"S256"}, "state": {"xyz"}}
	for k, v := range extra {
		q[k] = v
	}
	return "/authorize?" + q.Encode()
}

var reqRe = regexp.MustCompile(`name="req" value="([^"]+)"`)

// consent loads the consent page and posts the secret, returning the redirect.
func (e *oauthEnv) consent(authPath, secret, action string) *http.Response {
	e.t.Helper()
	page := e.do("GET", authPath, "", nil)
	if page.StatusCode != 200 {
		return page
	}
	b := new(strings.Builder)
	buf := make([]byte, 4096)
	for {
		n, err := page.Body.Read(buf)
		b.Write(buf[:n])
		if err != nil {
			break
		}
	}
	page.Body.Close()
	m := reqRe.FindStringSubmatch(b.String())
	if m == nil {
		e.t.Fatalf("no req id in consent page:\n%s", b.String())
	}
	if strings.Contains(b.String(), "<b>App</b>") {
		e.t.Error("the client name must be HTML-escaped on the consent page")
	}
	form := url.Values{"req": {m[1]}, "secret": {secret}, "action": {action}}
	return e.do("POST", "/authorize", form.Encode(), map[string]string{"Content-Type": "application/x-www-form-urlencoded"})
}

func (e *oauthEnv) token(form url.Values, hdr map[string]string) *http.Response {
	if hdr == nil {
		hdr = map[string]string{}
	}
	hdr["Content-Type"] = "application/x-www-form-urlencoded"
	return e.do("POST", "/token", form.Encode(), hdr)
}

func codeFrom(t *testing.T, resp *http.Response) (code, state string) {
	t.Helper()
	if resp.StatusCode != 302 {
		t.Fatalf("want a redirect, got %d", resp.StatusCode)
	}
	u, _ := url.Parse(resp.Header.Get("Location"))
	if u.Query().Get("error") != "" {
		t.Fatalf("redirected with error %s", u.Query().Get("error"))
	}
	return u.Query().Get("code"), u.Query().Get("state")
}

func TestOAuthFullFlow(t *testing.T) {
	fakeCLI(t)
	e := newOAuthEnv(t)

	// discovery
	resp := e.do("GET", "/.well-known/oauth-protected-resource", "", nil)
	prm := readJSON(t, resp)
	if prm["resource"] != e.oa.resource {
		t.Errorf("prm resource %v", prm["resource"])
	}
	as := readJSON(t, e.do("GET", "/.well-known/oauth-authorization-server", "", nil))
	if as["registration_endpoint"] == nil || as["code_challenge_methods_supported"].([]any)[0] != "S256" {
		t.Errorf("bad AS metadata: %v", as)
	}

	// an unauthenticated MCP request is told where to authenticate
	r := e.do("POST", "/mcp", "{}", nil)
	if r.StatusCode != 401 || !strings.Contains(r.Header.Get("WWW-Authenticate"), `resource_metadata="`+e.oa.prmURL()+`"`) {
		t.Errorf("401 challenge: %d %q", r.StatusCode, r.Header.Get("WWW-Authenticate"))
	}

	id, _ := e.register("none")
	verifier, challenge := pkce()
	path := e.authorizeURL(id, challenge, url.Values{"resource": {e.oa.resource}})

	// wrong secret: no code
	if bad := e.consent(path, "wrong", "approve"); bad.StatusCode != 403 {
		t.Fatalf("wrong secret: got %d", bad.StatusCode)
	}
	code, state := codeFrom(t, e.consent(path, ownerSecret, "approve"))
	if state != "xyz" || code == "" {
		t.Fatalf("code %q state %q", code, state)
	}

	// wrong PKCE verifier burns the code
	badV, _ := pkce()
	form := url.Values{"grant_type": {"authorization_code"}, "client_id": {id}, "code": {code}, "redirect_uri": {cb}, "code_verifier": {badV}}
	if r := e.token(form, nil); r.StatusCode != 400 {
		t.Errorf("bad verifier: %d", r.StatusCode)
	}
	form.Set("code_verifier", verifier)
	if r := e.token(form, nil); r.StatusCode != 400 {
		t.Errorf("a code must work only once, even after a failed attempt: %d", r.StatusCode)
	}

	// the real exchange
	code, _ = codeFrom(t, e.consent(path, ownerSecret, "approve"))
	form.Set("code", code)
	tok := readJSON(t, e.token(form, nil))
	access, refresh := tok["access_token"].(string), tok["refresh_token"].(string)
	if access == "" || refresh == "" || tok["token_type"] != "Bearer" {
		t.Fatalf("tokens: %v", tok)
	}

	// the access token reaches the tools
	sess, err := mcp.NewClient(&mcp.Implementation{Name: "t"}, nil).Connect(context.Background(),
		&mcp.StreamableClientTransport{Endpoint: e.hs.URL + "/mcp", HTTPClient: &http.Client{Transport: headerTransport{access}}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if out, isErr := call(t, sess, "demo", map[string]any{"file": "a"}); isErr || !strings.Contains(out, "ARG=a") {
		t.Errorf("call via OAuth token: %v %q", isErr, out)
	}
	sess.Close()

	// refresh rotates; the old token then trips reuse detection and kills the family
	rf := url.Values{"grant_type": {"refresh_token"}, "client_id": {id}, "refresh_token": {refresh}}
	tok2 := readJSON(t, e.token(rf, nil))
	access2, refresh2 := tok2["access_token"].(string), tok2["refresh_token"].(string)
	if refresh2 == refresh || access2 == access {
		t.Error("refresh must issue new tokens")
	}
	if r := e.token(rf, nil); r.StatusCode != 400 {
		t.Errorf("reusing a rotated refresh token: %d", r.StatusCode)
	}
	for _, tk := range []string{access, access2} {
		if r := e.do("POST", "/mcp", "{}", map[string]string{"Authorization": "Bearer " + tk}); r.StatusCode != 401 {
			t.Errorf("after reuse the whole family must be revoked, got %d", r.StatusCode)
		}
	}
	rf.Set("refresh_token", refresh2)
	if r := e.token(rf, nil); r.StatusCode != 400 {
		t.Errorf("the family's newest refresh token must be dead too: %d", r.StatusCode)
	}
}

func TestOAuthRejections(t *testing.T) {
	e := newOAuthEnv(t)
	id, _ := e.register("none")
	_, challenge := pkce()

	if r := e.do("GET", e.authorizeURL("nope", challenge, nil), "", nil); r.StatusCode != 400 {
		t.Errorf("unknown client: %d", r.StatusCode)
	}
	bad := e.authorizeURL(id, challenge, url.Values{"redirect_uri": {"https://evil.example/cb"}})
	if r := e.do("GET", bad, "", nil); r.StatusCode != 400 || r.Header.Get("Location") != "" {
		t.Errorf("an unregistered redirect URI must be an error page, never a redirect: %d %q", r.StatusCode, r.Header.Get("Location"))
	}
	for name, extra := range map[string]url.Values{
		"plain pkce":     {"code_challenge_method": {"plain"}},
		"no challenge":   {"code_challenge": {""}},
		"wrong resource": {"resource": {"https://other.example/mcp"}},
		"token response": {"response_type": {"token"}},
	} {
		r := e.do("GET", e.authorizeURL(id, challenge, extra), "", nil)
		loc, _ := url.Parse(r.Header.Get("Location"))
		if r.StatusCode != 302 || loc.Query().Get("error") == "" {
			t.Errorf("%s: want an error redirect, got %d %q", name, r.StatusCode, r.Header.Get("Location"))
		}
	}
	// deny comes back as access_denied
	r := e.consent(e.authorizeURL(id, challenge, nil), "", "deny")
	if loc, _ := url.Parse(r.Header.Get("Location")); loc == nil || loc.Query().Get("error") != "access_denied" {
		t.Errorf("deny: %d %q", r.StatusCode, r.Header.Get("Location"))
	}
	// five wrong secrets lock that address out, even for the right secret
	path := e.authorizeURL(id, challenge, nil)
	for i := 0; i < 5; i++ {
		e.consent(path, "wrong", "approve")
	}
	if r := e.consent(path, ownerSecret, "approve"); r.StatusCode != 429 {
		t.Errorf("lockout: %d", r.StatusCode)
	}
}

func TestOAuthRegistrationValidation(t *testing.T) {
	e := newOAuthEnv(t)
	for name, body := range map[string]string{
		"no redirects":  `{"client_name":"x"}`,
		"http redirect": `{"redirect_uris":["http://evil.example/cb"]}`,
		"javascript":    `{"redirect_uris":["javascript:alert(1)"]}`,
		"fragment":      `{"redirect_uris":["https://a.example/cb#x"]}`,
		"userinfo":      `{"redirect_uris":["https://u:p@a.example/cb"]}`,
		"bad method":    `{"redirect_uris":["https://a.example/cb"],"token_endpoint_auth_method":"private_key_jwt"}`,
		"bad grant":     `{"redirect_uris":["https://a.example/cb"],"grant_types":["password"]}`,
		"not json":      `nope`,
	} {
		if r := e.do("POST", "/register", body, nil); r.StatusCode != 400 {
			t.Errorf("%s: got %d", name, r.StatusCode)
		}
	}
	for _, ok := range []string{"http://127.0.0.1:5555/cb", "http://localhost/cb", "https://chatgpt.com/connector_platform_oauth_redirect"} {
		if r := e.do("POST", "/register", `{"redirect_uris":["`+ok+`"]}`, nil); r.StatusCode != 201 {
			t.Errorf("%s: got %d", ok, r.StatusCode)
		}
	}
}

func TestOAuthConfidentialClient(t *testing.T) {
	e := newOAuthEnv(t)
	id, secret := e.register("client_secret_basic")
	if secret == "" {
		t.Fatal("a confidential client must get a secret")
	}
	verifier, challenge := pkce()
	code, _ := codeFrom(t, e.consent(e.authorizeURL(id, challenge, nil), ownerSecret, "approve"))
	form := url.Values{"grant_type": {"authorization_code"}, "code": {code}, "redirect_uri": {cb}, "code_verifier": {verifier}}

	if r := e.token(form, map[string]string{"Authorization": "Basic " + base64.StdEncoding.EncodeToString([]byte(id+":wrong"))}); r.StatusCode != 401 {
		t.Errorf("wrong secret: %d", r.StatusCode)
	}
	// the failed attempt did not reach the code check, so the code is still good
	ok := e.token(form, map[string]string{"Authorization": "Basic " + base64.StdEncoding.EncodeToString([]byte(id+":"+secret))})
	if ok.StatusCode != 200 {
		t.Errorf("right secret: %d", ok.StatusCode)
	}
}

func TestOAuthStorePersists(t *testing.T) {
	e := newOAuthEnv(t)
	id, _ := e.register("none")
	verifier, challenge := pkce()
	code, _ := codeFrom(t, e.consent(e.authorizeURL(id, challenge, nil), ownerSecret, "approve"))
	tok := readJSON(t, e.token(url.Values{"grant_type": {"authorization_code"}, "client_id": {id}, "code": {code},
		"redirect_uri": {cb}, "code_verifier": {verifier}}, nil))
	access := tok["access_token"].(string)

	st, err := os.Stat(e.state)
	if err != nil || st.Mode().Perm() != 0o600 {
		t.Fatalf("state file: %v %v", st, err)
	}
	raw, _ := os.ReadFile(e.state)
	if strings.Contains(string(raw), access) || strings.Contains(string(raw), tok["refresh_token"].(string)) {
		t.Error("the state file must hold token hashes, never tokens")
	}
	reopened, err := openOAuthStore(e.state)
	if err != nil {
		t.Fatal(err)
	}
	if !reopened.validAccess(access, e.oa.resource) {
		t.Error("an access token must survive a restart")
	}
	if reopened.validAccess(access, "https://other.example/mcp") {
		t.Error("a token must not be valid for another resource")
	}
}

func TestOAuthPublicURLValidation(t *testing.T) {
	for _, bad := range []string{"http://mcp.example.com", "mcp.example.com", "https://mcp.example.com/path", "https://u@mcp.example.com", "ftp://x.example"} {
		if _, err := newOAuthServer(bad, "", ownerSecret, false); err == nil {
			t.Errorf("%q should be refused", bad)
		}
	}
	for _, good := range []string{"https://mcp.example.com", "https://mcp.example.com/", "http://127.0.0.1:8808"} {
		if _, err := newOAuthServer(good, "", ownerSecret, false); err != nil {
			t.Errorf("%q: %v", good, err)
		}
	}
}

func TestOAuthStaticTokenStillWorks(t *testing.T) {
	e := newOAuthEnv(t)
	if r := e.do("POST", "/mcp", "{}", map[string]string{"Authorization": "Bearer " + goodToken}); r.StatusCode == 401 {
		t.Error("the static token must keep working next to OAuth")
	}
	if r := e.do("POST", "/mcp", "{}", map[string]string{"Authorization": "Bearer made-up"}); r.StatusCode != 401 ||
		!strings.Contains(r.Header.Get("WWW-Authenticate"), "invalid_token") {
		t.Errorf("a bad token: %d %q", r.StatusCode, r.Header.Get("WWW-Authenticate"))
	}
}
