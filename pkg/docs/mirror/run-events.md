# Run events

Sparkwing posts a signed message to a URL you choose when a run starts or finishes. Use it for chat notifications,
dashboards or anything that should react to a run. Each message is a [CloudEvents 1.0](https://cloudevents.io) JSON
body signed with [Standard Webhooks](https://www.standardwebhooks.com) headers.

## Subscribe

A team administrator subscribes a URL:

```bash
sparkwing subscriptions create --url https://hooks.example.com/sparkwing --status failed --profile cloud
```

The command prints the subscription's signing secret once. Store it where your receiver reads it; rotate it with
`sparkwing subscriptions rotate-secret`. Filters narrow what is sent:

- `--event run.started` or `--event run.finished`. Without `--event`, both are sent, or only `run.finished` when
  `--status` is given.
- `--status success`, `failed`, `cancelled` or `dry_run` selects which finished runs are sent.
- `--pipeline NAME` and `--repo KEY` select pipelines and repositories.

A controller sends only to an `https` URL whose host resolves to public addresses. The local daemon also sends to
`http` and loopback URLs, which is how you try a receiver on your machine. List, inspect and remove subscriptions with
`sparkwing subscriptions list`, `show` and `delete`; see [the command reference](cli-subscriptions.md).

## The message

Each delivery is a `POST` with `Content-Type: application/cloudevents+json`:

```json
{
  "specversion": "1.0",
  "id": "run-6b1f0c2a9d4e/3",
  "source": "urn:sparkwing:34bbf2a10ba08b7f4163168790916971:team:acme",
  "type": "dev.sparkwing.run.finished.v1",
  "subject": "runs/run-6b1f0c2a9d4e",
  "time": "2026-11-16T07:12:09.412Z",
  "datacontenttype": "application/json",
  "sparkwingstateseq": 3,
  "data": {
    "run_id": "run-6b1f0c2a9d4e",
    "run_number": 42,
    "pipeline": "deploy",
    "status": "failed",
    "repository": { "key": "gh:123456", "display": "github.com/acme/app" },
    "git": { "branch": "main", "sha": "4f2c1d0e9b8a7f6e5d4c3b2a1908f7e6d5c4b3a2" },
    "trigger": "push",
    "created_at": "2026-11-16T07:10:01.087Z",
    "started_at": "2026-11-16T07:10:03.215Z",
    "finished_at": "2026-11-16T07:12:09.412Z",
    "lineage": { "root_run_id": null, "parent_run_id": null, "retry_of": null },
    "url": "https://app.sparkwing.example/runs/run-6b1f0c2a9d4e"
  }
}
```

`type` is `dev.sparkwing.run.started.v1` or `dev.sparkwing.run.finished.v1`; a breaking change to `data` gets a new
type. `reason` appears on a finished run that Sparkwing itself ended, such as `queue_timeout`. `repository`, `git` and
`url` are absent when the run has none. The body carries no logs, errors, arguments or environment.

## Delivery

- A response from 200 to 299 is delivered. Anything else, or no response within 10 seconds, is retried with backoff
  from 10 seconds up to one hour, for 24 hours. A `Retry-After` header can lengthen a wait.
- Delivery is at least once. Deduplicate on the event `source` and `id` together; the `webhook-id` header equals `id`.
- Redirects are not followed.
- The local daemon sends only while it runs. A delivery left pending while it is stopped is sent when it starts again.

## Verify the signature

Every delivery carries three headers:

| Header | Value |
|---|---|
| `webhook-id` | The event `id` |
| `webhook-timestamp` | Unix seconds when this attempt was signed |
| `webhook-signature` | `v1,` followed by the base64 HMAC-SHA256 signature; several are separated by spaces |

To verify:

1. Take the part of the secret after `whsec_` and base64-decode it. That is the HMAC key.
2. Compute HMAC-SHA256 over `<webhook-id>.<webhook-timestamp>.<raw body>`.
3. Accept the delivery when any `v1,` entry equals the base64 of that HMAC. Compare in constant time.
4. Refuse a timestamp more than a few minutes from your clock, so a captured delivery cannot be replayed later.

Verify the raw bytes before parsing the JSON. Any Standard Webhooks library does all of this.

## Example: post to Slack

[`examples/slack-run-events`](https://github.com/sparkwing-dev/sparkwing/tree/main/examples/slack-run-events) is a
small Go program with no dependencies. It verifies each delivery, posts one line to a
[Slack incoming webhook](https://api.slack.com/messaging/webhooks), and answers 502 when Slack fails so that Sparkwing
retries:

```text
deploy #42 failed on main https://app.sparkwing.example/runs/run-6b1f0c2a9d4e
```

Run it with the subscription's secret and your Slack webhook URL in the environment:

```bash
export SPARKWING_WEBHOOK_SECRET='<secret printed by sparkwing subscriptions create>'
export SLACK_WEBHOOK_URL='https://hooks.slack.com/services/...'
ADDR=127.0.0.1:8080 go run ./examples/slack-run-events
```

To try it on your machine, subscribe it on the local controller, then run any pipeline:

```bash
sparkwing subscriptions create --url http://127.0.0.1:8080/ --profile=
```

For Sparkwing Cloud, serve it behind HTTPS at a public address and subscribe that URL with `--profile cloud`.
