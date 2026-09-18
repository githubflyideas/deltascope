package httpapi

import (
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/githubflyideas/deltascope/internal/state"
	"github.com/githubflyideas/deltascope/internal/store"
)

// healthServer returns a Server with only the fields the health endpoints
// touch. Everything else is nil: the probes must not reach into auth,
// sessions, PCP, or the web filesystem.
func healthServer(t *testing.T) (*Server, *store.Store) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "health.db")
	st, err := store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	ss, err := state.NewStore(st.DB())
	if err != nil {
		t.Fatal(err)
	}
	return &Server{StateStore: ss}, st
}

func plainBody(rec *httptest.ResponseRecorder) string {
	return strings.TrimSpace(rec.Body.String())
}

// --- healthz ---

func TestHealthzAlwaysOK(t *testing.T) {
	s, _ := healthServer(t)
	rec := httptest.NewRecorder()
	s.handleHealthz(rec, httptest.NewRequest("GET", "/healthz", nil))
	if rec.Code != http.StatusOK {
		t.Errorf("healthz = %d, want 200", rec.Code)
	}
	if got := plainBody(rec); got != "ok" {
		t.Errorf("body = %q, want ok", got)
	}
}

func TestHealthzNoCookie(t *testing.T) {
	s, _ := healthServer(t)
	s.WebFS = emptyFS{}
	h := s.Routes()
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/healthz", nil))
	if rec.Code != http.StatusOK {
		t.Errorf("healthz through Routes = %d, want 200 without auth", rec.Code)
	}
}

// --- readyz ---

func TestReadyzWithLiveStore(t *testing.T) {
	s, _ := healthServer(t)
	rec := httptest.NewRecorder()
	s.handleReadyz(rec, httptest.NewRequest("GET", "/readyz", nil))
	if rec.Code != http.StatusOK {
		t.Errorf("readyz = %d, want 200 with a live store", rec.Code)
	}
	if got := plainBody(rec); got != "ok" {
		t.Errorf("body = %q, want ok", got)
	}
}

func TestReadyzNilStoreIsReady(t *testing.T) {
	s := &Server{}
	rec := httptest.NewRecorder()
	s.handleReadyz(rec, httptest.NewRequest("GET", "/readyz", nil))
	if rec.Code != http.StatusOK {
		t.Errorf("readyz = %d, want 200 when StateStore is nil (no change accounting)", rec.Code)
	}
}

// The test that matters most: liveness stays up while readiness goes down.
// A liveness probe that fails on a dependency outage asks the supervisor to
// restart a process that would come back in exactly the same state.
func TestHealthzUpWhileReadyzDown(t *testing.T) {
	s, st := healthServer(t)
	st.Close() // break the database

	recH := httptest.NewRecorder()
	s.handleHealthz(recH, httptest.NewRequest("GET", "/healthz", nil))
	if recH.Code != http.StatusOK {
		t.Errorf("healthz = %d, want 200 even with a broken store", recH.Code)
	}

	recR := httptest.NewRecorder()
	s.handleReadyz(recR, httptest.NewRequest("GET", "/readyz", nil))
	if recR.Code != http.StatusServiceUnavailable {
		t.Errorf("readyz = %d, want 503 with a closed store", recR.Code)
	}
	if got := plainBody(recR); got != "state store unavailable" {
		t.Errorf("body = %q, want 'state store unavailable'", got)
	}
}

// --- shared properties ---

// Neither endpoint leaks the version string. /api/version is the one place
// the version lives; these must stay opaque so an attacker that can only
// reach the probe cannot fingerprint the install.
func TestHealthEndpointsDoNotLeakVersion(t *testing.T) {
	s, _ := healthServer(t)
	s.Version = "4.2.0-test"

	for _, ep := range []string{"/healthz", "/readyz"} {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest("GET", ep, nil)
		switch ep {
		case "/healthz":
			s.handleHealthz(rec, req)
		case "/readyz":
			s.handleReadyz(rec, req)
		}
		body, _ := io.ReadAll(rec.Result().Body)
		if strings.Contains(string(body), "4.2.0") {
			t.Errorf("%s leaks the version string", ep)
		}
	}
}

func TestHealthEndpointsArePlainText(t *testing.T) {
	s, _ := healthServer(t)
	for _, ep := range []struct {
		path    string
		handler func(http.ResponseWriter, *http.Request)
	}{
		{"/healthz", s.handleHealthz},
		{"/readyz", s.handleReadyz},
	} {
		rec := httptest.NewRecorder()
		ep.handler(rec, httptest.NewRequest("GET", ep.path, nil))
		ct := rec.Header().Get("Content-Type")
		if !strings.HasPrefix(ct, "text/plain") {
			t.Errorf("%s Content-Type = %q, want text/plain", ep.path, ct)
		}
		cc := rec.Header().Get("Cache-Control")
		if cc != "no-store" {
			t.Errorf("%s Cache-Control = %q, want no-store", ep.path, cc)
		}
	}
}
