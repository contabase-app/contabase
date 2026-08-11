package civildateplan

import (
	"encoding/hex"
	"reflect"
	"strings"
	"sync"
	"testing"
)

const (
	compactPreparedDigestHex = "e4608569e8f093b6933cdb447c5a18fb341af27b70031cb89083389eeafa924b"
	fullPreparedDigestHex    = "cedc098b3e2ee10cc2c8201b6b32119ed61e562fc84a32a50893a02be66cd68d"
)

func planJSONWithFindings(findings ...string) string {
	return strings.Replace(
		planJSON(""),
		`"findings":[]`,
		`"findings":[`+strings.Join(findings, ",")+`]`,
		1,
	)
}

func fullGoldenPlanJSON() string {
	nonRecurring := strings.ReplaceAll(nrFinding(), "tx-1", "tx-nr-é")
	recurring := strings.NewReplacer(
		`"source_row_id":"tx-2"`, `"source_row_id":"tx-rcr"`,
		`"audited_field":"date_civil"`, `"audited_field":"due_date_civil"`,
		`"reason_code":"DATE_CIVIL_MISSING"`, `"reason_code":"DUE_DATE_CIVIL_MISSING"`,
		`"expected_legacy_value":1717689600`, `"expected_legacy_value":1717603200`,
		`"proposed_civil_value":"2024-06-06"`, `"proposed_civil_value":"2024-06-05"`,
		`"id":"tx-2"`, `"id":"tx-rcr"`,
		`"type":"EXPENSE"`, `"type":"INCOME"`,
		`"date_unix":1717689600`, `"date_unix":1717603200`,
		`"due_date_unix":null`, `"due_date_unix":1717603200`,
		`"is_recurring_exception":false`, `"is_recurring_exception":true`,
		`"recurring_exception_reason":null`, `"recurring_exception_reason":"adjusted"`,
		`"parent_rule_id":null`, `"parent_rule_id":"r-parent"`,
		`"effective_end_sequence":null`, `"effective_end_sequence":12`,
	).Replace(rcrFinding())
	rule := strings.ReplaceAll(ruleFinding(), `"active":true`, `"active":false`)
	return planJSONWithFindings(nonRecurring, recurring, rule)
}

func assertPrepareFailure(
	t *testing.T,
	data []byte,
	wantKind loadErrorKind,
) {
	t.Helper()
	state, digest, err := preparePlanBytes(data)
	if err == nil {
		t.Fatal("preparePlanBytes unexpectedly succeeded")
	}
	if !reflect.DeepEqual(state, authorizedPlanState{}) {
		t.Fatalf("error returned non-zero state: %+v", state)
	}
	if digest != ([32]byte{}) {
		t.Fatalf("error returned non-zero digest: %x", digest)
	}
	classified, ok := err.(*loadError)
	if !ok || classified == nil {
		t.Fatalf("error is not a private loadError: %T", err)
	}
	if classified.kind != wantKind {
		t.Fatalf("error kind = %d, want %d", classified.kind, wantKind)
	}
}

func TestPreparePlanBytesCompactGolden(t *testing.T) {
	state, digest, err := preparePlanBytes([]byte(planJSON(nrFinding())))
	if err != nil {
		t.Fatalf("preparePlanBytes: %v", err)
	}
	if !reflect.DeepEqual(state, *simpleState()) {
		t.Fatalf("prepared compact state differs from frozen state")
	}
	if got := hex.EncodeToString(digest[:]); got != compactPreparedDigestHex {
		t.Fatalf("digest = %s, want %s", got, compactPreparedDigestHex)
	}
}

func TestPreparePlanBytesCompleteGolden(t *testing.T) {
	state, digest, err := preparePlanBytes([]byte(fullGoldenPlanJSON()))
	if err != nil {
		t.Fatalf("preparePlanBytes: %v", err)
	}
	if !reflect.DeepEqual(state, *fullGoldenState()) {
		t.Fatalf("prepared complete state differs from frozen state\n got: %#v\nwant: %#v", state, *fullGoldenState())
	}
	if got := hex.EncodeToString(digest[:]); got != fullPreparedDigestHex {
		t.Fatalf("digest = %s, want %s", got, fullPreparedDigestHex)
	}
}

