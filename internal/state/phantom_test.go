package state

import (
	"strings"
	"testing"
)

// The phantom Modified this file exists to pin down:
//
// itemMap builds map[string]Item from a Section's items, so two items sharing a
// key means the last one seen wins. Capture sorts the items before that, so
// which one is last is decided by the sort -- and an unstable sort among equal
// keys can hand the win to a different item on each capture. An unchanged
// machine then reports a Modified that flips back and forth forever, which is
// the one failure mode that destroys the reader's trust in every other row.
//
// Two defences, both tested here: the keys themselves are made unique where a
// real host produces collisions, and Capture's sort has a total order so that a
// collision which slips through anyway is boring instead of alternating.

// A multi-homed host has one default route per uplink, separated by metric.
// Keyed on the prefix alone both landed on `route:default`.
func TestTwoDefaultRoutesAreTwoItems(t *testing.T) {
	a := fields("default via 10.0.0.1 dev eth0 proto dhcp metric 100")
	b := fields("default via 192.168.1.1 dev eth1 proto dhcp metric 200")
	ka, kb := routeKey(a), routeKey(b)
	if ka == kb {
		t.Fatalf("two uplinks' default routes collided on one key: %q", ka)
	}
	for _, k := range []string{ka, kb} {
		if k == "route:default" {
			t.Errorf("route key %q carries no device or metric, so it collides", k)
		}
	}
}

// Policy routing repeats the same prefix in several tables. Same story.
func TestSamePrefixInTwoTablesAreTwoItems(t *testing.T) {
	a := routeKey(fields("10.0.0.0/8 dev eth0 scope link table main"))
	b := routeKey(fields("10.0.0.0/8 dev eth0 scope link table 200"))
	if a == b {
		t.Fatalf("the same prefix in two routing tables collided on %q", a)
	}
}

// A route with no device and no metric still has to produce a usable key
// rather than an empty one.
func TestRouteKeyWithoutOptionsStillIdentifies(t *testing.T) {
	if k := routeKey(fields("broadcast 127.0.0.0")); k != "route:broadcast" {
		t.Errorf("bare route key = %q, want route:broadcast", k)
	}
	if routeField(fields("default via 10.0.0.1 dev eth0"), "metric") != "" {
		t.Error("routeField invented a value for an absent option")
	}
	// The option name appearing as the last token has no value after it; the
	// extractor must not read past the end of the line.
	if routeField(fields("default via 10.0.0.1 metric"), "metric") != "" {
		t.Error("routeField read past the end of the line")
	}
}

// /proc/net/route is the fallback when `ip` is missing, and its columns are
// just as non-unique: Iface, Destination, Gateway, Flags, RefCnt, Use, Metric,
// Mask. The mask separates a host route from the network route it sits inside;
// the metric separates the uplinks.
func TestProcRouteKeysSeparateMaskAndMetric(t *testing.T) {
	host := fields("eth0\t0064000A\t00000000\t0001\t0\t0\t0\tFFFFFFFF\t0\t0\t0")
	net := fields("eth0\t0064000A\t00000000\t0001\t0\t0\t0\t00FFFFFF\t0\t0\t0")
	if procRouteKey(host) == procRouteKey(net) {
		t.Fatalf("host route and network route collided on %q", procRouteKey(host))
	}
	lo := fields("eth0\t00000000\t0100000A\t0003\t0\t0\t100\t00000000\t0 0 0")
	hi := fields("eth0\t00000000\t0100000A\t0003\t0\t0\t200\t00000000\t0 0 0")
	if procRouteKey(lo) == procRouteKey(hi) {
		t.Fatalf("two metrics on one prefix collided on %q", procRouteKey(lo))
	}
	// A short line (an old kernel, or a truncated read) must still key without
	// panicking on the absent columns.
	if k := procRouteKey(fields("eth0\t00000000")); k == "" {
		t.Error("a two-column /proc/net/route line produced an empty key")
	}
}

// A snap refresh moves the revision inside the mountpoint, so keyed on the
// mountpoint it was an Added plus a Removed -- two rows for one update, neither
// of which says what the update was.
func TestSnapRefreshIsOneModifiedNotAPair(t *testing.T) {
	before := mountItem(fields("/dev/loop3 /snap/core20/1974 squashfs ro,nodev,relatime 0 0"))
	after := mountItem(fields("/dev/loop7 /snap/core20/2015 squashfs ro,nodev,relatime 0 0"))
	if before.Key != after.Key {
		t.Fatalf("snap refresh changed the key: %q -> %q", before.Key, after.Key)
	}
	if before.Key != "mount:/snap/core20" {
		t.Errorf("snap mount key = %q, want mount:/snap/core20", before.Key)
	}
	if before.Value == after.Value {
		t.Fatal("the refresh produced no change at all; the revision was dropped, not moved")
	}
	// The revision is what changed, and it has to be readable in the value --
	// a Modified whose two sides are opaque hashes tells the reader nothing.
	for _, want := range []string{"1974", "2015"} {
		if before.Value != "" && after.Value != "" &&
			!strings.Contains(before.Value+" "+after.Value, want) {
			t.Errorf("revision %s is not visible in %q -> %q", want, before.Value, after.Value)
		}
	}
}

