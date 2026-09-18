package state

import (
	"errors"
	"sort"
	"testing"
	"time"
)

// The timeline is tested against a hand-built history rather than a database:
// what is under test is the bisect and the grouping, and a fixture makes the
// interesting cases -- an unreadable probe, a schema boundary, a flapping value
// -- constructible instead of hypothetical.

var base = time.Date(2026, 3, 4, 12, 0, 0, 0, time.UTC)

func at(step int) time.Time { return base.Add(time.Duration(step) * 10 * time.Minute) }

// hsnap builds a snapshot from section -> key -> value. Items are sorted the
// way Capture sorts them, so a fixture cannot accidentally depend on map order.
func hsnap(taken time.Time, secs map[string]map[string]string) Snapshot {
	s := Snapshot{Host: "h", Taken: taken}
	names := make([]string, 0, len(secs))
	for n := range secs {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		sec := Section{Name: n, Title: n}
		for k, v := range secs[n] {
			sec.Items = append(sec.Items, Item{Key: k, Value: v})
		}
		sort.Slice(sec.Items, func(i, j int) bool { return sec.Items[i].Key < sec.Items[j].Key })
		s.Sections = append(s.Sections, sec)
	}
	return s
}

// sysctls is the common shape: one section, plain key/value parameters.
func sysctls(taken time.Time, kv map[string]string) Snapshot {
	return hsnap(taken, map[string]map[string]string{"sysctl": kv})
}

// history is a fixture SnapshotSource. Row ids are 1-based indexes into snaps,
// which makes an intentionally broken probe easy to name.
type history struct {
	snaps []Snapshot
	bad   map[int]bool // 1-based ids whose body fails to load
	loads int          // bodies actually read, so the budget can be asserted
	err   error        // returned from Stamps, to exercise the degraded path
}

func (h *history) Stamps(from, to time.Time) ([]Stamp, error) {
	if h.err != nil {
		return nil, h.err
	}
	var out []Stamp
	for i, s := range h.snaps {
		if s.Taken.After(from) && s.Taken.Before(to) {
			out = append(out, Stamp{ID: int64(i + 1), Taken: s.Taken})
		}
	}
	return out, nil
}

func (h *history) ByID(id int64) (Snapshot, error) {
	h.loads++
	if h.bad[int(id)] {
		return Snapshot{}, errors.New("corrupt body")
	}
	if id < 1 || int(id) > len(h.snaps) {
		return Snapshot{}, errors.New("no such snapshot")
	}
	return h.snaps[id-1], nil
}

// seen counts how many events reported each change, keyed section+key. Every
// test asserts on this: whatever the dating does, a change must be reported
// exactly once, because the page shows events instead of the flat diff.
func seen(evs []Event) map[string]int {
	m := map[string]int{}
	for _, ev := range evs {
		for _, ch := range ev.Changes {
			m[ch.Section+"/"+ch.Key]++
		}
	}
	return m
}

func expectOnce(t *testing.T, evs []Event, keys ...string) {
	t.Helper()
	got := seen(evs)
	for _, k := range keys {
		if got[k] != 1 {
			t.Errorf("%s reported %d times, want exactly 1", k, got[k])
		}
	}
	if len(got) != len(keys) {
		t.Errorf("reported %d distinct changes, want %d: %v", len(got), len(keys), got)
	}
}

// TestNilSource: the graceful degradation to one undated event.
func TestLocateNilSource(t *testing.T) {
	a := sysctls(at(0), map[string]string{"a": "1"})
	b := sysctls(at(6), map[string]string{"a": "2"})
	d := Compare(a, b)
	evs := Locate(nil, d)
	if len(evs) != 1 {
		t.Fatalf("want 1 undated event, got %d", len(evs))
	}
	if evs[0].Dated {
		t.Error("expected Dated false")
	}
	expectOnce(t, evs, "sysctl/a")
}

// TestNoInteriorProbes: from and to with nothing in between.
func TestLocateNoProbes(t *testing.T) {
	a := sysctls(at(0), map[string]string{"a": "1"})
	b := sysctls(at(1), map[string]string{"a": "2"})
	h := &history{snaps: []Snapshot{a, b}}
	d := Compare(a, b)
	evs := Locate(h, d)
	if len(evs) != 1 {
		t.Fatalf("want 1 event, got %d", len(evs))
	}
	if evs[0].Dated {
		t.Error("expected Dated false with no interior probes")
	}
}

