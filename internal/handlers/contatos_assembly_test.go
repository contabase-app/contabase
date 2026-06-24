package handlers

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestBuildContatosDataSeparatesPageMetadataFromList(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	seedTenantIsolationScenario(t, db)

	now := time.Now().UTC().Unix()
	uploadRoot := t.TempDir()
	t.Setenv("UPLOADS_DIR", uploadRoot)

	execTestSQL(t, db, `UPDATE users SET profile_photo_path = '/uploads/profile/user-a.png', updated_at = ? WHERE id = 'user-a'`, now)

	profileDir := filepath.Join(uploadRoot, "profile")
	if err := os.MkdirAll(profileDir, 0o755); err != nil {
		t.Fatalf("create profile dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(profileDir, "user-a.png"), []byte("profile"), 0o600); err != nil {
		t.Fatalf("write profile file: %v", err)
	}

	h := ContatosHandler{
		DB:          db,
		WorkspaceID: "ws-a",
		UserID:      "user-a",
	}

	listData, err := h.buildContatosListData("", "client", false)
	if err != nil {
		t.Fatalf("buildContatosListData: %v", err)
	}
	if listData.CustomClientIDPlaceholder != "" {
		t.Fatalf("list CustomClientIDPlaceholder = %q, want empty", listData.CustomClientIDPlaceholder)
	}
	if listData.UserInitials != "" {
		t.Fatalf("list UserInitials = %q, want empty", listData.UserInitials)
	}
	if listData.ProfilePhotoURL != "" {
		t.Fatalf("list ProfilePhotoURL = %q, want empty", listData.ProfilePhotoURL)
	}
	if listData.ActiveWorkspaceName != "" {
		t.Fatalf("list ActiveWorkspaceName = %q, want empty", listData.ActiveWorkspaceName)
	}
	if listData.TipoLabel != "Clientes" {
		t.Fatalf("list TipoLabel = %q, want %q", listData.TipoLabel, "Clientes")
	}
	if listData.FabOOB {
		t.Fatalf("list FabOOB = true, want false")
	}
	if len(listData.Contatos) != 1 || listData.Contatos[0].ID != "a-contact" {
		t.Fatalf("list contacts = %#v, want only a-contact", listData.Contatos)
	}

	pageData, err := h.buildContatosPageData("ana", "vendor", true)
	if err != nil {
		t.Fatalf("buildContatosPageData: %v", err)
	}
	if pageData.CustomClientIDPlaceholder != "CLI-001" {
		t.Fatalf("page CustomClientIDPlaceholder = %q, want %q", pageData.CustomClientIDPlaceholder, "CLI-001")
	}
	if pageData.UserInitials != "UA" {
		t.Fatalf("page UserInitials = %q, want %q", pageData.UserInitials, "UA")
	}
	wantPhoto := fmt.Sprintf("/uploads/profile/user-a.png?v=%d", now)
	if pageData.ProfilePhotoURL != wantPhoto {
		t.Fatalf("page ProfilePhotoURL = %q, want %q", pageData.ProfilePhotoURL, wantPhoto)
	}
	if pageData.ActiveWorkspaceName != "Workspace A" {
		t.Fatalf("page ActiveWorkspaceName = %q, want %q", pageData.ActiveWorkspaceName, "Workspace A")
	}
	if pageData.TipoLabel != "Fornecedores" {
		t.Fatalf("page TipoLabel = %q, want %q", pageData.TipoLabel, "Fornecedores")
	}
	if !pageData.FabOOB {
		t.Fatalf("page FabOOB = false, want true")
	}
}
