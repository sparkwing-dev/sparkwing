# Migrating to the next release

Go callers of `pkg/store` update one call site; nothing else changes on a
running install.

## SettleCardPayment returns the matched warning

`Store.SettleCardPayment` gains a middle return: the id of the actionable early
fraud warning that matched the payment or the card that paid, empty when none
did. A caller alerts on it, because a warned payment either repays a pay-now
debt with the team held or grants nothing.

- **Before:** `created, err := st.SettleCardPayment(ctx, payment, now)`
- **After:** `created, warning, err := st.SettleCardPayment(ctx, payment, now)`

A redelivery of a settled payment returns an empty warning, so an alert keyed
on it fires once per payment.
