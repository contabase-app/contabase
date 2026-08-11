package handlers

import (
	"database/sql"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/contabase-app/contabase/internal/database"
)

func versionedSeedRule(t *testing.T, db *sql.DB, desc, date string, seqs int64) (ruleID, seriesID, firstID string) {
	t.Helper()
	ruleID, firstID = seedRecurringSeriesOnChecking(t, db, desc, date, seqs)
	seriesID = "series-" + desc
	attachRecurringSeriesIdentity(t, db, ruleID, seriesID)
	return
}

func versionedRequest(t *testing.T, txID, wsID, userID, value, desc, date, scope string, db *sql.DB) *httptest.ResponseRecorder {
	t.Helper()
	req := newMultipartUpdateRequest(t, "/transacoes/"+txID, map[string]string{
		"valor":            value,
		"descricao":        desc,
		"data":             date,
		"tipo":             "despesa",
		"origem_conta_id":  "checking-test",
		"categoria_id":     "cat-expense-rec",
		"status_pagamento": "pending",
		"escopo":           scope,
	})
	h := TransactionHandler{DB: db, WorkspaceID: wsID, UserID: userID, Templates: testOOBTemplatesSeries(t)}
	rr := httptest.NewRecorder()
	h.HandleAtualizarTransacao(rr, req, txID)
	return rr
}

func versionedSeedProducedRule(t *testing.T, db *sql.DB, wsType, desc, startDate, dueDate, contactID string, total int64) (ruleID, seriesID, firstID string) {
	t.Helper()
	seedInvoicePaymentScenario(t, db)
	seedRecurringExpenseCategory(t, db)
	execTestSQL(t, db, `UPDATE workspaces SET type = ? WHERE id = 'ws-test'`, wsType)
	if contactID != "" {
		execTestSQL(t, db, `INSERT INTO contacts (id, workspace_id, name, type, created_at) VALUES (?, 'ws-test', ?, 'vendor', ?)`, contactID, contactID, time.Now().Unix())
	}
	var due OptionalCivilWriteDate
	if dueDate != "" {
		due = MustOptionalCivilWriteDate(dueDate)
	}
	h := TransactionHandler{DB: db, WorkspaceID: "ws-test", UserID: "user-test", Templates: testOOBTemplatesSeries(t)}
	var err error
	firstID, err = h.insertTransaction(
		"EXPENSE", 10000, desc, "", "", MustCivilWriteDate(startDate),
		"checking-test", "", "cat-expense-rec", 1, "pending", true, "MONTHLY",
		contactID, due, &total, "", false,
	)
	if err != nil {
		t.Fatalf("insert produced recurrence: %v", err)
	}
	if err := db.QueryRow(`SELECT id, series_id FROM recurring_rules WHERE workspace_id='ws-test' AND description=?`, desc).Scan(&ruleID, &seriesID); err != nil {
		t.Fatalf("load produced rule: %v", err)
	}
	return ruleID, seriesID, firstID
}

func versionedTxIDBySequence(t *testing.T, db *sql.DB, seriesID string, seq int64) string {
	t.Helper()
	var id string
	if err := db.QueryRow(`SELECT id FROM transactions WHERE workspace_id='ws-test' AND series_id=? AND recurrence_sequence=?`, seriesID, seq).Scan(&id); err != nil {
		t.Fatalf("load seq %d: %v", seq, err)
	}
	return id
}

func versionedCreateRuleWithRealProducer(t *testing.T, db *sql.DB, producer, wsType, desc, contactID string) (ruleID, seriesID string) {
	t.Helper()
	seedInvoicePaymentScenario(t, db)
	seedRecurringExpenseCategory(t, db)
	execTestSQL(t, db, `UPDATE workspaces SET type = ? WHERE id = 'ws-test'`, wsType)
	if contactID != "" {
		execTestSQL(t, db, `INSERT INTO contacts (id, workspace_id, name, type, created_at) VALUES (?, 'ws-test', ?, 'vendor', ?)`, contactID, contactID, time.Now().Unix())
	}

	h := TransactionHandler{DB: db, WorkspaceID: "ws-test", UserID: "user-test", Templates: testOOBTemplatesSeries(t)}
	switch producer {
	case "insertTransaction":
		total := int64(3)
		normalizedContactID := contactID
		if wsType != "business" {
			normalizedContactID = ""
		}
		if _, err := h.insertTransaction(
			"EXPENSE", 10000, desc, "", "", MustCivilWriteDate("2026-06-05"),
			"checking-test", "", "cat-expense-rec", 1, "pending", true, "MONTHLY",
			normalizedContactID, MustOptionalCivilWriteDate("2026-06-15"), &total, "", false,
		); err != nil {
			t.Fatalf("insertTransaction producer: %v", err)
		}
	case "insertRecurringRuleTx":
		if _, err := h.insertTransaction(
			"EXPENSE", 10000, desc, "", "", MustCivilWriteDate("2026-06-05"),
			"checking-test", "", "cat-expense-rec", 1, "pending", false, "",
			"", OptionalCivilWriteDate{}, nil, "", false,
		); err != nil {
			t.Fatalf("seed transaction for conversion: %v", err)
		}
		var txID string
		if err := db.QueryRow(`SELECT id FROM transactions WHERE workspace_id='ws-test' AND description=?`, desc).Scan(&txID); err != nil {
			t.Fatalf("load transaction for conversion: %v", err)
		}
		req := newMultipartUpdateRequest(t, "/transacoes/"+txID, map[string]string{
			"valor":            "100,00",
			"descricao":        desc,
			"data":             "2026-06-05",
			"tipo":             "despesa",
			"origem_conta_id":  "checking-test",
			"categoria_id":     "cat-expense-rec",
			"status_pagamento": "pending",
			"lancamento_fixo":  "on",
			"recorrencia":      "MONTHLY",
			"contact_id":       contactID,
			"due_date":         "2026-06-15",
			"escopo":           "single",
		})
		rr := httptest.NewRecorder()
		h.HandleAtualizarTransacao(rr, req, txID)
		if rr.Code != http.StatusOK {
			t.Fatalf("conversion producer status=%d body=%q", rr.Code, rr.Body.String())
		}
	default:
		t.Fatalf("unknown producer %q", producer)
	}

	if err := db.QueryRow(`SELECT id, series_id FROM recurring_rules WHERE workspace_id='ws-test' AND description=?`, desc).Scan(&ruleID, &seriesID); err != nil {
		t.Fatalf("load rule from %s: %v", producer, err)
	}
	return ruleID, seriesID
}

// ---------- TESTES DE VERSIONAMENTO ----------

func TestRecurringFutureVersioning_FirstSplitAB_Seq2(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	const desc = "P51-AB"
	ruleA, series, firstID := versionedSeedRule(t, db, desc, "2026-06-05", 3)
	middleID, _ := seedAdditionalCheckingOccurrences(t, db, ruleA, series, desc)

	rr := versionedRequest(t, middleID, "ws-test", "user-test", "200,00", desc+" B", "2026-07-05", "future", db)
	if rr.Code != http.StatusOK {
		t.Fatalf("status=%d body=%q", rr.Code, rr.Body.String())
	}

	aSeries, aParent, aStart, aEnd, aActive := recurringRuleVersionFields(t, db, ruleA)
	if aSeries != series || aParent.Valid || aStart != 1 || !aEnd.Valid || aEnd.Int64 != 1 || aActive != 0 {
		t.Fatalf("A: series=%q parent=%v start=%d end=%v active=%d; want (%s,nil,1,1,0)", aSeries, aParent, aStart, aEnd, aActive, series)
	}

	seq2Rule, seq3Rule, seq2ID, seq3ID := ruleAndTxBySequence(t, db, series, 2, 3)
	if seq2Rule == ruleA || seq2Rule == "" {
		t.Fatalf("B=%q want new", seq2Rule)
	}
	if seq3Rule != seq2Rule {
		t.Fatalf("C=%q want same as B=%q", seq3Rule, seq2Rule)
	}

	bSeries, bParent, bStart, bEnd, bActive := recurringRuleVersionFields(t, db, seq2Rule)
	if bSeries != series || !bParent.Valid || bParent.String != ruleA || bStart != 2 || bEnd.Valid || bActive != 1 {
		t.Fatalf("B: series=%q parent=%v start=%d end=%v active=%d; want (%s,A,2,nil,1)", bSeries, bParent, bStart, bEnd, bActive, series)
	}

	_ = firstID
	_ = seq2ID
	_ = seq3ID
}

func TestRecurringFutureVersioning_SecondSplitBC_SameSeq2(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	const desc = "P51-BC"
	ruleA, series, firstID := versionedSeedRule(t, db, desc, "2026-06-05", 3)
	middleID, _ := seedAdditionalCheckingOccurrences(t, db, ruleA, series, desc)

	rr1 := versionedRequest(t, middleID, "ws-test", "user-test", "200,00", desc+" B", "2026-07-05", "future", db)
	if rr1.Code != http.StatusOK {
		t.Fatalf("first split: status=%d", rr1.Code)
	}

	_, ruleB, seq2ID, _ := ruleAndTxBySequence(t, db, series, 2, 3)

	rr2 := versionedRequest(t, seq2ID, "ws-test", "user-test", "300,00", desc+" C", "2026-07-05", "future", db)
	if rr2.Code != http.StatusOK {
		t.Fatalf("second split same seq: status=%d body=%q", rr2.Code, rr2.Body.String())
	}

	seq2Rule, seq3Rule, _, _ := ruleAndTxBySequence(t, db, series, 2, 3)
	if seq2Rule == ruleA || seq2Rule == ruleB || seq2Rule == "" {
		t.Fatalf("seq2 rule = %q, want new C distinct from A=%q/B=%q", seq2Rule, ruleA, ruleB)
	}
	if seq2Rule != seq3Rule {
		t.Fatalf("seq3 rule != seq2 rule")
	}

	cSeries, cParent, cStart, cEnd, cActive := recurringRuleVersionFields(t, db, seq2Rule)
	if cSeries != series || !cParent.Valid || cParent.String != ruleB || cStart != 2 || cEnd.Valid || cActive != 1 {
		t.Fatalf("C: series=%q parent=%v start=%d end=%v active=%d", cSeries, cParent, cStart, cEnd, cActive)
	}
	_, bParent, bStart, bEnd, bActive := recurringRuleVersionFields(t, db, ruleB)
	if !bParent.Valid || bParent.String != ruleA || bStart != 2 || !bEnd.Valid || bEnd.Int64 != 0 || bActive != 0 {
		t.Fatalf("B: parent=%v start=%d end=%v active=%d; want A/2/0/0", bParent, bStart, bEnd, bActive)
	}

	_ = firstID
}

