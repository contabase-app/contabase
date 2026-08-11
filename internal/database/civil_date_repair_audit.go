package database

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"sort"
	"strings"
	"text/tabwriter"
	"time"
)

const (
	repairSchemaVersion  = 1
	repairPageSize       = 256
)

var testRepairPaginationHook func()
var testRepairRollbackHook func(*sql.Tx) error
var testRepairPrimaryErrHook func() error

func repairRollback(tx *sql.Tx) error {
	if testRepairRollbackHook != nil {
		return testRepairRollbackHook(tx)
	}
	return tx.Rollback()
}

type RepairRawRow struct {
	Source             string
	SourceRowID        string
	WorkspaceID        string
	WorkspaceType      string
	TransactionID      sql.NullString
	SeriesID           sql.NullString
	RecurringRuleID    sql.NullString
	RecurrenceSequence sql.NullInt64
	TransactionType    sql.NullString
	Status             sql.NullString
	IsException        bool

	DateUnix     int64
	DateCivil    sql.NullString
	DueDateUnix  sql.NullInt64
	DueDateCivil sql.NullString
	PaidAt       sql.NullInt64
	PaidDateCiv  sql.NullString

	StartDateUnix  sql.NullInt64
	StartDateCivil sql.NullString
	StartsAtUnix   sql.NullInt64
	StartsAtCivil  sql.NullString

	RuleWorkspaceID   sql.NullString
	SeriesWorkspaceID sql.NullString
	RuleSeriesID      sql.NullString
	RuleActive        sql.NullInt64
}

type CivilDateRepairFinding struct {
	WorkspaceID         string `json:"workspace_id"`
	WorkspaceType       string `json:"workspace_type"`
	Source              string `json:"source"`
	SourceRowID         string `json:"source_row_id"`
	TransactionID       string `json:"transaction_id,omitempty"`
	SeriesID            string `json:"series_id,omitempty"`
	RecurringRuleID     string `json:"recurring_rule_id,omitempty"`
	RecurrenceSequence  int64  `json:"recurrence_sequence,omitempty"`
	TransactionType     string `json:"transaction_type,omitempty"`
	Status              string `json:"status,omitempty"`
	IsException         bool   `json:"is_recurring_exception,omitempty"`
	AuditedField        string `json:"audited_field"`
	ReasonCode          string `json:"reason_code"`
	ProposedCivilValue  string `json:"proposed_civil_value,omitempty"`
	CachedUTC           string `json:"cached_utc,omitempty"`
	CachedTimezone      string `json:"cached_timezone,omitempty"`
	TimezoneDivergent   bool   `json:"timezone_divergent,omitempty"`
	RepairOutcome       string `json:"repair_outcome"`
}

type repairSummary struct {
	TransactionsScanned int64 `json:"transactions_scanned"`
	RulesScanned        int64 `json:"rules_scanned"`
	Findings            int64 `json:"findings"`
	AutoRepair          int64 `json:"auto_repair"`
	HumanReview         int64 `json:"human_review"`
}

type CivilDateRepairReport struct {
	SchemaVersion int                     `json:"schema_version"`
	Timezone      string                  `json:"timezone"`
	Summary       repairSummary           `json:"summary"`
	Findings      []CivilDateRepairFinding `json:"findings"`
}

