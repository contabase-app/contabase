package main

import (
	"net/url"
	"strings"
	"testing"

	"github.com/google/uuid"
)

// F1 HTTP contract: both funding modes are visible and explicit in the
// interface; "record" means money already in the investment account — no
// transfer, no balance movement, no receipt, including on funded lots.

func f1RecordCreationForm(name, principal string) url.Values {
	form := investimentosRouteForm()
	form.Set("name", name)
	form.Set("principal_amount", principal)
	form.Set("funding_mode", "record")
	return form
}

func f1RecordPurchaseForm(principal string) url.Values {
	form := investmentI21Form()
	form.Set("principal_amount", principal)
	form.Set("funding_mode", "record")
	return form
}

func f1Count(t *testing.T, h *investimentosRoutesHarness, query string) int {
	t.Helper()
	var count int
	if err := h.db.QueryRow(query).Scan(&count); err != nil {
		t.Fatal(err)
	}
	return count
}

func TestF1CreationFormShowsBothModesAndRecordOnlyCreatesWithoutTransfer(t *testing.T) {
	h := b7GateHarness(t)
	body := h.request(t, "GET", "/investimentos", h.admin, nil, false, false).Body.String()
	for _, want := range []string{
		`value="transfer" data-investment-funding-mode checked`,
		`value="record" data-investment-funding-mode`,
		"Transferir agora",
		"Dinheiro já está na conta de investimento",
		"Sem transferência automática",
		`name="source_account_id" class="cb-input" required`,
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("creation form missing %q", want)
		}
	}
	if strings.Contains(body, `<div data-investment-funding-source hidden>`) {
		t.Fatal("fresh form hides the source account in transfer mode")
	}

	requireInvestimentosStatus(t, h.request(t, "POST", "/investimentos", h.admin, f1RecordCreationForm("F1 registro direto", "10.000,00"), true, false), 303)
	if got := f1Count(t, h, `SELECT COUNT(*) FROM transactions`); got != 0 {
		t.Fatalf("record-only creation wrote %d ledger rows", got)
	}
	if b7GateBalance(t, h, "inv-a") != 0 || b7GateBalance(t, h, "checking-a") != 200000000 {
		t.Fatal("record-only creation moved balances")
	}
	var id string
	if err := h.db.QueryRow(`SELECT id FROM investments WHERE workspace_id='workspace-rbac' AND name='F1 registro direto'`).Scan(&id); err != nil {
		t.Fatal(err)
	}
	detail := h.request(t, "GET", "/investimentos/"+id, h.admin, nil, false, false).Body.String()
	for _, want := range []string{"Sem movimentação automática", "R$ 10.000,00"} {
		if !strings.Contains(detail, want) {
			t.Fatalf("record-only detail missing %q", want)
		}
	}
	for _, unwanted := range []string{"Transferência automática", "Consultar lançamento"} {
		if strings.Contains(detail, unwanted) {
			t.Fatalf("record-only detail must not reference a transfer: %q", unwanted)
		}
	}
}

