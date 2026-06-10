# sparkwing -- repo conventions

This is the public sparkwing repo. The companion repo `../sparkwing-platform` is private; nothing in here should reference it.

## Comments

This is enforced, not just advised: `internal/commentcheck` runs in the pre-commit gate and fails when the staged change adds a disallowed comment. Run `go run ./internal/commentcheck .` for a whole-tree audit.

- **Two kinds of comment are allowed, nothing else.** Godoc attached to a top-level declaration (package, func, type, const, var, import) or a struct field / interface method; and a tiny set of *tagged* implementation comments. Free-floating comments, body narration, section dividers, and "what" comments that restate the code are rejected.
- **The only allowed inline tags**, each forcing you to justify the comment's existence:
  - `// hack:` -- a deliberate deviation from the obvious/correct approach.
  - `// safety:` -- an invariant that must hold but isn't visible locally (a lock held, an ordering).
  - `// bug:` -- a known defect left in on purpose.
  - `// perf:` -- a non-obvious optimization worth defending.
  One short line each. Anything you'd reach for `// note:` or `// why:` to write doesn't meet the bar -- delete it or make the code say it.
- Don't explain *what* the code does. Names carry that. Don't reference the current task, fix, or callers ("added for X flow", "used by Y") -- those rot.
- **A claim about another package's behavior belongs in a test or a type, never in prose.** "The controller retries three times" rots silently the moment the controller changes; an assertion that retries three times goes red. If you're tempted to write down how code outside your folder behaves, write the assertion instead.
- **Function- and type-level godoc is different -- encouraged.** Document exported APIs properly, including usage examples where they help. The scarcity rule applies to *inline implementation comments*, not godoc. Compiler directives (`//go:embed`, `//nolint:...`) are always fine.

## No internal pointers

- **Never** reference platform tickets: `IMP-`, `CLI-`, `ISS-`, `FRG-`, `ORG-`, `REG-`, `RUN-`, `SDK-`, `LOCAL-`, `TOD-` followed by a number. The platform repo is private; ticket IDs in this repo are dangling pointers for outside readers and look unprofessional in user-visible output (CLI help, docs, error messages).
- No "see commit abc123" or "per Slack thread X" -- same reasoning.
- No "pre-X" / "post-X" milestone phrasings (e.g. `pre-<ticket> binaries`). Describe the behavior directly: "older binaries omit this field", "current wiring".
- If you find a leftover ticket reference while editing, scrub it. Rewrite the surrounding prose so it stands on its own -- don't replace with a new pointer.

## No TODOs

- Don't write `// TODO`, `// FIXME`, `// XXX`, `# TODO:`, `{/* TODO */}` etc. If something needs to be done, do it now or write a real ticket externally. Inline TODOs rot, clutter, and never get triaged.

## Help text and user-visible strings

Plain English. No internal jargon, no ticket IDs, no version-marker noise. CLI help, doc snippets, and error messages are the public face of the tool -- treat them that way.

## Tests

- Test names describe what's being verified, not why the test was added (`TestX_RejectsYWhenZ`, not `TestX_AddedForIMP022`).
- No multi-paragraph block-comment essays at the top of test files. One-line note above a non-obvious assertion is fine.
- Test fixture names (pipeline IDs, run IDs registered via `Register`/`Lookup`) shouldn't encode tickets either -- name them after what they exercise (`step-range-validate`, not `imp007-validate`).

## Docs

- See [DOCS-STYLE.md](DOCS-STYLE.md) for the full guide: the layer model (generate the reference, hand-write only concepts), the source-of-truth rules, and the gates that enforce them.
- `docs/`, `README.md`, CHANGELOG entries: same rules. Self-contained prose, no internal pointers, no "this will be tightened in <ticket>" future-promises.
- **Edit `docs/` -- it's the canonical source.** `pkg/docs/mirror/` is a generated copy of `docs/` (the CLI embeds it via `//go:embed`; the sparkwing-product website also builds from `docs/`). After editing `docs/`, run `bash bin/sync-docs.sh` to regenerate the mirror and commit both. NEVER hand-edit `pkg/docs/mirror/` -- the pre-commit gate and a guard test reject drift.

## Commit messages

Conventional commits (`type: subject`), under 72 chars, no ticket IDs in the subject or body. Valid types: `feat`, `fix`, `refactor`, `test`, `docs`, `chore`.
