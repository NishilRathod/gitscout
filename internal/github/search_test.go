package github

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func testConfig() DiscoverConfig {
	cfg := DefaultDiscoverConfig()
	cfg.Now = time.Date(2026, 9, 3, 0, 0, 0, 0, time.UTC)
	cfg.Languages = []string{"Go", "Rust"}
	return cfg
}

func TestBuildSlicesCoversEveryLens(t *testing.T) {
	slices := BuildSlices(testConfig())

	want := []string{
		"rising:go", "rising:rust",
		SliceGoodFirst, SliceHelpWanted, SliceAbandoned,
	}
	if len(slices) != len(want) {
		t.Fatalf("got %d slices, want %d: %v", len(slices), len(want), sliceNames(slices))
	}
	for i, n := range want {
		if slices[i].Name != n {
			t.Errorf("slice %d = %q, want %q", i, slices[i].Name, n)
		}
	}
}

func TestBuildSlicesRisingQuery(t *testing.T) {
	s := BuildSlices(testConfig())[0]

	for _, want := range []string{
		"language:Go",
		"stars:>=150",
		"archived:false",
		"is:public",
		// 540 days before 2026-09-03.
		"created:>=2025-03-12",
		// 21 days before 2026-09-03.
		"pushed:>=2026-08-13",
	} {
		if !strings.Contains(s.Query, want) {
			t.Errorf("query %q missing %q", s.Query, want)
		}
	}
	if s.Sort != "stars" {
		t.Errorf("sort = %q, want stars", s.Sort)
	}
}

// The abandoned slice is the one that finds gap opportunities, and it is the
// only slice that looks for the absence of recent activity rather than its
// presence. Getting the comparison backwards would silently return the
// healthiest repos on GitHub instead of the dead ones.
func TestBuildSlicesAbandonedLooksBackwards(t *testing.T) {
	var s Slice
	for _, x := range BuildSlices(testConfig()) {
		if x.Name == SliceAbandoned {
			s = x
		}
	}
	if !strings.Contains(s.Query, "pushed:<2025-12-07") {
		t.Errorf("query %q should exclude repos pushed since the cutoff", s.Query)
	}
	if strings.Contains(s.Query, "pushed:>=") {
		t.Errorf("query %q must not require recent activity", s.Query)
	}
	if !strings.Contains(s.Query, "help-wanted-issues:>=3") {
		t.Errorf("query %q should require unmet requests for help", s.Query)
	}
	// Archived repos belong here: an archived repo with open help-wanted
	// issues is precisely a project someone gave up on.
	if strings.Contains(s.Query, "archived:false") {
		t.Errorf("query %q should not exclude archived repos", s.Query)
	}
}

func TestBuildSlicesWithoutLanguages(t *testing.T) {
	cfg := testConfig()
	cfg.Languages = nil
	slices := BuildSlices(cfg)

	if slices[0].Name != SliceRising {
		t.Errorf("first slice = %q, want %q", slices[0].Name, SliceRising)
	}
	if strings.Contains(slices[0].Query, "language:") {
		t.Errorf("query %q should not pin a language", slices[0].Query)
	}
}

func TestSearchReposStopsOnShortPage(t *testing.T) {
	var pages int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		pages++
		// One full page, then a short one.
		n := searchPageSize
		if pages == 2 {
			n = 3
		}
		fmt.Fprint(w, repoPage(n, pages*1000))
	}))
	defer srv.Close()

	c := NewClient(WithBaseURL(srv.URL), WithToken("t"),
		WithClock(time.Now, func(time.Duration) {}))

	repos, err := c.SearchRepos(context.Background(), Slice{Name: "x", Query: "q", MaxPages: 5})
	if err != nil {
		t.Fatal(err)
	}
	if pages != 2 {
		t.Errorf("requested %d pages, want 2", pages)
	}
	if len(repos) != searchPageSize+3 {
		t.Errorf("got %d repos, want %d", len(repos), searchPageSize+3)
	}
}

// The search API refuses to return more than 1000 results for any single query,
// so paginating past page 10 only wastes the scarce search budget.
func TestSearchReposRespectsResultCeiling(t *testing.T) {
	var pages int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		pages++
		fmt.Fprint(w, repoPage(searchPageSize, pages*1000))
	}))
	defer srv.Close()

	c := NewClient(WithBaseURL(srv.URL), WithToken("t"),
		WithClock(time.Now, func(time.Duration) {}))

	if _, err := c.SearchRepos(context.Background(), Slice{Name: "x", Query: "q", MaxPages: 50}); err != nil {
		t.Fatal(err)
	}
	if pages != 10 {
		t.Errorf("requested %d pages, want 10", pages)
	}
}

