package pcp

// Polarity 描述指标数值上升时对系统健康的含义。
type Polarity string

const (
	WorseUp  Polarity = "worse_up"  // 数值上升 = 恶化(如 CPU 占用、重传)
	BetterUp Polarity = "better_up" // 数值上升 = 改善(如可用内存)
	Neutral  Polarity = "neutral"   // 本身中性,变化仅提示关注(如吞吐量)
)

// MetricInfo 是单个 PCP 指标的展示与判定元数据。
type MetricInfo struct {
	Metric   string   `json:"metric"`
	Label    string   `json:"label"`
	Category string   `json:"category"`
	Polarity Polarity `json:"polarity"`
}

// Categories 定义分类展示顺序。
var Categories = []string{"CPU", "内存", "磁盘 I/O", "网络"}

// Catalog 是对比引擎的指标白名单。任何进入 pmlogsummary 命令行的
// 指标名都必须来自这里,天然杜绝了参数注入。
var Catalog = []MetricInfo{
	// CPU
	{"kernel.all.cpu.user", "用户态 CPU", "CPU", WorseUp},
	{"kernel.all.cpu.sys", "内核态 CPU", "CPU", WorseUp},
	{"kernel.all.cpu.wait.total", "I/O 等待", "CPU", WorseUp},
	{"kernel.all.cpu.idle", "空闲 CPU", "CPU", BetterUp},
	{"kernel.all.load", "系统负载", "CPU", WorseUp},
	{"kernel.all.runnable", "可运行任务数", "CPU", WorseUp},
	{"kernel.all.pswitch", "上下文切换", "CPU", WorseUp},
	{"kernel.all.intr", "中断速率", "CPU", Neutral},

	// 内存
	{"mem.util.available", "可用内存", "内存", BetterUp},
	{"mem.util.free", "空闲内存", "内存", BetterUp},
	{"mem.util.cached", "页缓存", "内存", Neutral},
	{"swap.pagesout", "换出页(swap out)", "内存", WorseUp},
	{"swap.pagesin", "换入页(swap in)", "内存", WorseUp},
	{"mem.vmstat.pgmajfault", "主缺页(major fault)", "内存", WorseUp},

	// 磁盘 I/O
	{"disk.all.read_bytes", "读吞吐", "磁盘 I/O", Neutral},
	{"disk.all.write_bytes", "写吞吐", "磁盘 I/O", Neutral},
	{"disk.all.read", "读 IOPS", "磁盘 I/O", Neutral},
	{"disk.all.write", "写 IOPS", "磁盘 I/O", Neutral},
	{"disk.all.avactive", "磁盘活跃时间", "磁盘 I/O", WorseUp},
	{"disk.all.aveq", "平均 I/O 队列", "磁盘 I/O", WorseUp},

	// 网络
	{"network.interface.in.bytes", "入向流量", "网络", Neutral},
	{"network.interface.out.bytes", "出向流量", "网络", Neutral},
	{"network.tcp.retranssegs", "TCP 重传", "网络", WorseUp},
	{"network.tcp.currestab", "TCP 已建立连接", "网络", Neutral},
	{"network.interface.in.errors", "入向错误", "网络", WorseUp},
	{"network.interface.out.errors", "出向错误", "网络", WorseUp},
	{"network.interface.in.drops", "入向丢包", "网络", WorseUp},
}

// catalogIndex 按指标名快速查询。
var catalogIndex = func() map[string]MetricInfo {
	m := make(map[string]MetricInfo, len(Catalog))
	for _, c := range Catalog {
		m[c.Metric] = c
	}
	return m
}()

// Lookup 返回指标元数据。
func Lookup(metric string) (MetricInfo, bool) {
	c, ok := catalogIndex[metric]
	return c, ok
}

// DiffMetrics 返回参与对比的全部指标名(白名单本身)。
func DiffMetrics() []string {
	out := make([]string, 0, len(Catalog))
	for _, c := range Catalog {
		out = append(out, c.Metric)
	}
	return out
}

// TrendPresets 定义趋势图页面可选的指标组,同样是 pmrep 的白名单。
var TrendPresets = map[string]struct {
	Label   string
	Metrics []string
}{
	"cpu":  {"CPU 使用(ms/s)", []string{"kernel.all.cpu.user", "kernel.all.cpu.sys", "kernel.all.cpu.wait.total"}},
	"load": {"系统负载", []string{"kernel.all.load"}},
	"mem":  {"可用内存", []string{"mem.util.available"}},
	"disk": {"磁盘吞吐", []string{"disk.all.read_bytes", "disk.all.write_bytes"}},
	"net":  {"网卡流量", []string{"network.interface.in.bytes", "network.interface.out.bytes"}},
	"tcp":  {"TCP 重传/连接", []string{"network.tcp.retranssegs", "network.tcp.currestab"}},
}