func TestRecurringFutureVersioning_ThirdSplitCD_Seq3(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	const desc = "P51-CD"
	ruleA, series, firstID := versionedSeedRule(t, db, desc, "2026-06-05", 3)
	middleID, _ := seedAdditionalCheckingOccurrences(t, db, ruleA, series, desc)

	rr1 := versionedRequest(t, middleID, "ws-test", "user-test", "200,00", desc+" B", "2026-07-05", "future", db)
	if rr1.Code != http.StatusOK {
		t.Fatalf("first split: status=%d body=%q", rr1.Code, rr1.Body.String())
	}
	_, ruleB, _, seq3ID := ruleAndTxBySequence(t, db, series, 2, 3)

	rr := versionedRequest(t, seq3ID, "ws-test", "user-test", "300,00", desc+" C", "2026-08-05", "future", db)
	if rr.Code != http.StatusOK {
		t.Fatalf("third split: status=%d body=%q", rr.Code, rr.Body.String())
	}

	_, seq3AfterRule, _, _ := ruleAndTxBySequence(t, db, series, 2, 3)
	if seq3AfterRule == ruleA || seq3AfterRule == ruleB || seq3AfterRule == "" {
		t.Fatalf("seq3 rule=%q want new C distinct from A=%q/B=%q", seq3AfterRule, ruleA, ruleB)
	}

	dSeries, dParent, dStart, dEnd, dActive := recurringRuleVersionFields(t, db, seq3AfterRule)
	if dSeries != series || !dParent.Valid || dParent.String != ruleB || dStart != 3 || dEnd.Valid || dActive != 1 {
		t.Fatalf("D: series=%q start=%d end=%v active=%d", dSeries, dStart, dEnd, dActive)
	}

	_ = firstID
}

func TestRecurringFutureVersioning_ChainABCD(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	const desc = "P51-ABCD"
	ruleA, series, firstID := versionedSeedRule(t, db, desc, "2026-06-05", 3)
	middleID, _ := seedAdditionalCheckingOccurrences(t, db, ruleA, series, desc)

	versionedRequest(t, middleID, "ws-test", "user-test", "200,00", desc+" B", "2026-07-05", "future", db)
	_, _, seq2ID, _ := ruleAndTxBySequence(t, db, series, 2, 3)
	versionedRequest(t, seq2ID, "ws-test", "user-test", "300,00", desc+" C", "2026-08-05", "future", db)
	_, _, _, newSeq3ID := ruleAndTxBySequence(t, db, series, 2, 3)
	versionedRequest(t, newSeq3ID, "ws-test", "user-test", "400,00", desc+" D", "2026-09-05", "future", db)

	rules := allRulesBySeries(t, db, series)
	if len(rules) != 4 {
		t.Fatalf("rules in series = %d, want 4 (A,B,C,D)", len(rules))
	}

	activeCount := activeRulesBySeries(t, db, series)
	if activeCount != 1 {
		t.Fatalf("active rules = %d, want 1", activeCount)
	}

	_, _, _, _, aActive := recurringRuleVersionFields(t, db, ruleA)
	if aActive != 0 {
		t.Fatalf("A active=%d want 0", aActive)
	}
	var ruleD string
	if err := db.QueryRow(`SELECT id FROM recurring_rules WHERE workspace_id='ws-test' AND series_id=? AND active=1`, series).Scan(&ruleD); err != nil {
		t.Fatalf("active D: %v", err)
	}
	_, dParent, dStart, dEnd, dActive := recurringRuleVersionFields(t, db, ruleD)
	if !dParent.Valid || dStart != 3 || dEnd.Valid || dActive != 1 {
		t.Fatalf("D parent=%v start=%d end=%v active=%d", dParent, dStart, dEnd, dActive)
	}
	ruleC := dParent.String
	_, cParent, cStart, cEnd, cActive := recurringRuleVersionFields(t, db, ruleC)
	if !cParent.Valid || cStart != 2 || !cEnd.Valid || cEnd.Int64 != 2 || cActive != 0 {
		t.Fatalf("C parent=%v start=%d end=%v active=%d", cParent, cStart, cEnd, cActive)
	}
	ruleB := cParent.String
	_, bParent, bStart, bEnd, bActive := recurringRuleVersionFields(t, db, ruleB)
	if !bParent.Valid || bParent.String != ruleA || bStart != 2 || !bEnd.Valid || bEnd.Int64 != 0 || bActive != 0 {
		t.Fatalf("B parent=%v start=%d end=%v active=%d", bParent, bStart, bEnd, bActive)
	}
	_, aParent, aStart, aEnd, _ := recurringRuleVersionFields(t, db, ruleA)
	if aParent.Valid || aStart != 1 || !aEnd.Valid || aEnd.Int64 != 1 {
		t.Fatalf("A parent=%v start=%d end=%v", aParent, aStart, aEnd)
	}
	if ruleA == ruleB || ruleB == ruleC || ruleC == ruleD || ruleA == ruleC || ruleA == ruleD || ruleB == ruleD {
		t.Fatalf("rule UUIDs are not unique: A=%s B=%s C=%s D=%s", ruleA, ruleB, ruleC, ruleD)
	}

	_ = firstID
	_ = newSeq3ID
}

func TestRecurringFutureVersioning_CurrentEqualsStartEmptiesSourceAndPreservesChildEnd(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	const desc = "P51-CurEqSt"
	ruleA, series, firstID := versionedSeedRule(t, db, desc, "2026-06-05", 3)
	middleID, _ := seedAdditionalCheckingOccurrences(t, db, ruleA, series, desc)

	execTestSQL(t, db, `UPDATE recurring_rules SET effective_end_sequence = 10 WHERE id = ?`, ruleA)

	rr := versionedRequest(t, firstID, "ws-test", "user-test", "200,00", desc+" B", "2026-06-05", "future", db)
	if rr.Code != http.StatusOK {
		t.Fatalf("status=%d", rr.Code)
	}

	_, childRule, _, _ := ruleAndTxBySequence(t, db, series, 1, 2)
	_, _, _, aEnd, aActive := recurringRuleVersionFields(t, db, ruleA)
	if aActive != 0 {
		t.Fatalf("A active=%d want 0", aActive)
	}
	if !aEnd.Valid || aEnd.Int64 != 0 {
		t.Fatalf("A end=%v want 0 (empty sentinel)", aEnd)
	}
	_, parent, start, childEnd, childActive := recurringRuleVersionFields(t, db, childRule)
	if !parent.Valid || parent.String != ruleA || start != 1 || !childEnd.Valid || childEnd.Int64 != 10 || childActive != 1 {
		t.Fatalf("child parent=%v start=%d end=%v active=%d; want A/1/10/1", parent, start, childEnd, childActive)
	}

	_ = middleID
}

func TestRecurringFutureVersioning_CurrentAfterLimitRejected(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	const desc = "P51-AfterLimit"
	ruleA, series, firstID := versionedSeedRule(t, db, desc, "2026-06-05", 3)
	middleID, _ := seedAdditionalCheckingOccurrences(t, db, ruleA, series, desc)

	execTestSQL(t, db, `UPDATE recurring_rules SET effective_end_sequence = 1 WHERE id = ?`, ruleA)

	rr := versionedRequest(t, middleID, "ws-test", "user-test", "200,00", desc+" B", "2026-07-05", "future", db)
	if rr.Code != http.StatusConflict {
		t.Fatalf("status=%d want 409 (after effective_end)", rr.Code)
	}

	_ = firstID
}

func TestRecurringFutureVersioning_CurrentGreaterThanStartClosesAtCurrentMinusOne(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	const desc = "P51-GT"
	ruleA, series, firstID := versionedSeedRule(t, db, desc, "2026-06-05", 3)
	middleID, _ := seedAdditionalCheckingOccurrences(t, db, ruleA, series, desc)

	rr := versionedRequest(t, middleID, "ws-test", "user-test", "200,00", desc+" B", "2026-07-05", "future", db)
	if rr.Code != http.StatusOK {
		t.Fatalf("status=%d", rr.Code)
	}

	_, _, _, aEnd, aActive := recurringRuleVersionFields(t, db, ruleA)
	if aActive != 0 {
		t.Fatalf("A active=%d", aActive)
	}
	if !aEnd.Valid || aEnd.Int64 != 1 {
		t.Fatalf("A end=%v want 1", aEnd)
	}

	_ = firstID
}

