package main

import (
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"html/template"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

// oauthServer makes this process an OAuth 2.1 authorization server for exactly
// one owner, and the MCP endpoint its protected resource. It supports what
// MCP clients use: dynamic client registration (RFC 7591), the authorization
// code flow with PKCE S256, refresh-token rotation, resource indicators
// (RFC 8707), and the discovery documents of RFC 8414 and RFC 9728.
//
// The owner approves each client on a consent page by typing the same secret
// that works as the static bearer token. There are no user accounts.
type oauthServer struct {
	issuer      string // the public base URL, no trailing slash
	resource    string // issuer + "/mcp": the audience of every token
	owner       string // the secret that approves a client
	store       *oauthStore
	behindProxy bool
	authFails   *limiter // wrong owner secrets, per client address
	regs        *limiter // client registrations, per client address
}

func newOAuthServer(publicURL, statePath, owner string, behindProxy bool) (*oauthServer, error) {
	u, err := url.Parse(publicURL)
	if err != nil || u.Host == "" {
		return nil, fmt.Errorf("-public-url %q is not a URL", publicURL)
	}
	if u.Path != "" && u.Path != "/" || u.RawQuery != "" || u.Fragment != "" || u.User != nil {
		return nil, errors.New("-public-url must be just the origin, e.g. https://mcp.example.com")
	}
	switch {
	case u.Scheme == "https":
	case u.Scheme == "http" && isLoopback(u.Host):
	default:
		return nil, errors.New("-public-url must be https (plain http is accepted only for a loopback host, for testing)")
	}
	store, err := openOAuthStore(statePath)
	if err != nil {
		return nil, err
	}
	base := u.Scheme + "://" + u.Host
	return &oauthServer{
		issuer:      base,
		resource:    base + "/mcp",
		owner:       owner,
		store:       store,
		behindProxy: behindProxy,
		authFails:   newLimiter(5, 15*time.Minute),
		regs:        newLimiter(30, time.Hour),
	}, nil
}

func (o *oauthServer) prmURL() string { return o.issuer + "/.well-known/oauth-protected-resource" }

func (o *oauthServer) register(mux *http.ServeMux) {
	mux.HandleFunc("GET /.well-known/oauth-protected-resource", o.handlePRM)
	mux.HandleFunc("GET /.well-known/oauth-protected-resource/mcp", o.handlePRM)
	mux.HandleFunc("GET /.well-known/oauth-authorization-server", o.handleASMetadata)
	mux.HandleFunc("GET /.well-known/openid-configuration", o.handleASMetadata)
	mux.HandleFunc("POST /register", o.handleRegister)
	mux.HandleFunc("GET /authorize", o.handleAuthorize)
	mux.HandleFunc("POST /authorize", o.handleConsent)
	mux.HandleFunc("POST /token", o.handleToken)
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func (o *oauthServer) handlePRM(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, 200, map[string]any{
		"resource":                 o.resource,
		"authorization_servers":    []string{o.issuer},
		"bearer_methods_supported": []string{"header"},
		"scopes_supported":         []string{"mcp"},
	})
}

func (o *oauthServer) handleASMetadata(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, 200, map[string]any{
		"issuer":                                         o.issuer,
		"authorization_endpoint":                         o.issuer + "/authorize",
		"token_endpoint":                                 o.issuer + "/token",
		"registration_endpoint":                          o.issuer + "/register",
		"response_types_supported":                       []string{"code"},
		"grant_types_supported":                          []string{"authorization_code", "refresh_token"},
		"code_challenge_methods_supported":               []string{"S256"},
		"token_endpoint_auth_methods_supported":          []string{"none", "client_secret_post", "client_secret_basic"},
		"scopes_supported":                               []string{"mcp"},
		"authorization_response_iss_parameter_supported": true,
	})
}

// ---- registration ----

type regRequest struct {
	ClientName   string   `json:"client_name"`
	RedirectURIs []string `json:"redirect_uris"`
	GrantTypes   []string `json:"grant_types"`
	ResponseType []string `json:"response_types"`
	AuthMethod   string   `json:"token_endpoint_auth_method"`
}

