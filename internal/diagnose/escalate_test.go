package diagnose

import (
	"testing"

	"github.com/githubflyideas/deltascope/internal/pcp"
	"github.com/githubflyideas/deltascope/internal/reasoning"
)

// fp already defined in culprit_test.go within this package.

// A single pegged core is invisible to the aggregate metric engine, so the
// CPU triage block comes back green. The reasoning layer sees it via the
// per-core derived metrics, and Run must fold that back so the one-click
// page does not show a green CPU light next to a "core saturated" finding.
func TestPerCoreSaturationEscalatesTheCPUBlock(t *testing.T) {
	out := &Diagnosis{
		Triage: []pcp.TriageBlock{
			{Key: "cpu", Label: "CPU", Status: pcp.TriageOK, Headline: "normal"},
			{Key: "mem", Label: "Memory", Status: pcp.TriageOK, Headline: "normal"},
		},
		Reasoning: []reasoning.Result{
			{ID: "diagnosis.single_core_saturated", Severity: "crit",
				Conclusion: "one core saturated"},
		},
	}
	escalateCPUFromReasoning(out)

	var cpu *pcp.TriageBlock
	for i := range out.Triage {
		if out.Triage[i].Key == "cpu" {
			cpu = &out.Triage[i]
		}
	}
	if cpu.Status != pcp.TriageBad {
		t.Errorf("CPU block should be escalated to bad, got %q", cpu.Status)
	}
	if cpu.Headline == "normal" {
		t.Error("CPU headline should be replaced with the per-core reason")
	}
}

// Escalation only ever raises severity. If the metric engine already
// flagged CPU red for its own reasons, a mere warn-level reasoning result
// must not pull it back down to amber.
func TestEscalationNeverLowersSeverity(t *testing.T) {
	out := &Diagnosis{
		Triage: []pcp.TriageBlock{
			{Key: "cpu", Label: "CPU", Status: pcp.TriageBad, Headline: "user CPU +400%"},
		},
		Reasoning: []reasoning.Result{
			{ID: "diagnosis.intermittent_cpu_saturation", Severity: "warn",
				Conclusion: "intermittent"},
		},
	}
	escalateCPUFromReasoning(out)
	if out.Triage[0].Status != pcp.TriageBad {
		t.Errorf("an existing bad status must not be lowered, got %q", out.Triage[0].Status)
	}
	if out.Triage[0].Headline != "user CPU +400%" {
		t.Error("the metric engine's headline must be preserved when it already flagged red")
	}
}

// A reasoning diagnosis the aggregate engine CAN see (e.g. a whole-machine
// diagnosis, not in the per-core map) must not touch the triage block --
// that path belongs to the metric engine and double-counting it would let
// the two disagree in the other direction.
func TestUnlistedReasoningDiagnosisDoesNotEscalate(t *testing.T) {
	out := &Diagnosis{
		Triage: []pcp.TriageBlock{
			{Key: "cpu", Label: "CPU", Status: pcp.TriageOK, Headline: "normal"},
		},
		Reasoning: []reasoning.Result{
			{ID: "diagnosis.vm_cpu_contention", Severity: "crit", Conclusion: "steal"},
		},
	}
	escalateCPUFromReasoning(out)
	if out.Triage[0].Status != pcp.TriageOK {
		t.Error("a diagnosis not in the per-core escalation map must leave triage untouched")
	}
}
