package handlers

import (
	"math"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

func TestParseCurrency(t *testing.T) {
	cases := []struct {
		in   string
		want int64
	}{
		{"R$ 54,20", 5420},
		{"54,20", 5420},
		{"1.234,56", 123456},
		{"1234.56", 123456},
		{"1,234.56", 123456},
		{"R$1.234,56", 123456},
		{"R$ 1.800,00", 180000},
		{"R$ 1.234.567,89", 123456789},
		{"  260000,00  ", 26000000},
		{"100", 10000},
		{"100,5", 10050},
		{"0,99", 99},
	}

	for _, tc := range cases {
		got, err := parseCurrency(tc.in)
		if err != nil {
			t.Fatalf("parseCurrency(%q): %v", tc.in, err)
		}
		if got != tc.want {
			t.Fatalf("parseCurrency(%q) = %d, want %d", tc.in, got, tc.want)
		}
	}
}

func TestParseCurrencyRejectsLetters(t *testing.T) {
	for _, raw := range []string{"abc100", "100 reais", "R$ cem"} {
		if _, err := parseCurrency(raw); err == nil {
			t.Fatalf("parseCurrency(%q) accepted letters", raw)
		}
	}
}

func TestParseCurrencyInt64Boundaries(t *testing.T) {
	for _, raw := range []string{"92233720368547758.07", "92.233.720.368.547.758,07", "92,233,720,368,547,758.07"} {
		got, err := parseCurrency(raw)
		if err != nil || got != math.MaxInt64 {
			t.Errorf("parseCurrency(%q) = %d, %v; want MaxInt64", raw, got, err)
		}
	}
	for _, raw := range []string{
		"92233720368547758.08", "92233720368547759", "184467440737095517.00",
		"184.467.440.737.095.517,00", "9223372036854775807.99",
		"-92233720368547758.08", "-184467440737095517.00",
		"--184467440737095517.00", "--9223372036854775808.00",
	} {
		if got, err := parseCurrency(raw); err == nil {
			t.Errorf("parseCurrency(%q) accepted out-of-range value as %d cents", raw, got)
		}
	}
}

func TestTransactionCreateRejectsCurrencyOverflowWithoutWrites(t *testing.T) {
	for _, kind := range []string{"personal", "business"} {
		t.Run(kind, func(t *testing.T) {
			db := openTestDB(t)
			defer db.Close()
			seedInvoicePaymentScenario(t, db)
			execTestSQL(t, db, `UPDATE workspaces SET type = ? WHERE id = 'ws-test'`, kind)
			execTestSQL(t, db, `INSERT INTO categories (id, workspace_id, name, type) VALUES ('overflow-category', 'ws-test', 'Overflow', 'EXPENSE')`)
			var beforeCount, beforeBalance int64
			if err := db.QueryRow(`SELECT COUNT(*) FROM transactions`).Scan(&beforeCount); err != nil {
				t.Fatal(err)
			}
			if err := db.QueryRow(`SELECT current_balance FROM accounts WHERE id = 'checking-test'`).Scan(&beforeBalance); err != nil {
				t.Fatal(err)
			}
			h := testPaidInvoiceMutationHandler(db)
			rr := httptest.NewRecorder()
			h.HandleSalvarTransacao(rr, formPost("/transacoes", url.Values{
				"tipo": {"despesa"}, "valor": {"184467440737095517.00"},
				"descricao": {"Overflow"}, "data": {"2026-07-12"},
				"origem_conta_id": {"checking-test"}, "status_pagamento": {"paid"},
				"categoria_id": {"overflow-category"},
			}))
			if rr.Code != http.StatusOK || !strings.Contains(rr.Header().Get("HX-Trigger"), `"field": "valor"`) {
				t.Fatalf("expected existing value-field error, status=%d trigger=%s", rr.Code, rr.Header().Get("HX-Trigger"))
			}
			var afterCount, afterBalance int64
			if err := db.QueryRow(`SELECT COUNT(*) FROM transactions`).Scan(&afterCount); err != nil {
				t.Fatal(err)
			}
			if err := db.QueryRow(`SELECT current_balance FROM accounts WHERE id = 'checking-test'`).Scan(&afterBalance); err != nil {
				t.Fatal(err)
			}
			if beforeCount != afterCount || beforeBalance != afterBalance {
				t.Fatalf("overflow wrote data: count %d -> %d; balance %d -> %d", beforeCount, afterCount, beforeBalance, afterBalance)
			}
		})
	}
}
