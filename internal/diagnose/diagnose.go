package diagnose

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/githubflyideas/deltascope/internal/pcp"
	"github.com/githubflyideas/deltascope/internal/state"
)

// Diagnosis is the one-screen answer: what is wrong, who caused it, what
// changed, and what to run next. It is assembled by correlating three
// independent engines rather than presenting them as three separate
// reports the reader has to join up themselves.
type Diagnosis struct {
	Window    Window   `json:"window"`
	Severity  string   `json:"severity"` // crit | warn | info | ok
	Headline  string   `json:"headline"`
	Culprit   string   `json:"culprit,omitempty"`
	Changed   string   `json:"changed,omitempty"`
	Next      []string `json:"next,omitempty"`
	Evidence  []string `json:"evidence,omitempty"`

	// Full detail from each engine, for the folded-out sections.
	Triage    []pcp.TriageBlock `json:"triage,omitempty"`
	Findings  []pcp.Finding     `json:"findings,omitempty"`
	Processes []state.ProcRow   `json:"processes,omitempty"`
	Changes   []ChangeSummary   `json:"changes,omitempty"`

	Notes []string `json:"notes,omitempty"`
}

type Window struct {
	AStart time.Time `json:"a_start"`
	AEnd   time.Time `json:"a_end"`
	BStart time.Time `json:"b_start"`
	BEnd   time.Time `json:"b_end"`
	Label  string    `json:"label"`
}

type ChangeSummary struct {
	Section string `json:"section"`
	Title   string `json:"title"`
	Key     string `json:"key"`
	Kind    string `json:"kind"`
	Old     string `json:"old,omitempty"`
	New     string `json:"new,omitempty"`
}

// Deps is what the chain needs to run. Kept as an interface-ish struct so
// tests can supply fakes without a live PCP or database.
type Deps struct {
	Runner     pcp.Runner
	Archive    string
	StateStore *state.Store
	Threshold  float64
	Absent     *pcp.AbsentSet
}

// Run picks windows automatically, runs the three engines concurrently,
// and correlates the results.
func Run(ctx context.Context, d Deps) (*Diagnosis, error) {
	w := pickWindow(time.Now())
	threshold := d.Threshold
	if threshold <= 0 {
		threshold = 15
	}

	out := &Diagnosis{Window: w}

	var (
		wg       sync.WaitGroup
		mu       sync.Mutex
		metricRep *pcp.DiffReport
		procDiff  state.ProcDiff
		stateDiff state.Diff
	)
	note := func(s string) {
		mu.Lock()
		out.Notes = append(out.Notes, s)
		mu.Unlock()
	}

	wg.Add(3)

	// 1. metric regression from PCP archives
	go func() {
		defer wg.Done()
		rep, err := pcp.Compare(ctx, d.Runner, d.Archive, pcp.Windows{
			AStart: w.AStart, AEnd: w.AEnd, BStart: w.BStart, BEnd: w.BEnd,
			ThresholdPct: threshold, Absent: d.Absent,
		})
		if err != nil {
			note("Performance metrics unavailable: " + firstLine(err.Error()))
			return
		}
		mu.Lock()
		metricRep = rep
		mu.Unlock()
	}()

	// 2. per-process accounting from snapshots
	go func() {
		defer wg.Done()
		if d.StateStore == nil {
			return
		}
		n, err := d.StateStore.Count()
		if err != nil || n < 2 {
			note("Process accounting needs at least two snapshots; the server captures every 10 minutes.")
			return
		}
		tol := 30 * time.Minute
		b1, e1 := d.StateStore.Nearest(w.BStart, tol)
		b2, e2 := d.StateStore.Nearest(w.BEnd, tol)
		if e1 != nil || e2 != nil {
			note("No snapshots cover the compare window yet.")
			return
		}
		a1, e3 := d.StateStore.Nearest(w.AStart, tol)
		a2, e4 := d.StateStore.Nearest(w.AEnd, tol)
		if e3 != nil || e4 != nil {
			a1, a2 = b1, b1
			note("No snapshots cover the baseline window; process figures are for the compare window only.")
		}
		pd := state.CompareProcesses(a1, a2, b1, b2, 20, 1, 10240)
		mu.Lock()
		procDiff = pd
		mu.Unlock()
	}()

	// 3. configuration / environment changes from snapshots
	go func() {
		defer wg.Done()
		if d.StateStore == nil {
			return
		}
		before, err := d.StateStore.Nearest(w.AEnd, 2*time.Hour)
		if err != nil {
			return
		}
		after, err := d.StateStore.Latest()
		if err != nil {
			return
		}
		sd := state.Compare(before, after)
		mu.Lock()
		stateDiff = sd
		mu.Unlock()
	}()

	wg.Wait()

	if metricRep != nil {
		out.Triage = metricRep.Triage
		out.Findings = metricRep.Findings
	}
	out.Processes = topProcesses(procDiff.Rows, 12)
	out.Changes = summarizeChanges(stateDiff, 40)

	synthesize(out, metricRep, procDiff, stateDiff)
	return out, nil
}

