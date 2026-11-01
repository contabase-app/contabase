package handlers

import (
	"bytes"
	"database/sql"
	"fmt"
	"html/template"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/contabase-app/contabase/internal/models"
)

func TestFaturasPageUsesGlobalAppShell(t *testing.T) {
	tpl := template.Must(template.New("faturas-app-shell").Funcs(withAppShellTestFuncs(template.FuncMap{
		"assetPath": func(path string) string { return path + "?v=test" },
		"hasString": func(items []string, target string) bool {
			for _, item := range items {
				if item == target {
					return true
				}
			}
			return false
		},
	})).ParseFiles(
		resolveTemplatePath(t, "templates/pages/faturas.html"),
		resolveTemplatePath(t, "templates/pages/lancamentos.html"),
		resolveTemplatePath(t, "templates/components/app_shell.html"),
	))

	data := FaturaData{
		Title:               "Fatura",
		UserName:            "Vitor Ferreira",
		UserInitials:        "VF",
		ActiveWorkspaceName: "Família",
		CardName:            "Cartão principal",
	}
	var out bytes.Buffer
	if err := tpl.ExecuteTemplate(&out, "faturas-page", data); err != nil {
		t.Fatalf("render faturas page: %v", err)
	}

	body := out.String()
	for _, expected := range []string{
		`data-app-shell`,
		`data-page-section="invoice"`,
		`cb-v2-faturas-header`,
		`id="faturas-content"`,
		`id="payment-confirm-modal"`,
		`id="global-confirm-modal"`,
		`/assets/js/modals-global.js`,
		`/assets/js/swipe.js`,
		`/assets/css/faturas.css?v=test`,
		`/assets/js/payment-confirm.js?v=test`,
		`/assets/js/faturas.js?v=test`,
		`/assets/js/show-more.js?v=test`,
		`/assets/js/privacy.js?v=test`,
		`data-privacy-toggle`,
		`data-privacy-icon`,
	} {
		if !strings.Contains(body, expected) {
			t.Fatalf("faturas page missing %q", expected)
		}
	}
	if strings.Contains(body, `{{template "layout-start"`) {
		t.Fatal("faturas page must use the global app shell, not the legacy layout")
	}

	data.InvoiceID = "invoice-test"
	data.NextReference = "2026-09"
	data.CanMutateItems = true
	data.Transactions = []TransactionRow{{
		ID:            "tx-test",
		UpdatedAt:     123,
		DateLabel:     "2 Set",
		Description:   "Compra teste",
		CategoryName:  "Categoria",
		CategoryIcon:  "tag",
		CategoryColor: "gray",
		TimeDisplay:   "12:00",
		AmountDisplay: "- R$ 10,00",
		AmountClass:   "text-red-500",
	}}
	data.TransactionGroups = []TransactionDayGroup{{DateLabel: "2 Set", FirstIndex: 0, Transactions: data.Transactions}}
	out.Reset()
	if err := tpl.ExecuteTemplate(&out, "faturas-page", data); err != nil {
		t.Fatalf("render faturas move actions: %v", err)
	}
	for _, expected := range []string{
		`data-swipe-right-vals='{"expected_invoice_id":"invoice-test","expected_destination_reference":"2026-09","expected_updated_at":"123","expected_move_scope":"single"}'`,
		`hx-vals='{"expected_invoice_id":"invoice-test","expected_destination_reference":"2026-09","expected_updated_at":"123","expected_move_scope":"single"}'`,
		`data-tooltip-hover`,
		`data-tooltip-text="Informar reembolso"`,
		`data-tooltip-text="Mover para próxima fatura"`,
		`Esquerda (&larr;) para mover a cobrança para a próxima fatura`,
		`Direita (&rarr;) para apagar o registro`,
		`cb-v2-swipe-hint`,
	} {
		if !strings.Contains(out.String(), expected) {
			t.Fatalf("faturas move action missing %q in %s", expected, out.String())
		}
	}

	out.Reset()
	if err := tpl.ExecuteTemplate(&out, "faturas-empty-page", FaturaEmptyData{
		Title:               "Fatura",
		UserName:            "Vitor Ferreira",
		UserInitials:        "VF",
		ActiveWorkspaceName: "Família",
		CardName:            "Cartão principal",
		PeriodLabel:         "Set/2026",
		PreviousURL:         "/cartoes/card/faturas?mes=8&ano=2026",
		NextURL:             "/cartoes/card/faturas?mes=10&ano=2026",
		OpenInvoiceURL:      "/cartoes/card/faturas/abrir?mes=9&ano=2026",
	}); err != nil {
		t.Fatalf("render empty faturas page: %v", err)
	}
	for _, expected := range []string{
		`Nenhuma fatura para este período`,
		`hx-post="/cartoes/card/faturas/abrir?mes=9&amp;ano=2026"`,
		`data-app-shell`,
	} {
		if !strings.Contains(out.String(), expected) {
			t.Fatalf("empty faturas page missing %q", expected)
		}
	}
}

func TestFaturasIndexOmiteCabecalhoDuplicadoEEscondeDicaDeSwipeEmDesktop(t *testing.T) {
	content, err := os.ReadFile(filepath.Join(projectRoot(), "templates/pages/faturas.html"))
	if err != nil {
		t.Fatal(err)
	}
	tpl := template.Must(template.New("faturas").Funcs(template.FuncMap{
		"assetPath": func(path string) string { return path },
	}).Parse(string(content)))
	data := FaturaData{
		CardName:       "Cartão que só aparece na página direta",
		Reference:      "2026-10",
		IsFaturasIndex: true,
	}
	var out strings.Builder
	if err := tpl.ExecuteTemplate(&out, "invoice-summary", data); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out.String(), data.CardName) {
		t.Fatalf("invoice hub repeated selected card identity: %s", out.String())
	}
	styles, err := os.ReadFile(resolveTemplatePath(t, "assets/css/design-system.css"))
	if err != nil {
		t.Fatal(err)
	}
	for _, expected := range []string{
		".cb-v2-swipe-hint {",
		"display: none;",
		"@media (max-width: 767px)",
		".cb-v2-swipe-hint { display: flex; }",
	} {
		if !strings.Contains(string(styles), expected) {
			t.Fatalf("missing mobile-only swipe hint style %q", expected)
		}
	}
	if strings.Contains(string(styles), "@media (any-pointer: coarse)") {
		t.Fatal("swipe hint must be hidden on desktop and tablet, not only on fine pointers")
	}
}

func TestFaturasIndexRendersSelectedCardAndInvoiceDetail(t *testing.T) {
	tpl := template.Must(template.New("faturas-index").Funcs(withAppShellTestFuncs(template.FuncMap{
		"assetPath": func(path string) string { return path + "?v=test" },
		"hasString": func(items []string, target string) bool {
			for _, item := range items {
				if item == target {
					return true
				}
			}
			return false
		},
	})).ParseFiles(
		resolveTemplatePath(t, "templates/pages/faturas_index.html"),
		resolveTemplatePath(t, "templates/pages/faturas.html"),
		resolveTemplatePath(t, "templates/pages/lancamentos.html"),
		resolveTemplatePath(t, "templates/components/app_shell.html"),
		resolveTemplatePath(t, "templates/components/credit_card.html"),
	))

	data := FaturasIndexData{
		Title:               "Faturas",
		UserName:            "Vitor Ferreira",
		UserInitials:        "VF",
		ActiveWorkspaceName: "Família",
		PeriodLabel:         "Setembro de 2026",
		PreviousURL:         "/faturas?cartao=card-1&mes=8&ano=2026",
		NextURL:             "/faturas?cartao=card-1&mes=10&ano=2026",
		RefreshCardsURL:     "/faturas?cartao=card-1&mes=9&ano=2026&partial=cards",
		HasSelectedInvoice:  true,
		Cards: []CreditCardCard{{
			ID:             "card-1",
			InvoiceID:      "invoice-1",
			Name:           "Cartão principal",
			Money:          MoneyMinor(9999),
			LimitMoney:     MoneyMinor(10000),
			DueDateCompact: "10/set",
			StatusLabel:    "Aberta",
			ContentURL:     "/faturas/conteudo?cartao=card-1&mes=9&ano=2026",
			Href:           "/faturas?cartao=card-1&mes=9&ano=2026",
			IsFaturasIndex: true,
			IsSelected:     true,
			ProviderMark:   "CB",
			Color:          "#6b5ce7",
			LimitPercent:   80,
		}},
		SelectedFatura: FaturaData{CardName: "Cartão principal", HideMonthNavigation: true},
	}
	var out bytes.Buffer
	if err := tpl.ExecuteTemplate(&out, "faturas-index-page", data); err != nil {
		t.Fatalf("render faturas index: %v", err)
	}
	body := out.String()
	for _, expected := range []string{
		`data-faturas-index`,
		`id="faturas-index-card-rail"`,
		`data-faturas-card`,
		`is-selected`,
		`aria-current="page"`,
		`data-faturas-content-url="/faturas/conteudo?cartao=card-1&amp;mes=9&amp;ano=2026"`,
		`/assets/css/faturas-index.css?v=test`,
		`/assets/js/faturas-index.js?v=test`,
		`/assets/js/privacy.js?v=test`,
		`data-privacy-toggle`,
		`data-privacy-icon`,
		`href="/configuracoes/cartoes"`,
		`id="faturas-content"`,
	} {
		if !strings.Contains(body, expected) {
			t.Fatalf("faturas index missing %q", expected)
		}
	}
}

func TestFaturasIndexURLsKeepSelectedCardPeriodAndSort(t *testing.T) {
	pageURL := faturasIndexURL("card-1", 9, 2026, "asc")
	contentURL := faturasIndexContentURL("card-1", 9, 2026, "asc")
	refreshURL := faturasIndexCardsURL("card-1", 9, 2026, "asc")
	for _, raw := range []string{pageURL, contentURL, refreshURL} {
		u, err := url.Parse(raw)
		if err != nil {
			t.Fatalf("parse index URL %q: %v", raw, err)
		}
		if u.Query().Get("cartao") != "card-1" || u.Query().Get("mes") != "9" || u.Query().Get("ano") != "2026" || u.Query().Get("ordem") != "asc" {
			t.Fatalf("index URL lost selected state: %s", raw)
		}
	}
	if u, _ := url.Parse(refreshURL); u.Query().Get("partial") != "cards" {
		t.Fatalf("refresh URL must request only the card rail: %s", refreshURL)
	}
}

func TestFaturasIndexKeepsRequestedCardWithoutInvoice(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	seedCreditCardWithLimit(t, db, "ws-hub-selection", "xp", "cc-xp", 100000)
	now := time.Now().Unix()
	execTestSQL(t, db, `INSERT INTO accounts (id, workspace_id, name, type, initial_balance, current_balance, created_at, updated_at)
		VALUES ('empty-card', 'ws-hub-selection', 'Cartão sem fatura', 'CREDIT_CARD', 0, 0, ?, ?)`, now, now)
	execTestSQL(t, db, `INSERT INTO credit_cards (id, account_id, closing_day, due_day, credit_limit)
		VALUES ('cc-empty', 'empty-card', 20, 10, 100000)`)
	handler := FaturasHandler{DB: db, WorkspaceID: "ws-hub-selection"}
	for _, id := range []string{"xp", "empty-card"} {
		t.Run(id, func(t *testing.T) {
			data, err := handler.buildFaturasIndexData(httptest.NewRequest(http.MethodGet, faturasIndexURL(id, 9, 2026, "desc"), nil))
			if err != nil {
				t.Fatal(err)
			}
			if data.SelectedCardID != id || data.HasSelectedInvoice {
				t.Fatalf("selected = %q, has invoice = %v", data.SelectedCardID, data.HasSelectedInvoice)
			}
			selectedCount := 0
			for _, card := range data.Cards {
				if card.IsSelected {
					selectedCount++
					if card.ID != id || data.SelectedEmpty.CardName != card.Name {
						t.Fatalf("selected card and empty detail disagree: %+v, %+v", card, data.SelectedEmpty)
					}
				}
			}
			if selectedCount != 1 {
				t.Fatalf("selected cards = %d, want 1", selectedCount)
			}
		})
	}
	if _, err := handler.buildFaturasIndexData(httptest.NewRequest(http.MethodGet, faturasIndexURL("unavailable-card", 9, 2026, "desc"), nil)); err == nil {
		t.Fatal("an unavailable explicit card must not silently select XP or another card")
	}
	// The Dashboard resolves an open invoice even for a newly created card.
	// Both generated links must survive a round trip through the hub.
	dashboard := BuildDashboardData(db, "user-ws-hub-selection", "ws-hub-selection")
	if len(dashboard.Cards) != 2 {
		t.Fatalf("dashboard cards = %d, want 2", len(dashboard.Cards))
	}
	for _, card := range dashboard.Cards {
		data, err := handler.buildFaturasIndexData(httptest.NewRequest(http.MethodGet, card.Href, nil))
		if err != nil {
			t.Fatal(err)
		}
		if data.SelectedCardID != card.ID || data.SelectedFatura.InvoiceID != card.InvoiceID {
			t.Fatalf("dashboard link %q opened another card or invoice", card.Href)
		}
	}
}

func TestFaturasIndexUsesDashboardCardPriorityOrder(t *testing.T) {
	now := time.Date(2026, time.September, 3, 12, 0, 0, 0, time.UTC)
	cards := []CreditCardCard{
		{ID: "paid", Status: models.InvoiceStatusPaid, Amount: 90000, DueDateUnix: now.AddDate(0, 0, -5).Unix()},
		{ID: "upcoming-low", Status: models.InvoiceStatusOpen, Amount: 10000, DueDateUnix: now.AddDate(0, 0, 5).Unix()},
		{ID: "upcoming-high", Status: models.InvoiceStatusClosed, Amount: 50000, DueDateUnix: now.AddDate(0, 0, 5).Unix()},
		{ID: "overdue", Status: models.InvoiceStatusClosed, Amount: 20000, DueDateUnix: now.AddDate(0, 0, -1).Unix()},
		{ID: "zero", Status: models.InvoiceStatusOpen, Amount: 0, DueDateUnix: now.AddDate(0, 0, -1).Unix()},
	}

	SortDashboardCardsByPaymentPriority(cards, now)
	got := make([]string, len(cards))
	for i := range cards {
		got[i] = cards[i].ID
	}
	want := []string{"overdue", "upcoming-high", "upcoming-low", "paid", "zero"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("card order = %v, want Dashboard order %v", got, want)
	}
}

func TestInvoiceIDEntryPointRedirectsToInvoiceHub(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	seedCreditCardWithLimit(t, db, "ws-invoice-hub", "card-invoice-hub", "cc-invoice-hub", 100000)
	seedInvoiceForCard(t, db, "ws-invoice-hub", "card-invoice-hub", "invoice-hub", "2026-08", "CLOSED", 2026, 8, 20)

	handler := FaturasHandler{DB: db, WorkspaceID: "ws-invoice-hub"}
	want := "/faturas?ano=2026&cartao=card-invoice-hub&mes=8"

	t.Run("browser request", func(t *testing.T) {
		rr := httptest.NewRecorder()
		handler.HandleFaturaConceitoPorID(rr, httptest.NewRequest(http.MethodGet, "/cartoes/faturas/invoice-hub", nil), "invoice-hub")
		if rr.Code != http.StatusSeeOther || rr.Header().Get("Location") != want {
			t.Fatalf("redirect = status %d location %q, want 303 %q", rr.Code, rr.Header().Get("Location"), want)
		}
	})

	t.Run("htmx request", func(t *testing.T) {
		rr := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "/cartoes/faturas/invoice-hub", nil)
		req.Header.Set("HX-Request", "true")
		handler.HandleFaturaConceitoPorID(rr, req, "invoice-hub")
		if rr.Code != http.StatusOK || rr.Header().Get("HX-Redirect") != want {
			t.Fatalf("HTMX redirect = status %d location %q, want 200 %q", rr.Code, rr.Header().Get("HX-Redirect"), want)
		}
	})
}

func TestDashboardCardLinksToResolvedInvoiceHub(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	seedCreditCardWithLimit(t, db, "ws-dashboard-hub", "card-dashboard-hub", "cc-dashboard-hub", 100000)
	seedInvoiceForCard(t, db, "ws-dashboard-hub", "card-dashboard-hub", "invoice-dashboard-hub", "2026-08", "CLOSED", 2026, 8, 20)
	seedExpenseOnInvoice(t, db, "ws-dashboard-hub", "card-dashboard-hub", "invoice-dashboard-hub", 5000, 1, 1, "tx-dashboard-hub")

	dashboard := BuildDashboardData(db, "user-ws-dashboard-hub", "ws-dashboard-hub")
	if len(dashboard.Cards) != 1 {
		t.Fatalf("dashboard cards = %d, want 1", len(dashboard.Cards))
	}
	if got, want := dashboard.Cards[0].Href, "/faturas?ano=2026&cartao=card-dashboard-hub&mes=8"; got != want {
		t.Fatalf("dashboard card hub URL = %q, want %q", got, want)
	}
}

func TestFaturasIndexMonthOptionsAreContinuousAroundSelection(t *testing.T) {
	selected := time.Date(2026, time.September, 1, 0, 0, 0, 0, time.UTC)
	options := buildFaturasIndexMonthOptions("card-1", selected, "desc")
	if len(options) != 12 {
		t.Fatalf("month options = %d, want 12", len(options))
	}
	if options[0].Label != "Abr" || options[0].Year != "2026" {
		t.Fatalf("first rolling option = %+v, want Abr/2026", options[0])
	}
	if options[5].Label != "Set" || !options[5].IsActive {
		t.Fatalf("selected month must remain centered: %+v", options[5])
	}
	if options[11].Label != "Mar" || options[11].Year != "2027" {
		t.Fatalf("rolling range must continue into next year: %+v", options[11])
	}
}

