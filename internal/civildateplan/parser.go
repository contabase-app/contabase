package civildateplan

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"strconv"
	"strings"
	"unicode/utf8"
)

const maxPlanSize = 64 * 1024 * 1024

// maxDepth is a defense-in-depth guard. Schema v1 has limited fixed nesting
// (plan → findings → finding → states). The parser rejects unknown structures
// before generic recursion. maxDepth protects against malformed or future
// schemas that might exceed the v1 structure.
const maxDepth = 32

type parsedField[T any] struct {
	present bool
	isNull  bool
	value   T
}

func fieldPresent[T any](f parsedField[T]) bool { return f.present && !f.isNull }
func fieldNull[T any](f parsedField[T]) bool    { return f.present && f.isNull }
func fieldMissing[T any](f parsedField[T]) bool { return !f.present }

type parsedPlan struct {
	schemaVersion             parsedField[int64]
	planID                    parsedField[string]
	scope                     parsedField[string]
	toolVersion               parsedField[string]
	classifierVersion         parsedField[string]
	databaseSchemaVersion     parsedField[int64]
	timezone                  parsedField[string]
	logicalFingerprintVersion parsedField[string]
	sourceLogicalFingerprint  parsedField[string]
	auditSetDigest            parsedField[string]
	generatedAt               parsedField[string]
	findings                  []*parsedFinding
}

type parsedFinding struct {
	variant             parsedField[string]
	source              parsedField[string]
	workspaceID         parsedField[string]
	sourceRowID         parsedField[string]
	auditedField        parsedField[string]
	reasonCode          parsedField[string]
	repairOutcome       parsedField[string]
	expectedLegacyValue parsedField[int64]
	expectedCivilState  parsedField[string]
	proposedCivilValue  parsedField[string]
	timezone            parsedField[string]
	classifierVersion   parsedField[string]
	transactionState    parsedField[*parsedTransactionState]
	ruleState           parsedField[*parsedRuleState]
	seriesState         parsedField[*parsedSeriesState]
}

type parsedTransactionState struct {
	id                       parsedField[string]
	workspaceID              parsedField[string]
	status                   parsedField[string]
	typeField                parsedField[string]
	dateUnix                 parsedField[int64]
	dueDateUnix              parsedField[*int64]
	recurringRuleID          parsedField[*string]
	seriesID                 parsedField[*string]
	recurrenceSequence       parsedField[*int64]
	isRecurringException     parsedField[bool]
	recurringExceptionReason parsedField[*string]
}

type parsedRuleState struct {
	id                     parsedField[string]
	workspaceID            parsedField[string]
	seriesID               parsedField[string]
	parentRuleID           parsedField[*string]
	effectiveStartSequence parsedField[int64]
	effectiveEndSequence   parsedField[*int64]
	active                 parsedField[bool]
}

type parsedSeriesState struct {
	id          parsedField[string]
	workspaceID parsedField[string]
	active      parsedField[bool]
}

// ---------- Entry point ----------

func parsePlanV1(data []byte) (*parsedPlan, error) {
	validated, err := parseAndValidatePlanV1(data)
	if err != nil {
		return nil, err
	}
	return validated.plan, nil
}

func parseAndValidatePlanV1(data []byte) (validatedPlanV1, error) {
	var zero validatedPlanV1
	plan, err := parsePlanV1Syntax(data)
	if err != nil {
		return zero, err
	}
	validated, err := validateParsedPlanV1(plan)
	if err != nil {
		return zero, err
	}
	return validated, nil
}

func parsePlanV1Syntax(data []byte) (*parsedPlan, error) {
	if len(data) > maxPlanSize {
		return nil, newPlanStageError(
			loadErrorSyntax,
			fmt.Errorf("plan exceeds maximum size of %d bytes", maxPlanSize),
		)
	}
	if !utf8.Valid(data) {
		return nil, newPlanStageError(loadErrorSyntax, fmt.Errorf("input is not valid UTF-8"))
	}
	if err := validateLexical(data); err != nil {
		return nil, newPlanStageError(loadErrorSyntax, fmt.Errorf("lexical validation: %w", err))
	}

	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()
	dec.DisallowUnknownFields()

	plan, err := parsePlanObject(dec, 0)
	if err != nil {
		return nil, newPlanStageError(loadErrorSyntax, err)
	}

	if err := checkTokenEOF(dec); err != nil {
		return nil, newPlanStageError(loadErrorSyntax, err)
	}

	return plan, nil
}

