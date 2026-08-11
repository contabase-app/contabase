package civildateplan

import (
	"fmt"
	"strings"
	"time"

	"github.com/contabase-app/contabase/internal/civildate"
)

const (
	schemaV1                 = 1
	scopeV1                  = "DATABASE"
	toolVersionV1            = "civil-date-repair/v1"
	classifierVersionV1      = "civil-date-classifier/v1"
	logicalFingerprintVersV1 = "contabase-logical/v1"
)

const (
	variantNonRecurring = "TRANSACTION_NON_RECURRING"
	variantRecurring    = "TRANSACTION_RECURRING"
	variantRule         = "RECURRING_RULE"
)

const (
	sourceTransaction   = "transaction"
	sourceRecurringRule = "recurring_rule"
)

const (
	reasonDateCivilMissing      = "DATE_CIVIL_MISSING"
	reasonDueDateCivilMissing   = "DUE_DATE_CIVIL_MISSING"
	reasonStartDateCivilMissing = "START_DATE_CIVIL_MISSING"
	reasonStartsAtCivilMissing  = "STARTS_AT_CIVIL_MISSING"
)

const repairOutcomeAutoRepair = "AUTO_REPAIR"

var validStatuses = map[string]bool{"paid": true, "pending": true}
var validTypes = map[string]bool{"EXPENSE": true, "INCOME": true, "TRANSFER": true}

type validatedPlanV1 struct {
	plan      *parsedPlan
	validated bool
}

func validateTransactionState(ts *parsedTransactionState) error {
	if ts == nil {
		return nil
	}
	st := ts.status.value
	if st == "" || st != strings.TrimSpace(st) {
		return fmt.Errorf("transaction_state.status must be one of: paid, pending")
	}
	if !validStatuses[st] {
		return fmt.Errorf("transaction_state.status must be one of: paid, pending")
	}
	tp := ts.typeField.value
	if tp == "" || tp != strings.TrimSpace(tp) {
		return fmt.Errorf("transaction_state.type must be one of: EXPENSE, INCOME, TRANSFER")
	}
	if !validTypes[tp] {
		return fmt.Errorf("transaction_state.type must be one of: EXPENSE, INCOME, TRANSFER")
	}
	return nil
}

func validatePlanV1(plan *parsedPlan) error {
	if err := validateTopFields(plan); err != nil {
		return err
	}
	return validatePlanSemanticsV1(plan)
}

func validateParsedPlanV1(plan *parsedPlan) (validatedPlanV1, error) {
	var zero validatedPlanV1
	if plan == nil {
		return zero, newPlanStageError(
			loadErrorInternalInvariant,
			fmt.Errorf("internal plan invariant failed"),
		)
	}
	if err := validateTopFields(plan); err != nil {
		return zero, newPlanStageError(
			loadErrorSchema,
			fmt.Errorf("incompatible plan schema: %w", err),
		)
	}
	if err := validatePlanSemanticsV1(plan); err != nil {
		return zero, newPlanStageError(
			loadErrorValidation,
			fmt.Errorf("invalid plan data: %w", err),
		)
	}
	return validatedPlanV1{plan: plan, validated: true}, nil
}

func validatePlanSemanticsV1(plan *parsedPlan) error {
	tz := plan.timezone.value
	loc, err := time.LoadLocation(tz)
	if err != nil {
		return fmt.Errorf("invalid timezone %q: %w", tz, err)
	}

	gt := plan.generatedAt.value
	parsed, err := time.Parse(time.RFC3339Nano, gt)
	if err != nil {
		return fmt.Errorf("invalid generated_at: %w", err)
	}
	if parsed.Location() != time.UTC {
		return fmt.Errorf("generated_at must be UTC")
	}
	if !strings.HasSuffix(gt, "Z") {
		return fmt.Errorf("generated_at must end with Z")
	}
	if parsed.Format(time.RFC3339Nano) != gt {
		return fmt.Errorf("generated_at must be canonical RFC3339Nano with trailing Z")
	}

	seen := make(map[string]bool)
	for i, f := range plan.findings {
		key := fmt.Sprintf("%s/%s/%s/%s/%s",
			f.source.value, f.variant.value,
			f.workspaceID.value, f.sourceRowID.value, f.auditedField.value)
		if seen[key] {
			return fmt.Errorf("finding %d: duplicate canonical key", i)
		}
		seen[key] = true

		if err := validateFinding(f, loc, tz, i); err != nil {
			return err
		}
	}

	return nil
}

