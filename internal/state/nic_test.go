package state

import (
	"context"
	"testing"
)

// Real `ethtool -g` output. Two blocks, the same four labels in each, and the
// distinction between them carried only by a heading line.
const ringOut = `Ring parameters for eth0:
Pre-set maximums:
RX:		4096
RX Mini:	0
RX Jumbo:	0
TX:		4096
Current hardware settings:
RX:		512
RX Mini:	0
RX Jumbo:	0
TX:		256
`

func TestRingParamsSeparatesCurrentFromMaximum(t *testing.T) {
	curRX, curTX, maxRX, maxTX := ringParams(ringOut)
	if curRX != "512" || curTX != "256" {
		t.Errorf("current ring sizes read as rx=%q tx=%q, want 512/256", curRX, curTX)
	}
	if maxRX != "4096" || maxTX != "4096" {
		t.Errorf("pre-set maximums read as rx=%q tx=%q, want 4096/4096", maxRX, maxTX)
	}
}

// The value the reader sees. "256" alone does not say whether the queue is at
// the floor or the ceiling.
func TestRingValueCarriesTheHeadroom(t *testing.T) {
	curRX, curTX, maxRX, maxTX := ringParams(ringOut)
	if got := "rx " + ringPair(curRX, maxRX) + " tx " + ringPair(curTX, maxTX); got != "rx 512/4096 tx 256/4096" {
		t.Errorf("ring value is %q", got)
	}
	// A driver that reports no maximum must not produce a trailing slash.
	if got := ringPair("512", ""); got != "512" {
		t.Errorf("a missing maximum leaked into the value: %q", got)
	}
	if got := ringPair("512", "0"); got != "512" {
		t.Errorf("a zero maximum was rendered as headroom: %q", got)
	}
}

// RX Mini and RX Jumbo are zero on essentially every driver. Reading either of
// them as the RX ring would report a ring size of 0 on a healthy card.
func TestTheMiniAndJumboRingsAreNotMistakenForTheRXRing(t *testing.T) {
	curRX, _, _, _ := ringParams(ringOut)
	if curRX == "0" {
		t.Error("RX Mini or RX Jumbo was read as the RX ring")
	}
}

// `ethtool -k` indents sub-features, and this package's line reader trims
// leading space, so the allowlist is what stops `tx-checksum-ipv4: off [fixed]`
// from being stored as a feature of its own. A sub-feature sharing a name with a
// top-level feature would defeat that, so the allowlist may only hold top-level
// names.
const featOut = `Features for eth0:
rx-checksumming: on
tx-checksumming: on
	tx-checksum-ipv4: off [fixed]
	tx-checksum-ip-generic: on
scatter-gather: on
	tx-scatter-gather: on
	tx-scatter-gather-fraglist: off [fixed]
tcp-segmentation-offload: on
	tx-tcp-segmentation: on
	tx-tcp6-segmentation: on
generic-segmentation-offload: on
generic-receive-offload: off
large-receive-offload: off [fixed]
rx-vlan-offload: on
tx-vlan-offload: on
ntuple-filters: off
receive-hashing: on
highdma: on [fixed]
`

func TestOnlyAllowlistedTopLevelFeaturesAreStored(t *testing.T) {
	got := parseOffloads(featOut)
	if got["gro"] != "off" {
		t.Errorf("gro read as %q, want off", got["gro"])
	}
	if got["sg"] != "on" {
		t.Errorf("sg read as %q -- the indented tx-scatter-gather line may have overwritten it", got["sg"])
	}
	// [fixed] is kept: it is the answer to "my tuning script sets this and it
	// does not take".
	if got["lro"] != "off fixed" {
		t.Errorf("lro read as %q, want \"off fixed\"", got["lro"])
	}
	if _, ok := got["highdma"]; ok {
		t.Error("highdma is not in the allowlist but was stored anyway")
	}
	if len(got) != 11 {
		t.Errorf("stored %d features from a full -k dump, want the 11 allowlisted: %v", len(got), got)
	}
}

// The allowlist is also a promise that no entry is a sub-feature of another
// entry -- if one were, indentation being unavailable would make the pair
// ambiguous.
func TestNoAllowlistedFeatureIsASubFeatureOfAnother(t *testing.T) {
	for a := range nicOffloads {
		for b := range nicOffloads {
			if a != b && len(a) > len(b) && a[len(a)-len(b):] == b {
				t.Errorf("%q ends in %q; one is likely a sub-feature of the other, "+
					"which the trimmed-indentation parse cannot tell apart", a, b)
			}
		}
	}
}

func TestEthtoolFieldsParsesTheDriverBlock(t *testing.T) {
	f := ethtoolFields(`driver: ixgbe
version: 5.1.0-k
firmware-version: 0x800003e7, 1.1histor
expansion-rom-version:
bus-info: 0000:03:00.0
supports-statistics: yes
`)
	if f["driver"] != "ixgbe" || f["version"] != "5.1.0-k" {
		t.Errorf("driver/version parsed as %q/%q", f["driver"], f["version"])
	}
	// A firmware string containing a comma and a colon-free tail must survive
	// whole: only the FIRST colon separates key from value.
	if f["firmware-version"] != "0x800003e7, 1.1histor" {
		t.Errorf("firmware version parsed as %q", f["firmware-version"])
	}
	if f["bus-info"] != "0000:03:00.0" {
		t.Errorf("bus-info lost its own colons: %q", f["bus-info"])
	}
	if v, ok := f["expansion-rom-version"]; !ok || v != "" {
		t.Errorf("an empty field should parse as present-and-empty, got %q ok=%v", v, ok)
	}
}

