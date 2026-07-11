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
	Metric   string   `json:"metric"`
	Label    string   `json:"label"`
	Category string   `json:"category"`
	Polarity Polarity `json:"polarity"`
	Fold     bool     `json:"fold,omitempty"`
}

type Preset struct {
	Label   string   `json:"label"`
	Metrics []string `json:"metrics"`
}

var Categories = []string{"CPU", "内存", "磁盘 I/O", "文件系统", "网络"}

var Catalog = []MetricInfo{
	{"kernel.all.cpu.user", "用户态 CPU", "CPU", WorseUp, false},
	{"kernel.all.cpu.sys", "内核态 CPU", "CPU", WorseUp, false},
	{"kernel.all.cpu.nice", "低优先级 CPU", "CPU", Neutral, false},
	{"kernel.all.cpu.idle", "空闲 CPU", "CPU", BetterUp, false},
	{"kernel.all.cpu.wait.total", "I/O 等待", "CPU", WorseUp, false},
	{"kernel.all.cpu.irq.hard", "硬中断 CPU", "CPU", WorseUp, false},
	{"kernel.all.cpu.irq.soft", "软中断 CPU", "CPU", WorseUp, false},
	{"kernel.all.cpu.steal", "被抢占 (steal)", "CPU", WorseUp, false},
	{"kernel.all.cpu.guest", "Guest CPU", "CPU", Neutral, false},
	{"kernel.all.load", "系统负载", "CPU", WorseUp, false},
	{"kernel.all.runnable", "可运行任务数", "CPU", WorseUp, false},
	{"kernel.all.blocked", "I/O 阻塞任务数", "CPU", WorseUp, false},
	{"kernel.all.nprocs", "进程总数", "CPU", Neutral, false},
	{"kernel.all.pswitch", "上下文切换", "CPU", WorseUp, false},
	{"kernel.all.intr", "中断速率", "CPU", Neutral, false},
	{"kernel.all.sysfork", "进程创建 (fork)", "CPU", Neutral, false},
	{"kernel.all.uptime", "运行时长 (uptime)", "CPU", Neutral, false},
	{"kernel.all.entropy.avail", "熵池可用位", "CPU", Neutral, false},
	{"kernel.all.pressure.cpu.some.avg", "CPU 压力 (PSI some)", "CPU", WorseUp, false},
	{"kernel.percpu.cpu.user", "用户态 CPU·每核", "CPU", WorseUp, false},
	{"kernel.percpu.cpu.sys", "内核态 CPU·每核", "CPU", WorseUp, false},
	{"kernel.percpu.cpu.wait.total", "I/O 等待·每核", "CPU", WorseUp, false},
	{"kernel.percpu.cpu.irq.soft", "软中断·每核", "CPU", WorseUp, false},

	{"mem.util.available", "可用内存", "内存", BetterUp, false},
	{"mem.util.free", "空闲内存", "内存", BetterUp, false},
	{"mem.util.cached", "页缓存", "内存", Neutral, false},
	{"mem.util.bufmem", "缓冲区", "内存", Neutral, false},
	{"mem.util.dirty", "脏页", "内存", WorseUp, false},
	{"mem.util.writeback", "回写中页", "内存", WorseUp, false},
	{"mem.util.slab", "slab", "内存", Neutral, false},
	{"mem.util.anonpages", "匿名页", "内存", Neutral, false},
	{"mem.util.mapped", "映射页", "内存", Neutral, false},
	{"mem.util.shmem", "共享内存", "内存", Neutral, false},
	{"mem.util.swapCached", "swap 缓存", "内存", Neutral, false},
	{"mem.util.committed_AS", "已承诺内存 (Committed_AS)", "内存", WorseUp, false},
	{"mem.util.pageTables", "页表占用", "内存", Neutral, false},
	{"swap.free", "可用 swap", "内存", BetterUp, false},
	{"swap.pagesin", "换入页 (swap in)", "内存", WorseUp, false},
	{"swap.pagesout", "换出页 (swap out)", "内存", WorseUp, false},
	{"mem.vmstat.pgmajfault", "主缺页 (major fault)", "内存", WorseUp, false},
	{"mem.vmstat.pgfault", "缺页总量", "内存", Neutral, false},
	{"mem.vmstat.oom_kill", "OOM Kill 次数", "内存", WorseUp, false},
	{"mem.vmstat.pgscan_direct", "直接回收扫描", "内存", WorseUp, false},
	{"mem.vmstat.pgscan_kswapd", "kswapd 回收扫描", "内存", WorseUp, false},
	{"mem.vmstat.allocstall", "分配停顿 (allocstall)", "内存", WorseUp, false},
	{"mem.vmstat.compact_stall", "内存规整停顿", "内存", WorseUp, false},
	{"mem.vmstat.thp_fault_alloc", "THP 缺页分配", "内存", Neutral, false},
	{"mem.vmstat.thp_collapse_alloc", "THP 合并分配", "内存", Neutral, false},
	{"mem.vmstat.pgactivate", "页激活", "内存", Neutral, false},
	{"mem.vmstat.pgdeactivate", "页去激活", "内存", Neutral, false},
	{"kernel.all.pressure.memory.some.avg", "内存压力 (PSI some)", "内存", WorseUp, false},
	{"kernel.all.pressure.memory.full.avg", "内存压力 (PSI full)", "内存", WorseUp, false},

	{"disk.all.read", "读 IOPS·全盘", "磁盘 I/O", Neutral, false},
	{"disk.all.write", "写 IOPS·全盘", "磁盘 I/O", Neutral, false},
	{"disk.all.read_bytes", "读吞吐·全盘", "磁盘 I/O", Neutral, false},
	{"disk.all.write_bytes", "写吞吐·全盘", "磁盘 I/O", Neutral, false},
	{"disk.all.total", "总 IOPS·全盘", "磁盘 I/O", Neutral, false},
	{"disk.all.read_merge", "读合并·全盘", "磁盘 I/O", Neutral, false},
	{"disk.all.write_merge", "写合并·全盘", "磁盘 I/O", Neutral, false},
	{"disk.all.avactive", "磁盘活跃时间·全盘", "磁盘 I/O", WorseUp, false},
	{"disk.all.aveq", "I/O 队列·全盘", "磁盘 I/O", WorseUp, false},
	{"disk.dev.read", "读 IOPS·分盘", "磁盘 I/O", Neutral, false},
	{"disk.dev.write", "写 IOPS·分盘", "磁盘 I/O", Neutral, false},
	{"disk.dev.read_bytes", "读吞吐·分盘", "磁盘 I/O", Neutral, false},
	{"disk.dev.write_bytes", "写吞吐·分盘", "磁盘 I/O", Neutral, false},
	{"disk.dev.total", "总 IOPS·分盘", "磁盘 I/O", Neutral, false},
	{"disk.dev.avactive", "磁盘活跃时间·分盘", "磁盘 I/O", WorseUp, false},
	{"disk.dev.aveq", "I/O 队列·分盘", "磁盘 I/O", WorseUp, false},
	{"disk.dm.read", "读 IOPS·LVM", "磁盘 I/O", Neutral, false},
	{"disk.dm.write", "写 IOPS·LVM", "磁盘 I/O", Neutral, false},
	{"disk.dm.read_bytes", "读吞吐·LVM", "磁盘 I/O", Neutral, false},
	{"disk.dm.write_bytes", "写吞吐·LVM", "磁盘 I/O", Neutral, false},
	{"disk.dm.avactive", "活跃时间·LVM", "磁盘 I/O", WorseUp, false},
	{"disk.dm.aveq", "I/O 队列·LVM", "磁盘 I/O", WorseUp, false},
	{"disk.md.read", "读 IOPS·MD RAID", "磁盘 I/O", Neutral, false},
	{"disk.md.write", "写 IOPS·MD RAID", "磁盘 I/O", Neutral, false},
	{"disk.md.read_bytes", "读吞吐·MD RAID", "磁盘 I/O", Neutral, false},
	{"disk.md.write_bytes", "写吞吐·MD RAID", "磁盘 I/O", Neutral, false},
	{"kernel.all.pressure.io.some.avg", "I/O 压力 (PSI some)", "磁盘 I/O", WorseUp, false},
	{"kernel.all.pressure.io.full.avg", "I/O 压力 (PSI full)", "磁盘 I/O", WorseUp, false},

	{"filesys.full", "空间使用率", "文件系统", WorseUp, false},
	{"filesys.free", "剩余空间", "文件系统", BetterUp, false},
	{"filesys.usedfiles", "已用 inode", "文件系统", WorseUp, false},
	{"vfs.files.count", "打开文件数", "文件系统", Neutral, false},
	{"vfs.inodes.count", "内核 inode 缓存", "文件系统", Neutral, false},
	{"vfs.dentry.count", "dentry 缓存", "文件系统", Neutral, false},
	{"filesys.avail", "非 root 可用空间", "文件系统", BetterUp, false},

	{"network.interface.in.bytes", "入向流量", "网络", Neutral, false},
	{"network.interface.out.bytes", "出向流量", "网络", Neutral, false},
	{"network.interface.in.packets", "入向包速率", "网络", Neutral, false},
	{"network.interface.out.packets", "出向包速率", "网络", Neutral, false},
	{"network.interface.in.errors", "入向错误", "网络", WorseUp, false},
	{"network.interface.out.errors", "出向错误", "网络", WorseUp, false},
	{"network.interface.in.drops", "入向丢包", "网络", WorseUp, false},
	{"network.interface.out.drops", "出向丢包", "网络", WorseUp, false},
	{"network.interface.collisions", "冲突", "网络", WorseUp, false},
	{"network.tcp.insegs", "TCP 入段", "网络", Neutral, false},
	{"network.tcp.outsegs", "TCP 出段", "网络", Neutral, false},
	{"network.tcp.retranssegs", "TCP 重传", "网络", WorseUp, false},
	{"network.tcp.inerrs", "TCP 入错误", "网络", WorseUp, false},
	{"network.tcp.outrsts", "TCP 出 RST", "网络", WorseUp, false},
	{"network.tcp.timeouts", "TCP 超时", "网络", WorseUp, false},
	{"network.tcp.currestab", "TCP 已建立连接", "网络", Neutral, false},
	{"network.tcp.activeopens", "TCP 主动建连", "网络", Neutral, false},
	{"network.tcp.passiveopens", "TCP 被动建连", "网络", Neutral, false},
	{"network.tcp.estabresets", "TCP 连接重置", "网络", WorseUp, false},
	{"network.tcp.attemptfails", "TCP 建连失败", "网络", WorseUp, false},
	{"network.tcp.listendrops", "TCP 监听丢弃", "网络", WorseUp, false},
	{"network.tcp.listenoverflows", "TCP 半连接队列溢出", "网络", WorseUp, false},
	{"network.tcp.syncookiessent", "SYN Cookie 发送", "网络", WorseUp, false},
	{"network.tcp.syncookiesrecv", "SYN Cookie 验证", "网络", Neutral, false},
	{"network.tcp.syncookiesfailed", "SYN Cookie 失败", "网络", WorseUp, false},
	{"network.udp.indatagrams", "UDP 入报文", "网络", Neutral, false},
	{"network.udp.outdatagrams", "UDP 出报文", "网络", Neutral, false},
	{"network.udp.inerrors", "UDP 入错误", "网络", WorseUp, false},
	{"network.udp.noports", "UDP 无监听端口", "网络", Neutral, false},
	{"network.udp.recvbuferrors", "UDP 收缓冲错误", "网络", WorseUp, false},
	{"network.udp.sndbuferrors", "UDP 发缓冲错误", "网络", WorseUp, false},
	{"network.sockstat.tcp.inuse", "TCP socket 使用", "网络", Neutral, false},
	{"network.sockstat.tcp.alloc", "TCP socket 分配", "网络", Neutral, false},
	{"network.sockstat.tcp.tw", "TIME-WAIT 连接", "网络", Neutral, false},
	{"network.sockstat.tcp.orphan", "孤儿连接", "网络", WorseUp, false},
	{"network.sockstat.udp.inuse", "UDP socket 使用", "网络", Neutral, false},
	{"network.softnet.dropped", "软中断收包丢弃", "网络", WorseUp, false},
	{"network.softnet.time_squeeze", "软中断时间片挤压", "网络", WorseUp, false},
	{"network.softnet.processed", "软中断处理包量", "网络", Neutral, false},
	{"network.tcpconn.established", "连接分布 ESTABLISHED", "网络", Neutral, false},
	{"network.tcpconn.time_wait", "连接分布 TIME-WAIT", "网络", Neutral, false},
	{"network.tcpconn.close_wait", "连接分布 CLOSE-WAIT", "网络", WorseUp, false},
	{"network.tcpconn.syn_recv", "连接分布 SYN-RECV", "网络", WorseUp, false},
	{"network.tcpconn.listen", "连接分布 LISTEN", "网络", Neutral, false},
	{"network.tcp.prunecalled", "接收缓冲修剪", "网络", WorseUp, false},
	{"network.tcp.rcvcollapsed", "接收队列折叠", "网络", WorseUp, false},
	{"network.tcp.delayedacks", "延迟 ACK", "网络", Neutral, false},
	{"network.icmp.inmsgs", "ICMP 入消息", "网络", Neutral, false},
	{"network.icmp.outmsgs", "ICMP 出消息", "网络", Neutral, false},
	{"network.icmp.inerrors", "ICMP 入错误", "网络", WorseUp, false},
	{"network.icmp.indestunreachs", "ICMP 目标不可达", "网络", Neutral, false},
	{"network.ip.inreceives", "IP 入包", "网络", Neutral, false},
	{"network.ip.outrequests", "IP 出包", "网络", Neutral, false},
	{"network.ip.inhdrerrors", "IP 头错误", "网络", WorseUp, false},
	{"network.ip.indiscards", "IP 入丢弃", "网络", WorseUp, false},
	{"network.ip.outdiscards", "IP 出丢弃", "网络", WorseUp, false},
	{"network.ip.forwdatagrams", "IP 转发包", "网络", Neutral, false},
	{"network.ip.fragfails", "IP 分片失败", "网络", WorseUp, false},
	{"network.ip.reasmfails", "IP 重组失败", "网络", WorseUp, false},
}

