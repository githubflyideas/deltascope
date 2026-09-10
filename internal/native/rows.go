package native

import (
	"sort"

	"github.com/githubflyideas/deltascope/internal/pcp"
)

// Build turns raw samples into rows the reasoning engine can evaluate.
//
// Two rules govern everything here, and both are about refusing to invent
// numbers. First, a metric absent from the samples produces no row at all,
// so reasoning's condMatch fails its B clauses and the state is reported
// unevaluated rather than false. Second, a counter with fewer than two
// usable readings produces no row either -- a rate needs an interval, and
// the alternative is reporting the since-boot total as if it happened in
// the last ten seconds.
func Build(samples []Sample) Window {
	w := Window{Increments: map[string]float64{}, Samples: len(samples)}
	if len(samples) == 0 {
		return w
	}
	w.Elapsed = samples[len(samples)-1].Taken.Sub(samples[0].Taken)
	w.Start, w.End = samples[0].Taken, samples[len(samples)-1].Taken

	for _, k := range sortedKeys(samples) {
		sp, known := specs[k.Metric]
		if !known {
			continue
		}
		// The catalog gate, mirroring internal/pcp/diff.go's buildRows: a
		// metric with no MetricInfo has no label, polarity or floor, and the
		// reasoning engine has no state written against it.
		info, ok := pcp.Lookup(k.Metric)
		if !ok {
			continue
		}
		s := measure(k, sp, samples)
		if !s.ok {
			continue
		}
		row := pcp.DiffRow{
			Metric: k.Metric, Instance: k.Instance,
			Label: info.Label, Category: info.Category, Units: sp.Unit,
			// A is unknown: a single native window has no baseline to compare
			// against. VFlat rather than VWatch says "not compared", which is
			// what the engine's own test rows say; VWatch would read as a
			// judgement that was never made.
			Verdict: pcp.VFlat,
		}
		s.applyTo(&row)
		if sp.Kind == Counter {
			w.Increments[RowKey(k.Metric, k.Instance)] = s.inc
		}
		w.Rows = append(w.Rows, row)
	}
	return w
}

// Compare measures two runs by the same rules and judges the newer against
// the older. It is what makes the two comparative states answerable off
// /proc alone: state.mem.available_low and state.disk.filling_fast ask for a
// Verdict and a DeltaPct, and a single window has neither -- with no baseline
// they are reported unevaluated, which is honest but never actionable.
//
// Rows are the newer window's rows: a metric the kernel published then and
// not now is not a measurement of the machine as it is. A row whose metric
// has no usable baseline keeps A nil and the "not compared" VFlat from Build,
// so the comparative conditions decline it and Unevaluated says why, rather
// than a missing baseline being silently read as no change.
func Compare(before, after []Sample, thresholdPct float64) Window {
	w := Build(after)
	if len(before) == 0 {
		return w
	}
	w.BaselineSamples = len(before)
	w.BaselineElapsed = before[len(before)-1].Taken.Sub(before[0].Taken)
	w.BaselineStart, w.BaselineEnd = before[0].Taken, before[len(before)-1].Taken

	base := map[key]side{}
	for _, k := range sortedKeys(before) {
		sp, known := specs[k.Metric]
		if !known {
			continue
		}
		if s := measure(k, sp, before); s.ok {
			base[k] = s
		}
	}

	for i := range w.Rows {
		row := &w.Rows[i]
		s, have := base[key{row.Metric, row.Instance}]
		if !have {
			continue
		}
		info, ok := pcp.Lookup(row.Metric)
		if !ok {
			continue
		}
		s.applyBaselineTo(row)
		// The per-metric threshold override and the dual-significance floor
		// come from the same catalog the archive path reads, through the same
		// exported Judge: a native comparison that judged by its own rules
		// would disagree with the web UI about the same two windows.
		eff := thresholdPct
		if info.ThresholdPct > 0 {
			eff = info.ThresholdPct
		}
		row.DeltaPct, row.Exceeded, row.Verdict = pcp.Judge(row.A, row.B, info.Polarity, eff, info.MinAbs)
	}
	return w
}

// Rows is Build for callers that only need the engine input.
func Rows(samples []Sample) []pcp.DiffRow { return Build(samples).Rows }

// side is one window's reading of one metric: the value the thresholds are
// compared against, the spread behind it, and how many independent
// observations it rests on. Both windows of a Compare are measured through
// this one type, so a baseline can never be computed by different rules than
// the value it is judged against.
type side struct {
	val   float64
	min   float64
	max   float64
	count int
	inc   float64
	ok    bool
}

