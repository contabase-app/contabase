package auth

import (
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/contabase-app/contabase/internal/models"

	"github.com/google/uuid"
	"golang.org/x/crypto/bcrypt"
)

var ErrInvalidCredentials = errors.New("invalid credentials")
var ErrInactiveAccount = errors.New("inactive account")
var ErrPreAuthNotFound = errors.New("pre-auth session not found")
var ErrTOTPReplay = errors.New("totp code was already used")

const (
	standardSessionTTL                = 24 * time.Hour
	rememberedSessionTTL              = 30 * 24 * time.Hour
	standardSessionAbsoluteLifetime   = 7 * 24 * time.Hour
	rememberedSessionAbsoluteLifetime = 90 * 24 * time.Hour
)

type TemporaryPasswordState struct {
	Required  bool
	Expired   bool
	ExpiresAt int64
}

type Service struct {
	DB *sql.DB
}

var dummyPasswordHash []byte

func NewService(db *sql.DB) *Service {
	if len(dummyPasswordHash) == 0 {
		hash, err := bcrypt.GenerateFromPassword([]byte("contabase-dummy-password"), bcrypt.DefaultCost)
		if err == nil {
			dummyPasswordHash = hash
		}
	}
	return &Service{DB: db}
}

func (s *Service) Authenticate(email, password string) (string, error) {
	var userID, passwordHash, status string
	var deletedAt int64
	err := s.DB.QueryRow(`SELECT id, password_hash, COALESCE(status, 'active'), COALESCE(deleted_at, 0) FROM users WHERE lower(email) = lower(?)`, email).Scan(&userID, &passwordHash, &status, &deletedAt)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			if len(dummyPasswordHash) > 0 {
				_ = bcrypt.CompareHashAndPassword(dummyPasswordHash, []byte(password))
			}
			return "", ErrInvalidCredentials
		}
		return "", err
	}
	if bcrypt.CompareHashAndPassword([]byte(passwordHash), []byte(password)) != nil {
		return "", ErrInvalidCredentials
	}
	if strings.ToLower(strings.TrimSpace(status)) != "active" {
		return "", ErrInactiveAccount
	}
	if deletedAt > 0 {
		return "", ErrInvalidCredentials
	}
	return userID, nil
}

func (s *Service) CreateSession(userID, workspaceID string, ttl time.Duration, isRemember bool) (string, time.Time, error) {
	token, err := randomToken()
	if err != nil {
		return "", time.Time{}, err
	}
	_, absoluteLifetime := sessionLifetimes(isRemember)
	if ttl > absoluteLifetime {
		ttl = absoluteLifetime
	}
	now := time.Now()
	expiresAt := now.Add(ttl)
	tokenHash := hashToken(token)
	flag := 0
	if isRemember {
		flag = 1
	}
	res, err := s.DB.Exec(`
		INSERT INTO sessions (id, user_id, workspace_id, token_hash, expires_at, is_remember, created_at, reauthenticated_at)
		SELECT ?, u.id, ?, ?, ?, ?, ?, ?
		FROM users u
		WHERE u.id = ? AND lower(trim(u.status)) = 'active' AND u.deleted_at = 0
	`, uuid.NewString(), workspaceID, tokenHash, expiresAt.Unix(), flag, now.Unix(), now.Unix(), userID)
	if err != nil {
		return "", time.Time{}, err
	}
	affected, err := res.RowsAffected()
	if err != nil {
		return "", time.Time{}, err
	}
	if affected != 1 {
		return "", time.Time{}, ErrInactiveAccount
	}
	return token, expiresAt, nil
}

