package diagnose

import (
	"strings"
	"testing"

	"github.com/githubflyideas/deltascope/internal/pcp"
	"github.com/githubflyideas/deltascope/internal/reasoning"
	"github.com/githubflyideas/deltascope/internal/state"
)

// The verdict used to rest on a boolean: rows arrived, therefore something was
// measured, therefore a green light is fair. These tests pin the replacement --
// the question is not whether rows arrived but whether any state could be
// judged from them -- and the CLI rule this page was missing, which is that
// states holding with no diagnosis pattern behind them is a warning and not
// silence.

// Rows that answer nothing are the case the boolean got wrong. A first sampler
// window has no A side, so every change-judgment state is no_baseline; if the
// families the absolute states rest on are not logged either, the row set is
// real and the catalog still has nothing to say. That is unknown, and calling
// it ok is a green light asserted from zero answers.
func TestRowsThatAnswerNothingAreNotHealthy(t *testing.T) {
	out := &Diagnosis{Coverage: &Coverage{Source: "proc", States: 70, Evaluated: 0}}
	synthesize(out, nil, state.ProcDiff{}, state.Diff{})

	if out.Severity != "unknown" {
		t.Errorf("severity = %q, want unknown: nothing could be judged", out.Severity)
	}
	// And the headline must not send the reader to go restart a collector that
	// is running perfectly well.
	if strings.Contains(out.Headline, "no metric data") {
		t.Errorf("headline blames collection, but data arrived: %q", out.Headline)
	}
	if !strings.Contains(out.Headline, "70") {
		t.Errorf("headline should name the denominator it failed to answer: %q", out.Headline)
	}
}

// The other side of the same fork: some states were answerable and came back
// false. That is a real all-clear, and it must keep saying so.
func TestPartialCoverageStillEarnsGreen(t *testing.T) {
	out := &Diagnosis{Coverage: &Coverage{Source: "proc", States: 70, Evaluated: 12}}
	synthesize(out, nil, state.ProcDiff{}, state.Diff{})

	if out.Severity != "ok" {
		t.Errorf("severity = %q, want ok: twelve states were checked and none hold", out.Severity)
	}
	// But it says so with the denominator attached. A green light over 12 of 70
	// states is a much weaker claim than one over all 70, and the reader cannot
	// weigh it without the numbers.
	for _, want := range []string{"12", "70", "unknown, not clean"} {
		if !strings.Contains(out.Headline, want) {
			t.Errorf("headline is missing %q: %q", want, out.Headline)
		}
	}
}

// Full coverage prints no qualification. A caveat that appears on every report
// is one the reader learns to skip past, which is exactly how the ones that
// matter stop being read.
func TestFullCoverageSaysNothingExtra(t *testing.T) {
	out := &Diagnosis{Coverage: &Coverage{Source: "archive", States: 70, Evaluated: 70}}
	synthesize(out, nil, state.ProcDiff{}, state.Diff{})

	if out.Severity != "ok" {
		t.Fatalf("severity = %q, want ok", out.Severity)
	}
	if strings.Contains(out.Headline, "could be checked") {
		t.Errorf("a fully answered catalog needs no qualification: %q", out.Headline)
	}
}

// The CLI's `check` has warned on this since it was written: states hold, and
// the catalog has no diagnosis pattern for the combination. This page answered
// "no regression detected", so one host got two different colours from one
// engine depending on which entry point the reader happened to use.
func TestStatesHoldingWithNoPatternIsAWarning(t *testing.T) {
	out := &Diagnosis{Coverage: &Coverage{
		Source: "archive", States: 70, Evaluated: 70, Active: 2,
		Holding: []string{"state.net.retrans_high", "state.net.timewait_high"},
	}}
	synthesize(out, nil, state.ProcDiff{}, state.Diff{})

	if out.Severity != "warn" {
		t.Fatalf("severity = %q, want warn: two states hold", out.Severity)
	}
	if !strings.Contains(out.Headline, "2 state(s) hold") {
		t.Errorf("headline = %q, want the count of states that hold", out.Headline)
	}
	// The states are the only evidence there is here -- no diagnosis produced
	// any -- so they have to reach the screen.
	if len(out.Evidence) != 2 {
		t.Errorf("evidence = %v, want the state IDs that hold", out.Evidence)
	}
}

