package civildateplan

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
)

func computeApprovedDigest(t *testing.T, data []byte) string {
	t.Helper()
	_, digest, err := preparePlanBytes(data)
	if err != nil {
		t.Fatal(err)
	}
	return "sha256:" + hex.EncodeToString(digest[:])
}

func writePlanFile(t *testing.T, dir, name, content string) string {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}
	return path
}

// ---------- success ----------

func TestLoadAuthorizedRepairPlanSuccess(t *testing.T) {
	dir := t.TempDir()
	content := planJSON(nrFinding())
	path := writePlanFile(t, dir, "plan.json", content)
	wantDigest := computeApprovedDigest(t, []byte(content))

	plan, err := LoadAuthorizedRepairPlan(path, wantDigest)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !plan.IsAuthorized() {
		t.Fatal("plan must be authorized")
	}
	if plan.FindingCount() != 1 {
		t.Fatalf("expected 1 finding, got %d", plan.FindingCount())
	}
}

func TestLoadAuthorizedRepairPlanComplete(t *testing.T) {
	dir := t.TempDir()
	content := planJSON(rcrFinding())
	path := writePlanFile(t, dir, "plan.json", content)
	wantDigest := computeApprovedDigest(t, []byte(content))

	plan, err := LoadAuthorizedRepairPlan(path, wantDigest)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !plan.IsAuthorized() {
		t.Fatal("plan must be authorized")
	}
}

func TestLoadAuthorizedRepairPlanSymlink(t *testing.T) {
	dir := t.TempDir()
	content := planJSON(nrFinding())
	realPath := writePlanFile(t, dir, "plan.json", content)
	linkPath := filepath.Join(dir, "link.json")
	if err := os.Symlink(realPath, linkPath); err != nil {
		t.Skip("symlink not supported")
	}
	wantDigest := computeApprovedDigest(t, []byte(content))

	plan, err := LoadAuthorizedRepairPlan(linkPath, wantDigest)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !plan.IsAuthorized() {
		t.Fatal("plan must be authorized")
	}
}

func TestLoadAuthorizedRepairPlanRepeated(t *testing.T) {
	dir := t.TempDir()
	content := planJSON(nrFinding())
	path := writePlanFile(t, dir, "plan.json", content)
	wantDigest := computeApprovedDigest(t, []byte(content))

	for i := 0; i < 5; i++ {
		plan, err := LoadAuthorizedRepairPlan(path, wantDigest)
		if err != nil {
			t.Fatalf("iteration %d: %v", i, err)
		}
		if !plan.IsAuthorized() {
			t.Fatalf("iteration %d: plan must be authorized", i)
		}
	}
}

func TestLoadAuthorizedRepairPlanIndependentCapabilities(t *testing.T) {
	dir := t.TempDir()
	content := planJSON(nrFinding())
	path := writePlanFile(t, dir, "plan.json", content)
	wantDigest := computeApprovedDigest(t, []byte(content))

	a, err := LoadAuthorizedRepairPlan(path, wantDigest)
	if err != nil {
		t.Fatal(err)
	}
	b, err := LoadAuthorizedRepairPlan(path, wantDigest)
	if err != nil {
		t.Fatal(err)
	}
	if a == b {
		t.Fatal("capabilities must be distinct allocations")
	}
	if !a.IsAuthorized() || !b.IsAuthorized() {
		t.Fatal("both must be authorized")
	}
}

func TestLoadAuthorizedRepairPlanGoldenDigest(t *testing.T) {
	dir := t.TempDir()
	content := planJSON(nrFinding())
	path := writePlanFile(t, dir, "plan.json", content)

	_, digest, err := preparePlanBytes([]byte(content))
	if err != nil {
		t.Fatal(err)
	}

	approved := "sha256:" + hex.EncodeToString(digest[:])

	plan, err := LoadAuthorizedRepairPlan(path, approved)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !plan.IsAuthorized() {
		t.Fatal("plan must be authorized")
	}
}

// ---------- approved digest format ----------