func (s *Service) ResolveAndRefreshSession(token string) (string, string, int, bool, error) {
	tokenHash := hashToken(token)
	now := time.Now().Unix()
	var userID, workspaceID string
	var expiresAt, createdAt int64
	var isRemember int
	err := s.DB.QueryRow(
		`SELECT s.user_id, s.workspace_id, s.expires_at, s.created_at, s.is_remember FROM sessions s JOIN users u ON u.id = s.user_id WHERE s.token_hash = ? AND s.revoked_at IS NULL AND s.expires_at > ? AND u.deleted_at = 0 AND lower(trim(u.status)) = 'active'`,
		tokenHash, now,
	).Scan(&userID, &workspaceID, &expiresAt, &createdAt, &isRemember)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return "", "", 0, false, sql.ErrNoRows
		}
		return "", "", 0, false, err
	}
	if strings.TrimSpace(workspaceID) == "" {
		return "", "", 0, false, sql.ErrNoRows
	}

	ttl, absoluteLifetime := sessionLifetimes(isRemember == 1)
	ttlSeconds := int64(ttl / time.Second)
	absoluteExpiresAt := createdAt + int64(absoluteLifetime/time.Second)
	if createdAt <= 0 || now >= absoluteExpiresAt {
		return "", "", 0, false, sql.ErrNoRows
	}

	targetExpiresAt := expiresAt
	if targetExpiresAt > absoluteExpiresAt {
		targetExpiresAt = absoluteExpiresAt
	}
	remaining := targetExpiresAt - now
	if remaining < ttlSeconds/2 {
		slidingExpiresAt := now + ttlSeconds
		if slidingExpiresAt > absoluteExpiresAt {
			slidingExpiresAt = absoluteExpiresAt
		}
		if slidingExpiresAt > targetExpiresAt {
			targetExpiresAt = slidingExpiresAt
		}
	}
	if targetExpiresAt != expiresAt {
		result, updateErr := s.DB.Exec(`
			UPDATE sessions
			SET expires_at = ?
			WHERE token_hash = ?
			  AND revoked_at IS NULL
			  AND expires_at > ?
			  AND EXISTS (
				SELECT 1
				FROM users u
				WHERE u.id = sessions.user_id
				  AND u.deleted_at = 0
				  AND lower(trim(u.status)) = 'active'
			  )
		`, targetExpiresAt, tokenHash, now)
		if updateErr != nil {
			return "", "", 0, false, updateErr
		}
		affected, updateErr := result.RowsAffected()
		if updateErr != nil {
			return "", "", 0, false, updateErr
		}
		if affected != 1 {
			return "", "", 0, false, sql.ErrNoRows
		}
		return userID, workspaceID, int(targetExpiresAt - now), true, nil
	}

	return userID, workspaceID, 0, false, nil
}

func sessionLifetimes(isRemember bool) (time.Duration, time.Duration) {
	if isRemember {
		return rememberedSessionTTL, rememberedSessionAbsoluteLifetime
	}
	return standardSessionTTL, standardSessionAbsoluteLifetime
}

func (s *Service) SessionRecentlyAuthenticated(token string, now time.Time, window time.Duration) (bool, error) {
	if strings.TrimSpace(token) == "" || window <= 0 {
		return false, nil
	}
	if now.IsZero() {
		now = time.Now()
	}

	var reauthenticatedAt, createdAt int64
	var isRemember int
	err := s.DB.QueryRow(`
		SELECT s.reauthenticated_at, s.created_at, s.is_remember
		FROM sessions s
		JOIN users u ON u.id = s.user_id
		WHERE s.token_hash = ?
		  AND s.revoked_at IS NULL
		  AND s.expires_at > ?
		  AND u.deleted_at = 0
		  AND lower(trim(u.status)) = 'active'
	`, hashToken(token), now.Unix()).Scan(&reauthenticatedAt, &createdAt, &isRemember)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return false, nil
		}
		return false, err
	}

	_, absoluteLifetime := sessionLifetimes(isRemember == 1)
	nowUnix := now.Unix()
	if createdAt <= 0 || nowUnix >= createdAt+int64(absoluteLifetime/time.Second) {
		return false, nil
	}
	if reauthenticatedAt <= 0 || reauthenticatedAt > nowUnix {
		return false, nil
	}
	return nowUnix-reauthenticatedAt <= int64(window/time.Second), nil
}

