package handlers

import (
	"database/sql"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestHandleExportarDREPDFBusinessIncluiIdentidadePeriodoEDados(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()

	seedDREPDFBusinessScenario(t, db)

	handler := RelatoriosHandler{ActorRole: testMetasAdminRole, DB: db, WorkspaceID: "ws-pdf-biz", UserID: "u-pdf-biz"}
	req := httptest.NewRequest(http.MethodGet, "/relatorios/dre.pdf?month=2026-08", nil)
	rr := httptest.NewRecorder()

	handler.HandleExportarDREPDF(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d, body = %q", rr.Code, http.StatusOK, rr.Body.String())
	}
	if got := rr.Header().Get("Content-Type"); got != "application/pdf" {
		t.Fatalf("Content-Type = %q, want application/pdf", got)
	}
	if got := rr.Header().Get("Content-Disposition"); !strings.Contains(got, `filename="dre-2026-08.pdf"`) {
		t.Fatalf("Content-Disposition = %q, want dre filename", got)
	}

	body := rr.Body.String()
	for _, want := range []string{
		"%PDF-1.4",
		"DRE Simplificada",
		"Agosto 2026",
		"2026-08",
		"Empresa PDF Ltda",
		"12.345.678/0001-90",
		"Rua Business, 100",
		"99999-0000",
		"Regime de compet\\352ncia",
		"Inclui lan\\347amentos pagos e pendentes",
		"Exclui pagamentos de fatura",
		"Receitas Operacionais",
		"Despesas Administrativas",
		"Despesas Comerciais",
		"Equipe e Prestadores",
		"Custos por Grupo Macro",
		"Receitas por categoria",
		"Gastos por categoria",
		"Mensalidade de cliente",
		"Banco/Tarifas",
		"Despesa comercial",
		"Folha equipe",
		"R$ 1.162,56",
		"R$ 2.466,42",
		"-R$ 2.132,13",
		"-R$ 234,29",
		"-R$ 100,00",
		"-R$ 1.303,86",
		"Pagina 1/2",
		"Pagina 2/2",
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("PDF body missing %q\nbody prefix: %s", want, body[:min(len(body), 1200)])
		}
	}
	if strings.Contains(body, "Receita Outro Workspace") {
		t.Fatalf("PDF leaked foreign workspace data")
	}
}

func TestHandleExportarDREPDFPersonalSemDadosCorporativosNaoQuebra(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()

	now := time.Now().Unix()
	execTestSQL(t, db, `INSERT INTO users (id, name, email, password_hash, created_at, updated_at) VALUES ('u-pdf-personal', 'User PDF', 'pdf-personal@example.com', 'hash', ?, ?)`, now, now)
	execTestSQL(t, db, `INSERT INTO workspaces (id, name, type, created_at, updated_at) VALUES ('ws-pdf-personal', 'Casa PDF', 'personal', ?, ?)`, now, now)
	execTestSQL(t, db, `INSERT INTO workspace_members (workspace_id, user_id, role, joined_at) VALUES ('ws-pdf-personal', 'u-pdf-personal', 'ADMIN', ?)`, now)

	handler := RelatoriosHandler{ActorRole: testMetasAdminRole, DB: db, WorkspaceID: "ws-pdf-personal", UserID: "u-pdf-personal"}
	req := httptest.NewRequest(http.MethodGet, "/relatorios/dre.pdf?mes=8&ano=2026", nil)
	rr := httptest.NewRecorder()

	handler.HandleExportarDREPDF(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d, body = %q", rr.Code, http.StatusOK, rr.Body.String())
	}
	body := rr.Body.String()
	if !strings.Contains(body, "Casa PDF") {
		t.Fatalf("PDF should use workspace name for personal fallback")
	}
	if !strings.Contains(body, "Sem dados de DRE") {
		t.Fatalf("PDF should render empty DRE state without failing")
	}
}

func TestParseDREPDFPeriodRejectsInvalidMonth(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/relatorios/dre.pdf?month=2026-99", nil)
	if _, _, err := parseDREPDFPeriod(req); err == nil {
		t.Fatal("expected invalid month error")
	}
}

