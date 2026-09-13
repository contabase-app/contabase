package database

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

const (
	AutomaticBackupRetentionDays = 7
	automaticBackupCheckInterval = time.Hour
	dailyAutomaticBackupPrefix   = "contabase-daily-"
)

type AutomaticBackupMetadata struct {
	Name      string
	CreatedAt time.Time
	Size      int64
	Day       time.Time
}

func ensureDailyAutomaticBackup(db *sql.DB, dbPath, backupDir string, civilNow time.Time, retentionDays int) error {
	return ensureDailyAutomaticBackupContext(context.Background(), db, dbPath, backupDir, civilNow, retentionDays)
}

func ensureDailyAutomaticBackupContext(ctx context.Context, db *sql.DB, dbPath, backupDir string, civilNow time.Time, retentionDays int) error {
	return ensureDailyAutomaticBackupWithPublication(ctx, db, dbPath, backupDir, civilNow, retentionDays, nil)
}

func ensureDailyAutomaticBackupWithPublication(ctx context.Context, db *sql.DB, dbPath, backupDir string, civilNow time.Time, retentionDays int, ops *backupPublicationOps) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if db == nil {
		return fmt.Errorf("nil database")
	}
	if retentionDays < 1 {
		retentionDays = 1
	}
	if civilNow.IsZero() {
		return fmt.Errorf("civil backup time is required")
	}
	if !sqliteDatabaseFileExists(dbPath) {
		return fmt.Errorf("sqlite database file not found")
	}
	if strings.TrimSpace(backupDir) == "" {
		return fmt.Errorf("automatic backup directory is required")
	}
	if err := os.MkdirAll(backupDir, 0o755); err != nil {
		return fmt.Errorf("create automatic backup directory: %w", err)
	}

	today := civilDay(civilNow)
	// Unattributed legacy files are preserved, never adopted as today's capture.

	destPath := filepath.Join(backupDir, dailyAutomaticBackupName(today))
	exists, err := confirmExistingBackup(ctx, destPath, ops)
	if err != nil {
		return err
	}
	if !exists {
		stageDir, err := os.MkdirTemp(backupDir, ".contabase-daily-*")
		if err != nil {
			return fmt.Errorf("create daily backup staging directory: %w", err)
		}
		defer os.RemoveAll(stageDir)
		tmpPath := filepath.Join(stageDir, "snapshot.db")

		if err := CreateSQLiteBackupFileContext(ctx, db, dbPath, tmpPath); err != nil {
			return fmt.Errorf("create daily automatic backup: %w", err)
		}
		info, err := os.Stat(tmpPath)
		if err != nil {
			return fmt.Errorf("inspect daily backup staging file: %w", err)
		}
		if !info.Mode().IsRegular() || info.Size() == 0 {
			return fmt.Errorf("daily backup staging file is invalid")
		}
		if err := os.Chmod(tmpPath, 0o444); err != nil {
			return fmt.Errorf("protect daily backup staging file: %w", err)
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		published, err := publishDailyBackup(ctx, tmpPath, destPath, ops)
		if err != nil {
			return fmt.Errorf("publish daily automatic backup: %w", err)
		}
		if !published {
			if exists, err := confirmExistingBackup(ctx, destPath, ops); err != nil {
				return err
			} else if !exists {
				return fmt.Errorf("concurrent daily backup disappeared before confirmation")
			}
		}
	}

	if err := pruneAutomaticBackups(ctx, backupDir, today, retentionDays, ops); err != nil {
		return err
	}
	return nil
}

// ListDailyAutomaticBackups inventories filenames only, including unattributed
// legacy daily files. It does not certify provenance, integrity or completion.
func ListDailyAutomaticBackups(dir string, limit int) ([]AutomaticBackupMetadata, error) {
	if limit <= 0 {
		return nil, nil
	}
	entries, err := os.ReadDir(dir)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}

	items := make([]AutomaticBackupMetadata, 0, len(entries))
	for _, entry := range entries {
		day, ok := parseDailyAutomaticBackupName(entry.Name())
		if !ok || entry.IsDir() || entry.Type()&os.ModeSymlink != 0 {
			continue
		}
		info, err := entry.Info()
		if err != nil || !info.Mode().IsRegular() || info.Size() == 0 {
			continue
		}
		items = append(items, AutomaticBackupMetadata{
			Name:      entry.Name(),
			CreatedAt: info.ModTime(),
			Size:      info.Size(),
			Day:       day,
		})
	}
	sort.Slice(items, func(i, j int) bool {
		if items[i].Day.Equal(items[j].Day) {
			return items[i].Name > items[j].Name
		}
		return items[i].Day.After(items[j].Day)
	})
	if len(items) > limit {
		items = items[:limit]
	}
	return items, nil
}

