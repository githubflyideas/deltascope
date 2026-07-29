package reasoning

import (
	"testing"

	"github.com/githubflyideas/deltascope/internal/pcp"
)

// percpu builds a per-core row set from a list of per-core busy values,
// split across the two components a real archive reports separately.
func percpu(userB, sysB []float64) []pcp.DiffRow {
	var out []pcp.DiffRow
	for i, v := range userB {
		vv := v
		out = append(out, pcp.DiffRow{
			Metric: "kernel.percpu.cpu.user", Instance: coreName(i),
			B: &vv, Verdict: pcp.VFlat,
		})
	}
	for i, v := range sysB {
		vv := v
		out = append(out, pcp.DiffRow{
			Metric: "kernel.percpu.cpu.sys", Instance: coreName(i),
			B: &vv, Verdict: pcp.VFlat,
		})
	}
	return out
}

func coreName(i int) string { return "cpu" + itoa(i) }

// The defect this fixes: one core pegged is invisible to every
// scale-relative aggregate threshold, on every machine size.
func TestSingleCoreSaturationIsDetectedAtAnyMachineSize(t *testing.T) {
	for _, ncpu := range []int{2, 4, 8, 16, 64} {
		user := make([]float64, ncpu)
		sys := make([]float64, ncpu)
		for i := range user {
			user[i], sys[i] = 20, 15 // idle-ish background on every core
		}
		// cpu1 is pegged: 700 user + 280 sys = 980 ms/s of one core
		user[1], sys[1] = 700, 280

		rows := append(percpu(user, sys),
			row("kernel.all.cpu.user", sum(user)),
			row("kernel.all.cpu.sys", sum(sys)),
			row("kernel.all.runnable", 3),
		)
		active := EvaluateOn(States, rows, Machine{NCPU: ncpu})
		if _, on := active["state.cpu.core_pegged"]; !on {
			t.Errorf("ncpu=%d: core_pegged must fire when one core is at 980 ms/s, active=%v",
				ncpu, ActiveIDs(active))
		}
		if _, on := active["state.cpu.serialized"]; !on {
			t.Errorf("ncpu=%d: serialized must fire when one core is pegged and the rest are idle", ncpu)
		}
		if _, on := active["state.cpu.all_cores_pegged"]; on {
			t.Errorf("ncpu=%d: all_cores_pegged must NOT fire when only one core is busy", ncpu)
		}
		got := diagnosisIDs(Diagnose(Diagnoses, active))
		if !contains(got, "diagnosis.single_core_saturated") {
			t.Errorf("ncpu=%d: expected single_core_saturated, got %v", ncpu, got)
		}
	}
}

// The user's actual host, from top: 4 cores, one `sh` busy-loop at 99.7%,
// machine-wide 19.2 us + 35.8 sy. Before this change the report said
// "CPU normal, 0 of 17 states active".
func TestUserReportedBusyLoopIsDiagnosed(t *testing.T) {
	// 4 cores: the loop lives on one, the rest carry desktop background.
	user := []float64{60, 640, 40, 28}
	sys := []float64{90, 350, 60, 40}
	rows := append(percpu(user, sys),
		row("kernel.all.cpu.user", 768),
		row("kernel.all.cpu.sys", 1432),
		row("kernel.all.runnable", 3),
		row("kernel.all.cpu.steal", 0),
	)
	active := EvaluateOn(States, rows, Machine{NCPU: 4})
	t.Logf("active: %v", ActiveIDs(active))
	got := diagnosisIDs(Diagnose(Diagnoses, active))
	t.Logf("diagnoses: %v", got)
	if !contains(got, "diagnosis.single_core_saturated") {
		t.Errorf("the reported busy-loop must produce a diagnosis, got %v", got)
	}
}

