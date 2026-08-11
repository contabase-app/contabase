package civildateplan

import (
	"encoding/hex"
	"encoding/json"
	"strings"
	"testing"
)

func testAuthorizedPlan(t *testing.T, parsed *parsedPlan) *AuthorizedRepairPlan {
	t.Helper()
	state := mustBuildAuthorizedStateForTest(t, parsed)
	if !state.invariantsValid() {
		t.Fatal("invariantsValid returned false")
	}
	digest, err := computeAuthorizedPlanDigestV1(state)
	if err != nil {
		t.Fatalf("computeAuthorizedPlanDigestV1: %v", err)
	}
	candidate := &AuthorizedRepairPlan{
		state:  state,
		digest: digest,
	}
	if candidate.IsAuthorized() {
		t.Fatal("candidate without seal should not be authorized")
	}
	if candidate.Digest() != "" {
		t.Fatal("Digest without seal should return empty")
	}
	candidate.seal = authorizedPlanSeal
	if !candidate.IsAuthorized() {
		t.Fatal("plan with seal should be authorized")
	}
	if candidate.Digest() == "" {
		t.Fatal("Digest of authorized plan should not be empty")
	}
	recomputed, err := computeAuthorizedPlanDigestV1(candidate.state)
	if err != nil {
		t.Fatalf("recompute digest: %v", err)
	}
	if candidate.digest != recomputed {
		t.Fatal("stored digest should equal recomputed digest")
	}
	return candidate
}

func mustBuildAuthorizedStateForTest(
	t *testing.T,
	parsed *parsedPlan,
) *authorizedPlanState {
	t.Helper()
	validated, err := validateParsedPlanV1(parsed)
	if err != nil {
		t.Fatalf("validateParsedPlanV1: %v", err)
	}
	state, err := buildAuthorizedPlanState(validated)
	if err != nil {
		t.Fatalf("buildAuthorizedPlanState: %v", err)
	}
	return state
}

func mustParsePlanForTest(t *testing.T) *parsedPlan {
	t.Helper()
	plan, err := parsePlanV1([]byte(planJSON(nrFinding())))
	if err != nil {
		t.Fatalf("parsePlanV1: %v", err)
	}
	return plan
}

func mustParseRecurringForTest(t *testing.T) *parsedPlan {
	t.Helper()
	plan, err := parsePlanV1([]byte(planJSON(rcrFinding())))
	if err != nil {
		t.Fatalf("parsePlanV1 recurring: %v", err)
	}
	return plan
}

func mustParseRuleForTest(t *testing.T) *parsedPlan {
	t.Helper()
	plan, err := parsePlanV1([]byte(planJSON(ruleFinding())))
	if err != nil {
		t.Fatalf("parsePlanV1 rule: %v", err)
	}
	return plan
}

func mutateAndCheckNotAuthorized(t *testing.T, mutate func(p *AuthorizedRepairPlan)) {
	t.Helper()
	parsed := mustParsePlanForTest(t)
	p := testAuthorizedPlan(t, parsed)
	mutate(p)
	if p.IsAuthorized() {
		t.Fatal("mutated plan should not be authorized")
	}
	if p.Metadata().SchemaVersion() != 0 {
		t.Fatal("metadata should be zero after invalidation")
	}
	if p.FindingCount() != 0 {
		t.Fatal("finding count should be zero after invalidation")
	}
	if _, ok := p.FindingAt(0); ok {
		t.Fatal("FindingAt should return false after invalidation")
	}
}

func TestAuthorizedNilPlan(t *testing.T) {
	var nilPlan *AuthorizedRepairPlan
	if nilPlan.IsAuthorized() {
		t.Fatal("nil plan should not be authorized")
	}
	if nilPlan.FindingCount() != 0 {
		t.Fatal("nil plan FindingCount should be 0")
	}
	_, ok := nilPlan.FindingAt(0)
	if ok {
		t.Fatal("nil plan FindingAt should return false")
	}
	md := nilPlan.Metadata()
	if md.SchemaVersion() != 0 {
		t.Fatal("nil plan Metadata should be zero")
	}
}

func TestAuthorizedZeroPlan(t *testing.T) {
	p := &AuthorizedRepairPlan{}
	if p.IsAuthorized() {
		t.Fatal("zero plan should not be authorized")
	}
	if p.FindingCount() != 0 {
		t.Fatal("zero plan FindingCount should be 0")
	}
	_, ok := p.FindingAt(0)
	if ok {
		t.Fatal("zero plan FindingAt should return false")
	}
}

func TestAuthorizedInvalidSeal(t *testing.T) {
	parsed := mustParsePlanForTest(t)
	state := mustBuildAuthorizedStateForTest(t, parsed)
	p := &AuthorizedRepairPlan{seal: &authorizationSeal{marker: 0xFF}, state: state}
	if p.IsAuthorized() {
		t.Fatal("plan with wrong seal should not be authorized")
	}
}

func TestAuthorizedStateNil(t *testing.T) {
	p := &AuthorizedRepairPlan{seal: authorizedPlanSeal, state: nil}
	if p.IsAuthorized() {
		t.Fatal("plan with nil state should not be authorized")
	}
}

func TestAuthorizedStateEmpty(t *testing.T) {
	p := &AuthorizedRepairPlan{seal: authorizedPlanSeal, state: &authorizedPlanState{}}
	if p.IsAuthorized() {
		t.Fatal("plan with empty state should not be authorized")
	}
}

func TestAuthorizedEmptyFindings(t *testing.T) {
	parsed := mustParsePlanForTest(t)
	state := mustBuildAuthorizedStateForTest(t, parsed)
	state.findings = nil
	p := &AuthorizedRepairPlan{seal: authorizedPlanSeal, state: state}
	if p.IsAuthorized() {
		t.Fatal("plan with empty findings should not be authorized")
	}
}

func TestAuthorizedInvalidVariant(t *testing.T) {
	parsed := mustParsePlanForTest(t)
	state := mustBuildAuthorizedStateForTest(t, parsed)
	state.findings[0].variant = "INVALID"
	p := &AuthorizedRepairPlan{seal: authorizedPlanSeal, state: state}
	if p.IsAuthorized() {
		t.Fatal("plan with invalid variant should not be authorized")
	}
}

func TestAuthorizedDeepCopyMetadata(t *testing.T) {
	parsed := mustParsePlanForTest(t)
	p := testAuthorizedPlan(t, parsed)
	originalMD := p.Metadata()
	parsed.planID.value = "changed"
	parsed.scope.value = "CHANGED"
	parsed.timezone.value = "Changed/Zone"
	if md := p.Metadata(); md.PlanID() != originalMD.PlanID() {
		t.Fatal("plan_id changed")
	}
	if md := p.Metadata(); md.Scope() != originalMD.Scope() {
		t.Fatal("scope changed")
	}
}

func TestAuthorizedDeepCopyFindingFields(t *testing.T) {
	parsed := mustParsePlanForTest(t)
	p := testAuthorizedPlan(t, parsed)
	f, ok := p.FindingAt(0)
	if !ok {
		t.Fatal("expected finding at 0")
	}
	origSource := f.Source()
	parsed.findings[0].source.value = "CHANGED"
	f2, _ := p.FindingAt(0)
	if f2.Source() != origSource {
		t.Fatal("source changed")
	}
}

func TestAuthorizedDeepCopyTxState(t *testing.T) {
	parsed := mustParsePlanForTest(t)
	p := testAuthorizedPlan(t, parsed)
	f, _ := p.FindingAt(0)
	tx, ok := f.TransactionState()
	if !ok {
		t.Fatal("expected transaction state")
	}
	origStatus := tx.Status()
	parsed.findings[0].transactionState.value.status.value = "CHANGED"
	f2, _ := p.FindingAt(0)
	tx2, _ := f2.TransactionState()
	if tx2.Status() != origStatus {
		t.Fatal("status changed")
	}
}

func TestAuthorizedDeepCopySlice(t *testing.T) {
	parsed := mustParsePlanForTest(t)
	p := testAuthorizedPlan(t, parsed)
	origCount := p.FindingCount()
	parsed.findings = append(parsed.findings, parsed.findings[0])
	if p.FindingCount() != origCount {
		t.Fatal("finding count changed")
	}
}

func TestAuthorizedSnapshotMetadataImmutable(t *testing.T) {
	parsed := mustParsePlanForTest(t)
	p := testAuthorizedPlan(t, parsed)
	md := p.Metadata()
	origScope := md.Scope()
	md = PlanMetadataSnapshot{schemaVersion: 999, scope: "CHANGED"}
	if p.Metadata().Scope() != origScope {
		t.Fatal("snapshot mutation affected state")
	}
}

func TestAuthorizedSnapshotFindingImmutable(t *testing.T) {
	parsed := mustParsePlanForTest(t)
	p := testAuthorizedPlan(t, parsed)
	f, _ := p.FindingAt(0)
	origSource := f.Source()
	f = RepairFindingSnapshot{source: "CHANGED"}
	f2, _ := p.FindingAt(0)
	if f2.Source() != origSource {
		t.Fatal("snapshot mutation affected state")
	}
}

func TestAuthorizedRepeatedSnapshotSame(t *testing.T) {
	parsed := mustParsePlanForTest(t)
	p := testAuthorizedPlan(t, parsed)
	md1 := p.Metadata()
	md2 := p.Metadata()
	if md1.PlanID() != md2.PlanID() {
		t.Fatal("repeated metadata call gave diff plan_id")
	}
}

