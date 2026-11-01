package database

import (
	"database/sql"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
)

func unixToDate(unix int64) time.Time {
	return time.Unix(unix, 0).UTC()
}

func nextInvoiceMonth(year int, month time.Month) (int, time.Month) {
	month++
	if month > 12 {
		return year + 1, 1
	}
	return year, month
}

func prevInvoiceMonth(year int, month time.Month) (int, time.Month) {
	month--
	if month < 1 {
		return year - 1, 12
	}
	return year, month
}

func calculateInvoiceDates(t time.Time, closingDay, dueDay int64) (closingUnix, dueUnix int64, reference string) {
	year, month := t.Year(), t.Month()
	if int64(t.Day()) >= closingDay {
		year, month = nextInvoiceMonth(year, month)
	}
	closingUnix = time.Date(year, month, int(closingDay), 12, 0, 0, 0, time.UTC).Unix()
	dueYear := year
	dueMonth := month
	if dueDay < closingDay {
		dueYear, dueMonth = nextInvoiceMonth(dueYear, dueMonth)
	}
	dueUnix = time.Date(dueYear, dueMonth, int(dueDay), 12, 0, 0, 0, time.UTC).Unix()
	reference = fmt.Sprintf("%04d-%02d", dueYear, int(dueMonth))
	return closingUnix, dueUnix, reference
}

type migration struct {
	version int
	name    string
	up      func(*sql.Tx) error
}

