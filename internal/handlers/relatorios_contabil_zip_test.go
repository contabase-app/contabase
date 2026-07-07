package handlers

import (
	"archive/zip"
	"bytes"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"testing"
)

func readContabilZIP(t *testing.T, raw []byte) map[string][]byte {
	t.Helper()
	zr, err := zip.NewReader(bytes.NewReader(raw), int64(len(raw)))
	if err != nil {
		t.Fatalf("zip.NewReader: %v", err)
	}
	files := make(map[string][]byte)
	for _, f := range zr.File {
		rc, err := f.Open()
		if err != nil {
			t.Fatalf("open %s: %v", f.Name, err)
		}
		var b bytes.Buffer
		if _, err := b.ReadFrom(rc); err != nil {
			rc.Close()
			t.Fatalf("read %s: %v", f.Name, err)
		}
		rc.Close()
		files[f.Name] = b.Bytes()
	}
	return files
}

func TestHandleExportarContabilZIP_Business(t *testing.T) {
	handler, _ := newContabilCSVBusinessHandler(t)

	req := httptest.NewRequest(http.MethodGet, "/relatorios/contabil.zip?month=2026-07", nil)
	rr := httptest.NewRecorder()
	handler.HandleExportarContabilZIP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200, body = %q", rr.Code, rr.Body.String())
	}
	if got := rr.Header().Get("Content-Type"); got != "application/zip" {
		t.Fatalf("Content-Type = %q, want application/zip", got)
	}
	if got := rr.Header().Get("X-Content-Type-Options"); got != "nosniff" {
		t.Fatalf("X-Content-Type-Options = %q, want nosniff", got)
	}
	if got := rr.Header().Get("Content-Disposition"); !strings.Contains(got, `filename="relatorio-contabil-2026-07.zip"`) {
		t.Fatalf("Content-Disposition = %q", got)
	}

	files := readContabilZIP(t, rr.Body.Bytes())

	wantNames := []string{
		"relatorio-contabil-2026-07.pdf",
		"relatorio-contabil-resumo-2026-07.csv",
		"relatorio-contabil-macros-2026-07.csv",
		"relatorio-contabil-saldos-2026-07.csv",
	}
	if len(files) != len(wantNames) {
		got := make([]string, 0, len(files))
		for name := range files {
			got = append(got, name)
		}
		sort.Strings(got)
		t.Fatalf("zip has %d files %v, want exactly %d %v", len(files), got, len(wantNames), wantNames)
	}
	for _, name := range wantNames {
		if _, ok := files[name]; !ok {
			t.Fatalf("zip missing %q", name)
		}
	}

	pdf := files["relatorio-contabil-2026-07.pdf"]
	if len(pdf) == 0 {
		t.Fatal("PDF inside zip is empty")
	}
	if !bytes.Contains(pdf, []byte("%PDF-1.4")) {
		t.Fatal("PDF inside zip missing header")
	}

	resumo := string(files["relatorio-contabil-resumo-2026-07.csv"])
	if !strings.Contains(resumo, "periodo,workspace,tipo,resultado_operacional_centavos,caixa_realizado_centavos,movimentos_confirmados,retiradas_distribuicao_centavos,investimentos_patrimonial_centavos,saldo_total_reconstruido_centavos,saldo_referencia") {
		t.Fatalf("resumo CSV missing header:\n%s", resumo)
	}
	if !strings.Contains(resumo, "1222000") {
		t.Fatalf("resumo CSV missing total reconstructed balance:\n%s", resumo)
	}
	macros := string(files["relatorio-contabil-macros-2026-07.csv"])
	if !strings.Contains(macros, "periodo,base,macro_group,natureza,total_centavos,percentual,observacao") {
		t.Fatalf("macros CSV missing header:\n%s", macros)
	}
	saldos := string(files["relatorio-contabil-saldos-2026-07.csv"])
	if !strings.Contains(saldos, "periodo,referencia,conta,tipo_conta,saldo_reconstruido_centavos,arquivada") {
		t.Fatalf("saldos CSV missing header:\n%s", saldos)
	}
	if !strings.Contains(saldos, "31/07/2026") || !strings.Contains(saldos, "Conta Movimento Smoke") {
		t.Fatalf("saldos CSV missing expected data:\n%s", saldos)
	}
	if !strings.Contains(saldos, "TOTAL,TOTAL,1222000,false") {
		t.Fatalf("saldos CSV missing TOTAL row:\n%s", saldos)
	}
}

