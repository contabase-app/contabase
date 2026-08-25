package database

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
	"unicode"

	"github.com/google/uuid"
	"golang.org/x/text/runes"
	"golang.org/x/text/transform"
	"golang.org/x/text/unicode/norm"
	"modernc.org/sqlite"

	"github.com/contabase-app/contabase/internal/boxcontribution"
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
    deleted_at      INTEGER NOT NULL DEFAULT 0,
    deleted_reason  TEXT,
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
	monthly_income_reference INTEGER CHECK (monthly_income_reference IS NULL OR monthly_income_reference >= 0),
	monthly_total_cost_reference INTEGER CHECK (monthly_total_cost_reference IS NULL OR monthly_total_cost_reference >= 0),
	monthly_essential_cost_reference INTEGER CHECK (monthly_essential_cost_reference IS NULL OR monthly_essential_cost_reference >= 0),
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
    workspace_id TEXT,
    title      TEXT NOT NULL DEFAULT '',
    message    TEXT NOT NULL DEFAULT '',
    type       TEXT NOT NULL DEFAULT '',
    dedupe_key TEXT,
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
    id                          TEXT PRIMARY KEY,
    account_id                  TEXT    NOT NULL UNIQUE,
    closing_day                 INTEGER NOT NULL CHECK (closing_day BETWEEN 1 AND 31),
    due_day                     INTEGER NOT NULL CHECK (due_day BETWEEN 1 AND 31),
    credit_limit                INTEGER NOT NULL DEFAULT 0,
    default_payment_account_id  TEXT,
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
    series_id               TEXT,
    parent_rule_id          TEXT,
    effective_start_sequence INTEGER NOT NULL DEFAULT 1,
    effective_end_sequence  INTEGER,
    starts_at               INTEGER,
    end_date                INTEGER,
    contact_id              TEXT,
    due_date_policy         TEXT,
    due_date_offset_days    INTEGER,
    generated_until         INTEGER,
    created_at              INTEGER NOT NULL DEFAULT (unixepoch()),
    updated_at              INTEGER NOT NULL DEFAULT (unixepoch()),
    FOREIGN KEY (workspace_id)           REFERENCES workspaces(id)  ON DELETE CASCADE,
    FOREIGN KEY (user_id)                REFERENCES users(id)       ON DELETE RESTRICT,
    FOREIGN KEY (account_id)             REFERENCES accounts(id)    ON DELETE RESTRICT,
    FOREIGN KEY (destination_account_id) REFERENCES accounts(id)    ON DELETE SET NULL,
    FOREIGN KEY (category_id)            REFERENCES categories(id)  ON DELETE SET NULL,
    FOREIGN KEY (series_id)              REFERENCES recurring_series(id) ON DELETE SET NULL,
    FOREIGN KEY (parent_rule_id)         REFERENCES recurring_rules(id)  ON DELETE SET NULL,
    FOREIGN KEY (contact_id)             REFERENCES contacts(id)         ON DELETE SET NULL
);

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
);

