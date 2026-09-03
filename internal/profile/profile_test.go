package profile

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/NishilRathod/gitscout/internal/github"
)

var now = time.Date(2026, 9, 3, 0, 0, 0, 0, time.UTC)

// fakeFetcher serves canned repositories and language byte counts.
type fakeFetcher struct {
	repos []github.Repo
	langs map[string]map[string]int
	err   error

	// languageCalls records which repos were asked about, so tests can assert
	// that forks are skipped rather than merely down-weighted.
	languageCalls []string
}

func (f *fakeFetcher) UserRepos(ctx context.Context, login string) ([]github.Repo, error) {
	return f.repos, f.err
}

func (f *fakeFetcher) RepoLanguages(ctx context.Context, fullName string) (map[string]int, error) {
	f.languageCalls = append(f.languageCalls, fullName)
	l, ok := f.langs[fullName]
	if !ok {
		return nil, errors.New("no such repo")
	}
	return l, nil
}

func repo(name string, fork bool, pushedDaysAgo int, topics ...string) github.Repo {
	return github.Repo{
		FullName: name,
		Fork:     fork,
		PushedAt: now.AddDate(0, 0, -pushedDaysAgo),
		Topics:   topics,
	}
}

func TestBuildWeightsAndNormalises(t *testing.T) {
	f := &fakeFetcher{
		repos: []github.Repo{
			repo("u/site", false, 1, "react", "portfolio"),
			repo("u/api", false, 1, "react"),
		},
		langs: map[string]map[string]int{
			"u/site": {"TypeScript": 200000, "CSS": 15000},
			"u/api":  {"Python": 50000},
		},
	}

	p, err := Build(context.Background(), f, "u", now)
	if err != nil {
		t.Fatal(err)
	}
	if p.Repos != 2 {
		t.Errorf("Repos = %d, want 2", p.Repos)
	}
	// The largest language normalises to exactly 1.
	if got := p.Weight("TypeScript"); got != 1 {
		t.Errorf("TypeScript weight = %v, want 1", got)
	}
	// Lookup is case-insensitive: callers pass GitHub's capitalisation.
	if p.Weight("typescript") != p.Weight("TypeScript") {
		t.Error("weight lookup should be case-insensitive")
	}
	if !p.Knows("Python") {
		t.Errorf("Python weight %v should clear the known threshold", p.Weight("Python"))
	}
	if p.Knows("Rust") {
		t.Error("Rust was never written and must not count as known")
	}
	if p.Topics["react"] != 2 || p.Topics["portfolio"] != 1 {
		t.Errorf("topics = %v", p.Topics)
	}
}

// Byte counts are log-scaled before normalising. Without that, one vendored or
// generated file would swamp every language the user actually wrote by hand.
func TestBuildLogScalesSoOneHugeFileCannotDominate(t *testing.T) {
	f := &fakeFetcher{
		repos: []github.Repo{repo("u/x", false, 1)},
		langs: map[string]map[string]int{
			"u/x": {"JavaScript": 5000000, "Go": 40000},
		},
	}
	p, err := Build(context.Background(), f, "u", now)
	if err != nil {
		t.Fatal(err)
	}
	// On a linear scale Go would score 0.008 and vanish. Log scaling keeps a
	// genuinely used language visible.
	if !p.Knows("Go") {
		t.Errorf("Go weight = %v, want it to survive a 125x byte difference", p.Weight("Go"))
	}
	if p.Weight("Go") >= p.Weight("JavaScript") {
		t.Error("the larger language should still rank higher")
	}
}

// A fork records what someone copied, not what they wrote.
func TestBuildIgnoresForks(t *testing.T) {
	f := &fakeFetcher{
		repos: []github.Repo{
			repo("u/mine", false, 1),
			repo("u/forked", true, 1, "rust"),
		},
		langs: map[string]map[string]int{
			"u/mine":   {"Go": 1000},
			"u/forked": {"Rust": 999999},
		},
	}
	p, err := Build(context.Background(), f, "u", now)
	if err != nil {
		t.Fatal(err)
	}
	if p.Repos != 1 {
		t.Errorf("Repos = %d, want 1", p.Repos)
	}
	if p.Knows("Rust") {
		t.Error("a forked repo's language must not count as experience")
	}
	if p.Topics["rust"] != 0 {
		t.Error("a forked repo's topics must not count as interest")
	}
	for _, c := range f.languageCalls {
		if c == "u/forked" {
			t.Error("forks should be skipped before costing a request")
		}
	}
}

func TestBuildDecaysOldWork(t *testing.T) {
	f := &fakeFetcher{
		repos: []github.Repo{
			repo("u/recent", false, 0),
			repo("u/ancient", false, 4*halfLifeDays),
		},
		langs: map[string]map[string]int{
			"u/recent":  {"Go": 100000},
			"u/ancient": {"Java": 100000},
		},
	}
	p, err := Build(context.Background(), f, "u", now)
	if err != nil {
		t.Fatal(err)
	}
	if p.Weight("Java") >= p.Weight("Go") {
		t.Errorf("equal bytes, but Java (old) %v should rank below Go (recent) %v",
			p.Weight("Java"), p.Weight("Go"))
	}
	// Decayed, but not erased: it is still real experience.
	if p.Weight("Java") <= 0 {
		t.Error("old work should still register")
	}
}

