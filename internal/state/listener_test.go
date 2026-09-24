package state

import (
	"testing"
	"time"
)

// The bug these tests pin, in the reader's words: the change report found a new
// listening port and named the process holding it, the reader turned to process
// accounting to see what that process costs, and process accounting did not
// mention it at all. Two engines looking at the same machine, one minute apart,
// appeared to contradict each other.
//
// The cause was that both of process accounting's gates rank a PRESENCE change
// by RESOURCE WEIGHT, and a service's significance has nothing to do with its
// weight. A 12 MB Go daemon holding port 9000 clears neither the 25%-of-a-core
// nor the 256 MB bar, so it became PVFlat and was filtered out of every
// renderer.

// listenSnap is procSnap plus the listen section that every capture takes
// beside the process section. owners maps "proto local" to the owning comm,
// which is exactly the shape collect_network.go stores.
func listenSnap(t time.Time, procs map[string]string, owners map[string]string) Snapshot {
	s := procSnap(t, procs)
	sec := Section{Name: "listen", Title: "Listening Ports", PrivSensitive: true}
	for k, v := range owners {
		sec.Items = append(sec.Items, Item{Key: k, Value: v, Note: v})
	}
	s.Sections = append(s.Sections, sec)
	return s
}

var (
	t0 = time.Date(2026, 9, 24, 10, 0, 0, 0, time.UTC)
	t1 = t0.Add(10 * time.Minute)
	t2 = t0.Add(24 * time.Hour)
	t3 = t2.Add(10 * time.Minute)
)

// A 12 MB daemon that consumed no measurable CPU, holding a port that was not
// held yesterday. This is the exact row that went missing.
func TestASmallNewServiceIsReportedNotFilteredAway(t *testing.T) {
	a1 := listenSnap(t0, map[string]string{"sshd": item(100, 8000, 1000, 1)}, map[string]string{"tcp *:22": "sshd"})
	a2 := listenSnap(t1, map[string]string{"sshd": item(100, 8000, 1000, 1)}, map[string]string{"tcp *:22": "sshd"})
	b1 := listenSnap(t2, map[string]string{
		"sshd":            item(200, 8000, 1000, 1),
		"smoketrail-linu": item(3, 12288, 9000000, 1),
	}, map[string]string{"tcp *:22": "sshd", "tcp *:9000": "smoketrail-linu"})
	b2 := listenSnap(t3, map[string]string{
		"sshd":            item(200, 8000, 1000, 1),
		"smoketrail-linu": item(5, 12288, 9000000, 1),
	}, map[string]string{"tcp *:22": "sshd", "tcp *:9000": "smoketrail-linu"})

	d := CompareProcesses(a1, a2, b1, b2, 30, 1, 10240)
	row := findRow(d, "smoketrail-linu")
	if row == nil {
		t.Fatal("the new listening process has no row at all")
	}
	if row.Verdict != PVAppeared {
		t.Errorf("a new service holding tcp *:9000 was judged %q; the config engine "+
			"reported the same process as a new listener in the same window", row.Verdict)
	}
	if !row.Listens {
		t.Error("Listens is false on a row taken from the listen section: the UI has "+
			"nothing to explain why a 12 MB idle process is in the report")
	}
	if len(d.UnwatchedListeners) != 0 {
		t.Errorf("a listener that IS in process accounting was also named as unwatched: %v", d.UnwatchedListeners)
	}
}

// The symmetric direction. A service that stopped is the more urgent of the two.
func TestASmallServiceLeavingIsReported(t *testing.T) {
	a1 := listenSnap(t0, map[string]string{"smoketrail-linu": item(3, 12288, 9000000, 1)},
		map[string]string{"tcp *:9000": "smoketrail-linu"})
	a2 := listenSnap(t1, map[string]string{"smoketrail-linu": item(5, 12288, 9000000, 1)},
		map[string]string{"tcp *:9000": "smoketrail-linu"})
	b1 := listenSnap(t2, map[string]string{"sshd": item(200, 8000, 1000, 1)}, map[string]string{"tcp *:22": "sshd"})
	b2 := listenSnap(t3, map[string]string{"sshd": item(200, 8000, 1000, 1)}, map[string]string{"tcp *:22": "sshd"})

	d := CompareProcesses(a1, a2, b1, b2, 30, 1, 10240)
	row := findRow(d, "smoketrail-linu")
	if row == nil || row.Verdict != PVGone {
		t.Fatalf("a service that stopped serving is %v", row)
	}
	// And it must not be able to veto a recovery headline: 12 MB stopping
	// explains no fall in a machine-level metric. internal/diagnose asks this
	// question through SubstantialInA.
	if row.SubstantialInA() {
		t.Error("a 12 MB no-CPU process counts as substantial; the recovery veto would " +
			"fire on it and silence legitimate improvement claims")
	}
}