CREATE TABLE IF NOT EXISTS recurring_occurrence_skips (
    workspace_id          TEXT    NOT NULL,
    series_id             TEXT    NOT NULL,
    recurrence_sequence   INTEGER NOT NULL CHECK (recurrence_sequence > 0),
    original_transaction_id TEXT,
    reason                TEXT    NOT NULL CHECK (reason IN ('single_delete')),
    deleted_by            TEXT,
    deleted_at            INTEGER NOT NULL,
    PRIMARY KEY (workspace_id, series_id, recurrence_sequence)
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
    series_id               TEXT,
    is_recurring_exception  INTEGER NOT NULL DEFAULT 0 CHECK (is_recurring_exception IN (0, 1)),
    recurring_exception_reason TEXT,
    -- LEGADO INATIVO: manter somente para compatibilidade e backfill da migration v11.
    -- @deprecated v20: use 'status' column exclusively.
    payment_status          TEXT    NOT NULL DEFAULT 'PAID' CHECK (payment_status IN ('PAID', 'PENDING')),
    status                  TEXT    NOT NULL DEFAULT 'paid' CHECK (status IN ('paid', 'pending')),
    due_date                INTEGER,
    paid_at                 INTEGER,
    scheduled_payment_at    INTEGER,
    scheduled_payment_created_by TEXT,
    scheduled_payment_executed_at INTEGER,
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
    FOREIGN KEY (recurring_rule_id)      REFERENCES recurring_rules(id) ON DELETE SET NULL,
    FOREIGN KEY (series_id)              REFERENCES recurring_series(id) ON DELETE SET NULL
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
    category_id      TEXT,
    target_amount    INTEGER NOT NULL DEFAULT 0,
    monthly_recharge INTEGER NOT NULL DEFAULT 0,
	continue_after_target INTEGER NOT NULL DEFAULT 1 CHECK (continue_after_target IN (0, 1)),
	automatic_contribution_state TEXT NOT NULL DEFAULT 'ACTIVE' CHECK (automatic_contribution_state IN ('ACTIVE', 'BLOCKED')),
    target_date      INTEGER,
    monthly_yield_rate REAL  NOT NULL DEFAULT 0.0,
	monthly_recharge_anchor_month TEXT CHECK (
		monthly_recharge_anchor_month IS NULL OR (
			length(monthly_recharge_anchor_month) = 7 AND
			substr(monthly_recharge_anchor_month, 5, 1) = '-' AND
			substr(monthly_recharge_anchor_month, 1, 4) NOT GLOB '*[^0-9]*' AND
			substr(monthly_recharge_anchor_month, 6, 2) NOT GLOB '*[^0-9]*' AND
			CAST(substr(monthly_recharge_anchor_month, 6, 2) AS INTEGER) BETWEEN 1 AND 12
		)
	),
    created_at       INTEGER NOT NULL DEFAULT (unixepoch()),
    updated_at       INTEGER NOT NULL DEFAULT (unixepoch()),
    FOREIGN KEY (workspace_id) REFERENCES workspaces(id) ON DELETE CASCADE,
    FOREIGN KEY (category_id)  REFERENCES categories(id) ON DELETE SET NULL
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
	automatic_recharge_month TEXT CHECK (
		automatic_recharge_month IS NULL OR (
			type = 'RECHARGE' AND
			length(automatic_recharge_month) = 7 AND
			substr(automatic_recharge_month, 5, 1) = '-' AND
			substr(automatic_recharge_month, 1, 4) NOT GLOB '*[^0-9]*' AND
			substr(automatic_recharge_month, 6, 2) NOT GLOB '*[^0-9]*' AND
			CAST(substr(automatic_recharge_month, 6, 2) AS INTEGER) BETWEEN 1 AND 12
		)
	),
    reference_date INTEGER NOT NULL,
    created_at     INTEGER NOT NULL DEFAULT (unixepoch()),
    FOREIGN KEY (box_id) REFERENCES boxes(id) ON DELETE CASCADE,
    FOREIGN KEY (reversal_of_ledger_id) REFERENCES box_virtual_ledger(id) ON DELETE SET NULL
);

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
);

