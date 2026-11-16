### Docs

- **docs (TypeScript):** The SDK page is now "Pipeline SDK" and pairs every Go
  example with its TypeScript form, including retries and timeouts, fan-out,
  child runs, secrets, caches and artifacts. The caching, artifacts, secrets,
  pipelines and authoring pages gain TypeScript forms too. A new
  [TypeScript API reference](docs/sdk-typescript-reference.md) is generated from the
  package's `index.d.ts`, so `sparkwing docs read --topic sdk-typescript-reference`
  lists every export without opening `node_modules`.
- **cli:** `pipeline new --template typescript` and
  `--template merge-queue-typescript` open the pipeline file with a comment
  naming the TypeScript docs, and `info --for-agent` names the TypeScript API
  reference.
