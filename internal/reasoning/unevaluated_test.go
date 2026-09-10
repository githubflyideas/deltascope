package reasoning

import (
	"strings"
	"testing"

	"github.com/githubflyideas/deltascope/internal/pcp"
)

// TestNoRowsMeansNothingEvaluated is the claim the whole file exists for: on
// an empty row set, Evaluate returns nothing and that "nothing" must read as
// unknown, not as a clean machine. Every state with conditions has to appear
// in the unevaluated list.
func TestNoRowsMeansNothingEvaluated(t *testing.T) {
	gaps := Unevaluated(States, nil)

	want := 0
	for _, st := range States {
		if len(st.When) > 0 {
			want++
		}
	}
	if len(gaps) != want {
		t.Errorf("unevaluated = %d states, want all %d: no rows means no answers", len(gaps), want)
	}
	for id, reason := range gaps {
		if reason == "" {
			t.Errorf("%s: unevaluated with no reason given", id)
		}
	}
}

// TestPresentMetricIsEvaluated: a state whose metric arrived with a value is
// answered, whether or not it fires. Both directions are checked, because a
// coverage report that called satisfied states "unevaluated" would be as
// misleading as one that called unknown states false.
func TestPresentMetricIsEvaluated(t *testing.T) {
	rows := []pcp.DiffRow{
		{Metric: "mem.util.available", B: fptr(100000), BCount: 2}, // below 524288: fires
	}
	if gaps := Unevaluated(States, rows); gaps["state.mem.available_critical"] != "" {
		t.Errorf("a state with data must be evaluated, got %q", gaps["state.mem.available_critical"])
	}
	if _, on := EvaluateOn(States, rows, Machine{NCPU: 8})["state.mem.available_critical"]; !on {
		t.Error("100000 Kbyte available should fire state.mem.available_critical")
	}

	rows[0].B = fptr(8000000) // plenty: evaluated and false
	gaps := Unevaluated(States, rows)
	if gaps["state.mem.available_critical"] != "" {
		t.Errorf("an ample value is still an answer, got %q", gaps["state.mem.available_critical"])
	}
	if _, on := EvaluateOn(States, rows, Machine{NCPU: 8})["state.mem.available_critical"]; on {
		t.Error("8 Gbyte available must not fire the critical state")
	}
}

// TestShortWindowIsUnevaluatedNotFalse is the reason a two-sample check run
// can be honest. The peak states decline on such a window, and the coverage
// report has to say why rather than leaving them among the states that were
// checked and found fine.
func TestShortWindowIsUnevaluatedNotFalse(t *testing.T) {
	var guarded *State
	for i := range States {
		for _, c := range States[i].When {
			if c.MinSamples > 0 && len(States[i].When) == 1 {
				guarded = &States[i]
				break
			}
		}
		if guarded != nil {
			break
		}
	}
	if guarded == nil {
		t.Skip("no single-condition MinSamples state to exercise")
	}
	c := guarded.When[0]

	rows := []pcp.DiffRow{{
		Metric: c.Metric, B: fptr(1), BMax: fptr(1e9), BCount: 2,
	}}
	reason := Unevaluated([]State{*guarded}, rows)[guarded.ID]
	if reason == "" {
		t.Fatalf("%s has MinSamples %d but a 2-sample row was called evaluated", guarded.ID, c.MinSamples)
	}
	if !strings.Contains(reason, "needs") {
		t.Errorf("reason should name the sample shortfall, got %q", reason)
	}
	t.Logf("%s: %s", guarded.ID, reason)
}

// TestMissingPeakIsReported covers the pmlogsummary-without-a case: the rows
// exist, carry a mean, and every peak state would silently read as false.
func TestMissingPeakIsReported(t *testing.T) {
	var peakState *State
	for i := range States {
		if len(States[i].When) == 1 && States[i].When[0].BMaxGte != nil {
			peakState = &States[i]
			break
		}
	}
	if peakState == nil {
		t.Skip("no single-condition BMaxGte state to exercise")
	}
	c := peakState.When[0]
	rows := []pcp.DiffRow{{Metric: c.Metric, B: fptr(10), BCount: 5000}}
	reason := Unevaluated([]State{*peakState}, rows)[peakState.ID]
	if !strings.Contains(reason, "peak") {
		t.Errorf("a row with no BMax must be reported as lacking a peak, got %q", reason)
	}
}

// TestSameInstanceNeedsOneInstanceWithEverything: sda supplying utilisation
// while sdb supplies queue depth is not a measurement of either disk, and
// counting it as coverage would claim the state was checked on a host where
// no single device could answer it.
func TestSameInstanceNeedsOneInstanceWithEverything(t *testing.T) {
	split := []pcp.DiffRow{
		{Metric: "disk.dev.avactive", Instance: "sda", B: fptr(0.9), BCount: 2},
		{Metric: "disk.dev.aveq", Instance: "sdb", B: fptr(4), BCount: 2},
	}
	if reason := Unevaluated(States, split)[stateIOSaturated]; reason == "" {
		t.Error("two half-covered disks must not count as an evaluated state")
	}

	together := []pcp.DiffRow{
		{Metric: "disk.dev.avactive", Instance: "sda", B: fptr(0.9), BCount: 2},
		{Metric: "disk.dev.aveq", Instance: "sda", B: fptr(4), BCount: 2},
	}
	if reason := Unevaluated(States, together)[stateIOSaturated]; reason != "" {
		t.Errorf("one disk carrying both metrics is evaluable, got %q", reason)
	}
	if _, on := EvaluateOn(States, together, Machine{NCPU: 8})[stateIOSaturated]; !on {
		t.Error("sda at 90% busy with queue 4 should fire state.io.saturated")
	}
}

const stateIOSaturated = "state.io.saturated"

// TestEveryFiredStateIsEvaluated is the consistency invariant between the two
// halves of the answer: a state cannot be both fired and unmeasurable. If
// this fails, one of the two functions is reading a field the other does not
// know about.
func TestEveryFiredStateIsEvaluated(t *testing.T) {
	rows := []pcp.DiffRow{
		{Metric: "mem.util.available", B: fptr(100000), BCount: 30},
		{Metric: "swap.free", B: fptr(0), BCount: 30},
		{Metric: "kernel.percpu.cpu.user", Instance: "cpu0", B: fptr(990), BMax: fptr(1000), BCount: 30},
		{Metric: "kernel.percpu.cpu.sys", Instance: "cpu0", B: fptr(5), BMax: fptr(10), BCount: 30},
		{Metric: "disk.dev.avactive", Instance: "sda", B: fptr(0.95), BMax: fptr(1), BCount: 30},
		{Metric: "disk.dev.aveq", Instance: "sda", B: fptr(8), BMax: fptr(12), BCount: 30},
	}
	active := EvaluateOn(States, rows, Machine{NCPU: 8})
	gaps := Unevaluated(States, rows)
	if len(active) == 0 {
		t.Fatal("fixture should fire something, or this test proves nothing")
	}
	for id := range active {
		if reason, ok := gaps[id]; ok {
			t.Errorf("%s fired but is also reported unevaluated (%s)", id, reason)
		}
	}
}
