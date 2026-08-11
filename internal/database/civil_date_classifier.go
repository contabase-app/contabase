package database

import (
	"database/sql"
	"fmt"
	"os"
	"sort"
	"strings"
	"time"
	_ "time/tzdata"

	"github.com/contabase-app/contabase/internal/civildate"
)

// ---------- Timezone ----------

const civilRepairDefaultTimezone = "America/Sao_Paulo"

func resolveCivilRepairTimezone() (*time.Location, error) {
	tz := strings.TrimSpace(os.Getenv("CONTABASE_TIMEZONE"))
	if tz == "" {
		tz = strings.TrimSpace(os.Getenv("TZ"))
	}
	if tz == "" {
		tz = civilRepairDefaultTimezone
	}
	loc, err := time.LoadLocation(tz)
	if err != nil {
		return nil, fmt.Errorf("timezone %q: %w", tz, err)
	}
	return loc, nil
}

// ---------- Shared constants ----------

const (
	ReasonDateCivilMissing         = "DATE_CIVIL_MISSING"
	ReasonDateCivilInvalid         = "DATE_CIVIL_INVALID"
	ReasonDateCivilMismatch        = "DATE_CIVIL_MISMATCH"
	ReasonDueDateCivilMissing      = "DUE_DATE_CIVIL_MISSING"
	ReasonDueDateCivilInvalid      = "DUE_DATE_CIVIL_INVALID"
	ReasonDueDateCivilMismatch     = "DUE_DATE_CIVIL_MISMATCH"
	ReasonStartDateCivilMissing    = "START_DATE_CIVIL_MISSING"
	ReasonStartDateCivilInvalid    = "START_DATE_CIVIL_INVALID"
	ReasonStartDateCivilMismatch   = "START_DATE_CIVIL_MISMATCH"
	ReasonStartsAtCivilMissing     = "STARTS_AT_CIVIL_MISSING"
	ReasonStartsAtCivilInvalid     = "STARTS_AT_CIVIL_INVALID"
	ReasonStartsAtCivilMismatch    = "STARTS_AT_CIVIL_MISMATCH"
	ReasonPaidDateUnknown          = "HUMAN_REVIEW_PAID_DATE"
	ReasonPaidDateMismatch         = "HUMAN_REVIEW_PAID_DATE_MISMATCH"
	ReasonLegacyDateMissing        = "LEGACY_DATE_MISSING"
	ReasonTimezoneAmbiguous        = "HUMAN_REVIEW_TIMEZONE_AMBIGUOUS"
	ReasonPaidDateStructural       = "PAID_DATE_STRUCTURAL_INVALID"
	ReasonRuleNotFound             = "RULE_NOT_FOUND"
	ReasonSeriesNotFound           = "SERIES_NOT_FOUND"
	ReasonCrossWorkspaceReference  = "CROSS_WORKSPACE_REFERENCE"
	ReasonSequenceInvalid          = "SEQUENCE_INVALID"
	ReasonSequenceDuplicate        = "SEQUENCE_DUPLICATE"
	ReasonSequenceOutsideRuleWindow = "SEQUENCE_OUTSIDE_RULE_WINDOW"
	ReasonInvoiceStructureInvalid  = "INVOICE_STRUCTURE_INVALID"
	ReasonExceptionStructureInvalid = "EXCEPTION_STRUCTURE_INVALID"
	ReasonParentRuleNotFound       = "PARENT_RULE_NOT_FOUND"
	ReasonParentRuleStructureInvalid = "PARENT_RULE_STRUCTURE_INVALID"
)

const (
	SemanticNormalOccurrence     = "NORMAL_OCCURRENCE"
	SemanticManualException      = "MANUAL_EXCEPTION"
	SemanticHistoricalDivergence = "HISTORICAL_DIVERGENCE"
)

