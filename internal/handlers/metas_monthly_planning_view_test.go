package handlers

import (
	"database/sql"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/contabase-app/contabase/internal/services"
)

func seedMonthlyPlanningScenario(t *testing.T, db *sql.DB) {
	t.Helper()
	now := time.Now().UTC().Unix()

	execTestSQL(t, db, `
		INSERT INTO users (id, name, email, password_hash, created_at, updated_at)
		VALUES
			('user-a', 'User A', 'user-a@exemplo.com', 'hash', ?, ?),
			('user-b', 'User B', 'user-b@exemplo.com', 'hash', ?, ?)
	`, now, now, now, now)
	execTestSQL(t, db, `
		INSERT INTO workspaces (id, name, description, type, created_at, updated_at,
			monthly_income_reference, monthly_total_cost_reference, monthly_essential_cost_reference)
		VALUES
			('ws-full', 'Full Planning', '', 'personal', ?, ?,
				500000, 300000, 70000),
			('ws-no-essential', 'No Essential', '', 'personal', ?, ?,
				500000, 300000, NULL),
			('ws-no-references', 'No References', '', 'personal', ?, ?,
				NULL, NULL, NULL)
	`, now, now, now, now, now, now)
	execTestSQL(t, db, `
		INSERT INTO workspace_members (workspace_id, user_id, role, joined_at)
		VALUES
			('ws-full', 'user-a', 'ADMIN', ?),
			('ws-no-essential', 'user-a', 'ADMIN', ?),
			('ws-no-references', 'user-a', 'ADMIN', ?)
	`, now, now, now)

	execTestSQL(t, db, `
		INSERT INTO categories (id, workspace_id, name, type, created_at)
		VALUES
			('cat-full-1', 'ws-full', 'Cat 1', 'EXPENSE', ?),
			('cat-full-2', 'ws-full', 'Cat 2', 'EXPENSE', ?),
			('cat-full-3', 'ws-full', 'Cat 3', 'EXPENSE', ?),
			('cat-full-4', 'ws-full', 'Cat 4', 'EXPENSE', ?),
			('cat-essential-1', 'ws-no-essential', 'Cat E1', 'EXPENSE', ?)
	`, now, now, now, now, now)

	execTestSQL(t, db, `
		INSERT INTO accounts (id, workspace_id, name, type, initial_balance, current_balance, created_at, updated_at)
		VALUES
			('acc-full-check', 'ws-full', 'Conta Corrente', 'CHECKING', 470000, 470000, ?, ?),
			('acc-essential-check', 'ws-no-essential', 'Conta Corrente', 'CHECKING', 470000, 470000, ?, ?)
	`, now, now, now, now)

	execTestSQL(t, db, `
		INSERT INTO boxes (id, workspace_id, name, category_id, target_amount, monthly_recharge,
			continue_after_target, automatic_contribution_state, created_at, updated_at)
		VALUES
			('box-full-a', 'ws-full', 'Box A', 'cat-full-1', 0, 10000, 0, 'ACTIVE', ?, ?),
			('box-full-b', 'ws-full', 'Box B', 'cat-full-2', 0, 1000, 1, 'ACTIVE', ?, ?),
			('box-full-c', 'ws-full', 'Box C', 'cat-full-3', 10000, 1000, 0, 'ACTIVE', ?, ?),
			('box-full-d', 'ws-full', 'Box D', 'cat-full-4', 0, 5000, 0, 'BLOCKED', ?, ?)
	`, now, now, now, now, now, now, now, now)

	execTestSQL(t, db, `
		INSERT INTO box_virtual_ledger (id, box_id, amount, type, description, reference_date, created_at)
		VALUES
			('l-full-a', 'box-full-a', 5000, 'RECHARGE', 'seed', ?, ?),
			('l-full-b', 'box-full-b', 500, 'RECHARGE', 'seed', ?, ?),
			('l-full-c', 'box-full-c', 9500, 'RECHARGE', 'seed', ?, ?),
			('l-full-d', 'box-full-d', 2000, 'RECHARGE', 'seed', ?, ?)
	`, now, now, now, now, now, now, now, now)
}

