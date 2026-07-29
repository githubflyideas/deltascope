package diagnose

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/githubflyideas/deltascope/internal/pcp"
	"github.com/githubflyideas/deltascope/internal/reasoning"
	"github.com/githubflyideas/deltascope/internal/state"
)

// Diagnosis is the one-screen answer: what is wrong, who caused it, what
// changed, and what to run next. It is assembled by correlating three
// independent engines rather than presenting them as three separate
// reports the reader has to join up themselves.
type Diagnosis struct {
	Window   Window   `json:"window"`
	Severity string   `json:"severity"` // crit | warn | info | ok
	Headline string   `json:"headline"`
	Culprit  string   `json:"culprit,omitempty"`
	Changed  string   `json:"changed,omitempty"`
	Next     []string `json:"next,omitempty"`
	Evidence []string `json:"evidence,omitempty"`

	// Full detail from each engine, for the folded-out sections.
	Triage    []pcp.TriageBlock `json:"triage,omitempty"`
	Findings  []pcp.Finding     `json:"findings,omitempty"`
	Processes []state.ProcRow   `json:"processes,omitempty"`
	Changes   []ChangeSummary   `json:"changes,omitempty"`
	// Reasoning carries the per-core / named-state diagnoses that the
	// aggregate metric engine cannot see. It also drives the CPU triage
	// block's escalation, so the one-click view and the reasoning tab agree.
	Reasoning []reasoning.Result `json:"reasoning,omitempty"`

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
}

// Run picks windows automatically, runs the three engines concurrently,
// and correlates the results.
func Run(ctx context.Context, d Deps) (*Diagnosis, error) {
	w := PickWindow(time.Now())
	threshold := d.Threshold
	if threshold <= 0 {
		threshold = 15
	}

	out := &Diagnosis{Window: w}

	var (
		wg        sync.WaitGroup
		mu        sync.Mutex
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
			ThresholdPct: threshold,
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
		// The triage blocks come straight from the metric engine, which
		// only looks at whole-machine aggregates. A single pegged core is
		// invisible there by construction (one core is 1000 ms/s regardless
		// of machine size -- see internal/reasoning). Fold the per-core
		// reasoning states back into the CPU block so the one-click page
		// agrees with the reasoning chain instead of showing a green light
		// next to a diagnosis that says a core is saturated.
		out.Reasoning = reasoning.Diagnose(reasoning.Diagnoses, reasoning.Evaluate(reasoning.States, metricRep.Rows))
		escalateCPUFromReasoning(out)
	}
	out.Processes = topProcesses(procDiff.Rows, 12)
	out.Changes = summarizeChanges(stateDiff, 40)

	synthesize(out, metricRep, procDiff, stateDiff)
	return out, nil
}

// cpuReasoningHeadline maps a reasoning diagnosis ID to the short phrase
// the CPU triage block should show when the aggregate metrics missed it.
// Only the CPU diagnoses that the aggregate view is structurally blind to
// are listed; anything the metric engine can already see is left to it.
var cpuReasoningHeadline = map[string]struct {
	status   pcp.TriageStatus
	headline string
}{
	"diagnosis.single_core_saturated":       {pcp.TriageBad, "one core saturated (serialized workload)"},
	"diagnosis.cpu_capacity_exhausted":      {pcp.TriageBad, "every core saturated"},
	"diagnosis.intermittent_cpu_saturation": {pcp.TriageWarn, "a core hit its limit intermittently"},
}

// escalateCPUFromReasoning raises the CPU triage block when a per-core
// reasoning diagnosis fired that the aggregate metric engine could not see.
// It only ever raises severity, never lowers it: if the metric engine
// already flagged CPU red for its own reasons, that stands.
func escalateCPUFromReasoning(out *Diagnosis) {
	rank := map[pcp.TriageStatus]int{pcp.TriageOK: 0, pcp.TriageWarn: 1, pcp.TriageBad: 2}
	for _, r := range out.Reasoning {
		info, ok := cpuReasoningHeadline[r.ID]
		if !ok {
			continue
		}
		for i := range out.Triage {
			b := &out.Triage[i]
			if b.Key != "cpu" {
				continue
			}
			if rank[info.status] > rank[b.Status] {
				b.Status = info.status
				b.Headline = info.headline
			}
		}
	}
}

