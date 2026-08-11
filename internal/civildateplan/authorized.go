package civildateplan

import (
	"encoding/hex"
	"fmt"
	"strings"
	"time"
)

type authorizationSeal struct {
	marker byte
}

var authorizedPlanSeal = &authorizationSeal{
	marker: 0xA5,
}

type AuthorizedRepairPlan struct {
	seal   *authorizationSeal
	state  *authorizedPlanState
	digest [32]byte
}

type authorizedPlanState struct {
	schemaVersion             int64
	planID                    string
	scope                     string
	toolVersion               string
	classifierVersion         string
	databaseSchemaVersion     int64
	timezone                  string
	logicalFingerprintVersion string
	sourceLogicalFingerprint  string
	auditSetDigest            string
	generatedAt               string
	findings                  []authorizedFinding
}

type authorizedFinding struct {
	variant             string
	source              string
	workspaceID         string
	sourceRowID         string
	auditedField        string
	reasonCode          string
	repairOutcome       string
	expectedLegacyValue int64
	expectedCivilState  string
	proposedCivilValue  string
	timezone            string
	classifierVersion   string

	transactionState    authorizedTransactionState
	hasTransactionState bool

	ruleState    authorizedRuleState
	hasRuleState bool

	seriesState    authorizedSeriesState
	hasSeriesState bool
}

type authorizedTransactionState struct {
	id                       string
	workspaceID              string
	status                   string
	typ                      string
	dateUnix                 int64
	dueDateUnix              OptionalInt64
	recurringRuleID          OptionalString
	seriesID                 OptionalString
	recurrenceSequence       OptionalInt64
	isRecurringException     bool
	recurringExceptionReason OptionalString
}

type authorizedRuleState struct {
	id                     string
	workspaceID            string
	seriesID               string
	parentRuleID           OptionalString
	effectiveStartSequence int64
	effectiveEndSequence   OptionalInt64
	active                 bool
}

type authorizedSeriesState struct {
	id          string
	workspaceID string
	active      bool
}

// ---------- Public methods ----------

func (p *AuthorizedRepairPlan) IsAuthorized() bool {
	if p == nil {
		return false
	}
	if p.seal != authorizedPlanSeal {
		return false
	}
	if p.state == nil {
		return false
	}
	return p.state.invariantsValid()
}

func (p *AuthorizedRepairPlan) Digest() string {
	if p == nil {
		return ""
	}
	if p.seal != authorizedPlanSeal {
		return ""
	}
	if p.state == nil || !p.state.invariantsValid() {
		return ""
	}
	d := hex.EncodeToString(p.digest[:])
	return "sha256:" + d
}

func (p *AuthorizedRepairPlan) Metadata() PlanMetadataSnapshot {
	if !p.IsAuthorized() {
		return PlanMetadataSnapshot{}
	}
	s := p.state
	return PlanMetadataSnapshot{
		schemaVersion:             s.schemaVersion,
		planID:                    s.planID,
		scope:                     s.scope,
		toolVersion:               s.toolVersion,
		classifierVersion:         s.classifierVersion,
		databaseSchemaVersion:     s.databaseSchemaVersion,
		timezone:                  s.timezone,
		logicalFingerprintVersion: s.logicalFingerprintVersion,
		sourceLogicalFingerprint:  s.sourceLogicalFingerprint,
		auditSetDigest:            s.auditSetDigest,
		generatedAt:               s.generatedAt,
	}
}

func (p *AuthorizedRepairPlan) FindingCount() int {
	if !p.IsAuthorized() {
		return 0
	}
	return len(p.state.findings)
}

func (p *AuthorizedRepairPlan) FindingAt(index int) (RepairFindingSnapshot, bool) {
	if !p.IsAuthorized() {
		return RepairFindingSnapshot{}, false
	}
	if index < 0 || index >= len(p.state.findings) {
		return RepairFindingSnapshot{}, false
	}
	return p.state.findings[index].snapshot(), true
}

