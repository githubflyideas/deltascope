package native

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"
)

// A Sampler keeps a rolling history of /proc passes so a request arriving at
// an arbitrary moment can be answered with two windows instead of one.
//
// This is what the `check` subcommand cannot be. check samples for ten
// seconds because someone is waiting at a terminal, and ten seconds is too
// short for two things: the ten states guarded by MinSamples: 30 decline
// outright, and the two comparative states have no baseline to be judged
// against. A long-lived server has no such excuse -- it can be sampling
// already when the request arrives, which is the only way those twelve
// states are ever answerable without PCP.
//
// The history is a fixed-size ring, so memory is bounded by construction
// rather than by remembering to trim. A measured sample of 103 readings
// costs about 8 KiB; a large host publishes several times that many
// instances, so the default 64-deep ring is single-digit megabytes and does
// not grow with uptime.
type Sampler struct {
	interval time.Duration

	// take is the sample source. Only tests replace it: snapshot is Linux
	// only, and the ring logic here is the part that must be verified on the
	// machine it is written on rather than the one it ships to.
	take func(time.Time) (Sample, error)

	mu      sync.Mutex
	buf     []Sample
	next    int
	full    bool
	started time.Time
	lastErr error
	// stopped records a permanent failure. ErrUnsupported does not become
	// true on a retry -- a host with no /proc will not grow one -- so the
	// loop gives up rather than waking every interval forever to fail.
	stopped bool
}

// Defaults chosen against the states they have to satisfy: 64 samples two
// seconds apart split into two 62-second windows of 32 samples each, which
// is the first pair that clears MinSamples: 30 on both the gauge peaks and
// the counter bursts. Sampling faster would not help -- the thresholds are
// per-second rates -- and keeping more would only widen the baseline into
// history the reasoning chain is deliberately not about.
const (
	DefaultSampleInterval = 2 * time.Second
	DefaultKeepSamples    = 64
)

// NewSampler returns a sampler that has not started. keep is the total
// number of passes retained across both windows; below four there is
// nothing to split, so that is the floor.
func NewSampler(interval time.Duration, keep int) *Sampler {
	if interval <= 0 {
		interval = DefaultSampleInterval
	}
	if keep < 4 {
		keep = 4
	}
	return &Sampler{
		interval: interval,
		take:     snapshot,
		buf:      make([]Sample, keep),
	}
}

// Interval is how often a pass is taken, for callers that describe the
// window to a reader.
func (s *Sampler) Interval() time.Duration { return s.interval }

// Run samples until the context is cancelled. It blocks, so callers start it
// in a goroutine; it returns early only when the host turns out to have no
// /proc at all, which no amount of waiting will change.
//
// The first pass is taken immediately rather than after one interval, so a
// server that is asked for a diagnosis two seconds after boot has one
// reading rather than none -- one reading answers no state, but it makes the
// "not enough samples yet" message able to say how far along it is.
func (s *Sampler) Run(ctx context.Context) {
	s.mu.Lock()
	s.started = time.Now()
	s.mu.Unlock()

	if !s.add(time.Now()) {
		return
	}
	t := time.NewTicker(s.interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			if !s.add(time.Now()) {
				return
			}
		}
	}
}

// add takes one pass and reports whether sampling is worth continuing.
//
// A failed pass is recorded and skipped, not fatal: /proc/pressure can be
// masked in a container, a disk can be unplugged mid-run, and none of that
// invalidates the readings already held. Only ErrUnsupported ends the loop.
func (s *Sampler) add(now time.Time) bool {
	smp, err := s.take(now)

	s.mu.Lock()
	defer s.mu.Unlock()
	if err != nil {
		s.lastErr = err
		if errors.Is(err, ErrUnsupported) {
			s.stopped = true
		}
		return !s.stopped
	}
	s.lastErr = nil
	// Overwriting the oldest slot with a freshly built Sample, never mutating
	// one in place, is what lets Window hand out a shallow copy: a caller
	// still reading a slice from before the wrap holds maps this loop will
	// not touch again.
	s.buf[s.next] = smp
	s.next = (s.next + 1) % len(s.buf)
	if s.next == 0 {
		s.full = true
	}
	return true
}

// Window judges the recent half of the history against the half before it.
//
// The split is what makes the two comparative states answerable: with a
// baseline, state.mem.available_low is a question about a number that is
// falling, and Compare gives it the Verdict and DeltaPct it asks for. The
// error is returned rather than an empty Window on purpose -- a caller that
// received Window{} could render it as a machine with no problems, which is
// the exact confusion this package exists to prevent.
func (s *Sampler) Window(thresholdPct float64) (Window, error) {
	s.mu.Lock()
	held := s.ordered()
	lastErr, stopped, started := s.lastErr, s.stopped, s.started
	s.mu.Unlock()

	if len(held) < 2 {
		switch {
		case stopped:
			return Window{}, ErrUnsupported
		case lastErr != nil:
			return Window{}, lastErr
		case started.IsZero():
			return Window{}, errors.New("native: the sampler has not been started")
		}
		return Window{}, fmt.Errorf("native: %d sample(s) %s after start; two are needed before any rate exists",
			len(held), time.Since(started).Round(time.Second))
	}
	// Under four there is nothing worth splitting: a one-sample window has no
	// counter rates at all, so halving two or three samples would trade every
	// rate in the report for a baseline made of a single reading. Report the
	// whole history as one window instead and let the comparative states
	// decline -- they will say "no baseline", which is true and temporary.
	if len(held) < 4 {
		return Build(held), nil
	}
	mid := len(held) / 2
	return Compare(held[:mid], held[mid:], thresholdPct), nil
}

// ordered returns the history oldest first. The returned slice is fresh but
// the Samples in it are shared with the ring; see add for why that is safe.
func (s *Sampler) ordered() []Sample {
	if !s.full {
		return append([]Sample(nil), s.buf[:s.next]...)
	}
	out := make([]Sample, 0, len(s.buf))
	out = append(out, s.buf[s.next:]...)
	out = append(out, s.buf[:s.next]...)
	return out
}
