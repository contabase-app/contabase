package handlers

import (
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"
)

func newContabilCSVBusinessHandler(t *testing.T) (*RelatoriosHandler, contabilSmokeExpectations) {
	t.Helper()
	db := openTestDB(t)
	t.Cleanup(func() { db.Close() })
	want := seedRelatoriosContabilSmokeBusinessJuly2026(t, db)
	return &RelatoriosHandler{DB: db, WorkspaceID: "ws-contabil-smoke", UserID: "u-contabil-smoke"}, want
}

func newContabilCSVPersonalHandler(t *testing.T) *RelatoriosHandler {
	t.Helper()
	db := openTestDB(t)
	t.Cleanup(func() { db.Close() })
	now := time.Now().Unix()
	execTestSQL(t, db, `INSERT INTO users (id, name, email, password_hash, created_at, updated_at) VALUES ('u-ccsv-pers', 'Pers', 'ccsvpers@t.com', 'hash', ?, ?)`, now, now)
	execTestSQL(t, db, `INSERT INTO workspaces (id, name, type, created_at, updated_at) VALUES ('ws-ccsv-pers', 'WS Pers CSV', 'personal', ?, ?)`, now, now)
	execTestSQL(t, db, `INSERT INTO workspace_members (workspace_id, user_id, role, joined_at) VALUES ('ws-ccsv-pers', 'u-ccsv-pers', 'ADMIN', ?)`, now)
	return &RelatoriosHandler{DB: db, WorkspaceID: "ws-ccsv-pers", UserID: "u-ccsv-pers"}
}

func TestHandleExportarContabilResumoCSV_Business(t *testing.T) {
	handler, want := newContabilCSVBusinessHandler(t)

	req := httptest.NewRequest(http.MethodGet, "/relatorios/contabil-resumo.csv?month=2026-07", nil)
	rr := httptest.NewRecorder()
	handler.HandleExportarContabilResumoCSV(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200, body = %q", rr.Code, rr.Body.String())
	}
	if got := rr.Header().Get("Content-Type"); got != "text/csv; charset=utf-8" {
		t.Fatalf("Content-Type = %q", got)
	}
	if got := rr.Header().Get("X-Content-Type-Options"); got != "nosniff" {
		t.Fatalf("X-Content-Type-Options = %q, want nosniff", got)
	}
	if got := rr.Header().Get("Content-Disposition"); !strings.Contains(got, `filename="relatorio-contabil-resumo-2026-07.csv"`) {
		t.Fatalf("Content-Disposition = %q", got)
	}

	body := rr.Body.String()
	header := "periodo,workspace,tipo,resultado_operacional_centavos,caixa_realizado_centavos,movimentos_confirmados,retiradas_distribuicao_centavos,investimentos_patrimonial_centavos,saldo_total_reconstruido_centavos,saldo_referencia"
	if !strings.Contains(body, header) {
		t.Fatalf("resumo header missing, body:\n%s", body)
	}
	for _, want := range []string{
		"2026-07",
		"business",
		itoa64(want.OperationalResult),
		itoa64(want.CashResult),
		itoa64(want.Withdrawals),
		itoa64(want.PatrimonialInvestments),
		itoa64(want.TotalClosingBalance),
		"31/07/2026",
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("resumo CSV missing %q, body:\n%s", want, body)
		}
	}
}

func TestHandleExportarContabilResumoCSV_PersonalForbidden(t *testing.T) {
	handler := newContabilCSVPersonalHandler(t)
	req := httptest.NewRequest(http.MethodGet, "/relatorios/contabil-resumo.csv?mes=8&ano=2026", nil)
	rr := httptest.NewRecorder()
	handler.HandleExportarContabilResumoCSV(rr, req)
	if rr.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", rr.Code)
	}
}

func TestHandleExportarContabilMacrosCSV_Business(t *testing.T) {
	handler, _ := newContabilCSVBusinessHandler(t)

	req := httptest.NewRequest(http.MethodGet, "/relatorios/contabil-macros.csv?month=2026-07", nil)
	rr := httptest.NewRecorder()
	handler.HandleExportarContabilMacrosCSV(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200, body = %q", rr.Code, rr.Body.String())
	}
	if got := rr.Header().Get("Content-Type"); got != "text/csv; charset=utf-8" {
		t.Fatalf("Content-Type = %q", got)
	}
	if got := rr.Header().Get("X-Content-Type-Options"); got != "nosniff" {
		t.Fatalf("X-Content-Type-Options = %q, want nosniff", got)
	}
	if got := rr.Header().Get("Content-Disposition"); !strings.Contains(got, `filename="relatorio-contabil-macros-2026-07.csv"`) {
		t.Fatalf("Content-Disposition = %q", got)
	}

	body := rr.Body.String()
	header := "periodo,base,macro_group,natureza,total_centavos,percentual,observacao"
	if !strings.Contains(body, header) {
		t.Fatalf("macros header missing, body:\n%s", body)
	}
	for _, want := range []string{
		"receitas,Receitas Financeiras,",
		"retiradas_distribuicao,Retiradas/Distribuição,",
		"investimentos_patrimonial,Investimentos/Patrimonial,",
		"despesas,Investimentos/Outros,",
		"alertas,Investimentos/Outros,",
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("macros CSV missing %q, body:\n%s", want, body)
		}
	}
	if !strings.Contains(body, itoa64(60000)) {
		t.Fatalf("macros CSV missing retiradas amount, body:\n%s", body)
	}
	if !strings.Contains(body, itoa64(110000)) {
		t.Fatalf("macros CSV missing patrimonial amount, body:\n%s", body)
	}
}

