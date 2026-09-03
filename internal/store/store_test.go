package store

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

var base = time.Date(2026, 9, 3, 12, 0, 0, 0, time.UTC)

func obs(id int64, stars int, at time.Time) Observation {
	return Observation{ID: id, FullName: "o/r", Stars: stars, ObservedAt: at}
}

func TestAppendAndLoadRoundTrip(t *testing.T) {
	s := New(t.TempDir())

	path, err := s.Append([]Observation{
		{ID: 1, FullName: "a/b", Stars: 10, Forks: 2, OpenIssues: 3, PushedAt: base},
	}, base)
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Base(path) != "2026-09-03.jsonl" {
		t.Errorf("path = %q, want a file named for the date", path)
	}

	h, err := s.Load()
	if err != nil {
		t.Fatal(err)
	}
	got, ok := h.Latest(1)
	if !ok {
		t.Fatal("observation did not survive the round trip")
	}
	if got.FullName != "a/b" || got.Stars != 10 || got.Forks != 2 || got.OpenIssues != 3 {
		t.Errorf("got %+v", got)
	}
	// Append fills in the observation time when the caller left it zero.
	if !got.ObservedAt.Equal(base) {
		t.Errorf("ObservedAt = %v, want %v", got.ObservedAt, base)
	}
}

// The first run has no directory yet. That is the normal case, not a failure.
func TestLoadMissingDirectoryIsEmptyNotAnError(t *testing.T) {
	s := New(filepath.Join(t.TempDir(), "never-created"))
	h, err := s.Load()
	if err != nil {
		t.Fatalf("err = %v, want nil", err)
	}
	if len(h) != 0 {
		t.Errorf("history = %v, want empty", h)
	}
}

func TestAppendEmptyWritesNothing(t *testing.T) {
	dir := t.TempDir()
	s := New(dir)
	path, err := s.Append(nil, base)
	if err != nil || path != "" {
		t.Fatalf("Append(nil) = %q, %v", path, err)
	}
	entries, _ := os.ReadDir(dir)
	if len(entries) != 0 {
		t.Errorf("wrote %d files for an empty run", len(entries))
	}
}

// A run killed mid-write leaves a truncated final line. Losing that one record
// is acceptable; losing every week of history behind it is not.
func TestLoadSkipsMalformedLines(t *testing.T) {
	dir := t.TempDir()
	content := `{"id":1,"full_name":"a/b","stars":5,"observed_at":"2026-09-01T00:00:00Z"}
{"id":2,"full_name":"trunca
{"id":3,"full_name":"c/d","stars":7,"observed_at":"2026-09-01T00:00:00Z"}
`
	if err := os.WriteFile(filepath.Join(dir, "2026-09-01.jsonl"), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	h, err := New(dir).Load()
	if err != nil {
		t.Fatalf("err = %v, want the readable records back", err)
	}
	if len(h) != 2 {
		t.Errorf("loaded %d repos, want 2", len(h))
	}
	if _, ok := h.Latest(3); !ok {
		t.Error("the record after the corrupt line should still load")
	}
}

func TestLoadIgnoresNonSnapshotFiles(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "README.md"), []byte("not a snapshot"), 0o644)
	os.WriteFile(filepath.Join(dir, "2026-09-01.jsonl"),
		[]byte(`{"id":1,"stars":1,"observed_at":"2026-09-01T00:00:00Z"}`+"\n"), 0o644)

	h, err := New(dir).Load()
	if err != nil {
		t.Fatal(err)
	}
	if len(h) != 1 {
		t.Errorf("loaded %d repos, want 1", len(h))
	}
}

// The whole point of the store: on the first run there is nothing to compare
// against, and the trend must say so rather than report a rate of zero.
func TestTrendFirstRunHasNoRate(t *testing.T) {
	h := History{1: {obs(1, 100, base)}}
	tr := h.Trend(1)
	if tr.HasRate || tr.HasAcceleration {
		t.Errorf("one observation cannot support a rate: %+v", tr)
	}
	if tr.Points != 1 {
		t.Errorf("Points = %d, want 1", tr.Points)
	}
}

func TestTrendUnknownRepo(t *testing.T) {
	tr := History{}.Trend(99)
	if tr.HasRate || tr.Points != 0 {
		t.Errorf("got %+v, want an empty trend", tr)
	}
}

