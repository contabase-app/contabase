package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestAllowedHostsAcceptConfiguredHost(t *testing.T) {
	cfg := newHTTPBoundaryConfig("app.example.com", "")

	if !cfg.hostAllowed("APP.EXAMPLE.COM:8443") {
		t.Fatalf("expected configured host with port to be allowed")
	}
}

func TestAllowedHostsRejectUnknownHost(t *testing.T) {
	cfg := newHTTPBoundaryConfig("app.example.com", "")
	handler := cfg.enforceRemoteHTTPS(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	req := httptest.NewRequest(http.MethodGet, "https://evil.example/", nil)
	req.RemoteAddr = "203.0.113.10:44123"
	rr := httptest.NewRecorder()

	handler.ServeHTTP(rr, req)

	if rr.Code != http.StatusMisdirectedRequest {
		t.Fatalf("expected status %d, got %d", http.StatusMisdirectedRequest, rr.Code)
	}
}

func TestAllowedHostsPermitLocalhostDev(t *testing.T) {
	cfg := newHTTPBoundaryConfig("", "")
	handler := cfg.enforceRemoteHTTPS(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	req := httptest.NewRequest(http.MethodGet, "http://localhost:8080/", nil)
	req.RemoteAddr = "127.0.0.1:44123"
	rr := httptest.NewRecorder()

	handler.ServeHTTP(rr, req)

	if rr.Code != http.StatusNoContent {
		t.Fatalf("expected localhost request to pass, got %d", rr.Code)
	}
}

func TestForwardedProtoIgnoredFromUntrustedRemote(t *testing.T) {
	cfg := newHTTPBoundaryConfig("app.example.com", "10.0.0.0/8")
	handler := cfg.enforceRemoteHTTPS(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	req := httptest.NewRequest(http.MethodGet, "http://app.example.com/", nil)
	req.RemoteAddr = "203.0.113.10:44123"
	req.Header.Set("X-Forwarded-Proto", "https")
	rr := httptest.NewRecorder()

	handler.ServeHTTP(rr, req)

	if rr.Code != http.StatusUpgradeRequired {
		t.Fatalf("expected untrusted forwarded proto to be blocked with 426, got %d", rr.Code)
	}
}

func TestForwardedProtoAcceptedFromTrustedProxy(t *testing.T) {
	cfg := newHTTPBoundaryConfig("app.example.com", "10.0.0.0/8")
	handler := cfg.enforceRemoteHTTPS(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	req := httptest.NewRequest(http.MethodGet, "http://app.example.com/", nil)
	req.RemoteAddr = "10.1.2.3:44123"
	req.Header.Set("X-Forwarded-Proto", "https")
	rr := httptest.NewRecorder()

	handler.ServeHTTP(rr, req)

	if rr.Code != http.StatusNoContent {
		t.Fatalf("expected trusted forwarded proto to pass, got %d", rr.Code)
	}
}

func TestForwardedForIgnoredFromUntrustedRemote(t *testing.T) {
	cfg := newHTTPBoundaryConfig("app.example.com", "10.0.0.0/8")
	req := httptest.NewRequest(http.MethodGet, "http://app.example.com/", nil)
	req.RemoteAddr = "203.0.113.10:44123"
	req.Header.Set("X-Forwarded-For", "198.51.100.20")

	if got := cfg.clientIP(req); got != "203.0.113.10" {
		t.Fatalf("expected direct remote IP, got %q", got)
	}
}

func TestClientIPRespectsTrustedProxy(t *testing.T) {
	cfg := newHTTPBoundaryConfig("app.example.com", "10.0.0.0/8")
	req := httptest.NewRequest(http.MethodGet, "http://app.example.com/", nil)
	req.RemoteAddr = "10.1.2.3:44123"
	req.Header.Set("X-Forwarded-For", "198.51.100.20, 10.1.2.3")

	if got := cfg.clientIP(req); got != "198.51.100.20" {
		t.Fatalf("expected forwarded client IP, got %q", got)
	}
}

func TestClientIPUsesRemoteAddrWithoutTrustedProxy(t *testing.T) {
	cfg := newHTTPBoundaryConfig("app.example.com", "10.0.0.0/8")
	req := httptest.NewRequest(http.MethodGet, "http://app.example.com/", nil)
	req.RemoteAddr = "203.0.113.10:44123"

	if got := cfg.clientIP(req); got != "203.0.113.10" {
		t.Fatalf("expected remote addr IP, got %q", got)
	}
}

func TestClientIPUsesFirstForwardedForFromTrustedProxy(t *testing.T) {
	cfg := newHTTPBoundaryConfig("app.example.com", "10.0.0.0/8")
	req := httptest.NewRequest(http.MethodGet, "http://app.example.com/", nil)
	req.RemoteAddr = "10.1.2.3:44123"
	req.Header.Set("X-Forwarded-For", " 198.51.100.20, 198.51.100.21")

	if got := cfg.clientIP(req); got != "198.51.100.20" {
		t.Fatalf("expected first forwarded IP, got %q", got)
	}
}

func TestClientIPFallsBackOnInvalidForwardedHeaders(t *testing.T) {
	cfg := newHTTPBoundaryConfig("app.example.com", "10.0.0.0/8")
	req := httptest.NewRequest(http.MethodGet, "http://app.example.com/", nil)
	req.RemoteAddr = "10.1.2.3:44123"
	req.Header.Set("X-Forwarded-For", "not-an-ip")
	req.Header.Set("X-Real-IP", "also-not-an-ip")
	req.Header.Set("CF-Connecting-IP", "still-not-an-ip")

	if got := cfg.clientIP(req); got != "10.1.2.3" {
		t.Fatalf("expected trusted proxy remote IP fallback, got %q", got)
	}
}

func TestClientIPSupportsRealIPAndCloudflareHeadersFromTrustedProxy(t *testing.T) {
	cfg := newHTTPBoundaryConfig("app.example.com", "10.0.0.0/8")
	req := httptest.NewRequest(http.MethodGet, "http://app.example.com/", nil)
	req.RemoteAddr = "10.1.2.3:44123"
	req.Header.Set("X-Real-IP", "198.51.100.30")

	if got := cfg.clientIP(req); got != "198.51.100.30" {
		t.Fatalf("expected x-real-ip client IP, got %q", got)
	}

	req.Header.Del("X-Real-IP")
	req.Header.Set("CF-Connecting-IP", "198.51.100.31")
	if got := cfg.clientIP(req); got != "198.51.100.31" {
		t.Fatalf("expected cloudflare client IP, got %q", got)
	}
}

func TestClientIPIgnoresRealIPAndCloudflareHeadersFromUntrustedRemote(t *testing.T) {
	cfg := newHTTPBoundaryConfig("app.example.com", "10.0.0.0/8")
	req := httptest.NewRequest(http.MethodGet, "http://app.example.com/", nil)
	req.RemoteAddr = "203.0.113.10:44123"
	req.Header.Set("X-Real-IP", "198.51.100.30")
	req.Header.Set("CF-Connecting-IP", "198.51.100.31")

	if got := cfg.clientIP(req); got != "203.0.113.10" {
		t.Fatalf("expected direct remote IP, got %q", got)
	}
}

func TestMemoryRateLimiterBlocksAfterLimit(t *testing.T) {
	limiter := newMemoryRateLimiter()
	policy := rateLimitPolicy{Limit: 2, Window: time.Minute}
	now := time.Unix(100, 0)

	if decision := limiter.Allow("login:ip:203.0.113.10", policy, now); !decision.Allowed {
		t.Fatalf("expected first request to be allowed")
	}
	if decision := limiter.Allow("login:ip:203.0.113.10", policy, now.Add(time.Second)); !decision.Allowed {
		t.Fatalf("expected second request to be allowed")
	}
	decision := limiter.Allow("login:ip:203.0.113.10", policy, now.Add(2*time.Second))
	if decision.Allowed {
		t.Fatalf("expected third request to be blocked")
	}
	if decision.RetryAfter <= 0 {
		t.Fatalf("expected positive retry-after, got %s", decision.RetryAfter)
	}
}

func TestMemoryRateLimiterReleasesAfterWindow(t *testing.T) {
	limiter := newMemoryRateLimiter()
	policy := rateLimitPolicy{Limit: 1, Window: time.Minute}
	now := time.Unix(100, 0)

	if decision := limiter.Allow("activation:ip:203.0.113.10", policy, now); !decision.Allowed {
		t.Fatalf("expected first request to be allowed")
	}
	if decision := limiter.Allow("activation:ip:203.0.113.10", policy, now.Add(30*time.Second)); decision.Allowed {
		t.Fatalf("expected request inside window to be blocked")
	}
	if decision := limiter.Allow("activation:ip:203.0.113.10", policy, now.Add(time.Minute)); !decision.Allowed {
		t.Fatalf("expected request at reset to be allowed")
	}
}

func TestWriteRateLimitExceededIncludesRetryAfter(t *testing.T) {
	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/login", nil)

	writeRateLimitExceeded(rr, req, rateLimitDecision{RetryAfter: 90 * time.Second})

	if rr.Code != http.StatusTooManyRequests {
		t.Fatalf("expected status %d, got %d", http.StatusTooManyRequests, rr.Code)
	}
	assertHeader(t, rr, "Retry-After", "90")
	assertHeader(t, rr, "Cache-Control", "no-store")
	assertHeader(t, rr, "Content-Type", "text/html; charset=utf-8")
}

func TestAllowRateLimitedRequestReturns429AfterExcess(t *testing.T) {
	limiter := newMemoryRateLimiter()
	policy := rateLimitPolicy{Limit: 1, Window: time.Minute}
	now := time.Unix(100, 0)
	req := httptest.NewRequest(http.MethodPost, "/login/2fa", nil)

	first := httptest.NewRecorder()
	if !allowRateLimitedRequest(first, req, limiter, "auth:2fa:ip:203.0.113.10", policy, now) {
		t.Fatalf("expected first 2FA request to be allowed")
	}

	second := httptest.NewRecorder()
	if allowRateLimitedRequest(second, req, limiter, "auth:2fa:ip:203.0.113.10", policy, now.Add(time.Second)) {
		t.Fatalf("expected second 2FA request to be blocked")
	}
	if second.Code != http.StatusTooManyRequests {
		t.Fatalf("expected status %d, got %d", http.StatusTooManyRequests, second.Code)
	}
	if got := second.Header().Get("Retry-After"); got == "" {
		t.Fatalf("expected Retry-After header")
	}
}

func TestRateLimitKeysDoNotCollideAcrossSensitiveFamilies(t *testing.T) {
	limiter := newMemoryRateLimiter()
	policy := rateLimitPolicy{Limit: 1, Window: time.Minute}
	now := time.Unix(100, 0)

	if decision := limiter.Allow("admin:backup:export:user:u1:ip:203.0.113.10", policy, now); !decision.Allowed {
		t.Fatalf("expected admin backup key to be allowed")
	}
	if decision := limiter.Allow("admin_backup_import:user:u1:ip:203.0.113.10", policy, now); !decision.Allowed {
		t.Fatalf("expected admin backup import key to be independent")
	}
	if decision := limiter.Allow("financial:bulk:workspace:w1:user:u1:route:/transacoes/bulk/delete", policy, now); !decision.Allowed {
		t.Fatalf("expected financial bulk key to be independent")
	}
	if decision := limiter.Allow("admin:backup:export:user:u1:ip:203.0.113.10", policy, now.Add(time.Second)); decision.Allowed {
		t.Fatalf("expected repeated admin backup key to be blocked")
	}
}

func TestRateLimitKeyHashDoesNotExposeToken(t *testing.T) {
	token := "pre-auth-token-sensitive"

	first := rateLimitKeyHash(token)
	second := rateLimitKeyHash(token)

	if first == "" || len(first) != 64 {
		t.Fatalf("expected sha256 hex hash, got %q", first)
	}
	if first != second {
		t.Fatalf("expected stable hash")
	}
	if first == token {
		t.Fatalf("expected hash not to expose raw token")
	}
}

func TestRateLimitBlockLogKeyHashDoesNotExposeEmail(t *testing.T) {
	email := "user@example.com"
	emailKey := "auth:login:email:" + email

	hash := rateLimitKeyHash(emailKey)

	if len(hash) != 64 {
		t.Fatalf("expected sha256 hex hash, got %q", hash)
	}
	if strings.Contains(hash, email) || strings.Contains(hash, strings.ToLower(email)) {
		t.Fatalf("hash must not contain raw email: %q", hash)
	}

	// Verify different emails produce different hashes
	hash2 := rateLimitKeyHash("auth:login:email:other@example.com")
	if hash == hash2 {
		t.Fatalf("expected different emails to produce different hashes")
	}
}

func TestRateLimitBlockLogFamilyExtraction(t *testing.T) {
	// Verify that the key prefix serves as reliable family identifier.
	// These match the prefixes used in key construction in main.go.
	keys := []struct {
		key    string
		family string
	}{
		{"auth:login:ip:203.0.113.10", "auth"},
		{"auth:2fa:ip:203.0.113.10", "auth"},
		{"auth:bootstrap:setup:ip:127.0.0.1", "auth"},
		{"admin:backup:export:user:u1:ip:203.0.113.10", "admin"},
		{"admin:identity:users-save:user:u1:ip:203.0.113.10", "admin"},
		{"admin:debug:seed-demo-b2b:workspace:w1:user:u1:route:/admin/seed-demo-b2b", "admin"},
		{"admin_backup_import:user:u1:ip:203.0.113.10", "admin_backup_import"},
		{"financial:bulk:workspace:w1:user:u1:route:/transacoes/bulk/delete", "financial"},
		{"security:totp:setup:user:u1:ip:203.0.113.10", "security"},
	}
	for _, tc := range keys {
		family := tc.key
		if idx := strings.Index(tc.key, ":"); idx >= 0 {
			family = tc.key[:idx]
		}
		if family != tc.family {
			t.Fatalf("key=%q expected family=%q got=%q", tc.key, tc.family, family)
		}
	}
}

func TestSecurityHeadersApplied(t *testing.T) {
	cfg := newHTTPBoundaryConfig("app.example.com", "10.0.0.0/8")
	handler := cfg.securityHeaders(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	req := httptest.NewRequest(http.MethodGet, "http://app.example.com/", nil)
	req.RemoteAddr = "10.1.2.3:44123"
	req.Header.Set("X-Forwarded-Proto", "https")
	rr := httptest.NewRecorder()

	handler.ServeHTTP(rr, req)

	assertHeader(t, rr, "X-Content-Type-Options", "nosniff")
	assertHeader(t, rr, "Referrer-Policy", "strict-origin-when-cross-origin")
	assertHeader(t, rr, "X-Frame-Options", "DENY")
	assertHeader(t, rr, "Permissions-Policy", "camera=(), microphone=(), geolocation=(), payment=(), usb=()")
	assertHeader(t, rr, "Content-Security-Policy-Report-Only", cspReportOnlyPolicy)
	assertHeader(t, rr, "Strict-Transport-Security", "max-age=31536000; includeSubDomains")
	assertHeader(t, rr, "Content-Security-Policy", cspEnforcingPolicy)
}

func assertHeader(t *testing.T, rr *httptest.ResponseRecorder, name, want string) {
	t.Helper()
	if got := rr.Header().Get(name); got != want {
		t.Fatalf("expected %s=%q, got %q", name, want, got)
	}
}
