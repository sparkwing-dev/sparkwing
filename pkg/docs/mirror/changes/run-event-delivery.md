### Added

- **controller:** Sparkwing delivers run events to subscriptions
  A run start and every terminal run state queue one delivery per matching
  subscription in the same transaction as the state change. The controller and
  the local daemon POST each delivery with `Content-Type:
  application/cloudevents+json` and Standard Webhooks headers
  (`webhook-id`, `webhook-timestamp`, `webhook-signature`), signed with the
  subscription's `whsec_` secret. Any non-2xx response or transport error is
  retried with jittered backoff from 10 seconds up to one hour for 24 hours;
  the delivery is then marked failed. A `Retry-After` can lengthen a wait up
  to one hour but never shorten it. The `reason` field carries only
  Sparkwing's own codes (`queue_timeout`, `owner_lost`, `launch_rejected`,
  `credit_refused`, `plan_failed`), never text a caller supplied. Redirects are not followed. A controller sends only to HTTPS
  destinations whose resolved addresses are all public and connects to the
  address it checked; the local daemon also sends to `http` and loopback.
  Delivery is at least once: receivers deduplicate on `webhook-id`, which is
  the event `id`. The local daemon sends deliveries only while it runs: one
  left pending while it is stopped is sent when it starts again, or marked
  failed without an attempt once its 24-hour window has closed.
