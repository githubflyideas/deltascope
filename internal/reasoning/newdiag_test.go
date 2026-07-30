package reasoning

import (
	"testing"

	"github.com/githubflyideas/deltascope/internal/pcp"
)

// Each newly added diagnosis must be reachable from a plausible metric
// scenario -- a diagnosis nothing can trigger is dead weight that inflates
// the count without adding coverage.
func TestNewDiagnosesAreReachable(t *testing.T) {
	m := Machine{NCPU: 4}
	cases := []struct {
		name string
		rows []pcp.DiffRow
		want string
	}{
		{"nic collisions", []pcp.DiffRow{row("network.interface.collisions", 5)}, "diagnosis.nic_link_fault_physical"},
		{"tx errors", []pcp.DiffRow{row("network.interface.out.errors", 3)}, "diagnosis.nic_link_fault_physical"},
		{"tx queue drops", []pcp.DiffRow{row("network.interface.out.drops", 4)}, "diagnosis.tx_queue_overrun"},
		{"ip discards", []pcp.DiffRow{row("network.ip.indiscards", 3)}, "diagnosis.ip_layer_loss"},
		{"ip header errors", []pcp.DiffRow{row("network.ip.inhdrerrors", 3)}, "diagnosis.ip_layer_loss"},
		{"reassembly fail", []pcp.DiffRow{row("network.ip.reasmfails", 3)}, "diagnosis.fragmentation_loss"},
		{"udp send buffer", []pcp.DiffRow{row("network.udp.sndbuferrors", 3)}, "diagnosis.udp_send_buffer_full"},
		{"syn-recv pile", []pcp.DiffRow{row("network.tcpconn.syn_recv", 80)}, "diagnosis.syn_flood_or_surge"},
		{"icmp errors in", []pcp.DiffRow{row("network.icmp.inerrors", 10)}, "diagnosis.path_unreachable_icmp"},
		{"receive collapse", []pcp.DiffRow{row("network.tcp.rcvcollapsed", 2)}, "diagnosis.socket_memory_collapse"},
		{"disk filling", []pcp.DiffRow{
			rowDelta("filesys.free", 100000, 60000, -40, pcp.VWorse),
		}, "diagnosis.disk_filling_fast"},
	}
	for _, tc := range cases {
		active := EvaluateOn(States, tc.rows, m)
		got := diagnosisIDs(Diagnose(Diagnoses, active))
		if !contains(got, tc.want) {
			t.Errorf("%s: want %s, got %v (active %v)", tc.name, tc.want, got, ActiveIDs(active))
		}
	}
}

// syn_flood_defense (cookies emitted) must suppress the weaker syn_recv
// diagnosis: the specific statement wins.
func TestSynCookiesSuppressesSynRecvDiagnosis(t *testing.T) {
	m := Machine{NCPU: 4}
	rows := []pcp.DiffRow{
		row("network.tcpconn.syn_recv", 200),
		row("network.tcp.syncookiessent", 50),
	}
	got := diagnosisIDs(Diagnose(Diagnoses, EvaluateOn(States, rows, m)))
	if !contains(got, "diagnosis.syn_backlog_saturated") {
		t.Errorf("cookies-sent should diagnose syn_backlog_saturated, got %v", got)
	}
	if contains(got, "diagnosis.syn_flood_or_surge") {
		t.Errorf("the weaker syn_recv diagnosis must be suppressed when cookies fired, got %v", got)
	}
}

// receive_pruning (data discarded) must suppress the collapse diagnosis.
func TestPruningSuppressesCollapse(t *testing.T) {
	m := Machine{NCPU: 4}
	rows := []pcp.DiffRow{
		row("network.tcp.prunecalled", 2),
		row("network.tcp.rcvcollapsed", 5),
	}
	got := diagnosisIDs(Diagnose(Diagnoses, EvaluateOn(States, rows, m)))
	if !contains(got, "diagnosis.socket_memory_pressure") {
		t.Errorf("pruning should diagnose socket_memory_pressure, got %v", got)
	}
	if contains(got, "diagnosis.socket_memory_collapse") {
		t.Errorf("collapse must be suppressed when pruning is already happening, got %v", got)
	}
}

// The count guarantee for the "expand to ~60 diagnoses" goal.
func TestCatalogSize(t *testing.T) {
	if len(Diagnoses) < 55 {
		t.Errorf("expected ~60 diagnoses, have %d", len(Diagnoses))
	}
	if len(States) < 75 {
		t.Errorf("expected ~78 states, have %d", len(States))
	}
	t.Logf("%d states, %d diagnoses", len(States), len(Diagnoses))
}
