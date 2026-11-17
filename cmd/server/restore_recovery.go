package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/contabase-app/contabase/internal/database"
	"github.com/contabase-app/contabase/internal/paths"
)

const (
	restoreJournalVersion        = 1
	restoreJournalPhasePrepared  = "prepared"
	restoreJournalPhaseValidated = "validated"

	restoreRecoveryStagingSuffix = ".restore-recovery.tmp"
)

// restoreJournal is written durably before the live database is renamed and is
// removed only after the restore is complete or fully rolled back. Its presence
// makes an interrupted swap recoverable on the next startup.
type restoreJournal struct {
	Version              int    `json:"version"`
	Phase                string `json:"phase"`
	DatabasePath         string `json:"database_path"`
	OriginalPath         string `json:"original_path"`
	CandidatePath        string `json:"candidate_path"`
	PreRestoreBackupPath string `json:"pre_restore_backup_path"`
	UpdatedAt            string `json:"updated_at"`
}

// restoreRecoveryOps carries per-call checkpoints for deterministic crash and
// retry tests. Production always uses the real filesystem operations.
type restoreRecoveryOps struct {
	checkpoint func(string) error
}

func (ops *restoreRecoveryOps) at(stage string) error {
	if ops == nil || ops.checkpoint == nil {
		return nil
	}
	return ops.checkpoint(stage)
}

func restoreJournalPath(dbPath string) string {
	return dbPath + ".restore-journal"
}

func restoreRecoveryStagingPath(dbPath string) string {
	return dbPath + restoreRecoveryStagingSuffix
}

func openRuntimeDBsAfterRestoreRecovery(dbURL string) (*database.RuntimeDBs, error) {
	if err := recoverInterruptedRestore(sqlitePathFromURL(dbURL)); err != nil {
		return nil, fmt.Errorf("recover interrupted restore: %w", err)
	}
	return database.OpenRuntimeDBs(dbURL)
}

// recoverInterruptedRestore runs before the database is opened for normal use.
// It must never create a fresh database while a previous restore swap is in an
// unknown state.
func recoverInterruptedRestore(dbPath string) error {
	return recoverInterruptedRestoreWithOps(dbPath, nil)
}

func recoverInterruptedRestoreWithOps(dbPath string, ops *restoreRecoveryOps) error {
	if !isSQLiteDiskDatabasePath(dbPath) {
		return nil
	}
	canonicalDBPath, err := canonicalRestorePath(dbPath)
	if err != nil {
		return err
	}
	dbPath = canonicalDBPath
	dbDir := filepath.Dir(dbPath)
	if _, err := os.Stat(dbDir); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return fmt.Errorf("inspect database directory: %w", err)
	}
	journalPath := restoreJournalPath(dbPath)

	info, err := os.Lstat(journalPath)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return recoverLegacyRestoreArtifacts(dbPath)
		}
		return fmt.Errorf("inspect restore journal: %w", err)
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("restore journal is not a regular file: %s", journalPath)
	}
	data, err := os.ReadFile(journalPath)
	if err != nil {
		return fmt.Errorf("read restore journal: %w", err)
	}
	var journal restoreJournal
	if err := json.Unmarshal(data, &journal); err != nil {
		return fmt.Errorf("restore journal is unreadable: %w", err)
	}
	if err := validateRestoreJournal(journal, dbPath); err != nil {
		return err
	}
	return recoverJournaledRestore(journal, ops)
}

// restoreCandidateStagingPrefixes lista os prefixos dos diretórios de staging
// reservados que os writers reais de restore (admin e bootstrap) criam dentro
// do diretório do banco via os.MkdirTemp.
var restoreCandidateStagingPrefixes = []string{".contabase-import-", ".contabase-bootstrap-restore-"}

// validRestoreCandidatePath aceita exatamente os dois layouts de candidato que
// os writers de restore produzem:
//
//  1. layout plano legado: arquivo "contabase-import-*.db" ou
//     "contabase-bootstrap-restore-*.db" direto no diretório do banco;
//  2. staging real dos writers: "<dbdir>/.contabase-import-<rand>/<upload>.db"
//     ou "<dbdir>/.contabase-bootstrap-restore-<rand>/<upload>.db", onde
//     <upload> é o nome sanitizado do upload (extensão .db aceita em qualquer
//     caixa, espelhando sanitizeRestoreUploadName).
//
// Qualquer outra estrutura — outro diretório, aninhamento mais profundo,
// caminho fora do diretório do banco ou extensão que não seja .db — é
// rejeitada.
func validRestoreCandidatePath(candidatePath, dbDir string) bool {
	candidateBase := filepath.Base(candidatePath)
	if !strings.EqualFold(filepath.Ext(candidateBase), ".db") {
		return false
	}
	candidateDir := filepath.Dir(candidatePath)
	if candidateDir == dbDir {
		return strings.HasPrefix(candidateBase, "contabase-import-") ||
			strings.HasPrefix(candidateBase, "contabase-bootstrap-restore-")
	}
	if filepath.Dir(candidateDir) != dbDir {
		return false
	}
	stagingBase := filepath.Base(candidateDir)
	for _, prefix := range restoreCandidateStagingPrefixes {
		if strings.HasPrefix(stagingBase, prefix) {
			return true
		}
	}
	return false
}

