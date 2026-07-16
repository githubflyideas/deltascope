package main

import (
	"context"
	"embed"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/githubflyideas/deltascope/internal/auth"
	"github.com/githubflyideas/deltascope/internal/httpapi"
	"github.com/githubflyideas/deltascope/internal/pcp"
	"github.com/githubflyideas/deltascope/internal/store"
)

//go:embed web
var webFS embed.FS

var version = "dev"

func main() {
	log.SetFlags(log.LstdFlags | log.Lmsgprefix)
	log.SetPrefix("deltascope: ")

	if len(os.Args) < 2 {
		usage()
		os.Exit(2)
	}
	switch os.Args[1] {
	case "serve":
		cmdServe(os.Args[2:])
	case "user":
		cmdUser(os.Args[2:])
	case "catalog":
		cmdCatalog(os.Args[2:])
	case "compare":
		cmdCompare(os.Args[2:])
	case "rules":
		cmdRules(os.Args[2:])
	case "-h", "--help", "help":
		usage()
	default:
		usage()
		os.Exit(2)
	}
}

func usage() {
	fmt.Fprintln(os.Stderr, `用法:
  deltascope serve [flags]     启动 Web 服务
  deltascope user add <name>   创建/重置用户 (口令读 DSCOPE_PASSWORD 或交互输入)
  deltascope user del <name>   删除用户
  deltascope user list          列出用户
  deltascope catalog export     导出内置指标目录 (编辑后经 serve -catalog 加载)
  deltascope rules export       导出内置诊断规则 (编辑后经 serve -rules 加载)
  deltascope compare            无头比对: -a-start/-a-end/-b-start/-b-end
                                [-format text|json] [-all] [-threshold N], 发现恶化退出码 2

serve flags:
  -listen   监听地址 (默认 127.0.0.1:8080)
  -archive  PCP 归档目录 (默认 /var/log/pcp/pmlogger/<hostname>)
  -data     数据目录, 存放 SQLite 与会话密钥 (默认 /var/lib/deltascope)
  -tls-cert / -tls-key  可选, 提供后直接以 HTTPS 监听
  -session-ttl          会话有效期 (默认 12h)`)
}

func defaultArchive() string {
	host, err := os.Hostname()
	if err != nil {
		host = "localhost"
	}
	return filepath.Join("/var/log/pcp/pmlogger", host)
}

func openStore(dataDir string) *store.Store {
	if err := os.MkdirAll(dataDir, 0o750); err != nil {
		log.Fatalf("创建数据目录失败: %v", err)
	}
	st, err := store.Open(filepath.Join(dataDir, "deltascope.db"))
	if err != nil {
		log.Fatalf("打开 SQLite 失败: %v", err)
	}
	return st
}

func loadOrCreateSecret(dataDir string) []byte {
	p := filepath.Join(dataDir, "session.key")
	if b, err := os.ReadFile(p); err == nil && len(b) >= 32 {
		return b
	}
	b, err := auth.GenerateSecret()
	if err != nil {
		log.Fatal(err)
	}
	if err := os.WriteFile(p, b, 0o600); err != nil {
		log.Fatalf("写入会话密钥失败: %v", err)
	}
	return b
}

