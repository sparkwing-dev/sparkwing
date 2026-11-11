### Security

- **delivery:** Queue Land with main's pipeline definition against the candidate checkout
  The trusted submission script pins fetched main and refuses changed registries
  or a target that moves before execution. Pipeline edits still run their own
  affected tests before landing. Queued pushes recheck the main pin after the
  gates finish.
