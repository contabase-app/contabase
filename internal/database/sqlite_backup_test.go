package database

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"
)

func openSnapshotTestDB(t *testing.T, path, options string) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", sqliteBackupURI(path, "_pragma=busy_timeout(5000)&_pragma=wal_autocheckpoint(0)&_pragma=synchronous(NORMAL)&"+options))
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { db.Close() })
	return db
}

func newSnapshotFixture(t *testing.T, journal string, rows int) (*sql.DB, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "source '?#.db")
	db := openSnapshotTestDB(t, path, "")
	var mode string
	if err := db.QueryRow("PRAGMA journal_mode=" + journal).Scan(&mode); err != nil {
		t.Fatal(err)
	}
	if mode != journal {
		t.Fatalf("journal=%s, want %s", mode, journal)
	}
	_, err := db.Exec(`
		CREATE TABLE balances(id INTEGER PRIMARY KEY, cents INTEGER NOT NULL, generation INTEGER NOT NULL, padding BLOB);
		CREATE TABLE state(counter INTEGER NOT NULL);
		INSERT INTO state VALUES(0);
		CREATE TABLE history(seq INTEGER PRIMARY KEY, cents INTEGER NOT NULL, account_id INTEGER REFERENCES balances(id));
		CREATE TABLE implicit_ids(value TEXT);
		INSERT INTO implicit_ids(rowid,value) VALUES(42,'preserve-rowid');
		CREATE INDEX history_account ON history(account_id);
		PRAGMA user_version=64;
		PRAGMA application_id=1234;
		WITH RECURSIVE n(i) AS (SELECT 1 UNION ALL SELECT i+1 FROM n WHERE i < ?)
		INSERT INTO balances SELECT i,1000000000,0,zeroblob(3900) FROM n;
	`, rows)
	if err != nil {
		t.Fatal(err)
	}
	return db, path
}

// One SQL read so the assertion itself cannot combine different commits.
func readSnapshotInvariant(db *sql.DB) (int64, error) {
	var a, b, ga, gb, n, count, sum, last int64
	err := db.QueryRow(`SELECT a.cents,b.cents,a.generation,b.generation,s.counter,
		(SELECT count(*) FROM history),(SELECT coalesce(sum(cents),0) FROM history),
		(SELECT coalesce(max(seq),0) FROM history)
		FROM balances a, balances b, state s
		WHERE a.id=1 AND b.id=(SELECT max(id) FROM balances)`).Scan(&a, &b, &ga, &gb, &n, &count, &sum, &last)
	if err != nil {
		return 0, err
	}
	if a+b != 2000000000 || a != 1000000000-n || b != 1000000000+n || ga != n || gb != n || count != n || sum != n || last != n {
		return n, fmt.Errorf("impossible snapshot: a=%d b=%d ga=%d gb=%d counter=%d history=%d sum=%d last=%d", a, b, ga, gb, n, count, sum, last)
	}
	return n, nil
}

func commitSnapshotTransfer(ctx context.Context, db *sql.DB) error {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	_, err = tx.ExecContext(ctx, `
		UPDATE balances SET cents=cents-1,generation=generation+1 WHERE id=1;
		UPDATE balances SET cents=cents+1,generation=generation+1 WHERE id=(SELECT max(id) FROM balances);
		UPDATE state SET counter=counter+1;
		INSERT INTO history SELECT counter,1,1 FROM state;
	`)
	if err != nil {
		return err
	}
	return tx.Commit()
}

