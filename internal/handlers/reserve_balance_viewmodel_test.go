package handlers

import (
	"database/sql"
	"html/template"
	"math"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"
	"time"
)

func TestDashboardAndMetasExposeReserveContractByWorkspace(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	seedReserveBalanceViewModelScenario(t, db)

	dashboardA := BuildDashboardData(db, "user-a", "ws-a")
	if !dashboardA.Balance.ReserveBalanceAvailable {
		t.Fatal("dashboard ws-a reserve balance unavailable")
	}
	if got := moneyDisplayToCentsForTest(t, dashboardA.Balance.Money); got != 15000 {
		t.Fatalf("dashboard ws-a saldo real = %d, want 15000", got)
	}
	if got := moneyDisplayToCentsForTest(t, dashboardA.Balance.Reserved); got != 2000 {
		t.Fatalf("dashboard ws-a reservado = %d, want 2000", got)
	}
	if got := moneyDisplayToCentsForTest(t, dashboardA.Balance.Free); got != 13000 {
		t.Fatalf("dashboard ws-a saldo livre = %d, want 13000", got)
	}
	if dashboardA.Balance.FreeIsNegative {
		t.Fatalf("dashboard ws-a free negative = true, want false")
	}

	metasHandlerA := MetasHandler{ActorRole: testMetasAdminRole, DB: db, WorkspaceID: "ws-a", UserID: "user-a"}
	metasA, err := metasHandlerA.buildMetasData("", "")
	if err != nil {
		t.Fatalf("buildMetasData ws-a: %v", err)
	}
	if got := moneyDisplayToCentsForTest(t, metasA.RealBalance); got != 15000 {
		t.Fatalf("metas ws-a saldo real = %d, want 15000", got)
	}
	if got := moneyDisplayToCentsForTest(t, metasA.ReservedBalance); got != 2000 {
		t.Fatalf("metas ws-a reservado = %d, want 2000", got)
	}
	if got := moneyDisplayToCentsForTest(t, metasA.FreeBalance); got != 13000 {
		t.Fatalf("metas ws-a saldo livre = %d, want 13000", got)
	}
	if metasA.FreeBalanceNegative {
		t.Fatalf("metas ws-a free negative = true, want false")
	}

	dashboardB := BuildDashboardData(db, "user-b", "ws-b")
	if !dashboardB.Balance.ReserveBalanceAvailable {
		t.Fatal("dashboard ws-b reserve balance unavailable")
	}
	if got := moneyDisplayToCentsForTest(t, dashboardB.Balance.Money); got != 7000 {
		t.Fatalf("dashboard ws-b saldo real = %d, want 7000", got)
	}
	if got := moneyDisplayToCentsForTest(t, dashboardB.Balance.Reserved); got != 9000 {
		t.Fatalf("dashboard ws-b reservado = %d, want 9000", got)
	}
	if got := moneyDisplayToCentsForTest(t, dashboardB.Balance.Free); got != -2000 {
		t.Fatalf("dashboard ws-b saldo livre = %d, want -2000", got)
	}
	if !dashboardB.Balance.FreeIsNegative {
		t.Fatalf("dashboard ws-b free negative = false, want true")
	}

	metasHandlerB := MetasHandler{ActorRole: testMetasAdminRole, DB: db, WorkspaceID: "ws-b", UserID: "user-b"}
	metasB, err := metasHandlerB.buildMetasData("", "")
	if err != nil {
		t.Fatalf("buildMetasData ws-b: %v", err)
	}
	if got := moneyDisplayToCentsForTest(t, metasB.RealBalance); got != 7000 {
		t.Fatalf("metas ws-b saldo real = %d, want 7000", got)
	}
	if got := moneyDisplayToCentsForTest(t, metasB.ReservedBalance); got != 9000 {
		t.Fatalf("metas ws-b reservado = %d, want 9000", got)
	}
	if got := moneyDisplayToCentsForTest(t, metasB.FreeBalance); got != -2000 {
		t.Fatalf("metas ws-b saldo livre = %d, want -2000", got)
	}
	if !metasB.FreeBalanceNegative {
		t.Fatalf("metas ws-b free negative = false, want true")
	}
}

