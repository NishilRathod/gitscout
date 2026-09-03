package render

import (
	"fmt"
	"io"
	"strings"
	"text/tabwriter"

	"github.com/NishilRathod/gitscout/internal/gaps"
	"github.com/NishilRathod/gitscout/internal/score"
)

// Table writes the run to a terminal as aligned columns.
func Table(w io.Writer, r Report) error {
	fmt.Fprintf(w, "gitscout — %s, personalised for @%s\n",
		r.GeneratedAt.Format("2 Jan 2006"), r.Profile.Login)
	fmt.Fprintf(w, "swept %d repos, enriched %d\n\n", r.Discovered, r.EnrichedRepo)
	fmt.Fprintf(w, "%s\n\n", wrap(r.HistoryNote(), 78))

	candidateTable(w, "CONTRIBUTE NOW", r.Contribute, "fit", func(c score.Candidate) float64 {
		return c.Fit.Comfort.Total
	})
	candidateTable(w, "STRETCH TARGETS", r.Stretch, "new", func(c score.Candidate) float64 {
		return c.Fit.Stretch.Total
	})

	signalList(w, "ABANDONED BUT WANTED", r.Abandoned)
	signalList(w, "STACKS TO GROW INTO", r.Stacks)
	signalList(w, "ECOSYSTEM HOLES (weak signal)", r.Holes)

	if len(r.Budgets) > 0 {
		fmt.Fprint(w, "rate limit left: ")
		var parts []string
		for _, res := range []string{"core", "search"} {
			if b, ok := r.Budgets[res]; ok {
				parts = append(parts, fmt.Sprintf("%s %d/%d", res, b.Remaining, b.Limit))
			}
		}
		fmt.Fprintln(w, strings.Join(parts, ", "))
	}
	if len(r.Errors) > 0 {
		fmt.Fprintf(w, "%d non-fatal errors (see the digest)\n", len(r.Errors))
	}
	return nil
}

func candidateTable(w io.Writer, heading string, cands []score.Candidate, fitLabel string, fit func(score.Candidate) float64) {
	fmt.Fprintf(w, "%s\n", heading)
	if len(cands) == 0 {
		fmt.Fprintf(w, "  nothing cleared the bar this run\n\n")
		return
	}

	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	fmt.Fprintf(tw, "  REPOSITORY\tLANG\tSTARS\tOPEN\tMERGE\t%s\tWHY\n", strings.ToUpper(fitLabel))
	for _, c := range cands {
		measured := ""
		if !c.Momentum.Measured {
			measured = "*"
		}
		fmt.Fprintf(tw, "  %s\t%s\t%s\t%.2f\t%.2f%s\t%.2f\t%s\n",
			truncate(c.Repo.FullName, 38),
			truncate(languageOr(c.Repo.Language, "-"), 12),
			humanCount(c.Repo.Stars),
			c.Contributability.Total,
			c.Momentum.Total, measured,
			fit(c),
			truncate(reason(c, 2), 52),
		)
	}
	tw.Flush()
	fmt.Fprintf(w, "  * lifetime average, not a measured rate\n\n")
}

func signalList(w io.Writer, heading string, sigs []gaps.Signal) {
	fmt.Fprintf(w, "%s\n", heading)
	if len(sigs) == 0 {
		fmt.Fprintf(w, "  nothing found this run\n\n")
		return
	}
	for _, s := range sigs {
		fmt.Fprintf(w, "  %s\n", s.Title)
		// This list is indented text rather than a tabwriter block, so an
		// extra line here costs no column alignment.
		if d := shortDescription(s.Description); d != "" {
			fmt.Fprintf(w, "      %s\n", d)
		}
		for _, e := range s.Evidence {
			fmt.Fprintf(w, "      %s\n", e)
		}
	}
	fmt.Fprintln(w)
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	if n <= 1 {
		return s[:n]
	}
	return s[:n-1] + "…"
}

// wrap breaks text at spaces to fit a width, so the history note stays readable
// in a terminal without depending on the terminal to wrap it sensibly.
func wrap(s string, width int) string {
	words := strings.Fields(s)
	if len(words) == 0 {
		return ""
	}
	var b strings.Builder
	line := words[0]
	for _, word := range words[1:] {
		if len(line)+1+len(word) > width {
			b.WriteString(line + "\n")
			line = word
			continue
		}
		line += " " + word
	}
	b.WriteString(line)
	return b.String()
}
