package main

import (
	"bytes"
	"database/sql"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/contabase-app/contabase/internal/auth"
	"github.com/pquerna/otp/totp"
	"golang.org/x/crypto/bcrypt"
)

type backupExportReauthFixture struct {
	db          *sql.DB
	authService *auth.Service
	handler     http.Handler
	token       string
	dataDir     string
}

func newBackupExportReauthFixture(t *testing.T, totpEnabled bool) *backupExportReauthFixture {
	t.Helper()
	dataDir := t.TempDir()
	t.Setenv("DATA_DIR", dataDir)
	dbPath := filepath.Join(dataDir, "live.db")
	current := createRestoreTestServerRuntime(t, dbPath, "G1")

	hash, err := bcrypt.GenerateFromPassword([]byte("correct-password"), bcrypt.MinCost)
	if err != nil {
		t.Fatalf("hash password: %v", err)
	}
	if _, err := current.DBs.DB.Exec(`UPDATE users SET password_hash = ? WHERE id = 'restore-user'`, string(hash)); err != nil {
		t.Fatalf("seed password: %v", err)
	}
	if totpEnabled {
		t.Setenv("AUTH_ENCRYPTION_KEY", testAuthEncryptionKey("backup-export-reauth"))
		secretEnc, err := encryptTextForAuth(testTOTPSecret)
		if err != nil {
			t.Fatalf("encrypt TOTP secret: %v", err)
		}
		if _, err := current.DBs.DB.Exec(`
			UPDATE users
			SET totp_enabled = 1, totp_secret_enc = ?, totp_backup_codes = '[]', totp_last_used_step = -1
			WHERE id = 'restore-user'
		`, secretEnc); err != nil {
			t.Fatalf("enable TOTP: %v", err)
		}
	}

	authService := auth.NewService(current.DBs.DB)
	token, _, err := authService.CreateSession("restore-user", "restore-workspace", time.Hour, false)
	if err != nil {
		t.Fatalf("create session: %v", err)
	}
	signer := &csrfSigner{secret: bytes.Repeat([]byte{13}, 32), ttl: time.Hour}
	tpl, err := newAppTemplateEngine(false, buildFuncMap())
	if err != nil {
		t.Fatalf("parse templates: %v", err)
	}

	db := current.DBs.DB
	coordinator := newServerRuntimeCoordinator(current, func(next *serverRuntime) {
		db = next.DBs.DB
		authService.DB = next.DBs.DB
	})
	t.Cleanup(func() { _ = coordinator.Close() })
	audit := newSecurityAuditService(func() *sql.DB { return db })
	limiter := newMemoryRateLimiter()
	reauthPolicy := rateLimitPolicy{Limit: 20, Window: 10 * time.Minute}
	exportPolicy := rateLimitPolicy{Limit: 6, Window: 5 * time.Minute}

	mux := http.NewServeMux()
	mux.HandleFunc("/admin/reautenticar", adminReauthRoute(authService, signer, tpl, limiter, reauthPolicy))
	mux.HandleFunc("/admin/backups/exportar", withAuth(authService, signer, func(w http.ResponseWriter, r *http.Request, ctx authContext) {
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		if !requireGlobalAdmin(w, r, ctx) {
			return
		}
		if !requireRecentAuthentication(w, r, authService, ctx, "/admin/backups/exportar") {
			return
		}
		key := "admin:backup:export:user:" + ctx.UserID + ":ip:" + clientIP(r)
		if !allowRateLimitedRequest(w, r, limiter, key, exportPolicy, time.Now()) {
			return
		}
		handleAdminBackupExport(w, r, db, dbPath, ctx.UserID)
	}))

	handler := coordinator.Middleware(audit.Middleware(securityHeaders(enforceRemoteHTTPS(limitTransactionMutationBodies(mux)))), isRuntimeLeaseBypassRequest)
	return &backupExportReauthFixture{
		db:          current.DBs.DB,
		authService: authService,
		handler:     handler,
		token:       token,
		dataDir:     dataDir,
	}
}