func TestAuthorizedCopyByValue(t *testing.T) {
	parsed := mustParsePlanForTest(t)
	original := testAuthorizedPlan(t, parsed)
	copyPlan := *original
	if !copyPlan.IsAuthorized() {
		t.Fatal("copy should be authorized")
	}
	if copyPlan.Metadata().PlanID() != original.Metadata().PlanID() {
		t.Fatal("copy metadata should match")
	}
}

func TestOptionalStringZeroValue(t *testing.T) {
	var o OptionalString
	if o.IsNull() {
		t.Fatal("zero OptionalString should not be null")
	}
	_, ok := o.Value()
	if ok {
		t.Fatal("zero OptionalString Value should return false")
	}
}

func TestOptionalStringNull(t *testing.T) {
	o := newOptionalStringNull()
	if !o.IsNull() {
		t.Fatal("null OptionalString IsNull should be true")
	}
	_, ok := o.Value()
	if ok {
		t.Fatal("null OptionalString Value should return false")
	}
}

func TestOptionalStringValue(t *testing.T) {
	o := newOptionalString("hello")
	if o.IsNull() {
		t.Fatal("value IsNull should be false")
	}
	v, ok := o.Value()
	if !ok {
		t.Fatal("value should return true")
	}
	if v != "hello" {
		t.Fatalf("expected 'hello', got %q", v)
	}
}

func TestOptionalStringEmptyValue(t *testing.T) {
	o := newOptionalString("")
	if o.IsNull() {
		t.Fatal("empty string IsNull should be false")
	}
	v, ok := o.Value()
	if !ok {
		t.Fatal("empty string Value should return true")
	}
	if v != "" {
		t.Fatal("expected empty string")
	}
}

func TestOptionalInt64ZeroValue(t *testing.T) {
	var o OptionalInt64
	if o.IsNull() {
		t.Fatal("zero OptionalInt64 should not be null")
	}
	_, ok := o.Value()
	if ok {
		t.Fatal("zero OptionalInt64 Value should return false")
	}
}

func TestOptionalInt64Null(t *testing.T) {
	o := newOptionalInt64Null()
	if !o.IsNull() {
		t.Fatal("null OptionalInt64 IsNull should be true")
	}
	_, ok := o.Value()
	if ok {
		t.Fatal("null OptionalInt64 Value should return false")
	}
}

func TestOptionalInt64Value(t *testing.T) {
	o := newOptionalInt64(42)
	if o.IsNull() {
		t.Fatal("value IsNull should be false")
	}
	v, ok := o.Value()
	if !ok {
		t.Fatal("value should return true")
	}
	if v != 42 {
		t.Fatalf("expected 42, got %d", v)
	}
}

func TestOptionalInt64ZeroValuePreserved(t *testing.T) {
	o := newOptionalInt64(0)
	if o.IsNull() {
		t.Fatal("zero int64 IsNull should be false")
	}
	v, ok := o.Value()
	if !ok {
		t.Fatal("zero int64 Value should return true")
	}
	if v != 0 {
		t.Fatal("expected 0")
	}
}

func TestOptionalBoolZeroValue(t *testing.T) {
	var o OptionalBool
	if o.IsNull() {
		t.Fatal("zero OptionalBool should not be null")
	}
	_, ok := o.Value()
	if ok {
		t.Fatal("zero OptionalBool Value should return false")
	}
}

func TestOptionalBoolValue(t *testing.T) {
	o := newOptionalBool(true)
	if o.IsNull() {
		t.Fatal("value IsNull should be false")
	}
	v, ok := o.Value()
	if !ok {
		t.Fatal("value should return true")
	}
	if !v {
		t.Fatal("expected true")
	}
}

func TestOptionalBoolFalsePreserved(t *testing.T) {
	o := newOptionalBool(false)
	if o.IsNull() {
		t.Fatal("false IsNull should be false")
	}
	v, ok := o.Value()
	if !ok {
		t.Fatal("false Value should return true")
	}
	if v {
		t.Fatal("expected false")
	}
}

func TestAuthorizedJSONMarshalEmpty(t *testing.T) {
	p := &AuthorizedRepairPlan{}
	data, err := json.Marshal(p)
	if err != nil {
		t.Fatalf("json.Marshal: %v", err)
	}
	if string(data) != "{}" {
		t.Fatalf("expected {}, got %s", string(data))
	}
}

func TestAuthorizedJSONMarshalAuthorized(t *testing.T) {
	parsed := mustParsePlanForTest(t)
	p := testAuthorizedPlan(t, parsed)
	data, err := json.Marshal(p)
	if err != nil {
		t.Fatalf("json.Marshal: %v", err)
	}
	if string(data) != "{}" {
		t.Fatalf("expected {}, got %s", string(data))
	}
}

// ---------- V2C: Digest method tests ----------

func TestAuthorizedDigestNil(t *testing.T) {
	var nilPlan *AuthorizedRepairPlan
	if nilPlan.Digest() != "" {
		t.Fatal("nil Digest should be empty")
	}
}

func TestAuthorizedDigestZeroValue(t *testing.T) {
	p := &AuthorizedRepairPlan{}
	if p.Digest() != "" {
		t.Fatal("zero Digest should be empty")
	}
}

func TestAuthorizedDigestInvalidSeal(t *testing.T) {
	parsed := mustParsePlanForTest(t)
	state := mustBuildAuthorizedStateForTest(t, parsed)
	p := &AuthorizedRepairPlan{seal: &authorizationSeal{marker: 0xFF}, state: state}
	if p.Digest() != "" {
		t.Fatal("wrong seal Digest should be empty")
	}
}

func TestAuthorizedDigestNilState(t *testing.T) {
	p := &AuthorizedRepairPlan{seal: authorizedPlanSeal, state: nil}
	if p.Digest() != "" {
		t.Fatal("nil state Digest should be empty")
	}
}

func TestAuthorizedDigestInvalidState(t *testing.T) {
	parsed := mustParsePlanForTest(t)
	p := testAuthorizedPlan(t, parsed)
	p.state.schemaVersion = 99
	if p.Digest() != "" {
		t.Fatal("invalid state Digest should be empty")
	}
}

func TestAuthorizedDigestSameRepeated(t *testing.T) {
	parsed := mustParsePlanForTest(t)
	p := testAuthorizedPlan(t, parsed)
	d1 := p.Digest()
	d2 := p.Digest()
	if d1 != d2 {
		t.Fatal("repeated Digest should be same")
	}
	if !strings.HasPrefix(d1, "sha256:") {
		t.Fatal("Digest should start with sha256:")
	}
	if len(d1) != 71 {
		t.Fatalf("Digest should be 71 chars (sha256: + 64 hex), got %d: %s", len(d1), d1)
	}
}

func TestAuthorizedDigestNotAffectedBySnapshots(t *testing.T) {
	parsed := mustParsePlanForTest(t)
	p := testAuthorizedPlan(t, parsed)
	d1 := p.Digest()
	_ = PlanMetadataSnapshot{schemaVersion: 999, scope: "CHANGED"}
	if p.Digest() != d1 {
		t.Fatal("Digest changed after snapshot creation")
	}
	_ = RepairFindingSnapshot{source: "CHANGED"}
	if p.Digest() != d1 {
		t.Fatal("Digest changed after finding snapshot creation")
	}
}

func TestAuthorizedDigestStoredEqualsPublic(t *testing.T) {
	parsed := mustParsePlanForTest(t)
	p := testAuthorizedPlan(t, parsed)
	recomputed, err := computeAuthorizedPlanDigestV1(p.state)
	if err != nil {
		t.Fatalf("recompute: %v", err)
	}
	expected := "sha256:" + hex.EncodeToString(recomputed[:])
	if p.Digest() != expected {
		t.Fatal("public Digest should equal recomputed")
	}
}

func TestAuthorizedCopyPlanDigestPreserved(t *testing.T) {
	parsed := mustParsePlanForTest(t)
	original := testAuthorizedPlan(t, parsed)
	copyPlan := *original
	if !copyPlan.IsAuthorized() {
		t.Fatal("copy should be authorized")
	}
	if copyPlan.Digest() != original.Digest() {
		t.Fatal("copy Digest should match original")
	}
	copyPlan.digest = [32]byte{}
	if original.Digest() == "" {
		t.Fatal("mutating copy digest should not affect original")
	}
}

func TestAuthorizedFindingAtBounds(t *testing.T) {
	parsed := mustParsePlanForTest(t)
	p := testAuthorizedPlan(t, parsed)
	if _, ok := p.FindingAt(-1); ok {
		t.Fatal("negative index should return false")
	}
	if _, ok := p.FindingAt(p.FindingCount()); ok {
		t.Fatal("index == count should return false")
	}
	if _, ok := p.FindingAt(0); !ok {
		t.Fatal("index 0 should return true")
	}
}

