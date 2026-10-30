package services

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"

	"github.com/contabase-app/contabase/internal/models"
)

const InvestmentReceiptUserDeclaration = "DECLARACAO_DO_USUARIO_AUTORIZADO"

type InvestmentSettlementAccountState struct {
	ID                           string
	BalanceCents, BalanceVersion int64
}

// C1 state and financial application are separate facts. No aggregation that
// writes balances, and no C2 writer, is used to compose this read snapshot.
type InvestmentSettlementOperationalState struct {
	InvestmentID, AccountID         string
	Account                         InvestmentSettlementAccountState
	DestinationAccount              *InvestmentSettlementAccountState
	Withdrawal                      models.InvestmentWithdrawal
	State                           string
	Version                         int64
	PrincipalCents                  *int64
	Settlement                      *InvestmentSettlement
	ReceiptConfirmationKind         string
	ApplicationID, ClassificationID string
	Application                     *InvestmentSettlementApplication
	Classification                  *InvestmentResultClassification
	Pending                         []string
}

func ReadInvestmentSettlementOperationalState(ctx context.Context, db *sql.DB, workspace, investment, withdrawal string) (InvestmentSettlementOperationalState, error) {
	s := InvestmentSettlementOperationalState{InvestmentID: investment, State: "MISSING", Pending: []string{}}
	tx, err := db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return s, err
	}
	defer tx.Rollback()
	err = tx.QueryRowContext(ctx, `SELECT i.account_id,a.current_balance,a.balance_version FROM investments i JOIN accounts a ON a.workspace_id=i.workspace_id AND a.id=i.account_id WHERE i.workspace_id=? AND i.id=?`, workspace, investment).Scan(&s.AccountID, &s.Account.BalanceCents, &s.Account.BalanceVersion)
	if errors.Is(err, sql.ErrNoRows) {
		return s, ErrInvestmentNotFound
	}
	if err != nil {
		return s, err
	}
	s.Account.ID = s.AccountID
	s.Withdrawal, err = settlementWithdrawal(ctx, tx, workspace, investment, withdrawal)
	if err != nil {
		return s, err
	}
	s.PrincipalCents, err = settlementPrincipal(s.Withdrawal)
	if err != nil {
		return s, err
	}
	var exists int
	err = tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM investment_settlement_revisions WHERE workspace_id=? AND investment_id=? AND withdrawal_id=?`, workspace, investment, withdrawal).Scan(&exists)
	if err != nil {
		return s, err
	}
	if exists == 0 {
		s.Pending = append(s.Pending, "SETTLEMENT_MISSING")
	} else {
		settlement, e := readInvestmentSettlementTx(ctx, tx, workspace, investment, withdrawal)
		if e != nil {
			return s, e
		}
		s.Settlement, s.State, s.Version = &settlement, settlement.State, settlement.Version
		destination := InvestmentSettlementAccountState{ID: settlement.Input.DestinationAccountID}
		err = tx.QueryRowContext(ctx, `SELECT current_balance,balance_version FROM accounts WHERE workspace_id=? AND id=?`, workspace, destination.ID).Scan(&destination.BalanceCents, &destination.BalanceVersion)
		if errors.Is(err, sql.ErrNoRows) {
			return s, ErrInvestmentNotFound
		}
		if err != nil {
			return s, err
		}
		s.DestinationAccount = &destination
		for _, field := range []struct {
			name  string
			value *int64
		}{
			{"GROSS_UNKNOWN", settlement.Input.GrossCents}, {"IR_UNKNOWN", settlement.Input.IRCents},
			{"IOF_UNKNOWN", settlement.Input.IOFCents}, {"FEES_UNKNOWN", settlement.Input.FeesCents},
			{"PATRIMONY_UNKNOWN", settlement.Input.PatrimonyCents},
		} {
			if field.value == nil {
				s.Pending = append(s.Pending, field.name)
			}
		}
		if settlement.State != InvestmentSettlementReconciled {
			s.Pending = append(s.Pending, "RECEIPT_CONFIRMATION_PENDING")
		} else {
			s.ReceiptConfirmationKind = InvestmentReceiptUserDeclaration
		}
	}
	if s.PrincipalCents == nil {
		s.Pending = append(s.Pending, "PRINCIPAL_UNKNOWN")
	}
	for _, lookup := range []struct {
		query  string
		dest   *string
		result any
	}{
		{`SELECT id,result_json FROM investment_settlement_applications WHERE workspace_id=? AND investment_id=? AND withdrawal_id=?`, &s.ApplicationID, &s.Application},
		{`SELECT id,result_json FROM investment_result_classifications WHERE workspace_id=? AND investment_id=? AND withdrawal_id=?`, &s.ClassificationID, &s.Classification},
	} {
		var result string
		err = tx.QueryRowContext(ctx, lookup.query, workspace, investment, withdrawal).Scan(lookup.dest, &result)
		if errors.Is(err, sql.ErrNoRows) {
			continue
		}
		if err != nil {
			return s, err
		}
		if err = json.Unmarshal([]byte(result), lookup.result); err != nil {
			return s, err
		}
	}
	if s.ApplicationID != "" && (s.Settlement == nil || s.Application == nil || s.Application.ID != s.ApplicationID || s.Application.SettlementRevisionID != s.Settlement.ID || s.Application.State != InvestmentSettlementFinancialApplied) {
		return s, ErrInvestmentConflict
	}
	if s.ClassificationID != "" && (s.Application == nil || s.Classification == nil || s.Classification.ID != s.ClassificationID || s.Classification.State != InvestmentClassificationCompleted && s.Classification.State != InvestmentClassificationUnavailable) {
		return s, ErrInvestmentConflict
	}
	return s, tx.Commit()
}
