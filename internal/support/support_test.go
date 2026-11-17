package support

import "testing"

func TestDefaultLinkMatchesValidator(t *testing.T) {
	if got := Resolve(DefaultURL); got != defaultLink {
		t.Fatalf("default destination must satisfy the validator, got %+v", got)
	}
	if !defaultLink.Enabled || defaultLink.URL != "https://contabase.pages.dev/apoiar/" {
		t.Fatalf("unexpected default link %+v", defaultLink)
	}
}

func TestFromEnvRendersDefaultWithoutConfiguration(t *testing.T) {
	for _, raw := range []string{"", "   "} {
		t.Setenv(EnvURL, raw)
		link := FromEnv()
		if !link.Enabled || link.URL != DefaultURL {
			t.Fatalf("%q must fall back to the default link, got %+v", raw, link)
		}
	}
}

func TestFromEnvUsesValidOverride(t *testing.T) {
	t.Setenv(EnvURL, "https://apoio.example.org/apoiar/")
	link := FromEnv()
	if !link.Enabled {
		t.Fatal("expected valid override to be enabled")
	}
	if link.URL != "https://apoio.example.org/apoiar/" {
		t.Fatalf("unexpected URL %q", link.URL)
	}
}

func TestFromEnvFallsBackToDefaultOnInvalidOverride(t *testing.T) {
	for _, raw := range []string{
		"contabase.pages.dev/apoiar/",
		"javascript:alert(1)",
		"ftp://contabase.pages.dev/apoiar/",
		"http://contabase.pages.dev/apoiar/",
		"https://user:secret@contabase.pages.dev/apoiar/",
		"https://contabase.pages.dev/setup/",
	} {
		t.Setenv(EnvURL, raw)
		link := FromEnv()
		if link != defaultLink {
			t.Errorf("%q must be ignored in favor of the default, got %+v", raw, link)
		}
	}
}

func TestResolveNormalizesTrailingSlash(t *testing.T) {
	link := Resolve("https://contabase.pages.dev/apoiar")
	if !link.Enabled || link.URL != "https://contabase.pages.dev/apoiar/" {
		t.Fatalf("expected canonical path, got %+v", link)
	}
}

func TestResolveAcceptsLoopbackHTTPForLocalValidation(t *testing.T) {
	for _, raw := range []string{
		"http://localhost:8080/apoiar/",
		"http://127.0.0.1:8080/apoiar/",
	} {
		if link := Resolve(raw); !link.Enabled {
			t.Errorf("%q must be accepted for local validation", raw)
		}
	}
}

func TestResolveRejectsUnsafeOrAmbiguousDestinations(t *testing.T) {
	for _, raw := range []string{
		"",
		"   ",
		"contabase.pages.dev/apoiar/",
		"/apoiar/",
		"//contabase.pages.dev/apoiar/",
		"http://contabase.pages.dev/apoiar/",
		"ftp://contabase.pages.dev/apoiar/",
		"javascript:alert(1)",
		"https://user:secret@contabase.pages.dev/apoiar/",
		"https://contabase.pages.dev/apoiar/?utm_source=app",
		"https://contabase.pages.dev/apoiar/#top",
		"https://contabase.pages.dev/",
		"https://contabase.pages.dev/setup/",
		"https://contabase.pages.dev/apoiar/extra",
	} {
		if link := Resolve(raw); link.Enabled || link.URL != "" {
			t.Errorf("%q must stay disabled, got %+v", raw, link)
		}
	}
}
