# CLI rules

Conventions every sparkwing command obeys. Edit this file to change what the CLI reviewer enforces -- no code change required.

1. **Every user-facing command supports `--output json` and `--output pretty`.** Pretty is the default (human-readable); `json` emits machine-readable output. A new command that prints results without offering both is a violation.

2. **No positional arguments -- with exactly one exception.** Only `sparkwing run <pipeline>` takes a positional argument (the pipeline name). Every other command and subcommand is flag-only. A new command that takes a positional argument is a violation.

3. **Control flags on `sparkwing run` use the `--sw-` prefix.** Flags that configure `run` itself (not the pipeline) are namespaced `--sw-*` (e.g. `--sw-cd`, `--sw-allow`) so they never collide with parameters a pipeline defines. Pipeline-defined inputs are plain flags. A new `run` control flag without the prefix is a violation.

4. **Flag names are kebab-case.** `--skip-web-bundle`, not `--skipWebBundle` or `--skip_web_bundle`.

5. **Help text and error messages are plain English.** No internal jargon, no platform ticket IDs (`IMP-`, `CLI-`, etc.), no version-marker noise ("pre-X", "post-rewrite"). They are the public face of the tool.

6. **Errors are actionable.** An error states what failed and what the user can do about it. A bare "failed" or a raw wrapped error with no remedy is a violation when the failure is one the user can fix.
