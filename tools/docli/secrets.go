package main

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// doPasswordRE matches the shape DigitalOcean database passwords have (they start with AVNS_). It is a second
// line of defence for a password docli was never shown; the main defence is that nothing ever prints one.
var doPasswordRE = regexp.MustCompile(`\bAVNS_[A-Za-z0-9_-]{6,}`)

// connURIRE matches a database connection string with credentials in it. Even a password docli never saw is
// removed from a message if it sits in one of these.
var connURIRE = regexp.MustCompile(`(?i)\b(postgres(?:ql)?|mysql|rediss?|mongodb(?:\+srv)?)://[^\s:/@]*:[^\s@]*@`)

// A Spaces secret key has no fixed prefix, so it cannot be recognised by shape: the main defence is that the value
// is registered with rememberSecret the moment it is read or created. These two patterns catch what a server might
// echo back from a signed request (the Signature= part of an Authorization header) and any secret_key-looking
// assignment, even for a key docli was never shown.
var (
	sigAuthRE   = regexp.MustCompile(`(?i)(\bSignature=)[0-9a-f]{16,}`)
	awsSecretRE = regexp.MustCompile(`(?i)(\b(?:secret[_-]?key|secret[_-]?access[_-]?key|aws[_-]?secret[_-]?access[_-]?key)["']?\s*[:=]\s*["']?)[A-Za-z0-9/+=_-]{16,}`)
)

// Database passwords are the one secret docli handles besides the token. The rules:
//   - a password is never printed, never put in --json, never logged, never written to the manifest;
//   - the only place it goes is a file the person names with --password-file, created with mode 0600;
//   - any password docli has seen is scrubbed from error text, even if the server echoes it back.

// rememberSecret registers a value to scrub from later output.
func (a *App) rememberSecret(s string) {
	if len(s) < 4 {
		return
	}
	for _, x := range a.secrets {
		if x == s {
			return
		}
	}
	a.secrets = append(a.secrets, s)
}

// noteSpacesSecret registers the Spaces secret key, when one is configured, before any command runs, so it is
// scrubbed even from a command that never uses it (a server could echo it back into any error).
func (a *App) noteSpacesSecret() {
	if c, err := a.loadSpacesCreds(); err == nil {
		a.rememberSecret(c.secret)
	}
}

func (a *App) scrubSecrets(msg string) string {
	for _, s := range a.secrets {
		msg = strings.ReplaceAll(msg, s, "[secret hidden]")
	}
	msg = connURIRE.ReplaceAllString(msg, "$1://[credentials hidden]@")
	msg = sigAuthRE.ReplaceAllString(msg, "$1[signature hidden]")
	msg = awsSecretRE.ReplaceAllString(msg, "$1[secret hidden]")
	return doPasswordRE.ReplaceAllString(msg, "[secret hidden]")
}

// checkSecretTarget is the early check, before anything is created: the file must not exist (unless --force),
// and its directory must exist.
func checkSecretTarget(path string, force bool) error {
	return checkSecretTargetFlag("--password-file", path, force)
}

// checkSecretTargetFlag is the same check for another flag's file (--secret-file for a Spaces key).
func checkSecretTargetFlag(flagName, path string, force bool) error {
	if path == "" {
		return nil
	}
	if fi, err := os.Stat(filepath.Dir(path)); err != nil || !fi.IsDir() {
		return fmt.Errorf("%s %s: the directory %s does not exist", flagName, path, filepath.Dir(path))
	}
	fi, err := os.Lstat(path)
	if err == nil {
		if !force {
			return fmt.Errorf("%s %s already exists; docli will not overwrite it without --force", flagName, path)
		}
		if !fi.Mode().IsRegular() {
			return fmt.Errorf("%s %s exists and is not a regular file; refusing to replace it", flagName, path)
		}
	}
	return nil
}

// writeSecretFile writes the secret exactly as given (no trailing newline) to a file only the owner can read.
// Without force the file is created exclusively, so a file that appears between the check and the write is
// still never overwritten. With force the old file is replaced through a temp file. Errors never contain the secret.
func writeSecretFile(path, secret string, force bool) error {
	if !force {
		f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
		if err != nil {
			return fmt.Errorf("cannot create %s: %v", path, pathErr(err))
		}
		if _, err := f.WriteString(secret); err != nil {
			f.Close()
			os.Remove(path)
			return fmt.Errorf("cannot write %s: %v", path, pathErr(err))
		}
		if err := f.Close(); err != nil {
			os.Remove(path)
			return fmt.Errorf("cannot write %s: %v", path, pathErr(err))
		}
		return os.Chmod(path, 0o600) // in case a umask or filesystem changed the mode
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".docli-secret-*.tmp") // CreateTemp makes files mode 0600
	if err != nil {
		return fmt.Errorf("cannot create a file next to %s: %v", path, pathErr(err))
	}
	name := tmp.Name()
	if _, err := tmp.WriteString(secret); err != nil {
		tmp.Close()
		os.Remove(name)
		return fmt.Errorf("cannot write %s: %v", path, pathErr(err))
	}
	if err := tmp.Close(); err != nil {
		os.Remove(name)
		return fmt.Errorf("cannot write %s: %v", path, pathErr(err))
	}
	if err := os.Chmod(name, 0o600); err != nil {
		os.Remove(name)
		return err
	}
	if err := os.Rename(name, path); err != nil {
		os.Remove(name)
		return fmt.Errorf("cannot replace %s: %v", path, pathErr(err))
	}
	return nil
}

func pathErr(err error) error {
	if pe, ok := err.(*os.PathError); ok {
		return pe.Err
	}
	return err
}