func TestPreparePlanBytesAllVariantsAndOptionals(t *testing.T) {
	state, _, err := preparePlanBytes([]byte(fullGoldenPlanJSON()))
	if err != nil {
		t.Fatalf("preparePlanBytes: %v", err)
	}
	if len(state.findings) != 3 {
		t.Fatalf("findings = %d, want 3", len(state.findings))
	}
	if state.findings[0].variant != variantNonRecurring ||
		state.findings[1].variant != variantRecurring ||
		state.findings[2].variant != variantRule {
		t.Fatalf("unexpected variant order: %+v", state.findings)
	}
	if _, ok := state.findings[1].transactionState.dueDateUnix.Value(); !ok {
		t.Fatal("present due date optional was not preserved")
	}
	if _, ok := state.findings[1].transactionState.recurringExceptionReason.Value(); !ok {
		t.Fatal("present optionals were not preserved")
	}
	if !state.findings[0].transactionState.dueDateUnix.IsNull() ||
		!state.findings[2].ruleState.parentRuleID.IsNull() {
		t.Fatal("null optionals were not preserved")
	}
	if state.findings[0].sourceRowID != "tx-nr-é" {
		t.Fatalf("Unicode source row id not preserved: %q", state.findings[0].sourceRowID)
	}
}

func TestPreparePlanBytesFormattingAndFieldOrderIndependent(t *testing.T) {
	compact := []byte(planJSON(nrFinding()))
	indented := []byte("\n  " + strings.ReplaceAll(string(compact), ",", ",\n  ") + "\n")
	reordered := strings.Replace(string(compact), `{"schema_version":1,`, `{`, 1)
	reordered = strings.TrimSuffix(reordered, "}") + `,"schema_version":1}`

	baseState, baseDigest, err := preparePlanBytes(compact)
	if err != nil {
		t.Fatalf("prepare compact: %v", err)
	}
	for name, data := range map[string][]byte{
		"whitespace":  indented,
		"field order": []byte(reordered),
	} {
		t.Run(name, func(t *testing.T) {
			state, digest, err := preparePlanBytes(data)
			if err != nil {
				t.Fatalf("preparePlanBytes: %v", err)
			}
			if !reflect.DeepEqual(state, baseState) || digest != baseDigest {
				t.Fatal("formatting changed semantic state or digest")
			}
		})
	}
}

func TestPreparePlanBytesSemanticChangeChangesDigest(t *testing.T) {
	base := []byte(planJSON(nrFinding()))
	changed := []byte(strings.Replace(
		string(base),
		`"plan_id":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"`,
		`"plan_id":"baaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"`,
		1,
	))
	_, baseDigest, err := preparePlanBytes(base)
	if err != nil {
		t.Fatalf("prepare base: %v", err)
	}
	_, changedDigest, err := preparePlanBytes(changed)
	if err != nil {
		t.Fatalf("prepare changed: %v", err)
	}
	if baseDigest == changedDigest {
		t.Fatal("semantic change did not change digest")
	}
}

func TestPreparePlanBytesInputBufferIndependent(t *testing.T) {
	data := []byte(fullGoldenPlanJSON())
	state, digest, err := preparePlanBytes(data)
	if err != nil {
		t.Fatalf("preparePlanBytes: %v", err)
	}
	wantState := *fullGoldenState()
	for i := range data {
		data[i] = byte(i)
	}
	if !reflect.DeepEqual(state, wantState) {
		t.Fatal("prepared state changed after input buffer mutation")
	}
	recomputed, err := computeAuthorizedPlanDigestV1(&state)
	if err != nil {
		t.Fatalf("recompute digest: %v", err)
	}
	if recomputed != digest {
		t.Fatal("prepared digest changed after input buffer mutation")
	}
}

func TestPreparePlanBytesErrorsClassifiedAndZero(t *testing.T) {
	valid := planJSON(nrFinding())
	tests := []struct {
		name string
		data []byte
		kind loadErrorKind
	}{
		{"nil", nil, loadErrorSyntax},
		{"empty", []byte{}, loadErrorSyntax},
		{"whitespace", []byte(" \n\t"), loadErrorSyntax},
		{"invalid UTF-8", []byte{0xff, 0xfe}, loadErrorSyntax},
		{"invalid JSON", []byte(`{"schema_version":`), loadErrorSyntax},
		{"trailing data", []byte(valid + `{}`), loadErrorSyntax},
		{"duplicate key", []byte(strings.Replace(valid, `"schema_version":1`, `"schema_version":1,"schema_version":1`, 1)), loadErrorSyntax},
		{"unknown field", []byte(strings.TrimSuffix(valid, "}") + `,"unknown":true}`), loadErrorSyntax},
		{"invalid number", []byte(strings.Replace(valid, `"database_schema_version":55`, `"database_schema_version":1e3`, 1)), loadErrorSyntax},
		{"future schema", []byte(strings.Replace(valid, `"schema_version":1`, `"schema_version":2`, 1)), loadErrorSchema},
		{"wrong tool", []byte(strings.Replace(valid, `"tool_version":"civil-date-repair/v1"`, `"tool_version":"civil-date-repair/v2"`, 1)), loadErrorSchema},
		{"wrong classifier", []byte(strings.Replace(valid, `"classifier_version":"civil-date-classifier/v1"`, `"classifier_version":"civil-date-classifier/v2"`, 1)), loadErrorSchema},
		{"wrong fingerprint version", []byte(strings.Replace(valid, `"logical_fingerprint_version":"contabase-logical/v1"`, `"logical_fingerprint_version":"contabase-logical/v2"`, 1)), loadErrorSchema},
		{"invalid finding", []byte(strings.Replace(valid, `"repair_outcome":"AUTO_REPAIR"`, `"repair_outcome":"MANUAL"`, 1)), loadErrorValidation},
		{"semantic workspace mismatch", []byte(strings.Replace(valid, `"workspace_id":"ws1"`, `"workspace_id":"other"`, 1)), loadErrorValidation},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			assertPrepareFailure(t, tc.data, tc.kind)
		})
	}
}

