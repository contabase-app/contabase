package main

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/contabase-app/contabase/internal/database"
)

func TestB1RestoreFaultsReportTruthfulRollback(t *testing.T) {
	for _, mode := range []string{"first_rename", "second_rename", "live_sidecar_once", "candidate_sidecar", "rollback_sidecar", "rollback_remove", "rollback_rename_copy", "rollback_copy_failed", "reopen", "reopen_both", "migration_partial", "runtime_start"} {
		t.Run(mode, func(t *testing.T) {
			root := t.TempDir()
			t.Setenv("DATA_DIR", root)
			live := filepath.Join(root, "live.db")
			candidate := filepath.Join(root, "candidate.db")
			current := createRestoreTestServerRuntime(t, live, "original")
			t.Cleanup(func() { _ = current.Close() })
			createRestoreTestDB(t, candidate, "candidate").Close()
			fault := errors.New("injected restore failure")
			ops := &adminRestoreFileOps{}
			renames, sidecarCalls := 0, 0
			ops.rename = func(from, to string) error {
				renames++
				if mode == "first_rename" && renames == 1 || mode == "second_rename" && renames == 2 || (mode == "rollback_rename_copy" || mode == "rollback_copy_failed") && renames >= 2 {
					return fault
				}
				return os.Rename(from, to)
			}
			ops.remove = func(path string) error {
				if path == live+"-wal" {
					sidecarCalls++
					if mode == "rollback_sidecar" || mode == "live_sidecar_once" && sidecarCalls == 1 {
						return fault
					}
				}
				if path == candidate+"-shm" && mode == "candidate_sidecar" {
					return fault
				}
				if path == live && mode == "rollback_remove" {
					return fault
				}
				return os.Remove(path)
			}
			if mode == "rollback_copy_failed" {
				ops.copy = func(string, string) error { return fault }
			}
			opens := 0
			openFn := func(url string) (*database.RuntimeDBs, error) {
				opens++
				if mode == "reopen_both" || (mode == "reopen" || mode == "rollback_remove") && opens == 1 {
					return nil, fault
				}
				if mode == "migration_partial" && opens == 1 {
					dbs, err := database.OpenRuntimeDBs(url)
					if err != nil {
						return nil, err
					}
					_ = dbs.Close()
					return nil, fault
				}
				return openRestoreTestRuntimeDBs(url)
			}
			starts := 0
			startFn := func(dbs *database.RuntimeDBs) (*serverRuntime, error) {
				starts++
				if mode == "runtime_start" && starts == 1 {
					return nil, fault
				}
				return startRestoreTestServerRuntime(dbs)
			}
			result, next, err := restoreSQLiteBackupFromFileWithOps(context.Background(), current, "file:"+live, live, candidate, openFn, startFn, validateSQLiteBackupCandidate, ops)
			if err == nil {
				if next != nil {
					_ = next.Close()
				}
				t.Fatal("fault did not fail restore")
			}
			if next != nil {
				defer next.Close()
			}
			failed := mode == "rollback_sidecar" || mode == "rollback_remove" || mode == "rollback_copy_failed" || mode == "reopen_both"
			if failed {
				if result.RolledBack || result.RollbackState != adminRollbackFailed || result.RuntimeState != adminRuntimeUnknown || next != nil {
					t.Fatalf("false rollback success: %+v next=%v err=%v", result, next, err)
				}
				message := adminRestoreFailureMessage(result)
				if strings.Contains(message, "foi revertida") || !strings.Contains(message, "verificação manual") {
					t.Fatal(message)
				}
			} else if mode == "first_rename" {
				if result.CommitmentReached || result.RolledBack || result.RollbackState != adminRollbackNotStarted || result.RuntimeState != adminRuntimeOriginal {
					t.Fatal(result)
				}
			} else if !result.RolledBack || result.RollbackState != adminRollbackCompleted || result.RuntimeState != adminRuntimeOriginal || next == nil {
				t.Fatalf("missing successful rollback: %+v %v", result, err)
			}
			if !failed || mode == "reopen_both" {
				assertRestoreMarker(t, live, "original")
			}
			if result.PreRestoreBackupPath != "" {
				assertRestoreMarker(t, result.PreRestoreBackupPath, "original")
			}
			if next != nil {
				if err := validateSQLiteIntegrity(next.DBs.DB); err != nil {
					t.Fatal(err)
				}
				if err := validateSQLiteForeignKeys(next.DBs.DB); err != nil {
					t.Fatal(err)
				}
			}
		})
	}
}

