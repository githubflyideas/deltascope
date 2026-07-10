package pcp

import (
	"context"
	"math"
	"sort"
	"time"
)

// Verdict 是单行对比的结论。
type Verdict string

const (
	VWorse  Verdict = "worse"  // 🔴 恶化
	VBetter Verdict = "better" // 🟢 改善
	VWatch  Verdict = "watch"  // 🟡 中性指标显著变化,提示关注
	VFlat   Verdict = "flat"   // 变化未超阈值
)

// DiffRow 是体检报告表中的一行。
type DiffRow struct {
	Metric   string   `json:"metric"`
	Instance string   `json:"instance"`
	Label    string   `json:"label"`
	Category string   `json:"category"`
	Units    string   `json:"units"`
	A        *float64 `json:"a"` // 基线均值,窗口内无数据为 null
	B        *float64 `json:"b"` // 对比均值
	DeltaPct *float64 `json:"delta_pct"` // (B-A)/A*100;A=0 且 B≠0 时为 null(前端显示 ∞/new)
	Verdict  Verdict  `json:"verdict"`
	Exceeded bool     `json:"exceeded"` // |Δ| 是否达到阈值
}

// DiffReport 是 /api/diff 的完整响应体。
type DiffReport struct {
	Window   Windows   `json:"window"`
	Rows     []DiffRow `json:"rows"`
	Warnings []string  `json:"warnings"`
}

type Windows struct {
	AStart time.Time `json:"a_start"`
	AEnd   time.Time `json:"a_end"`
	BStart time.Time `json:"b_start"`
	BEnd   time.Time `json:"b_end"`
	// ThresholdPct 为判定阈值(百分比),需求默认 15。
	ThresholdPct float64 `json:"threshold_pct"`
}

// Compare 对两个时间窗各跑一次 pmlogsummary,对齐后产出报告。
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
	return report, nil
}

// buildRows 是纯函数,便于单测:对齐 A/B 两侧的 指标+实例,计算 Δ 与结论。
func buildRows(a, b map[string]Value, thresholdPct float64) []DiffRow {
	keys := map[string]Value{} // key → 任意一侧的元信息(指标名/实例/单位)
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
			continue // 不在白名单内的输出行直接丢弃
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
		row.DeltaPct, row.Exceeded, row.Verdict = judge(row.A, row.B, info.Polarity, thresholdPct)
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
		// 同类内按 |Δ| 降序,超阈值优先;∞(nil 且两侧存在)视为最大。
		di, dj := absDelta(rows[i]), absDelta(rows[j])
		if di != dj {
			return di > dj
		}
		if rows[i].Metric != rows[j].Metric {
			return rows[i].Metric < rows[j].Metric
		}
		return rows[i].Instance < rows[j].Instance
	})
	return rows
}

func absDelta(r DiffRow) float64 {
	if r.DeltaPct != nil {
		return math.Abs(*r.DeltaPct)
	}
	if r.A != nil && r.B != nil {
		return 0
	}
	return math.Inf(1) // 单侧缺失或 0→非0,排最前提示关注
}

// judge 计算 Δ% 并按极性给出结论。
func judge(a, b *float64, pol Polarity, thresholdPct float64) (*float64, bool, Verdict) {
	// 单侧无数据:无法计算比率,标记关注。
	if a == nil || b == nil {
		return nil, true, VWatch
	}
	av, bv := *a, *b
	if av == 0 {
		if bv == 0 {
			z := 0.0
			return &z, false, VFlat
		}
		// 0 → 非 0:比率为 ∞,按方向直接判定。
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
