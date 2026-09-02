package main

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"log"
	"math/big"
	"net"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"golang.org/x/crypto/bcrypt"
)

const (
	sessionCookieName = "matcha_session"
	csrfCookieName    = "matcha_csrf"

	sessionLifetime = 30 * 24 * time.Hour
	// Re-issue a cookie once it is this old so an active user is never logged
	// out mid-session, without minting a fresh cookie on every request.
	sessionRefreshAfter = 7 * 24 * time.Hour
	loginCSRFLifetime   = 10 * time.Minute

	bcryptCost        = 12
	minPasswordLength = 10

	// Alphabet for the generated first-run password: no 0/O/1/l/I, because
	// this is a string people read off a terminal and retype.
	passwordAlphabet = "abcdefghijkmnopqrstuvwxyzABCDEFGHJKLMNPQRSTUVWXYZ23456789"
	passwordLength   = 20
)

// authFile is the on-disk credential store.
type authFile struct {
	Version         int       `json:"version"`
	PasswordHash    string    `json:"password_hash"`
	SessionSecret   string    `json:"session_secret"`
	PasswordVersion int       `json:"password_version"`
	UpdatedAt       time.Time `json:"updated_at"`
}

type authStore struct {
	mu sync.RWMutex
	a  authFile
}

// dummyHash gives failed logins against a store with no usable hash the same
// cost as a real comparison, so "is this instance claimed" is not readable
// from response latency. Computed lazily; bcrypt at cost 12 is deliberately
// slow and we do not want to pay for it on every startup.
var (
	dummyHashOnce sync.Once
	dummyHash     []byte
)

func compareWithDummy(password string) {
	dummyHashOnce.Do(func() {
		h, err := bcrypt.GenerateFromPassword([]byte("matcha-placeholder"), bcryptCost)
		if err == nil {
			dummyHash = h
		}
	})
	if dummyHash != nil {
		bcrypt.CompareHashAndPassword(dummyHash, []byte(password))
	}
}

func generateSecret() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("generate session secret: %w", err)
	}
	return base64.StdEncoding.EncodeToString(b), nil
}