func (f *backupExportReauthFixture) serveAs(t *testing.T, token, method, target string, form url.Values, cookies ...*http.Cookie) *httptest.ResponseRecorder {
	t.Helper()
	var body io.Reader
	if form != nil {
		body = strings.NewReader(form.Encode())
	}
	req := httptest.NewRequest(method, "http://localhost"+target, body)
	req.RemoteAddr = "127.0.0.1:12345"
	if form != nil {
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	}
	if token != "" {
		req.AddCookie(&http.Cookie{Name: sessionCookieName, Value: token})
	}
	for _, cookie := range cookies {
		if cookie != nil {
			req.AddCookie(cookie)
		}
	}
	rr := httptest.NewRecorder()
	f.handler.ServeHTTP(rr, req)
	return rr
}

func (f *backupExportReauthFixture) serve(t *testing.T, method, target string, form url.Values, cookies ...*http.Cookie) *httptest.ResponseRecorder {
	t.Helper()
	return f.serveAs(t, f.token, method, target, form, cookies...)
}

func (f *backupExportReauthFixture) expireRecentAuthentication(t *testing.T) {
	t.Helper()
	if _, err := f.db.Exec(`
		UPDATE sessions
		SET reauthenticated_at = ?
		WHERE user_id = 'restore-user'
	`, time.Now().Add(-recentAdminAuthenticationWindow-time.Second).Unix()); err != nil {
		t.Fatalf("expire recent authentication: %v", err)
	}
}

func (f *backupExportReauthFixture) backupExportAuditCount(t *testing.T) int {
	t.Helper()
	var count int
	if err := f.db.QueryRow(`SELECT COUNT(*) FROM security_logs WHERE event_type LIKE 'backup.export%'`).Scan(&count); err != nil {
		t.Fatalf("count backup export audit: %v", err)
	}
	return count
}

func (f *backupExportReauthFixture) reauthenticationForm(t *testing.T, wantTOTP bool) (url.Values, *http.Cookie) {
	t.Helper()
	rr := f.serve(t, http.MethodGet, "/admin/reautenticar?return_to=%2Fadmin%2Fbackups%2Fexportar", nil)
	if rr.Code != http.StatusOK {
		t.Fatalf("reauthentication page status = %d, want %d; body=%q", rr.Code, http.StatusOK, rr.Body.String())
	}
	body := rr.Body.String()
	for _, want := range []string{
		`name="return_to" value="/admin/backups/exportar"`,
		`href="/admin/backups"`,
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("reauthentication page missing %q: %q", want, body)
		}
	}
	if got := strings.Contains(body, `name="current_totp_code"`); got != wantTOTP {
		t.Fatalf("reauthentication page TOTP field = %v, want %v", got, wantTOTP)
	}
	if strings.Contains(body, `href="/admin/backups/exportar"`) {
		t.Fatal("cancel action points to the export download")
	}
	csrfToken := extractAdminReauthCSRFToken(t, body)
	csrfCookie := responseCookie(t, rr.Result().Cookies(), csrfCookieName)
	return url.Values{
		"csrf_token": {csrfToken},
		"return_to":  {"/admin/backups/exportar"},
	}, csrfCookie
}

func assertBackupExportDownload(t *testing.T, rr *httptest.ResponseRecorder) {
	t.Helper()
	if rr.Code != http.StatusOK {
		t.Fatalf("export status = %d, want %d; body=%q", rr.Code, http.StatusOK, rr.Body.String())
	}
	if disposition := rr.Header().Get("Content-Disposition"); !strings.HasPrefix(disposition, "attachment;") {
		t.Fatalf("Content-Disposition = %q, want attachment", disposition)
	}
	if !strings.HasPrefix(rr.Body.String(), "SQLite format 3\x00") {
		t.Fatal("export response is not a SQLite snapshot")
	}
}

