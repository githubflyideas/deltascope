// Package native reads the metrics the reasoning engine needs straight
// from /proc and /sys, so the 78-state / 58-diagnosis chain can run on a
// host with no PCP installed and no archive to query.
//
// It exists because of an asymmetry: 76 of the 78 states are fixed
// thresholds on the window-B value, so the engine needs current values,
// not a time series -- but the only source of those values was a fork of
// pmlogsummary. Without the pcp package installed the chain produced
// nothing at all, and internal/diagnose fell through to severity "ok"
// with the headline "No regression ... detected" on a machine that could
// be out of disk and OOM-killing.
//
// The engine itself needs no changes: it is already a pure function of
// []pcp.DiffRow (its own tests build those rows by hand), so this package
// only has to produce rows that mean the same thing pmlogsummary's did.
// "Mean the same thing" is the whole difficulty, and it is why the unit
// and counter/gauge semantics live in this file: pcp.MetricInfo records
// label, category, polarity and floors, but NOT units, NOT scale and NOT
// whether a metric is a counter. Those were implicit in what pmlogsummary
// happened to emit. Getting one wrong here silently mis-scales a
// threshold, so every entry below states the target unit explicitly.
package native

// Kind says how raw readings become the value a state is written against.
type Kind int

const (
	// Gauge is instantaneous: the newest reading is the answer. Sampling
	// it repeatedly gives a peak but never a rate.
	Gauge Kind = iota

	// Counter is monotonic since boot. The answer is a per-second rate
	// over the interval between two readings, which is what pmlogsummary
	// reports for a PCP counter metric -- see the comment at
	// internal/reasoning/state.go:397. A single reading yields nothing.
	Counter
)

// spec is the per-metric conversion: multiply the raw delta (or the raw
// gauge) by Scale, and for a counter divide by elapsed seconds.
type spec struct {
	Kind  Kind
	Scale float64
	// Unit is documentation, not behaviour. It is here because the wrong
	// unit is the single easiest way to break this package invisibly:
	// every threshold in internal/reasoning assumes these exact ones.
	Unit string
}

// clockTicksPerSec is USER_HZ, fixed at 100 on all mainstream Linux
// builds -- the same assumption internal/state/procdiff.go already makes
// for /proc/<pid>/stat. CPU ticks therefore scale by 1000/100 = 10 to
// reach the millisec/second that kernel.all.cpu.* is measured in, where
// 1000 ms/s is one fully consumed core and NOT one percent.
const clockTicksPerSec = 100

const msPerTick = 1000 / clockTicksPerSec

// specs is the complete set of metrics this package can produce. The
// reasoning catalog is a closed set (every state metric must resolve
// here -- see TestEveryStateMetricHasANativeSource), so this map is the
// contract, and anything absent from it is reported as unevaluated
// rather than as zero.
var specs = map[string]spec{}

func declare(kind Kind, scale float64, unit string, names ...string) {
	for _, n := range names {
		specs[n] = spec{kind, scale, unit}
	}
}

func declarePrefixed(kind Kind, scale float64, unit, prefix string, leaves ...string) {
	for _, l := range leaves {
		specs[prefix+l] = spec{kind, scale, unit}
	}
}

