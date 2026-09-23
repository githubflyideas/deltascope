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
	// Mtime is the surviving side's file modification time in Unix seconds,
	// for file-backed items (see Item.Mtime). Zero means unknown. It is the
	// only per-item timestamp in change accounting: everything else is dated
	// only by the interval between the two snapshots.
	Mtime int64 `json:"mtime,omitempty"`
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
	// PrivilegeDrift names sections excluded because the two captures ran as
	// different users and the section's contents depend on that (see
	// Section.PrivSensitive). Like Unreadable, the difference is in our access
	// rather than in the machine -- but it is a distinct claim, because these
	// sections were not skipped: both captures produced a full-looking list,
	// and comparing them would report the whole list as changed.
	PrivilegeDrift []string `json:"privilege_drift,omitempty"`
	// EuidA and EuidB are the effective uids the two captures ran as, recorded
	// only when PrivilegeDrift is non-empty -- whoever has to fix the drift
	// needs to know which run was which.
	EuidA int `json:"euid_a,omitempty"`
	EuidB int `json:"euid_b,omitempty"`
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
	// Privilege drift, which is a different claim from a schema boundary and is
	// only decidable when both captures recorded who they ran as. A snapshot
	// from before Euid existed decodes as nil, and guessing root for it would
	// silently suppress real changes.
	privDrift := a.Euid != nil && b.Euid != nil && *a.Euid != *b.Euid
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
		// Both sides produced a list, but the lists answer to different users.
		// `ss -lntuHp` run unprivileged keeps every socket and drops only the
		// owning process, so comparing the two would report every port on the
		// machine as having lost its service. Excluded rather than diffed, and
		// named in the payload so the shorter report says why it is short.
		if privDrift && (as.PrivSensitive || bs.PrivSensitive) {
			d.PrivilegeDrift = append(d.PrivilegeDrift, name)
			d.EuidA, d.EuidB = *a.Euid, *b.Euid
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
					Old: av.Value, New: bv.Value, Note: bv.Note, Mtime: bv.Mtime,
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
					New: bv.Value, Note: bv.Note, Mtime: bv.Mtime,
				})
			case aok && !bok:
				if av.ModifyOnly || crossVersion {
					continue
				}
				sd.Changes = append(sd.Changes, Change{
					Section: name, Title: title, Key: k, Kind: Removed,
					Old: av.Value, Note: av.Note, Mtime: av.Mtime,
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