var TrendPresets = map[string]Preset{
	"cpu":  {"CPU 使用(ms/s)", []string{"kernel.all.cpu.user", "kernel.all.cpu.sys", "kernel.all.cpu.wait.total", "kernel.all.cpu.steal"}},
	"load": {"系统负载", []string{"kernel.all.load"}},
	"mem":  {"可用内存", []string{"mem.util.available"}},
	"disk": {"磁盘吞吐", []string{"disk.all.read_bytes", "disk.all.write_bytes"}},
	"net":  {"网卡流量", []string{"network.interface.in.bytes", "network.interface.out.bytes"}},
	"tcp":  {"TCP 重传/连接", []string{"network.tcp.retranssegs", "network.tcp.currestab"}},
	"sock": {"TCP 连接状态", []string{"network.sockstat.tcp.alloc", "network.sockstat.tcp.tw", "network.sockstat.tcp.orphan"}},
	"syn":  {"SYN 压力", []string{"network.tcp.syncookiessent", "network.tcp.listendrops", "network.tcp.listenoverflows"}},
	"psi":  {"压力 PSI (some)", []string{"kernel.all.pressure.cpu.some.avg", "kernel.all.pressure.memory.some.avg", "kernel.all.pressure.io.some.avg"}},
	"fs":   {"文件系统使用率(%)", []string{"filesys.full"}},
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
		return fmt.Errorf("解析指标目录失败: %w", err)
	}
	if len(cf.Categories) == 0 || len(cf.Metrics) == 0 {
		return fmt.Errorf("指标目录必须包含 categories 与 metrics")
	}
	cats := map[string]bool{}
	for _, c := range cf.Categories {
		if c == "" {
			return fmt.Errorf("分类名不能为空")
		}
		cats[c] = true
	}
	for _, m := range cf.Metrics {
		if !metricNameRe.MatchString(m.Metric) {
			return fmt.Errorf("非法指标名: %q", m.Metric)
		}
		if !cats[m.Category] {
			return fmt.Errorf("指标 %s 引用了未声明的分类 %q", m.Metric, m.Category)
		}
		switch m.Polarity {
		case WorseUp, BetterUp, Neutral:
		default:
			return fmt.Errorf("指标 %s 的极性 %q 无效 (worse_up|better_up|neutral)", m.Metric, m.Polarity)
		}
	}
	for name, p := range cf.Presets {
		if len(p.Metrics) == 0 {
			return fmt.Errorf("指标组 %s 不能为空", name)
		}
		for _, m := range p.Metrics {
			if !metricNameRe.MatchString(m) {
				return fmt.Errorf("指标组 %s 含非法指标名: %q", name, m)
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