func TestMetasMonthlyPlanningFullSnapshot(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	seedMonthlyPlanningScenario(t, db)

	h := MetasHandler{ActorRole: testMetasAdminRole, DB: db, WorkspaceID: "ws-full", UserID: "user-a"}
	data, err := h.buildMetasData("", "caixinhas")
	if err != nil {
		t.Fatalf("buildMetasData: %v", err)
	}

	if !data.HasMonthlyPlanning {
		t.Fatal("HasMonthlyPlanning = false, want true for ws-full with all references")
	}

	if got := moneyDisplayToCentsForTest(t, data.ReservedBalance); got != 17000 {
		t.Fatalf("ReservedBalance = %d, want 17000 (5000+500+9500+2000 ledger)", got)
	}

	if got := moneyDisplayToCentsForTest(t, data.FreeBalance); got != 453000 {
		t.Fatalf("FreeBalance = %d, want 453000 (470000-17000)", got)
	}

	progCents := moneyDisplayToCentsForTest(t, data.ProgrammedContributions)
	if progCents != 11500 {
		t.Fatalf("ProgrammedContributions = %d, want 11500 (boxA:10000 + boxB:1000 + boxC:500 partial + boxD:0 BLOCKED)", progCents)
	}

	if !data.HasMargin {
		t.Fatal("HasMargin = false, want true (income 500000, cost 300000, prog 11500 → margin 188500)")
	}
	marginCents := moneyDisplayToCentsForTest(t, data.Margin)
	if marginCents != 188500 {
		t.Fatalf("Margin = %d, want 188500", marginCents)
	}

	if !data.HasCoverage {
		t.Fatal("HasCoverage = false, want true (essential 70000, free 453000)")
	}
	if !strings.Contains(data.CoverageText, "Cobre cerca de 6,5") {
		t.Fatalf("CoverageText = %q, expected to contain 'Cobre cerca de 6,5'", data.CoverageText)
	}
	if data.CoverageSourceText != "Baseado no custo essencial mensal configurado." {
		t.Fatalf("CoverageSourceText = %q, want manual reference explanation", data.CoverageSourceText)
	}
}

func TestMetasMonthlyPlanningRendersFullAndEmptyStates(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	seedMonthlyPlanningScenario(t, db)

	t.Run("full summary", func(t *testing.T) {
		h := MetasHandler{ActorRole: testMetasAdminRole,
			DB:          db,
			Templates:   testMetasPageTemplates(t),
			WorkspaceID: "ws-full",
			UserID:      "user-a",
		}
		req := httptest.NewRequest(http.MethodGet, "/metas?aba=reservas", nil)
		rr := httptest.NewRecorder()

		h.HandleListarMetas(rr, req)
		if rr.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200, body=%s", rr.Code, rr.Body.String())
		}
		body := rr.Body.String()
		assertContains(t, body, "Cobre cerca de 6,5 meses")
		assertContains(t, body, "Baseado no custo essencial mensal configurado.")
		assertContains(t, body, "Aportes programados")
		assertContains(t, body, "Margem mensal estimada: <span data-sensitive>R$ 1.885,00</span>")
	})

	t.Run("empty workspace", func(t *testing.T) {
		h := MetasHandler{ActorRole: testMetasAdminRole,
			DB:          db,
			Templates:   testMetasPageTemplates(t),
			WorkspaceID: "ws-no-references",
			UserID:      "user-a",
		}
		req := httptest.NewRequest(http.MethodGet, "/metas?aba=reservas", nil)
		rr := httptest.NewRecorder()

		h.HandleListarMetas(rr, req)
		if rr.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200, body=%s", rr.Code, rr.Body.String())
		}
		body := rr.Body.String()
		assertContains(t, body, "Nenhuma reserva criada")
		if strings.Contains(body, "Cobre cerca de") || strings.Contains(body, "Baseado no custo essencial") {
			t.Fatalf("empty workspace rendered unavailable coverage: %s", body)
		}
	})
}

func TestMetasMonthlyPlanningNoEssentialReference(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	seedMonthlyPlanningScenario(t, db)

	h := MetasHandler{ActorRole: testMetasAdminRole, DB: db, WorkspaceID: "ws-no-essential", UserID: "user-a"}
	data, err := h.buildMetasData("", "caixinhas")
	if err != nil {
		t.Fatalf("buildMetasData: %v", err)
	}

	if !data.HasMonthlyPlanning {
		t.Fatal("HasMonthlyPlanning = false, want true (income and cost references present)")
	}

	if !data.HasMargin {
		t.Fatal("HasMargin = false, want true (income and cost available)")
	}

	if data.HasCoverage {
		t.Fatal("HasCoverage = true, want false (no essential cost reference)")
	}

	if data.CoverageText != "" {
		t.Fatalf("CoverageText = %q, want empty", data.CoverageText)
	}
	if data.CoverageSourceText != "" {
		t.Fatalf("CoverageSourceText = %q, want empty", data.CoverageSourceText)
	}
}

