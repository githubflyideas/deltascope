package pcp

import (
	"encoding/json"
	"fmt"
	"os"
)

type RuleCond struct {
	Metric   string   `json:"metric"`
	Verdict  string   `json:"verdict,omitempty"`
	DeltaGte *float64 `json:"delta_gte,omitempty"`
	DeltaLte *float64 `json:"delta_lte,omitempty"`
	BGte     *float64 `json:"b_gte,omitempty"`
	BGtZero  bool     `json:"b_gt_zero,omitempty"`
	Appeared bool     `json:"appeared,omitempty"`
}

type Rule struct {
	ID         string     `json:"id"`
	Severity   string     `json:"severity"`
	Conclusion string     `json:"conclusion"`
	Next       []string   `json:"next,omitempty"`
	When       []RuleCond `json:"when"`
}

type Finding struct {
	ID         string   `json:"id"`
	Severity   string   `json:"severity"`
	Conclusion string   `json:"conclusion"`
	Evidence   []string `json:"evidence"`
	Next       []string `json:"next,omitempty"`
}

func fp(v float64) *float64 { return &v }

var Rules = []Rule{
	{"swap-spiral", "crit", "内存不足已触发换页:可用内存下跌且 swap 换入/换出同时活跃,任务会被换页 I/O 拖慢",
		[]string{"free -m", "ps aux --sort=-rss | head -15", "sar -B 1 5"},
		[]RuleCond{{Metric: "swap.pagesout", Verdict: "worse", BGtZero: true}, {Metric: "mem.util.available", Verdict: "worse"}}},
	{"direct-reclaim", "crit", "出现直接内存回收:分配路径上同步回收,B 窗口内所有内存分配都可能被卡顿",
		[]string{"sar -B 1 5", "grep -E 'pgscan|allocstall' /proc/vmstat"},
		[]RuleCond{{Metric: "mem.vmstat.pgscan_direct", Verdict: "worse", BGtZero: true}}},
	{"oom", "crit", "B 窗口发生 OOM Kill:内核已经在杀进程,这是内存问题的终点信号",
		[]string{"dmesg -T | grep -i 'out of memory'", "journalctl -k --since '-1 day' | grep -i oom"},
		[]RuleCond{{Metric: "mem.vmstat.oom_kill", BGtZero: true, Verdict: "worse"}}},
	{"disk-saturated", "crit", "存在饱和的磁盘:活跃时间与队列深度同时恶化,I/O 请求在排队",
		[]string{"iostat -x 1 5", "pidstat -d 1 5"},
		[]RuleCond{{Metric: "disk.dev.avactive", Verdict: "worse"}, {Metric: "disk.dev.aveq", Verdict: "worse"}}},
	{"iowait-drag", "warn", "I/O 等待拖累 CPU:CPU 空转等盘,瓶颈在存储不在算力",
		[]string{"iostat -x 1 5", "iotop -o"},
		[]RuleCond{{Metric: "kernel.all.cpu.wait.total", Verdict: "worse"}, {Metric: "disk.all.avactive", Verdict: "worse"}}},
	{"accept-overflow", "crit", "TCP 全连接队列溢出:应用 accept 不过来,新连接被丢,前端会看到超时",
		[]string{"ss -lnt", "netstat -s | grep -i 'listen'", "检查应用 backlog 与 net.core.somaxconn"},
		[]RuleCond{{Metric: "network.tcp.listendrops", BGtZero: true}}},
	{"syn-pressure", "warn", "SYN Cookie 被激活:半连接队列承压,可能是突发流量或 SYN 攻击",
		[]string{"ss -s", "netstat -s | grep -i syn", "检查 net.ipv4.tcp_max_syn_backlog"},
		[]RuleCond{{Metric: "network.tcp.syncookiessent", Verdict: "worse", BGtZero: true}}},
	{"softnet-drop", "crit", "软中断收包丢弃:内核收包预算耗尽,网卡收上来的包在软中断层被丢",
		[]string{"cat /proc/net/softnet_stat", "检查 RSS/RPS 配置与 net.core.netdev_budget"},
		[]RuleCond{{Metric: "network.softnet.dropped", Verdict: "worse", BGtZero: true}}},
	{"single-core-hot", "warn", "存在单核热点:负载集中在个别核心(常见于中断/软中断绑核不均)",
		[]string{"mpstat -P ALL 1 5", "cat /proc/interrupts | sort -k2 -nr | head"},
		[]RuleCond{{Metric: "kernel.percpu.cpu.irq.soft", Verdict: "worse", DeltaGte: fp(200)}}},
	{"steal", "warn", "CPU 被宿主机抢占 (steal) 显著上升:邻居虚机在争抢物理核,本机无解",
		[]string{"vmstat 1 5 (st 列)", "联系云平台或迁移实例"},
		[]RuleCond{{Metric: "kernel.all.cpu.steal", Verdict: "worse", DeltaGte: fp(100)}}},
	{"retrans", "warn", "TCP 重传显著上升:链路质量劣化或对端拥塞",
		[]string{"ss -ti | grep -B1 retrans", "mtr -rw <对端>", "检查网卡 errors/drops"},
		[]RuleCond{{Metric: "network.tcp.retranssegs", Verdict: "worse", DeltaGte: fp(100)}}},
	{"fs-filling", "crit", "有分区接近写满:使用率超过 90% 且仍在恶化",
		[]string{"df -h", "du -x --max-depth=1 /挂载点 | sort -rh | head"},
		[]RuleCond{{Metric: "filesys.full", Verdict: "worse", BGte: fp(90)}}},
	{"inode-growth", "warn", "inode 消耗异常增长:通常是海量小文件(日志碎片、会话文件、临时文件)",
		[]string{"df -i", "find /var -xdev -type f | head -100000 | awk -F/ '{print $3}' | sort | uniq -c | sort -rn | head"},
		[]RuleCond{{Metric: "filesys.usedfiles", Verdict: "worse", DeltaGte: fp(50)}}},
	{"mem-leak", "warn", "疑似进程内存泄漏:可用内存持续下行且匿名页同步上行",
		[]string{"ps aux --sort=-rss | head -15", "smem -rk | head"},
		[]RuleCond{{Metric: "mem.util.available", Verdict: "worse"}, {Metric: "mem.util.anonpages", Verdict: "worse", DeltaGte: fp(30)}}},
	{"orphan-pileup", "warn", "孤儿 TCP 连接堆积:连接未正常收尾,常见于对端异常断开或应用未关闭 socket",
		[]string{"ss -s", "ss -o state fin-wait-1"},
		[]RuleCond{{Metric: "network.sockstat.tcp.orphan", Verdict: "worse", BGtZero: true}}},
	{"rebooted", "info", "两个窗口之间机器发生过重启:uptime 显著回落,B 窗口是冷启动后的状态",
		[]string{"last reboot | head", "journalctl -b -1 -p err | tail -50"},
		[]RuleCond{{Metric: "kernel.all.uptime", DeltaLte: fp(-50)}}},
}