func TestFaturaMonthOptionsAreContinuousAroundSelection(t *testing.T) {
	selected := time.Date(2026, time.September, 1, 0, 0, 0, 0, time.UTC)
	options := buildFaturaMonthOptionsAt("card-1", selected, "asc", selected)

	if len(options) != 12 {
		t.Fatalf("month options = %d, want 12", len(options))
	}
	if options[0].Label != "Abr" || options[0].Year != "2026" {
		t.Fatalf("first rolling option = %+v, want Abr/2026", options[0])
	}
	if !options[5].IsActive || options[5].Label != "Set" {
		t.Fatalf("selected month must remain in the sixth position: %+v", options[5])
	}
	for _, expected := range []string{"/cartoes/card-1/faturas?", "mes=3", "ano=2027", "ordem=asc"} {
		if !strings.Contains(options[11].URL, expected) {
			t.Fatalf("cross-year option lost %q in URL %q", expected, options[11].URL)
		}
	}
}

func TestFaturaMonthNavigationUsesSharedContinuousRail(t *testing.T) {
	content, err := os.ReadFile(filepath.Join(projectRoot(), "templates/pages/faturas.html"))
	if err != nil {
		t.Fatalf("read faturas template: %v", err)
	}
	for _, expected := range []string{`data-month-selector`, `id="monthRail"`, `data-month-rail-viewport`, `data-month-current="true"`, `class="month-btn btn-tactile"`} {
		if !strings.Contains(string(content), expected) {
			t.Fatalf("invoice month navigation missing shared rail marker %q", expected)
		}
	}

	script, err := os.ReadFile(filepath.Join(projectRoot(), "assets/js/faturas.js"))
	if err != nil {
		t.Fatalf("read faturas script: %v", err)
	}
	if strings.Contains(string(script), "centerActiveMonth") {
		t.Fatal("invoice page must use the global month-selector behavior, not a duplicate centering handler")
	}
}

func TestFaturasJSRebindsPaymentActionsAfterOOBSummarySwap(t *testing.T) {
	raw, err := os.ReadFile(resolveTemplatePath(t, "assets/js/faturas.js"))
	if err != nil {
		t.Fatalf("read faturas.js: %v", err)
	}
	script := string(raw)
	for _, expected := range []string{"htmx:oobAfterSwap", "invoice-summary", "bindPaymentModals"} {
		if !strings.Contains(script, expected) {
			t.Fatalf("faturas.js missing OOB payment rebind contract %q", expected)
		}
	}
}

func TestFaturasTooltipScriptShowsActionHelpOnHoverAndFocus(t *testing.T) {
	raw, err := os.ReadFile(resolveTemplatePath(t, "assets/js/tooltip.js"))
	if err != nil {
		t.Fatalf("read tooltip.js: %v", err)
	}
	script := string(raw)
	for _, expected := range []string{"data-tooltip-hover", "pointerenter", "pointerleave", "trigger.addEventListener('focus'", "trigger.addEventListener('blur'"} {
		if !strings.Contains(script, expected) {
			t.Fatalf("tooltip.js missing immediate action tooltip contract %q", expected)
		}
	}

	lifecycleRaw, err := os.ReadFile(resolveTemplatePath(t, "assets/js/htmx-lifecycle.js"))
	if err != nil {
		t.Fatalf("read htmx lifecycle: %v", err)
	}
	if !strings.Contains(string(lifecycleRaw), "initHoverTooltips") {
		t.Fatal("HTMX lifecycle must initialize hover tooltips after swaps")
	}
}