// PickWindow defaults to "the trailing hour up to now vs the same clock
// span yesterday" -- the comparison people reach for when something is
// wrong right now.
//
// It used to anchor B to the last COMPLETE clock hour (truncate to the
// hour, step back). Repeated runs were then byte-identical, but at the cost
// of never looking at the hour in progress -- which is exactly where a
// problem that just started lives. A process that began pegging a core at
// 18:05 stayed invisible until 19:00, because the window sat at 17:00-18:00
// no matter how many times it was re-run. A one-click "what is wrong now"
// is defeated if "now" is excluded by construction, and that is the single
// most common thing the tool is pointed at.
//
// So B now ends at now and runs back one hour; A is the identical clock
// span exactly one day earlier, so the day-over-day baseline still lines up
// (18:54 today vs 18:54 yesterday). The tradeoff is deliberate: two runs a
// minute apart no longer produce identical windows. For a live-triage tool
// freshness beats reproducibility, and anyone who needs a frozen window can
// set one explicitly in the manual compare view.
//
// PickWindow is exported so both entry points -- one-click diagnosis and
// the reasoning chain -- select the identical window and stay directly
// comparable against the same data.
func PickWindow(now time.Time) Window {
	bEnd := now
	bStart := bEnd.Add(-time.Hour)
	return Window{
		AStart: bStart.AddDate(0, 0, -1),
		AEnd:   bEnd.AddDate(0, 0, -1),
		BStart: bStart,
		BEnd:   bEnd,
		Label:  "the last hour up to now vs the same hour yesterday",
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

	// A per-core reasoning diagnosis outranks a bare triage colour but not
	// a full rule-engine conclusion: the reasoning chain knows "one core is
	// pegged and the rest are idle" is a serialized workload, which is a
	// real conclusion, whereas the triage block only knows a colour. It
	// sits just below crit findings for the same reason those do -- a rule
	// that fired already understands the whole combination.
	var reasonCrit, reasonWarn *reasoning.Result
	for i := range out.Reasoning {
		r := &out.Reasoning[i]
		if _, relevant := cpuReasoningHeadline[r.ID]; !relevant {
			continue
		}
		if r.Severity == "crit" && reasonCrit == nil {
			reasonCrit = r
		}
		if r.Severity == "warn" && reasonWarn == nil {
			reasonWarn = r
		}
	}

	switch {
	case crit != nil:
		out.Severity, out.Headline = "crit", crit.Conclusion
		out.Evidence, out.Next = crit.Evidence, crit.Next
	case reasonCrit != nil:
		out.Severity, out.Headline = "crit", reasonCrit.Conclusion
		out.Evidence, out.Next = reasonCrit.Evidence, reasonCrit.Next
	case worstBlock != nil && worstBlock.Status == pcp.TriageBad:
		out.Severity = "crit"
		out.Headline = worstBlock.Label + " is degraded: " + worstBlock.Headline
	case warn != nil:
		out.Severity, out.Headline = "warn", warn.Conclusion
		out.Evidence, out.Next = warn.Evidence, warn.Next
	case reasonWarn != nil:
		out.Severity, out.Headline = "warn", reasonWarn.Conclusion
		out.Evidence, out.Next = reasonWarn.Evidence, reasonWarn.Next
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

// culpritCPUScore ranks candidates for "which process is responsible".
//
// The previous scoring added a delta percentage to a percent-of-a-core:
//
//	v := *r.CPUPctB      // percent of one core
//	v += *r.CPUDelta     // percent CHANGE -- a different quantity entirely
//
// Those are not the same unit and their sum means nothing. On a live host it
// picked gnome-shell at 5.3% of a core (+14799%) over firefox at 17.3%
// (+6518%) -- naming the wrong process, and naming it confidently.
//
// What "responsible" should mean is how much CPU this process is consuming
// now. That is CPUPctB, full stop. A large increase is corroborating evidence
// that the consumption is new, so it breaks ties and adds a bounded nudge,
// but it cannot outweigh the consumption itself: a process that went from
// 0.1% to 1% of a core has a spectacular ratio and is not the reason the
// machine is busy.
func culpritCPUScore(r state.ProcRow) float64 {
	if r.CPUPctB == nil {
		return 0
	}
	score := *r.CPUPctB
	// A confirmed increase is worth at most a 50% bonus on the process's own
	// consumption: enough to prefer a newly-hot process over an equally-large
	// one that was always that size, never enough to promote a small consumer.
	if r.CPUDelta != nil && *r.CPUDelta > 20 {
		score *= 1.5
	} else if r.FromZero {
		// No ratio exists, but the row rose from an idle baseline -- the same
		// evidence in the form the data actually took.
		score *= 1.5
	}
	return score
}

func culpritByCPU(rows []state.ProcRow) string {
	best, bestVal := "", 0.0
	for _, r := range rows {
		if r.CPUPctB == nil || *r.CPUPctB < 5 {
			continue
		}
		if v := culpritCPUScore(r); v > bestVal {
			best, bestVal = r.Name, v
		}
	}
	if best == "" {
		return ""
	}
	for _, r := range rows {
		if r.Name == best {
			pct := fmt.Sprintf("%.0f%%", *r.CPUPctB)
			if r.CPUApproxB {
				// Lifetime average, not a measured windowed rate. Saying so
				// costs one character and stops a reader over-trusting it.
				pct = "~" + pct
			}
			s := fmt.Sprintf("%s at %s of a core", best, pct)
			switch {
			case r.CPUDelta != nil:
				s += fmt.Sprintf(" (%+.0f%%)", *r.CPUDelta)
			case r.FromZero:
				s += " (was idle)"
			}
			if r.Restarted {
				s += ", restarted in this window"
			}
			return s
		}
	}
	return best
}

// culpritByRSS ranks by growth rather than by size, because for memory the
// question is usually "what grew" -- the largest process on a host is often
// supposed to be the largest (a database given most of the RAM), so naming it
// every time would be noise.
//
// Growth is weighted by the absolute footprint so that a process that grew
// 40% to 4 GB outranks one that grew 400% to 40 MB. Ranking on the percentage
// alone had the same unit-blindness as the CPU path: it made a rounding error
// on a small process look more important than gigabytes.
func culpritByRSS(rows []state.ProcRow) string {
	best, bestVal := "", 0.0
	for _, r := range rows {
		if r.RSSKBB == nil {
			continue
		}
		var growth float64
		switch {
		case r.RSSDelta != nil && *r.RSSDelta > 20:
			growth = *r.RSSDelta
		case r.FromZero && r.RSSKBA != nil && *r.RSSKBB > *r.RSSKBA:
			// Rose from a footprint too small to form a ratio against.
			// Treated as large-but-bounded growth so it competes without
			// automatically winning.
			growth = 100
		default:
			continue
		}
		// MB of current footprint scales the growth, keeping both halves of
		// "grew a lot" and "is a lot" in the score.
		if v := growth * (*r.RSSKBB / 1024); v > bestVal {
			best, bestVal = r.Name, v
		}
	}
	if best == "" {
		return ""
	}
	for _, r := range rows {
		if r.Name == best {
			var s string
			if r.RSSDelta != nil {
				s = fmt.Sprintf("%s memory %+.0f%% (now %s)", best, *r.RSSDelta, humanKB(*r.RSSKBB))
			} else {
				s = fmt.Sprintf("%s memory grew from idle to %s", best, humanKB(*r.RSSKBB))
			}
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
