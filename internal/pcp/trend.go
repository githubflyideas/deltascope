package pcp

import (
	"bytes"
	"context"
	"encoding/csv"
	"fmt"
	"io"
	"regexp"
	"strconv"
	"strings"
	"time"
)

type Series struct {
	Name   string   `json:"name"`
	Points [][2]any `json:"points"`
}

func TrendStep(start, end time.Time) time.Duration {
	step := end.Sub(start) / 600
	switch {
	case step < 10*time.Second:
		return 10 * time.Second
	case step > 15*time.Minute:
		return 15 * time.Minute
	}
	return step.Round(time.Second)
}

var invalidMetricRe = regexp.MustCompile(`Invalid metric ([A-Za-z][A-Za-z0-9._]*)`)

func RunTrend(ctx context.Context, r Runner, archive, preset string, start, end time.Time) ([]Series, []string, error) {
	p, ok := TrendPresets[preset]
	if !ok {
		return nil, nil, fmt.Errorf("unknown metric group: %q", preset)
	}
	if !end.After(start) {
		return nil, nil, fmt.Errorf("invalid time window: end must be after start")
	}
	step := TrendStep(start, end)
	metrics := append([]string{}, p.Metrics...)
	var missing []string

	for len(metrics) > 0 {
		args := append([]string{
			"-a", archive,
			"-S", PCPTime(start),
			"-T", PCPTime(end),
			"-t", fmt.Sprintf("%ds", int(step.Seconds())),
			"-o", "csv",
		}, metrics...)

		stdout, stderr, err := r.Run(ctx, "pmrep", args...)
		if err == nil {
			series, perr := ParseTrendCSV(bytes.NewReader(stdout))
			return series, missing, perr
		}
		m := invalidMetricRe.FindStringSubmatch(string(stderr))
		if m == nil {
			return nil, missing, fmt.Errorf("trend query failed: %w (%s)", err, firstLine(string(stderr)))
		}
		removed := false
		next := metrics[:0]
		for _, mt := range metrics {
			if mt == m[1] && !removed {
				removed = true
				missing = append(missing, mt)
				continue
			}
			next = append(next, mt)
		}
		if !removed {
			return nil, missing, fmt.Errorf("trend query failed: %w (%s)", err, firstLine(string(stderr)))
		}
		metrics = next
	}
	return nil, missing, fmt.Errorf("none of this group's metrics were found in the archive")
}

func ParseTrendCSV(r io.Reader) ([]Series, error) {
	cr := csv.NewReader(r)
	cr.FieldsPerRecord = -1
	header, err := cr.Read()
	if err != nil {
		return nil, fmt.Errorf("failed to parse pmrep CSV header: %w", err)
	}
	if len(header) < 2 {
		return nil, fmt.Errorf("pmrep produced no output (the window may have no archived data)")
	}

	series := make([]Series, len(header)-1)
	for i, h := range header[1:] {
		series[i] = Series{Name: strings.TrimSpace(h)}
	}

	for {
		rec, err := cr.Read()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("failed to parse pmrep CSV: %w", err)
		}
		if len(rec) < 2 {
			continue
		}
		ts, err := time.ParseInLocation("2006-01-02 15:04:05", strings.TrimSpace(rec[0]), time.Local)
		if err != nil {
			continue
		}
		ms := ts.UnixMilli()
		for i := 1; i < len(rec) && i-1 < len(series); i++ {
			cell := strings.TrimSpace(rec[i])
			var v any
			if cell == "" || cell == "N/A" || cell == "?" {
				v = nil
			} else if f, err := strconv.ParseFloat(cell, 64); err == nil {
				v = f
			} else {
				v = nil
			}
			series[i-1].Points = append(series[i-1].Points, [2]any{ms, v})
		}
	}
	return series, nil
}
