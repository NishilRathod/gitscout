package score

import (
	"fmt"
	"math"
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

	// acceptanceReference is the share of closed pull requests from forks
	// that must have been merged before a project counts as accepting
	// outside work. Below it the whole score is scaled in proportion, down
	// to closedFloor. Measured on 2026-11-02 over the projects in the
	// previous digest, those that take outside work merged 45-94% of the
	// fork PRs they closed in 90 days, large ones such as vllm-project/vllm
	// at the low end because they attract the most drive-by PRs.
	// DuarteSantos8/openGym, whose maintainer closes outside PRs and
	// re-implements the fixes, merged 13%.
	acceptanceReference = 0.3

	// acceptanceMinSample is the fewest closed fork PRs worth judging a
	// project on. Below it one withdrawn draft or spam PR moves the rate by
	// more than ten points.
	acceptanceMinSample = 10

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

	// Each discount below is noted on its own evidence, but only the strongest
	// is applied. They all count the same outside merges, so multiplying
	// them would push a project that merges nothing from outside below
	// closedFloor for one fact seen twice.
	scale, scaledBy := 1.0, -1
	discount := func(f float64, note string) {
		s.note(note)
		if f < scale {
			scale, scaledBy = f, len(s.Notes)-1
		}
	}

	// Nothing merged from anyone in three months is disqualifying however
	// welcoming the documentation is. Merging only the maintainers' own
	// branches is disqualifying for the same reason: invitations, throughput
	// and a large contributor base say nothing about whether an outsider's
	// work would land.
	switch {
	case r.MergedPRs90d == 0:
		discount(closedFloor, "no pull requests merged in 90 days")
	case r.HumanPRs90d == 0:
		discount(closedFloor, "no human-authored PRs merged in 90d — only bots")
	default:
		share := float64(r.ForkPRs90d) / float64(r.HumanPRs90d)
		if share < forkShareReference {
			discount(closedFloor+(1-closedFloor)*share/forkShareReference,
				fmt.Sprintf("only %d/%d human PRs merged in 90d came from forks — most work lands from in-repo branches",
					r.ForkPRs90d, r.HumanPRs90d))
		}
	}

	// Closing outside work is disqualifying too, however much of it still
	// gets merged: a busy maintainer can merge dozens of fork PRs while
	// closing hundreds more. The rate is the nearest thing to the odds that
	// an outside PR lands, so the score is scaled in proportion to it rather
	// than ramped like the fork share: a project merging a tenth of the
	// outside PRs it closes is a third as good a bet as one at the reference.
	if closed := r.ForkPRs90d + r.ForkUnmerged90d; closed >= acceptanceMinSample {
		rate := float64(r.ForkPRs90d) / float64(closed)
		if rate < acceptanceReference {
			discount(math.Max(closedFloor, rate/acceptanceReference),
				fmt.Sprintf("only %d/%d closed fork PRs merged in 90d — outside PRs are mostly closed unmerged",
					r.ForkPRs90d, closed))
		}
	}

	if scaledBy >= 0 {
		s.Total *= scale
		s.Notes[scaledBy] += fmt.Sprintf(", so the score is scaled by %.2f", scale)
	}

	s.Total = clamp(s.Total)
	return s
}

// forkEvidence states who gets merged, and what becomes of outside work, in
// the terms the score uses.
func forkEvidence(r github.Repo) string {
	e := "no human PRs merged in 90d"
	if r.HumanPRs90d > 0 {
		authors := "authors"
		if r.ForkAuthors90d == 1 {
			authors = "author"
		}
		e = fmt.Sprintf("%d/%d human PRs merged from forks, %d outside %s",
			r.ForkPRs90d, r.HumanPRs90d, r.ForkAuthors90d, authors)
	}
	if closed := r.ForkPRs90d + r.ForkUnmerged90d; closed > 0 {
		e += fmt.Sprintf(", %d/%d closed fork PRs merged", r.ForkPRs90d, closed)
	}
	return e
}
