package pcp

import "testing"

// A block's status light answers "is there trouble", and an improvement is not
// trouble -- so the light must not move. What must change is the sentence: a
// resource that improved dramatically used to render the same single word
// ("normal") as a resource where nothing happened at all.
func TestTriageReportsAnImprovementWithoutChangingTheLight(t *testing.T) {
	rows := []DiffRow{
		{Metric: "kernel.all.cpu.user", Category: "CPU", Verdict: VBetter,
			A: f(225), B: f(11), DeltaPct: f(-95)},
	}
	blocks := Triage(rows)
	var cpu *TriageBlock
	for i := range blocks {
		if blocks[i].Key == "cpu" {
			cpu = &blocks[i]
		}
	}
	if cpu == nil {
		t.Fatal("no cpu block")
	}
	if cpu.Status != TriageOK {
		t.Errorf("an improvement moved the status light to %s", cpu.Status)
	}
	if cpu.Improved == "" {
		t.Fatal("a 95% fall in user CPU was reported as nothing at all")
	}
	if cpu.ImprovedPct == nil || *cpu.ImprovedPct != -95 {
		t.Errorf("improved_pct = %v, want -95", cpu.ImprovedPct)
	}
	// Headline stays the flat wording: the improvement is a separate field so
	// no existing reader of Headline changes meaning.
	if cpu.Headline != "normal" {
		t.Errorf("headline = %q, want the unchanged flat wording", cpu.Headline)
	}
}

// Improvements are held to the same bar as regressions: only core metrics
// speak. A jittery secondary counter falling is not news, and good news should
// not be easier to earn than bad.
func TestTriageIgnoresANonCoreImprovement(t *testing.T) {
	rows := []DiffRow{
		{Metric: "network.tcp.insegs", Category: "Network", Verdict: VBetter,
			A: f(1000), B: f(100), DeltaPct: f(-90)},
	}
	for _, b := range Triage(rows) {
		if b.Key == "net" && b.Improved != "" {
			t.Errorf("a non-core metric claimed an improvement: %q", b.Improved)
		}
	}
}

// VBetter with no ratio means a metric went from zero to non-zero under
// better-is-up polarity. For mem.util.available that is a metric which was not
// sampled in the baseline half far more often than it is memory coming back --
// the same artifact the VWatch branch already distrusts, and it must not get in
// through the door marked "improvement".
func TestTriageRefusesAnUnquantifiedImprovement(t *testing.T) {
	rows := []DiffRow{
		{Metric: "mem.util.available", Category: "Memory", Verdict: VBetter,
			A: nil, B: f(4000000), DeltaPct: nil},
	}
	for _, b := range Triage(rows) {
		if b.Key == "mem" && b.Improved != "" {
			t.Errorf("an appeared metric was reported as an improvement: %q", b.Improved)
		}
	}
}

// A resource can regress on one core metric and improve on another. Hiding the
// improvement because the light is red would hide half of what the window
// shows, so the two fields are independent.
func TestTriageReportsAnImprovementAlongsideARegression(t *testing.T) {
	rows := []DiffRow{
		{Metric: "kernel.all.cpu.sys", Category: "CPU", Verdict: VWorse, DeltaPct: f(300)},
		{Metric: "kernel.all.cpu.steal", Category: "CPU", Verdict: VBetter, DeltaPct: f(-80)},
	}
	for _, b := range Triage(rows) {
		if b.Key != "cpu" {
			continue
		}
		if b.Status != TriageBad {
			t.Errorf("status = %s, want bad -- sys CPU tripled", b.Status)
		}
		if b.Improved == "" {
			t.Error("the steal-time improvement was dropped because the block was red")
		}
	}
}

// Ranking within the improvements: the largest movement wins the line, the same
// rule the regression path uses to pick a headline.
func TestTriageKeepsTheLargestImprovement(t *testing.T) {
	rows := []DiffRow{
		{Metric: "kernel.all.cpu.user", Category: "CPU", Verdict: VBetter, DeltaPct: f(-95), Label: "user CPU"},
		{Metric: "kernel.all.load", Category: "CPU", Verdict: VBetter, DeltaPct: f(-30), Label: "load"},
	}
	for _, b := range Triage(rows) {
		if b.Key == "cpu" && (b.ImprovedPct == nil || *b.ImprovedPct != -95) {
			t.Errorf("improved_pct = %v, want the larger -95", b.ImprovedPct)
		}
	}
}
