package handlers

import (
	"math"
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
	"strings"
	"testing"
)

func TestParseAccountBalance(t *testing.T) {
	for _, tc := range []struct {
		raw  string
		want int64
	}{
		{"0", 0}, {"0,00", 0}, {"-0,00", 0}, {"-0,01", -1},
		{"-R$ 1.234,56", -123456}, {"R$ -1.234,56", -123456},
		{"1,234.56", 123456}, {"1.234", 123400}, {"10,5", 1050},
		{"92.233.720.368.547.758,07", math.MaxInt64},
		{"-92.233.720.368.547.758,08", math.MinInt64},
	} {
		got, err := parseAccountBalance(tc.raw)
		if err != nil || got != tc.want {
			t.Errorf("parse %q = %d/%v, want %d", tc.raw, got, err, tc.want)
		}
	}
	for _, raw := range []string{"", "abc0", "R$", "1,2,3", "1.23,45", "1,2345", "0,123", "-0.001", "--1", "-R$ -1", "NaN", "1e2", "92233720368547758.08", "-92233720368547758.09", "184467440737095517.00"} {
		if got, err := parseAccountBalance(raw); err == nil {
			t.Errorf("accepted %q as %d", raw, got)
		}
	}
}

func TestAccountBalanceFormattingRoundTripsSignedBoundaries(t *testing.T) {
	for _, value := range []int64{math.MinInt64, math.MaxInt64, -1, 0, 1, -100, 100} {
		for _, formatted := range []string{centsToInput(value), formatCurrencyLabel(value)} {
			got, err := parseAccountBalance(formatted)
			if err != nil || got != value {
				t.Fatalf("format %d as %q round trips to %d/%v", value, formatted, got, err)
			}
		}
	}
}

// Every string the signed money mask can produce for a value must come back as
// the same centavos; the mask never introduces a format the parser rejects.
func TestReconciliationMaskOutputParsesToTheSameCents(t *testing.T) {
	for _, tc := range []struct {
		masked string
		want   int64
	}{
		{"R$ 0,00", 0}, {"R$ 0,01", 1}, {"R$ 0,10", 10}, {"R$ 1,00", 100}, {"R$ 1,01", 101},
		{"R$ 1.234,56", 123456}, {"R$ 139.931,00", 13993100}, {"-R$ 0,00", 0}, {"-R$ 0,01", -1},
		{"-R$ 1.234,56", -123456}, {"R$ 1.000.000.000,00", 100000000000},
		{"R$ 92.233.720.368.547.758,07", math.MaxInt64},
		{"-R$ 92.233.720.368.547.758,08", math.MinInt64},
	} {
		got, err := parseAccountBalance(tc.masked)
		if err != nil || got != tc.want {
			t.Errorf("masked %q = %d/%v, want %d", tc.masked, got, err, tc.want)
		}
	}
}

