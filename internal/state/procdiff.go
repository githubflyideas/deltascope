package state

import (
	"math"
	"sort"
	"strconv"
	"time"

	"github.com/githubflyideas/deltascope/internal/pcp"
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
	// PID is the representative process id for this command name -- the
	// instance that consumed the most CPU. It lets the one-click answer
	// point its next-step command at the actual process (top -H -p PID)
	// rather than a generic command the reader must then re-target. 0 when
	// unknown (an older snapshot without pids, or a gone process).
	PID int `json:"pid,omitempty"`
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
	// Listens marks a process that owned a listening socket in a window it
	// was seen in, according to the listen section captured beside the
	// process section. It is why a row consuming almost nothing can still be
	// reported as having appeared or gone, so the reader is not left to
	// wonder what a 12 MB idle process is doing in the report.
	Listens bool `json:"listens,omitempty"`
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
	// UnwatchedListeners names processes that own a listening socket and are
	// absent from process accounting altogether, so this report holds no CPU
	// or memory figure for them at all -- not even a flat row.
	//
	// It exists because of the contradiction a reader actually hit: the change
	// report named a new listening port and the process holding it, the reader
	// turned to process accounting to see what that process costs, and process
	// accounting said nothing. The selection is the reason (a service
	// whitelist plus the heaviest procTopN by weight, and a new daemon is
	// neither), and an unstated reason reads as the two engines contradicting
	// each other.
	UnwatchedListeners []string `json:"unwatched_listeners,omitempty"`
	Note               string   `json:"note,omitempty"`
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

// listenOwners is the set of process names owning a listening socket in a
// snapshot, taken from the listen section that is captured beside the process
// section on every snapshot. Nil when that section is absent or was skipped,
// which is what an older stored snapshot and a host without `ss` look like.
//
// Read here, at comparison time, rather than folded into the process
// collector's selection -- and that is the whole design decision.
// `ss -lntuHp` reports the owning process only for sockets the capture had the
// privilege to see, which is why the listen section is PrivSensitive. Choosing
// WHICH processes to record on that basis would make the process section's key
// set a function of who ran the capture, so a root baseline against a
// service-user capture would report every small daemon as having appeared or
// vanished -- the phantom add/remove class of bug, reintroduced for the sake of
// a wider selection. Used at comparison time it can only upgrade the verdict of
// a row whose presence changed for real, and no privilege difference can
// manufacture that: what the process section contains does not depend on `ss`.
//
// Empty values are dropped. An unprivileged `ss` lists the socket and omits the
// owner, so keeping "" would match nothing useful and match it eagerly.
func listenOwners(s Snapshot) map[string]bool {
	for _, sec := range s.Sections {
		if sec.Name != "listen" || sec.Skipped != "" {
			continue
		}
		owners := make(map[string]bool, len(sec.Items))
		for _, it := range sec.Items {
			if it.Value != "" {
				owners[it.Value] = true
			}
		}
		return owners
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

	// Who was serving. A process owning a listening socket is a service, and a
	// service arriving or leaving is an event at any size -- which the weight
	// bar below cannot see, because it ranks a presence change by how much the
	// process consumes.
	ownA, ownB := listenOwners(a2), listenOwners(b2)

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
		uptime float64, hasUptime bool) (cpu, rss *float64, start uint64, inst, pid int, approx, present bool) {
		i2, ok2 := second[name]
		if !ok2 {
			return nil, nil, 0, 0, 0, false, false
		}
		t2, r2, s2, c2, p2, ok := DecodeProcItem(i2.Value)
		if !ok {
			return nil, nil, 0, 0, 0, false, false
		}
		rssv := float64(r2)
		rss = &rssv
		start, inst, pid, present = s2, c2, p2, true

		if first != nil {
			if i1, ok1 := first[name]; ok1 {
				if t1, _, s1, _, _, ok := DecodeProcItem(i1.Value); ok && s1 == s2 && t2 >= t1 {
					// same process lifetime and monotonic ticks: a valid rate
					c := cpuPercent(t2-t1, elapsed)
					return &c, rss, start, inst, pid, false, present
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
				return &c, rss, start, inst, pid, true, present
			}
		}
		return nil, rss, start, inst, pid, false, present
	}

	names := map[string]bool{}
	for n := range pa2 {
		names[n] = true
	}
	for n := range pb2 {
		names[n] = true
	}

	for name := range names {
		cpuA, rssA, startA, _, _, _, inA := rate(pa1, pa2, name, elapsedA, upA, hasUpA)
		cpuB, rssB, startB, instB, pidB, approxB, inB := rate(pb1, pb2, name, elapsedB, upB, hasUpB)

		row := ProcRow{
			Name:       name,
			PID:        pidB,
			CPUPctA:    cpuA,
			CPUPctB:    cpuB,
			RSSKBA:     rssA,
			RSSKBB:     rssB,
			Instances:  instB,
			CPUApproxB: approxB,
			Listens:    ownA[name] || ownB[name],
		}
		if inA && inB && startA > 0 && startB > startA {
			row.Restarted = true
		}

		switch {
		case !inA && inB:
			// A process appearing is only a finding if it is substantial --
			// a database that was not here yesterday, or a process now
			// consuming real CPU/RSS -- or if it is serving. A desktop's
			// churn of short-lived, D-Bus-activated helpers (goa-identity,
			// gsd-*, transient Socket Process) enters and leaves on its own
			// and is not an event. noteworthyPresence() gates that; trivial
			// appearances fall through to flat and are filtered from the
			// report.
			if noteworthyPresence(cpuB, rssB, ownB[name]) {
				row.Verdict = PVAppeared
			} else {
				row.Verdict = PVFlat
			}
		case inA && !inB:
			if noteworthyPresence(cpuA, rssA, ownA[name]) {
				row.Verdict = PVGone
			} else {
				row.Verdict = PVFlat
			}
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

	// The residual gap, stated rather than left to look like a contradiction.
	// A listening socket whose owner is not in the process section at all has
	// no row here -- not even a flat one -- because the collector records a
	// service whitelist plus the heaviest procTopN by weight, and a small new
	// daemon is neither. Only the compare window is considered: a name that
	// stopped listening is already covered by the listen section's own diff.
	for name := range ownB {
		if !names[name] {
			d.UnwatchedListeners = append(d.UnwatchedListeners, name)
		}
	}
	sort.Strings(d.UnwatchedListeners)

	sortProcRows(d.Rows)
	sort.Slice(d.Restarts, func(i, j int) bool { return d.Restarts[i].Name < d.Restarts[j].Name })
	return d
}

// noteworthyPresence reports whether a process's mere appearance or
// disappearance is worth showing. There are two ways to qualify, and they
// answer different questions.
//
// By weight: a quarter-core of CPU or a quarter-gig of RSS. The bar is
// deliberately well above the significance floors, because a process only
// "appearing" carries one fact -- it exists now -- so it has to be substantial
// to outweigh the desktop's constant churn of tiny transient helpers. A 9 MB
// identity broker that lives for thirty seconds does not clear it.
//
// By serving: the process owned a listening socket. This exists because the
// weight bar asks the wrong question of a service. A 12 MB Go daemon that binds
// a port is the ordinary shape of a new service, not churn -- and the report
// had a reader hit exactly that: change accounting named the new port and the
// process holding it, process accounting said nothing about that process, and
// two engines looking at the same machine appeared to contradict each other.
// The weight bar was built for the anonymous helpers nothing else in the
// snapshot mentions; a process the listen section names is not one of those.
//
// Membership in procWhitelist deliberately does NOT qualify. That list holds
// generic runtimes (python3, node, java, ruby) whose one-off invocations are
// precisely the churn the bar exists to suppress. A listening socket is a fact
// about what the process is doing; a name is a guess about what it is.
func noteworthyPresence(cpu, rss *float64, serves bool) bool {
	if serves {
		return true
	}
	if cpu != nil && *cpu >= presenceCPUPct {
		return true
	}
	if rss != nil && *rss >= presenceRSSKB {
		return true
	}
	return false
}

// SubstantialInA reports whether the row's baseline-window CPU or memory clears
// the presence weight bar on its own, ignoring whether it was serving.
//
// Exported for the recovery veto, which must not fire on the rows the
// listening-socket qualifier added. That qualifier exists so a small service
// arriving or leaving gets reported; it says nothing about whether the process
// consumed enough for its departure to explain a fall in a machine-level
// metric, which is the only question the veto asks.
func (r ProcRow) SubstantialInA() bool { return noteworthyPresence(r.CPUPctA, r.RSSKBA, false) }

const (
	presenceCPUPct = 25     // percent of one core
	presenceRSSKB  = 262144 // 256 MB
)

// pctChange applies the same two-bar rule as the metric engine: a change
// must be both relatively large and absolutely meaningful. Below the
// absolute floor it returns nil, meaning "not a signal".
//
// The floor also governs the DENOMINATOR, which is the part that was
// missing. Requiring only that ONE side clear the floor lets a baseline of
// 0.0005% of a core survive as a divisor, and a percentage computed against
// noise is a number with no meaning attached:
//
//	0.00055% -> 1.7% of a core   reported as   +308991%
//
// Those figures then dominated every ranking that sorts by delta, so a
// process using 1.7% of a core outranked one using 17%. A baseline under the
// floor is not a baseline; it is indistinguishable from idle, and the honest
// report is no ratio at all -- which the absolute-evidence path in worstOf
// now handles on its own terms.
func pctChange(a, b *float64, minAbs float64) *float64 {
	if a == nil || b == nil {
		return nil
	}
	// Shared core: both-idle noise and the zero denominator.
	delta, noise := pcp.RelChange(*a, *b, minAbs)
	if noise {
		return nil // idle: not a signal for the process ranker
	}
	// Process-path extra policy (stricter than the metric path on purpose):
	// a baseline below the floor is not a usable denominator either, because
	// a process going 0.0005 -> 1.7 % of a core is a division by noise that
	// would otherwise dominate the culprit ranking. The metric path keeps
	// small-baseline ratios (a legitimate 10 -> 25 counter); the process
	// path does not. worstOf handles the from-idle rise on absolute evidence.
	if minAbs > 0 && math.Abs(*a) < minAbs {
		return nil
	}
	return delta
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