CREATE TABLE IF NOT EXISTS transaction_status_history (
    id              TEXT PRIMARY KEY,
    transaction_id  TEXT NOT NULL,
    workspace_id    TEXT NOT NULL,
    user_id         TEXT NOT NULL,
    previous_status TEXT,
    new_status      TEXT,
    previous_paid_at INTEGER,
    new_paid_at     INTEGER,
    source          TEXT NOT NULL DEFAULT 'manual',
    created_at      INTEGER NOT NULL
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
CREATE INDEX IF NOT EXISTS idx_recurring_materialization_state_locked_until ON recurring_materialization_state(locked_until);
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
CREATE INDEX IF NOT EXISTS idx_tsh_transaction ON transaction_status_history(transaction_id);
CREATE INDEX IF NOT EXISTS idx_tsh_workspace_created ON transaction_status_history(workspace_id, created_at);
CREATE INDEX IF NOT EXISTS idx_cost_limits_workspace       ON cost_limits(workspace_id);
CREATE INDEX IF NOT EXISTS idx_boxes_workspace             ON boxes(workspace_id);
CREATE INDEX IF NOT EXISTS idx_boxes_workspace_category    ON boxes(workspace_id, category_id);
CREATE INDEX IF NOT EXISTS idx_box_virtual_ledger_box      ON box_virtual_ledger(box_id);
CREATE INDEX IF NOT EXISTS idx_box_virtual_ledger_box_reference_date ON box_virtual_ledger(box_id, reference_date);
CREATE INDEX IF NOT EXISTS idx_box_contribution_lifecycle_events_box_occurred ON box_contribution_lifecycle_events(box_id, occurred_at, id);
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

type sqliteOpenConfig struct {
	normalDSN      string
	immediateDSN   string
	dbPath         string
	existedAtStart bool
}

type sqliteRuntimeJobHooks struct {
	onStart func(string)
	onStop  func(string)
}

type sqliteRuntimeOpenTestHooks struct {
	beforeNormalOpen    func() error
	afterNormalOpen     func(*sql.DB)
	beforeImmediateOpen func() error
	afterImmediateOpen  func(*sql.DB)
	beforeInitialize    func() error
	afterInitialize     func()
	beforeJobsStart     func() error
	jobs                *sqliteRuntimeJobHooks
}

type sqliteRuntimeJobs struct {
	cancel context.CancelFunc
	wait   sync.WaitGroup
}

var (
	errRuntimeDBsClosed  = errors.New("runtime databases already closed")
	errRuntimeDBsInvalid = errors.New("invalid runtime database owner")
)

// RuntimeDBs owns the server's normal/deferred pool, its dedicated immediate
// writer pool, and the database runtime jobs. Immediate is intentionally
// reserved for financial flows that must acquire the SQLite writer before
// reading decision-making state.
type RuntimeDBs struct {
	DB        *sql.DB
	Immediate *sql.DB

	dbPath          string
	beforeJobsStart func() error
	jobHooks        *sqliteRuntimeJobHooks
	stateMu         sync.Mutex
	jobs            *sqliteRuntimeJobs
	closed          bool
	closeOnce       sync.Once
	closeErr        error
}

func prepareSQLiteOpenConfig(dbURL string, withImmediate bool) (sqliteOpenConfig, error) {
	if dbURL == "" {
		dbURL = paths.DefaultDatabaseURL()
	}
	normalized := normalizeSQLiteDSN(dbURL)
	dbPath := sqliteFilePath(normalized)
	if withImmediate && isSQLiteMemoryDSN(normalized) {
		return sqliteOpenConfig{}, fmt.Errorf("sqlite runtime pools require a disk-backed database")
	}
	if isSQLiteDiskPath(dbPath) {
		dir := filepath.Dir(dbPath)
		if err := os.MkdirAll(dir, 0755); err != nil {
			return sqliteOpenConfig{}, fmt.Errorf("failed to create database directory %s: %w", dir, err)
		}
	}
	config := sqliteOpenConfig{
		normalDSN:      normalized,
		dbPath:         dbPath,
		existedAtStart: sqliteDatabaseFileExists(dbPath),
	}
	if withImmediate {
		config.normalDSN = sqliteDSNWithTxLock(normalized, "deferred")
		config.immediateDSN = sqliteDSNWithTxLock(normalized, "immediate")
	}
	return config, nil
}

func sqliteDSNWithTxLock(dbURL, mode string) string {
	base, query, hasQuery := strings.Cut(dbURL, "?")
	parts := make([]string, 0)
	if hasQuery {
		for _, part := range strings.Split(query, "&") {
			if part == "" || strings.HasPrefix(strings.ToLower(part), "_txlock=") {
				continue
			}
			parts = append(parts, part)
		}
	}
	parts = append(parts, "_txlock="+mode)
	return base + "?" + strings.Join(parts, "&")
}

func isSQLiteMemoryDSN(dbURL string) bool {
	lower := strings.ToLower(strings.TrimSpace(dbURL))
	return sqliteFilePath(lower) == ":memory:" || strings.Contains(lower, "mode=memory")
}

func openSQLitePool(dbURL string) (*sql.DB, error) {
	db, err := sql.Open("sqlite", dbURL)
	if err != nil {
		return nil, fmt.Errorf("failed to open database: %w", err)
	}
	db.SetMaxOpenConns(1)
	if err := db.Ping(); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("failed to ping database: %w", err)
	}
	return db, nil
}

