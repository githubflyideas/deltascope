package httpapi

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log"
	"net"
	"net/http"
	"os"
	"sort"
	"strconv"
	"time"

	"github.com/githubflyideas/deltascope/internal/auth"
	"github.com/githubflyideas/deltascope/internal/diagnose"
	"github.com/githubflyideas/deltascope/internal/native"
	"github.com/githubflyideas/deltascope/internal/pcp"
	"github.com/githubflyideas/deltascope/internal/reasoning"
	"github.com/githubflyideas/deltascope/internal/state"
	"github.com/githubflyideas/deltascope/internal/store"
)

const (
	sessionCookie = "ds_session"
	execTimeout   = 60 * time.Second
)

type Server struct {
	Store      *store.Store
	StateStore *state.Store
	Sessions   *auth.Sessions
	Limiter    *auth.RateLimiter
	Runner     pcp.Runner
	Archive    string
	Version    string
	WebFS      fs.FS
	SecureCk   bool
	Caps       Capabilities

	// Sampler is the fallback metric source for the reasoning chain on a host
	// with no PCP: a rolling /proc history, already running when the request
	// arrives. Left nil when PCP is present, because the archive path can
	// answer any window the user picks and this one only ever answers "now".
	Sampler ProcSampler
}

// ProcSampler is the rolling /proc history the reasoning chain falls back to,
// satisfied by *native.Sampler.
//
// An interface rather than the concrete type because what this fallback has to
// get right is its behaviour on a machine it cannot measure -- too few samples
// yet, /proc absent -- and none of that is reachable through a real sampler on
// a host where sampling works. Keeping it injectable is what makes the
// warming-up and unmeasurable paths testable anywhere.
type ProcSampler interface {
	// Window judges the recent half of the history against the half before
	// it, or reports why it cannot.
	Window(thresholdPct float64) (native.Window, error)
	// Interval is the sampling period, for telling a reader how long until
	// the window widens.
	Interval() time.Duration
}

func (s *Server) Routes() http.Handler {
	mux := http.NewServeMux()

	static, _ := fs.Sub(s.WebFS, "web/static")
	mux.Handle("GET /static/", http.StripPrefix("/static/", http.FileServerFS(static)))

	mux.HandleFunc("GET /login", s.servePage("web/login.html", false))
	mux.HandleFunc("POST /api/login", s.handleLogin)
	mux.HandleFunc("POST /api/logout", s.handleLogout)

	mux.HandleFunc("GET /{$}", s.servePage("web/index.html", true))
	mux.Handle("GET /api/me", s.requireAuth(s.handleMe))
	mux.Handle("GET /api/catalog", s.requireAuth(s.handleCatalog))
	mux.Handle("GET /api/diff", s.requireAuth(s.handleDiff))
	mux.Handle("GET /api/trend", s.requireAuth(s.handleTrend))
	mux.Handle("GET /api/procdiff", s.requireAuth(s.handleProcDiff))
	mux.Handle("GET /api/statediff", s.requireAuth(s.handleStateDiff))
	mux.Handle("GET /api/diagnose", s.requireAuth(s.handleDiagnose))
	mux.Handle("GET /api/reasoning", s.requireAuth(s.handleReasoning))
	mux.HandleFunc("GET /api/version", s.handleVersion)

	return securityHeaders(mux)
}

func securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("X-Frame-Options", "DENY")
		h.Set("Referrer-Policy", "no-referrer")
		h.Set("Content-Security-Policy",
			"default-src 'self'; script-src 'self'; style-src 'self'; img-src 'self' data:; connect-src 'self'")
		next.ServeHTTP(w, r)
	})
}

