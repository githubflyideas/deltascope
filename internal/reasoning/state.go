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
	byMetric := map[string][]pcp.DiffRow{}
	for _, r := range rows {
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

// States is the built-in catalog. Kept deliberately small for this first
// pass: enough to prove the chain end-to-end, not a claim of coverage.
// Thresholds are chosen to describe a genuinely notable condition, not
// merely a non-zero one.
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
}
