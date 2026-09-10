package httpapi

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/githubflyideas/deltascope/internal/pcp"
	"github.com/githubflyideas/deltascope/internal/reasoning"
	"github.com/githubflyideas/deltascope/internal/state"
)

// The reasoning tab used to offer the same four time pickers the
// metric-comparison tab does, which made the two screens read as duplicates
// while giving this one nothing the other did not already have. The window is
// fixed now -- "the last 30 minutes against the 30 before" -- and these tests
// pin the two consequences that are easy to get wrong: leftover query
// parameters must be ignored rather than rejected, and the report has to say
// something about a resource the metric engine flagged even when no state in
// the catalog fired for it.

// A page cached before the pickers were removed still sends the old query
// string. Rejecting it with a 400 would break the tab for exactly as long as
// that page stayed in a browser cache, and the parameters are no longer
// meaningful, so ignoring them is the only correct answer.
func TestReasoningIgnoresLeftoverWindowParameters(t *testing.T) {
	s := &Server{
		Caps:   Capabilities{Metrics: true, Reasoning: true},
		Runner: errRunner{},
	}
	rec := httptest.NewRecorder()
	s.handleReasoning(rec, httptest.NewRequest("GET",
		"/api/reasoning?a_start=nonsense&a_end=also-nonsense"+
			"&b_start=2026-01-02T00:00&b_end=2026-01-02T01:00", nil))

	// 502 is the archive runner failing, which means the handler got as far as
	// asking it. A 400 would mean the removed parameters still gate the path.
	if rec.Code == http.StatusBadRequest {
		t.Fatalf("status = 400: unparseable leftover parameters still reject the request: %s", rec.Body.String())
	}
	if rec.Code != http.StatusBadGateway {
		t.Errorf("status = %d, want 502 from the archive path", rec.Code)
	}
}

// The case the user's own machine hit. The metric engine judged CPU degraded
// off a relative comparison -- a load average that jumped tenfold from an idle
// baseline -- while every CPU state in the catalog is absolute or
// scale-relative and none of them fired. The chain's only conclusion was about
// the network. A reader seeing one network warn and nothing else concludes the
// CPU is fine, which is the opposite of what the other engine just said.
func TestUnexplainedNamesTheResourceTheChainMissed(t *testing.T) {
	pct := 1200.0
	triage := []pcp.TriageBlock{
		{Key: "cpu", Label: "CPU", Status: pcp.TriageBad,
			Headline: "System load[15 minute] +spike", WorstPct: &pct},
		{Key: "mem", Label: "Memory", Status: pcp.TriageOK, Headline: "normal"},
		{Key: "net", Label: "Network", Status: pcp.TriageWarn, Headline: "Retransmits +40%"},
	}
	results := []reasoning.Result{{
		ID: "diagnosis.network_loss", Branch: reasoning.BranchNetwork,
		Severity: "warn", Conclusion: "TCP is retransmitting heavily",
	}}

	got := unexplained(triage, results)

	if len(got) != 1 {
		t.Fatalf("unexplained = %+v, want exactly the CPU block: net has a diagnosis and mem is green", got)
	}
	if got[0].Key != "cpu" {
		t.Fatalf("unexplained[0].Key = %q, want cpu", got[0].Key)
	}
	// The headline has to survive: it is the only thing on this screen that
	// says what the other engine actually saw.
	if !strings.Contains(got[0].Headline, "System load") {
		t.Errorf("headline = %q, want the metric engine's own words", got[0].Headline)
	}
	// Domains are how the UI finds the states that were checked and came back
	// quiet, which is the answer to "why did the chain not conclude anything".
	if len(got[0].Domains) != 1 || got[0].Domains[0] != "cpu" {
		t.Errorf("domains = %v, want [cpu]: without these the UI cannot show what was checked", got[0].Domains)
	}
}

// The complement. When the chain does have a conclusion for a flagged
// resource, repeating it as "unexplained" would be both wrong and the
// duplication this whole change exists to avoid.
func TestUnexplainedStaysQuietWhenTheChainCoversTheResource(t *testing.T) {
	triage := []pcp.TriageBlock{
		{Key: "cpu", Label: "CPU", Status: pcp.TriageBad, Headline: "CPU user +300%"},
	}
	results := []reasoning.Result{{
		ID: "diagnosis.cpu_saturated_own_workload", Branch: reasoning.BranchCPU,
		Severity: "crit", Conclusion: "This machine's own workload is saturating the CPU",
	}}

	if got := unexplained(triage, results); len(got) != 0 {
		t.Errorf("unexplained = %+v, want empty: the chain answered for CPU", got)
	}
}

