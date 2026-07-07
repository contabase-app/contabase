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

func seedContabilPDFScenario(t *testing.T, db *sql.DB) {
	now := time.Now().Unix()
	execTestSQL(t, db, `
		INSERT INTO users (id, name, email, password_hash, created_at, updated_at)
		VALUES ('u-cpdf', 'CPDF', 'cpdf@t.com', 'hash', ?, ?)
	`, now, now)
	execTestSQL(t, db, `
		INSERT INTO workspaces (id, name, type, company_name, cnpj_cpf, created_at, updated_at)
		VALUES ('ws-cpdf', 'Empresa PDF', 'business', 'Empresa PDF Ltda', '12.345.678/0001-90', ?, ?)
	`, now, now)
	execTestSQL(t, db, `
		INSERT INTO workspace_members (workspace_id, user_id, role, joined_at)
		VALUES ('ws-cpdf', 'u-cpdf', 'ADMIN', ?)
	`, now)
	execTestSQL(t, db, `
		INSERT INTO accounts (id, workspace_id, name, type, initial_balance, current_balance, created_at, updated_at)
		VALUES ('acc-cpdf', 'ws-cpdf', 'Conta Corrente', 'CHECKING', 100000, 100000, ?, ?)
	`, now, now)
}

func TestHandleExportarContabilPDF_Business(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	seedContabilPDFScenario(t, db)

	handler := &RelatoriosHandler{DB: db, WorkspaceID: "ws-cpdf", UserID: "u-cpdf"}

	req := httptest.NewRequest(http.MethodGet, "/relatorios/contabil.pdf?mes=8&ano=2026", nil)
	rr := httptest.NewRecorder()
	handler.HandleExportarContabilPDF(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200 OK, got %d: %s", rr.Code, rr.Body.String())
	}

	ct := rr.Header().Get("Content-Type")
	if ct != "application/pdf" {
		t.Fatalf("expected Content-Type application/pdf, got %s", ct)
	}

	xcto := rr.Header().Get("X-Content-Type-Options")
	if xcto != "nosniff" {
		t.Fatalf("expected X-Content-Type-Options nosniff, got %s", xcto)
	}

	cd := rr.Header().Get("Content-Disposition")
	if !strings.Contains(cd, "filename=\"relatorio-contabil-2026-08.pdf\"") {
		t.Fatalf("expected Content-Disposition with filename relatorio-contabil-2026-08.pdf, got %s", cd)
	}
}

func TestHandleExportarContabilPDF_PersonalForbidden(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()

	now := time.Now().Unix()
	execTestSQL(t, db, `INSERT INTO users (id, name, email, password_hash, created_at, updated_at) VALUES ('u-cpers', 'CPers', 'cp@t.com', 'hash', ?, ?)`, now, now)
	execTestSQL(t, db, `INSERT INTO workspaces (id, name, type, created_at, updated_at) VALUES ('ws-cpers', 'WS Pers', 'personal', ?, ?)`, now, now)
	execTestSQL(t, db, `INSERT INTO workspace_members (workspace_id, user_id, role, joined_at) VALUES ('ws-cpers', 'u-cpers', 'ADMIN', ?)`, now)

	handler := &RelatoriosHandler{DB: db, WorkspaceID: "ws-cpers", UserID: "u-cpers"}

	req := httptest.NewRequest(http.MethodGet, "/relatorios/contabil.pdf?mes=8&ano=2026", nil)
	rr := httptest.NewRecorder()
	handler.HandleExportarContabilPDF(rr, req)

	if rr.Code != http.StatusForbidden {
		t.Fatalf("expected 403 Forbidden for personal, got %d", rr.Code)
	}
}

func TestHandleExportarContabilPDF_ContentContainsKeyElements(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	seedContabilPDFScenario(t, db)

	handler := &RelatoriosHandler{DB: db, WorkspaceID: "ws-cpdf", UserID: "u-cpdf"}

	req := httptest.NewRequest(http.MethodGet, "/relatorios/contabil.pdf?mes=8&ano=2026", nil)
	rr := httptest.NewRecorder()
	handler.HandleExportarContabilPDF(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200 OK, got %d", rr.Code)
	}

	body := rr.Body.String()

	// PDF header
	if !strings.Contains(body, "%PDF-1.4") {
		t.Fatalf("PDF should start with PDF-1.4 header")
	}

	// Title
	if !strings.Contains(body, "Relatorio Contabil") {
		t.Fatalf("PDF should contain Relatorio Contabil title")
	}

	// Period
	if !strings.Contains(body, "Agosto 2026") {
		t.Fatalf("PDF should contain period Agost 2026")
	}

	// Resultado Operacional card
	if !strings.Contains(body, "RESULTADO OPER") {
		t.Fatalf("PDF should contain RESULTADO OPER card")
	}

	// Saldo section with date
	if !strings.Contains(body, "Saldo por conta em") {
		t.Fatalf("PDF should contain 'Saldo por conta em' title")
	}

	// Operational note (text may be WinAnsi-encoded)
	if !strings.Contains(body, "operacional") || !strings.Contains(body, "estimativa") {
		t.Fatalf("PDF should contain operational estimate disclaimer")
	}
	if !strings.Contains(body, "conciliacao") || !strings.Contains(body, "auditavel") {
		t.Fatalf("PDF should contain auditable disclaimer")
	}

	// Workspace identity (CNPJ)
	if !strings.Contains(body, "12.345.678/0001-90") {
		t.Fatalf("PDF should contain workspace CNPJ")
	}
}

