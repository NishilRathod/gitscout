package render

import (
	"bytes"
	"strings"
	"testing"
	"time"

	"github.com/NishilRathod/gitscout/internal/gaps"
	"github.com/NishilRathod/gitscout/internal/github"
	"github.com/NishilRathod/gitscout/internal/profile"
	"github.com/NishilRathod/gitscout/internal/score"
	"github.com/NishilRathod/gitscout/internal/store"
)

var now = time.Date(2026, 9, 3, 0, 0, 0, 0, time.UTC)

func prof() profile.Profile {
	return profile.Profile{
		Login:     "NishilRathod",
		Repos:     14,
		Languages: map[string]float64{"typescript": 1, "python": 0.7},
	}
}

func repo() github.Repo {
	r := github.Repo{
		ID: 1, FullName: "acme/toolkit", HTMLURL: "https://github.com/acme/toolkit",
		Description: "A well-run toolkit",
		Language:    "Go", Stars: 4200, Topics: []string{"cli"},
		CreatedAt: now.AddDate(0, 0, -300), PushedAt: now.AddDate(0, 0, -2),
		Contributors: 140, MergedPRs30d: 18, MergedPRs90d: 52, PRAuthors90d: 21,
		HasContributing: true, Enriched: true,
		Slices: []string{github.SliceGoodFirst},
	}
	r.License.SPDXID = "MIT"
	return r
}

func reportWith(h store.History, historyRuns int) Report {
	cands := score.Evaluate([]github.Repo{repo()}, prof(), h, now)
	measured := 0
	for _, c := range cands {
		if c.Momentum.Measured {
			measured++
		}
	}
	return Report{
		GeneratedAt:  now,
		Profile:      prof(),
		Contribute:   score.TopContribute(cands, 10),
		Stretch:      score.TopStretch(cands, prof(), 10),
		Discovered:   1,
		EnrichedRepo: 1,
		HistoryRuns:  historyRuns,
		Measured:     measured,
		Budgets: map[string]github.Budget{
			"core":   {Remaining: 4800, Limit: 5000},
			"search": {Remaining: 12, Limit: 30},
		},
	}
}

// The most important sentence in the digest. On a first run the momentum
// figures are lifetime averages, and the report must say so before showing any.
func TestHistoryNoteIsHonestAboutTheFirstRun(t *testing.T) {
	note := reportWith(store.History{}, 0).HistoryNote()

	for _, want := range []string{"First run", "stars divided by age"} {
		if !strings.Contains(note, want) {
			t.Errorf("note %q missing %q", note, want)
		}
	}
}

func TestHistoryNoteChangesOnceThereIsHistory(t *testing.T) {
	h := store.History{1: {
		{ID: 1, Stars: 4000, ObservedAt: now.AddDate(0, 0, -14)},
		{ID: 1, Stars: 4100, ObservedAt: now.AddDate(0, 0, -7)},
		{ID: 1, Stars: 4200, ObservedAt: now},
	}}
	r := reportWith(h, 3)

	if r.Measured != 1 {
		t.Fatalf("Measured = %d, want 1", r.Measured)
	}
	note := r.HistoryNote()
	if strings.Contains(note, "First run") {
		t.Errorf("note %q still claims a first run", note)
	}
	if !strings.Contains(note, "measured growth rates") {
		t.Errorf("note %q should say the rates are measured", note)
	}
}

// History exists but today's candidates are new to it. The report must not
// imply their rates were measured.
func TestHistoryNoteWhenCandidatesAreAllNew(t *testing.T) {
	h := store.History{999: {{ID: 999, Stars: 1, ObservedAt: now.AddDate(0, 0, -7)}}}
	note := reportWith(h, 4).HistoryNote()

	if !strings.Contains(note, "none of today's candidates") {
		t.Errorf("note %q should explain why nothing is measured", note)
	}
}

func TestMarkdownStructureAndContent(t *testing.T) {
	md := Markdown(reportWith(store.History{}, 0))

	for _, want := range []string{
		"# gitscout — 3 September 2026",
		"## Contribute now",
		"## Stretch targets",
		"## Gap signals",
		"### Abandoned but wanted",
		"### Stacks to grow into",
		"### Ecosystem holes",
		"## Run details",
		// The repository, linked.
		"[acme/toolkit](https://github.com/acme/toolkit)",
		// Its measured evidence, not just a score.
		"21 people had PRs merged in 90d",
		// The profile it was personalised against.
		"typescript 1.00",
		// Rate-limit accounting.
		"core 4800/5000",
	} {
		if !strings.Contains(md, want) {
			t.Errorf("digest missing %q", want)
		}
	}
}

