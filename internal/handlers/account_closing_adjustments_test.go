package handlers

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/csv"
	"errors"
	"fmt"
	"math"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/contabase-app/contabase/internal/services"
)

func r03Scenario(t *testing.T, kind string, balance int64) *sql.DB {
	t.Helper()
	db := openTestDB(t)
	t.Cleanup(func() { db.Close() })
	execTestSQL(t, db, `INSERT INTO users(id,name,email,password_hash) VALUES('r03-user','Author','r03@example.test','hash')`)
	execTestSQL(t, db, `INSERT INTO workspaces(id,name,type) VALUES('r03-ws','R03',?)`, kind)
	execTestSQL(t, db, `INSERT INTO workspace_members(workspace_id,user_id,role) VALUES('r03-ws','r03-user','ADMIN')`)
	execTestSQL(t, db, `INSERT INTO accounts(id,workspace_id,name,type,initial_balance,current_balance) VALUES('r03-account','r03-ws','R03 Account','CHECKING',?,?)`, balance, balance)
	return db
}

func r03Adjust(t *testing.T, db *sql.DB, workspace, account string, target, at int64) {
	t.Helper()
	var version int64
	if err := db.QueryRow(`SELECT balance_version FROM accounts WHERE workspace_id=? AND id=?`, workspace, account).Scan(&version); err != nil {
		t.Fatal(err)
	}
	if err := services.AdjustAccountBalance(context.Background(), db, services.AccountBalanceAdjustment{
		ID: fmt.Sprintf("%s-%d", account, version), WorkspaceID: workspace, AccountID: account,
		UserID: "r03-user", Reason: "R03 reconciliation", TargetBalance: target, ExpectedVersion: version, CreatedAt: at,
	}); err != nil {
		t.Fatal(err)
	}
}

func r03Move(t *testing.T, db *sql.DB, kind, dest string, amount, at int64) {
	t.Helper()
	tx, err := db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`INSERT INTO transactions(id,workspace_id,user_id,account_id,destination_account_id,type,amount,date,paid_at,status,description)
		VALUES(?,'r03-ws','r03-user','r03-account',NULLIF(?,''),?,?,?,?,'paid','R03 movement')`, fmt.Sprintf("move-%d", at), dest, kind, amount, at, at); err != nil {
		t.Fatal(err)
	}
	if err := services.ApplyBalanceEffect(tx, "r03-ws", kind, "CHECKING", "paid", amount, "r03-account", dest, at); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
}

func r03WantBalances(t *testing.T, db *sql.DB, workspace string, cut int64, want map[string]int64) {
	t.Helper()
	rows, err := BuildAccountClosingBalances(db, workspace, cut)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]int64{}
	for _, row := range rows {
		got[row.ID] = row.Balance
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("cut=%s balances=%v, want %v", time.Unix(cut, 0).UTC(), got, want)
	}
}

func TestR03ClosingAdjustmentTemporalBoundary(t *testing.T) {
	cut := NewReportPeriodFromMonth(2026, 8).End
	for _, kind := range []string{"personal", "business"} {
		for _, target := range []int64{150000, 60000} {
			for _, offset := range []int64{-1, 0, 1, 5 * 86400} {
				t.Run(fmt.Sprintf("%s/%d/%d", kind, target, offset), func(t *testing.T) {
					db := r03Scenario(t, kind, 100000)
					r03Adjust(t, db, "r03-ws", "r03-account", target, cut+offset)
					want := target
					if offset > 0 {
						want = 100000
					}
					r03WantBalances(t, db, "r03-ws", cut, map[string]int64{"r03-account": want})
					r03WantBalances(t, db, "r03-ws", cut+offset, map[string]int64{"r03-account": target})
				})
			}
		}
	}
}

func TestR03ClosingInterleavedMovementsAndWorkspace(t *testing.T) {
	for _, kind := range []string{"personal", "business"} {
		t.Run(kind, func(t *testing.T) {
			db := r03Scenario(t, kind, 100000)
			execTestSQL(t, db, `INSERT INTO accounts(id,workspace_id,name,type,current_balance) VALUES('r03-other','r03-ws','Other','SAVINGS',20000)`)
			execTestSQL(t, db, `INSERT INTO workspaces(id,name,type) VALUES('r03-foreign-ws','Foreign',?)`, kind)
			execTestSQL(t, db, `INSERT INTO accounts(id,workspace_id,name,type,current_balance) VALUES('r03-foreign','r03-foreign-ws','Foreign','CHECKING',900000)`)
			cut := NewReportPeriodFromMonth(2026, 8).End
			r03Adjust(t, db, "r03-ws", "r03-account", 105000, cut-1)
			r03Move(t, db, "INCOME", "", 20000, cut+1)
			r03Adjust(t, db, "r03-ws", "r03-account", 110000, cut+2)
			r03Move(t, db, "EXPENSE", "", 10000, cut+3)
			r03Move(t, db, "TRANSFER", "r03-other", 5000, cut+4)
			r03Adjust(t, db, "r03-ws", "r03-account", 99000, cut+5)
			r03Adjust(t, db, "r03-ws", "r03-other", 27000, cut+6)
			r03Adjust(t, db, "r03-foreign-ws", "r03-foreign", 1, cut+7)
			for _, tc := range []struct{ offset, source, dest int64 }{
				{-2, 100000, 20000}, {0, 105000, 20000}, {1, 125000, 20000},
				{2, 110000, 20000}, {3, 100000, 20000}, {4, 95000, 25000}, {5, 99000, 25000}, {7, 99000, 27000},
			} {
				r03WantBalances(t, db, "r03-ws", cut+tc.offset, map[string]int64{"r03-account": tc.source, "r03-other": tc.dest})
			}
			r03WantBalances(t, db, "r03-foreign-ws", cut, map[string]int64{"r03-foreign": 900000})
		})
	}
}

