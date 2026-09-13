package auth

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"time"
)

type Sessions struct {
	secret []byte
	TTL    time.Duration
}

func NewSessions(secret []byte, ttl time.Duration) *Sessions {
	return &Sessions{secret: secret, TTL: ttl}
}

type sessionPayload struct {
	User string `json:"u"`
	Exp  int64  `json:"e"`
	FP   string `json:"f"`
}

// Claims is what a session token asserts. FP is the credential fingerprint
// the token was issued against; the caller checks it against the account as
// it stands now, which is what makes the token revocable. See Accounts.
type Claims struct {
	User string
	FP   string
}

func (s *Sessions) Issue(user, fingerprint string) string {
	p, _ := json.Marshal(sessionPayload{
		User: user,
		Exp:  time.Now().Add(s.TTL).Unix(),
		FP:   fingerprint,
	})
	body := base64.RawURLEncoding.EncodeToString(p)
	return body + "." + s.sign(body)
}

func (s *Sessions) Verify(token string) (Claims, bool) {
	body, sig, ok := strings.Cut(token, ".")
	if !ok {
		return Claims{}, false
	}
	if subtle.ConstantTimeCompare([]byte(s.sign(body)), []byte(sig)) != 1 {
		return Claims{}, false
	}
	raw, err := base64.RawURLEncoding.DecodeString(body)
	if err != nil {
		return Claims{}, false
	}
	var p sessionPayload
	if err := json.Unmarshal(raw, &p); err != nil {
		return Claims{}, false
	}
	if time.Now().Unix() >= p.Exp || p.User == "" {
		return Claims{}, false
	}
	// A token minted before fingerprints existed carries no FP, and treating
	// an empty one as "matches anything" would reopen exactly the hole this
	// closes. Reject it: the cost is one re-login on upgrade.
	if p.FP == "" {
		return Claims{}, false
	}
	return Claims{User: p.User, FP: p.FP}, true
}

func (s *Sessions) sign(body string) string {
	m := hmac.New(sha256.New, s.secret)
	m.Write([]byte(body))
	return base64.RawURLEncoding.EncodeToString(m.Sum(nil))
}

type RateLimiter struct {
	mu       sync.Mutex
	window   time.Duration
	maxFails int
	entries  map[string]*rlEntry
}

type rlEntry struct {
	fails int
	since time.Time
}

func NewRateLimiter(maxFails int, window time.Duration) *RateLimiter {
	return &RateLimiter{window: window, maxFails: maxFails, entries: map[string]*rlEntry{}}
}

func (rl *RateLimiter) Allow(ip string) (bool, time.Duration) {
	rl.mu.Lock()
	defer rl.mu.Unlock()
	e := rl.entries[ip]
	if e == nil {
		return true, 0
	}
	if time.Since(e.since) > rl.window {
		delete(rl.entries, ip)
		return true, 0
	}
	if e.fails >= rl.maxFails {
		return false, rl.window - time.Since(e.since)
	}
	return true, 0
}

func (rl *RateLimiter) Fail(ip string) {
	rl.mu.Lock()
	defer rl.mu.Unlock()
	e := rl.entries[ip]
	if e == nil || time.Since(e.since) > rl.window {
		rl.entries[ip] = &rlEntry{fails: 1, since: time.Now()}
		return
	}
	e.fails++
}

func (rl *RateLimiter) Reset(ip string) {
	rl.mu.Lock()
	defer rl.mu.Unlock()
	delete(rl.entries, ip)
}

func GenerateSecret() ([]byte, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return nil, fmt.Errorf("failed to generate session key: %w", err)
	}
	return b, nil
}
