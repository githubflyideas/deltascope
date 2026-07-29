package reasoning

import (
	"math"
	"sort"
	"strings"

	"github.com/githubflyideas/deltascope/internal/pcp"
)

// Derived rows exist because of a structural gap in the aggregate
// metrics: kernel.all.cpu.* are whole-machine sums, so a single fully
// saturated core is 1000 ms/s no matter how big the machine is, which is
// 25% of a 4-core host and 1.5% of a 64-core one. Every scale-relative
// threshold in the state catalog is therefore guaranteed to miss it --
// and "one process pegging one core" is one of the most common failure
// shapes there is (a busy loop, a single-threaded bottleneck, a runtime
// with a global lock).
//
// The per-core instance metrics DO carry the signal, but no single one of
// them carries it alone: a core pegged in kernel mode shows up in
// kernel.percpu.cpu.sys, not .user, and a state written against either
// one individually would miss the other half of the cases. So we
// synthesize the sum here, once, and let states be written against a
// metric that means what they need it to mean.
//
// These names are namespaced under "derived." so they can never collide
// with a real PCP metric, and they are added to the row set rather than
// replacing anything: the raw per-core rows stay visible in the report.
const (
	// MetricCoreBusy is per-core non-idle, non-iowait CPU time in ms/s.
	// A value near 1000 means that core is saturated with actual work.
	// Instance is the PCP core instance name (cpu0, cpu1, ...).
	MetricCoreBusy = "derived.percpu.cpu.busy"

	// MetricBusiestCore is the maximum of MetricCoreBusy across all
	// cores: "how saturated is the single most loaded core". This is the
	// scalar that makes single-core saturation expressible as a state
	// without needing instance-aware threshold logic.
	MetricBusiestCore = "derived.cpu.busiest_core"

	// MetricCoreImbalance is busiest-core minus mean-core busy time. High
	// imbalance with a low machine-wide average is the signature of a
	// serialized workload: adding cores will not help, and the machine
	// looks idle in every aggregate view.
	MetricCoreImbalance = "derived.cpu.core_imbalance"

	// MetricCoresBusy counts how many cores are individually saturated,
	// which separates "one hot core" from "the whole machine is pegged"
	// without re-deriving it from the aggregate.
	MetricCoresBusy = "derived.cpu.cores_busy"
)

// coreBusyComponents are the per-core metrics summed into "busy". iowait
// is deliberately excluded: a core in iowait is not doing work, it is
// waiting, and counting it as busy would turn a storage problem into a
// phantom CPU problem. Idle and steal are excluded for the same reason --
// steal has its own dedicated state, and folding it in here would make a
// starved guest look like a busy one.
var coreBusyComponents = []string{
	"kernel.percpu.cpu.user",
	"kernel.percpu.cpu.sys",
	"kernel.percpu.cpu.irq.soft",
}

// coreSaturatedMsPerSec is the per-core busy level counted as saturated.
// A core cannot exceed 1000 ms/s by definition, and sampling jitter means
// a genuinely pegged core reports slightly under it, so the bar sits at
// 85% rather than at the ceiling.
const coreSaturatedMsPerSec = 850

