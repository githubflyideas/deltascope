package pcp

import (
	"context"
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"
	"time"
)

// ProcMetrics are the hotproc metric names process accounting depends on.
// The collector side must record these in pmlogger (see hotproc config).
var ProcMetrics = []string{
	"hotproc.psinfo.utime",      // cumulative user CPU (ms)
	"hotproc.psinfo.stime",      // cumulative system CPU (ms)
	"hotproc.psinfo.rss",        // resident memory (KB)
	"hotproc.psinfo.start_time", // process start time (ms since epoch)
}

// ProcSample is one process's aggregated values within a window.
type ProcSample struct {
	Name      string
	CPUms     float64 // window mean of utime+stime (ms/s semantics)
	RSS       float64 // resident memory KB
	StartTime float64 // start time (ms epoch), used for restart detection
	Count     int     // number of instances aggregated under this name
}

// ProcVerdict describes the verdict for one process-accounting row.
type ProcVerdict string

const (
	PVWorse    ProcVerdict = "worse"
	PVBetter   ProcVerdict = "better"
	PVFlat     ProcVerdict = "flat"
	PVAppeared ProcVerdict = "appeared"
	PVGone     ProcVerdict = "gone"
)

// ProcRow is one row of the process-accounting report.
type ProcRow struct {
	Name      string
	Metric    string // "cpu" | "mem"
	A, B      *float64
	DeltaPct  *float64
	Verdict   ProcVerdict
	Restarted bool
	StartA    float64
	StartB    float64
	Unit      string
}

// ProcReport is the full result of process accounting.
type ProcReport struct {
	AStart, AEnd time.Time
	BStart, BEnd time.Time
	ThresholdPct float64
	CPURows      []ProcRow
	MemRows      []ProcRow
	Restarts     []ProcRow
	Warnings     []string
}

// parseProcSummary parses hotproc metrics from pmlogsummary output and
// aggregates by process name. hotproc instance names look like
// "1234 mysqld" - the command name after the space is the aggregation key.
func parseProcSummary(vals []Value) map[string]*ProcSample {
	acc := map[string]*ProcSample{}
	get := func(name string) *ProcSample {
		if acc[name] == nil {
			acc[name] = &ProcSample{Name: name}
		}
		return acc[name]
	}
	for _, v := range vals {
		name := procName(v.Instance)
		if name == "" {
			continue
		}
		s := get(name)
		switch v.Metric {
		case "hotproc.psinfo.utime", "hotproc.psinfo.stime":
			s.CPUms += v.Val
		case "hotproc.psinfo.rss":
			if v.Val > s.RSS {
				s.RSS = v.Val
			}
		case "hotproc.psinfo.start_time":
			if v.Val > s.StartTime {
				s.StartTime = v.Val
			}
		}
		s.Count++
	}
	return acc
}

// procName extracts the command name from a hotproc instance name.
// Typical formats: "01234 mysqld" or "1234 /usr/sbin/nginx".
func procName(inst string) string {
	inst = strings.TrimSpace(inst)
	if inst == "" {
		return ""
	}
	parts := strings.Fields(inst)
	if len(parts) < 2 {
		if _, err := strconv.Atoi(parts[0]); err == nil {
			return ""
		}
		return parts[0]
	}
	cmd := parts[1]
	if i := strings.LastIndexByte(cmd, '/'); i >= 0 {
		cmd = cmd[i+1:]
	}
	return cmd
}

