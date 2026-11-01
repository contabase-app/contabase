package database

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"modernc.org/sqlite"
)

// CreateSQLiteBackupFile publishes a standalone SQLite snapshot. destPath must
// not exist; even an empty file or symlink is never overwritten.
func CreateSQLiteBackupFile(db *sql.DB, dbPath, destPath string) error {
	return CreateSQLiteBackupFileContext(context.Background(), db, dbPath, destPath)
}

// CreateSQLiteBackupFileContext captures committed data, including WAL frames,
// using SQLite's Online Backup API. Callers own the runtime generation: they
// must not replace the source pathname until this call returns.
func CreateSQLiteBackupFileContext(ctx context.Context, db *sql.DB, dbPath, destPath string) error {
	return createSQLiteBackupFile(ctx, db, dbPath, destPath, nil)
}

// Per-call checkpoints allow deterministic fault/concurrency tests without
// global hooks or changes to SQLite's locking behavior.
type sqliteBackupHooks struct {
	afterStep     func() error
	beforePublish func(string) error
	publication   *backupPublicationOps
}

func createSQLiteBackupFile(ctx context.Context, db *sql.DB, dbPath, destPath string, hooks *sqliteBackupHooks) (retErr error) {
	if err := ctx.Err(); err != nil {
		return err
	}
	if db == nil {
		return fmt.Errorf("nil database")
	}
	dbPath = strings.TrimSpace(dbPath)
	if strings.HasPrefix(dbPath, "file:") {
		u, err := url.Parse(dbPath)
		if err != nil {
			return err
		}
		dbPath = u.Path
		if u.Opaque != "" {
			dbPath, err = url.PathUnescape(u.Opaque)
			if err != nil {
				return err
			}
		}
	}
	if !sqliteDatabaseFileExists(dbPath) {
		return fmt.Errorf("sqlite database file not found")
	}
	if strings.TrimSpace(destPath) == "" {
		return fmt.Errorf("backup destination path is required")
	}
	destAbs, err := filepath.Abs(destPath)
	if err != nil {
		return err
	}
	// Reject mismatched handles/paths before opening a separate reader.
	var sourcePath string
	if err := db.QueryRowContext(ctx, `SELECT file FROM pragma_database_list WHERE name = 'main'`).Scan(&sourcePath); err != nil {
		return fmt.Errorf("identify backup source: %w", err)
	}
	sourceInfo, err := os.Stat(sourcePath)
	if err != nil {
		return err
	}
	expectedInfo, err := os.Stat(dbPath)
	if err != nil || !os.SameFile(sourceInfo, expectedInfo) {
		return fmt.Errorf("backup source does not match database path")
	}
	if _, err := os.Lstat(destAbs); !errors.Is(err, os.ErrNotExist) {
		if err != nil {
			return err
		}
		return fmt.Errorf("backup destination already exists: %w", os.ErrExist)
	}
	parent := filepath.Dir(destAbs)
	if err := os.MkdirAll(parent, 0o755); err != nil {
		return err
	}
	// Retain directory access for cleanup even if its read permission is lost
	// during capture. Cleanup errors must not be silently discarded.
	parentRoot, err := os.OpenRoot(parent)
	if err != nil {
		return err
	}
	defer parentRoot.Close()
	// Same filesystem as destination; private staging also protects sidecars.
	stageDir, err := os.MkdirTemp(parent, ".contabase-snapshot-*")
	if err != nil {
		return err
	}
	defer func() {
		retErr = errors.Join(retErr, parentRoot.RemoveAll(filepath.Base(stageDir)))
	}()
	stagePath := filepath.Join(stageDir, "snapshot.db")
	f, err := os.OpenFile(stagePath, os.O_CREATE|os.O_EXCL|os.O_RDWR, 0o600)
	if err != nil {
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	if err := captureSQLiteSnapshot(ctx, sourcePath, stagePath, hooks); err != nil {
		return fmt.Errorf("capture SQLite snapshot: %w", err)
	}
	if hooks != nil && hooks.beforePublish != nil {
		if err := hooks.beforePublish(stagePath); err != nil {
			return err
		}
	}
	if err := validateSQLiteSnapshot(ctx, stagePath); err != nil {
		return fmt.Errorf("validate SQLite snapshot: %w", err)
	}
	f, err = os.OpenFile(stagePath, os.O_RDWR, 0)
	if err != nil {
		return err
	}
	var publication *backupPublicationOps
	if hooks != nil {
		publication = hooks.publication
	}
	syncErr := publication.fileSync(f)
	closeErr := f.Close()
	if err := errors.Join(syncErr, closeErr); err != nil {
		return fmt.Errorf("sync SQLite snapshot: %w", err)
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	// Link is atomic and does not overwrite a destination that appeared during
	// capture. The complete, synced inode becomes visible at this commit point.
	published, err := publishBackupFile(ctx, stagePath, destAbs, publication)
	if err != nil {
		return fmt.Errorf("publish SQLite snapshot: %w", err)
	}
	if !published {
		return fmt.Errorf("backup destination already exists: %w", os.ErrExist)
	}
	return nil
}

func sqliteBackupURI(path string, query string) string {
	u := &url.URL{Scheme: "file", Path: filepath.ToSlash(path), RawQuery: query}
	// A relative path would otherwise render as file://data/db, and SQLite
	// reads the first segment as an URI authority. Keeping the host omitted
	// yields file:data/db, which resolves against the working directory.
	if !filepath.IsAbs(path) {
		u.OmitHost = true
	}
	return u.String()
}

func captureSQLiteSnapshot(ctx context.Context, sourcePath, destPath string, hooks *sqliteBackupHooks) error {
	// Dedicated read-only/private-cache connection: no lease of the normal or
	// Immediate pool throughout capture, no migrations, no jobs, no write lock.
	source, err := sql.Open("sqlite", sqliteBackupURI(sourcePath, "mode=ro&cache=private&_pragma=busy_timeout(1000)"))
	if err != nil {
		return err
	}
	defer source.Close()
	source.SetMaxOpenConns(1)
	conn, err := source.Conn(ctx)
	if err != nil {
		return err
	}
	defer conn.Close()
	tx, err := conn.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return err
	}
	defer tx.Rollback()
	// BEGIN is deferred. This read fixes the WAL end mark before the first
	// backup step. Keep the transaction open between steps so commits from
	// other connections cannot restart the backup or change its snapshot.
	var schemaCount int
	if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM sqlite_schema`).Scan(&schemaCount); err != nil {
		return err
	}
	err = conn.Raw(func(raw any) (retErr error) {
		backuper, ok := raw.(interface {
			NewBackup(string) (*sqlite.Backup, error)
		})
		if !ok {
			return fmt.Errorf("SQLite driver does not support online backup")
		}
		backup, err := backuper.NewBackup(sqliteBackupURI(destPath, "mode=rw&_pragma=synchronous(FULL)"))
		if err != nil {
			return err
		}
		finished := false
		defer func() {
			if !finished {
				retErr = errors.Join(retErr, backup.Finish())
			}
		}()
		for {
			if err := ctx.Err(); err != nil {
				return err
			}
			more, err := backup.Step(128)
			if err != nil {
				return err // BUSY/LOCKED also fail closed; no unbounded retries.
			}
			if hooks != nil && hooks.afterStep != nil {
				if err := hooks.afterStep(); err != nil {
					return err
				}
			}
			if !more {
				break // Only SQLITE_DONE is successful completion.
			}
		}
		// Commit calls sqlite3_backup_finish and transfers ownership of the
		// destination handle, allowing us to check its Close error too.
		dest, err := backup.Commit()
		finished = true
		if err != nil {
			return err
		}
		return dest.Close()
	})
	if err != nil {
		return err
	}
	return errors.Join(tx.Rollback(), ctx.Err()) // Release source before validation/fsync.
}

func validateSQLiteSnapshot(ctx context.Context, path string) error {
	// immutable avoids creating WAL/SHM beside the finished, standalone file.
	db, err := sql.Open("sqlite", sqliteBackupURI(path, "mode=ro&immutable=1"))
	if err != nil {
		return err
	}
	defer db.Close()
	var result string
	if err := db.QueryRowContext(ctx, `PRAGMA integrity_check`).Scan(&result); err != nil {
		return err
	}
	if result != "ok" {
		return fmt.Errorf("integrity_check failed")
	}
	// Do not impose new foreign-key/schema policy here: pre-migration backups
	// must also preserve legacy databases before their compatibility repairs.
	// Snapshot isolation is guaranteed by capture, not by integrity_check.
	return nil
}