func (f authorizedFinding) snapshot() RepairFindingSnapshot {
	s := RepairFindingSnapshot{
		variant:             f.variant,
		source:              f.source,
		workspaceID:         f.workspaceID,
		sourceRowID:         f.sourceRowID,
		auditedField:        f.auditedField,
		reasonCode:          f.reasonCode,
		repairOutcome:       f.repairOutcome,
		expectedLegacyValue: f.expectedLegacyValue,
		expectedCivilState:  f.expectedCivilState,
		proposedCivilValue:  f.proposedCivilValue,
		timezone:            f.timezone,
		classifierVersion:   f.classifierVersion,
	}
	if f.hasTransactionState {
		s.transactionState = TransactionStateSnapshot{
			id:                       f.transactionState.id,
			workspaceID:              f.transactionState.workspaceID,
			status:                   f.transactionState.status,
			typ:                      f.transactionState.typ,
			dateUnix:                 f.transactionState.dateUnix,
			dueDateUnix:              f.transactionState.dueDateUnix,
			recurringRuleID:          f.transactionState.recurringRuleID,
			seriesID:                 f.transactionState.seriesID,
			recurrenceSequence:       f.transactionState.recurrenceSequence,
			isRecurringException:     f.transactionState.isRecurringException,
			recurringExceptionReason: f.transactionState.recurringExceptionReason,
		}
		s.hasTransactionState = true
	}
	if f.hasRuleState {
		s.ruleState = RuleStateSnapshot{
			id:                     f.ruleState.id,
			workspaceID:            f.ruleState.workspaceID,
			seriesID:               f.ruleState.seriesID,
			parentRuleID:           f.ruleState.parentRuleID,
			effectiveStartSequence: f.ruleState.effectiveStartSequence,
			effectiveEndSequence:   f.ruleState.effectiveEndSequence,
			active:                 f.ruleState.active,
		}
		s.hasRuleState = true
	}
	if f.hasSeriesState {
		s.seriesState = SeriesStateSnapshot{
			id:          f.seriesState.id,
			workspaceID: f.seriesState.workspaceID,
			active:      f.seriesState.active,
		}
		s.hasSeriesState = true
	}
	return s
}

// ---------- Invariants ----------

func (s *authorizedPlanState) invariantsValid() bool {
	if s == nil {
		return false
	}
	if s.schemaVersion != schemaV1 {
		return false
	}
	if s.scope != scopeV1 {
		return false
	}
	if s.toolVersion != toolVersionV1 {
		return false
	}
	if s.classifierVersion != classifierVersionV1 {
		return false
	}
	if s.logicalFingerprintVersion != logicalFingerprintVersV1 {
		return false
	}
	if s.databaseSchemaVersion <= 0 {
		return false
	}
	if !isValidDigest(s.sourceLogicalFingerprint) {
		return false
	}
	if !isValidDigest(s.auditSetDigest) {
		return false
	}
	if !isValidPlanID(s.planID) {
		return false
	}
	if s.timezone == "" {
		return false
	}
	if _, err := time.LoadLocation(s.timezone); err != nil {
		return false
	}
	if !isValidCanonicalTimestamp(s.generatedAt) {
		return false
	}
	if len(s.findings) == 0 {
		return false
	}
	seen := make(map[string]bool)
	for _, f := range s.findings {
		key := f.source + "/" + f.variant + "/" + f.workspaceID + "/" + f.sourceRowID + "/" + f.auditedField
		if seen[key] {
			return false
		}
		seen[key] = true
		if !f.invariantsValid(s.timezone, s.classifierVersion) {
			return false
		}
	}
	return true
}

func isValidPlanID(pid string) bool {
	if len(pid) != 32 {
		return false
	}
	for _, c := range pid {
		if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f')) {
			return false
		}
	}
	return true
}

func isValidCanonicalTimestamp(gt string) bool {
	if !strings.HasSuffix(gt, "Z") {
		return false
	}
	parsed, err := time.Parse(time.RFC3339Nano, gt)
	if err != nil {
		return false
	}
	if parsed.Location() != time.UTC {
		return false
	}
	return parsed.Format(time.RFC3339Nano) == gt
}

