package main

import (
	"crypto/hmac"
	"crypto/pbkdf2"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	sessionCookie = "fn_session"
	pbkdf2Iter    = 300_000
)

// ---------- secret hashing (PBKDF2-SHA256, stdlib only) ----------

func hashSecret(secret string) (string, error) {
	salt := randomBytes(16)
	key, err := pbkdf2.Key(sha256.New, secret, salt, pbkdf2Iter, 32)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("pbkdf2-sha256$%d$%s$%s", pbkdf2Iter,
		base64.RawStdEncoding.EncodeToString(salt),
		base64.RawStdEncoding.EncodeToString(key)), nil
}

func verifySecret(encoded, secret string) bool {
	parts := strings.Split(encoded, "$")
	if len(parts) != 4 || parts[0] != "pbkdf2-sha256" {
		return false
	}
	iter, err := strconv.Atoi(parts[1])
	if err != nil || iter < 1 {
		return false
	}
	salt, err1 := base64.RawStdEncoding.DecodeString(parts[2])
	want, err2 := base64.RawStdEncoding.DecodeString(parts[3])
	if err1 != nil || err2 != nil {
		return false
	}
	got, err := pbkdf2.Key(sha256.New, secret, salt, iter, len(want))
	if err != nil {
		return false
	}
	return subtle.ConstantTimeCompare(got, want) == 1
}

func tokenKey(token string) string {
	h := sha256.Sum256([]byte(token))
	return hex.EncodeToString(h[:])
}

// ---------- brute-force limiter ----------

type limiter struct {
	mu   sync.Mutex
	hits map[string][]time.Time
	max  int
	win  time.Duration
}

func newLimiter(max int, win time.Duration) *limiter {
	return &limiter{hits: map[string][]time.Time{}, max: max, win: win}
}

// Blocked reports whether key has exceeded the failure budget.
func (l *limiter) Blocked(key string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	return len(l.prune(key)) >= l.max
}

func (l *limiter) Fail(key string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.hits[key] = append(l.prune(key), time.Now())
}

func (l *limiter) Reset(key string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	delete(l.hits, key)
}

func (l *limiter) prune(key string) []time.Time {
	cut := time.Now().Add(-l.win)
	h := l.hits[key]
	i := 0
	for i < len(h) && h[i].Before(cut) {
		i++
	}
	h = h[i:]
	if len(h) == 0 {
		delete(l.hits, key)
		return nil
	}
	l.hits[key] = h
	return h
}

func (l *limiter) GC() {
	l.mu.Lock()
	defer l.mu.Unlock()
	for k := range l.hits {
		l.prune(k)
	}
}

// ---------- request helpers ----------

