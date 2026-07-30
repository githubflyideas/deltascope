package reasoning

import (
	"testing"

	"github.com/githubflyideas/deltascope/internal/pcp"
)

func devRow(metric, inst string, b float64) pcp.DiffRow {
	v := b
	return pcp.DiffRow{Metric: metric, Instance: inst, B: &v, Verdict: pcp.VFlat}
}

// The C2 defect: state.io.saturated requires avactive>=0.7 AND aveq>=2.
// With independent matching, sda (busy, empty queue) and sdb (idle, deep
// queue) together satisfy it, reporting a saturated disk that does not
// exist. SameInstance must require one disk to satisfy both.
func TestSaturatedNeedsOneDiskToMeetBothConditions(t *testing.T) {
	m := Machine{NCPU: 4}

	// sda busy but empty queue (doing its job), sdb idle but transient deep
	// queue. No single disk is saturated.
	split := []pcp.DiffRow{
		devRow("disk.dev.avactive", "sda", 0.95),
		devRow("disk.dev.aveq", "sda", 0.1),
		devRow("disk.dev.avactive", "sdb", 0.05),
		devRow("disk.dev.aveq", "sdb", 8),
	}
	if _, on := EvaluateOn(States, split, m)["state.io.saturated"]; on {
		t.Error("saturated must NOT fire when the busy disk and the queued disk are different devices")
	}

	// sdb genuinely saturated: busy AND queued on the same device.
	real := []pcp.DiffRow{
		devRow("disk.dev.avactive", "sda", 0.05),
		devRow("disk.dev.aveq", "sda", 0),
		devRow("disk.dev.avactive", "sdb", 0.92),
		devRow("disk.dev.aveq", "sdb", 12),
	}
	a := EvaluateOn(States, real, m)
	if _, on := a["state.io.saturated"]; !on {
		t.Fatalf("saturated must fire when one disk is both busy and queued, active=%v", ActiveIDs(a))
	}
	// evidence should point at the actually-saturated device
	ev := a["state.io.saturated"].Evidence
	joined := ""
	for _, e := range ev {
		joined += e + " "
	}
	if !contains([]string{joined}, joined) || len(ev) != 2 {
		t.Errorf("expected two evidence rows for the saturated disk, got %v", ev)
	}
}

// A whole-machine (instance-less) condition ANDed into a same-instance
// state must still work: it applies to whichever instance is under test.
func TestSameInstanceAllowsInstancelessCondition(t *testing.T) {
	// Hypothetical: per-disk busy AND a machine-wide PSI. The PSI row has no
	// instance and should satisfy the condition for the candidate disk.
	states := []State{{
		ID: "state.test.combo", Domain: "io", SameInstance: true,
		When: []Cond{
			{Metric: "disk.dev.avactive", BGte: fptr(0.7)},
			{Metric: "kernel.all.pressure.io.some.avg", BGte: fptr(10)},
		},
	}}
	rows := []pcp.DiffRow{
		devRow("disk.dev.avactive", "sda", 0.9),
		{Metric: "kernel.all.pressure.io.some.avg", B: fptr(42), Verdict: pcp.VFlat},
	}
	if _, on := EvaluateOn(states, rows, Machine{NCPU: 4})["state.test.combo"]; !on {
		t.Error("an instance-less condition must satisfy a same-instance state for the candidate disk")
	}
}

// Guard: a normal (non-SameInstance) state is unchanged by the new path.
func TestIndependentMatchStillWorks(t *testing.T) {
	rows := []pcp.DiffRow{row("kernel.all.cpu.steal", 1600)}
	if _, on := EvaluateOn(States, rows, Machine{NCPU: 8})["state.cpu.steal_high"]; !on {
		t.Error("independent single-condition states must be unaffected")
	}
}
