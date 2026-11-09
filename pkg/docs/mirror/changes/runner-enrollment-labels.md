### Security

- **runner:** Bind advertised labels to admin enrollment
  `runners add --label` records the allowed set. Extra claim labels are refused
  with an admin update command. `sparkwing-controller admin runner-labels` views
  and replaces the set. Existing runners keep their last stored labels on upgrade.
