package pcp

import "testing"

func dr(metric string, delta *float64, a, b *float64, v Verdict) DiffRow {
	return drCat("CPU", metric, delta, a, b, v)
}
func drCat(cat, metric string, delta *float64, a, b *float64, v Verdict) DiffRow {
	return DiffRow{Metric: metric, Category: cat, DeltaPct: delta, A: a, B: b, Verdict: v}
}

// H2: a boundary artifact (a core metric that merely "appeared", DeltaPct
// nil) must not outrank a real quantified regression and steal the CPU
// block's headline. Before the fix its magnitude was 1e18.
func TestQuantifiedRegressionBeatsAppearedArtifact(t *testing.T) {
	rows := []DiffRow{
		// a real +300% regression on a core metric
		dr("kernel.all.cpu.user", f(300), f(100), f(400), VWorse),
		// a core metric that just "appeared" -- no baseline, DeltaPct nil
		dr("kernel.all.load", nil, nil, f(1), VWorse),
	}
	blocks := Triage(rows)
	var cpu *TriageBlock
	for i := range blocks {
		if blocks[i].Key == "cpu" {
			cpu = &blocks[i]
		}
	}
	if cpu == nil || cpu.Status != TriageBad {
		t.Fatalf("cpu block should be bad, got %+v", cpu)
	}
	// headline must describe the measured regression, not the artifact
	if cpu.WorstPct == nil || *cpu.WorstPct != 300 {
		t.Errorf("headline should come from the +300%% regression, got WorstPct=%v headline=%q",
			cpu.WorstPct, cpu.Headline)
	}
}

// When two real regressions compete, the larger still wins.
func TestLargerQuantifiedRegressionWins(t *testing.T) {
	rows := []DiffRow{
		dr("kernel.all.cpu.user", f(120), f(100), f(220), VWorse),
		dr("kernel.all.cpu.sys", f(450), f(20), f(110), VWorse),
	}
	blocks := Triage(rows)
	for _, b := range blocks {
		if b.Key == "cpu" {
			if b.WorstPct == nil || *b.WorstPct != 450 {
				t.Errorf("the +450%% row should own the headline, got %v", b.WorstPct)
			}
		}
	}
}

// If ALL that a block has is an appeared core metric, it still surfaces --
// we prefer quantified, but an unquantified core signal is better than
// silence.
func TestAppearedCoreStillSurfacesWhenAlone(t *testing.T) {
	rows := []DiffRow{drCat("Memory", "mem.vmstat.oom_kill", nil, nil, f(3), VWorse)}
	// oom_kill maps to mem
	blocks := Triage(rows)
	for _, b := range blocks {
		if b.Key == "mem" && b.Status != TriageBad {
			t.Errorf("an appeared OOM-kill core metric alone should still redden the block, got %q", b.Status)
		}
	}
}
