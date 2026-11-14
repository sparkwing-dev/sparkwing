### Added

- **controller:** Sparkwing delivers run events to subscriptions
  A run start and every terminal run state queue one delivery per matching
  subscription in the same transaction as the state change. The controller and
  the local daemon POST each delivery with `Content-Type:
  application/cloudevents+json` and Standard Webhooks headers
  (`webhook-id`, `webhook-timestamp`, `webhook-signature`), signed with the
  subscription's `whsec_` secret. Any non-2xx response or transport error is
  retried with jittered backoff from 10 seconds up to one hour, honouring
  `Retry-After` up to one hour, for 24 hours; the delivery is then marked
  failed. Redirects are not followed. A controller sends only to HTTPS
  destinations whose resolved addresses are all public and connects to the
  address it checked; the local daemon also sends to `http` and loopback.
  Delivery is at least once: receivers deduplicate on `webhook-id`, which is
  the event `id`. Deliveries left pending while the local daemon is stopped are
  sent when it starts again, if their 24-hour window is still open.
