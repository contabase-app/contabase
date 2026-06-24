package database

import (
	"database/sql"
	"database/sql/driver"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
	"unicode"

	"github.com/google/uuid"
	"golang.org/x/text/runes"
	"golang.org/x/text/transform"
	"golang.org/x/text/unicode/norm"
	"modernc.org/sqlite"

	"github.com/contabase-app/contabase/internal/paths"
)

func RemoveAccents(s string) string {
	t := transform.Chain(norm.NFD, runes.Remove(runes.In(unicode.Mn)), norm.NFC)
	result, _, _ := transform.String(t, s)
	return strings.ToLower(result)
}

func init() {
	sqlite.MustRegisterDeterministicScalarFunction("UNACCENT", 1, func(ctx *sqlite.FunctionContext, args []driver.Value) (driver.Value, error) {
		if len(args) != 1 || args[0] == nil {
			return nil, nil
		}
		var s string
		switch v := args[0].(type) {
		case string:
			s = v
		case []byte:
			s = string(v)
		default:
			s = fmt.Sprintf("%v", v)
		}
		return RemoveAccents(s), nil
	})
}

var ddl = `
PRAGMA journal_mode = WAL;
PRAGMA foreign_keys = ON;
PRAGMA synchronous = NORMAL;

CREATE TABLE IF NOT EXISTS users (
    id              TEXT PRIMARY KEY,
    name            TEXT NOT NULL,
    email           TEXT NOT NULL UNIQUE,
    password_hash   TEXT NOT NULL,
    profile_photo_path TEXT NOT NULL DEFAULT '',
    default_workspace_id TEXT REFERENCES workspaces(id) ON DELETE SET NULL,
    status          TEXT NOT NULL DEFAULT 'active' CHECK (status IN ('active', 'pending')),
    activation_token_hash TEXT,
    activation_expires_at INTEGER,
    must_change_password INTEGER NOT NULL DEFAULT 0 CHECK (must_change_password IN (0, 1)),
    temporary_password_expires_at INTEGER,
    totp_enabled    INTEGER NOT NULL DEFAULT 0 CHECK (totp_enabled IN (0, 1)),
    totp_secret_enc TEXT,
    totp_backup_codes TEXT NOT NULL DEFAULT '[]',
    totp_enabled_at INTEGER,
    last_notifications_clear_at INTEGER NOT NULL DEFAULT 0,
    created_at      INTEGER NOT NULL DEFAULT (unixepoch()),
    updated_at      INTEGER NOT NULL DEFAULT (unixepoch())
);

CREATE TABLE IF NOT EXISTS workspaces (
    id           TEXT PRIMARY KEY,
    name         TEXT NOT NULL,
    description  TEXT NOT NULL DEFAULT '',
    type         TEXT NOT NULL DEFAULT 'personal' CHECK (type IN ('personal', 'business')),
    theme_token  TEXT NOT NULL DEFAULT 'violeta',
    company_name   TEXT,
    cnpj_cpf       TEXT,
    address        TEXT,
    phone          TEXT,
    logo_light_url TEXT,
    logo_dark_url  TEXT,
    smtp_host      TEXT,
    smtp_port      INTEGER NOT NULL DEFAULT 587,
    smtp_user      TEXT,
    smtp_pass      TEXT,
    notification_email TEXT,
    email_preferences TEXT NOT NULL DEFAULT '[]',
    created_at     INTEGER NOT NULL DEFAULT (unixepoch()),
    updated_at     INTEGER NOT NULL DEFAULT (unixepoch())
);

CREATE TABLE IF NOT EXISTS workspace_members (
    workspace_id TEXT NOT NULL,
    user_id      TEXT NOT NULL,
    role         TEXT NOT NULL DEFAULT 'USER' CHECK (role IN ('ADMIN', 'MANAGER', 'USER')),
    custom_permissions TEXT NOT NULL DEFAULT '[]',
    joined_at    INTEGER NOT NULL DEFAULT (unixepoch()),
    PRIMARY KEY (workspace_id, user_id),
    FOREIGN KEY (workspace_id) REFERENCES workspaces(id) ON DELETE CASCADE,
    FOREIGN KEY (user_id)      REFERENCES users(id)      ON DELETE CASCADE
);

CREATE TABLE IF NOT EXISTS user_notification_dismissals (
    user_id       TEXT NOT NULL,
    workspace_id  TEXT NOT NULL,
    notification_key TEXT NOT NULL,
    dismissed_at  INTEGER NOT NULL DEFAULT (unixepoch()),
    PRIMARY KEY (user_id, workspace_id, notification_key),
    FOREIGN KEY (user_id)      REFERENCES users(id)      ON DELETE CASCADE,
    FOREIGN KEY (workspace_id) REFERENCES workspaces(id) ON DELETE CASCADE
);

CREATE TABLE IF NOT EXISTS sessions (
    id           TEXT PRIMARY KEY,
    user_id      TEXT NOT NULL,
    workspace_id TEXT NOT NULL,
    token_hash   TEXT NOT NULL UNIQUE,
    expires_at   INTEGER NOT NULL,
    revoked_at   INTEGER,
    created_at   INTEGER NOT NULL DEFAULT (unixepoch()),
    FOREIGN KEY (user_id) REFERENCES users(id) ON DELETE CASCADE,
    FOREIGN KEY (workspace_id) REFERENCES workspaces(id) ON DELETE CASCADE
);

CREATE TABLE IF NOT EXISTS pre_auth_sessions (
    id           TEXT PRIMARY KEY,
    user_id      TEXT NOT NULL,
    token_hash   TEXT NOT NULL UNIQUE,
    method       TEXT NOT NULL CHECK (method IN ('TOTP')),
    expires_at   INTEGER NOT NULL,
    consumed_at  INTEGER,
    created_at   INTEGER NOT NULL DEFAULT (unixepoch()),
    FOREIGN KEY (user_id) REFERENCES users(id) ON DELETE CASCADE
);

CREATE TABLE IF NOT EXISTS auth_audit_events (
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
);

CREATE TABLE IF NOT EXISTS auth_lockouts (
    user_id               TEXT PRIMARY KEY,
    failed_password_count INTEGER NOT NULL DEFAULT 0 CHECK (failed_password_count >= 0),
    failed_2fa_count      INTEGER NOT NULL DEFAULT 0 CHECK (failed_2fa_count >= 0),
    first_failed_at       INTEGER NOT NULL DEFAULT 0,
    last_failed_at        INTEGER NOT NULL DEFAULT 0,
    locked_until          INTEGER NOT NULL DEFAULT 0,
    lock_reason           TEXT NOT NULL DEFAULT '' CHECK (lock_reason IN ('', 'password', 'totp')),
    updated_at            INTEGER NOT NULL DEFAULT (unixepoch()),
    FOREIGN KEY (user_id) REFERENCES users(id) ON DELETE CASCADE
);

CREATE TABLE IF NOT EXISTS security_logs (
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
);

CREATE TABLE IF NOT EXISTS user_notifications (
    id         TEXT PRIMARY KEY,
    user_id    TEXT NOT NULL,
    title      TEXT NOT NULL DEFAULT '',
    message    TEXT NOT NULL DEFAULT '',
    type       TEXT NOT NULL DEFAULT '',
    is_read    INTEGER NOT NULL DEFAULT 0 CHECK (is_read IN (0, 1)),
    created_at INTEGER NOT NULL DEFAULT (unixepoch()),
    FOREIGN KEY (user_id) REFERENCES users(id) ON DELETE CASCADE
);

CREATE TABLE IF NOT EXISTS accounts (
    id              TEXT PRIMARY KEY,
    workspace_id    TEXT    NOT NULL,
    name            TEXT    NOT NULL,
    type            TEXT    NOT NULL CHECK (type IN ('CHECKING', 'SAVINGS', 'INVESTMENT', 'WALLET', 'CREDIT_CARD')),
    color           TEXT    NOT NULL DEFAULT '#6B7280',
    icon            TEXT    NOT NULL DEFAULT '',
    provider_slug   TEXT    NOT NULL DEFAULT 'custom',
    initial_balance INTEGER NOT NULL DEFAULT 0,
    current_balance INTEGER NOT NULL DEFAULT 0,
    archived_at     INTEGER NULL,
    created_at      INTEGER NOT NULL DEFAULT (unixepoch()),
    updated_at      INTEGER NOT NULL DEFAULT (unixepoch()),
    FOREIGN KEY (workspace_id) REFERENCES workspaces(id) ON DELETE CASCADE
);

CREATE TABLE IF NOT EXISTS credit_cards (
    id           TEXT PRIMARY KEY,
    account_id   TEXT    NOT NULL UNIQUE,
    closing_day  INTEGER NOT NULL CHECK (closing_day BETWEEN 1 AND 31),
    due_day      INTEGER NOT NULL CHECK (due_day BETWEEN 1 AND 31),
    credit_limit INTEGER NOT NULL DEFAULT 0,
    FOREIGN KEY (account_id) REFERENCES accounts(id) ON DELETE CASCADE
);

CREATE TABLE IF NOT EXISTS categories (
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
    FOREIGN KEY (parent_id) REFERENCES categories(id) ON DELETE CASCADE
);

CREATE TABLE IF NOT EXISTS invoices (
    id            TEXT PRIMARY KEY,
    account_id    TEXT NOT NULL,
    reference     TEXT NOT NULL,
    closing_date  INTEGER NOT NULL,
    due_date      INTEGER NOT NULL,
    status        TEXT NOT NULL DEFAULT 'OPEN' CHECK (status IN ('OPEN', 'CLOSED', 'PAID')),
    paid_at       INTEGER,
    paid_amount   INTEGER,
    created_at    INTEGER NOT NULL DEFAULT (unixepoch()),
    FOREIGN KEY (account_id) REFERENCES accounts(id) ON DELETE CASCADE,
    UNIQUE (account_id, reference)
);

CREATE TABLE IF NOT EXISTS recurring_rules (
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
    total_occurrences       INTEGER,
    generated_until         INTEGER,
    created_at              INTEGER NOT NULL DEFAULT (unixepoch()),
    updated_at              INTEGER NOT NULL DEFAULT (unixepoch()),
    FOREIGN KEY (workspace_id)           REFERENCES workspaces(id)  ON DELETE CASCADE,
    FOREIGN KEY (user_id)                REFERENCES users(id)       ON DELETE RESTRICT,
    FOREIGN KEY (account_id)             REFERENCES accounts(id)    ON DELETE RESTRICT,
    FOREIGN KEY (destination_account_id) REFERENCES accounts(id)    ON DELETE SET NULL,
    FOREIGN KEY (category_id)            REFERENCES categories(id)  ON DELETE SET NULL
);

CREATE TABLE IF NOT EXISTS contacts (
    id           TEXT PRIMARY KEY,
    workspace_id TEXT NOT NULL,
    custom_client_id TEXT,
    name         TEXT NOT NULL,
    document     TEXT NOT NULL DEFAULT '',
    type         TEXT NOT NULL CHECK (type IN ('client', 'vendor')),
    email        TEXT NOT NULL DEFAULT '',
    phone        TEXT NOT NULL DEFAULT '',
    created_at   INTEGER NOT NULL DEFAULT (unixepoch()),
    FOREIGN KEY (workspace_id) REFERENCES workspaces(id) ON DELETE CASCADE,
    UNIQUE (workspace_id, custom_client_id)
);

CREATE TABLE IF NOT EXISTS transactions (
    id                      TEXT PRIMARY KEY,
    workspace_id            TEXT    NOT NULL,
    user_id                 TEXT    NOT NULL,
    account_id              TEXT    NOT NULL,
    destination_account_id  TEXT,
    category_id             TEXT,
    invoice_id              TEXT,
    invoice_override_id     TEXT,
    type                    TEXT    NOT NULL CHECK (type IN ('EXPENSE', 'INCOME', 'TRANSFER')),
    amount                  INTEGER NOT NULL CHECK (amount > 0),
    date                    INTEGER NOT NULL,
    description             TEXT    NOT NULL DEFAULT '',
    notes                   TEXT    NOT NULL DEFAULT '',
    attachment_path         TEXT    NOT NULL DEFAULT '',
    installment_number      INTEGER NOT NULL DEFAULT 1,
    total_installments      INTEGER NOT NULL DEFAULT 1,
    parent_id               TEXT,
    recurring_rule_id       TEXT,
    recurrence_sequence     INTEGER,
    -- LEGADO INATIVO: manter somente para compatibilidade e backfill da migration v11.
    -- @deprecated v20: use 'status' column exclusively.
    payment_status          TEXT    NOT NULL DEFAULT 'PAID' CHECK (payment_status IN ('PAID', 'PENDING')),
    status                  TEXT    NOT NULL DEFAULT 'paid' CHECK (status IN ('paid', 'pending')),
    due_date                INTEGER,
    contact_id              TEXT REFERENCES contacts(id) ON DELETE RESTRICT,
    created_at              INTEGER NOT NULL DEFAULT (unixepoch()),
    updated_at              INTEGER NOT NULL DEFAULT (unixepoch()),
    FOREIGN KEY (workspace_id)           REFERENCES workspaces(id)  ON DELETE CASCADE,
    FOREIGN KEY (user_id)                REFERENCES users(id)       ON DELETE RESTRICT,
    FOREIGN KEY (account_id)             REFERENCES accounts(id)    ON DELETE RESTRICT,
    FOREIGN KEY (destination_account_id) REFERENCES accounts(id)    ON DELETE SET NULL,
    FOREIGN KEY (category_id)            REFERENCES categories(id)  ON DELETE SET NULL,
    FOREIGN KEY (invoice_id)             REFERENCES invoices(id)    ON DELETE SET NULL,
    FOREIGN KEY (invoice_override_id)    REFERENCES invoices(id)    ON DELETE SET NULL,
    FOREIGN KEY (parent_id)              REFERENCES transactions(id) ON DELETE SET NULL,
    FOREIGN KEY (recurring_rule_id)      REFERENCES recurring_rules(id) ON DELETE SET NULL
);

CREATE TABLE IF NOT EXISTS cost_limits (
    id                 TEXT PRIMARY KEY,
    workspace_id       TEXT    NOT NULL,
    category_id        TEXT    NOT NULL,
    max_amount_monthly INTEGER NOT NULL CHECK (max_amount_monthly > 0),
    alert_threshold    INTEGER NOT NULL DEFAULT 0,
    FOREIGN KEY (workspace_id) REFERENCES workspaces(id) ON DELETE CASCADE,
    FOREIGN KEY (category_id)  REFERENCES categories(id) ON DELETE CASCADE,
    UNIQUE (workspace_id, category_id)
);

CREATE TABLE IF NOT EXISTS boxes (
    id               TEXT PRIMARY KEY,
    workspace_id     TEXT    NOT NULL,
    name             TEXT    NOT NULL,
    description      TEXT    NOT NULL DEFAULT '',
    category_id      TEXT    NOT NULL,
    target_amount    INTEGER NOT NULL DEFAULT 0,
    monthly_recharge INTEGER NOT NULL DEFAULT 0,
    target_date      INTEGER,
    monthly_yield_rate REAL  NOT NULL DEFAULT 0.0,
    created_at       INTEGER NOT NULL DEFAULT (unixepoch()),
    updated_at       INTEGER NOT NULL DEFAULT (unixepoch()),
    FOREIGN KEY (workspace_id) REFERENCES workspaces(id) ON DELETE CASCADE,
    FOREIGN KEY (category_id)  REFERENCES categories(id) ON DELETE CASCADE
);

CREATE TABLE IF NOT EXISTS box_virtual_ledger (
    id             TEXT PRIMARY KEY,
    box_id         TEXT    NOT NULL,
    amount         INTEGER NOT NULL CHECK (
        (type IN ('RECHARGE', 'BONUS', 'REVERSAL') AND amount > 0) OR
        (type IN ('RELEASE', 'CONSUME') AND amount < 0)
    ),
    type           TEXT    NOT NULL CHECK (type IN ('RECHARGE', 'BONUS', 'RELEASE', 'CONSUME', 'REVERSAL')),
    description    TEXT    NOT NULL DEFAULT '',
    source_transaction_id TEXT,
    reversal_of_ledger_id TEXT,
    reference_date INTEGER NOT NULL,
    created_at     INTEGER NOT NULL DEFAULT (unixepoch()),
    FOREIGN KEY (box_id) REFERENCES boxes(id) ON DELETE CASCADE,
    FOREIGN KEY (reversal_of_ledger_id) REFERENCES box_virtual_ledger(id) ON DELETE SET NULL
);

CREATE INDEX IF NOT EXISTS idx_workspace_members_user      ON workspace_members(user_id);
CREATE INDEX IF NOT EXISTS idx_user_notification_dismissals_workspace ON user_notification_dismissals(workspace_id, user_id);
CREATE INDEX IF NOT EXISTS idx_sessions_user_id        ON sessions(user_id);
CREATE INDEX IF NOT EXISTS idx_sessions_expires_at     ON sessions(expires_at);
CREATE INDEX IF NOT EXISTS idx_pre_auth_sessions_user_id ON pre_auth_sessions(user_id);
CREATE INDEX IF NOT EXISTS idx_pre_auth_sessions_expires_at ON pre_auth_sessions(expires_at);
CREATE INDEX IF NOT EXISTS idx_auth_audit_events_user_created ON auth_audit_events(user_id, created_at DESC);
CREATE INDEX IF NOT EXISTS idx_auth_audit_events_type_created ON auth_audit_events(event_type, created_at DESC);
CREATE INDEX IF NOT EXISTS idx_auth_lockouts_locked_until ON auth_lockouts(locked_until);
CREATE INDEX IF NOT EXISTS idx_security_logs_workspace_created ON security_logs(workspace_id, created_at DESC);
CREATE INDEX IF NOT EXISTS idx_security_logs_event_created ON security_logs(event_type, created_at DESC);
CREATE INDEX IF NOT EXISTS idx_user_notifications_user_read ON user_notifications(user_id, is_read);
CREATE INDEX IF NOT EXISTS idx_accounts_workspace          ON accounts(workspace_id);
CREATE INDEX IF NOT EXISTS idx_categories_workspace        ON categories(workspace_id);
CREATE INDEX IF NOT EXISTS idx_invoices_account            ON invoices(account_id);
CREATE INDEX IF NOT EXISTS idx_invoices_status             ON invoices(status);
CREATE INDEX IF NOT EXISTS idx_invoices_account_reference_status ON invoices(account_id, reference, status);
CREATE INDEX IF NOT EXISTS idx_recurring_rules_workspace   ON recurring_rules(workspace_id);
CREATE INDEX IF NOT EXISTS idx_recurring_rules_active      ON recurring_rules(active);
CREATE INDEX IF NOT EXISTS idx_recurring_rules_start_date  ON recurring_rules(start_date);
CREATE INDEX IF NOT EXISTS idx_contacts_workspace_name_created ON contacts(workspace_id, name COLLATE NOCASE, created_at DESC);
CREATE INDEX IF NOT EXISTS idx_transactions_workspace      ON transactions(workspace_id);
CREATE INDEX IF NOT EXISTS idx_transactions_account        ON transactions(account_id);
CREATE INDEX IF NOT EXISTS idx_transactions_category       ON transactions(category_id);
CREATE INDEX IF NOT EXISTS idx_transactions_invoice        ON transactions(invoice_id);
CREATE INDEX IF NOT EXISTS idx_transactions_date           ON transactions(date);
CREATE INDEX IF NOT EXISTS idx_transactions_type           ON transactions(type);
CREATE INDEX IF NOT EXISTS idx_transactions_parent         ON transactions(parent_id);
CREATE INDEX IF NOT EXISTS idx_transactions_workspace_parent ON transactions(workspace_id, parent_id);
CREATE INDEX IF NOT EXISTS idx_transactions_workspace_parent_date ON transactions(workspace_id, parent_id, date);
CREATE INDEX IF NOT EXISTS idx_transactions_workspace_date_created ON transactions(workspace_id, date DESC, created_at DESC);
CREATE INDEX IF NOT EXISTS idx_transactions_workspace_type_date ON transactions(workspace_id, type, date);
CREATE INDEX IF NOT EXISTS idx_transactions_workspace_category_type_date ON transactions(workspace_id, category_id, type, date);
CREATE INDEX IF NOT EXISTS idx_transactions_workspace_invoice_date ON transactions(workspace_id, invoice_id, date DESC);
CREATE INDEX IF NOT EXISTS idx_cost_limits_workspace       ON cost_limits(workspace_id);
CREATE INDEX IF NOT EXISTS idx_boxes_workspace             ON boxes(workspace_id);
CREATE INDEX IF NOT EXISTS idx_boxes_workspace_category    ON boxes(workspace_id, category_id);
CREATE INDEX IF NOT EXISTS idx_box_virtual_ledger_box      ON box_virtual_ledger(box_id);
CREATE INDEX IF NOT EXISTS idx_box_virtual_ledger_box_reference_date ON box_virtual_ledger(box_id, reference_date);
`