// A repo surfaced by several lenses should appear once, carrying every label.
// That overlap is itself the strongest signal the tool has: rising *and*
// advertising good first issues is a far better target than either alone.
func TestDiscoverDeduplicatesAndMergesSlices(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query().Get("q")
		switch {
		case strings.Contains(q, "language:Go"):
			fmt.Fprint(w, `{"items":[
			  {"id":1,"full_name":"a/shared"},
			  {"id":2,"full_name":"b/go-only"}]}`)
		case strings.Contains(q, "good-first-issues"):
			fmt.Fprint(w, `{"items":[{"id":1,"full_name":"a/shared"}]}`)
		default:
			fmt.Fprint(w, `{"items":[]}`)
		}
	}))
	defer srv.Close()

	c := NewClient(WithBaseURL(srv.URL), WithToken("t"),
		WithClock(time.Now, func(time.Duration) {}))

	cfg := testConfig()
	cfg.Languages = []string{"Go"}
	repos, errs := c.Discover(context.Background(), cfg)
	if len(errs) != 0 {
		t.Fatalf("errors: %v", errs)
	}
	if len(repos) != 2 {
		t.Fatalf("got %d repos, want 2: %+v", len(repos), repos)
	}

	byName := map[string][]string{}
	for _, r := range repos {
		byName[r.FullName] = r.Slices
	}
	shared := byName["a/shared"]
	if len(shared) != 2 {
		t.Errorf("a/shared slices = %v, want both lenses", shared)
	}
	if got := byName["b/go-only"]; len(got) != 1 || got[0] != "rising:go" {
		t.Errorf("b/go-only slices = %v, want [rising:go]", got)
	}
}

// One failing slice must not lose the results of the others: a partial digest
// beats no digest.
func TestDiscoverSurvivesSliceFailure(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Query().Get("q"), "help-wanted-issues:>=5") {
			w.WriteHeader(http.StatusUnprocessableEntity)
			fmt.Fprint(w, `{"message":"Validation Failed"}`)
			return
		}
		fmt.Fprint(w, `{"items":[{"id":7,"full_name":"ok/repo"}]}`)
	}))
	defer srv.Close()

	c := NewClient(WithBaseURL(srv.URL), WithToken("t"),
		WithClock(time.Now, func(time.Duration) {}))

	cfg := testConfig()
	cfg.Languages = []string{"Go"}
	repos, errs := c.Discover(context.Background(), cfg)
	if len(errs) != 1 {
		t.Errorf("got %d errors, want 1: %v", len(errs), errs)
	}
	if len(repos) != 1 || repos[0].FullName != "ok/repo" {
		t.Errorf("repos = %+v, want the surviving slice's result", repos)
	}
}

func TestRepoHelpers(t *testing.T) {
	now := time.Date(2026, 9, 3, 0, 0, 0, 0, time.UTC)
	r := Repo{
		FullName:  "NishilRathod/gitscout",
		CreatedAt: now.AddDate(0, 0, -100),
		PushedAt:  now.AddDate(0, 0, -5),
	}
	if r.Owner() != "NishilRathod" || r.Name() != "gitscout" {
		t.Errorf("owner/name = %q/%q", r.Owner(), r.Name())
	}
	if got := r.AgeDays(now); got != 100 {
		t.Errorf("AgeDays = %v, want 100", got)
	}
	if got := r.StaleDays(now); got != 5 {
		t.Errorf("StaleDays = %v, want 5", got)
	}

	// Age is floored at 1 so it is always safe to divide by.
	fresh := Repo{CreatedAt: now.Add(-time.Hour)}
	if got := fresh.AgeDays(now); got != 1 {
		t.Errorf("AgeDays for a new repo = %v, want 1", got)
	}

	// GitHub reports NOASSERTION for a licence file it cannot identify, which
	// tells a would-be contributor no more than having none.
	unclear := Repo{}
	unclear.License.SPDXID = "NOASSERTION"
	if unclear.HasLicense() {
		t.Error("NOASSERTION should not count as a licence")
	}
	mit := Repo{}
	mit.License.SPDXID = "MIT"
	if !mit.HasLicense() {
		t.Error("MIT should count as a licence")
	}
}

func sliceNames(s []Slice) []string {
	out := make([]string, len(s))
	for i, x := range s {
		out[i] = x.Name
	}
	return out
}

func repoPage(n, idBase int) string {
	var b strings.Builder
	b.WriteString(`{"items":[`)
	for i := 0; i < n; i++ {
		if i > 0 {
			b.WriteByte(',')
		}
		fmt.Fprintf(&b, `{"id":%d,"full_name":"o/r%d"}`, idBase+i, idBase+i)
	}
	b.WriteString(`]}`)
	return b.String()
}