func seedDREPDFBusinessScenario(t *testing.T, db *sql.DB) {
	t.Helper()

	now := time.Now().Unix()
	augDate := testUnixDate("2026-08-10")

	execTestSQL(t, db, `INSERT INTO users (id, name, email, password_hash, created_at, updated_at) VALUES ('u-pdf-biz', 'Biz PDF', 'biz-pdf@example.com', 'hash', ?, ?)`, now, now)
	execTestSQL(t, db, `
		INSERT INTO workspaces (id, name, type, company_name, cnpj_cpf, address, phone, created_at, updated_at)
		VALUES ('ws-pdf-biz', 'Workspace PDF Biz', 'business', 'Empresa PDF Ltda', '12.345.678/0001-90', 'Rua Business, 100', '(11) 99999-0000', ?, ?)
	`, now, now)
	execTestSQL(t, db, `INSERT INTO workspaces (id, name, type, created_at, updated_at) VALUES ('ws-pdf-other', 'Other PDF', 'business', ?, ?)`, now, now)
	execTestSQL(t, db, `INSERT INTO workspace_members (workspace_id, user_id, role, joined_at) VALUES ('ws-pdf-biz', 'u-pdf-biz', 'ADMIN', ?)`, now)
	execTestSQL(t, db, `INSERT INTO accounts (id, workspace_id, name, type, initial_balance, current_balance, created_at, updated_at) VALUES
		('acc-pdf-biz', 'ws-pdf-biz', 'Conta PJ', 'CHECKING', 100000, 100000, ?, ?),
		('acc-pdf-other', 'ws-pdf-other', 'Conta Outro', 'CHECKING', 100000, 100000, ?, ?)
	`, now, now, now, now)
	execTestSQL(t, db, `INSERT INTO categories (id, workspace_id, name, icon, color, type, macro_group, created_at) VALUES
		('cat-pdf-rev', 'ws-pdf-biz', 'Mensalidade de cliente', 'briefcase', '#22c55e', 'INCOME', 'Receitas Operacionais', ?),
		('cat-pdf-admin', 'ws-pdf-biz', 'Banco/Tarifas', 'landmark', '#eab308', 'EXPENSE', 'Despesas Administrativas', ?),
		('cat-pdf-commercial', 'ws-pdf-biz', 'Despesa comercial', 'tag', '#f97316', 'EXPENSE', 'Despesas Comerciais', ?),
		('cat-pdf-team', 'ws-pdf-biz', 'Folha equipe', 'users', '#ef4444', 'EXPENSE', 'Equipe e Prestadores', ?),
		('cat-pdf-other', 'ws-pdf-other', 'Receita Outro Workspace', 'briefcase', '#22c55e', 'INCOME', 'Receitas Operacionais', ?)
	`, now, now, now, now, now)
	execTestSQL(t, db, `INSERT INTO transactions (id, workspace_id, user_id, account_id, category_id, type, amount, date, description, status, paid_at, installment_number, total_installments, created_at, updated_at) VALUES
		('tx-pdf-rev', 'ws-pdf-biz', 'u-pdf-biz', 'acc-pdf-biz', 'cat-pdf-rev', 'INCOME', 116256, ?, 'Mensalidade agosto', 'paid', ?, 1, 1, ?, ?),
		('tx-pdf-admin', 'ws-pdf-biz', 'u-pdf-biz', 'acc-pdf-biz', 'cat-pdf-admin', 'EXPENSE', 10000, ?, 'Banco agosto', 'paid', ?, 1, 1, ?, ?),
		('tx-pdf-commercial', 'ws-pdf-biz', 'u-pdf-biz', 'acc-pdf-biz', 'cat-pdf-commercial', 'EXPENSE', 23429, ?, 'Comercial agosto', 'paid', ?, 1, 1, ?, ?),
		('tx-pdf-team', 'ws-pdf-biz', 'u-pdf-biz', 'acc-pdf-biz', 'cat-pdf-team', 'EXPENSE', 213213, ?, 'Equipe agosto', 'pending', NULL, 1, 1, ?, ?),
		('tx-pdf-other', 'ws-pdf-other', 'u-pdf-biz', 'acc-pdf-other', 'cat-pdf-other', 'INCOME', 999999, ?, 'Receita Outro Workspace', 'paid', ?, 1, 1, ?, ?)
	`, augDate, augDate, now, now, augDate, augDate, now, now, augDate, augDate, now, now, augDate, now, now, augDate, augDate, now, now)
}

func seedExtratoPDFScenario(t *testing.T, db *sql.DB) {
	t.Helper()
	now := time.Now().Unix()
	augDate := testUnixDate("2026-08-10")

	execTestSQL(t, db, `INSERT INTO users (id, name, email, password_hash, created_at, updated_at) VALUES ('u-pdf-ext', 'User PDF Ext', 'pdf-ext@example.com', 'hash', ?, ?)`, now, now)
	execTestSQL(t, db, `INSERT INTO workspaces (id, name, type, created_at, updated_at) VALUES ('ws-pdf-ext', 'WS PDF Ext', 'personal', ?, ?)`, now, now)
	execTestSQL(t, db, `INSERT INTO workspace_members (workspace_id, user_id, role, joined_at) VALUES ('ws-pdf-ext', 'u-pdf-ext', 'ADMIN', ?)`, now)
	execTestSQL(t, db, `INSERT INTO accounts (id, workspace_id, name, type, initial_balance, current_balance, created_at, updated_at) VALUES
		('chk-pdf-ext', 'ws-pdf-ext', 'Conta Corrente', 'CHECKING', 100000, 100000, ?, ?),
		('sav-pdf-ext', 'ws-pdf-ext', 'Poupanca', 'SAVINGS', 50000, 50000, ?, ?)
	`, now, now, now, now)
	execTestSQL(t, db, `INSERT INTO categories (id, workspace_id, name, icon, color, type, macro_group, created_at) VALUES
		('cat-pdf-salary', 'ws-pdf-ext', 'Salario', 'briefcase', '#22c55e', 'INCOME', 'Receitas Operacionais', ?),
		('cat-pdf-food', 'ws-pdf-ext', 'Alimentacao', 'utensils', '#f97316', 'EXPENSE', 'Essencial', ?)
	`, now, now)
	execTestSQL(t, db, `INSERT INTO transactions (id, workspace_id, user_id, account_id, category_id, type, amount, date, description, status, paid_at, installment_number, total_installments, created_at, updated_at) VALUES
		('tx-pdf-inc', 'ws-pdf-ext', 'u-pdf-ext', 'chk-pdf-ext', 'cat-pdf-salary', 'INCOME', 120000, ?, 'Freela', 'paid', ?, 1, 1, ?, ?),
		('tx-pdf-exp', 'ws-pdf-ext', 'u-pdf-ext', 'chk-pdf-ext', 'cat-pdf-food', 'EXPENSE', 35000, ?, 'Supermercado', 'paid', ?, 1, 1, ?, ?),
		('tx-pdf-transfer', 'ws-pdf-ext', 'u-pdf-ext', 'chk-pdf-ext', NULL, 'TRANSFER', 50000, ?, 'Transferencia poupanca', 'paid', ?, 1, 1, ?, ?)
	`, augDate, augDate, now, now, augDate, augDate, now, now, augDate, augDate, now, now)
	execTestSQL(t, db, `UPDATE transactions SET destination_account_id = 'sav-pdf-ext' WHERE id = 'tx-pdf-transfer'`)
}