func normalizeSQLiteDSN(dbURL string) string {
	if dbURL == ":memory:" || strings.HasSuffix(dbURL, ":memory:") {
		return dbURL
	}

	type pragma struct {
		key   string
		value string
		param string
	}
	requiredPragmas := []pragma{
		{key: "journal_mode", value: "WAL", param: "_pragma=journal_mode(WAL)"},
		{key: "foreign_keys", value: "1", param: "_pragma=foreign_keys(1)"},
		{key: "synchronous", value: "NORMAL", param: "_pragma=synchronous(NORMAL)"},
		{key: "busy_timeout", value: "5000", param: "_pragma=busy_timeout(5000)"},
	}

	lower := strings.ToLower(dbURL)
	existing := make(map[string]bool)
	for _, p := range requiredPragmas {
		if strings.Contains(lower, "_pragma="+p.key+"(") {
			existing[p.key] = true
		}
	}

	var missing []pragma
	for _, p := range requiredPragmas {
		if !existing[p.key] {
			missing = append(missing, p)
		}
	}

	if len(missing) == 0 {
		return dbURL
	}

	var sb strings.Builder
	sb.WriteString(dbURL)
	if !strings.Contains(dbURL, "?") {
		sb.WriteString("?")
	} else {
		sb.WriteString("&")
	}
	for i, p := range missing {
		if i > 0 {
			sb.WriteString("&")
		}
		sb.WriteString(p.param)
	}
	return sb.String()
}

