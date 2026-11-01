package main

import (
	"database/sql"
	"os"
	"path/filepath"
	"testing"

	"github.com/contabase-app/contabase/internal/database"
)

func TestInvestimentosB7I2CandidateValidationRejectsExperimentalSchemaBeforeRestore(t *testing.T) {
	t.Setenv("DATA_DIR", t.TempDir())
	path := filepath.Join(t.TempDir(), "candidate.db")
	p, err := database.OpenRuntimeDBs(path)
	if err != nil {
		t.Fatal(err)
	}
	p.Close()
	if err := validateSQLiteBackupCandidate(path); err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO schema_migrations VALUES(78,'experimental',1)`); err != nil {
		t.Fatal(err)
	}
	db.Close()
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, validate := range []func(string) error{validateSQLiteBackupCandidate, validateBootstrapRestoreCandidate} {
		if err := validate(path); err == nil {
			t.Fatal("restore candidate accepted incompatible schema")
		}
	}
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(before) != string(after) {
		t.Fatal("candidate validation changed source file")
	}
}
