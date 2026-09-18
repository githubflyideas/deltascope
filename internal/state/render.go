package state

import (
	"fmt"
	"io"
	"strings"
	"time"
)

const (
	ansiRed   = "\x1b[31m"
	ansiGreen = "\x1b[32m"
	ansiAmber = "\x1b[33m"
	ansiDim   = "\x1b[90m"
	ansiBold  = "\x1b[1m"
)

// colorizer wraps a string in an ANSI code, or returns it untouched.
type colorizer func(code, s string) string

func newColorizer(color bool) colorizer {
	return func(code, s string) string {
		if !color {
			return s
		}
		return code + s + "\x1b[0m"
	}
}

// changeLine formats one change, the same way the flat and the grouped views
// both print it. The trailing dim field is the extra evidence the collector
// had: its own note, and the file's modification time for file-backed items --
// the only per-item timestamp in change accounting, so worth the width.
func changeLine(c colorizer, ch Change) string {
	var s string
	switch ch.Kind {
	case Added:
		s = c(ansiGreen, fmt.Sprintf("  + %s = %s", ch.Key, ch.New))
	case Removed:
		s = c(ansiDim, fmt.Sprintf("  - %s  (was %s)", ch.Key, ch.Old))
	default:
		s = c(ansiAmber, fmt.Sprintf("  ~ %s: %s → %s", ch.Key, ch.Old, ch.New))
	}
	var extra []string
	// Some collectors put the value itself in Note -- a listening port's
	// process is both -- and repeating it would read as a second fact.
	if ch.Note != "" && ch.Note != ch.New && ch.Note != ch.Old {
		extra = append(extra, ch.Note)
	}
	if ch.Mtime > 0 {
		extra = append(extra, "mtime "+time.Unix(ch.Mtime, 0).Local().Format("2006-01-02 15:04"))
	}
	if len(extra) > 0 {
		s += "  " + c(ansiDim, strings.Join(extra, " · "))
	}
	return s
}

// RenderText renders a diff as terminal-friendly colored text.
func RenderText(w io.Writer, d Diff, color bool) {
	c := newColorizer(color)

	fmt.Fprintf(w, "%s\n", c(ansiBold, "deltascope statediff"))
	fmt.Fprintf(w, "  A %s\n  B %s\n\n",
		d.A.Taken.Local().Format("2006-01-02 15:04:05"),
		d.B.Taken.Local().Format("2006-01-02 15:04:05"))

	if d.Total == 0 {
		fmt.Fprintln(w, c(ansiGreen, "State is identical between the two points in time - no configuration or environment changes detected."))
		return
	}

	fmt.Fprintf(w, "%s %d changes\n\n", c(ansiBold, "▲"), d.Total)

	for _, sd := range d.Sections {
		fmt.Fprintf(w, "%s  (%d)\n", c(ansiBold, "== "+sd.Title+" =="), len(sd.Changes))
		for _, ch := range sd.Changes {
			fmt.Fprintln(w, changeLine(c, ch))
		}
		fmt.Fprintln(w)
	}
}

// RenderTimeline prints the same changes as the events the stored history
// places them in: one block per event, headed by the interval the bisect
// narrowed it to. The flat view answers "what is different"; this one answers
// "how many separate things happened", which for a kernel upgrade across four
// sections is the difference between one line and three hundred.
//
// An event the history could not narrow says so instead of printing the full
// window as if it had been confirmed.
func RenderTimeline(w io.Writer, d Diff, evs []Event, color bool) {
	c := newColorizer(color)

	fmt.Fprintf(w, "%s\n", c(ansiBold, "deltascope statediff"))
	fmt.Fprintf(w, "  A %s\n  B %s\n\n",
		d.A.Taken.Local().Format("2006-01-02 15:04:05"),
		d.B.Taken.Local().Format("2006-01-02 15:04:05"))
	fmt.Fprintf(w, "%s %d changes in %d event(s)\n\n", c(ansiBold, "▲"), d.Total, len(evs))

	for _, ev := range evs {
		head := eventHead(ev)
		fmt.Fprintf(w, "%s  (%d)\n", c(ansiBold, "== "+head+" =="), len(ev.Changes))
		if ev.Dated {
			fmt.Fprintf(w, "   %s\n", c(ansiDim, fmt.Sprintf("between %s and %s",
				ev.From.Local().Format("2006-01-02 15:04:05"),
				ev.To.Local().Format("15:04:05"))))
		} else {
			fmt.Fprintf(w, "   %s\n", c(ansiDim, "time unknown - no stored snapshot narrows this down"))
		}
		if ev.Unstable {
			fmt.Fprintf(w, "   %s\n", c(ansiRed, "value changed more than once in the window - the interval above bounds only the last move"))
		}
		section := ""
		for _, ch := range ev.Changes {
			if ch.Section != section {
				section = ch.Section
				title := ch.Title
				if title == "" {
					title = ch.Section
				}
				fmt.Fprintf(w, "   %s\n", c(ansiDim, "-- "+title))
			}
			fmt.Fprintln(w, changeLine(c, ch))
		}
		fmt.Fprintln(w)
	}
}

// eventHead names an event from its class and subject. The web UI words this
// in the reader's language from the same two fields; the CLI has only English
// to offer, which is what it has always printed.
func eventHead(ev Event) string {
	var base string
	switch ev.Class {
	case "kernel":
		base = "kernel change"
	case "packages":
		base = "package change"
	case "section":
		base = ev.Changes[0].Title
		if base == "" {
			base = ev.Changes[0].Section
		}
	default:
		base = "several areas changed together"
	}
	if ev.Subject != "" {
		return base + " " + ev.Subject
	}
	return base
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
