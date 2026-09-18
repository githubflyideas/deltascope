package state

import (
	"sort"
	"strings"
	"time"
)

// The change timeline: dating a diff, and grouping what moved together.
//
// A diff between two points in time says WHAT is different and nothing about
// when, or about what belongs with what. One `dnf upgrade nginx` arrives as
// four rows in three sections. A kernel upgrade arrives as the kernel version,
// the build string, the boot cmdline, the package, every module that came or
// went, and every /proc/sys node the new kernel added or retired -- hundreds of
// rows, all of them one event, with nothing on the page saying so.
//
// What fixes that is already on disk. The scheduler stores a full snapshot
// every 10 minutes and keeps a week, so between any A and B there are up to a
// thousand intermediate observations that nothing has ever queried. Bisecting
// them dates each change to the interval between two adjacent snapshots, and
// changes that land in the same interval are, as far as this machine can tell,
// one event.
//
// Nothing new is collected for any of this.

// Event is a set of changes the stored history places in the same interval.
type Event struct {
	// From and To bound the change: at From it had not happened, at To it had.
	// Adjacent snapshot times when the bisect converged, the whole requested
	// window when it could not.
	From time.Time `json:"from"`
	To   time.Time `json:"to"`
	// Dated is false when From/To are still the full A..B window, i.e. the
	// history could not narrow this change at all. The distinction is the
	// point: "changed at 14:20" and "changed sometime in the last 24 hours"
	// are different claims and must not render as the same one.
	Dated bool `json:"dated"`
	// Class and Subject say what the event was about as data rather than as a
	// sentence, so the browser words it in the reader's language instead of
	// receiving more English prose from the server.
	Class   string `json:"class"`             // kernel | packages | section | mixed
	Subject string `json:"subject,omitempty"` // the single package or key, when there is one
	// Unstable marks a value observed moving more than once inside the window
	// -- flapping, not one change. From/To then bound only the last transition
	// the probes happened to catch.
	Unstable bool     `json:"unstable,omitempty"`
	Changes  []Change `json:"changes"`
}

// SnapshotSource is the slice of *Store the timeline reads. An interface so
// the bisect is testable against a fixture history with no database.
type SnapshotSource interface {
	Stamps(from, to time.Time) ([]Stamp, error)
	ByID(id int64) (Snapshot, error)
}

// DefaultProbeBudget caps how many snapshot bodies one Locate call loads. The
// bisect costs O(k log n) probes for k distinct change times, so 24 covers
// several independent events across a week of 10-minute snapshots. The cap is
// what stops a pathological diff -- a hundred changes at a hundred different
// times -- from turning one page load into a hundred JSON parses.
const DefaultProbeBudget = 24

// MaxDatableChanges is the diff size above which dating is skipped outright.
// Past a few hundred rows the reader's question is no longer "when did this
// happen" but "why are there so many rows", and the answer to that is usually
// one event, not three hundred.
const MaxDatableChanges = 400

// Locate dates every change in d against the snapshots stored between its two
// endpoints, and groups the changes that landed in the same interval.
//
// Every failure degrades to one undated event spanning the whole window: no
// source, no intermediate snapshots, a read error, too many changes. That
// event has the same shape as a dated one with Dated false, so no caller has
// to handle two kinds of result, and every change is reported exactly once
// either way.
func Locate(src SnapshotSource, d Diff) []Event {
	all := flattenChanges(d)
	if len(all) == 0 {
		return nil
	}
	whole := func() []Event {
		ev := Event{From: d.A.Taken, To: d.B.Taken, Changes: all}
		sortChanges(ev.Changes)
		classify(&ev)
		return []Event{ev}
	}
	if src == nil || len(all) > MaxDatableChanges {
		return whole()
	}
	probes, err := src.Stamps(d.A.Taken, d.B.Taken)
	if err != nil || len(probes) == 0 {
		return whole()
	}
	l := &locator{
		src: src, probes: probes,
		aTime: d.A.Taken, bTime: d.B.Taken,
		schema: d.B.Schema, budget: DefaultProbeBudget,
		loaded:  map[int]*probe{},
		buckets: map[[2]int][]Change{},
	}
	l.narrow(-1, len(probes), all, 0)
	return l.events()
}

