### Changed

- **billing:** Verified Cloud memo hits cost no job compute credits
  The controller refunds all job seconds and the minimum when it served the memo
  for the attempt and its single compute interval lasts at most 60 seconds. Longer
  intervals bill normally. Usage and audit facts remain recorded. User-condition skips,
  body execution, retries, orchestration and storage keep their normal billing.