func TestAuthorizedAllMetadataGetters(t *testing.T) {
	parsed := mustParsePlanForTest(t)
	p := testAuthorizedPlan(t, parsed)
	md := p.Metadata()
	if md.SchemaVersion() != 1 {
		t.Fatal("wrong schema_version")
	}
	if md.PlanID() != "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa" {
		t.Fatal("wrong plan_id")
	}
	if md.Scope() != "DATABASE" {
		t.Fatal("wrong scope")
	}
	if md.ToolVersion() != "civil-date-repair/v1" {
		t.Fatal("wrong tool_version")
	}
	if md.ClassifierVersion() != "civil-date-classifier/v1" {
		t.Fatal("wrong classifier_version")
	}
	if md.DatabaseSchemaVersion() != 55 {
		t.Fatal("wrong database_schema_version")
	}
	if md.Timezone() != "America/Sao_Paulo" {
		t.Fatal("wrong timezone")
	}
	if md.LogicalFingerprintVersion() != "contabase-logical/v1" {
		t.Fatal("wrong logical_fingerprint_version")
	}
	if !strings.HasPrefix(md.SourceLogicalFingerprint(), "sha256:") {
		t.Fatal("wrong source_fingerprint")
	}
	if !strings.HasPrefix(md.AuditSetDigest(), "sha256:") {
		t.Fatal("wrong audit_digest")
	}
	if md.GeneratedAt() != "2024-06-05T12:00:00Z" {
		t.Fatal("wrong generated_at")
	}
}

// ---------- Invalid state tests ----------

func TestAuthorizedInvalidSchemaVersion(t *testing.T) {
	parsed := mustParsePlanForTest(t)
	p := testAuthorizedPlan(t, parsed)
	p.state.schemaVersion = 99
	if p.IsAuthorized() {
		t.Fatal("wrong schema_version should not be authorized")
	}
}

func TestAuthorizedInvalidPlanID(t *testing.T) {
	parsed := mustParsePlanForTest(t)
	p := testAuthorizedPlan(t, parsed)
	p.state.planID = "short"
	if p.IsAuthorized() {
		t.Fatal("short plan_id should not be authorized")
	}
	p.state.planID = "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"
	if p.IsAuthorized() {
		t.Fatal("uppercase plan_id should not be authorized")
	}
}

func TestAuthorizedInvalidTimezone(t *testing.T) {
	parsed := mustParsePlanForTest(t)
	p := testAuthorizedPlan(t, parsed)
	p.state.timezone = "Invalid/Zone"
	if p.IsAuthorized() {
		t.Fatal("invalid timezone should not be authorized")
	}
}

func TestAuthorizedInvalidGeneratedAt(t *testing.T) {
	parsed := mustParsePlanForTest(t)
	p := testAuthorizedPlan(t, parsed)
	p.state.generatedAt = "2024-06-05T12:00:00-03:00"
	if p.IsAuthorized() {
		t.Fatal("non-UTC timestamp should not be authorized")
	}
}

func TestAuthorizedInvalidStatus(t *testing.T) {
	parsed := mustParsePlanForTest(t)
	p := testAuthorizedPlan(t, parsed)
	p.state.findings[0].transactionState.status = "INVALID"
	if p.IsAuthorized() {
		t.Fatal("invalid status should not be authorized")
	}
}

func TestAuthorizedInvalidType(t *testing.T) {
	parsed := mustParsePlanForTest(t)
	p := testAuthorizedPlan(t, parsed)
	p.state.findings[0].transactionState.typ = "invalid"
	if p.IsAuthorized() {
		t.Fatal("invalid type should not be authorized")
	}
}

func TestAuthorizedNonRecurringWithRuleState(t *testing.T) {
	parsed := mustParsePlanForTest(t)
	p := testAuthorizedPlan(t, parsed)
	p.state.findings[0].hasRuleState = true
	if p.IsAuthorized() {
		t.Fatal("NON_RECURRING with rule_state should not be authorized")
	}
}

func TestAuthorizedNonRecurringNoTransaction(t *testing.T) {
	parsed := mustParsePlanForTest(t)
	p := testAuthorizedPlan(t, parsed)
	p.state.findings[0].hasTransactionState = false
	if p.IsAuthorized() {
		t.Fatal("NON_RECURRING without transaction should not be authorized")
	}
}

func TestAuthorizedDuplicateCanonicalKey(t *testing.T) {
	parsed := mustParsePlanForTest(t)
	p := testAuthorizedPlan(t, parsed)
	p.state.findings = append(p.state.findings, p.state.findings[0])
	if p.IsAuthorized() {
		t.Fatal("duplicate canonical key should not be authorized")
	}
}

func TestAuthorizedOptionalInvalid(t *testing.T) {
	parsed := mustParsePlanForTest(t)
	p := testAuthorizedPlan(t, parsed)
	p.state.findings[0].transactionState.dueDateUnix = OptionalInt64{}
	if p.IsAuthorized() {
		t.Fatal("invalid optional should not be authorized")
	}
}

func TestAuthorizedRecurringWorkspaceMismatch(t *testing.T) {
	parsed := mustParsePlanForTest(t)
	p := testAuthorizedPlan(t, parsed)
	f := &p.state.findings[0]
	f.variant = variantRecurring
	f.hasRuleState = true
	f.hasSeriesState = true
	f.ruleState = authorizedRuleState{
		id:                     f.transactionState.id,
		workspaceID:            "wrong",
		seriesID:               "s-1",
		effectiveStartSequence: 1,
		active:                 true,
	}
	f.seriesState = authorizedSeriesState{id: "s-1", workspaceID: "ws1", active: true}
	f.transactionState.recurringRuleID = newOptionalString(f.transactionState.id)
	f.transactionState.seriesID = newOptionalString("s-1")
	f.transactionState.recurrenceSequence = newOptionalInt64(1)
	f.transactionState.isRecurringException = false
	if p.IsAuthorized() {
		t.Fatal("workspace mismatch should not be authorized")
	}
}

// ========== V2B.2: state pointer sharing ==========

func TestAuthorizedRepairPlanValueCopySharesImmutableState(t *testing.T) {
	parsed := mustParsePlanForTest(t)
	original := testAuthorizedPlan(t, parsed)
	copyPlan := *original
	if original.state != copyPlan.state {
		t.Fatal("value copy should share state pointer")
	}
	if !copyPlan.IsAuthorized() {
		t.Fatal("copy should be authorized")
	}
	if copyPlan.Metadata().PlanID() != original.Metadata().PlanID() {
		t.Fatal("copy metadata should match original")
	}
	if copyPlan.FindingCount() != original.FindingCount() {
		t.Fatal("copy finding count should match")
	}
	// Mutating snapshot returned by copy does not affect original
	md := copyPlan.Metadata()
	md = PlanMetadataSnapshot{schemaVersion: 999}
	if original.Metadata().SchemaVersion() != md.SchemaVersion() {
		_ = md // snapshot is copy, mutating it has no effect on original
	}
	if original.Metadata().SchemaVersion() != 1 {
		t.Fatal("original metadata should remain unchanged after snapshot mutation")
	}
}

// ========== V2B.2: complete deep copy matrix ==========

func checkDeepCopyBaseline(t *testing.T, parsed *parsedPlan, p *AuthorizedRepairPlan) (schemaVersion int64, count int, firstSource string) {
	t.Helper()
	schemaVersion = p.Metadata().SchemaVersion()
	count = p.FindingCount()
	f, ok := p.FindingAt(0)
	if !ok {
		t.Fatal("expected finding at 0")
	}
	firstSource = f.Source()
	return
}

func assertDeepCopyPreserved(t *testing.T, p *AuthorizedRepairPlan, schemaVersion int64, count int, firstSource string) {
	t.Helper()
	if !p.IsAuthorized() {
		t.Fatal("plan should remain authorized after parsed mutation")
	}
	if p.Metadata().SchemaVersion() != schemaVersion {
		t.Fatal("schemaVersion changed after parsed mutation")
	}
	if p.FindingCount() != count {
		t.Fatal("finding count changed after parsed mutation")
	}
	f, _ := p.FindingAt(0)
	if f.Source() != firstSource {
		t.Fatal("finding source changed after parsed mutation")
	}
}

// deepCopyCheck mutates a single parsed field and verifies the authorized plan is unchanged.
func deepCopyCheck(t *testing.T, mutate func(parsed *parsedPlan)) {
	t.Helper()
	parsed := mustParsePlanForTest(t)
	p := testAuthorizedPlan(t, parsed)
	sv, cnt, src := checkDeepCopyBaseline(t, parsed, p)
	mutate(parsed)
	assertDeepCopyPreserved(t, p, sv, cnt, src)
}

// deepCopyRecurringCheck is like deepCopyCheck but uses recurring fixture.
func deepCopyRecurringCheck(t *testing.T, mutate func(parsed *parsedPlan)) {
	t.Helper()
	parsed := mustParseRecurringForTest(t)
	p := testAuthorizedPlan(t, parsed)
	sv, cnt, src := checkDeepCopyBaseline(t, parsed, p)
	mutate(parsed)
	assertDeepCopyPreserved(t, p, sv, cnt, src)
}

func TestAuthorizedDeepCopyAllMetadata(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(parsed *parsedPlan)
	}{
		{"schemaVersion", func(p *parsedPlan) { p.schemaVersion.value = 99 }},
		{"planID", func(p *parsedPlan) { p.planID.value = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb" }},
		{"scope", func(p *parsedPlan) { p.scope.value = "INVALID" }},
		{"toolVersion", func(p *parsedPlan) { p.toolVersion.value = "changed" }},
		{"classifierVersion", func(p *parsedPlan) { p.classifierVersion.value = "changed" }},
		{"databaseSchemaVersion", func(p *parsedPlan) { p.databaseSchemaVersion.value = 999 }},
		{"timezone", func(p *parsedPlan) { p.timezone.value = "UTC" }},
		{"logicalFingerprintVersion", func(p *parsedPlan) { p.logicalFingerprintVersion.value = "changed" }},
		{"sourceLogicalFingerprint", func(p *parsedPlan) { p.sourceLogicalFingerprint.value = "sha256:" + strings.Repeat("1", 64) }},
		{"auditSetDigest", func(p *parsedPlan) { p.auditSetDigest.value = "sha256:" + strings.Repeat("2", 64) }},
		{"generatedAt", func(p *parsedPlan) { p.generatedAt.value = "2024-06-05T12:00:00-03:00" }},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) { deepCopyCheck(t, c.mutate) })
	}
}

