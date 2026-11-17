package main

import (
	"database/sql"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/contabase-app/contabase/internal/auth"
	"github.com/contabase-app/contabase/internal/database"
	"github.com/contabase-app/contabase/internal/handlers"
	"golang.org/x/crypto/bcrypt"
)

func startBindingReplacement(t *testing.T, coordinator *serverRuntimeCoordinator, dbs *database.RuntimeDBs) *serverRuntime {
	t.Helper()
	var next *serverRuntime
	if err := coordinator.Replace(nil, func(old *serverRuntime) (*serverRuntime, error) {
		if err := old.Close(); err != nil {
			return nil, err
		}
		var startErr error
		next, startErr = startServerRuntime(dbs, serverRuntimeOptions{
			scheduledInterval: time.Hour,
			recurringInterval: time.Hour,
			sessionInterval:   time.Hour,
		})
		return next, startErr
	}); err != nil {
		t.Fatalf("replace runtime: %v", err)
	}
	if next == nil {
		t.Fatal("replacement runtime was not published")
	}
	return next
}

func newDashboardBindingHandler(t *testing.T, coordinator *serverRuntimeCoordinator, provider *sqlDBProvider, authService *auth.Service, signer *csrfSigner) http.Handler {
	t.Helper()
	mux := http.NewServeMux()
	registerDashboardRoute(mux, provider, runtimeBindingTemplate{}, authService, signer, func(string) {})
	return coordinator.Middleware(mux, isRuntimeLeaseBypassRequest)
}

func getWithSession(t *testing.T, handler http.Handler, path, token string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, path, nil)
	if token != "" {
		req.AddCookie(&http.Cookie{Name: sessionCookieName, Value: token})
	}
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	return rec
}

func seedBindingUserRole(t *testing.T, db *sql.DB, role string) string {
	t.Helper()
	token := seedBindingIdentity(t, db)
	if _, err := db.Exec(`UPDATE workspace_members SET role = ? WHERE workspace_id = 'ws-binding' AND user_id = 'user-binding'`, role); err != nil {
		t.Fatalf("set binding role %s: %v", role, err)
	}
	return token
}

func seedBindingAttachmentTransaction(t *testing.T, db *sql.DB, workspaceID, transactionID, attachmentPath, description string) {
	t.Helper()
	now := time.Now().Unix()
	accountID := "account-" + transactionID
	if _, err := serverFixtureExec(t, db, `
		INSERT INTO accounts (id, workspace_id, name, type, initial_balance, current_balance, created_at, updated_at)
		VALUES (?, ?, 'Binding Account', 'CHECKING', 100000, 100000, ?, ?)
	`, accountID, workspaceID, now, now); err != nil {
		t.Fatalf("seed binding attachment account: %v", err)
	}
	if _, err := serverFixtureExec(t, db, `
		INSERT INTO transactions (
			id, workspace_id, user_id, account_id, type, amount, date, description,
			status, attachment_path, installment_number, total_installments, created_at, updated_at
		) VALUES (?, ?, 'user-binding', ?, 'EXPENSE', 12345, ?, ?, 'paid', ?, 1, 1, ?, ?)
	`, transactionID, workspaceID, accountID, now, description, attachmentPath, now, now); err != nil {
		t.Fatalf("seed binding attachment transaction: %v", err)
	}
}

func copyBindingSessionToRuntime(t *testing.T, source, target *sql.DB) {
	t.Helper()
	var id, userID, workspaceID, tokenHash string
	var expiresAt, createdAt, reauthenticatedAt int64
	var revokedAt sql.NullInt64
	var isRemember int
	if err := source.QueryRow(`
		SELECT id, user_id, workspace_id, token_hash, expires_at, revoked_at,
		       created_at, reauthenticated_at, is_remember
		FROM sessions
		ORDER BY created_at DESC, id
		LIMIT 1
	`).Scan(&id, &userID, &workspaceID, &tokenHash, &expiresAt, &revokedAt, &createdAt, &reauthenticatedAt, &isRemember); err != nil {
		t.Fatalf("read binding session: %v", err)
	}
	var revoked any
	if revokedAt.Valid {
		revoked = revokedAt.Int64
	}
	if _, err := target.Exec(`
		INSERT INTO sessions (
			id, user_id, workspace_id, token_hash, expires_at, revoked_at,
			created_at, reauthenticated_at, is_remember
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
	`, id, userID, workspaceID, tokenHash, expiresAt, revoked, createdAt, reauthenticatedAt, isRemember); err != nil {
		t.Fatalf("copy binding session: %v", err)
	}
}

