package score

import (
	"strings"
	"testing"
	"time"

	"github.com/NishilRathod/gitscout/internal/github"
	"github.com/NishilRathod/gitscout/internal/profile"
	"github.com/NishilRathod/gitscout/internal/store"
)

var now = time.Date(2026, 9, 3, 0, 0, 0, 0, time.UTC)

func lic(r github.Repo, spdx string) github.Repo {
	r.License.SPDXID = spdx
	return r
}

// clawCode reproduces ultraworkers/claw-code as measured against the live API
// on 2026-09-03: enormous stars and forks, almost no contributors, no topics.
// The tool exists to *not* recommend this.
func clawCode() github.Repo {
	return github.Repo{
		ID:           1197021090,
		FullName:     "ultraworkers/claw-code",
		Language:     "Rust",
		Stars:        195165,
		Forks:        108752,
		OpenIssues:   41,
		Topics:       nil,
		CreatedAt:    now.AddDate(0, 0, -156),
		PushedAt:     now.AddDate(0, 0, -18),
		Contributors: 23,
		MergedPRs30d: 1,
		MergedPRs90d: 1,
		PRAuthors90d: 1,
		Enriched:     true,
		Slices:       []string{"rising:rust"},
	}
}

// theFuck reproduces nvbn/thefuck: a genuinely loved project with 97k stars
// that nobody has pushed to since July 2024.
func theFuck() github.Repo {
	r := github.Repo{
		ID:        27083582,
		FullName:  "nvbn/thefuck",
		Language:  "Python",
		Stars:     97763,
		Forks:     3900,
		Topics:    []string{"cli", "python", "shell"},
		CreatedAt: now.AddDate(-12, 0, 0),
		PushedAt:  time.Date(2024, 7, 19, 0, 0, 0, 0, time.UTC),
		Slices:    []string{github.SliceAbandoned},
	}
	return lic(r, "MIT")
}

// healthy is an ordinary, well-run project: growing steadily and merging work
// from many different people.
func healthy() github.Repo {
	r := github.Repo{
		ID:              42,
		FullName:        "acme/toolkit",
		Description:     "A well-run toolkit",
		Language:        "Go",
		Stars:           4200,
		Forks:           310,
		Topics:          []string{"cli", "devtools"},
		CreatedAt:       now.AddDate(0, 0, -300),
		PushedAt:        now.AddDate(0, 0, -2),
		Contributors:    140,
		MergedPRs30d:    18,
		MergedPRs90d:    52,
		PRAuthors90d:    21,
		HasContributing: true,
		Enriched:        true,
		Slices:          []string{github.SliceGoodFirst, "rising:go"},
	}
	return lic(r, "Apache-2.0")
}

// --- Momentum ---------------------------------------------------------------

// The headline honesty requirement: with no history the score must not claim to
// have measured anything.
func TestMomentumFirstRunIsLabelledAProxy(t *testing.T) {
	s := Momentum(healthy(), store.Trend{}, now)
	if s.Measured {
		t.Error("a first run has measured nothing")
	}
	if !hasNote(s, "no history yet") {
		t.Errorf("notes = %v, want the proxy called out", s.Notes)
	}
	if !hasComponent(s, "rate (proxy)") {
		t.Errorf("components = %+v, want the proxy named", s.Components)
	}
}

func TestMomentumUsesMeasuredRateWhenHistoryExists(t *testing.T) {
	tr := store.Trend{HasRate: true, StarsPerDay: 40, SpanDays: 7, Points: 2}
	s := Momentum(healthy(), tr, now)

	if !s.Measured {
		t.Error("with history the rate is measured")
	}
	if hasNote(s, "no history yet") {
		t.Error("should not claim a proxy when it measured")
	}
	if !hasComponent(s, "rate") {
		t.Errorf("components = %+v", s.Components)
	}
}

// Accelerating growth is what the tool was asked to find, so it must outscore
// the same rate holding steady.
func TestMomentumRewardsAcceleration(t *testing.T) {
	steady := store.Trend{HasRate: true, StarsPerDay: 20, SpanDays: 7, Points: 2}
	rising := store.Trend{
		HasRate: true, StarsPerDay: 20, SpanDays: 21, Points: 3,
		HasAcceleration: true, PrevStarsPerDay: 5, Acceleration: 15,
	}
	slowing := store.Trend{
		HasRate: true, StarsPerDay: 20, SpanDays: 21, Points: 3,
		HasAcceleration: true, PrevStarsPerDay: 60, Acceleration: -40,
	}

	r := healthy()
	a, b, c := Momentum(r, rising, now).Total, Momentum(r, steady, now).Total, Momentum(r, slowing, now).Total
	if !(a > b) {
		t.Errorf("accelerating %.3f should beat unknown-trend %.3f", a, b)
	}
	if b != c {
		t.Errorf("a slowing repo gets no bonus, so %.3f should equal %.3f", c, b)
	}
	if !hasNote(Momentum(r, slowing, now), "slowing") {
		t.Error("deceleration should be called out in the notes")
	}
}

