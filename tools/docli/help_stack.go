package main

// Help for milestone 3. Added to the topics in help.go when the program starts.
func init() {
	helpTopics["stack"] = `docli stack init [--file PATH] [--force]       write a commented example stack file (default config/deploys/stack.yml)
docli stack validate [--file PATH]              check the file alone: unknown keys, secrets, references, names; no token needed

A stack file declares ssh_keys, vpcs, firewalls, droplets, domains (with DNS records), databases (with users,
databases and trusted sources), projects, volumes and load_balancers, and the references between them by name.
Reserved IPs and Spaces are not stack file kinds (use docli reserved-ip and docli spaces). Parsing is strict: unknown keys,
duplicate keys, keys that look like secrets (password, token, ...) and values such as $VAR or ${VAR} are errors
with the line number. Nothing is read from the environment. Relative paths in the file are from the project root.`

	helpTopics["plan"] = `docli plan [--file PATH] [--prune [--prune-data]] [--json]

Reads the stack file and the account (read requests only) and prints what apply would create, change or leave
alone, and the added monthly cost. It never changes anything and never writes the manifest.
Exit code: 0 no changes, 1 error (a bad stack file, a failed read, a mistake in plan's own flags), 2 changes.
Resources docli created that the file no longer lists are shown as "orphaned, not deleted". Things in the account
that docli did not create are only reported. --prune shows the deletions that apply --prune would make.`

	helpTopics["apply"] = `docli apply [--file PATH] [--yes] [--dry-run] [--wait-timeout 5m] [--db-wait-timeout 20m]
            [--prune [--prune-data] [--confirm-prune prune] [--prune-confirm-name NAME]...]

Runs the plan, prints it, asks (default no) and then does the steps in a fixed order: projects, SSH keys, VPCs,
firewalls, droplets (and attaching them to firewalls), volumes, load balancers, databases (resize, trusted sources,
users, databases), then DNS.
It waits for droplets, and for a new database only when a later step needs it, stops at the first failure and says
exactly what was done and what was not, never retries a create, updates the manifest after each step, and is safe
to run again. Changes that cannot be made in place (droplet region, image or size; database engine, version,
region or VPC) are reported as "needs replacement, not done" and never done.
Nothing is deleted unless you pass --prune, and then only resources docli created that this project's manifest
lists. --prune also needs the typed word "prune" (or --confirm-prune prune), the name of each droplet, database
and domain typed (--prune-confirm-name NAME without a terminal), and --prune-data to delete a droplet or database.`

	helpTopics["adopt"] = `docli adopt droplet NAME|ID [--stack NAME]     docli adopt database NAME|ID [--stack NAME]
docli adopt volume NAME|ID                        docli adopt reserved-ip IP
docli adopt domain NAME                           docli adopt record DOMAIN RECORD_ID

Puts something that already exists under docli's management. Droplets, databases and volumes get the tag docli:managed
(and docli:stack:NAME): nothing else about them changes. Domains, records and reserved IPs cannot be tagged, so adopting one only
records it in the manifest. VPCs, firewalls, SSH keys, projects and load balancers are not adopted: docli knows its own by a name prefix and
will not rename what it did not create. Shows a plan and asks first; --dry-run shows it and changes nothing.`
}
