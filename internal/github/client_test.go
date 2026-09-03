package github

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// newTestClient wires a client to a test server with a clock that records
// sleeps instead of performing them, so retry and pacing logic can be asserted
// without the test taking as long as the backoff it exercises.
func newTestClient(t *testing.T, h http.Handler) (*Client, *[]time.Duration) {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)

	var slept []time.Duration
	now := time.Date(2026, 9, 3, 12, 0, 0, 0, time.UTC)
	c := NewClient(
		WithBaseURL(srv.URL),
		WithToken("test-token"),
		WithClock(
			func() time.Time { return now },
			func(d time.Duration) { slept = append(slept, d) },
		),
	)
	return c, &slept
}

func TestGetRetriesServerErrorsThenSucceeds(t *testing.T) {
	var calls int
	c, slept := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if calls < 3 {
			w.WriteHeader(http.StatusBadGateway)
			return
		}
		fmt.Fprint(w, `{"login":"NishilRathod"}`)
	}))

	var u User
	if _, err := c.get(context.Background(), "/users/NishilRathod", &u); err != nil {
		t.Fatalf("get: %v", err)
	}
	if u.Login != "NishilRathod" {
		t.Errorf("login = %q, want NishilRathod", u.Login)
	}
	if calls != 3 {
		t.Errorf("calls = %d, want 3", calls)
	}
	// Backoff must grow, not hammer the API at a fixed interval.
	want := []time.Duration{1 * time.Second, 2 * time.Second}
	if len(*slept) != len(want) {
		t.Fatalf("slept %v, want %v", *slept, want)
	}
	for i, d := range want {
		if (*slept)[i] != d {
			t.Errorf("sleep %d = %v, want %v", i, (*slept)[i], d)
		}
	}
}

func TestGetGivesUpAfterMaxAttempts(t *testing.T) {
	var calls int
	c, _ := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.WriteHeader(http.StatusInternalServerError)
	}))

	if _, err := c.get(context.Background(), "/users/x", nil); err == nil {
		t.Fatal("want error, got nil")
	}
	if calls != maxAttempts {
		t.Errorf("calls = %d, want %d", calls, maxAttempts)
	}
}

func TestGetWaitsForPrimaryRateLimitReset(t *testing.T) {
	// The fake clock sits at 12:00:00; the reset is 90s later.
	reset := time.Date(2026, 9, 3, 12, 1, 30, 0, time.UTC)
	var calls int
	c, slept := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if calls == 1 {
			w.Header().Set("X-RateLimit-Remaining", "0")
			w.Header().Set("X-RateLimit-Reset", fmt.Sprint(reset.Unix()))
			w.Header().Set("X-RateLimit-Resource", "core")
			w.WriteHeader(http.StatusForbidden)
			return
		}
		fmt.Fprint(w, `{"login":"ok"}`)
	}))

	var u User
	if _, err := c.get(context.Background(), "/users/x", &u); err != nil {
		t.Fatalf("get: %v", err)
	}
	if len(*slept) != 1 {
		t.Fatalf("slept %v, want one wait", *slept)
	}
	// 90s until reset, plus the one-second cushion.
	if got, want := (*slept)[0], 91*time.Second; got != want {
		t.Errorf("wait = %v, want %v", got, want)
	}
}

func TestGetHonoursRetryAfter(t *testing.T) {
	var calls int
	c, slept := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if calls == 1 {
			w.Header().Set("Retry-After", "7")
			w.WriteHeader(http.StatusTooManyRequests)
			return
		}
		fmt.Fprint(w, `{}`)
	}))

	if _, err := c.get(context.Background(), "/users/x", nil); err != nil {
		t.Fatalf("get: %v", err)
	}
	if got, want := (*slept)[0], 8*time.Second; got != want {
		t.Errorf("wait = %v, want %v", got, want)
	}
}

// A 403 carrying no rate-limit headers is an authorisation failure. Retrying it
// wastes the run's time budget and will never succeed.
func TestGetDoesNotRetryPlainForbidden(t *testing.T) {
	var calls int
	c, _ := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.WriteHeader(http.StatusForbidden)
		fmt.Fprint(w, `{"message":"Bad credentials"}`)
	}))

	_, err := c.get(context.Background(), "/users/x", nil)
	if err == nil {
		t.Fatal("want error, got nil")
	}
	if calls != 1 {
		t.Errorf("calls = %d, want 1 (no retry)", calls)
	}
}

func TestGetReturnsErrNotFound(t *testing.T) {
	c, _ := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))

	_, err := c.get(context.Background(), "/repos/a/b/community/profile", nil)
	if !errors.Is(err, ErrNotFound) {
		t.Errorf("err = %v, want ErrNotFound", err)
	}
}

func TestGetSendsAuthAndVersionHeaders(t *testing.T) {
	var got http.Header
	c, _ := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r.Header.Clone()
		fmt.Fprint(w, `{}`)
	}))

	if _, err := c.get(context.Background(), "/x", nil); err != nil {
		t.Fatal(err)
	}
	if v := got.Get("Authorization"); v != "Bearer test-token" {
		t.Errorf("Authorization = %q", v)
	}
	if v := got.Get("X-GitHub-Api-Version"); v == "" {
		t.Error("missing X-GitHub-Api-Version")
	}
	if v := got.Get("User-Agent"); v != userAgent {
		t.Errorf("User-Agent = %q, want %q", v, userAgent)
	}
}