func TestHandleExportarExtratoPDF_Movimentos(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()

	seedExtratoPDFScenario(t, db)

	handler := RelatoriosHandler{ActorRole: testMetasAdminRole, DB: db, WorkspaceID: "ws-pdf-ext", UserID: "u-pdf-ext"}
	req := httptest.NewRequest(http.MethodGet, "/relatorios/export.pdf?tipo=extrato-movimentos&mes=8&ano=2026", nil)
	rr := httptest.NewRecorder()

	handler.HandleExportarExtratoPDF(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d, body = %q", rr.Code, http.StatusOK, rr.Body.String())
	}
	if got := rr.Header().Get("Content-Type"); got != "application/pdf" {
		t.Fatalf("Content-Type = %q, want application/pdf", got)
	}
	if got := rr.Header().Get("Content-Disposition"); !strings.Contains(got, `filename="extrato-movimentos-2026-08.pdf"`) {
		t.Fatalf("Content-Disposition = %q, want extrato-movimentos filename", got)
	}

	body := rr.Body.String()
	for _, want := range []string{"%PDF-1.4", "Extrato", "Movimentos confirmados por conta", "Transfer\\352ncias listadas separadamente", "Freela", "Supermercado", "Transferencia"} {
		if !strings.Contains(body, want) {
			t.Fatalf("PDF body missing %q", want)
		}
	}
}

func TestHandleExportarExtratoPDF_Diario(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()

	seedExtratoPDFScenario(t, db)

	handler := RelatoriosHandler{ActorRole: testMetasAdminRole, DB: db, WorkspaceID: "ws-pdf-ext", UserID: "u-pdf-ext"}
	req := httptest.NewRequest(http.MethodGet, "/relatorios/export.pdf?tipo=extrato-diario&mes=8&ano=2026", nil)
	rr := httptest.NewRecorder()

	handler.HandleExportarExtratoPDF(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d, body = %q", rr.Code, http.StatusOK, rr.Body.String())
	}
	if got := rr.Header().Get("Content-Type"); got != "application/pdf" {
		t.Fatalf("Content-Type = %q, want application/pdf", got)
	}
	if got := rr.Header().Get("Content-Disposition"); !strings.Contains(got, `filename="extrato-diario-2026-08.pdf"`) {
		t.Fatalf("Content-Disposition = %q, want extrato-diario filename", got)
	}

	body := rr.Body.String()
	for _, want := range []string{"Resumo di\\341rio", "Fechamento di\\341rio do per\\355odo", "Sem transfer\\352ncias internas"} {
		if !strings.Contains(body, want) {
			t.Fatalf("PDF body missing %q", want)
		}
	}
}

func TestHandleExportarExtratoPDF_TransferenciaNeutra(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()

	seedExtratoPDFScenario(t, db)

	handler := RelatoriosHandler{ActorRole: testMetasAdminRole, DB: db, WorkspaceID: "ws-pdf-ext", UserID: "u-pdf-ext"}
	req := httptest.NewRequest(http.MethodGet, "/relatorios/export.pdf?tipo=extrato-movimentos&mes=8&ano=2026", nil)
	rr := httptest.NewRecorder()

	handler.HandleExportarExtratoPDF(rr, req)

	body := rr.Body.String()
	if !strings.Contains(body, "Transferencia") {
		t.Fatal("PDF body missing transfer row")
	}
	if !strings.Contains(body, "R$ 500,00") {
		t.Fatal("transfer should appear as R$ 500,00 in Entrada/Saida columns")
	}
}

func TestHandleExportarExtratoPDF_DiarioIgnoraTransferencia(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()

	seedExtratoPDFScenario(t, db)

	handler := RelatoriosHandler{ActorRole: testMetasAdminRole, DB: db, WorkspaceID: "ws-pdf-ext", UserID: "u-pdf-ext"}
	req := httptest.NewRequest(http.MethodGet, "/relatorios/export.pdf?tipo=extrato-diario&mes=8&ano=2026", nil)
	rr := httptest.NewRecorder()

	handler.HandleExportarExtratoPDF(rr, req)

	body := rr.Body.String()
	if strings.Contains(body, "500,00") {
		t.Fatal("daily summary PDF should not contain transfer amount")
	}
}

