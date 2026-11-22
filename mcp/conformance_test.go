package main

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestEveryManifestFlagExistsInTheRealCLI runs each built-in tool, with a value
// for every parameter, against the real binary. A manifest is a hand-typed list of
// flags, so a misspelt one passes every other test and only fails when an
// assistant calls the tool. The run uses an empty home, an empty working
// directory and a minimal environment, so no credential is found and nothing
// reaches a real service. Only "that flag or command does not exist" fails the
// test; every other error (no config, an invalid sample id) is the CLI doing its
// job after it understood the arguments.
//
// It needs the binaries built (make build). Without them the tool is skipped.
func TestEveryManifestFlagExistsInTheRealCLI(t *testing.T) {
	bin, err := filepath.Abs("../target/release")
	if err != nil {
		t.Fatal(err)
	}
	tools, err := loadTools(nil)
	if err != nil {
		t.Fatal(err)
	}
	home := t.TempDir()
	work := t.TempDir()
	// wharf reads .wharf before it looks at its command, so without one it
	// never gets as far as the arguments. This one points at 127.0.0.1 port 1,
	// where nothing listens: the connection is refused and nothing real is touched.
	if err := os.WriteFile(filepath.Join(work, ".wharf"), []byte("host = \"127.0.0.1\"\nport = 1\nusername = \"u\"\npassword = \"p\"\nremote_base_path = \"/x\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	env := []string{
		"HOME=" + home,
		"XDG_CONFIG_HOME=" + filepath.Join(home, "cfg"),
		"XDG_STATE_HOME=" + filepath.Join(home, "state"),
		"PATH=" + bin + ":/usr/bin:/bin",
	}
	notFlags := []string{"flag provided but not defined", "unexpected argument", "unrecognized option", "unrecognized argument",
		"unknown flag", "unknown option", "unknown command", "unrecognized subcommand", "invalid subcommand", "unknown subcommand",
		"wharf - sftp deployment tool"} // wharf prints its usage banner for a command it does not know
	checked, skipped := 0, 0
	for _, tl := range tools {
		tl := tl
		path := filepath.Join(bin, tl.Bin)
		if _, err := os.Stat(path); err != nil {
			skipped++
			t.Logf("skipped %s: %s is not built", tl.Name, tl.Bin)
			continue
		}
		t.Run(tl.Name, func(t *testing.T) {
			args := map[string]any{}
			for _, p := range tl.Params {
				switch p.Type {
				case "integer":
					args[p.Name] = 1
				case "boolean":
					args[p.Name] = true
				case "map":
					args[p.Name] = map[string]string{"k": "v"}
				default:
					if len(p.Enum) > 0 {
						args[p.Name] = p.Enum[0]
					} else {
						args[p.Name] = "sample1"
					}
				}
			}
			raw, _ := json.Marshal(args)
			argv, err := tl.buildArgs(raw)
			if err != nil {
				t.Fatalf("the manifest's own arguments do not build: %v", err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
			defer cancel()
			cmd := exec.CommandContext(ctx, path, argv...)
			cmd.Dir, cmd.Env = work, env
			out, runErr := cmd.CombinedOutput()
			low := strings.ToLower(string(out))
			// Exit 2 is "usage error" in docli, the notif tools and the apikit tools.
			// "invalid value" is the exception: the flag exists and the CLI rejected
			// my placeholder for it (a numeric id, say), which proves the flag is real.
			// A tool that uses 2 for something else (docli plan: "there are changes")
			// lists it in ok_exit and is judged by its message alone.
			usageExit := true
			for _, c := range tl.OKExit {
				if c == 2 {
					usageExit = false
				}
			}
			if ee, ok := runErr.(*exec.ExitError); ok && ee.ExitCode() == 2 && usageExit && !strings.Contains(low, "invalid value") {
				t.Errorf("%s %s\n  exited 2, a usage error: the CLI did not accept the manifest's arguments:\n%s", tl.Bin, strings.Join(argv, " "), out)
				return
			}
			for _, bad := range notFlags {
				if strings.Contains(low, bad) {
					t.Errorf("%s %s\n  the CLI rejected the manifest's arguments (%q):\n%s", tl.Bin, strings.Join(argv, " "), bad, out)
					return
				}
			}
			checked++
		})
	}
	if checked == 0 {
		t.Skip("no CLI binaries are built; run `make build` first")
	}
	t.Logf("checked %d tools against their real CLIs, skipped %d", checked, skipped)
}
