package diagnose

import (
	"strconv"
	"testing"

	"github.com/githubflyideas/deltascope/internal/pcp"
	"github.com/githubflyideas/deltascope/internal/state"
)

// The pidstat-mismatch scenario: a busy-loop `sh` pegs a core; the noise it
// makes (a terminal scrolling, kworkers) shows as a context-switch storm.
// The answer must (1) name sh as the culprit and (2) point its next-step
// command at sh's PID, not at the generic "pidstat -w" that chases context
// switches -- a different process entirely.
func TestNextStepTargetsCulpritPID(t *testing.T) {
	out := &Diagnosis{
		Triage: []pcp.TriageBlock{
			{Key: "cpu", Label: "CPU", Status: pcp.TriageBad, Headline: "core saturated"},
		},
	}
	pd := state.ProcDiff{Rows: []state.ProcRow{
		{Name: "sh", PID: 1291929, CPUPctB: fp(99), FromZero: true},
	}}
	synthesize(out, nil, pd, state.Diff{})

	if !containsStr(out.Culprit, "sh") {
		t.Fatalf("culprit should be sh, got %q", out.Culprit)
	}
	pidStr := strconv.Itoa(1291929)
	var targetsPID bool
	for _, c := range out.Next {
		if containsStr(c, pidStr) {
			targetsPID = true
		}
		if containsStr(c, "pidstat -w") {
			t.Errorf("next-step must not be the context-switch command that misleads to the wrong process: %q", c)
		}
	}
	if !targetsPID {
		t.Errorf("a next-step command must target the culprit PID %s; got %v", pidStr, out.Next)
	}
	t.Logf("culprit=%q next=%v", out.Culprit, out.Next)
}

// When there is no PID (older snapshot, or gone process), fall back to the
// diagnosis's own commands rather than emitting a broken "-p 0".
func TestNoPIDFallsBackToGenericCommands(t *testing.T) {
	out := &Diagnosis{
		Triage:   []pcp.TriageBlock{{Key: "cpu", Label: "CPU", Status: pcp.TriageBad, Headline: "busy"}},
		Findings: []pcp.Finding{{Severity: "crit", Conclusion: "cpu", Next: []string{"pidstat 1 5"}}},
	}
	pd := state.ProcDiff{Rows: []state.ProcRow{{Name: "sh", PID: 0, CPUPctB: fp(99), FromZero: true}}}
	synthesize(out, nil, pd, state.Diff{})
	for _, c := range out.Next {
		if containsStr(c, "-p 0") {
			t.Errorf("must not emit a command with pid 0: %q", c)
		}
	}
}
