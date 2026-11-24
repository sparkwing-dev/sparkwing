### Fixed

- **controller:** Keep the Cloud launcher outside the team request budget
  The deployment's Cloud launcher claims and reconciles every team's Cloud Jobs under one credential, so `--requests-per-token-minute` charged all of that work to one bucket and sustained refusals could delay placement, stop lease renewal, or tear down a Job at activation. Only the operator-provisioned launcher credential is exempt, and only on its claim and sync routes, so a credential that also carries other grants stays budgeted on their routes; team credentials, team-pool launchers and other operator credentials keep their buckets, and the request-rate alarm still counts launcher traffic.

### Docs

- **controller:** Describe `--requests-per-token-minute` as a token bucket per team or authority
  The flag help, chart values and 429 message named a per-token rolling minute. Every credential of one team shares the team's bucket, which holds N requests and refills N a minute.

- **controller:** Describe the per-runner claim and heartbeat budgets by their real key
  The `--claims-per-runner-minute` and `--heartbeats-per-runner-minute` help and the chart values said the budgets were keyed on the token prefix. They are keyed on the caller's team, or on the credential when it has no team or the default team, together with the runner identity: the authenticated holder, else the runner name the request states, else its Here run or machine.
