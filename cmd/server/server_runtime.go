package main

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/contabase-app/contabase/internal/auth"
	"github.com/contabase-app/contabase/internal/database"
	"github.com/contabase-app/contabase/internal/handlers"
	"github.com/contabase-app/contabase/internal/runtimelease"
	"github.com/google/uuid"
)

var (
	errServerRuntimeUnavailable  = errors.New("server runtime unavailable")
	errRestoreRevalidationDenied = errors.New("restore revalidation denied")
	errServerRuntimeBusy         = errors.New("server runtime busy")
)

const (
	serverRuntimeStageDBs       = "runtime_dbs"
	serverRuntimeStageFallback  = "fallback"
	serverRuntimeStageScheduled = "scheduled"
	serverRuntimeStageRecurring = "recurring"
	serverRuntimeStageSessions  = "sessions"

	serverRuntimeReplaceWait    = 5 * time.Second
	serverRuntimeImportReadWait = 5 * time.Second
)

type serverRuntimeStartHooks struct {
	beforeActivationCheckpoint func(string) error
	afterRuntimeDBsActivate    func()
	onResourceStart            func(string, *sql.DB)
	onResourceStop             func(string)
	onSessionCleanup           func(error)
}

type serverRuntimeOptions struct {
	scheduledInterval time.Duration
	recurringInterval time.Duration
	sessionInterval   time.Duration
	hooks             *serverRuntimeStartHooks
}

type serverRuntime struct {
	DBs          *database.RuntimeDBs
	generationID string

	cancel        context.CancelFunc
	fallback      *handlers.RecurringMaterializationFallback
	scheduledDone <-chan struct{}
	recurringDone <-chan struct{}
	sessionsDone  <-chan struct{}

	closeOnce sync.Once
	closeErr  error
	hooks     *serverRuntimeStartHooks
}

func startServerRuntime(dbs *database.RuntimeDBs, options serverRuntimeOptions) (*serverRuntime, error) {
	if dbs == nil || dbs.DB == nil || dbs.Immediate == nil {
		return nil, fmt.Errorf("invalid runtime database owner")
	}
	if options.scheduledInterval <= 0 {
		options.scheduledInterval = 5 * time.Minute
	}
	if options.recurringInterval <= 0 {
		options.recurringInterval = 24 * time.Hour
	}
	if options.sessionInterval <= 0 {
		options.sessionInterval = 6 * time.Hour
	}

	ctx, cancel := context.WithCancel(context.Background())
	runtime := &serverRuntime{DBs: dbs, cancel: cancel, hooks: options.hooks}
	fail := func(stage string) error {
		if options.hooks == nil || options.hooks.beforeActivationCheckpoint == nil {
			return nil
		}
		return options.hooks.beforeActivationCheckpoint(stage)
	}
	if err := fail(serverRuntimeStageDBs); err != nil {
		_ = runtime.Close()
		return nil, err
	}

	if err := fail(serverRuntimeStageFallback); err != nil {
		_ = runtime.Close()
		return nil, err
	}
	if err := fail(serverRuntimeStageScheduled); err != nil {
		_ = runtime.Close()
		return nil, err
	}
	if err := fail(serverRuntimeStageRecurring); err != nil {
		_ = runtime.Close()
		return nil, err
	}
	if err := fail(serverRuntimeStageSessions); err != nil {
		_ = runtime.Close()
		return nil, err
	}
	// Restored databases must pass the same credential containment as cold starts.
	if err := disableLegacySeedCredentials(dbs.Immediate); err != nil {
		_ = runtime.Close()
		return nil, fmt.Errorf("disable legacy seed credentials: %w", err)
	}
	// RuntimeDBs and every external background resource remain dormant through
	// all fallible construction checkpoints. Activate is the final operation
	// that can reject this generation.
	if err := dbs.Activate(); err != nil {
		_ = runtime.Close()
		return nil, err
	}
	if options.hooks != nil && options.hooks.afterRuntimeDBsActivate != nil {
		options.hooks.afterRuntimeDBsActivate()
	}

	// From this point to binding/publication, every primitive is non-fallible:
	// each starter only installs ownership and returns its lifecycle handle.
	runtime.fallback = handlers.InitRecurringMaterializationFallback(dbs.Immediate)
	runtime.resourceStarted(serverRuntimeStageFallback, dbs.Immediate)
	// Reserve the writer before scheduled payments reload eligibility. A deferred
	// read-to-write upgrade can return plain SQLITE_BUSY while another job writes.
	runtime.scheduledDone = handlers.StartScheduledPaymentExecutor(ctx, dbs.Immediate, options.scheduledInterval)
	runtime.resourceStarted(serverRuntimeStageScheduled, dbs.Immediate)
	runtime.recurringDone = handlers.StartRecurringMaterializationJob(ctx, dbs.Immediate, options.recurringInterval)
	runtime.resourceStarted(serverRuntimeStageRecurring, dbs.Immediate)
	var onSessionCleanup func(error)
	if options.hooks != nil {
		onSessionCleanup = options.hooks.onSessionCleanup
	}
	runtime.sessionsDone = startSessionCleanupJob(ctx, dbs.DB, options.sessionInterval, onSessionCleanup)
	runtime.resourceStarted(serverRuntimeStageSessions, dbs.DB)
	return runtime, nil
}

