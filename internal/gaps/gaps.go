// Package gaps looks for openings to build something rather than contribute to
// something.
//
// A caveat belongs at the top of this file, and it is repeated in the digest:
// these are heuristics over public metadata, not judgements about whether an
// idea is worth pursuing. The tool can show that a well-loved project has not
// been touched in two years, or that a hot topic has no command-line client. It
// cannot tell whether that is an opportunity or a dead end — that part is the
// reader's, and the evidence is printed alongside every signal so it can be
// checked rather than trusted.
package gaps

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/NishilRathod/gitscout/internal/github"
	"github.com/NishilRathod/gitscout/internal/profile"
	"github.com/NishilRathod/gitscout/internal/score"
)

// Kind distinguishes the three signals, which need different reading.
type Kind string

const (
	// KindAbandoned is the strongest of the three: a project people
	// demonstrably wanted, with unanswered requests for help, that nobody has
	// pushed to in a long time.
	KindAbandoned Kind = "abandoned"

	// KindStretchStack is a language absent from the user's profile that is
	// drawing unusual activity — a suggestion about what to learn, not what
	// to build.
	KindStretchStack Kind = "stretch-stack"

	// KindEcosystemHole is the weakest and noisiest: an active topic with no
	// repository matching an expected companion tool. Often the companion
	// exists under a name the query did not guess.
	KindEcosystemHole Kind = "ecosystem-hole"
)

// Signal is one opportunity, with the evidence that produced it.
type Signal struct {
	Kind     Kind
	Title    string
	Evidence []string
	URL      string
	Score    float64
}

// Abandoned finds well-loved projects that have gone quiet with open requests
// for help. Reviving one, or building its replacement, is a portfolio project
// with a proven audience — the hardest part of open source, having users, is
// already done.
func Abandoned(cands []score.Candidate, now time.Time, limit int) []Signal {
	var out []Signal
	for _, c := range cands {
		r := c.Repo
		if !hasSlice(r, github.SliceAbandoned) {
			continue
		}
		stale := r.StaleDays(now)

		ev := []string{
			fmt.Sprintf("%s stars, %d forks", humanCount(r.Stars), r.Forks),
			fmt.Sprintf("no push in %.0f days (last %s)", stale, r.PushedAt.Format("Jan 2006")),
			fmt.Sprintf("%d open issues", r.OpenIssues),
		}
		if r.Archived {
			ev = append(ev, "archived by its owner")
		}
		if r.HasLicense() {
			ev = append(ev, "licence "+r.License.SPDXID)
		} else {
			ev = append(ev, "no clear licence — check before forking")
		}

		// Weight by how many people cared and how long it has been left.
		// Both are capped so one enormous relic cannot crowd out the list.
		out = append(out, Signal{
			Kind:     KindAbandoned,
			Title:    r.FullName,
			Evidence: ev,
			URL:      r.HTMLURL,
			Score:    0.7*norm(float64(r.Stars), 50000) + 0.3*norm(stale, 1095),
		})
	}
	return trim(out, limit)
}

// StretchStacks reports which languages the user has never worked in are
// drawing the most activity among the repositories this run discovered.
//
// It answers "what should I learn next" with numbers instead of opinion. It is
// bounded by what discovery swept, so it describes the sample rather than all
// of GitHub — which is why the evidence names the sample size.
func StretchStacks(repos []github.Repo, p profile.Profile, limit int) []Signal {
	type agg struct {
		repos int
		stars int
		top   string
	}
	byLang := map[string]*agg{}

	for _, r := range repos {
		if r.Language == "" || p.Knows(r.Language) {
			continue
		}
		// Only count repositories found by a rising slice: the abandoned
		// slice would otherwise make dead languages look lively.
		if !hasSlicePrefix(r, github.SliceRising) {
			continue
		}
		a := byLang[r.Language]
		if a == nil {
			a = &agg{}
			byLang[r.Language] = a
		}
		a.repos++
		a.stars += r.Stars
		if a.top == "" {
			a.top = r.FullName
		}
	}

	var out []Signal
	for lang, a := range byLang {
		if a.repos < 2 {
			// One repository is an anecdote, not a trend.
			continue
		}
		out = append(out, Signal{
			Kind:  KindStretchStack,
			Title: lang,
			Evidence: []string{
				fmt.Sprintf("%d rising repos in this run, %s stars between them", a.repos, humanCount(a.stars)),
				"busiest: " + a.top,
				"absent from your profile",
			},
			Score: norm(float64(a.stars), 200000)*0.6 + norm(float64(a.repos), 25)*0.4,
		})
	}
	return trim(out, limit)
}

// Counter is the slice of the GitHub client the ecosystem-hole pass needs.
type Counter interface {
	CountRepos(ctx context.Context, query string) (int, error)
}