// Retention owns only verified protocol pairs. Legacy, unknown and mismatched
// files are preserved. Deletion is serialized with publication/reconfirmation.
func pruneAutomaticBackups(ctx context.Context, dir string, today time.Time, retentionDays int, ops *backupPublicationOps) error {
	return withBackupPublicationLock(ctx, dir, func() error {
		entries, err := os.ReadDir(dir)
		if err != nil {
			return err
		}
		cutoff := today.AddDate(0, 0, -(retentionDays - 1))
		for _, entry := range entries {
			day, ok := parseDailyAutomaticBackupName(entry.Name())
			if !ok || !civilDayInLocation(day, today.Location()).Before(cutoff) {
				continue
			}
			path := filepath.Join(dir, entry.Name())
			pair, err := openDailyBackupPair(ctx, path)
			if err != nil {
				if ctx.Err() != nil {
					return ctx.Err()
				}
				continue // No proof of ownership: no deletion.
			}
			if pair == nil {
				continue
			}
			err = pair.checkIdentity(path)
			if err == nil {
				err = os.Remove(path)
			}
			// Sync removal of the DB before removing its proof. A crash leaves an
			// orphan proof, never an unproven file eligible for automated deletion.
			if err == nil {
				err = ops.directorySync(dir)
			}
			if err == nil {
				err = sameBackupFile(pair.proof, dailyBackupProofPath(path))
			}
			if err == nil {
				err = os.Remove(dailyBackupProofPath(path))
			}
			if err == nil {
				err = ops.directorySync(dir)
			}
			err = errors.Join(err, pair.close())
			if err != nil {
				return fmt.Errorf("retain daily backups: %w", err)
			}
		}
		return nil
	})
}

func syncDirectory(dir string) error {
	f, err := os.Open(dir)
	if err != nil {
		return err
	}
	return errors.Join(f.Sync(), f.Close())
}

func dailyAutomaticBackupName(day time.Time) string {
	return dailyAutomaticBackupPrefix + day.Format("20060102") + ".db"
}

func parseDailyAutomaticBackupName(name string) (time.Time, bool) {
	if !strings.HasPrefix(name, dailyAutomaticBackupPrefix) || !strings.HasSuffix(name, ".db") {
		return time.Time{}, false
	}
	dateText := strings.TrimSuffix(strings.TrimPrefix(name, dailyAutomaticBackupPrefix), ".db")
	if len(dateText) != 8 {
		return time.Time{}, false
	}
	day, err := time.Parse("20060102", dateText)
	if err != nil || dailyAutomaticBackupName(day) != name {
		return time.Time{}, false
	}
	return day, true
}

func parseLegacyAutomaticBackupName(name string) (time.Time, bool) {
	if !strings.HasPrefix(name, "contabase-") || strings.HasPrefix(name, dailyAutomaticBackupPrefix) || !strings.HasSuffix(name, ".db") {
		return time.Time{}, false
	}
	stem := strings.TrimSuffix(strings.TrimPrefix(name, "contabase-"), ".db")
	if len(stem) < len("20060102-150405") {
		return time.Time{}, false
	}
	base := stem[:len("20060102-150405")]
	when, err := time.Parse("20060102-150405", base)
	if err != nil {
		return time.Time{}, false
	}
	rest := stem[len(base):]
	if rest != "" {
		if len(rest) < 2 || rest[0] != '-' {
			return time.Time{}, false
		}
		for _, char := range rest[1:] {
			if char < '0' || char > '9' {
				return time.Time{}, false
			}
		}
	}
	return when, true
}

func civilDay(value time.Time) time.Time {
	return time.Date(value.Year(), value.Month(), value.Day(), 0, 0, 0, 0, value.Location())
}

func civilDayInLocation(value time.Time, loc *time.Location) time.Time {
	return time.Date(value.Year(), value.Month(), value.Day(), 0, 0, 0, 0, loc)
}
