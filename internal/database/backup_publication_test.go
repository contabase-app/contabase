package database

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"sync"
	"syscall"
	"testing"
	"time"
)

func TestBackupPublicationF05R01DoubleFailureRequiresReconfirmation(t *testing.T) {
	db, source := openDailyBackupTestDB(t)
	dir := t.TempDir()
	day := time.Date(2026, 9, 10, 12, 0, 0, 0, time.UTC)
	insertDailyBackupMarker(t, db, "previous")
	if err := ensureDailyAutomaticBackup(db, source, dir, day.AddDate(0, 0, -1), 7); err != nil {
		t.Fatal(err)
	}
	previous := filepath.Join(dir, dailyAutomaticBackupName(day.AddDate(0, 0, -1)))
	previousBytes := readDailyBackupBytes(t, previous)
	insertDailyBackupMarker(t, db, "captured")
	dest := filepath.Join(dir, dailyAutomaticBackupName(day))
	var events []string
	var syncCalls int
	ops := &backupPublicationOps{
		link: func(src, dst string) error { events = append(events, "link"); return os.Link(src, dst) },
		syncDir: func(dir string) error {
			syncCalls++
			if syncCalls == 1 {
				return syncDirectory(dir)
			}
			events = append(events, "sync-failed")
			return syscall.EIO
		},
		remove: func(path string) error {
			events = append(events, "remove-failed")
			if path != dest {
				t.Fatalf("rollback attempted to remove %s", path)
			}
			return syscall.EACCES
		},
	}
	err := ensureDailyAutomaticBackupWithPublication(context.Background(), db, source, dir, day, 7, ops)
	if !errors.Is(err, syscall.EIO) || !errors.Is(err, syscall.EACCES) {
		t.Fatalf("double failure missing: %v", err)
	}
	if !reflect.DeepEqual(events, []string{"link", "sync-failed", "remove-failed"}) {
		t.Fatalf("sequence=%v", events)
	}
	remnant := readDailyBackupBytes(t, dest)
	if got := dailyBackupMarkerCount(t, dest); got != 2 {
		t.Fatalf("remnant generation=%d", got)
	}
	if !bytes.Equal(previousBytes, readDailyBackupBytes(t, previous)) {
		t.Fatal("previous backup changed")
	}

	// Simulate a later invocation with no retained in-memory publication state.
	// It must attempt file and directory syncs, and fail while durability fails.
	var confirmations []string
	retry := &backupPublicationOps{
		syncFile: func(f *os.File) error { confirmations = append(confirmations, "file"); return f.Sync() },
		syncDir:  func(string) error { confirmations = append(confirmations, "directory-failed"); return syscall.EIO },
		link:     func(string, string) error { t.Fatal("retry overwrote existing candidate"); return nil },
		remove:   func(string) error { t.Fatal("retry removed existing candidate"); return nil },
	}
	if err := ensureDailyAutomaticBackupWithPublication(context.Background(), db, source, dir, day, 7, retry); !errors.Is(err, syscall.EIO) {
		t.Fatalf("retry silently accepted remnant: %v", err)
	}
	if !reflect.DeepEqual(confirmations, []string{"file", "file", "directory-failed"}) {
		t.Fatalf("confirmation=%v", confirmations)
	}
	if !bytes.Equal(remnant, readDailyBackupBytes(t, dest)) {
		t.Fatal("failed retry changed bytes")
	}

	// Recovery proves durability now, without claiming the first attempt succeeded.
	confirmations = nil
	retry.syncDir = func(path string) error {
		confirmations = append(confirmations, "directory")
		return syncDirectory(path)
	}
	if err := ensureDailyAutomaticBackupWithPublication(context.Background(), db, source, dir, day, 7, retry); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(confirmations, []string{"file", "file", "directory"}) {
		t.Fatalf("recovery confirmation=%v", confirmations)
	}
	if !bytes.Equal(remnant, readDailyBackupBytes(t, dest)) {
		t.Fatal("recovery recaptured or changed bytes")
	}
	insertDailyBackupMarker(t, db, "next-day")
	if err := ensureDailyAutomaticBackup(db, source, dir, day.AddDate(0, 0, 1), 7); err != nil {
		t.Fatal(err)
	}
	if got := dailyBackupMarkerCount(t, filepath.Join(dir, dailyAutomaticBackupName(day.AddDate(0, 0, 1)))); got != 3 {
		t.Fatalf("next day generation=%d", got)
	}
	if !bytes.Equal(previousBytes, readDailyBackupBytes(t, previous)) {
		t.Fatal("previous backup changed after recovery")
	}
}

