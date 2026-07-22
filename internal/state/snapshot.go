package state

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"sort"
	"time"
)

// Item 是快照中的一条事实。Key 在同一 Section 内唯一,Value 是其可比较的当前值。
// 对配置文件类,Value 存哈希;对参数类,Value 存参数值本身。
type Item struct {
	Key   string `json:"key"`
	Value string `json:"value"`
	Note  string `json:"note,omitempty"`
}

// Section 是一组同类事实,由单个 Collector 产出。
type Section struct {
	Name    string `json:"name"`
	Title   string `json:"title"`
	Items   []Item `json:"items"`
	Skipped string `json:"skipped,omitempty"`
}

// Snapshot 是某一时刻整机可枚举状态的全量拍平。
type Snapshot struct {
	Host     string    `json:"host"`
	Taken    time.Time `json:"taken"`
	Sections []Section `json:"sections"`
}

// Collector 采集一个 Section。实现必须是只读的,且在权限不足或工具缺失时
// 返回带 Skipped 说明的空 Section 而非错误,保证部分成功。
type Collector interface {
	Name() string
	Collect(ctx context.Context) Section
}

var registry []Collector

func register(c Collector) { registry = append(registry, c) }

// Collectors 返回全部已注册采集器。
func Collectors() []Collector { return registry }

// Capture 依次运行全部采集器,产出一份完整快照。
func Capture(ctx context.Context, host string) Snapshot {
	snap := Snapshot{Host: host, Taken: time.Now().UTC()}
	for _, c := range registry {
		sec := c.Collect(ctx)
		sort.Slice(sec.Items, func(i, j int) bool { return sec.Items[i].Key < sec.Items[j].Key })
		snap.Sections = append(snap.Sections, sec)
	}
	return snap
}

func hashBytes(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:12])
}
