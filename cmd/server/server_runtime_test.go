package main

import (
	"database/sql"
	"errors"
	"fmt"
	"html/template"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/contabase-app/contabase/internal/auth"
	"github.com/contabase-app/contabase/internal/database"
	"github.com/contabase-app/contabase/internal/handlers"
)

func TestServerRuntimeStartFailureIsAllOrNothing(t *testing.T) {
	stages := []string{
		serverRuntimeStageDBs,
		serverRuntimeStageFallback,
		serverRuntimeStageScheduled,
		serverRuntimeStageRecurring,
		serverRuntimeStageSessions,
	}
	for _, stage := range stages {
		t.Run(stage, func(t *testing.T) {
			t.Setenv("CONTABASE_TIMEZONE", "UTC")
			dbPath := filepath.Join(t.TempDir(), "runtime.db")
			dbs := openDormantRuntimeWithAllEligibleJobs(t, dbPath, "box-abandoned")
			before := readRuntimePersistentEffects(t, dbs.DB, "box-abandoned")
			var resourceStarts atomic.Int32
			runtime, err := startServerRuntime(dbs, serverRuntimeOptions{
				scheduledInterval: time.Hour,
				recurringInterval: time.Hour,
				sessionInterval:   time.Hour,
				hooks: &serverRuntimeStartHooks{
					beforeActivationCheckpoint: func(got string) error {
						if got == stage {
							return fmt.Errorf("forced failure at %s checkpoint", stage)
						}
						return nil
					},
					onResourceStart: func(string, *sql.DB) { resourceStarts.Add(1) },
				},
			})
			if err == nil || runtime != nil {
				t.Fatalf("expected all-or-nothing failure after %s", stage)
			}
			if err := dbs.DB.Ping(); err == nil {
				t.Fatalf("normal pool leaked after %s failure", stage)
			}
			if err := dbs.Immediate.Ping(); err == nil {
				t.Fatalf("immediate pool leaked after %s failure", stage)
			}
			if got := resourceStarts.Load(); got != 0 {
				t.Fatalf("external resources started before rejected %s checkpoint: %d", stage, got)
			}
			reopened, err := database.OpenRuntimeDBs("file:" + dbPath)
			if err != nil {
				t.Fatalf("reopen abandoned generation after %s: %v", stage, err)
			}
			after := readRuntimePersistentEffects(t, reopened.DB, "box-abandoned")
			if after != before {
				t.Fatalf("rejected %s checkpoint persisted background effects: before=%+v after=%+v", stage, before, after)
			}
			if err := reopened.Close(); err != nil {
				t.Fatalf("close reopened generation after %s: %v", stage, err)
			}
		})
	}
}

func TestServerRuntimeActivationFailureStartsNoExternalResources(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "runtime-activation-failure.db")
	dbs := openDormantRuntimeWithAllEligibleJobs(t, dbPath, "box-activation-failure")
	before := readRuntimePersistentEffects(t, dbs.DB, "box-activation-failure")
	if err := dbs.Close(); err != nil {
		t.Fatalf("close owner before activation: %v", err)
	}
	var resourceStarts atomic.Int32
	runtime, err := startServerRuntime(dbs, serverRuntimeOptions{
		hooks: &serverRuntimeStartHooks{
			onResourceStart: func(string, *sql.DB) { resourceStarts.Add(1) },
		},
	})
	if err == nil || runtime != nil {
		t.Fatalf("closed owner start runtime=%v err=%v, want nil/error", runtime, err)
	}
	if got := resourceStarts.Load(); got != 0 {
		t.Fatalf("activation failure started %d external resources", got)
	}
	reopened, err := database.OpenRuntimeDBs("file:" + dbPath)
	if err != nil {
		t.Fatalf("reopen activation failure database: %v", err)
	}
	defer reopened.Close()
	after := readRuntimePersistentEffects(t, reopened.DB, "box-activation-failure")
	if after != before {
		t.Fatalf("activation failure persisted background effects: before=%+v after=%+v", before, after)
	}
}

func TestServerRuntimeActivatesInternalJobsAtFinalBoundary(t *testing.T) {
	t.Setenv("CONTABASE_TIMEZONE", "UTC")
	dbPath := filepath.Join(t.TempDir(), "runtime-activation.db")
	dbs := openDormantRuntimeWithEligibleBox(t, dbPath, "box-activated")
	observedDormantAtLastFallibleStage := false
	runtime, err := startServerRuntime(dbs, serverRuntimeOptions{
		scheduledInterval: time.Hour,
		recurringInterval: time.Hour,
		sessionInterval:   time.Hour,
		hooks: &serverRuntimeStartHooks{beforeActivationCheckpoint: func(stage string) error {
			if stage == serverRuntimeStageSessions {
				assertRuntimeAutomaticRechargeCount(t, dbs.DB, "box-activated", 0)
				observedDormantAtLastFallibleStage = true
			}
			return nil
		}},
	})
	if err != nil {
		t.Fatalf("start runtime at activation boundary: %v", err)
	}
	defer runtime.Close()
	if !observedDormantAtLastFallibleStage {
		t.Fatal("last fallible stage was not observed before RuntimeDBs activation")
	}
	waitForRuntimeAutomaticRecharge(t, dbs.DB, "box-activated", 1)
}

