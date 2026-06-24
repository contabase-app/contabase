package handlers

import (
	"database/sql"
	"strings"
)

type WorkspaceTheme struct {
	Token         string
	Nome          string
	AccentRGB     string
	AccentSoftRGB string
	AccentTextRGB string
}

var workspaceThemes = map[string]WorkspaceTheme{
	"violeta": {
		Token:         "violeta",
		Nome:          "Violeta",
		AccentRGB:     "103 68 241",
		AccentSoftRGB: "33 22 77",
		AccentTextRGB: "239 233 252",
	},
	"laranja": {
		Token:         "laranja",
		Nome:          "Laranja",
		AccentRGB:     "196 137 82",
		AccentSoftRGB: "76 49 29",
		AccentTextRGB: "246 229 206",
	},
	"amarelo": {
		Token:         "amarelo",
		Nome:          "Amarelo",
		AccentRGB:     "184 166 82",
		AccentSoftRGB: "74 65 30",
		AccentTextRGB: "246 238 203",
	},
	"azul": {
		Token:         "azul",
		Nome:          "Azul",
		AccentRGB:     "37 99 235",
		AccentSoftRGB: "30 58 138",
		AccentTextRGB: "219 234 254",
	},
	"esmeralda": {
		Token:         "esmeralda",
		Nome:          "Esmeralda",
		AccentRGB:     "5 150 105",
		AccentSoftRGB: "6 78 59",
		AccentTextRGB: "209 250 229",
	},
	"rosa": {
		Token:         "rosa",
		Nome:          "Rosa",
		AccentRGB:     "191 120 160",
		AccentSoftRGB: "74 41 61",
		AccentTextRGB: "248 225 238",
	},
}

func workspaceType(db *sql.DB, workspaceID string) string {
	var wsType string
	if err := db.QueryRow(`SELECT COALESCE(type, 'personal') FROM workspaces WHERE id = ?`, workspaceID).Scan(&wsType); err != nil {
		return "personal"
	}
	return normalizeWorkspaceTypeValue(wsType)
}

func ResolveWorkspaceTheme(rawToken, rawWorkspaceType string) WorkspaceTheme {
	workspaceType := normalizeWorkspaceTypeValue(rawWorkspaceType)
	token := strings.TrimSpace(strings.ToLower(rawToken))
	if _, ok := workspaceThemes[token]; !ok {
		token = defaultWorkspaceThemeToken(workspaceType)
	}
	return workspaceThemes[token]
}

func NormalizeWorkspaceThemeToken(rawToken, rawWorkspaceType string) string {
	return ResolveWorkspaceTheme(rawToken, rawWorkspaceType).Token
}

func normalizeWorkspaceTypeValue(raw string) string {
	if strings.TrimSpace(raw) == "business" {
		return "business"
	}
	return "personal"
}

func defaultWorkspaceThemeToken(workspaceType string) string {
	if workspaceType == "business" {
		return "azul"
	}
	return "esmeralda"
}
