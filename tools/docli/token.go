package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

// loadToken finds the API token. Order: DOCLI_TOKEN, DIGITALOCEAN_TOKEN, then ~/.config/docli/token
// (which must not be readable by anyone else). It is never read from a flag.
func (a *App) loadToken() (string, error) {
	for _, name := range []string{"DOCLI_TOKEN", "DIGITALOCEAN_TOKEN"} {
		if v := strings.TrimSpace(a.Getenv(name)); v != "" {
			return v, nil
		}
	}
	home, err := a.HomeDir()
	if err != nil {
		return "", errors.New("no token found: set DIGITALOCEAN_TOKEN (or DOCLI_TOKEN), or put it in ~/.config/docli/token with mode 0600")
	}
	path := filepath.Join(home, ".config", "docli", "token")
	fi, err := os.Stat(path)
	if err != nil {
		if os.IsNotExist(err) {
			return "", errors.New("no token found: set DIGITALOCEAN_TOKEN (or DOCLI_TOKEN), or put it in ~/.config/docli/token with mode 0600")
		}
		return "", fmt.Errorf("cannot read the token file %s: %v", path, err)
	}
	if runtime.GOOS != "windows" && fi.Mode().Perm()&0o077 != 0 {
		return "", fmt.Errorf("the token file %s can be read by other users (mode %04o); refusing to use it. Run: chmod 600 %s", path, fi.Mode().Perm(), path)
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("cannot read the token file %s: %v", path, err)
	}
	tok := strings.TrimSpace(string(b))
	if tok == "" {
		return "", fmt.Errorf("the token file %s is empty", path)
	}
	return tok, nil
}

// redact removes the token from any text that might be shown.
func redact(s, token string) string {
	if token == "" {
		return s
	}
	return strings.ReplaceAll(s, token, "[token hidden]")
}