const (
	AuditValid                         = "VALID"
	AuditLegacyIncompleteDeterministic = "LEGACY_INCOMPLETE_DETERMINISTIC"
	AuditInconsistentHumanReview       = "INCONSISTENT_HUMAN_REVIEW"
)

const (
	CivilRepairOutcomeAutoRepair = "AUTO_REPAIR"
	CivilRepairOutcomeHumanReview = "HUMAN_REVIEW"
	CivilRepairOutcomeValid       = "VALID"
)

const legacyDivergenceReason = "C0.2 legacy date divergence"

// ---------- Shared helpers ----------

func validCivilFormat(s string) bool {
	if len(s) != 10 {
		return false
	}
	if s[4] != '-' || s[7] != '-' {
		return false
	}
	_, err := civildate.ParseCivilDateInput(s)
	return err == nil
}

func civilDatesEqual(a, b *civildate.CivilDate) bool {
	return a.String() == b.String()
}

func coalesceSeq(ns sql.NullInt64) int64 {
	if ns.Valid {
		return ns.Int64
	}
	return 0
}

func nullStr(ns sql.NullString) string {
	if ns.Valid {
		return ns.String
	}
	return ""
}

func nullStrRef(ns sql.NullString) string {
	if ns.Valid {
		return ns.String
	}
	return ""
}

func sortFindings(findings []auditFinding) {
	sort.Slice(findings, func(i, j int) bool {
		a, b := findings[i], findings[j]
		if a.WorkspaceID != b.WorkspaceID {
			return a.WorkspaceID < b.WorkspaceID
		}
		if a.SeriesID != b.SeriesID {
			return a.SeriesID < b.SeriesID
		}
		if a.RecurrenceSequence != b.RecurrenceSequence {
			return a.RecurrenceSequence < b.RecurrenceSequence
		}
		if a.TransactionID != b.TransactionID {
			return a.TransactionID < b.TransactionID
		}
		return a.AuditedField < b.AuditedField
	})
}

// ---------- Civil pair classification ----------

type CivilPairClassification struct {
	FieldName          string
	LegacyValue        int64
	CivilPresent       bool
	CivilFormatValid   bool
	CachedUTC          string
	CachedTimezone     string
	TimezoneDivergent  bool
	ProposedUTCValue   string
	ReasonCode         string
	AuditOutcome       string
	Deterministic      bool
	RepairOutcome      string
}

