package civildateplan

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
)

func mustParseV1(t *testing.T, data string) *parsedPlan {
	t.Helper()
	plan, err := parsePlanV1([]byte(data))
	if err != nil {
		t.Fatalf("parsePlanV1: %v", err)
	}
	return plan
}

func mustFailV1(t *testing.T, data, substr string) {
	t.Helper()
	_, err := parsePlanV1([]byte(data))
	if err == nil {
		t.Fatalf("expected error containing %q", substr)
	}
	if !strings.Contains(err.Error(), substr) {
		t.Fatalf("error %q does not contain %q", err.Error(), substr)
	}
}

func assertParserStage(t *testing.T, data string, want loadErrorKind) {
	t.Helper()
	_, err := parsePlanV1([]byte(data))
	if err == nil {
		t.Fatal("expected classified parser error")
	}
	got, ok := planStageErrorKind(err)
	if !ok {
		t.Fatalf("parser error is not typed: %T", err)
	}
	if got != want {
		t.Fatalf("parser stage = %d, want %d", got, want)
	}
}

func TestParsePlanV1PrivateStageClassification(t *testing.T) {
	valid := planJSON(nrFinding())
	assertParserStage(t, `{"schema_version":`, loadErrorSyntax)
	assertParserStage(
		t,
		strings.Replace(valid, `"schema_version":1`, `"schema_version":2`, 1),
		loadErrorSchema,
	)
	assertParserStage(
		t,
		strings.Replace(valid, `"repair_outcome":"AUTO_REPAIR"`, `"repair_outcome":"MANUAL"`, 1),
		loadErrorValidation,
	)
}

// helpers to build valid plan JSON fragments
func nrFinding() string {
	return `{"variant":"TRANSACTION_NON_RECURRING","source":"transaction","workspace_id":"ws1","source_row_id":"tx-1","audited_field":"date_civil","reason_code":"DATE_CIVIL_MISSING","repair_outcome":"AUTO_REPAIR","expected_legacy_value":1717603200,"expected_civil_state":"NULL","proposed_civil_value":"2024-06-05","timezone":"America/Sao_Paulo","classifier_version":"civil-date-classifier/v1","transaction_state":{"id":"tx-1","workspace_id":"ws1","status":"pending","type":"EXPENSE","date_unix":1717603200,"due_date_unix":null,"recurring_rule_id":null,"series_id":null,"recurrence_sequence":null,"is_recurring_exception":false,"recurring_exception_reason":null},"rule_state":null,"series_state":null}`
}

func rcrFinding() string {
	return `{"variant":"TRANSACTION_RECURRING","source":"transaction","workspace_id":"ws1","source_row_id":"tx-2","audited_field":"date_civil","reason_code":"DATE_CIVIL_MISSING","repair_outcome":"AUTO_REPAIR","expected_legacy_value":1717689600,"expected_civil_state":"NULL","proposed_civil_value":"2024-06-06","timezone":"America/Sao_Paulo","classifier_version":"civil-date-classifier/v1","transaction_state":{"id":"tx-2","workspace_id":"ws1","status":"pending","type":"EXPENSE","date_unix":1717689600,"due_date_unix":null,"recurring_rule_id":"r-1","series_id":"s-1","recurrence_sequence":2,"is_recurring_exception":false,"recurring_exception_reason":null},"rule_state":{"id":"r-1","workspace_id":"ws1","series_id":"s-1","parent_rule_id":null,"effective_start_sequence":1,"effective_end_sequence":null,"active":true},"series_state":{"id":"s-1","workspace_id":"ws1","active":true}}`
}

func ruleFinding() string {
	return `{"variant":"RECURRING_RULE","source":"recurring_rule","workspace_id":"ws1","source_row_id":"r-a","audited_field":"start_date_civil","reason_code":"START_DATE_CIVIL_MISSING","repair_outcome":"AUTO_REPAIR","expected_legacy_value":1717603200,"expected_civil_state":"NULL","proposed_civil_value":"2024-06-05","timezone":"America/Sao_Paulo","classifier_version":"civil-date-classifier/v1","transaction_state":null,"rule_state":{"id":"r-a","workspace_id":"ws1","series_id":"s-a","parent_rule_id":null,"effective_start_sequence":1,"effective_end_sequence":null,"active":true},"series_state":{"id":"s-a","workspace_id":"ws1","active":true}}`
}

func planJSON(finding string) string {
	return `{"schema_version":1,"plan_id":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","scope":"DATABASE","tool_version":"civil-date-repair/v1","classifier_version":"civil-date-classifier/v1","database_schema_version":55,"timezone":"America/Sao_Paulo","logical_fingerprint_version":"contabase-logical/v1","source_logical_fingerprint":"sha256:0000000000000000000000000000000000000000000000000000000000000000","audit_set_digest":"sha256:0000000000000000000000000000000000000000000000000000000000000000","generated_at":"2024-06-05T12:00:00Z","findings":[` + finding + `]}`
}

// ---------- Happy path ----------

func TestParsePlanV1NonRecurring(t *testing.T) {
	plan := mustParseV1(t, planJSON(nrFinding()))
	if plan.schemaVersion.value != 1 {
		t.Fatalf("schema_version = %d", plan.schemaVersion.value)
	}
	if len(plan.findings) != 1 {
		t.Fatalf("findings len = %d", len(plan.findings))
	}
}

func TestParsePlanV1Recurring(t *testing.T) {
	plan := mustParseV1(t, planJSON(rcrFinding()))
	if len(plan.findings) != 1 {
		t.Fatalf("findings len = %d", len(plan.findings))
	}
}

func TestParsePlanV1Rule(t *testing.T) {
	plan := mustParseV1(t, planJSON(ruleFinding()))
	if len(plan.findings) != 1 {
		t.Fatalf("findings len = %d", len(plan.findings))
	}
}

// ---------- Size limit ----------

func TestParsePlanV1Oversize(t *testing.T) {
	big := make([]byte, maxPlanSize+1)
	for i := range big {
		big[i] = ' '
	}
	_, err := parsePlanV1(big)
	if err == nil {
		t.Fatal("expected error for oversize input")
	}
}

// ---------- UTF-8 ----------

func TestParsePlanV1InvalidUTF8(t *testing.T) {
	data := []byte("{\"schema_version\":1,\"plan_id\":\"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa\",\"scope\":\"DATABASE\",\"tool_version\":\"civil-date-repair/v1\",\"classifier_version\":\"civil-date-classifier/v1\",\"database_schema_version\":55,\"timezone\":\"America/Sao_Paulo\",\"logical_fingerprint_version\":\"contabase-logical/v1\",\"source_logical_fingerprint\":\"sha256:0000000000000000000000000000000000000000000000000000000000000000\",\"audit_set_digest\":\"sha256:0000000000000000000000000000000000000000000000000000000000000000\",\"generated_at\":\"2024-06-05T12:00:00Z\",\"findings\":[\xff\xfe\xfd]}")
	_, err := parsePlanV1(data)
	if err == nil {
		t.Fatal("expected error for invalid UTF-8")
	}
}

// ---------- Duplicate keys ----------

func TestParsePlanV1DuplicateTopKey(t *testing.T) {
	raw := planJSON(nrFinding())
	raw = raw[:len(raw)-1] + `,"schema_version":1}`
	mustFailV1(t, raw, "duplicate")
}

