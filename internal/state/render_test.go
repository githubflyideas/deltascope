package state

import (
	"bytes"
	"strings"
	"testing"
	"time"
)

// The rendering is where an honest dating can still be reported dishonestly:
// an undated event has a From and a To like any other, and printing them as a
// verified interval would turn "we do not know" into a timestamp. These tests
// pin the wording apart for the two cases.

func renderedTimeline(t *testing.T, evs []Event) string {
	t.Helper()
	d := Diff{
		A:     Snapshot{Taken: at(0)},
		B:     Snapshot{Taken: at(6)},
		Total: len(evs),
	}
	var buf bytes.Buffer
	RenderTimeline(&buf, d, evs, false)
	return buf.String()
}

func TestRenderTimelineSeparatesDatedFromUndated(t *testing.T) {
	dated := Event{
		From: at(1), To: at(2), Dated: true, Class: "kernel", Subject: "6.6.0",
		Changes: []Change{
			{Section: "system", Title: "System", Key: "kernel", Kind: Modified, Old: "6.1.0", New: "6.6.0"},
			{Section: "packages", Title: "Packages", Key: "kernel-core", Kind: Modified, Old: "6.1.0-1", New: "6.6.0-1"},
		},
	}
	undated := Event{
		From: at(0), To: at(6), Class: "section",
		Changes: []Change{
			{Section: "sysctl", Title: "Kernel parameters", Key: "vm.swappiness", Kind: Modified, Old: "60", New: "10"},
		},
	}
	out := renderedTimeline(t, []Event{dated, undated})

	for _, want := range []string{
		"2 changes in 2 event(s)",
		"kernel change 6.6.0",
		"between ",
		"time unknown",
		// Both sections of the kernel event are named inside the one block.
		"-- System", "-- Packages",
		// The section event has no wording of its own; its title is the head.
		"Kernel parameters",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("output missing %q:\n%s", want, out)
		}
	}
	// The undated event must not print its window as a verified interval.
	if n := strings.Count(out, "between "); n != 1 {
		t.Errorf("%d dated intervals printed, want 1:\n%s", n, out)
	}
}

func TestRenderTimelineFlagsUnstable(t *testing.T) {
	out := renderedTimeline(t, []Event{{
		From: at(1), To: at(2), Dated: true, Unstable: true, Class: "section",
		Changes: []Change{{Section: "sysctl", Title: "Kernel parameters", Key: "x", Kind: Modified, Old: "1", New: "2"}},
	}})
	if !strings.Contains(out, "more than once") {
		t.Errorf("a flapping value must say so:\n%s", out)
	}
}

// mtime is the one per-item timestamp change accounting has, and a Note that
// merely repeats the value is not a second fact.
func TestChangeLineExtras(t *testing.T) {
	c := newColorizer(false)
	mt := time.Date(2026, 3, 4, 9, 30, 0, 0, time.UTC).Unix()

	withMtime := changeLine(c, Change{Key: "/etc/ssh/sshd_config", Kind: Modified,
		Old: "sha256:aaa", New: "sha256:bbb", Mtime: mt})
	if !strings.Contains(withMtime, "mtime ") {
		t.Errorf("a file-backed change should show its mtime: %q", withMtime)
	}

	noMtime := changeLine(c, Change{Key: "vm.swappiness", Kind: Modified, Old: "60", New: "10"})
	if strings.Contains(noMtime, "mtime") {
		t.Errorf("an item with no mtime must not claim one: %q", noMtime)
	}

	echo := changeLine(c, Change{Key: "tcp/0.0.0.0:22", Kind: Added, New: "sshd", Note: "sshd"})
	if strings.Count(echo, "sshd") != 1 {
		t.Errorf("a Note repeating the value should print once: %q", echo)
	}

	real := changeLine(c, Change{Key: "/etc/sudoers", Kind: Modified,
		Old: "sha256:aaa", New: "sha256:bbb", Note: "root:root 0440"})
	if !strings.Contains(real, "root:root 0440") {
		t.Errorf("a Note carrying a second fact must print: %q", real)
	}
}
