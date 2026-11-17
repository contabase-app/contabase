package handlers

import (
	"database/sql"
	"html/template"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

type receiptIsolationFixture struct {
	workspaceID     string
	userID          string
	accountID       string
	contactID       string
	transactionID   string
	workspaceName   string
	companyName     string
	document        string
	phone           string
	address         string
	contactName     string
	contactDocument string
	logoPath        string
}

func setupReceiptIsolationWorkspace(
	t *testing.T,
	db *sql.DB,
	workspaceID, workspaceName, companyName, document, phone, address,
	contactName, contactDocument, logoPath string,
) receiptIsolationFixture {
	t.Helper()

	base := seedF1Base(t, db, workspaceID)
	execTestSQL(t, db, `
		UPDATE workspaces
		SET type = 'business', name = ?, company_name = ?, cnpj_cpf = ?,
			address = ?, phone = ?, logo_light_url = ?, logo_dark_url = ''
		WHERE id = ?
	`, workspaceName, companyName, document, address, phone, logoPath, workspaceID)

	contactID := "contact-" + workspaceID
	execTestSQL(t, db, `
		INSERT INTO contacts (id, workspace_id, name, document, type, email, phone, created_at)
		VALUES (?, ?, ?, ?, 'client', '', '', ?)
	`, contactID, workspaceID, contactName, contactDocument, base.now)

	transactionID := "tx-" + workspaceID
	addF1Tx(t, db, f1TxSeed{
		ID:          transactionID,
		WorkspaceID: workspaceID,
		UserID:      base.user,
		AccountID:   base.checking,
		Type:        "INCOME",
		Amount:      123456,
		Status:      "paid",
		Description: "Serviço " + workspaceName,
		CreatedAt:   time.Date(2026, 11, 10, 12, 0, 0, 0, time.UTC).Unix(),
	})
	execTestSQL(t, db, `
		UPDATE transactions SET contact_id = ? WHERE workspace_id = ? AND id = ?
	`, contactID, workspaceID, transactionID)

	return receiptIsolationFixture{
		workspaceID:     workspaceID,
		userID:          base.user,
		accountID:       base.checking,
		contactID:       contactID,
		transactionID:   transactionID,
		workspaceName:   workspaceName,
		companyName:     companyName,
		document:        document,
		phone:           phone,
		address:         address,
		contactName:     contactName,
		contactDocument: contactDocument,
		logoPath:        logoPath,
	}
}

func renderReceiptForIsolationTest(t *testing.T, db *sql.DB, workspaceID, transactionID string) *httptest.ResponseRecorder {
	t.Helper()

	tpl := template.Must(template.New("receipt").Funcs(template.FuncMap{
		"assetPath": func(path string) string { return path },
	}).Parse(readTestFile(t, "templates/pages/recibo.html")))

	handler := &TransactionHandler{
		DB:          db,
		WriterDB:    db,
		Templates:   tpl,
		WorkspaceID: workspaceID,
	}
	recorder := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/transacoes/"+transactionID+"/recibo", nil)
	handler.HandleGerarRecibo(recorder, req, transactionID)
	return recorder
}

func assertReceiptContains(t *testing.T, body string, values ...string) {
	t.Helper()
	for _, value := range values {
		if !strings.Contains(body, value) {
			t.Fatalf("receipt missing isolated value %q", value)
		}
	}
}

func assertReceiptExcludes(t *testing.T, body string, values ...string) {
	t.Helper()
	for _, value := range values {
		if strings.Contains(body, value) {
			t.Fatalf("receipt leaked value %q from another workspace", value)
		}
	}
}

func TestReceiptHandlerKeepsWorkspaceCadastralDataIsolated(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()

	a := setupReceiptIsolationWorkspace(
		t, db, "ws-receipt-a",
		"Empresa Exemplo A", "Empresa Exemplo A Ltda",
		"00.000.000/0000-00", "(00) 0000-0000", "Rua Exemplo A, 100",
		"Contato Exemplo A", "000.000.000-00",
		"/uploads/workspaces/ws-receipt-a/logo-a.png",
	)
	b := setupReceiptIsolationWorkspace(
		t, db, "ws-receipt-b",
		"Empresa Exemplo B", "Empresa Exemplo B Ltda",
		"11.111.111/1111-11", "(00) 0000-0001", "Rua Exemplo B, 200",
		"Contato Exemplo B", "111.111.111-11",
		"/uploads/workspaces/ws-receipt-b/logo-b.png",
	)

	t.Run("workspace A receipt contains only A", func(t *testing.T) {
		recorder := renderReceiptForIsolationTest(t, db, a.workspaceID, a.transactionID)
		if recorder.Code != http.StatusOK {
			t.Fatalf("A receipt status = %d, want %d", recorder.Code, http.StatusOK)
		}
		body := recorder.Body.String()
		assertReceiptContains(t, body,
			a.workspaceName, a.document, a.phone, a.address,
			a.contactName, a.contactDocument, a.logoPath,
			">EA</span>", "R$ 1.234,56",
		)
		assertReceiptExcludes(t, body,
			a.companyName, b.workspaceName, b.companyName, b.document, b.phone,
			b.address, b.contactName, b.contactDocument, b.logoPath, ">EB</span>",
		)
	})

	t.Run("workspace B receipt contains only B", func(t *testing.T) {
		recorder := renderReceiptForIsolationTest(t, db, b.workspaceID, b.transactionID)
		if recorder.Code != http.StatusOK {
			t.Fatalf("B receipt status = %d, want %d", recorder.Code, http.StatusOK)
		}
		body := recorder.Body.String()
		assertReceiptContains(t, body,
			b.workspaceName, b.document, b.phone, b.address,
			b.contactName, b.contactDocument, b.logoPath,
			">EB</span>", "R$ 1.234,56",
		)
		assertReceiptExcludes(t, body,
			b.companyName, a.workspaceName, a.companyName, a.document, a.phone,
			a.address, a.contactName, a.contactDocument, a.logoPath, ">EA</span>",
		)
	})

	t.Run("workspace A cannot read workspace B receipt", func(t *testing.T) {
		recorder := renderReceiptForIsolationTest(t, db, a.workspaceID, b.transactionID)
		if recorder.Code != http.StatusNotFound {
			t.Fatalf("cross-tenant receipt status = %d, want %d", recorder.Code, http.StatusNotFound)
		}
		assertReceiptExcludes(t, recorder.Body.String(),
			b.workspaceName, b.document, b.phone, b.address,
			b.contactName, b.contactDocument, b.logoPath,
		)
	})

	t.Run("personal workspace cannot issue a receipt", func(t *testing.T) {
		personal := seedF1Base(t, db, "ws-receipt-personal")
		recorder := renderReceiptForIsolationTest(t, db, personal.ws, "tx-personal-test")
		if recorder.Code != http.StatusUnprocessableEntity {
			t.Fatalf("personal receipt status = %d, want %d", recorder.Code, http.StatusUnprocessableEntity)
		}
		assertReceiptExcludes(t, recorder.Body.String(),
			a.workspaceName, a.document, a.phone, a.address,
			b.workspaceName, b.document, b.phone, b.address,
		)
	})

	t.Run("empty optional fields do not fall back to another workspace", func(t *testing.T) {
		execTestSQL(t, db, `
			UPDATE workspaces SET cnpj_cpf = '', phone = '', address = '' WHERE id = ?
		`, b.workspaceID)
		recorder := renderReceiptForIsolationTest(t, db, b.workspaceID, b.transactionID)
		if recorder.Code != http.StatusOK {
			t.Fatalf("empty-optional receipt status = %d, want %d", recorder.Code, http.StatusOK)
		}
		body := recorder.Body.String()
		assertReceiptContains(t, body, b.workspaceName, b.contactName, b.contactDocument, b.logoPath)
		assertReceiptExcludes(t, body,
			b.companyName, b.document, b.phone, b.address,
			a.workspaceName, a.document, a.phone, a.address,
			a.contactName, a.contactDocument, a.logoPath,
		)
		if strings.Contains(body, "<dt>Telefone</dt>") {
			t.Fatal("empty phone must not render the issuer phone row")
		}
		if strings.Contains(body, "<dt>Endereço</dt>") {
			t.Fatal("empty address must not render the issuer address row")
		}
		if strings.Count(body, "<dt>CNPJ/CPF</dt>") != 1 {
			t.Fatalf("empty issuer document should leave only the contact document label")
		}
	})

	t.Run("logo fallback never reads another workspace", func(t *testing.T) {
		execTestSQL(t, db, `
			UPDATE workspaces SET logo_light_url = '', logo_dark_url = '' WHERE id = ?
		`, a.workspaceID)
		execTestSQL(t, db, `
			UPDATE users SET profile_photo_path = '' WHERE id = ?
		`, a.userID)
		execTestSQL(t, db, `
			UPDATE users SET profile_photo_path = ? WHERE id = ?
		`, "/uploads/profile/ws-receipt-b-user.png", b.userID)

		recorder := renderReceiptForIsolationTest(t, db, a.workspaceID, a.transactionID)
		if recorder.Code != http.StatusOK {
			t.Fatalf("fallback receipt status = %d, want %d", recorder.Code, http.StatusOK)
		}
		body := recorder.Body.String()
		assertReceiptContains(t, body,
			a.workspaceName, a.document, a.phone, a.address,
			a.contactName, a.contactDocument, `class="brand-fallback"`, ">EA</span>",
		)
		assertReceiptExcludes(t, body,
			a.companyName, b.logoPath, "/uploads/profile/ws-receipt-b-user.png",
			b.workspaceName, b.document, b.phone, b.address, b.contactName,
		)
	})
}
