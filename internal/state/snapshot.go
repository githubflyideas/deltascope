package state

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
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
const SchemaVersion = 3

type Snapshot struct {
	Host     string    `json:"host"`
	Taken    time.Time `json:"taken"`
	Sections []Section `json:"sections"`
	// Schema is the collector layout version this snapshot was captured
	// with. Zero means "before versioning existed" (an old snapshot), which
	// Compare treats as a version boundary against any current snapshot.
	Schema int `json:"schema,omitempty"`
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

// Capture runs all collectors in turn, producing a complete snapshot.
func Capture(ctx context.Context, host string) Snapshot {
	snap := Snapshot{Host: host, Taken: time.Now().UTC(), Schema: SchemaVersion}
	for _, c := range registry {
		sec := c.Collect(ctx)
		sort.Slice(sec.Items, func(i, j int) bool { return sec.Items[i].Key < sec.Items[j].Key })
		snap.Sections = append(snap.Sections, sec)
	}
	return snap
}

func hashBytes(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:12])
}
