package reasoning

import (
	"testing"

	"github.com/githubflyideas/deltascope/internal/pcp"
)

func row(metric string, b float64) pcp.DiffRow {
	bv := b
	return pcp.DiffRow{Metric: metric, B: &bv, Verdict: pcp.VFlat}
}

func rowDelta(metric string, a, b, delta float64, v pcp.Verdict) pcp.DiffRow {
	av, bv, dv := a, b, delta
	return pcp.DiffRow{Metric: metric, A: &av, B: &bv, DeltaPct: &dv, Verdict: v}
}

// The central claim of this design: two situations that share the same
// positive signal (a queue is building) are told apart by what is ABSENT.
// If the negative condition doesn't work, both diagnoses fire on both
// inputs and the whole indirection layer is pointless.
func TestNegativeConditionSeparatesContentionFromBusy(t *testing.T) {
	// Case 1 — hypervisor contention: steal is high, queue is building,
	// but this guest's own userspace is nearly idle.
	contention := []pcp.DiffRow{
		row("kernel.all.cpu.steal", 380),
		row("kernel.all.runnable", 14),
		row("kernel.all.cpu.user", 120), // well under the user_high floor
	}
	active := Evaluate(States, contention)
	if _, on := active["state.cpu.steal_high"]; !on {
		t.Fatal("steal_high should be active at 380 ms/s")
	}
	if _, on := active["state.cpu.user_high"]; on {
		t.Fatal("user_high must NOT be active at 120 ms/s")
	}
	got := diagnosisIDs(Diagnose(Diagnoses, active))
	if !contains(got, "diagnosis.vm_cpu_contention") {
		t.Errorf("vm_cpu_contention should fire on steal+queue without user load, got %v", got)
	}
	if contains(got, "diagnosis.cpu_saturated_own_workload") {
		t.Errorf("own_workload must NOT fire when steal is high, got %v", got)
	}

	// Case 2 — the guest is simply busy: same queue, no steal.
	busy := []pcp.DiffRow{
		row("kernel.all.cpu.steal", 5), // negligible
		row("kernel.all.runnable", 14),
		row("kernel.all.cpu.user", 3400), // 3.4 cores of real work
	}
	active2 := Evaluate(States, busy)
	got2 := diagnosisIDs(Diagnose(Diagnoses, active2))
	if !contains(got2, "diagnosis.cpu_saturated_own_workload") {
		t.Errorf("own_workload should fire on high user CPU with a queue, got %v", got2)
	}
	if contains(got2, "diagnosis.vm_cpu_contention") {
		t.Errorf("vm_cpu_contention must NOT fire without steal, got %v", got2)
	}
}

// An absolute state must catch a problem that is present in BOTH windows.
// This is the case a pure change-diff structurally cannot see, and the
// main reason absolute conditions exist in this layer.
func TestAbsoluteStateCatchesSteadyProblem(t *testing.T) {
	// Steal has been pinned at ~30% of a core in both windows: zero
	// change, so the row is judged flat, but the machine is being starved
	// the whole time.
	steady := []pcp.DiffRow{
		rowDelta("kernel.all.cpu.steal", 300, 305, 1.7, pcp.VFlat),
		rowDelta("kernel.all.runnable", 11, 11.2, 1.8, pcp.VFlat),
		rowDelta("kernel.all.cpu.user", 150, 148, -1.3, pcp.VFlat),
	}
	active := Evaluate(States, steady)
	if _, on := active["state.cpu.steal_high"]; !on {
		t.Fatal("steal_high must be active on a steadily-high value, even with no change")
	}
	got := diagnosisIDs(Diagnose(Diagnoses, active))
	if !contains(got, "diagnosis.vm_cpu_contention") {
		t.Errorf("a steady contention problem should still be diagnosed, got %v", got)
	}
}

// The counterpart guarantee: a genuinely healthy machine produces nothing.
// A diagnosis layer that invents findings on an idle host is worse than
// no diagnosis layer, and this codebase has already shipped that bug once.
func TestHealthyMachineProducesNoDiagnosis(t *testing.T) {
	healthy := []pcp.DiffRow{
		rowDelta("kernel.all.cpu.steal", 0, 0, 0, pcp.VFlat),
		rowDelta("kernel.all.runnable", 2, 2.1, 5, pcp.VFlat),
		rowDelta("kernel.all.cpu.user", 18, 21, 16.7, pcp.VFlat),
		rowDelta("mem.util.available", 13_800_000, 13_700_000, -0.7, pcp.VFlat),
		rowDelta("swap.pagesout", 0, 0, 0, pcp.VFlat),
		rowDelta("disk.dev.avactive", 0.001, 0.002, 100, pcp.VFlat),
		rowDelta("disk.dev.aveq", 0.003, 0.005, 66.7, pcp.VFlat),
	}
	active := Evaluate(States, healthy)
	if len(active) > 0 {
		t.Errorf("no state should be active on a healthy machine, got %v", ActiveIDs(active))
	}
	if got := Diagnose(Diagnoses, active); len(got) > 0 {
		t.Errorf("no diagnosis should fire on a healthy machine, got %v", diagnosisIDs(got))
	}
}

