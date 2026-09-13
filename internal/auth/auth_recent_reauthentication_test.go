package auth_test

import (
	"database/sql"
	"errors"
	"testing"
	"time"
)

func TestSessionRecentReauthenticationWindow(t *testing.T) {
	db, svc, userID, workspaceID := setupTestDB(t)
	defer db.Close()

	token, _, err := svc.CreateSession(userID, workspaceID, time.Hour, false)
	if err != nil {
		t.Fatalf("create session: %v", err)
	}
	now := time.Now()

	recent, err := svc.SessionRecentlyAuthenticated(token, now.Add(9*time.Minute), 10*time.Minute)
	if err != nil || !recent {
		t.Fatalf("fresh session recent = %v, err = %v; want true", recent, err)
	}

	if _, err := db.Exec(`UPDATE sessions SET reauthenticated_at = ?`, now.Add(-11*time.Minute).Unix()); err != nil {
		t.Fatalf("age reauthentication: %v", err)
	}
	recent, err = svc.SessionRecentlyAuthenticated(token, now, 10*time.Minute)
	if err != nil {
		t.Fatalf("check stale reauthentication: %v", err)
	}
	if recent {
		t.Fatal("reauthentication older than the window was accepted")
	}

	if err := svc.MarkSessionReauthenticated(token, userID, now); err != nil {
		t.Fatalf("mark session reauthenticated: %v", err)
	}
	recent, err = svc.SessionRecentlyAuthenticated(token, now, 10*time.Minute)
	if err != nil || !recent {
		t.Fatalf("renewed reauthentication recent = %v, err = %v; want true", recent, err)
	}
}

func TestSessionRecentReauthenticationRejectsInvalidSessionState(t *testing.T) {
	db, svc, userID, workspaceID := setupTestDB(t)
	defer db.Close()
	token, _, err := svc.CreateSession(userID, workspaceID, time.Hour, false)
	if err != nil {
		t.Fatalf("create session: %v", err)
	}
	now := time.Now()

	if _, err := db.Exec(`UPDATE sessions SET reauthenticated_at = ?`, now.Add(time.Minute).Unix()); err != nil {
		t.Fatalf("set future timestamp: %v", err)
	}
	if recent, err := svc.SessionRecentlyAuthenticated(token, now, 10*time.Minute); err != nil || recent {
		t.Fatalf("future timestamp recent = %v, err = %v; want false", recent, err)
	}

	if err := svc.RevokeSession(token); err != nil {
		t.Fatalf("revoke session: %v", err)
	}
	if err := svc.MarkSessionReauthenticated(token, userID, now); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("mark revoked session error = %v, want sql.ErrNoRows", err)
	}
	if recent, err := svc.SessionRecentlyAuthenticated(token, now, 10*time.Minute); err != nil || recent {
		t.Fatalf("revoked session recent = %v, err = %v; want false", recent, err)
	}
}