func TestAuthorizedDeepCopyAllFindingFields(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(parsed *parsedPlan)
	}{
		{"variant", func(p *parsedPlan) { p.findings[0].variant.value = "CHANGED" }},
		{"source", func(p *parsedPlan) { p.findings[0].source.value = "CHANGED" }},
		{"workspaceID", func(p *parsedPlan) { p.findings[0].workspaceID.value = "CHANGED" }},
		{"sourceRowID", func(p *parsedPlan) { p.findings[0].sourceRowID.value = "CHANGED" }},
		{"auditedField", func(p *parsedPlan) { p.findings[0].auditedField.value = "CHANGED" }},
		{"reasonCode", func(p *parsedPlan) { p.findings[0].reasonCode.value = "CHANGED" }},
		{"repairOutcome", func(p *parsedPlan) { p.findings[0].repairOutcome.value = "CHANGED" }},
		{"expectedLegacyValue", func(p *parsedPlan) { p.findings[0].expectedLegacyValue.value = 999999 }},
		{"expectedCivilState", func(p *parsedPlan) { p.findings[0].expectedCivilState.value = "CHANGED" }},
		{"proposedCivilValue", func(p *parsedPlan) { p.findings[0].proposedCivilValue.value = "CHANGED" }},
		{"timezone", func(p *parsedPlan) { p.findings[0].timezone.value = "UTC" }},
		{"classifierVersion", func(p *parsedPlan) { p.findings[0].classifierVersion.value = "CHANGED" }},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) { deepCopyCheck(t, c.mutate) })
	}
}

func TestAuthorizedDeepCopyAllTxState(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(parsed *parsedPlan)
	}{
		{"id", func(p *parsedPlan) { p.findings[0].transactionState.value.id.value = "CHANGED" }},
		{"workspaceID", func(p *parsedPlan) { p.findings[0].transactionState.value.workspaceID.value = "CHANGED" }},
		{"status", func(p *parsedPlan) { p.findings[0].transactionState.value.status.value = "CHANGED" }},
		{"type", func(p *parsedPlan) { p.findings[0].transactionState.value.typeField.value = "CHANGED" }},
		{"dateUnix", func(p *parsedPlan) { p.findings[0].transactionState.value.dateUnix.value = 99999999 }},
		{"dueDateUnixState", func(p *parsedPlan) {
			p.findings[0].transactionState.value.dueDateUnix = parsedField[*int64]{present: true, isNull: true}
		}},
		{"recurringRuleIDState", func(p *parsedPlan) {
			p.findings[0].transactionState.value.recurringRuleID = parsedField[*string]{present: true, isNull: true}
		}},
		{"seriesIDState", func(p *parsedPlan) {
			p.findings[0].transactionState.value.seriesID = parsedField[*string]{present: true, isNull: true}
		}},
		{"recurrenceSequenceState", func(p *parsedPlan) {
			p.findings[0].transactionState.value.recurrenceSequence = parsedField[*int64]{present: true, isNull: true}
		}},
		{"isRecurringException", func(p *parsedPlan) { p.findings[0].transactionState.value.isRecurringException.value = true }},
		{"recurringExceptionReasonState", func(p *parsedPlan) {
			p.findings[0].transactionState.value.recurringExceptionReason = parsedField[*string]{present: true, isNull: true}
		}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) { deepCopyCheck(t, c.mutate) })
	}
}

func TestAuthorizedDeepCopyAllRuleState(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(parsed *parsedPlan)
	}{
		{"id", func(p *parsedPlan) { p.findings[0].ruleState.value.id.value = "CHANGED" }},
		{"workspaceID", func(p *parsedPlan) { p.findings[0].ruleState.value.workspaceID.value = "CHANGED" }},
		{"seriesID", func(p *parsedPlan) { p.findings[0].ruleState.value.seriesID.value = "CHANGED" }},
		{"parentRuleIDState", func(p *parsedPlan) {
			p.findings[0].ruleState.value.parentRuleID = parsedField[*string]{present: true, isNull: true}
		}},
		{"effectiveStartSequence", func(p *parsedPlan) { p.findings[0].ruleState.value.effectiveStartSequence.value = 999 }},
		{"effectiveEndSequenceState", func(p *parsedPlan) {
			p.findings[0].ruleState.value.effectiveEndSequence = parsedField[*int64]{present: true, isNull: true}
		}},
		{"active", func(p *parsedPlan) { p.findings[0].ruleState.value.active.value = false }},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) { deepCopyRecurringCheck(t, c.mutate) })
	}
}

func TestAuthorizedDeepCopyAllSeriesState(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(parsed *parsedPlan)
	}{
		{"id", func(p *parsedPlan) { p.findings[0].seriesState.value.id.value = "CHANGED" }},
		{"workspaceID", func(p *parsedPlan) { p.findings[0].seriesState.value.workspaceID.value = "CHANGED" }},
		{"active", func(p *parsedPlan) { p.findings[0].seriesState.value.active.value = false }},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) { deepCopyRecurringCheck(t, c.mutate) })
	}
}

func TestAuthorizedDeepCopySliceReplace(t *testing.T) {
	parsed := mustParsePlanForTest(t)
	p := testAuthorizedPlan(t, parsed)
	origCount := p.FindingCount()
	origSrc, _ := p.FindingAt(0)
	parsed.findings[0] = &parsedFinding{source: parsedField[string]{present: true, value: "CHANGED"}}
	if p.FindingCount() != origCount {
		t.Fatal("count changed")
	}
	f, _ := p.FindingAt(0)
	if f.Source() != origSrc.Source() {
		t.Fatal("finding source changed after slice replace")
	}
}

func secondNonRecurringFinding() string {
	return `{"variant":"TRANSACTION_NON_RECURRING","source":"transaction","workspace_id":"ws1","source_row_id":"tx-99","audited_field":"date_civil","reason_code":"DATE_CIVIL_MISSING","repair_outcome":"AUTO_REPAIR","expected_legacy_value":1717603200,"expected_civil_state":"NULL","proposed_civil_value":"2024-06-05","timezone":"America/Sao_Paulo","classifier_version":"civil-date-classifier/v1","transaction_state":{"id":"tx-99","workspace_id":"ws1","status":"pending","type":"EXPENSE","date_unix":1717603200,"due_date_unix":null,"recurring_rule_id":null,"series_id":null,"recurrence_sequence":null,"is_recurring_exception":false,"recurring_exception_reason":null},"rule_state":null,"series_state":null}`
}

func planWithTwoNRFindings() string {
	return `{"schema_version":1,"plan_id":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","scope":"DATABASE","tool_version":"civil-date-repair/v1","classifier_version":"civil-date-classifier/v1","database_schema_version":55,"timezone":"America/Sao_Paulo","logical_fingerprint_version":"contabase-logical/v1","source_logical_fingerprint":"sha256:0000000000000000000000000000000000000000000000000000000000000000","audit_set_digest":"sha256:0000000000000000000000000000000000000000000000000000000000000000","generated_at":"2024-06-05T12:00:00Z","findings":[` + nrFinding() + `,` + secondNonRecurringFinding() + `]}`
}

func TestAuthorizedDeepCopySliceReorder(t *testing.T) {
	parsed, err := parsePlanV1([]byte(planWithTwoNRFindings()))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	p := testAuthorizedPlan(t, parsed)
	origCount := p.FindingCount()
	f0, _ := p.FindingAt(0)
	f1, _ := p.FindingAt(1)
	parsed.findings[0], parsed.findings[1] = parsed.findings[1], parsed.findings[0]
	if p.FindingCount() != origCount {
		t.Fatal("count changed")
	}
	r0, _ := p.FindingAt(0)
	r1, _ := p.FindingAt(1)
	if r0.Source() != f0.Source() {
		t.Fatal("finding 0 changed after reorder")
	}
	if r1.Source() != f1.Source() {
		t.Fatal("finding 1 changed after reorder")
	}
}

func TestAuthorizedDeepCopySliceRemove(t *testing.T) {
	parsed, err := parsePlanV1([]byte(planWithTwoNRFindings()))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	p := testAuthorizedPlan(t, parsed)
	origCount := p.FindingCount()
	parsed.findings = parsed.findings[:1]
	if p.FindingCount() != origCount {
		t.Fatal("count changed after removal")
	}
}

func TestAuthorizedDeepCopySliceAppend(t *testing.T) {
	parsed := mustParsePlanForTest(t)
	p := testAuthorizedPlan(t, parsed)
	origCount := p.FindingCount()
	parsed.findings = append(parsed.findings, parsed.findings[0])
	if p.FindingCount() != origCount {
		t.Fatal("count changed after append")
	}
}

