package main

import "strings"

const helpTop = `docli: manage DigitalOcean droplets, VPCs, firewalls, SSH keys, DNS, PostgreSQL, projects, volumes, reserved IPs,
load balancers and Spaces from the command line.

Usage: docli [global flags] COMMAND [args] [flags]

Commands:
  version                      print the version
  help [COMMAND]               this help, or help for one command
  token check                  check the token and what it can read
  sizes | regions | images     what you can create (read only)
  ssh-key list|get|create|delete
  vpc list|get|create|delete
  firewall list|get|create|delete|add-rule|remove-rule|attach|detach
  droplet list|get|create|delete|power-off|power-on|reboot
  domain list|get|create|delete
  dns list|create|update|delete
  db list|get|create|delete|resize|wait|connection|backups list
  db user|database|pool list|create|delete      db firewall list|set
  project list|get|create|delete|assign
  volume list|get|create|delete|attach|detach|resize
  reserved-ip list|get|create|delete|assign|unassign
  lb list|get|create|delete
  spaces bucket list|create|delete     spaces key list|create|delete
  manifest show|verify|refresh the record of what docli set up (config/deploys/manifest.yml)
  stack init|validate          write an example stack file, or check one (no account needed)
  plan                         read the stack file and the account, say what would change (changes nothing)
  apply                        make the account match the stack file (plan, confirm, fixed order, stops at the first failure)
  adopt droplet|database|domain|record   put something that already exists under docli's management
  adopt volume|reserved-ip               the same for a volume (tagged) or a reserved IP (recorded in the manifest)

Global flags (anywhere on the line):
  --json          machine-readable output (same fields every time)
  --yes           skip the y/N question (never skips typing a droplet's name)
  --dry-run       show the plan and change nothing
  --region R      region slug, default $DOCLI_REGION
  --timeout D     per-request timeout (default 30s)
  --debug         print each request's method, URL and status to stderr (never headers or bodies)

Token: set DIGITALOCEAN_TOKEN (or DOCLI_TOKEN), or put it in ~/.config/docli/token (mode 0600).
It is never taken from a flag and never printed.

Safety: everything that changes your account shows a plan and asks first (default no). Without --yes and
without a terminal it refuses. docli only deletes what it created: droplets tagged docli:managed, and SSH
keys, VPCs and firewalls whose names start with docli- (they cannot carry tags), database clusters tagged
docli:managed, and domains and DNS records that the manifest says docli created. Database passwords are never
printed: they go only to a 0600 file named with --password-file. Volumes are tagged docli:managed; projects, load
balancers, Spaces keys and buckets are docli- names; reserved IPs (no name, no tag) are the ones the manifest lists.
A Spaces secret key goes only to a 0600 file named with --secret-file. Creating commands take --project NAME.

Exit codes: 0 ok, 1 error, 2 usage, 3 refused (declined or unsafe).
Exception: for "docli plan", 2 means "there are changes" and its own mistakes exit 1.
Environment defaults: DOCLI_REGION, DOCLI_VPC, DOCLI_SSH_KEY.
Spaces buckets need a Spaces access key: DOCLI_SPACES_ACCESS_KEY and DOCLI_SPACES_SECRET_KEY, or ~/.config/docli/spaces.
`

