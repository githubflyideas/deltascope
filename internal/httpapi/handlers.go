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
	"time"

	"github.com/githubflyideas/deltascope/internal/auth"
	"github.com/githubflyideas/deltascope/internal/diagnose"
	"github.com/githubflyideas/deltascope/internal/pcp"
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
	WebFS      fs.FS
	SecureCk   bool
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
		"user":    r.Context().Value(ctxUser{}),
		"archive": s.Archive,
	})
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
		AStart: aStart, AEnd: aEnd, BStart: bStart, BEnd: bEnd, ThresholdPct: threshold,
	})
	if err != nil {
		log.Printf("diff: %v", err)
		writeErr(w, http.StatusBadGateway, "archive query failed, check server logs or confirm the selected window has data")
		return
	}
	writeJSON(w, rep)
}

func (s *Server) handleTrend(w http.ResponseWriter, r *http.Request) {
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
	})
	if err != nil {
		log.Printf("diagnose: %v", err)
		writeErr(w, http.StatusBadGateway, err.Error())
		return
	}
	writeJSON(w, d)
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
		"a_time":  before.Taken,
		"b_time":  after.Taken,
		"total":   diff.Total,
		"sections": stateDiffJSON(diff),
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


func parseLocal(s string) (time.Time, error) {
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
