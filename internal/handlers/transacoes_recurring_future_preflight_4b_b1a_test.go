package handlers

import (
	"bytes"
	"database/sql"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"testing"
	"time"

	"github.com/contabase-app/contabase/internal/database"
)

// ---------- helpers ----------

func pfSeedCheckingRule(t *testing.T, db *sql.DB, suffix string) (wsID, userID, ruleID, seriesID string) {
	t.Helper()
	wsID = "ws-pf-" + suffix
	userID = "u-pf-" + suffix
	now := time.Now().Unix()
	execTestSQL(t, db, `INSERT OR IGNORE INTO users (id, name, email, password_hash, created_at, updated_at) VALUES (?, ?, ?, 'hash', ?, ?)`, userID, "User "+suffix, userID+"@e.com", now, now)
	execTestSQL(t, db, `INSERT OR IGNORE INTO workspaces (id, name, type, description, created_at, updated_at) VALUES (?, ?, 'business', '', ?, ?)`, wsID, "WS "+suffix, now, now)
	execTestSQL(t, db, `INSERT OR IGNORE INTO workspace_members (workspace_id, user_id, role, joined_at) VALUES (?, ?, 'ADMIN', ?)`, wsID, userID, now)
	execTestSQL(t, db, `INSERT OR IGNORE INTO accounts (id, workspace_id, name, type, initial_balance, current_balance, created_at, updated_at) VALUES ('acc-pf-chk', ?, 'Checking', 'CHECKING', 0, 0, ?, ?)`, wsID, now, now)
	execTestSQL(t, db, `INSERT OR IGNORE INTO accounts (id, workspace_id, name, type, initial_balance, current_balance, created_at, updated_at) VALUES ('acc-pf-card', ?, 'Card', 'CREDIT_CARD', 0, 0, ?, ?)`, wsID, now, now)
	execTestSQL(t, db, `INSERT OR IGNORE INTO categories (id, workspace_id, name, icon, color, type, macro_group, created_at) VALUES ('cat-pf', ?, 'ExpCat', 'tag', '#ef4444', 'EXPENSE', '', ?)`, wsID, now)
	ruleID = "rule-pf-" + suffix
	seriesID = "series-pf-" + suffix
	execTestSQL(t, db, `INSERT INTO recurring_series (id, workspace_id, user_id, active, created_at, updated_at) VALUES (?, ?, ?, 1, ?, ?)`, seriesID, wsID, userID, now, now)
	execTestSQL(t, db, `INSERT INTO recurring_rules (id, workspace_id, user_id, account_id, category_id, type, amount, description, start_date, start_date_civil, starts_at, starts_at_civil, frequency, default_payment_status, active, series_id, effective_start_sequence, total_occurrences, created_at, updated_at) VALUES (?, ?, ?, 'acc-pf-chk', 'cat-pf', 'EXPENSE', 15000, ?, ?, ?, ?, ?, 'MONTHLY', 'PENDING', 1, ?, 1, 3, ?, ?)`, ruleID, wsID, userID, suffix, testUnixDate("2026-06-05"), "2026-06-05", testUnixDate("2026-06-05"), "2026-06-05", seriesID, now, now)
	return
}

func pfSeedLegacyRule(t *testing.T, db *sql.DB, suffix string) (wsID, userID, ruleID string) {
	t.Helper()
	wsID = "ws-pf-" + suffix
	userID = "u-pf-" + suffix
	now := time.Now().Unix()
	execTestSQL(t, db, `INSERT OR IGNORE INTO users (id, name, email, password_hash, created_at, updated_at) VALUES (?, ?, ?, 'hash', ?, ?)`, userID, "User "+suffix, userID+"@e.com", now, now)
	execTestSQL(t, db, `INSERT OR IGNORE INTO workspaces (id, name, type, description, created_at, updated_at) VALUES (?, ?, 'business', '', ?, ?)`, wsID, "WS "+suffix, now, now)
	execTestSQL(t, db, `INSERT OR IGNORE INTO workspace_members (workspace_id, user_id, role, joined_at) VALUES (?, ?, 'ADMIN', ?)`, wsID, userID, now)
	execTestSQL(t, db, `INSERT OR IGNORE INTO accounts (id, workspace_id, name, type, initial_balance, current_balance, created_at, updated_at) VALUES ('acc-pf-chk', ?, 'Checking', 'CHECKING', 0, 0, ?, ?)`, wsID, now, now)
	execTestSQL(t, db, `INSERT OR IGNORE INTO categories (id, workspace_id, name, icon, color, type, macro_group, created_at) VALUES ('cat-pf', ?, 'ExpCat', 'tag', '#ef4444', 'EXPENSE', '', ?)`, wsID, now)
	ruleID = "rule-pf-" + suffix
	execTestSQL(t, db, `INSERT INTO recurring_rules (id, workspace_id, user_id, account_id, category_id, type, amount, description, start_date, frequency, default_payment_status, active, effective_start_sequence, total_occurrences, created_at, updated_at) VALUES (?, ?, ?, 'acc-pf-chk', 'cat-pf', 'EXPENSE', 15000, ?, ?, 'MONTHLY', 'PENDING', 1, 1, 3, ?, ?)`, ruleID, wsID, userID, suffix, testUnixDate("2026-06-05"), now, now)
	return
}

func pfSeedOccurrences(t *testing.T, db *sql.DB, wsID, userID, seriesID, ruleID, desc string) (seq1ID, seq2ID, seq3ID string) {
	t.Helper()
	now := time.Now().Unix()
	seq1ID = "tx-pf-" + desc + "-1"
	seq2ID = "tx-pf-" + desc + "-2"
	seq3ID = "tx-pf-" + desc + "-3"
	for i, id := range []string{seq1ID, seq2ID, seq3ID} {
		seq := int64(i + 1)
		var serVal interface{}
		if seriesID != "" {
			serVal = seriesID
		}
		dateVal := testUnixDate("2026-06-05")
		if i == 1 {
			dateVal = testUnixDate("2026-07-05")
		}
		if i == 2 {
			dateVal = testUnixDate("2026-08-05")
		}
		execTestSQL(t, db, `INSERT INTO transactions (id, workspace_id, user_id, account_id, category_id, type, amount, date, description, status, installment_number, total_installments, recurring_rule_id, recurrence_sequence, series_id, created_at, updated_at) VALUES (?, ?, ?, 'acc-pf-chk', 'cat-pf', 'EXPENSE', 15000, ?, ?, 'pending', 1, 1, ?, ?, ?, ?, ?)`, id, wsID, userID, dateVal, desc, ruleID, seq, serVal, now, now)
	}
	return
}

func pfRequest(t *testing.T, wsID, userID, txID string, db *sql.DB, extra url.Values) *httptest.ResponseRecorder {
	t.Helper()
	formValues := url.Values{
		"valor":            {"150.00"},
		"data":             {"2026-07-05"},
		"tipo":             {"despesa"},
		"origem_conta_id":  {"acc-pf-chk"},
		"categoria_id":     {"cat-pf"},
		"status_pagamento": {"pending"},
		"descricao":        {"PF Test"},
		"escopo":           {"future"},
	}
	for k, vals := range extra {
		formValues[k] = vals
	}

	var buf bytes.Buffer
	w := multipart.NewWriter(&buf)
	for k, vals := range formValues {
		for _, v := range vals {
			w.WriteField(k, v)
		}
	}
	w.Close()

	req := httptest.NewRequest(http.MethodPost, "/transacoes/"+txID+"/atualizar", &buf)
	req.Header.Set("Content-Type", w.FormDataContentType())

	h := TransactionHandler{DB: db, WorkspaceID: wsID, UserID: userID, Templates: testOOBTemplatesSeries(t)}
	rr := httptest.NewRecorder()
	h.HandleAtualizarTransacao(rr, req, txID)
	return rr
}

// ---------- CURRENT PROTECTED ----------

func TestRecurringFuturePreflight_BlockedCurrentPaid(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	wsID, userID, ruleID, seriesID := pfSeedCheckingRule(t, db, "cur_paid")
	_, seq2ID, seq3ID := pfSeedOccurrences(t, db, wsID, userID, seriesID, ruleID, "cur_paid")
	execTestSQL(t, db, `UPDATE transactions SET status='paid' WHERE id=?`, seq2ID)
	rr := pfRequest(t, wsID, userID, seq2ID, db, nil)
	if rr.Code != http.StatusConflict {
		t.Fatalf("status=%d, want 409", rr.Code)
	}
	_ = seq3ID
}