func TestDashboardReserveBalanceAvailability(t *testing.T) {
	for _, tc := range []struct {
		name      string
		mutate    func(*testing.T, *sql.DB)
		available bool
		real      int64
		reserved  int64
		free      int64
	}{
		{
			name: "normal", available: true, real: 15000, reserved: 2000, free: 13000,
		},
		{
			name: "legitimate-zero", available: true, real: 0, reserved: 2000, free: -2000,
			mutate: func(t *testing.T, db *sql.DB) {
				execTestSQL(t, db, `UPDATE accounts SET current_balance = 0 WHERE workspace_id = 'ws-a' AND type != 'CREDIT_CARD'`)
			},
		},
		{
			name: "empty-workspace", available: true,
			mutate: func(t *testing.T, db *sql.DB) {
				execTestSQL(t, db, `DELETE FROM accounts WHERE workspace_id = 'ws-a'`)
				execTestSQL(t, db, `DELETE FROM box_virtual_ledger WHERE box_id IN (SELECT id FROM boxes WHERE workspace_id = 'ws-a')`)
			},
		},
		{
			name: "real-overflow", mutate: func(t *testing.T, db *sql.DB) {
				execTestSQL(t, db, `UPDATE accounts SET current_balance = ? WHERE id = 'a-check'`, math.MaxInt64)
				execTestSQL(t, db, `UPDATE accounts SET current_balance = 1 WHERE id = 'a-save'`)
			},
		},
		{
			name: "reserved-overflow", mutate: func(t *testing.T, db *sql.DB) {
				execTestSQL(t, db, `UPDATE box_virtual_ledger SET amount = ? WHERE id = 'a-ledger-1'`, math.MaxInt64)
				execTestSQL(t, db, `UPDATE box_virtual_ledger SET amount = 1 WHERE id = 'a-ledger-2'`)
			},
		},
		{
			name: "free-overflow", mutate: func(t *testing.T, db *sql.DB) {
				execTestSQL(t, db, `UPDATE accounts SET current_balance = ? WHERE id = 'a-check'`, math.MaxInt64)
				execTestSQL(t, db, `UPDATE accounts SET current_balance = 0 WHERE id = 'a-save'`)
				execTestSQL(t, db, `DELETE FROM box_virtual_ledger WHERE id = 'a-ledger-2'`)
				execTestSQL(t, db, `UPDATE box_virtual_ledger SET type = 'RELEASE', amount = -1 WHERE id = 'a-ledger-1'`)
			},
		},
		{
			name: "cancel-intermediate-overflow", available: true, real: math.MaxInt64, reserved: 2000, free: math.MaxInt64 - 2000,
			mutate: func(t *testing.T, db *sql.DB) {
				execTestSQL(t, db, `UPDATE accounts SET current_balance = ? WHERE id = 'a-check'`, math.MaxInt64)
				execTestSQL(t, db, `UPDATE accounts SET current_balance = 1 WHERE id = 'a-save'`)
				execTestSQL(t, db, `INSERT INTO accounts (id, workspace_id, name, type, initial_balance, current_balance) VALUES ('a-cancel', 'ws-a', 'Cancel', 'WALLET', -1, -1)`)
			},
		},
		{
			name: "database-error", mutate: func(t *testing.T, db *sql.DB) {
				execTestSQL(t, db, `DROP TABLE box_virtual_ledger`)
			},
		},
		{
			name: "foreign-workspace-extreme", available: true, real: 15000, reserved: 2000, free: 13000,
			mutate: func(t *testing.T, db *sql.DB) {
				execTestSQL(t, db, `UPDATE accounts SET current_balance = ? WHERE id = 'b-check'`, math.MaxInt64)
				execTestSQL(t, db, `INSERT INTO accounts (id, workspace_id, name, type, initial_balance, current_balance) VALUES ('b-extra', 'ws-b', 'Extra', 'CHECKING', 1, 1)`)
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			db := openTestDB(t)
			defer db.Close()
			seedReserveBalanceViewModelScenario(t, db)
			if tc.mutate != nil {
				tc.mutate(t, db)
			}
			data := BuildDashboardData(db, "user-a", "ws-a")
			if tc.name == "real-overflow" {
				_, err := (&MetasHandler{ActorRole: testMetasAdminRole, DB: db, WorkspaceID: "ws-a", UserID: "user-a"}).buildMetasData("", "")
				if err == nil || !strings.Contains(err.Error(), "agregado de saldo fora do intervalo int64") {
					t.Fatalf("metas error = %v, want propagated reserve aggregate error", err)
				}
			}
			if data.Balance.ReserveBalanceAvailable != tc.available {
				t.Fatalf("available = %t, want %t", data.Balance.ReserveBalanceAvailable, tc.available)
			}
			if tc.available {
				for _, value := range []struct {
					got  MoneyDisplay
					want int64
				}{
					{data.Balance.Money, tc.real},
					{data.Balance.Reserved, tc.reserved},
					{data.Balance.Free, tc.free},
				} {
					if got := moneyDisplayToCentsForTest(t, value.got); got != value.want {
						t.Fatalf("balance = %d, want %d", got, value.want)
					}
				}
			}
			panel := renderReserveDashboardPanel(t, data)
			if tc.available {
				if strings.Contains(panel, "Indisponível") {
					t.Fatalf("available balance rendered unavailable: %s", panel)
				}
				for _, value := range []int64{tc.real, tc.reserved, tc.free} {
					money := MoneyMajor(value)
					if !strings.Contains(panel, "R$ "+money.Reais+money.Cents) {
						t.Fatalf("missing exact balance %d in dashboard panel", value)
					}
				}
			} else {
				for _, pattern := range []string{
					`(?s)Saldo total</p>\s*<p[^>]*data-sensitive>Indisponível</p>`,
					`(?s)Reservado</span><strong[^>]*data-sensitive>Indisponível</strong>`,
					`(?s)Disponível</span><strong[^>]*data-sensitive>Indisponível</strong>`,
				} {
					if !regexp.MustCompile(pattern).MatchString(panel) {
						t.Fatalf("dashboard panel missing unavailable indicator %s", pattern)
					}
				}
				if strings.Contains(panel, "R$ 0,00") {
					t.Fatalf("dashboard panel did not mark all balances unavailable: %s", panel)
				}
			}
		})
	}
}