func validateRestoreJournal(journal restoreJournal, dbPath string) error {
	if journal.Version != restoreJournalVersion {
		return fmt.Errorf("unsupported restore journal version: %d", journal.Version)
	}
	switch journal.Phase {
	case restoreJournalPhasePrepared, restoreJournalPhaseValidated:
	default:
		return fmt.Errorf("invalid restore journal phase: %q", journal.Phase)
	}

	journalDBPath, err := canonicalRestorePath(journal.DatabasePath)
	if err != nil {
		return fmt.Errorf("invalid restore journal database path: %w", err)
	}
	if journalDBPath != dbPath {
		return fmt.Errorf("restore journal belongs to another database")
	}
	dbDir := filepath.Dir(dbPath)
	dbBase := filepath.Base(dbPath)

	originalPath, err := canonicalRestorePath(journal.OriginalPath)
	if err != nil {
		return fmt.Errorf("invalid restore journal original path: %w", err)
	}
	if filepath.Dir(originalPath) != dbDir {
		return fmt.Errorf("restore journal original path escaped the database directory")
	}
	originalBase := filepath.Base(originalPath)
	if !strings.HasPrefix(originalBase, dbBase+".restore-current-") || !strings.HasSuffix(originalBase, ".db") {
		return fmt.Errorf("restore journal original path is not a staged database")
	}

	candidatePath, err := canonicalRestorePath(journal.CandidatePath)
	if err != nil {
		return fmt.Errorf("invalid restore journal candidate path: %w", err)
	}
	if !validRestoreCandidatePath(candidatePath, dbDir) {
		return fmt.Errorf("restore journal candidate path is not a restore upload")
	}

	backupPath, err := canonicalRestorePath(journal.PreRestoreBackupPath)
	if err != nil {
		return fmt.Errorf("invalid restore journal backup path: %w", err)
	}
	backupBase := filepath.Base(backupPath)
	if !strings.HasPrefix(backupBase, "contabase-pre-restore-") || !strings.HasSuffix(backupBase, ".db") {
		return fmt.Errorf("restore journal backup path is not a pre-restore backup")
	}
	backupDir := filepath.Dir(backupPath)
	allowedBackupDir, err := canonicalRestorePath(filepath.Join(paths.BackupsDir(), "pre-restore"))
	if err != nil {
		return fmt.Errorf("invalid pre-restore backup directory: %w", err)
	}
	if backupDir != dbDir && backupDir != allowedBackupDir {
		return fmt.Errorf("restore journal backup path escaped the installation")
	}
	return nil
}

func recoverJournaledRestore(journal restoreJournal, ops *restoreRecoveryOps) error {
	if journal.Phase == restoreJournalPhaseValidated {
		if err := validateRecoveryDatabase(journal.DatabasePath); err == nil {
			return acceptRecoveredRestore(journal, ops)
		} else {
			slog.Warn("restore recovery rejected validated database", "path", journal.DatabasePath, "error", err)
		}
	}

	originalExists := regularFileExists(journal.OriginalPath)
	if !originalExists {
		if err := validateRecoveryDatabase(journal.DatabasePath); err == nil {
			return acceptRecoveredRestore(journal, ops)
		}
	}
	return rollbackJournaledRestore(journal, ops)
}

func acceptRecoveredRestore(journal restoreJournal, ops *restoreRecoveryOps) error {
	if err := validateRecoveryDatabase(journal.DatabasePath); err != nil {
		return fmt.Errorf("recover interrupted restore: %w", err)
	}
	if err := ensureRestoredDatabasePermissions(journal.DatabasePath); err != nil {
		return fmt.Errorf("set recovered database permissions: %w", err)
	}
	if err := syncRestoreDirectory(filepath.Dir(journal.DatabasePath)); err != nil {
		return fmt.Errorf("sync recovered database directory: %w", err)
	}
	if err := ops.at("recovery_accept_validated"); err != nil {
		return err
	}
	if err := removeRestoreWorkingCopy(journal.OriginalPath); err != nil {
		return err
	}
	if err := ops.at("recovery_accept_cleaned"); err != nil {
		return err
	}
	if err := removeRestoreJournal(restoreJournalPath(journal.DatabasePath)); err != nil {
		return fmt.Errorf("remove restore journal: %w", err)
	}
	slog.Warn("restore recovery accepted installed database", "path", journal.DatabasePath)
	return nil
}