// Scenario A: a normally initialized runtime completes the login flow and the
// authenticated root renders the dashboard.
func TestRuntimeDBBindingKeepsLoginAndDashboardWorking(t *testing.T) {
	root := t.TempDir()
	t.Setenv("DATA_DIR", root)
	runtime := openFullBindingRuntime(t, filepath.Join(root, "g1.db"), "g1")
	provider := newSQLDBProvider(runtime.DBs.DB)
	authService := auth.NewService(runtime.DBs.DB)
	coordinator := newServerRuntimeCoordinator(runtime, func(current *serverRuntime) {
		provider.Store(current.DBs.DB)
		authService.DB = current.DBs.DB
	})
	defer coordinator.Close()
	signer := newBootstrapRestoreTestCSRFSigner()
	handler := newDashboardBindingHandler(t, coordinator, provider, authService, signer)

	seedBindingIdentity(t, runtime.DBs.DB)
	hash, err := bcrypt.GenerateFromPassword([]byte("secret123"), bcrypt.DefaultCost)
	if err != nil {
		t.Fatalf("hash login password: %v", err)
	}
	if _, err := runtime.DBs.DB.Exec(`UPDATE users SET password_hash = ? WHERE id = 'user-binding'`, string(hash)); err != nil {
		t.Fatalf("set login password: %v", err)
	}

	form := url.Values{
		"email":    {"binding@example.test"},
		"password": {"secret123"},
	}
	loginReq := httptest.NewRequest(http.MethodPost, "/login", strings.NewReader(form.Encode()))
	loginReq.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	loginResp := httptest.NewRecorder()
	handleLoginSubmit(loginResp, loginReq, runtimeBindingTemplate{}, authService, runtime.DBs.DB, "csrf")
	if loginResp.Code != http.StatusSeeOther || loginResp.Header().Get("Location") != "/" {
		t.Fatalf("login response = %d location=%q", loginResp.Code, loginResp.Header().Get("Location"))
	}
	session := findCookie(loginResp.Result().Cookies(), sessionCookieName)
	if session == nil || session.Value == "" {
		t.Fatal("login did not issue a session cookie")
	}

	dashboard := getWithSession(t, handler, "/", session.Value)
	if dashboard.Code != http.StatusOK {
		t.Fatalf("authenticated dashboard = %d location=%q body=%q", dashboard.Code, dashboard.Header().Get("Location"), dashboard.Body.String())
	}
}

func TestDashboardRouteKeepsLegitimateFirstSetupRedirect(t *testing.T) {
	root := t.TempDir()
	t.Setenv("DATA_DIR", root)
	runtime := openFullBindingRuntime(t, filepath.Join(root, "g1.db"), "g1")
	provider := newSQLDBProvider(runtime.DBs.DB)
	authService := auth.NewService(runtime.DBs.DB)
	coordinator := newServerRuntimeCoordinator(runtime, func(current *serverRuntime) {
		provider.Store(current.DBs.DB)
		authService.DB = current.DBs.DB
	})
	defer coordinator.Close()
	handler := newDashboardBindingHandler(t, coordinator, provider, authService, newBootstrapRestoreTestCSRFSigner())

	rec := getWithSession(t, handler, "/", "")
	if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/setup" {
		t.Fatalf("first setup response = %d location=%q, want 303 /setup", rec.Code, rec.Header().Get("Location"))
	}
}

// Scenario B: after Replace closes the old runtime, the registered dashboard
// route must answer from the new DB instead of redirecting to setup.
func TestDashboardRouteUsesCurrentDBAfterRuntimeReplace(t *testing.T) {
	root := t.TempDir()
	t.Setenv("DATA_DIR", root)
	g1 := openFullBindingRuntime(t, filepath.Join(root, "g1.db"), "g1")
	g2DBs := openFullBindingDBs(t, filepath.Join(root, "g2.db"), "g2")
	provider := newSQLDBProvider(g1.DBs.DB)
	authService := auth.NewService(g1.DBs.DB)
	coordinator := newServerRuntimeCoordinator(g1, func(current *serverRuntime) {
		provider.Store(current.DBs.DB)
		authService.DB = current.DBs.DB
	})
	defer coordinator.Close()
	handler := newDashboardBindingHandler(t, coordinator, provider, authService, newBootstrapRestoreTestCSRFSigner())

	g1Token := seedBindingIdentity(t, g1.DBs.DB)
	if rec := getWithSession(t, handler, "/", g1Token); rec.Code != http.StatusOK {
		t.Fatalf("dashboard before replace = %d location=%q", rec.Code, rec.Header().Get("Location"))
	}

	oldDB := g1.DBs.DB
	g2 := startBindingReplacement(t, coordinator, g2DBs)
	if oldDB.Ping() == nil {
		t.Fatal("old runtime DB remained open after replace")
	}
	if g2.DBs.DB == oldDB {
		t.Fatal("replacement reused the old DB handle")
	}

	g2Token := seedBindingIdentity(t, g2.DBs.DB)
	rec := getWithSession(t, handler, "/", g2Token)
	if rec.Code != http.StatusOK {
		t.Fatalf("dashboard after replace = %d location=%q body=%q", rec.Code, rec.Header().Get("Location"), rec.Body.String())
	}
	if rec.Header().Get("Location") == "/setup" {
		t.Fatal("dashboard redirected to setup after a successful restore")
	}
}