// The idle-disk numbers that produced a false "disk is saturated" verdict
// on a live host earlier in this project must not resurrect it here.
func TestIdleDiskDoesNotTriggerIOBound(t *testing.T) {
	idle := []pcp.DiffRow{
		rowDelta("disk.dev.avactive", 0.001, 0.002, 100, pcp.VFlat),
		rowDelta("disk.dev.aveq", 0.003, 0.005, 66.7, pcp.VFlat),
		rowDelta("kernel.all.pressure.io.some.avg", 0.001, 0.002, 100, pcp.VFlat),
	}
	active := Evaluate(States, idle)
	if _, on := active["state.io.saturated"]; on {
		t.Error("an idle disk (0.2% busy, queue 0.005) must not count as saturated")
	}
	if got := Diagnose(Diagnoses, active); len(got) > 0 {
		t.Errorf("no diagnosis should fire on idle disk noise, got %v", diagnosisIDs(got))
	}

	// ...while a genuinely saturated disk still is detected.
	busy := []pcp.DiffRow{
		row("disk.dev.avactive", 0.94),
		row("disk.dev.aveq", 18.5),
		row("kernel.all.pressure.io.some.avg", 42),
	}
	active2 := Evaluate(States, busy)
	if _, on := active2["state.io.saturated"]; !on {
		t.Error("a disk 94% busy with queue depth 18 must count as saturated")
	}
	if got := diagnosisIDs(Diagnose(Diagnoses, active2)); !contains(got, "diagnosis.io_bound") {
		t.Errorf("io_bound should fire on a genuinely saturated disk, got %v", got)
	}
}

// Mutually-exclusive diagnoses must not both fire: the more specific one
// (swapping) suppresses the general one (reclaim pressure).
func TestMoreSpecificDiagnosisSuppressesGeneral(t *testing.T) {
	swapping := []pcp.DiffRow{
		rowDelta("mem.util.available", 8_000_000, 3_000_000, -62.5, pcp.VWorse),
		row("swap.pagesout", 55),
		row("mem.vmstat.pgscan_direct", 12),
	}
	active := Evaluate(States, swapping)
	got := diagnosisIDs(Diagnose(Diagnoses, active))
	if !contains(got, "diagnosis.memory_pressure_swapping") {
		t.Errorf("swapping diagnosis should fire, got %v", got)
	}
	if contains(got, "diagnosis.memory_reclaim_pressure") {
		t.Errorf("the general reclaim diagnosis should be suppressed when swapping is diagnosed, got %v", got)
	}

	// Reclaim without swap: the general one is now the right answer.
	reclaimOnly := []pcp.DiffRow{
		row("mem.vmstat.pgscan_direct", 12),
		rowDelta("swap.pagesout", 0, 0, 0, pcp.VFlat),
	}
	active2 := Evaluate(States, reclaimOnly)
	got2 := diagnosisIDs(Diagnose(Diagnoses, active2))
	if !contains(got2, "diagnosis.memory_reclaim_pressure") {
		t.Errorf("reclaim diagnosis should fire when there is no swapping, got %v", got2)
	}
}

// A diagnosis defined only by negatives would be true on a healthy
// machine; it must be rejected rather than firing on everything.
func TestAllNegativeDiagnosisNeverFires(t *testing.T) {
	bogus := []Diagnosis{{
		ID: "diagnosis.bogus", Severity: "crit", Conclusion: "should never appear",
		RequiresNone: []string{"state.cpu.steal_high"},
	}}
	if got := Diagnose(bogus, map[string]Active{}); len(got) > 0 {
		t.Errorf("an all-negative diagnosis must not fire, got %v", diagnosisIDs(got))
	}
}

// Results must carry the chain: which states fired, and the metric-level
// evidence behind them. A conclusion nobody can audit is the failure mode
// this whole layer exists to avoid.
func TestResultCarriesFullChain(t *testing.T) {
	rows := []pcp.DiffRow{
		row("kernel.all.cpu.steal", 380),
		row("kernel.all.runnable", 14),
		row("kernel.all.cpu.user", 120),
	}
	res := Diagnose(Diagnoses, Evaluate(States, rows))
	if len(res) == 0 {
		t.Fatal("expected a diagnosis")
	}
	r := res[0]
	if len(r.States) == 0 {
		t.Error("result must name the states that triggered it")
	}
	if len(r.Evidence) == 0 {
		t.Error("result must carry metric-level evidence")
	}
	if len(r.Next) == 0 {
		t.Error("result must suggest next steps")
	}
	t.Logf("diagnosis: %s", r.ID)
	t.Logf("  states:   %v", r.States)
	t.Logf("  evidence: %v", r.Evidence)
}

func diagnosisIDs(rs []Result) []string {
	out := make([]string, 0, len(rs))
	for _, r := range rs {
		out = append(out, r.ID)
	}
	return out
}

func contains(hay []string, needle string) bool {
	for _, h := range hay {
		if h == needle {
			return true
		}
	}
	return false
}
