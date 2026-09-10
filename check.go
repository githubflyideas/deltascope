package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"sort"
	"strings"
	"syscall"
	"time"

	"github.com/githubflyideas/deltascope/internal/native"
	"github.com/githubflyideas/deltascope/internal/reasoning"
)

// check is the answer to "what is wrong with this machine right now" on a
// host with no PCP, no archive and no database. It samples /proc directly,
// runs the same 78 states and 58 diagnoses the archive path runs, and -- the
// part that makes it trustworthy rather than merely available -- prints what
// it could not judge next to what it could. A tool that reports only its
// positives on a partial dataset is how "nothing found" comes to mean
// "nothing looked at".

// checkReport is the JSON shape, kept flat so `deltascope check -format json`
// is greppable from a shell without a JSON processor.
type checkReport struct {
	Host        string             `json:"host"`
	Taken       time.Time          `json:"taken"`
	Samples     int                `json:"samples"`
	ElapsedSec  float64            `json:"elapsed_sec"`
	NCPU        int                `json:"ncpu"`
	Rows        int                `json:"rows"`
	Active      []reasoning.Active `json:"active"`
	Diagnoses   []reasoning.Result `json:"diagnoses"`
	Unevaluated map[string]string  `json:"unevaluated"`
	Counts      map[string]float64 `json:"counts,omitempty"`
	Severity    string             `json:"severity"`
	Headline    string             `json:"headline"`
}

func cmdCheck(args []string) {
	fs := flag.NewFlagSet("check", flag.ExitOnError)
	window := fs.Duration("for", 10*time.Second, "how long to sample (longer windows unlock the burst states)")
	interval := fs.Duration("interval", time.Second, "delay between samples")
	format := fs.String("format", "text", "text or json")
	all := fs.Bool("all", false, "also list the states that were checked and did not hold")
	noColor := fs.Bool("no-color", false, "disable colored output")
	fs.Parse(args)

	if *interval <= 0 {
		fmt.Fprintln(os.Stderr, "-interval must be positive")
		os.Exit(2)
	}
	// Sampling n times interval apart spans (n-1) intervals, so the count is
	// derived from the requested wall-clock span rather than guessed. Two is
	// the floor: one sample answers every gauge state and no rate at all.
	samples := int(*window / *interval) + 1
	if samples < 2 {
		samples = 2
	}

	// Ctrl-C reports what it has instead of throwing the window away: a
	// partial window still answers every absolute state, and the operator
	// interrupting is usually the one who already knows what they saw.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if *format == "text" {
		fmt.Fprintf(os.Stderr, "sampling /proc: %d samples over %s ...\n", samples, *window)
	}
	w, err := native.Collect(ctx, samples, *interval)
	if err != nil && len(w.Rows) == 0 {
		fmt.Fprintf(os.Stderr, "deltascope: %v\n", err)
		os.Exit(1)
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "deltascope: collection ended early: %v\n", err)
	}

	machine := reasoning.Host()
	active := reasoning.EvaluateOn(reasoning.States, w.Rows, machine)
	results := reasoning.Diagnose(reasoning.Diagnoses, active)
	gaps := reasoning.Unevaluated(reasoning.States, w.Rows)

	host, _ := os.Hostname()
	rep := checkReport{
		Host:        host,
		Taken:       time.Now(),
		Samples:     w.Samples,
		ElapsedSec:  w.Elapsed.Seconds(),
		NCPU:        machine.NCPU,
		Rows:        len(w.Rows),
		Active:      sortedActive(active),
		Diagnoses:   results,
		Unevaluated: gaps,
		Counts:      notableCounts(w.Increments),
	}
	rep.Severity, rep.Headline = checkVerdict(results, active, gaps)

	if *format == "json" {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		if err := enc.Encode(rep); err != nil {
			fmt.Fprintf(os.Stderr, "deltascope: %v\n", err)
			os.Exit(1)
		}
	} else {
		renderCheck(os.Stdout, rep, *all, !*noColor)
	}

	// Exit 2 on a critical finding, matching `compare`. A machine that could
	// not be measured exits 1: scripts that treat 0 as "fine" must not read
	// an unmeasured host as fine.
	switch rep.Severity {
	case "crit":
		os.Exit(2)
	case "unknown":
		os.Exit(1)
	}
}

// checkVerdict reduces the run to one line. The order matters: a root-cause
// diagnosis outranks a consequence, a consequence outranks a bare state, and
// "nothing fired" only counts as healthy when something was actually
// measured.
func checkVerdict(results []reasoning.Result, active map[string]reasoning.Active, gaps map[string]string) (string, string) {
	var firstRootCrit, firstCrit, firstWarn *reasoning.Result
	for i := range results {
		r := &results[i]
		if r.Severity == "crit" {
			if firstCrit == nil {
				firstCrit = r
			}
			if r.IsRoot && firstRootCrit == nil {
				firstRootCrit = r
			}
		}
		if r.Severity == "warn" && firstWarn == nil {
			firstWarn = r
		}
	}
	switch {
	case firstRootCrit != nil:
		return "crit", firstRootCrit.Conclusion
	case firstCrit != nil:
		return "crit", firstCrit.Conclusion
	case firstWarn != nil:
		return "warn", firstWarn.Conclusion
	case len(active) > 0:
		return "warn", fmt.Sprintf("%d state(s) hold but no diagnosis pattern matched them", len(active))
	case len(gaps) == len(reasoning.States):
		return "unknown", "nothing could be measured on this host"
	default:
		return "ok", fmt.Sprintf("%d state(s) checked and none hold", len(reasoning.States)-len(gaps))
	}
}

func sortedActive(active map[string]reasoning.Active) []reasoning.Active {
	out := make([]reasoning.Active, 0, len(active))
	for _, a := range active {
		out = append(out, a)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Domain != out[j].Domain {
			return out[i].Domain < out[j].Domain
		}
		return out[i].ID < out[j].ID
	})
	return out
}

// notableCounts keeps the raw window increments whose states are defeated by
// rate conversion. "mem.vmstat.oom_kill BGte 1" means one kill per second,
// so a machine that OOM-killed a process during a 10 s window does not trip
// it -- the state is right that a single kill matters and wrong about the
// unit. Printing the count keeps the fact visible while the threshold is
// what it is.
var notableCounters = []string{
	"mem.vmstat.oom_kill",
	"mem.vmstat.allocstall",
	"network.tcp.listendrops",
	"network.tcp.listenoverflows",
	"network.softnet.dropped",
	"network.softnet.time_squeeze",
	"network.interface.in.drops",
	"network.interface.out.drops",
	"network.interface.in.errors",
	"network.interface.out.errors",
	"network.interface.collisions",
	"network.udp.recvbuferrors",
	"network.udp.sndbuferrors",
	"network.tcp.syncookiessent",
}

func notableCounts(inc map[string]float64) map[string]float64 {
	out := map[string]float64{}
	for _, m := range notableCounters {
		for key, v := range inc {
			// Increments are keyed by metric or metric[instance], so a
			// per-NIC drop count matches on the metric prefix.
			if v > 0 && (key == m || strings.HasPrefix(key, m+"[")) {
				out[key] = v
			}
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}