func (s side) applyTo(row *pcp.DiffRow) {
	v, mn, mx := s.val, s.min, s.max
	row.B = &v
	if s.count > 0 {
		row.BMin, row.BMax, row.BCount = &mn, &mx, s.count
	}
}

func (s side) applyBaselineTo(row *pcp.DiffRow) {
	v, mn, mx := s.val, s.min, s.max
	row.A = &v
	if s.count > 0 {
		row.AMin, row.AMax, row.ACount = &mn, &mx, s.count
	}
}

func measure(k key, sp spec, samples []Sample) side {
	switch sp.Kind {
	case Gauge:
		return gaugeSide(k, sp, samples)
	case Counter:
		return counterSide(k, sp, samples)
	}
	return side{}
}

// gaugeSide takes the newest reading and the spread over the run. count is
// the number of samples, which is what MinSamples means for a gauge: each
// sample is one independent observation, so a peak state guarded by
// MinSamples: 30 needs thirty passes and correctly declines on a two-sample
// check run.
func gaugeSide(k key, sp spec, samples []Sample) side {
	var out side
	for i := range samples {
		v, ok := samples[i].Value(k.Metric, k.Instance)
		if !ok {
			continue
		}
		v *= sp.Scale
		if out.count == 0 || v < out.min {
			out.min = v
		}
		if out.count == 0 || v > out.max {
			out.max = v
		}
		out.val = v
		out.count++
	}
	out.ok = out.count > 0
	return out
}

// counterSide converts a monotonic counter into the per-second rate the state
// thresholds are written against, and carries the raw increment alongside it.
//
// val is the rate over the whole run rather than the mean of the per-interval
// rates: the whole-run rate is time-weighted, so a long interval cannot be
// outvoted by several short ones. max is the fastest single interval, which
// is what the peak states want, and it is deliberately computed only from
// intervals that pass the monotonicity check below.
func counterSide(k key, sp spec, samples []Sample) side {
	type reading struct {
		t float64
		v float64
	}
	var rs []reading
	for i := range samples {
		v, ok := samples[i].Value(k.Metric, k.Instance)
		if !ok {
			continue
		}
		rs = append(rs, reading{samples[i].Taken.Sub(samples[0].Taken).Seconds(), v * sp.Scale})
	}
	if len(rs) < 2 {
		return side{}
	}
	first, last := rs[0], rs[len(rs)-1]
	span := last.t - first.t
	// A counter that went backwards over the run has been reset -- the
	// machine rebooted, the device was removed and re-added, or the kernel
	// wrapped a 32-bit field. There is no honest rate to report, and a
	// negative one would read as "better" on every polarity.
	if span <= 0 || last.v < first.v {
		return side{}
	}
	out := side{ok: true}
	out.inc = last.v - first.v
	out.val = out.inc / span

	for i := 0; i+1 < len(rs); i++ {
		dt := rs[i+1].t - rs[i].t
		if dt <= 0 || rs[i+1].v < rs[i].v {
			continue
		}
		r := (rs[i+1].v - rs[i].v) / dt
		if out.count == 0 || r > out.max {
			out.max = r
		}
		if out.count == 0 || r < out.min {
			out.min = r
		}
		out.count++
	}
	return out
}

// sortedKeys is the union of every sample's keys in a stable order,
// matching the category/metric ordering the PCP path produces so a native
// report and an archive report list the same metrics in the same place.
func sortedKeys(samples []Sample) []key {
	seen := map[key]bool{}
	var keys []key
	for i := range samples {
		for k := range samples[i].vals {
			if !seen[k] {
				seen[k] = true
				keys = append(keys, k)
			}
		}
	}
	catOrder := map[string]int{}
	for i, c := range pcp.Categories {
		catOrder[c] = i
	}
	cat := func(m string) int {
		if info, ok := pcp.Lookup(m); ok {
			return catOrder[info.Category]
		}
		return len(catOrder)
	}
	sort.Slice(keys, func(i, j int) bool {
		if ci, cj := cat(keys[i].Metric), cat(keys[j].Metric); ci != cj {
			return ci < cj
		}
		oi, oj := pcp.OrderIndex(keys[i].Metric), pcp.OrderIndex(keys[j].Metric)
		if oi != oj {
			return oi < oj
		}
		if keys[i].Metric != keys[j].Metric {
			return keys[i].Metric < keys[j].Metric
		}
		return keys[i].Instance < keys[j].Instance
	})
	return keys
}
