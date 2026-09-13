package auth_test

import (
	"errors"
	"testing"
	"time"

	"github.com/contabase-app/contabase/internal/auth"
	"golang.org/x/crypto/bcrypt"
)

func TestInactiveUserCannotResolveOrCreateAuthenticationSessions(t *testing.T) {
	db, service, userID, workspaceID := setupTestDB(t)
	defer db.Close()

	sessionToken, _, err := service.CreateSession(userID, workspaceID, time.Hour, false)
	if err != nil {
		t.Fatalf("create session: %v", err)
	}
	preAuthToken, _, err := service.CreatePreAuthSession(userID, "TOTP", time.Minute, false)
	if err != nil {
		t.Fatalf("create pre-auth session: %v", err)
	}
	if _, err := db.Exec(`UPDATE users SET status = 'pending' WHERE id = ?`, userID); err != nil {
		t.Fatalf("mark user pending: %v", err)
	}

	if _, _, _, _, err := service.ResolveAndRefreshSession(sessionToken); err == nil {
		t.Fatal("pending user session remained valid")
	}
	if _, _, _, err := service.ResolvePreAuthSession(preAuthToken); err == nil {
		t.Fatal("pending user pre-auth session remained valid")
	}
	if _, _, err := service.CreateSession(userID, workspaceID, time.Hour, false); !errors.Is(err, auth.ErrInactiveAccount) {
		t.Fatalf("create session error = %v, want inactive account", err)
	}
	if _, _, err := service.CreatePreAuthSession(userID, "TOTP", time.Minute, false); !errors.Is(err, auth.ErrInactiveAccount) {
		t.Fatalf("create pre-auth error = %v, want inactive account", err)
	}
}

func TestPasswordChangeAtomicallyRevokesPreAuthAndOtherSessions(t *testing.T) {
	db, service, userID, workspaceID := setupTestDB(t)
	defer db.Close()

	currentToken, _, err := service.CreateSession(userID, workspaceID, time.Hour, false)
	if err != nil {
		t.Fatalf("create current session: %v", err)
	}
	otherToken, _, err := service.CreateSession(userID, workspaceID, time.Hour, false)
	if err != nil {
		t.Fatalf("create other session: %v", err)
	}
	preAuthToken, _, err := service.CreatePreAuthSession(userID, "TOTP", time.Minute, false)
	if err != nil {
		t.Fatalf("create pre-auth session: %v", err)
	}
	newHash, err := bcrypt.GenerateFromPassword([]byte("new-password-123"), bcrypt.DefaultCost)
	if err != nil {
		t.Fatalf("hash password: %v", err)
	}
	if err := service.ChangePasswordAndRevokeAuthArtifacts(userID, string(newHash), currentToken); err != nil {
		t.Fatalf("change password: %v", err)
	}
	if _, _, _, _, err := service.ResolveAndRefreshSession(currentToken); err != nil {
		t.Fatalf("current session was revoked: %v", err)
	}
	if _, _, _, _, err := service.ResolveAndRefreshSession(otherToken); err == nil {
		t.Fatal("other session remained valid")
	}
	if _, _, _, err := service.ResolvePreAuthSession(preAuthToken); err == nil {
		t.Fatal("pre-auth session survived password change")
	}
	if _, err := service.Authenticate("u@u", "new-password-123"); err != nil {
		t.Fatalf("new password did not authenticate: %v", err)
	}
}

func TestTOTPTimeStepCanOnlyCompleteOnePreAuthSession(t *testing.T) {
	db, service, userID, _ := setupTestDB(t)
	defer db.Close()
	if err := service.UpdateTOTPSetup(userID, "enc", "[]", true); err != nil {
		t.Fatalf("enable totp: %v", err)
	}
	firstToken, _, err := service.CreatePreAuthSession(userID, "TOTP", time.Minute, false)
	if err != nil {
		t.Fatalf("create first pre-auth: %v", err)
	}
	secondToken, _, err := service.CreatePreAuthSession(userID, "TOTP", time.Minute, false)
	if err != nil {
		t.Fatalf("create second pre-auth: %v", err)
	}
	step := time.Now().Unix() / 30
	if err := service.ConsumePreAuthSessionWithTOTP(firstToken, userID, step); err != nil {
		t.Fatalf("consume first pre-auth: %v", err)
	}
	if err := service.ConsumePreAuthSessionWithTOTP(secondToken, userID, step); !errors.Is(err, auth.ErrTOTPReplay) {
		t.Fatalf("second consume error = %v, want TOTP replay", err)
	}
}

func TestMFASetupChallengeIsSingleUseAndRevokesOtherArtifacts(t *testing.T) {
	db, service, userID, workspaceID := setupTestDB(t)
	defer db.Close()
	currentToken, _, err := service.CreateSession(userID, workspaceID, time.Hour, false)
	if err != nil {
		t.Fatalf("create current session: %v", err)
	}
	otherToken, _, err := service.CreateSession(userID, workspaceID, time.Hour, false)
	if err != nil {
		t.Fatalf("create other session: %v", err)
	}
	preAuthToken, _, err := service.CreatePreAuthSession(userID, "TOTP", time.Minute, false)
	if err != nil {
		t.Fatalf("create pre-auth: %v", err)
	}
	challengeToken, _, err := service.CreateMFASetupChallenge(userID, "encrypted-secret", time.Minute)
	if err != nil {
		t.Fatalf("create setup challenge: %v", err)
	}
	if got, err := service.ResolveMFASetupChallenge(challengeToken, userID); err != nil || got != "encrypted-secret" {
		t.Fatalf("resolve setup challenge = %q, %v", got, err)
	}
	if err := service.CompleteMFASetupChallenge(challengeToken, userID, "encrypted-secret", "[]", currentToken); err != nil {
		t.Fatalf("complete setup challenge: %v", err)
	}
	if _, err := service.ResolveMFASetupChallenge(challengeToken, userID); !errors.Is(err, auth.ErrPreAuthNotFound) {
		t.Fatalf("challenge reuse error = %v, want not found", err)
	}
	if _, _, _, _, err := service.ResolveAndRefreshSession(otherToken); err == nil {
		t.Fatal("other session remained valid after MFA change")
	}
	if _, _, _, err := service.ResolvePreAuthSession(preAuthToken); err == nil {
		t.Fatal("pre-auth session survived MFA change")
	}
	enabled, err := service.IsTOTPEnabled(userID)
	if err != nil || !enabled {
		t.Fatalf("totp enabled = %v, %v", enabled, err)
	}
}

func TestNewMFASetupChallengeInvalidatesThePreviousChallenge(t *testing.T) {
	db, service, userID, _ := setupTestDB(t)
	defer db.Close()
	firstToken, _, err := service.CreateMFASetupChallenge(userID, "first", time.Minute)
	if err != nil {
		t.Fatalf("create first challenge: %v", err)
	}
	secondToken, _, err := service.CreateMFASetupChallenge(userID, "second", time.Minute)
	if err != nil {
		t.Fatalf("create second challenge: %v", err)
	}
	if _, err := service.ResolveMFASetupChallenge(firstToken, userID); !errors.Is(err, auth.ErrPreAuthNotFound) {
		t.Fatalf("first challenge error = %v, want not found", err)
	}
	if got, err := service.ResolveMFASetupChallenge(secondToken, userID); err != nil || got != "second" {
		t.Fatalf("second challenge = %q, %v", got, err)
	}
}
