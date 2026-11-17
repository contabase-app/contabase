package main

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/contabase-app/contabase/internal/database"
)

type restoreRecoveryFixture struct {
	root      string
	live      string
	old       string
	candidate string
	backup    string
}

func newRestoreRecoveryFixture(t *testing.T) *restoreRecoveryFixture {
	t.Helper()
	root := t.TempDir()
	t.Setenv("DATA_DIR", root)
	backupDir := filepath.Join(root, "backups", "pre-restore")
	if err := os.MkdirAll(backupDir, 0o700); err != nil {
		t.Fatal(err)
	}
	return &restoreRecoveryFixture{
		root:      root,
		live:      filepath.Join(root, "contabase.db"),
		old:       filepath.Join(root, "contabase.db.restore-current-20261105-120000.db"),
		candidate: filepath.Join(root, "contabase-import-20261105.db"),
		backup:    filepath.Join(backupDir, "contabase-pre-restore-20261105-120000.db"),
	}
}

func (f *restoreRecoveryFixture) seedOriginal(t *testing.T) {
	t.Helper()
	seedRestoreRecoveryDatabase(t, f.live, "original")
}

func (f *restoreRecoveryFixture) seedBackup(t *testing.T) {
	t.Helper()
	seedRestoreRecoveryDatabase(t, f.backup, "original")
}

func (f *restoreRecoveryFixture) seedCandidate(t *testing.T) {
	t.Helper()
	seedRestoreRecoveryDatabase(t, f.candidate, "candidate")
}

func (f *restoreRecoveryFixture) writeJournal(t *testing.T, phase string) {
	t.Helper()
	journal := restoreJournal{
		Version:              restoreJournalVersion,
		Phase:                phase,
		DatabasePath:         f.live,
		OriginalPath:         f.old,
		CandidatePath:        f.candidate,
		PreRestoreBackupPath: f.backup,
	}
	if err := writeRestoreJournal(restoreJournalPath(f.live), journal); err != nil {
		t.Fatalf("write restore journal: %v", err)
	}
}

func seedRestoreRecoveryDatabase(t *testing.T, path, marker string) {
	t.Helper()
	db := createRestoreTestDB(t, path, marker)
	if err := db.Close(); err != nil {
		t.Fatalf("close seeded database: %v", err)
	}
}

func assertRestoreRecoveryResult(t *testing.T, live, marker string) {
	t.Helper()
	assertRestoreRecoveryResultMode(t, live, marker, true)
}

func assertRestoreRecoveryResultMode(t *testing.T, live, marker string, secureMode bool) {
	t.Helper()
	assertRestoreMarker(t, live, marker)
	db, err := sql.Open("sqlite", "file:"+live+"?mode=ro")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := validateSQLiteIntegrity(db); err != nil {
		t.Fatal(err)
	}
	if err := validateSQLiteForeignKeys(db); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(live)
	if err != nil {
		t.Fatal(err)
	}
	if secureMode {
		if got := info.Mode().Perm(); got != 0o600 {
			t.Fatalf("recovered database mode = %o, want 600", got)
		}
	} else if got := info.Mode().Perm(); got&0o022 != 0 {
		t.Fatalf("untouched database mode = %o is group/world writable", got)
	}
	assertRestorePathMissing(t, restoreJournalPath(live))
}

func assertRestorePathMissing(t *testing.T, path string) {
	t.Helper()
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("path %s should not exist, stat err=%v", path, err)
	}
}

func assertRestorePathPresent(t *testing.T, path string) {
	t.Helper()
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("path %s should exist: %v", path, err)
	}
}

