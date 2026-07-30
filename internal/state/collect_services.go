package state

import (
	"context"
	"strings"
)

type services struct{}

func (services) Name() string { return "services" }
func (services) Collect(ctx context.Context) Section {
	sec := Section{Name: "services", Title: "Service Status"}

	// On-demand and oneshot units come and go by design -- fwupd exits when
	// idle, fprintd is D-Bus-activated, NetworkManager-dispatcher is a
	// oneshot that runs and exits. Their active<->inactive flapping is the
	// normal lifecycle of the unit, not a change to the machine, and
	// reporting it buries the enable/disable changes that actually matter
	// (the same reason loopback traffic is filtered from the NIC report).
	// So the RUNNING STATE is recorded only for long-running units; the
	// ENABLED STATE below is recorded for every unit, because that is the
	// real configuration -- whether a service is set to start at all.
	volatile := volatileServices(ctx)

	if out, ok := runCmd(ctx, "systemctl", "list-units", "--type=service", "--all", "--no-legend", "--plain"); ok {
		for _, l := range lines(out) {
			f := fields(l)
			if len(f) >= 4 && strings.HasSuffix(f[0], ".service") {
				if volatile[f[0]] {
					continue // lifecycle noise, not a configuration change
				}
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

// volatileServices returns the set of service units whose running state is
// expected to come and go on its own, so a change in it is not a change to
// the machine. A unit qualifies when it is:
//
//   - oneshot: runs to completion and exits (Type=oneshot),
//   - socket/path/timer-activated: started on demand (TriggeredBy set),
//   - D-Bus-activated: started when its bus name is first called (BusName
//     set) -- this is what catches fprintd and fwupd, which are otherwise
//     ordinary services that simply exit when idle.
//
// One bulk `systemctl show` classifies every service; on any failure the
// set is empty and every service is treated as long-running, i.e. the old
// behaviour, so this can only ever suppress noise, never hide a unit that
// should have been compared.
func volatileServices(ctx context.Context) map[string]bool {
	out, ok := runCmd(ctx, "systemctl", "show", "--type=service", "--all",
		"--property=Id,Type,TriggeredBy,BusName", "*.service")
	if !ok {
		return nil
	}
	return parseVolatile(out)
}

// parseVolatile is the pure parser behind volatileServices, split out so it
// can be tested without a live systemctl.
func parseVolatile(out string) map[string]bool {
	// `systemctl show` prints one property=value per line, records
	// separated by a blank line. lines() strips blanks, so a new "Id="
	// marks the start of the next record: flush the accumulated one first.
	vol := map[string]bool{}
	var id, typ, trig, bus string
	flush := func() {
		if id != "" && (typ == "oneshot" || trig != "" || bus != "") {
			vol[id] = true
		}
		id, typ, trig, bus = "", "", "", ""
	}
	for _, l := range lines(out) {
		switch {
		case strings.HasPrefix(l, "Id="):
			flush()
			id = strings.TrimPrefix(l, "Id=")
		case strings.HasPrefix(l, "Type="):
			typ = strings.TrimPrefix(l, "Type=")
		case strings.HasPrefix(l, "TriggeredBy="):
			trig = strings.TrimPrefix(l, "TriggeredBy=")
		case strings.HasPrefix(l, "BusName="):
			bus = strings.TrimPrefix(l, "BusName=")
		}
	}
	flush()
	return vol
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
