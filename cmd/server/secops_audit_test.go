package main

import (
	"database/sql"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
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