func TestHandleExportarExtratoPDF_WorkspaceIsolation(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()

	seedExtratoPDFScenario(t, db)
	seedReportHelpersSecondWorkspace(t, db)

	augDate := testUnixDate("2026-08-10")
	execTestSQL(t, db, `INSERT INTO transactions (id, workspace_id, user_id, account_id, category_id, type, amount, date, description, status, paid_at, installment_number, total_installments, created_at, updated_at)
		VALUES ('tx-pdf-other', 'ws-rpt2', 'u-rpt2', 'chk-rpt2', 'cat-other', 'EXPENSE', 99999, ?, 'Despesa Outro WS', 'paid', ?, 1, 1, ?, ?)
	`, augDate, augDate, time.Now().Unix(), time.Now().Unix())

	handler := RelatoriosHandler{ActorRole: testMetasAdminRole, DB: db, WorkspaceID: "ws-pdf-ext", UserID: "u-pdf-ext"}
	req := httptest.NewRequest(http.MethodGet, "/relatorios/export.pdf?tipo=extrato-movimentos&mes=8&ano=2026", nil)
	rr := httptest.NewRecorder()

	handler.HandleExportarExtratoPDF(rr, req)

	body := rr.Body.String()
	if strings.Contains(body, "Despesa Outro WS") {
		t.Fatal("PDF leaked foreign workspace data")
	}
}

func TestHandleExportarExtratoPDF_PeriodoInvalido(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()

	handler := RelatoriosHandler{ActorRole: testMetasAdminRole, DB: db, WorkspaceID: "ws-pdf-ext", UserID: "u-pdf-ext"}
	req := httptest.NewRequest(http.MethodGet, "/relatorios/export.pdf?tipo=extrato-movimentos&mes=99&ano=2026", nil)
	rr := httptest.NewRecorder()

	handler.HandleExportarExtratoPDF(rr, req)

	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d", rr.Code, http.StatusBadRequest)
	}
}

func TestHandleExportarExtratoPDF_TipoInvalido(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()

	handler := RelatoriosHandler{ActorRole: testMetasAdminRole, DB: db, WorkspaceID: "ws-pdf-ext", UserID: "u-pdf-ext"}
	req := httptest.NewRequest(http.MethodGet, "/relatorios/export.pdf?tipo=invalido&mes=8&ano=2026", nil)
	rr := httptest.NewRecorder()

	handler.HandleExportarExtratoPDF(rr, req)

	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d", rr.Code, http.StatusBadRequest)
	}
}

func TestFitPDFColumnsToWidthMantemLimiteDaPagina(t *testing.T) {
	cols := fitPDFColumnsToWidth([]pdfTableColumn{
		{Label: "Data", Width: 55},
		{Label: "Descricao", Width: 170},
		{Label: "Conta", Width: 110},
		{Label: "Tipo", Width: 70},
		{Label: "Entrada", Width: 75},
		{Label: "Saida", Width: 75},
	}, drePDFContentWidth)

	if got := pdfTableWidth(cols); got > drePDFContentWidth {
		t.Fatalf("table width = %.2f, want <= %.2f", got, drePDFContentWidth)
	}
}

func assertPDFBodyContainsText(t *testing.T, body, want string) {
	t.Helper()
	if !strings.Contains(body, pdfEscapeText(want)) {
		t.Fatalf("PDF body missing %q\nbody prefix: %s", want, body[:min(len(body), 1200)])
	}
}

func TestPDFWinAnsiEscapesAcentosPortugueses(t *testing.T) {
	escaped := pdfEscapeText("Fatura de cartão previsão competência mês família relatório")
	if strings.Contains(escaped, "?") {
		t.Fatalf("pdfEscapeText should not replace Portuguese accents with ?: %q", escaped)
	}
	for _, want := range []string{`cart\343o`, `previs\343o`, `compet\352ncia`, `m\352s`, `fam\355lia`, `relat\363rio`} {
		if !strings.Contains(escaped, want) {
			t.Fatalf("pdfEscapeText missing %q in %q", want, escaped)
		}
	}
}

func TestPDFDocumentPreservaTextoCartaoWinAnsi(t *testing.T) {
	doc := newDREPDFPage()
	doc.text(42, 760, 10, pdfColor{24, 24, 27}, "Fatura de cartão")
	body := string(doc.build(nil))
	assertPDFBodyContainsText(t, body, "Fatura de cartão")
	if strings.Contains(body, "cart?o") || strings.Contains(body, "cart�o") {
		t.Fatalf("PDF body contains broken card text marker")
	}
}