func cmdServe(args []string) {
	fs := flag.NewFlagSet("serve", flag.ExitOnError)
	listen := fs.String("listen", "127.0.0.1:8080", "监听地址")
	archive := fs.String("archive", defaultArchive(), "PCP 归档目录")
	dataDir := fs.String("data", "/var/lib/deltascope", "数据目录")
	tlsCert := fs.String("tls-cert", "", "TLS 证书 (可选)")
	tlsKey := fs.String("tls-key", "", "TLS 私钥 (可选)")
	ttl := fs.Duration("session-ttl", 12*time.Hour, "会话有效期")
	catalogPath := fs.String("catalog", "", "自定义指标目录 JSON (可选)")
	rulesPath := fs.String("rules", "", "自定义诊断规则 JSON (可选)")
	fs.Parse(args)

	if *catalogPath != "" {
		if err := pcp.LoadCatalogFile(*catalogPath); err != nil {
			log.Fatalf("加载指标目录失败: %v", err)
		}
		log.Printf("已加载自定义指标目录 %s (%d 项)", *catalogPath, len(pcp.Catalog))
	}
	if *rulesPath != "" {
		if err := pcp.LoadRulesFile(*rulesPath); err != nil {
			log.Fatalf("加载诊断规则失败: %v", err)
		}
		log.Printf("已加载自定义诊断规则 %s (%d 条)", *rulesPath, len(pcp.Rules))
	}

	if _, err := os.Stat(*archive); err != nil {
		log.Printf("警告: 归档目录 %s 不可访问 (%v), 请确认 pmlogger 已运行", *archive, err)
	}
	if _, err := exec.LookPath("pmlogsummary"); err != nil {
		log.Printf("警告: 未找到 pmlogsummary, 请安装 pcp 软件包")
	}
	if _, err := exec.LookPath("pmrep"); err != nil {
		log.Printf("警告: 未找到 pmrep, 请安装 pcp-system-tools 软件包")
	}

	st := openStore(*dataDir)
	defer st.Close()
	if users, err := st.ListUsers(); err == nil && len(users) == 0 {
		log.Printf("提示: 尚无任何用户, 先执行 deltascope user add <name> 创建管理员")
	}

	srv := &httpapi.Server{
		Store:    st,
		Sessions: auth.NewSessions(loadOrCreateSecret(*dataDir), *ttl),
		Limiter:  auth.NewRateLimiter(10, 15*time.Minute),
		Runner:   pcp.ExecRunner{},
		Archive:  *archive,
		WebFS:    webFS,
		SecureCk: *tlsCert != "",
	}

	h := &http.Server{
		Addr:              *listen,
		Handler:           srv.Routes(),
		ReadHeaderTimeout: 5 * time.Second,
	}
	if *tlsCert != "" {
		log.Printf("v%s HTTPS 监听 %s, 归档 %s", version, *listen, *archive)
		log.Fatal(h.ListenAndServeTLS(*tlsCert, *tlsKey))
	}
	log.Printf("v%s HTTP 监听 %s, 归档 %s", version, *listen, *archive)
	log.Fatal(h.ListenAndServe())
}

func cmdCatalog(args []string) {
	if len(args) == 0 || args[0] != "export" {
		fmt.Fprintln(os.Stderr, "用法: deltascope catalog export > catalog.json")
		os.Exit(2)
	}
	data, err := pcp.ExportCatalog()
	if err != nil {
		log.Fatal(err)
	}
	os.Stdout.Write(data)
	fmt.Println()
}

func parseWhen(s string) (time.Time, error) {
	for _, layout := range []string{"2006-01-02T15:04:05", "2006-01-02T15:04", "2006-01-02 15:04:05", "2006-01-02 15:04"} {
		if t, err := time.ParseInLocation(layout, s, time.Local); err == nil {
			return t, nil
		}
	}
	return time.Time{}, fmt.Errorf("时间格式无效: %q (期望 2006-01-02T15:04)", s)
}

