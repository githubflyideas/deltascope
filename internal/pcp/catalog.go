package pcp

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
	{"kernel.all.load", "系统负载", "CPU", WorseUp},
	{"kernel.all.runnable", "可运行任务数", "CPU", WorseUp},
	{"kernel.all.pswitch", "上下文切换", "CPU", WorseUp},
	{"kernel.all.intr", "中断速率", "CPU", Neutral},
	{"kernel.all.sysfork", "进程创建 (fork)", "CPU", Neutral},

	{"mem.util.available", "可用内存", "内存", BetterUp},
	{"mem.util.free", "空闲内存", "内存", BetterUp},
	{"mem.util.cached", "页缓存", "内存", Neutral},
	{"mem.util.bufmem", "缓冲区", "内存", Neutral},
	{"mem.util.dirty", "脏页", "内存", WorseUp},
	{"mem.util.writeback", "回写中页", "内存", WorseUp},
	{"mem.util.slab", "slab", "内存", Neutral},
	{"mem.util.anonpages", "匿名页", "内存", Neutral},
	{"swap.free", "可用 swap", "内存", BetterUp},
	{"swap.pagesin", "换入页 (swap in)", "内存", WorseUp},
	{"swap.pagesout", "换出页 (swap out)", "内存", WorseUp},
	{"mem.vmstat.pgmajfault", "主缺页 (major fault)", "内存", WorseUp},
	{"mem.vmstat.pgfault", "缺页总量", "内存", Neutral},

	{"disk.all.read", "读 IOPS·全盘", "磁盘 I/O", Neutral},
	{"disk.all.write", "写 IOPS·全盘", "磁盘 I/O", Neutral},
	{"disk.all.read_bytes", "读吞吐·全盘", "磁盘 I/O", Neutral},
	{"disk.all.write_bytes", "写吞吐·全盘", "磁盘 I/O", Neutral},
	{"disk.all.total", "总 IOPS·全盘", "磁盘 I/O", Neutral},
	{"disk.all.avactive", "磁盘活跃时间·全盘", "磁盘 I/O", WorseUp},
	{"disk.all.aveq", "I/O 队列·全盘", "磁盘 I/O", WorseUp},
	{"disk.dev.read", "读 IOPS·分盘", "磁盘 I/O", Neutral},
	{"disk.dev.write", "写 IOPS·分盘", "磁盘 I/O", Neutral},
	{"disk.dev.read_bytes", "读吞吐·分盘", "磁盘 I/O", Neutral},
	{"disk.dev.write_bytes", "写吞吐·分盘", "磁盘 I/O", Neutral},
	{"disk.dev.avactive", "磁盘活跃时间·分盘", "磁盘 I/O", WorseUp},
	{"disk.dev.aveq", "I/O 队列·分盘", "磁盘 I/O", WorseUp},

	{"filesys.full", "空间使用率", "文件系统", WorseUp},
	{"vfs.files.count", "打开文件数", "文件系统", Neutral},

	{"network.interface.in.bytes", "入向流量", "网络", Neutral},
	{"network.interface.out.bytes", "出向流量", "网络", Neutral},
	{"network.interface.in.packets", "入向包速率", "网络", Neutral},
	{"network.interface.out.packets", "出向包速率", "网络", Neutral},
	{"network.interface.in.errors", "入向错误", "网络", WorseUp},
	{"network.interface.out.errors", "出向错误", "网络", WorseUp},
	{"network.interface.in.drops", "入向丢包", "网络", WorseUp},
	{"network.interface.out.drops", "出向丢包", "网络", WorseUp},
	{"network.interface.collisions", "冲突", "网络", WorseUp},
	{"network.tcp.retranssegs", "TCP 重传", "网络", WorseUp},
	{"network.tcp.currestab", "TCP 已建立连接", "网络", Neutral},
	{"network.tcp.activeopens", "TCP 主动建连", "网络", Neutral},
	{"network.tcp.passiveopens", "TCP 被动建连", "网络", Neutral},
	{"network.tcp.estabresets", "TCP 连接重置", "网络", WorseUp},
	{"network.tcp.attemptfails", "TCP 建连失败", "网络", WorseUp},
	{"network.udp.indatagrams", "UDP 入报文", "网络", Neutral},
	{"network.udp.outdatagrams", "UDP 出报文", "网络", Neutral},
	{"network.udp.inerrors", "UDP 入错误", "网络", WorseUp},
}

var catalogIndex = func() map[string]MetricInfo {
	m := make(map[string]MetricInfo, len(Catalog))
	for _, c := range Catalog {
		m[c.Metric] = c
	}
	return m
}()

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

var TrendPresets = map[string]struct {
	Label   string
	Metrics []string
}{
	"cpu":  {"CPU 使用(ms/s)", []string{"kernel.all.cpu.user", "kernel.all.cpu.sys", "kernel.all.cpu.wait.total", "kernel.all.cpu.steal"}},
	"load": {"系统负载", []string{"kernel.all.load"}},
	"mem":  {"可用内存", []string{"mem.util.available"}},
	"disk": {"磁盘吞吐", []string{"disk.all.read_bytes", "disk.all.write_bytes"}},
	"net":  {"网卡流量", []string{"network.interface.in.bytes", "network.interface.out.bytes"}},
	"tcp":  {"TCP 重传/连接", []string{"network.tcp.retranssegs", "network.tcp.currestab"}},
	"fs":   {"文件系统使用率(%)", []string{"filesys.full"}},
}
