package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestHandlersDoNotCallEnsureRecurringMaterializedDirectly garante que os handlers
// HTTP nunca chamem o motor pesado EnsureRecurringMaterialized diretamente. Toda
// materialização disparada por request deve passar pelo fallback leve
// (ScheduleRecurringMaterializationFallback) ou pelo job/orquestrador, que aplicam
// estado, lock e throttle.
func TestHandlersDoNotCallEnsureRecurringMaterializedDirectly(t *testing.T) {
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatalf("read cmd/server dir: %v", err)
	}
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		data, err := os.ReadFile(filepath.Join(".", name))
		if err != nil {
			t.Fatalf("read %s: %v", name, err)
		}
		content := string(data)
		if strings.Contains(content, "handlers.EnsureRecurringMaterialized(") {
			t.Fatalf("%s calls handlers.EnsureRecurringMaterialized directly; use ScheduleRecurringMaterializationFallback or the job instead", name)
		}
		if strings.Contains(content, "handlers.EnsureRecurringMaterializedTx(") {
			t.Fatalf("%s calls handlers.EnsureRecurringMaterializedTx directly; use the orchestrator instead", name)
		}
	}
}