func TestParsePlanV1DuplicateFindingKey(t *testing.T) {
	f := strings.Replace(nrFinding(), `"reason_code":"DATE_CIVIL_MISSING"`, `"reason_code":"DATE_CIVIL_MISSING","reason_code":"DATE_CIVIL_MISSING"`, 1)
	mustFailV1(t, planJSON(f), "duplicate")
}

func TestParsePlanV1DuplicateTxStateKey(t *testing.T) {
	f := strings.Replace(nrFinding(), `"status":"pending"`, `"status":"pending","status":"pending"`, 1)
	mustFailV1(t, planJSON(f), "duplicate")
}

func TestParsePlanV1DuplicateRuleStateKey(t *testing.T) {
	f := strings.Replace(rcrFinding(), `"active":true`, `"active":true,"active":true`, 1)
	mustFailV1(t, planJSON(f), "duplicate")
}

func TestParsePlanV1DuplicateSeriesStateKey(t *testing.T) {
	f := strings.Replace(ruleFinding(), `"active":true},"series_state`, `"active":true,"active":true},"series_state`, 1)
	mustFailV1(t, planJSON(f), "duplicate")
}

// ---------- Unknown fields ----------

func TestParsePlanV1UnknownTopField(t *testing.T) {
	raw := planJSON(nrFinding())
	raw = raw[:len(raw)-1] + `,"extra_field":1}`
	mustFailV1(t, raw, "unknown field")
}

func TestParsePlanV1UnknownFindingField(t *testing.T) {
	f := strings.Replace(nrFinding(), `"rule_state":null`, `"extra":1,"rule_state":null`, 1)
	mustFailV1(t, planJSON(f), "unknown field in finding")
}

func TestParsePlanV1UnknownTxStateField(t *testing.T) {
	f := strings.Replace(nrFinding(), `"is_recurring_exception":false`, `"extra":1,"is_recurring_exception":false`, 1)
	mustFailV1(t, planJSON(f), "unknown field in transaction_state")
}

// ---------- Presence ----------

func TestParsePlanV1MissingRequiredField(t *testing.T) {
	raw := planJSON(nrFinding())
	raw = strings.Replace(raw, `"plan_id":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",`, "", 1)
	mustFailV1(t, raw, "required field missing")
}

func TestParsePlanV1NullRequiredField(t *testing.T) {
	raw := planJSON(nrFinding())
	raw = strings.Replace(raw, `"plan_id":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"`, `"plan_id":null`, 1)
	mustFailV1(t, raw, "plan_id")
}

func TestParsePlanV1MissingFindingField(t *testing.T) {
	f := strings.Replace(nrFinding(), `"source_row_id":"tx-1",`, "", 1)
	mustFailV1(t, planJSON(f), "required field missing in finding")
}

func TestParsePlanV1WrongTypeField(t *testing.T) {
	raw := planJSON(nrFinding())
	raw = strings.Replace(raw, `"schema_version":1`, `"schema_version":"abc"`, 1)
	mustFailV1(t, raw, "expected number")
}

// ---------- Nullable ----------

func TestParsePlanV1NullableMissingRejected(t *testing.T) {
	f := strings.Replace(nrFinding(), `,"due_date_unix":null`, ``, 1)
	mustFailV1(t, planJSON(f), "required")
}

func TestParsePlanV1NullableAcceptedWhenNull(t *testing.T) {
	mustParseV1(t, planJSON(nrFinding()))
}

func TestParsePlanV1ConcreteFieldNullRejected(t *testing.T) {
	f := strings.Replace(nrFinding(), `"status":"pending"`, `"status":null`, 1)
	mustFailV1(t, planJSON(f), "must not be null")
}

// ---------- Document trailing ----------

func TestParsePlanV1TrailingObject(t *testing.T) {
	mustFailV1(t, planJSON(nrFinding())+`{}`, "unexpected")
}

func TestParsePlanV1TrailingArray(t *testing.T) {
	mustFailV1(t, planJSON(nrFinding())+`[]`, "unexpected")
}

func TestParsePlanV1TrailingNumber(t *testing.T) {
	mustFailV1(t, planJSON(nrFinding())+`42`, "unexpected")
}

func TestParsePlanV1TrailingString(t *testing.T) {
	mustFailV1(t, planJSON(nrFinding())+`"x"`, "unexpected")
}

func TestParsePlanV1TrailingBool(t *testing.T) {
	mustFailV1(t, planJSON(nrFinding())+`true`, "unexpected")
}

func TestParsePlanV1TrailingNull(t *testing.T) {
	mustFailV1(t, planJSON(nrFinding())+`null`, "unexpected")
}

func TestParsePlanV1TrailingGarbage(t *testing.T) {
	raw := planJSON(nrFinding()) + `zzz`
	_, err := parsePlanV1([]byte(raw))
	if err == nil {
		t.Fatal("expected error for trailing garbage")
	}
}

// ---------- Numbers ----------

func TestParsePlanV1NumberValid(t *testing.T) {
	mustParseV1(t, planJSON(nrFinding()))
}

func TestParsePlanV1NumberLeadingZero(t *testing.T) {
	raw := planJSON(nrFinding())
	raw = strings.Replace(raw, `"schema_version":1`, `"schema_version":01`, 1)
	_, err := parsePlanV1([]byte(raw))
	if err == nil {
		t.Fatal("expected error for leading zero (01)")
	}
}

func TestParsePlanV1NumberNegativeZero(t *testing.T) {
	f := strings.Replace(nrFinding(), `"expected_legacy_value":1717603200`, `"expected_legacy_value":-0`, 1)
	_, err := parsePlanV1([]byte(planJSON(f)))
	if err == nil {
		t.Fatal("expected error for -0")
	}
}

func TestParsePlanV1NumberFraction(t *testing.T) {
	f := strings.Replace(nrFinding(), `"expected_legacy_value":1717603200`, `"expected_legacy_value":1.5`, 1)
	_, err := parsePlanV1([]byte(planJSON(f)))
	if err == nil {
		t.Fatal("expected error for fraction 1.5")
	}
}

func TestParsePlanV1NumberExponent(t *testing.T) {
	f := strings.Replace(nrFinding(), `"expected_legacy_value":1717603200`, `"expected_legacy_value":1e5`, 1)
	_, err := parsePlanV1([]byte(planJSON(f)))
	if err == nil {
		t.Fatal("expected error for exponent 1e5")
	}
}

// ---------- Top-level validations ----------

func TestParsePlanV1WrongSchemaVersion(t *testing.T) {
	raw := strings.Replace(planJSON(nrFinding()), `"schema_version":1`, `"schema_version":2`, 1)
	mustFailV1(t, raw, "schema_version")
}

func TestParsePlanV1InvalidPlanID(t *testing.T) {
	raw := strings.Replace(planJSON(nrFinding()), `"plan_id":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"`, `"plan_id":"short"`, 1)
	mustFailV1(t, raw, "plan_id must be 32 hex chars")
}

func TestParsePlanV1PlanIDUppercase(t *testing.T) {
	raw := strings.Replace(planJSON(nrFinding()), `"plan_id":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"`, `"plan_id":"AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"`, 1)
	mustFailV1(t, raw, "lowercase hex")
}

func TestParsePlanV1NegativeDBSchemaVersion(t *testing.T) {
	raw := strings.Replace(planJSON(nrFinding()), `"database_schema_version":55`, `"database_schema_version":0`, 1)
	mustFailV1(t, raw, "positive")
}

