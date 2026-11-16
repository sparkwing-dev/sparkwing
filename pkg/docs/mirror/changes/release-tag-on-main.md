### Security

- **release:** Publish only tags reachable from `main`
  The hosted release refuses a tag whose commit is not on the default branch,
  so a pushed tag cannot publish a signed release of unmerged code.
