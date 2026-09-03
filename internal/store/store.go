// Package store persists one line of JSON per repository per run.
//
// It exists because GitHub does not expose star history. The stargazers
// endpoint that once carried per-star timestamps returns 404 over REST and
// empty edges over GraphQL, so the only way to know whether a project is
// accelerating is to have watched it before. Each run appends what it saw; from
// the second run onward the difference is real measurement rather than a proxy.
//
// The format is JSON Lines rather than a database so that history can be
// committed to the repository as reviewable text and appended to by a scheduled
// job without a migration story.
package store

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// mergeWindow collapses observations recorded close together. Two runs in the
// same afternoon say nothing about a trend, and dividing by the minutes between
// them would manufacture enormous rates out of a single new star.
const mergeWindow = 12 * time.Hour

// Observation is one repository as seen during one run.
type Observation struct {
	ID         int64     `json:"id"`
	FullName   string    `json:"full_name"`
	Stars      int       `json:"stars"`
	Forks      int       `json:"forks"`
	OpenIssues int       `json:"open_issues"`
	PushedAt   time.Time `json:"pushed_at"`
	ObservedAt time.Time `json:"observed_at"`
}

// Store reads and writes snapshot files in a directory.
type Store struct{ dir string }

// New returns a store rooted at dir. The directory is created on first write.
func New(dir string) *Store { return &Store{dir: dir} }

// Append writes observations to the snapshot file for their date, creating it
// if needed. Re-running on the same day appends rather than replacing, and the
// merge window keeps those extra points from distorting any trend.
func (s *Store) Append(obs []Observation, at time.Time) (string, error) {
	if len(obs) == 0 {
		return "", nil
	}
	if err := os.MkdirAll(s.dir, 0o755); err != nil {
		return "", fmt.Errorf("creating %s: %w", s.dir, err)
	}

	path := filepath.Join(s.dir, at.UTC().Format("2006-01-02")+".jsonl")
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return "", fmt.Errorf("opening %s: %w", path, err)
	}
	defer f.Close()

	w := bufio.NewWriter(f)
	enc := json.NewEncoder(w)
	for _, o := range obs {
		if o.ObservedAt.IsZero() {
			o.ObservedAt = at
		}
		if err := enc.Encode(o); err != nil {
			return "", fmt.Errorf("encoding %s: %w", o.FullName, err)
		}
	}
	if err := w.Flush(); err != nil {
		return "", fmt.Errorf("writing %s: %w", path, err)
	}
	return path, nil
}

// History is every past observation, grouped by repository ID and ordered
// oldest first.
type History map[int64][]Observation

// Load reads every snapshot file in the directory. A missing directory is not
// an error — it is simply the first run, and yields an empty history.
//
// Malformed lines are skipped rather than fatal: a run interrupted mid-write
// should cost one truncated record, not every week of history behind it.
func (s *Store) Load() (History, error) {
	entries, err := os.ReadDir(s.dir)
	if err != nil {
		if os.IsNotExist(err) {
			return History{}, nil
		}
		return nil, fmt.Errorf("reading %s: %w", s.dir, err)
	}

	h := History{}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".jsonl") {
			continue
		}
		path := filepath.Join(s.dir, e.Name())
		f, err := os.Open(path)
		if err != nil {
			return nil, fmt.Errorf("opening %s: %w", path, err)
		}

		sc := bufio.NewScanner(f)
		sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
		for sc.Scan() {
			line := sc.Bytes()
			if len(line) == 0 {
				continue
			}
			var o Observation
			if err := json.Unmarshal(line, &o); err != nil {
				continue
			}
			h[o.ID] = append(h[o.ID], o)
		}
		f.Close()
	}

	for id := range h {
		obs := h[id]
		sort.Slice(obs, func(i, j int) bool { return obs[i].ObservedAt.Before(obs[j].ObservedAt) })
		h[id] = merge(obs)
	}
	return h, nil
}

// merge collapses runs of observations recorded within mergeWindow of each
// other, keeping the most recent of each cluster.
func merge(obs []Observation) []Observation {
	if len(obs) < 2 {
		return obs
	}
	out := []Observation{obs[0]}
	for _, o := range obs[1:] {
		last := &out[len(out)-1]
		if o.ObservedAt.Sub(last.ObservedAt) < mergeWindow {
			*last = o
			continue
		}
		out = append(out, o)
	}
	return out
}

// Trend is what the snapshot history can say about a repository's growth.
//
// The two Has fields matter as much as the numbers: a zero rate because the
// project gained no stars and a zero rate because this is the first run are
// entirely different claims, and the digest must not present them alike.
type Trend struct {
	Points          int
	SpanDays        float64
	StarsPerDay     float64
	PrevStarsPerDay float64
	Acceleration    float64
	HasRate         bool
	HasAcceleration bool
}

// Trend measures a repository's growth from its recorded history.
//
// The most recent pair of observations gives the current rate; the pair before
// it gives the previous rate, and their difference is the acceleration — the
// thing that separates a project that is still climbing from one that got
// popular and levelled off.
func (h History) Trend(id int64) Trend {
	obs := h[id]
	var t Trend
	t.Points = len(obs)
	if len(obs) < 2 {
		return t
	}
	t.SpanDays = days(obs[0].ObservedAt, obs[len(obs)-1].ObservedAt)

	rate, ok := rateBetween(obs[len(obs)-2], obs[len(obs)-1])
	if !ok {
		return t
	}
	t.StarsPerDay = rate
	t.HasRate = true

	if len(obs) < 3 {
		return t
	}
	prev, ok := rateBetween(obs[len(obs)-3], obs[len(obs)-2])
	if !ok {
		return t
	}
	t.PrevStarsPerDay = prev
	t.Acceleration = rate - prev
	t.HasAcceleration = true
	return t
}

// Latest returns the most recent observation of a repository.
func (h History) Latest(id int64) (Observation, bool) {
	obs := h[id]
	if len(obs) == 0 {
		return Observation{}, false
	}
	return obs[len(obs)-1], true
}

func rateBetween(a, b Observation) (float64, bool) {
	d := days(a.ObservedAt, b.ObservedAt)
	if d <= 0 {
		return 0, false
	}
	return float64(b.Stars-a.Stars) / d, true
}

func days(from, to time.Time) float64 {
	return to.Sub(from).Hours() / 24
}

// RunDates lists the dates on which snapshots were recorded, oldest first. The
// report uses it to say how many earlier runs stand behind today's numbers.
func (s *Store) RunDates() ([]string, error) {
	entries, err := os.ReadDir(s.dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var out []string
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".jsonl") {
			continue
		}
		out = append(out, strings.TrimSuffix(e.Name(), ".jsonl"))
	}
	sort.Strings(out)
	return out, nil
}
