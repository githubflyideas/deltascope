package reasoning

import (
	"testing"

	"github.com/githubflyideas/deltascope/internal/pcp"
)

// H1: counter-valued states that only looked at the mean flapped on the
// threshold line and missed sub-window bursts. Each now has a peak-based
// twin. A burst whose mean is under the line but whose peak is over it must
// fire the spike, deterministically.
func TestCounterSpikesCaughtByPeak(t *testing.T) {
	m := Machine{NCPU: 4}
	cases := []struct {
		metric, meanState, spikeState string
		mean, peak                    float64
	}{
		{"network.tcp.retranssegs", "state.net.retransmit_high", "state.net.retransmit_spike", 4, 120},
		{"network.tcp.activeopens", "state.net.conn_churn_high", "state.net.conn_churn_spike", 90, 900},
		{"mem.vmstat.pgmajfault", "state.mem.major_faults_high", "state.mem.major_faults_spike", 20, 400},
	}
	for _, c := range cases {
		mean, peak := c.mean, c.peak
		rows := []pcp.DiffRow{{
			Metric: c.metric, B: &mean, BMax: &peak, BCount: 1400, Verdict: pcp.VFlat,
		}}
		active := EvaluateOn(States, rows, m)
		if _, on := active[c.meanState]; on {
			t.Errorf("%s: mean state must NOT fire below the line", c.metric)
		}
		if _, on := active[c.spikeState]; !on {
			t.Errorf("%s: spike state must fire on the peak, active=%v", c.metric, ActiveIDs(active))
		}
		// deterministic across repeats -- no flapping
		for i := 0; i < 4; i++ {
			if _, on := EvaluateOn(States, rows, m)[c.spikeState]; !on {
				t.Fatalf("%s: spike must not flap", c.metric)
			}
		}
	}
}

// A thin window cannot support any of the new spike claims.
func TestCounterSpikesNeedSamples(t *testing.T) {
	m := Machine{NCPU: 4}
	mean, peak := 1.0, 500.0
	for _, metric := range []string{"network.tcp.retranssegs", "network.tcp.activeopens", "mem.vmstat.pgmajfault"} {
		rows := []pcp.DiffRow{{Metric: metric, B: &mean, BMax: &peak, BCount: 5, Verdict: pcp.VFlat}}
		for _, st := range ActiveIDs(EvaluateOn(States, rows, m)) {
			if len(st) > 6 && st[len(st)-6:] == "_spike" {
				t.Errorf("%s: a 5-sample window must not fire %s", metric, st)
			}
		}
	}
}

// A retransmit spike must reach a diagnosis, not sit as an orphan active
// state with "no diagnosis".
func TestRetransmitSpikeReachesDiagnosis(t *testing.T) {
	m := Machine{NCPU: 4}
	mean, peak := 4.0, 200.0
	rows := []pcp.DiffRow{{
		Metric: "network.tcp.retranssegs", B: &mean, BMax: &peak, BCount: 1400, Verdict: pcp.VFlat,
	}}
	got := diagnosisIDs(Diagnose(Diagnoses, EvaluateOn(States, rows, m)))
	if !contains(got, "diagnosis.network_loss") {
		t.Errorf("a retransmit spike should diagnose network_loss, got %v", got)
	}
}
