package reasoning

import (
	"testing"

	"github.com/githubflyideas/deltascope/internal/pcp"
)

// testMachine fixes the scaling base so tests describe a concrete,
// realistic host rather than inheriting whatever the CI runner happens to
// have. Scale-relative thresholds are meaningless without a known size,
// and a 1-core sandbox would silently make every threshold trivially low.
var testMachine = Machine{NCPU: 8}

func evalTest(rows []pcp.DiffRow) map[string]Active {
	return EvaluateOn(States, rows, testMachine)
}

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
	// On the 8-core test machine: 1600 ms/s of steal is 20% of total
	// capacity, 30 runnable is nearly 4x the core count, and 400 ms/s of
	// user time is 5% -- this guest is barely doing anything itself.
	contention := []pcp.DiffRow{
		row("kernel.all.cpu.steal", 1600),
		row("kernel.all.runnable", 30),
		row("kernel.all.cpu.user", 400), // well under the user_high floor
	}
	active := evalTest(contention)
	if _, on := active["state.cpu.steal_high"]; !on {
		t.Fatal("steal_high should be active at 1600 ms/s on an 8-core host (20% of capacity)")
	}
	if _, on := active["state.cpu.user_high"]; on {
		t.Fatal("user_high must NOT be active at 400 ms/s on an 8-core host (5% of capacity)")
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
		row("kernel.all.runnable", 30),
		row("kernel.all.cpu.user", 6000), // 6 of 8 cores doing real work
	}
	active2 := evalTest(busy)
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
		rowDelta("kernel.all.cpu.steal", 1580, 1605, 1.6, pcp.VFlat),
		rowDelta("kernel.all.runnable", 28, 28.5, 1.8, pcp.VFlat),
		rowDelta("kernel.all.cpu.user", 420, 415, -1.2, pcp.VFlat),
	}
	active := evalTest(steady)
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
	active := evalTest(healthy)
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
	active := evalTest(idle)
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
	active2 := evalTest(busy)
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
	active := evalTest(swapping)
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
	active2 := evalTest(reclaimOnly)
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
		row("kernel.all.cpu.steal", 1600),
		row("kernel.all.runnable", 30),
		row("kernel.all.cpu.user", 400),
	}
	res := Diagnose(Diagnoses, evalTest(rows))
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

// The whole reason scale-relative thresholds exist: the SAME metric
// values must be judged differently depending on machine size, because
// the aggregate PCP metrics are whole-machine sums. If this test fails,
// fixed thresholds have crept back in and the engine is guaranteed to be
// wrong on one class of host or the other.
func TestSameNumbersDifferentVerdictByMachineSize(t *testing.T) {
	// 1500 ms/s of user time == 1.5 cores' worth.
	rows := []pcp.DiffRow{
		row("kernel.all.cpu.user", 1500),
		row("kernel.all.runnable", 6),
	}

	// On a 2-core box that is 75% of everything the machine has, plus a
	// run queue 3x the core count: genuinely saturated.
	small := EvaluateOn(States, rows, Machine{NCPU: 2})
	if _, on := small["state.cpu.user_high"]; !on {
		t.Error("1.5 cores of user time on a 2-core host is 75% of capacity and must count as high")
	}
	if _, on := small["state.cpu.runqueue_high"]; !on {
		t.Error("6 runnable on a 2-core host is 3x capacity and must count as a queue")
	}

	// The identical numbers on a 64-core box are 2.3% of capacity and a
	// run queue well under the core count: completely unremarkable.
	large := EvaluateOn(States, rows, Machine{NCPU: 64})
	if _, on := large["state.cpu.user_high"]; on {
		t.Error("1.5 cores of user time on a 64-core host is 2% of capacity and must NOT count as high")
	}
	if _, on := large["state.cpu.runqueue_high"]; on {
		t.Error("6 runnable on a 64-core host is far below capacity and must NOT count as a queue")
	}

	// And the diagnoses that depend on them follow suit.
	if got := diagnosisIDs(Diagnose(Diagnoses, small)); !contains(got, "diagnosis.cpu_saturated_own_workload") {
		t.Errorf("the 2-core host should be diagnosed as CPU-saturated, got %v", got)
	}
	if got := Diagnose(Diagnoses, large); len(got) > 0 {
		t.Errorf("the 64-core host should have no diagnosis at all, got %v", diagnosisIDs(got))
	}
}

// Host detection must always yield a usable number: a zero core count
// would make every scale-relative threshold zero, activating every state
// on every machine -- the worst possible failure mode for this design.
func TestHostAlwaysReturnsUsableCoreCount(t *testing.T) {
	if n := Host().NCPU; n < 1 {
		t.Fatalf("Host().NCPU must be at least 1, got %d", n)
	}
	// An explicitly bogus override must still be clamped.
	SetHost(Machine{NCPU: 0})
	if n := Host().NCPU; n < 1 {
		t.Errorf("SetHost must clamp a bogus core count to at least 1, got %d", n)
	}
}