func oauthError(w http.ResponseWriter, status int, code, desc string) {
	writeJSON(w, status, map[string]string{"error": code, "error_description": desc})
}

// validRedirect accepts https URLs and http URLs on a loopback host (native
// apps), and nothing with credentials or a fragment.
func validRedirect(raw string) bool {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" || u.Fragment != "" || u.User != nil || len(raw) > 512 {
		return false
	}
	switch u.Scheme {
	case "https":
		return true
	case "http":
		return isLoopback(u.Host)
	}
	return false
}

func (o *oauthServer) handleRegister(w http.ResponseWriter, r *http.Request) {
	if !o.regs.allow(o.clientIP(r)) {
		oauthError(w, http.StatusTooManyRequests, "temporarily_unavailable", "too many registrations; try again later")
		return
	}
	var req regRequest
	body, err := io.ReadAll(io.LimitReader(r.Body, 16<<10))
	if err != nil || json.Unmarshal(body, &req) != nil {
		oauthError(w, 400, "invalid_client_metadata", "the body must be a JSON object")
		return
	}
	if len(req.RedirectURIs) == 0 || len(req.RedirectURIs) > 10 {
		oauthError(w, 400, "invalid_redirect_uri", "give between 1 and 10 redirect_uris")
		return
	}
	for _, u := range req.RedirectURIs {
		if !validRedirect(u) {
			oauthError(w, 400, "invalid_redirect_uri", "redirect URIs must be https, or http on a loopback host: "+u)
			return
		}
	}
	method := req.AuthMethod
	if method == "" {
		method = "client_secret_basic" // the RFC 7591 default
	}
	switch method {
	case "none", "client_secret_post", "client_secret_basic":
	default:
		oauthError(w, 400, "invalid_client_metadata", "unsupported token_endpoint_auth_method")
		return
	}
	for _, g := range req.GrantTypes {
		if g != "authorization_code" && g != "refresh_token" {
			oauthError(w, 400, "invalid_client_metadata", "unsupported grant type "+g)
			return
		}
	}
	for _, t := range req.ResponseType {
		if t != "code" {
			oauthError(w, 400, "invalid_client_metadata", "unsupported response type "+t)
			return
		}
	}
	name := strings.TrimSpace(req.ClientName)
	if name == "" {
		name = "unnamed client"
	}
	if len([]rune(name)) > 80 {
		name = string([]rune(name)[:80])
	}
	c := &oauthClient{ID: "mcp_" + randomToken(16), Name: name, Redirects: req.RedirectURIs, AuthMethod: method, Created: time.Now().UTC()}
	resp := map[string]any{
		"client_id":                  c.ID,
		"client_id_issued_at":        c.Created.Unix(),
		"client_name":                name,
		"redirect_uris":              c.Redirects,
		"token_endpoint_auth_method": method,
		"grant_types":                []string{"authorization_code", "refresh_token"},
		"response_types":             []string{"code"},
	}
	if method != "none" {
		secret := randomToken(32)
		c.SecretHash = hashOf(secret)
		resp["client_secret"] = secret
		resp["client_secret_expires_at"] = 0
	}
	if err := o.store.addClient(c); err != nil {
		if errors.Is(err, errTooManyClients) {
			oauthError(w, http.StatusServiceUnavailable, "temporarily_unavailable", "too many registered clients")
		} else {
			logf("oauth: saving a client failed: %v", err)
			oauthError(w, 500, "server_error", "could not save the client")
		}
		return
	}
	logf("oauth: registered client %q (%s)", name, c.ID)
	writeJSON(w, http.StatusCreated, resp)
}

// ---- authorize ----

