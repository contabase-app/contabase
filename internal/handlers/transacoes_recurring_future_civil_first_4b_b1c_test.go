package handlers

import (
	"database/sql"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync"
	"testing"

	"github.com/contabase-app/contabase/internal/database"
)

func b1cFutureRequest(t *testing.T, db *sql.DB, txID, description, anchor, status string, dueDate *string) *httptest.ResponseRecorder {
	t.Helper()
	fields := map[string]string{
		"valor":            "200,00",
		"descricao":        description,
		"data":             anchor,
		"tipo":             "despesa",
		"origem_conta_id":  "checking-test",
		"categoria_id":     "cat-expense-rec",
		"status_pagamento": status,
		"escopo":           "future",
	}
	if dueDate != nil {
		fields["due_date"] = *dueDate
	}
	req := newMultipartUpdateRequest(t, "/transacoes/"+txID, fields)
	h := TransactionHandler{DB: db, WorkspaceID: "ws-test", UserID: "user-test", Templates: testOOBTemplatesSeries(t)}
	rr := httptest.NewRecorder()
	h.HandleAtualizarTransacao(rr, req, txID)
	return rr
}

func b1cActiveRule(t *testing.T, db *sql.DB, seriesID string) (string, sql.NullString, sql.NullInt64, int64, string, int64, string) {
	t.Helper()
	var id string
	var policy sql.NullString
	var offset sql.NullInt64
	var startUnix, startsAtUnix int64
	var startCivil, startsAtCivil string
	if err := db.QueryRow(`
		SELECT id, due_date_policy, due_date_offset_days,
		       start_date, start_date_civil, starts_at, starts_at_civil
		FROM recurring_rules
		WHERE workspace_id = 'ws-test' AND series_id = ? AND active = 1
	`, seriesID).Scan(&id, &policy, &offset, &startUnix, &startCivil, &startsAtUnix, &startsAtCivil); err != nil {
		t.Fatalf("load active rule: %v", err)
	}
	return id, policy, offset, startUnix, startCivil, startsAtUnix, startsAtCivil
}

func b1cAssertDuePair(t *testing.T, db *sql.DB, seriesID string, seq int64, wantCivil string) {
	t.Helper()
	var due sql.NullInt64
	var civil sql.NullString
	if err := db.QueryRow(`
		SELECT due_date, due_date_civil
		FROM transactions
		WHERE workspace_id = 'ws-test' AND series_id = ? AND recurrence_sequence = ?
	`, seriesID, seq).Scan(&due, &civil); err != nil {
		t.Fatalf("load due pair seq %d: %v", seq, err)
	}
	if wantCivil == "" {
		if due.Valid || civil.Valid {
			t.Fatalf("seq %d due=%v civil=%v, want NULL/NULL", seq, due, civil)
		}
		return
	}
	if !due.Valid || !civil.Valid || civil.String != wantCivil || LegacyUnixToCivilDateUTC(due.Int64).String() != wantCivil {
		t.Fatalf("seq %d due=%v civil=%v, want %s coherent", seq, due, civil, wantCivil)
	}
}