func TestServerRuntimeFinalBoundaryStartsAllJobsInOrderAndPersistsPositiveEffects(t *testing.T) {
	t.Setenv("CONTABASE_TIMEZONE", "UTC")
	dbPath := filepath.Join(t.TempDir(), "runtime-final-boundary.db")
	dbs := openDormantRuntimeWithAllEligibleJobs(t, dbPath, "box-final-boundary")
	sessionCleanupDone := make(chan error, 1)

	var orderMu sync.Mutex
	var order []string
	record := func(event string) {
		orderMu.Lock()
		order = append(order, event)
		orderMu.Unlock()
	}
	runtime, err := startServerRuntime(dbs, serverRuntimeOptions{
		scheduledInterval: time.Hour,
		recurringInterval: time.Hour,
		sessionInterval:   time.Hour,
		hooks: &serverRuntimeStartHooks{
			beforeActivationCheckpoint: func(stage string) error {
				record("checkpoint:" + stage)
				return nil
			},
			afterRuntimeDBsActivate: func() { record("runtime_dbs:activated") },
			onResourceStart:         func(stage string, _ *sql.DB) { record("start:" + stage) },
			onSessionCleanup:        func(err error) { sessionCleanupDone <- err },
		},
	})
	if err != nil {
		t.Fatalf("start runtime at final boundary: %v", err)
	}
	defer runtime.Close()
	record("returned")

	wantOrder := []string{
		"checkpoint:" + serverRuntimeStageDBs,
		"checkpoint:" + serverRuntimeStageFallback,
		"checkpoint:" + serverRuntimeStageScheduled,
		"checkpoint:" + serverRuntimeStageRecurring,
		"checkpoint:" + serverRuntimeStageSessions,
		"runtime_dbs:activated",
		"start:" + serverRuntimeStageFallback,
		"start:" + serverRuntimeStageScheduled,
		"start:" + serverRuntimeStageRecurring,
		"start:" + serverRuntimeStageSessions,
		"returned",
	}
	orderMu.Lock()
	gotOrder := append([]string(nil), order...)
	orderMu.Unlock()
	if strings.Join(gotOrder, "|") != strings.Join(wantOrder, "|") {
		t.Fatalf("runtime activation order=%v want=%v", gotOrder, wantOrder)
	}
	select {
	case err := <-sessionCleanupDone:
		if err != nil {
			t.Fatalf("initial session cleanup: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("initial session cleanup did not run")
	}

	waitForRuntimePersistentEffects(t, dbs.DB, "box-final-boundary")
}

func TestRestoreGenerationAbandonedBeforeFinalActivationHasNoBackgroundJobEffect(t *testing.T) {
	t.Setenv("CONTABASE_TIMEZONE", "UTC")
	fault := errors.New("forced restore generation rejection")
	for _, tt := range []struct {
		name           string
		invalidateAuth bool
	}{
		{name: "candidate G2", invalidateAuth: true},
		{name: "rollback G3", invalidateAuth: false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			dbPath := filepath.Join(t.TempDir(), "restore-generation.db")
			seed := openDormantRuntimeWithAllEligibleJobs(t, dbPath, "box-restore-abandoned")
			before := readRuntimePersistentEffects(t, seed.DB, "box-restore-abandoned")
			if err := seed.Close(); err != nil {
				t.Fatalf("close restore seed owner: %v", err)
			}

			var resourceStarts atomic.Int32
			runtime, err := reopenAdminRestoreRuntime("file:"+dbPath, database.OpenRuntimeDBs, func(dbs *database.RuntimeDBs) (*serverRuntime, error) {
				return startServerRuntime(dbs, serverRuntimeOptions{
					hooks: &serverRuntimeStartHooks{
						beforeActivationCheckpoint: func(stage string) error {
							if stage == serverRuntimeStageSessions {
								return fault
							}
							return nil
						},
						onResourceStart: func(string, *sql.DB) { resourceStarts.Add(1) },
					},
				})
			}, tt.invalidateAuth)
			if runtime != nil || !errors.Is(err, fault) {
				t.Fatalf("abandoned restore runtime=%v err=%v, want nil/%v", runtime, err, fault)
			}
			if got := resourceStarts.Load(); got != 0 {
				t.Fatalf("abandoned restore started %d external resources", got)
			}

			reopened, err := database.OpenRuntimeDBs("file:" + dbPath)
			if err != nil {
				t.Fatalf("reopen abandoned restore generation: %v", err)
			}
			after := readRuntimePersistentEffects(t, reopened.DB, "box-restore-abandoned")
			if after.scheduledStatus != before.scheduledStatus ||
				after.scheduledExecuted != before.scheduledExecuted ||
				after.accountBalance != before.accountBalance ||
				after.historyCount != before.historyCount ||
				after.notificationCount != before.notificationCount ||
				after.recurringTransactionCount != before.recurringTransactionCount ||
				after.recurringStateCount != before.recurringStateCount ||
				after.automaticRechargeCount != before.automaticRechargeCount ||
				after.sessionCount != before.sessionCount {
				t.Fatalf("abandoned restore persisted background effects: before=%+v after=%+v", before, after)
			}
			if err := reopened.Close(); err != nil {
				t.Fatalf("close abandoned restore verification owner: %v", err)
			}
		})
	}
}

func TestServerRuntimeCloseIsIdempotentAndWaitsAllJobs(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "runtime.db")
	dbs := createRestoreTestRuntimeDBs(t, dbPath, "runtime")
	runtime, err := startServerRuntime(dbs, serverRuntimeOptions{
		scheduledInterval: time.Hour,
		recurringInterval: time.Hour,
		sessionInterval:   time.Hour,
	})
	if err != nil {
		t.Fatalf("start runtime: %v", err)
	}
	if err := runtime.Close(); err != nil {
		t.Fatalf("first close: %v", err)
	}
	if err := runtime.Close(); err != nil {
		t.Fatalf("second close: %v", err)
	}
	for name, done := range map[string]<-chan struct{}{
		"scheduled": runtime.scheduledDone,
		"recurring": runtime.recurringDone,
		"sessions":  runtime.sessionsDone,
	} {
		select {
		case <-done:
		default:
			t.Fatalf("%s job still running after Close", name)
		}
	}
}

func TestServerRuntimeReplacementStartsAndStopsEachResourceExactlyOnce(t *testing.T) {
	root := t.TempDir()
	g1 := openFullBindingRuntime(t, filepath.Join(root, "g1.db"), "g1")
	g2DBs := openFullBindingDBs(t, filepath.Join(root, "g2.db"), "g2")
	coordinator := newServerRuntimeCoordinator(g1, nil)

	var mu sync.Mutex
	starts := make(map[string]int)
	stops := make(map[string]int)
	hooks := &serverRuntimeStartHooks{
		onResourceStart: func(name string, db *sql.DB) {
			wantDB := g2DBs.DB
			if name == serverRuntimeStageFallback || name == serverRuntimeStageRecurring {
				wantDB = g2DBs.Immediate
			}
			if db != wantDB {
				t.Errorf("%s started with the wrong G2 transaction pool", name)
			}
			mu.Lock()
			starts[name]++
			mu.Unlock()
		},
		onResourceStop: func(name string) {
			mu.Lock()
			stops[name]++
			mu.Unlock()
		},
	}
	if err := coordinator.Replace(nil, func(old *serverRuntime) (*serverRuntime, error) {
		if err := old.Close(); err != nil {
			return nil, err
		}
		return startServerRuntime(g2DBs, serverRuntimeOptions{
			scheduledInterval: time.Hour,
			recurringInterval: time.Hour,
			sessionInterval:   time.Hour,
			hooks:             hooks,
		})
	}); err != nil {
		t.Fatalf("replace runtime: %v", err)
	}

	resources := []string{
		serverRuntimeStageFallback,
		serverRuntimeStageScheduled,
		serverRuntimeStageRecurring,
		serverRuntimeStageSessions,
	}
	mu.Lock()
	for _, name := range resources {
		if starts[name] != 1 {
			mu.Unlock()
			t.Fatalf("%s starts=%d want=1", name, starts[name])
		}
	}
	mu.Unlock()

	if err := coordinator.Close(); err != nil {
		t.Fatalf("close replacement runtime: %v", err)
	}
	if err := coordinator.Close(); err != nil {
		t.Fatalf("second coordinator close: %v", err)
	}
	mu.Lock()
	defer mu.Unlock()
	for _, name := range resources {
		if stops[name] != 1 {
			t.Fatalf("%s stops=%d want=1", name, stops[name])
		}
	}
}

