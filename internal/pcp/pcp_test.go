package pcp

import (
	"context"
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
		t.Fatalf("期望 9 行, 得到 %d: %+v", len(vals), vals)
	}
	byKey := map[string]Value{}
	for _, v := range vals {
		byKey[v.Key()] = v
	}
	load1 := byKey["kernel.all.load\x001 minute"]
	if load1.Val != 1.542 || load1.Units != "none" {
		t.Errorf("load[1 minute] 解析错误: %+v", load1)
	}
	cpu := byKey["kernel.all.cpu.user\x00"]
	if cpu.Val != 245.917 || cpu.Units != "millisec / second" {
		t.Errorf("cpu.user 解析错误: %+v", cpu)
	}
	eth0 := byKey["network.interface.in.bytes\x00eth0"]
	if eth0.Val != 10240.5 {
		t.Errorf("in.bytes[eth0] 解析错误: %+v", eth0)
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
		{"CPU 上涨 500% 恶化", f(100), f(600), WorseUp, VWorse, true, f(500)},
		{"CPU 下降改善", f(100), f(60), WorseUp, VBetter, true, f(-40)},
		{"可用内存下降恶化", f(1000), f(500), BetterUp, VWorse, true, f(-50)},
		{"可用内存上升改善", f(1000), f(1300), BetterUp, VBetter, true, nil},
		{"吞吐变化标关注", f(100), f(200), Neutral, VWatch, true, nil},
		{"低于阈值平稳", f(100), f(110), WorseUp, VFlat, false, f(10)},
		{"0 到 0 平稳", f(0), f(0), WorseUp, VFlat, false, f(0)},
		{"0 到 非0 恶化(∞)", f(0), f(5), WorseUp, VWorse, true, nil},
		{"单侧缺失标关注", nil, f(5), WorseUp, VWatch, true, nil},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			d, exc, v := judge(c.a, c.b, c.pol, 15)
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
		t.Fatalf("期望调用两次 pmlogsummary: %+v", fr.args)
	}
	got := map[string]DiffRow{}
	for _, r := range rep.Rows {
		got[r.Metric] = r
	}
	if r := got["kernel.all.cpu.user"]; r.Verdict != VWorse || !r.Exceeded || math.Abs(*r.DeltaPct-500) > 1e-9 {
		t.Errorf("cpu.user 判定错误: %+v", r)
	}
	if r := got["mem.util.available"]; r.Verdict != VWorse || math.Abs(*r.DeltaPct+50) > 1e-9 {
		t.Errorf("mem.available 判定错误: %+v", r)
	}
	if r := got["network.tcp.retranssegs"]; r.Verdict != VFlat || r.Exceeded {
		t.Errorf("retranssegs 应为 flat: %+v", r)
	}
	if rep.Rows[0].Metric != "kernel.all.cpu.user" {
		t.Errorf("排序错误, 首行=%s", rep.Rows[0].Metric)
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
		t.Fatalf("期望 2 条序列, 得到 %d", len(series))
	}
	if series[0].Name != `kernel.all.load-1 minute` {
		t.Errorf("序列名错误: %q", series[0].Name)
	}
	if len(series[0].Points) != 3 {
		t.Fatalf("期望 3 个点, 得到 %d", len(series[0].Points))
	}
	if series[0].Points[1][1] != nil {
		t.Errorf("缺失值应为 nil: %+v", series[0].Points[1])
	}
	if v, ok := series[0].Points[2][1].(float64); !ok || v != 1.7 {
		t.Errorf("数值解析错误: %+v", series[0].Points[2])
	}
}

func TestTrendStep(t *testing.T) {
	now := time.Now()
	if s := TrendStep(now.Add(-time.Hour), now); s != 10*time.Second {
		t.Errorf("1h 窗口步长应为 10s, got %v", s)
	}
	if s := TrendStep(now.Add(-14*24*time.Hour), now); s != 15*time.Minute {
		t.Errorf("14d 窗口步长应封顶 15m, got %v", s)
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
		t.Fatalf("往返后指标数不一致: %d != %d", len(Catalog), before)
	}
	if _, ok := Lookup("network.tcp.syncookiessent"); !ok {
		t.Error("加载后索引未重建")
	}
	bad := t.TempDir() + "/bad.json"
	os.WriteFile(bad, []byte(`{"categories":["x"],"metrics":[{"metric":"a;rm -rf","label":"l","category":"x","polarity":"neutral"}]}`), 0o644)
	if err := LoadCatalogFile(bad); err == nil {
		t.Error("非法指标名应被拒绝")
	}
	os.WriteFile(bad, []byte(`{"categories":["x"],"metrics":[{"metric":"a.b","label":"l","category":"x","polarity":"up"}]}`), 0o644)
	if err := LoadCatalogFile(bad); err == nil {
		t.Error("非法极性应被拒绝")
	}
}
