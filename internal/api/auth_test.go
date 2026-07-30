package api

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestNewRejectsInvalidAuthenticationConfiguration(t *testing.T) {
	validSecret := "a-session-secret-that-is-definitely-at-least-32-bytes"
	sameValue := "same-password-and-secret-value-over-thirty-two-bytes"
	tests := []struct {
		name      string
		password  string
		secret    string
		ttl       string
		wantInErr string
	}{
		{
			name:      "missing password",
			secret:    validSecret,
			wantInErr: "APP_ACCESS_PASSWORD",
		},
		{
			name:      "missing session secret",
			password:  testAccessPassword,
			wantInErr: "APP_AUTH_SESSION_SECRET",
		},
		{
			name:      "short session secret",
			password:  testAccessPassword,
			secret:    "too-short",
			wantInErr: "at least 32 bytes",
		},
		{
			name:      "session secret equals password",
			password:  sameValue,
			secret:    sameValue,
			wantInErr: "must differ",
		},
		{
			name:      "zero ttl",
			password:  testAccessPassword,
			secret:    validSecret,
			ttl:       "0",
			wantInErr: "APP_AUTH_SESSION_TTL_HOURS",
		},
		{
			name:      "ttl above maximum",
			password:  testAccessPassword,
			secret:    validSecret,
			ttl:       "25",
			wantInErr: "APP_AUTH_SESSION_TTL_HOURS",
		},
		{
			name:      "non numeric ttl",
			password:  testAccessPassword,
			secret:    validSecret,
			ttl:       "twelve",
			wantInErr: "APP_AUTH_SESSION_TTL_HOURS",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			setAuthEnvironment(t, "production", tt.password, tt.secret, tt.ttl)
			srv, err := New(context.Background())
			if err == nil {
				srv.Close()
				t.Fatal("New succeeded with invalid authentication configuration")
			}
			if !strings.Contains(err.Error(), tt.wantInErr) {
				t.Fatalf("error = %q, want it to contain %q", err, tt.wantInErr)
			}
			for _, secret := range []string{tt.password, tt.secret} {
				if secret != "" && strings.Contains(err.Error(), secret) {
					t.Fatalf("configuration error leaks a secret: %q", err)
				}
			}
		})
	}
}