func TestCoordinatorReplaceWaitsForRequestLease(t *testing.T) {
	g1 := createRestoreTestServerRuntime(t, filepath.Join(t.TempDir(), "g1.db"), "g1")
	g2 := createRestoreTestServerRuntime(t, filepath.Join(t.TempDir(), "g2.db"), "g2")
	coordinator := newServerRuntimeCoordinator(g1, nil)
	defer coordinator.Close()

	readEntered := make(chan struct{})
	releaseRead := make(chan struct{})
	readDone := make(chan struct{})
	go func() {
		_ = coordinator.Read(func(runtime *serverRuntime) error {
			if runtime != g1 {
				t.Errorf("request did not receive G1")
			}
			close(readEntered)
			<-releaseRead
			return nil
		})
		close(readDone)
	}()
	<-readEntered

	swapEntered := make(chan struct{})
	replaceDone := make(chan error, 1)
	go func() {
		replaceDone <- coordinator.Replace(nil, func(old *serverRuntime) (*serverRuntime, error) {
			close(swapEntered)
			if err := old.Close(); err != nil {
				return nil, err
			}
			return g2, nil
		})
	}()
	select {
	case <-swapEntered:
		t.Fatalf("Replace entered while a G1 request still held the lease")
	default:
	}
	close(releaseRead)
	<-readDone
	if err := <-replaceDone; err != nil {
		t.Fatalf("replace: %v", err)
	}
	select {
	case <-swapEntered:
	default:
		t.Fatalf("Replace did not run after the lease was released")
	}
}

func TestCoordinatorHealthUsesG2AndRollbackG3OnSameMux(t *testing.T) {
	g1 := createRestoreTestServerRuntime(t, filepath.Join(t.TempDir(), "g1.db"), "g1")
	g2 := createRestoreTestServerRuntime(t, filepath.Join(t.TempDir(), "g2.db"), "g2")
	g3 := createRestoreTestServerRuntime(t, filepath.Join(t.TempDir(), "g3.db"), "g3")
	coordinator := newServerRuntimeCoordinator(g1, nil)
	defer coordinator.Close()
	mux := http.NewServeMux()
	registerHealthRoute(mux, coordinator)

	assertHealthStatus(t, mux, http.StatusOK)
	oldG1DB := g1.DBs.DB
	oldG1Immediate := g1.DBs.Immediate
	if err := coordinator.Replace(nil, func(old *serverRuntime) (*serverRuntime, error) {
		if err := old.Close(); err != nil {
			return nil, err
		}
		return g2, nil
	}); err != nil {
		t.Fatalf("publish G2: %v", err)
	}
	if oldG1DB.Ping() == nil || oldG1Immediate.Ping() == nil {
		t.Fatalf("G1 pools remained usable after replacement")
	}
	assertHealthStatus(t, mux, http.StatusOK)

	errRollback := errors.New("candidate failed after rollback")
	if err := coordinator.Replace(nil, func(old *serverRuntime) (*serverRuntime, error) {
		if err := old.Close(); err != nil {
			return nil, err
		}
		return g3, errRollback
	}); !errors.Is(err, errRollback) {
		t.Fatalf("expected rollback result, got %v", err)
	}
	assertHealthStatus(t, mux, http.StatusOK)
}

func TestHealthBehindWaitingWriterCompletesWithoutNestedReadLock(t *testing.T) {
	g1 := createRestoreTestServerRuntime(t, filepath.Join(t.TempDir(), "g1.db"), "g1")
	g2 := createRestoreTestServerRuntime(t, filepath.Join(t.TempDir(), "g2.db"), "g2")
	coordinator := newServerRuntimeCoordinator(g1, nil)
	defer coordinator.Close()
	mux := http.NewServeMux()
	registerHealthRoute(mux, coordinator)

	readerEntered := make(chan struct{})
	releaseReader := make(chan struct{})
	go func() {
		_ = coordinator.Read(func(*serverRuntime) error {
			close(readerEntered)
			<-releaseReader
			return nil
		})
	}()
	<-readerEntered
	writerEntered := make(chan struct{})
	writerDone := make(chan error, 1)
	go func() {
		writerDone <- coordinator.Replace(nil, func(old *serverRuntime) (*serverRuntime, error) {
			close(writerEntered)
			_ = old.Close()
			return g2, nil
		})
	}()
	for {
		if !coordinator.mu.TryRLock() {
			break
		}
		coordinator.mu.RUnlock()
		runtime.Gosched()
	}
	healthDone := make(chan int, 1)
	go func() {
		rr := httptest.NewRecorder()
		mux.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/health", nil))
		healthDone <- rr.Code
	}()
	close(releaseReader)
	if err := <-writerDone; err != nil {
		t.Fatalf("replace: %v", err)
	}
	<-writerEntered
	select {
	case code := <-healthDone:
		if code != http.StatusOK {
			t.Fatalf("health after writer returned %d", code)
		}
	case <-time.After(time.Second):
		t.Fatalf("health deadlocked behind waiting writer")
	}
}

func TestCoordinatorDoesNotPublishRuntimeBeforeSwapCompletes(t *testing.T) {
	g1 := createRestoreTestServerRuntime(t, filepath.Join(t.TempDir(), "g1.db"), "g1")
	g2 := createRestoreTestServerRuntime(t, filepath.Join(t.TempDir(), "g2.db"), "g2")
	var bound atomic.Pointer[serverRuntime]
	coordinator := newServerRuntimeCoordinator(g1, func(runtime *serverRuntime) { bound.Store(runtime) })
	defer coordinator.Close()
	swapEntered := make(chan struct{})
	releaseSwap := make(chan struct{})
	replaceDone := make(chan error, 1)
	go func() {
		replaceDone <- coordinator.Replace(nil, func(old *serverRuntime) (*serverRuntime, error) {
			_ = old.Close()
			close(swapEntered)
			<-releaseSwap
			return g2, nil
		})
	}()
	<-swapEntered
	if bound.Load() != g1 {
		t.Fatalf("G2 binding became visible before complete start")
	}
	readDone := make(chan struct{})
	go func() {
		_ = coordinator.Read(func(*serverRuntime) error { return nil })
		close(readDone)
	}()
	select {
	case <-readDone:
		t.Fatalf("request crossed the incomplete publication boundary")
	default:
	}
	close(releaseSwap)
	if err := <-replaceDone; err != nil {
		t.Fatalf("replace: %v", err)
	}
	if bound.Load() != g2 {
		t.Fatalf("G2 was not bound at publication")
	}
	<-readDone
}

