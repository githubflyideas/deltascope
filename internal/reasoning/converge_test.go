package reasoning

import (
	"testing"

	"github.com/githubflyideas/deltascope/internal/pcp"
)

// The causal edges must form a DAG. A cycle would make "which is the root"
// undefined -- converge() has a runtime guard, but a cycle in the catalog is
// a design error that should fail the build.
func TestDownstreamEdgesFormADAG(t *testing.T) {
	edges := map[string][]string{}
	ids := map[string]bool{}
	for _, d := range Diagnoses {
		ids[d.ID] = true
		edges[d.ID] = d.DownstreamOf
	}
	// every referenced upstream must exist
	for id, ups := range edges {
		for _, u := range ups {
			if !ids[u] {
				t.Errorf("%s is DownstreamOf %q which is not a diagnosis", id, u)
			}
		}
	}
	// DFS cycle check
	const (
		white = 0
		gray  = 1
		black = 2
	)
	color := map[string]int{}
	var visit func(string) bool
	visit = func(n string) bool { // returns true if a cycle is found
		color[n] = gray
		for _, up := range edges[n] {
			switch color[up] {
			case gray:
				t.Errorf("cycle in downstream edges through %s -> %s", n, up)
				return true
			case white:
				if visit(up) {
					return true
				}
			}
		}
		color[n] = black
		return false
	}
	for id := range edges {
		if color[id] == white {
			if visit(id) {
				return
			}
		}
	}
}

// The headline scenario: memory pressure drives swapping drives disk
// saturation drives CPU load. All four fire; only the memory root should be
// a root, and everything must roll up to it.
func TestMemoryToIOToCPUConvergesToMemoryRoot(t *testing.T) {
	m := Machine{NCPU: 4}
	rows := []pcp.DiffRow{
		// memory_pressure_swapping: available_low (worse) + swapping
		rowDelta("mem.util.available", 8_000_000, 2_000_000, -75, pcp.VWorse),
		row("swap.pagesout", 80),
		row("swap.pagesin", 60), // swap_thrashing (swapping_in) + corroborators
		row("mem.vmstat.pgscan_direct", 20),
		// io_bound: a saturated disk + io pressure
		devRow("disk.dev.avactive", "sda", 0.95),
		devRow("disk.dev.aveq", "sda", 12),
		row("kernel.all.pressure.io.some.avg", 40),
		// load high but run queue not (load_without_cpu_demand)
		row("kernel.all.load", 30),
		row("kernel.all.blocked", 12),
	}
	res := Diagnose(Diagnoses, EvaluateOn(States, rows, m))

	byID := map[string]*Result{}
	for i := range res {
		byID[res[i].ID] = &res[i]
	}
	// The memory root must be present and a root.
	root, ok := byID["diagnosis.memory_pressure_swapping"]
	if !ok {
		t.Fatalf("expected memory_pressure_swapping to fire; got %v", diagnosisIDs(res))
	}
	if !root.IsRoot {
		t.Error("memory_pressure_swapping should be a root cause")
	}
	// io_bound and load_without_cpu_demand, if they fired, must NOT be roots
	// and must roll up to the memory root.
	for _, downstream := range []string{"diagnosis.io_bound", "diagnosis.load_without_cpu_demand", "diagnosis.swap_thrashing"} {
		if r, ok := byID[downstream]; ok {
			if r.IsRoot {
				t.Errorf("%s should be a consequence, not a root", downstream)
			}
			if r.RootID != "diagnosis.memory_pressure_swapping" {
				t.Errorf("%s should roll up to the memory root, got RootID=%s", downstream, r.RootID)
			}
		}
	}
	// The first result overall must be the root -- the one-line answer lands
	// on the cause, not a symptom.
	if !res[0].IsRoot || res[0].RootID != "diagnosis.memory_pressure_swapping" {
		t.Errorf("first result should be the memory root, got %s (root=%v)", res[0].ID, res[0].IsRoot)
	}
	t.Logf("order: %v", diagnosisIDs(res))
}

// Nothing is hidden (approach B): a consequence is still present in the
// output, just annotated and demoted.
func TestConsequencesRemainVisible(t *testing.T) {
	m := Machine{NCPU: 4}
	rows := []pcp.DiffRow{
		rowDelta("mem.util.available", 8_000_000, 2_000_000, -75, pcp.VWorse),
		row("swap.pagesout", 80),
		devRow("disk.dev.avactive", "sda", 0.95),
		devRow("disk.dev.aveq", "sda", 12),
		row("kernel.all.pressure.io.some.avg", 40),
	}
	res := Diagnose(Diagnoses, EvaluateOn(States, rows, m))
	var sawRoot, sawConsequence bool
	for _, r := range res {
		if r.ID == "diagnosis.memory_pressure_swapping" {
			sawRoot = true
		}
		if r.ID == "diagnosis.io_bound" {
			sawConsequence = true
			if len(r.DownstreamOf) == 0 {
				t.Error("io_bound should carry its active upstream, not an empty DownstreamOf")
			}
		}
	}
	if !sawRoot || !sawConsequence {
		t.Errorf("both root and consequence must remain in output (root=%v consequence=%v)", sawRoot, sawConsequence)
	}
}

// When an upstream did NOT fire, the downstream stands on its own as a root
// -- an isolated io_bound with no memory pressure is its own root cause.
func TestIsolatedDownstreamIsItsOwnRoot(t *testing.T) {
	m := Machine{NCPU: 4}
	rows := []pcp.DiffRow{
		devRow("disk.dev.avactive", "sda", 0.95),
		devRow("disk.dev.aveq", "sda", 12),
		row("kernel.all.pressure.io.some.avg", 40),
	}
	res := Diagnose(Diagnoses, EvaluateOn(States, rows, m))
	for _, r := range res {
		if r.ID == "diagnosis.io_bound" {
			if !r.IsRoot {
				t.Error("io_bound with no memory pressure upstream must be its own root")
			}
			if r.RootID != "diagnosis.io_bound" {
				t.Errorf("isolated io_bound RootID should be itself, got %s", r.RootID)
			}
		}
	}
}

// A healthy machine still produces nothing -- convergence must not invent.
func TestConvergeOnHealthyMachineIsEmpty(t *testing.T) {
	res := Diagnose(Diagnoses, map[string]Active{})
	if len(res) != 0 {
		t.Errorf("no diagnoses, no convergence artifacts; got %d", len(res))
	}
}