func TestTrendTwoPointsGivesRateButNotAcceleration(t *testing.T) {
	h := History{1: {
		obs(1, 100, base),
		obs(1, 170, base.AddDate(0, 0, 7)),
	}}
	tr := h.Trend(1)
	if !tr.HasRate {
		t.Fatal("two observations should give a rate")
	}
	if tr.StarsPerDay != 10 {
		t.Errorf("StarsPerDay = %v, want 10", tr.StarsPerDay)
	}
	if tr.HasAcceleration {
		t.Error("acceleration needs three observations")
	}
	if tr.SpanDays != 7 {
		t.Errorf("SpanDays = %v, want 7", tr.SpanDays)
	}
}

// The signal the tool is actually named for: not growing, but growing faster.
func TestTrendDetectsAcceleration(t *testing.T) {
	h := History{1: {
		obs(1, 100, base),
		obs(1, 170, base.AddDate(0, 0, 7)),  // 10/day
		obs(1, 380, base.AddDate(0, 0, 14)), // 30/day
	}}
	tr := h.Trend(1)
	if !tr.HasAcceleration {
		t.Fatal("three observations should give an acceleration")
	}
	if tr.StarsPerDay != 30 || tr.PrevStarsPerDay != 10 {
		t.Errorf("rates = %v then %v, want 10 then 30", tr.PrevStarsPerDay, tr.StarsPerDay)
	}
	if tr.Acceleration != 20 {
		t.Errorf("Acceleration = %v, want 20", tr.Acceleration)
	}
}

// A project that got popular and levelled off must be distinguishable from one
// still climbing, even though both have a healthy rate.
func TestTrendDetectsDeceleration(t *testing.T) {
	h := History{1: {
		obs(1, 100, base),
		obs(1, 800, base.AddDate(0, 0, 7)),  // 100/day
		obs(1, 870, base.AddDate(0, 0, 14)), // 10/day
	}}
	tr := h.Trend(1)
	if tr.Acceleration >= 0 {
		t.Errorf("Acceleration = %v, want negative", tr.Acceleration)
	}
}

// Two runs in one afternoon say nothing about a trend. Dividing by the minutes
// between them would turn a single new star into a rate of hundreds per day.
func TestTrendMergesSameDayReruns(t *testing.T) {
	dir := t.TempDir()
	s := New(dir)

	if _, err := s.Append([]Observation{obs(1, 100, base)}, base); err != nil {
		t.Fatal(err)
	}
	// A rerun ten minutes later, one star richer.
	if _, err := s.Append([]Observation{obs(1, 101, base.Add(10*time.Minute))}, base); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Append([]Observation{obs(1, 170, base.AddDate(0, 0, 7))}, base.AddDate(0, 0, 7)); err != nil {
		t.Fatal(err)
	}

	h, err := s.Load()
	if err != nil {
		t.Fatal(err)
	}
	if got := len(h[1]); got != 2 {
		t.Fatalf("kept %d observations, want the reruns merged into 2", got)
	}
	tr := h.Trend(1)
	// From the merged 101 to 170 over just under seven days.
	if tr.StarsPerDay < 9 || tr.StarsPerDay > 11 {
		t.Errorf("StarsPerDay = %v, want roughly 10", tr.StarsPerDay)
	}
	if tr.HasAcceleration {
		t.Error("the merged rerun must not masquerade as a third data point")
	}
}

// Stars can be removed as well as added.
func TestTrendHandlesLostStars(t *testing.T) {
	h := History{1: {
		obs(1, 500, base),
		obs(1, 430, base.AddDate(0, 0, 7)),
	}}
	tr := h.Trend(1)
	if tr.StarsPerDay != -10 {
		t.Errorf("StarsPerDay = %v, want -10", tr.StarsPerDay)
	}
}

func TestLoadSortsAcrossFiles(t *testing.T) {
	dir := t.TempDir()
	// Written out of order, and in separate files, as a scheduled job would.
	os.WriteFile(filepath.Join(dir, "2026-09-10.jsonl"),
		[]byte(`{"id":1,"stars":200,"observed_at":"2026-09-10T00:00:00Z"}`+"\n"), 0o644)
	os.WriteFile(filepath.Join(dir, "2026-09-03.jsonl"),
		[]byte(`{"id":1,"stars":100,"observed_at":"2026-09-03T00:00:00Z"}`+"\n"), 0o644)

	h, err := New(dir).Load()
	if err != nil {
		t.Fatal(err)
	}
	if h[1][0].Stars != 100 || h[1][1].Stars != 200 {
		t.Errorf("observations = %+v, want oldest first", h[1])
	}
	if got := h.Trend(1).StarsPerDay; got < 14 || got > 15 {
		t.Errorf("StarsPerDay = %v, want about 14.3", got)
	}
}
