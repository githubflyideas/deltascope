package state

import (
	"context"
	"os"
	"sort"
	"strconv"
	"strings"
)

// Process accounting reads /proc directly -- the same source ps and top
// use -- so it needs no PCP hotproc PMDA, no pmlogger configuration, and
// no waiting for an archive to accumulate. Data is available from the
// first snapshot onward.
//
// CPU is cumulative clock ticks since the process started, so a single
// snapshot is not meaningful on its own; CompareProcesses turns two
// snapshots into a rate. That is also why this section is excluded from
// the generic change diff: cumulative counters always differ, and would
// otherwise report every process as "modified" on every snapshot.

// procWhitelist names are always recorded regardless of usage, so a core
// service always has history to compare even when it happens to be idle.
var procWhitelist = map[string]bool{
	"mysqld": true, "mariadbd": true, "postgres": true, "mongod": true,
	"redis-server": true, "memcached": true,
	"nginx": true, "httpd": true, "apache2": true, "haproxy": true, "envoy": true,
	"java": true, "php-fpm": true, "python3": true, "node": true, "ruby": true,
	"dockerd": true, "containerd": true, "kubelet": true,
	"pmcd": true, "pmlogger": true, "deltascope": true,
}

// Selection policy: whitelisted services are always recorded, and beyond
// those we keep the heaviest procTopN userspace processes by weight. We
// deliberately do NOT require passing an absolute floor -- on a quiet
// machine that would record nothing at all, leaving no baseline to
// compare against later. Recording the top of a quiet machine is exactly
// what makes "yesterday vs today" work once it gets busy.
const procTopN = 40

// agg accumulates all instances sharing one command name.
type agg struct {
	ticks   uint64
	rssKB   uint64
	startAt uint64 // earliest start among instances of this name
	count   int
}

type processes struct{}

func (processes) Name() string { return "processes" }

func (processes) Collect(ctx context.Context) Section {
	sec := Section{Name: "processes", Title: "Processes", SkipDiff: true}

	entries, err := os.ReadDir("/proc")
	if err != nil {
		sec.Skipped = "could not read /proc"
		return sec
	}

	pageKB := uint64(os.Getpagesize()) / 1024
	if pageKB == 0 {
		pageKB = 4
	}

	acc := map[string]*agg{}

	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		pid := e.Name()
		if pid[0] < '0' || pid[0] > '9' {
			continue
		}
		raw, ok := readFile("/proc/" + pid + "/stat")
		if !ok {
			continue // process exited between readdir and read; normal
		}
		comm, fields, ok := parseProcStat(raw)
		if !ok || comm == "" {
			continue
		}
		// fields are 0-indexed from /proc stat field 3 (state)
		utime := parseU(fields, 11)
		stime := parseU(fields, 12)
		start := parseU(fields, 19)
		rssPages := parseU(fields, 21)

		// Kernel threads (kworker, ksoftirqd, rcu_*, ...) have no user
		// address space, so RSS is always 0. Their cost belongs to the
		// system-level metrics, not to process accounting -- nobody is
		// going to act on "kworker used 2 ticks".
		if rssPages == 0 {
			continue
		}

		a := acc[comm]
		if a == nil {
			a = &agg{startAt: start}
			acc[comm] = a
		}
		a.ticks += utime + stime
		a.rssKB += rssPages * pageKB
		a.count++
		if start < a.startAt || a.startAt == 0 {
			a.startAt = start
		}
	}

	// Whitelisted names always in; beyond those, keep the heaviest few so
	// a busy host doesn't bloat the snapshot.
	type cand struct {
		name string
		a    *agg
	}
	var extra []cand
	for name, a := range acc {
		if procWhitelist[name] {
			sec.Items = append(sec.Items, procItem(name, a.ticks, a.rssKB, a.startAt, a.count))
			continue
		}
		extra = append(extra, cand{name, a})
	}
	sort.Slice(extra, func(i, j int) bool {
		return procWeight(extra[i].a) > procWeight(extra[j].a)
	})
	if len(extra) > procTopN {
		extra = extra[:procTopN]
	}
	for _, c := range extra {
		sec.Items = append(sec.Items, procItem(c.name, c.a.ticks, c.a.rssKB, c.a.startAt, c.a.count))
	}

	// The host's uptime at capture time is what lets a later comparison turn
	// a process's start tick into a wall-clock instant. Without it a
	// cumulative tick counter is only comparable against another reading of
	// the same process -- precisely the assumption that fails for a process
	// that started mid-window.
	if up, ok := readUptimeSeconds(); ok {
		sec.Meta = map[string]string{"uptime_sec": strconv.FormatFloat(up, 'f', 2, 64)}
	}

	if len(sec.Items) == 0 {
		sec.Skipped = "no userspace processes found"
	}
	return sec
}

// readUptimeSeconds reads the host's uptime. /proc/uptime is used rather
// than /proc/stat's btime because it needs no clock arithmetic and is
// immune to the wall clock being stepped by NTP between snapshots -- the
// process start tick it will be combined with is measured on the same
// monotonic boot timeline.
func readUptimeSeconds() (float64, bool) {
	raw, ok := readFile("/proc/uptime")
	if !ok {
		return 0, false
	}
	fields := strings.Fields(raw)
	if len(fields) == 0 {
		return 0, false
	}
	v, err := strconv.ParseFloat(fields[0], 64)
	if err != nil || v <= 0 {
		return 0, false
	}
	return v, true
}

// procWeight ranks by memory footprint plus CPU consumed, in roughly
// comparable magnitudes (MB of RSS + seconds of CPU).
func procWeight(a *agg) uint64 { return a.rssKB/1024 + a.ticks/clockTicksPerSec }

// procItem encodes a process's counters into one Item value.
// Layout: "<cpu_ticks> <rss_kb> <start_ticks> <instances>"
func procItem(name string, ticks, rssKB, start uint64, count int) Item {
	return Item{
		Key: name,
		Value: strconv.FormatUint(ticks, 10) + " " +
			strconv.FormatUint(rssKB, 10) + " " +
			strconv.FormatUint(start, 10) + " " +
			strconv.Itoa(count),
	}
}

// parseProcStat splits a /proc/<pid>/stat line. comm is parenthesized and
// may itself contain spaces or parens, so we split at the LAST ')'.
func parseProcStat(raw string) (comm string, fields []string, ok bool) {
	open := strings.IndexByte(raw, '(')
	close := strings.LastIndexByte(raw, ')')
	if open < 0 || close < 0 || close < open {
		return "", nil, false
	}
	comm = raw[open+1 : close]
	rest := strings.TrimSpace(raw[close+1:])
	if rest == "" {
		return "", nil, false
	}
	return comm, strings.Fields(rest), true
}

func parseU(fields []string, i int) uint64 {
	if i >= len(fields) {
		return 0
	}
	v, err := strconv.ParseUint(fields[i], 10, 64)
	if err != nil {
		return 0
	}
	return v
}

// DecodeProcItem reads back what procItem encoded.
func DecodeProcItem(v string) (ticks, rssKB, start uint64, count int, ok bool) {
	f := strings.Fields(v)
	if len(f) != 4 {
		return 0, 0, 0, 0, false
	}
	ticks, _ = strconv.ParseUint(f[0], 10, 64)
	rssKB, _ = strconv.ParseUint(f[1], 10, 64)
	start, _ = strconv.ParseUint(f[2], 10, 64)
	c, _ := strconv.Atoi(f[3])
	return ticks, rssKB, start, c, true
}

func init() { register(processes{}) }