// CompareProc runs process-level accounting between two windows.
func CompareProc(ctx context.Context, r Runner, archive string, w Windows) (*ProcReport, error) {
	a, warnA, err := RunSummary(ctx, r, archive, w.AStart, w.AEnd, ProcMetrics)
	if err != nil {
		return nil, err
	}
	b, warnB, err := RunSummary(ctx, r, archive, w.BStart, w.BEnd, ProcMetrics)
	if err != nil {
		return nil, err
	}

	pa := parseProcSummary(valsOf(a))
	pb := parseProcSummary(valsOf(b))

	rep := &ProcReport{
		AStart: w.AStart, AEnd: w.AEnd, BStart: w.BStart, BEnd: w.BEnd,
		ThresholdPct: w.ThresholdPct,
		Warnings:     dedupe(append(warnA, warnB...)),
	}

	names := map[string]bool{}
	for n := range pa {
		names[n] = true
	}
	for n := range pb {
		names[n] = true
	}

	for name := range names {
		sa, oka := pa[name]
		sb, okb := pb[name]

		var restarted bool
		var startA, startB float64
		if oka {
			startA = sa.StartTime
		}
		if okb {
			startB = sb.StartTime
		}
		if oka && okb && startA > 0 && startB-startA > 60000 {
			restarted = true
		}

		cpuRow := procRow(name, "cpu", "ms/s", ptrOf(sa, oka, func(s *ProcSample) float64 { return s.CPUms }),
			ptrOf(sb, okb, func(s *ProcSample) float64 { return s.CPUms }), w.ThresholdPct)
		cpuRow.Restarted = restarted
		cpuRow.StartA, cpuRow.StartB = startA, startB
		rep.CPURows = append(rep.CPURows, cpuRow)

		memRow := procRow(name, "mem", "KB", ptrOf(sa, oka, func(s *ProcSample) float64 { return s.RSS }),
			ptrOf(sb, okb, func(s *ProcSample) float64 { return s.RSS }), w.ThresholdPct)
		memRow.Restarted = restarted
		memRow.StartA, memRow.StartB = startA, startB
		rep.MemRows = append(rep.MemRows, memRow)

		if restarted {
			rep.Restarts = append(rep.Restarts, cpuRow)
		}
	}

	sortProcRows(rep.CPURows)
	sortProcRows(rep.MemRows)
	sort.Slice(rep.Restarts, func(i, j int) bool { return rep.Restarts[i].Name < rep.Restarts[j].Name })
	return rep, nil
}

func procRow(name, metric, unit string, a, b *float64, threshold float64) ProcRow {
	row := ProcRow{Name: name, Metric: metric, A: a, B: b, Unit: unit}
	row.DeltaPct, _, row.Verdict = judgeProc(a, b, threshold)
	return row
}

// judgeProc: process metrics are always worse_up semantics (higher usage = worse).
func judgeProc(a, b *float64, threshold float64) (*float64, bool, ProcVerdict) {
	if a == nil && b != nil {
		return nil, true, PVAppeared
	}
	if a != nil && b == nil {
		return nil, true, PVGone
	}
	if a == nil || b == nil {
		return nil, false, PVFlat
	}
	av, bv := *a, *b
	if av == 0 {
		if bv == 0 {
			z := 0.0
			return &z, false, PVFlat
		}
		return nil, true, PVWorse
	}
	d := (bv - av) / math.Abs(av) * 100
	if math.Abs(d) < threshold {
		return &d, false, PVFlat
	}
	if d > 0 {
		return &d, true, PVWorse
	}
	return &d, true, PVBetter
}

func sortProcRows(rows []ProcRow) {
	rank := map[ProcVerdict]int{PVWorse: 0, PVAppeared: 1, PVGone: 2, PVBetter: 3, PVFlat: 4}
	sort.Slice(rows, func(i, j int) bool {
		ri, rj := rank[rows[i].Verdict], rank[rows[j].Verdict]
		if ri != rj {
			return ri < rj
		}
		di, dj := procAbs(rows[i]), procAbs(rows[j])
		if di != dj {
			return di > dj
		}
		return rows[i].Name < rows[j].Name
	})
}

func procAbs(r ProcRow) float64 {
	if r.DeltaPct != nil {
		return math.Abs(*r.DeltaPct)
	}
	if r.A == nil || r.B == nil {
		return math.Inf(1)
	}
	return 0
}

func ptrOf(s *ProcSample, ok bool, f func(*ProcSample) float64) *float64 {
	if !ok {
		return nil
	}
	v := f(s)
	return &v
}

func valsOf(m map[string]Value) []Value {
	out := make([]Value, 0, len(m))
	for _, v := range m {
		out = append(out, v)
	}
	return out
}

// FormatStartDelta renders two start times (ms epoch) as "20d ago -> 2h ago".
func FormatStartDelta(startA, startB float64) string {
	now := float64(time.Now().UnixMilli())
	return fmt.Sprintf("%s → %s", agoText(now-startA), agoText(now-startB))
}

func agoText(ms float64) string {
	d := time.Duration(ms) * time.Millisecond
	switch {
	case d < time.Minute:
		return "just now"
	case d < time.Hour:
		return fmt.Sprintf("%dm ago", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh ago", int(d.Hours()))
	default:
		return fmt.Sprintf("%dd ago", int(d.Hours()/24))
	}
}