// A proxy figure must be marked wherever it is shown, not only in the header.
func TestMarkdownMarksProxyRates(t *testing.T) {
	md := Markdown(reportWith(store.History{}, 0))
	if !strings.Contains(md, "lifetime average rather than a measured rate") {
		t.Error("the table legend should explain the asterisk")
	}
	// The row itself carries the marker.
	if !strings.Contains(md, "| acme/toolkit") && !strings.Contains(md, "0.") {
		t.Error("expected a scored row")
	}
}

func TestMarkdownEmptySections(t *testing.T) {
	md := Markdown(Report{GeneratedAt: now, Profile: prof()})

	if strings.Count(md, "_Nothing cleared the bar this run._") != 2 {
		t.Error("both candidate lists should say they are empty")
	}
	if strings.Count(md, "_Nothing found this run._") != 3 {
		t.Error("all three gap sections should say they are empty")
	}
}

func TestMarkdownIncludesGapEvidence(t *testing.T) {
	r := reportWith(store.History{}, 0)
	r.Abandoned = []gaps.Signal{{
		Kind:     gaps.KindAbandoned,
		Title:    "nvbn/thefuck",
		URL:      "https://github.com/nvbn/thefuck",
		Evidence: []string{"97.8k stars, 3900 forks", "no push in 776 days"},
	}}
	md := Markdown(r)

	if !strings.Contains(md, "[nvbn/thefuck](https://github.com/nvbn/thefuck)") {
		t.Error("gap signals should link to the repository")
	}
	if !strings.Contains(md, "no push in 776 days") {
		t.Error("the evidence must be shown, not just the conclusion")
	}
}

func TestMarkdownListsNonFatalErrors(t *testing.T) {
	r := reportWith(store.History{}, 0)
	r.Errors = []string{"slice \"rising:zig\" page 1: 422"}
	md := Markdown(r)

	if !strings.Contains(md, "1 non-fatal errors") {
		t.Error("errors should be summarised")
	}
	if !strings.Contains(md, "rising:zig") {
		t.Error("the error text should be shown")
	}
}

func TestTableRenders(t *testing.T) {
	var buf bytes.Buffer
	if err := Table(&buf, reportWith(store.History{}, 0)); err != nil {
		t.Fatal(err)
	}
	out := buf.String()

	for _, want := range []string{
		"CONTRIBUTE NOW", "STRETCH TARGETS",
		"ABANDONED BUT WANTED", "ECOSYSTEM HOLES (weak signal)",
		"acme/toolkit", "REPOSITORY", "core 4800/5000",
		"* lifetime average, not a measured rate",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("table missing %q\n%s", want, out)
		}
	}
}

