package httpapi

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/githubflyideas/deltascope/internal/state"
)

// The change page used to answer a question it had not actually asked. Two
// separate silences did it: Compare excludes a section it could only read on
// one side, and the exclusion was never serialised; and NearestBefore quietly
// substitutes the oldest stored snapshot when nothing covers the requested
// time. Either way the reader got a short report that looked like a quiet
// machine. These tests pin both answers into the payload.

func sec(name, title, skipped string, keys ...string) state.Section {
	s := state.Section{Name: name, Title: title, Skipped: skipped}
	for _, k := range keys {
		s.Items = append(s.Items, state.Item{Key: k, Value: "v"})
	}
	return s
}

// A section readable in one capture but not the other is a statement about our
// access, and it must say which side lost it: a baseline taken by the service
// user against a manual root run is a different story from a privilege the
// service used to have and no longer does.
func TestCoverageNamesTheSideThatCouldNotRead(t *testing.T) {
	a := state.Snapshot{Sections: []state.Section{
		sec("firewall", "Firewall", "needs root"),
		sec("system", "System", "", "kernel"),
	}}
	b := state.Snapshot{Sections: []state.Section{
		sec("firewall", "Firewall", "", "rule:1"),
		sec("system", "System", "", "kernel"),
	}}

	cov := coverageJSON(state.Compare(a, b), a, b)
	if cov == nil {
		t.Fatal("a section excluded from the comparison must be reported, not dropped")
	}
	rows, _ := cov["unreadable"].([]map[string]any)
	if len(rows) != 1 {
		t.Fatalf("unreadable = %v, want exactly firewall", cov["unreadable"])
	}
	if rows[0]["section"] != "firewall" || rows[0]["title"] != "Firewall" {
		t.Errorf("row does not identify the section: %v", rows[0])
	}
	if rows[0]["side"] != "a" {
		t.Errorf("side = %v, want a: the baseline is the capture that could not read it", rows[0]["side"])
	}
	if rows[0]["reason"] != "needs root" {
		t.Errorf("reason = %v, want the collector's own words", rows[0]["reason"])
	}
	if cov["skipped"] != nil {
		t.Errorf("a one-sided section is excluded, not never-compared: %v", cov["skipped"])
	}
}

// Unreadable-on-one-side and unreadable-on-both are different claims and must
// not be merged. The first says the comparison was narrowed; the second says a
// whole area has never been under observation at all, which no amount of
// waiting will fix.
func TestCoverageSeparatesNeverComparedFromExcluded(t *testing.T) {
	a := state.Snapshot{Sections: []state.Section{
		sec("firewall", "Firewall", "needs root"),
		sec("security", "Security", "selinux tools not installed"),
	}}
	b := state.Snapshot{Sections: []state.Section{
		sec("firewall", "Firewall", "", "rule:1"),
		sec("security", "Security", "selinux tools not installed"),
	}}

	cov := coverageJSON(state.Compare(a, b), a, b)
	unreadable, _ := cov["unreadable"].([]map[string]any)
	skipped, _ := cov["skipped"].([]map[string]any)
	if len(unreadable) != 1 || unreadable[0]["section"] != "firewall" {
		t.Errorf("unreadable = %v, want firewall alone", cov["unreadable"])
	}
	if len(skipped) != 1 || skipped[0]["section"] != "security" {
		t.Fatalf("skipped = %v, want security alone", cov["skipped"])
	}
	if skipped[0]["reason"] != "selinux tools not installed" {
		t.Errorf("reason = %v, want the collector's own words", skipped[0]["reason"])
	}
	// No side: neither capture read it, so there is no side to blame and the
	// UI must not print one.
	if _, ok := skipped[0]["side"]; ok {
		t.Errorf("a never-compared section has no side: %v", skipped[0])
	}
}

// The absence of a coverage block is itself a claim -- "everything was in
// scope" -- so it has to be reachable. A helper that always returned something
// would put a permanent warning on a healthy host and the reader would stop
// looking at it.
func TestCoverageIsNilWhenNothingWasLeftOut(t *testing.T) {
	a := state.Snapshot{Sections: []state.Section{sec("system", "System", "", "kernel")}}
	b := state.Snapshot{Sections: []state.Section{sec("system", "System", "", "kernel")}}
	if cov := coverageJSON(state.Compare(a, b), a, b); cov != nil {
		t.Errorf("a fully readable pair must report no coverage gap, got %v", cov)
	}
}

// A section that vanished between the two collector versions has no Section
// entry to take a title or reason from. It must still be listed: falling back
// to the bare name is a worse answer than the title, and a much better one
// than dropping the row.
func TestCoverageFallsBackToTheSectionName(t *testing.T) {
	d := state.Diff{Unreadable: []string{"containers"}}
	cov := coverageJSON(d, state.Snapshot{}, state.Snapshot{})
	rows, _ := cov["unreadable"].([]map[string]any)
	if len(rows) != 1 || rows[0]["title"] != "containers" {
		t.Fatalf("want one row titled by its name, got %v", cov["unreadable"])
	}
	if _, ok := rows[0]["reason"]; ok {
		t.Errorf("no section, no reason to invent: %v", rows[0])
	}
}

// The front end reads cov.unreadable / cov.skipped and prints each row's
// reason inline. Pin the wire names so renaming a map key cannot silently
// empty the block on screen while every Go test still passes.
func TestCoverageJSONKeysMatchWhatThePageReads(t *testing.T) {
	a := state.Snapshot{Sections: []state.Section{sec("firewall", "Firewall", "needs root")}}
	b := state.Snapshot{Sections: []state.Section{sec("firewall", "Firewall", "", "rule:1")}}
	blob, err := json.Marshal(coverageJSON(state.Compare(a, b), a, b))
	if err != nil {
		t.Fatal(err)
	}
	var got struct {
		Unreadable []struct {
			Section, Title, Side, Reason string
		} `json:"unreadable"`
		Skipped []struct {
			Section, Title, Reason string
		} `json:"skipped"`
	}
	if err := json.Unmarshal(blob, &got); err != nil {
		t.Fatal(err)
	}
	if len(got.Unreadable) != 1 || got.Unreadable[0].Title == "" || got.Unreadable[0].Reason == "" {
		t.Errorf("payload does not decode into the shape app.js reads: %s", blob)
	}
}

// The tolerance is the whole subtlety. The scheduler probes on a coarse grid,
// so an honest report is always a little newer than the instant asked for;
// flagging that would put a warning on every report and teach the reader to
// ignore it. What must be flagged is the fallback to the oldest row, which
// answers a 24-hour question with whatever history exists.
func TestSubstitutedBaselineToleratesTheProbeGrid(t *testing.T) {
	requested := time.Date(2026, 9, 18, 0, 0, 0, 0, time.UTC)
	iv := state.DefaultSnapshotInterval
	cases := []struct {
		name   string
		actual time.Time
		want   bool
	}{
		{"older than asked for is exactly what NearestBefore promises", requested.Add(-3 * time.Minute), false},
		{"one grid step late is the grid, not a substitution", requested.Add(iv - time.Second), false},
		{"three hours of history against a 24-hour question", requested.Add(21 * time.Hour), true},
	}
	for _, c := range cases {
		if got := substitutedBaseline(requested, c.actual); got != c.want {
			t.Errorf("%s: substituted = %v, want %v", c.name, got, c.want)
		}
	}
}