func TestRestoreJournalIsPrivate(t *testing.T) {
	f := newRestoreRecoveryFixture(t)
	f.seedOriginal(t)
	f.seedBackup(t)
	f.seedCandidate(t)
	f.writeJournal(t, restoreJournalPhasePrepared)
	info, err := os.Stat(restoreJournalPath(f.live))
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != 0o600 {
		t.Fatalf("journal mode = %o, want 600", got)
	}
	entries, err := os.ReadDir(f.root)
	if err != nil {
		t.Fatal(err)
	}
	tmpPrefix := "." + filepath.Base(restoreJournalPath(f.live)) + ".tmp-"
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), tmpPrefix) {
			t.Fatalf("journal staging file was left behind: %s", entry.Name())
		}
	}
	if err := removeRestoreJournal(restoreJournalPath(f.live)); err != nil {
		t.Fatal(err)
	}
}

func TestRestoreRecoveryJournalStates(t *testing.T) {
	t.Run("before moving the active database", func(t *testing.T) {
		f := newRestoreRecoveryFixture(t)
		f.seedOriginal(t)
		f.seedBackup(t)
		f.seedCandidate(t)
		f.writeJournal(t, restoreJournalPhasePrepared)

		if err := recoverInterruptedRestore(f.live); err != nil {
			t.Fatal(err)
		}
		assertRestoreRecoveryResult(t, f.live, "original")
		assertRestorePathMissing(t, f.old)
		assertRestoreMarker(t, f.candidate, "candidate")
	})

	t.Run("after moving the active database", func(t *testing.T) {
		f := newRestoreRecoveryFixture(t)
		f.seedOriginal(t)
		f.seedBackup(t)
		f.seedCandidate(t)
		if err := os.Rename(f.live, f.old); err != nil {
			t.Fatal(err)
		}
		f.writeJournal(t, restoreJournalPhasePrepared)

		if err := recoverInterruptedRestore(f.live); err != nil {
			t.Fatal(err)
		}
		assertRestoreRecoveryResult(t, f.live, "original")
		assertRestorePathMissing(t, f.old)
		assertRestoreMarker(t, f.candidate, "candidate")
	})

	t.Run("before installing the candidate", func(t *testing.T) {
		f := newRestoreRecoveryFixture(t)
		f.seedOriginal(t)
		f.seedBackup(t)
		f.seedCandidate(t)
		if err := os.Rename(f.live, f.old); err != nil {
			t.Fatal(err)
		}
		if err := removeSQLiteSidecars(f.live); err != nil {
			t.Fatal(err)
		}
		f.writeJournal(t, restoreJournalPhasePrepared)

		if err := recoverInterruptedRestore(f.live); err != nil {
			t.Fatal(err)
		}
		assertRestoreRecoveryResult(t, f.live, "original")
		assertRestorePathMissing(t, f.old)
	})

	t.Run("after installing the candidate but before validation", func(t *testing.T) {
		f := newRestoreRecoveryFixture(t)
		f.seedOriginal(t)
		f.seedBackup(t)
		f.seedCandidate(t)
		if err := os.Rename(f.live, f.old); err != nil {
			t.Fatal(err)
		}
		if err := os.Rename(f.candidate, f.live); err != nil {
			t.Fatal(err)
		}
		f.writeJournal(t, restoreJournalPhasePrepared)

		if err := recoverInterruptedRestore(f.live); err != nil {
			t.Fatal(err)
		}
		assertRestoreRecoveryResult(t, f.live, "original")
		assertRestorePathMissing(t, f.old)
	})

	t.Run("after validating the restored database", func(t *testing.T) {
		f := newRestoreRecoveryFixture(t)
		f.seedOriginal(t)
		f.seedBackup(t)
		f.seedCandidate(t)
		if err := os.Rename(f.live, f.old); err != nil {
			t.Fatal(err)
		}
		if err := os.Rename(f.candidate, f.live); err != nil {
			t.Fatal(err)
		}
		f.writeJournal(t, restoreJournalPhaseValidated)

		if err := recoverInterruptedRestore(f.live); err != nil {
			t.Fatal(err)
		}
		assertRestoreRecoveryResult(t, f.live, "candidate")
		assertRestorePathMissing(t, f.old)
	})

	t.Run("validated database with the old copy already removed", func(t *testing.T) {
		f := newRestoreRecoveryFixture(t)
		f.seedOriginal(t)
		f.seedBackup(t)
		f.seedCandidate(t)
		if err := os.Rename(f.candidate, f.live); err != nil {
			t.Fatal(err)
		}
		f.writeJournal(t, restoreJournalPhaseValidated)

		if err := recoverInterruptedRestore(f.live); err != nil {
			t.Fatal(err)
		}
		assertRestoreRecoveryResult(t, f.live, "candidate")
	})

	t.Run("incomplete installed candidate is rolled back", func(t *testing.T) {
		f := newRestoreRecoveryFixture(t)
		f.seedOriginal(t)
		f.seedBackup(t)
		f.seedCandidate(t)
		if err := os.Rename(f.live, f.old); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(f.live, []byte("incomplete sqlite candidate"), 0o600); err != nil {
			t.Fatal(err)
		}
		f.writeJournal(t, restoreJournalPhasePrepared)

		if err := recoverInterruptedRestore(f.live); err != nil {
			t.Fatal(err)
		}
		assertRestoreRecoveryResult(t, f.live, "original")
		assertRestorePathMissing(t, f.old)
	})

	t.Run("missing active database with only the backup left", func(t *testing.T) {
		f := newRestoreRecoveryFixture(t)
		f.seedOriginal(t)
		f.seedBackup(t)
		f.seedCandidate(t)
		if err := os.Rename(f.live, f.old); err != nil {
			t.Fatal(err)
		}
		if err := os.Remove(f.old); err != nil {
			t.Fatal(err)
		}
		f.writeJournal(t, restoreJournalPhasePrepared)

		if err := recoverInterruptedRestore(f.live); err != nil {
			t.Fatal(err)
		}
		assertRestoreRecoveryResult(t, f.live, "original")
	})

	t.Run("unusable backup fails closed", func(t *testing.T) {
		f := newRestoreRecoveryFixture(t)
		f.seedOriginal(t)
		if err := os.Rename(f.live, f.old); err != nil {
			t.Fatal(err)
		}
		f.writeJournal(t, restoreJournalPhasePrepared)

		if err := recoverInterruptedRestore(f.live); err == nil {
			t.Fatal("recovery accepted an interrupted restore without a usable backup")
		}
		assertRestorePathMissing(t, f.live)
		assertRestorePathPresent(t, f.old)
		assertRestorePathPresent(t, restoreJournalPath(f.live))
	})

	t.Run("journal for another database is rejected", func(t *testing.T) {
		f := newRestoreRecoveryFixture(t)
		f.seedOriginal(t)
		f.seedBackup(t)
		f.seedCandidate(t)
		other := filepath.Join(f.root, "other.db")
		journal := restoreJournal{
			Version:              restoreJournalVersion,
			Phase:                restoreJournalPhasePrepared,
			DatabasePath:         other,
			OriginalPath:         f.old,
			CandidatePath:        f.candidate,
			PreRestoreBackupPath: f.backup,
		}
		if err := writeRestoreJournal(restoreJournalPath(f.live), journal); err != nil {
			t.Fatal(err)
		}
		if err := recoverInterruptedRestore(f.live); err == nil {
			t.Fatal("recovery accepted a journal from another database path")
		}
		assertRestoreMarker(t, f.live, "original")
	})
}

