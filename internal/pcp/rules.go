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
	{"swap-spiral", "crit", "Memory pressure has triggered swapping: available memory is falling while swap in/out are both active, so tasks will stall on swap I/O",
		[]string{"free -m", "ps aux --sort=-rss | head -15", "sar -B 1 5"},
		[]RuleCond{{Metric: "swap.pagesout", Verdict: "worse", BGtZero: true}, {Metric: "mem.util.available", Verdict: "worse"}}},
	{"direct-reclaim", "crit", "Direct memory reclaim is occurring: reclaim is happening synchronously in the allocation path, so any allocation in window B can stall",
		[]string{"sar -B 1 5", "grep -E 'pgscan|allocstall' /proc/vmstat"},
		[]RuleCond{{Metric: "mem.vmstat.pgscan_direct", Verdict: "worse", BGtZero: true}}},
	{"oom", "crit", "An OOM kill occurred in window B: the kernel is killing processes, the terminal signal of a memory problem",
		[]string{"dmesg -T | grep -i 'out of memory'", "journalctl -k --since '-1 day' | grep -i oom"},
		[]RuleCond{{Metric: "mem.vmstat.oom_kill", BGtZero: true, Verdict: "worse"}}},
	{"disk-saturated", "crit", "A disk is saturated: active time and queue depth have both worsened, I/O requests are queuing up",
		[]string{"iostat -x 1 5", "pidstat -d 1 5"},
		[]RuleCond{{Metric: "disk.dev.avactive", Verdict: "worse"}, {Metric: "disk.dev.aveq", Verdict: "worse"}}},
	{"iowait-drag", "warn", "I/O wait is dragging down the CPU: the CPU is idling on disk, the bottleneck is storage not compute",
		[]string{"iostat -x 1 5", "iotop -o"},
		[]RuleCond{{Metric: "kernel.all.cpu.wait.total", Verdict: "worse"}, {Metric: "disk.all.avactive", Verdict: "worse"}}},
	{"accept-overflow", "crit", "TCP accept queue overflow: the app can't accept fast enough, new connections are being dropped, clients will see timeouts",
		[]string{"ss -lnt", "netstat -s | grep -i 'listen'", "check app backlog and net.core.somaxconn"},
		[]RuleCond{{Metric: "network.tcp.listendrops", BGtZero: true}}},
	{"syn-pressure", "warn", "SYN cookies have kicked in: the half-open queue is under pressure, likely a traffic burst or a SYN flood",
		[]string{"ss -s", "netstat -s | grep -i syn", "check net.ipv4.tcp_max_syn_backlog"},
		[]RuleCond{{Metric: "network.tcp.syncookiessent", Verdict: "worse", BGtZero: true}}},
	{"softnet-drop", "crit", "Softirq packet drops: the kernel's receive budget is exhausted, incoming packets are being dropped at the softirq layer",
		[]string{"cat /proc/net/softnet_stat", "check RSS/RPS config and net.core.netdev_budget"},
		[]RuleCond{{Metric: "network.softnet.dropped", Verdict: "worse", BGtZero: true}}},
	{"single-core-hot", "warn", "A single-core hotspot: load is concentrated on a few cores (often uneven IRQ/softirq affinity)",
		[]string{"mpstat -P ALL 1 5", "cat /proc/interrupts | sort -k2 -nr | head"},
		[]RuleCond{{Metric: "kernel.percpu.cpu.irq.soft", Verdict: "worse", DeltaGte: fp(200)}}},
	{"steal", "warn", "CPU steal has risen significantly: a noisy neighbor VM is contending for physical cores, nothing to fix locally",
		[]string{"vmstat 1 5 (st column)", "contact the cloud provider or migrate the instance"},
		[]RuleCond{{Metric: "kernel.all.cpu.steal", Verdict: "worse", DeltaGte: fp(100)}}},
	{"retrans", "warn", "TCP retransmits have risen significantly: link quality has degraded or the peer is congested",
		[]string{"ss -ti | grep -B1 retrans", "mtr -rw <peer>", "check NIC errors/drops"},
		[]RuleCond{{Metric: "network.tcp.retranssegs", Verdict: "worse", DeltaGte: fp(100)}}},
	{"fs-filling", "crit", "A partition is nearly full: usage is above 90% and still worsening",
		[]string{"df -h", "du -x --max-depth=1 /mountpoint | sort -rh | head"},
		[]RuleCond{{Metric: "filesys.full", Verdict: "worse", BGte: fp(90)}}},
	{"inode-growth", "warn", "Abnormal inode growth: usually a large number of small files (log fragments, session files, temp files)",
		[]string{"df -i", "find /var -xdev -type f | head -100000 | awk -F/ '{print $3}' | sort | uniq -c | sort -rn | head"},
		[]RuleCond{{Metric: "filesys.usedfiles", Verdict: "worse", DeltaGte: fp(50)}}},
	{"mem-leak", "warn", "Suspected process memory leak: available memory is trending down while anonymous pages trend up in step",
		[]string{"ps aux --sort=-rss | head -15", "smem -rk | head"},
		[]RuleCond{{Metric: "mem.util.available", Verdict: "worse"}, {Metric: "mem.util.anonpages", DeltaGte: fp(30)}}},
	{"orphan-pileup", "warn", "Orphan TCP connections piling up: connections aren't closing cleanly, often the peer disconnecting abnormally or the app not closing sockets",
		[]string{"ss -s", "ss -o state fin-wait-1"},
		[]RuleCond{{Metric: "network.sockstat.tcp.orphan", Verdict: "worse", BGtZero: true}}},
	{"rebooted", "info", "The machine rebooted between the two windows: uptime dropped sharply, window B reflects post-boot state",
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
		return name + " (new)"
	case r.B == nil:
		return name + " (gone)"
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
		return fmt.Errorf("failed to parse rules file: %w", err)
	}
	if len(rules) == 0 {
		return fmt.Errorf("rules file is empty")
	}
	seen := map[string]bool{}
	for _, r := range rules {
		if r.ID == "" || r.Conclusion == "" || len(r.When) == 0 {
			return fmt.Errorf("rule %q is missing id/conclusion/when", r.ID)
		}
		if seen[r.ID] {
			return fmt.Errorf("duplicate rule id: %s", r.ID)
		}
		seen[r.ID] = true
		switch r.Severity {
		case "crit", "warn", "info":
		default:
			return fmt.Errorf("rule %s has invalid severity %q (crit|warn|info)", r.ID, r.Severity)
		}
		for _, c := range r.When {
			if !metricNameRe.MatchString(c.Metric) {
				return fmt.Errorf("rule %s has an invalid metric name: %q", r.ID, c.Metric)
			}
			switch c.Verdict {
			case "", "worse", "better", "watch", "flat":
			default:
				return fmt.Errorf("rule %s has invalid verdict %q", r.ID, c.Verdict)
			}
		}
	}
	Rules = rules
	return nil
}