func TestRecurringFutureCivilIntentValidationAndParsing(t *testing.T) {
	valid := MustCivilWriteDate("2026-07-20")
	tests := []struct {
		name    string
		intent  futureOptionalCivilIntent
		wantErr bool
	}{
		{name: "zero value is preserve", intent: futureOptionalCivilIntent{}},
		{name: "preserve", intent: preserveFutureOptionalCivilIntent()},
		{name: "clear", intent: clearFutureOptionalCivilIntent()},
		{name: "set", intent: setFutureOptionalCivilIntent(valid)},
		{name: "set without value", intent: futureOptionalCivilIntent{state: futureCivilSet}, wantErr: true},
		{name: "clear with value", intent: futureOptionalCivilIntent{state: futureCivilClear, value: valid}, wantErr: true},
		{name: "preserve with value", intent: futureOptionalCivilIntent{state: futureCivilPreserve, value: valid}, wantErr: true},
		{name: "unknown", intent: futureOptionalCivilIntent{state: futureCivilIntentState(99)}, wantErr: true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.intent.validate()
			if (err != nil) != tc.wantErr {
				t.Fatalf("validate error=%v, wantErr=%t", err, tc.wantErr)
			}
		})
	}

	absent, err := parseFutureOptionalCivilIntent(true, false, "")
	if err != nil || absent.state != futureCivilPreserve {
		t.Fatalf("absent=%+v err=%v, want PRESERVE", absent, err)
	}
	empty, err := parseFutureOptionalCivilIntent(true, true, "")
	if err != nil || empty.state != futureCivilClear {
		t.Fatalf("empty=%+v err=%v, want CLEAR", empty, err)
	}
	set, err := parseFutureOptionalCivilIntent(true, true, "2026-07-20")
	if err != nil || set.state != futureCivilSet || set.value.Civil() != "2026-07-20" {
		t.Fatalf("set=%+v err=%v, want SET 2026-07-20", set, err)
	}
	if _, err := parseFutureOptionalCivilIntent(true, true, "2026-02-30"); err == nil {
		t.Fatal("invalid Business due date accepted")
	}
	personal, err := parseFutureOptionalCivilIntent(false, true, "invalid-ignored")
	if err != nil || personal.state != futureCivilPreserve {
		t.Fatalf("Personal intent=%+v err=%v, want normalized PRESERVE", personal, err)
	}
}

func TestRecurringFutureCivilFirstAnchorAndRequiredDate(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	_, series, _ := versionedSeedProducedRule(t, db, "personal", "B1c-Anchor", "2026-06-05", "", "", 4)
	currentID := versionedTxIDBySequence(t, db, series, 2)

	rr := b1cFutureRequest(t, db, currentID, "B1c Anchor B", "2026-07-31", "pending", nil)
	if rr.Code != http.StatusOK {
		t.Fatalf("status=%d body=%q", rr.Code, rr.Body.String())
	}
	_, _, _, startUnix, startCivil, startsAtUnix, startsAtCivil := b1cActiveRule(t, db, series)
	if startCivil != "2026-07-31" || startsAtCivil != startCivil || startUnix != MustCivilWriteDate(startCivil).LegacyUnix() || startsAtUnix != startUnix {
		t.Fatalf("child anchor start=%d/%q starts=%d/%q", startUnix, startCivil, startsAtUnix, startsAtCivil)
	}
	var seq3Date int64
	var seq3Civil string
	if err := db.QueryRow(`SELECT date, date_civil FROM transactions WHERE workspace_id='ws-test' AND series_id=? AND recurrence_sequence=3`, series).Scan(&seq3Date, &seq3Civil); err != nil {
		t.Fatalf("seq3: %v", err)
	}
	if seq3Civil != "2026-08-31" || seq3Date != MustCivilWriteDate(seq3Civil).LegacyUnix() {
		t.Fatalf("seq3=%d/%q, want 2026-08-31 civil-first", seq3Date, seq3Civil)
	}

	for _, tc := range []struct {
		name string
		date *string
	}{
		{name: "absent"},
		{name: "empty", date: stringPtr("")},
		{name: "invalid", date: stringPtr("2026-02-30")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			db2 := openTestDB(t)
			defer db2.Close()
			_, series2, _ := versionedSeedProducedRule(t, db2, "personal", "B1c-Date-"+tc.name, "2026-06-05", "", "", 3)
			id := versionedTxIDBySequence(t, db2, series2, 2)
			fields := map[string]string{
				"valor": "200,00", "descricao": "B1c invalid date", "tipo": "despesa",
				"origem_conta_id": "checking-test", "categoria_id": "cat-expense-rec",
				"status_pagamento": "pending", "escopo": "future",
			}
			if tc.date != nil {
				fields["data"] = *tc.date
			}
			req := newMultipartUpdateRequest(t, "/transacoes/"+id, fields)
			h := TransactionHandler{DB: db2, WorkspaceID: "ws-test", UserID: "user-test", Templates: testOOBTemplatesSeries(t)}
			rec := httptest.NewRecorder()
			h.HandleAtualizarTransacao(rec, req, id)
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("status=%d body=%q, want 400", rec.Code, rec.Body.String())
			}
			if got := activeRulesBySeries(t, db2, series2); got != 1 {
				t.Fatalf("active rules=%d after invalid date", got)
			}
		})
	}
}

