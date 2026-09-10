package diagnose

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/githubflyideas/deltascope/internal/native"
	"github.com/githubflyideas/deltascope/internal/pcp"
	"github.com/githubflyideas/deltascope/internal/reasoning"
	"github.com/githubflyideas/deltascope/internal/state"
)

// The one-click page used to have nothing to say about performance on a host
// without PCP: the metric leg contributed a single note naming the missing
// package, and the page reduced to process and config changes with no
// measurement to correlate them against. These tests cover the /proc leg that
// replaced that, and the two things it must not do -- claim a measurement it
// did not take, and stay silent about which window it measured.

type fakeNative struct {
	win native.Window
	err error
}

func (f fakeNative) Window(float64) (native.Window, error) { return f.win, f.err }

// pegWindow is a /proc window in which cpu1 is pegged (700 ms/s user +
// 280 ms/s sys of one core) and the other three cores are idle. A pegged core
// is chosen deliberately: it is the case the aggregate metric engine is
// structurally blind to, so a diagnosis appearing here can only have come from
// the reasoning chain having actually run over these rows.
//
// baseline controls whether the window carries an older half to compare
// against, which is the difference between a sampler that has been up for two
// minutes and one that has just started.
func pegWindow(baseline bool) native.Window {
	end := time.Date(2026, 9, 10, 12, 0, 0, 0, time.UTC)
	percpu := []struct {
		metric, inst string
		v            float64
	}{
		{"kernel.percpu.cpu.user", "cpu0", 40}, {"kernel.percpu.cpu.user", "cpu1", 700},
		{"kernel.percpu.cpu.user", "cpu2", 30}, {"kernel.percpu.cpu.user", "cpu3", 25},
		{"kernel.percpu.cpu.sys", "cpu0", 15}, {"kernel.percpu.cpu.sys", "cpu1", 280},
		{"kernel.percpu.cpu.sys", "cpu2", 12}, {"kernel.percpu.cpu.sys", "cpu3", 10},
		{"kernel.all.cpu.user", "", 795}, {"kernel.all.cpu.sys", "", 317},
	}
	w := native.Window{
		Samples: 32,
		Elapsed: 62 * time.Second,
		Start:   end.Add(-62 * time.Second),
		End:     end,
	}
	for _, m := range percpu {
		row := pcp.DiffRow{
			Metric: m.metric, Instance: m.inst, Category: "cpu",
			Units: "millisec / second",
			B:     f(m.v), BMax: f(m.v), BCount: 32, Verdict: pcp.VFlat,
		}
		if baseline {
			// Both halves see the same numbers, so the peg is sustained rather
			// than a spike -- the same shape the archive-backed test uses.
			row.A, row.AMax, row.ACount = f(m.v), f(m.v), 32
			row.DeltaPct = f(0)
		}
		w.Rows = append(w.Rows, row)
	}
	if baseline {
		w.BaselineSamples = 32
		w.BaselineElapsed = 62 * time.Second
		w.BaselineStart = w.Start.Add(-64 * time.Second)
		w.BaselineEnd = w.Start.Add(-2 * time.Second)
	}
	return w
}

