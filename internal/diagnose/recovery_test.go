package diagnose

import (
	"strings"
	"testing"

	"github.com/githubflyideas/deltascope/internal/pcp"
	"github.com/githubflyideas/deltascope/internal/state"
)

// These tests exist because of one observation on a real host: after a bug in
// nodedata-linux was fixed, user CPU fell from ~2.25 cores to ~0.1, and the
// one-click page reported "No regression and no configuration changes
// detected". True, and it threw away the most significant thing that happened
// in the window.
//
// Reporting it is easy. Reporting it without lying is the whole difficulty,
// because the aggregate metric cannot tell a fixed bug from a dead service --
// both drive kernel.all.cpu.user to the floor. So most of what follows asserts
// SILENCE: the cases where the tool must not say "improved" outnumber the case
// where it may.

// okBlocks is a clean four-resource board, the state every test here starts in
// so that the improvement branch is the only thing that can fire.
func okBlocks() []pcp.TriageBlock {
	return []pcp.TriageBlock{
		{Key: "cpu", Label: "CPU", Status: pcp.TriageOK, Headline: "normal"},
		{Key: "mem", Label: "Memory", Status: pcp.TriageOK, Headline: "normal"},
		{Key: "disk", Label: "Disk", Status: pcp.TriageOK, Headline: "normal"},
		{Key: "net", Label: "Network", Status: pcp.TriageOK, Headline: "normal"},
	}
}

func improvedCPU(blocks []pcp.TriageBlock, pct float64) []pcp.TriageBlock {
	for i := range blocks {
		if blocks[i].Key == "cpu" {
			blocks[i].Improved = "user CPU -95%"
			blocks[i].ImprovedPct = f(pct)
		}
	}
	return blocks
}

// The case from the host: the process is still there, using far less. This is
// the only shape that earns the claim.
func TestRecoveryIsReportedWhenTheProcessIsStillRunning(t *testing.T) {
	out := &Diagnosis{Triage: improvedCPU(okBlocks(), -95)}
	pd := state.ProcDiff{Rows: []state.ProcRow{
		{Name: "nodedata-linux-", CPUPctA: f(225), CPUPctB: f(11), CPUDelta: f(-95), Verdict: state.PVBetter},
		{Name: "sshd", CPUPctA: f(0.1), CPUPctB: f(0.1), Verdict: state.PVFlat},
	}}

	synthesize(out, nil, 0, pd, state.Diff{})

	if out.Severity != "ok" {
		t.Errorf("severity = %q, want ok -- an improvement is not trouble", out.Severity)
	}
	if !contains(out.Headline, "improved") || !contains(out.Headline, "CPU") {
		t.Errorf("headline should say CPU improved, got %q", out.Headline)
	}
	// The point of the evidence line: the reader can check the claim without
	// leaving the page.
	ev := strings.Join(out.Evidence, " ")
	for _, want := range []string{"nodedata-linux-", "225%", "11%", "still running"} {
		if !contains(ev, want) {
			t.Errorf("evidence does not mention %q: %q", want, ev)
		}
	}
	if contains(out.Headline, "%!") || contains(ev, "%!") {
		t.Errorf("formatting artifact: %q / %q", out.Headline, ev)
	}
	t.Logf("headline: %s", out.Headline)
	t.Logf("evidence: %s", ev)
}

// The trap, stated as a test. The aggregate fell exactly as far as it did
// above; the difference is that the process it belonged to is gone. Calling
// that an improvement would be the tool telling an operator not to look at a
// service that died.
func TestRecoveryIsNotClaimedWhenTheProcessDied(t *testing.T) {
	out := &Diagnosis{Triage: improvedCPU(okBlocks(), -95)}
	pd := state.ProcDiff{Rows: []state.ProcRow{
		{Name: "nodedata-linux-", CPUPctA: f(225), CPUPctB: nil, Verdict: state.PVGone},
	}}

	synthesize(out, nil, 0, pd, state.Diff{})

	if contains(out.Headline, "improved") {
		t.Errorf("claimed an improvement over a process that disappeared: %q", out.Headline)
	}
	if len(out.Evidence) != 0 {
		t.Errorf("offered evidence for a claim it did not make: %v", out.Evidence)
	}
}

// A process dying vetoes the whole claim, not only the claim about itself.
// A second process genuinely using less does not license "CPU improved" while
// something substantial is missing from the window: we cannot tell from here
// whether those are two events or one seen twice, and the honest answer to an
// ambiguous window is the answer it gave before this feature existed.
func TestADeadProcessVetoesEvenAnUnrelatedImprovement(t *testing.T) {
	out := &Diagnosis{Triage: improvedCPU(okBlocks(), -95)}
	pd := state.ProcDiff{Rows: []state.ProcRow{
		{Name: "nodedata-linux-", CPUPctA: f(225), CPUPctB: f(11), CPUDelta: f(-95), Verdict: state.PVBetter},
		{Name: "postgres", CPUPctA: f(80), CPUPctB: nil, Verdict: state.PVGone},
	}}

	synthesize(out, nil, 0, pd, state.Diff{})

	if contains(out.Headline, "improved") {
		t.Errorf("a gone process should silence the improvement, got %q", out.Headline)
	}
}