// generateToken produces a random value safe to put in a cookie, an HTML
// attribute and a form body without any encoding step.
//
// Standard base64 is wrong here: its '+' gets escaped to &#43; when rendered
// into the login form, and the resulting ';' makes Go's own ParseForm reject
// the submission. Browsers decode the entity and cope, but there is no reason
// to depend on that.
func generateToken() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("generate token: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

func generatePassword() (string, error) {
	max := big.NewInt(int64(len(passwordAlphabet)))
	out := make([]byte, passwordLength)
	for i := range out {
		n, err := rand.Int(rand.Reader, max)
		if err != nil {
			return "", fmt.Errorf("generate password: %w", err)
		}
		out[i] = passwordAlphabet[n.Int64()]
	}
	return string(out), nil
}

// Load reads auth.json, creating it with a random password on first run.
//
// A corrupt file is a hard error rather than a reason to regenerate: silently
// re-creating credentials would turn file corruption into a free takeover of
// an already-claimed instance.
func (as *authStore) Load() error {
	data, err := os.ReadFile(authPath())
	if err != nil {
		if os.IsNotExist(err) {
			return as.bootstrap()
		}
		return fmt.Errorf("read %s: %w", authPath(), err)
	}

	var a authFile
	if err := json.Unmarshal(data, &a); err != nil {
		return fmt.Errorf(
			"%s is unreadable (%v); fix the file or delete it to have a new password generated",
			authPath(), err)
	}
	if a.SessionSecret == "" {
		return fmt.Errorf("%s has no session secret; delete it to have a new password generated", authPath())
	}

	as.mu.Lock()
	as.a = a
	as.mu.Unlock()

	// Nag on every start while the generated password is still in force, so
	// this does not quietly stay the credential forever.
	if _, err := os.Stat(initialPwPath()); err == nil {
		log.Printf("NOTE: still using the generated admin password. It is in %s. Change it at /settings and the file will be removed.", initialPwPath())
	}
	return nil
}

func (as *authStore) bootstrap() error {
	password, err := generatePassword()
	if err != nil {
		return err
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcryptCost)
	if err != nil {
		return fmt.Errorf("hash generated password: %w", err)
	}
	secret, err := generateSecret()
	if err != nil {
		return err
	}

	a := authFile{
		Version:         1,
		PasswordHash:    string(hash),
		SessionSecret:   secret,
		PasswordVersion: 1,
		UpdatedAt:       time.Now().UTC(),
	}
	if err := as.persist(a); err != nil {
		return err
	}

	// Written to disk as well as logged: on Unraid/Synology the container log
	// is easy to miss, and the config directory is right there on the host.
	if err := writeFileAtomic(initialPwPath(), []byte(password+"\n"), 0o600); err != nil {
		log.Printf("WARNING: could not write %s: %v", initialPwPath(), err)
	}

	log.Printf("+------------------------------------------------------------+")
	log.Printf("|  Matcha settings password generated                          ")
	log.Printf("|                                                              ")
	log.Printf("|    %s", password)
	log.Printf("|                                                              ")
	log.Printf("|  Also saved to %s", initialPwPath())
	log.Printf("|  Log in at http://<host>:7321/settings and change it.        ")
	log.Printf("+------------------------------------------------------------+")
	return nil
}

func (as *authStore) persist(a authFile) error {
	data, err := json.MarshalIndent(a, "", "  ")
	if err != nil {
		return fmt.Errorf("encode auth store: %w", err)
	}
	if err := writeFileAtomic(authPath(), append(data, '\n'), 0o600); err != nil {
		return err
	}
	as.mu.Lock()
	as.a = a
	as.mu.Unlock()
	return nil
}

func (as *authStore) get() authFile {
	as.mu.RLock()
	defer as.mu.RUnlock()
	return as.a
}

func (as *authStore) secret() []byte {
	b, _ := base64.StdEncoding.DecodeString(as.get().SessionSecret)
	return b
}

// Verify checks a password, always paying the bcrypt cost.
func (as *authStore) Verify(password string) bool {
	a := as.get()
	if a.PasswordHash == "" {
		compareWithDummy(password)
		return false
	}
	return bcrypt.CompareHashAndPassword([]byte(a.PasswordHash), []byte(password)) == nil
}

// SetPassword replaces the password and bumps PasswordVersion, which
// invalidates every outstanding session -- including an attacker's, which is
// the entire point of changing a password.
func (as *authStore) SetPassword(newPassword string) error {
	if len([]rune(newPassword)) < minPasswordLength {
		return fmt.Errorf("password must be at least %d characters", minPasswordLength)
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(newPassword), bcryptCost)
	if err != nil {
		return fmt.Errorf("hash password: %w", err)
	}

	a := as.get()
	a.PasswordHash = string(hash)
	a.PasswordVersion++
	a.UpdatedAt = time.Now().UTC()
	if err := as.persist(a); err != nil {
		return err
	}

	// The generated password is no longer valid, so stop advertising it.
	if err := os.Remove(initialPwPath()); err != nil && !os.IsNotExist(err) {
		log.Printf("WARNING: could not remove %s: %v", initialPwPath(), err)
	}
	return nil
}

// ---------- sessions ----------

type sessionPayload struct {
	IssuedAt        int64 `json:"iat"`
	ExpiresAt       int64 `json:"exp"`
	PasswordVersion int   `json:"pv"`
}

func b64(b []byte) string            { return base64.RawURLEncoding.EncodeToString(b) }
func unb64(s string) ([]byte, error) { return base64.RawURLEncoding.DecodeString(s) }

func sign(secret []byte, msg string) string {
	m := hmac.New(sha256.New, secret)
	m.Write([]byte(msg))
	return b64(m.Sum(nil))
}

func (as *authStore) issueSession() (string, error) {
	now := time.Now()
	p := sessionPayload{
		IssuedAt:        now.Unix(),
		ExpiresAt:       now.Add(sessionLifetime).Unix(),
		PasswordVersion: as.get().PasswordVersion,
	}
	raw, err := json.Marshal(p)
	if err != nil {
		return "", err
	}
	msg := "v1." + b64(raw)
	return msg + "." + sign(as.secret(), msg), nil
}

// validateSession returns the payload if the cookie is authentic, unexpired,
// and issued under the current password.
func (as *authStore) validateSession(value string) (sessionPayload, bool) {
	var p sessionPayload

	parts := strings.Split(value, ".")
	if len(parts) != 3 || parts[0] != "v1" {
		return p, false
	}
	msg := parts[0] + "." + parts[1]
	// Compare before decoding: never parse attacker-controlled bytes that
	// have not been authenticated.
	if !hmac.Equal([]byte(sign(as.secret(), msg)), []byte(parts[2])) {
		return p, false
	}
	raw, err := unb64(parts[1])
	if err != nil {
		return p, false
	}
	if err := json.Unmarshal(raw, &p); err != nil {
		return p, false
	}
	if time.Now().Unix() >= p.ExpiresAt {
		return p, false
	}
	if p.PasswordVersion != as.get().PasswordVersion {
		return p, false
	}
	return p, true
}

// csrfTokenFor derives a token from the session cookie, so it needs no
// server-side storage and rotates whenever the session does.
func (as *authStore) csrfTokenFor(sessionValue string) string {
	if sessionValue == "" {
		return ""
	}
	return sign(as.secret(), "csrf|"+sessionValue)
}

// cookieSecure decides whether to set the Secure flag.
//
// Hardcoding it would break the common plain-HTTP LAN deployment in a
// maddening way: the login POST appears to succeed and the next request comes
// back anonymous, because the browser refuses to send the cookie.
func cookieSecure(r *http.Request) bool {
	if os.Getenv("MATCHA_COOKIE_SECURE") == "1" {
		return true
	}
	if r.TLS != nil {
		return true
	}
	return r.Header.Get("X-Forwarded-Proto") == "https"
}

func setSessionCookie(w http.ResponseWriter, r *http.Request, value string) {
	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookieName,
		Value:    value,
		Path:     "/",
		HttpOnly: true,
		Secure:   cookieSecure(r),
		SameSite: http.SameSiteLaxMode,
		MaxAge:   int(sessionLifetime.Seconds()),
	})
}

