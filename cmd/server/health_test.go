package main

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

type testPinger struct {
	err    error
	pinged int
}

func (tp *testPinger) Ping() error {
	tp.pinged++
	return tp.err
}

func TestHealthHandlerHealthyDB(t *testing.T) {
	tp := &testPinger{}
	handler := newHealthHandler(tp)

	req := httptest.NewRequest(http.MethodGet, "/health", nil)
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rr.Code)
	}
	ct := rr.Header().Get("Content-Type")
	if ct != "application/json" {
		t.Fatalf("expected Content-Type application/json, got %q", ct)
	}
	cc := rr.Header().Get("Cache-Control")
	if cc != "no-store" {
		t.Fatalf("expected Cache-Control no-store, got %q", cc)
	}

	var resp healthResponse
	if err := json.NewDecoder(rr.Body).Decode(&resp); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}
	if resp.Status != "healthy" {
		t.Fatalf("expected status healthy, got %q", resp.Status)
	}
	if tp.pinged != 1 {
		t.Fatalf("expected exactly 1 Ping() call, got %d", tp.pinged)
	}
}

func TestHealthHandlerUnhealthyDB(t *testing.T) {
	tp := &testPinger{err: errors.New("database disk image is malformed — sensitive internal detail")}
	handler := newHealthHandler(tp)

	req := httptest.NewRequest(http.MethodGet, "/health", nil)
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	if rr.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected 503, got %d", rr.Code)
	}
	ct := rr.Header().Get("Content-Type")
	if ct != "application/json" {
		t.Fatalf("expected Content-Type application/json, got %q", ct)
	}
	cc := rr.Header().Get("Cache-Control")
	if cc != "no-store" {
		t.Fatalf("expected Cache-Control no-store, got %q", cc)
	}

	bodyBytes, err := io.ReadAll(rr.Body)
	if err != nil {
		t.Fatalf("failed to read body: %v", err)
	}
	body := string(bodyBytes)

	var resp healthResponse
	if err := json.Unmarshal(bodyBytes, &resp); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}
	if resp.Status != "unhealthy" {
		t.Fatalf("expected status unhealthy, got %q", resp.Status)
	}

	if strings.Contains(body, "malformed") || strings.Contains(body, "sensitive") {
		t.Fatalf("response body leaked internal error details: %s", body)
	}

	if tp.pinged != 1 {
		t.Fatalf("expected exactly 1 Ping() call, got %d", tp.pinged)
	}
}

func TestHealthHandlerUnavailableWithoutPublishedRuntime(t *testing.T) {
	coordinator := &serverRuntimeCoordinator{}
	handler := newHealthHandler(coordinator)

	req := httptest.NewRequest(http.MethodGet, "/health", nil)
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	if rr.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected 503 without a published runtime, got %d", rr.Code)
	}
	var resp healthResponse
	if err := json.NewDecoder(rr.Body).Decode(&resp); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}
	if resp.Status != "unhealthy" {
		t.Fatalf("expected status unhealthy, got %q", resp.Status)
	}
}

func TestHealthHandlerMethodNotAllowed(t *testing.T) {
	methods := []string{http.MethodPost, http.MethodPut, http.MethodDelete, http.MethodHead,
		http.MethodPatch, http.MethodConnect, http.MethodOptions, http.MethodTrace}

	for _, method := range methods {
		t.Run(method, func(t *testing.T) {
			tp := &testPinger{}
			handler := newHealthHandler(tp)

			req := httptest.NewRequest(method, "/health", nil)
			rr := httptest.NewRecorder()
			handler.ServeHTTP(rr, req)

			if rr.Code != http.StatusMethodNotAllowed {
				t.Fatalf("expected 405 for %s, got %d", method, rr.Code)
			}
			allow := rr.Header().Get("Allow")
			if allow != http.MethodGet {
				t.Fatalf("expected Allow: GET for %s, got %q", method, allow)
			}
			cc := rr.Header().Get("Cache-Control")
			if cc != "no-store" {
				t.Fatalf("expected Cache-Control no-store for %s, got %q", method, cc)
			}
			if tp.pinged != 0 {
				t.Fatalf("expected 0 Ping() calls for %s, got %d", method, tp.pinged)
			}
		})
	}
}

func TestHealthHandlerMethodNotAllowedBodyMinimal(t *testing.T) {
	disallowedMethods := []string{http.MethodPost, http.MethodPut, http.MethodDelete, http.MethodHead}
	sensitiveTerms := []string{"error", "internal", "database", "sql", "stack", "trace", "password", "secret", "token", "key"}

	for _, method := range disallowedMethods {
		t.Run(method, func(t *testing.T) {
			tp := &testPinger{}
			handler := newHealthHandler(tp)

			req := httptest.NewRequest(method, "/health", nil)
			rr := httptest.NewRecorder()
			handler.ServeHTTP(rr, req)

			bodyBytes, err := io.ReadAll(rr.Body)
			if err != nil {
				t.Fatalf("failed to read body: %v", err)
			}
			body := strings.ToLower(string(bodyBytes))

			for _, term := range sensitiveTerms {
				if strings.Contains(body, term) {
					t.Fatalf("405 body for %s leaked sensitive term %q: %s", method, term, body)
				}
			}
		})
	}
}

