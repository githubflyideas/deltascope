package native

import (
	"strings"
	"testing"
	"time"
)

const tcpFixture = `  sl  local_address rem_address   st tx_queue rx_queue tr tm->when retrnsmt   uid  timeout inode
   0: 00000000:1F90 00000000:0000 0A 00000000:00000000 00:00000000 00000000     0        0 12345 1 0000000000000000 100 0 0 10 0
   1: 0100007F:1F90 0100007F:C350 01 00000000:00000000 00:00000000 00000000  1000        0 12346 1 0000000000000000 20 4 30 10 -1
   2: 0100007F:C350 0100007F:1F90 08 00000000:00000000 00:00000000 00000000  1000        0 12347 1 0000000000000000 20 4 30 10 -1
   3: 0100007F:C351 0100007F:1F90 06 00000000:00000000 03:00000B54 00000000     0        0 0 2 0000000000000000
`

const tcp6Fixture = `  sl  local_address                         remote_address                        st tx_queue rx_queue tr tm->when retrnsmt   uid  timeout inode
   0: 00000000000000000000000000000000:0016 00000000000000000000000000000000:0000 0A 00000000:00000000 00:00000000 00000000     0        0 20000 1 0000000000000000 100 0 0 10 0
   1: 0000000000000000FFFF00000100007F:1F90 0000000000000000FFFF00000100007F:C352 01 00000000:00000000 00:00000000 00000000  1000        0 20001 1 0000000000000000 20 4 30 10 -1
`

func TestParseTCPConn(t *testing.T) {
	s := newSample(zeroTime)
	s.parseTCPConn(tcpFixture, tcp6Fixture)

	// IPv4 and IPv6 are two views of one socket table and must be summed.
	mustVal(t, &s, "network.tcpconn.established", "", 2)
	mustVal(t, &s, "network.tcpconn.listen", "", 2)
	mustVal(t, &s, "network.tcpconn.close_wait", "", 1)
	mustVal(t, &s, "network.tcpconn.time_wait", "", 1)
	// Zero is a real answer here and has to be stated: the scan completed
	// and found no half-open connections. Leaving it absent would make
	// state.net.syn_flood permanently unevaluated on a healthy host.
	mustVal(t, &s, "network.tcpconn.syn_recv", "", 0)
}

// TestParseTCPConnCapEmitsNothing pins the one place this package prefers
// silence to a number. Past the cap the tally is partial, and a partial
// count does not read as partial -- it reads as a low, healthy count, which
// would assert that a machine drowning in CLOSE_WAIT is fine.
func TestParseTCPConnCapEmitsNothing(t *testing.T) {
	s := newSample(zeroTime)
	s.parseTCPConn(strings.Repeat("   1: a:b c:d 08 x\n", maxSocketLines+1))
	mustAbsent(t, &s, "network.tcpconn.close_wait", "")
	mustAbsent(t, &s, "network.tcpconn.established", "")
}

// twoSamples builds a pair of samples the given distance apart, each filled
// by the supplied function, which is how every counter test here states its
// before and after without touching a real /proc.
func twoSamples(gap time.Duration, a, b func(s *Sample)) []Sample {
	s1 := newSample(zeroTime)
	a(&s1)
	s2 := newSample(zeroTime.Add(gap))
	b(&s2)
	return []Sample{s1, s2}
}

func findRow(t *testing.T, w Window, metric, instance string) (float64, bool) {
	t.Helper()
	for _, r := range w.Rows {
		if r.Metric == metric && r.Instance == instance {
			if r.B == nil {
				t.Fatalf("%s: row present with nil B", RowKey(metric, instance))
			}
			return *r.B, true
		}
	}
	return 0, false
}

// TestCounterRateMatchesThresholdUnits is the conversion the whole package
// turns on: one core fully consumed for the whole interval must come out as
// 1000 ms/s, because that is what every CPU threshold in the state catalog
// is written against.
func TestCounterRateMatchesThresholdUnits(t *testing.T) {
	// 1000 ticks over 10 s at USER_HZ=100 is exactly one saturated core.
	samples := twoSamples(10*time.Second,
		func(s *Sample) { s.set("kernel.all.cpu.user", "", 5000) },
		func(s *Sample) { s.set("kernel.all.cpu.user", "", 6000) })
	w := Build(samples)

	got, ok := findRow(t, w, "kernel.all.cpu.user", "")
	if !ok {
		t.Fatal("kernel.all.cpu.user produced no row")
	}
	if got != 1000 {
		t.Errorf("one saturated core = %v ms/s, want 1000", got)
	}
}

