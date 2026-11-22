module github.com/revans/agent-tools/facebook

go 1.25.4

require github.com/revans/agent-tools/apikit v0.0.0

require github.com/pelletier/go-toml/v2 v2.4.3 // indirect

replace github.com/revans/agent-tools/apikit => ../../libs/apikit

require github.com/revans/agent-tools/postlog v0.0.0

replace github.com/revans/agent-tools/postlog => ../../libs/postlog
