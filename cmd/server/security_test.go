package main

import (
	"crypto/sha256"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
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

func TestAllowedHostsPermitLoopbackHTTP(t *testing.T) {
	cfg := newHTTPBoundaryConfig("", "")
	handler := cfg.enforceRemoteHTTPS(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))

	tests := []struct {
		name       string
		targetURL  string
		remoteAddr string
	}{
		{
			name:       "ipv4",
			targetURL:  "http://127.0.0.1:8080/",
			remoteAddr: "127.0.0.1:44123",
		},
		{
			name:       "ipv6",
			targetURL:  "http://[::1]:8080/",
			remoteAddr: "[::1]:44123",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, tc.targetURL, nil)
			req.RemoteAddr = tc.remoteAddr
			rr := httptest.NewRecorder()

			handler.ServeHTTP(rr, req)

			if rr.Code != http.StatusNoContent {
				t.Fatalf("expected loopback request to pass, got %d", rr.Code)
			}
		})
	}
}

func TestLocalhostHostFromLANRemoteIsBlocked(t *testing.T) {
	cfg := newHTTPBoundaryConfig("", "")
	handler := cfg.enforceRemoteHTTPS(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	req := httptest.NewRequest(http.MethodGet, "http://localhost:8080/", nil)
	req.RemoteAddr = "192.168.80.50:44123"
	rr := httptest.NewRecorder()

	handler.ServeHTTP(rr, req)

	if rr.Code != http.StatusUpgradeRequired {
		t.Fatalf("expected remote request spoofing localhost Host to be blocked with 426, got %d", rr.Code)
	}
}

func TestAccessDockerLocalHTTPAllowedWithExplicitMode(t *testing.T) {
	cfg := newHTTPBoundaryConfigWithAccess("localhost,127.0.0.1,::1", "", "local-docker", "http://localhost:8080")
	handler := cfg.enforceRemoteHTTPS(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))

	tests := []struct {
		name      string
		targetURL string
	}{
		{name: "localhost", targetURL: "http://localhost:8080/"},
		{name: "ipv4 loopback host", targetURL: "http://127.0.0.1:8080/"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, tc.targetURL, nil)
			req.RemoteAddr = "172.17.0.1:44123"
			rr := httptest.NewRecorder()

			handler.ServeHTTP(rr, req)

			if rr.Code != http.StatusNoContent {
				t.Fatalf("expected Docker local request to pass, got %d", rr.Code)
			}
		})
	}
}