// ---------- Helpers ----------

func depthExceeded(d int) bool { return d > maxDepth }

func expectDelim(dec *json.Decoder, expected json.Delim) error {
	tok, err := dec.Token()
	if err != nil {
		return err
	}
	got, ok := tok.(json.Delim)
	if !ok || got != expected {
		return fmt.Errorf("expected %c", expected)
	}
	return nil
}

func checkTokenEOF(dec *json.Decoder) error {
	tok, err := dec.Token()
	if err == io.EOF {
		return nil
	}
	if err != nil {
		return err
	}
	return fmt.Errorf("unexpected trailing token: %v", tok)
}

// ---------- Object parsers ----------

func parsePlanObject(dec *json.Decoder, depth int) (*parsedPlan, error) {
	if depthExceeded(depth) {
		return nil, fmt.Errorf("maximum nesting depth exceeded")
	}
	if err := expectDelim(dec, '{'); err != nil {
		return nil, err
	}

	plan := &parsedPlan{}
	seen := make(map[string]bool)

	for dec.More() {
		tok, err := dec.Token()
		if err != nil {
			return nil, err
		}
		key, ok := tok.(string)
		if !ok {
			return nil, fmt.Errorf("expected string key")
		}
		if seen[key] {
			return nil, fmt.Errorf("duplicate key: %s", key)
		}
		seen[key] = true

		switch key {
		case "schema_version":
			v, err := readInt64Field(dec)
			if err != nil {
				return nil, fmt.Errorf("schema_version: %w", err)
			}
			plan.schemaVersion = v
		case "plan_id":
			v, err := readStringField(dec)
			if err != nil {
				return nil, fmt.Errorf("plan_id: %w", err)
			}
			plan.planID = v
		case "scope":
			v, err := readStringField(dec)
			if err != nil {
				return nil, fmt.Errorf("scope: %w", err)
			}
			plan.scope = v
		case "tool_version":
			v, err := readStringField(dec)
			if err != nil {
				return nil, fmt.Errorf("tool_version: %w", err)
			}
			plan.toolVersion = v
		case "classifier_version":
			v, err := readStringField(dec)
			if err != nil {
				return nil, fmt.Errorf("classifier_version: %w", err)
			}
			plan.classifierVersion = v
		case "database_schema_version":
			v, err := readInt64Field(dec)
			if err != nil {
				return nil, fmt.Errorf("database_schema_version: %w", err)
			}
			plan.databaseSchemaVersion = v
		case "timezone":
			v, err := readStringField(dec)
			if err != nil {
				return nil, fmt.Errorf("timezone: %w", err)
			}
			plan.timezone = v
		case "logical_fingerprint_version":
			v, err := readStringField(dec)
			if err != nil {
				return nil, fmt.Errorf("logical_fingerprint_version: %w", err)
			}
			plan.logicalFingerprintVersion = v
		case "source_logical_fingerprint":
			v, err := readStringField(dec)
			if err != nil {
				return nil, fmt.Errorf("source_logical_fingerprint: %w", err)
			}
			plan.sourceLogicalFingerprint = v
		case "audit_set_digest":
			v, err := readStringField(dec)
			if err != nil {
				return nil, fmt.Errorf("audit_set_digest: %w", err)
			}
			plan.auditSetDigest = v
		case "generated_at":
			v, err := readStringField(dec)
			if err != nil {
				return nil, fmt.Errorf("generated_at: %w", err)
			}
			plan.generatedAt = v
		case "findings":
			findings, err := parseFindingsArray(dec, depth+1)
			if err != nil {
				return nil, fmt.Errorf("findings: %w", err)
			}
			plan.findings = findings
		default:
			return nil, fmt.Errorf("unknown field: %s", key)
		}
	}

	if err := expectDelim(dec, '}'); err != nil {
		return nil, err
	}

	required := []string{"schema_version", "plan_id", "scope", "tool_version",
		"classifier_version", "database_schema_version", "timezone",
		"logical_fingerprint_version", "source_logical_fingerprint",
		"audit_set_digest", "generated_at", "findings"}
	for _, k := range required {
		if !seen[k] {
			return nil, fmt.Errorf("required field missing: %s", k)
		}
	}

	return plan, nil
}

