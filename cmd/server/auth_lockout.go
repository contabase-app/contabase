package main

import (
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
)

const (
	authLockoutPasswordLimit  = 8
	authLockoutTOTPEventLimit = 5
	authLockoutWindow         = 30 * time.Minute
	authLockoutDuration       = 15 * time.Minute
)

type authLockoutStage string

const (
	authLockoutStagePassword authLockoutStage = "password"
	authLockoutStageTOTP     authLockoutStage = "totp"
)

type authLockoutState struct {
	FailedPasswordCount int
	Failed2FACount      int
	FirstFailedAt       int64
	LastFailedAt        int64
	LockedUntil         int64
	LockReason          string
}

func isAuthLocked(db *sql.DB, userID string, now time.Time) (bool, error) {
	userID = strings.TrimSpace(userID)
	if db == nil || userID == "" {
		return false, nil
	}
	if now.IsZero() {
		now = time.Now()
	}
	var lockedUntil int64
	err := db.QueryRow(`SELECT locked_until FROM auth_lockouts WHERE user_id = ?`, userID).Scan(&lockedUntil)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("load auth lockout: %w", err)
	}
	return lockedUntil > now.Unix(), nil
}

func recordAuthFailure(db *sql.DB, userID string, stage authLockoutStage, now time.Time) (bool, error) {
	userID = strings.TrimSpace(userID)
	if db == nil || userID == "" {
		return false, nil
	}
	if stage != authLockoutStagePassword && stage != authLockoutStageTOTP {
		return false, fmt.Errorf("invalid auth lockout stage")
	}
	if now.IsZero() {
		now = time.Now()
	}
	nowUnix := now.Unix()
	tx, err := db.Begin()
	if err != nil {
		return false, fmt.Errorf("begin auth lockout update: %w", err)
	}
	defer tx.Rollback()

	state, exists, err := loadAuthLockoutState(tx, userID)
	if err != nil {
		return false, err
	}
	if exists && state.LockedUntil > nowUnix {
		if err := tx.Commit(); err != nil {
			return false, fmt.Errorf("commit existing auth lockout: %w", err)
		}
		return true, nil
	}
	if !exists || state.FirstFailedAt <= 0 || nowUnix-state.FirstFailedAt > int64(authLockoutWindow.Seconds()) {
		state = authLockoutState{FirstFailedAt: nowUnix}
	}

	switch stage {
	case authLockoutStagePassword:
		state.FailedPasswordCount++
	case authLockoutStageTOTP:
		state.Failed2FACount++
	}
	state.LastFailedAt = nowUnix
	locked := false
	if stage == authLockoutStagePassword && state.FailedPasswordCount >= authLockoutPasswordLimit {
		locked = true
		state.LockedUntil = now.Add(authLockoutDuration).Unix()
		state.LockReason = string(authLockoutStagePassword)
	}
	if stage == authLockoutStageTOTP && state.Failed2FACount >= authLockoutTOTPEventLimit {
		locked = true
		state.LockedUntil = now.Add(authLockoutDuration).Unix()
		state.LockReason = string(authLockoutStageTOTP)
	}

	if err := upsertAuthLockoutState(tx, userID, state, nowUnix); err != nil {
		return false, err
	}
	if err := tx.Commit(); err != nil {
		return false, fmt.Errorf("commit auth lockout update: %w", err)
	}
	return locked, nil
}

func clearAuthFailureStage(db *sql.DB, userID string, stage authLockoutStage) error {
	userID = strings.TrimSpace(userID)
	if db == nil || userID == "" {
		return nil
	}
	switch stage {
	case authLockoutStagePassword:
		_, err := db.Exec(`
			UPDATE auth_lockouts
			SET failed_password_count = 0,
			    first_failed_at = CASE WHEN failed_2fa_count = 0 THEN 0 ELSE first_failed_at END,
			    last_failed_at = CASE WHEN failed_2fa_count = 0 THEN 0 ELSE last_failed_at END,
			    locked_until = CASE WHEN lock_reason = 'password' THEN 0 ELSE locked_until END,
			    lock_reason = CASE WHEN lock_reason = 'password' THEN '' ELSE lock_reason END,
			    updated_at = unixepoch()
			WHERE user_id = ?
		`, userID)
		if err != nil {
			return fmt.Errorf("clear password auth lockout: %w", err)
		}
		return nil
	case authLockoutStageTOTP:
		_, err := db.Exec(`
			UPDATE auth_lockouts
			SET failed_2fa_count = 0,
			    first_failed_at = CASE WHEN failed_password_count = 0 THEN 0 ELSE first_failed_at END,
			    last_failed_at = CASE WHEN failed_password_count = 0 THEN 0 ELSE last_failed_at END,
			    locked_until = CASE WHEN lock_reason = 'totp' THEN 0 ELSE locked_until END,
			    lock_reason = CASE WHEN lock_reason = 'totp' THEN '' ELSE lock_reason END,
			    updated_at = unixepoch()
			WHERE user_id = ?
		`, userID)
		if err != nil {
			return fmt.Errorf("clear totp auth lockout: %w", err)
		}
		return nil
	default:
		return fmt.Errorf("invalid auth lockout stage")
	}
}

func clearAuthLockout(db *sql.DB, userID string) error {
	userID = strings.TrimSpace(userID)
	if db == nil || userID == "" {
		return nil
	}
	if _, err := db.Exec(`DELETE FROM auth_lockouts WHERE user_id = ?`, userID); err != nil {
		return fmt.Errorf("clear auth lockout: %w", err)
	}
	return nil
}

func loadAuthLockoutState(tx *sql.Tx, userID string) (authLockoutState, bool, error) {
	var state authLockoutState
	err := tx.QueryRow(`
		SELECT failed_password_count, failed_2fa_count, first_failed_at, last_failed_at, locked_until, lock_reason
		FROM auth_lockouts
		WHERE user_id = ?
	`, userID).Scan(
		&state.FailedPasswordCount,
		&state.Failed2FACount,
		&state.FirstFailedAt,
		&state.LastFailedAt,
		&state.LockedUntil,
		&state.LockReason,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return authLockoutState{}, false, nil
	}
	if err != nil {
		return authLockoutState{}, false, fmt.Errorf("load auth lockout state: %w", err)
	}
	return state, true, nil
}

func upsertAuthLockoutState(tx *sql.Tx, userID string, state authLockoutState, nowUnix int64) error {
	_, err := tx.Exec(`
		INSERT INTO auth_lockouts (
			user_id, failed_password_count, failed_2fa_count, first_failed_at, last_failed_at, locked_until, lock_reason, updated_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(user_id) DO UPDATE SET
			failed_password_count = excluded.failed_password_count,
			failed_2fa_count = excluded.failed_2fa_count,
			first_failed_at = excluded.first_failed_at,
			last_failed_at = excluded.last_failed_at,
			locked_until = excluded.locked_until,
			lock_reason = excluded.lock_reason,
			updated_at = excluded.updated_at
	`, userID, state.FailedPasswordCount, state.Failed2FACount, state.FirstFailedAt, state.LastFailedAt, state.LockedUntil, state.LockReason, nowUnix)
	if err != nil {
		return fmt.Errorf("upsert auth lockout state: %w", err)
	}
	return nil
}
