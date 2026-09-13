package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestSecurityAuditFinishesInsideSingleRuntimeLease(t *testing.T) {
	root := t.TempDir()
	t.Setenv("DATA_DIR", root)
	g1 := openFullBindingRuntime(t, filepath.Join(root, "audit-g1.db"), "g1")
	g2 := openFullBindingRuntime(t, filepath.Join(root, "audit-g2.db"), "g2")
	coordinator := newServerRuntimeCoordinator(g1, nil)
	defer coordinator.Close()

	auditEntered := make(chan struct{})
	releaseAudit := make(chan struct{})
	audit := newSecurityAuditService(func() *sql.DB { return g1.DBs.DB })
	audit.testHooks = &securityAuditTestHooks{beforeRecord: func() {
		close(auditEntered)
		<-releaseAudit
	}}
	mux := http.NewServeMux()
	mux.HandleFunc("/forbidden", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set(secOpsEventHeader, "security.tampering")
		w.WriteHeader(http.StatusForbidden)
	})
	handler := coordinator.Middleware(audit.Middleware(mux), nil)
	requestDone := make(chan struct{})
	go func() {
		rr := httptest.NewRecorder()
		handler.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/forbidden", nil))
		close(requestDone)
	}()
	<-auditEntered

	swapEntered := make(chan struct{})
	replaceDone := make(chan error, 1)
	go func() {
		replaceDone <- coordinator.Replace(nil, func(old *serverRuntime) (*serverRuntime, error) {
			close(swapEntered)
			_ = old.Close()
			return g2, nil
		})
	}()
	select {
	case <-swapEntered:
		t.Fatalf("Replace passed the request while security audit was still using G1")
	default:
	}
	close(releaseAudit)
	<-requestDone
	if err := <-replaceDone; err != nil {
		t.Fatalf("replace after audit: %v", err)
	}
}

func TestSecurityAuditRecordsAuthenticatedActorAcrossRequestContextCopies(t *testing.T) {
	db, authService, userID := setupTestDB(t)
	defer db.Close()

	var sourceWorkspaceID string
	if err := db.QueryRow(`SELECT workspace_id FROM workspace_members WHERE user_id = ?`, userID).Scan(&sourceWorkspaceID); err != nil {
		t.Fatalf("load source workspace: %v", err)
	}
	targetWorkspaceID := "workspace-audit-target"
	if _, err := db.Exec(`
		INSERT INTO workspaces (id, name, created_at, updated_at) VALUES (?, 'Audit target', unixepoch(), unixepoch());
		INSERT INTO workspace_members (workspace_id, user_id, role, joined_at) VALUES (?, ?, 'ADMIN', unixepoch())
	`, targetWorkspaceID, userID); err != nil {
		t.Fatalf("create target workspace: %v", err)
	}

	sessionToken, _, err := authService.CreateSession(userID, sourceWorkspaceID, time.Hour, false)
	if err != nil {
		t.Fatalf("create session: %v", err)
	}
	signer := &csrfSigner{secret: make([]byte, 32), ttl: time.Hour}
	audit := newSecurityAuditService(func() *sql.DB { return db })
	protected := withAuth(authService, signer, func(w http.ResponseWriter, r *http.Request, ctx authContext) {
		switch r.URL.Path {
		case "/admin/backups/exportar":
			if r.URL.Query().Get("switch_workspace") == "1" {
				if err := authService.UpdateSessionWorkspace(ctx.SessionToken, targetWorkspaceID); err != nil {
					http.Error(w, "workspace switch failed", http.StatusInternalServerError)
					return
				}
			}
			w.WriteHeader(http.StatusNoContent)
		case "/forbidden":
			http.Error(w, "forbidden", http.StatusForbidden)
		default:
			http.NotFound(w, r)
		}
	})
	handler := audit.Middleware(protected)

	request := func(target string) *http.Request {
		req := httptest.NewRequest(http.MethodGet, target, nil)
		req = req.WithContext(context.WithValue(req.Context(), runtimeLeaseContextKey{}, true))
		req.AddCookie(&http.Cookie{Name: sessionCookieName, Value: sessionToken})
		return req
	}
	assertAudit := func(t *testing.T, target, eventType string, wantStatus int) {
		t.Helper()
		if _, err := db.Exec(`DELETE FROM security_logs`); err != nil {
			t.Fatalf("clear audit log: %v", err)
		}
		rr := httptest.NewRecorder()
		handler.ServeHTTP(rr, request(target))
		if rr.Code != wantStatus {
			t.Fatalf("status = %d, want %d", rr.Code, wantStatus)
		}
		var gotUserID, gotWorkspaceID, gotEventType string
		if err := db.QueryRow(`SELECT COALESCE(user_id, ''), COALESCE(workspace_id, ''), event_type FROM security_logs`).Scan(&gotUserID, &gotWorkspaceID, &gotEventType); err != nil {
			t.Fatalf("read security audit: %v", err)
		}
		if gotUserID != userID || gotWorkspaceID != sourceWorkspaceID || gotEventType != eventType {
			t.Fatalf("audit = user=%q workspace=%q event=%q, want user=%q workspace=%q event=%q", gotUserID, gotWorkspaceID, gotEventType, userID, sourceWorkspaceID, eventType)
		}
	}

	assertAudit(t, "/admin/backups/exportar", "backup.export", http.StatusNoContent)
	assertAudit(t, "/forbidden", "auth.unauthorized", http.StatusForbidden)
	assertAudit(t, "/admin/backups/exportar?switch_workspace=1", "backup.export", http.StatusNoContent)

	var activeWorkspaceID string
	if err := db.QueryRow(`SELECT workspace_id FROM sessions WHERE user_id = ?`, userID).Scan(&activeWorkspaceID); err != nil {
		t.Fatalf("read switched session workspace: %v", err)
	}
	if activeWorkspaceID != targetWorkspaceID {
		t.Fatalf("session workspace = %q, want %q", activeWorkspaceID, targetWorkspaceID)
	}
}

