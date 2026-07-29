package reasoning

import (
	"strings"
	"testing"

	"github.com/githubflyideas/deltascope/internal/pcp"
)

// Every state must reference a metric the tool actually collects. A state
// whose metric is absent from the catalog is not a conservative state that
// never fires -- it is a silent hole in coverage that looks like coverage,
// and it will be listed as "evaluated, not active" in the UI forever.
func TestEveryStateMetricIsCollectable(t *testing.T) {
	derived := map[string]bool{
		MetricCoreBusy: true, MetricBusiestCore: true,
		MetricCoreImbalance: true, MetricCoresBusy: true,
	}
	for _, st := range States {
		for _, c := range st.When {
			if derived[c.Metric] {
				continue
			}
			if _, ok := pcp.Lookup(c.Metric); !ok {
				t.Errorf("%s references %q, which is in neither the metric catalog nor the derived set",
					st.ID, c.Metric)
			}
		}
	}
}

// Every diagnosis must reference states that exist. A typo in a state name
// silently makes a diagnosis unreachable, and an unreachable diagnosis is
// indistinguishable from a working one that simply hasn't fired yet.
func TestEveryDiagnosisReferencesRealStates(t *testing.T) {
	known := map[string]bool{}
	for _, st := range States {
		known[st.ID] = true
	}
	for _, d := range Diagnoses {
		for _, group := range [][]string{d.RequiresAll, d.RequiresAny, d.RequiresNone} {
			for _, id := range group {
				if !known[id] {
					t.Errorf("%s references unknown state %q", d.ID, id)
				}
			}
		}
	}
}

func TestIDsAreUniqueAndWellFormed(t *testing.T) {
	seen := map[string]bool{}
	for _, st := range States {
		if seen[st.ID] {
			t.Errorf("duplicate state ID %q", st.ID)
		}
		seen[st.ID] = true
		if !strings.HasPrefix(st.ID, "state.") {
			t.Errorf("state ID %q must be namespaced under state.", st.ID)
		}
		if st.Domain == "" {
			t.Errorf("%s has no domain, so it cannot be grouped in the UI", st.ID)
		}
		if len(st.When) == 0 {
			t.Errorf("%s has no conditions and would be unconditionally true", st.ID)
		}
		if st.Description == "" {
			t.Errorf("%s has no description; the threshold's rationale is the point", st.ID)
		}
	}
	seenD := map[string]bool{}
	for _, d := range Diagnoses {
		if seenD[d.ID] {
			t.Errorf("duplicate diagnosis ID %q", d.ID)
		}
		seenD[d.ID] = true
		switch d.Severity {
		case "crit", "warn", "info":
		default:
			t.Errorf("%s has severity %q, which the UI cannot rank", d.ID, d.Severity)
		}
		if d.Conclusion == "" {
			t.Errorf("%s has no conclusion sentence", d.ID)
		}
		if len(d.Next) == 0 {
			t.Errorf("%s suggests no next step; a conclusion nobody can act on is half a diagnosis", d.ID)
		}
		if len(d.RequiresAll) == 0 && len(d.RequiresAny) == 0 {
			t.Errorf("%s has only negative conditions and can never fire", d.ID)
		}
	}
}

// Every condition must actually constrain something. A Cond with no
// threshold fields set matches any row where the metric merely exists,
// which turns its state into "is this metric collected?".
func TestEveryConditionConstrainsSomething(t *testing.T) {
	for _, st := range States {
		for i, c := range st.When {
			constrained := c.BGte != nil || c.BLte != nil || c.BGteCores != nil ||
				c.BGteMachineFrac != nil || c.BGtePerCPU != nil ||
				c.BMaxGte != nil || c.BMaxGteCores != nil || c.BMaxMachineFrac != nil ||
				c.PeakRatioGte != nil || c.DeltaGte != nil || c.DeltaLte != nil ||
				c.Verdict != "" || c.Appeared
			if !constrained {
				t.Errorf("%s condition %d on %q sets no threshold and matches any collected value",
					st.ID, i, c.Metric)
			}
		}
	}
}

// Peak conditions without a sample-count guard read a single outlier sample
// as an event. This is a property of the catalog, not of one state.
func TestPeakConditionsCarryASampleGuard(t *testing.T) {
	for _, st := range States {
		for _, c := range st.When {
			usesPeak := c.BMaxGte != nil || c.BMaxGteCores != nil ||
				c.BMaxMachineFrac != nil || c.PeakRatioGte != nil
			if usesPeak && c.MinSamples <= 0 {
				t.Errorf("%s uses a peak condition on %q with no MinSamples guard, so one outlier "+
					"sample would satisfy it", st.ID, c.Metric)
			}
		}
	}
}