// TestTwoChangesDifferentTimes: the core grouping case. Two parameters that
// changed at different times must land in different events.
func TestTwoChangesDifferentTimes(t *testing.T) {
	// a changed between step 2 and 3, b changed between step 4 and 5.
	a := sysctls(at(0), map[string]string{"a": "old", "b": "old"})
	s1 := sysctls(at(1), map[string]string{"a": "old", "b": "old"})
	s2 := sysctls(at(2), map[string]string{"a": "old", "b": "old"})
	s3 := sysctls(at(3), map[string]string{"a": "new", "b": "old"})
	s4 := sysctls(at(4), map[string]string{"a": "new", "b": "old"})
	s5 := sysctls(at(5), map[string]string{"a": "new", "b": "new"})
	b := sysctls(at(6), map[string]string{"a": "new", "b": "new"})
	h := &history{snaps: []Snapshot{a, s1, s2, s3, s4, s5, b}}
	d := Compare(a, b)
	evs := Locate(h, d)
	if len(evs) != 2 {
		t.Fatalf("want 2 events, got %d", len(evs))
	}
	expectOnce(t, evs, "sysctl/a", "sysctl/b")
	// Both events must be dated and the newer one (b) should sort first.
	for _, ev := range evs {
		if !ev.Dated {
			t.Errorf("expected Dated for event %+v", ev)
		}
	}
}

// TestSameBucketGrouping: two changes that happened in the same 10-minute
// interval must be grouped into one event.
func TestSameBucketGrouping(t *testing.T) {
	a := sysctls(at(0), map[string]string{"a": "1", "b": "1"})
	s1 := sysctls(at(1), map[string]string{"a": "1", "b": "1"})
	s2 := sysctls(at(2), map[string]string{"a": "2", "b": "2"}) // both changed here
	b := sysctls(at(3), map[string]string{"a": "2", "b": "2"})
	h := &history{snaps: []Snapshot{a, s1, s2, b}}
	d := Compare(a, b)
	evs := Locate(h, d)
	if len(evs) != 1 {
		t.Fatalf("want 1 grouped event, got %d", len(evs))
	}
	if len(evs[0].Changes) != 2 {
		t.Fatalf("want 2 changes in the event, got %d", len(evs[0].Changes))
	}
	if !evs[0].Dated {
		t.Error("expected Dated")
	}
}

// TestAddedRemoved: added and removed changes follow the correct predicate.
func TestAddedRemoved(t *testing.T) {
	a := sysctls(at(0), map[string]string{"gone": "x"})
	s1 := sysctls(at(1), map[string]string{"gone": "x"})
	s2 := sysctls(at(2), map[string]string{"new": "y"})
	b := sysctls(at(3), map[string]string{"new": "y"})
	h := &history{snaps: []Snapshot{a, s1, s2, b}}
	d := Compare(a, b)
	evs := Locate(h, d)
	expectOnce(t, evs, "sysctl/gone", "sysctl/new")
}

// kernelUpgrade builds the fan-out a kernel upgrade produces: the version in
// system, the package, a module that came with it, and a /proc/sys node the new
// kernel added. All of it is one event, and the point of Class is to say so.
func kernelUpgrade(taken time.Time, ver string, done bool) Snapshot {
	secs := map[string]map[string]string{
		"system":   {"kernel": ver, "hostname": "h"},
		"packages": {"kernel-core": ver + "-1", "bash": "5.2-3"},
		"sysctl":   {"vm.swappiness": "60"},
		"modules":  {"nf_tables": "loaded"},
	}
	if done {
		secs["sysctl"]["kernel.new_knob"] = "1"
		secs["modules"]["intel_uncore_frequency"] = "loaded"
	}
	return hsnap(taken, secs)
}

