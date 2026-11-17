package main

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func lastCookie(cookies []*http.Cookie, name string) *http.Cookie {
	var found *http.Cookie
	for _, cookie := range cookies {
		if cookie.Name == name {
			found = cookie
		}
	}
	return found
}

func TestLogoutRouteRejectsMissingCSRF(t *testing.T) {
	db, service, userID := setupTestDB(t)
	defer db.Close()
	workspaceID, _, err := service.ResolveWorkspaceMembership(userID)
	if err != nil {
		t.Fatalf("resolve workspace: %v", err)
	}
	token, _, err := service.CreateSession(userID, workspaceID, time.Hour, false)
	if err != nil {
		t.Fatalf("create session: %v", err)
	}
	signer := &csrfSigner{secret: bytes.Repeat([]byte{41}, 32), ttl: time.Hour}
	mux := http.NewServeMux()
	mux.HandleFunc("/logout", withAuth(service, signer, func(w http.ResponseWriter, r *http.Request, _ authContext) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		handleLogout(w, r, service)
	}))

	req := httptest.NewRequest(http.MethodPost, "/logout", nil)
	req.AddCookie(&http.Cookie{Name: sessionCookieName, Value: token})
	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, req)

	if rr.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want %d", rr.Code, http.StatusForbidden)
	}
	if _, _, _, _, err := service.ResolveAndRefreshSession(token); err != nil {
		t.Fatalf("missing CSRF revoked the session: %v", err)
	}
}

func TestLogoutRouteRevokesSessionForHTMXAndNavigation(t *testing.T) {
	for _, tc := range []struct {
		name       string
		hx         bool
		wantStatus int
	}{
		{name: "htmx", hx: true, wantStatus: http.StatusOK},
		{name: "navigation", hx: false, wantStatus: http.StatusSeeOther},
	} {
		t.Run(tc.name, func(t *testing.T) {
			db, service, userID := setupTestDB(t)
			defer db.Close()
			workspaceID, _, err := service.ResolveWorkspaceMembership(userID)
			if err != nil {
				t.Fatalf("resolve workspace: %v", err)
			}
			token, _, err := service.CreateSession(userID, workspaceID, time.Hour, false)
			if err != nil {
				t.Fatalf("create session: %v", err)
			}
			signer := &csrfSigner{secret: bytes.Repeat([]byte{42}, 32), ttl: time.Hour}
			csrfToken := signer.issue(time.Now())
			mux := http.NewServeMux()
			mux.HandleFunc("/logout", withAuth(service, signer, func(w http.ResponseWriter, r *http.Request, _ authContext) {
				if r.Method != http.MethodPost {
					http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
					return
				}
				handleLogout(w, r, service)
			}))

			req := httptest.NewRequest(http.MethodPost, "/logout", nil)
			req.AddCookie(&http.Cookie{Name: sessionCookieName, Value: token})
			req.AddCookie(&http.Cookie{Name: csrfCookieName, Value: csrfToken})
			req.Header.Set(csrfHeaderName, csrfToken)
			if tc.hx {
				req.Header.Set("HX-Request", "true")
			}
			rr := httptest.NewRecorder()
			mux.ServeHTTP(rr, req)

			if rr.Code != tc.wantStatus {
				t.Fatalf("status = %d, want %d; body=%q", rr.Code, tc.wantStatus, rr.Body.String())
			}
			if _, _, _, _, err := service.ResolveAndRefreshSession(token); err == nil {
				t.Fatal("logout did not revoke the session")
			}
			if cookie := lastCookie(rr.Result().Cookies(), sessionCookieName); cookie == nil || cookie.MaxAge >= 0 || cookie.Value != "" {
				t.Fatalf("session cookie was not cleared: %+v", cookie)
			}
			if tc.hx {
				if got := rr.Header().Get("HX-Redirect"); got != "/login" {
					t.Fatalf("HX-Redirect = %q, want /login", got)
				}
				if got := rr.Header().Get("HX-Trigger"); got != "contabase:logout" {
					t.Fatalf("HX-Trigger = %q, want contabase:logout", got)
				}
			} else if got := rr.Header().Get("Location"); got != "/login" {
				t.Fatalf("Location = %q, want /login", got)
			}
		})
	}
}
