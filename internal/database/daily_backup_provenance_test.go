package database

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
)

func TestDailyBackupF05R02RejectsStaleSQLiteWithoutProvenance(t *testing.T) {
	db, source := openDailyBackupTestDB(t)
	dir := t.TempDir()
	day := time.Date(2026, 9, 11, 12, 0, 0, 0, time.UTC)
	dest := filepath.Join(dir, dailyAutomaticBackupName(day))
	insertDailyBackupMarker(t, db, "generation-1")
	if err := CreateSQLiteBackupFile(db, source, dest); err != nil {
		t.Fatal(err)
	}
	before := readDailyBackupBytes(t, dest)
	info, err := os.Stat(dest)
	if err != nil {
		t.Fatal(err)
	}
	insertDailyBackupMarker(t, db, "generation-2")
	err = ensureDailyAutomaticBackup(db, source, dir, day, 7)
	if !errors.Is(err, errBackupProvenance) {
		t.Fatal("F05-R02: stale SQLite without provenance accepted as today's backup")
	}
	afterInfo, statErr := os.Stat(dest)
	if statErr != nil || !os.SameFile(info, afterInfo) || info.Mode() != afterInfo.Mode() || !bytes.Equal(before, readDailyBackupBytes(t, dest)) {
		t.Fatal("unknown SQLite was removed, replaced or modified")
	}
}

