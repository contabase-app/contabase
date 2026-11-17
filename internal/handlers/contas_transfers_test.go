package handlers

import (
	"database/sql"
	"html/template"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/contabase-app/contabase/internal/models"
)

func seedContasTransferScenario(t *testing.T, db *sql.DB) {
	t.Helper()
	seedInvoicePaymentScenario(t, db)

	execTestSQL(t, db, `
		INSERT INTO accounts (id, workspace_id, name, type, initial_balance, current_balance, created_at, updated_at)
		VALUES ('checking-extra', 'ws-test', 'Conta Extra', 'CHECKING', 0, 0, strftime('%s','now'), strftime('%s','now'))
	`)

	insertScheduledPaymentTestTransaction(t, db, "tx-transfer-installment", models.TransactionTypeTransfer, "checking-test", "checking-extra", "pending", nil)
	execTestSQL(t, db, `UPDATE transactions SET description='Transferência parcelada', date=?, due_date=?, installment_number=2, total_installments=3 WHERE id='tx-transfer-installment'`, testUnixDate("2026-07-10"), testUnixDate("2026-08-20"))

	execTestSQL(t, db, `
		INSERT INTO recurring_rules (id, workspace_id, user_id, account_id, destination_account_id, type, amount, description, start_date, frequency)
		VALUES ('rule-transfer', 'ws-test', 'user-test', 'checking-test', 'checking-extra', 'TRANSFER', 5000, 'Transferência recorrente', ?, 'MONTHLY')
	`, testUnixDate("2026-07-15"))
	insertScheduledPaymentTestTransaction(t, db, "tx-transfer-recurring", models.TransactionTypeTransfer, "checking-test", "checking-extra", "pending", nil)
	execTestSQL(t, db, `UPDATE transactions SET description='Transferência recorrente', date=?, due_date=NULL, recurring_rule_id='rule-transfer', recurrence_sequence=2 WHERE id='tx-transfer-recurring'`, testUnixDate("2026-08-15"))

	insertScheduledPaymentTestTransaction(t, db, "tx-transfer-paid", models.TransactionTypeTransfer, "checking-test", "checking-extra", "paid", ptrInt64(testUnixDate("2026-07-12")))
	execTestSQL(t, db, `UPDATE transactions SET description='Transferência paga', date=? WHERE id='tx-transfer-paid'`, testUnixDate("2026-07-12"))

	insertScheduledPaymentTestTransaction(t, db, "tx-transfer-june", models.TransactionTypeTransfer, "checking-test", "checking-extra", "pending", nil)
	execTestSQL(t, db, `UPDATE transactions SET description='Transferência de junho', date=?, due_date=NULL WHERE id='tx-transfer-june'`, testUnixDate("2026-06-30"))

	insertScheduledPaymentTestTransaction(t, db, "tx-transfer-august", models.TransactionTypeTransfer, "checking-test", "checking-extra", "pending", nil)
	execTestSQL(t, db, `UPDATE transactions SET description='Transferência de agosto', date=?, due_date=NULL WHERE id='tx-transfer-august'`, testUnixDate("2026-08-01"))

	insertScheduledPaymentTestTransaction(t, db, "tx-expense-pending", models.TransactionTypeExpense, "checking-test", "", "pending", nil)
	execTestSQL(t, db, `UPDATE transactions SET description='Despesa pendente', amount=10000, date=? WHERE id='tx-expense-pending'`, testUnixDate("2026-07-05"))

	insertScheduledPaymentTestTransaction(t, db, "tx-income-pending", models.TransactionTypeIncome, "checking-test", "", "pending", nil)
	execTestSQL(t, db, `UPDATE transactions SET description='Receita pendente', amount=12345, date=? WHERE id='tx-income-pending'`, testUnixDate("2026-07-06"))

	execTestSQL(t, db, `
		INSERT INTO workspaces (id, name, description, created_at, updated_at)
		VALUES ('ws-other', 'Outro Workspace', '', strftime('%s','now'), strftime('%s','now'))
	`)
	execTestSQL(t, db, `
		INSERT INTO accounts (id, workspace_id, name, type, provider_slug, color, initial_balance, current_balance, created_at, updated_at)
		VALUES ('other-acc', 'ws-other', 'Conta Outro WS', 'CHECKING', 'itau', '#EC7000', 0, 0, strftime('%s','now'), strftime('%s','now'))
	`)
	execTestSQL(t, db, `
		INSERT INTO transactions (id, workspace_id, user_id, account_id, destination_account_id, type, amount, date, description, status, installment_number, total_installments, created_at, updated_at)
		VALUES ('tx-other-transfer', 'ws-other', 'user-test', 'other-acc', NULL, 'TRANSFER', 5000, ?, 'Transferência outro workspace', 'pending', 1, 1, strftime('%s','now'), strftime('%s','now'))
	`, testUnixDate("2026-07-10"))

	insertScheduledPaymentTestTransaction(t, db, "tx-transfer-cross-dest", models.TransactionTypeTransfer, "checking-test", "other-acc", "pending", nil)
	execTestSQL(t, db, `UPDATE transactions SET description='Transferência destino externo', date=?, due_date=NULL WHERE id='tx-transfer-cross-dest'`, testUnixDate("2026-07-20"))
}

