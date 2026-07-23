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
}

// Section is a group of related facts produced by a single Collector.
type Section struct {
	Name    string `json:"name"`
	Title   string `json:"title"`
	Items   []Item `json:"items"`
	Skipped string `json:"skipped,omitempty"`
}

// Snapshot is a full flattening of a machine's enumerable state at one point in time.
type Snapshot struct {
	Host     string    `json:"host"`
	Taken    time.Time `json:"taken"`
	Sections []Section `json:"sections"`
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
	snap := Snapshot{Host: host, Taken: time.Now().UTC()}
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