func TestAccountAdjustmentHandlerAppliesMaskedTargetsExactly(t *testing.T) {
	for _, tc := range []struct {
		name        string
		balance     int64
		submitted   string
		wantBalance int64
	}{
		{"credit", 139931, "R$ 1.500,00", 150000},
		{"negative cent", 0, "-R$ 0,01", -1},
		{"debit one cent", 100, "R$ 0,99", 99},
		{"zero target", 150000, "R$ 0,00", 0},
		{"sign of current balance", 100, "-R$ 1.234,56", -123456},
		{"int64 max", 0, "R$ 92.233.720.368.547.758,07", math.MaxInt64},
		{"int64 min", 0, "-R$ 92.233.720.368.547.758,08", math.MinInt64},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := newConfiguracoesRenderTestHandler(t)
			execTestSQL(t, h.DB, `INSERT INTO accounts (id,workspace_id,name,type,initial_balance,current_balance)
				VALUES ('masked',?,'Account','CHECKING',?,?)`, h.WorkspaceID, tc.balance, tc.balance)
			get := httptest.NewRecorder()
			h.HandleContaBalanceAdjustmentForm(get, httptest.NewRequest(http.MethodGet, "/configuracoes/contas/masked/conciliar", nil), "masked")
			if get.Code != http.StatusOK {
				t.Fatalf("form status=%d", get.Code)
			}
			values := adjustmentFormValues(t, get.Body.String())
			values.Set("target_balance", tc.submitted)
			values.Set("reason", "Conferência com o extrato")
			post := httptest.NewRecorder()
			h.HandleContaBalanceAdjustment(post, formPost("/configuracoes/contas/masked/conciliar", values), "masked")
			if post.Code != http.StatusOK || !strings.Contains(post.Body.String(), "Conciliação registrada") {
				t.Fatalf("post failed: %d %s", post.Code, post.Body.String())
			}
			var current, initial int64
			if err := h.DB.QueryRow(`SELECT current_balance,initial_balance FROM accounts WHERE id='masked'`).Scan(&current, &initial); err != nil {
				t.Fatal(err)
			}
			if current != tc.wantBalance || initial != tc.balance {
				t.Fatalf("balances=%d/%d, want %d/%d", current, initial, tc.wantBalance, tc.balance)
			}
			var before, after, delta int64
			if err := h.DB.QueryRow(`SELECT before_balance,after_balance,delta_cents FROM account_balance_adjustments WHERE account_id='masked'`).Scan(&before, &after, &delta); err != nil {
				t.Fatal(err)
			}
			if before != tc.balance || after != tc.wantBalance || delta != tc.wantBalance-tc.balance {
				t.Fatalf("adjustment=%d→%d delta %d, want %d→%d delta %d", before, after, delta, tc.balance, tc.wantBalance, tc.wantBalance-tc.balance)
			}
			// The re-rendered form keeps the new balance in the canonical format the
			// mask and the parser both accept, so a second round trip cannot drift.
			if want := `value="` + formatCurrencyLabel(tc.wantBalance) + `"`; !strings.Contains(post.Body.String(), want) {
				t.Fatalf("form did not re-render %s: %s", want, post.Body.String())
			}
		})
	}
}

// Shared separator samples with the signed money mask (assets/js/money-mask.test.js,
// "frontend and backend share the same separator frontier"). Every origin that is
// not a keystroke is validated against these shapes on the frontend, so the
// accepted/rejected verdicts must match this parser.
func TestReconciliationSeparatorContractSamples(t *testing.T) {
	for _, tc := range []struct {
		raw  string
		want int64
	}{
		{"1.234,56", 123456}, {"12.345,67", 1234567}, {"123.456,78", 12345678},
		{"1.234.567,89", 123456789}, {"1234,56", 123456}, {"123456", 12345600},
		{"0,01", 1}, {"1.234", 123400}, {"1,234.56", 123456},
	} {
		got, err := parseAccountBalance(tc.raw)
		if err != nil || got != tc.want {
			t.Errorf("parse %q = %d/%v, want %d", tc.raw, got, err, tc.want)
		}
	}
	for _, raw := range []string{"1.23,45", "1.2.3", "1..234", "12.34,56", "123.45,67", "1.2345", "1.2345,67", ".123", "123.", "abc0", "1e2"} {
		if got, err := parseAccountBalance(raw); err == nil {
			t.Errorf("accepted %q as %d", raw, got)
		}
	}
	// Documented divergence: the parser reads a lone dot as the decimal separator
	// (so 1.23 is 123 centavos), while the signed mask only accepts a dot as a
	// thousands group or in the US grouping shape, so these never reach the server
	// as masked values.
	for _, raw := range []string{"1.23", "12.34", "1234.56"} {
		got, err := parseAccountBalance(raw)
		if err != nil {
			t.Fatalf("parser stopped accepting %q: %v", raw, err)
		}
		t.Logf("parser reads %q as %d centavos; the signed mask rejects it as a malformed thousands group", raw, got)
	}
}