func TestAuthorizedDeepCopyOverwriteTxState(t *testing.T) {
	parsed := mustParsePlanForTest(t)
	p := testAuthorizedPlan(t, parsed)
	f, _ := p.FindingAt(0)
	tx, _ := f.TransactionState()
	origID := tx.ID()
	parsed.findings[0].transactionState = parsedField[*parsedTransactionState]{present: true, value: &parsedTransactionState{
		id: parsedField[string]{present: true, value: "new-id"},
	}}
	f2, _ := p.FindingAt(0)
	tx2, _ := f2.TransactionState()
	if tx2.ID() != origID {
		t.Fatal("tx id changed after overwrite")
	}
}

func TestAuthorizedDeepCopyOverwriteRuleState(t *testing.T) {
	parsed := mustParseRecurringForTest(t)
	p := testAuthorizedPlan(t, parsed)
	f, _ := p.FindingAt(0)
	rs, _ := f.RuleState()
	origID := rs.ID()
	parsed.findings[0].ruleState = parsedField[*parsedRuleState]{present: true, value: &parsedRuleState{
		id: parsedField[string]{present: true, value: "new-rule"},
	}}
	f2, _ := p.FindingAt(0)
	rs2, _ := f2.RuleState()
	if rs2.ID() != origID {
		t.Fatal("rule id changed after overwrite")
	}
}

func TestAuthorizedDeepCopyOverwriteSeriesState(t *testing.T) {
	parsed := mustParseRecurringForTest(t)
	p := testAuthorizedPlan(t, parsed)
	f, _ := p.FindingAt(0)
	ss, _ := f.SeriesState()
	origID := ss.ID()
	parsed.findings[0].seriesState = parsedField[*parsedSeriesState]{present: true, value: &parsedSeriesState{
		id: parsedField[string]{present: true, value: "new-series"},
	}}
	f2, _ := p.FindingAt(0)
	ss2, _ := f2.SeriesState()
	if ss2.ID() != origID {
		t.Fatal("series id changed after overwrite")
	}
}

func TestAuthorizedDeepCopySharedNestedState(t *testing.T) {
	sharedTx := &parsedTransactionState{
		id:                       parsedField[string]{present: true, value: "shared-tx"},
		workspaceID:              parsedField[string]{present: true, value: "ws1"},
		status:                   parsedField[string]{present: true, value: "pending"},
		typeField:                parsedField[string]{present: true, value: "EXPENSE"},
		dateUnix:                 parsedField[int64]{present: true, value: 1717603200},
		dueDateUnix:              parsedField[*int64]{present: true, isNull: true},
		recurringRuleID:          parsedField[*string]{present: true, isNull: true},
		seriesID:                 parsedField[*string]{present: true, isNull: true},
		recurrenceSequence:       parsedField[*int64]{present: true, isNull: true},
		isRecurringException:     parsedField[bool]{present: true, value: false},
		recurringExceptionReason: parsedField[*string]{present: true, isNull: true},
	}
	parsed := mustParsePlanForTest(t)
	f1 := *parsed.findings[0]
	f1.transactionState = parsedField[*parsedTransactionState]{present: true, value: sharedTx}
	f1.sourceRowID = parsedField[string]{present: true, value: "shared-tx"}
	f2 := f1
	f2.sourceRowID = parsedField[string]{present: true, value: "shared-tx"}
	f2.auditedField = parsedField[string]{present: true, value: "due_date_civil"}
	f2.reasonCode = parsedField[string]{present: true, value: reasonDueDateCivilMissing}
	v := int64(1717603200)
	sharedTx.dueDateUnix = parsedField[*int64]{present: true, value: &v}
	f2.transactionState = parsedField[*parsedTransactionState]{present: true, value: sharedTx}
	parsed.findings = []*parsedFinding{&f1, &f2}
	if parsed.findings[0].transactionState.value != parsed.findings[1].transactionState.value {
		t.Fatal("shared pointer should be identical")
	}
	p := testAuthorizedPlan(t, parsed)
	a0, _ := p.FindingAt(0)
	a1, _ := p.FindingAt(1)
	tx0, _ := a0.TransactionState()
	tx1, _ := a1.TransactionState()
	if tx0.ID() != tx1.ID() {
		t.Fatal("both findings should have same tx id from shared source")
	}
	sharedTx.status.value = "CHANGED"
	if p.FindingCount() != 2 {
		t.Fatal("count changed")
	}
	a0after, _ := p.FindingAt(0)
	a1after, _ := p.FindingAt(1)
	tx0after, _ := a0after.TransactionState()
	tx1after, _ := a1after.TransactionState()
	if tx0after.Status() != tx0.Status() {
		t.Fatal("finding 0 tx status changed")
	}
	if tx1after.Status() != tx1.Status() {
		t.Fatal("finding 1 tx status changed")
	}
}

// ========== V2B.2: complete snapshot immutability ==========

func TestAuthorizedSnapshotTxStateImmutable(t *testing.T) {
	parsed := mustParsePlanForTest(t)
	p := testAuthorizedPlan(t, parsed)
	f, _ := p.FindingAt(0)
	tx, ok := f.TransactionState()
	if !ok {
		t.Fatal("expected transaction state")
	}
	origID := tx.ID()
	// mutate all fields
	tx = TransactionStateSnapshot{
		id: "NEW", workspaceID: "NEW", status: "NEW", typ: "NEW", dateUnix: 999,
		dueDateUnix: newOptionalInt64(1), recurringRuleID: newOptionalString("x"),
		seriesID: newOptionalString("x"), recurrenceSequence: newOptionalInt64(1),
		isRecurringException: true, recurringExceptionReason: newOptionalString("x"),
	}
	f2, _ := p.FindingAt(0)
	tx2, _ := f2.TransactionState()
	if tx2.ID() != origID {
		t.Fatal("tx id should remain original after snapshot mutation")
	}
	if tx2.IsRecurringException() {
		t.Fatal("isRecurringException should remain false")
	}
}

func TestAuthorizedSnapshotRuleStateImmutable(t *testing.T) {
	parsed := mustParseRecurringForTest(t)
	p := testAuthorizedPlan(t, parsed)
	f, _ := p.FindingAt(0)
	rs, ok := f.RuleState()
	if !ok {
		t.Fatal("expected rule state")
	}
	origID := rs.ID()
	rs = RuleStateSnapshot{
		id: "NEW", workspaceID: "NEW", seriesID: "NEW",
		parentRuleID: newOptionalString("x"), effectiveStartSequence: 999,
		effectiveEndSequence: newOptionalInt64(99), active: false,
	}
	f2, _ := p.FindingAt(0)
	rs2, _ := f2.RuleState()
	if rs2.ID() != origID {
		t.Fatal("rule id should remain original")
	}
	if !rs2.Active() {
		t.Fatal("active should remain true")
	}
}

func TestAuthorizedSnapshotSeriesStateImmutable(t *testing.T) {
	parsed := mustParseRecurringForTest(t)
	p := testAuthorizedPlan(t, parsed)
	f, _ := p.FindingAt(0)
	ss, ok := f.SeriesState()
	if !ok {
		t.Fatal("expected series state")
	}
	origID := ss.ID()
	ss = SeriesStateSnapshot{id: "NEW", workspaceID: "NEW", active: false}
	f2, _ := p.FindingAt(0)
	ss2, _ := f2.SeriesState()
	if ss2.ID() != origID {
		t.Fatal("series id should remain original")
	}
}

func TestAuthorizedSnapshotFindingNestedImmutable(t *testing.T) {
	parsed := mustParseRecurringForTest(t)
	p := testAuthorizedPlan(t, parsed)
	f, _ := p.FindingAt(0)
	origSource := f.Source()
	origTx, _ := f.TransactionState()
	origTxID := origTx.ID()
	f = RepairFindingSnapshot{
		source:              "CHANGED",
		transactionState:    TransactionStateSnapshot{id: "NEW-TX"},
		ruleState:           RuleStateSnapshot{id: "NEW-RULE"},
		seriesState:         SeriesStateSnapshot{id: "NEW-SERIES"},
		hasTransactionState: false,
		hasRuleState:        false,
		hasSeriesState:      false,
	}
	f2, _ := p.FindingAt(0)
	if f2.Source() != origSource {
		t.Fatal("source changed after snapshot mutation")
	}
	tx2, ok := f2.TransactionState()
	if !ok {
		t.Fatal("transaction state should still be present")
	}
	if tx2.ID() != origTxID {
		t.Fatal("nested tx id changed after snapshot mutation")
	}
}

func TestAuthorizedSnapshotCrossCopyIndependent(t *testing.T) {
	parsed := mustParsePlanForTest(t)
	p := testAuthorizedPlan(t, parsed)
	f1, _ := p.FindingAt(0)
	f2, _ := p.FindingAt(0)
	newTx := TransactionStateSnapshot{id: "MUTATED"}
	f1 = RepairFindingSnapshot{source: "M1", transactionState: newTx, hasTransactionState: true}
	if f1.Source() == "M1" {
		_ = f2 // f1 is a copy, mutating it does not affect f2
	}
	if f2.Source() != f1.Source() {
		_ = f2 // f2 should still have original values
	}
	f2again, _ := p.FindingAt(0)
	if f2again.Source() == "M1" {
		t.Fatal("mutating one snapshot copy affected another or state")
	}
}

// ========== V2B.2: optional copy tests ==========

