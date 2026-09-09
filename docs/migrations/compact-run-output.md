# Compact foreground run output

Piped `sparkwing run` output now contains a start record, up to 20 node
completion records, up to five diagnostics, and one terminal record. A normal
run emits at most 27 display records, each below 32 KiB. Build and setup
diagnostics remain on stderr. Validation can fail before a run exists; such
failures retain their nonzero exit and diagnostic without a terminal record. Child output stays in stored
logs. This changes the default JSON display, including explicit
`SPARKWING_LOG_FORMAT=json` on a terminal.

The terminal `run_finish` record keeps the run ID and actual status. Its
attributes include node outcome counts, duration when available, up to five
failed-node causes, omitted event/failure counts, and commands for status and
logs. Display strings are at most 256 bytes and carry `[truncated]` when cut.
Use the terminal status rather than inferring success from progress records.
The process still exits nonzero for failed or cancelled runs.

Consumers that need every live event must request it:

```sh
sparkwing run checks --sw-verbose
```

For already-running or completed work, read the stored stream:

```sh
sparkwing runs logs --run RUN_ID --follow
```

`SPARKWING_LOG_LEVEL=debug` also selects the complete JSON stream for a
pipeline binary invoked directly. Pretty terminal output and explicit quiet
summaries retain their existing behavior. Detached launch acknowledgments and
node-process transport are unchanged.