func TestAccountAdjustmentHandlerRejectsInvalidTargetsWithoutMask(t *testing.T) {
	// The signed mask flags these before submit; the server must reject the same
	// text when it arrives from a manual or scripted request.
	for _, submitted := range []string{"", "-", ",", "abc", "abc0", "1e2", "1,2,3", "1..000", "R$ R$ 10,00", "1.234,567", "92233720368547758.08", "184467440737095517.00"} {
		t.Run(submitted, func(t *testing.T) {
			h := newConfiguracoesRenderTestHandler(t)
			execTestSQL(t, h.DB, `INSERT INTO accounts (id,workspace_id,name,type,initial_balance,current_balance)
				VALUES ('invalid',?,'Account','CHECKING',25000,25000)`, h.WorkspaceID)
			get := httptest.NewRecorder()
			h.HandleContaBalanceAdjustmentForm(get, httptest.NewRequest(http.MethodGet, "/configuracoes/contas/invalid/conciliar", nil), "invalid")
			values := adjustmentFormValues(t, get.Body.String())
			values.Set("target_balance", submitted)
			values.Set("reason", "Conferência")
			post := httptest.NewRecorder()
			h.HandleContaBalanceAdjustment(post, formPost("/configuracoes/contas/invalid/conciliar", values), "invalid")
			if post.Code != http.StatusOK || !strings.Contains(post.Body.String(), "Saldo inválido") {
				t.Fatalf("invalid %q was not rejected: %d %s", submitted, post.Code, post.Body.String())
			}
			var current int64
			var adjustments int
			if err := h.DB.QueryRow(`SELECT current_balance FROM accounts WHERE id='invalid'`).Scan(&current); err != nil {
				t.Fatal(err)
			}
			if err := h.DB.QueryRow(`SELECT COUNT(*) FROM account_balance_adjustments`).Scan(&adjustments); err != nil {
				t.Fatal(err)
			}
			if current != 25000 || adjustments != 0 {
				t.Fatalf("invalid %q changed state: balance=%d adjustments=%d", submitted, current, adjustments)
			}
		})
	}
}

func adjustmentFormValues(t *testing.T, body string) url.Values {
	t.Helper()
	form := url.Values{}
	for _, name := range []string{"request_id", "balance_version"} {
		match := regexp.MustCompile(`name="` + name + `" value="([^"]+)"`).FindStringSubmatch(body)
		if len(match) != 2 {
			t.Fatalf("missing %s in form", name)
		}
		form.Set(name, match[1])
	}
	return form
}

