package state

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
)

// The NIC settings collector.
//
// Every fact here shares one property: it changes without anybody deciding to
// change it, and it costs throughput when it does. A driver update resets the
// ring descriptors to the driver's default and the host starts dropping at the
// NIC under the same load it carried yesterday. A switch port is reconfigured,
// autonegotiation lands on 100Mb half-duplex, and the machine is a tenth as fast
// with no error anywhere. A tuning script -- or an upgrade that reverted it --
// turns GRO off. The kernel says nothing about any of it, and none of it appears
// in a metric until the retransmits start.
//
// deltascope collected none of it. `ethtool <iface>` is the prescription three
// diagnoses already print (internal/reasoning/diagnosis.go), which tells the
// reader to go and look at the current value -- the one thing a NIC problem's
// cause is least likely to be. Stored per capture, the same fact answers the
// question the reader actually has: it was 4096 last week, and it became 256
// somewhere between Tuesday and Wednesday.
//
// Values only, never a set: ethtool is invoked exclusively with its query forms.

type nic struct{}

func (nic) Name() string { return "nic" }

const sysNet = "/sys/class/net"

// nicInterfaces lists the interfaces whose settings are host configuration,
// in a stable order.
//
// Device-backed only. /sys/class/net/<if>/device exists for an interface sitting
// on a real PCI/USB/virtio device and not for a bond, a bridge, a VLAN or a
// tunnel -- and ring sizes, negotiated duplex, pause frames and driver firmware
// are properties of the hardware underneath, so the interfaces without one have
// nothing here to record. The stacked interfaces are a real subject and a
// separate one: their members and options live in /etc, which the config
// fingerprint section already hashes.
//
// SR-IOV virtual functions are dropped even though they are device-backed. A VF
// is handed to a guest, which sets its own offloads; recording them would fill
// this section with a hundred keys that answer for somebody else's machine and
// churn every time a guest boots. The physfn link is what distinguishes a VF
// from the physical function it was carved out of.
func nicInterfaces() []string {
	ents, err := os.ReadDir(sysNet)
	if err != nil {
		return nil
	}
	var out []string
	for _, e := range ents {
		name := e.Name()
		if name == "lo" || ephemeralIfaceRe.MatchString(name) {
			continue
		}
		dev := filepath.Join(sysNet, name, "device")
		if _, err := os.Stat(dev); err != nil {
			continue
		}
		if _, err := os.Stat(filepath.Join(dev, "physfn")); err == nil {
			continue // an SR-IOV virtual function: the guest's setting, not ours
		}
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}

// sysNetAttr reads one /sys/class/net attribute, "" if unreadable. Several of
// these -- speed, duplex, carrier -- are also in `ethtool <iface>` output, but
// reading the file is both cheaper than a subprocess and unambiguous, where the
// ethtool text prints "Unknown!" for a value that is merely unavailable.
func sysNetAttr(iface, attr string) string {
	b, err := os.ReadFile(filepath.Join(sysNet, iface, attr))
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(b))
}

// nicDriverName is the driver bound to the interface, from the symlink the
// kernel maintains. ethtool -i reports the same name; this is the fallback for
// when the -i probe fails but the interface is plainly there.
func nicDriverName(iface string) string {
	l, err := os.Readlink(filepath.Join(sysNet, iface, "device", "driver"))
	if err != nil {
		return ""
	}
	return filepath.Base(l)
}

// ethtoolFields parses the `key: value` form that ethtool -i prints, lowercasing
// nothing and keeping the last value for a repeated key.
func ethtoolFields(out string) map[string]string {
	m := map[string]string{}
	for _, l := range lines(out) {
		i := strings.IndexByte(l, ':')
		if i <= 0 {
			continue
		}
		m[strings.TrimSpace(l[:i])] = strings.TrimSpace(l[i+1:])
	}
	return m
}

// ringParams pulls the current and pre-set-maximum RX/TX descriptor counts out
// of `ethtool -g` output, which prints the same four labels twice under two
// headings:
//
//	Pre-set maximums:
//	RX:             4096
//	...
//	Current hardware settings:
//	RX:             512
//
// Only the exact labels RX and TX are taken: "RX Mini" and "RX Jumbo" are
// separate rings that are 0 on essentially every modern driver, and including
// them would put two always-zero numbers in the value where a reader is looking
// for the one that changed.
func ringParams(out string) (curRX, curTX, maxRX, maxTX string) {
	current := false
	for _, l := range lines(out) {
		if strings.HasPrefix(l, "Current hardware settings") {
			current = true
			continue
		}
		i := strings.IndexByte(l, ':')
		if i <= 0 {
			continue
		}
		label, val := strings.TrimSpace(l[:i]), strings.TrimSpace(l[i+1:])
		switch {
		case label == "RX" && current:
			curRX = val
		case label == "TX" && current:
			curTX = val
		case label == "RX":
			maxRX = val
		case label == "TX":
			maxTX = val
		}
	}
	return
}