var migrations = []migration{
	{
		version: 1,
		name:    "baseline_recurrence_and_indexes",
		up: func(tx *sql.Tx) error {
			// LEGADO INATIVO: payment_status permanece somente para compatibilidade histórica.
			if err := addColumnIfMissing(tx, "transactions", "payment_status", "TEXT NOT NULL DEFAULT 'PAID' CHECK (payment_status IN ('PAID', 'PENDING'))"); err != nil {
				return err
			}
			if err := addColumnIfMissing(tx, "transactions", "status", "TEXT NOT NULL DEFAULT 'paid' CHECK (status IN ('paid', 'pending'))"); err != nil {
				return err
			}
			stmts := []string{
				`CREATE TABLE IF NOT EXISTS recurring_rules (
					id                      TEXT PRIMARY KEY,
					workspace_id            TEXT    NOT NULL,
					user_id                 TEXT    NOT NULL,
					account_id              TEXT    NOT NULL,
					destination_account_id  TEXT,
					category_id             TEXT,
					type                    TEXT    NOT NULL CHECK (type IN ('EXPENSE', 'INCOME', 'TRANSFER')),
					amount                  INTEGER NOT NULL CHECK (amount > 0),
					description             TEXT    NOT NULL DEFAULT '',
					start_date              INTEGER NOT NULL,
					frequency               TEXT    NOT NULL CHECK (frequency IN ('DAILY', 'WEEKLY', 'BIWEEKLY', 'MONTHLY', 'BIMONTHLY', 'QUARTERLY', 'SEMIANNUAL', 'ANNUAL')),
					default_payment_status  TEXT    NOT NULL DEFAULT 'PENDING' CHECK (default_payment_status IN ('PAID', 'PENDING')),
					active                  INTEGER NOT NULL DEFAULT 1 CHECK (active IN (0, 1)),
					generated_until         INTEGER,
					created_at              INTEGER NOT NULL DEFAULT (unixepoch()),
					updated_at              INTEGER NOT NULL DEFAULT (unixepoch()),
					FOREIGN KEY (workspace_id)           REFERENCES workspaces(id)  ON DELETE CASCADE,
					FOREIGN KEY (user_id)                REFERENCES users(id)       ON DELETE RESTRICT,
					FOREIGN KEY (account_id)             REFERENCES accounts(id)    ON DELETE RESTRICT,
					FOREIGN KEY (destination_account_id) REFERENCES accounts(id)    ON DELETE SET NULL,
					FOREIGN KEY (category_id)            REFERENCES categories(id)  ON DELETE SET NULL
				)`,
				`CREATE INDEX IF NOT EXISTS idx_recurring_rules_workspace ON recurring_rules(workspace_id)`,
				`CREATE INDEX IF NOT EXISTS idx_recurring_rules_active ON recurring_rules(active)`,
				`CREATE INDEX IF NOT EXISTS idx_recurring_rules_start_date ON recurring_rules(start_date)`,
				`CREATE UNIQUE INDEX IF NOT EXISTS idx_invoices_account_reference_unique ON invoices(account_id, reference)`,
				`CREATE INDEX IF NOT EXISTS idx_invoices_account_reference_status ON invoices(account_id, reference, status)`,
				`CREATE INDEX IF NOT EXISTS idx_transactions_workspace_date_created ON transactions(workspace_id, date DESC, created_at DESC)`,
				`CREATE INDEX IF NOT EXISTS idx_transactions_workspace_type_date ON transactions(workspace_id, type, date)`,
				`CREATE INDEX IF NOT EXISTS idx_transactions_workspace_category_type_date ON transactions(workspace_id, category_id, type, date)`,
				`CREATE INDEX IF NOT EXISTS idx_transactions_workspace_status_date ON transactions(workspace_id, status, date)`,
				`CREATE INDEX IF NOT EXISTS idx_transactions_workspace_invoice_date ON transactions(workspace_id, invoice_id, date DESC)`,
				`CREATE INDEX IF NOT EXISTS idx_box_virtual_ledger_box_reference_date ON box_virtual_ledger(box_id, reference_date)`,
				`CREATE INDEX IF NOT EXISTS idx_boxes_workspace_category ON boxes(workspace_id, category_id)`,
			}
			for _, stmt := range stmts {
				if _, err := tx.Exec(stmt); err != nil {
					return err
				}
			}
			return nil
		},
	},
	{
		version: 2,
		name:    "transactions_recurrence_columns",
		up: func(tx *sql.Tx) error {
			if err := addColumnIfMissing(tx, "transactions", "recurring_rule_id", "TEXT"); err != nil {
				return err
			}
			if err := addColumnIfMissing(tx, "transactions", "recurrence_sequence", "INTEGER"); err != nil {
				return err
			}
			if err := addColumnIfMissing(tx, "transactions", "notes", "TEXT NOT NULL DEFAULT ''"); err != nil {
				return err
			}
			if err := addColumnIfMissing(tx, "transactions", "attachment_path", "TEXT NOT NULL DEFAULT ''"); err != nil {
				return err
			}
			_, err := tx.Exec(`CREATE INDEX IF NOT EXISTS idx_transactions_recurring_rule ON transactions(recurring_rule_id)`)
			return err
		},
	},
	{
		version: 3,
		name:    "users_categories_notifications_additions",
		up: func(tx *sql.Tx) error {
			if err := addColumnIfMissing(tx, "users", "last_notifications_clear_at", "INTEGER NOT NULL DEFAULT 0"); err != nil {
				return err
			}
			if err := addColumnIfMissing(tx, "categories", "is_fixed", "INTEGER NOT NULL DEFAULT 0 CHECK (is_fixed IN (0, 1))"); err != nil {
				return err
			}
			if _, err := tx.Exec(`CREATE TABLE IF NOT EXISTS user_notification_dismissals (
				user_id       TEXT NOT NULL,
				workspace_id  TEXT NOT NULL,
				notification_key TEXT NOT NULL,
				dismissed_at  INTEGER NOT NULL DEFAULT (unixepoch()),
				PRIMARY KEY (user_id, workspace_id, notification_key),
				FOREIGN KEY (user_id)      REFERENCES users(id)      ON DELETE CASCADE,
				FOREIGN KEY (workspace_id) REFERENCES workspaces(id) ON DELETE CASCADE
			)`); err != nil {
				return err
			}
			_, err := tx.Exec(`CREATE INDEX IF NOT EXISTS idx_user_notification_dismissals_workspace ON user_notification_dismissals(workspace_id, user_id)`)
			return err
		},
	},
	{
		version: 4,
		name:    "sessions_table_for_auth",
		up: func(tx *sql.Tx) error {
			if _, err := tx.Exec(`CREATE TABLE IF NOT EXISTS sessions (
				id           TEXT PRIMARY KEY,
				user_id      TEXT NOT NULL,
				workspace_id TEXT NOT NULL,
				token_hash   TEXT NOT NULL UNIQUE,
				expires_at   INTEGER NOT NULL,
				revoked_at   INTEGER,
				created_at   INTEGER NOT NULL DEFAULT (unixepoch()),
				FOREIGN KEY (user_id) REFERENCES users(id) ON DELETE CASCADE,
				FOREIGN KEY (workspace_id) REFERENCES workspaces(id) ON DELETE CASCADE
			)`); err != nil {
				return err
			}
			if _, err := tx.Exec(`CREATE INDEX IF NOT EXISTS idx_sessions_user_id ON sessions(user_id)`); err != nil {
				return err
			}
			_, err := tx.Exec(`CREATE INDEX IF NOT EXISTS idx_sessions_expires_at ON sessions(expires_at)`)
			return err
		},
	},
	{
		version: 5,
		name:    "auth_hardening_status_activation_and_session_workspace",
		up: func(tx *sql.Tx) error {
			if err := addColumnIfMissing(tx, "users", "status", "TEXT NOT NULL DEFAULT 'active' CHECK (status IN ('active', 'pending'))"); err != nil {
				return err
			}
			if err := addColumnIfMissing(tx, "users", "activation_token_hash", "TEXT"); err != nil {
				return err
			}
			if err := addColumnIfMissing(tx, "users", "activation_expires_at", "INTEGER"); err != nil {
				return err
			}
			if err := addColumnIfMissing(tx, "sessions", "workspace_id", "TEXT"); err != nil {
				return err
			}
			if _, err := tx.Exec(`UPDATE sessions SET workspace_id = COALESCE(workspace_id, (
				SELECT wm.workspace_id
				FROM workspace_members wm
				WHERE wm.user_id = sessions.user_id
				ORDER BY wm.joined_at ASC
				LIMIT 1
			)) WHERE workspace_id IS NULL OR workspace_id = ''`); err != nil {
				return err
			}
			if _, err := tx.Exec(`UPDATE users
				SET status = CASE
					WHEN lower(password_hash) = 'pending' THEN 'pending'
					ELSE 'active'
				END
				WHERE status IS NULL OR status = ''`); err != nil {
				return err
			}
			_, err := tx.Exec(`CREATE INDEX IF NOT EXISTS idx_users_activation_token_hash ON users(activation_token_hash)`)
			return err
		},
	},
	{
		version: 6,
		name:    "users_profile_photo_path",
		up: func(tx *sql.Tx) error {
			return addColumnIfMissing(tx, "users", "profile_photo_path", "TEXT NOT NULL DEFAULT ''")
		},
	},
	{
		version: 7,
		name:    "categories_macro_group",
		up: func(tx *sql.Tx) error {
			return addColumnIfMissing(tx, "categories", "macro_group", "TEXT NOT NULL DEFAULT 'Estilo de Vida' CHECK (macro_group IN ('Essencial', 'Estilo de Vida', 'Receitas', 'Receitas Operacionais', 'Deduções/Impostos', 'Custos Operacionais', 'Despesas Administrativas', 'Despesas Comerciais', 'Equipe e Prestadores', 'Financeiro', 'Investimentos/Outros'))")
		},
	},
	{
		version: 8,
		name:    "categories_parent_and_nullable_macro_group",
		up: func(tx *sql.Tx) error {
			if err := addColumnIfMissing(tx, "categories", "parent_id", "TEXT REFERENCES categories(id) ON DELETE CASCADE"); err != nil {
				return err
			}
			_, err := tx.Exec(`CREATE INDEX IF NOT EXISTS idx_categories_parent ON categories(parent_id)`)
			return err
		},
	},
	{
		version: 9,
		name:    "users_default_workspace_id",
		up: func(tx *sql.Tx) error {
			return addColumnIfMissing(tx, "users", "default_workspace_id", "TEXT REFERENCES workspaces(id) ON DELETE SET NULL")
		},
	},
	{
		version: 10,
		name:    "security_2fa_preauth_and_auth_audit",
		up: func(tx *sql.Tx) error {
			if err := addColumnIfMissing(tx, "users", "totp_enabled", "INTEGER NOT NULL DEFAULT 0 CHECK (totp_enabled IN (0, 1))"); err != nil {
				return err
			}
			if err := addColumnIfMissing(tx, "users", "totp_secret_enc", "TEXT"); err != nil {
				return err
			}
			if err := addColumnIfMissing(tx, "users", "totp_backup_codes", "TEXT NOT NULL DEFAULT '[]'"); err != nil {
				return err
			}
			if err := addColumnIfMissing(tx, "users", "totp_enabled_at", "INTEGER"); err != nil {
				return err
			}
			stmts := []string{
				`CREATE TABLE IF NOT EXISTS pre_auth_sessions (
					id           TEXT PRIMARY KEY,
					user_id      TEXT NOT NULL,
					token_hash   TEXT NOT NULL UNIQUE,
					method       TEXT NOT NULL CHECK (method IN ('TOTP')),
					expires_at   INTEGER NOT NULL,
					consumed_at  INTEGER,
					created_at   INTEGER NOT NULL DEFAULT (unixepoch()),
					FOREIGN KEY (user_id) REFERENCES users(id) ON DELETE CASCADE
				)`,
				`CREATE INDEX IF NOT EXISTS idx_pre_auth_sessions_user_id ON pre_auth_sessions(user_id)`,
				`CREATE INDEX IF NOT EXISTS idx_pre_auth_sessions_expires_at ON pre_auth_sessions(expires_at)`,
				`CREATE TABLE IF NOT EXISTS auth_audit_events (
					id            TEXT PRIMARY KEY,
					user_id       TEXT,
					workspace_id  TEXT,
					event_type    TEXT NOT NULL,
					ip            TEXT NOT NULL DEFAULT '',
					user_agent    TEXT NOT NULL DEFAULT '',
					metadata_json TEXT NOT NULL DEFAULT '{}',
					created_at    INTEGER NOT NULL DEFAULT (unixepoch()),
					FOREIGN KEY (user_id) REFERENCES users(id) ON DELETE SET NULL,
					FOREIGN KEY (workspace_id) REFERENCES workspaces(id) ON DELETE SET NULL
				)`,
				`CREATE INDEX IF NOT EXISTS idx_auth_audit_events_user_created ON auth_audit_events(user_id, created_at DESC)`,
				`CREATE INDEX IF NOT EXISTS idx_auth_audit_events_type_created ON auth_audit_events(event_type, created_at DESC)`,
			}
			for _, stmt := range stmts {
				if _, err := tx.Exec(stmt); err != nil {
					return err
				}
			}
			return nil
		},
	},
	{
		version: 11,
		name:    "b2b_workspaces_contacts_and_transaction_status",
		up: func(tx *sql.Tx) error {
			if err := addColumnIfMissing(tx, "workspaces", "type", "TEXT NOT NULL DEFAULT 'personal' CHECK (type IN ('personal', 'business'))"); err != nil {
				return err
			}
			if err := addColumnIfMissing(tx, "workspaces", "company_name", "TEXT"); err != nil {
				return err
			}
			if err := addColumnIfMissing(tx, "workspaces", "cnpj_cpf", "TEXT"); err != nil {
				return err
			}
			if err := addColumnIfMissing(tx, "workspaces", "address", "TEXT"); err != nil {
				return err
			}
			if err := addColumnIfMissing(tx, "workspaces", "phone", "TEXT"); err != nil {
				return err
			}
			if _, err := tx.Exec(`CREATE TABLE IF NOT EXISTS contacts (
				id           TEXT PRIMARY KEY,
				workspace_id TEXT NOT NULL,
				name         TEXT NOT NULL,
				document     TEXT NOT NULL DEFAULT '',
				type         TEXT NOT NULL CHECK (type IN ('client', 'vendor')),
				email        TEXT NOT NULL DEFAULT '',
				phone        TEXT NOT NULL DEFAULT '',
				created_at   INTEGER NOT NULL DEFAULT (unixepoch()),
				FOREIGN KEY (workspace_id) REFERENCES workspaces(id) ON DELETE CASCADE
			)`); err != nil {
				return err
			}
			if err := addColumnIfMissing(tx, "transactions", "status", "TEXT NOT NULL DEFAULT 'paid' CHECK (status IN ('paid', 'pending'))"); err != nil {
				return err
			}
			if err := addColumnIfMissing(tx, "transactions", "due_date", "INTEGER"); err != nil {
				return err
			}
			if err := addColumnIfMissing(tx, "transactions", "contact_id", "TEXT REFERENCES contacts(id) ON DELETE RESTRICT"); err != nil {
				return err
			}
			if _, err := tx.Exec(`UPDATE transactions
				SET status = CASE
					WHEN lower(COALESCE(payment_status, '')) = 'pending' THEN 'pending'
					ELSE 'paid'
				END
			`); err != nil {
				return err
			}
			stmts := []string{
				`DROP INDEX IF EXISTS idx_transactions_workspace_payment_status_date`,
				`DROP INDEX IF EXISTS idx_contacts_workspace_type_name`,
				`DROP INDEX IF EXISTS idx_transactions_workspace_status_due`,
				`CREATE INDEX IF NOT EXISTS idx_contacts_workspace_name_created ON contacts(workspace_id, name COLLATE NOCASE, created_at DESC)`,
				`CREATE INDEX IF NOT EXISTS idx_transactions_workspace_status_type_due_created ON transactions(workspace_id, status, type, due_date, created_at)`,
				`CREATE INDEX IF NOT EXISTS idx_transactions_workspace_status_date ON transactions(workspace_id, status, date)`,
				`CREATE INDEX IF NOT EXISTS idx_transactions_workspace_contact ON transactions(workspace_id, contact_id)`,
			}
			for _, stmt := range stmts {
				if _, err := tx.Exec(stmt); err != nil {
					return err
				}
			}
			return nil
		},
	},
	{
		version: 12,
		name:    "categories_macro_group_free_text",
		up: func(tx *sql.Tx) error {
			if _, err := tx.Exec(`PRAGMA defer_foreign_keys = ON`); err != nil {
				return err
			}
			if _, err := tx.Exec(`CREATE TABLE IF NOT EXISTS categories_new (
				id           TEXT PRIMARY KEY,
				workspace_id TEXT NOT NULL,
				name         TEXT NOT NULL,
				icon         TEXT NOT NULL DEFAULT 'tag',
				color        TEXT NOT NULL DEFAULT '#6b7280',
				type         TEXT NOT NULL CHECK (type IN ('EXPENSE', 'INCOME')),
				macro_group  TEXT,
				parent_id    TEXT,
				is_fixed     INTEGER NOT NULL DEFAULT 0 CHECK (is_fixed IN (0, 1)),
				created_at   INTEGER NOT NULL DEFAULT (unixepoch()),
				FOREIGN KEY (workspace_id) REFERENCES workspaces(id) ON DELETE CASCADE,
				FOREIGN KEY (parent_id) REFERENCES categories_new(id) ON DELETE CASCADE
			)`); err != nil {
				return err
			}
			if _, err := tx.Exec(`INSERT INTO categories_new (id, workspace_id, name, icon, color, type, macro_group, parent_id, is_fixed, created_at)
				SELECT id, workspace_id, name, icon, color, type, macro_group, parent_id, COALESCE(is_fixed, 0), created_at
				FROM categories`); err != nil {
				return err
			}
			if _, err := tx.Exec(`DROP TABLE categories`); err != nil {
				return err
			}
			if _, err := tx.Exec(`ALTER TABLE categories_new RENAME TO categories`); err != nil {
				return err
			}
			stmts := []string{
				`CREATE INDEX IF NOT EXISTS idx_categories_workspace ON categories(workspace_id)`,
				`CREATE INDEX IF NOT EXISTS idx_categories_parent ON categories(parent_id)`,
			}
			for _, stmt := range stmts {
				if _, err := tx.Exec(stmt); err != nil {
					return err
				}
			}
			return nil
		},
	},
	{
		version: 13,
		name:    "contacts_custom_client_id_unique",
		up: func(tx *sql.Tx) error {
			if err := addColumnIfMissing(tx, "contacts", "custom_client_id", "TEXT"); err != nil {
				return err
			}
			if _, err := tx.Exec(`UPDATE contacts SET custom_client_id = NULL WHERE TRIM(COALESCE(custom_client_id, '')) = ''`); err != nil {
				return err
			}
			if _, err := tx.Exec(`CREATE UNIQUE INDEX IF NOT EXISTS idx_contacts_workspace_custom_client_id_unique ON contacts(workspace_id, custom_client_id)`); err != nil {
				return err
			}
			return nil
		},
	},
	{
		version: 14,
		name:    "accounts_provider_slug_and_color",
		up: func(tx *sql.Tx) error {
			if err := addColumnIfMissing(tx, "accounts", "color", "TEXT NOT NULL DEFAULT '#6B7280'"); err != nil {
				return err
			}
			if err := addColumnIfMissing(tx, "accounts", "provider_slug", "TEXT NOT NULL DEFAULT 'custom'"); err != nil {
				return err
			}
			if _, err := tx.Exec(`UPDATE accounts SET provider_slug = 'custom' WHERE TRIM(COALESCE(provider_slug, '')) = ''`); err != nil {
				return err
			}
			if _, err := tx.Exec(`UPDATE accounts SET color = '#6B7280' WHERE TRIM(COALESCE(color, '')) = ''`); err != nil {
				return err
			}
			return nil
		},
	},
	{
		version: 15,
		name:    "workspaces_theme_token",
		up: func(tx *sql.Tx) error {
			if err := addColumnIfMissing(tx, "accounts", "color", "TEXT NOT NULL DEFAULT '#6B7280'"); err != nil {
				return err
			}
			if err := addColumnIfMissing(tx, "accounts", "provider_slug", "TEXT NOT NULL DEFAULT 'custom'"); err != nil {
				return err
			}
			if err := addColumnIfMissing(tx, "workspaces", "theme_token", "TEXT NOT NULL DEFAULT 'violeta'"); err != nil {
				return err
			}
			if _, err := tx.Exec(`UPDATE workspaces
				SET theme_token = CASE
					WHEN COALESCE(type, 'personal') = 'business' THEN 'laranja'
					ELSE 'violeta'
				END
				WHERE TRIM(COALESCE(theme_token, '')) = ''`); err != nil {
				return err
			}
			return nil
		},
	},
	{
		version: 16,
		name:    "workspace_members_custom_permissions",
		up: func(tx *sql.Tx) error {
			if err := addColumnIfMissing(tx, "workspace_members", "custom_permissions", "TEXT NOT NULL DEFAULT '[]'"); err != nil {
				return err
			}
			if _, err := tx.Exec(`UPDATE workspace_members SET custom_permissions = '[]' WHERE TRIM(COALESCE(custom_permissions, '')) = ''`); err != nil {
				return err
			}
			return nil
		},
	},
	{
		version: 17,
		name:    "workspaces_logo_columns_and_security_logs",
		up: func(tx *sql.Tx) error {
			if err := addColumnIfMissing(tx, "workspaces", "logo_light_url", "TEXT"); err != nil {
				return err
			}
			if err := addColumnIfMissing(tx, "workspaces", "logo_dark_url", "TEXT"); err != nil {
				return err
			}
			stmts := []string{
				`CREATE TABLE IF NOT EXISTS security_logs (
					id           TEXT PRIMARY KEY,
					workspace_id TEXT,
					user_id      TEXT,
					event_type   TEXT NOT NULL,
					severity     TEXT NOT NULL,
					ip_address   TEXT NOT NULL DEFAULT '',
					user_agent   TEXT NOT NULL DEFAULT '',
					metadata     TEXT NOT NULL DEFAULT '{}',
					created_at   INTEGER NOT NULL DEFAULT (unixepoch()),
					FOREIGN KEY (workspace_id) REFERENCES workspaces(id) ON DELETE SET NULL,
					FOREIGN KEY (user_id) REFERENCES users(id) ON DELETE SET NULL
				)`,
				`CREATE INDEX IF NOT EXISTS idx_security_logs_workspace_created ON security_logs(workspace_id, created_at DESC)`,
				`CREATE INDEX IF NOT EXISTS idx_security_logs_event_created ON security_logs(event_type, created_at DESC)`,
			}
			for _, stmt := range stmts {
				if _, err := tx.Exec(stmt); err != nil {
					return err
				}
			}
			return nil
		},
	},
	{
		version: 18,
		name:    "workspaces_smtp_and_alert_preferences",
		up: func(tx *sql.Tx) error {
			if err := addColumnIfMissing(tx, "workspaces", "smtp_host", "TEXT"); err != nil {
				return err
			}
			if err := addColumnIfMissing(tx, "workspaces", "smtp_port", "INTEGER NOT NULL DEFAULT 587"); err != nil {
				return err
			}
			if err := addColumnIfMissing(tx, "workspaces", "smtp_user", "TEXT"); err != nil {
				return err
			}
			if err := addColumnIfMissing(tx, "workspaces", "smtp_pass", "TEXT"); err != nil {
				return err
			}
			if err := addColumnIfMissing(tx, "workspaces", "notification_email", "TEXT"); err != nil {
				return err
			}
			if err := addColumnIfMissing(tx, "workspaces", "email_preferences", "TEXT NOT NULL DEFAULT '[]'"); err != nil {
				return err
			}
			if _, err := tx.Exec(`UPDATE workspaces SET email_preferences = '[]' WHERE TRIM(COALESCE(email_preferences, '')) = ''`); err != nil {
				return err
			}
			return nil
		},
	},
	{
		version: 19,
		name:    "user_notifications_for_security_alerts",
		up: func(tx *sql.Tx) error {
			stmts := []string{
				`CREATE TABLE IF NOT EXISTS user_notifications (
					id         TEXT PRIMARY KEY,
					user_id    TEXT NOT NULL,
					title      TEXT NOT NULL DEFAULT '',
					message    TEXT NOT NULL DEFAULT '',
					type       TEXT NOT NULL DEFAULT '',
					is_read    INTEGER NOT NULL DEFAULT 0 CHECK (is_read IN (0, 1)),
					created_at INTEGER NOT NULL DEFAULT (unixepoch()),
					FOREIGN KEY (user_id) REFERENCES users(id) ON DELETE CASCADE
				)`,
				`CREATE INDEX IF NOT EXISTS idx_user_notifications_user_read ON user_notifications(user_id, is_read)`,
			}
			for _, stmt := range stmts {
				if _, err := tx.Exec(stmt); err != nil {
					return err
				}
			}
			return nil
		},
	},
	{
		version: 20,
		name:    "transaction_engine_hardening_indexes",
		up: func(tx *sql.Tx) error {
			stmts := []string{
				`CREATE INDEX IF NOT EXISTS idx_transactions_workspace_parent ON transactions(workspace_id, parent_id)`,
				`CREATE INDEX IF NOT EXISTS idx_transactions_workspace_recurring_date ON transactions(workspace_id, recurring_rule_id, date)`,
				`CREATE INDEX IF NOT EXISTS idx_transactions_workspace_parent_date ON transactions(workspace_id, parent_id, date)`,
			}
			for _, stmt := range stmts {
				if _, err := tx.Exec(stmt); err != nil {
					return err
				}
			}
			return nil
		},
	},
	{
		version: 21,
		name:    "recurring_rules_total_occurrences",
		up: func(tx *sql.Tx) error {
			return addColumnIfMissing(tx, "recurring_rules", "total_occurrences", "INTEGER")
		},
	},
	{
		version: 22,
		name:    "invoices_reference_by_due_date",
		up: func(tx *sql.Tx) error {
			rows, err := tx.Query(`
				SELECT i.id, i.account_id, i.reference, i.closing_date, i.due_date,
				       cc.closing_day, cc.due_day
				FROM invoices i
				JOIN credit_cards cc ON cc.account_id = i.account_id
			`)
			if err != nil {
				return err
			}
			type invoiceCandidate struct {
				id        string
				accountID string
				oldRef    string
				dueDate   int64
			}
			var candidates []invoiceCandidate

			for rows.Next() {
				var item invoiceCandidate
				var closingDate, closingDay, dueDay int64
				if err := rows.Scan(&item.id, &item.accountID, &item.oldRef, &closingDate, &item.dueDate, &closingDay, &dueDay); err != nil {
					rows.Close()
					return err
				}
				candidates = append(candidates, item)
			}
			if err := rows.Err(); err != nil {
				rows.Close()
				return err
			}
			if err := rows.Close(); err != nil {
				return err
			}

			type invoiceUpdate struct {
				id     string
				newRef string
			}
			var updates []invoiceUpdate

			for _, item := range candidates {
				dueTime := unixToDate(item.dueDate)
				newRef := fmt.Sprintf("%04d-%02d", dueTime.Year(), int(dueTime.Month()))
				if newRef == item.oldRef {
					continue
				}

				var existing int
				if err := tx.QueryRow(`SELECT COUNT(1) FROM invoices WHERE account_id = ? AND reference = ? AND id != ?`,
					item.accountID, newRef, item.id).Scan(&existing); err != nil {
					return err
				}
				if existing > 0 {
					continue
				}

				updates = append(updates, invoiceUpdate{id: item.id, newRef: newRef})
			}

			for _, u := range updates {
				if _, err := tx.Exec(`UPDATE invoices SET reference = ? WHERE id = ?`, u.newRef, u.id); err != nil {
					return err
				}
			}
			return nil
		},
	},
	{
		version: 23,
		name:    "credit_card_transactions_invoice_backfill",
		up: func(tx *sql.Tx) error {
			rows, err := tx.Query(`
				SELECT t.id, t.workspace_id, t.account_id, t.date, cc.closing_day, cc.due_day
				FROM transactions t
				JOIN accounts a ON a.id = t.account_id AND a.workspace_id = t.workspace_id
				JOIN credit_cards cc ON cc.account_id = a.id
				WHERE t.type = 'EXPENSE'
				  AND a.type = 'CREDIT_CARD'
				  AND t.invoice_id IS NULL
			`)
			if err != nil {
				return err
			}

			type cardTransaction struct {
				id          string
				workspaceID string
				accountID   string
				dateUnix    int64
				closingDay  int64
				dueDay      int64
			}
			var items []cardTransaction
			for rows.Next() {
				var item cardTransaction
				if err := rows.Scan(&item.id, &item.workspaceID, &item.accountID, &item.dateUnix, &item.closingDay, &item.dueDay); err != nil {
					rows.Close()
					return err
				}
				items = append(items, item)
			}
			if err := rows.Close(); err != nil {
				return err
			}

			now := time.Now().Unix()
			for _, item := range items {
				closingUnix, dueUnix, reference := calculateInvoiceDates(time.Unix(item.dateUnix, 0).UTC(), item.closingDay, item.dueDay)
				var invoiceID string
				err := tx.QueryRow(`
					SELECT i.id
					FROM invoices i
					JOIN accounts a ON a.id = i.account_id
					WHERE i.account_id = ? AND i.reference = ? AND a.workspace_id = ?
				`, item.accountID, reference, item.workspaceID).Scan(&invoiceID)
				if err == sql.ErrNoRows {
					invoiceID = uuid.NewString()
					status := "OPEN"
					if closingUnix < now {
						status = "CLOSED"
					}
					if _, err := tx.Exec(`
						INSERT INTO invoices (id, account_id, reference, closing_date, due_date, status, created_at)
						VALUES (?, ?, ?, ?, ?, ?, ?)
					`, invoiceID, item.accountID, reference, closingUnix, dueUnix, status, now); err != nil {
						return err
					}
				} else if err != nil {
					return err
				}

				if _, err := tx.Exec(`
					UPDATE transactions
					SET invoice_id = ?, status = 'paid', updated_at = ?
					WHERE id = ? AND workspace_id = ? AND invoice_id IS NULL
				`, invoiceID, now, item.id, item.workspaceID); err != nil {
					return err
				}
			}
			return nil
		},
	},
	{
		version: 24,
		name:    "transactions_recurring_unique_duplicate_prevention",
		up: func(tx *sql.Tx) error {
			_, err := tx.Exec(`CREATE UNIQUE INDEX IF NOT EXISTS idx_transactions_recurring_rule_date ON transactions(recurring_rule_id, date)`)
			return err
		},
	},
	{
		version: 25,
		name:    "transactions_recurring_drop_unique_index",
		up: func(tx *sql.Tx) error {
			_, err := tx.Exec(`DROP INDEX IF EXISTS idx_transactions_recurring_rule_date`)
			return err
		},
	},
	{
		version: 26,
		name:    "cost_limits_alert_threshold",
		up: func(tx *sql.Tx) error {
			return addColumnIfMissing(tx, "cost_limits", "alert_threshold", "INTEGER NOT NULL DEFAULT 0")
		},
	},
	{
		version: 27,
		name:    "cost_limits_timestamps",
		up: func(tx *sql.Tx) error {
			created, err := columnExists(tx, "cost_limits", "created_at")
			if err != nil {
				return err
			}
			updated, err := columnExists(tx, "cost_limits", "updated_at")
			if err != nil {
				return err
			}
			if created && updated {
				return nil
			}
			var count int
			if err := tx.QueryRow(`SELECT count(*) FROM cost_limits`).Scan(&count); err != nil {
				return err
			}
			if count == 0 {
				// Preserve the original path for fresh/empty installations.
				if err := addColumnIfMissing(tx, "cost_limits", "created_at", "INTEGER NOT NULL DEFAULT (unixepoch())"); err != nil {
					return err
				}
				return addColumnIfMissing(tx, "cost_limits", "updated_at", "INTEGER NOT NULL DEFAULT (unixepoch())")
			}
			// SQLite rejects ADD COLUMN with a nonconstant default on populated
			// tables. Rebuild only pending v27, retaining its original defaults.
			// No historical schema references cost_limits; refuse unknown incoming
			// FKs rather than risking delete actions in a customized installation.
			var dependents, scratch, columns int
			if err := tx.QueryRow(`SELECT count(*) FROM sqlite_schema s, pragma_foreign_key_list(s.name) f WHERE s.type='table' AND lower(f."table")='cost_limits'`).Scan(&dependents); err != nil {
				return err
			}
			if dependents != 0 {
				return fmt.Errorf("migration 27: unexpected cost_limits dependents; preserve for inspection")
			}
			if err := tx.QueryRow(`SELECT count(*) FROM sqlite_schema WHERE name='cost_limits_new'`).Scan(&scratch); err != nil {
				return err
			}
			if scratch != 0 {
				return fmt.Errorf("migration 27: staging name already exists")
			}
			if err := tx.QueryRow(`SELECT count(*) FROM pragma_table_info('cost_limits')`).Scan(&columns); err != nil {
				return err
			}
			expected := 5
			if created {
				expected++
			}
			if updated {
				expected++
			}
			if columns != expected {
				return fmt.Errorf("migration 27: unexpected cost_limits columns; preserve for inspection")
			}
			projection := `id,workspace_id,category_id,max_amount_monthly,alert_threshold`
			createdExpr, updatedExpr := "unixepoch()", "unixepoch()"
			if created {
				projection += ",created_at"
				createdExpr = "created_at"
			}
			if updated {
				projection += ",updated_at"
				updatedExpr = "updated_at"
			}
			query := "SELECT " + projection + " FROM cost_limits ORDER BY id"
			before, err := migration12Digest(tx, query)
			if err != nil {
				return err
			}
			objects, err := tx.Query(`SELECT sql FROM sqlite_schema WHERE tbl_name='cost_limits' AND type IN ('index','trigger') AND sql IS NOT NULL ORDER BY type,name`)
			if err != nil {
				return err
			}
			var definitions []string
			for objects.Next() {
				var definition string
				if err := objects.Scan(&definition); err != nil {
					objects.Close()
					return err
				}
				definitions = append(definitions, definition)
			}
			if err := objects.Err(); err != nil {
				objects.Close()
				return err
			}
			if err := objects.Close(); err != nil {
				return err
			}
			if _, err := tx.Exec(`CREATE TABLE cost_limits_new (
				id TEXT PRIMARY KEY,
				workspace_id TEXT NOT NULL,
				category_id TEXT NOT NULL,
				max_amount_monthly INTEGER NOT NULL CHECK (max_amount_monthly > 0),
				alert_threshold INTEGER NOT NULL DEFAULT 0,
				created_at INTEGER NOT NULL DEFAULT (unixepoch()),
				updated_at INTEGER NOT NULL DEFAULT (unixepoch()),
				FOREIGN KEY (workspace_id) REFERENCES workspaces(id) ON DELETE CASCADE,
				FOREIGN KEY (category_id) REFERENCES categories(id) ON DELETE CASCADE,
				UNIQUE (workspace_id, category_id)
			)`); err != nil {
				return err
			}
			if _, err := tx.Exec(`INSERT INTO cost_limits_new SELECT id,workspace_id,category_id,max_amount_monthly,alert_threshold,` + createdExpr + "," + updatedExpr + ` FROM cost_limits`); err != nil {
				return err
			}
			if _, err := tx.Exec(`DROP TABLE cost_limits`); err != nil {
				return err
			}
			if _, err := tx.Exec(`ALTER TABLE cost_limits_new RENAME TO cost_limits`); err != nil {
				return err
			}
			for _, definition := range definitions {
				if _, err := tx.Exec(definition); err != nil {
					return err
				}
			}
			after, err := migration12Digest(tx, query)
			if err != nil {
				return err
			}
			if before != after {
				return fmt.Errorf("migration 27 changed historical cost limits")
			}
			rows, err := tx.Query(`PRAGMA foreign_key_check(cost_limits)`)
			if err != nil {
				return err
			}
			defer rows.Close()
			if rows.Next() {
				return fmt.Errorf("migration 27: cost_limits foreign key validation failed")
			}
			return rows.Err()
		},
	},
	{
		version: 28,
		name:    "workspace_members_custom_permissions_registry_guard",
		up: func(tx *sql.Tx) error {
			if err := addColumnIfMissing(tx, "workspace_members", "custom_permissions", "TEXT NOT NULL DEFAULT '[]'"); err != nil {
				return err
			}
			_, err := tx.Exec(`UPDATE workspace_members SET custom_permissions = '[]' WHERE TRIM(COALESCE(custom_permissions, '')) = ''`)
			return err
		},
	},
	{
		version: 29,
		name:    "boxes_target_date",
		up: func(tx *sql.Tx) error {
			exists, err := tableExists(tx, "boxes")
			if err != nil {
				return err
			}
			if !exists {
				return nil
			}
			return addColumnIfMissing(tx, "boxes", "target_date", "INTEGER")
		},
	},
	{
		version: 30,
		name:    "box_virtual_ledger_release_events",
		up: func(tx *sql.Tx) error {
			exists, err := tableExists(tx, "box_virtual_ledger")
			if err != nil {
				return err
			}
			if !exists {
				return nil
			}

			if _, err := tx.Exec(`PRAGMA defer_foreign_keys = ON`); err != nil {
				return err
			}
			if _, err := tx.Exec(`CREATE TABLE IF NOT EXISTS box_virtual_ledger_new (
				id             TEXT PRIMARY KEY,
				box_id         TEXT    NOT NULL,
				amount         INTEGER NOT NULL CHECK (amount != 0),
				type           TEXT    NOT NULL CHECK (type IN ('RECHARGE', 'BONUS', 'RELEASE')),
				description    TEXT    NOT NULL DEFAULT '',
				reference_date INTEGER NOT NULL,
				created_at     INTEGER NOT NULL DEFAULT (unixepoch()),
				FOREIGN KEY (box_id) REFERENCES boxes(id) ON DELETE CASCADE
			)`); err != nil {
				return err
			}
			if _, err := tx.Exec(`INSERT INTO box_virtual_ledger_new (id, box_id, amount, type, description, reference_date, created_at)
				SELECT id, box_id, amount, type, description, reference_date, created_at
				FROM box_virtual_ledger`); err != nil {
				return err
			}
			if _, err := tx.Exec(`DROP TABLE box_virtual_ledger`); err != nil {
				return err
			}
			if _, err := tx.Exec(`ALTER TABLE box_virtual_ledger_new RENAME TO box_virtual_ledger`); err != nil {
				return err
			}

			stmts := []string{
				`CREATE INDEX IF NOT EXISTS idx_box_virtual_ledger_box ON box_virtual_ledger(box_id)`,
				`CREATE INDEX IF NOT EXISTS idx_box_virtual_ledger_box_reference_date ON box_virtual_ledger(box_id, reference_date)`,
			}
			for _, stmt := range stmts {
				if _, err := tx.Exec(stmt); err != nil {
					return err
				}
			}
			return nil
		},
	},
	{
		version: 31,
		name:    "box_virtual_ledger_consume_reversal_audit_fields",
		up: func(tx *sql.Tx) error {
			exists, err := tableExists(tx, "box_virtual_ledger")
			if err != nil {
				return err
			}
			if !exists {
				return nil
			}

			if _, err := tx.Exec(`PRAGMA defer_foreign_keys = ON`); err != nil {
				return err
			}
			if _, err := tx.Exec(`CREATE TABLE IF NOT EXISTS box_virtual_ledger_new (
				id                    TEXT PRIMARY KEY,
				box_id                TEXT    NOT NULL,
				amount                INTEGER NOT NULL CHECK (
					(type IN ('RECHARGE', 'BONUS', 'REVERSAL') AND amount > 0) OR
					(type IN ('RELEASE', 'CONSUME') AND amount < 0)
				),
				type                  TEXT    NOT NULL CHECK (type IN ('RECHARGE', 'BONUS', 'RELEASE', 'CONSUME', 'REVERSAL')),
				description           TEXT    NOT NULL DEFAULT '',
				source_transaction_id TEXT,
				reversal_of_ledger_id TEXT,
				reference_date        INTEGER NOT NULL,
				created_at            INTEGER NOT NULL DEFAULT (unixepoch()),
				FOREIGN KEY (box_id) REFERENCES boxes(id) ON DELETE CASCADE,
				FOREIGN KEY (reversal_of_ledger_id) REFERENCES box_virtual_ledger_new(id) ON DELETE SET NULL
			)`); err != nil {
				return err
			}
			if _, err := tx.Exec(`INSERT INTO box_virtual_ledger_new (
					id,
					box_id,
					amount,
					type,
					description,
					source_transaction_id,
					reversal_of_ledger_id,
					reference_date,
					created_at
				)
				SELECT
					id,
					box_id,
					CASE
						WHEN type = 'RELEASE' THEN -ABS(amount)
						ELSE ABS(amount)
					END AS amount,
					type,
					description,
					NULL AS source_transaction_id,
					NULL AS reversal_of_ledger_id,
					reference_date,
					created_at
				FROM box_virtual_ledger`); err != nil {
				return err
			}
			if _, err := tx.Exec(`DROP TABLE box_virtual_ledger`); err != nil {
				return err
			}
			if _, err := tx.Exec(`ALTER TABLE box_virtual_ledger_new RENAME TO box_virtual_ledger`); err != nil {
				return err
			}

			stmts := []string{
				`CREATE INDEX IF NOT EXISTS idx_box_virtual_ledger_box ON box_virtual_ledger(box_id)`,
				`CREATE INDEX IF NOT EXISTS idx_box_virtual_ledger_box_reference_date ON box_virtual_ledger(box_id, reference_date)`,
				`CREATE INDEX IF NOT EXISTS idx_box_virtual_ledger_source_transaction ON box_virtual_ledger(source_transaction_id)`,
				`CREATE INDEX IF NOT EXISTS idx_box_virtual_ledger_reversal_of ON box_virtual_ledger(reversal_of_ledger_id)`,
			}
			for _, stmt := range stmts {
				if _, err := tx.Exec(stmt); err != nil {
					return err
				}
			}
			return nil
		},
	},
	{
		version: 32,
		name:    "boxes_monthly_yield_rate",
		up: func(tx *sql.Tx) error {
			exists, err := tableExists(tx, "boxes")
			if err != nil {
				return err
			}
			if !exists {
				return nil
			}
			return addColumnIfMissing(tx, "boxes", "monthly_yield_rate", "REAL NOT NULL DEFAULT 0.0")
		},
	},
	{
		version: 33,
		name:    "sessions_remember_me_flags",
		up: func(tx *sql.Tx) error {
			exists, err := tableExists(tx, "sessions")
			if err != nil {
				return err
			}
			if !exists {
				return nil
			}
			if err := addColumnIfMissing(tx, "sessions", "is_remember", "INTEGER NOT NULL DEFAULT 0"); err != nil {
				return err
			}
			return addColumnIfMissing(tx, "pre_auth_sessions", "remember_me", "INTEGER NOT NULL DEFAULT 0")
		},
	},
	{
		version: 34,
		name:    "user_notifications_workspace_isolation",
		up: func(tx *sql.Tx) error {
			exists, err := tableExists(tx, "user_notifications")
			if err != nil {
				return err
			}
			if !exists {
				return nil
			}
			if err := addColumnIfMissing(tx, "user_notifications", "workspace_id", "TEXT"); err != nil {
				return err
			}
			rows, err := tx.Query(`SELECT id, user_id FROM user_notifications WHERE workspace_id IS NULL`)
			if err != nil {
				return nil
			}
			defer rows.Close()
			type notifRow struct {
				ID     string
				UserID string
			}
			var toUpdate []notifRow
			for rows.Next() {
				var r notifRow
				if err := rows.Scan(&r.ID, &r.UserID); err != nil {
					continue
				}
				toUpdate = append(toUpdate, r)
			}
			if err := rows.Err(); err != nil {
				return err
			}
			for _, r := range toUpdate {
				var wsID string
				err := tx.QueryRow(`SELECT COALESCE(default_workspace_id, '') FROM users WHERE id = ?`, r.UserID).Scan(&wsID)
				if err != nil || wsID == "" {
					err2 := tx.QueryRow(`SELECT wm.workspace_id FROM workspace_members wm WHERE wm.user_id = ? ORDER BY wm.joined_at ASC LIMIT 1`, r.UserID).Scan(&wsID)
					if err2 != nil || wsID == "" {
						continue
					}
				}
				if _, err := tx.Exec(`UPDATE user_notifications SET workspace_id = ? WHERE id = ?`, wsID, r.ID); err != nil {
					return err
				}
			}
			_, err = tx.Exec(`CREATE INDEX IF NOT EXISTS idx_user_notifications_user_workspace_created ON user_notifications(user_id, workspace_id, created_at)`)
			return err
		},
	},
	{
		version: 35,
		name:    "accounts_icon_and_wallet_type",
		up: func(tx *sql.Tx) error {
			exists, err := tableExists(tx, "accounts")
			if err != nil {
				return err
			}
			if !exists {
				return nil
			}
			if err := addColumnIfMissing(tx, "accounts", "icon", "TEXT NOT NULL DEFAULT ''"); err != nil {
				return fmt.Errorf("migration 35 add accounts.icon: %w", err)
			}
			if _, err := tx.Exec(`UPDATE accounts SET icon = '' WHERE TRIM(COALESCE(icon, '')) = ''`); err != nil {
				return fmt.Errorf("migration 35 normalize blank account icons: %w", err)
			}
			if err := prepareAccountsMigration35Compatibility(tx); err != nil {
				return fmt.Errorf("migration 35 prepare account workspace compatibility: %w", err)
			}
			if _, err := tx.Exec(`DROP TABLE IF EXISTS accounts_mig35`); err != nil {
				return fmt.Errorf("migration 35 clear stale rebuilt accounts table: %w", err)
			}
			if _, err := tx.Exec(`
					CREATE TABLE IF NOT EXISTS accounts_mig35 (
						id              TEXT PRIMARY KEY,
						workspace_id    TEXT NOT NULL,
						name            TEXT NOT NULL,
					type            TEXT NOT NULL CHECK (type IN ('CHECKING', 'SAVINGS', 'INVESTMENT', 'WALLET', 'CREDIT_CARD')),
					color           TEXT NOT NULL DEFAULT '#6B7280',
					icon            TEXT NOT NULL DEFAULT '',
					provider_slug   TEXT NOT NULL DEFAULT 'custom',
					initial_balance INTEGER NOT NULL DEFAULT 0,
						current_balance INTEGER NOT NULL DEFAULT 0,
						created_at      INTEGER NOT NULL DEFAULT (unixepoch()),
						updated_at      INTEGER NOT NULL DEFAULT (unixepoch()),
						FOREIGN KEY (workspace_id) REFERENCES workspaces(id) ON DELETE CASCADE
					)
				`); err != nil {
				return fmt.Errorf("migration 35 create rebuilt accounts table: %w", err)
			}
			if _, err := tx.Exec(`
					INSERT INTO accounts_mig35 (id, workspace_id, name, type, color, icon, provider_slug, initial_balance, current_balance, created_at, updated_at)
					SELECT
						id,
						workspace_id,
						COALESCE(NULLIF(TRIM(name), ''), 'Conta restaurada'),
						CASE
							WHEN type IN ('CHECKING', 'SAVINGS', 'INVESTMENT', 'WALLET', 'CREDIT_CARD') THEN type
							ELSE 'CHECKING'
						END,
						CASE
							WHEN TRIM(COALESCE(color, '')) <> '' AND upper(TRIM(color)) <> '#6B7280' THEN TRIM(color)
							WHEN lower(COALESCE(provider_slug, '')) = 'nubank' OR lower(name) LIKE '%nubank%' THEN '#820AD1'
							WHEN lower(COALESCE(provider_slug, '')) = 'itau' OR lower(name) LIKE '%itaú%' OR lower(name) LIKE '%itau%' THEN '#EC7000'
							WHEN lower(COALESCE(provider_slug, '')) = 'inter' OR lower(name) LIKE '%inter%' THEN '#FF6B00'
							WHEN lower(COALESCE(provider_slug, '')) = 'bb' OR lower(name) LIKE '%banco do brasil%' THEN '#0056A4'
							WHEN lower(COALESCE(provider_slug, '')) = 'caixa' OR lower(name) LIKE '%caixa%' THEN '#005CA9'
							WHEN lower(COALESCE(provider_slug, '')) = 'picpay' OR lower(name) LIKE '%picpay%' THEN '#11C76F'
							WHEN lower(COALESCE(provider_slug, '')) = 'mercadopago' OR lower(name) LIKE '%mercado pago%' THEN '#00A6FF'
							WHEN lower(COALESCE(provider_slug, '')) = 'bradesco' OR lower(name) LIKE '%bradesco%' THEN '#CC092F'
							WHEN lower(COALESCE(provider_slug, '')) = 'santander' OR lower(name) LIKE '%santander%' THEN '#EC0000'
							WHEN lower(COALESCE(provider_slug, '')) = 'c6' OR lower(name) LIKE '%c6%' THEN '#111111'
							WHEN lower(COALESCE(provider_slug, '')) = 'xp' OR lower(name) LIKE '%xp%' THEN '#000000'
							WHEN lower(COALESCE(provider_slug, '')) = 'pagbank' OR lower(name) LIKE '%pagbank%' OR lower(name) LIKE '%pagseguro%' THEN '#FFE72D'
							ELSE '#6B7280'
						END,
						COALESCE(NULLIF(TRIM(icon), ''), CASE
							WHEN type = 'CREDIT_CARD' THEN 'credit-card'
							WHEN lower(COALESCE(provider_slug, '')) IN ('picpay', 'mercadopago') OR lower(name) LIKE '%picpay%' OR lower(name) LIKE '%mercado pago%' THEN 'wallet-cards'
							WHEN lower(COALESCE(provider_slug, '')) IN ('nubank', 'itau', 'inter', 'bb', 'caixa', 'bradesco', 'santander', 'c6', 'xp', 'pagbank') THEN 'building-2'
							WHEN lower(name) LIKE '%nubank%' OR lower(name) LIKE '%itaú%' OR lower(name) LIKE '%itau%' OR lower(name) LIKE '%inter%' OR lower(name) LIKE '%banco do brasil%' OR lower(name) LIKE '%caixa%' OR lower(name) LIKE '%bradesco%' OR lower(name) LIKE '%santander%' OR lower(name) LIKE '%c6%' OR lower(name) LIKE '%xp%' OR lower(name) LIKE '%pagbank%' OR lower(name) LIKE '%pagseguro%' THEN 'building-2'
							ELSE 'wallet'
						END),
						CASE
							WHEN lower(COALESCE(provider_slug, '')) IN ('nubank', 'itau', 'inter', 'bb', 'caixa', 'picpay', 'mercadopago', 'bradesco', 'santander', 'c6', 'xp', 'pagbank') THEN lower(provider_slug)
							WHEN lower(name) LIKE '%nubank%' THEN 'nubank'
							WHEN lower(name) LIKE '%itaú%' OR lower(name) LIKE '%itau%' THEN 'itau'
							WHEN lower(name) LIKE '%inter%' THEN 'inter'
							WHEN lower(name) LIKE '%banco do brasil%' THEN 'bb'
							WHEN lower(name) LIKE '%caixa%' THEN 'caixa'
							WHEN lower(name) LIKE '%picpay%' THEN 'picpay'
							WHEN lower(name) LIKE '%mercado pago%' THEN 'mercadopago'
							WHEN lower(name) LIKE '%bradesco%' THEN 'bradesco'
							WHEN lower(name) LIKE '%santander%' THEN 'santander'
							WHEN lower(name) LIKE '%c6%' THEN 'c6'
							WHEN lower(name) LIKE '%xp%' THEN 'xp'
							WHEN lower(name) LIKE '%pagbank%' OR lower(name) LIKE '%pagseguro%' THEN 'pagbank'
							ELSE 'custom'
						END,
						COALESCE(initial_balance, 0),
						COALESCE(current_balance, 0),
						COALESCE(created_at, unixepoch()),
						COALESCE(updated_at, unixepoch())
					FROM accounts
				`); err != nil {
				return fmt.Errorf("migration 35 copy normalized accounts into rebuilt table: %w", err)
			}
			if _, err := tx.Exec(`DROP TABLE accounts`); err != nil {
				return fmt.Errorf("migration 35 drop old accounts table after safe copy: %w", err)
			}
			if _, err := tx.Exec(`ALTER TABLE accounts_mig35 RENAME TO accounts`); err != nil {
				return fmt.Errorf("migration 35 rename rebuilt accounts table: %w", err)
			}
			if err := assertNoForeignKeyViolations(tx); err != nil {
				return fmt.Errorf("migration 35 final foreign_key_check: %w", err)
			}
			return nil
		},
	},
	{
		version: 36,
		name:    "accounts_archived_at",
		up: func(tx *sql.Tx) error {
			exists, err := tableExists(tx, "accounts")
			if err != nil {
				return err
			}
			if !exists {
				return nil
			}
			if err := addColumnIfMissing(tx, "accounts", "archived_at", "INTEGER NULL"); err != nil {
				return err
			}
			_, err = tx.Exec(`CREATE INDEX IF NOT EXISTS idx_accounts_workspace_archived ON accounts(workspace_id, archived_at)`)
			return err
		},
	},
	{
		version: 37,
		name:    "auth_lockouts",
		up: func(tx *sql.Tx) error {
			stmts := []string{
				`CREATE TABLE IF NOT EXISTS auth_lockouts (
					user_id               TEXT PRIMARY KEY,
					failed_password_count INTEGER NOT NULL DEFAULT 0 CHECK (failed_password_count >= 0),
					failed_2fa_count      INTEGER NOT NULL DEFAULT 0 CHECK (failed_2fa_count >= 0),
					first_failed_at       INTEGER NOT NULL DEFAULT 0,
					last_failed_at        INTEGER NOT NULL DEFAULT 0,
					locked_until          INTEGER NOT NULL DEFAULT 0,
					lock_reason           TEXT NOT NULL DEFAULT '' CHECK (lock_reason IN ('', 'password', 'totp')),
					updated_at            INTEGER NOT NULL DEFAULT (unixepoch()),
					FOREIGN KEY (user_id) REFERENCES users(id) ON DELETE CASCADE
				)`,
				`CREATE INDEX IF NOT EXISTS idx_auth_lockouts_locked_until ON auth_lockouts(locked_until)`,
			}
			for _, stmt := range stmts {
				if _, err := tx.Exec(stmt); err != nil {
					return err
				}
			}
			return nil
		},
	},
	{
		version: 38,
		name:    "temporary_password_recovery",
		up: func(tx *sql.Tx) error {
			exists, err := tableExists(tx, "users")
			if err != nil {
				return err
			}
			if !exists {
				return nil
			}
			if err := addColumnIfMissing(tx, "users", "must_change_password", "INTEGER NOT NULL DEFAULT 0 CHECK (must_change_password IN (0, 1))"); err != nil {
				return err
			}
			return addColumnIfMissing(tx, "users", "temporary_password_expires_at", "INTEGER")
		},
	},
	{
		version: 39,
		name:    "invoice_payments_schema",
		up: func(tx *sql.Tx) error {
			stmts := []string{
				`CREATE TABLE IF NOT EXISTS invoice_payments (
					id             TEXT PRIMARY KEY,
					workspace_id   TEXT NOT NULL,
					invoice_id     TEXT NOT NULL,
					account_id     TEXT NOT NULL,
					transaction_id TEXT,
					amount_cents   INTEGER NOT NULL CHECK(amount_cents > 0),
					paid_at        INTEGER NOT NULL,
					note           TEXT,
					source         TEXT NOT NULL DEFAULT 'manual',
					reversed_at    INTEGER,
					created_by     TEXT,
					created_at     INTEGER NOT NULL,
					FOREIGN KEY (workspace_id) REFERENCES workspaces(id) ON DELETE CASCADE,
					FOREIGN KEY (invoice_id) REFERENCES invoices(id) ON DELETE CASCADE,
					FOREIGN KEY (account_id) REFERENCES accounts(id) ON DELETE CASCADE
				)`,
				`CREATE INDEX IF NOT EXISTS idx_invoice_payments_workspace_invoice ON invoice_payments(workspace_id, invoice_id)`,
				`CREATE INDEX IF NOT EXISTS idx_invoice_payments_workspace_account ON invoice_payments(workspace_id, account_id)`,
				`CREATE INDEX IF NOT EXISTS idx_invoice_payments_transaction ON invoice_payments(transaction_id)`,
			}
			for _, stmt := range stmts {
				if _, err := tx.Exec(stmt); err != nil {
					return err
				}
			}
			return nil
		},
	},
	{
		version: 40,
		name:    "accounts_sort_order",
		up: func(tx *sql.Tx) error {
			exists, err := tableExists(tx, "accounts")
			if err != nil {
				return err
			}
			if !exists {
				return nil
			}
			if err := addColumnIfMissing(tx, "accounts", "sort_order", "INTEGER NOT NULL DEFAULT 0"); err != nil {
				return fmt.Errorf("migration 40 add accounts.sort_order: %w", err)
			}
			_, err = tx.Exec(`CREATE INDEX IF NOT EXISTS idx_accounts_workspace_sort ON accounts(workspace_id, sort_order)`)
			return err
		},
	},
	{
		version: 41,
		name:    "boxes_nullable_category",
		up: func(tx *sql.Tx) error {
			exists, err := tableExists(tx, "boxes")
			if err != nil {
				return err
			}
			if !exists {
				return nil
			}

			rows, err := tx.Query(`PRAGMA table_info(boxes)`)
			if err != nil {
				return err
			}
			categoryNotNull := false
			columns := make(map[string]bool)
			for rows.Next() {
				var cid int
				var name, columnType string
				var notNull int
				var defaultValue interface{}
				var pk int
				if err := rows.Scan(&cid, &name, &columnType, &notNull, &defaultValue, &pk); err != nil {
					rows.Close()
					return err
				}
				columns[name] = true
				if name == "category_id" {
					categoryNotNull = notNull == 1
				}
			}
			if err := rows.Err(); err != nil {
				rows.Close()
				return err
			}
			if err := rows.Close(); err != nil {
				return err
			}
			if !categoryNotNull {
				return nil
			}

			preservedSchemaSQL, err := tableSchemaSQL(tx, "boxes")
			if err != nil {
				return fmt.Errorf("migration 41 read boxes schema objects: %w", err)
			}

			hasWorkspaces, err := tableExists(tx, "workspaces")
			if err != nil {
				return err
			}
			hasCategories, err := tableExists(tx, "categories")
			if err != nil {
				return err
			}
			foreignKeys := ""
			if hasWorkspaces && hasCategories {
				foreignKeys = `,
						FOREIGN KEY (workspace_id) REFERENCES workspaces(id) ON DELETE CASCADE,
						FOREIGN KEY (category_id)  REFERENCES categories(id) ON DELETE SET NULL`
			}

			if _, err := tx.Exec(`DROP TABLE IF EXISTS boxes_mig41`); err != nil {
				return fmt.Errorf("migration 41 clear stale rebuilt boxes table: %w", err)
			}
			if _, err := tx.Exec(fmt.Sprintf(`
						CREATE TABLE IF NOT EXISTS boxes_mig41 (
						id               TEXT PRIMARY KEY,
						workspace_id     TEXT    NOT NULL,
						name             TEXT    NOT NULL,
						description      TEXT    NOT NULL DEFAULT '',
						category_id      TEXT,
						target_amount    INTEGER NOT NULL DEFAULT 0,
						monthly_recharge INTEGER NOT NULL DEFAULT 0,
						target_date      INTEGER,
						monthly_yield_rate REAL  NOT NULL DEFAULT 0.0,
						created_at       INTEGER NOT NULL DEFAULT (unixepoch()),
						updated_at       INTEGER NOT NULL DEFAULT (unixepoch())%s
					)
				`, foreignKeys)); err != nil {
				return fmt.Errorf("migration 41 create rebuilt boxes table: %w", err)
			}
			descriptionExpr := "''"
			if columns["description"] {
				descriptionExpr = "description"
			}
			targetDateExpr := "NULL"
			if columns["target_date"] {
				targetDateExpr = "target_date"
			}
			monthlyYieldRateExpr := "0.0"
			if columns["monthly_yield_rate"] {
				monthlyYieldRateExpr = "monthly_yield_rate"
			}

			if _, err := tx.Exec(fmt.Sprintf(`
						INSERT INTO boxes_mig41 (
							id, workspace_id, name, description, category_id, target_amount,
							monthly_recharge, target_date, monthly_yield_rate, created_at, updated_at
						)
						SELECT id, workspace_id, name, %s, category_id, target_amount,
							monthly_recharge, %s, %s, created_at, updated_at
						FROM boxes
					`, descriptionExpr, targetDateExpr, monthlyYieldRateExpr)); err != nil {
				return fmt.Errorf("migration 41 copy boxes into rebuilt table: %w", err)
			}
			if _, err := tx.Exec(`DROP TABLE boxes`); err != nil {
				return fmt.Errorf("migration 41 drop old boxes table after safe copy: %w", err)
			}
			if _, err := tx.Exec(`ALTER TABLE boxes_mig41 RENAME TO boxes`); err != nil {
				return fmt.Errorf("migration 41 rename rebuilt boxes table: %w", err)
			}

			for _, stmt := range preservedSchemaSQL {
				if _, err := tx.Exec(stmt); err != nil {
					return fmt.Errorf("migration 41 recreate boxes schema object: %w", err)
				}
			}
			stmts := []string{
				`CREATE INDEX IF NOT EXISTS idx_boxes_workspace ON boxes(workspace_id)`,
				`CREATE INDEX IF NOT EXISTS idx_boxes_workspace_category ON boxes(workspace_id, category_id)`,
			}
			for _, stmt := range stmts {
				if _, err := tx.Exec(stmt); err != nil {
					return err
				}
			}
			return nil
		},
	},
	{
		version: 42,
		name:    "add_users_deleted_columns",
		up: func(tx *sql.Tx) error {
			exists, err := tableExists(tx, "users")
			if err != nil {
				return err
			}
			if !exists {
				return nil
			}
			if err := addColumnIfMissing(tx, "users", "deleted_at", "INTEGER NOT NULL DEFAULT 0"); err != nil {
				return fmt.Errorf("migration 42 add users.deleted_at: %w", err)
			}
			if err := addColumnIfMissing(tx, "users", "deleted_reason", "TEXT"); err != nil {
				return fmt.Errorf("migration 42 add users.deleted_reason: %w", err)
			}
			return nil
		},
	},
	{
		version: 43,
		name:    "add_transactions_paid_at",
		up: func(tx *sql.Tx) error {
			exists, err := tableExists(tx, "transactions")
			if err != nil {
				return err
			}
			if !exists {
				return nil
			}
			if err := addColumnIfMissing(tx, "transactions", "paid_at", "INTEGER"); err != nil {
				return fmt.Errorf("migration 43 add transactions.paid_at: %w", err)
			}
			return nil
		},
	},
	{
		version: 44,
		name:    "add_transaction_status_history",
		up: func(tx *sql.Tx) error {
			if _, err := tx.Exec(`
				CREATE TABLE IF NOT EXISTS transaction_status_history (
					id              TEXT PRIMARY KEY,
					transaction_id  TEXT NOT NULL,
					workspace_id    TEXT NOT NULL,
					user_id         TEXT NOT NULL,
					previous_status TEXT,
					new_status      TEXT,
					previous_paid_at INTEGER,
					new_paid_at     INTEGER,
					created_at      INTEGER NOT NULL
				)
			`); err != nil {
				return fmt.Errorf("migration 44 create transaction_status_history: %w", err)
			}
			if _, err := tx.Exec(`CREATE INDEX IF NOT EXISTS idx_tsh_transaction ON transaction_status_history(transaction_id)`); err != nil {
				return fmt.Errorf("migration 44 create idx_tsh_transaction: %w", err)
			}
			if _, err := tx.Exec(`CREATE INDEX IF NOT EXISTS idx_tsh_workspace_created ON transaction_status_history(workspace_id, created_at)`); err != nil {
				return fmt.Errorf("migration 44 create idx_tsh_workspace_created: %w", err)
			}
			return nil
		},
	},
	{
		version: 45,
		name:    "credit_cards_default_payment_account",
		up: func(tx *sql.Tx) error {
			exists, err := tableExists(tx, "credit_cards")
			if err != nil {
				return err
			}
			if !exists {
				return nil
			}
			return addColumnIfMissing(tx, "credit_cards", "default_payment_account_id", "TEXT")
		},
	},
	{
		version: 46,
		name:    "scheduled_transaction_payments",
		up: func(tx *sql.Tx) error {
			exists, err := tableExists(tx, "transactions")
			if err != nil {
				return err
			}
			if !exists {
				return nil
			}
			if err := addColumnIfMissing(tx, "transactions", "scheduled_payment_at", "INTEGER"); err != nil {
				return fmt.Errorf("migration 46 add transactions.scheduled_payment_at: %w", err)
			}
			if err := addColumnIfMissing(tx, "transactions", "scheduled_payment_created_by", "TEXT"); err != nil {
				return fmt.Errorf("migration 46 add transactions.scheduled_payment_created_by: %w", err)
			}
			if err := addColumnIfMissing(tx, "transactions", "scheduled_payment_executed_at", "INTEGER"); err != nil {
				return fmt.Errorf("migration 46 add transactions.scheduled_payment_executed_at: %w", err)
			}
			if _, err := tx.Exec(`CREATE INDEX IF NOT EXISTS idx_transactions_scheduled_due ON transactions(workspace_id, scheduled_payment_at, scheduled_payment_executed_at) WHERE scheduled_payment_at IS NOT NULL`); err != nil {
				return fmt.Errorf("migration 46 create idx_transactions_scheduled_due: %w", err)
			}
			return nil
		},
	},
	{
		version: 47,
		name:    "notification_dedupe_key",
		up: func(tx *sql.Tx) error {
			exists, err := tableExists(tx, "user_notifications")
			if err != nil {
				return err
			}
			if !exists {
				return nil
			}
			if err := addColumnIfMissing(tx, "user_notifications", "dedupe_key", "TEXT"); err != nil {
				return fmt.Errorf("migration 47 add user_notifications.dedupe_key: %w", err)
			}
			if _, err := tx.Exec(`CREATE UNIQUE INDEX IF NOT EXISTS idx_user_notifications_dedupe ON user_notifications(user_id, workspace_id, dedupe_key) WHERE dedupe_key IS NOT NULL AND dedupe_key <> ''`); err != nil {
				return fmt.Errorf("migration 47 create idx_user_notifications_dedupe: %w", err)
			}
			return nil
		},
	},
	{
		version: 48,
		name:    "transaction_status_history_source",
		up: func(tx *sql.Tx) error {
			exists, err := tableExists(tx, "transaction_status_history")
			if err != nil {
				return err
			}
			if !exists {
				return nil
			}
			if err := addColumnIfMissing(tx, "transaction_status_history", "source", "TEXT NOT NULL DEFAULT 'manual'"); err != nil {
				return fmt.Errorf("migration 48 add transaction_status_history.source: %w", err)
			}
			return nil
		},
	},
	{
		version: 49,
		name:    "business_canonical_categories_repair",
		up: func(tx *sql.Tx) error {
			if _, err := EnsureBusinessCanonicalCategoriesForExistingWorkspacesTx(tx); err != nil {
				return fmt.Errorf("migration 49 repair business canonical categories: %w", err)
			}
			return nil
		},
	},
	{
		version: 50,
		name:    "recurring_series_versioning_base",
		up: func(tx *sql.Tx) error {
			if err := migrateRecurringSeriesVersioningBase(tx); err != nil {
				return fmt.Errorf("migration 50 recurring series versioning base: %w", err)
			}
			return nil
		},
	},
	{
		version: 51,
		name:    "recurring_materialization_state",
		up: func(tx *sql.Tx) error {
			if err := migrateRecurringMaterializationState(tx); err != nil {
				return fmt.Errorf("migration 51 recurring materialization state: %w", err)
			}
			return nil
		},
	},
	{
		version: 52,
		name:    "civil_date_columns",
		up: func(tx *sql.Tx) error {
			if err := migrateCivilDateColumns(tx); err != nil {
				return fmt.Errorf("migration 52 civil date columns: %w", err)
			}
			return nil
		},
	},
	{
		version: 53,
		name:    "recurring_rules_active_leaf_invariant",
		up: func(tx *sql.Tx) error {
			rulesExist, err := tableExists(tx, "recurring_rules")
			if err != nil {
				return err
			}
			if !rulesExist {
				return nil
			}

			stmts := []string{
				`CREATE TRIGGER IF NOT EXISTS trg_recurring_rules_no_active_parent_insert
BEFORE INSERT ON recurring_rules
WHEN NEW.parent_rule_id IS NOT NULL AND (
    SELECT active FROM recurring_rules WHERE id = NEW.parent_rule_id
) = 1
BEGIN
    SELECT RAISE(ABORT, 'active rule cannot have children');
END`,
				`CREATE TRIGGER IF NOT EXISTS trg_recurring_rules_no_active_parent_update
BEFORE UPDATE OF parent_rule_id ON recurring_rules
WHEN NEW.parent_rule_id IS NOT NULL
 AND NEW.parent_rule_id != COALESCE(OLD.parent_rule_id, '')
 AND (
    SELECT active FROM recurring_rules WHERE id = NEW.parent_rule_id
) = 1
BEGIN
    SELECT RAISE(ABORT, 'active rule cannot have children');
END`,
				`CREATE TRIGGER IF NOT EXISTS trg_recurring_rules_no_reactivation_with_children
BEFORE UPDATE OF active ON recurring_rules
WHEN NEW.active = 1 AND OLD.active = 0 AND (
    SELECT COUNT(1) FROM recurring_rules WHERE parent_rule_id = OLD.id
) > 0
BEGIN
    SELECT RAISE(ABORT, 'active rule cannot have children');
END`,
				`CREATE INDEX IF NOT EXISTS idx_recurring_rules_parent_rule_id ON recurring_rules(parent_rule_id)`,
			}
			for _, stmt := range stmts {
				if _, err := tx.Exec(stmt); err != nil {
					return fmt.Errorf("migration 53 active leaf invariant: %w", err)
				}
			}

			rows, err := tx.Query(`
				SELECT p.id, p.workspace_id
				FROM recurring_rules p
				WHERE p.active = 1
				  AND EXISTS (SELECT 1 FROM recurring_rules c WHERE c.parent_rule_id = p.id)
				ORDER BY p.id
			`)
			if err != nil {
				return fmt.Errorf("migration 53 preflight check invariant violations: %w", err)
			}
			defer rows.Close()

			var violations int
			var sampleID, sampleWS string
			for rows.Next() {
				if err := rows.Scan(&sampleID, &sampleWS); err != nil {
					return fmt.Errorf("migration 53 preflight scan invariant violation: %w", err)
				}
				violations++
			}
			if err := rows.Err(); err != nil {
				return fmt.Errorf("migration 53 preflight rows invariant violations: %w", err)
			}
			if violations > 0 {
				return fmt.Errorf(
					"migration 53 aborted: found %d active rule(s) with children in recurring_rules that violate the active-leaf invariant; example rule_id=%q workspace_id=%q; resolve these data violations before upgrading (deactivate the parent or reassign the children)",
					violations, sampleID, sampleWS,
				)
			}
			return nil
		},
	},
	{
		version: 54,
		name:    "recurring_occurrence_skips",
		up: func(tx *sql.Tx) error {
			if err := migrateRecurringOccurrenceSkips(tx); err != nil {
				return fmt.Errorf("migration 54 recurring occurrence skips: %w", err)
			}
			return nil
		},
	},
	{
		version: 55,
		name:    "box_monthly_recharge_identity",
		up: func(tx *sql.Tx) error {
			if err := migrateBoxMonthlyRechargeIdentity(tx, time.Now()); err != nil {
				return fmt.Errorf("migration 55 box monthly recharge identity: %w", err)
			}
			return nil
		},
	},
	{
		version: 56,
		name:    "box_contribution_lifecycle",
		up: func(tx *sql.Tx) error {
			if err := migrateBoxContributionLifecycle(tx); err != nil {
				return fmt.Errorf("migration 56 box contribution lifecycle: %w", err)
			}
			return nil
		},
	},
	{
		version: 57,
		name:    "workspace_monthly_planning_references",
		up: func(tx *sql.Tx) error {
			if err := migrateWorkspaceMonthlyPlanningReferences(tx); err != nil {
				return fmt.Errorf("migration 57 workspace monthly planning references: %w", err)
			}
			return nil
		},
	},
	{
		version: 58,
		name:    "invoice_payment_reversal_identity",
		up: func(tx *sql.Tx) error {
			if err := migrateInvoicePaymentReversalIdentity(tx); err != nil {
				return fmt.Errorf("migration 58 invoice payment reversal identity: %w", err)
			}
			return nil
		},
	},
	{
		version: 59,
		name:    "recurring_occurrence_blocks",
		up: func(tx *sql.Tx) error {
			if err := migrateRecurringOccurrenceBlocks(tx); err != nil {
				return fmt.Errorf("migration 59 recurring occurrence blocks: %w", err)
			}
			return nil
		},
	},
	{
		version: 60,
		name:    "transaction_refunds",
		up: func(tx *sql.Tx) error {
			if err := migrateTransactionRefunds(tx); err != nil {
				return fmt.Errorf("migration 60 transaction refunds: %w", err)
			}
			return nil
		},
	},
	{
		version: 61,
		name:    "invoice_general_refunds",
		up: func(tx *sql.Tx) error {
			if err := migrateInvoiceGeneralRefunds(tx); err != nil {
				return fmt.Errorf("migration 61 invoice general refunds: %w", err)
			}
			return nil
		},
	},
	{
		version: 62,
		name:    "auth_mfa_step_up_and_totp_replay_protection",
		up: func(tx *sql.Tx) error {
			if err := addColumnIfMissing(tx, "users", "totp_last_used_step", "INTEGER NOT NULL DEFAULT -1"); err != nil {
				return fmt.Errorf("add users.totp_last_used_step: %w", err)
			}
			stmts := []string{
				`CREATE TABLE IF NOT EXISTS mfa_setup_challenges (
					id           TEXT PRIMARY KEY,
					user_id      TEXT NOT NULL,
					token_hash   TEXT NOT NULL UNIQUE,
					secret_enc   TEXT NOT NULL,
					expires_at   INTEGER NOT NULL,
					consumed_at  INTEGER,
					created_at   INTEGER NOT NULL DEFAULT (unixepoch()),
					FOREIGN KEY (user_id) REFERENCES users(id) ON DELETE CASCADE
				)`,
				`CREATE INDEX IF NOT EXISTS idx_mfa_setup_challenges_user_id ON mfa_setup_challenges(user_id)`,
				`CREATE INDEX IF NOT EXISTS idx_mfa_setup_challenges_expires_at ON mfa_setup_challenges(expires_at)`,
			}
			for _, stmt := range stmts {
				if _, err := tx.Exec(stmt); err != nil {
					return err
				}
			}
			return nil
		},
	},
	{
		version: 63,
		name:    "session_recent_reauthentication",
		up: func(tx *sql.Tx) error {
			exists, err := tableExists(tx, "sessions")
			if err != nil {
				return fmt.Errorf("inspect sessions table: %w", err)
			}
			if !exists {
				return nil
			}
			if err := addColumnIfMissing(tx, "sessions", "reauthenticated_at", "INTEGER NOT NULL DEFAULT 0"); err != nil {
				return fmt.Errorf("add sessions.reauthenticated_at: %w", err)
			}
			return nil
		},
	},
	{
		version: 64,
		name:    "account_balance_adjustments",
		up:      migrateAccountBalanceAdjustments,
	},
	{
		version: 65,
		name:    "investments_and_valuations",
		up:      migrateInvestmentsAndValuations,
	},
	{
		version: 66,
		name:    "icon_keys_for_categories_limits_and_boxes",
		up:      migrateIconKeys,
	},
	{
		version: 67,
		name:    "transaction_refund_invariants",
		up:      migrateTransactionRefundInvariants,
	},
	{
		version: 68,
		name:    "transaction_refund_parent_hardening",
		up:      migrateTransactionRefundParentHardening,
	},
	// 69 is reserved for the suspended R7 provenance phase.
	{
		version: 70,
		name:    "investment_purchases",
		up:      migrateInvestmentPurchases,
	},
	{version: 71, name: "investment_withdrawals", up: migrateInvestmentWithdrawals},
	{version: 72, name: "investment_reference_revisions", up: migrateInvestmentReferences},
	{version: 73, name: "investment_financial_integration", up: migrateInvestmentFinancialIntegration},
	{version: 74, name: "investment_settlements", up: migrateInvestmentSettlements},
	{version: 75, name: "investment_settlement_financial", up: migrateInvestmentSettlementFinancial},
	{version: 76, name: "investment_result_classification", up: migrateInvestmentClassification},
	{version: 77, name: "investment_adoption_confirmation", up: migrateInvestmentAdoption},
	{version: 79, name: "investment_cancellation", up: migrateInvestmentCancellations},
	{version: 80, name: "investment_purchase_reversal", up: migrateInvestmentPurchaseReversals},
	{version: 81, name: "investment_record_cancellation", up: migrateInvestmentRecordCancellations},
	{version: 82, name: "investment_purchase_reversal_operability", up: migrateInvestmentPurchaseReversals82},
	// Experimental migration 78 is not homologated. Apply it explicitly in
	// candidate tests only; normal startup skips it and applies schema 79.
}