// One repo whose languages cannot be read must not sink the whole profile.
func TestBuildToleratesPerRepoFailure(t *testing.T) {
	f := &fakeFetcher{
		repos: []github.Repo{
			repo("u/good", false, 1),
			repo("u/broken", false, 1),
		},
		langs: map[string]map[string]int{
			"u/good": {"Go": 1000},
		},
	}
	p, err := Build(context.Background(), f, "u", now)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !p.Knows("Go") {
		t.Error("the readable repo should still have counted")
	}
}

func TestBuildPropagatesListFailure(t *testing.T) {
	f := &fakeFetcher{err: errors.New("boom")}
	if _, err := Build(context.Background(), f, "u", now); err == nil {
		t.Fatal("want error when the repo listing itself fails")
	}
}

func TestBuildEmptyAccount(t *testing.T) {
	p, err := Build(context.Background(), &fakeFetcher{}, "u", now)
	if err != nil {
		t.Fatal(err)
	}
	if len(p.Languages) != 0 || p.Weight("Go") != 0 {
		t.Errorf("empty account should yield an empty profile, got %v", p.Languages)
	}
	if p.Knows("Go") {
		t.Error("an empty profile knows nothing")
	}
}

func TestTopLanguagesOrdersByWeight(t *testing.T) {
	p := Profile{Languages: map[string]float64{"go": 0.4, "typescript": 1, "python": 0.6}}
	got := p.TopLanguages(2)
	if len(got) != 2 || got[0].Language != "typescript" || got[1].Language != "python" {
		t.Errorf("TopLanguages = %+v", got)
	}
	if len(p.TopLanguages(0)) != 3 {
		t.Error("n=0 should return everything")
	}
}

func TestUnfamiliarKeepsOnlyUnknownLanguages(t *testing.T) {
	p := Profile{Languages: map[string]float64{"typescript": 1, "python": 0.5, "css": 0.01}}
	got := p.Unfamiliar([]string{"Go", "TypeScript", "Rust", "CSS"})

	// CSS is present but below the threshold, so it is still unfamiliar.
	want := []string{"Go", "Rust", "CSS"}
	if len(got) != len(want) {
		t.Fatalf("Unfamiliar = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("Unfamiliar[%d] = %q, want %q", i, got[i], want[i])
		}
	}
}

// The sweep must contain both halves: something to contribute to now, and
// something that teaches him a new stack.
func TestSweepLanguagesMixesComfortAndStretch(t *testing.T) {
	p := Profile{Languages: map[string]float64{
		"typescript": 1, "python": 0.7, "java": 0.3, "javascript": 0.9,
	}}
	got := p.SweepLanguages([]string{"Go", "Rust", "TypeScript"}, 2, 2)

	want := []string{"TypeScript", "JavaScript", "Go", "Rust"}
	if len(got) != len(want) {
		t.Fatalf("SweepLanguages = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("[%d] = %q, want %q", i, got[i], want[i])
		}
	}
}

// TypeScript is in both the profile and the stretch pool. It must appear once,
// and the duplicate must not consume a stretch slot that a genuinely new
// language could have used.
func TestSweepLanguagesDoesNotDoubleCount(t *testing.T) {
	p := Profile{Languages: map[string]float64{"go": 1}}
	got := p.SweepLanguages([]string{"Go", "Rust", "Zig"}, 1, 2)

	want := []string{"Go", "Rust", "Zig"}
	if len(got) != len(want) {
		t.Fatalf("SweepLanguages = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("[%d] = %q, want %q", i, got[i], want[i])
		}
	}
}

// Language names go straight into GitHub's language: qualifier, which is picky
// about capitalisation.
func TestCanonicalRestoresGitHubCapitalisation(t *testing.T) {
	tests := map[string]string{
		"typescript":       "TypeScript",
		"javascript":       "JavaScript",
		"html":             "HTML",
		"c++":              "C++",
		"jupyter notebook": "Jupyter Notebook",
		"go":               "Go",
		"rust":             "Rust",
		"":                 "",
	}
	for in, want := range tests {
		if got := canonical(in); got != want {
			t.Errorf("canonical(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestDecayHandlesFutureAndZeroTimes(t *testing.T) {
	if got := decay(now.AddDate(0, 0, 5), now); got != 1 {
		t.Errorf("a future push should not earn extra weight, got %v", got)
	}
	if got := decay(time.Time{}, now); got != 0.5 {
		t.Errorf("an unknown push time should get a neutral weight, got %v", got)
	}
	if got := decay(now.AddDate(0, 0, -halfLifeDays), now); got < 0.49 || got > 0.51 {
		t.Errorf("one half-life should halve the weight, got %v", got)
	}
}