// companions are the tools a healthy ecosystem tends to grow around itself. A
// topic with plenty of activity and none of these is either an opening or, more
// often, a topic whose companion is named something the query did not guess.
var companions = []struct {
	label string
	terms string
}{
	{"a command-line client", "cli OR command-line"},
	{"an editor extension", "vscode OR neovim OR intellij"},
	{"a Go or Rust SDK", "sdk OR client"},
}

// EcosystemHolesConfig bounds the pass, which is the only part of gitscout that
// spends search requests outside discovery.
type EcosystemHolesConfig struct {
	// Topics is how many of the hottest topics to probe.
	Topics int
	// ExistsThreshold is the match count at or below which a companion is
	// treated as missing.
	ExistsThreshold int
	// MinTopicRepos is how many rising repositories must share a topic before
	// it is worth probing at all.
	MinTopicRepos int
}

// DefaultEcosystemHolesConfig probes four topics, costing twelve search
// requests — roughly 25 seconds at the paced rate.
func DefaultEcosystemHolesConfig() EcosystemHolesConfig {
	return EcosystemHolesConfig{Topics: 4, ExistsThreshold: 2, MinTopicRepos: 3}
}

// HoleReport is the result of the ecosystem-hole pass, including what it
// actually did.
//
// The counts matter because an empty Signals list is ambiguous on its own: it
// can mean the probes ran and every companion already exists, or that no topic
// was popular enough to probe at all. Those are different findings and the
// caller has no other way to tell them apart.
type HoleReport struct {
	Signals      []Signal
	TopicsProbed []string
	QueriesRun   int
	Errors       []error
}

// EcosystemHoles probes the hottest topics for missing companion tools.
//
// This is the weakest signal the tool produces and it is labelled as such
// everywhere it appears. Errors are collected rather than fatal: a failed probe
// costs one line of the digest, not the run.
func EcosystemHoles(ctx context.Context, c Counter, repos []github.Repo, cfg EcosystemHolesConfig) HoleReport {
	topics := hotTopics(repos, cfg.MinTopicRepos, cfg.Topics)

	var (
		out  []Signal
		errs []error
		rep  HoleReport
	)
	for _, t := range topics {
		rep.TopicsProbed = append(rep.TopicsProbed, t.name)
		for _, comp := range companions {
			q := fmt.Sprintf("topic:%s %s stars:>=25", t.name, comp.terms)
			rep.QueriesRun++
			n, err := c.CountRepos(ctx, q)
			if err != nil {
				errs = append(errs, err)
				continue
			}
			if n > cfg.ExistsThreshold {
				continue
			}
			out = append(out, Signal{
				Kind:  KindEcosystemHole,
				Title: fmt.Sprintf("%s appears to have no %s", t.name, comp.label),
				Evidence: []string{
					fmt.Sprintf("%d rising repos tagged %q, %s stars between them", t.repos, t.name, humanCount(t.stars)),
					fmt.Sprintf("only %d repos match %q", n, q),
					"weak signal: the companion may exist under a name this query did not guess",
				},
				Score: norm(float64(t.stars), 100000),
			})
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Score > out[j].Score })
	rep.Signals = out
	rep.Errors = errs
	return rep
}

type topicStat struct {
	name  string
	repos int
	stars int
}

func hotTopics(repos []github.Repo, minRepos, limit int) []topicStat {
	byTopic := map[string]*topicStat{}
	for _, r := range repos {
		if !hasSlicePrefix(r, github.SliceRising) {
			continue
		}
		for _, t := range r.Topics {
			t = strings.ToLower(t)
			s := byTopic[t]
			if s == nil {
				s = &topicStat{name: t}
				byTopic[t] = s
			}
			s.repos++
			s.stars += r.Stars
		}
	}

	var out []topicStat
	for _, s := range byTopic {
		if s.repos >= minRepos {
			out = append(out, *s)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].stars != out[j].stars {
			return out[i].stars > out[j].stars
		}
		return out[i].name < out[j].name
	})
	if limit > 0 && len(out) > limit {
		out = out[:limit]
	}
	return out
}

func hasSlice(r github.Repo, name string) bool {
	for _, s := range r.Slices {
		if s == name {
			return true
		}
	}
	return false
}

func hasSlicePrefix(r github.Repo, prefix string) bool {
	for _, s := range r.Slices {
		if strings.HasPrefix(s, prefix) {
			return true
		}
	}
	return false
}

func trim(s []Signal, limit int) []Signal {
	sort.SliceStable(s, func(i, j int) bool {
		if s[i].Score != s[j].Score {
			return s[i].Score > s[j].Score
		}
		return s[i].Title < s[j].Title
	})
	if limit > 0 && len(s) > limit {
		s = s[:limit]
	}
	return s
}

func norm(v, ref float64) float64 {
	if v <= 0 {
		return 0
	}
	if v >= ref {
		return 1
	}
	return v / ref
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