func Open(dbURL string) (*sql.DB, error) {
	if dbURL == "" {
		dbURL = paths.DefaultDatabaseURL()
	}

	dbURL = normalizeSQLiteDSN(dbURL)

	dbPath := sqliteFilePath(dbURL)
	dbExistedBeforeOpen := sqliteDatabaseFileExists(dbPath)
	if isSQLiteDiskPath(dbPath) {
		dir := filepath.Dir(dbPath)
		if err := os.MkdirAll(dir, 0755); err != nil {
			return nil, fmt.Errorf("failed to create database directory %s: %w", dir, err)
		}
	}

	db, err := sql.Open("sqlite", dbURL)
	if err != nil {
		return nil, fmt.Errorf("failed to open database: %w", err)
	}

	db.SetMaxOpenConns(1)

	if err := db.Ping(); err != nil {
		db.Close()
		return nil, fmt.Errorf("failed to ping database: %w", err)
	}

	if dbExistedBeforeOpen {
		if _, err := createPreMigrationBackup(db, dbPath); err != nil {
			db.Close()
			return nil, fmt.Errorf("failed to create pre-migration backup: %w", err)
		}
	}

	if _, err := db.Exec(ddl); err != nil {
		db.Close()
		return nil, fmt.Errorf("failed to execute DDL: %w", err)
	}

	if err := runMigrations(db); err != nil {
		db.Close()
		return nil, fmt.Errorf("failed to run migrations: %w", err)
	}
	startAutoBackup(db, dbPath)
	startSecurityLogPruner(db, dbPath)
	startMonthlyEngine(db, dbPath)

	return db, nil
}