func TestRecurringFuturePreflight_BlockedCurrentException(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	wsID, userID, ruleID, seriesID := pfSeedCheckingRule(t, db, "cur_exc")
	_, seq2ID, seq3ID := pfSeedOccurrences(t, db, wsID, userID, seriesID, ruleID, "cur_exc")
	execTestSQL(t, db, `UPDATE transactions SET is_recurring_exception=1 WHERE id=?`, seq2ID)
	rr := pfRequest(t, wsID, userID, seq2ID, db, nil)
	if rr.Code != http.StatusConflict {
		t.Fatalf("status=%d, want 409", rr.Code)
	}
	_ = seq3ID
}

func TestRecurringFuturePreflight_BlockedCurrentHistory(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	wsID, userID, ruleID, seriesID := pfSeedCheckingRule(t, db, "cur_hist")
	_, seq2ID, seq3ID := pfSeedOccurrences(t, db, wsID, userID, seriesID, ruleID, "cur_hist")
	now := time.Now().Unix()
	execTestSQL(t, db, `INSERT INTO transaction_status_history (id, transaction_id, workspace_id, user_id, previous_status, new_status, created_at) VALUES ('h-pf-cur', ?, ?, ?, 'paid', 'pending', ?)`, seq2ID, wsID, userID, now)
	rr := pfRequest(t, wsID, userID, seq2ID, db, nil)
	if rr.Code != http.StatusConflict {
		t.Fatalf("status=%d, want 409", rr.Code)
	}
	_ = seq3ID
}

func TestRecurringFuturePreflight_BlockedCurrentInvoice(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	wsID, userID, ruleID, seriesID := pfSeedCheckingRule(t, db, "cur_inv")
	_, seq2ID, seq3ID := pfSeedOccurrences(t, db, wsID, userID, seriesID, ruleID, "cur_inv")
	now := time.Now().Unix()
	execTestSQL(t, db, `INSERT INTO invoices (id, account_id, reference, closing_date, due_date, status, created_at) VALUES ('inv-pf-cur', 'acc-pf-card', '2026-07', ?, ?, 'OPEN', ?)`, testUnixDate("2026-06-20"), testUnixDate("2026-07-10"), now)
	execTestSQL(t, db, `UPDATE transactions SET account_id='acc-pf-card', invoice_id='inv-pf-cur' WHERE id=?`, seq2ID)
	rr := pfRequest(t, wsID, userID, seq2ID, db, url.Values{"origem_conta_id": {"acc-pf-card"}})
	if rr.Code != http.StatusConflict {
		t.Fatalf("status=%d, want 409", rr.Code)
	}
	_ = seq3ID
}

// ---------- FUTURE PROTECTED ----------

func TestRecurringFuturePreflight_BlockedFuturePaid(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	wsID, userID, ruleID, seriesID := pfSeedCheckingRule(t, db, "fut_paid")
	_, seq2ID, seq3ID := pfSeedOccurrences(t, db, wsID, userID, seriesID, ruleID, "fut_paid")
	execTestSQL(t, db, `UPDATE transactions SET status='paid' WHERE id=?`, seq3ID)
	rr := pfRequest(t, wsID, userID, seq2ID, db, nil)
	if rr.Code != http.StatusConflict {
		t.Fatalf("status=%d, want 409", rr.Code)
	}
}

func TestRecurringFuturePreflight_BlockedFutureException(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	wsID, userID, ruleID, seriesID := pfSeedCheckingRule(t, db, "fut_exc")
	_, seq2ID, seq3ID := pfSeedOccurrences(t, db, wsID, userID, seriesID, ruleID, "fut_exc")
	execTestSQL(t, db, `UPDATE transactions SET is_recurring_exception=1 WHERE id=?`, seq3ID)
	rr := pfRequest(t, wsID, userID, seq2ID, db, nil)
	if rr.Code != http.StatusConflict {
		t.Fatalf("status=%d, want 409", rr.Code)
	}
}

func TestRecurringFuturePreflight_BlockedFutureHistory(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	wsID, userID, ruleID, seriesID := pfSeedCheckingRule(t, db, "fut_hist")
	_, seq2ID, seq3ID := pfSeedOccurrences(t, db, wsID, userID, seriesID, ruleID, "fut_hist")
	now := time.Now().Unix()
	execTestSQL(t, db, `INSERT INTO transaction_status_history (id, transaction_id, workspace_id, user_id, previous_status, new_status, created_at) VALUES ('h-pf-fut', ?, ?, ?, 'pending', 'paid', ?)`, seq3ID, wsID, userID, now)
	rr := pfRequest(t, wsID, userID, seq2ID, db, nil)
	if rr.Code != http.StatusConflict {
		t.Fatalf("status=%d, want 409", rr.Code)
	}
}

func TestRecurringFuturePreflight_BlockedFutureInvoice(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	wsID, userID, ruleID, seriesID := pfSeedCheckingRule(t, db, "fut_inv")
	_, seq2ID, seq3ID := pfSeedOccurrences(t, db, wsID, userID, seriesID, ruleID, "fut_inv")
	now := time.Now().Unix()
	execTestSQL(t, db, `INSERT INTO invoices (id, account_id, reference, closing_date, due_date, status, created_at) VALUES ('inv-pf-fut', 'acc-pf-card', '2026-08', ?, ?, 'OPEN', ?)`, testUnixDate("2026-07-20"), testUnixDate("2026-08-10"), now)
	execTestSQL(t, db, `UPDATE transactions SET account_id='acc-pf-card', invoice_id='inv-pf-fut' WHERE id=?`, seq3ID)
	rr := pfRequest(t, wsID, userID, seq2ID, db, url.Values{"origem_conta_id": {"acc-pf-card"}})
	if rr.Code != http.StatusConflict {
		t.Fatalf("status=%d, want 409", rr.Code)
	}
}

// ---------- PAYMENT FIELD EDGE CASES ----------

func TestRecurringFuturePreflight_BlockedFutureStatusPaidWithoutPaidAt(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	wsID, userID, ruleID, seriesID := pfSeedCheckingRule(t, db, "fut_spa")
	_, seq2ID, seq3ID := pfSeedOccurrences(t, db, wsID, userID, seriesID, ruleID, "fut_spa")
	execTestSQL(t, db, `UPDATE transactions SET status='paid' WHERE id=?`, seq3ID)
	rr := pfRequest(t, wsID, userID, seq2ID, db, nil)
	if rr.Code != http.StatusConflict {
		t.Fatalf("status=%d, want 409", rr.Code)
	}
}

func TestRecurringFuturePreflight_BlockedFuturePaidAtPendingStatus(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	wsID, userID, ruleID, seriesID := pfSeedCheckingRule(t, db, "fut_pat")
	_, seq2ID, seq3ID := pfSeedOccurrences(t, db, wsID, userID, seriesID, ruleID, "fut_pat")
	now := time.Now().Unix()
	execTestSQL(t, db, `UPDATE transactions SET paid_at=? WHERE id=?`, now, seq3ID)
	rr := pfRequest(t, wsID, userID, seq2ID, db, nil)
	if rr.Code != http.StatusConflict {
		t.Fatalf("status=%d, want 409", rr.Code)
	}
}

func TestRecurringFuturePreflight_BlockedFuturePaidDateCivilPendingStatus(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	wsID, userID, ruleID, seriesID := pfSeedCheckingRule(t, db, "fut_pdc")
	_, seq2ID, seq3ID := pfSeedOccurrences(t, db, wsID, userID, seriesID, ruleID, "fut_pdc")
	execTestSQL(t, db, `UPDATE transactions SET paid_date_civil='2026-06-05' WHERE id=?`, seq3ID)
	rr := pfRequest(t, wsID, userID, seq2ID, db, nil)
	if rr.Code != http.StatusConflict {
		t.Fatalf("status=%d, want 409", rr.Code)
	}
}

// ---------- LEGACY RULE (NO series_id) ----------

func TestRecurringFuturePreflight_LegacyRuleBlockedFuturePaid(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	wsID, userID, ruleID := pfSeedLegacyRule(t, db, "leg_blk")
	_, seq2ID, seq3ID := pfSeedOccurrences(t, db, wsID, userID, "", ruleID, "leg_blk")
	execTestSQL(t, db, `UPDATE transactions SET status='paid' WHERE id=?`, seq3ID)
	rr := pfRequest(t, wsID, userID, seq2ID, db, nil)
	if rr.Code != http.StatusConflict {
		t.Fatalf("status=%d, want 409 (legacy rule rejected)", rr.Code)
	}
}