// The de-noising the weight bar was built for has to survive. Without a socket,
// a process of the same size is still churn -- this is the same 12 MB, and the
// only difference is that nothing else in the snapshot mentions it.
func TestTheSameSizeWithoutASocketIsStillFlat(t *testing.T) {
	a1 := listenSnap(t0, map[string]string{"sshd": item(100, 8000, 1000, 1)}, map[string]string{"tcp *:22": "sshd"})
	a2 := listenSnap(t1, map[string]string{"sshd": item(100, 8000, 1000, 1)}, map[string]string{"tcp *:22": "sshd"})
	b1 := listenSnap(t2, map[string]string{
		"sshd":         item(200, 8000, 1000, 1),
		"goa-identity": item(3, 12288, 9000000, 1),
	}, map[string]string{"tcp *:22": "sshd"})
	b2 := listenSnap(t3, map[string]string{
		"sshd":         item(200, 8000, 1000, 1),
		"goa-identity": item(5, 12288, 9000000, 1),
	}, map[string]string{"tcp *:22": "sshd"})

	d := CompareProcesses(a1, a2, b1, b2, 30, 1, 10240)
	row := findRow(d, "goa-identity")
	if row == nil || row.Verdict != PVFlat {
		t.Fatalf("a transient helper of the same size is %v; the listening-socket "+
			"qualifier has widened into the churn the weight bar exists to suppress", row)
	}
	if row.Listens {
		t.Error("Listens is true for a process no listen item names")
	}
}

// An unprivileged `ss` lists the socket and omits the owner. Keeping "" as an
// owner would match nothing useful, and match it eagerly -- in a map lookup it
// is a real key, and any process whose name were empty would inherit it.
func TestAnOwnerlessSocketDoesNotQualifyEverything(t *testing.T) {
	a1 := listenSnap(t0, map[string]string{"sshd": item(100, 8000, 1000, 1)}, map[string]string{"tcp *:22": ""})
	a2 := listenSnap(t1, map[string]string{"sshd": item(100, 8000, 1000, 1)}, map[string]string{"tcp *:22": ""})
	b1 := listenSnap(t2, map[string]string{
		"sshd":         item(200, 8000, 1000, 1),
		"goa-identity": item(3, 12288, 9000000, 1),
	}, map[string]string{"tcp *:22": "", "tcp *:9000": ""})
	b2 := listenSnap(t3, map[string]string{
		"sshd":         item(200, 8000, 1000, 1),
		"goa-identity": item(5, 12288, 9000000, 1),
	}, map[string]string{"tcp *:22": "", "tcp *:9000": ""})

	d := CompareProcesses(a1, a2, b1, b2, 30, 1, 10240)
	if row := findRow(d, "goa-identity"); row == nil || row.Verdict != PVFlat {
		t.Fatalf("an ss run with no privilege to name owners upgraded an unrelated process: %v", row)
	}
	if len(d.UnwatchedListeners) != 0 {
		t.Errorf("an empty owner was reported as an unwatched listener: %v", d.UnwatchedListeners)
	}
}

// A skipped listen section (no `ss` on the host) must leave the verdict exactly
// where it was. Same fixture as the appeared case, minus the evidence.
func TestASkippedListenSectionChangesNothing(t *testing.T) {
	skip := func(t time.Time, procs map[string]string) Snapshot {
		s := procSnap(t, procs)
		s.Sections = append(s.Sections, Section{Name: "listen", Title: "Listening Ports",
			PrivSensitive: true, Skipped: "ss not found",
			Items: []Item{{Key: "tcp *:9000", Value: "smoketrail-linu"}}})
		return s
	}
	a1 := skip(t0, map[string]string{"sshd": item(100, 8000, 1000, 1)})
	a2 := skip(t1, map[string]string{"sshd": item(100, 8000, 1000, 1)})
	b1 := skip(t2, map[string]string{"sshd": item(200, 8000, 1000, 1), "smoketrail-linu": item(3, 12288, 9000000, 1)})
	b2 := skip(t3, map[string]string{"sshd": item(200, 8000, 1000, 1), "smoketrail-linu": item(5, 12288, 9000000, 1)})

	d := CompareProcesses(a1, a2, b1, b2, 30, 1, 10240)
	if row := findRow(d, "smoketrail-linu"); row == nil || row.Verdict != PVFlat {
		t.Fatalf("a skipped section's items were read as evidence: %v", row)
	}
	if len(d.UnwatchedListeners) != 0 {
		t.Errorf("a skipped section produced unwatched listeners: %v", d.UnwatchedListeners)
	}
}