func rollbackJournaledRestore(journal restoreJournal, ops *restoreRecoveryOps) error {
	if err := validateRecoveryDatabase(journal.PreRestoreBackupPath); err != nil {
		return fmt.Errorf("recover interrupted restore: pre-restore backup is not usable: %w", err)
	}
	if err := ops.at("recovery_rollback_before_remove"); err != nil {
		return err
	}
	if err := removeSQLiteSidecars(journal.DatabasePath); err != nil {
		return fmt.Errorf("clean interrupted restore sidecars: %w", err)
	}
	if err := removeRegularFile(journal.DatabasePath); err != nil {
		return fmt.Errorf("remove interrupted restore database: %w", err)
	}
	if err := ops.at("recovery_rollback_active_removed"); err != nil {
		return err
	}
	if err := copyRestoreRecoveryFile(journal.PreRestoreBackupPath, journal.DatabasePath); err != nil {
		return fmt.Errorf("restore pre-restore backup: %w", err)
	}
	if err := ops.at("recovery_rollback_published"); err != nil {
		return err
	}
	if err := ensureRestoredDatabasePermissions(journal.DatabasePath); err != nil {
		return fmt.Errorf("set recovered database permissions: %w", err)
	}
	if err := validateRecoveryDatabase(journal.DatabasePath); err != nil {
		return fmt.Errorf("validate recovered database: %w", err)
	}
	if err := removeRestoreWorkingCopy(journal.OriginalPath); err != nil {
		return err
	}
	if err := ops.at("recovery_rollback_cleaned"); err != nil {
		return err
	}
	if err := removeRestoreJournal(restoreJournalPath(journal.DatabasePath)); err != nil {
		return fmt.Errorf("remove restore journal: %w", err)
	}
	slog.Warn("restore recovery rolled back interrupted restore", "path", journal.DatabasePath, "backup", journal.PreRestoreBackupPath)
	return nil
}

func copyRestoreRecoveryFile(src, dbPath string) error {
	stagingPath := restoreRecoveryStagingPath(dbPath)
	if err := removeRegularFile(stagingPath); err != nil {
		return fmt.Errorf("remove stale recovery staging file: %w", err)
	}
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(stagingPath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	removeStaging := true
	defer func() {
		if removeStaging {
			_ = os.Remove(stagingPath)
		}
	}()
	if _, err := io.Copy(out, in); err != nil {
		_ = out.Close()
		return err
	}
	if err := out.Sync(); err != nil {
		_ = out.Close()
		return err
	}
	if err := out.Close(); err != nil {
		return err
	}
	if err := os.Rename(stagingPath, dbPath); err != nil {
		return err
	}
	removeStaging = false
	return syncRestoreDirectory(filepath.Dir(dbPath))
}

func validateRecoveryDatabase(path string) error {
	if !isSQLiteDiskDatabasePath(path) {
		return fmt.Errorf("recovery database path is not disk-backed")
	}
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("recovery database is not a regular file")
	}
	return validateContaBaseBackupCandidate(path)
}

func recoverLegacyRestoreArtifacts(dbPath string) error {
	if _, err := os.Lstat(dbPath); err == nil {
		if working, err := legacyRestoreWorkingFiles(dbPath); err == nil && len(working) > 0 {
			slog.Warn("stale restore working copy preserved", "path", dbPath, "files", strings.Join(working, ","))
		}
		return nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("inspect database path: %w", err)
	}

	working, err := legacyRestoreWorkingFiles(dbPath)
	if err != nil {
		return err
	}
	switch len(working) {
	case 0:
		if err := refuseUnknownRestoreArtifacts(dbPath); err != nil {
			return err
		}
		return nil
	case 1:
		oldPath := working[0]
		if err := validateRecoveryDatabase(oldPath); err != nil {
			return fmt.Errorf("interrupted restore working copy is not usable: %w", err)
		}
		// The SQLite sidecars still belong to this database, so rename before
		// touching them. This preserves WAL frames from a crash after the first
		// rename.
		if err := os.Rename(oldPath, dbPath); err != nil {
			return fmt.Errorf("restore interrupted restore working copy: %w", err)
		}
		if err := syncRestoreDirectory(filepath.Dir(dbPath)); err != nil {
			return fmt.Errorf("sync recovered database directory: %w", err)
		}
		if err := ensureRestoredDatabasePermissions(dbPath); err != nil {
			return fmt.Errorf("set recovered database permissions: %w", err)
		}
		if err := validateRecoveryDatabase(dbPath); err != nil {
			return fmt.Errorf("validate recovered database: %w", err)
		}
		slog.Warn("restore recovery used legacy working copy", "path", dbPath, "working_copy", oldPath)
		return nil
	default:
		return fmt.Errorf("ambiguous interrupted restore: multiple working copies for %s: %s", dbPath, strings.Join(working, ","))
	}
}