func TestRecurringFuturePreflight_LegacyRuleMutablePasses(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	wsID, userID, ruleID := pfSeedLegacyRule(t, db, "leg_ok")
	_, seq2ID, seq3ID := pfSeedOccurrences(t, db, wsID, userID, "", ruleID, "leg_ok")
	rr := pfRequest(t, wsID, userID, seq2ID, db, nil)
	if rr.Code != http.StatusOK {
		t.Fatalf("status=%d body=%q, want 200 (mutable legacy should pass)", rr.Code, rr.Body.String())
	}
	_ = seq3ID
}

// ---------- INACTIVE RULE ----------

func TestRecurringFuturePreflight_InactiveRuleRejected(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	wsID, userID, ruleID, seriesID := pfSeedCheckingRule(t, db, "inactive")
	_, seq2ID, seq3ID := pfSeedOccurrences(t, db, wsID, userID, seriesID, ruleID, "inactive")
	execTestSQL(t, db, `UPDATE recurring_rules SET active=0 WHERE id=?`, ruleID)
	rr := pfRequest(t, wsID, userID, seq2ID, db, nil)
	if rr.Code != http.StatusConflict {
		t.Fatalf("status=%d, want 409 (inactive rule)", rr.Code)
	}
	_ = seq3ID
}

// ---------- MUTABLE SUCCESS ----------

func TestRecurringFuturePreflight_MutablePasses(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	wsID, userID, ruleID, seriesID := pfSeedCheckingRule(t, db, "mut_ok")
	_, seq2ID, seq3ID := pfSeedOccurrences(t, db, wsID, userID, seriesID, ruleID, "mut_ok")
	rr := pfRequest(t, wsID, userID, seq2ID, db, nil)
	if rr.Code != http.StatusOK {
		t.Fatalf("status=%d body=%q, want 200", rr.Code, rr.Body.String())
	}
	_ = seq3ID
}

// ---------- CROSS-WORKSPACE ----------

func TestRecurringFuturePreflight_CrossWorkspaceRejected(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	wsA, userA, ruleA, seriesA := pfSeedCheckingRule(t, db, "cws_a")
	_, seq2A, seq3A := pfSeedOccurrences(t, db, wsA, userA, seriesA, ruleA, "cws_a")
	wsB, userB, _, _ := pfSeedCheckingRule(t, db, "cws_b")

	rr := pfRequest(t, wsB, userB, seq2A, db, nil)
	if rr.Code != http.StatusConflict && rr.Code != http.StatusNotFound && rr.Code != http.StatusInternalServerError {
	}
	if rr.Code == http.StatusOK {
		t.Fatal("cross-workspace edit should be rejected")
	}
	_ = seq3A
}

// ---------- ZERO-WRITES PROOF (TEMP TRIGGERS) ----------

func TestRecurringFuturePreflight_ZeroWritesBefore409(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	wsID, userID, ruleID := pfSeedLegacyRule(t, db, "zw_leg")
	_, seq2ID, seq3ID := pfSeedOccurrences(t, db, wsID, userID, "", ruleID, "zw_leg")
	execTestSQL(t, db, `UPDATE transactions SET status='paid' WHERE id=?`, seq3ID)

	execTestSQL(t, db, `CREATE TEMP TRIGGER IF NOT EXISTS pf_s_ins BEFORE INSERT ON recurring_series BEGIN SELECT RAISE(ABORT, 'write before preflight: series insert'); END`)
	execTestSQL(t, db, `CREATE TEMP TRIGGER IF NOT EXISTS pf_r_upd BEFORE UPDATE ON recurring_rules BEGIN SELECT RAISE(ABORT, 'write before preflight: rule update'); END`)
	execTestSQL(t, db, `CREATE TEMP TRIGGER IF NOT EXISTS pf_t_upd BEFORE UPDATE ON transactions BEGIN SELECT RAISE(ABORT, 'write before preflight: tx update'); END`)

	rr := pfRequest(t, wsID, userID, seq2ID, db, nil)
	if rr.Code != http.StatusConflict {
		t.Fatalf("status=%d, want 409", rr.Code)
	}

	execTestSQL(t, db, `DROP TRIGGER IF EXISTS pf_s_ins`)
	execTestSQL(t, db, `DROP TRIGGER IF EXISTS pf_r_upd`)
	execTestSQL(t, db, `DROP TRIGGER IF EXISTS pf_t_upd`)

	var seriesCount int
	db.QueryRow(`SELECT COUNT(1) FROM recurring_series WHERE workspace_id=?`, wsID).Scan(&seriesCount)
	if seriesCount != 0 {
		t.Fatalf("recurring_series created despite block: %d rows", seriesCount)
	}

	var active int
	db.QueryRow(`SELECT active FROM recurring_rules WHERE id=?`, ruleID).Scan(&active)
	if active != 1 {
		t.Fatalf("rule active=%d, want 1", active)
	}

	_ = seq2ID
}

// ---------- LEGACY: INCOMPATIBLE SERIES REJECTED ----------

func TestRecurringFuturePreflight_LegacyRejectsIncompatibleNonEmptySeries(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	wsID, userID, ruleID := pfSeedLegacyRule(t, db, "leg_incomp")
	_, seq2ID, _ := pfSeedOccurrences(t, db, wsID, userID, "", ruleID, "leg_incomp")

	now := time.Now().Unix()
	execTestSQL(t, db, `INSERT OR IGNORE INTO recurring_series (id, workspace_id, user_id, active, created_at, updated_at) VALUES ('incompativel', ?, ?, 1, ?, ?)`, wsID, userID, now, now)
	execTestSQL(t, db, `INSERT INTO transactions (id, workspace_id, user_id, account_id, category_id, type, amount, date, description, status, installment_number, total_installments, recurring_rule_id, recurrence_sequence, series_id, created_at, updated_at) VALUES ('tx-pf-leg-incomp-extra', ?, ?, 'acc-pf-chk', 'cat-pf', 'EXPENSE', 15000, ?, 'leg-incomp-extra', 'pending', 1, 1, ?, 4, 'incompativel', ?, ?)`, wsID, userID, testUnixDate("2026-09-05"), ruleID, now, now)

	execTestSQL(t, db, `CREATE TEMP TRIGGER IF NOT EXISTS pf_s_ins BEFORE INSERT ON recurring_series BEGIN SELECT RAISE(ABORT, 'write before preflight: series insert'); END`)
	execTestSQL(t, db, `CREATE TEMP TRIGGER IF NOT EXISTS pf_r_upd BEFORE UPDATE ON recurring_rules BEGIN SELECT RAISE(ABORT, 'write before preflight: rule update'); END`)
	execTestSQL(t, db, `CREATE TEMP TRIGGER IF NOT EXISTS pf_t_upd BEFORE UPDATE ON transactions BEGIN SELECT RAISE(ABORT, 'write before preflight: tx update'); END`)

	rr := pfRequest(t, wsID, userID, seq2ID, db, nil)
	if rr.Code != http.StatusConflict {
		t.Fatalf("status=%d, want 409 (incompatible series_id)", rr.Code)
	}

	execTestSQL(t, db, `DROP TRIGGER IF EXISTS pf_s_ins`)
	execTestSQL(t, db, `DROP TRIGGER IF EXISTS pf_r_upd`)
	execTestSQL(t, db, `DROP TRIGGER IF EXISTS pf_t_upd`)

	var seriesCount int
	db.QueryRow(`SELECT COUNT(1) FROM recurring_series WHERE workspace_id=?`, wsID).Scan(&seriesCount)
	if seriesCount != 1 {
		t.Fatalf("recurring_series altered: %d rows (want 1, only the incompatible series)", seriesCount)
	}

	var childRuleCount int
	db.QueryRow(`SELECT COUNT(1) FROM recurring_rules WHERE parent_rule_id=?`, ruleID).Scan(&childRuleCount)
	if childRuleCount != 0 {
		t.Fatalf("child rule created: %d", childRuleCount)
	}

	var seriesAfter sql.NullString
	db.QueryRow(`SELECT series_id FROM transactions WHERE id='tx-pf-leg-incomp-extra'`).Scan(&seriesAfter)
	if !seriesAfter.Valid || seriesAfter.String != "incompativel" {
		t.Fatalf("incompatible series_id was altered: %v", seriesAfter)
	}

	var statusAfter string
	db.QueryRow(`SELECT status FROM transactions WHERE id='tx-pf-leg-incomp-extra'`).Scan(&statusAfter)
	if statusAfter != "pending" {
		t.Fatalf("transaction status altered: %q", statusAfter)
	}
}