func TestAdminReauthReturnPathAllowlistKeepsExportExact(t *testing.T) {
	for _, tc := range []struct {
		name string
		raw  string
		want string
	}{
		{"export", "/admin/backups/exportar", "/admin/backups/exportar"},
		{"backups", "/admin/backups", "/admin/backups"},
		{"users", "/admin/usuarios", "/admin/usuarios"},
		{"workspaces", "/admin/workspaces", "/admin/workspaces"},
		{"audit", "/admin/auditoria", "/admin/auditoria"},
		{"query string", "/admin/backups/exportar?download=1", "/configuracoes"},
		{"fragment", "/admin/backups/exportar#download", "/configuracoes"},
		{"trailing slash", "/admin/backups/exportar/", "/configuracoes"},
		{"external URL", "https://attacker.example/admin/backups/exportar", "/configuracoes"},
		{"scheme relative URL", "//attacker.example/admin/backups/exportar", "/configuracoes"},
		{"arbitrary path", "/admin/backups/exportar/../../usuarios", "/configuracoes"},
		{"empty", "", "/configuracoes"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := safeAdminReauthReturnPath(tc.raw); got != tc.want {
				t.Fatalf("safeAdminReauthReturnPath(%q) = %q, want %q", tc.raw, got, tc.want)
			}
		})
	}

	for _, tc := range []struct {
		returnTo string
		cancelTo string
	}{
		{"/admin/backups/exportar", "/admin/backups"},
		{"/admin/backups", "/admin/backups"},
		{"/admin/usuarios", "/admin/usuarios"},
		{"https://attacker.example/", "/configuracoes"},
	} {
		if got := safeAdminReauthCancelPath(tc.returnTo); got != tc.cancelTo {
			t.Fatalf("safeAdminReauthCancelPath(%q) = %q, want %q", tc.returnTo, got, tc.cancelTo)
		}
	}
}

func TestBackupExportWithRecentAuthenticationDownloadsDirectly(t *testing.T) {
	f := newBackupExportReauthFixture(t, false)
	rr := f.serve(t, http.MethodGet, "/admin/backups/exportar", nil)
	assertBackupExportDownload(t, rr)
	if got := f.backupExportAuditCount(t); got != 1 {
		t.Fatalf("backup export audit events = %d, want 1", got)
	}
	end := readExportReceipt(t, exportReceiptDirectory(t, f.dataDir), "finished.json")
	if end.Event != "backup.export" || end.State != "server_write_completed" {
		t.Fatalf("unexpected export receipt: %+v", end)
	}
}

func TestBackupExportReauthenticationResumesDownloadAndAuditsOnce(t *testing.T) {
	f := newBackupExportReauthFixture(t, true)
	f.expireRecentAuthentication(t)

	redirect := f.serve(t, http.MethodGet, "/admin/backups/exportar", nil)
	if redirect.Code != http.StatusSeeOther {
		t.Fatalf("stale export status = %d, want %d", redirect.Code, http.StatusSeeOther)
	}
	if got := redirect.Header().Get("Location"); got != "/admin/reautenticar?return_to=%2Fadmin%2Fbackups%2Fexportar" {
		t.Fatalf("stale export location = %q", got)
	}
	if got := f.backupExportAuditCount(t); got != 0 {
		t.Fatalf("redirect created %d backup export audit events", got)
	}

	form, csrfCookie := f.reauthenticationForm(t, true)
	code, err := totp.GenerateCode(testTOTPSecret, time.Now())
	if err != nil {
		t.Fatalf("generate TOTP code: %v", err)
	}
	form.Set("current_password", "correct-password")
	form.Set("current_totp_code", code)

	post := f.serve(t, http.MethodPost, "/admin/reautenticar", form, csrfCookie)
	if post.Code != http.StatusSeeOther {
		t.Fatalf("reauthentication status = %d, want %d; body=%q", post.Code, http.StatusSeeOther, post.Body.String())
	}
	if got := post.Header().Get("Location"); got != "/admin/backups/exportar" {
		t.Fatalf("reauthentication location = %q, want export", got)
	}
	recent, err := f.authService.SessionRecentlyAuthenticated(f.token, time.Now(), recentAdminAuthenticationWindow)
	if err != nil || !recent {
		t.Fatalf("session recent = %v, err = %v; want true", recent, err)
	}

	download := f.serve(t, http.MethodGet, "/admin/backups/exportar", nil)
	assertBackupExportDownload(t, download)
	if got := f.backupExportAuditCount(t); got != 1 {
		t.Fatalf("backup export audit events = %d, want 1", got)
	}
	end := readExportReceipt(t, exportReceiptDirectory(t, f.dataDir), "finished.json")
	if end.Event != "backup.export" || end.State != "server_write_completed" {
		t.Fatalf("unexpected export receipt: %+v", end)
	}
}

