package github

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	defaultBaseURL = "https://api.github.com"
	userAgent      = "gitscout"

	// GitHub allows 30 authenticated search requests per minute. Discovery
	// issues dozens of them back to back, so they are paced rather than left
	// to trip the limiter and back off.
	searchInterval = 2100 * time.Millisecond

	maxAttempts = 4

	// A rate-limit reset further out than this is treated as fatal rather
	// than slept through; a run should never silently stall for an hour.
	maxRateLimitWait = 5 * time.Minute
)

// ErrNotFound is returned for a 404. Several endpoints 404 for reasons that are
// not errors in context — a repo with no CONTRIBUTING.md, for instance — so
// callers need to distinguish it from a genuine failure.
var ErrNotFound = errors.New("github: not found")

// Budget is the remaining request allowance for one rate-limit resource, as
// reported by the last response's headers.
type Budget struct {
	Remaining int
	Limit     int
	Reset     time.Time
}

// Client is a minimal GitHub REST client with rate-limit handling, retries and
// search pacing. The zero value is not usable; call NewClient.
type Client struct {
	http    *http.Client
	token   string
	baseURL string

	// Injected for tests.
	now   func() time.Time
	sleep func(time.Duration)

	mu         sync.Mutex
	budgets    map[string]Budget
	lastSearch time.Time
}

// Option configures a Client.
type Option func(*Client)

// WithBaseURL points the client at an alternative API root. Tests use it to aim
// at an httptest server.
func WithBaseURL(u string) Option {
	return func(c *Client) { c.baseURL = strings.TrimSuffix(u, "/") }
}

// WithClock replaces the client's time source and sleep function so tests can
// exercise backoff and pacing without actually waiting.
func WithClock(now func() time.Time, sleep func(time.Duration)) Option {
	return func(c *Client) { c.now, c.sleep = now, sleep }
}

// WithToken sets the auth token explicitly, bypassing discovery.
func WithToken(t string) Option {
	return func(c *Client) { c.token = t }
}

// NewClient builds a client, resolving a token via ResolveToken unless one was
// supplied. A missing token is not fatal: the client still works at the much
// lower unauthenticated rate limit, which is enough for a smoke test but not
// for a real run.
func NewClient(opts ...Option) *Client {
	c := &Client{
		http:    &http.Client{Timeout: 30 * time.Second},
		baseURL: defaultBaseURL,
		now:     time.Now,
		sleep:   time.Sleep,
		budgets: map[string]Budget{},
	}
	for _, o := range opts {
		o(c)
	}
	if c.token == "" {
		c.token, _ = ResolveToken()
	}
	return c
}

// Authenticated reports whether the client has a token.
func (c *Client) Authenticated() bool { return c.token != "" }

// ResolveToken finds a GitHub token: the GITHUB_TOKEN or GH_TOKEN environment
// variables first, then the gh CLI's stored credential. The CLI fallback is what
// makes the tool work on a developer machine with no exported token, while the
// environment variables are what the Actions workflow supplies.
func ResolveToken() (string, error) {
	for _, k := range []string{"GITHUB_TOKEN", "GH_TOKEN"} {
		if v := strings.TrimSpace(os.Getenv(k)); v != "" {
			return v, nil
		}
	}
	out, err := exec.Command("gh", "auth", "token").Output()
	if err != nil {
		return "", fmt.Errorf("no GITHUB_TOKEN and gh auth token failed: %w", err)
	}
	t := strings.TrimSpace(string(out))
	if t == "" {
		return "", errors.New("gh auth token returned nothing")
	}
	return t, nil
}

// Budgets returns a copy of the last-seen rate-limit state per resource.
func (c *Client) Budgets() map[string]Budget {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make(map[string]Budget, len(c.budgets))
	for k, v := range c.budgets {
		out[k] = v
	}
	return out
}