func (s *Server) servePage(path string, needAuth bool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if needAuth && s.currentUser(r) == "" {
			http.Redirect(w, r, "/login", http.StatusFound)
			return
		}
		data, err := fs.ReadFile(s.WebFS, path)
		if err != nil {
			http.Error(w, "page not found", http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Write(data)
	}
}

// currentUser returns the signed-in user, or "" when the request carries no
// usable session.
//
// This deliberately goes back to the user table on every request instead of
// trusting the cookie alone. The cookie is a signed, self-contained token, so
// on its own it keeps working for its full TTL no matter what happens to the
// account -- delete the account or change its password and every session
// opened with it stayed live. Now that accounts are declared with -user and
// reconciled at every start, that check is what makes editing the flag mean
// something: changing the password there has to take the old holder out, and it
// only does because the fingerprint is re-read here.
func (s *Server) currentUser(r *http.Request) string {
	c, err := r.Cookie(sessionCookie)
	if err != nil {
		return ""
	}
	cl, ok := s.Sessions.Verify(c.Value)
	if !ok {
		return ""
	}
	hash, err := s.Store.PasswordHash(cl.User)
	if err != nil {
		// Fail closed, including on a database error: a request that cannot
		// be checked is not a request that has been authorized.
		if !errors.Is(err, store.ErrNotFound) {
			log.Printf("auth: could not check the session's account: %v", err)
		}
		return ""
	}
	if subtle.ConstantTimeCompare([]byte(auth.Fingerprint(hash)), []byte(cl.FP)) != 1 {
		return ""
	}
	return cl.User
}

func (s *Server) requireAuth(next http.HandlerFunc) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		u := s.currentUser(r)
		if u == "" {
			writeErr(w, http.StatusUnauthorized, "not logged in or session expired")
			return
		}
		next(w, r.WithContext(context.WithValue(r.Context(), ctxUser{}, u)))
	})
}

type ctxUser struct{}

func (s *Server) handleLogin(w http.ResponseWriter, r *http.Request) {
	ip := clientIP(r)
	if ok, wait := s.Limiter.Allow(ip); !ok {
		writeErr(w, http.StatusTooManyRequests,
			"too many failed attempts, try again in "+strconv.Itoa(int(wait.Minutes())+1)+" minute(s)")
		return
	}

	var req struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(&req); err != nil ||
		req.Username == "" || req.Password == "" {
		writeErr(w, http.StatusBadRequest, "username and password required")
		return
	}

	hash, err := s.Store.PasswordHash(req.Username)
	if errors.Is(err, store.ErrNotFound) {
		auth.VerifyPassword("pbkdf2-sha256$600000$AAAAAAAAAAAAAAAAAAAAAA$AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA", req.Password)
		s.Limiter.Fail(ip)
		writeErr(w, http.StatusUnauthorized, "invalid username or password")
		return
	}
	if err != nil {
		log.Printf("login: failed to read user: %v", err)
		writeErr(w, http.StatusInternalServerError, "internal error")
		return
	}
	if !auth.VerifyPassword(hash, req.Password) {
		s.Limiter.Fail(ip)
		writeErr(w, http.StatusUnauthorized, "invalid username or password")
		return
	}

	s.Limiter.Reset(ip)
	http.SetCookie(w, &http.Cookie{
		Name: sessionCookie, Value: s.Sessions.Issue(req.Username, auth.Fingerprint(hash)),
		Path: "/", HttpOnly: true, SameSite: http.SameSiteStrictMode,
		Secure: s.SecureCk, MaxAge: int(s.Sessions.TTL.Seconds()),
	})
	writeJSON(w, map[string]string{"user": req.Username})
}

func (s *Server) handleLogout(w http.ResponseWriter, r *http.Request) {
	http.SetCookie(w, &http.Cookie{
		Name: sessionCookie, Value: "", Path: "/", HttpOnly: true, MaxAge: -1,
	})
	writeJSON(w, map[string]string{"ok": "1"})
}

func (s *Server) handleMe(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, map[string]any{
		"user":         r.Context().Value(ctxUser{}),
		"archive":      s.Archive,
		"version":      s.Version,
		"capabilities": s.Caps,
	})
}

// metricsNote returns the reason the metric leg cannot run on this host, or
// "" when it can. Engines that degrade leg-by-leg pass this through instead
// of failing the whole request.
func (s *Server) metricsNote() string {
	if s.Caps.Metrics {
		return ""
	}
	if s.Caps.Reason != "" {
		return "Performance metrics unavailable: " + s.Caps.Reason
	}
	return "Performance metrics unavailable: PCP archives are not available on this host."
}

// requireMetrics rejects the archive-backed engines when this host has no
// usable PCP source, with the reason the operator needs rather than a
// generic gateway error. 503 rather than 502: nothing failed, the feature
// simply is not available here.
func (s *Server) requireMetrics(w http.ResponseWriter) bool {
	if s.Caps.Metrics {
		return true
	}
	reason := s.Caps.Reason
	if reason == "" {
		reason = "PCP archives are not available on this host."
	}
	writeErr(w, http.StatusServiceUnavailable, reason)
	return false
}

