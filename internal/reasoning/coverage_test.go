package reasoning

import (
	"testing"

	"github.com/githubflyideas/deltascope/internal/pcp"
)

// The bug the user hit: a state fired but no diagnosis matched, because
// context_switch_storm only had ONE diagnosis (scheduler_thrashing) and
// that diagnosis required a queue or PSI stall as corroboration. Run
// alone -- a real, sustained storm with no backed-up queue -- had no
// diagnosis to land in at all, so "1/59 active" printed alongside "no
// diagnosis matched any known pattern", which reads as a contradiction to
// anyone who does not already know the catalog is a lattice, not a tree.
func TestContextSwitchStormAloneStillProducesADiagnosis(t *testing.T) {
	rows := []pcp.DiffRow{
		rowDelta("kernel.all.pswitch", 1096, 222355, 20323, pcp.VWorse),
		rowDelta("kernel.all.cpu.user", 400, 420, 5, pcp.VFlat),
		rowDelta("kernel.all.runnable", 3, 3, 0, pcp.VFlat),
	}
	active := EvaluateOn(States, rows, Machine{NCPU: 4})
	if _, on := active["state.cpu.context_switch_storm"]; !on {
		t.Fatal("context_switch_storm should be active on this input")
	}
	got := diagnosisIDs(Diagnose(Diagnoses, active))
	if len(got) == 0 {
		t.Errorf("a state that fired alone produced NO diagnosis; got %v", got)
	}
	if !contains(got, "diagnosis.context_switch_overhead") {
		t.Errorf("expected the fallback diagnosis, got %v", got)
	}
}

// The stronger diagnosis (queue backed up too) must still win over the
// fallback when both conditions hold -- the fallback exists to catch the
// gap, not to compete with the more specific answer.
func TestSchedulerThrashingStillWinsOverFallback(t *testing.T) {
	rows := []pcp.DiffRow{
		row("kernel.all.pswitch", 250000),
		row("kernel.all.runnable", 20),
	}
	active := EvaluateOn(States, rows, Machine{NCPU: 4})
	got := diagnosisIDs(Diagnose(Diagnoses, active))
	if !contains(got, "diagnosis.scheduler_thrashing") {
		t.Errorf("expected scheduler_thrashing when the queue is also backed up, got %v", got)
	}
	if contains(got, "diagnosis.context_switch_overhead") {
		t.Errorf("the fallback must be suppressed when the specific diagnosis fires, got %v", got)
	}
}

// Every state in the catalog must be reachable through at least one
// diagnosis (RequiresAll or RequiresAny). A state that can never
// contribute to a diagnosis is worse than a missing state: it fires,
// reports "1 active", and then says "no diagnosis matched" right next to
// it -- which looks like a broken tool rather than a catalog gap.
func TestEveryStateContributesToAtLeastOneDiagnosis(t *testing.T) {
	referenced := map[string]bool{}
	for _, d := range Diagnoses {
		for _, id := range d.RequiresAll {
			referenced[id] = true
		}
		for _, id := range d.RequiresAny {
			referenced[id] = true
		}
	}
	for _, st := range States {
		if !referenced[st.ID] {
			t.Errorf("%s appears in no diagnosis's RequiresAll/RequiresAny, so it can fire "+
				"alone and never produce a diagnosis", st.ID)
		}
	}
}

// A state that can fire completely alone (no other state ever active at
// the same time by construction) must still resolve to a diagnosis on its
// own -- otherwise the previous test's guarantee is satisfied on paper
// (it appears in SOME diagnosis) while that diagnosis also requires a
// second state that may never co-occur with it.
func TestSingleActiveStatesEachYieldADiagnosis(t *testing.T) {
	cases := []struct {
		name string
		rows []pcp.DiffRow
	}{
		{"bursty alone", []pcp.DiffRow{{
			Metric: "kernel.all.cpu.user", B: fp2(600), BMax: fp2(3400), BCount: 1400, Verdict: pcp.VFlat,
		}}},
		{"kswapd alone", []pcp.DiffRow{row("mem.vmstat.pgscan_kswapd", 5000)}},
		{"io pressure full alone", []pcp.DiffRow{row("kernel.all.pressure.io.full.avg", 10)}},
		{"read heavy alone with saturation", []pcp.DiffRow{
			row("disk.all.read_bytes", 80000), row("disk.dev.avactive", 0.9), row("disk.dev.aveq", 5),
		}},
		{"reset storm alone", []pcp.DiffRow{row("network.tcp.outrsts", 200)}},
		{"orphan high alone", []pcp.DiffRow{row("network.sockstat.tcp.orphan", 5000)}},
	}
	for _, tc := range cases {
		active := EvaluateOn(States, tc.rows, Machine{NCPU: 4})
		if len(active) == 0 {
			t.Errorf("%s: expected at least one active state, got none", tc.name)
			continue
		}
		got := Diagnose(Diagnoses, active)
		if len(got) == 0 {
			t.Errorf("%s: active=%v produced NO diagnosis", tc.name, ActiveIDs(active))
		}
	}
}

func fp2(v float64) *float64 { return &v }