// The residual gap, stated. The process collector records a service whitelist
// plus the heaviest procTopN by weight, so a small new daemon may not be in the
// section at all -- and then there is no row to upgrade. Saying so is the
// difference between an explained absence and two engines contradicting each
// other.
func TestAListenerMissingFromProcessAccountingIsNamed(t *testing.T) {
	a1 := listenSnap(t0, map[string]string{"sshd": item(100, 8000, 1000, 1)}, map[string]string{"tcp *:22": "sshd"})
	a2 := listenSnap(t1, map[string]string{"sshd": item(100, 8000, 1000, 1)}, map[string]string{"tcp *:22": "sshd"})
	b1 := listenSnap(t2, map[string]string{"sshd": item(200, 8000, 1000, 1)},
		map[string]string{"tcp *:22": "sshd", "tcp *:9000": "smoketrail-linu"})
	b2 := listenSnap(t3, map[string]string{"sshd": item(200, 8000, 1000, 1)},
		map[string]string{"tcp *:22": "sshd", "tcp *:9000": "smoketrail-linu"})

	d := CompareProcesses(a1, a2, b1, b2, 30, 1, 10240)
	if len(d.UnwatchedListeners) != 1 || d.UnwatchedListeners[0] != "smoketrail-linu" {
		t.Errorf("a listener absent from process accounting was silently absent here too: %v",
			d.UnwatchedListeners)
	}
	if findRow(d, "smoketrail-linu") != nil {
		t.Error("a row was invented for a process the section does not contain")
	}
}

// procWhitelist membership must not qualify on its own. The list holds generic
// runtimes whose one-off invocations are precisely the churn the weight bar
// suppresses: `python3 -c ...` from a cron job is not a service arriving.
func TestWhitelistMembershipAloneDoesNotQualify(t *testing.T) {
	a1 := listenSnap(t0, map[string]string{"sshd": item(100, 8000, 1000, 1)}, map[string]string{"tcp *:22": "sshd"})
	a2 := listenSnap(t1, map[string]string{"sshd": item(100, 8000, 1000, 1)}, map[string]string{"tcp *:22": "sshd"})
	b1 := listenSnap(t2, map[string]string{
		"sshd":    item(200, 8000, 1000, 1),
		"python3": item(3, 12288, 9000000, 1),
	}, map[string]string{"tcp *:22": "sshd"})
	b2 := listenSnap(t3, map[string]string{
		"sshd":    item(200, 8000, 1000, 1),
		"python3": item(5, 12288, 9000000, 1),
	}, map[string]string{"tcp *:22": "sshd"})

	d := CompareProcesses(a1, a2, b1, b2, 30, 1, 10240)
	if row := findRow(d, "python3"); row == nil || row.Verdict != PVFlat {
		t.Fatalf("a short-lived whitelisted runtime with no socket is %v, want flat", row)
	}
}

// A snapshot pair with no listen section at all is what every stored snapshot
// taken before this section existed looks like, and what the other process
// tests in this package construct. The weight bar has to remain the whole rule
// there.
func TestNoListenSectionLeavesTheWeightBarInCharge(t *testing.T) {
	a1 := procSnap(t0, map[string]string{"sshd": item(100, 8000, 1000, 1)})
	a2 := procSnap(t1, map[string]string{"sshd": item(100, 8000, 1000, 1)})
	b1 := procSnap(t2, map[string]string{"sshd": item(200, 8000, 1000, 1), "newproc": item(3, 12288, 9000000, 1)})
	b2 := procSnap(t3, map[string]string{"sshd": item(200, 8000, 1000, 1), "newproc": item(5, 12288, 9000000, 1)})

	d := CompareProcesses(a1, a2, b1, b2, 30, 1, 10240)
	if row := findRow(d, "newproc"); row == nil || row.Verdict != PVFlat {
		t.Fatalf("an older snapshot pair behaved differently: %v", row)
	}
	if d.UnwatchedListeners != nil {
		t.Errorf("unwatched listeners were reported without a listen section: %v", d.UnwatchedListeners)
	}
}
