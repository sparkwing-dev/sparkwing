# SDK rules

Conventions for the public `sparkwing` package -- the API pipeline authors program against. Edit this file to change what the SDK reviewer enforces.

1. **Every exported identifier carries godoc**, and the godoc says how and why to use it, not what the code literally does. An exported func/type/const/var with no doc comment, or with a doc comment that just restates the signature, is a violation.

2. **Keep the surface minimal.** Don't export a type, function, method, or field unless a pipeline author outside the package needs it. Prefer unexported helpers. (The api-surface reviewer owns the deep judgment here; flag the obvious cases.)

3. **Don't leak `internal/` types through exported signatures.** A public function that returns or accepts a type from an `internal/` package forces consumers to reach into internals -- a violation.

4. **Changing or removing an exported symbol is a breaking change.** It must be reflected in `CHANGELOG.md` and, when it breaks consumers, a `docs/migrations/` guide. (The release-notes reviewer cross-checks; flag the SDK side.)

5. **Prefer options structs or functional options over long positional parameter lists.** A new exported function with many positional parameters (especially several of the same type, which callers can transpose) is a violation.

6. **Errors wrap with `%w` and carry context.** A returned error that discards the underlying cause, or that gives no context about what operation failed, is a violation.
