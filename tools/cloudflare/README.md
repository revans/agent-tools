# cloudflare

Manage Cloudflare from the command line, or through an AI assistant (see the MCP
server): list zones, add, change and delete DNS records, purge the cache, list
tunnels. It talks to the Cloudflare API v4 and works in **domain names**, not zone
ids. Built on [apikit](../../libs/apikit/README.md): every API call is a TOML
endpoint, so adding more takes a few lines and no code.

```bash
cloudflare zones
cloudflare dns list --zone example.com --type A
cloudflare dns add --zone example.com --type A --name www --content 192.0.2.10 --proxied
cloudflare dns update RECORD_ID --zone example.com --content 192.0.2.11
cloudflare dns delete RECORD_ID --zone example.com --max-safety destructive --dry-run
cloudflare purge --zone example.com --files https://example.com/css/site.css
cloudflare tunnels
```

## Set up

1. Create an API token at <https://dash.cloudflare.com/profile/api-tokens>
   ("Create Custom Token"). Give it **only** the permissions for what you will do:

   | Command | Permission (scope) |
   | --- | --- |
   | `zones`, and every `--zone NAME` lookup | Zone Read (Zone), on the zones you use |
   | `dns list` | DNS Read, or DNS Write (Zone) |
   | `dns add`, `dns update`, `dns delete` | DNS Write (Zone). The dashboard may label it "Edit" |
   | `purge` | Cache Purge (Zone) |
   | `tunnels`, `call tunnel*` | Cloudflare Tunnel Read (Account) |
   | `call pages_*` | Cloudflare Pages Read (Account) |
   | `call accounts` | some account-level read permission (not checked which) |
   | `check` | none |

   Scope the token to the zones it needs. A read-only setup needs no write permission at all.
2. `cloudflare init` adds a `[cloudflare]` table to the shared file,
   `~/.config/agent-tools/config.toml` (mode 0600; see
   [docs/configuration.md](../../docs/configuration.md)). Fill it in:

   ```toml
   [cloudflare]
   default = "main"

   [cloudflare.accounts.main]
   api_token = "..."                 # or api_token_env = "CLOUDFLARE_API_TOKEN"
                                     # or api_token_command = "pass show cloudflare/token"
   account_id = "..."                # needed for tunnels and Pages; `cloudflare call accounts` lists yours
   # base_url = "..."                # only for tests

   [cloudflare.accounts.other]       # another token, e.g. another Cloudflare account
   api_token_env = "CLOUDFLARE_OTHER_TOKEN"
   ```
3. `cloudflare check` confirms the token is accepted and active. It calls
   `/user/tokens/verify` and, for an account-owned token (which that path
   rejects), falls back to `/accounts/{account_id}/tokens/verify` when `account_id` is set.

The token is sent as `Authorization: Bearer ...`, never in the URL. `--dry-run`
and `--debug` redact it.

## Commands

| Command | Does |
| --- | --- |
| `zones [--name N] [--status S] [--all]` | zones: `name`, `id`, `status`, `account`. First page unless `--all`; `more` says whether pages remain |
| `dns list --zone Z [--type T] [--name N] [--content C] [--all]` | records: `id`, `type`, `name`, `content`, `ttl`, `proxied`, `priority`, `comment`. `--name www` is read as `www.example.com`, `@` as the apex |
| `dns add --zone Z --type T --name N --content C [--ttl 1] [--proxied] [--comment TEXT] [--priority N] [--data JSON]` | create a record |
| `dns update RECORD_ID --zone Z [fields...]` | a PATCH of **only the fields given**. `--proxied=false` and `--comment ""` count as given |
| `dns delete RECORD_ID --zone Z` | delete a record. Runs **only** with an explicit `--max-safety destructive` |
| `purge --zone Z (--everything \| --files URL,URL \| --hosts H,H \| --tags T,T)` | purge the cache; exactly one mode |
| `tunnels [--name N] [--status S] [--all]` | tunnels: `name`, `id`, `status`, `connections` (live connections). Needs `account_id` |
| `check` | verify the token |
| `endpoints [NAME]`, `call ENDPOINT --param k=v [--pages N]`, `accounts`, `init`, `where` | the apikit standard commands |

