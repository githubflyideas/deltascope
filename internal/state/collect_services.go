package state

import (
	"context"
	"strings"
)

type services struct{}

func (services) Name() string { return "services" }
func (services) Collect(ctx context.Context) Section {
	sec := Section{Name: "services", Title: "Service Status"}

	// The ENABLED state (enabled/disabled/masked) is the real configuration
	// -- whether a service is set to start -- and is recorded for every
	// unit. Collected first because it also decides which units' RUNNING
	// state is worth recording.
	enabled := map[string]bool{}
	if out, ok := runCmd(ctx, "systemctl", "list-unit-files", "--type=service", "--no-legend", "--plain"); ok {
		for _, l := range lines(out) {
			f := fields(l)
			if len(f) >= 2 && strings.HasSuffix(f[0], ".service") {
				sec.Items = append(sec.Items, Item{Key: "enabled:" + f[0], Value: f[1]})
				if f[1] == "enabled" || f[1] == "enabled-runtime" {
					enabled[f[0]] = true
				}
			}
		}
	}

	// The RUNNING state is recorded ONLY for units enabled at boot, and the
	// reason is stability of the fingerprint's key set.
	//
	// `list-units --all` reports a churning population: a desktop carries a
	// hundred static oneshots sitting inactive/dead, systemd unloads idle
	// units from the list entirely, and on-demand daemons (ModemManager,
	// fwupd) start and exit on their own. Recording any of those means a
	// unit present in snapshot A is simply gone from B -- a phantom
	// "removed" -- with no change to the machine behind it. That is what
	// produced 100+ bogus rows in the field.
	//
	// The set of ENABLED units, by contrast, is stable: it only changes
	// when someone runs enable/disable. Recording running state over that
	// set means the value can change (enabled daemon active -> inactive =
	// "the service you provisioned is down", a real and useful finding)
	// but the KEY set does not churn, so nothing flaps in or out on its
	// own. A manually-started, never-enabled daemon is deliberately out of
	// scope here: its enable state is still tracked above, and its resource
	// use still shows in process accounting.
	// One more guard on top of "enabled only": an enabled unit that is
	// itself on-demand (a timer-triggered oneshot, a D-Bus-activated
	// daemon) still flaps in VALUE even though its key is stable -- it is
	// enabled AND runs briefly then exits. Those are dropped too, so what
	// remains is enabled, long-running daemons, whose running value only
	// changes when the daemon actually goes up or down.
	volatile := volatileServices(ctx)
	if out, ok := runCmd(ctx, "systemctl", "list-units", "--type=service", "--all", "--no-legend", "--plain"); ok {
		for _, l := range lines(out) {
			f := fields(l)
			if len(f) >= 4 && strings.HasSuffix(f[0], ".service") && enabled[f[0]] && !volatile[f[0]] {
				// ModifyOnly: systemd unloads idle units from list-units, so
				// a running: key blinks in and out on its own. That churn is
				// not an event; a daemon we provisioned flipping
				// active/running -> failed is, and that is a value change,
				// still reported. This is what stops setvtrgb-class
				// "appeared/removed" noise regardless of unit classification.
				sec.Items = append(sec.Items, Item{Key: "running:" + f[0], Value: f[2] + "/" + f[3], ModifyOnly: true})
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
