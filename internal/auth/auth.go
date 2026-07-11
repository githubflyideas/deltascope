package auth

import (
	"crypto/hmac"
	"crypto/pbkdf2"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"
)


const (
	pbkdf2Iters  = 600_000
	saltLen      = 16
	keyLen       = 32
	hashVersion  = "pbkdf2-sha256"
)

func HashPassword(password string) (string, error) {
	salt := make([]byte, saltLen)
	if _, err := rand.Read(salt); err != nil {
		return "", err
	}
	dk, err := pbkdf2.Key(sha256.New, password, salt, pbkdf2Iters, keyLen)
	if err != nil {
		return "", err
	}
	return strings.Join([]string{
		hashVersion,
		strconv.Itoa(pbkdf2Iters),
		base64.RawStdEncoding.EncodeToString(salt),
		base64.RawStdEncoding.EncodeToString(dk),
	}, "$"), nil
}

func VerifyPassword(stored, password string) bool {
	parts := strings.Split(stored, "$")
	if len(parts) != 4 || parts[0] != hashVersion {
		return false
	}
	iters, err := strconv.Atoi(parts[1])
	if err != nil || iters < 10_000 || iters > 10_000_000 {
		return false
	}
	salt, err := base64.RawStdEncoding.DecodeString(parts[2])
	if err != nil {
		return false
	}
	want, err := base64.RawStdEncoding.DecodeString(parts[3])
	if err != nil {
		return false
	}
	got, err := pbkdf2.Key(sha256.New, password, salt, iters, len(want))
	if err != nil {
		return false
	}
	return subtle.ConstantTimeCompare(got, want) == 1
}


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
}

func (s *Sessions) Issue(user string) string {
	p, _ := json.Marshal(sessionPayload{User: user, Exp: time.Now().Add(s.TTL).Unix()})
	body := base64.RawURLEncoding.EncodeToString(p)
	return body + "." + s.sign(body)
}

func (s *Sessions) Verify(token string) (string, bool) {
	body, sig, ok := strings.Cut(token, ".")
	if !ok {
		return "", false
	}
	if subtle.ConstantTimeCompare([]byte(s.sign(body)), []byte(sig)) != 1 {
		return "", false
	}
	raw, err := base64.RawURLEncoding.DecodeString(body)
	if err != nil {
		return "", false
	}
	var p sessionPayload
	if err := json.Unmarshal(raw, &p); err != nil {
		return "", false
	}
	if time.Now().Unix() >= p.Exp || p.User == "" {
		return "", false
	}
	return p.User, true
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
		return nil, fmt.Errorf("生成会话密钥失败: %w", err)
	}
	return b, nil
}
