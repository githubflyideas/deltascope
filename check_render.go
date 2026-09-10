package main

import (
	"fmt"
	"io"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/githubflyideas/deltascope/internal/reasoning"
)

// allStateIDs is the catalog's own order, which groups by domain -- the same
// order the web UI lists states in, so the two views can be read side by side.
func allStateIDs() []string {
	out := make([]string, 0, len(reasoning.States))
	for _, st := range reasoning.States {
		out = append(out, st.ID)
	}
	return out
}

// renderCheck prints the check report as three lists in a fixed order:
// what fired, what it means, and what could not be judged. The third list is
// not an appendix -- it is the difference between "your machine is fine" and
// "I looked at 41 of 78 things and they were fine", and it prints even when
// everything is quiet.
func renderCheck(w io.Writer, rep checkReport, showAll, color bool) {
	c := newPalette(color)

	fmt.Fprintf(w, "%s%s%s · %d samples over %s · %d cores · %d metric rows\n",
		c.bold, rep.Host, c.reset, rep.Samples,
		roundDur(time.Duration(rep.ElapsedSec*float64(time.Second))), rep.NCPU, rep.Rows)

	tag, tc := "OK", c.green
	switch rep.Severity {
	case "crit":
		tag, tc = "CRIT", c.red
	case "warn":
		tag, tc = "WARN", c.yellow
	case "unknown":
		tag, tc = "NOT MEASURED", c.dim
	}
	fmt.Fprintf(w, "%s%s[%s]%s %s%s%s\n\n", tc, c.bold, tag, c.reset, c.bold, rep.Headline, c.reset)

	if len(rep.Diagnoses) > 0 {
		fmt.Fprintf(w, "%sdiagnoses (%d)%s\n", c.bold, len(rep.Diagnoses), c.reset)
		for _, r := range rep.Diagnoses {
			mark, mc := "·", c.dim
			switch r.Severity {
			case "crit":
				mark, mc = "!", c.red
			case "warn":
				mark, mc = "~", c.yellow
			}
			lead := ""
			if !r.IsRoot {
				// A consequence printed at the same level as its cause reads
				// as a second, separate problem.
				lead = "  " + c.dim + "↳ caused by " + strings.Join(r.DownstreamOf, ", ") + c.reset + "\n    "
			}
			fmt.Fprintf(w, "%s%s%s %s%s\n", lead, mc, mark, r.Conclusion, c.reset)
			if len(r.Evidence) > 0 {
				fmt.Fprintf(w, "      %s%s%s\n", c.dim, strings.Join(r.Evidence, " · "), c.reset)
			}
			if len(r.Next) > 0 {
				fmt.Fprintf(w, "      %snext: %s%s\n", c.dim, strings.Join(r.Next, "  |  "), c.reset)
			}
		}
		fmt.Fprintln(w)
	}

	if len(rep.Active) > 0 {
		fmt.Fprintf(w, "%sstates that hold (%d)%s\n", c.bold, len(rep.Active), c.reset)
		for _, a := range rep.Active {
			fmt.Fprintf(w, "  %-38s %s%s%s\n", a.ID, c.dim, strings.Join(a.Evidence, " · "), c.reset)
		}
		fmt.Fprintln(w)
	}

	if len(rep.Counts) > 0 {
		// These are raw window counts, not rates, and they are printed
		// separately for that reason: several states threshold the rate of an
		// event that matters at all once.
		fmt.Fprintf(w, "%sevents during the window%s %s(counts, not rates)%s\n", c.bold, c.reset, c.dim, c.reset)
		for _, k := range sortedCountKeys(rep.Counts) {
			fmt.Fprintf(w, "  %-38s %s\n", k, trimCount(rep.Counts[k]))
		}
		fmt.Fprintln(w)
	}

	if len(rep.Unevaluated) > 0 {
		fmt.Fprintf(w, "%snot measured (%d)%s %s-- these are unknown, not fine%s\n",
			c.bold, len(rep.Unevaluated), c.reset, c.dim, c.reset)
		byReason := groupByReason(rep.Unevaluated)
		for _, g := range byReason {
			fmt.Fprintf(w, "  %s%s%s\n", c.dim, g.reason, c.reset)
			for _, id := range g.ids {
				if d := g.detail[id]; d != "" {
					fmt.Fprintf(w, "    %-38s %s%s%s\n", id, c.dim, d, c.reset)
					continue
				}
				fmt.Fprintf(w, "    %s\n", id)
			}
		}
		if hint := sampleHint(rep); hint != "" {
			fmt.Fprintf(w, "  %s%s%s\n", c.dim, hint, c.reset)
		}
		fmt.Fprintln(w)
	}

	if showAll {
		fmt.Fprintf(w, "%schecked and did not hold (%d)%s\n", c.bold, quietCount(rep), c.reset)
		for _, id := range quietStates(rep) {
			fmt.Fprintf(w, "  %s%s%s\n", c.dim, id, c.reset)
		}
		fmt.Fprintln(w)
	}
}

