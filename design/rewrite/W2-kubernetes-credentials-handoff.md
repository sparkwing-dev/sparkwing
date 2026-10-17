# W2 Kubernetes credentials and proxy attribution

<!-- design:begin -->

Implemented the assigned integration slice. No git writes or deployment actions.

## Implementation

The credential invariant is: bytes are read only from a regular target inside
the resolved configured directory, owned by the process user or root, with no
group or other write bit. `fssecure.OpenCredential` resolves links, checks
containment, opens through `os.Root` and validates the opened handle. Kubernetes
`..data` projections and absolute links within the directory are accepted.
Escapes, unsafe permissions, foreign owners and nonregular targets fail.
Windows checks the handle's owner and DACL. Current user, LocalSystem and
Administrators are trusted. Other principals may read but cannot have write,
delete, owner-change or ACL-change rights.

Directory credentials, Stripe keys, runner/launcher tokens, retained here
credentials, local encryption keys, serve tokens, CLI secret input files and
filesystem secret backends use that reader. Standalone paths use their parent
as the boundary. Operator config and private snapshots retain their existing
stricter config policy. Removed ReadPrivate and FileBearer's machine-policy
parameter because machine and service credential files share one invariant.

`--trusted-proxy-cidrs` and `controller.trustedProxyCIDRs` configure the proxy
networks. Empty ignores X-Forwarded-For. Invalid CIDRs fail before startup.
The controller checks the TCP peer, walks forwarded hops from right to left
and selects the first untrusted address for logs and every per-IP limiter.
Malformed trusted hops fall back to the TCP peer. Untrusted peers cannot mint
new client budgets. The existing separate X-Real-IP listener remains supported.

Both mechanisms were requested. A Stripe-only exception would leave other
readers inconsistent, and unconditional forwarded headers would admit spoofing.
No environment variable, dependency, schema change or compatibility shim added.

## Evidence

Tests were written and run red before each reader/proxy slice. The original
Kubernetes test refused the projected token, the escaping-link test returned
outside bytes, and the serve-token, filesystem-backend and CLI-input tests
also demonstrated escaping reads before their fixes.

All Go commands used GOWORK=off, GOFLAGS=-buildvcs=false,
GOCACHE=/tmp/sw-gocache and GOTMPDIR=/tmp/sw-astra-W2/tmp, with at most -p 4.

- Whole-repository `go build -p 4 ./...` and `go vet -p 4 ./...` pass.
- Full credentials, paths, agentconfig, ratelimit, capability and localsecrets
  suites pass. The full chart suite passes. Short fssecure suite passes.
- Focused controller, controller-command, CLI, runner, launcher and filesystem
  secret regressions pass. The real startup readers initialize Stripe and the
  launcher bearer from a two-level Kubernetes projection. The existing launcher
  bootstrap-award projection regression also passes.
- Generated properties cover inside/outside links, relative/absolute targets,
  permission bits, spoofed forwarded prefixes and arbitrary headers from
  untrusted peers. Credential and ratelimit suites pass with -race. Focused
  controller proxy tests also pass with -race.
- Windows ACL tests cross-compile for amd64 and arm64. Native Windows execution
  is not available here. Symlink fixtures require Windows symlink privileges.
- Documentation mirror/check and git diff --check pass. Commentcheck against
  HEAD passes across the changed Go files.

No acceptance topology cell was unskipped. No historical test port was needed.
Existing credential route declaration/coverage regressions pass. This report
claims the assigned slice, not completion of the broader W2 acceptance matrix.

## Contracts and shared files

C2 records the file invariant, Windows policy, removed duplicate API and proxy
attribution/configuration contract. No other contract changes are needed.
Lead-owned handoffs and amendments are untouched. No open design question.

Shared production files outside W2 are internal/fssecure/credential*.go,
internal/credentials/credentials.go, internal/agentconfig/agentconfig.go,
internal/runners/credentials.go, internal/cluster/runner_cli.go,
internal/localsecrets/key.go, internal/paths/servetoken.go,
internal/secretsource/secretsource.go, internal/ratelimit/{ratelimit,proxies}.go,
cmd/sparkwing-controller/main.go and cmd/sparkwing/{runners,here_credentials,secrets}.go.
Their focused tests changed or were added. The full chart's deployment, values,
README and render test changed. Self-hosting docs and their embedded mirror changed.

Removed/replaced 47 production lines, including the duplicate private reader,
service/machine branch and stale policy commentary. Added 266 production lines
including shared files, net +219. W2's session delta is +17/-8, net +9. W2 versus
main is +16,760/-17,435, net -675, within the -250 budget. Counts include new Go
files and exclude tests, testdata, acceptance and contract declarations.

## Friction and native follow-up

- Required commentcheck against main reports the pre-existing untagged comment
  at pkg/store/store.go:107, also present in HEAD. No store file was changed.
- Full fssecure tests report the existing config-writer inventory misses for
  cmd/sparkwing/configure_init.go and internal/userconfig/legacy_migrate.go.
  The short suite and all focused file-security tests pass.
- The full controller-command suite stops at its existing httptest.NewServer
  socket refusal in TestConfigureSecrets_SingleTeamSQLiteCreatesItsKeyOnTheFirstSecret.
  Focused startup/configuration and credential tests pass without sockets.
- The lead must rerun native Windows tests and deploy the candidate to confirm
  EKS startup and set the actual ingress CIDRs. No live credential was read or
  production manifest changed by this worker.

Task scratch under /tmp/sw-astra-W2 is removed at handoff. The shared build cache
is retained.

<!-- design:end -->

STATUS: COMPLETE
