package main

import (
	"crypto/md5"
	"encoding/base64"
	"fmt"
	"regexp"
	"strings"

	"github.com/digitalocean/godo"
)

var labelRE = regexp.MustCompile(`^[A-Za-z0-9]([A-Za-z0-9-]{0,61}[A-Za-z0-9])?$`)

// validateHostname checks a droplet name is a valid hostname (RFC 1123).
func validateHostname(n string) error {
	if n == "" {
		return fmt.Errorf("a droplet name is required")
	}
	if len(n) > 253 {
		return fmt.Errorf("the name %q is too long for a hostname (253 characters at most)", n)
	}
	for _, l := range strings.Split(n, ".") {
		if !labelRE.MatchString(l) {
			return fmt.Errorf("%q is not a valid hostname: use letters, digits and hyphens (not at the start or end of a part), each dot-separated part 1-63 characters, no spaces or underscores", n)
		}
	}
	return nil
}

var tagRE = regexp.MustCompile(`^[A-Za-z0-9:_-]{1,255}$`)

func validateTag(t string) error {
	if !tagRE.MatchString(t) {
		return fmt.Errorf("%q is not a valid tag: use letters, digits, colon, dash and underscore only (up to 255 characters)", t)
	}
	return nil
}

var nameRE = regexp.MustCompile(`^[A-Za-z0-9._-]{1,64}$`)

func validateResourceName(kind, n string) error {
	if !nameRE.MatchString(n) {
		return fmt.Errorf("%q is not a valid %s name: use letters, digits, dot, dash and underscore (up to 64 characters)", n, kind)
	}
	return nil
}

// Cost is the price of a droplet size as the API reports it.
type Cost struct {
	Monthly        float64
	Hourly         float64
	BackupsMonthly float64 // an estimate: DigitalOcean charges 20% of the droplet price; the sizes list does not say so
}

const backupsFraction = 0.20

func estimateCost(s godo.Size, backups bool) Cost {
	c := Cost{Monthly: s.PriceMonthly, Hourly: s.PriceHourly}
	if backups {
		c.BackupsMonthly = round2(s.PriceMonthly * backupsFraction)
	}
	return c
}

func (c Cost) Total() float64 { return round2(c.Monthly + c.BackupsMonthly) }

func round2(v float64) float64 { return float64(int(v*100+0.5)) / 100 }

// parsePublicKey checks a public key line and computes DigitalOcean's fingerprint (MD5 of the key blob).
// It refuses private keys outright so one can never be uploaded or echoed.
func parsePublicKey(text string) (line, keyType, fingerprint string, err error) {
	t := strings.TrimSpace(text)
	if strings.Contains(t, "PRIVATE KEY") {
		return "", "", "", fmt.Errorf("that looks like a PRIVATE key. Never upload it. Give the public half (the .pub file)")
	}
	f := strings.Fields(t)
	if len(f) < 2 {
		return "", "", "", fmt.Errorf("that is not an SSH public key (expected: ssh-ed25519 AAAA... [comment])")
	}
	ok := f[0] == "ssh-rsa" || f[0] == "ssh-ed25519" || f[0] == "ssh-dss" || strings.HasPrefix(f[0], "ecdsa-sha2-") || strings.HasPrefix(f[0], "sk-")
	if !ok {
		return "", "", "", fmt.Errorf("unknown SSH key type %q (expected ssh-ed25519, ssh-rsa or ecdsa-sha2-...)", f[0])
	}
	blob, err := base64.StdEncoding.DecodeString(f[1])
	if err != nil || len(blob) < 8 {
		return "", "", "", fmt.Errorf("the key data is not valid base64")
	}
	sum := md5.Sum(blob)
	var parts []string
	for _, b := range sum {
		parts = append(parts, fmt.Sprintf("%02x", b))
	}
	return strings.Join(f, " "), f[0], strings.Join(parts, ":"), nil
}