func TestRecurringFutureCivilFirstPreserveExactAndRecalculate(t *testing.T) {
	t.Run("unchanged competence preserves exact legacy pair", func(t *testing.T) {
		db := openTestDB(t)
		defer db.Close()
		_, series, _ := versionedSeedProducedRule(t, db, "business", "B1c-Preserve-Exact", "2026-06-05", "2026-06-15", "contact-b1c-exact", 4)
		currentID := versionedTxIDBySequence(t, db, series, 2)
		var originalDue int64
		if err := db.QueryRow(`SELECT due_date FROM transactions WHERE id=?`, currentID).Scan(&originalDue); err != nil {
			t.Fatal(err)
		}
		execTestSQL(t, db, `UPDATE transactions SET due_date_civil=NULL WHERE id=?`, currentID)

		rr := b1cFutureRequest(t, db, currentID, "B1c Preserve Exact B", "2026-07-05", "pending", nil)
		if rr.Code != http.StatusOK {
			t.Fatalf("status=%d body=%q", rr.Code, rr.Body.String())
		}
		var due sql.NullInt64
		var civil sql.NullString
		if err := db.QueryRow(`SELECT due_date, due_date_civil FROM transactions WHERE id=?`, currentID).Scan(&due, &civil); err != nil {
			t.Fatal(err)
		}
		if !due.Valid || due.Int64 != originalDue || civil.Valid {
			t.Fatalf("preserved pair=%v/%v want %d/NULL", due, civil, originalDue)
		}
	})

	for _, tc := range []struct {
		name, sourceDue, anchor string
		wantOffset              int64
		wantSeq2, wantSeq3      string
	}{
		{name: "positive", sourceDue: "2026-06-15", anchor: "2026-07-10", wantOffset: 10, wantSeq2: "2026-07-20", wantSeq3: "2026-08-20"},
		{name: "zero", sourceDue: "2026-06-05", anchor: "2026-07-10", wantOffset: 0, wantSeq2: "2026-07-10", wantSeq3: "2026-08-10"},
		{name: "negative", sourceDue: "2026-05-31", anchor: "2026-07-10", wantOffset: -5, wantSeq2: "2026-07-05", wantSeq3: "2026-08-05"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			db := openTestDB(t)
			defer db.Close()
			_, series, _ := versionedSeedProducedRule(t, db, "business", "B1c-Preserve-"+tc.name, "2026-06-05", tc.sourceDue, "contact-b1c-"+tc.name, 4)
			currentID := versionedTxIDBySequence(t, db, series, 2)
			rr := b1cFutureRequest(t, db, currentID, "B1c Preserve B", tc.anchor, "pending", nil)
			if rr.Code != http.StatusOK {
				t.Fatalf("status=%d body=%q", rr.Code, rr.Body.String())
			}
			_, _, offset, _, _, _, _ := b1cActiveRule(t, db, series)
			if !offset.Valid || offset.Int64 != tc.wantOffset {
				t.Fatalf("offset=%v want %d", offset, tc.wantOffset)
			}
			b1cAssertDuePair(t, db, series, 2, tc.wantSeq2)
			b1cAssertDuePair(t, db, series, 3, tc.wantSeq3)
		})
	}
}

