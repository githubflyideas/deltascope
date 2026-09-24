package main

import (
	"fmt"
	"io"
	"strings"
	"text/tabwriter"

	"github.com/githubflyideas/deltascope/internal/state"
)

const (
	cRed    = "\x1b[31m"
	cGreen  = "\x1b[32m"
	cViolet = "\x1b[35m"
	cGray   = "\x1b[90m"
	cBold   = "\x1b[1m"
)

func renderProcDiff(w io.Writer, d state.ProcDiff, color bool) {
	c := func(code, s string) string {
		if !color {
			return s
		}
		return code + s + "\x1b[0m"
	}

	fmt.Fprintf(w, "%s\n", c(cBold, "deltascope proc-diff"))
	fmt.Fprintf(w, "  A %s ~ %s\n  B %s ~ %s\n\n",
		d.AStart.Local().Format("01-02 15:04"), d.AEnd.Local().Format("15:04"),
		d.BStart.Local().Format("01-02 15:04"), d.BEnd.Local().Format("15:04"))

	if d.Note != "" {
		fmt.Fprintln(w, c(cGray, d.Note))
		return
	}

	if len(d.Restarts) > 0 {
		fmt.Fprintf(w, "%s\n", c(cViolet+cBold, "\u27f3 restarted during this window"))
		for _, r := range d.Restarts {
			fmt.Fprintf(w, "  %s\n", r.Name)
		}
		fmt.Fprintln(w)
	}

	shown := 0
	for _, r := range d.Rows {
		if r.Verdict != state.PVFlat {
			shown++
		}
	}
	fmt.Fprintf(w, "%s  (%d changed of %d tracked)\n", c(cBold, "== Process accounting =="), shown, len(d.Rows))
	if shown == 0 {
		fmt.Fprintln(w, c(cGray, "  no significant change"))
		// Still say it. "No significant change" next to a change report that
		// just named a new listening port is the contradiction this note
		// exists to prevent, and it is at its worst on exactly this path.
		unwatchedNote(w, c, d)
		return
	}

	tw := tabwriter.NewWriter(w, 2, 4, 2, ' ', 0)
	fmt.Fprintln(tw, "  PROCESS\tCPU A\tCPU B\tΔCPU\tMEM A\tMEM B\tΔMEM\tVERDICT")
	for _, r := range d.Rows {
		if r.Verdict == state.PVFlat {
			continue
		}
		var col, verdict string
		switch r.Verdict {
		case state.PVWorse:
			col, verdict = cRed, "worse"
		case state.PVBetter:
			col, verdict = cGreen, "better"
		case state.PVAppeared:
			col, verdict = cViolet, "appeared"
		case state.PVGone:
			col, verdict = cGray, "gone"
		}
		mark := ""
		if r.Restarted {
			mark = " \u27f3"
		}
		// The listening marker earns a row its place. A 12 MB process with no
		// measurable CPU is in this table because it holds a port, and without
		// the marker the reader is left wondering what it is doing here.
		if r.Listens {
			mark += " \u25cf"
		}
		line := fmt.Sprintf("  %s%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s",
			r.Name, mark,
			pct(r.CPUPctA), pctApprox(r.CPUPctB, r.CPUApproxB), deltaFrom(r.CPUDelta, r.FromZero),
			mem(r.RSSKBA), mem(r.RSSKBB), deltaFrom(r.RSSDelta, r.FromZero),
			verdict)
		fmt.Fprintln(tw, c(col, line))
	}
	tw.Flush()

	if listensShown(d.Rows) {
		fmt.Fprintln(w)
		fmt.Fprintln(w, c(cGray, "  \u25cf = owns a listening socket"))
	}
	unwatchedNote(w, c, d)
}

// unwatchedNote states the residual coverage gap: a process holding a listening
// socket that process accounting does not track at all, so this report has no
// figure for it -- not even a flat row. The selection is the reason (a service
// whitelist plus the heaviest processes by weight, and a new daemon is neither),
// and an unstated reason reads as the change report and this report
// contradicting each other.
func unwatchedNote(w io.Writer, c func(string, string) string, d state.ProcDiff) {
	if len(d.UnwatchedListeners) == 0 {
		return
	}
	fmt.Fprintf(w, "%s\n", c(cGray, fmt.Sprintf(
		"  holds a listening socket but is not tracked here: %s",
		strings.Join(d.UnwatchedListeners, ", "))))
	fmt.Fprintln(w, c(cGray, "  (this section records a service whitelist plus the heaviest processes by weight)"))
}

// listensShown reports whether the table printed a listening marker, so the
// legend appears only when there is something to explain.
func listensShown(rows []state.ProcRow) bool {
	for _, r := range rows {
		if r.Verdict != state.PVFlat && r.Listens {
			return true
		}
	}
	return false
}

func pct(v *float64) string {
	if v == nil {
		return "—"
	}
	return fmt.Sprintf("%.1f%%", *v)
}

func delta(v *float64) string {
	if v == nil {
		return "—"
	}
	return fmt.Sprintf("%+.0f%%", *v)
}

// deltaFrom explains an absent percentage instead of printing a dash next
// to a row that was nonetheless flagged. A rise from an idle baseline has
// no ratio -- the change from zero is infinite -- and "from idle" tells the
// reader why the row is here, which a dash does not.
func deltaFrom(v *float64, fromZero bool) string {
	if v == nil && fromZero {
		return "from idle"
	}
	return delta(v)
}

// pctApprox marks a lifetime average, which is what a process born inside
// the window has instead of a rate measured across it. It is the best
// figure available and must not read as if it were measured.
func pctApprox(v *float64, approx bool) string {
	if v == nil {
		return "—"
	}
	if approx {
		return fmt.Sprintf("~%.1f%%", *v)
	}
	return fmt.Sprintf("%.1f%%", *v)
}

func mem(v *float64) string {
	if v == nil {
		return "—"
	}
	kb := *v
	switch {
	case kb >= 1048576:
		return fmt.Sprintf("%.1fG", kb/1048576)
	case kb >= 1024:
		return fmt.Sprintf("%.0fM", kb/1024)
	}
	return fmt.Sprintf("%.0fK", kb)
}
