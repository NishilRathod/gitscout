// Command gitscout finds open-source projects worth your attention: growing
// repositories that actually merge outside work, targets that would stretch you
// into an unfamiliar stack, and gaps where something is missing entirely.
//
// It personalises everything against your own GitHub profile — what you have
// written, weighted by volume and recency — and writes both a terminal table
// and a dated Markdown digest.
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"time"

	"github.com/NishilRathod/gitscout/internal/gaps"
	"github.com/NishilRathod/gitscout/internal/github"
	"github.com/NishilRathod/gitscout/internal/profile"
	"github.com/NishilRathod/gitscout/internal/render"
	"github.com/NishilRathod/gitscout/internal/score"
	"github.com/NishilRathod/gitscout/internal/store"
)

type options struct {
	user       string
	languages  string
	minStars   int
	pages      int
	top        int
	enrich     int
	dataDir    string
	digestDir  string
	skipDigest bool
	skipHoles  bool
}

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "gitscout:", err)
		os.Exit(1)
	}
}

func run(args []string) error {
	// "gitscout run" and a bare "gitscout" do the same thing. The subcommand
	// exists so the command line has room to grow without breaking.
	if len(args) > 0 && args[0] == "run" {
		args = args[1:]
	}

	fs := flag.NewFlagSet("gitscout", flag.ContinueOnError)
	var o options
	fs.StringVar(&o.user, "user", "", "GitHub account to personalise against (default: the token's owner)")
	fs.StringVar(&o.languages, "languages", "", "comma-separated languages to sweep, overriding the profile-derived set")
	fs.IntVar(&o.minStars, "min-stars", 150, "star floor for the rising slices")
	fs.IntVar(&o.pages, "pages", 2, "search pages per slice (100 repos each)")
	fs.IntVar(&o.top, "top", 15, "rows per list")
	fs.IntVar(&o.enrich, "enrich", 60, "how many top repositories to enrich with contribution data")
	fs.StringVar(&o.dataDir, "data", "data/snapshots", "directory for snapshot history")
	fs.StringVar(&o.digestDir, "digests", "digests", "directory for Markdown digests")
	fs.BoolVar(&o.skipDigest, "no-digest", false, "print to the terminal only")
	fs.BoolVar(&o.skipHoles, "no-holes", false, "skip the ecosystem-hole probes, saving search requests")
	fs.Usage = func() {
		fmt.Fprintln(fs.Output(), "usage: gitscout [run] [flags]")
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		return err
	}

	// Ctrl-C should stop the run promptly rather than after the next paced
	// search request.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	return scout(ctx, o)
}

func scout(ctx context.Context, o options) error {
	now := time.Now().UTC()
	client := github.NewClient()
	if !client.Authenticated() {
		return fmt.Errorf("no GitHub token: set GITHUB_TOKEN or run 'gh auth login'\n" +
			"unauthenticated requests are capped at 60/hour, far below what a run needs")
	}

	login, err := resolveLogin(ctx, o.user, client.Viewer, os.Getenv)
	if err != nil {
		return err
	}

	var runErrs []string
	note := func(format string, a ...any) {
		runErrs = append(runErrs, fmt.Sprintf(format, a...))
	}

	// 1. What has this person actually written?
	fmt.Fprintf(os.Stderr, "profiling @%s...\n", login)
	prof, err := profile.Build(ctx, client, login, now)
	if err != nil {
		return fmt.Errorf("building the profile for @%s: %w", login, err)
	}
	fmt.Fprintf(os.Stderr, "  %d repos, top languages: %s\n", prof.Repos, summarise(prof))

	// 2. Sweep GitHub, biased by that profile.
	cfg := github.DefaultDiscoverConfig()
	cfg.Now = now
	cfg.MinStars = o.minStars
	cfg.PagesPerSlice = o.pages
	if o.languages != "" {
		cfg.Languages = splitList(o.languages)
	} else {
		cfg.Languages = prof.SweepLanguages(profile.DefaultStretchPool, 3, 3)
	}
	fmt.Fprintf(os.Stderr, "sweeping %d slices (%s)...\n",
		len(github.BuildSlices(cfg)), strings.Join(cfg.Languages, ", "))

	repos, errs := client.Discover(ctx, cfg)
	for _, e := range errs {
		note("discovery: %v", e)
	}
	if len(repos) == 0 {
		return fmt.Errorf("discovery returned nothing (%d errors)", len(errs))
	}
	fmt.Fprintf(os.Stderr, "  %d repositories\n", len(repos))

	// 3. Record what we saw, then read back every run including this one. The
	//    order matters: today's observation is the endpoint of every rate, so
	//    it has to be written before the history is loaded.
	snapshots := store.New(o.dataDir)
	priorRuns, err := snapshots.RunDates()
	if err != nil {
		note("reading snapshot dates: %v", err)
	}
	priorRuns = excluding(priorRuns, now.Format("2006-01-02"))

	if _, err := snapshots.Append(observations(repos, now), now); err != nil {
		note("writing snapshots: %v", err)
	}
	history, err := snapshots.Load()
	if err != nil {
		note("loading history: %v", err)
		history = store.History{}
	}

	// 4. Score cheaply, then spend enrichment requests only on what looks
	//    worth the budget.
	cands := score.Evaluate(repos, prof, history, now)
	shortlist := score.ByMomentum(cands, o.enrich)
	fmt.Fprintf(os.Stderr, "enriching %d repositories...\n", len(shortlist))

	enriched := make([]github.Repo, len(shortlist))
	for i, c := range shortlist {
		enriched[i] = c.Repo
	}
	for _, e := range client.Enrich(ctx, enriched) {
		note("enrichment: %v", e)
	}
	applyEnrichment(repos, enriched)

	// 5. Re-score with the contribution evidence now in hand.
	cands = score.Evaluate(repos, prof, history, now)

	// 6. Gap signals.
	abandoned := gaps.Abandoned(cands, now, o.top)
	stacks := gaps.StretchStacks(repos, prof, 6)

	var holes []gaps.Signal
	if !o.skipHoles {
		fmt.Fprintln(os.Stderr, "probing for ecosystem holes...")
		rep := gaps.EcosystemHoles(ctx, client, repos, gaps.DefaultEcosystemHolesConfig())
		holes = rep.Signals
		for _, e := range rep.Errors {
			note("ecosystem probe: %v", e)
		}
		// Say what the probe actually did. "Nothing found" is otherwise
		// indistinguishable from "nothing was even looked at".
		fmt.Fprintf(os.Stderr, "  %d queries across %d topics (%s) -> %d holes\n",
			rep.QueriesRun, len(rep.TopicsProbed),
			strings.Join(rep.TopicsProbed, ", "), len(rep.Signals))
	}

	report := render.Report{
		GeneratedAt:  now,
		Profile:      prof,
		Contribute:   score.TopContribute(cands, o.top),
		Stretch:      score.TopStretch(cands, prof, o.top),
		Abandoned:    abandoned,
		Stacks:       stacks,
		Holes:        holes,
		Discovered:   len(repos),
		EnrichedRepo: countEnriched(repos),
		HistoryRuns:  len(priorRuns),
		Measured:     countMeasured(cands),
		Budgets:      client.Budgets(),
		Errors:       runErrs,
	}

	fmt.Fprintln(os.Stderr)
	if err := render.Table(os.Stdout, report); err != nil {
		return err
	}

	if o.skipDigest {
		return nil
	}
	path, err := writeDigest(o.digestDir, report)
	if err != nil {
		return err
	}
	fmt.Fprintf(os.Stderr, "\ndigest written to %s\n", path)
	return nil
}