func TestPreparePlanBytesErrorPrecedence(t *testing.T) {
	valid := planJSON(nrFinding())
	semanticInvalid := strings.Replace(
		valid,
		`"repair_outcome":"AUTO_REPAIR"`,
		`"repair_outcome":"MANUAL"`,
		1,
	)
	tests := []struct {
		name string
		data string
		kind loadErrorKind
	}{
		{
			name: "syntax before schema",
			data: strings.Replace(
				strings.Replace(valid, `"schema_version":1`, `"schema_version":2`, 1),
				`"database_schema_version":55`,
				`"database_schema_version":1e3`,
				1,
			),
			kind: loadErrorSyntax,
		},
		{
			name: "schema before semantics",
			data: strings.Replace(
				semanticInvalid,
				`"schema_version":1`,
				`"schema_version":2`,
				1,
			),
			kind: loadErrorSchema,
		},
		{
			name: "unknown field before semantics",
			data: strings.TrimSuffix(semanticInvalid, "}") + `,"unknown":true}`,
			kind: loadErrorSyntax,
		},
		{
			name: "duplicate key before semantics",
			data: strings.Replace(
				semanticInvalid,
				`"schema_version":1`,
				`"schema_version":1,"schema_version":1`,
				1,
			),
			kind: loadErrorSyntax,
		},
		{
			name: "top-level protocol before finding semantics",
			data: strings.Replace(
				semanticInvalid,
				`"tool_version":"civil-date-repair/v1"`,
				`"tool_version":"civil-date-repair/v2"`,
				1,
			),
			kind: loadErrorSchema,
		},
		{
			name: "malformed JSON before schema",
			data: `{"schema_version":2`,
			kind: loadErrorSyntax,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			assertPrepareFailure(t, []byte(tc.data), tc.kind)
		})
	}
}

func TestPreparePlanBytesPrivateErrorMessagesAreSafe(t *testing.T) {
	const sensitive = "workspace-secret"
	tests := []struct {
		kind    loadErrorKind
		message string
	}{
		{loadErrorSyntax, "invalid plan syntax"},
		{loadErrorSchema, "incompatible plan schema"},
		{loadErrorValidation, "invalid plan data"},
		{loadErrorCanonicalization, "plan canonicalization failed"},
		{loadErrorInternalInvariant, "internal plan invariant failed"},
	}
	for _, tc := range tests {
		err := newLoadError(tc.kind)
		if err.Error() != tc.message || strings.Contains(err.Error(), sensitive) {
			t.Fatalf("unsafe message for kind %d: %q", tc.kind, err.Error())
		}
		if _, ok := interface{}(err).(interface{ Unwrap() error }); ok {
			t.Fatalf("loadError kind %d must not expose Unwrap", tc.kind)
		}
	}
}

func TestFinalizePreparedPlanStateInvariantFailureReturnsZero(t *testing.T) {
	state := *simpleState()
	state.scope = "INVALID"

	gotState, digest, err := finalizePreparedPlanState(state)
	if !reflect.DeepEqual(gotState, authorizedPlanState{}) {
		t.Fatalf("invariant error returned non-zero state: %+v", gotState)
	}
	if digest != ([32]byte{}) {
		t.Fatalf("invariant error returned non-zero digest: %x", digest)
	}
	classified, ok := err.(*loadError)
	if !ok || classified.kind != loadErrorInternalInvariant {
		t.Fatalf("error = %T %v, want private internal invariant error", err, err)
	}
	if err.Error() != "internal plan invariant failed" {
		t.Fatalf("error message = %q", err.Error())
	}
}

