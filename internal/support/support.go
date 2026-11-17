// Package support resolves the public "Apoiar o Projeto" destination shown in
// the authenticated interface. It never authorizes payments or stores donor
// data: the link only navigates to the static public page operated outside the
// application.
package support

import (
	"net/url"
	"os"
	"strings"
)

// DefaultURL is the public support page published together with the release.
// The footer link is visible without any configuration.
const DefaultURL = "https://contabase.pages.dev/apoiar/"

// EnvURL is the configuration entry that replaces the default destination.
// It is optional: the interface always renders the default when it is absent.
const EnvURL = "CONTABASE_SUPPORT_URL"

// Link is the resolved public support destination.
type Link struct {
	URL     string
	Enabled bool
}

// defaultLink is the destination rendered when no valid override is configured.
// TestDefaultLinkMatchesValidator keeps it aligned with Resolve.
var defaultLink = Link{URL: DefaultURL, Enabled: true}

// FromEnv resolves the link from the environment. The default destination is
// always visible; a valid override replaces it. Absent, empty or invalid
// configuration is ignored instead of being rendered, so the footer never
// exposes a destination rejected by the validator.
func FromEnv() Link {
	if override := Resolve(os.Getenv(EnvURL)); override.Enabled {
		return override
	}
	return defaultLink
}

// Resolve validates raw and returns the canonical override destination. Only a
// public HTTPS origin pointing at the canonical /apoiar/ path is accepted.
// Loopback HTTP stays available for local validation. Invalid input returns a
// disabled Link; callers decide whether to fall back to the default.
func Resolve(raw string) Link {
	value := strings.TrimSpace(raw)
	if value == "" {
		return Link{}
	}

	parsed, err := url.Parse(value)
	if err != nil {
		return Link{}
	}
	if parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		return Link{}
	}
	if parsed.Host == "" || !acceptableScheme(parsed) {
		return Link{}
	}
	if strings.TrimSuffix(parsed.Path, "/") != "/apoiar" {
		return Link{}
	}

	parsed.Path = "/apoiar/"
	parsed.RawPath = ""
	return Link{URL: parsed.String(), Enabled: true}
}

// acceptableScheme requires HTTPS for public origins. Plain HTTP is limited to
// loopback hosts used by local development and smoke tests.
func acceptableScheme(parsed *url.URL) bool {
	switch parsed.Scheme {
	case "https":
		return true
	case "http":
		switch parsed.Hostname() {
		case "localhost", "127.0.0.1", "::1":
			return true
		}
	}
	return false
}
