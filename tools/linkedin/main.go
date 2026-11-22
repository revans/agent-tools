// linkedin publishes posts (text, optionally one image) to LinkedIn from the
// command line. It is built on apikit: every API call is a TOML endpoint, so
// more can be added without changing this program (see `linkedin endpoints`).
package main

import (
	"context"
	_ "embed"
	"fmt"

	"github.com/revans/agent-tools/apikit"
)

//go:embed endpoints.toml
var builtin string

var version = "0.1.0"

const apiBase = "https://api.linkedin.com"

const starter = `# linkedin config. The access token is pasted here: there is no login flow yet.
# Get one with the scopes "openid profile w_member_social" (see the README).
# LinkedIn access tokens expire, often after 60 days; when posting starts failing
# with 401, paste a new one.
# Each credential can be written three ways: access_token, access_token_env (the
# name of an environment variable) or access_token_command (a command that prints it).

default = "main"

[accounts.main]
access_token = "..."
# The author is found from the token (the /v2/userinfo call). Set it to skip that
# call, or when the token has no openid scope: "urn:li:person:ABC123" or just "ABC123".
# author = "urn:li:person:ABC123"
`

// tool carries what newClient learns that the commands need.
type tool struct{ author string }

func service() *apikit.Service {
	t := &tool{}
	return &apikit.Service{
		Name:      "linkedin",
		Summary:   "publish posts, with an optional image, to LinkedIn",
		Version:   version,
		Starter:   starter,
		Builtin:   builtin,
		UserAgent: "agent-tools-linkedin/" + version,
		NewClient: t.newClient,
		Check: func(ctx context.Context, r *apikit.Run) (any, error) {
			res, err := r.Call("userinfo", nil)
			if err != nil {
				return nil, wrapAPIError(err)
			}
			if r.DryRun() {
				return res.Data, nil
			}
			m, _ := res.Data.(map[string]any)
			return map[string]any{"ok": true, "name": m["name"], "sub": m["sub"]}, nil
		},
		Commands: []*apikit.Command{t.postCommand()},
	}
}

func (t *tool) newClient(name string, acct map[string]any) (*apikit.Client, error) {
	token, err := apikit.ResolveSecret(name, acct, "access_token")
	if err != nil {
		return nil, err
	}
	t.author, err = normalizeAuthor(apikit.OptString(acct, "author", ""))
	if err != nil {
		return nil, fmt.Errorf("account %q: %w", name, err)
	}
	return &apikit.Client{
		BaseURL:    apikit.OptString(acct, "base_url", apiBase),
		Auth:       apikit.Bearer{Token: token},
		RetryLimit: 2,
	}, nil
}

func main() { apikit.Main(service()) }
