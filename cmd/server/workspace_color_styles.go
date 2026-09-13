package main

import (
	"database/sql"
	"fmt"
	"log/slog"
	"net/http"
	"regexp"
	"sort"
	"strings"

	"github.com/contabase-app/contabase/internal/handlers"
)

var workspaceColorPattern = regexp.MustCompile(`^#[0-9A-Fa-f]{6}$`)

func normalizeWorkspaceColor(raw string) (string, bool) {
	color := strings.ToUpper(strings.TrimSpace(raw))
	if !workspaceColorPattern.MatchString(color) {
		return "", false
	}
	return color, true
}

func queryWorkspaceColors(db *sql.DB, workspaceID string) ([]string, error) {
	colors := map[string]struct{}{
		"#6B7280": {},
		"#6366F1": {},
	}
	for _, raw := range handlers.AccountProviderColors() {
		if color, ok := normalizeWorkspaceColor(raw); ok {
			colors[color] = struct{}{}
		}
	}

	rows, err := db.Query(`
		SELECT color FROM accounts WHERE workspace_id = ?
		UNION ALL
		SELECT color FROM categories WHERE workspace_id = ?
	`, workspaceID, workspaceID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var raw sql.NullString
		if err := rows.Scan(&raw); err != nil {
			return nil, err
		}
		if color, ok := normalizeWorkspaceColor(raw.String); ok {
			colors[color] = struct{}{}
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	out := make([]string, 0, len(colors))
	for color := range colors {
		out = append(out, color)
	}
	sort.Strings(out)
	return out, nil
}

func serveWorkspaceColorStyles(w http.ResponseWriter, r *http.Request, db *sql.DB, workspaceID string) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		w.Header().Set("Allow", "GET, HEAD")
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	colors, err := queryWorkspaceColors(db, workspaceID)
	if err != nil {
		slog.Error("workspace color stylesheet query failed", "workspace_id", workspaceID, "error", err)
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "text/css; charset=utf-8")
	w.Header().Set("Cache-Control", "private, no-store, max-age=0")
	w.Header().Set("Vary", "Cookie")
	if r.Method == http.MethodHead {
		return
	}
	_, _ = fmt.Fprintln(w, "/* ContaBase workspace color palette; generated from validated hexadecimal values. */")
	for _, color := range colors {
		_, _ = fmt.Fprintf(w, "[data-cb-color=\"%s\" i]{--cb-runtime-color-value:%s}\n", color, color)
	}
}