func assertSnapshotFile(t *testing.T, path string) int64 {
	t.Helper()
	if err := validateSQLiteSnapshot(context.Background(), path); err != nil {
		t.Fatal(err)
	}
	db := openSnapshotTestDB(t, path, "mode=ro&immutable=1")
	defer db.Close()
	if err := assertDatabaseForeignKeyIntegrity(db); err != nil {
		t.Fatal(err)
	}
	n, err := readSnapshotInvariant(db)
	if err != nil {
		t.Fatal(err)
	}
	var rowID, version, appID int
	if err := db.QueryRow(`SELECT rowid FROM implicit_ids WHERE value='preserve-rowid'`).Scan(&rowID); err != nil || rowID != 42 {
		t.Fatalf("rowid=%d err=%v", rowID, err)
	}
	if err := db.QueryRow(`PRAGMA user_version`).Scan(&version); err != nil || version != 64 {
		t.Fatalf("user_version=%d err=%v", version, err)
	}
	if err := db.QueryRow(`PRAGMA application_id`).Scan(&appID); err != nil || appID != 1234 {
		t.Fatalf("application_id=%d err=%v", appID, err)
	}
	return n
}

func TestSQLiteSnapshotSmallAndWAL(t *testing.T) {
	for _, mode := range []string{"delete", "wal"} {
		t.Run(mode, func(t *testing.T) {
			db, path := newSnapshotFixture(t, mode, 8)
			if err := commitSnapshotTransfer(context.Background(), db); err != nil {
				t.Fatal(err)
			}
			if mode == "wal" {
				info, err := os.Stat(path + "-wal")
				if err != nil || info.Size() == 0 {
					t.Fatalf("fixture needs uncheckpointed WAL: %v", err)
				}
			}
			dest := filepath.Join(t.TempDir(), "backup '?#.db")
			// File URI handles names that would otherwise be parsed as query text.
			if err := CreateSQLiteBackupFile(db, sqliteBackupURI(path, ""), dest); err != nil {
				t.Fatal(err)
			}
			if n := assertSnapshotFile(t, dest); n != 1 {
				t.Fatalf("counter=%d", n)
			}
			info, err := os.Stat(dest)
			if err != nil || info.Mode().Perm() != 0o600 {
				t.Fatalf("snapshot permissions: %v %v", info, err)
			}
			for _, suffix := range []string{"-wal", "-shm", "-journal"} {
				if _, err := os.Stat(dest + suffix); !os.IsNotExist(err) {
					t.Fatalf("standalone backup has sidecar %s: %v", suffix, err)
				}
			}
		})
	}
}

