package main

import (
	"net/url"
	"strings"
	"testing"

	"github.com/contabase-app/contabase/internal/services"
)

// B7 investment gate: inclusion, metadata edition, purchase replay, lifecycle
// refusal and schema-79 receipt guards. Every database is disposable; no
// production financial code is exercised differently from the HTTP contract.
func b7GateHarness(t *testing.T) *investimentosRoutesHarness {
	t.Helper()
	h := newInvestimentosI22Harness(t)
	h.exec(t, `UPDATE accounts SET initial_balance=200000000,current_balance=200000000
		WHERE workspace_id='workspace-rbac' AND id='checking-a'`)
	return h
}

func b7GateCreationForm(name, principal string) url.Values {
	form := investimentosRouteForm()
	form.Set("name", name)
	form.Set("principal_amount", principal)
	form.Set("source_account_id", "checking-a")
	form.Set("funding_mode", "transfer")
	return form
}

func b7GateCreateFunded(t *testing.T, h *investimentosRoutesHarness, name, principal string) string {
	t.Helper()
	rr := h.request(t, "POST", "/investimentos", h.admin, b7GateCreationForm(name, principal), true, false)
	requireInvestimentosStatus(t, rr, 303)
	var id string
	if err := h.db.QueryRow(`SELECT id FROM investments WHERE workspace_id='workspace-rbac' AND name=?`, name).Scan(&id); err != nil {
		t.Fatal(err)
	}
	return id
}

func b7GatePurchaseForm(principal string) url.Values {
	form := investmentI21Form()
	form.Set("principal_amount", principal)
	form.Set("source_account_id", "checking-a")
	form.Set("funding_mode", "transfer")
	return form
}

func b7GateBalance(t *testing.T, h *investimentosRoutesHarness, account string) int64 {
	t.Helper()
	var balance int64
	if err := h.db.QueryRow(`SELECT current_balance FROM accounts WHERE workspace_id='workspace-rbac' AND id=?`, account).Scan(&balance); err != nil {
		t.Fatal(err)
	}
	return balance
}