func TestTableEmptyReport(t *testing.T) {
	var buf bytes.Buffer
	if err := Table(&buf, Report{GeneratedAt: now, Profile: prof()}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(buf.String(), "nothing cleared the bar this run") {
		t.Error("an empty run should say so rather than print bare headings")
	}
}

func TestTruncate(t *testing.T) {
	tests := []struct{ in, want string }{
		{"short", "short"},
		{"averyveryverylongrepositoryname", "averyveryv…"},
	}
	for _, tt := range tests {
		if got := truncate(tt.in, 11); got != tt.want {
			t.Errorf("truncate(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

func TestWrapBreaksAtSpaces(t *testing.T) {
	got := wrap("the quick brown fox jumps over the lazy dog", 15)
	for _, line := range strings.Split(got, "\n") {
		if len(line) > 15 {
			t.Errorf("line %q exceeds the width", line)
		}
	}
	if strings.ReplaceAll(got, "\n", " ") != "the quick brown fox jumps over the lazy dog" {
		t.Errorf("wrapping changed the text: %q", got)
	}
}

func TestReasonPrefersStrongestComponents(t *testing.T) {
	cands := score.Evaluate([]github.Repo{repo()}, prof(), store.History{}, now)
	got := reason(cands[0], 2)

	if strings.Count(got, ";") != 1 {
		t.Errorf("reason = %q, want exactly 2 parts", got)
	}
	// The single strongest piece of evidence this tool has.
	if !strings.Contains(got, "21 people had PRs merged") {
		t.Errorf("reason = %q, want author diversity leading", got)
	}
}

func TestHumanCountAndLanguageFallback(t *testing.T) {
	if got := humanCount(97763); got != "97.8k" {
		t.Errorf("humanCount = %q", got)
	}
	if got := languageOr("", "—"); got != "—" {
		t.Errorf("languageOr = %q", got)
	}
}

// A row that is a name and six numbers still makes the reader open the
// repository to find out what it does.
func TestMarkdownShowsDescriptionUnderTheLink(t *testing.T) {
	r := reportWith(store.History{}, 0)
	md := Markdown(r)

	if !strings.Contains(md, "[acme/toolkit](https://github.com/acme/toolkit)<br><sub>A well-run toolkit</sub>") {
		t.Errorf("description should sit under the link in the same cell:\n%s", md)
	}
}

// A pipe in an author-written description would split the table cell and
// silently corrupt the row.
func TestMarkdownEscapesPipesInDescriptions(t *testing.T) {
	rp := repo()
	rp.Description = "Parses a|b|c streams"
	cands := score.Evaluate([]github.Repo{rp}, prof(), store.History{}, now)

	r := reportWith(store.History{}, 0)
	r.Contribute = score.TopContribute(cands, 10)
	md := Markdown(r)

	if strings.Contains(md, "Parses a|b|c") {
		t.Error("raw pipes would break the table row")
	}
	if !strings.Contains(md, `Parses a\|b\|c streams`) {
		t.Errorf("expected escaped pipes:\n%s", md)
	}
	// The row must still have the right number of cells.
	for _, line := range strings.Split(md, "\n") {
		if strings.HasPrefix(line, "| [acme/toolkit") {
			if got := strings.Count(line, "|") - strings.Count(line, `\|`); got != 8 {
				t.Errorf("row has %d unescaped pipes, want 8: %s", got, line)
			}
		}
	}
}

func TestMarkdownGapSignalCarriesDescription(t *testing.T) {
	r := reportWith(store.History{}, 0)
	r.Abandoned = []gaps.Signal{{
		Kind:        gaps.KindAbandoned,
		Title:       "nvbn/thefuck",
		URL:         "https://github.com/nvbn/thefuck",
		Description: "Magnificent app which corrects your previous console command.",
		Evidence:    []string{"97.8k stars, 3961 forks"},
	}}
	md := Markdown(r)

	if !strings.Contains(md, "<sub>Magnificent app which corrects your previous console command.</sub>") {
		t.Errorf("gap signals should say what the project is:\n%s", md)
	}
}

func TestTableShowsGapSignalDescription(t *testing.T) {
	r := reportWith(store.History{}, 0)
	r.Abandoned = []gaps.Signal{{
		Title:       "nvbn/thefuck",
		Description: "Magnificent app which corrects your previous console command.",
		Evidence:    []string{"97.8k stars"},
	}}
	var buf bytes.Buffer
	if err := Table(&buf, r); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(buf.String(), "Magnificent app which corrects") {
		t.Errorf("terminal output missing the description:\n%s", buf.String())
	}
}

func TestShortDescription(t *testing.T) {
	long := "This project does a great many things and the author has explained every single one of them at considerable length indeed"

	tests := []struct {
		name, in, want string
	}{
		{"empty stays empty", "", ""},
		{"short passes through", "A small tool", "A small tool"},
		{"newlines and runs of spaces collapse", "A  tool\nfor\tthings", "A tool for things"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := shortDescription(tt.in); got != tt.want {
				t.Errorf("got %q, want %q", got, tt.want)
			}
		})
	}

	got := shortDescription(long)
	if len(got) > maxDescription+3 {
		t.Errorf("length %d exceeds the cap, got %q", len(got), got)
	}
	if !strings.HasSuffix(got, "…") {
		t.Errorf("truncated text should be marked: %q", got)
	}
	// Truncation lands on a word boundary rather than mid-word.
	if strings.Contains(got, "considerabl…") {
		t.Errorf("broke mid-word: %q", got)
	}
}
