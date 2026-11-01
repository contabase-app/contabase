package handlers

import (
	"context"
	"database/sql"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/contabase-app/contabase/internal/models"
	"github.com/contabase-app/contabase/internal/services"
	"github.com/google/uuid"
)

func c2bHandlerFixture(t *testing.T, economicDate string, net int64) (*sql.DB, services.ClassifyInvestmentSettlementInput) {
	t.Helper()
	db := r03Scenario(t, "business", 500000)
	ctx := context.Background()
	execTestSQL(t, db, `UPDATE accounts SET type='INVESTMENT' WHERE workspace_id='r03-ws' AND id='r03-account'`)
	inv, err := services.CreateInvestment(ctx, db, services.CreateInvestmentInput{WorkspaceID: "r03-ws", AccountID: "r03-account", CreatedBy: "r03-user", Name: "CDB", Institution: "Banco", Kind: "CDB", PrincipalCents: 500000, TaxRegime: "MANUAL", AppliedDate: "2025-01-15"})
	if err != nil {
		t.Fatal(err)
	}
	v, err := services.UpsertInvestmentValuation(ctx, db, services.UpsertInvestmentValuationInput{WorkspaceID: "r03-ws", InvestmentID: inv.ID, ActingUserID: "r03-user", ValuationDate: "2025-02-01", AmountCents: 500000})
	if err != nil {
		t.Fatal(err)
	}
	c, err := services.ReadInvestmentReferenceCandidate(ctx, db, "r03-ws", inv.ID, v.ID)
	if err != nil {
		t.Fatal(err)
	}
	represented := int64(500000)
	root, err := services.RecordInvestmentReference(ctx, db, services.RecordInvestmentReferenceInput{WorkspaceID: "r03-ws", InvestmentID: inv.ID, AccountID: "r03-account", PositionID: v.ID, CreatedBy: "r03-user", RequestID: uuid.NewString(), Reason: "Referência", CoverageToken: c.CoverageToken, RepresentedCents: &represented, Confirm: true})
	if err != nil {
		t.Fatal(err)
	}
	_, err = services.IntegrateInvestmentPosition(ctx, db, services.IntegrateInvestmentPositionInput{WorkspaceID: "r03-ws", InvestmentID: inv.ID, AccountID: "r03-account", ReferenceRevisionID: root.ID, PositionID: v.ID, CoverageToken: c.CoverageToken, CreatedBy: "r03-user", RequestID: uuid.NewString(), Reason: "Ativação"})
	if err != nil {
		t.Fatal(err)
	}
	b := financialHistoryPosition(t, db, inv.ID, "2025-03-01", 530000, root.ID, 1)
	w, err := services.CreateInvestmentWithdrawal(ctx, db, services.CreateInvestmentWithdrawalInput{WorkspaceID: "r03-ws", InvestmentID: inv.ID, CreatedBy: "r03-user", RequestID: uuid.NewString(), WithdrawalDate: economicDate, Mode: services.InvestmentWithdrawalTotal, ReceivedCents: net, Allocations: []models.InvestmentWithdrawalAllocation{{PurchaseID: "initial:" + inv.ID, ReceivedCents: net, PrincipalCents: &represented}}})
	if err != nil {
		t.Fatal(err)
	}
	gross, ir, zero := int64(530000), int64(530000-net), int64(0)
	s, err := services.RecordInvestmentSettlement(ctx, db, services.RecordInvestmentSettlementInput{WorkspaceID: "r03-ws", InvestmentID: inv.ID, WithdrawalID: w.ID, AccountID: "r03-account", DestinationAccountID: "r03-account", EconomicDate: w.WithdrawalDate, CreatedBy: "r03-user", RequestID: uuid.NewString(), Reason: "Comprovante", GrossCents: &gross, IRCents: &ir, IOFCents: &zero, FeesCents: &zero, NetCents: net, PatrimonyCents: &gross, PatrimonyEvidence: "Extrato", ReceiptEvidence: "Caixa confirmado", ConfirmReceipt: true})
	if err != nil {
		t.Fatal(err)
	}
	var version int64
	if err = db.QueryRow(`SELECT balance_version FROM accounts WHERE workspace_id='r03-ws' AND id='r03-account'`).Scan(&version); err != nil {
		t.Fatal(err)
	}
	a, err := services.ApplyInvestmentSettlement(ctx, db, services.ApplyInvestmentSettlementInput{WorkspaceID: "r03-ws", InvestmentID: inv.ID, WithdrawalID: w.ID, SettlementRevisionID: s.ID, ReferenceRevisionID: root.ID, FinancialRevisionID: b.ID, ExpectedSettlementVersion: 1, ExpectedFinancialVersion: 2, ExpectedBalanceVersion: version, ExpectedDestinationVersion: version, CreatedBy: "r03-user", RequestID: uuid.NewString(), Reason: "Reconciliação", ReconciliationEvidence: "Recebimento mantido; nenhum imposto previamente debitado"})
	if err != nil {
		t.Fatal(err)
	}
	execTestSQL(t, db, `INSERT INTO categories(id,workspace_id,name,type,macro_group) VALUES('c2b-gain','r03-ws','Resultado realizado','INCOME','Receitas Financeiras'),('c2b-loss','r03-ws','Perda realizada','EXPENSE','Financeiro')`)
	category := "c2b-gain"
	if net < 500000 {
		category = "c2b-loss"
	}
	return db, services.ClassifyInvestmentSettlementInput{WorkspaceID: "r03-ws", InvestmentID: inv.ID, WithdrawalID: w.ID, SettlementRevisionID: s.ID, ApplicationID: a.ID, ExpectedSettlementVersion: s.Version, CreatedBy: "r03-user", RequestID: uuid.NewString(), CategoryID: category, LegacyReconciliationEvidence: "Extratos e resultados gerenciais conferidos; sem receitas ou despesas legadas equivalentes"}
}

