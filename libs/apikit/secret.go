package apikit

import (
	"fmt"
	"os"
	"os/exec"
	"strings"
)

// ResolveSecret reads one credential from an account table. A credential can be
// written three ways, and exactly one may be present:
//
//	key         = "literal value"
//	key_env     = "NAME_OF_ENV_VAR"
//	key_command = "pass show service/key"   (run with sh -c; stdout is the value)
//
// The _env and _command forms keep the secret out of the config file.
// Errors name the account and field, never the value.
func ResolveSecret(account string, t map[string]any, key string) (string, error) {
	lit, hasLit := t[key]
	env, hasEnv := t[key+"_env"]
	cmd, hasCmd := t[key+"_command"]
	n := 0
	for _, b := range []bool{hasLit, hasEnv, hasCmd} {
		if b {
			n++
		}
	}
	switch {
	case n == 0:
		return "", fmt.Errorf("account %q has no %s, %s_env or %s_command", account, key, key, key)
	case n > 1:
		return "", fmt.Errorf("account %q sets more than one of %s, %s_env, %s_command", account, key, key, key)
	}
	var val string
	switch {
	case hasLit:
		s, ok := lit.(string)
		if !ok {
			return "", fmt.Errorf("account %q: %s must be a string", account, key)
		}
		val = s
	case hasEnv:
		name, ok := env.(string)
		if !ok || name == "" {
			return "", fmt.Errorf("account %q: %s_env must name an environment variable", account, key)
		}
		val = os.Getenv(name)
		if val == "" {
			return "", fmt.Errorf("account %q: environment variable %s (for %s) is not set", account, name, key)
		}
	default:
		c, ok := cmd.(string)
		if !ok || c == "" {
			return "", fmt.Errorf("account %q: %s_command must be a command line", account, key)
		}
		var stderr strings.Builder
		run := exec.Command("sh", "-c", c)
		run.Stderr = &stderr
		out, err := run.Output()
		if err != nil {
			return "", fmt.Errorf("account %q: running %s_command failed: %v: %s", account, key, err, strings.TrimSpace(stderr.String()))
		}
		val = string(out)
	}
	val = strings.TrimSpace(val)
	if val == "" {
		return "", fmt.Errorf("account %q: %s is empty", account, key)
	}
	return val, nil
}

// OptString returns an optional plain (non-secret) string setting.
func OptString(t map[string]any, key, def string) string {
	if s, ok := t[key].(string); ok && s != "" {
		return s
	}
	return def
}