func TestSecurityAuditStripsInternalHeadersAndRecordsOverride(t *testing.T) {
	db, authService, userID := setupTestDB(t)
	defer db.Close()

	var workspaceID string
	if err := db.QueryRow(`SELECT workspace_id FROM workspace_members WHERE user_id = ?`, userID).Scan(&workspaceID); err != nil {
		t.Fatalf("load workspace: %v", err)
	}
	sessionToken, _, err := authService.CreateSession(userID, workspaceID, time.Hour, false)
	if err != nil {
		t.Fatalf("create session: %v", err)
	}

	signer := &csrfSigner{secret: make([]byte, 32), ttl: time.Hour}
	audit := newSecurityAuditService(func() *sql.DB { return db })
	protected := withAuth(authService, signer, func(w http.ResponseWriter, _ *http.Request, _ authContext) {
		setSecOpsFailureHeaders(w, "security.tampering", "CRITICAL", "workspace_scope_violation", "")
		w.Header().Set("X-SecOps-Meta-Workspace-Id", "foreign-workspace")
		http.Error(w, "forbidden", http.StatusForbidden)
	})
	handler := audit.Middleware(protected)

	req := httptest.NewRequest(http.MethodGet, "/workspace/switch", nil)
	req = req.WithContext(context.WithValue(req.Context(), runtimeLeaseContextKey{}, true))
	req.AddCookie(&http.Cookie{Name: sessionCookieName, Value: sessionToken})
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	if rr.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want %d", rr.Code, http.StatusForbidden)
	}
	for key := range rr.Header() {
		if strings.HasPrefix(strings.ToLower(key), "x-secops-") {
			t.Fatalf("internal audit header leaked to client: %s", key)
		}
	}

	var gotEventType, gotSeverity, gotWorkspaceID, gotUserID, metadataRaw string
	if err := db.QueryRow(`
		SELECT event_type, severity, COALESCE(workspace_id, ''), COALESCE(user_id, ''), metadata
		FROM security_logs
	`).Scan(&gotEventType, &gotSeverity, &gotWorkspaceID, &gotUserID, &metadataRaw); err != nil {
		t.Fatalf("read security audit: %v", err)
	}
	if gotEventType != "security.tampering" || gotSeverity != "CRITICAL" || gotWorkspaceID != workspaceID || gotUserID != userID {
		t.Fatalf("audit = event=%q severity=%q workspace=%q user=%q", gotEventType, gotSeverity, gotWorkspaceID, gotUserID)
	}
	var metadata map[string]any
	if err := json.Unmarshal([]byte(metadataRaw), &metadata); err != nil {
		t.Fatalf("decode metadata: %v", err)
	}
	if metadata["reason"] != "workspace_scope_violation" {
		t.Fatalf("reason = %#v, want workspace_scope_violation", metadata["reason"])
	}
	if metadata["workspace_id"] != "foreign-workspace" {
		t.Fatalf("attempted workspace = %#v, want foreign-workspace", metadata["workspace_id"])
	}
}

func TestSecurityAuditRecordsBackupExportOnlyAfterFileDelivery(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/admin/backups/exportar", nil)
	if event, ok := classifySecurityEvent(req, http.StatusSeeOther, nil); ok {
		t.Fatalf("reauthentication redirect classified as successful event: %#v", event)
	}
	event, ok := classifySecurityEvent(req, http.StatusOK, nil)
	if !ok || event.Type != "backup.export" || event.Status != "success" {
		t.Fatalf("successful backup delivery event = %#v, recorded=%v", event, ok)
	}
}