func TestDailyBackupProvenanceRejectsUnknownCandidates(t *testing.T) {
	for _, kind := range []string{"external-sqlite", "other-database", "truncated", "text", "symlink", "hardlink"} {
		t.Run(kind, func(t *testing.T) {
			db, source := openDailyBackupTestDB(t)
			dir := t.TempDir()
			day := time.Date(2026, 9, 11, 0, 0, 0, 0, time.UTC)
			dest := filepath.Join(dir, dailyAutomaticBackupName(day))
			other := filepath.Join(t.TempDir(), "external.db")
			if err := CreateSQLiteBackupFile(db, source, other); err != nil {
				t.Fatal(err)
			}
			data := readDailyBackupBytes(t, other)
			switch kind {
			case "other-database":
				x, path := newSnapshotFixture(t, "wal", 8)
				if err := CreateSQLiteBackupFile(x, path, dest); err != nil {
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
				if kind == "truncated" {
					data = data[:100]
				}
				if kind == "text" {
					data = []byte("not sqlite")
				}
				if err := os.WriteFile(dest, data, 0o600); err != nil {
					t.Fatal(err)
				}
			}
			before := readDailyBackupBytes(t, dest)
			info, _ := os.Lstat(dest)
			if err := ensureDailyAutomaticBackup(db, source, dir, day, 7); !errors.Is(err, errBackupProvenance) {
				t.Fatalf("err=%v", err)
			}
			after, err := os.Lstat(dest)
			if err != nil || !os.SameFile(info, after) || info.Mode() != after.Mode() || !bytes.Equal(before, readDailyBackupBytes(t, dest)) {
				t.Fatal("unknown modified")
			}
			if _, err := os.Lstat(dailyBackupProofPath(dest)); !os.IsNotExist(err) {
				t.Fatal("proof invented for unknown")
			}
		})
	}
}

func newProvenDailyFixture(t *testing.T) (string, time.Time) {
	t.Helper()
	db, source := openDailyBackupTestDB(t)
	day := time.Date(2026, 9, 11, 0, 0, 0, 0, time.UTC)
	dir := t.TempDir()
	insertDailyBackupMarker(t, db, "original")
	if err := ensureDailyAutomaticBackup(db, source, dir, day, 7); err != nil {
		t.Fatal(err)
	}
	return filepath.Join(dir, dailyAutomaticBackupName(day)), day
}

func TestDailyBackupProvenanceDetectsReplacementAndTampering(t *testing.T) {
	for _, kind := range []string{"identical-replacement", "in-place-same-size-mtime", "proof-text", "proof-hash", "proof-name", "proof-size", "proof-inode", "proof-device", "proof-protocol", "proof-symlink", "proof-hardlink", "proof-without-file", "file-without-proof"} {
		t.Run(kind, func(t *testing.T) {
			path, _ := newProvenDailyFixture(t)
			proofPath := dailyBackupProofPath(path)
			switch kind {
			case "identical-replacement":
				data := readDailyBackupBytes(t, path)
				if err := os.Rename(path, path+".original"); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(path, data, 0o444); err != nil {
					t.Fatal(err)
				}
			case "in-place-same-size-mtime":
				info, _ := os.Stat(path)
				data := readDailyBackupBytes(t, path)
				data[len(data)-1] ^= 1
				if err := os.Chmod(path, 0o600); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(path, data, 0o600); err != nil {
					t.Fatal(err)
				}
				if err := os.Chtimes(path, info.ModTime(), info.ModTime()); err != nil {
					t.Fatal(err)
				}
			case "proof-without-file":
				if err := os.Remove(path); err != nil {
					t.Fatal(err)
				}
			case "file-without-proof":
				if err := os.Remove(proofPath); err != nil {
					t.Fatal(err)
				}
			case "proof-symlink", "proof-hardlink":
				if err := os.Rename(proofPath, proofPath+".original"); err != nil {
					t.Fatal(err)
				}
				link := os.Link
				if kind == "proof-symlink" {
					link = os.Symlink
				}
				if err := link(proofPath+".original", proofPath); err != nil {
					t.Fatal(err)
				}
			default:
				data := readDailyBackupBytes(t, proofPath)
				var proof dailyBackupProof
				if err := json.Unmarshal(data, &proof); err != nil {
					t.Fatal(err)
				}
				switch kind {
				case "proof-hash":
					proof.SHA256 = strings.Repeat("0", 64)
				case "proof-name":
					proof.Name = "contabase-daily-20260101.db"
				case "proof-size":
					proof.Size++
				case "proof-inode":
					proof.Inode++
				case "proof-device":
					proof.Device++
				case "proof-protocol":
					proof.Protocol = "other"
				}
				data, _ = json.Marshal(proof)
				if kind == "proof-text" {
					data = []byte("invalid")
				}
				if err := os.Chmod(proofPath, 0o600); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(proofPath, data, 0o600); err != nil {
					t.Fatal(err)
				}
			}
			beforeDB, dbErr := os.ReadFile(path)
			beforeProof, proofErr := os.ReadFile(proofPath)
			if exists, err := confirmExistingBackup(context.Background(), path, nil); err == nil || exists {
				t.Fatalf("tampering accepted: %v %v", exists, err)
			}
			if dbErr == nil && !bytes.Equal(beforeDB, readDailyBackupBytes(t, path)) {
				t.Fatal("DB changed")
			}
			if proofErr == nil && !bytes.Equal(beforeProof, readDailyBackupBytes(t, proofPath)) {
				t.Fatal("proof changed")
			}
		})
	}
}

func TestDailyBackupProvenanceSurvivesRestart(t *testing.T) {
	if path := os.Getenv("CONTABASE_TEST_PROVENANCE_RESTART"); path != "" {
		if exists, err := confirmExistingBackup(context.Background(), path, nil); err != nil || !exists {
			t.Fatalf("restart: %v %v", exists, err)
		}
		return
	}
	path, _ := newProvenDailyFixture(t)
	before := readDailyBackupBytes(t, path)
	cmd := exec.Command(os.Args[0], "-test.run=^TestDailyBackupProvenanceSurvivesRestart$")
	cmd.Env = append(os.Environ(), "CONTABASE_TEST_PROVENANCE_RESTART="+path)
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("child: %v %s", err, output)
	}
	if !bytes.Equal(before, readDailyBackupBytes(t, path)) {
		t.Fatal("restart recaptured")
	}
}

