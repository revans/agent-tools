// cloudflare manages Cloudflare zones, DNS records, the cache and tunnels from the
// command line through the Cloudflare API v4. It is built on apikit: every API
// call is a TOML endpoint, so more can be added without changing this program
// (see `cloudflare endpoints` and endpoints.d).
package main

import (
	"bytes"
	"context"
	_ "embed"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"

	"github.com/revans/agent-tools/apikit"
)

//go:embed endpoints.toml
var builtin string

var version = "0.1.0"

const apiBase = "https://api.cloudflare.com/client/v4"

const starter = `# cloudflare config. Create an API token at https://dash.cloudflare.com/profile/api-tokens
# with only the permissions you need (see the README). The token can be written
# three ways: api_token, api_token_env (the name of an environment variable) or
# api_token_command (a command that prints it).

default = "main"

[accounts.main]
api_token = "..."
# account_id = "..."   # needed for tunnels and Pages; "cloudflare call accounts" lists yours
`

func service() *apikit.Service {
	return &apikit.Service{
		Name:      "cloudflare",
		Summary:   "manage Cloudflare zones, DNS records, cache purges and tunnels (API v4)",
		Version:   version,
		Starter:   starter,
		Builtin:   builtin,
		UserAgent: "agent-tools-cloudflare/" + version,
		NewClient: newClient,
		Check:     check,
		Commands: []*apikit.Command{
			zonesCommand(), dnsCommand(), purgeCommand(), tunnelsCommand(),
		},
	}
}

// accountIDs remembers each client's configured account_id (apikit's Client has
// no place for it), so tunnels can use it.
var accountIDs sync.Map // *apikit.Client -> string

func newClient(name string, acct map[string]any) (*apikit.Client, error) {
	token, err := apikit.ResolveSecret(name, acct, "api_token")
	if err != nil {
		return nil, err
	}
	c := &apikit.Client{
		BaseURL: apikit.OptString(acct, "base_url", apiBase),
		// The token goes in a header, never in the URL: URLs end up in logs.
		Auth:       apikit.Bearer{Token: token},
		HTTP:       &http.Client{Transport: envelopeTransport{base: http.DefaultTransport}},
		RetryLimit: 2,
	}
	if id := apikit.OptString(acct, "account_id", ""); id != "" {
		accountIDs.Store(c, id)
	}
	return c, nil
}

func accountID(r *apikit.Run) (string, error) {
	c, err := r.Client()
	if err != nil {
		return "", err
	}
	if id, ok := accountIDs.Load(c); ok {
		return id.(string), nil
	}
	return "", fmt.Errorf("this needs account_id in the account's config (`cloudflare call accounts` lists the ids your token can see)")
}

func check(ctx context.Context, r *apikit.Run) (any, error) {
	res, err := r.Call("token_verify", nil)
	if err != nil {
		// An account-owned token is verified at a different path.
		if id, e := accountID(r); e == nil {
			if res2, err2 := r.Call("account_token_verify", map[string]string{"account_id": id}); err2 == nil {
				return tokenDetail(res2.Data), nil
			}
		}
		return nil, explain(err)
	}
	return tokenDetail(res.Data), nil
}

func tokenDetail(data any) any {
	m, _ := data.(map[string]any)
	if st, _ := m["status"].(string); st != "" && st != "active" {
		return "the token is " + st
	}
	return map[string]any{"status": m["status"], "expires_on": m["expires_on"]}
}

// envelopeTransport makes Cloudflare's envelope honest. Every response carries
// {"success": bool, "errors": [{"code", "message"}]}; a success:false envelope is
// an error even when the HTTP status is 200. The transport gives such a response
// a 400 status when it had a 2xx one, and puts every error ("[code] message")
// into a top-level "message", which is where apikit looks for the text to show.
type envelopeTransport struct{ base http.RoundTripper }

const maxBody = 8<<20 + 1

func (t envelopeTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	resp, err := t.base.RoundTrip(req)
	if err != nil {
		return nil, err
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxBody))
	resp.Body.Close()
	if err != nil {
		return nil, err
	}
	if len(body) >= maxBody {
		resp.Body = io.NopCloser(bytes.NewReader(body))
		return resp, nil
	}
	if fixed, ok := rewriteEnvelope(body, resp.StatusCode/100 == 2); ok {
		body = fixed
		if resp.StatusCode/100 == 2 {
			resp.StatusCode, resp.Status = http.StatusBadRequest, "400 Bad Request"
		}
		resp.Header.Del("Content-Length")
		resp.ContentLength = int64(len(body))
	}
	resp.Body = io.NopCloser(bytes.NewReader(body))
	return resp, nil
}

// rewriteEnvelope returns the body with a "message" added when it is a
// success:false envelope.
func rewriteEnvelope(body []byte, wasOK bool) ([]byte, bool) {
	var env map[string]json.RawMessage
	dec := json.NewDecoder(bytes.NewReader(body))
	dec.UseNumber()
	if dec.Decode(&env) != nil {
		return nil, false
	}
	var success bool
	if raw, ok := env["success"]; !ok || json.Unmarshal(raw, &success) != nil || success {
		return nil, false
	}
	var errs []struct {
		Code    json.Number `json:"code"`
		Message string      `json:"message"`
	}
	_ = json.Unmarshal(env["errors"], &errs)
	var parts []string
	for _, e := range errs {
		switch {
		case e.Code != "" && e.Message != "":
			parts = append(parts, fmt.Sprintf("[%s] %s", e.Code, e.Message))
		case e.Message != "":
			parts = append(parts, e.Message)
		}
	}
	msg := strings.Join(parts, "; ")
	if msg == "" {
		msg = "the API reported success:false without an error message"
	}
	if wasOK {
		msg = "the API answered 200 but success is false: " + msg
	}
	env["message"], _ = json.Marshal(msg)
	out, err := json.Marshal(env)
	if err != nil {
		return nil, false
	}
	return out, true
}

func main() { apikit.Main(service()) }
