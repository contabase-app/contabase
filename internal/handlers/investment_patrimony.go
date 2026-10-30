package handlers

import (
	"context"
	"database/sql"

	"github.com/contabase-app/contabase/internal/features"
	"github.com/contabase-app/contabase/internal/models"
	"github.com/contabase-app/contabase/internal/services"
)

type investmentPatrimonyPositionView struct {
	Name, Date, DateISO, Value, Reason string
	Eligible                           bool
}

type investmentPatrimonyAccountView struct {
	Name               string
	ArchivedMaintained bool
	Positions          []investmentPatrimonyPositionView
}

type investmentPatrimonyView struct {
	Title, Value, Coverage         string
	Available                      bool
	WithoutValidPosition           int
	ArchivedMaintainedAccountCount int
	Accounts                       []investmentPatrimonyAccountView
}

// Reuse existing feature/permission decisions; do not expose investment data
// through a less privileged dashboard or settings read.
func queryInvestmentPatrimonyViews(db *sql.DB, workspaceID, userID string) (*investmentPatrimonyView, map[string]*investmentPatrimonyView) {
	if !features.FromEnv().Investments {
		return nil, nil
	}
	var member models.WorkspaceMember
	if err := db.QueryRow(`SELECT role FROM workspace_members WHERE workspace_id = ? AND user_id = ?`, workspaceID, userID).Scan(&member.Role); err != nil || !models.HasPermission(&member, models.PermissionInvestmentsRead) {
		return nil, nil
	}
	projection, err := services.ReadWorkspaceInvestmentPatrimony(context.Background(), db, workspaceID)
	if err != nil {
		return &investmentPatrimonyView{Title: "Patrimônio aplicado conhecido", Coverage: "Indisponível — não foi possível consultar as posições"}, nil
	}
	view := &investmentPatrimonyView{
		Title: "Patrimônio aplicado conhecido", Available: projection.EligibleCount > 0,
		Value: formatCurrencyLabel(projection.SubtotalCents), Coverage: investmentPatrimonyCoverage(projection.EligibleCount),
		WithoutValidPosition: projection.WithoutValidPosition, ArchivedMaintainedAccountCount: projection.ArchivedMaintainedAccountCount,
	}
	accounts := make(map[string]*investmentPatrimonyView)
	for _, account := range projection.Accounts {
		accountView := investmentPatrimonyAccountView{Name: account.AccountName, ArchivedMaintained: account.Archived && account.HasMaintainedInvestments}
		for _, item := range account.Investments {
			position := investmentPatrimonyPositionView{Name: item.Investment.Name, Eligible: item.Eligible, Reason: item.Reason}
			if item.Position != nil {
				position.Value = formatCurrencyLabel(item.Position.AmountCents)
				position.DateISO = item.Position.ValuationDate
				position.Date = investmentDateLabel(item.Position.ValuationDate)
			}
			accountView.Positions = append(accountView.Positions, position)
		}
		view.Accounts = append(view.Accounts, accountView)
		accounts[account.AccountID] = &investmentPatrimonyView{
			Title: "Posição aplicada conhecida", Available: account.EligibleCount > 0,
			Value: formatCurrencyLabel(account.SubtotalCents), Coverage: investmentPatrimonyCoverage(account.EligibleCount),
			WithoutValidPosition: account.WithoutValidPosition, Accounts: []investmentPatrimonyAccountView{accountView},
		}
	}
	return view, accounts
}

func investmentPatrimonyCoverage(eligible int) string {
	if eligible == 0 {
		return "Indisponível — nenhuma posição válida para a composição atual"
	}
	return "Parcial — caixa livre da corretora ainda não conciliado"
}

func (h *ConfiguracoesHandler) attachConfigInvestmentPatrimony(data *ConfiguracoesData) {
	view, accounts := queryInvestmentPatrimonyViews(h.DB, h.WorkspaceID, h.UserID)
	data.InvestmentPatrimony = view
	for _, rows := range [][]ConfigAccountRow{data.Contas, data.ContasArquivadas} {
		for i := range rows {
			if rows[i].Type == models.AccountTypeInvestment {
				rows[i].InvestmentPatrimony = accountInvestmentPatrimonyView(view, accounts, rows[i].ID)
			}
		}
	}
}

func accountInvestmentPatrimonyView(summary *investmentPatrimonyView, accounts map[string]*investmentPatrimonyView, accountID string) *investmentPatrimonyView {
	if summary == nil {
		return nil
	}
	if view := accounts[accountID]; view != nil {
		return view
	}
	return &investmentPatrimonyView{Title: "Posição aplicada conhecida", Coverage: "Indisponível — não foi possível consultar as posições"}
}
