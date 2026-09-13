package auth

import (
	"strings"
	"testing"
	"time"
)

func TestSessions(t *testing.T) {
	secret, _ := GenerateSecret()
	s := NewSessions(secret, time.Hour)
	acc := NewAccounts(secret)
	acc.Add("admin", "s3cret-password")
	fp := acc.Fingerprint("admin")
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
