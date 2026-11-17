package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// D4-C1: the shell consolidation removed the old PWA banner markup while the
// CSP migration deleted its script. The install invite must remain a shell
// concern: one banner, one controller, a pass-through worker and no cache.
func TestPWAInstallInviteAndServiceWorkerContract(t *testing.T) {
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
	for _, want := range []string{
		`id="pwa-install-banner"`,
		`id="pwa-install-btn"`,
		`Instale o ContaBase`,
		`Acesse suas finanças diretamente pelo aplicativo.`,
		`Instalar aplicativo`,
		`assetPath "/assets/js/pwa-install.js"`,
		`defer`,
	} {
		if !strings.Contains(shell, want) {
			t.Fatalf("app shell missing %q", want)
		}
	}

	controller := read("assets/js/pwa-install.js")
	for _, want := range []string{
		"beforeinstallprompt",
		"appinstalled",
		"pwa_banner_dismissed",
		"display-mode: standalone",
		"navigator.serviceWorker.register('/sw.js'",
	} {
		if !strings.Contains(controller, want) {
			t.Fatalf("pwa-install.js missing %q", want)
		}
	}

	if strings.Contains(shell, "pwa-ios-instructions") {
		t.Fatal("PWA invite must not render fake iOS instructions")
	}

	shellCSS := read("assets/css/app-shell.css")
	for _, want := range []string{
		".cb-pwa-banner .cb-pwa-banner__install",
		"width: 100%",
		"min-height: 46px",
		"border-radius: 12px",
		"background: var(--ui-action-bg)",
		"color: var(--ui-action-fg)",
		"justify-content: center",
	} {
		if !strings.Contains(shellCSS, want) {
			t.Fatalf("PWA CTA styling missing %q", want)
		}
	}

	worker := read("assets/js/sw.js")
	for _, want := range []string{"addEventListener('install'", "addEventListener('fetch'"} {
		if !strings.Contains(worker, want) {
			t.Fatalf("sw.js missing %q", want)
		}
	}
	for _, forbidden := range []string{"caches.open", "localStorage", "sessionStorage", "indexedDB"} {
		if strings.Contains(worker, forbidden) {
			t.Fatalf("service worker must not persist data: %q", forbidden)
		}
	}

	mainSource := read("cmd/server/main.go")
	for _, want := range []string{`mux.HandleFunc("/sw.js"`, `Service-Worker-Allowed`} {
		if !strings.Contains(mainSource, want) {
			t.Fatalf("server route missing %q", want)
		}
	}
}
