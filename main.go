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
	"github.com/githubflyideas/deltascope/internal/state"
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
	case "snapshot":
		cmdSnapshot(os.Args[2:])
	case "statediff":
		cmdStatediff(os.Args[2:])
	case "proc-diff":
		cmdProcDiff(os.Args[2:])
	case "verify":
		cmdVerify(os.Args[2:])
	case "-h", "--help", "help":
		usage()
	default:
		usage()
		os.Exit(2)
	}
}

func usage() {
	fmt.Fprintln(os.Stderr, `usage:
  deltascope serve [flags]     start the web server
  deltascope user add <name>   create/reset a user (password via DSCOPE_PASSWORD or prompt)
  deltascope user del <name>   delete a user
  deltascope user list          list users
  deltascope catalog export     export the built-in metric catalog (edit, load with serve -catalog)
  deltascope rules export       export the built-in diagnosis rules (edit, load with serve -rules)
  deltascope snapshot           capture current whole-machine state and store it
  deltascope statediff          diff two points in time, showing only what changed
  deltascope proc-diff          per-process CPU/memory accounting (needs hotproc archive)
  deltascope verify start       baseline before a release; verify report after, for an impact report
  deltascope compare            headless diff: -a-start/-a-end/-b-start/-b-end
                                [-format text|json] [-all] [-threshold N], exit 2 on regressions

serve flags:
  -listen   listen address (default 127.0.0.1:8080)
  -archive  PCP archive directory (default /var/log/pcp/pmlogger/<hostname>)
  -data     data directory for SQLite and session keys (default /var/lib/deltascope)
  -tls-cert / -tls-key  optional, serve HTTPS when provided
  -session-ttl          session lifetime (default 12h)`)
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
		log.Fatalf("failed to create data directory: %v", err)
	}
	st, err := store.Open(filepath.Join(dataDir, "deltascope.db"))
	if err != nil {
		log.Fatalf("failed to open SQLite: %v", err)
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
		log.Fatalf("failed to write session key: %v", err)
	}
	return b
}

func cmdServe(args []string) {
	fs := flag.NewFlagSet("serve", flag.ExitOnError)
	listen := fs.String("listen", "127.0.0.1:8080", "listen address")
	archive := fs.String("archive", defaultArchive(), "PCP archive directory")
	dataDir := fs.String("data", "/var/lib/deltascope", "data directory")
	tlsCert := fs.String("tls-cert", "", "TLS certificate (optional)")
	tlsKey := fs.String("tls-key", "", "TLS private key (optional)")
	ttl := fs.Duration("session-ttl", 12*time.Hour, "session lifetime")
	catalogPath := fs.String("catalog", "", "custom metric catalog JSON (optional)")
	rulesPath := fs.String("rules", "", "custom diagnosis rules JSON (optional)")
	fs.Parse(args)

	if *catalogPath != "" {
		if err := pcp.LoadCatalogFile(*catalogPath); err != nil {
			log.Fatalf("failed to load catalog: %v", err)
		}
		log.Printf("loaded custom catalog %s (%d metrics)", *catalogPath, len(pcp.Catalog))
	}
	if *rulesPath != "" {
		if err := pcp.LoadRulesFile(*rulesPath); err != nil {
			log.Fatalf("failed to load rules: %v", err)
		}
		log.Printf("loaded custom rules %s (%d rules)", *rulesPath, len(pcp.Rules))
	}

	if _, err := os.Stat(*archive); err != nil {
		log.Printf("warning: archive directory %s is not accessible (%v), check that pmlogger is running", *archive, err)
	}
	if _, err := exec.LookPath("pmlogsummary"); err != nil {
		log.Printf("warning: pmlogsummary not found, install the pcp package")
	}
	if _, err := exec.LookPath("pmrep"); err != nil {
		log.Printf("warning: pmrep not found, install the pcp-system-tools package")
	}

	st := openStore(*dataDir)
	defer st.Close()
	if users, err := st.ListUsers(); err == nil && len(users) == 0 {
		log.Printf("note: no users yet, run deltascope user add <name> to create an admin")
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
		log.Printf("v%s listening HTTPS on %s, archive %s", version, *listen, *archive)
		log.Fatal(h.ListenAndServeTLS(*tlsCert, *tlsKey))
	}
	log.Printf("v%s listening HTTP on %s, archive %s", version, *listen, *archive)
	log.Fatal(h.ListenAndServe())
}

