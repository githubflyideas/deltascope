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
}

type Preset struct {
	Label   string   `json:"label"`
	Metrics []string `json:"metrics"`
}

var Categories = []string{"CPU", "内存", "磁盘 I/O", "文件系统", "网络"}

var Catalog = []MetricInfo{
	{"kernel.all.cpu.user", "用户态 CPU", "CPU", WorseUp},
	{"kernel.all.cpu.sys", "内核态 CPU", "CPU", WorseUp},
	{"kernel.all.cpu.nice", "低优先级 CPU", "CPU", Neutral},
	{"kernel.all.cpu.idle", "空闲 CPU", "CPU", BetterUp},
	{"kernel.all.cpu.wait.total", "I/O 等待", "CPU", WorseUp},
	{"kernel.all.cpu.irq.hard", "硬中断 CPU", "CPU", WorseUp},
	{"kernel.all.cpu.irq.soft", "软中断 CPU", "CPU", WorseUp},
	{"kernel.all.cpu.steal", "被抢占 (steal)", "CPU", WorseUp},
	{"kernel.all.cpu.guest", "Guest CPU", "CPU", Neutral},
	{"kernel.all.load", "系统负载", "CPU", WorseUp},
	{"kernel.all.runnable", "可运行任务数", "CPU", WorseUp},
	{"kernel.all.blocked", "I/O 阻塞任务数", "CPU", WorseUp},
	{"kernel.all.nprocs", "进程总数", "CPU", Neutral},
	{"kernel.all.pswitch", "上下文切换", "CPU", WorseUp},
	{"kernel.all.intr", "中断速率", "CPU", Neutral},
	{"kernel.all.sysfork", "进程创建 (fork)", "CPU", Neutral},
	{"kernel.all.uptime", "运行时长 (uptime)", "CPU", Neutral},

	{"mem.util.available", "可用内存", "内存", BetterUp},
	{"mem.util.free", "空闲内存", "内存", BetterUp},
	{"mem.util.cached", "页缓存", "内存", Neutral},
	{"mem.util.bufmem", "缓冲区", "内存", Neutral},
	{"mem.util.dirty", "脏页", "内存", WorseUp},
	{"mem.util.writeback", "回写中页", "内存", WorseUp},
	{"mem.util.slab", "slab", "内存", Neutral},
	{"mem.util.anonpages", "匿名页", "内存", Neutral},
	{"mem.util.mapped", "映射页", "内存", Neutral},
	{"mem.util.shmem", "共享内存", "内存", Neutral},
	{"mem.util.swapCached", "swap 缓存", "内存", Neutral},
	{"mem.util.committed_AS", "已承诺内存 (Committed_AS)", "内存", WorseUp},
	{"mem.util.pageTables", "页表占用", "内存", Neutral},
	{"swap.free", "可用 swap", "内存", BetterUp},
	{"swap.pagesin", "换入页 (swap in)", "内存", WorseUp},
	{"swap.pagesout", "换出页 (swap out)", "内存", WorseUp},
	{"mem.vmstat.pgmajfault", "主缺页 (major fault)", "内存", WorseUp},
	{"mem.vmstat.pgfault", "缺页总量", "内存", Neutral},
	{"mem.vmstat.oom_kill", "OOM Kill 次数", "内存", WorseUp},

	{"disk.all.read", "读 IOPS·全盘", "磁盘 I/O", Neutral},
	{"disk.all.write", "写 IOPS·全盘", "磁盘 I/O", Neutral},
	{"disk.all.read_bytes", "读吞吐·全盘", "磁盘 I/O", Neutral},
	{"disk.all.write_bytes", "写吞吐·全盘", "磁盘 I/O", Neutral},
	{"disk.all.total", "总 IOPS·全盘", "磁盘 I/O", Neutral},
	{"disk.all.read_merge", "读合并·全盘", "磁盘 I/O", Neutral},
	{"disk.all.write_merge", "写合并·全盘", "磁盘 I/O", Neutral},
	{"disk.all.avactive", "磁盘活跃时间·全盘", "磁盘 I/O", WorseUp},
	{"disk.all.aveq", "I/O 队列·全盘", "磁盘 I/O", WorseUp},
	{"disk.dev.read", "读 IOPS·分盘", "磁盘 I/O", Neutral},
	{"disk.dev.write", "写 IOPS·分盘", "磁盘 I/O", Neutral},
	{"disk.dev.read_bytes", "读吞吐·分盘", "磁盘 I/O", Neutral},
	{"disk.dev.write_bytes", "写吞吐·分盘", "磁盘 I/O", Neutral},
	{"disk.dev.total", "总 IOPS·分盘", "磁盘 I/O", Neutral},
	{"disk.dev.avactive", "磁盘活跃时间·分盘", "磁盘 I/O", WorseUp},
	{"disk.dev.aveq", "I/O 队列·分盘", "磁盘 I/O", WorseUp},

	{"filesys.full", "空间使用率", "文件系统", WorseUp},
	{"filesys.free", "剩余空间", "文件系统", BetterUp},
	{"filesys.usedfiles", "已用 inode", "文件系统", WorseUp},
	{"vfs.files.count", "打开文件数", "文件系统", Neutral},
	{"vfs.inodes.count", "内核 inode 缓存", "文件系统", Neutral},
	{"vfs.dentry.count", "dentry 缓存", "文件系统", Neutral},

	{"network.interface.in.bytes", "入向流量", "网络", Neutral},
	{"network.interface.out.bytes", "出向流量", "网络", Neutral},
	{"network.interface.in.packets", "入向包速率", "网络", Neutral},
	{"network.interface.out.packets", "出向包速率", "网络", Neutral},
	{"network.interface.in.errors", "入向错误", "网络", WorseUp},
	{"network.interface.out.errors", "出向错误", "网络", WorseUp},
	{"network.interface.in.drops", "入向丢包", "网络", WorseUp},
	{"network.interface.out.drops", "出向丢包", "网络", WorseUp},
	{"network.interface.collisions", "冲突", "网络", WorseUp},
	{"network.tcp.insegs", "TCP 入段", "网络", Neutral},
	{"network.tcp.outsegs", "TCP 出段", "网络", Neutral},
	{"network.tcp.retranssegs", "TCP 重传", "网络", WorseUp},
	{"network.tcp.inerrs", "TCP 入错误", "网络", WorseUp},
	{"network.tcp.outrsts", "TCP 出 RST", "网络", WorseUp},
	{"network.tcp.timeouts", "TCP 超时", "网络", WorseUp},
	{"network.tcp.currestab", "TCP 已建立连接", "网络", Neutral},
	{"network.tcp.activeopens", "TCP 主动建连", "网络", Neutral},
	{"network.tcp.passiveopens", "TCP 被动建连", "网络", Neutral},
	{"network.tcp.estabresets", "TCP 连接重置", "网络", WorseUp},
	{"network.tcp.attemptfails", "TCP 建连失败", "网络", WorseUp},
	{"network.tcp.listendrops", "TCP 监听丢弃", "网络", WorseUp},
	{"network.tcp.listenoverflows", "TCP 半连接队列溢出", "网络", WorseUp},
	{"network.tcp.syncookiessent", "SYN Cookie 发送", "网络", WorseUp},
	{"network.tcp.syncookiesrecv", "SYN Cookie 验证", "网络", Neutral},
	{"network.tcp.syncookiesfailed", "SYN Cookie 失败", "网络", WorseUp},
	{"network.udp.indatagrams", "UDP 入报文", "网络", Neutral},
	{"network.udp.outdatagrams", "UDP 出报文", "网络", Neutral},
	{"network.udp.inerrors", "UDP 入错误", "网络", WorseUp},
	{"network.udp.noports", "UDP 无监听端口", "网络", Neutral},
	{"network.udp.recvbuferrors", "UDP 收缓冲错误", "网络", WorseUp},
	{"network.udp.sndbuferrors", "UDP 发缓冲错误", "网络", WorseUp},
	{"network.sockstat.tcp.inuse", "TCP socket 使用", "网络", Neutral},
	{"network.sockstat.tcp.alloc", "TCP socket 分配", "网络", Neutral},
	{"network.sockstat.tcp.tw", "TIME-WAIT 连接", "网络", Neutral},
	{"network.sockstat.tcp.orphan", "孤儿连接", "网络", WorseUp},
	{"network.sockstat.udp.inuse", "UDP socket 使用", "网络", Neutral},
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
	"fs":   {"文件系统使用率(%)", []string{"filesys.full"}},
}

var catalogIndex map[string]MetricInfo

func rebuildIndex() {
	catalogIndex = make(map[string]MetricInfo, len(Catalog))
	for _, c := range Catalog {
		catalogIndex[c.Metric] = c
	}
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
