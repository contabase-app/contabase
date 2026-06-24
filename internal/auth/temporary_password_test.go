package auth_test

import (
	"testing"
	"time"
)

func TestTemporaryPasswordStateAndClear(t *testing.T) {
	db, svc, userID, _ := setupTestDB(t)
	defer db.Close()

	state, err := svc.TemporaryPasswordState(userID, time.Now())
	if err != nil {
		t.Fatalf("temporary password state: %v", err)
	}
	if state.Required || state.Expired || state.ExpiresAt != 0 {
		t.Fatalf("unexpected initial state: %+v", state)
	}

	expiresAt := time.Now().Add(time.Hour).Unix()
	if _, err := db.Exec(`UPDATE users SET must_change_password = 1, temporary_password_expires_at = ? WHERE id = ?`, expiresAt, userID); err != nil {
		t.Fatalf("set temporary password flags: %v", err)
	}
	state, err = svc.TemporaryPasswordState(userID, time.Now())
	if err != nil {
		t.Fatalf("temporary password state: %v", err)
	}
	if !state.Required || state.Expired || state.ExpiresAt != expiresAt {
		t.Fatalf("unexpected active state: %+v", state)
	}

	state, err = svc.TemporaryPasswordState(userID, time.Unix(expiresAt+1, 0))
	if err != nil {
		t.Fatalf("temporary password state: %v", err)
	}
	if !state.Required || !state.Expired {
		t.Fatalf("expected expired state: %+v", state)
	}

	if err := svc.ClearTemporaryPasswordRequirement(userID); err != nil {
		t.Fatalf("clear temporary requirement: %v", err)
	}
	state, err = svc.TemporaryPasswordState(userID, time.Now())
	if err != nil {
		t.Fatalf("temporary password state after clear: %v", err)
	}
	if state.Required || state.Expired || state.ExpiresAt != 0 {
		t.Fatalf("unexpected cleared state: %+v", state)
	}
}

func TestTemporaryPasswordMissingExpiryIsExpired(t *testing.T) {
	db, svc, userID, _ := setupTestDB(t)
	defer db.Close()

	if _, err := db.Exec(`UPDATE users SET must_change_password = 1, temporary_password_expires_at = NULL WHERE id = ?`, userID); err != nil {
		t.Fatalf("set temporary password without expiry: %v", err)
	}
	state, err := svc.TemporaryPasswordState(userID, time.Now())
	if err != nil {
		t.Fatalf("temporary password state: %v", err)
	}
	if !state.Required || !state.Expired {
		t.Fatalf("expected missing expiry to be expired: %+v", state)
	}
}
