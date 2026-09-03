package gaps

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/NishilRathod/gitscout/internal/github"
	"github.com/NishilRathod/gitscout/internal/profile"
	"github.com/NishilRathod/gitscout/internal/score"
	"github.com/NishilRathod/gitscout/internal/store"
)

var now = time.Date(2026, 9, 3, 0, 0, 0, 0, time.UTC)

func nishil() profile.Profile {
	return profile.Profile{
		Login:     "NishilRathod",
		Languages: map[string]float64{"typescript": 1, "javascript": 0.9, "python": 0.7, "java": 0.4},
		Topics:    map[string]int{"react": 2},
	}
}

// theFuck and mkcert are both real, both measured against the live API on
// 2026-09-03, and both exactly what this list is for: projects with tens of
// thousands of users that nobody has pushed to in over two years.
func theFuck() github.Repo {
	r := github.Repo{
		ID: 27083582, FullName: "nvbn/thefuck", Language: "Python",
		Stars: 97763, Forks: 3900, OpenIssues: 300,
		PushedAt: time.Date(2024, 7, 19, 0, 0, 0, 0, time.UTC),
		HTMLURL:  "https://github.com/nvbn/thefuck",
		Slices:   []string{github.SliceAbandoned},
	}
	r.License.SPDXID = "MIT"
	return r
}

func mkcert() github.Repo {
	r := github.Repo{
		ID: 132464395, FullName: "FiloSottile/mkcert", Language: "Go",
		Stars: 59540, Forks: 2900, OpenIssues: 180,
		PushedAt: time.Date(2024, 8, 13, 0, 0, 0, 0, time.UTC),
		HTMLURL:  "https://github.com/FiloSottile/mkcert",
		Slices:   []string{github.SliceAbandoned},
	}
	r.License.SPDXID = "BSD-3-Clause"
	return r
}

func rising(name, lang string, stars int, topics ...string) github.Repo {
	r := github.Repo{
		FullName: name, Language: lang, Stars: stars, Topics: topics,
		CreatedAt: now.AddDate(0, 0, -200), PushedAt: now.AddDate(0, 0, -1),
		Slices: []string{github.SliceRising + ":" + strings.ToLower(lang)},
	}
	r.License.SPDXID = "MIT"
	return r
}

func candidates(repos ...github.Repo) []score.Candidate {
	return score.Evaluate(repos, nishil(), store.History{}, now)
}

// The plan's named regression case: both known-abandoned projects must appear.
func TestAbandonedFindsTheKnownDeadProjects(t *testing.T) {
	got := Abandoned(candidates(theFuck(), mkcert(), rising("acme/live", "Go", 5000)), now, 10)

	if len(got) != 2 {
		t.Fatalf("got %d signals, want 2: %v", len(got), titles(got))
	}
	// thefuck has more stars and has been dead longer, so it leads.
	if got[0].Title != "nvbn/thefuck" || got[1].Title != "FiloSottile/mkcert" {
		t.Errorf("order = %v", titles(got))
	}
	for _, s := range got {
		if s.Kind != KindAbandoned {
			t.Errorf("%s kind = %q", s.Title, s.Kind)
		}
		if s.URL == "" {
			t.Errorf("%s has no URL to follow", s.Title)
		}
	}
}

// A healthy project must never be filed as abandoned, however few stars it has.
func TestAbandonedIgnoresLiveProjects(t *testing.T) {
	if got := Abandoned(candidates(rising("acme/live", "Go", 90000)), now, 10); len(got) != 0 {
		t.Errorf("got %v, want nothing", titles(got))
	}
}

// The evidence has to be checkable, and a missing licence is the one thing that
// can make forking a dead project a bad idea.
func TestAbandonedEvidenceFlagsMissingLicence(t *testing.T) {
	r := theFuck()
	r.License.SPDXID = ""

	got := Abandoned(candidates(r), now, 10)
	if len(got) != 1 {
		t.Fatalf("got %d signals", len(got))
	}
	if !evidenceMentions(got[0], "no clear licence") {
		t.Errorf("evidence = %v", got[0].Evidence)
	}
	if !evidenceMentions(got[0], "no push in") {
		t.Errorf("evidence should quantify the silence: %v", got[0].Evidence)
	}
}