func TestDailyBackupProvenanceTwoProcesses(t *testing.T) {
	if dir := os.Getenv("CONTABASE_TEST_PROVENANCE_PROCESSES"); dir != "" {
		db, err := sql.Open("sqlite", sqliteBackupURI(filepath.Join(dir, "source.db"), "_pragma=busy_timeout(5000)"))
		if err != nil {
			t.Fatal(err)
		}
		defer db.Close()
		day := time.Date(2026, 9, 11, 0, 0, 0, 0, time.UTC)
		// A publisher may still have its staging hard link when the other
		// process first inspects the pair. That transient condition must fail
		// closed, and a retry after cleanup must work.
		deadline := time.Now().Add(5 * time.Second)
		for {
			err = ensureDailyAutomaticBackup(db, filepath.Join(dir, "source.db"), filepath.Join(dir, "daily"), day, 7)
			if err == nil {
				return
			}
			if !errors.Is(err, errBackupProvenance) || time.Now().After(deadline) {
				t.Fatal(err)
			}
			time.Sleep(10 * time.Millisecond)
		}
	}
	db, source := openDailyBackupTestDB(t)
	dir := t.TempDir()
	if err := CreateSQLiteBackupFile(db, source, filepath.Join(dir, "source.db")); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	var commands []*exec.Cmd
	var outputs []*bytes.Buffer
	for i := 0; i < 2; i++ {
		cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestDailyBackupProvenanceTwoProcesses$")
		cmd.Env = append(os.Environ(), "CONTABASE_TEST_PROVENANCE_PROCESSES="+dir)
		buf := new(bytes.Buffer)
		cmd.Stdout = buf
		cmd.Stderr = buf
		if err := cmd.Start(); err != nil {
			t.Fatal(err)
		}
		commands = append(commands, cmd)
		outputs = append(outputs, buf)
	}
	for i, cmd := range commands {
		if err := cmd.Wait(); err != nil {
			t.Fatalf("process %d: %v %s", i, err, outputs[i])
		}
	}
	entries, err := os.ReadDir(filepath.Join(dir, "daily"))
	if err != nil || len(entries) != 2 {
		t.Fatalf("artifacts %v %v", entries, err)
	}
}

func TestDailyBackupProvenanceRollbackNeverRemovesReplacement(t *testing.T) {
	db, source := openDailyBackupTestDB(t)
	dir := t.TempDir()
	stage := filepath.Join(t.TempDir(), "stage.db")
	if err := CreateSQLiteBackupFile(db, source, stage); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "contabase-daily-20260911.db")
	unknown := []byte("external replacement")
	syncs := 0
	removed := false
	ops := &backupPublicationOps{syncDir: func(dir string) error {
		syncs++
		if syncs == 1 {
			return syncDirectory(dir)
		}
		if err := os.Remove(path); err != nil {
			return err
		}
		if err := os.WriteFile(path, unknown, 0o600); err != nil {
			return err
		}
		return syscall.EIO
	}, remove: func(path string) error { removed = true; return os.Remove(path) }}
	if _, err := publishDailyBackup(context.Background(), stage, path, ops); !errors.Is(err, syscall.EIO) {
		t.Fatal(err)
	}
	if removed || !bytes.Equal(unknown, readDailyBackupBytes(t, path)) {
		t.Fatal("unknown removed")
	}
	if exists, err := confirmExistingBackup(context.Background(), path, nil); err == nil || exists {
		t.Fatal("replacement confirmed")
	}
}

func TestDailyBackupProvenanceTwoJobs(t *testing.T) {
	db, source := openDailyBackupTestDB(t)
	dir := t.TempDir()
	day := time.Date(2026, 9, 11, 0, 0, 0, 0, time.UTC)
	start := make(chan struct{})
	var wg sync.WaitGroup
	results := make(chan error, 2)
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); <-start; results <- ensureDailyAutomaticBackup(db, source, dir, day, 7) }()
	}
	close(start)
	wg.Wait()
	close(results)
	succeeded := 0
	for err := range results {
		if err == nil {
			succeeded++
		} else if !errors.Is(err, errBackupProvenance) {
			t.Fatal(err)
		}
	}
	if succeeded == 0 {
		t.Fatal("no successful publisher")
	}
	if err := ensureDailyAutomaticBackup(db, source, dir, day, 7); err != nil {
		t.Fatal(err)
	}
	entries, _ := os.ReadDir(dir)
	if len(entries) != 2 {
		t.Fatalf("not one DB/proof pair: %v", entries)
	}
}