func (s *Service) MarkSessionReauthenticated(token, userID string, now time.Time) error {
	if strings.TrimSpace(token) == "" || strings.TrimSpace(userID) == "" {
		return sql.ErrNoRows
	}
	if now.IsZero() {
		now = time.Now()
	}
	nowUnix := now.Unix()
	result, err := s.DB.Exec(`
		UPDATE sessions
		SET reauthenticated_at = ?
		WHERE token_hash = ?
		  AND user_id = ?
		  AND revoked_at IS NULL
		  AND expires_at > ?
		  AND created_at > 0
		  AND (
			(COALESCE(is_remember, 0) = 1 AND created_at + ? > ?)
			OR (COALESCE(is_remember, 0) != 1 AND created_at + ? > ?)
		  )
		  AND EXISTS (
			SELECT 1
			FROM users u
			WHERE u.id = sessions.user_id
			  AND u.deleted_at = 0
			  AND lower(trim(u.status)) = 'active'
		  )
	`,
		nowUnix,
		hashToken(token),
		userID,
		nowUnix,
		int64(rememberedSessionAbsoluteLifetime/time.Second), nowUnix,
		int64(standardSessionAbsoluteLifetime/time.Second), nowUnix,
	)
	if err != nil {
		return err
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if affected != 1 {
		return sql.ErrNoRows
	}
	return nil
}

func (s *Service) RevokeSession(token string) error {
	tokenHash := hashToken(token)
	_, err := s.DB.Exec(`UPDATE sessions SET revoked_at = unixepoch() WHERE token_hash = ? AND revoked_at IS NULL`, tokenHash)
	return err
}

func (s *Service) RevokeAllUserSessions(userID string) error {
	_, err := s.DB.Exec(`UPDATE sessions SET revoked_at = unixepoch() WHERE user_id = ? AND revoked_at IS NULL`, userID)
	return err
}

func (s *Service) RevokeAllUserPreAuthSessions(userID string) error {
	_, err := s.DB.Exec(`UPDATE pre_auth_sessions SET consumed_at = unixepoch() WHERE user_id = ? AND consumed_at IS NULL`, userID)
	return err
}

func (s *Service) RevokeUserSessionsExcept(userID, exceptToken string) error {
	exceptHash := hashToken(exceptToken)
	_, err := s.DB.Exec(`UPDATE sessions SET revoked_at = unixepoch() WHERE user_id = ? AND token_hash != ? AND revoked_at IS NULL`, userID, exceptHash)
	return err
}

func (s *Service) TemporaryPasswordState(userID string, now time.Time) (TemporaryPasswordState, error) {
	var mustChange int
	var expires sql.NullInt64
	err := s.DB.QueryRow(`
		SELECT COALESCE(must_change_password, 0), temporary_password_expires_at
		FROM users
		WHERE id = ?
	`, userID).Scan(&mustChange, &expires)
	if err != nil {
		return TemporaryPasswordState{}, err
	}
	state := TemporaryPasswordState{Required: mustChange == 1}
	if expires.Valid {
		state.ExpiresAt = expires.Int64
	}
	state.Expired = state.Required && (!expires.Valid || expires.Int64 <= 0 || now.Unix() > expires.Int64)
	return state, nil
}

func (s *Service) ClearTemporaryPasswordRequirement(userID string) error {
	_, err := s.DB.Exec(`
		UPDATE users
		SET must_change_password = 0,
		    temporary_password_expires_at = NULL,
		    updated_at = unixepoch()
		WHERE id = ?
	`, userID)
	return err
}

func (s *Service) RevokePreAuthSession(token string) error {
	tokenHash := hashToken(token)
	_, err := s.DB.Exec(`
		UPDATE pre_auth_sessions
		SET consumed_at = unixepoch()
		WHERE token_hash = ? AND consumed_at IS NULL
	`, tokenHash)
	return err
}

func (s *Service) ResolveWorkspaceMembership(userID string) (string, string, error) {
	var defaultWorkspaceID string
	_ = s.DB.QueryRow(`SELECT COALESCE(default_workspace_id, '') FROM users WHERE id = ?`, userID).Scan(&defaultWorkspaceID)
	if strings.TrimSpace(defaultWorkspaceID) != "" {
		var role string
		err := s.DB.QueryRow(`
			SELECT role
			FROM workspace_members
			WHERE user_id = ? AND workspace_id = ?
			LIMIT 1
		`, userID, defaultWorkspaceID).Scan(&role)
		if err == nil {
			return defaultWorkspaceID, role, nil
		}
	}
	var workspaceID, role string
	err := s.DB.QueryRow(`
		SELECT workspace_id, role
		FROM workspace_members
		WHERE user_id = ?
		ORDER BY joined_at ASC
		LIMIT 1
	`, userID).Scan(&workspaceID, &role)
	return workspaceID, role, err
}

func (s *Service) ResolveWorkspaceRole(userID, workspaceID string) (string, error) {
	member, err := s.ResolveWorkspaceMember(userID, workspaceID)
	if err != nil {
		return "", err
	}
	return member.Role, nil
}

func (s *Service) ResolveWorkspaceMember(userID, workspaceID string) (models.WorkspaceMember, error) {
	member := models.WorkspaceMember{
		CustomPermissionsRaw: "[]",
		CustomPermissions:    make([]string, 0),
	}
	err := s.DB.QueryRow(`
		SELECT workspace_id, user_id, role, joined_at, COALESCE(custom_permissions, '[]')
		FROM workspace_members
		WHERE user_id = ? AND workspace_id = ?
		LIMIT 1
	`, userID, workspaceID).Scan(
		&member.WorkspaceID,
		&member.UserID,
		&member.Role,
		&member.JoinedAt,
		&member.CustomPermissionsRaw,
	)
	if err != nil && isMissingCustomPermissionsColumnError(err) {
		err = s.DB.QueryRow(`
			SELECT workspace_id, user_id, role, joined_at
			FROM workspace_members
			WHERE user_id = ? AND workspace_id = ?
			LIMIT 1
		`, userID, workspaceID).Scan(
			&member.WorkspaceID,
			&member.UserID,
			&member.Role,
			&member.JoinedAt,
		)
		member.CustomPermissionsRaw = "[]"
	}
	if err != nil {
		return models.WorkspaceMember{}, err
	}
	member.CustomPermissions = models.ParsePermissionList(member.CustomPermissionsRaw)
	if member.CustomPermissions == nil {
		member.CustomPermissions = make([]string, 0)
	}
	if strings.TrimSpace(member.CustomPermissionsRaw) == "" {
		member.CustomPermissionsRaw = "[]"
	}
	return member, nil
}

func isMissingCustomPermissionsColumnError(err error) bool {
	if err == nil {
		return false
	}
	return strings.Contains(strings.ToLower(err.Error()), "no such column: custom_permissions")
}

func (s *Service) UpdateSessionWorkspace(token, workspaceID string) error {
	res, err := s.DB.Exec(`
		UPDATE sessions SET workspace_id = ?
		WHERE token_hash = ? AND revoked_at IS NULL AND expires_at > unixepoch()
	`, workspaceID, hashToken(token))
	if err != nil {
		return err
	}
	affected, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if affected != 1 {
		return fmt.Errorf("session not found or expired")
	}
	return nil
}

func (s *Service) SetActivationTokenByEmail(email string, tokenTTL time.Duration) (string, error) {
	token, err := randomToken()
	if err != nil {
		return "", err
	}
	tokenHash := hashToken(token)
	expiresAt := time.Now().Add(tokenTTL).Unix()
	now := time.Now().Unix()
	_, err = s.DB.Exec(`
		UPDATE users
		SET activation_token_hash = ?, activation_expires_at = ?, status = 'pending', updated_at = ?
		WHERE lower(email) = lower(?)
	`, tokenHash, expiresAt, now, email)
	if err != nil {
		return "", err
	}
	return token, nil
}

func (s *Service) ActivateAccount(token, password string) error {
	if strings.TrimSpace(token) == "" {
		return ErrInvalidCredentials
	}
	tokenHash := hashToken(token)
	now := time.Now().Unix()
	var userID string
	if err := s.DB.QueryRow(`
		SELECT id
		FROM users
		WHERE activation_token_hash = ?
		  AND activation_expires_at IS NOT NULL
		  AND activation_expires_at > ?
		  AND status = 'pending'
		LIMIT 1
	`, tokenHash, now).Scan(&userID); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return ErrInvalidCredentials
		}
		return err
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		return err
	}
	_, err = s.DB.Exec(`
		UPDATE users
		SET password_hash = ?, status = 'active', activation_token_hash = NULL, activation_expires_at = NULL, updated_at = unixepoch()
		WHERE id = ?
	`, string(hash), userID)
	return err
}

