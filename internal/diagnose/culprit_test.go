package diagnose

import (
	"testing"

	"github.com/githubflyideas/deltascope/internal/state"
)

func fp(v float64) *float64 { return &v }

// The numbers are from a live host's report. The old scoring added a delta
// percentage to a percent-of-a-core and named gnome-shell -- a process
// using a third as much CPU as firefox -- as responsible.
func TestCulpritIsTheProcessActuallyUsingTheCPU(t *testing.T) {
	rows := []state.ProcRow{
		{Name: "Xorg", CPUPctB: fp(1.7), CPUDelta: fp(307070)},
		{Name: "gnome-shell", CPUPctB: fp(5.3), CPUDelta: fp(14799)},
		{Name: "firefox", CPUPctB: fp(17.3), CPUDelta: fp(6518)},
		{Name: "Isolated Web Co", CPUPctB: fp(4.1), CPUDelta: fp(1304)},
	}
	got := culpritByCPUName(rows)
	t.Logf("culprit: %s", got)
	if got == "" {
		t.Fatal("expected a culprit")
	}
	if got[:7] != "firefox" {
		t.Errorf("the process using 17.3%% of a core must be named, got %q", got)
	}
}

// A spectacular ratio on a tiny process must never outrank real
// consumption. This is the property the old scoring violated.
func TestHugeRatioOnSmallProcessDoesNotWin(t *testing.T) {
	rows := []state.ProcRow{
		{Name: "tiny", CPUPctB: fp(6), CPUDelta: fp(999999)},
		{Name: "real", CPUPctB: fp(180), CPUDelta: fp(25)},
	}
	got := culpritByCPUName(rows)
	if got[:4] != "real" {
		t.Errorf("180%% of a core must outrank 6%% with any ratio, got %q", got)
	}
}

// A newly-hot process should be preferred over an equally-large one that
// has always been that size -- the increase is corroborating evidence.
func TestIncreaseBreaksTiesBetweenEqualConsumers(t *testing.T) {
	rows := []state.ProcRow{
		{Name: "steady", CPUPctB: fp(100), CPUDelta: fp(2)},
		{Name: "newlyhot", CPUPctB: fp(95), CPUDelta: fp(400)},
	}
	got := culpritByCPUName(rows)
	if got[:8] != "newlyhot" {
		t.Errorf("a newly-hot process should win a near-tie, got %q", got)
	}
}

// A row that rose from an idle baseline has no ratio at all. It must still
// receive the same corroboration credit, since FromZero is that evidence
// in the form the data actually took.
func TestFromZeroCountsAsCorroboration(t *testing.T) {
	rows := []state.ProcRow{
		{Name: "steady", CPUPctB: fp(100), CPUDelta: fp(2)},
		{Name: "runaway", CPUPctB: fp(95), FromZero: true},
	}
	got := culpritByCPUName(rows)
	if got[:7] != "runaway" {
		t.Errorf("a process that went from idle to 95%% of a core should win, got %q", got)
	}
	if !containsStr(got, "was idle") {
		t.Errorf("the description must explain the missing percentage, got %q", got)
	}
}

// An approximate figure must be labelled in the headline, not presented as
// a measurement.
func TestApproximateFigureIsMarkedInTheCulpritLine(t *testing.T) {
	rows := []state.ProcRow{
		{Name: "sh", CPUPctB: fp(100), CPUApproxB: true, FromZero: true},
	}
	got := culpritByCPUName(rows)
	if !containsStr(got, "~100%") {
		t.Errorf("a lifetime average must be marked approximate, got %q", got)
	}
}

// Memory: gigabytes of growth must outrank a large percentage on a small
// process, the same unit-blindness the CPU path had.
func TestMemoryCulpritWeightsGrowthByFootprint(t *testing.T) {
	rows := []state.ProcRow{
		{Name: "small", RSSKBB: fp(40 * 1024), RSSDelta: fp(400)},
		{Name: "big", RSSKBB: fp(4 * 1024 * 1024), RSSDelta: fp(40)},
	}
	got := culpritByRSSName(rows)
	if got[:3] != "big" {
		t.Errorf("40%% growth to 4 GB must outrank 400%% growth to 40 MB, got %q", got)
	}
}

// Below the 5%-of-a-core bar there is no culprit worth naming; a report
// that always names someone is a report that is sometimes wrong.
func TestNoCulpritOnAQuietMachine(t *testing.T) {
	rows := []state.ProcRow{
		{Name: "a", CPUPctB: fp(0.4), CPUDelta: fp(900)},
		{Name: "b", CPUPctB: fp(1.1), CPUDelta: fp(4000)},
	}
	if got := culpritByCPUName(rows); got != "" {
		t.Errorf("no process is consuming meaningful CPU; expected no culprit, got %q", got)
	}
}

func containsStr(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
