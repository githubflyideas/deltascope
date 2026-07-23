package state

import (
	"context"
	"strings"
)

type services struct{}

func (services) Name() string { return "services" }
func (services) Collect(ctx context.Context) Section {
	sec := Section{Name: "services", Title: "Service Status"}
	if out, ok := runCmd(ctx, "systemctl", "list-units", "--type=service", "--all", "--no-legend", "--plain"); ok {
		for _, l := range lines(out) {
			f := fields(l)
			if len(f) >= 4 && strings.HasSuffix(f[0], ".service") {
				sec.Items = append(sec.Items, Item{Key: f[0], Value: f[2] + "/" + f[3]})
			}
		}
	}
	if out, ok := runCmd(ctx, "systemctl", "list-unit-files", "--type=service", "--no-legend", "--plain"); ok {
		for _, l := range lines(out) {
			f := fields(l)
			if len(f) >= 2 && strings.HasSuffix(f[0], ".service") {
				sec.Items = append(sec.Items, Item{Key: "enabled:" + f[0], Value: f[1]})
			}
		}
	}
	if len(sec.Items) == 0 {
		sec.Skipped = "systemctl not found"
	}
	return sec
}

type crontab struct{}

func (crontab) Name() string { return "cron" }
func (crontab) Collect(ctx context.Context) Section {
	sec := Section{Name: "cron", Title: "Scheduled Tasks"}
	for _, p := range globFiles([]string{
		"/etc/crontab", "/etc/cron.d/*", "/etc/cron.hourly/*",
		"/etc/cron.daily/*", "/etc/cron.weekly/*", "/etc/cron.monthly/*",
		"/var/spool/cron/*", "/var/spool/cron/crontabs/*",
	}) {
		if h, ok := fileHash(p); ok {
			sec.Items = append(sec.Items, Item{Key: p, Value: h})
		}
	}
	if len(sec.Items) == 0 {
		sec.Skipped = "no cron jobs or unreadable"
	}
	return sec
}

type configs struct{}

func (configs) Name() string { return "configs" }
func (configs) Collect(ctx context.Context) Section {
	sec := Section{Name: "configs", Title: "Config File Fingerprints"}
	patterns := []string{
		"/etc/ssh/sshd_config", "/etc/ssh/sshd_config.d/*",
		"/etc/security/limits.conf", "/etc/security/limits.d/*",
		"/etc/sysctl.conf", "/etc/sysctl.d/*",
		"/etc/systemd/system.conf", "/etc/systemd/journald.conf",
		"/etc/selinux/config",
		"/etc/nginx/nginx.conf", "/etc/nginx/conf.d/*",
		"/etc/pcp/pmlogger/*.config", "/etc/pcp.conf",
		"/etc/profile", "/etc/environment",
		"/etc/pam.d/*",
	}
	for _, p := range globFiles(patterns) {
		if h, ok := fileHash(p); ok {
			sec.Items = append(sec.Items, Item{Key: p, Value: h})
		}
	}
	if len(sec.Items) == 0 {
		sec.Skipped = "no matching config files"
	}
	return sec
}

type security struct{}

func (security) Name() string { return "security" }
func (security) Collect(ctx context.Context) Section {
	sec := Section{Name: "security", Title: "Security Posture"}
	add := func(k, v string) {
		if v != "" {
			sec.Items = append(sec.Items, Item{Key: k, Value: strings.TrimSpace(v)})
		}
	}
	if out, ok := runCmd(ctx, "getenforce"); ok {
		add("selinux", out)
	}
	if v, ok := readFile("/sys/module/apparmor/parameters/enabled"); ok {
		add("apparmor", v)
	}
	for _, key := range []string{
		"net.ipv4.ip_forward",
		"net.ipv4.conf.all.rp_filter",
		"net.ipv4.conf.all.accept_redirects",
		"net.ipv4.tcp_syncookies",
		"kernel.randomize_va_space",
		"kernel.kptr_restrict",
		"kernel.dmesg_restrict",
	} {
		if v, ok := readFile("/proc/sys/" + strings.ReplaceAll(key, ".", "/")); ok {
			add(key, v)
		}
	}
	if h, ok := fileHash("/etc/sudoers"); ok {
		add("sudoers.hash", h)
	}
	for _, p := range globFiles([]string{"/root/.ssh/authorized_keys", "/home/*/.ssh/authorized_keys"}) {
		if h, ok := fileHash(p); ok {
			add("authorized_keys:"+p, h)
		}
	}
	if len(sec.Items) == 0 {
		sec.Skipped = "no security posture items collectable"
	}
	return sec
}

func init() {
	register(services{})
	register(crontab{})
	register(configs{})
	register(security{})
}
