// Package updates reads publication metadata. It never downloads or runs updates.
package updates

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"
	"sync"
	"time"
)

// Operational endpoint shared by both channel requests. It is not browser input.
const OfficialOrigin = "https://get-contabase.pages.dev"

const (
	cacheTTL = 6 * time.Hour
	maxBody  = 32 << 10
	Unknown  = "unknown"
	Checking = "checking"
	Failed   = "failed"
	Empty    = "empty"
	Current  = "current"
	Newer    = "newer"
)

type Result struct {
	Identity
	Status    string
	Latest    string
	NotesURL  string
	CheckedAt time.Time
}

type Checker struct {
	mu         sync.Mutex
	id         Identity
	client     *http.Client
	origin     string
	now        func() time.Time
	result     Result
	inFlight   bool
	lastManual time.Time
}

func New(runtimeVersion, stateFile string) *Checker {
	return newChecker(ReadIdentity(runtimeVersion, stateFile), OfficialOrigin, http.DefaultTransport)
}

func newChecker(id Identity, origin string, transport http.RoundTripper) *Checker {
	return &Checker{
		id: id, origin: origin, now: time.Now,
		client: &http.Client{Timeout: 3 * time.Second, Transport: transport,
			// No redirects are needed for the channel manifests, even within a host.
			CheckRedirect: func(*http.Request, []*http.Request) error { return errors.New("redirect refused") }},
	}
}

// Snapshot starts at most one process-wide request and returns immediately.
// Both successes and failures have a TTL; manual refresh is shared and throttled.
func (c *Checker) Snapshot(manual bool) Result {
	if c == nil {
		return Result{Status: Unknown}
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.id.Channel == "" || versionChannel(c.id.Version) != c.id.Channel {
		return Result{Identity: c.id, Status: Unknown}
	}
	now := c.now()
	refresh := manual && (c.lastManual.IsZero() || now.Sub(c.lastManual) >= 30*time.Second)
	if !c.inFlight && (c.result.CheckedAt.IsZero() || now.Sub(c.result.CheckedAt) >= cacheTTL || refresh) {
		if manual {
			c.lastManual = now
		}
		c.inFlight = true
		go c.check()
	}
	if c.inFlight {
		return Result{Identity: c.id, Status: Checking, CheckedAt: c.result.CheckedAt}
	}
	return c.result
}

func (c *Checker) check() {
	result := c.fetch()
	c.mu.Lock()
	defer c.mu.Unlock()
	result.CheckedAt = c.now().UTC()
	c.result, c.inFlight = result, false
}

type manifest struct {
	SchemaVersion   int     `json:"schema_version"`
	Channel         string  `json:"channel"`
	DisplayName     string  `json:"display_name"`
	NextDisplayName *string `json:"next_display_name"`
	Version         *string `json:"version"`
	ReleaseType     string  `json:"release_type"`
	PublishedAt     *string `json:"published_at"`
	NotesURL        *string `json:"notes_url"`
	Message         string  `json:"message"`
}

func (c *Checker) fetch() Result {
	result := Result{Identity: c.id, Status: Failed}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.origin+"/channels/"+c.id.Channel+".json", nil)
	if err != nil {
		return result
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "ContaBase-Version-Check")
	resp, err := c.client.Do(req)
	if err != nil {
		return result
	}
	defer resp.Body.Close()
	mediaType, _, err := mime.ParseMediaType(resp.Header.Get("Content-Type"))
	if err != nil || mediaType != "application/json" || resp.StatusCode != http.StatusOK {
		return result
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxBody+1))
	if err != nil || len(body) > maxBody {
		return result
	}
	m, err := decodeManifest(body)
	if err != nil || m.SchemaVersion != 1 || m.Channel != c.id.Channel || m.ReleaseType != c.id.Channel {
		return result
	}
	if m.Version == nil {
		if m.PublishedAt == nil && m.NotesURL == nil {
			result.Status = Empty
		}
		return result
	}
	if versionChannel(*m.Version) != c.id.Channel || m.PublishedAt == nil || m.NotesURL == nil {
		return result
	}
	published, err := time.Parse(time.RFC3339, *m.PublishedAt)
	if err != nil || published.After(c.now()) || published.IsZero() {
		return result
	}
	// Exact matching rejects userinfo, lookalike hosts, ports, queries and fragments.
	if *m.NotesURL != "https://github.com/contabase-app/contabase/releases/tag/"+*m.Version {
		return result
	}
	result.Latest, result.NotesURL, result.Status = *m.Version, *m.NotesURL, Current
	if compareVersion(*m.Version, c.id.Version) > 0 {
		result.Status = Newer
	}
	return result
}

func decodeManifest(body []byte) (manifest, error) {
	// Require the v1 fields, reject duplicate keys and trailing JSON documents.
	dec := json.NewDecoder(bytes.NewReader(body))
	if token, err := dec.Token(); err != nil || token != json.Delim('{') {
		return manifest{}, errors.New("invalid manifest object")
	}
	fields := make(map[string]json.RawMessage)
	for dec.More() {
		token, err := dec.Token()
		if err != nil {
			return manifest{}, err
		}
		key, ok := token.(string)
		if _, exists := fields[key]; !ok || exists {
			return manifest{}, errors.New("duplicate manifest field")
		}
		var value json.RawMessage
		if err := dec.Decode(&value); err != nil {
			return manifest{}, err
		}
		fields[key] = value
	}
	if _, err := dec.Token(); err != nil {
		return manifest{}, err
	}
	if _, err := dec.Token(); err != io.EOF {
		return manifest{}, errors.New("trailing manifest data")
	}
	for _, key := range []string{"schema_version", "channel", "version", "release_type", "published_at"} {
		if _, ok := fields[key]; !ok {
			return manifest{}, errors.New("missing manifest field")
		}
	}
	var m manifest
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	err := decoder.Decode(&m)
	return m, err
}
