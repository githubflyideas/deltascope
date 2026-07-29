package reasoning

import "sort"

// Diagnosis is a conclusion expressed as a combination of named states,
// rather than as a set of raw metric conditions.
//
// The key addition over the existing flat rule engine is RequiresNone:
// being able to say "this holds ONLY IF that other state does not". That
// negation is what separates diagnoses that share the same positive
// signals. Hypervisor CPU contention and an ordinary busy guest both show
// a high run queue; what distinguishes them is that under contention the
// guest's own userspace is NOT the thing consuming the CPU.
type Diagnosis struct {
	ID       string `json:"id"`
	Severity string `json:"severity"` // crit | warn | info
	// Conclusion is a plain-language sentence, in the same voice as the
	// existing rule engine's conclusions.
	Conclusion string `json:"conclusion"`
	// Next lists commands that would confirm or refute the diagnosis.
	Next []string `json:"next,omitempty"`

	// RequiresAll: every listed state must be active.
	RequiresAll []string `json:"requires_all,omitempty"`
	// RequiresAny: at least one listed state must be active. Empty means
	// this clause is not used (not "none required").
	RequiresAny []string `json:"requires_any,omitempty"`
	// RequiresNone: none of the listed states may be active.
	RequiresNone []string `json:"requires_none,omitempty"`
}

// Result is a diagnosis that fired, carrying the states that triggered it
// and the underlying metric evidence, so the chain stays inspectable:
// metric → state → diagnosis, each step visible.
type Result struct {
	ID         string   `json:"id"`
	Severity   string   `json:"severity"`
	Conclusion string   `json:"conclusion"`
	Next       []string `json:"next,omitempty"`
	// States lists the state IDs that satisfied this diagnosis.
	States []string `json:"states"`
	// Evidence is the deduplicated metric-level evidence from those states.
	Evidence []string `json:"evidence"`
}

// Diagnose evaluates every diagnosis against the active state set.
//
// Results are ordered by severity (crit, warn, info) then by ID, so
// output is stable across runs -- important because these results feed a
// UI and a JSON export that people diff against each other.
func Diagnose(diagnoses []Diagnosis, active map[string]Active) []Result {
	var out []Result
	for _, d := range diagnoses {
		states, ok := match(d, active)
		if !ok {
			continue
		}
		out = append(out, Result{
			ID: d.ID, Severity: d.Severity, Conclusion: d.Conclusion,
			Next: d.Next, States: states, Evidence: collectEvidence(states, active),
		})
	}
	sevRank := map[string]int{"crit": 0, "warn": 1, "info": 2}
	sort.SliceStable(out, func(i, j int) bool {
		ri, rj := sevRank[out[i].Severity], sevRank[out[j].Severity]
		if ri != rj {
			return ri < rj
		}
		return out[i].ID < out[j].ID
	})
	return out
}

// match reports whether a diagnosis holds, and which states satisfied it.
// A diagnosis with no positive requirement at all never fires: an
// all-negative definition would be true on a healthy machine, which is
// the opposite of useful.
func match(d Diagnosis, active map[string]Active) ([]string, bool) {
	if len(d.RequiresAll) == 0 && len(d.RequiresAny) == 0 {
		return nil, false
	}
	var satisfied []string
	for _, id := range d.RequiresAll {
		if _, on := active[id]; !on {
			return nil, false
		}
		satisfied = append(satisfied, id)
	}
	if len(d.RequiresAny) > 0 {
		anyHit := false
		for _, id := range d.RequiresAny {
			if _, on := active[id]; on {
				satisfied = append(satisfied, id)
				anyHit = true
			}
		}
		if !anyHit {
			return nil, false
		}
	}
	for _, id := range d.RequiresNone {
		if _, on := active[id]; on {
			return nil, false
		}
	}
	return satisfied, true
}

func collectEvidence(states []string, active map[string]Active) []string {
	seen := map[string]bool{}
	var out []string
	for _, id := range states {
		for _, e := range active[id].Evidence {
			if !seen[e] {
				seen[e] = true
				out = append(out, e)
			}
		}
	}
	return out
}