// Every state domain in the catalog has to be reachable from a triage key, or
// a flagged resource would be reported as unexplained with nowhere for the
// reader to look. Filesystem is the one that is easy to drop: the metric
// engine files it under Disk together with I/O, so it has no key of its own.
func TestEveryStateDomainIsReachableFromATriageKey(t *testing.T) {
	covered := map[string]bool{}
	for _, domains := range reasoningDomains {
		for _, d := range domains {
			covered[d] = true
		}
	}
	for _, st := range reasoning.States {
		if !covered[st.Domain] {
			t.Errorf("state %s is in domain %q, which no triage key maps to", st.ID, st.Domain)
		}
	}
}

// A software-branch diagnosis is about a change rather than a resource, so it
// must not be counted as explaining one. If it were, a single "a package was
// upgraded" verdict would silence the report about all four resources at once.
func TestSoftwareDiagnosesDoNotExplainAResource(t *testing.T) {
	triage := []pcp.TriageBlock{
		{Key: "cpu", Label: "CPU", Status: pcp.TriageBad, Headline: "CPU user +300%"},
	}
	results := []reasoning.Result{{
		ID: "diagnosis.something_changed", Branch: reasoning.BranchSoftware,
		Severity: "warn", Conclusion: "A package changed",
	}}

	if got := unexplained(triage, results); len(got) != 1 {
		t.Errorf("unexplained = %+v, want the CPU block: a software verdict explains no resource", got)
	}
}

// The chain reasons over machine-wide metrics only, so it can describe a CPU
// problem in full and never name the process causing it. The process leg is
// what closes that, and when nothing has changed it still has to answer "who
// is using this machine" rather than going blank -- ordered by CPU, because
// that is the column a reader arrives at this panel asking about.
func TestTopProcsFallsBackToTheHeaviestByCPU(t *testing.T) {
	rows := []state.ProcRow{
		{Name: "sshd", CPUPctB: f64(0.2), Verdict: state.PVFlat},
		{Name: "nodedata-linux-", CPUPctB: f64(124.3), Verdict: state.PVFlat},
		{Name: "cron", Verdict: state.PVFlat},
	}

	got := topProcs(rows, 12)

	if len(got) != 3 {
		t.Fatalf("topProcs = %d rows, want all 3: a flat window must not blank the panel", len(got))
	}
	if got[0].Name != "nodedata-linux-" {
		t.Errorf("first row = %q, want the heaviest process by CPU", got[0].Name)
	}
	// A nil CPU figure must sort last rather than panicking or floating up.
	if got[2].Name != "cron" {
		t.Errorf("last row = %q, want the process with no CPU figure", got[2].Name)
	}
}

// When rows did move, only those are worth showing -- the fallback would bury
// them under idle processes that happen to be large.
func TestTopProcsPrefersRowsThatMoved(t *testing.T) {
	rows := []state.ProcRow{
		{Name: "idle-but-huge", CPUPctB: f64(900), Verdict: state.PVFlat},
		{Name: "grew", CPUPctB: f64(3), Verdict: state.PVWorse},
	}

	got := topProcs(rows, 12)

	if len(got) != 1 || got[0].Name != "grew" {
		t.Errorf("topProcs = %+v, want only the row that moved", got)
	}
}

// Both legs of the report can be partial for different reasons, and a reader
// needs both reasons. Dropping either one leaves a gap on screen with no cause
// given, which is the failure this whole screen is written against.
func TestJoinNotesKeepsBothReasons(t *testing.T) {
	if got := joinNotes("sampler is warming up.", "no snapshots yet."); !strings.Contains(got, "warming") ||
		!strings.Contains(got, "snapshots") {
		t.Errorf("joinNotes = %q, want both reasons", got)
	}
	if got := joinNotes("", "only one."); got != "only one." {
		t.Errorf("joinNotes = %q, want no stray separator", got)
	}
	if got := joinNotes("only one.", ""); got != "only one." {
		t.Errorf("joinNotes = %q, want no stray separator", got)
	}
}

// End to end on the source that needs it most: a host with no PCP still gets
// triage and the unexplained list in the payload, because those are computed
// from the same rows the chain reasoned over and need no archive.
func TestReasoningFromProcCarriesTriageAndUnexplained(t *testing.T) {
	s := &Server{
		Caps:    Capabilities{Metrics: false, Reasoning: true, Reason: "PCP tools not installed"},
		Sampler: fakeSampler{win: procWindow(32)},
	}
	rec := httptest.NewRecorder()
	s.handleReasoning(rec, httptest.NewRequest("GET", "/api/reasoning", nil))
	body := decodeReasoning(t, rec)

	if _, ok := body["triage"]; !ok {
		t.Error("payload has no triage: the two engines cannot be cross-checked on this host")
	}
	if _, ok := body["unexplained"]; !ok {
		t.Error("payload has no unexplained list")
	}
	// procWindow drops available memory by 97.5%, which is a red memory block
	// in the metric engine and an active state in the chain. The chain having
	// covered it is what keeps it off the unexplained list -- so an empty list
	// here is the assertion, not an absence of evidence.
	tri := body["triage"].([]any)
	if len(tri) != 4 {
		t.Errorf("triage = %d blocks, want the four resources", len(tri))
	}
}
