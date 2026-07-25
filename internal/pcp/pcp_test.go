package pcp

import (
	"context"
	"fmt"
	"math"
	"os"
	"strings"
	"testing"
	"time"
)

const sampleSummary = `Performance metrics from host rocky9.example
  commencing Thu Jul  2 14:00:00.000 2026
  ending     Thu Jul  2 15:00:00.000 2026

kernel.all.cpu.user  245.917 millisec / second
kernel.all.cpu.sys  51.300 millisec / second
kernel.all.load ["1 minute"] 1.542 none
kernel.all.load ["5 minute"] 1.230 none
mem.util.available  10485760.000 Kbyte
disk.all.read_bytes  512.250 Kbyte / second
network.interface.in.bytes ["eth0"] 10240.500 byte / second
network.interface.in.bytes ["lo"] 12.000 byte / second
network.tcp.retranssegs  0.000 count / second
some.unparseable line without number tail x
`

func TestParseSummary(t *testing.T) {
	vals := ParseSummary(strings.NewReader(sampleSummary))
	if len(vals) != 9 {
		t.Fatalf("expected 9 rows, got %d: %+v", len(vals), vals)
	}
	byKey := map[string]Value{}
	for _, v := range vals {
		byKey[v.Key()] = v
	}
	load1 := byKey["kernel.all.load\x001 minute"]
	if load1.Val != 1.542 || load1.Units != "none" {
		t.Errorf("load[1 minute] parse error: %+v", load1)
	}
	cpu := byKey["kernel.all.cpu.user\x00"]
	if cpu.Val != 245.917 || cpu.Units != "millisec / second" {
		t.Errorf("cpu.user parse error: %+v", cpu)
	}
	eth0 := byKey["network.interface.in.bytes\x00eth0"]
	if eth0.Val != 10240.5 {
		t.Errorf("in.bytes[eth0] parse error: %+v", eth0)
	}
}

func TestPCPTime(t *testing.T) {
	ts := time.Date(2026, 7, 3, 14, 0, 0, 0, time.Local)
	got := PCPTime(ts)
	want := "@ Fri Jul  3 14:00:00 2026"
	if got != want {
		t.Errorf("PCPTime = %q, want %q", got, want)
	}
}

func f(v float64) *float64 { return &v }

func TestJudge(t *testing.T) {
	cases := []struct {
		name     string
		a, b     *float64
		pol      Polarity
		wantV    Verdict
		wantExc  bool
		wantDpct *float64
	}{
		{"CPU up 500% worse", f(100), f(600), WorseUp, VWorse, true, f(500)},
		{"CPU down better", f(100), f(60), WorseUp, VBetter, true, f(-40)},
		{"available mem down worse", f(1000), f(500), BetterUp, VWorse, true, f(-50)},
		{"available mem up better", f(1000), f(1300), BetterUp, VBetter, true, nil},
		{"throughput change flagged watch", f(100), f(200), Neutral, VWatch, true, nil},
		{"below threshold flat", f(100), f(110), WorseUp, VFlat, false, f(10)},
		{"0 to 0 flat", f(0), f(0), WorseUp, VFlat, false, f(0)},
		{"0 to nonzero worse (inf)", f(0), f(5), WorseUp, VWorse, true, nil},
		{"one side missing flagged watch", nil, f(5), WorseUp, VWatch, true, nil},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			d, exc, v := judge(c.a, c.b, c.pol, 15, 0)
			if v != c.wantV || exc != c.wantExc {
				t.Fatalf("judge=%v/%v, want %v/%v", v, exc, c.wantV, c.wantExc)
			}
			if c.wantDpct != nil {
				if d == nil || math.Abs(*d-*c.wantDpct) > 1e-9 {
					t.Fatalf("delta=%v, want %v", d, *c.wantDpct)
				}
			}
		})
	}
}

type fakeRunner struct {
	outs [][]byte
	i    int
	args [][]string
}

func (fr *fakeRunner) Run(_ context.Context, name string, args ...string) ([]byte, []byte, error) {
	fr.args = append(fr.args, append([]string{name}, args...))
	o := fr.outs[fr.i]
	fr.i++
	return o, nil, nil
}

