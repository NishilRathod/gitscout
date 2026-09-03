package score

import (
	"fmt"
	"strings"

	"github.com/NishilRathod/gitscout/internal/github"
	"github.com/NishilRathod/gitscout/internal/profile"
)

// Fit reports how a repository sits against what the user has already written.
//
// It deliberately produces two numbers rather than one, because the user wants
// two different things and they pull in opposite directions. Comfort answers
// "could I open a pull request this weekend"; Stretch answers "would this teach
// me something my portfolio does not already show". A repository that scores
// high on both does not exist, and a single blended number would quietly hide
// which question it was answering.
type Fit struct {
	Comfort Score
	Stretch Score
}

// FitFor scores a repository against a profile.
func FitFor(r github.Repo, p profile.Profile) Fit {
	var f Fit

	lang := r.Language
	weight := p.Weight(lang)

	if lang == "" {
		// GitHub reports no primary language for documentation and
		// configuration repositories. Neither question has an answer.
		f.Comfort.note("no primary language reported")
		f.Stretch.note("no primary language reported")
		return f
	}

	f.Comfort.add("language", 0.7*weight,
		fmt.Sprintf("%s, weight %.2f in your profile", lang, weight))

	// Unfamiliarity is the point of the stretch score, so it is simply the
	// inverse. A language the user has never touched scores a full point.
	f.Stretch.add("new language", 0.7*(1-weight),
		fmt.Sprintf("%s, weight %.2f in your profile", lang, weight))

	shared, novel := topicOverlap(r.Topics, p.Topics)
	if len(r.Topics) > 0 {
		f.Comfort.add("familiar topics", 0.3*normalize(float64(shared), 3),
			topicDetail(shared, "shared with your repos"))
		f.Stretch.add("new subject matter", 0.3*normalize(float64(novel), 5),
			topicDetail(novel, "topics you have never tagged"))
	}

	f.Comfort.Total = clamp(f.Comfort.Total)
	f.Stretch.Total = clamp(f.Stretch.Total)
	return f
}

func topicOverlap(repoTopics []string, userTopics map[string]int) (shared, novel int) {
	for _, t := range repoTopics {
		if userTopics[strings.ToLower(t)] > 0 {
			shared++
		} else {
			novel++
		}
	}
	return shared, novel
}

func topicDetail(n int, suffix string) string {
	if n == 0 {
		return ""
	}
	return fmt.Sprintf("%d %s", n, suffix)
}