func TestHandleExportarContabilMacrosCSV_PersonalForbidden(t *testing.T) {
	handler := newContabilCSVPersonalHandler(t)
	req := httptest.NewRequest(http.MethodGet, "/relatorios/contabil-macros.csv?mes=8&ano=2026", nil)
	rr := httptest.NewRecorder()
	handler.HandleExportarContabilMacrosCSV(rr, req)
	if rr.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", rr.Code)
	}
}

func TestHandleExportarContabilSaldosCSV_Business(t *testing.T) {
	handler, want := newContabilCSVBusinessHandler(t)

	req := httptest.NewRequest(http.MethodGet, "/relatorios/contabil-saldos.csv?month=2026-07", nil)
	rr := httptest.NewRecorder()
	handler.HandleExportarContabilSaldosCSV(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200, body = %q", rr.Code, rr.Body.String())
	}
	if got := rr.Header().Get("Content-Type"); got != "text/csv; charset=utf-8" {
		t.Fatalf("Content-Type = %q", got)
	}
	if got := rr.Header().Get("X-Content-Type-Options"); got != "nosniff" {
		t.Fatalf("X-Content-Type-Options = %q, want nosniff", got)
	}
	if got := rr.Header().Get("Content-Disposition"); !strings.Contains(got, `filename="relatorio-contabil-saldos-2026-07.csv"`) {
		t.Fatalf("Content-Disposition = %q", got)
	}

	body := rr.Body.String()
	header := "periodo,referencia,conta,tipo_conta,saldo_reconstruido_centavos,arquivada"
	if !strings.Contains(body, header) {
		t.Fatalf("saldos header missing, body:\n%s", body)
	}
	for _, want := range []string{
		"31/07/2026",
		"Conta Movimento Smoke",
		"Reserva Smoke",
		itoa64(want.MainClosingBalance),
		itoa64(want.ReserveClosingBalance),
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("saldos CSV missing %q, body:\n%s", want, body)
		}
	}
	totalRow := "TOTAL,TOTAL," + itoa64(want.TotalClosingBalance) + ",false"
	if !strings.Contains(body, totalRow) {
		t.Fatalf("saldos CSV missing TOTAL row %q, body:\n%s", totalRow, body)
	}
}

func TestHandleExportarContabilSaldosCSV_PersonalForbidden(t *testing.T) {
	handler := newContabilCSVPersonalHandler(t)
	req := httptest.NewRequest(http.MethodGet, "/relatorios/contabil-saldos.csv?mes=8&ano=2026", nil)
	rr := httptest.NewRecorder()
	handler.HandleExportarContabilSaldosCSV(rr, req)
	if rr.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", rr.Code)
	}
}

func TestHandleExportarContabilCSV_SanitizaInjecao(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()

	now := time.Now().Unix()
	execTestSQL(t, db, `INSERT INTO users (id, name, email, password_hash, created_at, updated_at) VALUES ('u-inj', 'Inj', 'inj@t.com', 'hash', ?, ?)`, now, now)
	execTestSQL(t, db, `INSERT INTO workspaces (id, name, type, company_name, created_at, updated_at) VALUES ('ws-inj', 'WS Inj', 'business', 'Inj Ltda', ?, ?)`, now, now)
	execTestSQL(t, db, `INSERT INTO workspace_members (workspace_id, user_id, role, joined_at) VALUES ('ws-inj', 'u-inj', 'ADMIN', ?)`, now)
	execTestSQL(t, db, `INSERT INTO accounts (id, workspace_id, name, type, initial_balance, current_balance, created_at, updated_at) VALUES ('acc-inj', 'ws-inj', '=SUM(A1:A2)', 'CHECKING', 1000, 1000, ?, ?)`, now, now)

	handler := &RelatoriosHandler{DB: db, WorkspaceID: "ws-inj", UserID: "u-inj"}
	req := httptest.NewRequest(http.MethodGet, "/relatorios/contabil-saldos.csv?mes=8&ano=2026", nil)
	rr := httptest.NewRecorder()
	handler.HandleExportarContabilSaldosCSV(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200, body = %q", rr.Code, rr.Body.String())
	}
	body := rr.Body.String()
	if !strings.Contains(body, "'=SUM(A1:A2)") {
		t.Fatalf("expected sanitized formula field, body:\n%s", body)
	}
}

func TestHandleExportarContabilCSV_WorkspaceIsolation(t *testing.T) {
	handler, _ := newContabilCSVBusinessHandler(t)
	handler.WorkspaceID = "ws-nonexistent"

	req := httptest.NewRequest(http.MethodGet, "/relatorios/contabil-saldos.csv?month=2026-07", nil)
	rr := httptest.NewRecorder()
	handler.HandleExportarContabilSaldosCSV(rr, req)

	// Non-business (empty) workspace should be forbidden, and must not leak other workspace data.
	if rr.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403 for isolated workspace", rr.Code)
	}
	if strings.Contains(rr.Body.String(), "Conta Movimento Smoke") {
		t.Fatal("workspace isolation leak: other workspace account exposed")
	}
}

func itoa64(v int64) string {
	return strconv.FormatInt(v, 10)
}
