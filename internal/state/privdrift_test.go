package state

import (
	"bytes"
	"strings"
	"testing"
)

// Privilege drift: two captures of the same machine taken by different users.
//
// The Skipped/Unreadable mechanism only catches the all-or-nothing case, where
// a collector gave up on one side. It misses the worse one: `ss -lntuHp` run
// unprivileged still lists every listening socket, it only drops the `users:`
// field for sockets owned by somebody else. So a root baseline against a
// service-user capture reports every port on the machine as Modified --
// "nginx" -> "" -- and the report reads as though every service on the host
// lost its process. Config fingerprints do the same thing by key: a 0600
// sshd_config is simply absent from the unprivileged snapshot.
//
// The fix has to be surgical. Suppressing everything whenever the two runs
// differ in uid would hide a real sysctl change, and sysctl/packages/modules
// read the same whoever runs them.

func uid(n int) *int { return &n }

func privSnap(euid *int, listenProc, sysctlVal string) Snapshot {
	return Snapshot{
		Schema: SchemaVersion, Euid: euid,
		Sections: []Section{
			{Name: "listen", Title: "Listening Ports", PrivSensitive: true, Items: []Item{
				{Key: "tcp 0.0.0.0:80", Value: listenProc, Note: listenProc},
			}},
			{Name: "sysctl", Title: "Kernel Parameters", Items: []Item{
				{Key: "net.core.somaxconn", Value: sysctlVal},
			}},
		},
	}
}

// The case that produced the false alarm: root baseline, service-user capture,
// every port apparently losing its service.
func TestDifferentUidsExcludeThePrivilegeSensitiveSection(t *testing.T) {
	d := Compare(privSnap(uid(0), "nginx", "4096"), privSnap(uid(997), "", "4096"))
	for _, sd := range d.Sections {
		if sd.Name == "listen" {
			t.Fatalf("a uid difference was reported as %d change(s) to the ports: %+v",
				len(sd.Changes), sd.Changes)
		}
	}
	if len(d.PrivilegeDrift) != 1 || d.PrivilegeDrift[0] != "listen" {
		t.Fatalf("drift not named in the payload: %v", d.PrivilegeDrift)
	}
	// Whoever has to fix the drift needs to know which run was which; a report
	// that says only "excluded" leaves them to guess which baseline to re-take.
	if d.EuidA != 0 || d.EuidB != 997 {
		t.Errorf("uids not recorded: a=%d b=%d, want 0 and 997", d.EuidA, d.EuidB)
	}
}

// The boundary the flag exists to hold: a section whose contents are the same
// whoever reads them must still be diffed, or a real change would be hidden
// whenever the two runs happened to differ in uid.
func TestDriftDoesNotSuppressASectionThatReadsTheSameForEveryone(t *testing.T) {
	d := Compare(privSnap(uid(0), "nginx", "4096"), privSnap(uid(997), "", "1024"))
	var found bool
	for _, sd := range d.Sections {
		if sd.Name != "sysctl" {
			continue
		}
		found = true
		if len(sd.Changes) != 1 || sd.Changes[0].Old != "4096" || sd.Changes[0].New != "1024" {
			t.Errorf("sysctl change mangled: %+v", sd.Changes)
		}
	}
	if !found {
		t.Fatal("a real sysctl change was suppressed along with the privilege drift")
	}
}

// Same uid on both sides is the steady state, and nothing may be excluded there
// -- a port genuinely losing its service is exactly what this section is for.
func TestSameUidStillReportsAPortLosingItsService(t *testing.T) {
	d := Compare(privSnap(uid(0), "nginx", "4096"), privSnap(uid(0), "", "4096"))
	if len(d.PrivilegeDrift) != 0 {
		t.Fatalf("two captures by the same user reported drift: %v", d.PrivilegeDrift)
	}
	var n int
	for _, sd := range d.Sections {
		if sd.Name == "listen" {
			n = len(sd.Changes)
		}
	}
	if n != 1 {
		t.Errorf("nginx disappearing from port 80 produced %d change(s), want 1", n)
	}
}

