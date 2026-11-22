# Changelog

## Unreleased (milestone 4: projects, volumes, reserved IPs, load balancers, Spaces)

- `docli project list|get|create|delete|assign`. Projects are stored as `docli-NAME` (they cannot be tagged). `delete`
  refuses a project that still holds resources (never overridable), the default project, and projects docli did not
  create. `assign PROJECT KIND:NAME...` moves resources and says which project each is in now. A `--project NAME`
  flag on `droplet create`, `db create`, `firewall create`, `domain create`, `volume create`, `reserved-ip create` and
  `lb create` looks the project up before anything is created and puts the new resource in it afterwards; if that last
  call fails the error says the resource exists and gives the `docli project assign` command. This replaces "there is
  no --project flag yet".
- `docli volume list|get|create|delete|attach|detach|resize`. Tagged `docli:managed` (and `docli:stack:NAME`), tags
  created first. Resize only grows (smaller is refused, the API cannot shrink a volume). Delete needs the name typed and
  refuses an attached volume; attach refuses a volume on another droplet or in another region; detach warns to unmount.
- `docli reserved-ip list|get|create|delete|assign|unassign`. A reserved IP has no name and no tag, so **the manifest
  is the authority** (like DNS): commands refuse an address it does not list unless `--force-unmanaged` and the typed
  address. `assign` shows what the address is on now and that moving it interrupts that droplet. `delete` refuses an
  assigned address and needs the address typed.
- `docli lb list|get|create|delete`: forwarding rules (`http:80=http:8080[,cert=ID][,passthrough]`), health check,
  droplets or a tag as targets, a VPC, `--wait`. Stored as `docli-NAME` (the API takes a load balancer's own tags only
  at creation, so docli does not rely on them). Delete needs the name typed. No price is shown: the API reports none.
- `docli spaces key list|create|delete` over the REST API (`/v2/spaces/keys`); DigitalOcean's docs disagree about
  whether it works and docli says so on a 401/403/404. `create` requires `--secret-file` (the secret is shown once),
  writes it with mode 0600 and never prints it; grants are explicit (`--grant BUCKET:PERM` or `--full-access`).
- `docli spaces bucket list|create|delete` over the S3-compatible API, because the REST API has no buckets: AWS
  Signature Version 4 with the standard library only (no SDK), four calls, a Spaces access key from
  `DOCLI_SPACES_ACCESS_KEY`/`DOCLI_SPACES_SECRET_KEY` or `~/.config/docli/spaces` (0600). Stored as `docli-NAME`;
  delete refuses a bucket that is not empty and needs the name typed.
- Scrubbing: a Spaces secret is registered the moment it is read or created, and removed from every message even in
  commands that never use it; `Signature=...` and `secret_key=...` shapes are removed even for a secret docli never saw.
- Manifest: new sections `projects`, `volumes`, `reserved_ips`, `load_balancers`, `spaces_buckets`, `spaces_keys`, with
  `verify` drift detection and `refresh` (reserved IPs and buckets are only updated when already listed; bucket checks
  need a Spaces key and are otherwise `unverifiable`). A Spaces key's entry has the access key id, never the secret.
- Stack file: `projects`, `volumes` and `load_balancers` with plan, apply, adopt and prune. Apply order: projects, SSH
  keys, VPCs, firewalls, droplets, volumes, load balancers, databases, DNS; prune in reverse (projects last). Volumes
  need `--prune-data` and their name typed; a load balancer's name is typed; an attached volume and a project that still
  holds resources are not pruned. Load balancers are never changed in place (docli has no update). Reserved IPs and
  Spaces are not stack file kinds and the file says why. `docli adopt volume` and `docli adopt reserved-ip` are new.
- Unchanged: `token check` still probes the same resources, the manifest header text and the shipped example stack file
  (both pinned by tests).

## Unreleased (milestone 3: stack file, plan, apply, adopt)

- `docli stack init` writes a commented example stack file (default `config/deploys/stack.yml`, found from the
  project root; `--file PATH`); `docli stack validate` checks a file alone, with no token and no network.
