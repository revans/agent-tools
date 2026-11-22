package main

import "testing"

func TestAnExampleReachesTheSchemaTheModelSees(t *testing.T) {
	tl := mustTool(t, `
[[tool]]
name = "demo"
description = "d"
bin = "x"
safety = "read"
  [[tool.param]]
  name = "since"
  type = "string"
  flag = "--since"
  description = "s"
  example = "7d"
  [[tool.param]]
  name = "plain"
  type = "string"
  flag = "--plain"
  description = "p"
`)
	props := tl.inputSchema()["properties"].(map[string]any)
	ex, _ := props["since"].(map[string]any)["examples"].([]any)
	if len(ex) != 1 || ex[0] != "7d" {
		t.Errorf("examples: %v", props["since"])
	}
	if _, has := props["plain"].(map[string]any)["examples"]; has {
		t.Error("a parameter with no example advertises none")
	}
}
