package pcp

import "strconv"

// Triage framework: buckets the catalog into four hardware resources plus
// "the software gremlin". Each block's status light is driven by that
// block's core metrics, so a noisy secondary counter can't flip the whole
// block red.

type TriageStatus string

const (
	TriageBad  TriageStatus = "bad"  // red: a core metric regressed
	TriageWarn TriageStatus = "warn" // amber: a core metric needs watching, or a secondary metric regressed
	TriageOK   TriageStatus = "ok"   // green: flat
)

type TriageBlock struct {
	Key      string       `json:"key"`
	Label    string       `json:"label"`
	Status   TriageStatus `json:"status"`
	Headline string       `json:"headline"` // one-line conclusion
	WorstPct *float64     `json:"worst_pct,omitempty"`

	// Improved names the core metric that got materially BETTER in this
	// window, and it is deliberately independent of Status.
	//
	// Without it the block had one word for two very different situations. A
	// host whose user CPU fell from 2.25 cores to 0.1 -- a bug fixed, the
	// single most significant thing that happened here -- rendered exactly
	// like a host where nothing moved at all: "normal". The status light is
	// still driven only by regressions, because a green light means "no
	// trouble found" and an improvement is not trouble; this is the second
	// half of the sentence, not a fourth colour.
	//
	// Independent of Status on purpose: a block can regress on one core
	// metric and improve on another, and suppressing the improvement because
	// the light is red would hide half of what the window shows.
	Improved    string   `json:"improved,omitempty"`
	ImprovedPct *float64 `json:"improved_pct,omitempty"`
}

// resourceOf maps a catalog category to one of the four resource blocks.
func resourceOf(category string) string {
	switch category {
	case "CPU":
		return "cpu"
	case "Memory":
		return "mem"
	case "Disk I/O", "Filesystem":
		return "disk"
	case "Network":
		return "net"
	}
	return "other"
}

// coreMetrics are the metrics that represent real trouble for their block —
// only these can flip a block red. Other regressions only raise it to amber,
// so high-jitter counters like ICMP or page activations can't taint the block.
var coreMetrics = map[string]bool{
	// CPU: metrics that genuinely mean "compute is tight"
	"kernel.all.cpu.user":              true,
	"kernel.all.cpu.sys":               true,
	"kernel.all.cpu.wait.total":        true,
	"kernel.all.cpu.steal":             true,
	"kernel.all.load":                  true,
	"kernel.all.runnable":              true,
	"kernel.all.pressure.cpu.some.avg": true,
	// Memory: metrics that mean "memory is short"
	"mem.util.available":                  true,
	"swap.pagesin":                        true,
	"swap.pagesout":                       true,
	"mem.vmstat.pgscan_direct":            true,
	"mem.vmstat.oom_kill":                 true,
	"kernel.all.pressure.memory.some.avg": true,
	// Disk: metrics that mean "disk saturated / filling up"
	"disk.all.avactive":               true,
	"disk.all.aveq":                   true,
	"disk.dev.avactive":               true,
	"disk.dev.aveq":                   true,
	"filesys.full":                    true,
	"kernel.all.pressure.io.some.avg": true,
	// Network: metrics that mean "link/queue trouble"
	"network.tcp.retranssegs":     true,
	"network.tcp.listendrops":     true,
	"network.tcp.listenoverflows": true,
	"network.softnet.dropped":     true,
	"network.interface.in.drops":  true,
	"network.interface.out.drops": true,
	"network.interface.in.errors": true,
}

var triageLabels = map[string]string{
	"cpu": "CPU", "mem": "Memory", "disk": "Disk", "net": "Network",
}

