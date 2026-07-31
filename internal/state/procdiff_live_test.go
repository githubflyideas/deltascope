package state

import (
	"context"
	"encoding/json"
	"testing"
	"time"
)

// TestLiveProcCollection exercises the /proc parser against the real
// filesystem, then verifies that a CPU burst shows up as a rate.
func TestLiveProcCollection(t *testing.T) {
	ctx := context.Background()
	s1 := Capture(ctx, "test")

	var sec Section
	for _, x := range s1.Sections {
		if x.Name == "processes" {
			sec = x
		}
	}
	if sec.Name == "" {
		t.Fatal("processes section missing entirely")
	}
	if len(sec.Items) == 0 {
		t.Fatalf("no processes collected (skipped=%q)", sec.Skipped)
	}
	if !sec.SkipDiff {
		t.Error("processes section must be marked SkipDiff")
	}
	t.Logf("collected %d process names", len(sec.Items))
	for i, it := range sec.Items {
		if i >= 5 {
			break
		}
		ticks, rss, start, count, ok := DecodeProcItem(it.Value)
		if !ok {
			t.Errorf("undecodable value for %s: %q", it.Key, it.Value)
			continue
		}
		t.Logf("  %-18s ticks=%-8d rss=%-9dKB start=%-10d inst=%d", it.Key, ticks, rss, start, count)
		if rss == 0 && ticks == 0 {
			t.Errorf("%s has both zero rss and zero ticks -- parser likely misaligned", it.Key)
		}
	}

	// burn CPU, snapshot again, confirm we see a rate
	stop := make(chan struct{})
	for i := 0; i < 2; i++ {
		go func() {
			x := 0.0
			for {
				select {
				case <-stop:
					return
				default:
					x += 1.000001
					_ = x
				}
			}
		}()
	}
	time.Sleep(4 * time.Second)
	s2 := Capture(ctx, "test")
	close(stop)

	d := CompareProcesses(s1, s1, s1, s2, 15, 1, 51200)
	t.Logf("window B elapsed: %v", d.BEnd.Sub(d.BStart))
	found := 0
	for _, r := range d.Rows {
		if r.CPUPctB != nil && *r.CPUPctB > 1 {
			t.Logf("  %-18s cpuB=%.1f%% of a core  rss=%.0fKB", r.Name, *r.CPUPctB, deref(r.RSSKBB))
			found++
		}
	}
	if found == 0 {
		t.Error("burned CPU for 4s but no process shows a rate above 1% -- rate math is wrong")
	}

	// the generic change diff must ignore the processes section
	sd := Compare(s1, s2)
	for _, s := range sd.Sections {
		if s.Name == "processes" {
			t.Errorf("processes leaked into the generic change diff (%d changes)", len(s.Changes))
		}
	}
	t.Logf("generic statediff between the two snapshots: %d changes (processes correctly excluded)", sd.Total)
}

func deref(f *float64) float64 {
	if f == nil {
		return 0
	}
	return *f
}

// TestProcDiffJSONFields locks the wire format. A struct tag written as
// `json:"a_start,a_end"` on a two-name field silently gives both fields
// the same JSON name, dropping one from the output entirely -- the
// frontend then renders "Invalid Date". go vet catches it, but a test
// makes the contract explicit.
func TestProcDiffJSONFields(t *testing.T) {
	now := time.Now()
	d := ProcDiff{
		AStart: now.Add(-2 * time.Hour), AEnd: now.Add(-time.Hour),
		BStart: now.Add(-time.Hour), BEnd: now,
	}
	raw, err := json.Marshal(d)
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatal(err)
	}
	for _, k := range []string{"a_start", "a_end", "b_start", "b_end"} {
		if _, ok := m[k]; !ok {
			t.Errorf("%s missing from ProcDiff JSON: %s", k, raw)
		}
	}
}
