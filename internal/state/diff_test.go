package state

import "testing"

func snap(items map[string]string) Snapshot {
	var secs []Section
	sec := Section{Name: "sysctl", Title: "Kernel Parameters"}
	for k, v := range items {
		sec.Items = append(sec.Items, Item{Key: k, Value: v})
	}
	secs = append(secs, sec)
	return Snapshot{Sections: secs}
}

func TestCompare(t *testing.T) {
	a := snap(map[string]string{"vm.swappiness": "60", "removed.key": "x", "same": "1"})
	b := snap(map[string]string{"vm.swappiness": "10", "added.key": "y", "same": "1"})
	d := Compare(a, b)
	if d.Total != 3 {
		t.Fatalf("expected 3 changes (mod/add/del), got %d", d.Total)
	}
	kinds := map[ChangeKind]int{}
	for _, sd := range d.Sections {
		for _, ch := range sd.Changes {
			kinds[ch.Kind]++
		}
	}
	if kinds[Modified] != 1 || kinds[Added] != 1 || kinds[Removed] != 1 {
		t.Fatalf("expected 1 each of add/mod/del: %+v", kinds)
	}
}

func TestCompareIdentical(t *testing.T) {
	a := snap(map[string]string{"a": "1", "b": "2"})
	if d := Compare(a, a); d.Total != 0 {
		t.Fatalf("identical snapshots should have no diff, got %d", d.Total)
	}
}

func TestSysctlSkip(t *testing.T) {
	for _, k := range []string{"kernel.random.uuid", "fs.dentry-state", "user.max_user_namespaces", "net.ipv4.ip_forward"} {
		if !sysctlSkip(k) {
			t.Errorf("%s should be skipped", k)
		}
	}
	if sysctlSkip("net.core.somaxconn") {
		t.Error("somaxconn should not be skipped")
	}
}

func TestMarkerRoundTrip(t *testing.T) {
	// Verifies marker logic against in-memory state without a real DB - tests only the Compare semantics.
	base := snap(map[string]string{"vm.swappiness": "60", "net.core.somaxconn": "4096"})
	after := snap(map[string]string{"vm.swappiness": "10", "net.core.somaxconn": "4096"})
	d := Compare(base, after)
	if d.Total != 1 {
		t.Fatalf("release changed only 1 item, impact should be 1, got %d", d.Total)
	}
	if d.Sections[0].Changes[0].Old != "60" || d.Sections[0].Changes[0].New != "10" {
		t.Errorf("impact report value wrong: %+v", d.Sections[0].Changes[0])
	}
}
