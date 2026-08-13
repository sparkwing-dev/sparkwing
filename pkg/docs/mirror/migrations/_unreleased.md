# Migrating to the next release

Staging ground for the breaking changes sitting in `[Unreleased]`. The
pre-release manicuring agent moves these sections into
`docs/migrations/v<X.Y.Z>.md` when the version is cut; until then the
CHANGELOG links here.

## List output is NDJSON

Every list-shaped `-o json` output is now newline-delimited JSON: one
complete, independently parseable object per line, with no enclosing
array and no pretty-printing. The migration is mechanical -- decode line
by line instead of decoding the whole document -- and takes about a
minute per consumer. `pretty`, `plain`, `markdown`, and `--quiet`
non-JSON output are untouched, as are the single-object verbs
(`runs status`, `runs get`, `runs receipt`, `pipeline describe`,
`queue`, `doctor`, `version`, `info`, ...), which still emit one
pretty-printed object.

**Before:**

```json
[
  {
    "path": "sparkwing runs list",
    "synopsis": "Recent runs, newest first"
  },
  {
    "path": "sparkwing runs status",
    "synopsis": "One run's nodes and outcome"
  }
]
```

**After:**

```json
{"path":"sparkwing runs list","synopsis":"Recent runs, newest first"}
{"path":"sparkwing runs status","synopsis":"One run's nodes and outcome"}
```

**Steps:**

1. Read the stream a line at a time -- `json.Decoder` in a loop, `jq -c
   .` with no `-s`, `while read line` in shell -- instead of
   unmarshalling the whole output into a slice. In Go, `json.Decoder`
   already does this: call `Decode` in a loop until `io.EOF` and drop
   the surrounding slice type.
2. Drop any `[0]`-style indexing into the top-level array. Each line
   *is* a record, and every field it used to carry inside the array is
   still on it, unrenamed.
3. `jq` consumers: `jq '.[] | .id'` becomes `jq '.id'`. To get the old
   array back, pipe through `jq -s .`.
4. Anything that counted records with `len(...)` or `length` counts
   lines instead.

**Why:** a caller's only defense against output too large for its
context is `head`, and `head` is line-oriented. `sparkwing commands -o
json` was 258KB across 6,439 pretty-printed lines, and `AGENTS.md`
points an arriving agent straight at it -- so the repository's own
orientation path handed a fresh agent a quarter-megabyte document whose
first five lines parse as nothing at all. NDJSON makes a truncated read
lossy but still valid: `sparkwing commands -o json | head -5` is now
five complete command records. This is house rule 12 of the CLI design
standard (list output is one record per line, in every mode).

**Gotchas:**

- An empty listing is now an empty stream -- zero bytes -- where it used
  to be `[]` or, at a few sites, `null`. A consumer that treated empty
  output as an error has to treat it as zero records; success is still
  carried by the exit code, which is where it always belonged.
- Nothing was renamed and nothing was dropped. If a field was on a
  record inside the old array, it is on that record's line now.
- `--quiet -o json` on `runs list`, `runs find`, and `runs grep` was a
  JSON array of id strings and is now one JSON-quoted id per line
  (`"run-2026…"`), which is the plain `--quiet` output with quotes.

**Affected commands:** `commands`, `runs list` (including
`--by-pipeline` and `--quiet`), `runs errors`, `runs failures`
(including `--group-by`), `runs stats` (including `--capacity`), `runs
find`, `runs grep`, `runs annotations list`, `runs approvals list`,
`runs triggers list`, `pipeline list`, `pipeline discover`, `pipeline
lint` (findings and `--rules`), `pipeline explain --all`, `pipeline
publish`, `pipeline hooks survey`, `pipeline hooks fire`, `cluster
tokens list`, `cluster agents list`, `cluster webhooks list`, `cluster
webhooks deliveries`, `configure xrepo list`, `examples`, `docs list`,
`docs guides`, `docs search`, `docs versions`, `docs migrations list`,
`repos`, and `repos update`.