func TestAuthenticationLifecycle(t *testing.T) {
	srv := newAPITestServer(t)
	fakeNow := time.Date(2026, 7, 30, 9, 0, 0, 0, time.UTC)
	srv.auth.now = func() time.Time { return fakeNow }

	var logOutput bytes.Buffer
	srv.SetLogger(func(format string, args ...any) {
		fmt.Fprintf(&logOutput, format, args...)
	})

	t.Run("empty password is rejected without a cookie", func(t *testing.T) {
		rec := authRequest(t, srv, http.MethodPost, "/api/auth/login", `{"password":""}`, nil, "198.51.100.10:1000")
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("status = %d, want 400 (body=%s)", rec.Code, rec.Body.String())
		}
		assertAuthError(t, rec, "AUTH_PASSWORD_REQUIRED", 0)
		if len(rec.Result().Cookies()) != 0 {
			t.Fatal("empty password response unexpectedly sets a cookie")
		}
	})

	t.Run("password is compared without trimming", func(t *testing.T) {
		body := `{"password":" ` + testAccessPassword + ` "}`
		rec := authRequest(t, srv, http.MethodPost, "/api/auth/login", body, nil, "198.51.100.11:1000")
		if rec.Code != http.StatusUnauthorized {
			t.Fatalf("status = %d, want 401 (body=%s)", rec.Code, rec.Body.String())
		}
		assertAuthError(t, rec, "AUTH_INVALID", 0)
	})

	t.Run("wrong password is sanitized", func(t *testing.T) {
		const wrongPassword = "wrong-password-not-for-logs"
		rec := authRequest(t, srv, http.MethodPost, "/api/auth/login", `{"password":"`+wrongPassword+`"}`, nil, "198.51.100.12:1000")
		if rec.Code != http.StatusUnauthorized {
			t.Fatalf("status = %d, want 401 (body=%s)", rec.Code, rec.Body.String())
		}
		assertAuthError(t, rec, "AUTH_INVALID", 0)
		for _, secret := range []string{wrongPassword, testAccessPassword, testSessionSecret} {
			if strings.Contains(rec.Body.String(), secret) || strings.Contains(logOutput.String(), secret) {
				t.Fatalf("response or log contains a secret")
			}
		}
	})

	var sessionCookie *http.Cookie
	t.Run("correct password creates a secure session shape", func(t *testing.T) {
		rec := authRequest(t, srv, http.MethodPost, "/api/auth/login", `{"password":"`+testAccessPassword+`"}`, nil, "198.51.100.13:1000")
		if rec.Code != http.StatusNoContent {
			t.Fatalf("status = %d, want 204 (body=%s)", rec.Code, rec.Body.String())
		}
		if rec.Body.Len() != 0 {
			t.Fatalf("204 response body = %q, want empty", rec.Body.String())
		}
		sessionCookie = responseCookie(t, rec, accessSessionCookie)
		if !sessionCookie.HttpOnly {
			t.Error("session cookie is not HttpOnly")
		}
		if sessionCookie.SameSite != http.SameSiteLaxMode {
			t.Errorf("SameSite = %v, want Lax", sessionCookie.SameSite)
		}
		if sessionCookie.Path != "/" {
			t.Errorf("Path = %q, want /", sessionCookie.Path)
		}
		if sessionCookie.MaxAge != int(defaultSessionTTL.Seconds()) {
			t.Errorf("MaxAge = %d, want %d", sessionCookie.MaxAge, int(defaultSessionTTL.Seconds()))
		}
		if sessionCookie.Secure {
			t.Error("test/development cookie unexpectedly has Secure set")
		}
	})

	t.Run("session endpoint returns authentication and expiry", func(t *testing.T) {
		rec := authRequest(t, srv, http.MethodGet, "/api/auth/session", "", sessionCookie, "198.51.100.13:1000")
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200 (body=%s)", rec.Code, rec.Body.String())
		}
		var got authSessionResponse
		if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
			t.Fatalf("decode session response: %v", err)
		}
		if !got.Authenticated {
			t.Error("authenticated = false, want true")
		}
		wantExpiry := fakeNow.Add(defaultSessionTTL).Format(time.RFC3339)
		if got.ExpiresAt != wantExpiry {
			t.Errorf("expiresAt = %q, want %q", got.ExpiresAt, wantExpiry)
		}
	})

	t.Run("tampered cookie is rejected and cleared", func(t *testing.T) {
		tampered := *sessionCookie
		tampered.Value += "x"
		rec := authRequest(t, srv, http.MethodGet, "/api/auth/session", "", &tampered, "198.51.100.13:1000")
		if rec.Code != http.StatusUnauthorized {
			t.Fatalf("status = %d, want 401 (body=%s)", rec.Code, rec.Body.String())
		}
		assertAuthError(t, rec, "AUTH_REQUIRED", 0)
		cleared := responseCookie(t, rec, accessSessionCookie)
		if cleared.MaxAge != -1 {
			t.Errorf("clearing cookie MaxAge = %d, want -1", cleared.MaxAge)
		}
		if strings.Contains(rec.Body.String(), tampered.Value) {
			t.Fatal("error response leaks the session token")
		}
	})

	t.Run("logout is idempotent and revokes the presented session", func(t *testing.T) {
		rec := authRequest(t, srv, http.MethodPost, "/api/auth/logout", "", sessionCookie, "198.51.100.13:1000")
		if rec.Code != http.StatusNoContent {
			t.Fatalf("logout status = %d, want 204", rec.Code)
		}
		cleared := responseCookie(t, rec, accessSessionCookie)
		if cleared.MaxAge != -1 {
			t.Errorf("logout cookie MaxAge = %d, want -1", cleared.MaxAge)
		}

		reuse := authRequest(t, srv, http.MethodGet, "/api/auth/session", "", sessionCookie, "198.51.100.13:1000")
		if reuse.Code != http.StatusUnauthorized {
			t.Fatalf("revoked cookie status = %d, want 401", reuse.Code)
		}
		again := authRequest(t, srv, http.MethodPost, "/api/auth/logout", "", nil, "198.51.100.13:1000")
		if again.Code != http.StatusNoContent {
			t.Fatalf("repeated logout status = %d, want 204", again.Code)
		}

		relogin := authRequest(t, srv, http.MethodPost, "/api/auth/login", `{"password":"`+testAccessPassword+`"}`, nil, "198.51.100.13:1000")
		reloginCookie := responseCookie(t, relogin, accessSessionCookie)
		if reloginCookie.Value == sessionCookie.Value {
			t.Fatal("re-login in the same second reused a revoked session token")
		}
		reloginSession := authRequest(t, srv, http.MethodGet, "/api/auth/session", "", reloginCookie, "198.51.100.13:1000")
		if reloginSession.Code != http.StatusOK {
			t.Fatalf("re-login session status = %d, want 200", reloginSession.Code)
		}
	})
}

