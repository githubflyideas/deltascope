package native

import (
	"strconv"
	"strings"
)

// Every parser in this file is a pure function of file content: it takes
// the bytes of one /proc file as a string and writes readings into a
// Sample. Nothing here opens a file, so the whole parsing layer is
// testable from fixtures on any OS -- which matters because this package
// is developed on a machine that has no /proc at all, and a parser that
// can only be exercised on the target host is a parser that ships
// unverified.
//
// Values are stored raw, in the units the kernel published them in. The
// unit conversion in metrics.go is applied later, once, in Rows -- doing
// it here would mean every parser had to know about counter rates.

// num parses a kernel-published number. Failure yields false rather than
// zero: a truncated or unexpected line must not become a reading, because
// a zero reading is indistinguishable from a healthy one.
func num(s string) (float64, bool) {
	v, err := strconv.ParseFloat(strings.TrimSpace(s), 64)
	if err != nil {
		return 0, false
	}
	return v, true
}

// field returns the i-th whitespace-separated field, or "" if absent.
func field(f []string, i int) string {
	if i < 0 || i >= len(f) {
		return ""
	}
	return f[i]
}

// setField stores f[i] under metric/instance when it parses.
func (s *Sample) setField(metric, instance string, f []string, i int) {
	if v, ok := num(field(f, i)); ok {
		s.set(metric, instance, v)
	}
}

// cpuLeaves maps the /proc/stat cpu-line column order onto the catalog's
// names. The order is fixed by the kernel and has only ever been appended
// to, so indexing by position is safe; guest_nice (column 9) has no
// catalog entry and is dropped rather than folded into guest.
var cpuLeaves = []string{
	"user", "nice", "sys", "idle", "wait.total", "irq.hard", "irq.soft", "steal", "guest",
}

// percpuLeaves is the subset of the same columns the catalog carries
// per-core. Kept as a set so parseStat walks the line once.
var percpuLeaves = map[int]string{0: "user", 2: "sys", 4: "wait.total", 6: "irq.soft"}

// parseStat reads /proc/stat: the CPU time counters, the per-core copies
// of them, and four whole-machine counters/gauges that live nowhere else.
func (s *Sample) parseStat(content string) {
	for _, line := range strings.Split(content, "\n") {
		f := strings.Fields(line)
		if len(f) < 2 {
			continue
		}
		switch {
		case f[0] == "cpu":
			for i, leaf := range cpuLeaves {
				s.setField("kernel.all.cpu."+leaf, "", f, i+1)
			}
		case strings.HasPrefix(f[0], "cpu"):
			// Instance names match PCP's ("cpu0", "cpu1"), which is also what
			// reasoning.Derive keys its per-core aggregation on.
			for i, leaf := range percpuLeaves {
				s.setField("kernel.percpu.cpu."+leaf, f[0], f, i+1)
			}
		case f[0] == "intr":
			// Only the first column is the total; the rest are per-IRQ.
			s.setField("kernel.all.intr", "", f, 1)
		case f[0] == "ctxt":
			s.setField("kernel.all.pswitch", "", f, 1)
		case f[0] == "processes":
			s.setField("kernel.all.sysfork", "", f, 1)
		case f[0] == "procs_blocked":
			s.setField("kernel.all.blocked", "", f, 1)
		case f[0] == "procs_running":
			s.setField("kernel.all.runnable", "", f, 1)
		}
	}
}

// psiWindow is the only pressure window emitted. PCP exposes
// kernel.all.pressure.*.avg as an instance-indexed metric ("10 second",
// "1 minute", "5 minute") and reasoning.firstMatch takes whichever
// instance it sees first, so emitting more than one would make a state
// like cpu.pressure_high depend on map iteration order. avg10 is the
// choice because these states are meant to describe now.
const psiWindow = "10 second"

// parsePressure reads one /proc/pressure/<resource> file, whose lines are
// "some avg10=1.23 avg60=0.45 avg300=0.11 total=123456". The values are
// already percentages of wall time stalled, and the catalog thresholds are
// written against 0-100, so they pass through unscaled.
func (s *Sample) parsePressure(resource, content string) {
	for _, line := range strings.Split(content, "\n") {
		f := strings.Fields(line)
		if len(f) < 2 {
			continue
		}
		kind := f[0] // "some" or "full"
		if kind != "some" && kind != "full" {
			continue
		}
		for _, kv := range f[1:] {
			k, v, ok := strings.Cut(kv, "=")
			if !ok || k != "avg10" {
				continue
			}
			if val, ok := num(v); ok {
				s.set("kernel.all.pressure."+resource+"."+kind+".avg", psiWindow, val)
			}
		}
	}
}