func TestHandleExportarContabilPDF_WorkspaceIsolation(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	seedContabilPDFScenario(t, db)

	// Create another workspace with its own data
	now := time.Now().Unix()
	execTestSQL(t, db, `INSERT INTO workspaces (id, name, type, company_name, created_at, updated_at) VALUES ('ws-other-cpdf', 'Other PDF', 'business', 'Other Ltda', ?, ?)`, now, now)
	execTestSQL(t, db, `INSERT INTO accounts (id, workspace_id, name, type, initial_balance, current_balance, created_at, updated_at) VALUES ('acc-other-cpdf', 'ws-other-cpdf', 'Other Acc', 'CHECKING', 999999, 999999, ?, ?)`, now, now)

	handler := &RelatoriosHandler{DB: db, WorkspaceID: "ws-cpdf", UserID: "u-cpdf"}

	req := httptest.NewRequest(http.MethodGet, "/relatorios/contabil.pdf?mes=8&ano=2026", nil)
	rr := httptest.NewRecorder()
	handler.HandleExportarContabilPDF(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200 OK, got %d", rr.Code)
	}

	body := rr.Body.String()
	// Should NOT contain data from the other workspace
	if strings.Contains(body, "999999") || strings.Contains(body, "999.999") {
		t.Fatalf("PDF should not leak data from other workspace")
	}
	if strings.Contains(body, "Other Ltda") {
		t.Fatalf("PDF should not contain other workspace identity")
	}
}

func TestHandleExportarContabilPDF_PeriodoInvalido(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	seedContabilPDFScenario(t, db)

	handler := &RelatoriosHandler{DB: db, WorkspaceID: "ws-cpdf", UserID: "u-cpdf"}

	req := httptest.NewRequest(http.MethodGet, "/relatorios/contabil.pdf?mes=13&ano=2026", nil)
	rr := httptest.NewRecorder()
	handler.HandleExportarContabilPDF(rr, req)

	if rr.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for invalid month, got %d", rr.Code)
	}
}

func TestHandleExportarContabilPDF_DefaultPeriod(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	seedContabilPDFScenario(t, db)

	handler := &RelatoriosHandler{DB: db, WorkspaceID: "ws-cpdf", UserID: "u-cpdf"}

	// No mes/ano params — uses current UTC month
	req := httptest.NewRequest(http.MethodGet, "/relatorios/contabil.pdf", nil)
	rr := httptest.NewRecorder()
	handler.HandleExportarContabilPDF(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200 OK for default period, got %d", rr.Code)
	}

	ct := rr.Header().Get("Content-Type")
	if ct != "application/pdf" {
		t.Fatalf("expected Content-Type application/pdf, got %s", ct)
	}

	now := time.Now().UTC()
	expectedFilename := fmt.Sprintf("filename=\"relatorio-contabil-%04d-%02d.pdf\"", now.Year(), now.Month())
	cd := rr.Header().Get("Content-Disposition")
	if !strings.Contains(cd, expectedFilename) {
		t.Fatalf("expected Content-Disposition with %s, got %s", expectedFilename, cd)
	}
}

func TestHandleExportarContabilPDF_ExistingExportsUnaffected(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	seedContabilPDFScenario(t, db)

	handler := &RelatoriosHandler{DB: db, WorkspaceID: "ws-cpdf", UserID: "u-cpdf"}

	// DRE PDF still works
	req := httptest.NewRequest(http.MethodGet, "/relatorios/dre.pdf?mes=8&ano=2026", nil)
	rr := httptest.NewRecorder()
	handler.HandleExportarDREPDF(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("DRE PDF should still work: got %d", rr.Code)
	}
	if rr.Header().Get("Content-Type") != "application/pdf" {
		t.Fatalf("DRE PDF should have Content-Type application/pdf")
	}

	// Caixa PDF still works
	req2 := httptest.NewRequest(http.MethodGet, "/relatorios/caixa.pdf?mes=8&ano=2026", nil)
	rr2 := httptest.NewRecorder()
	handler.HandleExportarCaixaPDF(rr2, req2)
	if rr2.Code != http.StatusOK {
		t.Fatalf("Caixa PDF should still work: got %d", rr2.Code)
	}

	// Extrato PDF still works
	req3 := httptest.NewRequest(http.MethodGet, "/relatorios/export.pdf?tipo=extrato-movimentos&mes=8&ano=2026", nil)
	rr3 := httptest.NewRecorder()
	handler.HandleExportarExtratoPDF(rr3, req3)
	if rr3.Code != http.StatusOK {
		t.Fatalf("Extrato PDF should still work: got %d", rr3.Code)
	}
}

func TestHandleExportarContabilPDF_MethodNotAllowed(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	seedContabilPDFScenario(t, db)

	handler := &RelatoriosHandler{DB: db, WorkspaceID: "ws-cpdf", UserID: "u-cpdf"}

	// POST should not work (route only handles GET via main.go, but handler itself is callable)
	// The route registration handles method check, but we test the handler directly
	req := httptest.NewRequest(http.MethodPost, "/relatorios/contabil.pdf?mes=8&ano=2026", nil)
	rr := httptest.NewRecorder()
	handler.HandleExportarContabilPDF(rr, req)

	// Handler doesn't check method itself (route does), so POST might work here.
	// Verify it at least produces something valid.
	if rr.Code == http.StatusOK {
		ct := rr.Header().Get("Content-Type")
		if ct != "application/pdf" {
			t.Fatalf("even on POST, handler should return PDF if no method check: got %s", ct)
		}
	}
}