func parseFindingsArray(dec *json.Decoder, depth int) ([]*parsedFinding, error) {
	if depthExceeded(depth) {
		return nil, fmt.Errorf("maximum nesting depth exceeded")
	}
	if err := expectDelim(dec, '['); err != nil {
		return nil, err
	}

	var findings []*parsedFinding
	for dec.More() {
		f, err := parseFindingObject(dec, depth+1)
		if err != nil {
			return nil, err
		}
		findings = append(findings, f)
	}

	if err := expectDelim(dec, ']'); err != nil {
		return nil, err
	}
	if len(findings) == 0 {
		return nil, fmt.Errorf("findings must not be empty")
	}
	return findings, nil
}

func parseFindingObject(dec *json.Decoder, depth int) (*parsedFinding, error) {
	if depthExceeded(depth) {
		return nil, fmt.Errorf("maximum nesting depth exceeded")
	}
	if err := expectDelim(dec, '{'); err != nil {
		return nil, err
	}

	f := &parsedFinding{}
	seen := make(map[string]bool)

	for dec.More() {
		tok, err := dec.Token()
		if err != nil {
			return nil, err
		}
		key, ok := tok.(string)
		if !ok {
			return nil, fmt.Errorf("expected string key in finding")
		}
		if seen[key] {
			return nil, fmt.Errorf("duplicate key in finding: %s", key)
		}
		seen[key] = true

		switch key {
		case "variant":
			f.variant, err = readStringField(dec)
		case "source":
			f.source, err = readStringField(dec)
		case "workspace_id":
			f.workspaceID, err = readStringField(dec)
		case "source_row_id":
			f.sourceRowID, err = readStringField(dec)
		case "audited_field":
			f.auditedField, err = readStringField(dec)
		case "reason_code":
			f.reasonCode, err = readStringField(dec)
		case "repair_outcome":
			f.repairOutcome, err = readStringField(dec)
		case "expected_legacy_value":
			f.expectedLegacyValue, err = readInt64Field(dec)
		case "expected_civil_state":
			f.expectedCivilState, err = readStringField(dec)
		case "proposed_civil_value":
			f.proposedCivilValue, err = readStringField(dec)
		case "timezone":
			f.timezone, err = readStringField(dec)
		case "classifier_version":
			f.classifierVersion, err = readStringField(dec)
		case "transaction_state":
			f.transactionState, err = readNullableObjectBodyField(dec, func(d *json.Decoder, d2 int) (*parsedTransactionState, error) {
				return parseTransactionStateBody(d, d2)
			}, depth+1)
		case "rule_state":
			f.ruleState, err = readNullableObjectBodyField(dec, func(d *json.Decoder, d2 int) (*parsedRuleState, error) {
				return parseRuleStateBody(d, d2)
			}, depth+1)
		case "series_state":
			f.seriesState, err = readNullableObjectBodyField(dec, func(d *json.Decoder, d2 int) (*parsedSeriesState, error) {
				return parseSeriesStateBody(d, d2)
			}, depth+1)
		default:
			return nil, fmt.Errorf("unknown field in finding: %s", key)
		}
		if err != nil {
			return nil, err
		}
	}

	if err := expectDelim(dec, '}'); err != nil {
		return nil, err
	}

	required := []string{"variant", "source", "workspace_id", "source_row_id", "audited_field",
		"reason_code", "repair_outcome", "expected_legacy_value", "expected_civil_state",
		"proposed_civil_value", "timezone", "classifier_version",
		"transaction_state", "rule_state", "series_state"}
	for _, k := range required {
		if !seen[k] {
			return nil, fmt.Errorf("required field missing in finding: %s", k)
		}
	}

	return f, nil
}