var consentTmpl = template.Must(template.New("consent").Parse(`<!doctype html>
<html lang="en"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1">
<title>Authorize access</title>
<style>
 body{font:16px/1.5 system-ui,sans-serif;max-width:30rem;margin:3rem auto;padding:0 1rem;color:#1a1a1a}
 @media (prefers-color-scheme:dark){body{background:#111;color:#eee}input{background:#222;color:#eee;border:1px solid #555}}
 code{word-break:break-all} input[type=password]{width:100%;box-sizing:border-box;padding:.6rem;font-size:1rem}
 button{padding:.6rem 1.2rem;font-size:1rem;margin-right:.5rem} .err{color:#b00020}
</style></head><body>
<h1>Authorize access</h1>
{{if .Error}}<p class="err">{{.Error}}</p>{{end}}
<p><strong>{{.ClientName}}</strong> wants to use the tools on <code>{{.Issuer}}</code>.</p>
<p>It will return to <code>{{.RedirectHost}}</code>. Approve only if you started this connection.</p>
<p>Approving lets it run every tool this server offers, such as posting messages, uploading files and reading your infrastructure.</p>
<form method="post" action="/authorize">
 <input type="hidden" name="req" value="{{.Req}}">
 <p><label>Server secret<br><input type="password" name="secret" autocomplete="current-password" autofocus required></label></p>
 <button type="submit" name="action" value="approve">Approve</button>
 <button type="submit" name="action" value="deny" formnovalidate>Deny</button>
</form></body></html>`))

var errorTmpl = template.Must(template.New("err").Parse(`<!doctype html><html lang="en"><head><meta charset="utf-8">
<meta name="viewport" content="width=device-width,initial-scale=1"><title>Cannot authorize</title>
<style>body{font:16px/1.5 system-ui,sans-serif;max-width:30rem;margin:3rem auto;padding:0 1rem}@media (prefers-color-scheme:dark){body{background:#111;color:#eee}}</style></head>
<body><h1>Cannot authorize</h1><p>{{.}}</p></body></html>`))

func securePage(w http.ResponseWriter) {
	h := w.Header()
	h.Set("Content-Type", "text/html; charset=utf-8")
	h.Set("Cache-Control", "no-store")
	h.Set("X-Frame-Options", "DENY")
	h.Set("Referrer-Policy", "no-referrer")
	h.Set("Content-Security-Policy", "default-src 'none'; style-src 'unsafe-inline'; frame-ancestors 'none'; base-uri 'none'")
}

func (o *oauthServer) errorPage(w http.ResponseWriter, status int, msg string) {
	securePage(w)
	w.WriteHeader(status)
	_ = errorTmpl.Execute(w, msg)
}

// redirectBack sends the browser to the client's redirect URI with query parameters.
func redirectBack(w http.ResponseWriter, r *http.Request, redirectURI string, params map[string]string) {
	u, err := url.Parse(redirectURI)
	if err != nil {
		http.Error(w, "bad redirect", http.StatusBadRequest)
		return
	}
	q := u.Query()
	for k, v := range params {
		if v != "" {
			q.Set(k, v)
		}
	}
	u.RawQuery = q.Encode()
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Referrer-Policy", "no-referrer")
	http.Redirect(w, r, u.String(), http.StatusFound)
}

func (o *oauthServer) handleAuthorize(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	// Until the client and redirect URI check out, an error is shown here and
	// never redirected: redirecting to an unchecked URI would make an open redirect.
	c := o.store.client(q.Get("client_id"))
	if c == nil {
		o.errorPage(w, 400, "Unknown client. Remove the connection and add it again.")
		return
	}
	redirect := q.Get("redirect_uri")
	if !contains(c.Redirects, redirect) {
		o.errorPage(w, 400, "The redirect address does not match the one this client registered.")
		return
	}
	state := q.Get("state")
	fail := func(code, desc string) {
		redirectBack(w, r, redirect, map[string]string{"error": code, "error_description": desc, "state": state, "iss": o.issuer})
	}
	if q.Get("response_type") != "code" {
		fail("unsupported_response_type", "only response_type=code is supported")
		return
	}
	challenge := q.Get("code_challenge")
	if q.Get("code_challenge_method") != "S256" || len(challenge) < 43 || len(challenge) > 128 {
		fail("invalid_request", "PKCE with code_challenge_method=S256 is required")
		return
	}
	resource := q.Get("resource")
	if resource != "" && resource != o.resource {
		fail("invalid_target", "this server only issues tokens for "+o.resource)
		return
	}
	if resource == "" {
		resource = o.resource
	}
	id := o.store.addPending(&pendingAuth{ClientID: c.ID, RedirectURI: redirect, Challenge: challenge, State: state, Resource: resource})
	o.renderConsent(w, http.StatusOK, c, redirect, id, "")
}

