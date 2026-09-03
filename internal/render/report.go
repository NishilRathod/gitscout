// Package render turns a scored run into the two things a person reads: a
// table in the terminal and a dated Markdown digest.
//
// Both show the components behind every ranking. A recommendation engine whose
// reasoning cannot be inspected is one that has to be trusted, and there is no
// reason to trust this one — it is heuristics over public metadata. Showing the
// arithmetic is what makes its output arguable, which is the most useful thing
// it can be.
package render

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/NishilRathod/gitscout/internal/gaps"
	"github.com/NishilRathod/gitscout/internal/github"
	"github.com/NishilRathod/gitscout/internal/profile"
	"github.com/NishilRathod/gitscout/internal/score"
)

// Report is everything one run produced.
type Report struct {
	GeneratedAt time.Time
	Profile     profile.Profile

	Contribute []score.Candidate
	Stretch    []score.Candidate

	Abandoned []gaps.Signal
	Stacks    []gaps.Signal
	Holes     []gaps.Signal

	// Run statistics, reported so the digest can be read sceptically.
	Discovered   int
	EnrichedRepo int
	HistoryRuns  int
	Measured     int
	Budgets      map[string]github.Budget
	Errors       []string
}

// HistoryNote states plainly what the momentum numbers are worth. It is the
// single most important line in the digest: on a first run every rate is a
// lifetime average dressed up as a trend, and saying so is the difference
// between a useful tool and a misleading one.
func (r Report) HistoryNote() string {
	switch {
	case r.HistoryRuns == 0:
		return "First run — no history to compare against. Every momentum figure below is stars divided by age, which flatters anything that launched loudly and then stalled. Run this again in a week for real rates, and once more for acceleration."
	case r.HistoryRuns == 1:
		return "One earlier run on record. Rates below are measured where a repository was seen before, and a lifetime average where it is new. Acceleration needs a third run."
	case r.Measured == 0:
		return fmt.Sprintf("%d earlier runs on record, but none of today's candidates appeared in them, so every figure below is a lifetime average rather than a measured rate.", r.HistoryRuns)
	default:
		return fmt.Sprintf("%d earlier runs on record. %d of today's candidates have measured growth rates; the rest are new and show a lifetime average instead.", r.HistoryRuns, r.Measured)
	}
}

// reason summarises why a candidate ranked where it did, strongest components
// first.
func reason(c score.Candidate, max int) string {
	type part struct {
		v float64
		s string
	}
	var parts []part

	collect := func(s score.Score) {
		for _, comp := range s.Components {
			if comp.Detail == "" || comp.Value <= 0 {
				continue
			}
			parts = append(parts, part{comp.Value, comp.Detail})
		}
	}
	collect(c.Contributability)
	collect(c.Momentum)

	sort.SliceStable(parts, func(i, j int) bool { return parts[i].v > parts[j].v })
	if len(parts) > max {
		parts = parts[:max]
	}

	out := make([]string, len(parts))
	for i, p := range parts {
		out[i] = p.s
	}
	return strings.Join(out, "; ")
}

// caveats gathers the notes attached to a candidate's scores.
func caveats(c score.Candidate) []string {
	var out []string
	out = append(out, c.Momentum.Notes...)
	out = append(out, c.Contributability.Notes...)
	return out
}

func humanCount(n int) string {
	switch {
	case n >= 1000000:
		return fmt.Sprintf("%.1fM", float64(n)/1000000)
	case n >= 1000:
		return fmt.Sprintf("%.1fk", float64(n)/1000)
	default:
		return fmt.Sprint(n)
	}
}

func languageOr(s, fallback string) string {
	if s == "" {
		return fallback
	}
	return s
}