func TestCloseWaitUnderCoordinatorLockHasNoLockInversion(t *testing.T) {
	dbs := createRestoreTestRuntimeDBs(t, filepath.Join(t.TempDir(), "g1.db"), "g1")
	jobDone := make(chan struct{})
	g1 := &serverRuntime{DBs: dbs, cancel: func() {}, scheduledDone: jobDone}
	g2 := createRestoreTestServerRuntime(t, filepath.Join(t.TempDir(), "g2.db"), "g2")
	coordinator := newServerRuntimeCoordinator(g1, nil)
	defer coordinator.Close()
	swapEntered := make(chan struct{})
	replaceDone := make(chan error, 1)
	go func() {
		replaceDone <- coordinator.Replace(nil, func(old *serverRuntime) (*serverRuntime, error) {
			close(swapEntered)
			if err := old.Close(); err != nil {
				return nil, err
			}
			return g2, nil
		})
	}()
	<-swapEntered
	select {
	case err := <-replaceDone:
		t.Fatalf("Close returned before owned job finished: %v", err)
	default:
	}
	close(jobDone)
	select {
	case err := <-replaceDone:
		if err != nil {
			t.Fatalf("replace after job completion: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatalf("Close waited on a job that depended on the coordinator")
	}
}

func TestCoordinatorFailClosedReturns503WithoutCallingHandler(t *testing.T) {
	g1 := createRestoreTestServerRuntime(t, filepath.Join(t.TempDir(), "g1.db"), "g1")
	coordinator := newServerRuntimeCoordinator(g1, nil)
	var called atomic.Int32
	mux := http.NewServeMux()
	registerHealthRoute(mux, coordinator)
	mux.HandleFunc("/db", func(w http.ResponseWriter, r *http.Request) {
		called.Add(1)
		w.WriteHeader(http.StatusOK)
	})
	handler := coordinator.Middleware(mux, isRuntimeLeaseBypassRequest)

	if err := coordinator.Replace(nil, func(old *serverRuntime) (*serverRuntime, error) {
		_ = old.Close()
		return nil, errors.New("candidate and rollback failed")
	}); err == nil {
		t.Fatalf("expected replacement failure")
	}
	request := httptest.NewRequest(http.MethodGet, "/db", nil)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusServiceUnavailable || called.Load() != 0 {
		t.Fatalf("fail-closed request code=%d handler_calls=%d", response.Code, called.Load())
	}
	assertHealthStatus(t, handler, http.StatusServiceUnavailable)
}

func TestCoordinatorRejectedRevalidationPreservesG1(t *testing.T) {
	g1 := createRestoreTestServerRuntime(t, filepath.Join(t.TempDir(), "g1.db"), "g1")
	coordinator := newServerRuntimeCoordinator(g1, nil)
	defer coordinator.Close()
	var swapCalls atomic.Int32
	err := coordinator.Replace(func(*serverRuntime) error {
		return errRestoreRevalidationDenied
	}, func(*serverRuntime) (*serverRuntime, error) {
		swapCalls.Add(1)
		return nil, nil
	})
	if !errors.Is(err, errRestoreRevalidationDenied) || swapCalls.Load() != 0 {
		t.Fatalf("revalidation did not fail closed: err=%v swap_calls=%d", err, swapCalls.Load())
	}
	if err := g1.DBs.DB.Ping(); err != nil {
		t.Fatalf("G1 was closed after rejected revalidation: %v", err)
	}
}

type blockingRestoreBody struct {
	readStarted chan struct{}
	release     chan struct{}
	once        sync.Once
}

func (b *blockingRestoreBody) Read(p []byte) (int, error) {
	b.once.Do(func() { close(b.readStarted) })
	<-b.release
	return 0, io.EOF
}

func (b *blockingRestoreBody) Close() error { return nil }

func TestAdminRestorePreliminaryIdentityDoesNotReadSlowUpload(t *testing.T) {
	root := t.TempDir()
	t.Setenv("DATA_DIR", root)
	runtime := openFullBindingRuntime(t, filepath.Join(root, "admin-phase-a.db"), "g1")
	coordinator := newServerRuntimeCoordinator(runtime, nil)
	defer coordinator.Close()
	token := seedBindingIdentity(t, runtime.DBs.DB)
	body := &blockingRestoreBody{readStarted: make(chan struct{}), release: make(chan struct{})}
	req := httptest.NewRequest(http.MethodPost, "/admin/backups/importar", body)
	req.AddCookie(&http.Cookie{Name: sessionCookieName, Value: token})
	rr := httptest.NewRecorder()

	if err := coordinator.Read(func(current *serverRuntime) error {
		_, err := resolveAdminRestoreIdentity(rr, req, current.DBs.DB)
		return err
	}); err != nil {
		t.Fatalf("preliminary identity: %v", err)
	}
	select {
	case <-body.readStarted:
		t.Fatalf("preliminary identity read the multipart body while holding RLock")
	default:
	}

	normalDone := make(chan struct{})
	go func() {
		_ = coordinator.Read(func(*serverRuntime) error { return nil })
		close(normalDone)
	}()
	select {
	case <-normalDone:
	case <-time.After(time.Second):
		t.Fatalf("normal request did not advance while candidate remained unstaged")
	}
	close(body.release)
}

func TestAdminRestorePhaseBRevokedSessionPreservesG1(t *testing.T) {
	root := t.TempDir()
	t.Setenv("DATA_DIR", root)
	g1 := openFullBindingRuntime(t, filepath.Join(root, "admin-revalidate.db"), "g1")
	coordinator := newServerRuntimeCoordinator(g1, nil)
	defer coordinator.Close()
	token := seedBindingIdentity(t, g1.DBs.DB)
	signer := newBootstrapRestoreTestCSRFSigner()
	csrfToken := signer.issue(time.Now())
	form := url.Values{"csrf_token": {csrfToken}}
	req := httptest.NewRequest(http.MethodPost, "/admin/backups/importar", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.AddCookie(&http.Cookie{Name: sessionCookieName, Value: token})
	req.AddCookie(&http.Cookie{Name: csrfCookieName, Value: csrfToken})
	if err := req.ParseForm(); err != nil {
		t.Fatalf("parse form: %v", err)
	}
	if err := auth.NewService(g1.DBs.DB).RevokeSession(token); err != nil {
		t.Fatalf("revoke session: %v", err)
	}
	var swapCalls atomic.Int32
	err := coordinator.Replace(func(current *serverRuntime) error {
		_, err := revalidateAdminRestoreRequest(httptest.NewRecorder(), req, current.DBs.DB, signer)
		return err
	}, func(*serverRuntime) (*serverRuntime, error) {
		swapCalls.Add(1)
		return nil, nil
	})
	if !errors.Is(err, errRestoreRevalidationDenied) || swapCalls.Load() != 0 {
		t.Fatalf("revoked session reached destructive phase: err=%v swaps=%d", err, swapCalls.Load())
	}
	if err := g1.DBs.DB.Ping(); err != nil {
		t.Fatalf("G1 was closed after TOCTOU denial: %v", err)
	}
}

func TestAdminAndBootstrapRestoreShareOneExclusiveCoordinator(t *testing.T) {
	g1 := createRestoreTestServerRuntime(t, filepath.Join(t.TempDir(), "g1.db"), "g1")
	coordinator := newServerRuntimeCoordinator(g1, nil)
	defer coordinator.Close()
	firstEntered := make(chan struct{})
	releaseFirst := make(chan struct{})
	secondEntered := make(chan struct{})
	firstDone := make(chan error, 1)
	secondDone := make(chan error, 1)
	go func() {
		firstDone <- coordinator.Replace(nil, func(old *serverRuntime) (*serverRuntime, error) {
			close(firstEntered)
			<-releaseFirst
			return old, nil
		})
	}()
	<-firstEntered
	go func() {
		secondDone <- coordinator.Replace(nil, func(old *serverRuntime) (*serverRuntime, error) {
			close(secondEntered)
			return old, nil
		})
	}()
	select {
	case <-secondEntered:
		t.Fatalf("bootstrap restore entered while admin restore held exclusivity")
	case <-time.After(20 * time.Millisecond):
	}
	close(releaseFirst)
	if err := <-firstDone; err != nil {
		t.Fatalf("admin replace: %v", err)
	}
	if err := <-secondDone; err != nil {
		t.Fatalf("bootstrap replace: %v", err)
	}
}

func TestRestoreRoutesBypassNormalLease(t *testing.T) {
	for _, path := range []string{"/admin/backups/importar", "/setup/restaurar", "/health"} {
		req := httptest.NewRequest(http.MethodPost, path, nil)
		if !isRuntimeLeaseBypassRequest(req) {
			t.Fatalf("special route %s would attempt RLock before its exclusive path", path)
		}
	}
	if isRuntimeLeaseBypassRequest(httptest.NewRequest(http.MethodGet, "/transacoes", nil)) {
		t.Fatalf("normal DB route bypassed the runtime lease")
	}
}

type runtimeBindingTemplate struct{}

func (runtimeBindingTemplate) ExecuteTemplate(io.Writer, string, any) error { return nil }
func (runtimeBindingTemplate) Lookup(string) *template.Template             { return nil }

func TestRuntimeBindingUsesG2ForAuthReadWriteAndInvoiceWriterOnSameMux(t *testing.T) {
	root := t.TempDir()
	t.Setenv("DATA_DIR", root)
	g1 := openFullBindingRuntime(t, filepath.Join(root, "g1.db"), "g1")
	g2DBs := openFullBindingDBs(t, filepath.Join(root, "g2.db"), "g2")

	db := g1.DBs.DB
	authService := auth.NewService(db)
	txBase := handlers.TransactionHandler{DB: db, Templates: runtimeBindingTemplate{}}
	faturasBase := handlers.FaturasHandler{DB: db, WriterDB: g1.DBs.Immediate, Templates: runtimeBindingTemplate{}}
	bind := func(runtime *serverRuntime) {
		db = runtime.DBs.DB
		authService.DB = runtime.DBs.DB
		txBase.DB = runtime.DBs.DB
		faturasBase.DB = runtime.DBs.DB
		faturasBase.WriterDB = runtime.DBs.Immediate
	}
	coordinator := newServerRuntimeCoordinator(g1, bind)
	defer coordinator.Close()

	signer := newBootstrapRestoreTestCSRFSigner()
	mux := http.NewServeMux()
	registerHealthRoute(mux, coordinator)
	mux.HandleFunc("/read", func(w http.ResponseWriter, r *http.Request) {
		var marker string
		if err := db.QueryRow(`SELECT value FROM runtime_generation LIMIT 1`).Scan(&marker); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		_, _ = io.WriteString(w, marker)
	})
	mux.HandleFunc("/session/check", withAuth(authService, signer, func(w http.ResponseWriter, r *http.Request, ctx authContext) {
		w.WriteHeader(http.StatusNoContent)
	}))
	mux.HandleFunc("/transacoes", func(w http.ResponseWriter, r *http.Request) {
		h := txBase
		if _, err := h.DB.Exec(`INSERT INTO runtime_writes (value) VALUES ('g2-write')`); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})
	mux.HandleFunc("/cartoes/faturas/pagar", func(w http.ResponseWriter, r *http.Request) {
		h := faturasBase
		h.WorkspaceID = "ws-binding"
		h.UserID = "user-binding"
		h.HandlePagarFatura(w, r)
	})
	handler := coordinator.Middleware(mux, isRuntimeLeaseBypassRequest)

	assertBody(t, handler, "/read", "g1")
	g1Token := seedBindingIdentity(t, g1.DBs.DB)
	assertAuthenticated(t, handler, g1Token)

	oldDB := g1.DBs.DB
	oldImmediate := g1.DBs.Immediate
	var g2 *serverRuntime
	if err := coordinator.Replace(nil, func(old *serverRuntime) (*serverRuntime, error) {
		if err := old.Close(); err != nil {
			return nil, err
		}
		var err error
		g2, err = startServerRuntime(g2DBs, serverRuntimeOptions{
			scheduledInterval: time.Hour,
			recurringInterval: time.Hour,
			sessionInterval:   time.Hour,
		})
		return g2, err
	}); err != nil {
		t.Fatalf("replace G1 with G2: %v", err)
	}
	if oldDB.Ping() == nil || oldImmediate.Ping() == nil {
		t.Fatalf("G1 remained operational after G2 publication")
	}
	assertBody(t, handler, "/read", "g2")
	g2Token := seedBindingIdentity(t, g2.DBs.DB)
	assertAuthenticated(t, handler, g2Token)

	writeReq := httptest.NewRequest(http.MethodPost, "/transacoes", nil)
	writeResp := httptest.NewRecorder()
	handler.ServeHTTP(writeResp, writeReq)
	if writeResp.Code != http.StatusNoContent {
		t.Fatalf("G2 transaction write failed: code=%d body=%s", writeResp.Code, writeResp.Body.String())
	}
	var writes int
	if err := g2.DBs.DB.QueryRow(`SELECT COUNT(1) FROM runtime_writes WHERE value = 'g2-write'`).Scan(&writes); err != nil || writes != 1 {
		t.Fatalf("transaction write did not reach G2: count=%d err=%v", writes, err)
	}

	seedBindingInvoice(t, g2.DBs.DB)
	form := url.Values{
		"invoice_id":         {"invoice-binding"},
		"payment_account_id": {"checking-binding"},
		"payment_mode":       {"settle"},
		"confirm_settle":     {"1"},
	}
	payReq := httptest.NewRequest(http.MethodPost, "/cartoes/faturas/pagar", strings.NewReader(form.Encode()))
	payReq.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	payResp := httptest.NewRecorder()
	handler.ServeHTTP(payResp, payReq)
	if payResp.Code >= http.StatusBadRequest {
		t.Fatalf("G2 invoice payment failed: code=%d body=%s", payResp.Code, payResp.Body.String())
	}
	var payments int
	if err := g2.DBs.DB.QueryRow(`SELECT COUNT(1) FROM invoice_payments WHERE invoice_id = 'invoice-binding'`).Scan(&payments); err != nil || payments != 1 {
		t.Fatalf("invoice payment did not reach G2 Immediate: count=%d err=%v", payments, err)
	}

	g2DB := g2.DBs.DB
	g2Immediate := g2.DBs.Immediate
	g3DBs := openFullBindingDBs(t, filepath.Join(root, "g3.db"), "g3")
	rollbackErr := errors.New("candidate failed and rollback rebuilt G3")
	var g3 *serverRuntime
	err := coordinator.Replace(nil, func(old *serverRuntime) (*serverRuntime, error) {
		if err := old.Close(); err != nil {
			return nil, err
		}
		var startErr error
		g3, startErr = startServerRuntime(g3DBs, serverRuntimeOptions{
			scheduledInterval: time.Hour,
			recurringInterval: time.Hour,
			sessionInterval:   time.Hour,
		})
		if startErr != nil {
			return nil, startErr
		}
		return g3, rollbackErr
	})
	if !errors.Is(err, rollbackErr) {
		t.Fatalf("replace G2 with rollback G3: got %v want %v", err, rollbackErr)
	}
	if g2DB.Ping() == nil || g2Immediate.Ping() == nil {
		t.Fatalf("G2 remained operational after rollback G3 publication")
	}
	assertBody(t, handler, "/read", "g3")
	g3Token := seedBindingIdentity(t, g3.DBs.DB)
	assertAuthenticated(t, handler, g3Token)

	writeReq = httptest.NewRequest(http.MethodPost, "/transacoes", nil)
	writeResp = httptest.NewRecorder()
	handler.ServeHTTP(writeResp, writeReq)
	if writeResp.Code != http.StatusNoContent {
		t.Fatalf("G3 transaction write failed: code=%d body=%s", writeResp.Code, writeResp.Body.String())
	}
	writes = 0
	if err := g3.DBs.DB.QueryRow(`SELECT COUNT(1) FROM runtime_writes WHERE value = 'g2-write'`).Scan(&writes); err != nil || writes != 1 {
		t.Fatalf("transaction write did not reach rollback G3: count=%d err=%v", writes, err)
	}
}

func TestRuntimeRebindsTransactionWriterDBToG2(t *testing.T) {
	root := t.TempDir()
	t.Setenv("DATA_DIR", root)
	g1 := openFullBindingRuntime(t, filepath.Join(root, "g1.db"), "g1")
	g2DBs := openFullBindingDBs(t, filepath.Join(root, "g2.db"), "g2")

	txBase := handlers.TransactionHandler{Templates: runtimeBindingTemplate{}}
	bindTransactionHandlerRuntimeDBs(&txBase, g1.DBs)
	coordinator := newServerRuntimeCoordinator(g1, func(runtime *serverRuntime) {
		bindTransactionHandlerRuntimeDBs(&txBase, runtime.DBs)
	})
	defer coordinator.Close()

	mux := http.NewServeMux()
	mux.HandleFunc("/transacoes/", func(w http.ResponseWriter, r *http.Request) {
		h := txBase
		h.WorkspaceID = "ws-binding"
		h.UserID = "user-binding"
		h.HandleTogglePagamento(w, r, "tx-binding")
	})
	handler := coordinator.Middleware(mux, isRuntimeLeaseBypassRequest)

	seedBindingToggleFixture(t, g1.DBs.DB, "tx-binding")
	assertBindingTogglePaid(t, handler, g1.DBs.DB, "tx-binding")

	oldDB := g1.DBs.DB
	oldImmediate := g1.DBs.Immediate
	var g2 *serverRuntime
	if err := coordinator.Replace(nil, func(old *serverRuntime) (*serverRuntime, error) {
		if err := old.Close(); err != nil {
			return nil, err
		}
		var err error
		g2, err = startServerRuntime(g2DBs, serverRuntimeOptions{
			scheduledInterval: time.Hour,
			recurringInterval: time.Hour,
			sessionInterval:   time.Hour,
		})
		return g2, err
	}); err != nil {
		t.Fatalf("replace G1 with G2: %v", err)
	}
	if oldDB.Ping() == nil || oldImmediate.Ping() == nil {
		t.Fatalf("G1 remained operational after G2 publication")
	}
	if g2 == nil {
		t.Fatal("G2 runtime was not published")
	}
	defer g2.Close()

	seedBindingToggleFixture(t, g2.DBs.DB, "tx-binding")
	assertBindingTogglePaid(t, handler, g2.DBs.DB, "tx-binding")
}

func seedBindingToggleFixture(t *testing.T, db *sql.DB, txID string) {
	t.Helper()
	now := time.Now().Unix()
	statements := []struct {
		query string
		args  []any
	}{
		{
			query: `INSERT INTO users (id, name, email, password_hash, created_at, updated_at)
				VALUES ('user-binding', 'Binding', 'binding@example.test', 'hash', ?, ?)`,
			args: []any{now, now},
		},
		{
			query: `INSERT INTO workspaces (id, name, type, created_at, updated_at)
				VALUES ('ws-binding', 'Binding', 'personal', ?, ?)`,
			args: []any{now, now},
		},
		{
			query: `INSERT INTO workspace_members (workspace_id, user_id, role, joined_at)
				VALUES ('ws-binding', 'user-binding', 'ADMIN', ?)`,
			args: []any{now},
		},
		{
			query: `INSERT INTO accounts (id, workspace_id, name, type, initial_balance, current_balance, created_at, updated_at)
				VALUES ('checking-binding', 'ws-binding', 'Checking', 'CHECKING', 100000, 100000, ?, ?)`,
			args: []any{now, now},
		},
		{
			query: `INSERT INTO transactions (id, workspace_id, user_id, account_id, type, amount, date, description, status, installment_number, total_installments, created_at, updated_at)
				VALUES (?, 'ws-binding', 'user-binding', 'checking-binding', 'EXPENSE', 12345, ?, 'Toggle Binding', 'pending', 1, 1, ?, ?)`,
			args: []any{txID, now, now, now},
		},
	}
	for _, statement := range statements {
		if _, err := db.Exec(statement.query, statement.args...); err != nil {
			t.Fatalf("seed binding toggle fixture: %v", err)
		}
	}
}

func assertBindingTogglePaid(t *testing.T, handler http.Handler, db *sql.DB, txID string) {
	t.Helper()
	form := url.Values{"payment_action": {"paid"}, "payment_date": {"2026-07-14"}}
	req := httptest.NewRequest(http.MethodPost, "/transacoes/"+txID+"/status-pagamento", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("toggle status = %d, body = %q", rr.Code, rr.Body.String())
	}
	var status string
	if err := db.QueryRow(`SELECT status FROM transactions WHERE id = ?`, txID).Scan(&status); err != nil {
		t.Fatalf("query toggled transaction: %v", err)
	}
	if status != "paid" {
		t.Fatalf("transaction status = %q, want paid", status)
	}
}

func assertHealthStatus(t *testing.T, handler http.Handler, want int) {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/health", nil)
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)
	if rr.Code != want {
		t.Fatalf("health status=%d want=%d body=%s", rr.Code, want, rr.Body.String())
	}
}

func openFullBindingDBs(t *testing.T, path, marker string) *database.RuntimeDBs {
	t.Helper()
	dbs, err := database.OpenRuntimeDBs("file:" + path)
	if err != nil {
		t.Fatalf("open full runtime DBs: %v", err)
	}
	if _, err := dbs.DB.Exec(`CREATE TABLE runtime_generation (value TEXT NOT NULL); CREATE TABLE runtime_writes (value TEXT NOT NULL); INSERT INTO runtime_generation (value) VALUES (?)`, marker); err != nil {
		_ = dbs.Close()
		t.Fatalf("seed runtime generation: %v", err)
	}
	return dbs
}

func openFullBindingRuntime(t *testing.T, path, marker string) *serverRuntime {
	t.Helper()
	dbs := openFullBindingDBs(t, path, marker)
	runtime, err := startServerRuntime(dbs, serverRuntimeOptions{
		scheduledInterval: time.Hour,
		recurringInterval: time.Hour,
		sessionInterval:   time.Hour,
	})
	if err != nil {
		t.Fatalf("start full runtime: %v", err)
	}
	return runtime
}

func openDormantRuntimeWithEligibleBox(t *testing.T, path, boxID string) *database.RuntimeDBs {
	t.Helper()
	dbs, err := database.OpenRuntimeDBs("file:" + path)
	if err != nil {
		t.Fatalf("open dormant runtime DBs: %v", err)
	}
	createdAt := time.Now().AddDate(0, -1, 0).Unix()
	if _, err := dbs.DB.Exec(`
		INSERT INTO workspaces (id, name, type) VALUES ('ws-runtime-jobs', 'Runtime Jobs', 'personal');
		INSERT INTO boxes (
			id, workspace_id, name, target_amount, monthly_recharge, created_at, updated_at
		) VALUES (?, 'ws-runtime-jobs', ?, 0, 1000, ?, ?)
	`, boxID, boxID, createdAt, createdAt); err != nil {
		_ = dbs.Close()
		t.Fatalf("seed eligible runtime box: %v", err)
	}
	return dbs
}

type runtimePersistentEffects struct {
	scheduledStatus           string
	scheduledExecuted         bool
	accountBalance            int64
	historyCount              int
	notificationCount         int
	recurringTransactionCount int
	recurringStateCount       int
	sessionCount              int
	automaticRechargeCount    int
}

func openDormantRuntimeWithAllEligibleJobs(t *testing.T, path, boxID string) *database.RuntimeDBs {
	t.Helper()
	dbs := openDormantRuntimeWithEligibleBox(t, path, boxID)
	now := time.Now().UTC()
	recurringStart := now.AddDate(0, -2, 0).Unix()
	scheduledAt := now.Add(-24 * time.Hour).Unix()
	if _, err := dbs.DB.Exec(`
		INSERT INTO users (id, name, email, password_hash, created_at, updated_at)
		VALUES ('user-runtime-jobs', 'Runtime Jobs', 'runtime-jobs@example.test', 'hash', ?, ?);
		INSERT INTO workspace_members (workspace_id, user_id, role, joined_at)
		VALUES ('ws-runtime-jobs', 'user-runtime-jobs', 'ADMIN', ?);
		INSERT INTO accounts (id, workspace_id, name, type, initial_balance, current_balance, created_at, updated_at)
		VALUES ('account-runtime-jobs', 'ws-runtime-jobs', 'Runtime Account', 'CHECKING', 500000, 500000, ?, ?);
		INSERT INTO categories (id, workspace_id, name, icon, color, type, created_at)
		VALUES ('category-runtime-jobs', 'ws-runtime-jobs', 'Runtime Category', 'tag', '#000000', 'EXPENSE', ?);
		INSERT INTO transactions (
			id, workspace_id, user_id, account_id, category_id, type, amount, date,
			description, status, scheduled_payment_at, scheduled_payment_created_by,
			created_at, updated_at
		) VALUES (
			'tx-runtime-scheduled', 'ws-runtime-jobs', 'user-runtime-jobs',
			'account-runtime-jobs', 'category-runtime-jobs', 'EXPENSE', 1000, ?,
			'Runtime scheduled', 'pending', ?, 'user-runtime-jobs', ?, ?
		);
		INSERT INTO recurring_series (
			id, workspace_id, user_id, active, total_occurrences, created_at, updated_at
		) VALUES (
			'series-runtime-jobs', 'ws-runtime-jobs', 'user-runtime-jobs', 1, NULL, ?, ?
		);
		INSERT INTO recurring_rules (
			id, workspace_id, user_id, account_id, category_id, type, amount,
			description, start_date, frequency, default_payment_status, active,
			total_occurrences, series_id, effective_start_sequence, created_at, updated_at
		) VALUES (
			'rule-runtime-jobs', 'ws-runtime-jobs', 'user-runtime-jobs',
			'account-runtime-jobs', 'category-runtime-jobs', 'EXPENSE', 1500,
			'Runtime recurring', ?, 'MONTHLY', 'PENDING', 1, NULL,
			'series-runtime-jobs', 1, ?, ?
		);
		INSERT INTO transactions (
			id, workspace_id, user_id, account_id, category_id, type, amount, date,
			description, status, recurring_rule_id, recurrence_sequence, series_id,
			created_at, updated_at
		) VALUES (
			'tx-runtime-recurring-1', 'ws-runtime-jobs', 'user-runtime-jobs',
			'account-runtime-jobs', 'category-runtime-jobs', 'EXPENSE', 1500, ?,
			'Runtime recurring', 'pending', 'rule-runtime-jobs', 1,
			'series-runtime-jobs', ?, ?
		);
		INSERT INTO sessions (
			id, user_id, workspace_id, token_hash, expires_at, revoked_at, created_at
		) VALUES (
			'session-runtime-expired', 'user-runtime-jobs', 'ws-runtime-jobs',
			'runtime-expired-token-hash', 1, NULL, 1
		)
	`,
		now.Unix(), now.Unix(), now.Unix(), now.Unix(), now.Unix(), now.Unix(),
		scheduledAt, scheduledAt, now.Unix(), now.Unix(),
		now.Unix(), now.Unix(), recurringStart, now.Unix(), now.Unix(),
		recurringStart, now.Unix(), now.Unix(),
	); err != nil {
		_ = dbs.Close()
		t.Fatalf("seed runtime publication-boundary fixture: %v", err)
	}
	return dbs
}

func readRuntimePersistentEffects(t *testing.T, db *sql.DB, boxID string) runtimePersistentEffects {
	t.Helper()
	var effects runtimePersistentEffects
	if err := db.QueryRow(`
		SELECT status, scheduled_payment_executed_at IS NOT NULL
		FROM transactions
		WHERE id = 'tx-runtime-scheduled'
	`).Scan(&effects.scheduledStatus, &effects.scheduledExecuted); err != nil {
		t.Fatalf("read scheduled runtime effect: %v", err)
	}
	queries := []struct {
		name  string
		query string
		dest  any
	}{
		{"account balance", `SELECT current_balance FROM accounts WHERE id = 'account-runtime-jobs'`, &effects.accountBalance},
		{"automatic history", `SELECT COUNT(1) FROM transaction_status_history WHERE transaction_id = 'tx-runtime-scheduled' AND source = 'automatic'`, &effects.historyCount},
		{"scheduled notification", `SELECT COUNT(1) FROM user_notifications WHERE dedupe_key LIKE 'scheduled_payment:tx-runtime-scheduled:%'`, &effects.notificationCount},
		{"recurring transactions", `SELECT COUNT(1) FROM transactions WHERE recurring_rule_id = 'rule-runtime-jobs'`, &effects.recurringTransactionCount},
		{"recurring state", `SELECT COUNT(1) FROM recurring_materialization_state WHERE workspace_id = 'ws-runtime-jobs'`, &effects.recurringStateCount},
		{"expired session", `SELECT COUNT(1) FROM sessions WHERE id = 'session-runtime-expired'`, &effects.sessionCount},
		{"automatic recharge", `SELECT COUNT(1) FROM box_virtual_ledger WHERE box_id = ? AND automatic_recharge_month IS NOT NULL`, &effects.automaticRechargeCount},
	}
	for _, query := range queries {
		var err error
		if strings.Contains(query.query, "box_id = ?") {
			err = db.QueryRow(query.query, boxID).Scan(query.dest)
		} else {
			err = db.QueryRow(query.query).Scan(query.dest)
		}
		if err != nil {
			t.Fatalf("read %s runtime effect: %v", query.name, err)
		}
	}
	return effects
}

func waitForRuntimePersistentEffects(t *testing.T, db *sql.DB, boxID string) {
	t.Helper()
	deadline := time.NewTimer(10 * time.Second)
	defer deadline.Stop()
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for {
		effects := readRuntimePersistentEffects(t, db, boxID)
		if effects.scheduledStatus == "paid" && effects.scheduledExecuted &&
			effects.accountBalance == 499000 && effects.historyCount == 1 &&
			effects.notificationCount == 1 && effects.recurringTransactionCount > 1 &&
			effects.recurringStateCount == 1 && effects.sessionCount == 0 &&
			effects.automaticRechargeCount == 1 {
			return
		}
		select {
		case <-deadline.C:
			t.Fatalf("runtime positive effects did not converge: %+v", effects)
		case <-ticker.C:
		}
	}
}

func assertRuntimeAutomaticRechargeCount(t *testing.T, db *sql.DB, boxID string, want int) {
	t.Helper()
	var got int
	if err := db.QueryRow(`
		SELECT COUNT(1)
		FROM box_virtual_ledger
		WHERE box_id = ? AND automatic_recharge_month IS NOT NULL
	`, boxID).Scan(&got); err != nil {
		t.Fatalf("count runtime automatic recharges: %v", err)
	}
	if got != want {
		t.Fatalf("runtime automatic recharge count = %d, want %d", got, want)
	}
}

func waitForRuntimeAutomaticRecharge(t *testing.T, db *sql.DB, boxID string, want int) {
	t.Helper()
	deadline := time.NewTimer(10 * time.Second)
	defer deadline.Stop()
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for {
		var got int
		if err := db.QueryRow(`
			SELECT COUNT(1)
			FROM box_virtual_ledger
			WHERE box_id = ? AND automatic_recharge_month IS NOT NULL
		`, boxID).Scan(&got); err != nil {
			t.Fatalf("count activated runtime automatic recharges: %v", err)
		}
		if got == want {
			return
		}
		select {
		case <-deadline.C:
			t.Fatalf("runtime automatic recharge count = %d, want %d", got, want)
		case <-ticker.C:
		}
	}
}

func seedBindingIdentity(t *testing.T, db *sql.DB) string {
	t.Helper()
	_, _ = db.Exec(`DELETE FROM sessions; DELETE FROM workspace_members; DELETE FROM users; DELETE FROM workspaces`)
	if _, err := db.Exec(`
		INSERT INTO users (id, name, email, password_hash, created_at, updated_at) VALUES ('user-binding', 'Binding', 'binding@example.test', 'hash', unixepoch(), unixepoch());
		INSERT INTO workspaces (id, name, type, created_at, updated_at) VALUES ('ws-binding', 'Binding', 'personal', unixepoch(), unixepoch());
		INSERT INTO workspace_members (workspace_id, user_id, role, joined_at) VALUES ('ws-binding', 'user-binding', 'ADMIN', unixepoch())
	`); err != nil {
		t.Fatalf("seed binding identity: %v", err)
	}
	token, _, err := auth.NewService(db).CreateSession("user-binding", "ws-binding", time.Hour, false)
	if err != nil {
		t.Fatalf("create binding session: %v", err)
	}
	return token
}

func assertAuthenticated(t *testing.T, handler http.Handler, token string) {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/session/check", nil)
	req.AddCookie(&http.Cookie{Name: sessionCookieName, Value: token})
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)
	if rr.Code != http.StatusNoContent {
		t.Fatalf("auth request failed: code=%d body=%s", rr.Code, rr.Body.String())
	}
}

func assertBody(t *testing.T, handler http.Handler, path, want string) {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, path, nil)
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK || strings.TrimSpace(rr.Body.String()) != want {
		t.Fatalf("request %s code=%d body=%q want=%q", path, rr.Code, rr.Body.String(), want)
	}
}