func TestKernelUpgradeIsOneEvent(t *testing.T) {
	a := kernelUpgrade(at(0), "6.1.0", false)
	s1 := kernelUpgrade(at(1), "6.1.0", false)
	s2 := kernelUpgrade(at(2), "6.6.0", true)
	s3 := kernelUpgrade(at(3), "6.6.0", true)
	b := kernelUpgrade(at(4), "6.6.0", true)
	h := &history{snaps: []Snapshot{a, s1, s2, s3, b}}
	d := Compare(a, b)
	if d.Total != 4 {
		t.Fatalf("fixture should produce 4 changes, got %d", d.Total)
	}
	evs := Locate(h, d)
	if len(evs) != 1 {
		t.Fatalf("a kernel upgrade is one event, got %d: %+v", len(evs), evs)
	}
	ev := evs[0]
	if ev.Class != "kernel" {
		t.Errorf("class = %q, want kernel", ev.Class)
	}
	if ev.Subject != "6.6.0" {
		t.Errorf("subject = %q, want the new kernel version", ev.Subject)
	}
	if !ev.Dated || !ev.From.Equal(at(1)) || !ev.To.Equal(at(2)) {
		t.Errorf("dated %s..%s (dated=%v), want %s..%s", ev.From, ev.To, ev.Dated, at(1), at(2))
	}
	expectOnce(t, evs,
		"system/kernel", "packages/kernel-core",
		"sysctl/kernel.new_knob", "modules/intel_uncore_frequency")
}

// A probe written by a different collector version keys its items differently,
// so its presence or absence of a key says nothing about ours. All of them
// unusable means no dating at all -- which must be reported as undated, not as
// a confident answer built on nothing.
func TestProbesFromAnotherSchemaVersionAreUnusable(t *testing.T) {
	mk := func(step int, v string, schema int) Snapshot {
		s := sysctls(at(step), map[string]string{"a": v})
		s.Schema = schema
		return s
	}
	a, b := mk(0, "1", 3), mk(6, "2", 3)
	h := &history{}
	for i := 1; i <= 5; i++ {
		v := "1"
		if i >= 3 {
			v = "2"
		}
		h.snaps = append(h.snaps, mk(i, v, 2))
	}
	evs := Locate(h, Compare(a, b))
	if len(evs) != 1 {
		t.Fatalf("want 1 event, got %d", len(evs))
	}
	if evs[0].Dated {
		t.Error("no usable probe means undated, not dated")
	}
	if !evs[0].From.Equal(at(0)) || !evs[0].To.Equal(at(6)) {
		t.Errorf("undated event should span the whole window, got %s..%s", evs[0].From, evs[0].To)
	}
	expectOnce(t, evs, "sysctl/a")
	if h.loads > maxProbeAttempts {
		t.Errorf("gave up after %d loads, want <= %d", h.loads, maxProbeAttempts)
	}
}

// One unreadable snapshot in the middle of the history must cost precision, not
// the dating: the bisect steps to the next probe and reports the wider interval
// it could actually verify.
func TestUnreadableProbeWidensRatherThanBlocks(t *testing.T) {
	a := sysctls(at(0), map[string]string{"a": "1"})
	b := sysctls(at(6), map[string]string{"a": "2"})
	h := &history{bad: map[int]bool{3: true}} // id 3 == the probe at step 2
	for i := 1; i <= 5; i++ {
		v := "1"
		if i >= 4 {
			v = "2"
		}
		h.snaps = append(h.snaps, sysctls(at(i), map[string]string{"a": v}))
	}
	evs := Locate(h, Compare(a, b))
	if len(evs) != 1 {
		t.Fatalf("want 1 event, got %d", len(evs))
	}
	if !evs[0].Dated {
		t.Fatal("a readable neighbour still dates the change")
	}
	// The bad probe is at step 3 (the midpoint), so the bisect tries step 2
	// next. Step 2 still has value "1", so the transition is between step 2
	// and step 4 -- wider than the "step 3..4" a perfect midpoint would give,
	// but still narrower than the full window.
	if !evs[0].From.Equal(at(2)) || !evs[0].To.Equal(at(4)) {
		t.Errorf("dated %s..%s, want %s..%s", evs[0].From, evs[0].To, at(2), at(4))
	}
}