func seedCaixaPDFScenario(t *testing.T, db *sql.DB) {
	t.Helper()
	now := time.Now().Unix()

	execTestSQL(t, db, `INSERT INTO users (id, name, email, password_hash, created_at, updated_at) VALUES ('u-pdf-caixa', 'User PDF Caixa', 'u-pdf-caixa@example.com', 'hash', ?, ?)`, now, now)
	execTestSQL(t, db, `
		INSERT INTO workspaces (id, name, type, company_name, cnpj_cpf, address, phone, created_at, updated_at)
		VALUES ('ws-pdf-caixa', 'Workspace PDF Caixa', 'business', 'Empresa Caixa Ltda', '12.345.678/0001-91', 'Rua Caixa, 200', '(11) 99999-1111', ?, ?)
	`, now, now)
	execTestSQL(t, db, `INSERT INTO workspaces (id, name, type, created_at, updated_at) VALUES ('ws-pdf-caixa-other', 'Other PDF Caixa', 'business', ?, ?)`, now, now)
	execTestSQL(t, db, `INSERT INTO workspace_members (workspace_id, user_id, role, joined_at) VALUES ('ws-pdf-caixa', 'u-pdf-caixa', 'ADMIN', ?)`, now)
	execTestSQL(t, db, `INSERT INTO accounts (id, workspace_id, name, type, initial_balance, current_balance, created_at, updated_at) VALUES
		('acc-pdf-caixa', 'ws-pdf-caixa', 'Conta PJ', 'CHECKING', 100000, 100000, ?, ?),
		('acc-pdf-caixa-other', 'ws-pdf-caixa-other', 'Conta Outro', 'CHECKING', 100000, 100000, ?, ?)
	`, now, now, now, now)
	execTestSQL(t, db, `INSERT INTO categories (id, workspace_id, name, icon, color, type, macro_group, created_at) VALUES
		('cat-pdf-caixa-rev', 'ws-pdf-caixa', 'Mensalidade', 'briefcase', '#22c55e', 'INCOME', 'Receitas Operacionais', ?),
		('cat-pdf-caixa-admin', 'ws-pdf-caixa', 'Banco/Tarifas', 'landmark', '#eab308', 'EXPENSE', 'Despesas Administrativas', ?),
		('cat-pdf-caixa-other', 'ws-pdf-caixa-other', 'Receita Outro Workspace', 'briefcase', '#22c55e', 'INCOME', 'Receitas Operacionais', ?)
	`, now, now, now)
}

func TestHandleExportarCaixaPDFBusinessIncluiIdentidadePeriodoEDados(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()

	seedCaixaPDFScenario(t, db)

	now := time.Now().Unix()
	augDate := testUnixDate("2026-08-10")
	execTestSQL(t, db, `INSERT INTO transactions (id, workspace_id, user_id, account_id, category_id, type, amount, date, due_date, description, status, paid_at, installment_number, total_installments, created_at, updated_at) VALUES
		('tx-pdf-caixa-rev', 'ws-pdf-caixa', 'u-pdf-caixa', 'acc-pdf-caixa', 'cat-pdf-caixa-rev', 'INCOME', 116256, ?, ?, 'Mensalidade agosto', 'paid', ?, 1, 1, ?, ?),
		('tx-pdf-caixa-admin', 'ws-pdf-caixa', 'u-pdf-caixa', 'acc-pdf-caixa', 'cat-pdf-caixa-admin', 'EXPENSE', 10000, ?, ?, 'Banco agosto', 'paid', ?, 1, 1, ?, ?),
		('tx-pdf-caixa-other', 'ws-pdf-caixa-other', 'u-pdf-caixa', 'acc-pdf-caixa-other', 'cat-pdf-caixa-other', 'INCOME', 999999, ?, ?, 'Receita Outro Workspace', 'paid', ?, 1, 1, ?, ?)
	`, augDate, augDate, augDate, now, now, augDate, augDate, augDate, now, now, augDate, augDate, augDate, now, now)

	handler := RelatoriosHandler{ActorRole: testMetasAdminRole, DB: db, WorkspaceID: "ws-pdf-caixa", UserID: "u-pdf-caixa"}
	req := httptest.NewRequest(http.MethodGet, "/relatorios/caixa.pdf?month=2026-08", nil)
	rr := httptest.NewRecorder()

	handler.HandleExportarCaixaPDF(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d, body = %q", rr.Code, http.StatusOK, rr.Body.String())
	}
	if got := rr.Header().Get("Content-Type"); got != "application/pdf" {
		t.Fatalf("Content-Type = %q, want application/pdf", got)
	}
	if got := rr.Header().Get("Content-Disposition"); !strings.Contains(got, `filename="caixa-2026-08.pdf"`) {
		t.Fatalf("Content-Disposition = %q, want caixa filename", got)
	}

	body := rr.Body.String()
	if !strings.Contains(body, "%PDF-1.4") {
		t.Fatalf("PDF body missing header\nbody prefix: %s", body[:min(len(body), 1200)])
	}
	for _, want := range []string{
		"Demonstrativo de Caixa Realizado",
		"Agosto 2026",
		"2026-08",
		"Empresa Caixa Ltda",
		"12.345.678/0001-91",
		"Rua Caixa, 200",
		"Regime de caixa",
		"Apenas lançamentos pagos",
		"Compras no cartão não entram",
		"Detalhe do mês selecionado",
		"Receitas Operacionais",
		"Despesas Administrativas",
		"Mensalidade",
		"Banco/Tarifas",
		"R$ 1.162,56",
		"-R$ 100,00",
	} {
		assertPDFBodyContainsText(t, body, want)
	}
	if strings.Contains(body, "Receita Outro Workspace") {
		t.Fatalf("PDF leaked foreign workspace data")
	}
	if strings.Contains(body, "Inclui lancamentos pagos e pendentes") || strings.Contains(body, "previsto") {
		t.Fatalf("PDF should not contain pending/projected text")
	}
}