// The guarantee that matters most as the catalog grows: an idle machine
// must produce nothing. Every state added is another chance to invent a
// finding on a healthy host, which is worse than having no states at all.
func TestExpandedCatalogStaysSilentOnAnIdleMachine(t *testing.T) {
	idle := []pcp.DiffRow{
		// CPU: a desktop-ish idle 4-core box
		rowDelta("kernel.all.cpu.user", 60, 70, 16, pcp.VFlat),
		rowDelta("kernel.all.cpu.sys", 30, 33, 10, pcp.VFlat),
		rowDelta("kernel.all.cpu.steal", 0, 0, 0, pcp.VFlat),
		rowDelta("kernel.all.cpu.irq.hard", 2, 3, 50, pcp.VFlat),
		rowDelta("kernel.all.cpu.irq.soft", 8, 9, 12, pcp.VFlat),
		rowDelta("kernel.all.cpu.wait.total", 5, 6, 20, pcp.VFlat),
		rowDelta("kernel.all.load", 0.4, 0.5, 25, pcp.VFlat),
		rowDelta("kernel.all.runnable", 2, 2, 0, pcp.VFlat),
		rowDelta("kernel.all.blocked", 0, 0, 0, pcp.VFlat),
		rowDelta("kernel.all.pswitch", 3000, 3200, 6, pcp.VFlat),
		rowDelta("kernel.all.sysfork", 5, 6, 20, pcp.VFlat),
		rowDelta("kernel.all.pressure.cpu.some.avg", 0.3, 0.4, 33, pcp.VFlat),
		// Memory: 16 GB box with 13 GB available, no swap in use
		rowDelta("mem.util.available", 13_800_000, 13_700_000, -0.7, pcp.VFlat),
		rowDelta("mem.util.dirty", 900, 1100, 22, pcp.VFlat),
		rowDelta("mem.util.writeback", 0, 0, 0, pcp.VFlat),
		rowDelta("swap.free", 2147483648, 2147483648, 0, pcp.VFlat),
		rowDelta("swap.pagesin", 0, 0, 0, pcp.VFlat),
		rowDelta("swap.pagesout", 0, 0, 0, pcp.VFlat),
		rowDelta("mem.vmstat.oom_kill", 0, 0, 0, pcp.VFlat),
		rowDelta("mem.vmstat.pgscan_direct", 0, 0, 0, pcp.VFlat),
		rowDelta("mem.vmstat.pgscan_kswapd", 0, 0, 0, pcp.VFlat),
		rowDelta("mem.vmstat.allocstall", 0, 0, 0, pcp.VFlat),
		rowDelta("mem.vmstat.compact_stall", 0, 0, 0, pcp.VFlat),
		rowDelta("mem.vmstat.pgmajfault", 2, 3, 50, pcp.VFlat),
		rowDelta("kernel.all.pressure.memory.some.avg", 0, 0, 0, pcp.VFlat),
		rowDelta("kernel.all.pressure.memory.full.avg", 0, 0, 0, pcp.VFlat),
		// Disk: the idle-disk noise that produced a false positive once before
		rowDelta("disk.dev.avactive", 0.001, 0.002, 100, pcp.VFlat),
		rowDelta("disk.dev.aveq", 0.003, 0.005, 66, pcp.VFlat),
		rowDelta("disk.all.read_bytes", 40, 55, 37, pcp.VFlat),
		rowDelta("disk.all.write_bytes", 120, 160, 33, pcp.VFlat),
		rowDelta("disk.all.total", 8, 11, 37, pcp.VFlat),
		rowDelta("kernel.all.pressure.io.some.avg", 0.2, 0.3, 50, pcp.VFlat),
		rowDelta("kernel.all.pressure.io.full.avg", 0, 0, 0, pcp.VFlat),
		// Filesystem: comfortable
		rowDelta("filesys.full", 42, 43, 2, pcp.VFlat),
		rowDelta("vfs.files.count", 4000, 4200, 5, pcp.VFlat),
		// Network: a quiet host
		rowDelta("network.tcp.retranssegs", 0, 1, 100, pcp.VFlat),
		rowDelta("network.tcp.timeouts", 0, 0, 0, pcp.VFlat),
		rowDelta("network.tcp.activeopens", 3, 4, 33, pcp.VFlat),
		rowDelta("network.tcp.attemptfails", 0, 0, 0, pcp.VFlat),
		rowDelta("network.tcp.outrsts", 1, 2, 100, pcp.VFlat),
		rowDelta("network.tcp.listendrops", 0, 0, 0, pcp.VFlat),
		rowDelta("network.tcp.listenoverflows", 0, 0, 0, pcp.VFlat),
		rowDelta("network.tcp.syncookiessent", 0, 0, 0, pcp.VFlat),
		rowDelta("network.tcp.prunecalled", 0, 0, 0, pcp.VFlat),
		rowDelta("network.sockstat.tcp.tw", 12, 20, 66, pcp.VFlat),
		rowDelta("network.sockstat.tcp.orphan", 0, 0, 0, pcp.VFlat),
		rowDelta("network.tcpconn.close_wait", 2, 3, 50, pcp.VFlat),
		rowDelta("network.interface.in.errors", 0, 0, 0, pcp.VFlat),
		rowDelta("network.interface.in.drops", 0, 0, 0, pcp.VFlat),
		rowDelta("network.softnet.dropped", 0, 0, 0, pcp.VFlat),
		rowDelta("network.softnet.time_squeeze", 0, 0, 0, pcp.VFlat),
		rowDelta("network.udp.recvbuferrors", 0, 0, 0, pcp.VFlat),
	}
	for _, ncpu := range []int{1, 2, 4, 16, 64} {
		active := EvaluateOn(States, idle, Machine{NCPU: ncpu})
		if len(active) > 0 {
			t.Errorf("ncpu=%d: an idle machine activated %v", ncpu, ActiveIDs(active))
		}
		if got := Diagnose(Diagnoses, active); len(got) > 0 {
			t.Errorf("ncpu=%d: an idle machine was diagnosed with %v", ncpu, diagnosisIDs(got))
		}
	}
}

