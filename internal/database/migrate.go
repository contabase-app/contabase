package database

import (
	"database/sql"
	"fmt"
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
	if int64(t.Day()) > closingDay {
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
			if err := addColumnIfMissing(tx, "cost_limits", "created_at", "INTEGER NOT NULL DEFAULT (unixepoch())"); err != nil {
				return err
			}
			return addColumnIfMissing(tx, "cost_limits", "updated_at", "INTEGER NOT NULL DEFAULT (unixepoch())")
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

		if m.version == 35 {
			if err := setForeignKeys(db, false); err != nil {
				return fmt.Errorf("disable foreign keys for migration %d (%s) table rebuild: %w", m.version, m.name, err)
			}
		}
		tx, err := db.Begin()
		if err != nil {
			if m.version == 35 {
				_ = restoreForeignKeys(db, m)
			}
			return err
		}
		if err := m.up(tx); err != nil {
			_ = tx.Rollback()
			if m.version == 35 {
				_ = restoreForeignKeys(db, m)
			}
			return fmt.Errorf("apply migration %d (%s): %w", m.version, m.name, err)
		}
		if _, err := tx.Exec(`INSERT INTO schema_migrations(version, name, applied_at) VALUES (?, ?, unixepoch())`, m.version, m.name); err != nil {
			_ = tx.Rollback()
			if m.version == 35 {
				_ = restoreForeignKeys(db, m)
			}
			return fmt.Errorf("register migration %d (%s): %w", m.version, m.name, err)
		}
		if err := tx.Commit(); err != nil {
			if m.version == 35 {
				_ = restoreForeignKeys(db, m)
			}
			return err
		}
		if m.version == 35 {
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