`--zone` takes a name (`example.com`) or a 32-character zone id. A name is
looked up first; **no match, or more than one match, is an error** (the message
lists the ids, and you then pass the id). Nothing is guessed.

Rules the commands enforce before anything is sent:

- The record type must be one Cloudflare knows (A, AAAA, CNAME, TXT, MX, NS, PTR, SRV, CAA, ...); the name must not be empty; an **MX record needs `--priority`**; `--ttl` is 1 or 30 to 86400 (Cloudflare says 60 minimum, 30 on Enterprise); only A, AAAA and CNAME can be `--proxied`.
- **TTL 1 means "automatic"** in Cloudflare, and it is the default here.
- **`proxied` defaults to false**: a created record is sent with `"proxied": false` unless you pass `--proxied`.
- Types with components (SRV, CAA, HTTPS, ...) take `--data` as a JSON object instead of `--content` (Cloudflare marks their `content` read-only). The MCP tools do not offer these types.
- `--dry-run` on a write shows the request (method, URL, body, redacted headers) and sends nothing. The lookups before it (zone name to id, the record about to be deleted) are real, read-only requests, so the preview is accurate.
- `dns delete --dry-run` prints `would_delete` (the record, fetched first) and the `DELETE` request. A delete of a record that does not exist stops before sending a `DELETE`.
- Purge: `--files` URLs must be complete (`https://...`) and cannot contain a comma (write it `%2C`); `--hosts` are bare hostnames. `--mode everything|files|hosts|tags --values A,B` is an equivalent spelling (the MCP tool uses it). Purging everything sends the whole zone's traffic back to your origin; use it sparingly.

A response whose envelope says `"success": false` is an error even with HTTP
status 200, and the error text carries every `errors[]` entry as `[code] message`.
A 401 or 403 adds a hint.

## Endpoints

`cloudflare endpoints` lists them; `cloudflare call NAME --param k=v` runs one
(`--pages N` follows page numbers: `result_info.page` up to `result_info.total_pages`).

| Endpoint | Safety | Request |
| --- | --- | --- |
| `token_verify` | read | `GET /user/tokens/verify` |
| `account_token_verify` | read | `GET /accounts/{account_id}/tokens/verify` |
| `accounts` | read | `GET /accounts` (paginated) |
| `zones` | read | `GET /zones` (`name`, `status`, `account.id`; paginated) |
| `zone` | read | `GET /zones/{zone_id}` |
| `dns_records` | read | `GET /zones/{zone_id}/dns_records` (`type`, `name`, `content`, `proxied`; paginated) |
| `dns_record` | read | `GET /zones/{zone_id}/dns_records/{dns_record_id}` |
| `zone_settings` | read | `GET /zones/{zone_id}/settings` |
| `tunnels` | read | `GET /accounts/{account_id}/cfd_tunnel` (paginated; deleted tunnels excluded) |
| `tunnel` | read | `GET /accounts/{account_id}/cfd_tunnel/{tunnel_id}` |
| `tunnel_configuration` | read | `GET /accounts/{account_id}/cfd_tunnel/{tunnel_id}/configurations` |
| `pages_projects` | read | `GET /accounts/{account_id}/pages/projects` (paginated) |
| `pages_deployments` | read | `GET /accounts/{account_id}/pages/projects/{project_name}/deployments` (paginated) |
| `dns_record_create` | write | `POST /zones/{zone_id}/dns_records` |
| `dns_record_update` | write | `PATCH /zones/{zone_id}/dns_records/{dns_record_id}` |
| `purge_cache` | write | `POST /zones/{zone_id}/purge_cache` |
| `dns_record_delete` | destructive | `DELETE /zones/{zone_id}/dns_records/{dns_record_id}` |