func TestBackupExportReauthenticationRejectsWrongPassword(t *testing.T) {
	f := newBackupExportReauthFixture(t, false)
	f.expireRecentAuthentication(t)

	form, csrfCookie := f.reauthenticationForm(t, false)
	form.Set("current_password", "wrong-password")
	post := f.serve(t, http.MethodPost, "/admin/reautenticar", form, csrfCookie)
	if post.Code != http.StatusUnauthorized {
		t.Fatalf("wrong password status = %d, want %d", post.Code, http.StatusUnauthorized)
	}
	if !strings.Contains(post.Body.String(), `href="/admin/backups"`) {
		t.Fatal("failed reauthentication lost the backup cancel target")
	}
	recent, err := f.authService.SessionRecentlyAuthenticated(f.token, time.Now(), recentAdminAuthenticationWindow)
	if err != nil || recent {
		t.Fatalf("session recent = %v, err = %v; want false", recent, err)
	}
	download := f.serve(t, http.MethodGet, "/admin/backups/exportar", nil)
	if download.Code != http.StatusSeeOther {
		t.Fatalf("export after wrong password status = %d, want reauthentication redirect", download.Code)
	}
	if got := f.backupExportAuditCount(t); got != 0 {
		t.Fatalf("wrong password created %d backup export audit events", got)
	}
}

func TestBackupExportReauthenticationRejectsInvalidTOTP(t *testing.T) {
	f := newBackupExportReauthFixture(t, true)
	f.expireRecentAuthentication(t)

	form, csrfCookie := f.reauthenticationForm(t, true)
	code, err := totp.GenerateCode(testTOTPSecret, time.Now())
	if err != nil {
		t.Fatalf("generate TOTP code: %v", err)
	}
	wrongCode := "000000"
	if code == wrongCode {
		wrongCode = "000001"
	}
	form.Set("current_password", "correct-password")
	form.Set("current_totp_code", wrongCode)
	post := f.serve(t, http.MethodPost, "/admin/reautenticar", form, csrfCookie)
	if post.Code != http.StatusUnauthorized {
		t.Fatalf("invalid TOTP status = %d, want %d", post.Code, http.StatusUnauthorized)
	}
	download := f.serve(t, http.MethodGet, "/admin/backups/exportar", nil)
	if download.Code != http.StatusSeeOther {
		t.Fatalf("export after invalid TOTP status = %d, want reauthentication redirect", download.Code)
	}
	if got := f.backupExportAuditCount(t); got != 0 {
		t.Fatalf("invalid TOTP created %d backup export audit events", got)
	}
}