func initializeSQLiteDatabase(db *sql.DB, config sqliteOpenConfig) error {
	if config.existedAtStart {
		if _, err := createPreMigrationBackup(db, config.dbPath); err != nil {
			return fmt.Errorf("failed to create pre-migration backup: %w", err)
		}
	}
	if _, err := db.Exec(ddl); err != nil {
		return fmt.Errorf("failed to execute DDL: %w", err)
	}
	if err := runMigrations(db); err != nil {
		return fmt.Errorf("failed to run migrations: %w", err)
	}
	return nil
}

// Open preserves the historical single-pool contract used by CLI commands,
// maintenance paths and tests. Runtime jobs intentionally remain attached to
// a background context in this compatibility mode.
func Open(dbURL string) (*sql.DB, error) {
	db, _, err := openSinglePool(dbURL, nil)
	return db, err
}

func openSinglePool(dbURL string, jobHooks *sqliteRuntimeJobHooks) (*sql.DB, *sqliteRuntimeJobs, error) {
	config, err := prepareSQLiteOpenConfig(dbURL, false)
	if err != nil {
		return nil, nil, err
	}
	db, err := openSQLitePool(config.normalDSN)
	if err != nil {
		return nil, nil, err
	}
	if err := initializeSQLiteDatabase(db, config); err != nil {
		_ = db.Close()
		return nil, nil, err
	}
	jobs := startSQLiteRuntimeJobs(context.Background(), db, config.dbPath, jobHooks)
	return db, jobs, nil
}

// OpenRuntimeDBs opens the server's two pools without calling Open. Schema
// initialization runs exactly once on DB. The returned owner is dormant: its
// runtime jobs do not start until Activate is called at the publication
// boundary of a fully prepared server generation.
func OpenRuntimeDBs(dbURL string) (*RuntimeDBs, error) {
	return openRuntimeDBs(dbURL, nil)
}

func openRuntimeDBs(dbURL string, hooks *sqliteRuntimeOpenTestHooks) (*RuntimeDBs, error) {
	config, err := prepareSQLiteOpenConfig(dbURL, true)
	if err != nil {
		return nil, err
	}
	if hooks != nil && hooks.beforeNormalOpen != nil {
		if err := hooks.beforeNormalOpen(); err != nil {
			return nil, err
		}
	}
	normalDB, err := openSQLitePool(config.normalDSN)
	if err != nil {
		return nil, err
	}
	if hooks != nil && hooks.afterNormalOpen != nil {
		hooks.afterNormalOpen(normalDB)
	}
	closeNormal := true
	defer func() {
		if closeNormal {
			_ = normalDB.Close()
		}
	}()

	if hooks != nil && hooks.beforeImmediateOpen != nil {
		if err := hooks.beforeImmediateOpen(); err != nil {
			return nil, err
		}
	}
	immediateDB, err := openSQLitePool(config.immediateDSN)
	if err != nil {
		return nil, err
	}
	if hooks != nil && hooks.afterImmediateOpen != nil {
		hooks.afterImmediateOpen(immediateDB)
	}
	closeImmediate := true
	defer func() {
		if closeImmediate {
			_ = immediateDB.Close()
		}
	}()

	if hooks != nil && hooks.beforeInitialize != nil {
		if err := hooks.beforeInitialize(); err != nil {
			return nil, err
		}
	}
	if err := initializeSQLiteDatabase(normalDB, config); err != nil {
		return nil, err
	}
	if hooks != nil && hooks.afterInitialize != nil {
		hooks.afterInitialize()
	}
	var beforeJobsStart func() error
	var jobHooks *sqliteRuntimeJobHooks
	if hooks != nil {
		beforeJobsStart = hooks.beforeJobsStart
		jobHooks = hooks.jobs
	}
	owner := &RuntimeDBs{
		DB:              normalDB,
		Immediate:       immediateDB,
		dbPath:          config.dbPath,
		beforeJobsStart: beforeJobsStart,
		jobHooks:        jobHooks,
	}
	closeNormal = false
	closeImmediate = false
	return owner, nil
}

