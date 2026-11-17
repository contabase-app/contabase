package handlers

import (
	"strings"
	"testing"
	"text/template"
)

func TestReciboModernLayoutPreservesFinancialContentAndFallback(t *testing.T) {
	tpl := template.New("recibo").Funcs(template.FuncMap{
		"assetPath": func(path string) string { return path + "?v=test" },
	})
	if _, err := tpl.Parse(readTestFile(t, "templates/pages/recibo.html")); err != nil {
		t.Fatalf("parse receipt: %v", err)
	}

	data := TransacaoReciboData{
		TransactionID:     "tx-123",
		WorkspaceName:     "Empresa Exemplo A",
		WorkspaceDocument: "00.000.000/0000-00",
		WorkspacePhone:    "(00) 0000-0000",
		WorkspaceAddress:  "Rua Exemplo A, 100",
		WorkspaceInitials: "EA",
		ContactName:       "Contato Exemplo A",
		ContactDocument:   "000.000.000-00",
		Description:       "Serviço prestado",
		AmountLabel:       "R$ 1.234,56",
		DateLabel:         "10/11/2026",
		IssueDateLabel:    "12/11/2026",
		DeclarationVerb:   "recebemos",
		DeclarationPrep:   "DE",
		CanPrint:          true,
	}
	var out strings.Builder
	if err := tpl.ExecuteTemplate(&out, "transacao-recibo-page", data); err != nil {
		t.Fatalf("render receipt: %v", err)
	}
	body := out.String()

	for _, want := range []string{
		`class="brand-fallback"`,
		"Comprovante de transação",
		"Valor do recibo",
		"R$ 1.234,56",
		"Contato Exemplo A",
		"000.000.000-00",
		"Serviço prestado",
		"10/11/2026",
		"12/11/2026",
		"#tx-123",
		"Emitente",
		"Contraparte",
		"recebemos",
		"Imprimir Recibo",
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("receipt missing %q", want)
		}
	}

	withLogo := data
	withLogo.WorkspaceLogoLightURL = "/uploads/workspaces/ws/logo.png"
	out.Reset()
	if err := tpl.ExecuteTemplate(&out, "transacao-recibo-page", withLogo); err != nil {
		t.Fatalf("render receipt with logo: %v", err)
	}
	logoBody := out.String()
	for _, want := range []string{
		`src="/uploads/workspaces/ws/logo.png"`,
		`data-receipt-logo`,
		`data-avatar-text`,
		`class="brand-fallback receipt-hidden"`,
	} {
		if !strings.Contains(logoBody, want) {
			t.Fatalf("receipt logo fallback missing %q", want)
		}
	}

	css := readTestFile(t, "assets/css/recibo.css")
	if !strings.Contains(css, "@media print") || !strings.Contains(css, ".actions { display: none !important; }") {
		t.Fatal("receipt print stylesheet must hide the screen-only actions")
	}
}
