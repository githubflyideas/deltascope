package pcp

import (
	"context"
	"math"
	"sort"
	"time"
)

type Verdict string

const (
	VWorse  Verdict = "worse"
	VBetter Verdict = "better"
	VWatch  Verdict = "watch"
	VFlat   Verdict = "flat"
)

type DiffRow struct {
	Metric   string   `json:"metric"`
	Instance string   `json:"instance"`
	Label    string   `json:"label"`
	Category string   `json:"category"`
	Units    string   `json:"units"`
	A        *float64 `json:"a"`
	B        *float64 `json:"b"`
	DeltaPct *float64 `json:"delta_pct"`
	Verdict  Verdict  `json:"verdict"`
	Exceeded bool     `json:"exceeded"`
}

type DiffReport struct {
	Window   Windows   `json:"window"`
	Rows     []DiffRow `json:"rows"`
	Findings []Finding     `json:"findings"`
	Triage   []TriageBlock `json:"triage"`
	Warnings []string      `json:"warnings"`
}

type Windows struct {
	AStart       time.Time `json:"a_start"`
	AEnd         time.Time `json:"a_end"`
	BStart       time.Time `json:"b_start"`
	BEnd         time.Time `json:"b_end"`
	ThresholdPct float64   `json:"threshold_pct"`
}

func Compare(ctx context.Context, r Runner, archive string, w Windows) (*DiffReport, error) {
	metrics := DiffMetrics()

	a, warnA, err := RunSummary(ctx, r, archive, w.AStart, w.AEnd, metrics)
	if err != nil {
		return nil, err
	}
	b, warnB, err := RunSummary(ctx, r, archive, w.BStart, w.BEnd, metrics)
	if err != nil {
		return nil, err
	}

	report := &DiffReport{Window: w, Warnings: dedupe(append(warnA, warnB...))}
	report.Rows = buildRows(a, b, w.ThresholdPct)
	report.Findings = EvaluateRules(report.Rows)
	report.Triage = Triage(report.Rows)
	return report, nil
}

func buildRows(a, b map[string]Value, thresholdPct float64) []DiffRow {
	keys := map[string]Value{}
	for k, v := range a {
		keys[k] = v
	}
	for k, v := range b {
		keys[k] = v
	}

	rows := make([]DiffRow, 0, len(keys))
	for k, meta := range keys {
		info, ok := Lookup(meta.Metric)
		if !ok {
			continue
		}
		row := DiffRow{
			Metric:   meta.Metric,
			Instance: meta.Instance,
			Label:    info.Label,
			Category: info.Category,
			Units:    meta.Units,
		}
		va, okA := a[k]
		vb, okB := b[k]
		if okA {
			x := va.Val
			row.A = &x
		}
		if okB {
			x := vb.Val
			row.B = &x
		}
		eff := thresholdPct
		if info.ThresholdPct > 0 {
			eff = info.ThresholdPct
		}
		row.DeltaPct, row.Exceeded, row.Verdict = judge(row.A, row.B, info.Polarity, eff)
		rows = append(rows, row)
	}

	catOrder := map[string]int{}
	for i, c := range Categories {
		catOrder[c] = i
	}
	sort.Slice(rows, func(i, j int) bool {
		ci, cj := catOrder[rows[i].Category], catOrder[rows[j].Category]
		if ci != cj {
			return ci < cj
		}
		oi, oj := OrderIndex(rows[i].Metric), OrderIndex(rows[j].Metric)
		if oi != oj {
			return oi < oj
		}
		return rows[i].Instance < rows[j].Instance
	})
	return rows
}

func judge(a, b *float64, pol Polarity, thresholdPct float64) (*float64, bool, Verdict) {
	if a == nil || b == nil {
		return nil, true, VWatch
	}
	av, bv := *a, *b
	if av == 0 {
		if bv == 0 {
			z := 0.0
			return &z, false, VFlat
		}
		return nil, true, verdictFor(bv > 0, pol)
	}
	d := (bv - av) / math.Abs(av) * 100
	exceeded := math.Abs(d) >= thresholdPct
	if !exceeded {
		return &d, false, VFlat
	}
	return &d, true, verdictFor(d > 0, pol)
}

func verdictFor(up bool, pol Polarity) Verdict {
	switch pol {
	case WorseUp:
		if up {
			return VWorse
		}
		return VBetter
	case BetterUp:
		if up {
			return VBetter
		}
		return VWorse
	default:
		return VWatch
	}
}

func dedupe(in []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, s := range in {
		if !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	return out
}
