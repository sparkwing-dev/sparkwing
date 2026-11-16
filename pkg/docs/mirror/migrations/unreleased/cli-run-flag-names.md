## Run and plan flags lose their prefix

**Before:** `sparkwing run` and `sparkwing pipeline plan` spelled their own
flags with a `--sw-` prefix, while `pipeline trigger` used plain names such as
`--detach` and `--ref`.

**After:** the three commands share one set of plain names. Pipeline arguments
still follow `--`, so a pipeline keeps every flag name for itself. The old
spellings are refused with a usage error that names the new flag, and nothing
runs.

| Before | After |
|---|---|
| `--sw-detached` | `--detach` |
| `--sw-dry-run` | `--dry-run` (also on `pipeline plan`) |
| `--sw-ref` | `--ref` |
| `--sw-pipeline-ref` | `--pipeline-ref` |
| `--sw-route` | `--route` |
| `--sw-allow-risk` | `--allow-risk` |
| `--sw-allow-unadmitted` | `--allow-unadmitted` |
| `--sw-only` | `--only` |
| `--sw-start-at` | `--start-at` |
| `--sw-stop-at` | `--stop-at` |
| `--sw-no-memo` | `--no-memo` |
| `--sw-workers` | `--workers` |
| `--sw-priority` | `--priority` |
| `--sw-no-update` | `--no-update` |
| `--sw-no-bincache` | `--no-bincache` |
| `--sw-hash-all-files` | `--hash-all-files` |
| `--sw-cache-from` | `--cache-from` |
| `--sw-pause-before` | `--pause-before` |
| `--sw-pause-after` | `--pause-after` |
| `--sw-pause-on-failure` | `--pause-on-failure` |
| `--sw-idempotency-key` | `--idempotency-key` |
| `--sw-request-id` | `--request-id` |
| `--sw-run-handle-file` | `--run-handle-file` |
| `--sw-index` | `--index` |
| `--sw-verbose` (`-v`) | `--verbose` (`-v`) |

Update every script, CI workflow, git hook and cron entry that starts a run.
For example, a standalone CI step becomes:

```sh
sparkwing run release --allow-unadmitted --workers=4 --no-update
```

and a detached, risk-authorized run becomes:

```sh
sparkwing run deploy --detach --allow-risk destructive,prod -- --region us-east
```

**Edge cases:** a pipeline whose SDK pin selects an engine older than this
release still names the old spellings in engine-side refusals, such as a step
that needs `--allow-risk`. Pass the new flag names to the CLI regardless. The
internal SDK protocol arguments keep their names.