func (o *oauthServer) renderConsent(w http.ResponseWriter, status int, c *oauthClient, redirect, req, errMsg string) {
	host := redirect
	if u, err := url.Parse(redirect); err == nil {
		host = u.Host
	}
	securePage(w)
	w.WriteHeader(status)
	_ = consentTmpl.Execute(w, map[string]string{
		"ClientName": c.Name, "Issuer": o.issuer, "RedirectHost": host, "Req": req, "Error": errMsg,
	})
}

func (o *oauthServer) handleConsent(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, 8<<10)
	if err := r.ParseForm(); err != nil {
		o.errorPage(w, 400, "The form could not be read.")
		return
	}
	reqID := r.PostForm.Get("req")
	p := o.store.getPending(reqID)
	if p == nil {
		o.errorPage(w, 400, "This request expired. Start the connection again from the app.")
		return
	}
	c := o.store.client(p.ClientID)
	if c == nil {
		o.errorPage(w, 400, "Unknown client.")
		return
	}
	if r.PostForm.Get("action") == "deny" {
		o.store.dropPending(reqID)
		redirectBack(w, r, p.RedirectURI, map[string]string{"error": "access_denied", "state": p.State, "iss": o.issuer})
		return
	}
	ip := o.clientIP(r)
	if !o.authFails.allowPeek(ip) {
		o.errorPage(w, http.StatusTooManyRequests, "Too many wrong secrets. Wait 15 minutes.")
		return
	}
	got := sha256.Sum256([]byte(r.PostForm.Get("secret")))
	want := sha256.Sum256([]byte(o.owner))
	if subtle.ConstantTimeCompare(got[:], want[:]) != 1 {
		o.authFails.record(ip)
		logf("oauth: wrong secret on the consent page from %s", ip)
		o.renderConsent(w, http.StatusForbidden, c, p.RedirectURI, reqID, "That secret is not right.")
		return
	}
	o.store.dropPending(reqID)
	code := o.store.addCode(&authCode{ClientID: p.ClientID, RedirectURI: p.RedirectURI, Challenge: p.Challenge, Resource: p.Resource})
	logf("oauth: approved client %q (%s)", c.Name, c.ID)
	redirectBack(w, r, p.RedirectURI, map[string]string{"code": code, "state": p.State, "iss": o.issuer})
}

// ---- token ----

func (o *oauthServer) authenticateClient(r *http.Request) (*oauthClient, bool) {
	id, secret, basic := r.BasicAuth()
	if !basic {
		id, secret = r.PostForm.Get("client_id"), r.PostForm.Get("client_secret")
	}
	c := o.store.client(id)
	if c == nil {
		return nil, basic
	}
	if c.AuthMethod == "none" {
		return c, basic
	}
	got, want := sha256.Sum256([]byte(hashOf(secret))), sha256.Sum256([]byte(c.SecretHash))
	if secret == "" || subtle.ConstantTimeCompare(got[:], want[:]) != 1 {
		return nil, basic
	}
	return c, basic
}

func (o *oauthServer) handleToken(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, 16<<10)
	if err := r.ParseForm(); err != nil {
		oauthError(w, 400, "invalid_request", "the body must be application/x-www-form-urlencoded")
		return
	}
	c, basic := o.authenticateClient(r)
	if c == nil {
		if basic {
			w.Header().Set("WWW-Authenticate", `Basic realm="agent-tools-mcp"`)
		}
		oauthError(w, http.StatusUnauthorized, "invalid_client", "client authentication failed")
		return
	}
	if res := r.PostForm.Get("resource"); res != "" && res != o.resource {
		oauthError(w, 400, "invalid_target", "this server only issues tokens for "+o.resource)
		return
	}
	switch r.PostForm.Get("grant_type") {
	case "authorization_code":
		o.grantCode(w, r, c)
	case "refresh_token":
		o.grantRefresh(w, r, c)
	default:
		oauthError(w, 400, "unsupported_grant_type", "use authorization_code or refresh_token")
	}
}