func clearCookie(w http.ResponseWriter, r *http.Request, name string) {
	http.SetCookie(w, &http.Cookie{
		Name:     name,
		Value:    "",
		Path:     "/",
		HttpOnly: true,
		Secure:   cookieSecure(r),
		SameSite: http.SameSiteLaxMode,
		MaxAge:   -1,
	})
}

func cookieValue(r *http.Request, name string) string {
	c, err := r.Cookie(name)
	if err != nil {
		return ""
	}
	return c.Value
}

// ---------- rate limiting ----------

type attemptRecord struct {
	count       int
	windowStart time.Time
	lockedUntil time.Time
	lastSeen    time.Time
}

type rateLimiter struct {
	mu          sync.Mutex
	entries     map[string]*attemptRecord
	maxEntries  int
	window      time.Duration
	maxAttempts int
	lockout     time.Duration
}

func newRateLimiter() *rateLimiter {
	return &rateLimiter{
		entries:     make(map[string]*attemptRecord),
		maxEntries:  1000,
		window:      15 * time.Minute,
		maxAttempts: 5,
		lockout:     15 * time.Minute,
	}
}

// Allowed reports whether key may attempt a login, and if not, how long until
// it may.
func (rl *rateLimiter) Allowed(key string) (bool, time.Duration) {
	rl.mu.Lock()
	defer rl.mu.Unlock()

	rec, ok := rl.entries[key]
	if !ok {
		return true, 0
	}
	now := time.Now()
	rec.lastSeen = now
	if now.Before(rec.lockedUntil) {
		return false, time.Until(rec.lockedUntil)
	}
	if now.Sub(rec.windowStart) > rl.window {
		rec.count = 0
		rec.windowStart = now
	}
	return true, 0
}

func (rl *rateLimiter) Fail(key string) {
	rl.mu.Lock()
	defer rl.mu.Unlock()

	now := time.Now()
	rec, ok := rl.entries[key]
	if !ok {
		rl.evictLocked()
		rec = &attemptRecord{windowStart: now}
		rl.entries[key] = rec
	}
	if now.Sub(rec.windowStart) > rl.window {
		rec.count = 0
		rec.windowStart = now
	}
	rec.count++
	rec.lastSeen = now
	if rec.count >= rl.maxAttempts {
		rec.lockedUntil = now.Add(rl.lockout)
	}
}

func (rl *rateLimiter) Reset(key string) {
	rl.mu.Lock()
	delete(rl.entries, key)
	rl.mu.Unlock()
}

// evictLocked keeps the map bounded so a stream of spoofed sources cannot
// grow it without limit. Caller holds rl.mu.
func (rl *rateLimiter) evictLocked() {
	if len(rl.entries) < rl.maxEntries {
		return
	}
	var oldestKey string
	var oldest time.Time
	for k, v := range rl.entries {
		if oldestKey == "" || v.lastSeen.Before(oldest) {
			oldestKey, oldest = k, v.lastSeen
		}
	}
	delete(rl.entries, oldestKey)
}

