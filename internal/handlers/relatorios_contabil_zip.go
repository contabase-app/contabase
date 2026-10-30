package handlers

import (
	"archive/zip"
	"bytes"
	"fmt"
	"net/http"
	"time"
)

func (h *RelatoriosHandler) HandleExportarContabilZIP(w http.ResponseWriter, r *http.Request) {
	if !h.requireReportsPermission(w) {
		return
	}
	mes, ano, err := parseDREPDFPeriod(r)
	if err != nil {
		http.Error(w, "periodo invalido", http.StatusBadRequest)
		return
	}

	data, err := h.buildRelatoriosDataWithModo("", "contabil", "movimentos", "realizado", mes, ano)
	if err != nil {
		http.Error(w, "erro ao gerar zip", http.StatusInternalServerError)
		return
	}

	if !data.IsBusiness {
		http.Error(w, "relatorio contabil disponivel apenas para workspaces business", http.StatusForbidden)
		return
	}

	identity, err := h.queryWorkspaceReportIdentity()
	if err != nil {
		http.Error(w, "erro ao carregar workspace", http.StatusInternalServerError)
		return
	}

	pdfBytes, err := buildContabilPDFReport(data, identity, h.WorkspaceID, mes, ano, appTime(time.Now()))
	if err != nil {
		http.Error(w, "erro ao gerar zip", http.StatusInternalServerError)
		return
	}

	periodo := fmt.Sprintf("%04d-%02d", ano, mes)
	workspace := queryWorkspaceName(h.DB, h.WorkspaceID)

	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)

	pdfEntry, err := zw.Create(fmt.Sprintf("relatorio-contabil-%s.pdf", periodo))
	if err == nil {
		_, err = pdfEntry.Write(pdfBytes)
	}
	if err != nil {
		http.Error(w, "erro ao gerar zip", http.StatusInternalServerError)
		return
	}

	resumoEntry, err := zw.Create(fmt.Sprintf("relatorio-contabil-resumo-%s.csv", periodo))
	if err != nil {
		http.Error(w, "erro ao gerar zip", http.StatusInternalServerError)
		return
	}
	writeContabilResumoCSVBody(resumoEntry, data, periodo, workspace)

	macrosEntry, err := zw.Create(fmt.Sprintf("relatorio-contabil-macros-%s.csv", periodo))
	if err != nil {
		http.Error(w, "erro ao gerar zip", http.StatusInternalServerError)
		return
	}
	writeContabilMacrosCSVBody(macrosEntry, data, periodo)

	saldosEntry, err := zw.Create(fmt.Sprintf("relatorio-contabil-saldos-%s.csv", periodo))
	if err != nil {
		http.Error(w, "erro ao gerar zip", http.StatusInternalServerError)
		return
	}
	writeContabilSaldosCSVBody(saldosEntry, data, periodo)

	if err := zw.Close(); err != nil {
		http.Error(w, "erro ao gerar zip", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/zip")
	w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="relatorio-contabil-%s.zip"`, periodo))
	w.Header().Set("X-Content-Type-Options", "nosniff")
	_, _ = w.Write(buf.Bytes())
}