func TestDashboardReserveLegacyComponentsAvailability(t *testing.T) {
	for _, name := range []string{"dashboard-balance", "dashboard-financial"} {
		t.Run(name, func(t *testing.T) {
			tpl := template.Must(template.New(name).Parse(readTemplateFile(t, "templates/components/"+name+".html") + `{{define "financial-emblem"}}{{end}}`))
			for _, available := range []bool{false, true} {
				data := DashboardData{Balance: BalanceData{
					ReserveBalanceAvailable: available,
					Money:                   MoneyMajor(0), Reserved: MoneyMinor(0), Free: MoneyMinor(0),
				}}
				recorder := httptest.NewRecorder()
				if err := tpl.ExecuteTemplate(recorder, name, data); err != nil {
					t.Fatal(err)
				}
				html := recorder.Body.String()
				if available {
					if strings.Count(html, "R$ 0,00") < 2 || strings.Contains(html, "Indisponível") {
						t.Fatalf("valid zero lost in %s", name)
					}
				} else if strings.Count(html, "Indisponível") < 3 || strings.Contains(html, "R$ 0,00") {
					t.Fatalf("unavailable balance rendered as zero in %s", name)
				}
			}
		})
	}
}

func renderReserveDashboardPanel(t *testing.T, data DashboardData) string {
	t.Helper()
	tpl := template.Must(template.New("dashboard").Funcs(withAppShellTestFuncs(template.FuncMap{
		"assetPath": func(path string) string { return path },
	})).Parse(
		readTemplateFile(t, "templates/pages/dashboard.html") +
			readTemplateFile(t, "templates/components/app_shell.html") +
			readTemplateFile(t, "templates/components/credit_card.html") +
			readTemplateFile(t, "templates/components/investment_patrimony.html"),
	))
	recorder := httptest.NewRecorder()
	if err := tpl.ExecuteTemplate(recorder, "dashboard-page", data); err != nil {
		t.Fatalf("render dashboard: %v", err)
	}
	if recorder.Code != http.StatusOK {
		t.Fatalf("dashboard HTTP status = %d", recorder.Code)
	}
	html := recorder.Body.String()
	start := strings.Index(html, `aria-labelledby="v2-financial-title"`)
	if start < 0 {
		t.Fatal("financial panel missing")
	}
	end := strings.Index(html[start:], "</section>")
	if end < 0 {
		t.Fatal("financial panel not closed")
	}
	return html[start : start+end]
}

