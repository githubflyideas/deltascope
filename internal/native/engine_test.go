package native

import (
	"testing"
	"time"

	"github.com/githubflyideas/deltascope/internal/reasoning"
)

// This file is the end-to-end claim of the package: /proc text in, fired
// states out, with no PCP, no archive and no pmlogsummary anywhere in the
// path. Everything before this file could be correct in isolation and still
// produce rows the engine cannot read.

// statFixtureLater is statFixture ten seconds on, with cpu0 having consumed
// 1000 extra ticks -- exactly one core saturated for the whole interval,
// while cpu1 did nothing. On an 8-core host that is 12.5% of the machine, so
// it is deliberately invisible to every aggregate threshold and visible only
// through reasoning.Derive's per-core rows. That asymmetry is why Derive
// exists, and this is the only test in the tree that exercises it from real
// /proc/stat text.
const statFixtureLater = `cpu  1100 20 50 900 30 5 7 3 1 0
cpu0 1060 10 25 450 15 3 4 2 1 0
cpu1 40 10 25 450 15 2 3 1 0 0
intr 123456 1 2 3 0 0 0
ctxt 987654
btime 1700000000
processes 4321
procs_running 3
procs_blocked 2
`

// diskstatsFixtureLater advances nvme0n1 by 9000 ms of io_ticks and 30000 ms
// of weighted queue time over the same ten seconds: 90% utilisation with an
// average queue depth of 3, which is state.io.saturated's definition of a
// disk that is genuinely the bottleneck.
const diskstatsFixtureLater = `   7       0 loop0 100 0 200 10 0 0 0 0 0 0 0
 259       0 nvme0n1 1900 50 24000 500 3000 100 40000 900 0 12000 34500
 259       1 nvme0n1p1 900 40 7000 450 1800 90 14000 800 0 2800 4200
   8       0 sda 500 20 4000 300 100 10 800 200 0 1000 1500
   8       1 sda1 490 18 3900 290 95 9 780 190 0 980 1400
 253       0 dm-0 400 0 3000 250 90 0 700 150 0 900 1200
   9       0 md0 300 0 2000 100 80 0 600 100 0 800 1000
`

// fixtureWindow is a two-sample native window built entirely from the /proc
// fixtures in this package.
func fixtureWindow() Window {
	s1 := newSample(zeroTime)
	s1.parseStat(statFixture)
	s1.parseMeminfo(meminfoFixture)
	s1.parseVmstat(vmstatFixture)
	s1.parseDiskstats(diskstatsFixture)
	s1.parseLoadavg("1.50 0.80 0.40 3/1183 40331\n")

	s2 := newSample(zeroTime.Add(10 * time.Second))
	s2.parseStat(statFixtureLater)
	s2.parseMeminfo(meminfoFixture)
	s2.parseVmstat(vmstatFixture)
	s2.parseDiskstats(diskstatsFixtureLater)
	s2.parseLoadavg("1.50 0.80 0.40 3/1183 40331\n")
	// One process was OOM-killed during the window. Set directly rather than
	// through a third vmstat fixture: the point under test is the arithmetic
	// downstream, not the parse.
	s2.set("mem.vmstat.oom_kill", "", 2)

	return Build([]Sample{s1, s2})
}

func TestNativeRowsDriveTheReasoningEngine(t *testing.T) {
	w := fixtureWindow()
	// No Derive call here: EvaluateOn derives internally, and doing it again
	// in the caller would test a path no production caller uses.
	active := reasoning.EvaluateOn(reasoning.States, w.Rows, reasoning.Machine{NCPU: 8})

	for _, id := range []string{
		// Absolute gauge states, straight off /proc/meminfo.
		"state.mem.available_critical",
		"state.mem.swap_exhausted",
		// Two counters on the same disk instance, requiring SameInstance to
		// match them to nvme0n1 rather than across devices.
		"state.io.saturated",
		// Per-core saturation, only reachable through Derive.
		"state.cpu.core_pegged",
	} {
		if _, on := active[id]; !on {
			t.Errorf("%s should be active on this fixture host", id)
		}
	}

	// The same fixture must NOT fire the aggregate CPU states: one core out
	// of eight is 12.5% of the machine. If this ever starts firing, a
	// machine-scaled threshold has been mis-scaled.
	if _, on := active["state.cpu.user_high"]; on {
		t.Error("state.cpu.user_high must not fire at one saturated core out of eight")
	}

	// Diagnose must run to completion on natively-collected rows -- the
	// whole chain, not just Evaluate.
	results := reasoning.Diagnose(reasoning.Diagnoses, active)
	if len(results) == 0 {
		t.Error("a host that is out of memory and IO-saturated should produce at least one diagnosis")
	}
}

// TestPeakStatesDeclineOnAShortWindow is the honesty guarantee for the
// two-sample check path. Ten states are written with MinSamples: 30 because
// a burst cannot be judged from one interval, and the tempting shortcut --
// sampling at 100 ms to reach thirty readings in three seconds -- would turn
// PeakRatioGte 4 into a false-positive generator on counter jitter. So a
// short window must decline them, and this test fails if any of them
// starts answering.
func TestPeakStatesDeclineOnAShortWindow(t *testing.T) {
	w := fixtureWindow()
	active := reasoning.EvaluateOn(reasoning.States, w.Rows, reasoning.Machine{NCPU: 8})

	guarded := 0
	for _, st := range reasoning.States {
		needsSamples := false
		for _, c := range st.When {
			if c.MinSamples > 0 {
				needsSamples = true
			}
		}
		if !needsSamples {
			continue
		}
		guarded++
		if _, on := active[st.ID]; on {
			t.Errorf("%s is guarded by MinSamples but fired on a 2-sample window", st.ID)
		}
	}
	if guarded == 0 {
		t.Fatal("no MinSamples-guarded states found: this test is no longer testing anything")
	}
}

// TestDeclinedStatesAreReportedAsUnknown closes the loop the previous test
// opens. Declining is only honest if the decline is visible: a state that
// silently drops out of the active set is indistinguishable, to a reader,
// from a state that was checked and found fine. Every state the two-sample
// window cannot judge must therefore show up in Unevaluated with a reason,
// and no state may be both fired and unevaluated.
func TestDeclinedStatesAreReportedAsUnknown(t *testing.T) {
	w := fixtureWindow()
	active := reasoning.EvaluateOn(reasoning.States, w.Rows, reasoning.Machine{NCPU: 8})
	gaps := reasoning.Unevaluated(reasoning.States, w.Rows)

	for id := range active {
		if reason, ok := gaps[id]; ok {
			t.Errorf("%s fired and is also reported unevaluated (%s)", id, reason)
		}
	}

	for _, st := range reasoning.States {
		needsSamples := false
		for _, c := range st.When {
			if c.MinSamples > 0 {
				needsSamples = true
			}
		}
		if !needsSamples {
			continue
		}
		if gaps[st.ID] == "" {
			t.Errorf("%s was declined for want of samples but is not reported as unevaluated", st.ID)
		}
	}

	// The states this window genuinely answers must NOT be listed as gaps,
	// or the report would hedge on its own evidence.
	for _, id := range []string{"state.mem.available_critical", "state.io.saturated"} {
		if reason, ok := gaps[id]; ok {
			t.Errorf("%s was answered from /proc but reported unevaluated: %s", id, reason)
		}
	}
}