func TestRecurringFutureCivilFirstPreserveMissingOffsetContracts(t *testing.T) {
	t.Run("absence stays absent", func(t *testing.T) {
		db := openTestDB(t)
		defer db.Close()
		_, series, _ := versionedSeedProducedRule(t, db, "business", "B1c-No-Due", "2026-06-05", "", "contact-b1c-none", 4)
		currentID := versionedTxIDBySequence(t, db, series, 2)
		rr := b1cFutureRequest(t, db, currentID, "B1c No Due B", "2026-07-10", "pending", nil)
		if rr.Code != http.StatusOK {
			t.Fatalf("status=%d body=%q", rr.Code, rr.Body.String())
		}
		_, _, offset, _, _, _, _ := b1cActiveRule(t, db, series)
		if offset.Valid {
			t.Fatalf("offset=%v want NULL", offset)
		}
		b1cAssertDuePair(t, db, series, 2, "")
	})

	t.Run("due without offset conflicts before write", func(t *testing.T) {
		db := openTestDB(t)
		defer db.Close()
		ruleID, series, _ := versionedSeedProducedRule(t, db, "business", "B1c-Conflict", "2026-06-05", "2026-06-15", "contact-b1c-conflict", 4)
		currentID := versionedTxIDBySequence(t, db, series, 2)
		execTestSQL(t, db, `UPDATE recurring_rules SET due_date_offset_days=NULL WHERE id=?`, ruleID)
		var rulesBefore, txBefore int
		db.QueryRow(`SELECT COUNT(*) FROM recurring_rules WHERE workspace_id='ws-test' AND series_id=?`, series).Scan(&rulesBefore)
		db.QueryRow(`SELECT COUNT(*) FROM transactions WHERE workspace_id='ws-test' AND series_id=?`, series).Scan(&txBefore)

		rr := b1cFutureRequest(t, db, currentID, "B1c Conflict B", "2026-07-10", "pending", nil)
		if rr.Code != http.StatusConflict {
			t.Fatalf("status=%d body=%q want 409", rr.Code, rr.Body.String())
		}
		var active int
		db.QueryRow(`SELECT active FROM recurring_rules WHERE id=?`, ruleID).Scan(&active)
		var rulesAfter, txAfter int
		db.QueryRow(`SELECT COUNT(*) FROM recurring_rules WHERE workspace_id='ws-test' AND series_id=?`, series).Scan(&rulesAfter)
		db.QueryRow(`SELECT COUNT(*) FROM transactions WHERE workspace_id='ws-test' AND series_id=?`, series).Scan(&txAfter)
		if active != 1 || rulesAfter != rulesBefore || txAfter != txBefore {
			t.Fatalf("partial write active=%d rules=%d/%d tx=%d/%d", active, rulesAfter, rulesBefore, txAfter, txBefore)
		}
	})
}

func TestRecurringFutureCivilFirstClearAndRematerialize(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	ruleID, series, _ := versionedSeedProducedRule(t, db, "business", "B1c-Clear", "2026-06-05", "2026-06-15", "contact-b1c-clear", 5)
	currentID := versionedTxIDBySequence(t, db, series, 2)
	execTestSQL(t, db, `UPDATE recurring_rules SET due_date_policy='SAME_DAY' WHERE id=?`, ruleID)
	execTestSQL(t, db, `DELETE FROM transactions WHERE workspace_id='ws-test' AND series_id=? AND recurrence_sequence > 3`, series)
	empty := ""
	rr := b1cFutureRequest(t, db, currentID, "B1c Clear B", "2026-07-05", "pending", &empty)
	if rr.Code != http.StatusOK {
		t.Fatalf("status=%d body=%q", rr.Code, rr.Body.String())
	}
	_, policy, offset, _, _, _, _ := b1cActiveRule(t, db, series)
	if policy.Valid || offset.Valid {
		t.Fatalf("child policy=%v offset=%v want NULL/NULL", policy, offset)
	}
	for seq := int64(2); seq <= 4; seq++ {
		b1cAssertDuePair(t, db, series, seq, "")
	}
	execTestSQL(t, db, `DELETE FROM transactions WHERE workspace_id='ws-test' AND series_id=? AND recurrence_sequence=4`, series)
	if err := EnsureRecurringMaterialized(db, "ws-test", MustCivilWriteDate("2027-12-31")); err != nil {
		t.Fatalf("rematerialize: %v", err)
	}
	b1cAssertDuePair(t, db, series, 4, "")
	if err := EnsureRecurringMaterialized(db, "ws-test", MustCivilWriteDate("2027-12-31")); err != nil {
		t.Fatalf("second materialize: %v", err)
	}
	var count int
	db.QueryRow(`SELECT COUNT(*) FROM transactions WHERE workspace_id='ws-test' AND series_id=? AND recurrence_sequence=4`, series).Scan(&count)
	if count != 1 {
		t.Fatalf("seq4 count=%d want 1", count)
	}
}