func migrateTransactionRefunds(tx *sql.Tx) error {
	statements := []string{
		`CREATE TABLE IF NOT EXISTS transaction_refunds (
			id             TEXT PRIMARY KEY,
			workspace_id   TEXT    NOT NULL,
			transaction_id TEXT    NOT NULL,
			amount_cents   INTEGER NOT NULL CHECK (amount_cents > 0),
			description    TEXT    NOT NULL,
			created_by     TEXT    NOT NULL,
			created_at     INTEGER NOT NULL DEFAULT (unixepoch()),
			reversed_at    INTEGER,
			reversed_by    TEXT,
			FOREIGN KEY (workspace_id)   REFERENCES workspaces(id)   ON DELETE CASCADE,
			FOREIGN KEY (transaction_id) REFERENCES transactions(id) ON DELETE CASCADE,
			FOREIGN KEY (created_by)     REFERENCES users(id)        ON DELETE RESTRICT,
			FOREIGN KEY (reversed_by)    REFERENCES users(id)        ON DELETE RESTRICT,
			CHECK ((reversed_at IS NULL AND reversed_by IS NULL) OR (reversed_at IS NOT NULL AND reversed_by IS NOT NULL))
		)`,
		`CREATE INDEX IF NOT EXISTS idx_transaction_refunds_transaction_active ON transaction_refunds(transaction_id, reversed_at)`,
		`CREATE INDEX IF NOT EXISTS idx_transaction_refunds_workspace_created ON transaction_refunds(workspace_id, created_at DESC)`,
		`CREATE TRIGGER IF NOT EXISTS trg_transaction_refunds_validate_insert
		BEFORE INSERT ON transaction_refunds
		FOR EACH ROW
		WHEN NEW.reversed_at IS NULL AND (
			NOT EXISTS (
				SELECT 1 FROM transactions t
				WHERE t.id = NEW.transaction_id
				  AND t.workspace_id = NEW.workspace_id
				  AND t.type = 'EXPENSE'
				  AND t.invoice_id IS NOT NULL
			) OR NEW.amount_cents + COALESCE((
				SELECT SUM(r.amount_cents)
				FROM transaction_refunds r
				WHERE r.transaction_id = NEW.transaction_id
				  AND r.workspace_id = NEW.workspace_id
				  AND r.reversed_at IS NULL
			), 0) > COALESCE((SELECT t.amount FROM transactions t WHERE t.id = NEW.transaction_id), 0)
		)
		BEGIN
			SELECT RAISE(ABORT, 'invalid transaction refund');
		END`,
		`CREATE TRIGGER IF NOT EXISTS trg_transactions_refund_floor
		BEFORE UPDATE OF amount ON transactions
		FOR EACH ROW
		WHEN NEW.amount < COALESCE((
			SELECT SUM(r.amount_cents)
			FROM transaction_refunds r
			WHERE r.transaction_id = OLD.id
			  AND r.workspace_id = OLD.workspace_id
			  AND r.reversed_at IS NULL
		), 0)
		BEGIN
			SELECT RAISE(ABORT, 'transaction amount below active refunds');
		END`,
	}
	for _, statement := range statements {
		if _, err := tx.Exec(statement); err != nil {
			return err
		}
	}
	return nil
}