func contasTransferTestTemplate(t *testing.T) *template.Template {
	t.Helper()
	contasBytes, err := os.ReadFile(filepath.Join(projectRoot(), "templates/pages/contas.html"))
	if err != nil {
		t.Fatalf("read contas template: %v", err)
	}
	stub := `{{define "seletor_meses"}}<div id="monthSelector">seletor</div>{{end}}`
	return template.Must(template.New("contas-transfers").Funcs(template.FuncMap{
		"assetPath": func(path string) string { return path },
	}).Parse(string(contasBytes) + "\n" + stub))
}

func TestQueryPendingTransfersFilteredIsolatesWorkspaceStatusAndPeriod(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	seedContasTransferScenario(t, db)

	augustRows, err := queryPendingTransfersFiltered(db, "ws-test", 8, 2026, "")
	if err != nil {
		t.Fatalf("query August pending transfers: %v", err)
	}
	augustDescriptions := contasPayableDescriptions(augustRows)
	for _, want := range []string{"Transferência parcelada", "Transferência recorrente", "Transferência de agosto"} {
		if !contasContains(augustDescriptions, want) {
			t.Errorf("August missing %q; got %v", want, augustDescriptions)
		}
	}
	for _, notWant := range []string{"Transferência paga", "Transferência de junho", "Transferência destino externo", "Despesa pendente", "Receita pendente", "Transferência outro workspace"} {
		if contasContains(augustDescriptions, notWant) {
			t.Errorf("August unexpectedly contains %q; got %v", notWant, augustDescriptions)
		}
	}

	installment, ok := contasFindByDesc(augustRows, "Transferência parcelada")
	if !ok {
		t.Fatal("installment transfer not found in its due month")
	}
	if !installment.IsTransfer || !installment.HasInstallmentInfo || installment.InstallmentNumber != 2 || installment.TotalInstallments != 3 {
		t.Fatalf("installment flags = transfer:%v info:%v number:%d total:%d", installment.IsTransfer, installment.HasInstallmentInfo, installment.InstallmentNumber, installment.TotalInstallments)
	}
	if installment.AccountName != "Conta Teste" || installment.DestinationAccountName != "Conta Extra" {
		t.Fatalf("accounts = %q -> %q", installment.AccountName, installment.DestinationAccountName)
	}
	if installment.DateLabel != "20/08/2026" {
		t.Fatalf("date label = %q, want 20/08/2026 (due_date)", installment.DateLabel)
	}
	if !installment.IsOverdue || installment.StatusTone != "danger" || installment.StatusTitle != "Transferência atrasada" {
		t.Fatalf("overdue status = overdue:%v tone:%q title:%q", installment.IsOverdue, installment.StatusTone, installment.StatusTitle)
	}
	if installment.Amount != 12345 {
		t.Fatalf("amount = %d, want 12345", installment.Amount)
	}

	recurring, ok := contasFindByDesc(augustRows, "Transferência recorrente")
	if !ok || !recurring.IsRecurring {
		t.Fatalf("recurring transfer = %+v, found=%v", recurring, ok)
	}
	if recurring.DateLabel != "15/08/2026" {
		t.Fatalf("recurring date label = %q, want 15/08/2026", recurring.DateLabel)
	}

	julyRows, err := queryPendingTransfersFiltered(db, "ws-test", 7, 2026, "")
	if err != nil {
		t.Fatalf("query July pending transfers: %v", err)
	}
	if len(julyRows) != 1 || julyRows[0].Description != "Transferência destino externo" {
		t.Fatalf("July rows = %v, want only the cross-destination transfer", contasPayableDescriptions(julyRows))
	}
	if julyRows[0].DestinationAccountName != "" {
		t.Fatalf("cross-workspace destination leaked: %q", julyRows[0].DestinationAccountName)
	}
}

func TestQueryPendingTransfersFilteredSearchAndMonthBoundaries(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	seedContasTransferScenario(t, db)

	for _, tt := range []struct {
		name  string
		mes   int
		ano   int
		query string
		want  int
	}{
		{name: "July description", mes: 7, ano: 2026, query: "parcelada", want: 0},
		{name: "August description", mes: 8, ano: 2026, query: "parcelada", want: 1},
		{name: "July origin", mes: 7, ano: 2026, query: "Conta Teste", want: 1},
		{name: "August origin", mes: 8, ano: 2026, query: "Conta Teste", want: 3},
		{name: "July destination", mes: 7, ano: 2026, query: "Conta Extra", want: 0},
		{name: "August destination", mes: 8, ano: 2026, query: "Conta Extra", want: 3},
		{name: "no result", mes: 7, ano: 2026, query: "inexistente", want: 0},
	} {
		t.Run(tt.name, func(t *testing.T) {
			rows, err := queryPendingTransfersFiltered(db, "ws-test", tt.mes, tt.ano, tt.query)
			if err != nil {
				t.Fatalf("query: %v", err)
			}
			if len(rows) != tt.want {
				t.Fatalf("rows = %d, want %d (%v)", len(rows), tt.want, contasPayableDescriptions(rows))
			}
		})
	}

	all, err := queryPendingTransfersFiltered(db, "ws-test", 0, 0, "")
	if err != nil {
		t.Fatalf("query all: %v", err)
	}
	if len(all) != 5 {
		t.Fatalf("all rows = %d, want 5 (installment, recurring, cross-dest, june, august)", len(all))
	}
}

