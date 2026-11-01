//go:build contabase_legacy_fixtures

package database

import (
	"path/filepath"
	"testing"
)

func TestLegacyFixtureRefusesUnregisteredAndCurrentSchemas(t *testing.T) {
	t.Setenv("DATA_DIR", t.TempDir())
	path, _ := cancellation79Disk77(t)
	if p, err := OpenLegacyInvestmentFixtureDBs(path); err == nil {
		p.Close()
		t.Fatal("unregistered fixture accepted")
	}
	if err := RegisterLegacyInvestmentFixture(path); err != nil {
		t.Fatal(err)
	}
	p, err := OpenLegacyInvestmentFixtureDBs(path)
	if err != nil {
		t.Fatal(err)
	}
	p.Close()
	current := filepath.Join(t.TempDir(), "current.db")
	p, err = OpenRuntimeDBs(current)
	if err != nil {
		t.Fatal(err)
	}
	p.Close()
	if err := RegisterLegacyInvestmentFixture(current); err == nil {
		t.Fatal("schema79 accepted as legacy")
	}
	legacy := filepath.Join(t.TempDir(), "candidate78.db")
	fixture := admissionCandidateDB(t)
	mustExecOK(t, fixture, `VACUUM INTO ?`, legacy)
	if err := RegisterLegacyInvestmentFixture(legacy); err != nil {
		t.Fatal(err)
	}
	if p, err := OpenRuntimeDBs(legacy); err == nil {
		p.Close()
		t.Fatal("normal runtime accepted registered experimental fixture")
	}
	p, err = OpenLegacyInvestmentFixtureDBs(legacy)
	if err != nil {
		t.Fatal(err)
	}
	p.Close()
}
