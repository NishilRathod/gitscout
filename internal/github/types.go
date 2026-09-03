// Package github wraps the parts of the GitHub REST API that gitscout needs:
// repository search, per-repo enrichment, and the user profile used to
// personalise scoring. It deliberately depends only on the standard library.
package github

import "time"

// Repo is a repository as returned by the search API. The search response
// already carries everything the discovery and scoring passes need, so a
// candidate never costs a per-repo request until the enrichment stage.
type Repo struct {
	ID          int64     `json:"id"`
	FullName    string    `json:"full_name"`
	Description string    `json:"description"`
	Language    string    `json:"language"`
	Topics      []string  `json:"topics"`
	Stars       int       `json:"stargazers_count"`
	Forks       int       `json:"forks_count"`
	OpenIssues  int       `json:"open_issues_count"`
	Archived    bool      `json:"archived"`
	Fork        bool      `json:"fork"`
	CreatedAt   time.Time `json:"created_at"`
	PushedAt    time.Time `json:"pushed_at"`
	HTMLURL     string    `json:"html_url"`

	License struct {
		SPDXID string `json:"spdx_id"`
	} `json:"license"`

	// Enrichment fields. Zero until the enrichment pass fills them in, so
	// scorers must check Enriched and treat these as "unknown" rather than
	// "zero" — a repo nobody enriched is not a repo with no contributors.
	Contributors    int  `json:"-"`
	MergedPRs30d    int  `json:"-"`
	MergedPRs90d    int  `json:"-"`
	PRAuthors90d    int  `json:"-"`
	HasContributing bool `json:"-"`
	Enriched        bool `json:"-"`

	// Provenance: which discovery slice surfaced this repo. A repo found by
	// several slices accumulates all of their labels.
	Slices []string `json:"-"`
}

// Owner returns the account part of the full name.
func (r Repo) Owner() string {
	for i := 0; i < len(r.FullName); i++ {
		if r.FullName[i] == '/' {
			return r.FullName[:i]
		}
	}
	return r.FullName
}

// Name returns the repository part of the full name.
func (r Repo) Name() string {
	for i := 0; i < len(r.FullName); i++ {
		if r.FullName[i] == '/' {
			return r.FullName[i+1:]
		}
	}
	return ""
}

// HasLicense reports whether the repo declares a recognised licence. GitHub
// returns the string "NOASSERTION" for licences it cannot identify, which for
// scoring purposes is no better than none at all.
func (r Repo) HasLicense() bool {
	return r.License.SPDXID != "" && r.License.SPDXID != "NOASSERTION"
}

// AgeDays is the repository's age in days at time now, floored at 1 so it is
// always safe as a divisor.
func (r Repo) AgeDays(now time.Time) float64 {
	d := now.Sub(r.CreatedAt).Hours() / 24
	if d < 1 {
		return 1
	}
	return d
}

// StaleDays is how long it has been since the last push.
func (r Repo) StaleDays(now time.Time) float64 {
	d := now.Sub(r.PushedAt).Hours() / 24
	if d < 0 {
		return 0
	}
	return d
}

// User is the authenticated or looked-up account, used to build the profile
// that personalises scoring.
type User struct {
	Login       string `json:"login"`
	Name        string `json:"name"`
	PublicRepos int    `json:"public_repos"`
}
