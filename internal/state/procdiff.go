package state

import (
	"math"
	"sort"
	"strconv"
	"time"
)

// ProcVerdict is the conclusion for one process row.
type ProcVerdict string

const (
	PVWorse    ProcVerdict = "worse"
	PVBetter   ProcVerdict = "better"
	PVFlat     ProcVerdict = "flat"
	PVAppeared ProcVerdict = "appeared"
	PVGone     ProcVerdict = "gone"
)

// ProcRow is one process's change between two snapshots.
type ProcRow struct {
	Name string `json:"name"`
	// CPUPctA/B are percent of one core, derived from the cumulative tick
	// delta across the interval between the two snapshots.
	CPUPctA   *float64    `json:"cpu_pct_a"`
	CPUPctB   *float64    `json:"cpu_pct_b"`
	CPUDelta  *float64    `json:"cpu_delta_pct"`
	RSSKBA    *float64    `json:"rss_kb_a"`
	RSSKBB    *float64    `json:"rss_kb_b"`
	RSSDelta  *float64    `json:"rss_delta_pct"`
	Verdict   ProcVerdict `json:"verdict"`
	Restarted bool        `json:"restarted"`
	Instances int         `json:"instances"`
	// CPUApproxB marks CPUPctB as a lifetime average over a process born
	// inside the window, rather than an exact rate between two readings.
	// It is the honest figure available for a process with no baseline
	// reading, and the UI must label it so nobody reads it as measured.
	CPUApproxB bool `json:"cpu_approx_b,omitempty"`
	// FromZero marks a row judged worse on absolute evidence because it
	// rose from an idle baseline, where no percentage change exists. The
	// UI and the JSON export need this to explain why a row with an empty
	// delta column is nonetheless flagged; without it the report looks
	// like it flagged a row for no reason.
	FromZero bool `json:"from_zero,omitempty"`
}

// ProcDiff is the full process comparison.
type ProcDiff struct {
	// The four snapshots involved: CPU rate needs a pair of snapshots per
	// window, since a rate requires two cumulative readings.
	AStart   time.Time `json:"a_start"`
	AEnd     time.Time `json:"a_end"`
	BStart   time.Time `json:"b_start"`
	BEnd     time.Time `json:"b_end"`
	Rows     []ProcRow `json:"rows"`
	Restarts []ProcRow `json:"restarts"`
	Note     string    `json:"note,omitempty"`
}

const clockTicksPerSec = 100 // USER_HZ, fixed at 100 on all mainstream Linux

// cpuPercent converts a cumulative tick delta over an elapsed wall
// interval into percent of a single core.
func cpuPercent(ticksDelta uint64, elapsed time.Duration) float64 {
	if elapsed <= 0 {
		return 0
	}
	return float64(ticksDelta) / clockTicksPerSec / elapsed.Seconds() * 100
}

// procSection pulls the process section out of a snapshot.
func procSection(s Snapshot) map[string]Item {
	for _, sec := range s.Sections {
		if sec.Name == "processes" {
			return itemMap(sec)
		}
	}
	return nil
}

// procUptime reads the host uptime recorded alongside the process section.
// Absent in snapshots taken before that field existed, which is why every
// caller must handle !ok rather than treating 0 as a valid reading.
func procUptime(s Snapshot) (float64, bool) {
	for _, sec := range s.Sections {
		if sec.Name != "processes" {
			continue
		}
		v, ok := sec.Meta["uptime_sec"]
		if !ok {
			return 0, false
		}
		f, err := strconv.ParseFloat(v, 64)
		if err != nil || f <= 0 {
			return 0, false
		}
		return f, true
	}
	return 0, false
}

// startedAgo converts a process start tick into how long before the
// snapshot the process began. Both quantities live on the same monotonic
// boot timeline, so this is unaffected by the wall clock being stepped.
func startedAgo(startTicks uint64, uptimeSec float64) time.Duration {
	age := uptimeSec - float64(startTicks)/clockTicksPerSec
	if age < 0 {
		// Cannot happen on a consistent pair of readings; if it does the
		// snapshot is self-inconsistent and we decline to guess.
		return 0
	}
	return time.Duration(age * float64(time.Second))
}

