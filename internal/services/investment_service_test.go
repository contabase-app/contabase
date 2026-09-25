package services

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"math"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/contabase-app/contabase/internal/database"
	"github.com/contabase-app/contabase/internal/models"
	"github.com/google/uuid"
	"modernc.org/sqlite"
)

// --- fixtures -----------------------------------------------------------------

func seedInvestmentServiceFixtures(t *testing.T, db *sql.DB) {
	t.Helper()
	now := time.Now().Unix()
	exec(t, db, `INSERT INTO users (id, name, email, password_hash) VALUES ('user-a', 'Owner A', 'a@example.test', 'hash')`)
	exec(t, db, `INSERT INTO users (id, name, email, password_hash) VALUES ('user-b', 'Owner B', 'b@example.test', 'hash')`)
	exec(t, db, `INSERT INTO workspaces (id, name) VALUES ('ws-a', 'WS A')`)
	exec(t, db, `INSERT INTO workspaces (id, name) VALUES ('ws-b', 'WS B')`)
	exec(t, db, `INSERT INTO accounts (id, workspace_id, name, type, initial_balance, current_balance) VALUES ('acc-inv-a', 'ws-a', 'Corretora A', 'INVESTMENT', 0, 0)`)
	exec(t, db, `INSERT INTO accounts (id, workspace_id, name, type, initial_balance, current_balance) VALUES ('acc-checking-a', 'ws-a', 'Banco A', 'CHECKING', 10000, 10000)`)
	exec(t, db, `INSERT INTO accounts (id, workspace_id, name, type, initial_balance, current_balance, archived_at) VALUES ('acc-inv-archived', 'ws-a', 'Arquivada', 'INVESTMENT', 0, 0, ?)`, now)
	exec(t, db, `INSERT INTO accounts (id, workspace_id, name, type, initial_balance, current_balance) VALUES ('acc-inv-b', 'ws-b', 'Corretora B', 'INVESTMENT', 0, 0)`)
}

func investmentCreateInput(ws, account string) CreateInvestmentInput {
	return CreateInvestmentInput{
		WorkspaceID:    ws,
		AccountID:      account,
		CreatedBy:      "user-a",
		Name:           "CDB Banco X",
		Institution:    "Banco X",
		Kind:           "CDB",
		PrincipalCents: 100000,
		TaxRegime:      models.InvestmentTaxRegimeIRRegressivo,
		AppliedDate:    "2026-01-15",
		YieldNote:      "100% CDI",
	}
}

func mustCreateInvestment(t *testing.T, db *sql.DB, input CreateInvestmentInput) models.Investment {
	t.Helper()
	created, err := CreateInvestment(context.Background(), db, input)
	if err != nil {
		t.Fatalf("create investment: %v", err)
	}
	return created
}

func insertRawInvestment(t *testing.T, db *sql.DB, id, ws, account, status string, archivedAt *int64) {
	t.Helper()
	var redeemedDate, redeemedBy, archived any
	if status == "RESGATADA" {
		redeemedDate = "2026-06-30"
		redeemedBy = "user-a"
	}
	if archivedAt != nil {
		archived = *archivedAt
	}
	exec(t, db, `INSERT INTO investments (
		id, workspace_id, account_id, name, institution, kind, principal_cents,
		tax_regime, applied_date, maturity_date, yield_note, status, redeemed_date, redeemed_by,
		archived_at, created_by, created_at, updated_at
	) VALUES (?, ?, ?, ?, '', 'CDB', 100000, 'MANUAL', '2026-01-10', NULL, '', ?, ?, ?, ?, 'user-a', 100, 100)`,
		id, ws, account, "Lote "+id, status, redeemedDate, redeemedBy, archived)
}

func countRows(t *testing.T, db *sql.DB, query string) int {
	t.Helper()
	var count int
	if err := db.QueryRow(query).Scan(&count); err != nil {
		t.Fatal(err)
	}
	return count
}

func queryScalar(t *testing.T, db *sql.DB, query string) string {
	t.Helper()
	var value string
	if err := db.QueryRow(query).Scan(&value); err != nil {
		t.Fatal(err)
	}
	return value
}

func insertValuationRow(t *testing.T, db *sql.DB, id, ws, investmentID, date string, amount int64) {
	t.Helper()
	exec(t, db, `INSERT INTO investment_valuations (
		id, workspace_id, investment_id, valuation_date, amount_cents, created_by, created_at
	) VALUES (?, ?, ?, ?, ?, 'user-a', 200)`, id, ws, investmentID, date, amount)
}

// --- create ---------------------------------------------------------------------

