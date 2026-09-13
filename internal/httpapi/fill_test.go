package httpapi

import (
	"testing"

	"github.com/githubflyideas/deltascope/internal/pcp"
	"github.com/githubflyideas/deltascope/internal/reasoning"
)

// These tests protect one rule: /proc may only speak where the archive was
// silent. Get that wrong in the permissive direction and the health check
// starts averaging two clocks under one metric name; get it wrong in the
// restrictive direction and the eight network states a stock pmlogger config
// never records stay unmeasured forever, which is the bug this exists to fix.

func row(metric, inst string, b float64) pcp.DiffRow {
	v := b
	return pcp.DiffRow{Metric: metric, Instance: inst, B: &v, BCount: 30}
}

func TestFillAppendsOnlyMetricsTheArchiveNeverAnswered(t *testing.T) {
	archive := []pcp.DiffRow{
		row("kernel.all.cpu.user", "", 900),
		row("mem.util.available", "", 1024),
	}
	proc := []pcp.DiffRow{
		// The archive already has an opinion about CPU; proc must not add a
		// second one.
		row("kernel.all.cpu.user", "", 50),
		row("network.tcp.listendrops", "", 7),
		row("network.tcpconn.close_wait", "", 3),
	}
	merged, filled := fillGaps(archive, proc)

	if len(merged) != 4 {
		t.Fatalf("merged = %d rows, want 4 (2 archive + 2 filled)", len(merged))
	}
	// The archive's rows keep their place and their values: this is the
	// "archive always wins" rule, and a silent overwrite of 900 by 50 is
	// exactly the outcome the design forbids.
	if merged[0].Metric != "kernel.all.cpu.user" || *merged[0].B != 900 {
		t.Errorf("archive row was displaced or overwritten: %+v", merged[0])
	}
	if len(filled) != 2 || !filled["network.tcp.listendrops"] || !filled["network.tcpconn.close_wait"] {
		t.Errorf("filled = %v, want exactly the two metrics the archive lacked", filled)
	}
	if filled["kernel.all.cpu.user"] {
		t.Error("a metric the archive answered must never be reported as filled")
	}
}

// A metric the archive answered for some instances but not all is left alone
// on purpose. Topping up the missing disks from /proc would put rows measured
// over two different spans under one metric, and a SameInstance state would
// then be free to satisfy one condition from the archive's sda and another
// from proc's sdb -- a saturated device that exists in neither source.
func TestFillDoesNotTopUpAPartialInstanceSet(t *testing.T) {
	archive := []pcp.DiffRow{row("disk.dev.avactive", "sda", 800)}
	proc := []pcp.DiffRow{
		row("disk.dev.avactive", "sda", 810),
		row("disk.dev.avactive", "sdb", 950),
	}
	merged, filled := fillGaps(archive, proc)

	if len(merged) != 1 {
		t.Fatalf("merged = %d rows, want 1: a partially-answered metric is not topped up", len(merged))
	}
	if len(filled) != 0 {
		t.Errorf("filled = %v, want empty", filled)
	}
}

func TestFillWithNothingFromProcChangesNothing(t *testing.T) {
	archive := []pcp.DiffRow{row("kernel.all.load", "1 minute", 4)}
	merged, filled := fillGaps(archive, nil)
	if len(merged) != 1 || merged[0].Metric != "kernel.all.load" {
		t.Fatalf("merged = %+v, want the archive rows untouched", merged)
	}
	if len(filled) != 0 {
		t.Errorf("filled = %v, want empty: an idle sampler must not mark anything", filled)
	}
}

// The shape on a host with no archive rows at all for a whole family: every
// instance of the filled metric comes across, not just the first.
func TestFillCarriesEveryInstanceOfAFilledMetric(t *testing.T) {
	proc := []pcp.DiffRow{
		row("network.interface.in.errors", "eth0", 1),
		row("network.interface.in.errors", "eth1", 2),
	}
	merged, filled := fillGaps(nil, proc)
	if len(merged) != 2 {
		t.Fatalf("merged = %d rows, want both instances", len(merged))
	}
	if len(filled) != 1 {
		t.Errorf("filled = %v, want one metric name regardless of instance count", filled)
	}
}

