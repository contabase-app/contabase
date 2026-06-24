package paths

import (
	"os"
	"path/filepath"
	"strings"
)

const (
	defaultDataDir = "data"
)

func DataDir() string {
	if dir := cleanEnvPath("DATA_DIR"); dir != "" {
		return dir
	}
	return defaultDataDir
}

func DatabasePath() string {
	return filepath.Join(DataDir(), "contabase.db")
}

func DefaultDatabaseURL() string {
	return "file:" + filepath.ToSlash(DatabasePath())
}

func UploadsDir() string {
	if dir := cleanEnvPath("UPLOADS_DIR"); dir != "" {
		return dir
	}
	return filepath.Join(DataDir(), "uploads")
}

func ProfileUploadsDir() string {
	return filepath.Join(UploadsDir(), "profile")
}

func WorkspaceUploadsDir(workspaceID string) string {
	return filepath.Join(UploadsDir(), "workspaces", workspaceID)
}

func TransactionUploadsDir(workspaceID string) string {
	return filepath.Join(UploadsDir(), workspaceID)
}

func BackupsDir() string {
	return filepath.Join(DataDir(), "backups")
}

func CSRFSecretPath() string {
	return filepath.Join(DataDir(), ".csrf-secret")
}

func cleanEnvPath(key string) string {
	value := strings.TrimSpace(os.Getenv(key))
	if value == "" {
		return ""
	}
	return filepath.Clean(value)
}
