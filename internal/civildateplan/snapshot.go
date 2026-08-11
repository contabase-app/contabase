package civildateplan

type PlanMetadataSnapshot struct {
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
}

func (s PlanMetadataSnapshot) SchemaVersion() int64             { return s.schemaVersion }
func (s PlanMetadataSnapshot) PlanID() string                    { return s.planID }
func (s PlanMetadataSnapshot) Scope() string                     { return s.scope }
func (s PlanMetadataSnapshot) ToolVersion() string               { return s.toolVersion }
func (s PlanMetadataSnapshot) ClassifierVersion() string         { return s.classifierVersion }
func (s PlanMetadataSnapshot) DatabaseSchemaVersion() int64      { return s.databaseSchemaVersion }
func (s PlanMetadataSnapshot) Timezone() string                  { return s.timezone }
func (s PlanMetadataSnapshot) LogicalFingerprintVersion() string { return s.logicalFingerprintVersion }
func (s PlanMetadataSnapshot) SourceLogicalFingerprint() string  { return s.sourceLogicalFingerprint }
func (s PlanMetadataSnapshot) AuditSetDigest() string            { return s.auditSetDigest }
func (s PlanMetadataSnapshot) GeneratedAt() string               { return s.generatedAt }

type TransactionStateSnapshot struct {
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

func (s TransactionStateSnapshot) ID() string                         { return s.id }
func (s TransactionStateSnapshot) WorkspaceID() string                { return s.workspaceID }
func (s TransactionStateSnapshot) Status() string                     { return s.status }
func (s TransactionStateSnapshot) Type() string                       { return s.typ }
func (s TransactionStateSnapshot) DateUnix() int64                    { return s.dateUnix }
func (s TransactionStateSnapshot) DueDateUnix() OptionalInt64         { return s.dueDateUnix }
func (s TransactionStateSnapshot) RecurringRuleID() OptionalString    { return s.recurringRuleID }
func (s TransactionStateSnapshot) SeriesID() OptionalString           { return s.seriesID }
func (s TransactionStateSnapshot) RecurrenceSequence() OptionalInt64  { return s.recurrenceSequence }
func (s TransactionStateSnapshot) IsRecurringException() bool         { return s.isRecurringException }
func (s TransactionStateSnapshot) RecurringExceptionReason() OptionalString { return s.recurringExceptionReason }

type RuleStateSnapshot struct {
	id                     string
	workspaceID            string
	seriesID               string
	parentRuleID           OptionalString
	effectiveStartSequence int64
	effectiveEndSequence   OptionalInt64
	active                 bool
}

func (s RuleStateSnapshot) ID() string                        { return s.id }
func (s RuleStateSnapshot) WorkspaceID() string               { return s.workspaceID }
func (s RuleStateSnapshot) SeriesID() string                  { return s.seriesID }
func (s RuleStateSnapshot) ParentRuleID() OptionalString      { return s.parentRuleID }
func (s RuleStateSnapshot) EffectiveStartSequence() int64     { return s.effectiveStartSequence }
func (s RuleStateSnapshot) EffectiveEndSequence() OptionalInt64 { return s.effectiveEndSequence }
func (s RuleStateSnapshot) Active() bool                      { return s.active }

type SeriesStateSnapshot struct {
	id          string
	workspaceID string
	active      bool
}

func (s SeriesStateSnapshot) ID() string          { return s.id }
func (s SeriesStateSnapshot) WorkspaceID() string { return s.workspaceID }
func (s SeriesStateSnapshot) Active() bool        { return s.active }

type RepairFindingSnapshot struct {
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
	transactionState    TransactionStateSnapshot
	hasTransactionState bool
	ruleState           RuleStateSnapshot
	hasRuleState        bool
	seriesState         SeriesStateSnapshot
	hasSeriesState      bool
}

func (s RepairFindingSnapshot) Variant() string               { return s.variant }
func (s RepairFindingSnapshot) Source() string                { return s.source }
func (s RepairFindingSnapshot) WorkspaceID() string           { return s.workspaceID }
func (s RepairFindingSnapshot) SourceRowID() string           { return s.sourceRowID }
func (s RepairFindingSnapshot) AuditedField() string          { return s.auditedField }
func (s RepairFindingSnapshot) ReasonCode() string            { return s.reasonCode }
func (s RepairFindingSnapshot) RepairOutcome() string         { return s.repairOutcome }
func (s RepairFindingSnapshot) ExpectedLegacyValue() int64    { return s.expectedLegacyValue }
func (s RepairFindingSnapshot) ExpectedCivilState() string    { return s.expectedCivilState }
func (s RepairFindingSnapshot) ProposedCivilValue() string    { return s.proposedCivilValue }
func (s RepairFindingSnapshot) Timezone() string              { return s.timezone }
func (s RepairFindingSnapshot) ClassifierVersion() string     { return s.classifierVersion }

func (s RepairFindingSnapshot) TransactionState() (TransactionStateSnapshot, bool) {
	return s.transactionState, s.hasTransactionState
}
func (s RepairFindingSnapshot) RuleState() (RuleStateSnapshot, bool) {
	return s.ruleState, s.hasRuleState
}
func (s RepairFindingSnapshot) SeriesState() (SeriesStateSnapshot, bool) {
	return s.seriesState, s.hasSeriesState
}
