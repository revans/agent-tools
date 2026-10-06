---
description = "Incident status update"
chat = "alerts"

[vars]
title = "Short incident title"
severity = { description = "sev1 | sev2 | sev3", default = "sev2" }
status = { description = "investigating | identified | monitoring | resolved", default = "investigating" }
summary = "What is happening, in one or two sentences"
owner = { description = "Who is driving", default = "unassigned" }
---
## 🚨 {{severity}} — {{title}}

**Status:** {{status}}
**Owner:** {{owner}}

{{summary}}