func RunCivilDateRepairAudit(ctx context.Context, db *sql.DB) (*CivilDateRepairReport, error) {
	if db == nil {
		return nil, fmt.Errorf("database connection is nil")
	}

	conn, err := db.Conn(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to get dedicated connection: %w", err)
	}
	defer conn.Close()

	if err := conn.PingContext(ctx); err != nil {
		return nil, fmt.Errorf("database ping failed: %w", err)
	}

	if _, err := conn.ExecContext(ctx, "PRAGMA query_only = ON"); err != nil {
		return nil, fmt.Errorf("failed to set query_only: %w", err)
	}
	var qo int
	if err := conn.QueryRowContext(ctx, "PRAGMA query_only").Scan(&qo); err != nil {
		return nil, fmt.Errorf("failed to verify query_only: %w", err)
	}
	if qo != 1 {
		return nil, fmt.Errorf("query_only is %d, expected 1", qo)
	}

	if err := quickCheckConn(ctx, conn); err != nil {
		return nil, fmt.Errorf("integrity check failed: %w", err)
	}

	tx, err := conn.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return nil, fmt.Errorf("failed to begin read-only transaction: %w", err)
	}

	var report *CivilDateRepairReport
	primaryErr := func() error {
		cl, clErr := NewCivilDateClassifier()
		if clErr != nil {
			return fmt.Errorf("failed to initialize classifier: %w", clErr)
		}
		tzName := civilRepairDefaultTimezone
		if cl.Location != nil {
			tzName = cl.Location.String()
		}

		report = &CivilDateRepairReport{
			SchemaVersion: repairSchemaVersion,
			Timezone:      tzName,
		}

		if aErr := appendRepairFindings(ctx, tx, cl, true, report); aErr != nil {
			return fmt.Errorf("transaction audit: %w", aErr)
		}
		if aErr := appendRepairFindings(ctx, tx, cl, false, report); aErr != nil {
			return fmt.Errorf("recurring rule audit: %w", aErr)
		}
		return nil
	}()
	rollbackErr := repairRollback(tx)

	if rollbackErr != nil && rollbackErr != sql.ErrTxDone {
		if primaryErr != nil {
			return nil, fmt.Errorf("audit error: %w; rollback error: %w", primaryErr, rollbackErr)
		}
		return nil, fmt.Errorf("failed to rollback read-only transaction: %w", rollbackErr)
	}
	if primaryErr != nil {
		return nil, primaryErr
	}

	sortRepairFindings(report.Findings)

	for _, f := range report.Findings {
		switch f.RepairOutcome {
		case CivilRepairOutcomeAutoRepair:
			report.Summary.AutoRepair++
			report.Summary.Findings++
		case CivilRepairOutcomeHumanReview:
			report.Summary.HumanReview++
			report.Summary.Findings++
		}
	}

	return report, nil
}

