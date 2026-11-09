## Admin session output

`admin session` returns a JSON object when stdout is piped. Enrollment reads
the raw session, so add `--output plain` to enrollment scripts.

Before:

```sh
kubectl exec deployment/sparkwing-controller -- sparkwing-controller admin session \
  --operator ACCOUNT_ID --team TEAM --reason "enroll build box" | \
  sparkwing runners add --session-stdin --profile work --runner build-box --repos 'github.com/acme/*'
```

After:

```sh
kubectl exec deployment/sparkwing-controller -- sparkwing-controller admin session \
  --operator ACCOUNT_ID --team TEAM --reason "enroll build box" --output plain | \
  sparkwing runners add --session-stdin --profile work --runner build-box --repos 'github.com/acme/*'
```

For JSON consumers, read the `session` field. Use `--output json` to keep
that shape when stdout is a terminal. `freeze`, `unfreeze` and `grant` also
select pretty output on a terminal and JSON in pipes; scripts that parse
JSON can pin `--output json`. Plain output returns the team slug for
freeze/unfreeze and the grant ID for grant. Help follows the same output
selection and returns a `kind: help` text record in JSON mode.
