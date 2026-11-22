---
description = "Deploy announcement for a service"
channel = "releases"
icon_emoji = ":rocket:"

[vars]
service = "Name of the deployed service"
version = "Version or tag that went out"
env = { description = "Target environment", default = "production" }
notes = { description = "Optional extra detail", default = "" }
---
## Deployed {{service}} {{version}}

**Environment:** {{env}}

{{notes}}
