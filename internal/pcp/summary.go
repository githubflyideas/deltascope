package pcp

import (
	"math"
	"bufio"
	"bytes"
	"context"
	"fmt"
	"io"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"time"
)

type Value struct {
	Metric   string  `json:"metric"`
	Instance string  `json:"instance"`
	Val      float64 `json:"val"`
	Units    string  `json:"units"`
}

func (v Value) Key() string { return v.Metric + "\x00" + v.Instance }

func PCPTime(t time.Time) string {
	return "@ " + t.Format("Mon Jan _2 15:04:05 2006")
}

var summaryLine = regexp.MustCompile(
	`^(\S+)(?:\s+\["(.*?)"\])?\s+(-?\d+(?:\.\d+)?(?:[eE][-+]?\d+)?)\s*(.*)$`)

func ParseSummary(r io.Reader) []Value {
	var out []Value
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for sc.Scan() {
		line := sc.Text()
		trimmed := strings.TrimSpace(line)
		if trimmed == "" ||
			strings.HasPrefix(line, " ") ||
			strings.HasPrefix(trimmed, "Performance metrics") {
			continue
		}
		m := summaryLine.FindStringSubmatch(trimmed)
		if m == nil {
			continue
		}
		val, err := strconv.ParseFloat(m[3], 64)
		if err != nil || math.IsNaN(val) || math.IsInf(val, 0) {
			continue
		}
		// The regex's trailing group captures everything after the mean --
		// on this pmlogsummary build that is "stddev min max count unit",
		// not just the unit. Only the last whitespace-delimited token is
		// the actual unit; the rest is statistics we don't use here. This
		// used to leak the whole tail into the Units field, showing up as
		// garbage like "0.018 0.002 1.147 766 none" instead of "none".
		out = append(out, Value{
			Metric:   m[1],
			Instance: m[2],
			Val:      val,
			Units:    extractUnit(m[4]),
		})
	}
	return out
}

// extractUnit finds where a pmlogsummary stat line's trailing numeric
// fields (stddev/min/max/count -- however many a given PCP build emits)
// end and the unit text begins. The unit itself can be multiple words
// ("count / sec", "byte / sec"), so this returns everything from the
// first non-numeric token onward, not just the last token.
func extractUnit(s string) string {
	fields := strings.Fields(s)
	for i, f := range fields {
		if _, err := strconv.ParseFloat(f, 64); err != nil {
			return strings.Join(fields[i:], " ")
		}
	}
	return ""
}

type Runner interface {
	Run(ctx context.Context, name string, args ...string) (stdout, stderr []byte, err error)
}

type ExecRunner struct{}

func (ExecRunner) Run(ctx context.Context, name string, args ...string) ([]byte, []byte, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	var so, se bytes.Buffer
	cmd.Stdout, cmd.Stderr = &so, &se
	err := cmd.Run()
	return so.Bytes(), se.Bytes(), err
}

func RunSummary(ctx context.Context, r Runner, archive string, start, end time.Time, metrics []string) (map[string]Value, []string, error) {
	if !end.After(start) {
		return nil, nil, fmt.Errorf("invalid time window: end must be after start")
	}
	args := append([]string{
		"-a", archive,
		"-S", PCPTime(start),
		"-T", PCPTime(end),
	}, metrics...)

	stdout, stderr, err := r.Run(ctx, "pmlogsummary", args...)
	warnings := splitNonEmptyLines(string(stderr))
	if err != nil {
		return nil, warnings, fmt.Errorf("pmlogsummary failed: %w (%s)", err, firstLine(string(stderr)))
	}

	vals := ParseSummary(bytes.NewReader(stdout))
	out := make(map[string]Value, len(vals))
	for _, v := range vals {
		out[v.Key()] = v
	}
	return out, warnings, nil
}

func splitNonEmptyLines(s string) []string {
	var out []string
	for _, l := range strings.Split(s, "\n") {
		if t := strings.TrimSpace(l); t != "" {
			out = append(out, t)
		}
	}
	return out
}

func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	return s
}