func TestHandlePagarFaturaLiquidaFaturaDebitaContaECriaProxima(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()

	seedInvoicePaymentScenario(t, db)
	scenario := moveSeedInvoiceScenarioToFuture(t, db)

	templates := template.Must(template.New("test").Parse(`
{{define "faturas-content"}}<main id="faturas-content">{{template "invoice-summary" .}}{{template "invoice-transactions" .}}</main>{{end}}
{{define "dashboard-balance"}}<section id="dashboard-balance" {{if .OOB}}hx-swap-oob="outerHTML"{{end}}></section>{{end}}
{{define "dashboard-accounts"}}<section id="dashboard-accounts" {{if .OOB}}hx-swap-oob="outerHTML"{{end}}></section>{{end}}
{{define "dashboard-cards"}}<section id="dashboard-cards" {{if .OOB}}hx-swap-oob="outerHTML"{{end}}></section>{{end}}
{{define "invoice-summary"}}<section id="invoice-summary" {{if .OOB}}hx-swap-oob="outerHTML"{{end}}></section>{{end}}
{{define "invoice-transactions"}}<section id="invoice-transactions" {{if .OOB}}hx-swap-oob="outerHTML"{{end}}></section>{{end}}
`))

	handler := FaturasHandler{
		DB:          db,
		WriterDB:    db,
		Templates:   templates,
		WorkspaceID: "ws-test",
		UserID:      "user-test",
	}

	form := url.Values{}
	form.Set("invoice_id", "invoice-2026-08")
	form.Set("payment_account_id", "checking-test")
	form.Set("payment_mode", "settle")
	form.Set("confirm_settle", "1")
	req := httptest.NewRequest(http.MethodPost, "/cartoes/faturas/pagar", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rr := httptest.NewRecorder()

	handler.HandlePagarFatura(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %q", rr.Code, rr.Body.String())
	}
	if !strings.Contains(rr.Body.String(), `hx-swap-oob="outerHTML"`) {
		t.Fatalf("expected OOB dashboard fragments, got %q", rr.Body.String())
	}

	var status string
	var paidAmount int64
	var paidAt sql.NullInt64
	if err := db.QueryRow(`
		SELECT status, paid_amount, paid_at FROM invoices WHERE id = ?
	`, "invoice-2026-08").Scan(&status, &paidAmount, &paidAt); err != nil {
		t.Fatalf("invoice query: %v", err)
	}
	if status != "PAID" {
		t.Fatalf("invoice status = %q, want PAID", status)
	}
	if paidAmount != 25000 {
		t.Fatalf("paid amount = %d, want 25000", paidAmount)
	}
	if !paidAt.Valid || paidAt.Int64 <= 0 {
		t.Fatalf("paid_at = %v, want unix timestamp", paidAt)
	}

	var balance int64
	if err := db.QueryRow(`SELECT current_balance FROM accounts WHERE id = ?`, "checking-test").Scan(&balance); err != nil {
		t.Fatalf("balance query: %v", err)
	}
	if balance != 75000 {
		t.Fatalf("balance = %d, want 75000", balance)
	}

	var paymentDescription string
	var paymentAmount int64
	if err := db.QueryRow(`
		SELECT description, amount
		FROM transactions
		WHERE account_id = ? AND invoice_id IS NULL AND description = ?
	`, "checking-test", "Pagamento de Fatura - Cartao Teste").Scan(&paymentDescription, &paymentAmount); err != nil {
		t.Fatalf("payment transaction query: %v", err)
	}
	if paymentAmount != 25000 {
		t.Fatalf("payment transaction amount = %d, want 25000", paymentAmount)
	}

	var nextStatus string
	if err := db.QueryRow(`
		SELECT status FROM invoices WHERE account_id = ? AND reference = ?
	`, "card-test", scenario.NextRef).Scan(&nextStatus); err != nil {
		t.Fatalf("next invoice query: %v", err)
	}
	if nextStatus != "OPEN" {
		t.Fatalf("next invoice status = %q, want OPEN", nextStatus)
	}

	var ipCount int
	if err := db.QueryRow(`
		SELECT COUNT(1) FROM invoice_payments WHERE invoice_id = ?
	`, "invoice-2026-08").Scan(&ipCount); err != nil {
		t.Fatalf("invoice_payments count query: %v", err)
	}
	if ipCount != 1 {
		t.Fatalf("invoice_payments count = %d, want 1", ipCount)
	}

	var ip models.InvoicePayment
	if err := db.QueryRow(`
		SELECT id, workspace_id, invoice_id, account_id, transaction_id,
		       amount_cents, paid_at, note, source, reversed_at, created_by, created_at
		FROM invoice_payments WHERE invoice_id = ?
	`, "invoice-2026-08").Scan(
		&ip.ID, &ip.WorkspaceID, &ip.InvoiceID, &ip.AccountID, &ip.TransactionID,
		&ip.AmountCents, &ip.PaidAt, &ip.Note, &ip.Source, &ip.ReversedAt, &ip.CreatedBy, &ip.CreatedAt,
	); err != nil {
		t.Fatalf("invoice_payments scan: %v", err)
	}
	if ip.WorkspaceID != "ws-test" {
		t.Fatalf("ip workspace_id = %q, want ws-test", ip.WorkspaceID)
	}
	if ip.InvoiceID != "invoice-2026-08" {
		t.Fatalf("ip invoice_id = %q, want invoice-2026-08", ip.InvoiceID)
	}
	if ip.AccountID != "checking-test" {
		t.Fatalf("ip account_id = %q, want checking-test (payment account)", ip.AccountID)
	}
	if ip.TransactionID == nil || *ip.TransactionID == "" {
		t.Fatalf("ip transaction_id should be populated")
	}
	if ip.AmountCents != 25000 {
		t.Fatalf("ip amount_cents = %d, want 25000", ip.AmountCents)
	}
	if ip.PaidAt <= 0 {
		t.Fatalf("ip paid_at = %d, want > 0", ip.PaidAt)
	}
	if ip.Note != nil {
		t.Fatalf("ip note should be nil")
	}
	if ip.Source != "manual" {
		t.Fatalf("ip source = %q, want manual", ip.Source)
	}
	if ip.ReversedAt != nil {
		t.Fatalf("ip reversed_at should be nil")
	}
	if ip.CreatedBy == nil || *ip.CreatedBy != "user-test" {
		t.Fatalf("ip created_by = %v, want user-test", ip.CreatedBy)
	}
	if ip.CreatedAt <= 0 {
		t.Fatalf("ip created_at = %d, want > 0", ip.CreatedAt)
	}
}

func TestHandlePagarFaturaQuitarSemConfirmacaoNaoCriaPagamento(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()

	seedInvoicePaymentScenario(t, db)

	handler := FaturasHandler{
		DB:          db,
		WriterDB:    db,
		Templates:   testInvoicePaymentTemplates(t),
		WorkspaceID: "ws-test",
		UserID:      "user-test",
	}

	form := url.Values{}
	form.Set("invoice_id", "invoice-2026-08")
	form.Set("payment_account_id", "checking-test")
	form.Set("payment_mode", "settle")
	req := httptest.NewRequest(http.MethodPost, "/cartoes/faturas/pagar", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rr := httptest.NewRecorder()

	handler.HandlePagarFatura(rr, req)

	if rr.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409, body = %q", rr.Code, rr.Body.String())
	}

	var ipCount, txCount int
	if err := db.QueryRow(`SELECT COUNT(1) FROM invoice_payments WHERE invoice_id = ?`, "invoice-2026-08").Scan(&ipCount); err != nil {
		t.Fatalf("invoice_payments count query: %v", err)
	}
	if ipCount != 0 {
		t.Fatalf("invoice_payments count = %d, want 0", ipCount)
	}
	if err := db.QueryRow(`SELECT COUNT(1) FROM transactions WHERE account_id = ? AND description = ?`, "checking-test", invoicePaymentDescription("Cartao Teste")).Scan(&txCount); err != nil {
		t.Fatalf("payment tx count query: %v", err)
	}
	if txCount != 0 {
		t.Fatalf("payment tx count = %d, want 0", txCount)
	}

	var status string
	var paidAmount int64
	if err := db.QueryRow(`SELECT status, COALESCE(paid_amount, 0) FROM invoices WHERE id = ?`, "invoice-2026-08").Scan(&status, &paidAmount); err != nil {
		t.Fatalf("invoice query: %v", err)
	}
	if status != "OPEN" || paidAmount != 0 {
		t.Fatalf("invoice status/paid_amount = %q/%d, want OPEN/0", status, paidAmount)
	}

	var balance int64
	if err := db.QueryRow(`SELECT current_balance FROM accounts WHERE id = ?`, "checking-test").Scan(&balance); err != nil {
		t.Fatalf("balance query: %v", err)
	}
	if balance != 100000 {
		t.Fatalf("balance = %d, want 100000", balance)
	}
}

func TestHandlePagarFaturaContaVaziaBloqueiaSemPagamento(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()

	seedInvoicePaymentScenario(t, db)

	handler := FaturasHandler{
		DB:          db,
		WriterDB:    db,
		Templates:   testInvoicePaymentTemplates(t),
		WorkspaceID: "ws-test",
		UserID:      "user-test",
	}

	form := url.Values{}
	form.Set("invoice_id", "invoice-2026-08")
	form.Set("payment_mode", "settle")
	form.Set("confirm_settle", "1")
	req := httptest.NewRequest(http.MethodPost, "/cartoes/faturas/pagar", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rr := httptest.NewRecorder()

	handler.HandlePagarFatura(rr, req)

	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400, body = %q", rr.Code, rr.Body.String())
	}

	var ipCount int
	if err := db.QueryRow(`SELECT COUNT(1) FROM invoice_payments WHERE invoice_id = ?`, "invoice-2026-08").Scan(&ipCount); err != nil {
		t.Fatalf("invoice_payments count query: %v", err)
	}
	if ipCount != 0 {
		t.Fatalf("invoice_payments count = %d, want 0", ipCount)
	}
}

func TestHandlePagarFaturaJaPagaNaoCriaNovoInvoicePayment(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()

	seedInvoicePaymentScenario(t, db)

	templates := template.Must(template.New("test").Parse(`
{{define "faturas-content"}}<main id="faturas-content">{{template "invoice-summary" .}}{{template "invoice-transactions" .}}</main>{{end}}
{{define "dashboard-balance"}}<section id="dashboard-balance" {{if .OOB}}hx-swap-oob="outerHTML"{{end}}></section>{{end}}
{{define "dashboard-accounts"}}<section id="dashboard-accounts" {{if .OOB}}hx-swap-oob="outerHTML"{{end}}></section>{{end}}
{{define "dashboard-cards"}}<section id="dashboard-cards" {{if .OOB}}hx-swap-oob="outerHTML"{{end}}></section>{{end}}
{{define "invoice-summary"}}<section id="invoice-summary" {{if .OOB}}hx-swap-oob="outerHTML"{{end}}></section>{{end}}
{{define "invoice-transactions"}}<section id="invoice-transactions" {{if .OOB}}hx-swap-oob="outerHTML"{{end}}></section>{{end}}
`))

	handler := FaturasHandler{
		DB:          db,
		WriterDB:    db,
		Templates:   templates,
		WorkspaceID: "ws-test",
		UserID:      "user-test",
	}

	form := url.Values{}
	form.Set("invoice_id", "invoice-2026-08")
	form.Set("payment_account_id", "checking-test")
	form.Set("payment_mode", "settle")
	form.Set("confirm_settle", "1")
	req := httptest.NewRequest(http.MethodPost, "/cartoes/faturas/pagar", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rr := httptest.NewRecorder()
	handler.HandlePagarFatura(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("first payment status = %d, want 200", rr.Code)
	}

	var ipCount int
	if err := db.QueryRow(`
		SELECT COUNT(1) FROM invoice_payments WHERE invoice_id = ?
	`, "invoice-2026-08").Scan(&ipCount); err != nil {
		t.Fatalf("invoice_payments count query: %v", err)
	}
	if ipCount != 1 {
		t.Fatalf("invoice_payments count after first payment = %d, want 1", ipCount)
	}

	var paidAmount int64
	if err := db.QueryRow(`SELECT paid_amount FROM invoices WHERE id = ?`, "invoice-2026-08").Scan(&paidAmount); err != nil {
		t.Fatalf("paid_amount query: %v", err)
	}
	if paidAmount != 25000 {
		t.Fatalf("paid_amount after first payment = %d, want 25000", paidAmount)
	}

	var balance int64
	if err := db.QueryRow(`SELECT current_balance FROM accounts WHERE id = ?`, "checking-test").Scan(&balance); err != nil {
		t.Fatalf("balance query: %v", err)
	}
	if balance != 75000 {
		t.Fatalf("balance after first payment = %d, want 75000", balance)
	}

	var txCount int
	if err := db.QueryRow(`
		SELECT COUNT(1) FROM transactions
		WHERE account_id = ? AND description = ?
	`, "checking-test", "Pagamento de Fatura - Cartao Teste").Scan(&txCount); err != nil {
		t.Fatalf("payment tx count query: %v", err)
	}
	if txCount != 1 {
		t.Fatalf("payment tx count after first payment = %d, want 1", txCount)
	}

	rr2 := httptest.NewRecorder()
	handler.HandlePagarFatura(rr2, req)
	if rr2.Code != http.StatusConflict {
		t.Fatalf("second payment status = %d, want 409", rr2.Code)
	}

	if err := db.QueryRow(`
		SELECT COUNT(1) FROM invoice_payments WHERE invoice_id = ?
	`, "invoice-2026-08").Scan(&ipCount); err != nil {
		t.Fatalf("invoice_payments count query after second attempt: %v", err)
	}
	if ipCount != 1 {
		t.Fatalf("invoice_payments count after second attempt = %d, want 1", ipCount)
	}

	if err := db.QueryRow(`SELECT paid_amount FROM invoices WHERE id = ?`, "invoice-2026-08").Scan(&paidAmount); err != nil {
		t.Fatalf("paid_amount query after second attempt: %v", err)
	}
	if paidAmount != 25000 {
		t.Fatalf("paid_amount after second attempt = %d, want 25000", paidAmount)
	}

	if err := db.QueryRow(`SELECT current_balance FROM accounts WHERE id = ?`, "checking-test").Scan(&balance); err != nil {
		t.Fatalf("balance query after second attempt: %v", err)
	}
	if balance != 75000 {
		t.Fatalf("balance after second attempt = %d, want 75000", balance)
	}

	if err := db.QueryRow(`
		SELECT COUNT(1) FROM transactions
		WHERE account_id = ? AND description = ?
	`, "checking-test", "Pagamento de Fatura - Cartao Teste").Scan(&txCount); err != nil {
		t.Fatalf("payment tx count query after second attempt: %v", err)
	}
	if txCount != 1 {
		t.Fatalf("payment tx count after second attempt = %d, want 1", txCount)
	}
}

func TestHandlePagarFaturaComPagamentoParcialQuitaApenasSaldoPendente(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()

	seedInvoicePaymentScenario(t, db)
	now := time.Now().Unix()
	execTestSQL(t, db, `
		INSERT INTO transactions (id, workspace_id, user_id, account_id, invoice_id, type, amount, date, description, status, installment_number, total_installments, created_at, updated_at)
		VALUES ('purchase-extra-partial', 'ws-test', 'user-test', 'card-test', 'invoice-2026-08', 'EXPENSE', 10000, ?, 'Compra Extra Parcial', 'paid', 1, 1, ?, ?)
	`, now, now, now)
	execTestSQL(t, db, `
		UPDATE accounts SET current_balance = 90000 WHERE id = 'checking-test' AND workspace_id = 'ws-test'
	`)
	execTestSQL(t, db, `
		INSERT INTO transactions (id, workspace_id, user_id, account_id, type, amount, date, description, status, installment_number, total_installments, created_at, updated_at)
		VALUES ('payment-partial-active-tx', 'ws-test', 'user-test', 'checking-test', 'EXPENSE', 10000, ?, ?, 'paid', 1, 1, ?, ?)
	`, now-200, invoicePaymentDescription("Cartao Teste"), now-200, now-200)
	execTestSQL(t, db, `
		INSERT INTO invoice_payments (id, workspace_id, invoice_id, account_id, transaction_id, amount_cents, paid_at, note, source, reversed_at, created_by, created_at)
		VALUES ('ip-partial-active', 'ws-test', 'invoice-2026-08', 'checking-test', 'payment-partial-active-tx', 10000, ?, NULL, 'manual', NULL, 'user-test', ?)
	`, now-200, now-200)
	execTestSQL(t, db, `
		INSERT INTO invoice_payments (id, workspace_id, invoice_id, account_id, transaction_id, amount_cents, paid_at, note, source, reversed_at, created_by, created_at)
		VALUES ('ip-partial-reversed', 'ws-test', 'invoice-2026-08', 'checking-test', NULL, 8000, ?, NULL, 'manual', ?, 'user-test', ?)
	`, now-100, now-50, now-100)

	templates := template.Must(template.New("test").Parse(`
{{define "faturas-content"}}<main id="faturas-content">{{template "invoice-summary" .}}{{template "invoice-transactions" .}}</main>{{end}}
{{define "dashboard-balance"}}<section id="dashboard-balance" {{if .OOB}}hx-swap-oob="outerHTML"{{end}}></section>{{end}}
{{define "dashboard-accounts"}}<section id="dashboard-accounts" {{if .OOB}}hx-swap-oob="outerHTML"{{end}}></section>{{end}}
{{define "dashboard-cards"}}<section id="dashboard-cards" {{if .OOB}}hx-swap-oob="outerHTML"{{end}}></section>{{end}}
{{define "invoice-summary"}}<section id="invoice-summary" {{if .OOB}}hx-swap-oob="outerHTML"{{end}}></section>{{end}}
{{define "invoice-transactions"}}<section id="invoice-transactions" {{if .OOB}}hx-swap-oob="outerHTML"{{end}}></section>{{end}}
`))

	handler := FaturasHandler{
		DB:          db,
		WriterDB:    db,
		Templates:   templates,
		WorkspaceID: "ws-test",
		UserID:      "user-test",
	}

	form := url.Values{}
	form.Set("invoice_id", "invoice-2026-08")
	form.Set("payment_account_id", "checking-test")
	form.Set("payment_mode", "settle")
	form.Set("confirm_settle", "1")
	req := httptest.NewRequest(http.MethodPost, "/cartoes/faturas/pagar", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rr := httptest.NewRecorder()

	handler.HandlePagarFatura(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %q", rr.Code, rr.Body.String())
	}

	var status string
	var paidAmount int64
	if err := db.QueryRow(`SELECT status, paid_amount FROM invoices WHERE id = ?`, "invoice-2026-08").Scan(&status, &paidAmount); err != nil {
		t.Fatalf("invoice query: %v", err)
	}
	if status != "PAID" {
		t.Fatalf("invoice status = %q, want PAID", status)
	}
	if paidAmount != 35000 {
		t.Fatalf("paid_amount = %d, want 35000", paidAmount)
	}

	var newPaymentAmount int64
	if err := db.QueryRow(`
		SELECT amount_cents
		FROM invoice_payments
		WHERE invoice_id = ? AND id != 'ip-partial-active' AND reversed_at IS NULL
	`, "invoice-2026-08").Scan(&newPaymentAmount); err != nil {
		t.Fatalf("new invoice_payment query: %v", err)
	}
	if newPaymentAmount != 25000 {
		t.Fatalf("new invoice_payment amount = %d, want 25000", newPaymentAmount)
	}

	var paymentTxAmount int64
	if err := db.QueryRow(`
		SELECT amount
		FROM transactions
		WHERE account_id = ?
		  AND description = ?
		  AND id != 'payment-partial-active-tx'
	`, "checking-test", invoicePaymentDescription("Cartao Teste")).Scan(&paymentTxAmount); err != nil {
		t.Fatalf("payment tx query: %v", err)
	}
	if paymentTxAmount != 25000 {
		t.Fatalf("payment transaction amount = %d, want 25000", paymentTxAmount)
	}

	var wrongFullPaymentCount int
	if err := db.QueryRow(`
		SELECT COUNT(1)
		FROM invoice_payments
		WHERE invoice_id = ? AND amount_cents = 35000 AND reversed_at IS NULL
	`, "invoice-2026-08").Scan(&wrongFullPaymentCount); err != nil {
		t.Fatalf("wrong full payment count query: %v", err)
	}
	if wrongFullPaymentCount != 0 {
		t.Fatalf("unexpected full amount payment count = %d, want 0", wrongFullPaymentCount)
	}
	if err := db.QueryRow(`
		SELECT COUNT(1)
		FROM transactions
		WHERE account_id = ?
		  AND description = ?
		  AND amount = 35000
	`, "checking-test", invoicePaymentDescription("Cartao Teste")).Scan(&wrongFullPaymentCount); err != nil {
		t.Fatalf("wrong full payment tx count query: %v", err)
	}
	if wrongFullPaymentCount != 0 {
		t.Fatalf("unexpected full amount payment transaction count = %d, want 0", wrongFullPaymentCount)
	}

	data, err := buildFaturaDataForInvoice(db, "ws-test", "invoice-2026-08", "desc")
	if err != nil {
		t.Fatalf("buildFaturaDataForInvoice: %v", err)
	}
	if data.Total.Reais != "350" || data.Total.Cents != ",00" {
		t.Fatalf("Total = R$ %s%s, want R$ 350,00", data.Total.Reais, data.Total.Cents)
	}
	if data.TotalPaid.Reais != "350" || data.TotalPaid.Cents != ",00" {
		t.Fatalf("TotalPaid = R$ %s%s, want R$ 350,00", data.TotalPaid.Reais, data.TotalPaid.Cents)
	}
	if data.PendingAmount.Reais != "0" || data.PendingAmount.Cents != ",00" {
		t.Fatalf("PendingAmount = R$ %s%s, want R$ 0,00", data.PendingAmount.Reais, data.PendingAmount.Cents)
	}
	if len(data.InvoicePayments) != 2 {
		t.Fatalf("active invoice payments = %d, want 2", len(data.InvoicePayments))
	}
}

func TestHandlePagarFaturaPagamentoParcialMenorQueSaldoMantemAberta(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()

	seedInvoicePaymentScenario(t, db)

	handler := FaturasHandler{
		DB:          db,
		WriterDB:    db,
		Templates:   testInvoicePaymentTemplates(t),
		WorkspaceID: "ws-test",
		UserID:      "user-test",
	}

	paymentDateInput := time.Now().UTC().Format("2006-01-02")
	paymentDateUnix, err := parseDate(paymentDateInput)
	if err != nil {
		t.Fatalf("parse payment date: %v", err)
	}

	form := url.Values{}
	form.Set("invoice_id", "invoice-2026-08")
	form.Set("payment_account_id", "checking-test")
	form.Set("payment_mode", "partial")
	form.Set("payment_amount", "100,00")
	form.Set("payment_date", paymentDateInput)
	req := httptest.NewRequest(http.MethodPost, "/cartoes/faturas/pagar", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rr := httptest.NewRecorder()

	handler.HandlePagarFatura(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %q", rr.Code, rr.Body.String())
	}
	for _, fragment := range []string{`id="faturas-content"`, `id="dashboard-balance" hx-swap-oob="outerHTML"`, `id="dashboard-accounts" hx-swap-oob="outerHTML"`, `id="dashboard-cards" hx-swap-oob="outerHTML"`} {
		if !strings.Contains(rr.Body.String(), fragment) {
			t.Fatalf("missing HTMX/OOB fragment %q in %q", fragment, rr.Body.String())
		}
	}

	var status string
	var paidAmount int64
	var paidAt sql.NullInt64
	if err := db.QueryRow(`SELECT status, paid_amount, paid_at FROM invoices WHERE id = ?`, "invoice-2026-08").Scan(&status, &paidAmount, &paidAt); err != nil {
		t.Fatalf("invoice query: %v", err)
	}
	if status != "OPEN" {
		t.Fatalf("invoice status = %q, want OPEN", status)
	}
	if paidAmount != 10000 {
		t.Fatalf("paid_amount = %d, want 10000", paidAmount)
	}
	if paidAt.Valid {
		t.Fatalf("paid_at = %v, want NULL while invoice remains open", paidAt)
	}

	var balance int64
	if err := db.QueryRow(`SELECT current_balance FROM accounts WHERE id = ?`, "checking-test").Scan(&balance); err != nil {
		t.Fatalf("balance query: %v", err)
	}
	if balance != 90000 {
		t.Fatalf("balance = %d, want 90000", balance)
	}

	var ipAmount, ipPaidAt, txAmount, txDate int64
	var transactionID, txStatus string
	var txInvoiceID sql.NullString
	if err := db.QueryRow(`
		SELECT ip.amount_cents, ip.paid_at, ip.transaction_id, t.amount, t.date, t.status, t.invoice_id
		FROM invoice_payments ip
		JOIN transactions t ON t.id = ip.transaction_id AND t.workspace_id = ip.workspace_id
		WHERE ip.invoice_id = ? AND ip.reversed_at IS NULL
	`, "invoice-2026-08").Scan(&ipAmount, &ipPaidAt, &transactionID, &txAmount, &txDate, &txStatus, &txInvoiceID); err != nil {
		t.Fatalf("invoice payment join query: %v", err)
	}
	if transactionID == "" {
		t.Fatalf("transaction_id should be populated")
	}
	if ipAmount != 10000 || txAmount != 10000 {
		t.Fatalf("payment amounts = ip %d tx %d, want 10000", ipAmount, txAmount)
	}
	if ipPaidAt != paymentDateUnix || txDate != paymentDateUnix {
		t.Fatalf("payment dates = ip %d tx %d, want %d", ipPaidAt, txDate, paymentDateUnix)
	}
	if txStatus != "paid" {
		t.Fatalf("transaction status = %q, want paid", txStatus)
	}
	if txInvoiceID.Valid {
		t.Fatalf("payment transaction invoice_id = %q, want NULL", txInvoiceID.String)
	}

	data, err := buildFaturaDataForInvoice(db, "ws-test", "invoice-2026-08", "desc")
	if err != nil {
		t.Fatalf("buildFaturaDataForInvoice: %v", err)
	}
	assertMoneyDisplay(t, "TotalPaid", data.TotalPaid, "100", ",00")
	assertMoneyDisplay(t, "PendingAmount", data.PendingAmount, "150", ",00")
	if !data.HasPayments || len(data.InvoicePayments) != 1 {
		t.Fatalf("invoice payments = has %v len %d, want one active payment", data.HasPayments, len(data.InvoicePayments))
	}

	_, monthlyExpense := queryDashboardMonthlySummary(db, "ws-test", time.Now().UTC())
	if monthlyExpense != 10000 {
		t.Fatalf("dashboard monthly expense = %d, want 10000", monthlyExpense)
	}
}

func TestHandlePagarFaturaClosedPartialAndExact(t *testing.T) {
	for _, tc := range []struct {
		name       string
		amount     string
		wantStatus string
		wantPaid   int64
	}{
		{name: "partial remains closed", amount: "100,00", wantStatus: "CLOSED", wantPaid: 10000},
		{name: "exact becomes paid", amount: "250,00", wantStatus: "PAID", wantPaid: 25000},
	} {
		t.Run(tc.name, func(t *testing.T) {
			db := openTestDB(t)
			defer db.Close()
			seedInvoicePaymentScenario(t, db)
			execTestSQL(t, db, `UPDATE invoices SET status = 'CLOSED', paid_at = NULL WHERE id = 'invoice-2026-08'`)
			handler := FaturasHandler{DB: db, WriterDB: db, Templates: testInvoicePaymentTemplates(t), WorkspaceID: "ws-test", UserID: "user-test"}
			form := url.Values{}
			form.Set("invoice_id", "invoice-2026-08")
			form.Set("payment_account_id", "checking-test")
			form.Set("payment_mode", "partial")
			form.Set("payment_amount", tc.amount)
			form.Set("payment_date", "2026-08-10")
			req := httptest.NewRequest(http.MethodPost, "/cartoes/faturas/pagar", strings.NewReader(form.Encode()))
			req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
			rr := httptest.NewRecorder()
			handler.HandlePagarFatura(rr, req)
			if rr.Code != http.StatusOK {
				t.Fatalf("status = %d, body = %q", rr.Code, rr.Body.String())
			}
			var status string
			var paidAmount int64
			var paidAt sql.NullInt64
			if err := db.QueryRow(`SELECT status, paid_amount, paid_at FROM invoices WHERE id = 'invoice-2026-08'`).Scan(&status, &paidAmount, &paidAt); err != nil {
				t.Fatal(err)
			}
			if status != tc.wantStatus || paidAmount != tc.wantPaid || (status == "PAID") != paidAt.Valid {
				t.Fatalf("invoice = %q/%d/%v", status, paidAmount, paidAt)
			}
		})
	}
}

func TestHandlePagarFaturaPagamentoParcialIgualSaldoQuitaFatura(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()

	seedInvoicePaymentScenario(t, db)

	handler := FaturasHandler{
		DB:          db,
		WriterDB:    db,
		Templates:   testInvoicePaymentTemplates(t),
		WorkspaceID: "ws-test",
		UserID:      "user-test",
	}

	form := url.Values{}
	form.Set("invoice_id", "invoice-2026-08")
	form.Set("payment_account_id", "checking-test")
	form.Set("payment_mode", "partial")
	form.Set("payment_amount", "250,00")
	form.Set("payment_date", time.Now().UTC().Format("2006-01-02"))
	req := httptest.NewRequest(http.MethodPost, "/cartoes/faturas/pagar", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rr := httptest.NewRecorder()

	handler.HandlePagarFatura(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %q", rr.Code, rr.Body.String())
	}

	var status string
	var paidAmount int64
	var paidAt sql.NullInt64
	if err := db.QueryRow(`SELECT status, paid_amount, paid_at FROM invoices WHERE id = ?`, "invoice-2026-08").Scan(&status, &paidAmount, &paidAt); err != nil {
		t.Fatalf("invoice query: %v", err)
	}
	if status != "PAID" {
		t.Fatalf("invoice status = %q, want PAID", status)
	}
	if paidAmount != 25000 {
		t.Fatalf("paid_amount = %d, want 25000", paidAmount)
	}
	if !paidAt.Valid || paidAt.Int64 <= 0 {
		t.Fatalf("paid_at = %v, want timestamp", paidAt)
	}
}

func TestHandlePagarFaturaPaymentTimelineDeterminesPaidAt(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	seedInvoicePaymentScenario(t, db)

	const priorPaymentAt = int64(1769947200) // 2026-02-01 noon UTC
	execTestSQL(t, db, `
		INSERT INTO invoice_payments (id, workspace_id, invoice_id, account_id, amount_cents, paid_at, source, created_by, created_at)
		VALUES ('payment-later', 'ws-test', 'invoice-2026-08', 'checking-test', 10000, ?, 'manual', 'user-test', ?)
	`, priorPaymentAt, priorPaymentAt)
	execTestSQL(t, db, `UPDATE invoices SET paid_amount = 10000 WHERE id = 'invoice-2026-08'`)

	handler := FaturasHandler{DB: db, WriterDB: db, Templates: testInvoicePaymentTemplates(t), WorkspaceID: "ws-test", UserID: "user-test"}
	form := url.Values{}
	form.Set("invoice_id", "invoice-2026-08")
	form.Set("payment_account_id", "checking-test")
	form.Set("payment_mode", "partial")
	form.Set("payment_amount", "150,00")
	form.Set("payment_date", "2026-01-01")
	req := httptest.NewRequest(http.MethodPost, "/cartoes/faturas/pagar", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rr := httptest.NewRecorder()
	handler.HandlePagarFatura(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %q", rr.Code, rr.Body.String())
	}

	var status string
	var paidAmount int64
	var paidAt sql.NullInt64
	if err := db.QueryRow(`SELECT status, paid_amount, paid_at FROM invoices WHERE id = 'invoice-2026-08'`).Scan(&status, &paidAmount, &paidAt); err != nil {
		t.Fatal(err)
	}
	if status != "PAID" || paidAmount != 25000 || !paidAt.Valid || paidAt.Int64 != priorPaymentAt {
		t.Fatalf("invoice = %q/%d/%v, want PAID/25000/%d", status, paidAmount, paidAt, priorPaymentAt)
	}
}

func TestHandlePagarFaturaLegacyPartialFailsClosed(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	seedInvoicePaymentScenario(t, db)
	execTestSQL(t, db, `UPDATE invoices SET paid_amount = 10000 WHERE id = 'invoice-2026-08'`)

	handler := FaturasHandler{DB: db, WriterDB: db, Templates: testInvoicePaymentTemplates(t), WorkspaceID: "ws-test", UserID: "user-test"}
	form := url.Values{}
	form.Set("invoice_id", "invoice-2026-08")
	form.Set("payment_account_id", "checking-test")
	form.Set("payment_mode", "settle")
	form.Set("confirm_settle", "1")
	req := httptest.NewRequest(http.MethodPost, "/cartoes/faturas/pagar", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rr := httptest.NewRecorder()
	handler.HandlePagarFatura(rr, req)
	if rr.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409, body = %q", rr.Code, rr.Body.String())
	}
	var payments, paymentTransactions int
	_ = db.QueryRow(`SELECT COUNT(1) FROM invoice_payments WHERE invoice_id = 'invoice-2026-08'`).Scan(&payments)
	_ = db.QueryRow(`SELECT COUNT(1) FROM transactions WHERE description = 'Pagamento de Fatura - Cartao Teste'`).Scan(&paymentTransactions)
	if payments != 0 || paymentTransactions != 0 {
		t.Fatalf("writes after legacy conflict = payments %d transactions %d", payments, paymentTransactions)
	}
}

func TestHandlePagarFaturaZeroTotalCreatesNoWrites(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	seedInvoicePaymentScenario(t, db)
	execTestSQL(t, db, `DELETE FROM transactions WHERE id = 'purchase-test'`)
	handler := FaturasHandler{DB: db, WriterDB: db, Templates: testInvoicePaymentTemplates(t), WorkspaceID: "ws-test", UserID: "user-test"}
	form := url.Values{}
	form.Set("invoice_id", "invoice-2026-08")
	form.Set("payment_account_id", "checking-test")
	form.Set("payment_mode", "settle")
	form.Set("confirm_settle", "1")
	req := httptest.NewRequest(http.MethodPost, "/cartoes/faturas/pagar", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rr := httptest.NewRecorder()
	handler.HandlePagarFatura(rr, req)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rr.Code)
	}
	var payments int
	_ = db.QueryRow(`SELECT COUNT(1) FROM invoice_payments WHERE invoice_id = 'invoice-2026-08'`).Scan(&payments)
	if payments != 0 {
		t.Fatalf("payments = %d, want 0", payments)
	}
}

func TestConcurrentInvoicePaymentsDoNotOverpay(t *testing.T) {
	db, writerDB := openInvoicePaymentReversalFileDBs(t)
	seedInvoicePaymentReversalFixture(t, db, reversalFixtureOptions{
		InvoiceTotal:   100000,
		ClosingCivil:   "2026-08-20",
		InvoiceStatus:  "OPEN",
		InitialBalance: 200000,
	})
	handler := FaturasHandler{
		DB:          db,
		WriterDB:    writerDB,
		Templates:   testInvoicePaymentTemplates(t),
		WorkspaceID: reversalWorkspaceID,
		UserID:      reversalUserID,
	}

	start := make(chan struct{})
	results := make(chan int, 2)
	var wg sync.WaitGroup
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			form := url.Values{}
			form.Set("invoice_id", reversalInvoiceID)
			form.Set("payment_account_id", reversalPaymentAccountID)
			form.Set("payment_mode", "settle")
			form.Set("confirm_settle", "1")
			req := httptest.NewRequest(http.MethodPost, "/cartoes/faturas/pagar", strings.NewReader(form.Encode()))
			req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
			rr := httptest.NewRecorder()
			handler.HandlePagarFatura(rr, req)
			results <- rr.Code
		}()
	}
	close(start)
	wg.Wait()
	close(results)
	statusCounts := map[int]int{}
	for status := range results {
		statusCounts[status]++
	}
	if statusCounts[http.StatusOK] != 1 || statusCounts[http.StatusConflict] != 1 {
		t.Fatalf("statuses = %v, want one 200 and one 409", statusCounts)
	}

	var status string
	var paidAmount, activeSum int64
	var activeCount, paymentTransactions int
	if err := db.QueryRow(`SELECT status, paid_amount FROM invoices WHERE id = ?`, reversalInvoiceID).Scan(&status, &paidAmount); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT COALESCE(SUM(amount_cents), 0), COUNT(1) FROM invoice_payments WHERE invoice_id = ? AND reversed_at IS NULL`, reversalInvoiceID).Scan(&activeSum, &activeCount); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT COUNT(1) FROM transactions WHERE description LIKE ?`, invoicePaymentDescriptionPrefix+"%").Scan(&paymentTransactions); err != nil {
		t.Fatal(err)
	}
	var balance int64
	_ = db.QueryRow(`SELECT current_balance FROM accounts WHERE id = ?`, reversalPaymentAccountID).Scan(&balance)
	if status != "PAID" || paidAmount != 100000 || activeSum != 100000 || activeCount != 1 || paymentTransactions != 1 || balance != 100000 {
		t.Fatalf("final state = status %q paid %d active %d/%d tx %d balance %d", status, paidAmount, activeSum, activeCount, paymentTransactions, balance)
	}
}

func TestHandlePagarFaturaPagamentoParcialValorInvalidoRejeita(t *testing.T) {
	cases := []struct {
		name   string
		amount string
	}{
		{name: "above pending", amount: "300,00"},
		{name: "zero", amount: "0"},
		{name: "negative", amount: "-10,00"},
		{name: "empty partial", amount: ""},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			db := openTestDB(t)
			defer db.Close()

			seedInvoicePaymentScenario(t, db)

			handler := FaturasHandler{
				DB:          db,
				WriterDB:    db,
				Templates:   testInvoicePaymentTemplates(t),
				WorkspaceID: "ws-test",
				UserID:      "user-test",
			}

			form := url.Values{}
			form.Set("invoice_id", "invoice-2026-08")
			form.Set("payment_account_id", "checking-test")
			form.Set("payment_mode", "partial")
			form.Set("payment_amount", tc.amount)
			req := httptest.NewRequest(http.MethodPost, "/cartoes/faturas/pagar", strings.NewReader(form.Encode()))
			req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
			rr := httptest.NewRecorder()

			handler.HandlePagarFatura(rr, req)

			if rr.Code != http.StatusUnprocessableEntity {
				t.Fatalf("status = %d, want 422, body = %q", rr.Code, rr.Body.String())
			}

			var ipCount int
			if err := db.QueryRow(`SELECT COUNT(1) FROM invoice_payments WHERE invoice_id = ?`, "invoice-2026-08").Scan(&ipCount); err != nil {
				t.Fatalf("invoice_payments count query: %v", err)
			}
			if ipCount != 0 {
				t.Fatalf("invoice_payments count = %d, want 0", ipCount)
			}

			var status string
			var paidAmount int64
			if err := db.QueryRow(`SELECT status, COALESCE(paid_amount, 0) FROM invoices WHERE id = ?`, "invoice-2026-08").Scan(&status, &paidAmount); err != nil {
				t.Fatalf("invoice query: %v", err)
			}
			if status != "OPEN" || paidAmount != 0 {
				t.Fatalf("invoice status/paid_amount = %q/%d, want OPEN/0", status, paidAmount)
			}
		})
	}
}

