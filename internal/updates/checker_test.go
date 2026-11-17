package updates

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func fixture(channel, version string) string {
	v, published, notes := "null", "null", "null"
	if version != "" {
		v = fmt.Sprintf("%q", version)
		published = `"2026-01-01T00:00:00Z"`
		notes = fmt.Sprintf("%q", "https://github.com/contabase-app/contabase/releases/tag/"+version)
	}
	return fmt.Sprintf(`{"schema_version":1,"channel":%q,"release_type":%q,"version":%s,"published_at":%s,"notes_url":%s}`, channel, channel, v, published, notes)
}

func completed(t *testing.T, c *Checker) Result {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		c.mu.Lock()
		running := c.inFlight
		c.mu.Unlock()
		if !running {
			return c.Snapshot(false)
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("check did not complete")
	return Result{}
}

func TestChannelsAndVersions(t *testing.T) {
	for _, tc := range []struct{ channel, installed, latest, status string }{
		{"beta", "v1.0.0-beta.9", "v1.0.0-beta.10", Newer},
		{"beta", "v1.0.0-beta.10", "v1.0.0-beta.9", Current},
		{"beta", "v1.0.0-beta.9", "v1.0.0-beta.9", Current},
		{"stable", "v1.0.0", "v1.1.0", Newer},
		{"stable", "v1.0.0", "v1.0.0", Current},
		{"stable", "v2.0.0", "v1.0.0", Current},
		{"stable", "v1.0.0", "", Empty},
		{"stable", "v1.0.0", "v2.0.0-beta.1", Failed},
		{"beta", "v1.0.0-beta.9", "v1.0.0", Failed},
	} {
		t.Run(tc.channel+tc.latest+tc.installed, func(t *testing.T) {
			var requests atomic.Int32
			srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests.Add(1)
				if r.URL.RequestURI() != "/channels/"+tc.channel+".json" || r.Method != http.MethodGet || r.ContentLength > 0 {
					t.Errorf("unexpected request: %s %s", r.Method, r.URL)
				}
				if r.Header.Get("Cookie") != "" || r.Header.Get("Authorization") != "" || r.Header.Get("Referer") != "" {
					t.Error("private request headers")
				}
				w.Header().Set("Content-Type", "application/json; charset=utf-8")
				fmt.Fprint(w, fixture(tc.channel, tc.latest))
			}))
			defer srv.Close()
			c := newChecker(Identity{Version: tc.installed, Channel: tc.channel}, srv.URL, srv.Client().Transport)
			if result := c.Snapshot(false); result.Status != Checking {
				t.Fatalf("initial status = %s", result.Status)
			}
			r := completed(t, c)
			if r.Status != tc.status || r.CheckedAt.IsZero() {
				t.Fatalf("result = %+v, want %s", r, tc.status)
			}
			if tc.status == Failed && (r.Latest != "" || r.NotesURL != "") {
				t.Fatal("invalid response leaked release information")
			}
			for range 30 {
				c.Snapshot(false)
			}
			if requests.Load() != 1 {
				t.Fatal("cached requests repeated")
			}
		})
	}
}

