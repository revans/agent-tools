# wharf

`wharf` is a small SFTP deployment tool for static sites. It knows one thing: how a local site repo's file layout maps onto the remote server's file layout, and it uses that to explore, upload, sync, move, archive, and (carefully) delete.

It is a self-contained Go module in the `agent-tools` repo. It is run *against* a site repo: it finds the site's root by looking upward for a `.wharf` config file, so the tool's source and the sites it deploys live in separate places. Everything below that says "the repo root" means the root of the **site** being deployed, and the runtime paths `scripts/wharf/log/` and `scripts/wharf/backups/` are created inside that site repo (add them to its `.gitignore`).

## Setup

1. Copy the config template and fill in real values:

   ```
   cp .wharf.example .wharf
   ```

   Edit `.wharf` (TOML) at the repo root:

   ```toml
   host = "sftp.example.com"
   port = 22
   username = "your-username"
   password = "your-password"
   remote_base_path = "/home/your-username/public_html"
   ```

   `remote_base_path` is the directory on the server that corresponds to this repo's root — every local path is mirrored under it. There's no default; it's a required field because assuming `/` would be unsafe.

   `.wharf` contains a plaintext password and is gitignored — it must never be committed. `.wharf.example` has no real credentials and is safe to commit.

2. Build the binary from the `agent-tools` repo root (or `wharf/`):

   ```
   make wharf            # builds target/release/wharf
   make install-wharf    # copies it to $(PREFIX)/bin (default ~/.cargo/bin)
   ```

   Or directly: `cd wharf && go build -o ../target/release/wharf .`

   `wharf` finds the repo root itself by searching upward from your current directory for a `.wharf` file (the same way `git` finds `.git`), so you can run it from the site's repo root or anywhere else inside the checkout.

## Commands

### `wharf explore`

Connects to the SFTP server, walks the entire remote directory tree from `remote_base_path`, and writes `WHARF.md` at the **repo root** (not inside `scripts/wharf/`) — a full directory tree, per-directory file counts, and a complete file listing, so anyone (or any LLM) working on the site can answer "where does X go on the server" just by reading that file. Re-running `explore` overwrites `WHARF.md` cleanly.

### `wharf copy <local-file>`

Uploads a single local file. The destination is the file's path relative to the repo root, mirrored onto `remote_base_path` (e.g. `assets/foo.png` → `<remote_base_path>/assets/foo.png`). Remote parent directories are created automatically.

If a file already exists at the destination, it's backed up first — see **Pre-overwrite backups** below.

