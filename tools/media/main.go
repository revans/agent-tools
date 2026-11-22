// media prepares images for a platform: it lists profiles, inspects and checks
// an image against one, and writes a fitted copy. It is purely local: no
// network, no credentials, no accounts.
package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
)

var version = "0.1.0"

type usageError struct{ msg string }

func (u usageError) Error() string { return u.msg }

const usageText = `media: prepare an image for a platform (instagram, x, linkedin, or your own profile)

Usage: media COMMAND [flags]

Commands:
  profiles                   list the profiles
  info FILE                  format, size, alpha, animation and EXIF orientation
  check --for PROFILE FILE   does the file already satisfy the profile? (exit 1 if not)
  fit --for PROFILE FILE     write a copy that satisfies the profile
        [--out PATH] [--mode pad|crop] [--background #RRGGBB] [--force]

Output is JSON. Errors go to stderr; exit 0 ok, 1 error, 2 usage.
Extra profiles: ~/.config/agent-tools/media/profiles.d/*.toml and $MEDIA_PROFILES.
`

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

func run(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprint(stderr, usageText)
		return 2
	}
	var err error
	switch args[0] {
	case "help", "-h", "--help":
		fmt.Fprint(stdout, usageText)
		return 0
	case "version", "--version":
		fmt.Fprintln(stdout, "media", version)
		return 0
	case "profiles":
		err = cmdProfiles(args[1:], stdout)
	case "info":
		err = cmdInfo(args[1:], stdout)
	case "check":
		var ok bool
		ok, err = cmdCheck(args[1:], stdout)
		if err == nil && !ok {
			return 1
		}
	case "fit":
		err = cmdFit(args[1:], stdout)
	default:
		fmt.Fprintf(stderr, "media: unknown command %q. Run `media help`.\n", args[0])
		return 2
	}
	if err == nil {
		return 0
	}
	if errors.Is(err, flag.ErrHelp) {
		return 0
	}
	var ue usageError
	if errors.As(err, &ue) {
		fmt.Fprintf(stderr, "media: %s\n", ue.msg)
		return 2
	}
	fmt.Fprintf(stderr, "media: %v\n", err)
	return 1
}

func printJSON(w io.Writer, v any) error {
	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	return enc.Encode(v)
}

// parseFlags parses flags that may come before or after the positional FILE.
// Everything after "--" is positional.
func parseFlags(fs *flag.FlagSet, args []string) ([]string, error) {
	fs.SetOutput(io.Discard)
	var pos []string
	for i, a := range args {
		if a == "--" {
			pos = append(pos, args[i+1:]...)
			args = args[:i]
			break
		}
	}
	for {
		if err := fs.Parse(args); err != nil {
			if errors.Is(err, flag.ErrHelp) {
				return nil, err
			}
			return nil, usageError{err.Error()}
		}
		if fs.NArg() == 0 {
			break
		}
		pos = append(pos, fs.Arg(0))
		args = fs.Args()[1:]
	}
	return pos, nil
}

func oneFile(pos []string) (string, error) {
	if len(pos) != 1 {
		return "", usageError{"expected exactly one FILE"}
	}
	return pos[0], nil
}

func cmdProfiles(args []string, out io.Writer) error {
	fs := flag.NewFlagSet("profiles", flag.ContinueOnError)
	pos, err := parseFlags(fs, args)
	if err != nil {
		return err
	}
	if len(pos) != 0 {
		return usageError{"profiles takes no arguments"}
	}
	ps, err := loadProfiles()
	if err != nil {
		return err
	}
	return printJSON(out, map[string]any{"profiles": ps})
}

func cmdInfo(args []string, out io.Writer) error {
	fs := flag.NewFlagSet("info", flag.ContinueOnError)
	pos, err := parseFlags(fs, args)
	if err != nil {
		return err
	}
	f, err := oneFile(pos)
	if err != nil {
		return err
	}
	l, err := load(f)
	if err != nil {
		return err
	}
	return printJSON(out, l.Info)
}

func cmdCheck(args []string, out io.Writer) (bool, error) {
	fs := flag.NewFlagSet("check", flag.ContinueOnError)
	name := fs.String("for", "", "profile name")
	pos, err := parseFlags(fs, args)
	if err != nil {
		return false, err
	}
	if *name == "" {
		return false, usageError{"--for PROFILE is required"}
	}
	f, err := oneFile(pos)
	if err != nil {
		return false, err
	}
	p, err := findProfile(*name)
	if err != nil {
		return false, err
	}
	l, err := load(f)
	if err != nil {
		return false, err
	}
	problems := checkInfo(p, l.Info)
	ok := len(problems) == 0
	return ok, printJSON(out, map[string]any{"profile": p.Name, "ok": ok, "problems": problems})
}

func cmdFit(args []string, out io.Writer) error {
	fs := flag.NewFlagSet("fit", flag.ContinueOnError)
	name := fs.String("for", "", "profile name")
	outPath := fs.String("out", "", "output path")
	mode := fs.String("mode", "pad", "pad or crop")
	bg := fs.String("background", "#FFFFFF", "background color #RRGGBB")
	force := fs.Bool("force", false, "overwrite an existing output")
	pos, err := parseFlags(fs, args)
	if err != nil {
		return err
	}
	if *name == "" {
		return usageError{"--for PROFILE is required"}
	}
	f, err := oneFile(pos)
	if err != nil {
		return err
	}
	if *mode != "pad" && *mode != "crop" {
		return usageError{"--mode must be pad or crop"}
	}
	c, err := parseBackground(*bg)
	if err != nil {
		return usageError{err.Error()}
	}
	p, err := findProfile(*name)
	if err != nil {
		return err
	}
	res, err := fitFile(p, f, fitOptions{Out: *outPath, Mode: *mode, Background: c, BGHex: *bg, Force: *force})
	if err != nil {
		return err
	}
	return printJSON(out, res)
}
