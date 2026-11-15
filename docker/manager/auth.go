package main

import (
	"crypto/pbkdf2"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"net/http"
	"sync"
	"time"
)

// Web UI sign-in. The password comes from WEBUI_PASSWORD, or is chosen on
// the first visit and stored as a PBKDF2 hash. Sessions are random cookies
// kept in memory (signing in again after a container restart).
//
// CSRF: every /api request must carry the header X-Tjena-UI: 1, which a
// page on another site can't send without a CORS preflight we never allow;
// the cookie is SameSite=Strict as well.
type Auth struct {
	envPass string
	prefs   *Prefs

	mu       sync.Mutex
	sessions map[string]time.Time
	fails    int
	failAt   time.Time
}

const (
	cookieName = "tjena_bridge_session"
	sessionTTL = 30 * 24 * time.Hour
	passIter   = 600_000
)

func newAuth(envPass string, prefs *Prefs) *Auth {
	return &Auth{envPass: envPass, prefs: prefs, sessions: map[string]time.Time{}}
}

func (a *Auth) configured() bool {
	_, _, hash := a.prefs.Get()
	return a.envPass != "" || hash != ""
}

func hashPass(pw string, salt []byte) string {
	k, _ := pbkdf2.Key(sha256.New, pw, salt, passIter, 32)
	return base64.RawStdEncoding.EncodeToString(k)
}

func (a *Auth) check(pw string) bool {
	if a.envPass != "" {
		return subtle.ConstantTimeCompare([]byte(pw), []byte(a.envPass)) == 1
	}
	_, salt, hash := a.prefs.Get()
	s, err := base64.RawStdEncoding.DecodeString(salt)
	if err != nil || hash == "" {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(hashPass(pw, s)), []byte(hash)) == 1
}

func (a *Auth) setPassword(pw string) error {
	if a.envPass != "" {
		return errors.New("The password is set with WEBUI_PASSWORD. Change it there.")
	}
	if len([]rune(pw)) < 10 {
		return errors.New("Use at least 10 characters.")
	}
	salt := make([]byte, 16)
	_, _ = rand.Read(salt)
	err := a.prefs.Update(func(p *Prefs) {
		p.PassSalt = base64.RawStdEncoding.EncodeToString(salt)
		p.PassHash = hashPass(pw, salt)
	})
	if err == nil {
		a.mu.Lock()
		a.sessions = map[string]time.Time{} // sign out everywhere
		a.mu.Unlock()
	}
	return err
}

// login checks the password, slowing down repeated failures.
func (a *Auth) login(w http.ResponseWriter, r *http.Request, pw string) bool {
	a.mu.Lock()
	wait := time.Duration(0)
	if a.fails > 3 {
		wait = min(time.Duration(a.fails-3)*2*time.Second, 30*time.Second) - time.Since(a.failAt)
	}
	a.mu.Unlock()
	if wait > 0 {
		time.Sleep(wait)
	}
	if !a.check(pw) {
		a.mu.Lock()
		a.fails++
		a.failAt = time.Now()
		a.mu.Unlock()
		time.Sleep(time.Second)
		return false
	}
	a.mu.Lock()
	a.fails = 0
	a.mu.Unlock()
	a.newSession(w, r)
	return true
}

func (a *Auth) newSession(w http.ResponseWriter, r *http.Request) {
	tok := randomSecret(32)
	a.mu.Lock()
	now := time.Now()
	for k, exp := range a.sessions {
		if now.After(exp) {
			delete(a.sessions, k)
		}
	}
	a.sessions[tok] = now.Add(sessionTTL)
	a.mu.Unlock()
	http.SetCookie(w, &http.Cookie{
		Name: cookieName, Value: tok, Path: "/", HttpOnly: true,
		SameSite: http.SameSiteStrictMode, Secure: isHTTPS(r), MaxAge: int(sessionTTL.Seconds()),
	})
}

func isHTTPS(r *http.Request) bool {
	return r.TLS != nil || r.Header.Get("X-Forwarded-Proto") == "https"
}

func (a *Auth) valid(r *http.Request) bool {
	c, err := r.Cookie(cookieName)
	if err != nil {
		return false
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	exp, ok := a.sessions[c.Value]
	return ok && time.Now().Before(exp)
}

func (a *Auth) logout(w http.ResponseWriter, r *http.Request) {
	if c, err := r.Cookie(cookieName); err == nil {
		a.mu.Lock()
		delete(a.sessions, c.Value)
		a.mu.Unlock()
	}
	http.SetCookie(w, &http.Cookie{Name: cookieName, Value: "", Path: "/", MaxAge: -1, HttpOnly: true})
}