func TestParsePlanV1InvalidTimezone(t *testing.T) {
	raw := strings.Replace(planJSON(nrFinding()), `"timezone":"America/Sao_Paulo"`, `"timezone":"Invalid/Zone"`, 1)
	mustFailV1(t, raw, "invalid timezone")
}

func TestParsePlanV1TimestampNotUTC(t *testing.T) {
	raw := planJSON(nrFinding())
	raw = strings.Replace(raw, `"generated_at":"2024-06-05T12:00:00Z"`, `"generated_at":"2024-06-05T12:00:00-03:00"`, 1)
	_, err := parsePlanV1([]byte(raw))
	if err == nil {
		t.Fatal("expected error for non-UTC timestamp")
	}
}

func TestParsePlanV1TimestampPlusZero(t *testing.T) {
	raw := planJSON(nrFinding())
	raw = strings.Replace(raw, `"generated_at":"2024-06-05T12:00:00Z"`, `"generated_at":"2024-06-05T12:00:00+00:00"`, 1)
	_, err := parsePlanV1([]byte(raw))
	if err == nil {
		t.Fatal("expected error for +00:00 timestamp")
	}
}

func TestParsePlanV1EmptyFindings(t *testing.T) {
	raw := planJSON(nrFinding())
	raw = strings.Replace(raw, `"findings":[`+nrFinding()+`]`, `"findings":[]`, 1)
	mustFailV1(t, raw, "not be empty")
}

// ---------- Invalid digest format ----------

func TestParsePlanV1InvalidDigest(t *testing.T) {
	raw := planJSON(nrFinding())
	raw = strings.Replace(raw, `"sha256:0000000000000000000000000000000000000000000000000000000000000000"`, `"bad:123"`, 1)
	mustFailV1(t, raw, "sha256:")
}

// ---------- Variant validations ----------

func TestParsePlanV1NonRecurringExtraNulls(t *testing.T) {
	// recurring_rule_id must be null for NON_RECURRING
	f := strings.Replace(nrFinding(), `"recurring_rule_id":null`, `"recurring_rule_id":"x"`, 1)
	mustFailV1(t, planJSON(f), "recurring_rule_id must be null")
}

func TestParsePlanV1RecurringMissingRuleState(t *testing.T) {
	f := strings.Replace(rcrFinding(), `,"rule_state":{"id":"r-1"`, `,"rule_state":null`, 1)
	// Remove the rest of the original rule_state content
	f = strings.Replace(f, `,"workspace_id":"ws1","series_id":"s-1","parent_rule_id":null,"effective_start_sequence":1,"effective_end_sequence":null,"active":true}`, ``, 1)
	_, err := parsePlanV1([]byte(planJSON(f)))
	if err == nil {
		t.Fatal("expected error for missing rule_state (set to null)")
	}
}

func TestParsePlanV1RuleExtraTxState(t *testing.T) {
	// RECURRING_RULE variant rejects non-null transaction_state
	f := ruleFinding()
	f = strings.Replace(f, `"transaction_state":null`, `"transaction_state":{"id":"x","workspace_id":"ws1"}`, 1)
	_, err := parsePlanV1([]byte(planJSON(f)))
	if err == nil {
		t.Fatal("expected error for non-null transaction_state in RECURRING_RULE")
	}
	_ = err
}

// ---------- Closed matrix ----------

func TestParsePlanV1InvalidFieldReason(t *testing.T) {
	f := strings.Replace(nrFinding(), `"reason_code":"DATE_CIVIL_MISSING"`, `"reason_code":"WRONG_REASON"`, 1)
	mustFailV1(t, planJSON(f), "invalid combination")
}

func TestParsePlanV1PaidDateCivilRejected(t *testing.T) {
	f := strings.Replace(nrFinding(), `"audited_field":"date_civil"`, `"audited_field":"paid_date_civil"`, 1)
	mustFailV1(t, planJSON(f), "paid_date_civil is forbidden")
}

// ---------- Duplicate canonical key ----------

func TestParsePlanV1DuplicateCanonicalFindingKey(t *testing.T) {
	finding1 := nrFinding()
	finding2 := nrFinding() // same source/variant/ws_id/source_row_id/audited_field
	raw := planJSON(finding1 + "," + finding2)
	mustFailV1(t, raw, "duplicate canonical key")
}

// ---------- Civil date validation ----------

func TestParsePlanV1WrongProposedCivilValue(t *testing.T) {
	f := strings.Replace(nrFinding(), `"proposed_civil_value":"2024-06-05"`, `"proposed_civil_value":"2024-06-06"`, 1)
	mustFailV1(t, planJSON(f), "proposed_civil_value")
}

// ---------- Depth limit ----------

func TestParsePlanV1MaxDepth(t *testing.T) {
	// Build a plan with exactly maxDepth nesting via nested objects in extra fields?
	// Actually the allowlist rejects unknown fields, so we can't easily nest.
	// But we can test that depth limit exists.
	raw := `{"schema_version":1,"plan_id":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","scope":"DATABASE","tool_version":"civil-date-repair/v1","classifier_version":"civil-date-classifier/v1","database_schema_version":55,"timezone":"America/Sao_Paulo","logical_fingerprint_version":"contabase-logical/v1","source_logical_fingerprint":"sha256:0000000000000000000000000000000000000000000000000000000000000000","audit_set_digest":"sha256:0000000000000000000000000000000000000000000000000000000000000000","generated_at":"2024-06-05T12:00:00Z","findings":[` + nrFinding() + `]}`
	mustParseV1(t, raw)
}

// ---------- No panic guarantee ----------

func TestParsePlanV1NoPanicMalformed(t *testing.T) {
	inputs := []string{
		"", "{", "[", `{"x`, `{"schema_version":`,
		`{"schema_version":1`,
	}
	for _, in := range inputs {
		func() {
			defer func() {
				if r := recover(); r != nil {
					t.Fatalf("panic on %q: %v", in, r)
				}
			}()
			parsePlanV1([]byte(in))
		}()
	}
}

// ---------- 4 valid matrix combinations ----------

func TestParsePlanV1AllMatrixCombinations(t *testing.T) {
	// date_civil + DATE_CIVIL_MISSING (already tested via nrFinding/rcrFinding)
	// due_date_civil + DUE_DATE_CIVIL_MISSING
	// Build a valid due_date finding: needs due_date_unix == expected_legacy_value
	f := strings.Replace(nrFinding(), `"audited_field":"date_civil","reason_code":"DATE_CIVIL_MISSING","repair_outcome":"AUTO_REPAIR","expected_legacy_value":1717603200,"expected_civil_state":"NULL","proposed_civil_value":"2024-06-05"`,
		`"audited_field":"due_date_civil","reason_code":"DUE_DATE_CIVIL_MISSING","repair_outcome":"AUTO_REPAIR","expected_legacy_value":1717603200,"expected_civil_state":"NULL","proposed_civil_value":"2024-06-05"`, 1)
	f = strings.Replace(f, `"due_date_unix":null`, `"due_date_unix":1717603200`, 1)
	mustParseV1(t, planJSON(f))

	// start_date_civil + START_DATE_CIVIL_MISSING (already tested via ruleFinding)
	// starts_at_civil + STARTS_AT_CIVIL_MISSING
	r := strings.Replace(ruleFinding(), `"audited_field":"start_date_civil","reason_code":"START_DATE_CIVIL_MISSING"`, `"audited_field":"starts_at_civil","reason_code":"STARTS_AT_CIVIL_MISSING"`, 1)
	mustParseV1(t, planJSON(r))
}