func TestAuthenticationSessionExpiryAndPasswordRotation(t *testing.T) {
	srv := newAPITestServer(t)
	fakeNow := time.Date(2026, 7, 30, 10, 0, 0, 0, time.UTC)
	srv.auth.now = func() time.Time { return fakeNow }
	login := authRequest(t, srv, http.MethodPost, "/api/auth/login", `{"password":"`+testAccessPassword+`"}`, nil, "203.0.113.20:1000")
	if login.Code != http.StatusNoContent {
		t.Fatalf("login status = %d, want 204", login.Code)
	}
	cookie := responseCookie(t, login, accessSessionCookie)

	t.Run("expired session", func(t *testing.T) {
		fakeNow = fakeNow.Add(defaultSessionTTL + time.Second)
		rec := authRequest(t, srv, http.MethodGet, "/api/auth/session", "", cookie, "203.0.113.20:1000")
		if rec.Code != http.StatusUnauthorized {
			t.Fatalf("status = %d, want 401", rec.Code)
		}
	})

	t.Run("password rotation", func(t *testing.T) {
		rotated := newServerWithAuthentication(t, "test", "rotated-access-password", testSessionSecret, "")
		rotated.auth.now = func() time.Time { return time.Date(2026, 7, 30, 11, 0, 0, 0, time.UTC) }
		rec := authRequest(t, rotated, http.MethodGet, "/api/auth/session", "", cookie, "203.0.113.20:1000")
		if rec.Code != http.StatusUnauthorized {
			t.Fatalf("status = %d, want 401", rec.Code)
		}
	})

	t.Run("session secret rotation", func(t *testing.T) {
		rotated := newServerWithAuthentication(t, "test", testAccessPassword, "rotated-session-secret-with-more-than-thirty-two-bytes", "")
		rotated.auth.now = func() time.Time { return time.Date(2026, 7, 30, 11, 0, 0, 0, time.UTC) }
		rec := authRequest(t, rotated, http.MethodGet, "/api/auth/session", "", cookie, "203.0.113.20:1000")
		if rec.Code != http.StatusUnauthorized {
			t.Fatalf("status = %d, want 401", rec.Code)
		}
	})
}

