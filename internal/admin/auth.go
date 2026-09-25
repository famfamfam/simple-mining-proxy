package admin

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"mime"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"
)

const (
	cookieName      = "smp_session"
	sessionLifetime = 12 * time.Hour
)

// auth checks logins: a cookie session for the UI and a Bearer token
// for scripts. Every password and token check goes through the guard.
type auth struct {
	username, password, token string
	guard                     *guard

	mu       sync.Mutex
	sessions map[string]time.Time // id -> expiry
}

func newAuth(username, password, token string, g *guard) *auth {
	return &auth{username: username, password: password, token: token, guard: g,
		sessions: map[string]time.Time{}}
}

func equal(a, b string) bool {
	aHash := sha256.Sum256([]byte(a))
	bHash := sha256.Sum256([]byte(b))
	return subtle.ConstantTimeCompare(aHash[:], bHash[:]) == 1
}

func clientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

// authenticate checks the Bearer token or the session cookie. A wrong
// Bearer token counts as a failed attempt, like a wrong password.
func (a *auth) authenticate(r *http.Request, now time.Time) (bool, attemptResult) {
	if h := r.Header.Get("Authorization"); a.token != "" && strings.HasPrefix(h, "Bearer ") {
		res := a.guard.attempt(clientIP(r), now, func() bool {
			return equal(strings.TrimSpace(strings.TrimPrefix(h, "Bearer ")), a.token)
		})
		return res.OK, res
	}
	c, err := r.Cookie(cookieName)
	if err != nil || c.Value == "" {
		return false, attemptResult{}
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	exp, found := a.sessions[c.Value]
	if !found {
		return false, attemptResult{}
	}
	if now.After(exp) {
		delete(a.sessions, c.Value)
		return false, attemptResult{}
	}
	return true, attemptResult{OK: true}
}

// attemptLogin checks the credentials through the guard and, on success,
// opens a new session.
func (a *auth) attemptLogin(ip, username, password string, now time.Time) (string, attemptResult) {
	res := a.guard.attempt(ip, now, func() bool {
		// Compare both values every time so timing does not reveal which failed.
		okUser := equal(username, a.username)
		okPass := equal(password, a.password)
		return okUser && okPass
	})
	if !res.OK {
		return "", res
	}
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", attemptResult{}
	}
	id := hex.EncodeToString(b)
	a.mu.Lock()
	for key, exp := range a.sessions {
		if now.After(exp) {
			delete(a.sessions, key)
		}
	}
	a.sessions[id] = now.Add(sessionLifetime)
	a.mu.Unlock()
	return id, res
}

func (a *auth) logout(r *http.Request) {
	if c, err := r.Cookie(cookieName); err == nil {
		a.mu.Lock()
		delete(a.sessions, c.Value)
		a.mu.Unlock()
	}
}

func secureRequest(r *http.Request) bool {
	return r.TLS != nil || strings.EqualFold(r.Header.Get("X-Forwarded-Proto"), "https")
}

func (a *auth) setCookie(w http.ResponseWriter, r *http.Request, id string, maxAge int) {
	http.SetCookie(w, &http.Cookie{
		Name: cookieName, Value: id, Path: "/", MaxAge: maxAge,
		HttpOnly: true, SameSite: http.SameSiteStrictMode, Secure: secureRequest(r),
	})
}

// jsonOnly rejects state-changing requests without Content-Type:
// application/json. Such a request from another site needs a CORS preflight,
// which the proxy never allows, so this blocks CSRF together with SameSite.
func jsonOnly(r *http.Request) bool {
	switch r.Method {
	case http.MethodGet, http.MethodHead, http.MethodOptions:
		return true
	}
	mt, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	return err == nil && mt == "application/json"
}
