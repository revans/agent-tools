package postlog

import (
	"flag"
	"fmt"
	"io"
)

// Options are the two switches every publishing command offers.
type Options struct {
	AllowDuplicate bool // publish even if the same text went out recently
	NoLog          bool // do not record this publication
}

// Flags registers --allow-duplicate and --no-log on a command's flag set.
func (o *Options) Flags(fs *flag.FlagSet) {
	fs.BoolVar(&o.AllowDuplicate, "allow-duplicate", false, "publish even if this exact text was already published to this account in the last 30 days")
	fs.BoolVar(&o.NoLog, "no-log", false, "do not record this in the post log")
}

// Before is the check a command makes just before it publishes. A repeat is an
// error; in a dry run, or if the ledger cannot be read, it is a warning on w.
func (o Options) Before(e Entry, dry bool, w io.Writer) error {
	// --no-log only stops the record. The repeat check still runs unless the
	// caller also passed --allow-duplicate.
	warn, err := Open("").Guard(e, o.AllowDuplicate, dry)
	if err != nil {
		return err
	}
	if warn != "" && w != nil {
		fmt.Fprintf(w, "%s: %s\n", e.Platform, warn)
	}
	return nil
}

// After records a publication that has just succeeded. A failure to write is
// reported on w and never turned into an error: the post is already out.
func (o Options) After(e Entry, w io.Writer) {
	if o.NoLog {
		return
	}
	if msg := Open("").Note(e); msg != "" && w != nil {
		fmt.Fprintf(w, "%s: %s\n", e.Platform, msg)
	}
}