// The provenance tag is per state, and a state is tagged when ANY of its
// conditions reads a filled metric. The reader's question is whether the
// answer was assembled from two windows, and one borrowed condition is enough
// for the answer to be yes.
func TestFilledStatesTagsAStateWithOneBorrowedCondition(t *testing.T) {
	catalog := []reasoning.State{
		{ID: "state.one", Domain: "net", When: []reasoning.Cond{
			{Metric: "network.tcp.listendrops"}, {Metric: "kernel.all.load"}}},
		{ID: "state.two", Domain: "cpu", When: []reasoning.Cond{{Metric: "kernel.all.load"}}},
	}
	got := filledStates(catalog, map[string]bool{"network.tcp.listendrops": true})
	if !got["state.one"] {
		t.Error("a state with one filled condition must be tagged")
	}
	if got["state.two"] {
		t.Error("a state reading only archive metrics must not be tagged")
	}
}

func TestFilledStatesIsNilWhenNothingWasFilled(t *testing.T) {
	if got := filledStates(reasoning.States, nil); got != nil {
		t.Errorf("got %v, want nil so stateViews skips the whole walk", got)
	}
}

// The eight metrics this change exists for. If a future edit to the native
// collector or the state catalog breaks the join between them, the health
// check silently goes back to reporting eight unmeasured network states on a
// healthy host -- a regression with no visible error anywhere.
func TestTheEightArchiveGapMetricsAreReachableFromStates(t *testing.T) {
	gapMetrics := []string{
		"network.tcp.listendrops", "network.tcp.listenoverflows",
		"network.tcp.syncookiessent", "network.tcp.timeouts",
		"network.tcp.prunecalled", "network.tcp.rcvcollapsed",
		"network.tcpconn.close_wait", "network.tcpconn.syn_recv",
	}
	filled := map[string]bool{}
	for _, m := range gapMetrics {
		filled[m] = true
	}
	tagged := filledStates(reasoning.States, filled)
	if len(tagged) == 0 {
		t.Fatal("no state reads any of the eight gap metrics: the fill would be invisible")
	}
	// Every one of the eight must be read by at least one state, or filling it
	// buys nothing and the metric should not be in the native collector either.
	used := map[string]bool{}
	for _, st := range reasoning.States {
		for _, c := range st.When {
			if filled[c.Metric] {
				used[c.Metric] = true
			}
		}
	}
	for _, m := range gapMetrics {
		if !used[m] {
			t.Errorf("%s is collected but no state reads it", m)
		}
	}
	t.Logf("%d states depend on the eight archive-gap metrics", len(tagged))
}

// A state whose metric neither source answered stays unmeasured, and must not
// pick up a /proc tag on the way through. The tag says "this answer came from
// /proc"; putting it on a row that has no answer would claim a measurement
// that never happened.
func TestUnmeasuredStatesAreNotTaggedAsProc(t *testing.T) {
	catalog := []reasoning.State{{ID: "state.x", Domain: "net",
		When: []reasoning.Cond{{Metric: "network.tcp.listendrops"}}}}
	views := stateViews(catalog, map[string]reasoning.Active{},
		map[string]reasoning.Gap{"state.x": {Kind: reasoning.GapNoData, Reason: "no data for network.tcp.listendrops"}},
		map[string]bool{"state.x": true})
	if views[0].Source != "" {
		t.Errorf("source = %q, want empty on an unmeasured state", views[0].Source)
	}
}

// The tag lands on quiet rows too, not only on the ones that fired. A quiet
// row answered from a two-minute /proc window is a weaker all-clear than a
// quiet row answered from half an hour of archive.
func TestQuietFilledStateStillCarriesItsSource(t *testing.T) {
	catalog := []reasoning.State{
		{ID: "state.quiet", Domain: "net", When: []reasoning.Cond{{Metric: "network.tcp.timeouts"}}},
		{ID: "state.fired", Domain: "net", When: []reasoning.Cond{{Metric: "network.tcp.timeouts"}}},
	}
	views := stateViews(catalog,
		map[string]reasoning.Active{"state.fired": {ID: "state.fired", Evidence: []string{"network.tcp.timeouts B=9"}}},
		map[string]reasoning.Gap{},
		map[string]bool{"state.quiet": true, "state.fired": true})
	for _, v := range views {
		if v.Source != "proc" {
			t.Errorf("%s: source = %q, want proc", v.ID, v.Source)
		}
	}
}
