# instagram

Post a photo, comment on your media and reply to comments through the Instagram
Graph API, from the command line or through an AI assistant (see the MCP server).
Built on [apikit](../../libs/apikit/README.md): every API call is a TOML endpoint,
so `instagram call NAME` reaches anything listed by `instagram endpoints`.

```bash
instagram post --caption "Hello from the terminal" --image-url https://example.com/photo.jpg
instagram comment --media-id 17895695668004550 --message "Thanks for coming!"
instagram reply --comment-id 18000000000000001 --message "See you next time"
instagram post --caption "Test" --image-url https://example.com/photo.jpg --dry-run
```

Everything these commands publish is **public and cannot be undone through this
tool**. Use `--dry-run` first: it shows each request (the token is never shown)
and sends nothing, not even the user-id lookup.

## Set up

1. In the Meta developer dashboard create an app with the **Instagram API with
   Instagram Login** product and add your Instagram professional (business or
   creator) account as a tester or live user.
2. Generate a **long-lived access token** for that account with the permissions
   `instagram_business_basic`, `instagram_business_content_publish` and
   `instagram_business_manage_comments`. (Mimir requests exactly these scopes.)
3. `instagram init` adds an `[instagram]` table to the shared file,
   `~/.config/agent-tools/config.toml` (mode 0600; see
   [docs/configuration.md](../../docs/configuration.md)). Paste the token in:

   ```toml
   [instagram]
   default = "main"

   [instagram.accounts.main]
   access_token = "..."     # or access_token_env / access_token_command
   # user_id = "17841400000000000"   # optional, see below
   # api_version = "v25.0"           # optional
   ```

   A second account is another `[instagram.accounts.NAME]` table; pick it with
   `--account NAME`.
4. `instagram check` calls `GET /me` and prints the account's user id and username.

**The user id** is looked up with the token (`GET /me?fields=user_id,username`) on
each posting run, the same call Mimir uses when it connects an account. Set
`user_id` to skip that call or to override it. Commenting and replying never need it.

**Tokens expire.** A long-lived token lasts about 60 days. This first version has
**no refresh and no login flow**: when it expires every command fails with "the
access token is expired or invalid", and you generate a new token and paste it into
the config. (Mimir refreshes tokens in the background; that is not included yet.)

## Images: a public URL only

Instagram fetches the image itself, so `--image-url` must be a public URL of a
JPEG. A local file cannot be given. Upload it somewhere public first, for example
with the `wharf` tool:

```bash
wharf copy photo.jpg          # prints the public URL
instagram post --caption "..." --image-url https://your-site.example/photo.jpg
```

The tool refuses a URL that is not http(s) or that ends in a non-JPEG extension
(`.png`, `.gif`, `.webp`, ...). It cannot check what the URL actually serves;
Instagram rejects a wrong or unreachable image and that message is shown.

## Commands

| Command | Does |
| --- | --- |
| `post --caption TEXT --image-url URL` | publish a photo; prints `id` (the media id), `container_id`, `account`. Caption 1 to 2200 characters |
| `comment --media-id ID --message TEXT` | new top-level comment on one of your media; prints the comment `id`. Message 1 to 2200 characters |
| `reply --comment-id ID --message TEXT` | reply to a comment; prints the reply `id`. Message 1 to 2200 characters |
| `endpoints [NAME]` | list endpoints, or show one's parameters |
| `call ENDPOINT --param k=v` | call any endpoint and print its JSON |
| `check`, `accounts`, `init`, `where` | setup helpers |

Ids must be numeric, at most 64 characters. Text must not be empty.

### How `post` publishes

1. `GET /me` for the user id (skipped when `user_id` is set).
2. `POST /{user_id}/media` with `image_url` and `caption`: creates a media container.
3. `POST /{user_id}/media_publish` with the container's `creation_id`.

As in Mimir, the container is published straight away. Only if that fails does the
tool poll `GET /{container_id}?fields=status_code`, up to 5 times, 3 seconds apart:
`IN_PROGRESS` waits, `FINISHED` publishes again, `PUBLISHED` reports that it was
already published, and `ERROR`, `EXPIRED` or an unknown status stops with that
status. A POST is never retried automatically, so a post is never published twice
by the tool's own retry.

## Endpoints

All on `https://graph.instagram.com/v25.0` (`base_url` and `api_version` settable
per account; `base_url` is what the tests point at a fake server).

