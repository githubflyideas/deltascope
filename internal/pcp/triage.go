package pcp

import "strconv"

// 分诊框架:把 146 项指标归到四大硬件资源 + "软件的鬼",
// 每块取其核心指标的最严重判定作为状态灯,避免次要计数器的抖动误报整块变红。

type TriageStatus string

const (
	TriageBad  TriageStatus = "bad"  // 红:核心指标恶化
	TriageWarn TriageStatus = "warn" // 黄:核心指标关注,或次要指标恶化
	TriageOK   TriageStatus = "ok"   // 绿:平稳
)

type TriageBlock struct {
	Key      string       `json:"key"`
	Label    string       `json:"label"`
	Status   TriageStatus `json:"status"`
	Headline string       `json:"headline"` // 一句话结论
	WorstPct *float64     `json:"worst_pct,omitempty"`
}

// resourceOf 把 catalog 分类映射到四大资源块。
func resourceOf(category string) string {
	switch category {
	case "CPU":
		return "cpu"
	case "内存":
		return "mem"
	case "磁盘 I/O", "文件系统":
		return "disk"
	case "网络":
		return "net"
	}
	return "other"
}

// coreMetrics 是每块资源的核心指标 —— 只有这些恶化才让整块亮红。
// 其余指标恶化只升到黄。这样避免 ICMP、页激活这类高抖动项误伤整块状态。
var coreMetrics = map[string]bool{
	// CPU:真正代表"算力紧张"的
	"kernel.all.cpu.user":       true,
	"kernel.all.cpu.sys":        true,
	"kernel.all.cpu.wait.total": true,
	"kernel.all.cpu.steal":      true,
	"kernel.all.load":           true,
	"kernel.all.runnable":       true,
	"kernel.all.pressure.cpu.some.avg": true,
	// 内存:代表"内存不足"的
	"mem.util.available":                  true,
	"swap.pagesin":                        true,
	"swap.pagesout":                       true,
	"mem.vmstat.pgscan_direct":            true,
	"mem.vmstat.oom_kill":                 true,
	"kernel.all.pressure.memory.some.avg": true,
	// 磁盘:代表"磁盘顶死/写满"的
	"disk.all.avactive":               true,
	"disk.all.aveq":                   true,
	"disk.dev.avactive":               true,
	"disk.dev.aveq":                   true,
	"filesys.full":                    true,
	"kernel.all.pressure.io.some.avg": true,
	// 网络:代表"链路/队列出问题"的
	"network.tcp.retranssegs":     true,
	"network.tcp.listendrops":     true,
	"network.tcp.listenoverflows": true,
	"network.softnet.dropped":     true,
	"network.interface.in.drops":  true,
	"network.interface.out.drops": true,
	"network.interface.in.errors": true,
}

var triageLabels = map[string]string{
	"cpu": "CPU", "mem": "内存", "disk": "磁盘", "net": "网卡",
}

// Triage 从 diff 报告生成四大资源的分诊摘要。
// "软件的鬼"块由调用方结合进程/变更对账单独填充。
func Triage(rows []DiffRow) []TriageBlock {
	type acc struct {
		worstBad  *DiffRow
		worstWarn *DiffRow
		worstAny  float64
	}
	blocks := map[string]*acc{"cpu": {}, "mem": {}, "disk": {}, "net": {}}

	for i := range rows {
		r := rows[i]
		res := resourceOf(r.Category)
		a := blocks[res]
		if a == nil {
			continue
		}
		core := coreMetrics[r.Metric]
		switch r.Verdict {
		case VWorse:
			if core {
				if a.worstBad == nil || absDeltaVal(r) > absDeltaVal(*a.worstBad) {
					rr := r
					a.worstBad = &rr
				}
			} else if a.worstWarn == nil {
				rr := r
				a.worstWarn = &rr
			}
		case VWatch:
			if a.worstWarn == nil {
				rr := r
				a.worstWarn = &rr
			}
		}
	}

	order := []string{"cpu", "mem", "disk", "net"}
	out := make([]TriageBlock, 0, 4)
	for _, k := range order {
		a := blocks[k]
		b := TriageBlock{Key: k, Label: triageLabels[k], Status: TriageOK, Headline: "正常"}
		switch {
		case a.worstBad != nil:
			b.Status = TriageBad
			b.Headline = headline(*a.worstBad)
			b.WorstPct = a.worstBad.DeltaPct
		case a.worstWarn != nil:
			b.Status = TriageWarn
			b.Headline = headline(*a.worstWarn)
			b.WorstPct = a.worstWarn.DeltaPct
		}
		out = append(out, b)
	}
	return out
}

func headline(r DiffRow) string {
	label := r.Label
	if r.Instance != "" {
		label += "[" + r.Instance + "]"
	}
	if r.DeltaPct == nil {
		if r.A == nil && r.B != nil {
			return label + " 新出现"
		}
		return label + " 异常"
	}
	sign := "+"
	if *r.DeltaPct < 0 {
		sign = ""
	}
	return label + " " + sign + formatPct(*r.DeltaPct)
}


func formatPct(v float64) string {
	if v > 999 || v < -999 {
		return "剧变"
	}
	return strconv.Itoa(int(v)) + "%"
}

func absDeltaVal(r DiffRow) float64 {
	if r.DeltaPct == nil {
		return 1e18
	}
	if *r.DeltaPct < 0 {
		return -*r.DeltaPct
	}
	return *r.DeltaPct
}