func (f *authorizedFinding) invariantsValid(planTimezone, planClassifierVersion string) bool {
	if f.timezone != planTimezone {
		return false
	}
	if f.classifierVersion != planClassifierVersion {
		return false
	}
	if !isValidVariant(f.variant) {
		return false
	}
	if !isValidSource(f.source) {
		return false
	}
	if f.workspaceID == "" {
		return false
	}
	if f.sourceRowID == "" {
		return false
	}
	if f.repairOutcome != repairOutcomeAutoRepair {
		return false
	}
	if f.expectedCivilState != "NULL" {
		return false
	}
	if f.expectedLegacyValue <= 0 {
		return false
	}
	if !isValidFieldReason(f.source, f.auditedField, f.reasonCode) {
		return false
	}
	if f.auditedField == "paid_date_civil" {
		return false
	}
	switch f.variant {
	case variantNonRecurring:
		return f.invariantsNonRecurring()
	case variantRecurring:
		return f.invariantsRecurring()
	case variantRule:
		return f.invariantsRuleVariant()
	default:
		return false
	}
}

func isValidVariant(v string) bool {
	switch v {
	case variantNonRecurring, variantRecurring, variantRule:
		return true
	}
	return false
}

func isValidSource(s string) bool {
	switch s {
	case sourceTransaction, sourceRecurringRule:
		return true
	}
	return false
}

func isValidFieldReason(src, field, reason string) bool {
	switch {
	case src == sourceTransaction && field == "date_civil" && reason == reasonDateCivilMissing:
		return true
	case src == sourceTransaction && field == "due_date_civil" && reason == reasonDueDateCivilMissing:
		return true
	case src == sourceRecurringRule && field == "start_date_civil" && reason == reasonStartDateCivilMissing:
		return true
	case src == sourceRecurringRule && field == "starts_at_civil" && reason == reasonStartsAtCivilMissing:
		return true
	}
	return false
}

func (f *authorizedFinding) invariantsNonRecurring() bool {
	if f.source != sourceTransaction {
		return false
	}
	if !f.hasTransactionState {
		return false
	}
	if f.hasRuleState {
		return false
	}
	if f.hasSeriesState {
		return false
	}
	ts := f.transactionState
	if !ts.valid() {
		return false
	}
	if f.sourceRowID != ts.id {
		return false
	}
	if f.workspaceID != ts.workspaceID {
		return false
	}
	if !ts.recurringRuleID.IsNull() || !ts.recurringRuleID.valid() {
		return false
	}
	if !ts.seriesID.IsNull() || !ts.seriesID.valid() {
		return false
	}
	if !ts.recurrenceSequence.IsNull() || !ts.recurrenceSequence.valid() {
		return false
	}
	if ts.isRecurringException {
		return false
	}
	if !ts.recurringExceptionReason.IsNull() || !ts.recurringExceptionReason.valid() {
		return false
	}
	if f.auditedField == "date_civil" {
		if f.expectedLegacyValue != ts.dateUnix {
			return false
		}
	}
	if f.auditedField == "due_date_civil" {
		if ts.dueDateUnix.IsNull() {
			return false
		}
		v, ok := ts.dueDateUnix.Value()
		if !ok || f.expectedLegacyValue != v {
			return false
		}
	}
	return true
}

func (f *authorizedFinding) invariantsRecurring() bool {
	if f.source != sourceTransaction {
		return false
	}
	if !f.hasTransactionState || !f.hasRuleState || !f.hasSeriesState {
		return false
	}
	ts := f.transactionState
	rs := f.ruleState
	ss := f.seriesState
	if !ts.valid() || !rs.valid() || !ss.valid() {
		return false
	}
	if f.sourceRowID != ts.id {
		return false
	}
	if f.workspaceID != ts.workspaceID {
		return false
	}
	if f.workspaceID != rs.workspaceID {
		return false
	}
	if f.workspaceID != ss.workspaceID {
		return false
	}
	if ts.recurringRuleID.IsNull() {
		return false
	}
	if ts.seriesID.IsNull() {
		return false
	}
	if ts.recurrenceSequence.IsNull() {
		return false
	}
	rRuleVal, _ := ts.recurringRuleID.Value()
	if rRuleVal != rs.id {
		return false
	}
	sIDVal, _ := ts.seriesID.Value()
	if sIDVal != rs.seriesID {
		return false
	}
	if rs.seriesID != ss.id {
		return false
	}
	seqVal, _ := ts.recurrenceSequence.Value()
	if seqVal <= 0 {
		return false
	}
	if seqVal < rs.effectiveStartSequence {
		return false
	}
	if ee, ok := rs.effectiveEndSequence.Value(); ok {
		if seqVal > ee {
			return false
		}
	}
	if !ts.isRecurringException {
		if !ts.recurringExceptionReason.IsNull() || !ts.recurringExceptionReason.valid() {
			return false
		}
	} else {
		v, ok := ts.recurringExceptionReason.Value()
		if !ok || v == "" {
			return false
		}
	}
	if f.auditedField == "date_civil" {
		if f.expectedLegacyValue != ts.dateUnix {
			return false
		}
	}
	if f.auditedField == "due_date_civil" {
		if ts.dueDateUnix.IsNull() {
			return false
		}
		v, ok := ts.dueDateUnix.Value()
		if !ok || f.expectedLegacyValue != v {
			return false
		}
	}
	return true
}

