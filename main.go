// deltascope: 单机 PCP 性能倒退对比与可视化 Web 系统。
// 单一静态二进制,前端资源内嵌,凭证存本地 SQLite,零外部服务依赖。
//
// 用法:
//   deltascope serve   [-listen :8080] [-archive DIR] [-data DIR] [-tls-cert F -tls-key F]
//   deltascope user add <name>     (口令从 DSCOPE_PASSWORD 环境变量或交互输入)
//   deltascope user del <name>
//   deltascope user list
package main

import (
	"embed"
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

// version 由构建时 -ldflags "-X main.version=..." 注入。
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
  deltascope user list         列出用户

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

// loadOrCreateSecret 读取或首启生成会话签名密钥(0600)。
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
	fs.Parse(args)

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

func cmdUser(args []string) {
	fs := flag.NewFlagSet("user", flag.ExitOnError)
	dataDir := fs.String("data", "/var/lib/deltascope", "数据目录")
	// 允许 flag 出现在子命令动词之后: user add -data /x name
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

// promptPassword 交互式读取口令,通过 stty 关闭回显(仅 Linux,避免引入 x/term 依赖)。
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
