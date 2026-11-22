package main

import (
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"text/tabwriter"
)

// printJSON writes v as indented JSON. Callers pass structs without omitempty and non-nil slices so the
// shape is the same every time.
func (r *Run) printJSON(v any) error {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	_, err = fmt.Fprintln(r.Stdout, string(b))
	return err
}

func printTable(w io.Writer, headers []string, rows [][]string) {
	tw := tabwriter.NewWriter(w, 0, 8, 2, ' ', 0)
	fmt.Fprintln(tw, strings.Join(headers, "\t"))
	for _, row := range rows {
		fmt.Fprintln(tw, strings.Join(row, "\t"))
	}
	tw.Flush()
}

func (r *Run) infof(format string, a ...any) {
	fmt.Fprint(r.Stderr, r.scrubSecrets(redact(fmt.Sprintf(format, a...), r.Token))+"\n")
}

func money(v float64) string { return fmt.Sprintf("$%.2f", v) }

func yesNo(b bool) string {
	if b {
		return "yes"
	}
	return "no"
}

func nonNil[T any](s []T) []T {
	if s == nil {
		return []T{}
	}
	return s
}

func orDash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}