`call` refuses anything above `--max-safety` (default `write`), so a delete through
`call dns_record_delete` also needs `--max-safety destructive`. The generic `call`
does not fill `account_id` from the config: pass `--param account_id=...`
(only the `tunnels` command reads it from the config).

### Add an endpoint

Create `~/.config/agent-tools/cloudflare/endpoints.d/mine.toml` (or point
`CLOUDFLARE_ENDPOINTS=/some/dir` at a directory):

```toml
[[endpoint]]
name = "zone_dnssec"
description = "A zone's DNSSEC status."
method = "GET"
path = "/zones/{zone_id}/dnssec"
safety = "read"
select = "result"
  [[endpoint.param]]
  name = "zone_id"
  in = "path"
  required = true
  description = "Zone id."
```

then `cloudflare call zone_dnssec --param zone_id=...`, and through the MCP server
`cloudflare_read` reaches it with no other change. For a list, add
`[endpoint.paginate]` with `kind = "page"`, `cursor_path = "result_info.page"`,
`cursor_param = "page"` (a declared param), `total_path = "result_info.total_pages"`.
Config file location for settings: `CLOUDFLARE_CONFIG` or `--config`.

## Not verified

**Nothing here has run against the real Cloudflare API**: there were no
credentials. Everything is tested against a fake server that checks the Bearer
header, paths, query parameters, JSON bodies, the page-number loop, zone-name
resolution, the `success:false` envelope and the delete gate.

What was checked against Cloudflare's **published OpenAPI schema**
(`cloudflare/api-schemas`, the source of the API reference at
developers.cloudflare.com/api; fetched and read, not run):

- Confirmed: paths and methods of every endpoint above; `zones` filters (`name`, `status`, `account.id`, `page`, `per_page` 5 to 50); `dns_records` filters (`name`, `type`, `content`, `proxied`, `page`, `per_page`, default 100) and that `name`/`content` are exact matches on the full value; the `result_info` fields `page` and `total_pages` for page pagination; the DNS record body fields (`type`, `name`, `content`, `ttl`, `proxied`, `comment`, `priority`, `data`), TTL 1 = automatic, `proxied` default false, MX priority required, and that SRV/CAA-style records take `data` (their `content` is read-only); the five purge bodies (`files`, `hosts`, `tags`, `purge_everything`, with `purge_everything` alone); the tunnel list filters (`name`, `status`, `is_deleted`, `page`, `per_page`) and the tunnel fields `id`, `name`, `status`, `connections`; Pages `page`/`per_page` and the `env` filter; the response envelope (`success`, `errors`, `messages`, `result`, `result_info`); the delete response (`result.id`); `/user/tokens/verify` and the account-owned variant.
- Confirmed in the docs page "API token permissions": the permission names Zone Read, DNS Read, DNS Write, Cache Purge, Cloudflare Tunnel Read, Cloudflare Pages Read.
- **Not confirmed**: that each token permission above is the *minimum* that call accepts (the table is the natural mapping, not a tested one); that `result_info` is present on the Pages and accounts lists with the same fields (the schema shows it for zones, DNS and tunnels; the Pages list schema was not checked for it, so `--pages` on Pages may stop after one page); the Enterprise TTL floor of 30 and rate limits; limits on how many files, hosts or tags one purge may carry (the tool enforces none); the exact error codes and messages the live API returns (the tests use invented ones); whether `/user/tokens/verify` is the right check for every kind of token (hence the account fallback); the `connections` count (the schema lists the field; its contents were not inspected, the count is `len(connections)`); the shape of `pages_deployments` and `tunnel_configuration` results (passed through untouched).
- A 200 response with `success:false` is not something the schema describes; it is handled because the task required it.