// A fixture that temporarily removes boxes must detach and restore its
// cross-table triggers. Production guards remain byte-for-byte identical.
func suspendClassificationReserveTriggersForFixture(t *testing.T, db *sql.DB) func() {
	t.Helper()
	rows, err := db.Query(`SELECT name,sql FROM sqlite_schema WHERE type='trigger' AND
 (name IN ('trg_c2b_reserve','trg_c2b_reserve_update') OR name LIKE 'trg_cancellation79_%' OR name LIKE 'trg_reversal80_%') ORDER BY name`)
	if err != nil {
		t.Fatal(err)
	}
	type definition struct{ name, sql string }
	var definitions []definition
	for rows.Next() {
		var d definition
		if err = rows.Scan(&d.name, &d.sql); err != nil {
			rows.Close()
			t.Fatal(err)
		}
		definitions = append(definitions, d)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		t.Fatal(err)
	}
	for _, d := range definitions {
		execTestSQL(t, db, `DROP TRIGGER `+d.name)
	}
	return func() {
		t.Helper()
		for _, d := range definitions {
			execTestSQL(t, db, d.sql)
			var restored string
			if err := db.QueryRow(`SELECT sql FROM sqlite_schema WHERE type='trigger' AND name=?`, d.name).Scan(&restored); err != nil || restored != d.sql {
				t.Fatal("reserve guard not restored", d.name, err)
			}
		}
	}
}

