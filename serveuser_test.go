package main

import (
	"strings"
	"testing"

	"github.com/githubflyideas/deltascope/internal/auth"
)

// -user is the only way an account gets created now, so its parser is on the
// path between "the operator typed a password" and "anyone can log in". These
// tests cover the two things that path can get wrong: accepting something that
// is not a credential, and mangling a credential that is.

func TestUserFlagParsesNameAndPassword(t *testing.T) {
	var u userList
	if err := u.Set("admin:hunter2hunter2"); err != nil {
		t.Fatal(err)
	}
	if err := u.check(); err != nil {
		t.Fatal(err)
	}
	if len(u) != 1 || u[0].name != "admin" || u[0].pass != "hunter2hunter2" {
		t.Fatalf("parsed %+v", u)
	}
}

// Cut at the first colon, not the last and not every colon: a generated
// password contains colons often enough that splitting anywhere else would
// silently store a truncated one, and the failure would look like "the password
// I pasted does not work".
func TestUserFlagKeepsColonsInThePassword(t *testing.T) {
	var u userList
	u.Set("admin:a:b:c:defg")
	if err := u.check(); err != nil {
		t.Fatal(err)
	}
	if u[0].name != "admin" || u[0].pass != "a:b:c:defg" {
		t.Fatalf("parsed %+v", u)
	}
}

func TestUserFlagIsRepeatable(t *testing.T) {
	var u userList
	for _, v := range []string{"admin:hunter2hunter2", "oncall:correcthorse"} {
		u.Set(v)
	}
	if err := u.check(); err != nil {
		t.Fatal(err)
	}
	if len(u) != 2 || u[0].name != "admin" || u[1].name != "oncall" {
		t.Fatalf("parsed %+v", u)
	}
}

// The bar the deleted web form enforced, kept: moving where accounts are
// declared must not quietly lower what counts as a password.
func TestUserFlagRejectsBadPairs(t *testing.T) {
	for _, bad := range []string{
		"admin",              // no colon at all
		":hunter2hunter2",    // no username
		"   :hunter2hunter2", // whitespace is not a username
		"admin:short",        // under eight characters
		"admin:",             // empty password
		strings.Repeat("a", 65) + ":hunter2hunter2", // username over 64
	} {
		var u userList
		if err := u.Set(bad); err != nil {
			t.Fatalf("Set must not fail (flag would echo the value): %v", err)
		}
		if err := u.check(); err == nil {
			t.Errorf("%q was accepted", bad)
		}
	}
}

// The reason validation lives in check rather than Set: flag prints `invalid
// value %q for flag -user: <err>` when Set fails, so a rejection there would
// write the password to the terminal and into the journal on every typo. Both
// halves are asserted -- Set stays silent, and check's own message keeps the
// password out.
func TestUserFlagRejectionDoesNotEchoThePassword(t *testing.T) {
	const pw = "s3cret"
	var u userList
	if err := u.Set("admin:" + pw); err != nil {
		t.Fatalf("Set returned an error, so flag would print the password: %v", err)
	}
	err := u.check()
	if err == nil {
		t.Fatal("expected a complaint about the short password")
	}
	if strings.Contains(err.Error(), pw) {
		t.Errorf("the error quotes the password: %v", err)
	}

	// And the no-colon case cannot name the value at all: what was typed there
	// may well be a password with the colon left off.
	var v userList
	v.Set(pw + "-but-long-enough")
	if err := v.check(); err == nil {
		t.Fatal("expected a complaint about the missing colon")
	} else if strings.Contains(err.Error(), pw) {
		t.Errorf("the error quotes what may be a password: %v", err)
	}
}

// The account the flag declares has to accept the password as typed. Trivial to
// assert and the thing that broke before: trailing whitespace trimmed off a
// password, or the name trimmed off the wrong side of the colon. This goes
// through declareAccounts because that is now the entire path from the command
// line to a login.
func TestUserFlagPasswordVerifies(t *testing.T) {
	var u userList
	if err := u.Set("admin: leading and trailing "); err != nil {
		t.Fatal(err)
	}
	if err := u.check(); err != nil {
		t.Fatal(err)
	}
	secret, err := auth.GenerateSecret()
	if err != nil {
		t.Fatal(err)
	}
	acc := declareAccounts(u, secret)
	if _, ok := acc.Verify("admin", " leading and trailing "); !ok {
		t.Error("the password was altered on the way in")
	}
	if _, ok := acc.Verify("admin", "leading and trailing"); ok {
		t.Error("a trimmed password verified, so the flag is trimming somewhere")
	}
}
