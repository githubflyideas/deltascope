package httpapi

import (
	"errors"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/githubflyideas/deltascope/internal/auth"
	"github.com/githubflyideas/deltascope/internal/store"
)

func testServer(t *testing.T) *Server {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	secret, err := auth.GenerateSecret()
	if err != nil {
		t.Fatal(err)
	}
	return &Server{
		Store:    st,
		Sessions: auth.NewSessions(secret, time.Hour),
		Limiter:  auth.NewRateLimiter(10, time.Minute),
	}
}

// login drives the real handler so the test exercises whatever the browser
// would actually receive, cookie included.
func login(t *testing.T, s *Server, user, pass string) *http.Cookie {
	t.Helper()
	req := httptest.NewRequest("POST", "/api/login",
		strings.NewReader(`{"username":"`+user+`","password":"`+pass+`"}`))
	rec := httptest.NewRecorder()
	s.handleLogin(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("login failed: %d %s", rec.Code, rec.Body)
	}
	for _, c := range rec.Result().Cookies() {
		if c.Name == sessionCookie {
			return c
		}
	}
	t.Fatal("login returned no session cookie")
	return nil
}

func authedRequest(c *http.Cookie) *http.Request {
	r := httptest.NewRequest("GET", "/api/me", nil)
	if c != nil {
		r.AddCookie(c)
	}
	return r
}

func TestSessionSurvivesNothingElseChanging(t *testing.T) {
	s := testServer(t)
	hash, _ := auth.HashPassword("hunter2hunter2")
	if err := s.Store.UpsertUser("admin", hash); err != nil {
		t.Fatal(err)
	}
	c := login(t, s, "admin", "hunter2hunter2")
	if got := s.currentUser(authedRequest(c)); got != "admin" {
		t.Fatalf("a fresh session did not authenticate: %q", got)
	}
}

// Deleting the account has to end its sessions. The token is signed and
// self-contained, so before the credential fingerprint check it stayed valid
// for its full TTL: the operator deleted the admin to start over, and the
// browser they were holding open still had the whole UI.
func TestDeletingTheAccountEndsItsSessions(t *testing.T) {
	s := testServer(t)
	hash, _ := auth.HashPassword("hunter2hunter2")
	s.Store.UpsertUser("admin", hash)
	c := login(t, s, "admin", "hunter2hunter2")

	if err := s.Store.DeleteUser("admin"); err != nil {
		t.Fatal(err)
	}
	if got := s.currentUser(authedRequest(c)); got != "" {
		t.Errorf("session still authenticates as %q after the account was deleted", got)
	}
}

// Changing the password must end sessions opened with the old one, for the
// same reason: otherwise "I changed the password" does not actually take the
// old holder out.
func TestChangingThePasswordEndsOldSessions(t *testing.T) {
	s := testServer(t)
	old, _ := auth.HashPassword("hunter2hunter2")
	s.Store.UpsertUser("admin", old)
	c := login(t, s, "admin", "hunter2hunter2")

	next, _ := auth.HashPassword("correcthorsebattery")
	if err := s.Store.UpsertUser("admin", next); err != nil {
		t.Fatal(err)
	}
	if got := s.currentUser(authedRequest(c)); got != "" {
		t.Errorf("session issued against the old password still authenticates as %q", got)
	}
	if got := s.currentUser(authedRequest(login(t, s, "admin", "correcthorsebattery"))); got != "admin" {
		t.Errorf("a session issued against the new password does not authenticate: %q", got)
	}
}

func TestNoCookieIsNotAuthenticated(t *testing.T) {
	s := testServer(t)
	s.Store.UpsertUser("admin", mustHash(t, "hunter2hunter2"))
	if got := s.currentUser(authedRequest(nil)); got != "" {
		t.Errorf("a request with no cookie authenticated as %q", got)
	}
	if got := s.currentUser(authedRequest(&http.Cookie{Name: sessionCookie, Value: "not.atoken"})); got != "" {
		t.Errorf("a forged cookie authenticated as %q", got)
	}
}

// There is no unauthenticated write path any more. The web setup endpoint was
// the one route that could create an account without a session; accounts now
// come from `serve -user`, so nothing reachable from the network can add one.
// This asserts the absence, because a route left mounted after its UI was
// deleted is exactly the kind of thing that survives a refactor unnoticed.
func TestNoUnauthenticatedAccountCreationRoute(t *testing.T) {
	s := testServer(t)
	s.Store.UpsertUser("admin", mustHash(t, "hunter2hunter2"))
	s.WebFS = emptyFS{}
	h := s.Routes()

	for _, tc := range []struct{ method, path string }{
		{"POST", "/api/setup"},
		{"GET", "/api/setup-status"},
	} {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(tc.method, tc.path,
			strings.NewReader(`{"username":"intruder","password":"hunter2hunter2"}`)))
		if rec.Code != http.StatusNotFound && rec.Code != http.StatusMethodNotAllowed {
			t.Errorf("%s %s returned %d, want it gone", tc.method, tc.path, rec.Code)
		}
	}
	if _, err := s.Store.PasswordHash("intruder"); !errors.Is(err, store.ErrNotFound) {
		t.Error("an unauthenticated request created an account")
	}
}

// emptyFS stands in for the embedded web assets: Routes needs an fs.FS to build
// the static handler, and this test never fetches a page.
type emptyFS struct{}

func (emptyFS) Open(string) (fs.File, error) { return nil, fs.ErrNotExist }

// A password written the way `serve -user` writes it has to verify against a
// freshly opened store, not just the one that wrote it: the report behind this
// was "the password only works for the run that created it".
func TestCredentialSurvivesReopeningTheDatabase(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "deltascope.db")

	first, err := store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := first.UpsertUser("admin", mustHash(t, "hunter2hunter2")); err != nil {
		t.Fatal(err)
	}
	// Deliberately no Close(): the service is killed by systemd, and
	// log.Fatal in the serve path skips the deferred close too, so recovery
	// from the WAL is the normal case rather than the exception.

	second, err := store.Open(path)
	if err != nil {
		t.Fatalf("reopening the database failed: %v", err)
	}
	defer second.Close()
	hash, err := second.PasswordHash("admin")
	if err != nil {
		t.Fatalf("the account is not in the reopened database: %v", err)
	}
	if !auth.VerifyPassword(hash, "hunter2hunter2") {
		t.Error("the password does not verify after the database was reopened")
	}
	first.Close()
}

func mustHash(t *testing.T, pw string) string {
	t.Helper()
	h, err := auth.HashPassword(pw)
	if err != nil {
		t.Fatal(err)
	}
	return h
}