// A repository that spiked on a launch post and was then abandoned is not
// gaining momentum, whatever its lifetime average says.
func TestMomentumDiscountsStaleRepos(t *testing.T) {
	fresh := healthy()
	stale := healthy()
	stale.PushedAt = now.AddDate(0, 0, -200)

	f := Momentum(fresh, store.Trend{}, now).Total
	s := Momentum(stale, store.Trend{}, now).Total
	if s >= f {
		t.Errorf("stale %.3f should score below fresh %.3f", s, f)
	}
	if !hasNote(Momentum(stale, store.Trend{}, now), "no push in") {
		t.Error("staleness should be explained")
	}
}

func TestStaleFactorDecaysButNeverToZero(t *testing.T) {
	if got := staleFactor(staleAfterDays); got != 1 {
		t.Errorf("at the threshold factor = %v, want 1", got)
	}
	if got := staleFactor(10000); got != staleFloor {
		t.Errorf("far past the threshold factor = %v, want the floor %v", got, staleFloor)
	}
	if a, b := staleFactor(60), staleFactor(120); a <= b {
		t.Errorf("factor should keep falling: %v then %v", a, b)
	}
}

// The regression case the whole star-farm filter exists for.
func TestMomentumPenalisesClawCodePattern(t *testing.T) {
	farm := Momentum(clawCode(), store.Trend{}, now)
	if !hasNote(farm, "inorganic") {
		t.Fatalf("claw-code should be flagged; notes = %v", farm.Notes)
	}

	// Same enormous star count, but with the marks of a real project.
	organic := clawCode()
	organic.Forks = 12000
	organic.Contributors = 900
	organic.Topics = []string{"rust", "cli"}
	organic.Description = "A real project"
	organic = lic(organic, "MIT")

	if got := Momentum(organic, store.Trend{}, now); got.Total <= farm.Total {
		t.Errorf("organic %.3f should outscore the farm pattern %.3f", got.Total, farm.Total)
	}
}

// Contributor count is only meaningful once enrichment has run. Before that,
// zero contributors means "nobody asked", not "nobody contributes".
func TestFarmPenaltyIgnoresContributorsBeforeEnrichment(t *testing.T) {
	r := clawCode()
	r.Enriched = false
	r.Forks = 5000 // below the fork-ratio trigger
	r.Description = "x"
	r.Topics = []string{"cli"}
	r = lic(r, "MIT")

	if p, why := farmPenalty(r); p != 1 {
		t.Errorf("penalty = %v (%s), want none before enrichment", p, why)
	}
}

func TestMomentumStaysInRange(t *testing.T) {
	extreme := healthy()
	extreme.Stars = 900000
	extreme.CreatedAt = now.AddDate(0, 0, -2)
	tr := store.Trend{HasRate: true, StarsPerDay: 50000, HasAcceleration: true, Acceleration: 40000, Points: 3}
	if got := Momentum(extreme, tr, now).Total; got < 0 || got > 1 {
		t.Errorf("Total = %v, want within [0,1]", got)
	}
}

// --- Contributability -------------------------------------------------------

// Trending and contributable are different questions. This is the pair the
// whole two-axis design exists to keep apart.
func TestContributabilitySeparatesPopularFromWelcoming(t *testing.T) {
	popular := Contributability(clawCode(), now)
	welcoming := Contributability(healthy(), now)

	if popular.Total >= welcoming.Total {
		t.Errorf("claw-code %.3f should score below acme/toolkit %.3f despite 46x the stars",
			popular.Total, welcoming.Total)
	}
	if !hasNote(popular, "no clear licence") {
		t.Errorf("an unlicensed repo should be flagged; notes = %v", popular.Notes)
	}
}

func TestContributabilityArchivedScoresZero(t *testing.T) {
	r := healthy()
	r.Archived = true
	s := Contributability(r, now)
	if s.Total != 0 {
		t.Errorf("Total = %v, want 0 for an archived repo", s.Total)
	}
	if !hasNote(s, "archived") {
		t.Errorf("notes = %v", s.Notes)
	}
}

// Nothing merged from anyone in three months outweighs any amount of welcoming
// documentation.
func TestContributabilityPunishesNoMergedWork(t *testing.T) {
	dead := healthy()
	dead.MergedPRs30d, dead.MergedPRs90d, dead.PRAuthors90d = 0, 0, 0

	s := Contributability(dead, now)
	if s.Total >= Contributability(healthy(), now).Total*0.5 {
		t.Errorf("a repo merging nothing scored %.3f, too close to a healthy one", s.Total)
	}
	if !hasNote(s, "no pull requests merged") {
		t.Errorf("notes = %v", s.Notes)
	}
}