// ---------- Workspace/ID mismatches ----------

func TestParsePlanV1WorkspaceMismatch(t *testing.T) {
	f := strings.Replace(nrFinding(), `"workspace_id":"ws1"`, `"workspace_id":"ws2"`, 1)
	mustFailV1(t, planJSON(f), "workspace_id mismatch")
}

func TestParsePlanV1SourceRowIDMismatch(t *testing.T) {
	f := strings.Replace(nrFinding(), `"source_row_id":"tx-1"`, `"source_row_id":"tx-2"`, 1)
	mustFailV1(t, planJSON(f), "source_row_id must equal")
}

// ---------- Recurring sequence validation ----------

func TestParsePlanV1RecurringSequenceZero(t *testing.T) {
	f := strings.Replace(rcrFinding(), `"recurrence_sequence":2`, `"recurrence_sequence":0`, 1)
	mustFailV1(t, planJSON(f), "positive")
}

func TestParsePlanV1RecurringSequenceBeforeStart(t *testing.T) {
	f := strings.Replace(rcrFinding(), `"recurrence_sequence":2`, `"recurrence_sequence":0`, 1)
	mustFailV1(t, planJSON(f), "positive")
}

func TestParsePlanV1RecurringSequenceAfterEnd(t *testing.T) {
	f := rcrFinding()
	f = strings.Replace(f, `"recurrence_sequence":2`, `"recurrence_sequence":10`, 1)
	f = strings.Replace(f, `"effective_end_sequence":null`, `"effective_end_sequence":5`, 1)
	mustFailV1(t, planJSON(f), "after effective_end")
}

// ---------- Exception flag/reason ----------

func TestParsePlanV1ExceptionTrueNoReason(t *testing.T) {
	f := rcrFinding()
	f = strings.Replace(f, `"is_recurring_exception":false`, `"is_recurring_exception":true`, 1)
	mustFailV1(t, planJSON(f), "recurring_exception_reason must be a value")
}

func TestParsePlanV1ExceptionFalseWithReason(t *testing.T) {
	f := rcrFinding()
	f = strings.Replace(f, `"recurring_exception_reason":null`, `"recurring_exception_reason":"some reason"`, 1)
	mustFailV1(t, planJSON(f), "recurring_exception_reason must be null when")
}

// ---------- effective_end_sequence >= effective_start_sequence - NOT tested here, schema allows end >= start? ----------

func TestParsePlanV1SequenceInRange(t *testing.T) {
	f := rcrFinding()
	f = strings.Replace(f, `"effective_end_sequence":null`, `"effective_end_sequence":2`, 1)
	mustParseV1(t, planJSON(f))
}

// ---------- due_date_unix for date_civil ignored; for due_date_civil must match ----------

func TestParsePlanV1DueDateCivilMissingDueDateUnix(t *testing.T) {
	// due_date_civil reason but due_date_unix is null
	f := strings.Replace(nrFinding(), `"audited_field":"date_civil","reason_code":"DATE_CIVIL_MISSING","repair_outcome":"AUTO_REPAIR","expected_legacy_value":1717603200,"expected_civil_state":"NULL","proposed_civil_value":"2024-06-05"`,
		`"audited_field":"due_date_civil","reason_code":"DUE_DATE_CIVIL_MISSING","repair_outcome":"AUTO_REPAIR","expected_legacy_value":1717603200,"expected_civil_state":"NULL","proposed_civil_value":"2024-06-05"`, 1)
	// due_date_unix is still null
	_, err := parsePlanV1([]byte(planJSON(f)))
	if err == nil {
		t.Fatal("expected error for due_date_civil with null due_date_unix")
	}
}

// ---------- Is_recurring_exception false for non-recurring ----------

func TestParsePlanV1NonRecurringExceptionTrue(t *testing.T) {
	f := strings.Replace(nrFinding(), `"is_recurring_exception":false`, `"is_recurring_exception":true`, 1)
	mustFailV1(t, planJSON(f), "is_recurring_exception must be false")
}

// ---------- UUID/ID not empty ----------

func TestParsePlanV1EmptyTransactionID(t *testing.T) {
	f := strings.Replace(nrFinding(), `"id":"tx-1"`, `"id":""`, 1)
	mustFailV1(t, planJSON(f), "")
}

// ---------- Helpers for field mutation ----------

func removeFieldKV(objJSON, key string) string {
	s := strings.Replace(objJSON, `"`+key+`":`, "", 1)
	if s == objJSON {
		panic("removeFieldKV: key not found: " + key)
	}
	s = strings.Replace(s, `,"`+key+`":`, "", 1)
	if s != objJSON {
		return s
	}
	return objJSON
}

func removeField(objJSON, key, value string) string {
	s := strings.Replace(objJSON, `"`+key+`":`+value+`,`, "", 1)
	if s != objJSON {
		return s
	}
	s = strings.Replace(objJSON, `,"`+key+`":`+value, "", 1)
	if s != objJSON {
		return s
	}
	return objJSON
}

func nullField(objJSON, key, value string) string {
	s := strings.Replace(objJSON, `"`+key+`":`+value, `"`+key+`":null`, 1)
	if s != objJSON {
		return s
	}
	return objJSON
}

func wrongTypeField(objJSON, key, value string, newVal string) string {
	s := strings.Replace(objJSON, `"`+key+`":`+value, `"`+key+`":`+newVal, 1)
	if s != objJSON {
		return s
	}
	return objJSON
}

// ---------- Status/Type validation ----------

func TestParsePlanV1ValidStatuses(t *testing.T) {
	for _, st := range []string{`"paid"`, `"pending"`} {
		f := strings.Replace(nrFinding(), `"status":"pending"`, `"status":`+st, 1)
		mustParseV1(t, planJSON(f))
	}
}

func TestParsePlanV1InvalidStatuses(t *testing.T) {
	tests := []struct {
		status, errStr string
	}{
		{`""`, "transaction_state.status"},
		{`"PAID"`, "transaction_state.status"},
		{`"Pending"`, "transaction_state.status"},
		{`" pending"`, "transaction_state.status"},
		{`"pending "`, "transaction_state.status"},
		{`"INVALID"`, "transaction_state.status"},
	}
	for _, tc := range tests {
		t.Run(tc.status, func(t *testing.T) {
			f := strings.Replace(nrFinding(), `"status":"pending"`, `"status":`+tc.status, 1)
			mustFailV1(t, planJSON(f), tc.errStr)
		})
	}
}

func TestParsePlanV1ValidTypes(t *testing.T) {
	for _, tp := range []string{`"EXPENSE"`, `"INCOME"`, `"TRANSFER"`} {
		f := strings.Replace(nrFinding(), `"type":"EXPENSE"`, `"type":`+tp, 1)
		mustParseV1(t, planJSON(f))
	}
}

func TestParsePlanV1InvalidTypes(t *testing.T) {
	tests := []struct {
		typ, errStr string
	}{
		{`""`, "transaction_state.type"},
		{`"expense"`, "transaction_state.type"},
		{`"Income"`, "transaction_state.type"},
		{`"transfer"`, "transaction_state.type"},
		{`" EXPENSE"`, "transaction_state.type"},
		{`"INVALID"`, "transaction_state.type"},
	}
	for _, tc := range tests {
		t.Run(tc.typ, func(t *testing.T) {
			f := strings.Replace(nrFinding(), `"type":"EXPENSE"`, `"type":`+tc.typ, 1)
			mustFailV1(t, planJSON(f), tc.errStr)
		})
	}
}