// Scenario C: transaction routes use the current DB after Replace, keep the
// workspace_id filter explicit and never fall back to another tenant.
func TestTransactionRoutesUseCurrentDBAndKeepWorkspaceIsolation(t *testing.T) {
	root := t.TempDir()
	t.Setenv("DATA_DIR", root)
	g1 := openFullBindingRuntime(t, filepath.Join(root, "g1.db"), "g1")
	g2DBs := openFullBindingDBs(t, filepath.Join(root, "g2.db"), "g2")
	provider := newSQLDBProvider(g1.DBs.DB)
	authService := auth.NewService(g1.DBs.DB)
	txBase := handlers.TransactionHandler{Templates: runtimeBindingTemplate{}}
	bindTransactionHandlerRuntimeDBs(&txBase, g1.DBs)
	coordinator := newServerRuntimeCoordinator(g1, func(current *serverRuntime) {
		provider.Store(current.DBs.DB)
		authService.DB = current.DBs.DB
		bindTransactionHandlerRuntimeDBs(&txBase, current.DBs)
	})
	defer coordinator.Close()

	signer := newBootstrapRestoreTestCSRFSigner()
	mux := http.NewServeMux()
	registerTransacoesRoutes(mux, provider, authService, signer, &txBase)
	handler := coordinator.Middleware(mux, isRuntimeLeaseBypassRequest)

	seedBindingUserRole(t, g1.DBs.DB, "USER")
	g2 := startBindingReplacement(t, coordinator, g2DBs)

	token := seedBindingUserRole(t, g2.DBs.DB, "USER")
	seedBindingAttachmentTransaction(t, g2.DBs.DB, "ws-binding", "tx-own-binding", "uploads/ws-binding/attachment.txt", "OWN_TRANSACTION")
	if _, err := serverFixtureExec(t, g2.DBs.DB, `
		INSERT INTO workspaces (id, name, type, created_at, updated_at)
		VALUES ('ws-foreign', 'Foreign', 'personal', unixepoch(), unixepoch())
	`); err != nil {
		t.Fatalf("seed foreign workspace: %v", err)
	}
	seedBindingAttachmentTransaction(t, g2.DBs.DB, "ws-foreign", "tx-foreign-binding", "", "FOREIGN_TRANSACTION")

	attachmentDir := filepath.Join(root, "uploads", "ws-binding")
	if err := os.MkdirAll(attachmentDir, 0o755); err != nil {
		t.Fatalf("create attachment dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(attachmentDir, "attachment.txt"), []byte("binding attachment"), 0o600); err != nil {
		t.Fatalf("write attachment: %v", err)
	}

	attachment := getWithSession(t, handler, "/transacoes/tx-own-binding/anexo", token)
	if attachment.Code != http.StatusOK || attachment.Body.String() != "binding attachment" {
		t.Fatalf("own attachment = %d body=%q; stale ownership DB or wrong tenant", attachment.Code, attachment.Body.String())
	}

	foreign := getWithSession(t, handler, "/transacoes/tx-foreign-binding", token)
	if foreign.Code != http.StatusNotFound {
		t.Fatalf("foreign transaction = %d, want 404", foreign.Code)
	}
	if strings.Contains(foreign.Body.String(), "FOREIGN_TRANSACTION") {
		t.Fatal("foreign transaction leaked data across workspaces")
	}

	own := getWithSession(t, handler, "/transacoes/tx-own-binding", token)
	if own.Code != http.StatusOK {
		t.Fatalf("own transaction detail = %d, want 200", own.Code)
	}
}

// Scenario D: a failing HasAnyUser check must answer 503, never redirect to
// setup just because the query failed.
func TestDashboardRouteHasAnyUserFailureDoesNotRedirectToSetup(t *testing.T) {
	root := t.TempDir()
	t.Setenv("DATA_DIR", root)
	runtime := openFullBindingRuntime(t, filepath.Join(root, "g1.db"), "g1")
	authService := auth.NewService(runtime.DBs.DB)
	coordinator := newServerRuntimeCoordinator(runtime, func(current *serverRuntime) {
		authService.DB = current.DBs.DB
	})
	defer coordinator.Close()

	unavailableDB, err := openRawServerFixture(t)
	if err != nil {
		t.Fatalf("open unavailable fixture: %v", err)
	}
	if err := unavailableDB.Close(); err != nil {
		t.Fatalf("close unavailable fixture: %v", err)
	}
	provider := newSQLDBProvider(unavailableDB)
	handler := newDashboardBindingHandler(t, coordinator, provider, authService, newBootstrapRestoreTestCSRFSigner())

	token := seedBindingIdentity(t, runtime.DBs.DB)
	rec := getWithSession(t, handler, "/", token)
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("failed DB check = %d location=%q, want 503 without redirect", rec.Code, rec.Header().Get("Location"))
	}
	if rec.Header().Get("Location") != "" || rec.Header().Get("HX-Redirect") != "" {
		t.Fatalf("failed DB check redirected to %q/%q", rec.Header().Get("Location"), rec.Header().Get("HX-Redirect"))
	}
}

