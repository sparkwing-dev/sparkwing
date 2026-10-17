# Personal runner enrollment CLI contract

`sparkwing runners add --personal` forwards `Personal: true` to the existing `EnrollRunnerRequest`. Omission leaves `Personal: false` and preserves team-runner enrollment. The controller derives account ownership from the authenticated credential. The CLI adds no caller-selected owner field or local authority setting.

Enrollment JSON includes `personal` for both values. Pretty output identifies personal capacity as accepting only the user's runs. Plain output remains the runner name. Credential file creation, service setup and rollback use the existing enrollment path.

The command registry documents the boolean flag and its scope. `docs/self-hosting.md` documents enrollment. Generated CLI references and embedded mirrors require the lead's regeneration pass.

`TestRunnersEnrollmentRequestsPersonalCapacityOnlyWhenSelected` exercises both modes through parsing, enrollment, private config creation and JSON rendering. It asserts the outgoing request's scope and excludes the bearer from output. Existing runner tests cover pool proposals, service installation, revocation and rollback.

This adds seven net production Go lines across `runners.go` and the command registry. Eight production lines were replaced. No backend contract changed in this slice.
