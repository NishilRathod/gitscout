package score

import (
	"fmt"
	"time"

	"github.com/NishilRathod/gitscout/internal/github"
)

const (
	// authorsReference is the number of distinct outside authors — people
	// whose pull requests from a fork were merged in the last quarter — that
	// earns full marks. A project merging work from this many different
	// outsiders is demonstrably open to them.
	authorsReference = 15

	// forkShareReference is the share of human-authored merges that must
	// come from forks before a project counts as open in practice. Below it
	// the whole score is discounted in proportion. Measured on 2026-10-07,
	// projects that genuinely take outside work merged 70-98% of their PRs
	// from forks; ones that only looked busy merged 2-11%.
	forkShareReference = 0.25

	// closedFloor is the fraction of a score left to a project that merges
	// nothing from outside: the same discount as merging nothing at all.
	closedFloor = 0.3

	// mergedReference is the merged-pull-request count over the same window
	// that earns full marks for throughput.
	mergedReference = 40

	// activeWithinDays is how recently a project must have been pushed to
	// before its responsiveness is in doubt.
	activeWithinDays = 30
)

// Contributability scores how realistic it is to land a merged pull request.
//
// This is the axis that separates a project worth approaching from one that is
// merely popular. Weight sits mainly on evidence that outside work actually
// gets merged, rather than on stated intentions: a CONTRIBUTING.md is cheap to
// write, while fifteen different outsiders' patches landing in ninety days
// cannot be faked.
func Contributability(r github.Repo, now time.Time) Score {
	var s Score

	if r.Archived {
		s.note("archived — it accepts nothing")
		return s
	}

	// Explicit invitations, from the discovery slices that found it.
	for _, sl := range r.Slices {
		switch sl {
		case github.SliceGoodFirst:
			s.add("good first issues", 0.20, "advertises 3+ good first issues")
		case github.SliceHelpWanted:
			s.add("help wanted", 0.10, "advertises 5+ help-wanted issues")
		}
	}

	if !r.Enriched {
		s.note("not enriched — scored on discovery signals alone")
		s.Total = clamp(s.Total)
		return s
	}

	// The strongest available evidence: distinct outsiders whose work was
	// merged recently. Only pull requests from forks count; anyone pushing a
	// branch to the repository itself already has write access. It is the
	// headline even when it scores nothing, because then it is the reason.
	s.headline("outside authors", 0.35*normalize(float64(r.ForkAuthors90d), authorsReference),
		forkEvidence(r))

	s.add("merge throughput", 0.15*normalize(float64(r.MergedPRs90d), mergedReference),
		fmt.Sprintf("%d PRs merged in 90d (%d in 30d)", r.MergedPRs90d, r.MergedPRs30d))

	// A project with a handful of contributors and an enormous audience is
	// usually one person's showcase, whatever its issue labels say.
	if r.Contributors > 0 {
		s.add("contributor base", 0.10*normalize(float64(r.Contributors), 100),
			fmt.Sprintf("%d contributors", r.Contributors))
	}

	if r.HasContributing {
		s.add("contribution guide", 0.05, "publishes CONTRIBUTING")
	}
	if r.HasLicense() {
		s.add("licence", 0.05, r.License.SPDXID)
	} else {
		s.note("no clear licence — contributions may be legally murky")
	}

	if stale := r.StaleDays(now); stale <= activeWithinDays {
		s.add("recent activity", 0.05, fmt.Sprintf("pushed %.0f days ago", stale))
	} else {
		s.note(fmt.Sprintf("no push in %.0f days — a PR may sit unreviewed", stale))
	}

	// Nothing merged from anyone in three months is disqualifying however
	// welcoming the documentation is. Merging only the maintainers' own
	// branches is disqualifying for the same reason: invitations, throughput
	// and a large contributor base say nothing about whether an outsider's
	// work would land.
	switch {
	case r.MergedPRs90d == 0:
		s.Total *= closedFloor
		s.note("no pull requests merged in 90 days")
	case r.HumanPRs90d == 0:
		s.Total *= closedFloor
		s.note("no human-authored PRs merged in 90d — only bots")
	default:
		share := float64(r.ForkPRs90d) / float64(r.HumanPRs90d)
		if share < forkShareReference {
			f := closedFloor + (1-closedFloor)*share/forkShareReference
			s.Total *= f
			s.note(fmt.Sprintf("only %d/%d human PRs merged in 90d came from forks — most work lands from in-repo branches, so the score is scaled by %.2f",
				r.ForkPRs90d, r.HumanPRs90d, f))
		}
	}

	s.Total = clamp(s.Total)
	return s
}

// forkEvidence states who gets merged, in the terms the score uses.
func forkEvidence(r github.Repo) string {
	if r.HumanPRs90d == 0 {
		return "no human PRs merged in 90d"
	}
	authors := "authors"
	if r.ForkAuthors90d == 1 {
		authors = "author"
	}
	return fmt.Sprintf("%d/%d human PRs merged from forks, %d outside %s",
		r.ForkPRs90d, r.HumanPRs90d, r.ForkAuthors90d, authors)
}