func TestHasAnyUserDistinguishesEmptyDatabaseFromQueryFailure(t *testing.T) {
	empty, err := openRawServerFixture(t)
	if err != nil {
		t.Fatalf("open empty fixture: %v", err)
	}
	hasUsers, err := database.HasAnyUser(empty)
	if err != nil || hasUsers {
		t.Fatalf("empty database = (%v, %v), want (false, nil)", hasUsers, err)
	}
	if err := empty.Close(); err != nil {
		t.Fatalf("close empty fixture: %v", err)
	}
	if _, err := database.HasAnyUser(empty); err == nil {
		t.Fatal("closed database did not report a query failure")
	}
	if _, err := database.HasAnyUser(nil); err == nil {
		t.Fatal("nil database did not report a query failure")
	}
}

// Scenario E: the atomic provider stays race-free while Replace publishes a new
// runtime and requests keep reading the route.
func TestSQLDBProviderConcurrentSwap(t *testing.T) {
	first, err := openRawServerFixture(t)
	if err != nil {
		t.Fatalf("open first provider fixture: %v", err)
	}
	second, err := openRawServerFixture(t)
	if err != nil {
		t.Fatalf("open second provider fixture: %v", err)
	}
	provider := newSQLDBProvider(first)
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 500; j++ {
				if provider.Load() == nil {
					t.Error("provider returned nil during swap")
					return
				}
			}
		}()
	}
	wg.Add(1)
	go func() {
		defer wg.Done()
		for j := 0; j < 500; j++ {
			if j%2 == 0 {
				provider.Store(second)
				continue
			}
			provider.Store(first)
		}
	}()
	wg.Wait()
}

func TestDashboardRouteSurvivesConcurrentRuntimeReplace(t *testing.T) {
	root := t.TempDir()
	t.Setenv("DATA_DIR", root)
	g1 := openFullBindingRuntime(t, filepath.Join(root, "g1.db"), "g1")
	g2DBs := openFullBindingDBs(t, filepath.Join(root, "g2.db"), "g2")
	provider := newSQLDBProvider(g1.DBs.DB)
	authService := auth.NewService(g1.DBs.DB)
	coordinator := newServerRuntimeCoordinator(g1, func(current *serverRuntime) {
		provider.Store(current.DBs.DB)
		authService.DB = current.DBs.DB
	})
	defer coordinator.Close()
	handler := newDashboardBindingHandler(t, coordinator, provider, authService, newBootstrapRestoreTestCSRFSigner())

	seedBindingIdentity(t, g1.DBs.DB)
	seedBindingIdentity(t, g2DBs.DB)
	if _, err := g1.DBs.DB.Exec(`DELETE FROM sessions`); err != nil {
		t.Fatalf("reset shared binding sessions: %v", err)
	}
	token, _, err := auth.NewService(g1.DBs.DB).CreateSession("user-binding", "ws-binding", time.Hour, false)
	if err != nil {
		t.Fatalf("create shared binding session: %v", err)
	}
	copyBindingSessionToRuntime(t, g1.DBs.DB, g2DBs.DB)

	const readers = 8
	const requestsPerReader = 10
	start := make(chan struct{})
	failures := make(chan string, readers*requestsPerReader)
	var wg sync.WaitGroup
	var requests atomic.Int64
	for i := 0; i < readers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			for j := 0; j < requestsPerReader; j++ {
				rec := getWithSession(t, handler, "/", token)
				requests.Add(1)
				if rec.Code != http.StatusOK {
					select {
					case failures <- fmt.Sprintf("code=%d location=%q", rec.Code, rec.Header().Get("Location")):
					default:
					}
				}
			}
		}()
	}
	close(start)
	startBindingReplacement(t, coordinator, g2DBs)
	wg.Wait()
	close(failures)
	if len(failures) > 0 {
		t.Fatalf("request failed during runtime replace: %s", <-failures)
	}
	if requests.Load() != readers*requestsPerReader {
		t.Fatalf("completed requests = %d, want %d", requests.Load(), readers*requestsPerReader)
	}
	if rec := getWithSession(t, handler, "/", token); rec.Code != http.StatusOK {
		t.Fatalf("dashboard after concurrent replace = %d location=%q", rec.Code, rec.Header().Get("Location"))
	}
}
