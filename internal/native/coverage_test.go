package native

import (
	"strings"
	"testing"

	"github.com/githubflyideas/deltascope/internal/pcp"
	"github.com/githubflyideas/deltascope/internal/reasoning"
)

// TestEveryStateMetricHasANativeSource is the acceptance gate for this
// package, and the mirror of internal/pcp's TestEveryStateMetricIsCollectable.
//
// Without it, adding a state is enough to make the native path silently
// weaker than the archive path: the state compiles, its metric is in the
// PCP catalog, and on a host with no PCP it is simply never evaluated --
// which looks identical to "the condition is not met". The whole point of
// the native collector is that unknown and false are different answers, so
// the set of collectable metrics has to be enforced, not hoped for.
func TestEveryStateMetricHasANativeSource(t *testing.T) {
	for _, st := range reasoning.States {
		for _, c := range st.When {
			m := c.Metric
			if strings.HasPrefix(m, "derived.") {
				// Derived metrics are synthesized by reasoning.Derive from the
				// per-core rows, so the requirement passes through to those.
				for _, comp := range []string{
					"kernel.percpu.cpu.user",
					"kernel.percpu.cpu.sys",
					"kernel.percpu.cpu.irq.soft",
				} {
					if _, ok := specs[comp]; !ok {
						t.Errorf("state %s needs %s, whose input %s has no native spec", st.ID, m, comp)
					}
				}
				continue
			}
			if _, ok := specs[m]; !ok {
				t.Errorf("state %s uses %s, which has no native spec: on a host without PCP this state can never be evaluated", st.ID, m)
			}
		}
	}
}

// TestEverySpecIsInCatalog guards the other direction. Build applies the
// same catalog gate internal/pcp/diff.go does, so a spec for a metric with
// no MetricInfo is dead code that reads as coverage.
func TestEverySpecIsInCatalog(t *testing.T) {
	for m := range specs {
		if _, ok := pcp.Lookup(m); !ok {
			t.Errorf("spec %s is not in the PCP catalog, so Build will drop every row for it", m)
		}
	}
}

// TestNoSpecHasZeroScale catches the easiest possible typo in metrics.go: a
// zero Scale silently turns every reading of that metric into 0, which the
// thresholds read as a perfectly healthy machine.
func TestNoSpecHasZeroScale(t *testing.T) {
	for m, sp := range specs {
		if sp.Scale == 0 {
			t.Errorf("spec %s has Scale 0, which would report every reading as zero", m)
		}
		if sp.Unit == "" {
			t.Errorf("spec %s has no documented unit", m)
		}
	}
}

// TestCPUScaleMatchesThresholdUnits pins the one conversion that would be
// wrong by a factor of ten without anyone noticing: kernel.all.cpu.* is
// millisec/second, where 1000 is one saturated core, so a core consuming
// every tick for a second must come out as 1000 and not as 100 (percent) or
// 10000. reasoning.coreSaturatedMsPerSec (850) is written against this.
func TestCPUScaleMatchesThresholdUnits(t *testing.T) {
	sp, ok := specs["kernel.all.cpu.user"]
	if !ok {
		t.Fatal("kernel.all.cpu.user has no spec")
	}
	// One second of wall clock at USER_HZ=100 is 100 ticks.
	if got := 100 * sp.Scale; got != 1000 {
		t.Errorf("one fully consumed core reads as %v ms/s, want 1000", got)
	}
}
