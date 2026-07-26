package state

import (
	"context"
	"regexp"
	"strings"
)

type network struct{}

func (network) Name() string { return "network" }
func (network) Collect(ctx context.Context) Section {
	sec := Section{Name: "network", Title: "Network Configuration"}

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
	sec := Section{Name: "listen", Title: "Listening Ports"}
	out, ok := runCmd(ctx, "ss", "-lntuHp")
	if !ok {
		if out, ok = runCmd(ctx, "ss", "-lntu"); !ok {
			sec.Skipped = "ss not found"
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
	sec := Section{Name: "firewall", Title: "Firewall"}
	if out, ok := runCmd(ctx, "nft", "list", "ruleset"); ok && strings.TrimSpace(out) != "" {
		sec.Items = append(sec.Items, Item{Key: "nftables.ruleset.hash", Value: hashBytes([]byte(stripNftCounters(out)))})
		for chain, n := range countNftRules(out) {
			sec.Items = append(sec.Items, Item{Key: "nftables.rules:" + chain, Value: itoa(n)})
		}
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
		normalized := stripIptablesCounters(kept)
		sec.Items = append(sec.Items, Item{Key: "iptables.rules.hash", Value: hashBytes([]byte(strings.Join(normalized, "\n")))})
		// A hash tells you something changed but not where. Rule counts per
		// table/chain narrow it down to the chain without storing the rules
		// themselves, which would put addresses and ports in the snapshot.
		for chain, n := range countIptablesRules(kept) {
			sec.Items = append(sec.Items, Item{Key: "iptables.rules:" + chain, Value: itoa(n)})
		}
		return sec
	}
	if !hasRoot() {
		sec.Skipped = "root required to read firewall rules"
	} else {
		sec.Skipped = "neither nft nor iptables found"
	}
	return sec
}

// stripIptablesCounters removes the live packet/byte counters iptables-save
// embeds in chain policy lines -- ":INPUT ACCEPT [153:9876]" becomes
// ":INPUT ACCEPT [-:-]". Those numbers increment continuously as traffic
// flows through the host and have nothing to do with the ruleset itself,
// so hashing them made the hash change on essentially every capture even
// when no rule was ever touched. The chain name and policy (ACCEPT/DROP)
// are kept, since changing the default policy is a real configuration
// change worth detecting.
var iptablesCounterRe = regexp.MustCompile(`^(:\S+ \S+) \[\d+:\d+\]$`)

func stripIptablesCounters(ls []string) []string {
	out := make([]string, len(ls))
	for i, l := range ls {
		if m := iptablesCounterRe.FindStringSubmatch(l); m != nil {
			out[i] = m[1] + " [-:-]"
			continue
		}
		out[i] = l
	}
	return out
}

// stripNftCounters removes inline "counter packets N bytes N" traffic
// counters that nft list ruleset prints for any rule using the counter
// statement, for the same reason as stripIptablesCounters above.
var nftCounterRe = regexp.MustCompile(`counter packets \d+ bytes \d+`)

func stripNftCounters(s string) string {
	return nftCounterRe.ReplaceAllString(s, "counter packets - bytes -")
}

// countIptablesRules counts "-A CHAIN" rules per table/chain from
// iptables-save output.
func countIptablesRules(ls []string) map[string]int {
	counts := map[string]int{}
	table := "unknown"
	for _, l := range ls {
		if strings.HasPrefix(l, "*") {
			table = strings.TrimPrefix(l, "*")
			continue
		}
		if strings.HasPrefix(l, "-A ") {
			f := fields(l)
			if len(f) >= 2 {
				counts[table+"/"+f[1]]++
			}
		}
	}
	return counts
}

// countNftRules counts rules per table/chain in "nft list ruleset" output
// by tracking the enclosing chain block.
func countNftRules(out string) map[string]int {
	counts := map[string]int{}
	table, chain := "", ""
	depth := 0
	for _, l := range lines(out) {
		f := fields(l)
		switch {
		case len(f) >= 3 && f[0] == "table":
			table = f[1] + "." + f[2]
		case len(f) >= 2 && f[0] == "chain":
			chain = f[1]
			if _, seen := counts[table+"/"+chain]; !seen {
				counts[table+"/"+chain] = 0
			}
		case l == "}":
			if depth > 0 {
				depth--
			}
			chain = ""
		default:
			// a rule line inside a chain: not a declaration, not a brace
			if chain != "" && !strings.HasPrefix(l, "type ") &&
				!strings.HasPrefix(l, "policy ") && !strings.HasPrefix(l, "comment ") &&
				!strings.Contains(l, "{") && !strings.Contains(l, "}") {
				counts[table+"/"+chain]++
			}
		}
	}
	return counts
}

type storage struct{}

func (storage) Name() string { return "storage" }
func (storage) Collect(ctx context.Context) Section {
	sec := Section{Name: "storage", Title: "Storage & Mounts"}
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
