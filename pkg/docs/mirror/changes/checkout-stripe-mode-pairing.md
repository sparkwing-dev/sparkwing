### Security

- **billing:** Pair the controller and the checkout service on their Stripe mode
  Every `--billing-url` call carries the controller's `--payment-mode` in the
  `Sparkwing-Payment-Mode` header, and the controller refuses an answer naming
  another mode or none. Checkout, card setup, the card portal, refills and
  refunds are all covered. Successes and final refusals require a matching
  response mode. Background refills and refunds retry an unpaired answer and
  log one `alert=checkout_misconfigured` naming `payment_mode`. Payment pages
  fail without that alert, so its absence does not prove the pairing is correct.
  A refill or refund never completes across modes. The checkout service must send the
  header: deploy the checkout service first, then the controller, both in
  test mode. The cutover runbook in `docs/billing.md` lists the prerequisites,
  including each restricted key's Stripe permissions, switches the checkout
  service before the one-way controller start, verifies the switch, recovers
  a failed check in live mode, and lists the credentials to rotate.