func TestBackupExportCancelDoesNotResumeDownload(t *testing.T) {
	f := newBackupExportReauthFixture(t, false)
	rr := f.serve(t, http.MethodGet, "/admin/reautenticar?return_to=%2Fadmin%2Fbackups%2Fexportar", nil)
	if rr.Code != http.StatusOK {
		t.Fatalf("reauthentication page status = %d, want %d", rr.Code, http.StatusOK)
	}
	if !strings.Contains(rr.Body.String(), `href="/admin/backups"`) {
		t.Fatal("cancel target must return to the backups page")
	}
	if got := f.backupExportAuditCount(t); got != 0 {
		t.Fatalf("cancel target created %d backup export audit events", got)
	}
}

func TestBackupExportRejectsNonAdmin(t *testing.T) {
	f := newBackupExportReauthFixture(t, false)
	now := time.Now().Unix()
	if _, err := f.db.Exec(`
		INSERT INTO users (id, name, email, password_hash, status, created_at, updated_at)
		VALUES ('manager-user', 'Manager User', 'manager@example.test', 'hash', 'active', ?, ?)
	`, now, now); err != nil {
		t.Fatalf("seed manager: %v", err)
	}
	if _, err := f.db.Exec(`
		INSERT INTO workspace_members (workspace_id, user_id, role, joined_at)
		VALUES ('restore-workspace', 'manager-user', 'MANAGER', ?)
	`, now); err != nil {
		t.Fatalf("seed manager membership: %v", err)
	}
	token, _, err := f.authService.CreateSession("manager-user", "restore-workspace", time.Hour, false)
	if err != nil {
		t.Fatalf("create manager session: %v", err)
	}
	rr := f.serveAs(t, token, http.MethodGet, "/admin/backups/exportar", nil)
	if rr.Code != http.StatusForbidden {
		t.Fatalf("manager export status = %d, want %d", rr.Code, http.StatusForbidden)
	}
	if got := f.backupExportAuditCount(t); got != 0 {
		t.Fatalf("manager export created %d backup export audit events", got)
	}
}

func TestAdminReauthenticationRejectsUnsafeReturnPath(t *testing.T) {
	f := newBackupExportReauthFixture(t, false)
	rr := f.serve(t, http.MethodGet, "/admin/reautenticar?return_to=https%3A%2F%2Fattacker.example%2Fcollect", nil)
	if rr.Code != http.StatusOK {
		t.Fatalf("reauthentication page status = %d, want %d", rr.Code, http.StatusOK)
	}
	body := rr.Body.String()
	if strings.Contains(body, "attacker.example") {
		t.Fatal("unsafe return URL reflected in response")
	}
	if !strings.Contains(body, `name="return_to" value="/configuracoes"`) {
		t.Fatal("unsafe return URL was not replaced")
	}
	if !strings.Contains(body, `href="/configuracoes"`) {
		t.Fatal("unsafe return URL was not removed from the cancel target")
	}
}

func TestBackupExportStaleReauthenticationDoesNotDownload(t *testing.T) {
	f := newBackupExportReauthFixture(t, false)
	if _, err := f.db.Exec(`
		UPDATE sessions
		SET reauthenticated_at = ?
		WHERE user_id = 'restore-user'
	`, time.Now().Add(-recentAdminAuthenticationWindow-time.Second).Unix()); err != nil {
		t.Fatalf("expire recent authentication: %v", err)
	}
	rr := f.serve(t, http.MethodGet, "/admin/backups/exportar", nil)
	if rr.Code != http.StatusSeeOther {
		t.Fatalf("expired reauthentication status = %d, want %d", rr.Code, http.StatusSeeOther)
	}
	if got := rr.Header().Get("Location"); got != "/admin/reautenticar?return_to=%2Fadmin%2Fbackups%2Fexportar" {
		t.Fatalf("expired reauthentication location = %q", got)
	}
	if strings.Contains(rr.Body.String(), "SQLite format 3\x00") {
		t.Fatal("expired reauthentication streamed a backup")
	}
	if got := f.backupExportAuditCount(t); got != 0 {
		t.Fatalf("expired reauthentication created %d backup export audit events", got)
	}
}