func TestRecurringFutureVersioning_ChildInheritsOriginalEndLimit(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	const desc = "P51-InhEnd"
	ruleA, series, firstID := versionedSeedRule(t, db, desc, "2026-06-05", 3)
	middleID, _ := seedAdditionalCheckingOccurrences(t, db, ruleA, series, desc)

	execTestSQL(t, db, `UPDATE recurring_rules SET effective_end_sequence = 5 WHERE id = ?`, ruleA)

	rr := versionedRequest(t, middleID, "ws-test", "user-test", "200,00", desc+" B", "2026-07-05", "future", db)
	if rr.Code != http.StatusOK {
		t.Fatalf("status=%d", rr.Code)
	}

	_, seq2Rule, _, _ := ruleAndTxBySequence(t, db, series, 2, 3)
	_, _, _, bEnd, bActive := recurringRuleVersionFields(t, db, seq2Rule)
	if bActive != 1 {
		t.Fatalf("child inactive")
	}
	if !bEnd.Valid || bEnd.Int64 != 5 {
		t.Fatalf("child end=%v want 5 (inherited)", bEnd)
	}

	_ = firstID
}

func TestRecurringFutureVersioning_SplitAtFinalSequenceProducesExactSingleItemChildRange(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	const desc = "P51-FinalLimit"
	ruleA, series, firstID := versionedSeedRule(t, db, desc, "2026-06-05", 3)
	_, lastID := seedAdditionalCheckingOccurrences(t, db, ruleA, series, desc)
	execTestSQL(t, db, `UPDATE recurring_rules SET effective_end_sequence = 3 WHERE id = ?`, ruleA)

	rr := versionedRequest(t, lastID, "ws-test", "user-test", "200,00", desc+" B", "2026-08-05", "future", db)
	if rr.Code != http.StatusOK {
		t.Fatalf("status=%d body=%q", rr.Code, rr.Body.String())
	}

	_, childRule, _, childTxID := ruleAndTxBySequence(t, db, series, 2, 3)
	if childTxID != lastID {
		t.Fatalf("seq3 tx=%q want moved original %q", childTxID, lastID)
	}
	_, _, aStart, aEnd, aActive := recurringRuleVersionFields(t, db, ruleA)
	if aStart != 1 || !aEnd.Valid || aEnd.Int64 != 2 || aActive != 0 {
		t.Fatalf("source start=%d end=%v active=%d want 1/2/0", aStart, aEnd, aActive)
	}
	_, parent, childStart, childEnd, childActive := recurringRuleVersionFields(t, db, childRule)
	if !parent.Valid || parent.String != ruleA || childStart != 3 || !childEnd.Valid || childEnd.Int64 != 3 || childActive != 1 {
		t.Fatalf("child parent=%v start=%d end=%v active=%d want A/3/3/1", parent, childStart, childEnd, childActive)
	}
	if activeRulesBySeries(t, db, series) != 1 {
		t.Fatal("want exactly one active rule")
	}
	_ = firstID
}

func TestRecurringFutureVersioning_ChildInheritsNullEndLimit(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	const desc = "P51-InhNull"
	ruleA, series, firstID := versionedSeedRule(t, db, desc, "2026-06-05", 3)
	middleID, _ := seedAdditionalCheckingOccurrences(t, db, ruleA, series, desc)

	rr := versionedRequest(t, middleID, "ws-test", "user-test", "200,00", desc+" B", "2026-07-05", "future", db)
	if rr.Code != http.StatusOK {
		t.Fatalf("status=%d", rr.Code)
	}

	_, seq2Rule, _, _ := ruleAndTxBySequence(t, db, series, 2, 3)
	_, _, _, bEnd, bActive := recurringRuleVersionFields(t, db, seq2Rule)
	if bActive != 1 {
		t.Fatalf("child inactive")
	}
	if bEnd.Valid {
		t.Fatalf("child end=%v want NULL", bEnd)
	}

	_ = firstID
}

func TestRecurringFutureVersioning_EmptyEndRejected(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	const desc = "P51-EndZero"
	ruleA, series, firstID := versionedSeedRule(t, db, desc, "2026-06-05", 3)
	middleID, _ := seedAdditionalCheckingOccurrences(t, db, ruleA, series, desc)

	execTestSQL(t, db, `UPDATE recurring_rules SET effective_end_sequence = 0 WHERE id = ?`, ruleA)

	rr := versionedRequest(t, middleID, "ws-test", "user-test", "200,00", desc+" B", "2026-07-05", "future", db)
	if rr.Code != http.StatusConflict {
		t.Fatalf("status=%d want 409 (end=0, current>0)", rr.Code)
	}

	_ = firstID
}

func TestRecurringFutureVersioning_ExactlyOneActiveRule(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	const desc = "P51-OneAct"
	ruleA, series, firstID := versionedSeedRule(t, db, desc, "2026-06-05", 3)
	middleID, _ := seedAdditionalCheckingOccurrences(t, db, ruleA, series, desc)

	versionedRequest(t, middleID, "ws-test", "user-test", "200,00", desc+" B", "2026-07-05", "future", db)

	activeCount := activeRulesBySeries(t, db, series)
	if activeCount != 1 {
		t.Fatalf("active rules = %d, want 1", activeCount)
	}

	_ = firstID
}

func TestRecurringFutureVersioning_InactiveSourceRejected(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	const desc = "P51-InactSrc"
	ruleA, series, firstID := versionedSeedRule(t, db, desc, "2026-06-05", 3)
	middleID, _ := seedAdditionalCheckingOccurrences(t, db, ruleA, series, desc)

	execTestSQL(t, db, `UPDATE recurring_rules SET active = 0 WHERE id = ?`, ruleA)

	rr := versionedRequest(t, middleID, "ws-test", "user-test", "200,00", desc+" B", "2026-07-05", "future", db)
	if rr.Code != http.StatusConflict {
		t.Fatalf("status=%d want 409 (inactive source)", rr.Code)
	}

	_ = firstID
}

func TestRecurringFutureVersioning_ExactTxMovement(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	const desc = "P51-TxMove"
	ruleA, series, firstID := versionedSeedRule(t, db, desc, "2026-06-05", 3)
	middleID, _ := seedAdditionalCheckingOccurrences(t, db, ruleA, series, desc)

	versionedRequest(t, middleID, "ws-test", "user-test", "200,00", desc+" B", "2026-07-05", "future", db)

	_, seq2Rule, seq2ID, seq3ID := ruleAndTxBySequence(t, db, series, 2, 3)

	var seq1Rule string
	db.QueryRow(`SELECT recurring_rule_id FROM transactions WHERE id=?`, firstID).Scan(&seq1Rule)
	if seq1Rule != ruleA {
		t.Fatalf("seq1 tx rule=%q want A=%q", seq1Rule, ruleA)
	}

	for _, id := range []string{seq2ID, seq3ID} {
		var r string
		db.QueryRow(`SELECT recurring_rule_id FROM transactions WHERE id=?`, id).Scan(&r)
		if r != seq2Rule {
			t.Fatalf("tx %s rule=%q want B=%q", id, r, seq2Rule)
		}
	}
}

func TestRecurringFutureVersioning_NoForeignTxIncluded(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	const desc = "P51-NoFor"
	ruleA, series, firstID := versionedSeedRule(t, db, desc, "2026-06-05", 3)
	middleID, _ := seedAdditionalCheckingOccurrences(t, db, ruleA, series, desc)

	now := time.Now().Unix()
	otherRuleID := "rule-other-foreign"
	execTestSQL(t, db, `INSERT INTO recurring_rules (id, workspace_id, user_id, account_id, category_id, type, amount, description, start_date, frequency, default_payment_status, active, total_occurrences, created_at, updated_at) VALUES (?, 'ws-test', 'user-test', 'checking-test', 'cat-expense-rec', 'EXPENSE', 15000, 'Other', 1, 'MONTHLY', 'PENDING', 1, 2, ?, ?)`, otherRuleID, testUnixDate("2026-06-05"), now, now)
	execTestSQL(t, db, `INSERT INTO transactions (id, workspace_id, user_id, account_id, category_id, type, amount, date, description, status, installment_number, total_installments, recurring_rule_id, recurrence_sequence, created_at, updated_at) VALUES ('tx-other-foreign', 'ws-test', 'user-test', 'checking-test', 'cat-expense-rec', 'EXPENSE', 15000, 1, 'Other', 'pending', 1, 1, ?, 1, ?, ?)`, otherRuleID, now, now)

	versionedRequest(t, middleID, "ws-test", "user-test", "200,00", desc+" B", "2026-07-05", "future", db)

	var otherTxRule string
	db.QueryRow(`SELECT recurring_rule_id FROM transactions WHERE recurring_rule_id=? LIMIT 1`, otherRuleID).Scan(&otherTxRule)
	if otherTxRule != otherRuleID {
		t.Fatalf("foreign tx rule altered: %q", otherTxRule)
	}

	var otherActive int
	db.QueryRow(`SELECT active FROM recurring_rules WHERE id=?`, otherRuleID).Scan(&otherActive)
	if otherActive != 1 {
		t.Fatalf("foreign rule deactivated")
	}

	_ = firstID
}

