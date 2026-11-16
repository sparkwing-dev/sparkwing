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

**Upgrade the pipeline first.** `sparkwing run` hands its flags to the engine
that the pipeline's SDK pin selects. An engine older than v0.74.0 does not know
the new names, and this CLI no longer accepts the old ones. Until the pin moves,
a run that passes any of these flags is refused before it starts, with an error
that says so. Run this in each repository after updating the CLI:

```sh
sparkwing pipeline upgrade
```

A run that passes none of these flags keeps working on the older engine. The
internal SDK protocol arguments keep their names.