func TestPauseValueNamesEveryFieldEvenWhenOneIsMissing(t *testing.T) {
	f := ethtoolFields("Pause parameters for eth0:\nAutonegotiate:\ton\nRX:\toff\nTX:\toff\n")
	if got := pauseValue(f); got != "autoneg on rx off tx off" {
		t.Errorf("pause value is %q", got)
	}
	// A driver reporting only autonegotiation must still produce a value that
	// names rx and tx, because "rx off" and a missing rx are different facts.
	if got := pauseValue(map[string]string{"Autonegotiate": "on"}); got != "autoneg on rx ? tx ?" {
		t.Errorf("a partial pause report rendered as %q", got)
	}
}

// Without ethtool the section reports that it collected nothing, rather than
// publishing the sysfs quarter of itself and letting the reader believe ring
// sizes and offloads are under watch. This is the path taken on any host where
// ethtool is not installed -- and on the developer machine, where these tests
// run, so the assertion is real everywhere.
func TestWithoutEthtoolTheSectionSaysSoInsteadOfHalfCollecting(t *testing.T) {
	sec := nic{}.Collect(context.Background())
	if sec.Name != "nic" || sec.Title == "" {
		t.Fatalf("section identity is %q/%q", sec.Name, sec.Title)
	}
	if sec.Skipped == "" && len(sec.Items) == 0 {
		t.Error("an empty section with no Skipped note: the comparison would read it as a section with nothing in it")
	}
	if sec.Skipped != "" && len(sec.Items) > 0 {
		t.Errorf("Skipped is set alongside %d items; every reader of Skipped "+
			"(timeline probes, the CLI summary, the Unreadable exclusion) treats it "+
			"as meaning the section is empty", len(sec.Items))
	}
}

// The reason SchemaVersion moved to 5. Every key in a brand-new section is
// absent from every stored snapshot, so the first comparison after the upgrade
// would otherwise report that the machine grew a driver version and eleven
// offload flags per NIC overnight.
func TestANewSectionIsNotReportedAsTheMachineChanging(t *testing.T) {
	old := Snapshot{Schema: 4, Sections: []Section{
		{Name: "network", Title: "Network Configuration", Items: []Item{{Key: "addr:1:eth0", Value: "inet 10.0.0.1/24"}}},
	}}
	now := Snapshot{Schema: SchemaVersion, Sections: []Section{
		{Name: "network", Title: "Network Configuration", Items: []Item{{Key: "addr:1:eth0", Value: "inet 10.0.0.1/24"}}},
		{Name: "nic", Title: "NIC Settings", Items: []Item{
			{Key: "nic.driver:eth0", Value: "ixgbe 5.1.0-k fw 0x800003e7"},
			{Key: "nic.ring:eth0", Value: "rx 512/4096 tx 512/4096"},
			{Key: "nic.offload:eth0:gro", Value: "on"},
		}},
	}}
	d := Compare(old, now)
	if !d.SchemaBoundary {
		t.Fatal("schema 4 against the current version is a version boundary")
	}
	if d.Total != 0 {
		t.Errorf("a section that only the newer binary collects produced %d changes: %+v", d.Total, d.Sections)
	}
	// And once both sides have it, a real ring-size change is reported.
	before := Snapshot{Schema: SchemaVersion, Sections: []Section{{Name: "nic", Title: "NIC Settings",
		Items: []Item{{Key: "nic.ring:eth0", Value: "rx 4096/4096 tx 4096/4096"}}}}}
	after := Snapshot{Schema: SchemaVersion, Sections: []Section{{Name: "nic", Title: "NIC Settings",
		Items: []Item{{Key: "nic.ring:eth0", Value: "rx 256/4096 tx 256/4096"}}}}}
	if d := Compare(before, after); d.Total != 1 {
		t.Errorf("a driver update resetting the ring produced %d changes, want 1", d.Total)
	}
}

// A link that flaps during a capture must not read as a NIC setting having been
// added and then removed; the same key's VALUE changing is the renegotiation we
// are here for.
func TestALinkGoingDownIsNotASettingChangeButARenegotiationIs(t *testing.T) {
	up := func(v string) Snapshot {
		return Snapshot{Schema: SchemaVersion, Sections: []Section{{Name: "nic", Title: "NIC Settings",
			Items: []Item{{Key: "nic.link:eth0", Value: v, ModifyOnly: true}}}}}
	}
	down := Snapshot{Schema: SchemaVersion, Sections: []Section{{Name: "nic", Title: "NIC Settings", Items: []Item{}}}}
	if d := Compare(up("1000Mb/s full autoneg on"), down); d.Total != 0 {
		t.Errorf("a link down at capture time was reported as a change: %+v", d.Sections)
	}
	if d := Compare(down, up("1000Mb/s full autoneg on")); d.Total != 0 {
		t.Errorf("a link coming back was reported as a change: %+v", d.Sections)
	}
	d := Compare(up("1000Mb/s full autoneg on"), up("100Mb/s half autoneg on"))
	if d.Total != 1 {
		t.Fatalf("renegotiation to 100Mb half produced %d changes, want 1", d.Total)
	}
}