func TestFinalizePreparedPlanStateCanonicalizationFailureReturnsZero(t *testing.T) {
	state := *simpleState()
	invalidUTF8 := string([]byte{0xff})
	state.findings[0].sourceRowID = invalidUTF8
	state.findings[0].transactionState.id = invalidUTF8
	if !state.invariantsValid() {
		t.Fatal("fixture must pass independent state invariants")
	}

	gotState, digest, err := finalizePreparedPlanState(state)
	if !reflect.DeepEqual(gotState, authorizedPlanState{}) {
		t.Fatalf("canonicalization error returned non-zero state: %+v", gotState)
	}
	if digest != ([32]byte{}) {
		t.Fatalf("canonicalization error returned non-zero digest: %x", digest)
	}
	classified, ok := err.(*loadError)
	if !ok || classified.kind != loadErrorCanonicalization {
		t.Fatalf("error = %T %v, want private canonicalization error", err, err)
	}
	if err.Error() != "plan canonicalization failed" {
		t.Fatalf("error message = %q", err.Error())
	}
	if _, ok := interface{}(err).(interface{ Unwrap() error }); ok {
		t.Fatal("canonicalization error must not expose Unwrap")
	}
}

func TestPreparePlanBytesRepeatedAndConcurrent(t *testing.T) {
	fixtures := [][]byte{
		[]byte(planJSON(nrFinding())),
		[]byte(fullGoldenPlanJSON()),
		[]byte(`{"schema_version":`),
	}
	const workers = 24
	var wg sync.WaitGroup
	errs := make(chan string, workers)
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func(index int) {
			defer wg.Done()
			input := append([]byte(nil), fixtures[index%len(fixtures)]...)
			state1, digest1, err1 := preparePlanBytes(input)
			state2, digest2, err2 := preparePlanBytes(append([]byte(nil), input...))
			if (err1 == nil) != (err2 == nil) {
				errs <- "success classification changed"
				return
			}
			if err1 != nil {
				if !reflect.DeepEqual(state1, authorizedPlanState{}) ||
					!reflect.DeepEqual(state2, authorizedPlanState{}) ||
					digest1 != ([32]byte{}) || digest2 != ([32]byte{}) {
					errs <- "error returned partial state"
				}
				return
			}
			if !reflect.DeepEqual(state1, state2) || digest1 != digest2 {
				errs <- "successful calls diverged"
			}
		}(i)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Error(err)
	}
}

func TestPreparePlanBytesMetaGateBehavior(t *testing.T) {
	valid := []byte(planJSON(nrFinding()))
	state, digest, err := preparePlanBytes(valid)
	if err != nil {
		t.Fatalf("valid plan rejected: %v", err)
	}
	if !reflect.DeepEqual(state, *simpleState()) {
		t.Fatal("real parser/builder state was not produced")
	}
	if got := hex.EncodeToString(digest[:]); got != compactPreparedDigestHex {
		t.Fatalf("real V1 digest was not produced: %s", got)
	}

	semanticChange := []byte(strings.Replace(
		string(valid),
		`"plan_id":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"`,
		`"plan_id":"caaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"`,
		1,
	))
	_, changedDigest, err := preparePlanBytes(semanticChange)
	if err != nil {
		t.Fatalf("semantic change should remain valid: %v", err)
	}
	if digest == changedDigest {
		t.Fatal("constant digest would pass the meta-gate")
	}

	assertPrepareFailure(t, []byte(`{"schema_version":`), loadErrorSyntax)
}

func FuzzPreparePlanBytes(f *testing.F) {
	f.Add([]byte{})
	f.Add([]byte(planJSON(nrFinding())))
	f.Add([]byte(fullGoldenPlanJSON()))
	f.Add([]byte(strings.Replace(planJSON(nrFinding()), `"schema_version":1`, `"schema_version":2`, 1)))
	f.Add([]byte(strings.Replace(planJSON(nrFinding()), `"schema_version":1`, `"schema_version":1,"schema_version":1`, 1)))
	f.Add([]byte{0xff, 0xfe})
	f.Add([]byte(`{"schema_version":`))
	f.Add([]byte(strings.Replace(planJSON(nrFinding()), `"database_schema_version":55`, `"database_schema_version":9223372036854775808`, 1)))

	f.Fuzz(func(t *testing.T, data []byte) {
		original := append([]byte(nil), data...)
		state, digest, err := preparePlanBytes(data)
		if err != nil {
			if !reflect.DeepEqual(state, authorizedPlanState{}) {
				t.Fatal("error returned non-zero state")
			}
			if digest != ([32]byte{}) {
				t.Fatal("error returned non-zero digest")
			}
			return
		}

		recomputed, recomputeErr := computeAuthorizedPlanDigestV1(&state)
		if recomputeErr != nil || recomputed != digest {
			t.Fatal("successful digest is not reproducible")
		}
		for i := range data {
			data[i] ^= 0xff
		}
		recomputedAfterMutation, recomputeErr := computeAuthorizedPlanDigestV1(&state)
		if recomputeErr != nil || recomputedAfterMutation != digest {
			t.Fatal("input mutation changed prepared state or digest")
		}
		stateAgain, digestAgain, errAgain := preparePlanBytes(original)
		if errAgain != nil || !reflect.DeepEqual(stateAgain, state) || digestAgain != digest {
			t.Fatal("repeated preparation is not deterministic")
		}
	})
}