// ---------- LEGACY: ACCEPT LOGICAL SERIES ID ----------

func TestRecurringFuturePreflight_LegacyAcceptsLogicalSeriesID(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	wsID, userID, ruleID := pfSeedLegacyRule(t, db, "leg_lsid")

	now := time.Now().Unix()
	execTestSQL(t, db, `INSERT INTO recurring_series (id, workspace_id, user_id, active, created_at, updated_at) VALUES (?, ?, ?, 1, ?, ?)`, ruleID, wsID, userID, now, now)

	seq1ID, seq2ID, seq3ID := "tx-pf-leg-lsid-1", "tx-pf-leg-lsid-2", "tx-pf-leg-lsid-3"
	for i, id := range []string{seq1ID, seq2ID, seq3ID} {
		seq := int64(i + 1)
		dateVal := testUnixDate("2026-06-05")
		if i == 1 {
			dateVal = testUnixDate("2026-07-05")
		}
		if i == 2 {
			dateVal = testUnixDate("2026-08-05")
		}
		execTestSQL(t, db, `INSERT INTO transactions (id, workspace_id, user_id, account_id, category_id, type, amount, date, description, status, installment_number, total_installments, recurring_rule_id, recurrence_sequence, series_id, created_at, updated_at) VALUES (?, ?, ?, 'acc-pf-chk', 'cat-pf', 'EXPENSE', 15000, ?, 'leg-lsid', 'pending', 1, 1, ?, ?, ?, ?, ?)`, id, wsID, userID, dateVal, ruleID, seq, ruleID, now, now)
	}

	rr := pfRequest(t, wsID, userID, seq2ID, db, nil)
	if rr.Code != http.StatusOK {
		t.Fatalf("status=%d body=%q, want 200 (series_id=ruleID is valid logical identity)", rr.Code, rr.Body.String())
	}
	_ = seq3ID
}

// ---------- MODERN RULE MUTABLE ----------

func TestRecurringFuturePreflight_ModernRuleMutablePasses(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	wsID, userID, ruleID, seriesID := pfSeedCheckingRule(t, db, "modern_ok")
	_, seq2ID, seq3ID := pfSeedOccurrences(t, db, wsID, userID, seriesID, ruleID, "modern_ok")
	rr := pfRequest(t, wsID, userID, seq2ID, db, nil)
	if rr.Code != http.StatusOK {
		t.Fatalf("status=%d body=%q, want 200", rr.Code, rr.Body.String())
	}
	_ = seq3ID
}

// ---------- MODERN: NULL-SERIES PROTECTED FUTURE ----------

func TestRecurringFuturePreflight_ModernIncludesNullSeriesProtectedFuture(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	wsID, userID, ruleID, seriesID := pfSeedCheckingRule(t, db, "mod_ns_prot")
	_, seq2ID, _ := pfSeedOccurrences(t, db, wsID, userID, seriesID, ruleID, "mod_ns_prot")

	now := time.Now().Unix()
	execTestSQL(t, db, `INSERT INTO transactions (id, workspace_id, user_id, account_id, category_id, type, amount, date, description, status, installment_number, total_installments, recurring_rule_id, recurrence_sequence, series_id, created_at, updated_at) VALUES ('tx-pf-mod-ns-prot-extra', ?, ?, 'acc-pf-chk', 'cat-pf', 'EXPENSE', 15000, ?, 'mod-ns-prot-extra', 'paid', 1, 1, ?, 4, NULL, ?, ?)`, wsID, userID, testUnixDate("2026-09-05"), ruleID, now, now)

	execTestSQL(t, db, `CREATE TEMP TRIGGER IF NOT EXISTS pf_s_ins BEFORE INSERT ON recurring_series BEGIN SELECT RAISE(ABORT, 'write before preflight: series insert'); END`)
	execTestSQL(t, db, `CREATE TEMP TRIGGER IF NOT EXISTS pf_r_upd BEFORE UPDATE ON recurring_rules BEGIN SELECT RAISE(ABORT, 'write before preflight: rule update'); END`)
	execTestSQL(t, db, `CREATE TEMP TRIGGER IF NOT EXISTS pf_t_upd BEFORE UPDATE ON transactions BEGIN SELECT RAISE(ABORT, 'write before preflight: tx update'); END`)

	rr := pfRequest(t, wsID, userID, seq2ID, db, nil)
	if rr.Code != http.StatusConflict {
		t.Fatalf("status=%d, want 409 (null-series future is protected)", rr.Code)
	}

	execTestSQL(t, db, `DROP TRIGGER IF EXISTS pf_s_ins`)
	execTestSQL(t, db, `DROP TRIGGER IF EXISTS pf_r_upd`)
	execTestSQL(t, db, `DROP TRIGGER IF EXISTS pf_t_upd`)

	var seriesAfter sql.NullString
	db.QueryRow(`SELECT series_id FROM transactions WHERE id='tx-pf-mod-ns-prot-extra'`).Scan(&seriesAfter)
	if seriesAfter.Valid {
		t.Fatalf("null-series future was normalized despite block: series_id=%q", seriesAfter.String)
	}

	var childCount int
	db.QueryRow(`SELECT COUNT(1) FROM recurring_rules WHERE parent_rule_id = ?`, ruleID).Scan(&childCount)
	if childCount != 0 {
		t.Fatalf("child rule created: %d", childCount)
	}

	var active int
	db.QueryRow(`SELECT active FROM recurring_rules WHERE id=?`, ruleID).Scan(&active)
	if active != 1 {
		t.Fatalf("rule deactivated: active=%d", active)
	}

	var seriesCount int
	db.QueryRow(`SELECT COUNT(1) FROM recurring_series WHERE workspace_id=?`, wsID).Scan(&seriesCount)
	if seriesCount != 1 {
		t.Fatalf("recurring_series altered: %d rows (want 1)", seriesCount)
	}
}

// ---------- MODERN: NULL-SERIES MUTABLE FUTURE ----------

func TestRecurringFuturePreflight_ModernIncludesNullSeriesMutableFuture(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	wsID, userID, ruleID, seriesID := pfSeedCheckingRule(t, db, "mod_ns_mut")
	_, seq2ID, _ := pfSeedOccurrences(t, db, wsID, userID, seriesID, ruleID, "mod_ns_mut")

	now := time.Now().Unix()
	extraID := "tx-pf-mod-ns-mut-extra"
	execTestSQL(t, db, `INSERT INTO transactions (id, workspace_id, user_id, account_id, category_id, type, amount, date, description, status, installment_number, total_installments, recurring_rule_id, recurrence_sequence, series_id, created_at, updated_at) VALUES (?, ?, ?, 'acc-pf-chk', 'cat-pf', 'EXPENSE', 15000, ?, 'mod-ns-mut-extra', 'pending', 1, 1, ?, 4, NULL, ?, ?)`, extraID, wsID, userID, testUnixDate("2026-09-05"), ruleID, now, now)

	rr := pfRequest(t, wsID, userID, seq2ID, db, nil)
	if rr.Code != http.StatusOK {
		t.Fatalf("status=%d body=%q, want 200 (null-series future is mutable)", rr.Code, rr.Body.String())
	}

	var seriesAfter sql.NullString
	db.QueryRow(`SELECT series_id FROM transactions WHERE id=?`, extraID).Scan(&seriesAfter)
	if !seriesAfter.Valid || seriesAfter.String != seriesID {
		t.Fatalf("null-series future not normalized: series_id=%v, want %q", seriesAfter, seriesID)
	}

	var seriesCount int
	db.QueryRow(`SELECT COUNT(1) FROM recurring_series WHERE workspace_id=?`, wsID).Scan(&seriesCount)
	if seriesCount != 1 {
		t.Fatalf("recurring_series altered: %d rows (want 1)", seriesCount)
	}
}

// ---------- MODERN: INCOMPATIBLE SERIES REJECTED ----------