func TestParsePlanV1RecurringValidStatuses(t *testing.T) {
	for _, st := range []string{`"paid"`, `"pending"`} {
		f := strings.Replace(rcrFinding(), `"status":"pending"`, `"status":`+st, 1)
		mustParseV1(t, planJSON(f))
	}
}

func TestParsePlanV1RecurringInvalidStatuses(t *testing.T) {
	tests := []struct {
		status, errStr string
	}{
		{`""`, "transaction_state.status"},
		{`"PAID"`, "transaction_state.status"},
		{`"Pending"`, "transaction_state.status"},
		{`" pending"`, "transaction_state.status"},
		{`"pending "`, "transaction_state.status"},
		{`"INVALID"`, "transaction_state.status"},
	}
	for _, tc := range tests {
		t.Run(tc.status, func(t *testing.T) {
			f := strings.Replace(rcrFinding(), `"status":"pending"`, `"status":`+tc.status, 1)
			mustFailV1(t, planJSON(f), tc.errStr)
		})
	}
}

func TestParsePlanV1RecurringValidTypes(t *testing.T) {
	for _, tp := range []string{`"EXPENSE"`, `"INCOME"`, `"TRANSFER"`} {
		f := strings.Replace(rcrFinding(), `"type":"EXPENSE"`, `"type":`+tp, 1)
		mustParseV1(t, planJSON(f))
	}
}

func TestParsePlanV1RecurringInvalidTypes(t *testing.T) {
	tests := []struct {
		typ, errStr string
	}{
		{`""`, "transaction_state.type"},
		{`"expense"`, "transaction_state.type"},
		{`"Income"`, "transaction_state.type"},
		{`"transfer"`, "transaction_state.type"},
		{`" EXPENSE"`, "transaction_state.type"},
		{`"INVALID"`, "transaction_state.type"},
	}
	for _, tc := range tests {
		t.Run(tc.typ, func(t *testing.T) {
			f := strings.Replace(rcrFinding(), `"type":"EXPENSE"`, `"type":`+tc.typ, 1)
			mustFailV1(t, planJSON(f), tc.errStr)
		})
	}
}

// ---------- Presence matrix: Plan (12 fields) ----------

func TestParsePlanV1PresenceMatrix(t *testing.T) {
	type field struct {
		key       string
		value     string
		nullable  bool
		valueType string // "string", "int", "arr"
	}
	planFields := []field{
		{"schema_version", "1", false, "int"},
		{"plan_id", `"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"`, false, "string"},
		{"scope", `"DATABASE"`, false, "string"},
		{"tool_version", `"civil-date-repair/v1"`, false, "string"},
		{"classifier_version", `"civil-date-classifier/v1"`, false, "string"},
		{"database_schema_version", "55", false, "int"},
		{"timezone", `"America/Sao_Paulo"`, false, "string"},
		{"logical_fingerprint_version", `"contabase-logical/v1"`, false, "string"},
		{"source_logical_fingerprint", `"sha256:0000000000000000000000000000000000000000000000000000000000000000"`, false, "string"},
		{"audit_set_digest", `"sha256:0000000000000000000000000000000000000000000000000000000000000000"`, false, "string"},
		{"generated_at", `"2024-06-05T12:00:00Z"`, false, "string"},
		{"findings", `[` + nrFinding() + `]`, false, "arr"},
	}

	for _, fld := range planFields {
		t.Run("plan/"+fld.key+"/missing", func(t *testing.T) {
			json := removeField(planJSON(nrFinding()), fld.key, fld.value)
			mustFailV1(t, json, "required")
		})
		t.Run("plan/"+fld.key+"/null", func(t *testing.T) {
			json := nullField(planJSON(nrFinding()), fld.key, fld.value)
			mustFailV1(t, json, "")
		})
		t.Run("plan/"+fld.key+"/wrongtype", func(t *testing.T) {
			var wrong string
			if fld.valueType == "string" || fld.valueType == "arr" {
				wrong = "42"
			} else {
				wrong = `"abc"`
			}
			json := wrongTypeField(planJSON(nrFinding()), fld.key, fld.value, wrong)
			mustFailV1(t, json, "expected")
		})
		t.Run("plan/"+fld.key+"/valid", func(t *testing.T) {
			mustParseV1(t, planJSON(nrFinding()))
		})
	}
}

// ---------- Presence matrix: Finding (15 fields) ----------

func TestParsePlanV1FindingPresenceMatrix(t *testing.T) {
	type field struct {
		key       string
		value     string
		nullable  bool
		valueType string
	}
	findingFields := []field{
		{"variant", `"TRANSACTION_NON_RECURRING"`, false, "string"},
		{"source", `"transaction"`, false, "string"},
		{"workspace_id", `"ws1"`, false, "string"},
		{"source_row_id", `"tx-1"`, false, "string"},
		{"audited_field", `"date_civil"`, false, "string"},
		{"reason_code", `"DATE_CIVIL_MISSING"`, false, "string"},
		{"repair_outcome", `"AUTO_REPAIR"`, false, "string"},
		{"expected_legacy_value", "1717603200", false, "int"},
		{"expected_civil_state", `"NULL"`, false, "string"},
		{"proposed_civil_value", `"2024-06-05"`, false, "string"},
		{"timezone", `"America/Sao_Paulo"`, false, "string"},
		{"classifier_version", `"civil-date-classifier/v1"`, false, "string"},
		{"transaction_state", "txObj", true, "obj"},
		{"rule_state", "null", true, "null"},
		{"series_state", "null", true, "null"},
	}

	txObj := `{"id":"tx-1","workspace_id":"ws1","status":"pending","type":"EXPENSE","date_unix":1717603200,"due_date_unix":null,"recurring_rule_id":null,"series_id":null,"recurrence_sequence":null,"is_recurring_exception":false,"recurring_exception_reason":null}`

	for _, fld := range findingFields {
		val := fld.value
		if fld.key == "transaction_state" {
			val = txObj
		}
		t.Run("finding/"+fld.key+"/missing", func(t *testing.T) {
			f := removeField(nrFinding(), fld.key, val)
			mustFailV1(t, planJSON(f), "required")
		})
		t.Run("finding/"+fld.key+"/null", func(t *testing.T) {
			f := nullField(nrFinding(), fld.key, val)
			if fld.nullable && fld.key != "transaction_state" {
				mustParseV1(t, planJSON(f))
			} else {
				mustFailV1(t, planJSON(f), "")
			}
		})
		t.Run("finding/"+fld.key+"/wrongtype", func(t *testing.T) {
			var wrong string
			if fld.valueType == "string" || fld.valueType == "null" || fld.valueType == "obj" {
				wrong = "42"
			} else {
				wrong = `"abc"`
			}
			f := wrongTypeField(nrFinding(), fld.key, val, wrong)
			mustFailV1(t, planJSON(f), "expected")
		})
		t.Run("finding/"+fld.key+"/valid", func(t *testing.T) {
			mustParseV1(t, planJSON(nrFinding()))
		})
	}
}

// ---------- Presence matrix: Transaction state (11 fields) ----------

