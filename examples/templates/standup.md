---
description = "Daily standup note"
channel = "standup"

[vars]
who = "Person posting the update"
yesterday = "What happened yesterday"
today = "Plan for today"
blockers = { description = "Blockers, if any", default = "None" }
---
## Standup — {{who}}

**Yesterday**
{{yesterday}}

**Today**
{{today}}

**Blockers:** {{blockers}}
