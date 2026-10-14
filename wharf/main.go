// Command wharf is a small SFTP deployment tool for static sites.
// See README.md for the full command reference.
package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"wharf/internal/commands"
	"wharf/internal/config"
	"wharf/internal/logging"
)

func usage() {
	fmt.Fprint(os.Stderr, `wharf - SFTP deployment tool for static sites

Usage:
  wharf explore                    Walk the remote SFTP tree and write WHARF.md at the repo root
  wharf copy [flags] <local-file>  Upload a single local file, mirroring its repo-relative path
  wharf sync [flags]               Find local files that are new/changed vs remote and interactively upload them
  wharf deploy [flags]             Run explore, then sync, in one step
  wharf delete [flags] <remote-path>
                                    Delete a remote file or directory (asks for confirmation; see README)
  wharf move [flags] <from> <to>   Rename/move a remote file or directory (never overwrites)
  wharf archive [flags] [--keep <name>]...
                                    Move everything at the site root into archive/<timestamp>/

Flags:
  --dry-run    copy/sync/deploy/delete/move/archive: show what would happen, make no changes (all but copy still connect to inspect)
  --keep NAME  archive: leave this top-level entry in place (repeatable; hidden entries and archive/ are always kept)
  --no-backup  copy/sync/deploy: skip the automatic pre-overwrite backup of an existing remote file

Config:
  Reads connection details from a .wharf TOML file at the site repo root.
  See .wharf.example for the expected format.
`)
}

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(1)
	}

	command := os.Args[1]
	if command == "-h" || command == "--help" || command == "help" {
		usage()
		return
	}

	cwd, err := os.Getwd()
	if err != nil {
		fmt.Fprintf(os.Stderr, "wharf: %v\n", err)
		os.Exit(1)
	}

	repoRoot, err := config.FindRepoRoot(cwd)
	if err != nil {
		fmt.Fprintf(os.Stderr, "wharf: %v\n", err)
		os.Exit(1)
	}

	logDir := filepath.Join(repoRoot, "scripts", "wharf", "log")
	logger, err := logging.New(logDir, command)
	if err != nil {
		fmt.Fprintf(os.Stderr, "wharf: %v\n", err)
		os.Exit(1)
	}

	runErr := run(command, repoRoot, logger)
	logger.Close(runErr)

	if runErr != nil {
		fmt.Fprintf(os.Stderr, "wharf: %v\n", runErr)
		fmt.Fprintf(os.Stderr, "See %s for full details.\n", logger.Path)
		os.Exit(1)
	}
}

// extractFlags pulls --dry-run and --no-backup out of args (they can
// appear anywhere, before or after the positional argument) and returns
// the remaining positional arguments alongside the two flag values.
func extractFlags(args []string) (remaining []string, dryRun, noBackup bool) {
	for _, a := range args {
		switch a {
		case "--dry-run":
			dryRun = true
		case "--no-backup":
			noBackup = true
		default:
			remaining = append(remaining, a)
		}
	}
	return remaining, dryRun, noBackup
}

// extractKeep pulls every --keep NAME / --keep=NAME pair out of args and
// returns the remaining arguments plus the collected names.
func extractKeep(args []string) (remaining, keep []string, err error) {
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch {
		case a == "--keep":
			if i+1 >= len(args) {
				return nil, nil, fmt.Errorf("--keep needs a name")
			}
			keep = append(keep, args[i+1])
			i++
		case strings.HasPrefix(a, "--keep="):
			keep = append(keep, strings.TrimPrefix(a, "--keep="))
		default:
			remaining = append(remaining, a)
		}
	}
	return remaining, keep, nil
}

func run(command, repoRoot string, logger *logging.Logger) error {
	cfg, err := config.Load(repoRoot)
	if err != nil {
		logger.Error("failed to load config: %v", err)
		return err
	}

	switch command {
	case "explore":
		args, _, _ := extractFlags(os.Args[2:])
		if len(args) != 0 {
			return fmt.Errorf("explore takes no arguments")
		}
		return commands.Explore(cfg, repoRoot, logger)

	case "copy":
		args, dryRun, noBackup := extractFlags(os.Args[2:])
		if len(args) != 1 {
			return fmt.Errorf("copy requires exactly one argument: <local-file> (optionally with --dry-run / --no-backup)")
		}
		return commands.Copy(cfg, repoRoot, args[0], dryRun, noBackup, logger)

	case "sync":
		args, dryRun, noBackup := extractFlags(os.Args[2:])
		if len(args) != 0 {
			return fmt.Errorf("sync takes no positional arguments (optionally --dry-run / --no-backup)")
		}
		return commands.Sync(cfg, repoRoot, dryRun, noBackup, logger, os.Stdin, os.Stdout)

	case "deploy":
		args, dryRun, noBackup := extractFlags(os.Args[2:])
		if len(args) != 0 {
			return fmt.Errorf("deploy takes no positional arguments (optionally --dry-run / --no-backup)")
		}
		return commands.Deploy(cfg, repoRoot, dryRun, noBackup, logger, os.Stdin, os.Stdout)

	case "delete":
		args, dryRun, noBackup := extractFlags(os.Args[2:])
		if noBackup {
			return fmt.Errorf("delete does not accept --no-backup; you'll be prompted interactively about backing up before every delete")
		}
		if len(args) != 1 {
			return fmt.Errorf("delete requires exactly one argument: <remote-path> (optionally with --dry-run)")
		}
		return commands.Delete(cfg, repoRoot, args[0], dryRun, logger, os.Stdin, os.Stdout)

	case "move":
		args, dryRun, noBackup := extractFlags(os.Args[2:])
		if noBackup {
			return fmt.Errorf("move does not accept --no-backup; a move transfers no data and nothing is overwritten")
		}
		if len(args) != 2 {
			return fmt.Errorf("move requires exactly two arguments: <from> <to> (optionally with --dry-run)")
		}
		return commands.Move(cfg, args[0], args[1], dryRun, logger, os.Stdin, os.Stdout)

	case "archive":
		rest, keep, err := extractKeep(os.Args[2:])
		if err != nil {
			return err
		}
		args, dryRun, noBackup := extractFlags(rest)
		if noBackup {
			return fmt.Errorf("archive does not accept --no-backup; the archive itself is the backup")
		}
		if len(args) != 0 {
			return fmt.Errorf("archive takes no positional arguments (optionally --dry-run and --keep NAME)")
		}
		return commands.Archive(cfg, keep, dryRun, logger, os.Stdin, os.Stdout)

	default:
		usage()
		return fmt.Errorf("unknown command %q", command)
	}
}