func EvaluateRules(rows []DiffRow) []Finding {
	byMetric := map[string][]DiffRow{}
	for _, r := range rows {
		byMetric[r.Metric] = append(byMetric[r.Metric], r)
	}
	var out []Finding
	for _, rule := range Rules {
		evidence := []string{}
		matched := true
		for _, c := range rule.When {
			hit := false
			for _, row := range byMetric[c.Metric] {
				if condMatch(c, row) {
					hit = true
					evidence = append(evidence, evidenceLine(row))
					break
				}
			}
			if !hit {
				matched = false
				break
			}
		}
		if matched {
			out = append(out, Finding{
				ID: rule.ID, Severity: rule.Severity, Conclusion: rule.Conclusion,
				Evidence: evidence, Next: rule.Next,
			})
		}
	}
	return out
}

func condMatch(c RuleCond, row DiffRow) bool {
	if c.Verdict != "" && string(row.Verdict) != c.Verdict {
		return false
	}
	if c.Appeared && !(row.A == nil && row.B != nil) {
		return false
	}
	if c.BGtZero && (row.B == nil || *row.B <= 0) {
		return false
	}
	if c.BGte != nil && (row.B == nil || *row.B < *c.BGte) {
		return false
	}
	if c.DeltaGte != nil && (row.DeltaPct == nil || *row.DeltaPct < *c.DeltaGte) {
		return false
	}
	if c.DeltaLte != nil && (row.DeltaPct == nil || *row.DeltaPct > *c.DeltaLte) {
		return false
	}
	return true
}

func evidenceLine(r DiffRow) string {
	name := r.Metric
	if r.Instance != "" {
		name += "[" + r.Instance + "]"
	}
	switch {
	case r.DeltaPct != nil:
		return fmt.Sprintf("%s Δ%+.1f%%", name, *r.DeltaPct)
	case r.A == nil && r.B != nil:
		return name + " 新出现"
	case r.B == nil:
		return name + " 消失"
	}
	return name
}

func ExportRules() ([]byte, error) {
	return json.MarshalIndent(Rules, "", "  ")
}

func LoadRulesFile(path string) error {
	raw, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	var rules []Rule
	if err := json.Unmarshal(raw, &rules); err != nil {
		return fmt.Errorf("解析规则文件失败: %w", err)
	}
	if len(rules) == 0 {
		return fmt.Errorf("规则文件为空")
	}
	seen := map[string]bool{}
	for _, r := range rules {
		if r.ID == "" || r.Conclusion == "" || len(r.When) == 0 {
			return fmt.Errorf("规则 %q 缺少 id/conclusion/when", r.ID)
		}
		if seen[r.ID] {
			return fmt.Errorf("规则 id 重复: %s", r.ID)
		}
		seen[r.ID] = true
		switch r.Severity {
		case "crit", "warn", "info":
		default:
			return fmt.Errorf("规则 %s 的 severity %q 无效 (crit|warn|info)", r.ID, r.Severity)
		}
		for _, c := range r.When {
			if !metricNameRe.MatchString(c.Metric) {
				return fmt.Errorf("规则 %s 含非法指标名: %q", r.ID, c.Metric)
			}
			switch c.Verdict {
			case "", "worse", "better", "watch", "flat":
			default:
				return fmt.Errorf("规则 %s 的 verdict %q 无效", r.ID, c.Verdict)
			}
		}
	}
	Rules = rules
	return nil
}