func (f *authorizedFinding) invariantsRuleVariant() bool {
	if f.source != sourceRecurringRule {
		return false
	}
	if f.hasTransactionState {
		return false
	}
	if !f.hasRuleState || !f.hasSeriesState {
		return false
	}
	rs := f.ruleState
	ss := f.seriesState
	if !rs.valid() || !ss.valid() {
		return false
	}
	if f.sourceRowID != rs.id {
		return false
	}
	if f.workspaceID != rs.workspaceID {
		return false
	}
	if rs.workspaceID != ss.workspaceID {
		return false
	}
	if rs.seriesID != ss.id {
		return false
	}
	return true
}

func (ts authorizedTransactionState) valid() bool {
	if ts.id == "" {
		return false
	}
	if ts.workspaceID == "" {
		return false
	}
	if !validStatuses[ts.status] {
		return false
	}
	if !validTypes[ts.typ] {
		return false
	}
	if !ts.dueDateUnix.valid() {
		return false
	}
	if !ts.recurringRuleID.valid() {
		return false
	}
	if !ts.seriesID.valid() {
		return false
	}
	if !ts.recurrenceSequence.valid() {
		return false
	}
	if !ts.recurringExceptionReason.valid() {
		return false
	}
	return true
}

func (rs authorizedRuleState) valid() bool {
	if rs.id == "" {
		return false
	}
	if rs.workspaceID == "" {
		return false
	}
	if rs.seriesID == "" {
		return false
	}
	if !rs.parentRuleID.valid() {
		return false
	}
	if rs.effectiveStartSequence <= 0 {
		return false
	}
	if !rs.effectiveEndSequence.valid() {
		return false
	}
	if ee, ok := rs.effectiveEndSequence.Value(); ok {
		if ee < rs.effectiveStartSequence {
			return false
		}
	}
	return true
}

func (ss authorizedSeriesState) valid() bool {
	if ss.id == "" {
		return false
	}
	if ss.workspaceID == "" {
		return false
	}
	return true
}

// ---------- Transformation parsed → canonical ----------

func buildAuthorizedPlanState(validated validatedPlanV1) (*authorizedPlanState, error) {
	if !validated.validated || validated.plan == nil {
		return nil, fmt.Errorf("validated plan is invalid")
	}
	parsed := validated.plan

	s := &authorizedPlanState{
		schemaVersion:             parsed.schemaVersion.value,
		planID:                    parsed.planID.value,
		scope:                     parsed.scope.value,
		toolVersion:               parsed.toolVersion.value,
		classifierVersion:         parsed.classifierVersion.value,
		databaseSchemaVersion:     parsed.databaseSchemaVersion.value,
		timezone:                  parsed.timezone.value,
		logicalFingerprintVersion: parsed.logicalFingerprintVersion.value,
		sourceLogicalFingerprint:  parsed.sourceLogicalFingerprint.value,
		auditSetDigest:            parsed.auditSetDigest.value,
		generatedAt:               parsed.generatedAt.value,
	}
	s.findings = make([]authorizedFinding, len(parsed.findings))
	for i, pf := range parsed.findings {
		f := authorizedFinding{
			variant:             pf.variant.value,
			source:              pf.source.value,
			workspaceID:         pf.workspaceID.value,
			sourceRowID:         pf.sourceRowID.value,
			auditedField:        pf.auditedField.value,
			reasonCode:          pf.reasonCode.value,
			repairOutcome:       pf.repairOutcome.value,
			expectedLegacyValue: pf.expectedLegacyValue.value,
			expectedCivilState:  pf.expectedCivilState.value,
			proposedCivilValue:  pf.proposedCivilValue.value,
			timezone:            pf.timezone.value,
			classifierVersion:   pf.classifierVersion.value,
		}
		if pf.transactionState.value != nil {
			f.transactionState = copyTransactionState(pf.transactionState.value)
			f.hasTransactionState = true
		}
		if pf.ruleState.value != nil {
			f.ruleState = copyRuleState(pf.ruleState.value)
			f.hasRuleState = true
		}
		if pf.seriesState.value != nil {
			f.seriesState = copySeriesState(pf.seriesState.value)
			f.hasSeriesState = true
		}
		s.findings[i] = f
	}
	return s, nil
}