func TestStretchStacksSkipsLanguagesHeAlreadyKnows(t *testing.T) {
	repos := []github.Repo{
		rising("a/one", "TypeScript", 5000),
		rising("a/two", "TypeScript", 4000),
		rising("b/one", "Rust", 3000),
		rising("b/two", "Rust", 2000),
	}
	got := StretchStacks(repos, nishil(), 10)

	if len(got) != 1 || got[0].Title != "Rust" {
		t.Fatalf("got %v, want only Rust", titles(got))
	}
	if !evidenceMentions(got[0], "absent from your profile") {
		t.Errorf("evidence = %v", got[0].Evidence)
	}
}

// One repository in a language is an anecdote. The list claims to show trends.
func TestStretchStacksNeedsMoreThanOneRepo(t *testing.T) {
	got := StretchStacks([]github.Repo{rising("a/only", "Zig", 90000)}, nishil(), 10)
	if len(got) != 0 {
		t.Errorf("got %v, want nothing from a single repo", titles(got))
	}
}

// Dead projects must not make a language look lively.
func TestStretchStacksIgnoresAbandonedRepos(t *testing.T) {
	dead1, dead2 := mkcert(), mkcert()
	dead2.FullName = "other/dead"

	if got := StretchStacks([]github.Repo{dead1, dead2}, nishil(), 10); len(got) != 0 {
		t.Errorf("got %v, want nothing — those repos are abandoned, not rising", titles(got))
	}
}

func TestStretchStacksRanksBusierStacksHigher(t *testing.T) {
	repos := []github.Repo{
		rising("a/1", "Rust", 50000), rising("a/2", "Rust", 40000), rising("a/3", "Rust", 30000),
		rising("b/1", "Zig", 300), rising("b/2", "Zig", 200),
	}
	got := StretchStacks(repos, nishil(), 10)
	if len(got) != 2 || got[0].Title != "Rust" {
		t.Errorf("order = %v, want Rust first", titles(got))
	}
}

// --- ecosystem holes --------------------------------------------------------

type fakeCounter struct {
	counts  map[string]int
	queries []string
	err     error
}

func (f *fakeCounter) CountRepos(ctx context.Context, q string) (int, error) {
	f.queries = append(f.queries, q)
	if f.err != nil {
		return 0, f.err
	}
	for frag, n := range f.counts {
		if strings.Contains(q, frag) {
			return n, nil
		}
	}
	return 0, nil
}

func hotRepos() []github.Repo {
	return []github.Repo{
		rising("a/1", "Rust", 40000, "wasm"),
		rising("a/2", "Rust", 30000, "wasm"),
		rising("a/3", "Go", 20000, "wasm"),
	}
}

func TestEcosystemHolesReportsOnlyMissingCompanions(t *testing.T) {
	c := &fakeCounter{counts: map[string]int{
		// A CLI plainly exists; an editor extension does not.
		"cli OR command-line": 40,
	}}
	cfg := EcosystemHolesConfig{Topics: 1, ExistsThreshold: 2, MinTopicRepos: 3}

	rep := EcosystemHoles(context.Background(), c, hotRepos(), cfg)
	got := rep.Signals
	if len(rep.Errors) != 0 {
		t.Fatalf("errs = %v", rep.Errors)
	}
	if len(got) != 2 {
		t.Fatalf("got %d signals, want the two absent companions: %v", len(got), titles(got))
	}
	for _, s := range got {
		if strings.Contains(s.Title, "command-line") {
			t.Errorf("reported a hole for a companion that exists: %q", s.Title)
		}
	}
}

