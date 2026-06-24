package admincli

import (
	"crypto/rand"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/contabase-app/contabase/internal/repository"

	"github.com/google/uuid"
	"golang.org/x/crypto/bcrypt"
)

var (
	ErrUserNotFound = errors.New("usuário não encontrado")
	ErrUserNotAdmin = errors.New("usuário não é administrador")
)

type DisableAdmin2FAResult struct {
	Email      string
	WasEnabled bool
}

type UserRecoveryResult struct {
	UserID     string
	Email      string
	WasEnabled bool
}

type TemporaryPasswordResetResult struct {
	UserID            string
	Email             string
	TemporaryPassword string
	ExpiresAt         int64
}

type AuthLockout struct {
	UserID              string
	Name                string
	Email               string
	FailedPasswordCount int
	Failed2FACount      int
	FirstFailedAt       int64
	LastFailedAt        int64
	LockedUntil         int64
	LockReason          string
	UpdatedAt           int64
}

type UnlockAuthLockoutResult struct {
	UserID  string
	Email   string
	Removed bool
}

type ClearExpiredAuthLockoutsResult struct {
	Removed int64
}

type RecoveryAudit struct {
	EventType string
	IP        string
	UserAgent string
	Metadata  map[string]string
}

const TemporaryPasswordTTL = time.Hour

// ResetAdminPassword generates a temporary password for a local user identified by email.
func ResetAdminPassword(db *sql.DB, email string) (TemporaryPasswordResetResult, error) {
	return ResetUserTemporaryPassword(db, email, TemporaryPasswordTTL, RecoveryAudit{
		EventType: "USER_TEMPORARY_PASSWORD_RESET_LOCAL_RECOVERY",
		IP:        "local-cli",
		UserAgent: "admin-cli",
		Metadata:  map[string]string{"method": "local_admin_cli"},
	})
}

func ResetUserTemporaryPassword(db *sql.DB, email string, ttl time.Duration, audit RecoveryAudit) (TemporaryPasswordResetResult, error) {
	email = strings.ToLower(strings.TrimSpace(email))
	if email == "" {
		return TemporaryPasswordResetResult{}, ErrUserNotFound
	}

	var userID string
	err := db.QueryRow(`SELECT id FROM users WHERE lower(email) = ?`, email).Scan(&userID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return TemporaryPasswordResetResult{}, ErrUserNotFound
		}
		return TemporaryPasswordResetResult{}, fmt.Errorf("erro ao buscar usuário: %w", err)
	}

	password, err := generateTemporaryPassword()
	if err != nil {
		return TemporaryPasswordResetResult{}, fmt.Errorf("erro ao gerar senha temporária: %w", err)
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		return TemporaryPasswordResetResult{}, fmt.Errorf("erro ao gerar hash da senha: %w", err)
	}
	if ttl <= 0 {
		ttl = TemporaryPasswordTTL
	}
	expiresAt := time.Now().Add(ttl).Unix()

	tx, err := db.Begin()
	if err != nil {
		return TemporaryPasswordResetResult{}, fmt.Errorf("erro ao iniciar transação: %w", err)
	}
	defer tx.Rollback()

	_, err = tx.Exec(`
		UPDATE users 
		SET password_hash = ?,
		    status = 'active',
		    must_change_password = 1,
		    temporary_password_expires_at = ?,
		    updated_at = unixepoch()
		WHERE id = ?
	`, string(hash), expiresAt, userID)
	if err != nil {
		return TemporaryPasswordResetResult{}, fmt.Errorf("erro ao atualizar senha: %w", err)
	}
	if err := revokeUserAuthStateTx(tx, userID); err != nil {
		return TemporaryPasswordResetResult{}, err
	}
	if audit.EventType == "" {
		audit.EventType = "USER_TEMPORARY_PASSWORD_RESET"
	}
	if audit.Metadata == nil {
		audit.Metadata = map[string]string{}
	}
	audit.Metadata["temporary_password_ttl_seconds"] = fmt.Sprintf("%d", int64(ttl.Seconds()))
	if err := insertRecoveryAuditTx(tx, userID, audit); err != nil {
		return TemporaryPasswordResetResult{}, fmt.Errorf("erro ao registrar auditoria: %w", err)
	}

	if err := tx.Commit(); err != nil {
		return TemporaryPasswordResetResult{}, fmt.Errorf("erro ao salvar alterações: %w", err)
	}
	return TemporaryPasswordResetResult{
		UserID:            userID,
		Email:             email,
		TemporaryPassword: password,
		ExpiresAt:         expiresAt,
	}, nil
}