func TestF1FundedLotRecordOnlyReapplicationDistinguishesHistory(t *testing.T) {
	h := b7GateHarness(t)
	id := b7GateCreateFunded(t, h, "F1 financiado", "1.000,00")
	detail := h.request(t, "GET", "/investimentos/"+id, h.admin, nil, false, false).Body.String()
	if !strings.Contains(detail, "Transferência automática") || strings.Count(detail, "Consultar lançamento") != 1 {
		t.Fatal("funded initial purchase lost its automatic transfer nature")
	}

	form := f1RecordPurchaseForm("2.000,00")
	requireInvestimentosStatus(t, h.request(t, "POST", "/investimentos/"+id+"/compras", h.admin, form, true, false), 303)
	if got := f1Count(t, h, `SELECT COUNT(*) FROM transactions`); got != 1 {
		t.Fatalf("record-only reapplication created %d extra ledger rows", got-1)
	}
	if b7GateBalance(t, h, "inv-a") != 100000 || b7GateBalance(t, h, "checking-a") != 200000000-100000 {
		t.Fatal("record-only reapplication moved balances")
	}
	detail = h.request(t, "GET", "/investimentos/"+id+"?purchased=1", h.admin, nil, false, false).Body.String()
	for _, want := range []string{"Reaplicação registrada com sucesso.", "Transferência automática", "Sem movimentação automática", "3.000,00", "2 compras"} {
		if !strings.Contains(detail, want) {
			t.Fatalf("mixed history missing %q", want)
		}
	}
	if got := strings.Count(detail, "Consultar lançamento"); got != 1 {
		t.Fatalf("transfer links=%d want=1 (only the funded purchase)", got)
	}

	// No-JS submission carrying a source account in record mode is translated
	// to the record-only representation; a source == destination transfer is
	// never built.
	translated := f1RecordPurchaseForm("500,00")
	translated.Set("source_account_id", "checking-a")
	requireInvestimentosStatus(t, h.request(t, "POST", "/investimentos/"+id+"/compras", h.admin, translated, true, false), 303)
	if got := f1Count(t, h, `SELECT COUNT(*) FROM transactions`); got != 1 {
		t.Fatalf("translated record submission created %d ledger rows", got)
	}

	// Transfer mode still requires the origin; the native error keeps the
	// submitted mode and reopens the drawer with the source select visible.
	missing := b7GatePurchaseForm("100,00")
	missing.Del("source_account_id")
	rr := h.request(t, "POST", "/investimentos/"+id+"/compras", h.admin, missing, true, false)
	requireInvestimentosStatus(t, rr, 400)
	for _, want := range []string{"data-investment-reopen", `value="transfer" data-investment-funding-mode checked`} {
		if !strings.Contains(rr.Body.String(), want) {
			t.Fatalf("transfer-mode error re-render missing %q", want)
		}
	}
	if strings.Contains(rr.Body.String(), `<div data-investment-funding-source hidden>`) {
		t.Fatal("transfer-mode error hid the source select")
	}

	// A validation error in record mode re-renders with the record choice
	// preserved and the origin select hidden.
	invalid := f1RecordPurchaseForm("0")
	rr = h.request(t, "POST", "/investimentos/"+id+"/compras", h.admin, invalid, true, false)
	requireInvestimentosStatus(t, rr, 400)
	for _, want := range []string{"data-investment-reopen", `value="record" data-investment-funding-mode checked`, `<div data-investment-funding-source hidden>`} {
		if !strings.Contains(rr.Body.String(), want) {
			t.Fatalf("record-mode error re-render missing %q", want)
		}
	}
}

func TestF1RecordOnlyHTMXAndReplayDoNotDuplicate(t *testing.T) {
	h := b7GateHarness(t)
	id := b7GateCreateFunded(t, h, "F1 HTMX", "1.000,00")
	form := f1RecordPurchaseForm("2.000,00")
	rr := h.request(t, "POST", "/investimentos/"+id+"/compras", h.admin, form, true, true)
	requireInvestimentosStatus(t, rr, 200)
	if rr.Header().Get("HX-Redirect") != "/investimentos/"+id+"?purchased=1" {
		t.Fatal("HTMX record-only purchase lacks feedback redirect")
	}
	requireInvestimentosStatus(t, h.request(t, "POST", "/investimentos/"+id+"/compras", h.admin, form, true, false), 303)
	if got := f1Count(t, h, `SELECT COUNT(*) FROM investment_purchases WHERE workspace_id='workspace-rbac' AND investment_id='`+id+`'`); got != 2 {
		t.Fatalf("purchases after replay=%d want=2", got)
	}
	if got := f1Count(t, h, `SELECT COUNT(*) FROM transactions`); got != 1 {
		t.Fatalf("ledger rows=%d want=1", got)
	}
	conflict := f1RecordPurchaseForm("2.000,01")
	conflict.Set("request_id", form.Get("request_id"))
	requireInvestimentosStatus(t, h.request(t, "POST", "/investimentos/"+id+"/compras", h.admin, conflict, true, false), 409)
}

func TestF1RecordCreationReplayKeepsNoLedgerState(t *testing.T) {
	h := b7GateHarness(t)
	form := f1RecordCreationForm("F1 replay criação", "3.000,00")
	requireInvestimentosStatus(t, h.request(t, "POST", "/investimentos", h.admin, form, true, false), 303)
	requireInvestimentosStatus(t, h.request(t, "POST", "/investimentos", h.admin, form, true, false), 303)
	if got := f1Count(t, h, `SELECT COUNT(*) FROM investments WHERE workspace_id='workspace-rbac' AND name='F1 replay criação'`); got != 1 {
		t.Fatalf("record-only creation replay duplicated lots=%d", got)
	}
	if got := f1Count(t, h, `SELECT COUNT(*) FROM transactions`); got != 0 {
		t.Fatalf("ledger rows=%d want=0", got)
	}
	changed := f1RecordCreationForm("F1 replay criação", "3.000,01")
	changed.Set("request_id", form.Get("request_id"))
	requireInvestimentosStatus(t, h.request(t, "POST", "/investimentos", h.admin, changed, true, false), 409)
	if _, err := uuid.Parse(form.Get("request_id")); err != nil {
		t.Fatalf("form request_id invalid: %v", err)
	}
}
