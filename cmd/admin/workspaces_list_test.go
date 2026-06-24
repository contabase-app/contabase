package main

import (
	"bytes"
	"database/sql"
	"fmt"
	"strings"
	"testing"
	"text/tabwriter"
	"time"
)

func runWorkspacesListTestable(db *sql.DB) (string, error) {
	rows, err := db.Query(`SELECT id, name, type, COALESCE(created_at, 0) FROM workspaces ORDER BY name ASC`)
	if err != nil {
		return "", fmt.Errorf("Erro ao listar workspaces: %v", err)
	}
	defer rows.Close()

	type workspaceRow struct {
		ID        string
		Name      string
		Type      string
		CreatedAt int64
	}

	var workspaces []workspaceRow
	for rows.Next() {
		var ws workspaceRow
		if err := rows.Scan(&ws.ID, &ws.Name, &ws.Type, &ws.CreatedAt); err != nil {
			return "", fmt.Errorf("Erro ao ler workspace: %v", err)
		}
		workspaces = append(workspaces, ws)
	}
	if err := rows.Err(); err != nil {
		return "", fmt.Errorf("Erro ao percorrer workspaces: %v", err)
	}

	if len(workspaces) == 0 {
		return "Nenhum workspace encontrado.\n", nil
	}

	var buf bytes.Buffer
	tw := tabwriter.NewWriter(&buf, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "ID\tTYPE\tNAME\tCREATED_AT")
	for _, ws := range workspaces {
		createdAt := "-"
		if ws.CreatedAt > 0 {
			createdAt = time.Unix(ws.CreatedAt, 0).Format("2006-01-02 15:04")
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\n", ws.ID, ws.Type, ws.Name, createdAt)
	}
	if err := tw.Flush(); err != nil {
		return "", fmt.Errorf("Erro ao escrever saída: %v", err)
	}
	return buf.String(), nil
}

func TestWorkspacesListEmptyDB(t *testing.T) {
	db := openMemoryDB(t)
	defer db.Close()

	output, err := runWorkspacesListTestable(db)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(output, "Nenhum workspace encontrado.") {
		t.Fatalf("expected empty message, got: %s", output)
	}
}

func TestWorkspacesListSinglePersonal(t *testing.T) {
	db := openMemoryDB(t)
	defer db.Close()

	wsID := "e44476a5-970f-4f78-94e9-b9d856517d8e"
	seedWorkspace(t, db, wsID, "personal")

	output, err := runWorkspacesListTestable(db)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !strings.Contains(output, wsID) {
		t.Fatalf("output missing workspace ID: %s", output)
	}
	if !strings.Contains(output, "personal") {
		t.Fatalf("output missing type personal: %s", output)
	}
	if !strings.Contains(output, wsID) {
		t.Fatalf("output missing workspace name: %s", output)
	}
}

func TestWorkspacesListMultipleTypes(t *testing.T) {
	db := openMemoryDB(t)
	defer db.Close()

	personalID := "e44476a5-970f-4f78-94e9-b9d856517d8e"
	businessID := "1f8c23d5-af38-424c-8504-a688b477e138"
	seedWorkspace(t, db, personalID, "personal")
	seedWorkspace(t, db, businessID, "business")

	output, err := runWorkspacesListTestable(db)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !strings.Contains(output, personalID) {
		t.Fatalf("output missing personal workspace ID: %s", output)
	}
	if !strings.Contains(output, businessID) {
		t.Fatalf("output missing business workspace ID: %s", output)
	}
	if !strings.Contains(output, "personal") {
		t.Fatalf("output missing type personal: %s", output)
	}
	if !strings.Contains(output, "business") {
		t.Fatalf("output missing type business: %s", output)
	}
}

func TestWorkspacesListOutputHasHeader(t *testing.T) {
	db := openMemoryDB(t)
	defer db.Close()

	wsID := "ws-header-test"
	seedWorkspace(t, db, wsID, "personal")

	output, err := runWorkspacesListTestable(db)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !strings.Contains(output, "ID") {
		t.Fatalf("output missing ID header: %s", output)
	}
	if !strings.Contains(output, "TYPE") {
		t.Fatalf("output missing TYPE header: %s", output)
	}
	if !strings.Contains(output, "NAME") {
		t.Fatalf("output missing NAME header: %s", output)
	}
}

func TestWorkspacesListCreatedAtColumn(t *testing.T) {
	db := openMemoryDB(t)
	defer db.Close()

	wsID := "ws-created-at"
	seedWorkspace(t, db, wsID, "personal")

	output, err := runWorkspacesListTestable(db)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !strings.Contains(output, "CREATED_AT") {
		t.Fatalf("output missing CREATED_AT header: %s", output)
	}
}

func TestWorkspacesListOrderByName(t *testing.T) {
	db := openMemoryDB(t)
	defer db.Close()

	seedWorkspace(t, db, "zebra-ws", "personal")
	seedWorkspace(t, db, "alpha-ws", "business")
	seedWorkspace(t, db, "mid-ws", "personal")

	output, err := runWorkspacesListTestable(db)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	alphaPos := strings.Index(output, "alpha-ws")
	midPos := strings.Index(output, "mid-ws")
	zebraPos := strings.Index(output, "zebra-ws")

	if alphaPos < 0 || midPos < 0 || zebraPos < 0 {
		t.Fatalf("missing workspace names in output: %s", output)
	}
	if !(alphaPos < midPos && midPos < zebraPos) {
		t.Fatalf("workspaces not sorted by name ASC: alpha=%d mid=%d zebra=%d\n%s", alphaPos, midPos, zebraPos, output)
	}
}
