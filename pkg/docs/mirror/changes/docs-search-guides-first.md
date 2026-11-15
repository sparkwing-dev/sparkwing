### Fixed

- **cli:** `sparkwing docs search` ranks guides above the changelog
  Changelog sections now rank below every matching guide or reference section,
  as migration guides already did, unless the query names a version such as
  `v0.70.0` or the word "breaking". Common words such as "how", "do" and "what"
  are dropped from a query that has other words, so "how do I add a secret"
  searches for "add secret".
