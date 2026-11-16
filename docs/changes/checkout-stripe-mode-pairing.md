### Security

- **billing:** Pair the controller and the checkout service on their Stripe mode
  Every `--billing-url` call carries the controller's `--payment-mode` in the
  `Sparkwing-Payment-Mode` header, and the controller refuses an answer naming
  another mode or none. Checkout, card setup, the card portal, refills and
  refunds are all covered. A refused answer is retried and logs one
  `alert=checkout_misconfigured`, which now names `payment_mode`, so a refill
  or refund never completes across modes. The checkout service must send the
  header, so deploy both services together. The cutover runbook in
  `docs/billing.md` switches both services in one step, verifies the switch and
  lists the credentials to rotate before the first live payment.