func (s *Server) handleCatalog(w http.ResponseWriter, r *http.Request) {
	presets := make(map[string]any, len(pcp.TrendPresets))
	for k, v := range pcp.TrendPresets {
		presets[k] = map[string]any{"label": v.Label, "metrics": v.Metrics}
	}
	writeJSON(w, map[string]any{
		"categories": pcp.Categories,
		"metrics":    pcp.Catalog,
		"presets":    presets,
	})
}

func (s *Server) handleDiff(w http.ResponseWriter, r *http.Request) {
	if !s.requireMetrics(w) {
		return
	}
	q := r.URL.Query()
	aStart, err1 := parseLocal(q.Get("a_start"))
	aEnd, err2 := parseLocal(q.Get("a_end"))
	bStart, err3 := parseLocal(q.Get("b_start"))
	bEnd, err4 := parseLocal(q.Get("b_end"))
	if err1 != nil || err2 != nil || err3 != nil || err4 != nil {
		writeErr(w, http.StatusBadRequest, "invalid time parameter, expected 2026-07-03T14:00")
		return
	}
	threshold := 15.0
	if t := q.Get("threshold"); t != "" {
		v, err := strconv.ParseFloat(t, 64)
		if err != nil || v < 0 || v > 10000 {
			writeErr(w, http.StatusBadRequest, "threshold must be a number between 0 and 10000")
			return
		}
		threshold = v
	}
	if err := checkWindow(aStart, aEnd); err != nil {
		writeErr(w, http.StatusBadRequest, "window A: "+err.Error())
		return
	}
	if err := checkWindow(bStart, bEnd); err != nil {
		writeErr(w, http.StatusBadRequest, "window B: "+err.Error())
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), execTimeout)
	defer cancel()
	rep, err := pcp.Compare(ctx, s.Runner, s.Archive, pcp.Windows{
		AStart: aStart, AEnd: aEnd, BStart: bStart, BEnd: bEnd,
		ThresholdPct: threshold,
	})
	if err != nil {
		log.Printf("diff: %v", err)
		writeErr(w, http.StatusBadGateway, "archive query failed, check server logs or confirm the selected window has data")
		return
	}
	// Run the reasoning chain over the very same rows the diff produced, so
	// the custom-window comparison inherits peak awareness and root-cause
	// convergence for free instead of maintaining a second, mean-only
	// verdict path. This is why the perf-compare view no longer misses a
	// sub-window spike that every other view catches: it now shares the
	// engine rather than reimplementing a weaker one.
	reasoningResults := reasoning.Diagnose(reasoning.Diagnoses, reasoning.Evaluate(reasoning.States, rep.Rows))
	writeJSON(w, struct {
		*pcp.DiffReport
		Reasoning []reasoning.Result `json:"reasoning,omitempty"`
	}{rep, reasoningResults})
}

func (s *Server) handleTrend(w http.ResponseWriter, r *http.Request) {
	if !s.requireMetrics(w) {
		return
	}
	q := r.URL.Query()
	start, err1 := parseLocal(q.Get("start"))
	end, err2 := parseLocal(q.Get("end"))
	if err1 != nil || err2 != nil {
		writeErr(w, http.StatusBadRequest, "invalid time parameter, expected 2026-07-03T14:00")
		return
	}
	if err := checkWindow(start, end); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	preset := q.Get("preset")

	ctx, cancel := context.WithTimeout(r.Context(), execTimeout)
	defer cancel()
	series, missing, err := pcp.RunTrend(ctx, s.Runner, s.Archive, preset, start, end)
	if err != nil {
		log.Printf("trend: %v", err)
		writeErr(w, http.StatusBadGateway, "archive query failed, check server logs or confirm the selected window has data")
		return
	}
	sort.Slice(series, func(i, j int) bool { return series[i].Name < series[j].Name })
	writeJSON(w, map[string]any{"series": series, "missing": missing})
}

func (s *Server) handleVersion(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, map[string]any{"version": s.Version})
}