func b7GateCancellationRows(t *testing.T, h *investimentosRoutesHarness) int {
	t.Helper()
	var count int
	if err := h.db.QueryRow(`SELECT COUNT(*) FROM investment_cancellations`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	return count
}

func TestB7GateInclusionTransferCentsAndHTMX(t *testing.T) {
	h := b7GateHarness(t)
	const principal int64 = 123456789
	beforeChecking := b7GateBalance(t, h, "checking-a")
	if beforeChecking != 200000000 {
		t.Fatalf("fixture balance=%d", beforeChecking)
	}
	id := b7GateCreateFunded(t, h, "Gate inclusão", "1.234.567,89")

	afterChecking := b7GateBalance(t, h, "checking-a")
	afterInvestment := b7GateBalance(t, h, "inv-a")
	if afterChecking != beforeChecking-principal || afterInvestment != principal {
		t.Fatalf("balances checking=%d investment=%d want=%d/%d", afterChecking, afterInvestment, beforeChecking-principal, principal)
	}
	var amount int64
	var source, destination, kind, status string
	if err := h.db.QueryRow(`SELECT amount, account_id, destination_account_id, type, status FROM transactions
		WHERE workspace_id='workspace-rbac' AND id=?`, services.InvestmentPurchaseTransferPrefix+"initial:"+id).
		Scan(&amount, &source, &destination, &kind, &status); err != nil {
		t.Fatal(err)
	}
	if amount != principal || source != "checking-a" || destination != "inv-a" || kind != "TRANSFER" || status != "paid" {
		t.Fatalf("transfer amount=%d source=%s destination=%s type=%s status=%s", amount, source, destination, kind, status)
	}
	var stored int64
	if err := h.db.QueryRow(`SELECT SUM(principal_cents) FROM investment_purchases
		WHERE workspace_id='workspace-rbac' AND investment_id=?`, id).Scan(&stored); err != nil {
		t.Fatal(err)
	}
	if stored != principal {
		t.Fatalf("stored principal=%d want=%d", stored, principal)
	}

	body := h.request(t, "GET", "/investimentos/"+id, h.admin, nil, false, false).Body.String()
	for _, want := range []string{"R$ 1.234.567,89", "1 compra"} {
		if !strings.Contains(body, want) {
			t.Fatalf("rendered detail missing %q", want)
		}
	}
	if got := b7GateCancellationRows(t, h); got != 0 {
		t.Fatalf("unexpected cancellation receipts: %d", got)
	}
	t.Logf("INCLUSION before_checking=%d after_checking=%d after_investment=%d transfer=%d delta_checking=%d delta_investment=%d",
		beforeChecking, afterChecking, afterInvestment, amount, afterChecking-beforeChecking, afterInvestment)
}

func TestB7GateMetadataEditionKeepsBalancesAndPurchaseHistory(t *testing.T) {
	h := b7GateHarness(t)
	id := b7GateCreateFunded(t, h, "Gate metadados", "1.000,00")
	path := "/investimentos/" + id + "/compras"
	requireInvestimentosStatus(t, h.request(t, "POST", path, h.admin, b7GatePurchaseForm("2.000,00"), true, false), 303)

	financial := o1aSnapshot(t, h, "accounts", "transactions", "investment_purchases")
	var purchaseTotal int64
	var purchaseCount int
	if err := h.db.QueryRow(`SELECT COALESCE(SUM(principal_cents),0), COUNT(*) FROM investment_purchases
		WHERE workspace_id='workspace-rbac' AND investment_id=?`, id).Scan(&purchaseTotal, &purchaseCount); err != nil {
		t.Fatal(err)
	}
	if purchaseTotal != 300000 || purchaseCount != 2 {
		t.Fatalf("history total=%d count=%d", purchaseTotal, purchaseCount)
	}

	edit := url.Values{"name": {"Gate metadados editado"}, "institution": {"Banco Editado"}, "kind": {"LCI"},
		"tax_regime": {"MANUAL"}, "yield_note": {"95% CDI"}, "maturity_date": {"2030-05-01"}}
	requireInvestimentosStatus(t, h.request(t, "POST", "/investimentos/"+id+"/editar", h.admin, edit, true, false), 303)

	if o1aSnapshot(t, h, "accounts", "transactions", "investment_purchases") != financial {
		t.Fatal("metadata edition changed balances, journal or purchase history")
	}
	var name, institution, taxRegime, status string
	var principal int64
	if err := h.db.QueryRow(`SELECT name, institution, tax_regime, status, principal_cents FROM investments
		WHERE workspace_id='workspace-rbac' AND id=?`, id).Scan(&name, &institution, &taxRegime, &status, &principal); err != nil {
		t.Fatal(err)
	}
	if name != "Gate metadados editado" || institution != "Banco Editado" || taxRegime != "MANUAL" || status != "ATIVA" || principal != 100000 {
		t.Fatalf("metadata name=%s institution=%s regime=%s status=%s principal=%d", name, institution, taxRegime, status, principal)
	}

	body := h.request(t, "GET", "/investimentos/"+id, h.admin, nil, false, false).Body.String()
	for _, want := range []string{"Gate metadados editado", "R$ 3.000,00", "2 compras"} {
		if !strings.Contains(body, want) {
			t.Fatalf("rendered detail missing %q", want)
		}
	}
	if got := b7GateCancellationRows(t, h); got != 0 {
		t.Fatalf("unexpected cancellation receipts: %d", got)
	}
	t.Logf("EDITION purchases=%d total_cents=%d principal_unchanged=%d journal_unchanged=true", purchaseCount, purchaseTotal, principal)
}

func TestB7GatePurchaseReplayConflictAndLaterOperationRefusals(t *testing.T) {
	h := b7GateHarness(t)
	id := b7GateCreateFunded(t, h, "Gate replay", "1.000,00")
	path := "/investimentos/" + id + "/compras"
	form := b7GatePurchaseForm("2.000,00")
	requireInvestimentosStatus(t, h.request(t, "POST", path, h.admin, form, true, false), 303)
	journal := o1aSnapshot(t, h, "accounts", "transactions", "investment_purchases")

	requireInvestimentosStatus(t, h.request(t, "POST", path, h.admin, form, true, false), 303)
	if o1aSnapshot(t, h, "accounts", "transactions", "investment_purchases") != journal {
		t.Fatal("idempotent replay duplicated the transfer")
	}
	conflict := b7GatePurchaseForm("2.000,01")
	conflict.Set("request_id", form.Get("request_id"))
	requireInvestimentosStatus(t, h.request(t, "POST", path, h.admin, conflict, true, false), 409)
	if o1aSnapshot(t, h, "accounts", "transactions", "investment_purchases") != journal {
		t.Fatal("refused purchases changed the journal")
	}
	// F1: an explicit record-only purchase on a funded lot registers history
	// without moving money — no new transfer, no balance change, no receipt.
	funded := b7GateBalance(t, h, "inv-a")
	beforeRecordless := o1aSnapshot(t, h, "accounts", "transactions")
	recordless := b7GatePurchaseForm("2.000,00")
	recordless.Del("source_account_id")
	recordless.Set("funding_mode", "record")
	requireInvestimentosStatus(t, h.request(t, "POST", path, h.admin, recordless, true, false), 303)
	if o1aSnapshot(t, h, "accounts", "transactions") != beforeRecordless {
		t.Fatal("record-only purchase touched accounts or ledger")
	}
	if b7GateBalance(t, h, "inv-a") != funded || b7GateBalance(t, h, "checking-a") != 200000000-300000 {
		t.Fatal("record-only purchase changed balances")
	}
	var recordlessReceipts int
	if err := h.db.QueryRow(`SELECT COUNT(*) FROM investment_purchases p
		WHERE p.workspace_id='workspace-rbac' AND p.investment_id=? AND p.is_initial=0
		AND NOT EXISTS(SELECT 1 FROM transactions t WHERE t.workspace_id=p.workspace_id AND t.id=?||p.id)`,
		id, services.InvestmentPurchaseTransferPrefix).Scan(&recordlessReceipts); err != nil || recordlessReceipts != 1 {
		t.Fatalf("record-only purchases without receipt=%d err=%v", recordlessReceipts, err)
	}
	afterRecordless := o1aSnapshot(t, h, "accounts", "transactions", "investment_purchases")
	requireInvestimentosStatus(t, h.request(t, "POST", path, h.admin, recordless, true, false), 303)
	if o1aSnapshot(t, h, "accounts", "transactions", "investment_purchases") != afterRecordless {
		t.Fatal("record-only replay duplicated state")
	}
	var purchases int
	if err := h.db.QueryRow(`SELECT COUNT(*) FROM investment_purchases WHERE workspace_id='workspace-rbac' AND investment_id=?`, id).Scan(&purchases); err != nil || purchases != 3 {
		t.Fatalf("purchases=%d want=3 err=%v", purchases, err)
	}

	// A closed record-only lot refuses new purchases and new metadata editions
	// before any write happens.
	closed := h.create(t, "Gate ciclo encerrado")
	purchase := investmentI21Form()
	requireInvestimentosStatus(t, h.request(t, "POST", "/investimentos/"+closed+"/compras", h.admin, purchase, true, false), 303)
	redeem := url.Values{"redeemed_date": {"2026-04-01"}, "final_amount": {"1.100,00"}}
	requireInvestimentosStatus(t, h.request(t, "POST", "/investimentos/"+closed+"/resgatar", h.admin, redeem, true, false), 303)
	closedJournal := o1aSnapshot(t, h, "accounts", "transactions", "investment_purchases", "investment_valuations")
	// A committed request retried after closing is a safe replay, not a new
	// operation; only a fresh request identity must be refused.
	requireInvestimentosStatus(t, h.request(t, "POST", "/investimentos/"+closed+"/compras", h.admin, purchase, true, false), 303)
	if o1aSnapshot(t, h, "accounts", "transactions", "investment_purchases", "investment_valuations") != closedJournal {
		t.Fatal("replay after closing wrote new state")
	}
	requireInvestimentosStatus(t, h.request(t, "POST", "/investimentos/"+closed+"/compras", h.admin, investmentI21Form(), true, false), 409)
	requireInvestimentosStatus(t, h.request(t, "POST", "/investimentos/"+closed+"/editar", h.admin, url.Values{"name": {"x"}, "institution": {"x"}, "kind": {"CDB"}, "tax_regime": {"MANUAL"}}, true, false), 409)
	if o1aSnapshot(t, h, "accounts", "transactions", "investment_purchases", "investment_valuations") != closedJournal {
		t.Fatal("refused operations after closing changed persisted state")
	}
	if got := b7GateCancellationRows(t, h); got != 0 {
		t.Fatalf("unexpected cancellation receipts: %d", got)
	}
	t.Logf("REPLAY transfer_once=true conflict=409 funded_record_only=303_no_transfer record_only_replay=true closed_purchase=409 closed_edit=409 journal_unchanged=true")
}

func TestB7GateWorkspaceAndPersonalBusinessIntegrity(t *testing.T) {
	h := b7GateHarness(t)
	h.exec(t, `UPDATE workspaces SET type='business' WHERE id='workspace-rbac'`)
	form := b7GateCreationForm("Gate business", "5.000,00")
	form.Set("workspace_id", "workspace-b")
	requireInvestimentosStatus(t, h.request(t, "POST", "/investimentos", h.admin, form, true, false), 303)
	var id, workspace string
	if err := h.db.QueryRow(`SELECT id, workspace_id FROM investments WHERE name='Gate business'`).Scan(&id, &workspace); err != nil {
		t.Fatal(err)
	}
	if workspace != "workspace-rbac" {
		t.Fatalf("payload chose workspace %s", workspace)
	}
	var workspaceType string
	if err := h.db.QueryRow(`SELECT type FROM workspaces WHERE id='workspace-rbac'`).Scan(&workspaceType); err != nil || workspaceType != "business" {
		t.Fatalf("workspace type=%s err=%v", workspaceType, err)
	}
	foreign := h.session(t, "admin-user", "workspace-b")
	requireInvestimentosStatus(t, h.request(t, "GET", "/investimentos/"+id, foreign, nil, false, false), 404)
	requireInvestimentosStatus(t, h.request(t, "POST", "/investimentos/"+id+"/compras", foreign, b7GatePurchaseForm("1,00"), true, false), 404)
	var foreignRows int
	if err := h.db.QueryRow(`SELECT COUNT(*) FROM transactions WHERE workspace_id='workspace-b'`).Scan(&foreignRows); err != nil || foreignRows != 0 {
		t.Fatalf("foreign workspace rows=%d err=%v", foreignRows, err)
	}
	if b7GateBalance(t, h, "inv-a") != 500000 {
		t.Fatalf("business workspace balance=%d", b7GateBalance(t, h, "inv-a"))
	}
	t.Logf("INTEGRITY workspace=%s type=%s foreign_read=404 foreign_write=404 foreign_rows=%d balance=%d",
		workspace, workspaceType, foreignRows, b7GateBalance(t, h, "inv-a"))
}

func TestB7GateCancellationReceiptRemainsUntouched(t *testing.T) {
	h := b7GateHarness(t)
	id := b7GateCreateFunded(t, h, "Gate recibo", "1.000,00")
	requireInvestimentosStatus(t, h.request(t, "POST", "/investimentos/"+id+"/compras", h.admin, b7GatePurchaseForm("500,00"), true, false), 303)

	var columns, triggers int
	if err := h.db.QueryRow(`SELECT COUNT(*) FROM pragma_table_info('investment_cancellations')`).Scan(&columns); err != nil {
		t.Fatal(err)
	}
	if err := h.db.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type='trigger' AND tbl_name='investment_cancellations'`).Scan(&triggers); err != nil {
		t.Fatal(err)
	}
	if columns == 0 || triggers == 0 {
		t.Fatalf("cancellation schema missing columns=%d triggers=%d", columns, triggers)
	}
	if got := b7GateCancellationRows(t, h); got != 0 {
		t.Fatalf("cancellation receipt written without service: %d", got)
	}
	t.Logf("CANCELLATION_SCHEMA columns=%d triggers=%d receipts=0 service=not_implemented", columns, triggers)
}
