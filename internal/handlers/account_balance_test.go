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
				if strings.Contains(get.Body.String(), "data-money-mask") {
					t.Fatal("balance form must not strip signs or convert via float")
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
				report := RelatoriosHandler{DB: h.DB, WorkspaceID: h.WorkspaceID, UserID: h.UserID}
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
