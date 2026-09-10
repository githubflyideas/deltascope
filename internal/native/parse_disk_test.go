package native

import (
	"reflect"
	"testing"
)

// Columns are: major minor name reads rmerge rsect rms writes wmerge wsect
// wms inflight io_ticks weighted_ms. nvme0n1p1 is a partition of nvme0n1 and
// its counters are already inside its parent's.
const diskstatsFixture = `   7       0 loop0 100 0 200 10 0 0 0 0 0 0 0
 259       0 nvme0n1 1000 50 8000 500 2000 100 16000 900 0 3000 4500
 259       1 nvme0n1p1 900 40 7000 450 1800 90 14000 800 0 2800 4200
   8       0 sda 500 20 4000 300 100 10 800 200 0 1000 1500
   8       1 sda1 490 18 3900 290 95 9 780 190 0 980 1400
 253       0 dm-0 400 0 3000 250 90 0 700 150 0 900 1200
   9       0 md0 300 0 2000 100 80 0 600 100 0 800 1000
`

func TestParseDiskstats(t *testing.T) {
	s := newSample(zeroTime)
	s.parseDiskstats(diskstatsFixture)

	mustVal(t, &s, "disk.dev.read", "nvme0n1", 1000)
	mustVal(t, &s, "disk.dev.write", "nvme0n1", 2000)
	mustVal(t, &s, "disk.dev.total", "nvme0n1", 3000)
	mustVal(t, &s, "disk.dev.read_bytes", "nvme0n1", 8000)
	mustVal(t, &s, "disk.dev.avactive", "nvme0n1", 3000)
	mustVal(t, &s, "disk.dev.aveq", "nvme0n1", 4500)

	// Partitions are excluded entirely: their IO is already counted in the
	// parent device, so including them would double the rollup.
	mustAbsent(t, &s, "disk.dev.read", "nvme0n1p1")
	mustAbsent(t, &s, "disk.dev.read", "sda1")
	// loop devices are mounted images, not storage health.
	mustAbsent(t, &s, "disk.dev.read", "loop0")

	// Stacked devices get their own families, not the dev one.
	mustVal(t, &s, "disk.dm.read", "dm-0", 400)
	mustAbsent(t, &s, "disk.dev.read", "dm-0")
	mustVal(t, &s, "disk.md.read", "md0", 300)
	mustAbsent(t, &s, "disk.dev.read", "md0")

	// disk.all sums the dev class only: dm-0 and md0 sit on top of the same
	// physical disks, so adding them would count the same IO twice.
	mustVal(t, &s, "disk.all.read", "", 1500)
	mustVal(t, &s, "disk.all.write", "", 2100)
	mustVal(t, &s, "disk.all.total", "", 3600)
	mustVal(t, &s, "disk.all.read_bytes", "", 12000)
}

func TestIsPartitionOf(t *testing.T) {
	cases := []struct {
		parent, child string
		want          bool
	}{
		{"sda", "sda1", true},
		{"nvme0n1", "nvme0n1p1", true},
		{"mmcblk0", "mmcblk0p2", true},
		// Sibling devices share a prefix relationship in neither direction,
		// and misreading one as a partition would drop a whole disk.
		{"sda", "sdb", false},
		{"nvme0n1", "nvme0n2", false},
		{"sda", "sda", false},
		{"dm-0", "dm-01", true},
		{"sda", "sdax", false},
	}
	for _, c := range cases {
		if got := isPartitionOf(c.parent, c.child); got != c.want {
			t.Errorf("isPartitionOf(%q, %q) = %v, want %v", c.parent, c.child, got, c.want)
		}
	}
}

func TestIsMDName(t *testing.T) {
	for name, want := range map[string]bool{
		"md0": true, "md127": true, "md": false, "mdx": false, "sda": false,
	} {
		if got := isMDName(name); got != want {
			t.Errorf("isMDName(%q) = %v, want %v", name, got, want)
		}
	}
}

const mountsFixture = `proc /proc proc rw,nosuid 0 0
sysfs /sys sysfs rw 0 0
/dev/nvme0n1p2 / ext4 rw,relatime 0 0
/dev/loop0 /snap/core20/1852 squashfs ro,nodev 0 0
tmpfs /run tmpfs rw,nosuid,size=1600140k 0 0
/dev/nvme0n1p2 /home ext4 rw,relatime 0 0
/dev/sda1 /mnt/data\040dir ext4 rw 0 0
overlay /var/lib/docker/overlay2/x/merged overlay rw 0 0
`

func TestParseMounts(t *testing.T) {
	got := parseMounts(mountsFixture)
	want := []mountPoint{
		{Dev: "/dev/nvme0n1p2", Path: "/"},
		{Dev: "/dev/sda1", Path: "/mnt/data dir"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("parseMounts = %+v, want %+v", got, want)
	}
}

func TestParseFirstNumber(t *testing.T) {
	s := newSample(zeroTime)
	// /proc/sys/fs/file-nr is "allocated free max"; only the first is the
	// open-file count the catalog means.
	s.parseFirstNumber("vfs.files.count", "1216\t0\t9223372036854775807\n")
	mustVal(t, &s, "vfs.files.count", "", 1216)

	s.parseFirstNumber("kernel.all.uptime", "123456.78 987654.32\n")
	mustVal(t, &s, "kernel.all.uptime", "", 123456.78)

	// A file that exists but holds nothing usable yields no reading.
	s.parseFirstNumber("kernel.all.entropy.avail", "\n")
	mustAbsent(t, &s, "kernel.all.entropy.avail", "")
}