func TestHandleMoverFaturaAvulsaMoveParaProximaFatura(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()

	seedInvoicePaymentScenario(t, db)
	scenario := moveSeedInvoiceScenarioToFuture(t, db)

	handler := TransactionHandler{
		DB:          db,
		Templates:   testOOBTemplates(t),
		WorkspaceID: "ws-test",
		UserID:      "user-test",
	}

	req := quickInvoiceMoveRequestForTest(t, db, "purchase-test")
	rr := httptest.NewRecorder()

	handler.HandleMoverFatura(rr, req, "purchase-test")

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %q", rr.Code, rr.Body.String())
	}
	assertTransactionInvoiceReference(t, db, "purchase-test", scenario.NextRef)
	assertInvoiceExists(t, db, "card-test", scenario.NextRef, "OPEN")
	if !strings.Contains(rr.Body.String(), `id="invoice-summary"`) || !strings.Contains(rr.Body.String(), `id="dashboard-cards"`) {
		t.Fatalf("expected invoice and dashboard OOB fragments, got %q", rr.Body.String())
	}
}

func TestHandleMoverFaturaParceladaMoveCadeiaFuturaEmCascata(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()

	seedInvoicePaymentScenario(t, db)
	seedInstallmentMoveScenario(t, db)
	// Move the whole scenario forward: the final destination is created by the
	// handler and must also have a closing date ahead of the real test clock.
	now := time.Now().UTC()
	firstReference := time.Date(now.Year()+1, now.Month(), 1, 0, 0, 0, 0, time.UTC)
	var references [4]string
	for i := range references {
		referenceDate := firstReference.AddDate(0, i, 0)
		references[i] = referenceDate.Format("2006-01")
		if i == len(references)-1 {
			break // Preserve creation of the last invoice by the handler.
		}
		closingUnix, dueUnix := invoiceDatesForReference(referenceDate.Year(), referenceDate.Month(), 20, 10)
		execTestSQL(t, db, `
			UPDATE invoices SET reference = ?, closing_date = ?, due_date = ?
			WHERE id = ? AND account_id = 'card-test'
		`, references[i], closingUnix, dueUnix, fmt.Sprintf("invoice-2026-%02d", 8+i))
		occurrence := time.Date(referenceDate.Year(), referenceDate.Month()-1, 5, 12, 0, 0, 0, time.UTC)
		execTestSQL(t, db, `
			UPDATE transactions SET date = ?, date_civil = ?
			WHERE id = ? AND workspace_id = 'ws-test'
		`, occurrence.Unix(), occurrence.Format("2006-01-02"), fmt.Sprintf("installment-%d", i+1))
	}

	handler := TransactionHandler{
		DB:          db,
		Templates:   testOOBTemplates(t),
		WorkspaceID: "ws-test",
		UserID:      "user-test",
	}

	req := quickInvoiceMoveRequestForTest(t, db, "installment-2")
	rr := httptest.NewRecorder()

	handler.HandleMoverFatura(rr, req, "installment-2")

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %q", rr.Code, rr.Body.String())
	}
	assertTransactionInvoiceReference(t, db, "installment-1", references[0])
	assertTransactionInvoiceReference(t, db, "installment-2", references[2])
	assertTransactionInvoiceReference(t, db, "installment-3", references[3])
	assertInvoiceExists(t, db, "card-test", references[3], "OPEN")
}

func quickInvoiceMoveRequestForTest(t *testing.T, db *sql.DB, transactionID string) *http.Request {
	t.Helper()
	var invoiceID string
	var invoiceReference string
	var updatedAt int64
	var parentID, recurringRuleID sql.NullString
	var totalInstallments int64
	if err := db.QueryRow(`
		SELECT t.invoice_id, i.reference, t.updated_at, t.parent_id, t.recurring_rule_id, t.total_installments
		FROM transactions t JOIN invoices i ON i.id = t.invoice_id
		WHERE t.id = ?
	`, transactionID).Scan(&invoiceID, &invoiceReference, &updatedAt, &parentID, &recurringRuleID, &totalInstallments); err != nil {
		t.Fatalf("quick move source: %v", err)
	}
	destinationReference, err := nextInvoiceReference(invoiceReference)
	if err != nil {
		t.Fatalf("quick move destination: %v", err)
	}
	moveScope := "single"
	if parentID.Valid || recurringRuleID.Valid || totalInstallments > 1 {
		moveScope = "chain"
	}
	form := url.Values{}
	form.Set("expected_invoice_id", invoiceID)
	form.Set("expected_destination_reference", destinationReference)
	form.Set("expected_updated_at", fmt.Sprintf("%d", updatedAt))
	form.Set("expected_move_scope", moveScope)
	req := httptest.NewRequest(http.MethodPost, "/transacoes/"+transactionID+"/mover-fatura", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	return req
}

func TestInsertTransactionCriaNovaFaturaQuandoCompetenciaAtualJaFoiPaga(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()

	seedInvoicePaymentScenario(t, db)
	scenario := moveSeedInvoiceScenarioToFuture(t, db)
	execTestSQL(t, db, `
		UPDATE invoices
		SET status = 'PAID', paid_at = ?, paid_amount = ?
		WHERE id = 'invoice-2026-08'
	`, time.Now().Unix(), int64(25000))

	now := time.Now().Unix()
	execTestSQL(t, db, `
		INSERT INTO categories (id, workspace_id, name, icon, color, type, created_at)
		VALUES ('cat-expense', 'ws-test', 'Compras', 'shopping-bag', '#6b7280', 'EXPENSE', ?)
	`, now)

	handler := TransactionHandler{
		DB:          db,
		WorkspaceID: "ws-test",
		UserID:      "user-test",
	}

	txDate := scenario.Occurrence.AddDate(0, 1, 0).Unix()
	_, err := handler.insertTransaction(
		"EXPENSE",
		12000,
		"Compra pos pagamento",
		"",
		"", civilWriteDateFromLegacyUnix(txDate), "card-test",
		"",
		"cat-expense",
		1,
		"paid",
		false,
		"",
		"", OptionalCivilWriteDate{}, nil,
		"",
		false,
	)
	if err != nil {
		t.Fatalf("insert transaction: %v", err)
	}

	assertInvoiceExists(t, db, "card-test", scenario.NextRef, "OPEN")
	assertTransactionInvoiceReference(t, db, findTransactionByDescription(t, db, "Compra pos pagamento"), scenario.NextRef)
}

func TestBuildLancamentosDataExibeFaturaNoMesDaReferencia(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()

	seedInvoicePaymentScenario(t, db)

	handler := TransactionHandler{
		DB:          db,
		WorkspaceID: "ws-test",
		UserID:      "user-test",
	}

	data, err := handler.buildLancamentosData("", 8, 2026, LancamentosFilters{})
	if err != nil {
		t.Fatalf("buildLancamentosData: %v", err)
	}
	if len(data.Invoices) != 1 {
		t.Fatalf("invoice count = %d, want 1", len(data.Invoices))
	}
	if data.Invoices[0].Reference != "2026-08" {
		t.Fatalf("invoice reference = %q, want 2026-08", data.Invoices[0].Reference)
	}
}

func TestBuildLancamentosDataExibeFaturaAoFiltrarCartao(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()

	seedInvoicePaymentScenario(t, db)

	handler := TransactionHandler{
		DB:          db,
		WorkspaceID: "ws-test",
		UserID:      "user-test",
	}

	data, err := handler.buildLancamentosData("card-test", 8, 2026, LancamentosFilters{})
	if err != nil {
		t.Fatalf("buildLancamentosData with card filter: %v", err)
	}
	if len(data.Invoices) != 1 {
		t.Fatalf("invoice count with card filter = %d, want 1", len(data.Invoices))
	}
	if data.Invoices[0].AccountID != "card-test" {
		t.Fatalf("invoice account = %q, want card-test", data.Invoices[0].AccountID)
	}
}

func TestBuildLancamentosDataInvoicePendingPartialAndPaidBadges(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()

	seedInvoicePaymentScenario(t, db)

	handler := TransactionHandler{
		DB:          db,
		WorkspaceID: "ws-test",
		UserID:      "user-test",
	}

	data, err := handler.buildLancamentosData("", 8, 2026, LancamentosFilters{})
	if err != nil {
		t.Fatalf("buildLancamentosData: %v", err)
	}
	if len(data.Invoices) != 1 {
		t.Fatalf("invoice count = %d, want 1", len(data.Invoices))
	}
	inv := data.Invoices[0]
	if inv.Total != 25000 || inv.Paid != 0 || inv.Pending != 25000 {
		t.Fatalf("open invoice amounts total/paid/pending = %d/%d/%d, want 25000/0/25000", inv.Total, inv.Paid, inv.Pending)
	}
	if inv.BadgeLabel != "Aberta" || inv.HasPartialPayment {
		t.Fatalf("open invoice badge/partial = %q/%v, want Aberta/false", inv.BadgeLabel, inv.HasPartialPayment)
	}

	now := time.Now().Unix()
	execTestSQL(t, db, `
		UPDATE invoices SET paid_amount = 10000 WHERE id = 'invoice-2026-08'
	`)
	execTestSQL(t, db, `
		INSERT INTO invoice_payments (id, workspace_id, invoice_id, account_id, amount_cents, paid_at, source, created_by, created_at)
		VALUES ('ip-lanc-partial', 'ws-test', 'invoice-2026-08', 'checking-test', 10000, ?, 'manual', 'user-test', ?)
	`, now, now)

	pendingData, err := handler.buildLancamentosData("", 8, 2026, LancamentosFilters{Situacoes: []string{"pendente"}})
	if err != nil {
		t.Fatalf("buildLancamentosData pending: %v", err)
	}
	if len(pendingData.Invoices) != 1 {
		t.Fatalf("pending invoice count = %d, want 1", len(pendingData.Invoices))
	}
	inv = pendingData.Invoices[0]
	if inv.Total != 25000 || inv.Paid != 10000 || inv.Pending != 15000 {
		t.Fatalf("partial invoice amounts total/paid/pending = %d/%d/%d, want 25000/10000/15000", inv.Total, inv.Paid, inv.Pending)
	}
	if inv.BadgeLabel != "Parcial" || !inv.HasPartialPayment {
		t.Fatalf("partial invoice badge/partial = %q/%v, want Parcial/true", inv.BadgeLabel, inv.HasPartialPayment)
	}

	paidDataBeforeSettle, err := handler.buildLancamentosData("", 8, 2026, LancamentosFilters{Situacoes: []string{"pago"}})
	if err != nil {
		t.Fatalf("buildLancamentosData paid before settle: %v", err)
	}
	if len(paidDataBeforeSettle.Invoices) != 0 {
		t.Fatalf("paid invoice count before settle = %d, want 0", len(paidDataBeforeSettle.Invoices))
	}

	execTestSQL(t, db, `
		UPDATE invoices SET status = 'PAID', paid_at = ?, paid_amount = 25000 WHERE id = 'invoice-2026-08'
	`, now)
	execTestSQL(t, db, `
		UPDATE invoice_payments SET amount_cents = 25000 WHERE id = 'ip-lanc-partial'
	`)

	defaultData, err := handler.buildLancamentosData("", 8, 2026, LancamentosFilters{})
	if err != nil {
		t.Fatalf("buildLancamentosData default after settle: %v", err)
	}
	if len(defaultData.Invoices) != 1 {
		t.Fatalf("default invoice count after settle = %d, want 1 (paid invoices now shown by default)", len(defaultData.Invoices))
	}

	paidData, err := handler.buildLancamentosData("", 8, 2026, LancamentosFilters{Situacoes: []string{"pago"}})
	if err != nil {
		t.Fatalf("buildLancamentosData paid: %v", err)
	}
	if len(paidData.Invoices) != 1 {
		t.Fatalf("paid invoice count = %d, want 1", len(paidData.Invoices))
	}
	inv = paidData.Invoices[0]
	if inv.Pending != 0 || inv.BadgeLabel != "Paga" {
		t.Fatalf("paid invoice pending/badge = %d/%q, want 0/Paga", inv.Pending, inv.BadgeLabel)
	}

	allData, err := handler.buildLancamentosData("", 8, 2026, LancamentosFilters{Situacoes: []string{"pendente", "vencido", "agendado", "pago"}})
	if err != nil {
		t.Fatalf("buildLancamentosData all statuses: %v", err)
	}
	if len(allData.Invoices) != 1 {
		t.Fatalf("all-status invoice count = %d, want 1", len(allData.Invoices))
	}
}

func TestBuildLancamentosDataInvoiceStatusFilters(t *testing.T) {
	t.Setenv("CONTABASE_TIMEZONE", "America/Sao_Paulo")

	db := openTestDB(t)
	defer db.Close()

	seedInvoicePaymentScenario(t, db)

	now := time.Now()
	overdueDue := now.AddDate(0, 0, -2)
	overdueDue = time.Date(overdueDue.Year(), overdueDue.Month(), overdueDue.Day(), 12, 0, 0, 0, time.UTC)
	overdueClosing := overdueDue.AddDate(0, -1, 0)
	execTestSQL(t, db, `
		UPDATE invoices
		SET status = 'OPEN', due_date = ?, closing_date = ?, paid_at = NULL, paid_amount = 10000
		WHERE id = 'invoice-2026-08'
	`, overdueDue.Unix(), overdueClosing.Unix())
	execTestSQL(t, db, `
		INSERT INTO invoice_payments (id, workspace_id, invoice_id, account_id, amount_cents, paid_at, source, created_by, created_at)
		VALUES ('ip-lanc-overdue-partial', 'ws-test', 'invoice-2026-08', 'checking-test', 10000, ?, 'manual', 'user-test', ?)
	`, now.Unix(), now.Unix())

	handler := TransactionHandler{
		DB:          db,
		WorkspaceID: "ws-test",
		UserID:      "user-test",
	}
	mes, ano := int(overdueDue.Month()), overdueDue.Year()

	scheduledData, err := handler.buildLancamentosData("", mes, ano, LancamentosFilters{Situacoes: []string{"agendado"}})
	if err != nil {
		t.Fatalf("buildLancamentosData scheduled: %v", err)
	}
	if len(scheduledData.Invoices) != 0 {
		t.Fatalf("scheduled invoice count = %d, want 0", len(scheduledData.Invoices))
	}

	overdueData, err := handler.buildLancamentosData("", mes, ano, LancamentosFilters{Situacoes: []string{"vencido"}})
	if err != nil {
		t.Fatalf("buildLancamentosData overdue: %v", err)
	}
	if len(overdueData.Invoices) != 1 {
		t.Fatalf("overdue invoice count = %d, want 1", len(overdueData.Invoices))
	}
	if !overdueData.Invoices[0].IsOverdue || overdueData.Invoices[0].Pending != 15000 {
		t.Fatalf("overdue invoice overdue/pending = %v/%d, want true/15000", overdueData.Invoices[0].IsOverdue, overdueData.Invoices[0].Pending)
	}

	pendingData, err := handler.buildLancamentosData("", mes, ano, LancamentosFilters{Situacoes: []string{"pendente"}})
	if err != nil {
		t.Fatalf("buildLancamentosData pending: %v", err)
	}
	if len(pendingData.Invoices) != 1 {
		t.Fatalf("pending invoice count = %d, want 1", len(pendingData.Invoices))
	}
}

func TestLancamentosInvoiceStatusAndCoverageRemainIndependent(t *testing.T) {
	todayUnix := testUnixDate("2026-08-10")
	tests := []struct {
		name            string
		status          string
		total           int64
		effectivePaid   int64
		wantRemaining   int64
		wantBadge       string
		wantPaidFilter  bool
		wantPendingView bool
	}{
		{"open zero", models.InvoiceStatusOpen, 0, 0, 0, "Aberta", false, false},
		{"closed zero", models.InvoiceStatusClosed, 0, 0, 0, "Fechada", false, false},
		{"paid zero", models.InvoiceStatusPaid, 0, 0, 0, "Paga", true, false},
		{"open overpaid", models.InvoiceStatusOpen, 100, 120, 0, "Aberta", false, false},
		{"closed overpaid", models.InvoiceStatusClosed, 100, 120, 0, "Fechada", false, false},
		{"open partial", models.InvoiceStatusOpen, 100, 40, 60, "Parcial", false, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			inv := InvoiceRow{Status: tt.status, Total: tt.total, DueDate: testUnixDate("2026-08-20")}
			prepareLancamentosInvoiceRow(&inv, tt.effectivePaid, todayUnix)

			if inv.Status != tt.status {
				t.Fatalf("status = %q, want persisted %q", inv.Status, tt.status)
			}
			if inv.Paid != tt.effectivePaid || inv.Pending != tt.wantRemaining {
				t.Fatalf("paid/remaining = %d/%d, want %d/%d", inv.Paid, inv.Pending, tt.effectivePaid, tt.wantRemaining)
			}
			if inv.BadgeLabel != tt.wantBadge {
				t.Fatalf("badge = %q, want %q", inv.BadgeLabel, tt.wantBadge)
			}
			paidFilter := lancamentosInvoiceMatchesStatus(inv, LancamentosFilters{Situacoes: []string{"pago"}}, todayUnix)
			if paidFilter != tt.wantPaidFilter {
				t.Fatalf("paid filter = %v, want %v", paidFilter, tt.wantPaidFilter)
			}
			pendingFilter := lancamentosInvoiceMatchesStatus(inv, LancamentosFilters{Situacoes: []string{"pendente"}}, todayUnix)
			if pendingFilter != tt.wantPendingView {
				t.Fatalf("pending filter = %v, want %v", pendingFilter, tt.wantPendingView)
			}
			if tt.effectivePaid > tt.total && tt.effectivePaid-tt.total != 20 {
				t.Fatalf("overpaid = %d, want 20", tt.effectivePaid-tt.total)
			}
		})
	}
}

