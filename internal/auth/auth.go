// Package auth 实现本地认证:PBKDF2-SHA256 加盐口令哈希、
// HMAC 签名的无状态会话 Cookie、按 IP 的登录失败限速。
// 全部基于 Go 标准库(crypto/pbkdf2 自 Go 1.24 起进入标准库)。
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

// ---- 口令哈希 ----

const (
	pbkdf2Iters  = 600_000 // OWASP 2023 对 PBKDF2-HMAC-SHA256 的建议值
	saltLen      = 16
	keyLen       = 32
	hashVersion  = "pbkdf2-sha256"
)

// HashPassword 生成形如 pbkdf2-sha256$600000$<b64salt>$<b64dk> 的哈希串。
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

// VerifyPassword 恒定时间比较。
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

// ---- 会话 ----

// Sessions 签发/校验 HMAC-SHA256 签名的会话令牌(无状态,不落库)。
// 令牌 = b64(payload JSON) + "." + b64(HMAC(payload))。
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

// Issue 为用户签发会话令牌。
func (s *Sessions) Issue(user string) string {
	p, _ := json.Marshal(sessionPayload{User: user, Exp: time.Now().Add(s.TTL).Unix()})
	body := base64.RawURLEncoding.EncodeToString(p)
	return body + "." + s.sign(body)
}

// Verify 校验令牌,返回用户名。
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

// ---- 登录失败限速(防暴力破解) ----

// RateLimiter 以固定窗口统计每个来源 IP 的连续失败次数,
// 达到上限后在窗口期内拒绝继续尝试。
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

// Allow 报告该 IP 当前是否允许尝试登录;不允许时返回剩余封禁时长。
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

// Fail 记录一次失败。
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

// Reset 登录成功后清除该 IP 的失败记录。
func (rl *RateLimiter) Reset(ip string) {
	rl.mu.Lock()
	defer rl.mu.Unlock()
	delete(rl.entries, ip)
}

// GenerateSecret 生成 32 字节随机会话签名密钥。
func GenerateSecret() ([]byte, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return nil, fmt.Errorf("生成会话密钥失败: %w", err)
	}
	return b, nil
}