func TestQueryPendingTransfersFilteredIncludesArchivedDestination(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	seedContasTransferScenario(t, db)
	execTestSQL(t, db, `UPDATE accounts SET archived_at = ? WHERE id = 'checking-extra'`, time.Now().Unix())

	rows, err := queryPendingTransfersFiltered(db, "ws-test", 8, 2026, "")
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	row, ok := contasFindByDesc(rows, "Transferência parcelada")
	if !ok {
		t.Fatal("archived destination row not found")
	}
	if row.DestinationAccountName != "Conta Extra" {
		t.Fatalf("archived destination name = %q, want Conta Extra", row.DestinationAccountName)
	}
}

func TestQueryPendingTransfersFilteredFutureIsPendingNotOverdue(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	seedInvoicePaymentScenario(t, db)
	execTestSQL(t, db, `
		INSERT INTO accounts (id, workspace_id, name, type, initial_balance, current_balance, created_at, updated_at)
		VALUES ('future-dest', 'ws-test', 'Conta Futura', 'CHECKING', 0, 0, strftime('%s','now'), strftime('%s','now'))
	`)
	now := time.Now()
	future := now.AddDate(0, 0, 3)
	insertScheduledPaymentTestTransaction(t, db, "tx-transfer-future", models.TransactionTypeTransfer, "checking-test", "future-dest", "pending", nil)
	execTestSQL(t, db, `UPDATE transactions SET description='Transferência futura', date=?, due_date=NULL WHERE id='tx-transfer-future'`, testUnixDate(future.Format("2006-01-02")))

	rows, err := queryPendingTransfersFiltered(db, "ws-test", 0, 0, "")
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	row, ok := contasFindByDesc(rows, "Transferência futura")
	if !ok {
		t.Fatal("future transfer not found")
	}
	if row.IsOverdue || row.StatusTone != "warning" || row.StatusTitle != "Transferência pendente" {
		t.Fatalf("future status = overdue:%v tone:%q title:%q", row.IsOverdue, row.StatusTone, row.StatusTitle)
	}
}

func TestQueryPendingTransfersFilteredBusinessUsesDueDateSemantics(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	seedInvoicePaymentScenario(t, db)
	execTestSQL(t, db, `UPDATE workspaces SET type = 'business' WHERE id = 'ws-test'`)
	execTestSQL(t, db, `
		INSERT INTO accounts (id, workspace_id, name, type, initial_balance, current_balance, created_at, updated_at)
		VALUES ('business-dest', 'ws-test', 'Conta Business Destino', 'CHECKING', 0, 0, strftime('%s','now'), strftime('%s','now'))
	`)

	now := time.Now()
	futureDate := now.AddDate(0, -2, 0)
	futureDue := now.AddDate(0, 2, 0)
	overdueDate := now.AddDate(0, -4, 0)
	overdueDue := now.AddDate(0, -1, 0)

	insertScheduledPaymentTestTransaction(t, db, "tx-business-future", models.TransactionTypeTransfer, "checking-test", "business-dest", "pending", nil)
	execTestSQL(t, db, `UPDATE transactions SET description='Transferência Business futura', date=?, due_date=? WHERE id='tx-business-future'`, testUnixDate(futureDate.Format("2006-01-02")), testUnixDate(futureDue.Format("2006-01-02")))

	insertScheduledPaymentTestTransaction(t, db, "tx-business-overdue", models.TransactionTypeTransfer, "checking-test", "business-dest", "pending", nil)
	execTestSQL(t, db, `UPDATE transactions SET description='Transferência Business vencida', date=?, due_date=? WHERE id='tx-business-overdue'`, testUnixDate(overdueDate.Format("2006-01-02")), testUnixDate(overdueDue.Format("2006-01-02")))

	futureRows, err := queryPendingTransfersFiltered(db, "ws-test", int(futureDue.Month()), futureDue.Year(), "")
	if err != nil {
		t.Fatalf("query future due month: %v", err)
	}
	future, ok := contasFindByDesc(futureRows, "Transferência Business futura")
	if !ok {
		t.Fatalf("future transfer not found in due month; rows=%v", contasPayableDescriptions(futureRows))
	}
	if future.DateLabel != futureDue.Format("02/01/2006") {
		t.Fatalf("future date label = %q, want %q", future.DateLabel, futureDue.Format("02/01/2006"))
	}
	if future.IsOverdue || future.StatusTone != "warning" || future.StatusTitle != "Transferência pendente" {
		t.Fatalf("future status = overdue:%v tone:%q title:%q", future.IsOverdue, future.StatusTone, future.StatusTitle)
	}

	dateMonthRows, err := queryPendingTransfersFiltered(db, "ws-test", int(futureDate.Month()), futureDate.Year(), "")
	if err != nil {
		t.Fatalf("query future competence month: %v", err)
	}
	if _, ok := contasFindByDesc(dateMonthRows, "Transferência Business futura"); ok {
		t.Fatal("future transfer appeared in its date month instead of due month")
	}

	overdueRows, err := queryPendingTransfersFiltered(db, "ws-test", int(overdueDue.Month()), overdueDue.Year(), "")
	if err != nil {
		t.Fatalf("query overdue due month: %v", err)
	}
	overdue, ok := contasFindByDesc(overdueRows, "Transferência Business vencida")
	if !ok {
		t.Fatalf("overdue transfer not found in due month; rows=%v", contasPayableDescriptions(overdueRows))
	}
	if overdue.DateLabel != overdueDue.Format("02/01/2006") {
		t.Fatalf("overdue date label = %q, want %q", overdue.DateLabel, overdueDue.Format("02/01/2006"))
	}
	if !overdue.IsOverdue || overdue.StatusTone != "danger" || overdue.StatusTitle != "Transferência atrasada" {
		t.Fatalf("overdue status = overdue:%v tone:%q title:%q", overdue.IsOverdue, overdue.StatusTone, overdue.StatusTitle)
	}

	overdueDateMonthRows, err := queryPendingTransfersFiltered(db, "ws-test", int(overdueDate.Month()), overdueDate.Year(), "")
	if err != nil {
		t.Fatalf("query overdue competence month: %v", err)
	}
	if _, ok := contasFindByDesc(overdueDateMonthRows, "Transferência Business vencida"); ok {
		t.Fatal("overdue transfer appeared in its date month instead of due month")
	}
}