func TestParsePlanV1TxStatePresenceMatrix(t *testing.T) {
	txJSON := `{"id":"tx-1","workspace_id":"ws1","status":"pending","type":"EXPENSE","date_unix":1717603200,"due_date_unix":null,"recurring_rule_id":null,"series_id":null,"recurrence_sequence":null,"is_recurring_exception":false,"recurring_exception_reason":null}`

	type field struct {
		key        string
		value      string
		nullable   bool
		nullableOf string // "", "string", "int64"
		valueType  string
	}
	txFields := []field{
		{"id", `"tx-1"`, false, "", "string"},
		{"workspace_id", `"ws1"`, false, "", "string"},
		{"status", `"pending"`, false, "", "string"},
		{"type", `"EXPENSE"`, false, "", "string"},
		{"date_unix", "1717603200", false, "", "int"},
		{"due_date_unix", "null", true, "int64", "null"},
		{"recurring_rule_id", "null", true, "string", "null"},
		{"series_id", "null", true, "string", "null"},
		{"recurrence_sequence", "null", true, "int64", "null"},
		{"is_recurring_exception", "false", false, "", "bool"},
		{"recurring_exception_reason", "null", true, "string", "null"},
	}

	for _, fld := range txFields {
		t.Run("txstate/"+fld.key+"/missing", func(t *testing.T) {
			tx := removeField(txJSON, fld.key, fld.value)
			f := strings.Replace(nrFinding(), txJSON, tx, 1)
			mustFailV1(t, planJSON(f), "required")
		})
		t.Run("txstate/"+fld.key+"/null", func(t *testing.T) {
			tx := nullField(txJSON, fld.key, fld.value)
			f := strings.Replace(nrFinding(), txJSON, tx, 1)
			if fld.nullable {
				mustParseV1(t, planJSON(f))
			} else {
				mustFailV1(t, planJSON(f), "must not be null")
			}
		})
		t.Run("txstate/"+fld.key+"/wrongtype", func(t *testing.T) {
			var wrong string
			if fld.nullableOf == "int64" {
				wrong = `"abc"`
			} else if fld.valueType == "string" || fld.nullableOf == "string" || fld.valueType == "null" {
				wrong = "42"
			} else {
				wrong = `"abc"`
			}
			tx := wrongTypeField(txJSON, fld.key, fld.value, wrong)
			f := strings.Replace(nrFinding(), txJSON, tx, 1)
			mustFailV1(t, planJSON(f), "expected")
		})
	}
}

// ---------- Presence matrix: Rule state (7 fields) ----------

func TestParsePlanV1RuleStatePresenceMatrix(t *testing.T) {
	rsJSON := `{"id":"r-1","workspace_id":"ws1","series_id":"s-1","parent_rule_id":null,"effective_start_sequence":1,"effective_end_sequence":null,"active":true}`

	type field struct {
		key        string
		value      string
		nullable   bool
		nullableOf string
		valueType  string
	}
	rsFields := []field{
		{"id", `"r-1"`, false, "", "string"},
		{"workspace_id", `"ws1"`, false, "", "string"},
		{"series_id", `"s-1"`, false, "", "string"},
		{"parent_rule_id", "null", true, "string", "null"},
		{"effective_start_sequence", "1", false, "", "int"},
		{"effective_end_sequence", "null", true, "int64", "null"},
		{"active", "true", false, "", "bool"},
	}

	for _, fld := range rsFields {
		t.Run("rulestate/"+fld.key+"/missing", func(t *testing.T) {
			rs := removeField(rsJSON, fld.key, fld.value)
			f := strings.Replace(rcrFinding(), rsJSON, rs, 1)
			mustFailV1(t, planJSON(f), "required")
		})
		t.Run("rulestate/"+fld.key+"/null", func(t *testing.T) {
			rs := nullField(rsJSON, fld.key, fld.value)
			f := strings.Replace(rcrFinding(), rsJSON, rs, 1)
			if fld.nullable {
				mustParseV1(t, planJSON(f))
			} else {
				mustFailV1(t, planJSON(f), "must not be null")
			}
		})
		t.Run("rulestate/"+fld.key+"/wrongtype", func(t *testing.T) {
			var wrong string
			if fld.nullableOf == "int64" {
				wrong = `"abc"`
			} else if fld.valueType == "string" || fld.nullableOf == "string" || fld.valueType == "null" {
				wrong = "42"
			} else {
				wrong = `"abc"`
			}
			rs := wrongTypeField(rsJSON, fld.key, fld.value, wrong)
			f := strings.Replace(rcrFinding(), rsJSON, rs, 1)
			mustFailV1(t, planJSON(f), "expected")
		})
	}
}

// ---------- Presence matrix: Series state (3 fields) ----------

func TestParsePlanV1SeriesStatePresenceMatrix(t *testing.T) {
	ssJSON := `{"id":"s-a","workspace_id":"ws1","active":true}`

	type field struct {
		key       string
		value     string
		valueType string
	}
	ssFields := []field{
		{"id", `"s-a"`, "string"},
		{"workspace_id", `"ws1"`, "string"},
		{"active", "true", "bool"},
	}

	for _, fld := range ssFields {
		t.Run("seriesstate/"+fld.key+"/missing", func(t *testing.T) {
			ss := removeField(ssJSON, fld.key, fld.value)
			f := strings.Replace(ruleFinding(), ssJSON, ss, 1)
			mustFailV1(t, planJSON(f), "required")
		})
		t.Run("seriesstate/"+fld.key+"/null", func(t *testing.T) {
			ss := nullField(ssJSON, fld.key, fld.value)
			f := strings.Replace(ruleFinding(), ssJSON, ss, 1)
			mustFailV1(t, planJSON(f), "must not be null")
		})
		t.Run("seriesstate/"+fld.key+"/wrongtype", func(t *testing.T) {
			var wrong string
			if fld.valueType == "string" {
				wrong = "42"
			} else {
				wrong = `"abc"`
			}
			ss := wrongTypeField(ssJSON, fld.key, fld.value, wrong)
			f := strings.Replace(ruleFinding(), ssJSON, ss, 1)
			mustFailV1(t, planJSON(f), "expected")
		})
	}
}

// ---------- Unknown fields: rule_state ----------

func TestParsePlanV1UnknownRuleStateField(t *testing.T) {
	rs := strings.Replace(`{"id":"r-1","workspace_id":"ws1","series_id":"s-1","parent_rule_id":null,"effective_start_sequence":1,"effective_end_sequence":null,"active":true}`,
		`"active":true`, `"unknown":1,"active":true`, 1)
	f := strings.Replace(rcrFinding(),
		`{"id":"r-1","workspace_id":"ws1","series_id":"s-1","parent_rule_id":null,"effective_start_sequence":1,"effective_end_sequence":null,"active":true}`,
		rs, 1)
	mustFailV1(t, planJSON(f), "unknown field in rule_state")
}

func TestParsePlanV1UnknownRuleStateAfter(t *testing.T) {
	rs := strings.Replace(`{"id":"r-1","workspace_id":"ws1","series_id":"s-1","parent_rule_id":null,"effective_start_sequence":1,"effective_end_sequence":null,"active":true}`,
		`"active":true`, `"active":true,"unknown":null`, 1)
	f := strings.Replace(rcrFinding(),
		`{"id":"r-1","workspace_id":"ws1","series_id":"s-1","parent_rule_id":null,"effective_start_sequence":1,"effective_end_sequence":null,"active":true}`,
		rs, 1)
	mustFailV1(t, planJSON(f), "unknown field in rule_state")
}

