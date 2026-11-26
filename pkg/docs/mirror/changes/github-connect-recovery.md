### Fixed

- **controller + web:** Offer the existing installation when a new GitHub connection cannot finish
  When GitHub has already reported an installation but connecting fails,
  because GitHub could not be reached, the server failed, or the connection
  expired or was already used, the failure page now leads to **Connect an
  existing installation** instead of back to **Team -> GitHub**, where a new
  install stopped on GitHub's settings page. The page says GitHub could not be
  reached only when a call to GitHub failed; Sparkwing's own 503 and 429
  failures say connecting failed on this server. See
  [connecting a team](docs/github-app.md#connecting-a-team).