func TestContasTransferTabRendersPendingTransfers(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	seedContasTransferScenario(t, db)

	handler := ContasHandler{
		DB:          db,
		Templates:   contasTransferTestTemplate(t),
		WorkspaceID: "ws-test",
		UserID:      "user-test",
	}
	req := httptest.NewRequest(http.MethodGet, "/contas?periodo=2026-08&aba=transferir&partial=content", nil)
	rr := httptest.NewRecorder()
	handler.HandleContasConceito(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rr.Code, rr.Body.String())
	}
	body := rr.Body.String()
	for _, want := range []string{
		`A Transferir`,
		`id="transfer-tx-transfer-installment"`,
		`id="tx-tx-transfer-installment"`,
		`Transferência parcelada`,
		`Transferência recorrente`,
		`Transferência de agosto`,
		`Conta Teste`,
		`Conta Extra`,
		`R$ 123,45`,
		`20/08/2026`,
		`15/08/2026`,
		`Atrasada`,
		`2/3`,
		`Recorrente`,
		`data-sensitive`,
		`is-transfer`,
		`data-payment-confirm`,
		`data-payment-type="TRANSFER"`,
		`data-payment-destination-account-name="Conta Extra"`,
		`data-swipe-row="true"`,
		`data-swipe-left-url="/transacoes/tx-transfer-installment?escopo=single"`,
		`data-swipe-left-method="DELETE"`,
		`data-swipe-left-target="#transfer-tx-transfer-installment"`,
		`data-swipe-left-swap="delete"`,
		`data-swipe-left-confirm="Remover esta transferência?"`,
		`data-swipe-right-payment-confirm="true"`,
		`hx-get="/transacoes/tx-transfer-installment"`,
		`Confirmar transferência`,
		`Editar transferência`,
		`cb-v2-swipe-hint`,
		`confirmar a transferência`,
		`para excluir`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("body missing %q", want)
		}
	}
	for _, notWant := range []string{
		`Transferência paga`,
		`Transferência destino externo`,
		`Transferência de junho`,
		`Despesa pendente`,
		`Receita pendente`,
		`Transferência outro workspace`,
		`data-swipe-left-payment-confirm`,
		`is-expense`,
		`is-income`,
	} {
		if strings.Contains(body, notWant) {
			t.Errorf("body unexpectedly contains %q", notWant)
		}
	}
	replaceURL := rr.Header().Get("HX-Replace-Url")
	if !strings.Contains(replaceURL, "aba=transferir") {
		t.Errorf("HX-Replace-Url = %q, want aba=transferir", replaceURL)
	}
}

func TestContasTransferTabPartialListAndSearch(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	seedContasTransferScenario(t, db)

	handler := ContasHandler{
		DB:          db,
		Templates:   contasTransferTestTemplate(t),
		WorkspaceID: "ws-test",
		UserID:      "user-test",
	}

	req := httptest.NewRequest(http.MethodGet, "/contas?periodo=2026-08&aba=transferir&partial=lista", nil)
	rr := httptest.NewRecorder()
	handler.HandleContasConceito(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rr.Code)
	}
	body := rr.Body.String()
	for _, want := range []string{`id="contas-list"`, `value="transferir"`, `A Transferir`, `id="transfer-tx-transfer-installment"`, `Transferência parcelada`, `data-payment-confirm`, `data-swipe-row="true"`} {
		if !strings.Contains(body, want) {
			t.Errorf("partial body missing %q", want)
		}
	}
	for _, notWant := range []string{`id="tx-expense-pending"`} {
		if strings.Contains(body, notWant) {
			t.Errorf("partial body unexpectedly contains %q", notWant)
		}
	}
	if !strings.Contains(rr.Header().Get("HX-Replace-Url"), "aba=transferir") {
		t.Errorf("HX-Replace-Url = %q, want aba=transferir", rr.Header().Get("HX-Replace-Url"))
	}

	searchReq := httptest.NewRequest(http.MethodGet, "/contas?periodo=2026-08&aba=transferir&partial=lista&q=Conta+Extra", nil)
	searchRR := httptest.NewRecorder()
	handler.HandleContasConceito(searchRR, searchReq)
	if searchRR.Code != http.StatusOK {
		t.Fatalf("search status = %d, want 200", searchRR.Code)
	}
	searchBody := searchRR.Body.String()
	if !strings.Contains(searchBody, "Transferência parcelada") || !strings.Contains(searchBody, "Transferência recorrente") {
		t.Fatalf("search body missing expected transfers: %s", searchBody)
	}
	if !strings.Contains(searchRR.Header().Get("HX-Replace-Url"), "q=Conta+Extra") {
		t.Errorf("search HX-Replace-Url = %q, want q preserved", searchRR.Header().Get("HX-Replace-Url"))
	}

	emptyReq := httptest.NewRequest(http.MethodGet, "/contas?periodo=2026-08&aba=transferir&partial=lista&q=inexistente", nil)
	emptyRR := httptest.NewRecorder()
	handler.HandleContasConceito(emptyRR, emptyReq)
	if emptyRR.Code != http.StatusOK {
		t.Fatalf("empty search status = %d, want 200", emptyRR.Code)
	}
	if !strings.Contains(emptyRR.Body.String(), "Nenhum resultado") {
		t.Fatalf("empty search missing empty state: %s", emptyRR.Body.String())
	}
}