func DisableAdmin2FA(db *sql.DB, email string) (DisableAdmin2FAResult, error) {
	result, err := DisableUser2FA(db, email)
	return DisableAdmin2FAResult{Email: result.Email, WasEnabled: result.WasEnabled}, err
}

func DisableUser2FA(db *sql.DB, email string) (UserRecoveryResult, error) {
	return DisableUser2FAWithAudit(db, email, RecoveryAudit{
		EventType: "USER_2FA_DISABLED_LOCAL_RECOVERY",
		IP:        "local-cli",
		UserAgent: "admin-cli",
		Metadata:  map[string]string{"method": "local_admin_cli"},
	})
}

func DisableUser2FAWithAudit(db *sql.DB, email string, audit RecoveryAudit) (UserRecoveryResult, error) {
	email = strings.ToLower(strings.TrimSpace(email))
	if email == "" {
		return UserRecoveryResult{}, ErrUserNotFound
	}

	userID, wasEnabled, err := lookup2FARecoveryUser(db, email)
	if err != nil {
		return UserRecoveryResult{}, err
	}

	tx, err := db.Begin()
	if err != nil {
		return UserRecoveryResult{}, fmt.Errorf("erro ao iniciar transação: %w", err)
	}
	defer tx.Rollback()

	if _, err := tx.Exec(`
		UPDATE users
		SET totp_enabled = 0,
		    totp_secret_enc = NULL,
		    totp_backup_codes = '[]',
		    totp_enabled_at = NULL,
		    updated_at = unixepoch()
		WHERE id = ?
	`, userID); err != nil {
		return UserRecoveryResult{}, fmt.Errorf("erro ao desativar 2FA: %w", err)
	}
	if err := revokeUserAuthStateTx(tx, userID); err != nil {
		return UserRecoveryResult{}, err
	}
	if audit.EventType == "" {
		audit.EventType = "USER_2FA_DISABLED_LOCAL_RECOVERY"
	}
	if err := insertRecoveryAuditTx(tx, userID, audit); err != nil {
		return UserRecoveryResult{}, fmt.Errorf("erro ao registrar auditoria: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return UserRecoveryResult{}, fmt.Errorf("erro ao salvar alterações: %w", err)
	}

	return UserRecoveryResult{UserID: userID, Email: email, WasEnabled: wasEnabled}, nil
}

func ListAuthLockouts(db *sql.DB, includeExpired bool, now time.Time) ([]AuthLockout, error) {
	if now.IsZero() {
		now = time.Now()
	}
	where := `WHERE al.locked_until > ?`
	args := []any{now.Unix()}
	if includeExpired {
		where = ``
		args = nil
	}
	rows, err := db.Query(`
		SELECT al.user_id,
		       COALESCE(u.name, ''),
		       COALESCE(u.email, ''),
		       al.failed_password_count,
		       al.failed_2fa_count,
		       al.first_failed_at,
		       al.last_failed_at,
		       al.locked_until,
		       al.lock_reason,
		       al.updated_at
		FROM auth_lockouts al
		LEFT JOIN users u ON u.id = al.user_id
		`+where+`
		ORDER BY al.locked_until DESC, al.updated_at DESC
	`, args...)
	if err != nil {
		return nil, fmt.Errorf("erro ao listar bloqueios: %w", err)
	}
	defer rows.Close()

	var lockouts []AuthLockout
	for rows.Next() {
		var item AuthLockout
		if err := rows.Scan(
			&item.UserID,
			&item.Name,
			&item.Email,
			&item.FailedPasswordCount,
			&item.Failed2FACount,
			&item.FirstFailedAt,
			&item.LastFailedAt,
			&item.LockedUntil,
			&item.LockReason,
			&item.UpdatedAt,
		); err != nil {
			return nil, fmt.Errorf("erro ao ler bloqueio: %w", err)
		}
		lockouts = append(lockouts, item)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("erro ao percorrer bloqueios: %w", err)
	}
	return lockouts, nil
}

func UnlockAuthLockoutByEmail(db *sql.DB, email string) (UnlockAuthLockoutResult, error) {
	email = strings.ToLower(strings.TrimSpace(email))
	if email == "" {
		return UnlockAuthLockoutResult{}, ErrUserNotFound
	}
	userID, normalizedEmail, err := lookupUserByEmail(db, email)
	if err != nil {
		return UnlockAuthLockoutResult{}, err
	}
	removed, err := deleteAuthLockout(db, userID)
	if err != nil {
		return UnlockAuthLockoutResult{}, err
	}
	return UnlockAuthLockoutResult{UserID: userID, Email: normalizedEmail, Removed: removed}, nil
}

func UnlockAuthLockoutByUserID(db *sql.DB, userID string) (UnlockAuthLockoutResult, error) {
	userID = strings.TrimSpace(userID)
	if userID == "" {
		return UnlockAuthLockoutResult{}, ErrUserNotFound
	}
	email, err := lookupUserEmailByID(db, userID)
	if err != nil {
		return UnlockAuthLockoutResult{}, err
	}
	removed, err := deleteAuthLockout(db, userID)
	if err != nil {
		return UnlockAuthLockoutResult{}, err
	}
	return UnlockAuthLockoutResult{UserID: userID, Email: email, Removed: removed}, nil
}

func ClearExpiredAuthLockouts(db *sql.DB, now time.Time) (ClearExpiredAuthLockoutsResult, error) {
	if now.IsZero() {
		now = time.Now()
	}
	res, err := db.Exec(`DELETE FROM auth_lockouts WHERE locked_until > 0 AND locked_until <= ?`, now.Unix())
	if err != nil {
		return ClearExpiredAuthLockoutsResult{}, fmt.Errorf("erro ao limpar bloqueios expirados: %w", err)
	}
	removed, err := res.RowsAffected()
	if err != nil {
		return ClearExpiredAuthLockoutsResult{}, fmt.Errorf("erro ao contar bloqueios removidos: %w", err)
	}
	return ClearExpiredAuthLockoutsResult{Removed: removed}, nil
}

func RevokeUserAuthStateWithAudit(db *sql.DB, email string, audit RecoveryAudit) (UserRecoveryResult, error) {
	email = strings.ToLower(strings.TrimSpace(email))
	if email == "" {
		return UserRecoveryResult{}, ErrUserNotFound
	}
	userID, wasEnabled, err := lookup2FARecoveryUser(db, email)
	if err != nil {
		return UserRecoveryResult{}, err
	}
	tx, err := db.Begin()
	if err != nil {
		return UserRecoveryResult{}, fmt.Errorf("erro ao iniciar transação: %w", err)
	}
	defer tx.Rollback()
	if err := revokeUserAuthStateTx(tx, userID); err != nil {
		return UserRecoveryResult{}, err
	}
	if audit.EventType == "" {
		audit.EventType = "USER_SESSIONS_REVOKED_ADMIN_RECOVERY"
	}
	if err := insertRecoveryAuditTx(tx, userID, audit); err != nil {
		return UserRecoveryResult{}, fmt.Errorf("erro ao registrar auditoria: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return UserRecoveryResult{}, fmt.Errorf("erro ao salvar alterações: %w", err)
	}
	return UserRecoveryResult{UserID: userID, Email: email, WasEnabled: wasEnabled}, nil
}

func lookup2FARecoveryUser(db *sql.DB, email string) (string, bool, error) {
	var userID string
	var wasEnabled int
	err := db.QueryRow(`SELECT id, COALESCE(totp_enabled, 0) FROM users WHERE lower(email) = ?`, email).Scan(&userID, &wasEnabled)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return "", false, ErrUserNotFound
		}
		return "", false, fmt.Errorf("erro ao buscar usuário: %w", err)
	}
	return userID, wasEnabled == 1, nil
}