func TestParseApprovedDigestInvalid(t *testing.T) {
	valid := "sha256:" + hex.EncodeToString(make([]byte, 32))

	tests := []struct {
		name  string
		value string
	}{
		{"prefix absent", valid[7:]},
		{"prefix duplicated", "sha256:sha256:" + strings.Repeat("0", 64)[:57]},
		{"bad prefix", "sha255:" + strings.Repeat("0", 64)},
		{"63 chars hex", valid[:70]},
		{"65 chars hex", valid + "0"},
		{"non-hex char g", "sha256:g" + strings.Repeat("0", 63)},
		{"uppercase", "sha256:" + strings.ToUpper(strings.Repeat("a", 64))},
		{"leading space", " " + valid},
		{"trailing space", valid + " "},
		{"newline", valid + "\n"},
		{"crlf", valid + "\r\n"},
		{"raw hex no prefix", strings.Repeat("00", 32)},
		{"empty", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := parseApprovedPlanDigestStrict(tt.value)
			if err == nil {
				t.Fatal("expected error")
			}
			le, ok := err.(*loadError)
			if !ok || le.kind != loadErrorApprovedDigestInvalid {
				t.Fatalf("expected loadErrorApprovedDigestInvalid, got %v", err)
			}
		})
	}
}

func TestParseApprovedDigestSuccess(t *testing.T) {
	var digest [32]byte
	for i := range digest {
		digest[i] = byte(i)
	}
	value := "sha256:" + hex.EncodeToString(digest[:])

	result, err := parseApprovedPlanDigestStrict(value)
	if err != nil {
		t.Fatal(err)
	}
	if result != approvedPlanDigest(digest) {
		t.Fatal("digest mismatch")
	}
}

// ---------- mismatch ----------

func TestLoadAuthorizedRepairPlanDigestMismatch(t *testing.T) {
	dir := t.TempDir()
	content := planJSON(nrFinding())
	path := writePlanFile(t, dir, "plan.json", content)

	// Correct digest then flip one bit
	_, correct, err := preparePlanBytes([]byte(content))
	if err != nil {
		t.Fatal(err)
	}
	correct[0] ^= 1
	wrongDigest := "sha256:" + hex.EncodeToString(correct[:])

	plan, err := LoadAuthorizedRepairPlan(path, wrongDigest)
	if err == nil {
		t.Fatal("expected mismatch error")
	}
	if plan != nil {
		t.Fatal("capability must be nil on error")
	}
	le, ok := err.(*loadError)
	if !ok || le.kind != loadErrorApprovedDigestMismatch {
		t.Fatalf("expected loadErrorApprovedDigestMismatch, got %v", err)
	}
}

// ---------- inherited failures ----------

func TestLoadAuthorizedRepairPlanInheritedErrors(t *testing.T) {
	dir := t.TempDir()

	tests := []struct {
		name     string
		path     string
		digest   string
		wantKind loadErrorKind
	}{
		{"empty path", "", "sha256:" + strings.Repeat("0", 64), loadErrorInvalidPath},
		{"relative path", "relative.json", "sha256:" + strings.Repeat("0", 64), loadErrorInvalidPath},
		{"nonexistent", filepath.Join(dir, "nonexistent.json"), "sha256:" + strings.Repeat("0", 64), loadErrorOpen},
		{"directory", dir, "sha256:" + strings.Repeat("0", 64), loadErrorNotRegular},
		{"empty file", "", "sha256:" + strings.Repeat("0", 64), loadErrorEmpty},
		{"syntax invalid", "", "sha256:" + strings.Repeat("0", 64), loadErrorSyntax},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			testPath := tt.path
			if tt.name == "empty file" {
				testPath = writePlanFile(t, dir, "empty.json", "")
			} else if tt.name == "syntax invalid" {
				testPath = writePlanFile(t, dir, "invalid.json", "not json")
			}
			plan, err := LoadAuthorizedRepairPlan(testPath, tt.digest)
			if err == nil {
				t.Fatal("expected error")
			}
			if plan != nil {
				t.Fatal("capability must be nil on error")
			}
			le, ok := err.(*loadError)
			if !ok || le.kind != tt.wantKind {
				t.Fatalf("expected kind %d, got %v", tt.wantKind, err)
			}
		})
	}
}

// ---------- precedence ----------

