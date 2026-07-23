package pcp

import (
	"testing"
)

func TestProcName(t *testing.T) {
	cases := map[string]string{
		"1234 mysqld":            "mysqld",
		"01234 /usr/sbin/nginx":  "nginx",
		"999 java":               "java",
		"1234":                   "",
		"":                       "",
	}
	for in, want := range cases {
		if got := procName(in); got != want {
			t.Errorf("procName(%q)=%q want %q", in, got, want)
		}
	}
}

func TestParseProcAggregation(t *testing.T) {
	vals := []Value{
		{Metric: "hotproc.psinfo.utime", Instance: "100 nginx", Val: 30},
		{Metric: "hotproc.psinfo.stime", Instance: "100 nginx", Val: 10},
		{Metric: "hotproc.psinfo.utime", Instance: "101 nginx", Val: 20},
		{Metric: "hotproc.psinfo.rss", Instance: "100 nginx", Val: 50000},
		{Metric: "hotproc.psinfo.rss", Instance: "101 nginx", Val: 80000},
		{Metric: "hotproc.psinfo.utime", Instance: "200 mysqld", Val: 500},
	}
	acc := parseProcSummary(vals)
	if len(acc) != 2 {
		t.Fatalf("should aggregate into 2 process names, got %d", len(acc))
	}
	// nginx: utime 30+20 + stime 10 = 60 CPU, rss takes the max 80000
	if acc["nginx"].CPUms != 60 {
		t.Errorf("nginx CPU aggregation wrong: %v", acc["nginx"].CPUms)
	}
	if acc["nginx"].RSS != 80000 {
		t.Errorf("nginx RSS should take the instance max: %v", acc["nginx"].RSS)
	}
}

func TestJudgeProcRestart(t *testing.T) {
	a := f(500.0)
	b := f(9000.0)
	d, exc, v := judgeProc(a, b, 20)
	if v != PVWorse || !exc || d == nil {
		t.Errorf("large CPU spike should be worse: %v %v", v, exc)
	}
	_, _, v2 := judgeProc(nil, f(100), 20)
	if v2 != PVAppeared {
		t.Errorf("new process should be appeared: %v", v2)
	}
	_, _, v3 := judgeProc(f(100), nil, 20)
	if v3 != PVGone {
		t.Errorf("vanished process should be gone: %v", v3)
	}
}
