package handlers

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
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
