package httpapi

import (
	"github.com/githubflyideas/deltascope/internal/pcp"
	"github.com/githubflyideas/deltascope/internal/reasoning"
)

// fillGaps lets the rolling /proc sampler answer for metrics the archive
// returned nothing about, and only those.
//
// The reason this is not "run both sources and merge" -- which main.go argues
// against on purpose -- is that the two sources are never asked the same
// question here. A pmlogger config decides which metric families land in the
// archive, and the common defaults omit whole files: TcpExt out of
// /proc/net/netstat and the socket-state counts out of /proc/net/tcp are both
// absent from a stock archive, which is why eight network states on a
// perfectly healthy host report as unmeasured rather than as quiet. Those
// eight are readable from /proc on any Linux box. The archive simply has no
// opinion about them, so there is no disagreement to arbitrate.
//
// The merge is keyed by metric name and is all-or-nothing:
//
//   - If the archive produced any row for a metric, the archive owns that
//     metric outright and proc is dropped for it. That includes a metric the
//     archive answered for only some instances -- one disk out of four, say.
//     Topping up the missing instances from a different source and a different
//     window would put rows from two clocks side by side under one metric, and
//     a SameInstance state would then be free to satisfy one condition from
//     the archive's sda and another from proc's sdb.
//   - If the archive produced no row at all, every proc row for that metric is
//     appended and the metric name is reported in filled, so the caller can
//     disclose which answers came from somewhere else.
//
// Order is preserved: archive rows first in their original order, then the
// appended proc rows in theirs. Nothing is sorted, because the state layer
// indexes by metric and does not care, while a stable order keeps the JSON
// diffable between requests.
func fillGaps(archive, proc []pcp.DiffRow) (merged []pcp.DiffRow, filled map[string]bool) {
	have := make(map[string]bool, len(archive))
	for _, r := range archive {
		have[r.Metric] = true
	}
	filled = map[string]bool{}
	merged = archive
	for _, r := range proc {
		if have[r.Metric] {
			continue
		}
		merged = append(merged, r)
		filled[r.Metric] = true
	}
	return merged, filled
}

// filledStates names the states whose verdict depends on at least one metric
// that came from /proc rather than the archive. A state is listed if any of
// its conditions reads a filled metric, not only if all of them do: the
// reader's question is "was this answer assembled from two windows", and one
// borrowed condition is enough for the answer to be yes.
//
// Returned as a set keyed by state ID because the view layer walks the catalog
// in order and needs O(1) lookup per row.
func filledStates(catalog []reasoning.State, filled map[string]bool) map[string]bool {
	if len(filled) == 0 {
		return nil
	}
	out := map[string]bool{}
	for _, st := range catalog {
		for _, c := range st.When {
			if filled[c.Metric] {
				out[st.ID] = true
				break
			}
		}
	}
	return out
}
