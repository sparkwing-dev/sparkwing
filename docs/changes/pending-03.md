### Fixed

- **run status:** Retry receipts list only completed reuse, excluding successful source jobs that had to execute again because their results could not be reused.
- **runner:** Child awaits pace controller reads with jittered backoff and reuse the authorized host request while waiting
- **Local onboarding:** the welcome card calls out the initial commit, and the quick start explains run inspection, managed schedules and the daemon-owned dashboard.
- **cli:** Show one admission-wait progress stream with holder charges, held and free capacity, external load and expected clear time. Print updates only for material queue changes at increasing intervals.
- **template verification:** Accept unused failure recovery after a successful deploy. Skipped dry-run bodies and steps still prevent reusable proof.

- **schedules:** Timer-started Linux controllers leave the timer's cgroup when the systemd user manager is available
- **scheduling:** Run status and the run page keep GitHub Actions dispatch receipts, provider links and lost-race counts after award, cancellation and completion.
- **billing:** Refuse checkout and card setup sessions whose Stripe mode differs from the controller's payment mode
- **Security CI:** Identify the updater's private staging files to avoid path traversal false positives.
- **Cloud execution:** Exact-source child runs can reuse their direct parent's verified source and installed pipeline. Changed build inputs log the mismatch and prepare fresh. Corrupt artifacts fail, and host logs separate source preparation stages.
- **deployment:** Smoke observation wakes when all runs finish and takes a final NodeClaim sample before checking claims and logs. Deploy status follows poll every second.

- **controller:** Runner claims succeed when another run waits for the same plan concurrency slot
- **local execution:** Command cancellation waits for cleanup of children in separate process groups before returning a result.

- **Go SDK:** Windows commands honor their own `PATH` and `PATHEXT` and strip engine credentials from child environments. Pipeline tests no longer pull controller dependencies into the SDK module closure.