// A snapshot written before Euid existed decodes as nil. Guessing root for it
// would claim a privilege the capture may never have had -- and if the guess
// happened to match, drift would be declared absent and every port reported as
// Modified again. Undecidable means diff it, and say nothing about privilege.
func TestUnknownUidIsNotGuessed(t *testing.T) {
	for _, tc := range []struct {
		name string
		a, b *int
	}{
		{"old baseline", nil, uid(997)},
		{"old current", uid(0), nil},
		{"both old", nil, nil},
	} {
		d := Compare(privSnap(tc.a, "nginx", "4096"), privSnap(tc.b, "", "4096"))
		if len(d.PrivilegeDrift) != 0 {
			t.Errorf("%s: drift claimed from an unknown uid: %v", tc.name, d.PrivilegeDrift)
		}
	}
}

// Skipped-on-one-side is already reported as Unreadable, and a section must not
// appear in both lists: the reader would read one exclusion as two.
func TestAnUnreadableSectionIsNotAlsoReportedAsDrift(t *testing.T) {
	a := privSnap(uid(0), "nginx", "4096")
	b := privSnap(uid(997), "", "4096")
	b.Sections[0].Skipped = "ss not found"
	b.Sections[0].Items = nil
	d := Compare(a, b)
	if len(d.Unreadable) != 1 || d.Unreadable[0] != "listen" {
		t.Fatalf("a section readable on only one side was not reported unreadable: %v", d.Unreadable)
	}
	if len(d.PrivilegeDrift) != 0 {
		t.Errorf("the same exclusion was reported twice: %v", d.PrivilegeDrift)
	}
}

// Capture must record the uid it ran as, or none of the above is decidable for
// snapshots taken from here on.
func TestCaptureRecordsTheUidItRanAs(t *testing.T) {
	snap := Snapshot{}
	if snap.Euid != nil {
		t.Fatal("the zero Snapshot claims to know its uid")
	}
	// Not calling Capture: it shells out to Linux tools. What is pinned here is
	// that the field is a pointer, so "unknown" is representable at all.
	known := privSnap(uid(0), "nginx", "4096")
	if known.Euid == nil || *known.Euid != 0 {
		t.Error("a recorded uid did not survive into the snapshot")
	}
}

// Excluding a section is only half the job: a report that excludes the ports and
// then prints "State is identical" has told the reader the opposite of what it
// knows. All three renderings are checked, because a cron job greps the one-line
// one and never sees the other two.
func TestTheRenderingsDoNotClaimAllClearAfterExcludingASection(t *testing.T) {
	d := Compare(privSnap(uid(0), "nginx", "4096"), privSnap(uid(997), "", "4096"))
	if d.Total != 0 {
		t.Fatalf("fixture no longer produces an empty-but-incomplete diff: %d", d.Total)
	}

	var text bytes.Buffer
	RenderText(&text, d, false)
	if strings.Contains(text.String(), "State is identical") {
		t.Errorf("text report claimed identical state:\n%s", text.String())
	}
	for _, want := range []string{"not compared", "Listening Ports", "uid 0", "uid 997"} {
		if !strings.Contains(text.String(), want) {
			t.Errorf("text report does not mention %q:\n%s", want, text.String())
		}
	}

	var md bytes.Buffer
	RenderMarkdown(&md, d, "")
	if !strings.Contains(md.String(), "Not compared") {
		t.Errorf("markdown report hid the exclusion:\n%s", md.String())
	}
	if strings.Contains(md.String(), "did not touch system state") {
		t.Errorf("markdown report claimed the change touched nothing:\n%s", md.String())
	}

	// The alert line is one string, so it has to carry the caveat inside it.
	if line := RenderSummaryLine(d); !strings.Contains(line, "not compared") {
		t.Errorf("summary line reads as a clean run: %q", line)
	}
	// And the steady state must keep the short wording a cron job already greps.
	clean := Compare(privSnap(uid(0), "nginx", "4096"), privSnap(uid(0), "nginx", "4096"))
	if line := RenderSummaryLine(clean); !strings.HasSuffix(line, "no change") {
		t.Errorf("an actually clean run stopped saying so: %q", line)
	}
}
