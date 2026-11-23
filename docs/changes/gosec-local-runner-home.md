### Security

- **runner:** Record why the local runner's state-home existence check is not a path traversal
  gosec's G703 taint rule flagged the check; the path is the absolute home wingd launches the runner with.