func sqliteFilePath(dbURL string) string {
	if dbURL == ":memory:" {
		return ":memory:"
	}

	dbPath := dbURL
	if strings.HasPrefix(dbURL, "file:") {
		dbPath = strings.TrimPrefix(dbURL, "file:")
	}

	if strings.HasPrefix(dbPath, ":memory:") {
		return ":memory:"
	}
	if idx := strings.IndexAny(dbPath, "?#"); idx >= 0 {
		dbPath = dbPath[:idx]
	}

	return dbPath
}

func isSQLiteDiskPath(dbPath string) bool {
	return dbPath != "" && dbPath != ":memory:"
}

func sqliteDatabaseFileExists(dbPath string) bool {
	if !isSQLiteDiskPath(dbPath) {
		return false
	}

	info, err := os.Stat(dbPath)
	return err == nil && !info.IsDir()
}

func createPreMigrationBackup(db *sql.DB, dbPath string) (string, error) {
	if !sqliteDatabaseFileExists(dbPath) {
		return "", nil
	}

	backupDir := filepath.Join(paths.BackupsDir(), "pre-migration")
	if err := os.MkdirAll(backupDir, 0o755); err != nil {
		return "", fmt.Errorf("failed to create pre-migration backup directory: %w", err)
	}

	dest, err := nextBackupPath(backupDir, "contabase-pre-migration")
	if err != nil {
		return "", err
	}
	if err := CreateSQLiteBackupFile(db, dbPath, dest); err != nil {
		return "", fmt.Errorf("failed to copy pre-migration backup: %w", err)
	}

	return dest, nil
}

