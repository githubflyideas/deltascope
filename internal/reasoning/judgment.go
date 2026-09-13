package reasoning

// A state answers one of two structurally different questions, and a reader
// deciding whether to trust a quiet screen needs to know which.
//
// An absolute state asks "is this value, right now, past a line no healthy
// machine crosses" -- a filesystem at 99%, a non-zero NIC error counter, a
// run queue eight deep per core. It needs one window and nothing else, so it
// is answerable on a first-ever collection, and it stays true for as long as
// the condition lasts.
//
// A change state asks "is this worse than it was" -- it rests on the row's
// verdict or on the A->B delta, and therefore on a baseline. On a
// single-window run every one of them is unanswerable, which is not a fault
// and not a clean bill of health. Lumping the two together is how a tool
// shows a green tick for a disk that has been full for a week: the absolute
// question was never asked, and the change question said "no change".
type JudgmentKind string

const (
	JudgeAbsolute JudgmentKind = "absolute"
	JudgeChange   JudgmentKind = "change"
)

// Judgment classifies the state by what it needs in order to answer.
//
// Any single change-relative condition makes the whole state change-relative,
// because the conditions are ANDed: a state that pairs an absolute floor with
// Verdict "worse" cannot fire without a baseline no matter how bad the
// current value is. Reachability, not intent, is what the split has to
// report, so the rule follows the AND.
func (st State) Judgment() JudgmentKind {
	for _, c := range st.When {
		if c.Verdict != "" || c.DeltaGte != nil || c.DeltaLte != nil || c.Appeared {
			return JudgeChange
		}
	}
	return JudgeAbsolute
}

// GapKind is why a state could not be judged, as a machine-readable value.
//
// The prose reason names the metric and the missing field, which is what a
// human debugging the catalog wants. It is the wrong thing to branch a UI on:
// grouping unmeasured states by matching reason strings breaks the moment the
// text is translated, and the four kinds below ask the reader for four
// different things -- or, in one case, for nothing at all.
type GapKind string

const (
	// GapNeedsRoot: the source exists but is not readable unprivileged.
	// Action: run the collector with more privilege.
	GapNeedsRoot GapKind = "needs_root"
	// GapAbsent: the host genuinely does not have this. No swap configured,
	// PSI not compiled into the kernel, a counter this kernel version does
	// not export. Action: none. This is the kind that must not produce a
	// hint, because there is nothing the reader could do and a hint would
	// turn a correct configuration into a permanent to-do item.
	GapAbsent GapKind = "absent"
	// GapTooFewSamples: the metric was read, but not enough times for the
	// statistic the condition wants. Action: collect for longer.
	GapTooFewSamples GapKind = "too_few_samples"
	// GapNoBaseline: a change-relative condition with nothing to compare
	// against. Action: take a second snapshot. This is the expected state of
	// the entire change-judgment half of the roster on a first run, and
	// reporting it as a failure would be wrong.
	GapNoBaseline GapKind = "no_baseline"
	// GapNoData: the metric is not in the row set and the host is expected to
	// have it -- an archive whose pmlogger config never logged it, or a read
	// that failed. Action: check what is being collected.
	GapNoData GapKind = "no_data"
)

// Gap is why one state is unmeasured: the kind for code, the prose for people.
type Gap struct {
	Kind   GapKind `json:"kind"`
	Reason string  `json:"reason"`
}

// optionalMetrics are the metric prefixes whose absence describes the host
// rather than a failure to collect it. Matched as a prefix so a family can be
// named once.
//
// Keeping this list short is deliberate. Every entry converts a visible gap
// into a silent one, so a metric belongs here only when its absence is a
// legitimate configuration -- not when it is merely often missing.
var optionalMetrics = []string{
	// PSI: CONFIG_PSI=n, psi=0 on the kernel command line, or /proc/pressure
	// masked inside a container. Common, and never actionable.
	"kernel.all.pressure.",
	// CONFIG_SWAP=n drops SwapFree from /proc/meminfo entirely, and a host
	// with no swap has no swap problems to report.
	"swap.",
	// vmstat's field set moves with the kernel version: allocstall was split
	// per zone in 4.14, oom_kill arrived in 4.13, compact_stall needs
	// CONFIG_COMPACTION. A missing counter here is the kernel's shape.
	"mem.vmstat.allocstall",
	"mem.vmstat.compact_stall",
	"mem.vmstat.oom_kill",
}

// rootOnlyMetrics are the metric prefixes that exist on every host but cannot
// be read without privilege.
//
// It is empty, and that is the honest answer for the current collector: every
// source behind the state catalog is world-readable /proc. The hardware and
// kernel error sources that do need privilege -- MCE and EDAC counters, SMART
// attributes, per-queue NIC statistics from ethtool, the kernel ring buffer
// under a restricted dmesg_restrict -- are not collected yet. When they land,
// naming them here is the whole of the wiring: the classification, the API
// field and the hint the reader sees all follow from this list.
var rootOnlyMetrics []string

// classifyMissing decides what a metric's total absence from the row set
// means.
func classifyMissing(metric string) GapKind {
	if matchesPrefix(metric, rootOnlyMetrics) {
		return GapNeedsRoot
	}
	if matchesPrefix(metric, optionalMetrics) {
		return GapAbsent
	}
	return GapNoData
}

func matchesPrefix(metric string, prefixes []string) bool {
	for _, p := range prefixes {
		if len(metric) >= len(p) && metric[:len(p)] == p {
			return true
		}
	}
	return false
}
