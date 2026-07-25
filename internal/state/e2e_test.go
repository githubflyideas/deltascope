package state

import (
	"database/sql"
	"os"
	"strconv"
	"testing"
	"time"

	_ "github.com/mattn/go-sqlite3"
)

// TestStoredProcRoundTrip loads the snapshots captured by the real binary
// into the real SQLite file and verifies the process pipeline end to end:
// storage round-trip, Nearest() selection, and rate computation.
func TestStoredProcRoundTrip(t *testing.T) {
	const dbPath = "/tmp/e2e/deltascope.db"
	if _, err := os.Stat(dbPath); err != nil {
		t.Skip("no e2e database present")
	}
	db, err := sql.Open("sqlite3", dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ss, err := NewStore(db)
	if err != nil {
		t.Fatal(err)
	}

	n, err := ss.Count()
	if err != nil || n < 4 {
		t.Fatalf("expected >=4 stored snapshots, got %d (%v)", n, err)
	}

	rows, err := db.Query(`SELECT taken FROM snapshots ORDER BY taken`)
	if err != nil {
		t.Fatal(err)
	}
	var times []time.Time
	for rows.Next() {
		var s string
		rows.Scan(&s)
		tm, _ := time.Parse(time.RFC3339, s)
		times = append(times, tm)
	}
	rows.Close()

	// exact-time Nearest lookups for the four captures
	snaps := make([]Snapshot, len(times))
	for i, tm := range times {
		s, err := ss.Nearest(tm, time.Second)
		if err != nil {
			t.Fatalf("Nearest(%v) failed: %v", tm, err)
		}
		snaps[i] = s
		// the taken column is second-precision while the JSON body keeps
		// nanoseconds, so allow sub-second difference
		if d := s.Taken.Sub(tm); d < -time.Second || d > time.Second {
			t.Errorf("Nearest(%v) returned %v (off by %v)", tm, s.Taken, d)
		}
	}

	// verify process data survived the SQLite round-trip
	ps := procSection(snaps[len(snaps)-1])
	if len(ps) == 0 {
		t.Fatal("process section empty after storage round-trip")
	}
	t.Logf("processes in last stored snapshot: %d", len(ps))

	// baseline = snaps[0]->snaps[1] (quiet), compare = snaps[2]->snaps[3] (CPU burn)
	d := CompareProcesses(snaps[0], snaps[1], snaps[2], snaps[3], 20, 1, 10240)
	t.Logf("window A %v (%v), window B %v (%v)",
		d.AStart.Format("15:04:05"), d.AEnd.Sub(d.AStart),
		d.BStart.Format("15:04:05"), d.BEnd.Sub(d.BStart))

	if len(d.Rows) == 0 {
		t.Fatal("no rows produced from stored snapshots")
	}

	var hotName string
	var hotPct float64
	for _, r := range d.Rows {
		if r.CPUPctB != nil && *r.CPUPctB > hotPct {
			hotPct, hotName = *r.CPUPctB, r.Name
		}
	}
	t.Logf("busiest process in window B: %s at %.1f%% of a core", hotName, hotPct)
	if hotPct < 10 {
		t.Errorf("burned CPU with 3 'yes' processes but the busiest shows only %.1f%%", hotPct)
	}

	appeared := 0
	for _, r := range d.Rows {
		if r.Verdict == PVAppeared {
			t.Logf("appeared: %s (cpuB=%s)", r.Name, fmtp(r.CPUPctB))
			appeared++
		}
	}
	t.Logf("verdict counts: %d rows, %d appeared", len(d.Rows), appeared)
}

func fmtp(f *float64) string {
	if f == nil {
		return "n/a"
	}
	return strconv.FormatFloat(*f, 'f', 1, 64) + "%"
}