func TestLoadAuthorizedRepairPlanPrecedence(t *testing.T) {
	dir := t.TempDir()

	t.Run("invalid path before invalid digest", func(t *testing.T) {
		badDigest := "bad"
		plan, err := LoadAuthorizedRepairPlan("", badDigest)
		if err == nil {
			t.Fatal("expected error")
		}
		if plan != nil {
			t.Fatal("capability must be nil")
		}
		le, ok := err.(*loadError)
		if !ok || le.kind != loadErrorInvalidPath {
			t.Fatalf("expected path error first, got %v", err)
		}
	})

	t.Run("open error before invalid digest", func(t *testing.T) {
		badDigest := "bad"
		plan, err := LoadAuthorizedRepairPlan(filepath.Join(dir, "no.json"), badDigest)
		if err == nil {
			t.Fatal("expected error")
		}
		if plan != nil {
			t.Fatal("capability must be nil")
		}
		// Should be open error, not digest invalid
		_, ok := err.(*loadError)
		if !ok {
			t.Fatalf("expected loadError, got %T", err)
		}
	})

	t.Run("structural error before invalid digest", func(t *testing.T) {
		badDigest := "bad"
		path := writePlanFile(t, dir, "bad.json", "not json")
		plan, err := LoadAuthorizedRepairPlan(path, badDigest)
		if err == nil {
			t.Fatal("expected error")
		}
		if plan != nil {
			t.Fatal("capability must be nil")
		}
		// Should be syntax/schema error, not digest invalid
		_, ok := err.(*loadError)
		if !ok {
			t.Fatalf("expected loadError, got %T", err)
		}
	})

	t.Run("valid plan invalid digest", func(t *testing.T) {
		path := writePlanFile(t, dir, "plan.json", planJSON(nrFinding()))
		badDigest := "bad"
		plan, err := LoadAuthorizedRepairPlan(path, badDigest)
		if err == nil {
			t.Fatal("expected error")
		}
		if plan != nil {
			t.Fatal("capability must be nil")
		}
		le, ok := err.(*loadError)
		if !ok || le.kind != loadErrorApprovedDigestInvalid {
			t.Fatalf("expected digest invalid, got %v", err)
		}
	})

	t.Run("valid plan wrong digest", func(t *testing.T) {
		content := planJSON(nrFinding())
		path := writePlanFile(t, dir, "plan.json", content)
		// Correct digest then flip
		_, correct, err := preparePlanBytes([]byte(content))
		if err != nil {
			t.Fatal(err)
		}
		correct[0] ^= 1
		wrongDigest := "sha256:" + hex.EncodeToString(correct[:])
		plan, err := LoadAuthorizedRepairPlan(path, wrongDigest)
		if err == nil {
			t.Fatal("expected error")
		}
		if plan != nil {
			t.Fatal("capability must be nil")
		}
		le, ok := err.(*loadError)
		if !ok || le.kind != loadErrorApprovedDigestMismatch {
			t.Fatalf("expected mismatch, got %v", err)
		}
	})
}

// ---------- concurrency and immutability ----------

func TestLoadAuthorizedRepairPlanConcurrency(t *testing.T) {
	dir := t.TempDir()
	content := planJSON(nrFinding())
	path := writePlanFile(t, dir, "plan.json", content)
	wantDigest := computeApprovedDigest(t, []byte(content))

	var wg sync.WaitGroup
	errCh := make(chan error, 20)
	for i := 0; i < 10; i++ {
		wg.Add(2)
		go func() {
			defer wg.Done()
			plan, err := LoadAuthorizedRepairPlan(path, wantDigest)
			if err != nil {
				errCh <- fmt.Errorf("correct digest: %v", err)
				return
			}
			if !plan.IsAuthorized() {
				errCh <- fmt.Errorf("plan not authorized")
			}
		}()
		// Wrong digest concurrent goroutine
		wrongDigest := "sha256:" + strings.Repeat("0", 64)
		go func() {
			defer wg.Done()
			plan, err := LoadAuthorizedRepairPlan(path, wrongDigest)
			if err == nil {
				errCh <- fmt.Errorf("expected error for wrong digest")
				return
			}
			if plan != nil {
				errCh <- fmt.Errorf("capability must be nil")
			}
		}()
	}
	wg.Wait()
	close(errCh)
	for err := range errCh {
		t.Error(err)
	}
}

func TestLoadAuthorizedRepairPlanImmutability(t *testing.T) {
	dir := t.TempDir()
	content := planJSON(nrFinding())
	path := writePlanFile(t, dir, "plan.json", content)
	wantDigest := computeApprovedDigest(t, []byte(content))

	plan, err := LoadAuthorizedRepairPlan(path, wantDigest)
	if err != nil {
		t.Fatal(err)
	}

	fc := plan.FindingCount()

	// Mutate the file after load
	if err := os.WriteFile(path, []byte(planJSON(rcrFinding())), 0644); err != nil {
		t.Fatal(err)
	}

	// Capability must be unchanged
	if plan.FindingCount() != fc {
		t.Fatal("capability changed after file mutation")
	}
	if !plan.IsAuthorized() {
		t.Fatal("capability must remain authorized")
	}
}