func (s *Server) handleDiagnose(w http.ResponseWriter, r *http.Request) {
	threshold := 15.0
	if t := r.URL.Query().Get("threshold"); t != "" {
		if v, err := strconv.ParseFloat(t, 64); err == nil && v >= 0 && v <= 10000 {
			threshold = v
		}
	}
	ctx, cancel := context.WithTimeout(r.Context(), execTimeout)
	defer cancel()
	d, err := diagnose.Run(ctx, diagnose.Deps{
		Runner: s.Runner, Archive: s.Archive,
		StateStore: s.StateStore, Threshold: threshold,
		MetricsUnavailable: s.metricsNote(),
		// The same sampler the reasoning tab falls back to. Passing it here is
		// what keeps the one-click page from being the weakest screen on a
		// PCP-less host: without it the metric leg contributes nothing but a
		// note saying pcp is not installed, and the page reduces to process
		// and config changes with no performance evidence to correlate them
		// against. A nil interface when there is no sampler, which Run reads
		// as "skip the fallback" -- see the assignment in cmdServe for why
		// this is never a typed nil.
		Native: s.diagnoseNative(),
	})
	if err != nil {
		log.Printf("diagnose: %v", err)
		writeErr(w, http.StatusBadGateway, err.Error())
		return
	}
	writeJSON(w, d)
}

// diagnoseNative narrows the server's sampler to what diagnose needs, and
// returns a genuinely nil interface when there is none.
//
// The explicit nil check is the whole point: `return s.Sampler` would convert
// a nil ProcSampler to a nil NativeSource correctly, but it would also happily
// carry across a non-nil interface wrapping a nil *native.Sampler, and
// diagnose.Run tests that field for nil to decide whether the fallback exists.
// One line here is cheaper than a nil-pointer panic inside a goroutine whose
// only visible effect is a diagnose page that returns 502.
func (s *Server) diagnoseNative() diagnose.NativeSource {
	if s.Sampler == nil {
		return nil
	}
	return s.Sampler
}

// handleReasoning runs the experimental two-layer chain: metric rows are
// first reduced to named states (state.cpu.steal_high, ...), then
// diagnoses are matched against combinations of those states including
// negation. This runs alongside the original rule engine rather than
// replacing it, so both can be compared against the same real data
// before deciding whether to migrate.
//
// Two metric sources can feed it. An archive is preferred whenever one
// exists; without one the rolling /proc sampler answers instead. Neither is
// asked about a caller-chosen window: see the fixed-window note below.
func (s *Server) handleReasoning(w http.ResponseWriter, r *http.Request) {
	threshold := 15.0
	if t := r.URL.Query().Get("threshold"); t != "" {
		if v, err := strconv.ParseFloat(t, 64); err == nil && v >= 0 && v <= 10000 {
			threshold = v
		}
	}
	if !s.Caps.Metrics {
		s.reasoningFromProc(w, threshold)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), execTimeout)
	defer cancel()

	// The window here is fixed, and that is a product decision rather than a
	// missing feature. This tab answers "what is wrong on this machine right
	// now": the last 30 minutes against the 30 minutes before them. A
	// caller-chosen window is what the metric-comparison tab is for, and
	// offering the same four pickers here made the two screens read as
	// duplicates of each other while giving this one nothing the other did
	// not already have.
	//
	// The recent baseline is also the correct one for state semantics. A
	// day-old baseline keeps a state active long after the process that
	// caused it has exited, because yesterday at this time really was
	// different; "recent vs just-before" tracks the machine as it is now and
	// clears once the burst is over.
	now := time.Now()
	win := pcp.Windows{
		AStart: now.Add(-60 * time.Minute), AEnd: now.Add(-30 * time.Minute),
		BStart: now.Add(-30 * time.Minute), BEnd: now,
		ThresholdPct: threshold,
	}
	w2 := diagnose.Window{
		AStart: win.AStart, AEnd: win.AEnd, BStart: win.BStart, BEnd: win.BEnd,
		Label: "last 30 min vs the 30 min before",
	}
	rep, err := pcp.Compare(ctx, s.Runner, s.Archive, win)
	if err != nil {
		log.Printf("reasoning: %v", err)
		writeErr(w, http.StatusBadGateway, err.Error())
		return
	}

	procs, procNote := s.reasoningProcs(w2)
	writeReasoning(w, w2, "pcp", rep.Rows, joinNotes("", procNote), procs)
}

