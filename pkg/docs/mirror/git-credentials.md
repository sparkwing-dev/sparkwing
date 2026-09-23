# Team git credentials

A runner without the operator's git cache fetches each run's source straight
from its host. It holds no git credential of its own, so the controller
releases one for each run, at fetch time, to the runner holding the run's
claim.

## Which credential a run gets

`POST /api/v1/runs/{id}/git-credential` resolves the credential in a fixed
order, the same for every run:

1. **The team's GitHub App token**, when the run's repository is on github.com
   and an installation bound to the run's team covers it. The token reads only
   that repository, with `contents: read`, for at most an hour. See
   [GitHub App](github-app.md#source-for-cloud-runners).
2. Otherwise **a git credential the team stored** for the host of the run's
   repository.
3. Otherwise the route answers 404 with `{"error": "no_source_credential"}`
   and a message naming both remedies, and the run fails before anything is
   fetched.

A runner never falls back to credentials of the machine it runs on. The one
exception is a runner its owner fenced with `--allow-repo`, such as a laptop:
when the controller releases nothing, it fetches with the machine's own git
config and credentials, and only from the repositories its list names. See
[local-execution.md](local-execution.md#what-a-laptop-runner-trusts).
