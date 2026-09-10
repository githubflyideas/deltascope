package httpapi

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/githubflyideas/deltascope/internal/reasoning"
)

// The reasoning screen has exactly one job these tests protect: a state the
// archive could not answer must not appear as a state that was answered in
// the negative. Everything the UI shows -- the mark, the label, the "N / M
// active" denominator -- is derived from the two fields stateViews sets, so
// getting that mapping wrong turns an unmeasured machine into a clean one on
// screen without any single line of code looking wrong.

var viewCatalog = []reasoning.State{
	{ID: "state.a", Domain: "cpu"},
	{ID: "state.b", Domain: "cpu"},
	{ID: "state.c", Domain: "memory"},
}

func TestStateViewsSeparatesQuietFromUnmeasured(t *testing.T) {
	views := stateViews(viewCatalog,
		map[string]reasoning.Active{"state.a": {ID: "state.a", Evidence: []string{"kernel.all.cpu.user B=0.9"}}},
		map[string]string{"state.c": "no data for mem.util.available"})

	if len(views) != 3 {
		t.Fatalf("views = %d, want one per catalog state", len(views))
	}
	by := map[string]stateView{}
	for _, v := range views {
		by[v.ID] = v
	}

	if !by["state.a"].Active || by["state.a"].Reason != "" {
		t.Errorf("a fired state must be active with no reason, got %+v", by["state.a"])
	}
	// The quiet one is the whole point: it was measured, so it carries neither
	// a reason nor an active flag, and that absence is a real answer.
	if by["state.b"].Active || by["state.b"].Reason != "" {
		t.Errorf("a checked-and-false state must be quiet with no reason, got %+v", by["state.b"])
	}
	if by["state.c"].Active || by["state.c"].Reason == "" {
		t.Errorf("an unmeasured state must carry its reason, got %+v", by["state.c"])
	}
}

// Active and Reason are mutually exclusive by construction. If they were ever
// both set the UI would render the state twice -- once as a finding and once
// as a gap -- and the denominator would count it as unmeasured while the
// diagnosis above cited it as evidence.
func TestStateViewsNeverBothActiveAndUnmeasured(t *testing.T) {
	views := stateViews(viewCatalog,
		map[string]reasoning.Active{"state.a": {ID: "state.a"}},
		// A stale gap entry for a state that fired: reasoning keeps these
		// disjoint, but this layer must not depend on that to stay coherent.
		map[string]string{"state.a": "no data for kernel.all.cpu.user"})

	for _, v := range views {
		if v.Active && v.Reason != "" {
			t.Errorf("%s is both active and unmeasured: %q", v.ID, v.Reason)
		}
	}
}

// The denominator the UI prints is len(states) - gaps. This test pins the
// arithmetic on the shape a PCP-less host actually produces: nothing fired,
// almost nothing was measured.
func TestStateViewsOnAnArchiveThatAnsweredNothing(t *testing.T) {
	gaps := map[string]string{}
	for _, st := range viewCatalog {
		gaps[st.ID] = "no data for " + st.ID
	}
	views := stateViews(viewCatalog, map[string]reasoning.Active{}, gaps)

	measured := 0
	for _, v := range views {
		if v.Reason == "" {
			measured++
		}
	}
	if measured != 0 {
		t.Errorf("measured = %d, want 0: this archive answered nothing", measured)
	}
}

// The JSON contract the front end reads: reason is absent for a measured
// state, so `!s.active && s.reason` is a sound test for "unknown" in JS,
// where an empty string and a missing key are both falsy but a zero-value
// struct field serialised unconditionally would not be.
func TestStateViewJSONOmitsReasonWhenMeasured(t *testing.T) {
	views := stateViews(viewCatalog,
		map[string]reasoning.Active{"state.a": {ID: "state.a"}},
		map[string]string{"state.c": "no data for mem.util.available"})
	blob, err := json.Marshal(views)
	if err != nil {
		t.Fatal(err)
	}
	out := string(blob)
	if strings.Count(out, `"reason"`) != 1 {
		t.Errorf(`want exactly one "reason" key, got: %s`, out)
	}
	if strings.Contains(out, `"reason":""`) {
		t.Errorf("an empty reason must be omitted, not serialised: %s", out)
	}
}

// Guard against the mapping being written against a hard-coded catalog: the
// handler passes reasoning.States, and every one of the 78 must come back
// classified, or states silently vanish from the screen.
func TestStateViewsCoversTheWholeCatalog(t *testing.T) {
	views := stateViews(reasoning.States, map[string]reasoning.Active{}, map[string]string{})
	if len(views) != len(reasoning.States) {
		t.Fatalf("views = %d, want %d", len(views), len(reasoning.States))
	}
	seen := map[string]bool{}
	for _, v := range views {
		if seen[v.ID] {
			t.Errorf("%s appears twice", v.ID)
		}
		seen[v.ID] = true
		if v.Domain == "" {
			t.Errorf("%s has no domain, so the UI cannot group it", v.ID)
		}
	}
}
