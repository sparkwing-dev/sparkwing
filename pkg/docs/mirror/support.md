# Support

## Where to ask

| You need | Go to |
| --- | --- |
| Help with Sparkwing Cloud: a run stuck on Cloud, billing, credits, your team or account | Email support@sparkwing.dev |
| To delete your account, a team, or specific runs | Email support@sparkwing.dev from the address on the account. A team owner can delete a team without asking; see [deleting a team or account](auth.md#deleting-a-team-or-account). |
| To report a bug or ask for a feature, Cloud or self-hosted | Open an issue at [github.com/sparkwing-dev/sparkwing/issues](https://github.com/sparkwing-dev/sparkwing/issues) |
| To report a suspected vulnerability | Follow the [security policy](https://github.com/sparkwing-dev/sparkwing/blob/main/SECURITY.md). Do not open a public issue. |

A self-hosted controller names its own operator's contact in the dashboard's
Help panel and in limit messages.

Once a deletion runs, the controller removes the data from the live service
within a few minutes.
Credit and payment records and the operator's audit log stay. On Sparkwing
Cloud, copies in database backups and old object versions remain for about 7
days, typically gone within 8: S3 rounds each expiry up to the next midnight
UTC and removes expired versions asynchronously. See
[backup and restore](backup-restore.md) for the details.

## What to include

- `sparkwing version` output.
- Your team slug.
- The run ID, from `sparkwing runs list`, for any problem with a run.
- The exact command you ran and its complete output, including the error.
- When it happened, with your time zone.
- Where the job ran: on your machine, on your own runner, or on Sparkwing Cloud.
- What you expected to happen.

Never send a secret value, a token, or a private key. Sparkwing redacts
registered secrets from logs, but check pasted output before sending it.
Support never asks for a token; revoke any you sent by mistake with
`sparkwing tokens revoke`.

## Response times

Support is staffed from US Mountain Time.

- Every email gets a first reply within one business day, usually sooner.
- A report that Sparkwing Cloud is down or that Cloud runs are failing is
  acknowledged the same day between 08:00 and 20:00 Mountain Time.
- An incident that affects your team is announced by email to its owners
  and in a pinned issue labeled `incident` on the GitHub repository, with
  updates until it is resolved.
- Bugs reported as GitHub issues are triaged in public. A fix ships in a
  patch release; `sparkwing update` installs it.