- The stack file declares `ssh_keys`, `vpcs`, `firewalls`, `droplets`, `domains` (with DNS records, an A record can
  take a droplet's address) and `databases` (with trusted droplets, tags and addresses, users and databases),
  referring to each other by name. Parsing is strict: unknown keys, duplicate keys, aliases, keys that look like
  secrets, values that look like secrets and `$VAR`-style references are errors with line numbers; nothing is read
  from the environment. A database must have at least one trusted source and cannot be open.
- `docli plan` reads the file and the account (GET only) and prints what would be created, changed, left alone,
  "needs replacement, not done", "orphaned, not deleted" or "not docli's, left alone", with the added monthly cost.
  It never writes, not even the manifest. Exit `0` no changes, `1` error, `2` changes (so plan's own usage mistakes
  exit `1`); `--json` is pinned by golden files.
- `docli apply` runs the plan, asks (default no; `--yes`, `--dry-run`), then applies in a fixed order (SSH keys, VPCs,
  firewalls, droplets and firewall attachment, databases with resize, trusted sources, users and databases, then
  DNS), waits for droplets and, only when needed, databases, updates the manifest after each step, stops at the
  first failure with an exact done / failed / not done report, never retries a create, and is a no-op when run
  again. Droplet region, image and size and database engine, version, region and VPC changes are never done.
- `--prune` (plan and apply) is the only way anything is deleted, and only for what docli created and this
  project's manifest lists. It needs the typed word `prune`, each droplet's, database's and domain's name typed, and
  `--prune-data` for droplets and databases; a VPC with members is not pruned; ownership is re-checked at deletion.
  Users and databases inside a cluster are never pruned.
- `docli adopt droplet|database|domain|record` puts an existing resource under docli's management: droplets and
  clusters get the `docli:managed` tag (nothing else changes), domains and records are only recorded in the
  manifest. VPCs, firewalls and SSH keys are refused (adopting would mean renaming them).
- `Cloud` gained `TagResource`; `Approve` shares its y/N question with apply (`askProceed`); `main.go` gained an
  exit-code error so plan can exit `2` without printing an error.
- Tests: `stack_test.go` and golden files for plan (empty, create-all, in-sync, partial, drift, orphaned, prune),
  apply (full, nothing, failed, dry run), adopt and validate; thirteen mutation checks by hand. The real API has
  not been called.

## Unreleased (milestone 2: DNS and managed PostgreSQL)

- `domain list|get|create|delete` and `dns list|create|update|delete` for A, AAAA, CNAME, TXT, MX, NS and CAA records,
  with names relative to the domain (`@` is the apex), a default TTL of 1800, type-specific validation and conflict
  checks (duplicates, CNAME sharing a name).
- DNS ownership: domains and records cannot be tagged, so the manifest's new `domains:` section is the authority for
  them. `domain delete`, `dns update` and `dns delete` refuse anything the manifest does not list as created by
  docli unless `--force-unmanaged` and the typed name; `domain delete` always needs the typed domain name.
- `db list|get|create|delete|resize|wait|connection|backups list`, `db user|database|pool list|create|delete` and
  `db firewall list|set` for managed PostgreSQL. Create needs version, size, region, nodes and a VPC, checks them
  against DigitalOcean's options, shows the monthly cost from the size list (or says it is unknown), supports
  `--wait`, tags the cluster `docli:managed`, and refuses to leave it open (no trusted source, or `0.0.0.0/0`)
  without `--allow-open` and the typed name. Delete needs the typed name.
- Passwords are never printed, put in JSON, logged, written to the manifest or left in an error (known passwords,
  connection strings and the `AVNS_` shape are scrubbed). `--password-file PATH` writes one to a new 0600 file and
  will not overwrite without `--force`.
- Manifest: `domains:` (with the records docli created) and `databases:` (host, port, trusted sources; no secrets)
  through `ManifestEntry`; `verify` detects drift for both (record by record for domains) and reports sections the
  token cannot read as `unverifiable`; `refresh` updates domain entries but never adds one. The manifest header now
  says DNS is the exception to "the manifest is not the authority".
- `token check` also probes `domains` and `databases`. `deleteSpec` gained a `Verb` so an update can use the same
  ownership guard as a delete.
- godo cannot page database lists, so a full page of 200 is refused as possibly incomplete.
- Tests: fake domain/record/database service, end-to-end tests, ownership refusals, password and token leak tests
  over many failure modes, golden JSON and manifest tests; seven safety rules were mutation-checked by hand. The real API has not been called.

## Unreleased (milestone 1)

- Foundation: `version`, `help`, `token check`; global flags `--json`, `--yes`, `--dry-run`, `--region`, `--timeout`,
  `--debug`; exit codes 0/1/2/3; paged lists; retries with backoff for reads only; `--wait` for droplets.
- Token from `DOCLI_TOKEN`, `DIGITALOCEAN_TOKEN` or a mode-0600 `~/.config/docli/token`; never a flag, never printed.
- Read commands: `sizes`, `regions`, `images`.
- `ssh-key`, `vpc`, `firewall` (readable rule specs) and `droplet` commands, each with plan, confirmation and
  `--dry-run`; droplet create shows the estimated monthly cost.
- Ownership: `docli:managed` and `docli:stack:NAME` tags on droplets; `docli-` name prefix for SSH keys, VPCs and
  firewalls; deletes refuse anything else unless `--force-unmanaged` and the name is typed.
- The manifest `config/deploys/manifest.yml` (project root found by walking up from the working directory):
  written atomically after successful changes, with `manifest show`, `verify` and `refresh`; `--manifest PATH` and
  `--no-manifest`. It is a record; ownership still comes from the account.
- Makefile (`build`, `test`, `install`, `dist`, `clean`) for six platforms with SHA256SUMS.
- Tests run against a fake DigitalOcean server only. The real API has not been called.