// The headline case: no archive, but /proc answers, and the answer is a real
// diagnosis rather than a page of notes explaining what could not be done.
func TestDiagnoseFallsBackToProc(t *testing.T) {
	// The scale-relative state layer reads the host core count; pin it so the
	// test describes a concrete 4-core machine rather than the CI box.
	reasoning.SetHost(reasoning.Machine{NCPU: 4})

	out, err := Run(context.Background(), Deps{
		MetricsUnavailable: "PCP tools not installed: pmlogsummary (package: pcp)",
		Native:             fakeNative{win: pegWindow(true)},
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	var ids []string
	for _, r := range out.Reasoning {
		ids = append(ids, r.ID)
	}
	found := false
	for _, id := range ids {
		if id == "diagnosis.single_core_saturated" {
			found = true
		}
	}
	if !found {
		t.Fatalf("reasoning = %v, want single_core_saturated: the /proc rows did not reach the engine", ids)
	}
	if out.Severity == "unknown" {
		t.Errorf("severity = unknown with 32 samples of /proc behind it: %s", out.Headline)
	}
	if strings.Contains(out.Headline, "Nothing was measured") {
		t.Errorf("headline = %q, but a window was measured", out.Headline)
	}
	// The note must say where the numbers came from and over what span. The
	// change and process legs describe the hour PickWindow chose; /proc can
	// only speak for the last minute, and nothing else on the page says so.
	if !hasNote(out, "/proc") {
		t.Errorf("notes should name /proc as the source: %v", out.Notes)
	}
	if !hasNote(out, "1m2s") {
		t.Errorf("notes should state the window that was actually measured: %v", out.Notes)
	}
}

// A quiet /proc window is the other half of the honesty rule. "unknown" is
// correct when nothing was measured; here 32 samples were read and every state
// that could be judged came back false, which is a real "ok" and the only
// difference between the two is whether rows arrived. Nothing else in this file
// exercises that, because every other window has a diagnosis firing over it.
func TestDiagnoseFromProcWithNothingWrongIsOkNotUnknown(t *testing.T) {
	reasoning.SetHost(reasoning.Machine{NCPU: 4})
	quiet := pegWindow(true)
	for i := range quiet.Rows {
		// Every core at 40 ms/s: measured, and idle.
		quiet.Rows[i].B, quiet.Rows[i].BMax = f(40), f(40)
		quiet.Rows[i].A, quiet.Rows[i].AMax = f(40), f(40)
	}
	out, err := Run(context.Background(), Deps{
		MetricsUnavailable: "PCP tools not installed",
		Native:             fakeNative{win: quiet},
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(out.Reasoning) != 0 {
		t.Fatalf("an idle machine should produce no diagnoses, got %v", out.Reasoning)
	}
	if out.Severity == "unknown" {
		t.Errorf("severity = unknown, but 10 metrics were read over 32 samples: %q", out.Headline)
	}
	if out.Severity != "ok" {
		t.Errorf("severity = %q, want ok: measured and quiet", out.Severity)
	}
}

// Neither source. This is the case where a green light would be a lie, so the
// verdict has to be "unknown" and the note has to name what to install.
func TestDiagnoseWithNoMetricSourceAtAll(t *testing.T) {
	out, err := Run(context.Background(), Deps{
		MetricsUnavailable: "PCP tools not installed: pmlogsummary (package: pcp)",
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if out.Severity != "unknown" {
		t.Errorf("severity = %q, want unknown: nothing was measured", out.Severity)
	}
	if !hasNote(out, "pmlogsummary") {
		t.Errorf("notes should name what is missing: %v", out.Notes)
	}
}

// A sampler that cannot answer yet is not the same as no sampler: the reader
// needs the archive reason to know what to install and the /proc reason to
// know whether waiting would fix it.
func TestDiagnoseWhenBothSourcesFail(t *testing.T) {
	out, err := Run(context.Background(), Deps{
		MetricsUnavailable: "PCP tools not installed: pmlogsummary (package: pcp)",
		Native: fakeNative{err: errors.New(
			"native: 1 sample(s) 2s after start; two are needed before any rate exists")},
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if out.Severity != "unknown" {
		t.Errorf("severity = %q, want unknown", out.Severity)
	}
	if !hasNote(out, "pmlogsummary") || !hasNote(out, "1 sample") {
		t.Errorf("both failure reasons must survive into the notes: %v", out.Notes)
	}
}

// Before the sampler can split its history the two comparative states have no
// baseline. The chain still runs, and the note must say the comparison did not
// happen rather than leaving the reader to assume it did.
func TestDiagnoseFromProcWithoutABaseline(t *testing.T) {
	reasoning.SetHost(reasoning.Machine{NCPU: 4})
	out, err := Run(context.Background(), Deps{
		MetricsUnavailable: "PCP tools not installed",
		Native:             fakeNative{win: pegWindow(false)},
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !hasNote(out, "no baseline") {
		t.Errorf("notes should say no comparison was made: %v", out.Notes)
	}
	// Rows were still read, so this is not the "nothing was measured" case.
	if out.Severity == "unknown" {
		t.Errorf("severity = unknown, but 32 samples of gauges were read: %v", out.Notes)
	}
}

// With no triage blocks the filter that defers to the metric engine has
// nothing to defer to. Before this was widened, a crit memory diagnosis sat in
// the payload under a headline saying no regression had been found -- the
// detail view and the one-line answer contradicting each other.
func TestReasoningTakesTheHeadlineWithNoArchive(t *testing.T) {
	out := &Diagnosis{Reasoning: []reasoning.Result{{
		ID: "diagnosis.memory_exhaustion", Branch: reasoning.BranchMemory,
		Severity: "crit", Conclusion: "Available memory is nearly gone",
	}}}
	synthesize(out, nil, 1, state.ProcDiff{}, state.Diff{})

	if out.Severity != "crit" {
		t.Errorf("severity = %q, want crit from the reasoning chain", out.Severity)
	}
	if out.Headline != "Available memory is nearly gone" {
		t.Errorf("headline = %q, want the reasoning conclusion", out.Headline)
	}
}

// The widened filter must not leak into the archive path. There, a non-CPU
// reasoning result is the metric engine's story to tell, and two engines
// narrating one problem produced two contradictory headlines.
func TestReasoningStillDefersToTheArchive(t *testing.T) {
	rep := &pcp.DiffReport{Rows: []pcp.DiffRow{{Metric: "mem.util.available"}}}
	out := &Diagnosis{
		Triage: []pcp.TriageBlock{
			{Key: "mem", Label: "Memory", Status: pcp.TriageOK, Headline: "normal"},
		},
		Reasoning: []reasoning.Result{{
			ID: "diagnosis.memory_exhaustion", Branch: reasoning.BranchMemory,
			Severity: "crit", Conclusion: "Available memory is nearly gone",
		}},
	}
	synthesize(out, rep, 0, state.ProcDiff{}, state.Diff{})

	if out.Severity == "crit" {
		t.Errorf("severity = crit: the reasoning chain overrode the metric engine on its own ground")
	}
}

// A /proc-backed memory diagnosis and the RSS growth behind it are both on the
// page; the culprit line is what connects them. Process figures come from
// snapshots, which work without PCP, so declining to attribute here would be a
// gap with no cause other than the absent triage block.
func TestReasoningHeadlineStillNamesACulprit(t *testing.T) {
	out := &Diagnosis{Reasoning: []reasoning.Result{{
		ID: "diagnosis.memory_exhaustion", Branch: reasoning.BranchMemory,
		Severity: "crit", Conclusion: "Available memory is nearly gone",
	}}}
	pd := state.ProcDiff{Rows: []state.ProcRow{
		{Name: "java", RSSKBA: f(500000), RSSKBB: f(520000), RSSDelta: f(4), Verdict: state.PVFlat},
		{Name: "nginx", RSSKBA: f(102400), RSSKBB: f(3800000), RSSDelta: f(3611), Verdict: state.PVWorse},
	}}
	synthesize(out, nil, 1, pd, state.Diff{})

	if !strings.Contains(out.Culprit, "nginx") {
		t.Errorf("culprit = %q, want nginx: a memory diagnosis must attribute by RSS", out.Culprit)
	}
}

func hasNote(out *Diagnosis, want string) bool {
	for _, n := range out.Notes {
		if strings.Contains(n, want) {
			return true
		}
	}
	return false
}