func TestInvestmentCreatePersistsActiveLot(t *testing.T) {
	db := openDB(t)
	defer db.Close()
	seedInvestmentServiceFixtures(t, db)

	created, err := CreateInvestment(context.Background(), db, investmentCreateInput("ws-a", "acc-inv-a"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := uuid.Parse(created.ID); err != nil {
		t.Fatalf("id deve ser uuid: %v", err)
	}
	if created.Status != models.InvestmentStatusAtiva || created.RedeemedDate != nil || created.RedeemedBy != nil || created.ArchivedAt != nil {
		t.Fatalf("lote recém-criado deve nascer ATIVA e limpo: %+v", created)
	}
	if created.CreatedBy != "user-a" || created.CreatedAt == 0 || created.UpdatedAt != created.CreatedAt {
		t.Fatalf("autoria/timestamps incorretos: %+v", created)
	}

	var status, createdBy string
	var principal int64
	var redeemedDate, redeemedBy, archivedAt sql.NullString
	if err := db.QueryRow(`SELECT status, created_by, principal_cents, redeemed_date, redeemed_by, archived_at
		FROM investments WHERE id = ? AND workspace_id = 'ws-a'`, created.ID).
		Scan(&status, &createdBy, &principal, &redeemedDate, &redeemedBy, &archivedAt); err != nil {
		t.Fatal(err)
	}
	if status != "ATIVA" || createdBy != "user-a" || principal != 100000 {
		t.Fatalf("linha persistida divergente: %s/%s/%d", status, createdBy, principal)
	}
	if redeemedDate.Valid || redeemedBy.Valid || archivedAt.Valid {
		t.Fatalf("campos de resgate/arquivo devem nascer NULL: %v/%v/%v", redeemedDate, redeemedBy, archivedAt)
	}
}

func TestInvestmentCreateValidationAndAccountRules(t *testing.T) {
	db := openDB(t)
	defer db.Close()
	seedInvestmentServiceFixtures(t, db)

	cases := []struct {
		name    string
		input   CreateInvestmentInput
		wantErr error
	}{
		{"conta CHECKING", investmentCreateInput("ws-a", "acc-checking-a"), ErrInvestmentInvalidAccount},
		{"conta de outro workspace", investmentCreateInput("ws-a", "acc-inv-b"), ErrInvestmentInvalidAccount},
		{"conta arquivada", investmentCreateInput("ws-a", "acc-inv-archived"), ErrInvestmentInvalidAccount},
		{"conta inexistente", investmentCreateInput("ws-a", "acc-fantasma"), ErrInvestmentInvalidAccount},
	}
	for _, tc := range cases {
		if _, err := CreateInvestment(context.Background(), db, tc.input); !errors.Is(err, tc.wantErr) {
			t.Fatalf("%s: err = %v, want %v", tc.name, err, tc.wantErr)
		}
	}

	invalidFields := []struct {
		name   string
		mutate func(*CreateInvestmentInput)
	}{
		{"principal zero", func(i *CreateInvestmentInput) { i.PrincipalCents = 0 }},
		{"principal negativo", func(i *CreateInvestmentInput) { i.PrincipalCents = -1 }},
		{"regime inválido", func(i *CreateInvestmentInput) { i.TaxRegime = "TRIBUTADO" }},
		{"applied inválida (calendário)", func(i *CreateInvestmentInput) { i.AppliedDate = "2026-02-30" }},
		{"applied inválida (formato)", func(i *CreateInvestmentInput) { i.AppliedDate = "15/01/2026" }},
		{"vencimento antes da aplicação", func(i *CreateInvestmentInput) {
			maturity := "2026-01-14"
			i.MaturityDate = &maturity
		}},
		{"vencimento inválida", func(i *CreateInvestmentInput) {
			maturity := "2026-13-01"
			i.MaturityDate = &maturity
		}},
		{"nome vazio", func(i *CreateInvestmentInput) { i.Name = "   " }},
		{"nome longo demais", func(i *CreateInvestmentInput) { i.Name = strings.Repeat("x", 121) }},
		{"kind vazio", func(i *CreateInvestmentInput) { i.Kind = "" }},
		{"kind longo demais", func(i *CreateInvestmentInput) { i.Kind = strings.Repeat("x", 41) }},
		{"sem workspace", func(i *CreateInvestmentInput) { i.WorkspaceID = "" }},
		{"sem autor", func(i *CreateInvestmentInput) { i.CreatedBy = "" }},
	}
	base := investmentCreateInput("ws-a", "acc-inv-a")
	for _, tc := range invalidFields {
		input := base
		tc.mutate(&input)
		_, err := CreateInvestment(context.Background(), db, input)
		if !errors.Is(err, ErrInvestmentValidation) {
			t.Fatalf("%s: err = %v, want ErrInvestmentValidation", tc.name, err)
		}
	}

	if rows := countRows(t, db, `SELECT COUNT(*) FROM investments`); rows != 0 {
		t.Fatalf("nenhum input inválido deveria persistir, existem %d linhas", rows)
	}

	// maturity vazia é tratada como ausente, e espaço em branco é aparado.
	input := base
	blank := "   "
	input.MaturityDate = &blank
	created := mustCreateInvestment(t, db, input)
	if created.MaturityDate != nil {
		t.Fatalf("maturity em branco deve virar NULL, got %v", *created.MaturityDate)
	}
}

// --- get --------------------------------------------------------------------------

func TestInvestmentGetCrossTenantIndistinguishable(t *testing.T) {
	db := openDB(t)
	defer db.Close()
	seedInvestmentServiceFixtures(t, db)
	created := mustCreateInvestment(t, db, investmentCreateInput("ws-a", "acc-inv-a"))

	got, err := GetInvestment(context.Background(), db, "ws-a", created.ID)
	if err != nil || got.ID != created.ID {
		t.Fatalf("get próprio workspace: %+v, %v", got, err)
	}
	// Outro workspace e id inexistente são o MESMO erro: não vazam existência.
	if _, err := GetInvestment(context.Background(), db, "ws-b", created.ID); !errors.Is(err, ErrInvestmentNotFound) {
		t.Fatalf("cross-tenant: err = %v, want ErrInvestmentNotFound", err)
	}
	if _, err := GetInvestment(context.Background(), db, "ws-a", "inv-fantasma"); !errors.Is(err, ErrInvestmentNotFound) {
		t.Fatalf("inexistente: err = %v, want ErrInvestmentNotFound", err)
	}
	if _, err := GetInvestment(context.Background(), db, "", created.ID); !errors.Is(err, ErrInvestmentNotFound) {
		t.Fatalf("workspace vazio deve falhar fechado, got %v", err)
	}
}

func TestInvestmentGetReadsRedeemedAndArchived(t *testing.T) {
	db := openDB(t)
	defer db.Close()
	seedInvestmentServiceFixtures(t, db)
	archived := int64(500)
	insertRawInvestment(t, db, "inv-resg", "ws-a", "acc-inv-a", "RESGATADA", nil)
	insertRawInvestment(t, db, "inv-arch", "ws-a", "acc-inv-a", "RESGATADA", &archived)

	for _, id := range []string{"inv-resg", "inv-arch"} {
		got, err := GetInvestment(context.Background(), db, "ws-a", id)
		if err != nil {
			t.Fatalf("%s deve permanecer legível: %v", id, err)
		}
		if got.Status != "RESGATADA" {
			t.Fatalf("%s status = %s", id, got.Status)
		}
	}
}

// --- list --------------------------------------------------------------------------

func TestInvestmentListFiltersOrderingAndIsolation(t *testing.T) {
	db := openDB(t)
	defer db.Close()
	seedInvestmentServiceFixtures(t, db)

	// ATIVAS em ws-a: ordenação maturity ASC NULLS LAST, applied DESC, id ASC.
	a1 := mustCreateInvestment(t, db, func() CreateInvestmentInput {
		in := investmentCreateInput("ws-a", "acc-inv-a")
		in.Name = "Lote jun"
		in.AppliedDate = "2026-01-01"
		maturity := "2026-06-01"
		in.MaturityDate = &maturity
		return in
	}())
	a2 := mustCreateInvestment(t, db, func() CreateInvestmentInput {
		in := investmentCreateInput("ws-a", "acc-inv-a")
		in.Name = "Lote dez A"
		in.AppliedDate = "2026-02-01"
		maturity := "2026-12-01"
		in.MaturityDate = &maturity
		return in
	}())
	a3 := mustCreateInvestment(t, db, func() CreateInvestmentInput {
		in := investmentCreateInput("ws-a", "acc-inv-a")
		in.Name = "Lote dez B (empate)"
		in.AppliedDate = "2026-02-01"
		maturity := "2026-12-01"
		in.MaturityDate = &maturity
		return in
	}())
	// O empate de maturity + applied é resolvido por id ASC; como UUIDs são
	// aleatórios, a expectativa deriva do próprio id (a ordenação SQL segue
	// determinística).
	tieFirst, tieSecond := a2.ID, a3.ID
	if tieFirst > tieSecond {
		tieFirst, tieSecond = tieSecond, tieFirst
	}
	a4 := mustCreateInvestment(t, db, func() CreateInvestmentInput {
		in := investmentCreateInput("ws-a", "acc-inv-a")
		in.Name = "Lote sem vencimento"
		in.AppliedDate = "2025-12-31"
		return in
	}())
	// RESGATADAS não arquivadas.
	insertRawInvestment(t, db, "list-r1", "ws-a", "acc-inv-a", "RESGATADA", nil)
	insertRawInvestment(t, db, "list-r2", "ws-a", "acc-inv-a", "RESGATADA", nil)
	exec(t, db, `UPDATE investments SET redeemed_date = '2026-01-01' WHERE id = 'list-r2'`)
	exec(t, db, `UPDATE investments SET redeemed_date = '2026-05-01' WHERE id = 'list-r1'`)
	// Arquivadas.
	archivedHigh, archivedLow := int64(900), int64(100)
	insertRawInvestment(t, db, "list-arch1", "ws-a", "acc-inv-a", "RESGATADA", &archivedHigh)
	insertRawInvestment(t, db, "list-arch2", "ws-a", "acc-inv-a", "RESGATADA", &archivedLow)
	// Outro workspace nunca aparece.
	wsB := mustCreateInvestment(t, db, investmentCreateInput("ws-b", "acc-inv-b"))

	ctx := context.Background()
	assertIDs := func(got []models.Investment, want []string) {
		t.Helper()
		ids := make([]string, 0, len(got))
		for _, inv := range got {
			ids = append(ids, inv.ID)
		}
		if strings.Join(ids, ",") != strings.Join(want, ",") {
			t.Fatalf("ordem = %v, want %v", ids, want)
		}
	}

	active, err := ListInvestments(ctx, db, "ws-a", InvestmentListActive)
	if err != nil {
		t.Fatal(err)
	}
	// a1 (06-01) < empate dez (menor id primeiro) < sem vencimento por último.
	assertIDs(active, []string{a1.ID, tieFirst, tieSecond, a4.ID})

	redeemed, err := ListInvestments(ctx, db, "ws-a", InvestmentListRedeemed)
	if err != nil {
		t.Fatal(err)
	}
	// redeemed_date ASC.
	assertIDs(redeemed, []string{"list-r2", "list-r1"})

	archivedList, err := ListInvestments(ctx, db, "ws-a", InvestmentListArchived)
	if err != nil {
		t.Fatal(err)
	}
	// archived_at DESC.
	assertIDs(archivedList, []string{"list-arch1", "list-arch2"})

	all, err := ListInvestments(ctx, db, "ws-a", InvestmentListAll)
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 8 {
		t.Fatalf("TODAS deve trazer os 8 lotes de ws-a, got %d", len(all))
	}
	for _, inv := range all {
		if inv.WorkspaceID != "ws-a" {
			t.Fatalf("vazamento cross-tenant na listagem: %s", inv.ID)
		}
	}

	listB, err := ListInvestments(ctx, db, "ws-b", InvestmentListAll)
	if err != nil {
		t.Fatal(err)
	}
	if len(listB) != 1 || listB[0].ID != wsB.ID {
		t.Fatalf("ws-b deve ver somente o próprio lote: %+v", listB)
	}

	if _, err := ListInvestments(ctx, db, "ws-a", InvestmentListFilter(99)); err == nil {
		t.Fatal("filtro inválido deve falhar fechado")
	}
}

// --- update ------------------------------------------------------------------------

func TestInvestmentUpdateGuardAndImmutability(t *testing.T) {
	db := openDB(t)
	defer db.Close()
	seedInvestmentServiceFixtures(t, db)
	created := mustCreateInvestment(t, db, investmentCreateInput("ws-a", "acc-inv-a"))
	originalUpdatedAt := created.UpdatedAt

	updateInput := func() UpdateInvestmentInput {
		maturity := "2027-06-30"
		return UpdateInvestmentInput{
			WorkspaceID:  "ws-a",
			InvestmentID: created.ID,
			UpdatedAt:    time.Now().Unix() + 60,
			Name:         "CDB Banco Y",
			Institution:  "Banco Y",
			Kind:         "CDB pós-fixado",
			TaxRegime:    models.InvestmentTaxRegimeIsentoPF,
			MaturityDate: &maturity,
			YieldNote:    "105% CDI",
		}
	}
	if err := UpdateInvestment(context.Background(), db, updateInput()); err != nil {
		t.Fatal(err)
	}
	got, err := GetInvestment(context.Background(), db, "ws-a", created.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Name != "CDB Banco Y" || got.Institution != "Banco Y" || got.Kind != "CDB pós-fixado" {
		t.Fatalf("campos editáveis não aplicados: %+v", got)
	}
	if got.TaxRegime != models.InvestmentTaxRegimeIsentoPF || got.MaturityDate == nil || *got.MaturityDate != "2027-06-30" || got.YieldNote != "105% CDI" {
		t.Fatalf("campos editáveis divergentes: %+v", got)
	}
	if got.UpdatedAt <= originalUpdatedAt {
		t.Fatalf("updated_at deve avançar: %d → %d", originalUpdatedAt, got.UpdatedAt)
	}
	// Campos históricos nunca participam do update.
	if got.PrincipalCents != created.PrincipalCents || got.AccountID != created.AccountID ||
		got.AppliedDate != created.AppliedDate || got.CreatedBy != created.CreatedBy ||
		got.Status != models.InvestmentStatusAtiva {
		t.Fatalf("mutação atingiu campo imutável: %+v", got)
	}

	// Update em outro workspace: NotFound e nenhuma alteração.
	cross := updateInput()
	cross.WorkspaceID = "ws-b"
	cross.Name = "Tentativa cross"
	if err := UpdateInvestment(context.Background(), db, cross); !errors.Is(err, ErrInvestmentNotFound) {
		t.Fatalf("cross-tenant: err = %v, want ErrInvestmentNotFound", err)
	}

	// Update em lote RESGATADA: InvalidStatus.
	insertRawInvestment(t, db, "inv-resg", "ws-a", "acc-inv-a", "RESGATADA", nil)
	resgUpdate := updateInput()
	resgUpdate.InvestmentID = "inv-resg"
	if err := UpdateInvestment(context.Background(), db, resgUpdate); !errors.Is(err, ErrInvestmentInvalidStatus) {
		t.Fatalf("resgatada: err = %v, want ErrInvestmentInvalidStatus", err)
	}

	// Inexistente: NotFound.
	ghost := updateInput()
	ghost.InvestmentID = "inv-fantasma"
	if err := UpdateInvestment(context.Background(), db, ghost); !errors.Is(err, ErrInvestmentNotFound) {
		t.Fatalf("inexistente: err = %v, want ErrInvestmentNotFound", err)
	}

	// Validação: vencimento anterior à aplicação lida do banco.
	badMaturity := "2026-01-14"
	bad := updateInput()
	bad.MaturityDate = &badMaturity
	if err := UpdateInvestment(context.Background(), db, bad); !errors.Is(err, ErrInvestmentValidation) {
		t.Fatalf("maturity < applied: err = %v, want ErrInvestmentValidation", err)
	}

	// Nada do que falhou pode ter alterado estado.
	if name := queryScalar(t, db, `SELECT name FROM investments WHERE id = '`+created.ID+`'`); name != "CDB Banco Y" {
		t.Fatalf("update falho alterou estado: %s", name)
	}
	if name := queryScalar(t, db, `SELECT name FROM investments WHERE id = 'inv-resg'`); name != "Lote inv-resg" {
		t.Fatalf("update em resgatada alterou estado: %s", name)
	}
}

// --- current value / valuations ------------------------------------------------------

func TestInvestmentCurrentValueRules(t *testing.T) {
	db := openDB(t)
	defer db.Close()
	seedInvestmentServiceFixtures(t, db)
	ctx := context.Background()
	created := mustCreateInvestment(t, db, investmentCreateInput("ws-a", "acc-inv-a"))

	// Sem valuation: principal.
	current, fromPrincipal, err := GetCurrentInvestmentValue(ctx, db, "ws-a", created.ID)
	if err != nil || !fromPrincipal || current != 100000 {
		t.Fatalf("fallback principal: %d/%v/%v", current, fromPrincipal, err)
	}

	// Com valuations: a mais recente vence; zero é valor legítimo.
	insertValuationRow(t, db, "val-1", "ws-a", created.ID, "2026-02-01", 110000)
	insertValuationRow(t, db, "val-2", "ws-a", created.ID, "2026-03-01", 0)
	current, fromPrincipal, err = GetCurrentInvestmentValue(ctx, db, "ws-a", created.ID)
	if err != nil || fromPrincipal || current != 0 {
		t.Fatalf("valuation zero mais recente deve vencer: %d/%v/%v", current, fromPrincipal, err)
	}

	// Cross-tenant: indistinguível de inexistente, nunca lê valuation alheia.
	if _, _, err := GetCurrentInvestmentValue(ctx, db, "ws-b", created.ID); !errors.Is(err, ErrInvestmentNotFound) {
		t.Fatalf("cross-tenant: err = %v, want ErrInvestmentNotFound", err)
	}
	// ws-b com lote próprio sem valuations: principal de ws-b, nunca 110000/0 de ws-a.
	wsB := mustCreateInvestment(t, db, investmentCreateInput("ws-b", "acc-inv-b"))
	current, fromPrincipal, err = GetCurrentInvestmentValue(ctx, db, "ws-b", wsB.ID)
	if err != nil || !fromPrincipal || current != 100000 {
		t.Fatalf("ws-b isolado: %d/%v/%v", current, fromPrincipal, err)
	}
}

func TestInvestmentValuationsHistoryTenantAware(t *testing.T) {
	db := openDB(t)
	defer db.Close()
	seedInvestmentServiceFixtures(t, db)
	ctx := context.Background()
	created := mustCreateInvestment(t, db, investmentCreateInput("ws-a", "acc-inv-a"))

	if valuations, err := GetInvestmentValuations(ctx, db, "ws-a", created.ID); err != nil || len(valuations) != 0 {
		t.Fatalf("sem histórico: %v/%v", valuations, err)
	}

	insertValuationRow(t, db, "val-b", "ws-a", created.ID, "2026-03-01", 120000)
	insertValuationRow(t, db, "val-a", "ws-a", created.ID, "2026-02-01", 110000)
	valuations, err := GetInvestmentValuations(ctx, db, "ws-a", created.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(valuations) != 2 || valuations[0].ValuationDate != "2026-02-01" || valuations[1].ValuationDate != "2026-03-01" {
		t.Fatalf("histórico deve vir ASC: %+v", valuations)
	}
	for _, val := range valuations {
		if val.WorkspaceID != "ws-a" || val.InvestmentID != created.ID {
			t.Fatalf("linha fora do escopo: %+v", val)
		}
	}

	if _, err := GetInvestmentValuations(ctx, db, "ws-b", created.ID); !errors.Is(err, ErrInvestmentNotFound) {
		t.Fatalf("cross-tenant: err = %v, want ErrInvestmentNotFound", err)
	}
	if _, err := GetInvestmentValuations(ctx, db, "ws-a", "inv-fantasma"); !errors.Is(err, ErrInvestmentNotFound) {
		t.Fatalf("inexistente: err = %v, want ErrInvestmentNotFound", err)
	}
}

func TestInvestmentValuationLifecycleAndLedgerInvariant(t *testing.T) {
	db := openDB(t)
	defer db.Close()
	seedInvestmentServiceFixtures(t, db)
	inv := mustCreateInvestment(t, db, investmentCreateInput("ws-a", "acc-inv-a"))
	exec(t, db, `UPDATE accounts SET current_balance=72345 WHERE id='acc-inv-a'`)
	beforeTransactions := countRows(t, db, `SELECT COUNT(*) FROM transactions`)
	balances := func() string {
		return queryScalar(t, db, `SELECT group_concat(id || ':' || current_balance) FROM (SELECT id,current_balance FROM accounts ORDER BY id)`)
	}
	beforeBalances := balances()
	beforeInvestment, err := GetInvestment(context.Background(), db, "ws-a", inv.ID)
	if err != nil {
		t.Fatal(err)
	}
	current, principal, err := GetCurrentInvestmentValue(context.Background(), db, "ws-a", inv.ID)
	if err != nil || !principal || current != inv.PrincipalCents {
		t.Fatalf("initial current: %d %v %v", current, principal, err)
	}
	if n := countRows(t, db, `SELECT COUNT(*) FROM investment_valuations`); n != 0 {
		t.Fatalf("initial rows=%d", n)
	}
	input := UpsertInvestmentValuationInput{"ws-a", inv.ID, "user-a", "2026-02-01", 110000}
	first, err := UpsertInvestmentValuation(context.Background(), db, input)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := uuid.Parse(first.ID); err != nil {
		t.Fatalf("id is not UUID: %v", err)
	}
	if first.WorkspaceID != input.WorkspaceID || first.InvestmentID != inv.ID || first.ValuationDate != input.ValuationDate || first.AmountCents != 110000 || first.CreatedBy != "user-a" || first.CreatedAt <= 0 || first.UpdatedBy != nil || first.UpdatedAt != nil {
		t.Fatalf("first row: %+v", first)
	}
	current, principal, err = GetCurrentInvestmentValue(context.Background(), db, "ws-a", inv.ID)
	if err != nil || principal || current != 110000 {
		t.Fatalf("valuation current: %d %v %v", current, principal, err)
	}
	input.AmountCents, input.ActingUserID = 90000, "user-b"
	corrected, err := UpsertInvestmentValuation(context.Background(), db, input)
	if err != nil {
		t.Fatal(err)
	}
	if corrected.ID != first.ID || corrected.CreatedBy != first.CreatedBy || corrected.CreatedAt != first.CreatedAt || corrected.AmountCents != 90000 || corrected.UpdatedBy == nil || *corrected.UpdatedBy != "user-b" || corrected.UpdatedAt == nil || *corrected.UpdatedAt < first.CreatedAt {
		t.Fatalf("same-day: first=%+v corrected=%+v", first, corrected)
	}
	if n := countRows(t, db, `SELECT COUNT(*) FROM investment_valuations`); n != 1 {
		t.Fatalf("same-day rows=%d", n)
	}
	history, err := GetInvestmentValuations(context.Background(), db, "ws-a", inv.ID)
	if err != nil || len(history) != 1 || !reflect.DeepEqual(history[0], corrected) {
		t.Fatalf("authoritative return: %+v %v", history, err)
	}
	// Future dates are valid by the frozen contract; zero is total loss.
	input.ValuationDate, input.AmountCents = "9999-12-31", 0
	last, err := UpsertInvestmentValuation(context.Background(), db, input)
	if err != nil || last.ID == first.ID || last.UpdatedBy != nil || last.UpdatedAt != nil {
		t.Fatalf("new date: %+v %v", last, err)
	}
	if n := countRows(t, db, `SELECT COUNT(*) FROM investment_valuations`); n != 2 {
		t.Fatalf("new-date rows=%d", n)
	}
	current, principal, err = GetCurrentInvestmentValue(context.Background(), db, "ws-a", inv.ID)
	if err != nil || principal || current != 0 {
		t.Fatalf("zero current: %d %v %v", current, principal, err)
	}
	// Correcting an older date must not replace the latest current value.
	input.ValuationDate, input.AmountCents = "2026-02-01", math.MaxInt64
	if _, err := UpsertInvestmentValuation(context.Background(), db, input); err != nil {
		t.Fatal(err)
	}
	current, principal, err = GetCurrentInvestmentValue(context.Background(), db, "ws-a", inv.ID)
	if err != nil || principal || current != 0 {
		t.Fatalf("older correction replaced current: %d %v %v", current, principal, err)
	}
	afterInvestment, err := GetInvestment(context.Background(), db, "ws-a", inv.ID)
	if err != nil || !reflect.DeepEqual(beforeInvestment, afterInvestment) {
		t.Fatalf("valuation changed investment: %+v %v", afterInvestment, err)
	}
	if countRows(t, db, `SELECT COUNT(*) FROM transactions`) != beforeTransactions || balances() != beforeBalances {
		t.Fatal("valuation changed ledger or account balances")
	}
}

func TestInvestmentValuationValidationAndIsolation(t *testing.T) {
	db := openDB(t)
	defer db.Close()
	seedInvestmentServiceFixtures(t, db)
	inv := mustCreateInvestment(t, db, investmentCreateInput("ws-a", "acc-inv-a"))
	insertRawInvestment(t, db, "redeemed", "ws-a", "acc-inv-a", "RESGATADA", nil)
	valid := UpsertInvestmentValuationInput{"ws-a", inv.ID, "user-a", "2026-01-15", 1}
	for _, tc := range []struct {
		name   string
		change func(*UpsertInvestmentValuationInput)
		want   error
	}{
		{"workspace-empty", func(i *UpsertInvestmentValuationInput) { i.WorkspaceID = "" }, ErrInvestmentValidation},
		{"investment-empty", func(i *UpsertInvestmentValuationInput) { i.InvestmentID = "" }, ErrInvestmentValidation},
		{"actor-empty", func(i *UpsertInvestmentValuationInput) { i.ActingUserID = "" }, ErrInvestmentValidation},
		{"negative", func(i *UpsertInvestmentValuationInput) { i.AmountCents = -1 }, ErrInvestmentValidation},
		{"format", func(i *UpsertInvestmentValuationInput) { i.ValuationDate = "15/01/2026" }, ErrInvestmentValidation},
		{"impossible", func(i *UpsertInvestmentValuationInput) { i.ValuationDate = "2026-02-30" }, ErrInvestmentValidation},
		{"empty-date", func(i *UpsertInvestmentValuationInput) { i.ValuationDate = "" }, ErrInvestmentValidation},
		{"before-applied", func(i *UpsertInvestmentValuationInput) { i.ValuationDate = "2026-01-14" }, ErrInvestmentValidation},
		{"redeemed", func(i *UpsertInvestmentValuationInput) { i.InvestmentID = "redeemed" }, ErrInvestmentInvalidStatus},
		{"missing", func(i *UpsertInvestmentValuationInput) { i.InvestmentID = "missing" }, ErrInvestmentNotFound},
		{"cross-tenant", func(i *UpsertInvestmentValuationInput) { i.WorkspaceID = "ws-b" }, ErrInvestmentNotFound},
	} {
		t.Run(tc.name, func(t *testing.T) {
			input := valid
			tc.change(&input)
			_, err := UpsertInvestmentValuation(context.Background(), db, input)
			if !errors.Is(err, tc.want) {
				t.Fatalf("got %v want %v", err, tc.want)
			}
			if n := countRows(t, db, `SELECT COUNT(*) FROM investment_valuations`); n != 0 {
				t.Fatalf("rejected write created %d rows", n)
			}
		})
	}
	// Exact applied date is allowed, including a zero valuation.
	valid.AmountCents = 0
	if _, err := UpsertInvestmentValuation(context.Background(), db, valid); err != nil {
		t.Fatal(err)
	}
}

// Test-only driver instrumentation preserves real SQLite transactions while
// placing a barrier immediately before the first COMMIT and observing the
// second BEGIN. No sleeps or production hooks determine the ordering.
type valuationTestConnector struct {
	dsn          string
	beforeBegin  func()
	beforeCommit func()
}

func (c *valuationTestConnector) Driver() driver.Driver { return &sqlite.Driver{} }
func (c *valuationTestConnector) Connect(context.Context) (driver.Conn, error) {
	conn, err := c.Driver().Open(c.dsn)
	if err != nil {
		return nil, err
	}
	return &valuationTestConn{Conn: conn, hooks: c}, nil
}

type valuationTestConn struct {
	driver.Conn
	hooks *valuationTestConnector
}

func (c *valuationTestConn) BeginTx(ctx context.Context, opts driver.TxOptions) (driver.Tx, error) {
	if c.hooks.beforeBegin != nil {
		c.hooks.beforeBegin()
	}
	tx, err := c.Conn.(driver.ConnBeginTx).BeginTx(ctx, opts)
	if err != nil {
		return nil, err
	}
	return &valuationTestTx{Tx: tx, beforeCommit: c.hooks.beforeCommit}, nil
}

type valuationTestTx struct {
	driver.Tx
	beforeCommit func()
}

func (tx *valuationTestTx) Commit() error {
	if tx.beforeCommit != nil {
		tx.beforeCommit()
	}
	return tx.Tx.Commit()
}

func TestInvestmentValuationConcurrentSameDayImmediate(t *testing.T) {
	path := filepath.Join(t.TempDir(), "valuations.db")
	pools, err := database.OpenRuntimeDBs(path)
	if err != nil {
		t.Fatal(err)
	}
	defer pools.Close()
	seedInvestmentServiceFixtures(t, pools.DB)
	inv := mustCreateInvestment(t, pools.Immediate, investmentCreateInput("ws-a", "acc-inv-a"))
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	firstAtCommit, secondAtBegin, release := make(chan struct{}), make(chan struct{}), make(chan struct{})
	dsn := path + "?_txlock=immediate&_pragma=busy_timeout(5000)&_pragma=foreign_keys(1)"
	firstDB := sql.OpenDB(&valuationTestConnector{dsn: dsn, beforeCommit: func() {
		close(firstAtCommit)
		select {
		case <-release:
		case <-ctx.Done():
		}
	}})
	defer firstDB.Close()
	secondDB := sql.OpenDB(&valuationTestConnector{dsn: dsn, beforeBegin: func() { close(secondAtBegin) }})
	defer secondDB.Close()
	type result struct {
		row models.InvestmentValuation
		err error
	}
	firstResult, secondResult := make(chan result, 1), make(chan result, 1)
	go func() {
		row, err := UpsertInvestmentValuation(ctx, firstDB, UpsertInvestmentValuationInput{"ws-a", inv.ID, "user-a", "2026-02-01", 111})
		firstResult <- result{row, err}
	}()
	select {
	case <-firstAtCommit:
	case <-ctx.Done():
		t.Fatal("first writer did not reach commit")
	}
	go func() {
		row, err := UpsertInvestmentValuation(ctx, secondDB, UpsertInvestmentValuationInput{"ws-a", inv.ID, "user-b", "2026-02-01", 222})
		secondResult <- result{row, err}
	}()
	select {
	case <-secondAtBegin:
	case <-ctx.Done():
		t.Fatal("second writer did not reach begin")
	}
	close(release)
	first, second := <-firstResult, <-secondResult
	if first.err != nil || second.err != nil {
		t.Fatalf("writers: %v / %v", first.err, second.err)
	}
	if first.row.UpdatedBy != nil || first.row.UpdatedAt != nil || second.row.ID != first.row.ID || second.row.CreatedBy != "user-a" || second.row.CreatedAt != first.row.CreatedAt || second.row.UpdatedBy == nil || *second.row.UpdatedBy != "user-b" || second.row.UpdatedAt == nil || *second.row.UpdatedAt < first.row.CreatedAt || second.row.AmountCents != 222 {
		t.Fatalf("serialized authorship: %+v / %+v", first.row, second.row)
	}
	reopened, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	rows, err := GetInvestmentValuations(ctx, reopened, "ws-a", inv.ID)
	if err != nil || len(rows) != 1 || !reflect.DeepEqual(rows[0], second.row) {
		t.Fatalf("durable result: %+v %v", rows, err)
	}
	if countRows(t, reopened, `SELECT COUNT(*) FROM transactions`) != 0 || queryScalar(t, reopened, `SELECT current_balance FROM accounts WHERE id='acc-inv-a'`) != "0" {
		t.Fatal("concurrent valuations changed ledger/balance")
	}
}

func TestInvestmentValuationStatusFlipOrdering(t *testing.T) {
	path := filepath.Join(t.TempDir(), "status.db")
	pools, err := database.OpenRuntimeDBs(path)
	if err != nil {
		t.Fatal(err)
	}
	defer pools.Close()
	seedInvestmentServiceFixtures(t, pools.DB)
	for _, valuationFirst := range []bool{true, false} {
		inv := mustCreateInvestment(t, pools.Immediate, investmentCreateInput("ws-a", "acc-inv-a"))
		input := UpsertInvestmentValuationInput{"ws-a", inv.ID, "user-a", "2026-02-01", 100}
		if valuationFirst {
			if _, err := UpsertInvestmentValuation(context.Background(), pools.Immediate, input); err != nil {
				t.Fatal(err)
			}
		}
		tx, err := pools.Immediate.BeginTx(context.Background(), nil)
		if err != nil {
			t.Fatal(err)
		}
		res, err := tx.Exec(`UPDATE investments SET status='RESGATADA', redeemed_date='2026-02-01', redeemed_by='user-a' WHERE id=? AND workspace_id='ws-a' AND status='ATIVA'`, inv.ID)
		if err != nil {
			tx.Rollback()
			t.Fatal(err)
		}
		n, err := res.RowsAffected()
		if err != nil || n != 1 {
			tx.Rollback()
			t.Fatalf("guarded flip: %d %v", n, err)
		}
		if err := tx.Commit(); err != nil {
			t.Fatal(err)
		}
		if _, err := UpsertInvestmentValuation(context.Background(), pools.Immediate, input); !errors.Is(err, ErrInvestmentInvalidStatus) {
			t.Fatalf("post-flip: %v", err)
		}
		rows, err := GetInvestmentValuations(context.Background(), pools.DB, "ws-a", inv.ID)
		want := 0
		if valuationFirst {
			want = 1
		}
		if err != nil || len(rows) != want {
			t.Fatalf("flip changed history: %+v %v", rows, err)
		}
	}
}

// I4 uses real runtime pools and disposable files for every lifecycle probe.
func newInvestmentLifecycleDB(t *testing.T) (*database.RuntimeDBs, string, models.Investment) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "lifecycle.db")
	pools, err := database.OpenRuntimeDBs(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { pools.Close() })
	seedInvestmentServiceFixtures(t, pools.DB)
	input := investmentCreateInput("ws-a", "acc-inv-a")
	maturity := "2027-01-15"
	input.MaturityDate = &maturity
	return pools, path, mustCreateInvestment(t, pools.Immediate, input)
}

func investmentRedeemInput(id string) RedeemInvestmentInput {
	return RedeemInvestmentInput{"ws-a", id, "user-b", "2026-02-01", 120000}
}

func TestInvestmentLifecycleRedeemArchiveInvariants(t *testing.T) {
	for _, sameDay := range []bool{false, true} {
		t.Run(map[bool]string{false: "new-final-zero", true: "same-day-correction"}[sameDay], func(t *testing.T) {
			pools, _, inv := newInvestmentLifecycleDB(t)
			ctx := context.Background()
			insertValuationRow(t, pools.Immediate, "old", "ws-a", inv.ID, "2026-01-20", 105000)
			if sameDay {
				insertValuationRow(t, pools.Immediate, "final", "ws-a", inv.ID, "2026-02-01", 110000)
			}
			exec(t, pools.Immediate, `UPDATE accounts SET current_balance=72345 WHERE id='acc-inv-a'`)
			ledger := func() string {
				return queryScalar(t, pools.DB, `SELECT (SELECT COUNT(*) FROM transactions) || ':' || group_concat(id || ':' || current_balance) FROM (SELECT id,current_balance FROM accounts ORDER BY id)`)
			}
			beforeLedger := ledger()
			input := investmentRedeemInput(inv.ID)
			input.RedeemedDate = "2026-02-01"
			if !sameDay {
				input.FinalAmountCents = 0
			}
			if err := RedeemInvestment(ctx, pools.Immediate, input); err != nil {
				t.Fatal(err)
			}
			redeemed, err := GetInvestment(ctx, pools.DB, "ws-a", inv.ID)
			if err != nil || redeemed.Status != "RESGATADA" || redeemed.RedeemedDate == nil || *redeemed.RedeemedDate != "2026-02-01" || redeemed.RedeemedBy == nil || *redeemed.RedeemedBy != "user-b" || redeemed.ArchivedAt != nil {
				t.Fatalf("redeemed=%+v err=%v", redeemed, err)
			}
			vals, err := GetInvestmentValuations(ctx, pools.DB, "ws-a", inv.ID)
			if err != nil || len(vals) != 2 {
				t.Fatalf("valuations=%+v %v", vals, err)
			}
			v := vals[1]
			if v.ValuationDate != "2026-02-01" || v.AmountCents != input.FinalAmountCents {
				t.Fatalf("final=%+v", v)
			}
			if sameDay {
				if v.ID != "final" || v.CreatedBy != "user-a" || v.CreatedAt != 200 || v.UpdatedBy == nil || *v.UpdatedBy != "user-b" || v.UpdatedAt == nil {
					t.Fatalf("correction=%+v", v)
				}
			} else if v.CreatedBy != "user-b" || v.UpdatedBy != nil {
				t.Fatalf("new final=%+v", v)
			}
			current, principal, err := GetCurrentInvestmentValue(ctx, pools.DB, "ws-a", inv.ID)
			if err != nil || principal || current != input.FinalAmountCents {
				t.Fatalf("current=%d %v %v", current, principal, err)
			}
			input.ActingUserID = "user-a"
			if err := RedeemInvestment(ctx, pools.Immediate, input); !errors.Is(err, ErrInvestmentInvalidStatus) {
				t.Fatalf("second redeem=%v", err)
			}
			if ledger() != beforeLedger {
				t.Fatal("redeem changed ledger")
			}
			if err := ArchiveInvestment(ctx, pools.Immediate, ArchiveInvestmentInput{"ws-a", inv.ID, "user-a"}); err != nil {
				t.Fatal(err)
			}
			archived, err := GetInvestment(ctx, pools.DB, "ws-a", inv.ID)
			if err != nil || archived.ArchivedAt == nil || *archived.ArchivedAt <= 0 {
				t.Fatalf("archive=%+v %v", archived, err)
			}
			archived.ArchivedAt, archived.UpdatedAt = redeemed.ArchivedAt, redeemed.UpdatedAt
			if !reflect.DeepEqual(archived, redeemed) {
				t.Fatal("archive changed redeemed fields")
			}
			after, err := GetInvestmentValuations(ctx, pools.DB, "ws-a", inv.ID)
			if err != nil || !reflect.DeepEqual(vals, after) {
				t.Fatalf("archive changed history=%+v %v", after, err)
			}
			if ledger() != beforeLedger {
				t.Fatal("archive changed ledger")
			}
			for _, filter := range []InvestmentListFilter{InvestmentListActive, InvestmentListRedeemed, InvestmentListArchived, InvestmentListAll} {
				rows, err := ListInvestments(ctx, pools.DB, "ws-a", filter)
				want := 0
				if filter == InvestmentListArchived || filter == InvestmentListAll {
					want = 1
				}
				if err != nil || len(rows) != want {
					t.Fatalf("filter=%d rows=%d err=%v", filter, len(rows), err)
				}
			}
			if err := ArchiveInvestment(ctx, pools.Immediate, ArchiveInvestmentInput{"ws-a", inv.ID, "user-a"}); !errors.Is(err, ErrInvestmentInvalidStatus) {
				t.Fatalf("second archive=%v", err)
			}
			if err := RedeemInvestment(ctx, pools.Immediate, input); !errors.Is(err, ErrInvestmentInvalidStatus) {
				t.Fatalf("archived redeem=%v", err)
			}
		})
	}
}

func TestInvestmentLifecycleRedeemTemporalOrder(t *testing.T) {
	for _, tc := range []struct {
		name, redeemedDate string
		positions          bool
		wantErr            bool
		wantCount          int
	}{
		{"retroactive", "2026-09-15", true, true, 2},
		{"same-day", "2026-09-20", true, false, 2},
		{"later", "2026-09-25", true, false, 3},
		{"no-valuation", "2026-09-10", false, false, 1},
		{"before-application", "2026-08-31", false, true, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			pools, _, _ := newInvestmentLifecycleDB(t)
			ctx := context.Background()
			create := investmentCreateInput("ws-a", "acc-inv-a")
			create.AppliedDate = "2026-09-01"
			create.PrincipalCents = 1000000
			inv := mustCreateInvestment(t, pools.Immediate, create)
			if tc.positions {
				insertValuationRow(t, pools.Immediate, "sep-10", "ws-a", inv.ID, "2026-09-10", 1010000)
				insertValuationRow(t, pools.Immediate, "sep-20", "ws-a", inv.ID, "2026-09-20", 1025000)
			}
			exec(t, pools.Immediate, `UPDATE accounts SET current_balance=32100 WHERE id='acc-inv-a'`)
			beforeInvestment, err := GetInvestment(ctx, pools.DB, "ws-a", inv.ID)
			if err != nil {
				t.Fatal(err)
			}
			beforePositions, err := GetInvestmentValuations(ctx, pools.DB, "ws-a", inv.ID)
			if err != nil {
				t.Fatal(err)
			}
			beforeTransactions := countRows(t, pools.DB, `SELECT COUNT(*) FROM transactions`)
			beforeBalance := queryScalar(t, pools.DB, `SELECT CAST(current_balance AS TEXT) FROM accounts WHERE id='acc-inv-a'`)
			input := RedeemInvestmentInput{"ws-a", inv.ID, "user-b", tc.redeemedDate, 1023000}
			err = RedeemInvestment(ctx, pools.Immediate, input)
			if tc.wantErr {
				if !errors.Is(err, ErrInvestmentValidation) {
					t.Fatalf("redeem error=%v, want validation", err)
				}
				afterInvestment, err := GetInvestment(ctx, pools.DB, "ws-a", inv.ID)
				if err != nil || !reflect.DeepEqual(beforeInvestment, afterInvestment) {
					t.Fatalf("rejected redeem changed investment: %+v %v", afterInvestment, err)
				}
				afterPositions, err := GetInvestmentValuations(ctx, pools.DB, "ws-a", inv.ID)
				if err != nil || !reflect.DeepEqual(beforePositions, afterPositions) {
					t.Fatalf("rejected redeem changed positions: %+v %v", afterPositions, err)
				}
				if tc.positions {
					current, fromPrincipal, err := GetCurrentInvestmentValue(ctx, pools.DB, "ws-a", inv.ID)
					if err != nil || fromPrincipal || current != 1025000 {
						t.Fatalf("latest after rejected redeem=%d %v %v", current, fromPrincipal, err)
					}
				}
			} else {
				if err != nil {
					t.Fatal(err)
				}
				redeemed, err := GetInvestment(ctx, pools.DB, "ws-a", inv.ID)
				if err != nil || redeemed.Status != "RESGATADA" || redeemed.ArchivedAt != nil {
					t.Fatalf("redeemed=%+v %v", redeemed, err)
				}
				positions, err := GetInvestmentValuations(ctx, pools.DB, "ws-a", inv.ID)
				if err != nil || len(positions) != tc.wantCount {
					t.Fatalf("positions=%+v %v, want count %d", positions, err, tc.wantCount)
				}
				latest := positions[len(positions)-1]
				if latest.ValuationDate != tc.redeemedDate || latest.AmountCents != input.FinalAmountCents {
					t.Fatalf("final position=%+v", latest)
				}
				if tc.name == "same-day" && (latest.ID != "sep-20" || latest.UpdatedBy == nil || *latest.UpdatedBy != "user-b") {
					t.Fatalf("same-day UPSERT changed identity: %+v", latest)
				}
				current, fromPrincipal, err := GetCurrentInvestmentValue(ctx, pools.DB, "ws-a", inv.ID)
				if err != nil || fromPrincipal || current != input.FinalAmountCents {
					t.Fatalf("current after redeem=%d %v %v", current, fromPrincipal, err)
				}
			}
			if got := countRows(t, pools.DB, `SELECT COUNT(*) FROM investment_valuations WHERE investment_id='`+inv.ID+`'`); got != tc.wantCount {
				t.Fatalf("valuation count=%d, want %d", got, tc.wantCount)
			}
			if countRows(t, pools.DB, `SELECT COUNT(*) FROM transactions`) != beforeTransactions ||
				queryScalar(t, pools.DB, `SELECT CAST(current_balance AS TEXT) FROM accounts WHERE id='acc-inv-a'`) != beforeBalance {
				t.Fatal("redeem changed ledger or account balance")
			}
		})
	}
}

func TestInvestmentLifecycleRedeemTemporalScope(t *testing.T) {
	pools, _, _ := newInvestmentLifecycleDB(t)
	ctx := context.Background()
	create := investmentCreateInput("ws-a", "acc-inv-a")
	create.AppliedDate = "2026-09-01"
	target := mustCreateInvestment(t, pools.Immediate, create)
	otherA := mustCreateInvestment(t, pools.Immediate, create)
	create.WorkspaceID, create.AccountID = "ws-b", "acc-inv-b"
	otherB := mustCreateInvestment(t, pools.Immediate, create)
	insertValuationRow(t, pools.Immediate, "target-old", "ws-a", target.ID, "2026-09-10", 101000)
	insertValuationRow(t, pools.Immediate, "other-a-new", "ws-a", otherA.ID, "2026-09-25", 103000)
	insertValuationRow(t, pools.Immediate, "other-b-new", "ws-b", otherB.ID, "2026-09-30", 104000)
	if err := RedeemInvestment(ctx, pools.Immediate, RedeemInvestmentInput{"ws-a", target.ID, "user-b", "2026-09-15", 102000}); err != nil {
		t.Fatalf("other investments or workspaces affected temporal guard: %v", err)
	}
	current, fromPrincipal, err := GetCurrentInvestmentValue(ctx, pools.DB, "ws-a", target.ID)
	if err != nil || fromPrincipal || current != 102000 {
		t.Fatalf("target final value=%d %v %v", current, fromPrincipal, err)
	}
	for _, other := range []struct{ ws, id string }{{"ws-a", otherA.ID}, {"ws-b", otherB.ID}} {
		inv, err := GetInvestment(ctx, pools.DB, other.ws, other.id)
		if err != nil || inv.Status != "ATIVA" {
			t.Fatalf("other investment=%+v %v", inv, err)
		}
	}
}

func TestInvestmentLifecycleValidationAndIsolation(t *testing.T) {
	pools, _, inv := newInvestmentLifecycleDB(t)
	ctx := context.Background()
	for _, tc := range []struct {
		name   string
		change func(*RedeemInvestmentInput)
		want   error
	}{
		{"workspace", func(i *RedeemInvestmentInput) { i.WorkspaceID = "" }, ErrInvestmentValidation},
		{"id", func(i *RedeemInvestmentInput) { i.InvestmentID = "" }, ErrInvestmentValidation},
		{"actor", func(i *RedeemInvestmentInput) { i.ActingUserID = "" }, ErrInvestmentValidation},
		{"negative", func(i *RedeemInvestmentInput) { i.FinalAmountCents = -1 }, ErrInvestmentValidation},
		{"date", func(i *RedeemInvestmentInput) { i.RedeemedDate = "01/02/2026" }, ErrInvestmentValidation},
		{"impossible", func(i *RedeemInvestmentInput) { i.RedeemedDate = "2026-02-30" }, ErrInvestmentValidation},
		{"before-applied", func(i *RedeemInvestmentInput) { i.RedeemedDate = "2026-01-14" }, ErrInvestmentValidation},
		{"missing", func(i *RedeemInvestmentInput) { i.InvestmentID = "missing" }, ErrInvestmentNotFound},
		{"tenant", func(i *RedeemInvestmentInput) { i.WorkspaceID = "ws-b" }, ErrInvestmentNotFound},
	} {
		t.Run(tc.name, func(t *testing.T) {
			input := investmentRedeemInput(inv.ID)
			tc.change(&input)
			if err := RedeemInvestment(ctx, pools.Immediate, input); !errors.Is(err, tc.want) {
				t.Fatalf("got %v want %v", err, tc.want)
			}
			// Prove input-only validation happens before any DB access.
			if tc.want == ErrInvestmentValidation && tc.name != "before-applied" {
				if err := RedeemInvestment(ctx, nil, input); !errors.Is(err, tc.want) {
					t.Fatal(err)
				}
			}
		})
	}
	for _, tc := range []struct {
		input ArchiveInvestmentInput
		want  error
	}{
		{ArchiveInvestmentInput{"ws-a", inv.ID, "user-a"}, ErrInvestmentInvalidStatus},
		{ArchiveInvestmentInput{"ws-a", "missing", "user-a"}, ErrInvestmentNotFound},
		{ArchiveInvestmentInput{"ws-b", inv.ID, "user-a"}, ErrInvestmentNotFound},
		{ArchiveInvestmentInput{"", inv.ID, "user-a"}, ErrInvestmentValidation},
		{ArchiveInvestmentInput{"ws-a", "", "user-a"}, ErrInvestmentValidation},
		{ArchiveInvestmentInput{"ws-a", inv.ID, ""}, ErrInvestmentValidation},
	} {
		if err := ArchiveInvestment(ctx, pools.Immediate, tc.input); !errors.Is(err, tc.want) {
			t.Fatalf("archive=%v want=%v", err, tc.want)
		}
	}
	if countRows(t, pools.DB, `SELECT COUNT(*) FROM investment_valuations`) != 0 {
		t.Fatal("rejected inputs wrote valuations")
	}
	input := investmentRedeemInput(inv.ID)
	input.RedeemedDate = "9999-12-31"
	if err := RedeemInvestment(ctx, pools.Immediate, input); err != nil {
		t.Fatalf("future date rejected: %v", err)
	}
}

func TestInvestmentLifecycleRollbackFinalBeforeFlip(t *testing.T) {
	for _, sameDay := range []bool{false, true} {
		t.Run(map[bool]string{false: "insert", true: "correction"}[sameDay], func(t *testing.T) {
			pools, _, inv := newInvestmentLifecycleDB(t)
			ctx := context.Background()
			if sameDay {
				insertValuationRow(t, pools.Immediate, "final", "ws-a", inv.ID, "2026-02-01", 77)
			}
			before, err := GetInvestmentValuations(ctx, pools.DB, "ws-a", inv.ID)
			if err != nil {
				t.Fatal(err)
			}
			exec(t, pools.Immediate, `CREATE TRIGGER i4_test_abort BEFORE UPDATE ON investments WHEN NEW.status='RESGATADA' BEGIN SELECT RAISE(ABORT,'i4 forced flip failure'); END`)
			if err := RedeemInvestment(ctx, pools.Immediate, investmentRedeemInput(inv.ID)); err == nil {
				t.Fatal("expected flip error")
			}
			after, err := GetInvestmentValuations(ctx, pools.DB, "ws-a", inv.ID)
			if err != nil || !reflect.DeepEqual(before, after) {
				t.Fatalf("partial valuation mutation: %+v %v", after, err)
			}
			got, err := GetInvestment(ctx, pools.DB, "ws-a", inv.ID)
			if err != nil || !reflect.DeepEqual(inv, got) {
				t.Fatalf("partial lifecycle: %+v %v", got, err)
			}
		})
	}
}

func TestInvestmentLifecycleConcurrentOrdering(t *testing.T) {
	for _, tc := range []struct {
		first, second string
		want          error
	}{
		{"valuation", "redeem", nil}, {"redeem", "valuation", ErrInvestmentInvalidStatus},
		{"redeem", "redeem", ErrInvestmentInvalidStatus},
		{"update", "redeem", nil}, {"redeem", "update", ErrInvestmentInvalidStatus},
		{"redeem", "archive", nil}, {"archive", "archive", ErrInvestmentInvalidStatus},
	} {
		t.Run(tc.first+"-then-"+tc.second, func(t *testing.T) {
			pools, path, inv := newInvestmentLifecycleDB(t)
			if tc.first == "archive" {
				if err := RedeemInvestment(context.Background(), pools.Immediate, investmentRedeemInput(inv.ID)); err != nil {
					t.Fatal(err)
				}
			}
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			firstCommit, secondBegin, release := make(chan struct{}), make(chan struct{}), make(chan struct{})
			dsn := path + "?_txlock=immediate&_pragma=busy_timeout(5000)&_pragma=foreign_keys(1)"
			firstDB := sql.OpenDB(&valuationTestConnector{dsn: dsn, beforeCommit: func() {
				close(firstCommit)
				select {
				case <-release:
				case <-ctx.Done():
				}
			}})
			defer firstDB.Close()
			secondDB := sql.OpenDB(&valuationTestConnector{dsn: dsn, beforeBegin: func() { close(secondBegin) }})
			defer secondDB.Close()
			run := func(db *sql.DB, op, actor string, amount int64) error {
				switch op {
				case "redeem":
					input := investmentRedeemInput(inv.ID)
					input.ActingUserID = actor
					input.FinalAmountCents = amount
					return RedeemInvestment(ctx, db, input)
				case "archive":
					return ArchiveInvestment(ctx, db, ArchiveInvestmentInput{"ws-a", inv.ID, actor})
				case "valuation":
					_, err := UpsertInvestmentValuation(ctx, db, UpsertInvestmentValuationInput{"ws-a", inv.ID, actor, "2026-02-01", amount})
					return err
				case "update":
					return UpdateInvestment(ctx, db, UpdateInvestmentInput{WorkspaceID: "ws-a", InvestmentID: inv.ID, Name: "Edited", Kind: "CDB", TaxRegime: "MANUAL", UpdatedAt: 123})
				default:
					panic("unknown test operation")
				}
			}
			firstResult, secondResult := make(chan error, 1), make(chan error, 1)
			go func() { firstResult <- run(firstDB, tc.first, "user-a", 111) }()
			select {
			case <-firstCommit:
			case err := <-firstResult:
				t.Fatalf("first failed before barrier: %v", err)
			case <-ctx.Done():
				t.Fatal("first barrier timeout")
			}
			go func() { secondResult <- run(secondDB, tc.second, "user-b", 222) }()
			select {
			case <-secondBegin:
			case <-ctx.Done():
				t.Fatal("second barrier timeout")
			}
			close(release)
			if err := <-firstResult; err != nil {
				t.Fatal(err)
			}
			if err := <-secondResult; !errors.Is(err, tc.want) {
				t.Fatalf("second=%v want=%v", err, tc.want)
			}
			reopened, err := sql.Open("sqlite", path)
			if err != nil {
				t.Fatal(err)
			}
			defer reopened.Close()
			got, err := GetInvestment(ctx, reopened, "ws-a", inv.ID)
			if err != nil {
				t.Fatal(err)
			}
			wantActor, wantAmount := "user-a", int64(111)
			if tc.second == "redeem" && tc.want == nil {
				wantActor, wantAmount = "user-b", 222
			}
			if tc.first == "archive" {
				wantActor, wantAmount = "user-b", 120000
			}
			if got.Status != "RESGATADA" || got.RedeemedBy == nil || *got.RedeemedBy != wantActor {
				t.Fatalf("winner=%+v", got)
			}
			if (tc.second == "archive") != (got.ArchivedAt != nil) {
				t.Fatalf("archive state=%+v", got)
			}
			vals, err := GetInvestmentValuations(ctx, reopened, "ws-a", inv.ID)
			if err != nil || len(vals) != 1 || vals[0].AmountCents != wantAmount {
				t.Fatalf("final valuation=%+v %v", vals, err)
			}
			if tc.first == "valuation" && (vals[0].CreatedBy != "user-a" || vals[0].UpdatedBy == nil || *vals[0].UpdatedBy != "user-b") {
				t.Fatal("same-day concurrency authorship")
			}
			if tc.first == "update" && got.Name != "Edited" {
				t.Fatal("committed update lost")
			}
			if tc.second == "update" && got.Name != inv.Name {
				t.Fatal("post-redeem update succeeded")
			}
			if countRows(t, reopened, `SELECT COUNT(*) FROM transactions`) != 0 || queryScalar(t, reopened, `SELECT current_balance FROM accounts WHERE id='acc-inv-a'`) != "0" {
				t.Fatal("concurrency changed ledger")
			}
		})
	}
}
