package state

import (
	"context"
	"os"
	"path/filepath"
	"strings"
)

type sysInfo struct{}

func (sysInfo) Name() string { return "system" }
func (sysInfo) Collect(ctx context.Context) Section {
	sec := Section{Name: "system", Title: "System Identity"}
	add := func(k, v string) {
		if v != "" {
			sec.Items = append(sec.Items, Item{Key: k, Value: strings.TrimSpace(v)})
		}
	}
	if v, ok := readFile("/proc/sys/kernel/osrelease"); ok {
		add("kernel", v)
	}
	if v, ok := readFile("/proc/sys/kernel/hostname"); ok {
		add("hostname", v)
	}
	if v, ok := readFile("/proc/sys/kernel/version"); ok {
		add("kernel.build", v)
	}
	if v, ok := readFile("/etc/os-release"); ok {
		for _, l := range lines(v) {
			if strings.HasPrefix(l, "PRETTY_NAME=") {
				add("os", strings.Trim(strings.TrimPrefix(l, "PRETTY_NAME="), `"`))
			}
			if strings.HasPrefix(l, "VERSION_ID=") {
				add("os.version_id", strings.Trim(strings.TrimPrefix(l, "VERSION_ID="), `"`))
			}
		}
	}
	if v, ok := readFile("/proc/cmdline"); ok {
		add("boot.cmdline", v)
	}
	if v, ok := readFile("/etc/timezone"); ok {
		add("timezone", v)
	} else if tgt, err := os.Readlink("/etc/localtime"); err == nil {
		add("timezone", filepath.Base(tgt))
	}
	if v, ok := readFile("/proc/cpuinfo"); ok {
		n, model := 0, ""
		for _, l := range lines(v) {
			if strings.HasPrefix(l, "processor") {
				n++
			}
			if model == "" && strings.HasPrefix(l, "model name") {
				if i := strings.Index(l, ":"); i >= 0 {
					model = strings.TrimSpace(l[i+1:])
				}
			}
		}
		if n > 0 {
			add("cpu.count", itoa(n))
		}
		add("cpu.model", model)
	}
	if v, ok := readFile("/proc/meminfo"); ok {
		for _, l := range lines(v) {
			if strings.HasPrefix(l, "MemTotal:") {
				add("mem.total", strings.TrimSpace(strings.TrimPrefix(l, "MemTotal:")))
				break
			}
		}
	}
	return sec
}

type sysctl struct{}

// sysctlVolatile lists key prefixes/exact names that fluctuate naturally at
// runtime and are not configuration - skipped during collection.
var sysctlVolatile = []string{
	"kernel.random.uuid", "kernel.random.boot_id", "kernel.ns_last_pid",
	"kernel.random.entropy_avail", "fs.dentry-state", "fs.file-nr",
	"fs.inode-nr", "fs.inode-state", "fs.quota", "kernel.sched_domain",
	"kernel.pty.nr", "net.netfilter.nf_conntrack_count",
	"kernel.hung_task_detect_count", "kernel.tainted", "user.",
}

// sysctlSecurityOwned are keys already surfaced individually by the security
// collector; skipped here to avoid duplicate alerts.
var sysctlSecurityOwned = map[string]bool{
	"net.ipv4.ip_forward":                true,
	"net.ipv4.conf.all.rp_filter":        true,
	"net.ipv4.conf.all.accept_redirects": true,
	"net.ipv4.tcp_syncookies":            true,
	"kernel.randomize_va_space":          true,
	"kernel.kptr_restrict":               true,
	"kernel.dmesg_restrict":              true,
}

func sysctlSkip(key string) bool {
	if sysctlSecurityOwned[key] {
		return true
	}
	for _, v := range sysctlVolatile {
		if key == v || (len(v) > 0 && v[len(v)-1] == '.' && len(key) >= len(v) && key[:len(v)] == v) {
			return true
		}
	}
	return false
}

func (sysctl) Name() string { return "sysctl" }
func (sysctl) Collect(ctx context.Context) Section {
	sec := Section{Name: "sysctl", Title: "Kernel Parameters (sysctl)"}
	roots := []string{
		"/proc/sys/net", "/proc/sys/vm", "/proc/sys/kernel",
		"/proc/sys/fs",
	}
	for _, root := range roots {
		filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
			if err != nil || d.IsDir() {
				return nil
			}
			b, rerr := os.ReadFile(path)
			if rerr != nil {
				return nil
			}
			val := strings.TrimSpace(strings.ReplaceAll(string(b), "\t", " "))
			if val == "" || strings.Contains(val, "\n") {
				return nil
			}
			key := strings.ReplaceAll(strings.TrimPrefix(path, "/proc/sys/"), "/", ".")
			if sysctlSkip(key) {
				return nil
			}
			sec.Items = append(sec.Items, Item{Key: key, Value: val})
			return nil
		})
	}
	if len(sec.Items) == 0 {
		sec.Skipped = "could not read /proc/sys"
	}
	return sec
}

type packages struct{}

func (packages) Name() string { return "packages" }
func (packages) Collect(ctx context.Context) Section {
	sec := Section{Name: "packages", Title: "Packages"}
	if out, ok := runCmd(ctx, "rpm", "-qa", "--qf", "%{NAME} %{VERSION}-%{RELEASE}\n"); ok {
		for _, l := range lines(out) {
			f := fields(l)
			if len(f) == 2 {
				sec.Items = append(sec.Items, Item{Key: f[0], Value: f[1]})
			}
		}
		return sec
	}
	if out, ok := runCmd(ctx, "dpkg-query", "-W", "-f=${Package} ${Version}\n"); ok {
		for _, l := range lines(out) {
			f := fields(l)
			if len(f) == 2 {
				sec.Items = append(sec.Items, Item{Key: f[0], Value: f[1]})
			}
		}
		return sec
	}
	sec.Skipped = "neither rpm nor dpkg found"
	return sec
}

type modules struct{}

func (modules) Name() string { return "modules" }
func (modules) Collect(ctx context.Context) Section {
	sec := Section{Name: "modules", Title: "Kernel Modules"}
	v, ok := readFile("/proc/modules")
	if !ok {
		sec.Skipped = "could not read /proc/modules"
		return sec
	}
	for _, l := range lines(v) {
		f := fields(l)
		if len(f) >= 1 {
			sec.Items = append(sec.Items, Item{Key: f[0], Value: "loaded"})
		}
	}
	return sec
}

func init() {
	register(sysInfo{})
	register(sysctl{})
	register(packages{})
	register(modules{})
}
