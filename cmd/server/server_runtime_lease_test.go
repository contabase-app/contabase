package main

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/contabase-app/contabase/internal/runtimelease"
)

type blockingBackupWriter struct {
	header  http.Header
	status  int
	once    sync.Once
	wrote   chan struct{}
	release chan struct{}
}

func TestB1BoundedAdmissionKeepsContinuousReadersAfterRefusal(t *testing.T) {
	for _, mode := range []string{"timeout", "cancel"} {
		t.Run(mode, func(t *testing.T) {
			t.Setenv("DATA_DIR", t.TempDir())
			g1 := createRestoreTestServerRuntime(t, filepath.Join(t.TempDir(), "live.db"), "G1")
			c := newServerRuntimeCoordinator(g1, nil)
			defer c.Close()
			c.replaceWait = 60 * time.Millisecond
			// Keep at least one reader across every handoff; no writer is queued.
			c.mu.RLock()
			defer c.mu.RUnlock()
			stop := make(chan struct{})
			var wg sync.WaitGroup
			var reads atomic.Int64
			for i := 0; i < 3; i++ {
				wg.Add(1)
				go func() {
					defer wg.Done()
					for {
						select {
						case <-stop:
							return
						default:
						}
						if err := c.ReadContext(context.Background(), func(*serverRuntime) error { reads.Add(1); return nil }); err != nil {
							return
						}
					}
				}()
			}
			defer func() { close(stop); wg.Wait() }()
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			if mode == "cancel" {
				timer := time.AfterFunc(15*time.Millisecond, cancel)
				defer timer.Stop()
			}
			var swaps atomic.Int64
			err := c.ReplaceContext(ctx, nil, func(old *serverRuntime) (*serverRuntime, error) { swaps.Add(1); return old, nil })
			if mode == "timeout" && !errors.Is(err, errServerRuntimeBusy) || mode == "cancel" && !errors.Is(err, context.Canceled) {
				t.Fatalf("admission result: %v", err)
			}
			before := reads.Load()
			readCtx, done := context.WithTimeout(context.Background(), time.Second)
			defer done()
			if err := c.ReadContext(readCtx, func(current *serverRuntime) error {
				if current != g1 {
					t.Fatal("refused import changed generation")
				}
				reads.Add(1)
				return nil
			}); err != nil {
				t.Fatal(err)
			}
			if reads.Load() <= before || swaps.Load() != 0 {
				t.Fatalf("readers blocked or abandoned writer ran: reads=%d swaps=%d", reads.Load(), swaps.Load())
			}
		})
	}
}

func newBlockingBackupWriter() *blockingBackupWriter {
	return &blockingBackupWriter{
		header:  make(http.Header),
		wrote:   make(chan struct{}),
		release: make(chan struct{}),
	}
}

func (w *blockingBackupWriter) Header() http.Header { return w.header }
func (w *blockingBackupWriter) WriteHeader(status int) {
	w.status = status
}
func (w *blockingBackupWriter) Write(p []byte) (int, error) {
	w.once.Do(func() { close(w.wrote) })
	<-w.release
	return len(p), nil
}

func waitForCoordinatorWriteAvailable(t *testing.T, coordinator *serverRuntimeCoordinator) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for {
		if coordinator.mu.TryLock() {
			coordinator.mu.Unlock()
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("coordinator did not become idle")
		}
		time.Sleep(time.Millisecond)
	}
}

