package main

import (
	"bytes"
	"os"
	"strings"
	"testing"
)

func TestRunCivilDateRepairRootHelp(t *testing.T) {
	oldArgs := os.Args
	defer func() { os.Args = oldArgs }()

	os.Args = []string{"admin", "civil-date-repair", "--help"}
	exitCode := runCivilDateRepair()
	if exitCode != 0 {
		t.Fatalf("exit = %d, want 0", exitCode)
	}
}

func TestRunCivilDateRepairRootHelpShort(t *testing.T) {
	oldArgs := os.Args
	defer func() { os.Args = oldArgs }()

	os.Args = []string{"admin", "civil-date-repair", "-h"}
	exitCode := runCivilDateRepair()
	if exitCode != 0 {
		t.Fatalf("exit = %d, want 0", exitCode)
	}
}

func TestRunCivilDateRepairRootFlagBeforeAuditRejected(t *testing.T) {
	oldArgs := os.Args
	defer func() { os.Args = oldArgs }()

	os.Args = []string{"admin", "civil-date-repair", "--database-url", "file:x.db", "audit"}
	exitCode := runCivilDateRepair()
	if exitCode != 1 {
		t.Fatalf("exit = %d, want 1", exitCode)
	}
}

func TestRunCivilDateRepairAuditHelpPreserved(t *testing.T) {
	oldArgs := os.Args
	defer func() { os.Args = oldArgs }()

	os.Args = []string{"admin", "civil-date-repair", "audit", "--help"}
	exitCode := runCivilDateRepair()
	if exitCode != 0 {
		t.Fatalf("exit = %d, want 0", exitCode)
	}
}

func TestRunCivilDateRepairApplyNotImplemented(t *testing.T) {
	oldArgs := os.Args
	defer func() { os.Args = oldArgs }()

	os.Args = []string{"admin", "civil-date-repair", "apply"}
	exitCode := runCivilDateRepair()
	if exitCode != 1 {
		t.Fatalf("exit = %d, want 1", exitCode)
	}
}

func TestRunCivilDateRepairUnknownSubcommand(t *testing.T) {
	oldArgs := os.Args
	defer func() { os.Args = oldArgs }()

	os.Args = []string{"admin", "civil-date-repair", "unknown-cmd"}
	exitCode := runCivilDateRepair()
	if exitCode != 1 {
		t.Fatalf("exit = %d, want 1", exitCode)
	}
}

func TestPrintCivilDateRepairHelpOutput(t *testing.T) {
	old := os.Stdout
	r, w, _ := os.Pipe()
	os.Stdout = w
	printCivilDateRepairHelp()
	w.Close()
	os.Stdout = old

	var buf bytes.Buffer
	buf.ReadFrom(r)
	output := buf.String()

	if !strings.Contains(output, "audit") {
		t.Fatal("help missing 'audit'")
	}
	if !strings.Contains(output, "apply") {
		t.Fatal("help missing 'apply'")
	}
	if !strings.Contains(output, "nao disponivel") {
		t.Fatal("help should indicate apply not available")
	}
	if !strings.Contains(output, "auto-reparo") {
		t.Fatal("help missing exit code docs")
	}
}