// ...but a diagnosis that did match owns the verdict. The warn above is for the
// gap between the state catalog and the diagnosis catalog, not a second opinion
// stacked on top of a conclusion that already exists.
func TestAMatchedDiagnosisOutranksTheBareStates(t *testing.T) {
	out := &Diagnosis{
		Coverage: &Coverage{Source: "archive", States: 70, Evaluated: 70, Active: 3},
		Reasoning: []reasoning.Result{{
			ID: "diagnosis.memory_exhaustion", Branch: reasoning.BranchMemory,
			Severity: "crit", Conclusion: "Available memory is nearly gone",
		}},
	}
	synthesize(out, nil, state.ProcDiff{}, state.Diff{})

	if out.Severity != "crit" || out.Headline != "Available memory is nearly gone" {
		t.Errorf("severity = %q / headline = %q, want the matched diagnosis", out.Severity, out.Headline)
	}
}

// Triage blocks are a measurement in their own right: they come from the metric
// engine, not the state catalog. A host whose archive produced triage but whose
// catalog answered nothing has still been looked at, and the old evidence must
// keep counting or the archive path regresses to unknown across the board.
func TestTriageAloneStillCountsAsMeasured(t *testing.T) {
	out := &Diagnosis{
		Triage:   []pcp.TriageBlock{{Key: "cpu", Label: "CPU", Status: pcp.TriageOK, Headline: "normal"}},
		Coverage: &Coverage{Source: "archive", States: 70, Evaluated: 0},
	}
	synthesize(out, nil, state.ProcDiff{}, state.Diff{})

	if out.Severity != "ok" {
		t.Errorf("severity = %q, want ok: the metric engine measured and reported", out.Severity)
	}
}

// A nil Coverage means the catalog was never run at all, which is not the same
// claim as running it and answering nothing. synthesize is called directly by
// callers that assemble a Diagnosis by hand, and a nil-dereference there would
// take the whole page down.
func TestNilCoverageDoesNotPanicOrInventAMeasurement(t *testing.T) {
	out := &Diagnosis{}
	synthesize(out, nil, state.ProcDiff{}, state.Diff{})
	if out.Severity != "unknown" {
		t.Errorf("severity = %q, want unknown: no catalog run and no triage", out.Severity)
	}
	if out.Coverage.GapTotal() != 0 {
		t.Errorf("a nil coverage has no gaps to report, got %d", out.Coverage.GapTotal())
	}
}

// The denominator has to be the states that can be evaluated at all. A catalog
// entry with no conditions is skipped by UnevaluatedGaps as well, so counting
// the whole catalog would make even a perfect run look permanently short --
// a silent off-by-N in the only number that qualifies a green light.
func TestTheDenominatorExcludesStatesNothingCanEvaluate(t *testing.T) {
	n := evaluableStates()
	if n == 0 || n > len(reasoning.States) {
		t.Fatalf("evaluableStates = %d, want 0 < n <= %d", n, len(reasoning.States))
	}
	gaps := reasoning.UnevaluatedGaps(reasoning.States, nil)
	// With no rows at all, every evaluable state is a gap and nothing else is.
	if len(gaps) != n {
		t.Errorf("no rows: %d gaps against a denominator of %d -- the two disagree about what is evaluable", len(gaps), n)
	}
}

// Coverage and the diagnoses must be computed from one row set through one
// index. Reading them apart is how a state ends up reported as unevaluated on
// the same screen where a diagnosis cites it as evidence.
func TestReasonCountsGapsByKindOverTheSameRows(t *testing.T) {
	reasoning.SetHost(reasoning.Machine{NCPU: 4})
	rows := []pcp.DiffRow{
		{Metric: "kernel.percpu.cpu.user", Instance: "cpu0", B: f(40), BMax: f(40), BCount: 32},
		{Metric: "kernel.percpu.cpu.sys", Instance: "cpu0", B: f(10), BMax: f(10), BCount: 32},
	}
	_, cov := reason(rows, "proc")

	if cov.Source != "proc" {
		t.Errorf("source = %q, want proc", cov.Source)
	}
	if cov.States != evaluableStates() {
		t.Errorf("states = %d, want the evaluable denominator %d", cov.States, evaluableStates())
	}
	if cov.GapTotal() <= 0 {
		t.Fatal("two cpu metrics cannot answer the whole catalog; some gap was expected")
	}
	if len(cov.Gaps) == 0 {
		t.Fatal("gaps were counted but not classified: the reader cannot tell what to do about them")
	}
	sum := 0
	for kind, n := range cov.Gaps {
		if kind == "" {
			t.Error("a gap with no kind renders as a to-do item with no action")
		}
		sum += n
	}
	if sum != cov.GapTotal() {
		t.Errorf("gap kinds sum to %d but %d states were unevaluated", sum, cov.GapTotal())
	}
}