// meminfoKeys maps /proc/meminfo labels to catalog metrics. Everything in
// meminfo is already Kbyte, which is what mem.util.* means, so the only
// rescale is swap.free's (applied in Rows, from its spec).
var meminfoKeys = map[string]string{
	"MemAvailable": "mem.util.available",
	"MemFree":      "mem.util.free",
	"Cached":       "mem.util.cached",
	"Buffers":      "mem.util.bufmem",
	"Dirty":        "mem.util.dirty",
	"Writeback":    "mem.util.writeback",
	"Slab":         "mem.util.slab",
	"AnonPages":    "mem.util.anonpages",
	"Mapped":       "mem.util.mapped",
	"Shmem":        "mem.util.shmem",
	"SwapCached":   "mem.util.swapCached",
	"Committed_AS": "mem.util.committed_AS",
	"PageTables":   "mem.util.pageTables",
	"SwapFree":     "swap.free",
}

// parseMeminfo reads /proc/meminfo ("MemFree:  1234 kB").
func (s *Sample) parseMeminfo(content string) {
	for _, line := range strings.Split(content, "\n") {
		label, rest, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		metric, want := meminfoKeys[strings.TrimSpace(label)]
		if !want {
			continue
		}
		if v, ok := num(field(strings.Fields(rest), 0)); ok {
			s.set(metric, "", v)
		}
	}
}

// parseLoadavg reads /proc/loadavg: "0.34 0.28 0.31 2/1183 40331". Only
// the 1-minute figure is emitted, under PCP's instance name for it, so
// that reasoning's firstMatch cannot pick a different averaging window
// than the state author intended.
func (s *Sample) parseLoadavg(content string) {
	f := strings.Fields(content)
	s.setField("kernel.all.load", "1 minute", f, 0)
	if procs := field(f, 3); procs != "" {
		if run, total, ok := strings.Cut(procs, "/"); ok {
			// runnable also comes from /proc/stat; whichever is parsed last
			// wins and they agree, so this is a fallback for kernels where
			// procs_running is absent.
			if v, ok := num(run); ok {
				s.set("kernel.all.runnable", "", v)
			}
			if v, ok := num(total); ok {
				s.set("kernel.all.nprocs", "", v)
			}
		}
	}
}

// vmstatKeys maps exact /proc/vmstat keys to catalog metrics. Exact, not
// prefix: pgscan_direct_throttle is a different thing from pgscan_direct
// and folding it in would inflate the reclaim signal.
var vmstatKeys = map[string]string{
	"pgfault":            "mem.vmstat.pgfault",
	"pgmajfault":         "mem.vmstat.pgmajfault",
	"oom_kill":           "mem.vmstat.oom_kill",
	"pgscan_direct":      "mem.vmstat.pgscan_direct",
	"pgscan_kswapd":      "mem.vmstat.pgscan_kswapd",
	"compact_stall":      "mem.vmstat.compact_stall",
	"pgactivate":         "mem.vmstat.pgactivate",
	"pgdeactivate":       "mem.vmstat.pgdeactivate",
	"thp_fault_alloc":    "mem.vmstat.thp_fault_alloc",
	"thp_collapse_alloc": "mem.vmstat.thp_collapse_alloc",
	"pswpin":             "swap.pagesin",
	"pswpout":            "swap.pagesout",
}

// parseVmstat reads /proc/vmstat ("pgfault 123456" per line).
//
// allocstall needs its own handling: kernel 4.14 split the single
// allocstall counter into per-zone allocstall_dma / allocstall_dma32 /
// allocstall_normal / allocstall_movable, so on any current kernel the
// name the catalog uses does not exist and the sum is the equivalent. A
// state written against direct-reclaim stalls would otherwise be silently
// unevaluated on every modern host.
func (s *Sample) parseVmstat(content string) {
	allocstall, haveAllocstall := 0.0, false
	for _, line := range strings.Split(content, "\n") {
		f := strings.Fields(line)
		if len(f) < 2 {
			continue
		}
		v, ok := num(f[1])
		if !ok {
			continue
		}
		if metric, want := vmstatKeys[f[0]]; want {
			s.set(metric, "", v)
			continue
		}
		if f[0] == "allocstall" || strings.HasPrefix(f[0], "allocstall_") {
			allocstall += v
			haveAllocstall = true
		}
	}
	if haveAllocstall {
		s.set("mem.vmstat.allocstall", "", allocstall)
	}
}