// clientIP identifies the caller for rate limiting.
//
// X-Forwarded-For is only honoured when TRUST_PROXY is set: taking it on faith
// would let anyone bypass the limiter by varying a header.
func clientIP(r *http.Request) string {
	if os.Getenv("TRUST_PROXY") == "1" {
		if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
			if first, _, ok := strings.Cut(xff, ","); ok {
				return strings.TrimSpace(first)
			}
			return strings.TrimSpace(xff)
		}
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

// ---------- middleware ----------

type authGuard struct {
	store   *authStore
	limiter *rateLimiter
	// loadErr is set when auth.json could not be read. Everything behind the
	// guard fails closed rather than falling back to an unauthenticated state.
	loadErr error
}

func (g *authGuard) authenticated(r *http.Request) bool {
	if g.loadErr != nil {
		return false
	}
	_, ok := g.store.validateSession(cookieValue(r, sessionCookieName))
	return ok
}

func isAPIPath(p string) bool { return strings.HasPrefix(p, "/api/") }

func (g *authGuard) requireAuth(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if g.loadErr != nil {
			msg := "Settings are unavailable: " + g.loadErr.Error()
			if isAPIPath(r.URL.Path) {
				writeJSONError(w, http.StatusServiceUnavailable, msg)
				return
			}
			http.Error(w, msg, http.StatusServiceUnavailable)
			return
		}

		payload, ok := g.store.validateSession(cookieValue(r, sessionCookieName))
		if !ok {
			if isAPIPath(r.URL.Path) {
				writeJSONError(w, http.StatusUnauthorized, "not signed in")
				return
			}
			dest := "/login"
			if next := r.URL.RequestURI(); next != "/" && r.Method == http.MethodGet {
				dest += "?next=" + urlQueryEscape(next)
			}
			http.Redirect(w, r, dest, http.StatusFound)
			return
		}

		// Slide the expiry so an active user is not logged out on day 30.
		if time.Since(time.Unix(payload.IssuedAt, 0)) > sessionRefreshAfter && r.Method == http.MethodGet {
			if fresh, err := g.store.issueSession(); err == nil {
				setSessionCookie(w, r, fresh)
			}
		}
		next(w, r)
	}
}

// sameOrigin rejects cross-site state-changing requests. SameSite=Lax already
// covers current browsers; this is the belt to that pair of braces.
func sameOrigin(r *http.Request) bool {
	if origin := r.Header.Get("Origin"); origin != "" {
		u, err := parseRequestURL(origin)
		if err != nil {
			return false
		}
		return u.Host == r.Host
	}
	switch r.Header.Get("Sec-Fetch-Site") {
	case "", "same-origin", "none":
		return true
	}
	return false
}

func (g *authGuard) requireCSRF(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !sameOrigin(r) {
			writeJSONError(w, http.StatusForbidden, "cross-origin request rejected")
			return
		}
		session := cookieValue(r, sessionCookieName)
		want := g.store.csrfTokenFor(session)
		got := r.Header.Get("X-CSRF-Token")
		if got == "" {
			got = r.FormValue("csrf_token")
		}
		if want == "" || !hmac.Equal([]byte(want), []byte(got)) {
			writeJSONError(w, http.StatusForbidden, "invalid or missing CSRF token")
			return
		}
		next(w, r)
	}
}

// ---------- handlers ----------

type loginPageData struct {
	Next      string
	CSRFToken string
	Error     string
}

func (g *authGuard) handleLoginPage(w http.ResponseWriter, r *http.Request) {
	if g.loadErr != nil {
		http.Error(w, "Settings are unavailable: "+g.loadErr.Error(), http.StatusServiceUnavailable)
		return
	}
	if g.authenticated(r) {
		http.Redirect(w, r, "/settings", http.StatusFound)
		return
	}

	// No session exists yet, so CSRF uses a double-submit cookie instead of a
	// session-derived token.
	token, err := generateToken()
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	http.SetCookie(w, &http.Cookie{
		Name:     csrfCookieName,
		Value:    token,
		Path:     "/",
		HttpOnly: true,
		Secure:   cookieSecure(r),
		SameSite: http.SameSiteLaxMode,
		MaxAge:   int(loginCSRFLifetime.Seconds()),
	})

	render(w, "login.html", loginPageData{
		Next:      sanitizeNext(r.URL.Query().Get("next")),
		CSRFToken: token,
	})
}