func TestBuildFaturaDataIncluiTransacaoPendenteNoTotal(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()

	seedInvoicePaymentScenario(t, db)
	now := time.Now().Unix()
	execTestSQL(t, db, `
		INSERT INTO transactions (id, workspace_id, user_id, account_id, invoice_id, type, amount, date, description, status, installment_number, total_installments, created_at, updated_at)
		VALUES ('purchase-pending-test', 'ws-test', 'user-test', 'card-test', 'invoice-2026-08', 'EXPENSE', 5000, ?, 'Compra Pendente', 'pending', 1, 1, ?, ?)
	`, now, now, now)

	data, err := buildFaturaDataForInvoice(db, "ws-test", "invoice-2026-08", "desc")
	if err != nil {
		t.Fatalf("buildFaturaDataForInvoice: %v", err)
	}
	total, err := sumInvoiceTotal(db, "ws-test", "invoice-2026-08")
	if err != nil {
		t.Fatalf("sumInvoiceTotal: %v", err)
	}
	if total != 30000 {
		t.Fatalf("invoice total = %d, want 30000", total)
	}
	if len(data.Transactions) != 2 {
		t.Fatalf("transactions = %d, want 2", len(data.Transactions))
	}
}

func TestBuildFaturaDataSortAndGroupByDay(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()

	seedInvoicePaymentScenario(t, db)
	now := time.Now().Unix()
	execTestSQL(t, db, `
		INSERT INTO transactions (id, workspace_id, user_id, account_id, invoice_id, type, amount, date, description, status, installment_number, total_installments, created_at, updated_at)
		VALUES
			('purchase-aug-18-a', 'ws-test', 'user-test', 'card-test', 'invoice-2026-08', 'EXPENSE', 1500, ?, 'Compra 18A', 'paid', 1, 1, ?, ?),
			('purchase-aug-18-b', 'ws-test', 'user-test', 'card-test', 'invoice-2026-08', 'EXPENSE', 2500, ?, 'Compra 18B', 'paid', 1, 1, ?, ?),
			('purchase-aug-20', 'ws-test', 'user-test', 'card-test', 'invoice-2026-08', 'EXPENSE', 3500, ?, 'Compra 20', 'paid', 1, 1, ?, ?)
	`, testUnixDate("2026-08-18"), now+1, now+1, testUnixDate("2026-08-18")+3600, now+2, now+2, testUnixDate("2026-08-20"), now+3, now+3)

	descData, err := buildFaturaDataForInvoice(db, "ws-test", "invoice-2026-08", "desc")
	if err != nil {
		t.Fatalf("buildFaturaDataForInvoice desc: %v", err)
	}
	if len(descData.TransactionGroups) != 3 {
		t.Fatalf("group count desc = %d, want 3", len(descData.TransactionGroups))
	}
	if descData.TransactionGroups[0].DateLabel != "20 Ago" || descData.TransactionGroups[1].DateLabel != "18 Ago" {
		t.Fatalf("unexpected desc leading groups: %#v", descData.TransactionGroups)
	}
	if descData.TransactionGroups[2].Transactions[0].Description != "Compra Teste" {
		t.Fatalf("unexpected desc trailing group: %#v", descData.TransactionGroups[2])
	}
	if descData.Transactions[0].Description != "Compra 20" {
		t.Fatalf("first desc tx = %q, want Compra 20", descData.Transactions[0].Description)
	}

	ascData, err := buildFaturaDataForInvoice(db, "ws-test", "invoice-2026-08", "asc")
	if err != nil {
		t.Fatalf("buildFaturaDataForInvoice asc: %v", err)
	}
	if len(ascData.TransactionGroups) != 3 {
		t.Fatalf("group count asc = %d, want 3", len(ascData.TransactionGroups))
	}
	if ascData.TransactionGroups[0].Transactions[0].Description != "Compra Teste" || ascData.TransactionGroups[1].DateLabel != "18 Ago" || ascData.TransactionGroups[2].DateLabel != "20 Ago" {
		t.Fatalf("unexpected asc group order: %#v", ascData.TransactionGroups)
	}
	if ascData.Transactions[0].Description != "Compra Teste" {
		t.Fatalf("first asc tx = %q, want Compra Teste", ascData.Transactions[0].Description)
	}
}

func TestInvoiceTransactionsTemplateHighlightsLoadMoreAndKeepsMetadataWithoutTagIcon(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()

	seedInvoicePaymentScenario(t, db)
	now := time.Now().Unix()
	for i := 0; i < 36; i++ {
		installmentNumber := 1
		totalInstallments := 1
		description := fmt.Sprintf("Compra extra %02d", i+1)
		if i == 0 {
			description = "Compra recorrente"
			installmentNumber = 1
			totalInstallments = 12
		}
		execTestSQL(t, db, `
			INSERT INTO transactions (id, workspace_id, user_id, account_id, invoice_id, type, amount, date, description, status, installment_number, total_installments, created_at, updated_at)
			VALUES (?, 'ws-test', 'user-test', 'card-test', 'invoice-2026-08', 'EXPENSE', 1000, ?, ?, 'paid', ?, ?, ?, ?)
		`, fmt.Sprintf("purchase-bulk-%02d", i+1), testUnixDate("2026-08-20")+int64(i*60), description, installmentNumber, totalInstallments, now+int64(i), now+int64(i))
	}

	data, err := buildFaturaDataForInvoice(db, "ws-test", "invoice-2026-08", "desc")
	if err != nil {
		t.Fatalf("buildFaturaDataForInvoice: %v", err)
	}
	if len(data.Transactions) != 37 {
		t.Fatalf("transactions = %d, want 37", len(data.Transactions))
	}
	if data.InitialVisibleItems != 30 {
		t.Fatalf("initial visible items = %d, want 30", data.InitialVisibleItems)
	}
	if data.VisibleItemCountLabel != "Você está vendo 30 de 37 lançamentos." {
		t.Fatalf("visible label = %q", data.VisibleItemCountLabel)
	}
	if data.HiddenItemCountLabel != "Ainda há 7 lançamentos ocultos nesta fatura." {
		t.Fatalf("hidden label = %q", data.HiddenItemCountLabel)
	}
	if data.LoadMoreButtonLabel != "Carregar mais 7 lançamentos" {
		t.Fatalf("load more label = %q", data.LoadMoreButtonLabel)
	}

	content, err := os.ReadFile(filepath.Join(projectRoot(), "templates/pages/faturas.html"))
	if err != nil {
		t.Fatalf("read template: %v", err)
	}
	tpl := template.Must(template.New("faturas").Funcs(template.FuncMap{
		"assetPath": func(path string) string { return path },
	}).Parse(string(content)))

	var buf strings.Builder
	if err := tpl.ExecuteTemplate(&buf, "invoice-transactions", data); err != nil {
		t.Fatalf("execute invoice-transactions: %v", err)
	}
	html := buf.String()
	if !strings.Contains(html, "Você está vendo 30 de 37 lançamentos.") {
		t.Fatalf("missing visible count copy: %s", html)
	}
	if !strings.Contains(html, "Ainda há 7 lançamentos ocultos nesta fatura.") {
		t.Fatalf("missing hidden count copy: %s", html)
	}
	if !strings.Contains(html, "Carregar mais 7 lançamentos") {
		t.Fatalf("missing load more button copy: %s", html)
	}
	if strings.Contains(html, `data-lucide="tag"`) {
		t.Fatalf("decorative tag icon should be absent from invoice rows: %s", html)
	}
	if !strings.Contains(html, "Sem categoria") {
		t.Fatalf("category text should remain visible: %s", html)
	}
	if !strings.Contains(html, "1/12") {
		t.Fatalf("installment badge should remain visible: %s", html)
	}
}

func TestAutoCloseInvoicesKeepsFutureZeroInvoiceOpen(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()

	seedInvoicePaymentScenario(t, db)
	execTestSQL(t, db, `DELETE FROM transactions WHERE invoice_id = 'invoice-2026-08'`)
	futureClosing := time.Now().UTC().AddDate(0, 1, 0).Unix()
	futureDue := time.Now().UTC().AddDate(0, 2, 0).Unix()
	execTestSQL(t, db, `
		UPDATE invoices
		SET status = 'OPEN', closing_date = ?, due_date = ?, paid_at = NULL, paid_amount = NULL
		WHERE id = 'invoice-2026-08'
	`, futureClosing, futureDue)

	if err := autoCloseInvoices(db, "ws-test", "card-test"); err != nil {
		t.Fatalf("autoCloseInvoices: %v", err)
	}

	assertInvoiceStatusAndZeroPayment(t, db, "invoice-2026-08", "OPEN", false)
}

func TestAutoCloseInvoicesPaysZeroInvoiceAtClosing(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()

	seedInvoicePaymentScenario(t, db)
	execTestSQL(t, db, `DELETE FROM transactions WHERE invoice_id = 'invoice-2026-08'`)
	pastClosing := time.Now().UTC().AddDate(0, -1, 0).Unix()
	futureDue := time.Now().UTC().AddDate(0, 1, 0).Unix()
	execTestSQL(t, db, `
		UPDATE invoices
		SET status = 'OPEN', closing_date = ?, due_date = ?, paid_at = NULL, paid_amount = NULL
		WHERE id = 'invoice-2026-08'
	`, pastClosing, futureDue)

	if err := autoCloseInvoices(db, "ws-test", "card-test"); err != nil {
		t.Fatalf("autoCloseInvoices: %v", err)
	}

	assertInvoiceStatusAndZeroPayment(t, db, "invoice-2026-08", "PAID", true)
}

func TestAutoCloseInvoicesPaysZeroInvoiceAtDueDate(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()

	seedInvoicePaymentScenario(t, db)
	execTestSQL(t, db, `DELETE FROM transactions WHERE invoice_id = 'invoice-2026-08'`)
	futureClosing := time.Now().UTC().AddDate(0, 1, 0).Unix()
	pastDue := time.Now().UTC().AddDate(0, -1, 0).Unix()
	execTestSQL(t, db, `
		UPDATE invoices
		SET status = 'OPEN', closing_date = ?, due_date = ?, paid_at = NULL, paid_amount = NULL
		WHERE id = 'invoice-2026-08'
	`, futureClosing, pastDue)

	if err := autoCloseInvoices(db, "ws-test", "card-test"); err != nil {
		t.Fatalf("autoCloseInvoices: %v", err)
	}

	assertInvoiceStatusAndZeroPayment(t, db, "invoice-2026-08", "PAID", true)
}

func TestAutoCloseInvoicesDoesNotPayPositiveInvoice(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()

	seedInvoicePaymentScenario(t, db)
	pastClosing := time.Now().UTC().AddDate(0, -2, 0).Unix()
	pastDue := time.Now().UTC().AddDate(0, -1, 0).Unix()
	execTestSQL(t, db, `
		UPDATE invoices
		SET status = 'OPEN', closing_date = ?, due_date = ?, paid_at = NULL, paid_amount = NULL
		WHERE id = 'invoice-2026-08'
	`, pastClosing, pastDue)

	if err := autoCloseInvoices(db, "ws-test", "card-test"); err != nil {
		t.Fatalf("autoCloseInvoices: %v", err)
	}

	assertInvoiceStatusAndZeroPayment(t, db, "invoice-2026-08", "CLOSED", false)
}