func TestRecurringFutureVersioning_RollbackAfterSourceDeactivation(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	const desc = "P51-RBA"
	ruleA, series, firstID := versionedSeedRule(t, db, desc, "2026-06-05", 3)
	middleID, _ := seedAdditionalCheckingOccurrences(t, db, ruleA, series, desc)
	before := versionedSeriesSnapshot(t, db, "ws-test", series)

	execTestSQL(t, db, `CREATE TEMP TRIGGER IF NOT EXISTS b1b_fail_rules BEFORE INSERT ON recurring_rules BEGIN SELECT RAISE(ABORT, 'simulated insert failure'); END`)

	rr := versionedRequest(t, middleID, "ws-test", "user-test", "200,00", desc+" B", "2026-07-05", "future", db)
	if rr.Code == http.StatusOK {
		t.Fatalf("expected failure from trigger")
	}

	execTestSQL(t, db, `DROP TRIGGER IF EXISTS b1b_fail_rules`)
	after := versionedSeriesSnapshot(t, db, "ws-test", series)
	if after != before {
		t.Fatalf("rollback snapshot changed\nbefore:\n%s\nafter:\n%s", before, after)
	}

	var aActive int
	db.QueryRow(`SELECT active FROM recurring_rules WHERE id=?`, ruleA).Scan(&aActive)
	if aActive != 1 {
		t.Fatalf("A active=%d want 1 (rollback restored)", aActive)
	}

	var ruleCount int
	db.QueryRow(`SELECT COUNT(1) FROM recurring_rules WHERE workspace_id='ws-test' AND series_id=?`, series).Scan(&ruleCount)
	if ruleCount != 1 {
		t.Fatalf("rules in series=%d want 1 (no child persisted)", ruleCount)
	}

	_ = firstID
}

func TestRecurringFutureVersioning_RollbackDuringTxMovement(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	const desc = "P51-RBTx"
	ruleA, series, firstID := versionedSeedRule(t, db, desc, "2026-06-05", 3)
	middleID, _ := seedAdditionalCheckingOccurrences(t, db, ruleA, series, desc)
	before := versionedSeriesSnapshot(t, db, "ws-test", series)

	execTestSQL(t, db, `CREATE TEMP TRIGGER IF NOT EXISTS b1b_fail_tx BEFORE UPDATE ON transactions WHEN NEW.recurring_rule_id != OLD.recurring_rule_id AND NEW.recurrence_sequence = 3 BEGIN SELECT RAISE(ABORT, 'simulated tx move failure'); END`)

	rr := versionedRequest(t, middleID, "ws-test", "user-test", "200,00", desc+" B", "2026-07-05", "future", db)
	if rr.Code == http.StatusOK {
		t.Fatalf("expected failure from trigger")
	}

	execTestSQL(t, db, `DROP TRIGGER IF EXISTS b1b_fail_tx`)
	after := versionedSeriesSnapshot(t, db, "ws-test", series)
	if after != before {
		t.Fatalf("rollback snapshot changed\nbefore:\n%s\nafter:\n%s", before, after)
	}

	var aActive int
	db.QueryRow(`SELECT active FROM recurring_rules WHERE id=?`, ruleA).Scan(&aActive)
	if aActive != 1 {
		t.Fatalf("A active=%d want 1", aActive)
	}

	_ = firstID
}

func TestRecurringFutureVersioning_ConcurrencyTwoSplitsSameSource(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	const desc = "P51-Conc"
	const series = "series-P51-Conc"
	ruleA, firstID := seedRecurringSeriesOnChecking(t, db, desc, "2026-06-05", 3)
	attachRecurringSeriesIdentity(t, db, ruleA, series)
	middleID, _ := seedAdditionalCheckingOccurrences(t, db, ruleA, series, desc)

	req := newMultipartUpdateRequest(t, "/transacoes/"+middleID, map[string]string{
		"valor":            "200,00",
		"descricao":        desc + " B",
		"data":             "2026-07-05",
		"tipo":             "despesa",
		"origem_conta_id":  "checking-test",
		"categoria_id":     "cat-expense-rec",
		"status_pagamento": "pending",
		"escopo":           "future",
	})

	h := TransactionHandler{DB: db, WorkspaceID: "ws-test", UserID: "user-test", Templates: testOOBTemplatesSeries(t)}
	rr1 := httptest.NewRecorder()
	rr2 := httptest.NewRecorder()
	h.HandleAtualizarTransacao(rr1, req, middleID)
	h.HandleAtualizarTransacao(rr2, req, middleID)

	if rr1.Code != http.StatusOK {
		t.Fatalf("first split: status=%d body=%q", rr1.Code, rr1.Body.String())
	}
	if rr2.Code != http.StatusOK {
		t.Fatalf("second split: status=%d body=%q", rr2.Code, rr2.Body.String())
	}

	rules := allRulesBySeries(t, db, series)
	if len(rules) != 3 {
		t.Fatalf("rules in series = %d, want 3 (A + two children from sequential splits)", len(rules))
	}

	activeCount := activeRulesBySeries(t, db, series)
	if activeCount != 1 {
		t.Fatalf("active rules = %d, want 1", activeCount)
	}

	_ = firstID
}

func TestRecurringFutureVersioning_PersonalNoDueDate(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()

	execTestSQL(t, db, `UPDATE workspaces SET type = 'personal' WHERE id = 'ws-test'`)

	const desc = "P51-Personal"
	ruleA, series, firstID := versionedSeedRule(t, db, desc, "2026-06-05", 3)
	middleID, _ := seedAdditionalCheckingOccurrences(t, db, ruleA, series, desc)

	rr := versionedRequest(t, middleID, "ws-test", "user-test", "200,00", desc+" B", "2026-07-05", "future", db)
	if rr.Code != http.StatusOK {
		t.Fatalf("status=%d body=%q", rr.Code, rr.Body.String())
	}

	_, seq2Rule, seq2ID, _ := ruleAndTxBySequence(t, db, series, 2, 3)
	var dueDate sql.NullInt64
	db.QueryRow(`SELECT due_date FROM transactions WHERE id=?`, seq2ID).Scan(&dueDate)
	if dueDate.Valid {
		t.Fatalf("Personal tx has due_date=%v, want null", dueDate)
	}

	var contactID sql.NullString
	db.QueryRow(`SELECT contact_id FROM recurring_rules WHERE id=?`, seq2Rule).Scan(&contactID)
	if contactID.Valid {
		t.Fatalf("Personal rule has contact_id=%v, want null", contactID)
	}

	_ = firstID
}

func TestRecurringFutureVersioning_BusinessPreservesDueDate(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()

	execTestSQL(t, db, `UPDATE workspaces SET type = 'business' WHERE id = 'ws-test'`)

	const desc = "P51-BizDue"
	ruleA, series, firstID := versionedSeedRule(t, db, desc, "2026-06-05", 3)
	middleID, _ := seedAdditionalCheckingOccurrences(t, db, ruleA, series, desc)

	execTestSQL(t, db, `UPDATE recurring_rules SET due_date_policy='SAME_DAY', due_date_offset_days=0 WHERE id=?`, ruleA)

	rr := versionedRequest(t, middleID, "ws-test", "user-test", "200,00", desc+" B", "2026-07-05", "future", db)
	if rr.Code != http.StatusOK {
		t.Fatalf("status=%d body=%q", rr.Code, rr.Body.String())
	}

	_, seq2Rule, _, _ := ruleAndTxBySequence(t, db, series, 2, 3)
	var dueDatePolicy sql.NullString
	db.QueryRow(`SELECT due_date_policy FROM recurring_rules WHERE id=?`, seq2Rule).Scan(&dueDatePolicy)
	if !dueDatePolicy.Valid || dueDatePolicy.String != "SAME_DAY" {
		t.Fatalf("child due_date_policy=%v, want SAME_DAY", dueDatePolicy)
	}

	_ = firstID
}

func TestRecurringFutureVersioning_RematerializationDoesNotDuplicate(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	const desc = "P51-ReMat"
	ruleA, series, firstID := versionedSeedRule(t, db, desc, "2026-06-05", 2)
	middleID, _ := seedAdditionalCheckingOccurrences(t, db, ruleA, series, desc)

	versionedRequest(t, middleID, "ws-test", "user-test", "200,00", desc+" B", "2026-07-05", "future", db)

	var txCount int
	db.QueryRow(`SELECT COUNT(1) FROM transactions WHERE workspace_id='ws-test' AND series_id=?`, series).Scan(&txCount)
	if txCount < 2 || txCount > 3 {
		t.Fatalf("txCount=%d, want 2-3", txCount)
	}

	_ = firstID
}

func TestRecurringFutureVersioning_SingleScopeUnaffected(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	const desc = "P51-Single"
	ruleA, series, firstID := versionedSeedRule(t, db, desc, "2026-06-05", 3)
	middleID, _ := seedAdditionalCheckingOccurrences(t, db, ruleA, series, desc)

	rr := versionedRequest(t, middleID, "ws-test", "user-test", "200,00", desc+" Sng", "2026-07-05", "single", db)
	if rr.Code != http.StatusOK {
		t.Fatalf("single scope: status=%d", rr.Code)
	}

	activeCount := activeRulesBySeries(t, db, series)
	if activeCount != 1 {
		t.Fatalf("active rules = %d after single scope, want 1", activeCount)
	}

	_ = firstID
}

