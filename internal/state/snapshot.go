package state

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"sort"
	"time"
)

// Item is one fact in a snapshot. Key is unique within its Section, Value is
// its comparable current value. For config files, Value holds a hash; for
// parameters, Value holds the parameter value itself.
type Item struct {
	Key   string `json:"key"`
	Value string `json:"value"`
	Note  string `json:"note,omitempty"`
	// ModifyOnly marks an item whose APPEARANCE and DISAPPEARANCE are not
	// changes to the machine -- only a change in its value is. It is for
	// transient entities that enter and leave a listing on their own: a
	// service's running state (systemd unloads idle/oneshot units from
	// `list-units`, so the key blinks in and out), for instance. Such an
	// item going A-present/B-absent is list churn, not an event; but its
	// value flipping active/running -> failed IS the event we want. Config
	// entities whose very existence is the signal -- an added listening
	// port, an installed package, an OOM kill -- leave this false so their
	// add/remove is still reported.
	ModifyOnly bool `json:"modify_only,omitempty"`
	// Mtime is the file's last-modification time as a Unix second, for items
	// whose Value is a content hash. Zero means unknown -- either the item is
	// not file-backed, or the snapshot predates this field.
	//
	// Deliberately NOT part of the comparison (see Compare, which reads Value
	// only). A touch that does not change content is not a configuration
	// change, and diffing mtime would report one. What it is for is the
	// opposite direction: once the hash says a file changed, its mtime says
	// WHEN, to the second, instead of "somewhere in the last 24 hours". That
	// is the one piece of provenance available without reading a package
	// manager log or an audit trail.
	Mtime int64 `json:"mtime,omitempty"`
}

// Section is a group of related facts produced by a single Collector.
type Section struct {
	Name    string `json:"name"`
	Title   string `json:"title"`
	Items   []Item `json:"items"`
	Skipped string `json:"skipped,omitempty"`
	// SkipDiff marks a section whose values are cumulative counters or
	// otherwise always-changing, so the generic change diff must ignore
	// it. Such sections get a purpose-built comparison instead (see
	// CompareProcesses).
	SkipDiff bool `json:"skip_diff,omitempty"`
	// Meta carries facts about the section as a whole rather than about any
	// one Item. The process section uses it to record the host's uptime,
	// which is what makes a cumulative per-process tick counter convertible
	// into a wall-clock start time.
	//
	// Deliberately not an Item: Items are diffed and displayed by key, and
	// process Items are keyed by comm, so any reserved key we picked could
	// collide with a real process name. Snapshots written before this field
	// existed have it nil, and every reader must treat absence as
	// "unknown" rather than as zero.
	Meta map[string]string `json:"meta,omitempty"`
	// PrivSensitive marks a section whose item keys or values depend on the
	// privilege the capture ran with, even when the collector did not have to
	// skip it outright.
	//
	// The Skipped/Unreadable mechanism only catches the all-or-nothing case. It
	// misses the worse one: `ss -lntuHp` as a non-root user still lists every
	// listening socket, it just silently drops the `users:` field for sockets
	// owned by somebody else. So a root baseline against a service-user capture
	// reports every port on the machine as Modified -- "nginx" -> "" -- and the
	// report reads as though every service on the host lost its process.
	// Config fingerprints do the same thing by key: a 0600 sshd_config is
	// simply absent from the unprivileged snapshot, so its item vanishes.
	//
	// Compare excludes these sections when the two captures ran as different
	// users (see Snapshot.Euid). A section whose contents are the same whoever
	// reads them -- sysctl, packages, modules -- must NOT set this, or a real
	// change would be hidden whenever the two runs happened to differ in uid.
	PrivSensitive bool `json:"priv_sensitive,omitempty"`
}

// Snapshot is a full flattening of a machine's enumerable state at one point in time.
// SchemaVersion is the collector layout version. It bumps whenever a
// collector changes how it KEYS its items -- adding an "enabled:"/"running:"
// prefix, filtering ephemeral interfaces, anything that makes a key present
// under one binary and absent under another for the same machine state.
// Compare uses it to tell "the machine changed" apart from "deltascope was
// upgraded between these two snapshots": across a version boundary a key
// that exists on only one side is a format migration, not an event, and is
// suppressed. Bump this on any keying change.
//
// 4: package keys carry arch (a multilib host had two `glibc` items fighting
// over one key, so which version won was decided by an unstable sort and the
// loser showed up as a phantom Modified on every comparison), route keys carry
// device and metric (two default routes are normal on a multi-homed host and
// both keyed as `route:default`), and a snap mount keys on its mountpoint
// without the revision so a refresh is one Modified instead of an add/remove
// pair.
//
// 5: the nic section exists. A whole new section is the same problem in its
// largest form -- every one of its keys is absent from every stored snapshot, so
// without the boundary the first comparison after the upgrade would report that
// the machine grew a driver version, an MTU, a ring size and eleven offload flags
// per interface overnight.
const SchemaVersion = 5

type Snapshot struct {
	Host     string    `json:"host"`
	Taken    time.Time `json:"taken"`
	Sections []Section `json:"sections"`
	// Schema is the collector layout version this snapshot was captured
	// with. Zero means "before versioning existed" (an old snapshot), which
	// Compare treats as a version boundary against any current snapshot.
	Schema int `json:"schema,omitempty"`
	// Euid is the effective uid the capture ran as.
	//
	// A pointer because the distinction that matters is "we know, and it was
	// root" versus "we do not know": every snapshot written before this field
	// existed decodes as nil, and reading that as uid 0 would claim a privilege
	// the capture may never have had. Two snapshots can only be compared for
	// privilege drift when both sides are known.
	Euid *int `json:"euid,omitempty"`
}

// Collector collects one Section. Implementations must be read-only, and on
// missing permissions or tools must return an empty Section with a Skipped
// note rather than an error, so partial success is preserved.
type Collector interface {
	Name() string
	Collect(ctx context.Context) Section
}

var registry []Collector

func register(c Collector) { registry = append(registry, c) }

// Collectors returns all registered collectors.
func Collectors() []Collector { return registry }

// sortItems orders a section's items for storage.
//
// Value breaks ties on Key. Two items should never share a key -- the
// collectors key to be unique -- but itemMap keeps the last one it sees, so if
// one ever slips through, an unstable sort would hand the win to a different
// item on each capture and an unchanged machine would report a phantom Modified
// that flips back and forth. A total order costs one comparison and makes that
// failure boring: the same item wins every time, so the diff is empty rather
// than alternating.
func sortItems(sec Section) Section {
	sort.Slice(sec.Items, func(i, j int) bool {
		if sec.Items[i].Key != sec.Items[j].Key {
			return sec.Items[i].Key < sec.Items[j].Key
		}
		return sec.Items[i].Value < sec.Items[j].Value
	})
	return sec
}

// Capture runs all collectors in turn, producing a complete snapshot.
func Capture(ctx context.Context, host string) Snapshot {
	euid := os.Geteuid()
	snap := Snapshot{Host: host, Taken: time.Now().UTC(), Schema: SchemaVersion, Euid: &euid}
	for _, c := range registry {
		snap.Sections = append(snap.Sections, sortItems(c.Collect(ctx)))
	}
	return snap
}

func hashBytes(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:12])
}
