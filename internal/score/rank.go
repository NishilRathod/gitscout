package score

import (
	"sort"
	"strings"
	"time"

	"github.com/NishilRathod/gitscout/internal/github"
	"github.com/NishilRathod/gitscout/internal/profile"
	"github.com/NishilRathod/gitscout/internal/store"
)

// Candidate is a repository with every axis scored.
type Candidate struct {
	Repo             github.Repo
	Trend            store.Trend
	Momentum         Score
	Contributability Score
	Fit              Fit
}

// Evaluate scores every discovered repository against the user's profile and
// whatever history the store holds.
//
// The user's own repositories are dropped: they are already in the portfolio,
// and recommending someone contribute to themselves is noise.
func Evaluate(repos []github.Repo, p profile.Profile, h store.History, now time.Time) []Candidate {
	out := make([]Candidate, 0, len(repos))
	for _, r := range repos {
		if strings.EqualFold(r.Owner(), p.Login) {
			continue
		}
		tr := h.Trend(r.ID)
		out = append(out, Candidate{
			Repo:             r,
			Trend:            tr,
			Momentum:         Momentum(r, tr, now),
			Contributability: Contributability(r, now),
			Fit:              FitFor(r, p),
		})
	}
	return out
}

// ContributeRank ranks repositories the user could realistically help with now.
// Contributability dominates: a welcoming project in a familiar language beats
// a spectacular one that merges nothing.
func (c Candidate) ContributeRank() float64 {
	return 0.50*c.Contributability.Total +
		0.25*c.Fit.Comfort.Total +
		0.25*c.Momentum.Total
}

// StretchRank ranks repositories that would add something new to the portfolio.
// Contributability still leads — an unfamiliar stack is only useful if the
// project will actually take the work — but unfamiliarity replaces comfort.
func (c Candidate) StretchRank() float64 {
	return 0.40*c.Contributability.Total +
		0.35*c.Fit.Stretch.Total +
		0.25*c.Momentum.Total
}

// TopContribute returns the best contribution targets, strongest first.
func TopContribute(cands []Candidate, n int) []Candidate {
	return top(cands, n, Candidate.ContributeRank, func(c Candidate) bool {
		// Something must actually invite contribution; ranking on comfort
		// and momentum alone would recommend closed shops.
		return c.Contributability.Total > 0
	})
}

// TopStretch returns the best portfolio-growth targets, strongest first.
//
// Repositories in a language the user already knows well are excluded outright
// rather than merely down-weighted: the entire purpose of this list is to leave
// familiar ground, and a strong TypeScript project would otherwise outrank
// every unfamiliar one on its other merits.
func TopStretch(cands []Candidate, p profile.Profile, n int) []Candidate {
	return top(cands, n, Candidate.StretchRank, func(c Candidate) bool {
		return c.Contributability.Total > 0 &&
			c.Repo.Language != "" &&
			!p.Knows(c.Repo.Language)
	})
}

func top(cands []Candidate, n int, rank func(Candidate) float64, keep func(Candidate) bool) []Candidate {
	var out []Candidate
	for _, c := range cands {
		if keep(c) {
			out = append(out, c)
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		ri, rj := rank(out[i]), rank(out[j])
		if ri != rj {
			return ri > rj
		}
		// A deterministic tiebreak keeps digests diffable between runs.
		return out[i].Repo.FullName < out[j].Repo.FullName
	})
	if n > 0 && len(out) > n {
		out = out[:n]
	}
	return out
}

// ByMomentum orders candidates by growth alone, for choosing which repositories
// are worth spending enrichment requests on.
func ByMomentum(cands []Candidate, n int) []Candidate {
	return top(cands, n, func(c Candidate) float64 { return c.Momentum.Total },
		func(Candidate) bool { return true })
}