func TestRecurringFutureVersioning_MovedOccurrencesKeepCivilPairs(t *testing.T) {
	tests := []struct {
		name       string
		start      string
		currentSeq int64
		splitDate  string
		wantDates  map[int64]string
	}{
		{name: "common", start: "2026-06-05", currentSeq: 2, splitDate: "2026-07-10", wantDates: map[int64]string{2: "2026-07-10", 3: "2026-08-10"}},
		{name: "february month end", start: "2026-01-31", currentSeq: 1, splitDate: "2026-01-31", wantDates: map[int64]string{1: "2026-01-31", 2: "2026-02-28", 3: "2026-03-31"}},
		{name: "year rollover", start: "2026-11-30", currentSeq: 2, splitDate: "2026-12-31", wantDates: map[int64]string{2: "2026-12-31", 3: "2027-01-31"}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			db := openTestDB(t)
			defer db.Close()
			desc := "P51-Civil-" + tc.name
			ruleID, series, _ := versionedSeedProducedRule(t, db, "personal", desc, tc.start, "", "", 3)
			currentID := versionedTxIDBySequence(t, db, series, tc.currentSeq)
			rr := versionedRequest(t, currentID, "ws-test", "user-test", "200,00", desc+" split", tc.splitDate, "future", db)
			if rr.Code != http.StatusOK {
				t.Fatalf("status=%d body=%q", rr.Code, rr.Body.String())
			}
			for seq, wantCivil := range tc.wantDates {
				var gotUnix int64
				var gotCivil string
				if err := db.QueryRow(`SELECT date, date_civil FROM transactions WHERE workspace_id='ws-test' AND series_id=? AND recurrence_sequence=?`, series, seq).Scan(&gotUnix, &gotCivil); err != nil {
					t.Fatalf("seq %d: %v", seq, err)
				}
				if gotCivil != wantCivil || LegacyUnixToCivilDateUTC(gotUnix).String() != wantCivil {
					t.Fatalf("seq %d date=%s civil=%q want=%s", seq, LegacyUnixToCivilDateUTC(gotUnix).String(), gotCivil, wantCivil)
				}
			}
			var sourceEnd sql.NullInt64
			var sourceEndCivil sql.NullString
			if err := db.QueryRow(`SELECT end_date, end_date_civil FROM recurring_rules WHERE id=?`, ruleID).Scan(&sourceEnd, &sourceEndCivil); err != nil {
				t.Fatalf("source end: %v", err)
			}
			if tc.currentSeq == 1 {
				if sourceEnd.Valid || sourceEndCivil.Valid {
					t.Fatalf("source end=%v civil=%v want NULL/NULL", sourceEnd, sourceEndCivil)
				}
			} else if !sourceEnd.Valid || !sourceEndCivil.Valid || LegacyUnixToCivilDateUTC(sourceEnd.Int64).String() != sourceEndCivil.String {
				t.Fatalf("source end=%v civil=%v want coherent", sourceEnd, sourceEndCivil)
			}
		})
	}
}

func TestRecurringFutureVersioning_RealProducerBusinessDueDateOffsets(t *testing.T) {
	tests := []struct {
		name         string
		dueDate      string
		wantMovedDue []string
		wantDueCivil string
	}{
		{name: "positive", dueDate: "2026-06-15", wantMovedDue: []string{"2026-07-15", "2026-08-15"}, wantDueCivil: "2026-09-15"},
		{name: "zero", dueDate: "2026-06-05", wantMovedDue: []string{"2026-07-05", "2026-08-05"}, wantDueCivil: "2026-09-05"},
		{name: "negative", dueDate: "2026-05-31", wantMovedDue: []string{"2026-06-30", "2026-07-31"}, wantDueCivil: "2026-08-31"},
		{name: "absent", dueDate: "", wantMovedDue: nil, wantDueCivil: ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			db := openTestDB(t)
			defer db.Close()
			desc := "P51-RealDue-" + tc.name
			_, series, _ := versionedSeedProducedRule(t, db, "business", desc, "2026-06-05", tc.dueDate, "contact-real-due", 5)
			currentID := versionedTxIDBySequence(t, db, series, 2)
			execTestSQL(t, db, `DELETE FROM transactions WHERE workspace_id='ws-test' AND series_id=? AND recurrence_sequence > 3`, series)
			rr := versionedRequest(t, currentID, "ws-test", "user-test", "200,00", desc+" split", "2026-07-05", "future", db)
			if rr.Code != http.StatusOK {
				t.Fatalf("status=%d body=%q", rr.Code, rr.Body.String())
			}
			for seq := 2; seq <= 3; seq++ {
				var movedDueUnix sql.NullInt64
				var movedDueCivil sql.NullString
				var movedContact sql.NullString
				if err := db.QueryRow(`SELECT due_date, due_date_civil, contact_id FROM transactions WHERE workspace_id='ws-test' AND series_id=? AND recurrence_sequence=?`, series, seq).Scan(&movedDueUnix, &movedDueCivil, &movedContact); err != nil {
					t.Fatalf("moved seq%d: %v", seq, err)
				}
				if tc.wantMovedDue == nil {
					if movedDueUnix.Valid || movedDueCivil.Valid {
						t.Fatalf("moved seq%d due=%v civil=%v want NULL", seq, movedDueUnix, movedDueCivil)
					}
				} else {
					want := tc.wantMovedDue[seq-2]
					if !movedDueUnix.Valid || !movedDueCivil.Valid || movedDueCivil.String != want || LegacyUnixToCivilDateUTC(movedDueUnix.Int64).String() != want {
						t.Fatalf("moved seq%d due=%v civil=%v want=%s", seq, movedDueUnix, movedDueCivil, want)
					}
				}
				if !movedContact.Valid || movedContact.String != "contact-real-due" {
					t.Fatalf("moved seq%d contact=%v want contact-real-due", seq, movedContact)
				}
			}
			var dateUnix int64
			var dateCivil string
			var dueUnix sql.NullInt64
			var dueCivil sql.NullString
			var contact sql.NullString
			if err := db.QueryRow(`SELECT date, date_civil, due_date, due_date_civil, contact_id FROM transactions WHERE workspace_id='ws-test' AND series_id=? AND recurrence_sequence=4`, series).Scan(&dateUnix, &dateCivil, &dueUnix, &dueCivil, &contact); err != nil {
				t.Fatalf("projected seq4: %v", err)
			}
			if dateCivil != "2026-09-05" || LegacyUnixToCivilDateUTC(dateUnix).String() != dateCivil {
				t.Fatalf("date=%d civil=%q want 2026-09-05", dateUnix, dateCivil)
			}
			if tc.wantDueCivil == "" {
				if dueUnix.Valid || dueCivil.Valid {
					t.Fatalf("due=%v civil=%v want NULL", dueUnix, dueCivil)
				}
			} else if !dueUnix.Valid || !dueCivil.Valid || dueCivil.String != tc.wantDueCivil || LegacyUnixToCivilDateUTC(dueUnix.Int64).String() != tc.wantDueCivil {
				t.Fatalf("due=%v civil=%v want=%s", dueUnix, dueCivil, tc.wantDueCivil)
			}
			if !contact.Valid || contact.String != "contact-real-due" {
				t.Fatalf("contact=%v want contact-real-due", contact)
			}
			if err := EnsureRecurringMaterialized(db, "ws-test", MustCivilWriteDate("2027-12-31")); err != nil {
				t.Fatalf("materialize after projection: %v", err)
			}
		})
	}
}

func TestRecurringFutureVersioning_PersonalIgnoresUnexpectedDueOffset(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	ruleID, series, _ := versionedSeedProducedRule(t, db, "personal", "P51-PersonalOffset", "2026-06-05", "", "", 5)
	currentID := versionedTxIDBySequence(t, db, series, 2)
	execTestSQL(t, db, `UPDATE recurring_rules SET due_date_offset_days=10 WHERE id=?`, ruleID)
	execTestSQL(t, db, `DELETE FROM transactions WHERE workspace_id='ws-test' AND series_id=? AND recurrence_sequence > 3`, series)
	rr := versionedRequest(t, currentID, "ws-test", "user-test", "200,00", "P51-PersonalOffset split", "2026-07-05", "future", db)
	if rr.Code != http.StatusOK {
		t.Fatalf("status=%d body=%q", rr.Code, rr.Body.String())
	}
	var due sql.NullInt64
	var dueCivil sql.NullString
	var contact sql.NullString
	if err := db.QueryRow(`SELECT due_date, due_date_civil, contact_id FROM transactions WHERE workspace_id='ws-test' AND series_id=? AND recurrence_sequence=4`, series).Scan(&due, &dueCivil, &contact); err != nil {
		t.Fatalf("seq4: %v", err)
	}
	if due.Valid || dueCivil.Valid || contact.Valid {
		t.Fatalf("Personal due=%v civil=%v contact=%v want NULL", due, dueCivil, contact)
	}
}

func TestRecurringFutureVersioning_CrossWorkspaceKnownIDsRejectedAndUntouched(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	ruleA, series, _ := versionedSeedRule(t, db, "P51-Tenant", "2026-06-05", 3)
	middleID, _ := seedAdditionalCheckingOccurrences(t, db, ruleA, series, "P51-Tenant")
	seedOtherWorkspaceRecurringSeries(t, db)

	rrForeign := versionedRequest(t, "other-rec-2", "ws-test", "user-test", "999,00", "foreign", "2026-07-06", "future", db)
	if rrForeign.Code != http.StatusConflict {
		t.Fatalf("foreign status=%d want 409", rrForeign.Code)
	}
	assertOtherWorkspaceRecurringUnchanged(t, db)
	var foreignActive int
	var foreignSeries sql.NullString
	if err := db.QueryRow(`SELECT active, series_id FROM recurring_rules WHERE id='rule-other' AND workspace_id='ws-other'`).Scan(&foreignActive, &foreignSeries); err != nil {
		t.Fatalf("foreign rule: %v", err)
	}
	if foreignActive != 1 || foreignSeries.Valid {
		t.Fatalf("foreign rule active=%d series=%v want 1/NULL", foreignActive, foreignSeries)
	}

	rrLocal := versionedRequest(t, middleID, "ws-test", "user-test", "200,00", "P51-Tenant split", "2026-07-05", "future", db)
	if rrLocal.Code != http.StatusOK {
		t.Fatalf("local status=%d body=%q", rrLocal.Code, rrLocal.Body.String())
	}
	assertOtherWorkspaceRecurringUnchanged(t, db)
	if err := db.QueryRow(`SELECT active, series_id FROM recurring_rules WHERE id='rule-other' AND workspace_id='ws-other'`).Scan(&foreignActive, &foreignSeries); err != nil {
		t.Fatalf("foreign rule after local split: %v", err)
	}
	if foreignActive != 1 || foreignSeries.Valid {
		t.Fatalf("foreign rule after local split active=%d series=%v", foreignActive, foreignSeries)
	}
}

