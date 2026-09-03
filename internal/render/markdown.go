package render

import (
	"fmt"
	"strings"

	"github.com/NishilRathod/gitscout/internal/gaps"
	"github.com/NishilRathod/gitscout/internal/score"
)

// Markdown renders the dated digest.
func Markdown(r Report) string {
	var b strings.Builder

	fmt.Fprintf(&b, "# gitscout — %s\n\n", r.GeneratedAt.Format("2 January 2006"))
	fmt.Fprintf(&b, "Personalised for [@%s](https://github.com/%s). ", r.Profile.Login, r.Profile.Login)
	fmt.Fprintf(&b, "Swept %d repositories, enriched %d.\n\n", r.Discovered, r.EnrichedRepo)
	fmt.Fprintf(&b, "> **%s**\n\n", r.HistoryNote())

	writeProfile(&b, r)
	writeContribute(&b, r)
	writeStretch(&b, r)
	writeGaps(&b, r)
	writeRunDetails(&b, r)

	return b.String()
}

func writeProfile(b *strings.Builder, r Report) {
	top := r.Profile.TopLanguages(6)
	if len(top) == 0 {
		return
	}
	parts := make([]string, len(top))
	for i, lw := range top {
		parts[i] = fmt.Sprintf("%s %.2f", lw.Language, lw.Weight)
	}
	fmt.Fprintf(b, "**Your profile as measured:** %s — from %d non-fork repositories, weighted by bytes written and how recently.\n\n",
		strings.Join(parts, ", "), r.Profile.Repos)
}

func writeContribute(b *strings.Builder, r Report) {
	b.WriteString("## Contribute now\n\n")
	b.WriteString("Growing projects that demonstrably merge outside work, in languages you already write.\n\n")

	if len(r.Contribute) == 0 {
		b.WriteString("_Nothing cleared the bar this run._\n\n")
		return
	}
	writeCandidateTable(b, r.Contribute, "Fit", func(c score.Candidate) float64 {
		return c.Fit.Comfort.Total
	})
}

func writeStretch(b *strings.Builder, r Report) {
	b.WriteString("## Stretch targets\n\n")
	b.WriteString("The same test, restricted to languages absent from your profile — where the work adds something your portfolio does not already show.\n\n")

	if len(r.Stretch) == 0 {
		b.WriteString("_Nothing cleared the bar this run._\n\n")
		return
	}
	writeCandidateTable(b, r.Stretch, "New", func(c score.Candidate) float64 {
		return c.Fit.Stretch.Total
	})
}

func writeCandidateTable(b *strings.Builder, cands []score.Candidate, fitLabel string, fit func(score.Candidate) float64) {
	fmt.Fprintf(b, "| Repository | Lang | Stars | Open | Merge | %s | Why |\n", fitLabel)
	b.WriteString("|---|---|---:|---:|---:|---:|---|\n")

	for _, c := range cands {
		repo := c.Repo
		measured := ""
		if !c.Momentum.Measured {
			measured = "*"
		}

		// The project's own one-line description, under its link. Without it
		// a row is a name and six numbers, and the reader still has to open
		// the repository to find out what it does.
		name := fmt.Sprintf("[%s](%s)", repo.FullName, repo.HTMLURL)
		if d := shortDescription(repo.Description); d != "" {
			name += fmt.Sprintf("<br><sub>%s</sub>", escapePipes(d))
		}

		fmt.Fprintf(b, "| %s | %s | %s | %.2f | %.2f%s | %.2f | %s |\n",
			name,
			languageOr(repo.Language, "—"),
			humanCount(repo.Stars),
			c.Contributability.Total,
			c.Momentum.Total, measured,
			fit(c),
			escapePipes(reason(c, 3)),
		)
	}
	b.WriteString("\nColumns: **Open** how readily outside work gets merged, **Merge** growth (`*` marks a lifetime average rather than a measured rate).\n\n")

	// Caveats go below the table so the table stays scannable, but they are
	// not omitted — the reasons a row might be wrong matter as much as the
	// score.
	var any bool
	var notes strings.Builder
	for _, c := range cands {
		cs := caveats(c)
		if len(cs) == 0 {
			continue
		}
		any = true
		fmt.Fprintf(&notes, "- **%s** — %s\n", c.Repo.FullName, strings.Join(cs, "; "))
	}
	if any {
		b.WriteString("<details><summary>Caveats</summary>\n\n")
		b.WriteString(notes.String())
		b.WriteString("\n</details>\n\n")
	}
}

func writeGaps(b *strings.Builder, r Report) {
	b.WriteString("## Gap signals\n\n")
	b.WriteString("Openings to build something rather than contribute to something. These are heuristics over public metadata: the evidence is printed so you can check it, because the tool cannot tell an opportunity from a dead end.\n\n")

	writeSignals(b, "Abandoned but wanted", r.Abandoned,
		"Projects with a proven audience that nobody has pushed to in a long time. Revive one, or build its replacement — the hardest part of open source, having users, is already done.")

	writeSignals(b, "Stacks to grow into", r.Stacks,
		"Languages absent from your profile, ranked by how much activity this run found in them.")

	writeSignals(b, "Ecosystem holes", r.Holes,
		"Active topics with no obvious companion tool. The weakest signal here — the companion often exists under a name the query did not guess.")
}

func writeSignals(b *strings.Builder, heading string, sigs []gaps.Signal, blurb string) {
	fmt.Fprintf(b, "### %s\n\n%s\n\n", heading, blurb)
	if len(sigs) == 0 {
		b.WriteString("_Nothing found this run._\n\n")
		return
	}
	for _, s := range sigs {
		title := s.Title
		if s.URL != "" {
			title = fmt.Sprintf("[%s](%s)", s.Title, s.URL)
		}
		fmt.Fprintf(b, "- **%s** — %s\n", title, strings.Join(s.Evidence, "; "))
		if d := shortDescription(s.Description); d != "" {
			fmt.Fprintf(b, "  <br><sub>%s</sub>\n", d)
		}
	}
	b.WriteString("\n")
}

func writeRunDetails(b *strings.Builder, r Report) {
	b.WriteString("## Run details\n\n")

	if len(r.Budgets) > 0 {
		b.WriteString("Rate limit remaining at exit: ")
		var parts []string
		for _, res := range []string{"core", "search"} {
			if bg, ok := r.Budgets[res]; ok {
				parts = append(parts, fmt.Sprintf("%s %d/%d", res, bg.Remaining, bg.Limit))
			}
		}
		b.WriteString(strings.Join(parts, ", ") + ".\n\n")
	}

	if len(r.Errors) > 0 {
		fmt.Fprintf(b, "<details><summary>%d non-fatal errors</summary>\n\n", len(r.Errors))
		for _, e := range r.Errors {
			fmt.Fprintf(b, "- `%s`\n", e)
		}
		b.WriteString("\n</details>\n\n")
	}

	b.WriteString("_Generated by [gitscout](https://github.com/NishilRathod/gitscout)._\n")
}
