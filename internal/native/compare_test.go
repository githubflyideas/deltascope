package native

import (
	"strings"
	"testing"
	"time"

	"github.com/githubflyideas/deltascope/internal/pcp"
	"github.com/githubflyideas/deltascope/internal/reasoning"
)

// Compare exists for exactly two states. state.mem.available_low and
// state.disk.filling_fast ask whether a number is falling, and falling is not
// a property of one window -- on a single /proc run they are reported
// unevaluated, which is honest and never actionable. These tests pin that a
// baseline turns them into real answers, and that a missing baseline still
// declines rather than reading as no change.

// memWithAvailable is meminfoFixture with MemAvailable rewritten, so the two
// windows differ in exactly the one number under test.
func memWithAvailable(kB string) string {
	out := []string{}
	for _, ln := range strings.Split(meminfoFixture, "\n") {
		if strings.HasPrefix(ln, "MemAvailable:") {
			ln = "MemAvailable:     " + kB + " kB"
		}
		out = append(out, ln)
	}
	return strings.Join(out, "\n")
}

// gaugeRun builds a run of n samples interval apart, all carrying the same
// meminfo -- a steady window, which is what a baseline is.
func gaugeRun(start time.Time, n int, interval time.Duration, meminfo string) []Sample {
	var out []Sample
	for i := 0; i < n; i++ {
		s := newSample(start.Add(time.Duration(i) * interval))
		s.parseMeminfo(meminfo)
		s.parseStat(statFixture)
		out = append(out, s)
	}
	return out
}

// rowFor returns the whole row, unlike rows_test.go's findRow which returns
// just B: every case here is about the A side and the verdict.
func rowFor(w Window, metric string) (pcp.DiffRow, bool) {
	for _, r := range w.Rows {
		if r.Metric == metric {
			return r, true
		}
	}
	return pcp.DiffRow{}, false
}

// The headline claim: available memory that has halved between two windows
// fires the comparative state, off /proc alone, with no archive anywhere in
// the path.
func TestCompareMakesFallingMemoryAnswerable(t *testing.T) {
	before := gaugeRun(zeroTime, 4, time.Second, memWithAvailable("8000000"))
	after := gaugeRun(zeroTime.Add(time.Minute), 4, time.Second, memWithAvailable("2000000"))

	w := Compare(before, after, 10)
	row, ok := rowFor(w, "mem.util.available")
	if !ok {
		t.Fatal("mem.util.available produced no row")
	}
	if row.A == nil {
		t.Fatal("the baseline window produced no A side")
	}
	if row.DeltaPct == nil {
		t.Fatal("two known sides must yield a delta")
	}
	// -75% of the baseline, judged BetterUp, is a worse verdict.
	if *row.DeltaPct > -70 || *row.DeltaPct < -80 {
		t.Errorf("DeltaPct = %.2f, want about -75", *row.DeltaPct)
	}
	if string(row.Verdict) != "worse" {
		t.Errorf("Verdict = %q, want worse: available memory fell by three quarters", row.Verdict)
	}

	active := reasoning.EvaluateOn(reasoning.States, w.Rows, reasoning.Machine{NCPU: 8})
	if _, on := active["state.mem.available_low"]; !on {
		t.Error("state.mem.available_low must fire: this is the state Compare was written for")
	}
	// And it must no longer be reported as something that could not be judged.
	if reason, gap := reasoning.Unevaluated(reasoning.States, w.Rows)["state.mem.available_low"]; gap {
		t.Errorf("the state was answered but is still reported unevaluated: %s", reason)
	}
}

// Without a baseline the same state must decline, and say why. This is the
// pre-Compare behaviour and it must survive: Compare adds an answer where
// there is evidence, it does not lower the bar for having one.
func TestNoBaselineStillDeclinesTheComparativeState(t *testing.T) {
	after := gaugeRun(zeroTime, 4, time.Second, memWithAvailable("2000000"))
	w := Compare(nil, after, 10)

	row, ok := rowFor(w, "mem.util.available")
	if !ok {
		t.Fatal("mem.util.available produced no row")
	}
	if row.A != nil || row.DeltaPct != nil {
		t.Error("a run with no baseline must not invent an A side")
	}
	if w.BaselineSamples != 0 {
		t.Errorf("BaselineSamples = %d, want 0 so the report cannot claim a comparison", w.BaselineSamples)
	}
	reason, gap := reasoning.Unevaluated(reasoning.States, w.Rows)["state.mem.available_low"]
	if !gap {
		t.Fatal("a state needing a verdict must be reported unevaluated when there is no baseline")
	}
	if !strings.Contains(reason, "baseline") {
		t.Errorf("the reason should name the missing baseline, got %q", reason)
	}
}

