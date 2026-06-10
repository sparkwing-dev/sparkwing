# Test rules

Conventions for tests in the sparkwing tree. Edit this file to change what the tests reviewer enforces. (Coverage-adequacy judgment lives in the reviewer's mandate, not here.)

1. **Test names describe what is verified, not why the test was added.** `TestRun_RejectsUnknownPipeline`, not `TestRun_AddedForIMP022`. A test name containing a ticket ID, a date, or a "fixes X" rationale is a violation.

2. **No block-comment essays atop test files.** A one-line note above a non-obvious assertion is fine; a multi-paragraph preamble explaining the backstory is a violation.

3. **Fixture names describe what they exercise, not tickets.** Pipeline IDs, run IDs, and other fixtures registered in tests are named for the behavior under test (`step-range-validate`), never after a ticket (`imp007-validate`).

4. **New non-trivial logic ships with tests.** A change that adds real behavior with no accompanying test covering it is a violation — at minimum the happy path plus one failure or edge path.

5. **Assert behavior, not implementation.** Tests check observable outputs, returned errors, and side effects — not private internals or incidental ordering that will churn without the behavior changing.

6. **Tests are deterministic.** No reliance on the real wall clock, network, or randomness unless it is injected and controlled. Prefer table-driven tests when covering several cases of the same behavior.