func flattenChanges(d Diff) []Change {
	out := make([]Change, 0, d.Total)
	for _, sd := range d.Sections {
		out = append(out, sd.Changes...)
	}
	return out
}

// locator carries the state of one bisect: the probe grid, the bodies loaded
// so far, and where each change has been narrowed to.
type locator struct {
	src          SnapshotSource
	probes       []Stamp
	aTime, bTime time.Time
	schema       int
	budget       int
	loaded       map[int]*probe
	// buckets maps a converged (lo, hi) probe-index pair to the changes that
	// landed in it. Co-occurrence in one bucket is the whole grouping signal:
	// the history cannot distinguish changes inside one interval, so as far as
	// this machine can tell they happened together.
	buckets map[[2]int][]Change
}

// probe is one loaded snapshot, indexed for lookup by section and key.
//
// has is kept separate from val rather than leaning on a zero value, because
// an empty value is legitimate: a non-root `ss` omits the users: field, so
// every listening port carries Value == "".
type probe struct {
	known map[string]bool   // section was collected and not skipped
	has   map[string]bool   // section \x00 key was present
	val   map[string]string // section \x00 key -> value
}

func ck(section, key string) string { return section + "\x00" + key }

// timeAt maps a probe index to a wall time, with -1 meaning the A snapshot and
// len(probes) meaning B -- the two virtual endpoints the bisect works between.
func (l *locator) timeAt(i int) time.Time {
	switch {
	case i < 0:
		return l.aTime
	case i >= len(l.probes):
		return l.bTime
	default:
		return l.probes[i].Taken
	}
}

// load reads and indexes one probe. It returns nil when the probe cannot
// speak: budget exhausted, read error, or a body written by a different
// collector version. Cross-version probes are excluded for the same reason
// Compare suppresses add/remove across that boundary -- their keys mean
// something else, so presence there says nothing about presence here.
func (l *locator) load(i int) *probe {
	if p, ok := l.loaded[i]; ok {
		return p // including a cached nil: a bad probe is not retried
	}
	if l.budget <= 0 {
		return nil
	}
	l.budget--
	snap, err := l.src.ByID(l.probes[i].ID)
	if err != nil || snap.Schema != l.schema {
		l.loaded[i] = nil
		return nil
	}
	p := &probe{
		known: make(map[string]bool, len(snap.Sections)),
		has:   map[string]bool{},
		val:   map[string]string{},
	}
	for _, sec := range snap.Sections {
		if sec.Skipped != "" {
			continue // collected nothing then: no opinion on its keys
		}
		p.known[sec.Name] = true
		for _, it := range sec.Items {
			k := ck(sec.Name, it.Key)
			p.has[k] = true
			p.val[k] = it.Value
		}
	}
	l.loaded[i] = p
	return p
}

// probeOrder lists the probe indices strictly inside (lo, hi), midpoint first
// and then alternating outward. Midpoint first is the bisect; stepping outward
// is what keeps one unreadable snapshot, or one section that was unreadable at
// that moment, from costing the whole dating.
func probeOrder(lo, hi int) []int {
	mid := lo + (hi-lo)/2
	out := make([]int, 0, hi-lo)
	for off := 0; off < hi-lo; off++ {
		for _, i := range [2]int{mid + off, mid - off} {
			if i > lo && i < hi {
				out = append(out, i)
			}
			if off == 0 {
				break // mid+0 and mid-0 are the same probe
			}
		}
	}
	return out
}

// maxProbeAttempts bounds how many different probes one interval may be
// narrowed with. Without it, an interval where every snapshot is unreadable
// would walk the whole week looking for a good one and spend the budget that
// the intervals after it need.
const maxProbeAttempts = 3

