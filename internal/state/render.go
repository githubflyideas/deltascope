package state

import (
	"fmt"
	"io"
	"time"
)

// RenderText renders a diff as terminal-friendly colored text.
func RenderText(w io.Writer, d Diff, color bool) {
	c := func(code, s string) string {
		if !color {
			return s
		}
		return code + s + "\x1b[0m"
	}
	const (
		red   = "\x1b[31m"
		green = "\x1b[32m"
		amber = "\x1b[33m"
		dim   = "\x1b[90m"
		bold  = "\x1b[1m"
	)

	fmt.Fprintf(w, "%s\n", c(bold, "deltascope statediff"))
	fmt.Fprintf(w, "  A %s\n  B %s\n\n",
		d.A.Taken.Local().Format("2006-01-02 15:04:05"),
		d.B.Taken.Local().Format("2006-01-02 15:04:05"))

	if d.Total == 0 {
		fmt.Fprintln(w, c(green, "State is identical between the two points in time - no configuration or environment changes detected."))
		return
	}

	fmt.Fprintf(w, "%s %d changes\n\n", c(bold, "▲"), d.Total)

	for _, sd := range d.Sections {
		fmt.Fprintf(w, "%s  (%d)\n", c(bold, "== "+sd.Title+" =="), len(sd.Changes))
		for _, ch := range sd.Changes {
			switch ch.Kind {
			case Added:
				line := fmt.Sprintf("  + %s = %s", ch.Key, ch.New)
				if ch.Note != "" {
					line += "  " + ch.Note
				}
				fmt.Fprintln(w, c(green, line))
			case Removed:
				fmt.Fprintln(w, c(dim, fmt.Sprintf("  - %s  (was %s)", ch.Key, ch.Old)))
			case Modified:
				fmt.Fprintln(w, c(amber, fmt.Sprintf("  ~ %s: %s → %s", ch.Key, ch.Old, ch.New)))
			}
		}
		fmt.Fprintln(w)
	}
}

// RenderSummaryLine returns a single-line summary for cron logs or alert pipelines.
func RenderSummaryLine(d Diff) string {
	if d.Total == 0 {
		return fmt.Sprintf("[%s] no change", time.Now().Format("2006-01-02"))
	}
	var a, r, m int
	for _, sd := range d.Sections {
		for _, ch := range sd.Changes {
			switch ch.Kind {
			case Added:
				a++
			case Removed:
				r++
			case Modified:
				m++
			}
		}
	}
	return fmt.Sprintf("[%s] %d changes: +%d added ~%d modified -%d removed",
		time.Now().Format("2006-01-02"), d.Total, a, m, r)
}

// RenderMarkdown renders a diff as Markdown, for pasting into a PR / Slack / ticket.
func RenderMarkdown(w io.Writer, d Diff, title string) {
	if title == "" {
		title = "Change Impact Report"
	}
	fmt.Fprintf(w, "## %s\n\n", title)
	fmt.Fprintf(w, "- Baseline A: `%s`\n- Compare B: `%s`\n\n",
		d.A.Taken.Local().Format("2006-01-02 15:04:05"),
		d.B.Taken.Local().Format("2006-01-02 15:04:05"))

	if d.Total == 0 {
		fmt.Fprintln(w, "✅ **No configuration or environment changes detected** - this change did not touch system state.")
		return
	}

	var a, m, r int
	for _, sd := range d.Sections {
		for _, ch := range sd.Changes {
			switch ch.Kind {
			case Added:
				a++
			case Modified:
				m++
			case Removed:
				r++
			}
		}
	}
	fmt.Fprintf(w, "⚠️ **%d changes** - 🟢 %d added · 🟡 %d modified · ⚪ %d removed\n\n", d.Total, a, m, r)

	for _, sd := range d.Sections {
		fmt.Fprintf(w, "### %s (%d)\n\n", sd.Title, len(sd.Changes))
		fmt.Fprintln(w, "| | Item | Change |")
		fmt.Fprintln(w, "|---|---|---|")
		for _, ch := range sd.Changes {
			switch ch.Kind {
			case Added:
				fmt.Fprintf(w, "| 🟢 | `%s` | added = `%s` |\n", ch.Key, mdEsc(ch.New))
			case Removed:
				fmt.Fprintf(w, "| ⚪ | `%s` | removed (was `%s`) |\n", ch.Key, mdEsc(ch.Old))
			case Modified:
				fmt.Fprintf(w, "| 🟡 | `%s` | `%s` → `%s` |\n", ch.Key, mdEsc(ch.Old), mdEsc(ch.New))
			}
		}
		fmt.Fprintln(w)
	}
}

func mdEsc(s string) string {
	if len(s) > 60 {
		s = s[:57] + "..."
	}
	out := make([]rune, 0, len(s))
	for _, r := range s {
		if r == '|' || r == '`' {
			out = append(out, ' ')
			continue
		}
		out = append(out, r)
	}
	return string(out)
}
