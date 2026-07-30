package reasoning

// Package reasoning implements the named-state layer that sits between raw
// metric rows and diagnoses.
//
// The existing rule engine (internal/pcp.EvaluateRules) maps a set of
// metric conditions directly onto a conclusion sentence. That works, but
// couples the two: a condition like "steal time is high" can't be reused
// by a second diagnosis without duplicating it, and a diagnosis can't be
// expressed as a combination of independently-meaningful conditions.
//
// This layer names those conditions. A State is a named, independently
// evaluable predicate over metric rows -- state.cpu.steal_high,
// state.mem.available_low. A Diagnosis (see diagnosis.go) then references
// states by name and combines them, including negation, so
// diagnosis.vm_cpu_contention can say "steal_high AND runqueue_high AND
// NOT user_cpu_high" -- the "NOT" being what distinguishes hypervisor
// contention from this guest simply being busy.
//
// Both absolute and change-relative conditions are supported on purpose:
//
//   - Absolute ("steal is above 10% right now") is what most named states
//     actually mean, and it catches a problem that has been present in
//     BOTH comparison windows -- steady 20% steal shows zero change but is
//     still a real problem. A pure change-diff would stay silent.
//   - Change-relative ("steal rose 300% vs the baseline window") is
//     deltascope's original premise and stays first-class.
//
// A state may use either or both. Which one a given state should use is a
// judgement call per state, not a global setting.

import (
	"fmt"
	"sort"
	"strings"

	"github.com/githubflyideas/deltascope/internal/pcp"
)

// Cond is one condition inside a state definition. Semantics deliberately
// mirror pcp.RuleCond so the existing evaluation logic and JSON shape stay
// familiar, with the absolute-value fields made explicit.
type Cond struct {
	Metric string `json:"metric"`

	// Absolute conditions, evaluated against window B's mean (the "now"
	// side). Use these for states that describe a condition rather than a
	// movement.
	BGte *float64 `json:"b_gte,omitempty"`
	BLte *float64 `json:"b_lte,omitempty"`

	// Scale-relative absolute conditions. These express a threshold in
	// terms of the machine's own size rather than as a bare number,
	// because the aggregate PCP metrics are whole-machine sums: 2000 ms/s
	// of user time is most of a 2-core VM and background noise on a
	// 64-core box, so a fixed threshold is wrong on one of them by
	// construction. Resolved against the host at evaluation time.
	//
	//   BGteCores:      "N cores' worth of CPU time" -> N*1000 ms/s
	//   BGteMachineFrac: fraction of TOTAL CPU capacity -> f*NCPU*1000 ms/s
	//   BGtePerCPU:     per-CPU quantity -> n*NCPU (run queue, etc)
	//
	// At most one of these should be set alongside a plain BGte.
	BGteCores       *float64 `json:"b_gte_cores,omitempty"`
	BGteMachineFrac *float64 `json:"b_gte_machine_frac,omitempty"`
	BGtePerCPU      *float64 `json:"b_gte_per_cpu,omitempty"`

	// Peak conditions, evaluated against window B's MAXIMUM sample rather
	// than its mean.
	//
	// These exist because the comparison window is an hour and the mean
	// over an hour is a poor description of a short, severe event. A
	// process that pegs a core for 20 of 60 minutes contributes 333 ms/s
	// to the hourly mean -- a third of a core, under every threshold worth
	// setting -- while the machine was genuinely saturated for a third of
	// the window. Averaging is precisely the operation that destroys the
	// signal, and the mean was the only thing this layer looked at.
	//
	// pmlogsummary already reports min/max/count alongside the mean, and
	// DiffRow has carried them since the sample-statistics work; they were
	// used only for a hover tooltip. Reading them here costs nothing.
	BMaxGte         *float64 `json:"b_max_gte,omitempty"`
	BMaxGteCores    *float64 `json:"b_max_gte_cores,omitempty"`
	BMaxMachineFrac *float64 `json:"b_max_gte_machine_frac,omitempty"`
	BMaxGtePerCPU   *float64 `json:"b_max_gte_per_cpu,omitempty"`

	// MinSamples requires at least this many samples behind the window's
	// statistics. A peak condition on a 3-sample window describes an
	// artifact; on a 1400-sample window it describes something that
	// happened. Peak conditions should always carry one.
	MinSamples int `json:"min_samples,omitempty"`

	// PeakRatioGte requires B's peak to exceed B's mean by this factor,
	// which is what "spiky rather than steadily loaded" actually means. A
	// steady 900 ms/s and a bursty 900-peak/90-mean both have a high peak
	// and want opposite conclusions.
	PeakRatioGte *float64 `json:"peak_ratio_gte,omitempty"`

	// Change-relative conditions, evaluated against the A→B delta.
	DeltaGte *float64 `json:"delta_gte,omitempty"`
	DeltaLte *float64 `json:"delta_lte,omitempty"`

	// Verdict constrains the row's own significance judgement, which
	// already accounts for the dual-significance floor (relative AND
	// absolute) from the metric catalog. Leaving it empty means the state
	// doesn't care how the row was judged, only about the raw numbers
	// above -- which is exactly right for absolute states, since a
	// steady-but-high value is judged "flat".
	Verdict string `json:"verdict,omitempty"`

	// Appeared matches a metric present in B but absent in A.
	Appeared bool `json:"appeared,omitempty"`
}