func TestR03ClosingUTCAndMonthBoundaries(t *testing.T) {
	for _, month := range []struct{ year, month int }{{2026, 8}, {2026, 12}, {2028, 2}} {
		period := NewReportPeriodFromMonth(month.year, month.month)
		// Different civil days in -03:00 still represent the same Unix instant.
		local := time.Unix(period.End, 0).In(time.FixedZone("minus-three", -3*3600))
		if local.Hour() != 20 || local.Minute() != 59 || local.Second() != 59 {
			t.Fatal("monthly cutoff no longer follows existing UTC contract")
		}
		db := r03Scenario(t, "business", 100000)
		r03Adjust(t, db, "r03-ws", "r03-account", 110000, local.Unix())
		r03Adjust(t, db, "r03-ws", "r03-account", 130000, local.Add(time.Second).Unix())
		r03WantBalances(t, db, "r03-ws", period.End, map[string]int64{"r03-account": 110000})
		r03WantBalances(t, db, "r03-ws", period.End+1, map[string]int64{"r03-account": 130000})
	}
}

func TestR03ClosingExactIntegerLimits(t *testing.T) {
	cut := NewReportPeriodFromMonth(2026, 8).End
	t.Run("minimum delta", func(t *testing.T) {
		db := r03Scenario(t, "personal", 0)
		r03Adjust(t, db, "r03-ws", "r03-account", math.MinInt64, cut+1)
		r03WantBalances(t, db, "r03-ws", cut, map[string]int64{"r03-account": 0})
	})
	t.Run("sum of deltas exceeds int64", func(t *testing.T) {
		db := r03Scenario(t, "personal", math.MinInt64)
		r03Adjust(t, db, "r03-ws", "r03-account", -1, cut+1)
		r03Adjust(t, db, "r03-ws", "r03-account", 0, cut+2)
		r03WantBalances(t, db, "r03-ws", cut, map[string]int64{"r03-account": math.MinInt64})
	})
	for _, tc := range []struct {
		name, movement string
		balance        int64
	}{{"upper intermediate", "EXPENSE", math.MaxInt64}, {"lower intermediate", "INCOME", math.MinInt64}} {
		t.Run(tc.name, func(t *testing.T) {
			db := r03Scenario(t, "personal", tc.balance)
			r03Move(t, db, tc.movement, "", 1, cut+1)
			r03Adjust(t, db, "r03-ws", "r03-account", tc.balance, cut+2)
			r03WantBalances(t, db, "r03-ws", cut, map[string]int64{"r03-account": tc.balance})
		})
	}
	for _, tc := range []struct {
		name, movement  string
		balance, target int64
	}{
		{"upper unrepresentable", "EXPENSE", math.MaxInt64, math.MaxInt64 - 1},
		{"lower unrepresentable", "INCOME", math.MinInt64, math.MinInt64 + 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			db := r03Scenario(t, "business", tc.balance)
			r03Adjust(t, db, "r03-ws", "r03-account", tc.target, cut+1)
			// Deliberately inconsistent synthetic ledger: reconstruction cannot fit.
			execTestSQL(t, db, `INSERT INTO transactions(id,workspace_id,user_id,account_id,type,amount,date,status,description) VALUES('bad','r03-ws','r03-user','r03-account',?,1,?,'paid','Synthetic out-of-range history')`, tc.movement, cut+2)
			rows, err := BuildAccountClosingBalances(db, "r03-ws", cut)
			if !errors.Is(err, services.ErrBalanceOutOfRange) || rows != nil {
				t.Fatalf("must reject unrepresentable history, got %v/%v", rows, err)
			}
			var got int64
			var storage string
			if err := db.QueryRow(`SELECT current_balance,typeof(current_balance) FROM accounts WHERE id='r03-account'`).Scan(&got, &storage); err != nil || got != tc.target || storage != "integer" {
				t.Fatalf("reconstruction changed persisted balance: %d/%s/%v", got, storage, err)
			}
			h := RelatoriosHandler{ActorRole: testMetasAdminRole, DB: db, WorkspaceID: "r03-ws", UserID: "r03-user"}
			if _, err := h.buildRelatoriosDataWithModo("", "contabil", "movimentos", "realizado", 8, 2026); !errors.Is(err, services.ErrBalanceOutOfRange) {
				t.Fatalf("report did not propagate reconstruction failure: %v", err)
			}
			for _, export := range []http.HandlerFunc{h.HandleExportarContabilSaldosCSV, h.HandleExportarContabilPDF} {
				rr := httptest.NewRecorder()
				export(rr, httptest.NewRequest(http.MethodGet, "/export?mes=8&ano=2026", nil))
				if rr.Code != http.StatusInternalServerError || rr.Header().Get("Content-Disposition") != "" {
					t.Fatalf("export accepted unrepresentable balance: status=%d", rr.Code)
				}
			}
		})
	}
}