func TestManifestRejections(t *testing.T) {
	valid := fixture("beta", "v1.0.0-beta.10")
	for _, tc := range []struct {
		name, body, contentType string
		status                  int
	}{
		{"html", "<html>Unavailable</html>", "text/html", 200},
		{"html-as-json", "<html></html>", "application/json", 200},
		{"http-error", valid, "application/json", 503},
		{"missing-content-type", valid, "", 200},
		{"large", strings.Repeat(" ", maxBody) + valid, "application/json", 200},
		{"schema", strings.Replace(valid, `"schema_version":1`, `"schema_version":2`, 1), "application/json", 200},
		{"missing-version", strings.Replace(valid, `"version":"v1.0.0-beta.10",`, "", 1), "application/json", 200},
		{"duplicate", strings.Replace(valid, `{`, `{"channel":"stable",`, 1), "application/json", 200},
		{"trailing", valid + `{}`, "application/json", 200},
		{"wrong-channel", fixture("stable", "v1.0.0"), "application/json", 200},
		{"wrong-type", strings.Replace(valid, `"release_type":"beta"`, `"release_type":"stable"`, 1), "application/json", 200},
		{"unavailable", strings.Replace(valid, `"2026-01-01T00:00:00Z"`, `null`, 1), "application/json", 200},
		{"future", strings.Replace(valid, `"2026-01-01T00:00:00Z"`, `"2999-01-01T00:00:00Z"`, 1), "application/json", 200},
		{"bad-date", strings.Replace(valid, `"2026-01-01T00:00:00Z"`, `"yesterday"`, 1), "application/json", 200},
		{"missing-notes", strings.Replace(valid, `"notes_url"`, `"unsupported_url"`, 1), "application/json", 200},
		{"invalid-semver", fixture("beta", "v1.0.0-beta.01"), "application/json", 200},
		{"branch", fixture("beta", "main"), "application/json", 200},
		{"unknown-field", strings.Replace(valid, `{`, `{"available":false,`, 1), "application/json", 200},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", tc.contentType)
				w.WriteHeader(tc.status)
				fmt.Fprint(w, tc.body)
			}))
			defer srv.Close()
			c := newChecker(Identity{Version: "v1.0.0-beta.9", Channel: "beta"}, srv.URL, srv.Client().Transport)
			c.Snapshot(false)
			if got := completed(t, c); got.Status != Failed {
				t.Fatalf("result = %+v", got)
			}
		})
	}
}

func TestUntrustedReleaseURLs(t *testing.T) {
	for _, url := range []string{
		"http://github.com/contabase-app/contabase/releases/tag/v1.0.0-beta.10",
		"https://github.com.evil.test/contabase-app/contabase/releases/tag/v1.0.0-beta.10",
		"https://github.com@127.0.0.1/contabase-app/contabase/releases/tag/v1.0.0-beta.10",
		"https://github.com/contabase-app/contabase/releases/tag/v1.0.0-beta.11",
		"https://github.com/contabase-app/contabase/releases/tag/v1.0.0-beta.10?token=x",
		"javascript:alert(1)", "http://169.254.169.254/",
	} {
		t.Run(url, func(t *testing.T) {
			var doc map[string]any
			json.Unmarshal([]byte(fixture("beta", "v1.0.0-beta.10")), &doc)
			doc["notes_url"] = url
			body, _ := json.Marshal(doc)
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				w.Write(body)
			}))
			defer srv.Close()
			c := newChecker(Identity{Version: "v1.0.0-beta.9", Channel: "beta"}, srv.URL, srv.Client().Transport)
			if got := c.fetch(); got.Status != Failed || got.NotesURL != "" {
				t.Fatalf("untrusted URL accepted: %+v", got)
			}
		})
	}
}

func TestRedirectNeverFollowed(t *testing.T) {
	var leaked atomic.Int32
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { leaked.Add(1) }))
	defer target.Close()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, target.URL, 302) }))
	defer srv.Close()
	c := newChecker(Identity{Version: "v1.0.0", Channel: "stable"}, srv.URL, srv.Client().Transport)
	if c.fetch().Status != Failed || leaked.Load() != 0 {
		t.Fatal("redirect was followed")
	}
}

type transportFunc func(*http.Request) (*http.Response, error)

func (f transportFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestTimeoutNetworkFailureAndFailureCache(t *testing.T) {
	for _, timeout := range []bool{false, true} {
		var calls atomic.Int32
		c := newChecker(Identity{Version: "v1.0.0", Channel: "stable"}, OfficialOrigin, transportFunc(func(r *http.Request) (*http.Response, error) {
			calls.Add(1)
			if timeout {
				<-r.Context().Done()
				return nil, r.Context().Err()
			}
			return nil, errors.New("network unavailable")
		}))
		c.client.Timeout = 10 * time.Millisecond
		c.Snapshot(false)
		if completed(t, c).Status != Failed {
			t.Fatal("network failure not contained")
		}
		c.Snapshot(false)
		if calls.Load() != 1 {
			t.Fatal("failure not cached")
		}
	}
}

func TestSharedCacheRefreshAndTTL(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, fixture("stable", "v1.0.1"))
	}))
	defer srv.Close()
	c := newChecker(Identity{Version: "v1.0.0", Channel: "stable"}, srv.URL, srv.Client().Transport)
	clock := time.Now()
	c.now = func() time.Time { return clock }
	var wg sync.WaitGroup
	for range 50 {
		wg.Go(func() { c.Snapshot(false) })
	}
	wg.Wait()
	completed(t, c)
	if calls.Load() != 1 {
		t.Fatal("parallel initial requests not coalesced")
	}
	c.Snapshot(true)
	completed(t, c)
	for range 50 {
		wg.Go(func() { c.Snapshot(true) })
	}
	wg.Wait()
	completed(t, c)
	if calls.Load() != 2 {
		t.Fatal("manual refresh not shared/throttled")
	}
	clock = clock.Add(cacheTTL - time.Second)
	c.Snapshot(false)
	if calls.Load() != 2 {
		t.Fatal("cache expired early")
	}
	clock = clock.Add(2 * time.Second)
	c.Snapshot(false)
	completed(t, c)
	if calls.Load() != 3 {
		t.Fatal("cache not expired")
	}
}

