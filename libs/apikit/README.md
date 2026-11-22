# apikit

A small Go library for building command-line tools around web APIs, where
**adding an API endpoint means adding a few lines of TOML**, not code.

The `reddit` tool is built on it, and the X, YouTube, LinkedIn and Instagram
tools will be. Every tool gets the same commands and the same safety behavior
for free.

## What a tool built on apikit gives you

| Command | Does |
| --- | --- |
| `NAME endpoints [ENDPOINT]` | list the endpoints, or show one with its parameters |
| `NAME call ENDPOINT --param k=v ...` | call any endpoint by name |
| `NAME check` | verify the credentials with the cheapest real call |
| `NAME accounts` | list configured accounts (never prints secrets) |
| `NAME init` | add the tool's starter `[NAME]` table to the shared config file (mode 0600) |
| `NAME where` | show which config file is used, whether it is the shared file, and where extra endpoints are read from |
| ... | plus the service's own high-level commands |

Every command takes `--account`, `--config`, `--dry-run`, `--debug`,
`--max-safety read|write|destructive` and `--pretty`. Output is JSON. Errors go
to stderr; exit 0 is ok, 1 an error, 2 a usage mistake.

## Adding an endpoint (no code, no rebuild)

Create `~/.config/agent-tools/TOOL/endpoints.d/anything.toml` (or
`~/.config/TOOL/endpoints.d/`), or point `TOOL_ENDPOINTS=/some/dir` at a directory
(`:`-separated for several):

```toml
[[endpoint]]
name = "subreddit_about"
description = "A subreddit's title, description and subscriber count."
method = "GET"
path = "/r/{subreddit}/about"          # {placeholders} come from path params
safety = "read"                        # read | write | destructive
select = "data"                        # the part of the response to return
fields = ["display_name", "title", "subscribers"]   # keep only these keys
  [endpoint.fixed]                     # constant query parameters
  raw_json = "1"
  [[endpoint.param]]
  name = "subreddit"
  in = "path"
  required = true
  description = "Subreddit name without the r/ prefix."
```

Then:

```bash
reddit endpoints subreddit_about            # see its parameters
reddit call subreddit_about --param subreddit=golang
```

and, through the MCP server, the assistants can call it too with `reddit_read`
(`{"endpoint": "subreddit_about", "params": {"subreddit": "golang"}}`) without
anyone touching the manifests. A mistake in the file stops the tool at start with
the file and endpoint named. A name that already exists is an error, never a
silent override.

### Endpoint reference