// ---------- error: zero state leakage ----------

func TestLoadAuthorizedRepairPlanNilOnAllErrors(t *testing.T) {
	// Smoke test: for every error path, return nil
	dir := t.TempDir()
	testPath := filepath.Join(dir, "nonexistent.json")

	plan, err := LoadAuthorizedRepairPlan(testPath, "sha256:"+strings.Repeat("0", 64))
	if err == nil {
		t.Fatal("expected error")
	}
	if plan != nil {
		t.Fatal("capability must be nil on error")
	}

	emptyPath := writePlanFile(t, dir, "empty.json", "")
	plan, err = LoadAuthorizedRepairPlan(emptyPath, "sha256:"+strings.Repeat("0", 64))
	if err == nil {
		t.Fatal("expected error")
	}
	if plan != nil {
		t.Fatal("capability must be nil on empty file")
	}
}

// ---------- Fuzz ----------

func FuzzParseApprovedPlanDigestStrict(f *testing.F) {
	var digest [32]byte
	for i := range digest {
		digest[i] = byte(i)
	}
	f.Add("sha256:" + hex.EncodeToString(digest[:]))
	f.Add("")
	f.Add("bad")
	f.Add("sha256:" + strings.Repeat("0", 64))

	f.Fuzz(func(t *testing.T, value string) {
		result, err := parseApprovedPlanDigestStrict(value)
		if err != nil {
			// On error, result must be zero
			if result != (approvedPlanDigest{}) {
				t.Fatal("error must return zero digest")
			}
			return
		}
		// Success: must be exactly 71 chars
		if len(value) != 71 {
			t.Fatal("success only for 71-byte input")
		}
		if !strings.HasPrefix(value, "sha256:") {
			t.Fatal("success only with sha256: prefix")
		}
		// Round-trip
		hexPart := value[7:]
		for _, c := range hexPart {
			if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
				t.Fatal("success with invalid hex char")
			}
		}
		var expected [32]byte
		decoded, err := hex.DecodeString(hexPart)
		if err != nil {
			t.Fatal("cannot decode valid hex")
		}
		copy(expected[:], decoded)
		if result != approvedPlanDigest(expected) {
			t.Fatal("round-trip mismatch")
		}
	})
}

// ---------- independent golden ----------

const independentGoldenDigest = "sha256:e4608569e8f093b6933cdb447c5a18fb341af27b70031cb89083389eeafa924b"

func TestAuthorizedPlanGoldenIndependentDigest(t *testing.T) {
	dir := t.TempDir()
	content := planJSON(nrFinding())
	path := writePlanFile(t, dir, "plan.json", content)

	plan, err := LoadAuthorizedRepairPlan(path, independentGoldenDigest)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !plan.IsAuthorized() {
		t.Fatal("plan must be authorized")
	}
	if plan.FindingCount() != 1 {
		t.Fatalf("expected 1 finding, got %d", plan.FindingCount())
	}
	if plan.Digest() != independentGoldenDigest {
		t.Fatalf("plan.Digest()=%s, want %s", plan.Digest(), independentGoldenDigest)
	}

	// Alter one nibble: 0xe... → 0xd...
	wrong := "sha256:d4608569e8f093b6933cdb447c5a18fb341af27b70031cb89083389eeafa924b"
	_, err = LoadAuthorizedRepairPlan(path, wrong)
	if err == nil {
		t.Fatal("mismatch expected")
	}
	le, ok := err.(*loadError)
	if !ok || le.kind != loadErrorApprovedDigestMismatch {
		t.Fatalf("expected loadErrorApprovedDigestMismatch, got %v", err)
	}
}

// ---------- raw hash rejection ----------

func TestLoadAuthorizedRejectsRawFileHash(t *testing.T) {
	dir := t.TempDir()
	content := planJSON(nrFinding())
	path := writePlanFile(t, dir, "plan.json", content)

	rawBytes, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	rawHash := sha256.Sum256(rawBytes)
	rawHashStr := "sha256:" + hex.EncodeToString(rawHash[:])

	if rawHashStr == independentGoldenDigest {
		t.Fatalf("raw hash equals golden semantic digest; fixture cannot prove contract")
	}

	plan, err := LoadAuthorizedRepairPlan(path, rawHashStr)
	if err == nil {
		t.Fatalf("raw hash should be rejected, but got: %+v", plan)
	}
	if plan != nil {
		t.Fatal("capability must be nil on error")
	}
	le, ok := err.(*loadError)
	if !ok || le.kind != loadErrorApprovedDigestMismatch {
		t.Fatalf("expected loadErrorApprovedDigestMismatch, got %v", err)
	}
}

