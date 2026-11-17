package handlers

import (
	"errors"
	"testing"

	"github.com/contabase-app/contabase/internal/services"
)

func TestFinancialIndicatorAvailability(t *testing.T) {
	tests := []struct {
		name string
		got  services.WorkspaceFinancialPositionF1
		err  error
		want FinancialIndicatorAvailability
	}{
		{
			name: "reliable zero",
			got:  services.WorkspaceFinancialPositionF1{Status: services.AvailableBalanceReliable},
			want: FinancialIndicatorAvailability{RealBalanceAvailable: true, ObligationsAvailable: true},
		},
		{
			name: "read error",
			got:  services.WorkspaceFinancialPositionF1{Status: services.AvailableBalanceReliable},
			err:  errors.New("read failed"),
			want: FinancialIndicatorAvailability{},
		},
		{
			name: "empty status",
			want: FinancialIndicatorAvailability{},
		},
		{
			name: "unavailable without reason",
			got:  services.WorkspaceFinancialPositionF1{Status: services.AvailableBalanceUnavailable},
			want: FinancialIndicatorAvailability{},
		},
		{
			name: "real overflow",
			got: services.WorkspaceFinancialPositionF1{
				Status:  services.AvailableBalanceUnavailable,
				Reasons: []services.AvailableBalanceReason{{Code: "real_overflow"}},
			},
			want: FinancialIndicatorAvailability{RealBalanceAvailable: false, ObligationsAvailable: true},
		},
		{
			name: "obligations invalid",
			got: services.WorkspaceFinancialPositionF1{
				Status:  services.AvailableBalanceUnavailable,
				Reasons: []services.AvailableBalanceReason{{Code: "invoice_link_invalid"}},
			},
			want: FinancialIndicatorAvailability{RealBalanceAvailable: true, ObligationsAvailable: false},
		},
		{
			name: "historical alerts keep both",
			got: services.WorkspaceFinancialPositionF1{
				Status: services.AvailableBalanceUnavailable,
				Reasons: []services.AvailableBalanceReason{
					{Code: "expense_destination_historical"},
					{Code: "invoice_payment_account_historical"},
					{Code: "invoice_payment_attribution_historical"},
				},
			},
			want: FinancialIndicatorAvailability{RealBalanceAvailable: true, ObligationsAvailable: true},
		},
		{
			name: "unknown reason blocks both",
			got: services.WorkspaceFinancialPositionF1{
				Status:  services.AvailableBalanceUnavailable,
				Reasons: []services.AvailableBalanceReason{{Code: "unknown_future_reason"}},
			},
			want: FinancialIndicatorAvailability{},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := financialIndicatorAvailability(tc.got, tc.err); got != tc.want {
				t.Fatalf("financialIndicatorAvailability() = %+v, want %+v", got, tc.want)
			}
		})
	}
}