var helpTopics = map[string]string{
	"sizes": `docli sizes [--region R] [--max-price N] [--all]
Lists droplet sizes with monthly and hourly prices as the API reports them. By default only available sizes up to
$100 a month are shown, cheapest first.`,
	"regions": `docli regions [--all]
Lists regions where droplets can be created (--all includes unavailable ones).`,
	"images": `docli images [--type distribution|application|custom] [--slug SLUG] [--region R]
Lists images. The default is distribution images only. --slug looks up one image.`,
	"ssh-key": `docli ssh-key list
docli ssh-key get NAME|ID|FINGERPRINT
docli ssh-key create --name NAME (--public-key-file FILE | --public-key "ssh-ed25519 AAAA...")
docli ssh-key delete NAME|ID|FINGERPRINT [--force-unmanaged --confirm-name NAME]

SSH keys cannot be tagged, so docli stores its keys as docli-NAME and refuses to delete keys without that prefix
unless you pass --force-unmanaged and type the name. Private keys are refused.`,
	"vpc": `docli vpc list | get NAME|ID | create --name NAME --region R [--ip-range CIDR] | delete NAME|ID

VPCs cannot be tagged, so names are stored as docli-NAME. Delete refuses when the VPC has members and names them.`,
	"firewall": `docli firewall list | get NAME
docli firewall create --name NAME --rule SPEC [--rule SPEC...] [--tag T] [--droplet NAME|ID]
docli firewall add-rule NAME SPEC [SPEC...]     docli firewall remove-rule NAME SPEC [SPEC...]
docli firewall attach NAME (--droplet NAME|ID | --tag T)...     docli firewall detach NAME ...
docli firewall delete NAME [--force-unmanaged --confirm-name NAME]

DigitalOcean firewalls DENY BY DEFAULT: a droplet behind a firewall accepts only what an inbound rule allows and
sends only what an outbound rule allows. --dry-run prints the full resulting rule list.
Firewalls cannot be tagged, so names are stored as docli-NAME.

` + ruleHelp,
	"droplet": `docli droplet list [--tag T] [--vpc NAME|ID] [--region R]
docli droplet get NAME|ID
docli droplet create --name N --region R --size S --image I [--vpc V] [--ssh-key K]... [--tag T]... [--stack NAME]
                     [--user-data-file F] [--backups] [--monitoring] [--ipv6] [--wait] [--wait-timeout 5m]
docli droplet delete NAME|ID            (type the name, or --yes --confirm-name NAME)
docli droplet power-off|power-on|reboot NAME|ID [--wait]

Create prints the plan with the estimated monthly cost and the tags it will apply (docli:managed, and
docli:stack:NAME with --stack). Names must be valid hostnames. A create is never retried automatically.`,
	"manifest": `docli manifest show | verify | refresh   [--manifest PATH]

The manifest (default config/deploys/manifest.yml in the project root) records what docli set up. It is a record,
not the authority: deletes are decided by the docli:managed tag on the account. Mutating commands update their
resource's entry (use --manifest PATH to move it, --no-manifest to skip).
  show     print the file
  verify   compare it with the account; exit 1 on any difference; changes nothing
  refresh  rewrite it from the account's docli:managed resources (shows a diff, asks first)`,
	"domain": `docli domain list | get DOMAIN | create --name example.com | delete DOMAIN

Domains cannot be tagged, so docli remembers what it created in the manifest (section domains:). delete refuses a
domain the manifest does not list as docli's unless you pass --force-unmanaged, and ALWAYS needs the domain name
typed (or --yes --confirm-name DOMAIN), because it removes every record in the domain.`,
	"dns": `docli dns list DOMAIN [--type A] [--name www]
docli dns create DOMAIN --type A|AAAA|CNAME|TXT|MX|NS|CAA --name NAME|@ --data VALUE [--ttl 1800]
                        [--priority N (MX, required)] [--flags 0|128 --tag issue|issuewild|iodef (CAA)]
docli dns update DOMAIN RECORD_ID [--data V] [--name N] [--ttl N] [--priority N] [--flags N] [--tag T]
docli dns delete DOMAIN RECORD_ID      (unmanaged records: --force-unmanaged --confirm-name NAME.DOMAIN)

Names are relative to the domain: www, not www.example.com; @ is the apex. Records are changed by id (see dns list).
Records cannot be tagged either: update and delete refuse records the manifest does not list as created by docli.`,
	"db": `docli db list | get CLUSTER | wait CLUSTER [--wait-timeout 20m] | backups list CLUSTER
docli db create --name N --region R --version 16 --size SLUG --nodes 1 --vpc V
                (--trusted-droplet D | --trusted-tag T | --trusted-ip ADDR)... [--stack S] [--tag T]...
                [--wait] [--password-file PATH [--force]] [--allow-open --confirm-name N]
docli db resize CLUSTER [--size SLUG] [--nodes N] [--wait]
docli db delete CLUSTER                 (type the name, or --yes --confirm-name NAME)
docli db connection CLUSTER [--user U] [--database D] [--password-file PATH [--force]]
docli db user|database|pool list CLUSTER
docli db user create CLUSTER USER [--password-file PATH]     docli db database create CLUSTER NAME
docli db pool create CLUSTER --name P --database D [--user U] [--mode transaction] [--size 10]
docli db user|database|pool delete CLUSTER NAME
docli db firewall list CLUSTER | set CLUSTER (--droplet D | --tag T | --ip ADDR)... [--allow-open]

Clusters are tagged docli:managed (and docli:stack:NAME) and only those can be deleted without --force-unmanaged.
Create needs a VPC and at least one trusted source; without any, the database could be reached from any address
that has the credentials, so docli refuses unless you pass --allow-open and type the cluster name. firewall set
REPLACES the list. Passwords are never printed or put in JSON: --password-file writes one to a new 0600 file and
refuses to overwrite an existing file without --force.`,
	"project": `docli project list | get NAME|ID | create --name NAME [--purpose "Web Application"] [--environment Production] [--description T]
docli project delete NAME|ID
docli project assign PROJECT KIND:NAME...      kinds: droplet, volume, lb, reserved-ip, database, domain, firewall

A project is a folder for resources. Names are stored as docli-NAME (a project cannot be tagged). delete refuses a
project that still holds resources, the default project, and projects docli did not create. assign MOVES resources
into the project and says which project each is in now; it refuses resources docli did not create unless you pass
--force-unmanaged and type the project name. Commands that create things take --project NAME.`,
	"volume": `docli volume list | get NAME|ID [--region R]
docli volume create --name N --region R --size GB [--filesystem-type ext4|xfs] [--tag T]... [--stack S] [--project P]
docli volume attach|detach VOLUME DROPLET     docli volume resize NAME|ID --size GB
docli volume delete NAME|ID                   (type the name, or --yes --confirm-name NAME)

Volumes are tagged docli:managed. Resize only grows (the API cannot shrink a volume). Delete refuses an attached volume
and always needs the name typed. The API reports no price, so none is shown. Unmanaged volumes need --force-unmanaged.`,
	"reserved-ip": `docli reserved-ip list | get IP
docli reserved-ip create (--region R | --droplet D) [--project P]
docli reserved-ip assign IP DROPLET     docli reserved-ip unassign IP
docli reserved-ip delete IP             (type the address, or --yes --confirm-name IP)

A reserved IP cannot be named or tagged, so the manifest says which ones docli created; the others are refused unless
you pass --force-unmanaged and type the address (or record one with: docli adopt reserved-ip IP). assign MOVES an
address that may be in use, and the plan names the droplet it is on now. delete refuses an assigned address.`,
	"lb": `docli lb list | get NAME|ID
docli lb create --name N --region R --forward ENTRY=TARGET [--forward ...] (--droplet D... | --tag T) [--vpc V]
                [--health-check PROTO:PORT[/PATH][,interval=10][,timeout=5][,healthy=5][,unhealthy=3]]
                [--size lb-small | --size-unit N] [--algorithm round_robin|least_connections]
                [--redirect-http-to-https] [--project P] [--wait] [--wait-timeout 10m]
docli lb delete NAME|ID                 (type the name, or --yes --confirm-name NAME)

--forward is ENTRY_PROTO:PORT=TARGET_PROTO:PORT[,cert=ID][,passthrough], for example http:80=http:8080 or
"https:443=http:8080,cert=CERT_ID". Names are stored as docli-NAME. The API reports no price for a load balancer; it
bills while it exists.`,
	"spaces": `docli spaces key list | create --name N (--grant BUCKET:read|readwrite|fullaccess ... | --full-access) --secret-file PATH [--force]
docli spaces key delete NAME|ACCESS_KEY_ID
docli spaces bucket list --region R | create --name N --region R | delete NAME --region R

Keys use the REST API; DigitalOcean's docs disagree about whether it can manage them, and docli says so if it cannot.
The secret key is written only to --secret-file (mode 0600, required, no overwrite without --force) and never printed.
Buckets use the S3-compatible API and need a Spaces access key with full access in DOCLI_SPACES_ACCESS_KEY and
DOCLI_SPACES_SECRET_KEY (or ~/.config/docli/spaces). Names are stored as docli-NAME. A bucket must be empty to be
deleted (docli never deletes objects). Names must be typed to delete.`,
	"token":   "docli token check\nChecks the token against the account and reports which resources it can read. The API does not list a token's scopes, and docli will not change anything to find out what it may write.",
	"version": "docli version [--json]",
}

func helpFor(cmd string) string {
	if h, ok := helpTopics[strings.Fields(cmd + " ")[0]]; ok {
		return h + "\n"
	}
	if cmd == "" {
		return helpTop
	}
	return ""
}