func TestSQLiteSnapshotConcurrentWAL(t *testing.T) {
	db, path := newSnapshotFixture(t, "wal", 32768)
	if err := checkpointWAL(db); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil || info.Size() < 128*1024*1024 {
		t.Fatalf("fixture too small: %v %v", info, err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	var wg sync.WaitGroup
	defer func() { cancel(); wg.Wait() }()
	errs := make(chan error, 4)
	var commits, checkpoints, busyTruncates, reads, longestCommit atomic.Int64
	for _, mode := range []string{"deferred", "immediate"} {
		writer := openSnapshotTestDB(t, path, "_txlock="+mode)
		wg.Add(1)
		go func() {
			defer wg.Done()
			for ctx.Err() == nil {
				start := time.Now()
				if err := commitSnapshotTransfer(ctx, writer); err != nil {
					if ctx.Err() == nil {
						errs <- err
					}
					return
				}
				elapsed := int64(time.Since(start))
				for previous := longestCommit.Load(); elapsed > previous; previous = longestCommit.Load() {
					if longestCommit.CompareAndSwap(previous, elapsed) {
						break
					}
				}
				commits.Add(1)
				time.Sleep(time.Millisecond)
			}
		}()
	}
	checkpointer := openSnapshotTestDB(t, path, "")
	// Do not repeat busy_timeout in the DSN: modernc sorts pragmas, so the
	// default 5000 could override 0. This loop probes TRUNCATE without waiting.
	if _, err := checkpointer.Exec(`PRAGMA busy_timeout=0`); err != nil {
		t.Fatal(err)
	}
	var checkpointTimeout int
	if err := checkpointer.QueryRow(`PRAGMA busy_timeout`).Scan(&checkpointTimeout); err != nil || checkpointTimeout != 0 {
		t.Fatalf("checkpoint timeout=%d err=%v", checkpointTimeout, err)
	}
	reader := openSnapshotTestDB(t, path, "mode=ro")
	wg.Add(2)
	go func() {
		defer wg.Done()
		for ctx.Err() == nil {
			var busy, total, copied int
			pragma := `PRAGMA wal_checkpoint(PASSIVE)`
			if checkpoints.Load()%2 == 1 {
				pragma = `PRAGMA wal_checkpoint(TRUNCATE)`
			}
			if err := checkpointer.QueryRowContext(ctx, pragma).Scan(&busy, &total, &copied); err != nil {
				if ctx.Err() == nil {
					errs <- err
				}
				return
			}
			if busy != 0 && pragma == `PRAGMA wal_checkpoint(TRUNCATE)` {
				busyTruncates.Add(1)
			}
			checkpoints.Add(1)
			time.Sleep(time.Millisecond)
		}
	}()
	go func() {
		defer wg.Done()
		for ctx.Err() == nil {
			if _, err := readSnapshotInvariant(reader); err != nil {
				errs <- err
				return
			}
			reads.Add(1)
			time.Sleep(time.Millisecond)
		}
	}()
	for cycle := 0; cycle < 5; cycle++ {
		dest := filepath.Join(t.TempDir(), "snapshot.db")
		firstStep := true
		var fixedGeneration int64
		hooks := &sqliteBackupHooks{afterStep: func() error {
			if !firstStep {
				return nil
			}
			firstStep = false
			if _, err := os.Stat(dest); !os.IsNotExist(err) {
				return fmt.Errorf("destination visible before completion: %v", err)
			}
			// Reading the normal one-connection pool while capture is paused
			// must work, and at least ten writes must commit between steps.
			var err error
			fixedGeneration, err = readSnapshotInvariant(db)
			if err != nil {
				return err
			}
			before := commits.Load()
			deadline := time.Now().Add(5 * time.Second)
			for commits.Load() < before+10 {
				if time.Now().After(deadline) || ctx.Err() != nil {
					return fmt.Errorf("writers did not advance during active snapshot")
				}
				time.Sleep(time.Millisecond)
			}
			return nil
		}}
		before, cp, rd := commits.Load(), checkpoints.Load(), reads.Load()
		start := time.Now()
		var err error
		if cycle == 0 {
			err = createSQLiteBackupFile(ctx, db, sqliteBackupURI(path, ""), dest, hooks)
		} else {
			// Exercise the public path with no pause/hook as well.
			err = CreateSQLiteBackupFileContext(ctx, db, sqliteBackupURI(path, ""), dest)
		}
		if err != nil {
			t.Fatal(err)
		}
		elapsed := time.Since(start)
		writesDuring, checkpointsDuring, readsDuring := commits.Load()-before, checkpoints.Load()-cp, reads.Load()-rd
		generation := assertSnapshotFile(t, dest)
		if cycle == 0 && generation > fixedGeneration {
			t.Fatalf("snapshot moved after its first step: %d > %d", generation, fixedGeneration)
		}
		if writesDuring < 10 || checkpointsDuring == 0 || readsDuring == 0 {
			t.Fatal("concurrency preconditions not met")
		}
		t.Logf("cycle=%d bytes=%d elapsed=%s commits=%d checkpoints=%d readers=%d generation=%d max_commit=%s", cycle, info.Size(), elapsed, writesDuring, checkpointsDuring, readsDuring, generation, time.Duration(longestCommit.Load()))
	}
	cancel()
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Error(err)
	}
	if busyTruncates.Load() == 0 {
		t.Fatal("no checkpoint encountered the active reader snapshot")
	}
	if _, err := readSnapshotInvariant(db); err != nil {
		t.Fatal(err)
	}
}

func TestSQLiteSnapshotFailuresNeverPublish(t *testing.T) {
	db, path := newSnapshotFixture(t, "wal", 256)
	for _, failure := range []string{"cancel_before", "cancel_during", "cancel_validation", "write_error", "read_error", "validation", "destination_readonly", "directory_sync", "link_conflict"} {
		t.Run(failure, func(t *testing.T) {
			dir := t.TempDir()
			dest := filepath.Join(dir, "new.db")
			prior := filepath.Join(dir, "previous.db")
			if err := CreateSQLiteBackupFile(db, sqliteBackupURI(path, ""), prior); err != nil {
				t.Fatal(err)
			}
			before, err := os.ReadFile(prior)
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			hooks := &sqliteBackupHooks{}
			switch failure {
			case "cancel_before":
				cancel()
			case "cancel_during":
				hooks.afterStep = func() error { cancel(); return nil }
			case "cancel_validation":
				hooks.beforePublish = func(string) error { cancel(); return nil }
			case "write_error":
				hooks.afterStep = func() error { return syscall.ENOSPC }
			case "read_error":
				hooks.afterStep = func() error { return syscall.EIO }
			case "validation":
				hooks.beforePublish = func(stage string) error { return os.Truncate(stage, 100) }
			case "destination_readonly":
				if os.Geteuid() == 0 {
					t.Skip("root bypasses file permissions")
				}
				hooks.beforePublish = func(stage string) error { return os.Chmod(stage, 0o400) }
			case "directory_sync":
				if os.Geteuid() == 0 {
					t.Skip("root bypasses directory permissions")
				}
				// Link can succeed with wx, but opening the directory to fsync
				// fails without read permission. Its publication must roll back.
				hooks.beforePublish = func(string) error { return os.Chmod(dir, 0o300) }
				defer os.Chmod(dir, 0o700)
			case "link_conflict":
				// Another successful publisher wins during capture. Preserve it.
				hooks.beforePublish = func(string) error { return os.Link(prior, dest) }
			}
			err = createSQLiteBackupFile(ctx, db, sqliteBackupURI(path, ""), dest, hooks)
			if failure == "directory_sync" {
				if chmodErr := os.Chmod(dir, 0o700); chmodErr != nil {
					t.Fatal(chmodErr)
				}
			}
			if err == nil {
				t.Fatal("failure reported success")
			}
			if failure == "cancel_before" || failure == "cancel_during" || failure == "cancel_validation" {
				if !errors.Is(err, context.Canceled) {
					t.Fatalf("not a cancellation: %v", err)
				}
			}
			if failure == "link_conflict" {
				got, _ := os.ReadFile(dest)
				if !bytes.Equal(got, before) {
					t.Fatal("concurrent destination replaced")
				}
			} else if _, err := os.Stat(dest); !os.IsNotExist(err) {
				t.Fatalf("partial backup published: %v", err)
			}
			after, err := os.ReadFile(prior)
			if err != nil || !bytes.Equal(before, after) {
				t.Fatal("previous backup changed")
			}
			entries, err := os.ReadDir(dir)
			if err != nil {
				t.Fatal(err)
			}
			for _, entry := range entries {
				if entry.Name() != "previous.db" && !(failure == "link_conflict" && entry.Name() == "new.db") {
					t.Fatalf("incomplete artifact left: %s", entry.Name())
				}
			}
		})
	}
}

func TestSQLiteSnapshotDestinationPermissionDenied(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root bypasses directory permissions")
	}
	db, path := newSnapshotFixture(t, "wal", 8)
	dir := t.TempDir()
	if err := os.Chmod(dir, 0o500); err != nil {
		t.Fatal(err)
	}
	defer os.Chmod(dir, 0o700)
	err := CreateSQLiteBackupFileContext(context.Background(), db, path, filepath.Join(dir, "backup.db"))
	if !errors.Is(err, os.ErrPermission) {
		t.Fatalf("want permission denied, got %v", err)
	}
	entries, err := os.ReadDir(dir)
	if err != nil || len(entries) != 0 {
		t.Fatalf("incomplete file: %v %v", entries, err)
	}
}

