### Fixed

- **controller + web:** Offer the existing installation when a new GitHub connection cannot finish
  When the OAuth callback arrives after the signed connect flow expired, or
  `/github/app/complete` fails because GitHub could not be reached, the server
  failed, or the connection was already used or expired, the page now offers
  **Connect an existing installation** instead of only leading back to
  **Team -> GitHub**, where a new install stopped on GitHub's settings page. A
  setup return whose flow cannot be verified offers the same path, conditioned
  on the App being installed. The page says GitHub could not be reached only
  when a request to GitHub failed; Sparkwing's own 503 and 429 failures and an
  App key that cannot sign say connecting failed on this server. See
  [connecting a team](docs/github-app.md#connecting-a-team).
