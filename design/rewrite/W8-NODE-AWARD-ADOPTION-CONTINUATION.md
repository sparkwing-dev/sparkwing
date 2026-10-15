# W8 local node-award adoption

Continues `69ac5caf4` and `W8-PROBE-CUT-CONTINUATION.md`. The tree started clean.
The local-controller priority remains incomplete. The remaining production
bindings below still prevent local runs and schedules from completing.

## Implemented

Invariant: an SDK process receives authority only from its authenticated node
award, narrowed to the persisted current generation and attempt.

The local native binder now implements W3's `OwnerHost.AdoptClaim` hook. It
validates committed self-claims through C2 authentication and relation checks,
registers the private bearer with the shared masker, and retains a copy for
fresh checks during execution. A run owner's credential cannot substitute for
an absent node award. Generation, subject, holder, tenant and node mismatches
fail before delegation. The binder rereads the node before each process grant.

The parent node claim stays at attempt zero across retries. Delegation narrows
its process binding and data scope to the running attempt without mutating the
parent. The test reaches W3's real `NativeProcesses.Delegate` and
`ProcessAuthorities`, checks the child's node-award parent and attempt, and
checks that revocation ends the child authority. Snapshot and relation services are fakes.

The private award map implements the existing adoption contract. Discarding the
bearer would prevent reusing C2's authenticator for fresh checks. Using the run
owner as the process parent would violate the independent node-award boundary.
No exported interface, setting, timer, wire operation or store opener was added.
The lifecycle source document describes this binding and its evidence limits.

## Real integration findings

A new `rewrite_integration` regression,
`TestLocalNodeAuthorityW8SurvivesStart`, uses the real store, claim transaction,
start transition and C2 authenticator. It fails without sockets:

```
applied attempt=1 credential attempt=0 live binding attempt=1
started claim cannot authenticate: credential is not live
```

W1/W2 must align the claim's attempt-zero lifetime with C2's exact attempt
comparison. Node-host child authority must remain fenced to one positive attempt.
W8's component authenticator fake supplies the claim-spanning behavior required
by A1-18. It does not establish that production C2 implements that behavior.
No foreign production policy or contract was changed.

The unchanged W9 production probes still give these results:

| Probe | Result |
| --- | --- |
| Local controller | Constructs and closes the concrete owner. |
| Local run | Reaches private socket dialing, refused by the sandbox. |
| Local schedules | Refuses the missing checkout/environment/pin registration binding. |

Other integration points remain:

- W2/W1 must implement persisted `CredentialPeer` authentication for the local queue.
- W2/W9 must supply local schedule registration, pin/unpin and tick transport.
  W1 must associate schedules with captured environments. The declared remote
  registration request only accepts a clone URL.
- W3 must supply the complete HTTP-backed `NativeHostStore` adapter for
  foreground local runs and the concrete owner policy binder. W8 consumes the
  declared adoption hook rather than adding another host or policy implementation.
- W3/W1 must supply the ephemeral node-host relation adapter. The store relation
  checker resolves persisted Controller authority rather than the narrowed child.
- W7/W3 must bind authorized bytes, records and output reads into native services.
- W2/W1 still own launcher sync persistence. W8's award and Job correlation is complete.
- Cloud isolation, enforced egress, aggregate scratch, DinD teardown and native
  Windows/Darwin execution still require deployment and platform evidence.

## Assignment status

The 12:35 shared retry-origin validator, A1-38 and the four dead table deletions
remain complete. No finished implementation was replaced. The per-amendment
matrix in `W8-PROBE-CUT-CONTINUATION.md` still applies. This session advances
A1-39/40/48 through node-award adoption and process delegation. No native
acceptance cell or archived T1 port is newly closed.

## Verification

Go checks use GOWORK=off, GOFLAGS=-buildvcs=false, GOCACHE=/tmp/sw-gocache,
and temporary directories under /tmp/sw-astra-W8. Tests use at most -p 4.

- Whole-repository build passes.
- Full localcontroller tests and race checks pass.
- Focused runner, launcher, service, engine, cluster, CLI composition and W8
  acceptance checks pass.
- Node-award, foreign-adoption and process-delegation fuzz properties pass.
  The final bounded runs execute 30,881 generated cases in total.
- Scoped vet, commentcheck and diff whitespace checks pass.
- The two integration failures above remain visible. The new real-store
  attempt-lifetime regression is retained behind `rewrite_integration`.

## Volume and scope

This session adds 138 and removes 26 production Go lines in W8, **net +112**.
The removed lines are the superseded delegation closure. No legacy file was
newly deleted. Production outside W8 changes by **0 lines**.

Against `main`, W8 adds 10,164 and removes 7,051 production Go lines:
**net +3,113**, or **5,413 above the -2,300 budget**. Counts use --no-renames
and exclude tests, testdata, acceptance and contract declaration paths.

The new 112 lines provide authenticated private award retention and per-attempt
narrowing that the local native binder lacked. The remaining package overage
justifications are in `W8-PROBE-CUT-CONTINUATION.md` and `W8-RETRY-AND-SIZE.md`.
They cover the engine/version requirements added by decisions 101-103 and the
shared runner, native service and local-controller obligations. The budget
remains unmet. This is not a waiver.

No `cmd/sparkwing` composition file, foreign production caller,
`internal/orchestrator` file or lead-owned contract ledger changed. The only
shared-path addition is the W8 integration regression at
`tests/acceptance/local_node_authority_w8_test.go`. No API was removed, so no
caller migration was needed.

Friction remains the missing foreign bindings, the socket sandbox and the
reproduced claim-attempt authentication failure. No git write, Sparkwing binary,
pipeline, deployment or release ran. Worker scratch is removed after all checks
join. The shared build cache is retained.

STATUS: CONTINUE