func refuseUnknownRestoreArtifacts(dbPath string) error {
	dbDir := filepath.Dir(dbPath)
	entries, err := os.ReadDir(dbDir)
	if err != nil {
		return fmt.Errorf("inspect restore artifacts: %w", err)
	}
	dbBase := filepath.Base(dbPath)
	journalTmpPrefix := "." + filepath.Base(restoreJournalPath(dbPath)) + ".tmp-"
	var artifacts []string
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		name := entry.Name()
		switch {
		case name == dbBase+restoreRecoveryStagingSuffix:
			artifacts = append(artifacts, name)
		case strings.HasPrefix(name, dbBase+".restore-current-"):
			artifacts = append(artifacts, name)
		case strings.HasPrefix(name, journalTmpPrefix):
			artifacts = append(artifacts, name)
		case strings.HasPrefix(name, "contabase-import-") && strings.HasSuffix(name, ".db"):
			artifacts = append(artifacts, name)
		case strings.HasPrefix(name, "contabase-bootstrap-restore-") && strings.HasSuffix(name, ".db"):
			artifacts = append(artifacts, name)
		}
	}
	if len(artifacts) > 0 {
		return fmt.Errorf("restore artifacts exist but no recoverable database: %s", strings.Join(artifacts, ","))
	}
	for _, suffix := range []string{"-wal", "-shm"} {
		if regularFileExists(dbPath + suffix) {
			return fmt.Errorf("sqlite sidecar exists without the active database: %s", dbPath+suffix)
		}
	}
	return nil
}

func legacyRestoreWorkingFiles(dbPath string) ([]string, error) {
	entries, err := os.ReadDir(filepath.Dir(dbPath))
	if err != nil {
		return nil, fmt.Errorf("inspect restore working copies: %w", err)
	}
	prefix := filepath.Base(dbPath) + ".restore-current-"
	var files []string
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		name := entry.Name()
		if strings.HasPrefix(name, prefix) && strings.HasSuffix(name, ".db") {
			files = append(files, filepath.Join(filepath.Dir(dbPath), name))
		}
	}
	return files, nil
}

func removeRestoreWorkingCopy(path string) error {
	if err := removeSQLiteSidecars(path); err != nil {
		return fmt.Errorf("clean restore working copy sidecars: %w", err)
	}
	if err := removeRegularFile(path); err != nil {
		return fmt.Errorf("remove restore working copy: %w", err)
	}
	return nil
}

func writeRestoreJournal(path string, journal restoreJournal) error {
	journal.Version = restoreJournalVersion
	journal.UpdatedAt = time.Now().UTC().Format(time.RFC3339Nano)
	data, err := json.MarshalIndent(journal, "", "  ")
	if err != nil {
		return err
	}
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, "."+filepath.Base(path)+".tmp-*")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	removeTmp := true
	defer func() {
		if removeTmp {
			_ = os.Remove(tmpPath)
		}
	}()
	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmpPath, path); err != nil {
		return err
	}
	removeTmp = false
	return syncRestoreDirectory(dir)
}

func removeRestoreJournal(path string) error {
	if err := removeRegularFile(path); err != nil {
		return err
	}
	return syncRestoreDirectory(filepath.Dir(path))
}

func regularFileExists(path string) bool {
	info, err := os.Lstat(path)
	return err == nil && info.Mode().IsRegular()
}

func removeRegularFile(path string) error {
	err := os.Remove(path)
	if err == nil || errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return err
}

func syncRestoreDirectory(path string) error {
	dir, err := os.Open(path)
	if err != nil {
		return err
	}
	return errors.Join(dir.Sync(), dir.Close())
}

func canonicalRestorePath(path string) (string, error) {
	path = strings.TrimSpace(path)
	if path == "" {
		return "", fmt.Errorf("empty path")
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	return filepath.Clean(abs), nil
}