func TestSQLiteSnapshotReleasesWaitingTruncateCheckpoint(t *testing.T) {
	db, path := newSnapshotFixture(t, "wal", 256)
	checkpointer := openSnapshotTestDB(t, path, "")
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	result := make(chan error, 1)
	first := true
	hooks := &sqliteBackupHooks{afterStep: func() error {
		if !first {
			return nil
		}
		first = false
		go func() {
			var busy, frames, copied int
			err := checkpointer.QueryRowContext(ctx, `PRAGMA wal_checkpoint(TRUNCATE)`).Scan(&busy, &frames, &copied)
			if err == nil && busy != 0 {
				err = fmt.Errorf("checkpoint timed out: busy=%d", busy)
			}
			result <- err
		}()
		// Unlike the nonblocking stress checkpointer, this connection has a
		// 5s busy timeout. It waits for the snapshot; capture must not wait for
		// new writes here, since TRUNCATE may hold the writer lock meanwhile.
		select {
		case err := <-result:
			return fmt.Errorf("checkpoint did not wait for snapshot: %v", err)
		case <-time.After(25 * time.Millisecond):
			return nil
		}
	}}
	dest := filepath.Join(t.TempDir(), "snapshot.db")
	if err := createSQLiteBackupFile(ctx, db, path, dest, hooks); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-result:
		if err != nil {
			t.Fatal(err)
		}
	case <-ctx.Done():
		t.Fatal("checkpoint did not finish after snapshot release")
	}
	assertSnapshotFile(t, dest)
	if err := commitSnapshotTransfer(ctx, db); err != nil {
		t.Fatal(err)
	}
}

