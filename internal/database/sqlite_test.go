package database

import (
	"strings"
	"testing"
)

func TestNormalizeSQLiteDSN_MemoryNoChange(t *testing.T) {
	got := normalizeSQLiteDSN(":memory:")
	if got != ":memory:" {
		t.Errorf("expected ':memory:', got %q", got)
	}
}

func TestNormalizeSQLiteDSN_MemorySuffixNoChange(t *testing.T) {
	got := normalizeSQLiteDSN("file::memory:")
	if got != "file::memory:" {
		t.Errorf("expected 'file::memory:', got %q", got)
	}
}

func TestNormalizeSQLiteDSN_PlainPathAddsAllPragmas(t *testing.T) {
	got := normalizeSQLiteDSN("file:data/contabase.db")
	if !strings.HasPrefix(got, "file:data/contabase.db?") {
		t.Errorf("expected query params after '?', got %q", got)
	}
	if !strings.Contains(got, "_pragma=journal_mode(WAL)") {
		t.Errorf("missing _pragma=journal_mode(WAL) in %q", got)
	}
	if !strings.Contains(got, "_pragma=foreign_keys(1)") {
		t.Errorf("missing _pragma=foreign_keys(1) in %q", got)
	}
	if !strings.Contains(got, "_pragma=synchronous(NORMAL)") {
		t.Errorf("missing _pragma=synchronous(NORMAL) in %q", got)
	}
	if !strings.Contains(got, "_pragma=busy_timeout(5000)") {
		t.Errorf("missing _pragma=busy_timeout(5000) in %q", got)
	}
}

func TestNormalizeSQLiteDSN_AbsolutePathAddsAllPragmas(t *testing.T) {
	got := normalizeSQLiteDSN("file:/app/data/contabase.db")
	if !strings.Contains(got, "_pragma=journal_mode(WAL)") {
		t.Errorf("missing _pragma=journal_mode(WAL) in %q", got)
	}
	if !strings.Contains(got, "_pragma=foreign_keys(1)") {
		t.Errorf("missing _pragma=foreign_keys(1) in %q", got)
	}
}

func TestNormalizeSQLiteDSN_ExistingQueryParamsUsesAmpersand(t *testing.T) {
	got := normalizeSQLiteDSN("file:/app/data/contabase.db?mode=rwc")
	if !strings.Contains(got, "&_pragma=journal_mode(WAL)") {
		t.Errorf("expected & separator before pragmas in %q", got)
	}
	if !strings.HasPrefix(got, "file:/app/data/contabase.db?mode=rwc&_pragma=") {
		t.Errorf("expected original param preserved in %q", got)
	}
}

func TestNormalizeSQLiteDSN_ExistingPragmaNoDuplicate(t *testing.T) {
	got := normalizeSQLiteDSN("file:data/contabase.db?_pragma=journal_mode(WAL)")
	count := strings.Count(got, "_pragma=journal_mode(WAL)")
	if count != 1 {
		t.Errorf("expected exactly 1 journal_mode pragma, got %d in %q", count, got)
	}
	if !strings.Contains(got, "_pragma=foreign_keys(1)") {
		t.Errorf("missing _pragma=foreign_keys(1) in %q", got)
	}
}

func TestNormalizeSQLiteDSN_AllPragmasAlreadyPresentNoChange(t *testing.T) {
	input := "file:data/contabase.db?_pragma=journal_mode(WAL)&_pragma=foreign_keys(1)&_pragma=synchronous(NORMAL)&_pragma=busy_timeout(5000)"
	got := normalizeSQLiteDSN(input)
	if got != input {
		t.Errorf("expected no change, got %q", got)
	}
}

func TestNormalizeSQLiteDSN_MixedExistingAndMissingPragmas(t *testing.T) {
	input := "file:data/contabase.db?_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)"
	got := normalizeSQLiteDSN(input)
	if strings.Count(got, "_pragma=journal_mode(WAL)") != 1 {
		t.Errorf("duplicate journal_mode pragma in %q", got)
	}
	if strings.Count(got, "_pragma=busy_timeout(5000)") != 1 {
		t.Errorf("duplicate busy_timeout pragma in %q", got)
	}
	if !strings.Contains(got, "_pragma=foreign_keys(1)") {
		t.Errorf("missing foreign_keys pragma in %q", got)
	}
	if !strings.Contains(got, "_pragma=synchronous(NORMAL)") {
		t.Errorf("missing synchronous pragma in %q", got)
	}
}

func TestNormalizeSQLiteDSN_CaseInsensitiveExistingPragma(t *testing.T) {
	got := normalizeSQLiteDSN("file:data/contabase.db?_pragma=JOURNAL_MODE(wal)")
	if strings.Count(strings.ToLower(got), "_pragma=journal_mode(") != 1 {
		t.Errorf("expected exactly 1 journal_mode pragma (case insensitive), got %q", got)
	}
}

func TestNormalizeSQLiteDSN_ReadOnlyModePreserved(t *testing.T) {
	got := normalizeSQLiteDSN("file:/app/data/contabase.db?mode=ro")
	if !strings.Contains(got, "mode=ro") {
		t.Errorf("mode=ro should be preserved in %q", got)
	}
	if !strings.Contains(got, "_pragma=journal_mode(WAL)") {
		t.Errorf("missing journal_mode pragma in %q", got)
	}
}