func (o *oauthServer) grantCode(w http.ResponseWriter, r *http.Request, c *oauthClient) {
	code := o.store.takeCode(r.PostForm.Get("code"))
	if code == nil || code.ClientID != c.ID || code.RedirectURI != r.PostForm.Get("redirect_uri") {
		oauthError(w, 400, "invalid_grant", "the code is invalid, expired, or already used")
		return
	}
	verifier := r.PostForm.Get("code_verifier")
	if len(verifier) < 43 || len(verifier) > 128 {
		oauthError(w, 400, "invalid_grant", "code_verifier must be 43 to 128 characters")
		return
	}
	sum := sha256.Sum256([]byte(verifier))
	if subtle.ConstantTimeCompare([]byte(base64.RawURLEncoding.EncodeToString(sum[:])), []byte(code.Challenge)) != 1 {
		oauthError(w, 400, "invalid_grant", "PKCE verification failed")
		return
	}
	access, refresh, err := o.store.issue(c.ID, code.Resource)
	if err != nil {
		logf("oauth: saving tokens failed: %v", err)
		oauthError(w, 500, "server_error", "could not save the tokens")
		return
	}
	writeTokens(w, access, refresh)
}

func (o *oauthServer) grantRefresh(w http.ResponseWriter, r *http.Request, c *oauthClient) {
	access, refresh, _, err := o.store.rotate(c.ID, r.PostForm.Get("refresh_token"))
	if err != nil {
		if errors.Is(err, errReuse) {
			logf("oauth: %v (client %s)", err, c.ID)
		}
		oauthError(w, 400, "invalid_grant", err.Error())
		return
	}
	writeTokens(w, access, refresh)
}

func writeTokens(w http.ResponseWriter, access, refresh string) {
	w.Header().Set("Pragma", "no-cache")
	writeJSON(w, 200, map[string]any{
		"access_token":  access,
		"token_type":    "Bearer",
		"expires_in":    int(accessTTL.Seconds()),
		"refresh_token": refresh,
		"scope":         "mcp",
	})
}

// clientIP is the address used for rate limits. Behind a proxy it is the last
// X-Forwarded-For entry, the one the nearest proxy appended, which a caller
// cannot forge.
func (o *oauthServer) clientIP(r *http.Request) string {
	if o.behindProxy {
		if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
			parts := strings.Split(xff, ",")
			return strings.TrimSpace(parts[len(parts)-1])
		}
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

// ---- rate limiting ----

// limiter counts events per key in a sliding window.
type limiter struct {
	mu     sync.Mutex
	max    int
	window time.Duration
	hits   map[string][]time.Time
}

func newLimiter(max int, window time.Duration) *limiter {
	return &limiter{max: max, window: window, hits: map[string][]time.Time{}}
}

func (l *limiter) prune(key string) []time.Time {
	cut := time.Now().Add(-l.window)
	kept := l.hits[key][:0]
	for _, t := range l.hits[key] {
		if t.After(cut) {
			kept = append(kept, t)
		}
	}
	if len(kept) == 0 {
		delete(l.hits, key)
	} else {
		l.hits[key] = kept
	}
	return kept
}

// allow records an event and reports whether it is within the limit.
func (l *limiter) allow(key string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	if len(l.prune(key)) >= l.max {
		return false
	}
	l.hits[key] = append(l.hits[key], time.Now())
	return true
}

// allowPeek reports whether another event is allowed, without recording one.
func (l *limiter) allowPeek(key string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	return len(l.prune(key)) < l.max
}

func (l *limiter) record(key string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.prune(key)
	l.hits[key] = append(l.hits[key], time.Now())
}