// migrateInvoiceGeneralRefunds records a credit returned in a later billing
// cycle, where there is no purchase row in the current invoice to attach it
// to. It is an invoice adjustment, never an income transaction.
func migrateInvoiceGeneralRefunds(tx *sql.Tx) error {
	statements := []string{
		`CREATE TABLE IF NOT EXISTS invoice_refunds (
			id           TEXT PRIMARY KEY,
			workspace_id TEXT    NOT NULL,
			invoice_id   TEXT    NOT NULL,
			amount_cents INTEGER NOT NULL CHECK (amount_cents > 0),
			description  TEXT    NOT NULL,
			created_by   TEXT    NOT NULL,
			created_at   INTEGER NOT NULL DEFAULT (unixepoch()),
			reversed_at  INTEGER,
			reversed_by  TEXT,
			FOREIGN KEY (workspace_id) REFERENCES workspaces(id) ON DELETE CASCADE,
			FOREIGN KEY (invoice_id)   REFERENCES invoices(id)   ON DELETE CASCADE,
			FOREIGN KEY (created_by)   REFERENCES users(id)      ON DELETE RESTRICT,
			FOREIGN KEY (reversed_by)  REFERENCES users(id)      ON DELETE RESTRICT,
			CHECK ((reversed_at IS NULL AND reversed_by IS NULL) OR (reversed_at IS NOT NULL AND reversed_by IS NOT NULL))
		)`,
		`CREATE INDEX IF NOT EXISTS idx_invoice_refunds_invoice_active ON invoice_refunds(invoice_id, reversed_at)`,
		`CREATE INDEX IF NOT EXISTS idx_invoice_refunds_workspace_created ON invoice_refunds(workspace_id, created_at DESC)`,
		`CREATE TRIGGER IF NOT EXISTS trg_invoice_refunds_validate_insert
		BEFORE INSERT ON invoice_refunds
		FOR EACH ROW
		WHEN NEW.reversed_at IS NULL AND NOT EXISTS (
			SELECT 1
			FROM invoices i
			JOIN accounts a ON a.id = i.account_id
			WHERE i.id = NEW.invoice_id
			  AND a.workspace_id = NEW.workspace_id
			  AND a.type = 'CREDIT_CARD'
			  AND i.status IN ('OPEN', 'CLOSED')
		)
		BEGIN
			SELECT RAISE(ABORT, 'invalid invoice refund');
		END`,
		`CREATE TRIGGER IF NOT EXISTS trg_invoice_refunds_no_overpay
		BEFORE INSERT ON invoice_refunds
		FOR EACH ROW
		WHEN NEW.reversed_at IS NULL AND NEW.amount_cents + COALESCE((
			SELECT SUM(ir.amount_cents)
			FROM invoice_refunds ir
			WHERE ir.workspace_id = NEW.workspace_id
			  AND ir.invoice_id = NEW.invoice_id
			  AND ir.reversed_at IS NULL
		), 0) > COALESCE((
			SELECT SUM(t.amount - COALESCE((
				SELECT SUM(tr.amount_cents)
				FROM transaction_refunds tr
				WHERE tr.workspace_id = t.workspace_id
				  AND tr.transaction_id = t.id
				  AND tr.reversed_at IS NULL
			), 0))
			FROM transactions t
			WHERE t.workspace_id = NEW.workspace_id
			  AND t.invoice_id = NEW.invoice_id
			  AND t.type = 'EXPENSE'
		), 0) - COALESCE((
			SELECT CASE
				WHEN EXISTS (
					SELECT 1 FROM invoice_payments ip_lineage
					WHERE ip_lineage.workspace_id = NEW.workspace_id
					  AND ip_lineage.invoice_id = NEW.invoice_id
				) THEN COALESCE((
					SELECT SUM(ip_active.amount_cents)
					FROM invoice_payments ip_active
					WHERE ip_active.workspace_id = NEW.workspace_id
					  AND ip_active.invoice_id = NEW.invoice_id
					  AND ip_active.reversed_at IS NULL
				), 0)
				ELSE COALESCE(i.paid_amount, 0)
			END
			FROM invoices i
			WHERE i.id = NEW.invoice_id
		), 0)
		BEGIN
			SELECT RAISE(ABORT, 'invoice refund exceeds pending balance');
		END`,
		`CREATE TRIGGER IF NOT EXISTS trg_invoice_refunds_immutable_update
		BEFORE UPDATE OF workspace_id, invoice_id, amount_cents, description, created_by, created_at ON invoice_refunds
		FOR EACH ROW
		BEGIN
			SELECT RAISE(ABORT, 'invoice refund audit fields are immutable');
		END`,
		`CREATE TRIGGER IF NOT EXISTS trg_invoice_refunds_no_delete
		BEFORE DELETE ON invoice_refunds
		FOR EACH ROW
		BEGIN
			SELECT RAISE(ABORT, 'invoice refunds must be reversed, not deleted');
		END`,
	}
	for _, statement := range statements {
		if _, err := tx.Exec(statement); err != nil {
			return err
		}
	}
	return nil
}