func TestRecurringFutureCivilFirstSetUsesSignedOffset(t *testing.T) {
	for _, tc := range []struct {
		name, anchor, due string
		wantOffset        int64
		wantSeq2          string
		wantSeq3          string
	}{
		{name: "positive", anchor: "2026-07-31", due: "2026-08-05", wantOffset: 5, wantSeq2: "2026-08-05", wantSeq3: "2026-09-05"},
		{name: "zero", anchor: "2026-07-31", due: "2026-07-31", wantOffset: 0, wantSeq2: "2026-07-31", wantSeq3: "2026-08-31"},
		{name: "negative", anchor: "2026-07-31", due: "2026-07-26", wantOffset: -5, wantSeq2: "2026-07-26", wantSeq3: "2026-08-26"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			db := openTestDB(t)
			defer db.Close()
			ruleID, series, _ := versionedSeedProducedRule(t, db, "business", "B1c-Set-"+tc.name, "2026-06-30", "2026-07-10", "contact-b1c-set-"+tc.name, 5)
			currentID := versionedTxIDBySequence(t, db, series, 2)
			execTestSQL(t, db, `UPDATE recurring_rules SET due_date_policy='LEGACY' WHERE id=?`, ruleID)
			rr := b1cFutureRequest(t, db, currentID, "B1c Set B", tc.anchor, "pending", &tc.due)
			if rr.Code != http.StatusOK {
				t.Fatalf("status=%d body=%q", rr.Code, rr.Body.String())
			}
			_, policy, offset, _, _, _, _ := b1cActiveRule(t, db, series)
			if policy.Valid || !offset.Valid || offset.Int64 != tc.wantOffset {
				t.Fatalf("policy=%v offset=%v want NULL/%d", policy, offset, tc.wantOffset)
			}
			b1cAssertDuePair(t, db, series, 2, tc.wantSeq2)
			b1cAssertDuePair(t, db, series, 3, tc.wantSeq3)
		})
	}
}