// A value that moved more than once in the window is flapping, not one change.
// The bisect cannot see that on its own -- every answer it gets is consistent
// with a single transition -- so the evidence comes from probes loaded for OTHER
// changes. Here y and z bracket the window with their own changes, and the
// probes they force show x back at its old value long after it had supposedly
// settled.
func TestFlappingValue(t *testing.T) {
	// x: new new new old new  (flaps -- reverted, then set again)
	// y: old new new new new  (one change, early)
	// z: old old old old new  (one change, late)
	vals := []struct{ x, y, z string }{
		{"new", "old", "old"},
		{"new", "new", "old"},
		{"new", "new", "old"},
		{"old", "new", "old"},
		{"new", "new", "new"},
	}
	a := sysctls(at(0), map[string]string{"x": "old", "y": "old", "z": "old"})
	b := sysctls(at(6), map[string]string{"x": "new", "y": "new", "z": "new"})
	h := &history{snaps: []Snapshot{a}}
	for i, v := range vals {
		h.snaps = append(h.snaps, sysctls(at(i+1),
			map[string]string{"x": v.x, "y": v.y, "z": v.z}))
	}
	h.snaps = append(h.snaps, b)
	evs := Locate(h, Compare(a, b))
	expectOnce(t, evs, "sysctl/x", "sysctl/y", "sysctl/z")
	var flagged []string
	for _, ev := range evs {
		if ev.Unstable {
			for _, ch := range ev.Changes {
				flagged = append(flagged, ch.Key)
			}
		}
	}
	if len(flagged) != 1 || flagged[0] != "x" {
		t.Errorf("want only x flagged unstable, got %v (events %+v)", flagged, evs)
	}
}

// The source errors out: degrade to one undated event.
func TestLocateSourceError(t *testing.T) {
	a := sysctls(at(0), map[string]string{"a": "1"})
	b := sysctls(at(3), map[string]string{"a": "2"})
	h := &history{snaps: []Snapshot{a, b}, err: errors.New("db locked")}
	evs := Locate(h, Compare(a, b))
	if len(evs) != 1 {
		t.Fatalf("want 1 event, got %d", len(evs))
	}
	if evs[0].Dated {
		t.Error("a source error means undated")
	}
}

// A section that was skipped (no permissions) at probe time is unusable for the
// changes it covers. Its changes must still be reported.
func TestSkippedSectionInProbe(t *testing.T) {
	mk := func(step int, val string, skipped bool) Snapshot {
		s := Snapshot{Host: "h", Taken: at(step), Schema: SchemaVersion}
		sec := Section{Name: "firewall", Title: "Firewall"}
		if skipped {
			sec.Skipped = "root required"
		} else {
			sec.Items = []Item{{Key: "rules.hash", Value: val}}
		}
		s.Sections = []Section{sec}
		return s
	}
	a := mk(0, "abc", false)
	b := mk(6, "def", false)
	h := &history{}
	for i := 1; i <= 5; i++ {
		// probe at step 3 lost privilege, the others saw the change
		if i == 3 {
			h.snaps = append(h.snaps, mk(i, "", true))
		} else {
			v := "abc"
			if i >= 4 {
				v = "def"
			}
			h.snaps = append(h.snaps, mk(i, v, false))
		}
	}
	evs := Locate(h, Compare(a, b))
	if len(evs) != 1 {
		t.Fatalf("want 1 event, got %d", len(evs))
	}
	if !evs[0].Dated {
		t.Error("the neighbouring probes should still date it")
	}
	expectOnce(t, evs, "firewall/rules.hash")
}

// An empty diff produces no events.
func TestEmptyDiff(t *testing.T) {
	a := sysctls(at(0), map[string]string{"a": "1"})
	d := Compare(a, a)
	if evs := Locate(nil, d); evs != nil {
		t.Fatalf("empty diff should produce nil events, got %d", len(evs))
	}
}

// Multiple events sorted newest-first.
func TestNewestFirst(t *testing.T) {
	a := sysctls(at(0), map[string]string{"a": "old", "b": "old"})
	s1 := sysctls(at(1), map[string]string{"a": "new", "b": "old"})
	s2 := sysctls(at(2), map[string]string{"a": "new", "b": "new"})
	b := sysctls(at(3), map[string]string{"a": "new", "b": "new"})
	h := &history{snaps: []Snapshot{a, s1, s2, b}}
	evs := Locate(h, Compare(a, b))
	if len(evs) != 2 {
		t.Fatalf("want 2, got %d", len(evs))
	}
	if evs[0].To.Before(evs[1].To) {
		t.Errorf("newest should be first: %s before %s", evs[0].To, evs[1].To)
	}
}