func (s *Server) clientIP(r *http.Request) string {
	if s.cfg.TrustProxy {
		if v := r.Header.Get("X-Real-IP"); v != "" {
			return strings.TrimSpace(v)
		}
		if v := r.Header.Get("X-Forwarded-For"); v != "" {
			parts := strings.Split(v, ",")
			return strings.TrimSpace(parts[len(parts)-1])
		}
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

func (s *Server) isHTTPS(r *http.Request) bool {
	if r.TLS != nil {
		return true
	}
	if s.cfg.Origin != "" {
		return strings.HasPrefix(s.cfg.Origin, "https://")
	}
	return s.cfg.TrustProxy && strings.EqualFold(r.Header.Get("X-Forwarded-Proto"), "https")
}

// origin returns the public origin (scheme://host[:port]) for this request.
func (s *Server) origin(r *http.Request) string {
	if s.cfg.Origin != "" {
		return s.cfg.Origin
	}
	scheme := "http"
	if s.isHTTPS(r) {
		scheme = "https"
	}
	host := r.Host
	if s.cfg.TrustProxy {
		if h := r.Header.Get("X-Forwarded-Host"); h != "" {
			host = strings.TrimSpace(strings.Split(h, ",")[0])
		}
	}
	return scheme + "://" + host
}

// ---------- sessions ----------

func (s *Server) newSession(w http.ResponseWriter, r *http.Request, method string) error {
	tok := randomToken(32)
	now := time.Now()
	ua := r.UserAgent()
	if len(ua) > 200 {
		ua = ua[:200]
	}
	sessionLifetime := time.Duration(s.cfg.SessionDays) * 24 * time.Hour
	err := s.st.Update(func(st *State) error {
		st.Sessions[tokenKey(tok)] = &Session{
			Created: now, Expires: now.Add(sessionLifetime),
			IP: s.clientIP(r), UA: ua, Method: method,
		}
		return nil
	})
	if err != nil {
		return err
	}
	http.SetCookie(w, &http.Cookie{
		Name: sessionCookie, Value: tok, Path: "/",
		Expires: now.Add(sessionLifetime), MaxAge: int(sessionLifetime / time.Second),
		HttpOnly: true, Secure: s.isHTTPS(r), SameSite: http.SameSiteLaxMode,
	})
	return nil
}

// sessionKey returns the hashed key of a valid session or "".
func (s *Server) sessionKey(r *http.Request) string {
	c, err := r.Cookie(sessionCookie)
	if err != nil || c.Value == "" {
		return ""
	}
	k := tokenKey(c.Value)
	ok := false
	s.st.View(func(st *State) {
		if sess, found := st.Sessions[k]; found && time.Now().Before(sess.Expires) {
			ok = true
		}
	})
	if !ok {
		return ""
	}
	return k
}

func (s *Server) authed(r *http.Request) bool { return !s.cfg.Auth || s.sessionKey(r) != "" }

// requireAuth wraps an admin handler; state-changing methods also need the
// CSRF header (cross-site requests cannot set custom headers without CORS).
func (s *Server) requireAuth(h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !s.authed(r) {
			jsonErr(w, http.StatusUnauthorized, "not logged in")
			return
		}
		if !csrfOK(r) {
			jsonErr(w, http.StatusForbidden, "missing CSRF header")
			return
		}
		h(w, r)
	}
}

// requireWrite is requireAuth plus a read_only check.
func (s *Server) requireWrite(h http.HandlerFunc) http.HandlerFunc {
	return s.requireAuth(func(w http.ResponseWriter, r *http.Request) {
		if s.cfg.ReadOnly {
			jsonErr(w, http.StatusForbidden, "server is in read-only mode")
			return
		}
		h(w, r)
	})
}

// requireFeature disables an endpoint when a config switch is off.
func requireFeature(on bool, msg string, h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !on {
			jsonErr(w, http.StatusForbidden, msg)
			return
		}
		h(w, r)
	}
}

func csrfOK(r *http.Request) bool {
	switch r.Method {
	case http.MethodGet, http.MethodHead, http.MethodOptions:
		return true
	}
	return r.Header.Get("X-Filenav") == "1"
}

// ---------- handlers ----------

func (s *Server) handleMe(w http.ResponseWriter, r *http.Request) {
	var passkeys int
	var passkeyOnly bool
	s.st.View(func(st *State) { passkeys = len(st.Passkeys); passkeyOnly = st.PasskeyOnly })
	if !s.cfg.Passkeys {
		passkeys, passkeyOnly = 0, false
	}
	writeJSON(w, map[string]any{
		"auth":          s.cfg.Auth,
		"passkeys_on":   s.cfg.Passkeys,
		"read_only":     s.cfg.ReadOnly,
		"shares_on":     s.cfg.Shares,
		"analyzer_on":   s.cfg.Analyzer,
		"share_uploads": s.cfg.ShareUploads,
		"max_upload":    s.cfg.MaxUploadMB << 20,
		"authed":        s.authed(r),
		"title":         s.cfg.Title,
		"version":       version,
		"passkeys":      passkeys,
		"passkey_only":  passkeyOnly,
		"chunk_size":    s.cfg.ChunkSize,
	})
}

func (s *Server) handleLogin(w http.ResponseWriter, r *http.Request) {
	if !csrfOK(r) {
		jsonErr(w, http.StatusForbidden, "missing CSRF header")
		return
	}
	ip := s.clientIP(r)
	if s.loginLimit.Blocked(ip) {
		jsonErr(w, http.StatusTooManyRequests, "too many attempts, try again later")
		return
	}
	var req struct {
		Passcode string `json:"passcode"`
	}
	if !readJSON(w, r, &req) {
		return
	}
	var hash string
	var passkeyOnly bool
	s.st.View(func(st *State) { hash = st.PasscodeHash; passkeyOnly = st.PasskeyOnly })
	if passkeyOnly && s.cfg.Passkeys {
		jsonErr(w, http.StatusForbidden, "passcode login is disabled; use a passkey")
		return
	}
	if !verifySecret(hash, req.Passcode) {
		s.loginLimit.Fail(ip)
		time.Sleep(500 * time.Millisecond)
		jsonErr(w, http.StatusUnauthorized, "wrong passcode")
		return
	}
	s.loginLimit.Reset(ip)
	if err := s.newSession(w, r, "passcode"); err != nil {
		jsonErr(w, 500, err.Error())
		return
	}
	writeJSON(w, map[string]any{"ok": true})
}

