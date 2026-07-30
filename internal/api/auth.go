package api

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"math"
	"net"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

const (
	accessSessionCookie = "vf_access_session"
	defaultSessionTTL   = 12 * time.Hour
	maxSessionTTL       = 24 * time.Hour
	loginFailureWindow  = time.Minute
	maxLoginFailures    = 5
	maxLoginBodyBytes   = 4 << 10
)

type loginFailureState struct {
	windowStartedAt time.Time
	count           int
}

type authCounters struct {
	succeeded atomic.Uint64
	failed    atomic.Uint64
	limited   atomic.Uint64
}

type authenticator struct {
	configured       bool
	passwordDigest   [sha256.Size]byte
	passwordVersion  string
	sessionSecret    []byte
	sessionTTL       time.Duration
	secureCookie     bool
	now              func() time.Time
	failureMu        sync.Mutex
	failuresBySource map[string]loginFailureState
	sessionMu        sync.Mutex
	revokedSessions  map[[sha256.Size]byte]time.Time
	counters         authCounters
}

type sessionClaims struct {
	IssuedAt        int64  `json:"iat"`
	ExpiresAt       int64  `json:"exp"`
	PasswordVersion string `json:"pv"`
	Nonce           string `json:"nonce"`
}

type authErrorEnvelope struct {
	Error authErrorBody `json:"error"`
}

type authErrorBody struct {
	Code              string `json:"code"`
	Message           string `json:"message"`
	RetryAfterSeconds int    `json:"retryAfterSeconds"`
}

type authSessionResponse struct {
	Authenticated bool   `json:"authenticated"`
	ExpiresAt     string `json:"expiresAt"`
}

func newAuthenticatorFromEnv(now func() time.Time) (*authenticator, error) {
	appEnv := strings.ToLower(strings.TrimSpace(os.Getenv("APP_ENV")))
	password := os.Getenv("APP_ACCESS_PASSWORD")
	sessionSecret := os.Getenv("APP_AUTH_SESSION_SECRET")

	a := &authenticator{
		sessionTTL:       defaultSessionTTL,
		secureCookie:     appEnv != "development" && appEnv != "test",
		now:              now,
		failuresBySource: make(map[string]loginFailureState),
		revokedSessions:  make(map[[sha256.Size]byte]time.Time),
	}

	if rawTTL := os.Getenv("APP_AUTH_SESSION_TTL_HOURS"); rawTTL != "" {
		hours, err := strconv.Atoi(rawTTL)
		if err != nil || hours < 1 || hours > int(maxSessionTTL/time.Hour) {
			return nil, errors.New("authentication configuration: APP_AUTH_SESSION_TTL_HOURS must be an integer from 1 through 24")
		}
		a.sessionTTL = time.Duration(hours) * time.Hour
	}

	// Unit tests may intentionally exercise an unavailable-auth health state.
	// Every non-test process fails closed before opening the database or serving.
	if appEnv == "test" && (password == "" || sessionSecret == "") {
		return a, nil
	}
	if password == "" {
		return nil, errors.New("authentication configuration: APP_ACCESS_PASSWORD is required")
	}
	if sessionSecret == "" {
		return nil, errors.New("authentication configuration: APP_AUTH_SESSION_SECRET is required")
	}
	if len([]byte(sessionSecret)) < 32 {
		return nil, errors.New("authentication configuration: APP_AUTH_SESSION_SECRET must contain at least 32 bytes")
	}
	if password == sessionSecret {
		return nil, errors.New("authentication configuration: APP_AUTH_SESSION_SECRET must differ from APP_ACCESS_PASSWORD")
	}

	a.configured = true
	a.passwordDigest = sha256.Sum256([]byte(password))
	a.sessionSecret = []byte(sessionSecret)
	a.passwordVersion = a.derivePasswordVersion(password)
	return a, nil
}

