package main

import (
	"encoding/json"
	"image"
	_ "image/png"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	contabase "github.com/contabase-app/contabase"
)

// runtimeBrandingAssets são os arquivos que o produto realmente entrega ao
// navegador. Cada um precisa existir, ser PNG válido e ter a dimensão correta.
func TestBrandingAssetsExistWithExpectedGeometry(t *testing.T) {
	repoRoot := filepath.Clean(filepath.Join("..", ".."))
	cases := []struct {
		path        string
		width       int
		height      int
		transparent bool
	}{
		{"assets/branding/logo-dark.png", 0, 80, true},
		{"assets/branding/logo-light.png", 0, 80, true},
		{"assets/branding/logo-mark.png", 256, 256, true},
		{"assets/branding/og-dark.png", 1200, 630, false},
		{"assets/branding/og-light.png", 1200, 630, false},
		{"assets/favicon-16x16.png", 16, 16, true},
		{"assets/favicon-32x32.png", 32, 32, true},
		{"assets/apple-touch-icon.png", 180, 180, false},
		{"assets/pwa-icon-dark-192x192.png", 192, 192, false},
		{"assets/pwa-icon-dark-512x512.png", 512, 512, false},
		{"assets/pwa-icon-dark-maskable-512x512.png", 512, 512, false},
	}

	for _, tc := range cases {
		full := filepath.Join(repoRoot, filepath.FromSlash(tc.path))
		file, err := os.Open(full)
		if err != nil {
			t.Errorf("asset de branding ausente %s: %v", tc.path, err)
			continue
		}
		img, _, err := image.Decode(file)
		file.Close()
		if err != nil {
			t.Errorf("asset %s não é uma imagem válida: %v", tc.path, err)
			continue
		}
		if tc.width != 0 && img.Bounds().Dx() != tc.width {
			t.Errorf("%s largura = %d, want %d", tc.path, img.Bounds().Dx(), tc.width)
		}
		if tc.height != 0 && img.Bounds().Dy() != tc.height {
			t.Errorf("%s altura = %d, want %d", tc.path, img.Bounds().Dy(), tc.height)
		}

		// A logo horizontal precisa manter a proporção do lockup original
		// (símbolo + wordmark), que é bem mais larga que alta.
		if strings.HasPrefix(tc.path, "assets/branding/logo-") && strings.HasSuffix(tc.path, "dark.png") && img.Bounds().Dx() < img.Bounds().Dy()*2 {
			t.Errorf("%s não preserva a proporção horizontal do lockup", tc.path)
		}

		if tc.transparent && !pngHasTransparency(t, full) {
			t.Errorf("%s deveria ter fundo transparente", tc.path)
		}
		if !tc.transparent && pngHasTransparency(t, full) {
			t.Errorf("%s deveria ter fundo sólido", tc.path)
		}
	}
}

func pngHasTransparency(t *testing.T, path string) bool {
	t.Helper()
	file, err := os.Open(path)
	if err != nil {
		t.Fatalf("open %s: %v", path, err)
	}
	defer file.Close()
	img, _, err := image.Decode(file)
	if err != nil {
		t.Fatalf("decode %s: %v", path, err)
	}
	bounds := img.Bounds()
	corner := img.At(bounds.Min.X, bounds.Min.Y)
	_, _, _, alpha := corner.RGBA()
	return alpha == 0
}

// O manifest precisa continuar apontando para ícones que existem no disco.
func TestManifestIconsResolveToExistingFiles(t *testing.T) {
	repoRoot := filepath.Clean(filepath.Join("..", ".."))
	raw, err := os.ReadFile(filepath.Join(repoRoot, "assets", "manifest.json"))
	if err != nil {
		t.Fatalf("read manifest: %v", err)
	}
	var manifest struct {
		Icons []struct {
			Src string `json:"src"`
		} `json:"icons"`
	}
	if err := json.Unmarshal(raw, &manifest); err != nil {
		t.Fatalf("decode manifest: %v", err)
	}
	if len(manifest.Icons) == 0 {
		t.Fatal("manifest sem ícones")
	}
	for _, icon := range manifest.Icons {
		path := filepath.Join(repoRoot, filepath.FromSlash(strings.TrimPrefix(icon.Src, "/")))
		info, err := os.Stat(path)
		if err != nil {
			t.Errorf("ícone do manifest ausente: %s (%v)", icon.Src, err)
			continue
		}
		if info.Size() == 0 {
			t.Errorf("ícone do manifest vazio: %s", icon.Src)
		}
	}
}

