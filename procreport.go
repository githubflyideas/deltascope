package main

import (
	"fmt"
	"io"
	"text/tabwriter"

	"github.com/githubflyideas/deltascope/internal/pcp"
)

const (
	cRed    = "\x1b[31m"
	cGreen  = "\x1b[32m"
	cViolet = "\x1b[35m"
	cGray   = "\x1b[90m"
	cBold   = "\x1b[1m"
)

func renderProcReport(w io.Writer, rep *pcp.ProcReport, color bool) {
	c := func(code, s string) string {
		if !color {
			return s
		}
		return code + s + "\x1b[0m"
	}

	fmt.Fprintf(w, "%s\n", c(cBold, "deltascope proc-diff"))
	fmt.Fprintf(w, "  A %s ~ %s\n  B %s ~ %s  (threshold %.0f%%)\n\n",
		rep.AStart.Format("01-02 15:04"), rep.AEnd.Format("15:04"),
		rep.BStart.Format("01-02 15:04"), rep.BEnd.Format("15:04"), rep.ThresholdPct)

	if len(rep.Restarts) > 0 {
		fmt.Fprintf(w, "%s\n", c(cViolet+cBold, "⟳ processes restarted during this window"))
		for _, r := range rep.Restarts {
			fmt.Fprintf(w, "  %s  %s\n", r.Name, c(cViolet, pcp.FormatStartDelta(r.StartA, r.StartB)))
		}
		fmt.Fprintln(w)
	}

	renderProcSection(w, c, "Process CPU accounting (higher = worse)", rep.CPURows)
	renderProcSection(w, c, "Process memory accounting", rep.MemRows)

	if len(rep.Warnings) > 0 {
		fmt.Fprintf(w, "%s\n", c(cGray, "PCP notes:"))
		for _, warn := range rep.Warnings {
			fmt.Fprintf(w, "  %s\n", c(cGray, warn))
		}
	}
}

func renderProcSection(w io.Writer, c func(string, string) string, title string, rows []pcp.ProcRow) {
	shown := 0
	for _, r := range rows {
		if r.Verdict != pcp.PVFlat {
			shown++
		}
	}
	fmt.Fprintf(w, "%s  (%d changed)\n", c(cBold, "== "+title+" =="), shown)
	if shown == 0 {
		fmt.Fprintln(w, c(cGray, "  no significant change"))
		fmt.Fprintln(w)
		return
	}
	tw := tabwriter.NewWriter(w, 2, 4, 2, ' ', 0)
	for _, r := range rows {
		if r.Verdict == pcp.PVFlat {
			continue
		}
		var col, verdict, delta string
		switch r.Verdict {
		case pcp.PVWorse:
			col, verdict = cRed, "worse"
		case pcp.PVBetter:
			col, verdict = cGreen, "better"
		case pcp.PVAppeared:
			col, verdict = cViolet, "appeared"
		case pcp.PVGone:
			col, verdict = cGray, "gone"
		}
		switch {
		case r.Verdict == pcp.PVAppeared:
			delta = "⊕"
		case r.Verdict == pcp.PVGone:
			delta = "⊖"
		case r.DeltaPct == nil:
			delta = "∞"
		default:
			delta = fmt.Sprintf("%+.1f%%", *r.DeltaPct)
		}
		mark := ""
		if r.Restarted {
			mark = " ⟳"
		}
		line := fmt.Sprintf("  %s%s\t%s\t%s\t%s\t%s\t%s",
			r.Name, mark, fmtVal(r.A), fmtVal(r.B), delta, verdict, r.Unit)
		fmt.Fprintln(tw, c(col, line))
	}
	tw.Flush()
	fmt.Fprintln(w)
}