func (s *Server) handleLogout(w http.ResponseWriter, r *http.Request) {
	if k := s.sessionKey(r); k != "" {
		_ = s.st.Update(func(st *State) error { delete(st.Sessions, k); return nil })
	}
	http.SetCookie(w, &http.Cookie{Name: sessionCookie, Value: "", Path: "/", MaxAge: -1, HttpOnly: true})
	writeJSON(w, map[string]any{"ok": true})
}

func (s *Server) handleChangePasscode(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Current string `json:"current"`
		New     string `json:"new"`
	}
	if !readJSON(w, r, &req) {
		return
	}
	ip := s.clientIP(r)
	if s.loginLimit.Blocked(ip) {
		jsonErr(w, http.StatusTooManyRequests, "too many attempts, try again later")
		return
	}
	if len(req.New) < 4 {
		jsonErr(w, 400, "new passcode must be at least 4 characters")
		return
	}
	var hash string
	s.st.View(func(st *State) { hash = st.PasscodeHash })
	if !verifySecret(hash, req.Current) {
		s.loginLimit.Fail(ip)
		jsonErr(w, http.StatusForbidden, "current passcode is wrong")
		return
	}
	nh, err := hashSecret(req.New)
	if err != nil {
		jsonErr(w, 500, err.Error())
		return
	}
	keep := s.sessionKey(r)
	err = s.st.Update(func(st *State) error {
		st.PasscodeHash = nh
		for k := range st.Sessions { // log out everyone else
			if k != keep {
				delete(st.Sessions, k)
			}
		}
		return nil
	})
	if err != nil {
		jsonErr(w, 500, err.Error())
		return
	}
	writeJSON(w, map[string]any{"ok": true})
}

func (s *Server) handlePasskeyOnly(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Enabled bool `json:"enabled"`
	}
	if !readJSON(w, r, &req) {
		return
	}
	err := s.st.Update(func(st *State) error {
		if req.Enabled && len(st.Passkeys) == 0 {
			return errors.New("register a passkey first")
		}
		st.PasskeyOnly = req.Enabled
		return nil
	})
	if err != nil {
		jsonErr(w, 400, err.Error())
		return
	}
	writeJSON(w, map[string]any{"ok": true})
}

func (s *Server) handleSessions(w http.ResponseWriter, r *http.Request) {
	cur := s.sessionKey(r)
	type out struct {
		ID      string    `json:"id"`
		Created time.Time `json:"created"`
		Expires time.Time `json:"expires"`
		IP      string    `json:"ip"`
		UA      string    `json:"ua"`
		Method  string    `json:"method"`
		Current bool      `json:"current"`
	}
	var list []out
	s.st.View(func(st *State) {
		for k, v := range st.Sessions {
			list = append(list, out{k[:16], v.Created, v.Expires, v.IP, v.UA, v.Method, k == cur})
		}
	})
	writeJSON(w, map[string]any{"sessions": list})
}

func (s *Server) handleRevokeSessions(w http.ResponseWriter, r *http.Request) {
	cur := s.sessionKey(r)
	id := r.PathValue("id")
	_ = s.st.Update(func(st *State) error {
		for k := range st.Sessions {
			if k == cur {
				continue
			}
			if id == "others" || strings.HasPrefix(k, id) && len(id) == 16 {
				delete(st.Sessions, k)
			}
		}
		return nil
	})
	writeJSON(w, map[string]any{"ok": true})
}

// ---------- share access tokens (HMAC-signed cookie) ----------

func shareCookieName(id string) string { return "fns_" + id }

func (s *Server) shareToken(sh *Share, exp time.Time) string {
	var secret []byte
	s.st.View(func(st *State) { secret = st.Secret })
	e := strconv.FormatInt(exp.Unix(), 10)
	m := hmac.New(sha256.New, secret)
	m.Write([]byte("share|" + sh.ID + "|" + sh.PassHash + "|" + e))
	return e + "." + base64.RawURLEncoding.EncodeToString(m.Sum(nil))
}

func (s *Server) shareUnlocked(r *http.Request, sh *Share) bool {
	if sh.PassHash == "" {
		return true
	}
	c, err := r.Cookie(shareCookieName(sh.ID))
	if err != nil {
		return false
	}
	e, _, ok := strings.Cut(c.Value, ".")
	if !ok {
		return false
	}
	n, err := strconv.ParseInt(e, 10, 64)
	if err != nil || time.Now().Unix() > n {
		return false
	}
	return hmac.Equal([]byte(c.Value), []byte(s.shareToken(sh, time.Unix(n, 0))))
}