func (a *authenticator) derivePasswordVersion(password string) string {
	mac := hmac.New(sha256.New, a.sessionSecret)
	_, _ = mac.Write([]byte("vibe-forge/password-version/v1\x00"))
	_, _ = mac.Write([]byte(password))
	return base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}

func (a *authenticator) passwordMatches(password string) bool {
	provided := sha256.Sum256([]byte(password))
	return subtle.ConstantTimeCompare(provided[:], a.passwordDigest[:]) == 1
}

func (a *authenticator) createSession() (string, time.Time, error) {
	now := a.now().UTC()
	expiresAt := now.Add(a.sessionTTL)
	var nonce [16]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		return "", time.Time{}, err
	}
	claims := sessionClaims{
		IssuedAt:        now.Unix(),
		ExpiresAt:       expiresAt.Unix(),
		PasswordVersion: a.passwordVersion,
		Nonce:           base64.RawURLEncoding.EncodeToString(nonce[:]),
	}
	payload, err := json.Marshal(claims)
	if err != nil {
		return "", time.Time{}, err
	}
	encodedPayload := base64.RawURLEncoding.EncodeToString(payload)
	signed := "v1." + encodedPayload
	mac := hmac.New(sha256.New, a.sessionSecret)
	_, _ = mac.Write([]byte(signed))
	signature := base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
	return signed + "." + signature, expiresAt, nil
}

func (a *authenticator) validateSession(token string) (time.Time, bool) {
	parts := strings.Split(token, ".")
	if len(parts) != 3 || parts[0] != "v1" {
		return time.Time{}, false
	}

	providedSignature, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil {
		return time.Time{}, false
	}
	mac := hmac.New(sha256.New, a.sessionSecret)
	_, _ = mac.Write([]byte(parts[0] + "." + parts[1]))
	if !hmac.Equal(providedSignature, mac.Sum(nil)) {
		return time.Time{}, false
	}

	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return time.Time{}, false
	}
	var claims sessionClaims
	if err := json.Unmarshal(payload, &claims); err != nil {
		return time.Time{}, false
	}
	nonce, err := base64.RawURLEncoding.DecodeString(claims.Nonce)
	if err != nil || len(nonce) != 16 {
		return time.Time{}, false
	}
	providedVersion := sha256.Sum256([]byte(claims.PasswordVersion))
	expectedVersion := sha256.Sum256([]byte(a.passwordVersion))
	if subtle.ConstantTimeCompare(providedVersion[:], expectedVersion[:]) != 1 {
		return time.Time{}, false
	}

	now := a.now().UTC()
	expiresAt := time.Unix(claims.ExpiresAt, 0).UTC()
	issuedAt := time.Unix(claims.IssuedAt, 0).UTC()
	if claims.IssuedAt <= 0 || claims.ExpiresAt <= claims.IssuedAt ||
		expiresAt.Sub(issuedAt) > maxSessionTTL || !now.Before(expiresAt) {
		return time.Time{}, false
	}
	if a.sessionRevoked(token, now) {
		return time.Time{}, false
	}
	return expiresAt, true
}

func (a *authenticator) sessionRevoked(token string, now time.Time) bool {
	tokenHash := sha256.Sum256([]byte(token))
	a.sessionMu.Lock()
	defer a.sessionMu.Unlock()
	for hash, expiresAt := range a.revokedSessions {
		if !now.Before(expiresAt) {
			delete(a.revokedSessions, hash)
		}
	}
	_, revoked := a.revokedSessions[tokenHash]
	return revoked
}

func (a *authenticator) revokeSession(token string) {
	expiresAt, valid := a.validateSession(token)
	if !valid {
		return
	}
	tokenHash := sha256.Sum256([]byte(token))
	a.sessionMu.Lock()
	a.revokedSessions[tokenHash] = expiresAt
	a.sessionMu.Unlock()
}

func (a *authenticator) sessionCookie(token string, expiresAt time.Time) *http.Cookie {
	return &http.Cookie{
		Name:     accessSessionCookie,
		Value:    token,
		Path:     "/",
		Expires:  expiresAt,
		MaxAge:   int(a.sessionTTL.Seconds()),
		HttpOnly: true,
		Secure:   a.secureCookie,
		SameSite: http.SameSiteLaxMode,
	}
}

