package pcp

import (
	"bytes"
	"context"
	"encoding/csv"
	"fmt"
	"io"
	"strconv"
	"strings"
	"time"
)

// Series 是趋势图中的一条曲线。Points 为 [unix 毫秒, 值];
// 缺采样点的值以 NaN 表示,JSON 层转为 null(ECharts 支持断线)。
type Series struct {
	Name   string      `json:"name"`
	Points [][2]any    `json:"points"`
}

// TrendStep 根据窗口长度选择采样步长,控制前端点数在 ~600 以内。
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

// RunTrend 通过 pmrep 导出 CSV 并解析为序列。
// preset 必须是 TrendPresets 的键(白名单)。
func RunTrend(ctx context.Context, r Runner, archive, preset string, start, end time.Time) ([]Series, error) {
	p, ok := TrendPresets[preset]
	if !ok {
		return nil, fmt.Errorf("未知的指标组: %q", preset)
	}
	if !end.After(start) {
		return nil, fmt.Errorf("时间窗无效: 结束时间必须晚于开始时间")
	}
	step := TrendStep(start, end)
	args := append([]string{
		"-a", archive,
		"-S", PCPTime(start),
		"-T", PCPTime(end),
		"-t", fmt.Sprintf("%ds", int(step.Seconds())),
		"-o", "csv",
	}, p.Metrics...)

	stdout, stderr, err := r.Run(ctx, "pmrep", args...)
	if err != nil {
		return nil, fmt.Errorf("pmrep 执行失败: %w (%s)", err, firstLine(string(stderr)))
	}
	return ParseTrendCSV(bytes.NewReader(stdout))
}

// ParseTrendCSV 解析 pmrep -o csv 输出。
// 首行: Time,"metric-instance",...   首列时间格式: 2026-07-03 14:00:00
func ParseTrendCSV(r io.Reader) ([]Series, error) {
	cr := csv.NewReader(r)
	cr.FieldsPerRecord = -1
	header, err := cr.Read()
	if err != nil {
		return nil, fmt.Errorf("解析 pmrep CSV 表头失败: %w", err)
	}
	if len(header) < 2 {
		return nil, fmt.Errorf("pmrep 输出为空(窗口内可能没有归档数据)")
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
			return nil, fmt.Errorf("解析 pmrep CSV 失败: %w", err)
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