func TestInvestmentClassificationDashboardFinancialQueries(t *testing.T) {
	for _, net := range []int64{525500, 490000} {
		t.Run(fmt.Sprint(net), func(t *testing.T) {
			day := AppCivilDateUTC(time.Now()).AddDate(0, 0, -1)
			db, in := c2bHandlerFixture(t, day.Format("2006-01-02"), net)
			at := day.Add(12 * time.Hour).Unix()
			tx, err := db.Begin()
			if err != nil {
				t.Fatal(err)
			}
			defer tx.Rollback()
			for _, ordinary := range []struct {
				id, kind, category string
				amount             int64
			}{{"ordinary-income", "INCOME", "c2b-gain", 10000}, {"ordinary-expense", "EXPENSE", "c2b-loss", 2000}} {
				if _, err = tx.Exec(`INSERT INTO transactions(id,workspace_id,user_id,account_id,category_id,type,amount,date,paid_at,status) VALUES(?,'r03-ws','r03-user','r03-account',?,?,?,?,?,'paid')`, ordinary.id, ordinary.category, ordinary.kind, ordinary.amount, at, at); err != nil {
					t.Fatal(err)
				}
				if err = services.ApplyBalanceEffect(tx, "r03-ws", ordinary.kind, "INVESTMENT", "paid", ordinary.amount, "r03-account", "", at); err != nil {
					t.Fatal(err)
				}
			}
			if err = tx.Commit(); err != nil {
				t.Fatal(err)
			}
			var balance int64
			if err = db.QueryRow(`SELECT current_balance FROM accounts WHERE workspace_id='r03-ws' AND id='r03-account'`).Scan(&balance); err != nil {
				t.Fatal(err)
			}
			type financialSnapshot struct {
				income, expense, flow, variable, average                       int64
				accountPercent, accountDirection, totalPercent, totalDirection string
				reserve                                                        services.WorkspaceReserveBalance
			}
			read := func() financialSnapshot {
				var s financialSnapshot
				s.income, s.expense = queryDashboardMonthlySummary(db, "r03-ws", day.Add(12*time.Hour))
				s.flow = queryRealAccountNetCashFlow(db, "r03-ws", day.Unix(), day.AddDate(0, 0, 1).Unix())
				s.variable = queryVariableDailyExpense90d(db, "r03-ws")
				s.average = queryAverageDailyExpense(db, "r03-ws")
				now := day.AddDate(0, 0, 1).Add(12 * time.Hour)
				s.accountPercent, s.accountDirection = queryAccountTrend(db, "r03-ws", "r03-account", balance, now)
				s.totalPercent, s.totalDirection = queryBalanceTrend(db, "r03-ws", balance, now)
				s.reserve, err = services.CalculateWorkspaceReserveBalance(db, "r03-ws")
				if err != nil {
					t.Fatal(err)
				}
				return s
			}
			before := read()
			// The sole STANDARD expense is 2000 cents over yesterday and today.
			if before.income != 10000 || before.expense != 2000 || before.flow != 8000 || before.variable <= 0 || before.average != 1000 || balance != net+8000 {
				t.Fatal("STANDARD control failed", before, balance)
			}
			if _, err = services.ClassifyInvestmentSettlementResult(context.Background(), db, in); err != nil {
				t.Fatal(err)
			}
			if after := read(); after != before {
				t.Fatal("classification changed financial indicators", before, after)
			}
			dre, err := BuildDREData(db, "r03-ws", day.Year())
			if err != nil {
				t.Fatal(err)
			}
			result, err := SumDRECompetencia(dre, day.Format("2006-01"))
			if err != nil || result != net-500000+8000 {
				t.Fatal("managerial result omitted", result, err)
			}
			if income, expense := queryDashboardMonthlySummary(db, "foreign", day); income != 0 || expense != 0 {
				t.Fatal("foreign workspace leak", income, expense)
			}
			if average := queryAverageDailyExpense(db, "foreign"); average != 0 {
				t.Fatal("foreign workspace average leak", average)
			}
		})
	}
}

func TestInvestmentClassificationAfterTransferKeepsBothAccounts(t *testing.T) {
	db, in := c2bHandlerFixture(t, "2026-03-31", 525500)
	execTestSQL(t, db, `INSERT INTO accounts(id,workspace_id,name,type) VALUES('after-c2a-bank','r03-ws','Banco','CHECKING')`)
	at := testUnixDate("2026-04-01")
	tx, err := db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if _, err = tx.Exec(`INSERT INTO transactions(id,workspace_id,user_id,account_id,destination_account_id,type,amount,date,paid_at,status) VALUES('after-c2a-transfer','r03-ws','r03-user','r03-account','after-c2a-bank','TRANSFER',525500,?,?,'paid')`, at, at); err != nil {
		t.Fatal(err)
	}
	if err = services.ApplyBalanceEffect(tx, "r03-ws", "TRANSFER", "INVESTMENT", "paid", 525500, "r03-account", "after-c2a-bank", at); err != nil {
		t.Fatal(err)
	}
	if err = tx.Commit(); err != nil {
		t.Fatal(err)
	}
	read := func() []AccountClosingBalance {
		balances, err := BuildAccountClosingBalances(db, "r03-ws", at+86400)
		if err != nil {
			t.Fatal(err)
		}
		return balances
	}
	before := read()
	r, err := services.ClassifyInvestmentSettlementResult(context.Background(), db, in)
	if err != nil {
		t.Fatal(err)
	}
	if after := read(); !reflect.DeepEqual(before, after) {
		t.Fatal("transfer balances changed", before, after)
	}
	var broker, bank int64
	if err = db.QueryRow(`SELECT current_balance FROM accounts WHERE workspace_id='r03-ws' AND id='r03-account'`).Scan(&broker); err != nil {
		t.Fatal(err)
	}
	if err = db.QueryRow(`SELECT current_balance FROM accounts WHERE workspace_id='r03-ws' AND id='after-c2a-bank'`).Scan(&bank); err != nil || broker != 0 || bank != 525500 || r.ResultCents == nil || *r.ResultCents != 25500 {
		t.Fatal(broker, bank, r, err)
	}
}

