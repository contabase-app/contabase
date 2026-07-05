package handlers

import (
	"database/sql"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestSanitizeCSVField(t *testing.T) {
	cases := []struct {
		name     string
		input    string
		expected string
	}{
		{"texto normal", "Compra de mouse", "Compra de mouse"},
		{"texto vazio", "", ""},
		{"sinal de igual", "=SUM(A1:A10)", "'=SUM(A1:A10)"},
		{"sinal de mais", "+1+1", "'+1+1"},
		{"sinal de menos", "-1-1", "'-1-1"},
		{"arroba", "@SUM", "'@SUM"},
		{"tab antes da formula", "\t=SUM(A1)", "'\t=SUM(A1)"},
		{"espaco antes da formula", "  =cmd|' /C calc'!A0", "'  =cmd|' /C calc'!A0"},
		{"CR antes", "\r+1", "'\r+1"},
		{"LF antes", "\n-1", "'\n-1"},
		{"espacos normais", "  texto normal", "  texto normal"},
		{"numeros e hifens soltos", "123-456", "123-456"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := sanitizeCSVField(tc.input)
			if got != tc.expected {
				t.Errorf("sanitizeCSVField(%q) = %q; want %q", tc.input, got, tc.expected)
			}
		})
	}
}

func TestHandleExportarCSV_ExtratoMovimentos(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()

	seedReportHelpersScenario(t, db)

	augDate := testUnixDate("2026-08-10")
	execTestSQL(t, db, `
		INSERT INTO transactions (id, workspace_id, user_id, account_id, category_id, type, amount, date, description, status, paid_at, installment_number, total_installments, created_at, updated_at)
		VALUES
			('csv-inc', 'ws-rpt', 'u-rpt', 'chk-rpt', 'cat-salary', 'INCOME', 120000, ?, 'Freela', 'paid', ?, 1, 1, ?, ?),
			('csv-exp', 'ws-rpt', 'u-rpt', 'chk-rpt', 'cat-food', 'EXPENSE', 35000, ?, 'Supermercado', 'paid', ?, 1, 1, ?, ?),
			('csv-pending', 'ws-rpt', 'u-rpt', 'chk-rpt', 'cat-food', 'EXPENSE', 20000, ?, 'Pendente', 'pending', NULL, 1, 1, ?, ?)
	`, augDate, augDate, time.Now().Unix(), time.Now().Unix(), augDate, augDate, time.Now().Unix(), time.Now().Unix(), augDate, time.Now().Unix(), time.Now().Unix())

	h := &RelatoriosHandler{DB: db, WorkspaceID: "ws-rpt"}
	req := httptest.NewRequest("GET", "/relatorios/export.csv?tipo=extrato-movimentos&mes=8&ano=2026", nil)
	rr := httptest.NewRecorder()
	h.HandleExportarCSV(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rr.Code, http.StatusOK)
	}
	if got := rr.Header().Get("Content-Type"); got != "text/csv; charset=utf-8" {
		t.Fatalf("Content-Type = %q, want text/csv; charset=utf-8", got)
	}

	body := rr.Body.String()
	if !strings.Contains(body, "data,descricao,conta,categoria,tipo,entrada_centavos,saida_centavos,valor_centavos") {
		t.Fatalf("missing csv header in body: %s", body)
	}
	if !strings.Contains(body, "Freela") {
		t.Fatalf("expected Freela in CSV. Body: %s", body)
	}
	if !strings.Contains(body, "Supermercado") {
		t.Fatalf("expected Supermercado in CSV. Body: %s", body)
	}
	if strings.Contains(body, "Pendente") {
		t.Fatalf("pending transaction should NOT be exported. Body: %s", body)
	}
	if !strings.Contains(body, "120000") {
		t.Fatalf("expected income cents in CSV. Body: %s", body)
	}
	if !strings.Contains(body, "35000") {
		t.Fatalf("expected expense cents in CSV. Body: %s", body)
	}
}

