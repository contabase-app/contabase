package handlers

import (
	"errors"
	"math"
	"testing"

	"github.com/contabase-app/contabase/internal/services"
	"github.com/google/uuid"
)

func TestInvestimentosI11ExactReaisParsing(t *testing.T) {
	h := &InvestimentosHandler{WorkspaceID: "ws-a", UserID: "user-a"}
	for _, tc := range []struct {
		amount string
		cents  int64
	}{
		{"300,00", 30000}, {"300", 30000}, {"300,0", 30000},
		{"0,01", 1}, {"1.234,56", 123456}, {"1.234", 123400}, {"1.234,5", 123450},
		{"1.234.567,89", 123456789}, {" 300,00 ", 30000},
		{"92233720368547758,07", math.MaxInt64}, {"92.233.720.368.547.758,07", math.MaxInt64},
	} {
		t.Run(tc.amount, func(t *testing.T) {
			form := investmentFormValues()
			form["principal_amount"] = []string{tc.amount}
			form["request_id"] = []string{uuid.NewString()}
			input, err := h.parseInvestmentCreateForm(formRequest("/investimentos", form))
			if err != nil || input.PrincipalCents != tc.cents {
				t.Fatalf("%s: cents=%d expected=%d err=%v", tc.amount, input.PrincipalCents, tc.cents, err)
			}
		})
	}
	for _, amount := range []string{"", "300,001", "300.00", "1,234", "12,3456", "abc", "0", "0,00", "-300,00", "1e3", "NaN", "30,0,0", "1.23,45", "1,234.56", "300,", ",01", "92233720368547758,08", "92.233.720.368.547.758,08", "92233720368547759", "18446744073709551616,00"} {
		t.Run("invalid/"+amount, func(t *testing.T) {
			form := investmentFormValues()
			form["principal_amount"] = []string{amount}
			if _, err := h.parseInvestmentCreateForm(formRequest("/investimentos", form)); !errors.Is(err, services.ErrInvestmentValidation) {
				t.Fatalf("%q must fail validation, got %v", amount, err)
			}
		})
	}
}

func TestInvestimentosI12VisibleAmountsExactAndLegacyFallback(t *testing.T) {
	for _, fields := range []struct{ reais, cents string }{{"amount", "amount_cents"}, {"final_amount", "final_amount_cents"}} {
		for _, tc := range []struct {
			raw   string
			cents int64
		}{
			{"3000", 300000}, {"300,00", 30000}, {"1.234,56", 123456}, {"R$ 1.234,56", 123456}, {"R$ 0,00", 0},
			{"92.233.720.368.547.758,07", math.MaxInt64},
		} {
			form := investmentFormValues()
			form[fields.reais] = []string{tc.raw}
			form[fields.cents] = []string{"999"}
			req := formRequest("/", form)
			if err := req.ParseForm(); err != nil {
				t.Fatal(err)
			}
			got, err := investmentOperationAmount(req, fields.reais, fields.cents, "valor")
			if err != nil || got != tc.cents {
				t.Fatalf("%s %q got %d err %v", fields.reais, tc.raw, got, err)
			}
		}
		for _, raw := range []string{"", "300,001", "300.00", "1,234.56", "1.23,45", "1.23,", "-1", "92233720368547758,08"} {
			form := investmentFormValues()
			form[fields.reais] = []string{raw}
			form[fields.cents] = []string{"999"}
			req := formRequest("/", form)
			if err := req.ParseForm(); err != nil {
				t.Fatal(err)
			}
			if _, err := investmentOperationAmount(req, fields.reais, fields.cents, "valor"); !errors.Is(err, services.ErrInvestmentValidation) {
				t.Fatalf("invalid %q err %v", raw, err)
			}
		}
		form := investmentFormValues()
		form[fields.cents] = []string{"123456"}
		req := formRequest("/", form)
		if err := req.ParseForm(); err != nil {
			t.Fatal(err)
		}
		if got, err := investmentOperationAmount(req, fields.reais, fields.cents, "valor"); err != nil || got != 123456 {
			t.Fatalf("legacy %d %v", got, err)
		}
	}
}
