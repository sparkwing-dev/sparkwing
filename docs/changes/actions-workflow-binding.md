### Security

- **controller:** Require GitHub Actions fallback workflows to match the default branch
  Dispatch refuses missing or different workflow files and names the file to sync.
  Receipt exchange rechecks the workflow and requires GitHub's reported workflow
  SHA to match the verified dispatch commit before granting hosted runner trust.