func TestR03ClosingArchivedAccountRetainsHistoricalAdjustment(t *testing.T) {
	db := r03Scenario(t, "business", 100000)
	cut := NewReportPeriodFromMonth(2026, 8).End
	r03Adjust(t, db, "r03-ws", "r03-account", 0, cut+1)
	execTestSQL(t, db, `UPDATE accounts SET archived_at=? WHERE id='r03-account'`, cut+2)
	r03WantBalances(t, db, "r03-ws", cut, map[string]int64{"r03-account": 100000})
	r03WantBalances(t, db, "r03-ws", cut+2, map[string]int64{})
}

func TestR03ClosingReportCSVAndPDFShareBalance(t *testing.T) {
	db := r03Scenario(t, "business", 100000)
	cut := NewReportPeriodFromMonth(2026, 8).End
	r03Adjust(t, db, "r03-ws", "r03-account", 150000, cut+5*86400)
	r03WantBalances(t, db, "r03-ws", cut, map[string]int64{"r03-account": 100000})
	h := RelatoriosHandler{ActorRole: testMetasAdminRole, DB: db, WorkspaceID: "r03-ws", UserID: "r03-user"}
	data, err := h.buildRelatoriosDataWithModo("", "contabil", "movimentos", "realizado", 8, 2026)
	if err != nil {
		t.Fatal(err)
	}
	if len(data.ContabilSaldosContas) != 1 || data.ContabilSaldosContas[0].RawBalance != 100000 || data.ContabilSaldoTotalRaw != 100000 {
		t.Fatalf("report does not share historical balance: %v", data.ContabilSaldosContas)
	}
	if data.TotalReceitas.rawCents() != 0 || data.TotalDespesas.rawCents() != 0 {
		t.Fatal("adjustment was converted into income/expense")
	}
	var html bytes.Buffer
	if err := testRelatoriosContabilTemplate(t).ExecuteTemplate(&html, "relatorios-page", data); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(html.String(), "Saldo por conta em 31/08/2026") || !strings.Contains(html.String(), "R$ 1.000,00") {
		t.Fatal("historical balance missing in rendered report")
	}
	csvResponse := httptest.NewRecorder()
	h.HandleExportarContabilSaldosCSV(csvResponse, httptest.NewRequest(http.MethodGet, "/relatorios/contabil-saldos.csv?mes=8&ano=2026", nil))
	if csvResponse.Code != http.StatusOK {
		t.Fatalf("CSV status=%d: %s", csvResponse.Code, csvResponse.Body.String())
	}
	records, err := csv.NewReader(strings.NewReader(csvResponse.Body.String())).ReadAll()
	if err != nil {
		t.Fatal(err)
	}
	if len(records) != 3 || records[1][4] != strconv.FormatInt(data.ContabilSaldosContas[0].RawBalance, 10) || records[2][4] != "100000" {
		t.Fatalf("CSV differs: %v", records)
	}
	pdfResponse := httptest.NewRecorder()
	h.HandleExportarContabilPDF(pdfResponse, httptest.NewRequest(http.MethodGet, "/relatorios/contabil.pdf?mes=8&ano=2026", nil))
	if pdfResponse.Code != http.StatusOK {
		t.Fatalf("PDF status=%d: %s", pdfResponse.Code, pdfResponse.Body.String())
	}
	text := decodedPDFText(pdfResponse.Body.Bytes())
	start := strings.Index(text, "Saldo por conta em 31/08/2026")
	if start < 0 || !strings.Contains(text[start:], "R03 Account") || !strings.Contains(text[start:], "R$ 1.000,00") || strings.Contains(text[start:], "R$ 1.500,00") {
		t.Fatal("PDF does not share historical closing balance")
	}
}