func (r *serverRuntime) resourceStarted(name string, db *sql.DB) {
	if r.hooks != nil && r.hooks.onResourceStart != nil {
		r.hooks.onResourceStart(name, db)
	}
}

func (r *serverRuntime) resourceStopped(name string) {
	if r.hooks != nil && r.hooks.onResourceStop != nil {
		r.hooks.onResourceStop(name)
	}
}

func startSessionCleanupJob(ctx context.Context, db *sql.DB, interval time.Duration, onRun func(error)) <-chan struct{} {
	done := make(chan struct{})
	if db == nil {
		close(done)
		return done
	}
	service := auth.NewService(db)
	go func() {
		defer close(done)
		cleanup := func() {
			_, _, err := service.CleanupExpiredSessions(time.Now())
			if onRun != nil {
				onRun(err)
			}
		}
		cleanup()
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				cleanup()
			}
		}
	}()
	return done
}

func waitDone(done <-chan struct{}) {
	if done != nil {
		<-done
	}
}

func (r *serverRuntime) Close() error {
	if r == nil {
		return nil
	}
	r.closeOnce.Do(func() {
		if r.cancel != nil {
			r.cancel()
		}
		if r.fallback != nil {
			r.fallback.Close()
			r.resourceStopped(serverRuntimeStageFallback)
		}
		if r.scheduledDone != nil {
			waitDone(r.scheduledDone)
			r.resourceStopped(serverRuntimeStageScheduled)
		}
		if r.recurringDone != nil {
			waitDone(r.recurringDone)
			r.resourceStopped(serverRuntimeStageRecurring)
		}
		if r.sessionsDone != nil {
			waitDone(r.sessionsDone)
			r.resourceStopped(serverRuntimeStageSessions)
		}
		if r.DBs != nil {
			r.closeErr = r.DBs.Close()
		}
	})
	return r.closeErr
}

type serverRuntimeCoordinator struct {
	mu          sync.RWMutex
	current     *serverRuntime
	bind        func(*serverRuntime)
	replaceWait time.Duration
}

func newServerRuntimeCoordinator(current *serverRuntime, bind func(*serverRuntime)) *serverRuntimeCoordinator {
	if current != nil && current.generationID == "" {
		current.generationID = uuid.NewString()
	}
	coordinator := &serverRuntimeCoordinator{
		current:     current,
		bind:        bind,
		replaceWait: serverRuntimeReplaceWait,
	}
	if current != nil && bind != nil {
		bind(current)
	}
	return coordinator
}

func (c *serverRuntimeCoordinator) Read(fn func(*serverRuntime) error) error {
	if c == nil {
		return errServerRuntimeUnavailable
	}
	c.mu.RLock()
	defer c.mu.RUnlock()
	if c.current == nil || c.current.DBs == nil || c.current.DBs.DB == nil {
		return errServerRuntimeUnavailable
	}
	if fn == nil {
		return nil
	}
	return fn(c.current)
}

// ReadContext acquires the shared runtime lease with a cancellation boundary.
// A request that gives up while waiting must not keep a latent reader behind a
// waiting writer; the cleanup goroutine releases the lease if it is acquired
// after the caller has already returned.
func (c *serverRuntimeCoordinator) ReadContext(ctx context.Context, fn func(*serverRuntime) error) error {
	if c == nil {
		return errServerRuntimeUnavailable
	}
	if ctx == nil {
		ctx = context.Background()
	}
	acquired := make(chan struct{})
	go func() {
		c.mu.RLock()
		close(acquired)
	}()
	select {
	case <-acquired:
	case <-ctx.Done():
		go func() {
			<-acquired
			c.mu.RUnlock()
		}()
		return ctx.Err()
	}
	defer c.mu.RUnlock()
	if err := ctx.Err(); err != nil {
		return err
	}
	if c.current == nil || c.current.DBs == nil || c.current.DBs.DB == nil {
		return errServerRuntimeUnavailable
	}
	if fn == nil {
		return nil
	}
	if err := fn(c.current); err != nil {
		return err
	}
	return ctx.Err()
}