// Triage builds the four-resource summary from a diff report.
// The "software gremlin" block is filled in by the caller from process/change signals.
func Triage(rows []DiffRow) []TriageBlock {
	type acc struct {
		worstBad    *DiffRow
		worstWarn   *DiffRow
		worstBetter *DiffRow
		worstAny    float64
	}
	blocks := map[string]*acc{"cpu": {}, "mem": {}, "disk": {}, "net": {}}

	for i := range rows {
		r := rows[i]
		res := resourceOf(r.Category)
		a := blocks[res]
		if a == nil {
			continue
		}
		core := coreMetrics[r.Metric]
		appeared := r.A == nil || r.B == nil
		switch r.Verdict {
		case VWorse:
			if core {
				if a.worstBad == nil || moreSevere(r, *a.worstBad) {
					rr := r
					a.worstBad = &rr
				}
			} else if a.worstWarn == nil && !appeared {
				rr := r
				a.worstWarn = &rr
			}
		case VWatch:
			// A metric appearing/vanishing with no prior baseline is a data-
			// collection artifact (archive window edge, PMDA reload, etc.)
			// far more often than a real signal. Only let it claim a block's
			// headline when it's a core metric -- e.g. OOM kills appearing
			// is worth flagging, entropy.avail appearing is noise.
			if appeared && !core {
				continue
			}
			if a.worstWarn == nil {
				rr := r
				a.worstWarn = &rr
			}
		case VBetter:
			// Only core metrics, for the same reason only core metrics can
			// flip a block red: a jittery secondary counter falling is not
			// news, and an improvement claim has to be at least as hard to
			// earn as a regression.
			//
			// A nil ratio is refused here even though the worse path accepts
			// one. Verdict is VBetter with no DeltaPct only for a metric that
			// went from zero to non-zero under BetterUp polarity -- which for
			// mem.util.available means the metric was not sampled in the
			// baseline half far more often than it means the memory came
			// back. The VWatch branch above already distrusts that shape; an
			// unquantified improvement would be the same artifact wearing a
			// better word.
			if core && r.DeltaPct != nil {
				if a.worstBetter == nil || absDeltaVal(r) > absDeltaVal(*a.worstBetter) {
					rr := r
					a.worstBetter = &rr
				}
			}
		}
	}

	order := []string{"cpu", "mem", "disk", "net"}
	out := make([]TriageBlock, 0, 4)
	for _, k := range order {
		a := blocks[k]
		b := TriageBlock{Key: k, Label: triageLabels[k], Status: TriageOK, Headline: "normal"}
		switch {
		case a.worstBad != nil:
			b.Status = TriageBad
			b.Headline = headline(*a.worstBad)
			b.WorstPct = a.worstBad.DeltaPct
		case a.worstWarn != nil:
			b.Status = TriageWarn
			b.Headline = headline(*a.worstWarn)
			b.WorstPct = a.worstWarn.DeltaPct
		}
		if a.worstBetter != nil {
			b.Improved = headline(*a.worstBetter)
			b.ImprovedPct = a.worstBetter.DeltaPct
		}
		out = append(out, b)
	}
	return out
}

func headline(r DiffRow) string {
	label := r.Label
	if r.Instance != "" {
		label += "[" + r.Instance + "]"
	}
	if r.DeltaPct == nil {
		if r.A == nil && r.B != nil {
			return label + " appeared"
		}
		return label + " anomaly"
	}
	sign := "+"
	if *r.DeltaPct < 0 {
		sign = ""
	}
	return label + " " + sign + formatPct(*r.DeltaPct)
}

func formatPct(v float64) string {
	if v > 999 || v < -999 {
		return "spike"
	}
	return strconv.Itoa(int(v)) + "%"
}

// moreSevere reports whether candidate should replace the current worst
// row for a triage block.
//
// A row whose DeltaPct is nil -- an appeared/vanished metric, or a 0->
// non-zero jump -- carries no measurable magnitude. The old code gave it a
// magnitude of 1e18, so a boundary artifact (a per-core counter that merely
// "appeared" at a window edge, a PMDA reload) always outranked a real,
// quantified +300% regression and stole the block's headline, leaving an
// empty-delta "anomaly" on screen. That contradicts the VWatch branch,
// which already suppresses appeared-but-not-core artifacts.
//
// So a row with a real DeltaPct always outranks one without; only when both
// are unquantified (or both quantified) does magnitude decide. This can
// only ever prefer a measured regression over an unmeasured artifact, never
// hide a real one.
func moreSevere(candidate, current DiffRow) bool {
	cq, curq := candidate.DeltaPct != nil, current.DeltaPct != nil
	if cq != curq {
		return cq // a quantified row beats an unquantified one
	}
	return absDeltaVal(candidate) > absDeltaVal(current)
}

func absDeltaVal(r DiffRow) float64 {
	if r.DeltaPct == nil {
		return 0
	}
	if *r.DeltaPct < 0 {
		return -*r.DeltaPct
	}
	return *r.DeltaPct
}