func TestContasPayablesTabDoesNotShowTransfers(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	seedContasTransferScenario(t, db)

	handler := ContasHandler{
		DB:          db,
		Templates:   contasTransferTestTemplate(t),
		WorkspaceID: "ws-test",
		UserID:      "user-test",
	}
	req := httptest.NewRequest(http.MethodGet, "/contas?periodo=2026-07&aba=pagar&partial=lista", nil)
	rr := httptest.NewRecorder()
	handler.HandleContasConceito(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rr.Code)
	}
	body := rr.Body.String()
	if !strings.Contains(body, "Despesa pendente") {
		t.Fatalf("payables tab missing pending expense: %s", body)
	}
	if strings.Contains(body, "Transferência parcelada") || strings.Contains(body, `id="transfer-`) {
		t.Fatalf("payables tab unexpectedly contains transfer: %s", body)
	}
}

func TestContasTransferTabDoesNotChangeKpiTotals(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	seedContasTransferScenario(t, db)

	tpl := template.Must(template.New("contas-kpi").Parse(`
{{define "contas-body"}}{{.TotalPayables.Reais}}|{{.TotalReceivables.Reais}}|{{.TotalOverdueReceivabs.Reais}}{{end}}
`))
	handler := ContasHandler{
		DB:          db,
		Templates:   tpl,
		WorkspaceID: "ws-test",
		UserID:      "user-test",
	}
	req := httptest.NewRequest(http.MethodGet, "/contas?periodo=2026-07&aba=transferir&partial=content", nil)
	rr := httptest.NewRecorder()
	handler.HandleContasConceito(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rr.Code)
	}
	body := strings.TrimSpace(rr.Body.String())
	if body != "100|123|123" {
		t.Fatalf("KPI totals = %q, want 100|123|123 (transfer must not be summed)", body)
	}
}

func TestContasTransferTabSearchDoesNotChangeKpiTotals(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	seedContasTransferScenario(t, db)

	tpl := template.Must(template.New("contas-kpi-search").Parse(`
{{define "contas-body"}}{{.TotalPayables.Reais}}|{{.TotalReceivables.Reais}}|{{.TotalOverdueReceivabs.Reais}}{{end}}
`))
	handler := ContasHandler{
		DB:          db,
		Templates:   tpl,
		WorkspaceID: "ws-test",
		UserID:      "user-test",
	}
	req := httptest.NewRequest(http.MethodGet, "/contas?periodo=2026-07&aba=transferir&partial=content&q=destino+externo", nil)
	rr := httptest.NewRecorder()
	handler.HandleContasConceito(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rr.Code)
	}
	body := strings.TrimSpace(rr.Body.String())
	if body != "100|123|123" {
		t.Fatalf("KPI totals with transfer search = %q, want 100|123|123 (search must not filter KPIs)", body)
	}
	replaceURL := rr.Header().Get("HX-Replace-Url")
	if !strings.Contains(replaceURL, "aba=transferir") || !strings.Contains(replaceURL, "q=destino+externo") {
		t.Fatalf("HX-Replace-Url = %q, want aba and q preserved", replaceURL)
	}
}