func TestResolveDashboardInvoiceSkipsAutoPaidZeroInvoice(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()

	seedInvoicePaymentScenario(t, db)
	execTestSQL(t, db, `DELETE FROM transactions WHERE invoice_id = 'invoice-2026-08'`)
	pastClosing := time.Now().UTC().AddDate(0, -2, 0).Unix()
	pastDue := time.Now().UTC().AddDate(0, -1, 0).Unix()
	nextClosing := time.Now().UTC().AddDate(0, 1, 0).Unix()
	nextDue := time.Now().UTC().AddDate(0, 2, 0).Unix()
	execTestSQL(t, db, `
		UPDATE invoices
		SET status = 'OPEN', closing_date = ?, due_date = ?, paid_at = NULL, paid_amount = NULL
		WHERE id = 'invoice-2026-08'
	`, pastClosing, pastDue)
	execTestSQL(t, db, `
		INSERT INTO invoices (id, account_id, reference, closing_date, due_date, status, created_at)
		VALUES ('invoice-next-zero-skip', 'card-test', '2099-01', ?, ?, 'OPEN', ?)
	`, nextClosing, nextDue, time.Now().Unix())

	invoiceID, _, status, _, _, err := resolveDashboardInvoice(db, "ws-test", "card-test", time.Now().Unix())
	if err != nil {
		t.Fatalf("resolveDashboardInvoice: %v", err)
	}
	if invoiceID != "invoice-next-zero-skip" || status != "OPEN" {
		t.Fatalf("dashboard invoice = %q/%q, want invoice-next-zero-skip/OPEN", invoiceID, status)
	}
	assertInvoiceStatusAndZeroPayment(t, db, "invoice-2026-08", "PAID", true)
}

func TestBuildLancamentosDataZeroPaidInvoiceDoesNotBecomePendingExpense(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()

	seedInvoicePaymentScenario(t, db)
	execTestSQL(t, db, `DELETE FROM transactions WHERE invoice_id = 'invoice-2026-08'`)
	due := time.Now().UTC().AddDate(0, -1, 0)
	dueDate := time.Date(due.Year(), due.Month(), 10, 12, 0, 0, 0, time.UTC)
	closingDate := dueDate.AddDate(0, -1, 0)
	execTestSQL(t, db, `
		UPDATE invoices
		SET status = 'OPEN', due_date = ?, closing_date = ?, paid_at = NULL, paid_amount = NULL
		WHERE id = 'invoice-2026-08'
	`, dueDate.Unix(), closingDate.Unix())

	handler := TransactionHandler{
		DB:          db,
		WorkspaceID: "ws-test",
		UserID:      "user-test",
	}
	data, err := handler.buildLancamentosData("", int(dueDate.Month()), dueDate.Year(), LancamentosFilters{Situacoes: []string{"pendente", "vencido", "agendado"}})
	if err != nil {
		t.Fatalf("buildLancamentosData: %v", err)
	}
	if len(data.Invoices) != 0 {
		t.Fatalf("invoice count = %d, want 0", len(data.Invoices))
	}
	for _, item := range data.UnifiedItems {
		if item.IsInvoice && item.Invoice.ID == "invoice-2026-08" {
			t.Fatal("zero paid invoice should not be rendered as a pending unified expense")
		}
	}
}

func TestBuildFaturaDataMaterializaRecorrenciaAntigaNaFatura(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()

	seedInvoicePaymentScenario(t, db)
	now := time.Now().Unix()
	execTestSQL(t, db, `
		INSERT INTO invoices (id, account_id, reference, closing_date, due_date, status, created_at)
		VALUES ('invoice-2026-12', 'card-test', '2026-12', ?, ?, 'OPEN', ?)
	`, testUnixDate("2026-11-20"), testUnixDate("2026-12-10"), now)
	execTestSQL(t, db, `
		INSERT INTO recurring_rules (id, workspace_id, user_id, account_id, type, amount, description, start_date, frequency, default_payment_status, active, created_at, updated_at)
		VALUES ('rule-card-monthly', 'ws-test', 'user-test', 'card-test', 'EXPENSE', 7000, 'Assinatura Cartao', ?, 'MONTHLY', 'PAID', 1, ?, ?)
	`, testUnixDate("2026-01-05"), now, now)

	for i := 0; i < 2; i++ {
		data, err := buildFaturaDataForInvoice(db, "ws-test", "invoice-2026-12", "desc")
		if err != nil {
			t.Fatalf("buildFaturaDataForInvoice run %d: %v", i+1, err)
		}
		if len(data.Transactions) != 1 {
			t.Fatalf("run %d transactions = %d, want 1", i+1, len(data.Transactions))
		}
	}
	total, err := sumInvoiceTotal(db, "ws-test", "invoice-2026-12")
	if err != nil {
		t.Fatalf("sumInvoiceTotal: %v", err)
	}
	if total != 7000 {
		t.Fatalf("invoice total = %d, want 7000", total)
	}
	var invoiceID, status, seriesID string
	occurrence := time.Date(2026, 11, 5, 12, 0, 0, 0, time.UTC).Unix()
	if err := db.QueryRow(`
		SELECT invoice_id, status, COALESCE(series_id, '')
		FROM transactions
		WHERE recurring_rule_id = 'rule-card-monthly' AND date = ?
	`, occurrence).Scan(&invoiceID, &status, &seriesID); err != nil {
		t.Fatalf("materialized recurrence query: %v", err)
	}
	if invoiceID != "invoice-2026-12" || status != "paid" {
		t.Fatalf("materialized invoice/status = %q/%q, want invoice-2026-12/paid", invoiceID, status)
	}
	if seriesID != "rule-card-monthly" {
		t.Fatalf("materialized series_id = %q, want rule-card-monthly", seriesID)
	}
	var count int
	if err := db.QueryRow(`
		SELECT COUNT(1)
		FROM transactions
		WHERE recurring_rule_id = 'rule-card-monthly' AND date = ?
	`, occurrence).Scan(&count); err != nil {
		t.Fatalf("materialized recurrence count query: %v", err)
	}
	if count != 1 {
		t.Fatalf("materialized recurrence count = %d, want 1", count)
	}
}

func TestInsertTransactionParceladaComFaturaManualDistribuiParcelas(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()

	seedInvoicePaymentScenario(t, db)
	now := time.Now().Unix()
	execTestSQL(t, db, `
		INSERT INTO categories (id, workspace_id, name, icon, color, type, created_at)
		VALUES ('cat-installment', 'ws-test', 'Compras', 'shopping-bag', '#6b7280', 'EXPENSE', ?)
	`, now)

	handler := TransactionHandler{DB: db, WorkspaceID: "ws-test", UserID: "user-test"}
	_, err := handler.insertTransaction(
		"EXPENSE",
		30000,
		"Compra Parcelada Override",
		"",
		"", civilWriteDateFromLegacyUnix(testUnixDate("2026-07-05")), "card-test",
		"",
		"cat-installment",
		3,
		"paid",
		false,
		"",
		"", OptionalCivilWriteDate{}, nil,
		"invoice-2026-08",
		false,
	)
	if err != nil {
		t.Fatalf("insertTransaction: %v", err)
	}

	for installment, wantReference := range map[int]string{1: "2026-08", 2: "2026-09", 3: "2026-10"} {
		var got string
		if err := db.QueryRow(`
			SELECT i.reference
			FROM transactions t
			JOIN invoices i ON i.id = t.invoice_id
			WHERE t.description = 'Compra Parcelada Override' AND t.installment_number = ?
		`, installment).Scan(&got); err != nil {
			t.Fatalf("installment %d query: %v", installment, err)
		}
		if got != wantReference {
			t.Fatalf("installment %d reference = %q, want %q", installment, got, wantReference)
		}
	}
}

func TestInvoiceDatesForReferenceClampDays293031(t *testing.T) {
	cases := []struct {
		name            string
		year            int
		month           time.Month
		closingDay      int64
		dueDay          int64
		wantClosingDate string
		wantDueDate     string
	}{
		{
			name:            "fevereiro com 31/31 faz clamp para 28",
			year:            2026,
			month:           time.February,
			closingDay:      31,
			dueDay:          31,
			wantClosingDate: "2026-02-28",
			wantDueDate:     "2026-02-28",
		},
		{
			name:            "mes de 30 dias com 31/31 faz clamp para 30",
			year:            2026,
			month:           time.April,
			closingDay:      31,
			dueDay:          31,
			wantClosingDate: "2026-04-30",
			wantDueDate:     "2026-04-30",
		},
		{
			name:            "mes de 31 dias preserva 31",
			year:            2026,
			month:           time.May,
			closingDay:      31,
			dueDay:          31,
			wantClosingDate: "2026-05-31",
			wantDueDate:     "2026-05-31",
		},
		{
			name:            "dueDay menor que closingDay fecha no mes anterior",
			year:            2026,
			month:           time.August,
			closingDay:      31,
			dueDay:          10,
			wantClosingDate: "2026-07-31",
			wantDueDate:     "2026-08-10",
		},
		{
			name:            "dueDay maior que closingDay fecha no mesmo mes",
			year:            2026,
			month:           time.August,
			closingDay:      10,
			dueDay:          25,
			wantClosingDate: "2026-08-10",
			wantDueDate:     "2026-08-25",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			closingUnix, dueUnix := invoiceDatesForReference(tc.year, tc.month, tc.closingDay, tc.dueDay)
			closing := time.Unix(closingUnix, 0).UTC().Format("2006-01-02")
			due := time.Unix(dueUnix, 0).UTC().Format("2006-01-02")
			if closing != tc.wantClosingDate {
				t.Fatalf("closing date = %s, want %s", closing, tc.wantClosingDate)
			}
			if due != tc.wantDueDate {
				t.Fatalf("due date = %s, want %s", due, tc.wantDueDate)
			}
		})
	}
}

func TestCalculateInvoiceDatesClosingDayStartsNextCycle(t *testing.T) {
	cases := []struct {
		name            string
		date            string
		closingDay      int64
		dueDay          int64
		wantReference   string
		wantClosingDate string
		wantDueDate     string
	}{
		{
			name:            "dia anterior permanece na fatura corrente",
			date:            "2026-08-31",
			closingDay:      1,
			dueDay:          7,
			wantReference:   "2026-09",
			wantClosingDate: "2026-09-01",
			wantDueDate:     "2026-09-07",
		},
		{
			name:            "dia do fechamento inicia a proxima fatura",
			date:            "2026-09-01",
			closingDay:      1,
			dueDay:          7,
			wantReference:   "2026-10",
			wantClosingDate: "2026-10-01",
			wantDueDate:     "2026-10-07",
		},
		{
			name:            "dia seguinte permanece na proxima fatura",
			date:            "2026-09-02",
			closingDay:      1,
			dueDay:          7,
			wantReference:   "2026-10",
			wantClosingDate: "2026-10-01",
			wantDueDate:     "2026-10-07",
		},
		{
			name:            "fechamento 31 usa o ultimo dia de fevereiro",
			date:            "2026-02-28",
			closingDay:      31,
			dueDay:          7,
			wantReference:   "2026-04",
			wantClosingDate: "2026-03-31",
			wantDueDate:     "2026-04-07",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			purchaseDate, err := time.ParseInLocation("2006-01-02", tc.date, time.UTC)
			if err != nil {
				t.Fatalf("parse date: %v", err)
			}
			purchaseDate = purchaseDate.Add(12 * time.Hour)

			closingUnix, dueUnix, reference := calculateInvoiceDates(purchaseDate, tc.closingDay, tc.dueDay)
			if reference != tc.wantReference {
				t.Fatalf("reference = %q, want %q", reference, tc.wantReference)
			}
			if got := time.Unix(closingUnix, 0).UTC().Format("2006-01-02"); got != tc.wantClosingDate {
				t.Fatalf("closing date = %q, want %q", got, tc.wantClosingDate)
			}
			if got := time.Unix(dueUnix, 0).UTC().Format("2006-01-02"); got != tc.wantDueDate {
				t.Fatalf("due date = %q, want %q", got, tc.wantDueDate)
			}
		})
	}
}

func TestInvoiceCycleBoundsUseClosingDayAsNextCycleBoundary(t *testing.T) {
	startUnix, endUnix := invoiceCycleBoundsForReference(2026, time.September, 1, 7)
	wantStart := time.Date(2026, time.August, 1, 12, 0, 0, 0, time.UTC).Unix()
	wantEnd := time.Date(2026, time.September, 1, 12, 0, 0, 0, time.UTC).Unix()

	if startUnix != wantStart {
		t.Fatalf("cycle start = %s, want %s", time.Unix(startUnix, 0).UTC(), time.Unix(wantStart, 0).UTC())
	}
	if endUnix != wantEnd {
		t.Fatalf("cycle end = %s, want %s", time.Unix(endUnix, 0).UTC(), time.Unix(wantEnd, 0).UTC())
	}
}

func TestEnsureInvoiceForReferenceTxUsesSafeClampedDates(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()

	seedInvoicePaymentScenario(t, db)

	now := time.Now().Unix()
	execTestSQL(t, db, `
		INSERT INTO accounts (id, workspace_id, name, type, initial_balance, current_balance, created_at, updated_at)
		VALUES ('card-31-10', 'ws-test', 'Card 31-10', 'CREDIT_CARD', 0, 0, ?, ?)
	`, now, now)
	execTestSQL(t, db, `
		INSERT INTO credit_cards (id, account_id, closing_day, due_day, credit_limit)
		VALUES ('cc-31-10', 'card-31-10', 31, 10, 100000)
	`)
	execTestSQL(t, db, `
		INSERT INTO accounts (id, workspace_id, name, type, initial_balance, current_balance, created_at, updated_at)
		VALUES ('card-10-31', 'ws-test', 'Card 10-31', 'CREDIT_CARD', 0, 0, ?, ?)
	`, now, now)
	execTestSQL(t, db, `
		INSERT INTO credit_cards (id, account_id, closing_day, due_day, credit_limit)
		VALUES ('cc-10-31', 'card-10-31', 10, 31, 100000)
	`)

	tx, err := db.Begin()
	if err != nil {
		t.Fatalf("begin tx: %v", err)
	}
	defer tx.Rollback()

	idA, refA, _, closeA, dueA, err := ensureInvoiceForReferenceTx(tx, "ws-test", "card-31-10", 2026, time.February)
	if err != nil {
		t.Fatalf("ensure invoice case A: %v", err)
	}
	if idA == "" || refA != "2026-02" {
		t.Fatalf("case A invoice id/ref = %q/%q", idA, refA)
	}
	if got := time.Unix(closeA, 0).UTC().Format("2006-01-02"); got != "2026-01-31" {
		t.Fatalf("case A closing = %s, want 2026-01-31", got)
	}
	if got := time.Unix(dueA, 0).UTC().Format("2006-01-02"); got != "2026-02-10" {
		t.Fatalf("case A due = %s, want 2026-02-10", got)
	}

	idB, refB, _, closeB, dueB, err := ensureInvoiceForReferenceTx(tx, "ws-test", "card-10-31", 2026, time.February)
	if err != nil {
		t.Fatalf("ensure invoice case B: %v", err)
	}
	if idB == "" || refB != "2026-02" {
		t.Fatalf("case B invoice id/ref = %q/%q", idB, refB)
	}
	if got := time.Unix(closeB, 0).UTC().Format("2006-01-02"); got != "2026-02-10" {
		t.Fatalf("case B closing = %s, want 2026-02-10", got)
	}
	if got := time.Unix(dueB, 0).UTC().Format("2006-01-02"); got != "2026-02-28" {
		t.Fatalf("case B due = %s, want 2026-02-28", got)
	}
}

func openTestDB(t *testing.T) *sql.DB {
	t.Helper()

	db, err := openGuardedHandlerFixture(t)
	if err != nil {
		t.Fatalf("open test db: %v", err)
	}
	return db
}

func testOOBTemplates(t *testing.T) *template.Template {
	t.Helper()

	return template.Must(template.New("test").Parse(`
{{define "dashboard-cards"}}<section id="dashboard-cards" {{if .OOB}}hx-swap-oob="outerHTML"{{end}}></section>{{end}}
{{define "invoice-summary"}}<section id="invoice-summary" {{if .OOB}}hx-swap-oob="outerHTML"{{end}}></section>{{end}}
{{define "invoice-transactions"}}<section id="invoice-transactions" {{if .OOB}}hx-swap-oob="outerHTML"{{end}}></section>{{end}}
`))
}

func testInvoicePaymentTemplates(t *testing.T) *template.Template {
	t.Helper()

	return template.Must(template.New("test").Parse(`
{{define "faturas-content"}}<main id="faturas-content">{{template "invoice-summary" .}}{{template "invoice-transactions" .}}</main>{{end}}
{{define "dashboard-balance"}}<section id="dashboard-balance" {{if .OOB}}hx-swap-oob="outerHTML"{{end}}></section>{{end}}
{{define "dashboard-accounts"}}<section id="dashboard-accounts" {{if .OOB}}hx-swap-oob="outerHTML"{{end}}></section>{{end}}
{{define "dashboard-cards"}}<section id="dashboard-cards" {{if .OOB}}hx-swap-oob="outerHTML"{{end}}></section>{{end}}
{{define "invoice-summary"}}<section id="invoice-summary" {{if .OOB}}hx-swap-oob="outerHTML"{{end}}>{{if .HasPayments}}has-payments{{end}}</section>{{end}}
{{define "invoice-transactions"}}<section id="invoice-transactions" {{if .OOB}}hx-swap-oob="outerHTML"{{end}}></section>{{end}}
`))
}