func TestMetasMonthlyPlanningNoReferences(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	seedMonthlyPlanningScenario(t, db)

	h := MetasHandler{ActorRole: testMetasAdminRole, DB: db, WorkspaceID: "ws-no-references", UserID: "user-a"}
	data, err := h.buildMetasData("", "caixinhas")
	if err != nil {
		t.Fatalf("buildMetasData: %v", err)
	}

	if !data.HasMonthlyPlanning {
		t.Fatal("HasMonthlyPlanning = false, want true (snapshot computed even without references)")
	}

	if data.HasMargin {
		t.Fatal("HasMargin = true, want false (no income or cost references)")
	}

	if data.HasCoverage {
		t.Fatal("HasCoverage = true, want false (no essential cost reference)")
	}
}

func TestMetasMonthlyPlanningSnapshotErrorDoesNotBreak(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	execTestSQL(t, db, `
		INSERT INTO users (id, name, email, password_hash, created_at, updated_at)
		VALUES ('user-x', 'User X', 'x@exemplo.com', 'hash', ?, ?)
	`, time.Now().UTC().Unix(), time.Now().UTC().Unix())

	h := MetasHandler{ActorRole: testMetasAdminRole, DB: db, WorkspaceID: "ws-missing", UserID: "user-x"}
	data, err := h.buildMetasData("", "caixinhas")
	if err != nil {
		t.Fatalf("buildMetasData should not break on missing workspace: %v", err)
	}
	if data.HasMonthlyPlanning {
		t.Fatal("HasMonthlyPlanning = true for missing workspace, want false")
	}
}

func TestMetasMonthlyPlanningReadOnly(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	seedMonthlyPlanningScenario(t, db)

	var changesBefore int64
	if err := db.QueryRow(`SELECT total_changes()`).Scan(&changesBefore); err != nil {
		t.Fatalf("total_changes before: %v", err)
	}

	h := MetasHandler{ActorRole: testMetasAdminRole, DB: db, WorkspaceID: "ws-full", UserID: "user-a"}
	_, err := h.buildMetasData("", "caixinhas")
	if err != nil {
		t.Fatalf("buildMetasData: %v", err)
	}

	var changesAfter int64
	if err := db.QueryRow(`SELECT total_changes()`).Scan(&changesAfter); err != nil {
		t.Fatalf("total_changes after: %v", err)
	}
	if changesAfter != changesBefore {
		t.Fatalf("buildMetasData with monthly planning wrote %d changes, want 0", changesAfter-changesBefore)
	}
}

func TestMetasMonthlyPlanningCrossWorkspaceIsolation(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	seedMonthlyPlanningScenario(t, db)

	execTestSQL(t, db, `
		INSERT INTO categories (id, workspace_id, name, type, created_at)
		VALUES ('cat-other-1', 'ws-no-essential', 'Other Cat', 'EXPENSE', ?)
	`, time.Now().UTC().Unix())
	execTestSQL(t, db, `
		INSERT INTO boxes (id, workspace_id, name, category_id, target_amount, monthly_recharge,
			automatic_contribution_state, created_at, updated_at)
		VALUES ('box-essential', 'ws-no-essential', 'Essential Box', 'cat-essential-1', 0, 50000, 'ACTIVE', ?, ?)
	`, time.Now().UTC().Unix(), time.Now().UTC().Unix())

	hFull := MetasHandler{ActorRole: testMetasAdminRole, DB: db, WorkspaceID: "ws-full", UserID: "user-a"}
	dataFull, err := hFull.buildMetasData("", "caixinhas")
	if err != nil {
		t.Fatalf("buildMetasData ws-full: %v", err)
	}
	fullProg := moneyDisplayToCentsForTest(t, dataFull.ProgrammedContributions)
	if fullProg != 11500 {
		t.Fatalf("ws-full ProgrammedContributions = %d, want 11500 (should not leak from other ws)", fullProg)
	}

	hEssential := MetasHandler{ActorRole: testMetasAdminRole, DB: db, WorkspaceID: "ws-no-essential", UserID: "user-a"}
	dataEssential, err := hEssential.buildMetasData("", "caixinhas")
	if err != nil {
		t.Fatalf("buildMetasData ws-no-essential: %v", err)
	}
	essentialProg := moneyDisplayToCentsForTest(t, dataEssential.ProgrammedContributions)
	if essentialProg != 50000 {
		t.Fatalf("ws-no-essential ProgrammedContributions = %d, want 50000", essentialProg)
	}
}

