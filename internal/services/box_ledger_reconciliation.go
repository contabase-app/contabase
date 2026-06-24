package services

import (
	"database/sql"
	"fmt"
	"sort"
	"strings"
)

type BoxLedgerEvent struct {
	ID                 string
	BoxID              string
	Amount             int64
	Type               string
	SourceTransaction  string
	ReversalOfLedgerID string
	ReferenceDate      int64
	CreatedAt          int64
	Active             bool
}

type BoxLedgerSourceSummary struct {
	SourceTransactionID string
	ConsumeEvents       int
	ActiveConsumes      int
	ReversalEvents      int
}

type BoxLedgerReconciliationIssue struct {
	Code               string
	LedgerID           string
	BoxID              string
	SourceTransaction  string
	ReversalOfLedgerID string
	Detail             string
}

type BoxLedgerReconciliationReport struct {
	WorkspaceID     string
	BoxTotals       map[string]int64
	SourceSummaries []BoxLedgerSourceSummary
	Issues          []BoxLedgerReconciliationIssue
}

func (r BoxLedgerReconciliationReport) HasIssues() bool {
	return len(r.Issues) > 0
}

func ListBoxLedgerBySourceTransaction(db *sql.DB, workspaceID, sourceTransactionID string) ([]BoxLedgerEvent, error) {
	if db == nil {
		return nil, fmt.Errorf("db obrigatório")
	}
	workspaceID = strings.TrimSpace(workspaceID)
	sourceTransactionID = strings.TrimSpace(sourceTransactionID)
	if workspaceID == "" {
		return nil, fmt.Errorf("workspace obrigatório")
	}
	if sourceTransactionID == "" {
		return nil, fmt.Errorf("source_transaction_id obrigatório")
	}

	rows, err := db.Query(`
		SELECT
			l.id,
			l.box_id,
			l.amount,
			l.type,
			COALESCE(l.source_transaction_id, ''),
			COALESCE(l.reversal_of_ledger_id, ''),
			l.reference_date,
			l.created_at,
			CASE
				WHEN l.type = 'CONSUME' AND NOT EXISTS (
					SELECT 1
					FROM box_virtual_ledger r
					WHERE r.reversal_of_ledger_id = l.id
					  AND r.type = 'REVERSAL'
				) THEN 1
				ELSE 0
			END AS active
		FROM box_virtual_ledger l
		JOIN boxes b ON b.id = l.box_id
		WHERE b.workspace_id = ?
		  AND l.source_transaction_id = ?
		ORDER BY l.created_at ASC, l.id ASC
	`, workspaceID, sourceTransactionID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var events []BoxLedgerEvent
	for rows.Next() {
		var e BoxLedgerEvent
		var activeInt int64
		if err := rows.Scan(
			&e.ID,
			&e.BoxID,
			&e.Amount,
			&e.Type,
			&e.SourceTransaction,
			&e.ReversalOfLedgerID,
			&e.ReferenceDate,
			&e.CreatedAt,
			&activeInt,
		); err != nil {
			return nil, err
		}
		e.Active = activeInt == 1
		events = append(events, e)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return events, nil
}

func ReconcileWorkspaceBoxLedger(db *sql.DB, workspaceID string) (BoxLedgerReconciliationReport, error) {
	report := BoxLedgerReconciliationReport{
		WorkspaceID: strings.TrimSpace(workspaceID),
		BoxTotals:   make(map[string]int64),
	}
	if db == nil {
		return report, fmt.Errorf("db obrigatório")
	}
	if report.WorkspaceID == "" {
		return report, fmt.Errorf("workspace obrigatório")
	}

	type boxTotal struct {
		boxID string
		total int64
	}
	var totals []boxTotal
	rowsTotals, err := db.Query(`
		SELECT b.id, COALESCE(SUM(l.amount), 0)
		FROM boxes b
		LEFT JOIN box_virtual_ledger l ON l.box_id = b.id
		WHERE b.workspace_id = ?
		GROUP BY b.id
	`, report.WorkspaceID)
	if err != nil {
		return report, err
	}
	for rowsTotals.Next() {
		var item boxTotal
		if err := rowsTotals.Scan(&item.boxID, &item.total); err != nil {
			rowsTotals.Close()
			return report, err
		}
		totals = append(totals, item)
	}
	if err := rowsTotals.Close(); err != nil {
		return report, err
	}
	for _, item := range totals {
		report.BoxTotals[item.boxID] = item.total
	}

	type consumeRow struct {
		ledgerID      string
		boxID         string
		amount        int64
		sourceTx      string
		reversalCount int64
	}
	var consumes []consumeRow
	rowsConsumes, err := db.Query(`
		SELECT
			l.id,
			l.box_id,
			l.amount,
			COALESCE(l.source_transaction_id, ''),
			COUNT(r.id)
		FROM box_virtual_ledger l
		JOIN boxes b ON b.id = l.box_id
		LEFT JOIN box_virtual_ledger r
			ON r.reversal_of_ledger_id = l.id
		   AND r.type = 'REVERSAL'
		WHERE b.workspace_id = ?
		  AND l.type = 'CONSUME'
		GROUP BY l.id, l.box_id, l.amount, l.source_transaction_id
	`, report.WorkspaceID)
	if err != nil {
		return report, err
	}
	for rowsConsumes.Next() {
		var row consumeRow
		if err := rowsConsumes.Scan(&row.ledgerID, &row.boxID, &row.amount, &row.sourceTx, &row.reversalCount); err != nil {
			rowsConsumes.Close()
			return report, err
		}
		consumes = append(consumes, row)
	}
	if err := rowsConsumes.Close(); err != nil {
		return report, err
	}

	sourceSummary := make(map[string]*BoxLedgerSourceSummary)
	getSummary := func(sourceTx string) *BoxLedgerSourceSummary {
		sourceTx = strings.TrimSpace(sourceTx)
		if sourceTx == "" {
			return nil
		}
		if existing, ok := sourceSummary[sourceTx]; ok {
			return existing
		}
		s := &BoxLedgerSourceSummary{SourceTransactionID: sourceTx}
		sourceSummary[sourceTx] = s
		return s
	}
	addIssue := func(issue BoxLedgerReconciliationIssue) {
		report.Issues = append(report.Issues, issue)
	}

	var activeConsumes []consumeRow
	for _, c := range consumes {
		if c.amount >= 0 {
			addIssue(BoxLedgerReconciliationIssue{
				Code:              "consume_non_negative_amount",
				LedgerID:          c.ledgerID,
				BoxID:             c.boxID,
				SourceTransaction: c.sourceTx,
				Detail:            "CONSUME deve ser negativo",
			})
		}
		if strings.TrimSpace(c.sourceTx) == "" {
			addIssue(BoxLedgerReconciliationIssue{
				Code:     "consume_missing_source_transaction",
				LedgerID: c.ledgerID,
				BoxID:    c.boxID,
				Detail:   "CONSUME sem source_transaction_id",
			})
		}
		if c.reversalCount > 1 {
			addIssue(BoxLedgerReconciliationIssue{
				Code:              "consume_multiple_reversals",
				LedgerID:          c.ledgerID,
				BoxID:             c.boxID,
				SourceTransaction: c.sourceTx,
				Detail:            "CONSUME com mais de um REVERSAL",
			})
		}
		summary := getSummary(c.sourceTx)
		if summary != nil {
			summary.ConsumeEvents++
			if c.reversalCount == 0 {
				summary.ActiveConsumes++
			}
		}
		if c.reversalCount == 0 {
			activeConsumes = append(activeConsumes, c)
		}
	}

	type reversalRow struct {
		ledgerID         string
		boxID            string
		amount           int64
		sourceTx         string
		reversalOfLedger string
		targetExists     int64
		targetBoxID      string
		targetType       string
		targetAmount     int64
		targetSourceTx   string
	}
	var reversals []reversalRow
	rowsReversals, err := db.Query(`
		SELECT
			r.id,
			r.box_id,
			r.amount,
			COALESCE(r.source_transaction_id, ''),
			COALESCE(r.reversal_of_ledger_id, ''),
			CASE WHEN c.id IS NULL THEN 0 ELSE 1 END AS target_exists,
			COALESCE(c.box_id, ''),
			COALESCE(c.type, ''),
			COALESCE(c.amount, 0),
			COALESCE(c.source_transaction_id, '')
		FROM box_virtual_ledger r
		JOIN boxes b ON b.id = r.box_id
		LEFT JOIN box_virtual_ledger c ON c.id = r.reversal_of_ledger_id
		WHERE b.workspace_id = ?
		  AND r.type = 'REVERSAL'
	`, report.WorkspaceID)
	if err != nil {
		return report, err
	}
	for rowsReversals.Next() {
		var row reversalRow
		if err := rowsReversals.Scan(
			&row.ledgerID,
			&row.boxID,
			&row.amount,
			&row.sourceTx,
			&row.reversalOfLedger,
			&row.targetExists,
			&row.targetBoxID,
			&row.targetType,
			&row.targetAmount,
			&row.targetSourceTx,
		); err != nil {
			rowsReversals.Close()
			return report, err
		}
		reversals = append(reversals, row)
	}
	if err := rowsReversals.Close(); err != nil {
		return report, err
	}

	for _, r := range reversals {
		summary := getSummary(r.sourceTx)
		if summary != nil {
			summary.ReversalEvents++
		}
		if r.amount <= 0 {
			addIssue(BoxLedgerReconciliationIssue{
				Code:               "reversal_non_positive_amount",
				LedgerID:           r.ledgerID,
				BoxID:              r.boxID,
				SourceTransaction:  r.sourceTx,
				ReversalOfLedgerID: r.reversalOfLedger,
				Detail:             "REVERSAL deve ser positivo",
			})
		}
		if strings.TrimSpace(r.reversalOfLedger) == "" {
			addIssue(BoxLedgerReconciliationIssue{
				Code:              "reversal_missing_target",
				LedgerID:          r.ledgerID,
				BoxID:             r.boxID,
				SourceTransaction: r.sourceTx,
				Detail:            "REVERSAL sem reversal_of_ledger_id",
			})
			continue
		}
		if r.targetExists == 0 {
			addIssue(BoxLedgerReconciliationIssue{
				Code:               "reversal_target_not_found",
				LedgerID:           r.ledgerID,
				BoxID:              r.boxID,
				SourceTransaction:  r.sourceTx,
				ReversalOfLedgerID: r.reversalOfLedger,
				Detail:             "REVERSAL aponta para evento inexistente",
			})
			continue
		}
		if r.targetType != "CONSUME" {
			addIssue(BoxLedgerReconciliationIssue{
				Code:               "reversal_target_invalid_type",
				LedgerID:           r.ledgerID,
				BoxID:              r.boxID,
				SourceTransaction:  r.sourceTx,
				ReversalOfLedgerID: r.reversalOfLedger,
				Detail:             "REVERSAL deve apontar para CONSUME",
			})
			continue
		}
		if r.targetBoxID != r.boxID {
			addIssue(BoxLedgerReconciliationIssue{
				Code:               "reversal_target_box_mismatch",
				LedgerID:           r.ledgerID,
				BoxID:              r.boxID,
				SourceTransaction:  r.sourceTx,
				ReversalOfLedgerID: r.reversalOfLedger,
				Detail:             "REVERSAL e CONSUME alvo em caixinhas diferentes",
			})
		}
		if r.targetAmount >= 0 {
			addIssue(BoxLedgerReconciliationIssue{
				Code:               "reversal_target_non_negative_consume",
				LedgerID:           r.ledgerID,
				BoxID:              r.boxID,
				SourceTransaction:  r.sourceTx,
				ReversalOfLedgerID: r.reversalOfLedger,
				Detail:             "CONSUME alvo deve ser negativo",
			})
		} else if r.amount != -r.targetAmount {
			addIssue(BoxLedgerReconciliationIssue{
				Code:               "reversal_amount_mismatch",
				LedgerID:           r.ledgerID,
				BoxID:              r.boxID,
				SourceTransaction:  r.sourceTx,
				ReversalOfLedgerID: r.reversalOfLedger,
				Detail:             "REVERSAL com valor diferente do CONSUME alvo",
			})
		}
		if strings.TrimSpace(r.sourceTx) != "" && strings.TrimSpace(r.targetSourceTx) != "" && strings.TrimSpace(r.sourceTx) != strings.TrimSpace(r.targetSourceTx) {
			addIssue(BoxLedgerReconciliationIssue{
				Code:               "reversal_source_transaction_mismatch",
				LedgerID:           r.ledgerID,
				BoxID:              r.boxID,
				SourceTransaction:  r.sourceTx,
				ReversalOfLedgerID: r.reversalOfLedger,
				Detail:             "source_transaction_id do REVERSAL difere do CONSUME alvo",
			})
		}
	}

	uniqueSourceIDs := make([]string, 0, len(activeConsumes))
	seenSource := make(map[string]struct{}, len(activeConsumes))
	for _, c := range activeConsumes {
		source := strings.TrimSpace(c.sourceTx)
		if source == "" {
			continue
		}
		if _, ok := seenSource[source]; ok {
			continue
		}
		seenSource[source] = struct{}{}
		uniqueSourceIDs = append(uniqueSourceIDs, source)
	}

	type txRow struct {
		id          string
		workspaceID string
		trType      string
		amount      int64
		categoryID  string
	}
	txByID := make(map[string]txRow, len(uniqueSourceIDs))
	if len(uniqueSourceIDs) > 0 {
		query := `
			SELECT id, workspace_id, type, amount, COALESCE(category_id, '')
			FROM transactions
			WHERE id IN (` + placeholders(len(uniqueSourceIDs)) + `)
		`
		args := make([]interface{}, 0, len(uniqueSourceIDs))
		for _, id := range uniqueSourceIDs {
			args = append(args, id)
		}
		rowsTx, err := db.Query(query, args...)
		if err != nil {
			return report, err
		}
		for rowsTx.Next() {
			var row txRow
			if err := rowsTx.Scan(&row.id, &row.workspaceID, &row.trType, &row.amount, &row.categoryID); err != nil {
				rowsTx.Close()
				return report, err
			}
			txByID[row.id] = row
		}
		if err := rowsTx.Close(); err != nil {
			return report, err
		}
	}

	type expectedBoxResult struct {
		boxID string
		count int64
	}
	expectedByCategory := make(map[string]expectedBoxResult)
	for _, c := range activeConsumes {
		source := strings.TrimSpace(c.sourceTx)
		if source == "" {
			continue
		}
		txData, ok := txByID[source]
		if !ok {
			addIssue(BoxLedgerReconciliationIssue{
				Code:              "active_consume_missing_transaction",
				LedgerID:          c.ledgerID,
				BoxID:             c.boxID,
				SourceTransaction: c.sourceTx,
				Detail:            "CONSUME ativo sem transação existente",
			})
			continue
		}
		if txData.workspaceID != report.WorkspaceID {
			addIssue(BoxLedgerReconciliationIssue{
				Code:              "active_consume_cross_workspace_transaction",
				LedgerID:          c.ledgerID,
				BoxID:             c.boxID,
				SourceTransaction: c.sourceTx,
				Detail:            "CONSUME ativo aponta para transação de outro workspace",
			})
			continue
		}
		if txData.trType != "EXPENSE" {
			addIssue(BoxLedgerReconciliationIssue{
				Code:              "active_consume_non_expense_transaction",
				LedgerID:          c.ledgerID,
				BoxID:             c.boxID,
				SourceTransaction: c.sourceTx,
				Detail:            "CONSUME ativo deve apontar para transação EXPENSE",
			})
		}
		if c.amount >= 0 || txData.amount != -c.amount {
			addIssue(BoxLedgerReconciliationIssue{
				Code:              "active_consume_amount_mismatch",
				LedgerID:          c.ledgerID,
				BoxID:             c.boxID,
				SourceTransaction: c.sourceTx,
				Detail:            "valor do CONSUME ativo difere do valor atual da transação",
			})
		}
		categoryID := strings.TrimSpace(txData.categoryID)
		if categoryID == "" {
			addIssue(BoxLedgerReconciliationIssue{
				Code:              "active_consume_missing_category",
				LedgerID:          c.ledgerID,
				BoxID:             c.boxID,
				SourceTransaction: c.sourceTx,
				Detail:            "transação do CONSUME ativo sem categoria",
			})
			continue
		}
		expected, ok := expectedByCategory[categoryID]
		if !ok {
			var count int64
			var boxID sql.NullString
			if err := db.QueryRow(`
				SELECT COUNT(b.id), MIN(b.id)
				FROM boxes b
				JOIN categories c ON c.workspace_id = b.workspace_id
				WHERE c.id = ?
				  AND c.workspace_id = ?
				  AND (
					b.category_id = c.id OR
					(COALESCE(c.parent_id, '') != '' AND b.category_id = c.parent_id)
				  )
			`, categoryID, report.WorkspaceID).Scan(&count, &boxID); err != nil {
				return report, err
			}
			expected = expectedBoxResult{count: count}
			if boxID.Valid {
				expected.boxID = boxID.String
			}
			expectedByCategory[categoryID] = expected
		}
		if expected.count == 0 {
			addIssue(BoxLedgerReconciliationIssue{
				Code:              "active_consume_category_without_box",
				LedgerID:          c.ledgerID,
				BoxID:             c.boxID,
				SourceTransaction: c.sourceTx,
				Detail:            "categoria atual sem caixinha vinculada",
			})
			continue
		}
		if expected.count > 1 {
			addIssue(BoxLedgerReconciliationIssue{
				Code:              "active_consume_category_ambiguous_box",
				LedgerID:          c.ledgerID,
				BoxID:             c.boxID,
				SourceTransaction: c.sourceTx,
				Detail:            "categoria atual com múltiplas caixinhas possíveis",
			})
			continue
		}
		if expected.boxID != c.boxID {
			addIssue(BoxLedgerReconciliationIssue{
				Code:              "active_consume_wrong_box",
				LedgerID:          c.ledgerID,
				BoxID:             c.boxID,
				SourceTransaction: c.sourceTx,
				Detail:            "CONSUME ativo em caixinha diferente da categoria atual",
			})
		}
	}

	for _, summary := range sourceSummary {
		report.SourceSummaries = append(report.SourceSummaries, *summary)
	}
	sort.Slice(report.SourceSummaries, func(i, j int) bool {
		return report.SourceSummaries[i].SourceTransactionID < report.SourceSummaries[j].SourceTransactionID
	})
	sort.Slice(report.Issues, func(i, j int) bool {
		if report.Issues[i].Code == report.Issues[j].Code {
			return report.Issues[i].LedgerID < report.Issues[j].LedgerID
		}
		return report.Issues[i].Code < report.Issues[j].Code
	})

	return report, nil
}

func placeholders(n int) string {
	if n <= 0 {
		return ""
	}
	if n == 1 {
		return "?"
	}
	return strings.TrimRight(strings.Repeat("?,", n), ",")
}
