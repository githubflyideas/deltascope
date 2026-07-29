package diagnose

import (
	"testing"
	"time"

	"github.com/githubflyideas/deltascope/internal/pcp"
	"github.com/githubflyideas/deltascope/internal/state"
)

func f(v float64) *float64 { return &v }

// TestSynthesizeCorrelation is the core of the chain: given three engines'
// output, does it name the right resource, the right process, and the
// right configuration change in one sentence?
func TestSynthesizeCorrelation(t *testing.T) {
	out := &Diagnosis{
		Triage: []pcp.TriageBlock{
			{Key: "cpu", Label: "CPU", Status: pcp.TriageBad, Headline: "user CPU +2733%"},
			{Key: "mem", Label: "Memory", Status: pcp.TriageOK, Headline: "normal"},
			{Key: "disk", Label: "Disk", Status: pcp.TriageOK, Headline: "normal"},
			{Key: "net", Label: "Network", Status: pcp.TriageOK, Headline: "normal"},
		},
	}
	pd := state.ProcDiff{Rows: []state.ProcRow{
		{Name: "mysqld", CPUPctA: f(4), CPUPctB: f(310), CPUDelta: f(7650), Verdict: state.PVWorse},
		{Name: "nginx", CPUPctA: f(2), CPUPctB: f(3), CPUDelta: f(50), Verdict: state.PVFlat},
		{Name: "sshd", CPUPctA: f(0.1), CPUPctB: f(0.1), Verdict: state.PVFlat},
	}}
	sd := state.Diff{Sections: []state.SectionDiff{{
		Name: "sysctl", Title: "Kernel Parameters",
		Changes: []state.Change{
			{Section: "sysctl", Key: "net.core.somaxconn", Kind: state.Modified, Old: "4096", New: "128"},
			{Section: "sysctl", Key: "kernel.sched_migration_cost_ns", Kind: state.Modified, Old: "500000", New: "5000000"},
		},
	}}, Total: 2}

	synthesize(out, nil, pd, sd)

	if out.Severity != "crit" {
		t.Errorf("severity = %q, want crit", out.Severity)
	}
	if out.Headline == "" || !contains(out.Headline, "CPU") {
		t.Errorf("headline should name the degraded resource, got %q", out.Headline)
	}
	if !contains(out.Culprit, "mysqld") {
		t.Errorf("culprit should be mysqld (310%% of a core), got %q", out.Culprit)
	}
	// CPU trouble should prefer the scheduler tunable over somaxconn
	if !contains(out.Changed, "sched") {
		t.Errorf("changed should prefer the CPU-related change, got %q", out.Changed)
	}
	t.Logf("headline: %s", out.Headline)
	t.Logf("culprit : %s", out.Culprit)
	t.Logf("changed : %s", out.Changed)
}

// A rule-engine conclusion must outrank a bare resource signal, since the
// rule already knows what the combination of signals means.
func TestFindingOutranksTriage(t *testing.T) {
	out := &Diagnosis{
		Findings: []pcp.Finding{{
			ID: "swap-spiral", Severity: "crit",
			Conclusion: "Memory pressure has triggered swapping",
			Evidence:   []string{"swap.pagesout"}, Next: []string{"free -m"},
		}},
		Triage: []pcp.TriageBlock{
			{Key: "cpu", Label: "CPU", Status: pcp.TriageBad, Headline: "user CPU +200%"},
		},
	}
	synthesize(out, nil, state.ProcDiff{}, state.Diff{})
	if !contains(out.Headline, "swapping") {
		t.Errorf("rule conclusion should win the headline, got %q", out.Headline)
	}
	if len(out.Next) == 0 {
		t.Error("next steps from the rule should be carried through")
	}
}

// Memory trouble must be attributed by RSS growth, not CPU.
func TestMemoryCulpritByRSS(t *testing.T) {
	out := &Diagnosis{Triage: []pcp.TriageBlock{
		{Key: "mem", Label: "Memory", Status: pcp.TriageBad, Headline: "available memory -70%"},
	}}
	pd := state.ProcDiff{Rows: []state.ProcRow{
		{Name: "java", CPUPctA: f(50), CPUPctB: f(52), RSSKBA: f(500000), RSSKBB: f(520000), RSSDelta: f(4), Verdict: state.PVFlat},
		{Name: "nginx", RSSKBA: f(102400), RSSKBB: f(3800000), RSSDelta: f(3611), Verdict: state.PVWorse},
	}}
	synthesize(out, nil, pd, state.Diff{})
	if !contains(out.Culprit, "nginx") {
		t.Errorf("memory culprit should be nginx (+3611%%), got %q", out.Culprit)
	}
	t.Logf("culprit: %s", out.Culprit)
}

// A healthy machine must say so plainly, and must not invent a culprit.
func TestHealthyMachine(t *testing.T) {
	out := &Diagnosis{Triage: []pcp.TriageBlock{
		{Key: "cpu", Label: "CPU", Status: pcp.TriageOK, Headline: "normal"},
		{Key: "mem", Label: "Memory", Status: pcp.TriageOK, Headline: "normal"},
	}}
	synthesize(out, nil, state.ProcDiff{}, state.Diff{})
	if out.Severity != "ok" {
		t.Errorf("severity = %q, want ok", out.Severity)
	}
	if out.Culprit != "" {
		t.Errorf("a healthy machine must not name a culprit, got %q", out.Culprit)
	}
	t.Logf("headline: %s", out.Headline)
}

// Config changes with no performance regression are worth reporting, but
// only as info -- nothing is actually wrong yet.
func TestChangesOnlyIsInfo(t *testing.T) {
	out := &Diagnosis{Triage: []pcp.TriageBlock{
		{Key: "cpu", Label: "CPU", Status: pcp.TriageOK, Headline: "normal"},
	}}
	sd := state.Diff{Sections: []state.SectionDiff{{
		Name: "security", Title: "Security Posture",
		Changes: []state.Change{{Key: "net.ipv4.ip_forward", Kind: state.Modified, Old: "0", New: "1"}},
	}}, Total: 1}
	synthesize(out, nil, state.ProcDiff{}, sd)
	if out.Severity != "info" {
		t.Errorf("severity = %q, want info", out.Severity)
	}
	if !contains(out.Changed, "ip_forward") {
		t.Errorf("changed should name the change, got %q", out.Changed)
	}
	t.Logf("headline: %s / changed: %s", out.Headline, out.Changed)
}

func TestPickWindowIsHourAnchored(t *testing.T) {
	now := time.Date(2026, 7, 25, 14, 37, 0, 0, time.UTC)
	w := PickWindow(now)
	if w.BEnd.Minute() != 0 || w.BStart.Minute() != 0 {
		t.Errorf("window should be hour-anchored, got %v..%v", w.BStart, w.BEnd)
	}
	if w.BEnd.Sub(w.BStart) != time.Hour {
		t.Errorf("compare window should be one hour, got %v", w.BEnd.Sub(w.BStart))
	}
	if w.AStart.AddDate(0, 0, 1) != w.BStart {
		t.Errorf("baseline should be exactly one day earlier: %v vs %v", w.AStart, w.BStart)
	}
}

func contains(s, sub string) bool {
	return len(s) >= len(sub) && (s == sub || indexOf(s, sub) >= 0)
}

func indexOf(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}
