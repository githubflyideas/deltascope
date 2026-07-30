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
		ID:       "diagnosis.single_core_saturated",
		Severity: "crit",
		Conclusion: "One CPU core is saturated while the rest of the machine has capacity to spare: a single " +
			"thread or process is the bottleneck, so the machine looks idle in every whole-machine average " +
			"even though something is running as fast as it possibly can and no faster",
		Next:        []string{"top -H", "pidstat -t 1 5", "mpstat -P ALL 1 5", "perf top -p <pid>"},
		RequiresAll: []string{"state.cpu.core_pegged", "state.cpu.serialized"},
		// If every core is pegged this is not a serialization problem, it
		// is a capacity problem, and the diagnosis below is the right one.
		RequiresNone: []string{"state.cpu.all_cores_pegged"},
	},
	{
		ID:       "diagnosis.intermittent_cpu_saturation",
		Severity: "warn",
		Conclusion: "CPU saturation is intermittent: a core reached its limit during this window but the average " +
			"hides it, so the machine looks healthy in every averaged view while anything arriving during the " +
			"spikes was queueing behind a full core",
		Next: []string{
			"mpstat -P ALL 1 60",
			"pidstat 1 60",
			"re-run the comparison over a window of minutes around the spike",
		},
		RequiresAll: []string{"state.cpu.core_peaked"},
		// If the MEAN is saturated too then this is not intermittent, and
		// the sustained diagnoses describe it better.
		RequiresNone: []string{"state.cpu.core_pegged", "state.cpu.all_cores_pegged"},
	},
	{
		ID:       "diagnosis.cpu_capacity_exhausted",
		Severity: "crit",
		Conclusion: "Every core on this machine is individually saturated: there is no CPU headroom left anywhere, " +
			"so any additional work will queue rather than run",
		Next:         []string{"mpstat -P ALL 1 5", "ps aux --sort=-%cpu | head -15", "uptime"},
		RequiresAll:  []string{"state.cpu.all_cores_pegged"},
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

	{
		ID:       "diagnosis.load_without_cpu_demand",
		Severity: "warn",
		Conclusion: "The load average is high but the CPUs are not the constraint: the tasks counted in it are " +
			"blocked in uninterruptible I/O, not queued for compute, so adding CPU would change nothing",
		Next:        []string{"vmstat 1 5", "iostat -x 1 5", "ps -eo state,pid,comm | awk '$1 ~ /D/'"},
		RequiresAll: []string{"state.cpu.load_high", "state.cpu.blocked_high"},
		// If the run queue is genuinely long, the load is about CPU after
		// all and the CPU diagnoses describe it.
		RequiresNone: []string{"state.cpu.runqueue_high"},
	},
	{
		ID:       "diagnosis.interrupt_overhead",
		Severity: "warn",
		Conclusion: "CPU is being consumed servicing interrupts rather than running work, which points at a device, " +
			"a driver, or an interrupt affinity problem rather than at anything the workload is asking for",
		Next:         []string{"cat /proc/interrupts", "mpstat -I SUM -P ALL 1 5", "ethtool -S <nic> | grep -i irq"},
		RequiresAny:  []string{"state.cpu.hardirq_high", "state.cpu.softirq_high"},
		RequiresNone: []string{"state.cpu.user_high"},
	},
	{
		ID:       "diagnosis.network_receive_cpu_bound",
		Severity: "crit",
		Conclusion: "The receive path is CPU-bound: softirq processing cannot keep up with arriving packets and the " +
			"kernel is discarding them, so this is packet loss caused by this machine rather than by the network",
		Next: []string{
			"cat /proc/net/softnet_stat",
			"mpstat -P ALL 1 5",
			"check RPS/RSS queue spreading and NIC IRQ affinity",
		},
		RequiresAll: []string{"state.cpu.softirq_high"},
		RequiresAny: []string{"state.net.softnet_dropping", "state.net.softnet_squeezed", "state.net.nic_dropping_in"},
	},
	{
		ID:       "diagnosis.scheduler_thrashing",
		Severity: "warn",
		Conclusion: "Context switching far exceeds what useful work requires while tasks queue for CPU: threads are " +
			"spending their time being scheduled rather than running, typically lock contention or many more " +
			"runnable threads than cores",
		Next:        []string{"pidstat -w 1 5", "perf sched latency", "top -H"},
		RequiresAll: []string{"state.cpu.context_switch_storm"},
		RequiresAny: []string{"state.cpu.runqueue_high", "state.cpu.pressure_high"},
	},
	{
		ID:       "diagnosis.context_switch_overhead",
		Severity: "warn",
		Conclusion: "Context switching is far above what useful work requires, on its own -- the run queue is not " +
			"backed up, so this is not (yet) costing throughput, but the CPU time spent switching between threads " +
			"is real and worth finding the source of before it does",
		Next:        []string{"pidstat -w 1 5", "perf sched latency", "cat /proc/interrupts"},
		RequiresAll: []string{"state.cpu.context_switch_storm"},
		// The more specific diagnosis above already covers the case where a
		// queue is backed up too; this is deliberately the fallback for
		// "context_switch_storm fired and nothing else did", which used to
		// produce no diagnosis at all -- a state with no possible catch-all
		// is a state that periodically reports "1/N active, no diagnosis"
		// and looks broken even though it correctly detected something.
		RequiresNone: []string{"state.cpu.runqueue_high", "state.cpu.pressure_high"},
	},
	{
		ID:       "diagnosis.context_switch_spike",
		Severity: "warn",
		Conclusion: "Context switching hit storm levels during part of this window, hidden by the hourly average: " +
			"a burst of scheduling activity came and went, and while the mean stays under the line the peak did " +
			"not -- worth finding the source before it becomes sustained",
		Next:        []string{"pidstat -w 1 30", "perf sched latency", "narrow the window to the minutes around the spike"},
		RequiresAll: []string{"state.cpu.context_switch_spike"},
		// The sustained storm states are the stronger statement; when the
		// mean itself is over the line this burst diagnosis is redundant.
		RequiresNone: []string{"state.cpu.context_switch_storm"},
	},
	{
		ID:       "diagnosis.fork_spike",
		Severity: "warn",
		Conclusion: "The process-creation rate hit storm levels during part of this window, hidden by the hourly " +
			"average: a brief fork burst that the mean does not show",
		Next:         []string{"execsnoop", "pidstat 1 30", "narrow the window to the minutes around the spike"},
		RequiresAll:  []string{"state.cpu.fork_spike"},
		RequiresNone: []string{"state.cpu.fork_storm"},
	},
	{
		ID:       "diagnosis.cpu_demand_bursty",
		Severity: "warn",
		Conclusion: "CPU demand is spiky rather than sustained: the peak is several times the mean, so an averaged " +
			"view understates how close the machine came to saturation during the spikes -- queueing or rate " +
			"limiting helps a burst in a way that adding steady-state capacity does not",
		Next:        []string{"mpstat -P ALL 1 30", "pidstat 1 30", "sar -q -f /var/log/sa/saXX"},
		RequiresAll: []string{"state.cpu.bursty"},
	},
	{
		ID:       "diagnosis.process_churn",
		Severity: "warn",
		Conclusion: "Processes are being created at a rate that is itself the workload: the CPU cost appears as " +
			"kernel time and is easily mistaken for generic overhead, but the cause is the fork rate",
		Next:        []string{"execsnoop", "pidstat 1 5", "ps -ef --forest | head -40"},
		RequiresAll: []string{"state.cpu.fork_storm"},
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
		ID:       "diagnosis.background_reclaim_active",
		Severity: "warn",
		Conclusion: "Background reclaim (kswapd) is running well ahead of allocations: memory pressure exists and " +
			"is being absorbed for now, without direct-reclaim stalls or swapping yet -- worth watching before it " +
			"escalates into either",
		Next:        []string{"sar -B 1 5", "free -m", "grep -E 'pgscan_kswapd|pgsteal' /proc/vmstat"},
		RequiresAll: []string{"state.mem.kswapd_active"},
		// The stronger, more specific diagnoses below already cover the
		// case where this has progressed past "being absorbed".
		RequiresNone: []string{"state.mem.direct_reclaim", "state.mem.swapping", "state.mem.alloc_stalling"},
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

	{
		ID:       "diagnosis.oom_killing",
		Severity: "crit",
		Conclusion: "The kernel has killed at least one process to reclaim memory. This is not a warning about a " +
			"future failure -- something has already been terminated, and whatever it was is now missing",
		Next: []string{
			"dmesg -T | grep -i -A5 'killed process'",
			"journalctl -k --since '1 hour ago' | grep -i oom",
			"ps aux --sort=-rss | head -15",
		},
		RequiresAll: []string{"state.mem.oom_killing"},
	},
	{
		ID:       "diagnosis.memory_exhaustion_imminent",
		Severity: "crit",
		Conclusion: "Available memory is critically low and swap has no room left: the escape valve is gone, so the " +
			"next significant allocation goes to the OOM killer rather than to swap",
		Next:        []string{"free -m", "ps aux --sort=-rss | head -15", "cat /proc/meminfo"},
		RequiresAll: []string{"state.mem.available_critical", "state.mem.swap_exhausted"},
		// Already-killing is a stronger and separate statement.
		RequiresNone: []string{"state.mem.oom_killing"},
	},
	{
		ID:       "diagnosis.standing_memory_risk",
		Severity: "warn",
		Conclusion: "Available memory sits at a level where any new load will fail, with no regression to point at: " +
			"this is a standing condition rather than a change, and a comparison against yesterday cannot see it " +
			"because yesterday was the same",
		Next:        []string{"free -m", "ps aux --sort=-rss | head -15", "review the machine's memory sizing"},
		RequiresAll: []string{"state.mem.available_critical"},
		RequiresNone: []string{
			"state.mem.oom_killing", "state.mem.swapping", "state.mem.available_low",
			"state.mem.swap_exhausted",
		},
	},
	{
		ID:       "diagnosis.swap_thrashing",
		Severity: "crit",
		Conclusion: "Pages are being read back from swap, so tasks are waiting on disk for memory they already " +
			"believed they had -- the latency cost of swap, as opposed to the housekeeping cost of merely writing " +
			"pages out",
		Next:        []string{"vmstat 1 5", "sar -W 1 5", "ps aux --sort=-rss | head -15"},
		RequiresAll: []string{"state.mem.swapping_in"},
		RequiresAny: []string{"state.mem.pressure_high", "state.mem.available_low", "state.mem.major_faults_high"},
	},
	{
		ID:       "diagnosis.allocation_stalling",
		Severity: "crit",
		Conclusion: "Allocations are stalling in the kernel: memory pressure is no longer merely present, it is " +
			"costing application latency directly on every allocation that has to wait",
		Next:        []string{"grep -E 'allocstall|pgscan' /proc/vmstat", "sar -B 1 5", "free -m"},
		RequiresAny: []string{"state.mem.alloc_stalling", "state.mem.pressure_full"},
	},
	{
		ID:       "diagnosis.memory_fragmentation",
		Severity: "warn",
		Conclusion: "Allocations are stalling in compaction rather than for lack of memory: contiguous pages have run " +
			"short, which is fragmentation and behaves differently from exhaustion -- free memory can look adequate " +
			"the whole time",
		Next: []string{
			"cat /proc/buddyinfo",
			"grep -E 'compact_' /proc/vmstat",
			"cat /sys/kernel/mm/transparent_hugepage/enabled",
		},
		RequiresAll:  []string{"state.mem.compaction_stalling"},
		RequiresNone: []string{"state.mem.available_critical"},
	},
	{
		ID:       "diagnosis.writeback_backpressure",
		Severity: "warn",
		Conclusion: "Dirty pages are accumulating faster than storage can absorb them, so writers will be throttled " +
			"when the dirty limit is reached -- a latency cliff rather than a gradual slowdown",
		Next: []string{
			"grep -E 'Dirty|Writeback' /proc/meminfo",
			"iostat -x 1 5",
			"sysctl vm.dirty_ratio vm.dirty_background_ratio",
		},
		RequiresAll: []string{"state.mem.dirty_high"},
		RequiresAny: []string{"state.mem.writeback_stuck", "state.io.saturated", "state.io.pressure_high"},
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
		ID:          "diagnosis.write_pressure",
		Severity:    "warn",
		Conclusion:  "Sustained heavy writing is driving I/O stalls: the write path, not reads, is where the pressure is",
		Next:        []string{"iotop -o -a", "iostat -x 1 5", "grep -E 'Dirty|Writeback' /proc/meminfo"},
		RequiresAll: []string{"state.io.write_heavy", "state.io.pressure_high"},
	},

	{
		ID:       "diagnosis.io_total_stall",
		Severity: "crit",
		Conclusion: "PSI reports every runnable task stalled on I/O at once: this is not a slow disk in the " +
			"background, it is the machine as a whole waiting on storage right now",
		Next:        []string{"iostat -x 1 5", "iotop -o", "cat /proc/pressure/io"},
		RequiresAll: []string{"state.io.pressure_full"},
	},
	{
		ID:       "diagnosis.storage_degraded",
		Severity: "crit",
		Conclusion: "A device has requests queued deeply while not being busy enough to justify it: completions are " +
			"slow rather than demand being high, which is a failing device, a throttled cloud volume, or a " +
			"misbehaving network filesystem -- not a workload problem",
		Next: []string{
			"iostat -x 1 5",
			"dmesg -T | tail -50",
			"smartctl -a /dev/<dev>",
			"check the volume's provisioned IOPS and burst balance",
		},
		RequiresAll:  []string{"state.io.queue_deep"},
		RequiresNone: []string{"state.io.saturated"},
	},
	{
		ID:       "diagnosis.read_heavy_stall",
		Severity: "warn",
		Conclusion: "Sustained read throughput is driving I/O stalls: the read path -- a cold cache, a large scan, " +
			"or a backup/restore -- is where the pressure is, not writes",
		Next:        []string{"iotop -o", "iostat -x 1 5", "pidstat -d 1 5"},
		RequiresAll: []string{"state.io.read_heavy"},
		RequiresAny: []string{"state.io.pressure_high", "state.io.saturated", "state.cpu.iowait_high"},
	},
	{
		ID:       "diagnosis.random_io_bound",
		Severity: "warn",
		Conclusion: "The I/O pattern is small and random rather than sequential, which exhausts a device's IOPS " +
			"budget long before its bandwidth -- throughput graphs will look unremarkable while the device is at its " +
			"limit",
		Next:        []string{"iostat -x 1 5", "biolatency", "pidstat -d 1 5"},
		RequiresAll: []string{"state.io.iops_heavy"},
		RequiresAny: []string{"state.io.saturated", "state.io.pressure_high", "state.io.queue_deep"},
	},
	{
		ID:       "diagnosis.filesystem_full",
		Severity: "crit",
		Conclusion: "A filesystem is effectively out of space: writes are failing or about to. Nothing about this is " +
			"a performance question, and no performance metric will report it",
		Next:        []string{"df -h", "du -xh --max-depth=2 / | sort -rh | head -20", "lsof +L1"},
		RequiresAll: []string{"state.fs.critically_full"},
	},
	{
		ID:       "diagnosis.filesystem_filling",
		Severity: "warn",
		Conclusion: "A filesystem is nearly full. This is a standing condition rather than a regression, so a window " +
			"comparison reports it as flat right up until the moment writes start failing",
		Next:        []string{"df -h", "du -xh --max-depth=2 / | sort -rh | head -20"},
		RequiresAll: []string{"state.fs.nearly_full"},
		// The critical form says it better.
		RequiresNone: []string{"state.fs.critically_full"},
	},
	{
		ID:       "diagnosis.fd_exhaustion_risk",
		Severity: "warn",
		Conclusion: "Open file descriptors are accumulating machine-wide. The failure is abrupt when it comes -- " +
			"accept() and open() begin failing while every performance metric still looks healthy",
		Next:        []string{"lsof | wc -l", "sysctl fs.file-nr fs.file-max", "ls /proc/*/fd | wc -l"},
		RequiresAny: []string{"state.fs.fd_high", "state.net.close_wait_leak"},
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
		Next:        []string{"ss -s", "sysctl net.ipv4.ip_local_port_range", "sysctl net.ipv4.tcp_tw_reuse"},
		RequiresAll: []string{"state.net.conn_churn_high", "state.net.timewait_high"},
	},
	{
		ID:       "diagnosis.accept_queue_overflow",
		Severity: "crit",
		Conclusion: "Incoming connections are being dropped at the listen queue: they reached this machine and were " +
			"discarded because the application was not accepting fast enough. Clients experience a timeout, and no " +
			"latency percentile on this host will explain it because the request never became a request",
		Next: []string{
			"ss -ltn",
			"sysctl net.core.somaxconn net.ipv4.tcp_max_syn_backlog",
			"check the application's listen() backlog and accept loop",
		},
		RequiresAny: []string{"state.net.listen_dropping", "state.net.listen_overflow"},
	},
	{
		ID:       "diagnosis.syn_backlog_saturated",
		Severity: "warn",
		Conclusion: "SYN cookies are being emitted, so the SYN backlog filled: either a SYN flood or a legitimate " +
			"connection surge the backlog is too small for. The two look identical here and are told apart by " +
			"whether the sources are plausible",
		Next: []string{
			"ss -s",
			"sysctl net.ipv4.tcp_max_syn_backlog",
			"ss -tn state syn-recv | awk '{print $4}' | cut -d: -f1 | sort | uniq -c | sort -rn | head",
		},
		RequiresAll: []string{"state.net.syn_flood_defense"},
	},
	{
		ID:       "diagnosis.nic_link_problem",
		Severity: "crit",
		Conclusion: "The NIC is reporting frame errors, which is a physical-layer fault -- cable, transceiver, or " +
			"switch port. No amount of kernel or application tuning addresses this",
		Next:        []string{"ip -s link", "ethtool <nic>", "ethtool -S <nic> | grep -i err"},
		RequiresAll: []string{"state.net.nic_errors"},
	},
	{
		ID:       "diagnosis.dependency_unreachable",
		Severity: "warn",
		Conclusion: "Outbound connection attempts from this host are failing, so something it depends on is down, " +
			"unreachable, or refusing connections. This machine can look entirely healthy while nothing it needs " +
			"is answering",
		Next: []string{
			"ss -tn state syn-sent",
			"dmesg -T | tail -30",
			"check DNS resolution and the health of upstream services",
		},
		RequiresAll: []string{"state.net.connect_failing"},
	},
	{
		ID:       "diagnosis.socket_descriptor_leak",
		Severity: "warn",
		Conclusion: "Sockets are accumulating in CLOSE-WAIT: the peer closed and this side never called close(). " +
			"That is an application bug rather than a network condition, and those descriptors are never reclaimed",
		Next: []string{
			"ss -tanp state close-wait | head -20",
			"ss -tan state close-wait | wc -l",
		},
		RequiresAll: []string{"state.net.close_wait_leak"},
	},
	{
		ID:       "diagnosis.udp_datagram_loss",
		Severity: "warn",
		Conclusion: "UDP datagrams are being dropped for lack of receive buffer space. UDP does not retransmit, so " +
			"these are gone for good -- for DNS or metrics traffic that means silent failures that will be " +
			"attributed to something else entirely",
		Next:        []string{"netstat -su", "ss -uan", "sysctl net.core.rmem_max net.core.rmem_default"},
		RequiresAll: []string{"state.net.udp_receive_errors"},
	},
	{
		ID:       "diagnosis.socket_memory_pressure",
		Severity: "crit",
		Conclusion: "The kernel is pruning TCP receive queues, discarding data it had already acknowledged to the " +
			"sender. Socket memory limits are being hit, and the peer has no way to know its data was dropped",
		Next: []string{
			"sysctl net.ipv4.tcp_mem net.core.rmem_max",
			"netstat -s | grep -i prune",
			"ss -tm | head -20",
		},
		RequiresAll: []string{"state.net.receive_pruning"},
	},
	{
		ID:       "diagnosis.connection_reset_storm",
		Severity: "warn",
		Conclusion: "This host is sending an unusually high rate of TCP resets: connections to closed ports, or an " +
			"application closing sockets with data still queued -- either way, peers are seeing this machine reset " +
			"connections rather than close them cleanly",
		Next:        []string{"ss -s", "tcpdump -c 200 'tcp[tcpflags] & tcp-rst != 0'", "check for connections to unlisted ports"},
		RequiresAll: []string{"state.net.reset_storm"},
	},
	{
		ID:       "diagnosis.orphan_socket_buildup",
		Severity: "warn",
		Conclusion: "A large number of orphaned sockets are being drained: their owning processes are gone, and " +
			"past the orphan limit the kernel resets them outright -- which peers experience as unexplained " +
			"connection loss rather than a clean close",
		Next:        []string{"ss -tan | grep -c ORPHAN", "sysctl net.ipv4.tcp_max_orphans", "ss -tanp | head -30"},
		RequiresAll: []string{"state.net.orphan_high"},
	},
	{
		ID:       "diagnosis.tcp_path_degraded",
		Severity: "crit",
		Conclusion: "TCP retransmission timers are firing, not just fast retransmits: segments are going " +
			"unacknowledged long enough to stall the connections carrying them. Each timeout costs at least a " +
			"retransmission timeout of latency, which no application tuning recovers",
		Next:        []string{"ss -ti | grep -B1 -i retrans", "mtr -rw <peer>", "netstat -s | grep -i timeout"},
		RequiresAll: []string{"state.net.tcp_timeouts_high"},
		RequiresAny: []string{"state.net.retransmit_high", "state.net.nic_dropping_in"},
	},
}
