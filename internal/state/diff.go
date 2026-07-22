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

// Change 是一条状态差异。
type Change struct {
	Section string     `json:"section"`
	Title   string     `json:"title"`
	Key     string     `json:"key"`
	Kind    ChangeKind `json:"kind"`
	Old     string     `json:"old,omitempty"`
	New     string     `json:"new,omitempty"`
	Note    string     `json:"note,omitempty"`
}

// SectionDiff 汇总某 Section 的全部变化。
type SectionDiff struct {
	Name    string   `json:"name"`
	Title   string   `json:"title"`
	Changes []Change `json:"changes"`
}

// Diff 汇总两份快照之间的全部差异。
type Diff struct {
	A, B     Snapshot      `json:"-"`
	Sections []SectionDiff `json:"sections"`
	Total    int           `json:"total"`
}

func itoa(n int) string { return strconv.Itoa(n) }

// Compare 对账 a→b 两份快照,仅保留发生变化的项。
func Compare(a, b Snapshot) Diff {
	d := Diff{A: a, B: b}
	amap := indexSections(a)
	bmap := indexSections(b)

	names := unionKeys(amap, bmap)
	for _, name := range names {
		as, bs := amap[name], bmap[name]
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
				sd.Changes = append(sd.Changes, Change{
					Section: name, Title: title, Key: k, Kind: Added,
					New: bv.Value, Note: bv.Note,
				})
			case aok && !bok:
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