Flags:
- `--dry-run` — resolve and print the source/destination without connecting to the server at all (copy's destination is fully determined locally, so no connection is needed to preview it).
- `--no-backup` — skip the automatic pre-overwrite backup.

### `wharf sync`

Compares the local repo (filtered through `.wharfignore`) against the remote SFTP contents, and reports which local files are **new or changed**:

- missing remotely entirely, or
- present remotely but with a different **size**, or
- present remotely but with a **modification time** that differs from the local file's by more than a couple of seconds.

**Diffing tradeoff:** size differences are a fully reliable signal. Modification-time comparison is best-effort — it compares the local machine's clock against whatever mtime the SFTP server reports, and those clocks are not guaranteed to agree. A small tolerance (2 seconds) absorbs normal clock/timestamp-precision noise, but if your local and remote clocks are meaningfully out of sync, mtime comparisons can be wrong in either direction. Size is the check you can fully trust; mtime is a convenience layered on top of it, not a replacement.

You're then prompted:

```
Upload (a)ll, (s)elect individual files, or (q)uit? [a/s/q]:
```

Choosing `s` lets you enter comma-separated numbers (e.g. `1,3,4`).

Flags:
- `--dry-run` — still connects and computes the full diff (that's the point of sync), but stops before the interactive prompt/upload step. Nothing is written anywhere.
- `--no-backup` — skip the automatic pre-overwrite backup for every file uploaded in this run.

### `wharf deploy`

Runs `explore` (refreshing `WHARF.md`) followed by `sync`, in one step — the common "make the live site match my local repo" workflow. `explore` has no destructive side effect, so `--dry-run` only changes `sync`'s half of the run. `--no-backup` is passed through to the sync step too.

### `wharf delete <remote-path>`

**Read this section before running this command.** It is the only command in `wharf` that destroys data on the server, and it has no flag that skips its safety checks.

`<remote-path>` is interpreted the same way `WHARF.md` lists paths: relative to `remote_base_path`, not an absolute server path.

What it does, in order:

1. Connects and inspects the target. For a file: its size. For a directory: walks it fully and shows a **complete blast radius** — file count, subdirectory count, total bytes, and the full list of contents — before asking anything.
2. Asks for explicit confirmation: `Delete <path>? This cannot be undone. [y/N]`. Only a literal `y` or `yes` proceeds; a blank answer, `n`, or anything else aborts. There is no flag to bypass this.
3. If confirmed, asks `Back up before deleting? [Y/n]` (default yes). If accepted, downloads the file (or, for a directory, every file inside it, recursively) into `scripts/wharf/backups/<timestamp>-<flattened-remote-path>/` **before** anything is deleted.
4. Deletes bottom-up: for a directory, every file and nested directory is removed before its parent (SFTP servers generally require a directory to be empty before it can be removed).
5. Refuses outright to delete `remote_base_path` itself — you must target something inside it.

`--dry-run` still connects and shows the full blast radius (so the preview is accurate), but stops before the confirmation prompt, backup, and delete.

Everything about a delete run — the resolved target, the full blast-radius listing, the confirmation answer, whether a backup was made and its exact local path, and the full list of paths actually removed (or the exact error if something failed partway through) — is written to that run's log file. If a `wharf delete` run ever needs to be reconstructed after the fact, the log is the source of truth; you should never need to re-run it to find out what happened.

### `wharf move <from> <to>`

Renames or moves a remote file or directory. Both paths are relative to `remote_base_path`, like `WHARF.md` and `delete`. It is a server-side rename: no file data is transferred, so it is fast, and it is undone by swapping the arguments.

- Never overwrites. If `<to>` already exists it refuses; give the full new path (this is not `mv file dir/`).
- Creates missing parent directories of `<to>`.
- Refuses to move `remote_base_path` itself, to leave it, or to move a directory into itself.
- Asks `Move it? [y/N]` before acting. `--dry-run` connects and shows the plan only.

### `wharf archive [--keep <name>]...`

Moves everything at the top level of `remote_base_path` into `archive/<YYYYMMDD-HHMMSS>/`, clearing the root so a new version of the site can be deployed without losing the old one. Nothing is deleted or transferred; every item is a server-side rename. Each run gets its own timestamped folder, so archiving again never collides with an earlier archive.

Left in place automatically: the `archive/` directory itself and hidden entries (names starting with `.`, such as `.well-known`, `.htaccess`, or provider diagnostics), since those are usually host infrastructure. Use `--keep <name>` (repeatable) to leave anything else, e.g. `--keep robots.txt --keep sitemap.xml`.

It shows the full list of what will move and what stays, then asks for confirmation. `--dry-run` connects and prints that plan without changing anything. If a rename fails partway, it stops and reports how many items had already moved; the run log has every rename, and `wharf move archive/<timestamp>/<name> <name>` restores one item.

Typical site-redesign flow: `wharf archive --keep robots.txt`, build the new site, then `wharf deploy`.

## Pre-overwrite backups (`copy` / `sync` / `deploy`)

**Modification times:** after each upload, `wharf` sets the remote file's modification time to the local file's, so the next `sync` sees matching size and mtime and skips unchanged files. If a server refuses this, the upload still succeeds with a warning, and that file will show as changed on the next sync. Files uploaded by older versions of wharf carry the upload time instead, so the first sync after upgrading re-uploads them once.

Before `copy` or `sync` overwrite a remote file that already exists, they download the current remote version first, into `scripts/wharf/backups/<timestamp>-<flattened-remote-path>/`. This is **on by default**; pass `--no-backup` to skip it (e.g. for very large files, or when you deliberately don't want the disk usage). A brand-new upload (nothing exists at the destination yet) has nothing to back up, so no backup is made in that case regardless of the flag.

**Retention:** the 5 most recent backups are kept per distinct remote path; once a 6th backup is made for the same remote path, the oldest is deleted automatically. This is hardcoded for v1, not configurable.

`scripts/wharf/backups/` is created automatically inside the site repo. Add it to that repo's `.gitignore` (backups can contain arbitrary site content and are runtime output, not source).

## `.wharfignore`

Controls which local files `wharf sync` (and `wharf deploy`'s sync step) even considers as deploy candidates. Lives at the repo root, gitignore-style: one pattern per line, blank lines and `#` comments ignored, `!` negates a pattern, a trailing `/` restricts a pattern to directories, a pattern containing a `/` (other than a trailing one) is anchored to the repo root, `*`/`?`/`[...]` behave as in a single path segment. Double-star (`**`) globs are **not** supported — not needed for this repo's ignore list, and not worth the complexity for v1.

A starting template is checked in, excluding `scripts/`, `.git/`, `.claude/`, `CLAUDE.md`, `docs/`, `specs/`, `log/`, wharf's own config/output files, and common OS/editor cruft.

## Logging

Every `wharf` command writes a **new timestamped log file per run** to `scripts/wharf/log/`, named:

```
wharf-<command>-<YYYYMMDD-HHMMSS>.log
```

(one file per run, not a single growing file that gets appended to forever). Each log records a timestamp, which command ran, every step taken (every file walked during `explore`, every file uploaded during `copy`/`sync`, every connection attempt, every file backed up or deleted), and full error detail on any failure — written so an LLM (or a human) reading the log afterward can understand exactly what happened without needing to re-run anything.

**To find the most recent run's log:** sort the log directory by filename (the timestamp format sorts lexically the same as chronologically) or by modification time, and take the last one — e.g. `ls -t scripts/wharf/log/*.log | head -1`.

`scripts/wharf/log/` is created automatically inside the site repo. Add `scripts/wharf/log/*.log` to that repo's `.gitignore` (logs are runtime output, not source).

## Progress output

For any step that touches multiple files or can take a noticeable amount of time — walking a large remote tree (`explore`, and the diff/blast-radius walks inside `sync`/`delete`), uploading multiple files (`sync`, and therefore `deploy`), or backing up/deleting a directory's worth of files (`delete`) — `wharf` prints simple progress to stdout (`Uploading 3/47: assets/foo.png`, etc.) so it's never silent for long stretches. This is purely visual: it has no effect on what gets written to the log file, which always records every individual operation regardless of what's shown on screen.

## Security notes

- Host key verification is intentionally **not** performed (`ssh.InsecureIgnoreHostKey`). `wharf` targets a single known personal deployment target with no existing `known_hosts` management, and building that out was judged out of scope for v1. This is a deliberate tradeoff: it means `wharf` won't detect a man-in-the-middle on the SFTP connection. If that ever matters, it's the first thing to add.
- Password auth only, matching `.wharf`'s config shape. Key-based auth was not added since it wasn't the actual requirement.

## Development

```
cd wharf
go build ./...
go vet ./...
go test ./...
```

Pure logic (`.wharf` TOML parsing, repo-root discovery, local↔remote path mirroring, `.wharfignore` pattern matching) has unit test coverage in `internal/config`, `internal/pathmap`, and `internal/ignore`. SFTP/SSH connection handling and the interactive command flows are not unit tested — mocking a real SFTP server was judged not worth it for v1; the dry-run flags exist in part to make manual verification of the connected commands' logic possible without touching a real server.