func migrateRecurringOccurrenceBlocks(tx *sql.Tx) error {
	// This preflight must remain before every v59 DDL statement. Existing
	// transaction+skip pairs are contradictory terminal outcomes and require an
	// explicit operator decision; migration 59 never repairs or chooses a winner.
	if err := preflightRecurringOccurrenceTerminalConflicts(tx); err != nil {
		return err
	}

	statements := []string{recurringOccurrenceBlocksDDL}
	for _, trigger := range recurringOccurrenceBlocksTriggerContract {
		statements = append(statements, trigger.ddl)
	}
	for _, statement := range statements {
		if _, err := tx.Exec(statement); err != nil {
			return err
		}
	}
	if err := validateRecurringOccurrenceBlocksSchema(tx); err != nil {
		return fmt.Errorf("validate recurring occurrence blocks schema: %w", err)
	}
	return nil
}

const recurringOccurrenceBlocksDDL = `CREATE TABLE recurring_occurrence_blocks (
    workspace_id          TEXT    NOT NULL,
    series_id             TEXT    NOT NULL,
    recurring_rule_id     TEXT    NOT NULL,
    recurrence_sequence   INTEGER NOT NULL CHECK (recurrence_sequence > 0),
    occurrence_date_civil TEXT    NOT NULL,
    invoice_id            TEXT    NOT NULL,
    invoice_reference     TEXT    NOT NULL,
    invoice_status        TEXT    NOT NULL CHECK (invoice_status IN ('CLOSED', 'PAID')),
    reason                TEXT    NOT NULL CHECK (reason = 'frozen_invoice'),
    created_at            INTEGER NOT NULL,
    PRIMARY KEY (workspace_id, series_id, recurrence_sequence),
    UNIQUE (workspace_id, recurring_rule_id, recurrence_sequence)
)`

var recurringOccurrenceBlocksTriggerContract = []struct {
	name      string
	tableName string
	ddl       string
}{
	{
		name:      "trg_recurring_occurrence_blocks_no_transaction",
		tableName: "recurring_occurrence_blocks",
		ddl: `CREATE TRIGGER trg_recurring_occurrence_blocks_no_transaction
BEFORE INSERT ON recurring_occurrence_blocks
WHEN EXISTS (
    SELECT 1 FROM transactions t
    WHERE t.workspace_id = NEW.workspace_id
      AND t.series_id = NEW.series_id
      AND t.recurrence_sequence = NEW.recurrence_sequence
)
BEGIN
    SELECT RAISE(ABORT, 'recurring occurrence terminal conflict: transaction exists');
END`,
	},
	{
		name:      "trg_recurring_occurrence_blocks_no_skip",
		tableName: "recurring_occurrence_blocks",
		ddl: `CREATE TRIGGER trg_recurring_occurrence_blocks_no_skip
BEFORE INSERT ON recurring_occurrence_blocks
WHEN EXISTS (
    SELECT 1 FROM recurring_occurrence_skips s
    WHERE s.workspace_id = NEW.workspace_id
      AND s.series_id = NEW.series_id
      AND s.recurrence_sequence = NEW.recurrence_sequence
)
BEGIN
    SELECT RAISE(ABORT, 'recurring occurrence terminal conflict: manual skip exists');
END`,
	},
	{
		name:      "trg_transactions_no_recurring_terminal_insert",
		tableName: "transactions",
		ddl: `CREATE TRIGGER trg_transactions_no_recurring_terminal_insert
BEFORE INSERT ON transactions
WHEN NEW.series_id IS NOT NULL
 AND NEW.recurrence_sequence IS NOT NULL
 AND (
    EXISTS (
        SELECT 1 FROM recurring_occurrence_skips s
        WHERE s.workspace_id = NEW.workspace_id
          AND s.series_id = NEW.series_id
          AND s.recurrence_sequence = NEW.recurrence_sequence
    )
    OR EXISTS (
        SELECT 1 FROM recurring_occurrence_blocks b
        WHERE b.workspace_id = NEW.workspace_id
          AND b.series_id = NEW.series_id
          AND b.recurrence_sequence = NEW.recurrence_sequence
    )
 )
BEGIN
    SELECT RAISE(ABORT, 'recurring occurrence terminal conflict: tombstone exists');
END`,
	},
	{
		name:      "trg_transactions_no_recurring_terminal_update",
		tableName: "transactions",
		ddl: `CREATE TRIGGER trg_transactions_no_recurring_terminal_update
BEFORE UPDATE OF workspace_id, series_id, recurrence_sequence ON transactions
WHEN NEW.series_id IS NOT NULL
 AND NEW.recurrence_sequence IS NOT NULL
 AND (
    EXISTS (
        SELECT 1 FROM recurring_occurrence_skips s
        WHERE s.workspace_id = NEW.workspace_id
          AND s.series_id = NEW.series_id
          AND s.recurrence_sequence = NEW.recurrence_sequence
    )
    OR EXISTS (
        SELECT 1 FROM recurring_occurrence_blocks b
        WHERE b.workspace_id = NEW.workspace_id
          AND b.series_id = NEW.series_id
          AND b.recurrence_sequence = NEW.recurrence_sequence
    )
 )
BEGIN
    SELECT RAISE(ABORT, 'recurring occurrence terminal conflict: tombstone exists');
END`,
	},
	{
		name:      "trg_recurring_occurrence_skips_no_block_insert",
		tableName: "recurring_occurrence_skips",
		ddl: `CREATE TRIGGER trg_recurring_occurrence_skips_no_block_insert
BEFORE INSERT ON recurring_occurrence_skips
WHEN EXISTS (
    SELECT 1 FROM recurring_occurrence_blocks b
    WHERE b.workspace_id = NEW.workspace_id
      AND b.series_id = NEW.series_id
      AND b.recurrence_sequence = NEW.recurrence_sequence
)
BEGIN
    SELECT RAISE(ABORT, 'recurring occurrence terminal conflict: frozen block exists');
END`,
	},
	{
		name:      "trg_recurring_occurrence_skips_no_block_update",
		tableName: "recurring_occurrence_skips",
		ddl: `CREATE TRIGGER trg_recurring_occurrence_skips_no_block_update
BEFORE UPDATE OF workspace_id, series_id, recurrence_sequence ON recurring_occurrence_skips
WHEN EXISTS (
    SELECT 1 FROM recurring_occurrence_blocks b
    WHERE b.workspace_id = NEW.workspace_id
      AND b.series_id = NEW.series_id
      AND b.recurrence_sequence = NEW.recurrence_sequence
)
BEGIN
    SELECT RAISE(ABORT, 'recurring occurrence terminal conflict: frozen block exists');
END`,
	},
	{
		name:      "trg_recurring_occurrence_blocks_immutable_update",
		tableName: "recurring_occurrence_blocks",
		ddl: `CREATE TRIGGER trg_recurring_occurrence_blocks_immutable_update
BEFORE UPDATE ON recurring_occurrence_blocks
BEGIN
    SELECT RAISE(ABORT, 'recurring occurrence block is immutable');
END`,
	},
	{
		name:      "trg_recurring_occurrence_blocks_immutable_delete",
		tableName: "recurring_occurrence_blocks",
		ddl: `CREATE TRIGGER trg_recurring_occurrence_blocks_immutable_delete
BEFORE DELETE ON recurring_occurrence_blocks
BEGIN
    SELECT RAISE(ABORT, 'recurring occurrence block is immutable');
END`,
	},
}