// Coverage is only meaningful if the states are reachable. Each domain must
// have a scenario that lights it up, or the catalog is a list of things
// that never happen.
func TestEachDomainHasAReachableDiagnosis(t *testing.T) {
	m := Machine{NCPU: 4}
	cases := []struct {
		name string
		rows []pcp.DiffRow
		want string
	}{
		{
			name: "oom kill",
			rows: []pcp.DiffRow{row("mem.vmstat.oom_kill", 1)},
			want: "diagnosis.oom_killing",
		},
		{
			name: "listen queue overflow",
			rows: []pcp.DiffRow{row("network.tcp.listendrops", 4)},
			want: "diagnosis.accept_queue_overflow",
		},
		{
			name: "filesystem full",
			rows: []pcp.DiffRow{row("filesys.full", 99)},
			want: "diagnosis.filesystem_full",
		},
		{
			name: "degraded storage: deep queue, disk not busy",
			rows: []pcp.DiffRow{row("disk.dev.aveq", 20), row("disk.dev.avactive", 0.2)},
			want: "diagnosis.storage_degraded",
		},
		{
			name: "load high but tasks are blocked on I/O",
			rows: []pcp.DiffRow{
				row("kernel.all.load", 20), row("kernel.all.blocked", 12),
				row("kernel.all.runnable", 3),
			},
			want: "diagnosis.load_without_cpu_demand",
		},
		{
			name: "CLOSE-WAIT leak",
			rows: []pcp.DiffRow{row("network.tcpconn.close_wait", 900)},
			want: "diagnosis.socket_descriptor_leak",
		},
		{
			name: "softirq-bound receive path",
			rows: []pcp.DiffRow{
				row("kernel.all.cpu.irq.soft", 900), row("network.softnet.dropped", 5),
			},
			want: "diagnosis.network_receive_cpu_bound",
		},
	}
	for _, tc := range cases {
		active := EvaluateOn(States, tc.rows, m)
		got := diagnosisIDs(Diagnose(Diagnoses, active))
		if !contains(got, tc.want) {
			t.Errorf("%s: expected %s, got %v (active: %v)", tc.name, tc.want, got, ActiveIDs(active))
		}
	}
}

// The weaker form of a paired diagnosis must stay quiet when the stronger
// one fires, or the report states the same problem twice at two different
// severities and the reader has to work out which to believe.
func TestStrongerDiagnosisSuppressesWeakerPair(t *testing.T) {
	m := Machine{NCPU: 4}

	// A filesystem at 99% satisfies both nearly_full and critically_full.
	got := diagnosisIDs(Diagnose(Diagnoses, EvaluateOn(States, []pcp.DiffRow{
		row("filesys.full", 99),
	}, m)))
	if contains(got, "diagnosis.filesystem_filling") {
		t.Errorf("the 'filling' diagnosis must be suppressed at 99%% full, got %v", got)
	}

	// An OOM kill supersedes both the imminent-exhaustion and standing-risk
	// diagnoses: the failure already happened.
	got = diagnosisIDs(Diagnose(Diagnoses, EvaluateOn(States, []pcp.DiffRow{
		row("mem.vmstat.oom_kill", 2),
		row("mem.util.available", 100_000),
		row("swap.free", 1000),
	}, m)))
	if !contains(got, "diagnosis.oom_killing") {
		t.Fatalf("expected oom_killing, got %v", got)
	}
	if contains(got, "diagnosis.memory_exhaustion_imminent") ||
		contains(got, "diagnosis.standing_memory_risk") {
		t.Errorf("an actual OOM kill must supersede the warnings about one, got %v", got)
	}
}