// Activate starts the database-owned runtime jobs exactly once. OpenRuntimeDBs
// deliberately leaves the owner dormant so callers can complete every
// fallible validation and construction step before crossing the generation's
// publication boundary.
func (r *RuntimeDBs) Activate() error {
	if r == nil {
		return errRuntimeDBsInvalid
	}
	r.stateMu.Lock()
	defer r.stateMu.Unlock()
	if r.closed {
		return errRuntimeDBsClosed
	}
	if r.DB == nil || r.Immediate == nil {
		return errRuntimeDBsInvalid
	}
	if r.jobs != nil {
		return nil
	}
	if r.beforeJobsStart != nil {
		if err := r.beforeJobsStart(); err != nil {
			return err
		}
	}
	r.jobs = startSQLiteRuntimeJobs(context.Background(), r.DB, r.dbPath, r.jobHooks)
	return nil
}

func (r *RuntimeDBs) Close() error {
	if r == nil {
		return nil
	}
	r.closeOnce.Do(func() {
		r.stateMu.Lock()
		r.closed = true
		jobs := r.jobs
		r.stateMu.Unlock()
		if jobs != nil {
			jobs.cancel()
			jobs.wait.Wait()
		}
		var errs []error
		if r.Immediate != nil {
			if err := r.Immediate.Close(); err != nil {
				errs = append(errs, err)
			}
		}
		if r.DB != nil {
			if err := r.DB.Close(); err != nil {
				errs = append(errs, err)
			}
		}
		r.closeErr = errors.Join(errs...)
	})
	return r.closeErr
}

// OpenExistingForMaintenance opens an existing on-disk SQLite database without
// executing DDL, migrations, automatic backups or background jobs. It exists
// for explicit, offline maintenance commands whose scope must stay narrower
// than normal application boot.
//
// DEPRECATED: use OpenExistingForMaintenanceReadOnly or OpenExistingForMaintenanceReadWrite.
// This function remains for backward compatibility only and applies mode=ro.
func OpenExistingForMaintenance(dbURL string) (*sql.DB, error) {
	db, _, err := OpenExistingForMaintenanceReadOnly(dbURL)
	return db, err
}

// OpenExistingForMaintenanceReadOnly opens an existing database with mode=ro
// and immediately sets query_only=ON. Returns (*sql.DB, canonicalDBPath, error).
// It rejects empty URLs, :memory:, non-existent files, and any user-supplied
// mode that conflicts with ro.
func OpenExistingForMaintenanceReadOnly(dbURL string) (*sql.DB, string, error) {
	return openExistingForMaintenance(dbURL, false)
}

// OpenExistingForMaintenanceReadWrite opens an existing database with mode=rw
// and BEGIN IMMEDIATE transaction locking. Returns (*sql.DB, canonicalDBPath, error).
// It rejects empty URLs, :memory:, non-existent files, and any user-supplied
// mode that would allow creation (rwc).
func OpenExistingForMaintenanceReadWrite(dbURL string) (*sql.DB, string, error) {
	return openExistingForMaintenance(dbURL, true)
}