func validateTopFields(plan *parsedPlan) error {
	if plan.schemaVersion.value != schemaV1 {
		return fmt.Errorf("schema_version must be %d", schemaV1)
	}
	if plan.scope.value != scopeV1 {
		return fmt.Errorf("scope must be %s", scopeV1)
	}
	if plan.toolVersion.value != toolVersionV1 {
		return fmt.Errorf("tool_version must be %s", toolVersionV1)
	}
	if plan.classifierVersion.value != classifierVersionV1 {
		return fmt.Errorf("classifier_version must be %s", classifierVersionV1)
	}
	if plan.logicalFingerprintVersion.value != logicalFingerprintVersV1 {
		return fmt.Errorf("logical_fingerprint_version must be %s", logicalFingerprintVersV1)
	}

	pid := plan.planID.value
	if len(pid) != 32 {
		return fmt.Errorf("plan_id must be 32 hex chars")
	}
	for _, c := range pid {
		if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f')) {
			return fmt.Errorf("plan_id must be lowercase hex")
		}
	}

	if plan.databaseSchemaVersion.value <= 0 {
		return fmt.Errorf("database_schema_version must be positive")
	}

	if plan.timezone.value == "" {
		return fmt.Errorf("timezone must not be empty")
	}

	if !isValidDigest(plan.sourceLogicalFingerprint.value) {
		return fmt.Errorf("source_logical_fingerprint must be sha256: followed by 64 hex chars")
	}
	if !isValidDigest(plan.auditSetDigest.value) {
		return fmt.Errorf("audit_set_digest must be sha256: followed by 64 hex chars")
	}

	return nil
}

func isValidDigest(s string) bool {
	if !strings.HasPrefix(s, "sha256:") {
		return false
	}
	hex := s[7:]
	if len(hex) != 64 {
		return false
	}
	for _, c := range hex {
		if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f')) {
			return false
		}
	}
	return true
}

func validateFinding(f *parsedFinding, loc *time.Location, planTZ string, idx int) error {
	mk := func(msg string) string { return fmt.Sprintf("finding %d: %s", idx, msg) }

	// All fields must be present and non-null (validated by parser already).
	// Double-check critical ones.
	if f.repairOutcome.value != repairOutcomeAutoRepair {
		return fmt.Errorf("%s", mk("repair_outcome must be AUTO_REPAIR"))
	}
	if f.expectedLegacyValue.value <= 0 {
		return fmt.Errorf("%s", mk("expected_legacy_value must be > 0"))
	}
	if f.expectedCivilState.value != "NULL" {
		return fmt.Errorf("%s", mk("expected_civil_state must be NULL"))
	}
	if f.auditedField.value == "paid_date_civil" {
		return fmt.Errorf("%s", mk("paid_date_civil is forbidden"))
	}
	if f.timezone.value != planTZ {
		return fmt.Errorf("%s", mk("finding timezone must equal plan timezone"))
	}
	if f.classifierVersion.value != classifierVersionV1 {
		return fmt.Errorf("%s", mk("finding classifier_version must equal plan classifier_version"))
	}

	// Validate civil date proposal
	if err := validateCivilDateProposal(f, loc); err != nil {
		return fmt.Errorf("%s", mk(err.Error()))
	}

	// Validate by variant
	switch f.variant.value {
	case variantNonRecurring:
		return validateNonRecurring(f, idx)
	case variantRecurring:
		return validateRecurring(f, idx)
	case variantRule:
		return validateRuleVariant(f, idx)
	default:
		return fmt.Errorf("%s", mk(fmt.Sprintf("unknown variant: %s", f.variant.value)))
	}
}

func validateCivilDateProposal(f *parsedFinding, loc *time.Location) error {
	legacy := f.expectedLegacyValue.value
	proposed := f.proposedCivilValue.value

	utcDate := civildate.LegacyUnixToCivilDateUTC(legacy)
	tzDate, err := civildate.LegacyUnixToCivilDateInLocation(legacy, loc)
	if err != nil {
		return fmt.Errorf("cannot compute civil date in timezone: %w", err)
	}

	if utcDate != tzDate {
		return fmt.Errorf("UTC civil date diverges from timezone civil date, cannot AUTO_REPAIR")
	}

	expected := civildate.FormatCivilDate(utcDate)
	if proposed != expected {
		return fmt.Errorf("proposed_civil_value %q does not match computed %q", proposed, expected)
	}

	// Validate canonical format
	if _, err := civildate.ParseCivilDateInput(proposed); err != nil {
		return fmt.Errorf("proposed_civil_value not valid YYYY-MM-DD: %w", err)
	}

	return nil
}

func validateFieldReasonClosed(f *parsedFinding, idx int) error {
	mk := func(msg string) string { return fmt.Sprintf("finding %d: %s", idx, msg) }

	src := f.source.value
	field := f.auditedField.value
	reason := f.reasonCode.value

	switch {
	case src == sourceTransaction && field == "date_civil" && reason == reasonDateCivilMissing:
		return nil
	case src == sourceTransaction && field == "due_date_civil" && reason == reasonDueDateCivilMissing:
		return nil
	case src == sourceRecurringRule && field == "start_date_civil" && reason == reasonStartDateCivilMissing:
		return nil
	case src == sourceRecurringRule && field == "starts_at_civil" && reason == reasonStartsAtCivilMissing:
		return nil
	default:
		return fmt.Errorf("%s", mk(fmt.Sprintf("invalid combination source=%s field=%s reason=%s", src, field, reason)))
	}
}