func seedReserveBalanceViewModelScenario(t *testing.T, db *sql.DB) {
	t.Helper()
	now := time.Now().UTC().Unix()

	execTestSQL(t, db, `
		INSERT INTO users (id, name, email, password_hash, created_at, updated_at)
		VALUES
			('user-a', 'User A', 'user-a@exemplo.com', 'hash', ?, ?),
			('user-b', 'User B', 'user-b@exemplo.com', 'hash', ?, ?)
	`, now, now, now, now)
	execTestSQL(t, db, `
		INSERT INTO workspaces (id, name, description, type, created_at, updated_at)
		VALUES
			('ws-a', 'Workspace A', '', 'personal', ?, ?),
			('ws-b', 'Workspace B', '', 'personal', ?, ?)
	`, now, now, now, now)
	execTestSQL(t, db, `
		INSERT INTO workspace_members (workspace_id, user_id, role, joined_at)
		VALUES
			('ws-a', 'user-a', 'ADMIN', ?),
			('ws-b', 'user-b', 'ADMIN', ?)
	`, now, now)
	execTestSQL(t, db, `
		INSERT INTO categories (id, workspace_id, name, type, created_at)
		VALUES
			('a-cat-1', 'ws-a', 'A Cat 1', 'EXPENSE', ?),
			('a-cat-2', 'ws-a', 'A Cat 2', 'EXPENSE', ?),
			('b-cat-1', 'ws-b', 'B Cat 1', 'EXPENSE', ?)
	`, now, now, now)
	execTestSQL(t, db, `
		INSERT INTO accounts (id, workspace_id, name, type, initial_balance, current_balance, created_at, updated_at)
		VALUES
			('a-check', 'ws-a', 'Conta A', 'CHECKING', 10000, 10000, ?, ?),
			('a-save', 'ws-a', 'Poupança A', 'SAVINGS', 5000, 5000, ?, ?),
			('a-card', 'ws-a', 'Card A', 'CREDIT_CARD', 0, 90000, ?, ?),
			('b-check', 'ws-b', 'Conta B', 'CHECKING', 7000, 7000, ?, ?),
			('b-card', 'ws-b', 'Card B', 'CREDIT_CARD', 0, 2000, ?, ?)
	`, now, now, now, now, now, now, now, now, now, now)
	execTestSQL(t, db, `
		INSERT INTO boxes (id, workspace_id, name, category_id, target_amount, monthly_recharge, created_at, updated_at)
		VALUES
			('a-box-1', 'ws-a', 'A Box 1', 'a-cat-1', 0, 0, ?, ?),
			('a-box-2', 'ws-a', 'A Box 2', 'a-cat-2', 0, 0, ?, ?),
			('b-box-1', 'ws-b', 'B Box 1', 'b-cat-1', 0, 0, ?, ?)
	`, now, now, now, now, now, now)
	execTestSQL(t, db, `
		INSERT INTO box_virtual_ledger (id, box_id, amount, type, description, reference_date, created_at)
		VALUES
			('a-ledger-1', 'a-box-1', 1200, 'RECHARGE', 'Aporte', ?, ?),
			('a-ledger-2', 'a-box-2', 800, 'RECHARGE', 'Aporte', ?, ?),
			('b-ledger-1', 'b-box-1', 9000, 'RECHARGE', 'Aporte', ?, ?)
	`, now, now, now, now, now, now)
}