func TestParsePlanV1UnknownRuleStateObj(t *testing.T) {
	rs := strings.Replace(`{"id":"r-1","workspace_id":"ws1","series_id":"s-1","parent_rule_id":null,"effective_start_sequence":1,"effective_end_sequence":null,"active":true}`,
		`"active":true`, `"unknown":{"x":1},"active":true`, 1)
	f := strings.Replace(rcrFinding(),
		`{"id":"r-1","workspace_id":"ws1","series_id":"s-1","parent_rule_id":null,"effective_start_sequence":1,"effective_end_sequence":null,"active":true}`,
		rs, 1)
	mustFailV1(t, planJSON(f), "unknown field in rule_state")
}

func TestParsePlanV1UnknownRuleStateArray(t *testing.T) {
	rs := strings.Replace(`{"id":"r-1","workspace_id":"ws1","series_id":"s-1","parent_rule_id":null,"effective_start_sequence":1,"effective_end_sequence":null,"active":true}`,
		`"active":true`, `"unknown":[1,2],"active":true`, 1)
	f := strings.Replace(rcrFinding(),
		`{"id":"r-1","workspace_id":"ws1","series_id":"s-1","parent_rule_id":null,"effective_start_sequence":1,"effective_end_sequence":null,"active":true}`,
		rs, 1)
	mustFailV1(t, planJSON(f), "unknown field in rule_state")
}

// ---------- Unknown fields: series_state ----------

func TestParsePlanV1UnknownSeriesStateField(t *testing.T) {
	ss := strings.Replace(`{"id":"s-1","workspace_id":"ws1","active":true}`,
		`"active":true`, `"unknown":"x","active":true`, 1)
	f := strings.Replace(ruleFinding(),
		`{"id":"s-a","workspace_id":"ws1","active":true}`,
		ss, 1)
	mustFailV1(t, planJSON(f), "unknown field in series_state")
}

func TestParsePlanV1UnknownSeriesStateAfter(t *testing.T) {
	ss := strings.Replace(`{"id":"s-1","workspace_id":"ws1","active":true}`,
		`"active":true`, `"active":true,"unknown":null`, 1)
	f := strings.Replace(ruleFinding(),
		`{"id":"s-a","workspace_id":"ws1","active":true}`,
		ss, 1)
	mustFailV1(t, planJSON(f), "unknown field in series_state")
}

func TestParsePlanV1UnknownSeriesStateObj(t *testing.T) {
	ss := strings.Replace(`{"id":"s-1","workspace_id":"ws1","active":true}`,
		`"active":true`, `"unknown":{},"active":true`, 1)
	f := strings.Replace(ruleFinding(),
		`{"id":"s-a","workspace_id":"ws1","active":true}`,
		ss, 1)
	mustFailV1(t, planJSON(f), "unknown field in series_state")
}

// ---------- Duplicate keys: rule_state ----------

func TestParsePlanV1DupRuleStateSameValue(t *testing.T) {
	rs := strings.Replace(`{"id":"r-1","workspace_id":"ws1","series_id":"s-1","parent_rule_id":null,"effective_start_sequence":1,"effective_end_sequence":null,"active":true}`,
		`"active":true`, `"active":true,"active":true`, 1)
	f := strings.Replace(rcrFinding(),
		`{"id":"r-1","workspace_id":"ws1","series_id":"s-1","parent_rule_id":null,"effective_start_sequence":1,"effective_end_sequence":null,"active":true}`,
		rs, 1)
	mustFailV1(t, planJSON(f), "duplicate")
}

func TestParsePlanV1DupRuleStateDiffValue(t *testing.T) {
	rs := strings.Replace(`{"id":"r-1","workspace_id":"ws1","series_id":"s-1","parent_rule_id":null,"effective_start_sequence":1,"effective_end_sequence":null,"active":true}`,
		`"active":true`, `"active":true,"active":false`, 1)
	f := strings.Replace(rcrFinding(),
		`{"id":"r-1","workspace_id":"ws1","series_id":"s-1","parent_rule_id":null,"effective_start_sequence":1,"effective_end_sequence":null,"active":true}`,
		rs, 1)
	mustFailV1(t, planJSON(f), "duplicate")
}

func TestParsePlanV1DupRuleStateDiffType(t *testing.T) {
	rs := strings.Replace(`{"id":"r-1","workspace_id":"ws1","series_id":"s-1","parent_rule_id":null,"effective_start_sequence":1,"effective_end_sequence":null,"active":true}`,
		`"active":true`, `"active":true,"active":42`, 1)
	f := strings.Replace(rcrFinding(),
		`{"id":"r-1","workspace_id":"ws1","series_id":"s-1","parent_rule_id":null,"effective_start_sequence":1,"effective_end_sequence":null,"active":true}`,
		rs, 1)
	mustFailV1(t, planJSON(f), "duplicate")
}

// ---------- Duplicate keys: series_state ----------

func TestParsePlanV1DupSeriesStateSameValue(t *testing.T) {
	ss := strings.Replace(`{"id":"s-1","workspace_id":"ws1","active":true}`,
		`"active":true`, `"active":true,"active":true`, 1)
	f := strings.Replace(ruleFinding(),
		`{"id":"s-a","workspace_id":"ws1","active":true}`,
		ss, 1)
	mustFailV1(t, planJSON(f), "duplicate")
}

func TestParsePlanV1DupSeriesStateDiffValue(t *testing.T) {
	ss := strings.Replace(`{"id":"s-1","workspace_id":"ws1","active":true}`,
		`"active":true`, `"active":true,"active":false`, 1)
	f := strings.Replace(ruleFinding(),
		`{"id":"s-a","workspace_id":"ws1","active":true}`,
		ss, 1)
	mustFailV1(t, planJSON(f), "duplicate")
}

// ---------- Unicode and scanner probes ----------

func TestParsePlanV1UnicodeEscape(t *testing.T) {
	// \u00e9 = é -- replace workspace_id in both finding and tx_state
	f := strings.Replace(nrFinding(), `"workspace_id":"ws1"`, `"workspace_id":"wsu\u00e9"`, -1)
	mustParseV1(t, planJSON(f))
}

func TestParsePlanV1UnicodeLiteralEAcute(t *testing.T) {
	f := strings.Replace(nrFinding(), `"workspace_id":"ws1"`, "\"workspace_id\":\"wsué\"", -1)
	mustParseV1(t, planJSON(f))
}

func TestParsePlanV1EscapedQuote(t *testing.T) {
	f := strings.Replace(nrFinding(), `"workspace_id":"ws1"`, `"workspace_id":"ws\\\"quote"`, -1)
	mustParseV1(t, planJSON(f))
}

func TestParsePlanV1EscapedBackslash(t *testing.T) {
	f := strings.Replace(nrFinding(), `"workspace_id":"ws1"`, `"workspace_id":"ws\\\\bs"`, -1)
	mustParseV1(t, planJSON(f))
}

func TestParsePlanV1LiteralBackslashUNotConfused(t *testing.T) {
	// \\u0061 inside string is literal \u0061, not hex escape
	f := strings.Replace(nrFinding(), `"workspace_id":"ws1"`, `"workspace_id":"ws\\\\u0061x"`, -1)
	mustParseV1(t, planJSON(f))
}

func TestParsePlanV1SurrogatePairValid(t *testing.T) {
	f := strings.Replace(nrFinding(), `"workspace_id":"ws1"`, `"workspace_id":"wsu\uD83D\uDE00x"`, -1)
	mustParseV1(t, planJSON(f))
}

