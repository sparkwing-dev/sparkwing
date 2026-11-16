# Slack notifications from run events

A receiver for Sparkwing [run events](../../docs/run-events.md). It verifies each delivery's Standard Webhooks
signature and posts one line per run to a Slack incoming webhook:

```text
deploy #42 failed on main https://app.sparkwing.example/runs/run-6b1f0c2a9d4e
```

It answers 401 to a delivery that does not verify, and 502 when Slack fails so Sparkwing retries.

```bash
sparkwing subscriptions create --url https://hooks.example.com/sparkwing --status failed --profile cloud
export SPARKWING_WEBHOOK_SECRET='<secret printed by sparkwing subscriptions create>'
export SLACK_WEBHOOK_URL='https://hooks.slack.com/services/...'
ADDR=127.0.0.1:8080 go run ./examples/slack-run-events
```

`ADDR` defaults to `127.0.0.1:8080`. Sparkwing Cloud sends only to HTTPS at a public address, so serve it behind a TLS
proxy or on any host that terminates HTTPS.
