package main

import (
	"os"
	"testing"

	"github.com/revans/agent-tools/postlog"
)

// No test may touch the real post log. Every package test starts with the log
// off; a test that wants one points it at a temp file with t.Setenv.
func TestMain(m *testing.M) {
	os.Setenv(postlog.EnvPath, "off")
	os.Exit(m.Run())
}
