package state

import (
	"fmt"
	"io"
	"time"
)

// RenderText 把 diff 渲染为终端友好的彩色文本。
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
		fmt.Fprintln(w, c(green, "两个时刻状态一致 — 未检出任何配置或环境变更。"))
		return
	}

	fmt.Fprintf(w, "%s 共 %d 处变更\n\n", c(bold, "▲"), d.Total)

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
				fmt.Fprintln(w, c(dim, fmt.Sprintf("  - %s  (原 %s)", ch.Key, ch.Old)))
			case Modified:
				fmt.Fprintln(w, c(amber, fmt.Sprintf("  ~ %s: %s → %s", ch.Key, ch.Old, ch.New)))
			}
		}
		fmt.Fprintln(w)
	}
}

// RenderSummaryLine 返回单行摘要,便于 cron 日志或告警管道判读。
func RenderSummaryLine(d Diff) string {
	if d.Total == 0 {
		return fmt.Sprintf("[%s] 状态一致", time.Now().Format("2006-01-02"))
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
	return fmt.Sprintf("[%s] %d 处变更: +%d 新增 ~%d 修改 -%d 移除",
		time.Now().Format("2006-01-02"), d.Total, a, m, r)
}

// RenderMarkdown 把 diff 渲染为 Markdown,便于贴进 PR / Slack / 工单。
func RenderMarkdown(w io.Writer, d Diff, title string) {
	if title == "" {
		title = "变更影响面报告"
	}
	fmt.Fprintf(w, "## %s\n\n", title)
	fmt.Fprintf(w, "- 基线 A: `%s`\n- 对比 B: `%s`\n\n",
		d.A.Taken.Local().Format("2006-01-02 15:04:05"),
		d.B.Taken.Local().Format("2006-01-02 15:04:05"))

	if d.Total == 0 {
		fmt.Fprintln(w, "✅ **未检出任何配置或环境变更** — 本次改动未触及系统状态。")
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
	fmt.Fprintf(w, "⚠️ **共 %d 处变更** — 🟢 新增 %d · 🟡 修改 %d · ⚪ 移除 %d\n\n", d.Total, a, m, r)

	for _, sd := range d.Sections {
		fmt.Fprintf(w, "### %s (%d)\n\n", sd.Title, len(sd.Changes))
		fmt.Fprintln(w, "| | 项 | 变化 |")
		fmt.Fprintln(w, "|---|---|---|")
		for _, ch := range sd.Changes {
			switch ch.Kind {
			case Added:
				fmt.Fprintf(w, "| 🟢 | `%s` | 新增 = `%s` |\n", ch.Key, mdEsc(ch.New))
			case Removed:
				fmt.Fprintf(w, "| ⚪ | `%s` | 移除 (原 `%s`) |\n", ch.Key, mdEsc(ch.Old))
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
