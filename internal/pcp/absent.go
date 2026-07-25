package pcp

import (
	"regexp"
	"sync"
)

// An archive only contains what pmlogger was configured to record. Asking
// for the rest produces the same "unknown metric name" warnings on every
// single query, which is noise the operator can do nothing about mid-
// session. Once a metric is known to be missing we stop requesting it:
// the warning appears once, subsequent reports are clean, and the queries
// get smaller.
//
// This is deliberately in-memory and per-process. A restart re-probes, so
// enabling fuller collection in pmlogger takes effect without having to
// clear any state.

var (
	absentMu sync.RWMutex
	absent   = map[string]bool{}
)

var absentMetricRe = regexp.MustCompile(`(?:traversal failed for|Invalid metric|Unknown metric name:?)\s+([A-Za-z][A-Za-z0-9._]*)`)

// NoteAbsent scans warning lines for metric names the archive does not
// contain and remembers them.
func NoteAbsent(warnings []string) (learned []string) {
	absentMu.Lock()
	defer absentMu.Unlock()
	for _, w := range warnings {
		for _, m := range absentMetricRe.FindAllStringSubmatch(w, -1) {
			name := m[1]
			if !absent[name] {
				absent[name] = true
				learned = append(learned, name)
			}
		}
	}
	return learned
}

// IsAbsent reports whether a metric is known to be missing.
func IsAbsent(metric string) bool {
	absentMu.RLock()
	defer absentMu.RUnlock()
	return absent[metric]
}

// AbsentMetrics returns the current known-missing set.
func AbsentMetrics() []string {
	absentMu.RLock()
	defer absentMu.RUnlock()
	out := make([]string, 0, len(absent))
	for m := range absent {
		out = append(out, m)
	}
	return out
}

// ResetAbsent clears the set (used by tests).
func ResetAbsent() {
	absentMu.Lock()
	defer absentMu.Unlock()
	absent = map[string]bool{}
}

// presentMetrics filters a request list down to metrics not already known
// to be missing, never returning an empty list (an empty request would
// fail outright rather than degrade).
func presentMetrics(metrics []string) []string {
	absentMu.RLock()
	defer absentMu.RUnlock()
	if len(absent) == 0 {
		return metrics
	}
	out := make([]string, 0, len(metrics))
	for _, m := range metrics {
		if !absent[m] {
			out = append(out, m)
		}
	}
	if len(out) == 0 {
		return metrics
	}
	return out
}
