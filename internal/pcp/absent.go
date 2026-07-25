package pcp

import (
	"regexp"
	"sync"
)

// A metric the local pmlogger never recorded will fail on every single
// query, producing the same warning forever. Remembering which metrics an
// archive lacks lets us stop asking for them: the warning stops repeating,
// the command line gets shorter, and the report is no longer dominated by
// a list of things that were never going to be there.
//
// The cache is in-memory on purpose. A restart re-probes, so enabling
// wider collection in pmlogger takes effect without clearing any state.
type AbsentSet struct {
	mu      sync.RWMutex
	absent  map[string]bool
}

func NewAbsentSet() *AbsentSet {
	return &AbsentSet{absent: map[string]bool{}}
}

var pmnsFailRe = regexp.MustCompile(`(?:PMNS traversal failed for|Invalid metric|Unknown metric name:?)\s+([A-Za-z][A-Za-z0-9._]*)`)

// Learn records metric names that the archive does not contain, taken
// from pmlogsummary/pmrep stderr.
func (a *AbsentSet) Learn(warnings []string) int {
	if a == nil {
		return 0
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	n := 0
	for _, w := range warnings {
		if m := pmnsFailRe.FindStringSubmatch(w); m != nil {
			if !a.absent[m[1]] {
				a.absent[m[1]] = true
				n++
			}
		}
	}
	return n
}

// Filter drops metrics already known to be absent. It never returns an
// empty slice when the input was non-empty: if every metric is thought to
// be absent, we ask anyway, so a stale cache cannot leave a permanently
// blank report.
func (a *AbsentSet) Filter(metrics []string) (kept []string, dropped []string) {
	if a == nil {
		return metrics, nil
	}
	a.mu.RLock()
	defer a.mu.RUnlock()
	for _, m := range metrics {
		if a.absent[m] {
			dropped = append(dropped, m)
			continue
		}
		kept = append(kept, m)
	}
	if len(kept) == 0 {
		return metrics, nil
	}
	return kept, dropped
}

// List returns the known-absent metric names.
func (a *AbsentSet) List() []string {
	if a == nil {
		return nil
	}
	a.mu.RLock()
	defer a.mu.RUnlock()
	out := make([]string, 0, len(a.absent))
	for m := range a.absent {
		out = append(out, m)
	}
	return out
}

func (a *AbsentSet) Len() int {
	if a == nil {
		return 0
	}
	a.mu.RLock()
	defer a.mu.RUnlock()
	return len(a.absent)
}