// State is a named predicate over metric rows.
type State struct {
	ID string `json:"id"`
	// Description is for humans reading the JSON, not shown in the UI.
	Description string `json:"description,omitempty"`
	// Domain groups states for filtering/reporting (cpu, memory, io, net).
	Domain string `json:"domain,omitempty"`
	// All conditions must hold for the state to be active (AND).
	When []Cond `json:"when"`
}

// Active is a state that evaluated true, with the rows that made it true.
type Active struct {
	ID       string   `json:"id"`
	Domain   string   `json:"domain,omitempty"`
	Evidence []string `json:"evidence"`
}

// Evaluate returns every state that holds for the given rows, keyed by
// state ID for O(1) lookup by the diagnosis layer.
func Evaluate(states []State, rows []pcp.DiffRow) map[string]Active {
	return EvaluateOn(states, rows, Host())
}

// EvaluateOn is Evaluate against an explicit machine description, so
// scale-relative thresholds can be resolved for a host other than the
// one running deltascope.
func EvaluateOn(states []State, rows []pcp.DiffRow, m Machine) map[string]Active {
	// Derived rows are computed here rather than by the caller so that
	// every entry point into the state layer sees them. A state that
	// depends on a derived metric would otherwise silently never fire
	// depending on which code path built the row set.
	byMetric := map[string][]pcp.DiffRow{}
	for _, r := range rows {
		byMetric[r.Metric] = append(byMetric[r.Metric], r)
	}
	for _, r := range Derive(rows) {
		byMetric[r.Metric] = append(byMetric[r.Metric], r)
	}

	out := map[string]Active{}
	for _, st := range states {
		if len(st.When) == 0 {
			continue // a state with no conditions would always be true
		}
		evidence := make([]string, 0, len(st.When))
		matched := true
		for _, c := range st.When {
			row, ok := firstMatch(c, byMetric[c.Metric], m)
			if !ok {
				matched = false
				break
			}
			evidence = append(evidence, evidenceLine(row))
		}
		if matched {
			out[st.ID] = Active{ID: st.ID, Domain: st.Domain, Evidence: evidence}
		}
	}
	return out
}

// firstMatch finds the first row (across instances) satisfying the
// condition. Instance-level metrics (per-disk, per-NIC) can have many
// rows; one instance meeting the condition makes the state true, since
// "some disk is saturated" is the useful reading, not "every disk is".
func firstMatch(c Cond, rows []pcp.DiffRow, m Machine) (pcp.DiffRow, bool) {
	for _, row := range rows {
		if condMatch(c, row, m) {
			return row, true
		}
	}
	return pcp.DiffRow{}, false
}