// nicOffloads maps the ethtool feature names worth storing to the short names
// the operator and the documentation both use.
//
// An allowlist, not everything `ethtool -k` prints. A modern driver reports
// upwards of sixty features, most of them sub-flags of the ones below that move
// only when their parent does -- so storing all of them would turn one GRO
// change into a dozen rows, and bury it. These eleven are the ones a tuning
// guide tells you to touch and a driver update is known to reset.
//
// Sub-feature lines are indented in ethtool's output and this collector's line
// reader trims leading space, so the allowlist is doing double duty: it is also
// what keeps `tx-checksum-ipv4` from being read as a top-level feature. Every
// name below is a top-level one, and no sub-feature shares a name with a
// top-level feature.
var nicOffloads = map[string]string{
	"rx-checksumming":              "rx-csum",
	"tx-checksumming":              "tx-csum",
	"scatter-gather":               "sg",
	"tcp-segmentation-offload":     "tso",
	"generic-segmentation-offload": "gso",
	"generic-receive-offload":      "gro",
	"large-receive-offload":        "lro",
	"rx-vlan-offload":              "rx-vlan",
	"tx-vlan-offload":              "tx-vlan",
	"ntuple-filters":               "ntuple",
	"receive-hashing":              "rxhash",
}

// offloadValue normalizes one `ethtool -k` value. The "[fixed]" marker means the
// driver refuses to change the flag, which is worth keeping: it is the answer to
// "my tuning script sets this and it does not take", and a driver update that
// makes a fixed flag settable is a real change in what the host can be tuned to.
func offloadValue(v string) string {
	v = strings.TrimSpace(v)
	if i := strings.IndexByte(v, '['); i >= 0 {
		state := strings.TrimSpace(v[:i])
		if strings.Contains(v[i:], "fixed") {
			return state + " fixed"
		}
		return state
	}
	return v
}

// parseOffloads reduces `ethtool -k` output to the allowlisted features, keyed
// by their short names.
func parseOffloads(out string) map[string]string {
	got := map[string]string{}
	for _, l := range lines(out) {
		i := strings.IndexByte(l, ':')
		if i <= 0 {
			continue
		}
		short, want := nicOffloads[strings.TrimSpace(l[:i])]
		if !want {
			continue
		}
		if v := offloadValue(l[i+1:]); v != "" {
			got[short] = v
		}
	}
	return got
}

// pauseValue renders flow control, naming every field even when the driver
// reported only some of them -- "rx off" and a missing rx are different facts.
func pauseValue(f map[string]string) string {
	return "autoneg " + or(f["Autonegotiate"], "?") +
		" rx " + or(f["RX"], "?") + " tx " + or(f["TX"], "?")
}

func (nic) Collect(ctx context.Context) Section {
	sec := Section{Name: "nic", Title: "NIC Settings"}

	// The whole section is gated on ethtool, including the parts read from
	// sysfs. Without it the ring, pause and offload keys are simply absent, and
	// a section that quietly drops three quarters of what it claims to cover is
	// worse than one that says it collected nothing: the Skipped note is carried
	// into the comparison, so a baseline taken where ethtool was installed is
	// excluded from the diff by name instead of reporting every ring size as
	// having been removed from the machine.
	if _, err := exec.LookPath("ethtool"); err != nil {
		sec.Skipped = "ethtool not found"
		return sec
	}
	ifaces := nicInterfaces()
	if len(ifaces) == 0 {
		sec.Skipped = "no device-backed network interfaces"
		return sec
	}

	for _, ifc := range ifaces {
		if ctx.Err() != nil {
			break // capture deadline: leave what we have rather than block the collectors behind us
		}
		sec.Items = append(sec.Items, nicItems(ctx, ifc)...)
	}
	if len(sec.Items) == 0 {
		sec.Skipped = "ethtool returned nothing for any interface"
	}
	return sec
}

