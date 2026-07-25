package state

import (
	"math"
	"sort"
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
	CPUPctA   *float64 `json:"cpu_pct_a"`
	CPUPctB   *float64 `json:"cpu_pct_b"`
	CPUDelta  *float64 `json:"cpu_delta_pct"`
	RSSKBA    *float64 `json:"rss_kb_a"`
	RSSKBB    *float64 `json:"rss_kb_b"`
	RSSDelta  *float64 `json:"rss_delta_pct"`
	Verdict   ProcVerdict `json:"verdict"`
	Restarted bool     `json:"restarted"`
	Instances int      `json:"instances"`
}

// ProcDiff is the full process comparison.
type ProcDiff struct {
	// The four snapshots involved: CPU rate needs a pair of snapshots per
	// window, since a rate requires two cumulative readings.
	AStart, AEnd time.Time `json:"a_start,a_end"`
	BStart, BEnd time.Time `json:"b_start,b_end"`
	Rows         []ProcRow `json:"rows"`
	Restarts     []ProcRow `json:"restarts"`
	Note         string    `json:"note,omitempty"`
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

	// rate computes a window's CPU percent and end-of-window RSS.
	rate := func(first, second map[string]Item, name string, elapsed time.Duration) (cpu, rss *float64, start uint64, inst int, present bool) {
		i2, ok2 := second[name]
		if !ok2 {
			return nil, nil, 0, 0, false
		}
		t2, r2, s2, c2, ok := DecodeProcItem(i2.Value)
		if !ok {
			return nil, nil, 0, 0, false
		}
		rssv := float64(r2)
		rss = &rssv
		start, inst, present = s2, c2, true

		if first != nil {
			if i1, ok1 := first[name]; ok1 {
				if t1, _, s1, _, ok := DecodeProcItem(i1.Value); ok && s1 == s2 && t2 >= t1 {
					// same process lifetime and monotonic ticks: a valid rate
					c := cpuPercent(t2-t1, elapsed)
					cpu = &c
				}
			}
		}
		return cpu, rss, start, inst, present
	}

	names := map[string]bool{}
	for n := range pa2 {
		names[n] = true
	}
	for n := range pb2 {
		names[n] = true
	}

	for name := range names {
		cpuA, rssA, startA, _, inA := rate(pa1, pa2, name, elapsedA)
		cpuB, rssB, startB, instB, inB := rate(pb1, pb2, name, elapsedB)

		row := ProcRow{
			Name:      name,
			CPUPctA:   cpuA,
			CPUPctB:   cpuB,
			RSSKBA:    rssA,
			RSSKBB:    rssB,
			Instances: instB,
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
			row.Verdict = worstOf(row.CPUDelta, row.RSSDelta, thresholdPct)
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

func worstOf(cpuD, rssD *float64, threshold float64) ProcVerdict {
	worst := PVFlat
	consider := func(d *float64) {
		if d == nil {
			return
		}
		if *d >= threshold {
			worst = PVWorse
		} else if *d <= -threshold && worst != PVWorse {
			worst = PVBetter
		}
	}
	consider(cpuD)
	consider(rssD)
	return worst
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

func rowMagnitude(r ProcRow) float64 {
	m := 0.0
	if r.CPUDelta != nil && math.Abs(*r.CPUDelta) > m {
		m = math.Abs(*r.CPUDelta)
	}
	if r.RSSDelta != nil && math.Abs(*r.RSSDelta) > m {
		m = math.Abs(*r.RSSDelta)
	}
	return m
}