func ClassifyCivilPair(fieldName string, legacyUnix int64, civilStr sql.NullString, loc *time.Location) *CivilPairClassification {
	c := &CivilPairClassification{
		FieldName:   fieldName,
		LegacyValue: legacyUnix,
	}

	if legacyUnix <= 0 {
		if !civilStr.Valid {
			if fieldName == "date_civil" {
				c.ReasonCode = ReasonLegacyDateMissing
				c.AuditOutcome = AuditInconsistentHumanReview
				c.Deterministic = false
				c.RepairOutcome = CivilRepairOutcomeHumanReview
				return c
			}
			return nil
		}
		c.CivilPresent = true
		c.CivilFormatValid = validCivilFormat(civilStr.String)
		if fieldName != "date_civil" {
			switch fieldName {
			case "due_date_civil":
				c.ReasonCode = ReasonDueDateCivilMismatch
			case "start_date_civil":
				c.ReasonCode = ReasonStartDateCivilMismatch
			case "starts_at_civil":
				c.ReasonCode = ReasonStartsAtCivilMismatch
			}
			c.AuditOutcome = AuditInconsistentHumanReview
			c.Deterministic = false
			c.RepairOutcome = CivilRepairOutcomeHumanReview
		}
		return c
	}

	c.CachedUTC = civildate.FormatCivilDate(civildate.LegacyUnixToCivilDateUTC(legacyUnix))
	if loc != nil {
		tzDate, err := civildate.LegacyUnixToCivilDateInLocation(legacyUnix, loc)
		if err == nil {
			c.CachedTimezone = civildate.FormatCivilDate(tzDate)
			c.TimezoneDivergent = c.CachedUTC != c.CachedTimezone
		}
	}

	if !civilStr.Valid {
		c.CivilPresent = false

		if loc != nil && c.TimezoneDivergent {
			c.ReasonCode = ReasonTimezoneAmbiguous
			c.AuditOutcome = AuditInconsistentHumanReview
			c.Deterministic = false
			c.RepairOutcome = CivilRepairOutcomeHumanReview
			c.ProposedUTCValue = ""
			return c
		}

		c.ProposedUTCValue = c.CachedUTC
		c.Deterministic = true
		c.AuditOutcome = AuditLegacyIncompleteDeterministic

		switch fieldName {
		case "due_date_civil":
			c.ReasonCode = ReasonDueDateCivilMissing
		case "start_date_civil":
			c.ReasonCode = ReasonStartDateCivilMissing
		case "starts_at_civil":
			c.ReasonCode = ReasonStartsAtCivilMissing
		default:
			c.ReasonCode = ReasonDateCivilMissing
		}

		if isAutoRepairField(fieldName) {
			c.RepairOutcome = CivilRepairOutcomeAutoRepair
		} else {
			c.RepairOutcome = CivilRepairOutcomeHumanReview
		}
		return c
	}

	c.CivilPresent = true
	civilVal := civilStr.String
	if !validCivilFormat(civilVal) {
		c.CivilFormatValid = false
		c.Deterministic = false
		c.AuditOutcome = AuditInconsistentHumanReview
		c.RepairOutcome = CivilRepairOutcomeHumanReview

		switch fieldName {
		case "due_date_civil":
			c.ReasonCode = ReasonDueDateCivilInvalid
		case "start_date_civil":
			c.ReasonCode = ReasonStartDateCivilInvalid
		case "starts_at_civil":
			c.ReasonCode = ReasonStartsAtCivilInvalid
		default:
			c.ReasonCode = ReasonDateCivilInvalid
		}
		return c
	}

	c.CivilFormatValid = true
	expected := civildate.LegacyUnixToCivilDateUTC(legacyUnix)
	civilParsed, err := civildate.ParseCivilDateInput(civilVal)
	if err != nil || !civilDatesEqual(&expected, &civilParsed) {
		c.Deterministic = false
		c.AuditOutcome = AuditInconsistentHumanReview
		c.RepairOutcome = CivilRepairOutcomeHumanReview
		c.ProposedUTCValue = civildate.FormatCivilDate(expected)

		switch fieldName {
		case "due_date_civil":
			c.ReasonCode = ReasonDueDateCivilMismatch
		case "start_date_civil":
			c.ReasonCode = ReasonStartDateCivilMismatch
		case "starts_at_civil":
			c.ReasonCode = ReasonStartsAtCivilMismatch
		default:
			c.ReasonCode = ReasonDateCivilMismatch
		}

		if c.TimezoneDivergent {
			c.ReasonCode = ReasonTimezoneAmbiguous
		}
		return c
	}

	c.AuditOutcome = AuditValid
	c.RepairOutcome = CivilRepairOutcomeValid
	return c
}

func isAutoRepairField(fieldName string) bool {
	switch fieldName {
	case "date_civil", "due_date_civil", "start_date_civil", "starts_at_civil":
		return true
	default:
		return false
	}
}

// ---------- Paid date civil classification (audit-only) ----------