func TestRecurringFutureCivilFirstPaidClearsDueContract(t *testing.T) {
	t.Run("Business valid due is validated but cleared", func(t *testing.T) {
		db := openTestDB(t)
		defer db.Close()
		ruleID, series, _ := versionedSeedProducedRule(t, db, "business", "B1c-Paid-Business", "2026-06-05", "2026-06-15", "contact-b1c-paid", 5)
		currentID := versionedTxIDBySequence(t, db, series, 2)
		execTestSQL(t, db, `UPDATE recurring_rules SET due_date_policy='LEGACY' WHERE id=?`, ruleID)
		execTestSQL(t, db, `DELETE FROM transactions WHERE workspace_id='ws-test' AND series_id=? AND recurrence_sequence > 3`, series)
		due := "2026-07-20"

		rr := b1cFutureRequest(t, db, currentID, "B1c Paid Business B", "2026-07-10", "paid", &due)
		if rr.Code != http.StatusOK {
			t.Fatalf("status=%d body=%q", rr.Code, rr.Body.String())
		}
		childID, policy, offset, _, _, _, _ := b1cActiveRule(t, db, series)
		if childID == ruleID || policy.Valid || offset.Valid {
			t.Fatalf("child=%q source=%q policy=%v offset=%v", childID, ruleID, policy, offset)
		}
		var sourcePolicy, sourceContact sql.NullString
		var sourceOffset sql.NullInt64
		if err := db.QueryRow(`SELECT due_date_policy, due_date_offset_days, contact_id FROM recurring_rules WHERE id=? AND workspace_id='ws-test'`, ruleID).Scan(&sourcePolicy, &sourceOffset, &sourceContact); err != nil {
			t.Fatalf("source due metadata: %v", err)
		}
		if !sourcePolicy.Valid || sourcePolicy.String != "LEGACY" || !sourceOffset.Valid || sourceOffset.Int64 != 10 || !sourceContact.Valid || sourceContact.String != "contact-b1c-paid" {
			t.Fatalf("source metadata policy=%v offset=%v contact=%v, want preserved", sourcePolicy, sourceOffset, sourceContact)
		}
		for seq := int64(2); seq <= 4; seq++ {
			var status string
			var paidAt sql.NullInt64
			var paidCivil sql.NullString
			if err := db.QueryRow(`SELECT status, paid_at, paid_date_civil FROM transactions WHERE workspace_id='ws-test' AND series_id=? AND recurrence_sequence=?`, series, seq).Scan(&status, &paidAt, &paidCivil); err != nil {
				t.Fatalf("status seq%d: %v", seq, err)
			}
			if status != "paid" || paidAt.Valid || paidCivil.Valid {
				t.Fatalf("seq%d status=%q paid_at=%v paid_civil=%v, want paid without payment timestamp", seq, status, paidAt, paidCivil)
			}
			b1cAssertDuePair(t, db, series, seq, "")
		}
		execTestSQL(t, db, `DELETE FROM transactions WHERE workspace_id='ws-test' AND series_id=? AND recurrence_sequence=4`, series)
		if err := EnsureRecurringMaterialized(db, "ws-test", MustCivilWriteDate("2027-12-31")); err != nil {
			t.Fatalf("rematerialize paid gap: %v", err)
		}
		b1cAssertDuePair(t, db, series, 4, "")
		var rebuiltStatus string
		db.QueryRow(`SELECT status FROM transactions WHERE workspace_id='ws-test' AND series_id=? AND recurrence_sequence=4`, series).Scan(&rebuiltStatus)
		if rebuiltStatus != "paid" {
			t.Fatalf("rematerialized status=%q want paid", rebuiltStatus)
		}
		if err := EnsureRecurringMaterialized(db, "ws-test", MustCivilWriteDate("2027-12-31")); err != nil {
			t.Fatalf("second paid materialization: %v", err)
		}
		var count int
		db.QueryRow(`SELECT COUNT(*) FROM transactions WHERE workspace_id='ws-test' AND series_id=? AND recurrence_sequence=4`, series).Scan(&count)
		if count != 1 {
			t.Fatalf("seq4 count=%d want 1", count)
		}
	})

	t.Run("Personal remains without due or contact", func(t *testing.T) {
		db := openTestDB(t)
		defer db.Close()
		ruleID, series, _ := versionedSeedProducedRule(t, db, "personal", "B1c-Paid-Personal", "2026-06-05", "", "", 4)
		currentID := versionedTxIDBySequence(t, db, series, 2)
		rr := b1cFutureRequest(t, db, currentID, "B1c Paid Personal B", "2026-07-05", "paid", nil)
		if rr.Code != http.StatusOK {
			t.Fatalf("status=%d body=%q", rr.Code, rr.Body.String())
		}
		childID, _, _, _, _, _, _ := b1cActiveRule(t, db, series)
		if childID == ruleID {
			t.Fatal("future paid reused source rule")
		}
		rows, err := db.Query(`SELECT status, due_date, due_date_civil, contact_id, paid_at, paid_date_civil FROM transactions WHERE workspace_id='ws-test' AND series_id=? AND recurrence_sequence>=2`, series)
		if err != nil {
			t.Fatal(err)
		}
		defer rows.Close()
		for rows.Next() {
			var status string
			var due, paidAt sql.NullInt64
			var dueCivil, contact, paidCivil sql.NullString
			if err := rows.Scan(&status, &due, &dueCivil, &contact, &paidAt, &paidCivil); err != nil {
				t.Fatal(err)
			}
			if status != "paid" || due.Valid || dueCivil.Valid || contact.Valid || paidAt.Valid || paidCivil.Valid {
				t.Fatalf("Personal paid row status=%q due=%v civil=%v contact=%v paid_at=%v paid_civil=%v", status, due, dueCivil, contact, paidAt, paidCivil)
			}
		}
	})
}