// CreateSQLiteBackupFile checkpoints WAL and copies a disk-backed SQLite database to destPath.
func CreateSQLiteBackupFile(db *sql.DB, dbPath, destPath string) error {
	if db == nil {
		return fmt.Errorf("nil database")
	}
	dbPath = sqliteFilePath(strings.TrimSpace(dbPath))
	destPath = strings.TrimSpace(destPath)
	if !sqliteDatabaseFileExists(dbPath) {
		return fmt.Errorf("sqlite database file not found")
	}
	if destPath == "" {
		return fmt.Errorf("backup destination path is required")
	}

	srcAbs, err := filepath.Abs(dbPath)
	if err != nil {
		return fmt.Errorf("failed to resolve database path: %w", err)
	}
	destAbs, err := filepath.Abs(destPath)
	if err != nil {
		return fmt.Errorf("failed to resolve backup destination path: %w", err)
	}
	if srcAbs == destAbs {
		return fmt.Errorf("backup destination must be different from database path")
	}
	if err := os.MkdirAll(filepath.Dir(destAbs), 0o755); err != nil {
		return fmt.Errorf("failed to create backup destination directory: %w", err)
	}
	if err := checkpointWAL(db); err != nil {
		return fmt.Errorf("failed to checkpoint WAL before backup: %w", err)
	}
	if err := copyFile(srcAbs, destAbs); err != nil {
		return err
	}
	return nil
}

