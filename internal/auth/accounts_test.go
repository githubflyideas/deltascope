package auth

import "testing"

func TestAccountsVerify(t *testing.T) {
	secret, _ := GenerateSecret()
	a := NewAccounts(secret)
	a.Add("admin", "correct horse battery")
	a.Add("ops", "another-password")

	if a.Len() != 2 {
		t.Fatalf("Len = %d, want 2", a.Len())
	}
	fp, ok := a.Verify("admin", "correct horse battery")
	if !ok {
		t.Fatal("the declared password failed verification")
	}
	if fp != a.Fingerprint("admin") {
		t.Error("Verify and Fingerprint disagree about the same account")
	}
	if fp == a.Fingerprint("ops") {
		t.Error("two accounts share a fingerprint")
	}
	if _, ok := a.Verify("admin", "correct horse batter"); ok {
		t.Error("a wrong password verified")
	}
	if _, ok := a.Verify("nobody", "correct horse battery"); ok {
		t.Error("an undeclared account verified")
	}
	if _, ok := a.Verify("", ""); ok {
		t.Error("an empty pair verified")
	}
}

// A restart with an unchanged command line must not log anyone out, and a
// changed password must. Both properties live in the fingerprint, so they are
// checked here rather than left to the HTTP layer.
func TestFingerprintSurvivesRestartAndTracksThePassword(t *testing.T) {
	secret, _ := GenerateSecret()

	first := NewAccounts(secret)
	first.Add("admin", "correct horse battery")
	second := NewAccounts(secret)
	second.Add("admin", "correct horse battery")
	if first.Fingerprint("admin") != second.Fingerprint("admin") {
		t.Error("the same declaration fingerprinted differently after a restart; every restart would log everyone out")
	}

	changed := NewAccounts(secret)
	changed.Add("admin", "a different password")
	if changed.Fingerprint("admin") == first.Fingerprint("admin") {
		t.Error("changing the password left the fingerprint alone; the old sessions would stay live")
	}

	if first.Fingerprint("gone") != "" {
		t.Error("an undeclared account has a fingerprint")
	}

	// Losing session.key is already a full logout because the token signature
	// stops verifying; the fingerprint must not be the one thing that survives it.
	other, _ := GenerateSecret()
	rekeyed := NewAccounts(other)
	rekeyed.Add("admin", "correct horse battery")
	if rekeyed.Fingerprint("admin") == first.Fingerprint("admin") {
		t.Error("the fingerprint does not depend on the session key")
	}
}

// The tag binds name and password with a length prefix. Without it the pairs
// below would collide, and `-user ab:c` would also accept a login as "a" with
// the password "bc".
func TestAccountNameBoundaryIsUnambiguous(t *testing.T) {
	secret, _ := GenerateSecret()
	a := NewAccounts(secret)
	a.Add("ab", "c")
	if _, ok := a.Verify("a", "bc"); ok {
		t.Error("a shifted name/password split verified against a different account")
	}
}

// -user is repeatable and an operator editing a unit file can leave the same
// name in twice. One entry has to win, and it has to be the readable one.
func TestLastDeclarationOfANameWins(t *testing.T) {
	secret, _ := GenerateSecret()
	a := NewAccounts(secret)
	a.Add("admin", "the-old-password")
	a.Add("admin", "the-new-password")

	if a.Len() != 1 {
		t.Fatalf("Len = %d, want 1: a repeated name should not leave two entries", a.Len())
	}
	if _, ok := a.Verify("admin", "the-new-password"); !ok {
		t.Error("the last declaration does not verify")
	}
	if _, ok := a.Verify("admin", "the-old-password"); ok {
		t.Error("the superseded password still verifies")
	}
	if names := a.Names(); len(names) != 1 || names[0] != "admin" {
		t.Errorf("Names = %v, want [admin]", names)
	}
}