// resolveLogin works out whose profile to personalise against.
//
// The environment fallback is not a convenience. Inside GitHub Actions the
// supplied GITHUB_TOKEN is a GitHub App installation token with no user
// identity attached, so /user answers 403 "Resource not accessible by
// integration" — which meant the tool failed outright in the one environment it
// is meant to run in unattended. Actions does set GITHUB_REPOSITORY_OWNER, and
// for a scheduled run on your own repository that is exactly the right account.
//
// The order tries the most accurate source first and falls back only when it
// has to, so a local run still personalises against whoever owns the token
// rather than whatever the environment happens to say.
func resolveLogin(
	ctx context.Context,
	flagUser string,
	viewer func(context.Context) (github.User, error),
	getenv func(string) string,
) (string, error) {
	if flagUser != "" {
		return flagUser, nil
	}
	if u, err := viewer(ctx); err == nil && u.Login != "" {
		return u.Login, nil
	}
	if owner := strings.TrimSpace(getenv("GITHUB_REPOSITORY_OWNER")); owner != "" {
		return owner, nil
	}
	return "", fmt.Errorf("could not identify whose profile to use: " +
		"the token has no user identity and GITHUB_REPOSITORY_OWNER is unset. Pass -user")
}

func writeDigest(dir string, r render.Report) (string, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", fmt.Errorf("creating %s: %w", dir, err)
	}
	path := filepath.Join(dir, r.GeneratedAt.Format("2006-01-02")+".md")
	if err := os.WriteFile(path, []byte(render.Markdown(r)), 0o644); err != nil {
		return "", fmt.Errorf("writing %s: %w", path, err)
	}
	return path, nil
}

func observations(repos []github.Repo, now time.Time) []store.Observation {
	out := make([]store.Observation, len(repos))
	for i, r := range repos {
		out[i] = store.Observation{
			ID:         r.ID,
			FullName:   r.FullName,
			Stars:      r.Stars,
			Forks:      r.Forks,
			OpenIssues: r.OpenIssues,
			PushedAt:   r.PushedAt,
			ObservedAt: now,
		}
	}
	return out
}

// applyEnrichment copies the fields the enrichment pass gathered back onto the
// master repository list, which the scorers and gap finders both read.
func applyEnrichment(repos []github.Repo, enriched []github.Repo) {
	byID := make(map[int64]github.Repo, len(enriched))
	for _, e := range enriched {
		byID[e.ID] = e
	}
	for i := range repos {
		e, ok := byID[repos[i].ID]
		if !ok {
			continue
		}
		repos[i].Contributors = e.Contributors
		repos[i].MergedPRs30d = e.MergedPRs30d
		repos[i].MergedPRs90d = e.MergedPRs90d
		repos[i].PRAuthors90d = e.PRAuthors90d
		repos[i].HasContributing = e.HasContributing
		repos[i].Enriched = e.Enriched
	}
}

func countEnriched(repos []github.Repo) int {
	n := 0
	for _, r := range repos {
		if r.Enriched {
			n++
		}
	}
	return n
}

func countMeasured(cands []score.Candidate) int {
	n := 0
	for _, c := range cands {
		if c.Momentum.Measured {
			n++
		}
	}
	return n
}

func summarise(p profile.Profile) string {
	top := p.TopLanguages(4)
	if len(top) == 0 {
		return "none found"
	}
	parts := make([]string, len(top))
	for i, lw := range top {
		parts[i] = fmt.Sprintf("%s %.2f", lw.Language, lw.Weight)
	}
	return strings.Join(parts, ", ")
}

func splitList(s string) []string {
	var out []string
	for _, p := range strings.Split(s, ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

func excluding(list []string, v string) []string {
	var out []string
	for _, x := range list {
		if x != v {
			out = append(out, x)
		}
	}
	return out
}