// CompareProcesses computes per-process CPU rate and memory for two
// windows, each defined by a pair of snapshots (a rate needs two
// cumulative readings). Window A is a1->a2, window B is b1->b2.
func CompareProcesses(a1, a2, b1, b2 Snapshot, thresholdPct, minCPUPct, minRSSKB float64) ProcDiff {
	d := ProcDiff{
		AStart: a1.Taken, AEnd: a2.Taken,
		BStart: b1.Taken, BEnd: b2.Taken,
	}

	pa1, pa2 := procSection(a1), procSection(a2)
	pb1, pb2 := procSection(b1), procSection(b2)
	if pa2 == nil || pb2 == nil {
		d.Note = "no process data in these snapshots"
		return d
	}

	elapsedA := a2.Taken.Sub(a1.Taken)
	elapsedB := b2.Taken.Sub(b1.Taken)

	// Uptime at the end of each window, used to date a process that did not
	// exist at the start of it. Absent on older snapshots, in which case the
	// lifetime-bounded path is simply unavailable.
	upA, hasUpA := procUptime(a2)
	upB, hasUpB := procUptime(b2)

	// rate computes a window's CPU percent and end-of-window RSS.
	// rate computes a window's CPU percent and end-of-window RSS.
	//
	// Two readings of the same process give an exact rate over the interval,
	// and that is always preferred. When they are unavailable -- the process
	// did not exist at the start of the window, or the name's aggregate
	// lifetime shifted because a transient instance came or went -- the
	// process's own lifetime provides a second, weaker basis: total ticks
	// consumed over the time it has actually been alive. That is a lifetime
	// average rather than a windowed rate, so it is reported as approximate.
	//
	// Returning nothing in those cases, which is what this did before, has a
	// specific consequence: culpritByCPU skips rows with no CPU figure, so a
	// process that started mid-window and immediately pegged a core could not
	// be named as the culprit no matter how much CPU it burned. A runaway
	// process is *more* likely to be newly started, not less.
	rate := func(first, second map[string]Item, name string, elapsed time.Duration,
		uptime float64, hasUptime bool) (cpu, rss *float64, start uint64, inst int, approx, present bool) {
		i2, ok2 := second[name]
		if !ok2 {
			return nil, nil, 0, 0, false, false
		}
		t2, r2, s2, c2, ok := DecodeProcItem(i2.Value)
		if !ok {
			return nil, nil, 0, 0, false, false
		}
		rssv := float64(r2)
		rss = &rssv
		start, inst, present = s2, c2, true

		if first != nil {
			if i1, ok1 := first[name]; ok1 {
				if t1, _, s1, _, ok := DecodeProcItem(i1.Value); ok && s1 == s2 && t2 >= t1 {
					// same process lifetime and monotonic ticks: a valid rate
					c := cpuPercent(t2-t1, elapsed)
					return &c, rss, start, inst, false, present
				}
			}
		}

		// Fall back to the lifetime average, bounded by the window. A
		// long-lived process's whole-life average says nothing about what it
		// did in the last hour; only a process born inside the window has a
		// lifetime short enough for the average to describe the window at all.
		if hasUptime && s2 > 0 {
			if age := startedAgo(s2, uptime); age > 0 && age <= elapsed {
				c := cpuPercent(t2, age)
				return &c, rss, start, inst, true, present
			}
		}
		return nil, rss, start, inst, false, present
	}

	names := map[string]bool{}
	for n := range pa2 {
		names[n] = true
	}
	for n := range pb2 {
		names[n] = true
	}

	for name := range names {
		cpuA, rssA, startA, _, _, inA := rate(pa1, pa2, name, elapsedA, upA, hasUpA)
		cpuB, rssB, startB, instB, approxB, inB := rate(pb1, pb2, name, elapsedB, upB, hasUpB)

		row := ProcRow{
			Name:       name,
			CPUPctA:    cpuA,
			CPUPctB:    cpuB,
			RSSKBA:     rssA,
			RSSKBB:     rssB,
			Instances:  instB,
			CPUApproxB: approxB,
		}
		if inA && inB && startA > 0 && startB > startA {
			row.Restarted = true
		}

		switch {
		case !inA && inB:
			row.Verdict = PVAppeared
		case inA && !inB:
			row.Verdict = PVGone
		default:
			row.CPUDelta = pctChange(cpuA, cpuB, minCPUPct)
			row.RSSDelta = pctChange(rssA, rssB, minRSSKB)
			cpuM := measure{delta: row.CPUDelta, a: cpuA, b: cpuB, minAbs: minCPUPct, burstAt: cpuBurstFloorPct}
			rssM := measure{delta: row.RSSDelta, a: rssA, b: rssB, minAbs: minRSSKB, burstAt: rssBurstFloorKB}
			row.Verdict = worstOf(cpuM, rssM, thresholdPct)
			row.FromZero = emergedFromZero(cpuM) || emergedFromZero(rssM)
		}
		d.Rows = append(d.Rows, row)
		if row.Restarted {
			d.Restarts = append(d.Restarts, row)
		}
	}

	sortProcRows(d.Rows)
	sort.Slice(d.Restarts, func(i, j int) bool { return d.Restarts[i].Name < d.Restarts[j].Name })
	return d
}

