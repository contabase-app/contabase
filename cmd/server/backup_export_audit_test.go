package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/contabase-app/contabase/internal/auth"
	"github.com/contabase-app/contabase/internal/database"
)

func backupAuditTestChain(t *testing.T, current *serverRuntime, dbPath string, ops *backupExportAuditOps, notifiers ...*smtpNotifier) (*serverRuntimeCoordinator, http.Handler, *http.Request) {
	t.Helper()
	db := current.DBs.DB
	service := auth.NewService(db)
	token, _, err := service.CreateSession("restore-user", "restore-workspace", time.Hour, false)
	if err != nil {
		t.Fatal(err)
	}
	coordinator := newServerRuntimeCoordinator(current, func(next *serverRuntime) { db = next.DBs.DB; service.DB = db })
	t.Cleanup(func() { _ = coordinator.Close() })
	audit := newSecurityAuditService(func() *sql.DB { return db })
	audit.exportAuditOps = ops
	if len(notifiers) > 0 {
		audit.notifier = notifiers[0]
	}
	mux := http.NewServeMux()
	signer := newBootstrapRestoreTestCSRFSigner()
	mux.HandleFunc("/admin/backups/exportar", withAuth(service, signer, func(w http.ResponseWriter, r *http.Request, actor authContext) {
		if !requireGlobalAdmin(w, r, actor) {
			return
		}
		if !requireRecentAuthentication(w, r, service, actor, "/admin/backups") {
			return
		}
		handleAdminBackupExport(w, r, db, dbPath, actor.UserID)
	}))
	handler := coordinator.Middleware(audit.Middleware(securityHeaders(enforceRemoteHTTPS(limitTransactionMutationBodies(mux)))), isRuntimeLeaseBypassRequest)
	req := httptest.NewRequest(http.MethodGet, "http://localhost/admin/backups/exportar", nil)
	req.RemoteAddr = "127.0.0.1:12345"
	req.AddCookie(&http.Cookie{Name: sessionCookieName, Value: token})
	return coordinator, handler, req
}

func readExportReceipt(t *testing.T, root, name string) backupExportReceipt {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(root, name))
	if err != nil {
		t.Fatal(err)
	}
	var receipt backupExportReceipt
	if err := json.Unmarshal(data, &receipt); err != nil {
		t.Fatal(err)
	}
	if receipt.OperationID == "" || receipt.GenerationID == "" || receipt.ActorUserID != "restore-user" || receipt.WorkspaceID != "restore-workspace" || receipt.ClientReceiptConfirmed {
		t.Fatalf("invalid receipt: %+v", receipt)
	}
	if strings.Contains(string(data), "token") || strings.Contains(string(data), "password") || strings.Contains(string(data), "SQLite format") {
		t.Fatal("receipt contains forbidden payload")
	}
	info, err := os.Stat(filepath.Join(root, name))
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("receipt permissions: %v %v", info, err)
	}
	return receipt
}

func exportReceiptDirectory(t *testing.T, dataDir string) string {
	t.Helper()
	dirs, err := filepath.Glob(filepath.Join(dataDir, "audit", "backup-exports", "*"))
	if err != nil || len(dirs) != 1 {
		t.Fatalf("receipt directories: %v %v", dirs, err)
	}
	for _, p := range []string{filepath.Join(dataDir, "audit"), filepath.Join(dataDir, "audit", "backup-exports"), dirs[0]} {
		info, err := os.Stat(p)
		if err != nil || info.Mode().Perm() != 0o700 {
			t.Fatalf("audit directory permissions: %v %v", info, err)
		}
	}
	return dirs[0]
}