func TestB1RestoreCancellationAtCommitBoundaries(t *testing.T) {
	for _, stage := range []string{"current_server_runtime_closed", "pre_restore_backup_created", "before_commit", "commitment_point", "candidate_installed"} {
		t.Run(stage, func(t *testing.T) {
			root := t.TempDir()
			t.Setenv("DATA_DIR", root)
			live := filepath.Join(root, "live.db")
			candidate := filepath.Join(root, "candidate.db")
			current := createRestoreTestServerRuntime(t, live, "original")
			createRestoreTestDB(t, candidate, "candidate").Close()
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			ops := &adminRestoreFileOps{checkpoint: func(at string) {
				if at == stage {
					cancel()
				}
			}}
			result, next, err := restoreSQLiteBackupFromFileWithOps(ctx, current, "file:"+live, live, candidate, openRestoreTestRuntimeDBs, startRestoreTestServerRuntime, validateSQLiteBackupCandidate, ops)
			if next == nil {
				t.Fatalf("no recovered runtime: %+v %v", result, err)
			}
			defer next.Close()
			after := stage == "commitment_point" || stage == "candidate_installed"
			if after {
				if err != nil || !result.CommitmentReached || result.RuntimeState != adminRuntimeRestored {
					t.Fatalf("postcommit cancellation: %+v %v", result, err)
				}
				assertRestoreMarker(t, live, "candidate")
			} else {
				if !errors.Is(err, context.Canceled) || result.CommitmentReached || result.RuntimeState != adminRuntimeOriginal {
					t.Fatalf("precommit cancellation: %+v %v", result, err)
				}
				assertRestoreMarker(t, live, "original")
			}
		})
	}
}

func TestB1CancellationAfterCommitStillRollsBack(t *testing.T) {
	root := t.TempDir()
	t.Setenv("DATA_DIR", root)
	live := filepath.Join(root, "live.db")
	candidate := filepath.Join(root, "candidate.db")
	current := createRestoreTestServerRuntime(t, live, "original")
	createRestoreTestDB(t, candidate, "candidate").Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ops := &adminRestoreFileOps{checkpoint: func(stage string) {
		if stage == "commitment_point" {
			cancel()
		}
	}, rename: func(from, to string) error {
		if from == candidate {
			return errors.New("second rename failed")
		}
		return os.Rename(from, to)
	}}
	result, next, err := restoreSQLiteBackupFromFileWithOps(ctx, current, "file:"+live, live, candidate, openRestoreTestRuntimeDBs, startRestoreTestServerRuntime, validateSQLiteBackupCandidate, ops)
	if err == nil || next == nil || !result.RolledBack || result.RollbackState != adminRollbackCompleted {
		t.Fatalf("postcommit rollback: %+v %v", result, err)
	}
	defer next.Close()
	assertRestoreMarker(t, live, "original")
}

func TestB1HTTPDisconnectAtCommitBoundary(t *testing.T) {
	for _, stage := range []string{"before_commit", "commitment_point"} {
		t.Run(stage, func(t *testing.T) {
			root := t.TempDir()
			t.Setenv("DATA_DIR", root)
			live := filepath.Join(root, "live.db")
			candidate := filepath.Join(root, "candidate.db")
			current := createRestoreTestServerRuntime(t, live, "original")
			createRestoreTestDB(t, candidate, "candidate").Close()
			c := newServerRuntimeCoordinator(current, nil)
			defer c.Close()
			reached := make(chan struct{})
			finished := make(chan adminRestoreResult, 1)
			serverErr := make(chan error, 1)
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var result adminRestoreResult
				err := c.ReplaceContext(r.Context(), nil, func(old *serverRuntime) (*serverRuntime, error) {
					ops := &adminRestoreFileOps{checkpoint: func(at string) {
						if at == stage {
							close(reached)
							<-r.Context().Done()
						}
					}}
					var next *serverRuntime
					var err error
					result, next, err = restoreSQLiteBackupFromFileWithOps(r.Context(), old, "file:"+live, live, candidate, openRestoreTestRuntimeDBs, startRestoreTestServerRuntime, validateSQLiteBackupCandidate, ops)
					return next, err
				})
				serverErr <- err
				finished <- result
				w.WriteHeader(http.StatusOK)
			}))
			defer srv.Close()
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			req, err := http.NewRequestWithContext(ctx, http.MethodPost, srv.URL, nil)
			if err != nil {
				t.Fatal(err)
			}
			clientDone := make(chan error, 1)
			go func() {
				resp, err := srv.Client().Do(req)
				if resp != nil {
					_ = resp.Body.Close()
				}
				clientDone <- err
			}()
			select {
			case <-reached:
			case <-time.After(3 * time.Second):
				cancel()
				t.Fatal("server did not reach boundary")
			}
			cancel()
			if err := <-clientDone; !errors.Is(err, context.Canceled) {
				t.Fatalf("client disconnect: %v", err)
			}
			var result adminRestoreResult
			select {
			case result = <-finished:
			case <-time.After(3 * time.Second):
				t.Fatal("server did not complete recovery after disconnect")
			}
			err = <-serverErr
			if stage == "before_commit" {
				if !errors.Is(err, context.Canceled) || result.CommitmentReached {
					t.Fatalf("late replacement after disconnect: %+v %v", result, err)
				}
				assertRestoreMarker(t, live, "original")
			} else {
				if err != nil || !result.CommitmentReached || result.RuntimeState != adminRuntimeRestored {
					t.Fatalf("postcommit did not finish: %+v %v", result, err)
				}
				assertRestoreMarker(t, live, "candidate")
			}
		})
	}
}