func TestRecurringFutureVersioning_ChildLimitsFutureProjection(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	const desc = "P51-LimitProj"
	ruleA, series, firstID := versionedSeedRule(t, db, desc, "2026-06-05", 3)
	middleID, _ := seedAdditionalCheckingOccurrences(t, db, ruleA, series, desc)
	execTestSQL(t, db, `UPDATE recurring_rules SET effective_end_sequence = 5, total_occurrences = NULL WHERE id = ?`, ruleA)

	rr := versionedRequest(t, middleID, "ws-test", "user-test", "200,00", desc+" B", "2026-07-05", "future", db)
	if rr.Code != http.StatusOK {
		t.Fatalf("status=%d body=%q", rr.Code, rr.Body.String())
	}

	var maxSeq sql.NullInt64
	db.QueryRow(`SELECT MAX(recurrence_sequence) FROM transactions WHERE workspace_id='ws-test' AND series_id=?`, series).Scan(&maxSeq)
	if !maxSeq.Valid || maxSeq.Int64 != 5 {
		t.Fatalf("max seq=%v want 5", maxSeq)
	}

	var txCount int
	db.QueryRow(`SELECT COUNT(1) FROM transactions WHERE workspace_id='ws-test' AND series_id=?`, series).Scan(&txCount)
	if txCount != 5 {
		t.Fatalf("tx count=%d want 5", txCount)
	}
	_ = firstID
}

func TestRecurringFutureVersioning_ChildProjectsDueDate(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	const desc = "P51-DueProj"
	ruleA, series, firstID := versionedSeedRule(t, db, desc, "2026-06-05", 2)
	middleID, _ := seedAdditionalCheckingOccurrences(t, db, ruleA, series, desc)
	execTestSQL(t, db, `UPDATE workspaces SET type = 'business' WHERE id = 'ws-test'`)
	execTestSQL(t, db, `UPDATE recurring_rules SET due_date_policy='SAME_DAY', due_date_offset_days=0, total_occurrences=NULL WHERE id=?`, ruleA)

	rr := versionedRequest(t, middleID, "ws-test", "user-test", "200,00", desc+" B", "2026-07-05", "future", db)
	if rr.Code != http.StatusOK {
		t.Fatalf("status=%d body=%q", rr.Code, rr.Body.String())
	}
	rows, err := db.Query(`SELECT recurrence_sequence, date, due_date FROM transactions WHERE workspace_id='ws-test' AND series_id=? AND recurrence_sequence > 3 ORDER BY recurrence_sequence`, series)
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	defer rows.Close()
	rowCount := 0
	for rows.Next() {
		rowCount++
		var seq, date, due int64
		if err := rows.Scan(&seq, &date, &due); err != nil {
			t.Fatalf("scan: %v", err)
		}
		if due != date {
			t.Fatalf("seq %d due_date=%d != date=%d", seq, due, date)
		}
	}
	if rowCount == 0 {
		t.Fatal("no projected rows checked")
	}
	_ = firstID
}

func TestRecurringFutureVersioning_ChildCivilFieldsPopulated(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	const desc = "P51-Civil"
	ruleA, series, firstID := versionedSeedRule(t, db, desc, "2026-06-05", 3)
	middleID, _ := seedAdditionalCheckingOccurrences(t, db, ruleA, series, desc)

	rr := versionedRequest(t, middleID, "ws-test", "user-test", "200,00", desc+" B", "2026-07-05", "future", db)
	if rr.Code != http.StatusOK {
		t.Fatalf("status=%d body=%q", rr.Code, rr.Body.String())
	}

	_, seq2Rule, _, _ := ruleAndTxBySequence(t, db, series, 2, 3)
	var startCivil, startsCivil string
	db.QueryRow(`SELECT start_date_civil, starts_at_civil FROM recurring_rules WHERE id=?`, seq2Rule).Scan(&startCivil, &startsCivil)
	if startCivil != "2026-07-05" || startsCivil != "2026-07-05" {
		t.Fatalf("civil fields start=%q starts=%q want 2026-07-05", startCivil, startsCivil)
	}
	_ = firstID
}

func TestRecurringFutureVersioning_ConcurrencyRealTwoSplits(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "conc.db")
	db, err := database.Open(dbPath)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer db.Close()
	db.SetMaxOpenConns(2)
	const desc = "P51-ConcReal"
	const series = "series-P51-ConcReal"
	ruleA, firstID := seedRecurringSeriesOnChecking(t, db, desc, "2026-06-05", 3)
	attachRecurringSeriesIdentity(t, db, ruleA, series)
	middleID, _ := seedAdditionalCheckingOccurrences(t, db, ruleA, series, desc)

	type splitResult struct {
		childID string
		err     error
	}
	ready := make(chan struct{}, 2)
	start := make(chan struct{})
	results := make(chan splitResult, 2)
	var wg sync.WaitGroup
	wg.Add(2)
	for i := 0; i < 2; i++ {
		go func() {
			defer wg.Done()
			tx, beginErr := db.Begin()
			if beginErr != nil {
				ready <- struct{}{}
				results <- splitResult{err: beginErr}
				return
			}
			ready <- struct{}{}
			<-start
			h := TransactionHandler{DB: db, WorkspaceID: "ws-test", UserID: "user-test"}
			childID, _, _, splitErr := h.splitFutureRecurringRuleTx(tx, recurringFutureSplitParams{
				CurrentID:       middleID,
				RuleID:          ruleA,
				SeriesID:        series,
				CurrentSequence: 2,
				Type:            "EXPENSE",
				Amount:          20000,
				Anchor:          MustCivilWriteDate("2026-07-05"),
				Description:     desc + " B",
				AccountID:       "checking-test",
				CategoryID:      "cat-expense-rec",
				Status:          "pending",
				Frequency:       "MONTHLY",
				Now:             time.Now().Unix(),
			})
			if splitErr == nil {
				splitErr = tx.Commit()
			} else {
				_ = tx.Rollback()
			}
			results <- splitResult{childID: childID, err: splitErr}
		}()
	}
	<-ready
	<-ready
	close(start)
	wg.Wait()
	close(results)

	okCount := 0
	var successfulChild string
	for result := range results {
		if result.err == nil {
			okCount++
			successfulChild = result.childID
			continue
		}
		errText := result.err.Error()
		if !strings.Contains(errText, "SQLITE_BUSY") && !strings.Contains(errText, "database is locked") && !strings.Contains(errText, "source rule deactivation affected 0 rows") {
			t.Fatalf("unexpected concurrent error: %v", result.err)
		}
	}
	if okCount != 1 {
		t.Fatalf("successful splits=%d want exactly 1", okCount)
	}

	for seq := int64(1); seq <= 3; seq++ {
		var count int
		var txRule string
		if err := db.QueryRow(`SELECT COUNT(1), MIN(recurring_rule_id) FROM transactions WHERE workspace_id='ws-test' AND series_id=? AND recurrence_sequence=?`, series, seq).Scan(&count, &txRule); err != nil {
			t.Fatalf("seq %d: %v", seq, err)
		}
		if count != 1 {
			t.Fatalf("seq %d count=%d want 1", seq, count)
		}
		wantRule := ruleA
		if seq >= 2 {
			wantRule = successfulChild
		}
		if txRule != wantRule {
			t.Fatalf("seq %d rule=%q want=%q", seq, txRule, wantRule)
		}
	}

	rules := allRulesBySeries(t, db, series)
	if len(rules) != 2 {
		t.Fatalf("rules=%d want 2", len(rules))
	}
	aSeries, aParent, aStart, aEnd, aActive := recurringRuleVersionFields(t, db, ruleA)
	if aSeries != series || aParent.Valid || aStart != 1 || !aEnd.Valid || aEnd.Int64 != 1 || aActive != 0 {
		t.Fatalf("source series=%q parent=%v start=%d end=%v active=%d", aSeries, aParent, aStart, aEnd, aActive)
	}
	bSeries, bParent, bStart, bEnd, bActive := recurringRuleVersionFields(t, db, successfulChild)
	if bSeries != series || !bParent.Valid || bParent.String != ruleA || bStart != 2 || bEnd.Valid || bActive != 1 {
		t.Fatalf("child series=%q parent=%v start=%d end=%v active=%d", bSeries, bParent, bStart, bEnd, bActive)
	}
	if activeRulesBySeries(t, db, series) != 1 {
		t.Fatal("want exactly one active rule")
	}
	var orphanCount int
	if err := db.QueryRow(`SELECT COUNT(1) FROM recurring_rules c LEFT JOIN recurring_rules p ON p.id=c.parent_rule_id AND p.workspace_id=c.workspace_id WHERE c.workspace_id='ws-test' AND c.series_id=? AND c.parent_rule_id IS NOT NULL AND p.id IS NULL`, series).Scan(&orphanCount); err != nil {
		t.Fatalf("orphans: %v", err)
	}
	if orphanCount != 0 {
		t.Fatalf("orphan children=%d want 0", orphanCount)
	}
	_ = firstID
}

