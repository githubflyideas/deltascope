package native

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/githubflyideas/deltascope/internal/reasoning"
)

// A Sampler exists for the twelve states a one-shot run cannot answer: the
// ten guarded by MinSamples: 30 and the two that need a baseline. These tests
// pin that a running sampler answers them, that a sampler which cannot
// measure the host says so instead of returning an empty window, and that the
// ring hands back history in the right order -- the last of which is the only
// place in this package where an off-by-one would silently produce a
// plausible-looking wrong answer rather than a compile error.

// procAt returns a fake /proc pass: a monotonic context-switch counter
// advancing at ctxPerSec and MemAvailable as given. Two readings is the
// minimum that exercises both a counter burst and a comparative gauge.
func procAt(origin time.Time, ctxPerSec, availKB float64) func(time.Time) (Sample, error) {
	return func(t time.Time) (Sample, error) {
		s := newSample(t)
		s.set("kernel.all.pswitch", "", ctxPerSec*t.Sub(origin).Seconds())
		s.set("mem.util.available", "", availKB)
		return s, nil
	}
}

// feed drives add directly with synthetic timestamps. Run's real clock would
// make every rate in these tests depend on scheduler jitter; the ring and the
// split are what is under test, and both are pure.
func feed(t *testing.T, s *Sampler, start time.Time, n int, interval time.Duration) {
	t.Helper()
	for i := 0; i < n; i++ {
		if !s.add(start.Add(time.Duration(i) * interval)) {
			t.Fatalf("add stopped sampling at pass %d", i)
		}
	}
}

// The headline claim: sampling in the background is what turns a
// MinSamples-guarded burst state from unevaluated into answered. The same
// numbers on a ten-second check run decline, and that is the gap serve was
// blind to.
func TestSamplerAnswersTheMinSamplesStates(t *testing.T) {
	s := NewSampler(2*time.Second, DefaultKeepSamples)
	// 100k switches per second on a 2-core box is 50k per CPU, exactly the
	// BMaxGtePerCPU threshold state.cpu.context_switch_storm is written at.
	s.take = procAt(zeroTime, 120000, 8000000)
	feed(t, s, zeroTime, DefaultKeepSamples, 2*time.Second)

	w, err := s.Window(10)
	if err != nil {
		t.Fatalf("a full ring must yield a window: %v", err)
	}
	if w.Samples != DefaultKeepSamples/2 || w.BaselineSamples != DefaultKeepSamples/2 {
		t.Fatalf("split = %d current / %d baseline, want an even halving of %d",
			w.Samples, w.BaselineSamples, DefaultKeepSamples)
	}
	row, ok := rowFor(w, "kernel.all.pswitch")
	if !ok {
		t.Fatal("kernel.all.pswitch produced no row")
	}
	// 31 intervals across 32 samples: the point of the ring is that this
	// clears the guard where a short check run cannot.
	if row.BCount < 30 {
		t.Errorf("BCount = %d, want at least the 30 the burst states require", row.BCount)
	}

	machine := reasoning.Machine{NCPU: 2}
	if _, on := reasoning.EvaluateOn(reasoning.States, w.Rows, machine)["state.cpu.context_switch_storm"]; !on {
		t.Error("state.cpu.context_switch_storm must fire: 120k/s on 2 cores is over the per-CPU threshold")
	}
	if reason, gap := reasoning.Unevaluated(reasoning.States, w.Rows)["state.cpu.context_switch_storm"]; gap {
		t.Errorf("the state was answered but is still reported unevaluated: %s", reason)
	}
}

// The second half of what the ring buys: a baseline, so the comparative
// states become questions about a number that is falling rather than
// questions with no answer.
func TestSamplerMakesTheComparativeStateAnswerable(t *testing.T) {
	s := NewSampler(2*time.Second, 8)
	s.take = procAt(zeroTime, 1000, 8000000)
	feed(t, s, zeroTime, 4, 2*time.Second)
	// Memory halves and then halves again while the ring rolls forward.
	s.take = procAt(zeroTime, 1000, 2000000)
	feed(t, s, zeroTime.Add(8*time.Second), 4, 2*time.Second)

	w, err := s.Window(10)
	if err != nil {
		t.Fatalf("Window: %v", err)
	}
	row, ok := rowFor(w, "mem.util.available")
	if !ok {
		t.Fatal("mem.util.available produced no row")
	}
	if row.A == nil || row.DeltaPct == nil {
		t.Fatalf("the older half must become the A side, got A=%v delta=%v", row.A, row.DeltaPct)
	}
	if *row.DeltaPct > -70 || *row.DeltaPct < -80 {
		t.Errorf("DeltaPct = %.1f, want about -75", *row.DeltaPct)
	}
	if _, on := reasoning.EvaluateOn(reasoning.States, w.Rows, reasoning.Machine{NCPU: 2})["state.mem.available_low"]; !on {
		t.Error("state.mem.available_low must fire once the sampler has both halves")
	}
}

// The ring is the one piece here that can be wrong without being loud. If it
// returned history newest-first, or leaked a stale slot after the wrap, the
// baseline and the current window would swap and every comparative verdict
// would come out with its sign inverted -- a wrong answer that looks exactly
// as plausible as the right one.
func TestSamplerRingKeepsTheNewestSamplesInOrder(t *testing.T) {
	s := NewSampler(time.Second, 4)
	// Ten passes through a four-deep ring: seven must be discarded.
	s.take = procAt(zeroTime, 0, 0)
	feed(t, s, zeroTime, 10, time.Second)

	held := s.ordered()
	if len(held) != 4 {
		t.Fatalf("held = %d, want the ring capacity 4", len(held))
	}
	for i, want := range []int{6, 7, 8, 9} {
		if got := held[i].Taken.Sub(zeroTime); got != time.Duration(want)*time.Second {
			t.Errorf("held[%d] is from t+%s, want t+%ds", i, got, want)
		}
	}
}