func (g *authGuard) handleLoginSubmit(w http.ResponseWriter, r *http.Request) {
	if g.loadErr != nil {
		http.Error(w, "Settings are unavailable: "+g.loadErr.Error(), http.StatusServiceUnavailable)
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	next := sanitizeNext(r.FormValue("next"))

	renderLoginError := func(status int, msg string) {
		w.WriteHeader(status)
		render(w, "login.html", loginPageData{
			Next:      next,
			CSRFToken: cookieValue(r, csrfCookieName),
			Error:     msg,
		})
	}

	if !sameOrigin(r) {
		renderLoginError(http.StatusForbidden, "Request rejected. Please try again from the login page.")
		return
	}
	cookieToken := cookieValue(r, csrfCookieName)
	formToken := r.FormValue("csrf_token")
	if cookieToken == "" || !hmac.Equal([]byte(cookieToken), []byte(formToken)) {
		renderLoginError(http.StatusForbidden, "Your login form expired. Please try again.")
		return
	}

	key := clientIP(r)
	if allowed, wait := g.limiter.Allowed(key); !allowed {
		w.Header().Set("Retry-After", fmt.Sprintf("%d", int(wait.Seconds())+1))
		renderLoginError(http.StatusTooManyRequests, fmt.Sprintf(
			"Too many failed attempts. Try again in %s.", wait.Round(time.Second)))
		return
	}

	if !g.store.Verify(r.FormValue("password")) {
		g.limiter.Fail(key)
		log.Printf("failed login attempt from %s", key)
		renderLoginError(http.StatusUnauthorized, "Incorrect password.")
		return
	}

	g.limiter.Reset(key)
	session, err := g.store.issueSession()
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	setSessionCookie(w, r, session)
	clearCookie(w, r, csrfCookieName)
	http.Redirect(w, r, next, http.StatusFound)
}

func (g *authGuard) handleLogout(w http.ResponseWriter, r *http.Request) {
	clearCookie(w, r, sessionCookieName)
	http.Redirect(w, r, "/login", http.StatusFound)
}

type passwordChangeRequest struct {
	CurrentPassword string `json:"current_password"`
	NewPassword     string `json:"new_password"`
	ConfirmPassword string `json:"confirm_password"`
}

func (g *authGuard) handleChangePassword(w http.ResponseWriter, r *http.Request) {
	var req passwordChangeRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<16)).Decode(&req); err != nil {
		writeJSONError(w, http.StatusBadRequest, "could not read request")
		return
	}

	key := clientIP(r)
	if allowed, wait := g.limiter.Allowed(key); !allowed {
		w.Header().Set("Retry-After", fmt.Sprintf("%d", int(wait.Seconds())+1))
		writeJSONError(w, http.StatusTooManyRequests,
			fmt.Sprintf("Too many attempts. Try again in %s.", wait.Round(time.Second)))
		return
	}
	if !g.store.Verify(req.CurrentPassword) {
		g.limiter.Fail(key)
		writeJSONError(w, http.StatusForbidden, "Current password is incorrect.")
		return
	}
	g.limiter.Reset(key)

	if req.NewPassword != req.ConfirmPassword {
		writeJSONError(w, http.StatusBadRequest, "New passwords do not match.")
		return
	}
	if err := g.store.SetPassword(req.NewPassword); err != nil {
		writeJSONError(w, http.StatusBadRequest, err.Error())
		return
	}

	// SetPassword bumped the password version, invalidating the cookie the
	// caller just used. Issue a fresh one so changing your own password does
	// not log you out.
	session, err := g.store.issueSession()
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "password changed, but the session could not be renewed; please sign in again")
		return
	}
	setSessionCookie(w, r, session)

	writeJSON(w, http.StatusOK, map[string]any{
		"ok":         true,
		"csrf_token": g.store.csrfTokenFor(session),
	})
}

// sanitizeNext keeps post-login redirects on this site. Without it, ?next=
// is an open redirect.
func sanitizeNext(next string) string {
	if next == "" || !strings.HasPrefix(next, "/") || strings.HasPrefix(next, "//") {
		return "/settings"
	}
	if strings.Contains(next, "\n") || strings.Contains(next, "\r") {
		return "/settings"
	}
	return next
}