func nextBackupPath(dir, prefix string) (string, error) {
	timestamp := time.Now().Format("20060102-150405")
	for i := 0; ; i++ {
		name := fmt.Sprintf("%s-%s.db", prefix, timestamp)
		if i > 0 {
			name = fmt.Sprintf("%s-%s-%02d.db", prefix, timestamp, i)
		}

		path := filepath.Join(dir, name)
		if _, err := os.Stat(path); os.IsNotExist(err) {
			return path, nil
		} else if err != nil {
			return "", fmt.Errorf("failed to inspect backup path: %w", err)
		}
	}
}

func checkpointWAL(db *sql.DB) error {
	rows, err := db.Query(`PRAGMA wal_checkpoint(TRUNCATE)`)
	if err != nil {
		return err
	}

	var busy int
	for rows.Next() {
		var logFrames, checkpointedFrames int
		if err := rows.Scan(&busy, &logFrames, &checkpointedFrames); err != nil {
			_ = rows.Close()
			return err
		}
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return err
	}
	if err := rows.Close(); err != nil {
		return err
	}
	if busy != 0 {
		return fmt.Errorf("WAL checkpoint busy: %d", busy)
	}

	return nil
}

func HasAnyUser(db *sql.DB) bool {
	var count int
	if err := db.QueryRow(`SELECT COUNT(*) FROM users`).Scan(&count); err != nil {
		log.Printf("has any user check failed: %v", err)
		return false
	}
	return count > 0
}

