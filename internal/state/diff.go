package state

import (
	"sort"
	"strconv"
)

type ChangeKind string

const (
	Added    ChangeKind = "added"
	Removed  ChangeKind = "removed"
	Modified ChangeKind = "modified"
)

// Change is one state difference.
type Change struct {
	Section string     `json:"section"`
	Title   string     `json:"title"`
	Key     string     `json:"key"`
	Kind    ChangeKind `json:"kind"`
	Old     string     `json:"old,omitempty"`
	New     string     `json:"new,omitempty"`
	Note    string     `json:"note,omitempty"`
}

// SectionDiff summarizes all changes within one Section.
type SectionDiff struct {
	Name    string   `json:"name"`
	Title   string   `json:"title"`
	Changes []Change `json:"changes"`
}

// Diff summarizes all differences between two snapshots.
type Diff struct {
	A, B     Snapshot      `json:"-"`
	Sections []SectionDiff `json:"sections"`
	Total    int           `json:"total"`
	// Unreadable lists sections that could be collected in one snapshot
	// but not the other, usually a privilege difference. Their contents
	// are excluded from the diff because the change is in our access, not
	// in the machine.
	Unreadable []string `json:"unreadable,omitempty"`
	// SchemaBoundary is true when the two snapshots came from different
	// collector versions (deltascope was upgraded between them). Additions
	// and removals were suppressed as format migration; only value changes
	// are shown. The UI notes this so a sparse result is not mistaken for a
	// quiet machine.
	SchemaBoundary bool `json:"schema_boundary,omitempty"`
}

func itoa(n int) string { return strconv.Itoa(n) }

// Compare diffs snapshot a against b, keeping only items that changed.
func Compare(a, b Snapshot) Diff {
	d := Diff{A: a, B: b}
	// A schema mismatch means the two snapshots were captured by binaries
	// that key their items differently -- deltascope was upgraded between
	// them. Across that boundary, a key present on only one side is a format
	// migration, not a machine change, so add/remove is suppressed
	// everywhere and only value changes on keys common to both are reported.
	// Same-schema (the steady state) is unaffected.
	crossVersion := a.Schema != b.Schema
	if crossVersion {
		d.SchemaBoundary = true
	}
	amap := indexSections(a)
	bmap := indexSections(b)

	names := unionKeys(amap, bmap)
	for _, name := range names {
		as, bs := amap[name], bmap[name]
		if as.SkipDiff || bs.SkipDiff {
			continue // cumulative counters; see CompareProcesses
		}
		// If a collector succeeded on one side and was skipped on the
		// other, the difference is in what we could read, not in the
		// machine. Snapshots taken with different privileges (the service
		// user cannot read iptables; a manual run as root can) would
		// otherwise report every item in the section as added or removed.
		if (as.Skipped == "") != (bs.Skipped == "") {
			d.Unreadable = append(d.Unreadable, name)
			continue
		}
		title := bs.Title
		if title == "" {
			title = as.Title
		}
		sd := SectionDiff{Name: name, Title: title}

		ai := itemMap(as)
		bi := itemMap(bs)
		for _, k := range unionItemKeys(ai, bi) {
			av, aok := ai[k]
			bv, bok := bi[k]
			switch {
			case aok && bok && av.Value != bv.Value:
				sd.Changes = append(sd.Changes, Change{
					Section: name, Title: title, Key: k, Kind: Modified,
					Old: av.Value, New: bv.Value, Note: bv.Note,
				})
			case !aok && bok:
				// A modify-only item appearing is list churn (a transient
				// entity entered the listing on its own), not a change to
				// the machine -- suppress it. Its value changing, when it is
				// present on both sides, is still reported above. Across a
				// schema boundary, suppress ALL appearances: a key that only
				// the newer binary emits is a format migration, not an event.
				if bv.ModifyOnly || crossVersion {
					continue
				}
				sd.Changes = append(sd.Changes, Change{
					Section: name, Title: title, Key: k, Kind: Added,
					New: bv.Value, Note: bv.Note,
				})
			case aok && !bok:
				if av.ModifyOnly || crossVersion {
					continue
				}
				sd.Changes = append(sd.Changes, Change{
					Section: name, Title: title, Key: k, Kind: Removed,
					Old: av.Value, Note: av.Note,
				})
			}
		}
		if len(sd.Changes) > 0 {
			sort.Slice(sd.Changes, func(i, j int) bool { return sd.Changes[i].Key < sd.Changes[j].Key })
			d.Sections = append(d.Sections, sd)
			d.Total += len(sd.Changes)
		}
	}
	return d
}

func indexSections(s Snapshot) map[string]Section {
	m := make(map[string]Section, len(s.Sections))
	for _, sec := range s.Sections {
		m[sec.Name] = sec
	}
	return m
}

func itemMap(s Section) map[string]Item {
	m := make(map[string]Item, len(s.Items))
	for _, it := range s.Items {
		m[it.Key] = it
	}
	return m
}

func unionKeys(a, b map[string]Section) []string {
	seen := map[string]bool{}
	var out []string
	for _, sec := range registry {
		if _, ok := a[sec.Name()]; ok {
			seen[sec.Name()] = true
			out = append(out, sec.Name())
		} else if _, ok := b[sec.Name()]; ok {
			seen[sec.Name()] = true
			out = append(out, sec.Name())
		}
	}
	for k := range a {
		if !seen[k] {
			seen[k] = true
			out = append(out, k)
		}
	}
	for k := range b {
		if !seen[k] {
			seen[k] = true
			out = append(out, k)
		}
	}
	return out
}

func unionItemKeys(a, b map[string]Item) []string {
	seen := map[string]bool{}
	var out []string
	for k := range a {
		seen[k] = true
		out = append(out, k)
	}
	for k := range b {
		if !seen[k] {
			out = append(out, k)
		}
	}
	return out
}