func seedInvoicePaymentScenario(t *testing.T, db *sql.DB) {
	t.Helper()

	ref := time.Date(2026, 7, 15, 12, 0, 0, 0, time.UTC)
	now := ref.Unix()
	// Keep the fixture's OPEN-state boundary ahead of the real test clock while
	// preserving its reference-month due date for reporting tests.
	closingDate := time.Now().UTC().AddDate(0, 1, 0).Unix()
	dueDate := ref.AddDate(0, 1, 10).Unix()
	execTestSQL(t, db, `
		INSERT INTO users (id, name, email, password_hash, created_at, updated_at)
		VALUES ('user-test', 'User Test', 'user-test@example.com', 'hash', ?, ?)
	`, now, now)
	execTestSQL(t, db, `
		INSERT INTO workspaces (id, name, description, created_at, updated_at)
		VALUES ('ws-test', 'Workspace Test', '', ?, ?)
	`, now, now)
	execTestSQL(t, db, `
		INSERT INTO workspace_members (workspace_id, user_id, role, joined_at)
		VALUES ('ws-test', 'user-test', 'ADMIN', ?)
	`, now)
	execTestSQL(t, db, `
		INSERT INTO accounts (id, workspace_id, name, type, initial_balance, current_balance, created_at, updated_at)
		VALUES ('checking-test', 'ws-test', 'Conta Teste', 'CHECKING', 100000, 100000, ?, ?)
	`, now, now)
	execTestSQL(t, db, `
		INSERT INTO accounts (id, workspace_id, name, type, initial_balance, current_balance, created_at, updated_at)
		VALUES ('card-test', 'ws-test', 'Cartao Teste', 'CREDIT_CARD', 0, 0, ?, ?)
	`, now, now)
	execTestSQL(t, db, `
		INSERT INTO credit_cards (id, account_id, closing_day, due_day, credit_limit)
		VALUES ('credit-card-test', 'card-test', 20, 10, 500000)
	`)
	execTestSQL(t, db, `
		INSERT INTO invoices (id, account_id, reference, closing_date, due_date, status, created_at)
		VALUES ('invoice-2026-08', 'card-test', '2026-08', ?, ?, 'OPEN', ?)
	`, closingDate, dueDate, now)
	execTestSQL(t, db, `
		INSERT INTO transactions (id, workspace_id, user_id, account_id, invoice_id, type, amount, date, description, status, installment_number, total_installments, created_at, updated_at)
		VALUES ('purchase-test', 'ws-test', 'user-test', 'card-test', 'invoice-2026-08', 'EXPENSE', 25000, ?, 'Compra Teste', 'paid', 1, 1, ?, ?)
	`, now, now, now)
}

func seedInstallmentMoveScenario(t *testing.T, db *sql.DB) {
	t.Helper()

	now := time.Now().Unix()
	for _, inv := range []struct {
		id        string
		reference string
		closeDate string
		dueDate   string
	}{
		{"invoice-2026-09", "2026-09", "2026-08-20", "2026-09-10"},
		{"invoice-2026-10", "2026-10", "2026-09-20", "2026-10-10"},
	} {
		execTestSQL(t, db, `
			INSERT INTO invoices (id, account_id, reference, closing_date, due_date, status, created_at)
			VALUES (?, 'card-test', ?, ?, ?, 'OPEN', ?)
		`, inv.id, inv.reference, testUnixDate(inv.closeDate), testUnixDate(inv.dueDate), now)
	}

	for _, tx := range []struct {
		id          string
		invoiceID   string
		parentID    interface{}
		installment int
		dateStr     string
	}{
		{"installment-1", "invoice-2026-08", nil, 1, "2026-07-05"},
		{"installment-2", "invoice-2026-09", "installment-1", 2, "2026-08-05"},
		{"installment-3", "invoice-2026-10", "installment-1", 3, "2026-09-05"},
	} {
		execTestSQL(t, db, `
			INSERT INTO transactions (id, workspace_id, user_id, account_id, invoice_id, type, amount, date, description, status, installment_number, total_installments, parent_id, created_at, updated_at)
			VALUES (?, 'ws-test', 'user-test', 'card-test', ?, 'EXPENSE', 10000, ?, 'Compra Parcelada', 'paid', ?, 3, ?, ?, ?)
		`, tx.id, tx.invoiceID, testUnixDate(tx.dateStr), tx.installment, tx.parentID, now, now)
	}
}

func testUnixDate(date string) int64 {
	t, _ := time.Parse("2006-01-02", date)
	return t.Unix()
}

func futureOpenInvoiceDatesForTest() (closingUnix, dueUnix int64) {
	closing := time.Now().UTC().AddDate(5, 0, 0)
	due := closing.AddDate(0, 1, 0)
	return closing.Unix(), due.Unix()
}

type futureInvoiceScenarioForTest struct {
	Occurrence time.Time
	CurrentRef string
	NextRef    string
}

func moveSeedInvoiceScenarioToFuture(t *testing.T, db *sql.DB) futureInvoiceScenarioForTest {
	t.Helper()

	base := time.Now().UTC().AddDate(1, 0, 0)
	occurrence := time.Date(base.Year(), base.Month(), 25, 12, 0, 0, 0, time.UTC)
	closingUnix, dueUnix, currentRef := calculateInvoiceDates(occurrence, 20, 10)
	nextRef, err := nextInvoiceReference(currentRef)
	if err != nil {
		t.Fatalf("next future invoice reference: %v", err)
	}
	execTestSQL(t, db, `
		UPDATE invoices
		SET reference = ?, closing_date = ?, due_date = ?
		WHERE id = 'invoice-2026-08' AND account_id = 'card-test'
	`, currentRef, closingUnix, dueUnix)
	execTestSQL(t, db, `
		UPDATE transactions
		SET date = ?, date_civil = ?
		WHERE id = 'purchase-test' AND workspace_id = 'ws-test'
	`, occurrence.Unix(), occurrence.Format("2006-01-02"))

	return futureInvoiceScenarioForTest{
		Occurrence: occurrence,
		CurrentRef: currentRef,
		NextRef:    nextRef,
	}
}

func keepOpenTestInvoicesFuture(t *testing.T, db *sql.DB, accountID string) {
	t.Helper()
	closingUnix, dueUnix := futureOpenInvoiceDatesForTest()
	execTestSQL(t, db, `
		UPDATE invoices
		SET status = 'OPEN', closing_date = ?, due_date = ?, paid_at = NULL, paid_amount = 0
		WHERE account_id = ? AND status IN ('OPEN', 'CLOSED')
	`, closingUnix, dueUnix, accountID)
}

func seedInvoiceLimitScenario(t *testing.T, db *sql.DB, creditLimit, expenseAmount int64) {
	t.Helper()

	now := time.Now().Unix()
	futureClosing := time.Date(2026, 7, 20, 12, 0, 0, 0, time.UTC).Unix()
	futureDue := time.Date(2026, 8, 10, 12, 0, 0, 0, time.UTC).Unix()
	execTestSQL(t, db, `
		INSERT INTO users (id, name, email, password_hash, created_at, updated_at)
		VALUES ('user-limit', 'User Limit', 'user-limit@example.com', 'hash', ?, ?)
	`, now, now)
	execTestSQL(t, db, `
		INSERT INTO workspaces (id, name, description, created_at, updated_at)
		VALUES ('ws-limit', 'Workspace Limit', '', ?, ?)
	`, now, now)
	execTestSQL(t, db, `
		INSERT INTO workspace_members (workspace_id, user_id, role, joined_at)
		VALUES ('ws-limit', 'user-limit', 'ADMIN', ?)
	`, now)
	execTestSQL(t, db, `
		INSERT INTO accounts (id, workspace_id, name, type, initial_balance, current_balance, created_at, updated_at)
		VALUES ('card-limit', 'ws-limit', 'Cartao Limit', 'CREDIT_CARD', 0, 0, ?, ?)
	`, now, now)
	execTestSQL(t, db, `
		INSERT INTO credit_cards (id, account_id, closing_day, due_day, credit_limit)
		VALUES ('credit-card-limit', 'card-limit', 20, 10, ?)
	`, creditLimit)
	execTestSQL(t, db, `
		INSERT INTO invoices (id, account_id, reference, closing_date, due_date, status, created_at)
		VALUES ('invoice-limit-2026-08', 'card-limit', '2026-08', ?, ?, 'OPEN', ?)
	`, futureClosing, futureDue, now)
	if expenseAmount > 0 {
		execTestSQL(t, db, `
			INSERT INTO transactions (id, workspace_id, user_id, account_id, invoice_id, type, amount, date, description, status, installment_number, total_installments, created_at, updated_at)
			VALUES ('purchase-limit', 'ws-limit', 'user-limit', 'card-limit', 'invoice-limit-2026-08', 'EXPENSE', ?, ?, 'Compra Limit', 'paid', 1, 1, ?, ?)
		`, expenseAmount, now, now, now)
	}
}

func TestFaturaExibeLimiteDoCartaoDisponivelCalculado(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()

	seedInvoiceLimitScenario(t, db, 100000, 64000)

	data, err := buildFaturaDataForInvoice(db, "ws-limit", "invoice-limit-2026-08", "desc")
	if err != nil {
		t.Fatalf("build fatura data error: %v", err)
	}

	if !data.HasCreditLimit {
		t.Fatalf("expected HasCreditLimit true")
	}
	if data.CreditLimit.Reais != "1.000" || data.CreditLimit.Cents != ",00" {
		t.Fatalf("CreditLimit = R$ %s%s, want R$ 1.000,00", data.CreditLimit.Reais, data.CreditLimit.Cents)
	}
	if data.LimitUsed.Reais != "640" || data.LimitUsed.Cents != ",00" {
		t.Fatalf("LimitUsed = R$ %s%s, want R$ 640,00", data.LimitUsed.Reais, data.LimitUsed.Cents)
	}
	if data.LimitAvailable.Reais != "360" || data.LimitAvailable.Cents != ",00" {
		t.Fatalf("LimitAvailable = R$ %s%s, want R$ 360,00", data.LimitAvailable.Reais, data.LimitAvailable.Cents)
	}
	if data.LimitPercent != 36 {
		t.Fatalf("LimitPercent = %d, want 36", data.LimitPercent)
	}
}

func TestFaturaLimiteDisponivelZeradoQuandoTotalMaiorQueLimite(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()

	seedInvoiceLimitScenario(t, db, 50000, 64000)

	data, err := buildFaturaDataForInvoice(db, "ws-limit", "invoice-limit-2026-08", "desc")
	if err != nil {
		t.Fatalf("build fatura data error: %v", err)
	}

	if !data.HasCreditLimit {
		t.Fatalf("expected HasCreditLimit true")
	}
	if data.LimitAvailable.Reais != "0" || data.LimitAvailable.Cents != ",00" {
		t.Fatalf("LimitAvailable = R$ %s%s, want R$ 0,00", data.LimitAvailable.Reais, data.LimitAvailable.Cents)
	}
	if data.LimitPercent != 0 {
		t.Fatalf("LimitPercent = %d, want 0", data.LimitPercent)
	}
}

func TestFaturaSemLimiteDeCreditoNaoExibeIndicador(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()

	seedInvoiceLimitScenario(t, db, 0, 25000)

	data, err := buildFaturaDataForInvoice(db, "ws-limit", "invoice-limit-2026-08", "desc")
	if err != nil {
		t.Fatalf("build fatura data error: %v", err)
	}

	if data.HasCreditLimit {
		t.Fatalf("expected HasCreditLimit false when credit_limit is 0")
	}
	if data.Total.Reais != "250" || data.Total.Cents != ",00" {
		t.Fatalf("Total = R$ %s%s, want R$ 250,00", data.Total.Reais, data.Total.Cents)
	}
}

func assertTransactionInvoiceReference(t *testing.T, db *sql.DB, transactionID, wantReference string) {
	t.Helper()

	var got string
	if err := db.QueryRow(`
		SELECT i.reference
		FROM transactions t
		JOIN invoices i ON i.id = t.invoice_id
		WHERE t.id = ?
	`, transactionID).Scan(&got); err != nil {
		t.Fatalf("transaction invoice reference query: %v", err)
	}
	if got != wantReference {
		t.Fatalf("transaction %s invoice reference = %q, want %q", transactionID, got, wantReference)
	}
}

func assertInvoiceExists(t *testing.T, db *sql.DB, accountID, reference, wantStatus string) {
	t.Helper()

	var gotStatus string
	if err := db.QueryRow(`
		SELECT status FROM invoices WHERE account_id = ? AND reference = ?
	`, accountID, reference).Scan(&gotStatus); err != nil {
		t.Fatalf("invoice %s query: %v", reference, err)
	}
	if gotStatus != wantStatus {
		t.Fatalf("invoice %s status = %q, want %q", reference, gotStatus, wantStatus)
	}
}

func assertInvoiceStatusAndZeroPayment(t *testing.T, db *sql.DB, invoiceID, wantStatus string, wantPaid bool) {
	t.Helper()

	var status string
	var paidAt sql.NullInt64
	var paidAmount sql.NullInt64
	if err := db.QueryRow(`
		SELECT status, paid_at, paid_amount FROM invoices WHERE id = ?
	`, invoiceID).Scan(&status, &paidAt, &paidAmount); err != nil {
		t.Fatalf("invoice status query: %v", err)
	}
	if status != wantStatus {
		t.Fatalf("invoice status = %q, want %q", status, wantStatus)
	}
	if wantPaid {
		if !paidAt.Valid || paidAt.Int64 <= 0 {
			t.Fatalf("paid_at = %v, want unix timestamp", paidAt)
		}
		if !paidAmount.Valid || paidAmount.Int64 != 0 {
			t.Fatalf("paid_amount = %v, want 0", paidAmount)
		}
		return
	}
	if paidAt.Valid || paidAmount.Valid {
		t.Fatalf("payment fields = paid_at %v paid_amount %v, want NULL/NULL", paidAt, paidAmount)
	}
}

func findTransactionByDescription(t *testing.T, db *sql.DB, description string) string {
	t.Helper()

	var id string
	if err := db.QueryRow(`
		SELECT id
		FROM transactions
		WHERE workspace_id = 'ws-test' AND description = ?
		ORDER BY created_at DESC
		LIMIT 1
	`, description).Scan(&id); err != nil {
		t.Fatalf("find transaction by description: %v", err)
	}
	return id
}

func TestBuildFaturaDataSemPagamentosMostraTotalPendente(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()

	seedInvoicePaymentScenario(t, db)

	data, err := buildFaturaDataForInvoice(db, "ws-test", "invoice-2026-08", "desc")
	if err != nil {
		t.Fatalf("buildFaturaDataForInvoice: %v", err)
	}
	if data.HasPayments {
		t.Fatalf("HasPayments should be false for invoice without payments")
	}
	if data.TotalPaid.Reais != "0" || data.TotalPaid.Cents != ",00" {
		t.Fatalf("TotalPaid = R$ %s%s, want R$ 0,00", data.TotalPaid.Reais, data.TotalPaid.Cents)
	}
	if data.PendingAmount.Reais != "250" || data.PendingAmount.Cents != ",00" {
		t.Fatalf("PendingAmount = R$ %s%s, want R$ 250,00", data.PendingAmount.Reais, data.PendingAmount.Cents)
	}
	if len(data.InvoicePayments) != 0 {
		t.Fatalf("InvoicePayments len = %d, want 0", len(data.InvoicePayments))
	}
}

func TestFaturaTemplateExpoeFluxoPagamentoParcial(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()

	seedInvoicePaymentScenario(t, db)

	data, err := buildFaturaDataForInvoice(db, "ws-test", "invoice-2026-08", "desc")
	if err != nil {
		t.Fatalf("buildFaturaDataForInvoice: %v", err)
	}

	content, err := os.ReadFile(filepath.Join(projectRoot(), "templates/pages/faturas.html"))
	if err != nil {
		t.Fatalf("read template: %v", err)
	}
	tpl := template.Must(template.New("faturas").Funcs(template.FuncMap{
		"assetPath": func(path string) string { return path },
	}).Parse(string(content)))

	var buf strings.Builder
	if err := tpl.ExecuteTemplate(&buf, "invoice-summary", data); err != nil {
		t.Fatalf("execute invoice-summary: %v", err)
	}
	html := buf.String()
	for _, expected := range []string{
		`type="button" data-open-settle-payment`,
		"Quitar fatura",
		"Confirmar quitação",
		`name="payment_mode" value="settle"`,
		`name="confirm_settle" value="1"`,
		"Competência",
		"Valor a quitar",
		"Conta selecionada",
		"Data de pagamento",
		"Saldo estimado da conta após pagamento",
		"R$ 750,00",
		"Pagamento parcial",
		"Reembolso geral",
		"ui-btn ui-btn--primary cb-invoice-action-btn",
		"ui-btn ui-btn--secondary cb-invoice-action-btn",
		`hx-get="/cartoes/faturas/reembolso?invoice_id=invoice-2026-08"`,
		`name="payment_mode" value="partial"`,
		`name="payment_amount"`,
		`name="payment_date"`,
		"Saldo restante após este pagamento",
		// Canonical summary composition.
		`class="ui-panel cb-invoice-main"`,
		`cb-invoice-hero-value`,
		`cb-invoice-limit-progress`,
		`data-sensitive`,
	} {
		if !strings.Contains(html, expected) {
			t.Fatalf("missing %q in invoice summary html: %s", expected, html)
		}
	}
	for _, legacy := range []string{
		"cb-invoice-action-label",
		"grid-cols-1 gap-2.5 pt-1",
		"font-black",
		"<span>Quitar</span>",
	} {
		if strings.Contains(html, legacy) {
			t.Fatalf("summary kept legacy presentation %q", legacy)
		}
	}
}

