package database

import (
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"
)

const (
	preMigrationBackupManifestVersion = 1
	preMigrationBackupManifestSuffix  = ".manifest.json"
)

type preMigrationBackupManifest struct {
	Version       int    `json:"version"`
	File          string `json:"file"`
	Size          int64  `json:"size"`
	SHA256        string `json:"sha256"`
	SchemaVersion int    `json:"schema_version"`
	CreatedAt     int64  `json:"created_at"`
}

func preMigrationBackupManifestPath(path string) string {
	return path + preMigrationBackupManifestSuffix
}

func publishPreMigrationBackupManifest(path string) error {
	hash, size, err := preMigrationBackupDigest(path)
	if err != nil {
		return err
	}
	schemaVersion, err := preMigrationBackupSchemaVersion(path)
	if err != nil {
		return err
	}
	manifest := preMigrationBackupManifest{
		Version:       preMigrationBackupManifestVersion,
		File:          filepath.Base(path),
		Size:          size,
		SHA256:        hash,
		SchemaVersion: schemaVersion,
		CreatedAt:     time.Now().Unix(),
	}
	data, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	manifestPath := preMigrationBackupManifestPath(path)
	if _, err := os.Lstat(manifestPath); !errors.Is(err, os.ErrNotExist) {
		if err != nil {
			return err
		}
		return fmt.Errorf("pre-migration backup manifest already exists: %w", os.ErrExist)
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".pre-migration-manifest-*")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath)
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmpPath, manifestPath); err != nil {
		return err
	}
	return nil
}

// ValidatePreMigrationBackup verifies the SHA-256/size manifest and SQLite
// integrity of a pre-migration backup. It is read-only and never opens the
// source database.
func ValidatePreMigrationBackup(path string) error {
	manifestPath := preMigrationBackupManifestPath(path)
	info, err := os.Stat(manifestPath)
	if err != nil {
		return fmt.Errorf("read pre-migration backup manifest: %w", err)
	}
	if info.Size() <= 0 || info.Size() > 64*1024 {
		return errors.New("invalid pre-migration backup manifest size")
	}
	manifestData, err := os.ReadFile(manifestPath)
	if err != nil {
		return fmt.Errorf("read pre-migration backup manifest: %w", err)
	}
	var manifest preMigrationBackupManifest
	if err := json.Unmarshal(manifestData, &manifest); err != nil {
		return fmt.Errorf("parse pre-migration backup manifest: %w", err)
	}
	if manifest.Version != preMigrationBackupManifestVersion {
		return fmt.Errorf("unsupported pre-migration backup manifest version %d", manifest.Version)
	}
	if manifest.File != filepath.Base(path) {
		return errors.New("pre-migration backup manifest file identity mismatch")
	}
	hash, size, err := preMigrationBackupDigest(path)
	if err != nil {
		return err
	}
	if size != manifest.Size || hash != manifest.SHA256 {
		return errors.New("pre-migration backup checksum or size mismatch")
	}
	db, err := sql.Open("sqlite", sqliteBackupURI(path, "mode=ro&immutable=1"))
	if err != nil {
		return err
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	if err := sqlite79Integrity(db); err != nil {
		return fmt.Errorf("pre-migration backup integrity: %w", err)
	}
	version, err := preMigrationBackupSchemaVersion(path)
	if err != nil {
		return err
	}
	if version != manifest.SchemaVersion {
		return fmt.Errorf("pre-migration backup schema version %d != manifest %d", version, manifest.SchemaVersion)
	}
	return nil
}

func preMigrationBackupDigest(path string) (string, int64, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", 0, err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return "", 0, err
	}
	if info.Size() <= 0 {
		return "", 0, errors.New("pre-migration backup is empty")
	}
	hash := sha256.New()
	if _, err := io.Copy(hash, file); err != nil {
		return "", 0, err
	}
	return hex.EncodeToString(hash.Sum(nil)), info.Size(), nil
}

func preMigrationBackupSchemaVersion(path string) (int, error) {
	db, err := sql.Open("sqlite", sqliteBackupURI(path, "mode=ro&immutable=1"))
	if err != nil {
		return 0, err
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	var exists int
	if err := db.QueryRow(`SELECT COUNT(1) FROM sqlite_schema WHERE type='table' AND name='schema_migrations'`).Scan(&exists); err != nil {
		return 0, err
	}
	if exists == 0 {
		return 0, nil
	}
	var version int
	if err := db.QueryRow(`SELECT COALESCE(MAX(version),0) FROM schema_migrations`).Scan(&version); err != nil {
		return 0, err
	}
	return version, nil
}
