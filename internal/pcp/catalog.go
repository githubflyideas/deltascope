package pcp

import (
	"encoding/json"
	"fmt"
	"os"
	"regexp"
)

type Polarity string

const (
	WorseUp  Polarity = "worse_up"
	BetterUp Polarity = "better_up"
	Neutral  Polarity = "neutral"
)

type MetricInfo struct {
	Metric       string   `json:"metric"`
	Label        string   `json:"label"`
	Category     string   `json:"category"`
	Polarity     Polarity `json:"polarity"`
	Fold         bool     `json:"fold,omitempty"`
	ThresholdPct float64  `json:"threshold_pct,omitempty"`
	MinAbs       float64  `json:"min_abs,omitempty"`
}

type Preset struct {
	Label   string   `json:"label"`
	Metrics []string `json:"metrics"`
}

var Categories = []string{"CPU", "Memory", "Disk I/O", "Filesystem", "Network"}

var Catalog = []MetricInfo{
	{"kernel.all.cpu.user", "user CPU", "CPU", WorseUp, false, 0, 0},
	{"kernel.all.cpu.sys", "system CPU", "CPU", WorseUp, false, 0, 0},
	{"kernel.all.cpu.nice", "nice CPU", "CPU", Neutral, false, 0, 0},
	{"kernel.all.cpu.idle", "idle CPU", "CPU", BetterUp, false, 0, 0},
	{"kernel.all.cpu.wait.total", "I/O wait", "CPU", WorseUp, false, 0, 0},
	{"kernel.all.cpu.irq.hard", "hard IRQ CPU", "CPU", WorseUp, false, 0, 0},
	{"kernel.all.cpu.irq.soft", "softirq CPU", "CPU", WorseUp, false, 0, 0},
	{"kernel.all.cpu.steal", "CPU steal", "CPU", WorseUp, false, 0, 0},
	{"kernel.all.cpu.guest", "Guest CPU", "CPU", Neutral, false, 0, 0},
	{"kernel.all.load", "System load", "CPU", WorseUp, false, 0, 0},
	{"kernel.all.runnable", "runnable tasks", "CPU", WorseUp, false, 0, 0},
	{"kernel.all.blocked", "I/O blocked tasks", "CPU", WorseUp, false, 0, 0},
	{"kernel.all.nprocs", "total processes", "CPU", Neutral, false, 0, 0},
	{"kernel.all.pswitch", "context switches", "CPU", WorseUp, false, 0, 0},
	{"kernel.all.intr", "interrupt rate", "CPU", Neutral, false, 0, 0},
	{"kernel.all.sysfork", "process forks", "CPU", Neutral, false, 0, 0},
	{"kernel.all.uptime", "uptime", "CPU", Neutral, false, 0, 0},
	{"kernel.all.entropy.avail", "entropy available", "CPU", Neutral, false, 0, 0},
	{"kernel.all.pressure.cpu.some.avg", "CPU pressure (PSI some)", "CPU", WorseUp, false, 0, 0},
	{"kernel.percpu.cpu.user", "user CPU · per-core", "CPU", WorseUp, false, 0, 0},
	{"kernel.percpu.cpu.sys", "system CPU · per-core", "CPU", WorseUp, false, 0, 0},
	{"kernel.percpu.cpu.wait.total", "I/O wait · per-core", "CPU", WorseUp, false, 0, 0},
	{"kernel.percpu.cpu.irq.soft", "softirq · per-core", "CPU", WorseUp, false, 0, 0},

	{"mem.util.available", "available memory", "Memory", BetterUp, false, 0, 0},
	{"mem.util.free", "free memory", "Memory", BetterUp, false, 0, 0},
	{"mem.util.cached", "page cache", "Memory", Neutral, false, 0, 0},
	{"mem.util.bufmem", "buffers", "Memory", Neutral, false, 0, 0},
	{"mem.util.dirty", "dirty pages", "Memory", WorseUp, false, 0, 0},
	{"mem.util.writeback", "writeback pages", "Memory", WorseUp, false, 0, 0},
	{"mem.util.slab", "slab", "Memory", Neutral, false, 0, 0},
	{"mem.util.anonpages", "anonymous pages", "Memory", Neutral, false, 0, 0},
	{"mem.util.mapped", "mapped pages", "Memory", Neutral, false, 0, 0},
	{"mem.util.shmem", "shared memory", "Memory", Neutral, false, 0, 0},
	{"mem.util.swapCached", "swap cache", "Memory", Neutral, false, 0, 0},
	{"mem.util.committed_AS", "committed memory (Committed_AS)", "Memory", WorseUp, false, 0, 0},
	{"mem.util.pageTables", "page table usage", "Memory", Neutral, false, 0, 0},
	{"swap.free", "swap free", "Memory", BetterUp, false, 0, 0},
	{"swap.pagesin", "pages swapped in", "Memory", WorseUp, false, 0, 0},
	{"swap.pagesout", "pages swapped out", "Memory", WorseUp, false, 0, 0},
	{"mem.vmstat.pgmajfault", "major faults", "Memory", WorseUp, false, 0, 0},
	{"mem.vmstat.pgfault", "total faults", "Memory", Neutral, false, 0, 0},
	{"mem.vmstat.oom_kill", "OOM kill count", "Memory", WorseUp, false, 0, 0},
	{"mem.vmstat.pgscan_direct", "direct reclaim scans", "Memory", WorseUp, false, 0, 0},
	{"mem.vmstat.pgscan_kswapd", "kswapd reclaim scans", "Memory", WorseUp, false, 0, 0},
	{"mem.vmstat.allocstall", "allocation stalls", "Memory", WorseUp, false, 0, 0},
	{"mem.vmstat.compact_stall", "compaction stalls", "Memory", WorseUp, false, 0, 0},
	{"mem.vmstat.thp_fault_alloc", "THP fault allocations", "Memory", Neutral, false, 0, 0},
	{"mem.vmstat.thp_collapse_alloc", "THP collapse allocations", "Memory", Neutral, false, 0, 0},
	{"mem.vmstat.pgactivate", "page activations", "Memory", Neutral, false, 0, 0},
	{"mem.vmstat.pgdeactivate", "page deactivations", "Memory", Neutral, false, 0, 0},
	{"kernel.all.pressure.memory.some.avg", "memory pressure (PSI some)", "Memory", WorseUp, false, 0, 0},
	{"kernel.all.pressure.memory.full.avg", "memory pressure (PSI full)", "Memory", WorseUp, false, 0, 0},

	{"disk.all.read", "read IOPS · all disks", "Disk I/O", Neutral, false, 0, 0},
	{"disk.all.write", "write IOPS · all disks", "Disk I/O", Neutral, false, 0, 0},
	{"disk.all.read_bytes", "read throughput · all disks", "Disk I/O", Neutral, false, 0, 0},
	{"disk.all.write_bytes", "write throughput · all disks", "Disk I/O", Neutral, false, 0, 0},
	{"disk.all.total", "total IOPS · all disks", "Disk I/O", Neutral, false, 0, 0},
	{"disk.all.read_merge", "read merges · all disks", "Disk I/O", Neutral, false, 0, 0},
	{"disk.all.write_merge", "write merges · all disks", "Disk I/O", Neutral, false, 0, 0},
	{"disk.all.avactive", "disk active time · all disks", "Disk I/O", WorseUp, false, 0, 0},
	{"disk.all.aveq", "I/O queue · all disks", "Disk I/O", WorseUp, false, 0, 0},
	{"disk.dev.read", "read IOPS · per-disk", "Disk I/O", Neutral, false, 0, 0},
	{"disk.dev.write", "write IOPS · per-disk", "Disk I/O", Neutral, false, 0, 0},
	{"disk.dev.read_bytes", "read throughput · per-disk", "Disk I/O", Neutral, false, 0, 0},
	{"disk.dev.write_bytes", "write throughput · per-disk", "Disk I/O", Neutral, false, 0, 0},
	{"disk.dev.total", "total IOPS · per-disk", "Disk I/O", Neutral, false, 0, 0},
	{"disk.dev.avactive", "disk active time · per-disk", "Disk I/O", WorseUp, false, 0, 0},
	{"disk.dev.aveq", "I/O queue · per-disk", "Disk I/O", WorseUp, false, 0, 0},
	{"disk.dm.read", "read IOPS · LVM", "Disk I/O", Neutral, false, 0, 0},
	{"disk.dm.write", "write IOPS · LVM", "Disk I/O", Neutral, false, 0, 0},
	{"disk.dm.read_bytes", "read throughput · LVM", "Disk I/O", Neutral, false, 0, 0},
	{"disk.dm.write_bytes", "write throughput · LVM", "Disk I/O", Neutral, false, 0, 0},
	{"disk.dm.avactive", "active time · LVM", "Disk I/O", WorseUp, false, 0, 0},
	{"disk.dm.aveq", "I/O queue · LVM", "Disk I/O", WorseUp, false, 0, 0},
	{"disk.md.read", "read IOPS · MD RAID", "Disk I/O", Neutral, false, 0, 0},
	{"disk.md.write", "write IOPS · MD RAID", "Disk I/O", Neutral, false, 0, 0},
	{"disk.md.read_bytes", "read throughput · MD RAID", "Disk I/O", Neutral, false, 0, 0},
	{"disk.md.write_bytes", "write throughput · MD RAID", "Disk I/O", Neutral, false, 0, 0},
	{"kernel.all.pressure.io.some.avg", "I/O pressure (PSI some)", "Disk I/O", WorseUp, false, 0, 0},
	{"kernel.all.pressure.io.full.avg", "I/O pressure (PSI full)", "Disk I/O", WorseUp, false, 0, 0},

	{"filesys.full", "space usage", "Filesystem", WorseUp, false, 0, 0},
	{"filesys.free", "free space", "Filesystem", BetterUp, false, 0, 0},
	{"filesys.usedfiles", "inodes used", "Filesystem", WorseUp, false, 0, 0},
	{"vfs.files.count", "open files", "Filesystem", Neutral, false, 0, 0},
	{"vfs.inodes.count", "kernel inode cache", "Filesystem", Neutral, false, 0, 0},
	{"vfs.dentry.count", "dentry cache", "Filesystem", Neutral, false, 0, 0},
	{"filesys.avail", "non-root available space", "Filesystem", BetterUp, false, 0, 0},

	{"network.interface.in.bytes", "inbound traffic", "Network", Neutral, false, 0, 0},
	{"network.interface.out.bytes", "outbound traffic", "Network", Neutral, false, 0, 0},
	{"network.interface.in.packets", "inbound packet rate", "Network", Neutral, false, 0, 0},
	{"network.interface.out.packets", "outbound packet rate", "Network", Neutral, false, 0, 0},
	{"network.interface.in.errors", "inbound errors", "Network", WorseUp, false, 0, 0},
	{"network.interface.out.errors", "outbound errors", "Network", WorseUp, false, 0, 0},
	{"network.interface.in.drops", "inbound drops", "Network", WorseUp, false, 0, 0},
	{"network.interface.out.drops", "outbound drops", "Network", WorseUp, false, 0, 0},
	{"network.interface.collisions", "collisions", "Network", WorseUp, false, 0, 0},
	{"network.tcp.insegs", "TCP in segments", "Network", Neutral, false, 0, 0},
	{"network.tcp.outsegs", "TCP out segments", "Network", Neutral, false, 0, 0},
	{"network.tcp.retranssegs", "TCP retransmits", "Network", WorseUp, false, 0, 0},
	{"network.tcp.inerrs", "TCP in errors", "Network", WorseUp, false, 0, 0},
	{"network.tcp.outrsts", "TCP out RST", "Network", WorseUp, false, 0, 0},
	{"network.tcp.timeouts", "TCP timeouts", "Network", WorseUp, false, 0, 0},
	{"network.tcp.currestab", "TCP established", "Network", Neutral, false, 0, 0},
	{"network.tcp.activeopens", "TCP active opens", "Network", Neutral, false, 0, 0},
	{"network.tcp.passiveopens", "TCP passive opens", "Network", Neutral, false, 0, 0},
	{"network.tcp.estabresets", "TCP established resets", "Network", WorseUp, false, 0, 0},
	{"network.tcp.attemptfails", "TCP attempt fails", "Network", WorseUp, false, 0, 0},
	{"network.tcp.listendrops", "TCP listen drops", "Network", WorseUp, false, 0, 0},
	{"network.tcp.listenoverflows", "TCP listen overflow", "Network", WorseUp, false, 0, 0},
	{"network.tcp.syncookiessent", "SYN cookies sent", "Network", WorseUp, false, 0, 0},
	{"network.tcp.syncookiesrecv", "SYN cookies verified", "Network", Neutral, false, 0, 0},
	{"network.tcp.syncookiesfailed", "SYN cookies failed", "Network", WorseUp, false, 0, 0},
	{"network.udp.indatagrams", "UDP in datagrams", "Network", Neutral, false, 0, 0},
	{"network.udp.outdatagrams", "UDP out datagrams", "Network", Neutral, false, 0, 0},
	{"network.udp.inerrors", "UDP in errors", "Network", WorseUp, false, 0, 0},
	{"network.udp.noports", "UDP no port", "Network", Neutral, false, 0, 0},
	{"network.udp.recvbuferrors", "UDP recv buffer errors", "Network", WorseUp, false, 0, 0},
	{"network.udp.sndbuferrors", "UDP send buffer errors", "Network", WorseUp, false, 0, 0},
	{"network.sockstat.tcp.inuse", "TCP sockets in use", "Network", Neutral, false, 0, 0},
	{"network.sockstat.tcp.alloc", "TCP sockets allocated", "Network", Neutral, false, 0, 0},
	{"network.sockstat.tcp.tw", "TIME-WAIT connections", "Network", Neutral, false, 0, 0},
	{"network.sockstat.tcp.orphan", "orphan connections", "Network", WorseUp, false, 0, 0},
	{"network.sockstat.udp.inuse", "UDP sockets in use", "Network", Neutral, false, 0, 0},
	{"network.softnet.dropped", "softnet drops", "Network", WorseUp, false, 0, 0},
	{"network.softnet.time_squeeze", "softnet time squeeze", "Network", WorseUp, false, 0, 0},
	{"network.softnet.processed", "softirq packets processed", "Network", Neutral, false, 0, 0},
	{"network.tcpconn.established", "conn state ESTABLISHED", "Network", Neutral, false, 0, 0},
	{"network.tcpconn.time_wait", "conn state TIME-WAIT", "Network", Neutral, false, 0, 0},
	{"network.tcpconn.close_wait", "conn state CLOSE-WAIT", "Network", WorseUp, false, 0, 0},
	{"network.tcpconn.syn_recv", "conn state SYN-RECV", "Network", WorseUp, false, 0, 0},
	{"network.tcpconn.listen", "conn state LISTEN", "Network", Neutral, false, 0, 0},
	{"network.tcp.prunecalled", "receive buffer pruning", "Network", WorseUp, false, 0, 0},
	{"network.tcp.rcvcollapsed", "receive queue collapses", "Network", WorseUp, false, 0, 0},
	{"network.tcp.delayedacks", "delayed ACKs", "Network", Neutral, false, 0, 0},
	{"network.icmp.inmsgs", "ICMP in messages", "Network", Neutral, false, 0, 0},
	{"network.icmp.outmsgs", "ICMP out messages", "Network", Neutral, false, 0, 0},
	{"network.icmp.inerrors", "ICMP in errors", "Network", WorseUp, false, 0, 0},
	{"network.icmp.indestunreachs", "ICMP dest unreachable", "Network", Neutral, false, 0, 0},
	{"network.ip.inreceives", "IP in packets", "Network", Neutral, false, 0, 0},
	{"network.ip.outrequests", "IP out packets", "Network", Neutral, false, 0, 0},
	{"network.ip.inhdrerrors", "IP header errors", "Network", WorseUp, false, 0, 0},
	{"network.ip.indiscards", "IP in discards", "Network", WorseUp, false, 0, 0},
	{"network.ip.outdiscards", "IP out discards", "Network", WorseUp, false, 0, 0},
	{"network.ip.forwdatagrams", "IP forwarded packets", "Network", Neutral, false, 0, 0},
	{"network.ip.fragfails", "IP fragmentation failures", "Network", WorseUp, false, 0, 0},
	{"network.ip.reasmfails", "IP reassembly failures", "Network", WorseUp, false, 0, 0},
}