// Before enrichment only the discovery signals are known, and the score must
// say so rather than imply the project merges nothing.
func TestContributabilityUnenrichedUsesSlicesOnly(t *testing.T) {
	r := healthy()
	r.Enriched = false
	s := Contributability(r, now)

	if !hasNote(s, "not enriched") {
		t.Errorf("notes = %v", s.Notes)
	}
	if !hasComponent(s, "good first issues") {
		t.Error("the discovery slice should still count")
	}
	if hasComponent(s, "outside authors") {
		t.Error("must not score evidence it has not gathered")
	}
}

// Many different people getting work merged is the signal that matters most.
func TestContributabilityRewardsAuthorDiversity(t *testing.T) {
	oneMaintainer := healthy()
	oneMaintainer.PRAuthors90d = 1

	many := healthy()
	many.PRAuthors90d = 25

	if Contributability(many, now).Total <= Contributability(oneMaintainer, now).Total {
		t.Error("a project merging work from many people should rank higher")
	}
}

// --- Fit --------------------------------------------------------------------

func nishil() profile.Profile {
	return profile.Profile{
		Login:     "NishilRathod",
		Languages: map[string]float64{"typescript": 1, "javascript": 0.9, "python": 0.7, "java": 0.4},
		Topics:    map[string]int{"react": 2, "portfolio": 1},
	}
}

func TestFitComfortAndStretchPullOppositeWays(t *testing.T) {
	known := healthy()
	known.Language = "TypeScript"
	unknown := healthy()
	unknown.Language = "Rust"

	k := FitFor(known, nishil())
	u := FitFor(unknown, nishil())

	if k.Comfort.Total <= u.Comfort.Total {
		t.Errorf("TypeScript comfort %.2f should beat Rust %.2f", k.Comfort.Total, u.Comfort.Total)
	}
	if u.Stretch.Total <= k.Stretch.Total {
		t.Errorf("Rust stretch %.2f should beat TypeScript %.2f", u.Stretch.Total, k.Stretch.Total)
	}
}

func TestFitNoLanguageAnswersNeitherQuestion(t *testing.T) {
	r := healthy()
	r.Language = ""
	f := FitFor(r, nishil())
	if f.Comfort.Total != 0 || f.Stretch.Total != 0 {
		t.Errorf("comfort %.2f stretch %.2f, want both zero", f.Comfort.Total, f.Stretch.Total)
	}
	if !hasNote(f.Stretch, "no primary language") {
		t.Errorf("notes = %v", f.Stretch.Notes)
	}
}

func TestFitCountsTopicOverlap(t *testing.T) {
	r := healthy()
	r.Language = "Go"
	r.Topics = []string{"react", "devtools", "wasm"}

	f := FitFor(r, nishil())
	if !hasComponent(f.Comfort, "familiar topics") {
		t.Error("the shared react topic should register as comfort")
	}
	if !hasComponent(f.Stretch, "new subject matter") {
		t.Error("devtools and wasm should register as new")
	}
}

// --- Ranking ----------------------------------------------------------------

func TestEvaluateSkipsSelfOwnedRepos(t *testing.T) {
	mine := healthy()
	mine.FullName = "NishilRathod/gitscout"

	cands := Evaluate([]github.Repo{mine, healthy()}, nishil(), store.History{}, now)
	if len(cands) != 1 {
		t.Fatalf("got %d candidates, want the user's own repo dropped", len(cands))
	}
	if cands[0].Repo.FullName != "acme/toolkit" {
		t.Errorf("kept %q", cands[0].Repo.FullName)
	}
}

// The stretch list exists to leave familiar ground. A strong project in a
// language he already knows must not appear on it, however good it is.
func TestTopStretchExcludesFamiliarLanguages(t *testing.T) {
	ts := healthy()
	ts.FullName = "acme/ts-thing"
	ts.ID = 2
	ts.Language = "TypeScript"

	rust := healthy()
	rust.FullName = "acme/rust-thing"
	rust.ID = 3
	rust.Language = "Rust"

	p := nishil()
	cands := Evaluate([]github.Repo{ts, rust}, p, store.History{}, now)

	got := TopStretch(cands, p, 10)
	if len(got) != 1 || got[0].Repo.FullName != "acme/rust-thing" {
		t.Errorf("stretch list = %v, want only the unfamiliar language", names(got))
	}

	// The contribute list has the opposite bias and should keep both.
	if len(TopContribute(cands, 10)) != 2 {
		t.Error("the contribute list should keep familiar languages")
	}
}

