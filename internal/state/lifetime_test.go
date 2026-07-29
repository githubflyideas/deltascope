package state

import (
	"testing"
	"time"
)

// snapWithUptime builds a process snapshot carrying the host uptime that
// lets a start tick be dated.
func snapWithUptime(t time.Time, uptimeSec string, procs map[string]string) Snapshot {
	s := procSnap(t, procs)
	s.Sections[0].Meta = map[string]string{"uptime_sec": uptimeSec}
	return s
}

// The defect: a process that started inside window B has no reading at
// BStart, so no rate could be computed, so culpritByCPU skipped it -- the
// busiest process on the machine could not be named as the cause.
func TestProcessBornMidWindowGetsARate(t *testing.T) {
	bStart := time.Date(2026, 7, 29, 13, 0, 0, 0, time.UTC)
	bEnd := bStart.Add(time.Hour)
	aStart := bStart.AddDate(0, 0, -1)

	// Host has been up 100000s at BStart, 103600s at BEnd.
	a1 := snapWithUptime(aStart, "13600.00", map[string]string{"firefox": item(100000, 736000, 100, 1)})
	a2 := snapWithUptime(aStart.Add(time.Hour), "17200.00", map[string]string{"firefox": item(110000, 736000, 100, 1)})
	b1 := snapWithUptime(bStart, "100000.00", map[string]string{"firefox": item(200000, 736000, 100, 1)})
	// sh started at boot-tick 10120000 (= 101200s of uptime), i.e. 2400s
	// before BEnd, and has consumed 2400s of CPU: a full core.
	b2 := snapWithUptime(bEnd, "103600.00", map[string]string{
		"firefox": item(206000, 735000, 100, 1),
		"sh":      item(240000, 532, 10120000, 1),
	})

	d := CompareProcesses(a1, a2, b1, b2, 20, 1, 10240)
	r := findRow(d, "sh")
	if r == nil {
		t.Fatal("sh row missing")
	}
	if r.CPUPctB == nil {
		t.Fatal("a process born mid-window must still get a CPU figure")
	}
	t.Logf("sh cpuB=%.1f%% approx=%v verdict=%s", *r.CPUPctB, r.CPUApproxB, r.Verdict)
	if *r.CPUPctB < 95 || *r.CPUPctB > 105 {
		t.Errorf("expected ~100%% of a core over its 2400s lifetime, got %.1f", *r.CPUPctB)
	}
	if !r.CPUApproxB {
		t.Error("a lifetime average must be marked approximate, not presented as a measured rate")
	}
}

// A long-lived process must NOT get a lifetime average: its whole-life
// figure says nothing about the window being examined, and substituting
// one for the other would be a fabricated answer rather than a missing one.
func TestLongLivedProcessGetsNoLifetimeAverage(t *testing.T) {
	bStart := time.Date(2026, 7, 29, 13, 0, 0, 0, time.UTC)
	bEnd := bStart.Add(time.Hour)

	// daemon started 90000s ago -- far outside the 1h window. Its aggregate
	// start tick differs between the two readings (a transient instance
	// came and went), so the exact-rate path is unavailable.
	b1 := snapWithUptime(bStart, "100000.00", map[string]string{"daemon": item(500000, 40000, 1000000, 1)})
	b2 := snapWithUptime(bEnd, "103600.00", map[string]string{"daemon": item(506000, 40000, 1200000, 1)})
	a1 := snapWithUptime(bStart.AddDate(0, 0, -1), "13600.00", map[string]string{"daemon": item(100000, 40000, 1000000, 1)})
	a2 := snapWithUptime(bStart.AddDate(0, 0, -1).Add(time.Hour), "17200.00", map[string]string{"daemon": item(106000, 40000, 1000000, 1)})

	d := CompareProcesses(a1, a2, b1, b2, 20, 1, 10240)
	r := findRow(d, "daemon")
	if r == nil {
		t.Fatal("daemon row missing")
	}
	if r.CPUPctB != nil {
		t.Errorf("a process alive far longer than the window must not report a lifetime average, got %.2f",
			*r.CPUPctB)
	}
}