// Diagnoses is the built-in catalog. Small on purpose for this first
// pass -- the point is to prove the state→diagnosis indirection works,
// not to claim broad coverage yet.
var Diagnoses = []Diagnosis{
	// ---- CPU ----
	{
		ID:       "diagnosis.vm_cpu_contention",
		Severity: "crit",
		Conclusion: "This VM is being starved of CPU by the hypervisor: steal time is high and tasks are queuing, " +
			"but this guest's own userspace is not the thing consuming the CPU — the contention is outside this machine",
		Next: []string{"mpstat -P ALL 1 5", "vmstat 1 5", "check the hypervisor host's overcommit ratio"},
		// The negative condition is the whole point: without it this would
		// also fire on a guest that is simply busy with its own work.
		RequiresAll:  []string{"state.cpu.steal_high"},
		RequiresAny:  []string{"state.cpu.runqueue_high", "state.cpu.pressure_high"},
		RequiresNone: []string{"state.cpu.user_high"},
	},
	{
		ID:       "diagnosis.cpu_saturated_own_workload",
		Severity: "warn",
		Conclusion: "This machine's own workload is saturating the CPU: userspace is consuming most of the machine's " +
			"capacity and tasks are queuing, with no meaningful steal time — the load originates here",
		Next:         []string{"pidstat 1 5", "top -H", "ps aux --sort=-%cpu | head -15"},
		RequiresAll:  []string{"state.cpu.user_high", "state.cpu.runqueue_high"},
		RequiresNone: []string{"state.cpu.steal_high"},
	},
	{
		ID:       "diagnosis.kernel_cpu_overhead",
		Severity: "warn",
		Conclusion: "An unusual share of CPU is being spent in the kernel rather than in application code, which " +
			"points at syscall volume, interrupt load, or contention inside the kernel rather than at the workload itself",
		Next:         []string{"pidstat 1 5", "perf top", "cat /proc/interrupts"},
		RequiresAll:  []string{"state.cpu.system_high"},
		RequiresNone: []string{"state.cpu.user_high"},
	},

	// ---- Memory ----
	{
		ID:       "diagnosis.memory_pressure_swapping",
		Severity: "crit",
		Conclusion: "Memory pressure has triggered swapping: available memory is falling while pages are being " +
			"written to swap, so tasks will stall on swap I/O",
		Next:        []string{"free -m", "ps aux --sort=-rss | head -15", "sar -B 1 5"},
		RequiresAll: []string{"state.mem.available_low", "state.mem.swapping"},
	},
	{
		ID:       "diagnosis.memory_reclaim_pressure",
		Severity: "warn",
		Conclusion: "The kernel is reclaiming memory synchronously in the allocation path: allocations are " +
			"stalling to free pages, which shows up as latency without necessarily showing up as swap",
		Next:        []string{"sar -B 1 5", "grep -E 'pgscan|allocstall' /proc/vmstat", "free -m"},
		RequiresAll: []string{"state.mem.direct_reclaim"},
		// Swapping has its own, more specific diagnosis above; suppress
		// this one there so the report doesn't say the same thing twice.
		RequiresNone: []string{"state.mem.swapping"},
	},
	{
		ID:       "diagnosis.thrashing",
		Severity: "crit",
		Conclusion: "The machine is thrashing: major page faults are frequent and memory is under pressure, so the " +
			"working set no longer fits in RAM and pages are being fetched back from disk as fast as they are evicted",
		Next:        []string{"free -m", "vmstat 1 5", "ps aux --sort=-rss | head -15"},
		RequiresAll: []string{"state.mem.major_faults_high"},
		RequiresAny: []string{"state.mem.pressure_high", "state.mem.swapping", "state.mem.available_low"},
	},

	// ---- I/O ----
	{
		ID:       "diagnosis.io_bound",
		Severity: "crit",
		Conclusion: "Storage is the bottleneck: a disk is busy nearly all the time with requests queued behind it, " +
			"and tasks are stalling on I/O",
		Next:        []string{"iostat -x 1 5", "pidstat -d 1 5", "iotop -o"},
		RequiresAll: []string{"state.io.saturated"},
		RequiresAny: []string{"state.io.pressure_high", "state.cpu.iowait_high"},
	},
	{
		ID:       "diagnosis.io_slow_not_busy",
		Severity: "warn",
		Conclusion: "Tasks are stalling on I/O even though no disk is saturated: the storage is responding slowly " +
			"rather than being overloaded — typical of a degraded device, a throttled cloud volume, or a network filesystem",
		Next:        []string{"iostat -x 1 5", "dmesg -T | tail -50", "check cloud volume IOPS/throughput limits"},
		RequiresAll: []string{"state.io.pressure_high"},
		// The distinction from io_bound above is precisely this absence.
		RequiresNone: []string{"state.io.saturated"},
	},
	{
		ID:       "diagnosis.write_pressure",
		Severity: "warn",
		Conclusion: "Sustained heavy writing is driving I/O stalls: the write path, not reads, is where the pressure is",
		Next:        []string{"iotop -o -a", "iostat -x 1 5", "grep -E 'Dirty|Writeback' /proc/meminfo"},
		RequiresAll: []string{"state.io.write_heavy", "state.io.pressure_high"},
	},

	// ---- Network ----
	{
		ID:       "diagnosis.network_loss",
		Severity: "warn",
		Conclusion: "TCP is retransmitting heavily, which means real packet loss between this host and its peers — " +
			"link quality, an overloaded middlebox, or a congested path rather than anything on this machine",
		Next:        []string{"ss -ti | grep -B1 retrans", "mtr -rw <peer>", "check NIC errors and drops"},
		RequiresAll: []string{"state.net.retransmit_high"},
	},
	{
		ID:       "diagnosis.port_exhaustion_risk",
		Severity: "warn",
		Conclusion: "High connection churn combined with a large TIME-WAIT pool: the ephemeral port range is at risk " +
			"of exhaustion, which surfaces as intermittent connection failures rather than as slowness",
		Next: []string{"ss -s", "sysctl net.ipv4.ip_local_port_range", "sysctl net.ipv4.tcp_tw_reuse"},
		RequiresAll: []string{"state.net.conn_churn_high", "state.net.timewait_high"},
	},
}