func openExistingForMaintenance(dbURL string, readWrite bool) (*sql.DB, string, error) {
	if strings.TrimSpace(dbURL) == "" {
		return nil, "", fmt.Errorf("maintenance database URL is required")
	}

	userMode := extractSQLiteMode(dbURL)
	canonicalPath := sqliteFilePath(dbURL)

	if canonicalPath == ":memory:" || canonicalPath == "" {
		return nil, "", fmt.Errorf("maintenance database must be an existing on-disk SQLite file; :memory: and empty path are not permitted")
	}
	if !isSQLiteDiskPath(canonicalPath) {
		return nil, "", fmt.Errorf("maintenance database must be an existing on-disk SQLite file")
	}
	if !sqliteDatabaseFileExists(canonicalPath) {
		return nil, "", fmt.Errorf("maintenance database file does not exist")
	}
	if userMode != "" {
		switch strings.ToLower(userMode) {
		case "rwc":
			return nil, "", fmt.Errorf("maintenance database: mode=rwc is rejected (would allow implicit file creation)")
		case "memory":
			return nil, "", fmt.Errorf("maintenance database: mode=memory is rejected")
		default:
			return nil, "", fmt.Errorf("maintenance database: user-supplied mode=%q is rejected; the maintenance opener enforces its own safe mode", userMode)
		}
	}

	var safeDSN string
	if readWrite {
		safeDSN = fmt.Sprintf("file:%s?mode=rw&_txlock=immediate", canonicalPath)
	} else {
		safeDSN = fmt.Sprintf("file:%s?mode=ro", canonicalPath)
	}

	db, err := sql.Open("sqlite", safeDSN)
	if err != nil {
		return nil, "", fmt.Errorf("failed to open maintenance database: %w", err)
	}
	db.SetMaxOpenConns(1)
	if err := db.Ping(); err != nil {
		db.Close()
		return nil, "", fmt.Errorf("failed to ping maintenance database: %w", err)
	}

	if !readWrite {
		if _, err := db.Exec(`PRAGMA query_only = ON`); err != nil {
			db.Close()
			return nil, "", fmt.Errorf("failed to enforce read-only: %w", err)
		}
	}

	return db, canonicalPath, nil
}