func TestFormatCoverageText(t *testing.T) {
	tests := []struct {
		tenths int64
		want   string
	}{
		{68, "Cobre cerca de 6,8 meses"},
		{10, "Cobre cerca de 1,0 mês"},
		{0, "Cobre cerca de 0,0 mês"},
		{5, "Cobre cerca de 0,5 mês"},
		{20, "Cobre cerca de 2,0 meses"},
		{386, "Cobre cerca de 38,6 meses"},
	}
	for _, tt := range tests {
		t.Run(fmt.Sprintf("tenths_%d", tt.tenths), func(t *testing.T) {
			got := formatCoverageText(tt.tenths)
			if got != tt.want {
				t.Fatalf("formatCoverageText(%d) = %q, want %q", tt.tenths, got, tt.want)
			}
		})
	}
}

func TestFormatCoverageSourceText(t *testing.T) {
	tests := []struct {
		source           services.MonthlyReferenceSource
		historicalMonths int
		want             string
	}{
		{services.MonthlyReferenceSourceHistorical, 6, "Baseado na média dos custos essenciais dos últimos 6 meses completos."},
		{services.MonthlyReferenceSourceHistorical, 3, "Baseado na média dos custos essenciais dos últimos 3 meses completos."},
		{services.MonthlyReferenceSourceManual, 0, "Baseado no custo essencial mensal configurado."},
		{services.MonthlyReferenceSourceUnavailable, 0, "Estimativa baseada no custo essencial mensal de referência."},
	}
	for _, tt := range tests {
		t.Run(fmt.Sprintf("source_%s", tt.source), func(t *testing.T) {
			got := formatCoverageSourceText(tt.source, tt.historicalMonths)
			if got != tt.want {
				t.Fatalf("formatCoverageSourceText(%s, %d) = %q, want %q", tt.source, tt.historicalMonths, got, tt.want)
			}
		})
	}
}

func TestMetasMonthlyPlanningMarginDisplay(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()

	now := time.Now().UTC().Unix()
	execTestSQL(t, db, `
		INSERT INTO users (id, name, email, password_hash, created_at, updated_at)
		VALUES ('user-m', 'Margin User', 'm@exemplo.com', 'hash', ?, ?)
	`, now, now)
	execTestSQL(t, db, `
		INSERT INTO workspaces (id, name, description, type, created_at, updated_at,
			monthly_income_reference, monthly_total_cost_reference)
		VALUES
			('ws-margin-pos', 'Positive', '', 'personal', ?, ?, 500000, 200000),
			('ws-margin-zero', 'Zero', '', 'personal', ?, ?, 300000, 300000),
			('ws-margin-neg', 'Negative', '', 'personal', ?, ?, 100000, 200000)
	`, now, now, now, now, now, now)
	execTestSQL(t, db, `
		INSERT INTO workspace_members (workspace_id, user_id, role, joined_at)
		VALUES
			('ws-margin-pos', 'user-m', 'ADMIN', ?),
			('ws-margin-zero', 'user-m', 'ADMIN', ?),
			('ws-margin-neg', 'user-m', 'ADMIN', ?)
	`, now, now, now)

	for _, ws := range []string{"ws-margin-pos", "ws-margin-zero", "ws-margin-neg"} {
		execTestSQL(t, db, `
			INSERT INTO categories (id, workspace_id, name, type, created_at)
			VALUES (?, ?, ?, 'EXPENSE', ?)
		`, ws+"-cat", ws, "Cat", now)
	}

	makeTest := func(wsID string, wantMargin int64, wantHasMargin bool) func(t *testing.T) {
		return func(t *testing.T) {
			h := MetasHandler{ActorRole: testMetasAdminRole, DB: db, WorkspaceID: wsID, UserID: "user-m"}
			data, err := h.buildMetasData("", "caixinhas")
			if err != nil {
				t.Fatalf("buildMetasData %s: %v", wsID, err)
			}
			if data.HasMargin != wantHasMargin {
				t.Fatalf("HasMargin = %v, want %v", data.HasMargin, wantHasMargin)
			}
			if wantHasMargin {
				got := moneyDisplayToCentsForTest(t, data.Margin)
				if got != wantMargin {
					t.Fatalf("Margin = %d, want %d", got, wantMargin)
				}
			}
		}
	}
	t.Run("positive", makeTest("ws-margin-pos", 300000, true))
	t.Run("zero", makeTest("ws-margin-zero", 0, true))
	t.Run("negative", makeTest("ws-margin-neg", -100000, true))
}

