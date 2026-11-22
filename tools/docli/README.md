# docli

A command-line tool for DigitalOcean that does the safe version of the job: droplets, VPCs, firewalls, SSH keys,
DNS, managed PostgreSQL, projects, block storage volumes, reserved IPs, load balancers and Spaces, with a plan you
read before anything changes, a cost shown before anything is created (where the API reports one), and a rule that
it only deletes what it made itself. Built on [godo](https://github.com/digitalocean/godo), the
official Go client. One static binary. This is milestone 4: the remaining services (see "What is not here yet").

> **Status: not yet run against the real DigitalOcean API.** Every test uses a fake DigitalOcean server that
> lives in the test code. Nothing in this repository has called the real API, read a real token, or created a
> real resource. Run `docli token check` and a `--dry-run` first, then try it on a throwaway project.

> **Milestones 2 (DNS and PostgreSQL), 3 (stack file, plan, apply, adopt) and 4 (projects, volumes, reserved IPs,
> load balancers, Spaces) are new and have the same status.** Several details of the real API are *not verified*;
> the list is in "Not verified against the real API" below. Spaces **buckets** go through the S3-compatible API with
> a signing code written for docli, and nothing has yet shown that the real Spaces service accepts it. Read the
> list before you point docli at an account that matters.

## Install

```sh
make install            # builds and copies docli to ~/.local/bin (needs Go)
make dist               # release archives for linux, macOS, windows (amd64, arm64) and SHA256SUMS in dist/
docli version
```

The version comes from the nearest git tag `v*` (`v0.1.0` becomes `0.1.0`), or `dev` outside a tagged checkout.

## The token

docli reads the token from, in this order:

1. the environment variable `DOCLI_TOKEN`
2. the environment variable `DIGITALOCEAN_TOKEN`
3. the file `~/.config/docli/token`, which must be readable only by you (`chmod 600`). A file with looser
   permissions is refused, with the command to fix it.

It is never accepted as a flag (so it cannot land in shell history), never printed, never logged, and is removed
from any error text before it is shown, even if the server echoes it back. `--debug` prints each request's method,
URL and status, never headers or bodies. `DOCLI_TOKEN` wins over `DIGITALOCEAN_TOKEN` so you can keep a separate
token for docli (for example a least-privilege one).

```sh
docli token check
```

Checks the token against the account and reports which resources it can read (it tries one small read of each).
DigitalOcean offers no way to list a token's scopes, and docli will not change anything to find out what a token
may write, so write access is reported as "unknown". It says so plainly.

## Safety rules

- **Every change shows a plan and waits.** Creating, changing or deleting prints what will happen, then asks
  `Proceed? [y/N]`. The default is no. `--yes` skips the question. If stdin is not a terminal and `--yes` is
  missing, docli refuses (exit 3); it never assumes yes.
- **`--dry-run`** on every mutating command prints the plan and changes nothing. Create prints the cost; firewall
  commands print the full resulting rule list.
- **Deleting a droplet means typing its name** (or `--yes --confirm-name NAME`). `--yes` alone is not enough.
- **Ownership.** docli tags what it creates with `docli:managed` (and `docli:stack:NAME` with `--stack`); missing
  tags are created first. `delete` refuses anything it did not create, says why, and changes nothing, unless you
  pass `--force-unmanaged` **and** type the name.
  - Droplets carry the tag `docli:managed`.
  - SSH keys, VPCs and firewalls cannot be tagged in DigitalOcean, so the rule there is a name prefix: docli
    stores them as `docli-NAME` and only deletes names that start with `docli-`.
- **DNS ownership comes from the manifest.** DigitalOcean cannot tag domains or records, so docli remembers what
  it created in `config/deploys/manifest.yml` and, for domains and records only, treats that file as the authority:
  `domain delete`, `dns update` and `dns delete` refuse anything the file does not list as created by docli,
  unless you pass `--force-unmanaged` **and** type the name. `domain delete` always needs the domain name typed,
  because it removes every record in the domain. See "DNS ownership: the choice and its limit".
- **Database clusters are tagged** `docli:managed` (and `docli:stack:NAME`). `db delete` needs the cluster name
  typed; `db user|database|pool delete` on a cluster docli did not create is refused like any other delete.
- **A database is never created or left open by accident.** `db create` needs a VPC and at least one trusted
  source (droplet, tag or IP). With none, or with `0.0.0.0/0`, docli refuses unless you pass `--allow-open` and
  type the cluster name. `db firewall set` follows the same rule and refuses an empty list.
- **Passwords are never shown.** Not on screen, not in `--json`, not in `--debug`, not in the manifest, not in an
  error even if the server echoes one. The only place one goes is a file you name with `--password-file`, created
  with mode 0600; docli will not overwrite an existing file without `--force`, and checks that before it creates
  anything. (Details in "PostgreSQL".)
- **Milestone 4 ownership.** Volumes are tagged `docli:managed` like droplets. Projects, load balancers, Spaces keys
  and Spaces buckets cannot be tagged (or, for load balancers, only at creation), so they are stored as `docli-NAME`
  and only `docli-` names are deleted. **Reserved IPs have neither a name nor a tag**, so the manifest is the
  authority for them, as it is for DNS (see "Reserved IPs"). Each refuses what docli did not create unless you pass
  `--force-unmanaged` and type the name.
- **Deleting a volume, load balancer, bucket, Spaces key or reserved IP means typing its name** (or the address), like
  a droplet. A volume that is attached, a project that still holds resources, a reserved IP that is assigned and a
  bucket that is not empty are refused outright: `--force-unmanaged` never overrides those.
- **A Spaces secret key is a credential like the token.** It is read from the environment or a 0600 file, never from a
  flag, and never printed, logged, put in `--json`, the manifest or an error. A key docli creates has its secret
  written only to the file named with `--secret-file` (mode 0600, no overwrite without `--force`, checked before
  anything is created).
- **Retries.** Reads (GET) are retried on 429 and 5xx with exponential backoff, honouring `Retry-After`. A create
  or any other write is never retried automatically.
- **A stack file never deletes anything unless you say `--prune`.** `docli apply` creates and changes. What docli
  created and the file no longer lists is shown as "orphaned, not deleted". Deleting needs `--prune`, the typed word
  `prune`, each droplet's, database's and domain's name typed, and `--prune-data` for droplets and databases; and it
  only ever covers things docli created that this project's manifest lists. Things docli did not create are only
  reported. See "Stack files: plan and apply".
- **Private keys are refused** by `ssh-key create`. User-data contents are sent to DigitalOcean but never printed or
  logged.
- DigitalOcean firewalls **deny by default**: a droplet behind a firewall accepts only what an inbound rule allows
  and sends only what an outbound rule allows. A firewall with no outbound rules blocks all outgoing traffic.
  docli warns about both cases in the plan.

Exit codes: `0` ok, `1` error, `2` usage, `3` refused (declined, or unsafe). One exception: for `docli plan`, `2`
means "there are changes" (like `diff` or `terraform plan -detailed-exitcode`), so plan's own mistakes exit `1`.

## Global flags

They can go anywhere on the line.

| Flag | Meaning |
| --- | --- |
| `--json` | machine-readable output, same fields every time (see below) |
| `--yes` | skip the y/N question |
| `--dry-run` | show the plan, change nothing |
| `--region R` | region slug; default `$DOCLI_REGION` |
| `--timeout D` | per-request timeout, default `30s` |
| `--debug` | method, URL and status of each request, to stderr |

Environment defaults: `DOCLI_REGION`, `DOCLI_VPC`, `DOCLI_SSH_KEY` (a name, id or fingerprint; comma-separated for
several). There is no config file. Spaces bucket commands need a Spaces access key: `DOCLI_SPACES_ACCESS_KEY` and
`DOCLI_SPACES_SECRET_KEY` (or `SPACES_ACCESS_KEY_ID` and `SPACES_SECRET_ACCESS_KEY`), or `~/.config/docli/spaces`
(mode 0600) with the lines `access_key=...` and `secret_key=...`.

**`--project NAME`** is a flag of every command that creates something a project can hold: `droplet create`,
`db create`, `volume create`, `reserved-ip create`, `lb create`, `firewall create` and `domain create`. The name or id
is looked up first, so a typo costs nothing. DigitalOcean puts a resource in a project with a second call after it is
created; if that call fails the resource exists, docli says so, and the error contains the `docli project assign`
command that finishes the job. The target project can be any project, not only one docli made.

## Commands

### Read

```sh
docli sizes                       # available sizes up to $100/month, cheapest first, with monthly and hourly prices
docli sizes --region sfo3 --all   # everything, including unavailable and expensive sizes
docli regions                     # regions that accept new droplets (--all for the rest)
docli images                      # distribution images (ubuntu, debian...); not the whole catalogue
docli images --type application   # or custom
docli images --slug ubuntu-24-04-x64
```

### SSH keys

```sh
docli ssh-key create --name laptop --public-key-file ~/.ssh/id_ed25519.pub     # stored as docli-laptop
docli ssh-key list
docli ssh-key get laptop            # by name (docli- is implied), id or fingerprint
docli ssh-key delete laptop
docli ssh-key delete robert-laptop --force-unmanaged --confirm-name robert-laptop --yes
```

### VPCs

```sh
docli vpc create --name net --region nyc3 --ip-range 10.50.0.0/24    # stored as docli-net
docli vpc list [--region nyc3]
docli vpc get net                   # includes its members
docli vpc delete net                # refuses, and names the members, if it still has any
```

### Firewalls

Rules are written as `DIRECTION:PROTOCOL:PORTS:TARGET`:

```
in:tcp:22:from=10.116.0.0/20          in:tcp:5432:from-tag=app          out:tcp:all:to=0.0.0.0/0
in:udp:8000-9000:from=1.2.3.4         in:icmp:all:from=0.0.0.0/0        in:tcp:443:from=0.0.0.0/0,::/0
in:tcp:80:from=10.0.0.0/8+from-tag=lb   (several kinds of target joined with +; several addresses with a comma)
```

Parsing is strict: anything it does not understand is an error that shows the format. Each rule is printed back in
plain English in the plan.

```sh
docli firewall create --name web --rule in:tcp:22:from=10.116.0.0/20 --rule in:tcp:443:from=0.0.0.0/0,::/0 \
      --rule out:tcp:all:to=0.0.0.0/0 --tag web --dry-run
docli firewall add-rule web in:tcp:5432:from-tag=app
docli firewall remove-rule web in:tcp:5432:from-tag=app
docli firewall attach web --droplet web-1 --tag web        # detach is the mirror image
docli firewall get web
docli firewall delete web
```

### Droplets

```sh
docli droplet create --name web-1 --region nyc3 --size s-1vcpu-1gb --image ubuntu-24-04-x64 \
      --vpc net --ssh-key laptop --stack blog --tag app \
      --user-data-file cloud-init.yml --backups --monitoring --ipv6 --wait
docli droplet list [--tag docli:managed] [--vpc net] [--region nyc3]
docli droplet get web-1             # includes private and public IPs
docli droplet power-off web-1       # also power-on, reboot; --wait for on/off
docli droplet delete web-1          # type the name, or: --yes --confirm-name web-1
```

Create prints the plan first: size, image, network, SSH keys, the tags it will apply, and the estimated monthly
cost taken from the sizes list the API returns (`price_monthly`, `price_hourly`). Backups are shown as an estimate
of 20% of the size price, because the API does not report that figure. With no SSH key, docli warns that
DigitalOcean will email a root password. Droplet names must be valid hostnames. `--wait` polls until the droplet is
active (`--wait-timeout`, default 5m) and prints progress to stderr; if it times out the droplet still exists and
docli says so. Use `--wait` when something reads the droplet's IP from the manifest.

### Domains and DNS

```sh
docli domain create --name shop.example          # the DNS zone only: no registration, no charge of its own
docli domain list | get shop.example
docli domain delete shop.example                 # type the name; removes ALL records in the domain

docli dns create shop.example --type A     --name www --data 203.0.113.5 --ttl 300
docli dns create shop.example --type AAAA  --name www --data 2001:db8::1
docli dns create shop.example --type CNAME --name blog --data host.example.net.
docli dns create shop.example --type TXT   --name @   --data "v=spf1 include:_spf.example.net ~all"
docli dns create shop.example --type MX    --name @   --data mail.example.net. --priority 10
docli dns create shop.example --type NS    --name sub --data ns1.example.net.
docli dns create shop.example --type CAA   --name @   --data letsencrypt.org --tag issue [--flags 0|128]
docli dns list shop.example [--type A] [--name www]
docli dns update shop.example 12345 --data 203.0.113.9 --ttl 600     # by record id (see dns list)
docli dns delete shop.example 12345
```

Names are relative to the domain: `www`, not `www.shop.example`; `@` is the apex. A full name is refused with the
name to write instead. The TTL defaults to 1800 seconds. docli checks before sending anything: A needs an IPv4
address and AAAA an IPv6 one; CNAME, MX and NS need a host name (not an IP); a CNAME cannot sit at the apex or
share a name with any other record; MX needs `--priority`; CAA needs `--tag issue|issuewild|iodef`; TXT cannot
contain line breaks. An identical record that already exists is refused. A record's type cannot be changed (delete
and create). TXT values are public DNS data and are written to the manifest like any record.

