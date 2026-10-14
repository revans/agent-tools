---
name: wharf-deploy
description: Deploy a static website to an SFTP server with the `wharf` CLI. Use whenever the user wants to deploy, publish, upload, or sync a site to its server over SFTP, see what is on the server, rename or move files on the server, archive the current live site before uploading a redesign, or delete remote files. Also use to answer "what would a deploy change" with a dry run.
allowed-tools: Bash, Read, Glob
---

# Deploying with `wharf`

`wharf` mirrors a local site repo onto an SFTP server. It finds the site root by
searching upward from the current directory for a `.wharf` TOML file (host,
port, username, password, `remote_base_path`). Run it from anywhere inside the
site checkout. Never print or echo the `.wharf` file: it holds a plaintext
password.

## Commands

| Command | What it does |
| --- | --- |
| `wharf explore` | Walk the server and write `WHARF.md` (read-only on the server) |
| `wharf sync` | List new/changed local files, then prompt to upload |
| `wharf deploy` | `explore` then `sync` |
| `wharf copy <file>` | Upload one file, mirroring its repo-relative path |
| `wharf move <from> <to>` | Rename on the server; never overwrites |
| `wharf archive [--keep name]...` | Move the site root's contents into `archive/<timestamp>/` |
| `wharf delete <path>` | Delete a remote file or directory (confirms, offers backup) |
| `wharf pull <path> [local]` | Download a remote file/dir; never overwrites without `--force` |
| `wharf cat <file>` | Print a remote file to stdout |
| `wharf diff <local-file>` | Unified diff of the server's copy vs the local file |
| `wharf mkdir <path>` | Create a remote directory |
| `wharf chmod [-r] <mode> <path>` | Change remote permissions (`644`, `755`) |
| `wharf prune` | List remote files with no local counterpart; never deletes |
| `wharf restore <path>` | Re-upload from local pre-overwrite backups (`--list`, `--from`) |
| `wharf verify` | HTTP-check every deployed file's public URL |

Remote paths are relative to `remote_base_path`. `--dry-run` works on all of
them and changes nothing.

## Working rules

1. **Dry run first.** Before any `deploy`, `sync`, `move`, `archive` or
   `delete`, run it with `--dry-run` and show the user the list.
2. **Uploading is interactive.** `sync`/`deploy` ask `Upload (a)ll, (s)elect, or
   (q)uit?`. When the user has approved the dry-run list, answer with
   `echo a | wharf deploy`.
3. **Sync skips unchanged files** by size and modification time. If everything
   shows as changed on the first run after an upgrade, that is expected once.
4. **Redesign flow:** `wharf archive --keep robots.txt --keep sitemap.xml`,
   build the new site, then `wharf deploy`. Archives are timestamped, so a
   second archive never collides with the first.
5. **Hidden entries stay put.** `archive` never moves names starting with `.`
   (`.well-known`, `.htaccess`) or the `archive/` directory itself.
6. **Confirm destructive steps with the user.** `archive` and `delete` change
   the live site; do not pass them an unprompted `y`.
7. **Restore one item** from an archive with
   `wharf move archive/<timestamp>/<name> <name>`.
8. **After every deploy, run `wharf verify`.** A successful upload does not
   prove the site works; verify requests each URL and fails on any non-2xx.
9. **Before deploying a file, `wharf diff` shows what would change.** Use
   `wharf prune` to find stale files, then `wharf delete` each one the user
   approves; prune itself never deletes.
10. **Server-only files** (robots.txt, favicons) should be brought into the
    repo with `wharf pull <path>` so they are versioned and redeployed.
11. **Undo a bad upload** with `wharf restore <path>` (newest local backup),
    or `wharf restore --list <path>` to choose one.

## Reading results

Each run writes a log to `scripts/wharf/log/wharf-<command>-<timestamp>.log`
inside the site repo. Read the newest one to see exactly what happened.