func seedBindingInvoice(t *testing.T, db *sql.DB) {
	t.Helper()
	now := time.Now().Unix()
	if _, err := db.Exec(`
		INSERT INTO accounts (id, workspace_id, name, type, current_balance, created_at, updated_at)
		VALUES ('card-binding', 'ws-binding', 'Card', 'CREDIT_CARD', 0, ?, ?),
		       ('checking-binding', 'ws-binding', 'Checking', 'CHECKING', 500000, ?, ?);
		INSERT INTO credit_cards (id, account_id, closing_day, due_day, credit_limit)
		VALUES ('cc-binding', 'card-binding', 5, 12, 1000000);
		INSERT INTO invoices (id, account_id, reference, closing_date, due_date, status, paid_amount, created_at)
		VALUES ('invoice-binding', 'card-binding', '2026-08', ?, ?, 'CLOSED', 0, ?);
		INSERT INTO transactions (id, workspace_id, user_id, account_id, invoice_id, type, amount, date, description, status, installment_number, total_installments, created_at, updated_at)
		VALUES ('expense-binding', 'ws-binding', 'user-binding', 'card-binding', 'invoice-binding', 'EXPENSE', 10000, ?, 'Expense', 'paid', 1, 1, ?, ?)
	`, now, now, now, now+86400, now, now, now, now); err != nil {
		t.Fatalf("seed binding invoice: %v", err)
	}
}