| Key | Meaning |
| --- | --- |
| `name`, `description` | required; the description is what a model reads to decide when to call |
| `method` | `GET`, `POST`, `PUT`, `PATCH`, `DELETE` |
| `path` | starts with `/` (joined to the account's base URL) or is a full URL; `{name}` is filled from a `path` param |
| `safety` | `read`, `write` or `destructive`. A non-GET endpoint cannot be `read`. `--max-safety` refuses anything above it |
| `base` | optional base URL for this endpoint only |
| `body` | `json`, `form` or `none`; by default `json` when there are body params, `form` when there are form params |
| `select` | path into the response: `data.children[*].data`, `items[0].id`. `[*]` maps over a list |
| `fields` | keep only these keys of each selected object. `name` or `alias:path.to.value` |
| `timeout` | per request, default `30s` |
| `fixed` | constant query parameters |
| `headers` | constant request headers |
| `[endpoint.paginate]` | `cursor_path` (where the next cursor is in the response), `cursor_param` (the declared param that carries it back), `max_pages` |

A **param** has `name`, `description` (required), `type`, `in`, `wire`,
`required`, `default` and `enum`.

| `type` | Input | Sent as |
| --- | --- | --- |
| `string` (default) | text | as is; `enum` limits the values |
| `integer`, `number`, `boolean` | text, checked | as is |
| `list` | `a,b,c` | comma-joined in a query; a JSON array in a body |
| `json` | a JSON document | embedded in the JSON body (body params only) |

`in` is `query` (default), `path`, `header`, `body` or `form`. `wire` is the name
sent on the wire when it differs from `name`.

Pagination: `--pages N` follows the cursor up to N pages and concatenates the
selected lists. Without `[endpoint.paginate]` an endpoint does not paginate.

## Building a new service tool

A tool is one `Service` and a `main`:

```go
//go:embed endpoints.toml
var builtin string

func main() {
	apikit.Main(&apikit.Service{
		Name:      "myapi",
		Summary:   "what it does",
		Version:   "0.1.0",
		Builtin:   builtin,                 // the built-in endpoints
		Starter:   starterConfig,           // what `init` writes
		UserAgent: "agent-tools-myapi/0.1",
		NewClient: func(name string, acct map[string]any) (*apikit.Client, error) {
			key, err := apikit.ResolveSecret(name, acct, "api_key")
			if err != nil {
				return nil, err
			}
			return &apikit.Client{
				BaseURL: apikit.OptString(acct, "base_url", "https://api.example.com"),
				Auth:    apikit.Bearer{Token: key},
				RetryLimit: 2,
			}, nil
		},
		Check:    func(ctx context.Context, r *apikit.Run) (any, error) { /* one cheap call */ },
		Commands: []*apikit.Command{ /* optional high-level commands */ },
	})
}
```

Put the tool in `tools/NAME` with its own `go.mod` and a `replace` pointing at
`../../libs/apikit`, then add `NAME` to `GO_TOOLS` in the root Makefile.

A **high-level command** is for what one endpoint cannot say: reshaping a
response, combining calls, filtering. It calls `r.Call("endpoint", params)` and
returns any value, which is printed as JSON. `Result.Raw` is the whole decoded
response; `Result.Data` is the `select`ed and `fields`-projected part. See
`tools/reddit/reddit.go`.

### Authentication

| Type | Use |
| --- | --- |
| `Bearer{Token}` | `Authorization: Bearer ...` |
| `HeaderKey{Name, Value}` | a secret in a named header (`X-Goog-Api-Key`) |
| `QueryKey{Name, Value}` | a secret in a query parameter, when nothing else is offered |
| `Basic{User, Pass}` | HTTP basic auth |
| `ClientCredentials{...}` | OAuth2 `client_credentials`: fetches a token once per run and sends it as a bearer |
| `NoAuth{}` | none |

Anything else implements `Authenticator` (one method, `Apply(req, dry)`). With
`dry` set it must neither use the network nor reveal a secret.

### Configuration and credentials

A tool reads the shared agent-tools file, with its settings in a table named for
it, and falls back to its own older file. The lookup order, the 0600 rule, and
how the Rust tools do the same are in
[docs/configuration.md](../../docs/configuration.md). `config.go` implements it
(`LoadConfig`, `Nest`, `AddToShared`, `InitShared`).

An account is a table in the tool's settings; each credential can be written
three ways, and exactly one may be present:

```toml
[reddit.accounts.main]
client_id = "literal"                       # simplest
client_id_env = "REDDIT_ID"                 # read from an environment variable
client_id_command = "pass show reddit/id"   # stdout of a command
```

## Safety, by construction

- **Secrets are never taken from flags** (they would show in `ps`) and never
  printed. `--dry-run` shows the request with secret headers and query values
  replaced by `[redacted]`, and never fetches a token.
- **Only GET is retried**, on 429 and 5xx, with backoff. A POST is never retried:
  a repeated post could publish twice.
- **Unknown parameters, wrong types, bad enum values and missing required
  parameters are errors** before anything is sent.
- Responses are capped at 8 MB, and each request has a timeout.
- Error text is the API's own message plus the status. A rate limit says how long to wait.

## Testing

`httptest` is all that is needed: point `NewClient` at a test server through a
`base_url` account setting and run commands with `apikit.RunArgs(service, args,
stdout, stderr)`. `tools/reddit/reddit_test.go` is the model. Run the library's
own tests with `make test-libs`.