func TestCompare(t *testing.T) {
	baseline := `kernel.all.cpu.user  100.000 millisec / second
mem.util.available  8000000.000 Kbyte
network.tcp.retranssegs  1.000 count / second
`
	current := `kernel.all.cpu.user  600.000 millisec / second
mem.util.available  4000000.000 Kbyte
network.tcp.retranssegs  1.050 count / second
`
	fr := &fakeRunner{outs: [][]byte{[]byte(baseline), []byte(current)}}
	w := Windows{
		AStart: time.Now().Add(-25 * time.Hour), AEnd: time.Now().Add(-24 * time.Hour),
		BStart: time.Now().Add(-1 * time.Hour), BEnd: time.Now(),
		ThresholdPct: 15,
	}
	rep, err := Compare(context.Background(), fr, "/var/log/pcp/pmlogger/host", w)
	if err != nil {
		t.Fatal(err)
	}
	if len(fr.args) != 2 || fr.args[0][0] != "pmlogsummary" {
		t.Fatalf("expected two pmlogsummary calls: %+v", fr.args)
	}
	got := map[string]DiffRow{}
	for _, r := range rep.Rows {
		got[r.Metric] = r
	}
	if r := got["kernel.all.cpu.user"]; r.Verdict != VWorse || !r.Exceeded || math.Abs(*r.DeltaPct-500) > 1e-9 {
		t.Errorf("cpu.user verdict error: %+v", r)
	}
	if r := got["mem.util.available"]; r.Verdict != VWorse || math.Abs(*r.DeltaPct+50) > 1e-9 {
		t.Errorf("mem.available verdict error: %+v", r)
	}
	if r := got["network.tcp.retranssegs"]; r.Verdict != VFlat || r.Exceeded {
		t.Errorf("retranssegs should be flat: %+v", r)
	}
	if rep.Rows[0].Metric != "kernel.all.cpu.user" {
		t.Errorf("sort order wrong, first row=%s", rep.Rows[0].Metric)
	}
}

const sampleCSV = `Time,"kernel.all.load-1 minute","kernel.all.load-5 minute"
2026-07-03 14:00:00,1.500,1.200
2026-07-03 14:01:00,,1.210
2026-07-03 14:02:00,1.700,1.220
`

func TestParseTrendCSV(t *testing.T) {
	series, err := ParseTrendCSV(strings.NewReader(sampleCSV))
	if err != nil {
		t.Fatal(err)
	}
	if len(series) != 2 {
		t.Fatalf("expected 2 series, got %d", len(series))
	}
	if series[0].Name != `kernel.all.load-1 minute` {
		t.Errorf("series name wrong: %q", series[0].Name)
	}
	if len(series[0].Points) != 3 {
		t.Fatalf("expected 3 points, got %d", len(series[0].Points))
	}
	if series[0].Points[1][1] != nil {
		t.Errorf("missing value should be nil: %+v", series[0].Points[1])
	}
	if v, ok := series[0].Points[2][1].(float64); !ok || v != 1.7 {
		t.Errorf("value parse error: %+v", series[0].Points[2])
	}
}

func TestTrendStep(t *testing.T) {
	now := time.Now()
	// floor is 60s: a step finer than pmlogger's logging interval returns
	// empty output rather than a denser chart
	if s := TrendStep(now.Add(-time.Hour), now); s != time.Minute {
		t.Errorf("1h window step should be 60s, got %v", s)
	}
	if s := TrendStep(now.Add(-14*24*time.Hour), now); s != 15*time.Minute {
		t.Errorf("14d window step should cap at 15m, got %v", s)
	}
}

