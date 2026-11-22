// x searches X (Twitter), reads timelines and threads, measures engagement, and
// posts and replies, from the command line. It is built on apikit: every API call
// is a TOML endpoint, so more can be added without changing this program (see
// `x endpoints` and endpoints.d).
package main

import (
	"context"
	_ "embed"
	"fmt"
	"time"

	"github.com/revans/agent-tools/apikit"
)

//go:embed endpoints.toml
var builtin string

var version = "0.1.0"

// apiBase is X API v2. Mimir's own client names it the same way.
const apiBase = "https://api.x.com/2"

const starter = `# x config. Create a project and app at https://developer.x.com, give the app
# Read and write permission, then generate the four keys below (see the README).
# Each credential can be written three ways: api_key, api_key_env (the name of
# an environment variable) or api_key_command (a command that prints it).

default = "main"

[accounts.main]
api_key             = "..."
api_secret          = "..."
access_token        = "..."
access_token_secret = "..."
# How long an engagement baseline is reused before it is recomputed:
# baseline_refresh_interval = "weekly"   # or "monthly" (30 days)
# For tests or a proxy:
# base_url = "https://api.x.com/2"
# state_dir = "/absolute/path"           # where baselines.json is kept
`

// settings are the plain (non-secret) account settings the commands need beyond
// the client itself. newClient fills it in when the account is resolved.
type settings struct {
	account  string
	stateDir string
	refresh  time.Duration
}

func service() *apikit.Service {
	st := &settings{}
	return &apikit.Service{
		Name:      "x",
		Summary:   "search X, read timelines and threads, measure engagement, post and reply",
		Version:   version,
		Starter:   starter,
		Builtin:   builtin,
		UserAgent: "agent-tools-x/" + version,
		NewClient: func(name string, acct map[string]any) (*apikit.Client, error) {
			return newClient(st, name, acct)
		},
		Check: func(ctx context.Context, r *apikit.Run) (any, error) {
			res, err := r.Call("users_me", nil)
			if err != nil {
				return nil, err
			}
			if r.DryRun() {
				return res.Data, nil
			}
			m, _ := res.Data.(map[string]any)
			return fmt.Sprintf("signed in as @%s", str(m, "username")), nil
		},
		Commands: commands(st),
	}
}

func newClient(st *settings, name string, acct map[string]any) (*apikit.Client, error) {
	var creds [4]string
	for i, key := range []string{"api_key", "api_secret", "access_token", "access_token_secret"} {
		v, err := apikit.ResolveSecret(name, acct, key)
		if err != nil {
			return nil, err
		}
		creds[i] = v
	}
	interval, err := refreshInterval(apikit.OptString(acct, "baseline_refresh_interval", "weekly"))
	if err != nil {
		return nil, fmt.Errorf("account %q: %w", name, err)
	}
	st.account = name
	st.refresh = interval
	st.stateDir = apikit.OptString(acct, "state_dir", "")
	return &apikit.Client{
		BaseURL: apikit.OptString(acct, "base_url", apiBase),
		Auth: &apikit.OAuth1{
			ConsumerKey:    creds[0],
			ConsumerSecret: creds[1],
			Token:          creds[2],
			TokenSecret:    creds[3],
		},
		// No retries: Mimir sends each request once, and X's rate limits reset in
		// 15-minute windows, so a retry seconds later only wastes a call.
		RetryLimit: 0,
	}, nil
}

func main() { apikit.Main(service()) }
