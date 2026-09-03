package score

import (
	"fmt"
	"time"

	"github.com/NishilRathod/gitscout/internal/github"
	"github.com/NishilRathod/gitscout/internal/store"
)

const (
	// The two rate references differ because the two rates are not the same
	// measurement, and scoring them on one scale ranked nothing.
	//
	// measuredRateReference applies to growth observed between runs. Holding
	// 150 stars/day across a week is genuinely exceptional.
	//
	// proxyRateReference applies to the first-run fallback, stars divided by
	// age. Discovery selects for repositories that are already climbing, so
	// their lifetime averages sit far higher: live sweeps put the leaders
	// between 300 and 690 stars/day. An earlier shared reference of 50, and
	// then 300, left everything above it clamped to the same number — 62/day
	// and 472/day scored identically and the axis did no ranking at all.
	measuredRateReference = 150
	proxyRateReference    = 700

	// accelReference is the increase in stars-per-day between runs that earns
	// the full acceleration bonus. It sits well below measuredRateReference on
	// purpose: sustaining a high rate is common among launches, but getting
	// materially faster from one week to the next is not.
	accelReference = 40

	// staleAfterDays is how long a repository can go without a push before
	// its momentum is discounted. A project can trend for a week on a launch
	// post while nobody works on it.
	staleAfterDays = 30
	staleFloor     = 0.35
)

// Momentum scores how fast a repository is gaining attention.
//
// When the snapshot history holds two or more observations the rate is
// measured, and Score.Measured is set. On a first run there is nothing to
// compare against, so the score falls back to stars divided by age — a proxy
// that flatters anything that launched loudly and stalled. The digest reports
// which of the two it used; presenting the proxy as a measurement would be the
// single most misleading thing this tool could do.
func Momentum(r github.Repo, tr store.Trend, now time.Time) Score {
	var s Score

	if tr.HasRate {
		s.Measured = true
		s.add("rate", 0.6*normalize(tr.StarsPerDay, measuredRateReference),
			fmt.Sprintf("%.1f stars/day measured over %.0fd", tr.StarsPerDay, tr.SpanDays))

		switch {
		case tr.HasAcceleration && tr.Acceleration > 0:
			s.add("acceleration", 0.4*normalize(tr.Acceleration, accelReference),
				fmt.Sprintf("+%.1f stars/day faster than last run", tr.Acceleration))
		case tr.HasAcceleration:
			s.note(fmt.Sprintf("slowing: %.1f stars/day, down from %.1f", tr.StarsPerDay, tr.PrevStarsPerDay))
		default:
			s.note("one more run will show whether it is accelerating")
		}
	} else {
		// Stars per day since creation. It cannot tell a project climbing
		// now from one that peaked a year ago, which is exactly why it is
		// labelled.
		perDay := float64(r.Stars) / r.AgeDays(now)
		s.add("rate (proxy)", 0.6*normalize(perDay, proxyRateReference),
			fmt.Sprintf("%.1f stars/day since creation", perDay))
		s.note("no history yet — lifetime average, not current rate")
	}

	if stale := r.StaleDays(now); stale > staleAfterDays {
		before := s.Total
		s.Total *= staleFactor(stale)
		s.note(fmt.Sprintf("no push in %.0f days (momentum cut from %.2f)", stale, before))
	}

	if p, why := farmPenalty(r); p < 1 {
		before := s.Total
		s.Total *= p
		s.note(fmt.Sprintf("star count looks inorganic: %s (cut from %.2f)", why, before))
	}

	s.Total = clamp(s.Total)
	return s
}

// staleFactor decays momentum for repositories nobody has pushed to, easing
// from 1 at the threshold down to staleFloor rather than dropping off a cliff.
func staleFactor(staleDays float64) float64 {
	excess := staleDays - staleAfterDays
	f := 1 - (excess/180)*(1-staleFloor)
	if f < staleFloor {
		return staleFloor
	}
	return f
}

// farmPenalty discounts repositories whose star counts do not look like the
// product of ordinary use.
//
// The pattern this exists for is real and measured: a repository with 195,000
// stars, 108,000 forks, 23 contributors and no topics. Genuine projects at that
// scale carry hundreds of contributors and a fork ratio nearer a tenth of their
// stars. Every signal here is weak alone, so they are only decisive together.
func farmPenalty(r github.Repo) (float64, string) {
	penalty := 1.0
	var why string

	// A fork for every two stars is not how people use software. Real
	// projects sit far below this even when heavily forked for coursework.
	if r.Stars > 1000 && float64(r.Forks) > 0.4*float64(r.Stars) {
		penalty *= 0.5
		why = fmt.Sprintf("%d forks against %d stars", r.Forks, r.Stars)
	}

	// Only meaningful once enrichment has run: an unenriched repo has zero
	// contributors recorded because nobody asked, not because it has none.
	if r.Enriched && r.Contributors > 0 && r.Stars > 5000 {
		perContributor := float64(r.Stars) / float64(r.Contributors)
		if perContributor > 3000 {
			penalty *= 0.55
			extra := fmt.Sprintf("%.0f stars per contributor", perContributor)
			if why == "" {
				why = extra
			} else {
				why += ", " + extra
			}
		}
	}

	// Neither of the above on its own, but a popular repository with no
	// description, no topics and no licence has had no maintenance effort
	// put into it at all.
	if r.Stars > 5000 && r.Description == "" && len(r.Topics) == 0 && !r.HasLicense() {
		penalty *= 0.7
		extra := "no description, topics or licence"
		if why == "" {
			why = extra
		} else {
			why += ", " + extra
		}
	}

	return penalty, why
}