var TrendPresets = map[string]Preset{
	"cpu":  {"CPU usage (ms/s)", []string{"kernel.all.cpu.user", "kernel.all.cpu.sys", "kernel.all.cpu.wait.total", "kernel.all.cpu.steal"}},
	"load": {"System load", []string{"kernel.all.load"}},
	"mem":  {"available memory", []string{"mem.util.available"}},
	"disk": {"Disk throughput", []string{"disk.all.read_bytes", "disk.all.write_bytes"}},
	"net":  {"NIC traffic", []string{"network.interface.in.bytes", "network.interface.out.bytes"}},
	"tcp":  {"TCP retransmits / connections", []string{"network.tcp.retranssegs", "network.tcp.currestab"}},
	"sock": {"TCP connection states", []string{"network.sockstat.tcp.alloc", "network.sockstat.tcp.tw", "network.sockstat.tcp.orphan"}},
	"psi":  {"Pressure PSI (some)", []string{"kernel.all.pressure.cpu.some.avg", "kernel.all.pressure.memory.some.avg", "kernel.all.pressure.io.some.avg"}},
}

// minAbsDefault sets an absolute floor below which a change is not
// significant no matter how large the percentage looks. A metric moving
// from 0.005 to 0.097 is +1840% but both values are effectively zero --
// without a floor, a nearly idle machine lights up red. The floor is the
// "is there actually anything happening here" test that percentage
// change alone cannot answer.
//
// Values are in each metric's own units. Conservative by design: the
// goal is to suppress noise on an idle box, not to hide real load.
var minAbsDefault = map[string]float64{
	// CPU time is in ms/s: 1000 ms/s == one full core. Below 10 ms/s
	// (1% of a core) nothing meaningful is happening.
	"kernel.all.cpu.user":       10,
	"kernel.all.cpu.sys":        10,
	"kernel.all.cpu.nice":       10,
	"kernel.all.cpu.wait.total": 10,
	"kernel.all.cpu.irq.hard":   10,
	"kernel.all.cpu.irq.soft":   10,
	"kernel.all.cpu.steal":      10,
	"kernel.all.cpu.guest":      10,
	"kernel.percpu.cpu.user":       10,
	"kernel.percpu.cpu.sys":        10,
	"kernel.percpu.cpu.wait.total": 10,
	"kernel.percpu.cpu.irq.soft":   10,

	// Load average: below 0.5 the machine is idle regardless of ratio.
	"kernel.all.load":     0.5,
	"kernel.all.runnable": 2,
	"kernel.all.blocked":  1,

	// PSI is a percentage of time stalled. Below 1% nothing is waiting.
	"kernel.all.pressure.cpu.some.avg":    1,
	"kernel.all.pressure.memory.some.avg": 1,
	"kernel.all.pressure.memory.full.avg": 1,
	"kernel.all.pressure.io.some.avg":     1,
	"kernel.all.pressure.io.full.avg":     1,

	// Memory in KB: a few hundred KB of dirty/writeback pages is nothing
	// on a machine with gigabytes of RAM.
	"mem.util.dirty":     51200,  // 50 MB
	"mem.util.writeback": 51200,  // 50 MB
	"mem.util.shmem":     51200,
	"swap.pagesin":       1,
	"swap.pagesout":      1,

	// Fault and reclaim rates (count/sec).
	"mem.vmstat.pgmajfault":    1,
	"mem.vmstat.pgscan_direct": 1,
	"mem.vmstat.pgactivate":    50,
	"mem.vmstat.pgdeactivate":  50,

	// Disk IOPS and throughput.
	"disk.all.read":        5,
	"disk.all.write":       5,
	"disk.all.total":       5,
	"disk.all.read_bytes":  512, // KB/s
	"disk.all.write_bytes": 512,
	"disk.all.read_merge":  5,
	"disk.all.write_merge": 5,
	"disk.dev.read":        5,
	"disk.dev.write":       5,
	"disk.dev.total":       5,
	"disk.dev.read_bytes":  512,
	"disk.dev.write_bytes": 512,
	"disk.dm.read":         5,
	"disk.dm.write":        5,
	"disk.dm.read_bytes":   512,
	"disk.dm.write_bytes":  512,
	"disk.md.read":         5,
	"disk.md.write":        5,
	"disk.md.read_bytes":   512,
	"disk.md.write_bytes":  512,

	// Network: bytes/sec and packets/sec on an idle link.
	"network.interface.in.bytes":    10240, // 10 KB/s
	"network.interface.out.bytes":   10240,
	"network.interface.in.packets":  50,
	"network.interface.out.packets": 50,
	"network.tcp.insegs":            50,
	"network.tcp.outsegs":           50,
	"network.tcp.retranssegs":       1,
	"network.tcp.outrsts":           1,
	"network.tcp.attemptfails":      1,
	"network.tcp.estabresets":       1,
	"network.tcp.activeopens":       1,
	"network.tcp.passiveopens":      1,
	"network.tcp.currestab":         10,
	"network.udp.indatagrams":       50,
	"network.udp.outdatagrams":      50,
	"network.sockstat.tcp.tw":       10,
	"network.sockstat.tcp.orphan":   1,
	"network.sockstat.tcp.inuse":    10,
	"network.sockstat.tcp.alloc":    10,
	"network.softnet.processed":     100,
	"network.icmp.inmsgs":           20,
	"network.icmp.outmsgs":          20,
	"network.ip.inreceives":         100,
	"network.ip.outrequests":        100,
	"network.ip.forwdatagrams":      50,
}