| Endpoint | Request | Safety |
| --- | --- | --- |
| `me` | `GET /me?fields=user_id,username` | read |
| `container_status` | `GET /{container_id}?fields=status_code` | read |
| `container_create` | `POST /{user_id}/media` | write |
| `media_publish` | `POST /{user_id}/media_publish` | write |
| `comment_create` | `POST /{media_id}/comments` | write |
| `reply_create` | `POST /{comment_id}/replies` | write |

The token travels where Mimir sends it: in the JSON body of a POST and in the query
string of a GET. It is never a flag or a parameter. Add endpoints in
`~/.config/agent-tools/instagram/endpoints.d/*.toml` (format in the
[apikit README](../../libs/apikit/README.md#adding-an-endpoint-no-code-no-rebuild)).

## MCP

`mcp/manifests/instagram.toml` exposes `instagram_post`, `instagram_comment` and
`instagram_reply` (safety `destructive`), `instagram_read` (read-only endpoints
only) and `instagram_endpoints`. There is no generic write tool.

## Errors

The API's own message is shown with the HTTP status. Three cases are rewritten, by
Meta's numeric error code, not by text: HTTP 429 (rate limited, with Retry-After
when sent), code 190 (token expired or invalid: generate a new one), and, for
comment and reply only, code 10 (the token lacks a permission: names the scopes).

## Parity with Mimir

| Mimir | Here | Why |
| --- | --- | --- |
| `instagram_post` with `image_url`, caption up to 2200 characters, non-empty | **same** | |
| `instagram_comment` / `instagram_reply`: numeric id up to 64 characters, message 1 to 2200 characters | **same** | |
| Publish flow: create container, publish at once, on failure poll status up to 5 times 3 s apart | **same** | |
| Request shapes, host `graph.instagram.com`, version `v25.0`, token in POST body / GET query | **same** | |
| Rate limit, error message and permission-denied mapping | **same**, reworded | Messages now name the config, not Mimir's Settings screen |
| `image_path` / `artifact_id` staged on an SFTP server | **not included** | Instagram only takes a public URL; use `wharf copy` and pass `--image-url`. Intentional |
| JPEG-only check (local files) | **different** | Local files are gone; for a URL only the extension can be checked (non-JPEG extensions refused). Stricter on the URL shape (http/https) than Mimir |
| After a failed publish, always poll | **different** | A 429 or an expired token (190) returns at once: waiting cannot fix them |
| Account from the connected-accounts database; user id saved at connect time | **different** | Accounts are `[instagram.accounts.NAME]` in the config; the user id comes from `GET /me`, or `user_id` |
| Local token-expiry check before each call | **different** | The token's age is unknown; an expired token is reported from the API's error (code 190) |
| OAuth connect flow, background token refresh | **not included** | First version pastes a long-lived token. No refresh or login yet |
| Live human confirmation before each call, activity log of posts | **not included** | The CLI has `--dry-run`; the MCP tools are marked `destructive` so the server can require confirmation. No post log is kept |
| Optional `media_description`, `original_comment_*` shown in the confirmation | **not included** | Belongs to Mimir's confirmation UI |

## Reading a post's numbers

`instagram metrics MEDIA_ID` reads `like_count` and `comments_count`, with the media
type and permalink, from the media object, which needs no extra permission. It then
asks for the insights `views`, `reach`, `saved` and `shares`, which need the
`instagram_business_manage_insights` permission. If Meta refuses them, the counts
are still returned with `insights_error` beside them. The owner can hide likes, in
which case the field is simply absent. `posts refresh` runs it for you, and `posts`
reports on the result as views, likes, comments, shares and saves. The fields come
from Meta's IG Media and IG Media Insights references; this has never run against
the live API, and which insight metrics apply varies by media type.

## The post log

Every successful post, comment and reply is recorded in the post log (see [posts](../posts/README.md)),
and `instagram` refuses to publish the same text to the same account twice within 30 days.
`--allow-duplicate` overrides that and `--no-log` skips the record. A `--dry-run`
never records, and only warns about a repeat. If the log cannot be written, the post
still goes out and stderr says so.

## Not verified

**Nothing here has run against the live Instagram API.** There were no credentials.
The tests use a local fake server that checks the requests as Mimir's code (which
says its request shapes were verified live) and the Graph API documentation
describe them. Not confirmed live: that the API accepts the token inside a JSON
body on every call exactly as Mimir sends it; the `GET /me` answer shape
`{"data":[{"user_id","username"}]}` for a token generated by hand (Mimir only saw it
right after its own OAuth login); error code 190 for an expired token (from Meta's
published error list, not from Mimir); and the wording and codes of real errors.
Not covered at all: carousels, video, reels and stories, scheduling, deleting or
hiding comments, listing media or comments, and token refresh.
