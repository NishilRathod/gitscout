// Package score ranks discovered repositories along three independent axes.
//
// They are kept separate on purpose. A repository can be growing explosively
// and still be impossible to contribute to — ultraworkers/claw-code was
// measured at 195k stars and 108k forks with 23 contributors and one merged
// pull request in a month — and collapsing momentum and contributability into a
// single number would recommend exactly that repository the hardest.
//
// Every score carries the components that produced it, so a ranking can be
// argued with rather than taken on faith.
package score

import "math"

// Component is one named contribution to a score.
type Component struct {
	Name   string
	Value  float64 // the score contribution, already weighted
	Detail string  // the underlying measurement, for display
}

// Score is a value in [0, 1] plus the reasoning behind it.
type Score struct {
	Total      float64
	Components []Component

	// Measured distinguishes a score computed from observed history from one
	// derived from a proxy. Only momentum sets it; the digest uses it to
	// avoid claiming knowledge the tool does not have.
	Measured bool

	// Notes are caveats worth showing next to the number.
	Notes []string
}

func (s *Score) add(name string, value float64, detail string) {
	if value == 0 && detail == "" {
		return
	}
	s.Components = append(s.Components, Component{Name: name, Value: value, Detail: detail})
	s.Total += value
}

func (s *Score) note(n string) { s.Notes = append(s.Notes, n) }

// clamp confines a value to [0, 1].
func clamp(v float64) float64 {
	if v < 0 {
		return 0
	}
	if v > 1 {
		return 1
	}
	return v
}

// normalize maps a non-negative measurement onto [0, 1] with diminishing
// returns, so that ref scores 1 and values far past it do not run away with the
// ranking. Star counts and pull-request rates both span orders of magnitude;
// on a linear scale the largest repository would win every list by default.
func normalize(v, ref float64) float64 {
	if v <= 0 || ref <= 0 {
		return 0
	}
	return clamp(math.Log1p(v) / math.Log1p(ref))
}
