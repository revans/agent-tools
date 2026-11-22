// Package cli holds wharf's small command-line argument helpers.
package cli

import (
	"fmt"
	"strings"
)

// Flag kinds for Parse.
const (
	Bool  = "bool"
	Value = "value"
)

// Parsed is the result of Parse.
type Parsed struct {
	Args  []string          // positional arguments, in order
	Bools map[string]bool   // boolean flags that were present
	Vals  map[string]string // value flags (last occurrence wins)
}

// Parse splits args into positional arguments and flags. allowed maps each
// accepted flag name (with leading dashes, e.g. "--dry-run") to Bool or
// Value. A value flag accepts both "--name value" and "--name=value".
// Flags may appear before, between, or after positional arguments. Any
// other argument starting with "-" is an error, so a typo such as
// "--dry-rn" fails loudly instead of being treated as a file name.
// A literal "--" ends flag parsing.
func Parse(args []string, allowed map[string]string) (*Parsed, error) {
	p := &Parsed{Bools: map[string]bool{}, Vals: map[string]string{}}
	for i := 0; i < len(args); i++ {
		a := args[i]
		if a == "--" {
			p.Args = append(p.Args, args[i+1:]...)
			break
		}
		if !strings.HasPrefix(a, "-") || a == "-" {
			p.Args = append(p.Args, a)
			continue
		}
		name, val, hasVal := a, "", false
		if eq := strings.Index(a, "="); eq > 0 && strings.HasPrefix(a, "--") {
			name, val, hasVal = a[:eq], a[eq+1:], true
		}
		kind, ok := allowed[name]
		if !ok {
			return nil, fmt.Errorf("unknown flag %s", name)
		}
		switch kind {
		case Bool:
			if hasVal {
				return nil, fmt.Errorf("flag %s does not take a value", name)
			}
			p.Bools[name] = true
		case Value:
			if !hasVal {
				if i+1 >= len(args) {
					return nil, fmt.Errorf("flag %s needs a value", name)
				}
				i++
				val = args[i]
			}
			p.Vals[name] = val
		}
	}
	return p, nil
}
