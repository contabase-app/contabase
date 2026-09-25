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

func TestInvestmentPermissionTokens(t *testing.T) {
	if PermissionInvestmentsRead != "investments:read" || PermissionInvestmentsWrite != "investments:write" {
		t.Fatalf("unexpected investment tokens: %q / %q", PermissionInvestmentsRead, PermissionInvestmentsWrite)
	}
	if PermissionInvestmentsRead == "" || PermissionInvestmentsWrite == "" || PermissionInvestmentsRead == PermissionInvestmentsWrite {
		t.Fatal("investment tokens must be nonempty and distinct")
	}
}

func TestInvestmentPermissionsPreserveRoleMatrix(t *testing.T) {
	for _, role := range []string{RoleAdmin, RoleManager, RoleUser, "UNKNOWN", "OWNER", ""} {
		t.Run(role, func(t *testing.T) {
			member := &WorkspaceMember{Role: role}
			for _, token := range []string{PermissionInvestmentsRead, PermissionInvestmentsWrite, "investments:unknown"} {
				// ADMIN intentionally accepts every nonempty token, including unknown ones.
				if got, want := HasPermission(member, token), role == RoleAdmin; got != want {
					t.Errorf("HasPermission(%q, %q) = %v, want %v", role, token, got, want)
				}
			}
			for _, token := range []string{"", " \t\n"} {
				if HasPermission(member, token) {
					t.Errorf("role %q accepted empty token %q", role, token)
				}
			}
		})
	}
	for _, token := range []string{PermissionInvestmentsRead, PermissionInvestmentsWrite} {
		if HasPermission(nil, token) {
			t.Errorf("nil member accepted %q", token)
		}
	}
}

func TestInvestmentCustomPermissionsRemainInert(t *testing.T) {
	for _, token := range []string{PermissionInvestmentsRead, PermissionInvestmentsWrite, " INVESTMENTS:WRITE "} {
		if IsAllowedCustomPermission(token) {
			t.Errorf("custom permission %q unexpectedly allowed", token)
		}
	}
	for _, role := range []string{RoleManager, RoleUser} {
		for _, raw := range []string{"", "[]", `["investments:read"]`, `["investments:write"]`,
			`["investments:read","investments:write"]`, "investments:read,investments:write"} {
			t.Run(role+"/"+raw, func(t *testing.T) {
				member := &WorkspaceMember{Role: role, CustomPermissionsRaw: raw}
				for _, hydrated := range []bool{false, true} {
					if hydrated {
						member.CustomPermissions = ParsePermissionList(raw)
					}
					for _, token := range []string{PermissionInvestmentsRead, PermissionInvestmentsWrite} {
						if HasPermission(member, token) {
							t.Errorf("custom list granted %q (hydrated=%v)", token, hydrated)
						}
					}
					if !HasPermission(member, "transactions:read") {
						t.Error("legacy read permission lost")
					}
				}
			})
		}
	}
}

func TestInvestmentPermissionsDoNotIntroduceWriteReadInheritance(t *testing.T) {
	// No current role has only investments:write. Use the existing fixed-role
	// counterexample to guard against introducing generic write -> read logic.
	member := &WorkspaceMember{Role: RoleManager}
	if !HasPermission(member, "goals:write") || HasPermission(member, "goals:read") {
		t.Fatal("write must not implicitly grant the corresponding read token")
	}
	member.SetCustomPermissions([]string{PermissionInvestmentsWrite})
	if HasPermission(member, PermissionInvestmentsWrite) || HasPermission(member, PermissionInvestmentsRead) {
		t.Fatal("custom investment write must grant neither write nor read")
	}
}