#### DNS ownership: the choice and its limit

Two ways to protect DNS were possible: (1) refuse everything not created by docli, remembering what it created in
the manifest, or (2) allow everything but ask for `--force-unmanaged` plus the typed name on every change. docli
uses (1), because it protects by default: a record is docli's only if `config/deploys/manifest.yml` lists it.
DigitalOcean's own records (the NS records of a new zone) and everything you or a colleague added by hand are
never docli's, and the `domains:` section also records which *domains* docli created (`created_by_docli`). In a
domain docli did not create, the records docli adds are tracked, but the domain stays not-docli's.

What this does **not** protect against, honestly:

- **The file is the authority.** Anyone who can edit it can claim a record. Editing the manifest cannot make docli
  delete a droplet, but it can make docli willing to delete a DNS record. Treat the file like code: review changes
  to it.
- **No file, no ownership.** On a fresh checkout, another machine, with `--no-manifest`, or after the file is lost,
  docli lists nothing as its own and refuses (the safe direction). An unreadable file is an error, not "nothing
  owned". `docli manifest refresh` cannot rebuild ownership: the account cannot say which records are docli's, so
  refresh only updates and prunes the domain entries already in the file and never adds one.
- **Someone else's change to a record docli owns is allowed.** The record id is what is remembered. If a colleague
  edits that record, docli still treats it as its own (`manifest verify` shows the drift).
- `update` is guarded like `delete`, because changing a record is as risky as removing it.

### PostgreSQL

```sh
docli db create --name blog-db --region nyc3 --version 16 --size db-s-1vcpu-1gb --nodes 1 --vpc apps \
      --trusted-droplet web-1 --stack blog --wait --password-file config/secrets/db_admin_password
docli db list | get blog-db | wait blog-db | backups list blog-db
docli db resize blog-db --size db-s-2vcpu-4gb --nodes 2 --wait
docli db delete blog-db                          # type the name, or: --yes --confirm-name blog-db

docli db connection blog-db                      # host, port, database, user, ssl mode; never the password
docli db connection blog-db --user blog --database blog_production --password-file config/secrets/db_password

docli db user list blog-db  |  db user create blog-db blog --password-file config/secrets/db_password  |  db user delete blog-db blog
docli db database list blog-db  |  db database create blog-db blog_production  |  db database delete blog-db blog_production
docli db pool list blog-db  |  db pool create blog-db --name blog_pool --database blog_production --user blog --mode transaction --size 10
docli db firewall list blog-db
docli db firewall set blog-db --droplet web-1 --tag app --ip 198.51.100.7     # REPLACES the whole list
```

**Create** needs a version, size, region, node count (1 to 3) and a VPC. It checks them against the options
DigitalOcean offers for PostgreSQL before asking, shows the plan (size, network, trusted sources, tags) and the
monthly cost taken from the API's size list (price per node times node count; storage and extras not included). If
the size list has no price for the size, the plan says the cost is **not shown** and `monthly_cost_usd` is `null`;
docli never invents a number. It tags the cluster `docli:managed`, takes `--stack`, `--tag`, `--wait` (default
timeout 20 minutes; the cluster is kept if the wait times out) and is never retried. `db wait` waits for `online`.