func validateNonRecurring(f *parsedFinding, idx int) error {
	mk := func(msg string) string { return fmt.Sprintf("finding %d: %s", idx, msg) }

	if f.source.value != sourceTransaction {
		return fmt.Errorf("%s", mk("NON_RECURRING requires source=transaction"))
	}
	if err := validateFieldReasonClosed(f, idx); err != nil {
		return err
	}

	if fieldNull[*parsedTransactionState](f.transactionState) {
		return fmt.Errorf("%s", mk("transaction_state must be present"))
	}
	if fieldPresent[*parsedRuleState](f.ruleState) {
		return fmt.Errorf("%s", mk("rule_state must be null"))
	}
	if fieldPresent[*parsedSeriesState](f.seriesState) {
		return fmt.Errorf("%s", mk("series_state must be null"))
	}

	ts := f.transactionState.value
	if ts == nil {
		return fmt.Errorf("%s", mk("transaction_state is null"))
	}

	if err := validateTransactionState(ts); err != nil {
		return fmt.Errorf("%s", mk(err.Error()))
	}

	if fieldPresent[*string](ts.recurringRuleID) {
		return fmt.Errorf("%s", mk("recurring_rule_id must be null"))
	}
	if fieldPresent[*string](ts.seriesID) {
		return fmt.Errorf("%s", mk("series_id must be null"))
	}
	if fieldPresent[*int64](ts.recurrenceSequence) {
		return fmt.Errorf("%s", mk("recurrence_sequence must be null"))
	}
	if ts.isRecurringException.value {
		return fmt.Errorf("%s", mk("is_recurring_exception must be false"))
	}
	if fieldPresent[*string](ts.recurringExceptionReason) {
		return fmt.Errorf("%s", mk("recurring_exception_reason must be null"))
	}

	if f.sourceRowID.value != ts.id.value {
		return fmt.Errorf("%s", mk("source_row_id must equal transaction_state.id"))
	}
	if f.workspaceID.value != ts.workspaceID.value {
		return fmt.Errorf("%s", mk("workspace_id mismatch with transaction_state"))
	}

	// expected_legacy_value == date_unix for date_civil
	if f.auditedField.value == "date_civil" {
		if f.expectedLegacyValue.value != ts.dateUnix.value {
			return fmt.Errorf("%s", mk("expected_legacy_value must equal transaction_state.date_unix"))
		}
	}

	// For due_date_civil, due_date_unix must be a value and match
	if f.auditedField.value == "due_date_civil" {
		if fieldNull[*int64](ts.dueDateUnix) || !fieldPresent[*int64](ts.dueDateUnix) {
			return fmt.Errorf("%s", mk("due_date_unix must be present for due_date_civil"))
		}
		if ts.dueDateUnix.value == nil {
			return fmt.Errorf("%s", mk("due_date_unix must be a value for due_date_civil"))
		}
		if f.expectedLegacyValue.value != *ts.dueDateUnix.value {
			return fmt.Errorf("%s", mk("expected_legacy_value must equal transaction_state.due_date_unix"))
		}
	}

	return nil
}