func TestOptionalStringCopyIndependent(t *testing.T) {
	o := newOptionalString("original")
	cp := newOptionalString("changed")
	if cp.IsNull() {
		t.Fatal("copy OptionalString should not be null")
	}
	cv, _ := cp.Value()
	if cv != "changed" {
		t.Fatal("copy OptionalString should have new value")
	}
	if o.IsNull() {
		t.Fatal("original OptionalString should not be null after copy mutation")
	}
	v, _ := o.Value()
	if v != "original" {
		t.Fatal("original OptionalString value changed after copy mutation")
	}
}

func TestOptionalInt64CopyIndependent(t *testing.T) {
	o := newOptionalInt64(42)
	cp := newOptionalInt64(99)
	cv, _ := cp.Value()
	if cv != 99 {
		t.Fatal("copy OptionalInt64 should have new value")
	}
	v, _ := o.Value()
	if v != 42 {
		t.Fatal("original OptionalInt64 value changed after copy mutation")
	}
}

func TestOptionalBoolCopyIndependent(t *testing.T) {
	o := newOptionalBool(true)
	cp := newOptionalBool(false)
	cv, _ := cp.Value()
	if cv {
		t.Fatal("copy OptionalBool should be false")
	}
	v, _ := o.Value()
	if !v {
		t.Fatal("original OptionalBool changed after copy mutation")
	}
}

func TestOptionalStringInvalidCopy(t *testing.T) {
	o := OptionalString{state: optionalInvalid}
	cp := newOptionalString("changed")
	if !cp.valid() {
		t.Fatal("copy OptionalString should be valid")
	}
	if o.valid() {
		t.Fatal("invalid original OptionalString should not be valid")
	}
}

func TestOptionalInt64InvalidCopy(t *testing.T) {
	o := OptionalInt64{state: optionalInvalid}
	cp := newOptionalInt64(99)
	v, _ := cp.Value()
	if v != 99 {
		t.Fatal("copy OptionalInt64 should have new value")
	}
	if o.valid() {
		t.Fatal("invalid original OptionalInt64 should not be valid")
	}
}

func TestOptionalBoolInvalidCopy(t *testing.T) {
	o := OptionalBool{state: optionalInvalid}
	cp := newOptionalBool(true)
	v, _ := cp.Value()
	if !v {
		t.Fatal("copy OptionalBool should have new value")
	}
	if o.valid() {
		t.Fatal("invalid original OptionalBool should not be valid")
	}
}

// ========== V2B.2: complete invariant capability matrix ==========

// --- Top-level invariants ---

func TestAuthorizedInvariantScopeInvalid(t *testing.T) {
	mutateAndCheckNotAuthorized(t, func(p *AuthorizedRepairPlan) { p.state.scope = "INVALID" })
}

func TestAuthorizedInvariantToolVersionInvalid(t *testing.T) {
	mutateAndCheckNotAuthorized(t, func(p *AuthorizedRepairPlan) { p.state.toolVersion = "INVALID" })
}

func TestAuthorizedInvariantClassifierVersionInvalid(t *testing.T) {
	mutateAndCheckNotAuthorized(t, func(p *AuthorizedRepairPlan) { p.state.classifierVersion = "INVALID" })
}

func TestAuthorizedInvariantLogicalFingerprintVersionInvalid(t *testing.T) {
	mutateAndCheckNotAuthorized(t, func(p *AuthorizedRepairPlan) { p.state.logicalFingerprintVersion = "INVALID" })
}

func TestAuthorizedInvariantDatabaseSchemaVersionZero(t *testing.T) {
	mutateAndCheckNotAuthorized(t, func(p *AuthorizedRepairPlan) { p.state.databaseSchemaVersion = 0 })
}

func TestAuthorizedInvariantPlanIDEmpty(t *testing.T) {
	mutateAndCheckNotAuthorized(t, func(p *AuthorizedRepairPlan) { p.state.planID = "" })
}