func lookupUserByEmail(db *sql.DB, email string) (string, string, error) {
	var userID, normalizedEmail string
	err := db.QueryRow(`SELECT id, email FROM users WHERE lower(email) = ?`, email).Scan(&userID, &normalizedEmail)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return "", "", ErrUserNotFound
		}
		return "", "", fmt.Errorf("erro ao buscar usuário: %w", err)
	}
	return userID, strings.ToLower(strings.TrimSpace(normalizedEmail)), nil
}

func lookupUserEmailByID(db *sql.DB, userID string) (string, error) {
	var email string
	err := db.QueryRow(`SELECT email FROM users WHERE id = ?`, userID).Scan(&email)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return "", ErrUserNotFound
		}
		return "", fmt.Errorf("erro ao buscar usuário: %w", err)
	}
	return strings.ToLower(strings.TrimSpace(email)), nil
}

func deleteAuthLockout(db *sql.DB, userID string) (bool, error) {
	res, err := db.Exec(`DELETE FROM auth_lockouts WHERE user_id = ?`, userID)
	if err != nil {
		return false, fmt.Errorf("erro ao desbloquear usuário: %w", err)
	}
	removed, err := res.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("erro ao contar bloqueios removidos: %w", err)
	}
	return removed > 0, nil
}

func revokeUserAuthStateTx(tx *sql.Tx, userID string) error {
	if _, err := tx.Exec(`UPDATE sessions SET revoked_at = unixepoch() WHERE user_id = ? AND revoked_at IS NULL`, userID); err != nil {
		return fmt.Errorf("erro ao revogar sessões: %w", err)
	}
	if _, err := tx.Exec(`UPDATE pre_auth_sessions SET consumed_at = unixepoch() WHERE user_id = ? AND consumed_at IS NULL`, userID); err != nil {
		return fmt.Errorf("erro ao revogar pre-auth: %w", err)
	}
	if _, err := tx.Exec(`DELETE FROM auth_lockouts WHERE user_id = ?`, userID); err != nil {
		return fmt.Errorf("erro ao limpar bloqueios: %w", err)
	}
	return nil
}