func TestHandleExportarCSV_ExtratoDiario(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()

	seedReportHelpersScenario(t, db)

	augDate := testUnixDate("2026-08-10")
	execTestSQL(t, db, `
		INSERT INTO transactions (id, workspace_id, user_id, account_id, category_id, type, amount, date, description, status, paid_at, installment_number, total_installments, created_at, updated_at)
		VALUES
			('csv-ds-inc', 'ws-rpt', 'u-rpt', 'chk-rpt', 'cat-salary', 'INCOME', 120000, ?, 'Freela', 'paid', ?, 1, 1, ?, ?),
			('csv-ds-exp', 'ws-rpt', 'u-rpt', 'chk-rpt', 'cat-food', 'EXPENSE', 35000, ?, 'Supermercado', 'paid', ?, 1, 1, ?, ?)
	`, augDate, augDate, time.Now().Unix(), time.Now().Unix(), augDate, augDate, time.Now().Unix(), time.Now().Unix())

	h := &RelatoriosHandler{DB: db, WorkspaceID: "ws-rpt"}
	req := httptest.NewRequest("GET", "/relatorios/export.csv?tipo=extrato-diario&mes=8&ano=2026", nil)
	rr := httptest.NewRecorder()
	h.HandleExportarCSV(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rr.Code, http.StatusOK)
	}

	body := rr.Body.String()
	if !strings.Contains(body, "data,entradas_centavos,saidas_centavos,resultado_centavos,acumulado_centavos") {
		t.Fatalf("missing csv header in body: %s", body)
	}
	if !strings.Contains(body, "120000") {
		t.Fatalf("expected daily income cents in CSV. Body: %s", body)
	}
	if !strings.Contains(body, "35000") {
		t.Fatalf("expected daily expense cents in CSV. Body: %s", body)
	}
	if !strings.Contains(body, "85000") {
		t.Fatalf("expected accumulated cents in CSV. Body: %s", body)
	}
}

func TestHandleExportarCSV_ExtratoMovimentosTransferenciaNeutra(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()

	seedReportHelpersScenario(t, db)

	augDate := testUnixDate("2026-08-10")
	execTestSQL(t, db, `
		INSERT INTO transactions (id, workspace_id, user_id, account_id, destination_account_id, type, amount, date, description, status, paid_at, installment_number, total_installments, created_at, updated_at)
		VALUES ('csv-transfer', 'ws-rpt', 'u-rpt', 'chk-rpt', 'sav-rpt', 'TRANSFER', 50000, ?, 'Transferencia poupanca', 'paid', ?, 1, 1, ?, ?)
	`, augDate, augDate, time.Now().Unix(), time.Now().Unix())

	h := &RelatoriosHandler{DB: db, WorkspaceID: "ws-rpt"}
	req := httptest.NewRequest("GET", "/relatorios/export.csv?tipo=extrato-movimentos&mes=8&ano=2026", nil)
	rr := httptest.NewRecorder()
	h.HandleExportarCSV(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rr.Code, http.StatusOK)
	}

	body := rr.Body.String()
	if !strings.Contains(body, "Transferencia") {
		t.Fatalf("expected transfer row in CSV. Body: %s", body)
	}
	if strings.Contains(body, "50000,50000") {
		t.Fatalf("transfer should not have equal entrada/saida cents. Body: %s", body)
	}
	if !strings.Contains(body, ",0,50000,0") {
		t.Fatalf("expected source row entrada=0/saida=50000/valor=0. Body: %s", body)
	}
	if !strings.Contains(body, ",50000,0,0") {
		t.Fatalf("expected dest row entrada=50000/saida=0/valor=0. Body: %s", body)
	}
}

func TestHandleExportarCSV_ExtratoOutroWorkspaceIsolado(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()

	seedReportHelpersScenario(t, db)
	seedReportHelpersSecondWorkspace(t, db)

	augDate := testUnixDate("2026-08-10")
	execTestSQL(t, db, `
		INSERT INTO transactions (id, workspace_id, user_id, account_id, category_id, type, amount, date, description, status, paid_at, installment_number, total_installments, created_at, updated_at)
		VALUES
			('csv-ws1', 'ws-rpt', 'u-rpt', 'chk-rpt', 'cat-food', 'EXPENSE', 50000, ?, 'Despesa WS1', 'paid', ?, 1, 1, ?, ?),
			('csv-ws2', 'ws-rpt2', 'u-rpt2', 'chk-rpt2', 'cat-other', 'EXPENSE', 99999, ?, 'Despesa WS2', 'paid', ?, 1, 1, ?, ?)
	`, augDate, augDate, time.Now().Unix(), time.Now().Unix(), augDate, augDate, time.Now().Unix(), time.Now().Unix())

	h := &RelatoriosHandler{DB: db, WorkspaceID: "ws-rpt"}
	req := httptest.NewRequest("GET", "/relatorios/export.csv?tipo=extrato-movimentos&mes=8&ano=2026", nil)
	rr := httptest.NewRecorder()
	h.HandleExportarCSV(rr, req)

	body := rr.Body.String()
	if !strings.Contains(body, "Despesa WS1") {
		t.Fatalf("expected WS1 expense in CSV. Body: %s", body)
	}
	if strings.Contains(body, "Despesa WS2") {
		t.Fatalf("WS2 expense should NOT leak into WS1 CSV. Body: %s", body)
	}
}