// The loop device a snap is mounted from is renumbered by the kernel on every
// boot. Keeping it in the value would trade the phantom add/remove pair for a
// phantom Modified after each reboot.
func TestSnapLoopRenumberingIsNotAChange(t *testing.T) {
	a := mountItem(fields("/dev/loop3 /snap/core20/1974 squashfs ro,nodev,relatime 0 0"))
	b := mountItem(fields("/dev/loop11 /snap/core20/1974 squashfs ro,nodev,relatime 0 0"))
	if a.Value != b.Value {
		t.Errorf("loop renumbering reported as a change: %q -> %q", a.Value, b.Value)
	}
}

// An ordinary mount must keep its device: /dev/sda2 becoming /dev/sdb2 under /
// is a real and alarming change, and this normalization must not touch it.
func TestOrdinaryMountKeepsItsDevice(t *testing.T) {
	it := mountItem(fields("/dev/sda2 / ext4 rw,relatime 0 0"))
	if it.Key != "mount:/" {
		t.Errorf("mount key = %q, want mount:/", it.Key)
	}
	if !strings.Contains(it.Value, "/dev/sda2") {
		t.Errorf("value %q lost the device", it.Value)
	}
	other := mountItem(fields("/dev/sdb2 / ext4 rw,relatime 0 0"))
	if it.Value == other.Value {
		t.Error("the root filesystem's device changing was not reported")
	}
}

// autofs prints a per-boot file descriptor number, the pid of the process group
// holding the mount, and the pipe's inode. All three change on every boot and on
// every systemd restart while the mount is configured identically.
func TestPerBootMountOptionsAreNotChanges(t *testing.T) {
	a := mountItem(fields("systemd-1 /proc/sys/fs/binfmt_misc autofs rw,relatime,fd=29,pgrp=1,timeout=0,minproto=5,maxproto=5,direct,pipe_ino=1234 0 0"))
	b := mountItem(fields("systemd-1 /proc/sys/fs/binfmt_misc autofs rw,relatime,fd=31,pgrp=1,timeout=0,minproto=5,maxproto=5,direct,pipe_ino=9876 0 0"))
	if a.Value != b.Value {
		t.Errorf("a reboot's autofs churn reported as a change:\n  %q\n  %q", a.Value, b.Value)
	}
	// What is left must still be the settings, not an empty string.
	for _, want := range []string{"autofs", "rw", "timeout=0", "direct"} {
		if !strings.Contains(a.Value, want) {
			t.Errorf("stripping per-boot fields also removed %q from %q", want, a.Value)
		}
	}
}

// The line this deliberately does not cross: devpts mode= and gid= look exactly
// as mechanical as fd=, and are not. mode going 620 -> 666 hands every user a
// readable terminal, which is the kind of change the tool exists to report.
func TestDevptsModeIsNotTreatedAsNoise(t *testing.T) {
	safe := mountItem(fields("devpts /dev/pts devpts rw,nosuid,noexec,relatime,gid=5,mode=620,ptmxmode=000 0 0"))
	wide := mountItem(fields("devpts /dev/pts devpts rw,nosuid,noexec,relatime,gid=5,mode=666,ptmxmode=000 0 0"))
	if safe.Value == wide.Value {
		t.Fatal("devpts mode 620 -> 666 was swallowed as per-boot noise")
	}
	if !strings.Contains(safe.Value, "mode=620") {
		t.Errorf("mode= was stripped from %q", safe.Value)
	}
	group := mountItem(fields("devpts /dev/pts devpts rw,nosuid,noexec,relatime,gid=100,mode=620,ptmxmode=000 0 0"))
	if safe.Value == group.Value {
		t.Error("devpts gid change was swallowed as per-boot noise")
	}
}

// Capture's sort is the second defence. Two items sharing a key should never
// happen -- the collectors key to be unique -- but if one ever does, the winner
// must be the same on every capture rather than alternating.
func TestDuplicateKeysResolveTheSameWayEveryTime(t *testing.T) {
	// Same key, two values, fed in opposite orders: two captures of a machine
	// that did not change.
	mk := func(first, second string) Section {
		return Section{Name: "storage", Title: "Storage", Items: []Item{
			{Key: "blk:dm-0", Value: first}, {Key: "blk:dm-0", Value: second},
			{Key: "blk:sda", Value: "500G disk"},
		}}
	}
	a := itemMap(sortItems(mk("100G lvm", "100G mpath")))
	b := itemMap(sortItems(mk("100G mpath", "100G lvm")))
	if a["blk:dm-0"].Value != b["blk:dm-0"].Value {
		t.Errorf("a duplicate key resolved differently between captures: %q vs %q",
			a["blk:dm-0"].Value, b["blk:dm-0"].Value)
	}
	d := Compare(
		Snapshot{Schema: SchemaVersion, Sections: []Section{sortItems(mk("100G lvm", "100G mpath"))}},
		Snapshot{Schema: SchemaVersion, Sections: []Section{sortItems(mk("100G mpath", "100G lvm"))}},
	)
	if d.Total != 0 {
		t.Errorf("an unchanged machine reported %d change(s): %+v", d.Total, d.Sections)
	}
}
