package database

import "database/sql"

// Additive only: legacy administrative closures do not imply withdrawals.
// Cross-row financial rules are checked atomically by the Immediate writer.
func migrateInvestmentWithdrawals(tx *sql.Tx) error {
	exists, err := tableExists(tx, "investment_purchases")
	if err != nil || !exists {
		return err
	}
	_, err = tx.Exec(`
 CREATE UNIQUE INDEX IF NOT EXISTS idx_investment_purchases_owner ON investment_purchases(workspace_id, investment_id, id);
 CREATE TABLE IF NOT EXISTS investment_withdrawals (
  id TEXT NOT NULL,
  workspace_id TEXT NOT NULL,
  investment_id TEXT NOT NULL,
  -- Explicit Gregorian calendar arithmetic avoids SQLite date normalization.
  withdrawal_date TEXT NOT NULL CHECK(COALESCE(
   typeof(withdrawal_date) = 'text' AND
   length(CAST(withdrawal_date AS BLOB)) = 10 AND
   withdrawal_date GLOB '[0-9][0-9][0-9][0-9]-[0-9][0-9]-[0-9][0-9]' AND
   CAST(substr(withdrawal_date, 1, 4) AS INTEGER) BETWEEN 1 AND 9999 AND
   CAST(substr(withdrawal_date, 6, 2) AS INTEGER) BETWEEN 1 AND 12 AND
   CAST(substr(withdrawal_date, 9, 2) AS INTEGER) BETWEEN 1 AND
    CASE CAST(substr(withdrawal_date, 6, 2) AS INTEGER)
     WHEN 2 THEN 28 + (CAST(substr(withdrawal_date, 1, 4) AS INTEGER) % 4 = 0 AND
      (CAST(substr(withdrawal_date, 1, 4) AS INTEGER) % 100 != 0 OR
       CAST(substr(withdrawal_date, 1, 4) AS INTEGER) % 400 = 0))
     WHEN 4 THEN 30 WHEN 6 THEN 30 WHEN 9 THEN 30 WHEN 11 THEN 30
     ELSE 31
    END,
   0) = 1),
  received_cents INTEGER NOT NULL CHECK(typeof(received_cents) = 'integer' AND received_cents >= 0),
  mode TEXT NOT NULL CHECK(mode IN ('PARTIAL','TOTAL')),
  created_by TEXT NOT NULL,
  request_id TEXT NOT NULL CHECK(length(request_id) = 36),
  note TEXT NOT NULL DEFAULT '' CHECK(length(note) <= 2000),
  created_at INTEGER NOT NULL DEFAULT(unixepoch()),
  PRIMARY KEY(workspace_id, investment_id, id),
  UNIQUE(workspace_id, investment_id, created_by, request_id),
  FOREIGN KEY(investment_id, workspace_id) REFERENCES investments(id, workspace_id) ON DELETE CASCADE,
  FOREIGN KEY(created_by) REFERENCES users(id) ON DELETE RESTRICT,
  CHECK(mode = 'TOTAL' OR received_cents > 0)
 );
 CREATE TABLE IF NOT EXISTS investment_withdrawal_allocations (
  workspace_id TEXT NOT NULL,
  investment_id TEXT NOT NULL,
  withdrawal_id TEXT NOT NULL,
  purchase_id TEXT NOT NULL,
  received_cents INTEGER NOT NULL CHECK(typeof(received_cents) = 'integer' AND received_cents >= 0),
  principal_cents INTEGER CHECK(principal_cents IS NULL OR (typeof(principal_cents) = 'integer' AND principal_cents >= 0)),
  closes_purchase INTEGER NOT NULL CHECK(closes_purchase IN (0,1)),
  PRIMARY KEY(workspace_id, investment_id, withdrawal_id, purchase_id),
  FOREIGN KEY(workspace_id, investment_id, withdrawal_id) REFERENCES investment_withdrawals(workspace_id, investment_id, id) ON DELETE CASCADE,
  FOREIGN KEY(workspace_id, investment_id, purchase_id) REFERENCES investment_purchases(workspace_id, investment_id, id) ON DELETE RESTRICT,
  CHECK(closes_purchase = 1 OR received_cents > 0)
 );
 CREATE INDEX IF NOT EXISTS idx_investment_withdrawals_date ON investment_withdrawals(workspace_id, investment_id, withdrawal_date, id);
 CREATE INDEX IF NOT EXISTS idx_investment_withdrawals_purchase ON investment_withdrawal_allocations(workspace_id, investment_id, purchase_id);
 `)
	return err
}