func parseTransactionStateBody(dec *json.Decoder, depth int) (*parsedTransactionState, error) {
	if depthExceeded(depth) {
		return nil, fmt.Errorf("maximum nesting depth exceeded")
	}

	ts := &parsedTransactionState{}
	seen := make(map[string]bool)

	for dec.More() {
		tok, err := dec.Token()
		if err != nil {
			return nil, err
		}
		key, ok := tok.(string)
		if !ok {
			return nil, fmt.Errorf("expected string key in transaction_state")
		}
		if seen[key] {
			return nil, fmt.Errorf("duplicate key in transaction_state: %s", key)
		}
		seen[key] = true

		var ferr error
		switch key {
		case "id":
			ts.id, ferr = readStringField(dec)
		case "workspace_id":
			ts.workspaceID, ferr = readStringField(dec)
		case "status":
			ts.status, ferr = readStringField(dec)
		case "type":
			ts.typeField, ferr = readStringField(dec)
		case "date_unix":
			ts.dateUnix, ferr = readInt64Field(dec)
		case "due_date_unix":
			ts.dueDateUnix, ferr = readNullableInt64Field(dec)
		case "recurring_rule_id":
			ts.recurringRuleID, ferr = readNullableStringField(dec)
		case "series_id":
			ts.seriesID, ferr = readNullableStringField(dec)
		case "recurrence_sequence":
			ts.recurrenceSequence, ferr = readNullableInt64Field(dec)
		case "is_recurring_exception":
			ts.isRecurringException, ferr = readBoolField(dec)
		case "recurring_exception_reason":
			ts.recurringExceptionReason, ferr = readNullableStringField(dec)
		default:
			return nil, fmt.Errorf("unknown field in transaction_state: %s", key)
		}
		if ferr != nil {
			return nil, ferr
		}
	}

	if err := expectDelim(dec, '}'); err != nil {
		return nil, err
	}

	required := []string{"id", "workspace_id", "status", "type", "date_unix",
		"due_date_unix", "recurring_rule_id", "series_id", "recurrence_sequence",
		"is_recurring_exception", "recurring_exception_reason"}
	for _, k := range required {
		if !seen[k] {
			return nil, fmt.Errorf("required field missing in transaction_state: %s", k)
		}
	}

	if fieldNull[string](ts.id) {
		return nil, fmt.Errorf("transaction_state.id must not be null")
	}
	if fieldNull[string](ts.workspaceID) {
		return nil, fmt.Errorf("transaction_state.workspace_id must not be null")
	}
	if fieldNull[string](ts.status) {
		return nil, fmt.Errorf("transaction_state.status must not be null")
	}
	if fieldNull[string](ts.typeField) {
		return nil, fmt.Errorf("transaction_state.type must not be null")
	}
	if fieldNull[int64](ts.dateUnix) {
		return nil, fmt.Errorf("transaction_state.date_unix must not be null")
	}
	if fieldNull[bool](ts.isRecurringException) {
		return nil, fmt.Errorf("transaction_state.is_recurring_exception must not be null")
	}

	return ts, nil
}

