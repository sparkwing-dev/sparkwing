# Agent orientation

Keep this file small. Discover current behavior from the system that owns it instead of copying command catalogs or standards here.

- Start with `bitwing info --for-agent`. Find or create the ticket, claim it with `bitwing ticket workon`, and keep that session current while artifacts change.
- Once a diff exists, run `bitwing doc standards route -i <ticket>` and read the returned standards before finishing the change.
- Treat this repository's README, embedded docs, CLI `--help`, and tests as the current product truth. Use their native discovery commands before relying on remembered syntax.
- When behavior changes, update the source documentation and its drift check in the same change.

`AGENTS.md` is the repository's canonical harness guidance. `CLAUDE.md` only imports it with `@AGENTS.md`.