// reasoningFromProc answers the same chain off the rolling sampler when this
// host has no archive. The window is not negotiable on either path, but here
// it is also not even knowable in advance: /proc has no history, so the only
// two windows that exist are the ones the sampler is holding, and the label
// reports the span that was actually measured. Quietly presenting "now" as
// some other window would be the worse failure.
func (s *Server) reasoningFromProc(w http.ResponseWriter, threshold float64) {
	if s.Sampler == nil {
		reason := s.Caps.Reason
		if reason == "" {
			reason = "PCP archives are not available on this host."
		}
		writeErr(w, http.StatusServiceUnavailable, reason)
		return
	}
	nw, err := s.Sampler.Window(threshold)
	if err != nil {
		// 503 rather than 502: on a host still warming up nothing has failed,
		// and the message carries how many samples it has so the operator can
		// tell "wait a minute" apart from "this will never work".
		writeErr(w, http.StatusServiceUnavailable, err.Error())
		return
	}

	win := diagnose.Window{
		BStart: nw.Start, BEnd: nw.End,
		AStart: nw.BaselineStart, AEnd: nw.BaselineEnd,
		Label: fmt.Sprintf("last %s read from /proc, no PCP archive on this host",
			nw.Elapsed.Round(time.Second)),
	}
	note := ""
	if nw.BaselineSamples > 0 {
		win.Label = fmt.Sprintf("last %s vs the %s before, read from /proc",
			nw.Elapsed.Round(time.Second), nw.BaselineElapsed.Round(time.Second))
	} else {
		// Said out loud because two states out of the catalog need a baseline
		// and will be listed as unmeasured until there is one. Without this
		// note the reader sees a gap with no cause.
		wait := time.Duration(4-nw.Samples) * s.Sampler.Interval()
		note = fmt.Sprintf("The /proc sampler has only %d sample(s) so far, which is not enough to split into "+
			"two windows; the two states that compare against a baseline stay unmeasured for another %s.",
			nw.Samples, wait.Round(time.Second))
	}
	// Process figures come from the snapshot store, which does not need PCP,
	// so this leg works here exactly as it does on the archive path. It is
	// also the only leg that can name a process: the metric rows the chain
	// reasons over are machine-wide by construction.
	procs, procNote := s.reasoningProcs(win)
	writeReasoning(w, win, "proc", nw.Rows, joinNotes(note, procNote), procs)
}

// reasoningProcs is the process leg of the reasoning report: the same
// snapshot comparison the one-click page runs, over this tab's window.
//
// It exists because the chain itself is structurally incapable of naming a
// culprit -- every metric it reasons over is machine-wide -- so a screen that
// showed only states and diagnoses could describe a CPU problem in full and
// still not say which process was causing it. The snapshot store is
// independent of PCP, so there is no host where the states are answerable and
// this is not.
//
// A returned note is a reason the figures are partial, never a failure: an
// empty ProcDiff with an explanation beats a blank panel.
func (s *Server) reasoningProcs(w diagnose.Window) ([]state.ProcRow, string) {
	if s.StateStore == nil {
		return nil, ""
	}
	if n, err := s.StateStore.Count(); err != nil || n < 2 {
		return nil, "Process attribution needs at least two snapshots; the server captures one every 10 minutes."
	}
	// A 30-minute window against a 10-minute snapshot cadence means the
	// nearest snapshot can legitimately sit outside it, so the tolerance has
	// to exceed the cadence or a correctly-running server would report no
	// coverage.
	const tol = 30 * time.Minute
	b1, e1 := s.StateStore.Nearest(w.BStart, tol)
	b2, e2 := s.StateStore.Nearest(w.BEnd, tol)
	if e1 != nil || e2 != nil {
		return nil, "No process snapshots cover this window yet."
	}
	note := ""
	a1, e3 := s.StateStore.Nearest(w.AStart, tol)
	a2, e4 := s.StateStore.Nearest(w.AEnd, tol)
	if e3 != nil || e4 != nil {
		// Comparing the compare window against itself yields flat verdicts
		// rather than fabricated growth, and topProcs then falls back to the
		// heaviest processes -- which is still the answer to "who is using
		// this machine", just not "who changed".
		a1, a2 = b1, b1
		note = "No snapshots cover the baseline half; process figures are for the recent half only."
	}
	pd := state.CompareProcesses(a1, a2, b1, b2, 20, 1, 10240)
	return topProcs(pd.Rows, 12), note
}

