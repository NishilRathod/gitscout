# gitscout

Finds open-source projects worth *your* attention, personalised against your own
GitHub profile — then writes it up as a dated Markdown digest.

It answers two questions that are easy to ask and tedious to answer by hand:

1. **Where could I land a merged pull request?** Growing projects that
   demonstrably merge outside work, in languages you already write.
2. **What should I build myself?** Well-loved projects nobody maintains any
   more, stacks you have never used that are heating up, and topics missing an
   obvious companion tool.

Zero dependencies — Go standard library only.

```bash
go run ./cmd/gitscout run
```

---

## What it actually measures

The interesting part of this tool is what it refuses to claim.

### GitHub does not expose star history

The stargazers endpoint that used to carry per-star timestamps returns **404
over REST and empty edges over GraphQL** — verified against `facebook/react`, not
assumed. There is no way to ask GitHub how fast a repository grew last week.

So gitscout keeps its own history. Every run appends one JSON line per
repository to `data/snapshots/YYYY-MM-DD.jsonl`. From the second run onward it
can measure a real rate; from the third, acceleration.

On a first run it has nothing to compare against, so it falls back to stars
divided by age — and **says so, in the digest header and against every affected
row**:

> First run — no history to compare against. Every momentum figure below is
> stars divided by age, which flatters anything that launched loudly and then
> stalled.

A lifetime average is not a trend. Presenting one as the other would be the most
misleading thing this tool could do, so it doesn't.

### Trending and contributable are different questions

The two are scored on separate axes and never collapsed into one number, because
collapsing them recommends exactly the wrong repositories.

The case that shaped the design, measured live on 2026-09-03:

| | `ultraworkers/claw-code` | `acme`-shaped healthy project |
|---|---|---|
| Stars | 195,165 | 4,200 |
| Forks | 108,752 | 310 |
| Contributors | **23** | 140 |
| PRs merged in 90d | **1** | 52 |

By popularity it wins by a factor of forty-six. As somewhere to send a pull
request it is a closed shop. gitscout ranks it below the smaller project and
flags the star pattern as inorganic.

### The strongest available signal is who gets merged

A `CONTRIBUTING.md` is cheap to write. Thirty-six different outsiders' patches
landing in ninety days cannot be faked, so **merges from forks** carry more
weight than any stated intention.

Only a pull request opened from a fork counts as outside work. Pushing a branch
to the repository itself needs write access, so in-repo branches are the
maintainers' own work however many maintainers there are. An earlier version
counted every distinct pull-request author, and rated projects that almost never
merge outside work as the most welcoming on the list. Measured on 2026-10-07,
over the previous ninety days:

| | `stablyai/orca` | `headroomlabs-ai/headroom` |
|---|---|---|
| People with PRs merged | 5 | 37 |
| Human PRs merged from forks | **5 of 75** | 72 of 87 |
| Outside authors | **1** | 36 |
| OPEN, counting every author | 0.93 | 0.75 |
| OPEN, counting fork merges | **0.38** | 0.75 |

Two measurements drive the score:

- **Outside authors** — distinct people whose pull requests from a fork were
  merged in ninety days. The largest single component; fifteen earns full marks.
- **Fork share** — the fraction of human-authored merges that came from forks.
  Below a quarter, the whole score is scaled down in proportion, to 0.3 of
  itself at zero, because good-first-issue labels, throughput and a large
  contributor count say nothing about whether an outsider's work would land.
  Projects that genuinely take outside work measured 70–98%; ones that only
  looked busy, 2–11%.

Every row leads its evidence with both, whatever they scored —
`5/75 human PRs merged from forks, 1 outside author` — and a discounted row says
by how much in its caveats. Bot accounts are excluded from both counts — a
repository whose merged PRs are mostly dependency bumps is not thereby welcoming.

The fork test is GraphQL's `isCrossRepository` (head repository differs from the
base), read from the REST pull-request listing enrichment already fetches, so it
costs no extra request. It errs in one direction: maintainers who work from
personal forks count as outsiders.