func cmdCatalog(args []string) {
	if len(args) == 0 || args[0] != "export" {
		fmt.Fprintln(os.Stderr, "usage: deltascope catalog export > catalog.json")
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
	return time.Time{}, fmt.Errorf("invalid time format: %q (expected 2006-01-02T15:04)", s)
}

func cmdCompare(args []string) {
	fs := flag.NewFlagSet("compare", flag.ExitOnError)
	aStart := fs.String("a-start", "", "baseline window start")
	aEnd := fs.String("a-end", "", "baseline window end")
	bStart := fs.String("b-start", "", "compare window start")
	bEnd := fs.String("b-end", "", "compare window end")
	archive := fs.String("archive", defaultArchive(), "archive directory")
	threshold := fs.Float64("threshold", 15, "significance threshold %")
	catalogPath := fs.String("catalog", "", "custom metric catalog JSON")
	rulesPath := fs.String("rules", "", "custom diagnosis rules JSON")
	format := fs.String("format", "text", "output format: text|json")
	showAll := fs.Bool("all", false, "text mode: include flat rows")
	noColor := fs.Bool("no-color", false, "disable ANSI color")
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
		fmt.Fprintln(os.Stderr, "usage: deltascope rules export > rules.json")
		os.Exit(2)
	}
	data, err := pcp.ExportRules()
	if err != nil {
		log.Fatal(err)
	}
	os.Stdout.Write(data)
	fmt.Println()
}

func cmdVerify(args []string) {
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "usage: deltascope verify start|report [-name <release-name>] [-format text|md]")
		os.Exit(2)
	}
	sub := args[0]
	fs := flag.NewFlagSet("verify", flag.ExitOnError)
	dataDir := fs.String("data", "/var/lib/deltascope", "data directory")
	name := fs.String("name", "", "release baseline name (default: auto timestamp)")
	format := fs.String("format", "text", "report output format: text | md")
	title := fs.String("title", "", "report title (md format)")
	noColor := fs.Bool("no-color", false, "disable colored output")
	fs.Parse(args[1:])

	st := openStore(*dataDir)
	defer st.Close()
	ss, err := state.NewStore(st.DB())
	if err != nil {
		log.Fatalf("failed to init snapshot store: %v", err)
	}
	host, _ := os.Hostname()
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	switch sub {
	case "start":
		if *name == "" {
			*name = "release-" + time.Now().Format("20060102-150405")
		}
		snap := state.Capture(ctx, host)
		if err := ss.SaveMarker(*name, snap); err != nil {
			log.Fatalf("failed to save baseline: %v", err)
		}
		total := 0
		for _, sec := range snap.Sections {
			total += len(sec.Items)
		}
		fmt.Printf("baseline %q recorded — %s · %d facts\n", *name, snap.Taken.Local().Format("15:04:05"), total)
		fmt.Println("after the release, run: deltascope verify report -name " + *name)

	case "report":
		if *name == "" {
			fmt.Fprintln(os.Stderr, "missing -name (use the name printed by 'verify start')")
			os.Exit(2)
		}
		base, err := ss.LoadMarker(*name)
		if err != nil {
			log.Fatal(err)
		}
		after := state.Capture(ctx, host)
		diff := state.Compare(base, after)
		if *format == "md" {
			state.RenderMarkdown(os.Stdout, diff, *title)
		} else {
			state.RenderText(os.Stdout, diff, !*noColor)
		}
		if diff.Total > 0 {
			os.Exit(3)
		}

	default:
		fmt.Fprintln(os.Stderr, "unknown subcommand, use start or report")
		os.Exit(2)
	}
}

func cmdSnapshot(args []string) {
	fs := flag.NewFlagSet("snapshot", flag.ExitOnError)
	dataDir := fs.String("data", "/var/lib/deltascope", "data directory")
	keep := fs.Int("keep-days", 7, "snapshot retention days")
	quiet := fs.Bool("quiet", false, "summary output only")
	fs.Parse(args)

	st := openStore(*dataDir)
	defer st.Close()
	ss, err := state.NewStore(st.DB())
	if err != nil {
		log.Fatalf("failed to init snapshot store: %v", err)
	}

	host, _ := os.Hostname()
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	snap := state.Capture(ctx, host)
	if err := ss.Save(snap); err != nil {
		log.Fatalf("failed to save snapshot: %v", err)
	}
	if n, _ := ss.Prune(*keep); n > 0 && !*quiet {
		log.Printf("pruned %d expired snapshots", n)
	}

	total := 0
	skipped := 0
	for _, sec := range snap.Sections {
		total += len(sec.Items)
		if sec.Skipped != "" {
			skipped++
		}
	}
	if *quiet {
		fmt.Printf("snapshot %s: %d facts / %d sections\n", snap.Taken.Format("2006-01-02 15:04:05"), total, len(snap.Sections))
		return
	}
	fmt.Printf("snapshot captured %s\n  host %s · %d facts · %d sections\n",
		snap.Taken.Local().Format("2006-01-02 15:04:05"), host, total, len(snap.Sections))
	for _, sec := range snap.Sections {
		if sec.Skipped != "" {
			fmt.Printf("  - %-12s skipped: %s\n", sec.Name, sec.Skipped)
		} else {
			fmt.Printf("  - %-12s %d items\n", sec.Name, len(sec.Items))
		}
	}
}

