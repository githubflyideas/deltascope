package pcp

import (
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
		if err != nil {
			continue
		}
		out = append(out, Value{
			Metric:   m[1],
			Instance: m[2],
			Val:      val,
			Units:    strings.TrimSpace(m[4]),
		})
	}
	return out
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
		return nil, nil, fmt.Errorf("时间窗无效: 结束时间必须晚于开始时间")
	}
	args := append([]string{
		"-a", archive,
		"-S", PCPTime(start),
		"-T", PCPTime(end),
	}, metrics...)

	stdout, stderr, err := r.Run(ctx, "pmlogsummary", args...)
	warnings := splitNonEmptyLines(string(stderr))
	if err != nil {
		return nil, warnings, fmt.Errorf("pmlogsummary 执行失败: %w (%s)", err, firstLine(string(stderr)))
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
