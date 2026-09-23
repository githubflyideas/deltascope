package reasoning

import (
	"strings"
	"testing"

	"github.com/githubflyideas/deltascope/internal/pcp"
)

// The catalog writes `ethtool <iface>` and `smartctl -a /dev/<dev>` because one
// entry has to serve every host. But the rows that made the state hold name the
// device, so handing the placeholder back to the reader asks them to re-derive
// something we already know -- and on a host with six NICs, to re-derive it by
// guessing. These tests pin the substitution, and just as importantly they pin
// where it must NOT happen.

// A disk with a queue eight deep: the diagnosis's smartctl line must name that
// disk, not a hole for the reader to fill.
func TestNextStepNamesTheDiskTheStateFiredOn(t *testing.T) {
	rows := []pcp.DiffRow{devRow("disk.dev.aveq", "sdb", 12)}
	res := Diagnose(Diagnoses, EvaluateOn(States, rows, Machine{NCPU: 8}))

	var found bool
	for _, r := range res {
		if r.ID != "diagnosis.storage_degraded" {
			continue
		}
		found = true
		joined := strings.Join(r.Next, " | ")
		if strings.Contains(joined, "<dev>") {
			t.Errorf("the placeholder survived into the reader's commands: %s", joined)
		}
		if !strings.Contains(joined, "/dev/sdb") {
			t.Errorf("next steps do not name sdb: %s", joined)
		}
	}
	if !found {
		t.Fatal("fixture no longer fires storage_degraded; the substitution is untested")
	}
}

// Same for a NIC. Collisions on eth1 must produce `ethtool eth1`.
func TestNextStepNamesTheInterfaceTheStateFiredOn(t *testing.T) {
	rows := []pcp.DiffRow{devRow("network.interface.collisions", "eth1", 4)}
	res := Diagnose(Diagnoses, EvaluateOn(States, rows, Machine{NCPU: 8}))

	var found bool
	for _, r := range res {
		if r.ID != "diagnosis.nic_link_fault_physical" {
			continue
		}
		found = true
		joined := strings.Join(r.Next, " | ")
		if strings.Contains(joined, "<iface>") || strings.Contains(joined, "<nic>") {
			t.Errorf("the placeholder survived into the reader's commands: %s", joined)
		}
		if !strings.Contains(joined, "ethtool eth1") {
			t.Errorf("next steps do not name eth1: %s", joined)
		}
	}
	if !found {
		t.Fatal("fixture no longer fires nic_link_fault_physical; the substitution is untested")
	}
}

// The line this must not cross. Two disks in trouble at once is a real
// situation, and naming one of them in a command presents half the answer as
// though it were all of it. The placeholder is the better output there: it
// reads as "you fill this in", which is true.
func TestAnAmbiguousTargetKeepsThePlaceholder(t *testing.T) {
	active := map[string]Active{
		"state.a": {ID: "state.a", Instance: "sdb", InstanceKind: "dev"},
		"state.b": {ID: "state.b", Instance: "sdc", InstanceKind: "dev"},
	}
	got := fillTargets([]string{"smartctl -a /dev/<dev>"}, []string{"state.a", "state.b"}, active)
	if got[0] != "smartctl -a /dev/<dev>" {
		t.Errorf("two candidate disks produced a command naming one of them: %q", got[0])
	}
	// One kind being ambiguous must not block the other: a NIC named
	// unambiguously in the same diagnosis is still filled in.
	active["state.c"] = Active{ID: "state.c", Instance: "eth0", InstanceKind: "iface"}
	got = fillTargets(
		[]string{"smartctl -a /dev/<dev>", "ethtool <iface>"},
		[]string{"state.a", "state.b", "state.c"}, active)
	if got[0] != "smartctl -a /dev/<dev>" || got[1] != "ethtool eth0" {
		t.Errorf("one ambiguous kind spoiled an unambiguous one: %v", got)
	}
}

// A whole-machine state has no instance, so there is nothing to substitute and
// the command must be passed through untouched rather than mangled into
// `ethtool ` with an empty argument.
func TestNoInstanceLeavesTheCommandAlone(t *testing.T) {
	active := map[string]Active{"state.a": {ID: "state.a"}}
	got := fillTargets([]string{"ethtool <iface>"}, []string{"state.a"}, active)
	if got[0] != "ethtool <iface>" {
		t.Errorf("a state with no instance still rewrote the command: %q", got[0])
	}
}

// Diagnoses is a package-level slice shared by every request. Writing this
// run's device name into it would make the next host's report name a disk it
// does not have.
func TestSubstitutionDoesNotWriteBackIntoTheCatalog(t *testing.T) {
	rows := []pcp.DiffRow{devRow("disk.dev.aveq", "sdb", 12)}
	Diagnose(Diagnoses, EvaluateOn(States, rows, Machine{NCPU: 8}))

	for _, d := range Diagnoses {
		if d.ID != "diagnosis.storage_degraded" {
			continue
		}
		if !strings.Contains(strings.Join(d.Next, " "), "<dev>") {
			t.Fatalf("the catalog entry was overwritten with one host's disk: %v", d.Next)
		}
	}
}

// Which instance gets named has to be the same on every run.
//
// The same-instance matcher used to range over a map of candidate instances, so
// when two disks both satisfied a state the winner was whichever one Go's map
// iteration happened to yield. That decided both the evidence the report cites
// and -- now -- the device its commands name, so an unchanged machine produced a
// report blaming sdb on one run and sdc on the next. This is the phantom-change
// failure mode in a different costume, and the fix is the same: a total order.
func TestTheNamedInstanceIsStableAcrossRuns(t *testing.T) {
	rows := []pcp.DiffRow{
		devRow("disk.dev.avactive", "sdc", 0.95),
		devRow("disk.dev.aveq", "sdc", 10),
		devRow("disk.dev.avactive", "sdb", 0.92),
		devRow("disk.dev.aveq", "sdb", 9),
	}
	for i := 0; i < 50; i++ {
		a := EvaluateOn(States, rows, Machine{NCPU: 8})["state.io.saturated"]
		if a.Instance != "sdb" {
			t.Fatalf("run %d named %q; two saturated disks must resolve to the "+
				"first in sort order every time", i, a.Instance)
		}
		if a.InstanceKind != "dev" {
			t.Fatalf("run %d classified sdb as %q, want dev", i, a.InstanceKind)
		}
	}
}

// A mount point is a genuine instance and a genuine fact, but it is not a block
// device: substituting it into `smartctl -a /dev/...` would send the reader
// after /dev//var. Only instances of a kind a command can be pointed at are
// recorded as targets.
func TestOnlyTargetableInstanceKindsAreRecorded(t *testing.T) {
	if k := instanceKind("filesys.full"); k != "" {
		t.Errorf("filesys instances were classified as %q; a mount point is not a device", k)
	}
	if k := instanceKind("disk.dm.aveq"); k != "dev" {
		t.Errorf("a device-mapper instance is still a device, got %q", k)
	}
	if k := instanceKind("kernel.all.cpu.user"); k != "" {
		t.Errorf("a whole-machine metric was given the kind %q", k)
	}
	// Rows disagreeing on the instance yield no target at all.
	mixed := []pcp.DiffRow{
		devRow("disk.dev.aveq", "sdb", 9),
		devRow("disk.dev.avactive", "sdc", 0.9),
	}
	if name, kind := soleInstance(mixed); name != "" || kind != "" {
		t.Errorf("rows on two devices produced the single target %q/%q", name, kind)
	}
}
