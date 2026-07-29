package state

import (
	"strconv"
	"testing"
	"time"
)

func procSnap(t time.Time, procs map[string]string) Snapshot {
	sec := Section{Name: "processes", Title: "Processes", SkipDiff: true}
	for k, v := range procs {
		sec.Items = append(sec.Items, Item{Key: k, Value: v})
	}
	return Snapshot{Taken: t, Sections: []Section{sec}}
}

// item encodes: cpu_ticks rss_kb start_ticks instances
func item(ticks, rssKB, start uint64, count int) string {
	return strconv.FormatUint(ticks, 10) + " " + strconv.FormatUint(rssKB, 10) + " " +
		strconv.FormatUint(start, 10) + " " + strconv.Itoa(count)
}

func findRow(d ProcDiff, name string) *ProcRow {
	for i := range d.Rows {
		if d.Rows[i].Name == name {
			return &d.Rows[i]
		}
	}
	return nil
}

// The defect: idle yesterday, pegging a core today. pctChange returns nil
// for a zero baseline, worstOf read that as "no signal", the row was
// judged flat, and topProcesses filters flat rows out -- so the clearest
// runaway signature there is never reached the report.
func TestIdleToPeggedCoreIsFlaggedWorse(t *testing.T) {
	base := time.Date(2026, 7, 29, 13, 0, 0, 0, time.UTC)
	yA := base.AddDate(0, 0, -1)
	const tenMin = 10 * time.Minute

	a1 := procSnap(yA, map[string]string{"sh": item(1000, 532, 5000, 1)})
	a2 := procSnap(yA.Add(tenMin), map[string]string{"sh": item(1000, 532, 5000, 1)})
	b1 := procSnap(base, map[string]string{"sh": item(1000, 532, 5000, 1)})
	// 600s of CPU over a 600s interval: a full core
	b2 := procSnap(base.Add(tenMin), map[string]string{"sh": item(1000+60000, 532, 5000, 1)})

	d := CompareProcesses(a1, a2, b1, b2, 20, 1, 10240)
	r := findRow(d, "sh")
	if r == nil {
		t.Fatal("sh row missing")
	}
	if r.CPUPctB == nil || *r.CPUPctB < 99 {
		t.Fatalf("expected ~100%% of a core, got %v", r.CPUPctB)
	}
	if r.Verdict != PVWorse {
		t.Errorf("0%% -> 100%% of a core must be worse, got %q", r.Verdict)
	}
	if !r.FromZero {
		t.Error("the row must be marked FromZero so the UI can explain the empty delta column")
	}
	// The ratio stays absent on purpose: there is no honest percentage
	// for a division by zero, and the value columns already say it.
	if r.CPUDelta != nil {
		t.Errorf("no percentage should be invented for a zero baseline, got %v", *r.CPUDelta)
	}
}

// A small rise from idle is not a finding. Without this, every process
// that ticks over from 0.0 to 2% of a core would be flagged worse, which
// is the noise the dual-significance floor exists to prevent.
func TestSmallRiseFromIdleStaysFlat(t *testing.T) {
	base := time.Date(2026, 7, 29, 13, 0, 0, 0, time.UTC)
	yA := base.AddDate(0, 0, -1)
	const tenMin = 10 * time.Minute

	a1 := procSnap(yA, map[string]string{"cron": item(500, 4000, 100, 1)})
	a2 := procSnap(yA.Add(tenMin), map[string]string{"cron": item(500, 4000, 100, 1)})
	b1 := procSnap(base, map[string]string{"cron": item(500, 4000, 100, 1)})
	// 3s of CPU over 600s = 0.5% of a core
	b2 := procSnap(base.Add(tenMin), map[string]string{"cron": item(800, 4000, 100, 1)})

	d := CompareProcesses(a1, a2, b1, b2, 20, 1, 10240)
	r := findRow(d, "cron")
	if r.Verdict != PVFlat {
		t.Errorf("0.5%% of a core is not a finding, got %q (cpuB=%v)", r.Verdict, r.CPUPctB)
	}
	if r.FromZero {
		t.Error("a sub-floor rise must not be marked FromZero")
	}
}

