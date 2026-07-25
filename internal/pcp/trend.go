package pcp

import (
	"bytes"
	"math"
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

// TrendStep picks a sampling step for a window, targeting ~600 points.
//
// The lower bound is 60s deliberately: pmlogger's tiered sampling records
// the warm tier (per-disk, per-NIC, per-core) once a minute, and asking
// pmrep for a step finer than the archive's own logging interval yields
// empty output rather than a denser chart. A one-hour window at 60s is 60
// points, which is plenty for spotting a shape.
func TrendStep(start, end time.Time) time.Duration {
	step := end.Sub(start) / 600
	switch {
	case step < time.Minute:
		return time.Minute
	case step > 15*time.Minute:
		return 15 * time.Minute
	}
	return step.Round(time.Second)
}

// pmrep's CSV timestamp format varies with version and sampling
// interval: some builds emit fractional seconds, some use an ISO 'T'
// separator, some append a zone. Accept them all rather than dropping
// data we could have read.
var trendTimeLayouts = []string{
	"2006-01-02 15:04:05",
	"2006-01-02 15:04:05.000",
	"2006-01-02 15:04:05.000000",
	"2006-01-02T15:04:05",
	"2006-01-02T15:04:05.000",
	"2006-01-02 15:04:05 MST",
	"2006-01-02T15:04:05Z07:00",
	time.RFC3339,
	"01/02/2006 15:04:05",
	"15:04:05",
}

func parseTrendTime(v string) (time.Time, bool) {
	for _, layout := range trendTimeLayouts {
		if t, err := time.ParseInLocation(layout, v, time.Local); err == nil {
			// a time-only layout yields year 0; anchor it to today so the
			// chart axis stays sane
			if t.Year() == 0 {
				now := time.Now()
				t = time.Date(now.Year(), now.Month(), now.Day(),
					t.Hour(), t.Minute(), t.Second(), 0, time.Local)
			}
			return t, true
		}
	}
	// last resort: a bare epoch (seconds or milliseconds)
	if f, err := strconv.ParseFloat(v, 64); err == nil && f > 1e8 {
		if f > 1e12 {
			return time.UnixMilli(int64(f)), true
		}
		return time.Unix(int64(f), 0), true
	}
	return time.Time{}, false
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

	// Rows whose timestamp we cannot read used to be dropped silently,
	// which rendered an empty chart with no explanation. Count them and
	// report, so a format we do not handle is visible instead of looking
	// like "no data".
	var skipped int
	var unparsed string
	rows := 0

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
		ts, ok := parseTrendTime(strings.TrimSpace(rec[0]))
		if !ok {
			skipped++
			if unparsed == "" {
				unparsed = strings.TrimSpace(rec[0])
			}
			continue
		}
		ms := ts.UnixMilli()
		for i := 1; i < len(rec) && i-1 < len(series); i++ {
			cell := strings.TrimSpace(rec[i])
			var v any
			if cell == "" || cell == "N/A" || cell == "?" {
				v = nil
			} else if f, err := strconv.ParseFloat(cell, 64); err == nil && !math.IsNaN(f) && !math.IsInf(f, 0) {
				// NaN/Inf can come out of pmrep for a degenerate ratio (e.g.
				// a 0-byte loop-device filesystem computing used/total).
				// Go's JSON encoder cannot encode either and errors out
				// mid-stream -- after headers and some bytes are already
				// sent, so the client sees a 200 with a truncated, invalid
				// body instead of a clean error. Treat it as a missing
				// sample instead of letting it reach the encoder.
				v = f
			} else {
				v = nil
			}
			series[i-1].Points = append(series[i-1].Points, [2]any{ms, v})
		}
		rows++
	}
	if rows == 0 && skipped > 0 {
		return nil, fmt.Errorf("could not read any timestamps from pmrep output (%d rows skipped, first was %q)", skipped, unparsed)
	}
	return series, nil
}