// nicItems collects one interface's settings.
func nicItems(ctx context.Context, ifc string) []Item {
	var items []Item

	// Driver, its version and the card's firmware. Grouped into one value
	// because they move together -- a driver update is one event, and splitting
	// it into three rows makes the reader reassemble it.
	drv, ver, fw := nicDriverName(ifc), "", ""
	if out, ok := runCmd(ctx, "ethtool", "-i", ifc); ok {
		f := ethtoolFields(out)
		if f["driver"] != "" {
			drv = f["driver"]
		}
		ver, fw = f["version"], f["firmware-version"]
		// Where the card sits. A change here is the card having been moved,
		// replaced, or renumbered by a firmware update that reordered the bus --
		// which is also how an interface silently swaps names with another.
		if bus := f["bus-info"]; bus != "" {
			items = append(items, Item{Key: "nic.bus:" + ifc, Value: bus})
		}
	}
	if drv != "" {
		v := drv
		if ver != "" {
			v += " " + ver
		}
		if fw != "" {
			v += " fw " + fw
		}
		items = append(items, Item{Key: "nic.driver:" + ifc, Value: v})
	}

	// MTU: jumbo frames configured months ago and reverted by a reboot that
	// dropped an ifcfg line is a standing cause of a path that works for small
	// packets and stalls on large ones.
	if mtu := sysNetAttr(ifc, "mtu"); mtu != "" {
		items = append(items, Item{Key: "nic.mtu:" + ifc, Value: mtu})
	}

	// The negotiated link, recorded only while there is one.
	//
	// ModifyOnly, which is the point of the item. A link that is down has no
	// speed or duplex to record -- sysfs reports -1 -- so the key is absent, and
	// a link that flaps during a capture must not be reported as a NIC setting
	// having appeared and then been removed. What IS reported is the value
	// changing while the link was up both times: 1000Mb/s full -> 100Mb/s half
	// is the renegotiation that makes a host inexplicably slow, and it is exactly
	// the kind of change no metric names and no log records.
	if sysNetAttr(ifc, "carrier") == "1" {
		link := ""
		if sp := sysNetAttr(ifc, "speed"); sp != "" && sp != "-1" {
			link = sp + "Mb/s"
		}
		if dx := sysNetAttr(ifc, "duplex"); dx != "" && dx != "unknown" {
			link = strings.TrimSpace(link + " " + dx)
		}
		if out, ok := runCmd(ctx, "ethtool", ifc); ok {
			if an := ethtoolFields(out)["Auto-negotiation"]; an != "" {
				link = strings.TrimSpace(link + " autoneg " + an)
			}
		}
		if link != "" {
			items = append(items, Item{Key: "nic.link:" + ifc, Value: link, ModifyOnly: true})
		}
	}

	// Ring descriptors, current out of maximum. The maximum belongs in the same
	// value: "256" alone does not say whether the queue is at the floor or the
	// ceiling, and the maximum itself moves when the driver changes.
	if out, ok := runCmd(ctx, "ethtool", "-g", ifc); ok {
		curRX, curTX, maxRX, maxTX := ringParams(out)
		if curRX != "" || curTX != "" {
			items = append(items, Item{Key: "nic.ring:" + ifc,
				Value: "rx " + ringPair(curRX, maxRX) + " tx " + ringPair(curTX, maxTX)})
		}
	}

	// Flow control. Pause frames left on where the switch does not expect them
	// let one slow receiver throttle a whole port, and they are turned on and off
	// by driver defaults as often as by anybody's decision.
	if out, ok := runCmd(ctx, "ethtool", "-a", ifc); ok {
		f := ethtoolFields(out)
		if f["RX"] != "" || f["TX"] != "" || f["Autonegotiate"] != "" {
			items = append(items, Item{Key: "nic.pause:" + ifc, Value: pauseValue(f)})
		}
	}

	// Offloads, one item per feature. Keyed per feature rather than joined into
	// one value so that GRO going off is a row that says GRO, instead of a
	// sixty-token value the reader has to diff by eye.
	//
	// Emitted in sorted order. Capture sorts every section before storing it and
	// the keys here are unique, so map order could not actually change what is
	// stored -- but this package has been bitten twice by an unstable order
	// deciding which of two facts a reader sees, and a collector that does not
	// depend on being sorted afterwards is one less place to check.
	if out, ok := runCmd(ctx, "ethtool", "-k", ifc); ok {
		feats := parseOffloads(out)
		shorts := make([]string, 0, len(feats))
		for short := range feats {
			shorts = append(shorts, short)
		}
		sort.Strings(shorts)
		for _, short := range shorts {
			items = append(items, Item{Key: "nic.offload:" + ifc + ":" + short, Value: feats[short]})
		}
	}
	return items
}

// ringPair renders "current/maximum", or just the current value when the
// pre-set maximum was not reported.
func ringPair(cur, max string) string {
	if cur == "" {
		cur = "?"
	}
	if max == "" || max == "0" {
		return cur
	}
	return cur + "/" + max
}

func or(v, fallback string) string {
	if v == "" {
		return fallback
	}
	return v
}

func init() { register(nic{}) }