func TestAuthenticationRateLimitAndSuccessReset(t *testing.T) {
	srv := newAPITestServer(t)
	fakeNow := time.Date(2026, 7, 30, 11, 0, 0, 0, time.UTC)
	srv.auth.now = func() time.Time { return fakeNow }
	const source = "203.0.113.30:1000"

	for i := 1; i <= maxLoginFailures; i++ {
		rec := authRequest(t, srv, http.MethodPost, "/api/auth/login", `{"password":"wrong"}`, nil, source)
		if rec.Code != http.StatusUnauthorized {
			t.Fatalf("failure %d status = %d, want 401", i, rec.Code)
		}
	}

	blocked := authRequest(t, srv, http.MethodPost, "/api/auth/login", `{"password":"`+testAccessPassword+`"}`, nil, source)
	if blocked.Code != http.StatusTooManyRequests {
		t.Fatalf("sixth attempt status = %d, want 429 (body=%s)", blocked.Code, blocked.Body.String())
	}
	assertAuthError(t, blocked, "AUTH_RATE_LIMITED", 60)
	if got := blocked.Header().Get("Retry-After"); got != "60" {
		t.Errorf("Retry-After = %q, want 60", got)
	}

	fakeNow = fakeNow.Add(30 * time.Second)
	stillBlocked := authRequest(t, srv, http.MethodPost, "/api/auth/login", `{"password":"`+testAccessPassword+`"}`, nil, source)
	assertAuthError(t, stillBlocked, "AUTH_RATE_LIMITED", 30)

	fakeNow = fakeNow.Add(31 * time.Second)
	success := authRequest(t, srv, http.MethodPost, "/api/auth/login", `{"password":"`+testAccessPassword+`"}`, nil, source)
	if success.Code != http.StatusNoContent {
		t.Fatalf("post-cooldown status = %d, want 204", success.Code)
	}

	// A successful verification clears earlier failures for the same source.
	oneFailure := authRequest(t, srv, http.MethodPost, "/api/auth/login", `{"password":"wrong"}`, nil, source)
	if oneFailure.Code != http.StatusUnauthorized {
		t.Fatalf("failure before reset status = %d, want 401", oneFailure.Code)
	}
	reset := authRequest(t, srv, http.MethodPost, "/api/auth/login", `{"password":"`+testAccessPassword+`"}`, nil, source)
	if reset.Code != http.StatusNoContent {
		t.Fatalf("reset login status = %d, want 204", reset.Code)
	}
	for i := 1; i <= maxLoginFailures; i++ {
		rec := authRequest(t, srv, http.MethodPost, "/api/auth/login", `{"password":"wrong-again"}`, nil, source)
		if rec.Code != http.StatusUnauthorized {
			t.Fatalf("post-reset failure %d status = %d, want 401", i, rec.Code)
		}
	}
	limited := authRequest(t, srv, http.MethodPost, "/api/auth/login", `{"password":"wrong-again"}`, nil, source)
	if limited.Code != http.StatusTooManyRequests {
		t.Fatalf("post-reset sixth failure status = %d, want 429", limited.Code)
	}
}

func TestAuthenticationEmptyPasswordDoesNotConsumeRateLimit(t *testing.T) {
	srv := newAPITestServer(t)
	for i := 0; i < maxLoginFailures+2; i++ {
		rec := authRequest(t, srv, http.MethodPost, "/api/auth/login", `{"password":""}`, nil, "203.0.113.31:1000")
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("empty attempt %d status = %d, want 400", i+1, rec.Code)
		}
	}
	success := authRequest(t, srv, http.MethodPost, "/api/auth/login", `{"password":"`+testAccessPassword+`"}`, nil, "203.0.113.31:1000")
	if success.Code != http.StatusNoContent {
		t.Fatalf("correct password after empty attempts status = %d, want 204", success.Code)
	}
}

func TestAuthenticationRateLimitIsAtomicAcrossConcurrentRequests(t *testing.T) {
	srv := newAPITestServer(t)
	srv.auth.now = func() time.Time {
		return time.Date(2026, 7, 30, 11, 30, 0, 0, time.UTC)
	}

	const attempts = 20
	start := make(chan struct{})
	statuses := make(chan int, attempts)
	for i := 0; i < attempts; i++ {
		go func() {
			<-start
			req := httptest.NewRequest(
				http.MethodPost,
				"/api/auth/login",
				bytes.NewBufferString(`{"password":"wrong"}`),
			)
			req.RemoteAddr = "203.0.113.32:1000"
			rec := httptest.NewRecorder()
			srv.Router().ServeHTTP(rec, req)
			statuses <- rec.Code
		}()
	}
	close(start)

	var unauthorized, limited int
	for i := 0; i < attempts; i++ {
		switch status := <-statuses; status {
		case http.StatusUnauthorized:
			unauthorized++
		case http.StatusTooManyRequests:
			limited++
		default:
			t.Fatalf("unexpected concurrent login status %d", status)
		}
	}
	if unauthorized != maxLoginFailures {
		t.Errorf("unauthorized responses = %d, want exactly %d", unauthorized, maxLoginFailures)
	}
	if limited != attempts-maxLoginFailures {
		t.Errorf("rate-limited responses = %d, want %d", limited, attempts-maxLoginFailures)
	}
}