func TestRecurringFuturePreflight_ModernRejectsIncompatibleSeriesForSameRule(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	wsID, userID, ruleID, seriesID := pfSeedCheckingRule(t, db, "mod_incomp")
	_, seq2ID, _ := pfSeedOccurrences(t, db, wsID, userID, seriesID, ruleID, "mod_incomp")

	now := time.Now().Unix()
	execTestSQL(t, db, `INSERT OR IGNORE INTO recurring_series (id, workspace_id, user_id, active, created_at, updated_at) VALUES ('incompativel', ?, ?, 1, ?, ?)`, wsID, userID, now, now)
	execTestSQL(t, db, `INSERT INTO transactions (id, workspace_id, user_id, account_id, category_id, type, amount, date, description, status, installment_number, total_installments, recurring_rule_id, recurrence_sequence, series_id, created_at, updated_at) VALUES ('tx-pf-mod-incomp-extra', ?, ?, 'acc-pf-chk', 'cat-pf', 'EXPENSE', 15000, ?, 'mod-incomp-extra', 'pending', 1, 1, ?, 4, 'incompativel', ?, ?)`, wsID, userID, testUnixDate("2026-09-05"), ruleID, now, now)

	execTestSQL(t, db, `CREATE TEMP TRIGGER IF NOT EXISTS pf_s_ins BEFORE INSERT ON recurring_series BEGIN SELECT RAISE(ABORT, 'write before preflight: series insert'); END`)
	execTestSQL(t, db, `CREATE TEMP TRIGGER IF NOT EXISTS pf_r_upd BEFORE UPDATE ON recurring_rules BEGIN SELECT RAISE(ABORT, 'write before preflight: rule update'); END`)
	execTestSQL(t, db, `CREATE TEMP TRIGGER IF NOT EXISTS pf_t_upd BEFORE UPDATE ON transactions BEGIN SELECT RAISE(ABORT, 'write before preflight: tx update'); END`)

	rr := pfRequest(t, wsID, userID, seq2ID, db, nil)
	if rr.Code != http.StatusConflict {
		t.Fatalf("status=%d, want 409 (incompatible series_id)", rr.Code)
	}

	execTestSQL(t, db, `DROP TRIGGER IF EXISTS pf_s_ins`)
	execTestSQL(t, db, `DROP TRIGGER IF EXISTS pf_r_upd`)
	execTestSQL(t, db, `DROP TRIGGER IF EXISTS pf_t_upd`)

	var seriesAfter sql.NullString
	db.QueryRow(`SELECT series_id FROM transactions WHERE id='tx-pf-mod-incomp-extra'`).Scan(&seriesAfter)
	if !seriesAfter.Valid || seriesAfter.String != "incompativel" {
		t.Fatalf("incompatible series_id was altered: %v", seriesAfter)
	}

	var seriesCount int
	db.QueryRow(`SELECT COUNT(1) FROM recurring_series WHERE workspace_id=?`, wsID).Scan(&seriesCount)
	if seriesCount != 2 {
		t.Fatalf("recurring_series altered: %d rows (want 2, original + incompatible)", seriesCount)
	}

	var childCount int
	db.QueryRow(`SELECT COUNT(1) FROM recurring_rules WHERE parent_rule_id=?`, ruleID).Scan(&childCount)
	if childCount != 0 {
		t.Fatalf("child rule created: %d", childCount)
	}

	var active int
	db.QueryRow(`SELECT active FROM recurring_rules WHERE id=?`, ruleID).Scan(&active)
	if active != 1 {
		t.Fatalf("rule deactivated: active=%d", active)
	}
}

// ---------- MODERN: RULE FROM OTHER SERIES REJECTED ----------

func TestRecurringFuturePreflight_ModernRejectsRuleFromOtherSeries(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	wsID, userID, ruleID, seriesID := pfSeedCheckingRule(t, db, "mod_os")
	_, seq2ID, _ := pfSeedOccurrences(t, db, wsID, userID, seriesID, ruleID, "mod_os")

	now := time.Now().Unix()
	otherSeriesID := "series-pf-mod_os_other"
	otherRuleID := "rule-pf-mod_os_other"
	execTestSQL(t, db, `INSERT OR IGNORE INTO recurring_series (id, workspace_id, user_id, active, created_at, updated_at) VALUES (?, ?, ?, 1, ?, ?)`, otherSeriesID, wsID, userID, now, now)
	execTestSQL(t, db, `INSERT INTO recurring_rules (id, workspace_id, user_id, account_id, category_id, type, amount, description, start_date, frequency, default_payment_status, active, series_id, effective_start_sequence, total_occurrences, created_at, updated_at) VALUES (?, ?, ?, 'acc-pf-chk', 'cat-pf', 'EXPENSE', 15000, 'mod-os-other', ?, 'MONTHLY', 'PENDING', 1, ?, 1, 3, ?, ?)`, otherRuleID, wsID, userID, testUnixDate("2026-06-05"), otherSeriesID, now, now)
	execTestSQL(t, db, `INSERT INTO transactions (id, workspace_id, user_id, account_id, category_id, type, amount, date, description, status, installment_number, total_installments, recurring_rule_id, recurrence_sequence, series_id, created_at, updated_at) VALUES ('tx-pf-mod-os-extra', ?, ?, 'acc-pf-chk', 'cat-pf', 'EXPENSE', 15000, ?, 'mod-os-extra', 'pending', 1, 1, ?, 4, ?, ?, ?)`, wsID, userID, testUnixDate("2026-09-05"), otherRuleID, seriesID, now, now)

	execTestSQL(t, db, `CREATE TEMP TRIGGER IF NOT EXISTS pf_s_ins BEFORE INSERT ON recurring_series BEGIN SELECT RAISE(ABORT, 'write before preflight: series insert'); END`)
	execTestSQL(t, db, `CREATE TEMP TRIGGER IF NOT EXISTS pf_r_upd BEFORE UPDATE ON recurring_rules BEGIN SELECT RAISE(ABORT, 'write before preflight: rule update'); END`)
	execTestSQL(t, db, `CREATE TEMP TRIGGER IF NOT EXISTS pf_t_upd BEFORE UPDATE ON transactions BEGIN SELECT RAISE(ABORT, 'write before preflight: tx update'); END`)

	rr := pfRequest(t, wsID, userID, seq2ID, db, nil)
	if rr.Code != http.StatusConflict {
		t.Fatalf("status=%d, want 409 (rule from other series)", rr.Code)
	}

	execTestSQL(t, db, `DROP TRIGGER IF EXISTS pf_s_ins`)
	execTestSQL(t, db, `DROP TRIGGER IF EXISTS pf_r_upd`)
	execTestSQL(t, db, `DROP TRIGGER IF EXISTS pf_t_upd`)

	var ruleAfter sql.NullString
	db.QueryRow(`SELECT recurring_rule_id FROM transactions WHERE id='tx-pf-mod-os-extra'`).Scan(&ruleAfter)
	if !ruleAfter.Valid || ruleAfter.String != otherRuleID {
		t.Fatalf("recurring_rule_id altered: %v", ruleAfter)
	}

	var seriesAfter sql.NullString
	db.QueryRow(`SELECT series_id FROM transactions WHERE id='tx-pf-mod-os-extra'`).Scan(&seriesAfter)
	if !seriesAfter.Valid || seriesAfter.String != seriesID {
		t.Fatalf("series_id altered: %v", seriesAfter)
	}

	var childCount int
	db.QueryRow(`SELECT COUNT(1) FROM recurring_rules WHERE parent_rule_id=?`, ruleID).Scan(&childCount)
	if childCount != 0 {
		t.Fatalf("child rule created: %d", childCount)
	}
}

// ---------- MODERN: RULE FROM OTHER WORKSPACE REJECTED ----------

