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

// Value 是 pmlogsummary 输出中的一行:某指标(某实例)在时间窗内的均值。
// pmlogsummary 对 counter 语义的指标默认换算为速率均值,与 pmdiff 语义一致。
type Value struct {
	Metric   string  `json:"metric"`
	Instance string  `json:"instance"` // 无实例时为空串
	Val      float64 `json:"val"`
	Units    string  `json:"units"`
}

// Key 唯一标识 指标+实例。
func (v Value) Key() string { return v.Metric + "\x00" + v.Instance }

// PCPTime 把 Go 时间格式化为 PCP 命令行接受的 "@ ctime" 形式,
// 例如 "@ Thu Jul  3 14:00:00 2026"。用户输入永远先经 time 解析再重格式化,
// 不存在把原始字符串透传给外部命令的路径。
func PCPTime(t time.Time) string {
	return "@ " + t.Format("Mon Jan _2 15:04:05 2006")
}

// 行格式示例:
//   kernel.all.cpu.user  245.917 millisec / second
//   kernel.all.load ["1 minute"] 1.542 none
//   disk.dev.read_bytes ["sda"] 12.500 Kbyte / second
var summaryLine = regexp.MustCompile(
	`^(\S+)(?:\s+\["(.*?)"\])?\s+(-?\d+(?:\.\d+)?(?:[eE][-+]?\d+)?)\s*(.*)$`)

// ParseSummary 解析 pmlogsummary 的标准输出。
// 头部("Performance metrics from host ..." / commencing / ending)与
// 无法解析的行被跳过;调用方可通过 stderr 获取告警(如某指标无数据)。
func ParseSummary(r io.Reader) []Value {
	var out []Value
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for sc.Scan() {
		line := sc.Text()
		trimmed := strings.TrimSpace(line)
		if trimmed == "" ||
			strings.HasPrefix(line, " ") || // commencing / ending 缩进行
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

// Runner 抽象外部命令执行,便于单测注入假输出。
type Runner interface {
	Run(ctx context.Context, name string, args ...string) (stdout, stderr []byte, err error)
}

// ExecRunner 是生产实现:数组形式 exec,不经过 shell。
type ExecRunner struct{}

func (ExecRunner) Run(ctx context.Context, name string, args ...string) ([]byte, []byte, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	var so, se bytes.Buffer
	cmd.Stdout, cmd.Stderr = &so, &se
	err := cmd.Run()
	return so.Bytes(), se.Bytes(), err
}

// RunSummary 对归档在 [start, end) 窗口内执行 pmlogsummary,
// 返回 指标+实例 → 均值 的映射及 stderr 告警行。
// archive 传 PCP 归档目录(PCP ≥5 支持目录,自动跨天拼接多个归档卷)。
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
		// pmlogsummary 在窗口完全无数据等情况下非零退出,把 stderr 一并带回。
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
