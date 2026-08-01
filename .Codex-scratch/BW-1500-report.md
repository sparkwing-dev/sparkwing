## Summary
Admission-waiting pipelines now present one coherent lifecycle instead of a stalled orchestration holder plus a healthy node waiter.

## Done
The daemon excludes a zero-cost orchestration parent from stall detection while a child participant waits for admission. Queue JSON keeps both technical rows and adds admission_waiting plus active_waiter_participant_ids. CLI pretty output and the dashboard collapse the technical parent into the active waiting row; raw JSON and plain records retain their backward-compatible fields and hierarchy. The changelog and embedded mirror describe the behavior.

## Unit
TestQueueState_AdmissionWaitingParentIsNotStalled deterministically holds host capacity, queues a child node under an idle semaphore-only parent, waits beyond the stall window, and verifies the parent has no stalled recovery while its child relation is published. TestRenderQueuePretty_ShowsAdmissionWaitingRunOnce verifies one waiting lifecycle and no cancel hint. Queue helper tests verify the dashboard collapse and retain a genuinely idle holder. JSON round-trip coverage pins the additive fields.

## Manual
go test ./internal/wingd -run 'TestQueueState_(AdmissionWaitingParentIsNotStalled|FlagsStalledHolderWithRecoveryCommand)$' -count=10 passed in 2.666s. go test ./internal/opsview -run 'TestRenderQueuePretty_(ShowsAdmissionWaitingRunOnce|UsesDisplayRunID)|TestRenderQueue_JSONRoundTrips' -count=10 passed in 0.280s. node --test --experimental-strip-types src/lib/queue.test.ts passed 34/34 in 0.142s. go test ./pkg/wingwire -count=1 passed in 0.204s. npx tsc --noEmit passed. go test ./pkg/docs -run TestEmbeddedChangelogMatchesRoot -count=1 passed in 0.280s. Sparkwing pre-commit run run-20260801-055952-bb3d passed in 1m31.52s. Branch f526bfc0 is clean and pushed.

## Not done
A live browser walkthrough was not run because the dashboard server was not switched to the ticket branch during concurrent pipeline work.

## Follow-ups
none

## Decisions and tradeoffs
The wire change is additive. Machine-oriented JSON and plain output retain both participant rows for accounting clients, while human pretty and dashboard views collapse only a zero-resource orchestration parent with a distinct active waiter. Genuine idle holders remain visible with their recovery command.

## Outbound Communications
Root architect was notified of the exact successful pre-commit run, commit, and pushed branch.