func TestBackupExportAuditSlowStreamSurvivesConcurrentImport(t *testing.T) {
	root := t.TempDir()
	t.Setenv("DATA_DIR", root)
	dbPath := filepath.Join(root, "live.db")
	current := createRestoreTestServerRuntime(t, dbPath, "G1")
	candidatePath := filepath.Join(root, "candidate.db")
	createRestoreTestDB(t, candidatePath, "G2").Close()
	coordinator, handler, req := backupAuditTestChain(t, current, dbPath, nil)
	generation := current.generationID
	w := newBlockingBackupWriter()
	done := make(chan struct{})
	go func() { defer close(done); handler.ServeHTTP(w, req) }()
	select {
	case <-w.wrote:
	case <-time.After(3 * time.Second):
		t.Fatal("stream did not start")
	}
	receiptDir := exportReceiptDirectory(t, root)
	start := readExportReceipt(t, receiptDir, "started.json")
	if start.State != "authorized" || start.GenerationID != generation {
		t.Fatalf("start: %+v", start)
	}
	if _, err := os.Stat(filepath.Join(receiptDir, "finished.json")); !os.IsNotExist(err) {
		t.Fatal("completion exists before stream finished")
	}
	var started int
	if err := current.DBs.DB.QueryRow(`SELECT count(*) FROM security_logs WHERE event_type='backup.export.started' AND user_id='restore-user' AND workspace_id='restore-workspace'`).Scan(&started); err != nil || started != 1 {
		t.Fatalf("G1 start audit: %d %v", started, err)
	}
	var result adminRestoreResult
	err := coordinator.ReplaceContext(context.Background(), nil, func(old *serverRuntime) (*serverRuntime, error) {
		var next *serverRuntime
		var err error
		result, next, err = restoreAdminBackupFromFile(old, "file:"+dbPath, dbPath, candidatePath, database.OpenRuntimeDBs, startRestoreTestServerRuntime)
		return next, err
	})
	if err != nil {
		close(w.release)
		<-done
		t.Fatal(err)
	}
	close(w.release)
	<-done
	end := readExportReceipt(t, receiptDir, "finished.json")
	if end.State != "server_write_completed" || end.Event != "backup.export" || end.OperationID != start.OperationID || end.GenerationID != generation || end.BytesWritten == 0 {
		t.Fatalf("completion: %+v", end)
	}
	entries, err := os.ReadDir(receiptDir)
	if err != nil || len(entries) != 2 {
		t.Fatalf("duplicate/pending events: %v %v", entries, err)
	}
	if err := coordinator.Read(func(next *serverRuntime) error {
		if next.generationID == generation {
			t.Fatal("generation did not change")
		}
		var count int
		if err := next.DBs.DB.QueryRow(`SELECT count(*) FROM security_logs WHERE event_type LIKE 'backup.export%'`).Scan(&count); err != nil {
			return err
		}
		if count != 0 {
			t.Fatalf("G1 audit was written into G2: %d", count)
		}
		return validateSQLiteIntegrity(next.DBs.DB)
	}); err != nil {
		t.Fatal(err)
	}
	backup := readExportReceipt(t, receiptDir, "started.json")
	if backup != start {
		t.Fatal("start receipt mutated")
	}
	// The independently captured pre-restore snapshot retains the G1 start.
	pre, err := sql.Open("sqlite", "file:"+result.PreRestoreBackupPath+"?mode=ro&immutable=1")
	if err != nil {
		t.Fatal(err)
	}
	defer pre.Close()
	if err := pre.QueryRow(`SELECT count(*) FROM security_logs WHERE id=?`, start.OperationID).Scan(&started); err != nil || started != 1 {
		t.Fatalf("lost G1 authorization: %d %v", started, err)
	}
}

type failedExportWriter struct{ *httptest.ResponseRecorder }

func (w failedExportWriter) Write(p []byte) (int, error) { return 0, io.ErrClosedPipe }

func TestBackupExportAuditFailureAndCancellation(t *testing.T) {
	for _, mode := range []string{"write_error", "cancel"} {
		t.Run(mode, func(t *testing.T) {
			root := t.TempDir()
			t.Setenv("DATA_DIR", root)
			path := filepath.Join(root, "live.db")
			current := createRestoreTestServerRuntime(t, path, "G1")
			_, handler, req := backupAuditTestChain(t, current, path, nil)
			if mode == "write_error" {
				handler.ServeHTTP(failedExportWriter{httptest.NewRecorder()}, req)
			} else {
				ctx, cancel := context.WithCancel(req.Context())
				req = req.WithContext(ctx)
				w := newBlockingBackupWriter()
				done := make(chan struct{})
				go func() { defer close(done); handler.ServeHTTP(w, req) }()
				select {
				case <-w.wrote:
				case <-time.After(3 * time.Second):
					t.Fatal("stream did not start")
				}
				cancel()
				close(w.release)
				<-done
			}
			end := readExportReceipt(t, exportReceiptDirectory(t, root), "finished.json")
			if end.Event != "backup.export.failed" || end.State == "server_write_completed" {
				t.Fatalf("false completion: %+v", end)
			}
		})
	}
}

