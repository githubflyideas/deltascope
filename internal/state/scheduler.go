package state

import (
	"context"
	"log"
	"os"
	"time"
)

// DefaultSnapshotInterval is how often the built-in scheduler captures
// state. Ten minutes keeps process CPU rates meaningful (a rate needs two
// readings, and a shorter gap makes short bursts visible) while keeping
// storage modest: ~1700 facts plus ~45 processes per snapshot.
const DefaultSnapshotInterval = 10 * time.Minute

// Scheduler captures snapshots on an interval so the operator does not
// have to install a cron job. It takes one immediately on start, so a
// freshly installed server has data to compare from the first interval
// onward rather than after a day of waiting.
type Scheduler struct {
	Store     *Store
	Interval  time.Duration
	KeepDays  int
	OnCapture func(Snapshot)
}

// Run blocks until ctx is cancelled, capturing on the interval.
func (s *Scheduler) Run(ctx context.Context) {
	interval := s.Interval
	if interval <= 0 {
		interval = DefaultSnapshotInterval
	}
	keep := s.KeepDays
	if keep <= 0 {
		keep = 7
	}
	host, _ := os.Hostname()

	capture := func() {
		cctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
		defer cancel()
		snap := Capture(cctx, host)
		if err := s.Store.Save(snap); err != nil {
			log.Printf("snapshot: save failed: %v", err)
			return
		}
		if s.OnCapture != nil {
			s.OnCapture(snap)
		}
	}

	capture()
	lastPrune := time.Now()

	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			capture()
			if time.Since(lastPrune) > 6*time.Hour {
				if n, err := s.Store.Prune(keep); err == nil && n > 0 {
					log.Printf("snapshot: pruned %d expired snapshots", n)
				}
				lastPrune = time.Now()
			}
		}
	}
}
