-- F10 historical baseline reconstructed from archived commit 8edd3e84, not current ddl.
-- Columns/tables introduced by migrations are omitted; tests apply real migrations 1..11.
-- In particular categories obtains its checked NOT NULL macro_group from v7,
-- parent_id from v8; Business and contacts come from v11.
-- Historical boxes.category_id uses CASCADE (changed by v41).
PRAGMA foreign_keys=ON;

CREATE TABLE IF NOT EXISTS users (
    id              TEXT PRIMARY KEY,
    name            TEXT NOT NULL,
    email           TEXT NOT NULL UNIQUE,
    password_hash   TEXT NOT NULL,
    created_at      INTEGER NOT NULL DEFAULT (unixepoch()),
    updated_at      INTEGER NOT NULL DEFAULT (unixepoch())
);

CREATE TABLE IF NOT EXISTS workspaces (
    id           TEXT PRIMARY KEY,
    name         TEXT NOT NULL,
    description  TEXT NOT NULL DEFAULT '',
    created_at     INTEGER NOT NULL DEFAULT (unixepoch()),
    updated_at     INTEGER NOT NULL DEFAULT (unixepoch())
);

CREATE TABLE IF NOT EXISTS workspace_members (
    workspace_id TEXT NOT NULL,
    user_id      TEXT NOT NULL,
    role         TEXT NOT NULL DEFAULT 'USER' CHECK (role IN ('ADMIN', 'MANAGER', 'USER')),
    joined_at    INTEGER NOT NULL DEFAULT (unixepoch()),
    PRIMARY KEY (workspace_id, user_id),
    FOREIGN KEY (workspace_id) REFERENCES workspaces(id) ON DELETE CASCADE,
    FOREIGN KEY (user_id)      REFERENCES users(id)      ON DELETE CASCADE
);

CREATE TABLE IF NOT EXISTS accounts (
    id              TEXT PRIMARY KEY,
    workspace_id    TEXT    NOT NULL,
    name            TEXT    NOT NULL,
    type            TEXT    NOT NULL CHECK (type IN ('CHECKING', 'SAVINGS', 'INVESTMENT', 'CREDIT_CARD')),
    initial_balance INTEGER NOT NULL DEFAULT 0,
    current_balance INTEGER NOT NULL DEFAULT 0,
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
    created_at   INTEGER NOT NULL DEFAULT (unixepoch()),
    FOREIGN KEY (workspace_id) REFERENCES workspaces(id) ON DELETE CASCADE
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
    installment_number      INTEGER NOT NULL DEFAULT 1,
    total_installments      INTEGER NOT NULL DEFAULT 1,
    parent_id               TEXT,
    created_at              INTEGER NOT NULL DEFAULT (unixepoch()),
    updated_at              INTEGER NOT NULL DEFAULT (unixepoch()),
    FOREIGN KEY (workspace_id)           REFERENCES workspaces(id)  ON DELETE CASCADE,
    FOREIGN KEY (user_id)                REFERENCES users(id)       ON DELETE RESTRICT,
    FOREIGN KEY (account_id)             REFERENCES accounts(id)    ON DELETE RESTRICT,
    FOREIGN KEY (destination_account_id) REFERENCES accounts(id)    ON DELETE SET NULL,
    FOREIGN KEY (category_id)            REFERENCES categories(id)  ON DELETE SET NULL,
    FOREIGN KEY (invoice_id)             REFERENCES invoices(id)    ON DELETE SET NULL,
    FOREIGN KEY (invoice_override_id)    REFERENCES invoices(id)    ON DELETE SET NULL,
    FOREIGN KEY (parent_id)              REFERENCES transactions(id) ON DELETE SET NULL
);

CREATE TABLE IF NOT EXISTS cost_limits (
    id                 TEXT PRIMARY KEY,
    workspace_id       TEXT    NOT NULL,
    category_id        TEXT    NOT NULL,
    max_amount_monthly INTEGER NOT NULL CHECK (max_amount_monthly > 0),
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
    created_at       INTEGER NOT NULL DEFAULT (unixepoch()),
    updated_at       INTEGER NOT NULL DEFAULT (unixepoch()),
    FOREIGN KEY (workspace_id) REFERENCES workspaces(id) ON DELETE CASCADE,
    FOREIGN KEY (category_id)  REFERENCES categories(id) ON DELETE CASCADE
);

CREATE TABLE IF NOT EXISTS box_virtual_ledger (
    id             TEXT PRIMARY KEY,
    box_id         TEXT    NOT NULL,
    amount         INTEGER NOT NULL CHECK (amount > 0),
    type           TEXT    NOT NULL CHECK (type IN ('RECHARGE', 'BONUS')),
    description    TEXT    NOT NULL DEFAULT '',
    reference_date INTEGER NOT NULL,
    created_at     INTEGER NOT NULL DEFAULT (unixepoch()),
    FOREIGN KEY (box_id) REFERENCES boxes(id) ON DELETE CASCADE
);
