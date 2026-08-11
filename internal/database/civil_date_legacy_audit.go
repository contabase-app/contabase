package database

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"text/tabwriter"
	"time"
)

// Constants (Reason*, Semantic*, Audit*) are defined in civil_date_classifier.go.

const auditSchemaVersion = 1

type auditFinding struct {
	WorkspaceID          string `json:"workspace_id"`
	WorkspaceType        string `json:"workspace_type"`
	SeriesID             string `json:"series_id"`
	RecurringRuleID      string `json:"recurring_rule_id"`
	ParentRuleID         string `json:"parent_rule_id,omitempty"`
	TransactionID        string `json:"transaction_id"`
	RecurrenceSequence   int64  `json:"recurrence_sequence"`
	TransactionType      string `json:"transaction_type"`
	Status               string `json:"status"`
	IsRecurringException bool   `json:"is_recurring_exception"`
	ReasonCode           string `json:"reason_code"`
	InvoicePresent       bool   `json:"invoice_present"`
	InvoiceStatus        string `json:"invoice_status,omitempty"`
	DueDatePresent       bool   `json:"due_date_present"`
	DateCivilPresent     bool   `json:"date_civil_present"`
	AuditedField         string `json:"audited_field"`
	SemanticState        string `json:"semantic_state"`
	AuditOutcome         string `json:"audit_outcome"`
	Deterministic        bool   `json:"deterministic"`
	ProposedCivilValue   string `json:"proposed_civil_value,omitempty"`
}

type auditSummary struct {
	WorkspacesScanned   int64 `json:"workspaces_scanned"`
	SeriesScanned       int64 `json:"series_scanned"`
	TransactionsScanned int64 `json:"transactions_scanned"`
	Valid               int64 `json:"valid"`
	Deterministic       int64 `json:"deterministic"`
	HumanReview         int64 `json:"human_review"`
}

type AuditReport struct {
	SchemaVersion int            `json:"schema_version"`
	Summary       auditSummary   `json:"summary"`
	Findings      []auditFinding `json:"findings"`
}

type RawAuditRow struct {
	TransactionID            string
	WorkspaceID              string
	WorkspaceType            string
	SeriesID                 sql.NullString
	RecurringRuleID          sql.NullString
	ParentRuleID             sql.NullString
	RecurrenceSequence       sql.NullInt64
	TransactionType          string
	Status                   string
	DateUnix                 int64
	DateCivil                sql.NullString
	DueDateUnix              sql.NullInt64
	DueDateCivil             sql.NullString
	IsRecurringException     bool
	RecurringExceptionReason sql.NullString
	InvoiceID                sql.NullString
	InvoiceStatus            sql.NullString

	RuleWorkspaceID       sql.NullString
	RuleStartDate         sql.NullInt64
	RuleStartDateCivil    sql.NullString
	RuleEffectiveStartSeq sql.NullInt64
	RuleEffectiveEndSeq   sql.NullInt64
	RuleActive            sql.NullInt64
	RuleFrequency         sql.NullString
	RuleSeriesID          sql.NullString

	SeriesWorkspaceID sql.NullString
	SeriesActive      sql.NullInt64

	ParentWorkspaceID sql.NullString
	ParentExists      bool
	ParentSeriesID    sql.NullString
}

const auditQuery = `
SELECT
	t.id,
	t.workspace_id,
	COALESCE(w.type, 'personal'),
	t.series_id,
	t.recurring_rule_id,
	r.parent_rule_id,
	t.recurrence_sequence,
	t.type,
	t.status,
	t.date,
	t.date_civil,
	t.due_date,
	t.due_date_civil,
	t.is_recurring_exception,
	t.recurring_exception_reason,
	t.invoice_id,
	inv.status,
	r.workspace_id AS rule_workspace_id,
	r.start_date,
	r.start_date_civil,
	r.effective_start_sequence,
	r.effective_end_sequence,
	r.active,
	r.frequency,
	r.series_id AS rule_series_id,
	s.workspace_id AS series_workspace_id,
	s.active,
	p.workspace_id AS parent_workspace_id,
	CASE WHEN p.id IS NOT NULL THEN 1 ELSE 0 END AS parent_exists,
	p.series_id AS parent_series_id
FROM transactions t
LEFT JOIN workspaces w ON w.id = t.workspace_id
LEFT JOIN recurring_rules r ON r.id = t.recurring_rule_id
LEFT JOIN recurring_series s ON s.id = t.series_id
LEFT JOIN invoices inv ON inv.id = t.invoice_id
LEFT JOIN recurring_rules p ON p.id = r.parent_rule_id
WHERE t.recurring_rule_id IS NOT NULL
ORDER BY t.workspace_id, t.series_id, t.recurrence_sequence, t.id
`