func TestBackupExportAuditPersistenceFailures(t *testing.T) {
	for _, mode := range []string{"start_write", "finish_write", "finish_sync", "finish_dirsync"} {
		t.Run(mode, func(t *testing.T) {
			root := t.TempDir()
			t.Setenv("DATA_DIR", root)
			path := filepath.Join(root, "live.db")
			current := createRestoreTestServerRuntime(t, path, "G1")
			ops := &backupExportAuditOps{}
			var writes, syncs, dirs atomic.Int32
			fault := errors.New("injected audit persistence failure")
			ops.write = func(f *os.File, p []byte) (int, error) {
				n := writes.Add(1)
				if mode == "start_write" || mode == "finish_write" && n == 2 {
					return 0, fault
				}
				return f.Write(p)
			}
			ops.syncFile = func(f *os.File) error {
				if syncs.Add(1) == 2 && mode == "finish_sync" {
					return fault
				}
				return f.Sync()
			}
			ops.syncDirectory = func(f *os.File) error {
				if dirs.Add(1) == 2 && mode == "finish_dirsync" {
					return fault
				}
				return f.Sync()
			}
			_, handler, req := backupAuditTestChain(t, current, path, ops)
			rr := httptest.NewRecorder()
			handler.ServeHTTP(rr, req)
			if mode == "start_write" {
				if rr.Code != 500 || strings.Contains(rr.Body.String(), "SQLite format") {
					t.Fatalf("export ran without audit: %d", rr.Code)
				}
				return
			}
			dir := exportReceiptDirectory(t, root)
			readExportReceipt(t, dir, "started.json")
			if mode != "finish_dirsync" {
				if _, err := os.Stat(filepath.Join(dir, "finished.json")); !os.IsNotExist(err) {
					t.Fatal("incomplete result was published")
				}
			} else {
				// Directory fsync failure is reported; a visible file still describes
				// the actual completed server write, never client acknowledgement.
				end := readExportReceipt(t, dir, "finished.json")
				if end.State != "server_write_completed" {
					t.Fatal(end)
				}
			}
		})
	}
}

