package pcp

import "strings"

// inferUnit derives a metric's PCP unit from its name, so trend series
// (which pmrep does not annotate with units at all, unlike pmlogsummary)
// can be labelled correctly. Patterns below are taken from real pmlogsummary
// output observed for every metric in the catalog, not guessed.
//
// This exists because a bare decimal-magnitude suffix (fmtNum's K/M/G) is
// ambiguous for a byte-valued metric: mem.util.available is natively in
// Kbyte, so a raw value of 13,800,000 formatted as "13.80M" reads as
// "13.8 megabytes" when it actually means "13.8 million Kbyte" = 13.8 GB
// -- a 1000x misread, and in exactly the direction that makes a healthy
// machine look like it's about to OOM. Units must travel with the number.
func inferUnit(metric string) string {
	switch {
	case metric == "swap.free":
		return "byte"
	case strings.HasPrefix(metric, "mem.util."):
		return "Kbyte"
	case strings.HasPrefix(metric, "swap.pages"):
		return "count / sec"
	case strings.HasPrefix(metric, "mem.vmstat."):
		return "count / sec"

	// Pressure metrics must be checked before the generic CPU-time rule
	// below, since "kernel.all.pressure.cpu.some.avg" contains ".cpu."
	// and would otherwise be misclassified as CPU time (ms/s) instead of
	// the ratio it actually is.
	case strings.HasPrefix(metric, "kernel.all.pressure."):
		return "none"
	case strings.Contains(metric, ".cpu.") && (strings.HasPrefix(metric, "kernel.all.") || strings.HasPrefix(metric, "kernel.percpu.")):
		return "millisec / second"
	case metric == "kernel.all.load" || metric == "kernel.all.runnable" || metric == "kernel.all.blocked" || metric == "kernel.all.nprocs":
		return "none"
	case metric == "kernel.all.pswitch" || metric == "kernel.all.intr" || metric == "kernel.all.sysfork":
		return "count / sec"
	case metric == "kernel.all.uptime":
		return "sec"
	case metric == "kernel.all.entropy.avail":
		return "none"

	case strings.HasSuffix(metric, "_bytes") && strings.HasPrefix(metric, "disk."):
		return "Kbyte / sec"
	case strings.HasPrefix(metric, "disk.") && (strings.HasSuffix(metric, ".avactive") || strings.HasSuffix(metric, ".aveq")):
		return "none"
	case strings.HasPrefix(metric, "disk."):
		return "count / sec"

	case metric == "filesys.full":
		return "none"
	case strings.HasPrefix(metric, "filesys."):
		return "Kbyte"
	case strings.HasPrefix(metric, "vfs."):
		return "none"

	case strings.HasSuffix(metric, ".in.bytes") || strings.HasSuffix(metric, ".out.bytes"):
		return "byte / sec"
	case strings.HasPrefix(metric, "network.interface."):
		return "count / sec"
	case metric == "network.tcp.currestab":
		return "none"
	case strings.HasPrefix(metric, "network.tcp.") || strings.HasPrefix(metric, "network.udp.") ||
		strings.HasPrefix(metric, "network.icmp.") || strings.HasPrefix(metric, "network.ip.") ||
		strings.HasPrefix(metric, "network.softnet.") || strings.HasPrefix(metric, "network.tcpconn."):
		return "count / sec"
	case strings.HasPrefix(metric, "network.sockstat."):
		return "count"

	default:
		return "none"
	}
}