func TestCatalogRoundTrip(t *testing.T) {
	before := len(Catalog)
	data, err := ExportCatalog()
	if err != nil {
		t.Fatal(err)
	}
	f := t.TempDir() + "/cat.json"
	if err := os.WriteFile(f, data, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := LoadCatalogFile(f); err != nil {
		t.Fatal(err)
	}
	if len(Catalog) != before {
		t.Fatalf("metric count mismatch after round-trip: %d != %d", len(Catalog), before)
	}
	if _, ok := Lookup("network.tcp.syncookiessent"); !ok {
		t.Error("index not rebuilt after load")
	}
	bad := t.TempDir() + "/bad.json"
	os.WriteFile(bad, []byte(`{"categories":["x"],"metrics":[{"metric":"a;rm -rf","label":"l","category":"x","polarity":"neutral"}]}`), 0o644)
	if err := LoadCatalogFile(bad); err == nil {
		t.Error("invalid metric name should be rejected")
	}
	os.WriteFile(bad, []byte(`{"categories":["x"],"metrics":[{"metric":"a.b","label":"l","category":"x","polarity":"up"}]}`), 0o644)
	if err := LoadCatalogFile(bad); err == nil {
		t.Error("invalid polarity should be rejected")
	}
}

func TestRulesEngine(t *testing.T) {
	rows := []DiffRow{
		{Metric: "swap.pagesout", Verdict: VWorse, A: f(0.1), B: f(50), DeltaPct: f(49900)},
		{Metric: "mem.util.available", Verdict: VWorse, A: f(8e6), B: f(2e6), DeltaPct: f(-75)},
		{Metric: "kernel.all.cpu.user", Verdict: VFlat, A: f(100), B: f(105), DeltaPct: f(5)},
	}
	fs := EvaluateRules(rows)
	found := false
	for _, x := range fs {
		if x.ID == "swap-spiral" {
			found = true
			if len(x.Evidence) != 2 {
				t.Errorf("swap-spiral should have 2 evidence lines: %+v", x.Evidence)
			}
		}
		if x.ID == "disk-saturated" {
			t.Error("unmet rule should not fire")
		}
	}
	if !found {
		t.Error("swap-spiral should fire")
	}
	if got := EvaluateRules(nil); len(got) != 0 {
		t.Errorf("empty row set should have no findings: %+v", got)
	}
}

func TestRulesRoundTrip(t *testing.T) {
	n := len(Rules)
	data, err := ExportRules()
	if err != nil {
		t.Fatal(err)
	}
	fpath := t.TempDir() + "/r.json"
	os.WriteFile(fpath, data, 0o644)
	if err := LoadRulesFile(fpath); err != nil {
		t.Fatal(err)
	}
	if len(Rules) != n {
		t.Fatalf("rule count mismatch after round-trip: %d != %d", len(Rules), n)
	}
	bad := t.TempDir() + "/bad.json"
	os.WriteFile(bad, []byte(`[{"id":"x","severity":"fatal","conclusion":"c","when":[{"metric":"a.b"}]}]`), 0o644)
	if err := LoadRulesFile(bad); err == nil {
		t.Error("invalid severity should be rejected")
	}
}

func TestPerMetricThreshold(t *testing.T) {
	a := map[string]Value{"network.icmp.inmsgs\x00": {Metric: "network.icmp.inmsgs", Val: 10}}
	b := map[string]Value{"network.icmp.inmsgs\x00": {Metric: "network.icmp.inmsgs", Val: 25}}
	rows := buildRows(a, b, 15)
	if len(rows) != 1 || rows[0].Verdict != VFlat {
		t.Fatalf("icmp +150%% should be flat under its 300%% override: %+v", rows)
	}
	b2 := map[string]Value{"network.icmp.inmsgs\x00": {Metric: "network.icmp.inmsgs", Val: 60}}
	rows = buildRows(a, b2, 15)
	if rows[0].Verdict == VFlat {
		t.Fatalf("icmp +500%% should exceed its override threshold: %+v", rows[0])
	}
}

type flakyRunner struct{ calls int }

func (fr *flakyRunner) Run(_ context.Context, _ string, args ...string) ([]byte, []byte, error) {
	fr.calls++
	for _, a := range args {
		if a == "network.sockstat.tcp.orphan" {
			return nil, []byte("pmrep: Invalid metric network.sockstat.tcp.orphan : Unknown metric name"), fmt.Errorf("exit status 1")
		}
	}
	return []byte(sampleCSV), nil, nil
}

func TestRunTrendSelfHeal(t *testing.T) {
	fr := &flakyRunner{}
	series, missing, err := RunTrend(context.Background(), fr, "/a", "sock", time.Now().Add(-time.Hour), time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if len(missing) != 1 || missing[0] != "network.sockstat.tcp.orphan" {
		t.Fatalf("missing should record the dropped metric: %+v", missing)
	}
	if fr.calls != 2 || len(series) == 0 {
		t.Fatalf("should retry once and return series: calls=%d series=%d", fr.calls, len(series))
	}
}

type allBadRunner struct{}

func (allBadRunner) Run(_ context.Context, _ string, args ...string) ([]byte, []byte, error) {
	last := args[len(args)-1]
	return nil, []byte("pmrep: Invalid metric " + last + " : Unknown metric name"), fmt.Errorf("exit status 1")
}

func TestRunTrendAllMissing(t *testing.T) {
	_, missing, err := RunTrend(context.Background(), allBadRunner{}, "/a", "load", time.Now().Add(-time.Hour), time.Now())
	if err == nil {
		t.Fatal("all metrics missing should error")
	}
	if len(missing) != 1 {
		t.Fatalf("missing=%v", missing)
	}
}

func TestTriage(t *testing.T) {
	rows := []DiffRow{
		{Metric: "kernel.all.cpu.user", Category: "CPU", Verdict: VWorse, DeltaPct: f(520)},
		{Metric: "mem.util.available", Category: "Memory", Verdict: VWorse, DeltaPct: f(-60)},
		// disk has only a non-core metric flagged -> should be warn, not bad
		{Metric: "disk.dev.read", Category: "Disk I/O", Verdict: VWatch, A: f(100), B: f(140), DeltaPct: f(40)},
		// network unchanged -> ok
		{Metric: "network.tcp.insegs", Category: "Network", Verdict: VFlat, DeltaPct: f(2)},
	}
	blocks := Triage(rows)
	got := map[string]TriageStatus{}
	for _, b := range blocks {
		got[b.Key] = b.Status
	}
	if got["cpu"] != TriageBad {
		t.Errorf("CPU core metric regression should be bad, got %s", got["cpu"])
	}
	if got["mem"] != TriageBad {
		t.Errorf("memory core metric regression should be bad, got %s", got["mem"])
	}
	if got["disk"] != TriageWarn {
		t.Errorf("disk with only non-core watch should be warn, got %s", got["disk"])
	}
	if got["net"] != TriageOK {
		t.Errorf("network unchanged should be ok, got %s", got["net"])
	}
}

func TestTriageIgnoresNonCoreAppeared(t *testing.T) {
	// A metric appearing with no prior baseline (a==nil) and no core
	// significance is a collection artifact, not a signal -- it must not
	// win the block's headline. Real-world case: kernel.all.entropy.avail
	// showing up for the first time should not make the CPU card yellow.
	rows := []DiffRow{
		{Metric: "kernel.all.entropy.avail", Category: "CPU", Verdict: VWatch, A: nil, B: f(256)},
	}
	blocks := Triage(rows)
	for _, b := range blocks {
		if b.Key == "cpu" && b.Status != TriageOK {
			t.Errorf("a non-core appeared metric should not flip CPU to %s: %+v", b.Status, b)
		}
	}

	// but an appearing CORE metric (e.g. OOM kill) must still be surfaced
	rows2 := []DiffRow{
		{Metric: "mem.vmstat.oom_kill", Category: "Memory", Verdict: VWatch, A: nil, B: f(3)},
	}
	blocks2 := Triage(rows2)
	found := false
	for _, b := range blocks2 {
		if b.Key == "mem" && b.Status == TriageWarn {
			found = true
		}
	}
	if !found {
		t.Error("a core metric appearing (e.g. oom_kill) should still be flagged")
	}
}

// TestJudgeAbsoluteFloor covers the real-world case that motivated the
// absolute floor: on a nearly idle machine, tiny movements produce huge
// percentages and light the whole report red.
func TestJudgeAbsoluteFloor(t *testing.T) {
	cases := []struct {
		name           string
		a, b           float64
		minAbs         float64
		wantV          Verdict
	}{
		// Reported from a live host: CPU PSI 0.005 -> 0.097 rendered as
		// "+1840%" and turned the CPU triage card red on an idle box.
		{"PSI micro-movement below floor stays flat", 0.005, 0.097, 1, VFlat},
		// user CPU in ms/s: 0.006 -> 0.019 is "+216%" of nothing.
		{"cpu ms/s micro-movement stays flat", 0.006, 0.019, 10, VFlat},
		// load average 0.001 -> 0.015 was reported as "+1400%".
		{"load micro-movement stays flat", 0.001, 0.015, 0.5, VFlat},
		// dirty pages 258KB -> 427KB on a multi-GB box is not a signal.
		{"dirty pages below floor stays flat", 258.6, 427.5, 51200, VFlat},
		// But real load must still be caught: half a core -> three cores.
		{"real cpu load still worse", 500, 3000, 10, VWorse},
		// And a genuine PSI stall must still register.
		{"real PSI stall still worse", 2, 40, 1, VWorse},
		// One side above the floor is enough to judge normally.
		{"crossing the floor upward is judged", 0.5, 80, 10, VWorse},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, _, v := judge(&c.a, &c.b, WorseUp, 15, c.minAbs)
			if v != c.wantV {
				t.Errorf("judge(%v -> %v, minAbs=%v) = %v, want %v",
					c.a, c.b, c.minAbs, v, c.wantV)
			}
		})
	}
}

