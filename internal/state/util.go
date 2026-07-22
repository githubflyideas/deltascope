package state

import (
	"bufio"
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

func readFile(path string) (string, bool) {
	b, err := os.ReadFile(path)
	if err != nil {
		return "", false
	}
	return string(b), true
}

func fileHash(path string) (string, bool) {
	b, err := os.ReadFile(path)
	if err != nil {
		return "", false
	}
	return hashBytes(b), true
}

// runCmd 执行只读命令,带超时。返回 stdout 与是否成功。
func runCmd(ctx context.Context, name string, args ...string) (string, bool) {
	if _, err := exec.LookPath(name); err != nil {
		return "", false
	}
	cctx, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()
	var out bytes.Buffer
	cmd := exec.CommandContext(cctx, name, args...)
	cmd.Stdout = &out
	if err := cmd.Run(); err != nil {
		return out.String(), false
	}
	return out.String(), true
}

func lines(s string) []string {
	var out []string
	sc := bufio.NewScanner(strings.NewReader(s))
	sc.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	for sc.Scan() {
		if t := strings.TrimSpace(sc.Text()); t != "" {
			out = append(out, t)
		}
	}
	return out
}

func fields(line string) []string {
	return strings.Fields(line)
}

func hasRoot() bool { return os.Geteuid() == 0 }

// globFiles 展开一个 glob 清单,返回存在的普通文件路径。
func globFiles(patterns []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, p := range patterns {
		matches, _ := filepath.Glob(p)
		for _, m := range matches {
			if seen[m] {
				continue
			}
			if fi, err := os.Stat(m); err == nil && fi.Mode().IsRegular() {
				seen[m] = true
				out = append(out, m)
			}
		}
	}
	return out
}
