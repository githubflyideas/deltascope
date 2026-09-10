package native

import (
	"testing"
	"time"
)

// zeroTime is the sample timestamp for parser tests, where elapsed time is
// irrelevant: parsers store raw readings and never look at the clock.
var zeroTime = time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

// Fixtures are verbatim shapes of real /proc files, including the header
// lines and the trailing columns this package ignores, because the parsers'
// job is to survive the real layout -- a fixture trimmed to only the fields
// under test would pass while the parser broke on column drift.

func mustVal(t *testing.T, s *Sample, metric, instance string, want float64) {
	t.Helper()
	got, ok := s.Value(metric, instance)
	if !ok {
		t.Errorf("%s: no reading", RowKey(metric, instance))
		return
	}
	if got != want {
		t.Errorf("%s = %v, want %v", RowKey(metric, instance), got, want)
	}
}

func mustAbsent(t *testing.T, s *Sample, metric, instance string) {
	t.Helper()
	if v, ok := s.Value(metric, instance); ok {
		t.Errorf("%s = %v, want no reading at all", RowKey(metric, instance), v)
	}
}

const statFixture = `cpu  100 20 50 900 30 5 7 3 1 0
cpu0 60 10 25 450 15 3 4 2 1 0
cpu1 40 10 25 450 15 2 3 1 0 0
intr 123456 1 2 3 0 0 0
ctxt 987654
btime 1700000000
processes 4321
procs_running 3
procs_blocked 2
softirq 555 1 2 3
`

func TestParseStat(t *testing.T) {
	s := newSample(zeroTime)
	s.parseStat(statFixture)

	mustVal(t, &s, "kernel.all.cpu.user", "", 100)
	mustVal(t, &s, "kernel.all.cpu.nice", "", 20)
	mustVal(t, &s, "kernel.all.cpu.sys", "", 50)
	mustVal(t, &s, "kernel.all.cpu.idle", "", 900)
	mustVal(t, &s, "kernel.all.cpu.wait.total", "", 30)
	mustVal(t, &s, "kernel.all.cpu.irq.hard", "", 5)
	mustVal(t, &s, "kernel.all.cpu.irq.soft", "", 7)
	mustVal(t, &s, "kernel.all.cpu.steal", "", 3)
	mustVal(t, &s, "kernel.all.cpu.guest", "", 1)

	// Per-core rows carry PCP's instance names, which is what
	// reasoning.Derive keys its per-core aggregation on.
	mustVal(t, &s, "kernel.percpu.cpu.user", "cpu0", 60)
	mustVal(t, &s, "kernel.percpu.cpu.sys", "cpu0", 25)
	mustVal(t, &s, "kernel.percpu.cpu.wait.total", "cpu0", 15)
	mustVal(t, &s, "kernel.percpu.cpu.irq.soft", "cpu0", 4)
	mustVal(t, &s, "kernel.percpu.cpu.user", "cpu1", 40)

	// Only the first intr column is the total; the per-IRQ columns after it
	// must not be summed in.
	mustVal(t, &s, "kernel.all.intr", "", 123456)
	mustVal(t, &s, "kernel.all.pswitch", "", 987654)
	mustVal(t, &s, "kernel.all.sysfork", "", 4321)
	mustVal(t, &s, "kernel.all.runnable", "", 3)
	mustVal(t, &s, "kernel.all.blocked", "", 2)
}

func TestParseLoadavg(t *testing.T) {
	s := newSample(zeroTime)
	s.parseLoadavg("1.50 0.80 0.40 3/1183 40331\n")

	// The instance name matters: reasoning.firstMatch takes whichever
	// instance appears first, so emitting more than one averaging window
	// would make load-based states depend on map order.
	mustVal(t, &s, "kernel.all.load", "1 minute", 1.5)
	mustAbsent(t, &s, "kernel.all.load", "5 minute")
	mustVal(t, &s, "kernel.all.nprocs", "", 1183)
}

const pressureFixture = `some avg10=12.34 avg60=5.00 avg300=1.00 total=1234567
full avg10=3.21 avg60=1.00 avg300=0.50 total=45678
`

