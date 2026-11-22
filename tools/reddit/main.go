// reddit searches Reddit and reads posts with their comments, from the command
// line. It is built on apikit: every API call is a TOML endpoint, so more can be
// added without changing this program (see `reddit endpoints` and endpoints.d).
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

const (
	apiBase  = "https://oauth.reddit.com"
	tokenURL = "https://www.reddit.com/api/v1/access_token"
)

const starter = `# reddit config. Create an app at https://www.reddit.com/prefs/apps
# (type "script" or "web") and copy its client id and secret here.
# Each credential can be written three ways: client_id, client_id_env (the name
# of an environment variable) or client_id_command (a command that prints it).

default = "main"

[accounts.main]
client_id     = "..."
client_secret = "..."
# Reddit asks for a descriptive User-Agent that names you:
# user_agent = "agent-tools-reddit/0.1 (by /u/yourname)"
`

func service() *apikit.Service {
	return &apikit.Service{
		Name:      "reddit",
		Summary:   "search Reddit and read posts with their top comments",
		Version:   version,
		Starter:   starter,
		Builtin:   builtin,
		UserAgent: "agent-tools-reddit/" + version,
		NewClient: newClient,
		Check: func(ctx context.Context, r *apikit.Run) (any, error) {
			c, err := r.Client()
			if err != nil {
				return nil, err
			}
			cc, ok := c.Auth.(*apikit.ClientCredentials)
			if !ok {
				return nil, fmt.Errorf("unexpected auth type")
			}
			if _, err := cc.Token(); err != nil {
				return nil, err
			}
			return "an access token was issued", nil
		},
		Commands: []*apikit.Command{searchCommand(), fetchCommand()},
	}
}

func newClient(name string, acct map[string]any) (*apikit.Client, error) {
	id, err := apikit.ResolveSecret(name, acct, "client_id")
	if err != nil {
		return nil, err
	}
	secret, err := apikit.ResolveSecret(name, acct, "client_secret")
	if err != nil {
		return nil, err
	}
	ua := apikit.OptString(acct, "user_agent", "agent-tools-reddit/"+version)
	return &apikit.Client{
		BaseURL:   apikit.OptString(acct, "base_url", apiBase),
		UserAgent: ua,
		Auth: &apikit.ClientCredentials{
			TokenURL:  apikit.OptString(acct, "token_url", tokenURL),
			ID:        id,
			Secret:    secret,
			UserAgent: ua,
		},
		RetryLimit: 2,
	}, nil
}

func main() { apikit.Main(service()) }