// sampleHint turns the commonest gap into the flag that closes it, because
// "needs 30 samples" is only actionable if the reader knows the run is what
// decides that.
func sampleHint(rep checkReport) string {
	short := 0
	for _, reason := range rep.Unevaluated {
		if strings.Contains(reason, "needs") {
			short++
		}
	}
	if short == 0 {
		return ""
	}
	return fmt.Sprintf("%d of these need a longer run: deltascope check -for 60s -interval 2s", short)
}

type reasonGroup struct {
	reason string
	ids    []string
	// detail carries the part of a state's own reason that the group label
	// generalises away, so collapsing a group never costs the reader a fact.
	detail map[string]string
}

var sampleGapRe = regexp.MustCompile(`^(.*) has (\d+) sample\(s\), needs (\d+)$`)

// canonicalReason folds the per-metric sample shortfalls onto their shared
// cause. Ten burst states declining on ten different metrics are not ten
// findings -- they are one run that was too short, and printing them as ten
// reason lines buries the gaps that do have distinct causes. The sample
// counts stay in the label because they are the shared fact; the metric name
// moves to the state's own line because it is not.
func canonicalReason(reason string) (label, detail string) {
	m := sampleGapRe.FindStringSubmatch(reason)
	if m == nil {
		return reason, ""
	}
	return fmt.Sprintf("the window is too short: %s of the %s samples these need", m[2], m[3]), m[1]
}

// groupByReason collapses "the same thing is missing for these twelve
// states" into one line, so the not-measured list stays readable instead of
// scrolling past the part the reader needs.
func groupByReason(gaps map[string]string) []reasonGroup {
	byReason := map[string][]string{}
	detail := map[string]string{}
	for id, reason := range gaps {
		label, d := canonicalReason(reason)
		byReason[label] = append(byReason[label], id)
		if d != "" {
			detail[id] = d
		}
	}
	out := make([]reasonGroup, 0, len(byReason))
	for reason, ids := range byReason {
		sort.Strings(ids)
		out = append(out, reasonGroup{reason: reason, ids: ids, detail: detail})
	}
	sort.Slice(out, func(i, j int) bool {
		if len(out[i].ids) != len(out[j].ids) {
			return len(out[i].ids) > len(out[j].ids)
		}
		return out[i].reason < out[j].reason
	})
	return out
}

func quietCount(rep checkReport) int {
	return len(quietStates(rep))
}

// quietStates are the states this run actually answered in the negative --
// the auditable middle ground between "fired" and "unknown".
func quietStates(rep checkReport) []string {
	fired := map[string]bool{}
	for _, a := range rep.Active {
		fired[a.ID] = true
	}
	var out []string
	for _, id := range allStateIDs() {
		if !fired[id] && rep.Unevaluated[id] == "" {
			out = append(out, id)
		}
	}
	return out
}

func sortedCountKeys(m map[string]float64) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func trimCount(v float64) string {
	if v == float64(int64(v)) {
		return fmt.Sprintf("%d", int64(v))
	}
	return fmt.Sprintf("%.2f", v)
}

// roundDur keeps the header honest without printing nanoseconds: an 10.0004 s
// window is a ten second window.
func roundDur(d time.Duration) time.Duration {
	if d >= time.Second {
		return d.Round(100 * time.Millisecond)
	}
	return d.Round(time.Millisecond)
}
