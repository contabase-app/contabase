package handlers

import (
	"database/sql"
	"strings"

	"github.com/contabase-app/contabase/internal/features"
	"github.com/contabase-app/contabase/internal/models"
)

// AppShellAccess contains only the navigation visibility granted to the
// authenticated member in the active workspace. It does not authorize routes
// or mutations; those checks remain with their respective handlers.
type AppShellAccess struct {
	Features features.Flags
	// Section is empty outside settings. ConfiguracoesData owns it when a
	// settings page is active; keeping it on the shared shell contract lets the
	// global navigation render safely on every V2 page.
	Section                   string
	CanManageOps              bool
	CanManageGlobal           bool
	CanManageWorkspaceProfile bool
}

// QueryAppShellAccess keeps the global V2 navigation aligned with the same
// workspace roles used by the settings area. A lookup failure is fail-closed:
// the member still sees their profile, but no privileged navigation links.
func QueryAppShellAccess(db *sql.DB, workspaceID, userID string, isBusiness bool) AppShellAccess {
	var role string
	if err := db.QueryRow(`SELECT role FROM workspace_members WHERE workspace_id = ? AND user_id = ?`, workspaceID, userID).Scan(&role); err != nil {
		return AppShellAccess{}
	}

	role = strings.ToUpper(strings.TrimSpace(role))
	isAdmin := role == models.RoleAdmin || role == "ADMINISTRATOR"
	isManager := role == models.RoleManager || role == "GESTOR"
	canManageOps := isManager || isAdmin
	return AppShellAccess{
		Features:                  features.FromEnv(),
		CanManageOps:              canManageOps,
		CanManageGlobal:           isAdmin,
		CanManageWorkspaceProfile: isBusiness && canManageOps,
	}
}