var recurringOccurrenceBlocksColumnContract = []struct {
	name     string
	typeName string
	notNull  int
	pk       int
}{
	{name: "workspace_id", typeName: "TEXT", notNull: 1, pk: 1},
	{name: "series_id", typeName: "TEXT", notNull: 1, pk: 2},
	{name: "recurring_rule_id", typeName: "TEXT", notNull: 1, pk: 0},
	{name: "recurrence_sequence", typeName: "INTEGER", notNull: 1, pk: 3},
	{name: "occurrence_date_civil", typeName: "TEXT", notNull: 1, pk: 0},
	{name: "invoice_id", typeName: "TEXT", notNull: 1, pk: 0},
	{name: "invoice_reference", typeName: "TEXT", notNull: 1, pk: 0},
	{name: "invoice_status", typeName: "TEXT", notNull: 1, pk: 0},
	{name: "reason", typeName: "TEXT", notNull: 1, pk: 0},
	{name: "created_at", typeName: "INTEGER", notNull: 1, pk: 0},
}

func validateRecurringOccurrenceBlocksSchema(tx *sql.Tx) error {
	type columnInfo struct {
		cid      int
		name     string
		typeName string
		notNull  int
		dflt     *string
		pk       int
		hidden   int
	}

	rows, err := tx.Query(`PRAGMA table_xinfo(recurring_occurrence_blocks)`)
	if err != nil {
		return fmt.Errorf("pragma table_xinfo recurring_occurrence_blocks: %w", err)
	}
	var actualColumns []columnInfo
	for rows.Next() {
		var column columnInfo
		if err := rows.Scan(&column.cid, &column.name, &column.typeName, &column.notNull, &column.dflt, &column.pk, &column.hidden); err != nil {
			rows.Close()
			return fmt.Errorf("scan recurring_occurrence_blocks column: %w", err)
		}
		actualColumns = append(actualColumns, column)
	}
	if err := rows.Close(); err != nil {
		return fmt.Errorf("close recurring_occurrence_blocks columns: %w", err)
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("iterate recurring_occurrence_blocks columns: %w", err)
	}
	if len(actualColumns) != len(recurringOccurrenceBlocksColumnContract) {
		return fmt.Errorf("recurring_occurrence_blocks: expected %d columns, got %d", len(recurringOccurrenceBlocksColumnContract), len(actualColumns))
	}
	for i, column := range actualColumns {
		want := recurringOccurrenceBlocksColumnContract[i]
		actualType := strings.TrimSpace(strings.ToUpper(column.typeName))
		if column.name != want.name || actualType != want.typeName || column.notNull != want.notNull || column.pk != want.pk {
			return fmt.Errorf("recurring_occurrence_blocks: column %d contract mismatch", i)
		}
		if column.dflt != nil || column.hidden != 0 {
			return fmt.Errorf("recurring_occurrence_blocks: column %q has unexpected default/visibility", column.name)
		}
	}

	var actualDDL string
	if err := tx.QueryRow(`SELECT sql FROM sqlite_schema WHERE type='table' AND name='recurring_occurrence_blocks'`).Scan(&actualDDL); err != nil {
		return fmt.Errorf("read recurring_occurrence_blocks DDL: %w", err)
	}
	if normalizeSkipsDDL(actualDDL) != normalizeSkipsDDL(recurringOccurrenceBlocksDDL) {
		return fmt.Errorf("recurring_occurrence_blocks: DDL does not match authorized contract")
	}
	if err := validateRecurringOccurrenceBlocksIndexes(tx); err != nil {
		return err
	}
	if err := validateRecurringOccurrenceBlocksTriggers(tx); err != nil {
		return err
	}
	foreignKeys, err := tx.Query(`PRAGMA foreign_key_list(recurring_occurrence_blocks)`)
	if err != nil {
		return fmt.Errorf("pragma foreign_key_list recurring_occurrence_blocks: %w", err)
	}
	defer foreignKeys.Close()
	if foreignKeys.Next() {
		return fmt.Errorf("recurring_occurrence_blocks: foreign key found, expected none")
	}
	return foreignKeys.Err()
}

func validateRecurringOccurrenceBlocksIndexes(tx *sql.Tx) error {
	rows, err := tx.Query(`PRAGMA index_list(recurring_occurrence_blocks)`)
	if err != nil {
		return fmt.Errorf("pragma index_list recurring_occurrence_blocks: %w", err)
	}
	defer rows.Close()
	wantPK := []string{"workspace_id", "series_id", "recurrence_sequence"}
	wantUnique := []string{"workspace_id", "recurring_rule_id", "recurrence_sequence"}
	var foundPK, foundUnique bool
	for rows.Next() {
		var seq, unique, partial int
		var name, origin string
		if err := rows.Scan(&seq, &name, &unique, &origin, &partial); err != nil {
			return fmt.Errorf("scan recurring_occurrence_blocks index: %w", err)
		}
		columns, err := sqliteIndexColumnsTx(tx, name)
		if err != nil {
			return err
		}
		if partial != 0 || unique != 1 {
			return fmt.Errorf("recurring_occurrence_blocks: incompatible index %q", name)
		}
		switch origin {
		case "pk":
			if foundPK || !stringSlicesEqual(columns, wantPK) {
				return fmt.Errorf("recurring_occurrence_blocks: incompatible primary-key index")
			}
			foundPK = true
		case "u":
			if foundUnique || !stringSlicesEqual(columns, wantUnique) {
				return fmt.Errorf("recurring_occurrence_blocks: incompatible UNIQUE index")
			}
			foundUnique = true
		default:
			return fmt.Errorf("recurring_occurrence_blocks: unexpected index %q origin=%q", name, origin)
		}
	}
	if err := rows.Err(); err != nil {
		return err
	}
	if !foundPK || !foundUnique {
		return fmt.Errorf("recurring_occurrence_blocks: missing PK or UNIQUE index")
	}
	return nil
}

func sqliteIndexColumnsTx(tx *sql.Tx, indexName string) ([]string, error) {
	query := fmt.Sprintf("PRAGMA index_info('%s')", strings.ReplaceAll(indexName, "'", "''"))
	rows, err := tx.Query(query)
	if err != nil {
		return nil, fmt.Errorf("pragma index_info %q: %w", indexName, err)
	}
	defer rows.Close()
	var columns []string
	for rows.Next() {
		var seq, cid int
		var name string
		if err := rows.Scan(&seq, &cid, &name); err != nil {
			return nil, fmt.Errorf("scan index_info %q: %w", indexName, err)
		}
		columns = append(columns, name)
	}
	return columns, rows.Err()
}

func stringSlicesEqual(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	for i := range left {
		if left[i] != right[i] {
			return false
		}
	}
	return true
}

func validateRecurringOccurrenceBlocksTriggers(tx *sql.Tx) error {
	for _, want := range recurringOccurrenceBlocksTriggerContract {
		var tableName, ddl string
		if err := tx.QueryRow(`SELECT tbl_name, sql FROM sqlite_schema WHERE type='trigger' AND name=?`, want.name).Scan(&tableName, &ddl); err != nil {
			return fmt.Errorf("recurring occurrence trigger %q: %w", want.name, err)
		}
		if tableName != want.tableName || normalizeSkipsDDL(ddl) != normalizeSkipsDDL(want.ddl) {
			return fmt.Errorf("recurring occurrence trigger %q does not match authorized contract", want.name)
		}
	}
	return nil
}

func preflightRecurringOccurrenceTerminalConflicts(tx *sql.Tx) error {
	const conflictSource = `
		FROM recurring_occurrence_skips s
		JOIN transactions t
		  ON t.workspace_id = s.workspace_id
		 AND t.series_id = s.series_id
		 AND t.recurrence_sequence = s.recurrence_sequence`

	var conflicts int
	if err := tx.QueryRow(`SELECT COUNT(1) FROM (
		SELECT s.workspace_id, s.series_id, s.recurrence_sequence
		` + conflictSource + `
		GROUP BY s.workspace_id, s.series_id, s.recurrence_sequence
	)`).Scan(&conflicts); err != nil {
		return fmt.Errorf("migration 59 preflight recurring terminal conflicts: %w", err)
	}
	if conflicts == 0 {
		return nil
	}

	var workspaceID, seriesID, transactionID string
	var sequence int64
	if err := tx.QueryRow(`
		SELECT s.workspace_id, s.series_id, s.recurrence_sequence, t.id
		`+conflictSource+`
		ORDER BY s.workspace_id, s.series_id, s.recurrence_sequence, t.id
		LIMIT 1
	`).Scan(&workspaceID, &seriesID, &sequence, &transactionID); err != nil {
		return fmt.Errorf("migration 59 preflight recurring terminal conflict sample: %w", err)
	}
	return fmt.Errorf(
		"migration 59 aborted: found %d committed recurring terminal occurrence conflict(s) between transactions and recurring_occurrence_skips; example workspace_id=%q series_id=%q recurrence_sequence=%d transaction_id=%q; resolve the integrity conflict explicitly before upgrading",
		conflicts, workspaceID, seriesID, sequence, transactionID,
	)
}

func migrateInvoicePaymentReversalIdentity(tx *sql.Tx) error {
	paymentsExist, err := tableExists(tx, "invoice_payments")
	if err != nil {
		return err
	}
	if !paymentsExist {
		return nil
	}

	columns := []struct {
		name string
		ddl  string
	}{
		{name: "reversed_by", ddl: "TEXT"},
		{name: "reversal_reason", ddl: "TEXT"},
		{name: "reversal_transaction_id", ddl: "TEXT REFERENCES transactions(id) ON DELETE RESTRICT"},
		{name: "reversed_date_civil", ddl: "TEXT"},
	}
	for _, column := range columns {
		if err := addColumnIfMissing(tx, "invoice_payments", column.name, column.ddl); err != nil {
			return fmt.Errorf("add invoice_payments.%s: %w", column.name, err)
		}
	}

	var duplicateWorkspaceID, duplicateTransactionID string
	var duplicateCount int
	err = tx.QueryRow(`
		SELECT workspace_id, transaction_id, COUNT(1)
		FROM invoice_payments
		WHERE transaction_id IS NOT NULL
		GROUP BY workspace_id, transaction_id
		HAVING COUNT(1) > 1
		ORDER BY workspace_id, transaction_id
		LIMIT 1
	`).Scan(&duplicateWorkspaceID, &duplicateTransactionID, &duplicateCount)
	if err != nil && err != sql.ErrNoRows {
		return fmt.Errorf("preflight original transaction identity: %w", err)
	}
	if err == nil {
		return fmt.Errorf(
			"preflight found duplicate original payment identity workspace_id=%q transaction_id=%q count=%d",
			duplicateWorkspaceID,
			duplicateTransactionID,
			duplicateCount,
		)
	}

	indexes := []string{
		`CREATE UNIQUE INDEX IF NOT EXISTS uq_invoice_payments_workspace_transaction
			ON invoice_payments(workspace_id, transaction_id)
			WHERE transaction_id IS NOT NULL`,
		`CREATE UNIQUE INDEX IF NOT EXISTS uq_invoice_payments_workspace_reversal_transaction
			ON invoice_payments(workspace_id, reversal_transaction_id)
			WHERE reversal_transaction_id IS NOT NULL`,
	}
	for _, stmt := range indexes {
		if _, err := tx.Exec(stmt); err != nil {
			return fmt.Errorf("create invoice payment identity index: %w", err)
		}
	}
	return nil
}

func migrateWorkspaceMonthlyPlanningReferences(tx *sql.Tx) error {
	workspacesExist, err := tableExists(tx, "workspaces")
	if err != nil {
		return err
	}
	if !workspacesExist {
		return nil
	}

	columns := []struct {
		name string
		ddl  string
	}{
		{name: "monthly_income_reference", ddl: "INTEGER CHECK (monthly_income_reference IS NULL OR monthly_income_reference >= 0)"},
		{name: "monthly_total_cost_reference", ddl: "INTEGER CHECK (monthly_total_cost_reference IS NULL OR monthly_total_cost_reference >= 0)"},
		{name: "monthly_essential_cost_reference", ddl: "INTEGER CHECK (monthly_essential_cost_reference IS NULL OR monthly_essential_cost_reference >= 0)"},
	}
	for _, column := range columns {
		if err := addColumnIfMissing(tx, "workspaces", column.name, column.ddl); err != nil {
			return fmt.Errorf("add workspaces.%s: %w", column.name, err)
		}
	}
	return nil
}

func migrateBoxContributionLifecycle(tx *sql.Tx) error {
	boxesExist, err := tableExists(tx, "boxes")
	if err != nil {
		return err
	}
	if !boxesExist {
		return nil
	}

	if err := addColumnIfMissing(tx, "boxes", "continue_after_target", "INTEGER NOT NULL DEFAULT 1 CHECK (continue_after_target IN (0, 1))"); err != nil {
		return fmt.Errorf("add boxes.continue_after_target: %w", err)
	}
	if err := addColumnIfMissing(tx, "boxes", "automatic_contribution_state", "TEXT NOT NULL DEFAULT 'ACTIVE' CHECK (automatic_contribution_state IN ('ACTIVE', 'BLOCKED'))"); err != nil {
		return fmt.Errorf("add boxes.automatic_contribution_state: %w", err)
	}

	if _, err := tx.Exec(`
		CREATE TABLE IF NOT EXISTS box_contribution_lifecycle_events (
			id             TEXT PRIMARY KEY,
			box_id         TEXT    NOT NULL,
			event_type     TEXT    NOT NULL CHECK (event_type IN ('GOAL_REACHED', 'CONTRIBUTION_BLOCKED', 'CONTRIBUTION_REACTIVATED')),
			previous_state TEXT CHECK (previous_state IS NULL OR previous_state IN ('ACTIVE', 'BLOCKED')),
			new_state      TEXT CHECK (new_state IS NULL OR new_state IN ('ACTIVE', 'BLOCKED')),
			occurred_at    INTEGER NOT NULL,
			CHECK (
				(event_type = 'GOAL_REACHED' AND previous_state IS NULL AND new_state IS NULL) OR
				(event_type = 'CONTRIBUTION_BLOCKED' AND previous_state = 'ACTIVE' AND new_state = 'BLOCKED') OR
				(event_type = 'CONTRIBUTION_REACTIVATED' AND previous_state = 'BLOCKED' AND new_state = 'ACTIVE')
			),
			FOREIGN KEY (box_id) REFERENCES boxes(id) ON DELETE CASCADE
		)
	`); err != nil {
		return fmt.Errorf("create box_contribution_lifecycle_events: %w", err)
	}
	if _, err := tx.Exec(`
		CREATE INDEX IF NOT EXISTS idx_box_contribution_lifecycle_events_box_occurred
		ON box_contribution_lifecycle_events(box_id, occurred_at, id)
	`); err != nil {
		return fmt.Errorf("create box contribution lifecycle event index: %w", err)
	}
	return nil
}

