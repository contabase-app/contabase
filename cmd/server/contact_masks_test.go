package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// D4-C1: the contact form is rendered both in the Contatos sheet and in the
// Lançamentos inline picker after an HTMX swap. Masks must live in one global
// script and be dispatched by the single delegated input listener.
func TestContactMasksAreGlobalAndSingleSource(t *testing.T) {
	repoRoot := filepath.Clean(filepath.Join("..", ".."))
	read := func(relative string) string {
		t.Helper()
		raw, err := os.ReadFile(filepath.Join(repoRoot, filepath.FromSlash(relative)))
		if err != nil {
			t.Fatalf("read %s: %v", relative, err)
		}
		return string(raw)
	}

	shell := read("templates/components/app_shell.html")
	if !strings.Contains(shell, `assetPath "/assets/js/contact-masks.js"`) {
		t.Fatal("contact masks must load globally from the app shell")
	}

	masks := read("assets/js/contact-masks.js")
	for _, want := range []string{
		"window.maskCnpjCpfContato",
		"window.maskPhoneContato",
		"formatCpfCnpj",
		"formatBrazilianPhone",
		"htmx:afterSwap",
		"formatExisting",
	} {
		if !strings.Contains(masks, want) {
			t.Fatalf("contact-masks.js missing %q", want)
		}
	}

	modal := read("assets/js/contatos-modal.js")
	if strings.Contains(modal, "window.maskCnpjCpfContato") || strings.Contains(modal, "window.maskPhoneContato") {
		t.Fatal("contatos-modal.js must not duplicate the global masks")
	}

	events := read("assets/js/event-handlers.js")
	if strings.Count(events, "addEventListener('input'") != 1 {
		t.Fatal("contact masks must keep one delegated input listener")
	}

	for _, templatePath := range []string{
		"templates/components/contatos.html",
		"templates/components/contato_picker_create.html",
	} {
		body := read(templatePath)
		for _, want := range []string{`data-oninput="maskCnpjCpfContato"`, `data-oninput="maskPhoneContato"`} {
			if !strings.Contains(body, want) {
				t.Fatalf("%s missing %q", templatePath, want)
			}
		}
	}
}