func condMatch(c Cond, row pcp.DiffRow, m Machine) bool {
	if c.Verdict != "" && string(row.Verdict) != c.Verdict {
		return false
	}
	if c.Appeared && !(row.A == nil && row.B != nil) {
		return false
	}
	if c.BGte != nil && (row.B == nil || *row.B < *c.BGte) {
		return false
	}
	if c.BGteCores != nil && (row.B == nil || *row.B < m.cores(*c.BGteCores)) {
		return false
	}
	if c.BGteMachineFrac != nil && (row.B == nil || *row.B < m.fractionOfMachine(*c.BGteMachineFrac)) {
		return false
	}
	if c.BGtePerCPU != nil && (row.B == nil || *row.B < m.perCPU(*c.BGtePerCPU)) {
		return false
	}
	if c.BLte != nil && (row.B == nil || *row.B > *c.BLte) {
		return false
	}
	// Peak conditions require the row to actually carry statistics. A
	// pmlogsummary build that omits min/max leaves BMax nil, and treating
	// a missing peak as zero would silently disable every peak state; not
	// matching is the honest outcome.
	if c.MinSamples > 0 && row.BCount < c.MinSamples {
		return false
	}
	if c.BMaxGte != nil && (row.BMax == nil || *row.BMax < *c.BMaxGte) {
		return false
	}
	if c.BMaxGteCores != nil && (row.BMax == nil || *row.BMax < m.cores(*c.BMaxGteCores)) {
		return false
	}
	if c.BMaxMachineFrac != nil && (row.BMax == nil || *row.BMax < m.fractionOfMachine(*c.BMaxMachineFrac)) {
		return false
	}
	if c.BMaxGtePerCPU != nil && (row.BMax == nil || *row.BMax < m.perCPU(*c.BMaxGtePerCPU)) {
		return false
	}
	if c.PeakRatioGte != nil {
		if row.BMax == nil || row.B == nil {
			return false
		}
		// A near-zero mean makes the ratio meaningless the same way a
		// near-zero baseline makes a percentage change meaningless: the
		// numbers are real and they describe nothing. Require a
		// non-trivial peak before trusting the ratio.
		if *row.B <= 0 || *row.BMax < 1 {
			return false
		}
		if *row.BMax / *row.B < *c.PeakRatioGte {
			return false
		}
	}
	if c.DeltaGte != nil && (row.DeltaPct == nil || *row.DeltaPct < *c.DeltaGte) {
		return false
	}
	if c.DeltaLte != nil && (row.DeltaPct == nil || *row.DeltaPct > *c.DeltaLte) {
		return false
	}
	return true
}

// evidenceLine renders a row as a short factual citation. Absolute-state
// evidence should show the value, not a percentage change -- "steal is at
// 23%" is the point, and reporting "+4%" for a steadily-bad metric would
// understate it.
func evidenceLine(row pcp.DiffRow) string {
	name := row.Metric
	if row.Instance != "" {
		name += "[" + row.Instance + "]"
	}
	var parts []string
	if row.B != nil {
		parts = append(parts, fmt.Sprintf("B=%s", trimFloat(*row.B)))
	}
	// The peak is cited whenever it is materially above the mean, because
	// there the mean alone actively misdescribes the window: "B=333" for a
	// core pegged for 20 minutes reads as a third of a core of steady
	// load, which is not what happened.
	if row.BMax != nil && row.B != nil && *row.BMax > *row.B*1.5 {
		parts = append(parts, fmt.Sprintf("peak=%s", trimFloat(*row.BMax)))
	}
	if row.DeltaPct != nil {
		parts = append(parts, fmt.Sprintf("Δ%+.1f%%", *row.DeltaPct))
	}
	if len(parts) == 0 {
		return name
	}
	return name + " " + strings.Join(parts, " ")
}

func trimFloat(v float64) string {
	s := fmt.Sprintf("%.3f", v)
	s = strings.TrimRight(s, "0")
	s = strings.TrimRight(s, ".")
	if s == "" || s == "-" {
		return "0"
	}
	return s
}