func TestBackupPublicationRollbackAndNoReplace(t *testing.T) {
	for _, rollbackFails := range []bool{false, true} {
		t.Run(map[bool]string{false: "rollback-success", true: "rollback-failure"}[rollbackFails], func(t *testing.T) {
			db, source := newSnapshotFixture(t, "wal", 8)
			dest := filepath.Join(t.TempDir(), "new.db")
			ops := &backupPublicationOps{syncDir: func(string) error { return syscall.EIO }}
			if rollbackFails {
				ops.remove = func(string) error { return syscall.EACCES }
			}
			err := createSQLiteBackupFile(context.Background(), db, source, dest, &sqliteBackupHooks{publication: ops})
			if !errors.Is(err, syscall.EIO) {
				t.Fatalf("err=%v", err)
			}
			if rollbackFails {
				assertSnapshotFile(t, dest)
				before := readDailyBackupBytes(t, dest)
				if err := CreateSQLiteBackupFile(db, source, dest); !errors.Is(err, os.ErrExist) {
					t.Fatalf("existing destination accepted: %v", err)
				}
				if !bytes.Equal(before, readDailyBackupBytes(t, dest)) {
					t.Fatal("existing destination changed")
				}
			} else if _, err := os.Lstat(dest); !os.IsNotExist(err) {
				t.Fatalf("rollback left final: %v", err)
			}
		})
	}
}

func TestBackupPublicationExistingCandidateFailures(t *testing.T) {
	for _, kind := range []string{"unknown", "symlink", "hardlink", "file-sync", "directory-sync"} {
		t.Run(kind, func(t *testing.T) {
			db, source := openDailyBackupTestDB(t)
			dir := t.TempDir()
			day := time.Date(2026, 9, 10, 12, 0, 0, 0, time.UTC)
			dest := filepath.Join(dir, dailyAutomaticBackupName(day))
			other := filepath.Join(t.TempDir(), "other.db")
			if err := CreateSQLiteBackupFile(db, source, other); err != nil {
				t.Fatal(err)
			}
			ops := &backupPublicationOps{}
			switch kind {
			case "unknown":
				if err := os.WriteFile(dest, []byte("not a backup"), 0o600); err != nil {
					t.Fatal(err)
				}
			case "symlink":
				if err := os.Symlink(other, dest); err != nil {
					t.Fatal(err)
				}
			case "hardlink":
				if err := os.Link(other, dest); err != nil {
					t.Fatal(err)
				}
			default:
				if err := ensureDailyAutomaticBackup(db, source, dir, day, 7); err != nil {
					t.Fatal(err)
				}
				if kind == "file-sync" {
					ops.syncFile = func(*os.File) error { return syscall.EIO }
				} else {
					ops.syncDir = func(string) error { return syscall.EIO }
				}
			}
			before := readDailyBackupBytes(t, dest)
			otherBefore, _ := os.Stat(other)
			if err := ensureDailyAutomaticBackupWithPublication(context.Background(), db, source, dir, day, 7, ops); err == nil {
				t.Fatal("unsafe/unconfirmed candidate accepted")
			}
			if !bytes.Equal(before, readDailyBackupBytes(t, dest)) {
				t.Fatal("unknown candidate changed")
			}
			otherAfter, _ := os.Stat(other)
			if otherBefore.Mode() != otherAfter.Mode() {
				t.Fatal("linked file permissions changed")
			}
		})
	}
}

func TestBackupPublicationConfirmationWaitsForRollback(t *testing.T) {
	db, source := newSnapshotFixture(t, "wal", 8)
	dir := t.TempDir()
	stage := filepath.Join(t.TempDir(), "stage.db")
	if err := CreateSQLiteBackupFile(db, source, stage); err != nil {
		t.Fatal(err)
	}
	dest := filepath.Join(dir, "final.db")
	linked, release := make(chan struct{}), make(chan struct{})
	finished := make(chan error, 1)
	ops := &backupPublicationOps{syncDir: func(string) error { close(linked); <-release; return syscall.EIO }, remove: func(string) error { return syscall.EACCES }}
	go func() { _, err := publishBackupFile(context.Background(), stage, dest, ops); finished <- err }()
	<-linked
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	_, err := confirmExistingBackup(ctx, dest, nil)
	close(release)
	publicationErr := <-finished
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("confirmation escaped publication lock: %v", err)
	}
	if !errors.Is(publicationErr, syscall.EACCES) {
		t.Fatalf("publication=%v", publicationErr)
	}
}

func TestBackupPublicationConcurrentPublishers(t *testing.T) {
	db, source := newSnapshotFixture(t, "wal", 8)
	stage := filepath.Join(t.TempDir(), "stage.db")
	if err := CreateSQLiteBackupFile(db, source, stage); err != nil {
		t.Fatal(err)
	}
	dest := filepath.Join(t.TempDir(), "final.db")
	start := make(chan struct{})
	var wg sync.WaitGroup
	results := make(chan bool, 2)
	errs := make(chan error, 2)
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			published, err := publishBackupFile(context.Background(), stage, dest, nil)
			results <- published
			errs <- err
		}()
	}
	close(start)
	wg.Wait()
	close(results)
	close(errs)
	winners := 0
	for winner := range results {
		if winner {
			winners++
		}
	}
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	if winners != 1 {
		t.Fatalf("winners=%d", winners)
	}
	assertSnapshotFile(t, dest)
}