func TestRecurringFutureCivilFirstScopeIsolation(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	ruleID, series, _ := versionedSeedProducedRule(t, db, "personal", "B1c-Single", "2026-06-05", "", "", 3)
	currentID := versionedTxIDBySequence(t, db, series, 2)
	req := newMultipartUpdateRequest(t, "/transacoes/"+currentID, map[string]string{
		"valor": "200,00", "descricao": "B1c Single Paid", "data": "2026-07-05",
		"tipo": "despesa", "origem_conta_id": "checking-test", "categoria_id": "cat-expense-rec",
		"status_pagamento": "paid", "escopo": "single",
	})
	h := TransactionHandler{DB: db, WorkspaceID: "ws-test", UserID: "user-test", Templates: testOOBTemplatesSeries(t)}
	rr := httptest.NewRecorder()
	h.HandleAtualizarTransacao(rr, req, currentID)
	if rr.Code != http.StatusOK {
		t.Fatalf("single paid status=%d body=%q", rr.Code, rr.Body.String())
	}
	var ruleCount, active int
	db.QueryRow(`SELECT COUNT(*) FROM recurring_rules WHERE workspace_id='ws-test' AND series_id=?`, series).Scan(&ruleCount)
	db.QueryRow(`SELECT active FROM recurring_rules WHERE id=?`, ruleID).Scan(&active)
	if ruleCount != 1 || active != 1 {
		t.Fatalf("single leaked future versioning rules=%d active=%d", ruleCount, active)
	}
}

func TestRecurringFutureCivilFirstConcurrent(t *testing.T) {
	db, err := database.Open(filepath.Join(t.TempDir(), "b1c-concurrent.db"))
	if err != nil {
		t.Fatalf("open concurrent db: %v", err)
	}
	defer db.Close()
	db.SetMaxOpenConns(2)
	_, series, _ := versionedSeedProducedRule(t, db, "personal", "B1c-Concurrent", "2026-06-05", "", "", 4)
	currentID := versionedTxIDBySequence(t, db, series, 2)

	start := make(chan struct{})
	statuses := make(chan int, 2)
	var wg sync.WaitGroup
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			rr := b1cFutureRequest(t, db, currentID, "B1c Concurrent B", "2026-07-10", "pending", nil)
			statuses <- rr.Code
		}()
	}
	close(start)
	wg.Wait()
	close(statuses)

	successes := 0
	for status := range statuses {
		if status == http.StatusOK {
			successes++
		}
	}
	if successes < 1 || successes > 2 {
		t.Fatalf("successes=%d want one or two serialized successes", successes)
	}
	if got := activeRulesBySeries(t, db, series); got != 1 {
		t.Fatalf("active rules=%d want 1", got)
	}
	var duplicates int
	if err := db.QueryRow(`
		SELECT COUNT(*) FROM (
			SELECT recurrence_sequence
			FROM transactions
			WHERE workspace_id='ws-test' AND series_id=?
			GROUP BY recurrence_sequence
			HAVING COUNT(*) > 1
		)
	`, series).Scan(&duplicates); err != nil {
		t.Fatalf("count duplicate sequences: %v", err)
	}
	if duplicates != 0 {
		t.Fatalf("duplicate series sequences=%d", duplicates)
	}
}