func parseRuleStateBody(dec *json.Decoder, depth int) (*parsedRuleState, error) {
	if depthExceeded(depth) {
		return nil, fmt.Errorf("maximum nesting depth exceeded")
	}

	rs := &parsedRuleState{}
	seen := make(map[string]bool)

	for dec.More() {
		tok, err := dec.Token()
		if err != nil {
			return nil, err
		}
		key, ok := tok.(string)
		if !ok {
			return nil, fmt.Errorf("expected string key in rule_state")
		}
		if seen[key] {
			return nil, fmt.Errorf("duplicate key in rule_state: %s", key)
		}
		seen[key] = true

		var ferr error
		switch key {
		case "id":
			rs.id, ferr = readStringField(dec)
		case "workspace_id":
			rs.workspaceID, ferr = readStringField(dec)
		case "series_id":
			rs.seriesID, ferr = readStringField(dec)
		case "parent_rule_id":
			rs.parentRuleID, ferr = readNullableStringField(dec)
		case "effective_start_sequence":
			rs.effectiveStartSequence, ferr = readInt64Field(dec)
		case "effective_end_sequence":
			rs.effectiveEndSequence, ferr = readNullableInt64Field(dec)
		case "active":
			rs.active, ferr = readBoolField(dec)
		default:
			return nil, fmt.Errorf("unknown field in rule_state: %s", key)
		}
		if ferr != nil {
			return nil, ferr
		}
	}

	if err := expectDelim(dec, '}'); err != nil {
		return nil, err
	}

	required := []string{"id", "workspace_id", "series_id", "parent_rule_id",
		"effective_start_sequence", "effective_end_sequence", "active"}
	for _, k := range required {
		if !seen[k] {
			return nil, fmt.Errorf("required field missing in rule_state: %s", k)
		}
	}

	if fieldNull[string](rs.id) {
		return nil, fmt.Errorf("rule_state.id must not be null")
	}
	if fieldNull[string](rs.workspaceID) {
		return nil, fmt.Errorf("rule_state.workspace_id must not be null")
	}
	if fieldNull[string](rs.seriesID) {
		return nil, fmt.Errorf("rule_state.series_id must not be null")
	}
	if fieldNull[int64](rs.effectiveStartSequence) {
		return nil, fmt.Errorf("rule_state.effective_start_sequence must not be null")
	}
	if fieldNull[bool](rs.active) {
		return nil, fmt.Errorf("rule_state.active must not be null")
	}

	return rs, nil
}

func parseSeriesStateBody(dec *json.Decoder, depth int) (*parsedSeriesState, error) {
	if depthExceeded(depth) {
		return nil, fmt.Errorf("maximum nesting depth exceeded")
	}

	ss := &parsedSeriesState{}
	seen := make(map[string]bool)

	for dec.More() {
		tok, err := dec.Token()
		if err != nil {
			return nil, err
		}
		key, ok := tok.(string)
		if !ok {
			return nil, fmt.Errorf("expected string key in series_state")
		}
		if seen[key] {
			return nil, fmt.Errorf("duplicate key in series_state: %s", key)
		}
		seen[key] = true

		var ferr error
		switch key {
		case "id":
			ss.id, ferr = readStringField(dec)
		case "workspace_id":
			ss.workspaceID, ferr = readStringField(dec)
		case "active":
			ss.active, ferr = readBoolField(dec)
		default:
			return nil, fmt.Errorf("unknown field in series_state: %s", key)
		}
		if ferr != nil {
			return nil, ferr
		}
	}

	if err := expectDelim(dec, '}'); err != nil {
		return nil, err
	}

	required := []string{"id", "workspace_id", "active"}
	for _, k := range required {
		if !seen[k] {
			return nil, fmt.Errorf("required field missing in series_state: %s", k)
		}
	}

	if fieldNull[string](ss.id) {
		return nil, fmt.Errorf("series_state.id must not be null")
	}
	if fieldNull[string](ss.workspaceID) {
		return nil, fmt.Errorf("series_state.workspace_id must not be null")
	}
	if fieldNull[bool](ss.active) {
		return nil, fmt.Errorf("series_state.active must not be null")
	}

	return ss, nil
}

// ---------- Nullable object body parser ----------

func readNullableObjectBodyField[T any](dec *json.Decoder, parser func(*json.Decoder, int) (T, error), depth int) (parsedField[T], error) {
	tok, err := dec.Token()
	if err != nil {
		return parsedField[T]{}, err
	}
	if tok == nil {
		return parsedField[T]{present: true, isNull: true}, nil
	}
	if tok != json.Delim('{') {
		return parsedField[T]{}, fmt.Errorf("expected { or null")
	}
	v, err := parser(dec, depth)
	if err != nil {
		return parsedField[T]{}, err
	}
	return parsedField[T]{present: true, value: v}, nil
}