// pctChange applies the same two-bar rule as the metric engine: a change
// must be both relatively large and absolutely meaningful. Below the
// absolute floor it returns nil, meaning "not a signal".
func pctChange(a, b *float64, minAbs float64) *float64 {
	if a == nil || b == nil {
		return nil
	}
	if math.Abs(*a) < minAbs && math.Abs(*b) < minAbs {
		return nil
	}
	if *a == 0 {
		if *b == 0 {
			z := 0.0
			return &z
		}
		return nil // 0 -> nonzero: infinite, caller treats nil as no ratio
	}
	d := (*b - *a) / math.Abs(*a) * 100
	return &d
}

// Burst floors are the absolute levels at which a rise from an idle
// baseline is a finding in its own right.
//
// They sit well above the significance floors that gate the ratio path,
// and that gap is deliberate. A ratio carries two pieces of evidence --
// the relative size of the change AND the absolute level -- while a rise
// from zero carries only the second, so it has to be stronger to earn the
// same verdict. 25% of a core sustained across a window does not happen by
// accident; 1% does.
const (
	cpuBurstFloorPct = 25     // percent of one core
	rssBurstFloorKB  = 262144 // 256 MB
)

// measure is one dimension of a process row with everything needed to
// judge it: the ratio if one could be computed, the raw values, and the
// two floors.
type measure struct {
	delta   *float64
	a, b    *float64
	minAbs  float64
	burstAt float64
}

// worstOf reduces a row's dimensions to a single verdict.
//
// The subtle case is a nil ratio. pctChange returns nil when the baseline
// is zero, because the percentage change from zero is infinite and there
// is no honest number to report. Treating that as "no signal" was wrong in
// the one direction that matters most: a process that consumed no CPU in
// the baseline window and a full core in the compare window -- the
// cleanest runaway signature there is, and the exact case the README
// advertises -- was judged flat and then filtered out of the report.
//
// So a nil ratio is no longer the end of the judgement. If the baseline
// was effectively idle and the current value clears the burst floor, the
// row is worse on absolute evidence alone. The ratio stays nil, because
// inventing a percentage for a division by zero would be worse than
// showing none; the two value columns already read "0.0% -> 100.0%".
func worstOf(cpu, rss measure, threshold float64) ProcVerdict {
	worst := PVFlat
	consider := func(m measure) {
		if m.delta != nil {
			if *m.delta >= threshold {
				worst = PVWorse
			} else if *m.delta <= -threshold && worst != PVWorse {
				worst = PVBetter
			}
			return
		}
		if emergedFromZero(m) {
			worst = PVWorse
		}
	}
	consider(cpu)
	consider(rss)
	return worst
}

// emergedFromZero reports whether this dimension went from an effectively
// idle baseline to a level notable on its own terms. Both sides must be
// known: a missing baseline is a different situation (PVAppeared) and is
// classified before this is reached.
func emergedFromZero(m measure) bool {
	if m.a == nil || m.b == nil || m.burstAt <= 0 {
		return false
	}
	// "Effectively idle" rather than exactly zero: a process that used a
	// few milliseconds of CPU in an hour is idle for every purpose that
	// matters here, and requiring an exact 0.0 would make the verdict
	// depend on whether a sampling tick happened to land.
	if *m.a >= m.minAbs {
		return false
	}
	return *m.b >= m.burstAt
}

func sortProcRows(rows []ProcRow) {
	rank := map[ProcVerdict]int{PVWorse: 0, PVAppeared: 1, PVGone: 2, PVBetter: 3, PVFlat: 4}
	sort.Slice(rows, func(i, j int) bool {
		ri, rj := rank[rows[i].Verdict], rank[rows[j].Verdict]
		if ri != rj {
			return ri < rj
		}
		wi, wj := rowMagnitude(rows[i]), rowMagnitude(rows[j])
		if wi != wj {
			return wi > wj
		}
		return rows[i].Name < rows[j].Name
	})
}

// rowMagnitude orders rows that share a verdict. Rows judged worse on
// absolute evidence have no ratio at all, and ranking them by a magnitude
// of zero would sort the clearest runaway to the bottom of its own
// section -- so a burst is scored by how far past its floor it went,
// expressed as a ratio so it stays comparable with a percentage change.
func rowMagnitude(r ProcRow) float64 {
	m := 0.0
	if r.CPUDelta != nil && math.Abs(*r.CPUDelta) > m {
		m = math.Abs(*r.CPUDelta)
	}
	if r.RSSDelta != nil && math.Abs(*r.RSSDelta) > m {
		m = math.Abs(*r.RSSDelta)
	}
	if r.CPUDelta == nil && r.CPUPctB != nil && *r.CPUPctB >= cpuBurstFloorPct {
		if v := *r.CPUPctB / cpuBurstFloorPct * 100; v > m {
			m = v
		}
	}
	if r.RSSDelta == nil && r.RSSKBB != nil && *r.RSSKBB >= rssBurstFloorKB {
		if v := *r.RSSKBB / rssBurstFloorKB * 100; v > m {
			m = v
		}
	}
	return m
}
