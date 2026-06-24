package paths

import (
	"path/filepath"
	"testing"
)

func TestDefaultPersistentPaths(t *testing.T) {
	t.Setenv("DATA_DIR", "")
	t.Setenv("UPLOADS_DIR", "")

	if got := DataDir(); got != "data" {
		t.Fatalf("DataDir() = %q, want data", got)
	}
	if got := DatabasePath(); got != filepath.Join("data", "contabase.db") {
		t.Fatalf("DatabasePath() = %q", got)
	}
	if got := UploadsDir(); got != filepath.Join("data", "uploads") {
		t.Fatalf("UploadsDir() = %q", got)
	}
	if got := BackupsDir(); got != filepath.Join("data", "backups") {
		t.Fatalf("BackupsDir() = %q", got)
	}
	if got := CSRFSecretPath(); got != filepath.Join("data", ".csrf-secret") {
		t.Fatalf("CSRFSecretPath() = %q", got)
	}
}

func TestDataDirCustomizesDatabaseAndDefaultUploads(t *testing.T) {
	root := t.TempDir()
	t.Setenv("DATA_DIR", filepath.Join(root, "state"))
	t.Setenv("UPLOADS_DIR", "")

	if got := DatabasePath(); got != filepath.Join(root, "state", "contabase.db") {
		t.Fatalf("DatabasePath() = %q", got)
	}
	if got := DefaultDatabaseURL(); got != "file:"+filepath.ToSlash(filepath.Join(root, "state", "contabase.db")) {
		t.Fatalf("DefaultDatabaseURL() = %q", got)
	}
	if got := UploadsDir(); got != filepath.Join(root, "state", "uploads") {
		t.Fatalf("UploadsDir() = %q", got)
	}
}

func TestUploadsDirCanBeCustomizedSeparately(t *testing.T) {
	root := t.TempDir()
	t.Setenv("DATA_DIR", filepath.Join(root, "state"))
	t.Setenv("UPLOADS_DIR", filepath.Join(root, "custom-uploads"))

	if got := ProfileUploadsDir(); got != filepath.Join(root, "custom-uploads", "profile") {
		t.Fatalf("ProfileUploadsDir() = %q", got)
	}
	if got := WorkspaceUploadsDir("workspace-1"); got != filepath.Join(root, "custom-uploads", "workspaces", "workspace-1") {
		t.Fatalf("WorkspaceUploadsDir() = %q", got)
	}
	if got := TransactionUploadsDir("workspace-1"); got != filepath.Join(root, "custom-uploads", "workspace-1") {
		t.Fatalf("TransactionUploadsDir() = %q", got)
	}
	if got := BackupsDir(); got != filepath.Join(root, "state", "backups") {
		t.Fatalf("BackupsDir() = %q", got)
	}
}