func insertRecoveryAuditTx(tx *sql.Tx, userID string, audit RecoveryAudit) error {
	eventType := strings.TrimSpace(audit.EventType)
	if eventType == "" {
		eventType = "USER_RECOVERY_ACTION"
	}
	ip := strings.TrimSpace(audit.IP)
	userAgent := strings.TrimSpace(audit.UserAgent)
	metadata := audit.Metadata
	if metadata == nil {
		metadata = map[string]string{}
	}
	metadataJSON, err := json.Marshal(metadata)
	if err != nil {
		metadataJSON = []byte("{}")
	}
	_, err = tx.Exec(`
		INSERT INTO auth_audit_events (id, user_id, workspace_id, event_type, ip, user_agent, metadata_json, created_at)
		VALUES (?, ?, NULL, ?, ?, ?, ?, unixepoch())
	`, uuid.NewString(), userID, eventType, ip, userAgent, string(metadataJSON))
	return err
}

func generateTemporaryPassword() (string, error) {
	raw := make([]byte, 18)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(raw), nil
}

type RepairOrphanCardsResult struct {
	Diagnosed int
	Repaired  int
	Details   []string
}

func RepairOrphanCreditCards(db *sql.DB, dryRun bool) (RepairOrphanCardsResult, error) {
	repo := repository.NewConfigRepository(db)
	repoResult, err := repo.RepairOrphanCreditCards(dryRun)
	if err != nil {
		return RepairOrphanCardsResult{}, fmt.Errorf("reparo de cartões órfãos: %w", err)
	}

	result := RepairOrphanCardsResult{
		Diagnosed: repoResult.Diagnosed,
		Repaired:  repoResult.Repaired,
	}

	for _, item := range repoResult.Orphans {
		detail := fmt.Sprintf("  - %s (id=%s, workspace=%s)", item.Name, item.AccountID, item.WorkspaceID)
		result.Details = append(result.Details, detail)
	}

	return result, nil
}