func TestRestoreRecoveryLegacyWorkingCopies(t *testing.T) {
	t.Run("missing data directory is a first run", func(t *testing.T) {
		f := newRestoreRecoveryFixture(t)
		missing := filepath.Join(f.root, "missing", "contabase.db")
		if err := recoverInterruptedRestore(missing); err != nil {
			t.Fatal(err)
		}
		if _, err := os.Stat(filepath.Dir(missing)); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("recovery created the data directory: %v", err)
		}
	})

	t.Run("single working copy is restored", func(t *testing.T) {
		f := newRestoreRecoveryFixture(t)
		f.seedOriginal(t)
		if err := os.Rename(f.live, f.old); err != nil {
			t.Fatal(err)
		}

		if err := recoverInterruptedRestore(f.live); err != nil {
			t.Fatal(err)
		}
		assertRestoreRecoveryResult(t, f.live, "original")
		assertRestorePathMissing(t, f.old)
	})

	t.Run("ambiguous working copies fail closed", func(t *testing.T) {
		f := newRestoreRecoveryFixture(t)
		f.seedOriginal(t)
		second := filepath.Join(f.root, "contabase.db.restore-current-20261105-130000.db")
		if err := os.Rename(f.live, f.old); err != nil {
			t.Fatal(err)
		}
		if err := copyFile(f.old, second); err != nil {
			t.Fatal(err)
		}

		if err := recoverInterruptedRestore(f.live); err == nil {
			t.Fatal("recovery accepted ambiguous working copies")
		}
		assertRestorePathMissing(t, f.live)
		assertRestorePathPresent(t, f.old)
		assertRestorePathPresent(t, second)
	})

	t.Run("orphan upload refuses empty initialization", func(t *testing.T) {
		f := newRestoreRecoveryFixture(t)
		if err := os.WriteFile(f.candidate, []byte("orphan"), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := openRuntimeDBsAfterRestoreRecovery("file:" + f.live); err == nil {
			t.Fatal("startup accepted orphan restore artifacts")
		}
		assertRestorePathMissing(t, f.live)
	})

	t.Run("orphan wal refuses empty initialization", func(t *testing.T) {
		f := newRestoreRecoveryFixture(t)
		if err := os.WriteFile(f.live+"-wal", []byte("wal"), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := openRuntimeDBsAfterRestoreRecovery("file:" + f.live); err == nil {
			t.Fatal("startup accepted a WAL without an active database")
		}
		assertRestorePathMissing(t, f.live)
	})

	t.Run("working copy sidecar refuses empty initialization", func(t *testing.T) {
		f := newRestoreRecoveryFixture(t)
		if err := os.WriteFile(f.old+"-wal", []byte("wal"), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := openRuntimeDBsAfterRestoreRecovery("file:" + f.live); err == nil {
			t.Fatal("startup accepted an orphan working copy sidecar")
		}
		assertRestorePathMissing(t, f.live)
	})

	t.Run("stale working copy beside a valid database is preserved", func(t *testing.T) {
		f := newRestoreRecoveryFixture(t)
		f.seedOriginal(t)
		seedRestoreRecoveryDatabase(t, f.old, "original")

		if err := recoverInterruptedRestore(f.live); err != nil {
			t.Fatal(err)
		}
		assertRestoreMarker(t, f.live, "original")
		assertRestorePathPresent(t, f.old)
	})
}

func TestRestoreRecoveryRetryIsIdempotent(t *testing.T) {
	for _, stage := range []string{"recovery_rollback_active_removed", "recovery_rollback_published"} {
		t.Run(stage, func(t *testing.T) {
			f := newRestoreRecoveryFixture(t)
			f.seedOriginal(t)
			f.seedBackup(t)
			f.seedCandidate(t)
			if err := os.Rename(f.live, f.old); err != nil {
				t.Fatal(err)
			}
			f.writeJournal(t, restoreJournalPhasePrepared)

			injected := errors.New("simulated recovery interruption")
			ops := &restoreRecoveryOps{checkpoint: func(at string) error {
				if at == stage {
					return injected
				}
				return nil
			}}
			if err := recoverInterruptedRestoreWithOps(f.live, ops); !errors.Is(err, injected) {
				t.Fatalf("first recovery error = %v, want %v", err, injected)
			}
			if stage == "recovery_rollback_active_removed" {
				assertRestorePathMissing(t, f.live)
			} else {
				assertRestorePathPresent(t, f.live)
			}
			assertRestorePathPresent(t, f.old)
			assertRestorePathPresent(t, restoreJournalPath(f.live))

			if err := recoverInterruptedRestore(f.live); err != nil {
				t.Fatalf("second recovery: %v", err)
			}
			assertRestoreRecoveryResult(t, f.live, "original")
			assertRestorePathMissing(t, f.old)
		})
	}
}

func TestRestoreRecoveryPreservesWALBackedData(t *testing.T) {
	f := newRestoreRecoveryFixture(t)
	f.seedCandidate(t)

	db, err := sql.Open("sqlite", "file:"+f.live)
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	if _, err := db.Exec(`PRAGMA journal_mode = WAL`); err != nil {
		_ = db.Close()
		t.Fatal(err)
	}
	if _, err := db.Exec(`PRAGMA wal_autocheckpoint = 0`); err != nil {
		_ = db.Close()
		t.Fatal(err)
	}
	frozen, err := os.ReadFile("../../internal/database/testdata/investment_schema77.sql")
	if err != nil {
		_ = db.Close()
		t.Fatal(err)
	}
	if _, err := db.Exec(string(frozen)); err != nil {
		_ = db.Close()
		t.Fatal(err)
	}
	for _, stmt := range []string{
		`INSERT INTO users(id,name,email,password_hash) VALUES('restore-user','Synthetic','restore@example.test','hash')`,
		`INSERT INTO workspaces(id,name) VALUES('restore-workspace','Synthetic')`,
		`INSERT INTO workspace_members(workspace_id,user_id,role) VALUES('restore-workspace','restore-user','ADMIN')`,
		`CREATE TABLE restore_marker (value TEXT NOT NULL)`,
		`INSERT INTO restore_marker (value) VALUES ('original')`,
		`CREATE TABLE restore_progress (value TEXT NOT NULL)`,
		`INSERT INTO restore_progress (value) VALUES ('uncheckpointed-wal-frame')`,
	} {
		if _, err := db.Exec(stmt); err != nil {
			_ = db.Close()
			t.Fatal(err)
		}
	}
	t.Cleanup(func() { _ = db.Close() })

	if err := database.CreateSQLiteBackupFile(db, f.live, f.backup); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(f.live, f.old); err != nil {
		t.Fatal(err)
	}
	_ = os.Remove(f.live + "-wal")
	_ = os.Remove(f.live + "-shm")
	f.writeJournal(t, restoreJournalPhasePrepared)

	if err := recoverInterruptedRestore(f.live); err != nil {
		t.Fatal(err)
	}
	assertRestoreRecoveryResult(t, f.live, "original")
	check, err := sql.Open("sqlite", "file:"+f.live+"?mode=ro")
	if err != nil {
		t.Fatal(err)
	}
	defer check.Close()
	var got string
	if err := check.QueryRow(`SELECT value FROM restore_progress LIMIT 1`).Scan(&got); err != nil {
		t.Fatalf("query WAL-backed row after recovery: %v", err)
	}
	if got != "uncheckpointed-wal-frame" {
		t.Fatalf("WAL-backed row = %q", got)
	}
	assertRestorePathMissing(t, f.old)
}

func TestRestoreRecoveryProcessCrashAtRestoreStages(t *testing.T) {
	if stage := os.Getenv("B1_RESTORE_CRASH_STAGE"); stage != "" {
		f := &restoreRecoveryFixture{
			root:      os.Getenv("DATA_DIR"),
			live:      filepath.Join(os.Getenv("DATA_DIR"), "contabase.db"),
			candidate: filepath.Join(os.Getenv("DATA_DIR"), "contabase-import-child.db"),
		}
		current := createRestoreTestServerRuntime(t, f.live, "original")
		f.seedCandidate(t)
		ops := &adminRestoreFileOps{checkpoint: func(at string) {
			if at == stage {
				os.Exit(91)
			}
		}}
		_, next, err := restoreSQLiteBackupFromFileWithOps(context.Background(), current, "file:"+f.live, f.live, f.candidate, openRestoreTestRuntimeDBs, startRestoreTestServerRuntime, validateSQLiteBackupCandidate, ops)
		if next != nil {
			_ = next.Close()
		}
		if err != nil {
			t.Fatalf("child restore: %v", err)
		}
		os.Exit(92)
	}

	for _, tc := range []struct {
		stage  string
		marker string
	}{
		{stage: "pre_restore_backup_created", marker: "original"},
		{stage: "restore_journal_prepared", marker: "original"},
		{stage: "commitment_point", marker: "original"},
		{stage: "candidate_installed", marker: "original"},
		{stage: "restore_journal_validated", marker: "candidate"},
	} {
		t.Run(tc.stage, func(t *testing.T) {
			root := t.TempDir()
			t.Setenv("DATA_DIR", root)
			cmd := exec.Command(os.Args[0], "-test.run=^TestRestoreRecoveryProcessCrashAtRestoreStages$")
			cmd.Env = []string{"DATA_DIR=" + root, "B1_RESTORE_CRASH_STAGE=" + tc.stage, "TMPDIR=" + os.TempDir(), "TZ=UTC"}
			err := cmd.Run()
			var exit *exec.ExitError
			if !errors.As(err, &exit) || exit.ExitCode() != 91 {
				t.Fatalf("child exit: %v", err)
			}

			live := filepath.Join(root, "contabase.db")
			if err := recoverInterruptedRestore(live); err != nil {
				t.Fatalf("recover interrupted restore: %v", err)
			}
			secureMode := tc.stage != "pre_restore_backup_created"
			assertRestoreRecoveryResultMode(t, live, tc.marker, secureMode)
			working, err := legacyRestoreWorkingFiles(live)
			if err != nil {
				t.Fatal(err)
			}
			if len(working) != 0 {
				t.Fatalf("working copies left after recovery: %v", working)
			}
		})
	}
}

func TestRestoreRecoveryProcessCrashDuringRecovery(t *testing.T) {
	if stage := os.Getenv("B1_RECOVERY_CRASH_STAGE"); stage != "" {
		live := filepath.Join(os.Getenv("DATA_DIR"), "contabase.db")
		ops := &restoreRecoveryOps{checkpoint: func(at string) error {
			if at == stage {
				os.Exit(91)
			}
			return nil
		}}
		if err := recoverInterruptedRestoreWithOps(live, ops); err != nil {
			t.Fatalf("child recovery: %v", err)
		}
		os.Exit(92)
	}

	for _, stage := range []string{"recovery_rollback_active_removed", "recovery_rollback_published"} {
		t.Run(stage, func(t *testing.T) {
			f := newRestoreRecoveryFixture(t)
			f.seedOriginal(t)
			f.seedBackup(t)
			f.seedCandidate(t)
			if err := os.Rename(f.live, f.old); err != nil {
				t.Fatal(err)
			}
			f.writeJournal(t, restoreJournalPhasePrepared)

			cmd := exec.Command(os.Args[0], "-test.run=^TestRestoreRecoveryProcessCrashDuringRecovery$")
			cmd.Env = []string{"DATA_DIR=" + f.root, "B1_RECOVERY_CRASH_STAGE=" + stage, "TMPDIR=" + os.TempDir(), "TZ=UTC"}
			err := cmd.Run()
			var exit *exec.ExitError
			if !errors.As(err, &exit) || exit.ExitCode() != 91 {
				t.Fatalf("child exit: %v", err)
			}

			if err := recoverInterruptedRestore(f.live); err != nil {
				t.Fatalf("second recovery: %v", err)
			}
			assertRestoreRecoveryResult(t, f.live, "original")
			assertRestorePathMissing(t, f.old)
		})
	}
}