func init() {
	const msPerSec = "millisec / second"
	const perSec = "count / sec"
	const count = "count"

	// --- CPU ------------------------------------------------------------
	// /proc/stat, USER_HZ ticks. 1000 ms/s == one saturated core.
	declarePrefixed(Counter, msPerTick, msPerSec, "kernel.all.cpu.",
		"user", "nice", "sys", "idle", "wait.total", "irq.hard", "irq.soft", "steal", "guest")
	declarePrefixed(Counter, msPerTick, msPerSec, "kernel.percpu.cpu.",
		"user", "sys", "wait.total", "irq.soft")
	declare(Counter, 1, perSec, "kernel.all.pswitch", "kernel.all.sysfork", "kernel.all.intr")
	// procs_blocked and procs_running are instantaneous queue depths, not
	// counters: /proc/stat reports the current value, not a total.
	declare(Gauge, 1, count, "kernel.all.blocked", "kernel.all.runnable", "kernel.all.nprocs")
	declare(Gauge, 1, count, "kernel.all.load")

	// --- PSI ------------------------------------------------------------
	// /proc/pressure/*, already a percentage of wall time stalled over the
	// kernel's own sliding window -- catalog.go:239 depends on the 0-100
	// range ("below 1% nothing is waiting"), so do not rescale it.
	declare(Gauge, 1, "none",
		"kernel.all.pressure.cpu.some.avg",
		"kernel.all.pressure.memory.some.avg",
		"kernel.all.pressure.memory.full.avg",
		"kernel.all.pressure.io.some.avg",
		"kernel.all.pressure.io.full.avg")

	// --- Memory ---------------------------------------------------------
	// /proc/meminfo reports Kbyte, which is what mem.util.* is measured in
	// (catalog thresholds are written as e.g. 512*1024 for 512 MB), so no
	// scaling. swap.free is the one metric in the whole catalog measured in
	// bytes -- state.mem.swap_exhausted compares it against a byte figure --
	// so it and only it multiplies by 1024.
	declarePrefixed(Gauge, 1, "Kbyte", "mem.util.",
		"available", "free", "cached", "bufmem", "dirty", "writeback", "slab",
		"anonpages", "mapped", "shmem", "swapCached", "committed_AS", "pageTables")
	declare(Gauge, 1024, "byte", "swap.free")

	// /proc/vmstat is monotonic since boot. These are the metrics that were
	// structurally dead under pmlogsummary rate conversion (a single OOM kill
	// in a 30-minute window rates to 0.0006/s and never reaches BGte 1); the
	// rate is still what the states are written against, so it is still what
	// is produced here -- Rows additionally carries the raw window increment
	// so a caller can report "1 OOM kill" rather than "0.0006 per second".
	declarePrefixed(Counter, 1, perSec, "mem.vmstat.",
		"pgfault", "pgmajfault", "oom_kill", "pgscan_direct", "pgscan_kswapd",
		"allocstall", "compact_stall", "pgactivate", "pgdeactivate",
		"thp_fault_alloc", "thp_collapse_alloc")
	// pswpin/pswpout are counted in pages, and so is PCP's swap.pagesin.
	declare(Counter, 1, perSec, "swap.pagesin", "swap.pagesout")
	// --- Disk -----------------------------------------------------------
	// All four classes come from the same /proc/diskstats fields; they
	// differ only in which device names feed them (dev = whole physical
	// disks, dm = device-mapper, md = software RAID, all = the sum over the
	// dev class, matching PCP).
	//
	// Sectors are 512 bytes and *_bytes is a Kbyte metric in PCP, hence 0.5.
	// io_ticks and weighted_ms are milliseconds of accumulated time, so a
	// per-second rate scaled by 0.001 turns io_ticks into the 0-1 busy
	// fraction disk.dev.avactive means and weighted_ms into the average
	// queue length disk.dev.aveq means. Getting either scale wrong is
	// invisible: avactive would read as 850 instead of 0.85 and
	// state.io.saturated (BGte 0.8) would fire on an idle disk.
	//
	// The leaf sets differ per class because the catalog's do: md has no
	// utilisation figure (a RAID device's io_ticks is not a disk's), and
	// only the rollup carries merges. Declaring a leaf the catalog lacks
	// would produce rows that Build silently drops -- coverage that is not
	// coverage, which TestEverySpecIsInCatalog exists to catch.
	declarePrefixed(Counter, 1, perSec, "disk.dev.", "read", "write", "total")
	declarePrefixed(Counter, 0.5, "Kbyte / sec", "disk.dev.", "read_bytes", "write_bytes")
	declarePrefixed(Counter, 0.001, "none", "disk.dev.", "avactive", "aveq")

	declarePrefixed(Counter, 1, perSec, "disk.all.",
		"read", "write", "total", "read_merge", "write_merge")
	declarePrefixed(Counter, 0.5, "Kbyte / sec", "disk.all.", "read_bytes", "write_bytes")
	declarePrefixed(Counter, 0.001, "none", "disk.all.", "avactive", "aveq")

	declarePrefixed(Counter, 1, perSec, "disk.dm.", "read", "write")
	declarePrefixed(Counter, 0.5, "Kbyte / sec", "disk.dm.", "read_bytes", "write_bytes")
	declarePrefixed(Counter, 0.001, "none", "disk.dm.", "avactive", "aveq")

	declarePrefixed(Counter, 1, perSec, "disk.md.", "read", "write")
	declarePrefixed(Counter, 0.5, "Kbyte / sec", "disk.md.", "read_bytes", "write_bytes")

	// --- Filesystem -----------------------------------------------------
	// statfs, per mounted filesystem, instance = device path. full is the
	// df-style percentage (0-100) rather than a raw ratio, because
	// state.disk.nearly_full is written as BGte 90.
	declare(Gauge, 1, "none", "filesys.full")
	declare(Gauge, 1, "Kbyte", "filesys.free", "filesys.avail")
	declare(Gauge, 1, count, "filesys.usedfiles")

	// --- VFS and misc kernel gauges -------------------------------------
	declare(Gauge, 1, count, "vfs.files.count", "vfs.inodes.count", "vfs.dentry.count")
	declare(Gauge, 1, count, "kernel.all.entropy.avail")
	declare(Gauge, 1, "second", "kernel.all.uptime")
	// --- Network interfaces ---------------------------------------------
	// /proc/net/dev, instance = interface name. Errors and drops are the
	// second family that pmlogsummary's rate conversion made unreachable:
	// state.net.rx_drops is BGte 1, i.e. one dropped frame per second.
	declarePrefixed(Counter, 1, "byte / sec", "network.interface.", "in.bytes", "out.bytes")
	declarePrefixed(Counter, 1, perSec, "network.interface.",
		"in.packets", "out.packets", "in.errors", "out.errors",
		"in.drops", "out.drops", "collisions")

	// --- TCP / UDP / ICMP / IP ------------------------------------------
	// /proc/net/snmp and /proc/net/netstat. Every one of these is monotonic
	// except currestab, which snmp reports as the current number of
	// established connections -- treating it as a counter would produce a
	// rate of change and make state.net.* nonsense.
	declarePrefixed(Counter, 1, perSec, "network.tcp.",
		"activeopens", "passiveopens", "attemptfails", "estabresets",
		"insegs", "outsegs", "retranssegs", "inerrs", "outrsts",
		"listendrops", "listenoverflows", "syncookiessent", "syncookiesrecv",
		"syncookiesfailed", "prunecalled", "rcvcollapsed", "delayedacks", "timeouts")
	declare(Gauge, 1, count, "network.tcp.currestab")
	declarePrefixed(Counter, 1, perSec, "network.udp.",
		"indatagrams", "outdatagrams", "noports", "inerrors",
		"recvbuferrors", "sndbuferrors")
	declarePrefixed(Counter, 1, perSec, "network.icmp.",
		"inmsgs", "outmsgs", "inerrors", "indestunreachs")
	declarePrefixed(Counter, 1, perSec, "network.ip.",
		"inreceives", "outrequests", "forwdatagrams", "indiscards",
		"outdiscards", "inhdrerrors", "fragfails", "reasmfails")

	// --- softnet, sockets ------------------------------------------------
	declarePrefixed(Counter, 1, perSec, "network.softnet.",
		"processed", "dropped", "time_squeeze")
	// /proc/net/sockstat: instantaneous socket accounting.
	declare(Gauge, 1, count,
		"network.sockstat.tcp.inuse", "network.sockstat.tcp.alloc",
		"network.sockstat.tcp.tw", "network.sockstat.tcp.orphan",
		"network.sockstat.udp.inuse")
	// tcpconn.* is the one family with no counter file behind it: it is
	// derived by counting socket states in /proc/net/tcp{,6}, which is O(n)
	// in open sockets. See the cap in parseTCPConn -- past it these are
	// reported unknown rather than undercounted.
	declarePrefixed(Gauge, 1, count, "network.tcpconn.",
		"established", "time_wait", "close_wait", "syn_recv", "listen")
}