// topProcs picks the rows worth showing: the ones that moved, or the heaviest
// few when nothing did.
func topProcs(rows []state.ProcRow, limit int) []state.ProcRow {
	active := make([]state.ProcRow, 0, len(rows))
	for _, r := range rows {
		if r.Verdict != state.PVFlat {
			active = append(active, r)
		}
	}
	if len(active) == 0 {
		active = append(active, rows...)
		sort.Slice(active, func(i, j int) bool {
			return procCPU(active[i]) > procCPU(active[j])
		})
	}
	if len(active) > limit {
		active = active[:limit]
	}
	return active
}

func procCPU(r state.ProcRow) float64 {
	if r.CPUPctB != nil {
		return *r.CPUPctB
	}
	return 0
}

// joinNotes concatenates the reasons a report is partial. Both legs can have
// something to say and the reader needs both, so neither wins.
func joinNotes(a, b string) string {
	switch {
	case a == "":
		return b
	case b == "":
		return a
	}
	return a + " " + b
}

// writeReasoning is the shared tail of both paths. Source is named in the
// response because the two are not interchangeable to a reader: an archive
// answer covers the last half hour, a /proc answer covers the last minute or
// two, and neither can be moved.
//
// Beyond the chain's own output this assembles the two things that make this
// screen the complete report rather than the narrowest one: the metric
// engine's triage over the very same rows, and the resources where the two
// engines disagree. The disagreement is the point. The metric engine is
// relative -- it flags a big change against the baseline -- while every state
// in the catalog is absolute or scale-relative. So a load average that jumped
// tenfold off an idle baseline lights up triage and satisfies no state, and a
// reader looking at one warn about the network would otherwise conclude the
// CPU was fine when the other engine had just called it degraded.
func writeReasoning(w http.ResponseWriter, win diagnose.Window, source string,
	rows []pcp.DiffRow, note string, procs []state.ProcRow) {
	active := reasoning.Evaluate(reasoning.States, rows)
	results := reasoning.Diagnose(reasoning.Diagnoses, active)
	gaps := reasoning.Unevaluated(reasoning.States, rows)
	triage := pcp.Triage(rows)

	out := map[string]any{
		"window":      win,
		"machine":     reasoning.Host(),
		"source":      source,
		"states":      stateViews(reasoning.States, active, gaps),
		"diagnoses":   results,
		"triage":      triage,
		"unexplained": unexplained(triage, results),
	}
	if len(procs) > 0 {
		out["processes"] = procs
	}
	if note != "" {
		out["note"] = note
	}
	writeJSON(w, out)
}

// reasoningDomains maps a triage block key to the state domains that speak for
// it. Disk is two domains because throughput and capacity are separate
// concerns that the metric engine files under one heading.
var reasoningDomains = map[string][]string{
	"cpu":  {"cpu"},
	"mem":  {"memory"},
	"disk": {"io", "filesystem"},
	"net":  {"network"},
}

// reasoningBranchKey is the reverse direction: which triage block a diagnosis
// belongs to. Software-branch diagnoses map to nothing on purpose -- they are
// about a change rather than a resource, so they neither explain nor fail to
// explain a resource being flagged.
var reasoningBranchKey = map[reasoning.Branch]string{
	reasoning.BranchCPU:     "cpu",
	reasoning.BranchMemory:  "mem",
	reasoning.BranchIO:      "disk",
	reasoning.BranchNetwork: "net",
}

// unexplainedBlock is one resource the metric engine flagged that the chain
// produced no diagnosis for. Domains is carried so the UI can show which
// states were checked and came back quiet, which is the answer to "why did
// the chain not conclude anything" -- it is in the payload already, this just
// says where to look. The phrasing is left to the frontend, which has the
// translations; this stays structural.
type unexplainedBlock struct {
	Key      string   `json:"key"`
	Label    string   `json:"label"`
	Status   string   `json:"status"`
	Headline string   `json:"headline"`
	WorstPct *float64 `json:"worst_pct,omitempty"`
	Domains  []string `json:"domains"`
}

