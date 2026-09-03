package github

import (
	"context"
	"fmt"
	"net/url"
	"sort"
	"strings"
	"time"
)

// searchPageSize is the API maximum. The search API also caps any single query
// at 1000 results no matter how many pages are requested, which is why
// discovery slices the space by language and star band instead of issuing one
// broad query.
const searchPageSize = 100

type searchResponse struct {
	TotalCount        int    `json:"total_count"`
	IncompleteResults bool   `json:"incomplete_results"`
	Items             []Repo `json:"items"`
}

// Slice is one discovery query: a named lens on the repository space. Every
// candidate records which slices surfaced it, which is what later lets the
// digest explain why a repo is on the list.
type Slice struct {
	Name     string
	Query    string
	Sort     string // "stars", "updated", or "" for best match
	MaxPages int
}

// SearchRepos runs one slice and returns its repositories. It stops at
// MaxPages, at the 1000-result API ceiling, or when a page comes back short,
// whichever happens first.
func (c *Client) SearchRepos(ctx context.Context, s Slice) ([]Repo, error) {
	pages := s.MaxPages
	if pages < 1 {
		pages = 1
	}
	if maxByCeiling := 1000 / searchPageSize; pages > maxByCeiling {
		pages = maxByCeiling
	}

	var out []Repo
	for page := 1; page <= pages; page++ {
		q := url.Values{}
		q.Set("q", s.Query)
		q.Set("per_page", fmt.Sprint(searchPageSize))
		q.Set("page", fmt.Sprint(page))
		if s.Sort != "" {
			q.Set("sort", s.Sort)
			q.Set("order", "desc")
		}

		var resp searchResponse
		if _, err := c.get(ctx, "/search/repositories?"+q.Encode(), &resp); err != nil {
			return out, fmt.Errorf("slice %q page %d: %w", s.Name, page, err)
		}
		for i := range resp.Items {
			resp.Items[i].Slices = []string{s.Name}
		}
		out = append(out, resp.Items...)

		if len(resp.Items) < searchPageSize {
			break
		}
	}
	return out, nil
}

// CountRepos returns how many repositories match a query without fetching them.
// Asking for a single item still yields total_count, which is all the
// gap-finding pass needs: it is testing whether something exists at all, and a
// full page of results would cost the same scarce search request for data it
// would throw away.
func (c *Client) CountRepos(ctx context.Context, query string) (int, error) {
	q := url.Values{}
	q.Set("q", query)
	q.Set("per_page", "1")

	var resp searchResponse
	if _, err := c.get(ctx, "/search/repositories?"+q.Encode(), &resp); err != nil {
		return 0, fmt.Errorf("counting %q: %w", query, err)
	}
	return resp.TotalCount, nil
}

// DiscoverConfig parameterises the query matrix. Languages are supplied by the
// caller rather than hardcoded, because which languages matter is a function of
// the user's profile: what they already know, and what they deliberately do not.
type DiscoverConfig struct {
	// Languages to sweep for rising repositories. Empty means one
	// language-agnostic sweep.
	Languages []string

	// MinStars is the floor for the rising slices. Below a few hundred stars
	// the signal is mostly noise.
	MinStars int

	// YoungerThanDays bounds repository age for the rising slices. A repo
	// that has been around for a decade is not "newly gaining popularity"
	// however many stars it has.
	YoungerThanDays int

	// ActiveWithinDays requires a push inside this window, filtering out
	// repos that spiked once and died.
	ActiveWithinDays int

	// AbandonedAfterDays is how long without a push qualifies a repo as
	// abandoned for the gap-finding slice.
	AbandonedAfterDays int

	// PagesPerSlice caps pagination per slice, trading breadth for run time.
	PagesPerSlice int

	// Now is the reference time. Zero means time.Now.
	Now time.Time
}

// DefaultDiscoverConfig returns settings that produce a useful run in about two
// minutes of wall time against the search rate limit.
func DefaultDiscoverConfig() DiscoverConfig {
	return DiscoverConfig{
		MinStars:           150,
		YoungerThanDays:    540,
		ActiveWithinDays:   21,
		AbandonedAfterDays: 270,
		PagesPerSlice:      2,
	}
}

func (cfg DiscoverConfig) now() time.Time {
	if cfg.Now.IsZero() {
		return time.Now()
	}
	return cfg.Now
}