// No snapshots means no process evidence, and no process evidence means no
// claim. This is the common case on a fresh install, and it must read exactly
// as it did before this feature: quiet, not confidently green.
func TestRecoveryNeedsProcessEvidence(t *testing.T) {
	out := &Diagnosis{Triage: improvedCPU(okBlocks(), -95)}

	synthesize(out, nil, 0, state.ProcDiff{}, state.Diff{})

	if contains(out.Headline, "improved") {
		t.Errorf("claimed an improvement with no process data at all: %q", out.Headline)
	}
	if out.Severity != "ok" || !contains(out.Headline, "No regression") {
		t.Errorf("should fall back to the old wording, got %q / %q", out.Severity, out.Headline)
	}
}

// The magnitude gate. A process drifting by a fraction of a core is not the
// explanation for a machine-wide fall, and pairing them would be the same
// unit-blind non-sequitur that once made culpritByCPU name the wrong process.
func TestRecoveryIgnoresATrivialProcessChange(t *testing.T) {
	out := &Diagnosis{Triage: improvedCPU(okBlocks(), -95)}
	pd := state.ProcDiff{Rows: []state.ProcRow{
		{Name: "chronyd", CPUPctA: f(1.2), CPUPctB: f(0.9), CPUDelta: f(-25), Verdict: state.PVBetter},
	}}

	synthesize(out, nil, 0, pd, state.Diff{})

	if contains(out.Headline, "improved") {
		t.Errorf("0.3%% of a core was accepted as the reason for -95%%: %q", out.Headline)
	}
}

// Trouble outranks good news. A window can contain both, and the reader's
// question is still "what is wrong here".
func TestARegressionOutranksAnImprovement(t *testing.T) {
	blocks := improvedCPU(okBlocks(), -95)
	for i := range blocks {
		if blocks[i].Key == "mem" {
			blocks[i].Status, blocks[i].Headline = pcp.TriageBad, "available memory -70%"
		}
	}
	out := &Diagnosis{Triage: blocks}
	pd := state.ProcDiff{Rows: []state.ProcRow{
		{Name: "nodedata-linux-", CPUPctA: f(225), CPUPctB: f(11), CPUDelta: f(-95), Verdict: state.PVBetter},
	}}

	synthesize(out, nil, 0, pd, state.Diff{})

	if out.Severity != "crit" {
		t.Errorf("severity = %q, want crit -- memory is degraded", out.Severity)
	}
	if contains(out.Headline, "improved") {
		t.Errorf("good news took the headline from a degraded resource: %q", out.Headline)
	}
}

// An improvement alongside configuration changes is the most useful pairing
// the tool can report, so neither half may swallow the other: the count has to
// survive into the headline, and it has to stay a correlation.
func TestRecoveryKeepsTheChangeCount(t *testing.T) {
	out := &Diagnosis{Triage: improvedCPU(okBlocks(), -95)}
	pd := state.ProcDiff{Rows: []state.ProcRow{
		{Name: "nodedata-linux-", CPUPctA: f(225), CPUPctB: f(11), CPUDelta: f(-95), Verdict: state.PVBetter},
	}}
	sd := state.Diff{Total: 3}

	synthesize(out, nil, 0, pd, sd)

	if !contains(out.Headline, "improved") || !contains(out.Headline, "3 configuration change") {
		t.Errorf("headline should carry both the improvement and the change count, got %q", out.Headline)
	}
	// "also detected" rather than "caused by": one window cannot establish that.
	if contains(out.Headline, "caused") || contains(out.Headline, "because") {
		t.Errorf("headline asserts causation it cannot know: %q", out.Headline)
	}
}

// Disk and network have no per-process accounting, the same reason synthesize
// names no culprit for them. An unattributable fall in network traffic is
// indistinguishable from a link going down, and a quiet link is not a fast one.
func TestNoRecoveryClaimForDiskOrNetwork(t *testing.T) {
	blocks := okBlocks()
	for i := range blocks {
		if blocks[i].Key == "net" {
			blocks[i].Improved = "interface drops -100%"
			blocks[i].ImprovedPct = f(-100)
		}
	}
	out := &Diagnosis{Triage: blocks}
	pd := state.ProcDiff{Rows: []state.ProcRow{
		{Name: "nodedata-linux-", CPUPctA: f(225), CPUPctB: f(11), CPUDelta: f(-95), Verdict: state.PVBetter},
	}}

	synthesize(out, nil, 0, pd, state.Diff{})

	if contains(out.Headline, "improved") {
		t.Errorf("claimed a network improvement it cannot attribute: %q", out.Headline)
	}
}

// A restart is not a caveat here, it is usually the point -- deploying a fix
// means restarting the thing you fixed. But it has to be stated, because a
// reader correlating this against their own afternoon needs to know a restart
// happened inside the window they are looking at.
func TestRecoveryMentionsARestart(t *testing.T) {
	out := &Diagnosis{Triage: improvedCPU(okBlocks(), -95)}
	pd := state.ProcDiff{Rows: []state.ProcRow{
		{Name: "nodedata-linux-", CPUPctA: f(225), CPUPctB: f(11), CPUDelta: f(-95),
			Verdict: state.PVBetter, Restarted: true, CPUApproxB: true},
	}}

	synthesize(out, nil, 0, pd, state.Diff{})

	ev := strings.Join(out.Evidence, " ")
	if !contains(ev, "restart") {
		t.Errorf("evidence hides the restart: %q", ev)
	}
	// The approximate figure has to be marked as one, the same way culpritByCPU
	// marks it: an unlabelled lifetime average reads as a measured rate.
	if !contains(ev, "~") {
		t.Errorf("an approximate CPU figure was printed as if measured: %q", ev)
	}
}