func TestRecurringFuturePreflight_ModernRejectsRuleFromOtherWorkspace(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	wsID, userID, ruleID, seriesID := pfSeedCheckingRule(t, db, "mod_ow")
	_, seq2ID, _ := pfSeedOccurrences(t, db, wsID, userID, seriesID, ruleID, "mod_ow")

	now := time.Now().Unix()
	otherWsID := "ws-pf-mod_ow_other"
	otherUserID := "u-pf-mod_ow_other"
	otherRuleID := "rule-pf-mod_ow_other"
	execTestSQL(t, db, `INSERT OR IGNORE INTO users (id, name, email, password_hash, created_at, updated_at) VALUES (?, ?, ?, 'hash', ?, ?)`, otherUserID, "User ow-other", otherUserID+"@e.com", now, now)
	execTestSQL(t, db, `INSERT OR IGNORE INTO workspaces (id, name, type, description, created_at, updated_at) VALUES (?, ?, 'business', '', ?, ?)`, otherWsID, "WS ow-other", now, now)
	execTestSQL(t, db, `INSERT OR IGNORE INTO workspace_members (workspace_id, user_id, role, joined_at) VALUES (?, ?, 'ADMIN', ?)`, otherWsID, otherUserID, now)
	execTestSQL(t, db, `INSERT OR IGNORE INTO accounts (id, workspace_id, name, type, initial_balance, current_balance, created_at, updated_at) VALUES ('acc-pf-ow-other', ?, 'Checking', 'CHECKING', 0, 0, ?, ?)`, otherWsID, now, now)
	execTestSQL(t, db, `INSERT OR IGNORE INTO categories (id, workspace_id, name, icon, color, type, macro_group, created_at) VALUES ('cat-pf-ow-other', ?, 'ExpCat', 'tag', '#ef4444', 'EXPENSE', '', ?)`, otherWsID, now)
	execTestSQL(t, db, `INSERT OR IGNORE INTO recurring_series (id, workspace_id, user_id, active, created_at, updated_at) VALUES (?, ?, ?, 1, ?, ?)`, seriesID, otherWsID, otherUserID, now, now)
	execTestSQL(t, db, `INSERT INTO recurring_rules (id, workspace_id, user_id, account_id, category_id, type, amount, description, start_date, frequency, default_payment_status, active, series_id, effective_start_sequence, total_occurrences, created_at, updated_at) VALUES (?, ?, ?, 'acc-pf-ow-other', 'cat-pf-ow-other', 'EXPENSE', 15000, 'mod-ow-other', ?, 'MONTHLY', 'PENDING', 1, ?, 1, 3, ?, ?)`, otherRuleID, otherWsID, otherUserID, testUnixDate("2026-06-05"), seriesID, now, now)
	execTestSQL(t, db, `INSERT INTO transactions (id, workspace_id, user_id, account_id, category_id, type, amount, date, description, status, installment_number, total_installments, recurring_rule_id, recurrence_sequence, series_id, created_at, updated_at) VALUES ('tx-pf-mod-ow-extra', ?, ?, 'acc-pf-chk', 'cat-pf', 'EXPENSE', 15000, ?, 'mod-ow-extra', 'pending', 1, 1, ?, 4, ?, ?, ?)`, wsID, userID, testUnixDate("2026-09-05"), otherRuleID, seriesID, now, now)

	execTestSQL(t, db, `CREATE TEMP TRIGGER IF NOT EXISTS pf_s_ins BEFORE INSERT ON recurring_series BEGIN SELECT RAISE(ABORT, 'write before preflight: series insert'); END`)
	execTestSQL(t, db, `CREATE TEMP TRIGGER IF NOT EXISTS pf_r_upd BEFORE UPDATE ON recurring_rules BEGIN SELECT RAISE(ABORT, 'write before preflight: rule update'); END`)
	execTestSQL(t, db, `CREATE TEMP TRIGGER IF NOT EXISTS pf_t_upd BEFORE UPDATE ON transactions BEGIN SELECT RAISE(ABORT, 'write before preflight: tx update'); END`)

	rr := pfRequest(t, wsID, userID, seq2ID, db, nil)
	if rr.Code != http.StatusConflict {
		t.Fatalf("status=%d, want 409 (rule from other workspace)", rr.Code)
	}

	execTestSQL(t, db, `DROP TRIGGER IF EXISTS pf_s_ins`)
	execTestSQL(t, db, `DROP TRIGGER IF EXISTS pf_r_upd`)
	execTestSQL(t, db, `DROP TRIGGER IF EXISTS pf_t_upd`)

	var ruleAfter sql.NullString
	db.QueryRow(`SELECT recurring_rule_id FROM transactions WHERE id='tx-pf-mod-ow-extra'`).Scan(&ruleAfter)
	if !ruleAfter.Valid || ruleAfter.String != otherRuleID {
		t.Fatalf("recurring_rule_id altered: %v", ruleAfter)
	}

	var childCount int
	db.QueryRow(`SELECT COUNT(1) FROM recurring_rules WHERE parent_rule_id=?`, ruleID).Scan(&childCount)
	if childCount != 0 {
		t.Fatalf("child rule created: %d", childCount)
	}
}

// ---------- MODERN: RULE OUTSIDE SOURCE CHAIN REJECTED ----------

func TestRecurringFuturePreflight_ModernRejectsRuleOutsideSourceChain(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	wsID, userID, ruleID, seriesID := pfSeedCheckingRule(t, db, "mod_oc")

	now := time.Now().Unix()
	otherRuleID := "rule-pf-mod_oc_other"
	execTestSQL(t, db, `INSERT INTO recurring_rules (id, workspace_id, user_id, account_id, category_id, type, amount, description, start_date, frequency, default_payment_status, active, series_id, effective_start_sequence, total_occurrences, created_at, updated_at) VALUES (?, ?, ?, 'acc-pf-chk', 'cat-pf', 'EXPENSE', 15000, 'mod-oc-other', ?, 'MONTHLY', 'PENDING', 1, ?, 1, 3, ?, ?)`, otherRuleID, wsID, userID, testUnixDate("2026-06-05"), seriesID, now, now)
	execTestSQL(t, db, `INSERT INTO transactions (id, workspace_id, user_id, account_id, category_id, type, amount, date, description, status, installment_number, total_installments, recurring_rule_id, recurrence_sequence, series_id, created_at, updated_at) VALUES ('tx-pf-mod-oc-extra', ?, ?, 'acc-pf-chk', 'cat-pf', 'EXPENSE', 15000, ?, 'mod-oc-extra', 'pending', 1, 1, ?, 4, ?, ?, ?)`, wsID, userID, testUnixDate("2026-09-05"), otherRuleID, seriesID, now, now)

	_, seq2ID, _ := pfSeedOccurrences(t, db, wsID, userID, seriesID, ruleID, "mod_oc")

	execTestSQL(t, db, `CREATE TEMP TRIGGER IF NOT EXISTS pf_s_ins BEFORE INSERT ON recurring_series BEGIN SELECT RAISE(ABORT, 'write before preflight: series insert'); END`)
	execTestSQL(t, db, `CREATE TEMP TRIGGER IF NOT EXISTS pf_r_upd BEFORE UPDATE ON recurring_rules BEGIN SELECT RAISE(ABORT, 'write before preflight: rule update'); END`)
	execTestSQL(t, db, `CREATE TEMP TRIGGER IF NOT EXISTS pf_t_upd BEFORE UPDATE ON transactions BEGIN SELECT RAISE(ABORT, 'write before preflight: tx update'); END`)

	rr := pfRequest(t, wsID, userID, seq2ID, db, nil)
	if rr.Code != http.StatusConflict {
		t.Fatalf("status=%d, want 409 (rule outside source chain)", rr.Code)
	}

	execTestSQL(t, db, `DROP TRIGGER IF EXISTS pf_s_ins`)
	execTestSQL(t, db, `DROP TRIGGER IF EXISTS pf_r_upd`)
	execTestSQL(t, db, `DROP TRIGGER IF EXISTS pf_t_upd`)

	var ruleAfter sql.NullString
	db.QueryRow(`SELECT recurring_rule_id FROM transactions WHERE id='tx-pf-mod-oc-extra'`).Scan(&ruleAfter)
	if !ruleAfter.Valid || ruleAfter.String != otherRuleID {
		t.Fatalf("recurring_rule_id altered: %v", ruleAfter)
	}

	var childCount int
	db.QueryRow(`SELECT COUNT(1) FROM recurring_rules WHERE parent_rule_id=?`, ruleID).Scan(&childCount)
	if childCount != 0 {
		t.Fatalf("child rule created: %d", childCount)
	}
}

// ---------- MODERN: RULE SEQUENCE OUTSIDE EFFECTIVE RANGE REJECTED ----------

