### Fixed

- **cli:** `sparkwing docs search` ranks guides above the changelog for how-to questions
  Changelog sections now rank below every matching guide or reference section,
  as migration guides already did. Question words such as "how", "do" and "what"
  are dropped from a query that has other words, so "how do I add a secret"
  searches for "add secret".