func TestHandleExportarContabilZIP_PreservaMesAno(t *testing.T) {
	handler, _ := newContabilCSVBusinessHandler(t)

	req := httptest.NewRequest(http.MethodGet, "/relatorios/contabil.zip?mes=7&ano=2026", nil)
	rr := httptest.NewRecorder()
	handler.HandleExportarContabilZIP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rr.Code)
	}
	if got := rr.Header().Get("Content-Disposition"); !strings.Contains(got, `filename="relatorio-contabil-2026-07.zip"`) {
		t.Fatalf("Content-Disposition = %q, want 2026-07", got)
	}
	files := readContabilZIP(t, rr.Body.Bytes())
	if _, ok := files["relatorio-contabil-resumo-2026-07.csv"]; !ok {
		t.Fatal("zip should contain 2026-07 filenames")
	}
}

func TestHandleExportarContabilZIP_PersonalForbidden(t *testing.T) {
	handler := newContabilCSVPersonalHandler(t)
	req := httptest.NewRequest(http.MethodGet, "/relatorios/contabil.zip?mes=8&ano=2026", nil)
	rr := httptest.NewRecorder()
	handler.HandleExportarContabilZIP(rr, req)
	if rr.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", rr.Code)
	}
}

func TestHandleExportarContabilZIP_WorkspaceIsolation(t *testing.T) {
	handler, _ := newContabilCSVBusinessHandler(t)
	handler.WorkspaceID = "ws-nonexistent"

	req := httptest.NewRequest(http.MethodGet, "/relatorios/contabil.zip?month=2026-07", nil)
	rr := httptest.NewRecorder()
	handler.HandleExportarContabilZIP(rr, req)

	if rr.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403 for isolated workspace", rr.Code)
	}
	if strings.Contains(rr.Body.String(), "Conta Movimento Smoke") {
		t.Fatal("workspace isolation leak: other workspace account exposed")
	}
}

func TestHandleExportarContabilZIP_IndividualExportsStillWork(t *testing.T) {
	handler, _ := newContabilCSVBusinessHandler(t)

	for _, tc := range []struct {
		url         string
		contentType string
	}{
		{"/relatorios/contabil.pdf?month=2026-07", "application/pdf"},
		{"/relatorios/contabil-resumo.csv?month=2026-07", "text/csv; charset=utf-8"},
		{"/relatorios/contabil-macros.csv?month=2026-07", "text/csv; charset=utf-8"},
		{"/relatorios/contabil-saldos.csv?month=2026-07", "text/csv; charset=utf-8"},
	} {
		req := httptest.NewRequest(http.MethodGet, tc.url, nil)
		rr := httptest.NewRecorder()
		switch {
		case strings.Contains(tc.url, "contabil.pdf"):
			handler.HandleExportarContabilPDF(rr, req)
		case strings.Contains(tc.url, "resumo"):
			handler.HandleExportarContabilResumoCSV(rr, req)
		case strings.Contains(tc.url, "macros"):
			handler.HandleExportarContabilMacrosCSV(rr, req)
		case strings.Contains(tc.url, "saldos"):
			handler.HandleExportarContabilSaldosCSV(rr, req)
		}
		if rr.Code != http.StatusOK {
			t.Fatalf("%s: status = %d, want 200", tc.url, rr.Code)
		}
		if got := rr.Header().Get("Content-Type"); got != tc.contentType {
			t.Fatalf("%s: Content-Type = %q, want %q", tc.url, got, tc.contentType)
		}
	}
}