func TestRecurringFuturePreflight_ModernRejectsRuleSequenceOutsideEffectiveRange(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	wsID, userID, ruleID, seriesID := pfSeedCheckingRule(t, db, "mod_oer")

	now := time.Now().Unix()
	ancRuleID := "rule-pf-mod_oer_anc"

	execTestSQL(t, db, `INSERT INTO recurring_rules (id, workspace_id, user_id, account_id, category_id, type, amount, description, start_date, frequency, default_payment_status, active, series_id, effective_start_sequence, effective_end_sequence, total_occurrences, created_at, updated_at) VALUES (?, ?, ?, 'acc-pf-chk', 'cat-pf', 'EXPENSE', 15000, 'mod-oer-anc', ?, 'MONTHLY', 'PENDING', 1, ?, 1, 1, 3, ?, ?)`, ancRuleID, wsID, userID, testUnixDate("2026-04-05"), seriesID, now, now)
	execTestSQL(t, db, `UPDATE recurring_rules SET active = 0 WHERE id = ?`, ancRuleID)
	execTestSQL(t, db, `UPDATE recurring_rules SET parent_rule_id = ? WHERE id = ?`, ancRuleID, ruleID)
	execTestSQL(t, db, `INSERT INTO transactions (id, workspace_id, user_id, account_id, category_id, type, amount, date, description, status, installment_number, total_installments, recurring_rule_id, recurrence_sequence, series_id, created_at, updated_at) VALUES ('tx-pf-mod-oer-extra', ?, ?, 'acc-pf-chk', 'cat-pf', 'EXPENSE', 15000, ?, 'mod-oer-extra', 'pending', 1, 1, ?, 4, ?, ?, ?)`, wsID, userID, testUnixDate("2026-09-05"), ancRuleID, seriesID, now, now)

	_, seq2ID, _ := pfSeedOccurrences(t, db, wsID, userID, seriesID, ruleID, "mod_oer")

	execTestSQL(t, db, `CREATE TEMP TRIGGER IF NOT EXISTS pf_s_ins BEFORE INSERT ON recurring_series BEGIN SELECT RAISE(ABORT, 'write before preflight: series insert'); END`)
	execTestSQL(t, db, `CREATE TEMP TRIGGER IF NOT EXISTS pf_r_upd BEFORE UPDATE ON recurring_rules BEGIN SELECT RAISE(ABORT, 'write before preflight: rule update'); END`)
	execTestSQL(t, db, `CREATE TEMP TRIGGER IF NOT EXISTS pf_t_upd BEFORE UPDATE ON transactions BEGIN SELECT RAISE(ABORT, 'write before preflight: tx update'); END`)

	rr := pfRequest(t, wsID, userID, seq2ID, db, nil)
	if rr.Code != http.StatusConflict {
		t.Fatalf("status=%d, want 409 (sequence outside effective range)", rr.Code)
	}

	execTestSQL(t, db, `DROP TRIGGER IF EXISTS pf_s_ins`)
	execTestSQL(t, db, `DROP TRIGGER IF EXISTS pf_r_upd`)
	execTestSQL(t, db, `DROP TRIGGER IF EXISTS pf_t_upd`)

	var ruleAfter sql.NullString
	db.QueryRow(`SELECT recurring_rule_id FROM transactions WHERE id='tx-pf-mod-oer-extra'`).Scan(&ruleAfter)
	if !ruleAfter.Valid || ruleAfter.String != ancRuleID {
		t.Fatalf("recurring_rule_id altered: %v", ruleAfter)
	}
}

// ---------- MODERN: VALID ANCESTOR RULE WITHIN EFFECTIVE RANGE ----------

func TestRecurringFuturePreflight_ModernAcceptsAncestorRuleWithinEffectiveRange(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	wsID, userID, ruleID, seriesID := pfSeedCheckingRule(t, db, "mod_aer")

	now := time.Now().Unix()
	ancRuleID := "rule-pf-mod_aer_anc"

	execTestSQL(t, db, `INSERT INTO recurring_rules (id, workspace_id, user_id, account_id, category_id, type, amount, description, start_date, frequency, default_payment_status, active, series_id, effective_start_sequence, total_occurrences, created_at, updated_at) VALUES (?, ?, ?, 'acc-pf-chk', 'cat-pf', 'EXPENSE', 15000, 'mod-aer-anc', ?, 'MONTHLY', 'PENDING', 1, ?, 1, 3, ?, ?)`, ancRuleID, wsID, userID, testUnixDate("2026-04-05"), seriesID, now, now)
	execTestSQL(t, db, `UPDATE recurring_rules SET active = 0 WHERE id = ?`, ancRuleID)
	execTestSQL(t, db, `UPDATE recurring_rules SET parent_rule_id = ? WHERE id = ?`, ancRuleID, ruleID)
	execTestSQL(t, db, `INSERT INTO transactions (id, workspace_id, user_id, account_id, category_id, type, amount, date, description, status, installment_number, total_installments, recurring_rule_id, recurrence_sequence, series_id, created_at, updated_at) VALUES ('tx-pf-mod-aer-extra', ?, ?, 'acc-pf-chk', 'cat-pf', 'EXPENSE', 15000, ?, 'mod-aer-extra', 'pending', 1, 1, ?, 4, ?, ?, ?)`, wsID, userID, testUnixDate("2026-09-05"), ancRuleID, seriesID, now, now)

	_, seq2ID, _ := pfSeedOccurrences(t, db, wsID, userID, seriesID, ruleID, "mod_aer")

	rr := pfRequest(t, wsID, userID, seq2ID, db, nil)
	if rr.Code != http.StatusOK {
		t.Fatalf("status=%d body=%q, want 200 (ancestor rule within effective range)", rr.Code, rr.Body.String())
	}
}

// ---------- NON-RECURRING SKIP ----------

func TestRecurringFuturePreflight_NonRecurringSkipsPreflight(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	wsID, userID, _, _ := pfSeedCheckingRule(t, db, "nonrec")
	now := time.Now().Unix()
	nonRecID := "tx-pf-nonrec"
	execTestSQL(t, db, `INSERT INTO transactions (id, workspace_id, user_id, account_id, category_id, type, amount, date, description, status, created_at, updated_at) VALUES (?, ?, ?, 'acc-pf-chk', 'cat-pf', 'EXPENSE', 15000, ?, 'non-recurring', 'pending', ?, ?)`, nonRecID, wsID, userID, testUnixDate("2026-06-05"), now, now)
	rr := pfRequest(t, wsID, userID, nonRecID, db, nil)
	if rr.Code != http.StatusOK {
		t.Fatalf("status=%d body=%q, want 200 (non-recurring, preflight skipped via nil return)", rr.Code, rr.Body.String())
	}
}

// ---------- DUPLICATE SEQUENCE REJECTED (BY DB CONSTRAINT) ----------

// The unique constraint on (workspace_id, recurring_rule_id, recurrence_sequence) already
// prevents duplicate sequences at insert time. No separate preflight test needed;
// the DB schema enforces this invariant before the preflight ever runs.

// ---------- PREFLIGHT SKIP FOR SCOPE != FUTURE ----------

func TestRecurringFuturePreflight_SingleScopeUnaffected(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	wsID, userID, ruleID, seriesID := pfSeedCheckingRule(t, db, "single")
	_, seq2ID, seq3ID := pfSeedOccurrences(t, db, wsID, userID, seriesID, ruleID, "single")
	execTestSQL(t, db, `UPDATE transactions SET status='paid' WHERE id=?`, seq2ID)
	execTestSQL(t, db, `UPDATE transactions SET status='paid' WHERE id=?`, seq3ID)

	formValues := url.Values{
		"valor":            {"150.00"},
		"data":             {"2026-07-05"},
		"tipo":             {"despesa"},
		"origem_conta_id":  {"acc-pf-chk"},
		"categoria_id":     {"cat-pf"},
		"status_pagamento": {"pending"},
		"descricao":        {"single test"},
		"escopo":           {"single"},
	}
	var buf bytes.Buffer
	w := multipart.NewWriter(&buf)
	for k, vals := range formValues {
		for _, v := range vals {
			w.WriteField(k, v)
		}
	}
	w.Close()
	req := httptest.NewRequest(http.MethodPost, "/transacoes/"+seq2ID+"/atualizar", &buf)
	req.Header.Set("Content-Type", w.FormDataContentType())
	h := TransactionHandler{DB: db, WorkspaceID: wsID, UserID: userID, Templates: testOOBTemplatesSeries(t)}
	rr := httptest.NewRecorder()
	h.HandleAtualizarTransacao(rr, req, seq2ID)
	if rr.Code != http.StatusOK {
		t.Fatalf("scope=single status=%d body=%q, want 200 (preflight only for future)", rr.Code, rr.Body.String())
	}
	_ = seq3ID
}

// ---------- PREFLIGHT RETURNS nil FOR NON-RECURRING (SCOPE FUTURE) ----------

