package reasoning

import (
	"fmt"
	"sort"
	"strings"

	"github.com/githubflyideas/deltascope/internal/pcp"
)

// A state's absence from Evaluate's result means one of two very different
// things: the state was checked and does not hold, or it could not be
// checked at all because the data it needs is missing. Reporting them as one
// category is how a monitoring tool ends up saying "no problems found" about
// a machine it never measured -- so this file separates them, and every
// entry point that shows states is expected to show both lists.
//
// Nothing here re-implements the thresholds. It asks only whether a
// condition had the *fields* it needs to produce an answer, which is why it
// stays correct as the catalog's numbers change.

// Unevaluated returns the states that could not be judged against these
// rows, keyed by state ID, with a one-line reason naming the metric and what
// was missing. A state that appears here is unknown, not false.
func Unevaluated(states []State, rows []pcp.DiffRow) map[string]string {
	byMetric := indexWithDerived(rows)
	out := map[string]string{}
	for _, st := range states {
		if len(st.When) == 0 {
			continue // a state with no conditions is never evaluated at all
		}
		if reason := stateGap(st, byMetric); reason != "" {
			out[st.ID] = reason
		}
	}
	return out
}

// indexWithDerived groups rows by metric and adds the derived rows, exactly
// as EvaluateOn does. Both callers must see the same row set or a state
// resting on a derived metric would be reported as unevaluated while
// Evaluate happily fires it.
func indexWithDerived(rows []pcp.DiffRow) map[string][]pcp.DiffRow {
	byMetric := make(map[string][]pcp.DiffRow, len(rows))
	for _, r := range rows {
		byMetric[r.Metric] = append(byMetric[r.Metric], r)
	}
	for _, r := range Derive(rows) {
		byMetric[r.Metric] = append(byMetric[r.Metric], r)
	}
	return byMetric
}

// stateGap returns why the state could not be evaluated, or "" if it could.
func stateGap(st State, byMetric map[string][]pcp.DiffRow) string {
	if !st.SameInstance {
		for _, c := range st.When {
			if reason := condGap(c, byMetric[c.Metric]); reason != "" {
				return reason
			}
		}
		return ""
	}

	// A SameInstance state needs one instance carrying every condition. Two
	// disks each supplying half the conditions is not a measurement of
	// either, so per-condition availability would overstate coverage here.
	instances := map[string]bool{}
	for _, c := range st.When {
		for _, row := range byMetric[c.Metric] {
			if row.Instance != "" {
				instances[row.Instance] = true
			}
		}
	}
	if len(instances) == 0 {
		return fmt.Sprintf("no per-instance data for %s", strings.Join(condMetrics(st), ", "))
	}
	var firstReason string
	for _, inst := range sortedInstances(instances) {
		ok := true
		for _, c := range st.When {
			reason := condGap(c, rowsForInstance(byMetric[c.Metric], inst))
			if reason != "" {
				ok = false
				if firstReason == "" {
					firstReason = reason + " on " + inst
				}
				break
			}
		}
		if ok {
			return ""
		}
	}
	return firstReason
}

// rowsForInstance keeps the rows a same-instance match would consider for
// one instance: that instance's own rows plus any instance-less row, which
// applies to every candidate.
func rowsForInstance(rows []pcp.DiffRow, inst string) []pcp.DiffRow {
	out := make([]pcp.DiffRow, 0, len(rows))
	for _, r := range rows {
		if r.Instance == "" || r.Instance == inst {
			out = append(out, r)
		}
	}
	return out
}

func sortedInstances(set map[string]bool) []string {
	out := make([]string, 0, len(set))
	for k := range set {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func condMetrics(st State) []string {
	seen := map[string]bool{}
	var out []string
	for _, c := range st.When {
		if !seen[c.Metric] {
			seen[c.Metric] = true
			out = append(out, c.Metric)
		}
	}
	return out
}

// condGap reports why no row can answer the condition, or "" if one can.
// "Can answer" is deliberately weaker than "does match": a row that carries
// the fields the condition reads produces a real true-or-false, and that is
// what makes the state evaluated.
func condGap(c Cond, rows []pcp.DiffRow) string {
	if len(rows) == 0 {
		return "no data for " + c.Metric
	}
	best := ""
	for _, row := range rows {
		reason := rowGap(c, row)
		if reason == "" {
			return ""
		}
		if best == "" {
			best = reason
		}
	}
	return best
}

// rowGap names the field this row lacks for this condition.
func rowGap(c Cond, row pcp.DiffRow) string {
	name := c.Metric
	if row.Instance != "" {
		name += "[" + row.Instance + "]"
	}
	needsB := c.BGte != nil || c.BLte != nil || c.BGteCores != nil ||
		c.BGteMachineFrac != nil || c.BGtePerCPU != nil
	if needsB && row.B == nil {
		return name + " has no value for this window"
	}
	needsPeak := c.BMaxGte != nil || c.BMaxGteCores != nil ||
		c.BMaxMachineFrac != nil || c.BMaxGtePerCPU != nil || c.PeakRatioGte != nil
	if needsPeak && row.BMax == nil {
		// pmlogsummary without -a reports no min/max, and a nil peak is the
		// reason every peak state would otherwise look uniformly false.
		return name + " carries no peak statistic"
	}
	if c.PeakRatioGte != nil && row.B == nil {
		return name + " has no mean to take a peak ratio against"
	}
	if c.MinSamples > 0 && row.BCount < c.MinSamples {
		return fmt.Sprintf("%s has %d sample(s), needs %d", name, row.BCount, c.MinSamples)
	}
	// Verdict and the Delta conditions describe a comparison against an
	// earlier window. A single-window collection has no A side, so those
	// states are unknown rather than false -- the distinction the native
	// path depends on. Appeared is not listed: it reads A and B directly and
	// gives a real answer either way.
	if (c.DeltaGte != nil || c.DeltaLte != nil) && row.DeltaPct == nil {
		return name + " has no baseline to compare against"
	}
	if c.Verdict != "" && row.A == nil {
		return name + " has no baseline, so it has no verdict"
	}
	return ""
}
