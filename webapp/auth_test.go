package main

import (
	"html"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func newTestAuthStore(t *testing.T) (*authStore, string) {
	t.Helper()
	dir := t.TempDir()
	old := configDir
	configDir = dir
	t.Cleanup(func() { configDir = old })

	as := &authStore{}
	if err := as.Load(); err != nil {
		t.Fatalf("Load: %v", err)
	}
	pw, err := os.ReadFile(filepath.Join(dir, "initial-password.txt"))
	if err != nil {
		t.Fatalf("initial password file: %v", err)
	}
	return as, strings.TrimSpace(string(pw))
}

func TestBootstrapGeneratesUsablePassword(t *testing.T) {
	as, password := newTestAuthStore(t)

	if len(password) != passwordLength {
		t.Errorf("generated password length = %d, want %d", len(password), passwordLength)
	}
	if !as.Verify(password) {
		t.Error("generated password does not verify")
	}
	if as.Verify(password + "x") {
		t.Error("a wrong password verified")
	}

	// The password file holds a live credential.
	info, err := os.Stat(initialPwPath())
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Errorf("initial-password.txt mode = %v, want 0600", info.Mode().Perm())
	}
	authInfo, err := os.Stat(authPath())
	if err != nil {
		t.Fatal(err)
	}
	if authInfo.Mode().Perm() != 0o600 {
		t.Errorf("auth.json mode = %v, want 0600", authInfo.Mode().Perm())
	}
}

func TestSessionRoundTrip(t *testing.T) {
	as, _ := newTestAuthStore(t)

	token, err := as.issueSession()
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := as.validateSession(token); !ok {
		t.Fatal("freshly issued session did not validate")
	}

	// Tampering with the payload must fail the signature check.
	parts := strings.Split(token, ".")
	if _, ok := as.validateSession(parts[0] + "." + parts[1] + ".AAAA"); ok {
		t.Error("a forged signature validated")
	}
	if _, ok := as.validateSession("garbage"); ok {
		t.Error("garbage validated")
	}
	if _, ok := as.validateSession(""); ok {
		t.Error("empty cookie validated")
	}
}

// Changing the password must invalidate every outstanding session, which is
// the entire point of changing it.
func TestPasswordChangeInvalidatesSessions(t *testing.T) {
	as, password := newTestAuthStore(t)

	token, err := as.issueSession()
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := as.validateSession(token); !ok {
		t.Fatal("session should be valid before the change")
	}

	if err := as.SetPassword("a-much-longer-password"); err != nil {
		t.Fatalf("SetPassword: %v", err)
	}
	if _, ok := as.validateSession(token); ok {
		t.Error("old session still valid after a password change")
	}
	if as.Verify(password) {
		t.Error("old password still works")
	}
	if !as.Verify("a-much-longer-password") {
		t.Error("new password does not work")
	}
	// The generated password is dead, so it must stop being advertised.
	if _, err := os.Stat(initialPwPath()); !os.IsNotExist(err) {
		t.Error("initial-password.txt should be removed once the password changes")
	}
}

func TestSetPasswordRejectsShort(t *testing.T) {
	as, _ := newTestAuthStore(t)
	if err := as.SetPassword("short"); err == nil {
		t.Error("expected a minimum-length error")
	}
}

func TestExpiredSessionRejected(t *testing.T) {
	as, _ := newTestAuthStore(t)

	// Forge a correctly signed but expired cookie.
	raw := `{"iat":1,"exp":2,"pv":1}`
	msg := "v1." + b64([]byte(raw))
	token := msg + "." + sign(as.secret(), msg)

	if _, ok := as.validateSession(token); ok {
		t.Error("an expired session validated")
	}
}

func TestCSRFTokenDerivedAndRotates(t *testing.T) {
	as, _ := newTestAuthStore(t)

	s1, _ := as.issueSession()
	if as.csrfTokenFor(s1) == "" {
		t.Fatal("empty CSRF token for a valid session")
	}
	if as.csrfTokenFor(s1) != as.csrfTokenFor(s1) {
		t.Error("CSRF token is not stable for a given session")
	}
	if as.csrfTokenFor("") != "" {
		t.Error("CSRF token should be empty without a session")
	}

	time.Sleep(1100 * time.Millisecond) // iat has second resolution
	s2, _ := as.issueSession()
	if s1 != s2 && as.csrfTokenFor(s1) == as.csrfTokenFor(s2) {
		t.Error("CSRF token should rotate with the session")
	}
}

