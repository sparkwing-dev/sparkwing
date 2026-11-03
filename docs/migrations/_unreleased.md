# Migrating to the next release

## Run handle files carry no status

`sparkwing run --sw-run-handle-file PATH` writes the handle once at intake. It
no longer includes `status`, which stayed `pending` after the run finished. The
file now holds only `schema_version`, `run_id` and `pipeline`.

A script that read `status` from the handle reads `run_id` and asks for the
current state instead:

```bash
run_id="$(jq -r .run_id /tmp/build-run.json)"
sparkwing runs status --run "$run_id" -o json
```