func TestSQLiteSnapshotPreservesLegacyForeignKeyState(t *testing.T) {
	db, path := newSnapshotFixture(t, "wal", 8)
	if _, err := db.Exec(`PRAGMA foreign_keys=OFF; INSERT INTO history VALUES(1,1,999)`); err != nil {
		t.Fatal(err)
	}
	dest := filepath.Join(t.TempDir(), "pre-migration.db")
	if err := CreateSQLiteBackupFile(db, path, dest); err != nil {
		t.Fatal(err)
	}
	backup := openSnapshotTestDB(t, dest, "mode=ro&immutable=1")
	if err := assertDatabaseForeignKeyIntegrity(backup); err == nil {
		t.Fatal("legacy violation should be faithfully preserved, not silently repaired")
	}
	var account int
	if err := backup.QueryRow(`SELECT account_id FROM history WHERE seq=1`).Scan(&account); err != nil || account != 999 {
		t.Fatalf("legacy state changed: %d %v", account, err)
	}
}

func TestSQLiteSnapshotRejectsUnsafeDestinations(t *testing.T) {
	db, path := newSnapshotFixture(t, "wal", 8)
	dir := t.TempDir()
	prior := filepath.Join(dir, "previous.db")
	if err := CreateSQLiteBackupFile(db, sqliteBackupURI(path, ""), prior); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(prior)
	if err != nil {
		t.Fatal(err)
	}
	symlink := filepath.Join(dir, "link.db")
	if err := os.Symlink(prior, symlink); err != nil {
		t.Fatal(err)
	}
	hardlink := filepath.Join(dir, "hardlink.db")
	if err := os.Link(path, hardlink); err != nil {
		t.Fatal(err)
	}
	for _, dest := range []string{path, prior, symlink, hardlink, dir, filepath.Join(prior, "child.db")} {
		if err := CreateSQLiteBackupFile(db, sqliteBackupURI(path, ""), dest); err == nil {
			t.Fatalf("unsafe destination accepted: %s", dest)
		}
	}
	after, err := os.ReadFile(prior)
	if err != nil || !bytes.Equal(before, after) {
		t.Fatal("previous backup changed")
	}
	if err := CreateSQLiteBackupFile(db, prior, filepath.Join(dir, "mismatched.db")); err == nil {
		t.Fatal("mismatched database handle/path accepted")
	}
}
