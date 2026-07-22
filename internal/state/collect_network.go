package state

import (
	"context"
	"strings"
)

type network struct{}

func (network) Name() string { return "network" }
func (network) Collect(ctx context.Context) Section {
	sec := Section{Name: "network", Title: "网络配置"}

	if out, ok := runCmd(ctx, "ip", "route", "show"); ok {
		for _, l := range lines(out) {
			f := fields(l)
			if len(f) > 0 {
				sec.Items = append(sec.Items, Item{Key: "route:" + f[0], Value: l})
			}
		}
	} else if v, ok := readFile("/proc/net/route"); ok {
		for i, l := range lines(v) {
			if i == 0 {
				continue
			}
			f := fields(l)
			if len(f) >= 2 {
				sec.Items = append(sec.Items, Item{Key: "route:" + f[0] + ":" + f[1], Value: l})
			}
		}
	}

	if out, ok := runCmd(ctx, "ip", "-o", "addr", "show"); ok {
		for _, l := range lines(out) {
			f := fields(l)
			if len(f) >= 4 && (f[2] == "inet" || f[2] == "inet6") {
				sec.Items = append(sec.Items, Item{Key: "addr:" + f[1] + ":" + f[3], Value: f[2] + " " + f[3]})
			}
		}
	}

	if v, ok := readFile("/etc/resolv.conf"); ok {
		for _, l := range lines(v) {
			if strings.HasPrefix(l, "nameserver") || strings.HasPrefix(l, "search") {
				sec.Items = append(sec.Items, Item{Key: "resolv:" + l, Value: l})
			}
		}
	}
	if h, ok := fileHash("/etc/hosts"); ok {
		sec.Items = append(sec.Items, Item{Key: "hosts.hash", Value: h})
	}
	return sec
}

type listen struct{}

func (listen) Name() string { return "listen" }
func (listen) Collect(ctx context.Context) Section {
	sec := Section{Name: "listen", Title: "监听端口"}
	out, ok := runCmd(ctx, "ss", "-lntuHp")
	if !ok {
		if out, ok = runCmd(ctx, "ss", "-lntu"); !ok {
			sec.Skipped = "未找到 ss"
			return sec
		}
	}
	seen := map[string]bool{}
	for _, l := range lines(out) {
		f := fields(l)
		if len(f) < 5 {
			continue
		}
		proto, local := f[0], f[4]
		proc := ""
		if i := strings.Index(l, "users:"); i >= 0 {
			proc = extractProc(l[i:])
		}
		key := proto + " " + local
		if seen[key] {
			continue
		}
		seen[key] = true
		sec.Items = append(sec.Items, Item{Key: key, Value: proc, Note: proc})
	}
	return sec
}

func extractProc(s string) string {
	i := strings.Index(s, `"`)
	if i < 0 {
		return ""
	}
	j := strings.Index(s[i+1:], `"`)
	if j < 0 {
		return ""
	}
	return s[i+1 : i+1+j]
}

type firewall struct{}

func (firewall) Name() string { return "firewall" }
func (firewall) Collect(ctx context.Context) Section {
	sec := Section{Name: "firewall", Title: "防火墙"}
	if out, ok := runCmd(ctx, "nft", "list", "ruleset"); ok && strings.TrimSpace(out) != "" {
		sec.Items = append(sec.Items, Item{Key: "nftables.ruleset.hash", Value: hashBytes([]byte(out))})
		return sec
	}
	if out, ok := runCmd(ctx, "iptables-save"); ok && strings.TrimSpace(out) != "" {
		var kept []string
		for _, l := range lines(out) {
			if strings.HasPrefix(l, "#") {
				continue
			}
			kept = append(kept, l)
		}
		sec.Items = append(sec.Items, Item{Key: "iptables.rules.hash", Value: hashBytes([]byte(strings.Join(kept, "\n")))})
		return sec
	}
	if !hasRoot() {
		sec.Skipped = "需要 root 读取防火墙规则"
	} else {
		sec.Skipped = "未找到 nft 或 iptables"
	}
	return sec
}

type storage struct{}

func (storage) Name() string { return "storage" }
func (storage) Collect(ctx context.Context) Section {
	sec := Section{Name: "storage", Title: "存储与挂载"}
	if v, ok := readFile("/proc/mounts"); ok {
		for _, l := range lines(v) {
			f := fields(l)
			if len(f) >= 4 && !strings.HasPrefix(f[0], "cgroup") && f[1] != "/proc" {
				sec.Items = append(sec.Items, Item{Key: "mount:" + f[1], Value: f[0] + " " + f[2] + " " + f[3]})
			}
		}
	}
	if h, ok := fileHash("/etc/fstab"); ok {
		sec.Items = append(sec.Items, Item{Key: "fstab.hash", Value: h})
	}
	if v, ok := readFile("/proc/mdstat"); ok {
		for _, l := range lines(v) {
			if strings.HasPrefix(l, "md") {
				sec.Items = append(sec.Items, Item{Key: "mdraid:" + fields(l)[0], Value: l})
			}
		}
	}
	if out, ok := runCmd(ctx, "lsblk", "-nio", "NAME,SIZE,TYPE"); ok {
		for _, l := range lines(out) {
			f := fields(l)
			if len(f) >= 3 {
				sec.Items = append(sec.Items, Item{Key: "blk:" + strings.TrimLeft(f[0], "|`- "), Value: f[1] + " " + f[2]})
			}
		}
	}
	return sec
}

func init() {
	register(network{})
	register(listen{})
	register(firewall{})
	register(storage{})
}