func TestDailyBackupProvenanceLockHidesPartialPair(t *testing.T) {
	db, source := openDailyBackupTestDB(t)
	dir := t.TempDir()
	stage := filepath.Join(t.TempDir(), "stage.db")
	if err := CreateSQLiteBackupFile(db, source, stage); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "contabase-daily-20260911.db")
	ready, release := make(chan struct{}), make(chan struct{})
	result := make(chan error, 1)
	syncs := 0
	go func() {
		_, err := publishDailyBackup(context.Background(), stage, path, &backupPublicationOps{syncDir: func(dir string) error {
			syncs++
			if syncs == 1 {
				close(ready)
				<-release
			}
			return syncDirectory(dir)
		}})
		result <- err
	}()
	<-ready
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	exists, err := confirmExistingBackup(ctx, path, nil)
	close(release)
	if err := <-result; err != nil {
		t.Fatal(err)
	}
	if !errors.Is(err, context.DeadlineExceeded) || exists {
		t.Fatalf("partial pair escaped lock: %t %v", exists, err)
	}
	if err := os.Remove(stage); err != nil {
		t.Fatal(err)
	}
	if exists, err := confirmExistingBackup(context.Background(), path, nil); err != nil || !exists {
		t.Fatalf("retry: %t %v", exists, err)
	}
}