func TestInvoiceActionButtonsStayFullWidthRowsAtEveryBreakpoint(t *testing.T) {
	raw, err := os.ReadFile(resolveTemplatePath(t, "assets/css/faturas.css"))
	if err != nil {
		t.Fatalf("read faturas css: %v", err)
	}
	stylesheet := string(raw)
	actionStart := strings.Index(stylesheet, ".cb-invoice-action-btn {")
	if actionStart < 0 {
		t.Fatal("invoice action button styles not found")
	}
	actionStyles := stylesheet[actionStart:]
	for _, expected := range []string{
		"width: 100%",
		"min-height: 42px",
		"justify-content: flex-start",
	} {
		if !strings.Contains(actionStyles[:600], expected) {
			t.Fatalf("invoice actions must remain full-width rows, missing %q", expected)
		}
	}
	if strings.Contains(actionStyles[:600], "flex-direction: column") {
		t.Fatal("invoice actions must remain full-width rows")
	}
	if !strings.Contains(stylesheet, ".cb-invoice-actions-list { display: grid; gap: 8px; }") {
		t.Fatal("invoice actions must stay in a compact stacked list")
	}
	if !strings.Contains(stylesheet, ".cb-invoice-actions-info summary") {
		t.Fatal("invoice action explanations must stay collapsible")
	}
}

func TestInvoiceHubMutationRendersOneAtomicDetailReplacement(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	seedInvoicePaymentScenario(t, db)

	templates := template.Must(template.New("test").Parse(`
{{define "faturas-content"}}<section id="faturas-content" {{if .OOBContent}}hx-swap-oob="outerHTML"{{end}}>R$ {{.Total.Reais}}{{.Total.Cents}}</section>{{end}}
{{define "invoice-summary"}}<section id="invoice-summary" {{if .OOB}}hx-swap-oob="outerHTML"{{end}}></section>{{end}}
{{define "invoice-transactions"}}<section id="invoice-transactions" {{if .OOB}}hx-swap-oob="outerHTML"{{end}}></section>{{end}}
{{define "dashboard-cards"}}<section id="dashboard-cards" {{if .OOB}}hx-swap-oob="outerHTML"{{end}}></section>{{end}}
`))
	h := TransactionHandler{DB: db, Templates: templates, WorkspaceID: "ws-test", UserID: "user-test"}
	req := httptest.NewRequest(http.MethodPost, "/transacoes/purchase-test/salvar", nil)
	req.Header.Set("HX-Current-URL", "http://localhost:8080/faturas?cartao=card-test&mes=8&ano=2026")
	rr := httptest.NewRecorder()
	if err := h.renderInvoiceContextOOB(rr, req, "invoice-2026-08", true); err != nil {
		t.Fatalf("render invoice hub context: %v", err)
	}
	body := rr.Body.String()
	if !strings.Contains(body, `id="faturas-content" hx-swap-oob="outerHTML"`) {
		t.Fatalf("invoice hub response must atomically replace its content: %s", body)
	}
	if strings.Contains(body, `id="invoice-summary" hx-swap-oob="outerHTML"`) || strings.Contains(body, `id="invoice-transactions" hx-swap-oob="outerHTML"`) {
		t.Fatalf("invoice hub response must not emit competing nested OOB swaps: %s", body)
	}
}

func TestFaturaTemplateSemContaCorrenteBloqueiaQuitacao(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()

	seedInvoicePaymentScenario(t, db)
	execTestSQL(t, db, `DELETE FROM accounts WHERE id = 'checking-test'`)

	data, err := buildFaturaDataForInvoice(db, "ws-test", "invoice-2026-08", "desc")
	if err != nil {
		t.Fatalf("buildFaturaDataForInvoice: %v", err)
	}
	if data.CanSubmitPayment {
		t.Fatalf("CanSubmitPayment should be false without payment accounts")
	}
	if data.PaymentDisabledReason != "Nenhuma conta corrente disponível" {
		t.Fatalf("PaymentDisabledReason = %q", data.PaymentDisabledReason)
	}

	content, err := os.ReadFile(filepath.Join(projectRoot(), "templates/pages/faturas.html"))
	if err != nil {
		t.Fatalf("read template: %v", err)
	}
	tpl := template.Must(template.New("faturas").Funcs(template.FuncMap{
		"assetPath": func(path string) string { return path },
	}).Parse(string(content)))

	var buf strings.Builder
	if err := tpl.ExecuteTemplate(&buf, "invoice-summary", data); err != nil {
		t.Fatalf("execute invoice-summary: %v", err)
	}
	html := buf.String()
	for _, expected := range []string{
		"Nenhuma conta corrente disponível",
		`data-open-settle-payment disabled`,
		`name="payment_account_id" required`,
	} {
		if !strings.Contains(html, expected) {
			t.Fatalf("missing %q in invoice summary html: %s", expected, html)
		}
	}
}

func TestBuildFaturaDataComPagamentoMostraTotalPagoEHistorico(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()

	seedInvoicePaymentScenario(t, db)
	now := time.Now().Unix()
	execTestSQL(t, db, `
		INSERT INTO invoice_payments (id, workspace_id, invoice_id, account_id, transaction_id, amount_cents, paid_at, note, source, reversed_at, created_by, created_at)
		VALUES ('ip-test', 'ws-test', 'invoice-2026-08', 'checking-test', NULL, 25000, ?, NULL, 'manual', NULL, 'user-test', ?)
	`, now, now)

	data, err := buildFaturaDataForInvoice(db, "ws-test", "invoice-2026-08", "desc")
	if err != nil {
		t.Fatalf("buildFaturaDataForInvoice: %v", err)
	}
	if !data.HasPayments {
		t.Fatalf("HasPayments should be true")
	}
	if data.TotalPaid.Reais != "250" || data.TotalPaid.Cents != ",00" {
		t.Fatalf("TotalPaid = R$ %s%s, want R$ 250,00", data.TotalPaid.Reais, data.TotalPaid.Cents)
	}
	if data.PendingAmount.Reais != "0" || data.PendingAmount.Cents != ",00" {
		t.Fatalf("PendingAmount = R$ %s%s, want R$ 0,00", data.PendingAmount.Reais, data.PendingAmount.Cents)
	}
	if len(data.InvoicePayments) != 1 {
		t.Fatalf("InvoicePayments len = %d, want 1", len(data.InvoicePayments))
	}
	row := data.InvoicePayments[0]
	if row.Source != "manual" {
		t.Fatalf("payment source = %q, want manual", row.Source)
	}
	if row.AccountName != "Conta Teste" {
		t.Fatalf("payment account name = %q, want Conta Teste", row.AccountName)
	}
	if row.Amount.Reais != "250" || row.Amount.Cents != ",00" {
		t.Fatalf("payment amount = R$ %s%s", row.Amount.Reais, row.Amount.Cents)
	}
	if row.DateLabel == "" || row.DateLabel == "-" {
		t.Fatalf("payment date label = %q, want non-empty", row.DateLabel)
	}
}

func TestBuildFaturaDataPagamentoRevertidoNaoAparece(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()

	seedInvoicePaymentScenario(t, db)
	now := time.Now().Unix()
	execTestSQL(t, db, `
		INSERT INTO invoice_payments (id, workspace_id, invoice_id, account_id, transaction_id, amount_cents, paid_at, note, source, reversed_at, created_by, created_at)
		VALUES ('ip-reversed', 'ws-test', 'invoice-2026-08', 'checking-test', NULL, 25000, ?, NULL, 'manual', ?, 'user-test', ?)
	`, now, now, now)

	data, err := buildFaturaDataForInvoice(db, "ws-test", "invoice-2026-08", "desc")
	if err != nil {
		t.Fatalf("buildFaturaDataForInvoice: %v", err)
	}
	if data.HasPayments {
		t.Fatalf("HasPayments should be false when all payments are reversed")
	}
	if len(data.InvoicePayments) != 0 {
		t.Fatalf("InvoicePayments len = %d, want 0", len(data.InvoicePayments))
	}
}

func TestBuildFaturaDataPagamentoOutroWorkspaceNaoAparece(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()

	seedInvoicePaymentScenario(t, db)
	now := time.Now().Unix()
	execTestSQL(t, db, `
		INSERT INTO workspaces (id, name, description, created_at, updated_at)
		VALUES ('ws-other', 'Other', '', ?, ?)
	`, now, now)
	execTestSQL(t, db, `
		INSERT INTO invoice_payments (id, workspace_id, invoice_id, account_id, transaction_id, amount_cents, paid_at, note, source, reversed_at, created_by, created_at)
		VALUES ('ip-other', 'ws-other', 'invoice-2026-08', 'checking-test', NULL, 25000, ?, NULL, 'manual', NULL, 'user-test', ?)
	`, now, now)

	data, err := buildFaturaDataForInvoice(db, "ws-test", "invoice-2026-08", "desc")
	if err != nil {
		t.Fatalf("buildFaturaDataForInvoice: %v", err)
	}
	if data.HasPayments {
		t.Fatalf("HasPayments should be false for cross-workspace payments")
	}
}

func TestHandlePagarFaturaRenderContemSecaoPagamentos(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()

	seedInvoicePaymentScenario(t, db)

	templates := template.Must(template.New("test").Parse(`
{{define "faturas-content"}}<main id="faturas-content">{{if .HasLegacyPaymentSummary}}legacy{{else if .HasPayments}}has-payments{{else}}empty{{end}}</main>{{end}}
{{define "dashboard-balance"}}<section id="dashboard-balance" {{if .OOB}}hx-swap-oob="outerHTML"{{end}}></section>{{end}}
{{define "dashboard-accounts"}}<section id="dashboard-accounts" {{if .OOB}}hx-swap-oob="outerHTML"{{end}}></section>{{end}}
{{define "dashboard-cards"}}<section id="dashboard-cards" {{if .OOB}}hx-swap-oob="outerHTML"{{end}}></section>{{end}}
{{define "invoice-summary"}}<section id="invoice-summary" {{if .OOB}}hx-swap-oob="outerHTML"{{end}}></section>{{end}}
{{define "invoice-transactions"}}<section id="invoice-transactions" {{if .OOB}}hx-swap-oob="outerHTML"{{end}}></section>{{end}}
`))

	handler := FaturasHandler{
		DB:          db,
		WriterDB:    db,
		Templates:   templates,
		WorkspaceID: "ws-test",
		UserID:      "user-test",
	}

	form := url.Values{}
	form.Set("invoice_id", "invoice-2026-08")
	form.Set("payment_account_id", "checking-test")
	form.Set("payment_mode", "settle")
	form.Set("confirm_settle", "1")
	req := httptest.NewRequest(http.MethodPost, "/cartoes/faturas/pagar", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rr := httptest.NewRecorder()

	handler.HandlePagarFatura(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rr.Code)
	}
	if !strings.Contains(rr.Body.String(), `hx-swap-oob="outerHTML"`) {
		t.Fatalf("expected OOB dashboard fragments, got %q", rr.Body.String())
	}

	var ipCount int
	if err := db.QueryRow(`
		SELECT COUNT(1) FROM invoice_payments WHERE invoice_id = ?
	`, "invoice-2026-08").Scan(&ipCount); err != nil {
		t.Fatalf("invoice_payments count: %v", err)
	}
	if ipCount != 1 {
		t.Fatalf("invoice_payments count = %d, want 1", ipCount)
	}
}

func TestBuildFaturaDataPaidLegacySemInvoicePaymentsUsaFallback(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()

	seedInvoicePaymentScenario(t, db)
	now := time.Now().Unix()
	execTestSQL(t, db, `
		UPDATE invoices
		SET status = 'PAID', paid_at = ?, paid_amount = 25000
		WHERE id = 'invoice-2026-08'
	`, now)

	data, err := buildFaturaDataForInvoice(db, "ws-test", "invoice-2026-08", "desc")
	if err != nil {
		t.Fatalf("buildFaturaDataForInvoice: %v", err)
	}
	if data.HasPayments {
		t.Fatalf("HasPayments should be false (no invoice_payments)")
	}
	if !data.HasLegacyPaymentSummary {
		t.Fatalf("HasLegacyPaymentSummary should be true")
	}
	if data.LegacyPaymentNotice == "" {
		t.Fatalf("LegacyPaymentNotice should not be empty")
	}
	if data.TotalPaid.Reais != "250" || data.TotalPaid.Cents != ",00" {
		t.Fatalf("TotalPaid = R$ %s%s, want R$ 250,00", data.TotalPaid.Reais, data.TotalPaid.Cents)
	}
	if data.PendingAmount.Reais != "0" || data.PendingAmount.Cents != ",00" {
		t.Fatalf("PendingAmount = R$ %s%s, want R$ 0,00", data.PendingAmount.Reais, data.PendingAmount.Cents)
	}
	if len(data.InvoicePayments) != 0 {
		t.Fatalf("InvoicePayments len = %d, want 0", len(data.InvoicePayments))
	}
}

func TestBuildFaturaDataPaidComInvoicePaymentsNaoUsaFallback(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()

	seedInvoicePaymentScenario(t, db)
	now := time.Now().Unix()
	execTestSQL(t, db, `
		UPDATE invoices
		SET status = 'PAID', paid_at = ?, paid_amount = 25000
		WHERE id = 'invoice-2026-08'
	`, now)
	execTestSQL(t, db, `
		INSERT INTO invoice_payments (id, workspace_id, invoice_id, account_id, transaction_id, amount_cents, paid_at, note, source, reversed_at, created_by, created_at)
		VALUES ('ip-test', 'ws-test', 'invoice-2026-08', 'checking-test', NULL, 25000, ?, NULL, 'manual', NULL, 'user-test', ?)
	`, now, now)

	data, err := buildFaturaDataForInvoice(db, "ws-test", "invoice-2026-08", "desc")
	if err != nil {
		t.Fatalf("buildFaturaDataForInvoice: %v", err)
	}
	if !data.HasPayments {
		t.Fatalf("HasPayments should be true (has real invoice_payment)")
	}
	if data.HasLegacyPaymentSummary {
		t.Fatalf("HasLegacyPaymentSummary should be false when real invoice_payments exist")
	}
}

func TestBuildFaturaDataPaidZeroSemPaymentsMostraFallbackZero(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()

	seedInvoicePaymentScenario(t, db)
	execTestSQL(t, db, `DELETE FROM transactions WHERE invoice_id = 'invoice-2026-08'`)
	now := time.Now().Unix()
	execTestSQL(t, db, `
		UPDATE invoices
		SET status = 'PAID', paid_at = ?, paid_amount = 0
		WHERE id = 'invoice-2026-08'
	`, now)

	data, err := buildFaturaDataForInvoice(db, "ws-test", "invoice-2026-08", "desc")
	if err != nil {
		t.Fatalf("buildFaturaDataForInvoice: %v", err)
	}
	if data.HasLegacyPaymentSummary {
		t.Fatalf("HasLegacyPaymentSummary should be false when paid_amount = 0")
	}
	if data.TotalPaid.Reais != "0" || data.TotalPaid.Cents != ",00" {
		t.Fatalf("TotalPaid = R$ %s%s, want R$ 0,00", data.TotalPaid.Reais, data.TotalPaid.Cents)
	}
}

func TestBuildFaturaDataCanonicalEffectivePaidMatrix(t *testing.T) {
	tests := []struct {
		name              string
		status            string
		legacyPaid        int64
		modernPaid        int64
		modernReversed    bool
		wantPaidReais     string
		wantPendingReais  string
		wantLegacySummary bool
	}{
		{"legacy partial open", "OPEN", 10000, 0, false, "100", "150", true},
		{"legacy partial closed", "CLOSED", 10000, 0, false, "100", "150", true},
		{"modern partial overrides legacy", "OPEN", 25000, 10000, false, "100", "150", false},
		{"all reversed does not resurrect legacy", "CLOSED", 25000, 25000, true, "0", "250", false},
		{"overpayment is not clamped", "OPEN", 25000, 30000, false, "300", "0", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			db := openTestDB(t)
			defer db.Close()
			seedInvoicePaymentScenario(t, db)

			futureClosing := testUnixDate("2099-08-20")
			futureDue := testUnixDate("2099-09-10")
			execTestSQL(t, db, `
				UPDATE invoices
				SET status = ?, closing_date = ?, due_date = ?, paid_at = NULL, paid_amount = ?
				WHERE id = 'invoice-2026-08'
			`, tt.status, futureClosing, futureDue, tt.legacyPaid)
			if tt.modernPaid > 0 {
				now := time.Now().Unix()
				var reversedAt any
				if tt.modernReversed {
					reversedAt = now
				}
				execTestSQL(t, db, `
					INSERT INTO invoice_payments (id, workspace_id, invoice_id, account_id, amount_cents, paid_at, source, reversed_at, created_by, created_at)
					VALUES ('ip-effective-matrix', 'ws-test', 'invoice-2026-08', 'checking-test', ?, ?, 'manual', ?, 'user-test', ?)
				`, tt.modernPaid, now, reversedAt, now)
			}

			data, err := buildFaturaDataForInvoice(db, "ws-test", "invoice-2026-08", "desc")
			if err != nil {
				t.Fatalf("buildFaturaDataForInvoice: %v", err)
			}
			if data.Status != tt.status {
				t.Fatalf("status = %q, want persisted %q", data.Status, tt.status)
			}
			if data.TotalPaid.Reais != tt.wantPaidReais || data.TotalPaid.Cents != ",00" {
				t.Fatalf("TotalPaid = R$ %s%s, want R$ %s,00", data.TotalPaid.Reais, data.TotalPaid.Cents, tt.wantPaidReais)
			}
			if data.PendingAmount.Reais != tt.wantPendingReais || data.PendingAmount.Cents != ",00" {
				t.Fatalf("PendingAmount = R$ %s%s, want R$ %s,00", data.PendingAmount.Reais, data.PendingAmount.Cents, tt.wantPendingReais)
			}
			if data.HasLegacyPaymentSummary != tt.wantLegacySummary {
				t.Fatalf("HasLegacyPaymentSummary = %v, want %v", data.HasLegacyPaymentSummary, tt.wantLegacySummary)
			}
			if tt.modernReversed && data.HasPayments {
				t.Fatal("all-reversed lineage must not appear as an active payment list")
			}
		})
	}
}

func execTestSQL(t *testing.T, db *sql.DB, query string, args ...interface{}) {
	t.Helper()
	if _, err := fixtureHandlerExec(t, db, query, args...); err != nil {
		t.Fatalf("exec test sql: %v\nquery: %s", err, query)
	}
}
