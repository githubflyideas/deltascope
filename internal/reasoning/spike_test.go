package reasoning

import (
	"testing"

	"github.com/githubflyideas/deltascope/internal/pcp"
)

// The user's real scenario: a context-switch storm that started partway
// through the window. Its PEAK is far past the threshold, but the hourly
// MEAN sits right on the line (200000/s on a 4-core host), so the
// mean-only state flaps -- "多点几次能抓到，也有好几次抓不到". A peak-aware
// sibling state must fire stably on the peak the moment the storm appears.
func TestContextSwitchSpikeCaughtByPeakNotMean(t *testing.T) {
	m := Machine{NCPU: 4} // storm threshold = 50000 * 4 = 200000/s

	// mean 195000 (just UNDER the line -- this is the flapping case), but
	// the window peaked at 2.2M/s during the storm, over 1400 samples.
	mean, max := 195000.0, 2_200_000.0
	rows := []pcp.DiffRow{{
		Metric: "kernel.all.pswitch",
		B:      &mean, BMax: &max, BCount: 1400, Verdict: pcp.VFlat,
	}}

	active := EvaluateOn(States, rows, m)
	if _, on := active["state.cpu.context_switch_storm"]; on {
		t.Error("the sustained state must NOT fire when the mean is below the line")
	}
	if _, on := active["state.cpu.context_switch_spike"]; !on {
		t.Fatalf("the peak state must fire on a 2.2M/s peak, active=%v", ActiveIDs(active))
	}
	got := diagnosisIDs(Diagnose(Diagnoses, active))
	if !contains(got, "diagnosis.context_switch_spike") {
		t.Errorf("expected diagnosis.context_switch_spike, got %v", got)
	}
	// and it must NOT flap: same numbers, evaluated again, same result.
	for i := 0; i < 5; i++ {
		a := EvaluateOn(States, rows, m)
		if _, on := a["state.cpu.context_switch_spike"]; !on {
			t.Fatal("peak state must be deterministic across runs, not flap")
		}
	}
}

// A sustained storm (mean over the line) is the stronger statement and
// must suppress the intermittent spike diagnosis, exactly as
// core_pegged suppresses the intermittent core_peaked path.
func TestSustainedStormSuppressesSpikeDiagnosis(t *testing.T) {
	m := Machine{NCPU: 4}
	mean, max := 260000.0, 2_200_000.0
	rows := []pcp.DiffRow{{
		Metric: "kernel.all.pswitch", B: &mean, BMax: &max, BCount: 1400, Verdict: pcp.VFlat,
	}}
	active := EvaluateOn(States, rows, m)
	if _, on := active["state.cpu.context_switch_storm"]; !on {
		t.Fatal("sustained storm should fire the mean state")
	}
	got := diagnosisIDs(Diagnose(Diagnoses, active))
	if contains(got, "diagnosis.context_switch_spike") {
		t.Errorf("the intermittent spike diagnosis must be suppressed when the storm is sustained, got %v", got)
	}
}

// A thin window (few samples) must not let one outlier sample read as a
// spike -- the same MinSamples guard core_peaked carries.
func TestContextSwitchSpikeNeedsEnoughSamples(t *testing.T) {
	m := Machine{NCPU: 4}
	mean, max := 100.0, 2_200_000.0
	rows := []pcp.DiffRow{{
		Metric: "kernel.all.pswitch", B: &mean, BMax: &max, BCount: 4, Verdict: pcp.VFlat,
	}}
	active := EvaluateOn(States, rows, m)
	if _, on := active["state.cpu.context_switch_spike"]; on {
		t.Error("4 samples must not support a spike claim")
	}
}

// fork storms have the same brief-spike shape.
func TestForkSpikeCaughtByPeak(t *testing.T) {
	m := Machine{NCPU: 4} // fork threshold = 100 * 4 = 400/s
	mean, max := 380.0, 9000.0
	rows := []pcp.DiffRow{{
		Metric: "kernel.all.sysfork", B: &mean, BMax: &max, BCount: 1400, Verdict: pcp.VFlat,
	}}
	active := EvaluateOn(States, rows, m)
	if _, on := active["state.cpu.fork_spike"]; !on {
		t.Fatalf("fork_spike must fire on a 9000/s peak, active=%v", ActiveIDs(active))
	}
}

// The silence guarantee still holds: a machine whose counters are quiet on
// both mean and peak activates nothing.
func TestQuietCountersNoSpike(t *testing.T) {
	m := Machine{NCPU: 4}
	cs, csMax := 3000.0, 8000.0
	fk, fkMax := 5.0, 40.0
	rows := []pcp.DiffRow{
		{Metric: "kernel.all.pswitch", B: &cs, BMax: &csMax, BCount: 1400, Verdict: pcp.VFlat},
		{Metric: "kernel.all.sysfork", B: &fk, BMax: &fkMax, BCount: 1400, Verdict: pcp.VFlat},
	}
	active := EvaluateOn(States, rows, m)
	for _, id := range []string{"state.cpu.context_switch_spike", "state.cpu.fork_spike",
		"state.cpu.context_switch_storm", "state.cpu.fork_storm"} {
		if _, on := active[id]; on {
			t.Errorf("%s must not fire on quiet counters", id)
		}
	}
}