func TestContasPayablesSearchStillFiltersKpis(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	seedContasTransferScenario(t, db)

	tpl := template.Must(template.New("contas-kpi-pagar").Parse(`
{{define "contas-body"}}{{.TotalPayables.Reais}}|{{.TotalReceivables.Reais}}|{{.TotalOverdueReceivabs.Reais}}{{end}}
`))
	handler := ContasHandler{
		DB:          db,
		Templates:   tpl,
		WorkspaceID: "ws-test",
		UserID:      "user-test",
	}

	for _, tt := range []struct {
		name  string
		aba   string
		query string
		want  string
	}{
		{name: "pagar no match", aba: "pagar", query: "inexistente", want: "0|0|0"},
		{name: "pagar payable match", aba: "pagar", query: "Despesa+pendente", want: "100|0|0"},
		{name: "receber no match", aba: "receber", query: "inexistente", want: "0|0|0"},
		{name: "receber receivable match", aba: "receber", query: "Receita+pendente", want: "0|123|123"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "/contas?periodo=2026-07&aba="+tt.aba+"&partial=content&q="+tt.query, nil)
			rr := httptest.NewRecorder()
			handler.HandleContasConceito(rr, req)
			if rr.Code != http.StatusOK {
				t.Fatalf("status = %d, want 200", rr.Code)
			}
			if body := strings.TrimSpace(rr.Body.String()); body != tt.want {
				t.Fatalf("KPI totals = %q, want %q (pagar/receber search must keep filtering KPIs)", body, tt.want)
			}
		})
	}
}

func TestContasTransferTabNullDestinationFallback(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	seedInvoicePaymentScenario(t, db)
	insertScheduledPaymentTestTransaction(t, db, "tx-transfer-null-dest", models.TransactionTypeTransfer, "checking-test", "", "pending", nil)
	execTestSQL(t, db, `UPDATE transactions SET description='Transferência sem destino', date=?, due_date=NULL WHERE id='tx-transfer-null-dest'`, testUnixDate("2026-07-10"))

	handler := ContasHandler{
		DB:          db,
		Templates:   contasTransferTestTemplate(t),
		WorkspaceID: "ws-test",
		UserID:      "user-test",
	}
	req := httptest.NewRequest(http.MethodGet, "/contas?periodo=2026-07&aba=transferir&partial=content", nil)
	rr := httptest.NewRecorder()
	handler.HandleContasConceito(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rr.Code)
	}
	body := rr.Body.String()
	for _, want := range []string{`Transferência sem destino`, `Destino não informado`, `id="transfer-tx-transfer-null-dest"`} {
		if !strings.Contains(body, want) {
			t.Errorf("null-destination body missing %q", want)
		}
	}
}

func TestContasTransferTabDoesNotRenderCrossWorkspaceDestinationMetadata(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	seedContasTransferScenario(t, db)

	handler := ContasHandler{
		DB:          db,
		Templates:   contasTransferTestTemplate(t),
		WorkspaceID: "ws-test",
		UserID:      "user-test",
	}
	req := httptest.NewRequest(http.MethodGet, "/contas?periodo=2026-07&aba=transferir&partial=content", nil)
	rr := httptest.NewRecorder()
	handler.HandleContasConceito(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rr.Code)
	}
	rows, err := queryPendingTransfersFiltered(db, "ws-test", 7, 2026, "")
	if err != nil {
		t.Fatalf("query cross-workspace transfer: %v", err)
	}
	cross, ok := contasFindByDesc(rows, "Transferência destino externo")
	if !ok {
		t.Fatal("cross-workspace transfer not found")
	}
	if cross.DestinationAccountName != "" || cross.DestinationProviderMark != "" || cross.DestinationProviderLogoPath != "" || cross.DestinationAccountColor != "" {
		t.Fatalf("cross-workspace destination fields not empty: name=%q mark=%q logo=%q color=%q",
			cross.DestinationAccountName, cross.DestinationProviderMark, cross.DestinationProviderLogoPath, cross.DestinationAccountColor)
	}

	body := rr.Body.String()
	rowStart := strings.Index(body, `id="transfer-tx-transfer-cross-dest"`)
	if rowStart < 0 {
		t.Fatal("cross-workspace transfer row not found")
	}
	rowEnd := strings.Index(body[rowStart:], `</article>`)
	if rowEnd < 0 {
		t.Fatal("cross-workspace transfer row end not found")
	}
	rowHTML := body[rowStart : rowStart+rowEnd]
	if !strings.Contains(rowHTML, "Transferência destino externo") || !strings.Contains(rowHTML, "Destino não informado") {
		t.Fatalf("cross-workspace transfer fallback missing from its row: %s", rowHTML)
	}
	if leak := crossWorkspaceDestinationMetadataLeak(rowHTML); leak != "" {
		t.Fatalf("cross-workspace destination metadata leaked into the transfer row: %q", leak)
	}

	syntheticMark := `<span class="ui-row__account-mark">Itaú</span>`
	if leak := crossWorkspaceDestinationMetadataLeak(syntheticMark); leak != "Itaú" {
		t.Fatalf("synthetic external mark was not detected by the assertion: %q", leak)
	}
}

func crossWorkspaceDestinationMetadataLeak(html string) string {
	for _, notWant := range []string{"Conta Outro WS", "itau", "#EC7000", "Itaú"} {
		if strings.Contains(html, notWant) {
			return notWant
		}
	}
	return ""
}

