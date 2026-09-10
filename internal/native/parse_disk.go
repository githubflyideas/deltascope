package native

import "strings"

// Disk and filesystem parsing. Split out from parse.go because
// /proc/diskstats needs a device-classification pass that none of the
// other files do: PCP presents four separate metric families (dev, dm, md,
// all) over what the kernel publishes as one flat list, and picking the
// wrong family for a device double-counts it into disk.all.

// diskstatsSkip are device name prefixes that are not real storage. Their
// stats are real but meaningless as a disk-health signal: a busy loop
// device is a mounted image, and counting it into disk.all would make
// state.io.saturated fire on a container host doing nothing wrong.
var diskstatsSkip = []string{"loop", "ram", "zram", "fd", "sr"}

// diskDev is one parsed /proc/diskstats line.
type diskDev struct {
	Name string
	F    []string
}

// parseDiskstatsDevices splits /proc/diskstats into per-device field
// slices, dropping obvious non-storage and short lines. It does no
// classification: partition detection needs the whole list first.
func parseDiskstatsDevices(content string) []diskDev {
	var out []diskDev
	for _, line := range strings.Split(content, "\n") {
		f := strings.Fields(line)
		// 14 fields is the kernel 2.6+ layout; the 4-field variant is for
		// partitions on ancient kernels and carries none of what is needed.
		if len(f) < 14 {
			continue
		}
		name := f[2]
		if skipDisk(name) {
			continue
		}
		out = append(out, diskDev{Name: name, F: f})
	}
	return out
}

func skipDisk(name string) bool {
	for _, p := range diskstatsSkip {
		if strings.HasPrefix(name, p) {
			return true
		}
	}
	return false
}