func TestHandleExportarCaixaPDFPrevisaoUsaModoETitulo(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()

	seedCaixaPDFScenario(t, db)

	now := time.Now().Unix()
	targetMonth := time.Now().UTC().AddDate(0, 1, 0)
	targetMonth = time.Date(targetMonth.Year(), targetMonth.Month(), 1, 12, 0, 0, 0, time.UTC)
	accountingDate := targetMonth.AddDate(0, -1, 9).Unix()
	targetDue := targetMonth.AddDate(0, 0, 14).Unix()
	execTestSQL(t, db, `
		INSERT INTO transactions (id, workspace_id, user_id, account_id, category_id, type, amount, date, due_date, description, status, paid_at, installment_number, total_installments, created_at, updated_at) VALUES
		('tx-pdf-caixa-prev', 'ws-pdf-caixa', 'u-pdf-caixa', 'acc-pdf-caixa', 'cat-pdf-caixa-admin', 'EXPENSE', 25000, ?, ?, 'Banco previsto agosto', 'pending', NULL, 1, 1, ?, ?)
	`, accountingDate, targetDue, now, now)

	handler := RelatoriosHandler{ActorRole: testMetasAdminRole, DB: db, WorkspaceID: "ws-pdf-caixa", UserID: "u-pdf-caixa"}
	req := httptest.NewRequest(http.MethodGet, fmt.Sprintf("/relatorios/caixa.pdf?modo=previsao&mes=%d&ano=%d", targetMonth.Month(), targetMonth.Year()), nil)
	rr := httptest.NewRecorder()

	handler.HandleExportarCaixaPDF(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d, body = %q", rr.Code, http.StatusOK, rr.Body.String())
	}
	wantFilename := fmt.Sprintf(`filename="caixa-previsao-%04d-%02d.pdf"`, targetMonth.Year(), targetMonth.Month())
	if got := rr.Header().Get("Content-Disposition"); !strings.Contains(got, wantFilename) {
		t.Fatalf("Content-Disposition = %q, want caixa-previsao filename", got)
	}
	body := rr.Body.String()
	for _, want := range []string{
		"Demonstrativo de Caixa Previsto",
		"Previsão por vencimento - próximos 12 meses",
		"Inclui realizados e pendentes",
		"SALDO PROJETADO",
		"R$ 750,00",
		"RESULTADO PREVISTO",
		"Detalhe do mês selecionado",
		"Despesas Administrativas",
		"-R$ 250,00",
	} {
		assertPDFBodyContainsText(t, body, want)
	}
	if strings.Contains(body, "cart�o") || strings.Contains(body, "cart?o") {
		t.Fatalf("PDF body contains broken card accent marker")
	}
	if strings.Contains(body, "Tabela Demonstrativo") {
		t.Fatalf("PDF should use cleaner demonstrativo label, got legacy table label")
	}
}

func TestHandleExportarCaixaPDFRealizadoNaoContemSaldoProjetado(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()

	seedCaixaPDFScenario(t, db)

	now := time.Now().Unix()
	augDate := testUnixDate("2026-08-10")
	execTestSQL(t, db, `INSERT INTO transactions (id, workspace_id, user_id, account_id, category_id, type, amount, date, due_date, description, status, paid_at, installment_number, total_installments, created_at, updated_at) VALUES
		('tx-pdf-caixa-rev-2', 'ws-pdf-caixa', 'u-pdf-caixa', 'acc-pdf-caixa', 'cat-pdf-caixa-rev', 'INCOME', 50000, ?, ?, 'Mensalidade', 'paid', ?, 1, 1, ?, ?)
	`, augDate, augDate, augDate, now, now)

	handler := RelatoriosHandler{ActorRole: testMetasAdminRole, DB: db, WorkspaceID: "ws-pdf-caixa", UserID: "u-pdf-caixa"}
	req := httptest.NewRequest(http.MethodGet, "/relatorios/caixa.pdf?month=2026-08", nil)
	rr := httptest.NewRecorder()

	handler.HandleExportarCaixaPDF(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rr.Code, http.StatusOK)
	}
	body := rr.Body.String()

	if strings.Contains(body, "Saldo Projetado") {
		t.Fatalf("Caixa Realizado PDF should not contain Saldo Projetado column")
	}
}

func TestHandleExportarCaixaPDFPrevisaoContemSaldoProjetadoEFaturaCartao(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()

	seedCaixaPDFScenario(t, db)

	handler := RelatoriosHandler{ActorRole: testMetasAdminRole, DB: db, WorkspaceID: "ws-pdf-caixa", UserID: "u-pdf-caixa"}
	req := httptest.NewRequest(http.MethodGet, "/relatorios/caixa.pdf?modo=previsao&mes=8&ano=2026", nil)
	rr := httptest.NewRecorder()

	handler.HandleExportarCaixaPDF(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rr.Code, http.StatusOK)
	}
	body := rr.Body.String()

	for _, want := range []string{"SALDO PROJETADO", "RESULTADO PREVISTO"} {
		if !strings.Contains(body, want) {
			t.Fatalf("Caixa Previsao PDF body missing %q", want)
		}
	}

	assertPDFBodyContainsText(t, body, "Previsão por vencimento")
	assertPDFBodyContainsText(t, body, "Compras no cartão")

	if strings.Contains(body, "cart?o") || strings.Contains(body, "cart\ufffdo") {
		t.Fatalf("Caixa Previsao PDF contains broken 'cartão' marker")
	}
}

