package reasoning

import (
	"testing"

	"github.com/githubflyideas/deltascope/internal/pcp"
)

// coreRow builds a per-core row with sample statistics, which is what a
// real archive gives us and what the peak conditions read.
func coreRow(metric, inst string, mean, max float64, count int) pcp.DiffRow {
	m, mx := mean, max
	return pcp.DiffRow{
		Metric: metric, Instance: inst, B: &m, BMax: &mx, BCount: count,
		Verdict: pcp.VFlat,
	}
}

// The defect: a core pegged for 20 of 60 minutes averages to 333 ms/s,
// which is under every threshold worth setting, and the window is
// reported as healthy.
func TestIntermittentSaturationIsSeenThroughTheMean(t *testing.T) {
	rows := []pcp.DiffRow{
		// cpu1 spent a third of the hour pegged: mean 320, peak 980.
		coreRow("kernel.percpu.cpu.user", "cpu1", 260, 900, 1400),
		coreRow("kernel.percpu.cpu.sys", "cpu1", 60, 90, 1400),
		coreRow("kernel.percpu.cpu.user", "cpu0", 20, 60, 1400),
		coreRow("kernel.percpu.cpu.sys", "cpu0", 15, 40, 1400),
	}
	active := EvaluateOn(States, rows, Machine{NCPU: 4})
	t.Logf("active: %v", ActiveIDs(active))

	if _, on := active["state.cpu.core_pegged"]; on {
		t.Error("core_pegged is a mean-based state and must not fire at a mean of 320 ms/s")
	}
	if _, on := active["state.cpu.core_peaked"]; !on {
		t.Fatalf("core_peaked must fire on a 900 ms/s peak, active=%v", ActiveIDs(active))
	}
	got := diagnosisIDs(Diagnose(Diagnoses, active))
	if !contains(got, "diagnosis.intermittent_cpu_saturation") {
		t.Errorf("expected intermittent_cpu_saturation, got %v", got)
	}
	// And the evidence must say "peak", not just the misleading mean.
	ev := active["state.cpu.core_peaked"].Evidence
	t.Logf("evidence: %v", ev)
	if len(ev) == 0 || !containsSubstr(ev[0], "peak=") {
		t.Errorf("evidence must cite the peak, got %v", ev)
	}
}

// A thin window cannot support a peak claim: three samples with one
// outlier is a collection artifact, not an event.
func TestPeakOnThinWindowIsRejected(t *testing.T) {
	rows := []pcp.DiffRow{
		coreRow("kernel.percpu.cpu.user", "cpu0", 300, 980, 3),
	}
	active := EvaluateOn(States, rows, Machine{NCPU: 4})
	if _, on := active["state.cpu.core_peaked"]; on {
		t.Error("a 3-sample window must not satisfy a peak state")
	}
}

// A build of pmlogsummary that omits min/max leaves BMax nil. Peak states
// must simply not match rather than treating the absence as zero.
func TestMissingStatsDisablesPeakStatesQuietly(t *testing.T) {
	m, cnt := 300.0, 1400
	rows := []pcp.DiffRow{{
		Metric: "kernel.percpu.cpu.user", Instance: "cpu0",
		B: &m, BCount: cnt, Verdict: pcp.VFlat,
	}}
	active := EvaluateOn(States, rows, Machine{NCPU: 4})
	if _, on := active["state.cpu.core_peaked"]; on {
		t.Error("a row with no recorded peak must not satisfy a peak condition")
	}
}

// Sustained saturation must produce the sustained diagnosis, not the
// intermittent one -- otherwise the report understates a worse problem.
func TestSustainedSaturationSuppressesIntermittent(t *testing.T) {
	rows := []pcp.DiffRow{
		coreRow("kernel.percpu.cpu.user", "cpu1", 900, 995, 1400),
		coreRow("kernel.percpu.cpu.user", "cpu0", 20, 50, 1400),
		coreRow("kernel.percpu.cpu.user", "cpu2", 18, 45, 1400),
		coreRow("kernel.percpu.cpu.user", "cpu3", 22, 55, 1400),
	}
	active := EvaluateOn(States, rows, Machine{NCPU: 4})
	got := diagnosisIDs(Diagnose(Diagnoses, active))
	t.Logf("active=%v diagnoses=%v", ActiveIDs(active), got)
	if !contains(got, "diagnosis.single_core_saturated") {
		t.Errorf("sustained saturation should be diagnosed as such, got %v", got)
	}
	if contains(got, "diagnosis.intermittent_cpu_saturation") {
		t.Error("the intermittent diagnosis must be suppressed when the mean is saturated too")
	}
}

// Component peaks must combine with max(), not by summing: summing
// assumes user and sys peaked in the same sample, which is not knowable
// from summary statistics and would fabricate a spike.
func TestComponentPeaksAreNotSummed(t *testing.T) {
	rows := []pcp.DiffRow{
		coreRow("kernel.percpu.cpu.user", "cpu0", 200, 600, 1400),
		coreRow("kernel.percpu.cpu.sys", "cpu0", 200, 600, 1400),
	}
	var busiest *pcp.DiffRow
	for _, r := range Derive(rows) {
		if r.Metric == MetricBusiestCore {
			rr := r
			busiest = &rr
		}
	}
	if busiest == nil || busiest.BMax == nil {
		t.Fatal("busiest_core must carry a peak")
	}
	if *busiest.BMax != 600 {
		t.Errorf("combined peak must be max(600,600)=600, not the sum 1200; got %v", *busiest.BMax)
	}
	// The mean, by contrast, is genuinely additive.
	if busiest.B == nil || *busiest.B != 400 {
		t.Errorf("combined mean should be 200+200=400, got %v", busiest.B)
	}
}

// A steady high load and a bursty one both have a high peak and want
// opposite conclusions; the ratio condition is what separates them.
func TestPeakRatioSeparatesBurstyFromSteady(t *testing.T) {
	bursty := []pcp.DiffRow{{
		Metric: "kernel.all.cpu.user", B: fptr(600), BMax: fptr(3400), BCount: 1400,
		Verdict: pcp.VFlat,
	}}
	if _, on := EvaluateOn(States, bursty, Machine{NCPU: 4})["state.cpu.bursty"]; !on {
		t.Error("a 3400 peak against a 600 mean is bursty and must be flagged")
	}

	steady := []pcp.DiffRow{{
		Metric: "kernel.all.cpu.user", B: fptr(2900), BMax: fptr(3400), BCount: 1400,
		Verdict: pcp.VFlat,
	}}
	if _, on := EvaluateOn(States, steady, Machine{NCPU: 4})["state.cpu.bursty"]; on {
		t.Error("a 3400 peak against a 2900 mean is steady load, not a burst")
	}
}

// A near-zero mean makes the peak ratio arithmetically valid and
// semantically empty -- the same trap as a near-zero baseline in a
// percentage change.
func TestPeakRatioIgnoresNearZeroMean(t *testing.T) {
	rows := []pcp.DiffRow{{
		Metric: "kernel.all.cpu.user", B: fptr(0.002), BMax: fptr(0.9), BCount: 1400,
		Verdict: pcp.VFlat,
	}}
	if _, on := EvaluateOn(States, rows, Machine{NCPU: 4})["state.cpu.bursty"]; on {
		t.Error("a 450x ratio between two near-zero values must not count as a burst")
	}
}

func containsSubstr(s, sub string) bool {
	return len(s) >= len(sub) && (func() bool {
		for i := 0; i+len(sub) <= len(s); i++ {
			if s[i:i+len(sub)] == sub {
				return true
			}
		}
		return false
	})()
}