func validateRecurring(f *parsedFinding, idx int) error {
	mk := func(msg string) string { return fmt.Sprintf("finding %d: %s", idx, msg) }

	if f.source.value != sourceTransaction {
		return fmt.Errorf("%s", mk("RECURRING requires source=transaction"))
	}
	if err := validateFieldReasonClosed(f, idx); err != nil {
		return err
	}

	if fieldNull[*parsedTransactionState](f.transactionState) || f.transactionState.value == nil {
		return fmt.Errorf("%s", mk("transaction_state must be present"))
	}
	if fieldNull[*parsedRuleState](f.ruleState) || f.ruleState.value == nil {
		return fmt.Errorf("%s", mk("rule_state must be present"))
	}
	if fieldNull[*parsedSeriesState](f.seriesState) || f.seriesState.value == nil {
		return fmt.Errorf("%s", mk("series_state must be present"))
	}

	ts := f.transactionState.value
	rs := f.ruleState.value
	ss := f.seriesState.value

	if err := validateTransactionState(ts); err != nil {
		return fmt.Errorf("%s", mk(err.Error()))
	}

	if f.sourceRowID.value != ts.id.value {
		return fmt.Errorf("%s", mk("source_row_id must equal transaction_state.id"))
	}
	if f.workspaceID.value != ts.workspaceID.value {
		return fmt.Errorf("%s", mk("workspace_id mismatch with transaction_state"))
	}
	if f.workspaceID.value != rs.workspaceID.value {
		return fmt.Errorf("%s", mk("workspace_id mismatch with rule_state"))
	}
	if f.workspaceID.value != ss.workspaceID.value {
		return fmt.Errorf("%s", mk("workspace_id mismatch with series_state"))
	}

	if fieldNull[*string](ts.recurringRuleID) || ts.recurringRuleID.value == nil {
		return fmt.Errorf("%s", mk("recurring_rule_id must be a value"))
	}
	if fieldNull[*string](ts.seriesID) || ts.seriesID.value == nil {
		return fmt.Errorf("%s", mk("series_id must be a value"))
	}
	if fieldNull[*int64](ts.recurrenceSequence) || ts.recurrenceSequence.value == nil {
		return fmt.Errorf("%s", mk("recurrence_sequence must be a value"))
	}
	if *ts.recurrenceSequence.value <= 0 {
		return fmt.Errorf("%s", mk("recurrence_sequence must be positive"))
	}

	if *ts.recurringRuleID.value != rs.id.value {
		return fmt.Errorf("%s", mk("recurring_rule_id does not match rule_state.id"))
	}
	if *ts.seriesID.value != rs.seriesID.value {
		return fmt.Errorf("%s", mk("series_id does not match rule_state.series_id"))
	}
	if rs.seriesID.value != ss.id.value {
		return fmt.Errorf("%s", mk("rule_state.series_id does not match series_state.id"))
	}

	seq := *ts.recurrenceSequence.value
	if seq < rs.effectiveStartSequence.value {
		return fmt.Errorf("%s", mk("recurrence_sequence before effective_start"))
	}
	if fieldPresent[*int64](rs.effectiveEndSequence) && rs.effectiveEndSequence.value != nil {
		if seq > *rs.effectiveEndSequence.value {
			return fmt.Errorf("%s", mk("recurrence_sequence after effective_end"))
		}
	}

	if !ts.isRecurringException.value {
		if fieldPresent[*string](ts.recurringExceptionReason) {
			return fmt.Errorf("%s", mk("recurring_exception_reason must be null when is_recurring_exception is false"))
		}
	} else {
		if fieldNull[*string](ts.recurringExceptionReason) || ts.recurringExceptionReason.value == nil {
			return fmt.Errorf("%s", mk("recurring_exception_reason must be a value when is_recurring_exception is true"))
		}
	}

	// Relation with target field
	if f.auditedField.value == "date_civil" {
		if f.expectedLegacyValue.value != ts.dateUnix.value {
			return fmt.Errorf("%s", mk("expected_legacy_value must equal transaction_state.date_unix"))
		}
	}
	if f.auditedField.value == "due_date_civil" {
		if !fieldPresent[*int64](ts.dueDateUnix) || ts.dueDateUnix.value == nil {
			return fmt.Errorf("%s", mk("due_date_unix must be a value for due_date_civil"))
		}
		if f.expectedLegacyValue.value != *ts.dueDateUnix.value {
			return fmt.Errorf("%s", mk("expected_legacy_value must equal due_date_unix"))
		}
	}

	return nil
}

func validateRuleVariant(f *parsedFinding, idx int) error {
	mk := func(msg string) string { return fmt.Sprintf("finding %d: %s", idx, msg) }

	if f.source.value != sourceRecurringRule {
		return fmt.Errorf("%s", mk("RECURRING_RULE requires source=recurring_rule"))
	}
	if err := validateFieldReasonClosed(f, idx); err != nil {
		return err
	}

	if fieldPresent[*parsedTransactionState](f.transactionState) {
		return fmt.Errorf("%s", mk("transaction_state must be null"))
	}
	if fieldNull[*parsedRuleState](f.ruleState) || f.ruleState.value == nil {
		return fmt.Errorf("%s", mk("rule_state must be present"))
	}
	if fieldNull[*parsedSeriesState](f.seriesState) || f.seriesState.value == nil {
		return fmt.Errorf("%s", mk("series_state must be present"))
	}

	rs := f.ruleState.value
	ss := f.seriesState.value

	if f.sourceRowID.value != rs.id.value {
		return fmt.Errorf("%s", mk("source_row_id must equal rule_state.id"))
	}
	if f.workspaceID.value != rs.workspaceID.value {
		return fmt.Errorf("%s", mk("workspace_id mismatch with rule_state"))
	}
	if f.workspaceID.value != ss.workspaceID.value {
		return fmt.Errorf("%s", mk("workspace_id mismatch with series_state"))
	}
	if rs.seriesID.value != ss.id.value {
		return fmt.Errorf("%s", mk("rule_state.series_id does not match series_state.id"))
	}

	return nil
}
