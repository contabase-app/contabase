package database

import (
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
	if err := migrateLegacyAutomaticBackups(backupDir, today, retentionDays); err != nil {
		return err
	}

	destPath := filepath.Join(backupDir, dailyAutomaticBackupName(today))
	exists, err := ensureReadOnlyRegularBackup(destPath)
	if err != nil {
		return err
	}
	if !exists {
		tmp, err := os.CreateTemp(backupDir, ".contabase-daily-*.tmp")
		if err != nil {
			return fmt.Errorf("create daily backup staging file: %w", err)
		}
		tmpPath := tmp.Name()
		if err := tmp.Close(); err != nil {
			_ = os.Remove(tmpPath)
			return fmt.Errorf("close daily backup staging file: %w", err)
		}
		defer func() { _ = os.Remove(tmpPath) }()

		if err := CreateSQLiteBackupFile(db, dbPath, tmpPath); err != nil {
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
		published, err := publishFileNoReplace(tmpPath, destPath)
		if err != nil {
			return fmt.Errorf("publish daily automatic backup: %w", err)
		}
		if !published {
			if _, err := ensureReadOnlyRegularBackup(destPath); err != nil {
				return err
			}
		}
	}

	if err := pruneAutomaticBackups(backupDir, today, retentionDays); err != nil {
		return err
	}
	return nil
}

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

func migrateLegacyAutomaticBackups(dir string, today time.Time, retentionDays int) error {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return fmt.Errorf("read automatic backup directory for migration: %w", err)
	}
	cutoff := today.AddDate(0, 0, -(retentionDays - 1))
	type legacyCandidate struct {
		name       string
		capturedAt time.Time
	}
	byDay := make(map[string][]legacyCandidate)
	for _, entry := range entries {
		capturedAt, ok := parseLegacyAutomaticBackupName(entry.Name())
		if !ok || entry.IsDir() || entry.Type()&os.ModeSymlink != 0 {
			continue
		}
		info, err := entry.Info()
		if err != nil || !info.Mode().IsRegular() || info.Size() == 0 {
			continue
		}
		day := civilDayInLocation(capturedAt, today.Location())
		if day.Before(cutoff) || day.After(today) {
			continue
		}
		key := day.Format("20060102")
		byDay[key] = append(byDay[key], legacyCandidate{name: entry.Name(), capturedAt: capturedAt})
	}

	keys := make([]string, 0, len(byDay))
	for key := range byDay {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		day, err := time.ParseInLocation("20060102", key, today.Location())
		if err != nil {
			return fmt.Errorf("parse legacy automatic backup day: %w", err)
		}
		destPath := filepath.Join(dir, dailyAutomaticBackupName(day))
		exists, err := ensureReadOnlyRegularBackup(destPath)
		if err != nil {
			return err
		}
		if exists {
			continue
		}
		candidates := byDay[key]
		sort.Slice(candidates, func(i, j int) bool {
			if candidates[i].capturedAt.Equal(candidates[j].capturedAt) {
				return candidates[i].name < candidates[j].name
			}
			return candidates[i].capturedAt.Before(candidates[j].capturedAt)
		})
		sourcePath := filepath.Join(dir, candidates[0].name)
		if err := os.Chmod(sourcePath, 0o444); err != nil {
			return fmt.Errorf("protect legacy automatic backup %q: %w", candidates[0].name, err)
		}
		published, err := publishFileNoReplace(sourcePath, destPath)
		if err != nil {
			return fmt.Errorf("adopt legacy automatic backup %q: %w", candidates[0].name, err)
		}
		if !published {
			if _, err := ensureReadOnlyRegularBackup(destPath); err != nil {
				return err
			}
		}
	}
	return nil
}

func pruneAutomaticBackups(dir string, today time.Time, retentionDays int) error {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return fmt.Errorf("read automatic backup directory for retention: %w", err)
	}
	cutoff := today.AddDate(0, 0, -(retentionDays - 1))
	changed := false
	for _, entry := range entries {
		if entry.IsDir() || entry.Type()&os.ModeSymlink != 0 {
			continue
		}
		name := entry.Name()
		if day, ok := parseDailyAutomaticBackupName(name); ok {
			day = civilDayInLocation(day, today.Location())
			if day.Before(cutoff) && !day.After(today) {
				if err := os.Remove(filepath.Join(dir, name)); err != nil {
					return fmt.Errorf("remove expired daily automatic backup %q: %w", name, err)
				}
				changed = true
			}
			continue
		}

		capturedAt, ok := parseLegacyAutomaticBackupName(name)
		if !ok {
			continue
		}
		day := civilDayInLocation(capturedAt, today.Location())
		if day.After(today) {
			continue
		}
		dailyPath := filepath.Join(dir, dailyAutomaticBackupName(day))
		if day.Before(cutoff) {
			if err := os.Remove(filepath.Join(dir, name)); err != nil {
				return fmt.Errorf("remove expired legacy automatic backup %q: %w", name, err)
			}
			changed = true
			continue
		}
		if exists, err := ensureReadOnlyRegularBackup(dailyPath); err != nil {
			return err
		} else if exists {
			if err := os.Remove(filepath.Join(dir, name)); err != nil {
				return fmt.Errorf("remove duplicate legacy automatic backup %q: %w", name, err)
			}
			changed = true
		}
	}
	if changed {
		if err := syncDirectory(dir); err != nil {
			return fmt.Errorf("sync automatic backup retention: %w", err)
		}
	}
	return nil
}

func publishFileNoReplace(sourcePath, destPath string) (bool, error) {
	if err := os.Link(sourcePath, destPath); err != nil {
		if os.IsExist(err) {
			return false, nil
		}
		return false, err
	}
	if err := syncDirectory(filepath.Dir(destPath)); err != nil {
		return true, err
	}
	return true, nil
}

func ensureReadOnlyRegularBackup(path string) (bool, error) {
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("inspect daily automatic backup: %w", err)
	}
	if !info.Mode().IsRegular() || info.Size() == 0 {
		return false, fmt.Errorf("daily automatic backup path is not a non-empty regular file")
	}
	if err := os.Chmod(path, 0o444); err != nil {
		return false, fmt.Errorf("protect daily automatic backup: %w", err)
	}
	return true, nil
}

func syncDirectory(dir string) error {
	f, err := os.Open(dir)
	if err != nil {
		return err
	}
	defer f.Close()
	return f.Sync()
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
