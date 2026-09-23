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

// A generic command can carry the placeholder itself -- the CPU catalog entry
// ends in `perf top -p <pid>`. Prepending targeted commands used to leave that
// one verbatim, so the reader got a command with a hole in it on the same screen
// that named the process it was asking about.
func TestAGenericCommandsOwnPIDPlaceholderIsFilledToo(t *testing.T) {
	out := &Diagnosis{
		Triage: []pcp.TriageBlock{{Key: "cpu", Label: "CPU", Status: pcp.TriageBad, Headline: "core saturated"}},
		Findings: []pcp.Finding{{Severity: "crit", Conclusion: "cpu",
			Next: []string{"perf top -p <pid>", "mpstat -P ALL 1 5"}}},
	}
	pd := state.ProcDiff{Rows: []state.ProcRow{
		{Name: "sh", PID: 1291929, CPUPctB: fp(99), FromZero: true},
	}}
	synthesize(out, nil, pd, state.Diff{})

	var filled bool
	for _, c := range out.Next {
		if containsStr(c, "<pid>") {
			t.Errorf("placeholder survived into the reader's commands: %q", c)
		}
		if c == "perf top -p 1291929" {
			filled = true
		}
	}
	if !filled {
		t.Errorf("perf was not pointed at the culprit: %v", out.Next)
	}
}

// And with no culprit to name, the placeholder stays. Substituting nothing
// would produce `perf top -p ` -- a command that fails in a way the reader has
// to debug -- and dropping the line would hide the suggestion entirely.
func TestWithoutACulpritThePIDPlaceholderSurvives(t *testing.T) {
	out := &Diagnosis{
		Triage: []pcp.TriageBlock{{Key: "cpu", Label: "CPU", Status: pcp.TriageBad, Headline: "busy"}},
		Findings: []pcp.Finding{{Severity: "crit", Conclusion: "cpu",
			Next: []string{"perf top -p <pid>"}}},
	}
	synthesize(out, nil, state.ProcDiff{}, state.Diff{})
	var seen bool
	for _, c := range out.Next {
		if c == "perf top -p <pid>" {
			seen = true
		}
		if containsStr(c, "-p ") && !containsStr(c, "<pid>") {
			t.Errorf("an empty pid was substituted into %q", c)
		}
	}
	if !seen {
		t.Errorf("the suggestion was dropped instead of left for the reader: %v", out.Next)
	}
}