// ActiveIDs returns the sorted IDs of the active states, for stable output.
func ActiveIDs(active map[string]Active) []string {
	ids := make([]string, 0, len(active))
	for id := range active {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids
}

func f(v float64) *float64 { return &v }

// States is the built-in catalog.
//
// Thresholds describe a genuinely notable condition, not merely a non-zero
// one. Three kinds appear, and which one a state uses is a judgement about
// that specific state rather than a global policy:
//
//   - Scale-relative (BGteMachineFrac, BGtePerCPU) for whole-machine sums,
//     where a bare number is wrong on some class of host by construction.
//   - Absolute for quantities that already mean the same thing everywhere:
//     a percentage, a per-core figure, or a counter whose first non-zero
//     value is the finding (an OOM kill, a listen-queue drop).
//   - Change-relative for the few conditions that only exist as movement,
//     principally available memory falling.
//
// Several states exist specifically to be used as NEGATIVE conditions.
// state.cpu.user_high earns its place mainly by separating "this guest is
// busy" from "this guest is starved", and the pairs
// saturated/queue_deep, nearly_full/critically_full, and
// pressure_high/pressure_full exist so a diagnosis can require the
// stronger form and suppress the weaker one.
//
// Counter-valued states are compared against B's mean rate rather than a
// total, because that is what pmlogsummary reports for a counter: a
// sustained rate, not the raw increment.
var States = []State{
	// ---- CPU ----
	// kernel.all.cpu.* are whole-machine sums in ms/s, so every threshold
	// here is expressed against machine size rather than as a bare number.
	{
		ID:          "state.cpu.steal_high",
		Domain:      "cpu",
		Description: "Hypervisor is taking CPU away from this guest. Absolute: steal matters regardless of whether it changed, and 10% of total capacity is well past noise on any machine size.",
		When:        []Cond{{Metric: "kernel.all.cpu.steal", BGteMachineFrac: f(0.10)}},
	},
	{
		ID:     "state.cpu.runqueue_high",
		Domain: "cpu",
		Description: "Substantially more runnable tasks than the machine has CPUs to run them on. " +
			"kernel.all.runnable counts tasks that are RUNNING as well as those waiting, so a healthy " +
			"machine sits near its core count by definition -- the threshold has to clear that baseline " +
			"before it means anything. 3x capacity is a queue nobody is draining.",
		When: []Cond{{Metric: "kernel.all.runnable", BGtePerCPU: f(3)}},
	},
	{
		ID:          "state.cpu.user_high",
		Domain:      "cpu",
		Description: "This guest's own userspace is genuinely busy. Used mainly as a negative condition, to separate 'we are busy' from 'we are being starved'.",
		When:        []Cond{{Metric: "kernel.all.cpu.user", BGteMachineFrac: f(0.50)}},
	},
	{
		ID:          "state.cpu.system_high",
		Domain:      "cpu",
		Description: "Kernel-mode CPU is disproportionately high, which points at syscall or interrupt overhead rather than application work.",
		When:        []Cond{{Metric: "kernel.all.cpu.sys", BGteMachineFrac: f(0.30)}},
	},
	{
		ID:          "state.cpu.iowait_high",
		Domain:      "cpu",
		Description: "CPUs are idle waiting on I/O completion rather than doing work.",
		When:        []Cond{{Metric: "kernel.all.cpu.wait.total", BGteMachineFrac: f(0.20)}},
	},
	{
		ID:          "state.cpu.pressure_high",
		Domain:      "cpu",
		Description: "PSI reports tasks stalling on CPU availability. Already a percentage, so it needs no scaling.",
		When:        []Cond{{Metric: "kernel.all.pressure.cpu.some.avg", BGte: f(10)}},
	},
	{
		ID:     "state.cpu.core_pegged",
		Domain: "cpu",
		Description: "At least one individual core is saturated. Deliberately NOT scale-relative: one pegged core " +
			"is 1000 ms/s on every machine, and expressing it as a fraction of total capacity is exactly what makes " +
			"it invisible on anything bigger than 2 cores. This is the state that catches a single runaway process.",
		When: []Cond{{Metric: MetricBusiestCore, BGte: f(coreSaturatedMsPerSec)}},
	},
	{
		ID:     "state.cpu.serialized",
		Domain: "cpu",
		Description: "A core is pegged while the machine as a whole has capacity to spare -- the workload is " +
			"serialized onto one core and more cores will not help. Both halves matter: without the imbalance " +
			"condition this would also fire on a uniformly busy machine, which is a different problem entirely.",
		When: []Cond{
			{Metric: MetricBusiestCore, BGte: f(coreSaturatedMsPerSec)},
			{Metric: MetricCoreImbalance, BGte: f(400)},
		},
	},
	{
		ID:     "state.cpu.core_peaked",
		Domain: "cpu",
		Description: "A core reached saturation at some point in the window even though the mean did not. This is " +
			"the case an averaged comparison structurally cannot see: 20 saturated minutes out of 60 is 333 ms/s on " +
			"the mean, under every threshold worth setting, while the machine was pegged for a third of the window.",
		When: []Cond{{Metric: MetricBusiestCore, BMaxGte: f(coreSaturatedMsPerSec), MinSamples: 30}},
	},
	{
		ID:     "state.cpu.bursty",
		Domain: "cpu",
		Description: "CPU demand is spiky rather than sustained: the peak is several times the mean. Worth separating " +
			"from steady load because the remedies differ -- a burst wants queueing or rate limiting, sustained load " +
			"wants more capacity.",
		When: []Cond{{
			Metric:          "kernel.all.cpu.user",
			BMaxMachineFrac: f(0.60),
			PeakRatioGte:    f(4),
			MinSamples:      30,
		}},
	},
	{
		ID:     "state.cpu.all_cores_pegged",
		Domain: "cpu",
		Description: "Every core is individually saturated, which is a genuinely different situation from a high " +
			"machine-wide average: there is no headroom anywhere, so even a small new task will queue.",
		When: []Cond{{Metric: MetricCoresBusy, BGtePerCPU: f(1)}},
	},

	{
		ID:     "state.cpu.load_high",
		Domain: "cpu",
		Description: "The load average is several times the core count. Load counts tasks runnable OR blocked on " +
			"uninterruptible I/O, so it is deliberately kept as a separate state from runqueue_high: a high load " +
			"with a short run queue means the tasks are stuck in I/O, not competing for CPU, and the two want " +
			"different investigations.",
		When: []Cond{{Metric: "kernel.all.load", BGtePerCPU: f(4)}},
	},
	{
		ID:     "state.cpu.blocked_high",
		Domain: "cpu",
		Description: "Many tasks are in uninterruptible sleep waiting on I/O. This is the other half of a high load " +
			"average, and the half that points at storage rather than at compute.",
		When: []Cond{{Metric: "kernel.all.blocked", BGtePerCPU: f(2)}},
	},
	{
		ID:     "state.cpu.hardirq_high",
		Domain: "cpu",
		Description: "A substantial share of CPU is spent servicing hardware interrupts, which points at device or " +
			"driver behaviour rather than at anything the workload is asking for.",
		When: []Cond{{Metric: "kernel.all.cpu.irq.hard", BGteMachineFrac: f(0.10)}},
	},
	{
		ID:     "state.cpu.softirq_high",
		Domain: "cpu",
		Description: "Softirq processing is consuming real CPU. On most machines this is network receive processing, " +
			"which is why it pairs with the softnet states to tell a NIC-driven CPU problem from a workload one.",
		When: []Cond{{Metric: "kernel.all.cpu.irq.soft", BGteMachineFrac: f(0.15)}},
	},
	{
		ID:     "state.cpu.context_switch_storm",
		Domain: "cpu",
		Description: "Context switches per core are far above what useful work requires. Scaled per-CPU because the " +
			"metric is a whole-machine count and a bigger machine legitimately switches more. Typically lock " +
			"contention, a thundering herd, or far more runnable threads than cores.",
		When: []Cond{{Metric: "kernel.all.pswitch", BGtePerCPU: f(50000)}},
	},
	{
		ID:     "state.cpu.context_switch_spike",
		Domain: "cpu",
		Description: "Context switching hit storm levels at some point in the window even though the mean did not. " +
			"The counterpart to context_switch_storm for a burst that started partway through the hour: averaging " +
			"an hour-long window dilutes a ten-minute storm below the per-core threshold, and a mean sitting right " +
			"on the line makes the storm state flap in and out between runs. The peak fires the moment the storm " +
			"appears and does not flap. MinSamples guards against one outlier sample reading as a storm.",
		When: []Cond{{Metric: "kernel.all.pswitch", BMaxGtePerCPU: f(50000), MinSamples: 30}},
	},
	{
		ID:     "state.cpu.fork_storm",
		Domain: "cpu",
		Description: "Processes are being created at a high sustained rate -- a fork bomb, a runaway supervisor, or a " +
			"shell loop spawning a command per iteration. Worth naming separately because the CPU cost shows up as " +
			"system time and looks like generic kernel overhead.",
		When: []Cond{{Metric: "kernel.all.sysfork", BGtePerCPU: f(100)}},
	},
	{
		ID:     "state.cpu.fork_spike",
		Domain: "cpu",
		Description: "The fork rate hit storm levels at some point in the window even if the mean did not -- the " +
			"burst equivalent of fork_storm, for a fork bomb or a runaway supervisor that fired briefly. Same peak " +
			"reasoning as context_switch_spike.",
		When: []Cond{{Metric: "kernel.all.sysfork", BMaxGtePerCPU: f(100), MinSamples: 30}},
	},

	// ---- Memory ----
	{
		ID:          "state.mem.available_low",
		Domain:      "memory",
		Description: "Available memory has fallen sharply. Relative by necessity: 'low' depends entirely on machine size, and a fixed byte threshold would be wrong on most hosts.",
		When:        []Cond{{Metric: "mem.util.available", Verdict: "worse", DeltaLte: f(-25)}},
	},
	{
		ID:          "state.mem.swapping",
		Domain:      "memory",
		Description: "Pages are actively being written to swap.",
		When:        []Cond{{Metric: "swap.pagesout", BGte: f(1)}},
	},
	{
		ID:          "state.mem.direct_reclaim",
		Domain:      "memory",
		Description: "Reclaim is happening synchronously in the allocation path, so allocations stall.",
		When:        []Cond{{Metric: "mem.vmstat.pgscan_direct", BGte: f(1)}},
	},
	{
		ID:          "state.mem.pressure_high",
		Domain:      "memory",
		Description: "PSI reports tasks stalling on memory.",
		When:        []Cond{{Metric: "kernel.all.pressure.memory.some.avg", BGte: f(5)}},
	},
	{
		ID:          "state.mem.major_faults_high",
		Domain:      "memory",
		Description: "Major page faults are frequent: pages are being fetched from disk rather than found in memory.",
		When:        []Cond{{Metric: "mem.vmstat.pgmajfault", BGte: f(50)}},
	},

	{
		ID:     "state.mem.available_critical",
		Domain: "memory",
		Description: "Available memory is at an absolute level where the next large allocation is in danger, " +
			"regardless of whether it changed. The change-relative available_low cannot see a machine that has been " +
			"sitting at 200 MB free all week -- that is not a regression, it is a standing risk, and it is exactly " +
			"the state that turns into an OOM kill under any new load.",
		When: []Cond{{Metric: "mem.util.available", BLte: f(524288)}}, // 512 MB, in Kbyte
	},
	{
		ID:     "state.mem.swap_exhausted",
		Domain: "memory",
		Description: "Swap is nearly full, so the escape valve for memory pressure is gone. Combined with active " +
			"swapping this means the next pressure event goes straight to the OOM killer.",
		When: []Cond{{Metric: "swap.free", BLte: f(67108864)}}, // 64 MB, in byte
	},
	{
		ID:     "state.mem.swapping_in",
		Domain: "memory",
		Description: "Pages are being read back FROM swap, which is the half of swap activity that actually costs " +
			"latency. Writing pages out can be housekeeping; reading them back means something is waiting on disk " +
			"for memory it already thought it had.",
		When: []Cond{{Metric: "swap.pagesin", BGte: f(1)}},
	},
	{
		ID:     "state.mem.oom_killing",
		Domain: "memory",
		Description: "The kernel has killed a process to reclaim memory. Never noise: an OOM kill is a completed " +
			"failure, not a warning about one.",
		When: []Cond{{Metric: "mem.vmstat.oom_kill", BGte: f(1)}},
	},
	{
		ID:     "state.mem.kswapd_active",
		Domain: "memory",
		Description: "Background reclaim is running. Distinguished from direct reclaim because kswapd works ahead of " +
			"allocations and costs no latency directly; it means pressure exists but is still being absorbed.",
		When: []Cond{{Metric: "mem.vmstat.pgscan_kswapd", BGte: f(1000)}},
	},
	{
		ID:     "state.mem.alloc_stalling",
		Domain: "memory",
		Description: "Allocations are stalling outright. This is the most direct evidence that memory pressure is " +
			"costing application latency rather than merely existing.",
		When: []Cond{{Metric: "mem.vmstat.allocstall", BGte: f(1)}},
	},
	{
		ID:     "state.mem.compaction_stalling",
		Domain: "memory",
		Description: "Allocations are stalling in memory compaction, which means physically contiguous pages have " +
			"run short even though total free memory may look adequate -- fragmentation rather than exhaustion.",
		When: []Cond{{Metric: "mem.vmstat.compact_stall", BGte: f(1)}},
	},
	{
		ID:     "state.mem.pressure_full",
		Domain: "memory",
		Description: "PSI 'full' means EVERY runnable task was stalled on memory at once, not just some. It is a " +
			"far stronger signal than 'some' and deserves its own name so a diagnosis can require it.",
		When: []Cond{{Metric: "kernel.all.pressure.memory.full.avg", BGte: f(1)}},
	},
	{
		ID:     "state.mem.dirty_high",
		Domain: "memory",
		Description: "A large volume of dirty pages is waiting to be written back. This is where a write-heavy " +
			"workload turns into a latency spike, when writeback finally throttles the writers.",
		When: []Cond{{Metric: "mem.util.dirty", BGte: f(524288)}}, // 512 MB in Kbyte
	},
	{
		ID:     "state.mem.writeback_stuck",
		Domain: "memory",
		Description: "Pages are sitting in writeback, meaning the kernel has handed them to storage and is waiting. " +
			"Sustained writeback with dirty pages piling up behind it is storage failing to keep up, not a memory " +
			"problem as such.",
		When: []Cond{{Metric: "mem.util.writeback", BGte: f(51200)}}, // 50 MB in Kbyte
	},

	// ---- I/O ----
	{
		ID:          "state.io.saturated",
		Domain:      "io",
		Description: "A disk is busy nearly all the time with requests actually queued behind it. Both conditions matter: high utilisation with an empty queue is a disk doing its job.",
		When: []Cond{
			{Metric: "disk.dev.avactive", BGte: f(0.7)},
			{Metric: "disk.dev.aveq", BGte: f(2)},
		},
	},
	{
		ID:          "state.io.pressure_high",
		Domain:      "io",
		Description: "PSI reports tasks stalling on I/O.",
		When:        []Cond{{Metric: "kernel.all.pressure.io.some.avg", BGte: f(10)}},
	},
	{
		ID:          "state.io.write_heavy",
		Domain:      "io",
		Description: "Sustained write throughput, useful for telling a write-driven stall from a read-driven one.",
		When:        []Cond{{Metric: "disk.all.write_bytes", BGte: f(51200)}}, // 50 MB/s
	},

	{
		ID:     "state.io.pressure_full",
		Domain: "io",
		Description: "PSI 'full' for I/O: every runnable task was stalled on storage simultaneously. On a machine " +
			"doing anything else at all this is close to a total stall.",
		When: []Cond{{Metric: "kernel.all.pressure.io.full.avg", BGte: f(5)}},
	},
	{
		ID:     "state.io.queue_deep",
		Domain: "io",
		Description: "Requests are queued deeply on a device. Separate from saturated because a deep queue on a " +
			"device that is NOT busy all the time means slow completions rather than too much work -- a failing " +
			"disk or a throttled cloud volume.",
		When: []Cond{{Metric: "disk.dev.aveq", BGte: f(8)}},
	},
	{
		ID:     "state.io.read_heavy",
		Domain: "io",
		Description: "Sustained read throughput, the counterpart to write_heavy. Which side dominates changes the " +
			"investigation entirely: reads point at a cold cache or a scan, writes at ingest or logging.",
		When: []Cond{{Metric: "disk.all.read_bytes", BGte: f(51200)}}, // 50 MB/s in Kbyte/s
	},
	{
		ID:     "state.io.iops_heavy",
		Domain: "io",
		Description: "High IOPS with the throughput that implies small requests -- a random-access pattern, which is " +
			"what actually exhausts a device's IOPS budget long before its bandwidth.",
		When: []Cond{{Metric: "disk.all.total", BGte: f(5000)}},
	},
	{
		ID:     "state.fs.nearly_full",
		Domain: "filesystem",
		Description: "A filesystem is nearly out of space. Absolute by nature and it does not need to have changed " +
			"to matter: a filesystem that has been at 96% for a month is a scheduled outage, and a pure change-diff " +
			"reports it as flat.",
		When: []Cond{{Metric: "filesys.full", BGte: f(90)}},
	},
	{
		ID:     "state.fs.critically_full",
		Domain: "filesystem",
		Description: "A filesystem is effectively out of space. Separated from nearly_full because the severity is " +
			"different in kind: at this level writes are already failing or about to.",
		When: []Cond{{Metric: "filesys.full", BGte: f(98)}},
	},
	{
		ID:     "state.fs.fd_high",
		Domain: "filesystem",
		Description: "A large number of open file descriptors machine-wide. The failure mode is abrupt -- accept() " +
			"and open() start failing with EMFILE while every performance metric still looks healthy -- so this is " +
			"worth surfacing before it is reached.",
		When: []Cond{{Metric: "vfs.files.count", BGte: f(500000)}},
	},

	// ---- Network ----
	{
		ID:          "state.net.retransmit_high",
		Domain:      "network",
		Description: "TCP is retransmitting at a rate that indicates real packet loss, not the occasional stray retransmit every link has.",
		When:        []Cond{{Metric: "network.tcp.retranssegs", BGte: f(10)}},
	},
	{
		ID:          "state.net.conn_churn_high",
		Domain:      "network",
		Description: "Connections are being opened at a high rate, which stresses the socket table and ephemeral port range.",
		When:        []Cond{{Metric: "network.tcp.activeopens", BGte: f(200)}},
	},
	{
		ID:          "state.net.timewait_high",
		Domain:      "network",
		Description: "A large TIME-WAIT pool, which combined with high churn can exhaust ephemeral ports.",
		When:        []Cond{{Metric: "network.sockstat.tcp.tw", BGte: f(10000)}},
	},
	{
		ID:     "state.net.listen_dropping",
		Domain: "network",
		Description: "The kernel is dropping incoming connections at the listen queue. Unambiguous: connections that " +
			"reached this machine were discarded because the application was not accepting fast enough. Clients see " +
			"this as a timeout, and nothing in a latency percentile explains it.",
		When: []Cond{{Metric: "network.tcp.listendrops", BGte: f(1)}},
	},
	{
		ID:     "state.net.listen_overflow",
		Domain: "network",
		Description: "The accept queue itself overflowed, meaning the backlog is too small or the application is " +
			"stalled in its accept loop. More specific than listendrops and points directly at somaxconn or the " +
			"listen() backlog.",
		When: []Cond{{Metric: "network.tcp.listenoverflows", BGte: f(1)}},
	},
	{
		ID:     "state.net.syn_flood_defense",
		Domain: "network",
		Description: "SYN cookies are being emitted, which the kernel only does once the SYN backlog is full. Either " +
			"a SYN flood or a legitimate connection surge the backlog is too small for.",
		When: []Cond{{Metric: "network.tcp.syncookiessent", BGte: f(1)}},
	},
	{
		ID:     "state.net.reset_storm",
		Domain: "network",
		Description: "This host is sending many RSTs, which usually means connections to closed ports or an " +
			"application closing sockets with data still queued. Distinct from receiving resets, since this is " +
			"behaviour originating here.",
		When: []Cond{{Metric: "network.tcp.outrsts", BGte: f(50)}},
	},
	{
		ID:     "state.net.connect_failing",
		Domain: "network",
		Description: "Outbound connection attempts are failing, so this host is the client of something that is " +
			"down, unreachable, or refusing it. Worth stating plainly because a machine can look perfectly healthy " +
			"while nothing it depends on is reachable.",
		When: []Cond{{Metric: "network.tcp.attemptfails", BGte: f(10)}},
	},
	{
		ID:     "state.net.tcp_timeouts_high",
		Domain: "network",
		Description: "TCP timers are firing: segments went unacknowledged long enough for a retransmission timeout. " +
			"A stronger statement than a raw retransmit count, which includes fast retransmits that cost almost " +
			"nothing.",
		When: []Cond{{Metric: "network.tcp.timeouts", BGte: f(10)}},
	},
	{
		ID:     "state.net.nic_errors",
		Domain: "network",
		Description: "The NIC is reporting frame errors, which is a physical-layer problem -- cable, transceiver, or " +
			"port -- not something any amount of tuning will fix.",
		When: []Cond{{Metric: "network.interface.in.errors", BGte: f(1)}},
	},
	{
		ID:     "state.net.nic_dropping_in",
		Domain: "network",
		Description: "The NIC or its driver is dropping received packets, typically ring-buffer exhaustion when the " +
			"host cannot drain the queue fast enough. Pairs with softirq_high to distinguish a CPU-starved receive " +
			"path from a link problem.",
		When: []Cond{{Metric: "network.interface.in.drops", BGte: f(1)}},
	},
	{
		ID:     "state.net.softnet_dropping",
		Domain: "network",
		Description: "The kernel's softirq network backlog is overflowing, which is unambiguously a receive-side CPU " +
			"problem: packets arrived and were discarded because no CPU processed them in time.",
		When: []Cond{{Metric: "network.softnet.dropped", BGte: f(1)}},
	},
	{
		ID:     "state.net.softnet_squeezed",
		Domain: "network",
		Description: "Softirq processing is exhausting its per-invocation budget, the precursor to outright backlog " +
			"drops. A leading indicator of the same problem softnet_dropping reports after the fact.",
		When: []Cond{{Metric: "network.softnet.time_squeeze", BGte: f(100)}},
	},
	{
		ID:     "state.net.close_wait_leak",
		Domain: "network",
		Description: "Many sockets sit in CLOSE-WAIT, which is an application bug rather than a network condition: " +
			"the peer closed and this side never called close(). Those descriptors are never released, so the machine " +
			"eventually runs out of them.",
		When: []Cond{{Metric: "network.tcpconn.close_wait", BGte: f(500)}},
	},
	{
		ID:     "state.net.orphan_high",
		Domain: "network",
		Description: "Many orphaned sockets: connections whose owning process is gone but which the kernel is still " +
			"draining. Past the orphan limit the kernel resets them outright, which surfaces to peers as unexplained " +
			"connection loss.",
		When: []Cond{{Metric: "network.sockstat.tcp.orphan", BGte: f(1000)}},
	},
	{
		ID:     "state.net.udp_receive_errors",
		Domain: "network",
		Description: "UDP datagrams are being dropped for lack of receive buffer space. Since UDP has no " +
			"retransmission, these are lost permanently -- which for DNS or metrics traffic means silent, " +
			"hard-to-attribute failures elsewhere.",
		When: []Cond{{Metric: "network.udp.recvbuferrors", BGte: f(1)}},
	},
	{
		ID:     "state.net.receive_pruning",
		Domain: "network",
		Description: "The kernel is pruning data from TCP receive queues under memory pressure, which discards data " +
			"already acknowledged to the sender. Rare, and always a sign that socket memory limits are being hit.",
		When: []Cond{{Metric: "network.tcp.prunecalled", BGte: f(1)}},
	},
}
