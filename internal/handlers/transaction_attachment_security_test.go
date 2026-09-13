package handlers

import (
	"bytes"
	"errors"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/contabase-app/contabase/internal/paths"
)

func TestPersistTransactionUploadCanonicalizesExtensionFromContent(t *testing.T) {
	root := t.TempDir()
	t.Setenv("DATA_DIR", filepath.Join(root, "state"))
	t.Setenv("UPLOADS_DIR", filepath.Join(root, "uploads"))

	h := TransactionHandler{WorkspaceID: "workspace-1"}
	req := newUploadRequest(t, "anexo", "recibo.html", []byte("%PDF-1.7\n<script>alert('xss')</script>"))

	relPath, err := h.persistTransactionUpload(req)
	if err != nil {
		t.Fatalf("persistTransactionUpload: %v", err)
	}
	if got := filepath.Ext(relPath); got != ".pdf" {
		t.Fatalf("stored extension = %q, want .pdf", got)
	}
	if strings.Contains(relPath, ".html") {
		t.Fatalf("stored path retains user-controlled extension: %q", relPath)
	}
	if _, err := os.Stat(filepath.Join(root, "uploads", filepath.FromSlash(relPath[len("uploads/"):]))); err != nil {
		t.Fatalf("stored attachment missing: %v", err)
	}
}

func TestPersistTransactionUploadRejectsHTMLNamedAsPDF(t *testing.T) {
	h := TransactionHandler{WorkspaceID: "workspace-1"}
	req := newUploadRequest(t, "anexo", "recibo.pdf", []byte("<script>alert('xss')</script>"))

	if _, err := h.persistTransactionUpload(req); err == nil || !strings.Contains(err.Error(), "unsupported file type") {
		t.Fatalf("expected content validation failure, got %v", err)
	}
}

func TestHandleDownloadAnexoForcesBinaryAttachmentForLegacyFiles(t *testing.T) {
	root := t.TempDir()
	t.Setenv("DATA_DIR", filepath.Join(root, "state"))
	t.Setenv("UPLOADS_DIR", filepath.Join(root, "uploads"))

	db := openTestDB(t)
	t.Cleanup(func() { _ = db.Close() })
	seedInvoicePaymentScenario(t, db)

	legacyName := "legacy.html"
	legacyPath := filepath.Join(paths.TransactionUploadsDir("ws-test"), legacyName)
	if err := os.MkdirAll(filepath.Dir(legacyPath), 0700); err != nil {
		t.Fatalf("mkdir legacy upload directory: %v", err)
	}
	payload := []byte("<script>alert('stored xss')</script>")
	if err := os.WriteFile(legacyPath, payload, 0600); err != nil {
		t.Fatalf("write legacy attachment: %v", err)
	}
	execTestSQL(t, db, `
		UPDATE transactions
		SET attachment_path = ?
		WHERE id = 'purchase-test' AND workspace_id = 'ws-test'
	`, "uploads/ws-test/"+legacyName)

	h := TransactionHandler{DB: db, WorkspaceID: "ws-test"}
	req := httptest.NewRequest(http.MethodGet, "/lancamentos/purchase-test/anexo", nil)
	rec := httptest.NewRecorder()
	h.HandleDownloadAnexo(rec, req, "purchase-test")

	res := rec.Result()
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want %d", res.StatusCode, http.StatusOK)
	}
	if got := res.Header.Get("Content-Type"); got != "application/octet-stream" {
		t.Fatalf("Content-Type = %q, want application/octet-stream", got)
	}
	if got := res.Header.Get("Content-Disposition"); !strings.HasPrefix(got, "attachment;") {
		t.Fatalf("Content-Disposition = %q, want attachment", got)
	}
	if got := res.Header.Get("X-Content-Type-Options"); got != "nosniff" {
		t.Fatalf("X-Content-Type-Options = %q, want nosniff", got)
	}

	// Ensure ServeContent did not rewrite the response to an inline HTML type.
	if got := rec.Body.String(); got != string(payload) {
		t.Fatalf("body = %q, want stored legacy payload", got)
	}
}

func TestTransactionAttachmentExtensionAcceptsRecognizedContentOnly(t *testing.T) {
	tests := []struct {
		name string
		body []byte
		want string
		ok   bool
	}{
		{name: "pdf", body: []byte("%PDF-1.4\n1 0 obj\n"), want: ".pdf", ok: true},
		{name: "png", body: testPNGBytes(), want: ".png", ok: true},
		{name: "webp", body: testWebPBytes(), want: ".webp", ok: true},
		{name: "html", body: []byte("<html><script>alert(1)</script></html>"), ok: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := transactionAttachmentExtension(tt.body)
			if ok != tt.ok || got != tt.want {
				t.Fatalf("transactionAttachmentExtension() = (%q, %v), want (%q, %v)", got, ok, tt.want, tt.ok)
			}
		})
	}
}

func TestSafeAttachmentFullPathRejectsCrossWorkspaceAttachment(t *testing.T) {
	root := t.TempDir()
	t.Setenv("UPLOADS_DIR", filepath.Join(root, "uploads"))
	if err := os.MkdirAll(paths.TransactionUploadsDir("workspace-b"), 0700); err != nil {
		t.Fatalf("mkdir other workspace: %v", err)
	}
	if err := os.WriteFile(filepath.Join(paths.TransactionUploadsDir("workspace-b"), "legacy.html"), []byte("x"), 0600); err != nil {
		t.Fatalf("write other workspace file: %v", err)
	}
	if _, err := safeAttachmentFullPath("workspace-a", "uploads/workspace-b/legacy.html"); err == nil {
		t.Fatal("expected cross-workspace attachment path to be rejected")
	}
}

func TestParseMultipartOrFormRejectsAggregateBodyOverLimit(t *testing.T) {
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	part, err := writer.CreateFormFile("anexo", "recibo.pdf")
	if err != nil {
		t.Fatalf("CreateFormFile: %v", err)
	}
	if _, err := part.Write(bytes.Repeat([]byte{'a'}, transactionMultipartMaxBytes)); err != nil {
		t.Fatalf("write attachment: %v", err)
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("close multipart: %v", err)
	}
	req := httptest.NewRequest(http.MethodPost, "/transacoes", &body)
	req.Header.Set("Content-Type", writer.FormDataContentType())

	err = parseMultipartOrForm(httptest.NewRecorder(), req)
	if err == nil {
		t.Fatal("expected aggregate multipart request to exceed the limit")
	}
	var maxErr *http.MaxBytesError
	if !errors.As(err, &maxErr) {
		t.Fatalf("parse error = %v, want MaxBytesError", err)
	}
}

func TestHandleSalvarTransacaoReturns413ForOversizedAggregateRequest(t *testing.T) {
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	part, err := writer.CreateFormFile("anexo", "recibo.pdf")
	if err != nil {
		t.Fatalf("CreateFormFile: %v", err)
	}
	if _, err := part.Write(bytes.Repeat([]byte{'a'}, transactionMultipartMaxBytes)); err != nil {
		t.Fatalf("write attachment: %v", err)
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("close multipart: %v", err)
	}
	req := httptest.NewRequest(http.MethodPost, "/transacoes", &body)
	req.Header.Set("Content-Type", writer.FormDataContentType())
	rr := httptest.NewRecorder()

	(&TransactionHandler{}).HandleSalvarTransacao(rr, req)
	if rr.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("status = %d, want %d", rr.Code, http.StatusRequestEntityTooLarge)
	}
}