// The login CSRF token is rendered into an HTML attribute and posted back as
// a form value. Standard base64 would put '+' in it, html/template would
// escape that to "&#43;", and the resulting ';' makes ParseForm reject the
// whole submission. Keep the token to the URL-safe alphabet.
func TestLoginCSRFTokenIsURLAndHTMLSafe(t *testing.T) {
	for i := 0; i < 200; i++ {
		tok, err := generateToken()
		if err != nil {
			t.Fatal(err)
		}
		if strings.ContainsAny(tok, "+/=&;%<>\"' ") {
			t.Fatalf("token %q contains a character that needs escaping", tok)
		}
		if html.EscapeString(tok) != tok {
			t.Fatalf("token %q changes under HTML escaping", tok)
		}
		if url.QueryEscape(tok) != tok {
			t.Fatalf("token %q changes under URL escaping", tok)
		}
	}
}

// The rendered login page must contain the same token as the cookie, byte for
// byte, or the double-submit check fails.
func TestLoginPageTokenMatchesCookie(t *testing.T) {
	as, _ := newTestAuthStore(t)
	g := &authGuard{store: as, limiter: newRateLimiter()}

	for i := 0; i < 20; i++ {
		w := httptest.NewRecorder()
		g.handleLoginPage(w, httptest.NewRequest("GET", "/login", nil))

		var cookieToken string
		for _, c := range w.Result().Cookies() {
			if c.Name == csrfCookieName {
				cookieToken = c.Value
			}
		}
		if cookieToken == "" {
			t.Fatal("login page set no CSRF cookie")
		}
		if !strings.Contains(w.Body.String(), `value="`+cookieToken+`"`) {
			t.Fatalf("form token does not match cookie %q (escaped in the HTML?)", cookieToken)
		}
	}
}

func TestRateLimiterLocksOut(t *testing.T) {
	rl := newRateLimiter()

	for i := 0; i < rl.maxAttempts; i++ {
		if allowed, _ := rl.Allowed("1.2.3.4"); !allowed {
			t.Fatalf("locked out after only %d attempts", i)
		}
		rl.Fail("1.2.3.4")
	}
	allowed, wait := rl.Allowed("1.2.3.4")
	if allowed {
		t.Error("expected a lockout after the attempt limit")
	}
	if wait <= 0 {
		t.Error("expected a positive retry-after duration")
	}

	// A different source must be unaffected.
	if allowed, _ := rl.Allowed("5.6.7.8"); !allowed {
		t.Error("an unrelated client was locked out")
	}

	rl.Reset("1.2.3.4")
	if allowed, _ := rl.Allowed("1.2.3.4"); !allowed {
		t.Error("Reset did not clear the lockout")
	}
}

func TestRateLimiterEvictsToStayBounded(t *testing.T) {
	rl := newRateLimiter()
	rl.maxEntries = 10
	for i := 0; i < 100; i++ {
		rl.Fail(string(rune('a'+i%26)) + string(rune('0'+i/26)))
	}
	if len(rl.entries) > rl.maxEntries {
		t.Errorf("limiter grew to %d entries, cap is %d", len(rl.entries), rl.maxEntries)
	}
}

func TestClientIPIgnoresForwardedHeaderByDefault(t *testing.T) {
	r := httptest.NewRequest("POST", "/login", nil)
	r.RemoteAddr = "10.0.0.5:1234"
	r.Header.Set("X-Forwarded-For", "1.1.1.1")

	if got := clientIP(r); got != "10.0.0.5" {
		t.Errorf("clientIP = %q; X-Forwarded-For must not be trusted by default", got)
	}

	t.Setenv("TRUST_PROXY", "1")
	if got := clientIP(r); got != "1.1.1.1" {
		t.Errorf("clientIP with TRUST_PROXY = %q, want 1.1.1.1", got)
	}
}

func TestSameOrigin(t *testing.T) {
	r := httptest.NewRequest("POST", "http://matcha.local/api/run", nil)
	r.Host = "matcha.local"

	r.Header.Set("Origin", "http://matcha.local")
	if !sameOrigin(r) {
		t.Error("same origin rejected")
	}
	r.Header.Set("Origin", "http://evil.example")
	if sameOrigin(r) {
		t.Error("cross origin accepted")
	}

	r.Header.Del("Origin")
	r.Header.Set("Sec-Fetch-Site", "cross-site")
	if sameOrigin(r) {
		t.Error("cross-site Sec-Fetch-Site accepted")
	}
	r.Header.Set("Sec-Fetch-Site", "same-origin")
	if !sameOrigin(r) {
		t.Error("same-origin Sec-Fetch-Site rejected")
	}
}