// Memory has the same shape: a process that allocated nothing and now
// holds a lot must be flagged on absolute evidence.
func TestMemoryBurstFromIdleIsFlagged(t *testing.T) {
	base := time.Date(2026, 7, 29, 13, 0, 0, 0, time.UTC)
	yA := base.AddDate(0, 0, -1)
	const tenMin = 10 * time.Minute

	// RSS 8 MB in A (under the 10 MB floor), 900 MB in B
	a1 := procSnap(yA, map[string]string{"worker": item(100, 8192, 100, 1)})
	a2 := procSnap(yA.Add(tenMin), map[string]string{"worker": item(100, 8192, 100, 1)})
	b1 := procSnap(base, map[string]string{"worker": item(200, 8192, 100, 1)})
	b2 := procSnap(base.Add(tenMin), map[string]string{"worker": item(200, 921600, 100, 1)})

	d := CompareProcesses(a1, a2, b1, b2, 20, 1, 10240)
	r := findRow(d, "worker")
	if r.Verdict != PVWorse {
		t.Errorf("8 MB -> 900 MB must be worse, got %q", r.Verdict)
	}
	if !r.FromZero {
		t.Error("expected FromZero on the memory dimension")
	}
}

// A row judged worse on absolute evidence has no ratio, so ranking by
// ratio magnitude alone would sort the clearest runaway to the bottom of
// its own section -- directly under the reader's eye is where it belongs.
func TestBurstRowSortsAboveSmallRatioChange(t *testing.T) {
	base := time.Date(2026, 7, 29, 13, 0, 0, 0, time.UTC)
	yA := base.AddDate(0, 0, -1)
	const tenMin = 10 * time.Minute

	a1 := procSnap(yA, map[string]string{
		"runaway": item(1000, 20000, 5000, 1),
		"chatty":  item(1000, 20000, 6000, 1),
	})
	a2 := procSnap(yA.Add(tenMin), map[string]string{
		"runaway": item(1000, 20000, 5000, 1), // idle
		"chatty":  item(2800, 20000, 6000, 1), // 3% of a core
	})
	b1 := procSnap(base, map[string]string{
		"runaway": item(1000, 20000, 5000, 1),
		"chatty":  item(5000, 20000, 6000, 1),
	})
	b2 := procSnap(base.Add(tenMin), map[string]string{
		"runaway": item(61000, 20000, 5000, 1), // a full core, from idle
		"chatty":  item(8000, 20000, 6000, 1),  // 5% of a core: +67%
	})

	d := CompareProcesses(a1, a2, b1, b2, 20, 1, 10240)
	if len(d.Rows) < 2 {
		t.Fatalf("expected both rows, got %d", len(d.Rows))
	}
	if d.Rows[0].Name != "runaway" {
		t.Errorf("the process that went from idle to a full core must sort first, got %q first (order: %s, %s)",
			d.Rows[0].Name, d.Rows[0].Name, d.Rows[1].Name)
	}
}

// Guard on the existing behaviour: a process absent from the baseline
// entirely is PVAppeared, a different and more informative verdict than
// "worse", and must not be reclassified by the new absolute path.
func TestTrulyNewProcessStillAppeared(t *testing.T) {
	base := time.Date(2026, 7, 29, 13, 0, 0, 0, time.UTC)
	yA := base.AddDate(0, 0, -1)
	const tenMin = 10 * time.Minute

	a1 := procSnap(yA, map[string]string{"other": item(100, 20000, 100, 1)})
	a2 := procSnap(yA.Add(tenMin), map[string]string{"other": item(100, 20000, 100, 1)})
	b1 := procSnap(base, map[string]string{"other": item(100, 20000, 100, 1), "fresh": item(0, 30000, 900000, 1)})
	b2 := procSnap(base.Add(tenMin), map[string]string{"other": item(100, 20000, 100, 1), "fresh": item(30000, 30000, 900000, 1)})

	d := CompareProcesses(a1, a2, b1, b2, 20, 1, 10240)
	r := findRow(d, "fresh")
	if r == nil {
		t.Fatal("fresh row missing")
	}
	if r.Verdict != PVAppeared {
		t.Errorf("a process absent from the baseline is 'appeared', got %q", r.Verdict)
	}
}