// unexplained lists the resources triage called degraded that no diagnosis
// covers. An empty result is the good case: either nothing was flagged, or
// everything flagged has a conclusion attached.
func unexplained(triage []pcp.TriageBlock, results []reasoning.Result) []unexplainedBlock {
	covered := make(map[string]bool, len(results))
	for _, r := range results {
		if k, ok := reasoningBranchKey[r.Branch]; ok {
			covered[k] = true
		}
	}
	out := []unexplainedBlock{}
	for _, b := range triage {
		if b.Status == pcp.TriageOK || covered[b.Key] {
			continue
		}
		out = append(out, unexplainedBlock{
			Key: b.Key, Label: b.Label, Status: string(b.Status),
			Headline: b.Headline, WorstPct: b.WorstPct,
			Domains: reasoningDomains[b.Key],
		})
	}
	return out
}

// stateView is one state's answer for this window. Reason is why the state
// could not be judged; non-empty implies the state is neither active nor
// quiet, so the UI keys off this one field rather than needing a flag too.
type stateView struct {
	ID       string   `json:"id"`
	Domain   string   `json:"domain"`
	Active   bool     `json:"active"`
	Evidence []string `json:"evidence,omitempty"`
	Reason   string   `json:"reason,omitempty"`
}

// stateViews reports every state in one of three conditions, not two: it
// held, it was checked and did not hold, or it could not be checked at all.
// The negative evidence is what makes a diagnosis auditable, but only if the
// reader can tell it apart from absent evidence -- a state whose metric this
// archive never recorded is not a state that came back clean, and collapsing
// the two is how a screen full of quiet rows comes to mean "this machine is
// fine" about something nobody measured.
//
// Split out of the handler so the three-way mapping is testable without an
// archive: it is the invariant the whole screen rests on.
func stateViews(catalog []reasoning.State, active map[string]reasoning.Active, gaps map[string]string) []stateView {
	out := make([]stateView, 0, len(catalog))
	for _, st := range catalog {
		v := stateView{ID: st.ID, Domain: st.Domain}
		if a, on := active[st.ID]; on {
			// A state that fired was measured by definition, so a stale gap
			// entry for it can never win here -- Active and Reason are
			// mutually exclusive on purpose.
			v.Active, v.Evidence = true, a.Evidence
		} else {
			v.Reason = gaps[st.ID]
		}
		out = append(out, v)
	}
	return out
}

func (s *Server) handleStateDiff(w http.ResponseWriter, r *http.Request) {
	if s.StateStore == nil {
		writeErr(w, http.StatusServiceUnavailable, "change accounting is not enabled on this server")
		return
	}
	q := r.URL.Query()
	sinceStr := q.Get("since")
	if sinceStr == "" {
		sinceStr = "24h"
	}
	since, err := time.ParseDuration(sinceStr)
	if err != nil || since <= 0 {
		writeErr(w, http.StatusBadRequest, "invalid since duration, expected e.g. 24h")
		return
	}

	host, _ := os.Hostname()
	ctx, cancel := context.WithTimeout(r.Context(), 60*time.Second)
	defer cancel()

	after := state.Capture(ctx, host)
	if err := s.StateStore.Save(after); err != nil {
		log.Printf("statediff: failed to persist snapshot: %v", err)
	}

	before, err := s.StateStore.NearestBefore(after.Taken.Add(-since))
	if err != nil {
		// no history yet: capture a second live snapshot so the
		// endpoint still works on a machine with zero prior snapshots.
		before = state.Capture(ctx, host)
	}

	diff := state.Compare(before, after)
	writeJSON(w, map[string]any{
		"a_time":          before.Taken,
		"b_time":          after.Taken,
		"total":           diff.Total,
		"sections":        stateDiffJSON(diff),
		"schema_boundary": diff.SchemaBoundary,
	})
}

func stateDiffJSON(d state.Diff) []map[string]any {
	out := make([]map[string]any, 0, len(d.Sections))
	for _, sd := range d.Sections {
		changes := make([]map[string]any, 0, len(sd.Changes))
		for _, ch := range sd.Changes {
			changes = append(changes, map[string]any{
				"key": ch.Key, "kind": string(ch.Kind), "old": ch.Old, "new": ch.New, "note": ch.Note,
			})
		}
		out = append(out, map[string]any{"name": sd.Name, "title": sd.Title, "changes": changes})
	}
	return out
}