func TestDailyBackupProvenanceFailuresAndCancellation(t *testing.T) {
	for _, kind := range []string{"file-sync", "proof-sync", "proof-directory-sync", "database-directory-sync", "cancel-before-db-link"} {
		t.Run(kind, func(t *testing.T) {
			db, source := openDailyBackupTestDB(t)
			dir := t.TempDir()
			day := time.Date(2026, 9, 11, 0, 0, 0, 0, time.UTC)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			files, dirs := 0, 0
			ops := &backupPublicationOps{
				syncFile: func(f *os.File) error {
					files++
					if (kind == "file-sync" && files == 1) || (kind == "proof-sync" && files == 2) {
						return syscall.EIO
					}
					return f.Sync()
				},
				syncDir: func(dir string) error {
					dirs++
					if (kind == "proof-directory-sync" && dirs == 1) || (kind == "database-directory-sync" && dirs == 2) {
						return syscall.EIO
					}
					if kind == "cancel-before-db-link" && dirs == 1 {
						cancel()
					}
					return syncDirectory(dir)
				},
			}
			if err := ensureDailyAutomaticBackupWithPublication(ctx, db, source, dir, day, 7, ops); err == nil {
				t.Fatal("fault accepted")
			}
			entries, _ := os.ReadDir(dir)
			if len(entries) != 0 {
				t.Fatalf("unexpected remnants: %v", entries)
			}
			if err := ensureDailyAutomaticBackup(db, source, dir, day, 7); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestDailyBackupProvenanceIdentityDuringConfirmation(t *testing.T) {
	for _, boundary := range []string{"file-sync", "directory-sync"} {
		t.Run(boundary, func(t *testing.T) {
			path, _ := newProvenDailyFixture(t)
			original := readDailyBackupBytes(t, path)
			swap := func() error {
				if err := os.Rename(path, path+".original"); err != nil {
					return err
				}
				return os.WriteFile(path, original, 0o600)
			}
			ops := &backupPublicationOps{}
			if boundary == "file-sync" {
				first := true
				ops.syncFile = func(f *os.File) error {
					if first {
						first = false
						return swap()
					}
					return f.Sync()
				}
			} else {
				ops.syncDir = func(string) error { return swap() }
			}
			if exists, err := confirmExistingBackup(context.Background(), path, ops); err == nil || exists {
				t.Fatalf("wrong inode confirmed: %v %v", exists, err)
			}
			if !bytes.Equal(original, readDailyBackupBytes(t, path)) {
				t.Fatal("replacement altered")
			}
		})
	}
}

func TestDailyBackupProvenanceRetentionPreservesUnknown(t *testing.T) {
	path, day := newProvenDailyFixture(t)
	dir := filepath.Dir(path)
	// The legacy file is the only copy of an earlier generation; neither
	// an unrelated canonical name nor retention may cause deduplication.
	unknown := filepath.Join(dir, "contabase-daily-20260901.db")
	legacy := filepath.Join(dir, "contabase-20260901-010000.db")
	for _, p := range []string{unknown, legacy} {
		if err := os.WriteFile(p, readDailyBackupBytes(t, path), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := pruneAutomaticBackups(context.Background(), dir, day.AddDate(0, 0, 10), 7, nil); err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{unknown, legacy} {
		if _, err := os.Stat(p); err != nil {
			t.Fatal("unknown/legacy removed", err)
		}
	}
	for _, p := range []string{path, dailyBackupProofPath(path)} {
		if _, err := os.Stat(p); !os.IsNotExist(err) {
			t.Fatal("verified expired artifact remains", p)
		}
	}
}

func TestDailyBackupProvenanceHashCost(t *testing.T) {
	db, source := newSnapshotFixture(t, "wal", 32768)
	dir := t.TempDir()
	stage := filepath.Join(t.TempDir(), "snapshot.db")
	if err := CreateSQLiteBackupFile(db, source, stage); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "contabase-daily-20260911.db")
	start := time.Now()
	if _, err := publishDailyBackup(context.Background(), stage, path, nil); err != nil {
		t.Fatal(err)
	}
	publication := time.Since(start)
	if err := os.Remove(stage); err != nil {
		t.Fatal(err)
	}
	start = time.Now()
	if exists, err := confirmExistingBackup(context.Background(), path, nil); err != nil || !exists {
		t.Fatal(err)
	}
	info, _ := os.Stat(path)
	t.Logf("bytes=%d publication_hash_sync=%s retry_hash_sync=%s", info.Size(), publication, time.Since(start))
}

func TestDailyBackupProvenanceCrashBoundaries(t *testing.T) {
	if dir := os.Getenv("CONTABASE_TEST_PROVENANCE_CRASH"); dir != "" {
		boundary := os.Getenv("CONTABASE_TEST_PROVENANCE_BOUNDARY")
		syncs := 0
		_, err := publishDailyBackup(context.Background(), filepath.Join(dir, "stage.db"), filepath.Join(dir, "contabase-daily-20260911.db"), &backupPublicationOps{syncDir: func(path string) error {
			syncs++
			if boundary == fmt.Sprint(syncs) {
				os.Exit(23)
			}
			if err := syncDirectory(path); err != nil {
				return err
			}
			if boundary == "confirmed" && syncs == 2 {
				os.Exit(23)
			}
			return nil
		}})
		t.Fatalf("no crash: %v", err)
	}
	for _, boundary := range []string{"1", "2", "confirmed"} {
		t.Run(boundary, func(t *testing.T) {
			db, source := openDailyBackupTestDB(t)
			dir := t.TempDir()
			stage := filepath.Join(dir, "stage.db")
			path := filepath.Join(dir, "contabase-daily-20260911.db")
			if err := CreateSQLiteBackupFile(db, source, stage); err != nil {
				t.Fatal(err)
			}
			cmd := exec.Command(os.Args[0], "-test.run=^TestDailyBackupProvenanceCrashBoundaries$")
			cmd.Env = append(os.Environ(), "CONTABASE_TEST_PROVENANCE_CRASH="+dir, "CONTABASE_TEST_PROVENANCE_BOUNDARY="+boundary)
			output, err := cmd.CombinedOutput()
			var exit *exec.ExitError
			if !errors.As(err, &exit) || exit.ExitCode() != 23 {
				t.Fatalf("child %v %s", err, output)
			}
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			if exists, err := confirmExistingBackup(ctx, path, nil); err == nil || exists {
				t.Fatalf("partial crash accepted %v %v", exists, err)
			}
			if boundary != "1" {
				if err := os.Remove(stage); err != nil {
					t.Fatal(err)
				} // Only this test's known synthetic orphan.
				if exists, err := confirmExistingBackup(ctx, path, nil); err != nil || !exists {
					t.Fatalf("recovery %v %v", exists, err)
				}
			} else {
				if _, err := os.Stat(dailyBackupProofPath(path)); err != nil {
					t.Fatal("orphan receipt removed", err)
				}
			}
		})
	}
}