// A uniformly busy machine is a capacity problem, not a serialization
// problem, and the two must not be conflated.
func TestAllCoresPeggedIsCapacityNotSerialization(t *testing.T) {
	user := []float64{700, 720, 690, 710}
	sys := []float64{200, 190, 210, 195}
	rows := append(percpu(user, sys),
		row("kernel.all.cpu.user", 2820),
		row("kernel.all.runnable", 20),
	)
	active := EvaluateOn(States, rows, Machine{NCPU: 4})
	if _, on := active["state.cpu.all_cores_pegged"]; !on {
		t.Fatalf("all_cores_pegged should fire, active=%v", ActiveIDs(active))
	}
	if _, on := active["state.cpu.serialized"]; on {
		t.Error("serialized must NOT fire when the load is spread evenly")
	}
	got := diagnosisIDs(Diagnose(Diagnoses, active))
	if !contains(got, "diagnosis.cpu_capacity_exhausted") {
		t.Errorf("expected cpu_capacity_exhausted, got %v", got)
	}
	if contains(got, "diagnosis.single_core_saturated") {
		t.Errorf("single_core_saturated must be suppressed when all cores are pegged, got %v", got)
	}
}

// iowait must not be counted as busy: a core waiting on storage is not
// doing work, and treating it as a pegged core would turn every disk
// problem into a phantom CPU problem.
func TestIowaitIsNotCountedAsCoreBusy(t *testing.T) {
	rows := []pcp.DiffRow{
		{Metric: "kernel.percpu.cpu.user", Instance: "cpu0", B: fptr(30), Verdict: pcp.VFlat},
		{Metric: "kernel.percpu.cpu.wait.total", Instance: "cpu0", B: fptr(950), Verdict: pcp.VFlat},
	}
	active := EvaluateOn(States, rows, Machine{NCPU: 4})
	if _, on := active["state.cpu.core_pegged"]; on {
		t.Error("a core sitting in iowait must not count as pegged")
	}
}

// With no per-core metrics in the archive, derived rows must be absent
// rather than fabricated as zero -- a zero here reads as "no core busy",
// the most misleading possible answer.
func TestNoPerCoreMetricsProducesNoDerivedRows(t *testing.T) {
	rows := []pcp.DiffRow{row("kernel.all.cpu.user", 500)}
	if got := Derive(rows); len(got) != 0 {
		t.Errorf("expected no derived rows without per-core input, got %d", len(got))
	}
	active := EvaluateOn(States, rows, Machine{NCPU: 4})
	if _, on := active["state.cpu.core_pegged"]; on {
		t.Error("core_pegged must not fire when the archive has no per-core data")
	}
}

// A resized VM has a different core count in each window; the per-side
// mean must not be computed across the union of instances.
func TestDifferentCoreCountPerWindow(t *testing.T) {
	rows := []pcp.DiffRow{
		{Metric: "kernel.percpu.cpu.user", Instance: "cpu0", A: fptr(500), B: fptr(900), Verdict: pcp.VFlat},
		{Metric: "kernel.percpu.cpu.user", Instance: "cpu1", A: fptr(500), B: fptr(900), Verdict: pcp.VFlat},
		// cpu2/cpu3 only exist in B (the VM grew)
		{Metric: "kernel.percpu.cpu.user", Instance: "cpu2", B: fptr(10), Verdict: pcp.VFlat},
		{Metric: "kernel.percpu.cpu.user", Instance: "cpu3", B: fptr(10), Verdict: pcp.VFlat},
	}
	var busiest *pcp.DiffRow
	for _, r := range Derive(rows) {
		if r.Metric == MetricBusiestCore {
			rr := r
			busiest = &rr
		}
	}
	if busiest == nil {
		t.Fatal("busiest_core row missing")
	}
	if busiest.A == nil || *busiest.A != 500 {
		t.Errorf("window A busiest should be 500 (only cpu0/cpu1 existed), got %v", busiest.A)
	}
	if busiest.B == nil || *busiest.B != 900 {
		t.Errorf("window B busiest should be 900, got %v", busiest.B)
	}
}

func sum(vs []float64) float64 {
	t := 0.0
	for _, v := range vs {
		t += v
	}
	return t
}

func fptr(v float64) *float64 { return &v }
