package auth

import (
	"fmt"
	"strings"
	"testing"
	"time"
)

func TestHashVerify(t *testing.T) {
	h, err := HashPassword("s3cret-password")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(h, "pbkdf2-sha256$600000$") {
		t.Fatalf("unexpected hash format: %s", h)
	}
	if !VerifyPassword(h, "s3cret-password") {
		t.Error("correct password failed verification")
	}
	if VerifyPassword(h, "wrong") {
		t.Error("wrong password incorrectly verified")
	}
	h2, _ := HashPassword("s3cret-password")
	if h == h2 {
		t.Error("two hashes are identical, salt not applied")
	}
	if VerifyPassword("garbage", "x") || VerifyPassword("a$b$c$d", "x") {
		t.Error("malformed hash should fail verification")
	}
}

func TestSessions(t *testing.T) {
	secret, _ := GenerateSecret()
	s := NewSessions(secret, time.Hour)
	fp := Fingerprint("pbkdf2-sha256$600000$c2FsdA$a2V5")
	tok := s.Issue("admin", fp)
	cl, ok := s.Verify(tok)
	if !ok || cl.User != "admin" {
		t.Fatalf("session verification failed: %v %v", cl, ok)
	}
	if cl.FP != fp {
		t.Errorf("fingerprint did not survive the round trip: %q want %q", cl.FP, fp)
	}
	if _, ok := s.Verify(tok + "x"); ok {
		t.Error("tampered signature should fail")
	}
	body, sig, _ := strings.Cut(tok, ".")
	if _, ok := s.Verify(body + "A." + sig); ok {
		t.Error("tampered payload should fail")
	}
	sExp := NewSessions(secret, -time.Minute)
	if _, ok := sExp.Verify(sExp.Issue("admin", fp)); ok {
		t.Error("expired session should fail")
	}
	secret2, _ := GenerateSecret()
	if _, ok := NewSessions(secret2, time.Hour).Verify(tok); ok {
		t.Error("verification with a different key should fail")
	}
	// A token from a build that predates fingerprints has no FP. Accepting it
	// would keep the pre-fix sessions alive past the upgrade that was supposed
	// to make them revocable.
	if _, ok := s.Verify(s.Issue("admin", "")); ok {
		t.Error("a session with no credential fingerprint should fail")
	}
}

// Changing the password or deleting the account must change what the
// fingerprint of the stored credential is, or the session check in the HTTP
// layer has nothing to notice.
func TestFingerprintTracksTheCredential(t *testing.T) {
	h1, _ := HashPassword("correct horse battery")
	h2, _ := HashPassword("correct horse battery")
	if Fingerprint(h1) == Fingerprint(h2) {
		t.Error("re-hashing the same password produced the same fingerprint; the salt should make it differ")
	}
	if Fingerprint(h1) != Fingerprint(h1) {
		t.Error("Fingerprint is not deterministic")
	}
	if Fingerprint("") == Fingerprint(h1) {
		t.Error("a missing credential fingerprints the same as a real one")
	}
}

func TestRateLimiter(t *testing.T) {
	rl := NewRateLimiter(3, time.Hour)
	ip := "10.0.0.1"
	for i := 0; i < 3; i++ {
		if ok, _ := rl.Allow(ip); !ok {
			t.Fatalf("attempt %d should not be rejected", i+1)
		}
		rl.Fail(ip)
	}
	if ok, wait := rl.Allow(ip); ok || wait <= 0 {
		t.Error("should be rejected after the limit, with remaining time returned")
	}
	if ok, _ := rl.Allow("10.0.0.2"); !ok {
		t.Error("a different IP should be unaffected")
	}
	rl.Reset(ip)
	if ok, _ := rl.Allow(ip); !ok {
		t.Error("should recover after Reset")
	}
}

func TestPBKDF2Vectors(t *testing.T) {
	cases := []struct {
		iter int
		want string
	}{
		{1, "120fb6cffcf8b32c43e7225256c4f837a86548c92ccc35480805987cb70be17b"},
		{4096, "c5e478d59288c841aa530db6845c4c8d962893a001ce4e11a4963873aa98134a"},
	}
	for _, c := range cases {
		got := fmt.Sprintf("%x", pbkdf2SHA256([]byte("password"), []byte("salt"), c.iter, 32))
		if got != c.want {
			t.Errorf("iter=%d got %s want %s", c.iter, got, c.want)
		}
	}
}