func TestCoordinatorReplaceContextTimesOutWithoutSwap(t *testing.T) {
	g1 := createRestoreTestServerRuntime(t, filepath.Join(t.TempDir(), "g1.db"), "g1")
	coordinator := newServerRuntimeCoordinator(g1, nil)
	coordinator.replaceWait = 40 * time.Millisecond
	defer coordinator.Close()

	readEntered := make(chan struct{})
	releaseRead := make(chan struct{})
	readDone := make(chan struct{})
	go func() {
		defer close(readDone)
		_ = coordinator.Read(func(*serverRuntime) error {
			close(readEntered)
			<-releaseRead
			return nil
		})
	}()
	<-readEntered

	swapped := false
	err := coordinator.ReplaceContext(context.Background(), nil, func(old *serverRuntime) (*serverRuntime, error) {
		swapped = true
		return old, nil
	})
	if !errors.Is(err, errServerRuntimeBusy) {
		t.Fatalf("ReplaceContext error = %v, want errServerRuntimeBusy", err)
	}
	if swapped {
		t.Fatal("ReplaceContext swapped the runtime after the wait timeout")
	}

	close(releaseRead)
	<-readDone
	waitForCoordinatorWriteAvailable(t, coordinator)
	if err := coordinator.Read(func(current *serverRuntime) error {
		if current != g1 {
			t.Fatalf("current runtime changed after timed-out replace")
		}
		return nil
	}); err != nil {
		t.Fatalf("read runtime after timed-out replace: %v", err)
	}
}

func TestTimedOutReplaceDoesNotBlockNewReaders(t *testing.T) {
	g1 := createRestoreTestServerRuntime(t, filepath.Join(t.TempDir(), "g1.db"), "g1")
	coordinator := newServerRuntimeCoordinator(g1, nil)
	coordinator.replaceWait = 40 * time.Millisecond
	defer coordinator.Close()

	readEntered := make(chan struct{})
	releaseRead := make(chan struct{})
	readDone := make(chan struct{})
	go func() {
		defer close(readDone)
		_ = coordinator.Read(func(*serverRuntime) error {
			close(readEntered)
			<-releaseRead
			return nil
		})
	}()
	<-readEntered

	if err := coordinator.ReplaceContext(context.Background(), nil, func(old *serverRuntime) (*serverRuntime, error) {
		return old, nil
	}); !errors.Is(err, errServerRuntimeBusy) {
		t.Fatalf("ReplaceContext error = %v, want errServerRuntimeBusy", err)
	}

	readCtx, cancelRead := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancelRead()
	if err := coordinator.ReadContext(readCtx, func(*serverRuntime) error { return nil }); err != nil {
		t.Fatalf("new reader was blocked by a timed-out replace: %v", err)
	}
	close(releaseRead)
	<-readDone
	waitForCoordinatorWriteAvailable(t, coordinator)
}

