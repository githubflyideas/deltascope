//go:build linux

package native

import (
	"fmt"
	"os"
	"syscall"
	"time"
)

// snapshot reads every source once. Individual failures are tolerated and
// simply produce no readings: a container with /proc/pressure masked, a
// kernel without PSI, a host with no software RAID -- none of those is an
// error, and each one only narrows what can be concluded. The pass fails
// only when nothing at all could be read, which means /proc is not there.
func snapshot(t time.Time) (Sample, error) {
	s := newSample(t)

	read := func(path string, fn func(string)) {
		if c, ok := readProc(path); ok {
			fn(c)
		}
	}
	read("/proc/stat", s.parseStat)
	read("/proc/loadavg", s.parseLoadavg)
	read("/proc/meminfo", s.parseMeminfo)
	read("/proc/vmstat", s.parseVmstat)
	read("/proc/diskstats", s.parseDiskstats)
	read("/proc/net/dev", s.parseNetDev)
	read("/proc/net/snmp", s.parseSNMP)
	read("/proc/net/netstat", s.parseSNMP)
	read("/proc/net/sockstat", s.parseSockstat)
	read("/proc/net/softnet_stat", s.parseSoftnet)

	for resource, path := range map[string]string{
		"cpu":    "/proc/pressure/cpu",
		"memory": "/proc/pressure/memory",
		"io":     "/proc/pressure/io",
	} {
		if c, ok := readProc(path); ok {
			s.parsePressure(resource, c)
		}
	}

	for metric, path := range map[string]string{
		"vfs.files.count":          "/proc/sys/fs/file-nr",
		"vfs.inodes.count":         "/proc/sys/fs/inode-nr",
		"vfs.dentry.count":         "/proc/sys/fs/dentry-state",
		"kernel.all.entropy.avail": "/proc/sys/kernel/random/entropy_avail",
		"kernel.all.uptime":        "/proc/uptime",
	} {
		if c, ok := readProc(path); ok {
			s.parseFirstNumber(metric, c)
		}
	}

	// Both socket tables are one logical table split by address family, so
	// they are counted together in a single call.
	tcp4, _ := readProc("/proc/net/tcp")
	tcp6, _ := readProc("/proc/net/tcp6")
	s.parseTCPConn(tcp4, tcp6)

	if c, ok := readProc("/proc/mounts"); ok {
		s.fillFilesys(parseMounts(c))
	}

	if s.Len() == 0 {
		return s, fmt.Errorf("native: no metrics readable under /proc")
	}
	return s, nil
}

func readProc(path string) (string, bool) {
	b, err := os.ReadFile(path)
	if err != nil {
		return "", false
	}
	return string(b), true
}

// fillFilesys measures each mounted block filesystem with statfs.
//
// full is computed the way df does it -- used / (used + available), not
// used / total -- because the root-reserved blocks are not available to
// anything that would fill the disk. Using total instead reports 95% as
// 90% and delays the state that matters by exactly the margin the reserve
// was meant to provide.
func (s *Sample) fillFilesys(mounts []mountPoint) {
	for _, m := range mounts {
		var st syscall.Statfs_t
		if err := syscall.Statfs(m.Path, &st); err != nil {
			continue
		}
		kbPerBlock := float64(st.Bsize) / 1024
		if kbPerBlock <= 0 {
			continue
		}
		blocks, bfree, bavail := float64(st.Blocks), float64(st.Bfree), float64(st.Bavail)
		if blocks <= 0 {
			continue
		}
		used := blocks - bfree
		if used+bavail > 0 {
			s.set("filesys.full", m.Dev, used/(used+bavail)*100)
		}
		s.set("filesys.free", m.Dev, bfree*kbPerBlock)
		s.set("filesys.avail", m.Dev, bavail*kbPerBlock)
		// Files == 0 means the filesystem has no fixed inode table (btrfs,
		// xfs with dynamic inodes), where a used-inode count is not a
		// meaningful number rather than a zero one.
		if st.Files > 0 && st.Files >= st.Ffree {
			s.set("filesys.usedfiles", m.Dev, float64(st.Files-st.Ffree))
		}
	}
}
