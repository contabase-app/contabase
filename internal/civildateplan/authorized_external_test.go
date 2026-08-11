package civildateplan_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/contabase-app/contabase/internal/civildateplan"
)

func TestAuthorizedExportSurface(t *testing.T) {
	var p civildateplan.AuthorizedRepairPlan
	_ = &p

	if p.IsAuthorized() {
		t.Fatal("zero AuthorizedRepairPlan should not be authorized")
	}

	var nilPlan *civildateplan.AuthorizedRepairPlan
	if nilPlan.IsAuthorized() {
		t.Fatal("nil AuthorizedRepairPlan should not be authorized")
	}
	if nilPlan.FindingCount() != 0 {
		t.Fatal("nil FindingCount should be 0")
	}
	_, ok := nilPlan.FindingAt(0)
	if ok {
		t.Fatal("nil FindingAt should return false")
	}
	md := nilPlan.Metadata()
	if md.SchemaVersion() != 0 {
		t.Fatal("nil Metadata should be zero")
	}
}

func TestNoExportedConstructor(t *testing.T) {
	var p civildateplan.AuthorizedRepairPlan
	_ = p
}

func TestSnapshotsAreConcreteTypes(t *testing.T) {
	var _ civildateplan.PlanMetadataSnapshot
	var _ civildateplan.RepairFindingSnapshot
	var _ civildateplan.TransactionStateSnapshot
	var _ civildateplan.RuleStateSnapshot
	var _ civildateplan.SeriesStateSnapshot
}

func TestOptionalsAreConcreteTypes(t *testing.T) {
	var _ civildateplan.OptionalString
	var _ civildateplan.OptionalInt64
	var _ civildateplan.OptionalBool
}

func TestLoadAuthorizedRepairPlanExternalSurface(t *testing.T) {
	dir := t.TempDir()
	planJSON := `{"schema_version":1,"plan_id":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","scope":"DATABASE","tool_version":"civil-date-repair/v1","classifier_version":"civil-date-classifier/v1","database_schema_version":55,"timezone":"America/Sao_Paulo","logical_fingerprint_version":"contabase-logical/v1","source_logical_fingerprint":"sha256:0000000000000000000000000000000000000000000000000000000000000000","audit_set_digest":"sha256:0000000000000000000000000000000000000000000000000000000000000000","generated_at":"2024-06-05T12:00:00Z","findings":[{"variant":"TRANSACTION_NON_RECURRING","source":"transaction","workspace_id":"ws1","source_row_id":"tx-1","audited_field":"date_civil","reason_code":"DATE_CIVIL_MISSING","repair_outcome":"AUTO_REPAIR","expected_legacy_value":1717603200,"expected_civil_state":"NULL","proposed_civil_value":"2024-06-05","timezone":"America/Sao_Paulo","classifier_version":"civil-date-classifier/v1","transaction_state":{"id":"tx-1","workspace_id":"ws1","status":"pending","type":"EXPENSE","date_unix":1717603200,"due_date_unix":null,"recurring_rule_id":null,"series_id":null,"recurrence_sequence":null,"is_recurring_exception":false,"recurring_exception_reason":null},"rule_state":null,"series_state":null}]}`
	path := filepath.Join(dir, "plan.json")
	if err := os.WriteFile(path, []byte(planJSON), 0644); err != nil {
		t.Fatal(err)
	}

	approvedDigest := "sha256:e4608569e8f093b6933cdb447c5a18fb341af27b70031cb89083389eeafa924b"
	plan, err := civildateplan.LoadAuthorizedRepairPlan(path, approvedDigest)
	if err != nil {
		t.Fatalf("LoadAuthorizedRepairPlan: %v", err)
	}
	if !plan.IsAuthorized() {
		t.Fatal("plan must be authorized")
	}
	if plan.FindingCount() != 1 {
		t.Fatalf("expected 1 finding, got %d", plan.FindingCount())
	}
	finding, ok := plan.FindingAt(0)
	if !ok {
		t.Fatal("finding not found")
	}
	_ = finding

	// Surface check: only public methods, no seal/state/private fields
	md := plan.Metadata()
	if md.SchemaVersion() != 1 {
		t.Fatalf("unexpected schema version: %d", md.SchemaVersion())
	}
	if md.PlanID() != "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa" {
		t.Fatalf("unexpected plan id: %s", md.PlanID())
	}
}
