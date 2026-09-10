package native

import (
	"context"
	"errors"
	"time"

	"github.com/githubflyideas/deltascope/internal/pcp"
)

// ErrUnsupported is returned when the host has no /proc to read. Callers
// are expected to say so in the report rather than falling through to a
// verdict, which is the failure mode this whole package was written to
// remove: internal/diagnose used to answer "no regression detected" when
// it had measured nothing at all.
var ErrUnsupported = errors.New("native: metric collection requires Linux /proc")

// A Sample is one pass over /proc and /sys: raw kernel numbers, keyed by
// metric and instance, plus the wall-clock time of the pass. Nothing is
// converted or interpreted at this stage, because a counter's meaning
// depends on the sample before it.
type Sample struct {
	Taken time.Time
	vals  map[key]float64
}

type key struct {
	Metric   string
	Instance string
}

func newSample(t time.Time) Sample {
	return Sample{Taken: t, vals: map[key]float64{}}
}

func (s *Sample) set(metric, instance string, v float64) {
	if s.vals == nil {
		s.vals = map[key]float64{}
	}
	s.vals[key{metric, instance}] = v
}

// Value reports a raw reading. The second result distinguishes "this
// kernel does not publish it" from "it is zero", which is the distinction
// the whole package exists to preserve: mem.vmstat.oom_kill is absent
// below kernel 4.13, and reporting that as 0 would assert that nothing has
// been OOM-killed on a host where it cannot be known.
func (s *Sample) Value(metric, instance string) (float64, bool) {
	v, ok := s.vals[key{metric, instance}]
	return v, ok
}

// Len is the number of readings taken, for reporting how much was seen.
func (s *Sample) Len() int { return len(s.vals) }

// RowKey is the display name for a metric/instance pair, matching how PCP
// writes instanced metrics.
func RowKey(metric, instance string) string {
	if instance == "" {
		return metric
	}
	return metric + "[" + instance + "]"
}

// Window is what a run of samples yields: rows the reasoning engine can
// consume, plus the raw counter increments behind them.
//
// Increments exist because the rate is not always the honest number. The
// states are written against rates (that is what pmlogsummary produced, so
// that is what the thresholds assume), but "0.0006 OOM kills per second"
// is an unusable thing to show a person; the increment says "1". Keeping
// both means the threshold logic stays unchanged while the report can
// state what actually happened.
type Window struct {
	Rows       []pcp.DiffRow
	Increments map[string]float64
	Elapsed    time.Duration
	Samples    int
	// Start and End bracket the samples behind Rows. Elapsed is End minus
	// Start; both are kept because a report needs the span to describe the
	// measurement and the wall-clock times to say when it happened, and a
	// window read from a rolling sampler is not "just now" -- it may end
	// several seconds before the request that returned it.
	Start time.Time
	End   time.Time
	// BaselineSamples and BaselineElapsed describe the older window a Compare
	// judged this one against. Zero means nothing was compared, which the
	// report must say rather than leaving the reader to assume a comparison
	// happened: every DeltaPct in Rows is nil in that case.
	BaselineSamples int
	BaselineElapsed time.Duration
	BaselineStart   time.Time
	BaselineEnd     time.Time
}

// Supported reports whether this host can be sampled at all, by taking one
// pass and discarding it. Callers use it to decide up front whether to offer
// a /proc-backed feature, so a machine that can never serve one shows a
// disabled control with a reason instead of an enabled control that fails on
// click -- the same reason DetectPCP runs at startup rather than per request.
func Supported() bool {
	_, err := snapshot(time.Now())
	return err == nil
}

// Collect takes n samples interval apart and builds the window. n < 2
// yields gauges only: every counter needs two readings to mean anything,
// and a fabricated single-reading rate would be a made-up number.
//
// The context is honoured between samples, so a cancelled run returns what
// it already has rather than nothing -- a partial window still evaluates
// every gauge state.
func Collect(ctx context.Context, n int, interval time.Duration) (Window, error) {
	if n < 1 {
		n = 1
	}
	samples := make([]Sample, 0, n)
	for i := 0; i < n; i++ {
		if i > 0 {
			select {
			case <-ctx.Done():
				return Build(samples), ctx.Err()
			case <-time.After(interval):
			}
		}
		s, err := snapshot(time.Now())
		if err != nil {
			if len(samples) == 0 {
				return Window{}, err
			}
			return Build(samples), err
		}
		samples = append(samples, s)
	}
	return Build(samples), nil
}