func startAutoBackup(db *sql.DB, dbPath string) {
	if db == nil || !isSQLiteDiskPath(dbPath) {
		return
	}
	go func() {
		ticker := time.NewTicker(24 * time.Hour)
		defer ticker.Stop()
		for {
			if err := createRotatingBackup(db, dbPath, 5); err != nil {
				log.Printf("auto backup failed: %v", err)
			}
			<-ticker.C
		}
	}()
}

func PruneSecurityLogs(db *sql.DB) error {
	if db == nil {
		return fmt.Errorf("nil database")
	}
	_, err := db.Exec(`DELETE FROM security_logs WHERE created_at < unixepoch('now', '-14 days')`)
	return err
}

func startSecurityLogPruner(db *sql.DB, dbPath string) {
	if db == nil || !isSQLiteDiskPath(dbPath) {
		return
	}
	go func() {
		if err := PruneSecurityLogs(db); err != nil {
			log.Printf("security logs prune failed: %v", err)
		}
		ticker := time.NewTicker(24 * time.Hour)
		defer ticker.Stop()
		for range ticker.C {
			if err := PruneSecurityLogs(db); err != nil {
				log.Printf("security logs prune failed: %v", err)
			}
		}
	}()
}

func startMonthlyEngine(db *sql.DB, dbPath string) {
	if db == nil || !isSQLiteDiskPath(dbPath) {
		return
	}
	go func() {
		ticker := time.NewTicker(6 * time.Hour)
		defer ticker.Stop()
		for {
			now := time.Now()
			if now.Day() == 1 {
				monthStart := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, time.UTC).Unix()
				rows, err := db.Query(`SELECT id, monthly_recharge FROM boxes WHERE monthly_recharge > 0`)
				if err == nil {
					type monthlyBox struct {
						id       string
						recharge int64
					}
					var boxes []monthlyBox
					for rows.Next() {
						var box monthlyBox
						if err := rows.Scan(&box.id, &box.recharge); err == nil {
							boxes = append(boxes, box)
						}
					}
					rowsErr := rows.Err()
					closeErr := rows.Close()
					if rowsErr == nil && closeErr == nil {
						for _, box := range boxes {
							var count int
							_ = db.QueryRow(`SELECT COUNT(1) FROM box_virtual_ledger WHERE box_id = ? AND type = 'RECHARGE' AND reference_date >= ? AND description = 'Aporte Automático'`, box.id, monthStart).Scan(&count)
							if count == 0 {
								_, _ = db.Exec(`INSERT INTO box_virtual_ledger (id, box_id, reference_date, amount, type, description, created_at) VALUES (?, ?, ?, ?, 'RECHARGE', 'Aporte Automático', ?)`,
									uuid.NewString(), box.id, now.Unix(), box.recharge, now.Unix())
							}
						}
					}
				}
			}
			<-ticker.C
		}
	}()
}