func TestAccessDockerLocalHTTPBlockedForPublicRemote(t *testing.T) {
	cfg := newHTTPBoundaryConfigWithAccess("localhost,127.0.0.1", "", "local-docker", "http://localhost:8080")
	handler := cfg.enforceRemoteHTTPS(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	req := httptest.NewRequest(http.MethodGet, "http://localhost:8080/", nil)
	req.RemoteAddr = "198.51.100.20:44123"
	rr := httptest.NewRecorder()

	handler.ServeHTTP(rr, req)

	if rr.Code != http.StatusUpgradeRequired {
		t.Fatalf("expected Docker local request from public remote to be blocked with 426, got %d", rr.Code)
	}
}

func TestAccessDockerLocalHTTPBlockedWithPublicBaseURL(t *testing.T) {
	cfg := newHTTPBoundaryConfigWithAccess("localhost,127.0.0.1,app.example.com", "", "local-docker", "https://app.example.com")
	handler := cfg.enforceRemoteHTTPS(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	req := httptest.NewRequest(http.MethodGet, "http://localhost:8080/", nil)
	req.RemoteAddr = "172.17.0.1:44123"
	rr := httptest.NewRecorder()

	handler.ServeHTTP(rr, req)

	if rr.Code != http.StatusUpgradeRequired {
		t.Fatalf("expected Docker local request with public APP_BASE_URL to be blocked with 426, got %d", rr.Code)
	}
}

func TestAccessDockerLocalHTTPBlockedWithLANBaseURL(t *testing.T) {
	cfg := newHTTPBoundaryConfigWithAccess("localhost,127.0.0.1", "", "local-docker", "http://192.168.80.27:8080")
	handler := cfg.enforceRemoteHTTPS(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	req := httptest.NewRequest(http.MethodGet, "http://localhost:8080/", nil)
	req.RemoteAddr = "172.17.0.1:44123"
	rr := httptest.NewRecorder()

	handler.ServeHTTP(rr, req)

	if rr.Code != http.StatusUpgradeRequired {
		t.Fatalf("expected Docker local request with LAN APP_BASE_URL to be blocked with 426, got %d", rr.Code)
	}
}

func TestAccessDockerLocalHTTPBlockedWithPublicAllowedHost(t *testing.T) {
	cfg := newHTTPBoundaryConfigWithAccess("localhost,127.0.0.1,app.example.com", "", "local-docker", "http://localhost:8080")
	handler := cfg.enforceRemoteHTTPS(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	req := httptest.NewRequest(http.MethodGet, "http://localhost:8080/", nil)
	req.RemoteAddr = "172.17.0.1:44123"
	rr := httptest.NewRecorder()

	handler.ServeHTTP(rr, req)

	if rr.Code != http.StatusUpgradeRequired {
		t.Fatalf("expected Docker local request with public ALLOWED_HOSTS to be blocked with 426, got %d", rr.Code)
	}
}

func TestHTTPBoundaryMiddlewareBlocksNonHTTPS(t *testing.T) {
	cfg := newHTTPBoundaryConfigWithAccess("app.example.com", "", "proxy", "https://app.example.com")
	mux := http.NewServeMux()
	mux.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})
	handler := cfg.enforceRemoteHTTPS(mux)
	req := httptest.NewRequest(http.MethodGet, "http://app.example.com/health", nil)
	req.RemoteAddr = "203.0.113.10:44123"
	rr := httptest.NewRecorder()

	handler.ServeHTTP(rr, req)

	if rr.Code != http.StatusUpgradeRequired {
		t.Fatalf("expected /health to follow HTTP boundary with 426, got %d", rr.Code)
	}
}

func TestPrivateLANHTTPAllowedWithExplicitLANMode(t *testing.T) {
	cfg := newHTTPBoundaryConfigWithAccess("localhost,127.0.0.1,192.168.80.27", "", "lan", "http://192.168.80.27:8080")
	handler := cfg.enforceRemoteHTTPS(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	req := httptest.NewRequest(http.MethodGet, "http://192.168.80.27:8080/", nil)
	req.RemoteAddr = "192.168.80.50:44123"
	rr := httptest.NewRecorder()

	handler.ServeHTTP(rr, req)

	if rr.Code != http.StatusNoContent {
		t.Fatalf("expected private LAN HTTP request to pass in lan mode, got %d", rr.Code)
	}
}

func TestPrivateLANHTTPAllowedFromEnvironment(t *testing.T) {
	t.Setenv("APP_BASE_URL", "http://192.168.80.27:8080")
	t.Setenv("ALLOWED_HOSTS", "localhost,127.0.0.1,192.168.80.27")
	t.Setenv("TRUSTED_PROXIES", "")
	t.Setenv("CONTABASE_ACCESS_MODE", "lan")

	handler := enforceRemoteHTTPS(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	req := httptest.NewRequest(http.MethodGet, "http://192.168.80.27:8080/", nil)
	req.RemoteAddr = "192.168.80.50:44123"
	rr := httptest.NewRecorder()

	handler.ServeHTTP(rr, req)

	if rr.Code != http.StatusNoContent {
		t.Fatalf("expected env-configured private LAN HTTP request to pass, got %d", rr.Code)
	}
}

func TestSecureCookieDisabledForExplicitLANHTTP(t *testing.T) {
	t.Setenv("APP_BASE_URL", "http://192.168.80.27:8080")
	t.Setenv("ALLOWED_HOSTS", "localhost,127.0.0.1,192.168.80.27")
	t.Setenv("TRUSTED_PROXIES", "")
	t.Setenv("CONTABASE_ACCESS_MODE", "lan")
	req := httptest.NewRequest(http.MethodGet, "http://192.168.80.27:8080/", nil)
	req.RemoteAddr = "192.168.80.50:44123"

	if shouldUseSecureCookie(req) {
		t.Fatalf("expected LAN HTTP request allowed by explicit lan mode to use non-secure cookies")
	}
}

func TestSecureCookieEnabledForTrustedProxyHTTPS(t *testing.T) {
	t.Setenv("APP_BASE_URL", "https://app.example.com")
	t.Setenv("ALLOWED_HOSTS", "app.example.com,localhost,127.0.0.1")
	t.Setenv("TRUSTED_PROXIES", "10.0.0.0/8")
	t.Setenv("CONTABASE_ACCESS_MODE", "proxy")
	req := httptest.NewRequest(http.MethodGet, "http://app.example.com/", nil)
	req.RemoteAddr = "10.1.2.3:44123"
	req.Header.Set("X-Forwarded-Proto", "https")

	if !shouldUseSecureCookie(req) {
		t.Fatalf("expected HTTPS request from trusted proxy to use secure cookies")
	}
}

func TestPrivateLANHTTPBlockedWithoutExplicitLANMode(t *testing.T) {
	cfg := newHTTPBoundaryConfigWithAccess("localhost,127.0.0.1,192.168.80.27", "", "", "http://192.168.80.27:8080")
	handler := cfg.enforceRemoteHTTPS(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	req := httptest.NewRequest(http.MethodGet, "http://192.168.80.27:8080/", nil)
	req.RemoteAddr = "192.168.80.50:44123"
	rr := httptest.NewRecorder()

	handler.ServeHTTP(rr, req)

	if rr.Code != http.StatusUpgradeRequired {
		t.Fatalf("expected private LAN HTTP request without lan mode to be blocked with 426, got %d", rr.Code)
	}
}

func TestPrivateLANHTTPBlockedWhenHostDiffersFromBaseURL(t *testing.T) {
	cfg := newHTTPBoundaryConfigWithAccess("localhost,127.0.0.1,192.168.80.27,192.168.80.28", "", "lan", "http://192.168.80.27:8080")
	handler := cfg.enforceRemoteHTTPS(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	req := httptest.NewRequest(http.MethodGet, "http://192.168.80.28:8080/", nil)
	req.RemoteAddr = "192.168.80.50:44123"
	rr := httptest.NewRecorder()

	handler.ServeHTTP(rr, req)

	if rr.Code != http.StatusUpgradeRequired {
		t.Fatalf("expected LAN HTTP host outside APP_BASE_URL to be blocked with 426, got %d", rr.Code)
	}
}

func TestPrivateLANHTTPBlockedWhenHostNotAllowed(t *testing.T) {
	cfg := newHTTPBoundaryConfigWithAccess("localhost,127.0.0.1,192.168.80.27", "", "lan", "http://192.168.80.27:8080")
	handler := cfg.enforceRemoteHTTPS(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	req := httptest.NewRequest(http.MethodGet, "http://192.168.80.28:8080/", nil)
	req.RemoteAddr = "192.168.80.50:44123"
	rr := httptest.NewRecorder()

	handler.ServeHTTP(rr, req)

	if rr.Code != http.StatusMisdirectedRequest {
		t.Fatalf("expected LAN HTTP host outside ALLOWED_HOSTS to be blocked with 421, got %d", rr.Code)
	}
}

func TestLANHTTPDoesNotTreatLocalhostAsLANHost(t *testing.T) {
	cfg := newHTTPBoundaryConfigWithAccess("localhost,127.0.0.1", "", "lan", "http://localhost:8080")
	handler := cfg.enforceRemoteHTTPS(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	req := httptest.NewRequest(http.MethodGet, "http://localhost:8080/", nil)
	req.RemoteAddr = "192.168.80.50:44123"
	rr := httptest.NewRecorder()

	handler.ServeHTTP(rr, req)

	if rr.Code != http.StatusUpgradeRequired {
		t.Fatalf("expected LAN mode with localhost host to be blocked with 426, got %d", rr.Code)
	}
}

func TestPublicIPHTTPBlockedEvenWithLANMode(t *testing.T) {
	cfg := newHTTPBoundaryConfigWithAccess("203.0.113.10", "", "lan", "http://203.0.113.10:8080")
	handler := cfg.enforceRemoteHTTPS(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	req := httptest.NewRequest(http.MethodGet, "http://203.0.113.10:8080/", nil)
	req.RemoteAddr = "198.51.100.20:44123"
	rr := httptest.NewRecorder()

	handler.ServeHTTP(rr, req)

	if rr.Code != http.StatusUpgradeRequired {
		t.Fatalf("expected public IP HTTP request to be blocked with 426, got %d", rr.Code)
	}
}

func TestHTTPSViaTrustedProxyAllowedInProxyMode(t *testing.T) {
	cfg := newHTTPBoundaryConfigWithAccess("app.example.com", "10.0.0.0/8", "proxy", "https://app.example.com")
	handler := cfg.enforceRemoteHTTPS(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	req := httptest.NewRequest(http.MethodGet, "http://app.example.com/", nil)
	req.RemoteAddr = "10.1.2.3:44123"
	req.Header.Set("X-Forwarded-Proto", "https")
	rr := httptest.NewRecorder()

	handler.ServeHTTP(rr, req)

	if rr.Code != http.StatusNoContent {
		t.Fatalf("expected HTTPS request from trusted proxy to pass, got %d", rr.Code)
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

func TestClientIPUsesRightmostUntrustedForwardedForFromTrustedProxy(t *testing.T) {
	cfg := newHTTPBoundaryConfig("app.example.com", "10.0.0.0/8")
	req := httptest.NewRequest(http.MethodGet, "http://app.example.com/", nil)
	req.RemoteAddr = "10.1.2.3:44123"
	req.Header.Set("X-Forwarded-For", " 198.51.100.20, 198.51.100.21")

	if got := cfg.clientIP(req); got != "198.51.100.21" {
		t.Fatalf("expected rightmost forwarded IP, got %q", got)
	}
}

func TestClientIPIgnoresSpoofedLeftmostForwardedForFromTrustedProxy(t *testing.T) {
	cfg := newHTTPBoundaryConfig("app.example.com", "10.0.0.0/8")
	req := httptest.NewRequest(http.MethodGet, "http://app.example.com/", nil)
	req.RemoteAddr = "10.1.2.3:44123"
	// A client-supplied value is retained on the left while the proxy appends
	// the peer it actually observed on the right.
	req.Header.Set("X-Forwarded-For", "203.0.113.250, 198.51.100.21")

	if got := cfg.clientIP(req); got != "198.51.100.21" {
		t.Fatalf("spoofed leftmost XFF selected as client IP: %q", got)
	}
}

func TestClientIPSkipsTrustedProxyHopsFromRight(t *testing.T) {
	cfg := newHTTPBoundaryConfig("app.example.com", "10.0.0.0/8")
	req := httptest.NewRequest(http.MethodGet, "http://app.example.com/", nil)
	req.RemoteAddr = "10.1.2.3:44123"
	req.Header.Set("X-Forwarded-For", "198.51.100.21, 10.2.3.4")

	if got := cfg.clientIP(req); got != "198.51.100.21" {
		t.Fatalf("expected client before trusted proxy chain, got %q", got)
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

func TestClientIPDoesNotFallbackToRealIPOrCloudflareHeaders(t *testing.T) {
	cfg := newHTTPBoundaryConfig("app.example.com", "10.0.0.0/8")
	req := httptest.NewRequest(http.MethodGet, "http://app.example.com/", nil)
	req.RemoteAddr = "10.1.2.3:44123"
	req.Header.Set("X-Real-IP", "198.51.100.30")

	if got := cfg.clientIP(req); got != "10.1.2.3" {
		t.Fatalf("X-Real-IP must not become client IP, got %q", got)
	}

	req.Header.Del("X-Real-IP")
	req.Header.Set("CF-Connecting-IP", "198.51.100.31")
	if got := cfg.clientIP(req); got != "10.1.2.3" {
		t.Fatalf("CF-Connecting-IP must not become client IP, got %q", got)
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

func TestMemoryRateLimiterBoundsBucketsAndFailsClosed(t *testing.T) {
	limiter := newMemoryRateLimiterWithCapacity(2)
	policy := rateLimitPolicy{Limit: 2, Window: time.Minute}
	now := time.Unix(100, 0)

	if decision := limiter.Allow("auth:login:ip:203.0.113.10", policy, now); !decision.Allowed {
		t.Fatal("expected first identity to be allowed")
	}
	if decision := limiter.Allow("auth:login:ip:203.0.113.11", policy, now); !decision.Allowed {
		t.Fatal("expected second identity to be allowed")
	}
	decision := limiter.Allow("auth:login:ip:203.0.113.12", policy, now)
	if decision.Allowed {
		t.Fatal("expected unseen identity to be blocked at capacity")
	}
	if decision.RetryAfter <= 0 {
		t.Fatalf("expected positive retry-after at capacity, got %s", decision.RetryAfter)
	}
	if got := len(limiter.buckets); got != 2 {
		t.Fatalf("bucket count = %d, want 2", got)
	}

	if decision := limiter.Allow("auth:login:ip:203.0.113.10", policy, now.Add(time.Second)); !decision.Allowed {
		t.Fatal("existing identity must keep its bucket at capacity")
	}
}

func TestMemoryRateLimiterReclaimsExpiredBucketAtCapacity(t *testing.T) {
	limiter := newMemoryRateLimiterWithCapacity(1)
	policy := rateLimitPolicy{Limit: 1, Window: time.Minute}
	now := time.Unix(100, 0)

	if decision := limiter.Allow("auth:login:email:first@example.test", policy, now); !decision.Allowed {
		t.Fatal("expected first identity to be allowed")
	}
	if decision := limiter.Allow("auth:login:email:second@example.test", policy, now.Add(time.Minute)); !decision.Allowed {
		t.Fatal("expected expired bucket to be reclaimed for a new identity")
	}
	if got := len(limiter.buckets); got != 1 {
		t.Fatalf("bucket count = %d, want 1", got)
	}
	if _, exists := limiter.buckets[rateLimitKeyHash("auth:login:email:first@example.test")]; exists {
		t.Fatal("expired bucket remained after capacity cleanup")
	}
}

func TestMemoryRateLimiterStoresFixedLengthHashedKeys(t *testing.T) {
	limiter := newMemoryRateLimiterWithCapacity(1)
	policy := rateLimitPolicy{Limit: 1, Window: time.Minute}
	rawKey := "auth:login:email:" + strings.Repeat("sensitive-user-input", 1_000)

	if decision := limiter.Allow(rawKey, policy, time.Unix(100, 0)); !decision.Allowed {
		t.Fatal("expected request to be allowed")
	}
	if _, exists := limiter.buckets[rawKey]; exists {
		t.Fatal("raw rate-limit key was retained in memory")
	}
	for key := range limiter.buckets {
		if len(key) != sha256.Size*2 {
			t.Fatalf("stored key length = %d, want %d", len(key), sha256.Size*2)
		}
		if key != rateLimitKeyHash(rawKey) {
			t.Fatalf("stored key = %q, want SHA-256 hash", key)
		}
	}
}

func TestMemoryRateLimiterCapacityIsSafeUnderConcurrency(t *testing.T) {
	const capacity = 8
	limiter := newMemoryRateLimiterWithCapacity(capacity)
	policy := rateLimitPolicy{Limit: 1, Window: time.Minute}
	now := time.Unix(100, 0)

	var wg sync.WaitGroup
	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func(index int) {
			defer wg.Done()
			limiter.Allow("auth:login:email:user"+strconv.Itoa(index)+"@example.test", policy, now)
		}(i)
	}
	wg.Wait()

	if got := len(limiter.buckets); got != capacity {
		t.Fatalf("bucket count after concurrent admission = %d, want %d", got, capacity)
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

func TestPasswordChangeRateLimitBlocksAcrossChangedSourceIPs(t *testing.T) {
	limiter := newMemoryRateLimiter()
	now := time.Unix(100, 0)

	for i := 0; i < passwordChangeUserRateLimitPolicy.Limit; i++ {
		req := httptest.NewRequest(http.MethodPost, "/configuracoes/perfil/senha", nil)
		req.RemoteAddr = "198.51.100." + strconv.Itoa(10+i) + ":4321"
		if !allowPasswordChangeRateLimit(httptest.NewRecorder(), req, limiter, "user-1", now.Add(time.Duration(i)*time.Second)) {
			t.Fatalf("attempt %d unexpectedly blocked", i+1)
		}
	}

	req := httptest.NewRequest(http.MethodPost, "/configuracoes/perfil/senha", nil)
	req.RemoteAddr = "203.0.113.10:4321"
	rr := httptest.NewRecorder()
	if allowPasswordChangeRateLimit(rr, req, limiter, "user-1", now.Add(time.Minute)) {
		t.Fatal("expected global user password-change limit to block a new source IP")
	}
	if rr.Code != http.StatusTooManyRequests {
		t.Fatalf("status = %d, want %d", rr.Code, http.StatusTooManyRequests)
	}
}

func TestNewHTTPServerUsesFiniteConnectionDeadlines(t *testing.T) {
	server := newHTTPServer(":8080", http.NotFoundHandler())
	if server.ReadHeaderTimeout != httpReadHeaderTimeout || server.ReadHeaderTimeout <= 0 {
		t.Fatalf("ReadHeaderTimeout = %s, want %s", server.ReadHeaderTimeout, httpReadHeaderTimeout)
	}
	if server.ReadTimeout != httpReadTimeout || server.ReadTimeout <= 0 {
		t.Fatalf("ReadTimeout = %s, want %s", server.ReadTimeout, httpReadTimeout)
	}
	if server.WriteTimeout != httpWriteTimeout || server.WriteTimeout <= 0 {
		t.Fatalf("WriteTimeout = %s, want %s", server.WriteTimeout, httpWriteTimeout)
	}
	if server.IdleTimeout != httpIdleTimeout || server.IdleTimeout <= 0 {
		t.Fatalf("IdleTimeout = %s, want %s", server.IdleTimeout, httpIdleTimeout)
	}
}

func TestLimitTransactionMutationBodiesRunsBeforeNextHandler(t *testing.T) {
	body := strings.Repeat("x", transactionRequestMaxBytes+1)
	handler := limitTransactionMutationBodies(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, err := io.ReadAll(r.Body)
		if err == nil {
			t.Fatal("expected body limit error")
		}
		var maxErr *http.MaxBytesError
		if !errors.As(err, &maxErr) {
			t.Fatalf("read error = %v, want MaxBytesError", err)
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	req := httptest.NewRequest(http.MethodPost, "/transacoes", strings.NewReader(body))
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)
	if rr.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want %d", rr.Code, http.StatusNoContent)
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
	assertHeader(t, rr, "Strict-Transport-Security", "max-age=31536000; includeSubDomains")
	assertHeader(t, rr, "Content-Security-Policy", cspEnforcingPolicy)
	if got := rr.Header().Get("Content-Security-Policy-Report-Only"); got != "" {
		t.Fatalf("report-only CSP remained active: %q", got)
	}
}

func assertHeader(t *testing.T, rr *httptest.ResponseRecorder, name, want string) {
	t.Helper()
	if got := rr.Header().Get(name); got != want {
		t.Fatalf("expected %s=%q, got %q", name, want, got)
	}
}