// usableProbe loads the first candidate after skipping the ones earlier
// attempts already used.
func (l *locator) usableProbe(lo, hi, skip int) (int, *probe) {
	order := probeOrder(lo, hi)
	for i, tries := skip, 0; i < len(order) && tries < maxProbeAttempts; i, tries = i+1, tries+1 {
		if p := l.load(order[i]); p != nil {
			return order[i], p
		}
	}
	return 0, nil
}

// narrow shrinks the interval (lo, hi) for each change in cs.
//
// Invariant on entry: at lo none of these changes had happened, and at hi all
// of them had. lo == -1 is the A snapshot and hi == len(probes) is B, which is
// precisely what the diff established, so the invariant holds at the top level
// by construction, and each recursion preserves it by splitting cs on what the
// probe saw. attempt counts how many probes this interval has already tried.
func (l *locator) narrow(lo, hi int, cs []Change, attempt int) {
	if len(cs) == 0 {
		return
	}
	if hi-lo <= 1 || attempt >= maxProbeAttempts {
		l.park(lo, hi, cs) // adjacent snapshots, or out of probes to ask
		return
	}
	mid, p := l.usableProbe(lo, hi, attempt)
	if p == nil {
		l.park(lo, hi, cs)
		return
	}
	var done, pending, blocked []Change
	for _, ch := range cs {
		happened, ok := p.reached(ch)
		switch {
		case !ok:
			// This probe cannot answer for THIS change -- the section was
			// unreadable then, or the key was momentarily absent -- though it
			// may answer for the others. Retry the same interval with the next
			// probe rather than guess a side and date it wrongly.
			blocked = append(blocked, ch)
		case happened:
			done = append(done, ch)
		default:
			pending = append(pending, ch)
		}
	}
	l.narrow(lo, hi, blocked, attempt+1)
	l.narrow(lo, mid, done, 0)
	l.narrow(mid, hi, pending, 0)
}

func (l *locator) park(lo, hi int, cs []Change) {
	k := [2]int{lo, hi}
	l.buckets[k] = append(l.buckets[k], cs...)
}

// flapped reports whether the loaded probes show the value of this change
// moving more than once in the window. A single transition is monotone: once
// the new value appears, every later probe has it too. Any probe that breaks
// that monotonicity proves the value went back and forth -- flapping, not one
// change. The (lo, hi) interval then bounds only the last transition the
// bisect landed on.
//
// A bisect alone cannot notice this. It splits on one probe per interval, and
// every answer it gets is consistent with a single transition by construction.
// The evidence comes from the full set of loaded probes, which includes the
// ones other changes needed -- so this costs no reads, and a busy window,
// exactly where flapping is likely, is also where the most probes are loaded.
func (l *locator) flapped(lo, hi int, ch Change) bool {
	// Build a time-ordered sequence of reached/unreached observations.
	// Any "reached then unreached" transition proves non-monotonicity.
	type obs struct {
		idx      int
		happened bool
	}
	var observations []obs
	for i, p := range l.loaded {
		if p == nil {
			continue
		}
		happened, ok := p.reached(ch)
		if !ok {
			continue
		}
		observations = append(observations, obs{i, happened})
	}
	if len(observations) < 2 {
		return false
	}
	sort.Slice(observations, func(a, b int) bool {
		return observations[a].idx < observations[b].idx
	})
	// A monotone sequence of {false, false, ..., true, true, ...} has exactly
	// zero false-after-true transitions. Anything else is a flap.
	seenTrue := false
	for _, o := range observations {
		if o.happened {
			seenTrue = true
		} else if seenTrue {
			return true
		}
	}
	return false
}

// reached reports whether this change had already happened as of this probe,
// and whether the probe can answer at all.
func (p *probe) reached(ch Change) (happened, ok bool) {
	if !p.known[ch.Section] {
		return false, false
	}
	k := ck(ch.Section, ch.Key)
	switch ch.Kind {
	case Added:
		return p.has[k], true
	case Removed:
		return !p.has[k], true
	default:
		if !p.has[k] {
			// Present at both ends of the window but absent here: the key left
			// and came back. Neither "before" nor "after" is true of this
			// probe, so it is no use for this change.
			return false, false
		}
		return p.val[k] == ch.New, true
	}
}