// ?next= must not become an open redirect.
func TestSanitizeNext(t *testing.T) {
	cases := map[string]string{
		"":                      "/settings",
		"/settings":             "/settings",
		"/file/2026-09-02.md":   "/file/2026-09-02.md",
		"//evil.example/":       "/settings",
		"https://evil.example/": "/settings",
		"/ok\nSet-Cookie: x=y":  "/settings",
	}
	for in, want := range cases {
		if got := sanitizeNext(in); got != want {
			t.Errorf("sanitizeNext(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestCookieSecureDetection(t *testing.T) {
	r := httptest.NewRequest("GET", "http://matcha.local/settings", nil)
	if cookieSecure(r) {
		t.Error("Secure must not be set on plain HTTP, or LAN logins silently break")
	}

	r.Header.Set("X-Forwarded-Proto", "https")
	if !cookieSecure(r) {
		t.Error("Secure should be set behind an HTTPS proxy")
	}

	r.Header.Del("X-Forwarded-Proto")
	t.Setenv("MATCHA_COOKIE_SECURE", "1")
	if !cookieSecure(r) {
		t.Error("MATCHA_COOKIE_SECURE override ignored")
	}
}

func TestRequireAuthBlocksAndRedirects(t *testing.T) {
	as, _ := newTestAuthStore(t)
	g := &authGuard{store: as, limiter: newRateLimiter()}

	called := false
	h := g.requireAuth(func(w http.ResponseWriter, r *http.Request) { called = true })

	// HTML request without a session: redirect to login.
	w := httptest.NewRecorder()
	h(w, httptest.NewRequest("GET", "/settings", nil))
	if called {
		t.Fatal("handler ran without a session")
	}
	if w.Code != http.StatusFound {
		t.Errorf("status = %d, want 302", w.Code)
	}

	// API request without a session: 401 JSON, not a redirect.
	w = httptest.NewRecorder()
	h(w, httptest.NewRequest("GET", "/api/settings", nil))
	if w.Code != http.StatusUnauthorized {
		t.Errorf("api status = %d, want 401", w.Code)
	}

	// With a valid session the handler runs.
	token, _ := as.issueSession()
	r := httptest.NewRequest("GET", "/settings", nil)
	r.AddCookie(&http.Cookie{Name: sessionCookieName, Value: token})
	w = httptest.NewRecorder()
	h(w, r)
	if !called {
		t.Error("handler did not run with a valid session")
	}
}

// An unreadable auth.json must fail closed, never fall back to generating new
// credentials.
func TestRequireAuthFailsClosedOnLoadError(t *testing.T) {
	as, _ := newTestAuthStore(t)
	g := &authGuard{store: as, limiter: newRateLimiter(), loadErr: os.ErrInvalid}

	token, _ := as.issueSession()
	r := httptest.NewRequest("GET", "/settings", nil)
	r.AddCookie(&http.Cookie{Name: sessionCookieName, Value: token})

	w := httptest.NewRecorder()
	g.requireAuth(func(w http.ResponseWriter, r *http.Request) {
		t.Error("handler ran despite an auth store load error")
	})(w, r)

	if w.Code != http.StatusServiceUnavailable {
		t.Errorf("status = %d, want 503", w.Code)
	}
}

func TestCorruptAuthFileIsNotRegenerated(t *testing.T) {
	dir := t.TempDir()
	old := configDir
	configDir = dir
	defer func() { configDir = old }()

	if err := os.WriteFile(filepath.Join(dir, "auth.json"), []byte("{ broken"), 0o600); err != nil {
		t.Fatal(err)
	}
	as := &authStore{}
	if err := as.Load(); err == nil {
		t.Fatal("a corrupt auth.json must be an error, not a silent re-bootstrap")
	}
	if _, err := os.Stat(filepath.Join(dir, "initial-password.txt")); !os.IsNotExist(err) {
		t.Error("a corrupt auth.json must not mint a new password")
	}
}

func TestRequireCSRF(t *testing.T) {
	as, _ := newTestAuthStore(t)
	g := &authGuard{store: as, limiter: newRateLimiter()}

	token, _ := as.issueSession()
	csrf := as.csrfTokenFor(token)

	newReq := func() *http.Request {
		r := httptest.NewRequest("POST", "http://matcha.local/api/run", nil)
		r.Host = "matcha.local"
		r.Header.Set("Origin", "http://matcha.local")
		r.AddCookie(&http.Cookie{Name: sessionCookieName, Value: token})
		return r
	}

	called := false
	h := g.requireCSRF(func(w http.ResponseWriter, r *http.Request) { called = true })

	// Missing token.
	w := httptest.NewRecorder()
	h(w, newReq())
	if w.Code != http.StatusForbidden || called {
		t.Errorf("missing CSRF token accepted (status %d)", w.Code)
	}

	// Wrong token.
	r := newReq()
	r.Header.Set("X-CSRF-Token", "nope")
	w = httptest.NewRecorder()
	h(w, r)
	if w.Code != http.StatusForbidden || called {
		t.Errorf("bad CSRF token accepted (status %d)", w.Code)
	}

	// Correct token.
	r = newReq()
	r.Header.Set("X-CSRF-Token", csrf)
	w = httptest.NewRecorder()
	h(w, r)
	if !called {
		t.Errorf("valid CSRF token rejected (status %d)", w.Code)
	}
}