func TestAccountAdjustmentHandlerHistoryAndReports(t *testing.T) {
	for _, kind := range []string{"personal", "business"} {
		for _, target := range []string{"-0,01", "0,00"} {
			t.Run(kind+"/"+target, func(t *testing.T) {
				h := newConfiguracoesRenderTestHandler(t)
				execTestSQL(t, h.DB, `UPDATE workspaces SET type=? WHERE id=?`, kind, h.WorkspaceID)
				execTestSQL(t, h.DB, `INSERT INTO accounts (id,workspace_id,name,type,initial_balance,current_balance) VALUES ('reconcile',?,'Account','CHECKING',10000,10000)`, h.WorkspaceID)
				get := httptest.NewRecorder()
				h.HandleContaBalanceAdjustmentForm(get, httptest.NewRequest(http.MethodGet, "/configuracoes/contas/reconcile/conciliar", nil), "reconcile")
				if get.Code != http.StatusOK || get.Header().Get("Cache-Control") != "no-store" {
					t.Fatalf("form status=%d", get.Code)
				}
				if !strings.Contains(get.Body.String(), `data-money-mask="signed"`) {
					t.Fatalf("balance form must bind the signed, integer-exact mask: %s", get.Body.String())
				}
				if !strings.Contains(get.Body.String(), `inputmode="decimal"`) {
					t.Fatalf("balance form must request a numeric keyboard: %s", get.Body.String())
				}
				if want := `value="` + formatCurrencyLabel(10000) + `"`; !strings.Contains(get.Body.String(), want) {
					t.Fatalf("balance form must pre-fill the canonical currency string %s: %s", want, get.Body.String())
				}
				values := adjustmentFormValues(t, get.Body.String())
				values.Set("target_balance", target)
				values.Set("reason", `<script>alert('x')</script>`)
				values.Set("workspace_id", "config-secondary")
				for i := 0; i < 2; i++ {
					post := httptest.NewRecorder()
					h.HandleContaBalanceAdjustment(post, formPost("/configuracoes/contas/reconcile/conciliar", values), "reconcile")
					if post.Code != http.StatusOK || !strings.Contains(post.Body.String(), "Conciliação registrada") {
						t.Fatalf("post failed: %d %s", post.Code, post.Body.String())
					}
					if strings.Contains(post.Body.String(), "<script>alert") || !strings.Contains(post.Body.String(), "&lt;script&gt;") {
						t.Fatal("history reason not escaped")
					}
				}
				var count, current, initial int64
				if err := h.DB.QueryRow(`SELECT current_balance,initial_balance FROM accounts WHERE id='reconcile'`).Scan(&current, &initial); err != nil {
					t.Fatal(err)
				}
				want, _ := parseAccountBalance(target)
				if current != want || initial != 10000 {
					t.Fatalf("balances=%d/%d", current, initial)
				}
				if err := h.DB.QueryRow(`SELECT COUNT(*) FROM account_balance_adjustments WHERE workspace_id=?`, h.WorkspaceID).Scan(&count); err != nil {
					t.Fatal(err)
				}
				if count != 1 {
					t.Fatalf("retries created %d history rows", count)
				}
				report := RelatoriosHandler{ActorRole: testMetasAdminRole, DB: h.DB, WorkspaceID: h.WorkspaceID, UserID: h.UserID}
				data, err := report.buildRelatoriosData("", "caixa", "movimentos", 9, 2026)
				if err != nil {
					t.Fatal(err)
				}
				if data.TotalReceitas.rawCents() != 0 || data.TotalDespesas.rawCents() != 0 {
					t.Fatal("reconciliation leaked into income/expenses")
				}
			})
		}
	}
}

func TestAccountAdjustmentHandlerConflictAndForeignAccount(t *testing.T) {
	h := newConfiguracoesRenderTestHandler(t)
	execTestSQL(t, h.DB, `INSERT INTO accounts (id,workspace_id,name,type,current_balance) VALUES ('reconcile',?,'Account','CHECKING',10000),('foreign','config-secondary','Foreign Secret','CHECKING',90000)`, h.WorkspaceID)
	get := httptest.NewRecorder()
	h.HandleContaBalanceAdjustmentForm(get, httptest.NewRequest(http.MethodGet, "/conciliar", nil), "reconcile")
	values := adjustmentFormValues(t, get.Body.String())
	values.Set("target_balance", "150,00")
	values.Set("reason", "Conferência")
	execTestSQL(t, h.DB, `UPDATE accounts SET current_balance=current_balance-2500 WHERE id='reconcile'`)
	post := httptest.NewRecorder()
	h.HandleContaBalanceAdjustment(post, formPost("/conciliar", values), "reconcile")
	if !strings.Contains(post.Body.String(), "O saldo mudou") || !strings.Contains(post.Body.String(), "R$ 75,00") {
		t.Fatalf("missing stale form explanation: %s", post.Body.String())
	}
	for _, method := range []string{http.MethodGet, http.MethodPost} {
		rr := httptest.NewRecorder()
		if method == http.MethodGet {
			h.HandleContaBalanceAdjustmentForm(rr, httptest.NewRequest(method, "/conciliar", nil), "foreign")
		} else {
			h.HandleContaBalanceAdjustment(rr, formPost("/conciliar", values), "foreign")
		}
		if rr.Code != http.StatusNotFound || strings.Contains(rr.Body.String(), "Foreign Secret") {
			t.Fatalf("foreign %s: %d %s", method, rr.Code, rr.Body.String())
		}
	}
	var count int
	if err := h.DB.QueryRow(`SELECT COUNT(*) FROM account_balance_adjustments`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatal("rejected requests created history")
	}
}

