<!-- GENERATED from internal/capability.Routes() by internal/apiref. Do not edit by hand; regenerate with go run ./internal/apiref > docs/api-reference.md. -->
# HTTP API reference

<!-- design:begin -->

The shared capability policy defines the controller HTTP and local API socket contract. Each row retains its audience, ownership lookup, authentication, retry policy and complete access alternatives. See [Controller HTTP API](api.md) for request and response contracts and [Authentication](auth.md) for credential issuance. Native controller bindings require separate integration evidence.

An access alternative is `credential / grants / binding / liveness`. Every grant joined with `+` is required. Alternatives separated by a line break stand alone. Grants, bindings and liveness exceptions cannot be combined across alternatives. External authentication has no capability alternative.

The owner names the server lookup. Bindings and current authority constrain every caller, including deployment administrators and local peers. An operator-console session is separate from a deployment-admin capability. `safe`, `exact_body` and `idempotency_key` identify eligible transport retries. A client may further refuse replay when a response carries one-time authority. `never` forbids automatic replay.

| Method | Path | Audience | Owner | Authentication | Retry | Access alternatives |
|---|---|---|---|---|---|---|
| `GET` | `/api/v1/runs/{run}/local-host/data-authority` | `controller` | `run` | `capability` | `safe` | `awarded_or_delegated_claim` / `data.read` / `runOfTrigger` / `sensitive`<br>`awarded_or_delegated_claim` / `data.read` / `planOwner` / `sensitive`<br>`awarded_or_delegated_claim` / `data.read` / `sourceReport` / `sensitive` |
| `GET` | `/api/v1/runs/{run}/local-host/nodes/{job}/data-authority` | `controller` | `run` | `capability` | `safe` | `awarded_or_delegated_claim` / `data.read` / `ownNode` / `sensitive` |
| `POST` | `/api/v1/local/doctor` | `controller` | `team` | `capability` | `never` | `unbound_member_or_local` / `runs.control` / `none` / `credential` |
| `GET` | `/api/v1/runs/{run}/local-host/nodes/{job}/snapshot` | `controller` | `run` | `capability` | `safe` | `awarded_or_delegated_claim` / `data.read` / `ownNode` / `reporting`<br>`awarded_or_delegated_claim` / `data.read` / `planOwner` / `reporting`<br>`eval_unit` / `expansion.submit` / `run` / `expansion` |
| `POST` | `/api/v1/runs/{run}/local-host/nodes/{job}/authentication` | `controller` | `run` | `capability` | `safe` | `awarded_or_delegated_claim` / `data.read` / `ownNode` / `reporting`<br>`awarded_or_delegated_claim` / `data.read` / `planOwner` / `reporting`<br>`eval_unit` / `expansion.submit` / `run` / `expansion` |
| `POST` | `/api/v1/runs/{run}/local-host/nodes/{job}/binding` | `controller` | `run` | `capability` | `safe` | `awarded_or_delegated_claim` / `data.read` / `ownNode` / `reporting`<br>`awarded_or_delegated_claim` / `data.read` / `planOwner` / `reporting`<br>`eval_unit` / `expansion.submit` / `run` / `expansion` |
| `GET` | `/api/v1/runs/{run}/local-host/snapshot` | `controller` | `run` | `capability` | `safe` | `awarded_or_delegated_claim` / `trigger.host` / `runOfTrigger` / `reporting`<br>`awarded_or_delegated_claim` / `data.read` / `planOwner` / `reporting`<br>`eval_unit` / `expansion.submit` / `run` / `expansion`<br>`verified_local_peer` / `runs.read` / `none` / `credential` |
| `GET` | `/api/v1/runs/{run}/local-host/nodes/{job}` | `controller` | `run` | `capability` | `safe` | `awarded_or_delegated_claim` / `trigger.host` / `runOfTrigger` / `reporting`<br>`awarded_or_delegated_claim` / `data.read` / `ownNode` / `reporting`<br>`awarded_or_delegated_claim` / `data.read` / `planOwner` / `reporting`<br>`eval_unit` / `expansion.submit` / `run` / `expansion` |
| `POST` | `/api/v1/runs/{run}/local-host/transition` | `controller` | `run` | `capability` | `exact_body` | `awarded_or_delegated_claim` / `runs.state` / `runOfTrigger` / `result` |
| `POST` | `/api/v1/runs/{run}/local-host/finish` | `controller` | `run` | `capability` | `exact_body` | `awarded_or_delegated_claim` / `runs.state` / `runOfTrigger` / `result`<br>`awarded_or_delegated_claim` / `node.result` / `planOwner` / `result` |
| `POST` | `/api/v1/runs/{run}/local-host/nodes/{job}/transition` | `controller` | `run` | `capability` | `exact_body` | `awarded_or_delegated_claim` / `node.result` / `ownNode` / `result`<br>`awarded_or_delegated_claim` / `node.result` / `runOfTrigger` / `result` |
| `POST` | `/api/v1/runs/{run}/local-host/authentication` | `controller` | `run` | `capability` | `safe` | `awarded_or_delegated_claim` / `trigger.host` / `runOfTrigger` / `reporting`<br>`awarded_or_delegated_claim` / `data.read` / `planOwner` / `reporting`<br>`eval_unit` / `expansion.submit` / `run` / `expansion` |
| `POST` | `/api/v1/runs/{run}/local-host/binding` | `controller` | `run` | `capability` | `safe` | `awarded_or_delegated_claim` / `trigger.host` / `runOfTrigger` / `reporting`<br>`awarded_or_delegated_claim` / `data.read` / `planOwner` / `reporting`<br>`eval_unit` / `expansion.submit` / `run` / `expansion` |
| `GET` | `/api/v1/runs/{run}/snapshot` | `controller` | `run` | `capability` | `safe` | `awarded_or_delegated_claim` / `trigger.host` / `runOfTrigger` / `sensitive` |
| `POST` | `/api/v1/runs/{run}/jobs/{job}/reconcile` | `controller` | `run` | `capability` | `never` | `awarded_or_delegated_claim` / `nodes.claim` / `runOfTrigger` / `sensitive` |
| `POST` | `/api/v1/runs` | `controller` | `team` | `capability` | `idempotency_key` | `unbound_member_or_local` / `runs.write` / `bodyNamesRun` / `credential`<br>`awarded_or_delegated_claim` / `children.start` / `bodyNamesRun` / `sensitive` |
| `GET` | `/api/v1/runs` | `controller` | `team` | `capability` | `safe` | `unbound_member_or_local` / `runs.read` / `none` / `credential` |
| `GET` | `/api/v1/runs/grep` | `controller` | `team` | `capability` | `safe` | `unbound_member_or_local` / `logs.read` / `none` / `credential` |
| `GET` | `/api/v1/runs/{run}` | `controller` | `run` | `capability` | `safe` | `unbound_member_or_local` / `runs.read` / `none` / `credential`<br>`awarded_or_delegated_claim` / `source.read` / `run` / `sensitive`<br>`awarded_or_delegated_claim` / `trigger.host` / `run` / `sensitive` |
| `DELETE` | `/api/v1/runs/{run}` | `controller` | `run` | `capability` | `safe` | `unbound_member_or_local` / `runs.control` / `none` / `credential` |
| `POST` | `/api/v1/runs/{run}/cancel` | `controller` | `run` | `capability` | `safe` | `unbound_member_or_local` / `runs.control` / `none` / `credential` |
| `POST` | `/api/v1/runs/{run}/jobs/{job}/cancel` | `controller` | `run` | `capability` | `safe` | `unbound_member_or_local` / `runs.control` / `none` / `credential`<br>`awarded_or_delegated_claim` / `trigger.host` / `runOfTrigger` / `reporting` |
| `GET` | `/api/v1/runs/{run}/jobs/{job}/failure` | `controller` | `run` | `capability` | `safe` | `awarded_or_delegated_claim` / `data.read` / `readSet` / `sensitive` |
| `POST` | `/api/v1/runs/{run}/retry` | `controller` | `run` | `capability` | `idempotency_key` | `unbound_member_or_local` / `runs.control` / `none` / `credential` |
| `GET` | `/api/v1/runs/{run}/receipt` | `controller` | `run` | `capability` | `safe` | `unbound_member_or_local` / `runs.read` / `none` / `credential`<br>`awarded_or_delegated_claim` / `source.read` / `run` / `sensitive`<br>`awarded_or_delegated_claim` / `trigger.host` / `run` / `sensitive` |
| `GET` | `/api/v1/runs/{run}/attempts` | `controller` | `run` | `capability` | `safe` | `unbound_member_or_local` / `runs.read` / `none` / `credential`<br>`awarded_or_delegated_claim` / `source.read` / `run` / `sensitive`<br>`awarded_or_delegated_claim` / `trigger.host` / `run` / `sensitive` |
| `GET` | `/api/v1/runs/{run}/steps` | `controller` | `run` | `capability` | `safe` | `unbound_member_or_local` / `runs.read` / `none` / `credential`<br>`awarded_or_delegated_claim` / `source.read` / `run` / `sensitive`<br>`awarded_or_delegated_claim` / `trigger.host` / `run` / `sensitive` |
| `GET` | `/api/v1/runs/{run}/events` | `controller` | `run` | `capability` | `safe` | `unbound_member_or_local` / `runs.read` / `none` / `credential`<br>`awarded_or_delegated_claim` / `trigger.host` / `run` / `sensitive` |
| `GET` | `/api/v1/runs/{run}/events/stream` | `controller` | `run` | `capability` | `safe` | `unbound_member_or_local` / `runs.read` / `none` / `credential`<br>`awarded_or_delegated_claim` / `trigger.host` / `run` / `sensitive` |
| `GET` | `/api/v1/runs/{run}/approvals` | `controller` | `run` | `capability` | `safe` | `unbound_member_or_local` / `runs.read` / `none` / `credential`<br>`awarded_or_delegated_claim` / `source.read` / `run` / `sensitive`<br>`awarded_or_delegated_claim` / `trigger.host` / `run` / `sensitive` |
| `GET` | `/api/v1/runs/{run}/debug-pauses` | `controller` | `run` | `capability` | `safe` | `unbound_member_or_local` / `runs.read` / `none` / `credential`<br>`awarded_or_delegated_claim` / `source.read` / `run` / `sensitive`<br>`awarded_or_delegated_claim` / `trigger.host` / `run` / `sensitive` |
| `POST` | `/api/v1/runs/{run}/debug-pauses` | `controller` | `run` | `capability` | `idempotency_key` | `unbound_member_or_local` / `runs.control` / `none` / `credential` |
| `GET` | `/api/v1/runs/{run}/jobs` | `controller` | `run` | `capability` | `safe` | `unbound_member_or_local` / `runs.read` / `none` / `credential`<br>`awarded_or_delegated_claim` / `trigger.host` / `run` / `sensitive` |
| `POST` | `/api/v1/runs/{run}/plan` | `controller` | `run` | `capability` | `exact_body` | `awarded_or_delegated_claim` / `node.result` / `planOwner` / `result`<br>`local_run_publisher` / `data.write` / `data` / `credential` |
| `POST` | `/api/v1/runs/{run}/finish` | `controller` | `run` | `capability` | `exact_body` | `awarded_or_delegated_claim` / `runs.state` / `runOfTrigger` / `result`<br>`local_run_publisher` / `data.write` / `data` / `credential` |
| `POST` | `/api/v1/runs/{run}/heartbeat` | `controller` | `run` | `capability` | `safe` | `awarded_or_delegated_claim` / `trigger.host` / `runOfTrigger` / `reporting` |
| `POST` | `/api/v1/runs/{run}/claim` | `controller` | `run` | `capability` | `never` | `here_award` / `trigger.host` / `runOfTrigger` / `hereClaim`<br>`any_live` / `triggers.claim` / `claimSelection` / `credential` |
| `POST` | `/api/v1/runs/{run}/source-credential` | `controller` | `run` | `capability` | `never` | `awarded_or_delegated_claim` / `source.read` / `source` / `sensitive`<br>`pod_bootstrap` / `source.read` / `source` / `sensitive` |
| `POST` | `/api/v1/runs/{run}/oidc-token` | `controller` | `run` | `capability` | `never` | `awarded_or_delegated_claim` / `oidc.mint` / `run` / `sensitive` |
| `POST` | `/api/v1/runs/{run}/children` | `controller` | `run` | `capability` | `idempotency_key` | `awarded_or_delegated_claim` / `children.start` / `bodyNamesRun` / `sensitive` |
| `GET` | `/api/v1/runs/{run}/children/{child}` | `controller` | `run` | `capability` | `safe` | `awarded_or_delegated_claim` / `children.start` / `childOfBoundNode` / `sensitive` |
| `GET` | `/api/v1/runs/{run}/children/{child}/jobs/{job}/output` | `controller` | `run` | `capability` | `safe` | `awarded_or_delegated_claim` / `refs.read` / `childOfBoundNode` / `sensitive` |
| `GET` | `/api/v1/runs/{run}/logs` | `controller` | `run` | `capability` | `safe` | `unbound_member_or_local` / `logs.read` / `none` / `credential`<br>`awarded_or_delegated_claim` / `trigger.host` / `run` / `sensitive` |
| `GET` | `/api/v1/runs/{run}/logs/search` | `controller` | `run` | `capability` | `safe` | `unbound_member_or_local` / `logs.read` / `none` / `credential`<br>`awarded_or_delegated_claim` / `trigger.host` / `run` / `sensitive` |
| `GET` | `/api/v1/runs/{run}/jobs/{job}` | `controller` | `run` | `capability` | `safe` | `unbound_member_or_local` / `runs.read` / `none` / `credential`<br>`awarded_or_delegated_claim` / `data.read` / `ownNode` / `sensitive`<br>`awarded_or_delegated_claim` / `trigger.host` / `runOfTrigger` / `sensitive` |
| `GET` | `/api/v1/runs/{run}/jobs/{job}/attempts` | `controller` | `run` | `capability` | `safe` | `unbound_member_or_local` / `runs.read` / `none` / `credential`<br>`awarded_or_delegated_claim` / `data.read` / `ownNode` / `sensitive`<br>`awarded_or_delegated_claim` / `trigger.host` / `runOfTrigger` / `sensitive` |
| `GET` | `/api/v1/runs/{run}/jobs/{job}/steps` | `controller` | `run` | `capability` | `safe` | `unbound_member_or_local` / `runs.read` / `none` / `credential`<br>`awarded_or_delegated_claim` / `data.read` / `ownNode` / `sensitive`<br>`awarded_or_delegated_claim` / `trigger.host` / `runOfTrigger` / `sensitive` |
| `GET` | `/api/v1/runs/{run}/jobs/{job}/metrics` | `controller` | `run` | `capability` | `safe` | `unbound_member_or_local` / `runs.read` / `none` / `credential`<br>`awarded_or_delegated_claim` / `data.read` / `ownNode` / `sensitive`<br>`awarded_or_delegated_claim` / `trigger.host` / `runOfTrigger` / `sensitive` |
| `GET` | `/api/v1/runs/{run}/jobs/{job}/debug-pause` | `controller` | `run` | `capability` | `safe` | `unbound_member_or_local` / `runs.read` / `none` / `credential`<br>`awarded_or_delegated_claim` / `data.read` / `ownNode` / `sensitive`<br>`awarded_or_delegated_claim` / `trigger.host` / `runOfTrigger` / `sensitive` |
| `GET` | `/api/v1/runs/{run}/jobs/{job}/logs` | `controller` | `run` | `capability` | `safe` | `unbound_member_or_local` / `logs.read` / `none` / `credential`<br>`awarded_or_delegated_claim` / `data.read` / `ownNode` / `sensitive`<br>`awarded_or_delegated_claim` / `trigger.host` / `runOfTrigger` / `sensitive` |
| `GET` | `/api/v1/runs/{run}/jobs/{job}/logs/stream` | `controller` | `run` | `capability` | `safe` | `unbound_member_or_local` / `logs.read` / `none` / `credential`<br>`awarded_or_delegated_claim` / `data.read` / `ownNode` / `sensitive`<br>`awarded_or_delegated_claim` / `trigger.host` / `runOfTrigger` / `sensitive` |
| `GET` | `/api/v1/runs/{run}/jobs/{job}/logs/completeness` | `controller` | `run` | `capability` | `safe` | `unbound_member_or_local` / `logs.read` / `none` / `credential`<br>`awarded_or_delegated_claim` / `data.read` / `ownNode` / `sensitive`<br>`awarded_or_delegated_claim` / `trigger.host` / `runOfTrigger` / `sensitive` |
| `GET` | `/api/v1/runs/{run}/jobs/{job}/output` | `controller` | `run` | `capability` | `safe` | `awarded_or_delegated_claim` / `data.read` / `readSet` / `sensitive`<br>`awarded_or_delegated_claim` / `data.read` / `readSet` / `expansion`<br>`awarded_or_delegated_claim` / `refs.read` / `readSet` / `sensitive`<br>`unbound_member_or_local` / `runs.read` / `none` / `credential` |
| `POST` | `/api/v1/runs/{run}/jobs/{job}/transition` | `controller` | `run` | `capability` | `exact_body` | `awarded_or_delegated_claim` / `node.result` / `ownNode` / `result`<br>`awarded_or_delegated_claim` / `node.result` / `runOfTrigger` / `result`<br>`local_run_publisher` / `data.write` / `data` / `credential` |
| `POST` | `/api/v1/runs/{run}/jobs/{job}/claim` | `controller` | `run` | `capability` | `never` | `awarded_or_delegated_claim` / `nodes.claim` / `runOfTrigger` / `sensitive` |
| `POST` | `/api/v1/runs/{run}/jobs/{job}/heartbeat` | `controller` | `run` | `capability` | `safe` | `awarded_or_delegated_claim` / `node.report` / `ownNode` / `reporting`<br>`awarded_or_delegated_claim` / `node.report` / `runOfTrigger` / `reporting` |
| `POST` | `/api/v1/runs/{run}/jobs/{job}/expansion` | `controller` | `run` | `capability` | `exact_body` | `awarded_or_delegated_claim` / `expansion.submit` / `expansion` / `expansion`<br>`awarded_or_delegated_claim` / `expansion.submit` / `runOfTrigger` / `sensitive` |
| `POST` | `/api/v1/runs/{run}/jobs/{job}/logs` | `controller` | `run` | `capability` | `exact_body` | `awarded_or_delegated_claim` / `node.report` / `ownNode` / `reporting`<br>`awarded_or_delegated_claim` / `node.report` / `runOfTrigger` / `reporting`<br>`local_run_publisher` / `data.write` / `data` / `credential` |
| `POST` | `/api/v1/runs/{run}/jobs/{job}/metrics` | `controller` | `run` | `capability` | `exact_body` | `awarded_or_delegated_claim` / `node.report` / `ownNode` / `reporting`<br>`awarded_or_delegated_claim` / `node.report` / `runOfTrigger` / `reporting`<br>`local_run_publisher` / `data.write` / `data` / `credential` |
| `POST` | `/api/v1/runs/{run}/jobs/{job}/usage` | `controller` | `run` | `capability` | `exact_body` | `awarded_or_delegated_claim` / `node.report` / `ownNode` / `reporting`<br>`awarded_or_delegated_claim` / `node.report` / `runOfTrigger` / `reporting`<br>`local_run_publisher` / `data.write` / `data` / `credential` |
| `POST` | `/api/v1/runs/{run}/jobs/{job}/activity` | `controller` | `run` | `capability` | `exact_body` | `awarded_or_delegated_claim` / `node.report` / `ownNode` / `reporting`<br>`awarded_or_delegated_claim` / `node.report` / `runOfTrigger` / `reporting`<br>`local_run_publisher` / `data.write` / `data` / `credential` |
| `POST` | `/api/v1/runs/{run}/jobs/{job}/annotations` | `controller` | `run` | `capability` | `exact_body` | `awarded_or_delegated_claim` / `node.report` / `ownNode` / `reporting`<br>`awarded_or_delegated_claim` / `node.report` / `runOfTrigger` / `reporting`<br>`awarded_or_delegated_claim` / `node.report` / `planOwner` / `reporting`<br>`awarded_or_delegated_claim` / `expansion.submit` / `sourceReport` / `expansion`<br>`local_run_publisher` / `data.write` / `data` / `credential` |
| `POST` | `/api/v1/runs/{run}/jobs/{job}/summary` | `controller` | `run` | `capability` | `exact_body` | `awarded_or_delegated_claim` / `node.report` / `ownNode` / `reporting`<br>`awarded_or_delegated_claim` / `node.report` / `runOfTrigger` / `reporting`<br>`awarded_or_delegated_claim` / `node.report` / `planOwner` / `reporting`<br>`awarded_or_delegated_claim` / `expansion.submit` / `sourceReport` / `expansion`<br>`local_run_publisher` / `data.write` / `data` / `credential` |
| `POST` | `/api/v1/runs/{run}/jobs/{job}/steps/start` | `controller` | `run` | `capability` | `exact_body` | `awarded_or_delegated_claim` / `node.report` / `ownNode` / `reporting`<br>`awarded_or_delegated_claim` / `node.report` / `runOfTrigger` / `reporting`<br>`local_run_publisher` / `data.write` / `data` / `credential` |
| `POST` | `/api/v1/runs/{run}/jobs/{job}/steps/finish` | `controller` | `run` | `capability` | `exact_body` | `awarded_or_delegated_claim` / `node.report` / `ownNode` / `reporting`<br>`awarded_or_delegated_claim` / `node.report` / `runOfTrigger` / `reporting`<br>`local_run_publisher` / `data.write` / `data` / `credential` |
| `POST` | `/api/v1/runs/{run}/jobs/{job}/steps/skip` | `controller` | `run` | `capability` | `exact_body` | `awarded_or_delegated_claim` / `node.report` / `ownNode` / `reporting`<br>`awarded_or_delegated_claim` / `node.report` / `runOfTrigger` / `reporting`<br>`local_run_publisher` / `data.write` / `data` / `credential` |
| `POST` | `/api/v1/runs/{run}/jobs/{job}/steps/annotations` | `controller` | `run` | `capability` | `exact_body` | `awarded_or_delegated_claim` / `node.report` / `ownNode` / `reporting`<br>`awarded_or_delegated_claim` / `node.report` / `runOfTrigger` / `reporting`<br>`local_run_publisher` / `data.write` / `data` / `credential` |
| `POST` | `/api/v1/runs/{run}/jobs/{job}/steps/summary` | `controller` | `run` | `capability` | `exact_body` | `awarded_or_delegated_claim` / `node.report` / `ownNode` / `reporting`<br>`awarded_or_delegated_claim` / `node.report` / `runOfTrigger` / `reporting`<br>`local_run_publisher` / `data.write` / `data` / `credential` |
| `POST` | `/api/v1/runs/{run}/jobs/{job}/log-segments` | `controller` | `run` | `capability` | `exact_body` | `awarded_or_delegated_claim` / `log.tail` / `ownNode` / `logTail`<br>`awarded_or_delegated_claim` / `log.tail` / `runOfTrigger` / `logTail`<br>`local_run_publisher` / `data.write` / `data` / `credential` |
| `POST` | `/api/v1/runs/{run}/jobs/{job}/log-manifest` | `controller` | `run` | `capability` | `exact_body` | `awarded_or_delegated_claim` / `log.tail` / `ownNode` / `logTail`<br>`awarded_or_delegated_claim` / `log.tail` / `runOfTrigger` / `logTail`<br>`local_run_publisher` / `data.write` / `data` / `credential` |
| `POST` | `/api/v1/runs/{run}/jobs/{job}/resume` | `controller` | `run` | `capability` | `safe` | `unbound_member_or_local` / `runs.control` / `none` / `credential` |
| `GET` | `/api/v1/runs/{run}/approvals/{job}` | `controller` | `run` | `capability` | `safe` | `unbound_member_or_local` / `runs.read` / `none` / `credential`<br>`awarded_or_delegated_claim` / `data.read` / `ownNode` / `sensitive`<br>`awarded_or_delegated_claim` / `trigger.host` / `runOfTrigger` / `sensitive` |
| `POST` | `/api/v1/runs/{run}/approvals/{job}` | `controller` | `run` | `capability` | `idempotency_key` | `unbound_member_or_local` / `approvals.write` / `none` / `credential` |
| `GET` | `/api/v1/approvals` | `controller` | `team` | `capability` | `safe` | `unbound_member_or_local` / `runs.read` / `none` / `credential` |
| `GET` | `/api/v1/pipelines` | `controller` | `team` | `capability` | `safe` | `unbound_member_or_local` / `runs.read` / `none` / `credential` |
| `GET` | `/api/v1/pipelines/{name}/latest` | `controller` | `team` | `capability` | `safe` | `unbound_member_or_local` / `runs.read` / `none` / `credential`<br>`awarded_or_delegated_claim` / `refs.read` / `readSet` / `sensitive` |
| `GET` | `/api/v1/pipelines/{name}/sizing` | `controller` | `team` | `capability` | `safe` | `unbound_member_or_local` / `runs.read` / `none` / `credential`<br>`awarded_or_delegated_claim` / `source.read` / `pipelineOfBoundRun` / `sensitive` |
| `PUT` | `/api/v1/pipelines/{name}/sizing/pin` | `controller` | `team` | `capability` | `exact_body` | `awarded_or_delegated_claim` / `node.report` / `pipelineOfBoundRun` / `reporting` |
| `POST` | `/api/v1/pipelines/{name}/sizing/observations` | `controller` | `team` | `capability` | `exact_body` | `awarded_or_delegated_claim` / `node.report` / `pipelineOfBoundRun` / `reporting` |
| `POST` | `/api/v1/pipelines/{name}/sizing/contention` | `controller` | `team` | `capability` | `exact_body` | `awarded_or_delegated_claim` / `node.report` / `pipelineOfBoundRun` / `reporting` |
| `POST` | `/api/v1/pipelines/{name}/sizing/waits` | `controller` | `team` | `capability` | `exact_body` | `awarded_or_delegated_claim` / `node.report` / `pipelineOfBoundRun` / `reporting` |
| `GET` | `/api/v1/sizing` | `controller` | `team` | `capability` | `safe` | `unbound_member_or_local` / `runs.read` / `none` / `credential` |
| `GET` | `/api/v1/sizing/explain` | `controller` | `team` | `capability` | `safe` | `unbound_member_or_local` / `runs.read` / `none` / `credential` |
| `GET` | `/api/v1/runners` | `controller` | `team` | `capability` | `safe` | `unbound_member_or_local` / `runs.read` / `none` / `credential` |
| `GET` | `/api/v1/runners/github` | `controller` | `team` | `capability` | `safe` | `unbound_member_or_local` / `runs.read` / `none` / `credential` |
| `GET` | `/api/v1/pools` | `controller` | `team` | `capability` | `safe` | `unbound_member_or_local` / `runs.read` / `none` / `credential` |
| `GET` | `/api/v1/schedules` | `controller` | `team` | `capability` | `safe` | `unbound_member_or_local` / `runs.read` / `none` / `credential` |
| `GET` | `/api/v1/schedules/{schedule}` | `controller` | `team` | `capability` | `safe` | `unbound_member_or_local` / `runs.read` / `none` / `credential` |
| `GET` | `/api/v1/queue` | `controller` | `team` | `capability` | `safe` | `unbound_member_or_local` / `runs.read` / `none` / `credential` |
| `GET` | `/api/v1/queue/groups/{group}` | `controller` | `team` | `capability` | `safe` | `unbound_member_or_local` / `runs.read` / `none` / `credential`<br>`awarded_or_delegated_claim` / `node.report` / `slotOfBoundNode` / `reporting`<br>`awarded_or_delegated_claim` / `runs.state` / `slotOfBoundNode` / `reporting` |
| `GET` | `/api/v1/trends` | `controller` | `team` | `capability` | `safe` | `unbound_member_or_local` / `runs.read` / `none` / `credential` |
| `GET` | `/api/v1/storage` | `controller` | `team` | `capability` | `safe` | `unbound_member_or_local` / `runs.read` / `none` / `credential` |
| `GET` | `/api/v1/credits` | `controller` | `team` | `capability` | `safe` | `unbound_member_or_local` / `runs.read` / `none` / `credential` |
| `GET` | `/api/v1/credits/history` | `controller` | `team` | `capability` | `safe` | `unbound_member_or_local` / `runs.read` / `none` / `credential` |
| `GET` | `/api/v1/credits/settings` | `controller` | `team` | `capability` | `safe` | `unbound_member_or_local` / `runs.read` / `none` / `credential` |
| `GET` | `/api/v1/team/billing` | `controller` | `team` | `capability` | `safe` | `unbound_member_or_local` / `runs.read` / `none` / `credential` |
| `GET` | `/api/v1/team/build-trust` | `controller` | `team` | `capability` | `safe` | `unbound_member_or_local` / `runs.read` / `none` / `credential` |
| `GET` | `/api/v1/team/git-credentials` | `controller` | `team` | `capability` | `safe` | `unbound_member_or_local` / `runs.read` / `none` / `credential` |
| `GET` | `/api/v1/team/github-app` | `controller` | `team` | `capability` | `safe` | `unbound_member_or_local` / `runs.read` / `none` / `credential` |
| `GET` | `/api/v1/team/github-app/installations/{installation}/repositories` | `controller` | `team` | `capability` | `safe` | `unbound_member_or_local` / `runs.read` / `none` / `credential` |
| `GET` | `/api/v1/team/github-app/triggers` | `controller` | `team` | `capability` | `safe` | `unbound_member_or_local` / `runs.read` / `none` / `credential` |
| `GET` | `/api/v1/team/github-app/automation` | `controller` | `team` | `capability` | `safe` | `unbound_member_or_local` / `runs.read` / `none` / `credential` |
| `GET` | `/api/v1/team/github-app/extra-repos` | `controller` | `team` | `capability` | `safe` | `unbound_member_or_local` / `runs.read` / `none` / `credential` |
| `GET` | `/api/v1/team/members` | `controller` | `team` | `capability` | `safe` | `unbound_member_or_local` / `runs.read` / `none` / `credential` |
| `POST` | `/api/v1/runners` | `controller` | `team` | `capability` | `never` | `unbound_member_or_local` / `runs.read` / `none` / `credential` |
| `GET` | `/api/v1/team/settings` | `controller` | `team` | `capability` | `safe` | `unbound_member_or_local` / `runs.read` / `none` / `credential` |
| `PUT` | `/api/v1/team/settings` | `controller` | `team` | `capability` | `safe` | `unbound_member_or_local` / `team.admin` / `none` / `credential` |
| `DELETE` | `/api/v1/runners/{runner}` | `controller` | `team` | `capability` | `safe` | `unbound_member_or_local` / `team.admin` / `none` / `credential` |
| `POST` | `/api/v1/runners/github` | `controller` | `team` | `capability` | `idempotency_key` | `unbound_member_or_local` / `team.admin` / `none` / `credential` |
| `DELETE` | `/api/v1/runners/github/{repository}` | `controller` | `team` | `capability` | `safe` | `unbound_member_or_local` / `team.admin` / `none` / `credential` |
| `POST` | `/api/v1/pools/{pool}/rules` | `controller` | `team` | `capability` | `idempotency_key` | `unbound_member_or_local` / `team.admin` / `none` / `credential` |
| `DELETE` | `/api/v1/pools/{pool}/rules/{rule}` | `controller` | `team` | `capability` | `safe` | `unbound_member_or_local` / `team.admin` / `none` / `credential` |
| `PUT` | `/api/v1/schedules/local/repos` | `controller` | `team` | `capability` | `never` | `unbound_member_or_local` / `runs.control` / `none` / `credential` |
| `POST` | `/api/v1/schedules/local/tick` | `controller` | `team` | `capability` | `never` | `unbound_member_or_local` / `runs.control` / `none` / `credential` |
| `POST` | `/api/v1/schedules/{schedule}/pin` | `controller` | `team` | `capability` | `never` | `unbound_member_or_local` / `runs.control` / `none` / `credential` |
| `POST` | `/api/v1/schedules/{schedule}/unpin` | `controller` | `team` | `capability` | `safe` | `unbound_member_or_local` / `runs.control` / `none` / `credential` |
| `PUT` | `/api/v1/schedules/repos` | `controller` | `team` | `capability` | `safe` | `unbound_member_or_local` / `runs.control` / `none` / `credential` |
| `DELETE` | `/api/v1/schedules/repos` | `controller` | `team` | `capability` | `safe` | `unbound_member_or_local` / `runs.control` / `none` / `credential` |
| `PUT` | `/api/v1/schedules/{schedule}/override` | `controller` | `team` | `capability` | `safe` | `unbound_member_or_local` / `runs.control` / `none` / `credential` |
| `DELETE` | `/api/v1/schedules/{schedule}/override` | `controller` | `team` | `capability` | `safe` | `unbound_member_or_local` / `runs.control` / `none` / `credential` |
| `DELETE` | `/api/v1/schedules/{schedule}` | `controller` | `team` | `capability` | `safe` | `unbound_member_or_local` / `runs.control` / `none` / `credential` |
| `POST` | `/api/v1/schedules/{schedule}/pause` | `controller` | `team` | `capability` | `safe` | `unbound_member_or_local` / `runs.control` / `none` / `credential` |
| `POST` | `/api/v1/schedules/{schedule}/resume` | `controller` | `team` | `capability` | `safe` | `unbound_member_or_local` / `runs.control` / `none` / `credential` |
| `POST` | `/api/v1/schedules/{schedule}/run` | `controller` | `team` | `capability` | `idempotency_key` | `unbound_member_or_local` / `runs.control` / `none` / `credential` |
| `PUT` | `/api/v1/queue/groups/{group}/priority` | `controller` | `team` | `capability` | `safe` | `unbound_member_or_local` / `runs.control` / `none` / `credential` |
| `POST` | `/api/v1/queue/groups/{group}/force-release` | `controller` | `team` | `capability` | `safe` | `unbound_member_or_local` / `runs.control` / `none` / `credential` |
| `POST` | `/api/v1/queue/groups/{group}/acquire` | `controller` | `team` | `capability` | `idempotency_key` | `awarded_or_delegated_claim` / `node.report` / `slotOfBoundNode` / `sensitive`<br>`awarded_or_delegated_claim` / `runs.state` / `slotOfBoundNode` / `sensitive` |
| `POST` | `/api/v1/queue/groups/{group}/heartbeat` | `controller` | `team` | `capability` | `safe` | `awarded_or_delegated_claim` / `node.report` / `slotOfBoundNode` / `reporting`<br>`awarded_or_delegated_claim` / `runs.state` / `slotOfBoundNode` / `reporting` |
| `POST` | `/api/v1/queue/groups/{group}/release` | `controller` | `team` | `capability` | `safe` | `awarded_or_delegated_claim` / `node.report` / `slotOfBoundNode` / `reporting`<br>`awarded_or_delegated_claim` / `runs.state` / `slotOfBoundNode` / `reporting` |
| `POST` | `/api/v1/queue/groups/{group}/cancel-waiter` | `controller` | `team` | `capability` | `safe` | `awarded_or_delegated_claim` / `node.report` / `slotOfBoundNode` / `reporting`<br>`awarded_or_delegated_claim` / `runs.state` / `slotOfBoundNode` / `reporting` |
| `GET` | `/api/v1/queue/groups/{group}/holder` | `controller` | `team` | `capability` | `safe` | `awarded_or_delegated_claim` / `node.report` / `slotOfBoundNode` / `reporting`<br>`awarded_or_delegated_claim` / `runs.state` / `slotOfBoundNode` / `reporting` |
| `GET` | `/api/v1/queue/groups/{group}/resolve` | `controller` | `team` | `capability` | `safe` | `awarded_or_delegated_claim` / `node.report` / `slotOfBoundNode` / `sensitive`<br>`awarded_or_delegated_claim` / `runs.state` / `slotOfBoundNode` / `sensitive` |
| `GET` | `/api/v1/tokens` | `controller` | `team` | `capability` | `safe` | `unbound_member_or_local` / `runs.read` / `none` / `credential` |
| `POST` | `/api/v1/tokens` | `controller` | `team` | `capability` | `never` | `unbound_member_or_local` / `runs.read` / `none` / `credential` |
| `GET` | `/api/v1/tokens/{token}` | `controller` | `team` | `capability` | `safe` | `unbound_member_or_local` / `runs.read` / `ownToken` / `credential` |
| `DELETE` | `/api/v1/tokens/{token}` | `controller` | `team` | `capability` | `safe` | `unbound_member_or_local` / `runs.read` / `ownToken` / `credential` |
| `POST` | `/api/v1/tokens/{token}/rotate` | `controller` | `team` | `capability` | `never` | `unbound_member_or_local` / `runs.read` / `ownToken` / `credential` |
| `GET` | `/api/v1/secrets` | `controller` | `team` | `capability` | `safe` | `unbound_member_or_local` / `runs.read` / `none` / `credential`<br>`unbound_member_or_local` / `team.admin` / `none` / `credential` |
| `POST` | `/api/v1/secrets` | `controller` | `team` | `capability` | `idempotency_key` | `unbound_member_or_local` / `team.admin` / `none` / `credential` |
| `GET` | `/api/v1/secrets/{name}` | `controller` | `team` | `capability` | `safe` | `unbound_member_or_local` / `secrets.read` / `none` / `credential`<br>`unbound_member_or_local` / `team.admin` / `none` / `credential`<br>`awarded_or_delegated_claim` / `secrets.release` / `declaredSecret` / `sensitive` |
| `DELETE` | `/api/v1/secrets/{name}` | `controller` | `team` | `capability` | `safe` | `unbound_member_or_local` / `team.admin` / `none` / `credential` |
| `POST` | `/api/v1/secrets/rotate` | `controller` | `team` | `capability` | `idempotency_key` | `unbound_member_or_local` / `team.admin` / `none` / `credential` |
| `PATCH` | `/api/v1/team` | `controller` | `team` | `capability` | `safe` | `unbound_member_or_local` / `team.admin` / `none` / `credential` |
| `DELETE` | `/api/v1/team` | `controller` | `team` | `capability` | `safe` | `unbound_member_or_local` / `team.admin` / `none` / `credential` |
| `PATCH` | `/api/v1/team/members/{account}` | `controller` | `team` | `capability` | `safe` | `unbound_member_or_local` / `team.admin` / `none` / `credential` |
| `GET` | `/api/v1/team/invitations` | `controller` | `team` | `capability` | `safe` | `unbound_member_or_local` / `team.admin` / `none` / `credential` |
| `POST` | `/api/v1/team/invitations` | `controller` | `team` | `capability` | `idempotency_key` | `unbound_member_or_local` / `team.admin` / `none` / `credential` |
| `DELETE` | `/api/v1/team/invitations/{invitation}` | `controller` | `team` | `capability` | `safe` | `unbound_member_or_local` / `team.admin` / `none` / `credential` |
| `PUT` | `/api/v1/team/build-trust` | `controller` | `team` | `capability` | `safe` | `unbound_member_or_local` / `team.admin` / `none` / `credential` |
| `POST` | `/api/v1/team/git-credentials` | `controller` | `team` | `capability` | `idempotency_key` | `unbound_member_or_local` / `team.admin` / `none` / `credential` |
| `POST` | `/api/v1/team/git-credentials/{host}/confirm` | `controller` | `team` | `capability` | `idempotency_key` | `unbound_member_or_local` / `team.admin` / `none` / `credential` |
| `DELETE` | `/api/v1/team/git-credentials/{host}` | `controller` | `team` | `capability` | `safe` | `unbound_member_or_local` / `team.admin` / `none` / `credential` |
| `GET` | `/api/v1/team/git-credentials/releases` | `controller` | `team` | `capability` | `safe` | `unbound_member_or_local` / `team.admin` / `none` / `credential` |
| `DELETE` | `/api/v1/team/github-app/installations/{installation}` | `controller` | `team` | `capability` | `safe` | `unbound_member_or_local` / `team.admin` / `none` / `credential` |
| `PUT` | `/api/v1/team/github-app/triggers` | `controller` | `team` | `capability` | `safe` | `unbound_member_or_local` / `team.admin` / `none` / `credential` |
| `DELETE` | `/api/v1/team/github-app/triggers` | `controller` | `team` | `capability` | `safe` | `unbound_member_or_local` / `team.admin` / `none` / `credential` |
| `PUT` | `/api/v1/team/github-app/automation` | `controller` | `team` | `capability` | `safe` | `unbound_member_or_local` / `team.admin` / `none` / `credential` |
| `DELETE` | `/api/v1/team/github-app/automation` | `controller` | `team` | `capability` | `safe` | `unbound_member_or_local` / `team.admin` / `none` / `credential` |
| `PUT` | `/api/v1/team/github-app/extra-repos` | `controller` | `team` | `capability` | `safe` | `unbound_member_or_local` / `team.admin` / `none` / `credential` |
| `POST` | `/api/v1/team/billing/checkout` | `controller` | `team` | `capability` | `idempotency_key` | `unbound_member_or_local` / `team.admin` / `none` / `credential` |
| `POST` | `/api/v1/team/billing/standing-request` | `controller` | `team` | `capability` | `never` | `unbound_member_or_local` / `team.admin` / `none` / `credential` |
| `POST` | `/api/v1/team/billing/card` | `controller` | `team` | `capability` | `idempotency_key` | `unbound_member_or_local` / `team.admin` / `none` / `credential` |
| `POST` | `/api/v1/team/billing/pay` | `controller` | `team` | `capability` | `idempotency_key` | `unbound_member_or_local` / `team.admin` / `none` / `credential` |
| `PUT` | `/api/v1/team/billing/budget` | `controller` | `team` | `capability` | `safe` | `unbound_member_or_local` / `team.admin` / `none` / `credential` |
| `DELETE` | `/api/v1/team/members/{account}` | `controller` | `team` | `capability` | `safe` | `unbound_member_or_local` / `runs.read` / `selfOrTeamAdmin` / `credential` |
| `GET` | `/api/v1/overflow` | `controller` | `team` | `capability` | `safe` | `unbound_member_or_local` / `runs.read` / `none` / `credential` |
| `PUT` | `/api/v1/overflow` | `controller` | `team` | `capability` | `safe` | `unbound_member_or_local` / `runs.write` / `none` / `credential` |
| `GET` | `/api/v1/me` | `controller` | `account` | `capability` | `safe` | `account_session` / `` / `none` / `credential` |
| `GET` | `/api/v1/me/team-deletions` | `controller` | `account` | `capability` | `safe` | `account_session` / `` / `none` / `credential` |
| `POST` | `/api/v1/me/active-team` | `controller` | `account` | `capability` | `idempotency_key` | `account_session` / `` / `none` / `credential` |
| `GET` | `/api/v1/me/identities` | `controller` | `account` | `capability` | `safe` | `account_session` / `` / `none` / `credential` |
| `DELETE` | `/api/v1/me/identities/{provider}` | `controller` | `account` | `capability` | `safe` | `account_session` / `` / `none` / `credential` |
| `POST` | `/api/v1/teams` | `controller` | `account` | `capability` | `idempotency_key` | `account_session` / `` / `none` / `credential` |
| `POST` | `/api/v1/invitations/{invitation}/accept` | `controller` | `account` | `capability` | `idempotency_key` | `account_session` / `` / `none` / `credential` |
| `GET` | `/api/v1/auth/whoami` | `controller` | `none` | `capability` | `safe` | `discovery` / `` / `none` / `credential` |
| `GET` | `/api/v1/services` | `controller` | `team` | `capability` | `safe` | `discovery` / `` / `none` / `credential` |
| `POST` | `/api/v1/runners/claim` | `controller` | `team` | `capability` | `never` | `any_live` / `nodes.claim` / `claimSelection` / `credential`<br>`any_live` / `triggers.claim` / `claimSelection` / `credential` |
| `POST` | `/api/v1/launcher/claim` | `controller` | `deployment` | `capability` | `never` | `launcher_service` / `claims.launch` / `claimSelection` / `credential` |
| `POST` | `/api/v1/launcher/sync` | `controller` | `deployment` | `capability` | `safe` | `launcher_service` / `claims.launch` / `claimSelection` / `credential` |
| `POST` | `/api/v1/claims/exchange` | `controller` | `run` | `capability` | `never` | `pod_bootstrap` / `claim.exchange` / `run` / `sensitive` |
| `POST` | `/api/v1/claims/{claim}/heartbeat` | `controller` | `run` | `capability` | `safe` | `awarded_or_delegated_claim` / `node.report` / `ownClaim` / `reporting`<br>`eval_unit` / `expansion.submit` / `ownEval` / `expansion` |
| `POST` | `/api/v1/claims/{claim}/logs` | `controller` | `run` | `capability` | `exact_body` | `awarded_or_delegated_claim` / `log.tail` / `ownClaim` / `logTail`<br>`eval_unit` / `expansion.submit` / `ownEval` / `expansion` |
| `POST` | `/api/v1/claims/{claim}/log-segments` | `controller` | `run` | `capability` | `exact_body` | `awarded_or_delegated_claim` / `log.tail` / `ownClaim` / `logTail`<br>`eval_unit` / `expansion.submit` / `ownEval` / `expansion` |
| `POST` | `/api/v1/claims/{claim}/log-manifest` | `controller` | `run` | `capability` | `exact_body` | `awarded_or_delegated_claim` / `log.tail` / `ownClaim` / `logTail`<br>`eval_unit` / `expansion.submit` / `ownEval` / `expansion` |
| `POST` | `/api/v1/data/download` | `controller` | `team` | `capability` | `safe` | `awarded_or_delegated_claim` / `data.read` / `data` / `sensitive`<br>`awarded_or_delegated_claim` / `data.read` / `data` / `expansion`<br>`repo_cache_reader` / `data.read` / `data` / `credential`<br>`pod_bootstrap` / `source.read` / `sourceBundle` / `sensitive` |
| `POST` | `/api/v1/data/source` | `controller` | `team` | `capability` | `never` | `unbound_member_or_local` / `runs.write` / `none` / `credential` |
| `POST` | `/api/v1/data/source/{upload}/commit` | `controller` | `team` | `capability` | `never` | `unbound_member_or_local` / `runs.write` / `none` / `credential` |
| `POST` | `/api/v1/data/upload` | `controller` | `team` | `capability` | `idempotency_key` | `awarded_or_delegated_claim` / `data.write` / `data` / `sensitive`<br>`local_run_publisher` / `data.write` / `data` / `credential` |
| `POST` | `/api/v1/data/commit` | `controller` | `team` | `capability` | `exact_body` | `awarded_or_delegated_claim` / `data.write` / `data` / `sensitive`<br>`local_run_publisher` / `data.write` / `data` / `credential` |
| `POST` | `/api/v1/data/records` | `controller` | `team` | `capability` | `exact_body` | `awarded_or_delegated_claim` / `data.write` / `data` / `sensitive`<br>`local_run_publisher` / `data.write` / `data` / `credential` |
| `POST` | `/api/v1/data/has-many` | `controller` | `team` | `capability` | `safe` | `awarded_or_delegated_claim` / `data.read` / `data` / `sensitive`<br>`awarded_or_delegated_claim` / `data.read` / `data` / `expansion`<br>`repo_cache_reader` / `data.read` / `data` / `credential` |
| `GET` | `/api/v1/data/records` | `controller` | `team` | `capability` | `safe` | `awarded_or_delegated_claim` / `data.read` / `data` / `sensitive`<br>`awarded_or_delegated_claim` / `data.read` / `data` / `expansion`<br>`repo_cache_reader` / `data.read` / `data` / `credential` |
| `POST` | `/api/v1/data/memo/lookup` | `controller` | `team` | `capability` | `safe` | `awarded_or_delegated_claim` / `data.read` / `data` / `sensitive`<br>`awarded_or_delegated_claim` / `data.read` / `data` / `expansion`<br>`repo_cache_reader` / `data.read` / `data` / `credential` |
| `POST` | `/api/v1/data/depcache/lookup` | `controller` | `team` | `capability` | `safe` | `awarded_or_delegated_claim` / `data.read` / `data` / `sensitive`<br>`awarded_or_delegated_claim` / `data.read` / `data` / `expansion`<br>`repo_cache_reader` / `data.read` / `data` / `credential` |
| `POST` | `/api/v1/data/memo/copy` | `controller` | `team` | `capability` | `exact_body` | `awarded_or_delegated_claim` / `data.read + data.write` / `data` / `sensitive` |
| `GET` | `/api/v1/data/capabilities` | `controller` | `team` | `capability` | `safe` | `discovery` / `` / `none` / `credential` |
| `GET` | `/api/v1/operator/limits` | `controller` | `deployment` | `capability` | `safe` | `deployment_admin` / `operator.limits` / `none` / `credential`<br>`operator_console_session` / `operator.console` / `none` / `credential` |
| `PUT` | `/api/v1/operator/limits` | `controller` | `deployment` | `capability` | `safe` | `deployment_admin` / `operator.limits` / `none` / `credential`<br>`operator_console_session` / `operator.console` / `none` / `credential` |
| `GET` | `/api/v1/operator/signups` | `controller` | `deployment` | `capability` | `safe` | `deployment_admin` / `operator.limits` / `none` / `credential`<br>`operator_console_session` / `operator.console` / `none` / `credential` |
| `PUT` | `/api/v1/operator/signups` | `controller` | `deployment` | `capability` | `safe` | `deployment_admin` / `operator.limits` / `none` / `credential`<br>`operator_console_session` / `operator.console` / `none` / `credential` |
| `GET` | `/api/v1/operator/object-store` | `controller` | `deployment` | `capability` | `safe` | `deployment_admin` / `operator.object_store` / `none` / `credential`<br>`operator_console_session` / `operator.console` / `none` / `credential` |
| `GET` | `/api/v1/operator/object-store/breaker` | `controller` | `deployment` | `capability` | `safe` | `deployment_admin` / `operator.object_store` / `none` / `credential`<br>`operator_console_session` / `operator.console` / `none` / `credential` |
| `POST` | `/api/v1/operator/object-store/reset-breaker` | `controller` | `deployment` | `capability` | `idempotency_key` | `deployment_admin` / `operator.object_store` / `none` / `credential`<br>`operator_console_session` / `operator.console` / `none` / `credential` |
| `PUT` | `/api/v1/operator/object-store/settings` | `controller` | `deployment` | `capability` | `safe` | `deployment_admin` / `operator.object_store` / `none` / `credential`<br>`operator_console_session` / `operator.console` / `none` / `credential` |
| `PUT` | `/api/v1/operator/object-store/quotas/{team}` | `controller` | `deployment` | `capability` | `safe` | `deployment_admin` / `operator.object_store` / `operator_settings` / `credential`<br>`operator_console_session` / `operator.console` / `operator_settings` / `credential` |
| `PUT` | `/api/v1/operator/object-store/quotas/{team}/allowance` | `controller` | `deployment` | `capability` | `safe` | `deployment_admin` / `operator.object_store` / `operator_settings` / `credential`<br>`operator_console_session` / `operator.console` / `operator_settings` / `credential` |
| `GET` | `/api/v1/operator/tokens` | `controller` | `deployment` | `capability` | `safe` | `deployment_admin` / `operator.tokens` / `none` / `credential`<br>`operator_console_session` / `operator.console` / `none` / `credential` |
| `POST` | `/api/v1/operator/tokens` | `controller` | `deployment` | `capability` | `never` | `deployment_admin` / `operator.tokens` / `none` / `credential`<br>`operator_console_session` / `operator.console` / `none` / `credential` |
| `GET` | `/api/v1/operator/tokens/{token}` | `controller` | `deployment` | `capability` | `safe` | `deployment_admin` / `operator.tokens` / `none` / `credential`<br>`operator_console_session` / `operator.console` / `none` / `credential` |
| `DELETE` | `/api/v1/operator/tokens/{token}` | `controller` | `deployment` | `capability` | `safe` | `deployment_admin` / `operator.tokens` / `none` / `credential`<br>`operator_console_session` / `operator.console` / `none` / `credential` |
| `POST` | `/api/v1/operator/tokens/{token}/rotate` | `controller` | `deployment` | `capability` | `never` | `deployment_admin` / `operator.tokens` / `none` / `credential`<br>`operator_console_session` / `operator.console` / `none` / `credential` |
| `POST` | `/api/v1/operator/secrets/rotate` | `controller` | `deployment` | `capability` | `safe` | `deployment_admin` / `operator.tokens` / `none` / `credential`<br>`operator_console_session` / `operator.console` / `none` / `credential` |
| `GET` | `/api/v1/operator/teams` | `controller` | `deployment` | `capability` | `safe` | `operator_console_session` / `operator.console` / `none` / `credential` |
| `POST` | `/api/v1/operator/teams` | `controller` | `deployment` | `capability` | `never` | `deployment_admin` / `operator.tokens` / `none` / `credential`<br>`operator_console_session` / `operator.console` / `none` / `credential` |
| `GET` | `/api/v1/operator/teams/{team}` | `controller` | `deployment` | `capability` | `safe` | `deployment_admin` / `operator.tokens` / `operator_settings` / `credential`<br>`operator_console_session` / `operator.console` / `none` / `credential` |
| `DELETE` | `/api/v1/operator/teams/{team}` | `controller` | `deployment` | `capability` | `safe` | `operator_console_session` / `operator.console` / `none` / `credential` |
| `POST` | `/api/v1/operator/teams/{team}/trust` | `controller` | `deployment` | `capability` | `idempotency_key` | `operator_console_session` / `operator.console` / `none` / `credential` |
| `POST` | `/api/v1/operator/teams/{team}/grants` | `controller` | `deployment` | `capability` | `idempotency_key` | `operator_console_session` / `operator.console` / `none` / `credential` |
| `POST` | `/api/v1/operator/teams/{team}/freeze` | `controller` | `deployment` | `capability` | `idempotency_key` | `operator_console_session` / `operator.console` / `none` / `credential` |
| `POST` | `/api/v1/operator/teams/{team}/unfreeze` | `controller` | `deployment` | `capability` | `idempotency_key` | `operator_console_session` / `operator.console` / `none` / `credential` |
| `POST` | `/api/v1/operator/teams/{team}/refunds` | `controller` | `deployment` | `capability` | `idempotency_key` | `operator_console_session` / `operator.console` / `none` / `credential` |
| `DELETE` | `/api/v1/operator/accounts/{account}` | `controller` | `deployment` | `capability` | `safe` | `operator_console_session` / `operator.console` / `none` / `credential` |
| `GET` | `/api/v1/operator/waitlist` | `controller` | `deployment` | `capability` | `safe` | `operator_console_session` / `operator.console` / `none` / `credential` |
| `POST` | `/api/v1/operator/waitlist/approve` | `controller` | `deployment` | `capability` | `idempotency_key` | `operator_console_session` / `operator.console` / `none` / `credential` |
| `GET` | `/api/v1/operator/standing-requests` | `controller` | `deployment` | `capability` | `safe` | `operator_console_session` / `operator.console` / `none` / `credential` |
| `POST` | `/api/v1/operator/teams/{team}/standing-request/decline` | `controller` | `deployment` | `capability` | `idempotency_key` | `operator_console_session` / `operator.console` / `none` / `credential` |
| `DELETE` | `/api/v1/operator/github-app/installations/{installation}` | `controller` | `deployment` | `capability` | `safe` | `operator_console_session` / `operator.console` / `none` / `credential` |
| `PUT` | `/api/v1/operator/credits/settings` | `controller` | `deployment` | `capability` | `safe` | `operator_console_session` / `operator.console` / `none` / `credential` |
| `POST` | `/api/v1/operator/runners/{runner}/metered` | `controller` | `deployment` | `capability` | `idempotency_key` | `operator_console_session` / `operator.console` / `none` / `credential` |
| `GET` | `/api/v1/health` | `controller` | `none` | `public` | `safe` |  |
| `GET` | `/api/v1/capabilities` | `controller` | `none` | `public` | `safe` |  |
| `POST` | `/webhooks/stripe` | `controller` | `team` | `payment_webhook_signature` | `exact_body` |  |
| `POST` | `/webhooks/github-app` | `controller` | `team` | `github_webhook_hmac` | `exact_body` |  |
| `POST` | `/api/v1/runners/github/exchange` | `controller` | `team` | `github_actions_oidc` | `never` |  |
| `GET` | `/.well-known/openid-configuration` | `controller` | `none` | `public` | `safe` |  |
| `GET` | `/.well-known/jwks.json` | `controller` | `none` | `public` | `safe` |  |
| `PUT` | `/api/v1/data/uploads/{upload}` | `controller` | `team` | `signed_object_url` | `exact_body` |  |
| `GET` | `/api/v1/data/objects/{key...}` | `controller` | `team` | `signed_object_url` | `safe` |  |
| `PUT` | `/api/v1/log-segments/{key...}` | `controller` | `run` | `signed_object_url` | `exact_body` |  |
| `GET` | `/api/v1/auth/session` | `controller` | `account` | `browser_flow_csrf_nonce` | `never` |  |
| `POST` | `/api/v1/auth/logout` | `controller` | `account` | `browser_flow_csrf_nonce` | `never` |  |
| `GET` | `/login` | `controller` | `account` | `browser_flow_csrf_nonce` | `never` |  |
| `POST` | `/login` | `controller` | `account` | `browser_flow_csrf_nonce` | `never` |  |
| `POST` | `/logout` | `controller` | `account` | `browser_flow_csrf_nonce` | `never` |  |
| `GET` | `/auth/{provider}/start` | `controller` | `account` | `browser_flow_csrf_nonce` | `never` |  |
| `GET` | `/auth/{provider}/callback` | `controller` | `account` | `browser_flow_csrf_nonce` | `never` |  |
| `POST` | `/auth/{provider}/link` | `controller` | `account` | `browser_flow_csrf_nonce` | `never` |  |
| `GET` | `/auth/{provider}/link/complete` | `controller` | `account` | `browser_flow_csrf_nonce` | `never` |  |
| `POST` | `/github/app/connect` | `controller` | `account` | `browser_flow_csrf_nonce` | `never` |  |
| `POST` | `/github/app/connect/existing` | `controller` | `account` | `browser_flow_csrf_nonce` | `never` |  |
| `GET` | `/github/app/setup` | `controller` | `account` | `browser_flow_csrf_nonce` | `never` |  |
| `GET` | `/github/app/callback` | `controller` | `account` | `browser_flow_csrf_nonce` | `never` |  |
| `GET` | `/github/app/complete` | `controller` | `account` | `browser_flow_csrf_nonce` | `never` |  |
| `GET` | `/github/app/available` | `controller` | `account` | `browser_flow_csrf_nonce` | `never` |  |
| `POST` | `/github/app/select` | `controller` | `account` | `browser_flow_csrf_nonce` | `never` |  |
| `GET` | `/metrics` | `controller` | `deployment` | `capability` | `safe` | `deployment_admin` / `operator.limits` / `none` / `credential`<br>`operator_console_session` / `operator.console` / `none` / `credential` |
| `GET` | `/node/v1/secrets/{name}` | `nodehost` | `nodehost` | `capability` | `safe` | `nodehost_process` / `secrets.release` / `declaredSecret` / `process`<br>`owner_process` / `secrets.release` / `declaredSecret` / `process` |
| `GET` | `/node/v1/jobs/{job}/output` | `nodehost` | `nodehost` | `capability` | `safe` | `nodehost_process` / `data.read` / `readSet` / `process`<br>`owner_process` / `data.read` / `readSet` / `process` |
| `GET` | `/node/v1/pipelines/{name}/latest` | `nodehost` | `nodehost` | `capability` | `safe` | `nodehost_process` / `refs.read` / `readSet` / `process`<br>`owner_process` / `refs.read` / `readSet` / `process` |
| `POST` | `/node/v1/oidc-token` | `nodehost` | `nodehost` | `capability` | `never` | `nodehost_process` / `oidc.mint` / `ownNode` / `process`<br>`owner_process` / `oidc.mint` / `runOfTrigger` / `process` |
| `POST` | `/node/v1/masks` | `nodehost` | `nodehost` | `capability` | `exact_body` | `nodehost_process` / `node.report` / `ownNode` / `process`<br>`owner_process` / `node.report` / `runOfTrigger` / `process`<br>`owner_process` / `expansion.submit` / `sourceReport` / `process`<br>`owner_process` / `node.report` / `planOwner` / `process` |
| `POST` | `/node/v1/annotations` | `nodehost` | `nodehost` | `capability` | `never` | `nodehost_process` / `node.report` / `ownNode` / `process`<br>`owner_process` / `node.report` / `runOfTrigger` / `process`<br>`owner_process` / `expansion.submit` / `sourceReport` / `process`<br>`owner_process` / `node.report` / `planOwner` / `process` |
| `POST` | `/node/v1/summary` | `nodehost` | `nodehost` | `capability` | `exact_body` | `nodehost_process` / `node.report` / `ownNode` / `process`<br>`owner_process` / `node.report` / `runOfTrigger` / `process`<br>`owner_process` / `expansion.submit` / `sourceReport` / `process`<br>`owner_process` / `node.report` / `planOwner` / `process` |
| `POST` | `/node/v1/children` | `nodehost` | `nodehost` | `capability` | `never` | `nodehost_process` / `children.start` / `bodyNamesRun` / `process`<br>`owner_process` / `children.start` / `bodyNamesRun` / `process` |
| `GET` | `/node/v1/children/{run}/await` | `nodehost` | `nodehost` | `capability` | `safe` | `nodehost_process` / `children.start` / `childOfBoundNode` / `process`<br>`owner_process` / `children.start` / `childOfBoundNode` / `process` |
| `POST` | `/node/v1/cleanups` | `nodehost` | `nodehost` | `capability` | `never` | `nodehost_process` / `node.report` / `ownCleanup` / `process`<br>`owner_process` / `node.report` / `runOfTrigger` / `process`<br>`owner_process` / `expansion.submit` / `sourceReport` / `process`<br>`owner_process` / `node.report` / `planOwner` / `process` |
| `DELETE` | `/node/v1/cleanups/{cleanup}` | `nodehost` | `nodehost` | `capability` | `never` | `nodehost_process` / `node.report` / `ownCleanup` / `process`<br>`owner_process` / `node.report` / `runOfTrigger` / `process`<br>`owner_process` / `expansion.submit` / `sourceReport` / `process`<br>`owner_process` / `node.report` / `planOwner` / `process` |

<!-- design:end -->
