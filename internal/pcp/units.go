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
	case metric == "swap.free" || metric == "swap.capacity":
		return "byte"
	case metric == "mem.physmem":
		return "Kbyte"
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
	// Inode counts, not space. The generic filesys. rule below says Kbyte,
	// which for these two would label a count of files as a size on disk.
	case metric == "filesys.usedfiles" || metric == "filesys.maxfiles":
		return "count"
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
	case strings.HasPrefix(metric, "network.conntrack."):
		return "count"

	default:
		return "none"
	}
}

// excludedInstance filters out per-instance rows that are noise
// regardless of change magnitude -- structural noise the significance
// floor can't catch, because the numbers can be large and "real" by
// PCP's own accounting; they just never mean what the report implies.
//
//   - network.interface.* on "lo": loopback traffic is inter-process
//     communication on this host, not network activity. It never leaves
//     the machine, so it has no business in a "NIC traffic" report and
//     its numbers (which can be large -- some databases and message
//     brokers talk to themselves over loopback TCP) look exactly like
//     real external traffic if you don't know to discount it.
//   - network.interface.* on a synthetic interface: see syntheticInterface.
//   - filesys.* on a /dev/loopN device: loop devices back snap package
//     mounts (Ubuntu ships dozens by default) and squashfs images, not
//     disks anyone provisions or manages capacity for. They are always
//     ~100% full by construction (a squashfs image is exactly as big as
//     its own contents), so they contribute zero signal and just repeat
//     the same "100.0% flat" row 15-20 times per report.
func excludedInstance(metric, instance string) bool {
	if instance == "" {
		return false
	}
	if strings.HasPrefix(metric, "network.interface.") && SyntheticInterface(instance) {
		return true
	}
	if strings.HasPrefix(metric, "filesys.") && strings.HasPrefix(instance, "/dev/loop") {
		return true
	}
	return false
}

// tunnelStubs are the interfaces the kernel creates the moment a tunnel
// module loads, whether or not anything uses them. Configuring one real
// GRE tunnel brings gre0, gretap0 and erspan0 into existence alongside
// it; they are permanently down and carry nothing. Matched by exact name
// so a tunnel someone actually created and named (natgre, gre1, wg0) is
// never mistaken for a stub.
var tunnelStubs = map[string]bool{
	"gre0": true, "gretap0": true, "erspan0": true,
	"ip_vti0": true, "ip6_vti0": true, "ip6tnl0": true,
	"ip6gre0": true, "sit0": true, "tunl0": true,
}

// SyntheticInterface reports whether an interface name belongs to the
// container/virtualisation plumbing rather than to a link this machine
// sends traffic over. A Docker host with 40 containers reports 80-odd
// interfaces, and every one of them is noise of a particularly bad kind:
//
//   - A veth is one end of a virtual pair. Its bytes are also counted on
//     the bridge it is enslaved to and again on the NIC that carries them
//     off the host, so including it triple-counts the same packet.
//   - Its name (veth1c3052f) is assigned at container start and is gone
//     for good at container stop. Every restart mints a fresh series, so
//     the trend store fills with dead instances nothing will ever append
//     to again -- which is what turns a chart legend into eighty entries.
//   - A tunnel stub carries nothing at all, ever.
//
// Deliberately NOT matched: bond*, team*, tap*, tun*, wg* and a bridge
// named br0 or br-lan. Those carry real traffic on real hosts -- a bond is
// the NIC on a serious server, tap interfaces are VM NICs on a hypervisor,
// and OpenWrt's br-lan is the LAN. Docker's own bridges are br- followed
// by twelve hex digits, which is what the br- rule below requires.
func SyntheticInterface(name string) bool {
	switch {
	case name == "" || name == "lo":
		return true
	case tunnelStubs[name]:
		return true
	case strings.HasPrefix(name, "veth"):
		return true
	case name == "docker0" || name == "docker_gwbridge":
		return true
	case strings.HasPrefix(name, "virbr"): // libvirt's own bridges
		return true
	case strings.HasPrefix(name, "cni") || strings.HasPrefix(name, "cali") ||
		strings.HasPrefix(name, "cilium_") || strings.HasPrefix(name, "flannel.") ||
		strings.HasPrefix(name, "lxcbr") || strings.HasPrefix(name, "nodelocaldns"):
		return true // Kubernetes CNI plumbing
	case strings.HasPrefix(name, "dummy"):
		return true
	case strings.HasPrefix(name, "br-") && isHex(name[len("br-"):], 12):
		return true // docker network create
	default:
		return false
	}
}

// isHex reports whether s is at least n characters long and entirely
// lower-case hexadecimal. Used to tell Docker's br-d862fd428d13 apart
// from a bridge a person named br-lan.
func isHex(s string, n int) bool {
	if len(s) < n {
		return false
	}
	for _, c := range s {
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return false
		}
	}
	return true
}