func (s *Service) IsTOTPEnabled(userID string) (bool, error) {
	var enabled int
	if err := s.DB.QueryRow(`SELECT COALESCE(totp_enabled, 0) FROM users WHERE id = ?`, userID).Scan(&enabled); err != nil {
		return false, err
	}
	return enabled == 1, nil
}

func (s *Service) GetEncryptedTOTPSecret(userID string) (string, error) {
	var enc string
	if err := s.DB.QueryRow(`SELECT COALESCE(totp_secret_enc, '') FROM users WHERE id = ?`, userID).Scan(&enc); err != nil {
		return "", err
	}
	return strings.TrimSpace(enc), nil
}

func (s *Service) GetBackupCodeHashes(userID string) (string, error) {
	var payload string
	if err := s.DB.QueryRow(`SELECT COALESCE(totp_backup_codes, '[]') FROM users WHERE id = ?`, userID).Scan(&payload); err != nil {
		return "", err
	}
	return payload, nil
}

func (s *Service) UpdateTOTPSetup(userID, secretEnc, backupCodesJSON string, enabled bool) error {
	flag := 0
	if enabled {
		flag = 1
	}
	_, err := s.DB.Exec(`
		UPDATE users
		SET totp_enabled = ?, totp_secret_enc = ?, totp_backup_codes = ?, totp_enabled_at = unixepoch(), updated_at = unixepoch()
		WHERE id = ?
	`, flag, secretEnc, backupCodesJSON, userID)
	return err
}

