package diagnose

import (
	"fmt"

	"github.com/githubflyideas/deltascope/internal/pcp"
	"github.com/githubflyideas/deltascope/internal/state"
)

// This file exists to answer one question the tool could not answer before:
// something got better -- can we say so?
//
// The naive version of that feature is a trap, and it is worth being explicit
// about which one. A metric falling is not evidence of an improvement. User
// CPU dropping from 2.25 cores to 0.1 is exactly what a fixed bug looks like,
// and it is also exactly what a crashed service, an OOM kill and a unit that
// failed to restart look like. The aggregate number is identical in all four
// cases, because the aggregate knows how much work was done and nothing at all
// about whether the work was supposed to be done. Reporting "CPU improved"
// over a dead service would be worse than the silence it replaced: silence
// makes the reader go and look, a false green light tells them not to.
//
// So an improvement is only ever claimed with per-process evidence that the
// work is still being done: a process that is still running and is now using
// materially less. That evidence already existed -- CompareProcesses has
// distinguished PVBetter from PVGone since it was written -- and had simply
// never been read by anything.
//
// The bias is deliberately toward silence. Every uncertain shape below is
// declined rather than guessed: no snapshots, no attributable process, a
// significant process missing, an improvement in a resource with no
// per-process accounting. A missed improvement costs the reader nothing they
// had yesterday; a wrong one costs them the habit of believing the page.

// Recovery floors: how much of the resource one process must have given back
// before it counts as the explanation for a machine-wide fall.
//
// These are the burst floors from the process differ, applied in the other
// direction, and the symmetry is the argument for them: if a rise to a quarter
// of a core is the bar for calling a process a finding, a fall of a quarter of
// a core is the bar for calling it the reason. Without a floor, a process
// drifting from 1.2% to 0.9% of a core could be offered as the evidence behind
// a -95% machine-wide drop -- a real number attached to an unrelated claim,
// which is the same unit-blindness that once made culpritByCPU name the wrong
// process.
const (
	recoveryCPUFloorPct = 25     // percent of one core
	recoveryRSSFloorKB  = 262144 // 256 MB
)

// recovery is an improvement plus the process that accounts for it. Both
// halves are required; there is no constructor that can produce one without
// the other.
type recovery struct {
	block *pcp.TriageBlock
	proc  *state.ProcRow
}

// findRecovery returns the improvement worth putting in the headline, or nil.
//
// It reads triage blocks, so it is silent on a host with no PCP archive: the
// /proc leg feeds the reasoning chain and produces no blocks, by the same
// decision that leaves that page without Findings. Claiming a recovery there
// would mean teaching the /proc path to build blocks, and the last minute of
// /proc against the minute before it is too short a baseline to recognise a
// fix that landed an hour ago anyway.
func findRecovery(blocks []pcp.TriageBlock, pd state.ProcDiff) *recovery {
	// A significant process disappearing vetoes the whole claim, not just the
	// claim about that process. When the top consumer of window A is simply
	// not there in window B, "it is using less" is the one explanation of the
	// fall that is definitely wrong, and we cannot tell from here whether the
	// improvement we found is a separate real one or the same event seen from
	// another angle. PVGone is already gated on the process having been
	// substantial (a quarter core or 256 MB), so every row it appears on is
	// worth staying quiet about.
	if gone := goneProcess(pd.Rows); gone != nil {
		return nil
	}

	var best *pcp.TriageBlock
	for i := range blocks {
		b := &blocks[i]
		if b.Improved == "" || b.ImprovedPct == nil {
			continue
		}
		// Disk and network have no per-process accounting here, the same
		// reason synthesize names no culprit for them. An improvement we
		// cannot attribute is one we cannot distinguish from a link going
		// down, and a quiet link is not a fast link.
		if b.Key != "cpu" && b.Key != "mem" {
			continue
		}
		if best == nil || absPct(b.ImprovedPct) > absPct(best.ImprovedPct) {
			best = b
		}
	}
	if best == nil {
		return nil
	}
	proc := recoveredProcess(pd.Rows, best.Key)
	if proc == nil {
		return nil
	}
	return &recovery{block: best, proc: proc}
}

// goneProcess returns the first substantial process present in the baseline
// window and absent from the compare window.
func goneProcess(rows []state.ProcRow) *state.ProcRow {
	for i := range rows {
		if rows[i].Verdict == state.PVGone {
			return &rows[i]
		}
	}
	return nil
}

// recoveredProcess finds the process that gave the most of the resource back,
// among those still running. Verdict must be PVBetter: that value is only
// reached from the branch where the process was present in both windows, so it
// carries the "still running" half of the claim, and CompareProcesses already
// required the fall to clear its own relative and absolute bars.
func recoveredProcess(rows []state.ProcRow, resource string) *state.ProcRow {
	var best *state.ProcRow
	var bestFreed float64
	for i := range rows {
		r := &rows[i]
		if r.Verdict != state.PVBetter {
			continue
		}
		var freed, floor float64
		switch resource {
		case "cpu":
			if r.CPUPctA == nil || r.CPUPctB == nil {
				continue
			}
			freed, floor = *r.CPUPctA-*r.CPUPctB, recoveryCPUFloorPct
		case "mem":
			if r.RSSKBA == nil || r.RSSKBB == nil {
				continue
			}
			freed, floor = *r.RSSKBA-*r.RSSKBB, recoveryRSSFloorKB
		default:
			return nil
		}
		if freed < floor {
			continue
		}
		if best == nil || freed > bestFreed {
			best, bestFreed = r, freed
		}
	}
	return best
}

// recoveryHeadline is the one line for the top of the page. It mirrors the
// degraded wording ("CPU is degraded: ...") so the two read as the same
// sentence with the sign flipped.
//
// A configuration change is mentioned alongside rather than blamed. "N changes
// were also detected" is what is known; "the improvement came from that
// change" is not, and one window cannot tell them apart.
func recoveryHeadline(rc *recovery, changes int) string {
	s := rc.block.Label + " improved: " + rc.block.Improved
	if changes > 0 {
		s += fmt.Sprintf("; %d configuration change(s) were also detected", changes)
	}
	return s
}

// recoveryEvidence is the sentence that makes the headline checkable: which
// process, from what to what, and still running. Without it the reader has to
// take the improvement on faith, which is the position they were in with the
// bogus SQLite out-of-memory message and the reason that one wasted an hour.
func recoveryEvidence(rc *recovery) string {
	r := rc.proc
	var s string
	switch rc.block.Key {
	case "cpu":
		now := fmt.Sprintf("%.0f%%", *r.CPUPctB)
		if r.CPUApproxB {
			// Lifetime average rather than a measured windowed rate; the same
			// one-character warning culpritByCPU uses.
			now = "~" + now
		}
		s = fmt.Sprintf("%s went from %.0f%% to %s of a core and is still running",
			r.Name, *r.CPUPctA, now)
	case "mem":
		s = fmt.Sprintf("%s went from %.0f MB to %.0f MB of RSS and is still running",
			r.Name, *r.RSSKBA/1024, *r.RSSKBB/1024)
	default:
		return ""
	}
	if r.Restarted {
		// Not a caveat -- usually the point. A process that restarted and came
		// back cheaper is what deploying a fix looks like, and the reader
		// correlating this against their own afternoon needs to know a restart
		// is in the window.
		s += ", after a restart in this window"
	}
	return s
}

func absPct(p *float64) float64 {
	if p == nil {
		return 0
	}
	if *p < 0 {
		return -*p
	}
	return *p
}