// events turns the converged buckets into the reported timeline.
func (l *locator) events() []Event {
	out := make([]Event, 0, len(l.buckets))
	for span, cs := range l.buckets {
		ev := Event{
			From: l.timeAt(span[0]),
			To:   l.timeAt(span[1]),
			// Dated is false only for the bucket that never moved off the
			// original endpoints: everything else was narrowed by at least one
			// probe, and saying so is the difference between a time and a shrug.
			Dated:   span[0] >= 0 || span[1] < len(l.probes),
			Changes: cs,
		}
		for _, ch := range cs {
			if l.flapped(span[0], span[1], ch) {
				ev.Unstable = true
				break
			}
		}
		sortChanges(ev.Changes)
		classify(&ev)
		out = append(out, ev)
	}
	// Newest first: the reader is asking what changed, and the most recent
	// change is the one most likely to explain the machine in front of them.
	sort.Slice(out, func(i, j int) bool {
		if !out[i].To.Equal(out[j].To) {
			return out[i].To.After(out[j].To)
		}
		return out[i].From.After(out[j].From)
	})
	return out
}

// sortChanges orders changes inside one event by collector registry position
// then key, which is the order every other view presents sections in.
func sortChanges(cs []Change) {
	order := make(map[string]int, len(registry))
	for i, c := range registry {
		order[c.Name()] = i
	}
	pos := func(name string) int {
		if i, ok := order[name]; ok {
			return i
		}
		return len(order) // a section no longer in the registry sorts last
	}
	sort.SliceStable(cs, func(i, j int) bool {
		if a, b := pos(cs[i].Section), pos(cs[j].Section); a != b {
			return a < b
		}
		if cs[i].Section != cs[j].Section {
			return cs[i].Section < cs[j].Section
		}
		return cs[i].Key < cs[j].Key
	})
}

// classify names an event by what it was: a class, plus at most one subject.
//
// Deliberately no prose. Every English sentence the server composes is a
// sentence a non-English operator reads in English, and the UI already carries
// ten locales; sending the class as data lets the browser word it. Class is
// also why the fan-out is worth grouping at all -- "kernel upgrade" over 300
// rows is a different page from 300 unexplained rows.
func classify(ev *Event) {
	sections := map[string]bool{}
	var pkgs []string
	kernel := false
	for _, ch := range ev.Changes {
		sections[ch.Section] = true
		switch ch.Section {
		case "packages":
			pkgs = append(pkgs, ch.Key)
			if strings.HasPrefix(ch.Key, "kernel") {
				kernel = true
			}
		case "system":
			if ch.Key == "kernel" || ch.Key == "kernel.build" || ch.Key == "boot.cmdline" {
				kernel = true
			}
		}
	}
	switch {
	case kernel:
		// Checked before packages, and this is the fan-out that made grouping
		// worth building: a new kernel rewrites the module list and every
		// /proc/sys node it added or retired, so it arrives as hundreds of rows
		// across four sections with nothing saying they are one thing.
		ev.Class = "kernel"
		ev.Subject = changeValue(ev.Changes, "system", "kernel")
	case len(pkgs) > 0:
		ev.Class = "packages"
		if len(pkgs) == 1 && len(ev.Changes) <= 6 {
			// One package, plus the config files and unit state it dragged
			// along: name it. Any more and the package is not the whole story,
			// so it stays unnamed rather than mislabel the event.
			ev.Subject = pkgs[0]
		}
	case len(sections) == 1:
		// One section, no packages. The UI already has the section title on
		// every change, so it needs no new string for this case.
		ev.Class = "section"
		if len(ev.Changes) == 1 {
			ev.Subject = ev.Changes[0].Key
		}
	default:
		ev.Class = "mixed"
	}
}

// changeValue returns the new value of one change in the set, or "".
func changeValue(cs []Change, section, key string) string {
	for _, ch := range cs {
		if ch.Section == section && ch.Key == key {
			return ch.New
		}
	}
	return ""
}
