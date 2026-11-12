### Security

- **delivery:** Bind queued landing checks to the merged owner tree
  Policy, fast checks, review and affected tests run on the merged owner
  checkout and record their checked tree. Verification and push refuse missing,
  failed or mismatched check receipts, so a passing pre-merge snapshot cannot
  authorize a different tree. Queued review uses its pinned main SHA and owner
  checkout instead of relying on a worker clone's remote-tracking refs.