// io_bound and io_slow_not_busy describe genuinely different failures
// that share the same symptom (tasks stalling on I/O). What separates
// them is whether any disk is actually saturated -- a degraded or
// throttled volume stalls tasks WITHOUT being busy, and telling an
// operator "the disk is overloaded" when it is idle sends them looking
// in the wrong place entirely.
func TestIOStallWithoutSaturationIsADifferentDiagnosis(t *testing.T) {
	// Slow storage: stalls are real, but the disk is nearly idle.
	slow := []pcp.DiffRow{
		row("kernel.all.pressure.io.some.avg", 45),
		row("disk.dev.avactive", 0.05),
		row("disk.dev.aveq", 0.2),
	}
	got := diagnosisIDs(Diagnose(Diagnoses, EvaluateOn(States, slow, testMachine)))
	if !contains(got, "diagnosis.io_slow_not_busy") {
		t.Errorf("stalls on an idle disk should diagnose slow storage, got %v", got)
	}
	if contains(got, "diagnosis.io_bound") {
		t.Errorf("io_bound must not fire when no disk is saturated, got %v", got)
	}

	// Overloaded storage: the disk is genuinely pinned.
	busy := []pcp.DiffRow{
		row("kernel.all.pressure.io.some.avg", 45),
		row("disk.dev.avactive", 0.96),
		row("disk.dev.aveq", 22),
	}
	got2 := diagnosisIDs(Diagnose(Diagnoses, EvaluateOn(States, busy, testMachine)))
	if !contains(got2, "diagnosis.io_bound") {
		t.Errorf("a saturated disk with stalls should diagnose io_bound, got %v", got2)
	}
	if contains(got2, "diagnosis.io_slow_not_busy") {
		t.Errorf("io_slow_not_busy must not fire when the disk IS saturated, got %v", got2)
	}
}

// Kernel-mode CPU overhead is only meaningful as a diagnosis when the
// application itself is NOT the thing burning CPU -- otherwise high sys
// time is just the normal cost of a busy workload doing syscalls.
func TestKernelOverheadRequiresUserToBeQuiet(t *testing.T) {
	// 8-core host: 40% of capacity in kernel mode, application quiet.
	kernelHeavy := []pcp.DiffRow{
		row("kernel.all.cpu.sys", 3200),
		row("kernel.all.cpu.user", 500),
	}
	got := diagnosisIDs(Diagnose(Diagnoses, EvaluateOn(States, kernelHeavy, testMachine)))
	if !contains(got, "diagnosis.kernel_cpu_overhead") {
		t.Errorf("high sys with quiet user should diagnose kernel overhead, got %v", got)
	}

	// Both high: this is just a busy application, not a kernel problem.
	bothHigh := []pcp.DiffRow{
		row("kernel.all.cpu.sys", 3200),
		row("kernel.all.cpu.user", 4500),
	}
	got2 := diagnosisIDs(Diagnose(Diagnoses, EvaluateOn(States, bothHigh, testMachine)))
	if contains(got2, "diagnosis.kernel_cpu_overhead") {
		t.Errorf("kernel overhead must not fire when userspace is also busy, got %v", got2)
	}
}

// Every diagnosis must reference states that actually exist. A typo in a
// state ID would silently make a diagnosis permanently unreachable --
// failing quietly rather than loudly, which is the worst kind of bug for
// a rule catalog that is meant to grow.
func TestAllDiagnosesReferenceRealStates(t *testing.T) {
	known := map[string]bool{}
	for _, st := range States {
		known[st.ID] = true
	}
	for _, d := range Diagnoses {
		for _, group := range [][]string{d.RequiresAll, d.RequiresAny, d.RequiresNone} {
			for _, id := range group {
				if !known[id] {
					t.Errorf("diagnosis %s references unknown state %q", d.ID, id)
				}
			}
		}
	}
}

// State and diagnosis IDs must be unique: a duplicate would silently
// shadow the earlier definition in the active-state map.
func TestCatalogIDsAreUnique(t *testing.T) {
	seen := map[string]bool{}
	for _, st := range States {
		if seen[st.ID] {
			t.Errorf("duplicate state ID %q", st.ID)
		}
		seen[st.ID] = true
	}
	seenD := map[string]bool{}
	for _, d := range Diagnoses {
		if seenD[d.ID] {
			t.Errorf("duplicate diagnosis ID %q", d.ID)
		}
		seenD[d.ID] = true
	}
}