// Ranking on comfort and momentum alone would recommend projects that merge
// nothing from outsiders.
func TestTopContributeRequiresSomeOpenness(t *testing.T) {
	closed := healthy()
	closed.Archived = true

	cands := Evaluate([]github.Repo{closed}, nishil(), store.History{}, now)
	if got := TopContribute(cands, 10); len(got) != 0 {
		t.Errorf("got %v, want nothing that accepts no contributions", names(got))
	}
}

func TestRankingIsDeterministic(t *testing.T) {
	a, b := healthy(), healthy()
	a.FullName, a.ID = "z/one", 1
	b.FullName, b.ID = "a/two", 2

	cands := Evaluate([]github.Repo{a, b}, nishil(), store.History{}, now)
	first := names(TopContribute(cands, 10))
	for i := 0; i < 5; i++ {
		if got := names(TopContribute(cands, 10)); got != first {
			t.Fatalf("ordering changed between runs: %v then %v", first, got)
		}
	}
	// Equal scores break ties alphabetically, so digests diff cleanly.
	if first != "a/two,z/one" {
		t.Errorf("order = %v, want the alphabetical tiebreak", first)
	}
}

func TestTopNLimits(t *testing.T) {
	var repos []github.Repo
	for i := 0; i < 10; i++ {
		r := healthy()
		r.ID = int64(100 + i)
		r.FullName = "acme/r" + string(rune('a'+i))
		repos = append(repos, r)
	}
	cands := Evaluate(repos, nishil(), store.History{}, now)
	if got := len(TopContribute(cands, 3)); got != 3 {
		t.Errorf("got %d, want 3", got)
	}
	if got := len(ByMomentum(cands, 0)); got != 10 {
		t.Errorf("n=0 should return everything, got %d", got)
	}
}

// --- helpers ----------------------------------------------------------------

func hasNote(s Score, substr string) bool {
	for _, n := range s.Notes {
		if strings.Contains(n, substr) {
			return true
		}
	}
	return false
}

func hasComponent(s Score, name string) bool {
	for _, c := range s.Components {
		if c.Name == name {
			return true
		}
	}
	return false
}

func names(cs []Candidate) string {
	out := make([]string, len(cs))
	for i, c := range cs {
		out[i] = c.Repo.FullName
	}
	return strings.Join(out, ",")
}

// The first live sweep exposed this: with the reference set too low, every
// repository past it scored the same and the momentum axis ranked nothing.
// These rates are real figures from that run.
func TestMomentumDiscriminatesAcrossRealRates(t *testing.T) {
	rates := []float64{14.0, 24.1, 51.6, 138.4, 331.4, 461.7, 676.7}

	var last float64 = -1
	for _, rate := range rates {
		r := healthy()
		// Reconstruct a repo whose lifetime average is this rate.
		r.Stars = int(rate * 200)
		r.CreatedAt = now.AddDate(0, 0, -200)

		got := Momentum(r, store.Trend{}, now).Total
		if got <= last {
			t.Errorf("%.1f stars/day scored %.3f, not above the %.3f of the slower repo below it", rate, got, last)
		}
		last = got
	}
}

// A measured rate and a lifetime average are different measurements and are
// scored on different scales. Sustaining 150 stars/day between runs is
// exceptional; a lifetime average of 150 is ordinary among repositories
// discovery already selected for climbing.
func TestMomentumScoresMeasuredRatesMoreGenerouslyThanProxies(t *testing.T) {
	const rate = 150

	r := healthy()
	r.Stars = int(rate * 200)
	r.CreatedAt = now.AddDate(0, 0, -200)

	proxy := Momentum(r, store.Trend{}, now)
	measured := Momentum(r, store.Trend{HasRate: true, StarsPerDay: rate, SpanDays: 7, Points: 2}, now)

	if measured.Total <= proxy.Total {
		t.Errorf("measured %.3f should outscore the same figure as a lifetime average %.3f",
			measured.Total, proxy.Total)
	}
	if !measured.Measured || proxy.Measured {
		t.Error("only the measured score should claim to be measured")
	}
}

// nvbn/thefuck: 97k stars earned over more than a decade, untouched since 2024.
// A lifetime average would call that momentum; it is not.
func TestMomentumRejectsTheFuckDespiteItsStars(t *testing.T) {
	fuck := Momentum(theFuck(), store.Trend{}, now)
	live := Momentum(healthy(), store.Trend{}, now)

	if fuck.Total >= live.Total {
		t.Errorf("thefuck scored %.3f against a live project's %.3f despite 23x the stars and two years of silence",
			fuck.Total, live.Total)
	}
	if !hasNote(fuck, "no push in") {
		t.Errorf("notes = %v", fuck.Notes)
	}
}