**Trusted sources** are the droplets, tags and IP addresses allowed to connect: `--trusted-droplet NAME|ID`,
`--trusted-tag TAG`, `--trusted-ip ADDR|CIDR` at create, and `--droplet`, `--tag`, `--ip` for `db firewall set`.
`set` shows what is added, removed and kept, then replaces the list; an unchanged list sends nothing. An empty
list, or `0.0.0.0/0` / `::/0`, is refused unless `--allow-open` and the cluster name is typed
(`--yes --confirm-name NAME` unattended).

**Passwords.** DigitalOcean returns database passwords in its answers; docli never prints them. `--password-file
PATH` (on `db create`, `db connection` and `db user create`) writes the password, exactly as given with no trailing
newline, to a **new** file with mode 0600. It refuses to overwrite an existing file unless you pass `--force`, and
checks this before `db create` spends anything. If the write fails after the cluster exists, the error says how to
fetch the password again (`db connection NAME --password-file PATH`). Keep the file out of git. `db connection`
without the flag prints `Password: not shown`. Doadmin and `defaultdb` can never be deleted by docli.

**Sizes and pagination.** godo cannot page database lists. docli asks for 200 items; if it gets a full page of 200
it refuses to continue ("may be incomplete") rather than act on a partial list.

#### PostgreSQL for a Kamal app

A droplet and a database on the same private network, the database reachable only from that droplet, passwords in
files that Kamal reads and git never sees. (docli was not run against a real account for this; the Kamal secrets
syntax is the documented `$(cat file)` form and was not executed here.)

```sh
# 1. a private network, a droplet inside it (tagged docli:managed, stack "blog")
docli vpc create --name apps --region nyc3
docli droplet create --name web-1 --region nyc3 --size s-1vcpu-1gb --image ubuntu-24-04-x64 \
      --vpc apps --ssh-key laptop --stack blog --tag app --wait

# 2. the database in the same VPC; only web-1 may connect; the admin password goes to a 0600 file
mkdir -p config/secrets && echo "config/secrets/" >> .gitignore
docli db create --name blog-db --region nyc3 --version 16 --size db-s-1vcpu-1gb --nodes 1 --vpc apps \
      --trusted-droplet web-1 --stack blog --wait --password-file config/secrets/db_admin_password

# 3. an application database and user (not doadmin); this password goes to a file too
docli db database create blog-db blog_production
docli db user create blog-db blog --password-file config/secrets/db_password

# 4. what to put in config/deploy.yml (host from the manifest, never a password)
docli db connection blog-db            # host, private host, port 25060, database, user, ssl mode require
```

```sh
# .kamal/secrets   (Kamal runs this as a shell script; the password never appears in deploy.yml)
DB_PASSWORD=$(cat config/secrets/db_password)
```

```yaml
# config/deploy.yml
<%
  manifest = YAML.load_file(File.join("config", "deploys", "manifest.yml"))
  web = manifest.dig("resources", "droplets").find { |d| d["name"] == "web-1" }
  db  = manifest.dig("resources", "databases").find { |d| d["name"] == "blog-db" }
-%>
service: blog
servers:
  web:
    - <%= web["public_ipv4"] %>
env:
  clear:
    DB_HOST: <%= db["private_host"] %>     # reachable only inside the VPC
    DB_PORT: <%= db["port"] %>
    DB_NAME: blog_production
    DB_USER: blog
    DB_SSLMODE: require
  secret:
    - DB_PASSWORD
```

Later, `docli db firewall set blog-db --droplet web-1 --droplet web-2` adds a second web server (the list is
replaced, so name every source you want to keep; the plan shows what would be removed).

### Projects

```sh
docli project create --name shop --environment Production     # stored as docli-shop
docli project list | get shop
docli project assign shop droplet:web-1 volume:shop-data lb:web reserved-ip:203.0.113.10
docli project delete shop
```

A project is a folder for resources (control panel and billing views); it has no charge and changes nothing about
the resources in it. **Ownership: the name prefix `docli-`**, because a project cannot be tagged. `project delete`
refuses a project that still holds resources (it lists them, and `--force-unmanaged` does not override this), refuses
the default project, and refuses projects docli did not create unless forced with the name typed.
`project assign PROJECT KIND:NAME...` (kinds `droplet`, `volume`, `lb`, `reserved-ip`, `database`, `domain`,
`firewall`) **moves** resources, because a resource is in one project at a time: the plan says which project each one
is in now. It refuses resources docli did not create unless `--force-unmanaged` is given and the project name typed.

### Volumes

```sh
docli volume create --name shop-data --region nyc3 --size 50 [--filesystem-type ext4] [--stack shop] [--project shop]
docli volume list | get shop-data
docli volume attach shop-data web-1      docli volume detach shop-data web-1
docli volume resize shop-data --size 80
docli volume delete shop-data            # type the name
```

Volumes are tagged `docli:managed` (and `docli:stack:NAME`), like droplets; the tags are created first. **Resize only
grows**: asking for a smaller size is refused (the API cannot shrink a volume), and the plan reminds you the
filesystem is not grown for you. `delete` needs the name typed (`--yes` alone is not enough) and refuses a volume that
is attached. `attach` refuses a volume that is on another droplet or in another region; `detach` warns to unmount
first. `attach`, `detach` and `resize` on a volume docli did not create need `--force-unmanaged` and the name typed.
The API reports no block storage price, so the plan says the cost is not shown rather than guess.

### Reserved IPs

```sh
docli reserved-ip create --region nyc3            # or --droplet web-1 to assign it at once
docli reserved-ip list | get 203.0.113.10
docli reserved-ip assign 203.0.113.10 web-2       # the plan says what it is on now
docli reserved-ip unassign 203.0.113.10
docli reserved-ip delete 203.0.113.10             # type the address
```

**Ownership: the manifest** (section `reserved_ips:`), because a reserved IP has no name and no tag. This is the
DNS arrangement: `reserved-ip create` records the address, and `assign`, `unassign` and `delete` refuse an address the
manifest does not list unless `--force-unmanaged` is given and the address typed. `docli adopt reserved-ip IP` records
an existing one (nothing changes on DigitalOcean). Delete the manifest and docli no longer treats the addresses as its
own. **`assign` moves an address that may be in use**: the plan names the droplet it is on now ("this MOVES it...") and
warns that traffic to the old droplet stops. `delete` refuses an assigned address, and asks for the address typed,
because DigitalOcean cannot give the same address back. The API reports no reserved IP price.

### Load balancers

```sh
docli lb create --name web --region nyc3 --forward http:80=http:8080 --forward "https:443=http:8080,cert=CERT_ID" \
      --health-check http:8080/healthz --droplet web-1 --droplet web-2 --vpc apps [--size lb-small] [--wait]
docli lb create --name web ... --tag web          # every droplet with the tag, now and in future
docli lb list | get web
docli lb delete web                                # type the name
```