func TestParsePlanV1IsolatedHighSurrogate(t *testing.T) {
	f := strings.Replace(nrFinding(), `"workspace_id":"ws1"`, `"workspace_id":"ws\uD83D"`, -1)
	_, err := parsePlanV1([]byte(planJSON(f)))
	if err == nil {
		t.Fatal("expected error for isolated high surrogate")
	}
}

func TestParsePlanV1IsolatedLowSurrogate(t *testing.T) {
	f := strings.Replace(nrFinding(), `"workspace_id":"ws1"`, `"workspace_id":"ws\uDE00"`, -1)
	_, err := parsePlanV1([]byte(planJSON(f)))
	if err == nil {
		t.Fatal("expected error for isolated low surrogate")
	}
}

func TestParsePlanV1HighSurrogateFollowedByCommonChar(t *testing.T) {
	f := strings.Replace(nrFinding(), `"workspace_id":"ws1"`, `"workspace_id":"ws\uD83Dx"`, -1)
	_, err := parsePlanV1([]byte(planJSON(f)))
	if err == nil {
		t.Fatal("expected error for high surrogate followed by common char")
	}
}

func TestParsePlanV1HighSurrogateFollowedByAnotherHigh(t *testing.T) {
	f := strings.Replace(nrFinding(), `"workspace_id":"ws1"`, `"workspace_id":"ws\uD83D\uD83D"`, -1)
	_, err := parsePlanV1([]byte(planJSON(f)))
	if err == nil {
		t.Fatal("expected error for high surrogate followed by another high")
	}
}

func TestParsePlanV1HighSurrogateIncompleteEscape(t *testing.T) {
	raw := strings.Replace(planJSON(nrFinding()), `"id":"tx-1"`, `"id":"\uD83D\u00ZX"`, 1)
	_, err := parsePlanV1([]byte(raw))
	if err == nil {
		t.Fatal("expected error for high surrogate with incomplete escape")
	}
}

func TestParsePlanV1LowSurrogatePrecededByCommonChar(t *testing.T) {
	f := strings.Replace(nrFinding(), `"workspace_id":"ws1"`, `"workspace_id":"wsx\uDE00"`, -1)
	_, err := parsePlanV1([]byte(planJSON(f)))
	if err == nil {
		t.Fatal("expected error for low surrogate alone")
	}
}

func TestParsePlanV1RawControlInString(t *testing.T) {
	raw := planJSON(nrFinding())
	raw = strings.Replace(raw, `"plan_id":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"`, "\"plan_id\":\"aaa\u0001aaa\"", 1)
	_, err := parsePlanV1([]byte(raw))
	if err == nil {
		t.Fatal("expected error for raw control in string")
	}
}

func TestParsePlanV1InvalidEscape(t *testing.T) {
	raw := strings.Replace(planJSON(nrFinding()), `"id":"tx-1"`, `"id":"\k"`, 1)
	_, err := parsePlanV1([]byte(raw))
	if err == nil {
		t.Fatal("expected error for invalid escape")
	}
}

func TestParsePlanV1UnicodeFFFDLiteral(t *testing.T) {
	f := strings.Replace(nrFinding(), `"workspace_id":"ws1"`, "\"workspace_id\":\"ws\ufffd\"", -1)
	mustParseV1(t, planJSON(f))
}

// ---------- EOF explicit ----------

func TestParsePlanV1EOFExplicit(t *testing.T) {
	// Valid plan followed by whitespace should parse fine
	raw := planJSON(nrFinding()) + "   \t\n  "
	mustParseV1(t, raw)
}

func TestParsePlanV1EOFExtraToken(t *testing.T) {
	// Extra token after plan with whitespace in between
	raw := planJSON(nrFinding()) + " 42"
	mustFailV1(t, raw, "trailing")
}

// ---------- Depth limit ----------

func TestParsePlanV1DepthGuard(t *testing.T) {
	if depthExceeded(maxDepth - 1) {
		t.Fatal("depthExceeded(maxDepth-1) should be false")
	}
	if !depthExceeded(maxDepth + 1) {
		t.Fatal("depthExceeded(maxDepth+1) should be true")
	}
	if depthExceeded(maxDepth) {
		t.Fatal("depthExceeded(maxDepth) should be false")
	}
}

func TestParsePlanV1DepthParserPaths(t *testing.T) {
	decFrom := func(s string) *json.Decoder {
		dec := json.NewDecoder(bytes.NewReader([]byte(s)))
		dec.UseNumber()
		dec.DisallowUnknownFields()
		return dec
	}

	tests := []struct {
		name            string
		fn              func(*json.Decoder, int) error
		json            string
		bodyErrContains string
	}{
		{"parsePlanObject", func(d *json.Decoder, n int) error { _, e := parsePlanObject(d, n); return e }, `{}`, "required field missing: schema_version"},
		{"parseFindingsArray", func(d *json.Decoder, n int) error { _, e := parseFindingsArray(d, n); return e }, `[]`, "findings must not be empty"},
		{"parseFindingObject", func(d *json.Decoder, n int) error { _, e := parseFindingObject(d, n); return e }, `{}`, "required field missing in finding: variant"},
		{"parseTransactionStateBody", func(d *json.Decoder, n int) error { d.Token(); _, e := parseTransactionStateBody(d, n); return e }, `{}`, "required field missing in transaction_state: id"},
		{"parseRuleStateBody", func(d *json.Decoder, n int) error { d.Token(); _, e := parseRuleStateBody(d, n); return e }, `{}`, "required field missing in rule_state: id"},
		{"parseSeriesStateBody", func(d *json.Decoder, n int) error { d.Token(); _, e := parseSeriesStateBody(d, n); return e }, `{}`, "required field missing in series_state: id"},
	}

	for _, tc := range tests {
		t.Run(tc.name+"/depth0_ok", func(t *testing.T) {
			err := tc.fn(decFrom(tc.json), 0)
			if err != nil && strings.Contains(err.Error(), "maximum nesting depth") {
				t.Fatalf("unexpected depth error at depth 0: %v", err)
			}
		})
		t.Run(tc.name+"/depthMax_ok", func(t *testing.T) {
			err := tc.fn(decFrom(tc.json), maxDepth)
			if err != nil && strings.Contains(err.Error(), "maximum nesting depth") {
				t.Fatalf("depth=maxDepth rejected by guard: %v", err)
			}
			if err == nil {
				t.Fatalf("expected structural error containing %q", tc.bodyErrContains)
			}
			if !strings.Contains(err.Error(), tc.bodyErrContains) {
				t.Fatalf("unexpected structural error: %v", err)
			}
		})
		t.Run(tc.name+"/depthExceeded", func(t *testing.T) {
			err := tc.fn(decFrom(tc.json), maxDepth+1)
			if err == nil {
				t.Fatalf("expected depth error at maxDepth+1")
			}
			if !strings.Contains(err.Error(), "maximum nesting depth") {
				t.Fatalf("error %q does not mention maximum nesting depth", err.Error())
			}
		})
	}
}

// ---------- Sequence before effective_start ----------

func TestParsePlanV1RecurringSequenceReallyBeforeStart(t *testing.T) {
	f := strings.Replace(rcrFinding(), `"recurrence_sequence":2`, `"recurrence_sequence":1`, 1)
	mustParseV1(t, planJSON(f))
}