// extractSQLiteMode returns the mode= URI parameter value or empty string.
func extractSQLiteMode(dbURL string) string {
	lower := strings.ToLower(dbURL)
	const prefix = "mode="
	idx := strings.Index(lower, prefix)
	if idx < 0 {
		return ""
	}
	start := idx + len(prefix)
	end := start
	for end < len(lower) && lower[end] != '&' && lower[end] != '?' && lower[end] != '#' {
		end++
	}
	return lower[start:end]
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

func PruneSecurityLogs(db *sql.DB) error {
	if db == nil {
		return fmt.Errorf("nil database")
	}
	_, err := db.Exec(`DELETE FROM security_logs WHERE created_at < unixepoch('now', '-14 days')`)
	return err
}

func startSQLiteRuntimeJobs(parent context.Context, db *sql.DB, dbPath string, hooks *sqliteRuntimeJobHooks) *sqliteRuntimeJobs {
	ctx, cancel := context.WithCancel(parent)
	jobs := &sqliteRuntimeJobs{cancel: cancel}
	if db == nil || !isSQLiteDiskPath(dbPath) {
		return jobs
	}

	start := func(name string, interval time.Duration, run func(context.Context)) {
		jobs.wait.Add(1)
		go func() {
			defer jobs.wait.Done()
			if hooks != nil && hooks.onStart != nil {
				hooks.onStart(name)
			}
			defer func() {
				if hooks != nil && hooks.onStop != nil {
					hooks.onStop(name)
				}
			}()

			ticker := time.NewTicker(interval)
			defer ticker.Stop()
			for {
				run(ctx)
				select {
				case <-ctx.Done():
					return
				case <-ticker.C:
				}
			}
		}()
	}

	start("auto_backup", automaticBackupCheckInterval, func(context.Context) {
		loc, err := resolveCivilRepairTimezone()
		if err != nil {
			log.Printf("auto backup timezone failed: %v", err)
			return
		}
		if err := ensureDailyAutomaticBackup(db, dbPath, paths.BackupsDir(), time.Now().In(loc), AutomaticBackupRetentionDays); err != nil {
			log.Printf("auto backup failed: %v", err)
		}
	})
	start("security_log_pruner", 24*time.Hour, func(context.Context) {
		if err := PruneSecurityLogs(db); err != nil {
			log.Printf("security logs prune failed: %v", err)
		}
	})
	start("monthly_engine", 6*time.Hour, func(jobContext context.Context) {
		loc, err := resolveCivilRepairTimezone()
		if err != nil {
			log.Printf("monthly box recharge timezone failed: %v", err)
		} else if err := materializeMonthlyBoxRecharges(jobContext, db, time.Now().In(loc)); err != nil && !errors.Is(err, context.Canceled) {
			log.Printf("monthly box recharge failed: %v", err)
		}
	})
	return jobs
}

// materializeMonthlyBoxRecharges creates at most one automatic recharge for
// each eligible box in civilNow's month. The caller supplies civilNow in the
// configured financial timezone so the financial core does not read a global
// clock. A BEGIN IMMEDIATE transaction serializes balance, lifecycle and
// ledger decisions; the partial unique index remains the final database
// invariant for (box_id, automatic_recharge_month).
func materializeMonthlyBoxRecharges(ctx context.Context, db *sql.DB, civilNow time.Time) (err error) {
	if db == nil {
		return fmt.Errorf("monthly box recharge: nil database")
	}
	if civilNow.IsZero() {
		return fmt.Errorf("monthly box recharge: zero civil time")
	}

	civilMonth := civilNow.Format("2006-01")
	materializedAt := civilNow.Unix()
	conn, err := db.Conn(ctx)
	if err != nil {
		return fmt.Errorf("monthly box recharge acquire connection: %w", err)
	}
	defer conn.Close()

	if _, err = conn.ExecContext(ctx, `BEGIN IMMEDIATE`); err != nil {
		return fmt.Errorf("monthly box recharge begin immediate: %w", err)
	}
	committed := false
	defer func() {
		if !committed {
			_, _ = conn.ExecContext(context.Background(), `ROLLBACK`)
		}
	}()

	rows, err := conn.QueryContext(ctx, `
		SELECT id, target_amount, monthly_recharge,
			continue_after_target, automatic_contribution_state,
			monthly_recharge_anchor_month, created_at
		FROM boxes
		WHERE monthly_recharge > 0
		ORDER BY id
	`)
	if err != nil {
		return fmt.Errorf("monthly box recharge select eligible boxes: %w", err)
	}
	type monthlyBox struct {
		id                  string
		target              int64
		recharge            int64
		continueAfterTarget int64
		state               BoxAutomaticContributionState
		anchor              sql.NullString
		createdAt           int64
	}
	var boxes []monthlyBox
	for rows.Next() {
		var box monthlyBox
		var stateRaw string
		if err := rows.Scan(
			&box.id,
			&box.target,
			&box.recharge,
			&box.continueAfterTarget,
			&stateRaw,
			&box.anchor,
			&box.createdAt,
		); err != nil {
			_ = rows.Close()
			return fmt.Errorf("monthly box recharge scan eligible box: %w", err)
		}
		box.state = BoxAutomaticContributionState(stateRaw)
		if !box.state.valid() {
			_ = rows.Close()
			return fmt.Errorf("monthly box recharge box %q has invalid contribution state %q", box.id, stateRaw)
		}
		if box.continueAfterTarget != 0 && box.continueAfterTarget != 1 {
			_ = rows.Close()
			return fmt.Errorf("monthly box recharge box %q has invalid continue-after-target value %d", box.id, box.continueAfterTarget)
		}
		boxes = append(boxes, box)
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return fmt.Errorf("monthly box recharge eligible boxes: %w", err)
	}
	if err := rows.Close(); err != nil {
		return fmt.Errorf("monthly box recharge close eligible boxes: %w", err)
	}

	for _, box := range boxes {
		anchorMonth := box.anchor.String
		if !box.anchor.Valid || anchorMonth == "" {
			anchorMonth = time.Unix(box.createdAt, 0).In(civilNow.Location()).Format("2006-01")
			if _, err := conn.ExecContext(ctx, `
				UPDATE boxes
				SET monthly_recharge_anchor_month = ?
				WHERE id = ? AND monthly_recharge_anchor_month IS NULL
			`, anchorMonth, box.id); err != nil {
				return fmt.Errorf("monthly box recharge set anchor for box %q: %w", box.id, err)
			}
		}
		if civilMonth < anchorMonth {
			continue
		}

		var balance int64
		if err := conn.QueryRowContext(ctx, `
			SELECT COALESCE(SUM(amount), 0)
			FROM box_virtual_ledger
			WHERE box_id = ?
		`, box.id).Scan(&balance); err != nil {
			return fmt.Errorf("monthly box recharge calculate balance for box %q: %w", box.id, err)
		}

		if box.state == BoxAutomaticContributionStateBlocked {
			if balance >= box.recharge {
				continue
			}
			changed, err := transitionBoxAutomaticContributionState(
				ctx,
				conn,
				box.id,
				BoxAutomaticContributionStateActive,
				materializedAt,
			)
			if err != nil {
				return fmt.Errorf("monthly box recharge reactivate box %q: %w", box.id, err)
			}
			if !changed {
				return fmt.Errorf("monthly box recharge box %q was not reactivated", box.id)
			}
			box.state = BoxAutomaticContributionStateActive
		}

		decision := boxcontribution.NextAutomaticContribution(boxcontribution.NextAutomaticContributionInput{
			BalanceCents:        balance,
			TargetCents:         box.target,
			MonthlyCents:        box.recharge,
			TargetDefined:       box.target > 0,
			ContinueAfterTarget: box.continueAfterTarget == 1,
			Active:              true,
		})
		if decision.TargetReached && decision.AmountCents == 0 {
			if err := recordBoxContributionGoalReached(ctx, conn, box.id, materializedAt); err != nil {
				return fmt.Errorf("monthly box recharge recognize reached target for box %q: %w", box.id, err)
			}
			changed, err := transitionBoxAutomaticContributionState(
				ctx,
				conn,
				box.id,
				BoxAutomaticContributionStateBlocked,
				materializedAt,
			)
			if err != nil {
				return fmt.Errorf("monthly box recharge block reached box %q: %w", box.id, err)
			}
			if !changed {
				return fmt.Errorf("monthly box recharge reached box %q was not blocked", box.id)
			}
			continue
		}

		var alreadyMaterialized int
		if err := conn.QueryRowContext(ctx, `
			SELECT EXISTS (
				SELECT 1
				FROM box_virtual_ledger
				WHERE box_id = ? AND automatic_recharge_month = ?
			)
		`, box.id, civilMonth).Scan(&alreadyMaterialized); err != nil {
			return fmt.Errorf("monthly box recharge read identity for box %q month %s: %w", box.id, civilMonth, err)
		}
		if alreadyMaterialized != 0 {
			continue
		}

		automaticAmount := decision.AmountCents
		blocksAfterRecharge := decision.TargetReached
		if automaticAmount <= 0 {
			return fmt.Errorf("monthly box recharge calculated non-positive amount %d for box %q", automaticAmount, box.id)
		}

		result, err := conn.ExecContext(ctx, `
			INSERT INTO box_virtual_ledger (
				id, box_id, reference_date, amount, type, description,
				automatic_recharge_month, created_at
			) VALUES (?, ?, ?, ?, 'RECHARGE', 'Aporte Automático', ?, ?)
			ON CONFLICT(box_id, automatic_recharge_month)
			WHERE automatic_recharge_month IS NOT NULL
			DO NOTHING
		`, uuid.NewString(), box.id, materializedAt, automaticAmount, civilMonth, materializedAt)
		if err != nil {
			return fmt.Errorf("monthly box recharge materialize box %q month %s: %w", box.id, civilMonth, err)
		}
		rowsAffected, err := result.RowsAffected()
		if err != nil {
			return fmt.Errorf("monthly box recharge result box %q month %s: %w", box.id, civilMonth, err)
		}
		if rowsAffected == 0 {
			continue
		}
		if rowsAffected != 1 {
			return fmt.Errorf("monthly box recharge box %q month %s affected %d rows", box.id, civilMonth, rowsAffected)
		}

		if blocksAfterRecharge {
			if err := recordBoxContributionGoalReached(ctx, conn, box.id, materializedAt); err != nil {
				return fmt.Errorf("monthly box recharge record reached target for box %q: %w", box.id, err)
			}
			changed, err := transitionBoxAutomaticContributionState(
				ctx,
				conn,
				box.id,
				BoxAutomaticContributionStateBlocked,
				materializedAt,
			)
			if err != nil {
				return fmt.Errorf("monthly box recharge block box %q after reaching target: %w", box.id, err)
			}
			if !changed {
				return fmt.Errorf("monthly box recharge box %q was not blocked after reaching target", box.id)
			}
		}
	}

	if _, err = conn.ExecContext(ctx, `COMMIT`); err != nil {
		return fmt.Errorf("monthly box recharge commit: %w", err)
	}
	committed = true
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
