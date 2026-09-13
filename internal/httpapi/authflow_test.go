package httpapi

import (
	"io/fs"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/githubflyideas/deltascope/internal/auth"
)

// testServer returns a server with one declared account, plus the session key,
// because the interesting cases here are what a restart with a different -user
// line does to a cookie that is already out there. A restart keeps the key
// (session.key is on disk) and rebuilds the accounts from the command line, so
// the tests below rebuild the accounts against the same key.
func testServer(t *testing.T) (*Server, []byte) {
	t.Helper()
	secret, err := auth.GenerateSecret()
	if err != nil {
		t.Fatal(err)
	}
	acc := auth.NewAccounts(secret)
	acc.Add("admin", "hunter2hunter2")
	return &Server{
		Accounts: acc,
		Sessions: auth.NewSessions(secret, time.Hour),
		Limiter:  auth.NewRateLimiter(10, time.Minute),
	}, secret
}

// declare rebuilds the account store the way a restart would.
func declare(secret []byte, pairs ...string) *auth.Accounts {
	acc := auth.NewAccounts(secret)
	for i := 0; i+1 < len(pairs); i += 2 {
		acc.Add(pairs[i], pairs[i+1])
	}
	return acc
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
	s, _ := testServer(t)
	c := login(t, s, "admin", "hunter2hunter2")
	if got := s.currentUser(authedRequest(c)); got != "admin" {
		t.Fatalf("a fresh session did not authenticate: %q", got)
	}
}

func TestWrongPasswordAndUnknownUserBothFail(t *testing.T) {
	s, _ := testServer(t)
	for _, tc := range []struct{ user, pass string }{
		{"admin", "hunter2hunter3"},
		{"nobody", "hunter2hunter2"},
		{"", ""},
	} {
		rec := httptest.NewRecorder()
		s.handleLogin(rec, httptest.NewRequest("POST", "/api/login",
			strings.NewReader(`{"username":"`+tc.user+`","password":"`+tc.pass+`"}`)))
		if rec.Code == http.StatusOK {
			t.Errorf("login as %q/%q succeeded", tc.user, tc.pass)
		}
		for _, c := range rec.Result().Cookies() {
			if c.Name == sessionCookie && c.Value != "" {
				t.Errorf("a failed login as %q still set a session cookie", tc.user)
			}
		}
	}
}

// Dropping the account from the command line has to end its sessions. The token
// is signed and self-contained, so before the credential fingerprint check it
// stayed valid for its full TTL: the operator removed the admin to start over,
// and the browser they were holding open still had the whole UI.
func TestDroppingTheAccountEndsItsSessions(t *testing.T) {
	s, secret := testServer(t)
	c := login(t, s, "admin", "hunter2hunter2")

	s.Accounts = declare(secret, "someone-else", "hunter2hunter2")
	if got := s.currentUser(authedRequest(c)); got != "" {
		t.Errorf("session still authenticates as %q after the account was undeclared", got)
	}
}

// Changing the password must end sessions opened with the old one, for the same
// reason: otherwise "I changed the password" does not actually take the old
// holder out. With accounts on the command line this is the whole password
// change procedure -- edit the line, restart -- so it has to work.
func TestChangingThePasswordEndsOldSessions(t *testing.T) {
	s, secret := testServer(t)
	c := login(t, s, "admin", "hunter2hunter2")

	s.Accounts = declare(secret, "admin", "correcthorsebattery")
	if got := s.currentUser(authedRequest(c)); got != "" {
		t.Errorf("session issued against the old password still authenticates as %q", got)
	}
	if got := s.currentUser(authedRequest(login(t, s, "admin", "correcthorsebattery"))); got != "admin" {
		t.Errorf("a session issued against the new password does not authenticate: %q", got)
	}
}

// The other half of the same property: a restart that does not change the -user
// line must not log anyone out. A service is restarted for reasons that have
// nothing to do with accounts, and the fingerprint is recomputed on every
// request, so if it were not stable across a restart every deploy would empty
// every open browser.
func TestRestartWithTheSameDeclarationKeepsSessions(t *testing.T) {
	s, secret := testServer(t)
	c := login(t, s, "admin", "hunter2hunter2")

	s.Accounts = declare(secret, "admin", "hunter2hunter2")
	if got := s.currentUser(authedRequest(c)); got != "admin" {
		t.Errorf("a restart with an unchanged -user line logged the session out: %q", got)
	}
}

func TestNoCookieIsNotAuthenticated(t *testing.T) {
	s, _ := testServer(t)
	if got := s.currentUser(authedRequest(nil)); got != "" {
		t.Errorf("a request with no cookie authenticated as %q", got)
	}
	if got := s.currentUser(authedRequest(&http.Cookie{Name: sessionCookie, Value: "not.atoken"})); got != "" {
		t.Errorf("a forged cookie authenticated as %q", got)
	}
}

// There is no unauthenticated write path any more, and now there is no
// authenticated one either: accounts exist only in the process, declared by
// -user. This asserts the absence of the old setup routes, because a route left
// mounted after its UI was deleted is exactly the kind of thing that survives a
// refactor unnoticed.
func TestNoUnauthenticatedAccountCreationRoute(t *testing.T) {
	s, _ := testServer(t)
	s.WebFS = emptyFS{}
	h := s.Routes()

	for _, tc := range []struct{ method, path string }{
		{"POST", "/api/setup"},
		{"GET", "/api/setup-status"},
		{"POST", "/api/users"},
	} {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(tc.method, tc.path,
			strings.NewReader(`{"username":"intruder","password":"hunter2hunter2"}`)))
		if rec.Code != http.StatusNotFound && rec.Code != http.StatusMethodNotAllowed {
			t.Errorf("%s %s returned %d, want it gone", tc.method, tc.path, rec.Code)
		}
	}
	if s.Accounts.Len() != 1 || s.Accounts.Fingerprint("intruder") != "" {
		t.Error("an unauthenticated request created an account")
	}
}

// emptyFS stands in for the embedded web assets: Routes needs an fs.FS to build
// the static handler, and this test never fetches a page.
type emptyFS struct{}

func (emptyFS) Open(string) (fs.File, error) { return nil, fs.ErrNotExist }
