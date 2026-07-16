package main

import (
	"fmt"
	"io"
	"strings"
	"text/tabwriter"

	"github.com/githubflyideas/deltascope/internal/pcp"
)

type palette struct {
	red, green, yellow, violet, dim, bold, reset string
}

func newPalette(color bool) palette {
	if !color {
		return palette{}
	}
	return palette{
		red: "\x1b[31m", green: "\x1b[32m", yellow: "\x1b[33m",
		violet: "\x1b[35m", dim: "\x1b[2m", bold: "\x1b[1m", reset: "\x1b[0m",
	}
}

func renderText(w io.Writer, rep *pcp.DiffReport, showAll, color bool) {
	c := newPalette(color)

	if len(rep.Findings) == 0 {
		fmt.Fprintf(w, "%s[诊断] 未命中已知诊断模式,请查看下方明细。%s\n\n", c.dim, c.reset)
	}
	for _, f := range rep.Findings {
		tag, tc := "提示", c.dim
		switch f.Severity {
		case "crit":
			tag, tc = "严重", c.red
		case "warn":
			tag, tc = "警告", c.yellow
		}
		fmt.Fprintf(w, "%s%s[%s]%s %s%s%s\n", tc, c.bold, tag, c.reset, c.bold, f.Conclusion, c.reset)
		fmt.Fprintf(w, "  %s依据: %s%s\n", c.dim, strings.Join(f.Evidence, " · "), c.reset)
		if len(f.Next) > 0 {
			fmt.Fprintf(w, "  %s下一步: %s%s\n", c.dim, strings.Join(f.Next, "  |  "), c.reset)
		}
		fmt.Fprintln(w)
	}

	counts := map[string]int{}
	for _, r := range rep.Rows {
		counts[kindOf(r)]++
	}
	win := rep.Window
	fmt.Fprintf(w, "%s恶化 %d%s · %s改善 %d%s · %s关注 %d%s · 平稳 %d",
		c.red, counts["worse"], c.reset, c.green, counts["better"], c.reset,
		c.yellow, counts["watch"], c.reset, counts["flat"])
	if counts["new"] > 0 {
		fmt.Fprintf(w, " · %s新出现 %d%s", c.violet, counts["new"], c.reset)
	}
	if counts["gone"] > 0 {
		fmt.Fprintf(w, " · 消失 %d", counts["gone"])
	}
	fmt.Fprintf(w, "\n[A %s → %s] vs [B %s → %s] · 阈值 %.0f%%\n\n",
		win.AStart.Format("01-02 15:04"), win.AEnd.Format("15:04"),
		win.BStart.Format("01-02 15:04"), win.BEnd.Format("15:04"), win.ThresholdPct)

	cat := ""
	tw := tabwriter.NewWriter(w, 2, 4, 2, ' ', 0)
	flush := func() { tw.Flush(); tw = tabwriter.NewWriter(w, 2, 4, 2, ' ', 0) }
	for _, r := range rep.Rows {
		kind := kindOf(r)
		if !showAll && kind == "flat" {
			continue
		}
		if r.Category != cat {
			flush()
			cat = r.Category
			fmt.Fprintf(w, "%s—— %s ——%s\n", c.dim, cat, c.reset)
		}
		name := r.Label
		if r.Instance != "" {
			name += " [" + r.Instance + "]"
		}
		var mark, delta, verdict, kc string
		switch kind {
		case "worse":
			mark, verdict, kc = "×", "恶化", c.red
		case "better":
			mark, verdict, kc = "+", "改善", c.green
		case "watch":
			mark, verdict, kc = "!", "关注", c.yellow
		case "new":
			mark, verdict, kc = "⊕", "新出现", c.violet
		case "gone":
			mark, verdict, kc = "⊖", "消失", c.dim
		default:
			mark, verdict, kc = "·", "平稳", c.dim
		}
		switch {
		case kind == "new":
			delta = "⊕"
		case kind == "gone":
			delta = "⊖"
		case r.DeltaPct == nil:
			delta = "∞"
		default:
			delta = fmt.Sprintf("%+.1f%%", *r.DeltaPct)
		}
		fmt.Fprintf(tw, "%s%s %s\t%s\t%s\t%s\t%s\t%s%s\n",
			kc, mark, name, fmtVal(r.A), fmtVal(r.B), delta, verdict, r.Units, c.reset)
	}
	flush()
}

func kindOf(r pcp.DiffRow) string {
	if r.A == nil && r.B != nil {
		return "new"
	}
	if r.B == nil && r.A != nil {
		return "gone"
	}
	return string(r.Verdict)
}

func fmtVal(v *float64) string {
	if v == nil {
		return "—"
	}
	x := *v
	switch {
	case x >= 1e9 || x <= -1e9:
		return fmt.Sprintf("%.2fG", x/1e9)
	case x >= 1e6 || x <= -1e6:
		return fmt.Sprintf("%.2fM", x/1e6)
	case x >= 1e4 || x <= -1e4:
		return fmt.Sprintf("%.2fk", x/1e3)
	case x == 0:
		return "0"
	}
	return fmt.Sprintf("%.2f", x)
}

func worstExit(rep *pcp.DiffReport) int {
	for _, f := range rep.Findings {
		if f.Severity == "crit" {
			return 2
		}
	}
	for _, r := range rep.Rows {
		if r.Verdict == pcp.VWorse {
			return 2
		}
	}
	return 0
}
