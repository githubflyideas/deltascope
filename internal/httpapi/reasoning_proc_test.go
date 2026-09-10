package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/githubflyideas/deltascope/internal/native"
	"github.com/githubflyideas/deltascope/internal/pcp"
)

// The reasoning screen used to be switched off entirely on a host without
// PCP, which meant the engine deltascope is mostly made of was unreachable on
// every Debian and Ubuntu box that had not had pmlogger set up. These tests
// cover the fallback that replaced that, and the thing the fallback must
// never do: answer with an empty window that renders as a healthy machine.

// fakeSampler stands in for the rolling /proc history. A real one cannot
// produce a warming-up or unmeasurable window on a host where /proc works,
// and those are the two cases with the most to get wrong.
type fakeSampler struct {
	win native.Window
	err error
}

func (f fakeSampler) Window(float64) (native.Window, error) { return f.win, f.err }
func (f fakeSampler) Interval() time.Duration               { return 2 * time.Second }

// errRunner is a PCP runner that always fails, used to prove the archive path
// was taken rather than to exercise it.
type errRunner struct{}

func (errRunner) Run(context.Context, string, ...string) ([]byte, []byte, error) {
	return nil, nil, errors.New("pmlogsummary: no archive for that window")
}

func f64(v float64) *float64 { return &v }

// procWindow is a plausible /proc window: one gauge row well over the
// available-memory floor, so a state fires and the response can be checked
// for having actually run the engine rather than merely returned a shape.
func procWindow(baseline int) native.Window {
	end := time.Date(2026, 9, 10, 12, 0, 0, 0, time.UTC)
	w := native.Window{
		Rows: []pcp.DiffRow{{
			Metric: "mem.util.available", Label: "Available memory", Category: "memory",
			Units: "Kbyte", B: f64(200000), BCount: 32, Verdict: pcp.VFlat,
		}},
		Samples: 32,
		Elapsed: 62 * time.Second,
		Start:   end.Add(-62 * time.Second),
		End:     end,
	}
	if baseline > 0 {
		w.BaselineSamples = baseline
		w.BaselineElapsed = 62 * time.Second
		w.BaselineStart = w.Start.Add(-64 * time.Second)
		w.BaselineEnd = w.Start.Add(-2 * time.Second)
		w.Rows[0].A = f64(8000000)
		w.Rows[0].DeltaPct = f64(-97.5)
		w.Rows[0].Exceeded = true
		w.Rows[0].Verdict = pcp.VWorse
	}
	return w
}

func decodeReasoning(t *testing.T, rec *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", rec.Code, rec.Body.String())
	}
	var out map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("response is not JSON: %v", err)
	}
	return out
}

func stateByID(t *testing.T, body map[string]any) map[string]map[string]any {
	t.Helper()
	raw, ok := body["states"].([]any)
	if !ok {
		t.Fatalf("no states in response: %v", body)
	}
	out := map[string]map[string]any{}
	for _, s := range raw {
		m := s.(map[string]any)
		out[m["id"].(string)] = m
	}
	return out
}

// The headline: a host with no archive gets a real answer off /proc, the
// response says which source produced it, and the engine actually ran.
func TestReasoningFallsBackToProcWithoutPCP(t *testing.T) {
	s := &Server{
		Caps:    Capabilities{Metrics: false, Reasoning: true, Reason: "PCP tools not installed"},
		Sampler: fakeSampler{win: procWindow(32)},
	}
	rec := httptest.NewRecorder()
	s.handleReasoning(rec, httptest.NewRequest("GET", "/api/reasoning", nil))
	body := decodeReasoning(t, rec)

	if body["source"] != "proc" {
		t.Errorf("source = %v, want proc: a reader must be able to tell which source answered", body["source"])
	}
	win := body["window"].(map[string]any)
	if !strings.Contains(win["label"].(string), "/proc") {
		t.Errorf("label = %q, should name the source", win["label"])
	}
	// The window must be the one that was measured, not the one the URL asked
	// for -- /proc has no history to seek in.
	if !strings.HasPrefix(win["b_end"].(string), "2026-09-10T12:00:00") {
		t.Errorf("b_end = %v, want the sampler's own end time", win["b_end"])
	}
	// 200000 kB available with a -97.5% verdict is what state.mem.available_low
	// is written for. If it is quiet here, the rows never reached the engine.
	st := stateByID(t, body)["state.mem.available_low"]
	if st["active"] != true {
		t.Errorf("state.mem.available_low = %v, want active: the /proc rows did not reach the engine", st)
	}
}