var thresholdDefault = map[string]float64{
	"kernel.all.intr":               100,
	"kernel.all.sysfork":            100,
	"kernel.all.entropy.avail":      100,
	"mem.vmstat.pgfault":            150,
	"mem.vmstat.pgactivate":         200,
	"mem.vmstat.pgdeactivate":       200,
	"mem.vmstat.thp_fault_alloc":    200,
	"mem.vmstat.thp_collapse_alloc": 200,
	"network.tcp.insegs":            150,
	"network.tcp.outsegs":           150,
	"network.tcp.delayedacks":       200,
	"network.icmp.inmsgs":           300,
	"network.icmp.outmsgs":          300,
	"network.icmp.indestunreachs":   300,
	"network.ip.inreceives":         150,
	"network.ip.outrequests":        150,
	"network.ip.forwdatagrams":      150,
	"network.udp.indatagrams":       150,
	"network.udp.outdatagrams":      150,
	"network.softnet.processed":     150,
}

var foldDefault = map[string]bool{
	"kernel.percpu.cpu.user":       true,
	"kernel.percpu.cpu.sys":        true,
	"kernel.percpu.cpu.wait.total": true,
	"kernel.percpu.cpu.irq.soft":   true,
}

var (
	catalogIndex map[string]MetricInfo
	orderIndex   map[string]int
)