func TestMetasMonthlyPlanningZeroContributions(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()

	now := time.Now().UTC().Unix()
	execTestSQL(t, db, `
		INSERT INTO users (id, name, email, password_hash, created_at, updated_at)
		VALUES ('user-z', 'Zero User', 'z@exemplo.com', 'hash', ?, ?)
	`, now, now)
	execTestSQL(t, db, `
		INSERT INTO workspaces (id, name, description, type, created_at, updated_at,
			monthly_income_reference, monthly_total_cost_reference, monthly_essential_cost_reference)
		VALUES ('ws-zero', 'Zero Contrib', '', 'personal', ?, ?,
			500000, 300000, 70000)
	`, now, now)
	execTestSQL(t, db, `
		INSERT INTO workspace_members (workspace_id, user_id, role, joined_at)
		VALUES ('ws-zero', 'user-z', 'ADMIN', ?)
	`, now)

	h := MetasHandler{ActorRole: testMetasAdminRole, DB: db, WorkspaceID: "ws-zero", UserID: "user-z"}
	data, err := h.buildMetasData("", "caixinhas")
	if err != nil {
		t.Fatalf("buildMetasData: %v", err)
	}

	progCents := moneyDisplayToCentsForTest(t, data.ProgrammedContributions)
	if progCents != 0 {
		t.Fatalf("ProgrammedContributions = %d, want 0", progCents)
	}
}

func TestMetasMonthlyPlanningReservedAndAvailablePreserved(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	seedMonthlyPlanningScenario(t, db)

	h := MetasHandler{ActorRole: testMetasAdminRole, DB: db, WorkspaceID: "ws-full", UserID: "user-a"}
	data, err := h.buildMetasData("", "caixinhas")
	if err != nil {
		t.Fatalf("buildMetasData: %v", err)
	}

	reservedCents := moneyDisplayToCentsForTest(t, data.ReservedBalance)
	if reservedCents != 17000 {
		t.Fatalf("ReservedBalance = %d, want 17000 (unchanged from previous known value)", reservedCents)
	}

	freeCents := moneyDisplayToCentsForTest(t, data.FreeBalance)
	if freeCents != 453000 {
		t.Fatalf("FreeBalance = %d, want 453000 (470000-17000, unchanged)", freeCents)
	}
}

func TestMetasMonthlyPlanningProgrammedContributionsIntegration(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()

	now := time.Now().UTC().Unix()
	execTestSQL(t, db, `
		INSERT INTO users (id, name, email, password_hash, created_at, updated_at)
		VALUES ('user-i', 'Integration User', 'i@exemplo.com', 'hash', ?, ?)
	`, now, now)
	execTestSQL(t, db, `
		INSERT INTO workspaces (id, name, description, type, created_at, updated_at,
			monthly_income_reference, monthly_total_cost_reference, monthly_essential_cost_reference)
		VALUES ('ws-integration', 'Integration', '', 'personal', ?, ?,
			500000, 300000, 70000)
	`, now, now)
	execTestSQL(t, db, `
		INSERT INTO workspace_members (workspace_id, user_id, role, joined_at)
		VALUES ('ws-integration', 'user-i', 'ADMIN', ?)
	`, now)
	execTestSQL(t, db, `
		INSERT INTO accounts (id, workspace_id, name, type, initial_balance, current_balance, created_at, updated_at)
		VALUES ('acc-integration', 'ws-integration', 'Conta', 'CHECKING', 500000, 500000, ?, ?)
	`, now, now)
	execTestSQL(t, db, `
		INSERT INTO categories (id, workspace_id, name, type, created_at)
		VALUES
			('int-cat-a', 'ws-integration', 'Cat A', 'EXPENSE', ?),
			('int-cat-b', 'ws-integration', 'Cat B', 'EXPENSE', ?),
			('int-cat-c', 'ws-integration', 'Cat C', 'EXPENSE', ?),
			('int-cat-d', 'ws-integration', 'Cat D', 'EXPENSE', ?)
	`, now, now, now, now)
	execTestSQL(t, db, `
		INSERT INTO boxes (id, workspace_id, name, category_id, target_amount, monthly_recharge,
			continue_after_target, automatic_contribution_state, created_at, updated_at)
		VALUES
			('int-a', 'ws-integration', 'Box A', 'int-cat-a', 0, 10000, 0, 'ACTIVE', ?, ?),
			('int-b', 'ws-integration', 'Box B', 'int-cat-b', 0, 1000, 1, 'ACTIVE', ?, ?),
			('int-c', 'ws-integration', 'Box C', 'int-cat-c', 10000, 1000, 0, 'ACTIVE', ?, ?),
			('int-d', 'ws-integration', 'Box D', 'int-cat-d', 0, 5000, 0, 'BLOCKED', ?, ?)
	`, now, now, now, now, now, now, now, now)
	execTestSQL(t, db, `
		INSERT INTO box_virtual_ledger (id, box_id, amount, type, description, reference_date, created_at)
		VALUES
			('l-int-a', 'int-a', 100, 'RECHARGE', 'seed', ?, ?),
			('l-int-c', 'int-c', 9500, 'RECHARGE', 'seed', ?, ?)
	`, now, now, now, now)

	h := MetasHandler{ActorRole: testMetasAdminRole, DB: db, WorkspaceID: "ws-integration", UserID: "user-i"}
	data, err := h.buildMetasData("", "caixinhas")
	if err != nil {
		t.Fatalf("buildMetasData: %v", err)
	}

	programmedFormatted := fmt.Sprintf("R$ %s%s", data.ProgrammedContributions.Reais, data.ProgrammedContributions.Cents)
	if !strings.Contains(programmedFormatted, "115,00") {
		t.Fatalf("Programmed contributions formatted = %q, want R$ 115,00", programmedFormatted)
	}
}

