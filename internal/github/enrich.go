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
	r.HumanPRs90d = stats.HumanMerged90d
	r.ForkPRs90d = stats.ForkMerged90d
	r.ForkAuthors90d = stats.ForkAuthors90d

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

// PRStats summarises recent pull-request throughput and how much of it came
// from outside the project.
type PRStats struct {
	Merged30d int
	Merged90d int

	// The rest cover the 90-day window and exclude bots.
	HumanMerged90d int
	ForkMerged90d  int // opened from a fork rather than a branch of the repo
	ForkAuthors90d int // distinct people behind ForkMerged90d
}

// MergedPRStats counts recently merged pull requests, and how many of them came
// from forks and from how many people, from a single page of recently updated
// closed PRs.
//
// A pull request from a fork is the honest proxy for outside work. Pushing a
// branch to the repository itself needs write access, which an outsider does
// not have, so in-repo branches are the maintainers' own work however many
// maintainers there are. Counting every distinct author instead rated projects
// that merge almost nothing from outside as welcoming: stablyai/orca had five
// people's PRs merged in 90 days, but measured on 2026-10-07 only 2 of its last
// 80 merged PRs came from forks, from one person. Establishing who is core
// directly would need the collaborators endpoint, which requires push access
// the tool does not have; the head repository is already in this response.
//
// The comparison is GraphQL's isCrossRepository, made over REST so it costs no
// extra request: the head repository differs from the base. A null head
// repository means the fork was deleted after the PR was opened, and still
// counts as a fork. Maintainers who work from personal forks are counted as
// outside authors; that errs towards calling a project open, the opposite of
// the mistake this replaces.
//
// The single-page cap means a very busy repository can undercount. That biases
// the score down for exactly the repos that need no help finding contributors,
// so it is a safe direction to be wrong in.
func (c *Client) MergedPRStats(ctx context.Context, fullName string, now time.Time) (PRStats, error) {
	type repoRef struct {
		ID int64 `json:"id"`
	}
	var prs []struct {
		MergedAt *time.Time `json:"merged_at"`
		User     struct {
			Login string `json:"login"`
			Type  string `json:"type"`
		} `json:"user"`
		Head struct {
			Repo *repoRef `json:"repo"`
		} `json:"head"`
		Base struct {
			Repo *repoRef `json:"repo"`
		} `json:"base"`
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
	forkAuthors := map[string]bool{}
	cut30 := now.AddDate(0, 0, -30)
	cut90 := now.AddDate(0, 0, -90)

	for _, pr := range prs {
		if pr.MergedAt == nil {
			continue
		}
		if pr.MergedAt.After(cut30) {
			st.Merged30d++
		}
		if !pr.MergedAt.After(cut90) {
			continue
		}
		st.Merged90d++

		// Bots are excluded: a repo whose merged PRs are mostly dependency
		// bumps from a bot is not thereby welcoming to human contributors,
		// and counting them either way would skew the fork share.
		if pr.User.Login == "" || isBot(pr.User.Login, pr.User.Type) {
			continue
		}
		st.HumanMerged90d++

		fromFork := pr.Head.Repo == nil ||
			(pr.Base.Repo != nil && pr.Head.Repo.ID != pr.Base.Repo.ID)
		if fromFork {
			st.ForkMerged90d++
			forkAuthors[pr.User.Login] = true
		}
	}
	st.ForkAuthors90d = len(forkAuthors)
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