// get issues a GET against path (a "/"-prefixed API path or a full URL) and
// decodes the JSON body into out. It returns the response headers so callers can
// read pagination Link headers. out may be nil to discard the body.
func (c *Client) get(ctx context.Context, path string, out any) (http.Header, error) {
	endpoint := path
	if strings.HasPrefix(path, "/") {
		endpoint = c.baseURL + path
	}
	isSearch := strings.Contains(endpoint, "/search/")

	var lastErr error
	for attempt := 0; attempt < maxAttempts; attempt++ {
		if isSearch {
			c.paceSearch()
		}

		req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
		if err != nil {
			return nil, err
		}
		req.Header.Set("Accept", "application/vnd.github+json")
		req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
		req.Header.Set("User-Agent", userAgent)
		if c.token != "" {
			req.Header.Set("Authorization", "Bearer "+c.token)
		}

		resp, err := c.http.Do(req)
		if err != nil {
			lastErr = err
			c.backoff(attempt)
			continue
		}

		body, readErr := io.ReadAll(resp.Body)
		resp.Body.Close()
		c.recordBudget(resp.Header)

		switch {
		case resp.StatusCode == http.StatusOK:
			if readErr != nil {
				lastErr = readErr
				c.backoff(attempt)
				continue
			}
			if out != nil {
				if err := json.Unmarshal(body, out); err != nil {
					return resp.Header, fmt.Errorf("decoding %s: %w", endpoint, err)
				}
			}
			return resp.Header, nil

		case resp.StatusCode == http.StatusNotFound:
			return resp.Header, fmt.Errorf("%s: %w", endpoint, ErrNotFound)

		case resp.StatusCode == http.StatusForbidden || resp.StatusCode == http.StatusTooManyRequests:
			wait, ok := c.rateLimitWait(resp.Header)
			if !ok {
				// A 403 with no rate-limit headers is an authorisation
				// failure, not a limit. Retrying will not help.
				return resp.Header, fmt.Errorf("%s: %d: %s", endpoint, resp.StatusCode, snippet(body))
			}
			if wait > maxRateLimitWait {
				return resp.Header, fmt.Errorf("%s: rate limited for %s, longer than the %s cap", endpoint, wait.Round(time.Second), maxRateLimitWait)
			}
			lastErr = fmt.Errorf("%s: rate limited", endpoint)
			c.sleep(wait)
			continue

		case resp.StatusCode >= 500:
			lastErr = fmt.Errorf("%s: %d: %s", endpoint, resp.StatusCode, snippet(body))
			c.backoff(attempt)
			continue

		default:
			return resp.Header, fmt.Errorf("%s: %d: %s", endpoint, resp.StatusCode, snippet(body))
		}
	}
	return nil, fmt.Errorf("giving up after %d attempts: %w", maxAttempts, lastErr)
}

// paceSearch spaces search requests out to stay inside the 30-per-minute search
// limit.
func (c *Client) paceSearch() {
	c.mu.Lock()
	var wait time.Duration
	if !c.lastSearch.IsZero() {
		if elapsed := c.now().Sub(c.lastSearch); elapsed < searchInterval {
			wait = searchInterval - elapsed
		}
	}
	c.lastSearch = c.now().Add(wait)
	c.mu.Unlock()

	if wait > 0 {
		c.sleep(wait)
	}
}

func (c *Client) backoff(attempt int) {
	c.sleep(time.Duration(1*intPow2(attempt)) * time.Second)
}

func intPow2(n int) time.Duration {
	d := time.Duration(1)
	for i := 0; i < n; i++ {
		d *= 2
	}
	return d
}

// rateLimitWait works out how long to wait after a 403 or 429. Primary limit
// exhaustion reports x-ratelimit-remaining: 0 with a reset timestamp; secondary
// limits use retry-after. A 403 with neither is an authorisation failure, so ok
// is false and the caller should not retry.
func (c *Client) rateLimitWait(h http.Header) (time.Duration, bool) {
	if v := h.Get("Retry-After"); v != "" {
		if secs, err := strconv.Atoi(v); err == nil {
			return time.Duration(secs)*time.Second + time.Second, true
		}
	}
	if h.Get("X-RateLimit-Remaining") == "0" {
		if v := h.Get("X-RateLimit-Reset"); v != "" {
			if unix, err := strconv.ParseInt(v, 10, 64); err == nil {
				d := time.Unix(unix, 0).Sub(c.now())
				if d < 0 {
					d = 0
				}
				return d + time.Second, true
			}
		}
	}
	return 0, false
}

func (c *Client) recordBudget(h http.Header) {
	res := h.Get("X-RateLimit-Resource")
	if res == "" {
		return
	}
	var b Budget
	b.Remaining, _ = strconv.Atoi(h.Get("X-RateLimit-Remaining"))
	b.Limit, _ = strconv.Atoi(h.Get("X-RateLimit-Limit"))
	if v, err := strconv.ParseInt(h.Get("X-RateLimit-Reset"), 10, 64); err == nil {
		b.Reset = time.Unix(v, 0)
	}
	c.mu.Lock()
	c.budgets[res] = b
	c.mu.Unlock()
}

func snippet(b []byte) string {
	s := strings.TrimSpace(string(b))
	if len(s) > 200 {
		return s[:200] + "..."
	}
	return s
}

// lastPage reads a Link header and returns the page number of its rel="last"
// link. Requesting a collection with per_page=1 and reading this back is how
// gitscout counts contributors in a single request instead of paginating through
// thousands of them. A collection with only one page has no Link header at all,
// hence ok=false and the caller falling back to what it already has.
func lastPage(h http.Header) (int, bool) {
	link := h.Get("Link")
	if link == "" {
		return 0, false
	}
	for _, part := range strings.Split(link, ",") {
		if !strings.Contains(part, "rel=\"last\"") {
			continue
		}
		start := strings.Index(part, "<")
		end := strings.Index(part, ">")
		if start < 0 || end <= start {
			continue
		}
		// The query must be parsed rather than scanned: a substring search
		// for "page=" matches inside "per_page=1", which every contributor
		// count request sends, and would report the per-page size as the
		// page number.
		u, err := url.Parse(part[start+1 : end])
		if err != nil {
			continue
		}
		if n, err := strconv.Atoi(u.Query().Get("page")); err == nil {
			return n, true
		}
	}
	return 0, false
}