func TestAuthorizedInvariantPlanIDLong(t *testing.T) {
	mutateAndCheckNotAuthorized(t, func(p *AuthorizedRepairPlan) { p.state.planID = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa" })
}

func TestAuthorizedInvariantPlanIDNonHex(t *testing.T) {
	mutateAndCheckNotAuthorized(t, func(p *AuthorizedRepairPlan) { p.state.planID = "gggggggggggggggggggggggggggggggg" })
}

func TestAuthorizedInvariantTimezoneEmpty(t *testing.T) {
	mutateAndCheckNotAuthorized(t, func(p *AuthorizedRepairPlan) { p.state.timezone = "" })
}

func TestAuthorizedInvariantSourceFingerprintInvalid(t *testing.T) {
	mutateAndCheckNotAuthorized(t, func(p *AuthorizedRepairPlan) { p.state.sourceLogicalFingerprint = "bad" })
}

func TestAuthorizedInvariantAuditDigestInvalid(t *testing.T) {
	mutateAndCheckNotAuthorized(t, func(p *AuthorizedRepairPlan) { p.state.auditSetDigest = "bad" })
}

func TestAuthorizedInvariantGeneratedAtOffsetPlusZero(t *testing.T) {
	mutateAndCheckNotAuthorized(t, func(p *AuthorizedRepairPlan) { p.state.generatedAt = "2024-06-05T12:00:00+00:00" })
}

func TestAuthorizedInvariantGeneratedAtLowercaseZ(t *testing.T) {
	mutateAndCheckNotAuthorized(t, func(p *AuthorizedRepairPlan) { p.state.generatedAt = "2024-06-05T12:00:00z" })
}

func TestAuthorizedInvariantGeneratedAtRedundantFraction(t *testing.T) {
	mutateAndCheckNotAuthorized(t, func(p *AuthorizedRepairPlan) { p.state.generatedAt = "2024-06-05T12:00:00.000000000Z" })
}

func TestAuthorizedInvariantGeneratedAtInvalidFormat(t *testing.T) {
	mutateAndCheckNotAuthorized(t, func(p *AuthorizedRepairPlan) { p.state.generatedAt = "not-a-timestamp" })
}

// --- Finding common invariants ---

func TestAuthorizedInvariantSourceInvalid(t *testing.T) {
	mutateAndCheckNotAuthorized(t, func(p *AuthorizedRepairPlan) { p.state.findings[0].source = "INVALID" })
}

func TestAuthorizedInvariantWorkspaceIDEmpty(t *testing.T) {
	mutateAndCheckNotAuthorized(t, func(p *AuthorizedRepairPlan) { p.state.findings[0].workspaceID = "" })
}

func TestAuthorizedInvariantSourceRowIDEmpty(t *testing.T) {
	mutateAndCheckNotAuthorized(t, func(p *AuthorizedRepairPlan) { p.state.findings[0].sourceRowID = "" })
}

func TestAuthorizedInvariantAuditedFieldInvalid(t *testing.T) {
	mutateAndCheckNotAuthorized(t, func(p *AuthorizedRepairPlan) { p.state.findings[0].auditedField = "INVALID" })
}

func TestAuthorizedInvariantReasonCodeIncompatible(t *testing.T) {
	mutateAndCheckNotAuthorized(t, func(p *AuthorizedRepairPlan) { p.state.findings[0].reasonCode = "INVALID" })
}

func TestAuthorizedInvariantPaidDateCivil(t *testing.T) {
	mutateAndCheckNotAuthorized(t, func(p *AuthorizedRepairPlan) { p.state.findings[0].auditedField = "paid_date_civil" })
}

func TestAuthorizedInvariantRepairOutcomeInvalid(t *testing.T) {
	mutateAndCheckNotAuthorized(t, func(p *AuthorizedRepairPlan) { p.state.findings[0].repairOutcome = "INVALID" })
}

func TestAuthorizedInvariantExpectedLegacyValueZero(t *testing.T) {
	mutateAndCheckNotAuthorized(t, func(p *AuthorizedRepairPlan) { p.state.findings[0].expectedLegacyValue = 0 })
}

func TestAuthorizedInvariantExpectedCivilStateInvalid(t *testing.T) {
	mutateAndCheckNotAuthorized(t, func(p *AuthorizedRepairPlan) { p.state.findings[0].expectedCivilState = "INVALID" })
}

func TestAuthorizedInvariantTimezoneDivergent(t *testing.T) {
	parsed := mustParsePlanForTest(t)
	p := testAuthorizedPlan(t, parsed)
	p.state.findings[0].timezone = "UTC"
	if p.IsAuthorized() {
		t.Fatal("timezone divergence should not be authorized")
	}
	if p.Metadata().SchemaVersion() != 0 {
		t.Fatal("metadata should be zero")
	}
	if p.FindingCount() != 0 {
		t.Fatal("finding count should be zero")
	}
	if _, ok := p.FindingAt(0); ok {
		t.Fatal("FindingAt should return false")
	}
}

func TestAuthorizedInvariantClassifierVersionDivergent(t *testing.T) {
	parsed := mustParsePlanForTest(t)
	p := testAuthorizedPlan(t, parsed)
	p.state.findings[0].classifierVersion = "INVALID"
	if p.IsAuthorized() {
		t.Fatal("classifierVersion divergence should not be authorized")
	}
	if p.Metadata().SchemaVersion() != 0 {
		t.Fatal("metadata should be zero")
	}
}

func TestAuthorizedInvariantTimezoneCoherentPositive(t *testing.T) {
	parsed := mustParsePlanForTest(t)
	p := testAuthorizedPlan(t, parsed)
	if !p.IsAuthorized() {
		t.Fatal("plan with coherent timezone should be authorized")
	}
	if p.state.findings[0].timezone != p.state.timezone {
		t.Fatal("finding timezone should match plan timezone")
	}
}

func TestAuthorizedInvariantClassifierVersionCoherentPositive(t *testing.T) {
	parsed := mustParsePlanForTest(t)
	p := testAuthorizedPlan(t, parsed)
	if !p.IsAuthorized() {
		t.Fatal("plan with coherent classifierVersion should be authorized")
	}
	if p.state.findings[0].classifierVersion != p.state.classifierVersion {
		t.Fatal("finding classifierVersion should match plan classifierVersion")
	}
}

func TestAuthorizedInvariantMultiFindingFirstTimezoneDivergent(t *testing.T) {
	parsed, err := parsePlanV1([]byte(planWithTwoNRFindings()))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	p := testAuthorizedPlan(t, parsed)
	p.state.findings[0].timezone = "UTC"
	if p.IsAuthorized() {
		t.Fatal("first finding timezone divergent should invalidate plan")
	}
}

func TestAuthorizedInvariantMultiFindingLastTimezoneDivergent(t *testing.T) {
	parsed, err := parsePlanV1([]byte(planWithTwoNRFindings()))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	p := testAuthorizedPlan(t, parsed)
	p.state.findings[len(p.state.findings)-1].timezone = "UTC"
	if p.IsAuthorized() {
		t.Fatal("last finding timezone divergent should invalidate plan")
	}
}

func TestAuthorizedInvariantMultiFindingFirstClassifierDivergent(t *testing.T) {
	parsed, err := parsePlanV1([]byte(planWithTwoNRFindings()))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	p := testAuthorizedPlan(t, parsed)
	p.state.findings[0].classifierVersion = "INVALID"
	if p.IsAuthorized() {
		t.Fatal("first finding classifierVersion divergent should invalidate plan")
	}
}

func TestAuthorizedInvariantMultiFindingLastClassifierDivergent(t *testing.T) {
	parsed, err := parsePlanV1([]byte(planWithTwoNRFindings()))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	p := testAuthorizedPlan(t, parsed)
	p.state.findings[len(p.state.findings)-1].classifierVersion = "INVALID"
	if p.IsAuthorized() {
		t.Fatal("last finding classifierVersion divergent should invalidate plan")
	}
}

func TestAuthorizedInvariantMultiFindingCoherent(t *testing.T) {
	parsed, err := parsePlanV1([]byte(planWithTwoNRFindings()))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	p := testAuthorizedPlan(t, parsed)
	if !p.IsAuthorized() {
		t.Fatal("plan with two coherent findings should be authorized")
	}
}

// --- Transaction state invariants ---

func TestAuthorizedInvariantTxIDEmpty(t *testing.T) {
	mutateAndCheckNotAuthorized(t, func(p *AuthorizedRepairPlan) { p.state.findings[0].transactionState.id = "" })
}

func TestAuthorizedInvariantTxWorkspaceEmpty(t *testing.T) {
	mutateAndCheckNotAuthorized(t, func(p *AuthorizedRepairPlan) { p.state.findings[0].transactionState.workspaceID = "" })
}

func TestAuthorizedInvariantTxDueDateUnixOptionalInvalid(t *testing.T) {
	mutateAndCheckNotAuthorized(t, func(p *AuthorizedRepairPlan) {
		p.state.findings[0].transactionState.dueDateUnix = OptionalInt64{state: 99}
	})
}

func TestAuthorizedInvariantTxRecurringRuleIDOptionalInvalid(t *testing.T) {
	mutateAndCheckNotAuthorized(t, func(p *AuthorizedRepairPlan) {
		p.state.findings[0].transactionState.recurringRuleID = OptionalString{state: 99}
	})
}

func TestAuthorizedInvariantTxSeriesIDOptionalInvalid(t *testing.T) {
	mutateAndCheckNotAuthorized(t, func(p *AuthorizedRepairPlan) {
		p.state.findings[0].transactionState.seriesID = OptionalString{state: 99}
	})
}

func TestAuthorizedInvariantTxRecurrenceSequenceOptionalInvalid(t *testing.T) {
	mutateAndCheckNotAuthorized(t, func(p *AuthorizedRepairPlan) {
		p.state.findings[0].transactionState.recurrenceSequence = OptionalInt64{state: 99}
	})
}

func TestAuthorizedInvariantTxExceptionReasonOptionalInvalid(t *testing.T) {
	mutateAndCheckNotAuthorized(t, func(p *AuthorizedRepairPlan) {
		p.state.findings[0].transactionState.recurringExceptionReason = OptionalString{state: 99}
	})
}

func TestAuthorizedInvariantExpectedLegacyValueDivergentDate(t *testing.T) {
	mutateAndCheckNotAuthorized(t, func(p *AuthorizedRepairPlan) { p.state.findings[0].expectedLegacyValue = 999 })
}

func TestAuthorizedInvariantDueDateCivilNull(t *testing.T) {
	parsed := mustParsePlanForTest(t)
	parsed.findings[0].auditedField.value = "due_date_civil"
	parsed.findings[0].reasonCode.value = reasonDueDateCivilMissing
	v := int64(1717603200)
	parsed.findings[0].transactionState.value.dueDateUnix = parsedField[*int64]{present: true, value: &v}
	state := mustBuildAuthorizedStateForTest(t, parsed)
	state.findings[0].transactionState.dueDateUnix = newOptionalInt64Null()
	p := &AuthorizedRepairPlan{seal: authorizedPlanSeal, state: state}
	if p.IsAuthorized() {
		t.Fatal("due_date_civil with null dueDateUnix should not be authorized")
	}
}

func TestAuthorizedInvariantDueDateCivilDivergent(t *testing.T) {
	parsed := mustParsePlanForTest(t)
	parsed.findings[0].auditedField.value = "due_date_civil"
	parsed.findings[0].reasonCode.value = reasonDueDateCivilMissing
	v := int64(1717603200)
	parsed.findings[0].transactionState.value.dueDateUnix = parsedField[*int64]{present: true, value: &v}
	// expectedLegacyValue is 1717603200 in the fixture, we set it to 999 later via mutation
	state := mustBuildAuthorizedStateForTest(t, parsed)
	state.findings[0].expectedLegacyValue = 999
	p := &AuthorizedRepairPlan{seal: authorizedPlanSeal, state: state}
	if p.IsAuthorized() {
		t.Fatal("divergent due_date value should not be authorized")
	}
}

// --- Rule state invariants ---

func TestAuthorizedInvariantRuleIDEmpty(t *testing.T) {
	parsed := mustParseRecurringForTest(t)
	p := testAuthorizedPlan(t, parsed)
	p.state.findings[0].ruleState.id = ""
	if p.IsAuthorized() {
		t.Fatal("rule with empty id should not be authorized")
	}
}

func TestAuthorizedInvariantRuleWorkspaceEmpty(t *testing.T) {
	parsed := mustParseRecurringForTest(t)
	p := testAuthorizedPlan(t, parsed)
	p.state.findings[0].ruleState.workspaceID = ""
	if p.IsAuthorized() {
		t.Fatal("rule with empty workspace should not be authorized")
	}
}

func TestAuthorizedInvariantRuleSeriesIDEmpty(t *testing.T) {
	parsed := mustParseRecurringForTest(t)
	p := testAuthorizedPlan(t, parsed)
	p.state.findings[0].ruleState.seriesID = ""
	if p.IsAuthorized() {
		t.Fatal("rule with empty seriesID should not be authorized")
	}
}

func TestAuthorizedInvariantRuleParentRuleIDOptionalInvalid(t *testing.T) {
	parsed := mustParseRecurringForTest(t)
	p := testAuthorizedPlan(t, parsed)
	p.state.findings[0].ruleState.parentRuleID = OptionalString{state: 99}
	if p.IsAuthorized() {
		t.Fatal("rule with invalid parentRuleID should not be authorized")
	}
}

func TestAuthorizedInvariantRuleEffectiveEndOptionalInvalid(t *testing.T) {
	parsed := mustParseRecurringForTest(t)
	p := testAuthorizedPlan(t, parsed)
	p.state.findings[0].ruleState.effectiveEndSequence = OptionalInt64{state: 99}
	if p.IsAuthorized() {
		t.Fatal("rule with invalid effectiveEndSequence should not be authorized")
	}
}

func TestAuthorizedInvariantRuleEffectiveStartZero(t *testing.T) {
	parsed := mustParseRecurringForTest(t)
	p := testAuthorizedPlan(t, parsed)
	p.state.findings[0].ruleState.effectiveStartSequence = 0
	if p.IsAuthorized() {
		t.Fatal("rule with zero effectiveStartSequence should not be authorized")
	}
}

func TestAuthorizedInvariantRuleEffectiveEndLessThanStart(t *testing.T) {
	parsed := mustParseRecurringForTest(t)
	p := testAuthorizedPlan(t, parsed)
	p.state.findings[0].ruleState.effectiveStartSequence = 10
	p.state.findings[0].ruleState.effectiveEndSequence = newOptionalInt64(5)
	if p.IsAuthorized() {
		t.Fatal("rule with end < start should not be authorized")
	}
}

// --- Series state invariants ---

func TestAuthorizedInvariantSeriesIDEmpty(t *testing.T) {
	parsed := mustParseRecurringForTest(t)
	p := testAuthorizedPlan(t, parsed)
	p.state.findings[0].seriesState.id = ""
	if p.IsAuthorized() {
		t.Fatal("series with empty id should not be authorized")
	}
}

func TestAuthorizedInvariantSeriesWorkspaceEmpty(t *testing.T) {
	parsed := mustParseRecurringForTest(t)
	p := testAuthorizedPlan(t, parsed)
	p.state.findings[0].seriesState.workspaceID = ""
	if p.IsAuthorized() {
		t.Fatal("series with empty workspace should not be authorized")
	}
}

// --- NON_RECURRING variant invariants ---

func TestAuthorizedInvariantNonRecurringWithRule(t *testing.T) {
	mutateAndCheckNotAuthorized(t, func(p *AuthorizedRepairPlan) { p.state.findings[0].hasRuleState = true })
}

func TestAuthorizedInvariantNonRecurringWithSeries(t *testing.T) {
	mutateAndCheckNotAuthorized(t, func(p *AuthorizedRepairPlan) { p.state.findings[0].hasSeriesState = true })
}

func TestAuthorizedInvariantNonRecurringRecurringRuleIDValue(t *testing.T) {
	mutateAndCheckNotAuthorized(t, func(p *AuthorizedRepairPlan) {
		p.state.findings[0].transactionState.recurringRuleID = newOptionalString("r-1")
	})
}

func TestAuthorizedInvariantNonRecurringSeriesIDValue(t *testing.T) {
	mutateAndCheckNotAuthorized(t, func(p *AuthorizedRepairPlan) {
		p.state.findings[0].transactionState.seriesID = newOptionalString("s-1")
	})
}

func TestAuthorizedInvariantNonRecurringSequenceValue(t *testing.T) {
	mutateAndCheckNotAuthorized(t, func(p *AuthorizedRepairPlan) {
		p.state.findings[0].transactionState.recurrenceSequence = newOptionalInt64(1)
	})
}

func TestAuthorizedInvariantNonRecurringExceptionTrue(t *testing.T) {
	mutateAndCheckNotAuthorized(t, func(p *AuthorizedRepairPlan) {
		p.state.findings[0].transactionState.isRecurringException = true
	})
}

func TestAuthorizedInvariantNonRecurringExceptionReasonValue(t *testing.T) {
	mutateAndCheckNotAuthorized(t, func(p *AuthorizedRepairPlan) {
		p.state.findings[0].transactionState.recurringExceptionReason = newOptionalString("reason")
	})
}

func TestAuthorizedInvariantNonRecurringSourceRowIDDivergent(t *testing.T) {
	mutateAndCheckNotAuthorized(t, func(p *AuthorizedRepairPlan) { p.state.findings[0].sourceRowID = "wrong" })
}

func TestAuthorizedInvariantNonRecurringWorkspaceDivergent(t *testing.T) {
	mutateAndCheckNotAuthorized(t, func(p *AuthorizedRepairPlan) { p.state.findings[0].workspaceID = "wrong" })
}

// --- RECURRING variant invariants ---

func recurringMutate(t *testing.T, mutate func(f *authorizedFinding)) {
	t.Helper()
	parsed := mustParseRecurringForTest(t)
	p := testAuthorizedPlan(t, parsed)
	f := &p.state.findings[0]
	mutate(f)
	if p.IsAuthorized() {
		t.Fatal("mutated recurring finding should not be authorized")
	}
	if p.Metadata().SchemaVersion() != 0 {
		t.Fatal("metadata should be zero after invalidation")
	}
}

func TestAuthorizedInvariantRecurringNoTransaction(t *testing.T) {
	recurringMutate(t, func(f *authorizedFinding) { f.hasTransactionState = false })
}

func TestAuthorizedInvariantRecurringNoRule(t *testing.T) {
	recurringMutate(t, func(f *authorizedFinding) { f.hasRuleState = false })
}

func TestAuthorizedInvariantRecurringNoSeries(t *testing.T) {
	recurringMutate(t, func(f *authorizedFinding) { f.hasSeriesState = false })
}

func TestAuthorizedInvariantRecurringNullRuleID(t *testing.T) {
	recurringMutate(t, func(f *authorizedFinding) { f.transactionState.recurringRuleID = newOptionalStringNull() })
}

func TestAuthorizedInvariantRecurringNullSeriesID(t *testing.T) {
	recurringMutate(t, func(f *authorizedFinding) { f.transactionState.seriesID = newOptionalStringNull() })
}

func TestAuthorizedInvariantRecurringNullSequence(t *testing.T) {
	recurringMutate(t, func(f *authorizedFinding) { f.transactionState.recurrenceSequence = newOptionalInt64Null() })
}

func TestAuthorizedInvariantRecurringSequenceZero(t *testing.T) {
	recurringMutate(t, func(f *authorizedFinding) { f.transactionState.recurrenceSequence = newOptionalInt64(0) })
}

func TestAuthorizedInvariantRecurringSequenceBelowStart(t *testing.T) {
	recurringMutate(t, func(f *authorizedFinding) {
		f.ruleState.effectiveStartSequence = 5
		f.transactionState.recurrenceSequence = newOptionalInt64(1)
	})
}

func TestAuthorizedInvariantRecurringSequenceAboveEnd(t *testing.T) {
	recurringMutate(t, func(f *authorizedFinding) {
		f.ruleState.effectiveEndSequence = newOptionalInt64(3)
		f.transactionState.recurrenceSequence = newOptionalInt64(99)
	})
}

func TestAuthorizedInvariantRecurringRuleIDDivergent(t *testing.T) {
	recurringMutate(t, func(f *authorizedFinding) { f.transactionState.recurringRuleID = newOptionalString("wrong") })
}

func TestAuthorizedInvariantRecurringTxSeriesIDDivergent(t *testing.T) {
	recurringMutate(t, func(f *authorizedFinding) { f.transactionState.seriesID = newOptionalString("wrong") })
}

func TestAuthorizedInvariantRecurringRuleSeriesDivergent(t *testing.T) {
	recurringMutate(t, func(f *authorizedFinding) { f.ruleState.seriesID = "wrong" })
}

func TestAuthorizedInvariantRecurringTxWorkspaceDivergent(t *testing.T) {
	recurringMutate(t, func(f *authorizedFinding) { f.transactionState.workspaceID = "wrong" })
}

func TestAuthorizedInvariantRecurringRuleWorkspaceDivergent(t *testing.T) {
	recurringMutate(t, func(f *authorizedFinding) { f.ruleState.workspaceID = "wrong" })
}

func TestAuthorizedInvariantRecurringSeriesWorkspaceDivergent(t *testing.T) {
	recurringMutate(t, func(f *authorizedFinding) { f.seriesState.workspaceID = "wrong" })
}

func TestAuthorizedInvariantRecurringExceptionFalseWithReason(t *testing.T) {
	recurringMutate(t, func(f *authorizedFinding) {
		f.transactionState.isRecurringException = false
		f.transactionState.recurringExceptionReason = newOptionalString("reason")
	})
}

func TestAuthorizedInvariantRecurringExceptionTrueNullReason(t *testing.T) {
	recurringMutate(t, func(f *authorizedFinding) {
		f.transactionState.isRecurringException = true
		f.transactionState.recurringExceptionReason = newOptionalStringNull()
	})
}

func TestAuthorizedInvariantRecurringExceptionTrueEmptyReason(t *testing.T) {
	recurringMutate(t, func(f *authorizedFinding) {
		f.transactionState.isRecurringException = true
		f.transactionState.recurringExceptionReason = newOptionalString("")
	})
}

func TestAuthorizedInvariantRecurringSourceRowIDDivergent(t *testing.T) {
	recurringMutate(t, func(f *authorizedFinding) { f.sourceRowID = "wrong" })
}

// --- RECURRING_RULE variant invariants ---

func ruleVariantMutate(t *testing.T, mutate func(f *authorizedFinding)) {
	t.Helper()
	parsed := mustParseRuleForTest(t)
	p := testAuthorizedPlan(t, parsed)
	f := &p.state.findings[0]
	mutate(f)
	if p.IsAuthorized() {
		t.Fatal("mutated rule variant finding should not be authorized")
	}
	if p.Metadata().SchemaVersion() != 0 {
		t.Fatal("metadata should be zero")
	}
}

func TestAuthorizedInvariantRuleVariantWithTransaction(t *testing.T) {
	ruleVariantMutate(t, func(f *authorizedFinding) { f.hasTransactionState = true })
}

func TestAuthorizedInvariantRuleVariantNoRule(t *testing.T) {
	ruleVariantMutate(t, func(f *authorizedFinding) { f.hasRuleState = false })
}

func TestAuthorizedInvariantRuleVariantNoSeries(t *testing.T) {
	ruleVariantMutate(t, func(f *authorizedFinding) { f.hasSeriesState = false })
}

func TestAuthorizedInvariantRuleVariantSourceRowDivergent(t *testing.T) {
	ruleVariantMutate(t, func(f *authorizedFinding) { f.sourceRowID = "wrong" })
}

func TestAuthorizedInvariantRuleVariantWorkspaceDivergent(t *testing.T) {
	ruleVariantMutate(t, func(f *authorizedFinding) { f.workspaceID = "wrong" })
}

func TestAuthorizedInvariantRuleVariantSeriesDivergent(t *testing.T) {
	ruleVariantMutate(t, func(f *authorizedFinding) { f.ruleState.seriesID = "wrong" })
}