func migrateBoxMonthlyRechargeIdentity(tx *sql.Tx, now time.Time) error {
	boxesExist, err := tableExists(tx, "boxes")
	if err != nil {
		return err
	}
	ledgerExists, err := tableExists(tx, "box_virtual_ledger")
	if err != nil {
		return err
	}
	if !boxesExist || !ledgerExists {
		// Historical migration tests intentionally use partial schemas. Normal
		// application startup executes ddl first, so a real ContaBase database
		// always has both tables before v55 runs.
		return nil
	}

	const civilMonthCheck = `CHECK (
		%s IS NULL OR (
			length(%s) = 7 AND
			substr(%s, 5, 1) = '-' AND
			substr(%s, 1, 4) NOT GLOB '*[^0-9]*' AND
			substr(%s, 6, 2) NOT GLOB '*[^0-9]*' AND
			CAST(substr(%s, 6, 2) AS INTEGER) BETWEEN 1 AND 12
		)
	)`
	anchorColumn := "monthly_recharge_anchor_month"
	anchorDDL := fmt.Sprintf("TEXT "+civilMonthCheck, anchorColumn, anchorColumn, anchorColumn, anchorColumn, anchorColumn, anchorColumn)
	if err := addColumnIfMissing(tx, "boxes", anchorColumn, anchorDDL); err != nil {
		return fmt.Errorf("add boxes.%s: %w", anchorColumn, err)
	}
	automaticMonthColumn := "automatic_recharge_month"
	const automaticCivilMonthCheck = `CHECK (
		%s IS NULL OR (
			type = 'RECHARGE' AND
			length(%s) = 7 AND
			substr(%s, 5, 1) = '-' AND
			substr(%s, 1, 4) NOT GLOB '*[^0-9]*' AND
			substr(%s, 6, 2) NOT GLOB '*[^0-9]*' AND
			CAST(substr(%s, 6, 2) AS INTEGER) BETWEEN 1 AND 12
		)
	)`
	automaticMonthDDL := fmt.Sprintf("TEXT "+automaticCivilMonthCheck, automaticMonthColumn, automaticMonthColumn, automaticMonthColumn, automaticMonthColumn, automaticMonthColumn, automaticMonthColumn)
	if err := addColumnIfMissing(tx, "box_virtual_ledger", automaticMonthColumn, automaticMonthDDL); err != nil {
		return fmt.Errorf("add box_virtual_ledger.%s: %w", automaticMonthColumn, err)
	}

	loc, err := resolveCivilRepairTimezone()
	if err != nil {
		return fmt.Errorf("resolve financial timezone: %w", err)
	}
	rolloutMonth := now.In(loc).Format("2006-01")
	if _, err := tx.Exec(`
		UPDATE boxes
		SET monthly_recharge_anchor_month = ?
		WHERE monthly_recharge_anchor_month IS NULL
	`, rolloutMonth); err != nil {
		return fmt.Errorf("anchor existing boxes at rollout month %s: %w", rolloutMonth, err)
	}

	// The old engine had no structural origin. Its exact fixed description is
	// therefore the only evidence available during upgrade. Use it once here to
	// recognize a deterministic first legacy event per box/civil month. Future
	// idempotency depends only on the structural column and unique index below.
	rows, err := tx.Query(`
		SELECT id, box_id, reference_date
		FROM box_virtual_ledger
		WHERE type = 'RECHARGE'
		  AND description = 'Aporte Automático'
		  AND automatic_recharge_month IS NULL
		ORDER BY box_id, reference_date, created_at, id
	`)
	if err != nil {
		return fmt.Errorf("select legacy automatic box recharges: %w", err)
	}
	type legacyRecharge struct {
		id            string
		boxID         string
		referenceDate int64
	}
	var legacy []legacyRecharge
	for rows.Next() {
		var event legacyRecharge
		if err := rows.Scan(&event.id, &event.boxID, &event.referenceDate); err != nil {
			_ = rows.Close()
			return fmt.Errorf("scan legacy automatic box recharge: %w", err)
		}
		legacy = append(legacy, event)
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return fmt.Errorf("legacy automatic box recharges: %w", err)
	}
	if err := rows.Close(); err != nil {
		return fmt.Errorf("close legacy automatic box recharges: %w", err)
	}

	recognized := make(map[string]struct{}, len(legacy))
	for _, event := range legacy {
		month := time.Unix(event.referenceDate, 0).In(loc).Format("2006-01")
		identity := event.boxID + "\x00" + month
		if _, duplicate := recognized[identity]; duplicate {
			continue
		}
		if _, err := tx.Exec(`
			UPDATE box_virtual_ledger
			SET automatic_recharge_month = ?
			WHERE id = ? AND automatic_recharge_month IS NULL
		`, month, event.id); err != nil {
			return fmt.Errorf("recognize legacy automatic box recharge %q: %w", event.id, err)
		}
		recognized[identity] = struct{}{}
	}

	if _, err := tx.Exec(`
		CREATE UNIQUE INDEX IF NOT EXISTS idx_box_virtual_ledger_automatic_recharge_month
		ON box_virtual_ledger(box_id, automatic_recharge_month)
		WHERE automatic_recharge_month IS NOT NULL
	`); err != nil {
		return fmt.Errorf("create automatic box recharge identity index: %w", err)
	}
	return nil
}

func migrateRecurringOccurrenceSkips(tx *sql.Tx) error {
	if _, err := tx.Exec(`CREATE TABLE IF NOT EXISTS recurring_occurrence_skips (
    workspace_id          TEXT    NOT NULL,
    series_id             TEXT    NOT NULL,
    recurrence_sequence   INTEGER NOT NULL CHECK (recurrence_sequence > 0),
    original_transaction_id TEXT,
    reason                TEXT    NOT NULL CHECK (reason IN ('single_delete')),
    deleted_by            TEXT,
    deleted_at            INTEGER NOT NULL,
    PRIMARY KEY (workspace_id, series_id, recurrence_sequence)
)`); err != nil {
		return err
	}

	if err := validateRecurringOccurrenceSkipsSchema(tx); err != nil {
		return err
	}
	return nil
}

const authorizedSkipsDDL = `CREATE TABLE recurring_occurrence_skips (
    workspace_id TEXT NOT NULL,
    series_id TEXT NOT NULL,
    recurrence_sequence INTEGER NOT NULL CHECK (recurrence_sequence > 0),
    original_transaction_id TEXT,
    reason TEXT NOT NULL CHECK (reason IN ('single_delete')),
    deleted_by TEXT,
    deleted_at INTEGER NOT NULL,
    PRIMARY KEY (workspace_id, series_id, recurrence_sequence)
)`

var authorizedSkipsColumnContract = []struct {
	name     string
	typeName string
	notNull  int
	pk       int
}{
	{name: "workspace_id", typeName: "TEXT", notNull: 1, pk: 1},
	{name: "series_id", typeName: "TEXT", notNull: 1, pk: 2},
	{name: "recurrence_sequence", typeName: "INTEGER", notNull: 1, pk: 3},
	{name: "original_transaction_id", typeName: "TEXT", notNull: 0, pk: 0},
	{name: "reason", typeName: "TEXT", notNull: 1, pk: 0},
	{name: "deleted_by", typeName: "TEXT", notNull: 0, pk: 0},
	{name: "deleted_at", typeName: "INTEGER", notNull: 1, pk: 0},
}

func validateRecurringOccurrenceSkipsSchema(tx *sql.Tx) error {
	rows, err := tx.Query(`PRAGMA table_xinfo(recurring_occurrence_skips)`)
	if err != nil {
		return fmt.Errorf("pragma table_xinfo recurring_occurrence_skips: %w", err)
	}
	defer rows.Close()

	type columnInfo struct {
		cid      int
		name     string
		typeName string
		notNull  int
		dflt     *string
		pk       int
		hidden   int
	}
	var actual []columnInfo
	for rows.Next() {
		var c columnInfo
		if err := rows.Scan(&c.cid, &c.name, &c.typeName, &c.notNull, &c.dflt, &c.pk, &c.hidden); err != nil {
			return fmt.Errorf("scan table_xinfo recurring_occurrence_skips: %w", err)
		}
		actual = append(actual, c)
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("iterate table_xinfo recurring_occurrence_skips: %w", err)
	}

	if len(actual) != len(authorizedSkipsColumnContract) {
		return fmt.Errorf("recurring_occurrence_skips: expected %d columns, got %d", len(authorizedSkipsColumnContract), len(actual))
	}

	for i, col := range actual {
		want := authorizedSkipsColumnContract[i]
		if col.name != want.name {
			return fmt.Errorf("recurring_occurrence_skips: column %d name = %q, want %q", i, col.name, want.name)
		}
		actualType := strings.TrimSpace(strings.ToUpper(col.typeName))
		if actualType != want.typeName {
			return fmt.Errorf("recurring_occurrence_skips: column %q type = %q, want %q", col.name, actualType, want.typeName)
		}
		if col.notNull != want.notNull {
			return fmt.Errorf("recurring_occurrence_skips: column %q notnull = %d, want %d", col.name, col.notNull, want.notNull)
		}
		if col.pk != want.pk {
			return fmt.Errorf("recurring_occurrence_skips: column %q pk = %d, want %d", col.name, col.pk, want.pk)
		}
		if col.dflt != nil {
			return fmt.Errorf("recurring_occurrence_skips: column %q has default value %q, want none", col.name, *col.dflt)
		}
		if col.hidden != 0 {
			return fmt.Errorf("recurring_occurrence_skips: column %q is hidden (%d), want visible", col.name, col.hidden)
		}
	}

	var actualDDL string
	if err := tx.QueryRow(`
		SELECT sql FROM sqlite_schema
		WHERE type = 'table'
		  AND name = 'recurring_occurrence_skips'
	`).Scan(&actualDDL); err != nil {
		return fmt.Errorf("read recurring_occurrence_skips DDL: %w", err)
	}

	expected := normalizeSkipsDDL(authorizedSkipsDDL)
	actualCanonical := normalizeSkipsDDL(actualDDL)

	if actualCanonical != expected {
		return fmt.Errorf("recurring_occurrence_skips: DDL does not match authorized contract")
	}

	if err := checkSkipsNoExtraIndexes(tx); err != nil {
		return err
	}
	if err := checkSkipsNoForeignKeys(tx); err != nil {
		return err
	}
	if err := checkSkipsNoTriggers(tx); err != nil {
		return err
	}

	return nil
}

func normalizeSkipsDDL(sql string) string {
	if sql == "" {
		return ""
	}
	var out strings.Builder
	out.Grow(len(sql))

	const (
		stateNormal = iota
		stateSingleQuote
		stateDoubleQuote
		stateBracket
		stateBacktick
	)

	state := stateNormal
	prevWasSpace := true

	for i := 0; i < len(sql); i++ {
		ch := sql[i]

		switch state {
		case stateNormal:
			switch ch {
			case '\'':
				state = stateSingleQuote
				out.WriteByte(ch)
			case '"':
				state = stateDoubleQuote
				out.WriteByte(ch)
			case '[':
				state = stateBracket
				out.WriteByte(ch)
			case '`':
				state = stateBacktick
				out.WriteByte(ch)
			case ' ', '\t', '\n', '\r', '\f':
				if prevWasSpace {
					continue
				}
				prevWasSpace = true
				out.WriteByte(' ')
				continue
			case '(':
				if !prevWasSpace {
					out.WriteByte(' ')
				}
				out.WriteByte(ch)
				prevWasSpace = false
			case ')':
				out.WriteByte(ch)
				prevWasSpace = false
			case ',':
				out.WriteByte(ch)
				prevWasSpace = false
			default:
				if ch >= 'a' && ch <= 'z' {
					out.WriteByte(ch - 32)
				} else {
					out.WriteByte(ch)
				}
				prevWasSpace = false
			}

		case stateSingleQuote:
			out.WriteByte(ch)
			if ch == '\'' {
				if i+1 < len(sql) && sql[i+1] == '\'' {
					i++
					out.WriteByte('\'')
				} else {
					state = stateNormal
				}
			}

		case stateDoubleQuote:
			out.WriteByte(ch)
			if ch == '"' {
				if i+1 < len(sql) && sql[i+1] == '"' {
					i++
					out.WriteByte('"')
				} else {
					state = stateNormal
				}
			}

		case stateBracket:
			out.WriteByte(ch)
			if ch == ']' {
				state = stateNormal
			}

		case stateBacktick:
			out.WriteByte(ch)
			if ch == '`' {
				state = stateNormal
			}
		}
	}

	result := strings.TrimSpace(out.String())
	if strings.HasSuffix(result, ";") {
		result = result[:len(result)-1]
	}
	result = strings.TrimSpace(result)
	return result
}

func checkSkipsNoExtraIndexes(tx *sql.Tx) error {
	rows, err := tx.Query(`PRAGMA index_list(recurring_occurrence_skips)`)
	if err != nil {
		return fmt.Errorf("pragma index_list recurring_occurrence_skips: %w", err)
	}
	defer rows.Close()

	for rows.Next() {
		var seq int
		var name, origin string
		var unique, partial int
		if err := rows.Scan(&seq, &name, &unique, &origin, &partial); err != nil {
			return fmt.Errorf("scan index_list: %w", err)
		}
		if origin != "pk" && origin != "u" {
			return fmt.Errorf("recurring_occurrence_skips: unexpected index %q (origin=%q unique=%d)", name, origin, unique)
		}
		if unique != 0 && origin != "pk" {
			return fmt.Errorf("recurring_occurrence_skips: unexpected UNIQUE index %q", name)
		}
	}
	return rows.Err()
}

func checkSkipsNoForeignKeys(tx *sql.Tx) error {
	rows, err := tx.Query(`PRAGMA foreign_key_list(recurring_occurrence_skips)`)
	if err != nil {
		return fmt.Errorf("pragma foreign_key_list recurring_occurrence_skips: %w", err)
	}
	defer rows.Close()

	if rows.Next() {
		return fmt.Errorf("recurring_occurrence_skips: foreign key found, but expected none")
	}
	return rows.Err()
}

func checkSkipsNoTriggers(tx *sql.Tx) error {
	rows, err := tx.Query(`
		SELECT name FROM sqlite_schema
		WHERE type = 'trigger'
		  AND tbl_name = 'recurring_occurrence_skips'
	`)
	if err != nil {
		return fmt.Errorf("query triggers for recurring_occurrence_skips: %w", err)
	}
	defer rows.Close()

	if rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return err
		}
		return fmt.Errorf("recurring_occurrence_skips: trigger %q found, expected none", name)
	}
	return rows.Err()
}

func runMigrations(db *sql.DB) error {
	if _, err := db.Exec(`CREATE TABLE IF NOT EXISTS schema_migrations (
		version INTEGER PRIMARY KEY,
		name TEXT NOT NULL,
		applied_at INTEGER NOT NULL DEFAULT (unixepoch())
	)`); err != nil {
		return fmt.Errorf("create schema_migrations: %w", err)
	}

	for _, m := range migrations {
		applied, err := isMigrationApplied(db, m.version)
		if err != nil {
			return fmt.Errorf("check migration %d: %w", m.version, err)
		}
		if applied {
			continue
		}
		// F10: only pending v12 needs connection-scoped protection against FK
		// delete actions. Already-applied v12 is never replayed or repaired.
		if m.version == 12 {
			if err := runCategoryMigration12(db, m); err != nil {
				return fmt.Errorf("apply migration %d (%s): %w", m.version, m.name, err)
			}
			continue
		}

		if m.version == 35 || m.version == 41 {
			if err := setForeignKeys(db, false); err != nil {
				return fmt.Errorf("disable foreign keys for migration %d (%s) table rebuild: %w", m.version, m.name, err)
			}
		}
		tx, err := db.Begin()
		if err != nil {
			if m.version == 35 || m.version == 41 {
				_ = restoreForeignKeys(db, m)
			}
			return err
		}
		if err := m.up(tx); err != nil {
			_ = tx.Rollback()
			if m.version == 35 || m.version == 41 {
				_ = restoreForeignKeys(db, m)
			}
			return fmt.Errorf("apply migration %d (%s): %w", m.version, m.name, err)
		}
		if _, err := tx.Exec(`INSERT INTO schema_migrations(version, name, applied_at) VALUES (?, ?, unixepoch())`, m.version, m.name); err != nil {
			_ = tx.Rollback()
			if m.version == 35 || m.version == 41 {
				_ = restoreForeignKeys(db, m)
			}
			return fmt.Errorf("register migration %d (%s): %w", m.version, m.name, err)
		}
		if err := tx.Commit(); err != nil {
			if m.version == 35 || m.version == 41 {
				_ = restoreForeignKeys(db, m)
			}
			return err
		}
		if m.version == 35 || m.version == 41 {
			if err := restoreForeignKeys(db, m); err != nil {
				return err
			}
		}
	}
	if err := assertDatabaseForeignKeyIntegrity(db); err != nil {
		return err
	}
	return nil
}

func restoreForeignKeys(db *sql.DB, m migration) error {
	if err := setForeignKeys(db, true); err != nil {
		return fmt.Errorf("restore foreign keys after migration %d (%s): %w", m.version, m.name, err)
	}
	return nil
}

func setForeignKeys(db *sql.DB, enabled bool) error {
	value := "OFF"
	want := 0
	if enabled {
		value = "ON"
		want = 1
	}
	if _, err := db.Exec(`PRAGMA foreign_keys = ` + value); err != nil {
		return err
	}
	var got int
	if err := db.QueryRow(`PRAGMA foreign_keys`).Scan(&got); err != nil {
		return err
	}
	if got != want {
		return fmt.Errorf("foreign_keys remained %d after setting %s", got, value)
	}
	return nil
}

func isMigrationApplied(db *sql.DB, version int) (bool, error) {
	var count int
	if err := db.QueryRow(`SELECT COUNT(1) FROM schema_migrations WHERE version = ?`, version).Scan(&count); err != nil {
		return false, err
	}
	return count > 0, nil
}

func addColumnIfMissing(tx *sql.Tx, table, column, ddl string) error {
	exists, err := columnExists(tx, table, column)
	if err != nil {
		return err
	}
	if exists {
		return nil
	}
	_, err = tx.Exec(fmt.Sprintf("ALTER TABLE %s ADD COLUMN %s %s", table, column, ddl))
	return err
}

func columnExists(tx *sql.Tx, table, column string) (bool, error) {
	rows, err := tx.Query(fmt.Sprintf("PRAGMA table_info(%s)", table))
	if err != nil {
		return false, err
	}
	defer rows.Close()

	for rows.Next() {
		var (
			cid        int
			name       string
			columnType string
			notNull    int
			defaultVal sql.NullString
			pk         int
		)
		if err := rows.Scan(&cid, &name, &columnType, &notNull, &defaultVal, &pk); err != nil {
			return false, err
		}
		if name == column {
			return true, nil
		}
	}
	if err := rows.Err(); err != nil {
		return false, err
	}
	return false, nil
}

func tableExists(tx *sql.Tx, table string) (bool, error) {
	var count int
	if err := tx.QueryRow(`SELECT COUNT(1) FROM sqlite_master WHERE type = 'table' AND name = ?`, table).Scan(&count); err != nil {
		return false, err
	}
	return count > 0, nil
}