func ClassifyPaidDateCivil(paymentStatus string, paidAt sql.NullInt64, paidDateCivil sql.NullString) *CivilPairClassification {
	c := &CivilPairClassification{FieldName: "paid_date_civil"}
	status := strings.ToLower(strings.TrimSpace(paymentStatus))

	if status == "pending" {
		if paidAt.Valid && paidAt.Int64 > 0 {
			c.ReasonCode = ReasonPaidDateStructural
			c.AuditOutcome = AuditInconsistentHumanReview
			c.Deterministic = false
			c.RepairOutcome = CivilRepairOutcomeHumanReview
			return c
		}
		if paidDateCivil.Valid {
			c.ReasonCode = ReasonPaidDateStructural
			c.AuditOutcome = AuditInconsistentHumanReview
			c.Deterministic = false
			c.RepairOutcome = CivilRepairOutcomeHumanReview
			return c
		}
		return nil
	}

	if status == "paid" {
		c.CivilPresent = paidDateCivil.Valid
		if paidAt.Valid && paidAt.Int64 > 0 && paidDateCivil.Valid {
			expected := civildate.FormatCivilDate(civildate.LegacyUnixToCivilDateUTC(paidAt.Int64))
			if paidDateCivil.String == expected {
				c.CivilFormatValid = true
				c.AuditOutcome = AuditValid
				c.RepairOutcome = CivilRepairOutcomeValid
				return c
			}
			c.ReasonCode = ReasonPaidDateMismatch
			c.ProposedUTCValue = expected
			c.AuditOutcome = AuditInconsistentHumanReview
			c.Deterministic = false
			c.RepairOutcome = CivilRepairOutcomeHumanReview
			return c
		}
		if paidAt.Valid && paidAt.Int64 > 0 && !paidDateCivil.Valid {
			c.ReasonCode = ReasonPaidDateUnknown
			c.AuditOutcome = AuditInconsistentHumanReview
			c.Deterministic = true
			c.RepairOutcome = CivilRepairOutcomeHumanReview
			return c
		}
		if (!paidAt.Valid || paidAt.Int64 <= 0) && !paidDateCivil.Valid {
			return nil
		}
		c.ReasonCode = ReasonPaidDateStructural
		c.AuditOutcome = AuditInconsistentHumanReview
		c.Deterministic = false
		c.RepairOutcome = CivilRepairOutcomeHumanReview
		return c
	}

	return nil
}

// ---------- Structural classification ----------

func classifySemanticState(raw RawAuditRow) string {
	if raw.IsRecurringException {
		if raw.RecurringExceptionReason.Valid &&
			raw.RecurringExceptionReason.String == legacyDivergenceReason {
			return SemanticHistoricalDivergence
		}
		return SemanticManualException
	}
	return SemanticNormalOccurrence
}

func ruleNotFound(raw RawAuditRow) bool {
	return !raw.RecurringRuleID.Valid || raw.RecurringRuleID.String == "" ||
		!raw.RuleWorkspaceID.Valid
}

func crossWorkspace(raw RawAuditRow) bool {
	if raw.RuleWorkspaceID.Valid && raw.RuleWorkspaceID.String != raw.WorkspaceID {
		return true
	}
	if raw.SeriesWorkspaceID.Valid && raw.SeriesWorkspaceID.String != raw.WorkspaceID {
		return true
	}
	return false
}

func seriesNotFound(raw RawAuditRow) bool {
	return raw.SeriesID.Valid && raw.SeriesID.String != "" &&
		(!raw.SeriesWorkspaceID.Valid || raw.SeriesWorkspaceID.String == "")
}

func isSequenceDuplicate(raw RawAuditRow, dups map[seqDupKey]bool) bool {
	if !raw.SeriesID.Valid || !raw.RecurrenceSequence.Valid {
		return false
	}
	return dups[seqDupKey{raw.WorkspaceID, raw.SeriesID.String, raw.RecurrenceSequence.Int64}]
}

func isSequenceOutsideWindow(raw RawAuditRow) bool {
	if !raw.RecurrenceSequence.Valid {
		return false
	}
	seq := raw.RecurrenceSequence.Int64
	if raw.RuleEffectiveStartSeq.Valid && seq < raw.RuleEffectiveStartSeq.Int64 {
		return true
	}
	if raw.RuleEffectiveEndSeq.Valid && seq > raw.RuleEffectiveEndSeq.Int64 {
		return true
	}
	return false
}