// ---------- semantic equivalence ----------

func TestLoadAuthorizedSemanticEquivalentFiles(t *testing.T) {
	dir := t.TempDir()

	// File A: compact JSON (no extra whitespace, standard key order)
	compactContent := planJSON(nrFinding())
	pathA := writePlanFile(t, dir, "a.json", compactContent)

	// File B: reformatted with extra whitespace and different field order
	formattedContent := reformattedNRPlanJSON()
	pathB := writePlanFile(t, dir, "b.json", formattedContent)

	if bytes.Equal([]byte(compactContent), []byte(formattedContent)) {
		t.Fatal("files must have different bytes to prove equivalence")
	}

	planA, err := LoadAuthorizedRepairPlan(pathA, independentGoldenDigest)
	if err != nil {
		t.Fatalf("file A: %v", err)
	}
	if !planA.IsAuthorized() {
		t.Fatal("plan A must be authorized")
	}

	planB, err := LoadAuthorizedRepairPlan(pathB, independentGoldenDigest)
	if err != nil {
		t.Fatalf("file B: %v", err)
	}
	if !planB.IsAuthorized() {
		t.Fatal("plan B must be authorized")
	}

	if planA.Digest() != planB.Digest() {
		t.Fatalf("digests must be equal: A=%s B=%s", planA.Digest(), planB.Digest())
	}
	if planA.Digest() != independentGoldenDigest {
		t.Fatalf("planA.Digest()=%s, want %s", planA.Digest(), independentGoldenDigest)
	}

	// ---------- full metadata comparison ----------

	metaA := planA.Metadata()
	metaB := planB.Metadata()
	if !reflect.DeepEqual(metaA, metaB) {
		t.Fatalf("metadata mismatch:\n  A: %+v\n  B: %+v", metaA, metaB)
	}

	// ---------- full finding comparison ----------

	if planA.FindingCount() != planB.FindingCount() {
		t.Fatalf("finding counts mismatch: A=%d B=%d", planA.FindingCount(), planB.FindingCount())
	}
	for i := 0; i < planA.FindingCount(); i++ {
		fA, okA := planA.FindingAt(i)
		fB, okB := planB.FindingAt(i)
		if okA != okB {
			t.Fatalf("finding[%d] ok flag mismatch: A=%v B=%v", i, okA, okB)
		}
		if !reflect.DeepEqual(fA, fB) {
			t.Fatalf("finding[%d] mismatch:\n  A: %+v\n  B: %+v", i, fA, fB)
		}
	}
}

// reformattedNRPlanJSON returns the same NR plan content with different
// whitespace and key ordering to test semantic equivalence.
func reformattedNRPlanJSON() string {
	return `{
	"schema_version": 1,
	"plan_id": "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
	"scope": "DATABASE",
	"tool_version": "civil-date-repair/v1",
	"audit_set_digest": "sha256:0000000000000000000000000000000000000000000000000000000000000000",
	"timezone": "America/Sao_Paulo",
	"generated_at": "2024-06-05T12:00:00Z",
	"source_logical_fingerprint": "sha256:0000000000000000000000000000000000000000000000000000000000000000",
	"classifier_version": "civil-date-classifier/v1",
	"database_schema_version": 55,
	"logical_fingerprint_version": "contabase-logical/v1",
	"findings": [{
		"variant": "TRANSACTION_NON_RECURRING",
		"source": "transaction",
		"workspace_id": "ws1",
		"source_row_id": "tx-1",
		"audited_field": "date_civil",
		"reason_code": "DATE_CIVIL_MISSING",
		"repair_outcome": "AUTO_REPAIR",
		"expected_legacy_value": 1717603200,
		"expected_civil_state": "NULL",
		"proposed_civil_value": "2024-06-05",
		"timezone": "America/Sao_Paulo",
		"classifier_version": "civil-date-classifier/v1",
		"rule_state": null,
		"series_state": null,
		"transaction_state": {
			"id": "tx-1",
			"workspace_id": "ws1",
			"status": "pending",
			"type": "EXPENSE",
			"date_unix": 1717603200,
			"due_date_unix": null,
			"recurring_rule_id": null,
			"series_id": null,
			"recurrence_sequence": null,
			"is_recurring_exception": false,
			"recurring_exception_reason": null
		}
	}]
}`
}