func TestBackupPublicationCancellationBoundaryAndFileSync(t *testing.T) {
	for _, kind := range []string{"before-link", "after-link", "file-sync"} {
		t.Run(kind, func(t *testing.T) {
			db, source := newSnapshotFixture(t, "wal", 8)
			dest := filepath.Join(t.TempDir(), "final.db")
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			hooks := &sqliteBackupHooks{publication: &backupPublicationOps{}}
			switch kind {
			case "before-link":
				hooks.publication.syncFile = func(f *os.File) error { err := f.Sync(); cancel(); return err }
			case "after-link":
				hooks.publication.link = func(src, dst string) error { err := os.Link(src, dst); cancel(); return err }
			case "file-sync":
				hooks.publication.syncFile = func(*os.File) error { return syscall.EIO }
			}
			err := createSQLiteBackupFile(ctx, db, source, dest, hooks)
			if kind == "after-link" {
				if err != nil {
					t.Fatal(err)
				}
				assertSnapshotFile(t, dest)
			} else {
				if err == nil {
					t.Fatal("failure accepted")
				}
				if _, err := os.Lstat(dest); !os.IsNotExist(err) {
					t.Fatalf("published after failure: %v", err)
				}
			}
		})
	}
}

func TestBackupPublicationRollbackPreservesReplacedDestination(t *testing.T) {
	db, source := newSnapshotFixture(t, "wal", 8)
	dest := filepath.Join(t.TempDir(), "final.db")
	unknown := []byte("external file, never remove")
	removed := false
	ops := &backupPublicationOps{
		syncDir: func(string) error {
			if err := os.Remove(dest); err != nil {
				return err
			}
			if err := os.WriteFile(dest, unknown, 0o600); err != nil {
				return err
			}
			return syscall.EIO
		},
		remove: func(path string) error { removed = true; return os.Remove(path) },
	}
	if err := createSQLiteBackupFile(context.Background(), db, source, dest, &sqliteBackupHooks{publication: ops}); !errors.Is(err, syscall.EIO) {
		t.Fatalf("err=%v", err)
	}
	if removed || !bytes.Equal(unknown, readDailyBackupBytes(t, dest)) {
		t.Fatal("rollback changed external destination")
	}
}

func TestBackupPublicationProcessCrash(t *testing.T) {
	if dir := os.Getenv("CONTABASE_TEST_PUBLICATION_CHILD"); dir != "" {
		syncCalls := 0
		_, err := publishDailyBackup(context.Background(), filepath.Join(dir, "stage.db"), filepath.Join(dir, "contabase-daily-20260911.db"), &backupPublicationOps{
			syncDir: func(path string) error {
				syncCalls++
				if syncCalls == 1 {
					return syncDirectory(path)
				}
				// Exit after the real link, before sync/rollback/unlock/cleanup.
				os.Exit(23)
				return nil
			},
		})
		t.Fatalf("child never reached crash point: %v", err)
	}
	db, source := newSnapshotFixture(t, "wal", 8)
	dir := t.TempDir()
	stage, dest := filepath.Join(dir, "stage.db"), filepath.Join(dir, "contabase-daily-20260911.db")
	if err := CreateSQLiteBackupFile(db, source, stage); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	child := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestBackupPublicationProcessCrash$")
	child.Env = append(os.Environ(), "CONTABASE_TEST_PUBLICATION_CHILD="+dir)
	output, err := child.CombinedOutput()
	var exitErr *exec.ExitError
	if !errors.As(err, &exitErr) || exitErr.ExitCode() != 23 {
		t.Fatalf("child=%v output=%s", err, output)
	}
	assertSnapshotFile(t, dest)
	// The OS released the child's directory lock. Unrecognized extra links are
	// preserved/fail closed until the operator inspects the orphan staging.
	if _, err := confirmExistingBackup(ctx, dest, nil); err == nil {
		t.Fatal("orphan hardlink silently accepted")
	}
	before := readDailyBackupBytes(t, dest)
	if err := os.Remove(stage); err != nil {
		t.Fatal(err)
	} // This test's own synthetic staging only.
	if exists, err := confirmExistingBackup(ctx, dest, nil); err != nil || !exists {
		t.Fatalf("reconfirmation=%t %v", exists, err)
	}
	if !bytes.Equal(before, readDailyBackupBytes(t, dest)) {
		t.Fatal("reconfirmation changed crash snapshot")
	}
}
