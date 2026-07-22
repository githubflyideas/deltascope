package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"log"
	"net"
	"net/http"
	"sort"
	"strconv"
	"time"

	"github.com/githubflyideas/deltascope/internal/auth"
	"github.com/githubflyideas/deltascope/internal/pcp"
	"github.com/githubflyideas/deltascope/internal/store"
)

const (
	sessionCookie = "ds_session"
	execTimeout   = 60 * time.Second
)

type Server struct {
	Store    *store.Store
	Sessions *auth.Sessions
	Limiter  *auth.RateLimiter
	Runner   pcp.Runner
	Archive  string
	WebFS    fs.FS
	SecureCk bool
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
			writeErr(w, http.StatusUnauthorized, "未登录或会话已过期")
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
			"失败次数过多,请 "+strconv.Itoa(int(wait.Minutes())+1)+" 分钟后再试")
		return
	}

	var req struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(&req); err != nil ||
		req.Username == "" || req.Password == "" {
		writeErr(w, http.StatusBadRequest, "请输入用户名和密码")
		return
	}

	hash, err := s.Store.PasswordHash(req.Username)
	if errors.Is(err, store.ErrNotFound) {
		auth.VerifyPassword("pbkdf2-sha256$600000$AAAAAAAAAAAAAAAAAAAAAA$AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA", req.Password)
		s.Limiter.Fail(ip)
		writeErr(w, http.StatusUnauthorized, "用户名或密码错误")
		return
	}
	if err != nil {
		log.Printf("login: 读取用户失败: %v", err)
		writeErr(w, http.StatusInternalServerError, "内部错误")
		return
	}
	if !auth.VerifyPassword(hash, req.Password) {
		s.Limiter.Fail(ip)
		writeErr(w, http.StatusUnauthorized, "用户名或密码错误")
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
		writeErr(w, http.StatusBadRequest, "时间参数格式错误, 期望 2026-07-03T14:00")
		return
	}
	threshold := 15.0
	if t := q.Get("threshold"); t != "" {
		v, err := strconv.ParseFloat(t, 64)
		if err != nil || v < 0 || v > 10000 {
			writeErr(w, http.StatusBadRequest, "threshold 必须是 0~10000 的数字")
			return
		}
		threshold = v
	}
	if err := checkWindow(aStart, aEnd); err != nil {
		writeErr(w, http.StatusBadRequest, "时间段 A: "+err.Error())
		return
	}
	if err := checkWindow(bStart, bEnd); err != nil {
		writeErr(w, http.StatusBadRequest, "时间段 B: "+err.Error())
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), execTimeout)
	defer cancel()
	rep, err := pcp.Compare(ctx, s.Runner, s.Archive, pcp.Windows{
		AStart: aStart, AEnd: aEnd, BStart: bStart, BEnd: bEnd, ThresholdPct: threshold,
	})
	if err != nil {
		log.Printf("diff: %v", err)
		writeErr(w, http.StatusBadGateway, "归档数据查询失败, 请检查服务端日志或确认所选窗口内存在数据")
		return
	}
	writeJSON(w, rep)
}

func (s *Server) handleTrend(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	start, err1 := parseLocal(q.Get("start"))
	end, err2 := parseLocal(q.Get("end"))
	if err1 != nil || err2 != nil {
		writeErr(w, http.StatusBadRequest, "时间参数格式错误, 期望 2026-07-03T14:00")
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
		writeErr(w, http.StatusBadGateway, "归档数据查询失败, 请检查服务端日志或确认所选窗口内存在数据")
		return
	}
	sort.Slice(series, func(i, j int) bool { return series[i].Name < series[j].Name })
	writeJSON(w, map[string]any{"series": series, "missing": missing})
}

func (s *Server) handleProcDiff(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	aStart, err1 := parseLocal(q.Get("a_start"))
	aEnd, err2 := parseLocal(q.Get("a_end"))
	bStart, err3 := parseLocal(q.Get("b_start"))
	bEnd, err4 := parseLocal(q.Get("b_end"))
	if err1 != nil || err2 != nil || err3 != nil || err4 != nil {
		writeErr(w, http.StatusBadRequest, "时间参数格式错误, 期望 2026-07-03T14:00")
		return
	}
	threshold := 20.0
	if t := q.Get("threshold"); t != "" {
		if v, err := strconv.ParseFloat(t, 64); err == nil && v >= 0 && v <= 10000 {
			threshold = v
		}
	}
	if err := checkWindow(aStart, aEnd); err != nil {
		writeErr(w, http.StatusBadRequest, "时间段 A: "+err.Error())
		return
	}
	if err := checkWindow(bStart, bEnd); err != nil {
		writeErr(w, http.StatusBadRequest, "时间段 B: "+err.Error())
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), execTimeout)
	defer cancel()
	rep, err := pcp.CompareProc(ctx, s.Runner, s.Archive, pcp.Windows{
		AStart: aStart, AEnd: aEnd, BStart: bStart, BEnd: bEnd, ThresholdPct: threshold,
	})
	if err != nil {
		log.Printf("procdiff: %v", err)
		writeErr(w, http.StatusBadGateway, err.Error()+" (需在 pmlogger 启用 hotproc 采集)")
		return
	}
	writeJSON(w, procReportJSON(rep))
}

func procReportJSON(rep *pcp.ProcReport) map[string]any {
	conv := func(rows []pcp.ProcRow) []map[string]any {
		out := make([]map[string]any, 0, len(rows))
		for _, r := range rows {
			m := map[string]any{
				"name": r.Name, "verdict": string(r.Verdict),
				"a": r.A, "b": r.B, "delta_pct": r.DeltaPct,
				"restarted": r.Restarted, "unit": r.Unit,
			}
			if r.Restarted {
				m["restart_text"] = pcp.FormatStartDelta(r.StartA, r.StartB)
			}
			out = append(out, m)
		}
		return out
	}
	return map[string]any{
		"cpu":      conv(rep.CPURows),
		"mem":      conv(rep.MemRows),
		"restarts": conv(rep.Restarts),
		"warnings": rep.Warnings,
	}
}

func parseLocal(s string) (time.Time, error) {
	if t, err := time.ParseInLocation("2006-01-02T15:04:05", s, time.Local); err == nil {
		return t, nil
	}
	return time.ParseInLocation("2006-01-02T15:04", s, time.Local)
}

func checkWindow(start, end time.Time) error {
	if !end.After(start) {
		return errors.New("结束时间必须晚于开始时间")
	}
	if end.Sub(start) > 32*24*time.Hour {
		return errors.New("单个窗口最长 32 天")
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