func (s *Server) handleProcDiff(w http.ResponseWriter, r *http.Request) {
	if s.StateStore == nil {
		writeErr(w, http.StatusServiceUnavailable, "process accounting is not enabled on this server")
		return
	}
	q := r.URL.Query()
	aStart, err1 := parseLocal(q.Get("a_start"))
	aEnd, err2 := parseLocal(q.Get("a_end"))
	bStart, err3 := parseLocal(q.Get("b_start"))
	bEnd, err4 := parseLocal(q.Get("b_end"))
	if err1 != nil || err2 != nil || err3 != nil || err4 != nil {
		writeErr(w, http.StatusBadRequest, "invalid time parameter, expected 2026-07-03T14:00")
		return
	}
	threshold := 20.0
	if t := q.Get("threshold"); t != "" {
		if v, err := strconv.ParseFloat(t, 64); err == nil && v >= 0 && v <= 10000 {
			threshold = v
		}
	}

	d, note := s.procDiffFromSnapshots(aStart, aEnd, bStart, bEnd, threshold)
	if note != "" {
		writeJSON(w, map[string]any{
			"rows": []any{}, "restarts": []any{},
			"no_data": true, "no_data_hint": note,
		})
		return
	}
	writeJSON(w, d)
}

// procDiffFromSnapshots lines up two snapshot pairs against the requested
// windows. A CPU rate needs two cumulative readings, so each window is
// bounded by the snapshots nearest its start and end.
func (s *Server) procDiffFromSnapshots(aStart, aEnd, bStart, bEnd time.Time, threshold float64) (state.ProcDiff, string) {
	n, err := s.StateStore.Count()
	if err != nil || n < 2 {
		return state.ProcDiff{}, "Not enough snapshots yet. The server captures state every " +
			state.DefaultSnapshotInterval.String() + "; process accounting needs at least two " +
			"captures in each window. Wait for the next capture and try again."
	}
	tol := 30 * time.Minute
	a1, e1 := s.StateStore.Nearest(aStart, tol)
	a2, e2 := s.StateStore.Nearest(aEnd, tol)
	b1, e3 := s.StateStore.Nearest(bStart, tol)
	b2, e4 := s.StateStore.Nearest(bEnd, tol)
	if e3 != nil || e4 != nil {
		return state.ProcDiff{}, "No snapshots cover the compare window. Snapshots start " +
			"accumulating when the server starts, so a window from before that has no data."
	}
	if e1 != nil || e2 != nil {
		// No baseline window: still useful -- show window B on its own.
		a1, a2 = b1, b1
	}
	// minCPUPct 1% of a core and minRSSKB 10MB are the absolute floors, so
	// a process idling at 0.01% doesn't post a huge percentage change.
	d := state.CompareProcesses(a1, a2, b1, b2, threshold, 1, 10240)
	if len(d.Rows) == 0 {
		return d, "No process data in these snapshots."
	}
	return d, ""
}

// parseLocal accepts a timestamp from the browser and returns an absolute
// instant.
//
// The browser now sends an ISO 8601 string with an explicit offset (or a
// trailing Z), so the instant is unambiguous no matter how the browser's
// timezone relates to the server's. This matters: the datetime pickers
// report the user's *local wall-clock* time, and until the browser started
// attaching an offset the server reinterpreted "18:54" in its OWN timezone.
// When the two disagreed -- a laptop a few hours ahead of a UTC server --
// the requested window landed in the server's future, the archive had no
// data there, and the chart came back empty with no error to explain it.
//
// The naive layouts are kept as a fallback so an older cached page, or a
// hand-built API call, still works: those are interpreted in the server's
// timezone exactly as before.
func parseLocal(s string) (time.Time, error) {
	for _, layout := range []string{time.RFC3339Nano, time.RFC3339} {
		if t, err := time.Parse(layout, s); err == nil {
			return t, nil
		}
	}
	if t, err := time.ParseInLocation("2006-01-02T15:04:05", s, time.Local); err == nil {
		return t, nil
	}
	return time.ParseInLocation("2006-01-02T15:04", s, time.Local)
}

func checkWindow(start, end time.Time) error {
	if !end.After(start) {
		return errors.New("end time must be after start time")
	}
	if end.Sub(start) > 32*24*time.Hour {
		return errors.New("a single window cannot exceed 32 days")
	}
	return nil
}

func clientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	json.NewEncoder(w).Encode(v)
}

func writeErr(w http.ResponseWriter, code int, msg string) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(map[string]string{"error": msg})
}