// TestCatalogMinAbsWired verifies the defaults actually reach the catalog,
// so the floor is live without any user configuration.
func TestCatalogMinAbsWired(t *testing.T) {
	for _, m := range []string{
		"kernel.all.cpu.user",
		"kernel.all.load",
		"kernel.all.pressure.cpu.some.avg",
		"mem.util.dirty",
		"network.tcp.insegs",
	} {
		info, ok := Lookup(m)
		if !ok {
			t.Fatalf("%s missing from catalog", m)
		}
		if info.MinAbs <= 0 {
			t.Errorf("%s has no absolute floor set (MinAbs=%v)", m, info.MinAbs)
		}
	}
}

// TestParseTrendTimeFormats guards the timestamp formats pmrep emits
// across versions. A format we fail to parse used to be dropped silently,
// producing an empty chart that looked like "no data in this window".
func TestParseTrendTimeFormats(t *testing.T) {
	for _, v := range []string{
		"2026-07-25 21:53:00",
		"2026-07-25 21:53:00.000",
		"2026-07-25 21:53:00.123456",
		"2026-07-25T21:53:00",
		"2026-07-25T21:53:00Z",
		"1784984049",
		"1784984049000",
	} {
		if _, ok := parseTrendTime(v); !ok {
			t.Errorf("failed to parse timestamp %q", v)
		}
	}
	if _, ok := parseTrendTime("not a time"); ok {
		t.Error("garbage should not parse")
	}
}