func TestHandleExportarCSV_ExtratoPeriodoInvalido(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()

	h := &RelatoriosHandler{DB: db, WorkspaceID: "ws-rpt"}
	req := httptest.NewRequest("GET", "/relatorios/export.csv?tipo=extrato-movimentos&mes=99&ano=2026", nil)
	rr := httptest.NewRecorder()
	h.HandleExportarCSV(rr, req)

	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d", rr.Code, http.StatusBadRequest)
	}
}

func TestHandleExportarCSV_SanitizaInjecao(t *testing.T) {
	// Apenas pra testar o fluxo de Pagar/Receber que exporta o CSV
	// e injeta a descrição no CSV
	db := openTestDB(t)
	defer db.Close()

	// Insere transação maliciosa
	_, err := db.Exec(`
		INSERT INTO workspaces (id, name) VALUES ('ws1', 'Workspace 1');
		INSERT INTO users (id, name, email, password_hash) VALUES ('u1', 'User 1', 'u1@example.com', 'hash');
		INSERT INTO workspace_members (workspace_id, user_id, role) VALUES ('ws1', 'u1', 'ADMIN');
		INSERT INTO contacts (id, workspace_id, name, type) VALUES ('c1', 'ws1', '=cmd|C calc', 'client');
		INSERT INTO accounts (id, workspace_id, name, type) VALUES ('a1', 'ws1', '+SUM(A1:A10)', 'CHECKING');
		INSERT INTO transactions (id, workspace_id, user_id, contact_id, account_id, description, amount, type, status, date) 
		VALUES ('t1', 'ws1', 'u1', 'c1', 'a1', '-DANGEROUS', 1000, 'EXPENSE', 'pending', unixepoch());
	`)
	if err != nil {
		t.Fatalf("failed to insert mock data: %v", err)
	}

	h := &RelatoriosHandler{
		DB:          db,
		WorkspaceID: "ws1",
	}

	req := httptest.NewRequest("GET", "/export.csv?tipo=pagar", nil)
	rr := httptest.NewRecorder()

	h.HandleExportarCSV(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rr.Code, http.StatusOK)
	}

	contentType := rr.Header().Get("Content-Type")
	if contentType != "text/csv; charset=utf-8" {
		t.Errorf("content-type = %q", contentType)
	}

	body := rr.Body.String()
	// Header
	if !strings.Contains(body, "id,descricao,valor_centavos,vencimento_unix,conta,contato") {
		t.Errorf("missing csv header in body: %s", body)
	}
	// Dados
	if !strings.Contains(body, "'-DANGEROUS") {
		t.Errorf("expected description to be sanitized. Body: %s", body)
	}
	if !strings.Contains(body, "'+SUM(A1:A10)") {
		t.Errorf("expected account to be sanitized. Body: %s", body)
	}
	if !strings.Contains(body, "'=cmd|C calc") {
		t.Errorf("expected contact to be sanitized. Body: %s", body)
	}
	// O ID não deve ser tocado e o valor não deve ter plic, pois números isolados ou ID q não comecem com +-=@
	if !strings.Contains(body, "t1,") {
		t.Errorf("expected ID to be present as is. Body: %s", body)
	}
}

func seedCaixaCSVScenario(t *testing.T, db *sql.DB) {
	t.Helper()
	now := time.Now().Unix()

	execTestSQL(t, db, `INSERT INTO users (id, name, email, password_hash, created_at, updated_at) VALUES ('u-csv-caixa', 'User CSV Caixa', 'u-csv-caixa@example.com', 'hash', ?, ?)`, now, now)
	execTestSQL(t, db, `INSERT INTO workspaces (id, name, type, company_name, cnpj_cpf, created_at, updated_at) VALUES ('ws-csv-caixa', 'WS CSV Caixa', 'business', 'CSV Empresa', '12.345.678/0001-90', ?, ?)`, now, now)
	execTestSQL(t, db, `INSERT INTO workspaces (id, name, type, created_at, updated_at) VALUES ('ws-csv-other', 'WS CSV Other', 'business', ?, ?)`, now, now)
	execTestSQL(t, db, `INSERT INTO workspace_members (workspace_id, user_id, role, joined_at) VALUES ('ws-csv-caixa', 'u-csv-caixa', 'ADMIN', ?)`, now)
	execTestSQL(t, db, `INSERT INTO accounts (id, workspace_id, name, type, initial_balance, current_balance, created_at, updated_at) VALUES
		('chk-csv-caixa', 'ws-csv-caixa', 'Conta Corrente', 'CHECKING', 100000, 100000, ?, ?),
		('chk-csv-other', 'ws-csv-other', 'Conta Outro', 'CHECKING', 100000, 100000, ?, ?)
	`, now, now, now, now)
	execTestSQL(t, db, `INSERT INTO categories (id, workspace_id, name, icon, color, type, macro_group, created_at) VALUES
		('cat-csv-food', 'ws-csv-caixa', 'Alimentacao', 'utensils', '#f97316', 'EXPENSE', 'Essencial', ?),
		('cat-csv-other', 'ws-csv-other', 'Outra', 'tag', '#22c55e', 'INCOME', 'Receitas Operacionais', ?)
	`, now, now)
}