func copyTransactionState(ts *parsedTransactionState) authorizedTransactionState {
	s := authorizedTransactionState{
		id:                   ts.id.value,
		workspaceID:          ts.workspaceID.value,
		status:               ts.status.value,
		typ:                  ts.typeField.value,
		dateUnix:             ts.dateUnix.value,
		isRecurringException: ts.isRecurringException.value,
	}
	if fieldPresent[*int64](ts.dueDateUnix) && ts.dueDateUnix.value != nil {
		s.dueDateUnix = newOptionalInt64(*ts.dueDateUnix.value)
	} else if fieldNull[*int64](ts.dueDateUnix) {
		s.dueDateUnix = newOptionalInt64Null()
	}
	if fieldPresent[*string](ts.recurringRuleID) && ts.recurringRuleID.value != nil {
		s.recurringRuleID = newOptionalString(*ts.recurringRuleID.value)
	} else if fieldNull[*string](ts.recurringRuleID) {
		s.recurringRuleID = newOptionalStringNull()
	}
	if fieldPresent[*string](ts.seriesID) && ts.seriesID.value != nil {
		s.seriesID = newOptionalString(*ts.seriesID.value)
	} else if fieldNull[*string](ts.seriesID) {
		s.seriesID = newOptionalStringNull()
	}
	if fieldPresent[*int64](ts.recurrenceSequence) && ts.recurrenceSequence.value != nil {
		s.recurrenceSequence = newOptionalInt64(*ts.recurrenceSequence.value)
	} else if fieldNull[*int64](ts.recurrenceSequence) {
		s.recurrenceSequence = newOptionalInt64Null()
	}
	if fieldPresent[*string](ts.recurringExceptionReason) && ts.recurringExceptionReason.value != nil {
		s.recurringExceptionReason = newOptionalString(*ts.recurringExceptionReason.value)
	} else if fieldNull[*string](ts.recurringExceptionReason) {
		s.recurringExceptionReason = newOptionalStringNull()
	}
	return s
}

func copyRuleState(rs *parsedRuleState) authorizedRuleState {
	s := authorizedRuleState{
		id:                     rs.id.value,
		workspaceID:            rs.workspaceID.value,
		seriesID:               rs.seriesID.value,
		effectiveStartSequence: rs.effectiveStartSequence.value,
		active:                 rs.active.value,
	}
	if fieldPresent[*string](rs.parentRuleID) && rs.parentRuleID.value != nil {
		s.parentRuleID = newOptionalString(*rs.parentRuleID.value)
	} else if fieldNull[*string](rs.parentRuleID) {
		s.parentRuleID = newOptionalStringNull()
	}
	if fieldPresent[*int64](rs.effectiveEndSequence) && rs.effectiveEndSequence.value != nil {
		s.effectiveEndSequence = newOptionalInt64(*rs.effectiveEndSequence.value)
	} else if fieldNull[*int64](rs.effectiveEndSequence) {
		s.effectiveEndSequence = newOptionalInt64Null()
	}
	return s
}

func copySeriesState(ss *parsedSeriesState) authorizedSeriesState {
	return authorizedSeriesState{
		id:          ss.id.value,
		workspaceID: ss.workspaceID.value,
		active:      ss.active.value,
	}
}