// Snapshots written before the uptime field existed must keep working:
// the exact-rate path is unaffected and the fallback is simply
// unavailable, rather than producing a wrong number from a zero uptime.
func TestOlderSnapshotsWithoutUptimeStillWork(t *testing.T) {
	bStart := time.Date(2026, 7, 29, 13, 0, 0, 0, time.UTC)
	bEnd := bStart.Add(time.Hour)

	// no Meta at all
	b1 := procSnap(bStart, map[string]string{"app": item(100000, 40000, 1000, 1)})
	b2 := procSnap(bEnd, map[string]string{
		"app": item(136000, 40000, 1000, 1),
		"new": item(120000, 5000, 9999999, 1),
	})
	a1 := procSnap(bStart.AddDate(0, 0, -1), map[string]string{"app": item(10000, 40000, 1000, 1)})
	a2 := procSnap(bStart.AddDate(0, 0, -1).Add(time.Hour), map[string]string{"app": item(46000, 40000, 1000, 1)})

	d := CompareProcesses(a1, a2, b1, b2, 20, 1, 10240)
	if r := findRow(d, "app"); r == nil || r.CPUPctB == nil {
		t.Fatal("the exact-rate path must be unaffected by a missing uptime")
	}
	r := findRow(d, "new")
	if r == nil {
		t.Fatal("new row missing")
	}
	if r.CPUPctB != nil {
		t.Errorf("without a recorded uptime there is no basis for a rate; got %v", *r.CPUPctB)
	}
}

// The other half of the defect: a name whose aggregate start tick shifts
// between readings (a transient second instance) lost its rate entirely,
// even though the busy instance was still there burning CPU. If that
// process was born inside the window, the lifetime path now covers it.
func TestAggregateStartShiftFallsBackToLifetime(t *testing.T) {
	bStart := time.Date(2026, 7, 29, 13, 0, 0, 0, time.UTC)
	bEnd := bStart.Add(time.Hour)

	// sh appears at BStart having just started, and at BEnd the aggregate
	// earliest-start has moved because the original instance exited.
	b1 := snapWithUptime(bStart, "100000.00", map[string]string{"sh": item(1000, 532, 9990000, 1)})
	b2 := snapWithUptime(bEnd, "103600.00", map[string]string{"sh": item(180000, 900, 10180000, 1)})
	a1 := snapWithUptime(bStart.AddDate(0, 0, -1), "13600.00", map[string]string{"other": item(100, 20000, 100, 1)})
	a2 := snapWithUptime(bStart.AddDate(0, 0, -1).Add(time.Hour), "17200.00", map[string]string{"other": item(100, 20000, 100, 1)})

	d := CompareProcesses(a1, a2, b1, b2, 20, 1, 10240)
	r := findRow(d, "sh")
	if r == nil {
		t.Fatal("sh row missing")
	}
	if r.CPUPctB == nil {
		t.Fatal("a shifted aggregate lifetime must not discard the CPU figure outright")
	}
	t.Logf("sh cpuB=%.1f%% approx=%v", *r.CPUPctB, r.CPUApproxB)
	if !r.CPUApproxB {
		t.Error("expected the figure to be marked approximate")
	}
}

// A self-inconsistent snapshot pair (start tick later than uptime) must
// produce no figure rather than a negative or absurd one.
func TestInconsistentUptimeProducesNoRate(t *testing.T) {
	bStart := time.Date(2026, 7, 29, 13, 0, 0, 0, time.UTC)
	bEnd := bStart.Add(time.Hour)

	// start tick claims the process began after the host booted... later
	// than the host's own uptime.
	b1 := snapWithUptime(bStart, "1000.00", map[string]string{})
	b2 := snapWithUptime(bEnd, "1000.00", map[string]string{"weird": item(5000, 1000, 900000, 1)})
	a1 := snapWithUptime(bStart.AddDate(0, 0, -1), "500.00", map[string]string{})
	a2 := snapWithUptime(bStart.AddDate(0, 0, -1).Add(time.Hour), "500.00", map[string]string{})

	d := CompareProcesses(a1, a2, b1, b2, 20, 1, 10240)
	if r := findRow(d, "weird"); r != nil && r.CPUPctB != nil {
		t.Errorf("an inconsistent reading must not yield a rate, got %v", *r.CPUPctB)
	}
}
