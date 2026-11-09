### Security

- **runner:** Bind advertised labels to admin enrollment
  `runners add --label` records the allowed set. Extra claim labels are refused
  with an admin update command. `sparkwing-controller admin runner-labels` views
  and replaces the set. Existing runners keep their last stored labels on upgrade.
  Browser approval shows the full allowed set before consent. Labels are bounded
  to 32 entries of 1 to 128 ASCII characters, including `host=...` and `tool:...`.

### Fixed

- **runs:** Keep run details and lists readable after a runner label is removed
  Placement refusal remains visible and prevents new execution, including in
  already-owned runs.
