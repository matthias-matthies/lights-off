package lightsoff

import (
	"cmp"
	"fmt"
	"io"
	"slices"
	"strings"
)

// outcome is how one Ticket Attempt ended, for the run's summary.
type outcome struct {
	ticket      int
	code        int // the exit code it calls for
	merged      bool
	interrupted bool // by Ctrl+C, and needs says where that left it
	pr          int
	needs       string   // what a person needs to look at; empty if nothing
	kept        []string // sandboxes kept for that person
}

// printSummary shows what merged and what needs a person, ticket by ticket, and
// returns the same text so the Dashboard can end on it.
func printSummary(w io.Writer, outcomes []outcome, logDir string) string {
	slices.SortStableFunc(outcomes, func(a, b outcome) int { return cmp.Compare(a.ticket, b.ticket) })
	var b strings.Builder
	b.WriteString("lights-off: summary\n")
	if len(outcomes) == 0 {
		b.WriteString("  no ticket started\n")
	}
	for _, o := range outcomes {
		if o.merged {
			fmt.Fprintf(&b, "  merged: #%d, pull request #%d\n", o.ticket, o.pr)
		}
		switch {
		case o.interrupted:
			fmt.Fprintf(&b, "  interrupted: #%d, %s\n", o.ticket, o.needs)
		case o.needs != "":
			fmt.Fprintf(&b, "  needs a person: #%d, %s\n", o.ticket, o.needs)
		}
		if len(o.kept) > 0 {
			fmt.Fprintf(&b, "    sandboxes kept: %s\n", strings.Join(o.kept, ", "))
		}
	}
	fmt.Fprintf(&b, "  logs: %s\n", logDir)
	summary := b.String()
	io.WriteString(w, summary)
	return summary
}
