// Package profile turns a GitHub account's public repositories into a picture
// of what its owner has actually written. That picture drives two opposite
// judgements in scoring: which projects they could contribute to today, and
// which would stretch them into something new.
package profile

import (
	"context"
	"math"
	"sort"
	"strings"
	"time"

	"github.com/NishilRathod/gitscout/internal/github"
)

// knownThreshold is the normalised weight above which a language counts as one
// the user knows. It exists to stop incidental files — a stray Dockerfile, the
// CSS that ships with a template — from being read as experience.
const knownThreshold = 0.05

// halfLifeDays is how quickly language experience decays. Something written
// three years ago is genuine experience but a weaker guide to what someone
// wants to work on now than something written last month.
const halfLifeDays = 540

// Fetcher is the slice of the GitHub client this package needs. Depending on an
// interface rather than the concrete client keeps the tests offline.
type Fetcher interface {
	UserRepos(ctx context.Context, login string) ([]github.Repo, error)
	RepoLanguages(ctx context.Context, fullName string) (map[string]int, error)
}

// Profile is the weighted summary of one account's public work.
type Profile struct {
	Login string

	// Languages maps a lower-cased language name to a weight in (0, 1],
	// normalised so the most-used language scores 1.
	Languages map[string]float64

	// Topics counts the repository topics the user has applied to their own
	// work — a rough read on subject-matter interest.
	Topics map[string]int

	// Repos is how many non-fork public repositories fed the profile.
	Repos int
}

// LangWeight pairs a language with its weight, for ordered output.
type LangWeight struct {
	Language string
	Weight   float64
}

// Build assembles a profile from an account's public repositories.
//
// Forks are excluded: a fork records what someone copied, not what they wrote,
// and counting them would credit the user with every language in every project
// they ever glanced at.
func Build(ctx context.Context, f Fetcher, login string, now time.Time) (Profile, error) {
	repos, err := f.UserRepos(ctx, login)
	if err != nil {
		return Profile{}, err
	}

	p := Profile{
		Login:     login,
		Languages: map[string]float64{},
		Topics:    map[string]int{},
	}

	raw := map[string]float64{}
	for _, r := range repos {
		if r.Fork {
			continue
		}
		p.Repos++

		for _, t := range r.Topics {
			p.Topics[strings.ToLower(t)]++
		}

		langs, err := f.RepoLanguages(ctx, r.FullName)
		if err != nil {
			// One unreadable repo should not sink the profile; the rest
			// still describe the user.
			continue
		}

		recency := decay(r.PushedAt, now)
		for lang, bytes := range langs {
			raw[strings.ToLower(lang)] += float64(bytes) * recency
		}
	}

	// Byte counts span orders of magnitude — a vendored lockfile can dwarf a
	// year of handwritten code — so weights are taken on a log scale before
	// normalising. Without it a single large generated file would decide the
	// whole profile.
	var max float64
	scaled := map[string]float64{}
	for lang, v := range raw {
		s := math.Log1p(v)
		scaled[lang] = s
		if s > max {
			max = s
		}
	}
	if max > 0 {
		for lang, s := range scaled {
			p.Languages[lang] = s / max
		}
	}
	return p, nil
}

// decay weights a repository by how recently it was touched, halving every
// halfLifeDays. A repo pushed in the future — clock skew, or a backdated import
// — is treated as current rather than given extra weight.
func decay(pushed, now time.Time) float64 {
	if pushed.IsZero() {
		return 0.5
	}
	days := now.Sub(pushed).Hours() / 24
	if days <= 0 {
		return 1
	}
	return math.Pow(0.5, days/halfLifeDays)
}

// Weight returns how strongly the user is associated with a language, from 0
// (never written) to 1 (their primary language).
func (p Profile) Weight(lang string) float64 {
	if lang == "" {
		return 0
	}
	return p.Languages[strings.ToLower(lang)]
}

// Knows reports whether the language is one the user has meaningful experience
// in, as opposed to one that merely appears somewhere in their repositories.
func (p Profile) Knows(lang string) bool {
	return p.Weight(lang) >= knownThreshold
}

// TopLanguages returns the n most-used languages, strongest first.
func (p Profile) TopLanguages(n int) []LangWeight {
	out := make([]LangWeight, 0, len(p.Languages))
	for l, w := range p.Languages {
		out = append(out, LangWeight{Language: l, Weight: w})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Weight != out[j].Weight {
			return out[i].Weight > out[j].Weight
		}
		return out[i].Language < out[j].Language
	})
	if n > 0 && len(out) > n {
		out = out[:n]
	}
	return out
}

// Unfamiliar returns the subset of candidates the user has not meaningfully
// worked in, preserving the order given. This is the stretch list: the
// languages where a project would add something new to their portfolio rather
// than another instance of what they already have.
func (p Profile) Unfamiliar(candidates []string) []string {
	var out []string
	for _, c := range candidates {
		if !p.Knows(c) {
			out = append(out, c)
		}
	}
	return out
}

// SweepLanguages picks the languages discovery should sweep: the user's own top
// languages, so there is something they can contribute to today, plus unfamiliar
// candidates from the stretch pool, so there is something that grows them.
//
// Both halves matter. Sweeping only what they know produces a list with no
// ambition; sweeping only what they do not produces one they cannot act on this
// week.
func (p Profile) SweepLanguages(stretchPool []string, comfort, stretch int) []string {
	seen := map[string]bool{}
	var out []string

	add := func(lang string) bool {
		key := strings.ToLower(lang)
		if lang == "" || seen[key] {
			return false
		}
		seen[key] = true
		out = append(out, lang)
		return true
	}

	for _, lw := range p.TopLanguages(0) {
		if len(out) >= comfort {
			break
		}
		add(canonical(lw.Language))
	}

	added := 0
	for _, c := range p.Unfamiliar(stretchPool) {
		if added >= stretch {
			break
		}
		if add(c) {
			added++
		}
	}
	return out
}

// canonicalNames restores the capitalisation GitHub's language: qualifier
// expects, for the languages a profile is most likely to contain. Anything not
// listed is title-cased, which is right for the long tail of single-word names.
var canonicalNames = map[string]string{
	"javascript":       "JavaScript",
	"typescript":       "TypeScript",
	"html":             "HTML",
	"css":              "CSS",
	"scss":             "SCSS",
	"php":              "PHP",
	"c++":              "C++",
	"c#":               "C#",
	"objective-c":      "Objective-C",
	"shell":            "Shell",
	"jupyter notebook": "Jupyter Notebook",
	"vim script":       "Vim Script",
}

func canonical(lower string) string {
	if v, ok := canonicalNames[lower]; ok {
		return v
	}
	if lower == "" {
		return ""
	}
	return strings.ToUpper(lower[:1]) + lower[1:]
}

// DefaultStretchPool is the set of languages considered worth growing into:
// widely used in serious open source, and distinct enough from web-application
// JavaScript that working in one teaches something new.
//
// It is a starting point, not a verdict — the --languages flag overrides it
// entirely.
var DefaultStretchPool = []string{"Go", "Rust", "Zig", "Elixir", "Kotlin", "Swift", "Scala", "Haskell", "OCaml", "C"}