func (s *Service) DisableTOTP(userID string) error {
	_, err := s.DB.Exec(`
		UPDATE users
		SET totp_enabled = 0, totp_secret_enc = NULL, totp_backup_codes = '[]', totp_enabled_at = NULL, updated_at = unixepoch()
		WHERE id = ?
	`, userID)
	return err
}

func (s *Service) ReplaceBackupCodeHashes(userID, backupCodesJSON string) error {
	_, err := s.DB.Exec(`UPDATE users SET totp_backup_codes = ?, updated_at = unixepoch() WHERE id = ?`, backupCodesJSON, userID)
	return err
}

func (s *Service) ReplaceBackupCodeHashesIfCurrent(userID, currentJSON, nextJSON string) (bool, error) {
	res, err := s.DB.Exec(`
		UPDATE users
		SET totp_backup_codes = ?, updated_at = unixepoch()
		WHERE id = ? AND totp_backup_codes = ?
	`, nextJSON, userID, currentJSON)
	if err != nil {
		return false, err
	}
	affected, err := res.RowsAffected()
	if err != nil {
		return false, err
	}
	return affected == 1, nil
}

func (s *Service) CreateMFASetupChallenge(userID, secretEnc string, ttl time.Duration) (string, time.Time, error) {
	token, err := randomToken()
	if err != nil {
		return "", time.Time{}, err
	}
	expiresAt := time.Now().Add(ttl)
	tx, err := s.DB.Begin()
	if err != nil {
		return "", time.Time{}, err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`UPDATE mfa_setup_challenges SET consumed_at = unixepoch() WHERE user_id = ? AND consumed_at IS NULL`, userID); err != nil {
		return "", time.Time{}, err
	}
	if _, err := tx.Exec(`
		INSERT INTO mfa_setup_challenges (id, user_id, token_hash, secret_enc, expires_at, created_at)
		VALUES (?, ?, ?, ?, ?, unixepoch())
	`, uuid.NewString(), userID, hashToken(token), secretEnc, expiresAt.Unix()); err != nil {
		return "", time.Time{}, err
	}
	if err := tx.Commit(); err != nil {
		return "", time.Time{}, err
	}
	return token, expiresAt, nil
}

func (s *Service) ResolveMFASetupChallenge(token, userID string) (string, error) {
	var secretEnc string
	err := s.DB.QueryRow(`
		SELECT secret_enc
		FROM mfa_setup_challenges
		WHERE token_hash = ? AND user_id = ? AND consumed_at IS NULL AND expires_at > unixepoch()
	`, hashToken(token), userID).Scan(&secretEnc)
	if errors.Is(err, sql.ErrNoRows) {
		return "", ErrPreAuthNotFound
	}
	return strings.TrimSpace(secretEnc), err
}