func rebuildIndex() {
	catalogIndex = make(map[string]MetricInfo, len(Catalog))
	orderIndex = make(map[string]int, len(Catalog))
	for i := range Catalog {
		if foldDefault[Catalog[i].Metric] {
			Catalog[i].Fold = true
		}
		if t, ok := thresholdDefault[Catalog[i].Metric]; ok && Catalog[i].ThresholdPct == 0 {
			Catalog[i].ThresholdPct = t
		}
		if m, ok := minAbsDefault[Catalog[i].Metric]; ok && Catalog[i].MinAbs == 0 {
			Catalog[i].MinAbs = m
		}
		catalogIndex[Catalog[i].Metric] = Catalog[i]
		orderIndex[Catalog[i].Metric] = i
	}
}

func OrderIndex(metric string) int {
	if i, ok := orderIndex[metric]; ok {
		return i
	}
	return 1 << 30
}

func init() { rebuildIndex() }

func Lookup(metric string) (MetricInfo, bool) {
	c, ok := catalogIndex[metric]
	return c, ok
}

func DiffMetrics() []string {
	out := make([]string, 0, len(Catalog))
	for _, c := range Catalog {
		out = append(out, c.Metric)
	}
	return out
}

type CatalogFile struct {
	Categories []string          `json:"categories"`
	Metrics    []MetricInfo      `json:"metrics"`
	Presets    map[string]Preset `json:"presets"`
}

