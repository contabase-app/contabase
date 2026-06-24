package auth_test

import (
	"testing"
	"time"
)

func TestRevokeAllUserSessions(t *testing.T) {
	db, svc, userID, workspaceID := setupTestDB(t)
	defer db.Close()

	token1, _, err := svc.CreateSession(userID, workspaceID, 24*time.Hour, false)
	if err != nil {
		t.Fatal(err)
	}
	token2, _, err := svc.CreateSession(userID, workspaceID, 24*time.Hour, false)
	if err != nil {
		t.Fatal(err)
	}

	if err := svc.RevokeAllUserSessions(userID); err != nil {
		t.Fatalf("RevokeAllUserSessions: %v", err)
	}

	_, _, _, _, err = svc.ResolveAndRefreshSession(token1)
	if err == nil {
		t.Fatal("expected token1 to be revoked")
	}
	_, _, _, _, err = svc.ResolveAndRefreshSession(token2)
	if err == nil {
		t.Fatal("expected token2 to be revoked")
	}
}

func TestRevokeUserSessionsExcept(t *testing.T) {
	db, svc, userID, workspaceID := setupTestDB(t)
	defer db.Close()

	token1, _, err := svc.CreateSession(userID, workspaceID, 24*time.Hour, false)
	if err != nil {
		t.Fatal(err)
	}
	token2, _, err := svc.CreateSession(userID, workspaceID, 24*time.Hour, false)
	if err != nil {
		t.Fatal(err)
	}
	token3, _, err := svc.CreateSession(userID, workspaceID, 24*time.Hour, false)
	if err != nil {
		t.Fatal(err)
	}

	if err := svc.RevokeUserSessionsExcept(userID, token2); err != nil {
		t.Fatalf("RevokeUserSessionsExcept: %v", err)
	}

	_, _, _, _, err = svc.ResolveAndRefreshSession(token1)
	if err == nil {
		t.Fatal("expected token1 to be revoked")
	}

	_, _, _, _, err = svc.ResolveAndRefreshSession(token3)
	if err == nil {
		t.Fatal("expected token3 to be revoked")
	}

	_, _, _, _, err = svc.ResolveAndRefreshSession(token2)
	if err != nil {
		t.Fatalf("expected token2 to be valid, got err: %v", err)
	}
}