func quickCheckConn(ctx context.Context, conn *sql.Conn) error {
	rows, err := conn.QueryContext(ctx, "PRAGMA quick_check")
	if err != nil {
		return fmt.Errorf("quick_check query failed: %w", err)
	}
	defer rows.Close()

	var results []string
	for rows.Next() {
		var val string
		if err := rows.Scan(&val); err != nil {
			return fmt.Errorf("quick_check scan failed: %w", err)
		}
		results = append(results, val)
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("quick_check iteration failed: %w", err)
	}

	if len(results) == 0 {
		return fmt.Errorf("quick_check returned no rows")
	}
	if len(results) > 1 {
		return fmt.Errorf("quick_check returned multiple rows: %v", strings.Join(results, ", "))
	}
	if strings.TrimSpace(results[0]) != "ok" {
		return fmt.Errorf("quick_check failed: %s", results[0])
	}
	return nil
}

type seqDupKey struct {
	workspaceID string
	seriesID    string
	seq         int64
}

func loadSequenceDuplicatesConn(ctx context.Context, conn *sql.Conn) (map[seqDupKey]bool, error) {
	rows, err := conn.QueryContext(ctx, `
		SELECT workspace_id, series_id, recurrence_sequence, COUNT(*)
		FROM transactions
		WHERE recurring_rule_id IS NOT NULL AND series_id IS NOT NULL AND recurrence_sequence IS NOT NULL
		GROUP BY workspace_id, series_id, recurrence_sequence
		HAVING COUNT(*) > 1
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	dups := make(map[seqDupKey]bool)
	for rows.Next() {
		var ws, series sql.NullString
		var seq sql.NullInt64
		var cnt int64
		if err := rows.Scan(&ws, &series, &seq, &cnt); err != nil {
			return nil, err
		}
		dups[seqDupKey{ws.String, series.String, seq.Int64}] = true
	}
	return dups, rows.Err()
}

func RunCivilDateLegacyAudit(ctx context.Context, db *sql.DB, format string) (*AuditReport, int, error) {
	if db == nil {
		return nil, 1, fmt.Errorf("database connection is nil")
	}

	conn, err := db.Conn(ctx)
	if err != nil {
		return nil, 1, fmt.Errorf("failed to get dedicated connection: %w", err)
	}
	defer conn.Close()

	if err := conn.PingContext(ctx); err != nil {
		return nil, 1, fmt.Errorf("database ping failed: %w", err)
	}

	if _, err := conn.ExecContext(ctx, "PRAGMA query_only = ON"); err != nil {
		return nil, 1, fmt.Errorf("failed to set query_only: %w", err)
	}
	var qo int
	if err := conn.QueryRowContext(ctx, "PRAGMA query_only").Scan(&qo); err != nil {
		return nil, 1, fmt.Errorf("failed to verify query_only: %w", err)
	}
	if qo != 1 {
		return nil, 1, fmt.Errorf("query_only is %d, expected 1", qo)
	}

	if err := quickCheckConn(ctx, conn); err != nil {
		return nil, 1, fmt.Errorf("integrity check failed: %w", err)
	}

	seqDups, err := loadSequenceDuplicatesConn(ctx, conn)
	if err != nil {
		return nil, 1, fmt.Errorf("failed to load sequence duplicates: %w", err)
	}

	report := &AuditReport{SchemaVersion: auditSchemaVersion}

	rows, err := conn.QueryContext(ctx, auditQuery)
	if err != nil {
		return nil, 1, fmt.Errorf("audit query failed: %w", err)
	}
	defer rows.Close()

	var (
		workspacesSeen = make(map[string]bool)
		seriesSeen     = make(map[string]bool)
		findings       []auditFinding
		totalScanned   int64
	)

	for rows.Next() {
		var raw RawAuditRow
		if err := rows.Scan(
			&raw.TransactionID,
			&raw.WorkspaceID,
			&raw.WorkspaceType,
			&raw.SeriesID,
			&raw.RecurringRuleID,
			&raw.ParentRuleID,
			&raw.RecurrenceSequence,
			&raw.TransactionType,
			&raw.Status,
			&raw.DateUnix,
			&raw.DateCivil,
			&raw.DueDateUnix,
			&raw.DueDateCivil,
			&raw.IsRecurringException,
			&raw.RecurringExceptionReason,
			&raw.InvoiceID,
			&raw.InvoiceStatus,
			&raw.RuleWorkspaceID,
			&raw.RuleStartDate,
			&raw.RuleStartDateCivil,
			&raw.RuleEffectiveStartSeq,
			&raw.RuleEffectiveEndSeq,
			&raw.RuleActive,
			&raw.RuleFrequency,
			&raw.RuleSeriesID,
			&raw.SeriesWorkspaceID,
			&raw.SeriesActive,
			&raw.ParentWorkspaceID,
			&raw.ParentExists,
			&raw.ParentSeriesID,
		); err != nil {
			return nil, 1, fmt.Errorf("row scan failed: %w", err)
		}

		workspacesSeen[raw.WorkspaceID] = true
		if raw.SeriesID.Valid {
			seriesSeen[raw.SeriesID.String] = true
		}
		totalScanned++

		rowFindings := classifyRow(raw, seqDups)
		findings = append(findings, rowFindings...)
	}

	if err := rows.Err(); err != nil {
		return nil, 1, fmt.Errorf("rows iteration failed: %w", err)
	}

	sortFindings(findings)

	var (
		validCount         int64
		detCount           int64
		humanCount         int64
		actionableFindings []auditFinding
	)

	for _, f := range findings {
		switch f.AuditOutcome {
		case AuditValid:
			validCount++
		default:
			actionableFindings = append(actionableFindings, f)
			if f.Deterministic {
				detCount++
			} else {
				humanCount++
			}
		}
	}

	report.Summary = auditSummary{
		WorkspacesScanned:   int64(len(workspacesSeen)),
		SeriesScanned:       int64(len(seriesSeen)),
		TransactionsScanned: totalScanned,
		Valid:               validCount,
		Deterministic:       detCount,
		HumanReview:         humanCount,
	}
	report.Findings = actionableFindings

	exitCode := exitCodeFromFindings(detCount, humanCount)
	return report, exitCode, nil
}

func exitCodeFromFindings(deterministic, humanReview int64) int {
	if humanReview > 0 {
		return 3
	}
	if deterministic > 0 {
		return 2
	}
	return 0
}

// classifyRow produces findings for a single transaction row.
// Precedence of structural checks before civil-date checks:
//  1. cross-workspace reference
//  2. missing rule/series
//  3. invalid/duplicate/out-of-window sequence
//  4. parent rule structure
//  5. invoice structure
//  6. exception structure
//  7. invalid/mismatched civil field
//  8. missing but deterministic civil field
func classifyRow(raw RawAuditRow, seqDups map[seqDupKey]bool) []auditFinding {
	var findings []auditFinding
	semState := classifySemanticState(raw)

	// 1. Sequence invalid (<= 0)
	if raw.RecurrenceSequence.Valid && raw.RecurrenceSequence.Int64 <= 0 {
		findings = append(findings, auditFinding{
			WorkspaceID:          raw.WorkspaceID,
			WorkspaceType:        raw.WorkspaceType,
			SeriesID:             nullStr(raw.SeriesID),
			RecurringRuleID:      nullStr(raw.RecurringRuleID),
			ParentRuleID:         nullStr(raw.ParentRuleID),
			TransactionID:        raw.TransactionID,
			RecurrenceSequence:   coalesceSeq(raw.RecurrenceSequence),
			TransactionType:      raw.TransactionType,
			Status:               raw.Status,
			IsRecurringException: raw.IsRecurringException,
			ReasonCode:           ReasonSequenceInvalid,
			SemanticState:        semState,
			AuditOutcome:         AuditInconsistentHumanReview,
			Deterministic:        false,
			AuditedField:         "recurrence_sequence",
		})
	}

	// 2. Rule not found
	if ruleNotFound(raw) {
		findings = append(findings, auditFinding{
			WorkspaceID:          raw.WorkspaceID,
			WorkspaceType:        raw.WorkspaceType,
			SeriesID:             nullStr(raw.SeriesID),
			RecurringRuleID:      nullStr(raw.RecurringRuleID),
			ParentRuleID:         nullStr(raw.ParentRuleID),
			TransactionID:        raw.TransactionID,
			RecurrenceSequence:   coalesceSeq(raw.RecurrenceSequence),
			TransactionType:      raw.TransactionType,
			Status:               raw.Status,
			IsRecurringException: raw.IsRecurringException,
			ReasonCode:           ReasonRuleNotFound,
			SemanticState:        semState,
			AuditOutcome:         AuditInconsistentHumanReview,
			Deterministic:        false,
			AuditedField:         "recurring_rule",
		})
		return findings
	}

	// 3. Cross-workspace reference
	if crossWorkspace(raw) {
		findings = append(findings, auditFinding{
			WorkspaceID:          raw.WorkspaceID,
			WorkspaceType:        raw.WorkspaceType,
			SeriesID:             nullStr(raw.SeriesID),
			RecurringRuleID:      nullStr(raw.RecurringRuleID),
			ParentRuleID:         nullStr(raw.ParentRuleID),
			TransactionID:        raw.TransactionID,
			RecurrenceSequence:   coalesceSeq(raw.RecurrenceSequence),
			TransactionType:      raw.TransactionType,
			Status:               raw.Status,
			IsRecurringException: raw.IsRecurringException,
			ReasonCode:           ReasonCrossWorkspaceReference,
			SemanticState:        semState,
			AuditOutcome:         AuditInconsistentHumanReview,
			Deterministic:        false,
			AuditedField:         "workspace_reference",
		})
		return findings
	}

	// 4. Series not found
	if seriesNotFound(raw) {
		findings = append(findings, auditFinding{
			WorkspaceID:          raw.WorkspaceID,
			WorkspaceType:        raw.WorkspaceType,
			SeriesID:             nullStr(raw.SeriesID),
			RecurringRuleID:      nullStr(raw.RecurringRuleID),
			ParentRuleID:         nullStr(raw.ParentRuleID),
			TransactionID:        raw.TransactionID,
			RecurrenceSequence:   coalesceSeq(raw.RecurrenceSequence),
			TransactionType:      raw.TransactionType,
			Status:               raw.Status,
			IsRecurringException: raw.IsRecurringException,
			ReasonCode:           ReasonSeriesNotFound,
			SemanticState:        semState,
			AuditOutcome:         AuditInconsistentHumanReview,
			Deterministic:        false,
			AuditedField:         "recurring_series",
		})
	}

	// 5. Sequence duplicate
	if isSequenceDuplicate(raw, seqDups) {
		findings = append(findings, auditFinding{
			WorkspaceID:          raw.WorkspaceID,
			WorkspaceType:        raw.WorkspaceType,
			SeriesID:             nullStr(raw.SeriesID),
			RecurringRuleID:      nullStr(raw.RecurringRuleID),
			ParentRuleID:         nullStr(raw.ParentRuleID),
			TransactionID:        raw.TransactionID,
			RecurrenceSequence:   coalesceSeq(raw.RecurrenceSequence),
			TransactionType:      raw.TransactionType,
			Status:               raw.Status,
			IsRecurringException: raw.IsRecurringException,
			ReasonCode:           ReasonSequenceDuplicate,
			SemanticState:        semState,
			AuditOutcome:         AuditInconsistentHumanReview,
			Deterministic:        false,
			AuditedField:         "recurrence_sequence",
		})
	}

	// 6. Sequence outside rule window
	if isSequenceOutsideWindow(raw) {
		findings = append(findings, auditFinding{
			WorkspaceID:          raw.WorkspaceID,
			WorkspaceType:        raw.WorkspaceType,
			SeriesID:             nullStr(raw.SeriesID),
			RecurringRuleID:      nullStr(raw.RecurringRuleID),
			ParentRuleID:         nullStr(raw.ParentRuleID),
			TransactionID:        raw.TransactionID,
			RecurrenceSequence:   coalesceSeq(raw.RecurrenceSequence),
			TransactionType:      raw.TransactionType,
			Status:               raw.Status,
			IsRecurringException: raw.IsRecurringException,
			ReasonCode:           ReasonSequenceOutsideRuleWindow,
			SemanticState:        semState,
			AuditOutcome:         AuditInconsistentHumanReview,
			Deterministic:        false,
			AuditedField:         "recurrence_sequence",
		})
	}

	// 7. Parent rule validation
	if raw.ParentRuleID.Valid && raw.ParentRuleID.String != "" {
		if !raw.ParentExists {
			findings = append(findings, auditFinding{
				WorkspaceID:          raw.WorkspaceID,
				WorkspaceType:        raw.WorkspaceType,
				SeriesID:             nullStr(raw.SeriesID),
				RecurringRuleID:      nullStr(raw.RecurringRuleID),
				ParentRuleID:         nullStr(raw.ParentRuleID),
				TransactionID:        raw.TransactionID,
				RecurrenceSequence:   coalesceSeq(raw.RecurrenceSequence),
				TransactionType:      raw.TransactionType,
				Status:               raw.Status,
				IsRecurringException: raw.IsRecurringException,
				ReasonCode:           ReasonParentRuleNotFound,
				SemanticState:        semState,
				AuditOutcome:         AuditInconsistentHumanReview,
				Deterministic:        false,
				AuditedField:         "parent_rule",
			})
		} else if raw.ParentRuleID.String == raw.RecurringRuleID.String {
			findings = append(findings, auditFinding{
				WorkspaceID:          raw.WorkspaceID,
				WorkspaceType:        raw.WorkspaceType,
				SeriesID:             nullStr(raw.SeriesID),
				RecurringRuleID:      nullStr(raw.RecurringRuleID),
				ParentRuleID:         nullStr(raw.ParentRuleID),
				TransactionID:        raw.TransactionID,
				RecurrenceSequence:   coalesceSeq(raw.RecurrenceSequence),
				TransactionType:      raw.TransactionType,
				Status:               raw.Status,
				IsRecurringException: raw.IsRecurringException,
				ReasonCode:           ReasonParentRuleStructureInvalid,
				SemanticState:        semState,
				AuditOutcome:         AuditInconsistentHumanReview,
				Deterministic:        false,
				AuditedField:         "parent_rule",
			})
		} else if raw.ParentWorkspaceID.Valid && raw.ParentWorkspaceID.String != raw.WorkspaceID {
			findings = append(findings, auditFinding{
				WorkspaceID:          raw.WorkspaceID,
				WorkspaceType:        raw.WorkspaceType,
				SeriesID:             nullStr(raw.SeriesID),
				RecurringRuleID:      nullStr(raw.RecurringRuleID),
				ParentRuleID:         nullStr(raw.ParentRuleID),
				TransactionID:        raw.TransactionID,
				RecurrenceSequence:   coalesceSeq(raw.RecurrenceSequence),
				TransactionType:      raw.TransactionType,
				Status:               raw.Status,
				IsRecurringException: raw.IsRecurringException,
				ReasonCode:           ReasonCrossWorkspaceReference,
				SemanticState:        semState,
				AuditOutcome:         AuditInconsistentHumanReview,
				Deterministic:        false,
				AuditedField:         "parent_rule",
			})
		}
	}

	// 8. Invoice structure validation
	if finding := classifyInvoice(raw, semState); finding != nil {
		findings = append(findings, *finding)
	}

	// 9. Exception structure validation
	if finding := classifyExceptionStructure(raw, semState); finding != nil {
		findings = append(findings, *finding)
	}

	// 10. Civil date checks
	findings = append(findings, classifyDateCivil(raw, semState)...)
	findings = append(findings, classifyDueDateCivil(raw, semState)...)

	if len(findings) == 0 {
		findings = append(findings, auditFinding{
			WorkspaceID:          raw.WorkspaceID,
			WorkspaceType:        raw.WorkspaceType,
			SeriesID:             nullStr(raw.SeriesID),
			RecurringRuleID:      nullStr(raw.RecurringRuleID),
			ParentRuleID:         nullStr(raw.ParentRuleID),
			TransactionID:        raw.TransactionID,
			RecurrenceSequence:   coalesceSeq(raw.RecurrenceSequence),
			TransactionType:      raw.TransactionType,
			Status:               raw.Status,
			IsRecurringException: raw.IsRecurringException,
			ReasonCode:           "",
			DateCivilPresent:     raw.DateCivil.Valid,
			DueDatePresent:       raw.DueDateUnix.Valid,
			SemanticState:        semState,
			AuditOutcome:         AuditValid,
			Deterministic:        false,
			AuditedField:         "all_valid",
		})
	}

	return findings
}

func classifyDateCivil(raw RawAuditRow, semState string) []auditFinding {
	cl := NewCivilDateClassifierNoTimezone()
	c := cl.ClassifyPair("date_civil", raw.DateUnix, raw.DateCivil)

	if c == nil {
		return nil
	}

	f := auditFinding{
		WorkspaceID:          raw.WorkspaceID,
		WorkspaceType:        raw.WorkspaceType,
		SeriesID:             nullStr(raw.SeriesID),
		RecurringRuleID:      nullStr(raw.RecurringRuleID),
		ParentRuleID:         nullStr(raw.ParentRuleID),
		TransactionID:        raw.TransactionID,
		RecurrenceSequence:   coalesceSeq(raw.RecurrenceSequence),
		TransactionType:      raw.TransactionType,
		Status:               raw.Status,
		IsRecurringException: raw.IsRecurringException,
		SemanticState:        semState,
		AuditedField:         "date_civil",
		DateCivilPresent:     raw.DateCivil.Valid,
		DueDatePresent:       raw.DueDateUnix.Valid,
		ReasonCode:           c.ReasonCode,
		AuditOutcome:         c.AuditOutcome,
		Deterministic:        c.Deterministic,
		ProposedCivilValue:   c.ProposedUTCValue,
	}

	if c.ReasonCode == "" || (c.ReasonCode == ReasonLegacyDateMissing && raw.DateCivil.Valid) {
		return nil
	}
	return []auditFinding{f}
}

func classifyDueDateCivil(raw RawAuditRow, semState string) []auditFinding {
	cl := NewCivilDateClassifierNoTimezone()
	var dueUnix int64
	if raw.DueDateUnix.Valid {
		dueUnix = raw.DueDateUnix.Int64
	}
	c := cl.ClassifyPair("due_date_civil", dueUnix, raw.DueDateCivil)

	if c == nil || c.ReasonCode == "" {
		return nil
	}

	f := auditFinding{
		WorkspaceID:          raw.WorkspaceID,
		WorkspaceType:        raw.WorkspaceType,
		SeriesID:             nullStr(raw.SeriesID),
		RecurringRuleID:      nullStr(raw.RecurringRuleID),
		ParentRuleID:         nullStr(raw.ParentRuleID),
		TransactionID:        raw.TransactionID,
		RecurrenceSequence:   coalesceSeq(raw.RecurrenceSequence),
		TransactionType:      raw.TransactionType,
		Status:               raw.Status,
		IsRecurringException: raw.IsRecurringException,
		SemanticState:        semState,
		AuditedField:         "due_date_civil",
		DateCivilPresent:     raw.DateCivil.Valid,
		DueDatePresent:       raw.DueDateUnix.Valid,
		ReasonCode:           c.ReasonCode,
		AuditOutcome:         c.AuditOutcome,
		Deterministic:        c.Deterministic,
		ProposedCivilValue:   c.ProposedUTCValue,
	}
	return []auditFinding{f}
}

func PrintAuditReportText(report *AuditReport, w io.Writer) {
	s := report.Summary
	fmt.Fprintf(w, "=== AUDITORIA DE DATAS CIVIS (LEGACY RECORRENTE) ===\n\n")
	fmt.Fprintf(w, "Resumo:\n")
	fmt.Fprintf(w, "  Workspaces escaneados:  %d\n", s.WorkspacesScanned)
	fmt.Fprintf(w, "  Series escaneadas:      %d\n", s.SeriesScanned)
	fmt.Fprintf(w, "  Transacoes escaneadas:  %d\n", s.TransactionsScanned)
	fmt.Fprintf(w, "  Validas:                %d\n", s.Valid)
	fmt.Fprintf(w, "  Deterministicas:         %d\n", s.Deterministic)
	fmt.Fprintf(w, "  Revisao humana:          %d\n", s.HumanReview)
	fmt.Fprintln(w)

	if len(report.Findings) == 0 {
		fmt.Fprintln(w, "Nenhum achado acionavel.")
		return
	}

	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "Workspace\tType\tTransaction\tSeq\tField\tSemantic\tOutcome\tReason\tProposed\tInvoice")
	for _, f := range report.Findings {
		proposed := "-"
		if f.ProposedCivilValue != "" {
			proposed = f.ProposedCivilValue
		}
		invoice := "-"
		if f.InvoicePresent {
			invoice = f.InvoiceStatus
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\t%d\t%s\t%s\t%s\t%s\t%s\t%s\n",
			f.WorkspaceID, f.WorkspaceType, f.TransactionID,
			f.RecurrenceSequence, f.AuditedField,
			f.SemanticState, f.AuditOutcome, f.ReasonCode, proposed, invoice)
	}
	tw.Flush()
}

func PrintAuditReportJSON(report *AuditReport, w io.Writer) {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	enc.Encode(report)
}

func RunCivilDateLegacyAuditWithArgs(args []string, stdout, stderr io.Writer) int {
	databaseURL := ""
	format := "text"

	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--database-url":
			i++
			if i < len(args) {
				databaseURL = args[i]
			}
		case "--format":
			i++
			if i < len(args) {
				format = args[i]
			}
		case "-h", "--help":
			fmt.Fprintf(stdout, "Uso: admin civil-date-audit --database-url file:<caminho> [--format json|text]\n")
			fmt.Fprintf(stdout, "\nAudita ocorrencias civis em transacoes recorrentes (read-only).\n")
			fmt.Fprintf(stdout, "\nFlags:\n")
			fmt.Fprintf(stdout, "  --database-url  URL do banco SQLite existente (obrigatorio)\n")
			fmt.Fprintf(stdout, "  --format        json ou text (padrao: text)\n")
			fmt.Fprintf(stdout, "\nExit codes:\n")
			fmt.Fprintf(stdout, "  0 = nenhum achado acionavel\n")
			fmt.Fprintf(stdout, "  2 = achados deterministicos somente\n")
			fmt.Fprintf(stdout, "  3 = ao menos um achado que requer revisao humana\n")
			fmt.Fprintf(stdout, "  1 = falha operacional\n")
			return 0
		}
	}

	if strings.TrimSpace(databaseURL) == "" {
		fmt.Fprintf(stderr, "Erro: --database-url e obrigatorio\n")
		return 1
	}

	if format != "text" && format != "json" {
		fmt.Fprintf(stderr, "Erro: --format deve ser 'text' ou 'json'\n")
		return 1
	}

	db, _, err := OpenExistingForMaintenanceReadOnly(databaseURL)
	if err != nil {
		fmt.Fprintf(stderr, "Erro ao abrir banco: %v\n", err)
		return 1
	}
	defer db.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	report, exitCode, err := RunCivilDateLegacyAudit(ctx, db, format)
	if err != nil {
		fmt.Fprintf(stderr, "Erro na auditoria: %v\n", err)
		return 1
	}

	switch format {
	case "json":
		PrintAuditReportJSON(report, stdout)
	default:
		PrintAuditReportText(report, stdout)
	}

	return exitCode
}