// Replace uses the same bounded admission as ReplaceContext for callers
// without a request context. Production restore paths use
// ReplaceContext so a client disconnect cannot leave an import queued forever.
func (c *serverRuntimeCoordinator) Replace(revalidate func(*serverRuntime) error, swap func(*serverRuntime) (*serverRuntime, error)) error {
	return c.ReplaceContext(context.Background(), revalidate, swap)
}

// ReplaceContext waits a bounded amount of time for exclusive runtime access.
// It probes with TryLock so a timed-out caller leaves no queued writer behind:
// a pending Lock call would keep blocking new readers even after the request
// had already received its bounded failure. Under continuous reader traffic the
// writer may time out with errServerRuntimeBusy, which is preferable to an
// unbounded queue.
func (c *serverRuntimeCoordinator) ReplaceContext(ctx context.Context, revalidate func(*serverRuntime) error, swap func(*serverRuntime) (*serverRuntime, error)) error {
	if c == nil || swap == nil {
		return errServerRuntimeUnavailable
	}
	if ctx == nil {
		ctx = context.Background()
	}
	wait := c.replaceWait
	if wait <= 0 {
		wait = serverRuntimeReplaceWait
	}
	start := time.Now()
	deadline := start.Add(wait)
	ticker := time.NewTicker(5 * time.Millisecond)
	defer ticker.Stop()
	for {
		if err := ctx.Err(); err != nil {
			slog.Info("server_runtime_replace_canceled", "wait_ms", time.Since(start).Milliseconds())
			return err
		}
		if !time.Now().Before(deadline) {
			slog.Warn("server_runtime_replace_lock_timeout", "wait_ms", time.Since(start).Milliseconds())
			return errServerRuntimeBusy
		}
		if c.mu.TryLock() {
			break
		}
		select {
		case <-ctx.Done():
			slog.Info("server_runtime_replace_canceled", "wait_ms", time.Since(start).Milliseconds())
			return ctx.Err()
		case <-ticker.C:
		}
	}
	defer c.mu.Unlock()
	slog.Info("server_runtime_replace_lock_acquired", "wait_ms", time.Since(start).Milliseconds())
	if err := ctx.Err(); err != nil {
		slog.Info("server_runtime_replace_canceled_after_lock", "wait_ms", time.Since(start).Milliseconds())
		return err
	}
	if c.current == nil {
		return errServerRuntimeUnavailable
	}
	old := c.current
	if revalidate != nil {
		if err := revalidate(old); err != nil {
			return err
		}
	}
	if err := ctx.Err(); err != nil {
		slog.Info("server_runtime_replace_canceled_before_commit", "wait_ms", time.Since(start).Milliseconds())
		return err
	}
	c.current = nil
	next, swapErr := swap(old)
	if next != nil {
		if next.generationID == "" {
			next.generationID = uuid.NewString()
		}
		if c.bind != nil {
			c.bind(next)
		}
		c.current = next
	}
	return swapErr
}

func (c *serverRuntimeCoordinator) Ping() error {
	return c.Read(func(runtime *serverRuntime) error {
		return runtime.DBs.DB.Ping()
	})
}

func runtimeLeaseHeld(ctx context.Context) bool {
	return runtimelease.Held(ctx)
}

func (c *serverRuntimeCoordinator) Middleware(next http.Handler, bypass func(*http.Request) bool) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if bypass != nil && bypass(r) {
			next.ServeHTTP(w, r)
			return
		}
		if runtimeLeaseHeld(r.Context()) {
			http.Error(w, "runtime lease already held", http.StatusInternalServerError)
			return
		}
		c.mu.RLock()
		if c.current == nil || c.current.DBs == nil || c.current.DBs.DB == nil {
			c.mu.RUnlock()
			http.Error(w, "service unavailable", http.StatusServiceUnavailable)
			return
		}
		release := sync.OnceFunc(c.mu.RUnlock)
		ctx := runtimelease.WithRelease(r.Context(), release)
		ctx = runtimelease.WithGeneration(ctx, c.current.generationID)
		defer runtimelease.Release(ctx)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

func (c *serverRuntimeCoordinator) Close() error {
	if c == nil {
		return nil
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	current := c.current
	c.current = nil
	if current == nil {
		return nil
	}
	return current.Close()
}

func isRuntimeLeaseBypassRequest(r *http.Request) bool {
	if r == nil {
		return false
	}
	switch r.URL.Path {
	case "/health", "/setup/restaurar", "/admin/backups/importar", "/favicon.ico", "/manifest.json":
		return true
	}
	return strings.HasPrefix(r.URL.Path, "/assets/")
}