func TestAccountCreationPreservesSignedBalanceAndRejectsInvalidInput(t *testing.T) {
	for _, kind := range []string{"personal", "business"} {
		for _, tc := range []struct {
			raw     string
			want    int64
			invalid bool
		}{{"", 0, false}, {"0,00", 0, false}, {"-0,01", -1, false}, {"100,00", 10000, false}, {"abc", 0, true}, {"92233720368547758.08", 0, true}} {
			t.Run(kind+"/"+tc.raw, func(t *testing.T) {
				h := newConfiguracoesRenderTestHandler(t)
				execTestSQL(t, h.DB, `UPDATE workspaces SET type=? WHERE id=?`, kind, h.WorkspaceID)
				rr := httptest.NewRecorder()
				h.HandleContasCreate(rr, formPost("/configuracoes/contas", url.Values{
					"provider_slug": {"custom"}, "provider_custom_name": {"Test Account"}, "type": {"CHECKING"}, "initial_balance": {tc.raw},
				}))
				var count int
				if err := h.DB.QueryRow(`SELECT COUNT(*) FROM accounts WHERE workspace_id=?`, h.WorkspaceID).Scan(&count); err != nil {
					t.Fatal(err)
				}
				if tc.invalid {
					if count != 0 || !strings.Contains(rr.Body.String(), "Saldo inicial inválido") {
						t.Fatalf("invalid initial balance persisted: count=%d body=%s", count, rr.Body.String())
					}
					return
				}
				var initial, current int64
				if err := h.DB.QueryRow(`SELECT initial_balance,current_balance FROM accounts WHERE workspace_id=?`, h.WorkspaceID).Scan(&initial, &current); err != nil {
					t.Fatal(err)
				}
				if count != 1 || initial != tc.want || current != tc.want {
					t.Fatalf("balances %d/%d count=%d, want %d", initial, current, count, tc.want)
				}
			})
		}
	}
}

func TestAccountMetadataPreservesConcurrentBalanceAndIgnoresLegacyInput(t *testing.T) {
	h := newConfiguracoesRenderTestHandler(t)
	execTestSQL(t, h.DB, `INSERT INTO accounts (id,workspace_id,name,type,initial_balance,current_balance) VALUES ('meta-account',?,'Original','CHECKING',100000,100000)`, h.WorkspaceID)
	form := httptest.NewRecorder()
	h.HandleContasInlineForm(form, httptest.NewRequest(http.MethodGet, "/configuracoes/contas/meta-account/formulario", nil), "meta-account")
	if form.Code != http.StatusOK || strings.Contains(form.Body.String(), `name="balance"`) {
		t.Fatalf("metadata form exposes balance write: %s", form.Body.String())
	}
	// A movement occurs after GET, before submitting the stale form.
	execTestSQL(t, h.DB, `UPDATE accounts SET current_balance=current_balance-25000 WHERE id='meta-account'`)
	for _, stale := range []string{"1.000,00", "invalid", "92233720368547758.08"} {
		rr := httptest.NewRecorder()
		h.HandleContasInlineSave(rr, formPost("/configuracoes/contas/meta-account/salvar", url.Values{"name": {"Renamed"}, "type": {"CHECKING"}, "color": {"#123456"}, "balance": {stale}}), "meta-account")
		if rr.Code != http.StatusOK {
			t.Fatalf("metadata status=%d", rr.Code)
		}
		var balance, initial int64
		var name, color string
		if err := h.DB.QueryRow(`SELECT current_balance,initial_balance,name,color FROM accounts WHERE id='meta-account'`).Scan(&balance, &initial, &name, &color); err != nil {
			t.Fatal(err)
		}
		if balance != 75000 || initial != 100000 || name != "Renamed" || color != "#123456" {
			t.Fatalf("unexpected state: %d/%d/%s/%s", balance, initial, name, color)
		}
	}
}
