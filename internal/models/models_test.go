package models

import "testing"

func TestHasPermissionUsesFixedRoleMatrix(t *testing.T) {
	cases := []struct {
		name       string
		role       string
		permission string
		want       bool
	}{
		{"admin has global backup permission by role", RoleAdmin, PermissionBackupExport, true},
		{"manager can read reports by role", RoleManager, PermissionReportsView, true},
		{"manager can delete contacts by role", RoleManager, PermissionContactsDelete, true},
		{"manager cannot export backup by role", RoleManager, PermissionBackupExport, false},
		{"user can read transactions by role", RoleUser, "transactions:read", true},
		{"user cannot read reports by role", RoleUser, PermissionReportsView, false},
		{"user cannot delete contacts by role", RoleUser, PermissionContactsDelete, false},
		{"user cannot write config by role", RoleUser, "config:write", false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			member := &WorkspaceMember{Role: tc.role}
			if got := HasPermission(member, tc.permission); got != tc.want {
				t.Fatalf("HasPermission(%s, %s) = %v, want %v", tc.role, tc.permission, got, tc.want)
			}
		})
	}
}

func TestCustomPermissionsAreIgnoredForMVP(t *testing.T) {
	member := &WorkspaceMember{
		Role: RoleUser,
		CustomPermissions: []string{
			PermissionBackupExport,
			PermissionContactsDelete,
			PermissionAdminAuditRead,
			PermissionWorkspaceAdmin,
			PermissionWorkspaceEdit,
			PermissionReportsView,
			"members:write",
			"config:write",
		},
	}

	for _, permission := range member.CustomPermissions {
		if HasPermission(member, permission) {
			t.Fatalf("custom permission %q unexpectedly granted access", permission)
		}
		if IsAllowedCustomPermission(permission) {
			t.Fatalf("custom permission %q unexpectedly accepted for submission", permission)
		}
	}
}