func TestHandleExportarCSV_CaixaApenasRealizado(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()

	seedCaixaCSVScenario(t, db)

	now := time.Now().Unix()
	agostoDate := testUnixDate("2026-08-05")
	agostoPaid := testUnixDate("2026-08-10")
	setembroPaid := testUnixDate("2026-09-01")

	execTestSQL(t, db, `
		INSERT INTO transactions (id, workspace_id, user_id, account_id, category_id, type, amount, date, due_date, description, status, paid_at, installment_number, total_installments, created_at, updated_at) VALUES
			('tx-csv-paid', 'ws-csv-caixa', 'u-csv-caixa', 'chk-csv-caixa', 'cat-csv-food', 'EXPENSE', 30000, ?, NULL, 'Despesa paga agosto', 'paid', ?, 1, 1, ?, ?),
			('tx-csv-pending', 'ws-csv-caixa', 'u-csv-caixa', 'chk-csv-caixa', 'cat-csv-food', 'EXPENSE', 25000, ?, NULL, 'Despesa pendente agosto', 'pending', NULL, 1, 1, ?, ?),
			('tx-csv-other', 'ws-csv-other', 'u-csv-caixa', 'chk-csv-other', 'cat-csv-other', 'INCOME', 99999, ?, NULL, 'Receita outro WS', 'paid', ?, 1, 1, ?, ?)
	`, agostoDate, agostoPaid, now, now, agostoDate, now, now, agostoDate, setembroPaid, now, now)

	h := &RelatoriosHandler{DB: db, WorkspaceID: "ws-csv-caixa"}
	req := httptest.NewRequest("GET", "/relatorios/export.csv?tipo=caixa&ano=2026", nil)
	rr := httptest.NewRecorder()
	h.HandleExportarCSV(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rr.Code, http.StatusOK)
	}
	if got := rr.Header().Get("Content-Type"); got != "text/csv; charset=utf-8" {
		t.Fatalf("Content-Type = %q, want text/csv; charset=utf-8", got)
	}

	body := rr.Body.String()
	if !strings.Contains(body, "mes,macro_group,total_centavos") {
		t.Fatalf("missing caixa csv header in body: %s", body)
	}
	if !strings.Contains(body, "2026-08,Essencial,-30000") {
		t.Fatalf("expected aggregated August Essential paid row. Body: %s", body)
	}
	if strings.Contains(body, "2026-09") {
		t.Fatalf("September paid_at should not leak into caixa CSV of other workspace. Body: %s", body)
	}
	if strings.Contains(body, "Receita outro WS") || strings.Contains(body, "99999") {
		t.Fatalf("other workspace data leaked into caixa CSV. Body: %s", body)
	}
	if strings.Contains(body, "-25000") {
		t.Fatalf("pending expense should not be in caixa CSV. Body: %s", body)
	}
}

func TestHandleExportarCSV_CaixaPrevisaoIncluiPendentePorVencimento(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()

	seedCaixaCSVScenario(t, db)

	now := time.Now().Unix()
	julhoDate := testUnixDate("2026-07-05")
	agostoDue := testUnixDate("2026-08-10")
	execTestSQL(t, db, `
		INSERT INTO transactions (id, workspace_id, user_id, account_id, category_id, type, amount, date, due_date, description, status, paid_at, installment_number, total_installments, created_at, updated_at) VALUES
			('tx-csv-prev', 'ws-csv-caixa', 'u-csv-caixa', 'chk-csv-caixa', 'cat-csv-food', 'EXPENSE', 25000, ?, ?, 'Despesa prevista agosto', 'pending', NULL, 1, 1, ?, ?)
	`, julhoDate, agostoDue, now, now)

	h := &RelatoriosHandler{DB: db, WorkspaceID: "ws-csv-caixa"}
	req := httptest.NewRequest("GET", "/relatorios/export.csv?tipo=caixa&modo=previsao&mes=8&ano=2026", nil)
	rr := httptest.NewRecorder()
	h.HandleExportarCSV(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rr.Code, http.StatusOK)
	}
	body := rr.Body.String()
	if !strings.Contains(body, "mes,macro_group,total_centavos,saldo_projetado_centavos") {
		t.Fatalf("expected forecast projected balance header. Body: %s", body)
	}
	if !strings.Contains(body, "2026-08,Essencial,-25000,") {
		t.Fatalf("expected pending due-date row in forecast CSV. Body: %s", body)
	}
}