// A logo da interface acompanha o tema pelo mecanismo já existente
// (atributo data-theme no <html>), sem lógica duplicada em JS.
func TestInterfaceLogoFollowsExistingThemeMechanism(t *testing.T) {
	shell, err := fs.ReadFile(contabase.TemplatesFS, "templates/components/app_shell.html")
	if err != nil {
		t.Fatalf("read app shell: %v", err)
	}
	markup := string(shell)
	for _, want := range []string{
		`/assets/branding/logo-dark.png`,
		`/assets/branding/logo-light.png`,
		`/assets/branding/logo-mark.png`,
		`cb-v2-brand-lockup-dark`,
		`cb-v2-brand-lockup-light`,
		`cb-v2-brand-mark`,
		`{{template "app-v2-brand-logo" .}}`,
	} {
		if !strings.Contains(markup, want) {
			t.Errorf("app shell não expõe %q para a logo por tema", want)
		}
	}

	css, err := os.ReadFile(filepath.Join("..", "..", "assets", "css", "app-shell.css"))
	if err != nil {
		t.Fatalf("read app shell css: %v", err)
	}
	stylesheet := string(css)
	for _, want := range []string{
		`html[data-theme="light"] .cb-v2-brand-lockup-dark`,
		`html[data-theme="light"] .cb-v2-brand-lockup-light`,
		`.cb-v2.is-sidebar-collapsed .cb-v2-sidebar .cb-v2-brand-mark`,
	} {
		if !strings.Contains(stylesheet, want) {
			t.Errorf("app-shell.css não implementa %q", want)
		}
	}
}

// O tema claro é aplicado por theme-init.js via data-theme; a troca de logo
// depende desse atributo, então ele precisa continuar sendo o mecanismo.
func TestThemeInitKeepsDataThemeContract(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "..", "assets", "js", "theme-init.js"))
	if err != nil {
		t.Fatalf("read theme-init: %v", err)
	}
	body := string(raw)
	for _, want := range []string{"data-theme", "classList.toggle('dark'", "localStorage"} {
		if !strings.Contains(body, want) {
			t.Errorf("theme-init.js perdeu o contrato de tema: falta %q", want)
		}
	}
}

// A identidade antiga não pode reaparecer em nenhuma superfície de runtime.
func TestNoLegacyBrandReferencesRemain(t *testing.T) {
	repoRoot := filepath.Clean(filepath.Join("..", ".."))
	forbidden := []string{
		"contabase-monogram",
		"contabase-logo.svg",
		"logo-horizontal.png",
		"logo-horizontal-dark.png",
		"pwa-logo.png",
		"pwa-logo-source.png",
		"android-chrome-",
		"assets/maskable-512x512.png",
		"assets/favicon.png",
		"new-temp",
	}

	// Apenas superfícies de runtime: dados, backups e binários ficam de fora.
	roots := []string{"templates", "assets/js", "assets/css", "internal", "cmd", "scripts"}
	sourceExtensions := map[string]bool{
		".go": true, ".html": true, ".js": true, ".css": true,
		".json": true, ".sh": true, ".yml": true, ".yaml": true,
	}
	skipDirs := map[string]bool{
		"data": true, "backups": true, "testdata": true, "pre-migration": true,
		"node_modules": true, "institutions": true, "fonts": true,
	}
	var offenders []string
	for _, root := range roots {
		base := filepath.Join(repoRoot, root)
		err := filepath.WalkDir(base, func(path string, entry os.DirEntry, err error) error {
			if err != nil {
				return nil
			}
			if entry.IsDir() {
				if skipDirs[entry.Name()] {
					return filepath.SkipDir
				}
				return nil
			}
			name := entry.Name()
			// O CSS compilado é regenerado; os bundles minificados são vendorizados.
			if name == "style.css" || name == "lucide.min.js" || name == "htmx.min.js" {
				return nil
			}
			if strings.HasSuffix(name, "_test.go") {
				return nil
			}
			if !sourceExtensions[strings.ToLower(filepath.Ext(name))] {
				return nil
			}
			content, readErr := os.ReadFile(path)
			if readErr != nil {
				return nil
			}
			body := string(content)
			for _, needle := range forbidden {
				if strings.Contains(body, needle) {
					relative, _ := filepath.Rel(repoRoot, path)
					offenders = append(offenders, relative+" -> "+needle)
				}
			}
			return nil
		})
		if err != nil {
			t.Fatalf("walk %s: %v", root, err)
		}
	}
	if len(offenders) > 0 {
		t.Fatalf("referências à identidade antiga remanescentes:\n%s", strings.Join(offenders, "\n"))
	}
}

// Os arquivos mortos da identidade anterior não podem continuar no repositório.
func TestLegacyBrandFilesAreRemoved(t *testing.T) {
	repoRoot := filepath.Clean(filepath.Join("..", ".."))
	for _, dead := range []string{
		"assets/contabase-monogram.svg",
		"assets/contabase-logo.svg",
		"assets/logo-horizontal.png",
		"assets/logo-horizontal-dark.png",
		"assets/pwa-logo.png",
		"assets/pwa-logo-source.png",
		"assets/favicon.png",
		"assets/android-chrome-192x192.png",
		"assets/android-chrome-512x512.png",
		"assets/maskable-512x512.png",
		"assets/new-temp",
	} {
		if _, err := os.Stat(filepath.Join(repoRoot, filepath.FromSlash(dead))); err == nil {
			t.Errorf("asset órfão da identidade antiga ainda presente: %s", dead)
		}
	}
}