// migrateRecurringSeriesVersioningBase adds the recurring-series versioning base schema
// (the recurring_series table plus new columns on recurring_rules and transactions) to an
// already existing database. It is additive only: no table rebuild, no destructive change,
// and no functional behavior (job, fallback, materializer) is enabled here.
//
// INTENTIONAL fresh-vs-migrated constraint divergence (Fase 1):
// On a fresh install, sqlite.go creates the new columns inline WITH FOREIGN KEY constraints:
//   - recurring_rules.series_id     -> recurring_series(id)
//   - recurring_rules.parent_rule_id -> recurring_rules(id)
//   - recurring_rules.contact_id    -> contacts(id)
//   - transactions.series_id        -> recurring_series(id)
//
// On an existing database these columns are added with ALTER TABLE ADD COLUMN, which in SQLite
// CANNOT attach FOREIGN KEY constraints. Because Fase 1 deliberately avoids a destructive table
// rebuild, migrated databases keep these columns as plain TEXT/INTEGER WITHOUT the FKs that a
// fresh install gets. This divergence is known and accepted for now. It is pinned by the
// TestMigration50ForeignKeyList* tests and is compensated by the duplicate preflight below,
// the indexes created here, and application-level validation planned for later phases (a future
// phase may rebuild the tables to converge the constraints).
func migrateRecurringSeriesVersioningBase(tx *sql.Tx) error {
	if _, err := tx.Exec(`
		CREATE TABLE IF NOT EXISTS recurring_series (
			id                TEXT PRIMARY KEY,
			workspace_id      TEXT NOT NULL,
			user_id           TEXT,
			active            INTEGER NOT NULL DEFAULT 1 CHECK (active IN (0, 1)),
			total_occurrences INTEGER,
			created_at        INTEGER NOT NULL DEFAULT (unixepoch()),
			updated_at        INTEGER NOT NULL DEFAULT (unixepoch()),
			FOREIGN KEY (workspace_id) REFERENCES workspaces(id) ON DELETE CASCADE,
			FOREIGN KEY (user_id)      REFERENCES users(id)      ON DELETE SET NULL
		)
	`); err != nil {
		return fmt.Errorf("create recurring_series: %w", err)
	}

	rulesExist, err := tableExists(tx, "recurring_rules")
	if err != nil {
		return err
	}
	if !rulesExist {
		return nil
	}
	// FK-divergent columns: on a fresh schema (sqlite.go) series_id, parent_rule_id and
	// contact_id carry FOREIGN KEY constraints; here they are plain columns because ALTER
	// TABLE ADD COLUMN cannot attach FKs and Fase 1 does not rebuild the table. See the
	// function doc for the accepted fresh-vs-migrated divergence.
	if err := addColumnIfMissing(tx, "recurring_rules", "series_id", "TEXT"); err != nil {
		return fmt.Errorf("add recurring_rules.series_id: %w", err)
	}
	if err := addColumnIfMissing(tx, "recurring_rules", "parent_rule_id", "TEXT"); err != nil {
		return fmt.Errorf("add recurring_rules.parent_rule_id: %w", err)
	}
	if err := addColumnIfMissing(tx, "recurring_rules", "effective_start_sequence", "INTEGER NOT NULL DEFAULT 1"); err != nil {
		return fmt.Errorf("add recurring_rules.effective_start_sequence: %w", err)
	}
	if err := addColumnIfMissing(tx, "recurring_rules", "effective_end_sequence", "INTEGER"); err != nil {
		return fmt.Errorf("add recurring_rules.effective_end_sequence: %w", err)
	}
	if err := addColumnIfMissing(tx, "recurring_rules", "starts_at", "INTEGER"); err != nil {
		return fmt.Errorf("add recurring_rules.starts_at: %w", err)
	}
	if err := addColumnIfMissing(tx, "recurring_rules", "end_date", "INTEGER"); err != nil {
		return fmt.Errorf("add recurring_rules.end_date: %w", err)
	}
	if err := addColumnIfMissing(tx, "recurring_rules", "contact_id", "TEXT"); err != nil {
		return fmt.Errorf("add recurring_rules.contact_id: %w", err)
	}
	if err := addColumnIfMissing(tx, "recurring_rules", "due_date_policy", "TEXT"); err != nil {
		return fmt.Errorf("add recurring_rules.due_date_policy: %w", err)
	}
	if err := addColumnIfMissing(tx, "recurring_rules", "due_date_offset_days", "INTEGER"); err != nil {
		return fmt.Errorf("add recurring_rules.due_date_offset_days: %w", err)
	}

	transactionsExist, err := tableExists(tx, "transactions")
	if err != nil {
		return err
	}
	if transactionsExist {
		// FK-divergent column: transactions.series_id references recurring_series(id) on a
		// fresh schema, but is added here without the FK (see function doc).
		if err := addColumnIfMissing(tx, "transactions", "series_id", "TEXT"); err != nil {
			return fmt.Errorf("add transactions.series_id: %w", err)
		}
		if err := addColumnIfMissing(tx, "transactions", "is_recurring_exception", "INTEGER NOT NULL DEFAULT 0 CHECK (is_recurring_exception IN (0, 1))"); err != nil {
			return fmt.Errorf("add transactions.is_recurring_exception: %w", err)
		}
		if err := addColumnIfMissing(tx, "transactions", "recurring_exception_reason", "TEXT"); err != nil {
			return fmt.Errorf("add transactions.recurring_exception_reason: %w", err)
		}
	}

	if _, err := tx.Exec(`
		INSERT OR IGNORE INTO recurring_series (
			id,
			workspace_id,
			user_id,
			active,
			total_occurrences,
			created_at,
			updated_at
		)
		SELECT
			rr.id,
			rr.workspace_id,
			rr.user_id,
			rr.active,
			rr.total_occurrences,
			COALESCE(rr.created_at, unixepoch()),
			COALESCE(rr.updated_at, rr.created_at, unixepoch())
		FROM recurring_rules rr
		WHERE rr.id IS NOT NULL
	`); err != nil {
		return fmt.Errorf("backfill recurring_series: %w", err)
	}

	if _, err := tx.Exec(`
		UPDATE recurring_rules
		SET series_id = id,
			effective_start_sequence = COALESCE(effective_start_sequence, 1)
		WHERE series_id IS NULL
	`); err != nil {
		return fmt.Errorf("backfill recurring_rules.series_id: %w", err)
	}

	if transactionsExist {
		if _, err := tx.Exec(`
			UPDATE transactions
			SET series_id = (
				SELECT rr.series_id
				FROM recurring_rules rr
				WHERE rr.id = transactions.recurring_rule_id
				  AND rr.workspace_id = transactions.workspace_id
			)
			WHERE recurring_rule_id IS NOT NULL
			  AND series_id IS NULL
		`); err != nil {
			return fmt.Errorf("backfill transactions.series_id: %w", err)
		}

		if err := preflightRecurringSequenceDuplicates(tx); err != nil {
			return err
		}
	}

	stmts := []string{
		`CREATE INDEX IF NOT EXISTS idx_recurring_series_workspace ON recurring_series(workspace_id)`,
		`CREATE INDEX IF NOT EXISTS idx_recurring_rules_workspace_series_start ON recurring_rules(workspace_id, series_id, effective_start_sequence)`,
		`CREATE INDEX IF NOT EXISTS idx_recurring_rules_workspace_series_dates ON recurring_rules(workspace_id, series_id, starts_at, end_date)`,
	}
	if transactionsExist {
		stmts = append(stmts,
			`CREATE UNIQUE INDEX IF NOT EXISTS idx_transactions_workspace_recurring_sequence_unique ON transactions(workspace_id, recurring_rule_id, recurrence_sequence) WHERE recurring_rule_id IS NOT NULL AND recurrence_sequence IS NOT NULL`,
			`CREATE INDEX IF NOT EXISTS idx_transactions_workspace_series_sequence ON transactions(workspace_id, series_id, recurrence_sequence)`,
			`CREATE INDEX IF NOT EXISTS idx_transactions_workspace_series_date ON transactions(workspace_id, series_id, date)`,
			`CREATE INDEX IF NOT EXISTS idx_transactions_workspace_series ON transactions(workspace_id, series_id)`,
			`CREATE INDEX IF NOT EXISTS idx_transactions_workspace_recurring_sequence ON transactions(workspace_id, recurring_rule_id, recurrence_sequence)`,
		)
	}
	for _, stmt := range stmts {
		if _, err := tx.Exec(stmt); err != nil {
			return fmt.Errorf("create recurring series versioning index: %w", err)
		}
	}
	return nil
}

func migrateRecurringMaterializationState(tx *sql.Tx) error {
	if _, err := tx.Exec(`
		CREATE TABLE IF NOT EXISTS recurring_materialization_state (
			workspace_id       TEXT PRIMARY KEY,
			materialized_until INTEGER NOT NULL DEFAULT 0,
			last_attempt_at    INTEGER NOT NULL DEFAULT 0,
			last_success_at    INTEGER NOT NULL DEFAULT 0,
			last_error         TEXT,
			status             TEXT NOT NULL DEFAULT 'idle' CHECK (status IN ('idle', 'running', 'error')),
			locked_until       INTEGER NOT NULL DEFAULT 0,
			lock_owner         TEXT,
			created_at         INTEGER NOT NULL DEFAULT (unixepoch()),
			updated_at         INTEGER NOT NULL DEFAULT (unixepoch()),
			FOREIGN KEY (workspace_id) REFERENCES workspaces(id) ON DELETE CASCADE
		)
	`); err != nil {
		return fmt.Errorf("create recurring_materialization_state: %w", err)
	}
	if _, err := tx.Exec(`
		CREATE INDEX IF NOT EXISTS idx_recurring_materialization_state_locked_until
		ON recurring_materialization_state(locked_until)
	`); err != nil {
		return fmt.Errorf("create idx_recurring_materialization_state_locked_until: %w", err)
	}
	return nil
}

// migrateCivilDateColumns adds nullable civil-date companion columns (ISO YYYY-MM-DD stored as
// TEXT) alongside the existing legacy INTEGER date columns. It is strictly additive:
//   - every new column is nullable, has no DEFAULT and no CHECK constraint;
//   - no backfill runs, so the new columns stay NULL until a later phase populates them;
//   - no legacy INTEGER column is read, written, altered or dropped;
//   - no table is rebuilt.
//
// Because the columns are only appended, a rollback to a previous application version simply
// ignores the unknown columns (SQLite keeps them; older code never selects them).
//
// Index policy: composite (workspace_id, <civil>) indexes are created only on tables that carry
// workspace_id directly (transactions, recurring_rules, recurring_materialization_state,
// invoice_payments). The invoices table has no workspace_id column — it scopes through account_id
// — so its civil indexes are single-column instead of inventing a workspace_id index.
func migrateCivilDateColumns(tx *sql.Tx) error {
	additions := []struct {
		table   string
		columns []string
	}{
		{table: "transactions", columns: []string{"date_civil", "due_date_civil", "paid_date_civil", "scheduled_payment_date_civil"}},
		{table: "recurring_rules", columns: []string{"start_date_civil", "starts_at_civil", "end_date_civil", "generated_until_civil"}},
		{table: "recurring_materialization_state", columns: []string{"materialized_until_civil"}},
		{table: "invoice_payments", columns: []string{"paid_date_civil"}},
		{table: "invoices", columns: []string{"closing_date_civil", "due_date_civil"}},
	}
	for _, addition := range additions {
		exists, err := tableExists(tx, addition.table)
		if err != nil {
			return fmt.Errorf("check %s existence: %w", addition.table, err)
		}
		if !exists {
			continue
		}
		for _, column := range addition.columns {
			if err := addColumnIfMissing(tx, addition.table, column, "TEXT"); err != nil {
				return fmt.Errorf("add %s.%s: %w", addition.table, column, err)
			}
		}
	}

	indexes := []struct {
		table string
		stmt  string
	}{
		{table: "transactions", stmt: `CREATE INDEX IF NOT EXISTS idx_transactions_workspace_date_civil ON transactions(workspace_id, date_civil)`},
		{table: "transactions", stmt: `CREATE INDEX IF NOT EXISTS idx_transactions_workspace_due_date_civil ON transactions(workspace_id, due_date_civil)`},
		{table: "transactions", stmt: `CREATE INDEX IF NOT EXISTS idx_transactions_workspace_paid_date_civil ON transactions(workspace_id, paid_date_civil)`},
		{table: "transactions", stmt: `CREATE INDEX IF NOT EXISTS idx_transactions_workspace_scheduled_payment_date_civil ON transactions(workspace_id, scheduled_payment_date_civil)`},
		{table: "recurring_rules", stmt: `CREATE INDEX IF NOT EXISTS idx_recurring_rules_workspace_start_date_civil ON recurring_rules(workspace_id, start_date_civil)`},
		{table: "recurring_rules", stmt: `CREATE INDEX IF NOT EXISTS idx_recurring_rules_workspace_starts_at_civil ON recurring_rules(workspace_id, starts_at_civil)`},
		{table: "recurring_rules", stmt: `CREATE INDEX IF NOT EXISTS idx_recurring_rules_workspace_end_date_civil ON recurring_rules(workspace_id, end_date_civil)`},
		{table: "recurring_materialization_state", stmt: `CREATE INDEX IF NOT EXISTS idx_recurring_materialization_state_workspace_materialized_until_civil ON recurring_materialization_state(workspace_id, materialized_until_civil)`},
		{table: "invoice_payments", stmt: `CREATE INDEX IF NOT EXISTS idx_invoice_payments_workspace_paid_date_civil ON invoice_payments(workspace_id, paid_date_civil)`},
		{table: "invoices", stmt: `CREATE INDEX IF NOT EXISTS idx_invoices_closing_date_civil ON invoices(closing_date_civil)`},
		{table: "invoices", stmt: `CREATE INDEX IF NOT EXISTS idx_invoices_due_date_civil ON invoices(due_date_civil)`},
	}
	for _, index := range indexes {
		exists, err := tableExists(tx, index.table)
		if err != nil {
			return fmt.Errorf("check %s existence for index: %w", index.table, err)
		}
		if !exists {
			continue
		}
		if _, err := tx.Exec(index.stmt); err != nil {
			return fmt.Errorf("create civil date index on %s: %w", index.table, err)
		}
	}
	return nil
}

// idx_transactions_workspace_recurring_sequence_unique BEFORE it is created. Without this
// check a duplicate would surface only as an opaque "UNIQUE constraint failed" error while
// creating the index. Instead this fails the migration with an actionable message that points
// to the offending (workspace_id, recurring_rule_id, recurrence_sequence) combination so the
// operator can resolve the real duplicate before upgrading.
func preflightRecurringSequenceDuplicates(tx *sql.Tx) error {
	rows, err := tx.Query(`
		SELECT workspace_id, recurring_rule_id, recurrence_sequence, COUNT(1) AS dup_count
		FROM transactions
		WHERE recurring_rule_id IS NOT NULL
		  AND recurrence_sequence IS NOT NULL
		GROUP BY workspace_id, recurring_rule_id, recurrence_sequence
		HAVING COUNT(1) > 1
		ORDER BY dup_count DESC, workspace_id, recurring_rule_id, recurrence_sequence
	`)
	if err != nil {
		return fmt.Errorf("preflight recurring sequence duplicates: %w", err)
	}
	defer rows.Close()

	var (
		groups      int
		sampleWs    string
		sampleRule  string
		sampleSeq   int64
		sampleCount int
	)
	for rows.Next() {
		var (
			ws    string
			rule  string
			seq   int64
			count int
		)
		if err := rows.Scan(&ws, &rule, &seq, &count); err != nil {
			return fmt.Errorf("preflight recurring sequence duplicates scan: %w", err)
		}
		if groups == 0 {
			sampleWs, sampleRule, sampleSeq, sampleCount = ws, rule, seq, count
		}
		groups++
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("preflight recurring sequence duplicates rows: %w", err)
	}
	if groups > 0 {
		return fmt.Errorf(
			"migration 50 aborted: found %d duplicate (workspace_id, recurring_rule_id, recurrence_sequence) group(s) in transactions that violate the unique recurring sequence index; example workspace_id=%q recurring_rule_id=%q recurrence_sequence=%d repeats %d times; resolve these duplicate recurring occurrences before upgrading",
			groups, sampleWs, sampleRule, sampleSeq, sampleCount,
		)
	}
	return nil
}

func tableSchemaSQL(tx *sql.Tx, table string) ([]string, error) {
	rows, err := tx.Query(`
		SELECT sql
		FROM sqlite_master
		WHERE tbl_name = ?
			AND type IN ('index', 'trigger')
			AND sql IS NOT NULL
		ORDER BY type, name
	`, table)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var stmts []string
	for rows.Next() {
		var sqlText string
		if err := rows.Scan(&sqlText); err != nil {
			return nil, err
		}
		stmts = append(stmts, sqlText)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return stmts, nil
}

func prepareAccountsMigration35Compatibility(tx *sql.Tx) error {
	var orphanCount int
	if err := tx.QueryRow(`
		SELECT COUNT(1)
		FROM accounts a
		WHERE NOT EXISTS (
			SELECT 1 FROM workspaces w WHERE w.id = a.workspace_id
		)
	`).Scan(&orphanCount); err != nil {
		return err
	}
	if orphanCount == 0 {
		return nil
	}
	var fallbackWorkspaceID string
	if err := tx.QueryRow(`SELECT id FROM workspaces ORDER BY id ASC LIMIT 1`).Scan(&fallbackWorkspaceID); err != nil {
		if err == sql.ErrNoRows {
			return fmt.Errorf("accounts migration compatibility requires at least one workspace for %d orphan accounts", orphanCount)
		}
		return err
	}
	if _, err := tx.Exec(`
		UPDATE accounts
		SET workspace_id = ?
		WHERE NOT EXISTS (
			SELECT 1 FROM workspaces w WHERE w.id = accounts.workspace_id
		)
	`, fallbackWorkspaceID); err != nil {
		return err
	}
	return nil
}

func assertNoForeignKeyViolations(tx *sql.Tx) error {
	rows, err := tx.Query(`PRAGMA foreign_key_check`)
	if err != nil {
		return err
	}
	defer rows.Close()
	if rows.Next() {
		var table string
		var rowID sql.NullInt64
		var parent string
		var fkID int
		if err := rows.Scan(&table, &rowID, &parent, &fkID); err != nil {
			return err
		}
		if rowID.Valid {
			return fmt.Errorf("foreign_key_check failed: table=%s rowid=%d parent=%s fk=%d", table, rowID.Int64, parent, fkID)
		}
		return fmt.Errorf("foreign_key_check failed: table=%s rowid=NULL parent=%s fk=%d", table, parent, fkID)
	}
	return rows.Err()
}

func assertDatabaseForeignKeyIntegrity(db *sql.DB) error {
	rows, err := db.Query(`PRAGMA foreign_key_check`)
	if err != nil {
		return fmt.Errorf("foreign_key_check: %w", err)
	}
	defer rows.Close()
	if rows.Next() {
		var table string
		var rowID sql.NullInt64
		var parent string
		var fkID int
		if err := rows.Scan(&table, &rowID, &parent, &fkID); err != nil {
			return fmt.Errorf("foreign_key_check scan: %w", err)
		}
		if rowID.Valid {
			return fmt.Errorf("foreign_key_check failed: table=%s rowid=%d parent=%s fk=%d", table, rowID.Int64, parent, fkID)
		}
		return fmt.Errorf("foreign_key_check failed: table=%s rowid=NULL parent=%s fk=%d", table, parent, fkID)
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("foreign_key_check rows: %w", err)
	}
	return nil
}