// pickWindow defaults to "the last full hour vs the same hour yesterday",
// which is the comparison people actually reach for. Anchored to the hour
// so repeated runs are stable and comparable.
func pickWindow(now time.Time) Window {
	bEnd := now.Truncate(time.Hour)
	if now.Sub(bEnd) < 10*time.Minute {
		// too early in the hour to have useful data; step back one
		bEnd = bEnd.Add(-time.Hour)
	}
	bStart := bEnd.Add(-time.Hour)
	return Window{
		AStart: bStart.AddDate(0, 0, -1),
		AEnd:   bEnd.AddDate(0, 0, -1),
		BStart: bStart,
		BEnd:   bEnd,
		Label:  "last hour vs the same hour yesterday",
	}
}

// synthesize is the correlation step: it decides the single headline and
// attaches the culprit process and the related configuration change.
func synthesize(out *Diagnosis, rep *pcp.DiffReport, pd state.ProcDiff, sd state.Diff) {
	// Descending priority: a rule-engine conclusion beats a bare resource
	// signal, because a rule already knows what the combination means.
	var crit, warn *pcp.Finding
	for i := range out.Findings {
		f := &out.Findings[i]
		if f.Severity == "crit" && crit == nil {
			crit = f
		}
		if f.Severity == "warn" && warn == nil {
			warn = f
		}
	}

	worstBlock := worstTriage(out.Triage)

	switch {
	case crit != nil:
		out.Severity, out.Headline = "crit", crit.Conclusion
		out.Evidence, out.Next = crit.Evidence, crit.Next
	case worstBlock != nil && worstBlock.Status == pcp.TriageBad:
		out.Severity = "crit"
		out.Headline = worstBlock.Label + " is degraded: " + worstBlock.Headline
	case warn != nil:
		out.Severity, out.Headline = "warn", warn.Conclusion
		out.Evidence, out.Next = warn.Evidence, warn.Next
	case worstBlock != nil && worstBlock.Status == pcp.TriageWarn:
		out.Severity = "warn"
		out.Headline = worstBlock.Label + " needs watching: " + worstBlock.Headline
	case sd.Total > 0:
		out.Severity = "info"
		out.Headline = fmt.Sprintf("No performance regression, but %d configuration change(s) were detected", sd.Total)
	default:
		out.Severity = "ok"
		out.Headline = "No regression and no configuration changes detected"
	}

	// Culprit: attribute the sick resource to a process where we can.
	// Only CPU and memory have per-process attribution -- claiming a
	// culprit for disk or network would be a guess, so we stay silent.
	if worstBlock != nil && worstBlock.Status != pcp.TriageOK {
		switch worstBlock.Key {
		case "cpu":
			out.Culprit = culpritByCPU(pd.Rows)
		case "mem":
			out.Culprit = culpritByRSS(pd.Rows)
		}
	}
	if out.Culprit == "" && out.Severity != "ok" {
		if c := culpritByCPU(pd.Rows); c != "" {
			out.Culprit = c
		} else if c := culpritByRSS(pd.Rows); c != "" {
			out.Culprit = c
		}
	}

	// Related change: only surface a change that plausibly relates to the
	// sick resource. When a specific resource is degraded, an unrelated
	// change is noise dressed up as a cause -- a firewall rule edit has
	// nothing to do with disk queue depth, and pairing them invites a
	// wrong conclusion. Fall back to "any change" only when no resource
	// is implicated, where the change itself is the story.
	if worstBlock != nil && worstBlock.Status != pcp.TriageOK {
		out.Changed = relatedChange(sd, worstBlock.Key)
	} else {
		out.Changed = relatedChange(sd, "")
	}
}

func worstTriage(blocks []pcp.TriageBlock) *pcp.TriageBlock {
	rank := map[pcp.TriageStatus]int{pcp.TriageBad: 0, pcp.TriageWarn: 1, pcp.TriageOK: 2}
	var best *pcp.TriageBlock
	for i := range blocks {
		b := &blocks[i]
		if best == nil || rank[b.Status] < rank[best.Status] {
			best = b
		}
	}
	if best != nil && best.Status == pcp.TriageOK {
		return best // caller checks Status
	}
	return best
}

