package score

import (
	"fmt"
	"time"

	"github.com/NishilRathod/gitscout/internal/github"
)

const (
	// authorsReference is the number of distinct people whose pull requests
	// were merged in the last quarter that earns full marks. A project
	// merging work from this many different contributors is demonstrably open
	// to outsiders.
	authorsReference = 15

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
// write, while fifteen different people's patches landing in ninety days cannot
// be faked.
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

	// The strongest available evidence: distinct humans whose work was
	// merged recently.
	s.add("outside authors", 0.35*normalize(float64(r.PRAuthors90d), authorsReference),
		fmt.Sprintf("%d people had PRs merged in 90d", r.PRAuthors90d))

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
	// welcoming the documentation is.
	if r.MergedPRs90d == 0 {
		s.Total *= 0.3
		s.note("no pull requests merged in 90 days")
	}

	s.Total = clamp(s.Total)
	return s
}