// TestGaugeUsesNewestReading: a gauge's answer is the current value, with
// the spread kept alongside it. swap.free doubles as the one byte-scaled
// metric in the catalog, so it also proves the scale is applied.
func TestGaugeUsesNewestReading(t *testing.T) {
	samples := twoSamples(10*time.Second,
		func(s *Sample) { s.set("swap.free", "", 4096) },
		func(s *Sample) { s.set("swap.free", "", 1000) })
	w := Build(samples)

	got, ok := findRow(t, w, "swap.free", "")
	if !ok {
		t.Fatal("swap.free produced no row")
	}
	if got != 1000*1024 {
		t.Errorf("swap.free = %v byte, want %v (1000 Kbyte)", got, 1000*1024)
	}
	for _, r := range w.Rows {
		if r.Metric != "swap.free" {
			continue
		}
		if r.BCount != 2 {
			t.Errorf("gauge BCount = %d, want 2 (one per sample)", r.BCount)
		}
		if r.BMax == nil || *r.BMax != 4096*1024 {
			t.Errorf("gauge BMax = %v, want the highest reading", r.BMax)
		}
	}
}

// TestSingleSampleYieldsGaugesOnly: a one-shot read can answer every
// absolute question and no rate question at all. Emitting a counter row
// from one reading would report the since-boot total as if it had happened
// in the last instant -- kernel.all.intr would read as billions per second.
func TestSingleSampleYieldsGaugesOnly(t *testing.T) {
	s := newSample(zeroTime)
	s.set("mem.util.available", "", 300000)
	s.set("kernel.all.intr", "", 9e9)
	w := Build([]Sample{s})

	if _, ok := findRow(t, w, "mem.util.available", ""); !ok {
		t.Error("a single sample must still answer gauge states")
	}
	if _, ok := findRow(t, w, "kernel.all.intr", ""); ok {
		t.Error("a counter with one reading must produce no row")
	}
}

// TestCounterResetProducesNoRow covers reboot, device removal and 32-bit
// wraparound with one rule: a counter that went backwards has no honest
// rate, and a negative one would read as an improvement under every
// polarity in the catalog.
func TestCounterResetProducesNoRow(t *testing.T) {
	samples := twoSamples(10*time.Second,
		func(s *Sample) { s.set("network.tcp.retranssegs", "", 5000) },
		func(s *Sample) { s.set("network.tcp.retranssegs", "", 12) })
	if _, ok := findRow(t, Build(samples), "network.tcp.retranssegs", ""); ok {
		t.Error("a counter that went backwards must produce no row")
	}
}

// TestIncrementSurvivesRateConversion is the defect this package is here to
// make visible rather than to hide.
//
// The state is written as "mem.vmstat.oom_kill BGte 1", which under rate
// semantics means one OOM kill per second -- so a machine that OOM-killed a
// process during the window still does not trip it, and the state's own
// description ("never noise: an OOM kill is a completed failure") is wrong
// about what it measures. Changing the threshold is a separate decision;
// what this package guarantees is that the raw count is not lost, so a
// report can say "1 OOM kill" even while the rate says 0.1.
func TestIncrementSurvivesRateConversion(t *testing.T) {
	samples := twoSamples(10*time.Second,
		func(s *Sample) { s.set("mem.vmstat.oom_kill", "", 0) },
		func(s *Sample) { s.set("mem.vmstat.oom_kill", "", 1) })
	w := Build(samples)

	rate, ok := findRow(t, w, "mem.vmstat.oom_kill", "")
	if !ok {
		t.Fatal("mem.vmstat.oom_kill produced no row")
	}
	if rate != 0.1 {
		t.Errorf("rate = %v/s, want 0.1 (1 kill over 10 s)", rate)
	}
	if got := w.Increments["mem.vmstat.oom_kill"]; got != 1 {
		t.Errorf("increment = %v, want 1: the raw count is what a person needs to read", got)
	}
}

// TestCounterBCountIsIntervals pins why a two-sample check run cannot fake
// the peak states. MinSamples: 30 is compared against BCount, and a counter
// has one fewer interval than it has readings -- so a quick check declines
// those states instead of judging a burst from a single interval.
func TestCounterBCountIsIntervals(t *testing.T) {
	s1, s2, s3 := newSample(zeroTime), newSample(zeroTime.Add(5*time.Second)), newSample(zeroTime.Add(10*time.Second))
	s1.set("kernel.all.pswitch", "", 1000)
	s2.set("kernel.all.pswitch", "", 2000)
	s3.set("kernel.all.pswitch", "", 10000)
	w := Build([]Sample{s1, s2, s3})

	for _, r := range w.Rows {
		if r.Metric != "kernel.all.pswitch" {
			continue
		}
		if r.BCount != 2 {
			t.Errorf("BCount = %d, want 2 (three readings, two intervals)", r.BCount)
		}
		// The whole-run rate is time-weighted (9000 over 10 s), while the
		// peak is the fastest single interval (8000 over 5 s).
		if *r.B != 900 {
			t.Errorf("B = %v, want 900 (time-weighted over the whole run)", *r.B)
		}
		if r.BMax == nil || *r.BMax != 1600 {
			t.Errorf("BMax = %v, want 1600 (the fastest interval)", r.BMax)
		}
	}
}