// ---------- Field readers ----------

func readStringField(dec *json.Decoder) (parsedField[string], error) {
	tok, err := dec.Token()
	if err != nil {
		return parsedField[string]{}, err
	}
	if tok == nil {
		return parsedField[string]{present: true, isNull: true}, nil
	}
	s, ok := tok.(string)
	if !ok {
		return parsedField[string]{}, fmt.Errorf("expected string")
	}
	return parsedField[string]{present: true, value: s}, nil
}

func readInt64Field(dec *json.Decoder) (parsedField[int64], error) {
	tok, err := dec.Token()
	if err != nil {
		return parsedField[int64]{}, err
	}
	if tok == nil {
		return parsedField[int64]{present: true, isNull: true}, nil
	}
	n, ok := tok.(json.Number)
	if !ok {
		return parsedField[int64]{}, fmt.Errorf("expected number")
	}
	v, err := validateInt64(n)
	if err != nil {
		return parsedField[int64]{}, fmt.Errorf("invalid integer: %w", err)
	}
	return parsedField[int64]{present: true, value: v}, nil
}

func readBoolField(dec *json.Decoder) (parsedField[bool], error) {
	tok, err := dec.Token()
	if err != nil {
		return parsedField[bool]{}, err
	}
	if tok == nil {
		return parsedField[bool]{present: true, isNull: true}, nil
	}
	b, ok := tok.(bool)
	if !ok {
		return parsedField[bool]{}, fmt.Errorf("expected boolean")
	}
	return parsedField[bool]{present: true, value: b}, nil
}

func readNullableStringField(dec *json.Decoder) (parsedField[*string], error) {
	tok, err := dec.Token()
	if err != nil {
		return parsedField[*string]{}, err
	}
	if tok == nil {
		return parsedField[*string]{present: true, isNull: true}, nil
	}
	s, ok := tok.(string)
	if !ok {
		return parsedField[*string]{}, fmt.Errorf("expected string or null")
	}
	return parsedField[*string]{present: true, value: &s}, nil
}

func readNullableInt64Field(dec *json.Decoder) (parsedField[*int64], error) {
	tok, err := dec.Token()
	if err != nil {
		return parsedField[*int64]{}, err
	}
	if tok == nil {
		return parsedField[*int64]{present: true, isNull: true}, nil
	}
	n, ok := tok.(json.Number)
	if !ok {
		return parsedField[*int64]{}, fmt.Errorf("expected number or null")
	}
	v, err := validateInt64(n)
	if err != nil {
		return parsedField[*int64]{}, fmt.Errorf("invalid integer: %w", err)
	}
	return parsedField[*int64]{present: true, value: &v}, nil
}

// ---------- Number validation ----------

func validateInt64(n json.Number) (int64, error) {
	s := n.String()
	if len(s) == 0 {
		return 0, fmt.Errorf("empty number")
	}
	if strings.ContainsAny(s, ".eE") {
		return 0, fmt.Errorf("number must be plain decimal integer")
	}
	if s == "0" {
		return 0, nil
	}
	if s == "-0" {
		return 0, fmt.Errorf("-0 is not allowed")
	}
	if s[0] == '0' && len(s) > 1 {
		return 0, fmt.Errorf("leading zero not allowed")
	}
	if s[0] == '-' {
		if len(s) < 2 {
			return 0, fmt.Errorf("invalid negative number")
		}
		if s[1] == '0' {
			return 0, fmt.Errorf("leading zero not allowed")
		}
		for _, c := range s[2:] {
			if c < '0' || c > '9' {
				return 0, fmt.Errorf("invalid character in number")
			}
		}
	} else {
		for _, c := range s[1:] {
			if c < '0' || c > '9' {
				return 0, fmt.Errorf("invalid character in number")
			}
		}
	}
	v, err := strconv.ParseInt(s, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("integer overflow or parse error: %w", err)
	}
	return v, nil
}