func appendRepairFindings(ctx context.Context, tx *sql.Tx, cl *CivilDateClassifier, isTransaction bool, report *CivilDateRepairReport) error {
	if testRepairPrimaryErrHook != nil {
		if err := testRepairPrimaryErrHook(); err != nil {
			return err
		}
	}

	var (
		cursorWS  string
		cursorID  string
	)

	for {
		var query string
		var args []interface{}
		if isTransaction {
			if cursorWS == "" {
				query = `
					SELECT
						'transaction' AS source,
						t.id AS source_row_id,
						t.workspace_id,
						COALESCE(w.type, 'personal'),
						t.id,
						t.series_id,
						t.recurring_rule_id,
						t.recurrence_sequence,
						t.type,
						t.status,
						t.date,
						t.date_civil,
						t.due_date,
						t.due_date_civil,
						t.paid_at,
						t.paid_date_civil,
						NULL, NULL, NULL, NULL,
						COALESCE(t.is_recurring_exception, 0),
						r.workspace_id,
						s.workspace_id,
						r.series_id,
						r.active
					FROM transactions t
					LEFT JOIN workspaces w ON w.id = t.workspace_id
					LEFT JOIN recurring_rules r ON r.id = t.recurring_rule_id
					LEFT JOIN recurring_series s ON s.id = t.series_id AND s.workspace_id = t.workspace_id
					WHERE t.recurring_rule_id IS NOT NULL
					ORDER BY t.workspace_id, t.id
					LIMIT ?
				`
				args = []interface{}{repairPageSize}
			} else {
				query = `
					SELECT
						'transaction' AS source,
						t.id AS source_row_id,
						t.workspace_id,
						COALESCE(w.type, 'personal'),
						t.id,
						t.series_id,
						t.recurring_rule_id,
						t.recurrence_sequence,
						t.type,
						t.status,
						t.date,
						t.date_civil,
						t.due_date,
						t.due_date_civil,
						t.paid_at,
						t.paid_date_civil,
						NULL, NULL, NULL, NULL,
						COALESCE(t.is_recurring_exception, 0),
						r.workspace_id,
						s.workspace_id,
						r.series_id,
						r.active
					FROM transactions t
					LEFT JOIN workspaces w ON w.id = t.workspace_id
					LEFT JOIN recurring_rules r ON r.id = t.recurring_rule_id
					LEFT JOIN recurring_series s ON s.id = t.series_id AND s.workspace_id = t.workspace_id
					WHERE t.recurring_rule_id IS NOT NULL
					  AND (t.workspace_id > ? OR (t.workspace_id = ? AND t.id > ?))
					ORDER BY t.workspace_id, t.id
					LIMIT ?
				`
				args = []interface{}{cursorWS, cursorWS, cursorID, repairPageSize}
			}
		} else {
			if cursorWS == "" {
				query = `
					SELECT
						'recurring_rule' AS source,
						r.id AS source_row_id,
						r.workspace_id,
						COALESCE(w.type, 'personal'),
						NULL, NULL, r.id, NULL, NULL, NULL,
						0, NULL, NULL, NULL, NULL, NULL,
						r.start_date, r.start_date_civil,
						r.starts_at, r.starts_at_civil,
						0, NULL, NULL, NULL, NULL
					FROM recurring_rules r
					LEFT JOIN workspaces w ON w.id = r.workspace_id
					ORDER BY r.workspace_id, r.id
					LIMIT ?
				`
				args = []interface{}{repairPageSize}
			} else {
				query = `
					SELECT
						'recurring_rule' AS source,
						r.id AS source_row_id,
						r.workspace_id,
						COALESCE(w.type, 'personal'),
						NULL, NULL, r.id, NULL, NULL, NULL,
						0, NULL, NULL, NULL, NULL, NULL,
						r.start_date, r.start_date_civil,
						r.starts_at, r.starts_at_civil,
						0, NULL, NULL, NULL, NULL
					FROM recurring_rules r
					LEFT JOIN workspaces w ON w.id = r.workspace_id
					WHERE r.workspace_id > ? OR (r.workspace_id = ? AND r.id > ?)
					ORDER BY r.workspace_id, r.id
					LIMIT ?
				`
				args = []interface{}{cursorWS, cursorWS, cursorID, repairPageSize}
			}
		}

		rows, err := tx.QueryContext(ctx, query, args...)
		if err != nil {
			return fmt.Errorf("query failed: %w", err)
		}

		rowCount := 0
		for rows.Next() {
			var r RepairRawRow
			if err := rows.Scan(
				&r.Source, &r.SourceRowID, &r.WorkspaceID, &r.WorkspaceType,
				&r.TransactionID, &r.SeriesID, &r.RecurringRuleID, &r.RecurrenceSequence,
				&r.TransactionType, &r.Status,
				&r.DateUnix, &r.DateCivil, &r.DueDateUnix, &r.DueDateCivil,
				&r.PaidAt, &r.PaidDateCiv,
				&r.StartDateUnix, &r.StartDateCivil, &r.StartsAtUnix, &r.StartsAtCivil,
				&r.IsException,
				&r.RuleWorkspaceID, &r.SeriesWorkspaceID, &r.RuleSeriesID, &r.RuleActive,
			); err != nil {
				rows.Close()
				return fmt.Errorf("row scan failed: %w", err)
			}

			cursorWS = r.WorkspaceID
			cursorID = r.SourceRowID
			rowCount++

			if isTransaction {
				report.Summary.TransactionsScanned++
			} else {
				report.Summary.RulesScanned++
			}

			var findings []CivilDateRepairFinding
			if isTransaction {
				findings = classifyRepairTransaction(r, cl)
			} else {
				findings = classifyRepairRule(r, cl)
			}
			report.Findings = append(report.Findings, findings...)
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return err
		}
		rows.Close()

		if testRepairPaginationHook != nil {
			testRepairPaginationHook()
		}

		if rowCount == 0 {
			break
		}
	}

	return nil
}