func cmdStatediff(args []string) {
	fs := flag.NewFlagSet("statediff", flag.ExitOnError)
	dataDir := fs.String("data", "/var/lib/deltascope", "data directory")
	since := fs.Duration("since", 24*time.Hour, "how far back the baseline is from now (alternative to -a/-b)")
	aAt := fs.String("a", "", "baseline time 2006-01-02T15:04 (default: nearest snapshot before -since)")
	bAt := fs.String("b", "", "compare time 2006-01-02T15:04 (default: latest snapshot)")
	noColor := fs.Bool("no-color", false, "disable colored output")
	summary := fs.Bool("summary", false, "single-line summary output (for cron)")
	fs.Parse(args)

	st := openStore(*dataDir)
	defer st.Close()
	ss, err := state.NewStore(st.DB())
	if err != nil {
		log.Fatalf("failed to init snapshot store: %v", err)
	}

	var a, b state.Snapshot
	if *bAt != "" {
		t, err := time.ParseInLocation("2006-01-02T15:04", *bAt, time.Local)
		if err != nil {
			log.Fatalf("invalid -b time: %v", err)
		}
		if b, err = ss.Before(t); err != nil {
			log.Fatalf("no snapshot found for -b: %v", err)
		}
	} else {
		if b, err = ss.Latest(); err != nil {
			log.Fatalf("no snapshots yet, run deltascope snapshot first")
		}
	}
	if *aAt != "" {
		t, err := time.ParseInLocation("2006-01-02T15:04", *aAt, time.Local)
		if err != nil {
			log.Fatalf("invalid -a time: %v", err)
		}
		if a, err = ss.Before(t); err != nil {
			log.Fatalf("no snapshot found for -a: %v", err)
		}
	} else {
		if a, err = ss.NearestBefore(b.Taken.Add(-*since)); err != nil {
			log.Fatalf("no baseline snapshot found: %v", err)
		}
	}

	diff := state.Compare(a, b)
	if *summary {
		fmt.Println(state.RenderSummaryLine(diff))
	} else {
		state.RenderText(os.Stdout, diff, !*noColor)
	}
	if diff.Total > 0 {
		os.Exit(3)
	}
}

func cmdProcDiff(args []string) {
	fs := flag.NewFlagSet("proc-diff", flag.ExitOnError)
	archive := fs.String("archive", defaultArchive(), "archive directory")
	aStart := fs.String("a-start", "", "baseline start 2006-01-02T15:04")
	aEnd := fs.String("a-end", "", "baseline window end")
	bStart := fs.String("b-start", "", "compare window start")
	bEnd := fs.String("b-end", "", "compare window end")
	threshold := fs.Float64("threshold", 20, "significance threshold %")
	noColor := fs.Bool("no-color", false, "disable colored output")
	fs.Parse(args)

	parse := func(v, name string) time.Time {
		t, err := time.ParseInLocation("2006-01-02T15:04", v, time.Local)
		if err != nil {
			log.Fatalf("invalid time %s (expected 2006-01-02T15:04): %v", name, err)
		}
		return t
	}
	if *aStart == "" || *aEnd == "" || *bStart == "" || *bEnd == "" {
		log.Fatal("must provide -a-start -a-end -b-start -b-end")
	}
	w := pcp.Windows{
		AStart: parse(*aStart, "a-start"), AEnd: parse(*aEnd, "a-end"),
		BStart: parse(*bStart, "b-start"), BEnd: parse(*bEnd, "b-end"),
		ThresholdPct: *threshold,
	}

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	rep, err := pcp.CompareProc(ctx, pcp.ExecRunner{}, *archive, w)
	if err != nil {
		log.Fatalf("process diff failed: %v\nhint: enable hotproc collection in pmlogger first (see hotproc.config)", err)
	}
	renderProcReport(os.Stdout, rep, !*noColor)
}

func cmdUser(args []string) {
	fs := flag.NewFlagSet("user", flag.ExitOnError)
	dataDir := fs.String("data", "/var/lib/deltascope", "data directory")
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
			log.Fatal("usage: deltascope user add <name>")
		}
		name := strings.TrimSpace(rest[1])
		if name == "" || len(name) > 64 {
			log.Fatal("username must be non-empty and at most 64 chars")
		}
		pw := os.Getenv("DSCOPE_PASSWORD")
		if pw == "" {
			pw = promptPassword()
		}
		if len(pw) < 8 {
			log.Fatal("password must be at least 8 characters")
		}
		hash, err := auth.HashPassword(pw)
		if err != nil {
			log.Fatal(err)
		}
		if err := st.UpsertUser(name, hash); err != nil {
			log.Fatal(err)
		}
		fmt.Printf("user %s created/updated\n", name)
	case "del":
		if len(rest) < 2 {
			log.Fatal("usage: deltascope user del <name>")
		}
		if err := st.DeleteUser(rest[1]); err != nil {
			log.Fatal(err)
		}
		fmt.Println("deleted")
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
	p1 := read("password: ")
	p2 := read("confirm: ")
	if p1 != p2 {
		log.Fatal("passwords do not match")
	}
	return p1
}