func TestInvestmentClassificationLegacyFixtureRestoresReserveGuard(t *testing.T) {
	db, in := c2bHandlerFixture(t, "2026-03-31", 490000)
	r, err := services.ClassifyInvestmentSettlementResult(context.Background(), db, in)
	if err != nil {
		t.Fatal(err)
	}
	execTestSQL(t, db, `INSERT INTO boxes(id,workspace_id,name,category_id) VALUES('c2b-box','r03-ws','Reserva','c2b-loss')`)
	tenantMetasCategoriesLegacyBoxes(t, db)
	if _, err = db.Exec(`INSERT INTO box_virtual_ledger(id,box_id,amount,type,source_transaction_id,reference_date) VALUES('forbidden-reserve','c2b-box',-10000,'CONSUME',?,1)`, r.TransactionID); err == nil || !strings.Contains(err.Error(), "classification cannot move reserves") {
		t.Fatal("reserve guard lost after fixture rebuild", err)
	}
}

func TestInvestmentClassificationReportsAndHistoricalCash(t *testing.T) {
	for _, tc := range []struct {
		date        string
		net, result int64
	}{{"2026-03-31", 525500, 25500}, {"2025-12-31", 525500, 25500}, {"2026-01-01", 525500, 25500}, {"2026-04-01", 490000, -10000}} {
		t.Run(tc.date, func(t *testing.T) {
			db, in := c2bHandlerFixture(t, tc.date, tc.net)
			day, err := time.Parse("2006-01-02", tc.date)
			if err != nil {
				t.Fatal(err)
			}
			period := NewReportPeriodFromMonth(day.Year(), int(day.Month()))
			before, err := BuildAccountClosingBalances(db, "r03-ws", period.Start-1)
			if err != nil {
				t.Fatal(err)
			}
			currentBefore, err := BuildAccountClosingBalances(db, "r03-ws", period.End)
			if err != nil {
				t.Fatal(err)
			}
			if _, err = services.ClassifyInvestmentSettlementResult(context.Background(), db, in); err != nil {
				t.Fatal(err)
			}
			after, err := BuildAccountClosingBalances(db, "r03-ws", period.Start-1)
			if err != nil || !reflect.DeepEqual(before, after) {
				t.Fatal("historical balances changed", err)
			}
			currentAfter, err := BuildAccountClosingBalances(db, "r03-ws", period.End)
			if err != nil || !reflect.DeepEqual(currentBefore, currentAfter) {
				t.Fatal("closing balances changed", err)
			}
			dre, err := BuildDREData(db, "r03-ws", day.Year())
			if err != nil {
				t.Fatal(err)
			}
			v, err := SumDRECompetencia(dre, day.Format("2006-01"))
			if err != nil || v != tc.result {
				t.Fatal("DRE", v, err)
			}
			foreign, err := BuildDREData(db, "foreign", day.Year())
			if err != nil || len(foreign) != 0 {
				t.Fatal("workspace leak", err)
			}
			for name, query := range map[string]func() ([]CashEvent, error){
				"realized":          func() ([]CashEvent, error) { return QueryRealizedCashEvents(db, "r03-ws", period) },
				"projected":         func() ([]CashEvent, error) { return QueryProjectedCashEvents(db, "r03-ws", period) },
				"realized due":      func() ([]CashEvent, error) { return QueryRealizedCashEventsByDueDate(db, "r03-ws", period) },
				"projected due":     func() ([]CashEvent, error) { return QueryProjectedCashEventsByDueDate(db, "r03-ws", period) },
				"realized combined": func() ([]CashEvent, error) { return BuildRealizadoCashFlowData(db, "r03-ws", period) },
			} {
				rows, err := query()
				if err != nil || len(rows) != 0 {
					t.Fatal(name, rows, err)
				}
			}
			statement, err := BuildStatementData(db, "r03-ws", "r03-account", period)
			if err != nil || len(statement) != 0 {
				t.Fatal("false statement movement", err)
			}
			extrato, err := BuildExtratoData(db, "r03-ws", period)
			if err != nil || len(extrato) != 0 {
				t.Fatal("false extrato", err)
			}
			caixa, err := BuildCaixaRealizadoData(db, "r03-ws", day.Year())
			if err != nil || len(caixa) != 0 {
				t.Fatal("false realized caixa", err)
			}
			forecast, err := BuildCaixaData(db, "r03-ws", day.Year(), int(day.Month()))
			if err != nil || len(forecast) != 0 {
				t.Fatal("false forecast caixa", err)
			}
			flow, err := calcPaidNetCashFlowBetween(db, "r03-ws", time.Unix(period.Start, 0), time.Unix(period.End+1, 0))
			if err != nil {
				t.Fatal(err)
			}
			net, err := flow.int64("C2B paid")
			if err != nil || net != 0 {
				t.Fatal("false paid flow", net, err)
			}
			flow, err = calcProjectedTransactionNetCashFlowBetween(db, "r03-ws", day.AddDate(0, 0, -2), day.AddDate(0, 0, -1), day.AddDate(0, 0, 2))
			if err != nil {
				t.Fatal(err)
			}
			net, err = flow.int64("C2B projection")
			if err != nil || net != 0 {
				t.Fatal("false projected flow", net, err)
			}
			balance, known, err := BuildProjectedBalanceUntilPeriodEnd(db, "r03-ws", period.End, day.AddDate(0, 0, -1))
			if err != nil || !known || balance != tc.net {
				t.Fatal("projection changed", balance, known, err)
			}
			h := &RelatoriosHandler{ActorRole: testMetasAdminRole, DB: db, WorkspaceID: "r03-ws", UserID: "r03-user"}
			income, err := h.queryContabilReceitasByMacro(context.Background(), period.Start, period.End)
			if err != nil {
				t.Fatal(err)
			}
			if tc.result > 0 && (len(income) != 1 || income[0].RawAmount != tc.result) {
				t.Fatal("managerial revenue", income)
			}
			raw, err := h.queryDRERaw(day.Year())
			if err != nil || len(raw) != 1 || raw[0].Amount != tc.result {
				t.Fatal("raw DRE", raw, err)
			}
			cash, err := h.queryNetCashflow(context.Background(), period.Start, period.End)
			if err != nil || cash != 0 {
				t.Fatal("report cash", cash, err)
			}
			pdfIn, err := queryDREPDFCategorySummary(db, "r03-ws", "INCOME", period.Start, period.End)
			if err != nil {
				t.Fatal(err)
			}
			if tc.result > 0 && len(pdfIn) != 1 {
				t.Fatal("DRE PDF omitted gain")
			}
			// Existing CSV export must preserve the managerial/cash separation.
			csv := httptest.NewRecorder()
			h.HandleExportarContabilResumoCSV(csv, httptest.NewRequest(http.MethodGet, "/relatorios?mes="+day.Format("01")+"&ano="+day.Format("2006"), nil))
			if csv.Code != http.StatusOK || !strings.Contains(csv.Body.String(), ",0,0,") {
				t.Fatal("CSV fictitious cash", csv.Code, csv.Body.String())
			}
		})
	}
}

