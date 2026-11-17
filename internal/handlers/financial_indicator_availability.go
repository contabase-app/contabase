package handlers

import "github.com/contabase-app/contabase/internal/services"

// These states authorize presentation of individual values from the F1
// snapshot. Reservado/Disponível were removed with Reservas; only Saldo Total
// and Obrigações remain as independent indicators.
type FinancialIndicatorAvailability struct {
	RealBalanceAvailable bool
	ObligationsAvailable bool
}

func financialIndicatorAvailability(position services.WorkspaceFinancialPositionF1, err error) FinancialIndicatorAvailability {
	if err != nil || (position.Status != services.AvailableBalanceReliable && position.Status != services.AvailableBalanceUnavailable) {
		return FinancialIndicatorAvailability{}
	}
	if position.Status == services.AvailableBalanceUnavailable && len(position.Reasons) == 0 {
		return FinancialIndicatorAvailability{}
	}
	state := FinancialIndicatorAvailability{
		RealBalanceAvailable: true,
		ObligationsAvailable: true,
	}
	for _, reason := range position.Reasons {
		switch reason.Code {
		case "real_overflow":
			state.RealBalanceAvailable = false
		case "obligations_negative", "obligations_overflow", "invoice_composition_invalid",
			"expense_composition_invalid", "invoice_payment_invalid", "card_technical_payment",
			"recurring_indeterminate", "expense_account_invalid", "card_expense_without_invoice",
			"invoice_link_invalid", "expense_status_invalid", "non_card_invoice_link",
			"installment_invalid", "invoice_status_invalid", "invoice_item_type_invalid",
			"payment_reference_duplicate", "invoice_payment_description_ambiguous",
			"invoice_refund_invalid", "transaction_refund_invalid", "expense_amount_invalid":
			state.ObligationsAvailable = false
		case "expense_destination_historical", "invoice_payment_account_historical",
			"invoice_payment_attribution_historical":
			// Historical link alerts do not change the obligation amount itself.
		default:
			// New or unclassified reasons require an explicit presentation review.
			return FinancialIndicatorAvailability{}
		}
	}
	return state
}