func TestContasTransferTabTemplateIndicatorsAndEmptyState(t *testing.T) {
	tpl := contasTransferTestTemplate(t)
	data := ContasData{
		Aba: "transferir",
		Transfers: []PendingItemRow{{
			ID:                          "tr-1",
			Description:                 "Transferência para reserva",
			Amount:                      12345,
			AmountMoney:                 MoneyMinor(12345),
			DateLabel:                   "20/08/2026",
			AccountName:                 "Conta Teste",
			AccountColor:                "#111111",
			ProviderMark:                "CT",
			DestinationAccountName:      "Conta Extra",
			DestinationAccountColor:     "#222222",
			DestinationProviderMark:     "CE",
			IsTransfer:                  true,
			IsRecurring:                 true,
			InstallmentNumber:           2,
			TotalInstallments:           3,
			HasInstallmentInfo:          true,
			StatusIcon:                  "arrow-left-right",
			StatusTone:                  "warning",
			StatusTitle:                 "Transferência pendente",
			PaymentAccountChangeAllowed: false,
		}},
	}
	var buf strings.Builder
	if err := tpl.ExecuteTemplate(&buf, "contas-list", data); err != nil {
		t.Fatalf("render contas-list: %v", err)
	}
	html := buf.String()
	for _, want := range []string{`class="ui-tab is-active"`, `aria-pressed="true"`} {
		if !strings.Contains(html, want) {
			t.Errorf("transfer tab missing %q", want)
		}
	}

	rowStart := strings.Index(html, `id="transfer-tr-1"`)
	if rowStart < 0 {
		t.Fatal("transfer row not found")
	}
	rowEnd := strings.Index(html[rowStart:], `</article>`)
	if rowEnd < 0 {
		t.Fatal("transfer row end not found")
	}
	rowHTML := html[rowStart : rowStart+rowEnd]
	for _, want := range []string{
		`id="transfer-tr-1"`,
		`id="tx-tr-1"`,
		`Transferência para reserva`,
		`Conta Teste`,
		`Conta Extra`,
		`R$ 123,45`,
		`20/08/2026`,
		`Pendente`,
		`2/3`,
		`Recorrente`,
		`ui-badge--neutral`,
		`ui-badge--accent`,
		`is-transfer`,
		`data-sensitive`,
		`data-payment-confirm`,
		`data-payment-type="TRANSFER"`,
		`data-payment-destination-account-name="Conta Extra"`,
		`data-swipe-row="true"`,
		`data-swipe-left-url="/transacoes/tr-1?escopo=single"`,
		`data-swipe-left-method="DELETE"`,
		`data-swipe-left-target="#transfer-tr-1"`,
		`data-swipe-left-swap="delete"`,
		`data-swipe-left-confirm="Remover esta transferência?"`,
		`data-swipe-right-payment-confirm="true"`,
		`hx-get="/transacoes/tr-1"`,
		`Confirmar transferência`,
		`Editar transferência`,
	} {
		if !strings.Contains(rowHTML, want) {
			t.Errorf("transfer row missing %q", want)
		}
	}
	if strings.Contains(rowHTML, "Atrasada") {
		t.Error("pending transfer row must not render Atrasada")
	}
	for _, notWant := range []string{`data-swipe-left-payment-confirm`, `hx-post`, `is-expense`, `is-income`} {
		if strings.Contains(rowHTML, notWant) {
			t.Errorf("transfer row unexpectedly contains %q", notWant)
		}
	}
	if !strings.Contains(html, `cb-v2-swipe-hint`) || !strings.Contains(html, `confirmar a transferência`) {
		t.Error("transfer tab must render its transfer-specific swipe hint")
	}

	empty := ContasData{Aba: "transferir"}
	buf.Reset()
	if err := tpl.ExecuteTemplate(&buf, "contas-list", empty); err != nil {
		t.Fatalf("render empty contas-list: %v", err)
	}
	emptyHTML := buf.String()
	for _, want := range []string{`Sem transferências pendentes`, `Nenhuma transferência a executar neste período`} {
		if !strings.Contains(emptyHTML, want) {
			t.Errorf("empty transfer state missing %q", want)
		}
	}
}

func TestContasTransferPrivacyMaskCoversSensitiveAmount(t *testing.T) {
	css, err := os.ReadFile(filepath.Join(projectRoot(), "assets/css/contas.css"))
	if err != nil {
		t.Fatalf("read contas css: %v", err)
	}
	for _, want := range []string{
		`.privacy-enabled .cb-v2-contas-main [data-sensitive]`,
		`.privacy-enabled .cb-v2-contas-main .ui-row__amount[data-sensitive]::after`,
	} {
		if !strings.Contains(string(css), want) {
			t.Errorf("contas.css missing privacy rule %q", want)
		}
	}
}

