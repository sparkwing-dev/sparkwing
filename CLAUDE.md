# sparkwing — repo conventions

This is the public sparkwing repo. The companion repo `../sparkwing-platform` is private; nothing in here should reference it.

## Comments
- Default to none. Add an inline comment only when the *why* is non-obvious — a hidden constraint, a workaround, surprising behavior. If removing it wouldn't confuse a future reader, don't write it.
- Don't explain *what* the code does. Names carry that.
- One short line max for inline comments inside function bodies. No multi-paragraph block comments mid-function.
- Don't reference the current task, fix, or callers ("added for X flow", "used by Y", "handles case from Z"). Those rot.
- **Function- and type-level godoc is different — encouraged.** Document exported APIs properly, including usage examples where they help. The minimalism rule applies to *inline implementation comments*, not godoc.

## No internal pointers
- **Never** reference platform tickets: `IMP-`, `CLI-`, `ISS-`, `FRG-`, `ORG-`, `REG-`, `RUN-`, `SDK-`, `LOCAL-`, `TOD-` followed by a number. The platform repo is private; ticket IDs in this repo are dangling pointers for outside readers and look unprofessional in user-visible output (CLI help, docs, error messages).
- No "see commit abc123" or "per Slack thread X" — same reasoning.
- No "pre-X" / "post-X" milestone phrasings (`pre-IMP-011 binaries`). Describe the behavior directly: "older binaries omit this field", "current wiring".
- If you find a leftover ticket reference while editing, scrub it. Rewrite the surrounding prose so it stands on its own — don't replace with a new pointer.

## No TODOs
- Don't write `// TODO`, `// FIXME`, `// XXX`, `# TODO:`, `{/* TODO */}` etc. If something needs to be done, do it now or write a real ticket externally. Inline TODOs rot, clutter, and never get triaged.

## Help text and user-visible strings
Plain English. No internal jargon, no ticket IDs, no version-marker noise. CLI help, doc snippets, and error messages are the public face of the tool — treat them that way.

## Tests
- Test names describe what's being verified, not why the test was added (`TestX_RejectsYWhenZ`, not `TestX_AddedForIMP022`).
- No multi-paragraph block-comment essays at the top of test files. One-line note above a non-obvious assertion is fine.
- Test fixture names (pipeline IDs, run IDs registered via `Register`/`Lookup`) shouldn't encode tickets either — name them after what they exercise (`step-range-validate`, not `imp007-validate`).

## Docs
- `docs/`, `pkg/docs/content/`, `README.md`, CHANGELOG entries: same rules. Self-contained prose, no internal pointers, no "this will be tightened in <ticket>" future-promises.

## Commit messages
Conventional commits (`type: subject`), under 72 chars, no ticket IDs in the subject or body. Valid types: `feat`, `fix`, `refactor`, `test`, `docs`, `chore`.