func (s *Service) CompleteMFASetupChallenge(token, userID, secretEnc, backupCodesJSON, keepSessionToken string) error {
	tx, err := s.DB.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	res, err := tx.Exec(`
		UPDATE mfa_setup_challenges
		SET consumed_at = unixepoch()
		WHERE token_hash = ? AND user_id = ? AND secret_enc = ? AND consumed_at IS NULL AND expires_at > unixepoch()
	`, hashToken(token), userID, secretEnc)
	if err != nil {
		return err
	}
	affected, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if affected != 1 {
		return ErrPreAuthNotFound
	}
	if _, err := tx.Exec(`
		UPDATE users
		SET totp_enabled = 1, totp_secret_enc = ?, totp_backup_codes = ?, totp_enabled_at = unixepoch(), totp_last_used_step = -1, updated_at = unixepoch()
		WHERE id = ?
	`, secretEnc, backupCodesJSON, userID); err != nil {
		return err
	}
	if err := revokeOtherAuthArtifactsTx(tx, userID, keepSessionToken); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Service) DisableTOTPAndRevokeOtherSessions(userID, keepSessionToken string) error {
	tx, err := s.DB.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`
		UPDATE users
		SET totp_enabled = 0, totp_secret_enc = NULL, totp_backup_codes = '[]', totp_enabled_at = NULL, totp_last_used_step = -1, updated_at = unixepoch()
		WHERE id = ?
	`, userID); err != nil {
		return err
	}
	if err := revokeOtherAuthArtifactsTx(tx, userID, keepSessionToken); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Service) ChangePasswordAndRevokeAuthArtifacts(userID, passwordHash, keepSessionToken string) error {
	tx, err := s.DB.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`
		UPDATE users
		SET password_hash = ?, must_change_password = 0, temporary_password_expires_at = NULL, updated_at = unixepoch()
		WHERE id = ?
	`, passwordHash, userID); err != nil {
		return err
	}
	if err := revokeOtherAuthArtifactsTx(tx, userID, keepSessionToken); err != nil {
		return err
	}
	return tx.Commit()
}

func revokeOtherAuthArtifactsTx(tx *sql.Tx, userID, keepSessionToken string) error {
	if strings.TrimSpace(keepSessionToken) == "" {
		if _, err := tx.Exec(`UPDATE sessions SET revoked_at = unixepoch() WHERE user_id = ? AND revoked_at IS NULL`, userID); err != nil {
			return err
		}
	} else if _, err := tx.Exec(`UPDATE sessions SET revoked_at = unixepoch() WHERE user_id = ? AND token_hash != ? AND revoked_at IS NULL`, userID, hashToken(keepSessionToken)); err != nil {
		return err
	}
	if _, err := tx.Exec(`UPDATE pre_auth_sessions SET consumed_at = unixepoch() WHERE user_id = ? AND consumed_at IS NULL`, userID); err != nil {
		return err
	}
	_, err := tx.Exec(`UPDATE mfa_setup_challenges SET consumed_at = unixepoch() WHERE user_id = ? AND consumed_at IS NULL`, userID)
	return err
}

func (s *Service) CreatePreAuthSession(userID, method string, ttl time.Duration, rememberMe bool) (string, time.Time, error) {
	token, err := randomToken()
	if err != nil {
		return "", time.Time{}, err
	}
	now := time.Now()
	expiresAt := now.Add(ttl)
	tokenHash := hashToken(token)
	flag := 0
	if rememberMe {
		flag = 1
	}
	res, err := s.DB.Exec(`
		INSERT INTO pre_auth_sessions (id, user_id, token_hash, method, expires_at, remember_me, created_at)
		SELECT ?, u.id, ?, ?, ?, ?, ?
		FROM users u
		WHERE u.id = ? AND lower(trim(u.status)) = 'active' AND u.deleted_at = 0
	`, uuid.NewString(), tokenHash, method, expiresAt.Unix(), flag, now.Unix(), userID)
	if err != nil {
		return "", time.Time{}, err
	}
	affected, err := res.RowsAffected()
	if err != nil {
		return "", time.Time{}, err
	}
	if affected != 1 {
		return "", time.Time{}, ErrInactiveAccount
	}
	return token, expiresAt, nil
}

func (s *Service) ResolvePreAuthSession(token string) (string, string, bool, error) {
	tokenHash := hashToken(token)
	var id, userID string
	var rememberMeFlag int
	err := s.DB.QueryRow(`
		SELECT p.id, p.user_id, COALESCE(p.remember_me, 0)
		FROM pre_auth_sessions p
		JOIN users u ON u.id = p.user_id
		WHERE p.token_hash = ? AND p.consumed_at IS NULL AND p.expires_at > unixepoch()
		  AND lower(trim(u.status)) = 'active' AND u.deleted_at = 0
		LIMIT 1
	`, tokenHash).Scan(&id, &userID, &rememberMeFlag)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return "", "", false, ErrPreAuthNotFound
		}
		return "", "", false, err
	}
	return id, userID, rememberMeFlag == 1, nil
}

