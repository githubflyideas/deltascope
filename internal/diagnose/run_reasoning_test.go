package diagnose

import (
	"context"
	"strings"
	"testing"

	"github.com/githubflyideas/deltascope/internal/reasoning"
)

// fakeRunner returns a fixed pmlogsummary body for both window queries, so
// Run's metric+reasoning path can be exercised without a live PCP archive.
type fakeRunner struct{ body string }

func (f fakeRunner) Run(_ context.Context, _ string, _ ...string) ([]byte, []byte, error) {
	return []byte(f.body), nil, nil
}

// A+C guarantee: the one-click Run() must carry the per-core reasoning
// diagnoses over the SAME window it uses for everything else, so the
// one-click page and the reasoning chain cannot disagree about whether a
// core is pegged. Before this, reasoning lived only on its own tab with its
// own window and the two views told different stories.
func TestRunCarriesReasoningOverTheSameWindow(t *testing.T) {
	// cpu1 pegged (700 user + 280 sys = 980 ms/s of one core), the rest
	// idle. Both windows see the same body, so it is a sustained peg.
	body := strings.Join([]string{
		`kernel.percpu.cpu.user ["cpu0"] 40.0 millisec / second`,
		`kernel.percpu.cpu.user ["cpu1"] 700.0 millisec / second`,
		`kernel.percpu.cpu.user ["cpu2"] 30.0 millisec / second`,
		`kernel.percpu.cpu.user ["cpu3"] 25.0 millisec / second`,
		`kernel.percpu.cpu.sys ["cpu0"] 15.0 millisec / second`,
		`kernel.percpu.cpu.sys ["cpu1"] 280.0 millisec / second`,
		`kernel.percpu.cpu.sys ["cpu2"] 12.0 millisec / second`,
		`kernel.percpu.cpu.sys ["cpu3"] 10.0 millisec / second`,
		`kernel.all.cpu.user 795.0 millisec / second`,
		`kernel.all.cpu.sys 317.0 millisec / second`,
	}, "\n") + "\n"

	// The scale-relative state layer reads the host core count; pin it so
	// the test describes a concrete 4-core machine rather than the CI box.
	reasoning.SetHost(reasoning.Machine{NCPU: 4})

	out, err := Run(context.Background(), Deps{Runner: fakeRunner{body}, Archive: "x"})
	if err != nil {
		t.Fatal(err)
	}
	if len(out.Reasoning) == 0 {
		t.Fatal("Run must populate Reasoning; the one-click page shows it inline")
	}
	var ids []string
	for _, r := range out.Reasoning {
		ids = append(ids, r.ID)
	}
	found := false
	for _, id := range ids {
		if id == "diagnosis.single_core_saturated" {
			found = true
		}
	}
	if !found {
		t.Errorf("a sustained pegged core must surface as single_core_saturated in Run's reasoning, got %v", ids)
	}

	// And the CPU triage block must have been escalated off green, so the
	// card and the reasoning section agree.
	for _, b := range out.Triage {
		if b.Key == "cpu" && string(b.Status) == "ok" {
			t.Error("CPU triage must not be green while a core is pegged")
		}
	}
	t.Logf("reasoning: %v", ids)
}
