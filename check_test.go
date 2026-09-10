package main

import (
	"bytes"
	"strings"
	"testing"

	"github.com/githubflyideas/deltascope/internal/reasoning"
)

// The check command's whole reason to exist is that it runs where there is
// no PCP, which means it runs on partial data. These tests pin the two ways
// that could go wrong: reporting a quiet run as healthy when nothing was
// measured, and hiding the not-measured list when it is the largest part of
// the answer.

func TestCheckVerdictNothingMeasuredIsNotOK(t *testing.T) {
	gaps := map[string]string{}
	for _, st := range reasoning.States {
		gaps[st.ID] = "no data for " + st.ID
	}
	sev, headline := checkVerdict(nil, map[string]reasoning.Active{}, gaps)
	if sev != "unknown" {
		t.Errorf("severity = %q, want unknown when every state is a gap", sev)
	}
	if strings.Contains(headline, "none hold") {
		t.Errorf("headline must not read as a clean result, got %q", headline)
	}
}

func TestCheckVerdictQuietButMeasuredIsOK(t *testing.T) {
	sev, headline := checkVerdict(nil, map[string]reasoning.Active{}, map[string]string{
		"state.cpu.core_peaked": "derived.cpu.busiest_core has 4 sample(s), needs 30",
	})
	if sev != "ok" {
		t.Errorf("severity = %q, want ok: states were checked and did not hold", sev)
	}
	// The count must exclude the gap, or the line overstates what was checked.
	want := len(reasoning.States) - 1
	if !strings.Contains(headline, itoa(want)) {
		t.Errorf("headline should say %d states were checked, got %q", want, headline)
	}
}

func TestCheckVerdictPrefersTheRootCause(t *testing.T) {
	results := []reasoning.Result{
		{ID: "diagnosis.consequence", Severity: "crit", Conclusion: "disk is slow", IsRoot: false,
			DownstreamOf: []string{"diagnosis.root"}},
		{ID: "diagnosis.root", Severity: "crit", Conclusion: "memory exhausted, the box is swapping", IsRoot: true},
	}
	_, headline := checkVerdict(results, map[string]reasoning.Active{"state.x": {ID: "state.x"}}, nil)
	if !strings.Contains(headline, "memory exhausted") {
		t.Errorf("the root cause should be the headline, got %q", headline)
	}
}

// A state firing with no diagnosis to land in is a catalog gap, not a clean
// run, and must not be reported at ok.
func TestCheckVerdictStatesWithoutDiagnosisIsWarn(t *testing.T) {
	sev, headline := checkVerdict(nil, map[string]reasoning.Active{
		"state.cpu.context_switch_storm": {ID: "state.cpu.context_switch_storm"},
	}, nil)
	if sev != "warn" {
		t.Errorf("severity = %q, want warn", sev)
	}
	if !strings.Contains(headline, "no diagnosis") {
		t.Errorf("headline should say the states matched no pattern, got %q", headline)
	}
}

func TestNotableCountsKeepsPerInstanceEvents(t *testing.T) {
	got := notableCounts(map[string]float64{
		"mem.vmstat.oom_kill":              1,
		"network.interface.in.drops[eth0]": 42,
		"network.interface.in.drops[eth1]": 0, // nothing happened: not an event
		"kernel.all.pswitch":               900000,
		"network.softnet.time_squeeze":     3,
	})
	if got["mem.vmstat.oom_kill"] != 1 {
		t.Error("an OOM kill must survive to the report: its state's threshold cannot see it")
	}
	if got["network.interface.in.drops[eth0]"] != 42 {
		t.Error("per-instance drop counts must be kept")
	}
	if _, ok := got["network.interface.in.drops[eth1]"]; ok {
		t.Error("a zero count is not an event and would pad the list")
	}
	if _, ok := got["kernel.all.pswitch"]; ok {
		t.Error("context switches are a rate, not an event count; listing them invites misreading")
	}
}

func TestGroupByReasonCollapsesTheCommonCause(t *testing.T) {
	groups := groupByReason(map[string]string{
		"state.a": "no data for mem.vmstat.oom_kill",
		"state.b": "no data for mem.vmstat.oom_kill",
		"state.c": "no data for mem.vmstat.oom_kill",
		"state.d": "kernel.all.load has no baseline",
	})
	if len(groups) != 2 {
		t.Fatalf("groups = %d, want 2", len(groups))
	}
	if len(groups[0].ids) != 3 {
		t.Errorf("the biggest group must come first, got %d ids", len(groups[0].ids))
	}
	if groups[0].ids[0] != "state.a" {
		t.Errorf("ids should be sorted for a stable report, got %v", groups[0].ids)
	}
}

