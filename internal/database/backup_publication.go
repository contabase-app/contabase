package database

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"syscall"
	"time"
)

// Operations are per invocation, so fault tests cannot affect another backup.
// Nil fields always use the real filesystem operation.
type backupPublicationOps struct {
	link     func(string, string) error
	syncDir  func(string) error
	remove   func(string) error
	syncFile func(*os.File) error
}

func (ops *backupPublicationOps) directorySync(path string) error {
	if ops != nil && ops.syncDir != nil {
		return ops.syncDir(path)
	}
	return syncDirectory(path)
}

func (ops *backupPublicationOps) fileSync(f *os.File) error {
	if ops != nil && ops.syncFile != nil {
		return ops.syncFile(f)
	}
	return f.Sync()
}

// Serialize only publication/confirmation in this directory, including across
// processes. SQLite capture and financial writers never hold or take this lock.
// In particular, a retry cannot confirm a link that another publisher is about
// to roll back. No lock file, marker or in-memory completion state is needed.
func withBackupPublicationLock(ctx context.Context, dir string, fn func() error) (retErr error) {
	f, err := os.Open(dir)
	if err != nil {
		return err
	}
	defer func() { retErr = errors.Join(retErr, f.Close()) }()
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
		if err == nil {
			break
		}
		if !errors.Is(err, syscall.EWOULDBLOCK) && !errors.Is(err, syscall.EAGAIN) {
			return fmt.Errorf("lock backup publication directory: %w", err)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(10 * time.Millisecond):
		}
	}
	defer func() {
		retErr = errors.Join(retErr, syscall.Flock(int(f.Fd()), syscall.LOCK_UN))
	}()
	return fn()
}

// published means a link was created, not that durability was confirmed. A
// non-nil error must always be handled, even when published is true.
func publishBackupFile(ctx context.Context, sourcePath, destPath string, ops *backupPublicationOps) (published bool, err error) {
	dir := filepath.Dir(destPath)
	err = withBackupPublicationLock(ctx, dir, func() error {
		published, err = publishBackupFileLocked(ctx, sourcePath, destPath, ops)
		return err
	})
	return published, err
}

// Caller holds the destination directory publication lock.
func publishBackupFileLocked(ctx context.Context, sourcePath, destPath string, ops *backupPublicationOps) (published bool, err error) {
	dir := filepath.Dir(destPath)
	err = func() error {
		if err := ctx.Err(); err != nil {
			return err
		}
		sourceInfo, err := os.Lstat(sourcePath)
		if err != nil {
			return err
		}
		if !sourceInfo.Mode().IsRegular() {
			return fmt.Errorf("backup publication source is not regular")
		}
		link := os.Link
		remove := os.Remove
		if ops != nil {
			if ops.link != nil {
				link = ops.link
			}
			if ops.remove != nil {
				remove = ops.remove
			}
		}
		if err := link(sourcePath, destPath); err != nil {
			if os.IsExist(err) {
				return nil
			}
			return err
		}
		published = true
		// The link is the commit boundary: cancellation after it does not skip
		// durability/rollback. A completed sync may still report success.
		if err := ops.directorySync(dir); err != nil {
			current, inspectErr := os.Lstat(destPath)
			if inspectErr != nil || !current.Mode().IsRegular() || !os.SameFile(sourceInfo, current) {
				return fmt.Errorf("backup publication unconfirmed; destination changed, refusing rollback: %w", errors.Join(err, inspectErr))
			}
			cleanupErr := remove(destPath)
			if cleanupErr != nil {
				return fmt.Errorf("backup publication unconfirmed at %q; retry must reconfirm durability: %w", destPath, errors.Join(err, cleanupErr))
			}
			return errors.Join(err, ops.directorySync(dir))
		}
		return nil
	}()
	return published, err
}
