package auth

import (
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"strconv"
)

// Accounts is the whole account store, and it lives in memory only.
//
// Accounts are declared with `serve -user name:password` and last exactly as
// long as the process. Nothing is persisted: there is no users table, no
// password hash at rest, and no command that writes one. The command line is
// therefore the complete and readable record of who may log in, a forgotten
// password is a one-line edit and a restart, and a stolen copy of the database
// file contains no credential to crack because there is none in it.
//
// The price, stated rather than buried: a process argument is world-readable in
// `ps` on a normal host, and it lands in shell history and in the unit file.
// There is no second path any more -- that was the point of removing it.
//
// What is kept in memory is not the password but a keyed tag of it (HMAC-SHA256
// under the session key). Verification compares tags, so the password itself
// exists in the process only for as long as flag parsing takes. That is a small
// win rather than a large one, since argv holds it anyway, but it costs nothing.
type Accounts struct {
	secret []byte
	names  []string
	tags   [][]byte
}

func NewAccounts(secret []byte) *Accounts {
	return &Accounts{secret: secret}
}

// Add declares one account. A repeated name replaces the earlier declaration,
// so the last -user on the command line wins rather than leaving two entries
// for the same name where only one could ever be reached.
func (a *Accounts) Add(name, password string) {
	tag := a.tag(name, password)
	for i, n := range a.names {
		if n == name {
			a.tags[i] = tag
			return
		}
	}
	a.names = append(a.names, name)
	a.tags = append(a.tags, tag)
}

func (a *Accounts) Len() int { return len(a.names) }

// Names is for logging, in declaration order.
func (a *Accounts) Names() []string {
	out := make([]string, len(a.names))
	copy(out, a.names)
	return out
}

// Verify checks a name/password pair and returns the fingerprint to mint the
// session token against.
//
// Every declared account is compared, with no early exit and on a fixed-length
// tag, so the time this takes does not say whether the name exists -- which is
// what the old code needed a dummy password hash to fake.
func (a *Accounts) Verify(name, password string) (string, bool) {
	got := a.tag(name, password)
	match := 0
	idx := 0
	for i, t := range a.tags {
		eq := subtle.ConstantTimeCompare(got, t)
		idx = subtle.ConstantTimeSelect(eq, i, idx)
		match |= eq
	}
	if match != 1 {
		return "", false
	}
	return fingerprint(a.tags[idx]), true
}

// Fingerprint returns the tag of the account as it stands now, or "" when no
// such account is declared. A session token carries the fingerprint it was
// issued against, so this is what makes editing the -user flag mean something:
// change the password or drop the account and the old sessions stop verifying
// on their next request instead of running out their TTL.
func (a *Accounts) Fingerprint(name string) string {
	for i, n := range a.names {
		if n == name {
			return fingerprint(a.tags[i])
		}
	}
	return ""
}

// tag binds the name and the password together under the session key.
//
// The key is loaded from session.key, which persists across restarts, so the
// tag -- and the fingerprint derived from it -- is stable for an unchanged
// password. That is deliberate: a restart with the same unit file must not log
// everyone out, and a changed password must.
//
// The name is length-prefixed so that "ab" + "c" and "a" + "bc" cannot produce
// the same tag.
func (a *Accounts) tag(name, password string) []byte {
	m := hmac.New(sha256.New, a.secret)
	m.Write([]byte(strconv.Itoa(len(name))))
	m.Write([]byte{0})
	m.Write([]byte(name))
	m.Write([]byte{0})
	m.Write([]byte(password))
	return m.Sum(nil)
}

// fingerprint truncates a tag to something short enough to ride inside a
// session token. It is already keyed, so truncation costs nothing that matters:
// the token holder cannot work backwards to the password without the key.
func fingerprint(tag []byte) string {
	return base64.RawURLEncoding.EncodeToString(tag[:8])
}