### Every score shows its arithmetic

No row is a bare number. Each carries the components that produced it and the
caveats that undermine it, because a recommendation engine whose reasoning
cannot be inspected is one you would have to trust — and there is no reason to
trust this one. It is heuristics over public metadata.

The gap signals are the weakest part and are labelled as such on every row. The
tool can show that a project with 97,000 stars has not been pushed to since July
2024. It cannot tell you whether reviving it is a good idea.

---

## Output

```
CONTRIBUTE NOW
  REPOSITORY                LANG    STARS  OPEN  MERGE  FIT   WHY
  headroomlabs-ai/headroom  Python  74.5k  0.75  0.51*  0.67  72/87 human PRs merged from forks, 36 outside autho…
```

- **OPEN** — how readily outside work actually gets merged
- **MERGE** — growth (`*` marks a lifetime average rather than a measured rate)
- **FIT** — how close it is to what you already write, or on the stretch list,
  how far from it

Plus `digests/YYYY-MM-DD.md` with the same content, full evidence, and
collapsible caveats.

## Usage

```bash
gitscout run                             # sweep, score, write today's digest
gitscout run -languages go,rust          # override the profile-derived sweep
gitscout run -min-stars 500 -pages 3     # cast wider
gitscout run -no-digest                  # terminal only
gitscout run -no-holes                   # skip the ecosystem probes, saving search budget
gitscout run -user someoneelse           # personalise against another account
```

Authentication: `GITHUB_TOKEN` if set, otherwise whatever `gh auth token`
returns. A run needs a token — unauthenticated requests are capped at 60/hour.

## How it works

```
discover ──► snapshot ──► score ──► enrich top N ──► score again ──► render
```

**Discover** sweeps a matrix of search queries sliced by language and by intent:
rising repositories, repositories advertising good first issues, repositories
asking for help, and repositories that people wanted and nobody maintains. Search
responses already carry stars, forks, language, topics, licence and timestamps,
so a candidate costs nothing beyond its slice until enrichment.

**Enrich** spends three core-budget requests each on the most promising ~60:
contributor count, recent merged-PR throughput and how much of it came from
forks, and whether contribution guidance exists. It uses no search requests at all — the search
limit is 30/minute, the core limit 5000/hour, so the expensive-looking work goes
where the budget is.

Contributor counts come from one request, not pagination: ask for `per_page=1`
and read the page number out of the `Link: rel="last"` header.

**Score** runs three independent scorers — momentum, contributability, and fit
(which produces *two* numbers, comfort and stretch, because they pull in
opposite directions and a blend would hide which question it answered).

**Profile** weights your languages by bytes written, log-scaled so one vendored
lockfile cannot decide the result, and decayed with a 540-day half-life so what
you wrote last month counts more than what you wrote in 2023. Forks are excluded:
a fork records what you copied, not what you wrote.

## Layout

```
cmd/gitscout/        flags and wiring
internal/github/     HTTP client, rate limiting, search, enrichment
internal/profile/    your repositories -> a language weight vector
internal/store/      JSONL snapshot history and trend maths
internal/score/      momentum, contributability, fit, ranking
internal/gaps/       abandoned projects, stretch stacks, ecosystem holes
internal/render/     terminal tables and Markdown digests
```

Storage is JSON Lines rather than SQLite on purpose: the dataset is a few
thousand rows per run, plain text diffs reviewably in git, and a scheduled job
can append to it without a migration story or a cgo toolchain.

## Tests

```bash
go test ./...
```

Every test runs offline against `httptest` servers and fixtures. The fixtures
are real: `ultraworkers/claw-code`, `nvbn/thefuck`, `FiloSottile/mkcert`,
`stablyai/orca` and `headroomlabs-ai/headroom` are recorded from live API
responses and asserted as named regression cases, so the star-farm filter, the
abandoned-project detector and the fork-merge test cannot silently stop working.

## Licence

MIT
