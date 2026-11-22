# linkedin

Publish a post to LinkedIn, with an optional image, from the command line or
through an AI assistant (see the MCP server). Built on
[apikit](../../libs/apikit/README.md): every API call is a TOML endpoint.

```bash
linkedin post --text "Shipped the thing."
linkedin post --text "With a picture" --image ./shot.png
linkedin post --text "Preview only" --dry-run
linkedin check
```

**A post is public the moment it succeeds.** This tool has no delete. Use
`--dry-run` first.

## Set up

1. Create an app at <https://www.linkedin.com/developers/apps> and add the
   products "Share on LinkedIn" (scope `w_member_social`) and "Sign In with
   LinkedIn using OpenID Connect" (scopes `openid profile`).
2. Get an access token for your member with those scopes, by any OAuth 2.0
   authorization-code flow (LinkedIn's developer portal has a token generator).
3. `linkedin init` adds a `[linkedin]` table to the shared file,
   `~/.config/agent-tools/config.toml` (mode 0600; see
   [docs/configuration.md](../../docs/configuration.md)). Paste the token:

   ```toml
   [linkedin]
   default = "main"

   [linkedin.accounts.main]
   access_token = "..."            # or access_token_env / access_token_command
   # author = "urn:li:person:ABC123"   # optional: skips the userinfo call
   ```
4. `linkedin check` calls `/v2/userinfo` and prints the member's name.

### The token expires, and there is no refresh

LinkedIn access tokens expire, often after about 60 days (that figure is not from a live check
and was not confirmed here). **This version has no login or refresh
flow.** When the token lapses, posting fails with HTTP 401 and a message saying
so; get a new token and paste it. Mimir tracked the expiry date and refused to
post after it. This tool does not know the date: it finds out from LinkedIn.

## Commands

| Command | Does |
| --- | --- |
| `post --text TEXT [--image PATH]` | publish publicly. Prints `id` (a `urn:li:share:` URN), `author`, `image_attached` |
| `endpoints [NAME]` | list endpoints, or show one's parameters |
| `call ENDPOINT --param k=v` | call any endpoint and print its JSON |
| `check`, `accounts`, `init`, `where` | setup helpers |

`post` flow: validate the text and image, find the author, (with an image)
initialize an upload and PUT the file's bytes to the returned URL, then create
the post referencing the image URN. The post's id is read from the
`x-restli-id` response header. A POST is never retried.

The author is `author` from the account's config if set, otherwise
`urn:li:person:` plus the `sub` from `/v2/userinfo` (as Mimir does at connect
time). A token with only `w_member_social` cannot call userinfo; set `author`.

`--dry-run` validates the text and the image path, type and size, then prints the
requests that would be sent (token and upload URL not shown, the image not read)
and sends nothing. With no `author` set the plan shows a placeholder.

## Endpoints

| Endpoint | Request | Safety |
| --- | --- | --- |
| `userinfo` | `GET /v2/userinfo` | read |
| `image_upload_init` | `POST /rest/images?action=initializeUpload` | write |
| `post_create` | `POST /rest/posts` | write |

The `/rest` calls send `Linkedin-Version: 202608` and
`X-Restli-Protocol-Version: 2.0.0`. LinkedIn retires a version about a year
after its release, so the version will need bumping in `endpoints.toml`.
`call post_create` does not escape markup characters and prints no id (the id
is in a header); use `post`. Add endpoints in
`~/.config/agent-tools/linkedin/endpoints.d/*.toml`; the MCP tool
`linkedin_read` can call added read endpoints at once. There is no generic write
tool through MCP.

`base_url` in an account overrides `https://api.linkedin.com` (used by tests).

## Mimir parity

| Behavior | Here | Why |
| --- | --- | --- |
| Text up to 3000 characters, non-empty, counted in characters, checked on the raw text before any request | same | |
| Text is backslash-escaped for LinkedIn's markup (`\ \| { } @ [ ] ( ) < > # * _ ~`) on the wire only | same | |
| Image JPEG/GIF/PNG by extension, WebP refused, must be a regular file, at most 8 MB | same | |
| Images API: initialize upload, PUT the bytes, post references the image URN | same | |
| Headers `Linkedin-Version: 202608`, `X-Restli-Protocol-Version: 2.0.0`; PUBLIC, MAIN_FEED, PUBLISHED | same | |
| Bearer token is also sent on the upload PUT | same | Mimir does; whether LinkedIn needs it is unverified |
| Post id from `x-restli-id`; error if absent | same | |
| Error text `HTTP <status>: <message>`, 429 as rate limited | same, via apikit | |
| Author URN from `/v2/userinfo` `sub` | different | Mimir does it once at connect time and stores it; here it is looked up per post unless `author` is configured |
| Expired token | different | Mimir refused to post after a stored expiry date; here LinkedIn's 401 is turned into a "paste a new token" message |
| 401 and 403 hints | extra | tell the person what to fix |
| Image path must be absolute | different | a CLI takes relative paths |
| `as_account` | different | the standard `--account` flag, optional with one account or a default |
| Live confirmation before posting | different | the MCP manifest marks `linkedin_post` destructive; `--dry-run` previews |
| `artifact_id` image source | not included | a Mimir concept |
| OAuth connect flow, token storage in the keychain, refresh | not included | first version: the token is pasted; secrets use key/_env/_command |
| `social_posts` record of what was posted | not included | Mimir's own database |

## Not verified

Everything here is tested against a **fake LinkedIn server** that asserts the
request details (bearer header, version headers, the upload receiving the raw
bytes, the post body, the id read from the response header). **Nothing has been
run against the live LinkedIn API**; no credentials were available. The request
shapes come from Mimir's client, which says they were verified live. Not covered
by Mimir and therefore unverified: whether the upload PUT needs the bearer
token, the exact token lifetime, and whether `/v2/userinfo` needs the `openid`
scope for your app. Mimir's `202608` version header was current when it was
written and may have been retired since.
