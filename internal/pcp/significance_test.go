package pcp

import "testing"

// The three behaviours these tests pin were each implemented more than once
// before RelChange and drifted. This is the regression net that keeps the
// shared core and its two callers from diverging again.

func TestRelChangeSharedCore(t *testing.T) {
	fp := func(f float64) *float64 { return &f }
	cases := []struct {
		name        string
		a, b, floor float64
		wantDelta   *float64
		wantNoise   bool
	}{
		{"plain rise", 100, 220, 0, fp(120), false},
		{"both idle below floor", 0.005, 0.097, 1, fp(1840), true},
		{"zero to nonzero", 0, 5, 0, nil, false},
		{"zero to zero", 0, 0, 0, fp(0), false},
		{"zero to zero with floor is idle", 0, 0, 1, fp(0), true},
		{"small baseline, above-floor b keeps ratio (metric policy)", 10, 25, 20, fp(150), false},
		{"fall", 200, 100, 0, fp(-50), false},
	}
	for _, c := range cases {
		d, noise := RelChange(c.a, c.b, c.floor)
		if noise != c.wantNoise {
			t.Errorf("%s: noise=%v want %v", c.name, noise, c.wantNoise)
		}
		switch {
		case c.wantDelta == nil && d != nil:
			t.Errorf("%s: delta=%v want nil", c.name, *d)
		case c.wantDelta != nil && d == nil:
			t.Errorf("%s: delta=nil want %v", c.name, *c.wantDelta)
		case c.wantDelta != nil && d != nil && (*d-*c.wantDelta > 1e-6 || *c.wantDelta-*d > 1e-6):
			t.Errorf("%s: delta=%v want %v", c.name, *d, *c.wantDelta)
		}
	}
}

// judge, the metric path, must keep its exact verdicts after routing through
// RelChange -- the "behaviour unchanged" guarantee for the shared-core
// refactor.
func TestJudgeVerdictsUnchanged(t *testing.T) {
	fp := func(f float64) *float64 { return &f }
	cases := []struct {
		a, b   *float64
		pol    Polarity
		minAbs float64
		wantV  Verdict
	}{
		{fp(100), fp(600), WorseUp, 0, VWorse},
		{fp(600), fp(100), WorseUp, 0, VBetter},
		{fp(100), fp(110), WorseUp, 0, VFlat},
		{fp(0), fp(5), WorseUp, 0, VWorse},
		{fp(0), fp(0), WorseUp, 0, VFlat},
		{fp(0.005), fp(0.097), WorseUp, 1, VFlat},
		{nil, fp(5), WorseUp, 0, VWatch},
	}
	for i, c := range cases {
		_, _, v := judge(c.a, c.b, c.pol, 15, c.minAbs)
		if v != c.wantV {
			t.Errorf("case %d: verdict=%v want %v", i, v, c.wantV)
		}
	}
}

// A near-idle counter (icmp inerrors, floor 1) ticking 0.002 -> 0 must be
// flat, not "-100%" worse -- the udp.noports-class bug, now guarded at the
// buildRows level because judge shares RelChange.
func TestBuildRowsIdleNoiseStaysFlat(t *testing.T) {
	a := map[string]Value{"network.icmp.inerrors\x00": {Metric: "network.icmp.inerrors", Val: 0.002}}
	b := map[string]Value{"network.icmp.inerrors\x00": {Metric: "network.icmp.inerrors", Val: 0}}
	rows := buildRows(a, b, 15)
	if len(rows) != 1 {
		t.Fatalf("want 1 row, got %d", len(rows))
	}
	if rows[0].Verdict != VFlat {
		t.Errorf("idle error counter 0.002 -> 0 must be flat, got %v", rows[0].Verdict)
	}
}