func (a *authenticator) expiredSessionCookie() *http.Cookie {
	return &http.Cookie{
		Name:     accessSessionCookie,
		Value:    "",
		Path:     "/",
		Expires:  time.Unix(1, 0).UTC(),
		MaxAge:   -1,
		HttpOnly: true,
		Secure:   a.secureCookie,
		SameSite: http.SameSiteLaxMode,
	}
}

func (a *authenticator) verifyLogin(source, password string, now time.Time) (bool, int) {
	a.failureMu.Lock()
	defer a.failureMu.Unlock()
	a.pruneFailuresLocked(now)

	state, hasFailures := a.failuresBySource[source]
	if hasFailures && state.count >= maxLoginFailures {
		remaining := state.windowStartedAt.Add(loginFailureWindow).Sub(now)
		return false, max(1, int(math.Ceil(remaining.Seconds())))
	}

	if a.passwordMatches(password) {
		delete(a.failuresBySource, source)
		return true, 0
	}
	if !hasFailures {
		state.windowStartedAt = now
	}
	state.count++
	a.failuresBySource[source] = state
	return false, 0
}

func (a *authenticator) pruneFailuresLocked(now time.Time) {
	for source, state := range a.failuresBySource {
		if !now.Before(state.windowStartedAt.Add(loginFailureWindow)) {
			delete(a.failuresBySource, source)
		}
	}
}

func (s *Server) login(w http.ResponseWriter, r *http.Request) {
	if s.auth == nil || !s.auth.configured {
		writeAuthError(w, http.StatusServiceUnavailable, "AUTH_UNAVAILABLE", "认证服务暂时不可用", 0)
		return
	}

	var req struct {
		Password string `json:"password"`
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxLoginBodyBytes)
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(&req); err != nil {
		writeAuthError(w, http.StatusBadRequest, "AUTH_PASSWORD_REQUIRED", "请输入访问密码", 0)
		return
	}
	if err := ensureJSONEOF(dec); err != nil {
		writeAuthError(w, http.StatusBadRequest, "AUTH_PASSWORD_REQUIRED", "请输入访问密码", 0)
		return
	}
	if req.Password == "" {
		writeAuthError(w, http.StatusBadRequest, "AUTH_PASSWORD_REQUIRED", "请输入访问密码", 0)
		return
	}

	now := s.auth.now().UTC()
	source := requestSource(r)
	requestID := requestIDForLog(r)
	passwordMatches, retryAfter := s.auth.verifyLogin(source, req.Password, now)
	if retryAfter > 0 {
		total := s.auth.counters.limited.Add(1)
		s.logf("auth_login outcome=rate_limited total=%d request_id=%q", total, requestID)
		w.Header().Set("Retry-After", strconv.Itoa(retryAfter))
		writeAuthError(w, http.StatusTooManyRequests, "AUTH_RATE_LIMITED", "尝试次数过多，请稍后重试", retryAfter)
		return
	}

	if !passwordMatches {
		total := s.auth.counters.failed.Add(1)
		s.logf("auth_login outcome=invalid total=%d request_id=%q", total, requestID)
		writeAuthError(w, http.StatusUnauthorized, "AUTH_INVALID", "密码错误，请重试", 0)
		return
	}

	token, expiresAt, err := s.auth.createSession()
	if err != nil {
		writeAuthError(w, http.StatusServiceUnavailable, "AUTH_UNAVAILABLE", "认证服务暂时不可用", 0)
		return
	}
	http.SetCookie(w, s.auth.sessionCookie(token, expiresAt))
	total := s.auth.counters.succeeded.Add(1)
	s.logf("auth_login outcome=succeeded total=%d request_id=%q", total, requestID)
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) authSession(w http.ResponseWriter, r *http.Request) {
	if s.auth == nil || !s.auth.configured {
		writeAuthError(w, http.StatusServiceUnavailable, "AUTH_UNAVAILABLE", "认证服务暂时不可用", 0)
		return
	}
	cookie, err := r.Cookie(accessSessionCookie)
	if err != nil {
		writeAuthError(w, http.StatusUnauthorized, "AUTH_REQUIRED", "请先完成访问验证", 0)
		return
	}
	expiresAt, valid := s.auth.validateSession(cookie.Value)
	if !valid {
		http.SetCookie(w, s.auth.expiredSessionCookie())
		writeAuthError(w, http.StatusUnauthorized, "AUTH_REQUIRED", "访问已过期，请重新验证", 0)
		return
	}
	writeJSON(w, http.StatusOK, authSessionResponse{
		Authenticated: true,
		ExpiresAt:     expiresAt.Format(time.RFC3339),
	})
}