// Before the ring wraps there is no stale tail to skip, and the same ordering
// must hold -- a partially filled ring is what every server serves from for
// its first two minutes.
func TestSamplerRingOrdersAPartiallyFilledBuffer(t *testing.T) {
	s := NewSampler(time.Second, 8)
	s.take = procAt(zeroTime, 0, 0)
	feed(t, s, zeroTime, 3, time.Second)

	held := s.ordered()
	if len(held) != 3 {
		t.Fatalf("held = %d, want 3", len(held))
	}
	for i := range held {
		if got := held[i].Taken.Sub(zeroTime); got != time.Duration(i)*time.Second {
			t.Errorf("held[%d] is from t+%s, want t+%ds", i, got, i)
		}
	}
}

// A host with no /proc must produce an error, never a Window. An empty Window
// carries no rows, so every state would be reported unevaluated -- which is
// almost right, but the caller would have no way to say why, and a caller
// that rendered it would show a machine with nothing wrong with it.
func TestSamplerOnAHostItCannotMeasureReturnsAnError(t *testing.T) {
	s := NewSampler(time.Millisecond, 8)
	s.take = func(time.Time) (Sample, error) { return Sample{}, ErrUnsupported }

	// Run must return rather than tick forever: ErrUnsupported is permanent.
	done := make(chan struct{})
	go func() { defer close(done); s.Run(context.Background()) }()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Run kept sampling a host that reported ErrUnsupported")
	}

	if _, err := s.Window(10); !errors.Is(err, ErrUnsupported) {
		t.Errorf("Window err = %v, want ErrUnsupported", err)
	}
}

// A transient read failure is not the same as an unmeasurable host, and must
// not end the loop: /proc/pressure can be masked, a device can vanish, and
// the readings already held stay valid.
func TestSamplerSurvivesATransientFailure(t *testing.T) {
	s := NewSampler(time.Second, 8)
	calls := 0
	good := procAt(zeroTime, 1000, 8000000)
	s.take = func(t time.Time) (Sample, error) {
		calls++
		if calls == 2 {
			return Sample{}, errors.New("transient: /proc/stat: input/output error")
		}
		return good(t)
	}
	for i := 0; i < 5; i++ {
		if !s.add(zeroTime.Add(time.Duration(i) * time.Second)) {
			t.Fatalf("a transient error ended sampling at pass %d", i)
		}
	}
	if got := len(s.ordered()); got != 4 {
		t.Errorf("held = %d, want 4: one pass failed and four succeeded", got)
	}
}

// A sampler that has just started has nothing to say yet, and must say that
// rather than answer. The message has to carry how long it has been running,
// because "wait" is only actionable if the reader can tell it apart from
// "stuck".
func TestSamplerBeforeItHasEnoughSamples(t *testing.T) {
	s := NewSampler(time.Second, 8)
	if _, err := s.Window(10); err == nil {
		t.Fatal("a sampler that never ran must not return a window")
	}

	s.take = procAt(zeroTime, 1000, 8000000)
	s.started = time.Now()
	feed(t, s, zeroTime, 1, time.Second)
	_, err := s.Window(10)
	if err == nil {
		t.Fatal("one sample is not a window: no rate can exist")
	}
	if !strings.Contains(err.Error(), "1 sample") {
		t.Errorf("err = %q, should say how many samples it has", err)
	}
}

// Two or three samples are enough for real counter rates but not enough to
// halve: splitting would trade every rate for a single-reading baseline. The
// window must then claim no baseline at all, so the comparative states
// decline with "no baseline" rather than being judged against one reading.
func TestSamplerDoesNotSplitTooFewSamples(t *testing.T) {
	s := NewSampler(time.Second, 8)
	s.take = procAt(zeroTime, 1000, 8000000)
	feed(t, s, zeroTime, 3, time.Second)

	w, err := s.Window(10)
	if err != nil {
		t.Fatalf("three samples must yield a window: %v", err)
	}
	if w.BaselineSamples != 0 {
		t.Errorf("BaselineSamples = %d, want 0: three samples cannot honestly be halved", w.BaselineSamples)
	}
	if w.Samples != 3 {
		t.Errorf("Samples = %d, want all 3 in the current window", w.Samples)
	}
	// The rates must survive that decision -- this is why the split is
	// skipped rather than the samples being held back.
	if row, ok := rowFor(w, "kernel.all.pswitch"); !ok || row.B == nil {
		t.Error("kernel.all.pswitch must still have a rate: two of the three samples bracket an interval")
	}
}

// Run and Window are called from a ticker goroutine and an HTTP handler
// respectively. Under -race this is the test that catches a missing lock.
func TestSamplerIsSafeToReadWhileSampling(t *testing.T) {
	s := NewSampler(time.Millisecond, 8)
	s.take = procAt(time.Now(), 1000, 8000000)
	ctx, cancel := context.WithCancel(context.Background())
	go s.Run(ctx)
	deadline := time.Now().Add(150 * time.Millisecond)
	for time.Now().Before(deadline) {
		s.Window(10)
	}
	cancel()
}
