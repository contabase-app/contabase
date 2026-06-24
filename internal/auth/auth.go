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
	err := s.DB.QueryRow(`SELECT id, password_hash, COALESCE(status, 'active') FROM users WHERE lower(email) = lower(?)`, email).Scan(&userID, &passwordHash, &status)
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
	return userID, nil
}

func (s *Service) CreateSession(userID, workspaceID string, ttl time.Duration, isRemember bool) (string, time.Time, error) {
	token, err := randomToken()
	if err != nil {
		return "", time.Time{}, err
	}
	now := time.Now()
	expiresAt := now.Add(ttl)
	tokenHash := hashToken(token)
	flag := 0
	if isRemember {
		flag = 1
	}
	_, err = s.DB.Exec(
		`INSERT INTO sessions (id, user_id, workspace_id, token_hash, expires_at, is_remember, created_at) VALUES (?, ?, ?, ?, ?, ?, ?)`,
		uuid.NewString(), userID, workspaceID, tokenHash, expiresAt.Unix(), flag, now.Unix(),
	)
	if err != nil {
		return "", time.Time{}, err
	}
	return token, expiresAt, nil
}

func (s *Service) ResolveAndRefreshSession(token string) (string, string, int, bool, error) {
	tokenHash := hashToken(token)
	now := time.Now().Unix()
	var userID, workspaceID string
	var expiresAt int64
	var isRemember int
	err := s.DB.QueryRow(
		`SELECT user_id, workspace_id, expires_at, is_remember FROM sessions WHERE token_hash = ? AND revoked_at IS NULL AND expires_at > ?`,
		tokenHash, now,
	).Scan(&userID, &workspaceID, &expiresAt, &isRemember)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return "", "", 0, false, sql.ErrNoRows
		}
		return "", "", 0, false, err
	}
	if strings.TrimSpace(workspaceID) == "" {
		return "", "", 0, false, sql.ErrNoRows
	}

	var ttl int64
	if isRemember == 1 {
		ttl = 30 * 24 * 3600
	} else {
		ttl = 24 * 3600
	}

	remaining := expiresAt - now
	if remaining < ttl/2 {
		newExpiresAt := now + ttl
		_, updateErr := s.DB.Exec(`UPDATE sessions SET expires_at = ? WHERE token_hash = ?`, newExpiresAt, tokenHash)
		if updateErr != nil {
			return "", "", 0, false, updateErr
		}
		return userID, workspaceID, int(ttl), true, nil
	}

	return userID, workspaceID, 0, false, nil
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
	_, err = s.DB.Exec(`
		INSERT INTO pre_auth_sessions (id, user_id, token_hash, method, expires_at, remember_me, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?)
	`, uuid.NewString(), userID, tokenHash, method, expiresAt.Unix(), flag, now.Unix())
	if err != nil {
		return "", time.Time{}, err
	}
	return token, expiresAt, nil
}

func (s *Service) ResolvePreAuthSession(token string) (string, string, bool, error) {
	tokenHash := hashToken(token)
	var id, userID string
	var rememberMeFlag int
	err := s.DB.QueryRow(`
		SELECT id, user_id, COALESCE(remember_me, 0)
		FROM pre_auth_sessions
		WHERE token_hash = ? AND consumed_at IS NULL AND expires_at > unixepoch()
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