// An output whose timestamps we cannot read must report that, not return
// an empty series that renders as a blank chart.
func TestParseTrendCSVReportsUnreadableTimestamps(t *testing.T) {
	csv := "Time,kernel.all.cpu.user\nBOGUS-TS,0.02\nALSO-BOGUS,0.03\n"
	_, err := ParseTrendCSV(strings.NewReader(csv))
	if err == nil {
		t.Fatal("expected an error when no timestamp could be parsed")
	}
	if !strings.Contains(err.Error(), "timestamps") {
		t.Errorf("error should name the cause, got %v", err)
	}
}

// TestIdleDiskNotSaturated replays the exact figures from a live host that
// produced a false CRITICAL "a disk is saturated" verdict. avactive is the
// fraction of time the device was busy (0.002 == 0.2%) and aveq is the
// average queue depth (0.005 == essentially nothing queued). The disk was
// completely idle; the percentage change was large only because the
// denominator was near zero.
func TestIdleDiskNotSaturated(t *testing.T) {
	a := map[string]Value{
		"disk.dev.avactive\x00sda": {Metric: "disk.dev.avactive", Instance: "sda", Val: 0.001},
		"disk.dev.aveq\x00sda":     {Metric: "disk.dev.aveq", Instance: "sda", Val: 0.003},
		"disk.all.avactive\x00":    {Metric: "disk.all.avactive", Val: 0.001},
		"disk.all.aveq\x00":        {Metric: "disk.all.aveq", Val: 0.003},
		"kernel.all.pswitch\x00":   {Metric: "kernel.all.pswitch", Val: 870.9},
		"network.sockstat.tcp.tw\x00": {Metric: "network.sockstat.tcp.tw", Val: 42.69},
		"vfs.dentry.count\x00":     {Metric: "vfs.dentry.count", Val: 203200},
		"network.tcp.outrsts\x00":  {Metric: "network.tcp.outrsts", Val: 0.572},
	}
	b := map[string]Value{
		"disk.dev.avactive\x00sda": {Metric: "disk.dev.avactive", Instance: "sda", Val: 0.002},
		"disk.dev.aveq\x00sda":     {Metric: "disk.dev.aveq", Instance: "sda", Val: 0.005},
		"disk.all.avactive\x00":    {Metric: "disk.all.avactive", Val: 0.002},
		"disk.all.aveq\x00":        {Metric: "disk.all.aveq", Val: 0.005},
		"kernel.all.pswitch\x00":   {Metric: "kernel.all.pswitch", Val: 1706.3},
		"network.sockstat.tcp.tw\x00": {Metric: "network.sockstat.tcp.tw", Val: 140.6},
		"vfs.dentry.count\x00":     {Metric: "vfs.dentry.count", Val: 246624},
		"network.tcp.outrsts\x00":  {Metric: "network.tcp.outrsts", Val: 1.654},
	}

	rows := buildRows(a, b, 15)
	for _, r := range rows {
		if r.Verdict != VFlat {
			name := r.Metric
			if r.Instance != "" {
				name += "[" + r.Instance + "]"
			}
			t.Errorf("%s on an idle machine should be flat, got %s (%v -> %v)",
				name, r.Verdict, deref(r.A), deref(r.B))
		}
	}

	// and the rule engine must stay silent
	for _, f := range EvaluateRules(rows) {
		t.Errorf("no rule should fire on an idle machine, got %q: %s", f.ID, f.Conclusion)
	}
}