func TestRecurringFutureVersioning_RollbackDuringProjection(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	const desc = "P51-RBProj"
	ruleA, series, firstID := versionedSeedProducedRule(t, db, "business", desc, "2026-06-05", "2026-06-15", "contact-rollback", 5)
	middleID := versionedTxIDBySequence(t, db, series, 2)
	execTestSQL(t, db, `DELETE FROM transactions WHERE workspace_id='ws-test' AND series_id=? AND recurrence_sequence > 3`, series)
	before := versionedSeriesSnapshot(t, db, "ws-test", series)

	execTestSQL(t, db, `CREATE TEMP TRIGGER IF NOT EXISTS b1b_fail_proj BEFORE INSERT ON transactions WHEN NEW.recurrence_sequence = 4 BEGIN SELECT RAISE(ABORT, 'simulated projection failure'); END`)

	rr := versionedRequest(t, middleID, "ws-test", "user-test", "200,00", desc+" B", "2026-07-05", "future", db)
	if rr.Code == http.StatusOK {
		t.Fatalf("expected failure from trigger")
	}

	execTestSQL(t, db, `DROP TRIGGER IF EXISTS b1b_fail_proj`)
	after := versionedSeriesSnapshot(t, db, "ws-test", series)
	if after != before {
		t.Fatalf("rollback snapshot changed\nbefore:\n%s\nafter:\n%s", before, after)
	}

	var aActive int
	db.QueryRow(`SELECT active FROM recurring_rules WHERE id=?`, ruleA).Scan(&aActive)
	if aActive != 1 {
		t.Fatalf("A active=%d want 1", aActive)
	}

	var ruleCount int
	db.QueryRow(`SELECT COUNT(1) FROM recurring_rules WHERE workspace_id='ws-test' AND series_id=?`, series).Scan(&ruleCount)
	if ruleCount != 1 {
		t.Fatalf("rules=%d want 1", ruleCount)
	}

	var txOnA int
	db.QueryRow(`SELECT COUNT(1) FROM transactions WHERE workspace_id='ws-test' AND series_id=? AND recurring_rule_id=?`, series, ruleA).Scan(&txOnA)
	if txOnA != 3 {
		t.Fatalf("tx on A=%d want 3", txOnA)
	}
	_ = firstID
}

func TestRecurringFutureVersioning_RealProducersPersistContactContract(t *testing.T) {
	scenarios := []struct {
		name, wsType, contactID string
		wantContact             bool
	}{
		{name: "business with contact", wsType: "business", contactID: "contact-contract", wantContact: true},
		{name: "business without contact", wsType: "business"},
		{name: "personal ignores unexpected contact", wsType: "personal", contactID: "contact-contract"},
	}
	for _, producer := range []string{"insertTransaction", "insertRecurringRuleTx"} {
		producer := producer
		for _, scenario := range scenarios {
			scenario := scenario
			t.Run(producer+"/"+scenario.name, func(t *testing.T) {
				db := openTestDB(t)
				defer db.Close()
				desc := "P51-Contact-" + producer + "-" + scenario.name
				ruleID, seriesID := versionedCreateRuleWithRealProducer(t, db, producer, scenario.wsType, desc, scenario.contactID)

				var ruleContact sql.NullString
				if err := db.QueryRow(`SELECT contact_id FROM recurring_rules WHERE id=? AND workspace_id='ws-test'`, ruleID).Scan(&ruleContact); err != nil {
					t.Fatalf("load rule contact: %v", err)
				}
				if scenario.wantContact {
					if !ruleContact.Valid || ruleContact.String != scenario.contactID {
						t.Fatalf("rule contact=%v want %q", ruleContact, scenario.contactID)
					}
				} else if ruleContact.Valid {
					t.Fatalf("rule contact=%v want NULL", ruleContact)
				}

				rows, err := db.Query(`SELECT contact_id FROM transactions WHERE workspace_id='ws-test' AND recurring_rule_id=? AND series_id=?`, ruleID, seriesID)
				if err != nil {
					t.Fatalf("query projected contacts: %v", err)
				}
				exercised := false
				for rows.Next() {
					exercised = true
					var projectedContact sql.NullString
					if err := rows.Scan(&projectedContact); err != nil {
						rows.Close()
						t.Fatalf("scan projected contact: %v", err)
					}
					if scenario.wantContact {
						if !projectedContact.Valid || projectedContact.String != scenario.contactID {
							rows.Close()
							t.Fatalf("projected contact=%v want %q", projectedContact, scenario.contactID)
						}
					} else if projectedContact.Valid {
						rows.Close()
						t.Fatalf("projected contact=%v want NULL", projectedContact)
					}
				}
				if err := rows.Err(); err != nil {
					rows.Close()
					t.Fatalf("iterate projected contacts: %v", err)
				}
				if err := rows.Close(); err != nil {
					t.Fatalf("close projected contacts: %v", err)
				}
				if !exercised {
					t.Fatal("producer created no projected occurrence")
				}
			})
		}
	}
}

func TestRecurringFutureVersioning_ForeignContactRejectedWithoutPartialRule(t *testing.T) {
	for _, producer := range []string{"insertTransaction", "insertRecurringRuleTx"} {
		producer := producer
		t.Run(producer, func(t *testing.T) {
			db := openTestDB(t)
			defer db.Close()
			seedInvoicePaymentScenario(t, db)
			seedRecurringExpenseCategory(t, db)
			execTestSQL(t, db, `UPDATE workspaces SET type='business' WHERE id='ws-test'`)
			seedOtherWorkspaceForRecurringCreate(t, db)
			execTestSQL(t, db, `INSERT INTO contacts (id, workspace_id, name, type, created_at) VALUES ('contact-foreign-rule', 'ws-other', 'Foreign', 'vendor', ?)`, time.Now().Unix())

			h := TransactionHandler{DB: db, WorkspaceID: "ws-test", UserID: "user-test", Templates: testOOBTemplatesSeries(t)}
			const desc = "P51-ForeignContact"
			var txID string
			if producer == "insertRecurringRuleTx" {
				if _, err := h.insertTransaction(
					"EXPENSE", 10000, desc, "", "", MustCivilWriteDate("2026-06-05"),
					"checking-test", "", "cat-expense-rec", 1, "pending", false, "",
					"", OptionalCivilWriteDate{}, nil, "", false,
				); err != nil {
					t.Fatalf("seed conversion transaction: %v", err)
				}
				if err := db.QueryRow(`SELECT id FROM transactions WHERE workspace_id='ws-test' AND description=?`, desc).Scan(&txID); err != nil {
					t.Fatalf("load conversion transaction: %v", err)
				}
			}

			var rulesBefore, seriesBefore, txBefore int
			if err := db.QueryRow(`SELECT COUNT(1) FROM recurring_rules WHERE workspace_id='ws-test'`).Scan(&rulesBefore); err != nil {
				t.Fatalf("rules before: %v", err)
			}
			if err := db.QueryRow(`SELECT COUNT(1) FROM recurring_series WHERE workspace_id='ws-test'`).Scan(&seriesBefore); err != nil {
				t.Fatalf("series before: %v", err)
			}
			if err := db.QueryRow(`SELECT COUNT(1) FROM transactions WHERE workspace_id='ws-test'`).Scan(&txBefore); err != nil {
				t.Fatalf("transactions before: %v", err)
			}

			if producer == "insertTransaction" {
				total := int64(3)
				if _, err := h.insertTransaction(
					"EXPENSE", 10000, desc, "", "", MustCivilWriteDate("2026-06-05"),
					"checking-test", "", "cat-expense-rec", 1, "pending", true, "MONTHLY",
					"contact-foreign-rule", MustOptionalCivilWriteDate("2026-06-15"), &total, "", false,
				); err == nil {
					t.Fatal("foreign contact accepted by insertTransaction")
				}
			} else {
				req := newMultipartUpdateRequest(t, "/transacoes/"+txID, map[string]string{
					"valor":            "100,00",
					"descricao":        desc,
					"data":             "2026-06-05",
					"tipo":             "despesa",
					"origem_conta_id":  "checking-test",
					"categoria_id":     "cat-expense-rec",
					"status_pagamento": "pending",
					"lancamento_fixo":  "on",
					"recorrencia":      "MONTHLY",
					"contact_id":       "contact-foreign-rule",
					"due_date":         "2026-06-15",
					"escopo":           "single",
				})
				rr := httptest.NewRecorder()
				h.HandleAtualizarTransacao(rr, req, txID)
				if rr.Code != http.StatusBadRequest {
					t.Fatalf("foreign conversion status=%d body=%q want 400", rr.Code, rr.Body.String())
				}
			}

			var rulesAfter, seriesAfter, txAfter int
			db.QueryRow(`SELECT COUNT(1) FROM recurring_rules WHERE workspace_id='ws-test'`).Scan(&rulesAfter)
			db.QueryRow(`SELECT COUNT(1) FROM recurring_series WHERE workspace_id='ws-test'`).Scan(&seriesAfter)
			db.QueryRow(`SELECT COUNT(1) FROM transactions WHERE workspace_id='ws-test'`).Scan(&txAfter)
			if rulesAfter != rulesBefore || seriesAfter != seriesBefore || txAfter != txBefore {
				t.Fatalf("partial mutation rules=%d/%d series=%d/%d tx=%d/%d", rulesBefore, rulesAfter, seriesBefore, seriesAfter, txBefore, txAfter)
			}
			if txID != "" {
				var ruleRef, seriesRef sql.NullString
				if err := db.QueryRow(`SELECT recurring_rule_id, series_id FROM transactions WHERE id=? AND workspace_id='ws-test'`, txID).Scan(&ruleRef, &seriesRef); err != nil {
					t.Fatalf("load conversion rollback: %v", err)
				}
				if ruleRef.Valid || seriesRef.Valid {
					t.Fatalf("conversion rollback left rule=%v series=%v", ruleRef, seriesRef)
				}
			}
		})
	}
}

