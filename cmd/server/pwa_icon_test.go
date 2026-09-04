package main

import (
	"encoding/json"
	"image"
	"image/color"
	_ "image/png"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type pwaManifest struct {
	BackgroundColor string            `json:"background_color"`
	ThemeColor      string            `json:"theme_color"`
	Icons           []pwaManifestIcon `json:"icons"`
}

type pwaManifestIcon struct {
	Src     string `json:"src"`
	Sizes   string `json:"sizes"`
	Type    string `json:"type"`
	Purpose string `json:"purpose"`
}

func TestPWAInstallIconsUseDarkBackgroundAndSafeMask(t *testing.T) {
	repoRoot := filepath.Clean(filepath.Join("..", ".."))
	manifestPath := filepath.Join(repoRoot, "assets", "manifest.json")
	raw, err := os.ReadFile(manifestPath)
	if err != nil {
		t.Fatalf("read PWA manifest: %v", err)
	}

	var manifest pwaManifest
	if err := json.Unmarshal(raw, &manifest); err != nil {
		t.Fatalf("decode PWA manifest: %v", err)
	}
	if manifest.BackgroundColor != "#0a0b0f" || manifest.ThemeColor != "#0a0b0f" {
		t.Fatalf("PWA chrome must stay dark; background=%q theme=%q", manifest.BackgroundColor, manifest.ThemeColor)
	}

	wanted := map[string]struct {
		size     string
		purpose  string
		maskSafe bool
	}{
		"/assets/pwa-icon-dark-192x192.png":          {size: "192x192", purpose: "any"},
		"/assets/pwa-icon-dark-512x512.png":          {size: "512x512", purpose: "any"},
		"/assets/pwa-icon-dark-maskable-512x512.png": {size: "512x512", purpose: "maskable", maskSafe: true},
	}

	if len(manifest.Icons) != len(wanted) {
		t.Fatalf("expected %d install icons, got %d", len(wanted), len(manifest.Icons))
	}

	for _, icon := range manifest.Icons {
		expected, ok := wanted[icon.Src]
		if !ok {
			t.Fatalf("unexpected install icon %q", icon.Src)
		}
		if icon.Sizes != expected.size || icon.Type != "image/png" || icon.Purpose != expected.purpose {
			t.Fatalf("invalid manifest metadata for %s: size=%q type=%q purpose=%q", icon.Src, icon.Sizes, icon.Type, icon.Purpose)
		}

		path := filepath.Join(repoRoot, filepath.FromSlash(icon.Src))
		file, err := os.Open(path)
		if err != nil {
			t.Fatalf("open install icon %s: %v", icon.Src, err)
		}
		img, _, err := image.Decode(file)
		closeErr := file.Close()
		if err != nil {
			t.Fatalf("decode install icon %s: %v", icon.Src, err)
		}
		if closeErr != nil {
			t.Fatalf("close install icon %s: %v", icon.Src, closeErr)
		}

		assertDarkIconCorners(t, icon.Src, img)
		assertBrandMarkIsVisible(t, icon.Src, img)
		if expected.maskSafe {
			assertMaskableArtworkInsideSafeCircle(t, icon.Src, img)
		}
	}
}

func TestPWAManifestLinkIsContentVersioned(t *testing.T) {
	repoRoot := filepath.Clean(filepath.Join("..", ".."))
	want := `<link rel="manifest" href="{{assetPath "/assets/manifest.json"}}">`
	for _, templatePath := range []string{
		"templates/components/app_shell.html",
		"templates/pages/ativar_conta.html",
		"templates/pages/login.html",
		"templates/pages/login_2fa.html",
		"templates/pages/setup.html",
		"templates/pages/trocar_senha.html",
	} {
		raw, err := os.ReadFile(filepath.Join(repoRoot, filepath.FromSlash(templatePath)))
		if err != nil {
			t.Fatalf("read %s: %v", templatePath, err)
		}
		if !strings.Contains(string(raw), want) {
			t.Fatalf("%s must version the PWA manifest URL so updated install icons are fetched", templatePath)
		}
	}
}

func assertDarkIconCorners(t *testing.T, name string, img image.Image) {
	t.Helper()
	bounds := img.Bounds()
	want := color.NRGBA{R: 10, G: 11, B: 15, A: 255}
	for _, point := range []image.Point{
		bounds.Min,
		{X: bounds.Max.X - 1, Y: bounds.Min.Y},
		{X: bounds.Min.X, Y: bounds.Max.Y - 1},
		{X: bounds.Max.X - 1, Y: bounds.Max.Y - 1},
	} {
		got := color.NRGBAModel.Convert(img.At(point.X, point.Y)).(color.NRGBA)
		if got != want {
			t.Fatalf("install icon %s must have dark full-bleed corners; point=%v got=%v want=%v", name, point, got, want)
		}
	}
}

func assertBrandMarkIsVisible(t *testing.T, name string, img image.Image) {
	t.Helper()
	hasWhiteC := false
	hasVioletB := false
	bounds := img.Bounds()
	for y := bounds.Min.Y; y < bounds.Max.Y && (!hasWhiteC || !hasVioletB); y++ {
		for x := bounds.Min.X; x < bounds.Max.X; x++ {
			pixel := color.NRGBAModel.Convert(img.At(x, y)).(color.NRGBA)
			if pixel.R >= 235 && pixel.G >= 235 && pixel.B >= 235 {
				hasWhiteC = true
			}
			if pixel.R >= 90 && pixel.B >= 220 && pixel.B >= pixel.G+80 {
				hasVioletB = true
			}
		}
	}
	if !hasWhiteC || !hasVioletB {
		t.Fatalf("install icon %s must keep the white C and violet B visible", name)
	}
}

func assertMaskableArtworkInsideSafeCircle(t *testing.T, name string, img image.Image) {
	t.Helper()
	bounds := img.Bounds()
	centerX := float64(bounds.Min.X+bounds.Max.X-1) / 2
	centerY := float64(bounds.Min.Y+bounds.Max.Y-1) / 2
	radius := float64(min(bounds.Dx(), bounds.Dy())) * 0.4
	background := color.NRGBA{R: 10, G: 11, B: 15, A: 255}

	for y := bounds.Min.Y; y < bounds.Max.Y; y++ {
		for x := bounds.Min.X; x < bounds.Max.X; x++ {
			if math.Hypot(float64(x)-centerX, float64(y)-centerY) <= radius {
				continue
			}
			pixel := color.NRGBAModel.Convert(img.At(x, y)).(color.NRGBA)
			if pixel != background {
				t.Fatalf("maskable artwork in %s escapes the central safe circle at (%d,%d): %v", name, x, y, pixel)
			}
		}
	}
}