func (s *Server) logout(w http.ResponseWriter, r *http.Request) {
	if s.auth != nil && s.auth.configured {
		if cookie, err := r.Cookie(accessSessionCookie); err == nil {
			s.auth.revokeSession(cookie.Value)
		}
		http.SetCookie(w, s.auth.expiredSessionCookie())
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) requireAuthentication(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if authPublicPath(r.URL.Path) {
			next.ServeHTTP(w, r)
			return
		}
		if s.auth == nil || !s.auth.configured {
			writeAuthError(w, http.StatusServiceUnavailable, "AUTH_UNAVAILABLE", "认证服务暂时不可用", 0)
			return
		}
		cookie, err := r.Cookie(accessSessionCookie)
		if err != nil {
			writeAuthError(w, http.StatusUnauthorized, "AUTH_REQUIRED", "请先完成访问验证", 0)
			return
		}
		if _, valid := s.auth.validateSession(cookie.Value); !valid {
			http.SetCookie(w, s.auth.expiredSessionCookie())
			writeAuthError(w, http.StatusUnauthorized, "AUTH_REQUIRED", "访问已过期，请重新验证", 0)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func authPublicPath(path string) bool {
	switch path {
	case "/api/health", "/api/auth/login", "/api/auth/session", "/api/auth/logout":
		return true
	default:
		return false
	}
}

func writeAuthError(w http.ResponseWriter, status int, code, message string, retryAfter int) {
	writeJSON(w, status, authErrorEnvelope{Error: authErrorBody{
		Code:              code,
		Message:           message,
		RetryAfterSeconds: retryAfter,
	}})
}

func ensureJSONEOF(dec *json.Decoder) error {
	var extra any
	err := dec.Decode(&extra)
	if errors.Is(err, io.EOF) {
		return nil
	}
	if err == nil {
		return errors.New("multiple JSON values are not allowed")
	}
	return err
}

func requestSource(r *http.Request) string {
	remoteHost := r.RemoteAddr
	if host, _, err := net.SplitHostPort(r.RemoteAddr); err == nil {
		remoteHost = host
	}
	remoteIP := net.ParseIP(remoteHost)
	if remoteIP != nil && (remoteIP.IsLoopback() || remoteIP.IsPrivate()) {
		forwarded := strings.Split(r.Header.Get("X-Forwarded-For"), ",")
		for i := len(forwarded) - 1; i >= 0; i-- {
			if ip := net.ParseIP(strings.TrimSpace(forwarded[i])); ip != nil {
				return ip.String()
			}
		}
		if ip := net.ParseIP(strings.TrimSpace(r.Header.Get("X-Real-IP"))); ip != nil {
			return ip.String()
		}
	}
	if remoteIP != nil {
		return remoteIP.String()
	}
	if remoteHost != "" {
		return remoteHost
	}
	return "unknown"
}

func requestIDForLog(r *http.Request) string {
	requestID := r.Header.Get("X-Request-ID")
	if len(requestID) > 128 {
		requestID = requestID[:128]
	}
	return requestID
}