var metricNameRe = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9._]*$`)

func ExportCatalog() ([]byte, error) {
	return json.MarshalIndent(CatalogFile{
		Categories: Categories,
		Metrics:    Catalog,
		Presets:    TrendPresets,
	}, "", "  ")
}

func LoadCatalogFile(path string) error {
	raw, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	var cf CatalogFile
	if err := json.Unmarshal(raw, &cf); err != nil {
		return fmt.Errorf("failed to parse catalog: %w", err)
	}
	if len(cf.Categories) == 0 || len(cf.Metrics) == 0 {
		return fmt.Errorf("catalog must contain categories and metrics")
	}
	cats := map[string]bool{}
	for _, c := range cf.Categories {
		if c == "" {
			return fmt.Errorf("category name must not be empty")
		}
		cats[c] = true
	}
	for _, m := range cf.Metrics {
		if !metricNameRe.MatchString(m.Metric) {
			return fmt.Errorf("invalid metric name: %q", m.Metric)
		}
		if !cats[m.Category] {
			return fmt.Errorf("metric %s references undeclared category %q", m.Metric, m.Category)
		}
		switch m.Polarity {
		case WorseUp, BetterUp, Neutral:
		default:
			return fmt.Errorf("metric %s has invalid polarity %q (worse_up|better_up|neutral)", m.Metric, m.Polarity)
		}
		if m.ThresholdPct < 0 || m.ThresholdPct > 10000 {
			return fmt.Errorf("metric %s threshold_pct out of range", m.Metric)
		}
		if m.MinAbs < 0 {
			return fmt.Errorf("metric %s min_abs must not be negative", m.Metric)
		}
	}
	for name, p := range cf.Presets {
		if len(p.Metrics) == 0 {
			return fmt.Errorf("metric group %s must not be empty", name)
		}
		for _, m := range p.Metrics {
			if !metricNameRe.MatchString(m) {
				return fmt.Errorf("metric group %s has an invalid metric name: %q", name, m)
			}
		}
	}
	Categories = cf.Categories
	Catalog = cf.Metrics
	if cf.Presets != nil {
		TrendPresets = cf.Presets
	}
	rebuildIndex()
	return nil
}