func TestRecurringFutureVersioning_RematerializationExact(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	const desc = "P51-ReMatEx"
	ruleID, series, _ := versionedSeedProducedRule(t, db, "business", desc, "2026-06-05", "2026-06-15", "contact-remat", 5)

	var ruleContact sql.NullString
	if err := db.QueryRow(`SELECT contact_id FROM recurring_rules WHERE id=? AND workspace_id='ws-test'`, ruleID).Scan(&ruleContact); err != nil {
		t.Fatalf("load producer rule contact: %v", err)
	}
	if !ruleContact.Valid || ruleContact.String != "contact-remat" {
		t.Fatalf("producer rule contact=%v want contact-remat", ruleContact)
	}

	var expected struct {
		id, workspaceID, ruleID, seriesID, dateCivil, dueCivil, contactID string
		sequence, dateUnix, dueUnix                                       int64
	}
	if err := db.QueryRow(`SELECT id, workspace_id, recurring_rule_id, series_id, recurrence_sequence, date, date_civil, due_date, due_date_civil, contact_id FROM transactions WHERE workspace_id='ws-test' AND series_id=? AND recurrence_sequence=4`, series).Scan(
		&expected.id, &expected.workspaceID, &expected.ruleID, &expected.seriesID, &expected.sequence,
		&expected.dateUnix, &expected.dateCivil, &expected.dueUnix, &expected.dueCivil, &expected.contactID,
	); err != nil {
		t.Fatalf("load future occurrence: %v", err)
	}
	result, err := db.Exec(`DELETE FROM transactions WHERE id=? AND workspace_id='ws-test'`, expected.id)
	if err != nil {
		t.Fatalf("delete future occurrence: %v", err)
	}
	if affected, err := result.RowsAffected(); err != nil || affected != 1 {
		t.Fatalf("deleted rows=%d err=%v want 1", affected, err)
	}
	var gapCount int
	if err := db.QueryRow(`SELECT COUNT(1) FROM transactions WHERE workspace_id='ws-test' AND series_id=? AND recurrence_sequence=4`, series).Scan(&gapCount); err != nil {
		t.Fatalf("count gap: %v", err)
	}
	if gapCount != 0 {
		t.Fatalf("gap count=%d want 0", gapCount)
	}

	if err := EnsureRecurringMaterialized(db, "ws-test", MustCivilWriteDate("2027-12-31")); err != nil {
		t.Fatalf("materialize: %v", err)
	}

	var rebuiltID, rebuiltWorkspace, rebuiltRule, rebuiltSeries, dateCivil, dueCivil, contact string
	var rebuiltSeq, dateUnix, dueUnix int64
	if err := db.QueryRow(`SELECT id, workspace_id, recurring_rule_id, series_id, recurrence_sequence, date, date_civil, due_date, due_date_civil, contact_id FROM transactions WHERE workspace_id='ws-test' AND series_id=? AND recurrence_sequence=4`, series).Scan(&rebuiltID, &rebuiltWorkspace, &rebuiltRule, &rebuiltSeries, &rebuiltSeq, &dateUnix, &dateCivil, &dueUnix, &dueCivil, &contact); err != nil {
		t.Fatalf("rebuilt seq4: %v", err)
	}
	if rebuiltID == expected.id || rebuiltID == "" {
		t.Fatalf("rebuilt UUID=%q old=%q want new", rebuiltID, expected.id)
	}
	if rebuiltWorkspace != expected.workspaceID || rebuiltRule != expected.ruleID || rebuiltSeries != expected.seriesID || rebuiltSeq != expected.sequence {
		t.Fatalf("rebuilt identity workspace=%q rule=%q series=%q seq=%d", rebuiltWorkspace, rebuiltRule, rebuiltSeries, rebuiltSeq)
	}
	if dateUnix != expected.dateUnix || dateCivil != expected.dateCivil || LegacyUnixToCivilDateUTC(dateUnix).String() != dateCivil {
		t.Fatalf("rebuilt date=%d/%q want %d/%q", dateUnix, dateCivil, expected.dateUnix, expected.dateCivil)
	}
	if dueUnix != expected.dueUnix || dueCivil != expected.dueCivil || LegacyUnixToCivilDateUTC(dueUnix).String() != dueCivil {
		t.Fatalf("rebuilt due=%d/%q want %d/%q", dueUnix, dueCivil, expected.dueUnix, expected.dueCivil)
	}
	if contact != expected.contactID {
		t.Fatalf("rebuilt contact=%q want %q", contact, expected.contactID)
	}
	if err := EnsureRecurringMaterialized(db, "ws-test", MustCivilWriteDate("2027-12-31")); err != nil {
		t.Fatalf("second materialize: %v", err)
	}
	var afterCount int
	var afterID string
	if err := db.QueryRow(`SELECT COUNT(1), MIN(id) FROM transactions WHERE workspace_id='ws-test' AND series_id=? AND recurrence_sequence=4`, series).Scan(&afterCount, &afterID); err != nil {
		t.Fatalf("second materialize state: %v", err)
	}
	if afterCount != 1 || afterID != rebuiltID {
		t.Fatalf("second materialize count=%d id=%q want 1/%q", afterCount, afterID, rebuiltID)
	}
}

// ---------- HELPERS ----------

func ruleAndTxBySequence(t *testing.T, db *sql.DB, seriesID string, seq2, seq3 int64) (seq2Rule, seq3Rule, seq2ID, seq3ID string) {
	t.Helper()
	db.QueryRow(`SELECT id, recurring_rule_id FROM transactions WHERE workspace_id='ws-test' AND series_id=? AND recurrence_sequence=?`, seriesID, seq2).Scan(&seq2ID, &seq2Rule)
	db.QueryRow(`SELECT id, recurring_rule_id FROM transactions WHERE workspace_id='ws-test' AND series_id=? AND recurrence_sequence=?`, seriesID, seq3).Scan(&seq3ID, &seq3Rule)
	return
}

func allRulesBySeries(t *testing.T, db *sql.DB, seriesID string) []string {
	t.Helper()
	rows, err := db.Query(`SELECT id FROM recurring_rules WHERE workspace_id='ws-test' AND series_id=?`, seriesID)
	if err != nil {
		t.Fatalf("query rules: %v", err)
	}
	defer rows.Close()
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			t.Fatalf("scan rule: %v", err)
		}
		ids = append(ids, id)
	}
	return ids
}

func activeRulesBySeries(t *testing.T, db *sql.DB, seriesID string) int {
	t.Helper()
	var count int
	db.QueryRow(`SELECT COUNT(1) FROM recurring_rules WHERE workspace_id='ws-test' AND series_id=? AND active=1`, seriesID).Scan(&count)
	return count
}

func versionedSeriesSnapshot(t *testing.T, db *sql.DB, workspaceID, seriesID string) string {
	t.Helper()
	var out strings.Builder
	rules, err := db.Query(`SELECT id, active, effective_start_sequence, effective_end_sequence, parent_rule_id, start_date, start_date_civil, starts_at, starts_at_civil, end_date, end_date_civil, generated_until, generated_until_civil, due_date_policy, due_date_offset_days, contact_id FROM recurring_rules WHERE workspace_id=? AND series_id=? ORDER BY id`, workspaceID, seriesID)
	if err != nil {
		t.Fatalf("snapshot rules: %v", err)
	}
	for rules.Next() {
		var id string
		var active, start int64
		var end, parent, startUnix, startCivil, startsAt, startsCivil, endDate, endCivil, generated, generatedCivil, policy, offset, contact interface{}
		if err := rules.Scan(&id, &active, &start, &end, &parent, &startUnix, &startCivil, &startsAt, &startsCivil, &endDate, &endCivil, &generated, &generatedCivil, &policy, &offset, &contact); err != nil {
			rules.Close()
			t.Fatalf("snapshot rule scan: %v", err)
		}
		fmt.Fprintf(&out, "R|%s|%d|%d|%v|%v|%v|%v|%v|%v|%v|%v|%v|%v|%v|%v|%v\n", id, active, start, end, parent, startUnix, startCivil, startsAt, startsCivil, endDate, endCivil, generated, generatedCivil, policy, offset, contact)
	}
	if err := rules.Close(); err != nil {
		t.Fatalf("snapshot rules close: %v", err)
	}
	txs, err := db.Query(`SELECT id, recurring_rule_id, recurrence_sequence, type, amount, date, date_civil, due_date, due_date_civil, contact_id, status, invoice_id, is_recurring_exception FROM transactions WHERE workspace_id=? AND series_id=? ORDER BY recurrence_sequence, id`, workspaceID, seriesID)
	if err != nil {
		t.Fatalf("snapshot txs: %v", err)
	}
	for txs.Next() {
		var id string
		var rule, seq, trType, amount, date, dateCivil, due, dueCivil, contact, status, invoice, exception interface{}
		if err := txs.Scan(&id, &rule, &seq, &trType, &amount, &date, &dateCivil, &due, &dueCivil, &contact, &status, &invoice, &exception); err != nil {
			txs.Close()
			t.Fatalf("snapshot tx scan: %v", err)
		}
		fmt.Fprintf(&out, "T|%s|%v|%v|%v|%v|%v|%v|%v|%v|%v|%v|%v|%v\n", id, rule, seq, trType, amount, date, dateCivil, due, dueCivil, contact, status, invoice, exception)
	}
	if err := txs.Close(); err != nil {
		t.Fatalf("snapshot tx close: %v", err)
	}
	return out.String()
}