func TestContasTransferConfirmationProcessesPendingTransferOnce(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	seedContasTransferScenario(t, db)

	handler := testPaidInvoiceMutationHandler(db)
	post := func() *httptest.ResponseRecorder {
		rr := httptest.NewRecorder()
		handler.HandleTogglePagamento(rr, formPost("/transacoes/tx-transfer-installment/status-pagamento", url.Values{
			"context":        {"contas"},
			"payment_action": {"paid"},
			"payment_date":   {"2026-08-20"},
		}), "tx-transfer-installment")
		return rr
	}

	if rr := post(); rr.Code != http.StatusOK {
		t.Fatalf("first confirmation status = %d, body = %q", rr.Code, rr.Body.String())
	}
	assertPaymentAccountBalance(t, db, "checking-test", 87655)
	assertPaymentAccountBalance(t, db, "checking-extra", 12345)

	var status string
	if err := db.QueryRow(`SELECT status FROM transactions WHERE id = 'tx-transfer-installment'`).Scan(&status); err != nil {
		t.Fatalf("query transfer status: %v", err)
	}
	if status != "paid" {
		t.Fatalf("transfer status = %q, want paid", status)
	}

	// A repeated confirmation caused by a double tap or retry is the existing
	// idempotent no-op: it must not move balances or append another history row.
	if rr := post(); rr.Code != http.StatusOK {
		t.Fatalf("second confirmation status = %d, body = %q", rr.Code, rr.Body.String())
	}
	assertPaymentAccountBalance(t, db, "checking-test", 87655)
	assertPaymentAccountBalance(t, db, "checking-extra", 12345)
	var historyCount int
	if err := db.QueryRow(`SELECT COUNT(1) FROM transaction_status_history WHERE transaction_id = 'tx-transfer-installment'`).Scan(&historyCount); err != nil {
		t.Fatalf("count transfer status history: %v", err)
	}
	if historyCount != 1 {
		t.Fatalf("transfer status history rows = %d, want 1", historyCount)
	}

	pending, err := queryPendingTransfersFiltered(db, "ws-test", 8, 2026, "")
	if err != nil {
		t.Fatalf("query pending transfers after confirmation: %v", err)
	}
	if _, ok := contasFindByDesc(pending, "Transferência parcelada"); ok {
		t.Fatal("confirmed transfer still appears in the pending transfer list")
	}
}

func TestContasTransferSwipeDeleteRemovesOnlySelectedPendingTransfer(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	seedContasTransferScenario(t, db)

	handler := testPaidInvoiceMutationHandler(db)
	req := httptest.NewRequest(http.MethodDelete, "/transacoes/tx-transfer-installment?escopo=single", nil)
	req.Header.Set("HX-Current-URL", "http://localhost/contas")
	rr := httptest.NewRecorder()
	handler.HandleDeletarTransacao(rr, req, "tx-transfer-installment")
	if rr.Code != http.StatusOK {
		t.Fatalf("delete status = %d, body = %q", rr.Code, rr.Body.String())
	}

	var deleted int
	if err := db.QueryRow(`SELECT COUNT(1) FROM transactions WHERE id = 'tx-transfer-installment'`).Scan(&deleted); err != nil {
		t.Fatalf("count deleted transfer: %v", err)
	}
	if deleted != 0 {
		t.Fatal("selected pending transfer was not removed")
	}
	for _, id := range []string{"tx-transfer-recurring", "tx-transfer-august", "tx-transfer-june", "tx-other-transfer"} {
		var remaining int
		if err := db.QueryRow(`SELECT COUNT(1) FROM transactions WHERE id = ?`, id).Scan(&remaining); err != nil {
			t.Fatalf("count %s: %v", id, err)
		}
		if remaining != 1 {
			t.Fatalf("unrelated transfer %s removed or missing", id)
		}
	}
	// Pending transfers have no realized balance effect; deletion must preserve
	// both sides of the account pair exactly as they were.
	assertPaymentAccountBalance(t, db, "checking-test", 100000)
	assertPaymentAccountBalance(t, db, "checking-extra", 0)
}

func TestContasTransferSwipeDeleteRejectsCrossWorkspaceTarget(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	seedContasTransferScenario(t, db)

	handler := testPaidInvoiceMutationHandler(db)
	req := httptest.NewRequest(http.MethodDelete, "/transacoes/tx-other-transfer?escopo=single", nil)
	req.Header.Set("HX-Current-URL", "http://localhost/contas")
	rr := httptest.NewRecorder()
	handler.HandleDeletarTransacao(rr, req, "tx-other-transfer")
	if rr.Code != http.StatusNotFound {
		t.Fatalf("cross-workspace delete status = %d, want 404; body = %q", rr.Code, rr.Body.String())
	}

	var remaining int
	if err := db.QueryRow(`SELECT COUNT(1) FROM transactions WHERE id = 'tx-other-transfer' AND workspace_id = 'ws-other'`).Scan(&remaining); err != nil {
		t.Fatalf("count cross-workspace transfer: %v", err)
	}
	if remaining != 1 {
		t.Fatal("cross-workspace transfer was removed")
	}
}

func TestNormalizeContasAba(t *testing.T) {
	for _, tt := range []struct {
		raw  string
		want string
	}{
		{raw: "", want: "pagar"},
		{raw: "pagar", want: "pagar"},
		{raw: "PAGAR", want: "pagar"},
		{raw: "receber", want: "receber"},
		{raw: "Transferir", want: "transferir"},
		{raw: "foo", want: "pagar"},
	} {
		if got := normalizeContasAba(tt.raw); got != tt.want {
			t.Errorf("normalizeContasAba(%q) = %q, want %q", tt.raw, got, tt.want)
		}
	}
}

func TestContasMonthOptionsPreserveTransferTab(t *testing.T) {
	selected := time.Date(2026, time.September, 1, 0, 0, 0, 0, time.UTC)
	options := buildContasMonthOptionsAt(selected, "aluguel", selected, "transferir")
	for _, option := range options {
		if !strings.Contains(option.Query, "aba=transferir") {
			t.Errorf("month option lost aba=transferir: %q", option.Query)
		}
		if !strings.Contains(option.Query, "q=aluguel") {
			t.Errorf("month option lost q=aluguel: %q", option.Query)
		}
	}
}
