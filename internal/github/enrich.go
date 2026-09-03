package github

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"sync"
	"time"
)

// enrichConcurrency is how many repositories are enriched in parallel. The core
// limit is 5000/hour, so the constraint here is politeness and connection reuse
// rather than budget.
const enrichConcurrency = 6

// Enrich fills in the fields that discovery cannot supply, for the repositories
// it is given. It costs three core-budget requests per repository and no search
// requests at all, so it is safe to run over a few hundred candidates.
//
// Failures are per-repository and non-fatal: a repo that cannot be enriched
// keeps Enriched=false and is scored on discovery data alone.
func (c *Client) Enrich(ctx context.Context, repos []Repo) []error {
	var (
		mu   sync.Mutex
		errs []error
		wg   sync.WaitGroup
	)
	sem := make(chan struct{}, enrichConcurrency)

	for i := range repos {
		wg.Add(1)
		go func(r *Repo) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()

			if err := c.enrichOne(ctx, r); err != nil {
				mu.Lock()
				errs = append(errs, fmt.Errorf("enriching %s: %w", r.FullName, err))
				mu.Unlock()
			}
		}(&repos[i])
	}
	wg.Wait()
	return errs
}

func (c *Client) enrichOne(ctx context.Context, r *Repo) error {
	var firstErr error
	note := func(err error) {
		if err != nil && firstErr == nil && !errors.Is(err, ErrNotFound) {
			firstErr = err
		}
	}

	n, err := c.ContributorCount(ctx, r.FullName)
	note(err)
	r.Contributors = n

	stats, err := c.MergedPRStats(ctx, r.FullName, time.Now())
	note(err)
	r.MergedPRs30d = stats.Merged30d
	r.MergedPRs90d = stats.Merged90d
	r.PRAuthors90d = stats.DistinctAuthors90d

	has, err := c.HasContributingGuide(ctx, r.FullName)
	note(err)
	r.HasContributing = has

	r.Enriched = firstErr == nil
	return firstErr
}

// ContributorCount returns the number of contributors. Asking for a single item
// per page and reading the page number off the rel="last" link turns what would
// be dozens of paginated requests into exactly one.
//
// GitHub caps this listing at 500 contributors for large repositories and
// returns 204 with no content for empty ones; both surface here as the best
// available count rather than an error.
func (c *Client) ContributorCount(ctx context.Context, fullName string) (int, error) {
	var items []struct {
		Login string `json:"login"`
	}
	h, err := c.get(ctx, "/repos/"+fullName+"/contributors?per_page=1&anon=false", &items)
	if err != nil {
		return 0, err
	}
	if n, ok := lastPage(h); ok {
		return n, nil
	}
	// No Link header means a single page, which at per_page=1 means at most
	// one contributor.
	return len(items), nil
}

// PRStats summarises recent pull-request throughput.
type PRStats struct {
	Merged30d          int
	Merged90d          int
	DistinctAuthors90d int
}

// MergedPRStats counts recently merged pull requests and how many distinct
// people authored them, from a single page of recently updated closed PRs.
//
// Distinct authors is the honest proxy for "does this project accept outside
// work". Establishing who is core and who is an outsider would need the
// collaborators endpoint, which requires push access the tool does not have; a
// project merging PRs from many different people is accepting outside work
// whoever those people are, and one merging fifty PRs from one maintainer is
// not.
//
// The single-page cap means a very busy repository can undercount. That biases
// the score down for exactly the repos that need no help finding contributors,
// so it is a safe direction to be wrong in.
func (c *Client) MergedPRStats(ctx context.Context, fullName string, now time.Time) (PRStats, error) {
	var prs []struct {
		MergedAt *time.Time `json:"merged_at"`
		User     struct {
			Login string `json:"login"`
			Type  string `json:"type"`
		} `json:"user"`
	}

	q := url.Values{}
	q.Set("state", "closed")
	q.Set("sort", "updated")
	q.Set("direction", "desc")
	q.Set("per_page", "100")

	if _, err := c.get(ctx, "/repos/"+fullName+"/pulls?"+q.Encode(), &prs); err != nil {
		return PRStats{}, err
	}

	var st PRStats
	authors := map[string]bool{}
	cut30 := now.AddDate(0, 0, -30)
	cut90 := now.AddDate(0, 0, -90)

	for _, pr := range prs {
		if pr.MergedAt == nil {
			continue
		}
		if pr.MergedAt.After(cut90) {
			st.Merged90d++
			// Bots are excluded: a repo whose merged PRs are mostly
			// dependency bumps from a bot is not thereby welcoming to
			// human contributors, and counting them would say it is.
			if pr.User.Login != "" && !isBot(pr.User.Login, pr.User.Type) {
				authors[pr.User.Login] = true
			}
		}
		if pr.MergedAt.After(cut30) {
			st.Merged30d++
		}
	}
	st.DistinctAuthors90d = len(authors)
	return st, nil
}

// isBot recognises automated accounts. GitHub sets the account type to "Bot"
// for GitHub Apps, but plenty of automation runs under ordinary user accounts
// whose only marker is the conventional "[bot]" login suffix.
func isBot(login, accountType string) bool {
	if accountType == "Bot" {
		return true
	}
	return strings.HasSuffix(login, "[bot]")
}

// HasContributingGuide reports whether the project publishes contribution
// guidance. The community profile endpoint answers this in one request and
// covers every location GitHub recognises, which a direct check for
// CONTRIBUTING.md at the repository root would not.
func (c *Client) HasContributingGuide(ctx context.Context, fullName string) (bool, error) {
	var profile struct {
		Files struct {
			Contributing *struct {
				HTMLURL string `json:"html_url"`
			} `json:"contributing"`
		} `json:"files"`
	}
	if _, err := c.get(ctx, "/repos/"+fullName+"/community/profile", &profile); err != nil {
		if errors.Is(err, ErrNotFound) {
			return false, nil
		}
		return false, err
	}
	return profile.Files.Contributing != nil, nil
}