func culpritByCPU(rows []state.ProcRow) string {
	best, bestVal := "", 0.0
	for _, r := range rows {
		if r.CPUPctB == nil {
			continue
		}
		// prefer a large increase; fall back to sheer size
		v := *r.CPUPctB
		if r.CPUDelta != nil && *r.CPUDelta > 20 {
			v += *r.CPUDelta
		}
		if v > bestVal && *r.CPUPctB >= 5 {
			best, bestVal = r.Name, v
		}
	}
	if best == "" {
		return ""
	}
	for _, r := range rows {
		if r.Name == best {
			s := fmt.Sprintf("%s at %.0f%% of a core", best, *r.CPUPctB)
			if r.CPUDelta != nil {
				s += fmt.Sprintf(" (%+.0f%%)", *r.CPUDelta)
			}
			if r.Restarted {
				s += ", restarted in this window"
			}
			return s
		}
	}
	return best
}

func culpritByRSS(rows []state.ProcRow) string {
	best, bestVal := "", 0.0
	for _, r := range rows {
		if r.RSSDelta == nil || r.RSSKBB == nil {
			continue
		}
		if *r.RSSDelta > bestVal && *r.RSSDelta > 20 {
			best, bestVal = r.Name, *r.RSSDelta
		}
	}
	if best == "" {
		return ""
	}
	for _, r := range rows {
		if r.Name == best {
			s := fmt.Sprintf("%s memory %+.0f%% (now %s)", best, *r.RSSDelta, humanKB(*r.RSSKBB))
			if r.Restarted {
				s += ", restarted in this window"
			}
			return s
		}
	}
	return best
}

// changeRelevance maps a resource block to the state keys worth blaming.
var changeRelevance = map[string][]string{
	"cpu":  {"kernel.sched", "kernel.pid_max", "cpu"},
	"mem":  {"vm.", "swap", "mem", "hugepage", "oom"},
	"disk": {"mount:", "fstab", "blk:", "mdraid", "vm.dirty"},
	"net":  {"net.", "route:", "addr:", "resolv", "iptables", "nftables", "somaxconn", "backlog"},
}

func relatedChange(sd state.Diff, resource string) string {
	keys := changeRelevance[resource]
	var fallback string
	for _, sec := range sd.Sections {
		for _, ch := range sec.Changes {
			desc := describeChange(ch)
			if fallback == "" {
				fallback = desc
			}
			if resource == "" {
				return desc
			}
			lower := strings.ToLower(ch.Key)
			for _, k := range keys {
				if strings.Contains(lower, strings.ToLower(k)) {
					return desc
				}
			}
		}
	}
	if resource == "" {
		return fallback
	}
	return ""
}

func describeChange(ch state.Change) string {
	switch ch.Kind {
	case state.Added:
		return ch.Key + " added (" + trunc(ch.New, 40) + ")"
	case state.Removed:
		return ch.Key + " removed"
	default:
		return fmt.Sprintf("%s %s -> %s", ch.Key, trunc(ch.Old, 24), trunc(ch.New, 24))
	}
}

func summarizeChanges(sd state.Diff, limit int) []ChangeSummary {
	var out []ChangeSummary
	for _, sec := range sd.Sections {
		for _, ch := range sec.Changes {
			out = append(out, ChangeSummary{
				Section: sec.Name, Title: sec.Title, Key: ch.Key,
				Kind: string(ch.Kind), Old: ch.Old, New: ch.New,
			})
			if len(out) >= limit {
				return out
			}
		}
	}
	return out
}

func topProcesses(rows []state.ProcRow, limit int) []state.ProcRow {
	active := make([]state.ProcRow, 0, len(rows))
	for _, r := range rows {
		if r.Verdict != state.PVFlat {
			active = append(active, r)
		}
	}
	if len(active) == 0 {
		// nothing changed: show the heaviest few so the tab is never blank
		sorted := append([]state.ProcRow(nil), rows...)
		sort.Slice(sorted, func(i, j int) bool {
			return deref(sorted[i].RSSKBB) > deref(sorted[j].RSSKBB)
		})
		if len(sorted) > limit {
			sorted = sorted[:limit]
		}
		return sorted
	}
	if len(active) > limit {
		active = active[:limit]
	}
	return active
}

func deref(f *float64) float64 {
	if f == nil {
		return 0
	}
	return *f
}

func humanKB(kb float64) string {
	switch {
	case kb >= 1048576:
		return fmt.Sprintf("%.1fG", kb/1048576)
	case kb >= 1024:
		return fmt.Sprintf("%.0fM", kb/1024)
	}
	return fmt.Sprintf("%.0fK", kb)
}

func trunc(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n-1] + "…"
}

func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	if len(s) > 160 {
		return s[:159] + "…"
	}
	return s
}