`--forward ENTRY_PROTO:PORT=TARGET_PROTO:PORT[,cert=ID][,passthrough]` (protocols `http https http2 http3 tcp udp`;
an `https`/`http2`/`http3` entry needs `cert=` or `passthrough`, so it is clear who ends TLS). `--health-check
PROTO:PORT[/PATH][,interval=10][,timeout=5][,healthy=5][,unhealthy=3]` (its port must be a target port of a rule).
Targets are `--droplet` (repeatable; same region, and the droplets' VPC is used) or `--tag`, one of the two.
**Ownership: the name prefix `docli-`**: the API accepts a load balancer's own tags only when it is created and
docli does not rely on them. `delete` needs the name typed. The API reports **no price** for a load balancer, so the
plan says so (a load balancer bills while it exists); `--wait` waits until it is active and has its IP.

### Spaces: buckets and keys

```sh
docli spaces key create --name ci --grant my-assets:read --secret-file ~/secrets/ci.spaces
docli spaces key list | delete ci
docli spaces bucket create --name assets --region nyc3      # stored as docli-assets, private
docli spaces bucket list --region nyc3 | delete assets --region nyc3
```

**What the API can do.** *Keys* are in DigitalOcean's REST API (`/v2/spaces/keys`, godo `SpacesKeys`) and docli uses
it. *Buckets* are **not** in the REST API at all; they are made, listed and deleted through the S3-compatible API at
`REGION.digitaloceanspaces.com`, signed with a Spaces access key. docli does this with the standard library only
(AWS Signature Version 4, four calls, no SDK; `spaces.go`). Bucket commands therefore need the Spaces access key
described under "Global flags", and that key needs **full access** (a limited key cannot list, create or delete
buckets). DigitalOcean's own documentation disagrees about keys: the API reference lists the endpoints, a how-to page
says keys "cannot currently be created, edited, or deleted using the DigitalOcean API". If `spaces key` gets a 401,
403 or 404, docli says so and you can make the key in the control panel (and give it to docli through the
environment).

**Keys.** `--grant BUCKET:read|readwrite|fullaccess` (repeatable) or `--full-access` (every bucket; the plan warns);
the two cannot be mixed, and a key with no grants is refused. `--secret-file` is **required**: DigitalOcean shows the
secret once, so docli needs somewhere to put it before it creates the key. The file gets the secret only (mode 0600; an
existing file is never overwritten without `--force`, checked before the key is created); the output shows the name
and the access key id, which is an identifier, not a secret. If the file cannot be written after the key exists, the
error says so and how to recover. `delete` needs the name typed. **Ownership: the name prefix `docli-`.**
**Buckets.** `create` makes a private bucket named `docli-NAME` (names are unique across the region). `delete` refuses
a bucket that is not empty (docli never deletes objects) and needs the name typed; a bucket docli did not create is
refused unless forced, and forcing never overrides "not empty". Spaces prices are not in the API, so none is shown.

## Stack files: plan and apply

A stack file is one YAML file that says what a project needs on DigitalOcean. `docli plan` shows what would change,
`docli apply` makes it so. Everything uses the milestone 1 and 2 resources and their rules (tags, `docli-` names,
the manifest, confirmation, cost, no passwords in output).

```sh
docli stack init                 # writes a commented example to config/deploys/stack.yml
docli stack validate             # checks the file alone: no token, no network
docli plan                       # reads the file and the account, prints what would change; changes nothing
docli apply                      # plan, ask, then do it in a fixed order; --yes, --dry-run as everywhere
docli adopt droplet legacy-web   # put something that already exists under docli's management
```

### Design

*What the plan/apply layer is.* One function, `planStack`, reads the stack file and the account (GET requests only)
and returns two things made together: the list of items you read in the plan (create, change, unchanged, needs
replacement, orphaned, unmanaged) and the list of steps `apply` will run (`stackOp`: a label and one function that
makes one API write or waits). `apply` runs the steps in the order they were listed, which is fixed in the code and
is the same every time: projects, SSH keys, VPCs, firewalls, droplets (create, wait, attach to firewalls), volumes
(create, grow, attach), load balancers, databases (create, wait, resize, trusted sources, users, databases), DNS
(domains, then records), and only then, with `--prune`, deletions in the reverse order (DNS, databases, load
balancers, volumes, droplets, firewalls, VPCs, SSH keys, projects). Reserved IPs and Spaces are not in the stack file
(see below). Milestone 4 added projects, volumes and load balancers as three more plain `plan...` methods called in
that fixed place; the planner did not need a registry or any other abstraction. State for one run (the ids of what was found or created, by the stack file's names)
lives in one small struct, `applyState`. The existing `Cloud` interface, `Approve`, the ownership rules and the
manifest writer are used as they are.

*What control it buys.* The plan and the steps cannot disagree, because they are built together. `plan` can be run
in CI (exit code 2) because it has no way to write: it never calls a step. `apply` can say exactly what was done
and what was not because every step is named and run once, and can stop at the first failure. A second `apply` does
nothing because each step is only planned when the account differs. "Docli never changes what it did not create"
and "docli never deletes unless asked" are checked in one place (planning) instead of in every command.

*What it deliberately does NOT do.* There is no plugin or resource-kind registry, no dependency-graph engine, no
parallelism and no state file: the account, the tags and the manifest are the state. There is no rollback (a failed
apply leaves what was done, and says so), no automatic retry of a create, no locking between two people running
apply at once, and no import of a whole account. It never deletes without `--prune`, never replaces a droplet or
database (region, image, size, engine, version are "needs replacement, not done"), never resizes a droplet, never
touches a resource docli did not create (it is used as it is and reported), and never takes a secret from the file or
the environment. Adding a resource kind later means editing the planner by hand, in the open, not registering a
handler.

### The stack file

Strict: an unknown key, a duplicate key, a YAML alias, a key that looks like a secret (`password`, `token`,
`secret`, `api_key`, `private_key`, ...), a value that looks like a secret (`dop_v1_...`, `AVNS_...`, a private key,
a connection string with a password) or an environment reference (`$VAR`, `${VAR}`, `$(cmd)`) is an error that
names the line, and every problem is listed, in line order. Nothing is read from the environment, so a secret cannot
be pulled into the file by accident. The file is found like the manifest, from the project root:
`config/deploys/stack.yml`; `--file PATH` overrides. Relative paths inside it are from the project root and `~/` is
your home directory.

| Key | Meaning |
| --- | --- |
| `version: 1` | required |
| `name` | optional stack name; droplets and databases are also tagged `docli:stack:NAME` |
| `region` | default region (also `--region` or `DOCLI_REGION`) |
| `ssh_keys[]` | `name`, `public_key_file` (a public key; a private key is refused). Without a file the key must already exist |
| `vpcs[]` | `name`, `region`, `ip_range` |
| `firewalls[]` | `name`, `rules[]` in the same `in:tcp:22:from=...` form the CLI takes |
| `droplets[]` | `name`, `region`, `size`, `image`, `vpc`, `ssh_keys[]`, `firewalls[]`, `tags[]`, `backups`, `monitoring`, `ipv6`, `user_data_file` |
| `domains[]` | `name`, `records[]`: `type`, `name` (relative; `@` is the apex), `data`, `ttl`, `priority` (MX), `flags` and `tag` (CAA), or `droplet` instead of `data` on an A record |
| `databases[]` | `name`, `region`, `version`, `size`, `nodes`, `vpc`, `trusted_droplets[]`, `trusted_tags[]`, `trusted_ips[]`, `users[]`, `databases[]` |
| `projects[]` | `name` (stored as `docli-NAME`), `purpose` (default `Web Application`), `environment`, `description` |
| `volumes[]` | `name`, `region`, `size_gb`, `description`, `filesystem_type`, `droplet` (attach once both exist), `project`, `tags[]` |
| `load_balancers[]` | `name` (stored as `docli-NAME`), `region`, `size`, `vpc`, `algorithm`, `forwarding_rules[]`, `health_check`, `droplets[]` or `tag`, `redirect_http_to_https`, `project` |

References are by name. A droplet names its VPC, SSH keys and firewalls; a database names its VPC and the droplets,
tags and addresses allowed to connect; an A record can name a droplet (its public IPv4 address is used once it
exists). Firewalls, droplets and trusted droplets must be declared in the file. A VPC or SSH key that is not
declared must already exist in the account (`plan` checks and names the line if not). A database with no trusted
source, or `0.0.0.0/0`, is a validation error: a stack file cannot create an open database. There are no password
fields at all: DigitalOcean makes database passwords, and you save one with
`docli db connection NAME --user U --password-file PATH` (apply prints these commands for new users).

**Kinds that are not in a stack file, on purpose.** *Reserved IPs* have no name and cannot be tagged, so a stack
file would have nothing to look one up by (the only identity is the address, which is the one thing a file cannot
know before it exists); use `docli reserved-ip`. *Spaces keys* are credentials and never go in a file; *buckets* need
a Spaces key to be made at all. A top-level `reserved_ips:`, `spaces:`, `spaces_keys:` or `spaces_buckets:` is an
error that says this. **`project:`** on a volume or load balancer is applied when it is created; docli does not move
an existing resource into a project from a stack file (use `docli project assign`). A volume's `droplet:` attaches it
once both exist; a volume that is attached to a different droplet is reported, never moved.
**What milestone 4 kinds compare:** a volume's size (a larger size is applied in place, a smaller one is reported as
"needs replacement, not done" because a volume cannot shrink) and attachment; a project's purpose and environment;
a load balancer's region, VPC, forwarding rules, health check, targets, algorithm, size and redirect. **docli has no
load balancer update**, so any difference in a load balancer is reported as "needs replacement, not done". Prices
for volumes and load balancers are not in the API, so they are listed under `cost_unknown_for`.

**Names.** SSH keys, VPCs and firewalls are stored as `docli-NAME` (they cannot be tagged); droplets and databases
keep their name and are tagged `docli:managed`. If a VPC, SSH key, droplet, database or domain with that name
already exists but docli did not create it, the plan says `not docli's, left alone`: it is used as a reference and is
never changed (docli does not attach a firewall to a droplet it does not own; adopt it first). An unprefixed
firewall of the same name is an error, because a firewall cannot be tagged or adopted.

**What is compared.** Firewall rules (exact list; apply adds and removes rules to match) and which declared droplets
are attached. Droplet region, image slug, size and VPC. Database engine, version, region and VPC; size and node count
(applied in place as a resize); trusted sources (the list is replaced); users and databases (missing ones are
created). DNS records by type, name and data, then TTL, priority, flags and tag. Not compared: droplet tags, backups,
monitoring, IPv6, user data and SSH keys of an existing droplet; SSH key material; database storage.

**DNS ownership** is still the manifest. A record that already exists and matches is `unchanged`; if docli did not
create it, the plan says so and `docli adopt record DOMAIN ID` makes it docli's. A record that differs and is not
docli's is reported and not changed. A record docli created (the manifest says so) whose value changed is updated;
one docli created that the file no longer lists is orphaned.

### plan

```sh
docli plan                       # human-readable
docli plan --json                # stable shape, pinned by golden files
docli plan --prune [--prune-data]   # also shows the deletions apply --prune would make
```

It reads the file and the account with GET requests only, prints every item and the total **added monthly cost**
(droplet size plus backups estimate, database price per node times nodes; a price the size list does not have is
listed under `cost_unknown_for`, never invented; resizes and storage are not counted), and exits:

| Exit | Meaning |
| --- | --- |
| `0` | no changes: nothing to create, change or replace (orphans and things that are not docli's do not count) |
| `1` | error: a bad stack file (with lines), something the account cannot do (unknown size, region, image, version), a failed read, or a mistake in plan's own flags |
| `2` | there are changes: something to create, change, replace (not done by apply) or prune |

Because `2` is "changes", `plan` reports its own usage mistakes with exit `1`. Items have an action: `create`,
`change`, `unchanged`, `needs_replacement` (differs in a way docli will not change in place; never done),
`orphaned` (docli created it, the file does not list it; **not deleted**), `unmanaged` (in the file, exists, docli
did not create it; left alone) and `prune` (only with `--prune`). Things in the account that docli did not create
and the file does not mention are listed under `not_managed` and only reported. Droplets and databases that carry
another stack's `docli:stack:X` tag are not this stack's orphans.

### apply

```sh
docli apply                      # print the plan, ask Proceed? [y/N], then do it
docli apply --yes                # no question (not for deleting: see below)
docli apply --dry-run [--json]   # the plan, nothing changed
```

The order is fixed (see Design). Each step is one API call or a wait, run once. Droplets are waited for until active
(`--wait-timeout`, default 5m); a new database is waited for only when users or databases are to be created in it
(`--db-wait-timeout`, default 20m). The manifest is updated after each step that succeeds. At the first failure it
stops, prints what was done, what failed (with the server's message, token and passwords removed) and what was not
done, exits `1`, and never retries. Run it again after fixing the cause: steps already done are seen as done.
A create whose request got no answer says it is not known whether it worked and how to check. Changes docli will not
make (replacements) are listed as `Needs replacement, not done` and do not make apply fail.

With `--json`, apply prints `{applied, done[], failed[{step, error}], not_done[], needs_replacement[], orphaned[],
next_steps[]}` and the plan goes to stderr.

### Pruning (deleting what the file no longer lists)

Nothing is deleted by default. `--prune` turns an orphan into a deletion only when all of these hold:

- docli created it (the tag or `docli-` prefix; for DNS, the manifest);
- this project's manifest lists it, so a VPC, firewall or key that belongs to another project's stack is not touched;
- for droplets, database clusters and volumes, `--prune-data` was given (docli cannot know what data they hold);
- a volume that is attached, and a project that still holds resources (other than ones pruned first), are not pruned;
- a VPC has no members left (members that are pruned first do not count);
- ownership is checked again at the moment a droplet or cluster is deleted.

Confirmation is asked for before any step runs, and everything is refused (exit `3`, nothing changed) if any part is
not given: the typed word `prune` (interactively, or `--confirm-prune prune`; `--yes` alone is never enough), and for
every droplet, database, volume, load balancer and domain its name typed (interactively; `--yes` does not skip it;
without a terminal use `--prune-confirm-name NAME` for each); anything else gets a y/N, or `--yes`. Users and
databases inside a cluster are never pruned. Deletions run in the reverse order: DNS records, domains, databases,
load balancers, volumes, droplets, firewalls, VPCs, keys, projects.

### adopt

```sh
docli adopt droplet legacy-web [--stack blog]   # adds the tag docli:managed (and docli:stack:blog)
docli adopt database legacy-pg
docli adopt volume legacy-data                  # adds the tag docli:managed
docli adopt reserved-ip 203.0.113.10            # manifest only: docli may then assign, unassign and release it
docli adopt domain shop.example                 # manifest only: docli may then manage (and delete) the domain
docli adopt record shop.example 901             # manifest only: docli may then update and delete this record
docli adopt droplet legacy-web --dry-run
```

It shows a plan and asks first. It changes nothing on DigitalOcean except adding the tag, where the API allows
tagging: no rename, no restart, no tag removed. Domains and records cannot be tagged, so adopting one only records
it in the manifest (this is how the DNS ownership gap is closed). VPCs, firewalls, SSH keys, projects and load balancers are refused: docli
knows its own by a name prefix, so adopting would mean renaming them. `--json` prints `{adopted, kind, name, id,
api_changed}`.

### A Kamal app: one droplet, one database, a VPC, a firewall, DNS

This is what `docli stack init` writes (the test suite runs it end to end against the fake API).

```yaml
version: 1
name: shop           # optional: tags droplets and databases docli:stack:shop
region: nyc3         # default region for everything below

ssh_keys:
  - name: deploy
    public_key_file: ~/.ssh/id_ed25519.pub      # the public half only; a private key is refused

vpcs:
  - name: shop-net
    ip_range: 10.50.0.0/24                      # optional

firewalls:
  # DigitalOcean firewalls deny by default: only what is listed is allowed.
  - name: shop-web
    rules:
      - in:tcp:22:from=0.0.0.0/0,::/0           # tighten this to your own address if you can
      - in:tcp:80:from=0.0.0.0/0,::/0
      - in:tcp:443:from=0.0.0.0/0,::/0
      - out:tcp:all:to=0.0.0.0/0,::/0
      - out:udp:all:to=0.0.0.0/0,::/0
      - out:icmp:all:to=0.0.0.0/0,::/0

droplets:
  - name: shop-web-1
    size: s-1vcpu-1gb
    image: ubuntu-24-04-x64
    vpc: shop-net
    ssh_keys: [deploy]
    firewalls: [shop-web]
    tags: [app]
    monitoring: true

databases:
  - name: shop-db
    version: "16"
    size: db-s-1vcpu-1gb
    nodes: 1
    vpc: shop-net
    trusted_droplets: [shop-web-1]              # only these may connect
    users: [shop]
    databases: [shop_production]

domains:
  - name: shop.example
    records:
      - {type: A, name: "@", droplet: shop-web-1, ttl: 300}   # the droplet's public IPv4, known once it exists
      - {type: CNAME, name: www, data: shop.example., ttl: 1800}
```

```sh
docli stack validate && docli plan      # read the plan; it exits 2 because there is a lot to create
docli apply                             # SSH key, VPC, firewall, droplet (waits), attach, database, user, DNS
docli db connection shop-db --user shop --password-file config/secrets/db_password   # the password, to a 0600 file
docli plan                              # exits 0: the account matches the file
```

After `apply`, `config/deploy.yml` reads the droplet's address and the database host from the manifest exactly as in
"PostgreSQL for a Kamal app" and "Kamal: read the host IP from the manifest" below: the names are the ones in the
stack file (`shop-web-1`, `shop-db`).

## The manifest: `config/deploys/manifest.yml`

docli writes a YAML file into your project that lists the infrastructure it set up, so people, agents and Kamal's
config can see what exists. Kamal does not record this (its `deploy.yml` lists only hosts).

**It is a record, not the authority, with one exception.** What docli may delete among droplets, VPCs, firewalls,
SSH keys and database clusters is decided by the `docli:managed` tag (or the `docli-` name prefix) on the
DigitalOcean account, never by this file. Editing the file cannot make docli delete any of those, and removing an
entry does not protect a resource. **DNS and reserved IPs are the exceptions**: domains, records and reserved IPs cannot
be tagged (or, for the last, named), so the `domains:` and `reserved_ips:` sections are what tell docli which it
created (see "DNS ownership" and "Reserved IPs").

- **Where:** `config/deploys/manifest.yml` under the project root: the nearest directory above the working
  directory that contains `.git`, a `config/` directory, or an existing `config/deploys/manifest.yml`. If none
  is found it uses the working directory and says so on stderr. `config/` and `config/deploys/` are created on the
  first write. Kamal's own files are `config/deploy.yml` and `config/deploy.<destination>.yml`, so a
  `config/deploys/` directory does not collide with them. `--manifest PATH` overrides the location and
  `--no-manifest` skips it.
- **When:** after a successful create, delete, add-rule, remove-rule, attach or detach of a managed resource,
  docli updates that resource's entry (create, replace or remove; for DNS, the entry of the domain), leaves every other entry in place, writes the
  file atomically (temp file in the same directory, then rename) and prints `updated config/deploys/manifest.yml`
  to stderr. `--dry-run`, refusals, declined confirmations and failed API calls never touch the file. If the change
  succeeded but the file could not be written, that is a warning, not an error.
  After a firewall change docli also refreshes the `firewalls:` list of droplet entries already in the file, so
  the two do not disagree.
- **What:** `version: 1`, `generated_by`, `updated_at`, and `resources:` grouped by kind. Droplets: id, name,
  region, status, size_slug, image, vcpus, memory_mb, disk_gb, public_ipv4, private_ipv4, vpc_name, vpc_id,
  monthly_cost, tags, attached firewalls, created_at. VPCs: id, name, region, ip_range, created_at. Firewalls: id,
  name, rules in the same `in:tcp:22:from=...` form the CLI accepts, the tags and droplet names they apply to,
  created_at. SSH keys: id, name, fingerprint. Databases: id, name, engine, version, region, size_slug, num_nodes,
  status, vpc_name, vpc_id, host, private_host, port, database (the default database name), tags, trusted_sources
  (as `droplet:ID`, `tag:NAME`, `ip_addr:ADDR`), created_at. Domains: id (the name), name, `created_by_docli`, ttl,
  and `records:` with only the records docli created (id, type, name, data, ttl, priority, flags, tag). Tags are
  listed for droplets and databases; VPCs, firewalls, keys and domains cannot carry tags. `status` is the state when
  the entry was written. Database entries carry no cost: the API does not report one on the cluster.
  Milestone 4 adds `projects:` (id, name, purpose, environment, created_at), `volumes:` (id, name, region, size_gb,
  the names of the droplets it is attached to, filesystem_type, tags, created_at), `reserved_ips:` (id and ip, region,
  the droplet's name), `load_balancers:` (id, name, ip, region, size, size_unit, algorithm, vpc_name, vpc_id,
  forwarding_rules in the `--forward` form, health_check, droplet names, tag, redirect_http_to_https, created_at; no
  status, which changes by itself), `spaces_buckets:` (id `REGION/NAME`, name, region, created_at) and `spaces_keys:`
  (id = the access key id, name, grants as `bucket:permission`, created_at). **A Spaces key's secret is never written;
  the access key id is an identifier, not a secret.**
- **Never written:** the token, passwords (database or otherwise), connection strings or URIs, user-data contents,
  private keys, public-key material. Host and port are fine. Tests plant secrets in user data, descriptions and keys
  and run database commands against a fake that returns passwords and URIs, and check none appear.

```sh
docli manifest show                 # print it (--json for JSON)
docli manifest verify               # compare with the account; exit 1 on any difference; changes nothing
docli manifest refresh              # rewrite from the account's docli:managed resources; shows a diff, asks first
```

`verify` reports entries whose resource no longer exists (or is no longer docli-managed), managed resources missing
from the file, and fields that drifted. For domains it compares record by record ("record 123 (A www) data: file
has ..., account has ..."). A token that may not read domains or databases still works for everything else; entries
in a section it cannot read are reported as `unverifiable`, never as gone. `refresh` is the only command that adds
resources the file does not know about (including database clusters, by their tag); it keeps sections it does not
own. It never adds a domain or record, a reserved IP or a bucket: the account cannot say which reserved IPs are docli's,
and buckets need a Spaces key and a region to be listed at all. For those it only updates the entries already in the
file and drops the ones that no longer exist. Projects, volumes, load balancers and Spaces keys are rebuilt from the
account like droplets. `verify` checks bucket entries only when a Spaces access key is configured; without one they
are reported as `unverifiable` and everything else is still checked.

### Kamal: read the host IP from the manifest

Kamal renders `config/deploy.yml` with ERB from the project root, so write the path relative to the root. (Do not
use `__dir__` there: under Kamal's ERB it is `.`, not `config/`.) This was checked against Kamal 2.12's
configuration loader.

```yaml
# config/deploy.yml
<%
  manifest = YAML.load_file(File.join("config", "deploys", "manifest.yml"))
  web = manifest.dig("resources", "droplets").find { |d| d["name"] == "web-1" }
-%>
service: blog
servers:
  web:
    - <%= web["public_ipv4"] %>
```

## JSON output

`--json` prints one JSON document on stdout. Every field is always present: unknown strings are `""`, unknown
numbers `0`, empty lists `[]`, and nothing is `null` except `monthly_cost_usd` in a plan with no cost (and an item's `monthly_cost_usd`, null unless it is a priced create). The golden
files in `testdata/golden/` pin each shape; a change to them is a breaking change.

| Command | Shape |
| --- | --- |
| `sizes` | `[{slug, memory_mb, vcpus, disk_gb, transfer_tb, price_monthly_usd, price_hourly_usd, available, regions[]}]` |
| `regions` | `[{slug, name, available, features[]}]` |
| `images` | `[{id, slug, name, type, distribution, public, status, min_disk_gb, regions[]}]` |
| `ssh-key list/get/create` | `[{id, name, fingerprint, managed}]` or one object |
| `vpc list/create` | `[{id, name, region, ip_range, default, description, managed, created_at}]`; `vpc get` adds `members[{urn, name}]` |
| `firewall ...` | `{id, name, status, managed, rules[{direction, protocol, ports, addresses[], tags[], droplet_ids[], spec, text}], droplet_ids[], tags[], created_at}` |
| `droplet list/get/create/power-*` | `{id, name, status, managed, region, size_slug, image{id, slug, name}, vcpus, memory_mb, disk_gb, public_ipv4, private_ipv4, public_ipv6, vpc_id, tags[], price_monthly_usd, created_at}` |
| `domain list/create` | `[{name, ttl, managed}]` or one object; `managed` means docli created the domain |
| `domain get` | `{name, ttl, managed, records[]}` |
| `dns list/create/update` | `[{id, domain, type, name, fqdn, data, ttl, priority, flags, tag, managed}]` or one object; `managed` means docli created the record |
| `db list/create/resize/wait` | `{id, name, engine, version, status, managed, region, size_slug, num_nodes, vpc_id, tags[], connection{host, port, database, user, ssl_mode}, private_connection{...}, storage_size_mib, created_at}` (a list for `list`) |
| `db get` | the object above plus `trusted_sources[{uuid, type, value, name}]` |
| `db connection` | `{name, host, port, private_host, private_port, database, user, ssl_mode, password_file}`; no password |
| `db user list/create` | `[{name, role}]` or one object; no password |
| `db database list/create` | `[{name}]` or one object |
| `db pool list/create` | `[{name, user, database, mode, size, host, private_host, port}]` or one object |
| `db backups list` | `[{created_at, size_gigabytes}]` |
| `db firewall list/set` | `[{uuid, type, value, name}]` (`name` is a droplet's name when known) |
| `project list/create` | `[{id, name, description, purpose, environment, default, managed, created_at}]` or one object; `project get` adds `resources[{urn, assigned_at, status}]`; `project assign` prints `{assigned, project_id, project_name, resources[]}` |
| `volume list/get/create/attach/detach/resize` | `{id, name, managed, region, size_gb, droplet_ids[], filesystem_type, filesystem_label, description, tags[], created_at}` (a list for `list`) |
| `reserved-ip list/get/create/assign/unassign` | `{ip, region, droplet_id, droplet, project_id, locked, managed}`; `droplet_id` is `0` when unassigned; `managed` means the manifest lists it |
| `lb list/get/create` | `{id, name, managed, ip, status, region, size, size_unit, algorithm, vpc_id, forwarding_rules[{entry_protocol, entry_port, target_protocol, target_port, certificate_id, tls_passthrough, spec}], health_check{protocol, port, path, interval_seconds, timeout_seconds, healthy_threshold, unhealthy_threshold, spec} or null, droplet_ids[], tag, redirect_http_to_https, created_at}`; no price field, because the API gives none |
| `spaces bucket list/create` | `[{name, region, managed, created_at}]` or one object |
| `spaces key list/create` | `{name, access_key_id, grants[{bucket, permission}], managed, created_at, secret_file}`; there is no secret field, and `secret_file` is where it was saved (`""` for a list) |
| any `delete` | `{deleted, type, id, name}` |
| any `--dry-run` | `{dry_run: true, plan: {action, summary, steps[], warnings[], monthly_cost_usd}}` |
| `token check` | `{valid, email, status, team, droplet_limit, source, read_access[{resource, read, detail}], write_access, notes[]}`; `read_access` now also probes `domains` and `databases` |
| `manifest verify` | `{path, in_sync, differences[{kind, section, id, name, detail}]}`; `kind` is `missing_in_account`, `missing_in_file`, `drift` or `unverifiable` |
| `plan` | `{stack_file, stack, has_changes, summary{create, change, unchanged, needs_replacement, orphaned, unmanaged, prune}, monthly_cost_added_usd, cost_unknown_for[], items[{kind, name, action, id, details[], monthly_cost_usd}], not_managed[{kind, name, id}], warnings[]}`; exit `2` when `has_changes` |
| `apply` | `{applied, done[], failed[{step, error}], not_done[], needs_replacement[], orphaned[], next_steps[]}`; `apply --dry-run` prints `{dry_run: true, plan: {...as plan}}` |
| `adopt` | `{adopted, kind, name, id, api_changed}` |
| `stack validate` | `{file, valid, stack, counts{}, notes[]}`; `counts` gains `projects`, `volumes` and `load_balancers` only when the file uses them |
| `version` | `{version, go, os, arch}` |

No JSON shape has a field for a password or a connection URI; a test walks the shapes and fails if one appears.
Errors with `--json` are one line on stderr: `{"error": "...", "exit_code": 3}`. `managed` is true when the
resource carries the `docli:managed` tag (droplets) or a `docli-` name (the rest). A rule's `spec` is the text you
can pass back to `--rule`.

## Tests

```sh
make test      # go vet, gofmt check, go test -race ./...
go test -update -run 'Golden' ./...   # rewrite golden files after an intended change
```

**Nothing here touches the real DigitalOcean API.** `fakedo_test.go` is an in-memory DigitalOcean (an `httptest`
server with paging, tags, VPC members, firewall rules, droplets that become active after a few polls, failure
injection and a 401 that echoes whatever it was sent) and every command is tested end to end through godo against
it. Unit tests cover rule parsing, hostname validation, cost, confirmation (terminal or not, `--yes`, `--dry-run`),
the ownership refusals, retry and backoff with an injected clock, token handling, JSON stability and the manifest.
The token tests run many commands in many failure modes and fail if the fake token appears anywhere in output.
Milestone 2 adds a fake domain, record and database service (with passwords and connection URIs in its answers, and
failures that echo them), end-to-end tests of every new command, the ownership refusals, the password tests (the
fake's passwords must not appear in any output, JSON, `--debug`, error or manifest, including when the server echoes
one), golden JSON files, and manifest update-in-place, drift and refresh tests. During development seven safety
rules were broken on purpose by hand (DNS and database ownership, the typed domain name, the open-database refusal,
password-file overwrite, password in `db connection` output, retrying writes, error scrubbing); each time tests
failed, and the change was reverted. The mutations are not part of the repository.
Milestone 3 adds `stack_test.go`: the shipped example stack end to end through plan and apply (empty, create-all,
partial, drift, orphaned, rerun is a no-op, failure in the middle with the exact done/not-done report, needs
replacement), prune refusals and confirmations, adopt, stack parsing errors with line numbers, secrets and
environment references rejected, token and password leak tests over many failure modes, `plan` proven read-only, and
golden JSON for plan, apply, adopt and validate. Thirteen safety rules were broken on purpose by hand (apply
deleting without `--prune`, plan running the steps, apply retrying a create, skipping the prune confirmation,
pruning data without `--prune-data`, pruning without the manifest, accepting secrets in the file, continuing after a
failure, treating a droplet docli did not create as its own, replacing a droplet automatically, adopt doing more
than tagging, not scrubbing the failure report, `--yes` standing in for the typed word); each time tests failed and
the change was reverted.

Milestone 4 adds `fakedo_m4_test.go` (projects, volumes, reserved IPs, load balancers, Spaces keys over REST, and a fake
S3 endpoint for buckets that **recomputes every request's AWS signature** and rejects a wrong one), end-to-end tests for
each new command (`m4_project_volume_test.go`, `m4_ip_lb_test.go`, `m4_spaces_test.go`), the refusals, `m4_secrets_test.go`
(the fake's Spaces secrets and the token must not appear in any output, JSON, `--debug`, error or manifest across many
failure modes, including a server that echoes them and an API that returns a key's secret in a list), manifest
drift tests for every new kind (`m4_manifest_test.go`), stack tests (`stack_m4_test.go`) and golden JSON
(`golden_m4_test.go`). One test checks docli's signing code against the signature AWS publishes for its own IAM example.
Sixteen safety rules were broken on purpose by hand (volume delete ignoring "attached", resize allowing a shrink, reserved IP ownership, project delete with resources, bucket delete ignoring "not empty", printing a key secret, load balancer delete without the typed name, overwriting the secret file, retrying writes, pruning volumes without --prune-data, not registering the Spaces secret up front, a broken signature, assign not saying it moves the address, project assign moving unmanaged resources, not registering a created key secret, not scrubbing signatures); each time tests failed and the
change was reverted. The mutations are not part of the repository.

## How the code is laid out

`cloud.go` is a thin interface over the godo calls docli uses, with paging done once; tests and, later, stack
planning use the same interface. `confirm.go` holds the shared plan, confirmation and ownership layer. Each
resource has its own `cmd_*.go`. `manifest.go` and `manifest_entries.go` write the record; a resource kind adds a
section by implementing `ManifestEntry` (`Section()` and `EntryID()`) without changing the writer.

Milestone 3 adds `stack_file.go` (strict parsing and offline validation), `stack_plan.go` and `stack_plan_dns.go`
(the planner: reads the account, builds items and steps together), `stack_apply.go` (the steps and `applyState`),
`cmd_stack.go` (`stack`, `plan`, `apply`, prune confirmation) and `cmd_adopt.go`.

Milestone 4 adds `cmd_project.go`, `cmd_volume.go`, `cmd_reservedip.go`, `cmd_lb.go` and `cmd_spaces.go`, `cloud_m4.go` (the
godo calls), `spaces.go` (the S3 client: credentials, AWS Signature Version 4, four calls), `views_m4.go` (JSON),
`manifest_m4.go` (entries; lists are loaded on first use) and, for the stack file, `stack_plan_m4.go` and
`stack_apply_m4.go`.

## What is not here yet

`destroy` of a whole stack, `import` of a whole account, rollback, and resizing droplets in place; load balancer
updates, a Spaces bucket's policy, CORS, lifecycle rules and objects, CDN endpoints, volume snapshots, IPv6 reserved
IPs and Kubernetes (not planned); reserved IPs and Spaces in the stack file (deliberately not, see above); read replicas, maintenance windows,
storage autoscaling, major-version upgrades, restore-from-backup and engines other than PostgreSQL (not planned); an opt-in
real-account smoke test (milestone 5).

## Not verified against the real API

Milestone 4 (projects, volumes, reserved IPs, load balancers, Spaces) has not touched the real API either. In order
of how much I would worry:

- **Spaces buckets over the S3 API.** The signing code reproduces the signature AWS publishes for its own IAM example, and
  the fake S3 server recomputes every signature, but that proves docli agrees with itself and with AWS's algorithm,
  not that Spaces accepts the request. Assumed and unchecked: **path-style addressing** (`REGION.digitaloceanspaces.com/BUCKET`)
  works for create, list and delete; the **region in the signature scope** is the Spaces region slug (`nyc3`); `PUT` with
  `x-amz-acl: private` creates a private bucket; `GET /` on a region's endpoint lists that region's buckets; a
  `list-type=2&max-keys=1` request says whether a bucket is empty (versioned or half-uploaded objects may still block a
  delete, in which case the delete's own error is shown). Only `GET` is retried. A bucket name taken by someone else
  in the region is reported by Spaces, not detected in advance.
- **Spaces keys over REST.** godo v1.237.0 and DigitalOcean's API reference have `/v2/spaces/keys`, but a how-to page says keys
  cannot be created through the API (see "Spaces: buckets and keys"). Whether the real API answers, whether the secret
  comes back only on create, and whether a list answer ever carries one, are unchecked; docli handles all three
  cases (the secret is registered for scrubbing the moment it is seen). The `fullaccess` grant with an empty bucket for
  `--full-access`, and the permission names `read`, `readwrite`, `fullaccess`, are taken from the API reference.
- **Sources for the Spaces findings** (read 2026-11-12): DigitalOcean's Spaces API reference
  (docs.digitalocean.com/products/spaces/reference/api/: only "CDN Endpoints" and "Spaces Keys" groups, no bucket
  endpoints); the Spaces Keys reference (docs.digitalocean.com/reference/api/reference/spaces-keys/: `/v2/spaces/keys`,
  secret returned on create, `fullaccess` with an empty bucket, "cannot mix fullaccess with scoped permissions");
  "How to Manage Access to DigitalOcean Spaces" (docs.digitalocean.com/products/spaces/how-to/manage-access/: keys
  "can only be created and managed through the DigitalOcean Control Panel", and a full-access key is what allows
  "bucket creation and configuration" and "listing all buckets"); the Spaces S3 reference
  (docs.digitalocean.com/reference/api/spaces/: `CreateBucket` as `PUT /` on `my-space.nyc3.digitaloceanspaces.com`,
  `ListBuckets` as `GET /` on `nyc3.digitaloceanspaces.com`, both with AWS Signature Version 4); and godo v1.237.0's
  own source, which has `SpacesKeys` (`spaces_keys.go`) and no bucket service.
- **Load balancers.** Field names are godo's. The size slugs (`lb-small`...), `size_unit`, the `round_robin` and
  `least_connections` algorithms, the health check defaults docli fills in (interval 10, timeout 5, healthy 5, unhealthy
  3), and whether a load balancer in a VPC needs its droplets in that VPC, are assumptions. docli sends no `tags` on a load
  balancer. The status `active` and the IP appearing when it is ready are assumed for `--wait`.
- **Volumes.** That the tags exist before a create (docli creates them first), that `attach`, `detach` and `resize` are
  asynchronous and succeed with the answers godo parses, and that a volume name is unique per region, are assumed.
  docli does not check the region supports block storage. The resize action takes the region.
- **Reserved IPs.** The `assign` and `unassign` actions and a droplet's embedded object in the answer are godo's; whether
  `droplet.name` is always filled in is assumed (docli shows the id when it is not).
- **Projects.** That a resource is assigned with its URN (`do:droplet:ID`, `do:volume:ID`, `do:loadbalancer:ID`,
  `do:reservedip:ADDR`, `do:dbaas:ID`, `do:domain:NAME`, `do:firewall:ID`) as godo builds them, that assigning moves it
  out of its old project, that deleting a project with resources fails (docli checks first), and that `purpose` takes
  `Web Application` and the three environment names, are assumed.
- **Prices.** The API gives none for volumes, reserved IPs, load balancers or Spaces, so docli shows none. Nothing in
  milestone 4 has a built-in price table.

Milestone 3 (stack file, plan, apply, adopt) has not touched the real API either. On top of the list below:

- **Tagging databases.** `adopt database` adds a tag with `POST /v2/tags/NAME/resources` and the resource type
  `database`; godo defines the type, but whether the real API accepts it for a cluster is unchecked. A failure is
  reported and nothing else is changed.
- **Whether a plan and the account agree after apply.** The in-sync check compares fields as the fake returns them:
  droplet image slug, database `version` and `region`, firewall rule shapes, the record data the API echoes
  (docli compares CNAME/MX/NS data without case or trailing dot). If the real API normalises any of them
  differently, `plan` would show a permanent change after `apply`; the fix is in the comparison, not in the account.
- **Database sizes and prices, passwords, trusted-source semantics, async states** are as listed below and the
  stack file relies on the same guesses.
- **A cluster VPC member URN** (`do:dbaas:ID`) is assumed when `--prune` decides whether a VPC is empty.
- **Apply order and waiting.** Whether a droplet can be attached to a firewall straight after it is `active`, and
  whether a cluster accepts users the moment it is `online`, is assumed. A failure stops the run and apply can be run
  again.

Nothing in milestone 2 has touched the real DigitalOcean API. These points come from godo's types and my reading of
the docs, not from a real answer, and the fake server only proves docli agrees with itself:

- **Field names and shapes** of domains, records, database clusters, users, pools, firewall rules and options
  (taken from godo v1.237.0's structs).
- **Database prices.** The plan reads the price from `/v2/sizes`. Whether that list includes database sizes
  (`db-s-1vcpu-1gb`) is unknown; if not, the plan says the cost is not shown. There is no built-in price table.
  Storage and other extras are never included.
- **Async states.** The cluster is waited on until `status` is `online` (after `creating`, `resizing`); other states
  such as `migrating` or `forking` are only reported. The poll interval (15 s) and timeouts are guesses.
- **Trusted-source semantics.** The rule types `droplet` (value: droplet id), `tag`, `ip_addr` are the ones godo
  documents. That a cluster with no rules accepts connections from anywhere, that `PUT` replaces the list, and
  whether IPv6 or `/0` ranges are accepted, are assumptions. docli treats an empty list and `0.0.0.0/0` as open.
- **Pool modes** `session`, `transaction`, `statement` and the 1 to 1000 size range are docli's checks.
- **Passwords.** Whether the admin password is returned only at create or on every `GET`, whether `GET .../users`
  returns each user's password, and whether a pool's answer carries one, is unknown. docli takes the password from
  whichever answer has it and says so plainly when none does. The `AVNS_` shape used as an extra scrubbing rule is
  an observation, not a documented guarantee.
- **Pagination.** godo parses no page links for database lists; docli refuses a full page of 200. Whether the real
  API honours `per_page=200` for those lists is unknown. Domain and record lists are paged normally.
- **DNS limits** (TTL 30 to 2147483647, TXT up to 2048 characters, record-name syntax) are docli's sanity checks.
  The API has the final say and its errors are shown. How DigitalOcean treats a trailing dot in record data is
  unchecked (docli passes your text through).
- **Cluster names** (3 to 63 lower case letters, digits, dashes) and database and user names (letters, digits,
  underscore) are conservative guesses.
- **Resize** behaviour (downtime, whether the node count can go down) is not claimed; the plan only warns that
  connections may be interrupted.
- **Name uniqueness**: docli refuses a duplicate cluster name; whether DigitalOcean requires unique names is unknown.