func TestCoordinatorReplaceContextCanceledWithoutSwap(t *testing.T) {
	g1 := createRestoreTestServerRuntime(t, filepath.Join(t.TempDir(), "g1.db"), "g1")
	coordinator := newServerRuntimeCoordinator(g1, nil)
	coordinator.replaceWait = time.Second
	defer coordinator.Close()

	readEntered := make(chan struct{})
	releaseRead := make(chan struct{})
	readDone := make(chan struct{})
	go func() {
		defer close(readDone)
		_ = coordinator.Read(func(*serverRuntime) error {
			close(readEntered)
			<-releaseRead
			return nil
		})
	}()
	<-readEntered

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	swapped := false
	err := coordinator.ReplaceContext(ctx, nil, func(old *serverRuntime) (*serverRuntime, error) {
		swapped = true
		return old, nil
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("ReplaceContext error = %v, want context.Canceled", err)
	}
	if swapped {
		t.Fatal("ReplaceContext swapped the runtime after cancellation")
	}

	close(releaseRead)
	<-readDone
	waitForCoordinatorWriteAvailable(t, coordinator)
	if err := coordinator.Read(func(current *serverRuntime) error {
		if current != g1 {
			t.Fatalf("current runtime changed after canceled replace")
		}
		return nil
	}); err != nil {
		t.Fatalf("read runtime after canceled replace: %v", err)
	}
}

func TestCoordinatorReadContextTimesOutBehindWriter(t *testing.T) {
	g1 := createRestoreTestServerRuntime(t, filepath.Join(t.TempDir(), "g1.db"), "g1")
	g2 := createRestoreTestServerRuntime(t, filepath.Join(t.TempDir(), "g2.db"), "g2")
	coordinator := newServerRuntimeCoordinator(g1, nil)
	coordinator.replaceWait = time.Second
	defer coordinator.Close()

	swapEntered := make(chan struct{})
	releaseSwap := make(chan struct{})
	replaceDone := make(chan error, 1)
	go func() {
		replaceDone <- coordinator.ReplaceContext(context.Background(), nil, func(old *serverRuntime) (*serverRuntime, error) {
			close(swapEntered)
			<-releaseSwap
			if err := old.Close(); err != nil {
				return nil, err
			}
			return g2, nil
		})
	}()
	<-swapEntered

	readCtx, cancelRead := context.WithTimeout(context.Background(), 40*time.Millisecond)
	defer cancelRead()
	err := coordinator.ReadContext(readCtx, func(*serverRuntime) error {
		t.Fatal("read callback ran after its context expired")
		return nil
	})
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("ReadContext error = %v, want context.DeadlineExceeded", err)
	}

	close(releaseSwap)
	if err := <-replaceDone; err != nil {
		t.Fatalf("replace after read timeout: %v", err)
	}
	waitForCoordinatorWriteAvailable(t, coordinator)
	if err := coordinator.Read(func(current *serverRuntime) error {
		if current != g2 {
			t.Fatalf("current runtime = %p, want g2", current)
		}
		return nil
	}); err != nil {
		t.Fatalf("read runtime after replacement: %v", err)
	}
}

func TestCoordinatorConcurrentReplaceReturnsBusy(t *testing.T) {
	g1 := createRestoreTestServerRuntime(t, filepath.Join(t.TempDir(), "g1.db"), "g1")
	g2 := createRestoreTestServerRuntime(t, filepath.Join(t.TempDir(), "g2.db"), "g2")
	coordinator := newServerRuntimeCoordinator(g1, nil)
	coordinator.replaceWait = 40 * time.Millisecond
	defer coordinator.Close()

	firstEntered := make(chan struct{})
	releaseFirst := make(chan struct{})
	firstDone := make(chan error, 1)
	go func() {
		firstDone <- coordinator.ReplaceContext(context.Background(), nil, func(old *serverRuntime) (*serverRuntime, error) {
			close(firstEntered)
			<-releaseFirst
			if err := old.Close(); err != nil {
				return nil, err
			}
			return g2, nil
		})
	}()
	<-firstEntered

	secondSwapped := false
	err := coordinator.ReplaceContext(context.Background(), nil, func(old *serverRuntime) (*serverRuntime, error) {
		secondSwapped = true
		return old, nil
	})
	if !errors.Is(err, errServerRuntimeBusy) {
		t.Fatalf("second ReplaceContext error = %v, want errServerRuntimeBusy", err)
	}
	if secondSwapped {
		t.Fatal("second ReplaceContext swapped after the wait timeout")
	}
	close(releaseFirst)
	if err := <-firstDone; err != nil {
		t.Fatalf("first ReplaceContext: %v", err)
	}
	waitForCoordinatorWriteAvailable(t, coordinator)
	if err := coordinator.Read(func(current *serverRuntime) error {
		if current != g2 {
			t.Fatalf("current runtime = %p, want g2", current)
		}
		return nil
	}); err != nil {
		t.Fatalf("read runtime after concurrent replacement: %v", err)
	}
}