func createRotatingBackup(db *sql.DB, dbPath string, retain int) error {
	if retain < 1 {
		retain = 1
	}
	if _, err := os.Stat(dbPath); err != nil {
		return err
	}
	backupDir := paths.BackupsDir()
	if err := os.MkdirAll(backupDir, 0o755); err != nil {
		return err
	}
	dest, err := nextBackupPath(backupDir, "contabase")
	if err != nil {
		return err
	}
	if err := CreateSQLiteBackupFile(db, dbPath, dest); err != nil {
		return err
	}
	entries, err := os.ReadDir(backupDir)
	if err != nil {
		return err
	}
	type item struct {
		name string
		time time.Time
	}
	var files []item
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".db") {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		files = append(files, item{name: filepath.Join(backupDir, e.Name()), time: info.ModTime()})
	}
	sort.Slice(files, func(i, j int) bool { return files[i].time.Before(files[j].time) })
	for len(files) > retain {
		if err := os.Remove(files[0].name); err != nil {
			log.Printf("backup retention remove failed: %v", err)
			break
		}
		files = files[1:]
	}
	return nil
}

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.Create(dst)
	if err != nil {
		return err
	}
	defer out.Close()
	if _, err := io.Copy(out, in); err != nil {
		return err
	}
	return out.Sync()
}