func classifyRepairTransaction(raw RepairRawRow, cl *CivilDateClassifier) []CivilDateRepairFinding {
	var findings []CivilDateRepairFinding

	appendPair := func(c *CivilPairClassification, fieldName string) {
		if c == nil || c.ReasonCode == "" {
			return
		}
		findings = append(findings, repairFindingFromPair(raw, c, fieldName))
	}

	appendStructural := func(reasonCode, fieldName string) {
		c := &CivilPairClassification{
			FieldName:     fieldName,
			ReasonCode:    reasonCode,
			AuditOutcome:  AuditInconsistentHumanReview,
			Deterministic: false,
			RepairOutcome: CivilRepairOutcomeHumanReview,
		}
		findings = append(findings, repairFindingFromPair(raw, c, fieldName))
	}

	blocked := false

	if raw.RecurringRuleID.Valid && raw.RecurringRuleID.String != "" {
		if !raw.RuleWorkspaceID.Valid {
			appendStructural(ReasonRuleNotFound, "recurring_rule")
			blocked = true
		} else if raw.RuleWorkspaceID.String != raw.WorkspaceID {
			appendStructural(ReasonCrossWorkspaceReference, "recurring_rule")
			blocked = true
		}
	}

	if !blocked && raw.SeriesID.Valid && raw.SeriesID.String != "" {
		if !raw.SeriesWorkspaceID.Valid {
			appendStructural(ReasonSeriesNotFound, "recurring_series")
			blocked = true
		} else if raw.SeriesWorkspaceID.String != raw.WorkspaceID {
			appendStructural(ReasonCrossWorkspaceReference, "recurring_series")
			blocked = true
		}
	}

	if !blocked && raw.RuleSeriesID.Valid && raw.SeriesID.Valid &&
		raw.RuleSeriesID.String != raw.SeriesID.String {
		appendStructural(ReasonSeriesNotFound, "rule_series_mismatch")
		blocked = true
	}

	if blocked {
		return findings
	}

	appendPair(cl.ClassifyPair("date_civil", raw.DateUnix, raw.DateCivil), "date_civil")

	var dueUnix int64
	if raw.DueDateUnix.Valid {
		dueUnix = raw.DueDateUnix.Int64
	}
	appendPair(cl.ClassifyPair("due_date_civil", dueUnix, raw.DueDateCivil), "due_date_civil")

	if raw.Status.Valid {
		appendPair(ClassifyPaidDateCivil(raw.Status.String, raw.PaidAt, raw.PaidDateCiv), "paid_date_civil")
	}

	return findings
}

func classifyRepairRule(raw RepairRawRow, cl *CivilDateClassifier) []CivilDateRepairFinding {
	var findings []CivilDateRepairFinding

	appendPair := func(c *CivilPairClassification, fieldName string) {
		if c == nil || c.ReasonCode == "" {
			return
		}
		findings = append(findings, repairFindingFromPair(raw, c, fieldName))
	}

	var startUnix int64
	if raw.StartDateUnix.Valid {
		startUnix = raw.StartDateUnix.Int64
	}
	appendPair(cl.ClassifyPair("start_date_civil", startUnix, raw.StartDateCivil), "start_date_civil")

	var startsAtUnix int64
	if raw.StartsAtUnix.Valid {
		startsAtUnix = raw.StartsAtUnix.Int64
	}
	appendPair(cl.ClassifyPair("starts_at_civil", startsAtUnix, raw.StartsAtCivil), "starts_at_civil")

	return findings
}

func repairFindingFromPair(raw RepairRawRow, c *CivilPairClassification, fieldName string) CivilDateRepairFinding {
	return CivilDateRepairFinding{
		WorkspaceID:        raw.WorkspaceID,
		WorkspaceType:      raw.WorkspaceType,
		Source:             raw.Source,
		SourceRowID:        raw.SourceRowID,
		TransactionID:      nullStrRef(raw.TransactionID),
		SeriesID:           nullStrRef(raw.SeriesID),
		RecurringRuleID:    nullStrRef(raw.RecurringRuleID),
		RecurrenceSequence: coalesceSeq(raw.RecurrenceSequence),
		TransactionType:    nullStrRef(raw.TransactionType),
		Status:             nullStrRef(raw.Status),
		IsException:        raw.IsException,
		AuditedField:       fieldName,
		ReasonCode:         c.ReasonCode,
		ProposedCivilValue: c.ProposedUTCValue,
		CachedUTC:          c.CachedUTC,
		CachedTimezone:     c.CachedTimezone,
		TimezoneDivergent:  c.TimezoneDivergent,
		RepairOutcome:      c.RepairOutcome,
	}
}