func TestMiddlewareEarlyReleaseUnblocksReplaceDuringStreaming(t *testing.T) {
	g1 := createRestoreTestServerRuntime(t, filepath.Join(t.TempDir(), "g1.db"), "g1")
	g2 := createRestoreTestServerRuntime(t, filepath.Join(t.TempDir(), "g2.db"), "g2")
	coordinator := newServerRuntimeCoordinator(g1, nil)
	coordinator.replaceWait = 40 * time.Millisecond
	defer coordinator.Close()

	streamEntered := make(chan struct{})
	releaseStream := make(chan struct{})
	streamDone := make(chan struct{})
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		runtimelease.Release(r.Context())
		close(streamEntered)
		<-releaseStream
		w.WriteHeader(http.StatusOK)
	})
	wrapped := coordinator.Middleware(handler, nil)
	go func() {
		defer close(streamDone)
		wrapped.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/stream", nil))
	}()
	<-streamEntered

	if err := coordinator.ReplaceContext(context.Background(), nil, func(old *serverRuntime) (*serverRuntime, error) {
		if err := old.Close(); err != nil {
			return nil, err
		}
		return g2, nil
	}); err != nil {
		t.Fatalf("ReplaceContext during early-released stream: %v", err)
	}
	close(releaseStream)
	<-streamDone
	if err := coordinator.Read(func(current *serverRuntime) error {
		if current != g2 {
			t.Fatalf("current runtime = %p, want g2", current)
		}
		return nil
	}); err != nil {
		t.Fatalf("read runtime after replacement: %v", err)
	}
}

func TestAdminBackupExportReleasesLeaseBeforeSlowStream(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("DATA_DIR", dir)
	dbPath := filepath.Join(dir, "export.db")
	dbs := createRestoreTestRuntimeDBs(t, dbPath, "export")
	current := &serverRuntime{DBs: dbs}
	g2 := createRestoreTestServerRuntime(t, filepath.Join(t.TempDir(), "g2.db"), "g2")
	coordinator, wrapped, req := backupAuditTestChain(t, current, dbPath, nil)
	coordinator.replaceWait = 100 * time.Millisecond
	defer coordinator.Close()

	writer := newBlockingBackupWriter()
	done := make(chan struct{})
	go func() {
		defer close(done)
		wrapped.ServeHTTP(writer, req)
	}()
	select {
	case <-writer.wrote:
	case <-time.After(2 * time.Second):
		t.Fatal("export did not start streaming")
	}

	if err := coordinator.ReplaceContext(context.Background(), nil, func(old *serverRuntime) (*serverRuntime, error) {
		if err := old.Close(); err != nil {
			return nil, err
		}
		return g2, nil
	}); err != nil {
		t.Fatalf("ReplaceContext during slow export: %v", err)
	}
	close(writer.release)
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("export did not finish after releasing the slow writer")
	}
	if err := coordinator.Read(func(current *serverRuntime) error {
		if current != g2 {
			t.Fatalf("current runtime = %p, want g2", current)
		}
		return nil
	}); err != nil {
		t.Fatalf("read runtime after export replacement: %v", err)
	}
}

func TestMiddlewareWithoutEarlyReleaseKeepsLeaseUntilStreamEnds(t *testing.T) {
	g1 := createRestoreTestServerRuntime(t, filepath.Join(t.TempDir(), "g1.db"), "g1")
	coordinator := newServerRuntimeCoordinator(g1, nil)
	coordinator.replaceWait = 40 * time.Millisecond
	defer coordinator.Close()

	streamEntered := make(chan struct{})
	releaseStream := make(chan struct{})
	streamDone := make(chan struct{})
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		close(streamEntered)
		<-releaseStream
		w.WriteHeader(http.StatusOK)
	})
	wrapped := coordinator.Middleware(handler, nil)
	go func() {
		defer close(streamDone)
		wrapped.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/stream", nil))
	}()
	<-streamEntered

	err := coordinator.ReplaceContext(context.Background(), nil, func(old *serverRuntime) (*serverRuntime, error) {
		return old, nil
	})
	if !errors.Is(err, errServerRuntimeBusy) {
		t.Fatalf("ReplaceContext error = %v, want errServerRuntimeBusy while the lease is held", err)
	}
	close(releaseStream)
	<-streamDone
	waitForCoordinatorWriteAvailable(t, coordinator)
}
