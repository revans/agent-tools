// youtube searches YouTube from the command line through the YouTube Data API
// v3. It is built on apikit: every API call is a TOML endpoint, so more can be
// added without changing this program (see `youtube endpoints` and endpoints.d).
package main

import (
	"context"
	_ "embed"

	"github.com/revans/agent-tools/apikit"
)

//go:embed endpoints.toml
var builtin string

var version = "0.1.0"

const apiBase = "https://www.googleapis.com/youtube/v3"

const starter = `# youtube config. Create an API key for the YouTube Data API v3 in Google Cloud
# (see the README). The key can be written three ways: api_key, api_key_env (the
# name of an environment variable) or api_key_command (a command that prints it).

default = "main"

[accounts.main]
api_key = "..."
`

func service() *apikit.Service {
	return &apikit.Service{
		Name:      "youtube",
		Summary:   "search YouTube videos (YouTube Data API v3)",
		Version:   version,
		Starter:   starter,
		Builtin:   builtin,
		UserAgent: "agent-tools-youtube/" + version,
		NewClient: newClient,
		Check: func(ctx context.Context, r *apikit.Run) (any, error) {
			// videos.list by id costs 1 quota unit (search.list costs 100). A
			// made-up id returns an empty list, which still proves the key works.
			if _, err := r.Call("videos", map[string]string{"id": "agenttoolscheck", "part": "id"}); err != nil {
				return nil, explain(err)
			}
			return "the API key was accepted", nil
		},
		Commands: []*apikit.Command{searchCommand()},
	}
}

func newClient(name string, acct map[string]any) (*apikit.Client, error) {
	key, err := apikit.ResolveSecret(name, acct, "api_key")
	if err != nil {
		return nil, err
	}
	return &apikit.Client{
		BaseURL: apikit.OptString(acct, "base_url", apiBase),
		// The key goes in a header, never in the URL: URLs end up in logs.
		Auth:       apikit.HeaderKey{Name: "X-Goog-Api-Key", Value: key},
		RetryLimit: 2,
	}, nil
}

func main() { apikit.Main(service()) }