func TestParsePressure(t *testing.T) {
	s := newSample(zeroTime)
	s.parsePressure("memory", pressureFixture)

	mustVal(t, &s, "kernel.all.pressure.memory.some.avg", psiWindow, 12.34)
	mustVal(t, &s, "kernel.all.pressure.memory.full.avg", psiWindow, 3.21)
	// avg60 and avg300 are deliberately not emitted.
	mustAbsent(t, &s, "kernel.all.pressure.memory.some.avg", "1 minute")
}

const meminfoFixture = `MemTotal:       16384000 kB
MemFree:          200000 kB
MemAvailable:     300000 kB
Buffers:           50000 kB
Cached:          1000000 kB
SwapCached:          100 kB
SwapTotal:       2000000 kB
SwapFree:           1000 kB
Dirty:             40000 kB
Writeback:          2000 kB
AnonPages:       5000000 kB
Mapped:           300000 kB
Shmem:             10000 kB
Slab:             400000 kB
PageTables:        30000 kB
Committed_AS:    9000000 kB
`

func TestParseMeminfo(t *testing.T) {
	s := newSample(zeroTime)
	s.parseMeminfo(meminfoFixture)

	mustVal(t, &s, "mem.util.available", "", 300000)
	mustVal(t, &s, "mem.util.free", "", 200000)
	mustVal(t, &s, "mem.util.bufmem", "", 50000)
	mustVal(t, &s, "mem.util.cached", "", 1000000)
	mustVal(t, &s, "mem.util.swapCached", "", 100)
	mustVal(t, &s, "mem.util.dirty", "", 40000)
	mustVal(t, &s, "mem.util.committed_AS", "", 9000000)
	// Raw here, still in Kbyte: swap.free's byte conversion is the spec's
	// job, applied once in Build.
	mustVal(t, &s, "swap.free", "", 1000)
	// MemTotal and SwapTotal have no catalog entry and must not be guessed
	// into one.
	mustAbsent(t, &s, "mem.util.total", "")
}

const vmstatFixture = `nr_free_pages 50000
pgpgin 100
pgfault 1000
pgmajfault 20
pgactivate 11
pgdeactivate 12
pgscan_kswapd 500
pgscan_direct 5
pgscan_direct_throttle 999
allocstall_dma 1
allocstall_normal 2
allocstall_movable 3
compact_stall 7
oom_kill 1
pswpin 4
pswpout 8
thp_fault_alloc 2
thp_collapse_alloc 1
`

func TestParseVmstat(t *testing.T) {
	s := newSample(zeroTime)
	s.parseVmstat(vmstatFixture)

	mustVal(t, &s, "mem.vmstat.pgfault", "", 1000)
	mustVal(t, &s, "mem.vmstat.pgmajfault", "", 20)
	mustVal(t, &s, "mem.vmstat.oom_kill", "", 1)
	mustVal(t, &s, "mem.vmstat.pgscan_kswapd", "", 500)
	mustVal(t, &s, "swap.pagesin", "", 4)
	mustVal(t, &s, "swap.pagesout", "", 8)

	// pgscan_direct_throttle shares a prefix with pgscan_direct and is a
	// different thing; matching it would inflate the direct-reclaim signal.
	mustVal(t, &s, "mem.vmstat.pgscan_direct", "", 5)

	// Kernel 4.14 split allocstall per zone, so the name the catalog uses
	// exists on no current kernel and the sum is the equivalent.
	mustVal(t, &s, "mem.vmstat.allocstall", "", 6)
}

// TestParseVmstatAbsentCounterStaysAbsent is the distinction the package
// exists for. mem.vmstat.oom_kill does not exist below kernel 4.13, and a
// zero there would assert that nothing has been OOM-killed on a host where
// that cannot be known.
func TestParseVmstatAbsentCounterStaysAbsent(t *testing.T) {
	s := newSample(zeroTime)
	s.parseVmstat("pgfault 1000\npgmajfault 20\n")
	mustAbsent(t, &s, "mem.vmstat.oom_kill", "")
	mustAbsent(t, &s, "mem.vmstat.allocstall", "")
}