// Slice names. Scorers and renderers key off these, so they are constants
// rather than loose strings.
const (
	SliceRising     = "rising"
	SliceGoodFirst  = "good-first-issues"
	SliceHelpWanted = "help-wanted"
	SliceAbandoned  = "abandoned-but-wanted"
)

// BuildSlices turns a config into the concrete set of search queries. It is
// separated from Discover so the matrix can be tested without any network.
func BuildSlices(cfg DiscoverConfig) []Slice {
	now := cfg.now()
	daysAgo := func(d int) string { return now.AddDate(0, 0, -d).Format("2006-01-02") }

	createdAfter := daysAgo(cfg.YoungerThanDays)
	activeAfter := daysAgo(cfg.ActiveWithinDays)
	abandonedBefore := daysAgo(cfg.AbandonedAfterDays)

	langs := cfg.Languages
	if len(langs) == 0 {
		langs = []string{""}
	}

	var slices []Slice

	// Rising: young, actively pushed, already past a star floor. One slice
	// per language keeps each query under the 1000-result ceiling.
	for _, lang := range langs {
		terms := []string{
			fmt.Sprintf("stars:>=%d", cfg.MinStars),
			"created:>=" + createdAfter,
			"pushed:>=" + activeAfter,
			"is:public",
			"archived:false",
		}
		name := SliceRising
		if lang != "" {
			terms = append(terms, "language:"+lang)
			name = SliceRising + ":" + strings.ToLower(lang)
		}
		slices = append(slices, Slice{
			Name:     name,
			Query:    strings.Join(terms, " "),
			Sort:     "stars",
			MaxPages: cfg.PagesPerSlice,
		})
	}

	// Repos advertising work for newcomers. Not restricted by age: a mature
	// project with a healthy good-first-issue queue is the single best place
	// to land a first merged PR.
	slices = append(slices, Slice{
		Name: SliceGoodFirst,
		Query: strings.Join([]string{
			"good-first-issues:>=3",
			"stars:>=200",
			"pushed:>=" + activeAfter,
			"is:public", "archived:false",
		}, " "),
		Sort:     "updated",
		MaxPages: cfg.PagesPerSlice,
	})

	slices = append(slices, Slice{
		Name: SliceHelpWanted,
		Query: strings.Join([]string{
			"help-wanted-issues:>=5",
			"stars:>=150",
			"pushed:>=" + activeAfter,
			"is:public", "archived:false",
		}, " "),
		Sort:     "updated",
		MaxPages: cfg.PagesPerSlice,
	})

	// The gap signal: plenty of stars, open requests for help, and nobody
	// has pushed in months. Either revive it or build the replacement.
	slices = append(slices, Slice{
		Name: SliceAbandoned,
		Query: strings.Join([]string{
			"help-wanted-issues:>=3",
			"stars:>=1000",
			"pushed:<" + abandonedBefore,
			"is:public",
		}, " "),
		Sort:     "stars",
		MaxPages: cfg.PagesPerSlice,
	})

	return slices
}

// Discover runs every slice and returns the deduplicated union. A repo found by
// several slices keeps all of their names in Slices, which is signal in itself:
// rising *and* good-first-issues is a far better contribution target than
// either alone.
//
// A failing slice does not abort the run — partial discovery still produces a
// useful digest — but its error is collected and returned alongside the results.
func (c *Client) Discover(ctx context.Context, cfg DiscoverConfig) ([]Repo, []error) {
	byID := map[int64]*Repo{}
	var errs []error

	for _, s := range BuildSlices(cfg) {
		repos, err := c.SearchRepos(ctx, s)
		if err != nil {
			errs = append(errs, err)
			// Keep whatever pages did come back before the failure.
		}
		for _, r := range repos {
			if existing, ok := byID[r.ID]; ok {
				existing.Slices = appendUnique(existing.Slices, s.Name)
				continue
			}
			cp := r
			cp.Slices = []string{s.Name}
			byID[r.ID] = &cp
		}
	}

	out := make([]Repo, 0, len(byID))
	for _, r := range byID {
		sort.Strings(r.Slices)
		out = append(out, *r)
	}
	// Stable output so digests diff cleanly between runs.
	sort.Slice(out, func(i, j int) bool { return out[i].FullName < out[j].FullName })
	return out, errs
}

func appendUnique(list []string, v string) []string {
	for _, x := range list {
		if x == v {
			return list
		}
	}
	return append(list, v)
}