func TestFitPDFColumnsToWidthExpandeParaPreencherLargura(t *testing.T) {
	cols := fitPDFColumnsToWidth([]pdfTableColumn{
		{Label: "Mes", Width: 92, Align: "left"},
		{Label: "Resultado", Width: 152, Align: "right"},
	}, drePDFContentWidth)

	if got := pdfTableWidth(cols); got < drePDFContentWidth-0.1 || got > drePDFContentWidth+0.1 {
		t.Fatalf("2-col table width = %.2f, want ~%.2f", got, drePDFContentWidth)
	}

	cols3 := fitPDFColumnsToWidth([]pdfTableColumn{
		{Label: "Mes", Width: 92, Align: "left"},
		{Label: "Resultado", Width: 152, Align: "right"},
		{Label: "Saldo Projetado", Width: 168, Align: "right"},
	}, drePDFContentWidth)

	if got := pdfTableWidth(cols3); got < drePDFContentWidth-0.1 || got > drePDFContentWidth+0.1 {
		t.Fatalf("3-col table width = %.2f, want ~%.2f", got, drePDFContentWidth)
	}
}

func TestBuildCaixaSummaryColumnsPreenchimentoProporcional(t *testing.T) {
	cols := buildCaixaSummaryColumns(drePDFContentWidth, false)
	if got := pdfTableWidth(cols); got < drePDFContentWidth-0.1 || got > drePDFContentWidth+0.1 {
		t.Fatalf("realizado summary width = %.2f, want ~%.2f", got, drePDFContentWidth)
	}
	if len(cols) != 2 {
		t.Fatalf("realizado summary cols = %d, want 2", len(cols))
	}

	cols = buildCaixaSummaryColumns(drePDFContentWidth, true)
	if got := pdfTableWidth(cols); got < drePDFContentWidth-0.1 || got > drePDFContentWidth+0.1 {
		t.Fatalf("previsao summary width = %.2f, want ~%.2f", got, drePDFContentWidth)
	}
	if len(cols) != 3 {
		t.Fatalf("previsao summary cols = %d, want 3", len(cols))
	}
	if cols[2].Label != "Saldo Projetado" {
		t.Fatalf("third column label = %q, want 'Saldo Projetado'", cols[2].Label)
	}
}

func TestPDFDreEscapesAcentosCorretosNoTexto(t *testing.T) {
	doc := newDREPDFPage()
	doc.text(42, 760, 10, pdfColor{24, 24, 27}, "Competência")
	doc.text(42, 745, 10, pdfColor{24, 24, 27}, "Previsão")
	doc.text(42, 730, 10, pdfColor{24, 24, 27}, "Fatura de cartão")
	doc.text(42, 715, 10, pdfColor{24, 24, 27}, "mês")
	doc.text(42, 700, 10, pdfColor{24, 24, 27}, "família")
	doc.text(42, 685, 10, pdfColor{24, 24, 27}, "relatório")
	doc.text(42, 670, 10, pdfColor{24, 24, 27}, "Pró-labore")
	body := string(doc.build(nil))

	for _, want := range []string{"Competência", "Previsão", "Fatura de cartão", "mês", "família", "relatório", "Pró-labore"} {
		assertPDFBodyContainsText(t, body, want)
	}
	if strings.Contains(body, "cart?o") || strings.Contains(body, "cart\ufffdo") {
		t.Fatalf("PDF body contains broken accent marker")
	}
}

func TestPDFCaixaRealizadoUmaPaginaQuandoCategoriasVazias(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()

	now := time.Now().Unix()
	execTestSQL(t, db, `INSERT INTO users (id, name, email, password_hash, created_at, updated_at) VALUES ('u-pdf-cp1', 'User CP1', 'u-pdf-cp1@example.com', 'hash', ?, ?)`, now, now)
	execTestSQL(t, db, `
		INSERT INTO workspaces (id, name, type, company_name, created_at, updated_at)
		VALUES ('ws-pdf-cp1', 'WS CP1', 'business', 'Empresa CP1', ?, ?)
	`, now, now)
	execTestSQL(t, db, `INSERT INTO workspace_members (workspace_id, user_id, role, joined_at) VALUES ('ws-pdf-cp1', 'u-pdf-cp1', 'ADMIN', ?)`, now)
	execTestSQL(t, db, `INSERT INTO accounts (id, workspace_id, name, type, initial_balance, current_balance, created_at, updated_at) VALUES
		('acc-pdf-cp1', 'ws-pdf-cp1', 'Conta', 'CHECKING', 100000, 100000, ?, ?)
	`, now, now)

	handler := RelatoriosHandler{ActorRole: testMetasAdminRole, DB: db, WorkspaceID: "ws-pdf-cp1", UserID: "u-pdf-cp1"}
	req := httptest.NewRequest(http.MethodGet, "/relatorios/caixa.pdf?month=2026-08", nil)
	rr := httptest.NewRecorder()

	handler.HandleExportarCaixaPDF(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rr.Code, http.StatusOK)
	}
	body := rr.Body.String()

	if !strings.Contains(body, "Pagina 1/1") {
		t.Fatalf("Caixa Realizado with no content must show Pagina 1/1, got tail: %s", body[max(0, len(body)-300):])
	}
	if strings.Contains(body, "Pagina 1/2") || strings.Contains(body, "Pagina 2/2") {
		t.Fatalf("Caixa Realizado should not show multi-page when content fits one page")
	}
}