func cmdCompare(args []string) {
	fs := flag.NewFlagSet("compare", flag.ExitOnError)
	aStart := fs.String("a-start", "", "基线开始")
	aEnd := fs.String("a-end", "", "基线结束")
	bStart := fs.String("b-start", "", "对比开始")
	bEnd := fs.String("b-end", "", "对比结束")
	archive := fs.String("archive", defaultArchive(), "归档目录")
	threshold := fs.Float64("threshold", 15, "显著阈值 %")
	catalogPath := fs.String("catalog", "", "自定义指标目录 JSON")
	rulesPath := fs.String("rules", "", "自定义诊断规则 JSON")
	format := fs.String("format", "text", "输出格式: text|json")
	showAll := fs.Bool("all", false, "文本模式包含平稳行")
	noColor := fs.Bool("no-color", false, "关闭 ANSI 颜色")
	fs.Parse(args)

	if *catalogPath != "" {
		if err := pcp.LoadCatalogFile(*catalogPath); err != nil {
			log.Fatal(err)
		}
	}
	if *rulesPath != "" {
		if err := pcp.LoadRulesFile(*rulesPath); err != nil {
			log.Fatal(err)
		}
	}
	var w pcp.Windows
	var err error
	if w.AStart, err = parseWhen(*aStart); err != nil {
		log.Fatal(err)
	}
	if w.AEnd, err = parseWhen(*aEnd); err != nil {
		log.Fatal(err)
	}
	if w.BStart, err = parseWhen(*bStart); err != nil {
		log.Fatal(err)
	}
	if w.BEnd, err = parseWhen(*bEnd); err != nil {
		log.Fatal(err)
	}
	w.ThresholdPct = *threshold

	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()
	rep, err := pcp.Compare(ctx, pcp.ExecRunner{}, *archive, w)
	if err != nil {
		log.Fatal(err)
	}
	if *format == "json" {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		if err := enc.Encode(rep); err != nil {
			log.Fatal(err)
		}
	} else {
		renderText(os.Stdout, rep, *showAll, !*noColor)
	}
	os.Exit(worstExit(rep))
}

func cmdRules(args []string) {
	if len(args) == 0 || args[0] != "export" {
		fmt.Fprintln(os.Stderr, "用法: deltascope rules export > rules.json")
		os.Exit(2)
	}
	data, err := pcp.ExportRules()
	if err != nil {
		log.Fatal(err)
	}
	os.Stdout.Write(data)
	fmt.Println()
}

func cmdUser(args []string) {
	fs := flag.NewFlagSet("user", flag.ExitOnError)
	dataDir := fs.String("data", "/var/lib/deltascope", "数据目录")
	var rest []string
	for i := 0; i < len(args); i++ {
		if args[i] == "-data" || args[i] == "--data" {
			fs.Parse(args[i:])
			break
		}
		rest = append(rest, args[i])
	}
	if len(rest) == 0 {
		usage()
		os.Exit(2)
	}

	st := openStore(*dataDir)
	defer st.Close()

	switch rest[0] {
	case "add":
		if len(rest) < 2 {
			log.Fatal("用法: deltascope user add <name>")
		}
		name := strings.TrimSpace(rest[1])
		if name == "" || len(name) > 64 {
			log.Fatal("用户名不能为空且不超过 64 字符")
		}
		pw := os.Getenv("DSCOPE_PASSWORD")
		if pw == "" {
			pw = promptPassword()
		}
		if len(pw) < 8 {
			log.Fatal("口令至少 8 位")
		}
		hash, err := auth.HashPassword(pw)
		if err != nil {
			log.Fatal(err)
		}
		if err := st.UpsertUser(name, hash); err != nil {
			log.Fatal(err)
		}
		fmt.Printf("用户 %s 已创建/更新\n", name)
	case "del":
		if len(rest) < 2 {
			log.Fatal("用法: deltascope user del <name>")
		}
		if err := st.DeleteUser(rest[1]); err != nil {
			log.Fatal(err)
		}
		fmt.Println("已删除")
	case "list":
		users, err := st.ListUsers()
		if err != nil {
			log.Fatal(err)
		}
		for _, u := range users {
			fmt.Println(u)
		}
	default:
		usage()
		os.Exit(2)
	}
}

func promptPassword() string {
	read := func(prompt string) string {
		fmt.Fprint(os.Stderr, prompt)
		_ = exec.Command("stty", "-F", "/dev/tty", "-echo").Run()
		defer func() {
			_ = exec.Command("stty", "-F", "/dev/tty", "echo").Run()
			fmt.Fprintln(os.Stderr)
		}()
		var s string
		fmt.Scanln(&s)
		return s
	}
	p1 := read("口令: ")
	p2 := read("再输一次: ")
	if p1 != p2 {
		log.Fatal("两次输入不一致")
	}
	return p1
}