// Derive returns rows synthesized from the raw ones. The result is meant
// to be appended to the input, not to replace it.
//
// When the archive has no per-core metrics at all (they are optional in
// PCP, and a slim catalog preset may omit them), this returns nothing:
// silence is correct, because a derived value with no inputs would be a
// fabricated zero, and a zero here reads as "no core is busy" -- the most
// misleading answer available.
func Derive(rows []pcp.DiffRow) []pcp.DiffRow {
	busyA, busyB := map[string]float64{}, map[string]float64{}
	seenA, seenB := map[string]bool{}, map[string]bool{}

	for _, r := range rows {
		if r.Instance == "" || !isCoreBusyComponent(r.Metric) {
			continue
		}
		if r.A != nil {
			busyA[r.Instance] += *r.A
			seenA[r.Instance] = true
		}
		if r.B != nil {
			busyB[r.Instance] += *r.B
			seenB[r.Instance] = true
		}
	}
	if len(seenA) == 0 && len(seenB) == 0 {
		return nil
	}

	instances := make([]string, 0, len(seenB))
	for inst := range seenB {
		instances = append(instances, inst)
	}
	for inst := range seenA {
		if !seenB[inst] {
			instances = append(instances, inst)
		}
	}
	sort.Strings(instances)

	out := make([]pcp.DiffRow, 0, len(instances)+3)
	for _, inst := range instances {
		out = append(out, derivedRow(MetricCoreBusy, inst, "per-core busy CPU", "CPU", "millisec / second",
			valOrNil(busyA, seenA, inst), valOrNil(busyB, seenB, inst)))
	}

	// Aggregates are computed per side independently: window A and window
	// B can legitimately have different core counts (a VM was resized, a
	// core was hot-plugged), and averaging across a union of instances
	// would silently understate the smaller side.
	maxA, meanA, cntA, okA := summarize(busyA, seenA)
	maxB, meanB, cntB, okB := summarize(busyB, seenB)

	out = append(out, derivedRow(MetricBusiestCore, "", "busiest core", "CPU", "millisec / second",
		ptrIf(maxA, okA), ptrIf(maxB, okB)))
	out = append(out, derivedRow(MetricCoreImbalance, "", "core imbalance", "CPU", "millisec / second",
		ptrIf(maxA-meanA, okA), ptrIf(maxB-meanB, okB)))
	out = append(out, derivedRow(MetricCoresBusy, "", "saturated cores", "CPU", "none",
		ptrIf(cntA, okA), ptrIf(cntB, okB)))
	return out
}

func isCoreBusyComponent(metric string) bool {
	for _, m := range coreBusyComponents {
		if metric == m {
			return true
		}
	}
	return false
}

// summarize reduces the per-core map to max, mean, and saturated count.
func summarize(vals map[string]float64, seen map[string]bool) (max, mean, saturated float64, ok bool) {
	n := 0
	max = math.Inf(-1)
	sum := 0.0
	for inst, present := range seen {
		if !present {
			continue
		}
		v := vals[inst]
		if v > max {
			max = v
		}
		sum += v
		if v >= coreSaturatedMsPerSec {
			saturated++
		}
		n++
	}
	if n == 0 {
		return 0, 0, 0, false
	}
	return max, sum / float64(n), saturated, true
}

func valOrNil(vals map[string]float64, seen map[string]bool, inst string) *float64 {
	if !seen[inst] {
		return nil
	}
	v := vals[inst]
	return &v
}

func ptrIf(v float64, ok bool) *float64 {
	if !ok {
		return nil
	}
	return &v
}

// derivedRow builds a row with the same delta semantics the metric engine
// uses, so a derived row behaves identically to a real one everywhere
// downstream -- including the dual-significance floor, which is why the
// absolute minimum is passed explicitly rather than left at zero.
func derivedRow(metric, instance, label, category, units string, a, b *float64) pcp.DiffRow {
	r := pcp.DiffRow{
		Metric: metric, Instance: instance, Label: label,
		Category: category, Units: units, A: a, B: b,
	}
	r.DeltaPct, r.Exceeded, r.Verdict = pcp.Judge(a, b, pcp.WorseUp, 15, derivedMinAbs(metric))
	return r
}

// derivedMinAbs keeps a derived row from reporting an explosive ratio off
// a near-idle baseline, the same reason the metric catalog carries an
// absolute floor. 50 ms/s is 5% of a core: below that on both sides, a
// change in "busy" is not a signal about anything.
func derivedMinAbs(metric string) float64 {
	if strings.HasSuffix(metric, "cores_busy") {
		return 1 // whole cores; a fractional floor would be meaningless
	}
	return 50
}
