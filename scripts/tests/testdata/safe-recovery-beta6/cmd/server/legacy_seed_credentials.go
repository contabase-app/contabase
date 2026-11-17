package main

import (
	"database/sql"
	"errors"
	"fmt"
	"log/slog"

	"github.com/google/uuid"
	"golang.org/x/crypto/bcrypt"
)

const (
	legacySeedUserID = "aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa"
	// This password is only used to recognize credentials written by older servers.
	legacySeedPassword     = "12345678"
	disabledLegacySeedHash = "!legacy-seed-credential-disabled"
)

func disableLegacySeedCredentials(db *sql.DB) error {
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	var hash string
	if err := tx.QueryRow(`SELECT password_hash FROM users WHERE id = ?`, legacySeedUserID).Scan(&hash); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil
		}
		return err
	}
	if hash == disabledLegacySeedHash {
		return nil
	}
	err = bcrypt.CompareHashAndPassword([]byte(hash), []byte(legacySeedPassword))
	if errors.Is(err, bcrypt.ErrMismatchedHashAndPassword) {
		return nil
	}

	// An unusable hash blocks the known password before any reset can be issued.
	// Keep status, deletion markers, memberships and enrolled factors unchanged.
	result, err := tx.Exec(`
		UPDATE users
		SET password_hash = ?, must_change_password = 1, temporary_password_expires_at = NULL,
		    activation_token_hash = NULL, activation_expires_at = NULL, updated_at = unixepoch()
		WHERE id = ? AND password_hash = ?
	`, disabledLegacySeedHash, legacySeedUserID, hash)
	if err != nil {
		return err
	}
	if affected, err := result.RowsAffected(); err != nil {
		return err
	} else if affected != 1 {
		return fmt.Errorf("legacy credential containment updated %d users, expected one", affected)
	}
	for _, query := range []string{
		`UPDATE sessions SET revoked_at = unixepoch() WHERE user_id = ? AND revoked_at IS NULL`,
		`UPDATE pre_auth_sessions SET consumed_at = unixepoch() WHERE user_id = ? AND consumed_at IS NULL`,
		`UPDATE mfa_setup_challenges SET consumed_at = unixepoch() WHERE user_id = ? AND consumed_at IS NULL`,
	} {
		if _, err := tx.Exec(query, legacySeedUserID); err != nil {
			return err
		}
	}
	if _, err := tx.Exec(`
		INSERT INTO auth_audit_events (id, user_id, event_type, ip, user_agent, metadata_json)
		VALUES (?, ?, 'LEGACY_SEED_CREDENTIAL_DISABLED', 'local-startup', 'server', '{}')
	`, uuid.NewString(), legacySeedUserID); err != nil {
		return fmt.Errorf("record legacy credential containment: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	slog.Warn("legacy seed credential disabled; administrative password recovery is required")
	return nil
}
