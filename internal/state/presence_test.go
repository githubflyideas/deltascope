package state

import (
	"testing"
	"time"
)

// A trivial transient helper appearing (goa-identity class: a few MB, no
// CPU, lives briefly) must NOT show as "appeared" -- that is the desktop
// churn the field reports flooded with. It falls through to flat and is
// filtered from the report.
func TestTrivialTransientAppearanceIsFlat(t *testing.T) {
	base := time.Date(2026, 8, 1, 7, 0, 0, 0, time.UTC)
	yA := base.AddDate(0, 0, -1)
	const tenMin = 10 * time.Minute

	a1 := procSnap(yA, map[string]string{"other": item(100, 20000, 100, 1)})
	a2 := procSnap(yA.Add(tenMin), map[string]string{"other": item(100, 20000, 100, 1)})
	// goa-identity-service: appears in B, 9 MB RSS, ~0 CPU
	b1 := procSnap(base, map[string]string{"other": item(100, 20000, 100, 1)})
	b2 := procSnap(base.Add(tenMin), map[string]string{
		"other":        item(100, 20000, 100, 1),
		"goa-identity": item(2, 9216, 900000, 1), // 9 MB, negligible CPU
	})

	d := CompareProcesses(a1, a2, b1, b2, 20, 1, 10240)
	r := findRow(d, "goa-identity")
	if r == nil {
		t.Fatal("row missing")
	}
	if r.Verdict != PVFlat {
		t.Errorf("a 9 MB, no-CPU transient appearing is churn, must be flat, got %q", r.Verdict)
	}
}

// The disappearance direction: a small helper that was there and is gone
// must also be flat, not "gone".
func TestTrivialTransientDisappearanceIsFlat(t *testing.T) {
	base := time.Date(2026, 8, 1, 7, 0, 0, 0, time.UTC)
	yA := base.AddDate(0, 0, -1)
	const tenMin = 10 * time.Minute

	// present yesterday, small; gone today
	a1 := procSnap(yA, map[string]string{"other": item(100, 20000, 100, 1), "gsd-power": item(5, 30000, 50, 1)})
	a2 := procSnap(yA.Add(tenMin), map[string]string{"other": item(100, 20000, 100, 1), "gsd-power": item(6, 30000, 50, 1)})
	b1 := procSnap(base, map[string]string{"other": item(100, 20000, 100, 1)})
	b2 := procSnap(base.Add(tenMin), map[string]string{"other": item(100, 20000, 100, 1)})

	d := CompareProcesses(a1, a2, b1, b2, 20, 1, 10240)
	r := findRow(d, "gsd-power")
	if r == nil {
		t.Fatal("row missing")
	}
	if r.Verdict != PVFlat {
		t.Errorf("a 30 MB, no-CPU helper disappearing is churn, must be flat, got %q", r.Verdict)
	}
}

// The signal we keep: a heavy process (a full core, or hundreds of MB)
// appearing or vanishing is a real event and must still surface.
func TestSubstantialAppearanceStillShows(t *testing.T) {
	base := time.Date(2026, 8, 1, 7, 0, 0, 0, time.UTC)
	yA := base.AddDate(0, 0, -1)
	const tenMin = 10 * time.Minute

	a1 := procSnap(yA, map[string]string{"other": item(100, 20000, 100, 1)})
	a2 := procSnap(yA.Add(tenMin), map[string]string{"other": item(100, 20000, 100, 1)})
	// a process pegging most of a core appears
	b1 := procSnap(base, map[string]string{"other": item(100, 20000, 100, 1), "batchjob": item(0, 40000, 5000, 1)})
	b2 := procSnap(base.Add(tenMin), map[string]string{
		"other":    item(100, 20000, 100, 1),
		"batchjob": item(45000, 40000, 5000, 1), // +45000 ticks / 600s = 75% of a core
	})

	d := CompareProcesses(a1, a2, b1, b2, 20, 1, 10240)
	r := findRow(d, "batchjob")
	if r == nil || r.Verdict != PVAppeared {
		t.Fatalf("a process appearing at 75%% of a core is a real event; got %v", r)
	}
}