func sortRepairFindings(findings []CivilDateRepairFinding) {
	sort.Slice(findings, func(i, j int) bool {
		a, b := findings[i], findings[j]
		if a.WorkspaceID != b.WorkspaceID {
			return a.WorkspaceID < b.WorkspaceID
		}
		if a.Source != b.Source {
			return a.Source < b.Source
		}
		if a.SourceRowID != b.SourceRowID {
			return a.SourceRowID < b.SourceRowID
		}
		return a.AuditedField < b.AuditedField
	})
}

func PrintCivilDateRepairReportText(report *CivilDateRepairReport, w io.Writer) {
	s := report.Summary
	fmt.Fprintf(w, "=== AUDITORIA DE DATAS CIVIS — REPAIR PREVIEW (READ-ONLY) ===\n\n")
	fmt.Fprintf(w, "Timezone: %s\n", report.Timezone)
	fmt.Fprintf(w, "Resumo:\n")
	fmt.Fprintf(w, "  Transacoes escaneadas:  %d\n", s.TransactionsScanned)
	fmt.Fprintf(w, "  Regras escaneadas:      %d\n", s.RulesScanned)
	fmt.Fprintf(w, "  Achados:                %d\n", s.Findings)
	fmt.Fprintf(w, "  Auto-reparo:            %d\n", s.AutoRepair)
	fmt.Fprintf(w, "  Revisao humana:         %d\n", s.HumanReview)
	fmt.Fprintln(w)

	if len(report.Findings) == 0 {
		fmt.Fprintln(w, "Nenhum achado.")
		return
	}

	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "Workspace\tType\tSource\tID\tField\tReason\tUTC\tTZ\tDivergent\tProposed\tOutcome")
	for _, f := range report.Findings {
		utc := f.CachedUTC
		if utc == "" {
			utc = "-"
		}
		tz := f.CachedTimezone
		if tz == "" {
			tz = "-"
		}
		divergent := "N"
		if f.TimezoneDivergent {
			divergent = "Y"
		}
		proposed := f.ProposedCivilValue
		if proposed == "" {
			proposed = "-"
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\n",
			f.WorkspaceID, f.WorkspaceType, f.Source, f.SourceRowID,
			f.AuditedField, f.ReasonCode, utc, tz, divergent, proposed, f.RepairOutcome)
	}
	tw.Flush()
}

func PrintCivilDateRepairReportJSON(report *CivilDateRepairReport, w io.Writer) {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	enc.Encode(report)
}

func RunCivilDateRepairAuditWithArgs(args []string, stdout, stderr io.Writer) int {
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
			fmt.Fprintf(stdout, "Uso: admin civil-date-repair audit --database-url file:<caminho> [--format json|text]\n")
			fmt.Fprintf(stdout, "\nAudita pares (legacy INTEGER, civil TEXT) e classifica a reparabilidade (read-only).\n")
			fmt.Fprintf(stdout, "\nFlags:\n")
			fmt.Fprintf(stdout, "  --database-url  URL do banco SQLite existente (obrigatorio)\n")
			fmt.Fprintf(stdout, "  --format        json ou text (padrao: text)\n")
			fmt.Fprintf(stdout, "\nExit codes:\n")
			fmt.Fprintf(stdout, "  0 = nenhum achado\n")
			fmt.Fprintf(stdout, "  2 = apenas auto-reparo\n")
			fmt.Fprintf(stdout, "  3 = ao menos um que requer revisao humana\n")
			fmt.Fprintf(stdout, "  1 = falha operacional\n")
			return 0
		default:
			if strings.HasPrefix(args[i], "--") {
				fmt.Fprintf(stderr, "Erro: flag desconhecida: %s\n", args[i])
				return 1
			}
			fmt.Fprintf(stderr, "Erro: argumento nao reconhecido: %s\n", args[i])
			return 1
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

	report, err := RunCivilDateRepairAudit(ctx, db)
	if err != nil {
		fmt.Fprintf(stderr, "Erro na auditoria: %v\n", err)
		return 1
	}

	switch format {
	case "json":
		PrintCivilDateRepairReportJSON(report, stdout)
	default:
		PrintCivilDateRepairReportText(report, stdout)
	}

	exitCode := 0
	if report.Summary.HumanReview > 0 {
		exitCode = 3
	} else if report.Summary.AutoRepair > 0 {
		exitCode = 2
	}
	return exitCode
}
