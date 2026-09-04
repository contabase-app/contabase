package handlers

import (
	"testing"
	"time"
)

func TestQueryAppShellAccessFollowsWorkspaceRole(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()

	now := time.Now().Unix()
	execTestSQL(t, db, `
		INSERT INTO users (id, name, email, password_hash, created_at, updated_at)
		VALUES ('user-admin', 'Admin', 'admin@example.test', 'hash', ?, ?),
		       ('user-manager', 'Manager', 'manager@example.test', 'hash', ?, ?),
		       ('user-user', 'Member', 'member@example.test', 'hash', ?, ?)
	`, now, now, now, now, now, now)
	execTestSQL(t, db, `
		INSERT INTO workspaces (id, name, description, created_at, updated_at)
		VALUES ('ws-admin', 'Admin', '', ?, ?),
		       ('ws-manager', 'Manager', '', ?, ?),
		       ('ws-user', 'Member', '', ?, ?)
	`, now, now, now, now, now, now)
	execTestSQL(t, db, `
		INSERT INTO workspace_members (workspace_id, user_id, role, joined_at)
		VALUES ('ws-admin', 'user-admin', 'ADMIN', ?),
		       ('ws-manager', 'user-manager', 'MANAGER', ?),
		       ('ws-user', 'user-user', 'USER', ?)
	`, now, now, now)

	admin := QueryAppShellAccess(db, "ws-admin", "user-admin", true)
	if !admin.CanManageOps || !admin.CanManageGlobal || !admin.CanManageWorkspaceProfile {
		t.Fatalf("admin access = %+v, want all navigation groups", admin)
	}

	manager := QueryAppShellAccess(db, "ws-manager", "user-manager", true)
	if !manager.CanManageOps || manager.CanManageGlobal || !manager.CanManageWorkspaceProfile {
		t.Fatalf("manager access = %+v, want operations and business profile only", manager)
	}

	member := QueryAppShellAccess(db, "ws-user", "user-user", true)
	if member.CanManageOps || member.CanManageGlobal || member.CanManageWorkspaceProfile {
		t.Fatalf("member access = %+v, want no privileged navigation", member)
	}

	missing := QueryAppShellAccess(db, "ws-missing", "user-missing", true)
	if missing.CanManageOps || missing.CanManageGlobal || missing.CanManageWorkspaceProfile {
		t.Fatalf("missing membership must fail closed, got %+v", missing)
	}
}