func TestRecurringFuturePreflight_FutureScopeNonRecurringSkips(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	wsID, userID, _, _ := pfSeedCheckingRule(t, db, "norec")
	now := time.Now().Unix()
	nrID := "tx-pf-norec-future"
	execTestSQL(t, db, `INSERT INTO transactions (id, workspace_id, user_id, account_id, category_id, type, amount, date, description, status, created_at, updated_at) VALUES (?, ?, ?, 'acc-pf-chk', 'cat-pf', 'EXPENSE', 15000, ?, 'non-rec-future', 'pending', ?, ?)`, nrID, wsID, userID, testUnixDate("2026-06-05"), now, now)

	formValues := url.Values{
		"valor":            {"150.00"},
		"data":             {"2026-07-05"},
		"tipo":             {"despesa"},
		"origem_conta_id":  {"acc-pf-chk"},
		"categoria_id":     {"cat-pf"},
		"status_pagamento": {"pending"},
		"descricao":        {"nr future"},
		"escopo":           {"future"},
	}
	var buf bytes.Buffer
	w := multipart.NewWriter(&buf)
	for k, vals := range formValues {
		for _, v := range vals {
			w.WriteField(k, v)
		}
	}
	w.Close()
	req := httptest.NewRequest(http.MethodPost, "/transacoes/"+nrID+"/atualizar", &buf)
	req.Header.Set("Content-Type", w.FormDataContentType())
	h := TransactionHandler{DB: db, WorkspaceID: wsID, UserID: userID, Templates: testOOBTemplatesSeries(t)}
	rr := httptest.NewRecorder()
	h.HandleAtualizarTransacao(rr, req, nrID)
	if rr.Code != http.StatusOK {
		t.Fatalf("status=%d body=%q, want 200 (non-recurring future skips preflight)", rr.Code, rr.Body.String())
	}
}

// ---------- CONCURRENCY: snapshot vs external write ----------

func TestRecurringFuturePreflight_ConcurrentSnapshotRejected(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "contabase-b1a-concurrency.db")

	dbA, err := database.Open(dbPath)
	if err != nil {
		t.Fatalf("open dbA: %v", err)
	}
	defer dbA.Close()

	dbB, err := sql.Open("sqlite", "file:"+dbPath+"?_pragma=journal_mode(WAL)&_pragma=busy_timeout(100)&_pragma=foreign_keys(1)")
	if err != nil {
		t.Fatalf("open dbB: %v", err)
	}
	defer dbB.Close()
	dbB.SetMaxOpenConns(1)

	wsID := "ws-pf-conc"
	userID := "u-pf-conc"
	now := time.Now().Unix()

	execOn := func(d *sql.DB, q string, args ...interface{}) {
		if _, e := d.Exec(q, args...); e != nil {
			t.Fatalf("exec fail: %v", e)
		}
	}
	execOn(dbB, `INSERT OR IGNORE INTO users (id, name, email, password_hash, created_at, updated_at) VALUES (?, ?, ?, 'hash', ?, ?)`, userID, "User conc", userID+"@e.com", now, now)
	execOn(dbB, `INSERT OR IGNORE INTO workspaces (id, name, type, description, created_at, updated_at) VALUES (?, ?, 'business', '', ?, ?)`, wsID, "WS conc", now, now)
	execOn(dbB, `INSERT OR IGNORE INTO workspace_members (workspace_id, user_id, role, joined_at) VALUES (?, ?, 'ADMIN', ?)`, wsID, userID, now)
	execOn(dbB, `INSERT OR IGNORE INTO accounts (id, workspace_id, name, type, initial_balance, current_balance, created_at, updated_at) VALUES ('acc-pf-chk', ?, 'Checking', 'CHECKING', 0, 0, ?, ?)`, wsID, now, now)
	execOn(dbB, `INSERT OR IGNORE INTO categories (id, workspace_id, name, icon, color, type, macro_group, created_at) VALUES ('cat-pf', ?, 'ExpCat', 'tag', '#ef4444', 'EXPENSE', '', ?)`, wsID, now)

	ruleID := "rule-pf-conc"
	execOn(dbB, `INSERT INTO recurring_rules (id, workspace_id, user_id, account_id, category_id, type, amount, description, start_date, frequency, default_payment_status, active, effective_start_sequence, total_occurrences, created_at, updated_at) VALUES (?, ?, ?, 'acc-pf-chk', 'cat-pf', 'EXPENSE', 15000, 'conc', ?, 'MONTHLY', 'PENDING', 1, 1, 3, ?, ?)`, ruleID, wsID, userID, testUnixDate("2026-06-05"), now, now)

	seq1ID := "tx-pf-conc-1"
	seq2ID := "tx-pf-conc-2"
	seq3ID := "tx-pf-conc-3"
	for i, id := range []string{seq1ID, seq2ID, seq3ID} {
		seq := int64(i + 1)
		dateVal := testUnixDate("2026-06-05")
		if i == 1 {
			dateVal = testUnixDate("2026-07-05")
		}
		if i == 2 {
			dateVal = testUnixDate("2026-08-05")
		}
		execOn(dbB, `INSERT INTO transactions (id, workspace_id, user_id, account_id, category_id, type, amount, date, description, status, installment_number, total_installments, recurring_rule_id, recurrence_sequence, created_at, updated_at) VALUES (?, ?, ?, 'acc-pf-chk', 'cat-pf', 'EXPENSE', 15000, ?, ?, 'pending', 1, 1, ?, ?, ?, ?)`, id, wsID, userID, dateVal, "conc", ruleID, seq, now, now)
	}

	hA := TransactionHandler{DB: dbA, WorkspaceID: wsID, UserID: userID}

	txA, err := dbA.Begin()
	if err != nil {
		t.Fatalf("begin txA: %v", err)
	}
	defer txA.Rollback()

	pf, err := hA.loadRecurringFuturePreflightTx(txA, seq2ID)
	if err != nil {
		t.Fatalf("preflight load: %v", err)
	}
	if pf == nil {
		t.Fatal("preflight is nil for recurring tx")
	}
	if err := validateRecurringFuturePreflight(pf); err != nil {
		t.Fatalf("preflight validation: %v (expected mutable)", err)
	}

	txB, err := dbB.Begin()
	if err != nil {
		t.Fatalf("begin txB: %v", err)
	}
	if _, err := txB.Exec(`UPDATE transactions SET status = 'paid' WHERE id = ?`, seq3ID); err != nil {
		txB.Rollback()
		t.Fatalf("txB update: %v", err)
	}
	if err := txB.Commit(); err != nil {
		t.Fatalf("txB commit: %v", err)
	}

	var postBStatus string
	if err := dbB.QueryRow(`SELECT status FROM transactions WHERE id = ?`, seq3ID).Scan(&postBStatus); err != nil {
		t.Fatalf("post B status: %v", err)
	}
	if postBStatus != "paid" {
		t.Fatalf("B did not persist: status = %q", postBStatus)
	}

	writeErr := execOneTx(txA, `UPDATE transactions SET description = 'txA attempt' WHERE id = ? AND workspace_id = ?`, seq2ID, wsID)

	rollbackErr := txA.Rollback()
	_ = rollbackErr

	if writeErr == nil {
		t.Fatal("A's write succeeded with stale snapshot — TOCTOU window confirmed")
	}
	t.Logf("writeErr = %v (expected: snapshot conflict detected)", writeErr)

	var (
		descAfter    string
		statusAfter  string
		childCount   int
		activeRule   string
		activeActive int
	)
	dbB.QueryRow(`SELECT description FROM transactions WHERE id = ?`, seq2ID).Scan(&descAfter)
	dbB.QueryRow(`SELECT status FROM transactions WHERE id = ?`, seq3ID).Scan(&statusAfter)
	dbB.QueryRow(`SELECT COUNT(1) FROM recurring_rules WHERE parent_rule_id = ?`, ruleID).Scan(&childCount)
	dbB.QueryRow(`SELECT id, active FROM recurring_rules WHERE active = 1 AND workspace_id = ? AND id = ?`, wsID, ruleID).Scan(&activeRule, &activeActive)

	if writeErr == nil {
		if descAfter != "conc" {
			t.Fatalf("A succeeded with stale snapshot: seq2 description=%q, seq3 status=%q, children=%d, active=(%s/%d)", descAfter, statusAfter, childCount, activeRule, activeActive)
		}
	} else {
		if childCount != 0 {
			t.Fatalf("writeErr=%v but child rule created: %d children", writeErr, childCount)
		}
		if descAfter != "conc" {
			t.Fatalf("A modified data despite writeErr=%v: desc=%q", writeErr, descAfter)
		}
	}
	if statusAfter != "paid" {
		t.Fatalf("B's protected write was lost: status=%q", statusAfter)
	}
	if activeRule == "" || activeActive != 1 {
		t.Fatalf("rule broken: activeRule=%q active=%d", activeRule, activeActive)
	}
	_ = descAfter
}