func TestUnknownIdentityMakesNoRequest(t *testing.T) {
	for _, id := range []Identity{{Version: "dev"}, {Version: "main", Channel: "stable"}, {Version: "v1.0.0-beta.1", Channel: "stable"}, {Version: "v1.0.0"}} {
		c := newChecker(id, OfficialOrigin, transportFunc(func(*http.Request) (*http.Response, error) {
			t.Error("unknown installation made request")
			return nil, context.Canceled
		}))
		if c.Snapshot(true).Status != Unknown {
			t.Fatal("unknown identity accepted")
		}
	}
}

func TestReadIdentity(t *testing.T) {
	for _, method := range []string{"docker", "docker-image", "release", "source", "source-bootstrap"} {
		for _, channel := range []string{"beta", "stable"} {
			v := "v1.0.0"
			if channel == "beta" {
				v += "-beta.9"
			}
			body := fmt.Sprintf("CONTABASE_STATE_VERSION=1\nCONTABASE_INSTALL_METHOD=%s\nCONTABASE_CHANNEL=%s\nCONTABASE_INSTALLED_VERSION=%s\n", method, channel, v)
			for _, tc := range []struct{ name, body, version, want string }{
				{"valid", body, v, channel},
				{"divergent-runtime", body, "v2.0.0", ""},
				{"dev", body, "dev", ""},
				{"pinned", strings.Replace(body, "CONTABASE_CHANNEL="+channel, "CONTABASE_CHANNEL=pinned", 1), v, ""},
				{"duplicate", body + "CONTABASE_CHANNEL=" + channel + "\n", v, ""},
				{"oversize", body + strings.Repeat("#", 33000), v, ""},
				{"schema", strings.Replace(body, "STATE_VERSION=1", "STATE_VERSION=2", 1), v, ""},
				{"shell", strings.Replace(body, "CHANNEL="+channel, "CHANNEL=$(echo "+channel+")", 1), v, ""},
			} {
				t.Run(method+channel+tc.name, func(t *testing.T) {
					path := filepath.Join(t.TempDir(), "install-state.env")
					if err := os.WriteFile(path, []byte(tc.body), 0600); err != nil {
						t.Fatal(err)
					}
					if got := ReadIdentity(tc.version, path); got.Channel != tc.want || got.Version != tc.version {
						t.Fatalf("identity = %+v", got)
					}
				})
			}
		}
	}
	if ReadIdentity("v1.0.0", filepath.Join(t.TempDir(), "missing")).Channel != "" {
		t.Fatal("missing state accepted")
	}
}

func TestSemVerComparison(t *testing.T) {
	for _, tc := range []struct {
		a, b string
		want int
	}{
		{"v1.0.0-beta.10", "v1.0.0-beta.9", 1},
		{"v1.0.0", "v1.0.0-beta.999", 1},
		{"v1.0.0-beta.1", "v1.0.0", -1},
		{"v2.0.0-beta.1", "v1.99.99", 1},
		{"v1.10.0", "v1.9.99", 1},
		{"v1.0.0", "v1.0.0", 0},
		{"v9999999999999999999999999.0.0", "v999999999999999999999999.0.0", 1},
	} {
		if got := compareVersion(tc.a, tc.b); got != tc.want {
			t.Errorf("compare %s %s = %d", tc.a, tc.b, got)
		}
	}
}