func TestPDFDREPaginaExtraApenasComConteudoReal(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()

	seedDREPDFBusinessScenario(t, db)

	handler := RelatoriosHandler{ActorRole: testMetasAdminRole, DB: db, WorkspaceID: "ws-pdf-biz", UserID: "u-pdf-biz"}
	req := httptest.NewRequest(http.MethodGet, "/relatorios/dre.pdf?month=2026-08", nil)
	rr := httptest.NewRecorder()

	handler.HandleExportarDREPDF(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rr.Code, http.StatusOK)
	}
	body := rr.Body.String()

	if !strings.Contains(body, "Pagina 2/2") {
		t.Fatalf("DRE PDF with categories should have Page 2/2")
	}

	if !strings.Contains(body, "Pagina 1/2") {
		t.Fatalf("DRE PDF with categories should have Page 1/2")
	}
}

func TestPDFFontEmbebidaSubtypeTrueTypeNaoType1(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()

	seedCaixaPDFScenario(t, db)

	handler := RelatoriosHandler{ActorRole: testMetasAdminRole, DB: db, WorkspaceID: "ws-pdf-caixa", UserID: "u-pdf-caixa"}
	req := httptest.NewRequest(http.MethodGet, "/relatorios/caixa.pdf?month=2026-08", nil)
	rr := httptest.NewRecorder()

	handler.HandleExportarCaixaPDF(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rr.Code, http.StatusOK)
	}
	body := rr.Body.String()

	if !strings.Contains(body, "/Subtype /TrueType") {
		t.Fatalf("PDF should use embedded TrueType font")
	}
	if !strings.Contains(body, "/FontFile2") {
		t.Fatalf("PDF should contain FontFile2 (embedded font file)")
	}
	if !strings.Contains(body, "/FontDescriptor") {
		t.Fatalf("PDF should contain FontDescriptor")
	}
	if strings.Contains(body, "/Subtype /Type1") && strings.Contains(body, "/BaseFont /Helvetica") {
		t.Fatalf("PDF should NOT use non-embedded Helvetica Type1")
	}
	if !strings.Contains(body, "/BaseFont /CBEmbedded-Regular") {
		t.Fatalf("PDF should use base font name CBEmbedded-Regular")
	}
}

func TestPDFFontEmbebidaDREPReservaTextoComAcentos(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()

	seedDREPDFBusinessScenario(t, db)

	handler := RelatoriosHandler{ActorRole: testMetasAdminRole, DB: db, WorkspaceID: "ws-pdf-biz", UserID: "u-pdf-biz"}
	req := httptest.NewRequest(http.MethodGet, "/relatorios/dre.pdf?month=2026-08", nil)
	rr := httptest.NewRecorder()

	handler.HandleExportarDREPDF(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rr.Code, http.StatusOK)
	}
	body := rr.Body.String()

	for _, want := range []string{
		"Relat\\363rio gerencial",
		"Regime de compet\\352ncia",
		"Inclui lan\\347amentos pagos",
		"Exclui pagamentos de fatura",
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("DRE PDF missing accented text %q", want)
		}
	}
	if strings.Contains(body, "cart?o") || strings.Contains(body, "cart\ufffdo") {
		t.Fatalf("DRE PDF contains broken accent marker")
	}
}

func TestPDFFontEmbebidaExtratoMovimentosUTF8(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()

	seedExtratoPDFScenario(t, db)

	handler := RelatoriosHandler{ActorRole: testMetasAdminRole, DB: db, WorkspaceID: "ws-pdf-ext", UserID: "u-pdf-ext"}
	req := httptest.NewRequest(http.MethodGet, "/relatorios/export.pdf?tipo=extrato-movimentos&mes=8&ano=2026", nil)
	rr := httptest.NewRecorder()

	handler.HandleExportarExtratoPDF(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rr.Code, http.StatusOK)
	}
	body := rr.Body.String()

	for _, want := range []string{
		"Relat\\363rio de movimentos",
		"Transfer\\352ncias listadas",
		"Transfer\\352ncia",
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("Extrato PDF missing accented text %q", want)
		}
	}
}

func TestPDFInitEmbeddedFontReady(t *testing.T) {
	initEmbeddedFont()

	if !embeddedFontReady {
		t.Fatal("embedded font should be ready after initEmbeddedFont")
	}
	if len(embeddedFontData) == 0 {
		t.Fatal("embedded font TTF data should not be empty")
	}
	if embeddedFontName != "CBEmbedded-Regular" {
		t.Fatalf("embedded font name = %q, want CBEmbedded-Regular", embeddedFontName)
	}

	nonZeroWidths := 0
	for _, w := range embeddedFontWidths {
		if w > 0 {
			nonZeroWidths++
		}
	}
	if nonZeroWidths < 200 {
		t.Fatalf("expected most glyph widths non-zero, got %d non-zero out of 224", nonZeroWidths)
	}
}

func TestWinAnsiToUnicodeRoundtripPortugues(t *testing.T) {
	runes := []rune{'ã', 'õ', 'ç', 'é', 'ê', 'á', 'í', 'ó', 'ú', 'â', 'ô', 'à'}
	for _, r := range runes {
		b, ok := winAnsiByte(r)
		if !ok {
			t.Fatalf("winAnsiByte(%c) returned false", r)
		}
		back := winAnsiToUnicode(b)
		if back != r {
			t.Fatalf("winAnsiToUnicode(winAnsiByte(%c)) = %c, want %c", r, back, r)
		}
	}
}
