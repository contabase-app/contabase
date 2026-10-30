package main

import (
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"testing"
)

func TestReportStrokePaletteUsesExternalStylesheet(t *testing.T) {
	_, currentFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("could not determine repository root")
	}
	repoRoot := filepath.Clean(filepath.Join(filepath.Dir(currentFile), "..", ".."))

	reportTemplate, err := os.ReadFile(filepath.Join(repoRoot, "templates", "pages", "relatorios.html"))
	if err != nil {
		t.Fatalf("read report template: %v", err)
	}
	if strings.Contains(string(reportTemplate), "colorStyle") || strings.Contains(string(reportTemplate), "style=") {
		t.Fatal("report template must use inert data-report-stroke attributes instead of inline style")
	}
	if got := strings.Count(string(reportTemplate), `data-report-stroke="{{.Stroke}}"`); got != 12 {
		t.Fatalf("report template has %d report stroke consumers, want 12", got)
	}

	handlerSource, err := os.ReadFile(filepath.Join(repoRoot, "internal", "handlers", "relatorios.go"))
	if err != nil {
		t.Fatalf("read report handler: %v", err)
	}
	paletteBlocks := regexp.MustCompile(`(?s)(?:strokeColors|colors)\s*:=\s*\[\]string\{(.*?)\}`).FindAllStringSubmatch(string(handlerSource), -1)
	colorLiteral := regexp.MustCompile(`"(#[0-9A-Fa-f]{6}|rgb\([0-9 ,]+\))"`)
	colors := make(map[string]struct{})
	for _, block := range paletteBlocks {
		for _, match := range colorLiteral.FindAllStringSubmatch(block[1], -1) {
			colors[match[1]] = struct{}{}
		}
	}
	if len(colors) != 17 {
		t.Fatalf("report handlers expose %d stroke colors, want 17", len(colors))
	}

	stylesheet, err := os.ReadFile(filepath.Join(repoRoot, "assets", "css", "relatorios.css"))
	if err != nil {
		t.Fatalf("read reports stylesheet: %v", err)
	}
	css := string(stylesheet)
	for color := range colors {
		if !strings.Contains(css, `data-report-stroke="`+color+`"`) {
			t.Errorf("stylesheet does not map supported report stroke %q", color)
		}
	}
	if !strings.Contains(css, ".cb-report-stroke-dot") {
		t.Fatal("stylesheet must provide the report stroke dot consumer")
	}
}