func TestInvestmentClassificationGenericMutationGuards(t *testing.T) {
	db, in := c2bHandlerFixture(t, "2026-03-31", 525500)
	r, err := services.ClassifyInvestmentSettlementResult(context.Background(), db, in)
	if err != nil {
		t.Fatal(err)
	}
	h := &TransactionHandler{DB: db, WriterDB: db, WorkspaceID: "r03-ws", UserID: "r03-user"}
	for name, call := range map[string]func(http.ResponseWriter, *http.Request, string){"edit": h.HandleAtualizarTransacao, "delete": h.HandleDeletarTransacao, "status": h.HandleTogglePagamento, "recurrence conversion": h.HandleAtualizarTransacao, "invoice": h.HandleMoverFatura} {
		t.Run(name, func(t *testing.T) {
			w := httptest.NewRecorder()
			request := httptest.NewRequest(http.MethodPost, "/transacoes/"+r.TransactionID, strings.NewReader("recorrente=1"))
			request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
			call(w, request, r.TransactionID)
			if w.Code != http.StatusConflict {
				t.Fatal(w.Code, w.Body.String())
			}
		})
	}
	for name, call := range map[string]func(http.ResponseWriter, *http.Request){"bulk delete": h.HandleBulkDelete, "bulk status": h.HandleBulkUpdate} {
		t.Run(name, func(t *testing.T) {
			w := httptest.NewRecorder()
			request := httptest.NewRequest(http.MethodPost, "/transacoes/lote", strings.NewReader(url.Values{"ids": {r.TransactionID}, "status_pagamento": {"pending"}}.Encode()))
			request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
			call(w, request)
			if w.Code != http.StatusConflict {
				t.Fatal(w.Code, w.Body.String())
			}
		})
	}
	if _, err = loadTransactionRefundTarget(db, "r03-ws", r.TransactionID); err != sql.ErrNoRows {
		t.Fatal("conventional refund permitted", err)
	}
	var balance, amount int64
	var status, mode string
	if err = db.QueryRow(`SELECT a.current_balance,t.amount,t.status,t.effect_mode FROM accounts a JOIN transactions t ON t.workspace_id=a.workspace_id AND t.account_id=a.id WHERE a.workspace_id='r03-ws' AND t.id=?`, r.TransactionID).Scan(&balance, &amount, &status, &mode); err != nil || balance != 525500 || amount != 25500 || status != "paid" || mode != "CLASSIFICATION_ONLY" {
		t.Fatal("generic mutation changed classification", err)
	}
}
