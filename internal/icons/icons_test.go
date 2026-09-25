package icons_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/contabase-app/contabase/internal/icons"
)

// lucideBundlePath aponta para o bundle self-hosted que o frontend carrega.
// É a fonte da verdade de quais chaves realmente renderizam um ícone.
func lucideBundlePath(t *testing.T) string {
	t.Helper()
	return filepath.Join("..", "..", "assets", "js", "lucide.min.js")
}

func pascalCase(key string) string {
	parts := strings.Split(key, "-")
	for i, part := range parts {
		if part == "" {
			continue
		}
		parts[i] = strings.ToUpper(part[:1]) + part[1:]
	}
	return strings.Join(parts, "")
}

// O catálogo é fechado: toda chave precisa existir no bundle versionado.
// Sem isso, uma chave errada viraria um ícone invisível em produção.
func TestIconCatalogKeysExistInLucideBundle(t *testing.T) {
	raw, err := os.ReadFile(lucideBundlePath(t))
	if err != nil {
		t.Fatalf("read lucide bundle: %v", err)
	}
	bundle := string(raw)

	keys := icons.SortedKeys()
	if len(keys) == 0 {
		t.Fatal("catálogo de ícones vazio")
	}
	for _, key := range keys {
		if !strings.Contains(bundle, pascalCase(key)) {
			t.Errorf("chave %q não existe no bundle Lucide versionado", key)
		}
	}
}

func TestIconCatalogIsInternallyConsistent(t *testing.T) {
	seen := map[string]bool{}
	for _, option := range icons.All() {
		if seen[option.Key] {
			t.Errorf("chave duplicada no catálogo: %q", option.Key)
		}
		seen[option.Key] = true
		if strings.TrimSpace(option.Label) == "" {
			t.Errorf("chave %q sem nome amigável", option.Key)
		}
		if strings.TrimSpace(option.Group) == "" {
			t.Errorf("chave %q sem grupo", option.Key)
		}
		if option.Key != strings.ToLower(option.Key) || strings.Contains(option.Key, " ") {
			t.Errorf("chave %q deve ser estável, minúscula e sem espaços", option.Key)
		}
	}

	grouped := 0
	for _, group := range icons.Grouped() {
		if len(group.Options) == 0 {
			t.Errorf("grupo %q sem opções", group.Name)
		}
		grouped += len(group.Options)
	}
	if grouped != len(icons.All()) {
		t.Fatalf("agrupamento perdeu ícones: %d de %d", grouped, len(icons.All()))
	}
}

func TestCanonicalKeyNormalizesAndRejectsUnknownInput(t *testing.T) {
	cases := []struct {
		input string
		want  string
	}{
		{"utensils", "utensils"},
		{"  Utensils  ", "utensils"},
		{"HOME", "house"},
		{"home", "house"},
		{"gamepad-2", "gamepad"},
		{"arrows-left-right", "arrow-left-right"},
		{"house-key", "house"},
		{"", ""},
		{"   ", ""},
		{"<script>alert(1)</script>", ""},
		{"../../etc/passwd", ""},
		{"dois termos", ""},
		{"nao-existe-no-catalogo", ""},
	}
	for _, tc := range cases {
		if got := icons.CanonicalKey(tc.input); got != tc.want {
			t.Errorf("CanonicalKey(%q) = %q, want %q", tc.input, got, tc.want)
		}
	}
}

// Um valor desconhecido nunca pode virar caminho, HTML ou SVG: só chave válida
// ou o fallback neutro.
func TestResolveFallsBackToNeutralIcon(t *testing.T) {
	if got := icons.Resolve(""); got != icons.DefaultKey {
		t.Errorf("Resolve(vazio) = %q, want %q", got, icons.DefaultKey)
	}
	if got := icons.Resolve("<svg/>", "nao-existe"); got != icons.DefaultKey {
		t.Errorf("Resolve(inválido) = %q, want %q", got, icons.DefaultKey)
	}
	if got := icons.Resolve("", "car", "house"); got != "car" {
		t.Errorf("Resolve deve respeitar a ordem de precedência, got %q", got)
	}
	if got := icons.Label("nao-existe"); got != "Geral" {
		t.Errorf("Label(inválido) = %q, want rótulo do fallback", got)
	}
}

func TestIsValidMatchesCatalog(t *testing.T) {
	for _, option := range icons.All() {
		if !icons.IsValid(option.Key) {
			t.Errorf("IsValid(%q) = false para chave do catálogo", option.Key)
		}
	}
	if icons.IsValid("inexistente") {
		t.Error("IsValid aceitou chave fora do catálogo")
	}
}

func TestNewPickerResolvesPreviewAndInheritance(t *testing.T) {
	// Sem escolha: o preview mostra o que seria herdado.
	picker := icons.NewPicker("catIcon", "icon", "", "utensils")
	if picker.Value != "" {
		t.Errorf("Value = %q, want vazio (sem escolha explícita)", picker.Value)
	}
	if picker.Preview != "utensils" {
		t.Errorf("Preview = %q, want utensils", picker.Preview)
	}
	if picker.HasExplicitChoice() {
		t.Error("HasExplicitChoice deve ser falso sem escolha")
	}
	if len(picker.Groups) == 0 {
		t.Error("seletor deve expor o catálogo agrupado")
	}

	// Com escolha: a chave explícita vence a herança.
	picker = icons.NewPicker("catIcon", "icon", "car", "utensils")
	if picker.Value != "car" || picker.Preview != "car" || !picker.HasExplicitChoice() {
		t.Errorf("escolha explícita não prevaleceu: %+v", picker)
	}

	// Valor inválido é descartado em favor da herança.
	picker = icons.NewPicker("catIcon", "icon", "<svg/>", "house")
	if picker.Value != "" || picker.Preview != "house" {
		t.Errorf("valor inválido deveria cair na herança: %+v", picker)
	}
}