func classifyInvoice(raw RawAuditRow, semState string) *auditFinding {
	if !raw.InvoiceID.Valid {
		return nil
	}
	if !raw.InvoiceStatus.Valid {
		return &auditFinding{
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
			ReasonCode:           ReasonInvoiceStructureInvalid,
			InvoicePresent:       true,
			SemanticState:        semState,
			AuditOutcome:         AuditInconsistentHumanReview,
			Deterministic:        false,
			AuditedField:         "invoice",
		}
	}
	switch raw.InvoiceStatus.String {
	case "OPEN", "CLOSED", "PAID":
		return nil
	default:
		return &auditFinding{
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
			ReasonCode:           ReasonInvoiceStructureInvalid,
			InvoicePresent:       true,
			InvoiceStatus:        raw.InvoiceStatus.String,
			SemanticState:        semState,
			AuditOutcome:         AuditInconsistentHumanReview,
			Deterministic:        false,
			AuditedField:         "invoice",
		}
	}
}

func classifyExceptionStructure(raw RawAuditRow, semState string) *auditFinding {
	if raw.IsRecurringException {
		if !raw.RecurringExceptionReason.Valid || strings.TrimSpace(raw.RecurringExceptionReason.String) == "" {
			return &auditFinding{
				WorkspaceID:          raw.WorkspaceID,
				WorkspaceType:        raw.WorkspaceType,
				SeriesID:             nullStr(raw.SeriesID),
				RecurringRuleID:      nullStr(raw.RecurringRuleID),
				ParentRuleID:         nullStr(raw.ParentRuleID),
				TransactionID:        raw.TransactionID,
				RecurrenceSequence:   coalesceSeq(raw.RecurrenceSequence),
				TransactionType:      raw.TransactionType,
				Status:               raw.Status,
				IsRecurringException: true,
				ReasonCode:           ReasonExceptionStructureInvalid,
				SemanticState:        semState,
				AuditOutcome:         AuditInconsistentHumanReview,
				Deterministic:        false,
				AuditedField:         "exception",
			}
		}
	} else {
		if raw.RecurringExceptionReason.Valid && strings.TrimSpace(raw.RecurringExceptionReason.String) != "" {
			return &auditFinding{
				WorkspaceID:          raw.WorkspaceID,
				WorkspaceType:        raw.WorkspaceType,
				SeriesID:             nullStr(raw.SeriesID),
				RecurringRuleID:      nullStr(raw.RecurringRuleID),
				ParentRuleID:         nullStr(raw.ParentRuleID),
				TransactionID:        raw.TransactionID,
				RecurrenceSequence:   coalesceSeq(raw.RecurrenceSequence),
				TransactionType:      raw.TransactionType,
				Status:               raw.Status,
				IsRecurringException: false,
				ReasonCode:           ReasonExceptionStructureInvalid,
				SemanticState:        semState,
				AuditOutcome:         AuditInconsistentHumanReview,
				Deterministic:        false,
				AuditedField:         "exception",
			}
		}
	}
	return nil
}

// ---------- Civil classifier factory ----------

type CivilDateClassifier struct {
	Location *time.Location
}

func NewCivilDateClassifier() (*CivilDateClassifier, error) {
	loc, err := resolveCivilRepairTimezone()
	if err != nil {
		return nil, err
	}
	return &CivilDateClassifier{Location: loc}, nil
}

func NewCivilDateClassifierNoTimezone() *CivilDateClassifier {
	return &CivilDateClassifier{Location: nil}
}

func (cl *CivilDateClassifier) ClassifyPair(fieldName string, legacyUnix int64, civilStr sql.NullString) *CivilPairClassification {
	return ClassifyCivilPair(fieldName, legacyUnix, civilStr, cl.Location)
}