func TestBackupExportAuditInterruptedOperationHasNoCompletion(t *testing.T) {
	root := t.TempDir()
	t.Setenv("DATA_DIR", root)
	r, err := openBackupExportAuditDirectory("interrupted")
	if err != nil {
		t.Fatal(err)
	}
	a := &backupExportAudit{root: r, start: backupExportReceipt{OperationID: "interrupted", GenerationID: "G1", ActorUserID: "restore-user", WorkspaceID: "restore-workspace", State: "authorized", Event: "backup.export.started"}}
	if err := a.publish("started.json", a.start); err != nil {
		t.Fatal(err)
	}
	f, err := r.OpenFile("finished.json.pending", os.O_CREATE|os.O_WRONLY|os.O_EXCL, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = f.WriteString(`{"state":"server_write_completed"`)
	_ = f.Close()
	_ = r.Close()
	dir := exportReceiptDirectory(t, root)
	readExportReceipt(t, dir, "started.json")
	if _, err := os.Stat(filepath.Join(dir, "finished.json")); !os.IsNotExist(err) {
		t.Fatal("interrupted operation falsely completed")
	}
}

func TestBackupExportAuditFinishIsIdempotent(t *testing.T) {
	root := t.TempDir()
	t.Setenv("DATA_DIR", root)
	r, err := openBackupExportAuditDirectory("idempotent")
	if err != nil {
		t.Fatal(err)
	}
	a := &backupExportAudit{root: r, start: backupExportReceipt{OperationID: "idempotent", GenerationID: "G1", ActorUserID: "restore-user", WorkspaceID: "restore-workspace", State: "authorized", Event: "backup.export.started"}}
	var notifications int
	a.notify = func() { notifications++ }
	if err := a.publish("started.json", a.start); err != nil {
		t.Fatal(err)
	}
	if err := a.finish("server_write_completed", 42); err != nil {
		t.Fatal(err)
	}
	if err := a.finish("server_write_failed", 0); err != nil {
		t.Fatal(err)
	}
	dir := exportReceiptDirectory(t, root)
	end := readExportReceipt(t, dir, "finished.json")
	entries, err := os.ReadDir(dir)
	if err != nil || len(entries) != 2 || end.State != "server_write_completed" || end.BytesWritten != 42 || notifications != 1 {
		t.Fatalf("duplicate or overwritten result: %+v %v", end, err)
	}
}

func TestBackupExportAuditNotificationKeepsG1ConfigurationAfterImport(t *testing.T) {
	root := t.TempDir()
	t.Setenv("DATA_DIR", root)
	path := filepath.Join(root, "live.db")
	current := createRestoreTestServerRuntime(t, path, "G1")
	if _, err := current.DBs.DB.Exec(`UPDATE workspaces SET smtp_host='smtp.invalid', smtp_port=25,
		notification_email='audit@example.test', email_preferences='["backup.export"]' WHERE id='restore-workspace'`); err != nil {
		t.Fatal(err)
	}
	notifier := &smtpNotifier{jobs: make(chan smtpJob, 4)}
	c, handler, req := backupAuditTestChain(t, current, path, nil, notifier)
	generation := current.generationID
	w := newBlockingBackupWriter()
	done := make(chan struct{})
	go func() { defer close(done); handler.ServeHTTP(w, req) }()
	select {
	case <-w.wrote:
	case <-time.After(3 * time.Second):
		t.Fatal("stream did not start")
	}
	select {
	case <-notifier.jobs:
		t.Fatal("completion notified before streaming finished")
	default:
	}
	g2 := createRestoreTestServerRuntime(t, filepath.Join(root, "g2.db"), "G2")
	if err := c.ReplaceContext(context.Background(), nil, func(old *serverRuntime) (*serverRuntime, error) {
		if err := old.Close(); err != nil {
			return nil, err
		}
		return g2, nil
	}); err != nil {
		close(w.release)
		<-done
		t.Fatal(err)
	}
	close(w.release)
	<-done
	select {
	case job := <-notifier.jobs:
		if job.Config.NotificationEmail != "audit@example.test" || !strings.Contains(job.Body, generation) || !strings.Contains(job.Body, "não confirmado") {
			t.Fatalf("notification lost provenance: %+v", job)
		}
	default:
		t.Fatal("completion notification lost")
	}
	select {
	case <-notifier.jobs:
		t.Fatal("duplicate completion notification")
	default:
	}
}

func TestBackupExportAuditProcessCrashLeavesPending(t *testing.T) {
	if os.Getenv("B1_AUDIT_CRASH_CHILD") == "1" {
		r, err := openBackupExportAuditDirectory("crashed")
		if err != nil {
			panic(err)
		}
		a := &backupExportAudit{root: r, start: backupExportReceipt{OperationID: "crashed", GenerationID: "G1", ActorUserID: "restore-user", WorkspaceID: "restore-workspace", State: "authorized", Event: "backup.export.started"}}
		if err := a.publish("started.json", a.start); err != nil {
			panic(err)
		}
		f, err := r.OpenFile("finished.json.pending", os.O_CREATE|os.O_WRONLY|os.O_EXCL, 0o600)
		if err != nil {
			panic(err)
		}
		_, _ = f.WriteString(`{"state":"server_write_completed"`)
		os.Exit(91)
	}
	root := t.TempDir()
	t.Setenv("DATA_DIR", root)
	cmd := exec.Command(os.Args[0], "-test.run=^TestBackupExportAuditProcessCrashLeavesPending$")
	cmd.Env = []string{"DATA_DIR=" + root, "B1_AUDIT_CRASH_CHILD=1", "TMPDIR=" + os.TempDir(), "TZ=UTC"}
	err := cmd.Run()
	var exit *exec.ExitError
	if !errors.As(err, &exit) || exit.ExitCode() != 91 {
		t.Fatalf("child exit: %v", err)
	}
	dir := exportReceiptDirectory(t, root)
	readExportReceipt(t, dir, "started.json")
	if _, err := os.Stat(filepath.Join(dir, "finished.json")); !os.IsNotExist(err) {
		t.Fatal("crash produced a false completion")
	}
}

func TestBackupExportAuditRefusesSymlinkDirectory(t *testing.T) {
	root := t.TempDir()
	t.Setenv("DATA_DIR", root)
	outside := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(root, "audit")); err != nil {
		t.Fatal(err)
	}
	if r, err := openBackupExportAuditDirectory("refused"); err == nil {
		_ = r.Close()
		t.Fatal("audit accepted symlink")
	}
	entries, err := os.ReadDir(outside)
	if err != nil || len(entries) != 0 {
		t.Fatalf("audit escaped DATA_DIR: %v %v", entries, err)
	}
}

func TestBackupExportAuditDatabaseFailurePreventsStreaming(t *testing.T) {
	root := t.TempDir()
	t.Setenv("DATA_DIR", root)
	path := filepath.Join(root, "live.db")
	current := createRestoreTestServerRuntime(t, path, "G1")
	_, handler, req := backupAuditTestChain(t, current, path, nil)
	if _, err := current.DBs.DB.Exec(`DROP TABLE security_logs`); err != nil {
		t.Fatal(err)
	}
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)
	if rr.Code != http.StatusInternalServerError || strings.Contains(rr.Body.String(), "SQLite format") {
		t.Fatalf("stream ran despite failed authorization audit: %d", rr.Code)
	}
	dir := exportReceiptDirectory(t, root)
	end := readExportReceipt(t, dir, "finished.json")
	if end.State != "authorization_audit_failed" || end.Event != "backup.export.failed" || end.BytesWritten != 0 {
		t.Fatalf("incorrect terminal event: %+v", end)
	}
}
