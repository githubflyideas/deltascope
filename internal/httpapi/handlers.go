package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"log"
	"net"
	"net/http"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/githubflyideas/deltascope/internal/auth"
	"github.com/githubflyideas/deltascope/internal/diagnose"
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
}

func (s *Server) Routes() http.Handler {
	mux := http.NewServeMux()

	static, _ := fs.Sub(s.WebFS, "web/static")
	mux.Handle("GET /static/", http.StripPrefix("/static/", http.FileServerFS(static)))

	mux.HandleFunc("GET /login", s.servePage("web/login.html", false))
	mux.HandleFunc("POST /api/login", s.handleLogin)
	mux.HandleFunc("GET /api/setup-status", s.handleSetupStatus)
	mux.HandleFunc("POST /api/setup", s.handleSetup)
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

func (s *Server) currentUser(r *http.Request) string {
	c, err := r.Cookie(sessionCookie)
	if err != nil {
		return ""
	}
	u, ok := s.Sessions.Verify(c.Value)
	if !ok {
		return ""
	}
	return u
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

// handleSetupStatus reports whether the server has zero users, in which
// case the login page offers to create the first admin account directly
// instead of requiring a separate CLI step. This exists because
// "user add" and "serve" resolving -data to two different directories --
// a relative path from a different shell, systemd's WorkingDirectory,
// forgetting the flag -- used to fail silently: the account "existed"
// somewhere the running server could not see, with nothing in the UI
// explaining why login kept failing.
func (s *Server) handleSetupStatus(w http.ResponseWriter, r *http.Request) {
	users, err := s.Store.ListUsers()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "internal error")
		return
	}
	writeJSON(w, map[string]any{"needs_setup": len(users) == 0})
}

// handleSetup creates the first admin account from the browser. It only
// ever succeeds once: the moment any user exists, this endpoint refuses
// unconditionally, so it cannot be used to add a second account or take
// over an already-configured server. This is intentionally
// unauthenticated -- there is nothing to authenticate against before the
// first account exists -- and mirrors the window every self-hosted admin
// tool has between "installed" and "configured".
func (s *Server) handleSetup(w http.ResponseWriter, r *http.Request) {
	users, err := s.Store.ListUsers()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "internal error")
		return
	}
	if len(users) > 0 {
		writeErr(w, http.StatusConflict, "an account already exists; setup is only available before the first account is created")
		return
	}
	var req struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid request")
		return
	}
	req.Username = strings.TrimSpace(req.Username)
	if req.Username == "" || len(req.Username) > 64 {
		writeErr(w, http.StatusBadRequest, "username must be non-empty and at most 64 characters")
		return
	}
	if len(req.Password) < 8 {
		writeErr(w, http.StatusBadRequest, "password must be at least 8 characters")
		return
	}
	hash, err := auth.HashPassword(req.Password)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "internal error")
		return
	}
	// Re-check immediately before writing to close the race between two
	// browsers hitting an unconfigured server at once; whichever request
	// wins the UpsertUser below is authoritative, and this is a single
	// local admin tool, not a multi-tenant service, so the residual race
	// window is acceptable.
	users, err = s.Store.ListUsers()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "internal error")
		return
	}
	if len(users) > 0 {
		writeErr(w, http.StatusConflict, "an account already exists; setup is only available before the first account is created")
		return
	}
	if err := s.Store.UpsertUser(req.Username, hash); err != nil {
		writeErr(w, http.StatusInternalServerError, "failed to create the account")
		return
	}
	log.Printf("setup: created initial admin account %q via web UI", req.Username)
	writeJSON(w, map[string]any{"ok": true})
}

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
		Name: sessionCookie, Value: s.Sessions.Issue(req.Username),
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
	})
	if err != nil {
		log.Printf("diagnose: %v", err)
		writeErr(w, http.StatusBadGateway, err.Error())
		return
	}
	writeJSON(w, d)
}

// handleReasoning runs the experimental two-layer chain: metric rows are
// first reduced to named states (state.cpu.steal_high, ...), then
// diagnoses are matched against combinations of those states including
// negation. This runs alongside the original rule engine rather than
// replacing it, so both can be compared against the same real data
// before deciding whether to migrate.
func (s *Server) handleReasoning(w http.ResponseWriter, r *http.Request) {
	if !s.requireMetrics(w) {
		return
	}
	threshold := 15.0
	if t := r.URL.Query().Get("threshold"); t != "" {
		if v, err := strconv.ParseFloat(t, 64); err == nil && v >= 0 && v <= 10000 {
			threshold = v
		}
	}
	ctx, cancel := context.WithTimeout(r.Context(), execTimeout)
	defer cancel()

	// The reasoning chain answers "what is wrong on this machine right
	// now", so it defaults to a short recent window -- the last 30 minutes
	// against the 30 minutes before that -- rather than the day-over-day
	// window the Diagnose tab uses. A day-old baseline keeps a state active
	// long after the process that caused it has exited, because yesterday
	// at this time really was different; a "recent vs just-before" window
	// tracks the machine as it is now and clears once the burst is over.
	// Explicit a_start/a_end/b_start/b_end override the default so the UI's
	// time picker can ask for any window.
	q := r.URL.Query()
	var win pcp.Windows
	if q.Get("b_end") != "" {
		aS, e1 := parseLocal(q.Get("a_start"))
		aE, e2 := parseLocal(q.Get("a_end"))
		bS, e3 := parseLocal(q.Get("b_start"))
		bE, e4 := parseLocal(q.Get("b_end"))
		if e1 != nil || e2 != nil || e3 != nil || e4 != nil {
			writeErr(w, http.StatusBadRequest, "invalid time parameter, expected 2026-07-03T14:00")
			return
		}
		if err := checkWindow(aS, aE); err != nil {
			writeErr(w, http.StatusBadRequest, err.Error())
			return
		}
		if err := checkWindow(bS, bE); err != nil {
			writeErr(w, http.StatusBadRequest, err.Error())
			return
		}
		win = pcp.Windows{AStart: aS, AEnd: aE, BStart: bS, BEnd: bE, ThresholdPct: threshold}
	} else {
		now := time.Now()
		win = pcp.Windows{
			AStart: now.Add(-60 * time.Minute), AEnd: now.Add(-30 * time.Minute),
			BStart: now.Add(-30 * time.Minute), BEnd: now,
			ThresholdPct: threshold,
		}
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

	active := reasoning.Evaluate(reasoning.States, rep.Rows)
	results := reasoning.Diagnose(reasoning.Diagnoses, active)

	// Report every state that was evaluated, not just the active ones, so
	// the UI can show what was checked and found NOT to hold -- the
	// negative evidence is what makes a diagnosis auditable.
	type stateView struct {
		ID       string   `json:"id"`
		Domain   string   `json:"domain"`
		Active   bool     `json:"active"`
		Evidence []string `json:"evidence,omitempty"`
	}
	states := make([]stateView, 0, len(reasoning.States))
	for _, st := range reasoning.States {
		v := stateView{ID: st.ID, Domain: st.Domain}
		if a, on := active[st.ID]; on {
			v.Active, v.Evidence = true, a.Evidence
		}
		states = append(states, v)
	}

	writeJSON(w, map[string]any{
		"window":    w2,
		"machine":   reasoning.Host(),
		"states":    states,
		"diagnoses": results,
	})
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