// The same rule must still fire on a genuinely saturated disk, so the
// floors suppress noise without suppressing real trouble.
func TestBusyDiskStillSaturated(t *testing.T) {
	a := map[string]Value{
		"disk.dev.avactive\x00sda": {Metric: "disk.dev.avactive", Instance: "sda", Val: 0.2},
		"disk.dev.aveq\x00sda":     {Metric: "disk.dev.aveq", Instance: "sda", Val: 1.2},
	}
	b := map[string]Value{
		"disk.dev.avactive\x00sda": {Metric: "disk.dev.avactive", Instance: "sda", Val: 0.94},
		"disk.dev.aveq\x00sda":     {Metric: "disk.dev.aveq", Instance: "sda", Val: 18.5},
	}
	rows := buildRows(a, b, 15)
	fired := false
	for _, f := range EvaluateRules(rows) {
		if f.ID == "disk-saturated" {
			fired = true
			t.Logf("correctly fired: %s", f.Conclusion)
		}
	}
	if !fired {
		t.Error("a disk at 94% busy with a queue of 18 must still be reported as saturated")
	}
}

func deref(f *float64) float64 {
	if f == nil {
		return 0
	}
	return *f
}

func TestAbsentMetricLearning(t *testing.T) {
	ResetAbsent()
	defer ResetAbsent()

	before := len(DiffMetrics())
	learned := NoteAbsent([]string{
		"pmlogsummary: PMNS traversal failed for mem.vmstat.oom_kill: Unknown metric name",
		"pmlogsummary: PMNS traversal failed for network.tcp.listendrops: Unknown metric name",
		"pmrep: Invalid metric network.tcpconn.established : Unknown metric name",
	})
	if len(learned) != 3 {
		t.Fatalf("should have learned 3 absent metrics, got %v", learned)
	}
	if !IsAbsent("mem.vmstat.oom_kill") || !IsAbsent("network.tcpconn.established") {
		t.Error("learned metrics should report as absent")
	}
	after := len(DiffMetrics())
	if after != before-3 {
		t.Errorf("request list should shrink by 3: %d -> %d", before, after)
	}
	// re-noting the same warnings must not double-count
	if again := NoteAbsent([]string{"pmlogsummary: PMNS traversal failed for mem.vmstat.oom_kill: Unknown metric name"}); len(again) != 0 {
		t.Errorf("already-known metric should not be relearned: %v", again)
	}
	// and a metric that is present must be untouched
	if IsAbsent("kernel.all.cpu.user") {
		t.Error("a present metric must not be marked absent")
	}
}