// isPartitionOf reports whether child is a partition of parent, by the
// only rule available from names alone: parent is a proper prefix and what
// remains is a partition number, optionally after the "p" that nvme and
// mmc use ("sda"+"1", "nvme0n1"+"p1"). This is how partitions are excluded
// from disk.all without asking sysfs, so it must not match sibling
// devices -- "sda" and "sdb" share no prefix relationship, and neither do
// "nvme0n1" and "nvme0n2".
func isPartitionOf(parent, child string) bool {
	if parent == child || !strings.HasPrefix(child, parent) {
		return false
	}
	rest := strings.TrimPrefix(child, parent)
	if strings.HasPrefix(rest, "p") {
		rest = rest[1:]
	}
	if rest == "" {
		return false
	}
	for _, r := range rest {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

// diskClass returns the metric-family prefix a device belongs to, or ""
// when the device must be ignored. Partitions are ignored: their counters
// are already included in their parent's, so summing both into disk.all
// would report twice the real IO.
func diskClass(name string, all []diskDev) string {
	switch {
	case strings.HasPrefix(name, "dm-"):
		return "disk.dm."
	case isMDName(name):
		return "disk.md."
	}
	for _, d := range all {
		if isPartitionOf(d.Name, name) {
			return ""
		}
	}
	return "disk.dev."
}

func isMDName(name string) bool {
	if !strings.HasPrefix(name, "md") {
		return false
	}
	rest := name[2:]
	if rest == "" {
		return false
	}
	for _, r := range rest {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

// diskField is the /proc/diskstats column layout after the device name,
// which has been stable since kernel 2.6. Named rather than inlined
// because an off-by-one here is invisible: read the wrong column and
// disk.dev.aveq reports milliseconds of write time as a queue length.
const (
	dsReads       = 3
	dsReadMerges  = 4
	dsReadSectors = 5
	dsWrites      = 7
	dsWriteMerges = 8
	dsWriteSector = 9
	dsIOTicks     = 12
	dsWeightedMS  = 13
)

// parseDiskstats emits the per-device families and the disk.all rollup.
//
// disk.all sums the dev class only, matching PCP: dm and md devices sit on
// top of the same physical disks, so including them would count the same
// IO two or three times over on any LVM or RAID host and turn
// state.io.throughput_high into a permanent false positive.
func (s *Sample) parseDiskstats(content string) {
	devs := parseDiskstatsDevices(content)
	sums := map[string]float64{}
	anyDev := false

	for _, d := range devs {
		class := diskClass(d.Name, devs)
		if class == "" {
			continue
		}
		vals := map[string]float64{}
		for leaf, idx := range map[string]int{
			"read": dsReads, "write": dsWrites,
			"read_merge": dsReadMerges, "write_merge": dsWriteMerges,
			"read_bytes": dsReadSectors, "write_bytes": dsWriteSector,
			"avactive": dsIOTicks, "aveq": dsWeightedMS,
		} {
			if v, ok := num(field(d.F, idx)); ok {
				vals[leaf] = v
				s.set(class+leaf, d.Name, v)
			}
		}
		if r, okR := vals["read"]; okR {
			if w, okW := vals["write"]; okW {
				s.set(class+"total", d.Name, r+w)
			}
		}
		if class != "disk.dev." {
			continue
		}
		anyDev = true
		for leaf, v := range vals {
			sums[leaf] += v
		}
		sums["total"] += vals["read"] + vals["write"]
	}

	if !anyDev {
		return
	}
	for leaf, v := range sums {
		s.set("disk.all."+leaf, "", v)
	}
}

// parseFirstNumber handles the several single-line /proc and /sys files
// whose answer is just the first field: file-nr (allocated handles),
// inode-nr (nr_inodes), dentry-state (nr_dentry), entropy_avail, uptime.
func (s *Sample) parseFirstNumber(metric, content string) {
	if v, ok := num(field(strings.Fields(content), 0)); ok {
		s.set(metric, "", v)
	}
}

// mountPoint is one filesystem worth measuring.
type mountPoint struct {
	Dev  string // instance name, matching PCP's use of the device path
	Path string // where to call statfs
}

// pseudoFS never has a meaningful capacity: a full tmpfs is a real
// problem, but PCP's filesys.* only covers block-device filesystems and
// the catalog's thresholds (90% full) assume that. Reporting tmpfs here
// would make /run at 40% look like a disk approaching exhaustion.
func isBlockMount(dev, fstype string) bool {
	if !strings.HasPrefix(dev, "/") {
		return false
	}
	switch fstype {
	case "squashfs", "iso9660", "devtmpfs", "tmpfs", "ramfs", "autofs":
		return false
	}
	// internal/pcp/units.go excludes /dev/loop* instances from the PCP path;
	// a native collector bypasses buildRows, so it must exclude them itself
	// or snap-package mounts (always 100% full by design) would each raise a
	// "filesystem nearly full" state.
	return !strings.HasPrefix(dev, "/dev/loop")
}

// parseMounts reads /proc/mounts, keeping the first mount of each device.
// First rather than last: a bind mount of an already-mounted device adds no
// new capacity information, and reporting the same device twice would let
// one full filesystem produce two identical states.
func parseMounts(content string) []mountPoint {
	seen := map[string]bool{}
	var out []mountPoint
	for _, line := range strings.Split(content, "\n") {
		f := strings.Fields(line)
		if len(f) < 3 {
			continue
		}
		dev, path, fstype := f[0], unescapeMount(f[1]), f[2]
		if !isBlockMount(dev, fstype) || seen[dev] {
			continue
		}
		seen[dev] = true
		out = append(out, mountPoint{Dev: dev, Path: path})
	}
	return out
}

// unescapeMount undoes the octal escaping /proc/mounts applies to spaces
// and tabs in paths. Without it, statfs on a mount point containing a
// space fails and the filesystem silently disappears from the report.
func unescapeMount(p string) string {
	r := strings.NewReplacer(`\040`, " ", `\011`, "\t", `\012`, "\n", `\134`, `\`)
	return r.Replace(p)
}