func TestMetasMonthlyPlanningZeroCoverage(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()

	now := time.Now().UTC().Unix()
	execTestSQL(t, db, `
		INSERT INTO users (id, name, email, password_hash, created_at, updated_at)
		VALUES ('user-cov', 'Cov User', 'cov@exemplo.com', 'hash', ?, ?)
	`, now, now)
	execTestSQL(t, db, `
		INSERT INTO workspaces (id, name, description, type, created_at, updated_at,
			monthly_income_reference, monthly_total_cost_reference, monthly_essential_cost_reference)
		VALUES ('ws-cov-zero', 'Zero Cov', '', 'personal', ?, ?,
			50000, 30000, 50000)
	`, now, now)
	execTestSQL(t, db, `
		INSERT INTO workspace_members (workspace_id, user_id, role, joined_at)
		VALUES ('ws-cov-zero', 'user-cov', 'ADMIN', ?)
	`, now)

	h := MetasHandler{ActorRole: testMetasAdminRole, DB: db, WorkspaceID: "ws-cov-zero", UserID: "user-cov"}
	data, err := h.buildMetasData("", "caixinhas")
	if err != nil {
		t.Fatalf("buildMetasData: %v", err)
	}

	if !data.HasCoverage {
		t.Fatal("HasCoverage = false, want true (essential reference present, free balance zero)")
	}
	if !strings.Contains(data.CoverageText, "Cobre cerca de 0,0") {
		t.Fatalf("CoverageText = %q, expected 'Cobre cerca de 0,0'", data.CoverageText)
	}
}

func TestMetasMonthlyPlanningHTMXFragmentsIncludeData(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	seedMonthlyPlanningScenario(t, db)

	h := &MetasHandler{ActorRole: testMetasAdminRole, DB: db, WorkspaceID: "ws-full", UserID: "user-a"}
	data, err := h.buildMetasData("", "caixinhas")
	if err != nil {
		t.Fatalf("buildMetasData: %v", err)
	}

	if !data.HasMonthlyPlanning {
		t.Fatal("HasMonthlyPlanning = false, want true for HTMX data build")
	}
	if moneyDisplayToCentsForTest(t, data.ProgrammedContributions) != 11500 {
		t.Fatalf("Programmed contributions = %d, want 11500 in HTMX data build",
			moneyDisplayToCentsForTest(t, data.ProgrammedContributions))
	}
	if !data.HasMargin {
		t.Fatal("HasMargin = false for HTMX data build")
	}
	if !data.HasCoverage {
		t.Fatal("HasCoverage = false for HTMX data build")
	}
	if data.CoverageText == "" {
		t.Fatal("CoverageText empty in HTMX data build")
	}
	if data.CoverageSourceText == "" {
		t.Fatal("CoverageSourceText empty in HTMX data build")
	}
}
