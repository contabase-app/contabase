package handlers

import (
	"database/sql"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// M5 — Contratos da apresentação Premium dos históricos de reservas e
// limites. Os testes usam os handlers reais para preservar tipos, sinais,
// rótulos, ordem, isolamento e os limites de consulta existentes.

func renderCaixinhaHistory(t *testing.T, db *sql.DB, boxID string) string {
	t.Helper()

	h := MetasHandler{ActorRole: testMetasAdminRole,
		DB:          db,
		Templates:   testHistoryTemplates(t),
		WorkspaceID: "ws-a",
		UserID:      "user-a",
	}
	rr := httptest.NewRecorder()
	h.HandleHistoricoCaixinha(rr, httptest.NewRequest(http.MethodGet, "/metas/caixinha/historico?box_id="+boxID, nil))
	if rr.Code != http.StatusOK {
		t.Fatalf("box history status = %d, want 200, body=%s", rr.Code, rr.Body.String())
	}
	return rr.Body.String()
}

func renderLimiteHistory(t *testing.T, db *sql.DB, limitID string) string {
	t.Helper()

	h := MetasHandler{ActorRole: testMetasAdminRole,
		DB:          db,
		Templates:   testHistoryTemplates(t),
		WorkspaceID: "ws-a",
		UserID:      "user-a",
	}
	rr := httptest.NewRecorder()
	h.HandleHistoricoLimite(rr, httptest.NewRequest(http.MethodGet, "/metas/limite/historico?limit_id="+limitID, nil))
	if rr.Code != http.StatusOK {
		t.Fatalf("limit history status = %d, want 200, body=%s", rr.Code, rr.Body.String())
	}
	return rr.Body.String()
}

func TestMetasBoxHistoryUsesPremiumListAndPreservesLedgerSemantics(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	seedHistoryScenario(t, db)

	now := time.Now().UTC().Unix()
	date := formatDateLabel(now)
	execTestSQL(t, db, `
		INSERT INTO accounts (id, workspace_id, name, type, initial_balance, current_balance, created_at, updated_at)
		VALUES ('a-checking', 'ws-a', 'Conta A', 'CHECKING', 0, 0, ?, ?)
	`, now, now)
	execTestSQL(t, db, `
		INSERT INTO transactions (id, workspace_id, user_id, account_id, category_id, type, amount, date, description, status, created_at, updated_at)
		VALUES ('tx-1', 'ws-a', 'user-a', 'a-checking', 'a-cat', 'EXPENSE', 2500, ?, 'Consumo manual', 'paid', ?, ?)
	`, now, now, now)
	execTestSQL(t, db, `
		INSERT INTO box_virtual_ledger (id, box_id, amount, type, description, source_transaction_id, reference_date, created_at)
		VALUES
			('h-rch1', 'a-box', 5000, 'RECHARGE', 'Aporte manual', NULL, ?, ?),
			('h-bon1', 'a-box', 1000, 'BONUS', 'Bonus manual', NULL, ?, ?),
			('h-rel1', 'a-box', -1000, 'RELEASE', 'Liberacao parcial', NULL, ?, ?),
			('h-cns1', 'a-box', -2500, 'CONSUME', 'Consumo manual', 'tx-1', ?, ?),
			('h-rev1', 'a-box', 2500, 'REVERSAL', 'Ajuste compensatorio', 'tx-1', ?, ?)
	`, now, now, now, now, now, now, now, now, now, now)

	body := renderCaixinhaHistory(t, db, "a-box")

	for _, expected := range []string{
		`data-metas-history="box"`,
		`data-metas-history-panel`,
		`role="dialog"`,
		`aria-modal="true"`,
		`aria-labelledby="box-history-title"`,
		`aria-describedby="box-history-description"`,
		`aria-label="Fechar histórico da reserva Caixinha A"`,
		`role="region" aria-label="Eventos da reserva Caixinha A" tabindex="0"`,
		`ui-panel ui-list-panel metas-history__panel`,
		`metas-history__panel-title">Eventos registrados`,
		`ui-list`,
		`ui-row metas-history__row`,
		`ui-badge ui-badge--neutral metas-history__type`,
		`metas-history__amount--credit" data-sensitive`,
		`metas-history__amount--debit" data-sensitive`,
		`Aporte manual`,
		`Bonus manual`,
		`Liberacao parcial`,
		`Consumo manual`,
		`Ajuste compensatorio`,
		`Aporte`,
		`Ajuste/Bonus`,
		`Liberacao`,
		`Consumo`,
		`Ajuste compensatorio`,
		`+R$ 50,00`,
		`+R$ 10,00`,
		`R$ -10,00`,
		`R$ -25,00`,
		`+R$ 25,00`,
		`Conta · Conta A`,
		date,
		`data-lucide="plus"`,
		`data-lucide="sparkles"`,
		`data-lucide="arrow-up-from-line"`,
		`data-lucide="shopping-bag"`,
		`data-lucide="rotate-ccw"`,
	} {
		assertContains(t, body, expected)
	}

	for _, forbidden := range []string{
		`cb-card-muted`,
		`Mais recentes primeiro`,
		`metas-history__order`,
		`Mostrar mais`,
		`Carregar mais`,
		`hx-get=`,
		`hx-post=`,
		`hx-put=`,
		`hx-patch=`,
		`hx-delete=`,
		`style=`,
	} {
		if strings.Contains(body, forbidden) {
			t.Fatalf("box history must not contain %q", forbidden)
		}
	}
}

func TestMetasLimitHistoryUsesPremiumListAndPreservesExpenseContext(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	seedHistoryScenario(t, db)

	now := appTime(time.Now())
	monthDate := func(day int) int64 {
		return CivilDateToLegacyUnixNoonUTC(MustCivilDate(fmt.Sprintf("%04d-%02d-%02d", now.Year(), now.Month(), day)))
	}
	execTestSQL(t, db, `
		INSERT INTO accounts (id, workspace_id, name, type, initial_balance, current_balance, created_at, updated_at)
		VALUES ('a-card', 'ws-a', 'Cartão A', 'CREDIT_CARD', 0, 0, ?, ?)
	`, now.Unix(), now.Unix())
	execTestSQL(t, db, `
		INSERT INTO cost_limits (id, workspace_id, category_id, max_amount_monthly)
		VALUES ('limit-a', 'ws-a', 'a-cat', 100000)
	`)
	execTestSQL(t, db, `
		INSERT INTO transactions (id, workspace_id, user_id, account_id, category_id, type, amount, date, description, status, created_at, updated_at)
		VALUES
			('tx-old', 'ws-a', 'user-a', 'a-card', 'a-cat', 'EXPENSE', 1000, ?, 'Gasto antigo', 'paid', ?, ?),
			('tx-new', 'ws-a', 'user-a', 'a-card', 'a-cat', 'EXPENSE', 2000, ?, 'Gasto recente', 'paid', ?, ?)
	`, monthDate(5), now.Unix(), now.Unix(), monthDate(20), now.Unix(), now.Unix())

	body := renderLimiteHistory(t, db, "limit-a")

	for _, expected := range []string{
		`data-metas-history="limit"`,
		`data-metas-history-panel`,
		`role="dialog"`,
		`aria-modal="true"`,
		`aria-labelledby="limit-history-title"`,
		`aria-describedby="limit-history-description"`,
		`aria-label="Fechar histórico do limite Categoria A"`,
		`role="region" aria-label="Lançamentos do limite Categoria A neste mês" tabindex="0"`,
		`ui-panel ui-list-panel metas-history__panel`,
		`metas-history__panel-title">Lançamentos deste mês`,
		`ui-row metas-history__row`,
		`metas-history__eyebrow">Categoria`,
		`>Gasto: <span class="metas-history__figure" data-sensitive>R$ 30,00</span>`,
		`metas-history__figure" data-sensitive>R$ 1.000,00`,
		`metas-history__figure" data-sensitive>R$ 30,00`,
		`ui-badge ui-badge--neutral metas-history__percent">3%`,
		`metas-history__amount--expense" data-sensitive`,
		`Gasto recente`,
		`Gasto antigo`,
		`R$ 10,00`,
		`R$ 20,00`,
		`Cartão · Cartão A`,
	} {
		assertContains(t, body, expected)
	}

	if strings.Index(body, "Gasto recente") > strings.Index(body, "Gasto antigo") {
		t.Fatal("limit history must keep the newest transaction first")
	}
	if strings.Contains(body, `cb-card-muted`) {
		t.Fatal("limit history must not keep the legacy cb-card-muted row")
	}
	for _, forbidden := range []string{`Mostrar mais`, `Carregar mais`, `hx-get=`, `hx-post=`} {
		if strings.Contains(body, forbidden) {
			t.Fatalf("limit history must not contain %q", forbidden)
		}
	}
}

func TestMetasHistoryEmptyStatesUsePremiumEmptyWithoutInventingBalance(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	seedHistoryScenario(t, db)

	boxBody := renderCaixinhaHistory(t, db, "a-box")
	for _, expected := range []string{
		`ui-empty metas-history__empty`,
		`data-lucide="history"`,
		`Nenhum evento registrado`,
		`Aportes, ajustes, liberações e consumos aparecem aqui`,
		`role="status"`,
	} {
		assertContains(t, boxBody, expected)
	}
	if strings.Contains(boxBody, "metas-history__row") || strings.Contains(boxBody, "R$") {
		t.Fatal("empty reserve history must not render rows or invent a monetary value")
	}
	if strings.Contains(boxBody, `tabindex="0"`) {
		t.Fatal("empty reserve history must not add a scroll-region tab stop")
	}

	execTestSQL(t, db, `
		INSERT INTO cost_limits (id, workspace_id, category_id, max_amount_monthly)
		VALUES ('limit-empty', 'ws-a', 'a-cat', 100000)
	`)
	limitBody := renderLimiteHistory(t, db, "limit-empty")
	for _, expected := range []string{
		`ui-empty metas-history__empty`,
		`data-lucide="inbox"`,
		`Nenhum lançamento este mês`,
		`Os gastos na categoria Categoria A aparecem aqui`,
		`role="status"`,
	} {
		assertContains(t, limitBody, expected)
	}
	if strings.Contains(limitBody, "metas-history__row") {
		t.Fatal("empty limit history must not render rows")
	}
	if strings.Contains(limitBody, `tabindex="0"`) {
		t.Fatal("empty limit history must not add a scroll-region tab stop")
	}
	if strings.Contains(limitBody, "Saldo") || strings.Contains(limitBody, "Disponível") {
		t.Fatal("empty limit history must not imply a missing balance")
	}
}

func TestMetasHistoryMarksEveryRenderedMoneyValueSensitive(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	seedHistoryScenario(t, db)

	now := time.Now().UTC().Unix()
	execTestSQL(t, db, `
		INSERT INTO box_virtual_ledger (id, box_id, amount, type, description, reference_date, created_at)
		VALUES
			('h-money-1', 'a-box', 12345, 'RECHARGE', 'Aporte', ?, ?),
			('h-money-2', 'a-box', -500, 'RELEASE', 'Liberacao', ?, ?)
	`, now, now, now, now)
	boxBody := renderCaixinhaHistory(t, db, "a-box")
	if got, want := strings.Count(boxBody, "R$"), strings.Count(boxBody, "data-sensitive"); got != want {
		t.Fatalf("box history money values=%d sensitive elements=%d; every value must be masked", got, want)
	}

	execTestSQL(t, db, `
		INSERT INTO accounts (id, workspace_id, name, type, initial_balance, current_balance, created_at, updated_at)
		VALUES ('a-checking-money', 'ws-a', 'Conta A', 'CHECKING', 0, 0, ?, ?)
	`, now, now)
	execTestSQL(t, db, `
		INSERT INTO cost_limits (id, workspace_id, category_id, max_amount_monthly)
		VALUES ('limit-money', 'ws-a', 'a-cat', 100000)
	`)
	execTestSQL(t, db, `
		INSERT INTO transactions (id, workspace_id, user_id, account_id, category_id, type, amount, date, description, status, created_at, updated_at)
		VALUES ('tx-money', 'ws-a', 'user-a', 'a-checking-money', 'a-cat', 'EXPENSE', 32000, ?, 'Compra', 'paid', ?, ?)
	`, CivilDateToLegacyUnixNoonUTC(civilDateAt(time.Now(), appTimeLocation())), now, now)
	limitBody := renderLimiteHistory(t, db, "limit-money")
	if got, want := strings.Count(limitBody, "R$"), strings.Count(limitBody, "data-sensitive"); got != want {
		t.Fatalf("limit history money values=%d sensitive elements=%d; every value must be masked", got, want)
	}

	for name, body := range map[string]string{"box": boxBody, "limit": limitBody} {
		if strings.Contains(body, `aria-label="R$`) || strings.Contains(body, `title="R$`) {
			t.Fatalf("%s history must not expose money in aria-label/title", name)
		}
	}
}

func TestMetasHistoryCSSStaysScopedAndTokenized(t *testing.T) {
	css := readTestFile(t, "assets/css/metas.css")
	start := strings.Index(css, "/* === M5 — Históricos de reservas e limites ===")
	if start < 0 {
		t.Fatal("metas.css missing the M5 history section")
	}
	end := strings.Index(css[start:], "/* === PRIVACIDADE ===")
	if end < 0 {
		t.Fatal("metas.css M5 history section must end before the privacy section")
	}
	section := css[start : start+end]

	for _, expected := range []string{
		`.metas-history__head`,
		`.metas-history__tile--accent`,
		`.metas-history__body`,
		`.metas-history__panel-head`,
		`.metas-history__row {`,
		`.metas-history__mark--accent`,
		`.metas-history__mark--info`,
		`.metas-history__mark--warning`,
		`.metas-history__mark--success`,
		`.metas-history__mark--neutral`,
		`.metas-history__description`,
		`.metas-history__amount--credit`,
		`.metas-history__amount--debit`,
		`.metas-history__amount--expense`,
		`@media (max-width: 389px)`,
		`grid-column: 1 / -1`,
		`box-shadow: none`,
	} {
		if !strings.Contains(section, expected) {
			t.Fatalf("metas.css M5 history section missing %q", expected)
		}
	}

	metaStart := strings.Index(section, ".metas-history__meta {")
	if metaStart < 0 {
		t.Fatal("metas.css M5 history section missing .metas-history__meta")
	}
	metaEnd := strings.Index(section[metaStart:], "}")
	if metaEnd < 0 {
		t.Fatal("metas.css .metas-history__meta rule is not closed")
	}
	metaRule := section[metaStart : metaStart+metaEnd]
	if !strings.Contains(metaRule, "color: var(--ui-text-muted);") {
		t.Fatal("metas.css .metas-history__meta must use the readable muted token")
	}

	for _, forbidden := range []string{
		"!important",
		"nth-child",
		"#",
		"rgb(",
		"rgba(",
		"\n.ui-row {",
		"\n.ui-list-panel {",
	} {
		if strings.Contains(section, forbidden) {
			t.Fatalf("metas.css M5 history section must not contain %q", forbidden)
		}
	}
}

func TestMetasBoxHistoryPreservesLimit100AndNewestFirstOrder(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	seedHistoryScenario(t, db)

	now := time.Now().UTC().Unix()
	for i := 0; i <= 100; i++ {
		execTestSQL(t, db, `
			INSERT INTO box_virtual_ledger (id, box_id, amount, type, description, reference_date, created_at)
			VALUES (?, 'a-box', 1000, 'RECHARGE', ?, ?, ?)
		`, fmt.Sprintf("h-order-%03d", i), fmt.Sprintf("Evento %d", i), now, now+int64(i))
	}

	body := renderCaixinhaHistory(t, db, "a-box")
	if got := strings.Count(body, "metas-history__row"); got != 100 {
		t.Fatalf("box history rows = %d, want the existing LIMIT 100", got)
	}
	if strings.Index(body, "Evento 100") > strings.Index(body, "Evento 99") {
		t.Fatal("box history must keep created_at DESC ordering")
	}
	if strings.Contains(body, "Evento 0<") {
		t.Fatal("box history must not include the 101st oldest event")
	}
}