func TestAuthenticationMiddlewareProtectsBusinessAPIs(t *testing.T) {
	srv := newAPITestServer(t)
	protected := []struct {
		name   string
		method string
		path   string
	}{
		{name: "create project", method: http.MethodPost, path: "/api/projects"},
		{name: "list projects", method: http.MethodGet, path: "/api/projects"},
		{name: "project detail", method: http.MethodGet, path: "/api/projects/project-id"},
		{name: "create run", method: http.MethodPost, path: "/api/projects/project-id/runs"},
		{name: "files", method: http.MethodGet, path: "/api/projects/project-id/files"},
		{name: "write file", method: http.MethodPut, path: "/api/projects/project-id/files/src/App.tsx"},
		{name: "versions", method: http.MethodGet, path: "/api/projects/project-id/versions"},
		{name: "version files", method: http.MethodGet, path: "/api/projects/project-id/versions/version-id/files"},
		{name: "restore", method: http.MethodPost, path: "/api/projects/project-id/versions/version-id/restore"},
		{name: "events", method: http.MethodGet, path: "/api/runs/run-id/events"},
		{name: "retry", method: http.MethodPost, path: "/api/runs/run-id/retry"},
		{name: "compile result", method: http.MethodPost, path: "/api/runs/run-id/compile-result"},
	}
	for _, tt := range protected {
		t.Run(tt.name, func(t *testing.T) {
			rec := authRequest(t, srv, tt.method, tt.path, "", nil, "203.0.113.40:1000")
			if rec.Code != http.StatusUnauthorized {
				t.Fatalf("status = %d, want 401 (body=%s)", rec.Code, rec.Body.String())
			}
			assertAuthError(t, rec, "AUTH_REQUIRED", 0)
		})
	}

	t.Run("health remains public", func(t *testing.T) {
		rec := authRequest(t, srv, http.MethodGet, "/api/health", "", nil, "203.0.113.40:1000")
		if rec.Code != http.StatusOK {
			t.Fatalf("health status = %d, want 200", rec.Code)
		}
	})

	t.Run("valid session reaches the business handler", func(t *testing.T) {
		login := authRequest(t, srv, http.MethodPost, "/api/auth/login", `{"password":"`+testAccessPassword+`"}`, nil, "203.0.113.40:1000")
		cookie := responseCookie(t, login, accessSessionCookie)
		rec := authRequest(t, srv, http.MethodGet, "/api/projects", "", cookie, "203.0.113.40:1000")
		if rec.Code != http.StatusOK {
			t.Fatalf("authenticated list status = %d, want 200 (body=%s)", rec.Code, rec.Body.String())
		}
	})
}

func TestProductionCookieIsSecure(t *testing.T) {
	srv := newServerWithAuthentication(t, "production", testAccessPassword, testSessionSecret, "1")
	rec := authRequest(t, srv, http.MethodPost, "/api/auth/login", `{"password":"`+testAccessPassword+`"}`, nil, "203.0.113.50:1000")
	if rec.Code != http.StatusNoContent {
		t.Fatalf("login status = %d, want 204", rec.Code)
	}
	cookie := responseCookie(t, rec, accessSessionCookie)
	if !cookie.Secure {
		t.Error("production session cookie is not Secure")
	}
	if cookie.MaxAge != int(time.Hour.Seconds()) {
		t.Errorf("MaxAge = %d, want %d", cookie.MaxAge, int(time.Hour.Seconds()))
	}
}