// The query string offers four window bounds. /proc cannot honour them, and
// answering "now" under the label the caller asked for would be a silent lie
// about which minutes were measured.
func TestReasoningFromProcIgnoresTheTimePickerAndSaysSo(t *testing.T) {
	s := &Server{
		Caps:    Capabilities{Reasoning: true},
		Sampler: fakeSampler{win: procWindow(32)},
	}
	rec := httptest.NewRecorder()
	s.handleReasoning(rec, httptest.NewRequest("GET",
		"/api/reasoning?a_start=2026-01-01T00:00&a_end=2026-01-01T01:00"+
			"&b_start=2026-01-02T00:00&b_end=2026-01-02T01:00", nil))
	win := decodeReasoning(t, rec)["window"].(map[string]any)

	if strings.HasPrefix(win["b_end"].(string), "2026-01-02") {
		t.Error("the response echoed the requested window; /proc cannot measure a window from January")
	}
	if !strings.HasPrefix(win["b_end"].(string), "2026-09-10T12:00:00") {
		t.Errorf("b_end = %v, want the window that was actually sampled", win["b_end"])
	}
}

// A sampler that has not collected enough yet must say how far along it is.
// This is the first minute of every restart, and "wait" is only actionable if
// the reader can tell it apart from "this will never work".
func TestReasoningWhileTheSamplerIsWarmingUp(t *testing.T) {
	s := &Server{
		Caps:    Capabilities{Reasoning: true},
		Sampler: fakeSampler{err: fmt.Errorf("native: 1 sample(s) 2s after start; two are needed before any rate exists")},
	}
	rec := httptest.NewRecorder()
	s.handleReasoning(rec, httptest.NewRequest("GET", "/api/reasoning", nil))

	// 503, not 502: nothing failed.
	if rec.Code != http.StatusServiceUnavailable {
		t.Errorf("status = %d, want 503", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "1 sample") {
		t.Errorf("body = %s, should pass the sampler's own count through", rec.Body.String())
	}
}

// No archive and no /proc is the one combination where the chain has nothing
// at all. It must refuse rather than return an empty window: a 200 with zero
// rows renders as a machine on which nothing is wrong.
func TestReasoningWithNoSourceAtAllRefuses(t *testing.T) {
	s := &Server{Caps: Capabilities{Metrics: false, Reason: "PCP tools not installed: pmlogsummary (package: pcp)"}}
	rec := httptest.NewRecorder()
	s.handleReasoning(rec, httptest.NewRequest("GET", "/api/reasoning", nil))

	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503 -- never 200 with an empty window", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "pmlogsummary") {
		t.Errorf("body = %s, should name what to install", rec.Body.String())
	}
}

// Before the sampler has enough history to split, the two comparative states
// cannot be judged. They must be reported as unmeasured with a note saying
// when that will change -- not as quiet, which would read as "checked, fine".
func TestReasoningFromProcExplainsAMissingBaseline(t *testing.T) {
	s := &Server{
		Caps:    Capabilities{Reasoning: true},
		Sampler: fakeSampler{win: procWindow(0)},
	}
	rec := httptest.NewRecorder()
	s.handleReasoning(rec, httptest.NewRequest("GET", "/api/reasoning", nil))
	body := decodeReasoning(t, rec)

	note, ok := body["note"].(string)
	if !ok || !strings.Contains(note, "baseline") {
		t.Errorf("note = %v, should explain the missing baseline", body["note"])
	}
	st := stateByID(t, body)["state.mem.available_low"]
	if st["active"] == true {
		t.Fatal("state.mem.available_low fired with no baseline to compare against")
	}
	reason, _ := st["reason"].(string)
	if reason == "" {
		t.Error("with no baseline the state must be unmeasured, not quiet: a quiet row reads as checked and fine")
	}
	// And the label must not claim a comparison that did not happen.
	if label := body["window"].(map[string]any)["label"].(string); strings.Contains(label, " vs ") {
		t.Errorf("label = %q, claims a comparison with no baseline", label)
	}
}

// A host with an archive must keep using it even if a sampler is somehow
// attached. Both sources answer the same fixed window now, but they do not
// answer it equally well: the archive has the full metric set and a real
// baseline half, while the sampler holds only what it has managed to collect
// since the process started. Silently preferring the narrower source would
// shrink the report with nothing on screen saying so.
func TestReasoningPrefersTheArchiveWhenPCPIsPresent(t *testing.T) {
	s := &Server{
		Caps:    Capabilities{Metrics: true, Reasoning: true},
		Runner:  errRunner{},
		Sampler: fakeSampler{win: procWindow(32)},
	}
	rec := httptest.NewRecorder()
	s.handleReasoning(rec, httptest.NewRequest("GET", "/api/reasoning", nil))

	if rec.Code != http.StatusBadGateway {
		t.Fatalf("status = %d, want 502 from the archive path: the sampler was used instead", rec.Code)
	}
	if strings.Contains(rec.Body.String(), "/proc") {
		t.Errorf("the /proc fallback answered on a host with PCP: %s", rec.Body.String())
	}
}