func TestRecordBudget(t *testing.T) {
	c, _ := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-RateLimit-Resource", "search")
		w.Header().Set("X-RateLimit-Remaining", "27")
		w.Header().Set("X-RateLimit-Limit", "30")
		fmt.Fprint(w, `{}`)
	}))

	if _, err := c.get(context.Background(), "/x", nil); err != nil {
		t.Fatal(err)
	}
	b, ok := c.Budgets()["search"]
	if !ok {
		t.Fatal("no search budget recorded")
	}
	if b.Remaining != 27 || b.Limit != 30 {
		t.Errorf("budget = %+v, want remaining 27 of 30", b)
	}
}

func TestLastPage(t *testing.T) {
	tests := []struct {
		name string
		link string
		want int
		ok   bool
	}{
		{
			name: "contributor count",
			link: `<https://api.github.com/repositories/1/contributors?per_page=1&page=2>; rel="next", <https://api.github.com/repositories/1/contributors?per_page=1&page=23>; rel="last"`,
			want: 23, ok: true,
		},
		{
			name: "page is the final parameter",
			link: `<https://api.github.com/x?page=7>; rel="last"`,
			want: 7, ok: true,
		},
		{
			name: "no link header means a single page",
			link: "",
			ok:   false,
		},
		{
			name: "next but no last",
			link: `<https://api.github.com/x?page=2>; rel="next"`,
			ok:   false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := http.Header{}
			if tt.link != "" {
				h.Set("Link", tt.link)
			}
			got, ok := lastPage(h)
			if ok != tt.ok || (tt.ok && got != tt.want) {
				t.Errorf("lastPage = (%d, %v), want (%d, %v)", got, ok, tt.want, tt.ok)
			}
		})
	}
}

func TestContributorCountUsesLinkHeader(t *testing.T) {
	c, _ := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("per_page") != "1" {
			t.Errorf("per_page = %q, want 1", r.URL.Query().Get("per_page"))
		}
		w.Header().Set("Link", `<https://api/x?page=2>; rel="next", <https://api/x?page=412>; rel="last"`)
		fmt.Fprint(w, `[{"login":"a"}]`)
	}))

	n, err := c.ContributorCount(context.Background(), "a/b")
	if err != nil {
		t.Fatal(err)
	}
	if n != 412 {
		t.Errorf("contributors = %d, want 412", n)
	}
}

func TestContributorCountSinglePage(t *testing.T) {
	c, _ := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `[{"login":"solo"}]`)
	}))

	n, err := c.ContributorCount(context.Background(), "a/b")
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Errorf("contributors = %d, want 1", n)
	}
}

func TestMergedPRStatsWindowsAndBots(t *testing.T) {
	now := time.Date(2026, 9, 3, 0, 0, 0, 0, time.UTC)
	body := `[
	  {"merged_at":"2026-09-01T00:00:00Z","user":{"login":"alice","type":"User"}},
	  {"merged_at":"2026-08-20T00:00:00Z","user":{"login":"bob","type":"User"}},
	  {"merged_at":"2026-07-10T00:00:00Z","user":{"login":"carol","type":"User"}},
	  {"merged_at":"2026-08-30T00:00:00Z","user":{"login":"dependabot[bot]","type":"Bot"}},
	  {"merged_at":"2026-08-29T00:00:00Z","user":{"login":"renovate[bot]","type":"User"}},
	  {"merged_at":"2025-01-01T00:00:00Z","user":{"login":"dave","type":"User"}},
	  {"merged_at":null,"user":{"login":"eve","type":"User"}}
	]`
	c, _ := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, body)
	}))

	st, err := c.MergedPRStats(context.Background(), "a/b", now)
	if err != nil {
		t.Fatal(err)
	}
	// Within 30 days: alice, bob, both bots.
	if st.Merged30d != 4 {
		t.Errorf("Merged30d = %d, want 4", st.Merged30d)
	}
	// Within 90 days: the above plus carol.
	if st.Merged90d != 5 {
		t.Errorf("Merged90d = %d, want 5", st.Merged90d)
	}
	// Distinct humans only: alice, bob, carol. The renovate account has type
	// "User" and is caught by the login suffix alone.
	if st.DistinctAuthors90d != 3 {
		t.Errorf("DistinctAuthors90d = %d, want 3", st.DistinctAuthors90d)
	}
}

func TestHasContributingGuide(t *testing.T) {
	tests := []struct {
		name   string
		status int
		body   string
		want   bool
	}{
		{"present", 200, `{"files":{"contributing":{"html_url":"https://x"}}}`, true},
		{"absent", 200, `{"files":{"contributing":null}}`, false},
		{"endpoint 404s", 404, ``, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c, _ := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(tt.status)
				fmt.Fprint(w, tt.body)
			}))
			got, err := c.HasContributingGuide(context.Background(), "a/b")
			if err != nil {
				t.Fatalf("err = %v", err)
			}
			if got != tt.want {
				t.Errorf("got %v, want %v", got, tt.want)
			}
		})
	}
}

func TestResolveTokenPrefersEnv(t *testing.T) {
	t.Setenv("GITHUB_TOKEN", "  env-token  ")
	got, err := ResolveToken()
	if err != nil {
		t.Fatal(err)
	}
	if got != "env-token" {
		t.Errorf("token = %q, want env-token (trimmed)", got)
	}
}

func TestIsBot(t *testing.T) {
	tests := []struct {
		login, typ string
		want       bool
	}{
		{"dependabot[bot]", "Bot", true},
		{"renovate[bot]", "User", true},
		{"alice", "User", false},
		{"bot-enthusiast", "User", false},
	}
	for _, tt := range tests {
		if got := isBot(tt.login, tt.typ); got != tt.want {
			t.Errorf("isBot(%q, %q) = %v, want %v", tt.login, tt.typ, got, tt.want)
		}
	}
}

func TestSnippetTruncates(t *testing.T) {
	if got := snippet([]byte(strings.Repeat("x", 500))); len(got) != 203 {
		t.Errorf("len = %d, want 203", len(got))
	}
}