// A steady machine is the case that must not fire. Judge's dual-significance
// rules are the archive path's rules, and Compare must not soften them: a 1%
// drift is not a state.
func TestCompareOnASteadyMachineIsFlat(t *testing.T) {
	before := gaugeRun(zeroTime, 4, time.Second, memWithAvailable("8000000"))
	after := gaugeRun(zeroTime.Add(time.Minute), 4, time.Second, memWithAvailable("7950000"))

	w := Compare(before, after, 10)
	row, _ := rowFor(w, "mem.util.available")
	if string(row.Verdict) != "flat" {
		t.Errorf("Verdict = %q, want flat at a 0.6%% drift", row.Verdict)
	}
	if row.Exceeded {
		t.Error("a 0.6% drift must not be marked as exceeding the threshold")
	}
	active := reasoning.EvaluateOn(reasoning.States, w.Rows, reasoning.Machine{NCPU: 8})
	if _, on := active["state.mem.available_low"]; on {
		t.Error("state.mem.available_low fired on a steady machine")
	}
}

// The baseline must not disturb the current window. Everything the
// non-comparative states read comes from the B side, so if adding a baseline
// changed a single B value, Compare would silently be a different measurement
// of now than Build is.
func TestCompareLeavesTheCurrentWindowUntouched(t *testing.T) {
	after := fixtureWindowSamples()
	plain := Build(after)
	compared := Compare(gaugeRun(zeroTime.Add(-time.Minute), 4, time.Second, memWithAvailable("8000000")), after, 10)

	if len(plain.Rows) != len(compared.Rows) {
		t.Fatalf("rows = %d with a baseline, %d without", len(compared.Rows), len(plain.Rows))
	}
	for i := range plain.Rows {
		p, c := plain.Rows[i], compared.Rows[i]
		if p.Metric != c.Metric || p.Instance != c.Instance {
			t.Fatalf("row %d moved: %s[%s] vs %s[%s]", i, p.Metric, p.Instance, c.Metric, c.Instance)
		}
		if (p.B == nil) != (c.B == nil) || (p.B != nil && *p.B != *c.B) {
			t.Errorf("%s: B changed when a baseline was added", p.Metric)
		}
		if p.BCount != c.BCount {
			t.Errorf("%s: BCount %d -> %d", p.Metric, p.BCount, c.BCount)
		}
	}
	if compared.BaselineSamples != 4 {
		t.Errorf("BaselineSamples = %d, want 4", compared.BaselineSamples)
	}
}

// A metric the kernel published in the baseline and not now must not survive
// into the rows: the report describes the machine as it is, and a row whose
// only evidence is historical would be judged against nothing.
func TestCompareDropsMetricsThatAreGoneNow(t *testing.T) {
	before := gaugeRun(zeroTime, 2, time.Second, meminfoFixture)
	for i := range before {
		before[i].set("network.interface.in.bytes", "eth9", 1000)
	}
	after := gaugeRun(zeroTime.Add(time.Minute), 2, time.Second, meminfoFixture)

	w := Compare(before, after, 10)
	for _, r := range w.Rows {
		if r.Instance == "eth9" {
			t.Errorf("eth9 is gone from the machine but still has a row: %+v", r)
		}
	}
}

// fixtureWindowSamples is the engine fixture's two samples, reused here so
// this test compares against a window with counters and instances in it, not
// only the gauges the other cases need.
func fixtureWindowSamples() []Sample {
	s1 := newSample(zeroTime)
	s1.parseStat(statFixture)
	s1.parseMeminfo(meminfoFixture)
	s1.parseVmstat(vmstatFixture)
	s1.parseDiskstats(diskstatsFixture)

	s2 := newSample(zeroTime.Add(10 * time.Second))
	s2.parseStat(statFixtureLater)
	s2.parseMeminfo(meminfoFixture)
	s2.parseVmstat(vmstatFixture)
	s2.parseDiskstats(diskstatsFixtureLater)

	return []Sample{s1, s2}
}
