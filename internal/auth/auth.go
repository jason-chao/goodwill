// Package auth handles dashboard passwords and sessions.
package auth

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"golang.org/x/crypto/argon2"

	"github.com/jason-chao/goodwill/internal/store"
)

// Argon2id parameters: 19 MiB of memory, two passes, one thread.
const (
	argonMemory  = 19 * 1024
	argonTime    = 2
	argonThreads = 1
	argonKeyLen  = 32
)

// HashPassword returns an encoded Argon2id hash of password.
func HashPassword(password string) (string, error) {
	salt := make([]byte, 16)
	if _, err := rand.Read(salt); err != nil {
		return "", err
	}
	key := argon2.IDKey([]byte(password), salt, argonTime, argonMemory, argonThreads, argonKeyLen)
	return fmt.Sprintf("$argon2id$v=%d$m=%d,t=%d,p=%d$%s$%s", argon2.Version, argonMemory, argonTime, argonThreads,
		base64.RawStdEncoding.EncodeToString(salt), base64.RawStdEncoding.EncodeToString(key)), nil
}

// CheckPassword reports whether password matches the encoded hash.
func CheckPassword(encoded, password string) bool {
	parts := strings.Split(encoded, "$")
	if len(parts) != 6 || parts[1] != "argon2id" {
		return false
	}
	var version int
	var memory, passes uint32
	var threads uint8
	if _, err := fmt.Sscanf(parts[2], "v=%d", &version); err != nil || version != argon2.Version {
		return false
	}
	if _, err := fmt.Sscanf(parts[3], "m=%d,t=%d,p=%d", &memory, &passes, &threads); err != nil {
		return false
	}
	salt, err := base64.RawStdEncoding.DecodeString(parts[4])
	if err != nil {
		return false
	}
	want, err := base64.RawStdEncoding.DecodeString(parts[5])
	if err != nil {
		return false
	}
	got := argon2.IDKey([]byte(password), salt, passes, memory, threads, uint32(len(want)))
	return subtle.ConstantTimeCompare(got, want) == 1
}

const (
	cookieName = "goodwill_session"
	sessionTTL = 30 * 24 * time.Hour
	// maxFailures is how many wrong passwords are tolerated per minute,
	// across all clients, before sign-in is paused.
	maxFailures = 10
)

// ErrThrottled is returned while sign-in is paused after repeated failures.
var ErrThrottled = errors.New("too many failed sign-in attempts; wait a minute")

// ErrBadLogin is returned for a wrong username or password.
var ErrBadLogin = errors.New("wrong username or password")

// Sessions signs users in and out.
type Sessions struct {
	Store  *store.Store
	Secure bool

	mu       sync.Mutex
	window   int64
	failures int
}

func randomToken() string {
	b := make([]byte, 32)
	rand.Read(b)
	return base64.RawURLEncoding.EncodeToString(b)
}

func hashToken(token string) []byte {
	sum := sha256.Sum256([]byte(token))
	return sum[:]
}

// A fixed hash to compare against when the user does not exist, so that the
// response time does not reveal which usernames are real.
var dummyHash = sync.OnceValue(func() string {
	h, _ := HashPassword("goodwill-placeholder")
	return h
})

// Login checks the credentials and, on success, sets the session cookie.
func (s *Sessions) Login(w http.ResponseWriter, username, password string) error {
	s.mu.Lock()
	if now := time.Now().Unix() / 60; now != s.window {
		s.window, s.failures = now, 0
	}
	throttled := s.failures >= maxFailures
	s.mu.Unlock()
	if throttled {
		return ErrThrottled
	}

	user, err := s.Store.UserByName(username)
	if err != nil {
		return err
	}
	hash := dummyHash()
	if user != nil {
		hash = user.PasswordHash
	}
	if !CheckPassword(hash, password) || user == nil {
		s.mu.Lock()
		s.failures++
		s.mu.Unlock()
		return ErrBadLogin
	}

	token := randomToken()
	if err := s.Store.CreateAuthSession(hashToken(token), user.ID, randomToken(), sessionTTL); err != nil {
		return err
	}
	http.SetCookie(w, &http.Cookie{
		Name: cookieName, Value: token, Path: "/", MaxAge: int(sessionTTL.Seconds()),
		HttpOnly: true, Secure: s.Secure, SameSite: http.SameSiteLaxMode,
	})
	return nil
}

// Current returns the session for the request, or nil if not signed in.
func (s *Sessions) Current(r *http.Request) *store.AuthSession {
	c, err := r.Cookie(cookieName)
	if err != nil || c.Value == "" {
		return nil
	}
	sess, err := s.Store.AuthSession(hashToken(c.Value))
	if err != nil {
		return nil
	}
	return sess
}

// Logout ends the session and clears the cookie.
func (s *Sessions) Logout(w http.ResponseWriter, r *http.Request) {
	if c, err := r.Cookie(cookieName); err == nil {
		s.Store.DeleteAuthSession(hashToken(c.Value))
	}
	http.SetCookie(w, &http.Cookie{Name: cookieName, Path: "/", MaxAge: -1, HttpOnly: true, Secure: s.Secure, SameSite: http.SameSiteLaxMode})
}

// SameOrigin reports whether a state-changing request came from the
// dashboard itself rather than from another site.
func SameOrigin(r *http.Request) bool {
	origin := r.Header.Get("Origin")
	if origin == "" {
		origin = r.Header.Get("Referer")
	}
	if origin == "" {
		return false
	}
	u, err := url.Parse(origin)
	return err == nil && u.Host == r.Host
}

// CheckCSRF compares the form's token with the session's.
func CheckCSRF(sess *store.AuthSession, r *http.Request) bool {
	return sess != nil && subtle.ConstantTimeCompare([]byte(r.PostFormValue("csrf")), []byte(sess.CSRF)) == 1
}