func (s *Service) ConsumePreAuthSession(token string) error {
	tokenHash := hashToken(token)
	res, err := s.DB.Exec(`
		UPDATE pre_auth_sessions
		SET consumed_at = unixepoch()
		WHERE token_hash = ? AND consumed_at IS NULL AND expires_at > unixepoch()
	`, tokenHash)
	if err != nil {
		return err
	}
	affected, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if affected != 1 {
		return ErrPreAuthNotFound
	}
	return nil
}

// ConsumePreAuthSessionWithTOTP consumes both authentication artifacts in one
// transaction. A TOTP time-step can only be accepted once per user, including
// when two valid pre-auth sessions race with the same six-digit code.
func (s *Service) ConsumePreAuthSessionWithTOTP(token, userID string, timeStep int64) error {
	if timeStep < 0 {
		return ErrTOTPReplay
	}
	tx, err := s.DB.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	res, err := tx.Exec(`
		UPDATE pre_auth_sessions
		SET consumed_at = unixepoch()
		WHERE token_hash = ? AND user_id = ? AND consumed_at IS NULL AND expires_at > unixepoch()
	`, hashToken(token), userID)
	if err != nil {
		return err
	}
	affected, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if affected != 1 {
		return ErrPreAuthNotFound
	}

	res, err = tx.Exec(`
		UPDATE users
		SET totp_last_used_step = ?
		WHERE id = ? AND totp_enabled = 1 AND totp_last_used_step < ?
	`, timeStep, userID, timeStep)
	if err != nil {
		return err
	}
	affected, err = res.RowsAffected()
	if err != nil {
		return err
	}
	if affected != 1 {
		return ErrTOTPReplay
	}
	return tx.Commit()
}

func (s *Service) ConsumeTOTPTimeStep(userID string, timeStep int64) error {
	if timeStep < 0 {
		return ErrTOTPReplay
	}
	res, err := s.DB.Exec(`
		UPDATE users
		SET totp_last_used_step = ?
		WHERE id = ? AND totp_enabled = 1 AND totp_last_used_step < ?
	`, timeStep, userID, timeStep)
	if err != nil {
		return err
	}
	affected, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if affected != 1 {
		return ErrTOTPReplay
	}
	return nil
}

func (s *Service) CleanupExpiredSessions(now time.Time) (sessionsDeleted, preAuthDeleted int, err error) {
	nowUnix := now.Unix()
	res, err := s.DB.Exec(`DELETE FROM sessions WHERE expires_at < ? AND revoked_at IS NOT NULL AND revoked_at < ?`, nowUnix, nowUnix-7*86400)
	if err != nil {
		return 0, 0, fmt.Errorf("cleanup expired sessions: %w", err)
	}
	affected, _ := res.RowsAffected()
	sessionsDeleted = int(affected)

	res, err = s.DB.Exec(`DELETE FROM sessions WHERE expires_at < ? AND revoked_at IS NULL`, nowUnix)
	if err != nil {
		return sessionsDeleted, 0, fmt.Errorf("cleanup expired sessions (no revoke): %w", err)
	}
	affected, _ = res.RowsAffected()
	sessionsDeleted += int(affected)

	res, err = s.DB.Exec(`DELETE FROM pre_auth_sessions WHERE expires_at < ?`, nowUnix)
	if err != nil {
		return sessionsDeleted, 0, fmt.Errorf("cleanup expired pre_auth_sessions: %w", err)
	}
	affected, _ = res.RowsAffected()
	preAuthDeleted = int(affected)

	return sessionsDeleted, preAuthDeleted, nil
}

func (s *Service) IsUserAdmin(userID string) (bool, error) {
	var count int
	if err := s.DB.QueryRow(`SELECT COUNT(1) FROM workspace_members WHERE user_id = ? AND role = 'ADMIN'`, userID).Scan(&count); err != nil {
		return false, fmt.Errorf("check admin role: %w", err)
	}
	return count > 0, nil
}

func randomToken() (string, error) {
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(buf), nil
}

func hashToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}