func TestRequestSourceUsesTheLastProxyHopAddress(t *testing.T) {
	req := httptest.NewRequest(http.MethodPost, "/api/auth/login", nil)
	req.RemoteAddr = "172.18.0.4:4321"
	req.Header.Set("X-Forwarded-For", "198.51.100.99, 203.0.113.60")
	if got := requestSource(req); got != "203.0.113.60" {
		t.Errorf("requestSource = %q, want last proxy-added address", got)
	}

	req.RemoteAddr = "198.51.100.20:4321"
	req.Header.Set("X-Forwarded-For", "203.0.113.61")
	if got := requestSource(req); got != "198.51.100.20" {
		t.Errorf("requestSource trusted a header from a public direct peer: %q", got)
	}
}

func setAuthEnvironment(t *testing.T, appEnv, password, secret, ttl string) {
	t.Helper()
	t.Setenv("APP_ENV", appEnv)
	t.Setenv("APP_ACCESS_PASSWORD", password)
	t.Setenv("APP_AUTH_SESSION_SECRET", secret)
	t.Setenv("APP_AUTH_SESSION_TTL_HOURS", ttl)
	t.Setenv("DATABASE_PATH", ":memory:")
	t.Setenv("ANTHROPIC_API_KEY", "test-key")
	t.Setenv("ANTHROPIC_AUTH_TOKEN", "")
	t.Setenv("ANTHROPIC_BASE_URL", "")
	t.Setenv("ANTHROPIC_MODEL", "test-model")
}

func newServerWithAuthentication(t *testing.T, appEnv, password, secret, ttl string) *Server {
	t.Helper()
	setAuthEnvironment(t, appEnv, password, secret, ttl)
	srv, err := New(context.Background())
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(func() { srv.Close() })
	return srv
}

func authRequest(
	t *testing.T,
	srv *Server,
	method string,
	path string,
	body string,
	cookie *http.Cookie,
	remoteAddr string,
) *httptest.ResponseRecorder {
	t.Helper()
	var requestBody *bytes.Reader
	if body == "" {
		requestBody = bytes.NewReader(nil)
	} else {
		requestBody = bytes.NewReader([]byte(body))
	}
	req := httptest.NewRequest(method, path, requestBody)
	req.RemoteAddr = remoteAddr
	req.Header.Set("X-Request-ID", "auth-test-request")
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	if cookie != nil {
		req.AddCookie(cookie)
	}
	rec := httptest.NewRecorder()
	srv.Router().ServeHTTP(rec, req)
	return rec
}

func responseCookie(t *testing.T, rec *httptest.ResponseRecorder, name string) *http.Cookie {
	t.Helper()
	for _, cookie := range rec.Result().Cookies() {
		if cookie.Name == name {
			return cookie
		}
	}
	t.Fatalf("response has no %s cookie; Set-Cookie=%q", name, rec.Header().Values("Set-Cookie"))
	return nil
}

func assertAuthError(t *testing.T, rec *httptest.ResponseRecorder, wantCode string, wantRetryAfter int) {
	t.Helper()
	var got authErrorEnvelope
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode auth error: %v (body=%s)", err, rec.Body.String())
	}
	if got.Error.Code != wantCode {
		t.Errorf("error.code = %q, want %q", got.Error.Code, wantCode)
	}
	if got.Error.Message == "" {
		t.Error("error.message is empty")
	}
	if got.Error.RetryAfterSeconds != wantRetryAfter {
		t.Errorf("retryAfterSeconds = %d, want %d", got.Error.RetryAfterSeconds, wantRetryAfter)
	}
	if retryHeader := rec.Header().Get("Retry-After"); retryHeader != "" {
		if _, err := strconv.Atoi(retryHeader); err != nil {
			t.Errorf("Retry-After = %q, want integer seconds", retryHeader)
		}
	}
}