// A short run declines the ten burst states on ten different metrics, which
// is one cause reported ten times. Left ungrouped it is the longest section
// of the report and it pushes the gaps with distinct causes off the screen.
func TestGroupByReasonCollapsesSampleShortfalls(t *testing.T) {
	groups := groupByReason(map[string]string{
		"state.cpu.core_peaked":    "derived.cpu.busiest_core has 10 sample(s), needs 30",
		"state.cpu.bursty":         "kernel.all.cpu.user has 10 sample(s), needs 30",
		"state.net.retrans_burst":  "network.tcp.retranssegs has 10 sample(s), needs 30",
		"state.net.conntrack_full": "no data for network.conntrack.count",
	})
	if len(groups) != 2 {
		t.Fatalf("groups = %d, want 2: the three sample shortfalls are one cause", len(groups))
	}
	if len(groups[0].ids) != 3 {
		t.Fatalf("the collapsed group must lead with 3 ids, got %d", len(groups[0].ids))
	}
	if !strings.Contains(groups[0].reason, "too short") {
		t.Errorf("the group label should name the run length as the cause, got %q", groups[0].reason)
	}
	// Collapsing must not cost the reader the metric: it moves to the state.
	if got := groups[0].detail["state.cpu.core_peaked"]; got != "derived.cpu.busiest_core" {
		t.Errorf("detail = %q, want the metric that was short", got)
	}
}

// A run whose rows disagree about how many samples they carry has two
// different shortfalls, and folding them together would invent a number that
// describes neither.
func TestGroupByReasonKeepsDifferentSampleCountsApart(t *testing.T) {
	groups := groupByReason(map[string]string{
		"state.a": "m1 has 10 sample(s), needs 30",
		"state.b": "m2 has 4 sample(s), needs 30",
	})
	if len(groups) != 2 {
		t.Errorf("groups = %d, want 2: 10-of-30 and 4-of-30 are different facts", len(groups))
	}
}

func TestQuietStatesExcludeFiredAndUnknown(t *testing.T) {
	rep := checkReport{
		Active:      []reasoning.Active{{ID: reasoning.States[0].ID}},
		Unevaluated: map[string]string{reasoning.States[1].ID: "no data"},
	}
	quiet := quietStates(rep)
	if len(quiet) != len(reasoning.States)-2 {
		t.Errorf("quiet = %d, want %d", len(quiet), len(reasoning.States)-2)
	}
	for _, id := range quiet {
		if id == reasoning.States[0].ID {
			t.Error("a fired state is not quiet")
		}
		if id == reasoning.States[1].ID {
			t.Error("an unmeasured state is not quiet")
		}
	}
}

// TestRenderShowsNotMeasuredEvenWhenNothingFired is the anti-false-green test
// for the output itself. A run that found nothing on a host where most
// metrics were missing must not print as a clean bill of health.
func TestRenderShowsNotMeasuredEvenWhenNothingFired(t *testing.T) {
	rep := checkReport{
		Host: "box", Samples: 2, NCPU: 8, Rows: 12,
		Severity: "ok", Headline: "2 state(s) checked and none hold",
		Unevaluated: map[string]string{
			"state.cpu.core_peaked":   "derived.cpu.busiest_core has 1 sample(s), needs 30",
			"state.net.nic_drops":     "no data for network.interface.in.drops",
			"state.mem.available_low": "mem.util.available has no baseline to compare against",
		},
	}
	var buf bytes.Buffer
	renderCheck(&buf, rep, false, false)
	out := buf.String()

	for _, want := range []string{
		"not measured (3)",
		"unknown, not fine",
		"state.cpu.core_peaked",
		"deltascope check -for 60s",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("output is missing %q:\n%s", want, out)
		}
	}
}

func TestRenderShowsEventCountsSeparatelyFromRates(t *testing.T) {
	rep := checkReport{
		Host: "box", Samples: 11, NCPU: 4, Rows: 300,
		Severity: "ok", Headline: "all clear",
		Counts: map[string]float64{"mem.vmstat.oom_kill": 1},
	}
	var buf bytes.Buffer
	renderCheck(&buf, rep, false, false)
	out := buf.String()
	if !strings.Contains(out, "counts, not rates") {
		t.Errorf("the count section must label itself, got:\n%s", out)
	}
	if !strings.Contains(out, "mem.vmstat.oom_kill") || !strings.Contains(out, " 1") {
		t.Errorf("the OOM kill count must be printed, got:\n%s", out)
	}
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b []byte
	for n > 0 {
		b = append([]byte{byte('0' + n%10)}, b...)
		n /= 10
	}
	return string(b)
}