// proxyHTTPSConfig returns a boundary config that accepts HTTPS forwarded
// by a trusted proxy for the domain app.example.com.
func proxyHTTPSConfig() httpBoundaryConfig {
	return newHTTPBoundaryConfigWithAccess("app.example.com", "", "proxy", "https://app.example.com")
}

func newProxyHTTPSRequest(method, path string) *http.Request {
	req := httptest.NewRequest(method, "https://app.example.com"+path, nil)
	req.RemoteAddr = "127.0.0.1:56789"
	req.Header.Set("X-Forwarded-Proto", "https")
	return req
}

func TestHealthIntegrationHTTPSHealthy(t *testing.T) {
	tp := &testPinger{}
	mux := http.NewServeMux()
	registerHealthRoute(mux, tp)
	cfg := proxyHTTPSConfig()
	handler := cfg.enforceRemoteHTTPS(mux)

	req := newProxyHTTPSRequest(http.MethodGet, "/health")
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rr.Code)
	}
	ct := rr.Header().Get("Content-Type")
	if ct != "application/json" {
		t.Fatalf("expected Content-Type application/json, got %q", ct)
	}
	cc := rr.Header().Get("Cache-Control")
	if cc != "no-store" {
		t.Fatalf("expected Cache-Control no-store, got %q", cc)
	}
	var resp healthResponse
	if err := json.NewDecoder(rr.Body).Decode(&resp); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}
	if resp.Status != "healthy" {
		t.Fatalf("expected status healthy, got %q", resp.Status)
	}
	if tp.pinged != 1 {
		t.Fatalf("expected exactly 1 Ping() call, got %d", tp.pinged)
	}
}

func TestHealthIntegrationHTTPSUnhealthy(t *testing.T) {
	tp := &testPinger{err: errors.New("database disk image is malformed — sensitive internal detail")}
	mux := http.NewServeMux()
	registerHealthRoute(mux, tp)
	cfg := proxyHTTPSConfig()
	handler := cfg.enforceRemoteHTTPS(mux)

	req := newProxyHTTPSRequest(http.MethodGet, "/health")
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	if rr.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected 503, got %d", rr.Code)
	}
	ct := rr.Header().Get("Content-Type")
	if ct != "application/json" {
		t.Fatalf("expected Content-Type application/json, got %q", ct)
	}
	cc := rr.Header().Get("Cache-Control")
	if cc != "no-store" {
		t.Fatalf("expected Cache-Control no-store, got %q", cc)
	}

	bodyBytes, _ := io.ReadAll(rr.Body)
	var resp healthResponse
	if err := json.Unmarshal(bodyBytes, &resp); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}
	if resp.Status != "unhealthy" {
		t.Fatalf("expected status unhealthy, got %q", resp.Status)
	}
	if strings.Contains(string(bodyBytes), "malformed") || strings.Contains(string(bodyBytes), "sensitive") {
		t.Fatalf("response body leaked internal error details")
	}
	if tp.pinged != 1 {
		t.Fatalf("expected exactly 1 Ping() call, got %d", tp.pinged)
	}
}

func TestHealthIntegrationHTTPBlockedBeforePing(t *testing.T) {
	tp := &testPinger{}
	mux := http.NewServeMux()
	registerHealthRoute(mux, tp)
	cfg := proxyHTTPSConfig()
	handler := cfg.enforceRemoteHTTPS(mux)

	req := httptest.NewRequest(http.MethodGet, "http://app.example.com/health", nil)
	req.RemoteAddr = "203.0.113.10:44123"
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	if rr.Code != http.StatusUpgradeRequired {
		t.Fatalf("expected 426, got %d", rr.Code)
	}
	if tp.pinged != 0 {
		t.Fatalf("expected 0 Ping() calls, got %d", tp.pinged)
	}
}

func TestHealthIntegrationMethodNotAllowed(t *testing.T) {
	tp := &testPinger{}
	mux := http.NewServeMux()
	registerHealthRoute(mux, tp)
	cfg := proxyHTTPSConfig()
	handler := cfg.enforceRemoteHTTPS(mux)

	req := newProxyHTTPSRequest(http.MethodPost, "/health")
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	if rr.Code != http.StatusMethodNotAllowed {
		t.Fatalf("expected 405, got %d", rr.Code)
	}
	allow := rr.Header().Get("Allow")
	if allow != http.MethodGet {
		t.Fatalf("expected Allow: GET, got %q", allow)
	}
	if tp.pinged != 0 {
		t.Fatalf("expected 0 Ping() calls, got %d", tp.pinged)
	}
}