// The noisiest signal in the tool must say so on every single row.
func TestEcosystemHolesAlwaysLabelItselfWeak(t *testing.T) {
	got := EcosystemHoles(context.Background(), &fakeCounter{}, hotRepos(),
		EcosystemHolesConfig{Topics: 1, ExistsThreshold: 2, MinTopicRepos: 3}).Signals

	if len(got) == 0 {
		t.Fatal("expected signals")
	}
	for _, s := range got {
		if !evidenceMentions(s, "weak signal") {
			t.Errorf("%q carries no caveat: %v", s.Title, s.Evidence)
		}
		if !evidenceMentions(s, "rising repos tagged") {
			t.Errorf("%q does not show what made the topic hot: %v", s.Title, s.Evidence)
		}
	}
}

// This is the only pass that spends search budget outside discovery, so its
// cost must stay proportional to its configuration.
func TestEcosystemHolesRespectsItsBudget(t *testing.T) {
	c := &fakeCounter{}
	repos := append(hotRepos(),
		rising("b/1", "Go", 9000, "database"),
		rising("b/2", "Go", 8000, "database"),
		rising("b/3", "Go", 7000, "database"),
		rising("c/1", "Go", 100, "rare"),
	)

	rep := EcosystemHoles(context.Background(), c, repos,
		EcosystemHolesConfig{Topics: 1, ExistsThreshold: 2, MinTopicRepos: 3})
	if len(rep.Errors) != 0 {
		t.Fatal(rep.Errors)
	}
	if rep.QueriesRun != 3 {
		t.Errorf("QueriesRun = %d, want 3", rep.QueriesRun)
	}
	// One topic, three companions.
	if len(c.queries) != 3 {
		t.Errorf("made %d queries, want 3: %v", len(c.queries), c.queries)
	}
	// The busiest topic is the one probed.
	for _, q := range c.queries {
		if !strings.Contains(q, "topic:wasm") {
			t.Errorf("probed %q, want the hottest topic", q)
		}
	}
}

// A topic only a couple of repos share is not evidence of an ecosystem.
func TestEcosystemHolesSkipsThinTopics(t *testing.T) {
	c := &fakeCounter{}
	rep := EcosystemHoles(context.Background(), c,
		[]github.Repo{rising("a/1", "Rust", 40000, "obscure")},
		EcosystemHolesConfig{Topics: 4, ExistsThreshold: 2, MinTopicRepos: 3})

	if len(c.queries) != 0 {
		t.Errorf("probed %v, want nothing", c.queries)
	}
	// An empty result with nothing probed must be distinguishable from an
	// empty result meaning every companion already exists.
	if len(rep.TopicsProbed) != 0 || rep.QueriesRun != 0 {
		t.Errorf("report claims %d topics and %d queries, want none",
			len(rep.TopicsProbed), rep.QueriesRun)
	}
}

// A failed probe costs one row, not the run.
func TestEcosystemHolesSurvivesQueryFailure(t *testing.T) {
	c := &fakeCounter{err: errors.New("rate limited")}
	rep := EcosystemHoles(context.Background(), c, hotRepos(),
		EcosystemHolesConfig{Topics: 1, ExistsThreshold: 2, MinTopicRepos: 3})

	if len(rep.Signals) != 0 {
		t.Errorf("got %v, want no signals when every probe failed", titles(rep.Signals))
	}
	if len(rep.Errors) != 3 {
		t.Errorf("got %d errors, want one per failed probe", len(rep.Errors))
	}
	// The probes ran; they failed. That is not the same as finding nothing.
	if rep.QueriesRun != 3 {
		t.Errorf("QueriesRun = %d, want 3", rep.QueriesRun)
	}
}

func TestHumanCount(t *testing.T) {
	tests := map[int]string{999: "999", 1500: "1.5k", 97763: "97.8k", 2400000: "2.4M"}
	for in, want := range tests {
		if got := humanCount(in); got != want {
			t.Errorf("humanCount(%d) = %q, want %q", in, got, want)
		}
	}
}

func titles(s []Signal) []string {
	out := make([]string, len(s))
	for i, x := range s {
		out[i] = x.Title
	}
	return out
}

func evidenceMentions(s Signal, substr string) bool {
	for _, e := range s.Evidence {
		if strings.Contains(e, substr) {
			return true
		}
	}
	return false
}
